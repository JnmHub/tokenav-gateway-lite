package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// OpenAIGatewayHandler handles OpenAI API gateway requests
type OpenAIGatewayHandler struct {
	gatewayService             *service.OpenAIGatewayService
	billingCacheService        *service.BillingCacheService
	apiKeyService              *service.APIKeyService
	usageRecordWorkerPool      *service.UsageRecordWorkerPool
	errorPassthroughService    *service.ErrorPassthroughService
	contentModerationService   *service.ContentModerationService
	pricingService             *service.PricingService
	opsService                 *service.OpsService
	concurrencyHelper          *ConcurrencyHelper
	imageLimiter               *imageConcurrencyLimiter
	imageTaskService           *service.ImageTaskService
	mediaProxyHandler          *MediaProxyHandler
	maxAccountSwitches         int
	cfg                        *config.Config
	securityAuditCoordinator   *securityaudit.Coordinator // [v0.2.5 新增字段]
	grokMediaEligibilityProber grokMediaEligibilityProber // [v0.2.5 新增字段]
}

// SetMediaProxy attaches the instance-local reader used by rewritten image
// URLs.  Keeping it as a setter avoids a dependency cycle in the Wire graph:
// the proxy needs the final JWT configuration, while the OpenAI handler is
// constructed by the normal gateway service set.
func (h *OpenAIGatewayHandler) SetMediaProxy(proxy *service.MediaProxy) {
	if h == nil {
		return
	}
	if proxy == nil {
		h.mediaProxyHandler = nil
		return
	}
	h.mediaProxyHandler = NewMediaProxyHandler(proxy)
}

// ServeMediaProxy is the route-facing entry point for signed generated-image
// URLs.  It deliberately has no API-key middleware; the encrypted, expiring
// token is the credential carried by a browser image request.
func (h *OpenAIGatewayHandler) ServeMediaProxy(c *gin.Context) {
	if h == nil || h.mediaProxyHandler == nil {
		c.Status(http.StatusNotFound)
		return
	}
	h.mediaProxyHandler.Serve(c)
}

// SetImageTaskService attaches the durable image queue after the ordinary
// gateway handler has been built. The two-phase wiring avoids a dependency
// cycle: the queue executor deliberately reuses this handler so image routing,
// moderation and billing continue to have one implementation.
func (h *OpenAIGatewayHandler) SetImageTaskService(imageTasks *service.ImageTaskService) {
	if h != nil {
		h.imageTaskService = imageTasks
	}
}

type openAIWSTurnChannelMappingSnapshot struct {
	turn    int
	mapping service.ChannelMappingResult
}

type openAIWSTurnRateLimitSnapshot struct {
	turn            int
	estimatedTokens int
}

// openAIGroupFailoverLoopStage keeps the request's cross-group state explicit.
// A request can cross at most once; source_fallback is the final same-group
// model fallback pass and must never reopen the cross-group budget.
type openAIGroupFailoverLoopStage uint8

const (
	openAIGroupFailoverStageSource openAIGroupFailoverLoopStage = iota
	openAIGroupFailoverStageTarget
	openAIGroupFailoverStageSourceFallback
)

func openAIGroupFailoverResponseStarted(streamStarted, failoverSafeAfterCommittedPreamble bool) bool {
	return streamStarted && !failoverSafeAfterCommittedPreamble
}

func nextOpenAIGroupFailoverStage(
	plan service.OpenAIGroupFailoverPlan,
	current openAIGroupFailoverLoopStage,
	failure error,
	responseStarted bool,
) (openAIGroupFailoverLoopStage, bool) {
	if responseStarted || !service.ShouldOpenAIGroupFailover(plan, failure) {
		return current, false
	}
	switch current {
	case openAIGroupFailoverStageSource:
		if plan.TrySourceFallbackBeforeCrossGroup {
			return openAIGroupFailoverStageSourceFallback, true
		}
		return openAIGroupFailoverStageTarget, true
	case openAIGroupFailoverStageTarget:
		if plan.TryCrossGroupBeforeSourceFallback {
			return openAIGroupFailoverStageSourceFallback, true
		}
	case openAIGroupFailoverStageSourceFallback:
		if plan.TrySourceFallbackBeforeCrossGroup {
			return openAIGroupFailoverStageTarget, true
		}
	}
	return current, false
}

// openAIGroupFailoverUsageFields deliberately keeps the source group's
// billing identity immutable. A target group's mapping is only diagnostic
// evidence when the request actually lands there; it must not alter price,
// channel or billing-source fields for the API key bound to A.
func openAIGroupFailoverUsageFields(
	c *gin.Context,
	billingMapping service.ChannelMappingResult,
	routingMapping service.ChannelMappingResult,
	requestedModel string,
	upstreamModel string,
	targetActive bool,
) service.ChannelUsageFields {
	fields := clientRequestedUsageFields(c, billingMapping, requestedModel, upstreamModel)
	if targetActive {
		fields.ModelMappingChain = routingMapping.BuildModelMappingChain(fields.OriginalModel, upstreamModel)
	}
	return fields
}

var errOpenAIWSUnsupportedModelSwitch = errors.New("selected account does not support websocket model switch")

func newOpenAIWSUnsupportedModelSwitchError(model string) error {
	cause := fmt.Errorf("%w: model %q", errOpenAIWSUnsupportedModelSwitch, strings.TrimSpace(model))
	return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "model switch requires reconnect", cause)
}

func shouldReportOpenAIWSProxyAccountFailure(err error) bool {
	return err != nil && !errors.Is(err, errOpenAIWSUnsupportedModelSwitch)
}

func openAIWSTurnBillingModel(result *service.OpenAIForwardResult, mapping service.ChannelMappingResult, requestedModel, upstreamModel string) string {
	billingModel := ""
	if result != nil {
		billingModel = strings.TrimSpace(result.BillingModel)
	}
	if billingModel == "" {
		billingModel = strings.TrimSpace(upstreamModel)
	}
	if billingModel == "" {
		billingModel = strings.TrimSpace(requestedModel)
	}
	requestedModel = strings.TrimSpace(requestedModel)
	switch mapping.BillingModelSource {
	case service.BillingModelSourceRequested:
		if requestedModel != "" {
			billingModel = requestedModel
		}
	case service.BillingModelSourceChannelMapped:
		if mapped := strings.TrimSpace(mapping.MappedModel); mapped != "" && mapped != requestedModel {
			billingModel = mapped
		}
	}
	return billingModel
}

// openAIWSTurnBillingResult gives every successful WebSocket turn its own
// stable, connection-scoped financial identity. Upstream response IDs are not
// safe ledger keys: compatible providers may omit them or reuse values such as
// resp_1 across independent connections.
// It intentionally clones the result: the forwarder still needs the original
// upstream ID for previous_response_id state and must never see this local
// billing-only identity.
func openAIWSTurnBillingResult(connectionRequestID string, turn int, result *service.OpenAIForwardResult) *service.OpenAIForwardResult {
	if result == nil {
		return result
	}
	connectionRequestID = strings.TrimSpace(connectionRequestID)
	if connectionRequestID == "" {
		return result
	}
	if turn < 1 {
		turn = 1
	}
	billingResult := *result
	if strings.TrimSpace(billingResult.ResponseID) == "" {
		billingResult.ResponseID = strings.TrimSpace(result.RequestID)
	}
	billingResult.RequestID = fmt.Sprintf("%s:ws-turn:%d", connectionRequestID, turn)
	return &billingResult
}

func resolveOpenAIMessagesDispatchMappedModel(apiKey *service.APIKey, requestedModel string) string {
	return resolveOpenAIMessagesDispatchMappedModelForRequest(nil, apiKey, requestedModel)
}

func resolveOpenAIMessagesDispatchMappedModelForRequest(c *gin.Context, apiKey *service.APIKey, requestedModel string) string {
	if apiKey == nil || apiKey.Group == nil {
		return ""
	}
	// 组合分组解析到 Grok/国产供应商时，OpenAI 专属的分组级 Messages 映射
	// 不适用；模型改写继续由组合路由和账号级映射负责。
	if apiKey.Group.Platform == service.PlatformComposite && c != nil && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok &&
			(platform == service.PlatformGrok || service.IsCNProvider(platform)) {
			return ""
		}
	}
	return strings.TrimSpace(apiKey.Group.ResolveMessagesDispatchModel(requestedModel))
}

func openAICompatibleRequestPlatform(apiKey *service.APIKey) string {
	if apiKey != nil && apiKey.Group != nil {
		switch apiKey.Group.Platform {
		case service.PlatformGrok, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek:
			return apiKey.Group.Platform
		}
	}
	return service.PlatformOpenAI
}

func openAIResponsesRequiredCapabilityForRequest(imageIntent bool, needsResponses bool, platform string) service.OpenAIEndpointCapability {
	if platform == service.PlatformOpenAI && (imageIntent || needsResponses) {
		return service.OpenAIEndpointCapabilityResponses
	}
	return service.OpenAIEndpointCapabilityChatCompletions
}

func allowOpenAICompatibleMessagesDispatch(apiKey *service.APIKey) bool {
	return allowOpenAICompatibleMessagesDispatchForRequest(nil, apiKey)
}

func allowOpenAICompatibleMessagesDispatchForRequest(c *gin.Context, apiKey *service.APIKey) bool {
	if apiKey == nil || apiKey.Group == nil {
		return true
	}
	// Grok/国产供应商的 Messages（消息）入口本身就是兼容桥，不能被 OpenAI
	// 分组专属的可选开关默认值误拦截。
	if apiKey.Group.Platform == service.PlatformGrok || service.IsCNProvider(apiKey.Group.Platform) {
		return true
	}
	if apiKey.Group.Platform == service.PlatformComposite && c != nil && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok &&
			(platform == service.PlatformGrok || service.IsCNProvider(platform)) {
			return true
		}
	}
	return apiKey.Group.AllowMessagesDispatch
}

func openAICompatibleTextTargetAllowed(c *gin.Context, apiKey *service.APIKey, model string) bool {
	return compositeTargetPlatformAllowed(c, apiKey, model,
		service.PlatformOpenAI, service.PlatformGrok,
		service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek)
}

// Composite（组合分组）的 Responses WebSocket 只放行已有完整桥接的目标。
// 国产供应商仍可使用 HTTP/SSE；这里提前给出明确拒绝，避免误导成无可用账号。
func isResponsesWebSocketCompositePlatform(platform string) bool {
	return platform == service.PlatformOpenAI || platform == service.PlatformGrok
}

