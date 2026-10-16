package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type tokenRefreshPoolRepo struct {
	AccountRepository

	mu                  sync.Mutex
	pages               map[int64][]Account
	requests            []OAuthRefreshPageOptions
	updatedCredentialID []int64
	setErrorCalls       int
	setTempUnschedCalls int
}

func (r *tokenRefreshPoolRepo) ListOAuthRefreshCandidatePage(_ context.Context, options OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, options)
	accounts := append([]Account(nil), r.pages[options.AfterID]...)
	page := &OAuthRefreshCandidatePage{
		Accounts: accounts,
		HasMore:  len(accounts) == options.Limit,
	}
	if len(accounts) > 0 {
		page.NextAfterID = accounts[len(accounts)-1].ID
	}
	return page, nil
}

func (r *tokenRefreshPoolRepo) UpdateCredentials(_ context.Context, id int64, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updatedCredentialID = append(r.updatedCredentialID, id)
	return nil
}

func (r *tokenRefreshPoolRepo) SetError(context.Context, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	return nil
}

func (r *tokenRefreshPoolRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setTempUnschedCalls++
	return nil
}

func (r *tokenRefreshPoolRepo) snapshot() ([]OAuthRefreshPageOptions, []int64, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]OAuthRefreshPageOptions(nil), r.requests...),
		append([]int64(nil), r.updatedCredentialID...),
		r.setErrorCalls,
		r.setTempUnschedCalls
}

type tokenRefreshPoolRefresher struct {
	platform string
	delay    time.Duration
	err      error

	calls     atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
	startMu   sync.Mutex
	starts    []time.Time
}

func (r *tokenRefreshPoolRefresher) CacheKey(account *Account) string {
	return fmt.Sprintf("token-refresh-pool:%d", account.ID)
}

func (r *tokenRefreshPoolRefresher) CanRefresh(account *Account) bool {
	return account != nil && account.Platform == r.platform && account.Type == AccountTypeOAuth
}

func (r *tokenRefreshPoolRefresher) NeedsRefresh(*Account, time.Duration) bool { return true }

func (r *tokenRefreshPoolRefresher) Refresh(ctx context.Context, _ *Account) (map[string]any, error) {
	r.calls.Add(1)
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		maxActive := r.maxActive.Load()
		if active <= maxActive || r.maxActive.CompareAndSwap(maxActive, active) {
			break
		}
	}
	r.startMu.Lock()
	r.starts = append(r.starts, time.Now())
	delay := r.delay
	r.startMu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{
		"access_token":  "new-access-token",
		"refresh_token": "new-refresh-token",
	}, nil
}

func (r *tokenRefreshPoolRefresher) startsSnapshot() []time.Time {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	return append([]time.Time(nil), r.starts...)
}

func tokenRefreshPoolAccount(id int64, platform string) Account {
	return Account{
		ID:       id,
		Platform: platform,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
		},
	}
}

func newTokenRefreshPoolService(repo *tokenRefreshPoolRepo, refresher *tokenRefreshPoolRefresher, cfg config.TokenRefreshConfig) *TokenRefreshService {
	return &TokenRefreshService{
		accountRepo:    repo,
		candidatePager: repo,
		registrations: []tokenRefreshRegistration{{
			platform:  refresher.platform,
			refresher: refresher,
			executor:  refresher,
		}},
		refreshPolicy: DefaultBackgroundRefreshPolicy(),
		cfg:           &cfg,
	}
}

