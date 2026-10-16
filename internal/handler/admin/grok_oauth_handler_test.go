//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type grokSSOImportOAuthClient struct{}

func (grokSSOImportOAuthClient) ExchangeCode(context.Context, string, string, string, string, string) (*xai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (grokSSOImportOAuthClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (grokSSOImportOAuthClient) ConvertSSOToBuild(_ context.Context, token, _ string) (*xai.TokenResponse, error) {
	if token == "bad-sso" {
		return nil, errors.New("invalid sso token")
	}
	return &xai.TokenResponse{
		AccessToken:  "access-" + token,
		RefreshToken: "refresh-" + token,
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	}, nil
}

type grokSSOImportProbeRepo struct {
	service.AccountRepository
	mu             sync.Mutex
	accounts       map[int64]*service.Account
	createdIDs     []int64
	probeCompleted chan int64
}

func (r *grokSSOImportProbeRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return nil, service.ErrAccountNotFound
	}
	return account, nil
}

func (r *grokSSOImportProbeRepo) UpdateExtra(_ context.Context, id int64, _ map[string]any) error {
	r.probeCompleted <- id
	return nil
}

func (r *grokSSOImportProbeRepo) snapshotCreatedIDs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.createdIDs...)
}

type grokSSOImportProbeAdminService struct {
	*stubAdminService
	repo   *grokSSOImportProbeRepo
	nextID int64
}

func (s *grokSSOImportProbeAdminService) CreateAccount(_ context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	s.repo.mu.Lock()
	s.nextID++
	account := &service.Account{
		ID:          s.nextID,
		Name:        input.Name,
		Platform:    input.Platform,
		Type:        input.Type,
		Status:      service.StatusActive,
		Concurrency: input.Concurrency,
		Credentials: input.Credentials,
	}
	s.repo.accounts[account.ID] = account
	s.repo.createdIDs = append(s.repo.createdIDs, account.ID)
	s.repo.mu.Unlock()
	return account, nil
}

type grokSSOImportProbeUpstream struct {
	mu    sync.Mutex
	calls map[int64]int
}

