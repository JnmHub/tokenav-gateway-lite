package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildGatewayLiteEffectivePoliciesIsDeterministic(t *testing.T) {
	accounts := []gatewaylite.GatewayAccountSnapshot{{
		ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true,
		GroupIDs: []int64{3}, Version: 11,
	}}
	groups := []gatewaylite.GatewayGroupSnapshot{{
		ID: 3, LocalGroupID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		AllowOpenAILongContextBilling: true, OpenAILongContextBillingAccountCount: 1,
		OpenAILongContextBillingStatus: service.GroupOpenAILongContextBillingStatusEnabled,
		EffectiveModels:                []string{"gpt-5.6-sol"}, Version: 12,
	}}
	prices := []gatewaylite.GatewayModelPrice{{
		Model: "gpt-5.6-sol", BillingMode: "token", SupportsServiceTier: true,
		InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8,
		InputCostPer1MTokensPriority: 4, OutputCostPer1MTokensPriority: 16,
		LongContextInputTokenThreshold: 272000,
		InputCostPer1MTokensAbove272K:  4, OutputCostPer1MTokensAbove272K: 12,
		InputCostPer1MTokensPriorityAbove272K: 8, OutputCostPer1MTokensPriorityAbove272K: 24,
		UpdatedAtMillis: 13,
	}}

	first, err := buildGatewayLiteEffectivePolicies("gateway-a", accounts, groups, prices, 100)
	require.NoError(t, err)
	second, err := buildGatewayLiteEffectivePolicies("gateway-a", accounts, groups, prices, 200)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Equal(t, first[0].Checksum, second[0].Checksum, "transport timestamps must not change logical checksum")
	require.Equal(t, gatewaylite.EffectivePolicyStateAvailable, first[0].State)
	require.Equal(t, 1, first[0].EligibleAccountCount)
	require.Equal(t, int64(100), first[0].Revision)
	require.Equal(t, int64(12), first[0].SourceVersions.Groups)
	require.Equal(t, int64(11), first[0].SourceVersions.Accounts)
	require.Equal(t, int64(13), first[0].SourceVersions.Prices)
	requireEffectivePolicyCapability(t, first[0], effectivePolicyCapabilityPriority, gatewaylite.EffectivePolicyStateAvailable, "available")
	requireEffectivePolicyCapability(t, first[0], effectivePolicyCapabilityLongContext, gatewaylite.EffectivePolicyStateAvailable, "available")
	requireEffectivePolicyCapability(t, first[0], effectivePolicyCapabilityLongContextPriority, gatewaylite.EffectivePolicyStateAvailable, "available")
}

func TestBuildGatewayLiteEffectivePoliciesFactsAdvanceRevisionAndChecksum(t *testing.T) {
	account := gatewaylite.GatewayAccountSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true,
		GroupIDs: []int64{3}, Version: 11,
	}
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 3, LocalGroupID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		EffectiveModels: []string{"gpt-test"}, Version: 12,
	}
	price := gatewaylite.GatewayModelPrice{
		Model: "gpt-test", BillingMode: "token", SupportsServiceTier: true,
		InputCostPer1MTokens: 1, OutputCostPer1MTokens: 2,
		InputCostPer1MTokensPriority: 2, OutputCostPer1MTokensPriority: 4, UpdatedAtMillis: 13,
	}
	build := func(account gatewaylite.GatewayAccountSnapshot, group gatewaylite.GatewayGroupSnapshot, price gatewaylite.GatewayModelPrice) gatewaylite.GatewayEffectivePolicy {
		policies, err := buildGatewayLiteEffectivePolicies("gateway-a", []gatewaylite.GatewayAccountSnapshot{account}, []gatewaylite.GatewayGroupSnapshot{group}, []gatewaylite.GatewayModelPrice{price}, 1)
		require.NoError(t, err)
		require.Len(t, policies, 1)
		return policies[0]
	}
	baseline := build(account, group, price)
	require.Equal(t, int64(13), baseline.Revision)

	featureGroup := group
	featureGroup.AllowBlockedKeywordRequests, featureGroup.Version = true, 20
	featurePolicy := build(account, featureGroup, price)
	require.Equal(t, int64(20), featurePolicy.Revision)
	require.NotEqual(t, baseline.Checksum, featurePolicy.Checksum)

	changedAccount := account
	changedAccount.Schedulable, changedAccount.Version = false, 21
	accountPolicy := build(changedAccount, group, price)
	require.Equal(t, int64(21), accountPolicy.Revision)
	require.NotEqual(t, baseline.Checksum, accountPolicy.Checksum)

	changedPrice := price
	changedPrice.InputCostPer1MTokens, changedPrice.UpdatedAtMillis = 3, 22
	pricePolicy := build(account, group, changedPrice)
	require.Equal(t, int64(22), pricePolicy.Revision)
	require.NotEqual(t, baseline.Checksum, pricePolicy.Checksum)
}

