package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var errImageTaskResponseTooLarge = errors.New("image task upstream response exceeds configured limit")

// GatewayImageTaskExecutor deliberately reuses the existing synchronous image
// handlers. This keeps account scheduling, failover, moderation and usage
// attribution identical, while a private Gin context prevents a background job
// from inheriting the original client connection or API key text.
type GatewayImageTaskExecutor struct {
	gateway  *OpenAIGatewayHandler
	accounts service.AccountRepository
	apiKeys  *service.APIKeyService
	cfg      service.ImageTaskConfig
	client   *http.Client
}

func NewGatewayImageTaskExecutor(gateway *OpenAIGatewayHandler, accounts service.AccountRepository, apiKeys *service.APIKeyService, cfg service.ImageTaskConfig) service.ImageTaskExecutor {
	return &GatewayImageTaskExecutor{
		gateway: gateway, accounts: accounts, apiKeys: apiKeys, cfg: cfg,
		client: newImageTaskDownloadClient(cfg.Workers),
	}
}

func (e *GatewayImageTaskExecutor) Execute(ctx context.Context, task *service.ImageTask, requestPayload []byte) (service.ImageTaskExecutionResult, error) {
	if e == nil || e.gateway == nil || task == nil {
		return service.ImageTaskExecutionResult{}, errors.New("image task executor is unavailable")
	}
	policy, err := service.DecodeImageTaskPolicy(task.PolicySnapshot)
	if err != nil {
		return service.ImageTaskExecutionResult{}, err
	}
	apiKey, err := e.revalidateKey(ctx, task, policy)
	if err != nil {
		return service.ImageTaskExecutionResult{}, err
	}
	var billing service.ImageTaskBillingSnapshot
	if len(task.BillingSnapshot) == 0 || json.Unmarshal(task.BillingSnapshot, &billing) != nil || billing.CostFor(1) == nil {
		return service.ImageTaskExecutionResult{}, errors.New("image task billing snapshot is invalid")
	}

	requestCtx := task.UsageContext(ctx)
	if len(policy.KeywordExcludedAccountIDs) > 0 {
		excluded := make(map[int64]struct{}, len(policy.KeywordExcludedAccountIDs))
		for _, accountID := range policy.KeywordExcludedAccountIDs {
			if accountID > 0 {
				excluded[accountID] = struct{}{}
			}
		}
		requestCtx = service.ContextWithKeywordProtectedAccountIDs(requestCtx, excluded)
	}
	if policy.BillingExempt {
		requestCtx = service.ContextWithGatewayLiteLocalAPIKey(requestCtx)
	}
	if task.RatePolicyAudit != nil && task.RatePolicyAudit.Valid() {
		requestCtx = service.ContextWithEffectiveRatePolicy(requestCtx, task.RatePolicyAudit.Clone())
	}
	requestCtx, capture := service.ContextWithImageTaskExecution(requestCtx, billing)
	if policy.TargetPlatform != "" || policy.RequestedModel != "" || policy.RouteSource != "" {
		upstreamModel := policy.Channel.ChannelMappedModel
		if upstreamModel == "" {
			upstreamModel = policy.RequestedModel
		}
		requestCtx = service.WithCompositeRouteDecision(requestCtx, service.CompositeRouteDecision{
			Matched: true, Source: policy.RouteSource, GroupID: policy.Group.ID,
			PublicModel: policy.RequestedModel, TargetPlatform: policy.TargetPlatform,
			UpstreamModel: upstreamModel, Endpoint: task.Endpoint,
		})
	}

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, task.Endpoint, bytes.NewReader(requestPayload))
	if err != nil {
		return service.ImageTaskExecutionResult{}, err
	}
	req.Header.Set("Content-Type", firstNonEmpty(task.ContentType, "application/json"))
	// Compatible upstreams that honor Idempotency-Key can return the original
	// result if the gateway crashes after generation but before persisting the
	// terminal task row.
	req.Header.Set("Idempotency-Key", "image-task:"+task.ID)
	if policy.UserAgent != "" {
		req.Header.Set("User-Agent", policy.UserAgent)
	}
	if policy.SessionID != "" {
		req.Header.Set("X-Session-ID", policy.SessionID)
	}
	if policy.IPAddress != "" {
		req.RemoteAddr = net.JoinHostPort(policy.IPAddress, "0")
	}

	maxResponseBytes := e.cfg.MaxTaskArtifactBytes*2 + (2 << 20)
	writer := newBoundedImageTaskWriter(maxResponseBytes)
	c, _ := gin.CreateTestContext(writer)
	c.Request = req
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: policy.User.ID, Concurrency: policy.User.Concurrency})
	if subscription := policy.SubscriptionValue(); subscription != nil {
		c.Set(string(middleware.ContextKeySubscription), subscription)
	}

	platform := strings.TrimSpace(policy.TargetPlatform)
	if platform == "" {
		platform = strings.TrimSpace(policy.Group.Platform)
	}
	switch platform {
	case service.PlatformOpenAI:
		e.gateway.Images(c)
	case service.PlatformGrok:
		e.gateway.GrokImages(c)
	default:
		return service.ImageTaskExecutionResult{}, fmt.Errorf("image task platform %q is unsupported", platform)
	}
	if writer.overflow {
		return service.ImageTaskExecutionResult{}, errImageTaskResponseTooLarge
	}
	if writer.Status() < http.StatusOK || writer.Status() >= http.StatusMultipleChoices {
		return service.ImageTaskExecutionResult{}, fmt.Errorf("image upstream returned status %d: %s", writer.Status(), safeImageTaskUpstreamError(writer.body.Bytes()))
	}

	artifacts, err := e.extractArtifacts(ctx, writer.body.Bytes())
	if err != nil {
		return service.ImageTaskExecutionResult{}, err
	}
	usageWaitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	actualCents, imageCount, _, _ := capture.Wait(usageWaitCtx)
	cancel()
	usageSnapshot := capture.UsageSnapshot()
	if usageSnapshot != nil {
		if payload, marshalErr := usageSnapshot.Marshal(); marshalErr == nil {
			task.ExecutionSnapshot = payload
		}
	}
	if imageCount <= 0 {
		imageCount = len(artifacts)
	}
	if actualCents <= 0 {
		if cost := billing.CostFor(imageCount); cost != nil {
			// This is display/audit data only. Sub-cent accumulation, Redis lease
			// adjustment and RecordUsage all happen later in Settle, after objects
			// are durable.
			actualCents = int64(math.Ceil(cost.ActualCost*100 - 1e-9))
		}
	}
	// A successful upstream image must never be generated again solely because
	// usage persistence timed out. Persist artifacts and the safe usage snapshot;
	// Settle will retry only RecordUsage with the same financial request ID.
	return service.ImageTaskExecutionResult{
		Artifacts: artifacts, ActualCents: maxImageTaskInt64(actualCents, 0),
		// The synchronous handler only captures replayable usage facts for an
		// async execution. RecordUsage is deliberately deferred until the worker
		// has durably stored every artifact.
		BillingSettled: false,
	}, nil
}

