package gatewaylite

import (
	"context"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestCanonicalRequestIDFromContextUsesReservationIdentity(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-header-id")
	ctx = ContextWithRequestMeta(ctx, RequestMeta{RequestID: "client:reserved-id", UserID: 42})

	requestID, ok := CanonicalRequestIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "client:reserved-id", requestID)
}

func TestCanonicalRequestIDFromContextPrefixesFallbackSources(t *testing.T) {
	clientCtx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-id")
	requestID, ok := CanonicalRequestIDFromContext(clientCtx)
	require.True(t, ok)
	require.Equal(t, "client:client-id", requestID)

	localCtx := context.WithValue(context.Background(), ctxkey.RequestID, "local-id")
	requestID, ok = CanonicalRequestIDFromContext(localCtx)
	require.True(t, ok)
	require.Equal(t, "local:local-id", requestID)

	requestID, ok = CanonicalRequestIDFromContext(context.Background())
	require.False(t, ok)
	require.Empty(t, requestID)
}

func TestContextWithEffectiveGroupPreservesRequestedGroup(t *testing.T) {
	requestedID := int64(10)
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID:         "req-group-fallback",
		KeyID:             "key-group-fallback",
		UserID:            42,
		GroupID:           requestedID,
		GroupName:         "请求分组",
		OriginalGroupID:   requestedID,
		OriginalGroupName: "请求分组",
	})

	ctx = ContextWithEffectiveGroup(ctx, 20, "最终分组")
	meta, ok := RequestMetaFromContext(ctx)
	require.True(t, ok)
	require.EqualValues(t, 20, meta.GroupID)
	require.Equal(t, "最终分组", meta.GroupName)
	require.EqualValues(t, requestedID, meta.OriginalGroupID)
	require.Equal(t, "请求分组", meta.OriginalGroupName)
	ingressGroupID, ok := IngressGroupIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, requestedID, ingressGroupID)
}

func TestContextWithIngressGroupPreservesFirstValidGroupWithoutRequestMeta(t *testing.T) {
	ctx := ContextWithIngressGroup(context.Background(), 7)
	ctx = ContextWithIngressGroup(ctx, 8)

	groupID, ok := IngressGroupIDFromContext(ctx)
	require.True(t, ok)
	require.EqualValues(t, 7, groupID)
	_, hasMeta := RequestMetaFromContext(ctx)
	require.False(t, hasMeta)
}

func TestActualCostToCentsForContextAccumulatesSubCentByKey(t *testing.T) {
	resetActualCostLocalRemaindersForTest()

	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID: "req-1",
		KeyID:     "key-a",
		UserID:    42,
		Region:    "sg",
	})
	otherKeyCtx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID: "req-2",
		KeyID:     "key-b",
		UserID:    42,
		Region:    "sg",
	})

	require.EqualValues(t, 0, ActualCostToCentsForContext(ctx, 0.004))
	require.EqualValues(t, 1, ActualCostToCentsForContext(ctx, 0.006))
	require.EqualValues(t, 0, ActualCostToCentsForContext(otherKeyCtx, 0.006))
	require.EqualValues(t, 1, ActualCostToCentsForContext(otherKeyCtx, 0.004))
}

func TestActualCostToCentsForContextUsesOneRoundingAndCarryRule(t *testing.T) {
	resetActualCostLocalRemaindersForTest()

	tests := []struct {
		name      string
		keyID     string
		costs     []float64
		wantCents []int64
	}{
		{name: "0.004 carries 0.4 cent", keyID: "key-004", costs: []float64{0.004, 0.006}, wantCents: []int64{0, 1}},
		{name: "0.005 carries half cent", keyID: "key-005", costs: []float64{0.005, 0.005}, wantCents: []int64{0, 1}},
		{name: "0.014 emits one cent and carries 0.4", keyID: "key-014", costs: []float64{0.014, 0.006}, wantCents: []int64{1, 1}},
		{name: "0.015 emits one cent and carries half", keyID: "key-015", costs: []float64{0.015, 0.005}, wantCents: []int64{1, 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
				RequestID: tt.name,
				KeyID:     tt.keyID,
				UserID:    42,
				Region:    "sg",
			})
			for i, cost := range tt.costs {
				require.EqualValues(t, tt.wantCents[i], ActualCostToCentsForContext(ctx, cost), "cost[%d]=%v", i, cost)
			}
		})
	}
}

func TestActualCostToCentsForContextLegacyKeepsDuplicateAdvance(t *testing.T) {
	resetActualCostLocalRemaindersForTest()
	SetDefaultRedisQuota(NewRedisQuota(nil, ""))
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID:             "legacy-duplicate",
		KeyID:                 "key-legacy",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: BillingSettlementModeLegacy,
	})
	require.EqualValues(t, 1, ActualCostToCentsForContext(ctx, 0.015))
	require.EqualValues(t, 2, ActualCostToCentsForContext(ctx, 0.015))
}

func TestContextWithRequestMetaPreservesExplicitZeroFrozenSalesRate(t *testing.T) {
	zero := 0.0
	policy := RatePolicyAudit{
		SchemaVersion: 1, BillingScope: "token", GroupRateMultiplier: 1.5,
		UserRateMultiplier: &zero, ResolvedRateMultiplier: 0,
		PeakRateEnabled: true, PeakRateMultiplier: 2, AppliedPeakMultiplier: 2, EffectiveRateMultiplier: 0,
		Timezone: "UTC", ResolvedAtMillis: 1_735_689_600_000, Version: "v1:0123456789abcdef",
	}
	require.True(t, policy.Valid())
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID: "explicit-zero-rate", UserID: 42, RateMultiplier: 1.5, RatePolicyAudit: &policy,
	})
	meta, ok := RequestMetaFromContext(ctx)
	require.True(t, ok)
	require.Zero(t, meta.RateMultiplier)
	require.NotNil(t, meta.RatePolicyAudit)
	require.Zero(t, meta.RatePolicyAudit.EffectiveRateMultiplier)
}

