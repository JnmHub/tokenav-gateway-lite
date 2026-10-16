package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func schedulerCostAccount(id int64, priority int, multiplier float64, now time.Time) *Account {
	account := &Account{
		ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: priority,
		Credentials: map[string]any{"api_key": fmt.Sprintf("key-%d", id), "base_url": "https://example.com/v1"},
		Extra:       map[string]any{UpstreamProviderExtraKey: UpstreamProviderSub2API},
	}
	freshUntil := now.Add(time.Hour)
	receivedAt := now
	snapshot := &UpstreamBillingProbeSnapshot{
		Status: UpstreamBillingProbeStatusOK, ReceivedAt: &receivedAt, FreshUntil: &freshUntil,
		Data: map[string]any{
			"resolved_rate_multiplier": multiplier,
			"peak_rate_enabled":        false,
		},
	}
	snapshot.ConfigFingerprint = upstreamBillingProbeConfigFingerprint(account)
	account.Extra[UpstreamBillingProbeExtraKey] = snapshot
	return account
}

func schedulerSoftCandidates(accounts ...*Account) []openAIAccountCandidateScore {
	result := make([]openAIAccountCandidateScore, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, openAIAccountCandidateScore{account: account, loadInfo: &AccountLoadInfo{AccountID: account.ID}, score: 10})
	}
	return result
}

func TestOpenAISchedulerCostFactorPrefersLowerCostWithinPriority(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	candidates := schedulerSoftCandidates(
		schedulerCostAccount(1, 1, 0.8, now),
		schedulerCostAccount(2, 1, 1.6, now),
	)
	applyOpenAISchedulerSoftSignals(candidates, UpstreamBillingRuntimeSettings{CostSchedulingEnabled: true, UpstreamCostWeight: 1}, now)
	require.Equal(t, 1.0, candidates[0].upstreamCostFactor)
	require.Equal(t, 0.0, candidates[1].upstreamCostFactor)
	require.Greater(t, candidates[0].score, candidates[1].score)
}

func TestOpenAISchedulerCostUnknownSingleAndStaleAreNeutral(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	known := schedulerCostAccount(1, 1, 0.8, now)
	stale := schedulerCostAccount(2, 1, 1.6, now)
	staleSnapshot := stale.Extra[UpstreamBillingProbeExtraKey].(*UpstreamBillingProbeSnapshot)
	staleAt := now.Add(-time.Second)
	staleSnapshot.FreshUntil = &staleAt
	candidates := schedulerSoftCandidates(known, stale)
	applyOpenAISchedulerSoftSignals(candidates, UpstreamBillingRuntimeSettings{CostSchedulingEnabled: true, UpstreamCostWeight: 1}, now)
	require.Equal(t, openAISchedulerNeutralSoftFactor, candidates[0].upstreamCostFactor, "one trustworthy cost is not enough to infer ranking")
	require.Equal(t, openAISchedulerNeutralSoftFactor, candidates[1].upstreamCostFactor)
	require.Equal(t, candidates[0].score, candidates[1].score)
}

func TestOpenAISchedulerFailedFreshLastGoodRemainsUsable(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	cheap := schedulerCostAccount(1, 1, 0.5, now)
	cheap.Extra[UpstreamBillingProbeExtraKey].(*UpstreamBillingProbeSnapshot).Status = UpstreamBillingProbeStatusFailed
	expensive := schedulerCostAccount(2, 1, 2, now)
	candidates := schedulerSoftCandidates(cheap, expensive)
	applyOpenAISchedulerSoftSignals(candidates, UpstreamBillingRuntimeSettings{CostSchedulingEnabled: true, UpstreamCostWeight: 1}, now)
	require.Equal(t, 1.0, candidates[0].upstreamCostFactor)
	require.Greater(t, candidates[0].score, candidates[1].score)
}

func TestOpenAISchedulerQuotaHeadroomAndSoonestReset(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	a := &Account{ID: 1, Priority: 1, Extra: map[string]any{
		"quota_limit": 100.0, "quota_used": 20.0,
		"quota_daily_reset_at": now.Add(time.Hour).Format(time.RFC3339), "quota_reset_updated_at": now.Format(time.RFC3339),
	}}
	b := &Account{ID: 2, Priority: 1, Extra: map[string]any{
		"quota_limit": 100.0, "quota_used": 80.0,
		"quota_daily_reset_at": now.Add(4 * time.Hour).Format(time.RFC3339), "quota_reset_updated_at": now.Format(time.RFC3339),
	}}
	unknown := &Account{ID: 3, Priority: 1, Extra: map[string]any{}}
	candidates := schedulerSoftCandidates(a, b, unknown)
	applyOpenAISchedulerSoftSignals(candidates, UpstreamBillingRuntimeSettings{QuotaHeadroomWeight: 1, PreferSoonestResetWeight: 1}, now)
	require.InDelta(t, 0.8, candidates[0].quotaHeadroomFactor, 1e-9)
	require.InDelta(t, 0.2, candidates[1].quotaHeadroomFactor, 1e-9)
	require.Equal(t, openAISchedulerNeutralSoftFactor, candidates[2].quotaHeadroomFactor)
	require.Equal(t, 1.0, candidates[0].soonestResetFactor)
	require.Equal(t, 0.0, candidates[1].soonestResetFactor)
	require.Equal(t, openAISchedulerNeutralSoftFactor, candidates[2].soonestResetFactor)
}

