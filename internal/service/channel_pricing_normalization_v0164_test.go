package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeChannelPricingModelNameV0164(t *testing.T) {
	require.Equal(t, "claude-opus-4-8", normalizeChannelPricingModelName("  Claude-Opus-4.8  "))
	require.Equal(t, "gpt-5.6-sol", normalizeChannelPricingModelName(" GPT-5.6-SOL "))
}

func TestLookupChannelPricingNormalizesAnthropicVersionSeparator(t *testing.T) {
	price := &ChannelModelPricing{Platform: PlatformAnthropic, Models: []string{"claude-opus-4.8"}}
	cache := newEmptyChannelCache()
	cache.pricingByGroupModel[channelModelKey{groupID: 1, platform: PlatformAnthropic, model: normalizeChannelPricingModelName("claude-opus-4.8")}] = price
	require.Same(t, price, lookupPricingAcrossPlatforms(cache, 1, PlatformAnthropic, "claude-opus-4-8"))
}
