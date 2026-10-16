package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

func TestEnforceCodexIdentityHeaders(t *testing.T) {
	tests := []struct {
		name           string
		originator     string
		userAgent      string
		version        string
		wantOriginator string
		wantUserAgent  string
		wantVersion    string
	}{
		{
			name:           "按最终 TUI User-Agent 修复错配身份",
			originator:     "codex_cli_rs",
			userAgent:      "codex-tui/0.145.0 (Mac OS X 14.0; arm64) iTerm (codex-tui; 0.145.0)",
			wantOriginator: "codex-tui",
			wantUserAgent:  "codex-tui/" + codexCLIVersion + " (Mac OS X 14.0; arm64) iTerm (codex-tui; " + codexCLIVersion + ")",
			wantVersion:    codexCLIVersion,
		},
		{
			name:           "第三方 User-Agent 整体回退",
			originator:     "opencode",
			userAgent:      "luna/1.0.0",
			wantOriginator: openai.CodexDefaultOriginator,
			wantUserAgent:  codexCLIUserAgent,
			wantVersion:    codexCLIVersion,
		},
		{
			name:           "低版本请求头提升到内置版本",
			originator:     "codex_cli_rs",
			userAgent:      "codex_cli_rs/0.125.0",
			version:        "0.125.0",
			wantOriginator: "codex_cli_rs",
			wantUserAgent:  "codex_cli_rs/" + codexCLIVersion,
			wantVersion:    codexCLIVersion,
		},
		{
			name:          "无 originator 时保持不变",
			userAgent:     "luna/1.0.0",
			wantUserAgent: "luna/1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			if tt.originator != "" {
				headers.Set("originator", tt.originator)
			}
			if tt.userAgent != "" {
				headers.Set("user-agent", tt.userAgent)
			}
			if tt.version != "" {
				headers.Set("version", tt.version)
			}

			enforceCodexIdentityHeaders(headers)

			if got := headers.Get("originator"); got != tt.wantOriginator {
				t.Fatalf("originator = %q, want %q", got, tt.wantOriginator)
			}
			if got := headers.Get("user-agent"); got != tt.wantUserAgent {
				t.Fatalf("user-agent = %q, want %q", got, tt.wantUserAgent)
			}
			if got := headers.Get("version"); got != tt.wantVersion {
				t.Fatalf("version = %q, want %q", got, tt.wantVersion)
			}
		})
	}
}
