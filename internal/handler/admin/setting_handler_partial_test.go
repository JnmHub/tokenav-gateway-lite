package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 所有读写均加锁，支持两个独立 handler/service 共用同一仓库的真实请求回归。
// 普通写方法故意报错，避免处理器绕过事务条件写却仍让用例通过。
type partialSettingsRepo struct {
	mu          sync.Mutex
	values      map[string]string
	lastUpdates map[string]string
	casCalls    int
	casError    error
	arrived     chan struct{}
	release     chan struct{}
}

func (r *partialSettingsRepo) Get(ctx context.Context, key string) (*service.Setting, error) {
	value, err := r.GetValue(ctx, key)
	return &service.Setting{Key: key, Value: value}, err
}

func (r *partialSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (r *partialSettingsRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *partialSettingsRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return clonePartialSettings(r.values), nil
}

func (*partialSettingsRepo) Set(context.Context, string, string) error {
	return fmt.Errorf("unexpected non-CAS Set")
}
func (*partialSettingsRepo) SetMultiple(context.Context, map[string]string) error {
	return fmt.Errorf("unexpected non-CAS SetMultiple")
}
func (*partialSettingsRepo) Delete(context.Context, string) error {
	return fmt.Errorf("unexpected Delete")
}

func (r *partialSettingsRepo) CompareAndSwapMultiple(ctx context.Context, key, expected, next string, updates map[string]string) (bool, error) {
	// 让两个请求均通过处理器的早期版本检查，精确覆盖读取与写入之间的竞态窗口。
	if r.arrived != nil {
		r.arrived <- struct{}{}
		select {
		case <-r.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.casCalls++
	if r.casError != nil {
		return false, r.casError
	}
	current := "0"
	if value, ok := r.values[key]; ok {
		current = value
	}
	if current != expected {
		return false, nil
	}
	for field, value := range updates {
		r.values[field] = value
	}
	r.values[key] = next
	r.lastUpdates = clonePartialSettings(updates)
	return true, nil
}

func clonePartialSettings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func nondefaultPartialSettings() map[string]string {
	// 预置所有值类型字段：一次保存只含一个字段，其余遗漏字段必须逐字节不变。
	return map[string]string{
		"registration_enabled": "true", "email_verify_enabled": "true",
		"registration_email_suffix_whitelist": `["example.com"]`, "promo_code_enabled": "false",
		"password_reset_enabled": "true", "frontend_url": "https://console.example.com",
		"invitation_code_enabled": "true", "totp_enabled": "true",
		"login_agreement_enabled": "true", "login_agreement_mode": "checkbox",
		"login_agreement_updated_at": "2026-10-11", "login_agreement_documents": `[{"id":"terms","title":"测试条款","content_md":"已保存的条款"}]`,
		"smtp_host": "smtp.example.com", "smtp_port": "465", "smtp_username": "tester",
		"smtp_password": "synthetic-mail-secret", "smtp_from": "no-reply@example.com", "smtp_from_name": "Test mail", "smtp_use_tls": "true",
		"turnstile_enabled": "true", "turnstile_site_key": "test-public-site-key", "turnstile_secret_key": "synthetic-turnstile-secret",
		"tencent_captcha_enabled": "true", "tencent_captcha_app_id": "tencent-app", "tencent_captcha_app_secret_key": "synthetic-tencent-secret",
		"tencent_captcha_cloud_secret_id": "cloud-id", "tencent_captcha_cloud_secret_key": "synthetic-cloud-secret", "tencent_captcha_region": "intl",
		"aliyun_captcha_enabled": "true", "aliyun_captcha_access_key_id": "aliyun-id", "aliyun_captcha_access_key_secret": "synthetic-aliyun-secret",
		"aliyun_captcha_scene_id": "scene", "aliyun_captcha_prefix": "prefix", "aliyun_captcha_region": "sgp",
		"api_key_acl_trust_forwarded_ip": "true", "forwarded_client_ip_headers": `["CF-Connecting-IP"]`,
		"linuxdo_connect_enabled": "true", "linuxdo_connect_client_id": "linuxdo-client", "linuxdo_connect_client_secret": "synthetic-linuxdo-secret",
		"linuxdo_connect_redirect_url": "https://console.example.com/auth/linuxdo/callback",
		"dingtalk_connect_enabled":     "true", "dingtalk_connect_client_id": "ding-client", "dingtalk_connect_client_secret": "synthetic-ding-secret",
		"dingtalk_connect_redirect_url": "https://console.example.com/auth/dingtalk/callback", "dingtalk_connect_corp_restriction_policy": "internal_only",
		"dingtalk_connect_internal_corp_id": "test-corp", "dingtalk_connect_bypass_registration": "true",
		"dingtalk_connect_sync_corp_email": "true", "dingtalk_connect_sync_display_name": "true", "dingtalk_connect_sync_dept": "true",
		"dingtalk_connect_sync_corp_email_attr_key": "corp_email", "dingtalk_connect_sync_display_name_attr_key": "corp_name", "dingtalk_connect_sync_dept_attr_key": "corp_dept",
		"dingtalk_connect_sync_corp_email_attr_name": "企业邮箱", "dingtalk_connect_sync_display_name_attr_name": "企业名称", "dingtalk_connect_sync_dept_attr_name": "企业部门",
		"wechat_connect_enabled": "true", "wechat_connect_app_id": "wechat-app", "wechat_connect_app_secret": "synthetic-wechat-secret",
		"wechat_connect_open_app_id": "wechat-open", "wechat_connect_open_app_secret": "synthetic-wechat-open-secret",
		"wechat_connect_mp_app_id": "wechat-mp", "wechat_connect_mp_app_secret": "synthetic-wechat-mp-secret",
		"wechat_connect_mobile_app_id": "wechat-mobile", "wechat_connect_mobile_app_secret": "synthetic-wechat-mobile-secret",
		"wechat_connect_open_enabled": "true", "wechat_connect_mp_enabled": "false", "wechat_connect_mobile_enabled": "false",
		"wechat_connect_mode": "open", "wechat_connect_scopes": "snsapi_login", "wechat_connect_redirect_url": "https://console.example.com/auth/wechat/callback",
		"wechat_connect_frontend_redirect_url": "/auth/wechat/callback",
		"oidc_connect_enabled":                 "true", "oidc_connect_provider_name": "Test OIDC", "oidc_connect_client_id": "oidc-client", "oidc_connect_client_secret": "synthetic-oidc-secret",
		"oidc_connect_issuer_url": "https://issuer.example.com", "oidc_connect_discovery_url": "https://issuer.example.com/.well-known/openid-configuration",
		"oidc_connect_authorize_url": "https://issuer.example.com/auth", "oidc_connect_token_url": "https://issuer.example.com/token",
		"oidc_connect_userinfo_url": "https://issuer.example.com/userinfo", "oidc_connect_jwks_url": "https://issuer.example.com/jwks",
		"oidc_connect_scopes": "openid profile email", "oidc_connect_redirect_url": "https://console.example.com/auth/oidc/callback", "oidc_connect_frontend_redirect_url": "/auth/oidc/callback",
		"oidc_connect_token_auth_method": "client_secret_post", "oidc_connect_use_pkce": "true", "oidc_connect_validate_id_token": "true",
		"oidc_connect_allowed_signing_algs": "RS256", "oidc_connect_clock_skew_seconds": "60", "oidc_connect_require_email_verified": "true",
		"oidc_connect_userinfo_email_path": "mail", "oidc_connect_userinfo_id_path": "sub", "oidc_connect_userinfo_username_path": "name",
		"github_oauth_enabled": "true", "github_oauth_client_id": "github-client", "github_oauth_client_secret": "synthetic-github-secret",
		"github_oauth_redirect_url": "https://console.example.com/auth/github/callback", "github_oauth_frontend_redirect_url": "/auth/github/callback",
		"google_oauth_enabled": "true", "google_oauth_client_id": "google-client", "google_oauth_client_secret": "synthetic-google-secret",
		"google_oauth_redirect_url": "https://console.example.com/auth/google/callback", "google_oauth_frontend_redirect_url": "/auth/google/callback",
		"site_name": "Nondefault site", "site_logo": "/logo-mark.png", "site_subtitle": "Saved subtitle", "api_base_url": "https://gateway.example.com",
		"contact_info": "Saved contact", "doc_url": "https://docs.example.com", "home_content": "Saved home", "hide_ccs_import_button": "true",
		"purchase_subscription_enabled": "true", "purchase_subscription_url": "https://console.example.com/buy", "subscription_enabled": "false",
		"table_default_page_size": "50", "table_page_size_options": "[25,50,100]", "custom_menu_items": `[{"id":"test","label":"测试","url":"https://docs.example.com","visibility":"admin"}]`,
		"custom_endpoints":    `[{"name":"测试","endpoint":"https://gateway.example.com","description":"保留"}]`,
		"default_concurrency": "17", "default_balance": "91.25000000", "default_user_rpm_limit": "123",
		"default_subscriptions": `[{"group_id":21,"validity_days":30}]`, "affiliate_rebate_rate": "7.50000000", "affiliate_rebate_freeze_hours": "12",
		"affiliate_rebate_duration_days": "90", "affiliate_rebate_per_invitee_cap": "50.00000000",
		"enable_model_fallback": "true", "fallback_model_anthropic": "saved-anthropic", "fallback_model_openai": "saved-openai",
		"fallback_model_gemini": "saved-gemini", "fallback_model_antigravity": "saved-antigravity", "enable_identity_patch": "true", "identity_patch_prompt": "Saved identity",
		"min_claude_code_version": "2.1.1", "max_claude_code_version": "3.0.0", "allow_ungrouped_key_scheduling": "true", "backend_mode_enabled": "true",
		"gateway_custom_error_message": "已保存错误", "enable_fingerprint_unification": "false", "enable_metadata_passthrough": "true", "enable_cch_signing": "true",
		"enable_claude_oauth_system_prompt_injection": "false", "claude_oauth_system_prompt": "Saved prompt", "claude_oauth_system_prompt_blocks": "",
		"enable_anthropic_cache_ttl_1h_injection": "true", "enable_client_dateline_normalization": "false", "rewrite_message_cache_control": "true",
		"antigravity_user_agent_version": "1.2.3", "openai_codex_user_agent": "saved-codex/1.2.3", "openai_codex_client_version": "0.146.0",
		"openai_codex_version_auto_sync_enabled": "false", "openai_codex_ticket_enabled": "true", "openai_codex_ticket_harvest_proxy_url": "http://test:synthetic-proxy-secret@127.0.0.1:7890",
		"openai_allow_claude_code_codex_plugin": "true", "grok_default_text_model": "grok-4.5", "grok_cross_client_model_map_enabled": "true", "grok_scheduling_threshold_percent": "85",
		"ops_monitoring_enabled": "true", "ops_realtime_monitoring_enabled": "true", "ops_query_mode_default": "raw", "ops_metrics_interval_seconds": "180",
		"channel_monitor_enabled": "true", "channel_monitor_mode": "v2", "channel_monitor_default_interval_seconds": "120", "channel_monitor_hide_throughput": "true",
		"available_channels_enabled": "true", "affiliate_enabled": "true", "risk_control_enabled": "true", "cyber_session_block_enabled": "true", "cyber_session_block_ttl_seconds": "7200", "cyber_policy_user_allowlist": "42,43",
		"balance_low_notify_enabled": "true", "balance_low_notify_threshold": "8.00000000", "balance_low_notify_recharge_url": "https://console.example.com/topup",
		"subscription_expiry_notify_enabled": "false", "account_quota_notify_enabled": "true", "account_quota_notify_emails": `[{"email":"test@example.com","remark":"测试"}]`,
		"auth_source_default_email_balance": "12.50000000", "auth_source_default_email_concurrency": "11", "auth_source_default_email_subscriptions": `[{"group_id":21,"validity_days":15}]`,
		"auth_source_default_email_grant_on_signup": "true", "auth_source_default_email_grant_on_first_bind": "true", "force_email_on_third_party_signup": "true",
		"default_platform_quotas": `{"openai":{"daily":9}}`, "auth_source_default_email_platform_quotas": `{"openai":{"monthly":29}}`,
		"allow_user_view_error_requests": "true",
		// 不属于此接口的策略、主站连接、审查自有配置必须保持逐字节不变。
		"openai_scheduler_policy_revision": "12", "gateway_lite_config": `{"enabled":true}`, "content_moderation_config": `{"enabled":true}`,
	}
}

func newPartialSettingsHandler(repo service.SettingRepository) *SettingHandler {
	return NewSettingHandler(service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}}), nil, nil, nil, nil, nil, nil)
}

