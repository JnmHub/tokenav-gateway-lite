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
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsChatCompletionsReasoningAuditSeparatesRequestedAndFinal(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"max","stream":false}`)
	finalBody, changed := ApplyOpenAIReasoningEffortPolicy(body, "high", nil)
	require.True(t, changed)

	ctx := gatewaylite.ContextWithReasoningEffortAudit(context.Background(), "max", "")
	result, upstreamBody, err := forwardReasoningAuditChatRequest(ctx, finalBody, "gpt-5.4")

	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(upstreamBody, "reasoning.effort").String())
	requireOptionalStringEqual(t, "max", result.RequestedReasoningEffort)
	requireOptionalStringEqual(t, "high", result.ReasoningEffort)
}

func TestForwardAsChatCompletionsReasoningAuditPreservesNoneAndMinimal(t *testing.T) {
	for _, effort := range []string{"none", "minimal"} {
		t.Run(effort, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"` + effort + `","stream":false}`)
			result, upstreamBody, err := forwardReasoningAuditChatRequest(context.Background(), body, "gpt-5.4")

			require.NoError(t, err)
			require.Equal(t, effort, gjson.GetBytes(upstreamBody, "reasoning.effort").String())
			requireOptionalStringEqual(t, effort, result.RequestedReasoningEffort)
			requireOptionalStringEqual(t, effort, result.ReasoningEffort)
		})
	}
}

func TestForwardAsChatCompletionsReasoningAuditDoesNotInferFromModel(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4-high","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	result, upstreamBody, err := forwardReasoningAuditChatRequest(context.Background(), body, "gpt-5.4-high")

	require.NoError(t, err)
	require.False(t, gjson.GetBytes(upstreamBody, "reasoning.effort").Exists())
	require.Nil(t, result.RequestedReasoningEffort)
	require.Nil(t, result.ReasoningEffort)
}

func TestForwardAsAnthropicReasoningAuditUsesFinalResponsesBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"output_config":{"effort":"max"},"stream":false}`)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_messages_audit", "gpt-5.4")}
	service := &OpenAIGatewayService{cfg: reasoningAuditTestConfig(), httpUpstream: upstream}
	account := reasoningAuditOpenAIAPIKeyAccount()
	ctx := gatewaylite.ContextWithReasoningEffortAudit(context.Background(), "max", "")
	ctx = WithOpenAIReasoningEffortPolicy(ctx, "high", nil)

	result, err := service.ForwardAsAnthropic(ctx, ginContext, account, body, "", "")

	require.NoError(t, err)
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
	requireOptionalStringEqual(t, "max", result.RequestedReasoningEffort)
	requireOptionalStringEqual(t, "high", result.ReasoningEffort)
}

func TestForwardGrokChatResponsesBridgeDoesNotInferReasoningFromModelAlias(t *testing.T) {
	body := []byte(`{"model":"grok-high","messages":[{"role":"user","content":"describe"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))

	account := &Account{
		ID: 907, Name: "grok-reasoning-audit", Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "xai-api-key",
			"base_url": "https://api.x.ai/v1",
			"model_mapping": map[string]any{
				"grok-high": "grok-4.5",
			},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_grok_audit","object":"chat.completion","model":"grok-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		)),
	}}
	service := &OpenAIGatewayService{
		cfg:          reasoningAuditTestConfig(),
		httpUpstream: upstream,
	}

	result, err := service.ForwardAsChatCompletions(context.Background(), ginContext, account, body, "", "")

	require.NoError(t, err)
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
	require.Nil(t, result.RequestedReasoningEffort)
	require.Nil(t, result.ReasoningEffort)
}

func forwardReasoningAuditChatRequest(ctx context.Context, body []byte, responseModel string) (*OpenAIForwardResult, []byte, error) {
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_chat_audit", responseModel)}
	service := &OpenAIGatewayService{cfg: reasoningAuditTestConfig(), httpUpstream: upstream}
	result, err := service.ForwardAsChatCompletions(ctx, ginContext, reasoningAuditOpenAIAPIKeyAccount(), body, "", "")
	return result, upstream.lastBody, err
}

func reasoningAuditOpenAIAPIKeyAccount() *Account {
	return &Account{
		ID: 906, Name: "openai-reasoning-audit", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-audit"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}
}

func reasoningAuditTestConfig() *config.Config {
	return &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
}

func requireOptionalStringEqual(t *testing.T, expected string, actual *string) {
	t.Helper()
	require.NotNil(t, actual)
	require.Equal(t, expected, *actual)
}
