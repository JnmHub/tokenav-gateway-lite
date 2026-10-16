//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type alphaSearchFailoverAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r alphaSearchFailoverAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

func (r alphaSearchFailoverAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r alphaSearchFailoverAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r alphaSearchFailoverAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	return r.accountsForPlatform(platform), nil
}

func (r alphaSearchFailoverAccountRepo) accountsForPlatform(platform string) []service.Account {
	out := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out
}

type alphaSearchFailoverHTTPUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	failAll    bool
}

type alphaSearchPATHTTPUpstream struct {
	service.HTTPUpstream
	mu       sync.Mutex
	requests []*http.Request
}

func (u *alphaSearchPATHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.requests = append(u.requests, req.Clone(req.Context()))
	u.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"X-Request-Id": []string{"req-pat-search"},
		},
		Body: io.NopCloser(strings.NewReader(
			"event: response.output_text.delta\n" +
				`data: {"type":"response.output_text.delta","delta":"PAT result"}` + "\n\n" +
				"event: response.completed\n" +
				`data: {"type":"response.completed","response":{"output":[]}}` + "\n\n",
		)),
	}, nil
}

func (u *alphaSearchPATHTTPUpstream) snapshot() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.requests...)
}

type alphaSearchPATUsageRepo struct {
	service.UsageLogRepository
	mu     sync.Mutex
	logs   []*service.UsageLog
	metas  []gatewaylite.RequestMeta
	metaOK []bool
}

func (r *alphaSearchPATUsageRepo) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copyLog := *log
	r.logs = append(r.logs, &copyLog)
	meta, ok := gatewaylite.RequestMetaFromContext(ctx)
	r.metas = append(r.metas, meta)
	r.metaOK = append(r.metaOK, ok)
	return true, nil
}

func (r *alphaSearchPATUsageRepo) snapshot() []*service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...)
}

func (r *alphaSearchPATUsageRepo) snapshotMeta() ([]gatewaylite.RequestMeta, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]gatewaylite.RequestMeta(nil), r.metas...), append([]bool(nil), r.metaOK...)
}

func (u *alphaSearchFailoverHTTPUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	if accountID == 1 || u.failAll {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"alpha search endpoint not found"}}`)),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"search-final-success"},
		},
		Body: io.NopCloser(strings.NewReader(`{"encrypted_output":"ciphertext","output":"ok"}`)),
	}, nil
}

func TestOpenAIGatewayHandlerAlphaSearchHonorsMaximumSwitches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4150)
	accounts := []service.Account{
		{ID: 1, Name: "search-1", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "sk-1"}},
		{ID: 2, Name: "search-2", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "sk-2"}},
		{ID: 3, Name: "search-3", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 2, GroupIDs: []int64{groupID}, Credentials: map[string]any{"api_key": "sk-3"}},
	}
	usageRepo := &alphaSearchPATUsageRepo{}
	upstream := &alphaSearchFailoverHTTPUpstream{failAll: true}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		alphaSearchFailoverAccountRepo{accounts: accounts}, usageRepo, nil, nil, nil, nil, nil,
		cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billingService,
		upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil,
	)
	handler := NewOpenAIGatewayHandler(
		gatewayService, service.NewConcurrencyService(nil), billingService,
		&service.APIKeyService{}, nil, nil, nil, nil, nil, cfg,
	)
	handler.maxAccountSwitches = 1

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"id":"max-switch","model":"gpt-5.6-sol"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 4151, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
		User:  &service.User{ID: 4152},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 4152})

	handler.AlphaSearch(c)

	require.Equal(t, []int64{1, 2}, upstream.calls(), "maximum switches=1 means initial attempt plus one account switch")
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Empty(t, usageRepo.snapshot(), "exhausted failures cannot create usage or per-request charges")
}

func (u *alphaSearchFailoverHTTPUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestOpenAIGatewayHandlerAlphaSearchFailsOverToSecondAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4100)
	accounts := []service.Account{
		{
			ID: 1, Name: "search-account-1", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Concurrency: 0, Priority: 0, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": "sk-first"},
		},
		{
			ID: 2, Name: "search-account-2", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Concurrency: 0, Priority: 1, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": "sk-second"},
		},
	}
	accountRepo := alphaSearchFailoverAccountRepo{accounts: accounts}
	usageRepo := &alphaSearchPATUsageRepo{}
	upstream := &alphaSearchFailoverHTTPUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	billingService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingService.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingService,
		upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil,
	)
	concurrencyService := service.NewConcurrencyService(nil)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, nil, cfg,
	)
	handler.maxAccountSwitches = 10

	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/alpha/search",
		strings.NewReader(`{"id":"search-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(gatewaylite.ContextWithRequestMeta(req.Context(), gatewaylite.RequestMeta{
		RequestID: "gateway-request-search-1",
		KeyID:     "gateway-key-search",
		UserID:    100,
		LeaseID:   "lease-search-1",
		Region:    "test",
	}))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 99, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
		User:  &service.User{ID: 100, Status: service.StatusActive, Balance: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})

	handler.AlphaSearch(c)

	require.Equal(t, []int64{1, 2}, upstream.calls())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"encrypted_output":"ciphertext","output":"ok"}`, rec.Body.String())
	logs := usageRepo.snapshot()
	require.Len(t, logs, 1, "404 失败账号不能产生 usage 或第二次按次费用")
	require.Equal(t, int64(2), logs[0].AccountID)
	require.Equal(t, "gateway-request-search-1", logs[0].RequestID)
	require.NotNil(t, logs[0].BillingMode)
	require.Equal(t, string(service.BillingModePerRequest), *logs[0].BillingMode)
	metas, metaOK := usageRepo.snapshotMeta()
	require.Equal(t, []bool{true}, metaOK)
	require.Len(t, metas, 1)
	require.Equal(t, "lease-search-1", metas[0].LeaseID, "最终单条 usage 必须保留同一额度租约事实")
}

func TestOpenAIGatewayHandlerAlphaSearchPATFallbackRecordsSingleResponsesUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(4200)
	account := service.Account{
		ID: 4201, Name: "pat-search", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
		Concurrency: 0, Priority: 0, GroupIDs: []int64{groupID},
		Credentials: map[string]any{
			"access_token":       "at-test-token",
			"auth_mode":          service.OpenAIAuthModePersonalAccessToken,
			"chatgpt_account_id": "chatgpt-account",
		},
	}
	accountRepo := alphaSearchFailoverAccountRepo{accounts: []service.Account{account}}
	usageRepo := &alphaSearchPATUsageRepo{}
	upstream := &alphaSearchPATHTTPUpstream{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	billingCacheService := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo, usageRepo, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCacheService, upstream,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil,
	)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		service.NewConcurrencyService(nil),
		billingCacheService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil, nil, nil, nil, nil, cfg,
	)

	user := &service.User{ID: 4202, Status: service.StatusActive, Balance: 100}
	group := &service.Group{
		ID: groupID, Name: "pat-openai", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, RateMultiplier: 1, Hydrated: true,
	}
	apiKey := &service.APIKey{
		ID: 4203, UserID: user.ID, GroupID: &groupID, Group: group, User: user,
		Status: service.StatusActive, GatewayStrategy: "fixed_group", KeyDefaultGatewayNodeID: 1,
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/alpha/search",
		strings.NewReader(`{"id":"pat-session","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"latest news"}]}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(gatewaylite.ContextWithRequestMeta(req.Context(), gatewaylite.RequestMeta{
		RequestID: "gateway-request-pat-1",
		KeyID:     "gateway-key-pat",
		UserID:    user.ID,
		LeaseID:   "lease-pat-1",
		Region:    "test",
	}))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID, Concurrency: 0})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"output":"PAT result"}`, rec.Body.String())
	requests := upstream.snapshot()
	require.Len(t, requests, 1)
	require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", requests[0].URL.String())
	logs := usageRepo.snapshot()
	require.Len(t, logs, 1, "PAT Responses 回退只能生成一条用量事实")
	require.Equal(t, int64(4201), logs[0].AccountID)
	require.Equal(t, "gateway-request-pat-1", logs[0].RequestID)
	require.NotNil(t, logs[0].UpstreamEndpoint)
	require.Equal(t, "/v1/responses", *logs[0].UpstreamEndpoint)
	require.NotNil(t, logs[0].BillingMode)
	require.Equal(t, string(service.BillingModePerRequest), *logs[0].BillingMode)
}
