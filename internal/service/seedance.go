package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	SeedanceEndpointCreate           GrokMediaEndpoint        = "seedance_create"
	SeedanceEndpointStatus           GrokMediaEndpoint        = "seedance_status"
	SeedanceEndpointDelete           GrokMediaEndpoint        = "seedance_delete"
	OpenAIEndpointCapabilitySeedance OpenAIEndpointCapability = "seedance"
)

func (e GrokMediaEndpoint) IsSeedance() bool {
	return e == SeedanceEndpointCreate || e == SeedanceEndpointStatus || e == SeedanceEndpointDelete
}

// MediaTaskIdentity 显式区分本地密钥与主站密钥，禁止 ID=0 的远程身份退化成本地身份。
// 企业付款主体也参加归属匹配，同员工的个人/企业任务不能互相读取。
type MediaTaskIdentity struct {
	Kind               string `json:"kind"`
	UserID             int64  `json:"user_id"`
	APIKeyID           int64  `json:"api_key_id,omitempty"`
	GatewayKeyID       string `json:"gateway_key_id,omitempty"`
	GatewayID          string `json:"gateway_id,omitempty"`
	OrganizationID     int64  `json:"organization_id,omitempty"`
	BillingSubjectType string `json:"billing_subject_type,omitempty"`
	BillingSubjectID   int64  `json:"billing_subject_id,omitempty"`
}

func ResolveMediaTaskIdentity(ctx context.Context, key *APIKey, userID int64) (MediaTaskIdentity, error) {
	if key == nil || userID <= 0 || (key.UserID > 0 && key.UserID != userID) {
		return MediaTaskIdentity{}, fmt.Errorf("media task identity is invalid")
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		if meta.UserID != userID || strings.TrimSpace(meta.KeyID) == "" || strings.TrimSpace(meta.GatewayID) == "" {
			return MediaTaskIdentity{}, fmt.Errorf("remote media task identity is invalid")
		}
		subjectType, subjectID := strings.TrimSpace(meta.BillingSubjectType), meta.BillingSubjectID
		if subjectType == "" && meta.OrganizationID == 0 {
			subjectType, subjectID = "user", userID
		}
		return MediaTaskIdentity{Kind: "remote", UserID: userID, GatewayKeyID: strings.TrimSpace(meta.KeyID),
			GatewayID: strings.TrimSpace(meta.GatewayID), OrganizationID: meta.OrganizationID,
			BillingSubjectType: subjectType, BillingSubjectID: subjectID}, nil
	}
	if key.ID <= 0 {
		return MediaTaskIdentity{}, fmt.Errorf("local media task identity is invalid")
	}
	return MediaTaskIdentity{Kind: "local", UserID: userID, APIKeyID: key.ID}, nil
}

func (owner MediaTaskIdentity) pendingKey(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || owner.UserID <= 0 {
		return ""
	}
	if owner.Kind == "local" && owner.APIKeyID > 0 {
		// 保留已有本地任务的键，不把历史本地任务错误归到远程身份。
		return grokVideoPendingBillingKey(taskID, owner.UserID, owner.APIKeyID)
	}
	if owner.Kind != "remote" || owner.GatewayKeyID == "" || owner.GatewayID == "" {
		return ""
	}
	identity, _ := json.Marshal(owner)
	return fmt.Sprintf("remote:%x:%s", sha256.Sum256(identity), taskID)
}

func (s *OpenAIGatewayService) LoadSeedancePendingBilling(ctx context.Context, taskID string, owner MediaTaskIdentity) (*GrokVideoPendingBilling, error) {
	if s == nil || s.cache == nil || owner.pendingKey(taskID) == "" {
		return nil, fmt.Errorf("seedance task ownership cache is unavailable or identity is invalid")
	}
	payload, err := s.cache.GetGrokVideoPendingBilling(ctx, owner.pendingKey(taskID))
	if err != nil || len(payload) == 0 {
		return nil, err
	}
	var pending GrokVideoPendingBilling
	if err := json.Unmarshal(payload, &pending); err != nil {
		return nil, err
	}
	if pending.Owner != nil && *pending.Owner != owner {
		return nil, nil
	}
	if owner.Kind == "remote" && pending.Owner == nil {
		return nil, nil
	}
	return &pending, nil
}

