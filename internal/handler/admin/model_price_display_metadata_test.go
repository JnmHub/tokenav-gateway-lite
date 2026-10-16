package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestModelPriceDisplayMetadataAdminResponseAndZeroRequestFields(t *testing.T) {
	zero := 0.0
	fields := modelPriceRequestDisplayFields(upsertModelPriceRequest{PerRequestPrice: &zero, VideoPricePerSecond480P: &zero})
	require.Equal(t, []string{"per_request_price", "video_price_per_second_480p"}, fields)
	resp := modelPriceCatalogEntryToResponse(service.ModelPricingCatalogEntry{Model: "exact-model", LiteLLMProvider: "relay", ModelVendor: "Original vendor", InputModalities: []string{"text", "image"}, MaxInputTokens: 128000, MetadataSource: "verified-catalog", PriceFields: fields})
	require.Equal(t, "relay", resp.Provider)
	require.Equal(t, "Original vendor", resp.ModelVendor)
	require.Equal(t, []string{"text", "image"}, resp.InputModalities)
	require.Equal(t, 128000, resp.MaxInputTokens)
	require.Equal(t, "verified-catalog", resp.MetadataSource)
	require.Equal(t, fields, resp.PriceFields)
	require.NotContains(t, fields, "input_cost_per_1m_tokens", "未配置默认零不能表示免费")
}
