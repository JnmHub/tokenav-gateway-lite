//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokBillingTestUpstream struct {
	mu      sync.Mutex
	calls   map[string]int
	delay   time.Duration
	handler func(*http.Request) *http.Response
}

func (u *grokBillingTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	key := req.URL.Path
	if req.URL.RawQuery != "" {
		key += "?" + req.URL.RawQuery
	}
	u.mu.Lock()
	if u.calls == nil {
		u.calls = make(map[string]int)
	}
	u.calls[key]++
	u.mu.Unlock()
	if u.delay > 0 {
		time.Sleep(u.delay)
	}
	return u.handler(req), nil
}

func (u *grokBillingTestUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *grokBillingTestUpstream) callCount(key string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[key]
}

type grokBillingUsageRepo struct {
	UsageLogRepository
	mu     sync.Mutex
	starts []time.Time
	stats  *usagestats.AccountStats
}

func (r *grokBillingUsageRepo) GetAccountWindowStats(_ context.Context, _ int64, start time.Time) (*usagestats.AccountStats, error) {
	r.mu.Lock()
	r.starts = append(r.starts, start)
	r.mu.Unlock()
	return r.stats, nil
}

func (r *grokBillingUsageRepo) windowStarts() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.starts...)
}

func TestGrokQuotaServiceProbeBillingMergesWindowsAndLocalUsage(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	weeklyStart := now.Add(-48 * time.Hour).Format(time.RFC3339)
	weeklyEnd := now.Add(5 * 24 * time.Hour).Format(time.RFC3339)
	monthlyStart := now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	monthlyEnd := now.Add(23 * 24 * time.Hour).Format(time.RFC3339)
	account := newGrokBillingTestAccount(71)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{71: account}}}
	usageRepo := &grokBillingUsageRepo{stats: &usagestats.AccountStats{Requests: 9, Tokens: 123, Cost: 1.25}}
	upstream := newGrokBillingJSONUpstream(weeklyStart, weeklyEnd, monthlyStart, monthlyEnd)
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil, usageRepo)

	result, err := svc.ProbeBilling(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "billing_probe", result.Source)
	require.True(t, result.Persisted)
	require.Equal(t, http.StatusOK, result.StatusCode)
	require.NotNil(t, result.Billing)
	require.Equal(t, "weekly", result.Billing.PeriodType)
	require.Equal(t, "SuperGrok", result.Billing.Plan)
	require.InDelta(t, 12.5, *result.Billing.UsagePercent, 1e-9)
	require.InDelta(t, 10, *result.Billing.UsedPercent, 1e-9)
	require.Nil(t, result.LocalUsage24h)
	require.EqualValues(t, 9, result.LocalUsage7d.Requests)
	require.EqualValues(t, 9, result.LocalUsageMonthly.Requests)
	require.Len(t, usageRepo.windowStarts(), 2)
	require.NotNil(t, repo.updates[account.ID][grokBillingExtraKey])
	require.Equal(t, 1, upstream.callCount("/v1/billing?format=credits"))
	require.Equal(t, 1, upstream.callCount("/v1/billing"))
	require.Zero(t, repo.rateLimitedCalls)
}

func TestGrokQuotaServiceQueryQuotaUsesBillingWithoutModelProbeForPaidPlan(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := newGrokBillingTestAccount(72)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{72: account}}}
	upstream := newGrokBillingJSONUpstream(
		now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(7*24*time.Hour).Format(time.RFC3339),
		now.Add(-24*time.Hour).Format(time.RFC3339),
		now.Add(29*24*time.Hour).Format(time.RFC3339),
	)
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	result, err := svc.QueryQuota(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "billing_probe", result.Source)
	require.NotNil(t, result.Billing)
	require.Zero(t, upstream.callCount("/v1/responses"))
}

func TestGrokQuotaServiceQueryQuotaAddsActiveProbeForFreeAccount(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := newGrokBillingTestAccount(73)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{73: account}}}
	upstream := &grokBillingTestUpstream{handler: func(req *http.Request) *http.Response {
		switch {
		case req.URL.Path == "/v1/billing" && req.URL.RawQuery == "format=credits":
			return grokBillingResponse(http.StatusOK, fmt.Sprintf(`{"config":{"currentPeriod":{"type":"WEEKLY","start":%q,"end":%q}}}`, now.Add(-time.Hour).Format(time.RFC3339), now.Add(7*24*time.Hour).Format(time.RFC3339)))
		case req.URL.Path == "/v1/billing":
			return grokBillingResponse(http.StatusOK, `{"config":{}}`)
		case req.URL.Path == "/v1/responses":
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"X-Ratelimit-Limit-Requests":     []string{"20"},
					"X-Ratelimit-Remaining-Requests": []string{"19"},
				},
				Body: io.NopCloser(strings.NewReader(`{"id":"probe"}`)),
			}
		default:
			return grokBillingResponse(http.StatusNotFound, `{}`)
		}
	}}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	result, err := svc.QueryQuota(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, "hybrid_probe", result.Source)
	require.NotNil(t, result.Billing)
	require.NotNil(t, result.Snapshot)
	require.True(t, result.HeadersObserved)
	require.Equal(t, 1, upstream.callCount("/v1/responses"))
}

