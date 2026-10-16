//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestCNExtraKey 验证 provider 维度的 Extra 快照键由前缀 + 后缀拼接。
func TestCNExtraKey(t *testing.T) {
	t.Parallel()
	require.Equal(t, "kimi_5h_used_percent", cnExtraKey(PlatformKimi, cnExtraSuffix5hUsed))
	require.Equal(t, "zhipu_weekly_reset_at", cnExtraKey(PlatformZhipu, cnExtraSuffixWeeklyReset))
	require.Equal(t, "deepseek_balance", cnExtraKey(PlatformDeepseek, cnBalanceExtraSuffixBalance))
}

// TestCNParseF64 兼容 JSON 数值与字符串（cc-switch 与上游字段类型不一致）。
func TestCNParseF64(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  any
		want float64
		ok   bool
	}{
		{"float64", float64(12.5), 12.5, true},
		{"int", 100, 100, true},
		{"numeric string", "33.3", 33.3, true},
		{"trim string", "  7  ", 7, true},
		{"non-numeric string", "abc", 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := cnParseF64(tc.raw)
			require.Equal(t, tc.ok, ok)
			if ok {
				require.InDelta(t, tc.want, got, 1e-9)
			}
		})
	}
}

// TestCNMillisToRFC3339 秒级（<1e12）按秒、毫秒级按毫秒处理；非正返回空串。
func TestCNMillisToRFC3339(t *testing.T) {
	t.Parallel()
	// 1700000000 秒 = 1700000000000 毫秒
	want := time.UnixMilli(1700000000000).UTC().Format(time.RFC3339)
	require.Equal(t, want, cnMillisToRFC3339(1700000000))    // 秒级
	require.Equal(t, want, cnMillisToRFC3339(1700000000000)) // 毫秒级
	require.Equal(t, "", cnMillisToRFC3339(0))               // 非正
	require.Equal(t, "", cnMillisToRFC3339(-1))
}

// TestCnnormalizeResetTime 覆盖 ISO8601 字符串 / 数字（秒、毫秒）/ 非法输入。
func TestCnnormalizeResetTime(t *testing.T) {
	t.Parallel()
	// ISO8601 字符串归一化为 RFC3339（UTC）。
	require.Equal(t, "2026-08-14T10:00:00Z", cnNormalizeResetTime("2026-08-14T10:00:00Z"))
	// 毫秒级 float64。
	require.Equal(t,
		time.UnixMilli(1700000000000).UTC().Format(time.RFC3339),
		cnNormalizeResetTime(float64(1700000000000)))
	// 非法字符串。
	require.Equal(t, "", cnNormalizeResetTime("not-a-time"))
	require.Equal(t, "", cnNormalizeResetTime(""))
}

// TestParseKimiUsageTiers 验证 Kimi For Coding /usages 解析：
//   - 首个 limits[].detail → 5h 桶，utilization=(limit-remaining)/limit*100
//   - usage → weekly 桶
//   - 仅取首个 detail（多个 detail 时不应重复产出 5h）
func TestParseKimiUsageTiers(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"limits": [
			{"name": "5h", "detail": {"limit": 1000, "remaining": 600, "resetTime": "2026-08-14T15:00:00Z"}},
			{"name": "ignored-second-detail", "detail": {"limit": 999, "remaining": 0, "resetTime": "2026-08-14T20:00:00Z"}}
		],
		"usage": {"limit": 10000, "remaining": 4000, "resetTime": "2026-08-18T00:00:00Z"}
	}`)
	tiers := parseKimiUsageTiers(body)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	require.InDelta(t, 40.0, tiers[0].UsedPercent, 1e-9) // (1000-600)/1000*100
	require.Equal(t, "2026-08-14T15:00:00Z", tiers[0].ResetAt)
	require.Equal(t, "weekly", tiers[1].Window)
	require.InDelta(t, 60.0, tiers[1].UsedPercent, 1e-9) // (10000-4000)/10000*100
	require.Equal(t, "2026-08-18T00:00:00Z", tiers[1].ResetAt)
}

// TestParseKimiUsageTiers_LimitZero 不应除零：limit=0 → utilization=0。
func TestParseKimiUsageTiers_LimitZero(t *testing.T) {
	t.Parallel()
	body := []byte(`{"limits":[{"detail":{"limit":0,"remaining":0,"resetTime":"2026-08-14T15:00:00Z"}}]}`)
	tiers := parseKimiUsageTiers(body)
	require.Len(t, tiers, 1)
	require.InDelta(t, 0.0, tiers[0].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_UnitClassification 显式 unit（3=5h / 6=weekly）优先分类，
// 不能被 reset 时间排序覆盖（周期末尾周窗口会更早重置）。
func TestParseZhipuTokenTiers_UnitClassification(t *testing.T) {
	t.Parallel()
	// weekly 的 nextResetTime 早于 5h（模拟周期末尾），但 unit 必须胜出。
	data := gjson.Parse(`{
		"limits": [
			{"type":"TOKENS_LIMIT","unit":6,"percentage":70,"nextResetTime":1700000000000},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":20,"nextResetTime":1700000099999}
		]
	}`)
	tiers := parseZhipuTokenTiers(data)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	require.InDelta(t, 20.0, tiers[0].UsedPercent, 1e-9)
	require.Equal(t, "weekly", tiers[1].Window)
	require.InDelta(t, 70.0, tiers[1].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_SingleTierOldPlan 老套餐仅回 1 条 → 降级为仅 5h。
func TestParseZhipuTokenTiers_SingleTierOldPlan(t *testing.T) {
	t.Parallel()
	data := gjson.Parse(`{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":15,"nextResetTime":1700000000000}]}`)
	tiers := parseZhipuTokenTiers(data)
	require.Len(t, tiers, 1)
	require.Equal(t, "5h", tiers[0].Window)
}