func resolveOpenAIWSClientFirstMessageTimeout(cfg *config.Config) time.Duration {
	seconds := config.DefaultOpenAIWSClientFirstMessageTimeoutSeconds
	if cfg != nil && cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds > 0 {
		seconds = cfg.Gateway.OpenAIWS.ClientFirstMessageTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

type openAIModelBodyReplaceFunc func([]byte, string) []byte

func openAIModelMappedBody(body []byte, mapped bool, mappedModel string, replace openAIModelBodyReplaceFunc) []byte {
	if !mapped || replace == nil {
		return body
	}
	return replace(body, mappedModel)
}

func seedOpenAIForwardImageIntentHint(c *gin.Context, channelMapped bool, imageIntent bool) {
	if channelMapped {
		// 渠道映射改变了规范请求，保持 unknown，由 Forward 按映射后的 model/body 初始化。
		return
	}
	service.SetOpenAIImageIntentHint(c, imageIntent)
}

func newOpenAIModelMappedBodyCache(body []byte, replace openAIModelBodyReplaceFunc) func(bool, string) []byte {
	replacedBodies := make(map[string][]byte)
	return func(mapped bool, mappedModel string) []byte {
		if !mapped {
			return body
		}
		if cachedBody, ok := replacedBodies[mappedModel]; ok {
			return cachedBody
		}
		replacedBody := openAIModelMappedBody(body, true, mappedModel, replace)
		replacedBodies[mappedModel] = replacedBody
		return replacedBody
	}
}

func usageRecordContext(parent context.Context, base context.Context) context.Context {
	if base == nil {
		base = context.Background()
	}
	if parent == nil {
		return base
	}
	if clientRequestID, _ := parent.Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(clientRequestID) != "" {
		base = context.WithValue(base, ctxkey.ClientRequestID, strings.TrimSpace(clientRequestID))
	}
	if requestID, _ := parent.Value(ctxkey.RequestID).(string); strings.TrimSpace(requestID) != "" {
		base = context.WithValue(base, ctxkey.RequestID, strings.TrimSpace(requestID))
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(parent); ok {
		if meta.Funding != nil {
			meta.Funding = gatewaylite.NewUsageFundingSnapshot(meta.Funding.Snapshot())
		}
		base = gatewaylite.ContextWithRequestMeta(base, meta)
	}
	if audit, ok := gatewaylite.ReasoningEffortAuditFromContext(parent); ok {
		// 本地 glk_ 密钥没有 RequestMeta；异步用量任务必须单独复制审计快照，
		// 否则流式请求结束后会丢失客户原始档次，进而让主站日志显示为空。
		base = gatewaylite.ContextWithReasoningEffortAudit(base, audit.Requested, audit.Final)
	}
	if service.IsGatewayLiteLocalAPIKeyContext(parent) {
		base = service.ContextWithGatewayLiteLocalAPIKey(base)
	}
	if policy, ok := service.EffectiveRatePolicyFromContext(parent); ok {
		// 用量任务会脱离客户端请求并进入异步 worker。倍率策略和 request_id
		// 一样属于结算必需快照；若不复制，流式请求结束跨过高峰边界或管理员
		// 中途改价后，worker 会退回结束时重算，造成同一请求前后价格不一致。
		base = service.ContextWithEffectiveRatePolicy(base, policy)
	}
	base = service.ContextWithExecutionPricingFromContext(base, parent)
	// 后台只需要原委托占用来完成结算。专用快照不携带原始凭据或执行权，
	// 避免断开用户请求后丢掉双侧结算，也不能让用量 worker 再次发起消费。
	return gatewaylite.ContextWithOAuthInvocationUsageSnapshot(base, parent)
}

func wrapUsageRecordTaskContext(parent context.Context, task service.UsageRecordTask) service.UsageRecordTask {
	if task == nil {
		return nil
	}
	return func(ctx context.Context) {
		task(usageRecordContext(parent, ctx))
	}
}

// NewOpenAIGatewayHandler creates a new OpenAIGatewayHandler
func NewOpenAIGatewayHandler(
	gatewayService *service.OpenAIGatewayService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	errorPassthroughService *service.ErrorPassthroughService,
	contentModerationService *service.ContentModerationService,
	pricingService *service.PricingService,
	opsService *service.OpsService,
	cfg *config.Config,
) *OpenAIGatewayHandler {
	pingInterval := time.Duration(0)
	maxAccountSwitches := 3
	if cfg != nil {
		pingInterval = time.Duration(cfg.Concurrency.PingInterval) * time.Second
		if cfg.Gateway.MaxAccountSwitches > 0 {
			maxAccountSwitches = cfg.Gateway.MaxAccountSwitches
		}
	}
	return &OpenAIGatewayHandler{
		gatewayService:           gatewayService,
		billingCacheService:      billingCacheService,
		apiKeyService:            apiKeyService,
		usageRecordWorkerPool:    usageRecordWorkerPool,
		errorPassthroughService:  errorPassthroughService,
		contentModerationService: contentModerationService,
		pricingService:           pricingService,
		opsService:               opsService,
		concurrencyHelper:        NewConcurrencyHelper(concurrencyService, SSEPingFormatComment, pingInterval),
		imageLimiter:             &imageConcurrencyLimiter{},
		maxAccountSwitches:       maxAccountSwitches,
		cfg:                      cfg,
	}
}

// Responses handles OpenAI Responses API endpoint
// POST /openai/v1/responses
func (h *OpenAIGatewayHandler) Responses(c *gin.Context) {
	// 局部兜底：确保该 handler 内部任何 panic 都不会击穿到进程级。
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)
	compactStartedAt := time.Now()
	defer h.logOpenAIRemoteCompactOutcome(c, compactStartedAt)
	setOpenAIClientTransportHTTP(c)

	requestStart := time.Now()

	// Get apiKey and user from context (set by ApiKeyAuth middleware)
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.responses",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	// Read request body
	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	setOpsRequestContext(c, "", false)
	sessionHashBody := body
	var normalizedOK bool
	body, normalizedOK = h.normalizeOpenAIResponsesCompactRequest(c, reqLog, body)
	if !normalizedOK {
		return
	}
	legacyCompact := isOpenAIRemoteCompactPath(c)
	nativeCompactionV2 := isBareOpenAIResponsesPath(c) && isOpenAIRemoteCompactionV2Request(body)
	if nativeCompactionV2 {
		service.MarkOpenAINativeCompactionV2(c)
	}
	stopCompactKeepalive := service.StartOpenAICompactSSEKeepalive(c, h.openAICompactKeepaliveInterval())
	defer stopCompactKeepalive()
	if sanitizedBody, sanitized, sanitizeErr := service.SanitizeOpenAIUndefinedSentinelRequestBody(body); sanitizeErr != nil {
		logRequestBodyParseFailure(reqLog, body, sanitizeErr)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	} else if sanitized {
		body = sanitizedBody
	}

	// 校验请求体 JSON 合法性
	if !gjson.ValidBytes(body) {
		logRequestBodyParseFailure(reqLog, body, nil)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	// 使用 gjson 只读提取字段做校验，避免完整 Unmarshal
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	// 后续分组/账号映射可能改写 body；提前保存客户模型供响应声明策略使用。
	c.Request = c.Request.WithContext(service.WithClientRequestedResponseModel(c.Request.Context(), reqModel))
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}
	if h.isModelDisabled(reqModel) {
		h.errorResponse(c, http.StatusForbidden, "model_disabled", "model is disabled")
		return
	}

	reqStream, ok := parseOpenAICompatibleStream(body)
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", invalidStreamFieldTypeMessage)
		return
	}
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))
	previousResponseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if previousResponseID != "" {
		previousResponseIDKind := service.ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
		reqLog = reqLog.With(
			zap.Bool("has_previous_response_id", true),
			zap.String("previous_response_id_kind", previousResponseIDKind),
			zap.Int("previous_response_id_len", len(previousResponseID)),
		)
		if previousResponseIDKind == service.OpenAIPreviousResponseIDKindMessageID {
			reqLog.Warn("openai.request_validation_failed",
				zap.String("reason", "previous_response_id_looks_like_message_id"),
			)
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "previous_response_id must be a response.id (resp_*), not a message id")
			return
		}
		reqLog.Warn("openai.request_validation_failed",
			zap.String("reason", "previous_response_id_requires_wsv2"),
		)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "previous_response_id is only supported on Responses WebSocket v2")
		return
	}

	setOpsRequestContext(c, reqModel, reqStream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(reqStream, false)))

	if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, reqModel, body); decision != nil && decision.Blocked {
		h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	body = contentModerationForwardBody(c, body)
	// 推理档位属于实际计费分组策略，不能在关键词切组前用原分组改写请求。
	if policyBody, changed := applyOpenAIReasoningEffortPolicyForRequest(c, apiKey, body); changed {
		body = policyBody
	}
	// Codex 委托引导首轮把线程描述塞进 function_call_output 却不带 call_id，
	// 必须在校验前还原成普通 user 消息，否则会被按协议错误 400 拒绝。
	// [官方 v0.2.0] 定时自动化（scheduled automation）启动请求是同构问题，
	// 同样缺 call_id，先于委托归一化处理。
	if normalizedBody, changed := normalizeCodexAutomationBootstrap(body); changed {
		body = normalizedBody
		reqLog.Info("openai.codex_automation_bootstrap_normalized",
			zap.String("normalization", "call_output_to_user_message"),
		)
	}
	if normalizedBody, changed := normalizeCodexDelegationBootstrap(body); changed {
		body = normalizedBody
		reqLog.Info("openai.codex_delegation_bootstrap_normalized",
			zap.String("normalization", "call_output_to_user_message"),
		)
	}

	// 渠道模型映射可能把用户侧别名映射到图片模型，必须在调度前合并判断，
	// 否则会先选中文本账号，直到转发时才发现这是图片请求。
	billingChannelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	routingChannelMapping := billingChannelMapping
	requestPlatform := openAICompatibleRequestPlatformFromContext(c.Request.Context(), apiKey)
	imageIntent := service.IsExplicitImageGenerationIntent("/v1/responses", reqModel, body)
	channelMappedModelForIntent := strings.TrimSpace(routingChannelMapping.MappedModel)
	if routingChannelMapping.Mapped && channelMappedModelForIntent != "" {
		imageIntent = imageIntent || service.IsExplicitImageGenerationIntent("/v1/responses", channelMappedModelForIntent, nil)
	}
	if imageIntent && !service.GroupAllowsImageGeneration(apiKey.Group) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}
	imageRoutingModel := ""
	var imageReleaseFunc func()
	if imageIntent {
		imageRoutingModel = service.ResolveOpenAIResponsesImageRoutingModel(body, channelMappedModelForIntent)
		if imageRoutingModel == "" {
			imageRoutingModel = service.ResolveOpenAIResponsesImageRoutingModel(body, reqModel)
		}
		var imageAcquired bool
		imageReleaseFunc, imageAcquired = h.acquireImageGenerationSlot(c, streamStarted)
		if !imageAcquired {
			return
		}
		if imageReleaseFunc != nil {
			defer imageReleaseFunc()
		}
	}

	forwardBodyRef := service.NewRequestBodyRef(openAIModelMappedBody(body, routingChannelMapping.Mapped, routingChannelMapping.MappedModel, h.gatewayService.ReplaceModelInBody))
	forwardModel := reqModel
	if routingChannelMapping.Mapped && strings.TrimSpace(routingChannelMapping.MappedModel) != "" {
		forwardModel = routingChannelMapping.MappedModel
	}
	c.Request = c.Request.WithContext(service.WithOpenAIForwardModel(c.Request.Context(), forwardModel, legacyCompact))
	if !imageIntent {
		c.Request = c.Request.WithContext(service.WithOpenAIGroupModelFallbackEnabled(c.Request.Context()))
	}
	seedOpenAIForwardImageIntentHint(c, routingChannelMapping.Mapped, imageIntent)

	// 提前校验 function_call_output 是否具备可关联上下文，避免上游 400。
	if !h.validateFunctionCallOutputRequest(c, body, reqLog) {
		return
	}

	// 绑定错误透传服务，允许 service 层在非 failover 错误场景复用规则。
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	// Get subscription info (may be nil)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if !imageIntent {
		longContextModels := []string{reqModel}
		if routingChannelMapping.Mapped {
			longContextModels = append([]string{routingChannelMapping.MappedModel}, longContextModels...)
		}
		routingCtx, _, routingErr := h.gatewayService.PrepareOpenAILongContextRouting(
			c.Request.Context(), apiKey.Group, requestPlatform, body, longContextModels...,
		)
		if routingErr != nil {
			status, errType, code, message, _ := service.OpenAILongContextRoutingErrorDetails(routingErr)
			h.handleStreamingAwareErrorWithCode(c, status, errType, code, message, streamStarted)
			return
		}
		c.Request = c.Request.WithContext(routingCtx)
	}

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()
	c.Request = c.Request.WithContext(service.ContextWithOpsRoutingTrace(c.Request.Context()))

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	// 确保请求取消时也会释放槽位，避免长连接被动中断造成泄漏
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2. Re-check billing eligibility after wait
	if !h.shouldSkipGatewayLiteBillingEligibility(c) {
		billingStart := time.Now()
		err := h.billingCacheService.CheckBillingEligibilityForRequest(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey), service.EstimateRequestTokensForLimit(body))
		service.AddOpsLatencyMs(c, service.OpsRoutingBillingLatencyMsKey, time.Since(billingStart).Milliseconds())
		if err != nil {
			reqLog.Info("openai.billing_eligibility_check_failed", zap.Error(err))
			status, code, message, retryAfter := billingErrorDetails(err)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.handleStreamingAwareError(c, status, code, message, streamStarted)
			return
		}
	}

	groupFailoverPlan, groupFailoverErr := h.gatewayService.ResolveOpenAIGroupFailoverPlan(
		c.Request.Context(),
		apiKey.Group,
		service.OpenAIGroupFailoverRequest{
			Endpoint:           service.OpenAIGroupFailoverEndpointResponses,
			RequestedModel:     reqModel,
			APIKey:             apiKey,
			PreviousResponseID: previousResponseID,
			Image:              imageIntent,
			Compact:            legacyCompact || nativeCompactionV2,
		},
	)
	if groupFailoverErr != nil {
		reqLog.Warn("openai.group_failover_plan_failed", zap.Error(groupFailoverErr))
		groupFailoverPlan = service.OpenAIGroupFailoverPlan{Reason: "plan_failed", SourceGroup: apiKey.Group}
	}
	if groupFailoverPlan.Eligible {
		c.Request = c.Request.WithContext(service.WithoutOpenAIGroupModelFallbackPolicy(c.Request.Context()))
	}

	// Generate session hash (header first; fallback to prompt_cache_key)
	sessionPolicyStart := time.Now()
	sessionHash := h.gatewayService.GenerateSessionHash(c, sessionHashBody)
	cyberSessionBlocked := h.rejectIfCyberSessionBlocked(c, apiKey, sessionHashBody, reqModel, cyberBlockFormatResponses)
	service.AddOpsLatencyMs(c, service.OpsRoutingSessionPolicyLatencyMsKey, time.Since(sessionPolicyStart).Milliseconds())
	if cyberSessionBlocked {
		return
	}
	requireCompact := legacyCompact

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	var passthroughFailoverState openAIPassthroughFailoverState
	failoverSafeAfterCommittedPreamble := false
	routingGroupID := apiKey.SchedulerGroupID()
	routingGroup := apiKey.Group
	groupFailoverStage := openAIGroupFailoverStageSource
	targetGroupActive := false
	var targetGroupRelease func()
	defer func() {
		if targetGroupRelease != nil {
			targetGroupRelease()
		}
	}()
	requiredEndpointCapability := openAIResponsesRequiredCapabilityForRequest(
		imageIntent,
		nativeCompactionV2 || legacyCompact,
		requestPlatform,
	)
	advanceGroupFailover := func(failure error, responseStarted bool) bool {
		nextStage, ok := nextOpenAIGroupFailoverStage(groupFailoverPlan, groupFailoverStage, failure, responseStarted)
		if !ok {
			return false
		}

		candidateCtx := c.Request.Context()
		candidateGroup := routingGroup
		candidateTargetActive := false
		var candidateTargetRelease func()
		var candidateGroupID int64
		var transitionErr error
		switch nextStage {
		case openAIGroupFailoverStageTarget:
			var acquired bool
			candidateTargetRelease, acquired, transitionErr = h.concurrencyHelper.TryAcquireFailoverGroupSlot(
				candidateCtx, apiKey.User, subject.UserID, groupFailoverPlan.TargetGroup,
			)
			if transitionErr != nil || !acquired {
				reqLog.Warn("openai.group_failover_target_concurrency_rejected",
					zap.Int64("target_group_id", groupFailoverPlan.TargetGroup.ID),
					zap.Error(transitionErr),
				)
				return false
			}
			candidateCtx, candidateGroupID, transitionErr = service.ApplyOpenAIGroupFailover(candidateCtx, groupFailoverPlan)
			candidateGroup = groupFailoverPlan.TargetGroup
			candidateTargetActive = true
		case openAIGroupFailoverStageSourceFallback:
			if groupFailoverStage == openAIGroupFailoverStageSource && groupFailoverPlan.TrySourceFallbackBeforeCrossGroup {
				candidateCtx = service.WithOpenAIGroupModelFallbackPolicy(candidateCtx)
				candidateCtx = service.WithOpenAIGroupModelFallbackOnly(candidateCtx)
				candidateGroup = groupFailoverPlan.SourceGroup
				candidateGroupID = candidateGroup.ID
			} else {
				candidateCtx, candidateGroupID, transitionErr = service.RestoreOpenAIGroupForModelFallback(candidateCtx, groupFailoverPlan)
				candidateGroup = groupFailoverPlan.SourceGroup
			}
		default:
			return false
		}
		if transitionErr != nil || candidateGroup == nil || candidateGroupID <= 0 {
			if candidateTargetRelease != nil {
				candidateTargetRelease()
			}
			reqLog.Warn("openai.group_failover_transition_failed", zap.Error(transitionErr))
			return false
		}

		candidateGroupIDRef := candidateGroupID
		candidateMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(candidateCtx, &candidateGroupIDRef, reqModel)
		candidateForwardModel := reqModel
		if candidateMapping.Mapped && strings.TrimSpace(candidateMapping.MappedModel) != "" {
			candidateForwardModel = strings.TrimSpace(candidateMapping.MappedModel)
		}
		candidateCtx = service.WithOpenAIForwardModel(candidateCtx, candidateForwardModel, legacyCompact)
		if !imageIntent {
			longContextModels := []string{reqModel}
			if candidateMapping.Mapped {
				longContextModels = append([]string{candidateMapping.MappedModel}, longContextModels...)
			}
			var routingErr error
			candidateCtx, _, routingErr = h.gatewayService.PrepareOpenAILongContextRouting(
				candidateCtx, candidateGroup, requestPlatform, body, longContextModels...,
			)
			if routingErr != nil {
				if candidateTargetRelease != nil {
					candidateTargetRelease()
				}
				reqLog.Warn("openai.group_failover_long_context_rejected",
					zap.Int64("target_group_id", candidateGroupID),
					zap.Error(routingErr),
				)
				return false
			}
		}
		if !candidateTargetActive && targetGroupRelease != nil {
			targetGroupRelease()
			targetGroupRelease = nil
		}
		if candidateTargetActive {
			targetGroupRelease = candidateTargetRelease
		}

		c.Request = c.Request.WithContext(candidateCtx)
		routingGroupID = &candidateGroupIDRef
		routingGroup = candidateGroup
		routingChannelMapping = candidateMapping
		forwardBodyRef = service.NewRequestBodyRef(openAIModelMappedBody(body, candidateMapping.Mapped, candidateMapping.MappedModel, h.gatewayService.ReplaceModelInBody))
		forwardModel = candidateForwardModel
		groupFailoverStage = nextStage
		targetGroupActive = candidateTargetActive
		failedAccountIDs = make(map[int64]struct{})
		sameAccountRetryCount = make(map[int64]int)
		switchCount = 0
		lastFailoverErr = nil
		reqLog.Warn("openai.group_failover_activated",
			zap.Int64("source_group_id", groupFailoverPlan.SourceGroup.ID),
			zap.Int64("routing_group_id", candidateGroupID),
			zap.Uint8("stage", uint8(nextStage)),
			zap.String("failure", failure.Error()),
		)
		return true
	}

	for {
		c.Request = c.Request.WithContext(service.WithoutOpenAIGroupModelFallback(c.Request.Context()))
		// Select account supporting the requested model
		reqLog.Debug("openai.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		requiredImageCapability := service.OpenAIImagesCapability("")
		if imageIntent {
			requiredImageCapability = service.OpenAIImagesCapabilityBasic
		}
		selectionStart := time.Now()
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapabilityAndImageModel(
			c.Request.Context(),
			routingGroupID,
			previousResponseID,
			sessionHash,
			reqModel,
			imageRoutingModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			requiredEndpointCapability,
			requiredImageCapability,
			requireCompact,
			requestPlatform,
		)
		service.AddOpsLatencyMs(c, service.OpsRoutingSelectionLatencyMsKey, time.Since(selectionStart).Milliseconds())
		if err != nil {
			reqLog.Warn("openai.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if errors.Is(err, service.ErrKeywordProtectedPreviousResponseAccount) {
				h.handleStreamingAwareErrorWithCode(c, http.StatusConflict, "invalid_request_error", "keyword_account_reconnect_required", "该连续会话绑定的上游账号不允许本次关键词内容，请新建会话后重试", streamStarted)
				return
			}
			if advanceGroupFailover(err, openAIGroupFailoverResponseStarted(streamStarted, failoverSafeAfterCommittedPreamble)) {
				continue
			}
			if len(failedAccountIDs) == 0 {
				if status, errType, code, message, ok := service.OpenAILongContextRoutingErrorDetails(err); ok {
					markOpsRoutingCapacityLimited(c)
					h.handleStreamingAwareErrorWithCode(c, status, errType, code, message, streamStarted)
					return
				}
				if errors.Is(err, service.ErrFallbackQueueFull) {
					h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
					return
				}
				if errors.Is(err, service.ErrNoAvailableCompactAccounts) {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
					h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "compact_not_supported", "No available accounts support /responses/compact", streamStarted)
					return
				}
				if errors.Is(err, service.ErrGatewayGroupUnavailable) {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
					notifyGatewayGroupUnavailable(routingGroupID)
					h.handleStreamingAwareErrorWithCode(c, http.StatusServiceUnavailable, "api_error", service.GatewayGroupUnavailableCode, gatewayGroupUnavailableClientMessage(err), streamStarted)
					return
				}
				if !errors.Is(err, service.ErrNoAvailableAccounts) {
					h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", streamStarted)
					return
				}
				cls := classifyNoAccountErrorFromGin(c, h.gatewayService, routingGroupID, reqModel, reqModel, requestPlatform)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
			} else {
				h.handleFailoverExhaustedSimple(c, 502, streamStarted)
			}
			return
		}
		if selection == nil || selection.Account == nil {
			if advanceGroupFailover(service.ErrNoAvailableAccounts, openAIGroupFailoverResponseStarted(streamStarted, failoverSafeAfterCommittedPreamble)) {
				continue
			}
			cls := classifyNoAccountErrorFromGin(c, h.gatewayService, routingGroupID, reqModel, reqModel, requestPlatform)
			if cls.ModelNotFound {
				h.handleStreamingAwareError(c, cls.Status, cls.ErrType, cls.Message, streamStarted)
				return
			}
			markOpsRoutingCapacityLimited(c)
			if routingGroupID != nil && *routingGroupID > 0 {
				notifyGatewayGroupUnavailable(routingGroupID)
				h.handleStreamingAwareErrorWithCode(c, http.StatusServiceUnavailable, "api_error", service.GatewayGroupUnavailableCode, gatewayGroupUnavailableClientMessage(service.ErrNoAvailableAccounts), streamStarted)
				return
			}
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", streamStarted)
			return
		}
		applyOpenAIGroupModelFallbackSelection(c, reqLog, routingGroupID, reqModel, selection)
		if groupFailoverStage == openAIGroupFailoverStageSource && groupFailoverPlan.TrySourceFallbackBeforeCrossGroup && selection.GroupFallbackUsed {
			groupFailoverStage = openAIGroupFailoverStageSourceFallback
		}
		if previousResponseID != "" && selection != nil && selection.Account != nil {
			reqLog.Debug("openai.account_selected_with_previous_response_id", zap.Int64("account_id", selection.Account.ID))
		}
		reqLog.Debug("openai.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_previous_hit", scheduleDecision.StickyPreviousHit),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, acquired := h.acquireResponsesAccountSlot(c, routingGroupID, sessionHash, selection, reqStream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		// Forward request
		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()
		writerSizeBeforeForward := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
		// 伪造流式首字：上游可能十几秒都不返回响应头，服务层的流式写入被调用时
		// 已经太晚；这里在真正请求上游之前武装定时器（随机延迟后若客户端仍未
		// 收到真实字节，就抢先提交 200 + SSE 注释行）。
		// 注释行计入 keepalive 字节，不算真实正文，因此不阻止安全换号；
		// 同一请求只在首次尝试时武装一次。
		if reqStream {
			stopFakeFirstToken := armFakeFirstTokenBeforeUpstream(c, h.gatewayService.FakeFirstTokenSettings(), requestStart)
			defer stopFakeFirstToken()
		}
		attemptBody := h.deriveOpenAIForwardAttemptBody(reqLog, forwardBodyRef.Bytes(), account, &passthroughFailoverState)
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			forwardCtx := service.WithClientRequestedResponseModel(c.Request.Context(), reqModel)
			return h.gatewayService.Forward(forwardCtx, c, account, attemptBody)
		}()
		cyberBlockKeyHTTP := ""
		if service.GetOpsCyberPolicy(c) != nil {
			cyberBlockKeyHTTP = h.findCyberSessionBlockKeyForAPIKey(c, apiKey, sessionHashBody)
		}
		h.recordCyberPolicyIfMarked(c, apiKey, account, subscription, reqModel, err != nil, cyberBlockKeyHTTP, openAIGroupFailoverUsageFields(c, billingChannelMapping, routingChannelMapping, reqModel, "", targetGroupActive), service.HashUsageRequestPayload(body))
		recordWeightedRouteForwardResult(c, routingGroupID, reqModel, selection, err)
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if err == nil && result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		if err != nil {
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					if failoverRequestCanceled(c.Request.Context()) {
						return
					}
					if service.OpenAICompactKeepaliveAdjustedWrittenSize(c) != writerSizeBeforeForward {
						h.handleFailoverExhausted(c, failoverErr, true)
						return
					}
					// 首输出暂存已确认当前只有 SSE 心跳或协议前导帧。HTTP 200
					// 一旦提交，最终错误必须走流内 response.failed；但这些非语义
					// 字节不能阻止同分组或跨分组的安全故障切换。
					if c.Writer.Written() {
						streamStarted = true
						failoverSafeAfterCommittedPreamble = true
					}
					if failoverErr.ShouldReportAccountScheduleFailure() {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
					}
					if !failoverErr.ShouldRetryNextAccount() {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					// 池模式：同账号重试
					if failoverErr.RetryableOnSameAccount {
						retryLimit := account.GetPoolModeRetryCount()
						if sameAccountRetryAllowedWithError(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
							sameAccountRetryCount[account.ID]++
							retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
							reqLog.Warn("openai.pool_mode_same_account_retry",
								zap.Int64("account_id", account.ID),
								zap.Int("upstream_status", failoverErr.StatusCode),
								zap.Int("retry_limit", retryLimit),
								zap.Int("retry_count", sameAccountRetryCount[account.ID]),
								zap.Duration("retry_delay", retryDelay),
							)
							select {
							case <-c.Request.Context().Done():
								return
							case <-time.After(retryDelay):
							}
							continue
						}
					}
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr
					switchBudget := openAIFailoverSwitchBudget(account, failoverErr, maxAccountSwitches)
					if switchCount >= switchBudget {
						if advanceGroupFailover(failoverErr, openAIGroupFailoverResponseStarted(streamStarted, failoverSafeAfterCommittedPreamble)) {
							continue
						}
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
						if advanceGroupFailover(failoverErr, openAIGroupFailoverResponseStarted(streamStarted, failoverSafeAfterCommittedPreamble)) {
							continue
						}
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					if !waitOpenAIFailoverSwitch(c.Request.Context(), account, failoverErr, switchCount) {
						return
					}
					h.gatewayService.RecordOpenAIAccountSwitch()
					reqLog.Warn("openai.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", switchBudget),
					)
					continue
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated {
					wroteFallback = h.ensureForwardErrorResponse(c, streamStarted)
				}
				fields := []zap.Field{
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				}
				if shouldLogOpenAIForwardFailureAsWarn(c, wroteFallback) {
					reqLog.Warn("openai.forward_failed", fields...)
					return
				}
				reqLog.Error("openai.forward_failed", fields...)
				return
			}
		}
		if result != nil {
			if account.Type == service.AccountTypeOAuth {
				h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(c.Request.Context(), account.ID, result.ResponseHeaders)
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
		}

		// 捕获请求信息（用于异步记录，避免在 goroutine 中访问 gin.Context）
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		sessionID := service.ExtractClientSessionID(c)

		// 使用量记录通过有界 worker 池提交，避免请求热路径创建无界 goroutine。
		cyberBlocked := service.GetOpsCyberPolicy(c) != nil
		h.submitOpenAIUsageRecordTask(c.Request.Context(), result, func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				SessionID:          sessionID,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      h.apiKeyService,
				ChannelUsageFields: openAIGroupFailoverUsageFields(c, billingChannelMapping, routingChannelMapping, reqModel, result.UpstreamModel, targetGroupActive),
				CyberBlocked:       cyberBlocked,
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.openai_gateway.responses"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", reqModel),
					zap.Int64("account_id", account.ID),
				).Error("openai.record_usage_failed", zap.Error(err))
			}
		})
		reqLog.Debug("openai.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func isOpenAIRemoteCompactPath(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	return strings.HasSuffix(normalizedPath, "/responses/compact")
}

