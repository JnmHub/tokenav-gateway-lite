package gatewaylite

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayConfigPushSnapshotJSONRoundTripPreservesStrictPriorityFields(t *testing.T) {
	want := GatewayConfigPushSnapshot{
		Accounts: []GatewayAccountSnapshot{{
			ID:              41,
			Priority:        50,
			GroupIDs:        []int64{7, 8},
			GroupPriorities: map[int64]int{7: 1, 8: 9},
		}},
		Groups: []GatewayGroupSnapshot{{
			ID:             7,
			Name:           "strict-pro",
			SchedulingMode: "strict_priority",
		}},
	}

	encoded, err := json.Marshal(want)
	require.NoError(t, err)

	var got GatewayConfigPushSnapshot
	require.NoError(t, json.Unmarshal(encoded, &got))
	require.Len(t, got.Accounts, 1)
	require.Equal(t, want.Accounts[0].GroupIDs, got.Accounts[0].GroupIDs)
	require.Equal(t, want.Accounts[0].GroupPriorities, got.Accounts[0].GroupPriorities)
	require.Len(t, got.Groups, 1)
	require.Equal(t, "strict_priority", got.Groups[0].SchedulingMode)
}

func TestGatewayConfigPushSnapshotChecksumIgnoresOrderingAndTransientVersions(t *testing.T) {
	first := GatewayConfigPushSnapshot{
		Accounts: []GatewayAccountSnapshot{
			{ID: 2, Name: "second", Status: "active", UpdatedAtMillis: 20},
			{ID: 1, Name: "first", Status: "active", UpdatedAtMillis: 10},
		},
		Groups: []GatewayGroupSnapshot{
			{ID: 2, Name: "second", RateMultiplier: 2, EffectiveModelsVersion: 100, ModelPrices: []GatewayModelPrice{
				{Model: "z-model", BillingMode: "per_request", PerRequestPrice: 0.2, UpdatedAtMillis: 100, Intervals: []GatewayModelPriceInterval{{TierLabel: "HD", SortOrder: 2}, {TierLabel: "SD", SortOrder: 1}}},
				{Model: "a-model", BillingMode: "token", InputCostPer1MTokens: 1},
			}},
			{ID: 1, Name: "first", RateMultiplier: 1, EffectiveModelsVersion: 200, ModelPrices: []GatewayModelPrice{}},
		},
		ModelPrices: []GatewayModelPrice{
			{Provider: "openai", Model: "z-model", InputCostPer1MTokens: 2},
			{Provider: "anthropic", Model: "a-model", InputCostPer1MTokens: 1},
		},
		EffectivePolicies: []GatewayEffectivePolicy{
			{LocalGroupID: 2, Revision: 20, GeneratedAtMillis: 200, Checksum: "POLICY-TWO"},
			{LocalGroupID: 1, Revision: 10, GeneratedAtMillis: 100, Checksum: "POLICY-ONE"},
		},
		RuntimeSettings: &GatewayRuntimeSettings{OpenAIFastPolicySettings: &OpenAIFastPolicyRuntimeSettings{
			Version: 1, PriorityDetectionByRequestEnabled: true, RuleCount: 2, ReportedAtMillis: 100,
		}},
	}
	second := first
	second.Accounts = []GatewayAccountSnapshot{first.Accounts[1], first.Accounts[0]}
	second.Groups = []GatewayGroupSnapshot{first.Groups[1], first.Groups[0]}
	second.Groups[0].EffectiveModelsVersion = 999
	second.Groups[1].EffectiveModelsVersion = 998
	second.Groups[1].ModelPrices = []GatewayModelPrice{first.Groups[0].ModelPrices[1], first.Groups[0].ModelPrices[0]}
	second.Groups[1].ModelPrices[1].UpdatedAtMillis = 999
	second.Groups[1].ModelPrices[1].Intervals = []GatewayModelPriceInterval{
		first.Groups[0].ModelPrices[0].Intervals[1], first.Groups[0].ModelPrices[0].Intervals[0],
	}
	second.ModelPrices = []GatewayModelPrice{first.ModelPrices[1], first.ModelPrices[0]}
	second.EffectivePolicies = []GatewayEffectivePolicy{first.EffectivePolicies[1], first.EffectivePolicies[0]}
	second.EffectivePolicies[0].Revision = 999
	second.EffectivePolicies[0].GeneratedAtMillis = 999
	second.RuntimeSettings = &GatewayRuntimeSettings{OpenAIFastPolicySettings: &OpenAIFastPolicyRuntimeSettings{
		Version: 1, PriorityDetectionByRequestEnabled: true, RuleCount: 2, ReportedAtMillis: 999,
	}}

	firstChecksum, err := GatewayConfigPushSnapshotChecksum(first)
	require.NoError(t, err)
	secondChecksum, err := GatewayConfigPushSnapshotChecksum(second)
	require.NoError(t, err)
	require.Equal(t, firstChecksum, secondChecksum)
}