func (e *GatewayImageTaskExecutor) revalidateKey(ctx context.Context, task *service.ImageTask, policy *service.ImageTaskPolicySnapshot) (*service.APIKey, error) {
	if policy == nil || task == nil || task.UserID != policy.User.ID || policy.Group.ID <= 0 {
		return nil, errors.New("image task ownership snapshot is invalid")
	}
	if strings.TrimSpace(task.GatewayKeyID) != "" {
		control := gatewaylite.DefaultControlPlaneClientRef()
		if control == nil || !control.Configured() {
			return nil, errors.New("control plane is unavailable for image task authorization")
		}
		resolved, err := control.ResolveKey(ctx, gatewaylite.ResolveKeyRequest{
			KeyID: task.GatewayKeyID, Region: task.Region, GatewayCode: task.GatewayID, SkipUsageTouch: true,
		})
		if err != nil || resolved == nil || !resolved.OK || !resolved.Key.Active() {
			return nil, errors.New("image task API key is no longer active")
		}
		if resolved.Key.ExpiresAt > 0 && time.Now().Unix() >= resolved.Key.ExpiresAt {
			return nil, errors.New("image task API key has expired")
		}
		if resolved.Key.UserID != task.UserID || resolved.Key.GroupID != policy.Group.ID || !imageTaskModelAllowed(task.Model, resolved.Key.AllowedModels) {
			return nil, errors.New("image task authorization or model policy changed")
		}
		if resolved.Key.Group == nil || resolved.Key.Group.Status != service.StatusActive || !resolved.Key.Group.AllowImageGeneration {
			return nil, errors.New("image task group is no longer available")
		}
		if !imageTaskGatewayTierAllowed(resolved.Key.OrganizationID, resolved.Key.Tier, resolved.Key.Group.MinimumTier, resolved.Key.Group.EffectiveRequiredTier) {
			return nil, errors.New("image task user tier no longer satisfies the group requirement")
		}
		return policy.APIKey(), nil
	}
	if e.apiKeys == nil || task.APIKeyID <= 0 {
		return nil, errors.New("local image task API key is unavailable")
	}
	latest, err := e.apiKeys.GetByID(ctx, task.APIKeyID)
	if err != nil || latest == nil || latest.Status != service.StatusAPIKeyActive || latest.User == nil || latest.Group == nil {
		return nil, errors.New("image task API key is no longer active")
	}
	if latest.User.ID != task.UserID || latest.User.Status != service.StatusActive || latest.Group.ID != policy.Group.ID || latest.Group.Status != service.StatusActive || !latest.Group.AllowImageGeneration {
		return nil, errors.New("image task user or group is no longer available")
	}
	if !imageTaskModelAllowed(task.Model, latest.AllowedModels) {
		return nil, errors.New("image task model is no longer allowed by the API key")
	}
	if !service.GroupTierAllowedForUser(latest.User, latest.Group) {
		return nil, errors.New("image task user tier no longer satisfies the group requirement")
	}
	if latest.ExpiresAt != nil && !latest.ExpiresAt.After(time.Now()) {
		return nil, errors.New("image task API key has expired")
	}
	return policy.APIKey(), nil
}

