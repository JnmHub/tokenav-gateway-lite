//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newGrokCacheTestContext(apiKeyID int64) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if apiKeyID > 0 {
		c.Set("api_key", &APIKey{ID: apiKeyID, Group: &Group{Platform: PlatformGrok}})
	}
	return c
}

func TestResolveGrokCacheIdentityStableAcrossAppendOnlyTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newGrokCacheTestContext(101)
	round1 := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"}]}`)
	round2 := []byte(`{"model":"grok","instructions":"be concise","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"input":[{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":"second question"}]}`)

	first := resolveGrokCacheIdentity(c, round1, "", "grok-4.5")
	second := resolveGrokCacheIdentity(c, round2, "", "grok-4.5")

	require.NotEmpty(t, first)
	require.Len(t, first, 36)
	require.Equal(t, first, second)
}

func TestResolveGrokCacheIdentityIsolatesAPIKeyAndMappedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","input":"same prompt"}`)

	base := resolveGrokCacheIdentity(newGrokCacheTestContext(201), body, "", "grok-4.5")
	otherTenant := resolveGrokCacheIdentity(newGrokCacheTestContext(202), body, "", "grok-4.5")
	otherModel := resolveGrokCacheIdentity(newGrokCacheTestContext(201), body, "", "grok-4.3")

	require.NotEmpty(t, base)
	require.NotEqual(t, base, otherTenant)
	require.NotEqual(t, base, otherModel)
}

func TestResolveGrokCacheIdentityExplicitHeaderPriority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"grok","prompt_cache_key":"body-key","input":"hi"}`)
	c := newGrokCacheTestContext(401)
	c.Request.Header.Set(grokConversationIDHeader, "grok-key")
	c.Request.Header.Set("conversation_id", "conversation-key")
	c.Request.Header.Set("session_id", "session-key")

	got := resolveGrokCacheIdentity(c, body, "explicit-argument", "grok-4.5")
	onlySession := newGrokCacheTestContext(401)
	onlySession.Request.Header.Set("session_id", "session-key")
	want := resolveGrokCacheIdentity(onlySession, []byte(`{"model":"grok","input":"unrelated"}`), "", "grok-4.5")

	require.Equal(t, want, got)
}

func TestResolveGrokCacheIdentityFailsClosedWithoutAPIKeyOrForCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withoutKey := newGrokCacheTestContext(0)
	withoutKey.Request.Header.Set(grokConversationIDHeader, "native-session")
	require.Empty(t, resolveGrokCacheIdentity(withoutKey, []byte(`{"model":"grok","input":"hi"}`), "", "grok-4.5"))

	compact := newGrokCacheTestContext(501)
	compact.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	require.Empty(t, resolveGrokCacheIdentity(compact, []byte(`{"model":"grok","prompt_cache_key":"key"}`), "", "grok-4.5"))
}

func TestApplyGrokResponsesCacheIdentityWritesIsolatedValueAndFreeRouteTools(t *testing.T) {
	source := []byte(`{"model":"grok-4.5","prompt_cache_key":"raw-client-key"}`)
	body, err := applyGrokResponsesCacheIdentity(source, source, "isolated-id", true)

	require.NoError(t, err)
	require.Equal(t, "isolated-id", gjson.GetBytes(body, "prompt_cache_key").String())
	require.Equal(t, "web_search", gjson.GetBytes(body, "tools.0.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(body, "tools.1.type").String())
	require.Equal(t, grokFreeCacheDisabledToolChoice, gjson.GetBytes(body, "tool_choice").String())

	chatBody, err := stripGrokChatPromptCacheKey(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(chatBody, "prompt_cache_key").Exists())
}

func TestApplyGrokResponsesCacheIdentityPreservesExplicitClientToolIntent(t *testing.T) {
	source := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`)
	body, err := applyGrokResponsesCacheIdentity(source, source, "isolated-id", true)

	require.NoError(t, err)
	require.Equal(t, "lookup", gjson.GetBytes(body, "tools.0.name").String())
	require.Equal(t, "auto", gjson.GetBytes(body, "tool_choice").String())
}

