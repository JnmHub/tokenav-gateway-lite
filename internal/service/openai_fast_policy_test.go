package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAIFastPolicyRepoStub struct {
	values        map[string]string
	getValueCalls atomic.Int64
}

func (s *openAIFastPolicyRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	panic("unexpected Get call")
}

func (s *openAIFastPolicyRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	s.getValueCalls.Add(1)
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func TestOpenAIFastPolicySettingsConcurrentReadsShareOneRepositoryLoad(t *testing.T) {
	settings := openAIFastFilterPriorityPolicy()
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	repo := &openAIFastPolicyRepoStub{values: map[string]string{SettingKeyOpenAIFastPolicySettings: string(raw)}}
	svc := NewSettingService(repo, &config.Config{})

	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, getErr := svc.GetOpenAIFastPolicySettings(context.Background())
			require.NoError(t, getErr)
			require.Len(t, got.Rules, 1)
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), repo.getValueCalls.Load())
}

func TestSetOpenAIFastPolicySettingsRefreshesHotPathCache(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	first, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Empty(t, first.Rules)

	updated := openAIFastFilterPriorityPolicy()
	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), updated))
	second, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, second.Rules, 1)
	require.Equal(t, int64(1), repo.getValueCalls.Load(), "写后读取必须直接命中进程缓存")
}

func (s *openAIFastPolicyRepoStub) Set(ctx context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func (s *openAIFastPolicyRepoStub) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	panic("unexpected GetMultiple call")
}

func (s *openAIFastPolicyRepoStub) SetMultiple(ctx context.Context, settings map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (s *openAIFastPolicyRepoStub) GetAll(ctx context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (s *openAIFastPolicyRepoStub) Delete(ctx context.Context, key string) error {
	panic("unexpected Delete call")
}

func newOpenAIGatewayServiceWithSettings(t *testing.T, settings *OpenAIFastPolicySettings) *OpenAIGatewayService {
	t.Helper()
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	if settings != nil {
		raw, err := json.Marshal(settings)
		require.NoError(t, err)
		repo.values[SettingKeyOpenAIFastPolicySettings] = string(raw)
	}
	return &OpenAIGatewayService{
		settingService: NewSettingService(repo, &config.Config{}),
	}
}

func openAIFastFilterPriorityPolicy() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionFilter,
			Scope:          BetaPolicyScopeAll,
			ModelWhitelist: []string{},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
}

func TestEvaluateOpenAIFastPolicy_DefaultPassesKnownTiers(t *testing.T) {
	require.Empty(t, DefaultOpenAIFastPolicySettings().Rules, "default policy must not rewrite service_tier unless admin configured rules")

	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5-turbo", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)

	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierFlex)
	require.Equal(t, BetaPolicyActionPass, action)

	// empty tier → pass
	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", "")
	require.Equal(t, BetaPolicyActionPass, action)
}

func TestEvaluateOpenAIFastPolicy_BlockRuleCarriesMessage(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionBlock,
			Scope:          BetaPolicyScopeAll,
			ErrorMessage:   "fast mode is not allowed",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	action, msg := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionBlock, action)
	require.Equal(t, "fast mode is not allowed", msg)
}

func TestEvaluateOpenAIFastPolicy_ScopeFiltersOAuth(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeOAuth,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)

	// OAuth account → rule matches
	oauthAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), oauthAccount, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, action)

	// API Key account → rule skipped → pass
	apiKeyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	action, _ = svc.evaluateOpenAIFastPolicy(context.Background(), apiKeyAccount, "gpt-4", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, action)
}

