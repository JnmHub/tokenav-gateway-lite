//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfiguredServiceTierMultiplier(t *testing.T) {
	fast := 2.5
	flex := 0.4
	pricing := &ModelPricing{FastMultiplier: &fast, FlexMultiplier: &flex}
	require.InDelta(t, 2.5, configuredServiceTierMultiplier("fast", pricing), 1e-12)
	require.InDelta(t, 2.5, configuredServiceTierMultiplier("priority", pricing), 1e-12)
	require.InDelta(t, 0.4, configuredServiceTierMultiplier("flex", pricing), 1e-12)
}

func TestPriorityExplicitPricesDoNotDependOnCapabilityFlag(t *testing.T) {
	pricing := &ModelPricing{
		InputPricePerToken:          1,
		InputPricePerTokenPriority:  3,
		OutputPricePerToken:         2,
		OutputPricePerTokenPriority: 4,
		SupportsServiceTier:         false,
	}

	for _, serviceTier := range []string{"priority", "fast"} {
		t.Run(serviceTier, func(t *testing.T) {
			require.True(t, usePriorityServiceTierPricing(serviceTier, pricing))
			require.NoError(t, validateOpenAITokenPricing(
				"catalog-fast-model",
				UsageTokens{InputTokens: 1, OutputTokens: 1},
				serviceTier,
				pricing,
				false,
			))
			cost := (&BillingService{}).computeTokenBreakdown(
				pricing,
				UsageTokens{InputTokens: 2, OutputTokens: 3},
				1,
				serviceTier,
				false,
			)
			require.InDelta(t, 6, cost.InputCost, 1e-12)
			require.InDelta(t, 12, cost.OutputCost, 1e-12)
		})
	}
}

func TestPriorityConfiguredMultiplierSkipsCatalogPriceValidation(t *testing.T) {
	fast := 2.5
	pricing := &ModelPricing{
		InputPricePerToken:  1,
		OutputPricePerToken: 2,
		FastMultiplier:      &fast,
	}

	for _, serviceTier := range []string{"priority", "fast"} {
		t.Run(serviceTier, func(t *testing.T) {
			require.False(t, usePriorityServiceTierPricing(serviceTier, pricing))
			require.NoError(t, validateOpenAITokenPricing(
				"channel-fast-model",
				UsageTokens{InputTokens: 1, OutputTokens: 1},
				serviceTier,
				pricing,
				false,
			))
			cost := (&BillingService{}).computeTokenBreakdown(
				pricing,
				UsageTokens{InputTokens: 2, OutputTokens: 3},
				1,
				serviceTier,
				false,
			)
			require.InDelta(t, 5, cost.InputCost, 1e-12)
			require.InDelta(t, 15, cost.OutputCost, 1e-12)
		})
	}
}

func TestOpenAIPriorityPreflightUsesExplicitPricesWithoutCapabilityFlag(t *testing.T) {
	pricing := &ModelPricing{
		InputPricePerTokenPriority:  3,
		OutputPricePerTokenPriority: 4,
		SupportsServiceTier:         false,
	}
	service := &OpenAIGatewayService{
		billingService: &BillingService{fallbackPrices: map[string]*ModelPricing{
			"gpt-5.3-codex": pricing,
		}},
	}

	require.True(t, service.openAIModelSupportsPriority("gpt-5.3-codex"))
	ready, reason := service.openAIModelPriorityPricingReady("gpt-5.3-codex", false)
	require.True(t, ready)
	require.Empty(t, reason)
}

func TestAnthropicSpeedServiceTier(t *testing.T) {
	account := &Account{Platform: PlatformAnthropic}
	for _, model := range []string{"claude-opus-5", "claude-opus-4-8", "claude-opus-4.8"} {
		tier := anthropicSpeedServiceTier(account, "fast", model)
		require.NotNil(t, tier)
		require.Equal(t, "fast", *tier)
	}
	require.Nil(t, anthropicSpeedServiceTier(account, "fast", "claude-opus-4-7"))
	require.Nil(t, anthropicSpeedServiceTier(&Account{Platform: PlatformOpenAI}, "fast", "claude-opus-5"))
	require.Nil(t, anthropicSpeedServiceTier(&Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}, "fast", "claude-opus-5"))
}

func TestV0179BuiltInPrices(t *testing.T) {
	service := &BillingService{fallbackPrices: make(map[string]*ModelPricing)}
	service.initFallbackPricing()

	gpt55, err := service.GetModelPricing("gpt-5.5")
	require.NoError(t, err)
	require.InDelta(t, 5e-6, gpt55.InputPricePerToken, 1e-12)
	require.InDelta(t, 30e-6, gpt55.OutputPricePerToken, 1e-12)

	pro, err := service.GetModelPricing("gpt-5.5-pro")
	require.NoError(t, err)
	require.InDelta(t, 30e-6, pro.InputPricePerToken, 1e-12)
	require.InDelta(t, 180e-6, pro.OutputPricePerToken, 1e-12)

	for _, model := range []string{"claude-opus-4.8", "claude-opus-5"} {
		pricing, err := service.GetModelPricing(model)
		require.NoError(t, err)
		require.InDelta(t, 2, pricing.InputPricePerTokenPriority/pricing.InputPricePerToken, 1e-12)
		require.InDelta(t, 2, pricing.OutputPricePerTokenPriority/pricing.OutputPricePerToken, 1e-12)
	}
}