func isBareOpenAIResponsesPath(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	switch normalizedPath {
	case EndpointResponses, "/openai/v1/responses", "/responses", "/backend-api/codex/responses":
		return true
	default:
		return false
	}
}

func isOpenAIRemoteCompactionV2Request(body []byte) bool {
	stream, valid := parseOpenAICompatibleStream(body)
	return valid && stream && service.HasCompactionTriggerInInput(body)
}

// normalizeOpenAIResponsesCompactRequest makes path-based and body-signal compact
// requests share the same downstream path, body whitelist and account capability checks.
func (h *OpenAIGatewayHandler) normalizeOpenAIResponsesCompactRequest(c *gin.Context, reqLog *zap.Logger, body []byte) ([]byte, bool) {
	isCompactRequest := service.IsOpenAIResponsesCompactPathForTest(c)
	if !isCompactRequest && isBareOpenAIResponsesPath(c) && service.HasCompactionTriggerInInput(body) {
		stream, streamValid := parseOpenAICompatibleStream(body)
		if streamValid && stream {
			// 官方 v0.1.177 的 native remote compaction v2 保留裸 /responses
			// 流式线路；只有旧版非流式 body-signal 才升级到 /compact。
			service.MarkOpenAINativeCompactionV2(c)
			if reqLog != nil {
				reqLog.Info("codex.remote_compact.native_v2", zap.Bool("client_stream", true))
			}
			return body, true
		}
		c.Request.URL.Path = strings.TrimRight(c.Request.URL.Path, "/") + "/compact"
		isCompactRequest = true
		clientStream := stream
		if clientStream {
			service.MarkOpenAICompactClientStream(c)
		}
		if reqLog != nil {
			reqLog.Info("codex.remote_compact.detected_body_signal", zap.Bool("client_stream", clientStream))
		}
	}
	if !isCompactRequest {
		return body, true
	}
	if compactSeed := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); compactSeed != "" {
		c.Set(service.OpenAICompactSessionSeedKeyForTest(), compactSeed)
	}
	normalizedBody, changed, err := service.NormalizeOpenAICompactRequestBodyForTest(body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to normalize compact request body")
		return nil, false
	}
	if changed {
		body = normalizedBody
	}
	return body, true
}

func (h *OpenAIGatewayHandler) logOpenAIRemoteCompactOutcome(c *gin.Context, startedAt time.Time) {
	if !isOpenAIRemoteCompactPath(c) {
		return
	}

	var (
		ctx    = context.Background()
		path   string
		status int
	)
	if c != nil {
		if c.Request != nil {
			ctx = c.Request.Context()
			if c.Request.URL != nil {
				path = strings.TrimSpace(c.Request.URL.Path)
			}
		}
		if c.Writer != nil {
			status = c.Writer.Status()
		}
	}

	outcome := "failed"
	if status >= 200 && status < 300 {
		outcome = "succeeded"
	}
	if outcome == "succeeded" && c != nil {
		if _, hasStreamError := service.GetOpsStreamError(c); hasStreamError {
			outcome = "failed"
		}
	}
	latencyMs := time.Since(startedAt).Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}

	fields := []zap.Field{
		zap.String("component", "handler.openai_gateway.responses"),
		zap.Bool("remote_compact", true),
		zap.String("compact_outcome", outcome),
		zap.Int("status_code", status),
		zap.Int64("latency_ms", latencyMs),
		zap.String("path", path),
		zap.Bool("force_codex_cli", h != nil && h.cfg != nil && h.cfg.Gateway.ForceCodexCLI),
	}

	if c != nil {
		if userAgent := strings.TrimSpace(c.GetHeader("User-Agent")); userAgent != "" {
			fields = append(fields, zap.String("request_user_agent", userAgent))
		}
		if v, ok := c.Get(opsModelKey); ok {
			if model, ok := v.(string); ok && strings.TrimSpace(model) != "" {
				fields = append(fields, zap.String("request_model", strings.TrimSpace(model)))
			}
		}
		if v, ok := c.Get(opsAccountIDKey); ok {
			if accountID, ok := v.(int64); ok && accountID > 0 {
				fields = append(fields, zap.Int64("account_id", accountID))
			}
		}
		if c.Writer != nil {
			if upstreamRequestID := strings.TrimSpace(c.Writer.Header().Get("x-request-id")); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			} else if upstreamRequestID := strings.TrimSpace(c.Writer.Header().Get("X-Request-Id")); upstreamRequestID != "" {
				fields = append(fields, zap.String("upstream_request_id", upstreamRequestID))
			}
		}
	}

	log := logger.FromContext(ctx).With(fields...)
	if outcome == "succeeded" {
		log.Info("codex.remote_compact.succeeded")
		return
	}
	log.Warn("codex.remote_compact.failed")
}

// Messages handles Anthropic Messages API requests routed to OpenAI platform.
// POST /v1/messages (when group platform is OpenAI)
func (h *OpenAIGatewayHandler) Messages(c *gin.Context) {
	streamStarted := false
	defer h.recoverAnthropicMessagesPanic(c, &streamStarted)

	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.messages",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	// 检查分组是否允许 /v1/messages 调度
	if !allowOpenAICompatibleMessagesDispatchForRequest(c, apiKey) {
		h.anthropicErrorResponse(c, http.StatusForbidden, "permission_error",
			"This group does not allow /v1/messages dispatch")
		return
	}

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.anthropicErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	if !gjson.ValidBytes(body) {
		logRequestBodyParseFailure(reqLog, body, nil)
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	// Messages 兼容路径会把请求转换并可能改写模型；保留客户原始声明模型。
	c.Request = c.Request.WithContext(service.WithClientRequestedResponseModel(c.Request.Context(), reqModel))
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}
	if h.isModelDisabled(reqModel) {
		h.anthropicErrorResponse(c, http.StatusForbidden, "model_disabled", "model is disabled")
		return
	}
	routingModel := service.NormalizeOpenAICompatRequestedModel(reqModel)
	preferredMappedModel := resolveOpenAIMessagesDispatchMappedModelForRequest(c, apiKey, reqModel)
	reqStream := gjson.GetBytes(body, "stream").Bool()

	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	setOpsRequestContext(c, reqModel, reqStream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(reqStream, false)))

	if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolAnthropicMessages, reqModel, body); decision != nil && decision.Blocked {
		h.anthropicErrorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	body = contentModerationForwardBody(c, body)
	bindOpenAIReasoningEffortPolicyForMessagesRequest(c, apiKey, body)

	// 解析渠道级模型映射
	billingChannelMappingMsg, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	routingChannelMappingMsg := billingChannelMappingMsg
	mappedBodyForMessages := newOpenAIModelMappedBodyCache(body, h.gatewayService.ReplaceModelInBody)

	// 绑定错误透传服务，允许 service 层在非 failover 错误场景复用规则。
	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	requestPlatform := openAICompatibleRequestPlatformFromContext(c.Request.Context(), apiKey)
	longContextModels := []string{routingModel, reqModel}
	if preferredMappedModel != "" {
		longContextModels = append([]string{preferredMappedModel}, longContextModels...)
	}
	if routingChannelMappingMsg.Mapped {
		longContextModels = append([]string{routingChannelMappingMsg.MappedModel}, longContextModels...)
	}
	routingCtx, _, routingErr := h.gatewayService.PrepareOpenAILongContextRouting(
		c.Request.Context(), apiKey.Group, requestPlatform, body, longContextModels...,
	)
	if routingErr != nil {
		status, errType, _, message, _ := service.OpenAILongContextRoutingErrorDetails(routingErr)
		h.anthropicStreamingAwareError(c, status, errType, message, streamStarted)
		return
	}
	c.Request = c.Request.WithContext(routingCtx)
	c.Request = c.Request.WithContext(service.WithOpenAIGroupModelFallbackEnabled(c.Request.Context()))

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()
	c.Request = c.Request.WithContext(service.ContextWithOpsRoutingTrace(c.Request.Context()))

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if !h.shouldSkipGatewayLiteBillingEligibility(c) {
		billingStart := time.Now()
		err := h.billingCacheService.CheckBillingEligibilityForRequest(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey), service.EstimateRequestTokensForLimit(body))
		service.AddOpsLatencyMs(c, service.OpsRoutingBillingLatencyMsKey, time.Since(billingStart).Milliseconds())
		if err != nil {
			reqLog.Info("openai_messages.billing_eligibility_check_failed", zap.Error(err))
			status, code, message, retryAfter := billingErrorDetails(err)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.anthropicStreamingAwareError(c, status, code, message, streamStarted)
			return
		}
	}

	groupFailoverPlanMsg, groupFailoverErrMsg := h.gatewayService.ResolveOpenAIGroupFailoverPlan(
		c.Request.Context(),
		apiKey.Group,
		service.OpenAIGroupFailoverRequest{
			Endpoint:       service.OpenAIGroupFailoverEndpointMessages,
			RequestedModel: reqModel,
			APIKey:         apiKey,
		},
	)
	if groupFailoverErrMsg != nil {
		reqLog.Warn("openai_messages.group_failover_plan_failed", zap.Error(groupFailoverErrMsg))
		groupFailoverPlanMsg = service.OpenAIGroupFailoverPlan{Reason: "plan_failed", SourceGroup: apiKey.Group}
	}
	if groupFailoverPlanMsg.Eligible && (groupFailoverPlanMsg.TargetGroup == nil || !groupFailoverPlanMsg.TargetGroup.AllowMessagesDispatch) {
		groupFailoverPlanMsg.Eligible = false
		groupFailoverPlanMsg.Reason = "target_messages_dispatch_disabled"
	}
	if groupFailoverPlanMsg.Eligible {
		c.Request = c.Request.WithContext(service.WithoutOpenAIGroupModelFallbackPolicy(c.Request.Context()))
	}

	sessionPolicyStart := time.Now()
	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	promptCacheKey := h.gatewayService.ExtractSessionID(c, body)
	sessionHash, promptCacheKey = resolveOpenAIMessagesMetadataSession(sessionHash, promptCacheKey, reqModel, body)
	cyberSessionBlocked := h.rejectIfCyberSessionBlocked(c, apiKey, body, reqModel, cyberBlockFormatAnthropic)
	service.AddOpsLatencyMs(c, service.OpsRoutingSessionPolicyLatencyMsKey, time.Since(sessionPolicyStart).Milliseconds())
	if cyberSessionBlocked {
		return
	}

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	effectiveMappedModel := preferredMappedModel
	routingGroupID := apiKey.SchedulerGroupID()
	routingGroupMsg := apiKey.Group
	groupFailoverStageMsg := openAIGroupFailoverStageSource
	targetGroupActiveMsg := false
	var targetGroupReleaseMsg func()
	defer func() {
		if targetGroupReleaseMsg != nil {
			targetGroupReleaseMsg()
		}
	}()
	advanceGroupFailoverMsg := func(failure error, responseStarted bool) bool {
		nextStage, ok := nextOpenAIGroupFailoverStage(groupFailoverPlanMsg, groupFailoverStageMsg, failure, responseStarted)
		if !ok {
			return false
		}

		candidateCtx := c.Request.Context()
		candidateGroup := routingGroupMsg
		candidateTargetActive := false
		var candidateTargetRelease func()
		var candidateGroupID int64
		var transitionErr error
		switch nextStage {
		case openAIGroupFailoverStageTarget:
			var acquired bool
			candidateTargetRelease, acquired, transitionErr = h.concurrencyHelper.TryAcquireFailoverGroupSlot(
				candidateCtx, apiKey.User, subject.UserID, groupFailoverPlanMsg.TargetGroup,
			)
			if transitionErr != nil || !acquired {
				reqLog.Warn("openai_messages.group_failover_target_concurrency_rejected",
					zap.Int64("target_group_id", groupFailoverPlanMsg.TargetGroup.ID),
					zap.Error(transitionErr),
				)
				return false
			}
			candidateCtx, candidateGroupID, transitionErr = service.ApplyOpenAIGroupFailover(candidateCtx, groupFailoverPlanMsg)
			candidateGroup = groupFailoverPlanMsg.TargetGroup
			candidateTargetActive = true
		case openAIGroupFailoverStageSourceFallback:
			if groupFailoverStageMsg == openAIGroupFailoverStageSource && groupFailoverPlanMsg.TrySourceFallbackBeforeCrossGroup {
				candidateCtx = service.WithOpenAIGroupModelFallbackPolicy(candidateCtx)
				candidateCtx = service.WithOpenAIGroupModelFallbackOnly(candidateCtx)
				candidateGroup = groupFailoverPlanMsg.SourceGroup
				candidateGroupID = candidateGroup.ID
			} else {
				candidateCtx, candidateGroupID, transitionErr = service.RestoreOpenAIGroupForModelFallback(candidateCtx, groupFailoverPlanMsg)
				candidateGroup = groupFailoverPlanMsg.SourceGroup
			}
		default:
			return false
		}
		if transitionErr != nil || candidateGroup == nil || candidateGroupID <= 0 {
			if candidateTargetRelease != nil {
				candidateTargetRelease()
			}
			reqLog.Warn("openai_messages.group_failover_transition_failed", zap.Error(transitionErr))
			return false
		}
		if !candidateGroup.AllowMessagesDispatch {
			if candidateTargetRelease != nil {
				candidateTargetRelease()
			}
			return false
		}

		candidateGroupIDRef := candidateGroupID
		candidateMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(candidateCtx, &candidateGroupIDRef, reqModel)
		candidateEffectiveMappedModel := strings.TrimSpace(candidateGroup.ResolveMessagesDispatchModel(reqModel))
		longContextModels := []string{routingModel, reqModel}
		if candidateEffectiveMappedModel != "" {
			longContextModels = append([]string{candidateEffectiveMappedModel}, longContextModels...)
		}
		if candidateMapping.Mapped {
			longContextModels = append([]string{candidateMapping.MappedModel}, longContextModels...)
		}
		var routingErr error
		candidateCtx, _, routingErr = h.gatewayService.PrepareOpenAILongContextRouting(
			candidateCtx, candidateGroup, requestPlatform, body, longContextModels...,
		)
		if routingErr != nil {
			if candidateTargetRelease != nil {
				candidateTargetRelease()
			}
			reqLog.Warn("openai_messages.group_failover_long_context_rejected",
				zap.Int64("target_group_id", candidateGroupID),
				zap.Error(routingErr),
			)
			return false
		}
		if !candidateTargetActive && targetGroupReleaseMsg != nil {
			targetGroupReleaseMsg()
			targetGroupReleaseMsg = nil
		}
		if candidateTargetActive {
			targetGroupReleaseMsg = candidateTargetRelease
		}

		c.Request = c.Request.WithContext(candidateCtx)
		routingGroupID = &candidateGroupIDRef
		routingGroupMsg = candidateGroup
		routingChannelMappingMsg = candidateMapping
		effectiveMappedModel = candidateEffectiveMappedModel
		groupFailoverStageMsg = nextStage
		targetGroupActiveMsg = candidateTargetActive
		failedAccountIDs = make(map[int64]struct{})
		sameAccountRetryCount = make(map[int64]int)
		switchCount = 0
		lastFailoverErr = nil
		reqLog.Warn("openai_messages.group_failover_activated",
			zap.Int64("source_group_id", groupFailoverPlanMsg.SourceGroup.ID),
			zap.Int64("routing_group_id", candidateGroupID),
			zap.Uint8("stage", uint8(nextStage)),
			zap.String("failure", failure.Error()),
		)
		return true
	}

	for {
		c.Request = c.Request.WithContext(service.WithoutOpenAIGroupModelFallback(c.Request.Context()))
		currentRoutingModel := routingModel
		if effectiveMappedModel != "" {
			currentRoutingModel = effectiveMappedModel
		}
		reqLog.Debug("openai_messages.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selectionStart := time.Now()
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			routingGroupID,
			"", // no previous_response_id
			sessionHash,
			currentRoutingModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			requestPlatform,
		)
		service.AddOpsLatencyMs(c, service.OpsRoutingSelectionLatencyMsKey, time.Since(selectionStart).Milliseconds())
		if err != nil {
			reqLog.Warn("openai_messages.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if advanceGroupFailoverMsg(err, streamStarted) {
				continue
			}
			if len(failedAccountIDs) == 0 {
				if status, errType, _, message, ok := service.OpenAILongContextRoutingErrorDetails(err); ok {
					markOpsRoutingCapacityLimited(c)
					h.anthropicStreamingAwareError(c, status, errType, message, streamStarted)
					return
				}
				if errors.Is(err, service.ErrFallbackQueueFull) {
					h.anthropicStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
					return
				}
				if err != nil {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
					h.anthropicStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", streamStarted)
					return
				}
			} else {
				if lastFailoverErr != nil {
					h.handleAnthropicFailoverExhausted(c, lastFailoverErr, streamStarted)
				} else {
					h.anthropicStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
				}
				return
			}
		}
		if selection == nil || selection.Account == nil {
			if advanceGroupFailoverMsg(service.ErrNoAvailableAccounts, streamStarted) {
				continue
			}
			markOpsRoutingCapacityLimited(c)
			h.anthropicStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", streamStarted)
			return
		}
		applyOpenAIGroupModelFallbackSelection(c, reqLog, routingGroupID, reqModel, selection)
		if groupFailoverStageMsg == openAIGroupFailoverStageSource && groupFailoverPlanMsg.TrySourceFallbackBeforeCrossGroup && selection.GroupFallbackUsed {
			groupFailoverStageMsg = openAIGroupFailoverStageSourceFallback
		}
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai_messages.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		_ = scheduleDecision
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, acquired := h.acquireResponsesAccountSlot(c, routingGroupID, sessionHash, selection, reqStream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()

		defaultMappedModel := strings.TrimSpace(effectiveMappedModel)
		// 应用渠道模型映射到请求体
		forwardBody := mappedBodyForMessages(routingChannelMappingMsg.Mapped, routingChannelMappingMsg.MappedModel)
		writerSizeBeforeForward := c.Writer.Size()
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.ForwardAsAnthropic(c.Request.Context(), c, account, forwardBody, promptCacheKey, defaultMappedModel)
		}()
		cyberBlockKeyMsg := ""
		if service.GetOpsCyberPolicy(c) != nil {
			cyberBlockKeyMsg = h.findCyberSessionBlockKeyForAPIKey(c, apiKey, body)
		}
		h.recordCyberPolicyIfMarked(c, apiKey, account, subscription, reqModel, err != nil, cyberBlockKeyMsg, openAIGroupFailoverUsageFields(c, billingChannelMappingMsg, routingChannelMappingMsg, reqModel, "", targetGroupActiveMsg), service.HashUsageRequestPayload(body))
		recordWeightedRouteForwardResult(c, routingGroupID, reqModel, selection, err)
		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if err == nil && result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		if err != nil {
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai_messages.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					if failoverRequestCanceled(c.Request.Context()) {
						return
					}
					if c.Writer.Size() != writerSizeBeforeForward {
						h.handleAnthropicFailoverExhausted(c, failoverErr, true)
						return
					}
					if failoverErr.ShouldReportAccountScheduleFailure() {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMappingMsg, currentRoutingModel))
					}
					if !failoverErr.ShouldRetryNextAccount() {
						h.handleAnthropicFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					// 池模式：同账号重试
					if failoverErr.RetryableOnSameAccount {
						retryLimit := account.GetPoolModeRetryCount()
						if sameAccountRetryAllowedWithError(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
							sameAccountRetryCount[account.ID]++
							retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
							reqLog.Warn("openai_messages.pool_mode_same_account_retry",
								zap.Int64("account_id", account.ID),
								zap.Int("upstream_status", failoverErr.StatusCode),
								zap.Int("retry_limit", retryLimit),
								zap.Int("retry_count", sameAccountRetryCount[account.ID]),
								zap.Duration("retry_delay", retryDelay),
							)
							select {
							case <-c.Request.Context().Done():
								return
							case <-time.After(retryDelay):
							}
							continue
						}
					}
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverErr = failoverErr
					switchBudget := openAIFailoverSwitchBudget(account, failoverErr, maxAccountSwitches)
					if switchCount >= switchBudget {
						if advanceGroupFailoverMsg(failoverErr, streamStarted) {
							continue
						}
						h.handleAnthropicFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
						if advanceGroupFailoverMsg(failoverErr, streamStarted) {
							continue
						}
						h.handleAnthropicFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					if !waitOpenAIFailoverSwitch(c.Request.Context(), account, failoverErr, switchCount) {
						return
					}
					h.gatewayService.RecordOpenAIAccountSwitch()
					reqLog.Warn("openai_messages.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", switchBudget),
					)
					continue
				}
				if result != nil && result.ClientDisconnect {
					reqLog.Info("openai_messages.client_disconnected",
						zap.Int64("account_id", account.ID),
						zap.Error(err),
					)
					return
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMappingMsg, currentRoutingModel))
				wroteFallback := h.ensureAnthropicErrorResponse(c, streamStarted)
				reqLog.Warn("openai_messages.forward_failed",
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Error(err),
				)
				return
			}
		}
		if result != nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMappingMsg, currentRoutingModel))
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMappingMsg, currentRoutingModel))
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		requestPayloadHash := service.HashUsageRequestPayload(body)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
		sessionID := service.ExtractClientSessionID(c)

		cyberBlocked := service.GetOpsCyberPolicy(c) != nil
		h.submitOpenAIUsageRecordTask(c.Request.Context(), result, func(ctx context.Context) {
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:             result,
				APIKey:             apiKey,
				User:               apiKey.User,
				Account:            account,
				Subscription:       subscription,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIP,
				SessionID:          sessionID,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      h.apiKeyService,
				ChannelUsageFields: openAIGroupFailoverUsageFields(c, billingChannelMappingMsg, routingChannelMappingMsg, reqModel, result.UpstreamModel, targetGroupActiveMsg),
				CyberBlocked:       cyberBlocked,
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.openai_gateway.messages"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", reqModel),
					zap.Int64("account_id", account.ID),
				).Error("openai_messages.record_usage_failed", zap.Error(err))
			}
		})
		reqLog.Debug("openai_messages.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func resolveOpenAIMessagesMetadataSession(sessionHash, promptCacheKey, reqModel string, body []byte) (string, string) {
	// Anthropic metadata.user_id 只作为账号粘性信号。上游 GPT/Codex 缓存键
	// 交给 ForwardAsAnthropic 从 cache_control 或完整消息 digest 派生，避免
	// 固定 metadata key 压住后续 turn 的缓存滚动。
	if sessionHash != "" {
		return sessionHash, promptCacheKey
	}
	if userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()); userID != "" {
		seed := reqModel + "-" + userID
		sessionHash = service.DeriveSessionHashFromSeed(seed)
	}
	return sessionHash, promptCacheKey
}