func TestGrokFreeFunctionToolCacheRouteRequiresKnownFreeTier(t *testing.T) {
	intentBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`)
	tests := []struct {
		name    string
		account *Account
		wantMix bool
	}{
		{
			name: "free credential tier",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(801)
				a.Credentials["subscription_tier"] = "free"
				return a
			}(),
			wantMix: false,
		},
		{
			name: "free billing tier",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(802)
				a.Extra = map[string]any{grokBillingExtraKey: map[string]any{"plan": "FREE"}}
				return a
			}(),
			wantMix: false,
		},
		{
			name: "free successful blank monthly billing",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(803)
				a.Extra = map[string]any{grokBillingExtraKey: map[string]any{
					"status_code":        http.StatusOK,
					"monthly_updated_at": "2026-07-15T05:00:00Z",
				}}
				return a
			}(),
			wantMix: false,
		},
		{
			name: "free rolling token quota",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(804)
				a.Extra = map[string]any{grokQuotaSnapshotExtraKey: map[string]any{
					"headers_observed": true,
					"tokens":           map[string]any{"limit": xai.GrokFreeRolling24hTokenLimit},
				}}
				return a
			}(),
			wantMix: false,
		},
		{
			name: "paid credential tier unchanged",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(805)
				a.Credentials["subscription_tier"] = "supergrok"
				return a
			}(),
		},
		{
			name: "paid billing overrides stale free quota",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(806)
				a.Extra = map[string]any{
					grokBillingExtraKey: map[string]any{"plan": "SuperGrok", "status_code": http.StatusOK},
					grokQuotaSnapshotExtraKey: map[string]any{
						"headers_observed": true,
						"tokens":           map[string]any{"limit": xai.GrokFreeRolling24hTokenLimit},
					},
				}
				return a
			}(),
		},
		{
			name: "partial billing remains unknown",
			account: func() *Account {
				a := newGrokCacheRouteTestAccount(807)
				a.Extra = map[string]any{grokBillingExtraKey: map[string]any{
					"status_code":    http.StatusOK,
					"partial":        true,
					"failed_windows": []string{"monthly"},
				}}
				return a
			}(),
		},
		{
			name:    "unknown tier unchanged",
			account: newGrokCacheRouteTestAccount(808),
		},
		{
			name: "api key unchanged",
			account: &Account{
				ID:          809,
				Platform:    PlatformGrok,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "token"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := applyGrokFreeFunctionToolCacheRoute(intentBody, intentBody, tt.account, "isolated-id")

			require.NoError(t, err)
			tools := gjson.GetBytes(body, "tools").Array()
			if tt.wantMix {
				require.Len(t, tools, 3)
				require.Equal(t, "function", tools[0].Get("type").String())
				require.Equal(t, "web_search", tools[1].Get("type").String())
				require.Equal(t, "x_search", tools[2].Get("type").String())
				return
			}
			require.Len(t, tools, 1)
		})
	}
}

func TestKnownGrokFreeAccountPrefersAccessTokenTier(t *testing.T) {
	free := newGrokCacheRouteTestAccount(810)
	free.Credentials["access_token"] = grokTierJWT(map[string]any{"tier": 0})
	free.Credentials["subscription_tier"] = "supergrok_heavy"
	require.True(t, isKnownGrokFreeAccount(free))

	paid := newGrokCacheRouteTestAccount(811)
	paid.Credentials["access_token"] = grokTierJWT(map[string]any{"tier": 5})
	paid.Credentials["subscription_tier"] = "free"
	require.False(t, isKnownGrokFreeAccount(paid))
}

func TestGrokFreeFunctionToolCacheRoutePreservesIneligibleIntents(t *testing.T) {
	account := newGrokCacheRouteTestAccount(820)
	account.Credentials["subscription_tier"] = "free"
	tests := []struct {
		name string
		body string
	}{
		{name: "empty tools", body: `{"model":"grok","tools":[]}`},
		{name: "native tool", body: `{"model":"grok","tools":[{"type":"web_search"}],"tool_choice":"auto"}`},
		{name: "required choice", body: `{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"required"}`},
		{name: "specific choice", body: `{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":{"type":"function","name":"lookup"}}`},
		{name: "unsupported mixed tool", body: `{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"namespace","name":"client_tools"}],"tool_choice":"auto"}`},
		{name: "chat completions function shape", body: `{"model":"grok","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"auto"}`},
		{name: "incomplete function", body: `{"model":"grok","tools":[{"type":"function","parameters":{"type":"object"}}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := applyGrokFreeFunctionToolCacheRoute([]byte(tt.body), []byte(tt.body), account, "isolated-id")

			require.NoError(t, err)
			require.JSONEq(t, tt.body, string(body))
		})
	}

	noneBody := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"none"}`)
	nonePatched, err := applyGrokFreeFunctionToolCacheRoute(noneBody, noneBody, account, "isolated-id")
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(nonePatched, "tools").Array(), 3)
	require.Equal(t, "none", gjson.GetBytes(nonePatched, "tool_choice").String(), "补齐缓存路由工具时不得启用执行")
}

func TestGrokFreeFunctionToolCacheRouteRequiresIdentityAndAvoidsDuplicateNativeTools(t *testing.T) {
	account := newGrokCacheRouteTestAccount(830)
	account.Credentials["subscription_tier"] = "free"
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"}],"tool_choice":"auto"}`)

	unchanged, err := applyGrokFreeFunctionToolCacheRoute(body, body, account, "")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(unchanged))

	first, err := applyGrokFreeFunctionToolCacheRoute(body, body, account, "isolated-id")
	require.NoError(t, err)
	require.Len(t, gjson.GetBytes(first, "tools").Array(), 1)

	second, err := applyGrokFreeFunctionToolCacheRoute(first, body, account, "isolated-id")
	require.NoError(t, err)
	require.JSONEq(t, string(first), string(second))
}