func (s *OpenAIGatewayService) StoreSeedancePendingBilling(ctx context.Context, taskID string, owner MediaTaskIdentity, pending GrokVideoPendingBilling) error {
	if s == nil || s.cache == nil || owner.pendingKey(taskID) == "" {
		return fmt.Errorf("seedance task ownership cache is unavailable or identity is invalid")
	}
	pending.Owner = &owner
	pending.Model = strings.TrimSpace(pending.Model)
	pending.BillingModel = strings.TrimSpace(pending.BillingModel)
	pending.UpstreamModel = strings.TrimSpace(pending.UpstreamModel)
	pending.OriginalModel = strings.TrimSpace(pending.OriginalModel)
	if pending.CreatedAt == "" {
		pending.CreatedAt = GrokVideoPendingCreatedAtNow()
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		// 只持久不可变标量。Reservation 是当前 HTTP 请求的共享锁对象，不能跨请求恢复。
		meta.Reservation = nil
		meta.ActualCentsOverride = nil
		meta.AsyncSettlement = false
		meta.AsyncPersistenceUncertain = false
		if meta.Funding != nil {
			// Funding 包含进程内锁与私有字段，持久化必须使用不可变授权副本。
			funding := meta.Funding.Snapshot()
			pending.FundingSnapshot = &funding
			meta.Funding = nil
		}
		pending.RequestMeta = &meta
	}
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		policy = policy.Clone()
		pending.RatePolicyAudit = &policy
	}
	if pending.ExecutionPricing != nil {
		if _, err := pending.ExecutionPricing.ApplyToCostInput(CostInput{Model: firstSeedanceBillingModel(pending)}); err != nil {
			return err
		}
		pending.ExecutionPricing = pending.ExecutionPricing.Clone()
		// 同一执行快照中的倍率审计为准，不能被免费轮询的上下文覆盖。
		if policy := pending.ExecutionPricing.RatePolicyAudit; policy != nil {
			copy := policy.Clone()
			pending.RatePolicyAudit = &copy
		}
	}
	payload, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	setter, ok := s.cache.(interface {
		SetGrokVideoPendingBilling(context.Context, string, []byte, time.Duration) error
	})
	if !ok {
		return fmt.Errorf("seedance task ownership store is unavailable")
	}
	return setter.SetGrokVideoPendingBilling(ctx, owner.pendingKey(taskID), payload, grokVideoPendingBillingTTL(s.cfg))
}

// SeedanceBillingStore 单独保存有所有权的短期认领与已结算状态，避免扩大通用缓存接口。
type SeedanceBillingStore interface {
	ClaimSeedanceBilling(context.Context, string, string, time.Duration) (bool, error)
	ConfirmSeedanceBilling(context.Context, string, string, time.Duration) (bool, error)
	ReleaseSeedanceBilling(context.Context, string, string) (bool, error)
}

const seedanceBillingClaimTTL = 2 * time.Minute

func (s *OpenAIGatewayService) ClaimSeedanceBilling(ctx context.Context, taskID string, owner MediaTaskIdentity) (string, error) {
	store, err := s.seedanceBillingStore(taskID, owner)
	if err != nil {
		return "", err
	}
	token, err := seedanceOwnershipToken()
	if err != nil {
		return "", err
	}
	claimed, err := store.ClaimSeedanceBilling(ctx, owner.pendingKey(taskID), token, seedanceBillingClaimTTL)
	if err != nil || !claimed {
		return "", err
	}
	return token, nil
}

func (s *OpenAIGatewayService) ConfirmSeedanceBilling(ctx context.Context, taskID string, owner MediaTaskIdentity, token string) error {
	store, err := s.seedanceBillingStore(taskID, owner)
	if err != nil {
		return err
	}
	confirmed, err := store.ConfirmSeedanceBilling(ctx, owner.pendingKey(taskID), token, grokVideoBilledClaimTTL(s.cfg))
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("seedance billing claim ownership was lost")
	}
	return gatewaylite.ConfirmBindingMediaSettlement(ctx,taskID)
}

func (s *OpenAIGatewayService) ReleaseSeedanceBilling(ctx context.Context, taskID string, owner MediaTaskIdentity, token string) error {
	store, err := s.seedanceBillingStore(taskID, owner)
	if err != nil {
		return err
	}
	_, err = store.ReleaseSeedanceBilling(ctx, owner.pendingKey(taskID), token)
	return err
}

func (s *OpenAIGatewayService) seedanceBillingStore(taskID string, owner MediaTaskIdentity) (SeedanceBillingStore, error) {
	if s == nil || s.cache == nil || owner.pendingKey(taskID) == "" {
		return nil, errors.New("seedance billing claim is unavailable or identity is invalid")
	}
	store, ok := s.cache.(SeedanceBillingStore)
	if !ok {
		return nil, errors.New("seedance billing claim store is unavailable")
	}
	return store, nil
}

func seedanceOwnershipToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func firstSeedanceBillingModel(pending GrokVideoPendingBilling) string {
	return firstNonEmpty(strings.TrimSpace(pending.BillingModel), strings.TrimSpace(pending.Model))
}

// ValidateSeedanceExecutionPricing 在认领前拒绝缺失或损坏的原价，不能按轮询时的新价补账。
func ValidateSeedanceExecutionPricing(pending *GrokVideoPendingBilling) error {
	if pending == nil || pending.ExecutionPricing == nil {
		return errors.New("seedance execution pricing snapshot is unavailable")
	}
	_, err := pending.ExecutionPricing.ApplyToCostInput(CostInput{Model: firstSeedanceBillingModel(*pending)})
	return err
}

// SeedanceCompletionBillingContext 恢复生成任务的资金身份，不得使用免费轮询的请求编号/租约。
func SeedanceCompletionBillingContext(ctx context.Context, pending *GrokVideoPendingBilling) context.Context {
	if pending == nil {
		return ctx
	}
	if pending.RequestMeta != nil {
		meta := *pending.RequestMeta
		meta.Reservation = nil
		meta.ActualCentsOverride = nil
		meta.AsyncSettlement = true
		meta.AsyncPersistenceUncertain = false
		// 此处只在完成认领后调用；保留原租约/主体，不复活创建请求的锁，也不切换结算模式。
		meta.CommitAsyncReservation = pending.FundingSnapshot == nil
		if pending.FundingSnapshot != nil {
			meta.Funding = gatewaylite.NewUsageFundingSnapshot(*pending.FundingSnapshot)
		}
		ctx = gatewaylite.ContextWithRequestMeta(ctx, meta)
	}
	if pending.RatePolicyAudit != nil && pending.RatePolicyAudit.Valid() {
		ctx = ContextWithEffectiveRatePolicy(ctx, pending.RatePolicyAudit.Clone())
	} else if pending.RequestMeta != nil && pending.RequestMeta.RatePolicyAudit != nil && pending.RequestMeta.RatePolicyAudit.Valid() {
		ctx = ContextWithEffectiveRatePolicy(ctx, pending.RequestMeta.RatePolicyAudit.Clone())
	}
	if pending.RequestMeta == nil {
		// 本地任务也不能沿用每次轮询的新 request_id；固定 task ID 支持失败后幂等重放。
		ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "")
		ctx = context.WithValue(ctx, ctxkey.RequestID, StableGrokVideoBillingRequestID(pending.TaskID))
	}
	ctx = ContextWithExecutionPricingSnapshot(ctx, pending.ExecutionPricing)
	return ctx
}

type seedanceCreationContextKey struct{}

type seedanceCreation struct {
	owner        MediaTaskIdentity
	pending      GrokVideoPendingBilling
	admissionKey string
}

// PrepareSeedanceTaskCreation 在调用可能产生费用的上游前保存身份、账号与资金快照。
// admission 记录保留到 TTL 到期；模糊创建结果不能自动再次提交或释放原资金占用。
func (s *OpenAIGatewayService) PrepareSeedanceTaskCreation(ctx context.Context, key *APIKey, userID int64, account *Account, model, originalModel string) (context.Context, error) {
	owner, err := ResolveMediaTaskIdentity(ctx, key, userID)
	if err != nil {
		return ctx, err
	}
	if account == nil || account.ID <= 0 {
		return ctx, errors.New("seedance task account is unavailable")
	}
	requestID, ok := gatewaylite.CanonicalRequestIDFromContext(ctx)
	if !ok {
		requestID, err = seedanceOwnershipToken()
		if err != nil {
			return ctx, err
		}
	}
	pending := GrokVideoPendingBilling{AccountID: account.ID, Model: model, BillingModel: model,
		UpstreamModel: account.GetMappedModel(model), OriginalModel: originalModel, CreatedAt: GrokVideoPendingCreatedAtNow()}
	if key.GroupID != nil {
		pending.GroupID = *key.GroupID
	}
	creation := seedanceCreation{owner: owner, pending: pending, admissionKey: "seedance-admission:" + requestID}
	if existing, lookupErr := s.LoadSeedancePendingBilling(ctx, creation.admissionKey, owner); lookupErr != nil {
		return ctx, lookupErr
	} else if existing != nil {
		// 预登记意味着上次可能已经提交；缓存到期或客户端重试都不是再次消费的许可。
		return ctx, errors.Join(gatewaylite.ErrRequestQuotaUnavailable, errors.New("seedance task admission already exists; recover the original task"))
	}
	if s.billingService == nil {
		return ctx, ErrModelPricingUnavailable
	}
	resolver := s.resolver
	if resolver == nil {
		resolver = NewModelPricingResolver(nil, s.billingService)
	}
	rate := 1.0
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		rate = policy.EffectiveRateMultiplier
	} else if key.UserRateMultiplier != nil {
		rate = *key.UserRateMultiplier
	} else if key.Group != nil {
		rate = key.Group.RateMultiplier
	}
	var snapshot *ExecutionPricingSnapshot
	var pricingErr error
	// 保留原客户价优先、账号映射价兜底的顺序；选择只发生在创建前，完成不得重新选价。
	for _, candidate := range usageBillingModelCandidates(model, pending.UpstreamModel) {
		pricingInput := PricingInput{Model: candidate, Group: key.Group, GroupID: key.GroupID}
		costInput := CostInput{Ctx: ctx, Model: candidate, Resolved: resolver.Resolve(ctx, pricingInput), RateMultiplier: rate,
			LongContextBillingEnabled: openAILongContextBillingGate(key, account)}
		if key.Group != nil {
			costInput.LongContextGroupEnabled = &key.Group.AllowOpenAILongContextBilling
		}
		snapshot, pricingErr = FreezeExecutionPricing(costInput)
		if pricingErr == nil {
			// 显式持久化最终计费候选，使别名无价时也能精确恢复原数值价，而不是热价回退。
			creation.pending.BillingModel = candidate
			break
		}
	}
	if snapshot == nil {
		return ctx, errors.Join(ErrModelPricingUnavailable, pricingErr)
	}
	creation.pending.ExecutionPricing = snapshot
	ctx = ContextWithExecutionPricingSnapshot(ctx, snapshot)
	if err := s.StoreSeedancePendingBilling(ctx, creation.admissionKey, owner, creation.pending); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, seedanceCreationContextKey{}, creation), nil
}