// anthropicErrorResponse writes an error in Anthropic Messages API format.
func (h *OpenAIGatewayHandler) anthropicErrorResponse(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// anthropicStreamingAwareError handles errors that may occur during streaming,
// using Anthropic SSE error format.
func (h *OpenAIGatewayHandler) anthropicStreamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	if streamStarted {
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			errPayload, _ := json.Marshal(gin.H{
				"type": "error",
				"error": gin.H{
					"type":    errType,
					"message": message,
				},
			})
			fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", errPayload) //nolint:errcheck
			flusher.Flush()
		}
		return
	}
	h.anthropicErrorResponse(c, status, errType, message)
}

// handleAnthropicFailoverExhausted maps upstream failover errors to Anthropic format.
func (h *OpenAIGatewayHandler) handleAnthropicFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool) {
	if failoverErr == nil {
		h.anthropicStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
		return
	}
	if failoverErr.IsCredentialFailure() {
		h.anthropicStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", service.GrokCredentialUnavailableClientMessage, streamStarted)
		return
	}
	status, errType, errMsg := h.mapUpstreamError(failoverErr.StatusCode)
	h.anthropicStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

// ensureAnthropicErrorResponse writes a fallback Anthropic error if no response was written.
func (h *OpenAIGatewayHandler) ensureAnthropicErrorResponse(c *gin.Context, streamStarted bool) bool {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return false
	}
	h.anthropicStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
	return true
}

func (h *OpenAIGatewayHandler) validateFunctionCallOutputRequest(c *gin.Context, body []byte, reqLog *zap.Logger) bool {
	if !gjson.GetBytes(body, `input.#(type=="function_call_output")`).Exists() {
		return true
	}

	validation := service.ValidateFunctionCallOutputContextBytes(body)
	if !validation.HasFunctionCallOutput {
		return true
	}

	previousResponseID := gjson.GetBytes(body, "previous_response_id").String()
	if strings.TrimSpace(previousResponseID) != "" || validation.HasToolCallContext {
		return true
	}

	if validation.HasFunctionCallOutputMissingCallID {
		reqLog.Warn("openai.request_validation_failed",
			zap.String("reason", "function_call_output_missing_call_id"),
		)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2")
		return false
	}
	if validation.HasItemReferenceForAllCallIDs {
		return true
	}

	reqLog.Warn("openai.request_validation_failed",
		zap.String("reason", "function_call_output_missing_item_reference"),
	)
	h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires item_reference ids matching each call_id on HTTP requests; continuation via previous_response_id is only supported on Responses WebSocket v2")
	return false
}

// normalizeCodexDelegationBootstrap 把 Codex 委托（delegation）工具的首轮引导请求
// 从 function_call_output 形态还原成普通 user 消息。Codex CLI 在创建委托线程的
// 第一轮会把线程描述 XML 直接塞进 function_call_output.output 且不带 call_id
// （此时线程尚未产生任何工具调用，call_id 不存在）；HTTP Responses 协议要求
// function_call_output 必须携带 call_id，直接转发会被 400 拒绝。判定从严：
// 只要存在任何调用/引用锚点（item_reference、*_call、带 call_id 的 *_call_output）
// 或工具名、输出信封不符，就不做改写，按普通请求走原校验。
func normalizeCodexDelegationBootstrap(body []byte) ([]byte, bool) {
	return normalizeCodexCallOutputBootstrap(body, isCodexDelegationCandidate)
}

// normalizeCodexAutomationBootstrap [官方 v0.2.0] 归一化 Codex 定时自动化
// （scheduled automation）的启动请求：automation_update 的 function_call_output
// 缺 call_id 时上游拒收，转成等价的 user message 后放行。
func normalizeCodexAutomationBootstrap(body []byte) ([]byte, bool) {
	return normalizeCodexCallOutputBootstrap(body, isCodexAutomationCandidate)
}

func normalizeCodexCallOutputBootstrap(body []byte, isCandidate func(map[string]any) bool) ([]byte, bool) {
	if !hasUniqueJSONMembers(body) {
		return body, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		return body, false
	}
	if previousResponseID, exists := request["previous_response_id"]; exists {
		value, ok := previousResponseID.(string)
		if !ok || strings.TrimSpace(value) != "" {
			return body, false
		}
	}
	input, ok := request["input"].([]any)
	if !ok {
		return body, false
	}

	// 任何调用/引用锚点都会让无 call_id 的 output 产生歧义。Responses 内建
	// 类型都遵循 *_call / *_call_output 命名，按线上类型形状分类而不是维护
	// 一份不完整的名单。
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := stringField(item, "type")
		if typ == "item_reference" || strings.HasSuffix(typ, "_call") {
			return body, false
		}
		if isResponsesCallOutputType(typ) {
			callIDValue, exists := item["call_id"]
			callID, isString := callIDValue.(string)
			if exists && (!isString || strings.TrimSpace(callID) != "") {
				return body, false
			}
			if !isCandidate(item) {
				return body, false
			}
		}
	}

	changed := false
	for i, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || !isCandidate(item) {
			continue
		}
		output, ok := item["output"].(string)
		if !ok {
			continue
		}
		input[i] = map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": output,
			}},
		}
		changed = true
	}
	if !changed {
		return body, false
	}
	normalized, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return normalized, true
}

// hasUniqueJSONMembers 校验请求体没有重复键。gjson/标准解码都取重复键的最后
// 一个值，而上游（如部分客户端代理）可能对重复键先来后到取值不同；改写建立在
// 解码结果与线上语义一致的前提上，重复键直接放弃规范化。
func hasUniqueJSONMembers(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if !consumeUniqueJSONValue(decoder) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func consumeUniqueJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}

	switch delim {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false
			}
			key, ok := keyToken.(string)
			if !ok {
				return false
			}
			if _, duplicate := members[key]; duplicate {
				return false
			}
			members[key] = struct{}{}
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for decoder.More() {
			if !consumeUniqueJSONValue(decoder) {
				return false
			}
		}
		end, err := decoder.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}

func isResponsesCallOutputType(typ string) bool {
	return strings.HasSuffix(typ, "_call_output") || typ == "tool_search_output"
}

func isCodexDelegationCandidate(item map[string]any) bool {
	if stringField(item, "type") != "function_call_output" ||
		!isCodexDelegationTool(stringField(item, "namespace"), stringField(item, "name")) {
		return false
	}
	output, ok := item["output"].(string)
	return ok && validCodexDelegationEnvelope(output)
}

func stringField(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func isCodexDelegationTool(namespace, name string) bool {
	return (namespace == "codex_app" || namespace == "codex_tui") &&
		(name == "create_thread" || name == "send_message_to_thread")
}

// isCodexAutomationCandidate [官方 v0.2.0] 定时自动化启动输出的候选判定：
// 仅 codex_app 命名空间下的 automation_update，且 output 形如固定信头。
func isCodexAutomationCandidate(item map[string]any) bool {
	if stringField(item, "type") != "function_call_output" ||
		stringField(item, "namespace") != "codex_app" ||
		stringField(item, "name") != "automation_update" {
		return false
	}
	output, ok := item["output"].(string)
	return ok && validCodexAutomationBootstrap(output)
}

// validCodexAutomationBootstrap 校验定时自动化启动输出的固定信头：
//
//	Automation: <标题>
//	Automation ID: <安全 ID>
//	Automation memory: $CODEX_HOME/automations/<ID>/memory.md
//	Last run: never | <RFC3339Nano> (<epoch 毫秒>)
//	<空行>
//	<非空提示词>
//
// memory 路径必须与 ID 匹配，时间戳的 epoch 毫秒必须与 RFC3339 部分一致，
// 防止把任意输出误判为自动化引导。
func validCodexAutomationBootstrap(value string) bool {
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	if strings.ContainsRune(normalized, '\r') {
		return false
	}
	lines := strings.Split(normalized, "\n")
	if len(lines) < 6 {
		return false
	}
	if _, ok := codexAutomationHeaderValue(lines[0], "Automation: "); !ok {
		return false
	}
	automationID, ok := codexAutomationHeaderValue(lines[1], "Automation ID: ")
	if !ok || !validCodexAutomationID(automationID) {
		return false
	}
	expectedMemory := "Automation memory: $CODEX_HOME/automations/" + automationID + "/memory.md"
	if lines[2] != expectedMemory {
		return false
	}
	lastRun, ok := codexAutomationHeaderValue(lines[3], "Last run: ")
	if !ok || !validCodexAutomationLastRun(lastRun) || lines[4] != "" {
		return false
	}
	return strings.TrimSpace(strings.Join(lines[5:], "\n")) != ""
}

func codexAutomationHeaderValue(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	value := strings.TrimPrefix(line, prefix)
	return value, value != "" && strings.TrimSpace(value) == value
}

func validCodexAutomationID(value string) bool {
	if len(value) == 0 || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func validCodexAutomationLastRun(value string) bool {
	if value == "never" {
		return true
	}
	separator := strings.LastIndex(value, " (")
	if separator <= 0 || !strings.HasSuffix(value, ")") {
		return false
	}
	runAt, err := time.Parse(time.RFC3339Nano, value[:separator])
	if err != nil {
		return false
	}
	epochMillis, err := strconv.ParseInt(value[separator+2:len(value)-1], 10, 64)
	return err == nil && runAt.UnixMilli() == epochMillis
}

// validCodexDelegationEnvelope 严格校验委托引导输出的 XML 信封：根元素必须是
// codex_delegation，仅允许 source_thread_id / input 两个子元素（各一个、非空、
// 无属性、无注释、根外无杂文本）。委托工具的合法输出只有这一种形状，任何
// 偏差都说明这不是委托引导而是普通工具输出，不能改写。
func validCodexDelegationEnvelope(value string) bool {
	decoder := xml.NewDecoder(strings.NewReader(value))
	var rootSeen, sourceSeen, inputSeen bool
	var childName string
	var childText bytes.Buffer
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rootSeen && depth == 0 && sourceSeen && inputSeen
		}
		if err != nil {
			return false
		}
		switch current := token.(type) {
		case xml.StartElement:
			depth++
			if current.Name.Space != "" || len(current.Attr) != 0 || (depth == 1 && current.Name.Local != "codex_delegation") || depth > 2 {
				return false
			}
			if depth == 1 {
				if rootSeen {
					return false
				}
				rootSeen = true
				continue
			}
			if current.Name.Local != "source_thread_id" && current.Name.Local != "input" {
				return false
			}
			childName = current.Name.Local
			childText.Reset()
		case xml.EndElement:
			if current.Name.Space != "" {
				return false
			}
			if depth == 2 {
				if current.Name.Local != childName || strings.TrimSpace(childText.String()) == "" {
					return false
				}
				if childName == "source_thread_id" {
					if sourceSeen {
						return false
					}
					sourceSeen = true
				} else {
					if inputSeen {
						return false
					}
					inputSeen = true
				}
				childName = ""
			}
			depth--
			if depth < 0 {
				return false
			}
		case xml.CharData:
			if depth == 2 {
				_, _ = childText.Write(current)
			} else if len(bytes.TrimSpace(current)) != 0 {
				return false
			}
		case xml.Comment:
			return false
		case xml.ProcInst, xml.Directive:
			return false
		}
	}
}

func (h *OpenAIGatewayHandler) acquireResponsesUserSlot(
	c *gin.Context,
	userID int64,
	userConcurrency int,
	reqStream bool,
	streamStarted *bool,
	reqLog *zap.Logger,
) (func(), bool) {
	ctx := c.Request.Context()
	startedAt := time.Now()
	userReleaseFunc, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, userID, userConcurrency, reqStream, streamStarted)
	service.AddOpsLatencyMs(c, service.OpsRoutingUserSlotLatencyMsKey, time.Since(startedAt).Milliseconds())
	if err != nil {
		reqLog.Warn("openai.user_slot_acquire_failed", zap.Error(err))
		h.handleConcurrencyError(c, err, "user", *streamStarted)
		return nil, false
	}
	return wrapReleaseOnDone(ctx, userReleaseFunc), true
}

// applyOpenAIGroupModelFallbackSelection applies a scheduler-approved target
// without rewriting the client body. Forwarding therefore keeps the requested
// model for response declaration and billing while account mapping receives the
// target that was actually selected as the emergency fallback.
func applyOpenAIGroupModelFallbackSelection(
	c *gin.Context,
	reqLog *zap.Logger,
	groupID *int64,
	requestedModel string,
	selection *service.AccountSelectionResult,
) {
	if c == nil || c.Request == nil || groupID == nil || *groupID <= 0 || selection == nil || !selection.GroupFallbackUsed {
		return
	}
	targetModel := strings.TrimSpace(selection.GroupFallbackModel)
	if targetModel == "" {
		return
	}
	c.Request = c.Request.WithContext(service.WithOpenAIGroupModelFallback(
		c.Request.Context(), *groupID, requestedModel, selection.GroupFallbackSource, targetModel,
	))
	if reqLog != nil {
		reqLog.Info("openai.group_model_fallback_active",
			zap.Int64("group_id", *groupID),
			zap.String("requested_model", requestedModel),
			zap.String("source_model", selection.GroupFallbackSource),
			zap.String("fallback_model", targetModel),
			zap.Int64("account_id", selectionAccountIDForFallbackLog(selection)),
		)
	}
}

func selectionAccountIDForFallbackLog(selection *service.AccountSelectionResult) int64 {
	if selection == nil || selection.Account == nil {
		return 0
	}
	return selection.Account.ID
}

// recordWeightedRouteForwardResult 在每次上游转发结束后记录智能分流目标的
// 健康：故障转移类失败按信号分级冷却目标（404/plan-gated 硬冷却，5xx 类
// 连击渐进冷却），成功立即恢复。仅当本请求实际走了分流目标（分组兜底选择
// 生效）才记录；普通单目标映射与直连请求零开销跳过。
func recordWeightedRouteForwardResult(
	c *gin.Context,
	groupID *int64,
	requestedModel string,
	selection *service.AccountSelectionResult,
	forwardErr error,
) {
	if c == nil || groupID == nil || *groupID <= 0 || selection == nil || !selection.GroupFallbackUsed {
		return
	}
	target := strings.TrimSpace(selection.GroupFallbackModel)
	if target == "" {
		return
	}
	var failoverErr *service.UpstreamFailoverError
	if forwardErr != nil && errors.As(forwardErr, &failoverErr) {
		service.RecordWeightedModelRouteUpstreamFailure(*groupID, requestedModel, target, failoverErr.StatusCode, failoverErr.ResponseBody)
		return
	}
	if forwardErr == nil {
		service.RecordWeightedModelRouteUpstreamSuccess(*groupID, requestedModel, target)
	}
}