func performPartialSettingsRequest(handler *SettingHandler, method, body string) *httptest.ResponseRecorder {
	router := gin.New()
	router.GET("/api/v1/admin/settings", handler.GetSettings)
	router.PUT("/api/v1/admin/settings", handler.UpdateSettings)
	router.PATCH("/api/v1/admin/settings", handler.UpdateSettings)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/api/v1/admin/settings", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func partialSettingsData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	data, ok := result.Data.(map[string]any)
	require.True(t, ok, "response=%s", rec.Body.String())
	return data
}

func TestSettingHandlerPartialUpdatesPreserveNondefaultSettings(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		for _, tab := range []struct {
			name, field string
			value       any
			stored      string
		}{
			{"General", "site_name", "Edited site", "Edited site"},
			{"Features", "channel_monitor_enabled", false, "false"},
			{"Security", "api_key_acl_trust_forwarded_ip", false, "false"},
			{"Gateway", "gateway_custom_error_message", "维护中", "维护中"},
		} {
			t.Run(method+"/"+tab.name, func(t *testing.T) {
				repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
				before := clonePartialSettings(repo.values)
				handler := newPartialSettingsHandler(repo)
				get := performPartialSettingsRequest(handler, http.MethodGet, "")
				require.Equal(t, http.StatusOK, get.Code)
				require.Equal(t, "0", partialSettingsData(t, get)["version"])
				raw, err := json.Marshal(map[string]any{"version": "0", tab.field: tab.value})
				require.NoError(t, err)
				rec := performPartialSettingsRequest(handler, method, string(raw))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				before[tab.field] = tab.stored
				before[service.SettingKeySystemSettingsVersion] = "1"
				require.Equal(t, before, repo.values, "遗漏字段必须逐字节保留")
				require.Equal(t, map[string]string{tab.field: tab.stored}, repo.lastUpdates, "数据库只写目标字段")
				data := partialSettingsData(t, rec)
				require.Equal(t, "1", data["version"])
				require.Equal(t, tab.value, data[tab.field])
				require.NotContains(t, rec.Body.String(), "synthetic-")
				require.True(t, data["smtp_password_configured"].(bool))
			})
		}
	}
}

