//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokThresholdRepo struct {
	*mockAccountRepoForPlatform
	mu                     sync.Mutex
	setCalls               int
	clearCalls             []int64
	clearAttempts          []int64
	setFailures            int
	accountsByPlatform     []Account
	accountsByID           map[int64]Account
	beforeConditionalClear func()
}

func (r *grokThresholdRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCalls++
	if r.setFailures > 0 {
		r.setFailures--
		return errors.New("temporary database failure")
	}
	r.initializeAccountsLocked()
	account := r.accountsByID[id]
	account.ID = id
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = reason
	r.accountsByID[id] = account
	return nil
}

func (r *grokThresholdRepo) ClearTempUnschedulable(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearCalls = append(r.clearCalls, id)
	return nil
}

func (r *grokThresholdRepo) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeAccountsLocked()
	return append([]Account(nil), r.accountsByPlatform...), nil
}

func (r *grokThresholdRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeAccountsLocked()
	account, ok := r.accountsByID[id]
	if !ok {
		return nil, errors.New("account not found")
	}
	copy := account
	return &copy, nil
}

func (r *grokThresholdRepo) ClearGrokSchedulingThresholdPauseIfMatch(_ context.Context, id int64, until time.Time, reason string) (bool, error) {
	r.mu.Lock()
	hook := r.beforeConditionalClear
	r.beforeConditionalClear = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeAccountsLocked()
	r.clearAttempts = append(r.clearAttempts, id)
	account, ok := r.accountsByID[id]
	if !ok || account.TempUnschedulableUntil == nil ||
		!account.TempUnschedulableUntil.Equal(until) || account.TempUnschedulableReason != reason {
		return false, nil
	}
	account.TempUnschedulableUntil = nil
	account.TempUnschedulableReason = ""
	r.accountsByID[id] = account
	r.clearCalls = append(r.clearCalls, id)
	return true, nil
}

func (r *grokThresholdRepo) replacePause(id int64, until time.Time, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initializeAccountsLocked()
	account := r.accountsByID[id]
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = reason
	r.accountsByID[id] = account
}

func (r *grokThresholdRepo) initializeAccountsLocked() {
	if r.accountsByID != nil {
		return
	}
	r.accountsByID = make(map[int64]Account, len(r.accountsByPlatform))
	for _, account := range r.accountsByPlatform {
		r.accountsByID[account.ID] = account
	}
}

func (r *grokThresholdRepo) setCallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.setCalls
}

type blockingGrokThresholdSettingRepo struct {
	*fakeSettingRepo
	started chan struct{}
	release chan struct{}
	once    sync.Once
	value   string
}

func (r *blockingGrokThresholdSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if key != SettingKeyGrokSchedulingThresholdPercent {
		return r.fakeSettingRepo.GetValue(ctx, key)
	}
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
		return r.value, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func newGrokThresholdService(repo AccountRepository, threshold int) *RateLimitService {
	settingRepo := &fakeSettingRepo{vals: map[string]string{
		SettingKeyGrokSchedulingThresholdPercent: "80",
	}}
	if threshold == 100 {
		settingRepo.vals[SettingKeyGrokSchedulingThresholdPercent] = "100"
	}
	settingService := NewSettingService(settingRepo, &config.Config{})
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetSettingService(settingService)
	return service
}

func grokThresholdAccount(id int64, used float64, resetAt time.Time) Account {
	return Account{
		ID:          id,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			"grok_sched_utilization": used,
			"grok_sched_reset_at":    resetAt.UTC().Format(time.RFC3339),
		},
	}
}

func TestBuildGrokSchedulerExtraUpdatesClearsMissingReset(t *testing.T) {
	updates := buildGrokSchedulerExtraUpdates(&xai.QuotaSnapshot{
		Requests: &xai.QuotaWindow{Limit: grokInt64PtrForTest(100), Remaining: grokInt64PtrForTest(50)},
	}, time.Now())
	require.Contains(t, updates, "grok_sched_reset_at")
	require.Nil(t, updates["grok_sched_reset_at"])
}

