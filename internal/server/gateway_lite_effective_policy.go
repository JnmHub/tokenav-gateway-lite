package server

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/platform/liveattestation"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	effectivePolicyCapabilityStandard            = "standard"
	effectivePolicyCapabilityPriority            = "priority"
	effectivePolicyCapabilityLongContext         = "long_context"
	effectivePolicyCapabilityLongContextPriority = "long_context_priority"
	effectivePolicyCapabilityImageGeneration     = "image_generation"
	effectivePolicyCapabilityVideoGeneration     = "video_generation"
	effectivePolicyCapabilityWebSearch           = "web_search"
	effectivePolicyCapabilityLive                = "live"
)

var (
	gatewayLiteLiveNodeEnabled      = gatewaylite.LiveEnabled
	gatewayLiteLiveAttestationCheck = func(ctx context.Context) error {
		return liveattestation.NewProvider().Check(ctx)
	}
)

// buildGatewayLiteEffectivePolicies produces a read-only shadow of the rules
// already used by the gateway. It deliberately runs only during config push;
// user requests never wait for policy serialization or checksum calculation.
func buildGatewayLiteEffectivePolicies(
	gatewayCode string,
	accounts []gatewaylite.GatewayAccountSnapshot,
	groups []gatewaylite.GatewayGroupSnapshot,
	prices []gatewaylite.GatewayModelPrice,
	generatedAtMillis int64,
	compositeRouteSets ...map[int64][]service.CompositeModelRoute,
) ([]gatewaylite.GatewayEffectivePolicy, error) {
	compositeRoutesByGroup := map[int64][]service.CompositeModelRoute{}
	if len(compositeRouteSets) > 0 && compositeRouteSets[0] != nil {
		compositeRoutesByGroup = compositeRouteSets[0]
	}
	priceByModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	priceByPlatformModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	genericPriceByModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	var priceVersion int64
	for _, price := range prices {
		model := strings.ToLower(strings.TrimSpace(price.Model))
		if model == "" {
			continue
		}
		if _, exists := priceByModel[model]; !exists {
			priceByModel[model] = price
		}
		provider := strings.ToLower(strings.TrimSpace(price.Provider))
		if provider == "" {
			genericPriceByModel[model] = price
		} else {
			priceByPlatformModel[provider+"\x00"+model] = price
		}
		if price.UpdatedAtMillis > priceVersion {
			priceVersion = price.UpdatedAtMillis
		}
	}
	var accountVersion int64
	for _, account := range accounts {
		if account.Version > accountVersion {
			accountVersion = account.Version
		}
	}
	var groupVersion int64
	for _, group := range groups {
		if group.Version > groupVersion {
			groupVersion = group.Version
		}
	}
	var compositeRouteVersion int64
	for _, routes := range compositeRoutesByGroup {
		for _, route := range routes {
			if updated := route.UpdatedAt.UnixMilli(); updated > compositeRouteVersion {
				compositeRouteVersion = updated
			}
		}
	}
	revision := maxEffectivePolicyVersion(generatedAtMillis, groupVersion, accountVersion, priceVersion, compositeRouteVersion)
	liveNodeEnabled := gatewayLiteLiveNodeEnabled()
	liveAttestationAvailable := false
	if liveNodeEnabled && gatewayLiteAnyLiveGroupEnabled(groups) {
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		liveAttestationAvailable = gatewayLiteLiveAttestationCheck(checkCtx) == nil
		cancel()
	}

	out := make([]gatewaylite.GatewayEffectivePolicy, 0, len(groups))
	for _, group := range groups {
		groupPrices := prices
		if group.ModelPrices != nil {
			groupPrices = group.ModelPrices
		}
		priceByModel, priceByPlatformModel, genericPriceByModel = gatewayLiteEffectivePolicyPriceIndexes(groupPrices)
		groupPriceVersion := int64(0)
		for _, price := range groupPrices {
			if price.UpdatedAtMillis > groupPriceVersion {
				groupPriceVersion = price.UpdatedAtMillis
			}
		}
		eligibleAccounts := gatewayLitePolicyEligibleAccounts(group, accounts)
		routes := compositeRoutesByGroup[effectivePolicyLocalGroupID(group)]
		groupRouteVersion := int64(0)
		for _, route := range routes {
			if updated := route.UpdatedAt.UnixMilli(); updated > groupRouteVersion {
				groupRouteVersion = updated
			}
		}
		policy := gatewaylite.GatewayEffectivePolicy{
			SchemaVersion:               gatewaylite.EffectivePolicySchemaVersion,
			Revision:                    revision,
			GeneratedAtMillis:           generatedAtMillis,
			Source:                      "gateway-lite",
			GatewayCode:                 strings.TrimSpace(gatewayCode),
			LocalGroupID:                effectivePolicyLocalGroupID(group),
			GroupName:                   group.Name,
			Platform:                    group.Platform,
			MinimumTier:                 group.MinimumTier,
			EffectiveRequiredTier:       service.GroupRequiredTier(&service.Group{MinimumTier: group.MinimumTier, EffectiveRequiredTier: group.EffectiveRequiredTier}),
			AllowBlockedKeywordRequests: group.AllowBlockedKeywordRequests,
			AllowLive:                   group.AllowLive,
			BillingModelSource:          group.BillingModelSource,
			MaxReasoningEffort:          group.MaxReasoningEffort,
			ReasoningEffortMappings:     append([]gatewaylite.ReasoningEffortMapping(nil), group.ReasoningEffortMappings...),
			EligibleAccountCount:        len(eligibleAccounts),
			SourceVersions: gatewaylite.EffectivePolicySourceVersions{
				Groups:          groupVersion,
				Accounts:        accountVersion,
				Prices:          maxEffectivePolicyVersion(priceVersion, groupPriceVersion),
				CompositeRoutes: groupRouteVersion,
			},
		}

		models := normalizeEffectivePolicyModels(group.EffectiveModels)
		policyPriceByModel := make(map[string]gatewaylite.GatewayModelPrice, len(models))
		availableModels := 0
		for _, model := range models {
			targetPlatform, upstreamModel, routeSource := strings.TrimSpace(group.Platform), model, "group"
			modelEligibleAccounts := len(eligibleAccounts)
			if strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformComposite) {
				decision := gatewayLiteCompositePolicyDecision(effectivePolicyLocalGroupID(group), routes, model)
				if decision.Matched {
					targetPlatform, upstreamModel, routeSource = decision.TargetPlatform, decision.UpstreamModel, decision.Source
				} else {
					targetPlatform, upstreamModel, routeSource = "", model, "unknown"
				}
				modelEligibleAccounts = gatewayLitePolicyEligibleAccountCountForPlatform(group, accounts, targetPlatform)
			} else if mappedModel, mapped := service.ResolveGroupModelMapping(group.ModelMapping, model); mapped {
				upstreamModel, routeSource = mappedModel, "group_mapping"
				modelEligibleAccounts = gatewayLitePolicyEligibleAccountCountForModel(group, accounts, targetPlatform, mappedModel)
			}
			priceModel := strings.TrimSpace(upstreamModel)
			if group.ModelPrices != nil {
				// 分组合同已按客户模型展开；策略巡检必须使用同一价格事实源，
				// 但仍保留 upstreamModel 供网关内部路由一致性检查。
				priceModel = strings.TrimSpace(model)
			}
			price, priceFound := gatewayLiteEffectivePolicyPrice(
				priceByPlatformModel,
				genericPriceByModel,
				priceByModel,
				targetPlatform,
				priceModel,
				strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformComposite),
			)
			if priceFound {
				policyPriceByModel[strings.ToLower(strings.TrimSpace(model))] = price
			}
			tierGroup := group
			if targetPlatform != "" {
				tierGroup.Platform = targetPlatform
			}
			if strings.EqualFold(targetPlatform, service.PlatformOpenAI) && strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformComposite) {
				// 聚合分组没有一个笼统的 OpenAI 账号计数；按本模型命中的真实
				// OpenAI 子池计算。分组未开启加价时仍可按标准价处理长上下文。
				tierGroup.OpenAIStandardBillingAccountCount = int64(modelEligibleAccounts)
				tierGroup.OpenAILongContextBillingAccountCount = 0
				tierGroup.OpenAILongContextBillingStatus = service.GroupOpenAILongContextBillingStatusStandardOnly
			}
			modelPolicy := gatewayLiteEffectiveModelPolicy(tierGroup, model, price, priceFound, modelEligibleAccounts)
			modelPolicy.TargetPlatform = targetPlatform
			modelPolicy.UpstreamModel = upstreamModel
			modelPolicy.PriceModel = priceModel
			modelPolicy.PriceSource = price.Source
			modelPolicy.RouteSource = routeSource
			modelPolicy.EligibleAccountCount = modelEligibleAccounts
			if modelPolicy.State == gatewaylite.EffectivePolicyStateAvailable {
				availableModels++
			}
			policy.Models = append(policy.Models, modelPolicy)
		}
		policy.Capabilities = gatewayLiteEffectiveCapabilities(group, policy.Models, len(eligibleAccounts), policyPriceByModel)
		if capability, ok := gatewayLiteEffectiveLiveCapability(group, eligibleAccounts, liveNodeEnabled, liveAttestationAvailable); ok {
			policy.Capabilities = append(policy.Capabilities, capability)
		}
		policy.State, policy.ReasonCode = gatewayLiteEffectiveGroupState(group, len(eligibleAccounts), len(models), availableModels)

		finalized, err := gatewaylite.FinalizeEffectivePolicy(policy)
		if err != nil {
			return nil, err
		}
		out = append(out, finalized)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalGroupID < out[j].LocalGroupID })
	return out, nil
}

