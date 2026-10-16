package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type reroutedRateRepoStub struct {
	UserGroupRateRepository
	rate  *float64
	err   error
	calls int
}

func (s *reroutedRateRepoStub) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.rate, nil
}

func peakPolicyTestGroup() *Group {
	return &Group{
		ID:                 17,
		RateMultiplier:     1.2,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "06:00",
		PeakRateMultiplier: 1.5,
		PeakTimezone:       "Asia/Shanghai",
		UpdatedAt:          time.Unix(1_700_000_000, 0),
	}
}

func TestResolveEffectiveRateDisabledPreservesExistingBilling(t *testing.T) {
	group := peakPolicyTestGroup()
	group.PeakRateEnabled = false
	group.PeakStart = ""
	group.PeakEnd = ""
	group.PeakRateMultiplier = 0
	group.PeakTimezone = ""

	policy, err := ResolveEffectiveRate(group, nil, time.Date(2026, 8, 1, 23, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1.2, policy.GroupRateMultiplier)
	require.Equal(t, 1.2, policy.ResolvedRateMultiplier)
	require.Equal(t, 1.0, policy.AppliedPeakMultiplier)
	require.Equal(t, 1.2, policy.EffectiveRateMultiplier)
	require.False(t, policy.PeakRateEnabled)
}

func TestResolveEffectiveRateUserOverrideThenPeak(t *testing.T) {
	group := peakPolicyTestGroup()
	userRate := 0.8
	// 15:00 UTC is 23:00 in Shanghai and therefore inside 22:00 -> 06:00.
	policy, err := ResolveEffectiveRate(group, &userRate, time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1.2, policy.GroupRateMultiplier)
	require.NotNil(t, policy.UserRateMultiplier)
	require.Equal(t, 0.8, *policy.UserRateMultiplier)
	require.Equal(t, 0.8, policy.ResolvedRateMultiplier)
	require.Equal(t, 1.5, policy.AppliedPeakMultiplier)
	require.InDelta(t, 1.2, policy.EffectiveRateMultiplier, 1e-12)
}

func TestResolveEffectiveRatePreservesExplicitZeroUserOverride(t *testing.T) {
	group := peakPolicyTestGroup()
	userRate := 0.0
	policy, err := ResolveEffectiveRate(group, &userRate, time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NotNil(t, policy.UserRateMultiplier)
	require.Zero(t, *policy.UserRateMultiplier)
	require.Zero(t, policy.ResolvedRateMultiplier)
	require.Zero(t, policy.EffectiveRateMultiplier)
	require.True(t, policy.Valid())
}

func TestResolveEffectiveRateVersionIsStableForEqualUserRateValues(t *testing.T) {
	group := peakPolicyTestGroup()
	firstRate := 0.8
	secondRate := 0.8
	now := time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)

	first, err := ResolveEffectiveRate(group, &firstRate, now)
	require.NoError(t, err)
	second, err := ResolveEffectiveRate(group, &secondRate, now)
	require.NoError(t, err)
	require.NotSame(t, &firstRate, &secondRate)
	require.Equal(t, first.Version, second.Version, "相同策略值不能因指针地址不同而改变版本")

	secondRate = 0.9
	changed, err := ResolveEffectiveRate(group, &secondRate, now)
	require.NoError(t, err)
	require.NotEqual(t, first.Version, changed.Version)
}

func TestPeakMultiplierAtBoundariesAndCrossMidnight(t *testing.T) {
	group := peakPolicyTestGroup()
	loc, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		{name: "before start", now: time.Date(2026, 8, 1, 21, 59, 59, 0, loc), want: 1},
		{name: "start included", now: time.Date(2026, 8, 1, 22, 0, 0, 0, loc), want: 1.5},
		{name: "before midnight", now: time.Date(2026, 8, 1, 23, 59, 0, 0, loc), want: 1.5},
		{name: "after midnight", now: time.Date(2026, 8, 2, 0, 1, 0, 0, loc), want: 1.5},
		{name: "before end", now: time.Date(2026, 8, 2, 5, 59, 59, 0, loc), want: 1.5},
		{name: "end excluded", now: time.Date(2026, 8, 2, 6, 0, 0, 0, loc), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, group.PeakMultiplierAt(tt.now))
		})
	}
}

func TestPeakMultiplierUsesConfiguredIANATimezoneAcrossDST(t *testing.T) {
	group := peakPolicyTestGroup()
	group.PeakStart = "01:00"
	group.PeakEnd = "03:00"
	group.PeakTimezone = "America/New_York"

	// On 2026-03-08 New York jumps from 01:59 to 03:00. IANA conversion
	// makes 06:30 UTC = 01:30 EST (inside), while 07:00 UTC = 03:00 EDT (end).
	require.Equal(t, 1.5, group.PeakMultiplierAt(time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC)))
	require.Equal(t, 1.0, group.PeakMultiplierAt(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)))
}

func TestFrozenRatePolicyDoesNotChangeAtStreamCompletionOrRetry(t *testing.T) {
	group := peakPolicyTestGroup()
	startedAt := time.Date(2026, 8, 1, 15, 30, 0, 0, time.UTC) // 23:30 Shanghai, peak
	ctx, frozen, err := FreezeEffectiveRatePolicy(context.Background(), group, nil, startedAt)
	require.NoError(t, err)
	require.InDelta(t, 1.8, frozen.EffectiveRateMultiplier, 1e-12)

	// Simulate an admin edit and a stream ending after the peak window. The
	// request context remains authoritative for retries and duplicate callbacks.
	group.RateMultiplier = 9
	group.PeakRateMultiplier = 3
	group.UpdatedAt = group.UpdatedAt.Add(time.Hour)
	endedPolicy, ok := EffectiveRatePolicyFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, frozen.Version, endedPolicy.Version)
	require.Equal(t, frozen.ResolvedAtMillis, endedPolicy.ResolvedAtMillis)
	require.InDelta(t, 1.8, endedPolicy.EffectiveRateMultiplier, 1e-12)

	retryPolicy, ok := EffectiveRatePolicyFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, endedPolicy, retryPolicy)
}

