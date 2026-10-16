//go:build unit

package service

import (
	"context"
	"encoding/json"
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

func TestV0165ClaudeOpus5PricingAndBedrockCapabilities(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	for _, model := range []string{"claude-opus-5", "us.anthropic.claude-opus-5-v1", "claude-opus-4-8"} {
		pricing, err := billing.GetModelPricing(model)
		require.NoError(t, err)
		require.InDelta(t, 5e-6, pricing.InputPricePerToken, 1e-12)
		require.InDelta(t, 25e-6, pricing.OutputPricePerToken, 1e-12)
	}
	require.True(t, isBedrockClaude45OrNewer("us.anthropic.claude-opus-5-v1"))
	require.True(t, bedrockModelSupportsToolSearch("us.anthropic.claude-opus-5-v1"))
	require.True(t, isBedrockOpus47OrNewer("us.anthropic.claude-opus-5-v1"))
	require.JSONEq(t, `{"thinking":{"type":"adaptive"}}`, string(sanitizeBedrockThinking(
		[]byte(`{"thinking":{"type":"enabled","budget_tokens":10000}}`),
		"us.anthropic.claude-opus-5-v1",
	)))
}

func TestV0165GrokSanitizationAndPoolCooldown(t *testing.T) {
	patched, err := sanitizeGrokResponsesTools([]byte(`{"input":"hello","tool_choice":"auto"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "tool_choice").Exists())

	withTool, err := sanitizeGrokResponsesTools([]byte(`{"tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`))
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(withTool, "tool_choice").Exists())

	poolRepo := &grokQuotaAccountRepo{}
	pool := &Account{ID: 611, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}
	(&OpenAIGatewayService{accountRepo: poolRepo}).handleGrokAccountUpstreamError(context.Background(), pool, http.StatusBadGateway, nil, nil)
	require.Zero(t, poolRepo.tempUnschedCalls)

	regularRepo := &grokQuotaAccountRepo{}
	regular := &Account{ID: 612, Platform: PlatformGrok, Type: AccountTypeAPIKey}
	(&OpenAIGatewayService{accountRepo: regularRepo}).handleGrokAccountUpstreamError(context.Background(), regular, http.StatusBadGateway, nil, nil)
	require.Equal(t, 1, regularRepo.tempUnschedCalls)
}

func TestV0165OpenAIPoolRetryDoesNotCreateTransientBlock(t *testing.T) {
	repo := &errorPolicyRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimitService}
	account := &Account{
		ID: 47, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(524)}},
	}
	for range 2 {
		require.False(t, gateway.handleOpenAIAccountUpstreamError(
			context.Background(), account, 524, http.Header{}, []byte(`{"error":{"message":"upstream timeout"}}`), "gpt-5.4",
		))
	}
	require.False(t, gateway.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.4"))
}

func TestV0165GeminiChatPreservesInlineImageOnly(t *testing.T) {
	geminiResp := map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"parts": []any{
			map[string]any{"text": "rendered:"},
			map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aW1hZ2U="}},
		}},
		"finishReason": "STOP",
	}}}
	raw, err := json.Marshal(geminiResp)
	require.NoError(t, err)
	chat, _, err := geminiResponseToChatCompletions(geminiResp, "gemini-test", raw, nil)
	require.NoError(t, err)
	var content string
	require.NoError(t, json.Unmarshal(chat.Choices[0].Message.Content, &content))
	require.Equal(t, "rendered:![image](data:image/png;base64,aW1hZ2U=)", content)

	claude, _ := convertGeminiToClaudeMessage(geminiResp, "gemini-test", raw, false)
	blocks := claude["content"].([]any)
	require.Len(t, blocks, 1, "Anthropic Messages 路径不能把 data URL 当原生图片块")
}

func TestV0165OpenAIInputSanitizersAreScoped(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"function","name":"keep","namespace":"tool"}],
		"input":[
			{"type":"message","id":"item_bad","namespace":"direct","content":[{"type":"input_text","text":"hello","namespace":"nested"}]},
			{"type":"message","id":"msg_valid","role":"user"},
			{"type":"function_call","id":"item_bad_call","call_id":"call_1","name":"run","arguments":"{}"},
			{"type":"function_call_output","id":"item_output","call_id":"call_1","output":"ok"}
		]
	}`)
	withoutIDs, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(withoutIDs, "input.0.id").Exists())
	require.Equal(t, "msg_valid", gjson.GetBytes(withoutIDs, "input.1.id").String())
	require.False(t, gjson.GetBytes(withoutIDs, "input.2.id").Exists())
	require.Equal(t, "item_output", gjson.GetBytes(withoutIDs, "input.3.id").String())

	stripped, err := stripOpenAIResponsesInputNamespaces(withoutIDs, false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(stripped, "input.0.namespace").Exists())
	require.Equal(t, "nested", gjson.GetBytes(stripped, "input.0.content.0.namespace").String())
	require.Equal(t, "tool", gjson.GetBytes(stripped, "tools.0.namespace").String())
}

func TestV0165PricingSchedulerBlankRemoteURLDoesNotStart(t *testing.T) {
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{RemoteURL: "  \t  "}}, nil)
	defer svc.Stop()
	svc.startUpdateScheduler()
	done := make(chan struct{})
	go func() {
		svc.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("blank remote URL must not start scheduler")
	}
}

func TestV0165SessionIDExtractionAndSafetyBoundary(t *testing.T) {
	c := &gin.Context{Request: httptest.NewRequest(http.MethodPost, "/v1/responses", nil)}
	c.Request.Header.Set("session_id", " session-1 ")
	require.Equal(t, "session-1", ExtractClientSessionID(c))

	c.Request.Header.Del("session_id")
	c.Request.Header.Set("prompt_cache_key", "must-not-enter-usage-audit")
	require.Empty(t, ExtractClientSessionID(c), "prompt_cache_key 只参与上游缓存，不能冒充显式会话标识")

	require.Equal(t, strings.Repeat("会", 255), sanitizeSessionID(strings.Repeat("会", 255)))
	require.Empty(t, sanitizeSessionID(strings.Repeat("a", 256)))
	require.Empty(t, sanitizeSessionID("session\npoison"))
	require.Empty(t, sanitizeSessionID(string([]byte{0xff})))
}
