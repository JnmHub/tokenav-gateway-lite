package server

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildGatewayLiteGroupModelPricesMergesInheritedAndCustomPricesWithoutRouteLeak(t *testing.T) {
	perRequest := 0.07
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, BillingModelSource: service.BillingModelSourceRequested,
		EffectiveModels: []string{"public-chat", "public-image"}, EffectiveModelsReported: true,
		ModelMapping: map[string]string{"public-chat": "secret-upstream-model"},
	}
	global := []gatewaylite.GatewayModelPrice{
		{Model: "public-chat", Provider: "openai", BillingMode: "token", InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8, GatewayCode: "secret-node"},
		{Model: "public-image", Provider: "openai", BillingMode: "image", OutputCostPerImage: 0.02},
	}
	channel := &service.Channel{Status: service.StatusActive, ModelPricing: []service.ChannelModelPricing{{
		Platform: service.PlatformOpenAI, Models: []string{"public-image"}, BillingMode: service.BillingModeImage, PerRequestPrice: &perRequest,
	}}}

	prices := buildGatewayLiteGroupModelPrices(group, global, nil, channel, nil)
	require.Len(t, prices, 2)
	byModel := map[string]gatewaylite.GatewayModelPrice{prices[0].Model: prices[0], prices[1].Model: prices[1]}
	require.Equal(t, 2.0, byModel["public-chat"].InputCostPer1MTokens)
	require.Equal(t, "", byModel["public-chat"].GatewayCode)
	require.Equal(t, "image", byModel["public-image"].BillingMode)
	require.Equal(t, perRequest, byModel["public-image"].PerRequestPrice)
	require.Equal(t, perRequest, byModel["public-image"].OutputCostPerImage)
	for _, price := range prices {
		require.NotEqual(t, "secret-upstream-model", price.Model)
		require.NotContains(t, price.Source, "channel-")
	}
}

func TestBuildGatewayLiteGroupModelPricesPreservesCompositeProviderAndVideoTiers(t *testing.T) {
	price480, price720, price1080 := 0.08, 0.12, 0.2
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 9, Platform: service.PlatformComposite, EffectiveModels: []string{"video-public"}, EffectiveModelsReported: true,
	}
	routes := []service.CompositeModelRoute{{
		ID: 1, GroupID: 9, PublicModel: "video-public", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformGrok, UpstreamModel: "secret-video", Endpoint: service.CompositeRouteEndpointAny, Enabled: true,
	}}
	channel := &service.Channel{Status: service.StatusActive, RestrictModels: true, ModelPricing: []service.ChannelModelPricing{{
		Platform: service.PlatformGrok, Models: []string{"video-public"}, BillingMode: service.BillingModeVideo,
		Intervals: []service.PricingInterval{
			{TierLabel: "480p", PerRequestPrice: &price480},
			{TierLabel: "720p", PerRequestPrice: &price720},
			{TierLabel: "1080p", PerRequestPrice: &price1080},
		},
	}}}

	prices := buildGatewayLiteGroupModelPrices(group, nil, routes, channel, nil)
	require.Len(t, prices, 1)
	require.Equal(t, "video-public", prices[0].Model)
	require.Equal(t, service.PlatformGrok, prices[0].Provider)
	require.Equal(t, "video", prices[0].BillingMode)
	require.Equal(t, price480, prices[0].VideoPricePerSecond480P)
	require.Equal(t, price720, prices[0].VideoPricePerSecond720P)
	require.Equal(t, price1080, prices[0].VideoPricePerSecond1080P)
	require.NotContains(t, prices[0].Model, "secret")
	require.Equal(t, []string{"video-public"}, gatewayLiteGroupRestrictedModels(group, group.EffectiveModels, nil, routes, channel, nil))
}

