package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBillingServiceGrok45OfficialTokenPrices(t *testing.T) {
	service := NewBillingService(&config.Config{}, nil)
	cost, err := service.CalculateCost("grok-4.5", UsageTokens{
		InputTokens:     50_000,
		CacheReadTokens: 50_000,
		OutputTokens:    50_000,
	}, 1)

	require.NoError(t, err)
	require.InDelta(t, 0.1, cost.InputCost, 1e-12)
	require.InDelta(t, 0.015, cost.CacheReadCost, 1e-12)
	require.InDelta(t, 0.3, cost.OutputCost, 1e-12)
	require.InDelta(t, 0.415, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.415, cost.ActualCost, 1e-12)
}

func TestBillingServiceGrok46LongContextBoundaryAndSafeFallback(t *testing.T) {
	service := NewBillingService(&config.Config{}, nil)
	before, err := service.CalculateCost("grok-4.6", UsageTokens{InputTokens: 199999, OutputTokens: 1}, 1)
	require.NoError(t, err)
	at, err := service.CalculateCost("grok-4.6", UsageTokens{InputTokens: 150000, CacheReadTokens: 50000, OutputTokens: 100}, 1)
	require.NoError(t, err)
	require.False(t, before.LongContextBillingApplied)
	require.True(t, at.LongContextBillingApplied)
	require.InDelta(t, 0.6, at.InputCost, 1e-12)
	require.InDelta(t, 0.05, at.CacheReadCost, 1e-12)
	require.InDelta(t, 0.0012, at.OutputCost, 1e-12)

	for _, model := range []string{"grok-5", "x-ai/grok-7", "grok/grok-8"} {
		_, err := service.GetModelPricing(model)
		require.NoError(t, err)
	}
	for _, model := range []string{"grok-imagine-image", "grok-imagine-image-3.0", "grok-imagine-video-1.5", "grok-imagine-video-2", "grok-voice", "grok-speech-1", "grok-web-search", "grok-x-search"} {
		_, err := service.GetModelPricing(model)
		require.Error(t, err)
	}

	grok420, err := service.GetModelPricing("grok-4.20-0309-reasoning")
	require.NoError(t, err)
	require.InDelta(t, 1.25e-6, grok420.InputPricePerToken, 1e-15)
	composer, err := service.GetModelPricing("grok-composer-2.5-fast")
	require.NoError(t, err)
	require.InDelta(t, 1e-6, composer.InputPricePerToken, 1e-15)
}

func TestGrokUnknownTextFallbackRejectsMediaFamilies(t *testing.T) {
	for _, model := range []string{
		"grok-2-image-1212",
		"grok-2-video",
		"grok-2-audio",
		"grok-2-speech",
		"grok-2-tts",
		"grok-2-transcribe",
		"grok-2-realtime",
		"x-ai/grok-6-image",
	} {
		require.False(t, isGrokUnknownTextFamilyModel(model), model)
	}

	// Grok multimodal chat models remain token-priced; only unit-priced media
	// endpoints are excluded from the text fallback.
	require.True(t, isGrokUnknownTextFamilyModel("grok-2-vision-1212"))
}

func TestBillingServiceGrokMiniRequiresExactPricing(t *testing.T) {
	// v0.1.176 将 Mini 加入可路由目录，但没有提供可核对的独立价卡。
	// 本地静态兜底必须拒绝猜成 Grok 4.5，精确动态价卡存在时才允许计费。
	withoutCatalog := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"grok-3-mini", "grok-3-mini-fast", "grok-3-mini-unknown"} {
		_, err := withoutCatalog.GetModelPricing(model)
		require.Error(t, err, model)
		require.False(t, withoutCatalog.HasIdentifiedTokenPricing(model), model)
	}

	exactPricing := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"grok-3-mini": {
				InputCostPerToken:  0.25e-6,
				OutputCostPerToken: 0.50e-6,
				LiteLLMProvider:    "xai",
				Mode:               "chat",
				BillingMode:        "token",
			},
		},
		pricingOverrides: map[string]*LiteLLMModelPricing{},
	}
	withCatalog := NewBillingService(&config.Config{}, exactPricing)
	pricing, err := withCatalog.GetModelPricing("grok-3-mini")
	require.NoError(t, err)
	require.InDelta(t, 0.25e-6, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 0.50e-6, pricing.OutputPricePerToken, 1e-15)
	require.True(t, withCatalog.HasIdentifiedTokenPricing("grok-3-mini"))
}

