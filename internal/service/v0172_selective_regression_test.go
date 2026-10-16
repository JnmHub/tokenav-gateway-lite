package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestV0172Grok405FailsOverWithoutAccountCooldown(t *testing.T) {
	svc := &OpenAIGatewayService{}
	require.True(t, svc.shouldFailoverUpstreamError(http.StatusMethodNotAllowed))
}

func TestV0172OpsSystemLogBackoffIsBounded(t *testing.T) {
	sink := &OpsSystemLogSink{flushBackoff: 2 * time.Second, flushBackoffMax: time.Minute}
	require.Equal(t, 2*time.Second, sink.flushBackoffFor(1))
	require.Equal(t, 8*time.Second, sink.flushBackoffFor(3))
	require.Equal(t, time.Minute, sink.flushBackoffFor(30))
}

func TestV0172GrokVideoTaskIDCanOwnFollowupRequest(t *testing.T) {
	require.Equal(t, "task-top", extractGrokMediaVideoRequestID([]byte(`{"task_id":"task-top"}`)))
	require.Equal(t, "task-data", extractGrokMediaVideoRequestID([]byte(`{"data":{"task_id":"task-data"}}`)))
	require.Equal(t, "task-video", extractGrokMediaVideoRequestID([]byte(`{"video":{"task_id":"task-video"}}`)))
}

func TestV0172DefaultCodexIdentityUsesTUI(t *testing.T) {
	require.Equal(t, "codex-tui", openai.CodexDefaultOriginator)
	require.Contains(t, codexCLIUserAgent, "codex-tui/")
	identity := resolveCodexOutboundIdentity("")
	require.Equal(t, openai.CodexDefaultOriginator, identity.originator)
	require.Equal(t, codexCLIUserAgent, identity.userAgent)
}
