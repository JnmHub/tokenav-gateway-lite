package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestShouldFlattenOpenAIResponsesNamespaces(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	grokOAuth := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}

	tests := []struct {
		name               string
		account            *Account
		transport          OpenAIUpstreamTransport
		passthroughEnabled bool
		compactPath        bool
		want               bool
	}{
		{name: "oauth_http", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "oauth_http_passthrough", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, passthroughEnabled: true, want: false},
		{name: "oauth_wsv2", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, want: false},
		{name: "oauth_wsv2_passthrough", account: oauth, transport: OpenAIUpstreamTransportResponsesWebsocketV2, passthroughEnabled: true, want: false},
		{name: "oauth_compact", account: oauth, transport: OpenAIUpstreamTransportHTTPSSE, compactPath: true, want: true},
		{name: "apikey_http", account: apiKey, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "grok_oauth", account: grokOAuth, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
		{name: "nil_account", account: nil, transport: OpenAIUpstreamTransportHTTPSSE, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldFlattenOpenAIResponsesNamespaces(tt.account, tt.transport, tt.passthroughEnabled, tt.compactPath))
		})
	}
}

func TestShouldKeepOpenAIResponsesToolCallNamespaces(t *testing.T) {
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.True(t, shouldKeepOpenAIResponsesToolCallNamespaces(oauth, OpenAIUpstreamTransportHTTPSSE, false, false))
	require.False(t, shouldKeepOpenAIResponsesToolCallNamespaces(oauth, OpenAIUpstreamTransportHTTPSSE, false, true))
	require.False(t, shouldKeepOpenAIResponsesToolCallNamespaces(apiKey, OpenAIUpstreamTransportHTTPSSE, false, false))
}

func TestStripOpenAIResponsesInputNamespacesKeepsToolCalls(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","namespace":"collaboration","name":"spawn_agent"},{"type":"message","namespace":"stale","content":"hello"}]}`)
	stripped, err := stripOpenAIResponsesInputNamespaces(body, true)
	require.NoError(t, err)
	require.Equal(t, "collaboration", gjson.GetBytes(stripped, "input.0.namespace").String())
	require.False(t, gjson.GetBytes(stripped, "input.1.namespace").Exists())
}

func TestFlattenAndRestoreOpenAIResponsesNamespaces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{
		"model":"gpt-5.5",
		"tools":[
			{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
		],
		"input":[{"type":"function_call","name":"spawn_agent","namespace":"collaboration","arguments":"{}"}]
	}`)

	flattened, err := flattenOpenAIResponsesNamespaces(c, body)
	require.NoError(t, err)
	require.Equal(t, "namespace", gjson.GetBytes(flattened, "tools.0.type").String())
	require.Equal(t, "image_gen", gjson.GetBytes(flattened, "tools.0.name").String())
	require.Equal(t, "function", gjson.GetBytes(flattened, "tools.1.type").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(flattened, "tools.1.name").String())
	require.Equal(t, "collaboration__spawn_agent", gjson.GetBytes(flattened, "input.0.name").String())
	require.False(t, gjson.GetBytes(flattened, "input.0.namespace").Exists())
	require.Equal(t, apicompat.ResponsesNamespaceName{Namespace: "collaboration", Name: "spawn_agent"}, openAIResponsesNamespaceNames(c)["collaboration__spawn_agent"])

	restored, err := restoreOpenAIResponsesNamespacePayload(c, []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","name":"collaboration__spawn_agent","arguments":"{}"}]}}`))
	require.NoError(t, err)
	require.Equal(t, "spawn_agent", gjson.GetBytes(restored, "response.output.0.name").String())
	require.Equal(t, "collaboration", gjson.GetBytes(restored, "response.output.0.namespace").String())

	clearOpenAIResponsesNamespaceNames(c)
	require.Empty(t, openAIResponsesNamespaceNames(c))
	unchanged, err := restoreOpenAIResponsesNamespacePayload(c, []byte(`{"type":"function_call","name":"collaboration__spawn_agent"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"function_call","name":"collaboration__spawn_agent"}`, string(unchanged))
}

