package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllPlatformsIncludesEveryConcretePlatform(t *testing.T) {
	t.Parallel()

	require.ElementsMatch(t, []string{
		PlatformAnthropic,
		PlatformOpenAI,
		PlatformGemini,
		PlatformAntigravity,
		PlatformGrok,
		PlatformKimi,
		PlatformZhipu,
		PlatformDeepseek,
		// MiniMax（M 系列）在本地计费兜底价、账号平台判定、调度阈值与
		// ollama-cloud 用量 SQL 里都是真实平台；官方 v0.2.13 的同一用例也包含它。
		PlatformMiniMax,
	}, AllPlatforms())
}
