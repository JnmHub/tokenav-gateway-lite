//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGrokChatResponsesBridgeEligibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		body   string
		want   bool
		reason string
	}{
		{
			name: "text and image are bridgeable",
			body: `{"model":"grok","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}]}]}`,
			want: true,
		},
		{
			name: "function tools are bridgeable",
			body: `{"model":"grok","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"},"strict":false}}],"tool_choice":"auto"}`,
			want: true,
		},
		{
			name:   "missing function schema stays on raw chat",
			body:   `{"model":"grok","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`,
			reason: "invalid_tool_function_parameters",
		},
		{
			name: "Trae compatibility fields are bridgeable",
			body: `{"model":"grok","messages":[{"role":"user","content":"return json"}],"instructions":"Be concise","response_format":{"type":"json_object"},"service_tier":"fast","stop":null,"reasoning_effort":null}`,
			want: true,
		},
		{
			name: "tool history and parallel calls are bridgeable",
			body: `{"model":"grok","messages":[{"role":"assistant","content":null,"reasoning_content":"call lookup","tool_calls":[{"index":0,"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"alpha\"}"}}]},{"role":"tool","tool_call_id":"call_lookup","content":"{\"value\":\"ok\"}"},{"role":"user","content":"summarize"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required","parallel_tool_calls":true}`,
			want: true,
		},
		{
			name:   "required choice without tools stays raw",
			body:   `{"model":"grok","messages":[{"role":"user","content":"hi"}],"tools":[],"tool_choice":"required"}`,
			reason: "required_tool_choice_without_tools",
		},
		{
			name:   "invalid tool arguments stay raw",
			body:   `{"model":"grok","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_lookup","type":"function","function":{"name":"lookup","arguments":"{"}}]}]}`,
			reason: "invalid_tool_call_arguments",
		},
		{
			name:   "non boolean parallel calls stay raw",
			body:   `{"model":"grok","messages":[{"role":"user","content":"hi"}],"parallel_tool_calls":"true"}`,
			reason: "invalid_parallel_tool_calls",
		},
		{
			name:   "unknown content part stays on raw chat",
			body:   `{"model":"grok","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"AA=="}}]}]}`,
			reason: "unsupported_content_part_input_audio",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := grokChatResponsesBridgeEligibility([]byte(tt.body))
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestGrokChatResponsesRuntimeEligibility(t *testing.T) {
	t.Parallel()

	require.True(t, grokChatResponsesRuntimeEligible("grok-4.5", "isolated-id"))
	require.True(t, grokChatResponsesRuntimeEligible("grok-4.6", "isolated-id"))
	require.True(t, grokChatResponsesRuntimeEligible("grok-4.6-latest", "isolated-id"))
	require.True(t, grokChatResponsesRuntimeEligible("xai/grok-4.6", "isolated-id"))
	require.True(t, grokChatResponsesRuntimeEligible("grok/grok-4.6", "isolated-id"))
	require.False(t, grokChatResponsesRuntimeEligible("grok-4.3", "isolated-id"))
	require.False(t, grokChatResponsesRuntimeEligible("grok-4.6", ""))
}

func TestForwardGrokOAuthVisionChatUsesResponsesInputImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))

	account := &Account{
		ID:          701,
		Name:        "grok-oauth-vision",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":      xai.DefaultCLIBaseURL,
		},
	}
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_grok_vision", "grok-4.5")}
	svc := &OpenAIGatewayService{
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "input_image", gjson.GetBytes(upstream.lastBody, "input.0.content.1.type").String())
	require.Equal(t, "data:image/png;base64,QUJD", gjson.GetBytes(upstream.lastBody, "input.0.content.1.image_url").String())
	require.Equal(t, "grok-4.5", result.UpstreamModel)
	require.Contains(t, recorder.Body.String(), "ok")
}

func TestForwardGrokChatV0162ToolsAndTraeFieldsUseResponsesCacheRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":"find alpha"}],"instructions":"Return concise JSON","service_tier":"fast","response_format":{"type":"json_object"},"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup a value","parameters":{"type":"object"}}}],"tool_choice":"auto","parallel_tool_calls":true,"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 703})

	account := &Account{
		ID: 703, Name: "grok-free-tools", Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token", "refresh_token": "refresh-token",
			"expires_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":   xai.DefaultCLIBaseURL, "subscription_tier": "free",
		},
	}
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_grok_tools", "grok-4.5")}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream, grokTokenProvider: NewGrokTokenProvider(repo, nil), accountRepo: repo,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "Return concise JSON", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, "json_object", gjson.GetBytes(upstream.lastBody, "text.format.type").String())
	require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Bool())
	tools := gjson.GetBytes(upstream.lastBody, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

func TestForwardGrokOAuthCustomBaseURLKeepsRawChatCompletions(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))

	account := &Account{
		ID:          702,
		Name:        "grok-oauth-custom-upstream",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
			"base_url":      "https://grok-proxy.example/v1",
		},
	}
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
		accountsByID: map[int64]*Account{account.ID: account},
	}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewBufferString(
			`{"id":"chatcmpl_custom","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		cfg:               rawChatCompletionsTestConfig(),
		httpUpstream:      upstream,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		accountRepo:       repo,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://grok-proxy.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "image_url", gjson.GetBytes(upstream.lastBody, "messages.0.content.1.type").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Contains(t, recorder.Body.String(), "chatcmpl_custom")
}