func TestFlattenOpenAIResponsesNamespacesRejectsCollision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"tools":[{"type":"function","name":"collaboration__spawn_agent"},{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}]}`)

	_, err := flattenOpenAIResponsesNamespaces(c, body)
	require.ErrorContains(t, err, "conflicts with a top-level tool")
	require.Empty(t, openAIResponsesNamespaceNames(c))
}

func TestOpenAIResponsesNamespaceCollisionReturnsBadRequestBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	account := &Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra:       map[string]any{"openai_responses_flatten_namespaces": true},
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"function","name":"collaboration__spawn_agent"},{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}]}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.Nil(t, result)
	require.ErrorContains(t, err, "conflicts with a top-level tool")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "tools", gjson.GetBytes(recorder.Body.Bytes(), "error.param").String())
}

func TestOpenAIResponsesNamespaceRestoredInNonStreamingHandlers(t *testing.T) {
	responseBody := `{"id":"resp_1","model":"gpt-5.5","output":[{"type":"function_call","name":"collaboration__spawn_agent","arguments":"{}"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	names := map[string]apicompat.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	t.Run("normal", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		setOpenAIResponsesNamespaceNames(c, names)
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody))}

		_, err := (&OpenAIGatewayService{}).handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5.5", "gpt-5.5")
		require.NoError(t, err)
		require.Equal(t, "spawn_agent", gjson.GetBytes(recorder.Body.Bytes(), "output.0.name").String())
		require.Equal(t, "collaboration", gjson.GetBytes(recorder.Body.Bytes(), "output.0.namespace").String())
	})

	t.Run("passthrough", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		setOpenAIResponsesNamespaceNames(c, names)
		resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(responseBody))}

		_, err := (&OpenAIGatewayService{}).handleNonStreamingResponsePassthrough(context.Background(), resp, c, "gpt-5.5", "gpt-5.5")
		require.NoError(t, err)
		require.Equal(t, "spawn_agent", gjson.GetBytes(recorder.Body.Bytes(), "output.0.name").String())
		require.Equal(t, "collaboration", gjson.GetBytes(recorder.Body.Bytes(), "output.0.namespace").String())
	})
}

func TestOpenAINonStreamingUsageKeepsTextTokensWhenImageToolIsInactive(t *testing.T) {
	responseBody := `{
		"id":"resp_long_context",
		"object":"response",
		"status":"completed",
		"model":"gpt-5.6-sol",
		"output":[],
		"usage":{"input_tokens":285929,"output_tokens":5,"total_tokens":285934},
		"tool_usage":{
			"image_gen":{
				"input_tokens":0,
				"input_tokens_details":{"image_tokens":0,"text_tokens":0},
				"output_tokens":0,
				"output_tokens_details":{"image_tokens":0,"text_tokens":0},
				"total_tokens":0
			},
			"web_search":{"num_requests":0}
		}
	}`
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tt := range []struct {
		name string
		run  func(*OpenAIGatewayService, *http.Response, *gin.Context) (*OpenAIUsage, error)
	}{
		{
			name: "normal",
			run: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) (*OpenAIUsage, error) {
				result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5.6-sol", "gpt-5.6-sol")
				if result == nil {
					return nil, err
				}
				return result.usage, err
			},
		},
		{
			name: "passthrough",
			run: func(svc *OpenAIGatewayService, resp *http.Response, c *gin.Context) (*OpenAIUsage, error) {
				result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, "gpt-5.6-sol", "gpt-5.6-sol")
				if result == nil {
					return nil, err
				}
				return result.usage, err
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(responseBody)),
			}

			usage, err := tt.run(&OpenAIGatewayService{}, resp, c)
			require.NoError(t, err)
			require.NotNil(t, usage)
			require.Equal(t, 285929, usage.InputTokens)
			require.Equal(t, 5, usage.OutputTokens)
		})
	}
}

func TestOpenAIResponsesNamespaceRestoredInStreamingHandlers(t *testing.T) {
	streamBody := "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"collaboration__spawn_agent\",\"arguments\":\"{}\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"function_call\",\"name\":\"collaboration__spawn_agent\",\"arguments\":\"{}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
	names := map[string]apicompat.ResponsesNamespaceName{
		"collaboration__spawn_agent": {Namespace: "collaboration", Name: "spawn_agent"},
	}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	t.Run("normal", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		setOpenAIResponsesNamespaceNames(c, names)
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(streamBody))}
		svc := &OpenAIGatewayService{toolCorrector: NewCodexToolCorrector()}

		_, err := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5.5", "gpt-5.5")
		require.NoError(t, err)
		require.Contains(t, recorder.Body.String(), `"name":"spawn_agent"`)
		require.Contains(t, recorder.Body.String(), `"namespace":"collaboration"`)
		require.NotContains(t, recorder.Body.String(), `"name":"collaboration__spawn_agent"`)
	})

	t.Run("passthrough", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		setOpenAIResponsesNamespaceNames(c, names)
		resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(streamBody))}

		_, err := (&OpenAIGatewayService{}).handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-5.5", "gpt-5.5")
		require.NoError(t, err)
		require.Contains(t, recorder.Body.String(), `"name":"spawn_agent"`)
		require.Contains(t, recorder.Body.String(), `"namespace":"collaboration"`)
		require.NotContains(t, recorder.Body.String(), `"name":"collaboration__spawn_agent"`)
	})
}
