package server

import (
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// buildGatewayLiteGroupModelPrices 将节点目录价与分组自定义价合并成客户可见模型合同。
// 该合同只在配置推送的冷路径生成；模型名始终使用客户请求名，不能暴露映射后的模型。
func buildGatewayLiteGroupModelPrices(
	group gatewaylite.GatewayGroupSnapshot,
	globalPrices []gatewaylite.GatewayModelPrice,
	compositeRoutes []service.CompositeModelRoute,
	pricingChannel *service.Channel,
	accounts []gatewaylite.GatewayAccountSnapshot,
) []gatewaylite.GatewayModelPrice {
	models := normalizeEffectivePolicyModels(group.EffectiveModels)
	if len(models) == 0 {
		return []gatewaylite.GatewayModelPrice{}
	}
	byPlatformModel, genericByModel, anyByModel := gatewayLiteModelPriceIndexes(globalPrices)
	billingSource := gatewayLiteGroupBillingModelSource(group.BillingModelSource)
	pricingVersion := gatewayLiteGroupPricingUpdatedAtMillis(pricingChannel)
	out := make([]gatewaylite.GatewayModelPrice, 0, len(models))
	for _, publicModel := range models {
		platform, catalogModel := strings.TrimSpace(group.Platform), publicModel
		strictPlatform := strings.EqualFold(platform, service.PlatformComposite)
		if strictPlatform {
			decision, unambiguous := gatewayLiteCompositeContractDecision(effectivePolicyLocalGroupID(group), compositeRoutes, publicModel)
			if !unambiguous {
				// 一个公开模型若按不同 endpoint（接口）命中不同上游，
				// 单一的分组合同无法准确表达价格；宁可不展示，避免误导用户。
				continue
			}
			if decision.Matched {
				platform, catalogModel = decision.TargetPlatform, decision.UpstreamModel
			} else {
				platform = ""
			}
		} else if mapped, ok := service.ResolveGroupModelMapping(group.ModelMapping, publicModel); ok &&
			billingSource != service.BillingModelSourceRequested {
			catalogModel = mapped
		}
		if billingSource == service.BillingModelSourceUpstream || billingSource == service.BillingModelSourceResponse {
			accountModel, unambiguous := gatewayLiteUniqueAccountMappedModel(group, accounts, platform, catalogModel)
			if !unambiguous {
				// 实际上游和响应模型计费会落到具体账号的模型映射。多个可调度
				// 账号给出不同目标时，单一公开合同无法准确表达价格，
				// 因此失败关闭而不是任选一个账号的价格。
				continue
			}
			catalogModel = accountModel
		}

		price, found := gatewayLiteEffectivePolicyPrice(
			byPlatformModel, genericByModel, anyByModel, platform, catalogModel, strictPlatform,
		)
		pricingModel := catalogModel
		if billingSource == service.BillingModelSourceRequested {
			pricingModel = publicModel
		}
		customApplied := false
		if custom := gatewayLiteGroupCustomPricing(pricingChannel, platform, pricingModel); custom != nil {
			price = applyGatewayLiteGroupPricingOverride(price, found, *custom)
			found = true
			customApplied = true
		}
		if !found {
			continue
		}
		price.GatewayNodeID = 0
		price.GatewayCode = ""
		price.Model = publicModel
		price.Provider = normalizeGatewayLitePublicPricingPlatform(platform, price.Provider)
		price.Source = gatewayLiteGroupPricingSource(price.Source, customApplied)
		if pricingVersion > price.UpdatedAtMillis {
			price.UpdatedAtMillis = pricingVersion
		}
		out = append(out, price)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(strings.TrimSpace(out[i].Model)) < strings.ToLower(strings.TrimSpace(out[j].Model))
	})
	return out
}