// TestParseZhipuTokenTiers_FallbackHeuristic unit 缺失时：无 reset 的条目优先归 5h，
// 其余按 reset 升序填入剩余槽位。
func TestParseZhipuTokenTiers_FallbackHeuristic(t *testing.T) {
	t.Parallel()
	// 无 unit：A 无 reset、B 有 reset。A 先填 5h，B 填 weekly。
	data := gjson.Parse(`{
		"limits": [
			{"type":"TOKENS_LIMIT","percentage":50,"nextResetTime":1700000000000},
			{"type":"TOKENS_LIMIT","percentage":10}
		]
	}`)
	tiers := parseZhipuTokenTiers(data)
	require.Len(t, tiers, 2)
	require.Equal(t, "5h", tiers[0].Window)
	require.InDelta(t, 10.0, tiers[0].UsedPercent, 1e-9) // 无 reset 优先 5h
	require.Equal(t, "weekly", tiers[1].Window)
	require.InDelta(t, 50.0, tiers[1].UsedPercent, 1e-9)
}

// TestParseZhipuTokenTiers_IgnoresNonTokenEntries 非 TOKENS_LIMIT/CREDIT_LIMIT 条目跳过。
func TestParseZhipuTokenTiers_IgnoresNonTokenEntries(t *testing.T) {
	t.Parallel()
	data := gjson.Parse(`{"limits":[{"type":"OTHER_LIMIT","unit":3,"percentage":99}]}`)
	require.Empty(t, parseZhipuTokenTiers(data))
}

// TestCNQuotaExtraUpdates 验证 tier 列表落 Extra 快照键的 provider 前缀与窗口映射。
func TestCNQuotaExtraUpdates(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	tiers := []CNQuotaTier{
		{Window: "5h", UsedPercent: 40, ResetAt: "2026-08-14T15:00:00Z"},
		{Window: "weekly", UsedPercent: 60, ResetAt: "2026-08-18T00:00:00Z"},
	}
	updates := cnQuotaExtraUpdates(PlatformKimi, tiers, now)
	require.Equal(t, 40.0, updates["kimi_5h_used_percent"])
	require.Equal(t, "2026-08-14T15:00:00Z", updates["kimi_5h_reset_at"])
	require.Equal(t, 60.0, updates["kimi_weekly_used_percent"])
	require.Equal(t, "2026-08-18T00:00:00Z", updates["kimi_weekly_reset_at"])
	require.Equal(t, now.Format(time.RFC3339), updates["kimi_usage_updated_at"])
}

// TestZhipuQuotaHost 按域名路由智谱额度端点主机（bigmodel.cn / z.ai / 默认国内站）。
func TestZhipuQuotaHost(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://open.bigmodel.cn", zhipuQuotaHost("https://open.bigmodel.cn/api/paas/v4"))
	require.Equal(t, "https://api.z.ai", zhipuQuotaHost("https://api.z.ai/api/paas/v4"))
	require.Equal(t, "https://open.bigmodel.cn", zhipuQuotaHost("https://custom.example.com")) // 默认国内站
	require.Equal(t, "https://open.bigmodel.cn/api/monitor/usage/quota/limit", zhipuQuotaURL("https://open.bigmodel.cn/api/paas/v4"))
}

