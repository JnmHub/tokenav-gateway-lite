package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newTurnStateTestContext(t *testing.T, apiKeyID int64, sessionID string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if sessionID != "" {
		c.Request.Header.Set("session_id", sessionID)
	}
	if apiKeyID > 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID})
	}
	return c, recorder
}

func TestOpenAICodexTurnStateSeed(t *testing.T) {
	c, _ := newTurnStateTestContext(t, 7, "sess-1")
	require.Equal(t, "7\x00sess-1", openAICodexTurnStateSeed(c))
	c.Request.Header.Set("session-id", "sess-hyphen")
	require.Equal(t, "7\x00sess-hyphen", openAICodexTurnStateSeed(c))
	c, _ = newTurnStateTestContext(t, 7, "")
	require.Empty(t, openAICodexTurnStateSeed(c))
}

func TestRelayAndGuardOpenAICodexTurnState(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "sess-guard")
	upstream := http.Header{"X-Codex-Turn-State": []string{"blob-A"}}
	svc.relayOpenAICodexTurnState(c, &Account{ID: 42}, upstream)
	require.Equal(t, "blob-A", c.Writer.Header().Get(openAICodexTurnStateHeader))

	same := http.Header{"X-Codex-Turn-State": []string{"blob-A"}}
	svc.guardOpenAICodexTurnStateEcho(c, &Account{ID: 42}, same)
	require.Equal(t, "blob-A", same.Get(openAICodexTurnStateHeader))

	foreign := http.Header{"X-Codex-Turn-State": []string{"blob-A"}}
	svc.guardOpenAICodexTurnStateEcho(c, &Account{ID: 43}, foreign)
	require.Empty(t, foreign.Get(openAICodexTurnStateHeader))
}

func TestRelayOpenAICodexTurnStateClearsStaleHeader(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "sess-stale")
	c.Writer.Header().Set(openAICodexTurnStateHeader, "stale")
	svc.relayOpenAICodexTurnState(c, &Account{ID: 43}, http.Header{})
	require.Empty(t, c.Writer.Header().Get(openAICodexTurnStateHeader))
}

func TestStageOpenAICodexTurnStateRecordsOnlyWhenCommitted(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 9, "sess-staged")
	var staged http.Header
	stageOpenAICodexTurnState(&staged, http.Header{"X-Codex-Turn-State": []string{"blob-B"}})
	require.Equal(t, "blob-B", staged.Get(openAICodexTurnStateHeader))
	_, exists := svc.openaiCodexTurnStateOrigins.Load("9\x00sess-staged")
	require.False(t, exists)

	svc.noteStagedOpenAICodexTurnStateCommitted(c, &Account{ID: 44}, staged)
	raw, exists := svc.openaiCodexTurnStateOrigins.Load("9\x00sess-staged")
	require.True(t, exists)
	require.Equal(t, int64(44), raw.(openAICodexTurnStateOrigin).accountID)
}

func TestGuardOpenAICodexTurnStateExpiredOriginPassesThrough(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 7, "sess-expired")
	svc.openaiCodexTurnStateOrigins.Store("7\x00sess-expired", openAICodexTurnStateOrigin{
		accountID: 42,
		expiresAt: time.Now().Add(-time.Minute),
	})
	headers := http.Header{"X-Codex-Turn-State": []string{"blob-A"}}
	svc.guardOpenAICodexTurnStateEcho(c, &Account{ID: 43}, headers)
	require.Equal(t, "blob-A", headers.Get(openAICodexTurnStateHeader))
	_, exists := svc.openaiCodexTurnStateOrigins.Load("7\x00sess-expired")
	require.False(t, exists)
}

func TestWriteOpenAIPassthroughResponseHeadersTurnState(t *testing.T) {
	dst := http.Header{}
	writeOpenAIPassthroughResponseHeaders(dst, http.Header{"X-Codex-Turn-State": []string{"blob-P"}}, nil)
	require.Equal(t, "blob-P", dst.Get(openAICodexTurnStateHeader))
	writeOpenAIPassthroughResponseHeaders(dst, http.Header{"Content-Type": []string{"application/json"}}, nil)
	require.Empty(t, dst.Get(openAICodexTurnStateHeader))
}
