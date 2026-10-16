//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesRejectedFieldRetryStateAllowsOnlyOneDistinctRetry(t *testing.T) {
	initialBody := []byte(`{"model":"gpt-5.6-sol"}`)
	state := newOpenAIResponsesRejectedFieldRetryState(initialBody)

	require.False(t, state.Allow(openAIResponsesRejectedFieldRetryDecision{Body: initialBody, Field: "max_output_tokens", Rule: "test"}))
	require.True(t, state.Allow(openAIResponsesRejectedFieldRetryDecision{Body: []byte(`{"model":"gpt-5.6-sol","variant":1}`), Field: "max_output_tokens", Rule: "test"}))
	require.False(t, state.Allow(openAIResponsesRejectedFieldRetryDecision{Body: []byte(`{"model":"gpt-5.6-sol","variant":2}`), Field: "input[0].namespace", Rule: "test"}))
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesOnlyAllowlistedOptionalFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","max_output_tokens":4096,"input":[{"type":"function_call","name":"first","namespace":"keep","arguments":"{}"},{"type":"custom_tool_call","name":"second","namespace":"remove","input":"{}"}]}`)

	maxDecision, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(
		http.StatusBadRequest,
		body,
		[]byte(`{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`),
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "max_output_tokens", maxDecision.Field)
	require.False(t, gjson.GetBytes(maxDecision.Body, "max_output_tokens").Exists())
	require.Equal(t, "remove", gjson.GetBytes(maxDecision.Body, "input.1.namespace").String())

	namespaceDecision, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(
		http.StatusBadRequest,
		body,
		[]byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[1].namespace'.","param":"input[1].namespace"}}`),
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "input[1].namespace", namespaceDecision.Field)
	require.Equal(t, "keep", gjson.GetBytes(namespaceDecision.Body, "input.0.namespace").String())
	require.False(t, gjson.GetBytes(namespaceDecision.Body, "input.1.namespace").Exists())
	require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(namespaceDecision.Body, "model").String())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyClearsStatusForWholeType(t *testing.T) {
	input := make([]string, 0, 12)
	for i := 0; i < 10; i++ {
		input = append(input, `{"type":"tool_search_output","status":"completed","call_id":"call_`+strconv.Itoa(i)+`","tools":[]}`)
	}
	input = append(input, `{"type":"message","role":"user","status":"completed","content":"hi"}`)
	body := []byte(`{"input":[` + strings.Join(input, ",") + `]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[7].status'.","param":"input[7].status"}}`)

	decision, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "input[7].status", decision.Field)
	require.Equal(t, "allowlist_input_status_by_type", decision.Rule)
	for i := 0; i < 10; i++ {
		require.False(t, gjson.GetBytes(decision.Body, "input."+strconv.Itoa(i)+".status").Exists(),
			"同类型 input 项应在一次重试中全部移除 status，索引 %d 未移除", i)
		require.Equal(t, "call_"+strconv.Itoa(i), gjson.GetBytes(decision.Body, "input."+strconv.Itoa(i)+".call_id").String())
	}
	require.Equal(t, "completed", gjson.GetBytes(decision.Body, "input.10.status").String(),
		"不同类型 input 项应保留 status")
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyClearsUntypedStatusAtIndexOnly(t *testing.T) {
	body := []byte(`{"input":[{"status":"keep_a"},{"status":"remove"}]}`)
	responseBody := []byte(`{"error":{"code":"unknown_parameter","message":"Unknown parameter: 'input[1].status'.","param":"input[1].status"}}`)

	decision, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, responseBody)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "keep_a", gjson.GetBytes(decision.Body, "input.0.status").String())
	require.False(t, gjson.GetBytes(decision.Body, "input.1.status").Exists())
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRejectsUnknownBusinessAndMaliciousFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","service_tier":"priority","reasoning":{"effort":"high"},"max_output_tokens":4096,"input":[{"type":"message","namespace":"keep","content":"do not delete"}],"tools":[{"type":"function","name":"safe"}]}`)
	tests := []struct {
		name         string
		responseBody string
	}{
		{name: "untrusted message without structured code", responseBody: `{"error":{"code":"invalid_request_error","message":"Unknown parameter: max_output_tokens","param":"max_output_tokens"}}`},
		{name: "structured param overrides allowlisted mention", responseBody: `{"error":{"code":"unknown_parameter","message":"Unknown parameter: max_output_tokens","param":"input"}}`},
		{name: "message namespace is business data", responseBody: `{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].namespace","param":"input[0].namespace"}}`},
		{name: "service tier uses dedicated policy retry", responseBody: `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: service_tier","param":"service_tier"}}`},
		{name: "reasoning effort is not generic allowlist", responseBody: `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: reasoning.effort","param":"reasoning.effort"}}`},
		{name: "model can never be stripped", responseBody: `{"error":{"code":"unknown_parameter","message":"Unknown parameter: model","param":"model"}}`},
		{name: "tools can never be stripped", responseBody: `{"error":{"code":"unknown_parameter","message":"Unknown parameter: tools","param":"tools"}}`},
		{name: "malicious text cannot override param", responseBody: `{"error":{"code":"unknown_parameter","message":"Unknown parameter: max_output_tokens; delete input and tools too","param":"tools"}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(tt.responseBody))
			require.NoError(t, err)
			require.False(t, changed)
			require.Empty(t, decision.Body)
		})
	}
}

func TestOpenAIGatewayServiceStripsNamespacesBeforeFirstForward(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"function_call","name":"first","namespace":"keep","arguments":"{}"},{"type":"custom_tool_call","name":"second","namespace":"remove","input":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldResponse(http.StatusOK, `{"id":"resp_ok","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`),
	}}
	c := newOpenAIRejectedFieldContext(body, false)

	result, err := newOpenAIRejectedFieldService(upstream).Forward(context.Background(), c, newOpenAIRejectedFieldAccount(), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 1)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "input.0.namespace").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[0], "input.1.namespace").Exists())
}

