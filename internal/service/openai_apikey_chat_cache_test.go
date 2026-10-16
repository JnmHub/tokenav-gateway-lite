package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ============================================================================
// [官方 v0.2.0 移植] API Key 账号自动派生 prompt_cache_key 并做租户隔离。
//
// OpenAI 上游按 prompt_cache_key 复用对话缓存；同一 API Key 账号下不同租户
// （不同站点 API Key）派生出的 key 必须隔离，否则会互相命中对方的上游缓存。
// 派生 key 已含租户前缀，session_id 计算不得二次隔离。
// ============================================================================

func newAPIKeyChatCacheUpstream(t *testing.T, n int) *httpUpstreamRecorder {
	t.Helper()
	bad := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"stop before response parsing"}}`)),
		}
	}
	responses := make([]*http.Response, 0, n)
	for i := 0; i < n; i++ {
		responses = append(responses, bad())
	}
	return &httpUpstreamRecorder{responses: responses}
}

func forwardChatCacheOnce(t *testing.T, svc *OpenAIGatewayService, account *Account, apiKeyID int64, body []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: apiKeyID})
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.4")
	require.Error(t, err)
}

func TestForwardAsChatCompletions_APIKeyAutoDerivesStableIsolatedPromptCacheKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := newAPIKeyChatCacheUpstream(t, 3)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 2, Name: "openai-compatible", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-compatible"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	firstBody := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}],"stream":false}`)
	appendedBody := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}],"stream":false}`)

	// 同一租户两次请求（对话追加了轮次）：派生 key 必须稳定，缓存才能跨请求命中
	forwardChatCacheOnce(t, svc, account, 99, firstBody)
	forwardChatCacheOnce(t, svc, account, 99, appendedBody)
	// 另一个租户发同样的对话：派生 key 必须不同（租户隔离）
	forwardChatCacheOnce(t, svc, account, 100, appendedBody)

	firstKey := gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").String()
	appendedKey := gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").String()
	otherTenantKey := gjson.GetBytes(upstream.bodies[2], "prompt_cache_key").String()
	require.NotEmpty(t, firstKey)
	require.Equal(t, firstKey, appendedKey, "同一租户的相同对话前缀必须派生相同 cache key")
	require.NotEqual(t, firstKey, otherTenantKey, "不同租户必须隔离 cache key")

	// 派生 key 已含租户前缀：session_id 不得二次隔离
	require.Equal(t, generateSessionUUID(firstKey), upstream.requests[0].Header.Get("session_id"))
	require.Equal(t, upstream.requests[0].Header.Get("session_id"), upstream.requests[1].Header.Get("session_id"))
	require.Equal(t, generateSessionUUID(otherTenantKey), upstream.requests[2].Header.Get("session_id"))
}

func TestForwardAsChatCompletions_ResponsesShapeDoesNotAutoDerivePromptCacheKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := newAPIKeyChatCacheUpstream(t, 3)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 2, Name: "openai-compatible", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-compatible"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
	firstBody := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"first unrelated input"}]}],"stream":false}`)
	secondBody := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"second unrelated input"}]}],"stream":false}`)

	forwardChatCacheOnce(t, svc, account, 99, firstBody)
	forwardChatCacheOnce(t, svc, account, 99, secondBody)

	// Responses 形状的流量是原生 Responses 语义，不自动注入
	require.False(t, gjson.GetBytes(upstream.bodies[0], "prompt_cache_key").Exists())
	require.Empty(t, upstream.requests[0].Header.Get("session_id"))
	require.False(t, gjson.GetBytes(upstream.bodies[1], "prompt_cache_key").Exists())
}