func TestBuildGatewayLiteGroupModelPricesOmitsAmbiguousCompositeRoute(t *testing.T) {
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 12, Platform: service.PlatformComposite,
		EffectiveModels: []string{"shared-model"}, EffectiveModelsReported: true,
	}
	routes := []service.CompositeModelRoute{
		{ID: 1, GroupID: 12, PublicModel: "shared-model", MatchType: service.CompositeRouteMatchExact,
			TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5", Endpoint: service.CompositeRouteEndpointAny, Enabled: true},
		{ID: 2, GroupID: 12, PublicModel: "shared-model", MatchType: service.CompositeRouteMatchExact,
			TargetPlatform: service.PlatformAnthropic, UpstreamModel: "claude-sonnet", Endpoint: service.CompositeRouteEndpointMessages, Enabled: true},
	}
	global := []gatewaylite.GatewayModelPrice{
		{Model: "gpt-5", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 1, OutputCostPer1MTokens: 2},
		{Model: "claude-sonnet", Provider: service.PlatformAnthropic, BillingMode: "token", InputCostPer1MTokens: 3, OutputCostPer1MTokens: 4},
	}

	require.Empty(t, buildGatewayLiteGroupModelPrices(group, global, routes, nil, nil))
	require.Empty(t, gatewayLiteGroupRestrictedModels(group, group.EffectiveModels, global, routes, &service.Channel{
		Status: service.StatusActive, RestrictModels: true,
		ModelPricing: []service.ChannelModelPricing{{
			Platform: service.PlatformOpenAI, Models: []string{"shared-model"}, BillingMode: service.BillingModeToken,
			InputPrice: testFloatPtr(0.000001), OutputPrice: testFloatPtr(0.000002),
		}},
	}, nil))
}

func testFloatPtr(value float64) *float64 { return &value }

func TestGatewayLiteGroupRestrictedModelsIncludesInheritedAndCustomContracts(t *testing.T) {
	price := 0.01
	group := gatewaylite.GatewayGroupSnapshot{ID: 11, Platform: service.PlatformOpenAI, BillingModelSource: service.BillingModelSourceRequested}
	channel := &service.Channel{Status: service.StatusActive, RestrictModels: true, ModelPricing: []service.ChannelModelPricing{{
		Platform: service.PlatformOpenAI, Models: []string{"allowed-*"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &price,
	}}}
	global := []gatewaylite.GatewayModelPrice{{
		Model: "inherited-one", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 1,
	}, {
		Model: "disabled-one", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 1, Disabled: true,
	}}
	require.Equal(t, []string{"allowed-one", "inherited-one"}, gatewayLiteGroupRestrictedModels(
		group, []string{"allowed-one", "blocked-one", "disabled-one", "inherited-one"}, global, nil, channel, nil,
	))
}

func TestBuildGatewayLiteGroupModelPricesUnrestrictedMergesCatalogAndCustomOnlyModels(t *testing.T) {
	perRequest := 0.03
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 13, Platform: service.PlatformOpenAI, BillingModelSource: service.BillingModelSourceRequested,
		EffectiveModels: []string{"catalog-model", "custom-model", "missing-model"},
	}
	global := []gatewaylite.GatewayModelPrice{{
		Model: "catalog-model", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 1,
	}}
	channel := &service.Channel{Status: service.StatusActive, RestrictModels: false, ModelPricing: []service.ChannelModelPricing{{
		Platform: service.PlatformOpenAI, Models: []string{"custom-model"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &perRequest,
	}}}

	prices := buildGatewayLiteGroupModelPrices(group, global, nil, channel, nil)
	require.Len(t, prices, 2)
	require.Equal(t, []string{"catalog-model", "custom-model"}, []string{prices[0].Model, prices[1].Model})
}

func TestBuildGatewayLiteGroupModelPricesFailsClosedForAmbiguousAccountMappings(t *testing.T) {
	accounts := []gatewaylite.GatewayAccountSnapshot{
		gatewayLitePricingAccount(1, 14, map[string]any{"mapped-model": "upstream-a"}),
		gatewayLitePricingAccount(2, 14, map[string]any{"mapped-model": "upstream-b"}),
	}
	global := []gatewaylite.GatewayModelPrice{
		{Model: "mapped-model", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 1},
		{Model: "upstream-a", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 2},
		{Model: "upstream-b", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 3},
	}
	for _, source := range []string{
		service.BillingModelSourceUpstream,
		service.BillingModelSourceResponse,
	} {
		t.Run(source, func(t *testing.T) {
			group := gatewaylite.GatewayGroupSnapshot{
				ID: 14, Platform: service.PlatformOpenAI, BillingModelSource: source,
				EffectiveModels: []string{"public-model"}, ModelMapping: map[string]string{"public-model": "mapped-model"},
			}
			require.Empty(t, buildGatewayLiteGroupModelPrices(group, global, nil, nil, accounts))
		})
	}

	requested := gatewaylite.GatewayGroupSnapshot{
		ID: 14, Platform: service.PlatformOpenAI, BillingModelSource: service.BillingModelSourceRequested,
		EffectiveModels: []string{"public-model"}, ModelMapping: map[string]string{"public-model": "mapped-model"},
	}
	requestedGlobal := append(global, gatewaylite.GatewayModelPrice{
		Model: "public-model", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 4,
	})
	require.Len(t, buildGatewayLiteGroupModelPrices(requested, requestedGlobal, nil, nil, accounts), 1)

	// channel_mapped（按分组映射后模型计费）在真实结算中止于分组映射，
	// 账号级映射只影响实际上游模型，不能让展示价格随随机账号变化。
	channelMapped := requested
	channelMapped.BillingModelSource = service.BillingModelSourceChannelMapped
	prices := buildGatewayLiteGroupModelPrices(channelMapped, global, nil, nil, accounts)
	require.Len(t, prices, 1)
	require.Equal(t, "public-model", prices[0].Model)
	require.Equal(t, 1.0, prices[0].InputCostPer1MTokens)
}

func TestBuildGatewayLiteGroupModelPricesUsesUniqueAccountMappedPrice(t *testing.T) {
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 15, Platform: service.PlatformOpenAI, BillingModelSource: service.BillingModelSourceUpstream,
		EffectiveModels: []string{"public-model"}, ModelMapping: map[string]string{"public-model": "mapped-model"},
	}
	accounts := []gatewaylite.GatewayAccountSnapshot{
		gatewayLitePricingAccount(1, 15, map[string]any{"mapped-model": "upstream-model"}),
		gatewayLitePricingAccount(2, 15, map[string]any{"mapped-model": "upstream-model"}),
	}
	global := []gatewaylite.GatewayModelPrice{{
		Model: "upstream-model", Provider: service.PlatformOpenAI, BillingMode: "token", InputCostPer1MTokens: 9,
	}}

	prices := buildGatewayLiteGroupModelPrices(group, global, nil, nil, accounts)
	require.Len(t, prices, 1)
	require.Equal(t, "public-model", prices[0].Model)
	require.Equal(t, 9.0, prices[0].InputCostPer1MTokens)
}