// TestKimiQuotaURL 两种协议默认 base（coding/v1 与 coding）都归一到 /coding/v1/usages
// （cc-switch 固定端点；无 /v1 的 /coding/usages 实测 404）。
func TestKimiQuotaURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://api.kimi.com/coding/v1/usages", kimiQuotaURL("https://api.kimi.com/coding/v1"))
	require.Equal(t, "https://api.kimi.com/coding/v1/usages", kimiQuotaURL("https://api.kimi.com/coding"))
	require.Equal(t, "https://api.kimi.com/coding/v1/usages", kimiQuotaURL("https://api.kimi.com/coding/"))
	require.Equal(t, "https://api.kimi.com/coding/v1/usages", kimiQuotaURL("https://api.kimi.com/coding/v1/"))
}

// TestCNBalanceURL Kimi 固定端点；DeepSeek 基于 base_url 拼接。
func TestCNBalanceURL(t *testing.T) {
	t.Parallel()
	kimi := &Account{Platform: PlatformKimi}
	require.Equal(t, "https://api.moonshot.cn/v1/users/me/balance", cnBalanceURL(kimi))

	deepseek := &Account{
		Platform:    PlatformDeepseek,
		Credentials: map[string]any{"base_url": "https://api.deepseek.com"},
	}
	require.Equal(t, "https://api.deepseek.com/user/balance", cnBalanceURL(deepseek))
}

// TestGetOpenAIProtocolAPIKey_CNProviders 验证 OpenAI 协议族密钥读取覆盖国产供应商，
// 同时保持 IsOpenAIApiKey 的 openai-only 语义（调度倍率/WS 门控不受影响）。
func TestGetOpenAIProtocolAPIKey_CNProviders(t *testing.T) {
	t.Parallel()

	kimi := &Account{
		Platform:    PlatformKimi,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-kimi"},
	}
	require.Equal(t, "sk-kimi", kimi.GetOpenAIProtocolAPIKey())
	require.False(t, kimi.IsOpenAIApiKey(), "IsOpenAIApiKey stays openai-only for scheduling gates")

	// 非 APIKey 类型的 CN 账号不返回密钥
	notAPIKey := &Account{
		Platform:    PlatformDeepseek,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"api_key": "sk-leak"},
	}
	require.Equal(t, "", notAPIKey.GetOpenAIProtocolAPIKey())

	// openai 原生账号行为不变
	openai := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-openai"},
	}
	require.Equal(t, "sk-openai", openai.GetOpenAIProtocolAPIKey())
}

// TestBuildUpstreamModelsRequest_CNProviders 验证模型同步继续使用国产账号的
// 协议密钥和 OpenAI 格式地址，修复 unit（单元测试）门禁时不能误删有效覆盖。
func TestBuildUpstreamModelsRequest_CNProviders(t *testing.T) {
	t.Parallel()

	service := &AccountTestService{cfg: &config.Config{}}
	tests := []struct {
		name     string
		platform string
		mode     string
		wantURL  string
	}{
		{"Kimi 默认", PlatformKimi, "", "https://api.moonshot.cn/v1/models"},
		{"Kimi Coding", PlatformKimi, AccountModeCoding, "https://api.kimi.com/coding/v1/models"},
		{"智谱默认", PlatformZhipu, "", "https://open.bigmodel.cn/api/paas/v4/models"},
		{"智谱 Coding", PlatformZhipu, AccountModeCoding, "https://open.bigmodel.cn/api/coding/paas/v4/models"},
		{"DeepSeek", PlatformDeepseek, "", "https://api.deepseek.com/v1/models"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials := map[string]any{"api_key": "sk-test"}
			if test.mode != "" {
				credentials["account_mode"] = test.mode
			}
			account := &Account{ID: 1, Platform: test.platform, Type: AccountTypeAPIKey, Credentials: credentials}
			request, err := service.buildUpstreamModelsRequest(context.Background(), account)
			require.NoError(t, err)
			require.Equal(t, test.wantURL, request.URL.String())
			require.Equal(t, "Bearer sk-test", request.Header.Get("Authorization"))
		})
	}
}

