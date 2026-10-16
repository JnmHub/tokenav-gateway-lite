package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type schedulerPolicyRepoStub struct {
	mu      sync.Mutex
	values  map[string]string
	failCAS error
}

func newSchedulerPolicyRepoStub() *schedulerPolicyRepoStub {
	return &schedulerPolicyRepoStub{values: make(map[string]string)}
}

func (r *schedulerPolicyRepoStub) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *schedulerPolicyRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	value, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return value.Value, nil
}

func (r *schedulerPolicyRepoStub) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *schedulerPolicyRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
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

func (r *schedulerPolicyRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *schedulerPolicyRepoStub) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *schedulerPolicyRepoStub) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func (r *schedulerPolicyRepoStub) CompareAndSwapMultiple(_ context.Context, revisionKey, expectedRevision, nextRevision string, values map[string]string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failCAS != nil {
		return false, r.failCAS
	}
	current := r.values[revisionKey]
	if current == "" {
		current = "0"
	}
	if current != expectedRevision {
		return false, nil
	}
	for key, value := range values {
		r.values[key] = value
	}
	r.values[revisionKey] = nextRevision
	return true, nil
}

func validSchedulerPolicyUpdate(revision int64) OpenAISchedulerRuntimePolicyUpdate {
	return OpenAISchedulerRuntimePolicyUpdate{
		ExpectedRevision:         revision,
		Preset:                   OpenAISchedulerPolicyPresetBalanced,
		AdvancedEnabled:          true,
		StickySoftScoringEnabled: false,
		TopK:                     10,
		PreferSubscriptionPool:   true,
		PriorityWeight:           0.8,
		LoadWeight:               1.2,
		QueueWeight:              1,
		ErrorRateWeight:          1,
		TTFTWeight:               0.8,
		PreferSoonestResetWeight: 0.4,
		QuotaHeadroomWeight:      0.7,
		UpstreamCostEnabled:      true,
		UpstreamCostWeight:       0.6,
		SessionStickyWeight:      0.5,
	}
}

func TestOpenAISchedulerRuntimePolicySafeDefaultsKeepHardBoundaries(t *testing.T) {
	svc := NewSettingService(newSchedulerPolicyRepoStub(), &config.Config{})
	policy := svc.CurrentOpenAISchedulerRuntimePolicy(context.Background())

	require.False(t, policy.AdvancedEnabled)
	require.False(t, policy.StickySoftScoringEnabled)
	require.Equal(t, OpenAISchedulerPreviousResponseModeHard, policy.PreviousResponseMode)
	require.True(t, policy.SalesBillingIsolated)
	require.Equal(t, defaultOpenAISchedulerRuntimeTopK, policy.TopK)
}

func TestOpenAISchedulerRuntimePolicyCASConflictAndPersistenceFailureKeepSnapshot(t *testing.T) {
	repo := newSchedulerPolicyRepoStub()
	svc := NewSettingService(repo, &config.Config{})
	before := svc.CurrentOpenAISchedulerRuntimePolicy(context.Background())

	updated, err := svc.UpdateOpenAISchedulerRuntimePolicy(context.Background(), validSchedulerPolicyUpdate(before.Revision))
	require.NoError(t, err)
	require.EqualValues(t, 1, updated.Revision)
	require.True(t, updated.AdvancedEnabled)
	require.Equal(t, OpenAISchedulerPreviousResponseModeHard, updated.PreviousResponseMode)
	require.True(t, updated.SalesBillingIsolated)

	_, err = svc.UpdateOpenAISchedulerRuntimePolicy(context.Background(), validSchedulerPolicyUpdate(0))
	require.ErrorIs(t, err, ErrOpenAISchedulerPolicyRevisionConflict)
	require.EqualValues(t, 1, svc.CurrentOpenAISchedulerRuntimePolicy(context.Background()).Revision)

	repo.failCAS = errors.New("database unavailable")
	failed := validSchedulerPolicyUpdate(1)
	failed.TopK = 20
	_, err = svc.UpdateOpenAISchedulerRuntimePolicy(context.Background(), failed)
	require.ErrorContains(t, err, "database unavailable")
	stillActive := svc.CurrentOpenAISchedulerRuntimePolicy(context.Background())
	require.EqualValues(t, 1, stillActive.Revision)
	require.Equal(t, 10, stillActive.TopK, "failed persistence must not replace the in-memory snapshot")
}

func TestOpenAISchedulerRuntimePolicyRequestPointerStaysFrozenAcrossUpdate(t *testing.T) {
	repo := newSchedulerPolicyRepoStub()
	svc := NewSettingService(repo, &config.Config{})
	frozen := svc.CurrentOpenAISchedulerRuntimePolicy(context.Background())
	updated, err := svc.UpdateOpenAISchedulerRuntimePolicy(context.Background(), validSchedulerPolicyUpdate(frozen.Revision))
	require.NoError(t, err)

	require.EqualValues(t, 0, frozen.Revision)
	require.False(t, frozen.AdvancedEnabled)
	require.EqualValues(t, 1, updated.Revision)
	require.True(t, updated.AdvancedEnabled)
	require.NotSame(t, frozen, updated)
}

