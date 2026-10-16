package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) Live(c *gin.Context) {
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
	if apiKey.Group == nil || (apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite) {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Live is not supported for this platform")
		return
	}
	if !gatewaylite.LiveEnabled() {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Live is disabled on this gateway")
		return
	}
	if !liveEnabledForAPIKey(apiKey) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Live is not enabled for this group")
		return
	}
	request, err := parseLiveCallRequest(c)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	model := strings.TrimSpace(gjson.GetBytes(request.Session, "model").String())
	if !compositeTargetPlatformAllowed(c, apiKey, model, service.PlatformOpenAI) {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Live only supports OpenAI models for Composite groups")
		return
	}
	if upstreamModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok && upstreamModel != model {
		rewrittenSession, rewriteErr := sjson.SetBytes(request.Session, "model", upstreamModel)
		if rewriteErr != nil {
			h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to apply Composite model route")
			return
		}
		request.Session = rewrittenSession
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.live",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if decision := h.checkContentModeration(
		c,
		reqLog,
		apiKey,
		subject,
		service.ContentModerationProtocolOpenAIResponses,
		model,
		request.Session,
	); decision != nil && decision.Blocked {
		h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	applyLiveContentModerationForwardSession(c, request)

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if h.billingCacheService == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Billing service unavailable")
		return
	}
	if err := h.billingCacheService.CheckBillingEligibilityForRequest(
		c.Request.Context(),
		apiKey.User,
		apiKey,
		apiKey.Group,
		subscription,
		service.QuotaPlatform(c.Request.Context(), apiKey),
		service.EstimateRequestTokensForLimit(request.Session),
	); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	groupID, groupMaxConcurrency := finalGroupConcurrencyLimit(c)
	userAcquire, err := h.concurrencyHelper.tryAcquireFinalUserSlotForAPIKey(
		c.Request.Context(),
		apiKey.User,
		subject.UserID,
		subject.Concurrency,
		groupID,
		groupMaxConcurrency,
		finalAPIKeyConcurrencyID(c),
		apiKey.KeyConcurrencyLimit,
	)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Live concurrency unavailable")
		return
	}
	if userAcquire == nil || !userAcquire.Acquired {
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Live concurrency limit reached")
		return
	}
	// Live 不经过普通 HTTP 的 AcquireUserSlotWithWait 入口，因此这里显式
	// 记录原始分组入口请求；该统计只用于后台观测，不参与限流或计费。
	userRelease := h.concurrencyHelper.withGroupIngressSlotFromContext(c, userAcquire.ReleaseFunc)
	defer userRelease()

	identity := liveCallIdentity(c, apiKey, subject.UserID, subscription)
	created, err := h.gatewayService.CreateLiveCall(c.Request.Context(), request, identity, subject.Concurrency)
	if err != nil {
		h.writeLiveCreateError(c, err)
		return
	}
	c.Header("Location", liveSidebandLocation(c.FullPath(), created.CallID))
	c.Data(http.StatusOK, "application/sdp", created.SDP)
}

func applyLiveContentModerationForwardSession(c *gin.Context, request *service.LiveCallRequest) {
	if request == nil {
		return
	}
	// 被拒轮次会在后续请求里从审查副本删除；Live 必须把同一副本交给
	// CreateLiveCall，否则审查虽然放行的是隔离结果，上游收到的却仍是原指令。
	request.Session = json.RawMessage(contentModerationForwardBody(c, request.Session))
}

func parseLiveCallRequest(c *gin.Context) (*service.LiveCallRequest, error) {
	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		sdp := c.PostForm("sdp")
		session := json.RawMessage(c.PostForm("session"))
		request := &service.LiveCallRequest{SDP: sdp, Session: session}
		if err := service.ValidateLiveCallRequest(request); err != nil {
			return nil, err
		}
		return request, nil
	}
	var request service.LiveCallRequest
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&request); err != nil {
		return nil, errors.New("request body must be valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON object")
	}
	if err := service.ValidateLiveCallRequest(&request); err != nil {
		return nil, err
	}
	return &request, nil
}

func liveSidebandLocation(fullPath, callID string) string {
	prefix := "/v1/live/"
	if strings.HasPrefix(fullPath, "/backend-api/codex/") {
		prefix = "/backend-api/codex/"
	}
	return prefix + url.PathEscape(callID)
}

