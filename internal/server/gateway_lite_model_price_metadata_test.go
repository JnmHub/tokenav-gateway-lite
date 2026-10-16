package server

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteModelPriceReportPreservesIndependentMetadataAndZeroPriceFields(t *testing.T) {
	svc := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	_, err := svc.UpsertModelPricingOverride("exact-model", service.LiteLLMModelPricing{
		LiteLLMProvider: "relay", ModelVendor: "Original vendor", Mode: "chat", InputModalities: []string{"text", "image", "audio", "video"},
		MaxInputTokens: 128000, MetadataSource: "verified-catalog", PriceFields: []string{"input_cost_per_1m_tokens", "output_cost_per_1m_tokens"},
	})
	require.NoError(t, err)
	prices := (gatewayLiteModelPriceProvider{pricingService: svc}).GatewayModelPrices([]string{"exact-model"}, "gateway-a")
	require.Len(t, prices, 1)
	require.Equal(t, "relay", prices[0].Provider)
	require.Equal(t, "Original vendor", prices[0].ModelVendor)
	require.Equal(t, []string{"text", "image", "audio", "video"}, prices[0].InputModalities)
	require.Equal(t, 128000, prices[0].MaxInputTokens)
	require.Equal(t, "verified-catalog", prices[0].MetadataSource)
	require.Contains(t, prices[0].PriceFields, "input_cost_per_1m_tokens")
	require.Zero(t, prices[0].InputCostPer1MTokens)
}

func TestGatewayLiteGroupDisplayPriceFieldsAuthorityAndUnknownCatalog(t *testing.T) {
	for _, fields := range [][]string{nil, {}} {
		base := gatewaylite.GatewayModelPrice{PriceFields: fields, InputCostPer1MTokens: 5}
		price := applyGatewayLiteGroupPricingOverride(base, true, service.ChannelModelPricing{BillingMode: service.BillingModeToken})
		if fields == nil {
			require.Contains(t, price.PriceFields, "input_cost_per_1m_tokens")
		} else {
			require.NotNil(t, price.PriceFields)
			require.Empty(t, price.PriceFields, "显式空清单不能被残留正价扩大")
		}
	}
}

func TestGatewayLiteGroupDisplayPriceFieldsPreserveFreeOverridesAcrossModes(t *testing.T) {
	zero := 0.0
	base := gatewaylite.GatewayModelPrice{ModelVendor: "Original vendor", InputModalities: []string{"text"}, PriceFields: []string{"input_cost_per_1m_tokens"}, InputCostPer1MTokens: 2}
	for _, mode := range []service.BillingMode{service.BillingModeToken, service.BillingModePerRequest, service.BillingModeImage, service.BillingModeVideo} {
		t.Run(string(mode), func(t *testing.T) {
			price := applyGatewayLiteGroupPricingOverride(base, true, service.ChannelModelPricing{BillingMode: mode, InputPrice: &zero, PerRequestPrice: &zero})
			require.Equal(t, "Original vendor", price.ModelVendor)
			require.Equal(t, []string{"text"}, price.InputModalities)
			if mode == service.BillingModeToken {
				require.Contains(t, price.PriceFields, "input_cost_per_1m_tokens")
			} else {
				require.NotContains(t, price.PriceFields, "input_cost_per_1m_tokens")
			}
			switch mode {
			case service.BillingModePerRequest:
				require.Contains(t, price.PriceFields, "per_request_price")
			case service.BillingModeImage:
				require.Contains(t, price.PriceFields, "output_cost_per_image")
			case service.BillingModeVideo:
				require.Contains(t, price.PriceFields, "video_price_per_second_480p")
				require.Contains(t, price.PriceFields, "video_price_per_second_1080p")
			}
		})
	}
}