func gatewayLiteEffectivePolicyPriceIndexes(prices []gatewaylite.GatewayModelPrice) (
	map[string]gatewaylite.GatewayModelPrice,
	map[string]gatewaylite.GatewayModelPrice,
	map[string]gatewaylite.GatewayModelPrice,
) {
	anyByModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	byPlatformModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	genericByModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	for _, price := range prices {
		model := strings.ToLower(strings.TrimSpace(price.Model))
		if model == "" {
			continue
		}
		if _, exists := anyByModel[model]; !exists {
			anyByModel[model] = price
		}
		provider := normalizeGatewayLitePublicPricingPlatform(price.Provider, price.Provider)
		if provider == "" {
			genericByModel[model] = price
		} else {
			byPlatformModel[provider+"\x00"+model] = price
		}
	}
	return anyByModel, byPlatformModel, genericByModel
}

func gatewayLiteAnyLiveGroupEnabled(groups []gatewaylite.GatewayGroupSnapshot) bool {
	for _, group := range groups {
		if group.AllowLive && strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformOpenAI) {
			return true
		}
	}
	return false
}

func gatewayLiteEffectiveLiveCapability(
	group gatewaylite.GatewayGroupSnapshot,
	eligibleAccounts []gatewaylite.GatewayAccountSnapshot,
	nodeEnabled bool,
	attestationAvailable bool,
) (gatewaylite.EffectivePolicyCapability, bool) {
	// 总开关关闭时完全省略字段，让旧节点与默认生产配置保持同一份逻辑
	// checksum（校验和），主站也不会给用户展示尚未开放的实验能力。
	if !nodeEnabled || !strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformOpenAI) {
		return gatewaylite.EffectivePolicyCapability{}, false
	}
	capability := gatewaylite.EffectivePolicyCapability{ID: effectivePolicyCapabilityLive}
	if !group.AllowLive {
		capability.State = gatewaylite.EffectivePolicyStateDisabled
		capability.ReasonCode = "live_disabled"
		return capability, true
	}

	liveAccounts := 0
	for _, snapshot := range eligibleAccounts {
		account := &service.Account{
			Platform:    snapshot.Platform,
			Type:        snapshot.Type,
			Credentials: snapshot.Credentials,
			Extra:       snapshot.Extra,
		}
		if account.SupportsOpenAIEndpointCapability(service.OpenAIEndpointCapabilityLive) {
			liveAccounts++
		}
	}
	capability.EligibleAccountCount = liveAccounts
	if liveAccounts == 0 {
		capability.State = gatewaylite.EffectivePolicyStateUnavailable
		capability.ReasonCode = "no_live_oauth_accounts"
		return capability, true
	}
	if !attestationAvailable {
		capability.State = gatewaylite.EffectivePolicyStateUnavailable
		capability.ReasonCode = "device_attestation_unavailable"
		return capability, true
	}
	capability.State = gatewaylite.EffectivePolicyStateAvailable
	capability.ReasonCode = "available"
	return capability, true
}