func (e *GatewayImageTaskExecutor) Settle(ctx context.Context, task *service.ImageTask) error {
	if e == nil || e.gateway == nil || task == nil {
		return errors.New("image task settlement executor is unavailable")
	}
	usage, err := service.DecodeImageTaskUsage(task.ExecutionSnapshot)
	if err != nil {
		return err
	}
	policy, err := service.DecodeImageTaskPolicy(task.PolicySnapshot)
	if err != nil {
		return err
	}
	var billing service.ImageTaskBillingSnapshot
	if len(task.BillingSnapshot) == 0 || json.Unmarshal(task.BillingSnapshot, &billing) != nil {
		return errors.New("image task billing snapshot is invalid")
	}
	result := usage.ResultValue()
	cost := billing.CostFor(result.ImageCount)
	if cost == nil {
		return errors.New("image task frozen settlement price is invalid")
	}
	account := usage.AccountValue()
	if e.accounts != nil {
		if current, loadErr := e.accounts.GetByID(ctx, usage.Account.ID); loadErr == nil && current != nil {
			account = current
		}
	}
	if account == nil {
		return errors.New("image task account snapshot is unavailable")
	}

	usageCtx := task.UsageContext(ctx)
	if policy.BillingExempt {
		usageCtx = service.ContextWithGatewayLiteLocalAPIKey(usageCtx)
	}
	actualCents := task.ActualCents
	if actualCents <= 0 {
		actualCents = int64(math.Ceil(cost.ActualCost*100 - 1e-9))
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(usageCtx); ok && meta.AsyncSettlement {
		if meta.BillingSettlementMode == gatewaylite.BillingSettlementModeIdempotentV2 {
			requestID, _ := gatewaylite.CanonicalRequestIDFromContext(usageCtx)
			prepared, prepareErr := gatewaylite.PrepareActualCostSettlement(usageCtx, requestID, cost.ActualCost)
			if prepareErr != nil {
				return prepareErr
			}
			actualCents = prepared
		}
		if err := gatewaylite.EnsureCommittedUsageCapacity(usageCtx, actualCents); err != nil {
			return err
		}
		usageCtx = gatewaylite.ContextWithActualCentsOverride(usageCtx, actualCents)
	}
	rate := billing.RateMultiplier
	imageRate := billing.ImageRateMultiplier
	apiKey := policy.APIKey()
	return e.gateway.gatewayService.RecordUsage(usageCtx, &service.OpenAIRecordUsageInput{
		Result: result, APIKey: apiKey, User: apiKey.User, Account: account,
		Subscription: policy.SubscriptionValue(), InboundEndpoint: usage.InboundEndpoint,
		UpstreamEndpoint: usage.UpstreamEndpoint, UserAgent: usage.UserAgent, IPAddress: usage.IPAddress,
		SessionID: usage.SessionID, RequestPayloadHash: usage.RequestPayloadHash,
		APIKeyService: e.gateway.apiKeyService, QuotaPlatform: usage.QuotaPlatform,
		CostOverride: cost, RateMultiplierOverride: &rate, ImageRateMultiplierOverride: &imageRate,
		ChannelUsageFields: usage.ChannelValue(),
	})
}

func (e *GatewayImageTaskExecutor) extractArtifacts(ctx context.Context, payload []byte) ([]service.ImageTaskExecutionArtifact, error) {
	if !gjson.ValidBytes(payload) {
		return nil, errors.New("image upstream response is not valid JSON")
	}
	values := collectImageTaskOutputValues(payload)
	if len(values) == 0 || len(values) > e.cfg.MaxImagesPerTask {
		return nil, errors.New("image upstream response contains no valid artifacts")
	}
	artifacts := make([]service.ImageTaskExecutionArtifact, 0, len(values))
	for index, value := range values {
		var data []byte
		var contentType string
		var err error
		switch value.kind {
		case "base64":
			data, contentType, err = decodeImageTaskBase64(value.value, e.cfg.MaxArtifactBytes)
		case "url":
			data, contentType, err = e.downloadArtifact(ctx, value.value)
		default:
			err = errors.New("unsupported image artifact encoding")
		}
		if err != nil {
			closeImageTaskArtifacts(artifacts)
			return nil, err
		}
		if contentType == "" {
			contentType = http.DetectContentType(data)
		}
		if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
			closeImageTaskArtifacts(artifacts)
			return nil, errors.New("image artifact has an invalid content type")
		}
		extensions, _ := mime.ExtensionsByType(contentType)
		extension := ".bin"
		if len(extensions) > 0 {
			extension = extensions[0]
		}
		artifacts = append(artifacts, service.ImageTaskExecutionArtifact{
			FileName: fmt.Sprintf("image-%02d%s", index+1, extension), ContentType: contentType,
			Reader: io.NopCloser(bytes.NewReader(data)),
		})
	}
	return artifacts, nil
}