func TestGrokBillingHasAuthoritativeQuotaDoesNotTrustPlanNameAlone(t *testing.T) {
	t.Parallel()

	require.False(t, grokBillingHasAuthoritativeQuota(&xai.BillingSummary{Plan: "SuperGrok"}))
	require.True(t, grokBillingHasAuthoritativeQuota(&xai.BillingSummary{
		Plan: "SuperGrok",
		ProductUsage: []xai.BillingProductSummary{{
			Product:      "Api",
			UsagePercent: grokBillingFloatPointer(12.5),
		}},
	}))
}

func TestGrokQuotaServiceBilling429DoesNotPauseScheduling(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := newGrokBillingTestAccount(74)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{74: account}}}
	upstream := &grokBillingTestUpstream{handler: func(req *http.Request) *http.Response {
		if req.URL.RawQuery == "format=credits" {
			return grokBillingResponse(http.StatusTooManyRequests, `{"error":"rate limited"}`)
		}
		return grokBillingResponse(http.StatusOK, fmt.Sprintf(`{"config":{"monthlyLimit":{"val":15000},"used":{"val":100},"billingPeriodStart":%q,"billingPeriodEnd":%q}}`, now.Add(-time.Hour).Format(time.RFC3339), now.Add(30*24*time.Hour).Format(time.RFC3339)))
	}}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), account.ID)
	require.NoError(t, err)
	require.True(t, result.Billing.Partial)
	require.Equal(t, []string{"weekly"}, result.Billing.FailedWindows)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
}

func TestGrokQuotaServicePartialBilling403PersistsMediaIneligibility(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := newGrokBillingTestAccount(741)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
	upstream := &grokBillingTestUpstream{handler: func(req *http.Request) *http.Response {
		if req.URL.RawQuery == "format=credits" {
			return grokBillingResponse(http.StatusForbidden, `{"error":"media entitlement unavailable"}`)
		}
		return grokBillingResponse(http.StatusOK, fmt.Sprintf(`{"config":{"monthlyLimit":{"val":15000},"used":{"val":100},"billingPeriodStart":%q,"billingPeriodEnd":%q}}`, now.Add(-time.Hour).Format(time.RFC3339), now.Add(30*24*time.Hour).Format(time.RFC3339)))
	}}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), account.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusForbidden, result.Billing.WeeklyStatusCode)
	require.Equal(t, http.StatusOK, result.Billing.MonthlyStatusCode)
	require.True(t, result.Billing.Partial)

	snapshot, ok := repo.updates[account.ID][GrokMediaEligibilitySnapshotExtraKey].(*GrokMediaEligibilitySnapshot)
	require.True(t, ok)
	require.NotNil(t, snapshot.Eligible)
	require.False(t, *snapshot.Eligible)
	require.False(t, snapshot.Stale)
	account.Extra = map[string]any{GrokMediaEligibilitySnapshotExtraKey: snapshot}
	eligible, reason := account.GrokMediaGenerationEligibility()
	require.False(t, eligible)
	require.Equal(t, "billing_forbidden", reason)
}

func TestGrokQuotaServiceDoubleFailurePreservesLastMediaEligibilityAsStale(t *testing.T) {
	t.Parallel()
	account := newGrokBillingTestAccount(742)
	observedAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	account.Extra = map[string]any{
		grokBillingExtraKey: &xai.BillingSummary{
			StatusCode:        http.StatusOK,
			WeeklyStatusCode:  http.StatusOK,
			MonthlyStatusCode: http.StatusOK,
			UpdatedAt:         observedAt,
		},
		GrokMediaEligibilitySnapshotExtraKey: &GrokMediaEligibilitySnapshot{
			Eligible:          boolPointer(true),
			Source:            "billing_probe",
			ObservedAt:        observedAt,
			ProbedAt:          observedAt,
			WeeklyStatusCode:  http.StatusOK,
			MonthlyStatusCode: http.StatusOK,
		},
	}
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
	upstream := &grokBillingTestUpstream{handler: func(req *http.Request) *http.Response {
		if req.URL.RawQuery == "format=credits" {
			return grokBillingResponse(http.StatusTooManyRequests, `{"error":"limited"}`)
		}
		return grokBillingResponse(http.StatusServiceUnavailable, `{"error":"down"}`)
	}}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), account.ID)
	require.Error(t, err)
	require.Nil(t, result)
	snapshot, ok := repo.updates[account.ID][GrokMediaEligibilitySnapshotExtraKey].(*GrokMediaEligibilitySnapshot)
	require.True(t, ok)
	require.NotNil(t, snapshot.Eligible)
	require.True(t, *snapshot.Eligible)
	require.True(t, snapshot.Stale)
	require.Equal(t, observedAt, snapshot.ObservedAt)
	require.Equal(t, http.StatusTooManyRequests, snapshot.WeeklyStatusCode)
	require.Equal(t, http.StatusServiceUnavailable, snapshot.MonthlyStatusCode)
	billing, ok := repo.updates[account.ID][grokBillingExtraKey].(*xai.BillingSummary)
	require.True(t, ok)
	require.NotNil(t, billing)
	require.Equal(t, http.StatusTooManyRequests, billing.WeeklyStatusCode)
	require.Equal(t, http.StatusServiceUnavailable, billing.MonthlyStatusCode)
	require.True(t, billing.Partial)
	require.Zero(t, repo.rateLimitedCalls)
}