// TestGetAPIProtocol 验证协议凭证维度的平台校验矩阵：
// responses 仅 deepseek / kimi；缺失/非法值回退 chat_completions（与旧行为一致）。
func TestGetAPIProtocol(t *testing.T) {
	t.Parallel()

	mk := func(platform, protocol string) *Account {
		creds := map[string]any{"api_key": "sk-test"}
		if protocol != "" {
			creds["api_protocol"] = protocol
		}
		return &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: creds}
	}

	require.Equal(t, APIProtocolChatCompletions, mk(PlatformKimi, "").GetAPIProtocol(), "缺失回退默认")
	require.Equal(t, APIProtocolAnthropic, mk(PlatformZhipu, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolAnthropic, mk(PlatformKimi, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolAnthropic, mk(PlatformDeepseek, APIProtocolAnthropic).GetAPIProtocol())
	require.Equal(t, APIProtocolResponses, mk(PlatformDeepseek, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolResponses, mk(PlatformKimi, APIProtocolResponses).GetAPIProtocol())
	require.Equal(t, APIProtocolAdaptive, mk(PlatformKimi, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolAdaptive, mk(PlatformZhipu, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolAdaptive, mk(PlatformDeepseek, APIProtocolAdaptive).GetAPIProtocol())
	require.Equal(t, APIProtocolChatCompletions, mk(PlatformZhipu, APIProtocolResponses).GetAPIProtocol(), "zhipu 无 responses 端点")
	require.Equal(t, APIProtocolChatCompletions, mk(PlatformKimi, "bogus").GetAPIProtocol(), "非法值回退默认")
	require.Equal(t, APIProtocolChatCompletions, (&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}).GetAPIProtocol(), "非 CN 供应商恒为默认")
}

func TestSupportsNativeCNResponses(t *testing.T) {
	t.Parallel()
	require.True(t, (&Account{Platform: PlatformDeepseek}).SupportsNativeCNResponses())
	require.True(t, (&Account{Platform: PlatformKimi}).SupportsNativeCNResponses())
	require.True(t, (&Account{Platform: PlatformKimi, Credentials: map[string]any{"account_mode": AccountModeCoding}}).SupportsNativeCNResponses())
	require.False(t, (&Account{Platform: PlatformZhipu}).SupportsNativeCNResponses())
	require.False(t, (&Account{Platform: PlatformOpenAI}).SupportsNativeCNResponses())
}

func TestAdaptiveProtocolBaseURLs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		platform      string
		mode          string
		wantChat      string
		wantAnthropic string
		wantResponses string
	}{
		{"kimi payg", PlatformKimi, AccountModePayG, DefaultKimiPayGBaseURL, DefaultKimiPayGAnthropicBaseURL, DefaultKimiPayGBaseURL},
		{"kimi coding", PlatformKimi, AccountModeCoding, DefaultKimiCodingBaseURL, DefaultKimiCodingAnthropicBaseURL, DefaultKimiCodingBaseURL},
		{"zhipu payg", PlatformZhipu, AccountModePayG, DefaultZhipuPayGBaseURL, DefaultZhipuAnthropicBaseURL, DefaultZhipuPayGBaseURL},
		{"zhipu coding", PlatformZhipu, AccountModeCoding, DefaultZhipuCodingBaseURL, DefaultZhipuAnthropicBaseURL, DefaultZhipuCodingBaseURL},
		{"deepseek", PlatformDeepseek, AccountModePayG, DefaultDeepseekBaseURL, DefaultDeepseekAnthropicBaseURL, DefaultDeepseekBaseURL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_protocol": APIProtocolAdaptive,
				"account_mode": tc.mode,
			}}
			require.Equal(t, tc.wantChat, account.GetCNProtocolBaseURL(APIProtocolChatCompletions))
			require.Equal(t, tc.wantAnthropic, account.GetCNProtocolBaseURL(APIProtocolAnthropic))
			require.Equal(t, tc.wantResponses, account.GetCNProtocolBaseURL(APIProtocolResponses))
			require.Equal(t, tc.wantAnthropic, account.GetAnthropicProtocolBaseURL())
		})
	}
}