func TestBuildGatewayLiteGroupModelPricesPreservesLatestGroupPricingVersion(t *testing.T) {
	channelUpdated := time.UnixMilli(100)
	pricingUpdated := time.UnixMilli(200)
	intervalUpdated := time.UnixMilli(300)
	price := 0.05
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 16, Platform: service.PlatformOpenAI, BillingModelSource: service.BillingModelSourceRequested,
		EffectiveModels: []string{"custom-model"},
	}
	channel := &service.Channel{
		Status: service.StatusActive, UpdatedAt: channelUpdated,
		ModelPricing: []service.ChannelModelPricing{{
			Platform: service.PlatformOpenAI, Models: []string{"custom-model"}, BillingMode: service.BillingModePerRequest,
			PerRequestPrice: &price, UpdatedAt: pricingUpdated,
			Intervals: []service.PricingInterval{{TierLabel: "default", PerRequestPrice: &price, UpdatedAt: intervalUpdated}},
		}},
	}

	prices := buildGatewayLiteGroupModelPrices(group, nil, nil, channel, nil)
	require.Len(t, prices, 1)
	require.Equal(t, intervalUpdated.UnixMilli(), prices[0].UpdatedAtMillis)

	group.ModelPrices = prices
	policies, err := buildGatewayLiteEffectivePolicies("gateway-a", nil, []gatewaylite.GatewayGroupSnapshot{group}, nil, 1)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, intervalUpdated.UnixMilli(), policies[0].SourceVersions.Prices)
}

func gatewayLitePricingAccount(id, groupID int64, mapping map[string]any) gatewaylite.GatewayAccountSnapshot {
	return gatewaylite.GatewayAccountSnapshot{
		ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
		Credentials: map[string]any{"model_mapping": mapping},
	}
}