func TestGrokQuotaServiceProbeBillingRetainsPreviousFailedWindow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := newGrokBillingTestAccount(75)
	account.Extra = map[string]any{
		grokBillingExtraKey: &xai.BillingSummary{
			PeriodType:      "weekly",
			UsagePercent:    grokBillingFloatPointer(44),
			PeriodStart:     now.Add(-time.Hour).Format(time.RFC3339),
			PeriodEnd:       now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
			WeeklyUpdatedAt: now.Add(-time.Hour).Format(time.RFC3339),
		},
	}
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{75: account}}}
	upstream := &grokBillingTestUpstream{handler: func(req *http.Request) *http.Response {
		if req.URL.RawQuery == "format=credits" {
			return grokBillingResponse(http.StatusServiceUnavailable, `{"error":"down"}`)
		}
		return grokBillingResponse(http.StatusOK, fmt.Sprintf(`{"config":{"monthlyLimit":{"val":15000},"used":{"val":1500},"billingPeriodStart":%q,"billingPeriodEnd":%q}}`, now.Add(-24*time.Hour).Format(time.RFC3339), now.Add(29*24*time.Hour).Format(time.RFC3339)))
	}}
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	result, err := svc.ProbeBilling(context.Background(), account.ID)
	require.NoError(t, err)
	require.InDelta(t, 44, *result.Billing.UsagePercent, 1e-9)
	require.InDelta(t, 10, *result.Billing.UsedPercent, 1e-9)
	require.Equal(t, []string{"weekly"}, result.Billing.FailedWindows)
}

func TestGrokQuotaServiceProbeBillingSingleflight(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	account := newGrokBillingTestAccount(76)
	repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{76: account}}}
	upstream := newGrokBillingJSONUpstream(
		now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(7*24*time.Hour).Format(time.RFC3339),
		now.Add(-24*time.Hour).Format(time.RFC3339),
		now.Add(29*24*time.Hour).Format(time.RFC3339),
	)
	upstream.delay = 40 * time.Millisecond
	svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)

	const callers = 8
	var waitGroup sync.WaitGroup
	errors := make(chan error, callers)
	waitGroup.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer waitGroup.Done()
			_, err := svc.ProbeBilling(context.Background(), account.ID)
			errors <- err
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 1, upstream.callCount("/v1/billing?format=credits"))
	require.Equal(t, 1, upstream.callCount("/v1/billing"))
}

func TestGrokLocalUsageForQuotaUsesRolling24HoursForFreeAccount(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	repo := &grokBillingUsageRepo{stats: &usagestats.AccountStats{Requests: 3}}

	usage24h, usage7d, usageMonthly := grokLocalUsageForQuota(context.Background(), repo, 77, nil, now)
	require.EqualValues(t, 3, usage24h.Requests)
	require.Nil(t, usage7d)
	require.Nil(t, usageMonthly)
	starts := repo.windowStarts()
	require.Len(t, starts, 1)
	require.WithinDuration(t, now.Add(-24*time.Hour), starts[0], time.Second)
}

func newGrokBillingTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 2,
		Credentials: map[string]any{
			"access_token": "access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func newGrokBillingJSONUpstream(weeklyStart, weeklyEnd, monthlyStart, monthlyEnd string) *grokBillingTestUpstream {
	return &grokBillingTestUpstream{handler: func(req *http.Request) *http.Response {
		if req.URL.RawQuery == "format=credits" {
			return grokBillingResponse(http.StatusOK, fmt.Sprintf(`{"config":{"currentPeriod":{"type":"WEEKLY","start":%q,"end":%q},"creditUsagePercent":12.5,"productUsage":[{"product":"Api","usagePercent":12.5}]}}`, weeklyStart, weeklyEnd))
		}
		return grokBillingResponse(http.StatusOK, fmt.Sprintf(`{"config":{"monthlyLimit":{"val":15000},"used":{"val":1500},"billingPeriodStart":%q,"billingPeriodEnd":%q}}`, monthlyStart, monthlyEnd))
	}}
}

func grokBillingResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func grokBillingFloatPointer(value float64) *float64 {
	return &value
}
