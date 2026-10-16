package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

var (
	ErrKeywordRouteNoCandidate = errors.New("no eligible keyword-allowed group")
	ErrKeywordRouteNoAccount   = errors.New("keyword-allowed groups have no schedulable account for model")
)

const maxKeywordRouteCandidates = 32

// ResolveBlockedKeywordRequestGroup 只在强关键词已经命中时调用。普通请求不会进入
// 此路径，因此不会新增数据库、Redis 或账号调度查询。候选只是请求态选择，不修改
// API Key 的持久绑定，也不写入原分组的粘性会话。
func (s *GatewayService) ResolveBlockedKeywordRequestGroup(
	ctx context.Context,
	apiKey *APIKey,
	requestedModel string,
	protocol string,
	body []byte,
	allowCrossGroup bool,
) (*KeywordRouteResult, error) {
	if s == nil || s.groupRepo == nil || apiKey == nil || apiKey.User == nil || apiKey.Group == nil {
		return nil, ErrKeywordRouteNoCandidate
	}
	originalGroup := apiKey.Group
	original, originalErr := s.resolveBlockedKeywordGroupAccountPolicy(ctx, originalGroup, requestedModel)
	if originalErr != nil {
		return nil, originalErr
	}
	if original.hasEligibleAccount {
		return &KeywordRouteResult{Group: originalGroup, ExcludedAccountIDs: original.excludedIDs}, nil
	}
	// 本地 glk_ 密钥只绑定当前轻量网关分组，也完全绕过主站钱包计费。
	// 因此它只能在原分组安全换号，绝不能借关键词路由免费进入其他分组。
	if IsGatewayLiteLocalAPIKeyContext(ctx) || IsGatewayLiteLocalAPIKey(apiKey.Key) {
		return nil, ErrKeywordRouteNoAccount
	}
	if !allowCrossGroup {
		return nil, ErrKeywordRouteNoAccount
	}
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	candidates := keywordRouteCandidates(apiKey, originalGroup, groups, requestedModel, protocol, body)
	if len(candidates) == 0 {
		return nil, ErrKeywordRouteNoCandidate
	}

	accountCandidateFound := false
	for index := range candidates {
		candidate := candidates[index]
		if !GroupAccessibleFromRequest(ctx, &candidate) {
			continue
		}
		candidate.Hydrated = true
		candidateID := candidate.ID
		candidateKey := *apiKey
		candidateKey.GroupID = &candidateID
		candidateKey.Group = &candidate

		var subscription *UserSubscription
		if candidate.IsSubscriptionType() {
			if s.userSubRepo == nil {
				continue
			}
			subscription, err = s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, apiKey.User.ID, candidate.ID)
			if err != nil || subscription == nil {
				continue
			}
		}
		if s.billingCacheService != nil {
			if err := s.billingCacheService.CheckBillingEligibilityWithoutRateConsumption(
				ctx, apiKey.User, &candidateKey, &candidate, subscription, candidate.Platform,
			); err != nil {
				continue
			}
		}
		policy, policyErr := s.resolveBlockedKeywordGroupAccountPolicy(ctx, &candidate, requestedModel)
		if policyErr != nil || !policy.hasEligibleAccount {
			continue
		}
		account, selectErr := s.SelectAccountForModelWithExclusions(ctx, &candidateID, "", requestedModel, policy.excludedIDs)
		if selectErr != nil || account == nil || !keywordRouteAccountBelongsToGroup(account, candidateID) {
			continue
		}
		accountCandidateFound = true
		return &KeywordRouteResult{Group: &candidate, Subscription: subscription, ExcludedAccountIDs: policy.excludedIDs}, nil
	}
	if !accountCandidateFound {
		return nil, ErrKeywordRouteNoAccount
	}
	return nil, ErrKeywordRouteNoCandidate
}

type blockedKeywordGroupAccountPolicy struct {
	excludedIDs        map[int64]struct{}
	hasEligibleAccount bool
}

func (s *GatewayService) resolveBlockedKeywordGroupAccountPolicy(ctx context.Context, group *Group, requestedModel string) (blockedKeywordGroupAccountPolicy, error) {
	if s == nil || s.accountRepo == nil || group == nil || group.ID <= 0 {
		return blockedKeywordGroupAccountPolicy{}, nil
	}
	groupID := group.ID
	ctx = s.withGroupContext(ctx, group)
	ctx, requestedModel = resolveGroupModelForSelection(ctx, group, requestedModel)
	platform := group.Platform
	hasForcePlatform := false
	if platform == PlatformComposite {
		decision, ok, err := s.resolveCompositeRouteDecision(ctx, group, requestedModel, CompositeRouteEndpointAny)
		if err != nil || !ok {
			return blockedKeywordGroupAccountPolicy{}, err
		}
		platform, requestedModel, hasForcePlatform = decision.TargetPlatform, decision.UpstreamModel, true
	}
	accounts, useMixed, err := s.listSchedulableAccounts(ctx, &groupID, platform, hasForcePlatform)
	if err != nil {
		return blockedKeywordGroupAccountPolicy{}, err
	}
	excluded := keywordPolicyExcludedAccountIDs(accounts, group.AllowBlockedKeywordRequests)
	for index := range accounts {
		account := &accounts[index]
		if _, blocked := excluded[account.ID]; blocked ||
			!s.isAccountAllowedForPlatform(account, platform, useMixed) ||
			!s.isAccountSchedulableForModelSelection(ctx, account, requestedModel) {
			continue
		}
		return blockedKeywordGroupAccountPolicy{excludedIDs: excluded, hasEligibleAccount: true}, nil
	}
	return blockedKeywordGroupAccountPolicy{excludedIDs: excluded}, nil
}

