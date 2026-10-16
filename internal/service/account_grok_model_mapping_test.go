package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountGetModelMappingGrokRestoresAliasesAfterUpstreamSync(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"grok-4.5":                   "relay-text",
				"grok-imagine-image-quality": "relay-image-quality",
				"grok-imagine-video-1.5":     "relay-video",
			},
		},
	}

	mapping := account.GetModelMapping()
	require.Equal(t, "relay-text", mapping["grok-4.5-latest"])
	require.Equal(t, "relay-text", mapping["grok-build-latest"])
	require.Equal(t, "relay-image-quality", mapping["grok-imagine-image-quality-latest"])
	require.Equal(t, "relay-image-quality", mapping["grok-imagine-image-pro"])
	require.Equal(t, "relay-video", mapping["grok-imagine-video-1.5-preview"])
	require.Equal(t, "relay-video", mapping["grok-imagine-video-1.5-2026-05-30"])
	require.NotContains(t, mapping, "grok-imagine-image-2026-03-02")
}

func TestAccountGetModelMappingGrokKeepsExplicitAliasOverride(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"grok-4.5":          "relay-text",
				"grok-build-latest": "relay-build-special",
			},
		},
	}

	mapping := account.GetModelMapping()
	require.Equal(t, "relay-build-special", mapping["grok-build-latest"])
}

func TestAccountGetModelMappingOpenAIDoesNotRestoreOrScheduleGrokAliases(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.4":                    "relay-openai",
				"grok-imagine-image-quality": "relay-grok-image",
			},
		},
	}

	mapping := account.GetModelMapping()
	require.Equal(t, "relay-openai", mapping["gpt-5.4"])
	require.Equal(t, "relay-grok-image", mapping["grok-imagine-image-quality"])
	require.NotContains(t, mapping, "grok-imagine-image-pro")
	require.NotContains(t, mapping, "grok-imagine-image-quality-latest")
	require.NotContains(t, mapping, "grok-4.5-latest")
	require.NotContains(t, mapping, "grok-imagine-video-1.5-preview")
	require.True(t, account.IsModelSupported("gpt-5.4"))
	require.False(t, account.IsModelSupported("grok-imagine-image-quality"))
	require.False(t, account.IsModelSupported("grok-imagine-image-pro"))
}

func TestAccountOpenAIAliasMappedToGrokIsNotSchedulable(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"fast-model": "grok-4.5"},
		},
	}

	require.False(t, account.IsModelSupported("fast-model"))
}
