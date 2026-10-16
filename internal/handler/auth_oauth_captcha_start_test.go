package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// [官方 v0.2.13 移植] OAuth 登录起点行为验证码闸门的护栏。
//
// 这组用例是「闸门是否真的接在每个登录起点上」的守卫：逐个 provider 调真实
// handler，验证码开启且缺 proof 时必须 400、不产生任何 cookie 副作用；POST 带
// proof 时返回 authorize_url 并调用一次验证器。

// oauthCaptchaStartVerifierStub 记录验证器调用次数与收到的 proof。
type oauthCaptchaStartVerifierStub struct {
	calls int
	proof service.TencentCaptchaProof
}

func (v *oauthCaptchaStartVerifierStub) VerifyTicket(
	_ context.Context,
	_ service.TencentCaptchaCredentials,
	proof service.TencentCaptchaProof,
	_ string,
) (*service.TencentCaptchaVerifyResponse, error) {
	v.calls++
	v.proof = proof
	return &service.TencentCaptchaVerifyResponse{CaptchaCode: 1}, nil
}

// newOAuthCaptchaStartTestHandler 造一个只依赖设置仓库的 AuthHandler。
// 自带 map 型设置仓库 stub（不引 unit tag 文件里的 stub，保证本护栏在
// `make test` 与 `make test-unit` 两种模式下都会跑）。
func newOAuthCaptchaStartTestHandler(enabled bool) (*AuthHandler, *oauthCaptchaStartVerifierStub) {
	values := map[string]string{}
	if enabled {
		values = map[string]string{
			service.SettingKeyTencentCaptchaEnabled:        "true",
			service.SettingKeyTencentCaptchaAppID:          "123456789",
			service.SettingKeyTencentCaptchaAppSecretKey:   "app-secret",
			service.SettingKeyTencentCaptchaCloudSecretID:  "cloud-secret-id",
			service.SettingKeyTencentCaptchaCloudSecretKey: "cloud-secret-key",
		}
	}
	cfg := &config.Config{}
	settings := service.NewSettingService(&oauthCaptchaStartSettingRepoStub{values: values}, cfg)
	verifier := &oauthCaptchaStartVerifierStub{}
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, settings, nil, nil, nil, nil, nil, nil, nil)
	authService.SetTencentCaptchaService(service.NewTencentCaptchaService(settings, verifier))
	return &AuthHandler{authService: authService, settingSvc: settings, cfg: cfg}, verifier
}

// oauthStartHandlers 返回所有必须挂闸门的登录起点（真实 handler）。
func oauthStartHandlers() map[string]func(*AuthHandler, *gin.Context) {
	return map[string]func(*AuthHandler, *gin.Context){
		"github":   func(h *AuthHandler, c *gin.Context) { h.GitHubOAuthStart(c) },
		"google":   func(h *AuthHandler, c *gin.Context) { h.GoogleOAuthStart(c) },
		"linuxdo":  func(h *AuthHandler, c *gin.Context) { h.LinuxDoOAuthStart(c) },
		"dingtalk": func(h *AuthHandler, c *gin.Context) { h.DingTalkOAuthStart(c) },
		"wechat":   func(h *AuthHandler, c *gin.Context) { h.WeChatOAuthStart(c) },
		"oidc":     func(h *AuthHandler, c *gin.Context) { h.OIDCOAuthStart(c) },
	}
}

// 验证码开启 + GET（无正文、无 proof）→ 必须 400 且无副作用。
func TestOAuthStartGetRejectedWhenCaptchaEnabledWithoutSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for provider, start := range oauthStartHandlers() {
		t.Run(provider, func(t *testing.T) {
			handler, verifier := newOAuthCaptchaStartTestHandler(true)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/"+provider+"/start?intent=bind_current_user", nil)

			start(handler, c)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), "TENCENT_CAPTCHA_VERIFICATION_FAILED")
			require.Empty(t, recorder.Header().Get("Location"), "被拒时不得给出跳转地址")
			require.Empty(t, recorder.Header().Values("Set-Cookie"), "被拒时不得落地任何 cookie")
			require.Zero(t, verifier.calls, "空 proof 不应打上游验证接口")
		})
	}
}

// 验证码开启 + POST 带 proof → 返回 authorize_url，验证器被调用一次且收到原始 proof。
func TestOAuthStartPostReturnsAuthorizeURLAfterCaptchaVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, verifier := newOAuthCaptchaStartTestHandler(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/oauth/linuxdo/start",
		strings.NewReader(`{"tencent_captcha_ticket":"ticket-value","tencent_captcha_randstr":"@rand-value"}`),
	)
	c.Request.Header.Set("Content-Type", "application/json")

	require.True(t, handler.requireActionCaptchaForOAuthLoginStart(c))
	respondOAuthStart(c, "https://provider.example/authorize")

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"authorize_url":"https://provider.example/authorize"`)
	require.Equal(t, 1, verifier.calls)
	require.Equal(t, service.TencentCaptchaProof{Ticket: "ticket-value", Randstr: "@rand-value"}, verifier.proof)
}

// 验证码开启 + POST 空 proof → fail-close。
func TestOAuthStartPostRequiresCaptchaProofWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, verifier := newOAuthCaptchaStartTestHandler(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/linuxdo/start", strings.NewReader(`{}`))
	c.Request.Header.Set("Content-Type", "application/json")

	require.False(t, handler.requireActionCaptchaForOAuthLoginStart(c))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "TENCENT_CAPTCHA_VERIFICATION_FAILED")
	require.Zero(t, verifier.calls)
}

// 绑定流程豁免：已登录用户在自己账号上做绑定，不该被匿名登录闸门拦下。
func TestOAuthBindingPathRemainsOutsideCaptchaGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, verifier := newOAuthCaptchaStartTestHandler(true)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/oidc/bind/start", nil)

	require.True(t, handler.requireActionCaptchaForOAuthLoginStart(c))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Zero(t, verifier.calls)
}

// 验证码未开启时行为完全不变：GET 仍 302 跳转，且不打验证接口。
func TestOAuthStartGetUnchangedWhenCaptchaDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, verifier := newOAuthCaptchaStartTestHandler(false)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/github/start", nil)

	require.True(t, handler.requireActionCaptchaForOAuthLoginStart(c))
	respondOAuthStart(c, "https://provider.example/authorize")

	require.Equal(t, http.StatusFound, recorder.Code)
	require.Equal(t, "https://provider.example/authorize", recorder.Header().Get("Location"))
	require.Zero(t, verifier.calls)
}

// oauthCaptchaStartSettingRepoStub 是 map 型设置仓库（只实现读取路径）。
type oauthCaptchaStartSettingRepoStub struct {
	values map[string]string
}

func (s *oauthCaptchaStartSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (s *oauthCaptchaStartSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (s *oauthCaptchaStartSettingRepoStub) Set(context.Context, string, string) error {
	return nil
}

func (s *oauthCaptchaStartSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (s *oauthCaptchaStartSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (s *oauthCaptchaStartSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(s.values))
	for key, value := range s.values {
		result[key] = value
	}
	return result, nil
}

func (s *oauthCaptchaStartSettingRepoStub) Delete(context.Context, string) error {
	return nil
}
