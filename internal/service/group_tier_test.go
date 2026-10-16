package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequiredTierUsesStrictestAvailableRule(t *testing.T) {
	tier0, tier1, tier2 := 0, 1, 2
	tests := []struct {
		name  string
		group *Group
		want  int
	}{
		{name: "missing group", group: nil, want: 0},
		{name: "inherit without snapshot", group: &Group{}, want: 0},
		{name: "explicit T0", group: &Group{MinimumTier: &tier0}, want: 0},
		{name: "explicit T1", group: &Group{MinimumTier: &tier1}, want: 1},
		{name: "explicit T2", group: &Group{MinimumTier: &tier2}, want: 2},
		{name: "node snapshot is stricter", group: &Group{MinimumTier: &tier1, EffectiveRequiredTier: 2}, want: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, GroupRequiredTier(tc.group))
		})
	}
}

func TestKeywordRouteCandidatesExcludeGroupsAboveUserTier(t *testing.T) {
	tier1, tier2 := 1, 2
	apiKey := &APIKey{User: &User{Tier: 1}}
	original := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive}
	groups := []Group{
		{ID: 20, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, MinimumTier: &tier2},
		{ID: 30, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, MinimumTier: &tier1},
		{ID: 40, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, EffectiveRequiredTier: 2},
	}

	candidates := keywordRouteCandidates(apiKey, original, groups, "gpt-test", ContentModerationProtocolOpenAIResponses, nil)
	require.Len(t, candidates, 1)
	require.Equal(t, int64(30), candidates[0].ID)
}

func TestEnterpriseUserTierDoesNotOverrideOrganizationAuthorization(t *testing.T) {
	tier2 := 2
	group := &Group{MinimumTier: &tier2, EffectiveRequiredTier: 2}
	require.False(t, GroupTierAllowedForUser(&User{Tier: 0}, group))
	require.True(t, GroupTierAllowedForUser(&User{OrganizationID: 88, Tier: 0}, group))
}

func TestEnterpriseKeywordRouteCandidatesUseOrganizationAllowlistInsteadOfPersonalTier(t *testing.T) {
	tier2 := 2
	apiKey := &APIKey{
		User:                            &User{OrganizationID: 88, Tier: 0},
		KeywordRouteGroupAccessEnforced: true,
		AllowedKeywordRouteGroupIDs:     []int64{30},
	}
	original := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive}
	groups := []Group{
		{ID: 20, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, MinimumTier: &tier2},
		{ID: 30, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, MinimumTier: &tier2},
	}

	candidates := keywordRouteCandidates(apiKey, original, groups, "gpt-test", ContentModerationProtocolOpenAIResponses, nil)
	require.Len(t, candidates, 1)
	require.Equal(t, int64(30), candidates[0].ID)
}
