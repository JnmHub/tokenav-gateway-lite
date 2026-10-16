//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func forceChatMessagesFallbackAccount() *Account {
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
	}
	return account
}

type messagesFallbackErrorTailReader struct {
	data []byte
	off  int
	err  error
}

func (r *messagesFallbackErrorTailReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

func (r *messagesFallbackErrorTailReader) Close() error { return nil }

func TestForwardAsAnthropicForceChatCompletionsNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Authorization", "Bearer user-key-must-not-forward")
	c.Request.Header.Set("X-Api-Key", "user-key-must-not-forward")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid_msg_cc_json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_json","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1,"cache_write_tokens":2}}}`,
		)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "http://upstream.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, []string{"Bearer sk-test"}, upstream.lastReq.Header.Values("Authorization"))
	require.Empty(t, upstream.lastReq.Header.Get("X-Api-Key"))
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "stream_options").Exists())
	require.Equal(t, "assistant", gjson.Get(rec.Body.String(), "role").String())
	require.Equal(t, "ok", gjson.Get(rec.Body.String(), "content.0.text").String())
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Equal(t, 1, result.Usage.CacheReadInputTokens)
	require.Equal(t, 2, result.Usage.CacheCreationInputTokens)
	require.False(t, result.Stream)
}

func TestForwardAsAnthropicForceChatCompletionsStreamingAndToolCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"weather?"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamBody := strings.Join([]string{
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"sf\"}"}}]},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
		`data: {"id":"chatcmpl_t","object":"chat.completion.chunk","model":"gpt-5.4","choices":[],"usage":{"prompt_tokens":6,"completion_tokens":5,"total_tokens":11}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_msg_cc_stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	out := rec.Body.String()
	require.Contains(t, out, `"type":"tool_use"`)
	require.Contains(t, out, `"name":"get_weather"`)
	require.Contains(t, out, `"input_json_delta"`)
	require.Contains(t, out, `"stop_reason":"tool_use"`)
	require.Contains(t, out, "event: message_stop")
	require.Equal(t, 6, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
	require.True(t, result.Stream)
	require.NotNil(t, result.FirstTokenMs)
}

func TestForwardAsAnthropicForceChatCompletionsStreamReadErrorSkipsFinalize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	partial := strings.Join([]string{
		`data: {"id":"chatcmpl_e","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl_e","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"he"},"finish_reason":null}]}`,
		"",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: &messagesFallbackErrorTailReader{
			data: []byte(partial),
			err:  errors.New("simulated upstream read failure"),
		},
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream usage incomplete")
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"text":"he"`)
	require.NotContains(t, rec.Body.String(), "event: message_stop")
}

func TestForwardAsAnthropicResponsesSupportedAccountKeepsResponsesEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_native","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := rawChatCompletionsTestAccount()
	account.Extra = map[string]any{
		openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
		openai_compat.ExtraKeyResponsesSupported: true,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, strings.HasSuffix(upstream.lastReq.URL.Path, "/responses"))
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
}

func TestForwardAsAnthropicForceChatCompletionsPreservesFastModePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("anthropic-beta", "fast-mode-2026-02-01")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_fast","object":"chat.completion","model":"gpt-5.4","service_tier":"priority","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, forceChatMessagesFallbackAccount(), body, "", "")
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(upstream.lastBody, "service_tier").String())
	require.NotNil(t, result.ServiceTier)
	require.Equal(t, "priority", *result.ServiceTier)
}

func TestForwardAsAnthropicForceChatCompletionsMappedModelKeepsClientModelAndAuditsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		stream     bool
		body       string
		upstream   string
		assertBody func(*testing.T, string)
	}{
		{
			name:     "non-streaming",
			body:     `{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`,
			upstream: `{"id":"chatcmpl_map","object":"chat.completion","model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
			assertBody: func(t *testing.T, body string) {
				require.Equal(t, "gpt-5.6-sol", gjson.Get(body, "model").String())
			},
		},
		{
			name:   "streaming",
			stream: true,
			body:   `{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`,
			upstream: strings.Join([]string{
				`data: {"id":"chatcmpl_map","object":"chat.completion.chunk","model":"gpt-5.6-luna","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
				"",
				`data: {"id":"chatcmpl_map","object":"chat.completion.chunk","model":"gpt-5.6-luna","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
				"",
				"data: [DONE]",
				"",
			}, "\n"),
			assertBody: func(t *testing.T, body string) {
				require.Contains(t, body, `"model":"gpt-5.6-sol"`)
				require.NotContains(t, body, `"model":"gpt-5.6-luna"`)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.body)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{"Content-Type": []string{func() string {
					if test.stream {
						return "text/event-stream"
					}
					return "application/json"
				}()}},
				Body: io.NopCloser(strings.NewReader(test.upstream)),
			}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			account := forceChatMessagesFallbackAccount()
			account.Credentials["model_mapping"] = map[string]any{"gpt-5.6-sol": "gpt-5.6-luna"}

			result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(upstream.lastBody, "model").String())
			test.assertBody(t, rec.Body.String())
			require.Equal(t, "gpt-5.6-luna", result.UpstreamResponseModel)
			require.NotNil(t, result.UpstreamModelMismatch)
			require.False(t, *result.UpstreamModelMismatch)
		})
	}
}