func TestOpenAISchedulerIgnoresStaleOrUnversionedQuotaReset(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	for name, extra := range map[string]map[string]any{
		"missing observation": {
			"quota_daily_reset_at": now.Add(time.Hour).Format(time.RFC3339),
		},
		"stale observation": {
			"quota_daily_reset_at":   now.Add(time.Hour).Format(time.RFC3339),
			"quota_reset_updated_at": now.Add(-openAIQuotaResetSnapshotStaleAfter - time.Second).Format(time.RFC3339),
		},
		"future observation": {
			"quota_daily_reset_at":   now.Add(time.Hour).Format(time.RFC3339),
			"quota_reset_updated_at": now.Add(6 * time.Minute).Format(time.RFC3339),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, known := openAISoonestTrustedReset(&Account{Extra: extra}, now)
			require.False(t, known)
		})
	}
}

func TestOpenAISchedulerSoftSignalsDisabledPreservesLegacyScore(t *testing.T) {
	now := time.Now()
	candidates := schedulerSoftCandidates(schedulerCostAccount(1, 1, 0.1, now), schedulerCostAccount(2, 1, 9, now))
	before := []float64{candidates[0].score, candidates[1].score}
	applyOpenAISchedulerSoftSignals(candidates, UpstreamBillingRuntimeSettings{}, now)
	require.Equal(t, before[0], candidates[0].score)
	require.Equal(t, before[1], candidates[1].score)
}

func TestUpstreamPurchaseCostNeverChangesCustomerBilling(t *testing.T) {
	now := time.Now().UTC()
	account := schedulerCostAccount(1, 1, 0.05, now)
	accountRate := 1.7
	account.RateMultiplier = &accountRate
	billing := NewBillingService(&config.Config{}, nil)
	input := CostInput{
		Model:          "gpt-5.6-sol",
		Tokens:         UsageTokens{InputTokens: 1000, OutputTokens: 200},
		RateMultiplier: account.BillingRateMultiplier(),
	}
	before, err := billing.CalculateCostUnified(input)
	require.NoError(t, err)

	candidates := schedulerSoftCandidates(account, schedulerCostAccount(2, 1, 9, now))
	applyOpenAISchedulerSoftSignals(candidates, UpstreamBillingRuntimeSettings{CostSchedulingEnabled: true, UpstreamCostWeight: 2}, now)
	after, err := billing.CalculateCostUnified(input)
	require.NoError(t, err)
	require.Equal(t, 1.7, account.BillingRateMultiplier())
	require.Equal(t, before.ActualCost, after.ActualCost)
}

func BenchmarkBuildOpenAIAccountLoadPlanSoftSignals(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprintf("accounts_%d", size), func(b *testing.B) {
			resetUpstreamBillingRuntimeSettingsForTest()
			b.Cleanup(resetUpstreamBillingRuntimeSettingsForTest)
			storeUpstreamBillingRuntimeSettings(UpstreamBillingRuntimeSettings{
				CostSchedulingEnabled: true, UpstreamCostWeight: 0.5, QuotaHeadroomWeight: 0.5, PreferSoonestResetWeight: 0.2,
			})
			now := time.Now().UTC()
			accounts := make([]*Account, 0, size)
			loadMap := make(map[int64]*AccountLoadInfo, size)
			for i := 0; i < size; i++ {
				account := schedulerCostAccount(int64(i+1), i%4, 0.5+float64(i%10)/10, now)
				account.Extra["quota_limit"] = 100.0
				account.Extra["quota_used"] = float64(i % 90)
				account.Extra["quota_daily_reset_at"] = now.Add(time.Duration(i%60+1) * time.Minute).Format(time.RFC3339)
				account.Extra["quota_reset_updated_at"] = now.Format(time.RFC3339)
				accounts = append(accounts, account)
				loadMap[account.ID] = &AccountLoadInfo{AccountID: account.ID, LoadRate: i % 100}
			}
			scheduler := &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{}, stats: newOpenAIAccountRuntimeStats()}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = scheduler.buildOpenAIAccountLoadPlan(OpenAIAccountScheduleRequest{SessionHash: "benchmark"}, accounts, loadMap)
			}
		})
	}
}
