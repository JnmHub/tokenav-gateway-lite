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

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesLiteTools_MovesNamespacesAndNormalizesReasoning(t *testing.T) {
	reqBody := map[string]any{
		"reasoning": map[string]any{"effort": "high", "context": "current_turn"},
		"tools": []any{
			map[string]any{"type": "function", "name": "lookup"},
			map[string]any{"type": "namespace", "name": "collaboration", "tools": []any{
				map[string]any{"type": "function", "name": "spawn_agent"},
			}},
		},
		"input": "hello",
	}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "high", reqBody["reasoning"].(map[string]any)["effort"])
	require.Equal(t, "all_turns", reqBody["reasoning"].(map[string]any)["context"])
	require.Len(t, reqBody["tools"].([]any), 1)
	require.Equal(t, "function", reqBody["tools"].([]any)[0].(map[string]any)["type"])
	input := reqBody["input"].([]any)
	require.Equal(t, "message", input[0].(map[string]any)["type"])
	require.Equal(t, "additional_tools", input[1].(map[string]any)["type"])
	require.Equal(t, "collaboration", input[1].(map[string]any)["tools"].([]any)[0].(map[string]any)["name"])
}

func TestNormalizeOpenAIResponsesLiteTools_AddsReasoningWithoutTools(t *testing.T) {
	reqBody := map[string]any{"model": "gpt-5.1", "input": "hello"}

	changed, err := normalizeOpenAIResponsesLiteTools(reqBody)

	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "all_turns", reqBody["reasoning"].(map[string]any)["context"])
}

func TestNormalizeOpenAIResponsesLiteTools_RejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "reasoning must be object",
			body: map[string]any{"reasoning": "high"},
			want: "reasoning to be an object",
		},
		{
			name: "hosted web search is not silently dropped",
			body: map[string]any{"tools": []any{map[string]any{"type": "web_search"}}},
			want: `does not support top-level tool type "web_search"`,
		},
		{
			name: "conflicting additional tool definitions",
			body: map[string]any{
				"tools": []any{map[string]any{"type": "namespace", "name": "collaboration", "description": "new"}},
				"input": []any{map[string]any{
					"type":  "additional_tools",
					"tools": []any{map[string]any{"type": "namespace", "name": "collaboration", "description": "old"}},
				}},
			},
			want: "conflicts with migrated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := normalizeOpenAIResponsesLiteTools(tt.body)
			require.Error(t, err)
			require.False(t, changed)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestNormalizeOpenAIResponsesLiteToolsPayload_DeduplicatesEquivalentNamespace(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.1",
		"reasoning":{"effort":"medium","context":"current_turn"},
		"tools":[{"type":"namespace","name":"collaboration"}],
		"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration"}]},
			{"type":"message","role":"user","content":"hello"}
		]
	}`)

	updated, changed, err := normalizeOpenAIResponsesLiteToolsPayload(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(updated, `tools.#(type=="namespace")`).Exists())
	require.Equal(t, 1, len(gjson.GetBytes(updated, `input.#(type=="additional_tools").tools`).Array()))
	require.Equal(t, "all_turns", gjson.GetBytes(updated, "reasoning.context").String())
}

func TestOpenAIGatewayService_ResponsesLiteNormalizesHTTPNativeAndPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestBody := []byte(`{
		"model":"gpt-5.1",
		"stream":false,
		"reasoning":{"effort":"high","context":"current_turn"},
		"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent"}]}],
		"input":[{"type":"message","role":"user","content":"hello"}]
	}`)
	responseBody := `{"id":"resp_lite","object":"response","status":"completed","model":"gpt-5.1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`

	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough], func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			c.Request.Header.Set(responsesLiteHeader, "true")

			contentType := "application/json"
			upstreamResponseBody := responseBody
			if passthrough {
				contentType = "text/event-stream"
				upstreamResponseBody = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + responseBody + "}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(strings.NewReader(upstreamResponseBody)),
			}}
			svc := &OpenAIGatewayService{
				cfg:           &config.Config{},
				httpUpstream:  upstream,
				toolCorrector: NewCodexToolCorrector(),
			}
			account := &Account{
				ID: 501, Name: "responses-lite", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "oauth-test"},
				Extra:       map[string]any{"openai_passthrough": passthrough},
			}

			result, err := svc.Forward(context.Background(), c, account, requestBody)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
			require.Equal(t, "all_turns", gjson.GetBytes(upstream.lastBody, "reasoning.context").String())
			require.False(t, gjson.GetBytes(upstream.lastBody, `tools.#(type=="namespace")`).Exists())
			require.Equal(t, "collaboration", gjson.GetBytes(upstream.lastBody, `input.#(type=="additional_tools").tools.0.name`).String())
		})
	}
}
