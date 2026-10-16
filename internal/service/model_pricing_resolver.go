package service

import (
	"context"
	"log/slog"
)

// PricingSource 定价来源标识
const (
	// PricingSourceGroup 表示定价来自分组（group）显式配置。
	// [tokenav 定制] 合并 v0.2.5 时补充：官方 billing_token_cost_request.go /
	// billing_context_schedule.go 会以该来源判定「走统一计费」分支，
	// 本地原先只有 channel/litellm/fallback 三种，补齐以避免新引入的
	// 上下文阶梯计费（context pricing schedule）无法识别分组定价。
	PricingSourceGroup    = "group"
	PricingSourceChannel  = "channel"
	PricingSourceLiteLLM  = "litellm"
	PricingSourceFallback = "fallback"
)

// ResolvedPricing 统一定价解析结果
type ResolvedPricing struct {
	// Mode 计费模式
	Mode BillingMode

	// Token 模式：基础定价（来自 LiteLLM 或 fallback）
	BasePricing *ModelPricing

	// Token 模式：区间定价列表（如有，覆盖 BasePricing 中的对应字段）
	Intervals []PricingInterval

	// 按次/图片模式：分层定价
	RequestTiers []PricingInterval

	// 按次/图片模式：默认价格（未命中层级时使用）
	DefaultPerRequestPrice float64

	// 来源标识
	Source string // "channel", "litellm", "fallback"

	// 是否支持缓存细分
	SupportsCacheBreakdown bool

	// 渠道定价原始配置（用于区间模式下获取 ImageOutputPrice）
	channelPricing *ChannelModelPricing

	// [v0.2.5 新增字段] 长上下文阶梯计费开关（来自 Group 或默认开启）
	longContextPricingEnabled bool
}

// ModelPricingResolver 统一模型定价解析器。
// 解析链：Channel → LiteLLM → Fallback。
type ModelPricingResolver struct {
	channelService *ChannelService
	billingService *BillingService
}

// NewModelPricingResolver 创建定价解析器实例
func NewModelPricingResolver(channelService *ChannelService, billingService *BillingService) *ModelPricingResolver {
	return &ModelPricingResolver{
		channelService: channelService,
		billingService: billingService,
	}
}

// PricingInput 定价解析输入
type PricingInput struct {
	Model   string
	GroupID *int64 // nil 表示不检查渠道
	Group   *Group // [v0.2.5 新增字段] 分组对象，用于长上下文阶梯开关
}

// Resolve 解析模型定价。
// 1. 获取基础定价（LiteLLM → Fallback）
// 2. 如果指定了 GroupID，查找渠道定价并覆盖
func (r *ModelPricingResolver) Resolve(ctx context.Context, input PricingInput) *ResolvedPricing {
	var chPricing *ChannelModelPricing
	if input.GroupID != nil && r.channelService != nil {
		chPricing = r.channelService.GetChannelModelPricing(ctx, *input.GroupID, input.Model)
		if chPricing != nil {
			mode := chPricing.BillingMode
			if mode == "" {
				mode = BillingModeToken
			}
			if mode == BillingModePerRequest || mode == BillingModeImage || mode == BillingModeVideo {
				resolved := &ResolvedPricing{
					Mode:           mode,
					Source:         PricingSourceChannel,
					channelPricing: chPricing,
				}
				r.applyRequestTierOverrides(chPricing, resolved)
				return resolved
			}
		}
	}

	// 1. 获取基础定价
	basePricing, source := r.resolveBasePricing(input.Model)

	resolved := &ResolvedPricing{
		Mode:                   BillingModeToken,
		BasePricing:            basePricing,
		Source:                 source,
		SupportsCacheBreakdown: basePricing != nil && basePricing.SupportsCacheBreakdown,
	}

	// 2. 如果有 GroupID，尝试渠道覆盖
	if chPricing != nil {
		resolved.Source = PricingSourceChannel
		resolved.channelPricing = chPricing
		r.applyTokenOverrides(chPricing, resolved)
	} else if input.GroupID != nil && r.channelService != nil {
		// 恢复执行价和目录计价可带分组身份，但没有渠道存储时不得回读空服务。
		r.applyChannelOverrides(ctx, *input.GroupID, input.Model, resolved)
	}

	return resolved
}

