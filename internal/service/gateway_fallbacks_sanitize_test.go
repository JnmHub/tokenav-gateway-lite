package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

// ============================================================================
// 背景（官方 v0.2.0 移植）
//
// Anthropic 上游对 body.fallbacks / body.fallback_credit_token 字段实施
// Pydantic extra='forbid' 校验：当且仅当 anthropic-beta header 含
// server-side-fallback-2026-07-01（fallback_credit_token 额外接受
// fallback-credit-2026-07-01 / fallback-credit-2026-06-01）时接受。
// 否则报："fallbacks: Extra inputs are not permitted"。
//
// fallbacks 全部来自客户端（Claude Code / SDK / OpenCode 等）透传；本仓
// 不写入。策略是"剥字段，不注入 beta"：fallback 会换模型、改计费，
// 不允许当默认打开。
// ============================================================================

func TestSanitizeAnthropicBodyForBetaTokens_NoFallbackFieldsNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	assert.False(t, changed)
	assert.Equal(t, string(body), string(out))
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","fallbacks":"default","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"claude-code-20250219,oauth-2025-04-20,server-side-fallback-2026-07-01")
	assert.False(t, changed, "客户端 header 已带 server-side-fallback beta → 字段保留（不过度删除）")
	assert.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	assert.Equal(t, "default", gjson.GetBytes(out, "fallbacks").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenBetaMissing(t *testing.T) {
	// 客户端透传的两种形态：字符串 "default" 与模型数组
	for name, fallbacks := range map[string]string{
		"string_default": `"fallbacks":"default"`,
		"model_array":    `"fallbacks":["claude-opus-4-6","claude-sonnet-4-6"]`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"claude-haiku-4-5",` + fallbacks + `,"messages":[]}`)
			// 模拟 OAuth mimic / 默认 API-key beta：只有 oauth/interleaved，无 fallback beta
			out, changed := sanitizeAnthropicBodyForBetaTokens(body,
				"oauth-2025-04-20,interleaved-thinking-2025-05-14")
			assert.True(t, changed)
			assert.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
				"header 不含 server-side-fallback beta 时必须 strip fallbacks，否则上游 400")
			assert.True(t, gjson.GetBytes(out, "messages").Exists(), "strip 不得误伤其他字段")
		})
	}
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbacksStrippedWhenHeaderEmpty(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallbacks":"default","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "")
	assert.True(t, changed)
	assert.False(t, gjson.GetBytes(out, "fallbacks").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_FallbackCreditTokenKeptWithAnyAcceptedBeta(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","fallback_credit_token":"tok_123","messages":[]}`)
	// 三个 beta token 任意一个在 header 中都必须保留字段
	for _, beta := range []string{
		claude.BetaServerSideFallback,
		claude.BetaFallbackCredit,
		claude.BetaFallbackCreditLegacy,
	} {
		out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,"+beta)
		assert.Falsef(t, changed, "header 含 %s 时 fallback_credit_token 必须保留", beta)
		assert.Truef(t, gjson.GetBytes(out, "fallback_credit_token").Exists(),
			"header 含 %s 时 fallback_credit_token 必须保留", beta)
	}
}

// ★ 组合场景：只带 context-management beta → 剥 fallbacks，保留 context_management
// （守住"早退导致 fallbacks 漏洗"与"过度删除 context_management"两个方向的回归）
func TestSanitizeAnthropicBodyForBetaTokens_StripsFallbacksKeepsContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "context-management-2025-06-27")
	assert.True(t, changed)
	assert.False(t, gjson.GetBytes(out, "fallbacks").Exists(),
		"header 只有 context-management beta → fallbacks 必须 strip")
	assert.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"context-management beta 在 header 中 → context_management 不得被误删")
}

func TestSanitizeAnthropicBodyForBetaTokens_KeepsBothWhenBothBetasPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"fallbacks":"default","fallback_credit_token":"tok_123","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"context-management-2025-06-27,server-side-fallback-2026-07-01")
	assert.False(t, changed, "两个 beta 都在 header 中 → 所有字段保留")
	assert.True(t, gjson.GetBytes(out, "context_management").Exists())
	assert.True(t, gjson.GetBytes(out, "fallbacks").Exists())
	assert.True(t, gjson.GetBytes(out, "fallback_credit_token").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_EmptyBodyUnchanged(t *testing.T) {
	out, changed := sanitizeAnthropicBodyForBetaTokens([]byte{}, "server-side-fallback-2026-07-01")
	assert.False(t, changed)
	assert.Empty(t, out)

	out, changed = sanitizeAnthropicBodyForBetaTokens(nil, "server-side-fallback-2026-07-01")
	assert.False(t, changed)
	assert.Empty(t, out)
}

// ============================================================================
// Bedrock 对称 strip
// ============================================================================

func TestSanitizeBedrockFieldsForBetaTokens_StripsFallbacksWithoutBeta(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":100,"fallbacks":"default","fallback_credit_token":"tok_123"}`)
	out := sanitizeBedrockFieldsForBetaTokens(body, []string{"context-1m-2025-08-07"})
	assert.False(t, gjson.GetBytes(out, "fallbacks").Exists())
	assert.False(t, gjson.GetBytes(out, "fallback_credit_token").Exists())
	assert.True(t, gjson.GetBytes(out, "messages").Exists())
}

// sanitizeBedrockCCFields：fallbacks / fallback_credit_token 是 Anthropic 直连
// beta API 专有字段，Bedrock 无对应 beta → 无条件剥除。
func TestSanitizeBedrockCCFields_StripsFallbacksUnconditionally(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-6","context_management":{"edits":[]},"fallbacks":"default","fallback_credit_token":"tok_123","messages":[]}`)
	result := sanitizeBedrockCCFields(body)

	assert.False(t, gjson.GetBytes(result, "fallbacks").Exists())
	assert.False(t, gjson.GetBytes(result, "fallback_credit_token").Exists())
	assert.False(t, gjson.GetBytes(result, "context_management").Exists())
	assert.True(t, gjson.GetBytes(result, "messages").Exists())
}