func TestGrokFreeFunctionToolCacheRouteConvertsFunctionSearchAndDeduplicates(t *testing.T) {
	account := newGrokCacheRouteTestAccount(831)
	account.Credentials["subscription_tier"] = "free"
	body := []byte(`{"model":"grok","tools":[{"type":"function","name":"lookup"},{"type":"function","name":"web_search"}],"tool_choice":"auto"}`)

	patched, err := applyGrokFreeFunctionToolCacheRoute(body, body, account, "isolated-id")
	require.NoError(t, err)
	tools := gjson.GetBytes(patched, "tools").Array()
	require.Len(t, tools, 3)
	require.Equal(t, "lookup", tools[0].Get("name").String())
	require.Equal(t, "web_search", tools[1].Get("type").String())
	require.Equal(t, "x_search", tools[2].Get("type").String())
}

func TestGrokClientToolCachePolicyV0162HonorsTierAndAdminSwitch(t *testing.T) {
	free := func() *Account {
		return &Account{
			Platform:    PlatformGrok,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"subscription_tier": "free"},
		}
	}

	enabled, explicit := grokClientToolCacheAccountPolicy(free())
	require.True(t, enabled)
	require.False(t, explicit)

	disabled := free()
	disabled.Extra = map[string]any{grokClientToolCacheOptInExtraKey: false}
	enabled, explicit = grokClientToolCacheAccountPolicy(disabled)
	require.False(t, enabled)
	require.True(t, explicit)

	paid := free()
	paid.Credentials["subscription_tier"] = "supergrok"
	enabled, explicit = grokClientToolCacheAccountPolicy(paid)
	require.False(t, enabled)
	require.False(t, explicit)

	apiKey := free()
	apiKey.Type = AccountTypeAPIKey
	enabled, explicit = grokClientToolCacheAccountPolicy(apiKey)
	require.False(t, enabled)
	require.False(t, explicit)

	body := []byte(`{"tools":[{"type":"function","name":"read_file"}],"tool_choice":"auto"}`)
	c := newGrokCacheTestContext(201)
	c.Request.Header.Set(grokClientToolCacheOptInHeader, "prefer-cache")
	patched, err := applyGrokFreeRequestToolCacheRoute(c, body, body, disabled, "identity")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched), "管理员显式关闭不能被客户端 Header 覆盖")

	c = newGrokCacheTestContext(202)
	c.Request.Header.Set(grokClientToolCacheOptInHeader, "off")
	patched, err = applyGrokFreeRequestToolCacheRoute(c, body, body, free(), "identity")
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(patched), "请求级关闭必须优先")

	c = newGrokCacheTestContext(203)
	patched, err = applyGrokFreeRequestToolCacheRoute(c, body, body, free(), "identity")
	require.NoError(t, err)
	require.Equal(t, "web_search", gjson.GetBytes(patched, "tools.1.type").String())
	require.Equal(t, "x_search", gjson.GetBytes(patched, "tools.2.type").String())
}

func TestExtractClaudeCodeSessionIDV0162(t *testing.T) {
	c := newGrokCacheTestContext(204)
	c.Request.Header.Set(claudeCodeSessionHeader, "header-session")
	require.Equal(t, "header-session", extractClaudeCodeSessionID(c, []byte(`{"metadata":{"user_id":"user_session_abc-def"}}`)))
	require.Equal(t, "abc-def", extractClaudeCodeSessionIDFromPayload([]byte(`{"metadata":{"user_id":"user_session_abc-def"}}`)))
	require.Equal(t, "json-session", extractClaudeCodeSessionIDFromPayload([]byte(`{"metadata":{"user_id":"{\"session_id\":\"json-session\"}"}}`)))
}

func TestGrokConversationHeaderOnlyAffectsGrokGroupSessionResolution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"prompt_cache_key":"body-session"}`)

	grokContext := newGrokCacheTestContext(601)
	grokContext.Request.Header.Set(grokConversationIDHeader, "native-grok-session")
	require.Equal(t, "native-grok-session", (&OpenAIGatewayService{}).ExtractSessionID(grokContext, body))

	openAIContext := newGrokCacheTestContext(601)
	openAIContext.Set("api_key", &APIKey{ID: 601, Group: &Group{Platform: PlatformOpenAI}})
	openAIContext.Request.Header.Set(grokConversationIDHeader, "must-be-ignored")
	require.Equal(t, "body-session", (&OpenAIGatewayService{}).ExtractSessionID(openAIContext, body))
}

func newGrokCacheRouteTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "access-token"},
		Extra:       map[string]any{},
	}
}

func TestResolveGrokCacheIdentityConcurrentDeterminism(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const workers = 50
	body := []byte(`{"model":"grok","messages":[{"role":"system","content":"stable"},{"role":"user","content":"hello"}]}`)
	identities := make(chan string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			identities <- resolveGrokCacheIdentity(newGrokCacheTestContext(701), body, "", "grok-4.5")
		}()
	}
	wg.Wait()
	close(identities)

	first := ""
	for identity := range identities {
		if first == "" {
			first = identity
		}
		require.Equal(t, first, identity)
	}
	require.NotEmpty(t, first)
}