func liveCallIdentity(
	c *gin.Context,
	apiKey *service.APIKey,
	userID int64,
	subscription *service.UserSubscription,
) service.LiveCallIdentity {
	var subscriptionID *int64
	if subscription != nil {
		value := subscription.ID
		subscriptionID = &value
	}
	organizationID := int64(0)
	organizationMaxConcurrency := 0
	if apiKey != nil && apiKey.User != nil {
		organizationID = apiKey.User.OrganizationID
		organizationMaxConcurrency = apiKey.User.OrganizationConcurrency
	}
	return service.LiveCallIdentity{
		APIKeyID:                   apiKey.WSIngressConcurrencyID(c.Request.Context()),
		UserID:                     userID,
		GroupID:                    finalLiveGroupID(c, apiKey),
		GroupMaxConcurrency:        finalLiveGroupMaxConcurrency(c),
		OrganizationID:             organizationID,
		OrganizationMaxConcurrency: organizationMaxConcurrency,
		APIKeyMaxConcurrency:       apiKey.KeyConcurrencyLimit,
		SubscriptionID:             subscriptionID,
		UserAgent:                  c.GetHeader("User-Agent"),
		IPAddress:                  ip.GetClientIP(c),
		InboundEndpoint:            GetInboundEndpoint(c),
	}
}

func finalLiveGroupID(c *gin.Context, apiKey *service.APIKey) *int64 {
	groupID, _ := finalGroupConcurrencyLimit(c)
	if groupID > 0 {
		return &groupID
	}
	if apiKey == nil {
		return nil
	}
	return apiKey.GroupID
}

func finalLiveGroupMaxConcurrency(c *gin.Context) int {
	_, groupMaxConcurrency := finalGroupConcurrencyLimit(c)
	return groupMaxConcurrency
}

func (h *OpenAIGatewayHandler) writeLiveCreateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrLiveConcurrencyFull):
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Live concurrency limit reached")
	case errors.Is(err, service.ErrLiveUnavailable):
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Live is unavailable")
	default:
		var attestationErr *service.LiveAttestationUnavailableError
		if errors.As(err, &attestationErr) {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", attestationErr.Error())
			return
		}
		var upstreamErr *service.UpstreamFailoverError
		if errors.As(err, &upstreamErr) && upstreamErr.StatusCode >= 400 && upstreamErr.StatusCode < 500 {
			h.errorResponse(c, upstreamErr.StatusCode, "invalid_request_error", "Live upstream rejected the request")
			return
		}
		h.errorResponse(c, http.StatusBadGateway, "api_error", "Live upstream request failed")
	}
}

func (h *OpenAIGatewayHandler) LiveSideband(c *gin.Context) {
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
	if !liveEnabledForAPIKey(apiKey) {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "Live is not enabled for this group")
		return
	}
	if !gatewaylite.LiveEnabled() {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Live is disabled on this gateway")
		return
	}
	identity := service.LiveCallIdentity{
		APIKeyID: apiKey.WSIngressConcurrencyID(c.Request.Context()),
		UserID:   subject.UserID,
	}
	record, err := h.gatewayService.GetLiveCallForIdentity(c.Request.Context(), c.Param("call_id"), identity)
	if err != nil {
		if errors.Is(err, service.ErrLiveIdentityMismatch) {
			h.errorResponse(c, http.StatusForbidden, "permission_error", "Live call belongs to another identity")
			return
		}
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Live call not found")
		return
	}
	downstream, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer func() { _ = downstream.CloseNow() }()
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.live_sideband",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("call_id", record.CallID),
	)
	// Live 会话已经绑定账号和分组，建连后的控制帧不能透明改路由；命中
	// 允许分组时与普通 Responses WebSocket 一样要求客户端重连。
	c.Set("content_moderation_disable_keyword_reroute", true)
	if allowed, policyErr := h.gatewayService.AccountAcceptsBlockedKeywords(
		c.Request.Context(), record.AccountID, apiKey.Group != nil && apiKey.Group.AllowBlockedKeywordRequests,
	); policyErr == nil {
		c.Set(contentModerationBoundAccountAllowsBlockedKeywordsKey, allowed)
	}
	frameHook := h.liveSidebandContentModerationHook(
		c, reqLog, apiKey, subject, record.Model,
		func(ctx context.Context, decision *service.ContentModerationDecision) {
			writeContentModerationWSError(ctx, downstream, decision)
		},
	)
	if err := h.gatewayService.ProxyLiveSidebandWithFrameHook(c.Request.Context(), record, downstream, frameHook); err != nil {
		reason := service.GatewayClientErrorMessage(c.Request.Context(), "live sideband closed")
		_ = downstream.Close(coderws.StatusInternalError, service.TruncateGatewayClientErrorReason(reason, 120))
		return
	}
	_ = downstream.Close(coderws.StatusNormalClosure, "")
}