func (h *OpenAIGatewayHandler) acquireResponsesAccountSlot(
	c *gin.Context,
	groupID *int64,
	sessionHash string,
	selection *service.AccountSelectionResult,
	reqStream bool,
	streamStarted *bool,
	reqLog *zap.Logger,
) (func(), bool) {
	startedAt := time.Now()
	defer func() {
		service.AddOpsLatencyMs(c, service.OpsRoutingPostAccountSlotLatencyMsKey, time.Since(startedAt).Milliseconds())
	}()
	if selection == nil || selection.Account == nil {
		markOpsRoutingCapacityLimited(c)
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", *streamStarted)
		return nil, false
	}

	ctx := c.Request.Context()
	account := selection.Account
	if selection.Acquired {
		// The scheduler normally performs this check itself, but a slot can be
		// acquired from a stale selection after the quota snapshot changes. Keep
		// the handler as the final protocol boundary so no request can leave
		// without a durable Tail-Burst admission marker.
		if !h.gatewayService.AdmitCodexTailBurstAfterSlot(ctx, account) {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			reqLog.Debug("openai.account_tail_burst_admission_rejected", zap.Int64("account_id", account.ID))
			markOpsRoutingCapacityLimited(c)
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", *streamStarted)
			return nil, false
		}
		return wrapReleaseOnDone(ctx, selection.ReleaseFunc), true
	}
	if selection.WaitPlan == nil {
		markOpsRoutingCapacityLimited(c)
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", *streamStarted)
		return nil, false
	}

	fastReleaseFunc, fastAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(
		ctx,
		account.ID,
		selection.WaitPlan.MaxConcurrency,
	)
	if err != nil {
		reqLog.Warn("openai.account_slot_quick_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.handleConcurrencyError(c, err, "account", *streamStarted)
		return nil, false
	}
	if fastAcquired {
		if !h.gatewayService.AdmitCodexTailBurstAfterSlot(ctx, account) {
			if fastReleaseFunc != nil {
				fastReleaseFunc()
			}
			reqLog.Debug("openai.account_tail_burst_admission_rejected", zap.Int64("account_id", account.ID))
			markOpsRoutingCapacityLimited(c)
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", *streamStarted)
			return nil, false
		}
		if err := h.gatewayService.BindStickySession(ctx, groupID, sessionHash, account.ID); err != nil {
			reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
		return wrapReleaseOnDone(ctx, fastReleaseFunc), true
	}

	accountReleaseFunc, err := h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
		c,
		account.ID,
		selection.WaitPlan.MaxConcurrency,
		selection.WaitPlan.Timeout,
		selection.WaitPlan.MaxWaiting,
		reqStream,
		streamStarted,
	)
	if err != nil {
		reqLog.Warn("openai.account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		h.handleConcurrencyError(c, err, "account", *streamStarted)
		return nil, false
	}
	if !h.gatewayService.AdmitCodexTailBurstAfterSlot(ctx, account) {
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		reqLog.Debug("openai.account_tail_burst_admission_rejected", zap.Int64("account_id", account.ID))
		markOpsRoutingCapacityLimited(c)
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", *streamStarted)
		return nil, false
	}

	if err := h.gatewayService.BindStickySession(ctx, groupID, sessionHash, account.ID); err != nil {
		reqLog.Warn("openai.bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}
	return wrapReleaseOnDone(ctx, accountReleaseFunc), true
}

// ResponsesWebSocket handles OpenAI Responses API WebSocket ingress endpoint
// GET /openai/v1/responses (Upgrade: websocket)
func (h *OpenAIGatewayHandler) ResponsesWebSocket(c *gin.Context) {
	if !isOpenAIWSUpgradeRequest(c.Request) {
		h.errorResponse(c, http.StatusUpgradeRequired, "invalid_request_error", "WebSocket upgrade required (Upgrade: websocket)")
		return
	}
	setOpenAIClientTransportWS(c)

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	reqLog := requestLogger(
		c,
		"handler.openai_gateway.responses_ws",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.Bool("openai_ws_mode", true),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}
	reqLog.Info("openai.websocket_ingress_started")
	clientIP := ip.GetClientIP(c)
	userAgent := strings.TrimSpace(c.GetHeader("User-Agent"))
	sessionID := service.ExtractClientSessionID(c)
	clientLifecycleCtx := c.Request.Context()
	ctx := clientLifecycleCtx
	wsBillingConnectionID, hasWSBillingConnectionID := gatewaylite.CanonicalRequestIDFromContext(ctx)
	if !hasWSBillingConnectionID {
		// Non gateway-lite deployments do not have a quota reservation ID, so
		// create one connection-scoped fallback once (not once per callback).
		wsBillingConnectionID = "generated:" + uuid.New().String()
	}
	maxIngressConnections := 0
	if h.cfg != nil {
		maxIngressConnections = h.cfg.Gateway.OpenAIWS.MaxIngressConnectionsPerAPIKey
	}
	ingressLease, ingressLeaseAcquired, ingressLeaseErr := h.concurrencyHelper.AcquireOpenAIWSIngressLease(ctx, apiKey.WSIngressConcurrencyID(ctx), maxIngressConnections)
	if ingressLeaseErr != nil {
		reqLog.Error("openai.websocket_ingress_lease_acquire_failed", zap.Error(ingressLeaseErr))
		h.errorResponse(c, http.StatusServiceUnavailable, "service_unavailable", "WebSocket ingress capacity is temporarily unavailable")
		return
	}
	if !ingressLeaseAcquired {
		reqLog.Info("openai.websocket_ingress_capacity_rejected", zap.Int("max_ingress_connections_per_api_key", maxIngressConnections))
		c.Header("Retry-After", "5")
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Too many open WebSocket connections, please retry later")
		return
	}
	if ingressLease != nil {
		defer ingressLease.Release()
		ctx = ingressLease.Context()
		c.Request = c.Request.WithContext(ctx)
	}

	wsConn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{
		CompressionMode: coderws.CompressionContextTakeover,
	})
	if err != nil {
		reqLog.Warn("openai.websocket_accept_failed",
			zap.Error(err),
			zap.String("client_ip", clientIP),
			zap.String("request_user_agent", userAgent),
			zap.String("upgrade_header", strings.TrimSpace(c.GetHeader("Upgrade"))),
			zap.String("connection_header", strings.TrimSpace(c.GetHeader("Connection"))),
			zap.String("sec_websocket_version", strings.TrimSpace(c.GetHeader("Sec-WebSocket-Version"))),
			zap.Bool("has_sec_websocket_key", strings.TrimSpace(c.GetHeader("Sec-WebSocket-Key")) != ""),
		)
		return
	}
	defer func() {
		_ = wsConn.CloseNow()
	}()
	wsConn.SetReadLimit(service.ResolveOpenAIWSClientReadLimitBytes(h.cfg))

	firstMessageTimeout := resolveOpenAIWSClientFirstMessageTimeout(h.cfg)
	msgType, firstMessage, err := service.ReadOpenAIWSClientMessage(
		ctx,
		wsConn,
		firstMessageTimeout,
		coderws.StatusPolicyViolation,
		"missing first response.create message",
	)
	if err != nil {
		if errors.Is(context.Cause(ctx), service.ErrOpenAIWSIngressLeaseLost) {
			reqLog.Warn("openai.websocket_ingress_lease_lost_before_first_message", zap.Error(err))
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect")
			return
		}
		closeStatus, closeReason := summarizeWSCloseErrorForLog(err)
		reqLog.Warn("openai.websocket_read_first_message_failed",
			zap.Error(err),
			zap.String("client_ip", clientIP),
			zap.String("close_status", closeStatus),
			zap.String("close_reason", closeReason),
			zap.Duration("read_timeout", firstMessageTimeout),
		)
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "missing first response.create message")
		return
	}
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusUnsupportedData, "unsupported websocket message type")
		return
	}
	var firstEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(firstMessage, &firstEnvelope); err != nil || firstEnvelope == nil {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "invalid JSON payload")
		return
	}
	var firstEventType string
	if rawType, exists := firstEnvelope["type"]; !exists || json.Unmarshal(rawType, &firstEventType) != nil || strings.TrimSpace(firstEventType) != "response.create" {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "first websocket event must be response.create")
		return
	}

	reqModel := strings.TrimSpace(gjson.GetBytes(firstMessage, "model").String())
	if reqModel == "" {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "model is required in first response.create payload")
		return
	}
	// 握手没有正文；首帧必须同时检查主站 Key、新分组白名单及旧模型配置。
	if _, message := middleware2.GatewayModelPermissionError(apiKey, reqModel); message != "" {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, message)
		return
	}
	// 保存客户端首帧的原始模型。内容审计可能替换 request context，后续
	// 分组/账号映射也会改写上游模型；响应声明策略必须始终能取回客户模型。
	ctx = service.WithClientRequestedResponseModel(ctx, reqModel)
	c.Request = c.Request.WithContext(ctx)
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	ctx = c.Request.Context()
	if apiKey.Group != nil && apiKey.Group.Platform == service.PlatformComposite {
		platform, resolved := service.ResolvedTargetPlatformFromContext(ctx)
		if !resolved || !isResponsesWebSocketCompositePlatform(platform) {
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "Responses WebSocket API only supports OpenAI-compatible models for composite groups")
			return
		}
	} else if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}
	previousResponseID := strings.TrimSpace(gjson.GetBytes(firstMessage, "previous_response_id").String())
	previousResponseIDKind := service.ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
	if previousResponseID != "" && previousResponseIDKind == service.OpenAIPreviousResponseIDKindMessageID {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "previous_response_id must be a response.id (resp_*), not a message id")
		return
	}
	reqLog = reqLog.With(
		zap.Bool("ws_ingress", true),
		zap.String("model", reqModel),
		zap.Bool("has_previous_response_id", previousResponseID != ""),
		zap.String("previous_response_id_kind", previousResponseIDKind),
	)
	setOpsRequestContext(c, reqModel, true)
	setOpsEndpointContext(c, "", int16(service.RequestTypeWSV2))

	// 首帧与后续帧使用同一传输/资金边界，必须先于 AI 外发和业务额度准入。
	// 捕获当前 ctx 变量而非快照，后续轮次仍能看到实时资金与审查配置。
	strictClientFrames := func() (bool, error) {
		if gatewaylite.HasOAuthInvocationCredential(ctx) || gatewaylite.HasFundingTurnAdmission(ctx) || gatewaylite.HasRequestFunding(c.Request.Context()) {
			return true, nil
		}
		if h.contentModerationService == nil {
			return false, nil
		}
		cfg, err := h.contentModerationService.GetConfig(ctx)
		if err != nil || cfg == nil {
			return true, errors.Join(errors.New("content moderation config is unavailable"), err)
		}
		return cfg.KeywordBlockingMode == service.ContentModerationKeywordModeAIOnly, nil
	}
	onStrictResponseCreateRejected := func(payload []byte, originalModel string) {
		h.notifyResponsesWSStrictRejection(clientLifecycleCtx, c, wsConn, apiKey, subject, originalModel, payload)
	}
	if err := service.ValidateOpenAIWSFirstClientFrame(&service.OpenAIWSIngressHooks{
		StrictClientFrames: strictClientFrames, OnStrictResponseCreateRejected: onStrictResponseCreateRejected,
	}, msgType, firstMessage, reqModel); err != nil {
		var closeErr *service.OpenAIWSClientCloseError
		if errors.As(err, &closeErr) {
			closeOpenAIClientWSWithContext(ctx, wsConn, closeErr.StatusCode(), closeErr.Reason())
		} else {
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "invalid websocket request payload")
		}
		return
	}

	decision := h.checkResponsesWSContentModeration(c, reqLog, apiKey, subject, reqModel, firstMessage)
	// 内容审计可能只为当前连接切换分组，并把最终分组、费率及账号保护策略
	// 写入 request context。后续 WS 映射、选号和逐轮回调必须统一捕获这份新上下文，
	// 不能继续使用 Upgrade 完成前保存的旧 ctx。
	ctx = openAIWSContextAfterModeration(c, ctx)
	// 审计切组后重新挂载，避免新 context 只带最终分组而丢失首帧模型。
	ctx = service.WithClientRequestedResponseModel(ctx, reqModel)
	c.Request = c.Request.WithContext(ctx)
	if decision != nil && decision.Blocked {
		writeContentModerationWSError(ctx, wsConn, decision)
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, decision.Message)
		return
	}
	firstMessage = contentModerationForwardBody(c, firstMessage)

	// 渠道映射可能把客户端别名变为图片模型；权限和能力门控都必须复用
	// 同一份规范图片意图，避免 WS 与 HTTP 选号口径分叉。
	channelMappingWS, _ := h.gatewayService.ResolveChannelMappingAndRestrict(ctx, apiKey.GroupID, reqModel)
	requestPlatform := openAICompatibleRequestPlatformFromContext(ctx, apiKey)
	imageIntent := service.IsExplicitImageGenerationIntent("/v1/responses", reqModel, firstMessage)
	if channelMappingWS.Mapped && strings.TrimSpace(channelMappingWS.MappedModel) != "" {
		imageIntent = imageIntent || service.IsExplicitImageGenerationIntent("/v1/responses", channelMappingWS.MappedModel, nil)
	}
	if imageIntent && !service.GroupAllowsImageGeneration(apiKey.Group) {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, service.ImageGenerationPermissionMessage())
		return
	}

	// F5a: 握手层会话屏蔽检查。WS 握手无 body，显式标识仅来自握手 header
	// （session_id / conversation_id）；无标识则放行，连接内仍有本地 flag 兜底。
	cyberBlockKey := h.findCyberSessionBlockKeyForAPIKey(c, apiKey, nil)
	if cyberBlockKey != "" && h.gatewayService.IsCyberSessionBlocked(c.Request.Context(), cyberBlockKey) {
		writeCyberSessionBlockedWSError(c.Request.Context(), wsConn)
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "session blocked by cyber-security policy")
		h.enqueueCyberSessionBlockedOpsEntry(c, apiKey, reqModel, cyberBlockKey)
		return
	}
	cyberBlockedThisConn := false

	var currentUserRelease func()
	var currentAccountRelease func()
	var currentIngressRelease func()
	releaseAccountSlot := func() {
		if currentAccountRelease != nil {
			currentAccountRelease()
			currentAccountRelease = nil
		}
	}
	releaseTurnSlots := func() {
		releaseAccountSlot()
		if currentIngressRelease != nil {
			currentIngressRelease()
			currentIngressRelease = nil
		}
		if currentUserRelease != nil {
			currentUserRelease()
			currentUserRelease = nil
		}
	}
	// 必须尽早注册，确保任何 early return 都能释放已获取的并发槽位。
	defer releaseTurnSlots()

	turnGroupID, turnGroupMaxConcurrency := finalGroupConcurrencyLimit(c)
	turnAPIKeyConcurrencyID := finalAPIKeyConcurrencyID(c)
	acquireTurnUserSlot := func() (func(), bool, error) {
		result, acquireErr := h.concurrencyHelper.tryAcquireFinalUserSlotForAPIKey(
			ctx,
			apiKey.User,
			subject.UserID,
			subject.Concurrency,
			turnGroupID,
			turnGroupMaxConcurrency,
			turnAPIKeyConcurrencyID,
			apiKey.KeyConcurrencyLimit,
		)
		if acquireErr != nil {
			return nil, false, acquireErr
		}
		if result == nil || !result.Acquired {
			return nil, false, nil
		}
		return result.ReleaseFunc, true, nil
	}

	userReleaseFunc, userAcquired, err := acquireTurnUserSlot()
	if err != nil {
		reqLog.Warn("openai.websocket_user_slot_acquire_failed", zap.Error(err))
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusInternalError, "failed to acquire user concurrency slot")
		return
	}
	if !userAcquired {
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "too many concurrent requests, please retry later")
		return
	}
	currentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)
	currentIngressRelease = h.concurrencyHelper.trackGroupIngressSlotFromContext(c)
	ensureUserSlotHeld := func() bool {
		if currentUserRelease != nil {
			return true
		}
		userReleaseFunc, userAcquired, err := acquireTurnUserSlot()
		if err != nil {
			reqLog.Warn("openai.websocket_user_slot_reacquire_failed", zap.Error(err))
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusInternalError, "failed to acquire user concurrency slot")
			return false
		}
		if !userAcquired {
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "too many concurrent requests, please retry later")
			return false
		}
		currentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)
		currentIngressRelease = h.concurrencyHelper.trackGroupIngressSlotFromContext(c)
		return true
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	longContextModelsWS := []string{reqModel}
	if channelMappingWS.Mapped {
		longContextModelsWS = append([]string{channelMappingWS.MappedModel}, longContextModelsWS...)
	}
	routingCtx, _, routingErr := h.gatewayService.PrepareOpenAILongContextRouting(
		ctx, apiKey.Group, requestPlatform, firstMessage, longContextModelsWS...,
	)
	if routingErr != nil {
		_, _, _, message, _ := service.OpenAILongContextRoutingErrorDetails(routingErr)
		closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, message)
		return
	}
	ctx = routingCtx
	// 长上下文路由可能返回新的派生 context；再次保留客户模型是必要的，
	// 否则跨协议/直通响应在本轮只能回退到映射后的上游模型。
	ctx = service.WithClientRequestedResponseModel(ctx, reqModel)
	c.Request = c.Request.WithContext(ctx)
	requiredTransport := service.OpenAIUpstreamTransportResponsesWebsocketV2
	if requestPlatform == service.PlatformGrok {
		// xAI 当前只提供 HTTP/SSE，上游长连接由网关桥接，不能要求账号具备
		// OpenAI Responses WebSocket v2 能力。
		requiredTransport = service.OpenAIUpstreamTransportHTTPSSE
	}
	if !h.shouldSkipGatewayLiteBillingEligibility(c) {
		if err := h.billingCacheService.CheckBillingEligibilityForRequest(ctx, apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey), service.EstimateRequestTokensForLimit(firstMessage)); err != nil {
			reqLog.Info("openai.websocket_billing_eligibility_check_failed", zap.Error(err))
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "billing check failed")
			return
		}
	}

	sessionHash := h.gatewayService.GenerateSessionHashWithFallback(
		c,
		firstMessage,
		openAIWSIngressFallbackSessionSeed(subject.UserID, apiKey.ID, apiKey.GroupID),
	)
	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	wsAttemptMessage := append([]byte(nil), firstMessage...)
	wsBillingTurnOffset := 0
	// Attempt reconnects share one server-generated identity per financial turn.
	// Completed usage workers retain their own context even after the next frame.
	wsFundingTurnContexts := sync.Map{}
	routingGroupID := apiKey.SchedulerGroupID()
	requiredEndpointCapability := service.OpenAIEndpointCapabilityChatCompletions
	if imageIntent && requestPlatform == service.PlatformOpenAI {
		requiredEndpointCapability = service.OpenAIEndpointCapabilityResponses
	}

	for {
		reqLog.Debug("openai.websocket_account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
			ctx,
			routingGroupID,
			previousResponseID,
			sessionHash,
			reqModel,
			failedAccountIDs,
			requiredTransport,
			requiredEndpointCapability,
			false,
			requestPlatform,
		)
		if err != nil {
			reqLog.Warn("openai.websocket_account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if errors.Is(err, service.ErrKeywordProtectedPreviousResponseAccount) {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusPolicyViolation, "该连续会话绑定的上游账号不允许本次关键词内容，请新建会话后重试")
				return
			}
			if _, _, _, message, ok := service.OpenAILongContextRoutingErrorDetails(err); ok {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, message)
				return
			}
			if errors.Is(err, service.ErrFallbackQueueFull) {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "too many pending requests")
				return
			}
			if lastFailoverErr != nil {
				closeOpenAIWSFailoverExhausted(ctx, wsConn, lastFailoverErr)
			} else {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "no available account")
			}
			return
		}
		if selection == nil || selection.Account == nil {
			if lastFailoverErr != nil {
				closeOpenAIWSFailoverExhausted(ctx, wsConn, lastFailoverErr)
			} else {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "no available account")
			}
			return
		}

		account := selection.Account
		accountMaxConcurrency := account.Concurrency
		if selection.WaitPlan != nil && selection.WaitPlan.MaxConcurrency > 0 {
			accountMaxConcurrency = selection.WaitPlan.MaxConcurrency
		}
		accountReleaseFunc := selection.ReleaseFunc
		if !selection.Acquired {
			if selection.WaitPlan == nil {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "account is busy, please retry later")
				return
			}
			fastReleaseFunc, fastAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(
				ctx,
				account.ID,
				selection.WaitPlan.MaxConcurrency,
			)
			if err != nil {
				reqLog.Warn("openai.websocket_account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusInternalError, "failed to acquire account concurrency slot")
				return
			}
			if !fastAcquired {
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "account is busy, please retry later")
				return
			}
			accountReleaseFunc = fastReleaseFunc
		}
		// A scheduler snapshot can become stale between candidate selection and
		// slot acquisition.  Enforce the durable Tail-Burst admission boundary
		// before opening the upstream WebSocket; otherwise a quota-tail request
		// could leave no active session marker.  This is the last point where we
		// can safely retry with another account without asking the client to
		// reconnect.
		if !h.gatewayService.AdmitCodexTailBurstAfterSlot(ctx, account) {
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			h.gatewayService.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, routingGroupID, reqModel)
			failedAccountIDs[account.ID] = struct{}{}
			reqLog.Debug("openai.websocket_account_tail_burst_admission_rejected",
				zap.Int64("account_id", account.ID),
			)
			continue
		}
		currentAccountRelease = wrapReleaseOnDone(ctx, accountReleaseFunc)
		if err := h.gatewayService.BindStickySession(ctx, routingGroupID, sessionHash, account.ID); err != nil {
			reqLog.Warn("openai.websocket_bind_sticky_session_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}

		token, _, err := h.gatewayService.GetRequestCredential(ctx, c, account)
		if err != nil {
			reqLog.Warn("openai.websocket_get_access_token_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			if failoverRequestCanceled(ctx) {
				return
			}
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				if failoverErr.ShouldReportAccountScheduleFailure() {
					h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, channelMappingWS, reqModel))
				}
				releaseAccountSlot()
				if !failoverErr.ShouldRetryNextAccount() {
					closeOpenAIWSFailoverExhausted(ctx, wsConn, failoverErr)
					return
				}
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				switchBudget := openAIFailoverSwitchBudget(account, failoverErr, maxAccountSwitches)
				if switchCount >= switchBudget {
					closeOpenAIWSFailoverExhausted(ctx, wsConn, failoverErr)
					return
				}
				switchCount++
				if !waitOpenAIFailoverSwitch(ctx, account, failoverErr, switchCount) {
					return
				}
				h.gatewayService.RecordOpenAIAccountSwitch()
				if !ensureUserSlotHeld() {
					return
				}
				continue
			}
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusInternalError, "failed to get access token")
			return
		}

		reqLog.Debug("openai.websocket_account_selected",
			zap.Int64("account_id", account.ID),
			zap.String("account_name", account.Name),
			zap.String("schedule_layer", scheduleDecision.Layer),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
		)

		maxReasoningEffort, reasoningEffortMappings, _ := openAIReasoningEffortPolicyForRequest(c, apiKey)
		var requestPayloadHash string
		var turnChannelMapping atomic.Pointer[openAIWSTurnChannelMappingSnapshot]
		var turnRateLimit atomic.Pointer[openAIWSTurnRateLimitSnapshot]
		turnChannelMapping.Store(&openAIWSTurnChannelMappingSnapshot{turn: 1, mapping: channelMappingWS})
		currentWSHealthModel := func() string {
			mapping := channelMappingWS
			if snapshot := turnChannelMapping.Load(); snapshot != nil {
				mapping = snapshot.mapping
			}
			return mappedOpenAIAccountScheduleModel(c.Request.Context(), account, mapping, reqModel)
		}
		hooks := &service.OpenAIWSIngressHooks{
			ClientLifecycleContext:         clientLifecycleCtx,
			InitialRequestModel:            reqModel,
			MaxReasoningEffort:             maxReasoningEffort,
			ReasoningEffortMappings:        reasoningEffortMappings,
			StrictClientFrames:             strictClientFrames,
			OnStrictResponseCreateRejected: onStrictResponseCreateRejected,
			BeforeRequest: func(turn int, payload []byte, originalModel string) ([]byte, error) {
				if turn == 1 {
					return payload, nil
				}
				if !gjson.ValidBytes(payload) {
					return payload, service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", errors.New("invalid json"))
				}
				model := strings.TrimSpace(originalModel)
				if model == "" {
					model = strings.TrimSpace(gjson.GetBytes(payload, "model").String())
				}
				if model == "" {
					model = reqModel
				}
				// 每个 response.create 都可能切换模型；不能只在连接首帧
				// 检查一次，否则后续轮次可以调用未列入分组白名单的模型。
				if _, message := middleware2.GatewayModelPermissionError(apiKey, model); message != "" {
					return payload, service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, message, nil)
				}
				// 上游 WebSocket 已经建立后不能透明迁移账号或分组；命中时返回可重试错误，
				// 由客户端重连并在新首帧重新选择实际分组。
				c.Set("content_moderation_disable_keyword_reroute", true)
				c.Set(contentModerationBoundAccountAllowsBlockedKeywordsKey, account.AcceptsBlockedKeywords(apiKey.Group != nil && apiKey.Group.AllowBlockedKeywordRequests))
				if decision := h.checkResponsesWSContentModeration(c, reqLog, apiKey, subject, model, payload); decision != nil && decision.Blocked {
					writeContentModerationWSError(ctx, wsConn, decision)
					return payload, service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, decision.Message, nil)
				}
				payload = contentModerationForwardBody(c, payload)
				mapping := channelMappingWS
				if snapshot := turnChannelMapping.Load(); snapshot != nil && snapshot.turn == turn {
					mapping = snapshot.mapping
				}
				turnModels := []string{model}
				if mapping.Mapped {
					turnModels = append([]string{mapping.MappedModel}, turnModels...)
				}
				turnCtx, _, routingErr := h.gatewayService.PrepareOpenAILongContextRouting(
					ctx, apiKey.Group, requestPlatform, payload, turnModels...,
				)
				if routingErr != nil {
					_, _, _, message, _ := service.OpenAILongContextRoutingErrorDetails(routingErr)
					return payload, service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, message, routingErr)
				}
				if !service.OpenAIAccountMatchesLongContextRouting(turnCtx, account) {
					message := "This WebSocket turn requires a different long-context billing account. Reconnect to let the gateway reschedule. / 本轮请求需要切换长上下文计费账号，请重新连接以便网关重新调度。"
					return payload, service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, message, nil)
				}
				// BeforeRequest 可能早于并发槽位获取执行。这里只保存本轮预估值，
				// 避免并发已满、请求根本未进入上游时仍消耗 RPM/TPM。
				turnRateLimit.Store(&openAIWSTurnRateLimitSnapshot{
					turn:            turn,
					estimatedTokens: service.EstimateRequestTokensForLimit(payload),
				})
				return payload, nil
			},
			MapRequestModel: func(turn int, originalModel string) (string, error) {
				model := strings.TrimSpace(originalModel)
				if model == "" {
					model = reqModel
				}
				mapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(ctx, apiKey.GroupID, model)
				mappedModelUnchanged := false
				if previous := turnChannelMapping.Load(); previous != nil && previous.turn < turn {
					mappedModelUnchanged = strings.TrimSpace(previous.mapping.MappedModel) == strings.TrimSpace(mapping.MappedModel)
				}
				if turn > 1 && !mappedModelUnchanged && !account.IsModelSupported(model) && !account.IsModelSupported(mapping.MappedModel) {
					return "", newOpenAIWSUnsupportedModelSwitchError(mapping.MappedModel)
				}
				turnChannelMapping.Store(&openAIWSTurnChannelMappingSnapshot{turn: turn, mapping: mapping})
				return mapping.MappedModel, nil
			},
			BeforeTurn: func(turn int) error {
				// turn==1 的会话屏蔽已由握手层检查覆盖；连接内 flag 只拦截后续 turn。
				if cyberBlockedThisConn {
					return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, cyberSessionBlockedClientMsg, nil)
				}
				if turn == 1 {
					return nil
				}
				// 防御式清理：避免异常路径下旧槽位覆盖导致泄漏。
				releaseTurnSlots()
				// 非首轮 turn 需要重新抢占并发槽位，避免长连接空闲占槽。
				userReleaseFunc, userAcquired, err := acquireTurnUserSlot()
				if err != nil {
					return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire user concurrency slot", err)
				}
				if !userAcquired {
					return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "too many concurrent requests, please retry later", nil)
				}
				accountReleaseFunc, accountAcquired, err := h.concurrencyHelper.TryAcquireAccountSlot(ctx, account.ID, accountMaxConcurrency)
				if err != nil {
					if userReleaseFunc != nil {
						userReleaseFunc()
					}
					return service.NewOpenAIWSClientCloseError(coderws.StatusInternalError, "failed to acquire account concurrency slot", err)
				}
				if !accountAcquired {
					if userReleaseFunc != nil {
						userReleaseFunc()
					}
					return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account is busy, please retry later", nil)
				}
				// The upstream WebSocket is already bound to this account.  A failed
				// post-slot Tail-Burst admission cannot be migrated transparently;
				// release both leases and ask the client to reconnect so the next
				// handshake can select a fresh account.
				if !h.gatewayService.AdmitCodexTailBurstAfterSlot(ctx, account) {
					if accountReleaseFunc != nil {
						accountReleaseFunc()
					}
					if userReleaseFunc != nil {
						userReleaseFunc()
					}
					return service.NewOpenAIWSClientCloseError(
						coderws.StatusTryAgainLater,
						"account quota state changed; please reconnect",
						nil,
					)
				}
				currentUserRelease = wrapReleaseOnDone(ctx, userReleaseFunc)
				currentIngressRelease = h.concurrencyHelper.trackGroupIngressSlotFromContext(c)
				currentAccountRelease = wrapReleaseOnDone(ctx, accountReleaseFunc)
				// 每个后续 response.create 只在用户与账号并发槽位都成功后消费一次。
				// 同轮 Priority（优先）降级和重连会跳过 BeforeRequest/BeforeTurn，
				// 因而复用当前轮次结果，不会重复计数。
				if !h.shouldSkipGatewayLiteBillingEligibility(c) {
					estimatedTokens := 0
					if snapshot := turnRateLimit.Load(); snapshot != nil && snapshot.turn == turn {
						estimatedTokens = snapshot.estimatedTokens
					}
					if err := h.billingCacheService.CheckBillingEligibilityForRequest(
						ctx, apiKey.User, apiKey, apiKey.Group, subscription,
						service.QuotaPlatform(ctx, apiKey), estimatedTokens,
					); err != nil {
						releaseTurnSlots()
						_, _, message, _ := billingErrorDetails(err)
						return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, message, err)
					}
				}
				return nil
			},
			BeforeDispatch: func(turn int, payload []byte, originalModel string) error {
				if err := gatewaylite.CheckBindingTurn(ctx); err != nil {
					return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, err.Error(), err)
				}
				financialTurn := wsBillingTurnOffset + turn
				turnCtx := ctx
				if stored, ok := wsFundingTurnContexts.Load(financialTurn); ok {
					turnCtx = stored.(context.Context)
				} else {
					model := strings.TrimSpace(originalModel)
					if model == "" {
						model = reqModel
					}
					requestID := fmt.Sprintf("%s:ws-turn:%d", wsBillingConnectionID, financialTurn)
					var admitted context.Context
					var err error
					if gatewaylite.HasOAuthInvocationCredential(ctx) {
						admitted = gatewaylite.ContextForOAuthInvocationTurn(ctx, requestID)
						admitted = service.ContextWithNewExecutionPricing(admitted)
						admitted, err = h.gatewayService.EnsureOAuthInvocationTextFunding(admitted, apiKey, payload, model, c.Request.URL.Path, model, gjson.GetBytes(payload, "model").String())
						if admitted != nil {
							wsFundingTurnContexts.Store(financialTurn, admitted)
							c.Request = c.Request.WithContext(admitted)
						}
					} else {
						admitted, err = gatewaylite.AdmitFundingTurn(ctx, requestID, model)
						if admitted != nil {
							admitted = service.ContextWithNewExecutionPricing(admitted)
						}
					}
					if err != nil {
						// Agent 实时权限拒绝已带安全的关闭状态；不能包装成资金故障。
						var authClose *middleware2.GatewayLiteAgentTurnAuthError
						if errors.As(err, &authClose) {
							return err
						}
						recoveryBase := turnCtx
						if admitted != nil {
							recoveryBase = admitted
						}
						recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(recoveryBase), 5*time.Second)
						err = errors.Join(err, gatewaylite.FinishOAuthInvocationRecovery(recoveryCtx))
						cancel()
						return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "本轮资金准入失败，请稍后重试", err)
					}
					turnCtx = service.ContextWithExecutionPricing(admitted)
					wsFundingTurnContexts.Store(financialTurn, turnCtx)
				}
				if err := h.gatewayService.EnsureFundingWebSocketPayload(turnCtx, account, payload); err != nil {
					releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(turnCtx), 10*time.Second)
					_ = gatewaylite.ReleaseFundingPreDispatch(releaseCtx, "websocket_pre_dispatch_failure")
					_ = gatewaylite.FinishOAuthInvocationRecovery(releaseCtx)
					cancel()
					return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "本轮没有可验证的费用上界或可用额度", err)
				}
				c.Request = c.Request.WithContext(turnCtx)
				return nil
			},
			AfterTurn: func(turn int, result *service.OpenAIForwardResult, turnErr error) {
				// F1: cyber 标记按 turn 生命周期清理——defer 保证任意早返回路径都执行；
				// CyberBlocked 必须在 submit 前同步预捕获（task 闭包由 worker 池异步执行，
				// 届时 defer 已清除标记）。
				defer clearCyberPolicyTurnState(c)
				releaseTurnSlots()
				usageCtx := ctx
				if stored, ok := wsFundingTurnContexts.Load(wsBillingTurnOffset + turn); ok {
					usageCtx = stored.(context.Context)
				}
				defer func() {
					recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(usageCtx), 5*time.Second)
					defer cancel()
					if err := gatewaylite.FinishOAuthInvocationRecovery(recoveryCtx); err != nil {
						reqLog.Error("openai.websocket_delegation_recovery_pending", zap.Int("turn", turn), zap.Error(err))
					}
				}()
				turnRequestedModel := reqModel
				turnUpstreamModel := ""
				if result != nil {
					if model := strings.TrimSpace(result.Model); model != "" {
						turnRequestedModel = model
					}
					turnUpstreamModel = strings.TrimSpace(result.UpstreamModel)
				}
				var turnMapping service.ChannelMappingResult
				if snapshot := turnChannelMapping.Load(); snapshot != nil && snapshot.turn == turn {
					turnMapping = snapshot.mapping
				} else {
					turnMapping, _ = h.gatewayService.ResolveChannelMappingAndRestrict(ctx, apiKey.GroupID, turnRequestedModel)
				}
				if turnUpstreamModel == "" {
					turnUpstreamModel = turnRequestedModel
				}
				turnUsageFields := turnMapping.ToUsageFields(turnRequestedModel, turnUpstreamModel)
				h.recordCyberPolicyIfMarked(c, apiKey, account, subscription, turnRequestedModel, turnErr != nil, cyberBlockKey, turnUsageFields, requestPayloadHash)
				if service.GetOpsCyberPolicy(c) != nil {
					cyberBlockedThisConn = true
				}
				if turnErr != nil {
					if result == nil || result.ImageCount <= 0 {
						return
					}
					// cyber 命中时该 turn 的用量已由 recordCyberPolicyIfMarked(forwardErrored=true)
					// 按真实 token 记录，这里不再走下方 RecordUsage，避免对同一 turn 双写/双扣费。
					if service.GetOpsCyberPolicy(c) != nil {
						return
					}
					reqLog.Warn("openai.websocket_partial_error_with_image_result",
						zap.Int64("account_id", account.ID),
						zap.Int("image_count", result.ImageCount),
						zap.Error(turnErr),
					)
				}
				if result == nil {
					return
				}
				result.BillingModel = openAIWSTurnBillingModel(result, turnMapping, turnRequestedModel, turnUpstreamModel)
				billingResult := openAIWSTurnBillingResult(wsBillingConnectionID, wsBillingTurnOffset+turn, result)
				if account.Type == service.AccountTypeOAuth {
					h.gatewayService.UpdateCodexUsageSnapshotFromHeaders(ctx, account.ID, result.ResponseHeaders)
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs, turnUpstreamModel)
				inboundEndpoint := GetInboundEndpoint(c)
				upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
				cyberBlocked := service.GetOpsCyberPolicy(c) != nil
				h.submitOpenAIUsageRecordTask(usageCtx, billingResult, func(taskCtx context.Context) {
					if err := h.gatewayService.RecordUsage(taskCtx, &service.OpenAIRecordUsageInput{
						Result:             billingResult,
						APIKey:             apiKey,
						User:               apiKey.User,
						Account:            account,
						Subscription:       subscription,
						InboundEndpoint:    inboundEndpoint,
						UpstreamEndpoint:   upstreamEndpoint,
						UserAgent:          userAgent,
						IPAddress:          clientIP,
						SessionID:          sessionID,
						RequestPayloadHash: requestPayloadHash,
						APIKeyService:      h.apiKeyService,
						ChannelUsageFields: turnUsageFields,
						CyberBlocked:       cyberBlocked,
					}); err != nil {
						reqLog.Error("openai.websocket_record_usage_failed",
							zap.Int64("account_id", account.ID),
							zap.String("request_id", billingResult.RequestID),
							zap.Error(err),
						)
					}
				})
			},
		}

		// 保留首帧中的客户原始模型。转发器会在解析每一轮时应用渠道和账号映射；
		// 入口提前改写会让 request（跟随请求）策略把映射后的上游模型误当成客户模型。
		wsFirstMessage := wsAttemptMessage
		// 切组/会话失配防护：previous_response_id 未在当前分组命中粘连账号（StickyPreviousHit=false），
		// 说明该会话链不属于本次调度到的账号，原样转发会触发上游会话链鉴权失败（“鉴权失败，请检查 API Key”）。
		// 故剥离首包里的 previous_response_id，改用首包内 input 重建上下文；带 function_call_output 的
		// 工具续链无法重建，保持原样。仅作用于首轮首包，后续 turn 的续链由 WS 转发层既有逻辑处理。
		if previousResponseID != "" && !scheduleDecision.StickyPreviousHit &&
			!service.ValidateFunctionCallOutputContextBytes(wsFirstMessage).HasFunctionCallOutput {
			wsFirstMessage = service.RemovePreviousResponseIDFromBody(wsFirstMessage)
			reqLog.Debug("openai.websocket_previous_response_id_stripped_cross_group",
				zap.Int64("account_id", account.ID),
				zap.String("schedule_layer", scheduleDecision.Layer),
			)
		}

		// WebSocket 首包可能很大，hash 必须在 hooks 外算成字符串，避免 AfterTurn 闭包保活请求体。
		requestPayloadHash = service.HashUsageRequestPayload(wsFirstMessage)

		if err := h.gatewayService.ProxyResponsesWebSocketFromClient(ctx, c, wsConn, account, token, wsFirstMessage, hooks, msgType); err != nil {
			if errors.Is(context.Cause(ctx), service.ErrOpenAIWSIngressLeaseLost) {
				reqLog.Warn("openai.websocket_ingress_lease_lost",
					zap.Int64("account_id", account.ID),
					zap.Error(err),
				)
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect")
				return
			}

			var closeErr *service.OpenAIWSClientCloseError
			if errors.As(err, &closeErr) {
				if closeErr.StatusCode() == coderws.StatusNormalClosure {
					reqLog.Info("openai.websocket_ingress_closed_normally",
						zap.Int64("account_id", account.ID),
						zap.String("reason", closeErr.Reason()),
					)
				} else {
					// 客户端帧格式、空闲超时和服务端主动关闭都不代表上游账号故障；
					// 在这里先结束，避免把协议错误写入账号健康 EWMA 或触发账号冷却。
					reqLog.Info("openai.websocket_ingress_client_closed",
						zap.Int64("account_id", account.ID),
						zap.Int("close_status", int(closeErr.StatusCode())),
						zap.String("reason", closeErr.Reason()),
					)
				}
				closeOpenAIClientWSWithContext(ctx, wsConn, closeErr.StatusCode(), closeErr.Reason())
				return
			}

			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				retryPayload, failedTurn, retryCurrentTurn := service.OpenAIWSCurrentTurnRetryPayload(err)
				nextAttemptMessage, retrySafe := openAIWSNextAttemptMessage(wsAttemptMessage, retryPayload, retryCurrentTurn)
				if !retrySafe {
					closeOpenAIWSFailoverExhausted(ctx, wsConn, failoverErr)
					return
				}
				wsAttemptMessage = nextAttemptMessage
				if retryCurrentTurn {
					if failedTurn > 1 {
						wsBillingTurnOffset += failedTurn - 1
					}
					previousResponseID = ""
					reqLog.Warn("openai.websocket_current_turn_failover_retry",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("retry_payload_bytes", len(retryPayload)),
						zap.Int("billing_turn_offset", wsBillingTurnOffset),
					)
				}
				if failoverRequestCanceled(c.Request.Context()) {
					return
				}
				if failoverErr.ShouldReportAccountScheduleFailure() {
					h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, currentWSHealthModel())
				}
				releaseAccountSlot()
				if !failoverErr.ShouldRetryNextAccount() {
					closeOpenAIWSFailoverExhausted(ctx, wsConn, failoverErr)
					return
				}
				if failoverErr.RetryableOnSameAccount {
					retryLimit := account.GetPoolModeRetryCount()
					if sameAccountRetryAllowedWithError(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
						sameAccountRetryCount[account.ID]++
						reqLog.Warn("openai.websocket_same_account_retry",
							zap.Int64("account_id", account.ID),
							zap.Int("retry_count", sameAccountRetryCount[account.ID]),
							zap.Int("retry_limit", retryLimit),
						)
						if !ensureUserSlotHeld() {
							return
						}
						continue
					}
				}
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				switchBudget := openAIFailoverSwitchBudget(account, failoverErr, maxAccountSwitches)
				if switchCount >= switchBudget {
					closeOpenAIWSFailoverExhausted(ctx, wsConn, failoverErr)
					return
				}
				switchCount++
				if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
					closeOpenAIWSFailoverExhausted(ctx, wsConn, failoverErr)
					return
				}
				if !waitOpenAIFailoverSwitch(ctx, account, failoverErr, switchCount) {
					return
				}
				h.gatewayService.RecordOpenAIAccountSwitch()
				reqLog.Warn("openai.websocket_upstream_failover_switching",
					zap.Int64("account_id", account.ID),
					zap.Int("upstream_status", failoverErr.StatusCode),
					zap.Int("switch_count", switchCount),
					zap.Int("max_switches", switchBudget),
				)
				if !ensureUserSlotHeld() {
					return
				}
				continue
			}

			if shouldReportOpenAIWSProxyAccountFailure(err) {
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, currentWSHealthModel())
			}
			closeStatus, closeReason := summarizeWSCloseErrorForLog(err)
			reqLog.Warn("openai.websocket_proxy_failed",
				zap.Int64("account_id", account.ID),
				zap.Error(err),
				zap.String("close_status", closeStatus),
				zap.String("close_reason", closeReason),
			)
			var protocolErr *service.OpenAIWSUpstreamProtocolError
			if errors.As(err, &protocolErr) {
				writeOpenAIWSUpstreamProtocolError(ctx, wsConn)
				closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusInternalError, "upstream websocket protocol error")
				return
			}
			closeOpenAIClientWSWithContext(ctx, wsConn, coderws.StatusInternalError, "upstream websocket proxy failed")
			return
		}
		reqLog.Info("openai.websocket_ingress_closed", zap.Int64("account_id", account.ID))
		return
	}

}

