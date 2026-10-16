//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func headerOverrideTestAccount(platform, accountType string, credentials map[string]any) *Account {
	return &Account{
		Platform:    platform,
		Type:        accountType,
		Credentials: credentials,
	}
}

func TestHeaderOverrideEligibility(t *testing.T) {
	require.True(t, headerOverrideTestAccount(PlatformAnthropic, AccountTypeAPIKey, nil).IsHeaderOverrideEligible())
	require.True(t, headerOverrideTestAccount(PlatformOpenAI, AccountTypeAPIKey, nil).IsHeaderOverrideEligible())
	require.True(t, headerOverrideTestAccount(PlatformGrok, AccountTypeAPIKey, nil).IsHeaderOverrideEligible())
	require.True(t, headerOverrideTestAccount(PlatformGrok, AccountTypeOAuth, nil).IsHeaderOverrideEligible())
	require.False(t, headerOverrideTestAccount(PlatformAnthropic, AccountTypeOAuth, nil).IsHeaderOverrideEligible())
	require.False(t, headerOverrideTestAccount(PlatformGemini, AccountTypeAPIKey, nil).IsHeaderOverrideEligible())

	var nilAccount *Account
	require.False(t, nilAccount.IsHeaderOverrideEligible())
	require.False(t, nilAccount.IsHeaderOverrideEnabled())
	require.Nil(t, nilAccount.GetHeaderOverrides())
}

func TestGetHeaderOverridesNormalizesAndSkipsUnsafeEntries(t *testing.T) {
	acc := headerOverrideTestAccount(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides: map[string]any{
			"User-Agent":    "my-agent/1.0",
			" X-App ":       "cli",
			"x-empty":       "",
			"authorization": "Bearer leaked",
			"bad name":      "value",
			"x-padded":      "  padded  ",
		},
	})

	require.Equal(t, map[string]string{
		"user-agent": "my-agent/1.0",
		"x-app":      "cli",
		"x-padded":   "padded",
	}, acc.GetHeaderOverrides())

	disabled := headerOverrideTestAccount(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
		credKeyHeaderOverrides: map[string]any{"user-agent": "x"},
	})
	require.Nil(t, disabled.GetHeaderOverrides())
}

func TestApplyHeaderOverrides(t *testing.T) {
	acc := headerOverrideTestAccount(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides: map[string]any{
			"user-agent":     "override-agent/2.0",
			"anthropic-beta": "custom-beta-1",
			"x-custom":       "custom-value",
		},
	})

	h := http.Header{}
	h.Set("User-Agent", "claude-cli/2.1.161 (external, cli)")
	h["anthropic-beta"] = []string{"claude-code-20250219,oauth-2025-04-20"}
	h.Set("Content-Type", "application/json")

	acc.ApplyHeaderOverrides(h)

	require.Equal(t, []string{"override-agent/2.0"}, h["User-Agent"])
	require.Equal(t, []string{"custom-beta-1"}, h["anthropic-beta"])
	require.Empty(t, h["Anthropic-Beta"])
	require.Equal(t, []string{"custom-value"}, h["x-custom"])
	require.Equal(t, "application/json", h.Get("Content-Type"))
}

func TestApplyHeaderOverridesNoOpPaths(t *testing.T) {
	oauth := headerOverrideTestAccount(PlatformAnthropic, AccountTypeOAuth, map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides:       map[string]any{"user-agent": "hacked"},
	})
	h := http.Header{}
	h.Set("User-Agent", "orig")
	oauth.ApplyHeaderOverrides(h)
	require.Equal(t, "orig", h.Get("User-Agent"))

	blocked := headerOverrideTestAccount(PlatformOpenAI, AccountTypeAPIKey, map[string]any{
		credKeyHeaderOverrideEnabled: true,
		credKeyHeaderOverrides: map[string]any{
			"Authorization":  "Bearer evil",
			"X-Api-Key":      "evil",
			"Host":           "evil.example.com",
			"Content-Length": "0",
		},
	})
	h = http.Header{}
	h.Set("Authorization", "Bearer real-key")
	blocked.ApplyHeaderOverrides(h)
	require.Equal(t, "Bearer real-key", h.Get("Authorization"))

	blocked.ApplyHeaderOverrides(nil)
}

func TestNormalizeHeaderOverrideCredentials(t *testing.T) {
	t.Run("normalizes names and values", func(t *testing.T) {
		creds := map[string]any{
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides: map[string]any{
				" User-Agent ": " my-agent ",
				"X-App":        "",
				"":             "",
			},
		}
		require.NoError(t, NormalizeHeaderOverrideCredentials(creds))
		require.Equal(t, map[string]any{
			"user-agent": "my-agent",
			"x-app":      "",
		}, creds[credKeyHeaderOverrides])
	})

	t.Run("rejects invalid enabled flag", func(t *testing.T) {
		err := NormalizeHeaderOverrideCredentials(map[string]any{
			credKeyHeaderOverrideEnabled: "yes",
		})
		require.Error(t, err)
	})

	t.Run("rejects invalid names and blocked headers", func(t *testing.T) {
		for _, name := range []string{"bad name", "bad:name", "Authorization", "x-api-key", "Host", "Content-Type", "Cookie", "x-client-request-id", "x-grok-conv-id"} {
			err := NormalizeHeaderOverrideCredentials(map[string]any{
				credKeyHeaderOverrides: map[string]any{name: "v"},
			})
			require.Error(t, err, "name %q should be rejected", name)
		}
	})

	t.Run("rejects duplicate names case-insensitively", func(t *testing.T) {
		err := NormalizeHeaderOverrideCredentials(map[string]any{
			credKeyHeaderOverrides: map[string]any{
				"User-Agent": "a",
				"user-agent": "b",
			},
		})
		require.Error(t, err)
	})

	t.Run("rejects oversized value", func(t *testing.T) {
		err := NormalizeHeaderOverrideCredentials(map[string]any{
			credKeyHeaderOverrides: map[string]any{
				"x-app": strings.Repeat("a", maxHeaderOverrideValueLength+1),
			},
		})
		require.Error(t, err)
	})
}
