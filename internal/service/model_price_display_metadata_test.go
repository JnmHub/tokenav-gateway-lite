package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseModelPriceDisplayMetadataPreservesDeclaredInputsAndFreePrices(t *testing.T) {
	svc := &PricingService{}
	parsed, err := svc.parsePricingDataWithFallback([]byte(`{
		"explicit-model": {"litellm_provider":"relay", "model_vendor":"Original vendor", "supported_modalities":["text","image","audio","video","image"], "max_input_tokens":1000000, "input_cost_per_token":0, "output_cost_per_token":0, "per_request_price":0},
		"vision-model": {"litellm_provider":"openai", "supports_vision":true, "mode":"chat", "input_cost_per_token":1e-6},
		"gpt-video-audio": {"litellm_provider":"openai", "mode":"chat", "input_cost_per_token":1e-6}
	}`), false)
	require.NoError(t, err)
	explicit := parsed["explicit-model"]
	require.Equal(t, "Original vendor", explicit.ModelVendor)
	require.Equal(t, []string{"text", "image", "audio", "video"}, explicit.InputModalities)
	require.Equal(t, 1000000, explicit.MaxInputTokens)
	require.Equal(t, "pricing-catalog", explicit.MetadataSource)
	require.Contains(t, explicit.PriceFields, "input_cost_per_1m_tokens")
	require.Contains(t, explicit.PriceFields, "output_cost_per_1m_tokens")
	require.Contains(t, explicit.PriceFields, "per_request_price")
	require.Zero(t, explicit.InputCostPerToken)
	require.Equal(t, []string{"image"}, parsed["vision-model"].InputModalities)
	require.Empty(t, parsed["vision-model"].ModelVendor, "provider（价格渠道）不能替代厂商")
	require.Empty(t, parsed["gpt-video-audio"].InputModalities, "模型名和 mode（类别）不能证明输入能力")
}

func TestModelPriceDisplayMetadataKeepsExistenceContractAcrossSerialization(t *testing.T) {
	for _, fields := range [][]string{nil, {}, {"input_cost_per_1m_tokens"}} {
		price := &LiteLLMModelPricing{LiteLLMProvider: "relay", PriceFields: fields}
		raw, err := json.Marshal(map[string]*LiteLLMModelPricing{"test-model": price})
		require.NoError(t, err)
		parsed, err := (&PricingService{}).parsePricingDataWithFallback(raw, false)
		require.NoError(t, err)
		require.Equal(t, fields, parsed["test-model"].PriceFields)
	}
}

func TestModelPriceDisplayMetadataAuthorityAndDerivedLongContextFields(t *testing.T) {
	parsed, err := (&PricingService{}).parsePricingDataWithFallback([]byte(`{
		"empty-model": {"price_fields":[], "input_cost_per_token":1e-6},
		"derived-model": {"input_cost_per_token":1e-6, "output_cost_per_token":2e-6, "long_context_input_token_threshold":272000, "long_context_input_cost_multiplier":2, "long_context_output_cost_multiplier":1.5}
	}`), false)
	require.NoError(t, err)
	require.NotNil(t, parsed["empty-model"].PriceFields)
	require.Empty(t, parsed["empty-model"].PriceFields, "显式空清单不能恢复旧残留正价")
	require.Contains(t, parsed["derived-model"].PriceFields, "input_cost_per_1m_tokens_above_272k")
	require.Contains(t, parsed["derived-model"].PriceFields, "output_cost_per_1m_tokens_above_272k")
}

func TestModelPriceDisplayMetadataUsesExactModelAndKeepsBaseMetadataOnPriceOverride(t *testing.T) {
	base := &LiteLLMModelPricing{ModelVendor: "Original vendor", InputModalities: []string{"text", "audio"}, MaxInputTokens: 64000, MetadataSource: "pricing-catalog"}
	svc := &PricingService{
		basePricingData: map[string]*LiteLLMModelPricing{"exact-model": base},
		pricingData:     map[string]*LiteLLMModelPricing{"exact-model": {InputCostPerToken: 2e-6}},
	}
	metadata := svc.GetModelPricingDisplayMetadata("exact-model")
	require.NotNil(t, metadata)
	require.Equal(t, "Original vendor", metadata.ModelVendor)
	require.Equal(t, []string{"text", "audio"}, metadata.InputModalities)
	require.Equal(t, 64000, metadata.MaxInputTokens)
	metadata.InputModalities[0] = "video"
	require.Equal(t, "text", base.InputModalities[0], "展示返回值不能修改源目录")
	require.Nil(t, svc.GetModelPricingDisplayMetadata("exact-model-custom"), "价格家族匹配不能确认别名能力")
}