func TestSettingHandlerPartialUpdatesExplicitZeroFalseAndEmpty(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
			rec := performPartialSettingsRequest(newPartialSettingsHandler(repo), method, `{"version":"0","default_balance":0,"default_user_rpm_limit":0,"default_subscriptions":[],"registration_enabled":false,"enable_model_fallback":false,"enable_identity_patch":false,"backend_mode_enabled":false,"allow_ungrouped_key_scheduling":false,"registration_email_suffix_whitelist":[],"forwarded_client_ip_headers":[],"min_claude_code_version":"","custom_menu_items":[],"custom_endpoints":[],"default_platform_quotas":{}}`)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			for _, field := range []string{"registration_enabled", "enable_model_fallback", "enable_identity_patch", "backend_mode_enabled", "allow_ungrouped_key_scheduling"} {
				require.Equal(t, "false", repo.values[field], field)
			}
			require.Equal(t, "0.00000000", repo.values["default_balance"])
			require.Equal(t, "0", repo.values["default_user_rpm_limit"])
			for _, field := range []string{"default_subscriptions", "registration_email_suffix_whitelist", "forwarded_client_ip_headers", "custom_menu_items", "custom_endpoints"} {
				require.Equal(t, "[]", repo.values[field], field)
			}
			require.Equal(t, "", repo.values["min_claude_code_version"])
			require.Equal(t, "{}", repo.values["default_platform_quotas"])
			require.Equal(t, "17", repo.values["default_concurrency"])
		})
	}
}

