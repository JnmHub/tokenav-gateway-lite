package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicAPIKeyAuthSchemeDefaultsToXAPIKey(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}

	require.Equal(t, AnthropicAPIKeyAuthSchemeXAPIKey, account.GetAnthropicAPIKeyAuthScheme())

	header := http.Header{}
	setAnthropicAPIKeyAuthHeader(header, account, "sk-ant")
	require.Equal(t, "sk-ant", header.Get("x-api-key"))
	require.Empty(t, header.Get("Authorization"))
}

func TestAnthropicAPIKeyAuthSchemeSupportsBearer(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeAuthorizationBearer,
		},
	}

	require.Equal(t, AnthropicAPIKeyAuthSchemeAuthorizationBearer, account.GetAnthropicAPIKeyAuthScheme())

	header := http.Header{}
	setAnthropicAPIKeyAuthHeader(header, account, "sk-ant")
	require.Equal(t, "Bearer sk-ant", header.Get("Authorization"))
	require.Empty(t, header.Get("x-api-key"))
}

func TestAnthropicAPIKeyAuthSchemeSupportsCNProviderBearer(t *testing.T) {
	account := &Account{
		Platform: PlatformZhipu,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeAuthorizationBearer,
		},
	}

	require.Equal(t, AnthropicAPIKeyAuthSchemeAuthorizationBearer, account.GetAnthropicAPIKeyAuthScheme())

	header := http.Header{}
	setAnthropicAPIKeyAuthHeader(header, account, "cn-token")
	require.Equal(t, "Bearer cn-token", header.Get("Authorization"))
	require.Empty(t, header.Get("x-api-key"))
}

func TestAnthropicAPIKeyAuthSchemeRejectsUnrelatedPlatformBearer(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra: map[string]any{
			anthropicAPIKeyAuthSchemeExtraKey: AnthropicAPIKeyAuthSchemeAuthorizationBearer,
		},
	}

	require.Equal(t, AnthropicAPIKeyAuthSchemeXAPIKey, account.GetAnthropicAPIKeyAuthScheme())
}