func TestBuildGatewayLiteEffectivePoliciesUsesGroupMappingTargetForPrice(t *testing.T) {
	accounts := []gatewaylite.GatewayAccountSnapshot{{
		ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true,
		GroupIDs: []int64{3}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}}, Version: 11,
	}}
	groups := []gatewaylite.GatewayGroupSnapshot{{
		ID: 3, LocalGroupID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		ModelMapping: map[string]string{"public-pro": "gpt-5.6-sol"}, EffectiveModels: []string{"public-pro"}, Version: 12,
	}}
	prices := []gatewaylite.GatewayModelPrice{{
		Model: "gpt-5.6-sol", Provider: "openai", Source: "default_catalog", BillingMode: "token",
		InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8, UpdatedAtMillis: 13,
	}}

	policies, err := buildGatewayLiteEffectivePolicies("gateway-a", accounts, groups, prices, 100)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Len(t, policies[0].Models, 1)
	model := policies[0].Models[0]
	require.Equal(t, "public-pro", model.Model)
	require.Equal(t, "gpt-5.6-sol", model.UpstreamModel)
	require.Equal(t, "gpt-5.6-sol", model.PriceModel)
	require.Equal(t, "group_mapping", model.RouteSource)
	require.Equal(t, gatewaylite.EffectivePolicyStateAvailable, model.State)
}

func TestBuildGatewayLiteEffectivePoliciesAppliesOAuthAndPrivacyRequirements(t *testing.T) {
	accounts := []gatewaylite.GatewayAccountSnapshot{
		{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff}},
		{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3}},
		{ID: 3, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3}, Extra: map[string]any{"privacy_mode": service.PrivacyModeTrainingOff}},
	}
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 3, LocalGroupID: 3, Name: "Private OAuth", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		RequireOAuthOnly: true, RequirePrivacySet: true,
		ModelMapping: map[string]string{"public-pro": "gpt-5.6-sol"}, EffectiveModels: []string{"public-pro"},
	}
	prices := []gatewaylite.GatewayModelPrice{{
		Model: "gpt-5.6-sol", Provider: service.PlatformOpenAI, BillingMode: "token",
		InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8,
	}}

	policies, err := buildGatewayLiteEffectivePolicies("gateway-a", accounts, []gatewaylite.GatewayGroupSnapshot{group}, prices, 100)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, 1, policies[0].EligibleAccountCount)
	require.Len(t, policies[0].Models, 1)
	require.Equal(t, 1, policies[0].Models[0].EligibleAccountCount)
	require.Equal(t, gatewaylite.EffectivePolicyStateAvailable, policies[0].Models[0].State)
}

