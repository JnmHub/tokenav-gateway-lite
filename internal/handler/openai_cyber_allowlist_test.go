package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// [官方 v0.2.13 移植] handler 侧 cyber 白名单短路的护栏。
//
// 这组用例守的是「白名单真的接进了准入路径」：非白名单用户照常派生会话屏蔽 key，
// 白名单用户直接短路成空 key（= 视为未被屏蔽），从而在 HTTP 与 WS 两条准入路径
// 都放行。复用 auth_oauth_captcha_start_test.go 里的 map 型设置仓库 stub。

func newCyberAllowlistTestHandler(allowlist string) *OpenAIGatewayHandler {
	values := map[string]string{}
	if allowlist != "" {
		values[service.SettingKeyCyberPolicyUserAllowlist] = allowlist
	}
	settings := service.NewSettingService(&oauthCaptchaStartSettingRepoStub{values: values}, &config.Config{})
	gwSvc := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, // 1-6 各 repository
		nil,                          // 7 cache
		&config.Config{},             // 8 cfg
		nil, nil, nil, nil,           // 9-12 scheduler/concurrency/billing/ratelimit
		nil,                          // 13 billingCacheService
		nil,                          // 14 httpUpstream
		nil, nil, nil, nil, nil,      // 15-19 deferred/token/resolver/channel/balanceNotify
		settings,                     // 20 settingService
		nil,                          // 21 userPlatformQuotaRepo
	)
	return &OpenAIGatewayHandler{gatewayService: gwSvc}
}

func newCyberAllowlistTestContext(t *testing.T, body string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	return c
}

func TestFindCyberSessionBlockKeyForAPIKeyRespectsAllowlist(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"sess-cyber-1"}`)
	c := newCyberAllowlistTestContext(t, string(body))
	apiKey := &service.APIKey{ID: 9, UserID: 12}

	// 非白名单：照常派生 key（后续会去查屏蔽表）。
	notAllowed := newCyberAllowlistTestHandler("")
	require.NotEmpty(t, notAllowed.findCyberSessionBlockKeyForAPIKey(c, apiKey, body))

	// 白名单：短路成空串 = 调用方视为未被屏蔽，直接放行。
	allowed := newCyberAllowlistTestHandler("12")
	require.Empty(t, allowed.findCyberSessionBlockKeyForAPIKey(c, apiKey, body))
	require.True(t, allowed.cyberPolicyLogOnly(c, apiKey))

	// 白名单里没有这个用户时不受影响。
	require.False(t, allowed.cyberPolicyLogOnly(c, &service.APIKey{ID: 9, UserID: 13}))
	require.NotEmpty(t, allowed.findCyberSessionBlockKeyForAPIKey(c, &service.APIKey{ID: 9, UserID: 13}, body))
}

// 没有显式会话标识时本来就返回空 key（放行），白名单不改变这个语义。
func TestFindCyberSessionBlockKeyForAPIKeyWithoutSessionID(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	c := newCyberAllowlistTestContext(t, string(body))

	handler := newCyberAllowlistTestHandler("")
	require.Empty(t, handler.findCyberSessionBlockKeyForAPIKey(c, &service.APIKey{ID: 9, UserID: 12}, body))
}

// nil 入参不得 panic（handler 里多处可选上下文）。
func TestFindCyberSessionBlockKeyForAPIKeyNilSafety(t *testing.T) {
	c := newCyberAllowlistTestContext(t, `{}`)
	handler := newCyberAllowlistTestHandler("12")

	require.Empty(t, handler.findCyberSessionBlockKeyForAPIKey(c, nil, nil))
	require.Empty(t, handler.findCyberSessionBlockKeyForAPIKey(nil, &service.APIKey{ID: 9, UserID: 12}, nil))
	require.False(t, handler.cyberPolicyLogOnly(nil, &service.APIKey{ID: 9, UserID: 12}))
}