func TestGatewayConfigPushSnapshotChecksumChangesWithLogicalConfiguration(t *testing.T) {
	base := GatewayConfigPushSnapshot{
		Accounts:          []GatewayAccountSnapshot{{ID: 1, Status: "active", GroupIDs: []int64{1}, GroupPriorities: map[int64]int{1: 1}}},
		Groups:            []GatewayGroupSnapshot{{ID: 1, Name: "Pro", RateMultiplier: 1}},
		ModelPrices:       []GatewayModelPrice{{Provider: "openai", Model: "gpt-test", InputCostPer1MTokens: 1}},
		EffectivePolicies: []GatewayEffectivePolicy{{LocalGroupID: 1, Checksum: "policy-one"}},
		RuntimeSettings: &GatewayRuntimeSettings{OpenAIFastPolicySettings: &OpenAIFastPolicyRuntimeSettings{
			Version: 1, PriorityDetectionByRequestEnabled: true,
		}},
	}
	baseChecksum, err := GatewayConfigPushSnapshotChecksum(base)
	require.NoError(t, err)

	mutations := []func(*GatewayConfigPushSnapshot){
		func(snapshot *GatewayConfigPushSnapshot) { snapshot.Accounts[0].Status = "disabled" },
		func(snapshot *GatewayConfigPushSnapshot) { snapshot.Accounts[0].GroupPriorities = map[int64]int{1: 2} },
		func(snapshot *GatewayConfigPushSnapshot) { snapshot.Groups[0].RateMultiplier = 1.5 },
		func(snapshot *GatewayConfigPushSnapshot) {
			snapshot.Groups[0].ModelPrices = []GatewayModelPrice{{Model: "gpt-test", BillingMode: "per_request", PerRequestPrice: 0.1}}
		},
		func(snapshot *GatewayConfigPushSnapshot) { snapshot.ModelPrices[0].InputCostPer1MTokens = 2 },
		func(snapshot *GatewayConfigPushSnapshot) { snapshot.EffectivePolicies[0].Checksum = "policy-two" },
		func(snapshot *GatewayConfigPushSnapshot) {
			snapshot.RuntimeSettings.OpenAIFastPolicySettings.PriorityDetectionByRequestEnabled = false
		},
	}
	for index, mutate := range mutations {
		candidate := base
		candidate.Accounts = append([]GatewayAccountSnapshot(nil), base.Accounts...)
		candidate.Groups = append([]GatewayGroupSnapshot(nil), base.Groups...)
		candidate.ModelPrices = append([]GatewayModelPrice(nil), base.ModelPrices...)
		candidate.EffectivePolicies = append([]GatewayEffectivePolicy(nil), base.EffectivePolicies...)
		settings := *base.RuntimeSettings
		fastPolicy := *base.RuntimeSettings.OpenAIFastPolicySettings
		settings.OpenAIFastPolicySettings = &fastPolicy
		candidate.RuntimeSettings = &settings
		mutate(&candidate)
		checksum, err := GatewayConfigPushSnapshotChecksum(candidate)
		require.NoError(t, err)
		require.NotEqualf(t, baseChecksum, checksum, "mutation %d did not change checksum", index)
	}
}
