package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const (
	OpenAIGroupFailoverEndpointResponses       = "/v1/responses"
	OpenAIGroupFailoverEndpointChatCompletions = "/v1/chat/completions"
	OpenAIGroupFailoverEndpointMessages        = "/v1/messages"
)

// OpenAIGroupFailoverLocalRejection 标识在访问上游前已经确定的业务拒绝。
// 这类失败换到另一个分组仍然无效，且可能绕过计费或安全边界，因此必须失败关闭。
type OpenAIGroupFailoverLocalRejection string

const (
	OpenAIGroupFailoverLocalRejectionNone        OpenAIGroupFailoverLocalRejection = ""
	OpenAIGroupFailoverLocalRejectionAuth        OpenAIGroupFailoverLocalRejection = "auth"
	OpenAIGroupFailoverLocalRejectionBalance     OpenAIGroupFailoverLocalRejection = "balance"
	OpenAIGroupFailoverLocalRejectionQuota       OpenAIGroupFailoverLocalRejection = "quota"
	OpenAIGroupFailoverLocalRejectionConcurrency OpenAIGroupFailoverLocalRejection = "concurrency"
	OpenAIGroupFailoverLocalRejectionContent     OpenAIGroupFailoverLocalRejection = "content_policy"
	OpenAIGroupFailoverLocalRejectionPolicy      OpenAIGroupFailoverLocalRejection = "policy"
)

type OpenAIGroupFailoverRequest struct {
	Endpoint           string
	RequestedModel     string
	APIKey             *APIKey
	PreviousResponseID string
	WebSocket          bool
	Image              bool
	Video              bool
	Compact            bool
	ResponseStarted    bool
	AlreadyFailedOver  bool
	LocalRejection     OpenAIGroupFailoverLocalRejection
}

// OpenAIGroupFailoverPlan 是一次请求的只读跨分组计划。计费仍使用请求开始时
// 冻结的原分组策略；TargetGroup 只用于账号调度和最终承接分组审计。
type OpenAIGroupFailoverPlan struct {
	Eligible                          bool
	Reason                            string
	Strategy                          string
	SourceGroup                       *Group
	TargetGroup                       *Group
	Subscription                      *UserSubscription
	TryCrossGroupBeforeSourceFallback bool
	TrySourceFallbackBeforeCrossGroup bool
}

type openAIGroupFailoverAttemptedContextKey struct{}