// gatewayLiteEffectivePolicyPrice 在聚合分组中必须按“真实平台 + 真实模型”取价。
// provider（供应商）为空的旧价格仍可作为通用价格使用，但绝不能拿另一个明确
// 平台的同名价格兜底；普通分组保留历史兼容回退。
func gatewayLiteEffectivePolicyPrice(
	byPlatformModel map[string]gatewaylite.GatewayModelPrice,
	genericByModel map[string]gatewaylite.GatewayModelPrice,
	anyByModel map[string]gatewaylite.GatewayModelPrice,
	platform, model string,
	strictPlatform bool,
) (gatewaylite.GatewayModelPrice, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	platform = strings.ToLower(strings.TrimSpace(platform))
	if model == "" {
		return gatewaylite.GatewayModelPrice{}, false
	}
	if platform != "" {
		if price, ok := byPlatformModel[platform+"\x00"+model]; ok {
			return price, true
		}
	}
	if price, ok := genericByModel[model]; ok {
		return price, true
	}
	if strictPlatform {
		return gatewaylite.GatewayModelPrice{}, false
	}
	price, ok := anyByModel[model]
	return price, ok
}

func gatewayLiteCompositePolicyDecision(groupID int64, routes []service.CompositeModelRoute, model string) service.CompositeRouteDecision {
	var detected service.CompositeRouteDecision
	for _, endpoint := range []string{
		service.CompositeRouteEndpointAny,
		service.CompositeRouteEndpointResponses,
		service.CompositeRouteEndpointMessages,
		service.CompositeRouteEndpointChatCompletions,
		service.CompositeRouteEndpointEmbeddings,
		service.CompositeRouteEndpointImages,
		service.CompositeRouteEndpointGemini,
	} {
		decision := service.ResolveCompositeRouteFromRoutes(groupID, routes, model, endpoint)
		if decision.Matched && decision.Source == service.CompositeRouteSourceExplicit {
			return decision
		}
		if !detected.Matched && decision.Matched {
			detected = decision
		}
	}
	return detected
}