func keywordRouteProtocolCompatible(protocol string, group *Group) bool {
	if group == nil {
		return false
	}
	platform := strings.ToLower(strings.TrimSpace(group.Platform))
	switch protocol {
	case ContentModerationProtocolAnthropicMessages:
		return platform == PlatformAnthropic || platform == PlatformAntigravity || platform == PlatformGemini ||
			platform == PlatformComposite || (platform == PlatformOpenAI && group.AllowMessagesDispatch)
	case ContentModerationProtocolGemini:
		return platform == PlatformGemini || platform == PlatformAntigravity || platform == PlatformComposite
	case ContentModerationProtocolOpenAIResponses, ContentModerationProtocolOpenAIChat, ContentModerationProtocolOpenAIImages:
		return platform == PlatformOpenAI || platform == PlatformGrok || platform == PlatformComposite
	default:
		return false
	}
}

func keywordRouteModelAllowed(group *Group, model string) bool {
	if group == nil || strings.TrimSpace(model) == "" || !group.CustomModelsListEnabled() {
		return group != nil
	}
	for _, allowed := range group.ModelsListConfig.Models {
		if allowed == "*" || matchModelPattern(allowed, model) {
			return true
		}
	}
	return false
}

func keywordRouteNeedsImagePermission(protocol, model string, body []byte) bool {
	return protocol == ContentModerationProtocolOpenAIImages || IsExplicitImageGenerationIntent("/v1/responses", model, body)
}

func keywordRouteAccountBelongsToGroup(account *Account, groupID int64) bool {
	if account == nil || groupID <= 0 {
		return false
	}
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, binding := range account.AccountGroups {
		if binding.GroupID == groupID {
			return true
		}
	}
	return false
}

// ResolveBlockedKeywordRequestGroup 为 OpenAI 专用网关复用同一规则；候选来自
// scheduler snapshot（调度快照），不要求 OpenAIGatewayService 持有数据库仓储。
func (s *OpenAIGatewayService) ResolveBlockedKeywordRequestGroup(
	ctx context.Context,
	apiKey *APIKey,
	requestedModel string,
	protocol string,
	body []byte,
	allowCrossGroup bool,
) (*KeywordRouteResult, error) {
	if s == nil || s.schedulerSnapshot == nil || apiKey == nil || apiKey.User == nil || apiKey.Group == nil {
		return nil, ErrKeywordRouteNoCandidate
	}
	groups, err := s.schedulerSnapshot.ListActiveGroups(ctx)
	if err != nil {
		return nil, err
	}
	originalGroup := apiKey.Group
	original, originalErr := s.resolveBlockedKeywordGroupAccountPolicy(ctx, originalGroup, requestedModel)
	if originalErr != nil {
		return nil, originalErr
	}
	if original.hasEligibleAccount {
		return &KeywordRouteResult{Group: originalGroup, ExcludedAccountIDs: original.excludedIDs}, nil
	}
	if IsGatewayLiteLocalAPIKeyContext(ctx) || IsGatewayLiteLocalAPIKey(apiKey.Key) {
		return nil, ErrKeywordRouteNoAccount
	}
	if !allowCrossGroup {
		return nil, ErrKeywordRouteNoAccount
	}
	candidates := keywordRouteCandidates(apiKey, originalGroup, groups, requestedModel, protocol, body)
	if len(candidates) == 0 {
		return nil, ErrKeywordRouteNoCandidate
	}
	for index := range candidates {
		candidate := candidates[index]
		if !GroupAccessibleFromRequest(ctx, &candidate) {
			continue
		}
		candidate.Hydrated = true
		candidateID := candidate.ID
		candidateKey := *apiKey
		candidateKey.GroupID, candidateKey.Group = &candidateID, &candidate
		var subscription *UserSubscription
		if candidate.IsSubscriptionType() {
			if s.userSubRepo == nil {
				continue
			}
			subscription, err = s.userSubRepo.GetActiveByUserIDAndGroupID(ctx, apiKey.User.ID, candidate.ID)
			if err != nil || subscription == nil {
				continue
			}
		}
		if s.billingCacheService != nil {
			if err := s.billingCacheService.CheckBillingEligibilityWithoutRateConsumption(ctx, apiKey.User, &candidateKey, &candidate, subscription, candidate.Platform); err != nil {
				continue
			}
		}
		policy, policyErr := s.resolveBlockedKeywordGroupAccountPolicy(ctx, &candidate, requestedModel)
		if policyErr != nil || !policy.hasEligibleAccount {
			continue
		}
		account, selectErr := s.SelectAccountForModelWithExclusions(ctx, &candidateID, "", requestedModel, policy.excludedIDs)
		if selectErr != nil || account == nil || !keywordRouteAccountBelongsToGroup(account, candidateID) {
			continue
		}
		return &KeywordRouteResult{Group: &candidate, Subscription: subscription, ExcludedAccountIDs: policy.excludedIDs}, nil
	}
	return nil, ErrKeywordRouteNoAccount
}

