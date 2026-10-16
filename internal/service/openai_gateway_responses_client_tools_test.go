package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func openAIAPIKeyClientToolsRequest(stream bool) []byte {
	streamValue := "false"
	if stream {
		streamValue = "true"
	}
	return []byte(`{"model":"gpt-5.6-sol","stream":` + streamValue + `,"input":"run tools","tools":[{"type":"custom","name":"exec"},{"type":"tool_search"},{"type":"namespace","name":"team","tools":[{"type":"function","name":"send","parameters":{"type":"object"}}]}]}`)
}

func openAIAPIKeyClientToolsService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
		httpUpstream: upstream,
	}
}

func openAIAPIKeyClientToolsAccount() *Account {
	return &Account{
		ID:          918001,
		Name:        "client-tools-api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-client-tools-test"},
	}
}

func assertOpenAIAPIKeyClientToolsLowered(t *testing.T, body []byte) {
	t.Helper()
	require.Equal(t, "function", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "exec", gjson.GetBytes(body, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(body, "tools.1.type").String())
	require.Equal(t, "tool_search", gjson.GetBytes(body, "tools.1.name").String())
	require.Equal(t, "function", gjson.GetBytes(body, "tools.2.type").String())
	require.Equal(t, "team__send", gjson.GetBytes(body, "tools.2.name").String())
	require.False(t, gjson.GetBytes(body, `tools.#(type=="custom")`).Exists())
	require.False(t, gjson.GetBytes(body, `tools.#(type=="tool_search")`).Exists())
	require.False(t, gjson.GetBytes(body, `tools.#(type=="namespace")`).Exists())
}

func TestOpenAIPassthroughAPIKeyRestoresClientToolsNonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := openAIAPIKeyClientToolsRequest(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_client_tools","status":"completed","output":[
			{"type":"function_call","id":"i_exec","call_id":"c_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"},
			{"type":"function_call","id":"i_search","call_id":"c_search","name":"tool_search","arguments":"{\"query\":\"docs\"}"},
			{"type":"function_call","id":"i_namespace","call_id":"c_namespace","name":"team__send","arguments":"{}"}],"usage":{}}`)),
	}}

	result, err := openAIAPIKeyClientToolsService(upstream).forwardOpenAIPassthrough(
		context.Background(), c, openAIAPIKeyClientToolsAccount(), body, body, "gpt-5.6-sol", false, nil, false, time.Now(),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assertOpenAIAPIKeyClientToolsLowered(t, upstream.lastBody)
	response := recorder.Body.Bytes()
	require.Equal(t, "custom_tool_call", gjson.GetBytes(response, "output.0.type").String())
	require.Equal(t, "pwd", gjson.GetBytes(response, "output.0.input").String())
	require.Equal(t, "tool_search_call", gjson.GetBytes(response, "output.1.type").String())
	require.Equal(t, "docs", gjson.GetBytes(response, "output.1.arguments.query").String())
	require.Equal(t, "team", gjson.GetBytes(response, "output.2.namespace").String())
	require.Equal(t, "send", gjson.GetBytes(response, "output.2.name").String())
}

func TestOpenAIPassthroughAPIKeyRestoresClientToolsStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := openAIAPIKeyClientToolsRequest(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))

	sse := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"i_exec","call_id":"c_exec","name":"exec","status":"in_progress"}}`,
		"",
		`event: response.function_call_arguments.done`,
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"output_index":0,"item_id":"i_exec","call_id":"c_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"}`,
		"",
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"i_exec","call_id":"c_exec","name":"exec","arguments":"{\"input\":\"pwd\"}","status":"completed"}}`,
		"",
		`event: response.done`,
		`data: {"type":"response.done","sequence_number":3,"response":{"id":"resp_client_tools_stream","status":"completed","output":[{"type":"function_call","id":"i_exec","call_id":"c_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}

	result, err := openAIAPIKeyClientToolsService(upstream).forwardOpenAIPassthrough(
		context.Background(), c, openAIAPIKeyClientToolsAccount(), body, body, "gpt-5.6-sol", false, nil, true, time.Now(),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	assertOpenAIAPIKeyClientToolsLowered(t, upstream.lastBody)
	output := recorder.Body.String()
	require.Contains(t, output, `"type":"custom_tool_call"`)
	require.Contains(t, output, `"type":"response.custom_tool_call_input.done"`)
	require.Contains(t, output, `"input":"pwd"`)
	require.NotContains(t, output, `"input":{"input"`)
	require.Contains(t, output, `"type":"response.done"`)
}

func TestOpenAIPassthroughRecognizesSSEEventFieldTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		// A few compatible upstreams only set the event name and omit data.type.
		// The payload deliberately has a non-empty output so this verifies terminal
		// detection rather than the separate empty-completion failover safeguard.
		Body: io.NopCloser(strings.NewReader("event: response.completed\n" +
			`data: {"response":{"id":"resp_event_field","model":"gpt-5.6-sol","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n")),
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	result, err := svc.handleStreamingResponsePassthrough(
		context.Background(), resp, c, openAIAPIKeyClientToolsAccount(), time.Now(), "gpt-5.6-sol", "gpt-5.6-sol",
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_event_field", result.responseID)
	require.Contains(t, recorder.Body.String(), "resp_event_field")
}

func TestAnthropicSSEOverloadedErrorUses529OnlyBeforeOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			`data: {"type":"error","error":{"type":"overloaded_error","message":"upstream overloaded"}}` + "\n\n",
		)),
	}
	svc := &GatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}

	_, err := svc.handleStreamingResponse(
		context.Background(), resp, c, &Account{ID: 918003}, time.Now(), "claude-test", "claude-test", false,
	)
	var streamErr *sseStreamErrorEventError
	require.ErrorAs(t, err, &streamErr)
	require.True(t, streamErr.Overloaded)
	require.Equal(t, 529, anthropicSSEStreamErrorStatus(streamErr, false))
	require.Equal(t, http.StatusForbidden, anthropicSSEStreamErrorStatus(streamErr, true))

	nonOverloaded := newSSEStreamErrorEventError(`{"type":"error","error":{"type":"invalid_request_error"}}`)
	require.False(t, nonOverloaded.Overloaded)
	require.Equal(t, http.StatusForbidden, anthropicSSEStreamErrorStatus(nonOverloaded, false))
}
