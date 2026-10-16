package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const contentModerationForwardBodyContextKey = "content_moderation_forward_body"
const contentModerationBoundAccountAllowsBlockedKeywordsKey = "content_moderation_bound_account_allows_blocked_keywords"
const contentModerationLocalPolicyOnlyKey = "content_moderation_local_policy_only"
const contentModerationDisableHistoryIsolationKey = "content_moderation_disable_history_isolation"

func (h *GatewayHandler) checkContentModeration(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) *service.ContentModerationDecision {
	if h == nil || h.contentModerationService == nil {
		return nil
	}
	return runContentModeration(c, reqLog, h.contentModerationService, h.gatewayService, apiKey, subject, protocol, model, body)
}

func contentModerationStatus(decision *service.ContentModerationDecision) int {
	if decision == nil || decision.StatusCode < 400 || decision.StatusCode > 599 {
		return http.StatusForbidden
	}
	return decision.StatusCode
}

func contentModerationErrorCode(decision *service.ContentModerationDecision) string {
	if decision == nil {
		return "content_policy_violation"
	}
	switch decision.Action {
	case service.ContentModerationActionLanguageBlock:
		return "unsupported_language"
	case service.ContentModerationActionError:
		if decision.ErrorCode != "" {
			return decision.ErrorCode
		}
		return "invalid_request_error"
	}
	switch decision.RouteReason {
	case "websocket_reconnect_required":
		return "keyword_group_reconnect_required"
	case "no_candidate":
		return "keyword_allowed_group_unavailable"
	}
	return "content_policy_violation"
}

func clientRequestedModel(c *gin.Context, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if c == nil || c.Request == nil {
		return fallback
	}
	if model, ok := service.RequestedPublicModelFromContext(c.Request.Context()); ok {
		return model
	}
	return fallback
}

func clientRequestedUsageFields(c *gin.Context, mapping service.ChannelMappingResult, fallbackModel, upstreamModel string) service.ChannelUsageFields {
	return mapping.ToUsageFields(clientRequestedModel(c, fallbackModel), upstreamModel)
}

// mappedOpenAIAccountScheduleModel keeps model-scoped health keyed by the same
// model that reaches the selected account. The channel/group mapping is applied
// before account-specific mapping in forwarding, so recording the public model
// here would cool down the wrong account+model pair after an alias fails.
func mappedOpenAIAccountScheduleModel(ctx context.Context, account *service.Account, mapping service.ChannelMappingResult, requestedModel string) string {
	model := strings.TrimSpace(requestedModel)
	if mapping.Mapped && strings.TrimSpace(mapping.MappedModel) != "" {
		model = strings.TrimSpace(mapping.MappedModel)
	}
	return service.MappedAccountModelForRequest(ctx, nil, account, model)
}

func (h *OpenAIGatewayHandler) checkContentModeration(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) *service.ContentModerationDecision {
	if h == nil || h.contentModerationService == nil {
		return nil
	}
	return runContentModeration(c, reqLog, h.contentModerationService, h.gatewayService, apiKey, subject, protocol, model, body)
}

type keywordPolicyGroupResolver interface {
	ResolveBlockedKeywordRequestGroup(context.Context, *service.APIKey, string, string, []byte, bool) (*service.KeywordRouteResult, error)
}

func runContentModeration(c *gin.Context, reqLog *zap.Logger, svc *service.ContentModerationService, gatewaySvc keywordPolicyGroupResolver, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) *service.ContentModerationDecision {
	return runContentModerationWithEnvelope(c, reqLog, svc, gatewaySvc, apiKey, subject, protocol, model, body, false)
}