func TestSettingHandlerPartialUpdatesKeepSecretAndPaginationRules(t *testing.T) {
	repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
	rec := performPartialSettingsRequest(newPartialSettingsHandler(repo), http.MethodPut, `{"version":"0","smtp_host":"","smtp_password":"","turnstile_secret_key":"","table_default_page_size":0,"table_page_size_options":[],"default_concurrency":0}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "smtp.example.com", repo.values["smtp_host"])
	require.Equal(t, "synthetic-mail-secret", repo.values["smtp_password"])
	require.Equal(t, "synthetic-turnstile-secret", repo.values["turnstile_secret_key"])
	require.Equal(t, "50", repo.values["table_default_page_size"])
	require.Equal(t, "[10,20,50]", repo.values["table_page_size_options"])
	require.Equal(t, "1", repo.values["default_concurrency"])
	require.NotContains(t, rec.Body.String(), "synthetic-")

	rec = performPartialSettingsRequest(newPartialSettingsHandler(repo), http.MethodPatch, `{"version":"1","smtp_password":"new-synthetic-password"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "new-synthetic-password", repo.values["smtp_password"])
	require.NotContains(t, rec.Body.String(), "new-synthetic-password")
}

func TestSettingHandlerPartialUpdatesRejectInvalidPreconditions(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"site_name":"bad"}`, http.StatusPreconditionRequired},
		{`{"version":"-1","site_name":"bad"}`, http.StatusBadRequest},
		{`{"version":0,"site_name":"bad"}`, http.StatusBadRequest},
		{`{"version":null,"site_name":"bad"}`, http.StatusBadRequest},
		{`{"version":"01","site_name":"bad"}`, http.StatusBadRequest},
		{`{"version":"18446744073709551615","site_name":"bad"}`, http.StatusBadRequest},
		{`{"version":"18446744073709551616","site_name":"bad"}`, http.StatusBadRequest},
		{`{"version":"0","registration_enabled":null}`, http.StatusBadRequest},
		{`{"version":"0","default_subscriptions":null}`, http.StatusBadRequest},
		{`null`, http.StatusBadRequest},
		{`[]`, http.StatusBadRequest},
		{`{"version":"0","min_claude_code_version":"4.0.0"}`, http.StatusBadRequest},
	} {
		t.Run(tc.body, func(t *testing.T) {
			repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
			before := clonePartialSettings(repo.values)
			rec := performPartialSettingsRequest(newPartialSettingsHandler(repo), http.MethodPut, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == http.StatusPreconditionRequired {
				require.Contains(t, rec.Body.String(), "SETTINGS_VERSION_REQUIRED")
			} else if strings.Contains(tc.body, `"version"`) && !strings.Contains(tc.body, `"version":"0"`) {
				require.Contains(t, rec.Body.String(), "SETTINGS_VERSION_INVALID")
			}
			require.Equal(t, before, repo.values)
			require.Equal(t, 0, repo.casCalls)
		})
	}
}

func TestSettingHandlerPartialUpdatesRejectStaleAdministratorSnapshot(t *testing.T) {
	repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
	first := newPartialSettingsHandler(repo)
	second := newPartialSettingsHandler(repo)
	stale := partialSettingsData(t, performPartialSettingsRequest(second, http.MethodGet, ""))
	rec := performPartialSettingsRequest(first, http.MethodPut, `{"version":"0","site_name":"first administrator"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	before := clonePartialSettings(repo.values)
	stale["gateway_custom_error_message"] = "second administrator"
	raw, err := json.Marshal(stale)
	require.NoError(t, err)
	rec = performPartialSettingsRequest(second, http.MethodPut, string(raw))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "SETTINGS_VERSION_CONFLICT")
	require.Equal(t, before, repo.values)
	rec = performPartialSettingsRequest(second, http.MethodPatch, `{"version":"1","gateway_custom_error_message":"second administrator"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "first administrator", repo.values["site_name"])
	require.Equal(t, "second administrator", repo.values["gateway_custom_error_message"])
	require.Equal(t, "2", partialSettingsData(t, rec)["version"])
}

func TestSettingHandlerPartialUpdatesConcurrentCAS(t *testing.T) {
	repo := &partialSettingsRepo{values: nondefaultPartialSettings(), arrived: make(chan struct{}, 2), release: make(chan struct{})}
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, name := range []string{"administrator-a", "administrator-b"} {
		handler := newPartialSettingsHandler(repo)
		go func(name string) {
			results <- performPartialSettingsRequest(handler, http.MethodPut, fmt.Sprintf(`{"version":"0","site_name":%q}`, name))
		}(name)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(repo.release) }) }
	t.Cleanup(release)
	for range 2 {
		select {
		case <-repo.arrived:
		case rec := <-results:
			t.Fatalf("request returned before CAS: status=%d", rec.Code)
		case <-time.After(5 * time.Second):
			t.Fatal("both requests must reach CAS within the test deadline")
		}
	}
	release()
	codes := map[int]int{}
	for range 2 {
		select {
		case rec := <-results:
			codes[rec.Code]++
		case <-time.After(5 * time.Second):
			t.Fatal("CAS requests did not finish within the test deadline")
		}
	}
	require.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: 1}, codes)
	require.Equal(t, 2, repo.casCalls, "两次都进入仓库 CAS，拒绝读后写竞态")
	require.Equal(t, "1", repo.values[service.SettingKeySystemSettingsVersion])
	require.Contains(t, []string{"administrator-a", "administrator-b"}, repo.values["site_name"])
	require.Equal(t, "17", repo.values["default_concurrency"])
}

func TestSettingHandlerPartialUpdatesCASFailureAndInvalidFastPolicyWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		failure    error
		code       int
	}{
		{"repository failure", `{"version":"0","site_name":"bad"}`, fmt.Errorf("synthetic transaction failure"), http.StatusInternalServerError},
		{"invalid Fast policy", `{"version":"0","site_name":"bad","openai_fast_policy_settings":{"version":1,"priority_detection_by_request_enabled":false,"priority_detection_by_response_enabled":false,"rules":[]}}`, nil, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &partialSettingsRepo{values: nondefaultPartialSettings(), casError: tc.failure}
			before := clonePartialSettings(repo.values)
			rec := performPartialSettingsRequest(newPartialSettingsHandler(repo), http.MethodPut, tc.body)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			require.Equal(t, before, repo.values)
		})
	}
}

// 只暴露基础仓库接口，故意不提供 CAS 扩展以验证禁止普通写降级。
type partialSettingsWithoutCAS struct{ service.SettingRepository }

func TestSettingHandlerPartialUpdatesRejectRepositoryWithoutCAS(t *testing.T) {
	repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
	before := clonePartialSettings(repo.values)
	rec := performPartialSettingsRequest(newPartialSettingsHandler(partialSettingsWithoutCAS{repo}), http.MethodPatch, `{"version":"0","site_name":"not persisted"}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Equal(t, before, repo.values)
	require.Equal(t, 0, repo.casCalls)
}

func TestSettingHandlerPartialUpdatesLoginAgreementEmpty(t *testing.T) {
	repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
	handler := newPartialSettingsHandler(repo)
	rec := performPartialSettingsRequest(handler, http.MethodPut, `{"version":"0","login_agreement_documents":[]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "启用条款不能清空文档")
	require.Equal(t, 0, repo.casCalls)
	rec = performPartialSettingsRequest(handler, http.MethodPatch, `{"version":"0","login_agreement_enabled":false,"login_agreement_documents":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "[]", repo.values["login_agreement_documents"])
	require.Empty(t, partialSettingsData(t, rec)["login_agreement_documents"])
}

func TestSettingHandlerPartialUpdatesAuditOnlySubmittedSecret(t *testing.T) {
	var audit bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&audit, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
	handler := newPartialSettingsHandler(repo)
	rec := performPartialSettingsRequest(handler, http.MethodPut, `{"version":"0","site_name":"audit site"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	var record struct {
		Changed []string `json:"changed"`
	}
	require.NoError(t, json.Unmarshal(audit.Bytes(), &record))
	require.Equal(t, []string{"site_name"}, record.Changed, "合并或归一化的遗漏字段不能产生伪变更审计")
	audit.Reset()
	rec = performPartialSettingsRequest(handler, http.MethodPatch, `{"version":"1","smtp_password":"new-synthetic-password"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(audit.Bytes(), &record))
	require.Equal(t, []string{"smtp_password"}, record.Changed)
	require.NotContains(t, audit.String(), "new-synthetic-password")
	require.NotContains(t, rec.Body.String(), "new-synthetic-password")
}

func TestSettingHandlerPartialUpdatesSnapshotAvoidsStaleFastPolicyCache(t *testing.T) {
	repo := &partialSettingsRepo{values: nondefaultPartialSettings()}
	handler := newPartialSettingsHandler(repo)
	_, err := handler.settingService.GetOpenAIFastPolicySettings(context.Background())
	require.NoError(t, err)
	// 模拟另一实例已写入策略；该 handler 的热路径缓存仍是默认，管理 GET 必须读同版数据。
	repo.values[service.SettingKeyOpenAIFastPolicySettings] = `{"version":1,"priority_detection_by_request_enabled":false,"priority_detection_by_response_enabled":true,"rules":[]}`
	repo.values[service.SettingKeySystemSettingsVersion] = "2"
	rec := performPartialSettingsRequest(handler, http.MethodGet, "")
	data := partialSettingsData(t, rec)
	require.Equal(t, "2", data["version"])
	require.Equal(t, false, data["openai_fast_policy_settings"].(map[string]any)["priority_detection_by_request_enabled"])
}

func TestMergeOmittedSettingsCoversAllValueFields(t *testing.T) {
	// 验证遗漏字段不会通过 request DTO 类型差异落回零值；行为回归以上面真实路由为准。
	current := &service.SystemSettings{DefaultSubscriptions: []service.DefaultSubscriptionSetting{{GroupID: 21, ValidityDays: 30}}}
	request := UpdateSettingsRequest{}
	require.NoError(t, mergeOmittedSettings(&request, map[string]bool{}, current))
	raw, err := json.Marshal(request.DefaultSubscriptions)
	require.NoError(t, err)
	require.True(t, bytes.Equal(raw, []byte(`[{"group_id":21,"validity_days":30}]`)))
}
