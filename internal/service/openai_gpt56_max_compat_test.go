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
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIReasoningEffortPreservesGPT56Max(t *testing.T) {
	require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("MAX", "openai/gpt-5.6-sol"))
	require.Equal(t, "max", normalizeOpenAIReasoningEffortForModel("max", "gpt-5.6-terra-2026-07-10"))
	require.Equal(t, "xhigh", normalizeOpenAIReasoningEffortForModel("max", "gpt-5.5"))
}

func TestDeriveOpenAIReasoningEffortFromGPT56MaxSuffix(t *testing.T) {
	require.Equal(t, "max", deriveOpenAIReasoningEffortFromModel("gpt-5.6-luna-max"))
	require.Equal(t, "max", deriveOpenAIReasoningEffortFromModel("openai/gpt-5.6-sol-2026-07-10-max"))
}

func TestExtractOpenAIReasoningEffortFallsBackAcrossModelCandidates(t *testing.T) {
	effort := extractOpenAIReasoningEffortFromBody(
		[]byte(`{"model":"gpt-5.6-sol-max"}`),
		"gpt-5.6-sol",
		"gpt-5.6-sol-max",
	)
	require.NotNil(t, effort)
	require.Equal(t, "max", *effort)
}

func TestExtractOpenAIReasoningEffortUsesMappedModelForExplicitMax(t *testing.T) {
	effort := extractOpenAIReasoningEffortFromBody(
		[]byte(`{"reasoning":{"effort":"max"}}`),
		"gpt-5.6-terra",
		"alias-without-family",
	)
	require.NotNil(t, effort)
	require.Equal(t, "max", *effort)
}

func TestOpenAICompatAnthropicReasoningEffortUsesFinalModel(t *testing.T) {
	req := &apicompat.AnthropicRequest{OutputConfig: &apicompat.AnthropicOutputConfig{Effort: "max"}}
	require.Equal(t, "max", openAICompatAnthropicReasoningEffort(req, "gpt-5.6-luna", "xhigh"))
	require.Equal(t, "xhigh", openAICompatAnthropicReasoningEffort(req, "gpt-5.5", "xhigh"))
	require.Equal(t, "medium", openAICompatAnthropicReasoningEffort(&apicompat.AnthropicRequest{}, "gpt-5.6-sol", "medium"))
}

func TestForwardAsAnthropicChatFallbackPreservesFinalModelMax(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"luna","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"output_config":{"effort":"max"},"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_effort","object":"chat.completion","model":"gpt-5.6-luna","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		)),
	}}
	account := &Account{
		ID: 502, Name: "gpt56-api-key", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
		},
		Credentials: map[string]any{
			"api_key": "sk-test", "base_url": "https://upstream.example/v1",
			"model_mapping": map[string]any{"luna": "gpt-5.6-luna"},
		},
	}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.6-luna", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String())
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "max", *result.ReasoningEffort)
}
