package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSeedanceEndpointSemantics 钉住 Seedance（Ark 原生任务）的端点语义，对齐官方 v0.2.13：
// 创建是一次新的付费提交（消费 RPM/TPM + 内容审计），查询/删除是既有任务轮询
// （不要求正文、不消费限额）。此前本仓两条谓词都漏了 Seedance。
func TestSeedanceEndpointSemantics(t *testing.T) {
	require.True(t, SeedanceEndpointCreate.IsGenerationRequest(), "创建要按提交计一次 RPM/TPM")
	require.False(t, SeedanceEndpointCreate.IsVideoLookupRequest())
	require.True(t, SeedanceEndpointCreate.RequiresRequestBody())
	require.False(t, SeedanceEndpointCreate.IsVideoCreationRequest(), "Grok 每秒计价口径不得覆盖 Seedance")

	for _, endpoint := range []GrokMediaEndpoint{SeedanceEndpointStatus, SeedanceEndpointDelete} {
		require.False(t, endpoint.IsGenerationRequest(), "查询/删除不得消费 RPM/TPM")
		require.True(t, endpoint.IsVideoLookupRequest(), "查询/删除属于既有任务访问，必须校验归属")
		require.False(t, endpoint.RequiresRequestBody(), "GET/DELETE 不能要求正文")
	}
}

func TestSeedanceAccountCapabilityRequiresExplicitCustomAPIKeyUpstream(t *testing.T) {
	for _, tc := range []struct {
		name         string
		platform     string
		accountType  string
		baseURL      string
		capabilities any
		allowed      bool
	}{
		{name: "explicit_seedance", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3", capabilities: []string{"seedance"}, allowed: true},
		{name: "json_capability_list", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3", capabilities: []any{"seedance"}, allowed: true},
		{name: "default_chat_only", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3"},
		{name: "explicit_chat_only", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3", capabilities: []string{"chat_completions"}},
		{name: "empty_capabilities", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3", capabilities: []string{}},
		{name: "oauth_cannot_create_ark_task", platform: PlatformOpenAI, accountType: AccountTypeOAuth, baseURL: "https://ark.example.test/api/v3", capabilities: []string{"seedance"}},
		{name: "official_text_default", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, capabilities: []string{"seedance"}},
		{name: "blank_custom_base", platform: PlatformOpenAI, accountType: AccountTypeAPIKey, baseURL: "  ", capabilities: []string{"seedance"}},
		{name: "grok_not_ark", platform: PlatformGrok, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3", capabilities: []string{"seedance"}},
		{name: "anthropic_not_ark", platform: PlatformAnthropic, accountType: AccountTypeAPIKey, baseURL: "https://ark.example.test/api/v3", capabilities: []string{"seedance"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: tc.platform, Type: tc.accountType, Credentials: map[string]any{"base_url": tc.baseURL}}
			if tc.capabilities != nil {
				account.Credentials[openAIEndpointCapabilitiesCredentialKey] = tc.capabilities
			}
			require.Equal(t, tc.allowed, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
		})
	}
	var account *Account
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilitySeedance))
}

// TestGrokVideoEndpointSemanticsUnchanged 保证上面改动没有影响 Grok 视频侧判定。
func TestGrokVideoEndpointSemanticsUnchanged(t *testing.T) {
	require.True(t, GrokMediaEndpointVideosGenerations.IsGenerationRequest())
	require.True(t, GrokMediaEndpointVideosGenerations.IsVideoCreationRequest())
	require.True(t, GrokMediaEndpointVideosGenerations.RequiresRequestBody())
	require.True(t, GrokMediaEndpointVideosEdits.IsGenerationRequest())
	require.True(t, GrokMediaEndpointVideosExtensions.IsVideoCreationRequest())

	require.False(t, GrokMediaEndpointVideoStatus.IsGenerationRequest())
	require.True(t, GrokMediaEndpointVideoStatus.IsVideoLookupRequest())
	require.False(t, GrokMediaEndpointVideoStatus.RequiresRequestBody())
	require.True(t, GrokMediaEndpointVideoContent.IsVideoLookupRequest())

	require.True(t, GrokMediaEndpointImagesGenerations.IsGenerationRequest())
	require.False(t, GrokMediaEndpointImagesGenerations.IsVideoLookupRequest())
}