func TestBuildGatewayLiteEffectivePoliciesCompositeUsesConcreteRouteAndPrice(t *testing.T) {
	updatedAt := time.UnixMilli(444)
	accounts := []gatewaylite.GatewayAccountSnapshot{
		{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{30}, Version: 11},
		{ID: 8, Platform: service.PlatformGrok, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{30}, Version: 12},
	}
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 30, LocalGroupID: 30, Name: "聚合", Platform: service.PlatformComposite, Status: service.StatusActive,
		EffectiveModels: []string{"coding-pro", "grok-4"}, Version: 13,
	}
	routes := map[int64][]service.CompositeModelRoute{30: {{
		ID: 1, GroupID: 30, PublicModel: "coding-pro", MatchType: service.CompositeRouteMatchExact,
		TargetPlatform: service.PlatformOpenAI, UpstreamModel: "gpt-5.6-sol", Endpoint: service.CompositeRouteEndpointResponses,
		Enabled: true, UpdatedAt: updatedAt,
	}}}
	prices := []gatewaylite.GatewayModelPrice{
		{Model: "gpt-5.6-sol", Provider: "anthropic", Source: "wrong_provider", InputCostPer1MTokens: 99, OutputCostPer1MTokens: 999},
		{Model: "gpt-5.6-sol", Provider: "openai", Source: "default_catalog", InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8},
		{Model: "grok-4", Provider: "grok", Source: "default_catalog", InputCostPer1MTokens: 3, OutputCostPer1MTokens: 15},
	}

	policies, err := buildGatewayLiteEffectivePolicies("gateway-a", accounts, []gatewaylite.GatewayGroupSnapshot{group}, prices, 100, routes)
	require.NoError(t, err)
	require.Len(t, policies, 1)
	require.Equal(t, gatewaylite.EffectivePolicyStateAvailable, policies[0].State)
	require.Equal(t, int64(444), policies[0].Revision)
	require.Equal(t, int64(444), policies[0].SourceVersions.CompositeRoutes)
	modelByID := make(map[string]gatewaylite.EffectivePolicyModel, len(policies[0].Models))
	for _, model := range policies[0].Models {
		modelByID[model.Model] = model
	}
	alias := modelByID["coding-pro"]
	require.Equal(t, service.PlatformOpenAI, alias.TargetPlatform)
	require.Equal(t, "gpt-5.6-sol", alias.UpstreamModel)
	require.Equal(t, "gpt-5.6-sol", alias.PriceModel)
	require.Equal(t, service.CompositeRouteSourceExplicit, alias.RouteSource)
	require.Equal(t, 1, alias.EligibleAccountCount)
	require.Equal(t, "default_catalog", alias.PriceSource)
	require.Equal(t, gatewaylite.EffectivePolicyStateAvailable, alias.State, "同名的其他平台价格不能覆盖真实 OpenAI 价格")
	require.Equal(t, service.PlatformGrok, modelByID["grok-4"].TargetPlatform)

	routes[30][0].UpdatedAt = time.UnixMilli(445)
	routes[30][0].UpstreamModel = "gpt-5.4"
	prices = append(prices, gatewaylite.GatewayModelPrice{Model: "gpt-5.4", Provider: "openai", Source: "default_catalog", InputCostPer1MTokens: 2.5, OutputCostPer1MTokens: 10})
	changed, err := buildGatewayLiteEffectivePolicies("gateway-a", accounts, []gatewaylite.GatewayGroupSnapshot{group}, prices, 100, routes)
	require.NoError(t, err)
	require.Equal(t, int64(445), changed[0].Revision)
	require.NotEqual(t, policies[0].Checksum, changed[0].Checksum, "route fact changes must produce a new logical checksum")
}

func TestBuildGatewayLiteEffectivePoliciesFailsClosed(t *testing.T) {
	baseGroup := gatewaylite.GatewayGroupSnapshot{
		ID: 3, LocalGroupID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		EffectiveModels: []string{"gpt-5.6-sol"},
	}
	baseAccount := gatewaylite.GatewayAccountSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3},
	}
	basePrice := gatewaylite.GatewayModelPrice{Model: "gpt-5.6-sol", InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8}

	tests := []struct {
		name       string
		group      gatewaylite.GatewayGroupSnapshot
		account    gatewaylite.GatewayAccountSnapshot
		prices     []gatewaylite.GatewayModelPrice
		wantState  string
		wantReason string
	}{
		{name: "disabled group", group: withGatewayLiteGroupStatus(baseGroup, "disabled"), account: baseAccount, prices: []gatewaylite.GatewayModelPrice{basePrice}, wantState: gatewaylite.EffectivePolicyStateDisabled, wantReason: "group_disabled"},
		{name: "unschedulable account", group: baseGroup, account: withGatewayLiteAccountSchedulable(baseAccount, false), prices: []gatewaylite.GatewayModelPrice{basePrice}, wantState: gatewaylite.EffectivePolicyStateUnavailable, wantReason: "no_eligible_accounts"},
		{name: "missing price", group: baseGroup, account: baseAccount, prices: nil, wantState: gatewaylite.EffectivePolicyStateUnavailable, wantReason: "no_priced_models"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policies, err := buildGatewayLiteEffectivePolicies("gateway-a", []gatewaylite.GatewayAccountSnapshot{tt.account}, []gatewaylite.GatewayGroupSnapshot{tt.group}, tt.prices, 100)
			require.NoError(t, err)
			require.Len(t, policies, 1)
			require.Equal(t, tt.wantState, policies[0].State)
			require.Equal(t, tt.wantReason, policies[0].ReasonCode)
		})
	}
}