func TestOpenAISchedulerRuntimePolicyStaleWhileRevalidateConvergesInstances(t *testing.T) {
	repo := newSchedulerPolicyRepoStub()
	first := NewSettingService(repo, &config.Config{})
	second := NewSettingService(repo, &config.Config{})
	stale := second.CurrentOpenAISchedulerRuntimePolicy(context.Background())
	require.EqualValues(t, 0, stale.Revision)

	_, err := first.UpdateOpenAISchedulerRuntimePolicy(context.Background(), validSchedulerPolicyUpdate(0))
	require.NoError(t, err)
	second.openAISchedulerRuntimePolicyCache.Store(&cachedOpenAISchedulerRuntimePolicy{
		policy: stale, expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})
	started := time.Now()
	require.EqualValues(t, 0, second.CurrentOpenAISchedulerRuntimePolicy(context.Background()).Revision)
	require.Less(t, time.Since(started), 100*time.Millisecond, "stale reads must not wait for PostgreSQL")
	require.Eventually(t, func() bool {
		return second.CurrentOpenAISchedulerRuntimePolicy(context.Background()).Revision == 1
	}, time.Second, 10*time.Millisecond)
}

func TestOpenAISchedulerRuntimePolicyRejectsUnsafeRanges(t *testing.T) {
	svc := NewSettingService(newSchedulerPolicyRepoStub(), &config.Config{})
	invalid := validSchedulerPolicyUpdate(0)
	invalid.TopK = 0
	_, err := svc.UpdateOpenAISchedulerRuntimePolicy(context.Background(), invalid)
	require.ErrorContains(t, err, "top_k")

	invalid = validSchedulerPolicyUpdate(0)
	invalid.PriorityWeight = 3
	_, err = svc.UpdateOpenAISchedulerRuntimePolicy(context.Background(), invalid)
	require.ErrorContains(t, err, "priority_weight")
}

func TestOpenAISchedulerSoftStickyAndSubscriptionScoresDoNotTouchBillingRates(t *testing.T) {
	rateOne, rateTwo := 1.7, 0.4
	accounts := []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"plan_type": "plus"}, Priority: 0, Concurrency: 10, RateMultiplier: &rateOne},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 0, Concurrency: 10, RateMultiplier: &rateTwo},
	}
	policy := &OpenAISchedulerRuntimePolicy{
		TopK: 2, StickySoftScoringEnabled: true, SessionStickyWeight: 1,
		PreferSubscriptionPool: true, SalesBillingIsolated: true,
	}
	scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{}, stats: newOpenAIAccountRuntimeStats()}
	plan := scheduler.buildOpenAIAccountLoadPlan(OpenAIAccountScheduleRequest{
		StickyAccountID: 1,
		RuntimePolicy:   policy,
	}, accounts, map[int64]*AccountLoadInfo{
		1: {AccountID: 1},
		2: {AccountID: 2},
	})

	require.Len(t, plan.candidates, 2)
	var stickyCandidate openAIAccountCandidateScore
	for _, candidate := range plan.candidates {
		if candidate.account.ID == 1 {
			stickyCandidate = candidate
		}
	}
	require.NotNil(t, stickyCandidate.account)
	require.Equal(t, 1.0, stickyCandidate.breakdown.SessionSticky)
	require.Equal(t, 0.35, stickyCandidate.breakdown.SubscriptionPool)
	require.Equal(t, 1.7, accounts[0].BillingRateMultiplier())
	require.Equal(t, 0.4, accounts[1].BillingRateMultiplier())
}

func TestOpenAISchedulerSessionStickySurvivesTenThousandReconnects(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)

	ctx := context.Background()
	groupID := int64(7310)
	account := Account{
		ID:          73101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 20_000,
		GroupIDs:    []int64{groupID},
	}
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{
		"openai:runtime_policy_reconnect": account.ID,
	}}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                &config.Config{},
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	for i := 0; i < 10_000; i++ {
		selection, decision, err := svc.SelectAccountWithScheduler(
			ctx,
			&groupID,
			"",
			"runtime_policy_reconnect",
			"gpt-5.1",
			nil,
			OpenAIUpstreamTransportAny,
			false,
		)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Equal(t, account.ID, selection.Account.ID)
		require.Equal(t, openAIAccountScheduleLayerSessionSticky, decision.Layer)
		require.True(t, decision.StickySessionHit)
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}
	require.Equal(t, account.ID, cache.sessionBindings["openai:runtime_policy_reconnect"])
}

func BenchmarkOpenAISchedulerTopKCandidateCounts(b *testing.B) {
	for _, count := range []int{1, 10, 100, 1000} {
		b.Run(fmt.Sprintf("accounts_%d", count), func(b *testing.B) {
			candidates := make([]openAIAccountCandidateScore, count)
			for i := range candidates {
				candidates[i] = openAIAccountCandidateScore{
					account:  &Account{ID: int64(i + 1), Priority: i % 10},
					loadInfo: &AccountLoadInfo{AccountID: int64(i + 1), LoadRate: i % 100},
					score:    float64((i * 37) % 101),
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = selectTopKOpenAICandidates(candidates, 7)
			}
		})
	}
}
