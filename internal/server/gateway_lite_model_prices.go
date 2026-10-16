package server

import (
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type gatewayLiteModelPriceProvider struct {
	pricingService *service.PricingService
}

func (p gatewayLiteModelPriceProvider) GatewayModelPrices(models []string, gatewayCode string) []gatewaylite.GatewayModelPrice {
	if p.pricingService == nil {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]gatewaylite.GatewayModelPrice, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		// 部分 OpenAI-compatible（OpenAI 兼容）上游把大小写不同的 ID
		// 当作两个可请求模型；价格上报必须保留账号实际公开的精确 ID。
		key := model
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		// 上报用「只认目录事实」的取价：拿不到价格就不上报，避免把未知模型
		// （如上游账号清单里的 gpt-reserve）静默按 gpt-5.4 兜底价写进主站价格镜像，
		// 也避免每次上报都打一条 pricing fallback 告警。计费路径不受影响。
		pricing := p.pricingService.GetModelPricingForReport(model)
		if pricing == nil {
			continue
		}
		cacheCreation := pricing.CacheCreationInputTokenCost
		if cacheCreation == 0 {
			cacheCreation = pricing.CacheCreationInputTokenCostAbove1hr
		}
		// 价格可以走已知别名回退；模型能力只允许从精确目录条目取得。
		metadata := p.pricingService.GetModelPricingDisplayMetadata(model)
		if metadata == nil {
			metadata = &service.LiteLLMModelPricing{}
		}
		out = append(out, gatewaylite.GatewayModelPrice{
			GatewayCode:                                   strings.TrimSpace(gatewayCode),
			Model:                                         model,
			Provider:                                      strings.TrimSpace(pricing.LiteLLMProvider),
			ModelVendor:                                   service.ModelPricingDisplayVendor(metadata.ModelVendor, pricing.LiteLLMProvider),
			InputModalities:                               metadata.InputModalities,
			MaxInputTokens:                                metadata.MaxInputTokens,
			MetadataSource:                                metadata.MetadataSource,
			PriceFields:                                   pricing.PriceFields,
			Mode:                                          strings.TrimSpace(pricing.Mode),
			BillingMode:                                   billingModeFromLiteLLM(pricing),
			InputCostPer1MTokens:                          perTokenToPerMillion(pricing.InputCostPerToken),
			InputCostPer1MTokensPriority:                  perTokenToPerMillion(pricing.InputCostPerTokenPriority),
			OutputCostPer1MTokens:                         perTokenToPerMillion(pricing.OutputCostPerToken),
			OutputCostPer1MTokensPriority:                 perTokenToPerMillion(pricing.OutputCostPerTokenPriority),
			CacheReadCostPer1MTokens:                      perTokenToPerMillion(pricing.CacheReadInputTokenCost),
			CacheReadCostPer1MTokensPriority:              perTokenToPerMillion(pricing.CacheReadInputTokenCostPriority),
			CacheCreationCostPer1MTokens:                  perTokenToPerMillion(cacheCreation),
			CacheCreationCostPer1MTokensPriority:          perTokenToPerMillion(pricing.CacheCreationInputTokenCostPriority),
			InputCostPer1MTokensAbove272K:                 perTokenToPerMillion(pricing.InputCostPerTokenAbove272K),
			InputCostPer1MTokensPriorityAbove272K:         perTokenToPerMillion(pricing.InputCostPerTokenPriorityAbove272K),
			OutputCostPer1MTokensAbove272K:                perTokenToPerMillion(pricing.OutputCostPerTokenAbove272K),
			OutputCostPer1MTokensPriorityAbove272K:        perTokenToPerMillion(pricing.OutputCostPerTokenPriorityAbove272K),
			CacheReadCostPer1MTokensAbove272K:             perTokenToPerMillion(pricing.CacheReadInputTokenCostAbove272K),
			CacheReadCostPer1MTokensPriorityAbove272K:     perTokenToPerMillion(pricing.CacheReadInputTokenCostPriorityAbove272K),
			CacheCreationCostPer1MTokensAbove272K:         perTokenToPerMillion(pricing.CacheCreationInputTokenCostAbove272K),
			CacheCreationCostPer1MTokensPriorityAbove272K: perTokenToPerMillion(pricing.CacheCreationInputTokenCostPriorityAbove272K),
			LongContextInputTokenThreshold:                pricing.LongContextInputTokenThreshold,
			InputCostPerImageToken:                        cloneGatewayLiteFloat64Ptr(pricing.InputCostPerImageToken),
			OutputCostPerImage:                            pricing.OutputCostPerImage,
			OutputCostPerImageToken:                       pricing.OutputCostPerImageToken,
			PerRequestPrice:                               perRequestPriceFromLiteLLM(pricing),
			VideoPricePerSecond480P:                       pricing.VideoPricePerSecond480P,
			VideoPricePerSecond720P:                       pricing.VideoPricePerSecond720P,
			VideoPricePerSecond1080P:                      pricing.VideoPricePerSecond1080P,
			SupportsPromptCaching:                         pricing.SupportsPromptCaching,
			SupportsServiceTier:                           pricing.SupportsServiceTier,
			Disabled:                                      pricing.Disabled,
			Currency:                                      "USD",
			Source:                                        "gateway-lite-litellm",
			// 目录内容本身参与 snapshot checksum（快照校验和）；这里不能使用
			// 当前时间，否则没有任何价格变化的周期推送也会被误判为新配置。
			UpdatedAtMillis: 0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Model) < strings.ToLower(out[j].Model)
	})
	return out
}

func billingModeFromLiteLLM(pricing *service.LiteLLMModelPricing) string {
	if pricing == nil {
		return "token"
	}
	if mode := strings.ToLower(strings.TrimSpace(pricing.BillingMode)); mode == "token" || mode == "per_request" || mode == "image" || mode == "video" {
		return mode
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(pricing.Mode)), "video") ||
		pricing.VideoPricePerSecond480P > 0 ||
		pricing.VideoPricePerSecond720P > 0 ||
		pricing.VideoPricePerSecond1080P > 0 {
		return "video"
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(pricing.Mode)), "image") || pricing.OutputCostPerImage > 0 {
		return "image"
	}
	if pricing.PerRequestPrice > 0 {
		return "per_request"
	}
	return "token"
}

func perRequestPriceFromLiteLLM(pricing *service.LiteLLMModelPricing) float64 {
	if pricing == nil {
		return 0
	}
	if pricing.PerRequestPrice > 0 {
		return pricing.PerRequestPrice
	}
	// 部分图片目录只提供每张图片价格；按次展示需要沿用同一默认价。
	return pricing.OutputCostPerImage
}

func perTokenToPerMillion(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return value * 1000000
}
