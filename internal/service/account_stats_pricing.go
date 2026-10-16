package service

import (
	"context"
	"math"
	"strings"
)

// resolveAccountStatsCost 计算账号统计定价费用。
// 返回 nil 表示最终上游模型没有可解析的采购价格。新请求会由调用方
// 写入明确的零值；只有历史上没有 upstream_model 的旧记录才允许回退
// 到 total_cost，避免把客户销售价格误当成账号采购成本。
//
// 优先级（先命中为准）：
//  1. 账号/分组自定义成本规则（始终尝试，不依赖开关）
//  2. ApplyPricingToAccountStats 开启时，使用分组定价合同按上游模型计费
//  3. LiteLLM（模型价格目录）/fallback（回退价格）按上游模型计费
//  4. nil → 新记录按 0/未知处理，历史无 upstream_model 记录才兼容旧回退
//
// upstreamModel 是最终发往上游的模型 ID。
// totalCost 是本次请求的客户计费（倍率前），仅作为历史兼容参数，不能作为账号成本模型。
// serviceTier 是最终发往上游的服务层级，参与账号成本计算。
func resolveAccountStatsCost(
	ctx context.Context,
	channelService *ChannelService,
	billingService *BillingService,
	accountID int64,
	groupID int64,
	upstreamModel string,
	tokens UsageTokens,
	requestCount int,
	totalCost float64,
	serviceTier string,
	sizeTiers ...string,
) *float64 {
	if upstreamModel == "" {
		return nil
	}

	// 渠道配置是账号成本的增强来源，不是模型目录定价的前置条件。
	// 分组可能没有渠道，或者渠道缓存临时不可用；这两种情况都应继续
	// 使用最终 upstreamModel 的全局价格，避免回退到用户请求模型的价格。
	var channel *Channel
	if channelService != nil {
		resolvedChannel, err := channelService.GetChannelForGroup(ctx, groupID)
		if err == nil {
			channel = resolvedChannel
		}
	}

	if channel != nil {
		platform := ""
		if channelService != nil {
			platform = effectiveChannelPlatform(ctx, channelService.GetGroupPlatform(ctx, groupID))
		}

		// 优先级 1：自定义规则（始终尝试）
		if cost, matched := tryCustomRulesResolved(channel, accountID, groupID, platform, upstreamModel, tokens, requestCount, sizeTiers...); matched {
			return cost
		}

		// 优先级 2：渠道开启“应用模型定价到账号统计”时，仍必须以最终上游模型
		// 重新解析价格。不能复用 totalCost：它可能来自用户请求模型或分组映射模型，
		// 在 sol → luna 等映射场景会把账号采购成本算错。
		if channel.ApplyPricingToAccountStats && billingService != nil {
			resolver := NewModelPricingResolver(channelService, billingService)
			groupIDCopy := groupID
			sizeTier := ""
			if len(sizeTiers) > 0 {
				sizeTier = strings.TrimSpace(sizeTiers[0])
			}
			resolved := resolver.Resolve(ctx, PricingInput{
				Model:   upstreamModel,
				GroupID: &groupIDCopy,
			})
			breakdown, pricingErr := billingService.CalculateCostUnified(CostInput{
				Ctx:            ctx,
				Model:          upstreamModel,
				GroupID:        &groupIDCopy,
				Tokens:         tokens,
				RequestCount:   requestCount,
				SizeTier:       sizeTier,
				RateMultiplier: 1,
				ServiceTier:    serviceTier,
				Resolver:       resolver,
				Resolved:       resolved,
			})
			if pricingErr == nil && breakdown != nil {
				cost := breakdown.TotalCost
				return &cost
			}
		}
	}

	// 优先级 3：模型定价文件（LiteLLM）默认价格
	if billingService != nil {
		return tryModelFilePricing(billingService, upstreamModel, tokens, serviceTier)
	}

	return nil
}

