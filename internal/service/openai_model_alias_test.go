package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeKnownOpenAICodexModelBareGPT56RoutesToSol(t *testing.T) {
	tests := map[string]string{
		"gpt-5.6":            "gpt-5.6-sol",
		"openai/gpt-5.6":     "gpt-5.6-sol",
		"gpt5.6":             "gpt-5.6-sol",
		"gpt-5.6-high":       "gpt-5.6-sol",
		"gpt-5.6-max":        "gpt-5.6-sol",
		"gpt-5.6-2026-07-09": "gpt-5.6-sol",
		"openai/gpt-5.6-max": "gpt-5.6-sol",
	}

	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, expected, normalizeKnownOpenAICodexModel(input))
			require.True(t, isOpenAIGPT56Model(input))
			require.Equal(t, expected, normalizeModelNameForPricing(input))
		})
	}
}

func TestNormalizeKnownOpenAICodexModelGPT6Astra(t *testing.T) {
	tests := map[string]string{
		"gpt-6-astra":          "gpt-6-astra",
		"openai/gpt-6-astra":   "gpt-6-astra",
		"gpt6astra":            "gpt-6-astra",
		"gpt-6-astra-high":     "gpt-6-astra",
		"gpt-6-astra-max":      "gpt-6-astra",
		"gpt-6-astra-20260903": "gpt-6-astra",
		// astar 是 astra 的常见误记，必须收敛到同一计价型号。
		"gpt-6-astar":        "gpt-6-astra",
		"openai/gpt-6-astar": "gpt-6-astra",
	}

	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			require.Equal(t, expected, normalizeKnownOpenAICodexModel(input))
			require.True(t, isOpenAIGPT56Model(input))
			require.Equal(t, expected, normalizeModelNameForPricing(input))
		})
	}
}

// 裸 gpt-6 前缀与未知 gpt-6 型号拒绝计价归一，防止未来 gpt-6-mini 等
// 小型号被误按 Astra 旗舰价超收。
func TestNormalizeKnownOpenAICodexModelRejectsUnknownGPT6Variant(t *testing.T) {
	for _, input := range []string{"gpt-6", "gpt-6-mini", "gpt-6-pro-preview", "gpt-6-turbo"} {
		require.Equal(t, "", normalizeKnownOpenAICodexModel(input), input)
	}
}

func TestUsageBillingModelCandidatesBareGPT56IncludesSol(t *testing.T) {
	require.Equal(t,
		[]string{"gpt-5.6", "gpt-5.6-sol"},
		usageBillingModelCandidates("gpt-5.6"),
	)
	require.Equal(t,
		[]string{"openai/gpt-5.6", "gpt-5.6", "gpt-5.6-sol"},
		usageBillingModelCandidates("openai/gpt-5.6"),
	)
}

func TestNormalizeKnownOpenAICodexModelRejectsUnknownGPT56Variant(t *testing.T) {
	require.Empty(t, normalizeKnownOpenAICodexModel("gpt-5.6-unknown"))
}