func TestValidatePeakRateConfigRejectsAmbiguousOrUnsafeValues(t *testing.T) {
	require.Error(t, ValidatePeakRateConfig(true, "22:00", "22:00", 1.5, "Asia/Shanghai"))
	require.Error(t, ValidatePeakRateConfig(true, "25:00", "06:00", 1.5, "Asia/Shanghai"))
	require.Error(t, ValidatePeakRateConfig(true, "22:00", "06:00", 0, "Asia/Shanghai"))
	require.Error(t, ValidatePeakRateConfig(true, "22:00", "06:00", 1.5, "Mars/Olympus"))
	require.NoError(t, ValidatePeakRateConfig(true, "22:00", "06:00", 1.5, "Asia/Shanghai"))
}

func TestFreezeRatePolicyForReroutedLocalKeyReadsFreshOverrideAndPreservesEqualValue(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 15, 30, 0, 0, time.UTC)
	group := peakPolicyTestGroup()
	userRate := group.RateMultiplier // 显式覆盖值即使与分组相等，也必须保留审计证据。
	repo := &reroutedRateRepoStub{rate: &userRate}
	apiKey := &APIKey{User: &User{ID: 42}, GroupID: &group.ID, Group: group}
	ctx := ContextWithGatewayLiteLocalAPIKey(context.Background())

	frozenCtx, err := freezeRatePolicyForReroutedAPIKey(
		ctx, &config.Config{RunMode: config.RunModeGatewayLite}, repo, apiKey, startedAt,
	)

	require.NoError(t, err)
	require.Equal(t, 1, repo.calls)
	require.NotNil(t, apiKey.UserRateMultiplier)
	policy, ok := EffectiveRatePolicyFromContext(frozenCtx)
	require.True(t, ok)
	require.NotNil(t, policy.UserRateMultiplier)
	require.InDelta(t, userRate, *policy.UserRateMultiplier, 1e-12)
	require.InDelta(t, 1.8, policy.EffectiveRateMultiplier, 1e-12)
}

func TestFreezeRatePolicyForReroutedMainSiteKeyNeverReadsGatewayLocalUserRate(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 15, 30, 0, 0, time.UTC)
	group := peakPolicyTestGroup()
	localShadowRate := 9.0
	repo := &reroutedRateRepoStub{rate: &localShadowRate}
	apiKey := &APIKey{User: &User{ID: 42}, GroupID: &group.ID, Group: group, UserRateMultiplier: &localShadowRate}

	frozenCtx, err := freezeRatePolicyForReroutedAPIKey(
		context.Background(), &config.Config{RunMode: config.RunModeGatewayLite}, repo, apiKey, startedAt,
	)

	require.NoError(t, err)
	require.Zero(t, repo.calls)
	require.Nil(t, apiKey.UserRateMultiplier)
	policy, ok := EffectiveRatePolicyFromContext(frozenCtx)
	require.True(t, ok)
	require.Nil(t, policy.UserRateMultiplier)
	require.InDelta(t, 1.8, policy.EffectiveRateMultiplier, 1e-12)
}

func TestFreezeRatePolicyForReroutedMainSiteKeyUsesOnlyExactTargetOverride(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 15, 30, 0, 0, time.UTC)
	group := peakPolicyTestGroup()
	group.ID = 88
	localShadowRate := 9.0
	targetRate := 0.7
	repo := &reroutedRateRepoStub{rate: &localShadowRate}
	apiKey := &APIKey{
		User: &User{ID: 42}, GroupID: &group.ID, Group: group,
		UserRateMultiplier: &localShadowRate,
		UserGroupRateMultipliers: map[int64]float64{88: targetRate, 99: 4.0},
	}

	frozenCtx, err := freezeRatePolicyForReroutedAPIKey(
		context.Background(), &config.Config{RunMode: config.RunModeGatewayLite}, repo, apiKey, startedAt,
	)

	require.NoError(t, err)
	require.Zero(t, repo.calls)
	require.NotNil(t, apiKey.UserRateMultiplier)
	require.InDelta(t, targetRate, *apiKey.UserRateMultiplier, 1e-12)
	policy, ok := EffectiveRatePolicyFromContext(frozenCtx)
	require.True(t, ok)
	require.InDelta(t, targetRate, policy.ResolvedRateMultiplier, 1e-12)
	require.InDelta(t, targetRate*group.PeakRateMultiplier, policy.EffectiveRateMultiplier, 1e-12)
}

func TestFreezeRatePolicyForReroutedLocalKeyFailsClosedOnRateLookupError(t *testing.T) {
	repoErr := errors.New("rate store unavailable")
	repo := &reroutedRateRepoStub{err: repoErr}
	group := peakPolicyTestGroup()
	apiKey := &APIKey{User: &User{ID: 42}, GroupID: &group.ID, Group: group}
	ctx := ContextWithGatewayLiteLocalAPIKey(context.Background())

	frozenCtx, err := freezeRatePolicyForReroutedAPIKey(
		ctx, &config.Config{RunMode: config.RunModeGatewayLite}, repo, apiKey, time.Now(),
	)

	require.ErrorIs(t, err, repoErr)
	require.Same(t, ctx, frozenCtx)
	_, ok := EffectiveRatePolicyFromContext(frozenCtx)
	require.False(t, ok)
}