// ResolveOpenAIGroupFailoverPlan 解析并校验跨分组目标，但不改变请求上下文。
// 首版只承接普通 OpenAI 文本 HTTP，避免有状态、媒体和长连接协议发生重复执行。
func (s *OpenAIGatewayService) ResolveOpenAIGroupFailoverPlan(
	ctx context.Context,
	sourceGroup *Group,
	request OpenAIGroupFailoverRequest,
) (OpenAIGroupFailoverPlan, error) {
	strategy, validStrategy := NormalizeGroupFailoverStrategy("")
	if sourceGroup != nil {
		if normalized, ok := NormalizeGroupFailoverStrategy(sourceGroup.FailoverStrategy); ok {
			strategy, validStrategy = normalized, true
		} else {
			validStrategy = false
		}
	}
	plan := OpenAIGroupFailoverPlan{
		Reason:      "not_configured",
		Strategy:    strategy,
		SourceGroup: sourceGroup,
	}
	plan.TryCrossGroupBeforeSourceFallback = strategy == FailoverStrategyOriginalModelFirst
	plan.TrySourceFallbackBeforeCrossGroup = strategy == FailoverStrategyCurrentGroupFirst

	if sourceGroup == nil || sourceGroup.ID <= 0 || sourceGroup.FailoverGroupID == nil || *sourceGroup.FailoverGroupID <= 0 {
		return plan, nil
	}
	if !validStrategy {
		plan.Reason = "invalid_strategy"
		return plan, nil
	}
	if reason := openAIGroupFailoverRequestRefusalReason(ctx, request); reason != "" {
		plan.Reason = reason
		return plan, nil
	}
	if sourceGroup.Platform != PlatformOpenAI {
		plan.Reason = "source_platform_not_supported"
		return plan, nil
	}
	targetID := *sourceGroup.FailoverGroupID
	if targetID == sourceGroup.ID {
		plan.Reason = "self_reference"
		return plan, nil
	}
	if s == nil || (s.schedulerSnapshot == nil && (s.channelService == nil || s.channelService.groupRepo == nil)) {
		plan.Reason = "group_repository_unavailable"
		return plan, nil
	}
	target, err := s.openAIGroupFailoverTargetGroup(ctx, targetID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			plan.Reason = "target_not_found"
			return plan, nil
		}
		return plan, err
	}
	plan.TargetGroup = target
	if target == nil || target.ID <= 0 {
		plan.Reason = "target_not_found"
		return plan, nil
	}
	if !target.IsActive() {
		plan.Reason = "target_inactive"
		return plan, nil
	}
	if target.Platform != PlatformOpenAI {
		plan.Reason = "target_platform_not_supported"
		return plan, nil
	}
	if !GroupAccessibleFromRequest(ctx, target) {
		plan.Reason = "target_not_accessible"
		return plan, nil
	}
	if target.FailoverGroupID != nil && *target.FailoverGroupID == sourceGroup.ID {
		plan.Reason = "cycle_detected"
		return plan, nil
	}

	apiKey := request.APIKey
	if apiKey != nil && apiKey.User != nil {
		// 订阅型分组的授权以「有效订阅」为准（见下面的 IsSubscriptionType 分支），
		// 不套用 CanBindGroup 的公开分组限制，否则受限用户的有效订阅会被误拦。
		if !GroupTierAllowedForUser(apiKey.User, target) {
			plan.Reason = "target_not_authorized"
			return plan, nil
		}
		if !target.IsSubscriptionType() && !apiKey.User.CanBindGroup(target.ID, target.IsExclusive) {
			plan.Reason = "target_not_authorized"
			return plan, nil
		}
		if target.IsSubscriptionType() {
			if s.userSubRepo == nil {
				plan.Reason = "target_subscription_unavailable"
				return plan, nil
			}
			subscription, subscriptionErr := s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, apiKey.User.ID, target.ID)
			if subscriptionErr != nil || subscription == nil {
				plan.Reason = "target_subscription_unavailable"
				return plan, nil
			}
			plan.Subscription = subscription
		}
		if s.billingCacheService != nil {
			targetIDCopy := target.ID
			candidateKey := *apiKey
			candidateKey.GroupID = &targetIDCopy
			candidateKey.Group = target
			if eligibilityErr := s.billingCacheService.CheckBillingEligibilityWithoutRateConsumption(
				ctx,
				apiKey.User,
				&candidateKey,
				target,
				plan.Subscription,
				target.Platform,
			); eligibilityErr != nil {
				plan.Reason = "target_billing_ineligible"
				return plan, nil
			}
		}
	}

	plan.Eligible = true
	plan.Reason = "eligible"
	return plan, nil
}

func (s *OpenAIGatewayService) openAIGroupFailoverTargetGroup(ctx context.Context, targetID int64) (*Group, error) {
	if s != nil && s.schedulerSnapshot != nil {
		if target, err := s.schedulerSnapshot.GetGroupByID(ctx, targetID); err != nil || target != nil {
			return target, err
		}
	}
	if s == nil || s.channelService == nil || s.channelService.groupRepo == nil {
		return nil, nil
	}
	return s.channelService.groupRepo.GetByIDLite(ctx, targetID)
}

