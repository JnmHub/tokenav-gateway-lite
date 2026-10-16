package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestEffectiveAccountMappingGroupIDPrefersFinalContextGroup(t *testing.T) {
	fallbackID := int64(20)
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:       10,
		Platform: PlatformOpenAI,
		Status:   StatusActive,
		Hydrated: true,
	})

	resolved := EffectiveAccountMappingGroupID(ctx, &fallbackID)
	require.NotNil(t, resolved)
	require.Equal(t, int64(10), *resolved)

	resolved = EffectiveAccountMappingGroupID(context.Background(), &fallbackID)
	require.NotNil(t, resolved)
	require.Equal(t, int64(20), *resolved)
}

func TestResolveAccountModelForRequestUsesGroupRuleThenGlobalFallback(t *testing.T) {
	groupID := int64(10)
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      groupID,
			ModelMapping: map[string]string{"gpt-5.6-sol": "gpt-5.6-terra"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:       groupID,
		Platform: PlatformOpenAI,
		Status:   StatusActive,
		Hydrated: true,
	})

	mapped, matched := ResolveAccountModelForRequest(ctx, nil, account, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-terra", mapped)

	mapped, matched = ResolveAccountModelForRequest(context.Background(), nil, account, "gpt-5.6-sol")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-luna", mapped)
}

func TestResolveOpenAIForwardModelForRequestKeepsExplicitMappingOverDefaultHint(t *testing.T) {
	groupID := int64(10)
	account := &Account{
		Platform: PlatformOpenAI,
		AccountGroups: []AccountGroup{{
			GroupID:      groupID,
			ModelMapping: map[string]string{"claude-sonnet-4-5": "gpt-5.6-terra"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:       groupID,
		Platform: PlatformOpenAI,
		Status:   StatusActive,
		Hydrated: true,
	})

	resolved := ResolveOpenAIForwardModelForRequest(ctx, nil, account, "claude-sonnet-4-5", "gpt-5.6-luna")
	require.Equal(t, "gpt-5.6-terra", resolved)
}

func TestResolveAccountModelForRequestAppliesAccountMappingAfterGroupFallback(t *testing.T) {
	groupID := int64(10)
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-terra"},
		},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:       groupID,
		Platform: PlatformOpenAI,
		Status:   StatusActive,
		Hydrated: true,
	})
	ctx = WithOpenAIGroupModelFallback(
		ctx,
		groupID,
		"gpt-5.6-luna",
		"gpt-5.6-luna",
		"gpt-5.6-sol",
	)

	mapped, matched := ResolveAccountModelForRequest(ctx, nil, account, "gpt-5.6-luna")
	require.True(t, matched)
	require.Equal(t, "gpt-5.6-terra", mapped)
}

func TestResolveBedrockModelIDForRequestUsesGroupSpecificMapping(t *testing.T) {
	groupID := int64(10)
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
		Credentials: map[string]any{
			"aws_region": "us-east-1",
			"model_mapping": map[string]any{
				"claude-sonnet-4-5": "claude-opus-4-6",
			},
		},
		AccountGroups: []AccountGroup{{
			GroupID:      groupID,
			ModelMapping: map[string]string{"claude-sonnet-4-5": "claude-haiku-4-5"},
		}},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ID:       groupID,
		Platform: PlatformAnthropic,
		Status:   StatusActive,
		Hydrated: true,
	})

	modelID, ok := ResolveBedrockModelIDForRequest(ctx, account, "claude-sonnet-4-5")
	require.True(t, ok)
	require.Equal(t, "us.anthropic.claude-haiku-4-5-20251001-v1:0", modelID)
}