// tryModelFilePricing 使用模型定价文件（LiteLLM/fallback）计算费用。
// 标准档保持原有 CalculateCost 逻辑；Priority/Fast/Flex 必须按最终服务层级计算，
// 否则账号成本统计会与本次请求的真实上游档位不一致。
func tryModelFilePricing(billingService *BillingService, model string, tokens UsageTokens, serviceTier string) *float64 {
	pricing, err := billingService.GetModelPricing(model)
	if err != nil || pricing == nil {
		return nil
	}
	var breakdown *CostBreakdown
	switch normalizeBillingServiceTier(serviceTier) {
	case "priority", "fast", "flex":
		breakdown, err = billingService.CalculateCostWithServiceTier(model, tokens, 1, serviceTier)
	default:
		breakdown, err = billingService.CalculateCost(model, tokens, 1)
	}
	if err != nil || breakdown == nil || breakdown.TotalCost <= 0 {
		return nil
	}
	return &breakdown.TotalCost
}

// tryCustomRules 遍历自定义规则，按数组顺序先命中为准。
func tryCustomRules(
	channel *Channel, accountID, groupID int64,
	platform, model string, tokens UsageTokens, requestCount int, sizeTiers ...string,
) *float64 {
	cost, _ := tryCustomRulesResolved(channel, accountID, groupID, platform, model, tokens, requestCount, sizeTiers...)
	return cost
}

// tryCustomRulesResolved 返回 (费用, 是否命中)。
// 价格为显式 0 时费用指针仍非 nil，避免把“免费采购价”误判为未命中后
// 回退到模型目录价格。只有完全没有价格字段的空规则才允许继续查下一条。
func tryCustomRulesResolved(
	channel *Channel, accountID, groupID int64,
	platform, model string, tokens UsageTokens, requestCount int, sizeTiers ...string,
) (*float64, bool) {
	if channel == nil {
		return nil, false
	}
	modelLower := strings.ToLower(model)
	for _, rule := range channel.AccountStatsPricingRules {
		if !matchAccountStatsRule(&rule, accountID, groupID) {
			continue
		}
		pricing := findPricingForModel(rule.Pricing, platform, modelLower)
		if pricing == nil {
			continue // 规则匹配但模型不在规则定价中，继续下一条
		}
		cost, hasPrice := calculateStatsCostResolved(pricing, tokens, requestCount, sizeTiers...)
		if hasPrice {
			return cost, true
		}
	}
	return nil, false
}