func TestApplyOpenAIFastPolicyToBody_DefaultPassesPriorityAndFast(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","messages":[]}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))

	body = []byte(`{"model":"gpt-5.5","service_tier":"fast"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())

	body = []byte(`{"model":"gpt-4","service_tier":"priority"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-4", body)
	require.Error(t, err)
	require.Equal(t, string(body), string(updated))
	var unsupported *OpenAIFastBlockedError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "priority_model_unsupported", unsupported.APIErrorCode())

	// No service_tier → no-op
	body = []byte(`{"model":"gpt-5.5"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
}

func TestApplyOpenAIPriorityPolicyToBody_FollowsRequestWithoutAPIKeyPreference(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	requestCtx := context.Background()
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())

	t.Run("normalizes fast but preserves original audit value", func(t *testing.T) {
		updated, decision, err := svc.applyOpenAIPriorityPolicyToBody(
			requestCtx,
			account,
			"gpt-5.5",
			[]byte(`{"model":"gpt-5.5","service_tier":"fast"}`),
		)
		require.NoError(t, err)
		require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())
		require.NotNil(t, decision.RequestedServiceTier)
		require.Equal(t, "fast", *decision.RequestedServiceTier)
		require.NotNil(t, decision.ServiceTier)
		require.Equal(t, "priority", *decision.ServiceTier)
		require.Equal(t, BetaPolicyActionPass, decision.PolicyAction)
	})

	t.Run("missing tier remains missing", func(t *testing.T) {
		updated, decision, err := svc.applyOpenAIPriorityPolicyToBody(
			requestCtx,
			account,
			"gpt-5.5",
			[]byte(`{"model":"gpt-5.5"}`),
		)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(updated, "service_tier").Exists())
		require.Nil(t, decision.ServiceTier)
	})

	t.Run("standard retry marker prevents reinjection", func(t *testing.T) {
		requested := "priority"
		sent := "priority"
		initial := OpenAIPriorityDecision{
			RequestedServiceTier:       &requested,
			ServiceTier:                &sent,
			PolicyAction:               BetaPolicyActionPass,
			DetectionByRequestEnabled:  true,
			DetectionByResponseEnabled: true,
		}
		retryCtx := contextWithOpenAIPriorityStandardRetry(requestCtx, initial, "service_tier_unsupported")
		updated, decision, err := svc.applyOpenAIPriorityPolicyToBody(
			retryCtx,
			account,
			"gpt-5.5",
			[]byte(`{"model":"gpt-5.5","service_tier":"priority"}`),
		)
		require.NoError(t, err)
		require.False(t, gjson.GetBytes(updated, "service_tier").Exists())
		require.Nil(t, decision.ServiceTier)
		require.Equal(t, "priority", *decision.RequestedServiceTier)
		require.Equal(t, "service_tier_unsupported", decision.FallbackReason)
	})
}

func TestApplyOpenAIPriorityPolicyToBody_ForcePriorityRule(t *testing.T) {
	settings := DefaultOpenAIFastPolicySettings()
	settings.Rules = []OpenAIFastPolicyRule{{
		ID: "force_rule", ServiceTier: OpenAIFastTierFlex,
		Action: OpenAIFastPolicyActionForcePriority, Scope: BetaPolicyScopeAll,
	}}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	updated, decision, err := svc.applyOpenAIPriorityPolicyToBody(
		context.Background(),
		account,
		"gpt-5.5",
		[]byte(`{"model":"gpt-5.5","service_tier":"flex"}`),
	)
	require.NoError(t, err)
	require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())
	require.Equal(t, OpenAIFastPolicyActionForcePriority, decision.PolicyAction)
	require.Equal(t, "force_rule", decision.PolicyRuleID)

	updated, decision, err = svc.applyOpenAIPriorityPolicyToBody(context.Background(), account, "gpt-5.5", []byte(`{"model":"gpt-5.5"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(updated, "service_tier").Exists())
	require.Nil(t, decision.ServiceTier)
}

func TestApplyOpenAIPriorityPolicyToBody_LongContextPricingPolicy(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body, err := json.Marshal(map[string]any{
		"model":        "gpt-5.6-sol",
		"service_tier": "priority",
		"input":        strings.Repeat("token ", 273000),
	})
	require.NoError(t, err)

	t.Run("missing long priority prices fail closed", func(t *testing.T) {
		svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
		_, decision, applyErr := svc.applyOpenAIPriorityPolicyToBody(context.Background(), account, "gpt-5.6-sol", body)
		require.Error(t, applyErr)
		require.NotNil(t, decision.ServiceTier)
		var blocked *OpenAIFastBlockedError
		require.ErrorAs(t, applyErr, &blocked)
		require.Equal(t, http.StatusBadRequest, blocked.HTTPStatusCode())
		require.Equal(t, "priority_long_context_pricing_unavailable", blocked.APIErrorCode())
	})

	t.Run("configured long priority prices keep priority", func(t *testing.T) {
		svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
		pricingService := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
			"custom-long-priority": {
				InputCostPerToken:                   1e-6,
				OutputCostPerToken:                  2e-6,
				InputCostPerTokenPriority:           2e-6,
				OutputCostPerTokenPriority:          4e-6,
				LongContextInputTokenThreshold:      272000,
				InputCostPerTokenPriorityAbove272K:  3e-6,
				OutputCostPerTokenPriorityAbove272K: 6e-6,
				SupportsServiceTier:                 true,
			},
		}}
		svc.billingService = NewBillingService(&config.Config{}, pricingService)
		customBody, marshalErr := json.Marshal(map[string]any{
			"model":        "custom-long-priority",
			"service_tier": "priority",
			"input":        strings.Repeat("token ", 273000),
		})
		require.NoError(t, marshalErr)
		updated, decision, applyErr := svc.applyOpenAIPriorityPolicyToBody(context.Background(), account, "custom-long-priority", customBody)
		require.NoError(t, applyErr)
		require.NotNil(t, decision.ServiceTier)
		require.Equal(t, "priority", gjson.GetBytes(updated, "service_tier").String())
	})
}

