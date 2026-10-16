package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

const claudeCodeMetadataUserIDJSON = `{"device_id":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","account_uuid":"","session_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}`

func TestClaudeCodeValidator_ProbeBypass(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.IsMaxTokensOneHaikuRequest, true))

	ok := validator.Validate(req, map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_ProbeBypassRequiresUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.IsMaxTokensOneHaikuRequest, true))

	ok := validator.Validate(req, map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesWithoutProbeStillNeedStrictValidation(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(req, map[string]any{
		"model":      "claude-haiku-4-5",
		"max_tokens": 1,
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_CountTokensPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")

	ok := validator.Validate(req, map[string]any{
		"model": "claude-opus-4-8",
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_CountTokensPathRequiresUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages/count_tokens", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")

	ok := validator.Validate(req, map[string]any{
		"model": "claude-opus-4-8",
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesPathFullValid(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(req, map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.",
			},
		},
		"metadata": map[string]any{
			"user_id": "user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_BillingBlockRecognizedWithoutIdentityPrompt(t *testing.T) {
	// 真实抓取的完整安全监视器 system prompt（不含身份 prose）。
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validator := NewClaudeCodeValidator()

	// 前提：完整监视器正文经 Dice 相似度远低于阈值，无法被身份 prose 机制识别——
	// 故下面 Validate 的放行只可能来自计费归因块识别。
	require.Less(t, validator.bestSimilarityScore(string(monitorPrompt)), systemPromptThreshold)

	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.162 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// Claude Code 安全监视器子请求：不携带身份 prose，但 system 数组携带计费归因块
	// cc_entrypoint=cli，应据此识别为 Claude Code 客户端。
	ok := validator.Validate(req, map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=cli; cch=d8726;",
			},
			map[string]any{
				"type": "text",
				"text": string(monitorPrompt),
			},
		},
		"metadata": map[string]any{
			"user_id": claudeCodeMetadataUserIDJSON,
		},
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_BillingBlockOfficialEntrypointRecognized(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.162 (external, cli)")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// 官方入口不只有 cli；VS Code、JetBrains 和 SDK 的入口值也应被识别。
	ok := validator.Validate(req, map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=sdk; cch=d8726;",
			},
			map[string]any{
				"type": "text",
				"text": "Some unrelated system prompt that does not resemble Claude Code.",
			},
		},
		"metadata": map[string]any{
			"user_id": claudeCodeMetadataUserIDJSON,
		},
	})
	require.True(t, ok)
}

func TestClaudeCodeValidator_SecurityMonitorWithoutBillingBlock(t *testing.T) {
	monitorPrompt, err := os.ReadFile("testdata/security_monitor_system_prompt.txt")
	require.NoError(t, err)

	validBody := func(prompt string) map[string]any {
		return map[string]any{
			"model": "claude-haiku-4-5-20251001",
			"system": []any{
				map[string]any{"type": "text", "text": prompt},
			},
			"metadata": map[string]any{"user_id": claudeCodeMetadataUserIDJSON},
		}
	}
	validate := func(body map[string]any) bool {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
		req.Header.Set("User-Agent", "claude-cli/2.1.220 (external, cli)")
		req.Header.Set("X-App", "cli")
		req.Header.Set("anthropic-beta", "claude-code-20250219")
		req.Header.Set("anthropic-version", "2023-06-01")
		return NewClaudeCodeValidator().Validate(req, body)
	}

	t.Run("official classifier", func(t *testing.T) {
		require.True(t, validate(validBody(string(monitorPrompt))))
	})

	t.Run("classifier output with category element", func(t *testing.T) {
		withCategory := strings.Replace(
			string(monitorPrompt),
			"<block>yes</block><reason>",
			"<block>yes</block><category>Exact BLOCK Rule Name</category><reason>",
			1,
		)
		require.True(t, validate(validBody(withCategory)))
	})

	t.Run("opening sentence and arbitrary padding are insufficient", func(t *testing.T) {
		prompt := claudeCodeSecurityMonitorPromptPrefix + "\n" + strings.Repeat("arbitrary content ", 700)
		require.False(t, validate(validBody(prompt)))
	})

	t.Run("classifier remains valid with additional system context", func(t *testing.T) {
		body := validBody(string(monitorPrompt))
		body["system"] = append(body["system"].([]any), map[string]any{
			"type": "text",
			"text": "unrelated extra block",
		})
		require.True(t, validate(body))
	})
}

func TestClaudeCodeValidator_BillingBlockStillRequiresClaudeCodeUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req.Header.Set("X-App", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	// 计费块无法绕过 UA 校验：非 claude-cli 客户端在 Step 1 即被拒。
	ok := validator.Validate(req, map[string]any{
		"model": "claude-3-5-haiku-20241022",
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "x-anthropic-billing-header: cc_version=2.1.162.884; cc_entrypoint=cli; cch=d8726;",
			},
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesPathRejectsNonClaudeCodeUA(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "curl/8.0.0")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(req, map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are Claude Code, Anthropic's official CLI for Claude.",
			},
		},
		"metadata": map[string]any{
			"user_id": "user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_MessagesPathWithoutSystemPromptStillRejected(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.156 (Claude Code)")
	req.Header.Set("X-App", "claude-code")
	req.Header.Set("anthropic-beta", "claude-code-20250219")
	req.Header.Set("anthropic-version", "2023-06-01")

	ok := validator.Validate(req, map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"metadata": map[string]any{
			"user_id": "user_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa_account__session_aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	})
	require.False(t, ok)
}

func TestClaudeCodeValidator_NonMessagesPathUAOnly(t *testing.T) {
	validator := NewClaudeCodeValidator()
	req := httptest.NewRequest(http.MethodPost, "http://example.com/v1/models", nil)
	req.Header.Set("User-Agent", "claude-cli/1.2.3 (darwin; arm64)")

	ok := validator.Validate(req, nil)
	require.True(t, ok)
}

func TestExtractVersion(t *testing.T) {
	v := NewClaudeCodeValidator()
	tests := []struct {
		ua   string
		want string
	}{
		{"claude-cli/2.1.22 (darwin; arm64)", "2.1.22"},
		{"claude-cli/1.0.0", "1.0.0"},
		{"Claude-CLI/3.10.5 (linux; x86_64)", "3.10.5"}, // 大小写不敏感
		{"curl/8.0.0", ""},                              // 非 Claude CLI
		{"", ""},                                        // 空字符串
		{"claude-cli/", ""},                             // 无版本号
		{"claude-cli/2.1.22-beta", "2.1.22"},            // 带后缀仍提取主版本号
	}
	for _, tt := range tests {
		got := v.ExtractVersion(tt.ua)
		require.Equal(t, tt.want, got, "ExtractVersion(%q)", tt.ua)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.1.0", "2.1.0", 0},   // 相等
		{"2.1.1", "2.1.0", 1},   // patch 更大
		{"2.0.0", "2.1.0", -1},  // minor 更小
		{"3.0.0", "2.99.99", 1}, // major 更大
		{"1.0.0", "2.0.0", -1},  // major 更小
		{"0.0.1", "0.0.0", 1},   // patch 差异
		{"", "1.0.0", -1},       // 空字符串 vs 正常版本
		{"v2.1.0", "2.1.0", 0},  // v 前缀处理
	}
	for _, tt := range tests {
		got := CompareVersions(tt.a, tt.b)
		require.Equal(t, tt.want, got, "CompareVersions(%q, %q)", tt.a, tt.b)
	}
}

func TestSetGetClaudeCodeVersion(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, "", GetClaudeCodeVersion(ctx), "empty context should return empty string")

	ctx = SetClaudeCodeVersion(ctx, "2.1.63")
	require.Equal(t, "2.1.63", GetClaudeCodeVersion(ctx))
}
