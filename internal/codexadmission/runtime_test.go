package codexadmission

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func codexRequest(path, ua, originator string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if originator != "" {
		req.Header.Set("Originator", originator)
	}
	return req
}

func TestRuntimeAuditOnlyUsesMultipleSignals(t *testing.T) {
	runtime := NewRuntime(DefaultSettings())

	onlyUA := runtime.Evaluate(codexRequest("/v1/chat/completions", "codex_cli_rs/0.144.1", ""))
	require.True(t, onlyUA.Observed)
	require.False(t, onlyUA.Detected)
	require.False(t, onlyUA.ShouldDeny)
	require.Equal(t, "insufficient_signals", onlyUA.Reason)

	detected := runtime.Evaluate(codexRequest("/v1/responses", "codex_cli_rs/0.144.1", "codex_cli_rs"))
	require.True(t, detected.Detected)
	require.Equal(t, ClientCLI, detected.Client)
	require.Equal(t, "0.144.1", detected.Version)
	require.Equal(t, "allowed", detected.Reason)

	snapshot := runtime.Snapshot()
	require.EqualValues(t, 2, snapshot.Inspected)
	require.EqualValues(t, 1, snapshot.Detected)
	require.NotEmpty(t, snapshot.Versions)

	unknown := runtime.Evaluate(codexRequest("/backend-api/codex/responses", "codex_unknown/9.9.9", ""))
	require.True(t, unknown.Detected)
	require.True(t, unknown.Violation)
	require.False(t, unknown.ShouldDeny, "audit-only must not block newly observed signatures")
	require.Equal(t, "client_type_unknown", unknown.Reason)
}

