package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteModelPricesVendorAcrossReportAndAdminCatalog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pricing service.LiteLLMModelPricing
		vendor  string
	}{
		{
			name:    "provider_fallback",
			pricing: service.LiteLLMModelPricing{LiteLLMProvider: " \topenai "},
			vendor:  "openai",
		},
		{
			name: "provider_fallback_preserves_metadata",
			pricing: service.LiteLLMModelPricing{
				LiteLLMProvider: "openai", ModelVendor: " \t", MetadataSource: "pricing-catalog",
				InputModalities: []string{"text", "image"}, MaxInputTokens: 128000,
			},
			vendor: "openai",
		},
		{
			name: "explicit_vendor_wins",
			pricing: service.LiteLLMModelPricing{
				LiteLLMProvider: "relay", ModelVendor: "Original vendor", MetadataSource: "verified-catalog",
			},
			vendor: "Original vendor",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const model = "vendor-test-model"
			pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
			tc.pricing.Mode = "chat"
			tc.pricing.InputCostPerToken = 2e-6
			tc.pricing.OutputCostPerToken = 8e-6
			tc.pricing.PriceFields = []string{"input_cost_per_1m_tokens", "output_cost_per_1m_tokens"}
			_, err := pricingService.UpsertModelPricingOverride(model, tc.pricing)
			require.NoError(t, err)

			prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices([]string{model}, "gateway-a")
			require.Len(t, prices, 1)
			require.Equal(t, tc.vendor, prices[0].ModelVendor)

			// 主站收到配置变更后会主动回拉管理目录；覆盖整个合并出口，防止把厂商再次同步为空。
			router := gin.New()
			router.GET("/model-prices", adminhandler.NewChannelHandler(nil, nil, pricingService).ListModelPrices)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/model-prices?search="+model, nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			var body struct {
				Data struct {
					Items []gatewaylite.GatewayModelPrice `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			require.Len(t, body.Data.Items, 1)
			require.Equal(t, tc.vendor, body.Data.Items[0].ModelVendor)
			for _, price := range []gatewaylite.GatewayModelPrice{prices[0], body.Data.Items[0]} {
				require.Equal(t, model, price.Model)
				require.Equal(t, tc.pricing.InputModalities, price.InputModalities)
				require.Equal(t, tc.pricing.MaxInputTokens, price.MaxInputTokens)
				require.Equal(t, tc.pricing.MetadataSource, price.MetadataSource)
				require.Equal(t, tc.pricing.PriceFields, price.PriceFields)
				require.Equal(t, "token", price.BillingMode)
				require.InDelta(t, 2, price.InputCostPer1MTokens, 1e-12)
				require.InDelta(t, 8, price.OutputCostPer1MTokens, 1e-12)
			}
		})
	}
}

func TestGatewayLiteModelPricesVendorSurvivesCompatibleGroup(t *testing.T) {
	const model = "vendor-compatible-model"
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	_, err := pricingService.UpsertModelPricingOverride(model, service.LiteLLMModelPricing{
		LiteLLMProvider: "moonshot", Mode: "chat", InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6,
	})
	require.NoError(t, err)
	global := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices([]string{model}, "gateway-a")
	require.Len(t, global, 1)
	require.Equal(t, "moonshot", global[0].Provider)
	require.Equal(t, "moonshot", global[0].ModelVendor)

	group := gatewaylite.GatewayGroupSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, EffectiveModels: []string{model},
		BillingModelSource: service.BillingModelSourceRequested,
	}
	prices := buildGatewayLiteGroupModelPrices(group, global, nil, nil, nil)
	require.Len(t, prices, 1)
	require.Equal(t, service.PlatformOpenAI, prices[0].Provider)
	require.Equal(t, "moonshot", prices[0].ModelVendor)
	expected := global[0]
	expected.GatewayCode = ""
	expected.Provider = service.PlatformOpenAI
	require.Equal(t, expected, prices[0], "分组只调整协议平台，不改变厂商、价格或能力事实")
}

func TestGatewayLiteModelPricesExposeShortPriorityAndStandardLongContext(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	_, err := pricingService.UpsertModelPricingOverride("gpt-5.6-sol", service.LiteLLMModelPricing{
		LiteLLMProvider:                              "openai",
		Mode:                                         "chat",
		InputCostPerToken:                            5e-6,
		InputCostPerTokenPriority:                    10e-6,
		OutputCostPerToken:                           30e-6,
		OutputCostPerTokenPriority:                   60e-6,
		CacheCreationInputTokenCost:                  6.25e-6,
		CacheCreationInputTokenCostPriority:          12.5e-6,
		CacheReadInputTokenCost:                      0.5e-6,
		CacheReadInputTokenCostPriority:              1e-6,
		InputCostPerTokenAbove272K:                   10e-6,
		InputCostPerTokenPriorityAbove272K:           20e-6,
		OutputCostPerTokenAbove272K:                  45e-6,
		OutputCostPerTokenPriorityAbove272K:          90e-6,
		CacheCreationInputTokenCostAbove272K:         12.5e-6,
		CacheCreationInputTokenCostPriorityAbove272K: 25e-6,
		CacheReadInputTokenCostAbove272K:             1e-6,
		CacheReadInputTokenCostPriorityAbove272K:     2e-6,
		LongContextInputTokenThreshold:               272000,
		SupportsPromptCaching:                        true,
		SupportsServiceTier:                          true,
	})
	require.NoError(t, err)

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices([]string{"gpt-5.6-sol"}, "gateway-a")
	require.Len(t, prices, 1)
	price := prices[0]
	require.True(t, price.SupportsServiceTier)
	require.Equal(t, 272000, price.LongContextInputTokenThreshold)
	require.InDelta(t, 10, price.InputCostPer1MTokensPriority, 1e-12)
	require.InDelta(t, 12.5, price.CacheCreationCostPer1MTokensAbove272K, 1e-12)
	require.InDelta(t, 45, price.OutputCostPer1MTokensAbove272K, 1e-12)
	require.InDelta(t, 20, price.InputCostPer1MTokensPriorityAbove272K, 1e-12)
	require.InDelta(t, 25, price.CacheCreationCostPer1MTokensPriorityAbove272K, 1e-12)
	require.InDelta(t, 90, price.OutputCostPer1MTokensPriorityAbove272K, 1e-12)
	require.InDelta(t, 2, price.CacheReadCostPer1MTokensPriorityAbove272K, 1e-12)
}

func TestGatewayLiteModelPricesPreservePerRequestBilling(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	_, err := pricingService.UpsertModelPricingOverride("flat-request-model", service.LiteLLMModelPricing{
		LiteLLMProvider: "openai",
		Mode:            "responses",
		BillingMode:     "per_request",
		PerRequestPrice: 0.07,
	})
	require.NoError(t, err)

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices([]string{"flat-request-model"}, "gateway-a")
	require.Len(t, prices, 1)
	require.Equal(t, "per_request", prices[0].BillingMode)
	require.InDelta(t, 0.07, prices[0].PerRequestPrice, 1e-12)
	require.Zero(t, prices[0].OutputCostPerImage)
}

func TestGatewayLiteModelPricesPreserveImageInputPriceNullableState(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	explicitFree := 0.0
	_, err := pricingService.UpsertModelPricingOverride("image-input-free", service.LiteLLMModelPricing{
		LiteLLMProvider:        "openai",
		Mode:                   "responses",
		InputCostPerToken:      2e-6,
		InputCostPerImageToken: &explicitFree,
	})
	require.NoError(t, err)
	_, err = pricingService.UpsertModelPricingOverride("image-input-inherit", service.LiteLLMModelPricing{
		LiteLLMProvider:   "openai",
		Mode:              "responses",
		InputCostPerToken: 2e-6,
	})
	require.NoError(t, err)

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices([]string{"image-input-free", "image-input-inherit"}, "gateway-a")
	require.Len(t, prices, 2)
	byModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	for _, price := range prices {
		byModel[price.Model] = price
	}
	require.NotNil(t, byModel["image-input-free"].InputCostPerImageToken)
	require.Zero(t, *byModel["image-input-free"].InputCostPerImageToken)
	require.Nil(t, byModel["image-input-inherit"].InputCostPerImageToken)
}

func TestGatewayLiteModelPricesExposeGLMCaseVariantsAndIndependentKimiModels(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	models := []string{"GLM-5.2", "glm-5.2", "kimi-k3", "kimi-k3-code", "k3", "k3-256k"}

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices(models, "gateway-openai-compatible")
	require.Len(t, prices, 6)
	byModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	for _, price := range prices {
		byModel[price.Model] = price
	}

	for _, model := range []string{"GLM-5.2", "glm-5.2"} {
		price := byModel[model]
		require.Equal(t, "zai", price.Provider)
		require.InDelta(t, 1.4, price.InputCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.26, price.CacheReadCostPer1MTokens, 1e-12)
		require.InDelta(t, 4.4, price.OutputCostPer1MTokens, 1e-12)
		require.Zero(t, price.CacheCreationCostPer1MTokens)
	}
	for _, model := range []string{"kimi-k3", "kimi-k3-code", "k3", "k3-256k"} {
		price := byModel[model]
		require.Equal(t, "moonshot", price.Provider)
		require.InDelta(t, 3, price.InputCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.3, price.CacheReadCostPer1MTokens, 1e-12)
		require.InDelta(t, 15, price.OutputCostPer1MTokens, 1e-12)
		require.Zero(t, price.CacheCreationCostPer1MTokens)
	}
}

func TestGatewayLiteModelPricesExposeOfficialDomesticExactIDs(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	models := []string{
		"deepseek-v4-flash-0731",
		"deepseek-v4-flash-vip",
		"glm-5.1-vip",
		"glm-5.2-vip",
		"glm-5.2-vip1",
		"kimi-k3-vip",
		"kimi-k3-vip2",
		"kimi-k3-vip3",
		"minimax-m2.1",
		"minimax-m2.5",
		"qwen3.5-plus",
		"qwen3.8-max",
	}

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices(models, "gateway-domestic")
	require.Len(t, prices, len(models))
	byModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	for _, price := range prices {
		byModel[price.Model] = price
		require.Equal(t, "gateway-domestic", price.GatewayCode)
		require.Equal(t, "token", price.BillingMode)
	}

	for _, model := range []string{"deepseek-v4-flash-0731", "deepseek-v4-flash-vip"} {
		require.Equal(t, "deepseek", byModel[model].Provider)
		require.InDelta(t, 0.14, byModel[model].InputCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.0028, byModel[model].CacheReadCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.28, byModel[model].OutputCostPer1MTokens, 1e-12)
	}
	for _, model := range []string{"glm-5.1-vip", "glm-5.2-vip", "glm-5.2-vip1"} {
		require.Equal(t, "zai", byModel[model].Provider)
		require.InDelta(t, 1.4, byModel[model].InputCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.26, byModel[model].CacheReadCostPer1MTokens, 1e-12)
		require.InDelta(t, 4.4, byModel[model].OutputCostPer1MTokens, 1e-12)
	}
	for _, model := range []string{"kimi-k3-vip", "kimi-k3-vip2", "kimi-k3-vip3"} {
		require.Equal(t, "moonshot", byModel[model].Provider)
		require.InDelta(t, 3, byModel[model].InputCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.3, byModel[model].CacheReadCostPer1MTokens, 1e-12)
		require.InDelta(t, 15, byModel[model].OutputCostPer1MTokens, 1e-12)
	}
	for _, model := range []string{"minimax-m2.1", "minimax-m2.5"} {
		require.Equal(t, "minimax", byModel[model].Provider)
		require.InDelta(t, 0.3, byModel[model].InputCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.375, byModel[model].CacheCreationCostPer1MTokens, 1e-12)
		require.InDelta(t, 0.03, byModel[model].CacheReadCostPer1MTokens, 1e-12)
		require.InDelta(t, 1.2, byModel[model].OutputCostPer1MTokens, 1e-12)
	}
	qwen35 := byModel["qwen3.5-plus"]
	require.Equal(t, "qwen", qwen35.Provider)
	require.InDelta(t, 0.4, qwen35.InputCostPer1MTokens, 1e-12)
	require.InDelta(t, 0.5, qwen35.CacheCreationCostPer1MTokens, 1e-12)
	require.InDelta(t, 0.04, qwen35.CacheReadCostPer1MTokens, 1e-12)
	require.InDelta(t, 2.4, qwen35.OutputCostPer1MTokens, 1e-12)
	require.Equal(t, 256000, qwen35.LongContextInputTokenThreshold)
	require.InDelta(t, 0.5, qwen35.InputCostPer1MTokensAbove272K, 1e-12)
	require.InDelta(t, 0.625, qwen35.CacheCreationCostPer1MTokensAbove272K, 1e-12)
	require.InDelta(t, 0.05, qwen35.CacheReadCostPer1MTokensAbove272K, 1e-12)
	require.InDelta(t, 3, qwen35.OutputCostPer1MTokensAbove272K, 1e-12)

	qwen38 := byModel["qwen3.8-max"]
	require.Equal(t, "qwen", qwen38.Provider)
	require.InDelta(t, 2, qwen38.InputCostPer1MTokens, 1e-12)
	require.InDelta(t, 2.5, qwen38.CacheCreationCostPer1MTokens, 1e-12)
	require.InDelta(t, 0.25, qwen38.CacheReadCostPer1MTokens, 1e-12)
	require.InDelta(t, 6, qwen38.OutputCostPer1MTokens, 1e-12)
	require.Zero(t, qwen38.LongContextInputTokenThreshold)
}

func TestGatewayLiteModelPricesExposeGrokMediaAliasesAndOfficialPrices(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices([]string{
		"grok-imagine-image-quality-latest",
		"grok-imagine-image-pro",
		"grok-imagine-image-2026-03-02",
		"grok-imagine-video",
		"grok-imagine-video-1.5",
		"grok-imagine-video-1.5-preview",
		"grok-imagine-video-1.5-2026-05-30",
	}, "gateway-grok")
	require.Len(t, prices, 7)
	byModel := make(map[string]gatewaylite.GatewayModelPrice, len(prices))
	for _, price := range prices {
		byModel[price.Model] = price
	}

	require.InDelta(t, 0.05, byModel["grok-imagine-image-quality-latest"].OutputCostPerImage, 1e-12)
	require.InDelta(t, 0.05, byModel["grok-imagine-image-pro"].OutputCostPerImage, 1e-12)
	require.InDelta(t, 0.02, byModel["grok-imagine-image-2026-03-02"].OutputCostPerImage, 1e-12)

	base := byModel["grok-imagine-video"]
	require.Equal(t, "video", base.BillingMode)
	require.InDelta(t, 0.05, base.VideoPricePerSecond480P, 1e-12)
	require.InDelta(t, 0.05, base.VideoPricePerSecond720P, 1e-12)
	require.Zero(t, base.VideoPricePerSecond1080P)

	for _, model := range []string{
		"grok-imagine-video-1.5",
		"grok-imagine-video-1.5-preview",
		"grok-imagine-video-1.5-2026-05-30",
	} {
		price := byModel[model]
		require.Equal(t, "video", price.BillingMode)
		require.Equal(t, "video_generation", price.Mode)
		require.Equal(t, "xai", price.Provider)
		require.InDelta(t, 0.08, price.VideoPricePerSecond480P, 1e-12)
		require.InDelta(t, 0.08, price.VideoPricePerSecond720P, 1e-12)
		require.InDelta(t, 0.08, price.VideoPricePerSecond1080P, 1e-12)
		require.Zero(t, price.InputCostPer1MTokens)
		require.Zero(t, price.OutputCostPerImage)
		require.Zero(t, price.PerRequestPrice)
	}
}

// 上报清单里混入目录中没有的模型（生产实测：上游账号 credentials 里的 gpt-reserve）时，
// 只上报能查到价格的模型：不得把未知 gpt-* 套成「默认测试模型 gpt-5.4」的兜底价写进
// 主站价格镜像，也不要每次都打一条 pricing fallback 告警。
func TestGatewayLiteModelPricesSkipUnknownOpenAIModelInsteadOfDefaultFallback(t *testing.T) {
	pricingService := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
	_, err := pricingService.UpsertModelPricingOverride("gpt-5.6-sol", service.LiteLLMModelPricing{
		LiteLLMProvider:    "openai",
		Mode:               "chat",
		InputCostPerToken:  5e-6,
		OutputCostPerToken: 3e-5,
	})
	require.NoError(t, err)

	prices := (gatewayLiteModelPriceProvider{pricingService: pricingService}).GatewayModelPrices(
		[]string{"gpt-5.6-sol", "gpt-reserve", ""}, "gateway-a")

	require.Len(t, prices, 1, "目录里没有价格的模型不得凭空上报")
	require.Equal(t, "gpt-5.6-sol", prices[0].Model)
}
