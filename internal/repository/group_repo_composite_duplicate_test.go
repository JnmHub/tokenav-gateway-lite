package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestValidGroupDuplicateAccountPlatform(t *testing.T) {
	t.Parallel()

	for _, platform := range []string{
		service.PlatformAnthropic,
		service.PlatformOpenAI,
		service.PlatformGemini,
		service.PlatformAntigravity,
		service.PlatformGrok,
		service.PlatformKimi,
		service.PlatformZhipu,
		service.PlatformDeepseek,
	} {
		require.True(t, validGroupDuplicateAccountPlatform(service.PlatformComposite, platform), platform)
	}
	require.False(t, validGroupDuplicateAccountPlatform(service.PlatformComposite, service.PlatformComposite))
	require.False(t, validGroupDuplicateAccountPlatform(service.PlatformComposite, "unknown"))
	require.True(t, validGroupDuplicateAccountPlatform(service.PlatformOpenAI, service.PlatformOpenAI))
	require.False(t, validGroupDuplicateAccountPlatform(service.PlatformOpenAI, service.PlatformAnthropic))
}