func gatewayLiteEffectiveModelPolicy(
	group gatewaylite.GatewayGroupSnapshot,
	model string,
	price gatewaylite.GatewayModelPrice,
	priceFound bool,
	eligibleAccounts int,
) gatewaylite.EffectivePolicyModel {
	standardState, standardReason := gatewayLiteStandardTierState(group, price, priceFound, eligibleAccounts)
	result := gatewaylite.EffectivePolicyModel{
		Model:                model,
		EligibleAccountCount: eligibleAccounts,
		BillingMode:          gatewayLiteEffectiveBillingMode(group, price),
		State:                standardState,
		ReasonCode:           standardReason,
		Tiers: []gatewaylite.EffectivePolicyCapability{{
			ID:                   effectivePolicyCapabilityStandard,
			State:                standardState,
			ReasonCode:           standardReason,
			EligibleAccountCount: eligibleAccounts,
			PriceComplete:        priceFound && !price.Disabled,
			Executor:             "gateway",
		}},
	}
	if result.BillingMode == "" {
		result.BillingMode = "token"
	}

	priorityState, priorityReason, priorityComplete := gatewayLitePriorityTierState(group, price, priceFound, eligibleAccounts)
	result.Tiers = append(result.Tiers, gatewaylite.EffectivePolicyCapability{
		ID: effectivePolicyCapabilityPriority, State: priorityState, ReasonCode: priorityReason,
		EligibleAccountCount: eligibleAccounts, PriceComplete: priorityComplete, Executor: "gateway",
	})
	longState, longReason, longComplete := gatewayLiteLongContextTierState(group, price, priceFound, eligibleAccounts)
	result.Tiers = append(result.Tiers, gatewaylite.EffectivePolicyCapability{
		ID: effectivePolicyCapabilityLongContext, State: longState, ReasonCode: longReason,
		EligibleAccountCount: int(group.OpenAILongContextBillingAccountCount), PriceComplete: longComplete, Executor: "gateway",
	})
	longPriorityState, longPriorityReason, longPriorityComplete := gatewayLiteLongContextPriorityTierState(
		group, price, priceFound, eligibleAccounts, priorityState, longState,
	)
	result.Tiers = append(result.Tiers, gatewaylite.EffectivePolicyCapability{
		ID: effectivePolicyCapabilityLongContextPriority, State: longPriorityState, ReasonCode: longPriorityReason,
		EligibleAccountCount: int(group.OpenAILongContextBillingAccountCount), PriceComplete: longPriorityComplete, Executor: "gateway",
	})
	return result
}

func gatewayLiteStandardTierState(group gatewaylite.GatewayGroupSnapshot, price gatewaylite.GatewayModelPrice, found bool, eligible int) (string, string) {
	if !strings.EqualFold(strings.TrimSpace(group.Status), service.StatusActive) {
		return gatewaylite.EffectivePolicyStateDisabled, "group_disabled"
	}
	if eligible == 0 {
		return gatewaylite.EffectivePolicyStateUnavailable, "no_eligible_accounts"
	}
	if !found {
		return gatewaylite.EffectivePolicyStateUnavailable, "price_missing"
	}
	if price.Disabled {
		return gatewaylite.EffectivePolicyStateDisabled, "price_disabled"
	}
	return gatewaylite.EffectivePolicyStateAvailable, "available"
}