// matchAccountStatsRule 检查规则是否匹配指定的 accountID 和 groupID。
// 匹配条件：accountID ∈ rule.AccountIDs 或 groupID ∈ rule.GroupIDs。
// 如果规则的 AccountIDs 和 GroupIDs 都为空，视为不匹配。
func matchAccountStatsRule(rule *AccountStatsPricingRule, accountID, groupID int64) bool {
	if len(rule.AccountIDs) == 0 && len(rule.GroupIDs) == 0 {
		return false
	}
	for _, id := range rule.AccountIDs {
		if id == accountID {
			return true
		}
	}
	for _, id := range rule.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

// findPricingForModel 在定价列表中查找匹配的模型定价。
// 先精确匹配，再通配符匹配（按配置顺序，先匹配先使用）。
func findPricingForModel(pricingList []ChannelModelPricing, platform, modelLower string) *ChannelModelPricing {
	// 精确匹配优先
	for i := range pricingList {
		p := &pricingList[i]
		if !isPlatformMatch(platform, p.Platform) {
			continue
		}
		for _, m := range p.Models {
			if strings.ToLower(m) == modelLower {
				return p
			}
		}
	}
	// 通配符匹配：按配置顺序，先匹配先使用
	for i := range pricingList {
		p := &pricingList[i]
		if !isPlatformMatch(platform, p.Platform) {
			continue
		}
		for _, m := range p.Models {
			ml := strings.ToLower(m)
			if !strings.HasSuffix(ml, "*") {
				continue
			}
			prefix := strings.TrimSuffix(ml, "*")
			if strings.HasPrefix(modelLower, prefix) {
				return p
			}
		}
	}
	return nil
}

// isPlatformMatch 判断平台是否匹配（空平台视为不限平台）。
func isPlatformMatch(queryPlatform, pricingPlatform string) bool {
	if queryPlatform == "" || pricingPlatform == "" {
		return true
	}
	return queryPlatform == pricingPlatform
}

// calculateStatsCost 使用给定的定价计算费用（不含任何倍率，原始费用）。
func calculateStatsCost(pricing *ChannelModelPricing, tokens UsageTokens, requestCount int, sizeTiers ...string) *float64 {
	if pricing == nil {
		return nil
	}
	switch pricing.BillingMode {
	case BillingModePerRequest, BillingModeImage, BillingModeVideo:
		return calculatePerRequestStatsCost(pricing, requestCount, sizeTiers...)
	default:
		return calculateTokenStatsCost(pricing, tokens)
	}
}

// calculatePerRequestStatsCost 按次/图片计费。
func calculatePerRequestStatsCost(pricing *ChannelModelPricing, requestCount int, sizeTiers ...string) *float64 {
	if pricing == nil {
		return nil
	}
	price := pricing.PerRequestPrice
	// 图片/视频及按次定价可按分辨率或上下文档位覆盖默认单价。
	// 区间命中且显式为 0 时必须保留该 0，不得回退默认价。
	if len(sizeTiers) > 0 {
		tierLabel := strings.TrimSpace(sizeTiers[0])
		if tierLabel != "" {
			for i := range pricing.Intervals {
				interval := &pricing.Intervals[i]
				if strings.EqualFold(strings.TrimSpace(interval.TierLabel), tierLabel) && interval.PerRequestPrice != nil {
					price = interval.PerRequestPrice
					break
				}
			}
		}
	}
	if price == nil || !finiteNonNegative(*price) {
		return nil
	}
	count := requestCount
	if count < 0 {
		count = 0
	}
	cost := *price * float64(count)
	if !finiteNonNegative(cost) {
		return nil
	}
	// Keep the historical helper contract: zero is returned as nil here. The
	// resolved custom-rule path below converts a configured zero into a
	// non-nil authoritative pointer.
	if cost <= 0 {
		return nil
	}
	return &cost
}

// calculateTokenStatsCost Token 计费。
// If the pricing has intervals, find the matching interval by total token count
// and use its prices instead of the flat pricing fields.
func calculateTokenStatsCost(pricing *ChannelModelPricing, tokens UsageTokens) *float64 {
	p := pricing
	if len(pricing.Intervals) > 0 {
		totalTokens := tokens.InputTokens + tokens.OutputTokens + tokens.CacheCreationTokens + tokens.CacheReadTokens
		if iv := FindMatchingInterval(pricing.Intervals, totalTokens); iv != nil {
			p = &ChannelModelPricing{
				InputPrice:       iv.InputPrice,
				OutputPrice:      iv.OutputPrice,
				CacheWritePrice:  iv.CacheWritePrice,
				CacheReadPrice:   iv.CacheReadPrice,
				ImageInputPrice:  pricing.ImageInputPrice,
				ImageOutputPrice: pricing.ImageOutputPrice,
				PerRequestPrice:  iv.PerRequestPrice,
			}
		}
	}
	deref := func(ptr *float64) float64 {
		if ptr == nil {
			return 0
		}
		if !finiteNonNegative(*ptr) {
			return 0
		}
		return *ptr
	}
	inputTokens := max(tokens.InputTokens, 0)
	outputTokens := max(tokens.OutputTokens, 0)
	imageInputTokens := min(max(tokens.ImageInputTokens, 0), inputTokens)
	textInputTokens := max(inputTokens-imageInputTokens, 0)
	imageOutputTokens := min(max(tokens.ImageOutputTokens, 0), outputTokens)
	textOutputTokens := max(outputTokens-imageOutputTokens, 0)
	imageInputPrice := deref(p.ImageInputPrice)
	if p.ImageInputPrice == nil {
		imageInputPrice = deref(p.InputPrice)
	}
	cost := float64(textInputTokens)*deref(p.InputPrice) +
		float64(imageInputTokens)*imageInputPrice +
		float64(textOutputTokens)*deref(p.OutputPrice) +
		float64(max(tokens.CacheCreationTokens, 0))*deref(p.CacheWritePrice) +
		float64(max(tokens.CacheReadTokens, 0))*deref(p.CacheReadPrice) +
		float64(imageOutputTokens)*deref(p.ImageOutputPrice)
	if !finiteNonNegative(cost) || cost <= 0 {
		return nil
	}
	return &cost
}

// calculateStatsCostResolved keeps the distinction between an absent price
// and a configured zero price. The latter is authoritative for account-cost
// accounting and must stop the fallback chain.
func calculateStatsCostResolved(pricing *ChannelModelPricing, tokens UsageTokens, requestCount int, sizeTiers ...string) (*float64, bool) {
	if pricing == nil || !hasEffectiveStatsPricingFields(pricing, tokens, sizeTiers...) {
		return nil, false
	}
	var cost *float64
	switch pricing.BillingMode {
	case BillingModePerRequest, BillingModeImage, BillingModeVideo:
		cost = calculatePerRequestStatsCost(pricing, requestCount, sizeTiers...)
	default:
		cost = calculateTokenStatsCost(pricing, tokens)
	}
	if cost != nil {
		return cost, true
	}
	zero := 0.0
	return &zero, true
}

// hasEffectiveStatsPricingFields reports whether the matched row has a price
// applicable to this request. A matched model row with an explicit zero is
// considered configured; an interval for a different tier is not a match, so
// later rules may still provide a price.
func hasEffectiveStatsPricingFields(pricing *ChannelModelPricing, tokens UsageTokens, sizeTiers ...string) bool {
	if pricing == nil {
		return false
	}
	if pricing.BillingMode == BillingModePerRequest || pricing.BillingMode == BillingModeImage || pricing.BillingMode == BillingModeVideo {
		if len(sizeTiers) > 0 {
			label := strings.TrimSpace(sizeTiers[0])
			if label != "" {
				for _, interval := range pricing.Intervals {
					if strings.EqualFold(strings.TrimSpace(interval.TierLabel), label) {
						return interval.PerRequestPrice != nil
					}
				}
			}
		}
		return pricing.PerRequestPrice != nil
	}

	if len(pricing.Intervals) > 0 {
		totalTokens := max(tokens.InputTokens, 0) + max(tokens.OutputTokens, 0) +
			max(tokens.CacheCreationTokens, 0) + max(tokens.CacheReadTokens, 0)
		if interval := FindMatchingInterval(pricing.Intervals, totalTokens); interval != nil {
			return hasTokenStatsPricingFields(interval)
		}
	}
	return hasTokenStatsPricingFields(&PricingInterval{
		InputPrice:      pricing.InputPrice,
		OutputPrice:     pricing.OutputPrice,
		CacheWritePrice: pricing.CacheWritePrice,
		CacheReadPrice:  pricing.CacheReadPrice,
		PerRequestPrice: pricing.PerRequestPrice,
	}) || pricing.ImageInputPrice != nil || pricing.ImageOutputPrice != nil
}

func hasTokenStatsPricingFields(interval *PricingInterval) bool {
	if interval == nil {
		return false
	}
	fields := []*float64{
		interval.InputPrice, interval.OutputPrice, interval.CacheWritePrice,
		interval.CacheReadPrice, interval.PerRequestPrice,
	}
	for _, field := range fields {
		if field != nil {
			return true
		}
	}
	if interval.InputMultiplier != nil || interval.OutputMultiplier != nil ||
		interval.CacheWriteMultiplier != nil || interval.CacheReadMultiplier != nil {
		return true
	}
	return false
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

// accountStatsCostMode describes whether the usage result carries a pricing
// fact that can be used for account procurement statistics.  Audio and search
// results currently have only customer-facing billing rules; their upstream
// procurement prices are not represented in the model pricing catalog.
type accountStatsCostMode uint8

const (
	accountStatsCostModeDefault accountStatsCostMode = iota
	// accountStatsCostModeNoProcurementEvidence prevents both token-catalog and
	// legacy total_cost fallbacks.  The caller records an explicit zero until a
	// dedicated upstream audio/search procurement price is available.
	accountStatsCostModeNoProcurementEvidence
)

// applyAccountStatsCost resolves the account stats cost for a usage log entry.
// A known final upstream model always wins. If that value is missing, the
// request model is used only when the usage row has no mapping evidence;
// mapped requests fail closed to an explicit zero instead of borrowing the
// customer's public-model price.
func applyAccountStatsCost(
	ctx context.Context,
	usageLog *UsageLog,
	cs *ChannelService, bs *BillingService,
	accountID int64, groupID int64,
	upstreamModel, requestedModel string,
	tokens UsageTokens,
	totalCost float64,
	modes ...accountStatsCostMode,
) {
	if usageLog == nil {
		return
	}
	if len(modes) > 0 && modes[0] == accountStatsCostModeNoProcurementEvidence {
		// Audio/search customer prices are not evidence of upstream procurement
		// cost.  Persist zero explicitly so quota/stat queries cannot fall back
		// to total_cost while this media pricing source is unavailable.
		zero := 0.0
		usageLog.AccountStatsCost = &zero
		return
	}
	model := resolveAccountStatsModel(usageLog, upstreamModel, requestedModel)
	requestCount := 1
	if usageLog != nil {
		if usageLog.VideoCount > 0 {
			requestCount = usageLog.VideoCount
			if usageLog.VideoDurationSeconds != nil && *usageLog.VideoDurationSeconds > 0 {
				requestCount *= *usageLog.VideoDurationSeconds
			}
		} else if usageLog.ImageCount > 0 {
			requestCount = usageLog.ImageCount
		}
	}
	serviceTier := ""
	if usageLog != nil && usageLog.ServiceTier != nil {
		serviceTier = *usageLog.ServiceTier
	}
	sizeTier := ""
	if usageLog != nil {
		if usageLog.VideoResolution != nil {
			sizeTier = *usageLog.VideoResolution
		} else if usageLog.ImageSize != nil {
			sizeTier = *usageLog.ImageSize
		} else if usageLog.BillingTier != nil {
			sizeTier = *usageLog.BillingTier
		}
	}
	resolvedCost := resolveAccountStatsCost(
		ctx, cs, bs, accountID, groupID, model, tokens, requestCount, totalCost, serviceTier,
		sizeTier,
	)
	if resolvedCost == nil {
		// 价格目录暂时没有该上游模型时也要写入权威零值。否则下游配额
		// 逻辑会把客户侧 total_cost 当成采购成本，映射到低价模型时会被放大。
		zero := 0.0
		resolvedCost = &zero
	}
	usageLog.AccountStatsCost = resolvedCost
}

// accountQuotaCostFromUsageLog converts the resolved raw procurement cost to
// the account quota unit. A non-nil AccountStatsCost is authoritative,
// including an intentional zero. If the final upstream model is known but its
// cost could not be resolved, fail closed at zero; only rows without upstream
// model evidence use the legacy customer-cost fallback.
func accountQuotaCostFromUsageLog(usageLog *UsageLog, fallbackTotalCost, accountRateMultiplier float64) float64 {
	rawCost := fallbackTotalCost
	if usageLog != nil && usageLog.AccountStatsCost != nil {
		rawCost = *usageLog.AccountStatsCost
	} else if usageLog != nil && usageLog.UpstreamModel != nil && strings.TrimSpace(*usageLog.UpstreamModel) != "" {
		rawCost = 0
	}
	if !finiteNonNegative(rawCost) || !finiteNonNegative(accountRateMultiplier) {
		return 0
	}
	cost := rawCost * accountRateMultiplier
	if !finiteNonNegative(cost) {
		return 0
	}
	return cost
}