func openAIWSNextAttemptMessage(current, retryPayload []byte, retryCurrentTurn bool) ([]byte, bool) {
	if !retryCurrentTurn {
		return append([]byte(nil), current...), true
	}
	if len(retryPayload) == 0 {
		return nil, false
	}
	return append([]byte(nil), retryPayload...), true
}

func openAIWSContextAfterModeration(c *gin.Context, fallback context.Context) context.Context {
	if c != nil && c.Request != nil && c.Request.Context() != nil {
		return c.Request.Context()
	}
	return fallback
}

func (h *OpenAIGatewayHandler) recoverResponsesPanic(c *gin.Context, streamStarted *bool) {
	recovered := recover()
	if recovered == nil {
		return
	}

	started := false
	if streamStarted != nil {
		started = *streamStarted
	}
	wroteFallback := h.ensureForwardErrorResponse(c, started)
	requestLogger(c, "handler.openai_gateway.responses").Error(
		"openai.responses_panic_recovered",
		zap.Bool("fallback_error_response_written", wroteFallback),
		zap.Any("panic", recovered),
		zap.ByteString("stack", debug.Stack()),
	)
}

// recoverAnthropicMessagesPanic recovers from panics in the Anthropic Messages
// handler and returns an Anthropic-formatted error response.
func (h *OpenAIGatewayHandler) recoverAnthropicMessagesPanic(c *gin.Context, streamStarted *bool) {
	recovered := recover()
	if recovered == nil {
		return
	}

	started := streamStarted != nil && *streamStarted
	requestLogger(c, "handler.openai_gateway.messages").Error(
		"openai.messages_panic_recovered",
		zap.Bool("stream_started", started),
		zap.Any("panic", recovered),
		zap.ByteString("stack", debug.Stack()),
	)
	if !started {
		h.anthropicErrorResponse(c, http.StatusInternalServerError, "api_error", "Internal server error")
	}
}