// resolveBasePricing 从 LiteLLM 或 Fallback 获取基础定价
func (r *ModelPricingResolver) resolveBasePricing(model string) (*ModelPricing, string) {
	pricing, err := r.billingService.GetModelPricing(model)
	if err != nil {
		slog.Debug("failed to get model pricing from LiteLLM, using fallback",
			"model", model, "error", err)
		return nil, PricingSourceFallback
	}
	return pricing, PricingSourceLiteLLM
}

// applyChannelOverrides 应用渠道定价覆盖
func (r *ModelPricingResolver) applyChannelOverrides(ctx context.Context, groupID int64, model string, resolved *ResolvedPricing) {
	chPricing := r.channelService.GetChannelModelPricing(ctx, groupID, model)
	if chPricing == nil {
		return
	}

	resolved.Source = PricingSourceChannel
	resolved.channelPricing = chPricing
	resolved.Mode = chPricing.BillingMode
	if resolved.Mode == "" {
		resolved.Mode = BillingModeToken
	}

	switch resolved.Mode {
	case BillingModeToken:
		r.applyTokenOverrides(chPricing, resolved)
	case BillingModePerRequest, BillingModeImage, BillingModeVideo:
		r.applyRequestTierOverrides(chPricing, resolved)
	}
}

// applyTokenOverrides 应用 token 模式的渠道覆盖
func (r *ModelPricingResolver) applyTokenOverrides(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	if resolved.BasePricing == nil {
		resolved.BasePricing = &ModelPricing{}
	} else {
		// 模型目录和 fallback（回退价）会共享对象；渠道覆盖必须复制后再改，
		// 否则一个分组的价格会污染其他分组的后续请求。
		cloned := *resolved.BasePricing
		resolved.BasePricing = &cloned
	}

	// 先应用渠道基础价，区间倍率才会以管理员配置的基础价换算；同时保留模型目录中
	// Fast/Priority（快速档）相对标准价的比例，避免自定义标准价把快速档抹平。
	applyChannelTokenPriceOverrides(resolved.BasePricing, chPricing)
	resolved.BasePricing.FastMultiplier = chPricing.FastMultiplier
	resolved.BasePricing.FlexMultiplier = chPricing.FlexMultiplier
	if chPricing.ImageInputPrice != nil {
		resolved.BasePricing.ImageInputPricePerToken = *chPricing.ImageInputPrice
		resolved.BasePricing.ImageInputPriceExplicit = true
		resolved.BasePricing.ImageInputPriceSource = "channel"
	}
	// 渠道定价覆盖一切：显式配置则用配置值，未配置则归零（不回退到 LiteLLM）
	if chPricing.ImageOutputPrice != nil {
		resolved.BasePricing.ImageOutputPricePerToken = *chPricing.ImageOutputPrice
	} else {
		resolved.BasePricing.ImageOutputPricePerToken = 0
	}
	resolved.BasePricing.ImageOutputPriceExplicit = true
	// 区间未命中时回退到已经应用渠道覆盖的基础价；纯倍率区间也必须保留。
	resolved.Intervals = filterValidIntervals(chPricing.Intervals)
}

// applyRequestTierOverrides 应用按次/图片模式的渠道覆盖
func (r *ModelPricingResolver) applyRequestTierOverrides(chPricing *ChannelModelPricing, resolved *ResolvedPricing) {
	resolved.RequestTiers = filterValidIntervals(chPricing.Intervals)
	if chPricing.PerRequestPrice != nil {
		resolved.DefaultPerRequestPrice = *chPricing.PerRequestPrice
	}
}

