package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectiveModelsForGroupSeparatesOpenAIAndGrokAccounts(t *testing.T) {
	openAIGroup := &Group{ID: 10, Platform: PlatformOpenAI, AllowImageGeneration: true}
	grokGroup := &Group{ID: 20, Platform: PlatformGrok, AllowImageGeneration: true}
	accounts := []Account{
		{
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs: []int64{openAIGroup.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.5",
			}},
		},
		{
			ID: 2, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs: []int64{grokGroup.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{
				"grok-4.5": "grok-4.5",
			}},
		},
	}
	known := []string{"gpt-5.5", "grok-4.5"}

	require.Equal(t, []string{"gpt-5.5"}, EffectiveModelsForGroup(openAIGroup, accounts, known))
	grokModels := EffectiveModelsForGroup(grokGroup, accounts, known)
	require.Contains(t, grokModels, "grok-4.5")
	require.NotContains(t, grokModels, "gpt-5.5")
}

func TestDefaultModelsListCandidateIDsDoesNotFallbackToClaudeForCNProviders(t *testing.T) {
	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek} {
		t.Run(platform, func(t *testing.T) {
			candidates := defaultModelsListCandidateIDs(platform)
			require.NotNil(t, candidates)
			require.Empty(t, candidates)
		})
	}
}

func TestGroupCustomModelsListRequestGateMatchesModelsDiscovery(t *testing.T) {
	group := &Group{ModelsListConfig: GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.6-*", "claude-sonnet-4"}}}
	require.True(t, group.IsModelAllowedByCustomList("gpt-5.6-sol"))
	require.True(t, group.IsModelAllowedByCustomList("GPT-5.6-SOL"))
	require.True(t, group.IsModelAllowedByCustomList("claude-sonnet-4"))
	require.False(t, group.IsModelAllowedByCustomList("gpt-5.5"))
	require.False(t, group.IsModelAllowedByCustomList("claude-opus-4"))
}

func TestModelPatternMatchesUsesSameCaseInsensitiveTrailingWildcardRule(t *testing.T) {
	tests := []struct {
		pattern string
		model   string
		want    bool
	}{
		{pattern: "GPT-5.6-*", model: "gpt-5.6-luna", want: true},
		{pattern: "claude-sonnet-4", model: "CLAUDE-SONNET-4", want: true},
		{pattern: "gpt-5.5", model: "gpt-5.6-luna", want: false},
		{pattern: "gpt-5.6-*", model: "gpt-5.5", want: false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, ModelPatternMatches(tt.pattern, tt.model), "%s vs %s", tt.pattern, tt.model)
	}
}

func TestGroupCustomModelsListEmptySelectionRejectsRequests(t *testing.T) {
	group := &Group{ModelsListConfig: GroupModelsListConfig{Enabled: true}}
	require.False(t, group.IsModelAllowedByCustomList("any-model"))
	require.Error(t, validateGroupModelsListConfig(group.ModelsListConfig))
}

func TestEffectiveModelsForGroupPublishesGroupAliasOnlyWhenTargetIsSchedulable(t *testing.T) {
	group := &Group{ID: 9, Platform: PlatformOpenAI, ModelMapping: map[string]string{
		"public-pro": "gpt-5.6-sol", "missing-alias": "gpt-missing",
	}}
	accounts := []Account{{
		ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, GroupIDs: []int64{9},
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}},
	}}

	models := EffectiveModelsForGroup(group, accounts, nil)
	require.Contains(t, models, "public-pro")
	require.NotContains(t, models, "missing-alias")
}

func TestEffectiveModelsForGroupUsesAccountBindingModelMapping(t *testing.T) {
	group := &Group{ID: 12, Platform: PlatformOpenAI, ModelMapping: map[string]string{
		"public-sol": "gpt-5.6-sol",
	}}
	accounts := []Account{{
		ID:          1,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{group.ID},
		Credentials: map[string]any{"model_mapping": map[string]any{"other-model": "other-model"}},
		AccountGroups: []AccountGroup{{
			GroupID:      group.ID,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-luna"},
		}},
	}}

	models := EffectiveModelsForGroup(group, accounts, []string{"gpt-5.6-sol"})
	require.Contains(t, models, "gpt-5.6-sol")
	require.Contains(t, models, "public-sol")
}

func TestEffectiveModelsForGroupUsesPersistentSchedulingStateAndManualIntersection(t *testing.T) {
	group := &Group{
		ID: 30, Platform: PlatformOpenAI, AllowImageGeneration: true,
		ModelsListConfig: GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.5", "unsupported-model"}},
	}
	accounts := []Account{
		{
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs: []int64{group.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{
				"gpt-5.*": "gpt-5.5",
			}},
		},
		{
			ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false,
			GroupIDs: []int64{group.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{
				"gpt-disabled": "gpt-disabled",
			}},
		},
	}

	require.Equal(t, []string{"gpt-5.5"}, EffectiveModelsForGroup(group, accounts, []string{
		"gpt-5.5", "gpt-5.6-sol", "gpt-disabled", "unsupported-model",
	}))
}

func TestEffectiveModelsForGroupTreatsEnabledEmptyManualListAsEmpty(t *testing.T) {
	group := &Group{
		ID: 32, Platform: PlatformOpenAI,
		ModelsListConfig: GroupModelsListConfig{Enabled: true},
	}
	accounts := []Account{{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		GroupIDs:    []int64{group.ID},
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"}},
	}}

	require.Empty(t, EffectiveModelsForGroup(group, accounts, []string{"gpt-5.5"}))
}

