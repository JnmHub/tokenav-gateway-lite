package handler

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 独立的搜索、语音与 Realtime 路由必须复用普通请求的三维并发入口，
// 否则它们会绕过用户等级、分组每用户并发和 API Key 活跃统计。
func TestGrokAuxiliaryRoutesUseCombinedUserGroupAPIKeyConcurrency(t *testing.T) {
	searchBody, err := os.ReadFile("gateway_web_search.go")
	require.NoError(t, err)
	require.Contains(t, string(searchBody), "AcquireUserSlotWithWait(")

	audioBody, err := os.ReadFile("grok_audio.go")
	require.NoError(t, err)
	require.Contains(t, string(audioBody), "acquireResponsesUserSlot(")

	helper, err := os.ReadFile("gateway_helper.go")
	require.NoError(t, err)
	source := string(helper)
	require.Contains(t, source, "finalGroupConcurrencyLimit(c)")
	require.Contains(t, source, "finalAPIKeyConcurrencyID(c)")
	require.True(t, strings.Contains(source, "AcquireUserGroupSlotForAPIKey("))
}
