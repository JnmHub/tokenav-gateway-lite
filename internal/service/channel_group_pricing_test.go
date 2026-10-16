//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterGroupPricingRulesDoesNotLeakOtherGroupAccounts(t *testing.T) {
	rules := []AccountStatsPricingRule{
		{Name: "other", AccountIDs: []int64{22}},
		{Name: "mixed", AccountIDs: []int64{11, 22}},
		{Name: "whole", GroupIDs: []int64{7}},
	}

	got := filterGroupPricingRules(rules, 7, []int64{11})
	require.Len(t, got, 2)
	require.Equal(t, "mixed", got[0].Name)
	require.Equal(t, []int64{11}, got[0].AccountIDs)
	require.Equal(t, "whole", got[1].Name)
}

func TestValidateGroupPricingPlatformsPreservesCompositePlatforms(t *testing.T) {
	pricing := []ChannelModelPricing{
		{Platform: PlatformOpenAI, Models: []string{"same-model"}, BillingMode: BillingModeToken},
		{Platform: PlatformAnthropic, Models: []string{"same-model"}, BillingMode: BillingModePerRequest, PerRequestPrice: groupPricingFloat(0.02)},
		{Platform: PlatformGrok, Models: []string{"video-model"}, BillingMode: BillingModeVideo, PerRequestPrice: groupPricingFloat(0.1)},
	}
	require.NoError(t, validateGroupPricingPlatforms(PlatformComposite, pricing, nil))
	require.NoError(t, validatePricingEntries(pricing))
	require.Error(t, validateGroupPricingPlatforms(PlatformComposite, []ChannelModelPricing{{Platform: PlatformComposite}}, nil))
}

func TestNormalizeGroupAccountStatsPricingRulesRejectsAccountOutsideGroup(t *testing.T) {
	groupRepo := &mockGroupRepoForGateway{groups: map[int64]*Group{7: {ID: 7}}}
	service := NewChannelService(&mockChannelRepository{}, groupRepo, nil, nil)
	rules := []AccountStatsPricingRule{{Name: "selected", AccountIDs: []int64{99}}}

	err := service.normalizeGroupAccountStatsPricingRules(context.Background(), 7, rules)
	require.Error(t, err)
}

func TestUpdateGroupPricingPreservesNativeGroupMappingAndBillingSource(t *testing.T) {
	group := &Group{
		ID:                 7,
		Name:               "mapped-group",
		Platform:           PlatformOpenAI,
		ModelMapping:       map[string]string{"gpt-public": "gpt-upstream"},
		BillingModelSource: BillingModelSourceChannelMapped,
	}
	existing := &Channel{
		ID:                 41,
		Name:               "legacy-channel",
		BillingModelSource: BillingModelSourceUpstream,
		ModelMapping: map[string]map[string]string{
			PlatformOpenAI: {"gpt-public": "legacy-target"},
		},
		GroupIDs: []int64{7},
	}
	current := existing
	repo := &mockChannelRepository{
		getChannelIDByGroupIDFn: func(context.Context, int64) (int64, error) { return 41, nil },
		getByIDFn: func(context.Context, int64) (*Channel, error) {
			return current, nil
		},
		getGroupIDsFn: func(context.Context, int64) ([]int64, error) { return []int64{7}, nil },
		replaceGroupPricingFn: func(_ context.Context, groupID int64, channel *Channel) (int64, error) {
			require.Equal(t, int64(7), groupID)
			require.Empty(t, channel.ModelMapping)
			require.Equal(t, BillingModelSourceRequested, channel.BillingModelSource)
			channel.ID = 41
			current = channel
			return channel.ID, nil
		},
	}
	service := NewChannelService(repo, &mockGroupRepoForGateway{groups: map[int64]*Group{7: group}}, nil, nil)
	price := 0.02

	_, err := service.UpdateGroupPricing(context.Background(), 7, &UpdateGroupPricingInput{
		Enabled: true,
		ModelPricing: []ChannelModelPricing{{
			Platform: PlatformOpenAI, Models: []string{"gpt-public"},
			BillingMode: BillingModePerRequest, PerRequestPrice: &price,
		}},
	})

	require.NoError(t, err)
	require.Equal(t, map[string]string{"gpt-public": "gpt-upstream"}, group.ModelMapping)
	require.Equal(t, BillingModelSourceChannelMapped, group.BillingModelSource)
}

func groupPricingFloat(value float64) *float64 { return &value }
