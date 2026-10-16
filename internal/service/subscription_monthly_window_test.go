//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// activateWindowUserSubRepo 记录 ActivateWindows 收到的锚点。
// 本地仓储为单锚点签名（startOfDay(激活时刻)），官方为 daily/periodic 双锚点。
type activateWindowUserSubRepo struct {
	userSubRepoNoop
	start        time.Time
	activateCall bool
}

type monthlyResetUserSubRepo struct {
	userSubRepoNoop
	resetCalled bool
	resetAt     time.Time
}

func (r *monthlyResetUserSubRepo) ResetMonthlyUsage(_ context.Context, _ int64, resetAt time.Time) error {
	r.resetCalled = true
	r.resetAt = resetAt
	return nil
}

func (r *activateWindowUserSubRepo) ActivateWindows(_ context.Context, _ int64, start time.Time) error {
	r.activateCall = true
	r.start = start
	return nil
}

func TestDelayedFirstUseAnchorsMonthlyWindowAtActivation(t *testing.T) {
	repo := &activateWindowUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	startsAt := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	sub := &UserSubscription{
		ID:        1,
		StartsAt:  startsAt,
		ExpiresAt: startsAt.Add(45 * 24 * time.Hour),
	}

	require.NoError(t, svc.CheckAndActivateWindow(context.Background(), sub))

	// 本地单锚点语义：ActivateWindows 只收到激活当天 0 点（日历日语义），
	// 周期窗口锚点由 automaticWindowStartAt 在读取侧推算，不再单独落库。
	require.True(t, repo.activateCall)
	require.Equal(t, startOfDay(time.Now()), repo.start)
}

func TestThirtyDaySubscriptionDoesNotResetMonthlyQuotaBeforeExpiry(t *testing.T) {
	startsAt := time.Date(2026, 7, 1, 23, 30, 0, 0, time.UTC)
	expiresAt := startsAt.Add(30 * 24 * time.Hour)
	renewed := renewedSubscriptionTerm(&UserSubscription{}, "", startsAt, expiresAt)

	require.Equal(t, startsAt, *renewed.MonthlyWindowStart)
	require.False(t, renewed.NeedsMonthlyResetAt(expiresAt.Add(-time.Second)))
	require.True(t, renewed.NeedsMonthlyResetAt(expiresAt))
	require.False(t, renewed.canAutomaticallyResetMonthlyAt(expiresAt))
	require.Equal(t, expiresAt, *renewed.MonthlyResetTime())
}

// [本地适配] 官方的 TestCheckAndResetWindowsDoesNotResetExactThirtyDayLegacyMonthlyWindow、
// TestCheckAndResetWindowsResetsPartialFinalMonthlySubscriptions、
// TestValidateAndCheckLimitsKeepsLegacyMonthlyUsageBeforeExpiry、
// TestValidateAndCheckLimitsResetsMonthlyUsageWithPartialFinalPeriod 依赖 svc.now
// 注入固定时钟；本仓库 SubscriptionService 无时间注入点（内部直接 time.Now()），
// 固定 2026-07 日期在真实时钟下语义反转，故暂不移植，窗口推算逻辑由下方
// automaticWindowStartAt / normalizeExpiredWindowsAt 的纯函数用例覆盖。

func TestNormalizeExpiredWindowsKeepsLegacyMonthlyUsageBeforeExpiry(t *testing.T) {
	windowStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	startsAt := windowStart.Add(23*time.Hour + 30*time.Minute)
	now := windowStart.Add(30 * 24 * time.Hour)
	subs := []UserSubscription{{
		StartsAt:           startsAt,
		ExpiresAt:          startsAt.Add(30 * 24 * time.Hour),
		MonthlyWindowStart: &windowStart,
		MonthlyUsageUSD:    12,
	}}

	normalizeExpiredWindowsAt(subs, now)

	require.Equal(t, 12.0, subs[0].MonthlyUsageUSD)
	require.Equal(t, windowStart, *subs[0].MonthlyWindowStart)
}