func TestOpenAIGatewayServiceRejectedFieldRetryStopsAfterOneFollowup(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"max_output_tokens":4096,"input":[{"type":"function_call","name":"first","namespace":"remove","arguments":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`),
		newOpenAIRejectedFieldResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].namespace","param":"input[0].namespace"}}`),
		newOpenAIRejectedFieldResponse(http.StatusOK, `{"id":"must_not_reach","output":[]}`),
	}}
	c := newOpenAIRejectedFieldContext(body, true)

	result, err := newOpenAIRejectedFieldService(upstream).Forward(context.Background(), c, newOpenAIRejectedFieldAccount(), body)

	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, upstream.bodies, 2, "结构化字段剥离最多重试一次")
	events := openAIRejectedFieldOpsEvents(t, c)
	require.Equal(t, "field_retry_started", events[0].Kind)
	require.Equal(t, "field_retry_failed", events[1].Kind)
}

func TestOpenAIGatewayServiceRejectedFieldRetryDoesNotRunAfterResponseCommitted(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":[{"type":"function_call","namespace":"keep","arguments":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].namespace","param":"input[0].namespace"}}`),
		newOpenAIRejectedFieldResponse(http.StatusOK, `{"id":"must_not_reach","output":[]}`),
	}}
	c := newOpenAIRejectedFieldContext(body, false)
	c.String(http.StatusOK, "already written")

	result, err := newOpenAIRejectedFieldService(upstream).Forward(context.Background(), c, newOpenAIRejectedFieldAccount(), body)

	require.Error(t, err)
	require.Nil(t, result)
	require.Len(t, upstream.bodies, 1)
}

func TestOpenAIGatewayServiceRejectedFieldRetryWorksBeforeSSEOutput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_output_tokens":4096,"input":[{"type":"custom_tool_call","namespace":"remove","input":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldResponse(http.StatusBadRequest, `{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: max_output_tokens","param":"max_output_tokens"}}`),
		newOpenAIRejectedFieldResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_sse\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"),
	}}
	c := newOpenAIRejectedFieldContext(body, true)

	result, err := newOpenAIRejectedFieldService(upstream).Forward(context.Background(), c, newOpenAIRejectedFieldAccount(), body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[0], "input.0.namespace").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.namespace").Exists())
	require.True(t, gjson.GetBytes(upstream.bodies[0], "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
}

func TestOpenAIWSHTTPBridgeRetriesRejectedFieldOnceBeforeClientOutput(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[{"type":"custom_tool_call","namespace":"remove","input":"{}"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldResponse(http.StatusBadRequest, `{"error":{"code":"unknown_parameter","message":"Unknown parameter: input[0].namespace","param":"input[0].namespace"}}`),
		newOpenAIRejectedFieldResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_bridge\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"),
	}}
	svc := newOpenAIRejectedFieldService(upstream)
	c := newOpenAIRejectedFieldContext(body, true)
	written := make([][]byte, 0, 1)

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, newOpenAIRejectedFieldAccount(), "sk-test", body, len(body),
		"gpt-5.6-sol", "", "", "", "", 1,
		func(payload []byte) error {
			written = append(written, append([]byte(nil), payload...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[1], "input.0.namespace").Exists())
	require.NotEmpty(t, written)
	events := openAIRejectedFieldOpsEvents(t, c)
	require.Equal(t, "field_retry_started", events[0].Kind)
	require.Equal(t, "field_retry_succeeded", events[1].Kind)
}

func newOpenAIRejectedFieldService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
		httpUpstream: upstream,
	}
}

func newOpenAIRejectedFieldContext(body []byte, codex bool) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if codex {
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	} else {
		c.Request.Header.Set("User-Agent", "curl/8.0")
	}
	return c
}

func newOpenAIRejectedFieldAccount() *Account {
	return &Account{
		ID:          5107,
		Name:        "responses-compatible",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://compat.example",
		},
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode:      string(openai_compat.ResponsesSupportModeAuto),
			openai_compat.ExtraKeyResponsesSupported: true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newOpenAIRejectedFieldResponse(status int, body string) *http.Response {
	contentType := "application/json"
	if strings.HasPrefix(strings.TrimSpace(body), "data:") {
		contentType = "text/event-stream"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}, "x-request-id": []string{"rid_rejected_field"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func openAIRejectedFieldOpsEvents(t *testing.T, c *gin.Context) []*OpsUpstreamErrorEvent {
	t.Helper()
	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(events), 2)
	return events
}