func TestApplyOpenAIPriorityPolicyToBody_DoesNotInjectImageRequests(t *testing.T) {
	settings := DefaultOpenAIFastPolicySettings()
	settings.Rules = []OpenAIFastPolicyRule{{ServiceTier: OpenAIFastTierAny, Action: OpenAIFastPolicyActionForcePriority, Scope: BetaPolicyScopeAll}}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation"}]}`)

	updated, decision, err := svc.applyOpenAIPriorityPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.Equal(t, string(body), string(updated))
	require.Nil(t, decision.ServiceTier)
	require.Equal(t, BetaPolicyActionPass, decision.PolicyAction)
}

func TestOpenAIFastPolicySettingsValidationAndLegacyDefaults(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, &config.Config{})

	require.Error(t, settingService.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Version: OpenAIFastPolicySettingsVersion,
	}))
	require.NoError(t, settingService.SetOpenAIFastPolicySettings(context.Background(), DefaultOpenAIFastPolicySettings()))

	stored, err := settingService.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.True(t, stored.DetectionByRequestEnabled)
	require.True(t, stored.DetectionByResponseEnabled)

	repo.values[SettingKeyOpenAIFastPolicySettings] = `{"rules":[]}`
	legacy, err := settingService.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.True(t, legacy.DetectionByRequestEnabled)
	require.True(t, legacy.DetectionByResponseEnabled)
}