func (u *grokSSOImportProbeUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls[accountID]++
	u.mu.Unlock()
	body := `{"config":{"monthlyLimit":{"val":15000},"used":{"val":1500},"billingPeriodStart":"2026-08-01T00:00:00Z","billingPeriodEnd":"2026-09-01T00:00:00Z"}}`
	if req.URL.RawQuery == "format=credits" {
		body = `{"config":{"currentPeriod":{"type":"WEEKLY","start":"2026-08-03T00:00:00Z","end":"2026-08-10T00:00:00Z"},"creditUsagePercent":12.5}}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func (u *grokSSOImportProbeUpstream) DoWithTLS(
	req *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *grokSSOImportProbeUpstream) callCount(accountID int64) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[accountID]
}

type grokQuotaHandlerAccountRepo struct {
	service.AccountRepository
	account *service.Account
	updates map[int64]map[string]any
}

func (r *grokQuotaHandlerAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	if r.account != nil && r.account.ID == id {
		return r.account, nil
	}
	return nil, service.ErrAccountNotFound
}

func (r *grokQuotaHandlerAccountRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	if r.updates == nil {
		r.updates = make(map[int64]map[string]any)
	}
	r.updates[id] = updates
	return nil
}

type grokQuotaHandlerUpstream struct {
	resp     *http.Response
	lastReq  *http.Request
	lastBody []byte
}

func (u *grokQuotaHandlerUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.lastReq = req
	if req.Body != nil {
		u.lastBody, _ = io.ReadAll(req.Body)
	}
	return u.resp, nil
}

func (u *grokQuotaHandlerUpstream) DoWithTLS(
	req *http.Request,
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	_ *tlsfingerprint.Profile,
) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGrokOAuthHandlerQueryQuotaProbesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &grokQuotaHandlerAccountRepo{account: &service.Account{
		ID:          42,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}}
	upstream := &grokQuotaHandlerUpstream{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"X-Ratelimit-Limit-Requests":     []string{"10"},
			"X-Ratelimit-Remaining-Requests": []string{"8"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_probe"}`)),
	}}
	quotaService := service.NewGrokQuotaService(repo, nil, service.NewGrokTokenProvider(repo, nil), upstream, nil)
	handler := NewGrokOAuthHandler(nil, nil, quotaService)

	router := gin.New()
	router.GET("/api/v1/admin/grok/accounts/:id/quota", handler.QueryQuota)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/grok/accounts/42/quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"source":"active_probe"`)
	require.Contains(t, rec.Body.String(), `"headers_observed":true`)
	require.NotContains(t, rec.Body.String(), "access-token")
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, string(upstream.lastBody), `"store":false`)
	require.NotNil(t, repo.updates[42])
}

func TestGrokOAuthHandlerResetQuotaReturnsUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)

	repo := &grokQuotaHandlerAccountRepo{account: &service.Account{
		ID:       43,
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
	}}
	quotaService := service.NewGrokQuotaService(repo, nil, nil, nil, nil)
	handler := NewGrokOAuthHandler(nil, nil, quotaService)

	router := gin.New()
	router.POST("/api/v1/admin/grok/accounts/:id/reset-quota", handler.ResetQuota)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/accounts/43/reset-quota", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	require.Contains(t, rec.Body.String(), `"reason":"GROK_QUOTA_RESET_UNSUPPORTED"`)
	require.NotContains(t, rec.Body.String(), "access-token")
}

func TestGrokSSOImportCredentialsPreservesOperatorRoutingOptions(t *testing.T) {
	built := map[string]any{
		"access_token": "server-token",
		"base_url":     xai.DefaultCLIBaseURL,
	}
	requested := map[string]any{
		"access_token":            "attacker-token",
		"base_url":                xai.DefaultUSWest2BaseURL,
		"header_override_enabled": true,
		service.HeaderOverridesCredentialKey: map[string]any{
			"x-tenant": "tenant-a",
		},
	}

	credentials := grokSSOImportCredentials(built, requested)
	require.Equal(t, "server-token", credentials["access_token"])
	require.Equal(t, xai.DefaultUSWest2BaseURL, credentials["base_url"])
	require.Equal(t, true, credentials["header_override_enabled"])
	require.Equal(t, map[string]any{"x-tenant": "tenant-a"}, credentials[service.HeaderOverridesCredentialKey])
}

func TestNormalizeSSOImportTokensAcceptsCookieAndDeduplicates(t *testing.T) {
	require.Equal(t, []string{"one", "two", "three"}, normalizeSSOImportTokens(
		[]string{"sso=one; other=value", "two\nthree", "one"},
		"",
	))
}

func TestGrokOAuthHandlerSSOPartialFailureKeepsSuccessfulAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	oauthSvc := service.NewGrokOAuthService(nil, grokSSOImportOAuthClient{})
	handler := NewGrokOAuthHandler(oauthSvc, adminSvc, nil)
	router := gin.New()
	router.POST("/api/v1/admin/grok/sso-to-oauth", handler.CreateAccountsFromSSO)

	payload := map[string]any{
		"sso_tokens": []string{"good-one", "bad-sso", "good-two"},
		"name":       "regional",
		"credentials": map[string]any{
			"base_url": xai.DefaultUSWest2BaseURL,
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/sso-to-oauth", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var responseBody struct {
		Data GrokSSOToOAuthResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &responseBody))
	require.Len(t, responseBody.Data.Created, 2)
	require.Len(t, responseBody.Data.Failed, 1)
	require.Equal(t, 2, responseBody.Data.Failed[0].Index)
	require.Len(t, adminSvc.createdAccounts, 2)
	for _, input := range adminSvc.createdAccounts {
		require.Equal(t, xai.DefaultUSWest2BaseURL, input.Credentials["base_url"])
	}
}

func TestGrokOAuthHandlerSSOImportSchedulesProbeForEveryCreatedAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &grokSSOImportProbeRepo{
		accounts:       make(map[int64]*service.Account),
		probeCompleted: make(chan int64, 2),
	}
	adminSvc := &grokSSOImportProbeAdminService{
		stubAdminService: newStubAdminService(),
		repo:             repo,
		nextID:           910000,
	}
	upstream := &grokSSOImportProbeUpstream{calls: make(map[int64]int)}
	quotaService := service.NewGrokQuotaService(
		repo,
		nil,
		service.NewGrokTokenProvider(repo, nil),
		upstream,
		nil,
	)
	oauthSvc := service.NewGrokOAuthService(nil, grokSSOImportOAuthClient{})
	defer oauthSvc.Stop()
	handler := NewGrokOAuthHandler(oauthSvc, adminSvc, quotaService)
	router := gin.New()
	router.POST("/api/v1/admin/grok/sso-to-oauth", handler.CreateAccountsFromSSO)

	body, err := json.Marshal(map[string]any{
		"sso_tokens": []string{"good-one", "bad-sso", "good-two"},
		"name":       "probe-import",
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/sso-to-oauth", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var responseBody struct {
		Data GrokSSOToOAuthResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &responseBody))
	require.Len(t, responseBody.Data.Created, 2)
	require.Len(t, responseBody.Data.Failed, 1)

	createdIDs := repo.snapshotCreatedIDs()
	require.Len(t, createdIDs, 2)
	completedIDs := make([]int64, 0, len(createdIDs))
	for range createdIDs {
		select {
		case accountID := <-repo.probeCompleted:
			completedIDs = append(completedIDs, accountID)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Grok SSO import quota probe")
		}
	}
	require.ElementsMatch(t, createdIDs, completedIDs)
	for _, accountID := range createdIDs {
		require.Equal(t, 2, upstream.callCount(accountID), "each account should receive one weekly and one monthly billing probe")
	}
	require.Eventually(t, func() bool {
		defaultGrokImportProbeScheduler.mu.Lock()
		defer defaultGrokImportProbeScheduler.mu.Unlock()
		for _, accountID := range createdIDs {
			if _, pending := defaultGrokImportProbeScheduler.pending[accountID]; pending {
				return false
			}
			if _, inFlight := defaultGrokImportProbeScheduler.inFlight[accountID]; inFlight {
				return false
			}
		}
		return true
	}, time.Second, time.Millisecond, "scheduled probes should leave no pending or in-flight state")
}

func TestGrokOAuthHandlerRuntimeSanityDoesNotExposeSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(xai.EnvBaseURL, "http://127.0.0.1:8080/v1?access_token=secret")
	t.Setenv(xai.EnvClientID, "client-secret-like-value")

	handler := NewGrokOAuthHandler(nil, nil, nil)
	router := gin.New()
	router.GET("/api/v1/admin/grok/runtime-sanity", handler.RuntimeSanity)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/grok/runtime-sanity", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"public_gateway_scope":"responses_only"`)
	require.Contains(t, rec.Body.String(), `"valid":false`)
	require.NotContains(t, rec.Body.String(), "access_token")
	require.NotContains(t, rec.Body.String(), "secret")
	require.NotContains(t, rec.Body.String(), "client-secret-like-value")
}