func TestSchedulingResetTimeAcceptsSupportedShapes(t *testing.T) {
	now := time.Now()
	unixSeconds := now.Add(time.Minute).Unix()
	unixMillis := now.Add(2 * time.Minute).UnixMilli()
	for _, raw := range []any{
		now.Add(3 * time.Minute).UTC().Format(time.RFC3339),
		unixSeconds,
		unixMillis,
		int64(60),
		"1m",
	} {
		parsed, ok := schedulingResetTime(raw)
		require.True(t, ok, "raw=%v", raw)
		require.True(t, parsed.After(now), "raw=%v parsed=%v", raw, parsed)
	}
}

func TestApplyGrokSchedulingThresholdRetriesAfterPersistFailure(t *testing.T) {
	repo := &grokThresholdRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{}, setFailures: 1}
	service := newGrokThresholdService(repo, 80)
	account := grokThresholdAccount(1001, 90, time.Now().Add(time.Hour))

	require.True(t, service.ApplyGrokSchedulingThreshold(context.Background(), &account))
	require.True(t, service.ApplyGrokSchedulingThreshold(context.Background(), &account))
	require.Equal(t, 2, repo.setCallCount())
}

func TestApplyGrokSchedulingThresholdCoalescesConcurrentPersist(t *testing.T) {
	repo := &grokThresholdRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{}}
	service := newGrokThresholdService(repo, 80)
	account := grokThresholdAccount(1002, 90, time.Now().Add(time.Hour))

	results := make(chan bool, 32)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			copy := account
			results <- service.ApplyGrokSchedulingThreshold(context.Background(), &copy)
		}()
	}
	wait.Wait()
	close(results)
	for result := range results {
		require.True(t, result)
	}
	require.Equal(t, 1, repo.setCallCount())
}

func TestGrokSchedulingThresholdCacheGenerationRejectsStaleLoad(t *testing.T) {
	repo := &blockingGrokThresholdSettingRepo{
		fakeSettingRepo: &fakeSettingRepo{vals: map[string]string{}},
		started:         make(chan struct{}),
		release:         make(chan struct{}),
		value:           "80",
	}
	settingService := NewSettingService(repo, &config.Config{})
	settingService.grokSchedulingThresholdCache.Store(&cachedGrokSchedulingThreshold{value: 100, expiresAt: 0})

	result := make(chan int, 1)
	go func() {
		result <- settingService.GetGrokSchedulingThresholdPercent(context.Background())
	}()

	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("等待旧阈值数据库读取开始超时")
	}
	settingService.refreshCachedSettings(&SystemSettings{GrokSchedulingThresholdPercent: 65})
	close(repo.release)

	select {
	case got := <-result:
		require.Equal(t, 65, got)
	case <-time.After(time.Second):
		t.Fatal("等待阈值读取完成超时")
	}
	require.Equal(t, 65, settingService.GetGrokSchedulingThresholdPercent(context.Background()))
}

func TestReconcileGrokSchedulingThresholdCASClearsOwnedPause(t *testing.T) {
	now := time.Now()
	owned := grokThresholdAccount(1003, 50, now.Add(time.Hour))
	ownedDecision := grokSchedulingThresholdDecision{ThresholdPercent: 80, UsedPercent: 90, Until: now.Add(time.Hour), ShouldPause: true}
	owned.TempUnschedulableUntil = &ownedDecision.Until
	owned.TempUnschedulableReason = buildGrokSchedulingThresholdReason(ownedDecision, now)
	other := grokThresholdAccount(1004, 50, now.Add(time.Hour))
	other.TempUnschedulableUntil = &ownedDecision.Until
	other.TempUnschedulableReason = `{"source":"oauth_401"}`
	repo := &grokThresholdRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{},
		accountsByPlatform:         []Account{owned, other},
	}
	blocker := &runtimeBlockRecorder{}
	service := newGrokThresholdService(repo, 80)
	service.SetAccountRuntimeBlocker(blocker)

	service.ReconcileGrokSchedulingThreshold(100)

	require.Equal(t, []int64{owned.ID}, repo.clearCalls)
	require.Equal(t, []int64{owned.ID}, repo.clearAttempts)
	require.Equal(t, []int64{owned.ID}, blocker.clearedIDs)
	cleared, err := repo.GetByID(context.Background(), owned.ID)
	require.NoError(t, err)
	require.Nil(t, cleared.TempUnschedulableUntil)
	require.Empty(t, cleared.TempUnschedulableReason)
}

