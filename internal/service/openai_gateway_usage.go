package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_gateway_usage.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)


// ResolveUserGroupRateMultiplier resolves the same cached multiplier used by OpenAI usage billing.
func (s *OpenAIGatewayService) ResolveUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	if s == nil {
		return groupDefaultMultiplier
	}
	resolver := s.userGroupRateResolver
	if resolver == nil {
		resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
	}
	// [tokenav 定制] 我们的 Resolve 返回 (float64, error)，出错时回退默认倍率。
	if multiplier, err := resolver.Resolve(ctx, userID, groupID, groupDefaultMultiplier); err == nil {
		return multiplier
	}
	return groupDefaultMultiplier
}

// openAIUsagePricingAt 返回本次用量记录使用的定价时刻：优先请求级 PricingAt
// （与利润门 D 同源同刻），未装配时回退记录时刻（既有行为）。
func openAIUsagePricingAt(input *OpenAIRecordUsageInput) time.Time {
	if input != nil && !input.PricingAt.IsZero() {
		return input.PricingAt
	}
	return timezone.Now()
}

func groupBillsOpenAIFastAtStandard(apiKey *APIKey, account *Account, serviceTier string) bool {
	if apiKey == nil || apiKey.Group == nil || !apiKey.Group.FreeOpenAIFast {
		return false
	}
	if account == nil || !account.IsOpenAI() {
		return false
	}
	if !groupSupportsOpenAIFast(apiKey.Group.Platform) {
		return false
	}
	switch normalizeBillingServiceTier(serviceTier) {
	case "priority", "fast":
		return true
	default:
		return false
	}
}

// filterCNProviderBillingModelCandidates 过滤国产供应商（kimi/zhipu/deepseek）
// 账号的计费候选模型名：claude-* 候选仅在运营者显式配置了分组/渠道定价时保留。
//
// 背景：候选链的兜底候选含客户端请求的原始模型名。CN 上游的 Anthropic 兼容端点
// 接受 claude-* 模型名但从不真正服务 Claude 模型；若放行，目录里的 Claude 价卡
// 与 getFallbackPricing 的 "claude"→Sonnet 统一兜底会把 CN 流量按 Claude 原价
// （数倍～数十倍）静默误计，且 usage 日志显示的正是 claude-* 名，无从察觉。
// 候选全部落空时走既有的零成本+告警路径（openai_usage.pricing_missing_record_
// zero_cost），与定价层「未知型号不回退以避免误计价」的既有设计意图一致；
// 运营者的修复手段是配置账号级 model_mapping（映射到已定价的 CN 模型）或
// 分组/渠道显式定价。
func (s *OpenAIGatewayService) filterCNProviderBillingModelCandidates(ctx context.Context, account *Account, apiKey *APIKey, candidates []string) []string {
	if account == nil || (!account.IsCNProvider() && !account.IsOpenCodeGo()) {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if strings.Contains(strings.ToLower(trimmed), "claude") &&
			s.resolveOpenAIChannelPricing(ctx, trimmed, apiKey) == nil {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func cloneImageSizeBreakdown(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