func TestAdaptiveProtocolBaseURLOverrides(t *testing.T) {
	t.Parallel()

	account := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_protocol": APIProtocolAdaptive,
		"base_url":     "https://legacy-chat.example.com",
		"api_base_urls": map[string]any{
			APIProtocolChatCompletions: "https://chat.example.com",
			APIProtocolAnthropic:       "https://anthropic.example.com",
			APIProtocolResponses:       "https://responses.example.com",
		},
	}}

	require.Equal(t, "https://chat.example.com", account.GetOpenAIBaseURL())
	require.Equal(t, "https://chat.example.com", account.GetCNProtocolBaseURL(APIProtocolChatCompletions))
	require.Equal(t, "https://anthropic.example.com", account.GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://responses.example.com", account.GetCNProtocolBaseURL(APIProtocolResponses))
}

// TestAnthropicProtocolBaseURL 验证 Anthropic 协议默认端点与协议感知的
// OpenAI 格式 base 回退。
func TestAnthropicProtocolBaseURL(t *testing.T) {
	t.Parallel()

	// 默认端点（按供应商 × 模式）
	require.Equal(t, "https://api.moonshot.cn/anthropic", (&Account{
		Platform: PlatformKimi, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}).GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://api.kimi.com/coding", (&Account{
		Platform: PlatformKimi, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic, "account_mode": AccountModeCoding},
	}).GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://open.bigmodel.cn/api/anthropic", (&Account{
		Platform: PlatformZhipu, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}).GetAnthropicProtocolBaseURL())
	require.Equal(t, "https://api.deepseek.com/anthropic", (&Account{
		Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}).GetAnthropicProtocolBaseURL())

	// 凭证 base_url 覆盖默认值
	require.Equal(t, "https://custom.example.com/anthropic", (&Account{
		Platform: PlatformZhipu, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic, "base_url": "https://custom.example.com/anthropic"},
	}).GetAnthropicProtocolBaseURL())

	// 非 Anthropic 协议返回空串
	require.Empty(t, (&Account{
		Platform: PlatformZhipu, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://open.bigmodel.cn/api/paas/v4"},
	}).GetAnthropicProtocolBaseURL())
}

// TestGetOpenAIFormatBaseURL_ProtocolAware anthropic 协议账号的凭证 base_url
// 指向 Anthropic 端点，OpenAI 格式路径（模型同步等）必须回退到 CC 默认 base。
func TestGetOpenAIFormatBaseURL_ProtocolAware(t *testing.T) {
	t.Parallel()

	zhipuAnthropic := &Account{
		Platform: PlatformZhipu, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAnthropic,
			"base_url":     "https://open.bigmodel.cn/api/anthropic",
		},
	}
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4", zhipuAnthropic.GetOpenAIFormatBaseURL())

	kimiCodingAnthropic := &Account{
		Platform: PlatformKimi, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_protocol": APIProtocolAnthropic,
			"account_mode": AccountModeCoding,
			"base_url":     "https://api.kimi.com/coding",
		},
	}
	require.Equal(t, "https://api.kimi.com/coding/v1", kimiCodingAnthropic.GetOpenAIFormatBaseURL())

	// chat_completions 协议下行为不变（凭证 base_url 原样返回）
	ccAccount := &Account{
		Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://ds-relay.example.com"},
	}
	require.Equal(t, "https://ds-relay.example.com", ccAccount.GetOpenAIFormatBaseURL())
}

// TestBuildUpstreamModelsRequest_AnthropicProtocol 验证 Anthropic 协议账号
// 同步模型时仍使用协议感知的 OpenAI 格式基础地址。
func TestBuildUpstreamModelsRequest_AnthropicProtocol(t *testing.T) {
	t.Parallel()
	service := &AccountTestService{cfg: &config.Config{}}
	account := &Account{
		ID: 1, Platform: PlatformZhipu, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": APIProtocolAnthropic,
			"base_url":     "https://open.bigmodel.cn/api/anthropic",
		},
	}
	request, err := service.buildUpstreamModelsRequest(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4/models", request.URL.String())
}

