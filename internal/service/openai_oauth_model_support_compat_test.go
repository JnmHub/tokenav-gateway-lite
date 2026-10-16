package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthEmptyMappingRejectsKnownForeignModels(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, model := range []string{
		"deepseek-v4",
		"GLM-5",
		"qwen3-max",
		"provider/gemini-3-pro",
		"grok-4",
		"k3",
		"k3-256k",
		"provider/k3",
	} {
		require.False(t, account.IsModelSupported(model), model)
	}
}

func TestOpenAIOAuthEmptyMappingKeepsOpenAIAndCustomAliases(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	for _, model := range []string{
		"gpt-5.6-sol",
		"gpt-image-2",
		"company-fast-model",
		"claude-alias-mapped-later",
		"my-k3-alias",
	} {
		require.True(t, account.IsModelSupported(model), model)
	}
}

func TestOpenAIOAuthExplicitMappingAndPassthroughRemainAuthoritative(t *testing.T) {
	mapped := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"deepseek-v4": "gpt-5.6-sol",
				"k3":          "gpt-5.6-sol",
			},
		},
	}
	require.True(t, mapped.IsModelSupported("deepseek-v4"))
	require.True(t, mapped.IsModelSupported("k3"))

	passthrough := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"openai_passthrough": true},
	}
	require.True(t, passthrough.IsModelSupported("deepseek-v4"))
}