func TestContextWithUpstreamConsumedPreservesRequestMeta(t *testing.T) {
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID: "stt-post-audit", UserID: 42,
	})
	ctx = ContextWithUpstreamConsumed(ctx)

	meta, ok := RequestMetaFromContext(ctx)
	require.True(t, ok)
	require.True(t, meta.UpstreamConsumed)
}

func TestSalesRateReservationCentsUsesFrozenEffectiveMultiplier(t *testing.T) {
	tests := []struct {
		name string
		rate float64
		want int64
	}{
		{name: "high multiplier rounds up", rate: 3.01, want: 4},
		{name: "integer high multiplier", rate: 8, want: 8},
		{name: "low multiplier keeps safe minimum", rate: 0.25, want: 1},
		{name: "explicit free multiplier keeps refundable placeholder", rate: 0, want: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, SalesRateReservationCents(test.rate))
		})
	}
}

func TestPrepareActualCostSettlementLocalReusesFirstResult(t *testing.T) {
	resetActualCostLocalRemaindersForTest()
	SetDefaultRedisQuota(NewRedisQuota(nil, ""))
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID:             "v2-retry",
		KeyID:                 "key-v2",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: BillingSettlementModeIdempotentV2,
	})
	for i := 0; i < 100; i++ {
		cents, err := PrepareActualCostSettlement(ctx, "v2-retry", 0.015)
		require.NoError(t, err)
		require.EqualValues(t, 1, cents, "retry %d", i)
	}
	cents, err := PrepareActualCostSettlement(ctx, "v2-retry", 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, cents, "an empty retry must reuse the frozen result")
	next := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID:             "v2-next",
		KeyID:                 "key-v2",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: BillingSettlementModeIdempotentV2,
	})
	cents, err = PrepareActualCostSettlement(next, "v2-next", 0.005)
	require.NoError(t, err)
	require.EqualValues(t, 1, cents)
}

func TestPrepareActualCostSettlementLocalConcurrent(t *testing.T) {
	resetActualCostLocalRemaindersForTest()
	SetDefaultRedisQuota(NewRedisQuota(nil, ""))
	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
		RequestID:             "v2-concurrent",
		KeyID:                 "key-concurrent",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: BillingSettlementModeIdempotentV2,
	})
	const workers = 1000
	type result struct {
		cents int64
		err   error
	}
	results := make(chan result, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			cents, err := PrepareActualCostSettlement(ctx, "v2-concurrent", 0.015)
			results <- result{cents: cents, err: err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.EqualValues(t, 1, result.cents)
	}
}

func TestPrepareActualCostSettlementLocalScopesRequestIDByKey(t *testing.T) {
	resetActualCostLocalRemaindersForTest()
	SetDefaultRedisQuota(NewRedisQuota(nil, ""))
	for _, keyID := range []string{"key-a", "key-b"} {
		ctx := ContextWithRequestMeta(context.Background(), RequestMeta{
			RequestID:             "shared-request-id",
			KeyID:                 keyID,
			UserID:                42,
			Region:                "sg",
			BillingSettlementMode: BillingSettlementModeIdempotentV2,
		})
		cents, err := PrepareActualCostSettlement(ctx, "shared-request-id", 0.015)
		require.NoError(t, err)
		require.EqualValues(t, 1, cents)
	}
}

func TestPrepareActualCostSettlementLocalCapacityFailsClosed(t *testing.T) {
	resetActualCostLocalRemaindersForTest()
	SetDefaultRedisQuota(NewRedisQuota(nil, ""))
	now := time.Now()
	actualCostLocalRemainders.Lock()
	for i := 0; i < actualCostLocalSettlementMaxEntries; i++ {
		actualCostLocalRemainders.settlements[strconv.Itoa(i)] = actualCostLocalSettlementResult{
			cents:     1,
			expiresAt: now.Add(time.Hour).Unix(),
		}
	}
	actualCostLocalRemainders.Unlock()

	ctx := ContextWithRequestMeta(context.Background(), RequestMeta{KeyID: "full", UserID: 42, Region: "sg"})
	_, err := PrepareActualCostSettlement(ctx, "new-request", 0.015)
	require.ErrorContains(t, err, "capacity exhausted")
	actualCostLocalRemainders.Lock()
	require.Len(t, actualCostLocalRemainders.settlements, actualCostLocalSettlementMaxEntries)
	actualCostLocalRemainders.Unlock()
}

func TestActualCostUSDToMicroCentsRejectsInvalidValues(t *testing.T) {
	require.Zero(t, actualCostUSDToMicroCents(0))
	require.Zero(t, actualCostUSDToMicroCents(-1))
	require.Zero(t, actualCostUSDToMicroCents(math.NaN()))
	require.Zero(t, actualCostUSDToMicroCents(math.Inf(1)))
	require.EqualValues(t, 400000, actualCostUSDToMicroCents(0.004))
	require.EqualValues(t, 500000, actualCostUSDToMicroCents(0.005))
	require.EqualValues(t, 1400000, actualCostUSDToMicroCents(0.014))
	require.EqualValues(t, 1500000, actualCostUSDToMicroCents(0.015))
}

func resetActualCostLocalRemaindersForTest() {
	actualCostLocalRemainders.Lock()
	actualCostLocalRemainders.values = map[string]int64{}
	actualCostLocalRemainders.settlements = map[string]actualCostLocalSettlementResult{}
	actualCostLocalRemainders.nextSweepUnix = 0
	actualCostLocalRemainders.Unlock()
}