func TestReconcileGrokSchedulingThresholdCASMismatchReloadsNewPause(t *testing.T) {
	now := time.Now()
	owned := grokThresholdAccount(1005, 50, now.Add(time.Hour))
	decision := grokSchedulingThresholdDecision{ThresholdPercent: 80, UsedPercent: 90, Until: now.Add(time.Hour), ShouldPause: true}
	owned.TempUnschedulableUntil = &decision.Until
	owned.TempUnschedulableReason = buildGrokSchedulingThresholdReason(decision, now)
	newUntil := now.Add(2 * time.Hour).UTC().Truncate(time.Second)
	newReason := `{"source":"oauth_401","error_message":"new concurrent pause"}`
	repo := &grokThresholdRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{},
		accountsByPlatform:         []Account{owned},
	}
	repo.beforeConditionalClear = func() {
		repo.replacePause(owned.ID, newUntil, newReason)
	}
	cache := &grokThresholdTempUnschedCache{}
	blocker := &runtimeBlockRecorder{}
	service := newGrokThresholdService(repo, 80)
	service.tempUnschedCache = cache
	service.SetAccountRuntimeBlocker(blocker)

	service.ReconcileGrokSchedulingThreshold(100)

	require.Empty(t, repo.clearCalls)
	require.Equal(t, []int64{owned.ID}, repo.clearAttempts)
	require.Empty(t, blocker.clearedIDs, "CAS 失败时不能短暂解除仍有效的新暂停")
	require.Len(t, blocker.accounts, 1)
	require.WithinDuration(t, newUntil, blocker.until[0], time.Second)
	state, err := cache.GetTempUnsched(context.Background(), owned.ID)
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Equal(t, newUntil.Unix(), state.UntilUnix)
	require.Equal(t, "new concurrent pause", state.ErrorMessage)
	current, err := repo.GetByID(context.Background(), owned.ID)
	require.NoError(t, err)
	require.NotNil(t, current.TempUnschedulableUntil)
	require.WithinDuration(t, newUntil, *current.TempUnschedulableUntil, time.Second)
	require.Equal(t, newReason, current.TempUnschedulableReason)
}

func TestReconcileGrokSchedulingThresholdSkipsMissingUntil(t *testing.T) {
	now := time.Now()
	owned := grokThresholdAccount(1006, 50, now.Add(time.Hour))
	decision := grokSchedulingThresholdDecision{ThresholdPercent: 80, UsedPercent: 90, Until: now.Add(time.Hour), ShouldPause: true}
	owned.TempUnschedulableReason = buildGrokSchedulingThresholdReason(decision, now)
	repo := &grokThresholdRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{},
		accountsByPlatform:         []Account{owned},
	}
	blocker := &runtimeBlockRecorder{}
	service := newGrokThresholdService(repo, 80)
	service.SetAccountRuntimeBlocker(blocker)

	require.NotPanics(t, func() { service.ReconcileGrokSchedulingThreshold(100) })
	require.Empty(t, repo.clearAttempts)
	require.Empty(t, blocker.clearedIDs)
}

type grokThresholdTempUnschedCache struct {
	mu     sync.Mutex
	states map[int64]*TempUnschedState
}

func (c *grokThresholdTempUnschedCache) SetTempUnsched(_ context.Context, accountID int64, state *TempUnschedState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.states == nil {
		c.states = make(map[int64]*TempUnschedState)
	}
	copy := *state
	c.states[accountID] = &copy
	return nil
}

func (c *grokThresholdTempUnschedCache) GetTempUnsched(_ context.Context, accountID int64) (*TempUnschedState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[accountID]
	if state == nil {
		return nil, nil
	}
	copy := *state
	return &copy, nil
}

func (c *grokThresholdTempUnschedCache) DeleteTempUnsched(_ context.Context, accountID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.states, accountID)
	return nil
}
