package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 独立审查模型的金额由已核对的固定目录钉住，同时验证三种真实服务装配。
func TestBillingCodexAutoReviewPricingSourcesAgree(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	embedded := &PricingService{}
	embedded.pricingData, err = embedded.parsePricingData(data)
	require.NoError(t, err)
	// 同一固定目录条目在静态快照与 nil 装配下也必须保持自己的价卡，不能回退通用 Codex。
	for source, pricingService := range map[string]*PricingService{
		"embedded": embedded,
		"static":   {},
		"nil":      nil,
	} {
		t.Run(source, func(t *testing.T) {
			svc := NewBillingService(&config.Config{}, pricingService)
			for _, model := range []string{"codex-auto-review", "openai/codex-auto-review"} {
				pricing, err := svc.GetModelPricing(model)
				require.NoError(t, err)
				require.InDelta(t, 5e-6, pricing.InputPricePerToken, 1e-12)
				require.InDelta(t, 30e-6, pricing.OutputPricePerToken, 1e-12)
				require.InDelta(t, 0.5e-6, pricing.CacheReadPricePerToken, 1e-12)
				require.Equal(t, 272000, pricing.LongContextInputThreshold)
				for _, total := range []int{272000, 272001} {
					for _, tier := range []string{"", "flex", "priority"} {
						tokens := UsageTokens{InputTokens: total - 1000, CacheReadTokens: 1000, OutputTokens: 100}
						cost, err := svc.CalculateCostWithServiceTier(model, tokens, 1.5, tier)
						require.NoError(t, err)
						inMult, outMult, tierMult := 1.0, 1.0, 1.0
						if total > 272000 {
							inMult, outMult = 2, 1.5
						}
						if tier == "flex" {
							tierMult = 0.5
						} else if tier == "priority" {
							tierMult = 2
						}
						require.InDelta(t, float64(tokens.InputTokens)*5e-6*inMult*tierMult, cost.InputCost, 1e-12)
						require.InDelta(t, float64(tokens.CacheReadTokens)*0.5e-6*inMult*tierMult, cost.CacheReadCost, 1e-12)
						require.InDelta(t, float64(tokens.OutputTokens)*30e-6*outMult*tierMult, cost.OutputCost, 1e-12)
						require.InDelta(t, cost.TotalCost*1.5, cost.ActualCost, 1e-12)
						require.Equal(t, total > 272000, cost.LongContextBillingApplied)
					}
				}
			}
		})
	}
}

// 三条价源共同支持的输入/缓存读/输出口径须一致；nil 专有缓存写兜底另由下方矩阵覆盖。
func TestBillingOpenAILongContextPricingSourcesAgree(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	embedded := &PricingService{}
	embedded.pricingData, err = embedded.parsePricingData(data)
	require.NoError(t, err)
	for _, model := range []string{"gpt-5.4", "gpt-5.5", "gpt-5.5-pro"} {
		for _, total := range []int{272000, 272001} {
			for _, tier := range []string{"", "flex", "priority"} {
				tokens := UsageTokens{InputTokens: total - 1000, CacheReadTokens: 1000, OutputTokens: 100}
				baseline, err := NewBillingService(&config.Config{}, embedded).CalculateCostWithServiceTier(model, tokens, 1.5, tier)
				require.NoError(t, err)
				for _, source := range []struct {
					name    string
					pricing *PricingService
				}{{name: "static", pricing: &PricingService{}}, {name: "nil"}} {
					cost, err := NewBillingService(&config.Config{}, source.pricing).CalculateCostWithServiceTier(model, tokens, 1.5, tier)
					require.NoError(t, err, "%s/%s/%d/%s", source.name, model, total, tier)
					require.InDelta(t, baseline.InputCost, cost.InputCost, 1e-12, "%s/%s/%d/%s input", source.name, model, total, tier)
					require.InDelta(t, baseline.CacheReadCost, cost.CacheReadCost, 1e-12, "%s/%s/%d/%s cache read", source.name, model, total, tier)
					require.InDelta(t, baseline.OutputCost, cost.OutputCost, 1e-12, "%s/%s/%d/%s output", source.name, model, total, tier)
					require.InDelta(t, baseline.ActualCost, cost.ActualCost, 1e-12, "%s/%s/%d/%s actual", source.name, model, total, tier)
					require.Equal(t, baseline.LongContextBillingApplied, cost.LongContextBillingApplied)
				}
			}
		}
	}
}

// nil PricingService 是真实装配兜底路径，必须独立覆盖阈值两侧与所有缓存 token。
func TestBillingNilPricingLongContextWholeSessionMatrix(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"gpt-5.4", "gpt-5.5", "gpt-5.5-pro"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.Equal(t, 272000, pricing.LongContextInputThreshold)
			if model == "gpt-5.5-pro" {
				require.InDelta(t, 3e-6, pricing.CacheReadPricePerToken, 1e-12)
				static := (&PricingService{}).GetModelPricing(model)
				require.InDelta(t, 3e-6, static.CacheReadInputTokenCost, 1e-12)
				require.InDelta(t, 6e-6, static.CacheReadInputTokenCostAbove272K, 1e-12)
			}
			for _, total := range []int{272000, 272001} {
				for _, tier := range []string{"", "flex", "priority"} {
					tokens := UsageTokens{InputTokens: total - 2000, CacheReadTokens: 1000, CacheCreationTokens: 1000, OutputTokens: 100}
					cost, err := svc.CalculateCostWithServiceTier(model, tokens, 1.5, tier)
					require.NoError(t, err)
					inMult, outMult, tierMult := 1.0, 1.0, 1.0
					if total > 272000 {
						inMult, outMult = 2, 1.5
					}
					if tier == "flex" {
						tierMult = 0.5
					} else if tier == "priority" {
						// 兜底服务沿现有业务档位倍率，GPT-5.5为2.5倍，其余2倍。
						tierMult = 2
						if model == "gpt-5.5" {
							tierMult = 2.5
						}
					}
					require.InDelta(t, float64(tokens.InputTokens)*pricing.InputPricePerToken*inMult*tierMult, cost.InputCost, 1e-12)
					require.InDelta(t, float64(tokens.CacheReadTokens)*pricing.CacheReadPricePerToken*inMult*tierMult, cost.CacheReadCost, 1e-12)
					require.InDelta(t, float64(tokens.CacheCreationTokens)*pricing.CacheCreationPricePerToken*inMult*tierMult, cost.CacheCreationCost, 1e-12)
					require.InDelta(t, float64(tokens.OutputTokens)*pricing.OutputPricePerToken*outMult*tierMult, cost.OutputCost, 1e-12)
					require.InDelta(t, cost.TotalCost*1.5, cost.ActualCost, 1e-12)
					require.Equal(t, total > 272000, cost.LongContextBillingApplied)
				}
			}
		})
	}
}