func gatewayLitePriorityTierState(group gatewaylite.GatewayGroupSnapshot, price gatewaylite.GatewayModelPrice, found bool, eligible int) (string, string, bool) {
	standardState, standardReason := gatewayLiteStandardTierState(group, price, found, eligible)
	if standardState != gatewaylite.EffectivePolicyStateAvailable {
		return standardState, standardReason, false
	}
	if !strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformOpenAI) || !price.SupportsServiceTier {
		return gatewaylite.EffectivePolicyStateUnsupported, "model_priority_unsupported", false
	}
	complete := effectivePolicyPriorityPriceComplete(price)
	if !complete {
		return gatewaylite.EffectivePolicyStateUnavailable, "priority_price_incomplete", false
	}
	return gatewaylite.EffectivePolicyStateAvailable, "available", true
}

func gatewayLiteLongContextTierState(group gatewaylite.GatewayGroupSnapshot, price gatewaylite.GatewayModelPrice, found bool, eligible int) (string, string, bool) {
	standardState, standardReason := gatewayLiteStandardTierState(group, price, found, eligible)
	if standardState != gatewaylite.EffectivePolicyStateAvailable {
		return standardState, standardReason, false
	}
	if !strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformOpenAI) || price.LongContextInputTokenThreshold <= 0 {
		return gatewaylite.EffectivePolicyStateUnsupported, "long_context_not_applicable", false
	}
	status := strings.ToLower(strings.TrimSpace(group.OpenAILongContextBillingStatus))
	if status == "" {
		if group.AllowOpenAILongContextBilling && group.OpenAILongContextBillingAccountCount > 0 {
			status = service.GroupOpenAILongContextBillingStatusEnabled
		} else if group.OpenAIStandardBillingAccountCount+group.OpenAILongContextBillingAccountCount > 0 {
			status = service.GroupOpenAILongContextBillingStatusStandardOnly
		} else {
			status = service.GroupOpenAILongContextBillingStatusUnavailable
		}
	}
	switch status {
	case service.GroupOpenAILongContextBillingStatusEnabled:
		if !group.AllowOpenAILongContextBilling || group.OpenAILongContextBillingAccountCount <= 0 {
			return gatewaylite.EffectivePolicyStateUnknown, "long_context_snapshot_inconsistent", false
		}
		if !effectivePolicyLongContextPriceComplete(price) {
			return gatewaylite.EffectivePolicyStateUnavailable, "long_context_price_incomplete", false
		}
		return gatewaylite.EffectivePolicyStateAvailable, "available", true
	case service.GroupOpenAILongContextBillingStatusStandardOnly:
		return gatewaylite.EffectivePolicyStateAvailable, "uses_standard_price", true
	case service.GroupOpenAILongContextBillingStatusUnavailable:
		return gatewaylite.EffectivePolicyStateUnavailable, "no_long_context_accounts", false
	default:
		return gatewaylite.EffectivePolicyStateUnknown, "long_context_state_unknown", false
	}
}

func gatewayLiteLongContextPriorityTierState(
	group gatewaylite.GatewayGroupSnapshot,
	price gatewaylite.GatewayModelPrice,
	found bool,
	eligible int,
	priorityState string,
	longState string,
) (string, string, bool) {
	if priorityState == gatewaylite.EffectivePolicyStateUnsupported || longState == gatewaylite.EffectivePolicyStateUnsupported {
		return gatewaylite.EffectivePolicyStateUnsupported, "long_context_priority_not_applicable", false
	}
	if priorityState != gatewaylite.EffectivePolicyStateAvailable {
		return priorityState, "priority_unavailable", false
	}
	if longState != gatewaylite.EffectivePolicyStateAvailable {
		return longState, "long_context_unavailable", false
	}
	if strings.EqualFold(strings.TrimSpace(group.OpenAILongContextBillingStatus), service.GroupOpenAILongContextBillingStatusStandardOnly) {
		return gatewaylite.EffectivePolicyStateAvailable, "uses_base_priority_price", true
	}
	complete := found && eligible > 0 && effectivePolicyLongContextPriorityPriceComplete(price)
	if !complete {
		return gatewaylite.EffectivePolicyStateUnavailable, "long_context_priority_price_incomplete", false
	}
	return gatewaylite.EffectivePolicyStateAvailable, "available", true
}