func TestEvaluateOpenAIFastPolicy_UserRulesBeforeGlobalRules(t *testing.T) {
	settings := DefaultOpenAIFastPolicySettings()
	settings.Rules = []OpenAIFastPolicyRule{
		{ID: "global", ServiceTier: OpenAIFastTierPriority, Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll},
		{ID: "user_42", ServiceTier: OpenAIFastTierPriority, Action: BetaPolicyActionPass, Scope: BetaPolicyScopeAll, UserIDs: []int64{42}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	allowedCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	allowed := svc.evaluateOpenAIFastPolicyMatch(allowedCtx, account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionPass, allowed.Action)
	require.Equal(t, "user_42", allowed.RuleID)

	filteredCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(43))
	filtered := svc.evaluateOpenAIFastPolicyMatch(filteredCtx, account, "gpt-5.5", OpenAIFastTierPriority)
	require.Equal(t, BetaPolicyActionFilter, filtered.Action)
	require.Equal(t, "global", filtered.RuleID)
}

func TestEvaluateOpenAIFastPolicy_FallbackKeepsRuleEvidence(t *testing.T) {
	settings := DefaultOpenAIFastPolicySettings()
	settings.Rules = []OpenAIFastPolicyRule{{
		ID:                   "priority_whitelist",
		ServiceTier:          OpenAIFastTierPriority,
		Action:               BetaPolicyActionBlock,
		Scope:                BetaPolicyScopeAll,
		ModelWhitelist:       []string{"gpt-5.5"},
		FallbackAction:       OpenAIFastPolicyActionForcePriority,
		FallbackErrorMessage: "unused",
	}}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	match := svc.evaluateOpenAIFastPolicyMatch(context.Background(), account, "gpt-5.4", OpenAIFastTierPriority)
	require.Equal(t, OpenAIFastPolicyActionForcePriority, match.Action)
	require.Equal(t, "priority_whitelist", match.RuleID)
	require.True(t, match.UsedFallback)
}

func TestApplyOpenAIFastPolicyToBody_ExplicitFilterRemovesField(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, openAIFastFilterPriorityPolicy())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority","messages":[]}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)

	body = []byte(`{"model":"gpt-5.5","service_tier":"fast"}`)
	updated, err = svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.NotContains(t, string(updated), `"service_tier"`)
}

// TestApplyOpenAIFastPolicyToBody_OfficialTiersBypassDefaultRule 验证默认配置
// 下客户端显式发送的 OpenAI 官方合法 tier 能透传到上游而不被静默剥离。
func TestApplyOpenAIFastPolicyToBody_OfficialTiersBypassDefaultRule(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"auto", "default", "scale"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err, "tier %q should pass without error", tier)
		require.Contains(t, string(updated), `"service_tier":"`+tier+`"`,
			"tier %q should be preserved in body under default policy", tier)
	}

	// evaluate 层也应判定为 pass（默认配置没有内置规则）。
	for _, tier := range []string{"auto", "default", "scale"} {
		action, _ := svc.evaluateOpenAIFastPolicy(context.Background(), account, "gpt-5.5", tier)
		require.Equal(t, BetaPolicyActionPass, action, "tier %q should evaluate to pass", tier)
	}
}

// TestApplyOpenAIFastPolicyToBody_AllRuleStripsOfficialTiers 验证管理员显式配置
// ServiceTier=all + Action=filter 规则后，auto/default/scale 等官方 tier 也会
// 被剥离。这是符合预期的——首条匹配 short-circuit，"all" 覆盖任意已识别 tier。
func TestApplyOpenAIFastPolicyToBody_AllRuleStripsOfficialTiers(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierAny,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeAll,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, tier := range []string{"auto", "default", "scale", "priority", "flex"} {
		body := []byte(`{"model":"gpt-5.5","service_tier":"` + tier + `"}`)
		updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
		require.NoError(t, err)
		require.NotContains(t, string(updated), `"service_tier"`,
			"tier %q should be stripped under ServiceTier=all + filter rule", tier)
	}
}

