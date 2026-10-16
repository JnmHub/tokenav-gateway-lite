//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	nativeAnthropicRequestedModel = "gpt-5.6-sol"
	nativeAnthropicUpstreamModel  = "gpt-5.6-luna"
)

func nativeAnthropicResponseModelTestAccount() *Account {
	return &Account{
		ID:          901,
		Name:        "native-anthropic-response-model",
		Platform:    PlatformDeepseek,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": APIProtocolAnthropic,
			"base_url":     "http://anthropic.example",
			"model_mapping": map[string]any{
				nativeAnthropicRequestedModel: nativeAnthropicUpstreamModel,
			},
		},
	}
}

func nativeAnthropicResponseModelTestContext(path string, body []byte) (*gin.Context, *httptest.ResponseRecorder, context.Context) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestCtx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest},
	})
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(requestCtx)
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder, requestCtx
}

func nativeAnthropicMessageStartEvent(model string) string {
	return `event: message_start
data: {"type":"message_start","message":{"id":"msg_native","type":"message","role":"assistant","content":[],"model":"` + model + `","stop_reason":null,"usage":{"input_tokens":2,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
}

func TestForwardAnthropicViaNativeEndpoint_MappedModelKeepsRequestedModelBuffered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, recorder, requestCtx := nativeAnthropicResponseModelTestContext("/v1/messages", body)
	upstreamBody := `{"id":"msg_native","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"gpt-5.6-luna","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-native-msg-json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}
	service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
	account := nativeAnthropicResponseModelTestAccount()

	result, err := service.forwardAnthropicViaNativeAnthropicEndpoint(requestCtx, c, account, body, "")

	require.NoError(t, err)
	require.Equal(t, nativeAnthropicUpstreamModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, nativeAnthropicRequestedModel, gjson.GetBytes(recorder.Body.Bytes(), "model").String())
	require.Equal(t, nativeAnthropicUpstreamModel, result.UpstreamResponseModel)
	require.NotNil(t, result.UpstreamModelMismatch)
	require.False(t, *result.UpstreamModelMismatch)
}

func TestForwardAnthropicViaNativeEndpoint_MappedModelKeepsRequestedModelStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c, recorder, requestCtx := nativeAnthropicResponseModelTestContext("/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-native-msg-sse"}},
		Body:       io.NopCloser(strings.NewReader(nativeAnthropicMessageStartEvent(nativeAnthropicUpstreamModel))),
	}}
	service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
	account := nativeAnthropicResponseModelTestAccount()

	result, err := service.forwardAnthropicViaNativeAnthropicEndpoint(requestCtx, c, account, body, "")

	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"model":"`+nativeAnthropicRequestedModel+`"`)
	require.NotContains(t, recorder.Body.String(), `"model":"`+nativeAnthropicUpstreamModel+`"`)
	require.Equal(t, nativeAnthropicUpstreamModel, result.UpstreamResponseModel)
}

func TestForwardChatAndResponsesViaNativeAnthropic_KeepRequestedModelAndAuditUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		path        string
		body        []byte
		forward     func(*OpenAIGatewayService, context.Context, *gin.Context, *Account, []byte) (*OpenAIForwardResult, error)
		assertModel func(*testing.T, string)
	}{
		{
			name: "chat completions buffered",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			forward: func(service *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return service.forwardChatCompletionsViaNativeAnthropic(ctx, c, account, body, "")
			},
			assertModel: func(t *testing.T, response string) {
				require.Equal(t, nativeAnthropicRequestedModel, gjson.Get(response, "model").String())
			},
		},
		{
			name: "chat completions streaming",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}],"stream":true}`),
			forward: func(service *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return service.forwardChatCompletionsViaNativeAnthropic(ctx, c, account, body, "")
			},
			assertModel: func(t *testing.T, response string) {
				require.Contains(t, response, `"model":"`+nativeAnthropicRequestedModel+`"`)
				require.NotContains(t, response, `"model":"`+nativeAnthropicUpstreamModel+`"`)
			},
		},
		{
			name: "responses buffered",
			path: "/v1/responses",
			body: []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":false}`),
			forward: func(service *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return service.forwardResponsesViaNativeAnthropic(ctx, c, account, body, "")
			},
			assertModel: func(t *testing.T, response string) {
				require.Equal(t, nativeAnthropicRequestedModel, gjson.Get(response, "model").String())
			},
		},
		{
			name: "responses streaming",
			path: "/v1/responses",
			body: []byte(`{"model":"gpt-5.6-sol","input":"hello","stream":true}`),
			forward: func(service *OpenAIGatewayService, ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
				return service.forwardResponsesViaNativeAnthropic(ctx, c, account, body, "")
			},
			assertModel: func(t *testing.T, response string) {
				require.Contains(t, response, `"model":"`+nativeAnthropicRequestedModel+`"`)
				require.NotContains(t, response, `"model":"`+nativeAnthropicUpstreamModel+`"`)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, recorder, requestCtx := nativeAnthropicResponseModelTestContext(test.path, test.body)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid-native-bridge"}},
				Body:       io.NopCloser(strings.NewReader(nativeAnthropicMessageStartEvent(nativeAnthropicUpstreamModel))),
			}}
			service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
			account := nativeAnthropicResponseModelTestAccount()

			result, err := test.forward(service, requestCtx, c, account, test.body)

			require.NoError(t, err)
			require.Equal(t, nativeAnthropicUpstreamModel, gjson.GetBytes(upstream.lastBody, "model").String())
			test.assertModel(t, recorder.Body.String())
			require.Equal(t, nativeAnthropicUpstreamModel, result.UpstreamResponseModel)
			require.NotNil(t, result.UpstreamModelMismatch)
			require.False(t, *result.UpstreamModelMismatch)
		})
	}
}

func TestForwardAnthropicViaNativeEndpoint_HonorsExplicitResponseModelModes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name      string
		mode      string
		custom    string
		wantModel string
	}{
		{name: "upstream", mode: ResponseModelModeUpstream, wantModel: nativeAnthropicUpstreamModel},
		{name: "custom", mode: ResponseModelModeCustom, custom: "public-alias", wantModel: "public-alias"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.6-sol","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
			c, recorder, requestCtx := nativeAnthropicResponseModelTestContext("/v1/messages", body)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"msg_native","type":"message","role":"assistant","content":[],"model":"gpt-5.6-luna","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`,
				)),
			}}
			service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
			account := nativeAnthropicResponseModelTestAccount()
			account.Extra = map[string]any{ResponseModelModeExtraKey: test.mode}
			if test.custom != "" {
				account.Extra[ResponseModelCustomExtraKey] = test.custom
			}

			result, err := service.forwardAnthropicViaNativeAnthropicEndpoint(requestCtx, c, account, body, "")

			require.NoError(t, err)
			require.Equal(t, test.wantModel, gjson.GetBytes(recorder.Body.Bytes(), "model").String())
			require.Equal(t, nativeAnthropicUpstreamModel, result.UpstreamResponseModel)
		})
	}
}