func TestTokenRefreshService_ProcessRefreshPagesByStableCursor(t *testing.T) {
	repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{
		0: {tokenRefreshPoolAccount(1, PlatformOpenAI), tokenRefreshPoolAccount(2, PlatformOpenAI)},
		2: {tokenRefreshPoolAccount(3, PlatformOpenAI)},
	}}
	refresher := &tokenRefreshPoolRefresher{platform: PlatformOpenAI}
	svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
		RefreshBeforeExpiryHours: 1,
		MaxRetries:               1,
		CandidatePageSize:        2,
		ProviderConcurrency:      2,
		ProviderQPS:              100,
		ProviderFailureThreshold: 5,
		CycleTimeoutSeconds:      2,
	})

	svc.processRefreshContext(context.Background())

	requests, updatedIDs, _, _ := repo.snapshot()
	require.Len(t, requests, 2)
	require.Equal(t, int64(0), requests[0].AfterID)
	require.Equal(t, int64(2), requests[1].AfterID)
	require.Equal(t, []string{PlatformOpenAI}, requests[0].Platforms)
	require.True(t, requests[0].ActiveOnly)
	require.True(t, requests[0].IncludeSetupToken)
	require.True(t, requests[0].RequireRefreshToken)
	require.True(t, requests[0].ExcludeRetryCooldown)
	sort.Slice(updatedIDs, func(i, j int) bool { return updatedIDs[i] < updatedIDs[j] })
	require.Equal(t, []int64{1, 2, 3}, updatedIDs)
	require.Zero(t, svc.candidateAfterID(), "短尾页完成后下个周期应从头扫描")
}

func TestTokenRefreshService_BoundsPerProviderConcurrency(t *testing.T) {
	accounts := make([]Account, 0, 6)
	for id := int64(1); id <= 6; id++ {
		accounts = append(accounts, tokenRefreshPoolAccount(id, PlatformOpenAI))
	}
	repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{0: accounts}}
	refresher := &tokenRefreshPoolRefresher{platform: PlatformOpenAI, delay: 60 * time.Millisecond}
	svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      2,
		ProviderQPS:              100,
		ProviderFailureThreshold: 10,
		CycleTimeoutSeconds:      2,
	})

	svc.processRefreshContext(context.Background())

	require.Equal(t, int64(6), refresher.calls.Load())
	require.Equal(t, int64(2), refresher.maxActive.Load())
}

func TestTokenRefreshService_RetriesConsumeProviderQPSPerAttempt(t *testing.T) {
	repo := &tokenRefreshPoolRepo{}
	refresher := &tokenRefreshPoolRefresher{
		platform: PlatformOpenAI,
		err:      errors.New("temporary provider failure"),
	}
	svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
		MaxRetries:          3,
		ProviderConcurrency: 1,
		ProviderQPS:         100,
	})
	state := &tokenRefreshProviderState{
		service:      svc,
		registration: svc.registrations[0],
		rateGate:     newTokenRefreshRateGateWithInterval(25 * time.Millisecond),
		poolGate:     newTokenRefreshConcurrencyGate(1),
	}
	account := tokenRefreshPoolAccount(44, PlatformOpenAI)

	err := svc.refreshWithRetryWithProviderState(context.Background(), &account, refresher, nil, time.Hour, state)

	require.Error(t, err)
	require.Equal(t, int64(3), refresher.calls.Load())
	starts := refresher.startsSnapshot()
	require.Len(t, starts, 3)
	for i := 1; i < len(starts); i++ {
		require.GreaterOrEqual(t, starts[i].Sub(starts[i-1]), 20*time.Millisecond)
	}
}

func TestTokenRefreshRateGate_CanceledWaiterDoesNotBlockBehindAnotherWaiter(t *testing.T) {
	gate := newTokenRefreshRateGateWithInterval(5 * time.Second)
	_, err := gate.acquire(context.Background())
	require.NoError(t, err)

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, acquireErr := gate.acquire(firstCtx)
		firstDone <- acquireErr
	}()
	// 让第一个等待者进入限速等待；实现不能在这段等待期间持有 gate 锁。
	time.Sleep(20 * time.Millisecond)

	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, acquireErr := gate.acquire(secondCtx)
		secondDone <- acquireErr
	}()
	cancelSecond()

	var secondErr error
	returnedPromptly := false
	select {
	case secondErr = <-secondDone:
		returnedPromptly = true
	case <-time.After(250 * time.Millisecond):
	}

	cancelFirst()
	require.ErrorIs(t, <-firstDone, context.Canceled)
	if !returnedPromptly {
		select {
		case secondErr = <-secondDone:
		case <-time.After(time.Second):
			require.FailNow(t, "canceled rate-gate waiter did not exit")
		}
	}
	require.True(t, returnedPromptly, "canceled waiter must not block behind another waiter's timer")
	require.ErrorIs(t, secondErr, context.Canceled)
}