func gatewayLiteEffectiveCapabilities(
	group gatewaylite.GatewayGroupSnapshot,
	models []gatewaylite.EffectivePolicyModel,
	eligible int,
	priceByModel map[string]gatewaylite.GatewayModelPrice,
) []gatewaylite.EffectivePolicyCapability {
	ids := []string{
		effectivePolicyCapabilityStandard,
		effectivePolicyCapabilityPriority,
		effectivePolicyCapabilityLongContext,
		effectivePolicyCapabilityLongContextPriority,
	}
	out := make([]gatewaylite.EffectivePolicyCapability, 0, len(ids)+2)
	for _, id := range ids {
		out = append(out, aggregateEffectivePolicyCapability(id, models, eligible))
	}
	imageModels := gatewayLiteAvailableMediaModelCount(models, priceByModel, "image")
	imageState, imageReason := gatewayLiteOptionalCapabilityState(group, imageModels, eligible, group.AllowImageGeneration, "image_generation_disabled")
	out = append(out, gatewaylite.EffectivePolicyCapability{
		ID: effectivePolicyCapabilityImageGeneration, State: imageState, ReasonCode: imageReason,
		EligibleAccountCount: eligible, PriceComplete: imageState == gatewaylite.EffectivePolicyStateAvailable, Executor: "gateway",
	})
	videoModels := gatewayLiteAvailableMediaModelCount(models, priceByModel, "video")
	videoState, videoReason := gatewayLiteOptionalCapabilityState(group, videoModels, eligible, group.AllowImageGeneration, "video_generation_disabled")
	out = append(out, gatewaylite.EffectivePolicyCapability{
		ID: effectivePolicyCapabilityVideoGeneration, State: videoState, ReasonCode: videoReason,
		EligibleAccountCount: eligible, PriceComplete: videoState == gatewaylite.EffectivePolicyStateAvailable, Executor: "gateway",
	})
	searchModels := 0
	if strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformOpenAI) {
		for _, model := range models {
			if model.State == gatewaylite.EffectivePolicyStateAvailable {
				searchModels++
			}
		}
	}
	searchState, searchReason := gatewayLiteOptionalCapabilityState(group, searchModels, eligible, true, "web_search_disabled")
	if !strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformOpenAI) {
		searchState, searchReason = gatewaylite.EffectivePolicyStateUnsupported, "web_search_not_applicable"
	}
	out = append(out, gatewaylite.EffectivePolicyCapability{
		ID: effectivePolicyCapabilityWebSearch, State: searchState, ReasonCode: searchReason,
		EligibleAccountCount: eligible, PriceComplete: searchState == gatewaylite.EffectivePolicyStateAvailable, Executor: "gateway",
	})
	return out
}

func aggregateEffectivePolicyCapability(id string, models []gatewaylite.EffectivePolicyModel, eligible int) gatewaylite.EffectivePolicyCapability {
	result := gatewaylite.EffectivePolicyCapability{
		ID: id, State: gatewaylite.EffectivePolicyStateUnsupported, ReasonCode: "not_applicable",
		EligibleAccountCount: eligible, Executor: "gateway",
	}
	for _, model := range models {
		for _, tier := range model.Tiers {
			if tier.ID != id {
				continue
			}
			if tier.State == gatewaylite.EffectivePolicyStateAvailable {
				result.State, result.ReasonCode, result.PriceComplete = tier.State, tier.ReasonCode, tier.PriceComplete
				return result
			}
			if result.State == gatewaylite.EffectivePolicyStateUnsupported || tier.State == gatewaylite.EffectivePolicyStateUnavailable || tier.State == gatewaylite.EffectivePolicyStateDisabled {
				result.State, result.ReasonCode, result.PriceComplete = tier.State, tier.ReasonCode, tier.PriceComplete
			}
		}
	}
	return result
}

func gatewayLiteOptionalCapabilityState(group gatewaylite.GatewayGroupSnapshot, matchingModels, eligible int, enabled bool, disabledReason string) (string, string) {
	if !strings.EqualFold(strings.TrimSpace(group.Status), service.StatusActive) {
		return gatewaylite.EffectivePolicyStateDisabled, "group_disabled"
	}
	if !enabled {
		return gatewaylite.EffectivePolicyStateDisabled, disabledReason
	}
	if eligible == 0 || matchingModels == 0 {
		return gatewaylite.EffectivePolicyStateUnavailable, "no_eligible_route"
	}
	return gatewaylite.EffectivePolicyStateAvailable, "available"
}

func gatewayLiteAvailableMediaModelCount(
	models []gatewaylite.EffectivePolicyModel,
	priceByModel map[string]gatewaylite.GatewayModelPrice,
	media string,
) int {
	count := 0
	for _, model := range models {
		if model.State != gatewaylite.EffectivePolicyStateAvailable {
			continue
		}
		price, found := priceByModel[strings.ToLower(strings.TrimSpace(model.Model))]
		if !found {
			continue
		}
		if media == "image" && gatewayLiteEffectivePriceUsesImage(price) {
			count++
		}
		if media == "video" && gatewayLiteEffectivePriceUsesVideo(price) {
			count++
		}
	}
	return count
}