func TestBuildGatewayLiteEffectivePoliciesLongContextMatrix(t *testing.T) {
	account := gatewaylite.GatewayAccountSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3},
	}
	price := gatewaylite.GatewayModelPrice{
		Model: "gpt-5.6-sol", InputCostPer1MTokens: 2, OutputCostPer1MTokens: 8,
		LongContextInputTokenThreshold: 272000,
		InputCostPer1MTokensAbove272K:  4, OutputCostPer1MTokensAbove272K: 12,
	}
	tests := []struct {
		name       string
		group      gatewaylite.GatewayGroupSnapshot
		wantState  string
		wantReason string
	}{
		{name: "enabled", group: gatewaylite.GatewayGroupSnapshot{ID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, EffectiveModels: []string{"gpt-5.6-sol"}, AllowOpenAILongContextBilling: true, OpenAILongContextBillingAccountCount: 1, OpenAILongContextBillingStatus: service.GroupOpenAILongContextBillingStatusEnabled}, wantState: gatewaylite.EffectivePolicyStateAvailable, wantReason: "available"},
		{name: "standard price", group: gatewaylite.GatewayGroupSnapshot{ID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, EffectiveModels: []string{"gpt-5.6-sol"}, OpenAIStandardBillingAccountCount: 1, OpenAILongContextBillingStatus: service.GroupOpenAILongContextBillingStatusStandardOnly}, wantState: gatewaylite.EffectivePolicyStateAvailable, wantReason: "uses_standard_price"},
		{name: "no account capability", group: gatewaylite.GatewayGroupSnapshot{ID: 3, Name: "Pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, EffectiveModels: []string{"gpt-5.6-sol"}, OpenAILongContextBillingStatus: service.GroupOpenAILongContextBillingStatusUnavailable}, wantState: gatewaylite.EffectivePolicyStateUnavailable, wantReason: "no_long_context_accounts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policies, err := buildGatewayLiteEffectivePolicies("gateway-a", []gatewaylite.GatewayAccountSnapshot{account}, []gatewaylite.GatewayGroupSnapshot{tt.group}, []gatewaylite.GatewayModelPrice{price}, 100)
			require.NoError(t, err)
			requireEffectivePolicyCapability(t, policies[0], effectivePolicyCapabilityLongContext, tt.wantState, tt.wantReason)
		})
	}
}

func TestBuildGatewayLiteEffectivePoliciesReportsOnlyRealMediaModels(t *testing.T) {
	imagePrice := 0.2
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 3, LocalGroupID: 3, Name: "Media", Platform: service.PlatformOpenAI, Status: service.StatusActive,
		AllowImageGeneration: true, ImagePrice1K: &imagePrice, ImagePrice2K: &imagePrice, ImagePrice4K: &imagePrice,
		EffectiveModels: []string{"gpt-5.6-sol", "gpt-image", "gpt-video"},
	}
	account := gatewaylite.GatewayAccountSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3},
	}
	policies, err := buildGatewayLiteEffectivePolicies("gateway-a", []gatewaylite.GatewayAccountSnapshot{account}, []gatewaylite.GatewayGroupSnapshot{group}, []gatewaylite.GatewayModelPrice{
		{Model: "gpt-5.6-sol", BillingMode: "token", InputCostPer1MTokens: 1, OutputCostPer1MTokens: 2},
		{Model: "gpt-image", Mode: "image_generation", BillingMode: "token", OutputCostPerImageToken: 0.001},
		{Model: "gpt-video", Mode: "video_generation", BillingMode: "video", VideoPricePerSecond720P: 0.1},
	}, 100)
	require.NoError(t, err)
	requireEffectivePolicyCapability(t, policies[0], effectivePolicyCapabilityImageGeneration, gatewaylite.EffectivePolicyStateAvailable, "available")
	requireEffectivePolicyCapability(t, policies[0], effectivePolicyCapabilityVideoGeneration, gatewaylite.EffectivePolicyStateAvailable, "available")
	for _, model := range policies[0].Models {
		if model.Model == "gpt-image" {
			require.Equal(t, "image", model.BillingMode, "complete group image prices are the final billing mode")
		}
	}

	group.EffectiveModels = []string{"gpt-5.6-sol"}
	policies, err = buildGatewayLiteEffectivePolicies("gateway-a", []gatewaylite.GatewayAccountSnapshot{account}, []gatewaylite.GatewayGroupSnapshot{group}, []gatewaylite.GatewayModelPrice{{
		Model: "gpt-5.6-sol", BillingMode: "token", InputCostPer1MTokens: 1, OutputCostPer1MTokens: 2,
	}}, 101)
	require.NoError(t, err)
	requireEffectivePolicyCapability(t, policies[0], effectivePolicyCapabilityImageGeneration, gatewaylite.EffectivePolicyStateUnavailable, "no_eligible_route")
}