func (h *OpenAIGatewayHandler) ensureResponsesDependencies(c *gin.Context, reqLog *zap.Logger) bool {
	missing := h.missingResponsesDependencies()
	if len(missing) == 0 {
		return true
	}

	if reqLog == nil {
		reqLog = requestLogger(c, "handler.openai_gateway.responses")
	}
	reqLog.Error("openai.handler_dependencies_missing", zap.Strings("missing_dependencies", missing))

	if c != nil && c.Writer != nil && !c.Writer.Written() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": gin.H{
				"type":    "api_error",
				"message": "Service temporarily unavailable",
			},
		})
	}
	return false
}

func (h *OpenAIGatewayHandler) missingResponsesDependencies() []string {
	missing := make([]string, 0, 5)
	if h == nil {
		return append(missing, "handler")
	}
	if h.gatewayService == nil {
		missing = append(missing, "gatewayService")
	}
	if h.billingCacheService == nil {
		missing = append(missing, "billingCacheService")
	}
	if h.apiKeyService == nil {
		missing = append(missing, "apiKeyService")
	}
	if h.concurrencyHelper == nil || h.concurrencyHelper.concurrencyService == nil {
		missing = append(missing, "concurrencyHelper")
	}
	return missing
}

func getContextInt64(c *gin.Context, key string) (int64, bool) {
	if c == nil || key == "" {
		return 0, false
	}
	v, ok := c.Get(key)
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case float64:
		return int64(t), true
	default:
		return 0, false
	}
}

func (h *OpenAIGatewayHandler) submitUsageRecordTask(parent context.Context, task service.UsageRecordTask) {
	if task == nil {
		return
	}
	task = wrapUsageRecordTaskContext(parent, task)
	if h.usageRecordWorkerPool != nil {
		if mode := h.usageRecordWorkerPool.Submit(task); !mode.Dropped() {
			return
		}
		logger.L().With(
			zap.String("component", "handler.openai_gateway.responses"),
		).Warn("openai.usage_record_task_sync_fallback_after_drop")
	}
	// 回退路径：worker 池未注入时同步执行，避免退回到无界 goroutine 模式。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.responses"),
				zap.Any("panic", recovered),
			).Error("openai.usage_record_task_panic_recovered")
		}
	}()
	task(ctx)
}

func (h *OpenAIGatewayHandler) submitOpenAIUsageRecordTask(parent context.Context, result *service.OpenAIForwardResult, task service.UsageRecordTask) {
	if result != nil && result.ImageCount > 0 {
		h.submitMandatoryUsageRecordTask(parent, task)
		return
	}
	h.submitUsageRecordTask(parent, task)
}

func (h *OpenAIGatewayHandler) submitMandatoryUsageRecordTask(parent context.Context, task service.UsageRecordTask) {
	if task == nil {
		return
	}
	task = wrapUsageRecordTaskContext(parent, task)
	if h.usageRecordWorkerPool != nil {
		if mode := h.usageRecordWorkerPool.Submit(task); !mode.Dropped() {
			return
		}
		logger.L().With(
			zap.String("component", "handler.openai_gateway.usage"),
		).Warn("openai.usage_record_task_mandatory_sync_fallback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.usage"),
				zap.Any("panic", recovered),
			).Error("openai.usage_record_task_panic_recovered")
		}
	}()
	task(ctx)
}

func (h *OpenAIGatewayHandler) acquireImageGenerationSlot(c *gin.Context, streamStarted bool) (func(), bool) {
	if h == nil || h.cfg == nil || h.imageLimiter == nil {
		return nil, true
	}
	imageConcurrency := h.cfg.Gateway.ImageConcurrency
	wait := strings.TrimSpace(imageConcurrency.OverflowMode) == config.ImageConcurrencyOverflowModeWait
	release, acquired := h.imageLimiter.Acquire(
		c.Request.Context(),
		imageConcurrency.Enabled,
		imageConcurrency.MaxConcurrentRequests,
		wait,
		time.Duration(imageConcurrency.WaitTimeoutSeconds)*time.Second,
		imageConcurrency.MaxWaitingRequests,
	)
	if acquired {
		return release, true
	}
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Image generation concurrency limit exceeded, please retry later", streamStarted)
	return nil, false
}

// handleConcurrencyError handles concurrency-related acquire errors.
func (h *OpenAIGatewayHandler) handleConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool) {
	status, errType, message := concurrencyErrorResponse(err, slotType)
	h.handleStreamingAwareError(c, status, errType, message, streamStarted)
}

func (h *OpenAIGatewayHandler) handleFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool) {
	if failoverErr == nil {
		h.handleFailoverExhaustedSimple(c, http.StatusBadGateway, streamStarted)
		return
	}
	if failoverErr.IsCredentialFailure() {
		h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "upstream_error", service.GrokCredentialUnavailableClientMessage, streamStarted)
		return
	}
	if failoverErr.IsOpenAIRequestBodyTooLarge() {
		// 上游错误体可能包含账号名、代理限制或供应商细节。候选耗尽后
		// 只返回固定 413，JSON 与 Responses SSE 共用同一脱敏消息。
		service.SetOpsUpstreamError(c, failoverErr.StatusCode, service.OpenAIRequestBodyTooLargeClientMessage, "")
		h.handleStreamingAwareErrorWithCode(
			c,
			http.StatusRequestEntityTooLarge,
			"invalid_request_error",
			"invalid_request",
			service.OpenAIRequestBodyTooLargeClientMessage,
			streamStarted,
		)
		return
	}
	statusCode := failoverErr.StatusCode
	responseBody := failoverErr.ResponseBody
	if service.IsOpenAISilentRefusalErrorBody(responseBody) {
		service.SetOpsUpstreamError(c, statusCode, service.OpenAISilentRefusalClientMessage(), "")
		h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", service.OpenAISilentRefusalClientMessage(), streamStarted)
		return
	}

	// 先检查透传规则
	if h.errorPassthroughService != nil && len(responseBody) > 0 {
		if rule := h.errorPassthroughService.MatchRule("openai", statusCode, responseBody); rule != nil {
			// 确定响应状态码
			respCode := statusCode
			if !rule.PassthroughCode && rule.ResponseCode != nil {
				respCode = *rule.ResponseCode
			}

			// 确定响应消息
			msg := service.ExtractUpstreamErrorMessage(responseBody)
			if !rule.PassthroughBody && rule.CustomMessage != nil {
				msg = *rule.CustomMessage
			}

			if rule.SkipMonitoring {
				c.Set(service.OpsSkipPassthroughKey, true)
			}

			h.handleStreamingAwareError(c, respCode, "upstream_error", msg, streamStarted)
			return
		}
	}

	// 记录原始上游状态码，以便 ops 错误日志捕获真实的上游错误
	upstreamMsg := service.ExtractUpstreamErrorMessage(responseBody)
	service.SetOpsUpstreamError(c, statusCode, upstreamMsg, "")

	// 使用默认的错误映射
	status, errType, errMsg := h.mapUpstreamError(statusCode)
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

// handleFailoverExhaustedSimple 简化版本，用于没有响应体的情况
func (h *OpenAIGatewayHandler) handleFailoverExhaustedSimple(c *gin.Context, statusCode int, streamStarted bool) {
	status, errType, errMsg := h.mapUpstreamError(statusCode)
	service.SetOpsUpstreamError(c, statusCode, errMsg, "")
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

func (h *OpenAIGatewayHandler) mapUpstreamError(statusCode int) (int, string, string) {
	switch statusCode {
	case 401:
		return http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "upstream_error", "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "upstream_error", "Upstream request failed"
	}
}

// handleStreamingAwareError handles errors that may occur after streaming has started
func (h *OpenAIGatewayHandler) handleStreamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	h.handleStreamingAwareErrorWithCode(c, status, errType, "", message, streamStarted)
}

func (h *OpenAIGatewayHandler) handleStreamingAwareErrorWithCode(c *gin.Context, status int, errType, code, message string, streamStarted bool) {
	if streamStarted {
		service.MarkOpsStreamError(c, errType, message, status)
		// /v1/responses 的严格 SDK（Codex CLI）要求终止事件必须属于
		// response.completed/failed/incomplete/cancelled 集合。
		// 通用 `event: error` 帧不被识别为终止事件，会导致
		// "stream closed before response.completed"。
		if inboundIsResponses(c) {
			if writeResponsesFailedSSEWithCode(c, errType, code, message) {
				return
			}
		}
		// Stream already started, send error as SSE event then close
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			clientMessage := service.GatewayClientErrorMessage(c.Request.Context(), message)
			// SSE 错误事件固定 schema，使用 Quote 直拼可避免额外 Marshal 分配。
			errorBody := `{"error":{"type":` + strconv.Quote(errType)
			if strings.TrimSpace(code) != "" {
				errorBody += `,"code":` + strconv.Quote(strings.TrimSpace(code))
			}
			errorBody += `,"message":` + strconv.Quote(clientMessage) + `}}`
			errorEvent := "event: error\ndata: " + errorBody + "\n\n"
			if _, err := fmt.Fprint(c.Writer, errorEvent); err != nil {
				_ = c.Error(err)
			}
			flusher.Flush()
		}
		return
	}

	// Normal case: return JSON response with proper status code
	h.errorResponseWithCode(c, status, errType, code, message)
}

func (h *OpenAIGatewayHandler) ensureOpenAIStreamReadErrorResponse(c *gin.Context, err error, streamStarted bool) bool {
	code, message, ok := service.OpenAIUpstreamStreamReadErrorDetails(err)
	if !ok || c == nil || c.Writer == nil || service.IsResponseCommitted(c) {
		return false
	}
	if c.Writer.Written() {
		streamStarted = true
	}
	h.handleStreamingAwareErrorWithCode(c, http.StatusBadGateway, "upstream_error", code, message, streamStarted)
	return true
}

// ensureForwardErrorResponse 在 Forward 返回错误但尚未写响应时补写统一错误响应。
func (h *OpenAIGatewayHandler) ensureForwardErrorResponse(c *gin.Context, streamStarted bool) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	// 伪造首字注释行与 compact keepalive 心跳同口径：只提交了 200+SSE 头和一条
	// 注释行，没有任何真实正文（注释字节已计入 keepalive 记账，调整后为 0）。
	// 此时上游失败仍必须以 SSE 终止事件告知客户端，否则流在注释行后静默结束，
	// 客户端（如 Cherry Studio）会表现为「很快完成但没有任何回复」。
	fakeFirstTokenCommitted := service.CommittedFakeFirstTokenMs(c) != nil
	compactKeepaliveCommitted := fakeFirstTokenCommitted || service.StopOpenAICompactSSEKeepaliveCommitted(c)
	if compactKeepaliveCommitted {
		streamStarted = true
	}
	imageHeartbeatOnly := false
	if service.StopOpenAIImagesJSONKeepaliveCommitted(c) {
		imageHeartbeatOnly = service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) < 0
	}
	compactKeepaliveHasMeaningfulOutput := compactKeepaliveCommitted && service.OpenAICompactKeepaliveAdjustedWrittenSize(c) > 0
	if service.IsResponseCommitted(c) && (!compactKeepaliveCommitted || compactKeepaliveHasMeaningfulOutput) {
		return false
	}
	if c.Writer.Written() && !imageHeartbeatOnly {
		streamStarted = true
	}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed", streamStarted)
	return true
}

func shouldLogOpenAIForwardFailureAsWarn(c *gin.Context, wroteFallback bool) bool {
	if wroteFallback {
		return false
	}
	if c == nil || c.Writer == nil {
		return false
	}
	return c.Writer.Written()
}

// openAIForwardErrorAlreadyCommunicated reports whether Forward returned an
// error after it had already written the upstream terminal error response to
// the client.
//
// This matters for Responses streams: upstream may return HTTP 200 with a
// non-retryable `response.failed` event (for example a policy/safety rejection).
// The service layer forwards that terminal event verbatim, then returns an
// error so the caller can log/account for the failed upstream response. The
// handler must not append its generic fallback `response.failed`, otherwise
// strict clients may see the useful upstream message replaced by "Upstream
// request failed" or receive duplicate terminal events.
func openAIForwardErrorAlreadyCommunicated(c *gin.Context, writerSizeBeforeForward int, err error) bool {
	if err == nil || c == nil || c.Writer == nil {
		return false
	}
	if service.OpenAICompactKeepaliveAdjustedWrittenSize(c) == writerSizeBeforeForward ||
		service.OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c) == writerSizeBeforeForward {
		return false
	}

	// cyber_policy 命中时上游原始错误体已透传给客户端（非流式 c.Data 写出 400 body，
	// 流式写出 response.failed 事件），不能再让 ensureForwardErrorResponse 追加
	// fallback —— 否则在已写出的完整响应尾部追加 SSE（responses 端点尾随
	// response.failed、chat 端点尾随 event:error），污染响应体。Size 已变化证明响应确已写出。
	if service.GetOpsCyberPolicy(c) != nil {
		return true
	}

	msg := strings.TrimSpace(err.Error())
	for _, prefix := range []string{
		"upstream response failed:",
		"non-streaming openai protocol error:",
	} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

func notifyGatewayGroupUnavailable(groupID *int64) {
	if groupID == nil || *groupID <= 0 {
		return
	}
	gatewaylite.NotifyDefaultConfigChanged("groups", "unavailable", *groupID)
}

func gatewayGroupUnavailableClientMessage(err error) string {
	if errors.Is(err, service.ErrNoAvailableAccounts) {
		return "No available accounts in gateway group"
	}
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "no available") || strings.Contains(msg, "available accounts") {
			return "No available accounts in gateway group"
		}
	}
	return "Gateway group is unavailable on this gateway"
}

// errorResponse returns OpenAI API format error response
func (h *OpenAIGatewayHandler) errorResponse(c *gin.Context, status int, errType, message string) {
	h.errorResponseWithCode(c, status, errType, "", message)
}

func (h *OpenAIGatewayHandler) errorResponseWithCode(c *gin.Context, status int, errType, code, message string) {
	// 伪造首字注释行已提交 200+SSE 头时，同样只能以 SSE 终止事件报错，
	// 不能再写 JSON 状态响应（头已提交，写 JSON 会污染/失败）。
	if service.StopOpenAICompactSSEKeepaliveCommitted(c) || service.CommittedFakeFirstTokenMs(c) != nil {
		service.MarkOpsStreamError(c, errType, message, status)
		if writeResponsesFailedSSEWithCode(c, errType, code, message) {
			return
		}
	}
	errorObj := gin.H{
		"type":    errType,
		"message": message,
	}
	if strings.TrimSpace(code) != "" {
		errorObj["code"] = strings.TrimSpace(code)
	}
	c.JSON(status, gin.H{
		"error": errorObj,
	})
}

func (h *OpenAIGatewayHandler) openAICompactKeepaliveInterval() time.Duration {
	if h == nil || h.cfg == nil || h.cfg.Gateway.StreamKeepaliveInterval <= 0 {
		return 0
	}
	return time.Duration(h.cfg.Gateway.StreamKeepaliveInterval) * time.Second
}

func setOpenAIClientTransportHTTP(c *gin.Context) {
	service.SetOpenAIClientTransport(c, service.OpenAIClientTransportHTTP)
}

func setOpenAIClientTransportWS(c *gin.Context) {
	service.SetOpenAIClientTransport(c, service.OpenAIClientTransportWS)
}

func ensureOpenAIPoolModeSessionHash(sessionHash string, account *service.Account) string {
	if sessionHash != "" || account == nil || !account.IsPoolMode() {
		return sessionHash
	}
	// 为当前请求生成一次性粘性会话键，确保同账号重试不会重新负载均衡到其他账号。
	return "openai-pool-retry-" + uuid.NewString()
}

func openAIWSIngressFallbackSessionSeed(userID, apiKeyID int64, groupID *int64) string {
	gid := int64(0)
	if groupID != nil {
		gid = *groupID
	}
	return fmt.Sprintf("openai_ws_ingress:%d:%d:%d", gid, userID, apiKeyID)
}

func isOpenAIWSUpgradeRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(r.Header.Get("Connection"))), "upgrade")
}

func closeOpenAIClientWS(conn *coderws.Conn, status coderws.StatusCode, reason string) {
	closeOpenAIClientWSWithContext(context.Background(), conn, status, reason)
}

