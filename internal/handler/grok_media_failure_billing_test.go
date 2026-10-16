package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type grokMediaFailureAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r *grokMediaFailureAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform != platform {
			continue
		}
		for _, candidateGroupID := range account.GroupIDs {
			if candidateGroupID == groupID {
				accounts = append(accounts, account)
				break
			}
		}
	}
	return accounts, nil
}

func (r *grokMediaFailureAccountRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *grokMediaFailureAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]service.Account, error) {
	accounts := make([]service.Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform && len(account.GroupIDs) == 0 {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *grokMediaFailureAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	for _, account := range r.accounts {
		if account.ID == id {
			copyAccount := account
			return &copyAccount, nil
		}
	}
	return nil, service.ErrNoAvailableAccounts
}

type grokMediaFailureUserRepo struct {
	service.UserRepository
	user service.User
}

func (r *grokMediaFailureUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	copyUser := r.user
	copyUser.ID = id
	return &copyUser, nil
}

type grokMediaFailureHTTPUpstream struct {
	mu           sync.Mutex
	calls        int
	status       int
	err          error
	beforeReturn func()
}

func (u *grokMediaFailureHTTPUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.calls++
	beforeReturn := u.beforeReturn
	status := u.status
	err := u.err
	u.mu.Unlock()
	if beforeReturn != nil {
		beforeReturn()
	}
	if err != nil {
		return nil, err
	}
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"upstream failed"}}`)),
	}, nil
}

func (u *grokMediaFailureHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *grokMediaFailureHTTPUpstream) setBeforeReturn(fn func()) {
	u.mu.Lock()
	u.beforeReturn = fn
	u.mu.Unlock()
}

func (u *grokMediaFailureHTTPUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func newGrokMediaFailureBillingHandler(
	t *testing.T,
	accounts []service.Account,
	upstream *grokMediaFailureHTTPUpstream,
) (*OpenAIGatewayHandler, *grokGatewayLiteE2EUsageLogRepo, *grokGatewayLiteE2EBillingRepo) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1
	cfg.Gateway.MaxAccountSwitches = 1
	cfg.Security.URLAllowlist.Enabled = false

	userRepo := &grokMediaFailureUserRepo{user: service.User{ID: 902, Balance: 100}}
	usageRepo := &grokGatewayLiteE2EUsageLogRepo{}
	billingRepo := &grokGatewayLiteE2EBillingRepo{}
	accountRepo := &grokMediaFailureAccountRepo{accounts: accounts}
	concurrencyCache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	concurrencyService := service.NewConcurrencyService(concurrencyCache)
	billingService := service.NewBillingService(cfg, nil)
	billingCacheService := service.NewBillingCacheService(nil, userRepo, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheService.Stop)
	gatewayService := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		billingRepo,
		userRepo,
		nil,
		nil,
		nil,
		cfg,
		nil,
		concurrencyService,
		billingService,
		nil,
		billingCacheService,
		upstream,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	handler := NewOpenAIGatewayHandler(
		gatewayService,
		concurrencyService,
		billingCacheService,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)
	return handler, usageRepo, billingRepo
}

func TestGrokMediaFailuresRemainZeroCostAndDoNotDuplicateBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID = int64(9)
	account := service.Account{
		ID:          451,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"api_key":               "test-upstream-key",
			"base_url":              "https://api.x.ai/v1",
			"pool_mode":             true,
			"pool_mode_retry_count": 0,
			"model_mapping": map[string]any{
				"grok-imagine-image": "grok-imagine-image",
			},
		},
	}

	tests := []struct {
		name             string
		accounts         []service.Account
		body             string
		video            bool
		upstreamStatus   int
		upstreamErr      error
		cancelDuringDo   bool
		wantStatuses     []int
		wantMinimumCalls int
	}{
		{
			name:             "upstream failure",
			accounts:         []service.Account{account},
			body:             `{"model":"grok-imagine-image","prompt":"draw"}`,
			upstreamStatus:   http.StatusServiceUnavailable,
			wantStatuses:     []int{http.StatusBadGateway, http.StatusServiceUnavailable},
			wantMinimumCalls: 1,
		},
		{
			name:         "unsupported model",
			accounts:     []service.Account{account},
			body:         `{"model":"grok-4.5","prompt":"video"}`,
			video:        true,
			wantStatuses: []int{http.StatusBadRequest},
		},
		{
			name:         "group has no account",
			body:         `{"model":"grok-imagine-image","prompt":"draw"}`,
			wantStatuses: []int{http.StatusServiceUnavailable},
		},
		{
			name:             "client cancellation",
			accounts:         []service.Account{account},
			body:             `{"model":"grok-imagine-image","prompt":"draw"}`,
			upstreamErr:      context.Canceled,
			cancelDuringDo:   true,
			wantMinimumCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &grokMediaFailureHTTPUpstream{status: tt.upstreamStatus, err: tt.upstreamErr}
			handler, usageRepo, billingRepo := newGrokMediaFailureBillingHandler(t, tt.accounts, upstream)

			for attempt := 0; attempt < 2; attempt++ {
				requestCtx := context.Background()
				if tt.cancelDuringDo {
					var cancel context.CancelFunc
					requestCtx, cancel = context.WithCancel(requestCtx)
					upstream.setBeforeReturn(cancel)
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				path := "/v1/images/generations"
				if tt.video {
					path = "/v1/videos/generations"
				}
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(tt.body)).WithContext(requestCtx)
				c.Request.Header.Set("Content-Type", "application/json")
				user := &service.User{ID: 902, Balance: 100}
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
					ID:      901,
					UserID:  user.ID,
					User:    user,
					GroupID: handlerInt64Ptr(groupID),
					Group: &service.Group{
						ID:                   groupID,
						Name:                 "Grok",
						Platform:             service.PlatformGrok,
						Status:               service.StatusActive,
						Hydrated:             true,
						RateMultiplier:       1,
						AllowImageGeneration: true,
					},
				})
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID, Concurrency: 1})
				if tt.video {
					handler.GrokVideoGeneration(c)
				} else {
					handler.GrokImages(c)
				}
				if len(tt.wantStatuses) > 0 {
					require.Contains(t, tt.wantStatuses, recorder.Code, recorder.Body.String())
				}
			}

			require.GreaterOrEqual(t, upstream.callCount(), tt.wantMinimumCalls)
			require.Never(t, func() bool {
				return len(usageRepo.snapshot()) > 0 || len(billingRepo.snapshot()) > 0
			}, 150*time.Millisecond, 10*time.Millisecond, "失败请求和重复观察都不得产生用量收费")
		})
	}
}

func TestGrokMediaAllIneligibleReturnsStable503WithoutUpstreamOrBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const groupID = int64(10)
	account := service.Account{
		ID:          452,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"access_token": "test-token",
			"model_mapping": map[string]any{
				"grok-imagine-image": "grok-imagine-image",
			},
		},
		Extra: map[string]any{service.GrokMediaEligibleExtraKey: false},
	}
	upstream := &grokMediaFailureHTTPUpstream{status: http.StatusOK}
	handler, usageRepo, billingRepo := newGrokMediaFailureBillingHandler(t, []service.Account{account}, upstream)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-imagine-image","prompt":"draw"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	user := &service.User{ID: 902, Balance: 100}
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      901,
		UserID:  user.ID,
		User:    user,
		GroupID: handlerInt64Ptr(groupID),
		Group: &service.Group{
			ID:                   groupID,
			Name:                 "Grok",
			Platform:             service.PlatformGrok,
			Status:               service.StatusActive,
			Hydrated:             true,
			RateMultiplier:       1,
			AllowImageGeneration: true,
		},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID, Concurrency: 1})

	handler.GrokImages(c)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "grok_media_no_eligible_account")
	require.Zero(t, upstream.callCount())
	require.Empty(t, usageRepo.snapshot())
	require.Empty(t, billingRepo.snapshot())
}

// CaptureBatchImageBalance 实现 UsageBillingRepository（Seedance/E2E 场景不触发批量生图计费）。
func (r *grokGatewayLiteE2EBillingRepo) CaptureBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return &service.BatchImageBalanceHoldResult{}, nil
}

// ReserveBatchImageBalance 实现 UsageBillingRepository（Seedance/E2E 场景不触发批量生图计费）。
func (r *grokGatewayLiteE2EBillingRepo) ReserveBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return &service.BatchImageBalanceHoldResult{}, nil
}

// ReleaseBatchImageBalance 实现 UsageBillingRepository（Seedance/E2E 场景不触发批量生图计费）。
func (r *grokGatewayLiteE2EBillingRepo) ReleaseBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return &service.BatchImageBalanceHoldResult{}, nil
}
