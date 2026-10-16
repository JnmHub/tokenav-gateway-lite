package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectModelPlatformIncludesCNProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		model    string
		platform string
	}{
		{"kimi-k2-thinking", PlatformKimi},
		{"moonshot/moonshot-v1-32k", PlatformKimi},
		{"glm-5.2", PlatformZhipu},
		{"zhipu/glm-4.7", PlatformZhipu},
		{"deepseek-v4-pro", PlatformDeepseek},
	}

	for _, test := range tests {
		platform, ok := DetectModelPlatform(test.model)
		require.True(t, ok, test.model)
		require.Equal(t, test.platform, platform, test.model)
	}

	_, ok := DetectModelPlatform("llama-unknown")
	require.False(t, ok)
}

func TestCompositeSchedulerIncludesCNProviderBuckets(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for _, bucket := range schedulerCanonicalBuckets(99) {
		seen[bucket.Platform] = true
	}

	for _, platform := range []string{PlatformKimi, PlatformZhipu, PlatformDeepseek} {
		require.True(t, seen[platform], platform)
		require.True(t, isConcreteRequestPlatform(platform), platform)
	}
}