// filterValidIntervals 过滤掉所有价格字段都为空的无效 interval。
// 前端可能创建了只有 min/max 但无价格的空 interval。
func filterValidIntervals(intervals []PricingInterval) []PricingInterval {
	var valid []PricingInterval
	for _, iv := range intervals {
		if iv.InputPrice != nil || iv.OutputPrice != nil ||
			iv.CacheWritePrice != nil || iv.CacheReadPrice != nil ||
			iv.PerRequestPrice != nil || iv.InputMultiplier != nil ||
			iv.OutputMultiplier != nil || iv.CacheWriteMultiplier != nil ||
			iv.CacheReadMultiplier != nil {
			valid = append(valid, iv)
		}
	}
	return valid
}

// GetIntervalPricing 根据 context token 数获取区间定价。
// 如果有区间列表，找到匹配区间并构造 ModelPricing；否则直接返回 BasePricing。
func (r *ModelPricingResolver) GetIntervalPricing(resolved *ResolvedPricing, totalContextTokens int) *ModelPricing {
	if len(resolved.Intervals) == 0 {
		return resolved.BasePricing
	}

	iv := FindMatchingInterval(resolved.Intervals, totalContextTokens)
	if iv == nil {
		return resolved.BasePricing
	}

	pricing := intervalToModelPricing(iv, resolved.BasePricing, resolved.channelPricing)
	// BasePricing 为空时无法复制这个能力位，从解析结果回填，避免缓存 5m/1h 分档丢失。
	pricing.SupportsCacheBreakdown = resolved.SupportsCacheBreakdown
	return pricing
}

// intervalToModelPricing 将区间定价转换为 ModelPricing
func intervalToModelPricing(iv *PricingInterval, basePricing *ModelPricing, chPricing *ChannelModelPricing) *ModelPricing {
	pricing := &ModelPricing{}
	if basePricing != nil {
		*pricing = *basePricing
	}
	applyMultiplier := func(value float64, multiplier *float64) float64 {
		if multiplier == nil {
			return value
		}
		return value * *multiplier
	}
	if iv.InputPrice != nil {
		pricing.InputPricePerTokenPriority = channelTierOverridePrice(pricing.InputPricePerToken, pricing.InputPricePerTokenPriority, *iv.InputPrice)
		pricing.InputPricePerToken = *iv.InputPrice
	} else if iv.InputMultiplier != nil {
		pricing.InputPricePerToken = applyMultiplier(pricing.InputPricePerToken, iv.InputMultiplier)
		pricing.InputPricePerTokenPriority = applyMultiplier(pricing.InputPricePerTokenPriority, iv.InputMultiplier)
	}
	if iv.OutputPrice != nil {
		pricing.OutputPricePerTokenPriority = channelTierOverridePrice(pricing.OutputPricePerToken, pricing.OutputPricePerTokenPriority, *iv.OutputPrice)
		pricing.OutputPricePerToken = *iv.OutputPrice
	} else if iv.OutputMultiplier != nil {
		pricing.OutputPricePerToken = applyMultiplier(pricing.OutputPricePerToken, iv.OutputMultiplier)
		pricing.OutputPricePerTokenPriority = applyMultiplier(pricing.OutputPricePerTokenPriority, iv.OutputMultiplier)
	}
	if iv.CacheWritePrice != nil {
		pricing.CacheCreationPricePerTokenPriority = channelTierOverridePrice(pricing.CacheCreationPricePerToken, pricing.CacheCreationPricePerTokenPriority, *iv.CacheWritePrice)
		pricing.CacheCreationPricePerToken = *iv.CacheWritePrice
		pricing.CacheCreation5mPrice = *iv.CacheWritePrice
		pricing.CacheCreation1hPrice = *iv.CacheWritePrice
	} else if iv.CacheWriteMultiplier != nil {
		pricing.CacheCreationPricePerToken = applyMultiplier(pricing.CacheCreationPricePerToken, iv.CacheWriteMultiplier)
		pricing.CacheCreationPricePerTokenPriority = applyMultiplier(pricing.CacheCreationPricePerTokenPriority, iv.CacheWriteMultiplier)
		pricing.CacheCreation5mPrice = applyMultiplier(pricing.CacheCreation5mPrice, iv.CacheWriteMultiplier)
		pricing.CacheCreation1hPrice = applyMultiplier(pricing.CacheCreation1hPrice, iv.CacheWriteMultiplier)
	}
	if iv.CacheReadPrice != nil {
		pricing.CacheReadPricePerTokenPriority = channelTierOverridePrice(pricing.CacheReadPricePerToken, pricing.CacheReadPricePerTokenPriority, *iv.CacheReadPrice)
		pricing.CacheReadPricePerToken = *iv.CacheReadPrice
	} else if iv.CacheReadMultiplier != nil {
		pricing.CacheReadPricePerToken = applyMultiplier(pricing.CacheReadPricePerToken, iv.CacheReadMultiplier)
		pricing.CacheReadPricePerTokenPriority = applyMultiplier(pricing.CacheReadPricePerTokenPriority, iv.CacheReadMultiplier)
	}
	// 渠道定价存在时，ImageOutputPrice 显式覆盖
	if chPricing != nil {
		if chPricing.ImageInputPrice != nil {
			pricing.ImageInputPricePerToken = *chPricing.ImageInputPrice
			pricing.ImageInputPriceExplicit = true
			pricing.ImageInputPriceSource = "channel"
		}
		pricing.ImageOutputPriceExplicit = true
		if chPricing.ImageOutputPrice != nil {
			pricing.ImageOutputPricePerToken = *chPricing.ImageOutputPrice
		}
	}
	return pricing
}

