package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDefaultPricingFileLongContextShapeIsHonored 守住「价格文件的两种长上下文形状都必须被解析」。
//
// 背景：内置价格文件存在两种等价形状——
//   - 老形状：直接铺开 *_above_272k_tokens 绝对价；
//   - 新形状（官方 v0.2.13 起，也用于 gpt-5.6/gpt-6 这批新模型）：只给
//     long_context_input_token_threshold 与 long_context_{input,output}_cost_multiplier。
//
// 解析器一度只认老形状，于是新形状模型的阈值恒为 0：长上下文整段会话加价静默失效
// （少收费用），>272K 绝对价字段为空还会让 Priority 的 fail-closed 完整性校验把这些
// 模型误判成「价格不完整」。这条用例对文件里**每一条**声明了 long_context_ 阈值的条目
// 做通用断言，以后任何一方改形状都会被立刻拦住。
func TestDefaultPricingFileLongContextShapeIsHonored(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))

	svc := &PricingService{}
	parsed, err := svc.parsePricingData(body)
	require.NoError(t, err)

	type rawEntry struct {
		Threshold *int     `json:"long_context_input_token_threshold"`
		InMult    *float64 `json:"long_context_input_cost_multiplier"`
		OutMult   *float64 `json:"long_context_output_cost_multiplier"`
		InBase    *float64 `json:"input_cost_per_token"`
		OutBase   *float64 `json:"output_cost_per_token"`
		InAbove   *float64 `json:"input_cost_per_token_above_272k_tokens"`
		OutAbove  *float64 `json:"output_cost_per_token_above_272k_tokens"`
	}

	checked := 0
	for model, rawModel := range raw {
		var entry rawEntry
		require.NoError(t, json.Unmarshal(rawModel, &entry))
		if entry.Threshold == nil || *entry.Threshold <= 0 {
			continue
		}
		checked++

		got := parsed[model]
		require.NotNil(t, got, "价格文件声明了长上下文阈值，但解析后没有该模型: %s", model)
		require.Equal(t, *entry.Threshold, got.LongContextInputTokenThreshold,
			"长上下文阈值没有从文件读入: %s", model)
		require.Greater(t, got.LongContextInputCostMultiplier, 0.0,
			"长上下文输入倍率缺失，长上下文加价会静默失效: %s", model)
		require.Greater(t, got.LongContextOutputCostMultiplier, 0.0,
			"长上下文输出倍率缺失，长上下文加价会静默失效: %s", model)

		// 文件没给绝对价时，必须由「基础价 × 倍率」补齐，下游（计价与 Priority 完整性校验）
		// 只读绝对价字段；文件给了绝对价时以文件为准，不做覆盖。
		if entry.InMult != nil && entry.InBase != nil && entry.InAbove == nil {
			require.InDelta(t, *entry.InBase**entry.InMult, got.InputCostPerTokenAbove272K, 1e-15,
				"输入 >272K 绝对价没有按倍率补齐: %s", model)
		}
		if entry.OutMult != nil && entry.OutBase != nil && entry.OutAbove == nil {
			require.InDelta(t, *entry.OutBase**entry.OutMult, got.OutputCostPerTokenAbove272K, 1e-15,
				"输出 >272K 绝对价没有按倍率补齐: %s", model)
		}
	}
	require.Greater(t, checked, 0, "价格文件里没有声明 long_context_ 阈值的条目，本护栏失去意义")
}
