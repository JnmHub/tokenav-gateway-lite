package service

import (
	"net/http"
	"strings"
)

const (
	anthropicAPIKeyAuthSchemeExtraKey = "anthropic_apikey_auth_scheme"

	AnthropicAPIKeyAuthSchemeXAPIKey             = "x_api_key"
	AnthropicAPIKeyAuthSchemeAuthorizationBearer = "authorization_bearer"
)

// GetAnthropicAPIKeyAuthScheme 返回 Anthropic API Key 及国产原生 Messages 账号的上游鉴权方式。
// 缺失或非法配置都保留历史 x-api-key 行为，避免老账号升级后请求方式变化。
func (a *Account) GetAnthropicAPIKeyAuthScheme() string {
	if a == nil || a.Type != AccountTypeAPIKey {
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}
	if a.Platform != PlatformAnthropic && !a.IsCNProvider() {
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}

	switch strings.TrimSpace(a.GetExtraString(anthropicAPIKeyAuthSchemeExtraKey)) {
	case AnthropicAPIKeyAuthSchemeAuthorizationBearer:
		return AnthropicAPIKeyAuthSchemeAuthorizationBearer
	default:
		return AnthropicAPIKeyAuthSchemeXAPIKey
	}
}

func setAnthropicAPIKeyAuthHeader(header http.Header, account *Account, token string) {
	if account.GetAnthropicAPIKeyAuthScheme() == AnthropicAPIKeyAuthSchemeAuthorizationBearer {
		header.Set("Authorization", "Bearer "+token)
		return
	}
	header.Set("x-api-key", token)
}