// TestApplyOpenAIFastPolicyToBody_UnknownTierStripped 验证未知 tier 在统一
// 策略入口被剥离，避免不同协议入口把非法值透传给上游并产生不一致行为。
func TestApplyOpenAIFastPolicyToBody_UnknownTierStripped(t *testing.T) {
	svc := newOpenAIGatewayServiceWithSettings(t, DefaultOpenAIFastPolicySettings())
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	// normalize 阶段会将未知值剥离
	require.Nil(t, normalizeOpenAIServiceTier("xxx"))

	body := []byte(`{"model":"gpt-5.5","service_tier":"xxx"}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(updated, "service_tier").Exists())
}

func TestApplyOpenAIFastPolicyToBody_BlockReturnsTypedError(t *testing.T) {
	settings := &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier:    OpenAIFastTierPriority,
			Action:         BetaPolicyActionBlock,
			Scope:          BetaPolicyScopeAll,
			ErrorMessage:   "fast mode is blocked for gpt-5.5",
			ModelWhitelist: []string{"gpt-5.5"},
			FallbackAction: BetaPolicyActionPass,
		}},
	}
	svc := newOpenAIGatewayServiceWithSettings(t, settings)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	body := []byte(`{"model":"gpt-5.5","service_tier":"priority"}`)
	updated, err := svc.applyOpenAIFastPolicyToBody(context.Background(), account, "gpt-5.5", body)
	require.Error(t, err)
	var blocked *OpenAIFastBlockedError
	require.True(t, errors.As(err, &blocked))
	require.Contains(t, blocked.Message, "fast mode is blocked")
	require.Equal(t, string(body), string(updated)) // body not mutated on block
}

func TestSetOpenAIFastPolicySettings_Validation(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})

	// Invalid action rejected
	err := svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      "bogus",
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	// Invalid service_tier rejected
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: "turbo",
			Action:      BetaPolicyActionPass,
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.Error(t, err)

	// Valid settings persisted
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Rules: []OpenAIFastPolicyRule{{
			ServiceTier: OpenAIFastTierPriority,
			Action:      BetaPolicyActionFilter,
			Scope:       BetaPolicyScopeAll,
		}},
	})
	require.NoError(t, err)

	got, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Rules, 1)
	require.Equal(t, OpenAIFastTierPriority, got.Rules[0].ServiceTier)
	require.True(t, got.DetectionByRequestEnabled)
	require.True(t, got.DetectionByResponseEnabled)

	// 新版请求明确同时关闭两个判断来源时必须拒绝。
	err = svc.SetOpenAIFastPolicySettings(context.Background(), &OpenAIFastPolicySettings{
		Version:                    OpenAIFastPolicySettingsVersion,
		DetectionByRequestEnabled:  false,
		DetectionByResponseEnabled: false,
		Rules:                      []OpenAIFastPolicyRule{},
	})
	require.ErrorContains(t, err, "cannot both be disabled")
}

func TestSetOpenAIFastPolicySettings_GeneratesStableUniqueRuleIDs(t *testing.T) {
	repo := &openAIFastPolicyRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	rule := OpenAIFastPolicyRule{
		ServiceTier: OpenAIFastTierPriority,
		Action:      BetaPolicyActionPass,
		Scope:       BetaPolicyScopeAll,
	}
	settings := DefaultOpenAIFastPolicySettings()
	settings.Rules = []OpenAIFastPolicyRule{rule, rule}

	require.NoError(t, svc.SetOpenAIFastPolicySettings(context.Background(), settings))
	stored, err := svc.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	require.Len(t, stored.Rules, 2)
	require.NotEmpty(t, stored.Rules[0].ID)
	require.NotEmpty(t, stored.Rules[1].ID)
	require.NotEqual(t, stored.Rules[0].ID, stored.Rules[1].ID)
	require.Equal(t, stored.Rules[0].ID+"-2", stored.Rules[1].ID)

	settings = DefaultOpenAIFastPolicySettings()
	settings.Rules = []OpenAIFastPolicyRule{
		{ID: "duplicate", ServiceTier: OpenAIFastTierPriority, Action: BetaPolicyActionPass, Scope: BetaPolicyScopeAll},
		{ID: "duplicate", ServiceTier: OpenAIFastTierFlex, Action: BetaPolicyActionPass, Scope: BetaPolicyScopeAll},
	}
	require.ErrorContains(t, svc.SetOpenAIFastPolicySettings(context.Background(), settings), "duplicate id")
}
