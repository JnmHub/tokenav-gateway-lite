package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCNProviderAPIProtocolValidation(t *testing.T) {
	t.Parallel()

	account := func(platform, protocol string) *Account {
		return &Account{
			Platform: platform,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key":      "sk-test",
				"api_protocol": protocol,
			},
		}
	}

	require.Equal(t, APIProtocolAdaptive, account(PlatformKimi, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolAnthropic, account(PlatformZhipu, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolResponses, account(PlatformDeepseek, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolResponses, account(PlatformKimi, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolChatCompletions, account(PlatformZhipu, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolChatCompletions, account(PlatformDeepseek, "invalid").GetAPIProtocol())
	require.Equal(t, APIProtocolChatCompletions, account(PlatformOpenAI, APIProtocolAdaptive).GetAPIProtocol())
}

func TestCNProviderAdaptiveBaseURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		platform      string
		mode          string
		wantChat      string
		wantAnthropic string
		wantResponses string
	}{
		{"Kimi 按量", PlatformKimi, AccountModePayG, DefaultKimiPayGBaseURL, DefaultKimiPayGAnthropicBaseURL, DefaultKimiPayGBaseURL},
		{"Kimi Coding", PlatformKimi, AccountModeCoding, DefaultKimiCodingBaseURL, DefaultKimiCodingAnthropicBaseURL, DefaultKimiCodingBaseURL},
		{"智谱按量", PlatformZhipu, AccountModePayG, DefaultZhipuPayGBaseURL, DefaultZhipuAnthropicBaseURL, DefaultZhipuPayGBaseURL},
		{"智谱 Coding", PlatformZhipu, AccountModeCoding, DefaultZhipuCodingBaseURL, DefaultZhipuAnthropicBaseURL, DefaultZhipuCodingBaseURL},
		{"DeepSeek", PlatformDeepseek, AccountModePayG, DefaultDeepseekBaseURL, DefaultDeepseekAnthropicBaseURL, DefaultDeepseekBaseURL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			account := &Account{
				Platform: test.platform,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_protocol": APIProtocolAdaptive,
					"account_mode": test.mode,
				},
			}
			require.Equal(t, test.wantChat, account.GetCNProtocolBaseURL(APIProtocolChatCompletions))
			require.Equal(t, test.wantAnthropic, account.GetCNProtocolBaseURL(APIProtocolAnthropic))
			require.Equal(t, test.wantResponses, account.GetCNProtocolBaseURL(APIProtocolResponses))
		})
	}
}

func TestCNProviderAdaptiveBaseURLOverrides(t *testing.T) {
	t.Parallel()

	account := &Account{
		Platform: PlatformDeepseek,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAdaptive,
			"base_url":     "https://legacy.example.com",
			"api_base_urls": map[string]any{
				APIProtocolChatCompletions: "https://chat.example.com/",
				APIProtocolAnthropic:       "https://messages.example.com/",
				APIProtocolResponses:       "https://responses.example.com/",
			},
		},
	}

	require.Equal(t, "https://chat.example.com", account.GetOpenAIBaseURL())
	require.Equal(t, "https://messages.example.com", account.GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://responses.example.com", account.GetCNProtocolBaseURL(APIProtocolResponses))
}

func TestDeepSeekResponsesRequestIsStateless(t *testing.T) {
	t.Parallel()

	body := []byte(`{"model":"deepseek-v4-pro","store":true,"previous_response_id":"resp_old","instructions":"codex default","max_output_tokens":32,"input":"hi"}`)
	account := &Account{
		Platform:    PlatformDeepseek,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAdaptive},
	}

	normalized := normalizeDeepSeekResponsesRequestBody(account, body)
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
	require.False(t, gjson.GetBytes(normalized, "previous_response_id").Exists())
	require.Equal(t, "codex default", gjson.GetBytes(normalized, "instructions").String())
	require.Equal(t, int64(32), gjson.GetBytes(normalized, "max_output_tokens").Int())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(normalized, "model").String())
}

func TestDeepSeekResponsesURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://api.deepseek.com/responses", buildOpenAIResponsesURLForPlatform(PlatformDeepseek, "https://api.deepseek.com"))
	require.Equal(t, "https://api.openai.com/v1/responses", buildOpenAIResponsesURLForPlatform(PlatformOpenAI, "https://api.openai.com"))
}

func TestCNAnthropicSSEUsageSupportsCachedTokens(t *testing.T) {
	t.Parallel()

	usage := &ClaudeUsage{}
	parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":12,"cached_tokens":9,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}}}}`, usage)
	require.Equal(t, 12, usage.InputTokens)
	require.Equal(t, 9, usage.CacheReadInputTokens)
	require.Equal(t, 7, usage.CacheCreationInputTokens)
	require.Equal(t, 3, usage.CacheCreation5mTokens)
	require.Equal(t, 4, usage.CacheCreation1hTokens)

	parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"output_tokens":5,"cached_tokens":11}}`, usage)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 9, usage.CacheReadInputTokens, "已记录的缓存读取量不能被后续兼容字段覆盖")
}