func gatewayLiteEffectiveBillingMode(group gatewaylite.GatewayGroupSnapshot, price gatewaylite.GatewayModelPrice) string {
	if group.AllowImageGeneration && group.ImagePrice1K != nil && group.ImagePrice2K != nil && group.ImagePrice4K != nil && gatewayLiteEffectivePriceUsesImage(price) {
		return "image"
	}
	mode := strings.ToLower(strings.TrimSpace(price.BillingMode))
	switch mode {
	case "token", "per_request", "image", "video":
		return mode
	}
	if gatewayLiteEffectivePriceUsesVideo(price) {
		return "video"
	}
	if gatewayLiteEffectivePriceUsesImage(price) {
		return "image"
	}
	return "token"
}

func gatewayLiteEffectivePriceUsesImage(price gatewaylite.GatewayModelPrice) bool {
	mode := strings.ToLower(strings.TrimSpace(price.BillingMode))
	return mode == "image" || mode == "per_request" || price.OutputCostPerImage > 0 ||
		price.OutputCostPerImageToken > 0 || price.PerRequestPrice > 0 ||
		strings.Contains(strings.ToLower(strings.TrimSpace(price.Mode)), "image")
}

func gatewayLiteEffectivePriceUsesVideo(price gatewaylite.GatewayModelPrice) bool {
	return strings.EqualFold(strings.TrimSpace(price.BillingMode), "video") ||
		price.VideoPricePerSecond480P > 0 || price.VideoPricePerSecond720P > 0 || price.VideoPricePerSecond1080P > 0 ||
		strings.Contains(strings.ToLower(strings.TrimSpace(price.Mode)), "video")
}

func gatewayLiteEffectiveGroupState(group gatewaylite.GatewayGroupSnapshot, eligible, modelCount, availableModels int) (string, string) {
	if !strings.EqualFold(strings.TrimSpace(group.Status), service.StatusActive) {
		return gatewaylite.EffectivePolicyStateDisabled, "group_disabled"
	}
	if eligible == 0 {
		return gatewaylite.EffectivePolicyStateUnavailable, "no_eligible_accounts"
	}
	if modelCount == 0 {
		return gatewaylite.EffectivePolicyStateUnavailable, "no_effective_models"
	}
	if availableModels == 0 {
		return gatewaylite.EffectivePolicyStateUnavailable, "no_priced_models"
	}
	return gatewaylite.EffectivePolicyStateAvailable, "available"
}

func gatewayLitePolicyEligibleAccounts(group gatewaylite.GatewayGroupSnapshot, accounts []gatewaylite.GatewayAccountSnapshot) []gatewaylite.GatewayAccountSnapshot {
	localGroupID := effectivePolicyLocalGroupID(group)
	out := make([]gatewaylite.GatewayAccountSnapshot, 0)
	for _, account := range accounts {
		if !strings.EqualFold(strings.TrimSpace(account.Status), service.StatusActive) || !account.Schedulable {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(group.Platform), service.PlatformComposite) {
			if !gatewayLiteConcretePolicyPlatform(account.Platform) {
				continue
			}
		} else if strings.TrimSpace(group.Platform) != "" && !strings.EqualFold(strings.TrimSpace(account.Platform), strings.TrimSpace(group.Platform)) {
			continue
		}
		if !effectivePolicyContainsInt64(account.GroupIDs, localGroupID) {
			continue
		}
		if !gatewayLitePolicyAccountMeetsGroupRequirements(group, account) {
			continue
		}
		out = append(out, account)
	}
	return out
}

func gatewayLitePolicyEligibleAccountCountForPlatform(group gatewaylite.GatewayGroupSnapshot, accounts []gatewaylite.GatewayAccountSnapshot, platform string) int {
	if !gatewayLiteConcretePolicyPlatform(platform) {
		return 0
	}
	count := 0
	localGroupID := effectivePolicyLocalGroupID(group)
	for _, account := range accounts {
		if strings.EqualFold(strings.TrimSpace(account.Platform), strings.TrimSpace(platform)) &&
			strings.EqualFold(strings.TrimSpace(account.Status), service.StatusActive) && account.Schedulable &&
			effectivePolicyContainsInt64(account.GroupIDs, localGroupID) &&
			gatewayLitePolicyAccountMeetsGroupRequirements(group, account) {
			count++
		}
	}
	return count
}