func TestPricingServiceCatalogIncludesGrokTextAndMediaModels(t *testing.T) {
	service := &PricingService{
		pricingData:      map[string]*LiteLLMModelPricing{},
		pricingOverrides: map[string]*LiteLLMModelPricing{},
	}

	entries := service.ListModelPricingCatalog()
	byModel := make(map[string]ModelPricingCatalogEntry, len(entries))
	for _, entry := range entries {
		byModel[strings.ToLower(entry.Model)] = entry
	}

	require.InDelta(t, 2e-6, byModel["grok-4.5"].InputCostPerToken, 1e-15)
	require.InDelta(t, 6e-6, byModel["grok-4.5"].OutputCostPerToken, 1e-15)
	require.Equal(t, "image", byModel["grok-imagine-image"].BillingMode)
	require.InDelta(t, 0.02, byModel["grok-imagine-image"].OutputCostPerImage, 1e-12)
	require.Equal(t, "video", byModel["grok-imagine-video"].BillingMode)
	require.Equal(t, "video_generation", byModel["grok-imagine-video"].Mode)
	require.InDelta(t, 0.05, byModel["grok-imagine-video"].VideoPricePerSecond480P, 1e-12)
	require.InDelta(t, 0.05, byModel["grok-imagine-video"].VideoPricePerSecond720P, 1e-12)
	require.InDelta(t, 0.08, byModel["grok-imagine-video-1.5"].VideoPricePerSecond1080P, 1e-12)
}

func TestBillingServiceGrokImageOfficialPrices(t *testing.T) {
	service := NewBillingService(&config.Config{}, nil)

	standard := service.CalculateImageCost("grok-imagine-image", ImageBillingSize2K, 2, nil, 1.5)
	require.Equal(t, string(BillingModeImage), standard.BillingMode)
	require.InDelta(t, 0.04, standard.TotalCost, 1e-12)
	require.InDelta(t, 0.06, standard.ActualCost, 1e-12)

	quality := service.CalculateImageCost("grok-imagine-image-quality", ImageBillingSize2K, 2, nil, 1)
	require.InDelta(t, 0.10, quality.TotalCost, 1e-12)
	require.InDelta(t, 0.10, quality.ActualCost, 1e-12)

	qualityAlias := service.CalculateImageCost("grok-imagine-image-pro", ImageBillingSize2K, 2, nil, 1)
	require.InDelta(t, 0.10, qualityAlias.TotalCost, 1e-12)
}

func TestBillingServiceGrokVideoOfficialPrices(t *testing.T) {
	service := NewBillingService(&config.Config{}, nil)

	base := service.CalculateVideoCost("grok-imagine-video", VideoBillingResolution720P, 1, 10, nil, 1.5)
	require.Equal(t, string(BillingModeVideo), base.BillingMode)
	require.InDelta(t, 0.50, base.TotalCost, 1e-12)
	require.InDelta(t, 0.75, base.ActualCost, 1e-12)

	latest := service.CalculateVideoCost("grok-imagine-video-1.5", VideoBillingResolution1080P, 2, 8, nil, 1)
	require.InDelta(t, 1.28, latest.TotalCost, 1e-12)
	require.InDelta(t, 1.28, latest.ActualCost, 1e-12)

	latestAlias := service.CalculateVideoCost("grok-imagine-video-1.5-2026-05-30", VideoBillingResolution720P, 1, 10, nil, 1)
	require.InDelta(t, 0.80, latestAlias.TotalCost, 1e-12)

	unsupported := service.CalculateVideoCost("grok-imagine-video", VideoBillingResolution1080P, 1, 10, nil, 1)
	require.Zero(t, unsupported.TotalCost)
}