func (s *OpenAIGatewayService) resolveBlockedKeywordGroupAccountPolicy(ctx context.Context, group *Group, requestedModel string) (blockedKeywordGroupAccountPolicy, error) {
	if s == nil || group == nil || group.ID <= 0 {
		return blockedKeywordGroupAccountPolicy{}, nil
	}
	groupID := group.ID
	ctx = context.WithValue(ctx, ctxkey.Group, group)
	ctx, requestedModel = resolveGroupModelForSelection(ctx, group, requestedModel)
	accounts, err := s.listSchedulableAccounts(ctx, &groupID, group.Platform)
	if err != nil {
		return blockedKeywordGroupAccountPolicy{}, err
	}
	excluded := keywordPolicyExcludedAccountIDs(accounts, group.AllowBlockedKeywordRequests)
	for index := range accounts {
		account := &accounts[index]
		if _, blocked := excluded[account.ID]; blocked ||
			!isOpenAICompatibleAccountEligibleForRequest(ctx, account, normalizeOpenAICompatiblePlatform(group.Platform), requestedModel, false, "", s.rateLimitService) {
			continue
		}
		return blockedKeywordGroupAccountPolicy{excludedIDs: excluded, hasEligibleAccount: true}, nil
	}
	return blockedKeywordGroupAccountPolicy{excludedIDs: excluded}, nil
}

func keywordRouteGroupAllowedForKey(apiKey *APIKey, groupID int64) bool {
	if apiKey == nil || groupID <= 0 || !apiKey.KeywordRouteGroupAccessEnforced {
		return apiKey != nil && groupID > 0
	}
	for _, allowedID := range apiKey.AllowedKeywordRouteGroupIDs {
		if allowedID == groupID {
			return true
		}
	}
	return false
}

// keywordRouteCandidates 只做请求内存中的确定性过滤和排序。账号、余额与订阅
// 仍在调用方逐个复核；拆成纯函数便于证明关键词路由不会越过套餐或能力边界。
func keywordRouteCandidates(apiKey *APIKey, originalGroup *Group, groups []Group, requestedModel, protocol string, body []byte) []Group {
	if apiKey == nil || apiKey.User == nil || originalGroup == nil {
		return nil
	}
	candidates := make([]Group, 0, len(groups))
	for _, candidate := range groups {
		if candidate.ID <= 0 || candidate.ID == originalGroup.ID || !candidate.IsActive() || !candidate.AllowBlockedKeywordRequests {
			continue
		}
		// 订阅型候选的授权以有效订阅为准（由调用方逐个复核，见函数注释），
		// 因此这里不套用 CanBindGroup 的公开分组限制，避免受限用户的有效订阅被误剔除。
		if !GroupTierAllowedForUser(apiKey.User, &candidate) ||
			(!candidate.IsSubscriptionType() && !apiKey.User.CanBindGroup(candidate.ID, candidate.IsExclusive)) ||
			!keywordRouteGroupAllowedForKey(apiKey, candidate.ID) ||
			!keywordRouteProtocolCompatible(protocol, &candidate) ||
			!keywordRouteModelAllowed(&candidate, requestedModel) {
			continue
		}
		if keywordRouteNeedsImagePermission(protocol, requestedModel, body) && !GroupAllowsImageGeneration(&candidate) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftSame := strings.EqualFold(candidates[i].Platform, originalGroup.Platform)
		rightSame := strings.EqualFold(candidates[j].Platform, originalGroup.Platform)
		if leftSame != rightSame {
			return leftSame
		}
		if candidates[i].SortOrder != candidates[j].SortOrder {
			return candidates[i].SortOrder < candidates[j].SortOrder
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) > maxKeywordRouteCandidates {
		candidates = candidates[:maxKeywordRouteCandidates]
	}
	return candidates
}