func gatewayLitePolicyEligibleAccountCountForModel(group gatewaylite.GatewayGroupSnapshot, accounts []gatewaylite.GatewayAccountSnapshot, platform, model string) int {
	if !gatewayLiteConcretePolicyPlatform(platform) || strings.TrimSpace(model) == "" {
		return 0
	}
	count := 0
	localGroupID := effectivePolicyLocalGroupID(group)
	for _, account := range accounts {
		if !strings.EqualFold(strings.TrimSpace(account.Platform), strings.TrimSpace(platform)) ||
			!strings.EqualFold(strings.TrimSpace(account.Status), service.StatusActive) || !account.Schedulable ||
			!effectivePolicyContainsInt64(account.GroupIDs, localGroupID) ||
			!gatewayLitePolicyAccountMeetsGroupRequirements(group, account) {
			continue
		}
		runtimeAccount := service.Account{Platform: account.Platform, Credentials: account.Credentials}
		if runtimeAccount.IsModelSupported(model) {
			count++
		}
	}
	return count
}

func gatewayLitePolicyAccountMeetsGroupRequirements(group gatewaylite.GatewayGroupSnapshot, account gatewaylite.GatewayAccountSnapshot) bool {
	if !group.RequireOAuthOnly && !group.RequirePrivacySet {
		return true
	}
	// effective policy（最终生效策略）必须复用真实调度的账号资格口径，
	// 不能仅按账号状态计数，否则主站会展示实际无法调度的模型能力。
	runtimeAccount := service.Account{Platform: account.Platform, Type: account.Type, Extra: account.Extra}
	if group.RequireOAuthOnly && !runtimeAccount.IsOAuth() {
		return false
	}
	if group.RequirePrivacySet && !runtimeAccount.IsPrivacySet() {
		return false
	}
	return true
}

func gatewayLiteConcretePolicyPlatform(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case service.PlatformAnthropic, service.PlatformOpenAI, service.PlatformGemini, service.PlatformAntigravity,
		service.PlatformGrok, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek:
		return true
	default:
		return false
	}
}

func normalizeEffectivePolicyModels(models []string) []string {
	seen := make(map[string]string, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || model == "*" {
			continue
		}
		seen[strings.ToLower(model)] = model
	}
	out := make([]string, 0, len(seen))
	for _, model := range seen {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func effectivePolicyPriorityPriceComplete(price gatewaylite.GatewayModelPrice) bool {
	if !price.SupportsServiceTier || price.InputCostPer1MTokensPriority <= 0 || price.OutputCostPer1MTokensPriority <= 0 {
		return false
	}
	if price.CacheReadCostPer1MTokens > 0 && price.CacheReadCostPer1MTokensPriority <= 0 {
		return false
	}
	return price.CacheCreationCostPer1MTokens <= 0 || price.CacheCreationCostPer1MTokensPriority > 0
}

func effectivePolicyLongContextPriceComplete(price gatewaylite.GatewayModelPrice) bool {
	if price.LongContextInputTokenThreshold <= 0 || price.InputCostPer1MTokensAbove272K <= 0 || price.OutputCostPer1MTokensAbove272K <= 0 {
		return false
	}
	if price.CacheReadCostPer1MTokens > 0 && price.CacheReadCostPer1MTokensAbove272K <= 0 {
		return false
	}
	return price.CacheCreationCostPer1MTokens <= 0 || price.CacheCreationCostPer1MTokensAbove272K > 0
}

func effectivePolicyLongContextPriorityPriceComplete(price gatewaylite.GatewayModelPrice) bool {
	if price.InputCostPer1MTokensPriorityAbove272K <= 0 || price.OutputCostPer1MTokensPriorityAbove272K <= 0 {
		return false
	}
	if price.CacheReadCostPer1MTokensAbove272K > 0 && price.CacheReadCostPer1MTokensPriorityAbove272K <= 0 {
		return false
	}
	return price.CacheCreationCostPer1MTokensAbove272K <= 0 || price.CacheCreationCostPer1MTokensPriorityAbove272K > 0
}

func effectivePolicyLocalGroupID(group gatewaylite.GatewayGroupSnapshot) int64 {
	if group.LocalGroupID > 0 {
		return group.LocalGroupID
	}
	return group.ID
}

func effectivePolicyContainsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func maxEffectivePolicyVersion(values ...int64) int64 {
	var out int64
	for _, value := range values {
		if value > out {
			out = value
		}
	}
	return out
}
