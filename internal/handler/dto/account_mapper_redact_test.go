package dto

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAccountFromServiceShallow_RedactsSensitiveCredentials(t *testing.T) {
	src := &service.Account{
		ID:       42,
		Name:     "demo",
		Platform: "anthropic",
		Type:     "oauth",
		Credentials: map[string]any{
			"access_token":  "at-secret",
			"refresh_token": "rt-secret",
			"id_token":      "id-secret",
			"api_key":       "sk-secret",
			"header_overrides": map[string]any{
				"api-key": "nested-header-secret",
			},
			"base_url":      "https://api.example.com",
			"model_mapping": map[string]any{"foo": "bar"},
		},
	}

	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)

	// 敏感键不在 Credentials 里
	require.NotContains(t, got.Credentials, "access_token")
	require.NotContains(t, got.Credentials, "refresh_token")
	require.NotContains(t, got.Credentials, "id_token")
	require.NotContains(t, got.Credentials, "api_key")
	// 非敏感键保留
	require.Equal(t, "https://api.example.com", got.Credentials["base_url"])
	require.Equal(t, map[string]any{"foo": "bar"}, got.Credentials["model_mapping"])

	// 状态 map 标记敏感键存在
	require.True(t, got.CredentialsStatus["has_access_token"])
	require.True(t, got.CredentialsStatus["has_refresh_token"])
	require.True(t, got.CredentialsStatus["has_id_token"])
	require.True(t, got.CredentialsStatus["has_api_key"])
	require.True(t, got.CredentialsStatus["has_header_overrides"])

	// JSON 序列化校验：响应体里不会出现敏感子串
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "rt-secret")
	require.NotContains(t, string(raw), "at-secret")
	require.NotContains(t, string(raw), "sk-secret")
	require.NotContains(t, string(raw), "id-secret")
	require.NotContains(t, string(raw), "nested-header-secret")
	require.Contains(t, string(raw), `"api-key":""`)
	// 状态标识应序列化进 JSON
	require.Contains(t, string(raw), "credentials_status")
	require.Contains(t, string(raw), "has_refresh_token")

	// 原始 service.Account 不应被改动
	require.Equal(t, "rt-secret", src.Credentials["refresh_token"])
}

func TestAccountFromServiceShallow_NilCredentialsOmitsStatus(t *testing.T) {
	src := &service.Account{ID: 1, Name: "n", Platform: "anthropic", Type: "oauth"}
	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)
	require.Nil(t, got.Credentials)
	require.Nil(t, got.CredentialsStatus)
}

func TestAccountFromServiceShallow_ExposesSeparateGrokMediaEligibilityView(t *testing.T) {
	src := &service.Account{
		ID:       2,
		Name:     "grok",
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
		Extra:    map[string]any{service.GrokMediaEligibleExtraKey: false},
	}

	got := AccountFromServiceShallow(src)
	require.NotNil(t, got.GrokMediaEligibility)
	require.Equal(t, "ineligible", got.GrokMediaEligibility.State)
	require.Equal(t, "override", got.GrokMediaEligibility.Source)
	require.Equal(t, "override_disabled", got.GrokMediaEligibility.Reason)
	require.NotNil(t, got.GrokMediaEligibility.Override)
	require.False(t, *got.GrokMediaEligibility.Override)
	require.False(t, got.GrokMediaEligibility.EffectiveEligible)
}

func TestAccountFromServiceShallow_PreservesCanonicalGrokUsageSnapshot(t *testing.T) {
	snapshot := map[string]any{
		"subscription_tier": "supergrok_heavy",
		"status_code":       200,
	}
	src := &service.Account{
		ID:       3,
		Name:     "grok-live-tier",
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":      "must-not-leak",
			"subscription_tier": "free",
		},
		Extra: map[string]any{"grok_usage_snapshot": snapshot},
	}

	got := AccountFromServiceShallow(src)
	require.Equal(t, snapshot, got.Extra["grok_usage_snapshot"])
	require.Equal(t, "free", got.Credentials["subscription_tier"])
	require.NotContains(t, got.Credentials, "access_token")

	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"subscription_tier":"supergrok_heavy"`)
	require.NotContains(t, string(raw), "must-not-leak")
}

func TestAccountFromServiceShallow_ExposesCodexQuotaHealthWithoutChangingExtra(t *testing.T) {
	now := time.Now().UTC()
	used := 96.0
	reset := now.Add(time.Hour).Format(time.RFC3339)
	src := &service.Account{
		ID:       4,
		Name:     "codex",
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Format(time.RFC3339),
			"codex_5h_used_percent":  used,
			"codex_5h_reset_at":      reset,
		},
	}

	// The mapper uses the current clock, so use the domain helper directly for
	// deterministic classification and only verify the public shape here.
	health := src.CodexQuotaHealth(now)
	require.NotNil(t, health)
	require.Equal(t, "near_limit", string(health.Status))

	got := AccountFromServiceShallow(src)
	require.NotNil(t, got)
	require.NotNil(t, got.CodexQuotaHealth)
	require.Equal(t, "near_limit", got.CodexQuotaHealth.Status)
	require.Equal(t, used, *got.CodexQuotaHealth.FiveHour.UsedPercent)
	require.Equal(t, used, src.Extra["codex_5h_used_percent"])
}
