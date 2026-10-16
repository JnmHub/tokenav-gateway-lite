package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestOpenAISessionIDUpstreamHeadersAreIsolatedByAPIKey exercises the actual
// Forward path so a regression in either ordinary HTTP or passthrough request
// construction cannot silently expose the client-provided session value.
func TestOpenAISessionIDUpstreamHeadersAreIsolatedByAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, passthrough := range []bool{false, true} {
		mode := "ordinary"
		if passthrough {
			mode = "passthrough"
		}
		for _, headerName := range []string{"session-id", "session_id"} {
			t.Run(mode+"/"+headerName, func(t *testing.T) {
				const clientSessionID = "client-session-must-not-leak"
				body := []byte(`{"model":"gpt-5.2","stream":false,"instructions":"session header test","input":[{"type":"text","text":"hello"}]}`)

				makeRequest := func(apiKeyID int64) *http.Request {
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(bytes.NewReader([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-session-header\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\ndata: [DONE]\n\n"))),
					}}
					svc := &OpenAIGatewayService{
						cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
						httpUpstream: upstream,
					}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")
					c.Request.Header.Set(headerName, clientSessionID)
					c.Set("api_key", &APIKey{ID: apiKeyID})

					extra := map[string]any{}
					if passthrough {
						extra["openai_passthrough"] = true
					}
					account := &Account{
						ID:          8801,
						Platform:    PlatformOpenAI,
						Type:        AccountTypeOAuth,
						Concurrency: 1,
						Credentials: map[string]any{"access_token": "oauth-session-test", "chatgpt_account_id": "chatgpt-session-test"},
						Extra:       extra,
						Status:      StatusActive,
						Schedulable: true,
					}

					_, err := svc.Forward(context.Background(), c, account, body)
					require.NoError(t, err)
					require.NotNil(t, upstream.lastReq)
					return upstream.lastReq
				}

				first := makeRequest(101)
				second := makeRequest(202)
				firstIsolated := isolateOpenAISessionID(101, clientSessionID)
				secondIsolated := isolateOpenAISessionID(202, clientSessionID)

				for name, req := range map[string]*http.Request{"first API Key": first, "second API Key": second} {
					require.NotEqual(t, clientSessionID, req.Header.Get("session-id"), name+" must not receive the raw hyphenated session header")
					require.NotEqual(t, clientSessionID, req.Header.Get("session_id"), name+" must not receive the raw underscored session header")
				}
				require.Equal(t, firstIsolated, first.Header.Get("session_id"))
				require.Equal(t, secondIsolated, second.Header.Get("session_id"))
				require.NotEqual(t, first.Header.Get("session_id"), second.Header.Get("session_id"), "相同客户端 session 必须按 API Key 隔离")
				require.Empty(t, first.Header.Get("session-id"), "上游不应保留客户端 session-id 头")
				require.Empty(t, second.Header.Get("session-id"), "上游不应保留客户端 session-id 头")
			})
		}
	}
}