func runContentModerationWithEnvelope(c *gin.Context, reqLog *zap.Logger, svc *service.ContentModerationService, gatewaySvc keywordPolicyGroupResolver, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte, responsesWebSocket bool) *service.ContentModerationDecision {
	if svc == nil || c == nil || c.Request == nil {
		return nil
	}
	apiKeyID := int64(0)
	isolationScopeID := int64(0)
	if apiKey != nil {
		apiKeyID = apiKey.ID
		// control-plane（主站控制面）快照 Key 没有本地数据库 ID；复用已经
		// 验证过的稳定负数命名空间，既能隔离后续历史，也不会与本地 Key 串线。
		isolationScopeID = apiKey.WSIngressConcurrencyID(c.Request.Context())
	}
	isolationSessionID := service.ExtractClientSessionID(c)
	if meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context()); ok {
		// 企业共享 Key 不能让一个员工被拦截的轮次污染其他员工。稳定 Key 命名空间
		// 再混入企业、成员和显式会话标识；个人 Key 继续保持原有快路径。
		isolationScopeID = service.ContentModerationIsolationScopeID(
			isolationScopeID, meta.OrganizationID, meta.UserID, isolationSessionID,
		)
	}
	forwardBody := body
	isolatedTurns := 0
	if !c.GetBool(contentModerationDisableHistoryIsolationKey) && !svc.IsAIOnly(c.Request.Context()) {
		forwardBody, isolatedTurns = svc.IsolateBlockedHistory(isolationScopeID, protocol, body)
	}
	// 后续模型映射、能力判断和真正转发必须读取同一份隔离副本；每次检查都
	// 覆盖 context 值，避免 WebSocket 多轮复用 gin.Context 时拿到上一轮 body。
	c.Set(contentModerationForwardBodyContextKey, forwardBody)
	body = forwardBody
	if isolatedTurns > 0 && reqLog != nil {
		reqLog.Info("content_moderation.blocked_history_isolated",
			zap.Int64("api_key_id", apiKeyID),
			zap.String("protocol", protocol),
			zap.Int("isolated_turns", isolatedTurns),
			zap.Int("forward_body_bytes", len(forwardBody)),
		)
	}
	input := buildContentModerationInput(c, apiKey, subject, protocol, model, body)
	input.ResponsesWebSocket = responsesWebSocket
	// A WebSocket reuses gin.Context. Each invocation is a new server-owned
	// admission turn; retries/cache hits never inherit a caller's tracing ID.
	admissionID, turn := nextContentModerationAdmissionTurn(c)
	input.AdmissionID, input.Turn = admissionID, turn
	input.RequestID = service.ContentModerationAdmissionTurnID(admissionID, turn)
	input.IsolationScopeID = isolationScopeID
	input.IsolationSessionID = isolationSessionID
	input.LocalPolicyOnly = c.GetBool(contentModerationLocalPolicyOnlyKey)
	input.DisableHistoryIsolation = c.GetBool(contentModerationDisableHistoryIsolationKey)
	if apiKey != nil && apiKey.Group != nil {
		input.OriginalGroupAllowsBlockedKeywords = apiKey.Group.AllowBlockedKeywordRequests
	}
	input.KeywordRouteRequiresReconnect = c.GetBool("content_moderation_disable_keyword_reroute")
	if value, exists := c.Get(contentModerationBoundAccountAllowsBlockedKeywordsKey); exists {
		if allowed, ok := value.(bool); ok {
			input.BoundAccountAllowsBlockedKeywords = &allowed
		}
	}
	if gatewaySvc != nil && apiKey != nil && !c.GetBool("content_moderation_disable_keyword_reroute") {
		input.KeywordRouteResolver = func(ctx context.Context, requestedModel, requestedProtocol string, requestBody []byte, allowCrossGroup bool) (*service.KeywordRouteResult, error) {
			return gatewaySvc.ResolveBlockedKeywordRequestGroup(ctx, apiKey, requestedModel, requestedProtocol, requestBody, allowCrossGroup)
		}
	}
	if reqLog != nil {
		reqLog.Debug("content_moderation.gateway_check_start",
			zap.String("request_id", input.RequestID),
			zap.Int64("user_id", input.UserID),
			zap.Int64("api_key_id", input.APIKeyID),
			zap.String("api_key_name", input.APIKeyName),
			zap.Int64p("group_id", input.GroupID),
			zap.String("group_name", input.GroupName),
			zap.String("endpoint", input.Endpoint),
			zap.String("provider", input.Provider),
			zap.String("protocol", input.Protocol),
			zap.String("model", input.Model),
			zap.Int("body_bytes", len(body)),
		)
	}
	decision, err := svc.Check(c.Request.Context(), input)
	if err != nil {
		if reqLog != nil {
			reqLog.Warn("content_moderation.check_failed", zap.Error(err))
		}
		decision = &service.ContentModerationDecision{
			Allowed: false, Blocked: true, Flagged: false,
			Action: service.ContentModerationActionError, Verdict: "error", ErrorCode: "ai_runtime_unavailable",
			StatusCode: http.StatusServiceUnavailable, Message: "内容审查暂时无法完成，请稍后重试", PenaltyState: "none",
		}
	}
	if decision != nil && decision.Allowed && len(decision.ExcludedAccountIDs) > 0 {
		// The exclusion set is immutable request policy. Selection/retry functions
		// merge it with their own failed-account set instead of replacing either one.
		c.Request = c.Request.WithContext(service.ContextWithKeywordProtectedAccountIDs(
			c.Request.Context(), decision.ExcludedAccountIDs,
		))
	}
	if decision != nil && decision.RouteReason != "" {
		// 只返回分组编号和决定原因，不回显命中的敏感关键词原文。
		if decision.OriginalGroupID > 0 {
			c.Header("X-Gateway-Original-Group-ID", strconv.FormatInt(decision.OriginalGroupID, 10))
		}
		if decision.EffectiveGroupID > 0 {
			c.Header("X-Gateway-Effective-Group-ID", strconv.FormatInt(decision.EffectiveGroupID, 10))
		}
		c.Header("X-Keyword-Policy-Route", decision.RouteReason)
	}
	if decision != nil && decision.Blocked {
		// HTTP 拒绝会由 middleware（中间件）根据 4xx 自动退款；WebSocket
		// 在升级成功后状态已经是 101，必须显式标记，否则本轮没有上游用量
		// 也会误结算最小 1 分钱预留。标记是幂等的，HTTP 路径重复执行安全。
		meta, _ := gatewaylite.RequestMetaFromContext(c.Request.Context())
		if !meta.UpstreamConsumed && !meta.AsyncPersistenceUncertain {
			gatewaylite.MarkRequestReservationForRefund(c.Request.Context())
		}
	}
	if decision != nil && decision.RouteReason != "" {
		// 失败关闭也必须保留非敏感审计证据。否则 no_candidate（没有允许分组）
		// 虽然会正确退款，但 middleware（中间件）最终生成的零费用用量事件无法
		// 解释为什么被拒绝，主站会误显示成普通 403。这里只写来源、语种、原始/
		// 实际分组和稳定原因码，不保存或回显命中的关键词原文。
		c.Request = c.Request.WithContext(gatewaylite.ContextWithKeywordPolicyDecision(
			c.Request.Context(), decision.EffectiveGroupID, keywordPolicyEffectiveGroupName(input, decision),
			decision.ContentSource, decision.DetectedLanguages, decision.Rerouted, decision.RouteReason,
		))
	}
	if decision != nil && decision.Rerouted && decision.EffectiveGroup != nil && apiKey != nil {
		// 只替换当前请求持有的 API Key 副本；缓存快照和数据库绑定保持原样。
		cloned := *apiKey
		effectiveGroupID := decision.EffectiveGroup.ID
		cloned.GroupID = &effectiveGroupID
		cloned.RequestGroupOverrideID = &effectiveGroupID
		cloned.Group = decision.EffectiveGroup
		*apiKey = cloned
		// gateway-lite 调度会优先读取 ctxkey.Group；只改 API Key 而留下旧上下文，
		// 会让后续模型映射、账号选择和计费重新落回原分组。
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, decision.EffectiveGroup))
		if freezer, ok := gatewaySvc.(interface {
			FreezeRatePolicyForAPIKey(context.Context, *service.APIKey, time.Time) (context.Context, error)
		}); ok {
			billingStartedAt := time.Now()
			if originalPolicy, exists := service.EffectiveRatePolicyFromContext(c.Request.Context()); exists && originalPolicy.ResolvedAtMillis > 0 {
				// 切到允许敏感词分组会改变分组价格，但不能改变同一个请求的计价时刻。
				// 否则内容检查刚好跨过高峰边界时，同一请求会被按检查结束时重新定价。
				billingStartedAt = time.UnixMilli(originalPolicy.ResolvedAtMillis)
			}
			if frozenCtx, freezeErr := freezer.FreezeRatePolicyForAPIKey(c.Request.Context(), apiKey, billingStartedAt); freezeErr == nil {
				c.Request = c.Request.WithContext(frozenCtx)
				if policy, exists := service.EffectiveRatePolicyFromContext(frozenCtx); exists {
					// The original group was admitted before moderation. A reroute to a
					// more expensive group must enlarge the same request reservation before
					// any upstream account sees the body; lower/equal rates stay allocation-free.
					targetCents := gatewaylite.SalesRateReservationCents(policy.EffectiveRateMultiplier)
					if _, reserveErr := gatewaylite.EnsureRequestReservation(frozenCtx, targetCents); reserveErr != nil {
						gatewaylite.MarkRequestReservationForRefund(frozenCtx)
						decision.Allowed = false
						decision.Blocked = true
						decision.StatusCode = http.StatusServiceUnavailable
						decision.Message = "Billing quota is unavailable"
						if errors.Is(reserveErr, gatewaylite.ErrRequestQuotaInsufficient) {
							decision.StatusCode = http.StatusTooManyRequests
							decision.Message = "Insufficient quota for the routed group"
						}
						if reqLog != nil {
							reqLog.Warn("content_moderation.reroute_reservation_failed",
								zap.Int64("effective_group_id", decision.EffectiveGroupID),
								zap.Int64("estimated_cents", targetCents),
								zap.Error(reserveErr),
							)
						}
					}
				}
			} else {
				gatewaylite.MarkRequestReservationForRefund(c.Request.Context())
				if reqLog != nil {
					reqLog.Error("content_moderation.rate_policy_freeze_failed", zap.Error(freezeErr))
				}
				decision.Blocked = true
				decision.StatusCode = http.StatusInternalServerError
				decision.Message = "Billing policy is unavailable"
			}
		}
		c.Set(string(middleware2.ContextKeySubscription), decision.EffectiveSubscription)
	}
	if reqLog != nil && decision != nil {
		reqLog.Debug("content_moderation.gateway_check_done",
			zap.String("request_id", input.RequestID),
			zap.Bool("allowed", decision.Allowed),
			zap.Bool("blocked", decision.Blocked),
			zap.Bool("flagged", decision.Flagged),
			zap.String("action", decision.Action),
			zap.Int("status_code", decision.StatusCode),
			zap.String("highest_category", decision.HighestCategory),
			zap.Float64("highest_score", decision.HighestScore),
		)
	}
	return decision
}

