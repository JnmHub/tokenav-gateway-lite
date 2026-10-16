//go:build unit && real

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type alphaSearchControlledThenRealUpstream struct {
	service.HTTPUpstream
	mu         sync.Mutex
	accountIDs []int64
	client     *http.Client
}

func (u *alphaSearchControlledThenRealUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	if accountID == 4901 {
		return &http.Response{
			StatusCode: http.StatusMethodNotAllowed,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"controlled endpoint does not support alpha search"}}`)),
		}, nil
	}
	return u.client.Do(req)
}

func (u *alphaSearchControlledThenRealUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

func TestRealOpenAIGatewayHandlerAlphaSearchControlled405ThenRealAPIKey(t *testing.T) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("REAL_OPENAI_BASE_URL")), "/")
	apiKeyValue := strings.TrimSpace(os.Getenv("REAL_OPENAI_API_KEY"))
	if baseURL == "" || apiKeyValue == "" {
		t.Fatal("REAL_OPENAI_BASE_URL and REAL_OPENAI_API_KEY are required for the real-tag test")
	}

	gin.SetMode(gin.TestMode)
	groupID := int64(4900)
	accounts := []service.Account{
		{
			ID: 4901, Name: "controlled-405", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Priority: 0, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": "controlled-placeholder", "base_url": "https://controlled.invalid/v1"},
		},
		{
			ID: 4902, Name: "real-api-key", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Priority: 1, GroupIDs: []int64{groupID},
			Credentials: map[string]any{"api_key": apiKeyValue, "base_url": baseURL},
		},
	}
	usageRepo := &alphaSearchPATUsageRepo{}
	upstream := &alphaSearchControlledThenRealUpstream{client: &http.Client{Timeout: 30 * time.Second}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true // 测试资产明确是 HTTP；生产默认仍要求 HTTPS。
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
	handler.maxAccountSwitches = 3

	requestBody := `{"id":"real-alpha-failover","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"OpenAI"}]}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(gatewaylite.ContextWithRequestMeta(req.Context(), gatewaylite.RequestMeta{
		RequestID: "real-alpha-request-1", KeyID: "real-alpha-key", UserID: 4903,
		LeaseID: "real-alpha-lease-1", Region: "local-real-test",
	}))
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	user := &service.User{ID: 4903, Status: service.StatusActive, Balance: 100}
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 4904, UserID: user.ID, GroupID: &groupID, User: user,
		Group:  &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true},
		Status: service.StatusActive, GatewayStrategy: "fixed_group", KeyDefaultGatewayNodeID: 1,
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})

	handler.AlphaSearch(c)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, rec.Body.Bytes())
	require.Equal(t, []int64{4901, 4902}, upstream.calls())
	logs := usageRepo.snapshot()
	require.Len(t, logs, 1)
	require.Equal(t, int64(4902), logs[0].AccountID)
	require.Equal(t, "real-alpha-request-1", logs[0].RequestID)
	require.NotNil(t, logs[0].BillingMode)
	require.Equal(t, string(service.BillingModePerRequest), *logs[0].BillingMode)
	metas, ok := usageRepo.snapshotMeta()
	require.Equal(t, []bool{true}, ok)
	require.Len(t, metas, 1)
	require.Equal(t, "real-alpha-lease-1", metas[0].LeaseID)
}

var _ service.HTTPUpstream = (*alphaSearchControlledThenRealUpstream)(nil)