// TestBuildOpenAIResponsesURLForPlatform deepseek 官方端点为 /responses（无 /v1）。
func TestBuildOpenAIResponsesURLForPlatform(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://api.deepseek.com/responses", buildOpenAIResponsesURLForPlatform(PlatformDeepseek, "https://api.deepseek.com"))
	require.Equal(t, "https://api.openai.com/v1/responses", buildOpenAIResponsesURLForPlatform(PlatformOpenAI, "https://api.openai.com"))
	require.Equal(t, "https://open.bigmodel.cn/api/paas/v4/responses", buildOpenAIResponsesURLForPlatform(PlatformZhipu, "https://open.bigmodel.cn/api/paas/v4"))
	require.Equal(t, "https://api.moonshot.cn/v1/responses", buildOpenAIResponsesURLForPlatform(PlatformKimi, "https://api.moonshot.cn/v1"))
	require.Equal(t, "https://api.kimi.com/coding/v1/responses", buildOpenAIResponsesURLForPlatform(PlatformKimi, "https://api.kimi.com/coding/v1"))
}

// TestNormalizeDeepSeekResponsesRequestBody 无状态适配：强制 store=false、
// 清除 previous_response_id；非原生 CN Responses 协议原样返回。
func TestNormalizeDeepSeekResponsesRequestBody(t *testing.T) {
	t.Parallel()

	deepseekResponses := &Account{
		Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolResponses},
	}
	body := []byte(`{"model":"deepseek-v4-pro","store":true,"previous_response_id":"resp_123","instructions":"codex default","max_output_tokens":32,"input":"hi"}`)
	normalized := normalizeDeepSeekResponsesRequestBody(deepseekResponses, body)
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
	require.False(t, gjson.GetBytes(normalized, "previous_response_id").Exists())
	require.Equal(t, "codex default", gjson.GetBytes(normalized, "instructions").String())
	require.Equal(t, int64(32), gjson.GetBytes(normalized, "max_output_tokens").Int())
	require.Equal(t, "deepseek-v4-pro", gjson.GetBytes(normalized, "model").String())

	deepseekAdaptive := &Account{
		Platform: PlatformDeepseek, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAdaptive},
	}
	adaptiveNormalized := normalizeDeepSeekResponsesRequestBody(deepseekAdaptive, body)
	require.False(t, gjson.GetBytes(adaptiveNormalized, "store").Bool())
	require.False(t, gjson.GetBytes(adaptiveNormalized, "previous_response_id").Exists())
	require.Equal(t, "codex default", gjson.GetBytes(adaptiveNormalized, "instructions").String())
	require.Equal(t, int64(32), gjson.GetBytes(adaptiveNormalized, "max_output_tokens").Int())

	// Kimi 固定 responses 与 coding adaptive 账号同样走无状态原生端点。
	kimiResponses := &Account{
		Platform: PlatformKimi, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolResponses},
	}
	kimiNormalized := normalizeDeepSeekResponsesRequestBody(kimiResponses, body)
	require.False(t, gjson.GetBytes(kimiNormalized, "store").Bool())
	require.False(t, gjson.GetBytes(kimiNormalized, "previous_response_id").Exists())

	kimiCodingAdaptive := &Account{
		Platform: PlatformKimi, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAdaptive, "account_mode": AccountModeCoding},
	}
	kimiCodingNormalized := normalizeDeepSeekResponsesRequestBody(kimiCodingAdaptive, body)
	require.False(t, gjson.GetBytes(kimiCodingNormalized, "store").Bool())
	require.False(t, gjson.GetBytes(kimiCodingNormalized, "previous_response_id").Exists())

	// 非 responses 协议（deepseek CC 账号）原样返回
	deepseekCC := &Account{Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
	require.Equal(t, string(body), string(normalizeDeepSeekResponsesRequestBody(deepseekCC, body)))

	// openai 账号原样返回
	openai := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.Equal(t, string(body), string(normalizeDeepSeekResponsesRequestBody(openai, body)))
}

// TestGetAnthropicAPIKeyAuthScheme_CNProvider CN 账号可经 extra 覆写鉴权方案，
// 默认保持 x-api-key。
func TestGetAnthropicAPIKeyAuthScheme_CNProvider(t *testing.T) {
	t.Parallel()

	zhipu := &Account{
		Platform: PlatformZhipu, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_protocol": APIProtocolAnthropic},
	}
	require.Equal(t, AnthropicAPIKeyAuthSchemeXAPIKey, zhipu.GetAnthropicAPIKeyAuthScheme())

	zhipu.Extra = map[string]any{"anthropic_apikey_auth_scheme": "authorization_bearer"}
	require.Equal(t, AnthropicAPIKeyAuthSchemeAuthorizationBearer, zhipu.GetAnthropicAPIKeyAuthScheme())
}
