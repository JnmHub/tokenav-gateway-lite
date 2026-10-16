//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestGetBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		account  Account
		expected string
	}{
		{
			name: "non-apikey type returns empty",
			account: Account{
				Type:     AccountTypeOAuth,
				Platform: PlatformAnthropic,
			},
			expected: "",
		},
		{
			name: "apikey without base_url returns default anthropic",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformAnthropic,
				Credentials: map[string]any{},
			},
			expected: "https://api.anthropic.com",
		},
		{
			name: "apikey with custom base_url",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformAnthropic,
				Credentials: map[string]any{"base_url": "https://custom.example.com"},
			},
			expected: "https://custom.example.com",
		},
		{
			name: "antigravity apikey auto-appends /antigravity",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com"},
			},
			expected: "https://upstream.example.com/antigravity",
		},
		{
			name: "antigravity apikey trims trailing slash before appending",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com/"},
			},
			expected: "https://upstream.example.com/antigravity",
		},
		{
			name: "antigravity non-apikey returns empty",
			account: Account{
				Type:        AccountTypeOAuth,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com"},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.account.GetBaseURL()
			if result != tt.expected {
				t.Errorf("GetBaseURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestGetGrokBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		account  Account
		expected string
	}{
		{
			name:     "oauth without base url uses cli proxy",
			account:  Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
			expected: xai.DefaultCLIBaseURL,
		},
		{
			name: "oauth stored official api url is preserved",
			account: Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"base_url": xai.DefaultBaseURL + "/",
			}},
			expected: xai.DefaultBaseURL,
		},
		{
			name: "oauth official api url with tls port is preserved",
			account: Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"base_url": "https://api.x.ai:443/v1",
			}},
			expected: "https://api.x.ai:443/v1",
		},
		{
			name: "oauth custom upstream is preserved",
			account: Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"base_url": "https://grok-relay.example.com/v1",
			}},
			expected: "https://grok-relay.example.com/v1",
		},
		{
			name:     "apikey without base url uses official api",
			account:  Account{Platform: PlatformGrok, Type: AccountTypeAPIKey},
			expected: xai.DefaultBaseURL,
		},
		{
			name: "apikey custom upstream is preserved",
			account: Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://grok-key-relay.example.com/v1",
			}},
			expected: "https://grok-key-relay.example.com/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if actual := tt.account.GetGrokBaseURL(); actual != tt.expected {
				t.Fatalf("GetGrokBaseURL() = %q, want %q", actual, tt.expected)
			}
		})
	}
}

func TestGetGrokMediaBaseURL(t *testing.T) {
	tests := []struct {
		name     string
		account  Account
		expected string
	}{
		{
			name:     "oauth without base url uses official media api",
			account:  Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
			expected: xai.DefaultBaseURL,
		},
		{
			name: "oauth stored cli proxy uses official media api",
			account: Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"base_url": xai.DefaultCLIBaseURL,
			}},
			expected: xai.DefaultBaseURL,
		},
		{
			name: "oauth stored official api stays on official media api",
			account: Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"base_url": xai.DefaultBaseURL + "/",
			}},
			expected: xai.DefaultBaseURL,
		},
		{
			name: "oauth custom upstream is preserved",
			account: Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
				"base_url": "https://grok-relay.example.com/v1",
			}},
			expected: "https://grok-relay.example.com/v1",
		},
		{
			name: "api key custom upstream is preserved",
			account: Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"base_url": "https://grok-key-relay.example.com/v1",
			}},
			expected: "https://grok-key-relay.example.com/v1",
		},
		{
			name:     "non grok account has no media url",
			account:  Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if actual := tt.account.GetGrokMediaBaseURL(); actual != tt.expected {
				t.Fatalf("GetGrokMediaBaseURL() = %q, want %q", actual, tt.expected)
			}
		})
	}
}

func TestNormalizeGrokBaseURLCredentials(t *testing.T) {
	for _, baseURL := range []string{xai.DefaultCLIBaseURL, xai.DefaultBaseURL, xai.DefaultUSEast1BaseURL, xai.DefaultUSWest2BaseURL, xai.DefaultEUWest1BaseURL, "https://relay.example.com/tenant/xai/v1/"} {
		credentials := map[string]any{"base_url": baseURL}
		require.NoError(t, NormalizeGrokBaseURLCredentials(PlatformGrok, AccountTypeOAuth, credentials))
		require.Equal(t, strings.TrimRight(baseURL, "/"), credentials["base_url"])
	}

	for _, baseURL := range []string{"https://api.x.ai.evil.example/v1", "https://api.x.ai/v1?token=secret", "https://user:pass@api.x.ai/v1", "http://api.x.ai/v1"} {
		credentials := map[string]any{"base_url": baseURL}
		require.Error(t, NormalizeGrokBaseURLCredentials(PlatformGrok, AccountTypeOAuth, credentials), baseURL)
	}
}

func TestGetGeminiBaseURL(t *testing.T) {
	const defaultGeminiURL = "https://generativelanguage.googleapis.com"

	tests := []struct {
		name     string
		account  Account
		expected string
	}{
		{
			name: "apikey without base_url returns default",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformGemini,
				Credentials: map[string]any{},
			},
			expected: defaultGeminiURL,
		},
		{
			name: "apikey with custom base_url",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformGemini,
				Credentials: map[string]any{"base_url": "https://custom-gemini.example.com"},
			},
			expected: "https://custom-gemini.example.com",
		},
		{
			name: "antigravity apikey auto-appends /antigravity",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com"},
			},
			expected: "https://upstream.example.com/antigravity",
		},
		{
			name: "antigravity apikey trims trailing slash",
			account: Account{
				Type:        AccountTypeAPIKey,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com/"},
			},
			expected: "https://upstream.example.com/antigravity",
		},
		{
			name: "antigravity oauth does NOT append /antigravity",
			account: Account{
				Type:        AccountTypeOAuth,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{"base_url": "https://upstream.example.com"},
			},
			expected: "https://upstream.example.com",
		},
		{
			name: "oauth without base_url returns default",
			account: Account{
				Type:        AccountTypeOAuth,
				Platform:    PlatformAntigravity,
				Credentials: map[string]any{},
			},
			expected: defaultGeminiURL,
		},
		{
			name: "nil credentials returns default",
			account: Account{
				Type:     AccountTypeAPIKey,
				Platform: PlatformGemini,
			},
			expected: defaultGeminiURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.account.GetGeminiBaseURL(defaultGeminiURL)
			if result != tt.expected {
				t.Errorf("GetGeminiBaseURL() = %q, want %q", result, tt.expected)
			}
		})
	}
}