func TestTokenRefreshService_ProviderFailureThresholdStopsRemainingAccounts(t *testing.T) {
	accounts := make([]Account, 0, 5)
	for id := int64(1); id <= 5; id++ {
		accounts = append(accounts, tokenRefreshPoolAccount(id, PlatformOpenAI))
	}
	repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{0: accounts}}
	refresher := &tokenRefreshPoolRefresher{
		platform: PlatformOpenAI,
		err:      errors.New("temporary provider failure"),
	}
	svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      1,
		ProviderQPS:              100,
		ProviderFailureThreshold: 2,
		CycleTimeoutSeconds:      2,
	})

	svc.processRefreshContext(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, int64(2), refresher.calls.Load())
	require.Zero(t, setErrorCalls)
	require.Equal(t, 2, setTempUnschedCalls)
}

func TestTokenRefreshService_ProviderFailureThresholdIsIsolatedPerProvider(t *testing.T) {
	accounts := []Account{
		tokenRefreshPoolAccount(1, PlatformOpenAI),
		tokenRefreshPoolAccount(2, PlatformOpenAI),
		tokenRefreshPoolAccount(3, PlatformGemini),
		tokenRefreshPoolAccount(4, PlatformGemini),
	}
	repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{0: accounts}}
	openAIRefresher := &tokenRefreshPoolRefresher{
		platform: PlatformOpenAI,
		err:      errors.New("temporary OpenAI failure"),
	}
	geminiRefresher := &tokenRefreshPoolRefresher{platform: PlatformGemini}
	cfg := config.TokenRefreshConfig{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      1,
		ProviderQPS:              100,
		ProviderFailureThreshold: 1,
		CycleTimeoutSeconds:      2,
	}
	svc := &TokenRefreshService{
		accountRepo:    repo,
		candidatePager: repo,
		registrations: []tokenRefreshRegistration{
			{platform: PlatformOpenAI, refresher: openAIRefresher, executor: openAIRefresher},
			{platform: PlatformGemini, refresher: geminiRefresher, executor: geminiRefresher},
		},
		refreshPolicy: DefaultBackgroundRefreshPolicy(),
		cfg:           &cfg,
	}

	svc.processRefreshContext(context.Background())

	_, updatedIDs, _, setTempUnschedCalls := repo.snapshot()
	sort.Slice(updatedIDs, func(i, j int) bool { return updatedIDs[i] < updatedIDs[j] })
	require.Equal(t, int64(1), openAIRefresher.calls.Load())
	require.Equal(t, int64(2), geminiRefresher.calls.Load())
	require.Equal(t, []int64{3, 4}, updatedIDs)
	require.Equal(t, 1, setTempUnschedCalls)
}

func TestTokenRefreshService_ProviderTerminalFailureTripsWithoutAccountMutation(t *testing.T) {
	accounts := []Account{
		tokenRefreshPoolAccount(1, PlatformGrok),
		tokenRefreshPoolAccount(2, PlatformGrok),
		tokenRefreshPoolAccount(3, PlatformGrok),
	}
	repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{0: accounts}}
	refresher := &tokenRefreshPoolRefresher{
		platform: PlatformGrok,
		err: &providerConfigurationRefreshError{
			err: errors.New("shared OAuth client is unavailable"),
		},
	}
	svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
		MaxRetries:               3,
		CandidatePageSize:        10,
		ProviderConcurrency:      1,
		ProviderQPS:              100,
		ProviderFailureThreshold: 10,
		CycleTimeoutSeconds:      2,
	})

	svc.processRefreshContext(context.Background())

	_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, int64(1), refresher.calls.Load())
	require.Zero(t, setErrorCalls)
	require.Zero(t, setTempUnschedCalls)
}