func gatewayLiteGroupRestrictedModels(
	group gatewaylite.GatewayGroupSnapshot,
	models []string,
	globalPrices []gatewaylite.GatewayModelPrice,
	compositeRoutes []service.CompositeModelRoute,
	channel *service.Channel,
	accounts []gatewaylite.GatewayAccountSnapshot,
) []string {
	group.EffectiveModels = append([]string(nil), models...)
	prices := buildGatewayLiteGroupModelPrices(group, globalPrices, compositeRoutes, channel, accounts)
	out := make([]string, 0, len(prices))
	for _, price := range prices {
		if price.Disabled {
			continue
		}
		out = append(out, price.Model)
	}
	return out
}

// gatewayLiteUniqueAccountMappedModel returns the only account-level upstream
// model reachable by this public contract. The snapshot is generated off the
// request path, so doing the full eligible-account comparison here adds no hot
// path overhead and prevents publishing a price that depends on random routing.
func gatewayLiteUniqueAccountMappedModel(
	group gatewaylite.GatewayGroupSnapshot,
	accounts []gatewaylite.GatewayAccountSnapshot,
	platform string,
	model string,
) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	var selected string
	found := false
	for _, account := range gatewayLitePolicyEligibleAccounts(group, accounts) {
		if platform != "" && !strings.EqualFold(strings.TrimSpace(account.Platform), strings.TrimSpace(platform)) {
			continue
		}
		runtimeAccount := service.Account{
			ID:          account.ID,
			Platform:    account.Platform,
			Type:        account.Type,
			Status:      account.Status,
			Schedulable: account.Schedulable,
			Credentials: account.Credentials,
			Extra:       account.Extra,
			GroupIDs:    cloneGatewayLiteInt64Slice(account.GroupIDs),
		}
		if !runtimeAccount.IsModelSupported(model) {
			continue
		}
		mappedModel := strings.TrimSpace(runtimeAccount.GetMappedModel(model))
		if mappedModel == "" {
			continue
		}
		if !found {
			selected, found = mappedModel, true
			continue
		}
		if !strings.EqualFold(selected, mappedModel) {
			return "", false
		}
	}
	return selected, found
}

func gatewayLiteGroupPricingUpdatedAtMillis(channel *service.Channel) int64 {
	if channel == nil {
		return 0
	}
	version := int64(0)
	if !channel.UpdatedAt.IsZero() {
		version = channel.UpdatedAt.UnixMilli()
	}
	for _, pricing := range channel.ModelPricing {
		if !pricing.UpdatedAt.IsZero() && pricing.UpdatedAt.UnixMilli() > version {
			version = pricing.UpdatedAt.UnixMilli()
		}
		for _, interval := range pricing.Intervals {
			if !interval.UpdatedAt.IsZero() && interval.UpdatedAt.UnixMilli() > version {
				version = interval.UpdatedAt.UnixMilli()
			}
		}
	}
	return version
}

func gatewayLiteGroupBillingModelSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		// 新版分组默认按客户请求模型计费；旧快照缺少字段时也按该默认值解释。
		return service.BillingModelSourceRequested
	}
	return source
}

func gatewayLiteModelPriceIndexes(prices []gatewaylite.GatewayModelPrice) (
	map[string]gatewaylite.GatewayModelPrice,
	map[string]gatewaylite.GatewayModelPrice,
	map[string]gatewaylite.GatewayModelPrice,
) {
	byPlatformModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	genericByModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	anyByModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
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
	return byPlatformModel, genericByModel, anyByModel
}

func gatewayLiteGroupCustomPricing(channel *service.Channel, platform, model string) *service.ChannelModelPricing {
	if channel == nil || !channel.IsActive() {
		return nil
	}
	return gatewayLiteChannelPricingForPlatform(channel.ModelPricing, platform, model)
}

func gatewayLiteChannelPricingForPlatform(prices []service.ChannelModelPricing, platform, model string) *service.ChannelModelPricing {
	platform = normalizeGatewayLitePublicPricingPlatform(platform, platform)
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	var wildcard *service.ChannelModelPricing
	for index := range prices {
		price := &prices[index]
		pricePlatform := normalizeGatewayLitePublicPricingPlatform(price.Platform, price.Platform)
		if platform != "" && pricePlatform != platform {
			continue
		}
		for _, pattern := range price.Models {
			pattern = strings.ToLower(strings.TrimSpace(pattern))
			if pattern == model {
				copyPrice := price.Clone()
				return &copyPrice
			}
			if wildcard == nil && strings.HasSuffix(pattern, "*") && strings.HasPrefix(model, strings.TrimSuffix(pattern, "*")) {
				copyPrice := price.Clone()
				wildcard = &copyPrice
			}
		}
	}
	return wildcard
}