func TestNormalizeExpiredWindowsResetsMonthlyUsageWithPartialFinalPeriod(t *testing.T) {
	startsAt := time.Date(2026, 7, 1, 23, 30, 0, 0, time.UTC)
	windowStart := startsAt.Add(-23*time.Hour - 30*time.Minute)
	now := startsAt.Add(30 * 24 * time.Hour)
	subs := []UserSubscription{{
		StartsAt:           startsAt,
		ExpiresAt:          startsAt.Add(31 * 24 * time.Hour),
		MonthlyWindowStart: &windowStart,
		MonthlyUsageUSD:    12,
	}}

	normalizeExpiredWindowsAt(subs, now)

	require.Zero(t, subs[0].MonthlyUsageUSD)
	require.Nil(t, subs[0].MonthlyWindowStart)
}

func TestValidateAndCheckLimitsRejectsExactExpiry(t *testing.T) {
	// 动态取当前时刻，避免固定日期随真实时间流逝语义漂移。
	now := time.Now()
	sub := &UserSubscription{Status: SubscriptionStatusActive, ExpiresAt: now}
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil)

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, &Group{})

	require.ErrorIs(t, err, ErrSubscriptionExpired)
	require.False(t, needsMaintenance)
}

func TestAutomaticWindowsAllowPartialFinalDailyAndWeeklyPeriods(t *testing.T) {
	startsAt := time.Date(2026, 7, 1, 15, 45, 0, 0, time.UTC)
	legacyWindowStart := startOfDay(startsAt)
	tests := []struct {
		name      string
		period    time.Duration
		expiresAt time.Time
	}{
		{name: "daily", period: 24 * time.Hour, expiresAt: startsAt.Add(36 * time.Hour)},
		{name: "weekly", period: 7 * 24 * time.Hour, expiresAt: startsAt.Add(10 * 24 * time.Hour)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &UserSubscription{StartsAt: startsAt, ExpiresAt: tt.expiresAt}
			resetAt, ok := sub.automaticWindowStartAt(&legacyWindowStart, tt.period, startsAt.Add(tt.period))

			require.True(t, ok)
			require.Equal(t, startsAt.Add(tt.period), resetAt)
		})
	}
}

func TestAutomaticWindowPreservesPersistedManualAnchorAndAdvancesWholePeriods(t *testing.T) {
	startsAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	manualAnchor := startsAt.Add(5 * 24 * time.Hour)
	sub := &UserSubscription{StartsAt: startsAt, ExpiresAt: startsAt.Add(100 * 24 * time.Hour)}

	resetAt, ok := sub.automaticWindowStartAt(&manualAnchor, 30*24*time.Hour, manualAnchor.Add(65*24*time.Hour))

	require.True(t, ok)
	require.Equal(t, manualAnchor.Add(60*24*time.Hour), resetAt)
}

func TestAutomaticWindowPreservesPeriodAlignedLaterMidnightManualAnchor(t *testing.T) {
	startsAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	manualAnchor := startOfDay(startsAt).Add(30 * 24 * time.Hour)
	sub := &UserSubscription{StartsAt: startsAt, ExpiresAt: startsAt.Add(100 * 24 * time.Hour)}

	resetAt, ok := sub.automaticWindowStartAt(&manualAnchor, 30*24*time.Hour, startsAt.Add(60*24*time.Hour))

	require.True(t, ok)
	require.Equal(t, manualAnchor.Add(30*24*time.Hour), resetAt)
}

func TestAutomaticWindowPreservesExactMidnightManualAnchor(t *testing.T) {
	startsAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	manualAnchor := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)
	sub := &UserSubscription{StartsAt: startsAt, ExpiresAt: startsAt.Add(100 * 24 * time.Hour)}

	resetAt, ok := sub.automaticWindowStartAt(&manualAnchor, 30*24*time.Hour, manualAnchor.Add(30*24*time.Hour))

	require.True(t, ok)
	require.Equal(t, manualAnchor.Add(30*24*time.Hour), resetAt)
}