func TestRuntimeEmptySnapshotUsesJSONArrays(t *testing.T) {
	encoded, err := json.Marshal(NewRuntime(DefaultSettings()).Snapshot())
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"versions":[]`)
	require.Contains(t, string(encoded), `"profiles":[]`)
}

func TestNormalizeVersionRejectsOversizedTelemetryValue(t *testing.T) {
	oversized := strings.Repeat("1", maxNormalizedVersionBytes) + ".2.3"
	require.Empty(t, normalizeVersion(oversized))
	require.Equal(t, "0.146.0", normalizeVersion("v0.146.0"))
}

func TestRuntimeDefaultsToAuditOnlyAndEnforcesAfterExplicitSwitch(t *testing.T) {
	settings := DefaultSettings()
	settings.DeniedClients = []string{ClientCLI}
	settings.AllowedClients = []string{ClientDesktop, ClientCLI}
	runtime := NewRuntime(settings)

	audited := runtime.Evaluate(codexRequest("/v1/responses", "codex_cli_rs/0.144.1", "codex_cli_rs"))
	require.True(t, audited.Violation)
	require.False(t, audited.ShouldDeny)
	require.Equal(t, "client_denied", audited.Reason)

	settings.Mode = ModeEnforce
	require.NoError(t, runtime.Update(settings))
	denied := runtime.Evaluate(codexRequest("/v1/responses", "codex_cli_rs/0.144.1", "codex_cli_rs"))
	require.True(t, denied.ShouldDeny)
	require.EqualValues(t, 1, runtime.Snapshot().Denied)
}

func TestRuntimeVersionAndFingerprintRules(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = ModeEnforce
	settings.MinVersion = "0.144.0"
	settings.MaxVersion = "0.150.0"
	runtime := NewRuntime(settings)

	tooOld := runtime.Evaluate(codexRequest("/v1/responses", "codex_cli_rs/0.143.9", "codex_cli_rs"))
	require.True(t, tooOld.ShouldDeny)
	require.Equal(t, "version_too_low", tooOld.Reason)

	conflict := runtime.Evaluate(codexRequest("/v1/responses", "codex_app/0.145.0", "codex_cli_rs"))
	require.True(t, conflict.ShouldDeny)
	require.Equal(t, "fingerprint_conflict", conflict.Reason)

	allowed := runtime.Evaluate(codexRequest("/v1/responses", "codex_app/0.145.0", "codex_app"))
	require.False(t, allowed.ShouldDeny)
	require.Equal(t, ClientDesktop, allowed.Client)
}

func TestRuntimeAppServerHasIndependentGate(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = ModeEnforce
	runtime := NewRuntime(settings)
	headerOnly := codexRequest("/v1/chat/completions", "custom-app-server/1.0", "")
	headerOnly.Header.Set("X-Codex-Engine", "responses")
	insufficient := runtime.Evaluate(headerOnly)
	require.False(t, insufficient.Detected)
	require.False(t, insufficient.ShouldDeny)
	require.Equal(t, "insufficient_signals", insufficient.Reason)

	req := codexRequest("/v1/responses", "custom-app-server/1.0", "")
	req.Header.Set("X-Codex-Engine", "responses")

	disabled := runtime.Evaluate(req)
	require.True(t, disabled.ShouldDeny)
	require.Equal(t, "app_server_disabled", disabled.Reason)

	settings.AppServerEnabled = true
	require.NoError(t, runtime.Update(settings))
	allowed := runtime.Evaluate(req)
	require.False(t, allowed.ShouldDeny)
	require.Equal(t, ClientApp, allowed.Client)

	settings.MinVersion = "9.9.9"
	require.NoError(t, runtime.Update(settings))
	require.False(t, runtime.Evaluate(req).ShouldDeny, "unlisted App Server clients use the independent fingerprint gate")
}

func TestRuntimeClassifiesRealClientBaselinesWithoutRetainingRawHeaders(t *testing.T) {
	settings := DefaultSettings()
	settings.AppServerEnabled = true
	runtime := NewRuntime(settings)

	tests := []struct {
		name       string
		ua         string
		originator string
		client     string
		version    string
		codexHead  bool
	}{
		{
			name:       "生产桌面端",
			ua:         "Codex Desktop/0.146.0-alpha.9.2 (Windows 10.0.26200; x86_64) unknown (Codex Desktop; 26.727.40816)",
			originator: "Codex Desktop",
			client:     ClientDesktop,
			version:    "0.146.0",
			codexHead:  true,
		},
		{
			name:       "生产命令行",
			ua:         "codex-tui/0.146.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.146.0)",
			originator: "codex_cli_rs",
			client:     ClientCLI,
			version:    "0.146.0",
		},
		{
			name:       "生产VSCode",
			ua:         "codex_vscode/0.146.0-alpha.9.2 (Windows 10.0.22631; x86_64) unknown (VS Code; 26.727.40816)",
			originator: "codex_vscode",
			client:     ClientVSCode,
			version:    "0.146.0",
			codexHead:  true,
		},
		{
			name:       "本机真实AppServer",
			ua:         "Codex Desktop/0.144.4 (Windows 10.0.26200; x86_64) unknown (Baseline App Server; 1.2.3)",
			originator: "Codex Desktop",
			client:     ClientApp,
			version:    "0.144.4",
			codexHead:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := codexRequest("/v1/responses", tt.ua, tt.originator)
			if tt.codexHead {
				req.Header.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
			}
			decision := runtime.Evaluate(req)
			require.True(t, decision.Detected)
			require.False(t, decision.ShouldDeny)
			require.Equal(t, tt.client, decision.Client)
			require.Equal(t, tt.version, decision.Version)
		})
	}

	snapshot := runtime.Snapshot()
	require.Len(t, snapshot.Profiles, 4)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	for _, forbidden := range []string{"Baseline App Server", "Windows 10.0", "iTerm.app", "Codex Desktop/"} {
		require.False(t, strings.Contains(string(encoded), forbidden), "raw client header fragment must not enter telemetry")
	}
}

func TestRuntimeRealAppServerRequiresPairedWrapperAndEngineSignals(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = ModeEnforce
	settings.AppServerEnabled = false
	runtime := NewRuntime(settings)
	ua := "Codex Desktop/0.144.4 (Windows 10.0.26200; x86_64) unknown (Baseline App Server; 1.2.3)"

	withoutEngineHeader := runtime.Evaluate(codexRequest("/v1/responses", ua, "Codex Desktop"))
	require.Equal(t, ClientDesktop, withoutEngineHeader.Client)
	require.False(t, withoutEngineHeader.ShouldDeny)

	withoutPairedOriginator := codexRequest("/v1/responses", ua, "")
	withoutPairedOriginator.Header.Set("X-Codex-Turn-Metadata", "present")
	missingPair := runtime.Evaluate(withoutPairedOriginator)
	require.Equal(t, ClientDesktop, missingPair.Client)
	require.False(t, missingPair.ShouldDeny)

	real := codexRequest("/v1/responses", ua, "Codex Desktop")
	real.Header.Set("X-Codex-Turn-Metadata", "present")
	decision := runtime.Evaluate(real)
	require.Equal(t, ClientApp, decision.Client)
	require.Equal(t, "app_server_disabled", decision.Reason)
	require.True(t, decision.ShouldDeny)
}

func TestRuntimeOfficialUAAndResponsesPathEnforceVersion(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = ModeEnforce
	settings.MinVersion = "0.145.0"
	runtime := NewRuntime(settings)

	tooOld := runtime.Evaluate(codexRequest("/v1/responses", "codex_cli_rs/0.144.1", ""))
	require.True(t, tooOld.Detected)
	require.True(t, tooOld.ShouldDeny)
	require.Equal(t, "version_too_low", tooOld.Reason)

	ordinary := runtime.Evaluate(codexRequest("/v1/responses", "curl/8.12.1", ""))
	require.True(t, ordinary.Observed)
	require.False(t, ordinary.Detected)
	require.False(t, ordinary.ShouldDeny)
	require.Equal(t, "insufficient_signals", ordinary.Reason)
}

func TestRuntimeDoesNotTreatClaudeCodeAsAppServer(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = ModeEnforce
	settings.AppServerEnabled = true
	runtime := NewRuntime(settings)

	tests := []struct {
		name       string
		ua         string
		originator string
	}{
		{name: "完整命名客户端", ua: "Claude Code/0.5.0 (Macos 15.5; arm64) iTerm2.app (Claude Code; 1.0.4)", originator: "Claude Code"},
		{name: "只声明 UA", ua: "Claude Code/0.5.0 (Macos 15.5; arm64)"},
		{name: "只声明 originator", ua: "custom-app-server/1.0", originator: "Claude Code"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := codexRequest("/v1/responses", tt.ua, tt.originator)
			req.Header.Set("X-Codex-Engine", "responses")
			decision := runtime.Evaluate(req)
			require.True(t, decision.Observed)
			require.False(t, decision.Detected)
			require.False(t, decision.Violation)
			require.False(t, decision.ShouldDeny)
			require.Empty(t, decision.Client)
			require.Equal(t, "account_policy_client", decision.Reason)
		})
	}
}

func TestRuntimeEnforceDoesNotRejectOrdinaryResponsesClients(t *testing.T) {
	settings := DefaultSettings()
	settings.Mode = ModeEnforce
	settings.MinVersion = "0.145.0"
	settings.AllowedClients = []string{ClientDesktop}
	runtime := NewRuntime(settings)

	for _, ua := range []string{"", "curl/8.12.1", "openai-python/2.3.0", "Mozilla/5.0"} {
		decision := runtime.Evaluate(codexRequest("/v1/responses", ua, ""))
		require.True(t, decision.Observed)
		require.False(t, decision.Detected)
		require.False(t, decision.Violation)
		require.False(t, decision.ShouldDeny)
		require.Equal(t, "insufficient_signals", decision.Reason)
	}
}

func TestRuntimeOrdinaryHotPathAllocatesNothing(t *testing.T) {
	runtime := NewRuntime(DefaultSettings())
	req := codexRequest("/v1/chat/completions", "openai-python/2.3.0", "")
	allocations := testing.AllocsPerRun(1_000, func() {
		_ = runtime.Evaluate(req)
	})
	require.Zero(t, allocations)
}

func TestNormalizeSettingsDenyWinsAndRejectsFreeFormRules(t *testing.T) {
	settings := DefaultSettings()
	settings.AllowedClients = []string{ClientCLI, ClientDesktop}
	settings.DeniedClients = []string{ClientCLI}
	normalized, err := NormalizeSettings(settings)
	require.NoError(t, err)
	require.Equal(t, []string{ClientDesktop}, normalized.AllowedClients)
	require.Equal(t, []string{ClientCLI}, normalized.DeniedClients)

	settings.AllowedClients = []string{"curl-header-rule"}
	_, err = NormalizeSettings(settings)
	require.Error(t, err)
}

func BenchmarkRuntimeEvaluateNonCodex(b *testing.B) {
	runtime := NewRuntime(DefaultSettings())
	req := codexRequest("/v1/chat/completions", "curl/8.12.1", "")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.Evaluate(req)
	}
}

func BenchmarkRuntimeEvaluateDetectedCodex(b *testing.B) {
	runtime := NewRuntime(DefaultSettings())
	req := codexRequest("/v1/responses", "codex_cli_rs/0.144.1", "codex_cli_rs")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.Evaluate(req)
	}
}

func BenchmarkRuntimeEvaluateEmptyHeaders(b *testing.B) {
	runtime := NewRuntime(DefaultSettings())
	req := codexRequest("/v1/chat/completions", "", "")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runtime.Evaluate(req)
	}
}