func closeOpenAIClientWSWithContext(ctx context.Context, conn *coderws.Conn, status coderws.StatusCode, reason string) {
	if conn == nil {
		return
	}
	reason = service.GatewayClientErrorMessage(ctx, reason)
	reason = service.TruncateGatewayClientErrorReason(reason, 120)
	_ = conn.Close(status, reason)
	_ = conn.CloseNow()
}

func closeOpenAIWSFailoverExhausted(ctx context.Context, conn *coderws.Conn, failoverErr *service.UpstreamFailoverError) {
	if failoverErr == nil {
		closeOpenAIClientWSWithContext(ctx, conn, coderws.StatusInternalError, "upstream websocket proxy failed")
		return
	}
	if failoverErr.IsCredentialFailure() {
		closeOpenAIClientWSWithContext(ctx, conn, coderws.StatusTryAgainLater, service.GrokCredentialUnavailableClientMessage)
		return
	}
	switch failoverErr.StatusCode {
	case http.StatusTooManyRequests:
		closeOpenAIClientWSWithContext(ctx, conn, coderws.StatusTryAgainLater, "upstream rate limit exceeded, please retry later")
	case 529, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		closeOpenAIClientWSWithContext(ctx, conn, coderws.StatusTryAgainLater, "upstream service temporarily unavailable")
	case http.StatusUnauthorized, http.StatusForbidden:
		closeOpenAIClientWSWithContext(ctx, conn, coderws.StatusPolicyViolation, "upstream websocket authentication failed")
	default:
		closeOpenAIClientWSWithContext(ctx, conn, coderws.StatusInternalError, "upstream websocket proxy failed")
	}
}

func writeContentModerationWSError(ctx context.Context, conn *coderws.Conn, decision *service.ContentModerationDecision) {
	if conn == nil || decision == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload := contentModerationWSErrorPayload(decision)
	if rewritten, changed := service.RewriteGatewayClientErrorJSON(payload, service.GatewayCustomErrorMessageFromContext(ctx)); changed {
		payload = rewritten
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

func contentModerationWSErrorPayload(decision *service.ContentModerationDecision) []byte {
	if decision == nil {
		return []byte(`{"event_id":"evt_content_moderation_blocked","type":"error","error":{"type":"invalid_request_error","code":"content_policy_violation","message":"content moderation blocked this request","retryable":false}}`)
	}
	message := strings.TrimSpace(decision.Message)
	if message == "" {
		message = "content moderation blocked this request"
	}
	payload, err := json.Marshal(gin.H{
		"event_id": "evt_content_moderation_blocked",
		"type":     "error",
		"error": gin.H{
			"type":         "invalid_request_error",
			"code":         contentModerationErrorCode(decision),
			"message":      message,
			"retryable":    decision.RouteReason == "websocket_reconnect_required",
			"route_reason": decision.RouteReason,
		},
	})
	if err != nil {
		return []byte(`{"event_id":"evt_content_moderation_blocked","type":"error","error":{"type":"invalid_request_error","code":"content_policy_violation","message":"content moderation blocked this request","retryable":false}}`)
	}
	return payload
}

func writeOpenAIWSUpstreamProtocolError(ctx context.Context, conn *coderws.Conn) {
	if conn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// 固定安全文案：只告诉客户端上游协议不可用，不回显畸形 payload、账号、
	// provider（服务商）地址或内部解析错误。
	payload := []byte(`{"event_id":"evt_upstream_ws_protocol_error","type":"error","error":{"type":"server_error","code":"upstream_websocket_protocol_error","message":"Upstream WebSocket protocol error"}}`)
	if rewritten, changed := service.RewriteGatewayClientErrorJSON(payload, service.GatewayCustomErrorMessageFromContext(ctx)); changed {
		payload = rewritten
	}
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

// writeCyberSessionBlockedWSError sends an error frame telling the client this
// session is blocked by the cyber session block (F5a) before closing.
func writeCyberSessionBlockedWSError(ctx context.Context, conn *coderws.Conn) {
	if conn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(gin.H{
		"event_id": "evt_cyber_session_blocked",
		"type":     "error",
		"error": gin.H{
			"type":    "permission_error",
			"code":    "session_blocked_by_cyber_policy",
			"message": cyberSessionBlockedClientMsg,
		},
	})
	if err != nil {
		payload = []byte(`{"event_id":"evt_cyber_session_blocked","type":"error","error":{"type":"permission_error","code":"session_blocked_by_cyber_policy","message":"This session is blocked by cyber-security policy, please start a new session"}}`)
	}
	if rewritten, changed := service.RewriteGatewayClientErrorJSON(payload, service.GatewayCustomErrorMessageFromContext(ctx)); changed {
		payload = rewritten
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

// cyberPolicyRecordedKey guards against double-firing recordCyberPolicyIfMarked
// within one request (e.g. in a retry/failover loop).
const cyberPolicyRecordedKey = "ops_cyber_recorded"

// cyberPolicyOpsErrorMeta carries request-scoped fields captured outside the
// async goroutine for building the cyber ops_error_logs entry.
type cyberPolicyOpsErrorMeta struct {
	RequestID       string
	ClientRequestID string
	Platform        string
	Model           string
	RequestPath     string
	Stream          bool
	InboundEndpoint string
	UserAgent       string
	APIKeyPrefix    string
	UserID          int64
	APIKeyID        int64
	AccountID       int64
	GroupID         *int64
	ClientIP        string
	CreatedAt       time.Time
	SessionBlockKey string
}

// buildCyberPolicyOpsErrorEntry builds the ops_error_logs entry for an upstream
// cyber_policy hit. StatusCode mirrors what the codex client actually received
// (400 non-stream / 200 stream), per F6.
func buildCyberPolicyOpsErrorEntry(meta cyberPolicyOpsErrorMeta, mark *service.CyberPolicyMark) *service.OpsInsertErrorLogInput {
	rt := int16(service.RequestTypeCyberBlocked)
	entry := &service.OpsInsertErrorLogInput{
		RequestID:         meta.RequestID,
		ClientRequestID:   meta.ClientRequestID,
		Platform:          meta.Platform,
		Model:             meta.Model,
		RequestPath:       meta.RequestPath,
		Stream:            meta.Stream,
		InboundEndpoint:   meta.InboundEndpoint,
		RequestType:       &rt,
		UserAgent:         meta.UserAgent,
		APIKeyPrefix:      meta.APIKeyPrefix,
		ErrorPhase:        "request",
		ErrorType:         "cyber_policy",
		Severity:          "P3",
		StatusCode:        mark.UpstreamStatus,
		IsBusinessLimited: true,
		ErrorMessage:      "cyber_policy: " + mark.Message,
		// 原始 body 直接入队；ops service 落库前统一走 sanitizeErrorBodyForStorage 脱敏与截断。
		ErrorBody:   mark.Body,
		ErrorSource: "upstream_http",
		ErrorOwner:  "provider",
		CreatedAt:   meta.CreatedAt,
	}
	if meta.UserID > 0 {
		entry.UserID = &meta.UserID
	}
	if meta.APIKeyID > 0 {
		entry.APIKeyID = &meta.APIKeyID
	}
	if meta.AccountID > 0 {
		entry.AccountID = &meta.AccountID
	}
	entry.GroupID = meta.GroupID
	if meta.ClientIP != "" {
		entry.ClientIP = &meta.ClientIP
	}
	return entry
}

// 双语单串：网关客户端面向中英用户，且本错误无 i18n 协商通道。
const cyberSessionBlockedClientMsg = "该会话已被网络安全策略屏蔽，请开启新会话 / This session is blocked by cyber-security policy, please start a new session"

// buildCyberSessionBlockedOpsEntry builds the ops_error_logs entry for a request
// rejected locally by the cyber session block (F5a). Distinct error_type from
// upstream `cyber_policy`; never feeds moderation logs / violation counting
// (the request never reached upstream — see spec).
func buildCyberSessionBlockedOpsEntry(meta cyberPolicyOpsErrorMeta) *service.OpsInsertErrorLogInput {
	rt := int16(service.RequestTypeCyberBlocked)
	entry := &service.OpsInsertErrorLogInput{
		RequestID:         meta.RequestID,
		ClientRequestID:   meta.ClientRequestID,
		Platform:          meta.Platform,
		Model:             meta.Model,
		RequestPath:       meta.RequestPath,
		Stream:            meta.Stream,
		InboundEndpoint:   meta.InboundEndpoint,
		RequestType:       &rt,
		UserAgent:         meta.UserAgent,
		APIKeyPrefix:      meta.APIKeyPrefix,
		ErrorPhase:        "request",
		ErrorType:         "cyber_policy_session_blocked",
		Severity:          "P3",
		StatusCode:        http.StatusForbidden,
		IsBusinessLimited: true,
		ErrorMessage:      "cyber_policy_session_blocked: request rejected locally by session block",
		ErrorSource:       "gateway_local",
		ErrorOwner:        "platform",
		CreatedAt:         meta.CreatedAt,
		// AccountID 有意不设：请求在账号选择前即被拒绝。
	}
	if meta.SessionBlockKey != "" {
		entry.ErrorBody = "session_block_key=" + meta.SessionBlockKey
	}
	if meta.UserID > 0 {
		entry.UserID = &meta.UserID
	}
	if meta.APIKeyID > 0 {
		entry.APIKeyID = &meta.APIKeyID
	}
	entry.GroupID = meta.GroupID
	if meta.ClientIP != "" {
		entry.ClientIP = &meta.ClientIP
	}
	return entry
}

// cyberSessionBlockFormat selects the per-endpoint error envelope for a locally
// blocked session (用户决策：兼容路径各自格式).
type cyberSessionBlockFormat int

const (
	cyberBlockFormatResponses cyberSessionBlockFormat = iota
	cyberBlockFormatChat
	cyberBlockFormatAnthropic
)

// rejectIfCyberSessionBlocked checks the session-block table BEFORE account
// selection. Returns true when the request was rejected (response already
// written + ops entry enqueued). Fail-open: disabled switch / empty key /
// store error → false.
func (h *OpenAIGatewayHandler) rejectIfCyberSessionBlocked(c *gin.Context, apiKey *service.APIKey, body []byte, model string, format cyberSessionBlockFormat) bool {
	if h == nil || h.gatewayService == nil || apiKey == nil {
		return false
	}
	// 开关默认关：先走 ~ns 级缓存开关检查，再付出 key 派生(gjson+sha256)成本。
	if enabled, _ := h.gatewayService.CyberSessionBlockRuntime(c.Request.Context()); !enabled {
		return false
	}
	key := h.findCyberSessionBlockKeyForAPIKey(c, apiKey, body)
	if key == "" {
		return false
	}
	if !h.gatewayService.IsCyberSessionBlocked(c.Request.Context(), key) {
		return false
	}
	switch format {
	case cyberBlockFormatAnthropic:
		c.JSON(http.StatusForbidden, gin.H{"type": "error", "error": gin.H{
			"type":    "permission_error",
			"message": cyberSessionBlockedClientMsg,
		}})
	default: // cyberBlockFormatResponses 与 cyberBlockFormatChat：同构的 OpenAI error envelope
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{
			"type":    "permission_error",
			"code":    "session_blocked_by_cyber_policy",
			"message": cyberSessionBlockedClientMsg,
		}})
	}
	h.enqueueCyberSessionBlockedOpsEntry(c, apiKey, model, key)
	return true
}

// enqueueCyberSessionBlockedOpsEntry captures request meta and enqueues the
// ops_error_logs entry for a locally blocked request.
func (h *OpenAIGatewayHandler) enqueueCyberSessionBlockedOpsEntry(c *gin.Context, apiKey *service.APIKey, model string, sessionBlockKey string) {
	if h.opsService == nil {
		return
	}
	meta := cyberPolicyOpsErrorMeta{Model: model, InboundEndpoint: GetInboundEndpoint(c), CreatedAt: time.Now(), SessionBlockKey: sessionBlockKey}
	meta.RequestID = c.Writer.Header().Get("X-Request-Id")
	if c.Request != nil && c.Request.URL != nil {
		meta.RequestPath = c.Request.URL.Path
	}
	if v, ok := c.Get(opsStreamKey); ok {
		if b, ok := v.(bool); ok {
			meta.Stream = b
		}
	}
	meta.Platform = resolveOpsPlatform(apiKey, guessPlatformFromPath(meta.RequestPath))
	if c.Request != nil {
		meta.ClientRequestID, _ = c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		meta.UserAgent = c.GetHeader("User-Agent")
		meta.ClientIP = strings.TrimSpace(ip.GetClientIP(c))
	}
	meta.APIKeyID = apiKey.ID
	meta.GroupID = apiKey.GroupID
	meta.APIKeyPrefix = keyPrefix(apiKey.Key, 8)
	if apiKey.User != nil {
		meta.UserID = apiKey.User.ID
	}
	enqueueOpsErrorLog(h.opsService, buildCyberSessionBlockedOpsEntry(meta))
}

// recordCyberPolicyIfMarked 在 gateway forward 返回后检查 cyber 标记，异步写风控日志/邮件，
// 并在 forward 返回错误时写一条 tokens=0 用量行。标记由 gateway 服务层在透传 cyber 后设置；
// 当前请求已发给用户，本方法只做事后记录，不影响响应。forwardErrored 为 true 时才写用量行，
// 避免与正常 RecordUsage(forward 成功路径)重复。每请求至多记录一次。
func (h *OpenAIGatewayHandler) recordCyberPolicyIfMarked(c *gin.Context, apiKey *service.APIKey, account *service.Account, subscription *service.UserSubscription, model string, forwardErrored bool, cyberBlockKey string, channelFields service.ChannelUsageFields, requestPayloadHash string) {
	mark := service.GetOpsCyberPolicy(c)
	if mark == nil {
		return
	}
	if c.GetBool(cyberPolicyRecordedKey) {
		return
	}
	c.Set(cyberPolicyRecordedKey, true)
	model = clientRequestedModel(c, model)
	upstreamModel := service.OpsUpstreamModel(c)

	requestID := c.Writer.Header().Get("X-Request-Id")
	var userID, apiKeyID int64
	var userEmail, apiKeyName, groupName string
	var groupID *int64
	if apiKey != nil {
		apiKeyID = apiKey.ID
		apiKeyName = apiKey.Name
		groupID = apiKey.GroupID
		if apiKey.User != nil {
			userID = apiKey.User.ID
			userEmail = apiKey.User.Email
		}
		if apiKey.Group != nil {
			groupName = apiKey.Group.Name
		}
	}
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := ""
	var accountID int64
	if account != nil {
		accountID = account.ID
		upstreamEndpoint = GetUpstreamEndpoint(c, account.Platform)
	}
	stream := false
	if v, ok := c.Get(opsStreamKey); ok {
		if b, ok := v.(bool); ok {
			stream = b
		}
	}
	cmSvc := h.contentModerationService
	gwSvc := h.gatewayService
	opsSvc := h.opsService
	apiKeySvc := h.apiKeyService
	requestPath := ""
	if c.Request != nil && c.Request.URL != nil {
		requestPath = c.Request.URL.Path
	}
	platform := resolveOpsPlatform(apiKey, guessPlatformFromPath(requestPath))
	var clientRequestID, userAgent, clientIPStr string
	sessionID := service.ExtractClientSessionID(c)
	requestCtx := context.Background()
	if c.Request != nil {
		requestCtx = c.Request.Context()
		clientRequestID, _ = c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		userAgent = c.GetHeader("User-Agent")
		clientIPStr = strings.TrimSpace(ip.GetClientIP(c))
	}
	var gatewayMeta gatewaylite.RequestMeta
	if meta, ok := gatewaylite.RequestMetaFromContext(requestCtx); ok {
		gatewayMeta = meta
		userID = meta.UserID
		if strings.TrimSpace(meta.UserEmail) != "" {
			userEmail = meta.UserEmail
		}
	}
	apiKeyPrefix := ""
	if apiKey != nil {
		apiKeyPrefix = keyPrefix(apiKey.Key, 8)
	}
	opsMeta := cyberPolicyOpsErrorMeta{
		RequestID:       requestID,
		ClientRequestID: clientRequestID,
		Platform:        platform,
		Model:           model,
		RequestPath:     requestPath,
		Stream:          stream,
		InboundEndpoint: inboundEndpoint,
		UserAgent:       userAgent,
		APIKeyPrefix:    apiKeyPrefix,
		UserID:          userID,
		APIKeyID:        apiKeyID,
		AccountID:       accountID,
		GroupID:         groupID,
		ClientIP:        clientIPStr,
		CreatedAt:       time.Now(),
	}
	go func(meta gatewaylite.RequestMeta) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if strings.TrimSpace(meta.RequestID) != "" {
			ctx = gatewaylite.ContextWithRequestMeta(ctx, meta)
		}
		if cmSvc != nil {
			cmSvc.RecordCyberPolicyEvent(ctx, service.CyberPolicyRecordInput{
				RequestID:          requestID,
				UserID:             userID,
				UserEmail:          userEmail,
				OrganizationID:     meta.OrganizationID,
				BillingSubjectType: meta.BillingSubjectType,
				BillingSubjectID:   meta.BillingSubjectID,
				ControlPlaneKeyID:  meta.KeyID,
				APIKeyID:           apiKeyID,
				APIKeyName:         apiKeyName,
				GroupID:            groupID,
				GroupName:          groupName,
				Endpoint:           inboundEndpoint,
				Model:              model,
				UpstreamMessage:    mark.Message,
				UpstreamBody:       mark.Body,
				UpstreamStatus:     mark.UpstreamStatus,
				UpstreamInTok:      mark.UpstreamInTok,
				UpstreamOutTok:     mark.UpstreamOutTok,
			})
		}
		if forwardErrored && gwSvc != nil {
			gwSvc.RecordCyberPolicyUsageLog(ctx, service.CyberPolicyUsageInput{
				APIKey:             apiKey,
				Account:            account,
				Subscription:       subscription,
				RequestID:          requestID,
				Model:              model,
				UpstreamModel:      upstreamModel,
				Stream:             stream,
				InputTokens:        mark.UpstreamInTok,
				OutputTokens:       mark.UpstreamOutTok,
				InboundEndpoint:    inboundEndpoint,
				UpstreamEndpoint:   upstreamEndpoint,
				UserAgent:          userAgent,
				IPAddress:          clientIPStr,
				SessionID:          sessionID,
				RequestPayloadHash: requestPayloadHash,
				APIKeyService:      apiKeySvc,
				ChannelUsageFields: channelFields,
			})
		}
		if gwSvc != nil && cyberBlockKey != "" {
			gwSvc.MarkCyberSessionBlocked(ctx, cyberBlockKey)
		}
		if opsSvc != nil {
			enqueueOpsErrorLog(opsSvc, buildCyberPolicyOpsErrorEntry(opsMeta, mark))
		}
	}(gatewayMeta)
}

// clearCyberPolicyTurnState resets the cyber mark and the per-request recorded
// guard. WS-only: called at the END of AfterTurn, after recordCyberPolicyIfMarked
// and RecordUsage (which reads CyberBlocked) have both consumed the mark.
func clearCyberPolicyTurnState(c *gin.Context) {
	if c == nil {
		return
	}
	service.ClearOpsCyberPolicy(c)
	c.Set(cyberPolicyRecordedKey, false)
}

func summarizeWSCloseErrorForLog(err error) (string, string) {
	if err == nil {
		return "-", "-"
	}
	statusCode := coderws.CloseStatus(err)
	if statusCode == -1 {
		return "-", "-"
	}
	closeStatus := fmt.Sprintf("%d(%s)", int(statusCode), statusCode.String())
	closeReason := "-"
	var closeErr coderws.CloseError
	if errors.As(err, &closeErr) {
		reason := strings.TrimSpace(closeErr.Reason)
		if reason != "" {
			closeReason = reason
		}
	}
	return closeStatus, closeReason
}

// [v0.2.5 新增]
type grokMediaEligibilityProber interface {
	ProbeMediaEligibility(ctx context.Context, accountID int64) (bool, string, error)
}