func applyGatewayLiteGroupPricingOverride(
	base gatewaylite.GatewayModelPrice,
	baseFound bool,
	override service.ChannelModelPricing,
) gatewaylite.GatewayModelPrice {
	if !baseFound {
		base = gatewaylite.GatewayModelPrice{Currency: "USD", BillingMode: "token"}
	}
	catalogBase := base
	mode := strings.ToLower(strings.TrimSpace(string(override.BillingMode)))
	if mode == "" {
		mode = "token"
	}
	base.BillingMode = mode
	base.Intervals = gatewayLiteGroupPricingIntervals(override.Intervals, mode)
	if mode != "token" {
		clearGatewayLiteGroupTokenPrices(&base)
		base.PerRequestPrice = 0
		base.OutputCostPerImage = 0
		base.VideoPricePerSecond480P = 0
		base.VideoPricePerSecond720P = 0
		base.VideoPricePerSecond1080P = 0
	}
	if override.InputPrice != nil {
		base.InputCostPer1MTokens = perTokenToPerMillion(*override.InputPrice)
		base.InputCostPer1MTokensPriority = base.InputCostPer1MTokens
		applyGatewayLiteLongContextOverride(&base.InputCostPer1MTokensAbove272K, catalogBase.InputCostPer1MTokensAbove272K, catalogBase.InputCostPer1MTokens, base.InputCostPer1MTokens)
		applyGatewayLiteLongContextOverride(&base.InputCostPer1MTokensPriorityAbove272K, catalogBase.InputCostPer1MTokensPriorityAbove272K, catalogBase.InputCostPer1MTokens, base.InputCostPer1MTokens)
	}
	if override.OutputPrice != nil {
		base.OutputCostPer1MTokens = perTokenToPerMillion(*override.OutputPrice)
		base.OutputCostPer1MTokensPriority = base.OutputCostPer1MTokens
		applyGatewayLiteLongContextOverride(&base.OutputCostPer1MTokensAbove272K, catalogBase.OutputCostPer1MTokensAbove272K, catalogBase.OutputCostPer1MTokens, base.OutputCostPer1MTokens)
		applyGatewayLiteLongContextOverride(&base.OutputCostPer1MTokensPriorityAbove272K, catalogBase.OutputCostPer1MTokensPriorityAbove272K, catalogBase.OutputCostPer1MTokens, base.OutputCostPer1MTokens)
	}
	if override.CacheReadPrice != nil {
		base.CacheReadCostPer1MTokens = perTokenToPerMillion(*override.CacheReadPrice)
		base.CacheReadCostPer1MTokensPriority = base.CacheReadCostPer1MTokens
		applyGatewayLiteLongContextOverride(&base.CacheReadCostPer1MTokensAbove272K, catalogBase.CacheReadCostPer1MTokensAbove272K, catalogBase.CacheReadCostPer1MTokens, base.CacheReadCostPer1MTokens)
		applyGatewayLiteLongContextOverride(&base.CacheReadCostPer1MTokensPriorityAbove272K, catalogBase.CacheReadCostPer1MTokensPriorityAbove272K, catalogBase.CacheReadCostPer1MTokens, base.CacheReadCostPer1MTokens)
	}
	if override.CacheWritePrice != nil {
		base.CacheCreationCostPer1MTokens = perTokenToPerMillion(*override.CacheWritePrice)
		base.CacheCreationCostPer1MTokensPriority = base.CacheCreationCostPer1MTokens
		applyGatewayLiteLongContextOverride(&base.CacheCreationCostPer1MTokensAbove272K, catalogBase.CacheCreationCostPer1MTokensAbove272K, catalogBase.CacheCreationCostPer1MTokens, base.CacheCreationCostPer1MTokens)
		applyGatewayLiteLongContextOverride(&base.CacheCreationCostPer1MTokensPriorityAbove272K, catalogBase.CacheCreationCostPer1MTokensPriorityAbove272K, catalogBase.CacheCreationCostPer1MTokens, base.CacheCreationCostPer1MTokens)
	}
	if override.ImageInputPrice != nil {
		value := *override.ImageInputPrice
		base.InputCostPerImageToken = &value
	}
	if override.ImageOutputPrice != nil {
		base.OutputCostPerImageToken = *override.ImageOutputPrice
	}
	if override.PerRequestPrice != nil {
		base.PerRequestPrice = *override.PerRequestPrice
		if mode == "image" {
			base.OutputCostPerImage = *override.PerRequestPrice
		}
		if mode == "video" {
			base.VideoPricePerSecond480P = *override.PerRequestPrice
			base.VideoPricePerSecond720P = *override.PerRequestPrice
			base.VideoPricePerSecond1080P = *override.PerRequestPrice
		}
	}
	applyGatewayLiteMediaTiers(&base, override.Intervals, mode)
	applyGatewayLiteDisplayPriceFields(&base, catalogBase, override, mode)
	base.Currency = "USD"
	base.Source = "gateway-lite-group-pricing"
	return base
}