func openAIGroupFailoverRequestRefusalReason(ctx context.Context, request OpenAIGroupFailoverRequest) string {
	if request.AlreadyFailedOver || OpenAIGroupFailoverAttempted(ctx) {
		return "already_failed_over"
	}
	if request.LocalRejection != OpenAIGroupFailoverLocalRejectionNone {
		return "local_rejection_" + string(request.LocalRejection)
	}
	// 命中过关键词策略后，账号排除集合是按入口分组计算的。直接带着它
	// 切到 B 会漏掉 B 自己的账号安全边界，因此首版安全地禁止跨组。
	if len(KeywordProtectedAccountIDsFromContext(ctx)) > 0 {
		return "content_policy_account_scope"
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok && meta.KeywordPolicyMatched {
		return "content_policy_matched"
	}
	if request.ResponseStarted {
		return "response_started"
	}
	if request.WebSocket {
		return "websocket_not_supported"
	}
	if request.Image || request.Video {
		return "media_not_supported"
	}
	if request.Compact {
		return "compact_not_supported"
	}
	if strings.TrimSpace(request.PreviousResponseID) != "" {
		return "stateful_request_not_supported"
	}
	if !openAIGroupFailoverTextEndpoint(request.Endpoint) {
		return "endpoint_not_supported"
	}
	return ""
}

func openAIGroupFailoverTextEndpoint(endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if queryIndex := strings.IndexByte(endpoint, '?'); queryIndex >= 0 {
		endpoint = endpoint[:queryIndex]
	}
	endpoint = "/" + strings.TrimLeft(endpoint, "/")
	for _, suffix := range []string{
		OpenAIGroupFailoverEndpointResponses,
		OpenAIGroupFailoverEndpointChatCompletions,
		OpenAIGroupFailoverEndpointMessages,
	} {
		if endpoint == suffix || strings.HasSuffix(endpoint, suffix) {
			return true
		}
	}
	return false
}

// ShouldOpenAIGroupFailover 只接受明确可恢复的失败。客户端参数、上游鉴权和
// 内容策略拒绝不会因为换分组而变正确，因此 4xx 仅放行 408 与 429。
func ShouldOpenAIGroupFailover(plan OpenAIGroupFailoverPlan, failure error) bool {
	if !plan.Eligible || plan.TargetGroup == nil || failure == nil {
		return false
	}
	if errors.Is(failure, context.Canceled) {
		return false
	}
	if errors.Is(failure, context.DeadlineExceeded) || errors.Is(failure, ErrNoAvailableAccounts) || errors.Is(failure, ErrGatewayGroupUnavailable) {
		return true
	}
	if errors.Is(failure, ErrNoAvailableCompactAccounts) {
		return false
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(failure, &failoverErr) || failoverErr == nil || !failoverErr.ShouldRetryNextAccount() {
		return false
	}
	status := failoverErr.StatusCode
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// ApplyOpenAIGroupFailover 只替换调度分组和最终承接证据，不修改 API Key、
// 原始分组或冻结计费策略。目标分组的同组模型兜底保持关闭，避免一次故障扩散成多重尝试。
func ApplyOpenAIGroupFailover(ctx context.Context, plan OpenAIGroupFailoverPlan) (context.Context, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !plan.Eligible || plan.SourceGroup == nil || plan.TargetGroup == nil || plan.TargetGroup.ID <= 0 {
		return ctx, 0, errors.New("openai group failover plan is not eligible")
	}
	if OpenAIGroupFailoverAttempted(ctx) {
		return ctx, 0, errors.New("openai group failover already attempted")
	}

	ctx = withFrozenClientResponseModelPolicy(ctx, plan.SourceGroup)
	ctx = context.WithValue(ctx, openAIGroupFailoverAttemptedContextKey{}, true)
	ctx = context.WithValue(ctx, ctxkey.Group, plan.TargetGroup)
	ctx = gatewaylite.ContextWithEffectiveGroup(ctx, plan.TargetGroup.ID, plan.TargetGroup.Name)
	ctx = context.WithValue(ctx, openAIGroupModelFallbackContextKey{}, nil)
	ctx = context.WithValue(ctx, openAIGroupModelFallbackAttemptedContextKey{}, false)
	// current_group_first may enter B from A's fallback-only pass. Clear that
	// request-local flag so B starts from the client's original model instead
	// of accidentally forcing B's own fallback model.
	ctx = context.WithValue(ctx, openAIGroupModelFallbackOnlyContextKey{}, false)
	ctx = WithoutOpenAIGroupModelFallbackPolicy(ctx)
	return ctx, plan.TargetGroup.ID, nil
}

func OpenAIGroupFailoverAttempted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	attempted, _ := ctx.Value(openAIGroupFailoverAttemptedContextKey{}).(bool)
	return attempted
}

// RestoreOpenAIGroupForModelFallback 在“优先保持原模型”策略中，仅当目标分组
// 也失败后恢复 A 分组并放行一次同组模型兜底；跨组已尝试标记继续保留，禁止 A→B→A→B 环路。
func RestoreOpenAIGroupForModelFallback(ctx context.Context, plan OpenAIGroupFailoverPlan) (context.Context, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !plan.Eligible || plan.SourceGroup == nil || plan.SourceGroup.ID <= 0 || plan.Strategy != FailoverStrategyOriginalModelFirst {
		return ctx, 0, errors.New("source group model fallback is not available")
	}
	ctx = context.WithValue(ctx, ctxkey.Group, plan.SourceGroup)
	ctx = gatewaylite.ContextWithEffectiveGroup(ctx, plan.SourceGroup.ID, plan.SourceGroup.Name)
	ctx = context.WithValue(ctx, openAIGroupModelFallbackContextKey{}, nil)
	ctx = context.WithValue(ctx, openAIGroupModelFallbackAttemptedContextKey{}, false)
	ctx = WithOpenAIGroupModelFallbackPolicy(ctx)
	ctx = WithOpenAIGroupModelFallbackOnly(ctx)
	return ctx, plan.SourceGroup.ID, nil
}