func TestEffectiveModelsForGroupExpandsWildcardWithoutExposingPattern(t *testing.T) {
	group := &Group{ID: 31, Platform: PlatformOpenAI, AllowImageGeneration: true}
	accounts := []Account{{
		ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		GroupIDs:    []int64{group.ID},
		Credentials: map[string]any{"model_mapping": map[string]any{"private-*": "gpt-5.5"}},
	}}

	models := EffectiveModelsForGroup(group, accounts, []string{"private-a", "private-b", "gpt-5.5"})
	require.Equal(t, []string{"private-a", "private-b"}, models)
	require.NotContains(t, models, "private-*")
}

func TestEffectiveModelsForGroupAppliesOAuthPrivacyAndImageRestrictions(t *testing.T) {
	group := &Group{
		ID: 40, Platform: PlatformOpenAI, AllowImageGeneration: false,
		RequireOAuthOnly: true, RequirePrivacySet: true,
	}
	accounts := []Account{
		{
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			GroupIDs: []int64{group.ID}, Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff},
			Credentials: map[string]any{"model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.5", "gpt-image-2": "gpt-image-2",
			}},
		},
		{
			ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs:    []int64{group.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{"api-only": "api-only"}},
		},
	}

	require.Equal(t, []string{"gpt-5.5"}, EffectiveModelsForGroup(group, accounts, []string{"gpt-5.5", "gpt-image-2", "api-only"}))
}

func TestEffectiveModelsForGroupReportsKnownEmptyCollection(t *testing.T) {
	models := EffectiveModelsForGroup(&Group{ID: 50, Platform: PlatformGrok}, nil, nil)
	require.NotNil(t, models)
	require.Empty(t, models)
}

func TestEffectiveModelsForGroupOnlyUsesAccountsBoundToTheRequestedGroup(t *testing.T) {
	groupA := &Group{ID: 60, Platform: PlatformOpenAI}
	groupB := &Group{ID: 61, Platform: PlatformOpenAI}
	groupC := &Group{ID: 62, Platform: PlatformOpenAI}
	accounts := []Account{
		{
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs:    []int64{groupA.ID, groupB.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{"shared-model": "shared-model"}},
		},
		{
			ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs:    []int64{groupC.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{"group-c-only": "group-c-only"}},
		},
	}
	known := []string{"shared-model", "group-c-only"}

	require.Equal(t, []string{"shared-model"}, EffectiveModelsForGroup(groupA, accounts, known))
	require.Equal(t, []string{"shared-model"}, EffectiveModelsForGroup(groupB, accounts, known))
	require.Equal(t, []string{"group-c-only"}, EffectiveModelsForGroup(groupC, accounts, known))
}

func TestEffectiveModelsForGroupUnionsSchedulableAccountsAndDropsDisabledUniqueModel(t *testing.T) {
	group := &Group{ID: 70, Platform: PlatformOpenAI}
	accounts := []Account{
		{
			ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs:    []int64{group.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{"shared": "shared", "account-a-only": "account-a-only"}},
		},
		{
			ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			GroupIDs:    []int64{group.ID},
			Credentials: map[string]any{"model_mapping": map[string]any{"shared": "shared", "account-b-only": "account-b-only"}},
		},
	}
	known := []string{"shared", "account-a-only", "account-b-only"}

	require.Equal(t, []string{"account-a-only", "account-b-only", "shared"}, EffectiveModelsForGroup(group, accounts, known))
	accounts[1].Schedulable = false
	require.Equal(t, []string{"account-a-only", "shared"}, EffectiveModelsForGroup(group, accounts, known))
}

func TestEffectiveModelsForCompositeGroupUsesConcreteAccountsAndExplicitAliases(t *testing.T) {
	group := &Group{ID: 80, Platform: PlatformComposite, AllowImageGeneration: true}
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{80}, Credentials: map[string]any{"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"}}},
		{ID: 2, Platform: PlatformGrok, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{80}, Credentials: map[string]any{"model_mapping": map[string]any{"grok-4": "grok-4"}}},
		{ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false, GroupIDs: []int64{80}, Credentials: map[string]any{"model_mapping": map[string]any{"gemini-3-pro": "gemini-3-pro"}}},
		{ID: 4, Platform: PlatformKimi, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{80}, Credentials: map[string]any{"model_mapping": map[string]any{"kimi-k2-thinking": "kimi-k2-thinking"}}},
		{ID: 5, Platform: PlatformZhipu, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{80}, Credentials: map[string]any{"model_mapping": map[string]any{"glm-5.2": "glm-5.2"}}},
		{ID: 6, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{80}, Credentials: map[string]any{"model_mapping": map[string]any{"deepseek-reasoner": "deepseek-reasoner"}}},
	}
	routes := []CompositeModelRoute{{
		ID: 1, GroupID: 80, PublicModel: "coding-pro", MatchType: CompositeRouteMatchExact,
		TargetPlatform: PlatformOpenAI, UpstreamModel: "gpt-5.6-sol", Endpoint: CompositeRouteEndpointAny, Enabled: true,
	}}

	models := EffectiveModelsForCompositeGroup(group, accounts, []string{
		"gpt-5.6-sol", "grok-4", "gemini-3-pro", "kimi-k2-thinking", "glm-5.2", "deepseek-reasoner",
	}, routes)
	require.Equal(t, []string{
		"coding-pro", "deepseek-reasoner", "glm-5.2", "gpt-5.6-sol", "grok-4", "kimi-k2-thinking",
	}, models)
}