func TestBillingServiceGrokVideoPerSecondBoundaryMatrix(t *testing.T) {
	price480P := 0.01
	price720P := 0.02
	price1080P := 0.03
	prices := &VideoPriceConfig{
		Price480P:  &price480P,
		Price720P:  &price720P,
		Price1080P: &price1080P,
	}
	service := NewBillingService(&config.Config{}, nil)

	tests := []struct {
		name       string
		resolution string
		count      int
		duration   int
		multiplier float64
		wantTotal  float64
		wantActual float64
	}{
		{name: "zero video count is free", resolution: "720p", count: 0, duration: 15, multiplier: 2, wantTotal: 0, wantActual: 0},
		{name: "zero seconds uses upstream default eight", resolution: "480p", count: 1, duration: 0, multiplier: 1, wantTotal: 0.08, wantActual: 0.08},
		{name: "minimum one second", resolution: "720p", count: 1, duration: 1, multiplier: 1.5, wantTotal: 0.02, wantActual: 0.03},
		{name: "quantity duration resolution and multiplier multiply", resolution: "1080p", count: 3, duration: 2, multiplier: 2, wantTotal: 0.18, wantActual: 0.36},
		{name: "maximum fifteen seconds", resolution: "720p", count: 2, duration: 15, multiplier: 0.5, wantTotal: 0.60, wantActual: 0.30},
		{name: "duration above maximum clamps to fifteen", resolution: "1080p", count: 1, duration: 16, multiplier: 1, wantTotal: 0.45, wantActual: 0.45},
		{name: "negative multiplier clamps to zero", resolution: "480p", count: 2, duration: 8, multiplier: -1, wantTotal: 0.16, wantActual: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := service.CalculateVideoCost("grok-imagine-video-1.5", tt.resolution, tt.count, tt.duration, prices, tt.multiplier)
			require.InDelta(t, tt.wantTotal, cost.TotalCost, 1e-12)
			require.InDelta(t, tt.wantActual, cost.ActualCost, 1e-12)
			if tt.count > 0 {
				require.Equal(t, string(BillingModeVideo), cost.BillingMode)
			}
		})
	}
}

func TestBillingServiceGrokVideoUsesPricingOverride(t *testing.T) {
	pricing := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"grok-imagine-video": {
				LiteLLMProvider:         "xai",
				Mode:                    "video_generation",
				BillingMode:             "video",
				VideoPricePerSecond480P: 0.11,
				VideoPricePerSecond720P: 0.22,
			},
		},
		pricingOverrides: map[string]*LiteLLMModelPricing{},
	}
	service := NewBillingService(&config.Config{}, pricing)
	cost := service.CalculateVideoCost("grok-imagine-video", VideoBillingResolution720P, 1, 5, nil, 2)
	require.InDelta(t, 1.10, cost.TotalCost, 1e-12)
	require.InDelta(t, 2.20, cost.ActualCost, 1e-12)
}

func TestPricingServiceGetModelPricingFallsBackToOfficialGrokCatalog(t *testing.T) {
	service := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}
	pricing := service.GetModelPricing("grok-imagine-video-1.5")
	require.NotNil(t, pricing)
	require.Equal(t, "video", pricing.BillingMode)
	require.InDelta(t, 0.08, pricing.VideoPricePerSecond1080P, 1e-12)
}

func TestPricingServiceGetModelPricingCanonicalizesGrokAliases(t *testing.T) {
	service := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}

	text := service.GetModelPricing("grok-latest")
	require.NotNil(t, text)
	require.InDelta(t, 2e-6, text.InputCostPerToken, 1e-15)
	require.InDelta(t, 6e-6, text.OutputCostPerToken, 1e-15)

	video := service.GetModelPricing("grok-imagine-video-1.5-preview")
	require.NotNil(t, video)
	require.Equal(t, "video", video.BillingMode)
	require.InDelta(t, 0.08, video.VideoPricePerSecond480P, 1e-12)
	require.InDelta(t, 0.08, video.VideoPricePerSecond720P, 1e-12)
	require.InDelta(t, 0.08, video.VideoPricePerSecond1080P, 1e-12)

	quality := service.GetModelPricing("grok-imagine-image-quality-latest")
	require.NotNil(t, quality)
	require.Equal(t, "image", quality.BillingMode)
	require.InDelta(t, 0.05, quality.OutputCostPerImage, 1e-12)

	standard := service.GetModelPricing("grok-imagine-image-2026-03-02")
	require.NotNil(t, standard)
	require.InDelta(t, 0.02, standard.OutputCostPerImage, 1e-12)
}
