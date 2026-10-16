package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const v0166ReasoningCanonicalBody = `{"model":"gpt-5.1","stream":false,"input":[` +
	`{"type":"message","role":"user","content":"hello"},` +
	`{"type":"reasoning","id":"rs_provider_abc123","encrypted_content":"ENC_BLOB","summary":[{"type":"summary_text","text":"thinking"}]},` +
	`{"type":"message","role":"assistant","content":"hi"}` +
	`]}`

func newV0166OpenAIPassthroughAccount(id int64, passthrough bool) *service.Account {
	return &service.Account{
		ID:       id,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Extra:    map[string]any{"openai_passthrough": passthrough},
	}
}

func v0166ReasoningItemCount(body []byte) int {
	count := 0
	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "reasoning" {
			count++
		}
		return true
	})
	return count
}

func TestDeriveOpenAIForwardAttemptBody_CrossModeStripsEncryptedReasoning(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	canonical := []byte(v0166ReasoningCanonicalBody)
	state := &openAIPassthroughFailoverState{}

	first := h.deriveOpenAIForwardAttemptBody(nil, canonical, newV0166OpenAIPassthroughAccount(1, true), state)
	require.Equal(t, 1, v0166ReasoningItemCount(first))
	require.JSONEq(t, v0166ReasoningCanonicalBody, string(first))

	second := h.deriveOpenAIForwardAttemptBody(nil, canonical, newV0166OpenAIPassthroughAccount(2, false), state)
	require.Equal(t, 0, v0166ReasoningItemCount(second))
	require.NotContains(t, string(second), "rs_provider_abc123")
	require.NotContains(t, string(second), "ENC_BLOB")
	require.Equal(t, 2, int(gjson.GetBytes(second, "input.#").Int()))
	require.Equal(t, "hello", gjson.GetBytes(second, "input.0.content").String())
	require.Equal(t, "hi", gjson.GetBytes(second, "input.1.content").String())
	require.JSONEq(t, v0166ReasoningCanonicalBody, string(canonical), "派生重试体不得修改原始请求")
}

func TestDeriveOpenAIForwardAttemptBody_SameModePreservesReasoning(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	canonical := []byte(v0166ReasoningCanonicalBody)

	t.Run("non_passthrough", func(t *testing.T) {
		state := &openAIPassthroughFailoverState{}
		_ = h.deriveOpenAIForwardAttemptBody(nil, canonical, newV0166OpenAIPassthroughAccount(10, false), state)
		retry := h.deriveOpenAIForwardAttemptBody(nil, canonical, newV0166OpenAIPassthroughAccount(11, false), state)
		require.Equal(t, 1, v0166ReasoningItemCount(retry))
	})

	t.Run("passthrough", func(t *testing.T) {
		state := &openAIPassthroughFailoverState{}
		_ = h.deriveOpenAIForwardAttemptBody(nil, canonical, newV0166OpenAIPassthroughAccount(20, true), state)
		retry := h.deriveOpenAIForwardAttemptBody(nil, canonical, newV0166OpenAIPassthroughAccount(21, true), state)
		require.Equal(t, 1, v0166ReasoningItemCount(retry))
	})
}

func TestOpenAIWSTurnBillingModel_UsesCurrentTurnMapping(t *testing.T) {
	result := &service.OpenAIForwardResult{BillingModel: "account-model"}

	require.Equal(t, "public-model", openAIWSTurnBillingModel(result, service.ChannelMappingResult{
		BillingModelSource: service.BillingModelSourceRequested,
	}, "public-model", "account-model"))
	require.Equal(t, "channel-model", openAIWSTurnBillingModel(result, service.ChannelMappingResult{
		Mapped:             true,
		MappedModel:        "channel-model",
		BillingModelSource: service.BillingModelSourceChannelMapped,
	}, "public-model", "account-model"))
	require.Equal(t, "account-model", openAIWSTurnBillingModel(result, service.ChannelMappingResult{
		BillingModelSource: service.BillingModelSourceUpstream,
	}, "public-model", "account-model"))
}
