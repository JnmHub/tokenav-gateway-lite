package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func cnProbeAllowlistConfig(hosts ...string) *config.Config {
	return &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:       true,
				UpstreamHosts: hosts,
			},
		},
	}
}

func TestCNValidateProbeURL_AllowlistPolicy(t *testing.T) {
	cfg := cnProbeAllowlistConfig("api.moonshot.cn", "api.deepseek.com")

	ok, err := cnValidateProbeURL(cfg, "https://api.moonshot.cn/v1/users/me/balance")
	require.NoError(t, err)
	require.Equal(t, "https://api.moonshot.cn/v1/users/me/balance", ok)

	_, err = cnValidateProbeURL(cfg, "https://relay.attacker.example/v1/usages")
	require.Error(t, err)
	require.Contains(t, err.Error(), "rejected by URL security policy")

	_, err = cnValidateProbeURL(cfg, "http://169.254.169.254/latest/meta-data")
	require.Error(t, err)

	formatOnly, err := cnValidateProbeURL(&config.Config{
		Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
	}, "https://relay.attacker.example/v1/usages")
	require.NoError(t, err)
	require.Equal(t, "https://relay.attacker.example/v1/usages", formatOnly)
}

type recordingCNProbeHTTPUpstream struct{ calls int }

func (u *recordingCNProbeHTTPUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return nil, context.DeadlineExceeded
}

func (u *recordingCNProbeHTTPUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	return nil, context.DeadlineExceeded
}

type fakeCNProbeAccountRepo struct {
	AccountRepository
	account *Account
}

func (r *fakeCNProbeAccountRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	return r.account, nil
}

func TestCNProviderQuotaService_RejectsURLBlockedByPolicy(t *testing.T) {
	repo := &fakeCNProbeAccountRepo{account: &Account{
		ID: 1, Platform: PlatformKimi, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{
			"account_mode": "coding",
			"api_key":      "sk-test",
			"base_url":     "https://relay.attacker.example/api.kimi.com/coding",
		},
	}}
	upstream := &recordingCNProbeHTTPUpstream{}
	svc := NewCNProviderQuotaService(repo, nil, upstream, cnProbeAllowlistConfig("api.kimi.com"))

	_, err := svc.QueryUsage(context.Background(), 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "CN_QUOTA_URL_REJECTED")
	require.Zero(t, upstream.calls, "被安全策略拒绝时不能把 API Key 发往上游")
}

func TestCNProviderBalanceService_RejectsURLBlockedByPolicy(t *testing.T) {
	repo := &fakeCNProbeAccountRepo{account: &Account{
		ID: 2, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{
			"account_mode": "payg",
			"api_key":      "sk-test",
			"base_url":     "https://relay.attacker.example",
		},
	}}
	upstream := &recordingCNProbeHTTPUpstream{}
	svc := NewCNProviderBalanceService(repo, nil, upstream, cnProbeAllowlistConfig("api.deepseek.com"))

	_, err := svc.QueryBalance(context.Background(), 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "CN_BALANCE_URL_REJECTED")
	require.Zero(t, upstream.calls, "被安全策略拒绝时不能把 API Key 发往上游")
}

func TestCNProviderBalanceService_OfficialHostPassesValidation(t *testing.T) {
	repo := &fakeCNProbeAccountRepo{account: &Account{
		ID: 3, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{
			"account_mode": "payg",
			"api_key":      "sk-test",
		},
	}}
	upstream := &recordingCNProbeHTTPUpstream{}
	svc := NewCNProviderBalanceService(repo, nil, upstream, cnProbeAllowlistConfig("api.deepseek.com"))

	_, _ = svc.QueryBalance(context.Background(), 3)
	require.Equal(t, 1, upstream.calls, "官方主机通过安全校验后应到达上游请求层")
}