// liveSidebandContentModerationHook 只审查会改变模型控制指令的客户端事件。
// 它把嵌套 instructions 投影成现有 Responses 审查请求，并将历史隔离产生的
// 删除结果同步回原始帧，确保审查副本与真正写上游的副本完全一致。
func (h *OpenAIGatewayHandler) liveSidebandContentModerationHook(
	c *gin.Context,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	fallbackModel string,
	onBlocked func(context.Context, *service.ContentModerationDecision),
) service.LiveSidebandClientFrameHook {
	return func(ctx context.Context, messageType coderws.MessageType, payload []byte) ([]byte, bool, error) {
		if messageType != coderws.MessageText {
			return payload, true, nil
		}
		moderationBody, instructionPath, model, ok, parseErr := liveSidebandInstructionModerationRequest(payload, fallbackModel)
		if errors.Is(parseErr, errLiveSidebandDuplicateInstructions) {
			decision := &service.ContentModerationDecision{
				Allowed: false, Blocked: true,
				Message:    "duplicate instructions fields are not allowed in a Live control event",
				StatusCode: http.StatusBadRequest,
				Action:     service.ContentModerationActionError,
			}
			if onBlocked != nil {
				onBlocked(ctx, decision)
			}
			return payload, false, nil
		}
		if parseErr != nil {
			return payload, false, parseErr
		}
		if !ok {
			return payload, true, nil
		}
		if h == nil || h.contentModerationService == nil || c == nil {
			return payload, true, nil
		}
		decision := h.checkContentModeration(
			c, reqLog, apiKey, subject,
			service.ContentModerationProtocolOpenAIResponses,
			model, moderationBody,
		)
		if decision != nil && decision.Blocked {
			if onBlocked != nil {
				onBlocked(ctx, decision)
			}
			return payload, false, nil
		}
		forwardBody := contentModerationForwardBody(c, moderationBody)
		forwardInstructions := gjson.GetBytes(forwardBody, "instructions")
		if forwardInstructions.Type == gjson.String {
			if forwardInstructions.String() == gjson.GetBytes(payload, instructionPath).String() {
				return payload, true, nil
			}
			rewritten, err := sjson.SetBytes(payload, instructionPath, forwardInstructions.String())
			return rewritten, true, err
		}
		rewritten, err := sjson.DeleteBytes(payload, instructionPath)
		return rewritten, true, err
	}
}

var errLiveSidebandDuplicateInstructions = errors.New("duplicate Live instructions fields")

func liveSidebandInstructionModerationRequest(payload []byte, fallbackModel string) (body []byte, instructionPath string, model string, ok bool, err error) {
	if !gjson.ValidBytes(payload) {
		return nil, "", "", false, nil
	}
	root := gjson.ParseBytes(payload)
	eventType := strings.TrimSpace(root.Get("type").String())
	var nestedObject gjson.Result
	var nestedPath string
	switch eventType {
	case "session.update":
		nestedObject = root.Get("session")
		nestedPath = "session."
		model = strings.TrimSpace(nestedObject.Get("model").String())
	case "response.create":
		nestedObject = root.Get("response")
		nestedPath = "response."
		model = strings.TrimSpace(nestedObject.Get("model").String())
		if model == "" {
			model = strings.TrimSpace(root.Get("model").String())
		}
	default:
		return nil, "", "", false, nil
	}

	// gjson.Get(path) 只返回一个值，无法表达同一对象中的重复 JSON key（键）。
	// 这里直接遍历原始对象，并把 instructions 的 ASCII 大小写变体视为同一
	// 控制字段；任一目标对象出现多个变体都拒绝，避免不同解析器选中不同值。
	nestedInstructions, nestedKey, nestedCount := liveSidebandInstructionField(nestedObject)
	topInstructions, topKey, topCount := liveSidebandInstructionField(root)
	if nestedCount+topCount > 1 {
		return nil, "", "", false, errLiveSidebandDuplicateInstructions
	}
	instructions := nestedInstructions
	if nestedCount == 1 && instructions.Type == gjson.String {
		instructionPath = nestedPath + nestedKey
	} else if topCount == 1 && topInstructions.Type == gjson.String {
		// 兼容部分 Live 客户端把控制字段放在事件顶层；仅当规范嵌套字段
		// 没有可审查字符串时才使用这一份。
		instructions = topInstructions
		instructionPath = topKey
	}
	if instructions.Type != gjson.String {
		return nil, "", "", false, nil
	}
	if model == "" {
		model = strings.TrimSpace(fallbackModel)
	}
	body, marshalErr := json.Marshal(map[string]string{"instructions": instructions.String()})
	if marshalErr != nil {
		return nil, "", "", false, marshalErr
	}
	return body, instructionPath, model, true, nil
}

func liveSidebandInstructionField(object gjson.Result) (value gjson.Result, key string, count int) {
	if !object.IsObject() {
		return gjson.Result{}, "", 0
	}
	object.ForEach(func(candidateKey, candidateValue gjson.Result) bool {
		if !strings.EqualFold(candidateKey.String(), "instructions") {
			return true
		}
		count++
		if count == 1 {
			key = candidateKey.String()
			value = candidateValue
		}
		return true
	})
	return value, key, count
}

func liveEnabledForAPIKey(apiKey *service.APIKey) bool {
	return apiKey != nil &&
		apiKey.Group != nil &&
		(apiKey.Group.Platform == service.PlatformOpenAI || apiKey.Group.Platform == service.PlatformComposite) &&
		apiKey.Group.AllowLive
}