// gatewayLiteCompositeContractDecision 汇总所有可用接口的真实聚合路由。
// 分组价格合同没有 endpoint（接口）维度；当同一公开模型在不同接口指向
// 不同平台或上游模型时，返回 false 让调用方省略该合同，避免展示一个并不
// 适用于所有请求路径的价格。只有目标完全一致时才可安全合并。
func gatewayLiteCompositeContractDecision(
	groupID int64,
	routes []service.CompositeModelRoute,
	model string,
) (service.CompositeRouteDecision, bool) {
	var selected service.CompositeRouteDecision
	matched := false
	for _, endpoint := range []string{
		service.CompositeRouteEndpointAny,
		service.CompositeRouteEndpointResponses,
		service.CompositeRouteEndpointMessages,
		service.CompositeRouteEndpointCountTokens,
		service.CompositeRouteEndpointChatCompletions,
		service.CompositeRouteEndpointEmbeddings,
		service.CompositeRouteEndpointImages,
		service.CompositeRouteEndpointGemini,
	} {
		decision := service.ResolveCompositeRouteFromRoutes(groupID, routes, model, endpoint)
		if !decision.Matched {
			continue
		}
		if !matched {
			selected = decision
			matched = true
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(selected.TargetPlatform), strings.TrimSpace(decision.TargetPlatform)) ||
			!strings.EqualFold(strings.TrimSpace(selected.UpstreamModel), strings.TrimSpace(decision.UpstreamModel)) {
			return service.CompositeRouteDecision{}, false
		}
	}
	if !matched {
		// 没有显式规则时，只有内置平台探测给出确定结果才允许生成合同；
		// 无法识别的平台继续失败关闭，避免跨平台猜价。
		fallback := service.ResolveCompositeRouteFromRoutes(groupID, routes, model, service.CompositeRouteEndpointAny)
		if fallback.Matched {
			return fallback, true
		}
		return service.CompositeRouteDecision{}, false
	}
	return selected, true
}

func applyGatewayLiteLongContextOverride(target *float64, catalogLong, catalogBase, overrideBase float64) {
	if target == nil || catalogLong <= 0 || catalogBase <= 0 {
		return
	}
	*target = overrideBase * catalogLong / catalogBase
}

