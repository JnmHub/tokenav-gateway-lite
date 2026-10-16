package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCompositeBillingFailsClosedWithoutResolvedTargetPlatform(t *testing.T) {
	require.Empty(t, effectiveChannelPlatform(context.Background(), PlatformComposite))
}

func TestCompositeBillingUsesConcreteModelAndResolvedPlatformPrice(t *testing.T) {
	const groupID int64 = 701
	openAIInput := 2e-6
	openAIOutput := 4e-6
	anthropicInput := 90e-6
	anthropicOutput := 180e-6
	aliasOnlyOnAnthropic := 300e-6

	channel := &Channel{
		ID: 1, Name: "composite-billing", Status: StatusActive, GroupIDs: []int64{groupID},
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformOpenAI, Models: []string{"shared-model"}, BillingMode: BillingModeToken, InputPrice: &openAIInput, OutputPrice: &openAIOutput},
			{Platform: PlatformAnthropic, Models: []string{"shared-model"}, BillingMode: BillingModeToken, InputPrice: &anthropicInput, OutputPrice: &anthropicOutput},
			{Platform: PlatformAnthropic, Models: []string{"public-alias"}, BillingMode: BillingModeToken, InputPrice: &aliasOnlyOnAnthropic, OutputPrice: &aliasOnlyOnAnthropic},
		},
	}
	cache := newEmptyChannelCache()
	cache.loadedAt = time.Now()
	cache.channelByGroupID[groupID] = channel
	cache.groupPlatform[groupID] = PlatformComposite
	expandPricingToCache(cache, channel, groupID, PlatformComposite)
	channelService := NewChannelService(nil, nil, nil, nil)
	channelService.cache.Store(cache)
	billingService := NewBillingService(&config.Config{}, nil)
	svc := &GatewayService{billingService: billingService, resolver: NewModelPricingResolver(channelService, billingService)}

	ctx := WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
		Matched: true, PublicModel: "public-alias", TargetPlatform: PlatformOpenAI, UpstreamModel: "shared-model",
	})
	apiKey := &APIKey{GroupID: i64p(groupID), Group: &Group{ID: groupID, Platform: PlatformComposite}}
	billingModel := svc.compositeBillableModel(ctx, apiKey, "public-alias", "shared-model")
	require.Equal(t, "shared-model", billingModel, "其他平台的同名别名价格不得被采用")

	cost := svc.calculateRecordUsageCost(ctx, &ForwardResult{
		Model: "public-alias", UpstreamModel: "shared-model",
		Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 100},
	}, apiKey, billingModel, 1, 1, nil)
	require.NotNil(t, cost)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
	require.InDelta(t, 0.0024, cost.ActualCost, 1e-12, "必须采用 OpenAI 真实模型价格，而不是 Anthropic 同名价格")
}