type imageTaskOutputValue struct{ kind, value string }

func collectImageTaskOutputValues(payload []byte) []imageTaskOutputValue {
	var root map[string]any
	if json.Unmarshal(payload, &root) != nil {
		return nil
	}
	values := make([]imageTaskOutputValue, 0, 4)
	for _, key := range []string{"data", "output", "images"} {
		collectImageTaskOutputNode(root[key], &values, 0)
	}
	return values
}

func collectImageTaskOutputNode(node any, values *[]imageTaskOutputValue, depth int) {
	if depth > 4 || node == nil {
		return
	}
	switch value := node.(type) {
	case []any:
		for _, item := range value {
			collectImageTaskOutputNode(item, values, depth+1)
		}
	case map[string]any:
		if raw, ok := value["b64_json"].(string); ok && strings.TrimSpace(raw) != "" {
			*values = append(*values, imageTaskOutputValue{kind: "base64", value: raw})
			return
		}
		if raw, ok := value["url"].(string); ok && strings.TrimSpace(raw) != "" {
			*values = append(*values, imageTaskOutputValue{kind: "url", value: raw})
			return
		}
		for _, key := range []string{"data", "output", "images", "image"} {
			collectImageTaskOutputNode(value[key], values, depth+1)
		}
	}
}

func decodeImageTaskBase64(raw string, limit int64) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	contentType := ""
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma <= 5 || !strings.Contains(strings.ToLower(raw[:comma]), ";base64") {
			return nil, "", errors.New("invalid image data URL")
		}
		contentType = strings.TrimSpace(strings.Split(strings.TrimPrefix(raw[:comma], "data:"), ";")[0])
		raw = raw[comma+1:]
	}
	if int64(base64.StdEncoding.DecodedLen(len(raw))) > limit {
		return nil, "", errors.New("base64 image exceeds configured size limit")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || int64(len(decoded)) > limit {
		return nil, "", errors.New("invalid or oversized base64 image")
	}
	return decoded, contentType, nil
}