func clearGatewayLiteGroupTokenPrices(price *gatewaylite.GatewayModelPrice) {
	price.InputCostPer1MTokens = 0
	price.InputCostPer1MTokensPriority = 0
	price.OutputCostPer1MTokens = 0
	price.OutputCostPer1MTokensPriority = 0
	price.CacheReadCostPer1MTokens = 0
	price.CacheReadCostPer1MTokensPriority = 0
	price.CacheCreationCostPer1MTokens = 0
	price.CacheCreationCostPer1MTokensPriority = 0
	price.InputCostPer1MTokensAbove272K = 0
	price.InputCostPer1MTokensPriorityAbove272K = 0
	price.OutputCostPer1MTokensAbove272K = 0
	price.OutputCostPer1MTokensPriorityAbove272K = 0
	price.CacheReadCostPer1MTokensAbove272K = 0
	price.CacheReadCostPer1MTokensPriorityAbove272K = 0
	price.CacheCreationCostPer1MTokensAbove272K = 0
	price.CacheCreationCostPer1MTokensPriorityAbove272K = 0
	price.LongContextInputTokenThreshold = 0
	price.InputCostPerImageToken = nil
	price.OutputCostPerImageToken = 0
	price.SupportsPromptCaching = false
	price.SupportsServiceTier = false
}

func gatewayLiteGroupPricingIntervals(intervals []service.PricingInterval, mode string) []gatewaylite.GatewayModelPriceInterval {
	out := make([]gatewaylite.GatewayModelPriceInterval, 0, len(intervals))
	for _, interval := range intervals {
		out = append(out, gatewaylite.GatewayModelPriceInterval{
			TierLabel:                 strings.TrimSpace(interval.TierLabel),
			MinTokens:                 interval.MinTokens,
			MaxTokens:                 cloneGatewayLiteIntPtr(interval.MaxTokens),
			InputCostPer1MTokens:      gatewayLitePerMillionPtr(interval.InputPrice),
			OutputCostPer1MTokens:     gatewayLitePerMillionPtr(interval.OutputPrice),
			CacheReadCostPer1MTokens:  gatewayLitePerMillionPtr(interval.CacheReadPrice),
			CacheWriteCostPer1MTokens: gatewayLitePerMillionPtr(interval.CacheWritePrice),
			PerRequestPrice:           cloneGatewayLiteFloat64Ptr(interval.PerRequestPrice),
			SortOrder:                 interval.SortOrder,
		})
		if mode == "image" && interval.PerRequestPrice != nil {
			value := *interval.PerRequestPrice
			out[len(out)-1].OutputCostPerImage = &value
		}
	}
	return out
}

func applyGatewayLiteMediaTiers(price *gatewaylite.GatewayModelPrice, intervals []service.PricingInterval, mode string) {
	for _, interval := range intervals {
		if interval.PerRequestPrice == nil {
			continue
		}
		tier := strings.ToLower(strings.TrimSpace(interval.TierLabel))
		if mode == "image" && price.OutputCostPerImage == 0 {
			price.OutputCostPerImage = *interval.PerRequestPrice
		}
		if mode != "video" {
			continue
		}
		switch tier {
		case "480p":
			price.VideoPricePerSecond480P = *interval.PerRequestPrice
		case "720p":
			price.VideoPricePerSecond720P = *interval.PerRequestPrice
		case "1080p":
			price.VideoPricePerSecond1080P = *interval.PerRequestPrice
		}
	}
}

func gatewayLitePerMillionPtr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	converted := perTokenToPerMillion(*value)
	return &converted
}

func normalizeGatewayLitePublicPricingPlatform(platform, fallback string) string {
	value := strings.ToLower(strings.TrimSpace(platform))
	switch {
	case value == "xai" || value == "x-ai" || strings.Contains(value, "grok"):
		return service.PlatformGrok
	case strings.Contains(value, "antigravity"):
		return service.PlatformAntigravity
	case strings.Contains(value, "anthropic") || strings.Contains(value, "claude"):
		return service.PlatformAnthropic
	case strings.Contains(value, "openai"):
		return service.PlatformOpenAI
	case strings.Contains(value, "gemini") || value == "google":
		return service.PlatformGemini
	case strings.Contains(value, "composite") || strings.Contains(value, "aggregate"):
		return service.PlatformComposite
	case value != "":
		return value
	default:
		return strings.ToLower(strings.TrimSpace(fallback))
	}
}

func gatewayLiteGroupPricingSource(source string, custom bool) string {
	if custom {
		return "gateway-lite-group-contract"
	}
	if strings.TrimSpace(source) != "" {
		return source
	}
	return "gateway-lite-catalog"
}