func TestTokenRefreshService_RawProviderClientErrorsTripWithoutAccountMutation(t *testing.T) {
	for _, message := range []string{
		"oauth token endpoint: invalid_client",
		"oauth token endpoint: unauthorized_client",
	} {
		t.Run(message, func(t *testing.T) {
			accounts := []Account{
				tokenRefreshPoolAccount(1, PlatformOpenAI),
				tokenRefreshPoolAccount(2, PlatformOpenAI),
				tokenRefreshPoolAccount(3, PlatformOpenAI),
			}
			repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{0: accounts}}
			refresher := &tokenRefreshPoolRefresher{
				platform: PlatformOpenAI,
				err:      errors.New(message),
			}
			svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
				MaxRetries:               3,
				CandidatePageSize:        10,
				ProviderConcurrency:      1,
				ProviderQPS:              100,
				ProviderFailureThreshold: 10,
				CycleTimeoutSeconds:      2,
			})

			svc.processRefreshContext(context.Background())

			_, _, setErrorCalls, setTempUnschedCalls := repo.snapshot()
			require.Equal(t, int64(1), refresher.calls.Load())
			require.Zero(t, setErrorCalls)
			require.Zero(t, setTempUnschedCalls)
		})
	}
}

func TestTokenRefreshService_CycleTimeoutDoesNotCooldownAndResumesPage(t *testing.T) {
	repo := &tokenRefreshPoolRepo{pages: map[int64][]Account{
		0: {tokenRefreshPoolAccount(1, PlatformOpenAI), tokenRefreshPoolAccount(2, PlatformOpenAI)},
	}}
	refresher := &tokenRefreshPoolRefresher{platform: PlatformOpenAI, delay: 200 * time.Millisecond}
	svc := newTokenRefreshPoolService(repo, refresher, config.TokenRefreshConfig{
		MaxRetries:               1,
		CandidatePageSize:        10,
		ProviderConcurrency:      1,
		ProviderQPS:              100,
		ProviderFailureThreshold: 2,
		CycleTimeoutSeconds:      2,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	svc.processRefreshContext(ctx)

	requests, _, _, setTempUnschedCalls := repo.snapshot()
	require.Len(t, requests, 1)
	require.Zero(t, setTempUnschedCalls, "周期超时不是账号刷新失败证据")
	require.Zero(t, svc.candidateAfterID(), "未完整处理的页不能推进游标")

	refresher.startMu.Lock()
	refresher.delay = 0
	refresher.startMu.Unlock()
	svc.processRefreshContext(context.Background())

	requests, updatedIDs, _, setTempUnschedCalls := repo.snapshot()
	require.Len(t, requests, 2)
	require.Equal(t, int64(0), requests[1].AfterID)
	sort.Slice(updatedIDs, func(i, j int) bool { return updatedIDs[i] < updatedIDs[j] })
	require.Equal(t, []int64{1, 2}, updatedIDs)
	require.Zero(t, setTempUnschedCalls)
}

func TestTokenRefreshService_ConfigBoundsAndSafeDefaults(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	svc := &TokenRefreshService{cfg: &config.TokenRefreshConfig{
		CandidatePageSize:        maxInt,
		ProviderConcurrency:      maxInt,
		ProviderQPS:              maxInt,
		ProviderFailureThreshold: maxInt,
		CycleTimeoutSeconds:      maxInt,
		MaxRetries:               maxInt,
		RetryBackoffSeconds:      maxInt,
	}}

	require.Equal(t, maxTokenRefreshCandidatePageSize, svc.candidatePageSize())
	require.Equal(t, maxTokenRefreshProviderConcurrency, svc.providerConcurrency())
	require.Equal(t, maxTokenRefreshProviderQPS, svc.providerQPS())
	require.Equal(t, maxTokenRefreshProviderFailureThreshold, svc.providerFailureThreshold())
	require.Equal(t, maxTokenRefreshCycleTimeout, svc.cycleTimeout())
	require.Equal(t, maxTokenRefreshMaxRetries, svc.maxRetries())
	require.LessOrEqual(t, svc.retryBackoff(maxTokenRefreshMaxRetries), maxTokenRefreshRetryBackoff)

	defaults := &TokenRefreshService{cfg: &config.TokenRefreshConfig{}}
	require.Equal(t, defaultTokenRefreshCandidatePageSize, defaults.candidatePageSize())
	require.Equal(t, defaultTokenRefreshProviderConcurrency, defaults.providerConcurrency())
	require.Equal(t, defaultTokenRefreshProviderQPS, defaults.providerQPS())
	require.Equal(t, defaultTokenRefreshProviderFailureThreshold, defaults.providerFailureThreshold())
	require.Equal(t, defaultTokenRefreshCycleTimeout, defaults.cycleTimeout())
}