// GetRequestTierPrice 根据层级标签获取按次价格
func (r *ModelPricingResolver) GetRequestTierPrice(resolved *ResolvedPricing, tierLabel string) float64 {
	price, _ := r.lookupRequestTierPrice(resolved, tierLabel)
	return price
}

// lookupRequestTierPrice 区分“明确配置为 0 元”和“没有匹配的档位”。
// 计费入口必须保留这个区别，否则免费档位会错误回退到默认正价。
func (r *ModelPricingResolver) lookupRequestTierPrice(resolved *ResolvedPricing, tierLabel string) (float64, bool) {
	if resolved == nil {
		return 0, false
	}
	for _, tier := range resolved.RequestTiers {
		if tier.TierLabel == tierLabel && tier.PerRequestPrice != nil {
			return *tier.PerRequestPrice, true
		}
	}
	return 0, false
}

// GetRequestTierPriceByContext 根据 context token 数获取按次价格
func (r *ModelPricingResolver) GetRequestTierPriceByContext(resolved *ResolvedPricing, totalContextTokens int) float64 {
	price, _ := r.lookupRequestTierPriceByContext(resolved, totalContextTokens)
	return price
}

// lookupRequestTierPriceByContext 返回匹配区间的价格及是否存在有效价格。
func (r *ModelPricingResolver) lookupRequestTierPriceByContext(resolved *ResolvedPricing, totalContextTokens int) (float64, bool) {
	if resolved == nil {
		return 0, false
	}
	iv := FindMatchingInterval(resolved.RequestTiers, totalContextTokens)
	if iv != nil && iv.PerRequestPrice != nil {
		return *iv.PerRequestPrice, true
	}
	return 0, false
}

// [v0.2.5 新增] 应用渠道图片输入价：显式配置则用配置值（含显式 0 归零）；
// 未配置时继承模型目录/fallback 的显式图片输入价（ImageInputNilInheritsAndZeroOverrides
// 契约，a76e810dc「图片输入计费证据链」），computeTokenBreakdown 仍可按显式价计费。
func applyChannelImageInputPrice(chPricing *ChannelModelPricing, pricing *ModelPricing) {
	if chPricing != nil && chPricing.ImageInputPrice != nil {
		pricing.ImageInputPricePerToken = *chPricing.ImageInputPrice
		pricing.ImageInputPriceExplicit = true
		pricing.ImageInputPriceSource = "channel"
	}
}

// [tokenav 定制] 我们的 LiteLLMModelPricing 图片价格字段为指针（nil=继承文本价），官方为 float64。
func liteLLMPtrVal(p *float64) float64 {
	if p != nil {
		return *p
	}
	return 0
}