func nextContentModerationAdmissionTurn(c *gin.Context) (string, uint64) {
	admission, _ := c.Get("content_moderation_admission_id")
	admissionID, _ := admission.(string)
	if admissionID == "" {
		admissionID = uuid.NewString()
		c.Set("content_moderation_admission_id", admissionID)
	}
	turnValue, _ := c.Get("content_moderation_admission_turn")
	turn, _ := turnValue.(uint64)
	turn++
	c.Set("content_moderation_admission_turn", turn)
	return admissionID, turn
}

func contentModerationForwardBody(c *gin.Context, fallback []byte) []byte {
	if c == nil {
		return fallback
	}
	value, exists := c.Get(contentModerationForwardBodyContextKey)
	if !exists {
		return fallback
	}
	body, ok := value.([]byte)
	if !ok || body == nil {
		return fallback
	}
	return body
}

func keywordPolicyEffectiveGroupName(input service.ContentModerationCheckInput, decision *service.ContentModerationDecision) string {
	if decision != nil && decision.EffectiveGroup != nil {
		return decision.EffectiveGroup.Name
	}
	return input.GroupName
}

func buildContentModerationInput(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, protocol string, model string, body []byte) service.ContentModerationCheckInput {
	input := service.ContentModerationCheckInput{
		RequestID: contentModerationRequestID(c.Request.Context()),
		UserID:    subject.UserID,
		Endpoint:  GetInboundEndpoint(c),
		Provider:  contentModerationProvider(apiKey),
		Model:     clientRequestedModel(c, model),
		Protocol:  protocol,
		Body:      body,
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context()); ok {
		// 入口冻结身份，后续审核 worker 不再从已经结束的 HTTP context 猜测企业归属。
		input.UserID = meta.UserID
		input.UserEmail = meta.UserEmail
		input.OrganizationID = meta.OrganizationID
		input.BillingSubjectType = strings.TrimSpace(meta.BillingSubjectType)
		input.BillingSubjectID = meta.BillingSubjectID
		input.ControlPlaneKeyID = strings.TrimSpace(meta.KeyID)
		input.GatewayNodeID = strings.TrimSpace(meta.GatewayID)
		input.Region = strings.TrimSpace(meta.Region)
	}
	if resolvedPlatform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
		input.Provider = resolvedPlatform
	}
	if forcedPlatform, ok := middleware2.GetForcePlatformFromContext(c); ok {
		input.Provider = strings.TrimSpace(forcedPlatform)
	}
	if apiKey != nil {
		input.APIKeyID = apiKey.ID
		input.APIKeyName = apiKey.Name
		if apiKey.User != nil {
			input.UserEmail = apiKey.User.Email
		}
		if apiKey.GroupID != nil {
			groupID := *apiKey.GroupID
			input.GroupID = &groupID
		}
		if apiKey.Group != nil {
			input.GroupName = apiKey.Group.Name
		}
	}
	if input.Endpoint == "" && c.Request != nil && c.Request.URL != nil {
		input.Endpoint = c.Request.URL.Path
	}
	return input
}

func contentModerationProvider(apiKey *service.APIKey) string {
	if apiKey == nil || apiKey.Group == nil {
		return ""
	}
	return strings.TrimSpace(apiKey.Group.Platform)
}

func contentModerationRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if requestID, ok := ctx.Value(ctxkey.RequestID).(string); ok {
		return strings.TrimSpace(requestID)
	}
	return ""
}