func requireEffectivePolicyCapability(t *testing.T, policy gatewaylite.GatewayEffectivePolicy, id, state, reason string) {
	t.Helper()
	for _, capability := range policy.Capabilities {
		if capability.ID == id {
			require.Equal(t, state, capability.State)
			require.Equal(t, reason, capability.ReasonCode)
			return
		}
	}
	t.Fatalf("capability %q not found", id)
}

func TestBuildGatewayLiteEffectivePoliciesLiveCapabilityFailsClosed(t *testing.T) {
	originalEnabled := gatewayLiteLiveNodeEnabled
	originalCheck := gatewayLiteLiveAttestationCheck
	t.Cleanup(func() {
		gatewayLiteLiveNodeEnabled = originalEnabled
		gatewayLiteLiveAttestationCheck = originalCheck
	})

	account := gatewaylite.GatewayAccountSnapshot{
		ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{3}, Version: 11,
	}
	group := gatewaylite.GatewayGroupSnapshot{
		ID: 3, LocalGroupID: 3, Name: "Live", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, EffectiveModels: []string{"gpt-live"}, Version: 12,
	}
	prices := []gatewaylite.GatewayModelPrice{{
		Model: "gpt-live", Provider: service.PlatformOpenAI, BillingMode: "token",
		InputCostPer1MTokens: 1, OutputCostPer1MTokens: 2, UpdatedAtMillis: 13,
	}}
	build := func(current gatewaylite.GatewayGroupSnapshot) gatewaylite.GatewayEffectivePolicy {
		policies, err := buildGatewayLiteEffectivePolicies("gateway-a", []gatewaylite.GatewayAccountSnapshot{account}, []gatewaylite.GatewayGroupSnapshot{current}, prices, 100)
		require.NoError(t, err)
		require.Len(t, policies, 1)
		return policies[0]
	}

	gatewayLiteLiveNodeEnabled = func() bool { return false }
	disabledChecksum := build(group).Checksum
	group.AllowLive = true
	nodeOff := build(group)
	require.NotEqual(t, disabledChecksum, nodeOff.Checksum, "分组 Live 配置属于策略事实，即使节点总开关关闭也必须进入校验和")
	for _, capability := range nodeOff.Capabilities {
		require.NotEqual(t, effectivePolicyCapabilityLive, capability.ID)
	}

	gatewayLiteLiveNodeEnabled = func() bool { return true }
	group.AllowLive = false
	requireEffectivePolicyCapability(t, build(group), effectivePolicyCapabilityLive, gatewaylite.EffectivePolicyStateDisabled, "live_disabled")

	group.AllowLive = true
	gatewayLiteLiveAttestationCheck = func(context.Context) error { return errors.New("unsupported platform") }
	requireEffectivePolicyCapability(t, build(group), effectivePolicyCapabilityLive, gatewaylite.EffectivePolicyStateUnavailable, "device_attestation_unavailable")

	gatewayLiteLiveAttestationCheck = func(context.Context) error { return nil }
	requireEffectivePolicyCapability(t, build(group), effectivePolicyCapabilityLive, gatewaylite.EffectivePolicyStateAvailable, "available")

	account.Type = service.AccountTypeAPIKey
	requireEffectivePolicyCapability(t, build(group), effectivePolicyCapabilityLive, gatewaylite.EffectivePolicyStateUnavailable, "no_live_oauth_accounts")
}

func withGatewayLiteGroupStatus(group gatewaylite.GatewayGroupSnapshot, status string) gatewaylite.GatewayGroupSnapshot {
	group.Status = status
	return group
}

func TestGatewayLiteConcretePolicyPlatformIncludesCNProviders(t *testing.T) {
	t.Parallel()

	for _, platform := range []string{
		service.PlatformKimi,
		service.PlatformZhipu,
		service.PlatformDeepseek,
	} {
		require.True(t, gatewayLiteConcretePolicyPlatform(platform), platform)
	}
	require.False(t, gatewayLiteConcretePolicyPlatform(service.PlatformComposite))
}

func withGatewayLiteAccountSchedulable(account gatewaylite.GatewayAccountSnapshot, value bool) gatewaylite.GatewayAccountSnapshot {
	account.Schedulable = value
	return account
}