func (s *OpenAIGatewayService) persistSeedanceCreatedTask(ctx context.Context, creation seedanceCreation, taskID string) error {
	creation.pending.TaskID = taskID
	// 同一任务登记可重试，禁止重试上游创建。脱离客户端取消保留恢复窗口。
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := s.StoreSeedancePendingBilling(persistCtx, taskID, creation.owner, creation.pending)
	if err != nil {
		err = s.StoreSeedancePendingBilling(persistCtx, taskID, creation.owner, creation.pending)
	}
	// admission 也记录已知任务编号；最终登记 unknown 时仍能按原资金请求追踪修复。
	admissionErr := s.StoreSeedancePendingBilling(persistCtx, creation.admissionKey, creation.owner, creation.pending)
	if err != nil {
		return errors.Join(errors.New("seedance task registration is uncertain"), err, admissionErr)
	}
	return nil
}

// SeedanceTaskKey isolates ownership and billing keys from other video providers.
func SeedanceTaskKey(id string) string { return "seedance:" + strings.TrimSpace(id) }

func ParseSeedanceRequest(body []byte) (GrokMediaRequestInfo, error) {
	var info GrokMediaRequestInfo
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return info, fmt.Errorf("request body must be a JSON object")
	}
	model := gjson.GetBytes(body, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return info, fmt.Errorf("model is required")
	}
	content := gjson.GetBytes(body, "content")
	if !content.IsArray() || len(content.Array()) == 0 {
		return info, fmt.Errorf("content must be a non-empty array")
	}
	info.Model = strings.TrimSpace(model.String())
	var texts []string
	for _, item := range content.Array() {
		switch item.Get("type").String() {
		case "text":
			texts = append(texts, item.Get("text").String())
		case "image_url":
			// [fork 适配] 统一 MediaInputs 抽象（审核/出站构建走我方体系）
			info.addGrokMediaURLInput("image_url", GrokMediaInputRoleReference, item.Get("image_url.url").String())
		}
	}
	info.Prompt = strings.Join(texts, "\n")
	return info, nil
}

func buildSeedanceURL(base string, endpoint GrokMediaEndpoint, taskID string) (string, error) {
	base = strings.TrimRight(base, "/")
	// Accept an origin, a proxy prefix, or the full Ark API base.
	if !strings.HasSuffix(base, "/api/v3") && !strings.HasSuffix(base, "/v3") {
		base += "/api/v3"
	}
	base += "/contents/generations/tasks"
	if endpoint != SeedanceEndpointCreate {
		if err := validateUpstreamPathSegment("Seedance task ID", taskID); err != nil || strings.TrimSpace(taskID) == "" {
			return "", fmt.Errorf("invalid Seedance task ID")
		}
		base += "/" + taskID
	}
	return base, nil
}

