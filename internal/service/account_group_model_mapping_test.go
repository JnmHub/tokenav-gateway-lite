package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestAccountResolveModelForGroupPrefersBindingMapping(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"},
		},
		AccountGroups: []AccountGroup{
			{GroupID: 10, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"}},
			{GroupID: 20, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-sol"}},
		},
	}

	mapped, matched := account.ResolveModelForGroup(10, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-terra", mapped)

	mapped, matched = account.ResolveModelForGroup(20, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-sol", mapped)

	mapped, matched = account.ResolveModelForGroup(30, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-luna", mapped)
}

func TestAccountGroupModelMappingFallsBackPerModel(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-luna": "gpt-5.6-sol"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"},
		}},
	}

	mapped, matched := account.ResolveModelForGroup(10, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-terra", mapped)

	mapped, matched = account.ResolveModelForGroup(10, "gpt-5.6-luna")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-sol", mapped)
}

func TestAccountGroupModelMappingSupportValidation(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-luna"},
		}},
	}

	require.True(t, account.IsModelSupportedForGroup(10, "gpt-5.6-sol"))
	require.False(t, account.IsModelSupportedForGroup(10, "grok-4.5"))
}

func TestNormalizeAccountGroupModelMappingRejectsInvalidInput(t *testing.T) {
	_, err := NormalizeAccountGroupModelMapping(PlatformOpenAI, map[string]string{
		"gpt-5.6-sol": "grok-4.5",
	})
	require.Error(t, err)
}

func TestAccountGroupModelMappingAllowsNoOpOverride(t *testing.T) {
	mapping, err := NormalizeAccountGroupModelMapping(PlatformOpenAI, map[string]string{
		"gpt-5.6-sol": "gpt-5.6-sol",
	})
	require.NoError(t, err)

	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"},
		},
		AccountGroups: []AccountGroup{{GroupID: 10, ModelMapping: mapping}},
	}

	mapped, matched := account.ResolveModelForGroup(10, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-sol", mapped)
}

func TestAccountResolveModelForRequestContextUsesFinalGroup(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"},
		}},
	}
	group := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)

	mapped, matched := account.ResolveModelForRequestContext(ctx, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-terra", mapped)

	// No valid group context keeps legacy account-wide behavior.
	mapped, matched = account.ResolveModelForRequestContext(context.Background(), "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-luna", mapped)
}

func TestAccountGroupIDFromRequestContextCopiesValidGroupID(t *testing.T) {
	group := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	id := AccountGroupIDFromRequestContext(context.WithValue(context.Background(), ctxkey.Group, group))
	require.NotNil(t, id)
	require.Equal(t, int64(10), *id)
	group.ID = 20
	require.Equal(t, int64(10), *id)

	require.Nil(t, AccountGroupIDFromRequestContext(context.Background()))
	require.Nil(t, AccountGroupIDFromRequestContext(context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 10})))
}

func TestEffectiveAccountMappingGroupIDPrefersFinalRequestGroup(t *testing.T) {
	fallbackID := int64(20)
	finalGroup := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	ctx := context.WithValue(context.Background(), ctxkey.Group, finalGroup)

	groupID := EffectiveAccountMappingGroupID(ctx, &fallbackID)
	require.NotNil(t, groupID)
	require.Equal(t, int64(10), *groupID)

	groupID = EffectiveAccountMappingGroupID(context.Background(), &fallbackID)
	require.NotNil(t, groupID)
	require.Equal(t, int64(20), *groupID)

	invalidCtx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 30})
	groupID = EffectiveAccountMappingGroupID(invalidCtx, &fallbackID)
	require.NotNil(t, groupID)
	require.Equal(t, int64(20), *groupID)
}

func TestMappedAccountModelForRequestUsesFinalGroup(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 10, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
	})

	require.Equal(t, "gpt-5.6-terra", MappedAccountModelForRequest(ctx, nil, account, "gpt-5.6-sol"))
}

func TestOpenAIUpstreamModelResolverUsesAccountGroupMapping(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 10, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
	})

	require.Equal(t, "gpt-5.6-terra", resolveOpenAIAccountUpstreamModelForRequest(ctx, nil, account, "gpt-5.6-sol", false))
}

func TestAntigravityModelResolverUsesAccountGroupMapping(t *testing.T) {
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gemini-3-flash": "gemini-3.1-pro-low"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"gemini-3-flash": "gemini-3.1-pro-high"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 10, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true,
	})

	require.Equal(t, "gemini-3.1-pro-high", mapAntigravityModelForRequest(ctx, account, "models/gemini-3-flash"))
	require.Equal(t, "gemini-3.1-pro-low", mapAntigravityModel(account, "gemini-3-flash"))
}

func TestGeminiMessagesCandidateValidationUsesAntigravityGroupMapping(t *testing.T) {
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"unrelated-model": "gemini-3.1-pro-low"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      10,
			ModelMapping: map[string]string{"group-only-model": "gemini-3.1-pro-high"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID: 10, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true,
	})

	service := &GeminiMessagesCompatService{}
	require.False(t, service.isModelSupportedByAccountForRequest(context.Background(), account, "group-only-model"))
	require.True(t, service.isModelSupportedByAccountForRequest(ctx, account, "group-only-model"))
}

func TestOpenAIAccountModelRuntimeBlockIsolatedByGroupMapping(t *testing.T) {
	groupLunaID := int64(10)
	groupTerraID := int64(20)
	account := &Account{
		ID:       77,
		Platform: PlatformOpenAI,
		AccountGroups: []AccountGroup{
			{GroupID: groupLunaID, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-luna"}},
			{GroupID: groupTerraID, ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"}},
		},
	}
	service := &OpenAIGatewayService{openaiModelTransient: newOpenAIAccountModelTransientState(8)}
	service.blockOpenAIAccountModelImmediately(account, "gpt-5.6-luna", time.Now(), time.Minute)

	require.True(t, service.isOpenAIAccountModelRuntimeBlockedForGroup(account, &groupLunaID, "gpt-5.6-sol"))
	require.False(t, service.isOpenAIAccountModelRuntimeBlockedForGroup(account, &groupTerraID, "gpt-5.6-sol"))
}