func (e *GatewayImageTaskExecutor) downloadArtifact(ctx context.Context, rawURL string) ([]byte, string, error) {
	validated, err := urlvalidator.ValidateHTTPSURL(rawURL, urlvalidator.ValidationOptions{})
	if err != nil {
		return nil, "", fmt.Errorf("image URL rejected: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, validated, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("image download returned status %d", resp.StatusCode)
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	reader := io.LimitReader(resp.Body, e.cfg.MaxArtifactBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > e.cfg.MaxArtifactBytes {
		return nil, "", errors.New("downloaded image exceeds configured size limit")
	}
	return data, contentType, nil
}

func newImageTaskDownloadClient(workers int) *http.Client {
	if workers < 1 {
		workers = 1
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, ForceAttemptHTTP2: true,
		MaxIdleConns: workers * 4, MaxIdleConnsPerHost: workers * 2, MaxConnsPerHost: workers * 2,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("resolve image host: %w", err)
		}
		for _, resolved := range ips {
			if imageTaskIPBlocked(resolved.IP) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		}
		return nil, errors.New("image host resolved only to blocked addresses")
	}
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many image download redirects")
		}
		if req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil {
			return errors.New("unsafe image download redirect")
		}
		_, err := urlvalidator.ValidateHTTPSURL(req.URL.String(), urlvalidator.ValidationOptions{})
		return err
	}
	return client
}

func imageTaskIPBlocked(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 0 || (v4[0] == 100 && v4[1]&0xc0 == 0x40)
	}
	return false
}

func imageTaskModelAllowed(model string, patterns []string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" || len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "*" || pattern == model {
			return true
		}
		if !strings.Contains(pattern, "*") {
			continue
		}
		parts, cursor, matched := strings.Split(pattern, "*"), 0, true
		for _, part := range parts {
			if part == "" {
				continue
			}
			position := strings.Index(model[cursor:], part)
			if position < 0 {
				matched = false
				break
			}
			cursor += position + len(part)
		}
		if matched {
			return true
		}
	}
	return false
}

func imageTaskGatewayTierAllowed(organizationID int64, userTier int, minimumTier *int, effectiveRequiredTier int) bool {
	return service.GroupTierAllowedForUser(&service.User{OrganizationID: organizationID, Tier: userTier}, &service.Group{
		MinimumTier: minimumTier, EffectiveRequiredTier: effectiveRequiredTier,
	})
}

func safeImageTaskUpstreamError(payload []byte) string {
	message := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(payload, "message").String())
	}
	if message == "" {
		message = http.StatusText(http.StatusBadGateway)
	}
	if len(message) > 240 {
		message = message[:240]
	}
	return message
}

func closeImageTaskArtifacts(items []service.ImageTaskExecutionArtifact) {
	for _, item := range items {
		if item.Reader != nil {
			_ = item.Reader.Close()
		}
	}
}

type boundedImageTaskWriter struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	limit    int64
	written  bool
	overflow bool
}

func newBoundedImageTaskWriter(limit int64) *boundedImageTaskWriter {
	return &boundedImageTaskWriter{header: make(http.Header), status: http.StatusOK, limit: limit}
}

func (w *boundedImageTaskWriter) Header() http.Header { return w.header }
func (w *boundedImageTaskWriter) WriteHeader(code int) {
	if w.written {
		return
	}
	w.status = code
	w.written = true
}
func (w *boundedImageTaskWriter) WriteHeaderNow() { w.WriteHeader(w.status) }
func (w *boundedImageTaskWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	if int64(w.body.Len()+len(data)) > w.limit {
		w.overflow = true
		return 0, errImageTaskResponseTooLarge
	}
	return w.body.Write(data)
}
func (w *boundedImageTaskWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }
func (w *boundedImageTaskWriter) Status() int                          { return w.status }
func (w *boundedImageTaskWriter) Size() int                            { return w.body.Len() }
func (w *boundedImageTaskWriter) Written() bool                        { return w.written }
func (w *boundedImageTaskWriter) Flush()                               {}
func (w *boundedImageTaskWriter) CloseNotify() <-chan bool {
	ch := make(chan bool)
	return ch
}
func (w *boundedImageTaskWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("image task writer does not support hijacking")
}
func (w *boundedImageTaskWriter) Pusher() http.Pusher { return nil }

func maxImageTaskInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

var _ gin.ResponseWriter = (*boundedImageTaskWriter)(nil)