// ForwardSeedance preserves the Ark protocol, including multimodal content and
// future fields. Only model is rewritten using the account's configured mapping.
func (s *OpenAIGatewayService) ForwardSeedance(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID string, body []byte) (*OpenAIForwardResult, error) {
	if !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance) || !endpoint.IsSeedance() {
		return nil, fmt.Errorf("seedance requires an OpenAI API key account with a custom base URL")
	}
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	target, err := buildSeedanceURL(base, endpoint, strings.TrimPrefix(taskID, "seedance:"))
	if err != nil {
		return nil, err
	}
	model, upstreamModel := "", ""
	method := http.MethodGet
	switch endpoint {
	case SeedanceEndpointCreate:
		info, parseErr := ParseSeedanceRequest(body)
		if parseErr != nil {
			return nil, parseErr
		}
		model = info.Model
		upstreamModel = account.GetMappedModel(model)
		body, err = sjson.SetBytes(body, "model", upstreamModel)
		if err != nil {
			return nil, err
		}
		method = http.MethodPost
	case SeedanceEndpointDelete:
		method = http.MethodDelete
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, fmt.Errorf("seedance account missing api_key")
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	creation, prepared := ctx.Value(seedanceCreationContextKey{}).(seedanceCreation)
	if endpoint == SeedanceEndpointCreate {
		if _, remote := gatewaylite.RequestMetaFromContext(ctx); remote && !prepared {
			return nil, errors.New("seedance create requires a durable task admission")
		}
	}
	if endpoint == SeedanceEndpointCreate {
		ctx,err=gatewaylite.PrepareBindingMediaDispatch(ctx)
		if err!=nil{return nil,err}
		req=req.WithContext(ctx)
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		if endpoint == SeedanceEndpointCreate {
			c.Request = c.Request.WithContext(gatewaylite.ContextWithUncertainAsyncPersistence(c.Request.Context()))
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if endpoint == SeedanceEndpointCreate {
			c.Request = c.Request.WithContext(gatewaylite.ContextWithUncertainAsyncPersistence(c.Request.Context()))
		}
		return nil, err
	}
	// Do not retry ambiguous asynchronous creates: the upstream may already have
	// accepted a billable job. Preserve native error codes and response bodies.
	if resp.StatusCode >= 300 {
		if endpoint==SeedanceEndpointCreate && resp.StatusCode>=400 && resp.StatusCode<500 {
			// An explicit rejection before task acceptance is a terminal no-dispatch
			// outcome. Transport failures and ambiguous 5xx remain pending.
			_ = gatewaylite.ReleaseBindingMediaBeforeAcceptance(context.WithoutCancel(ctx))
		}
		writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
		return nil, fmt.Errorf("seedance upstream status %d", resp.StatusCode)
	}
	result := &OpenAIForwardResult{Model: model, BillingModel: model, UpstreamModel: upstreamModel, Duration: time.Since(started), ResponseHeaders: resp.Header.Clone()}
	if endpoint == SeedanceEndpointCreate {
		id := strings.TrimSpace(gjson.GetBytes(responseBody, "id").String())
		if id == "" {
			c.Request = c.Request.WithContext(gatewaylite.ContextWithUncertainAsyncPersistence(c.Request.Context()))
			return nil, fmt.Errorf("seedance create response missing task ID")
		}
		result.ResponseID = SeedanceTaskKey(id)
		if err:=gatewaylite.RecordBindingMediaTask(context.WithoutCancel(ctx),result.ResponseID);err!=nil{
			c.Request=c.Request.WithContext(gatewaylite.ContextWithUncertainAsyncPersistence(c.Request.Context()))
			return nil,errors.New("seedance binding task registration is uncertain")
		}
		if prepared {
			if err := s.persistSeedanceCreatedTask(ctx, creation, result.ResponseID); err != nil {
				c.Request = c.Request.WithContext(gatewaylite.ContextWithUncertainAsyncPersistence(c.Request.Context()))
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "code": "seedance_task_registration_uncertain", "message": "Seedance task was accepted but registration is uncertain; do not submit again", "task_id": id}})
				return nil, err
			}
			c.Request = c.Request.WithContext(gatewaylite.ContextWithAsyncSettlement(c.Request.Context()))
		}
	}
	if endpoint == SeedanceEndpointStatus {
		result.ResponseID = taskID
		result.UpstreamModel = gjson.GetBytes(responseBody, "model").String()
		if gjson.GetBytes(responseBody, "status").String() == "succeeded" {
			result.Usage.OutputTokens = max(0, int(gjson.GetBytes(responseBody, "usage.completion_tokens").Int()))
		}
	}
	writeGrokMediaResponse(c, resp, responseBody, s.responseHeaderFilter)
	return result, nil
}
