package gatewaylite

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisUsageReporterIdempotentSettlementConcurrentReplaysAdvanceOnce(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const (
		prefix      = "settlement-concurrent"
		concurrency = 1000
	)
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	event := settlementUsageEvent("req-concurrent", "", 1_500_000)

	start := make(chan struct{})
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			<-start
			errs <- reporter.ReportUsage(ctx, event)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	quota := NewRedisQuota(client, prefix)
	scope := actualCostAccumulatorScopeForUsageEvent(event)
	remainder, err := client.Get(ctx, quota.actualCostRemainderKey(scope)).Int64()
	require.NoError(t, err)
	require.EqualValues(t, 500_000, remainder)

	request, err := client.HGetAll(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1500000", request["settlement_micro_cents"])
	require.Equal(t, "1", request["settlement_actual_cents"])
	require.Equal(t, "1", request["settlement_usage_enqueued"])
	require.Equal(t, []int64{1}, settlementStreamResolvedCents(t, client, reporter.streamKey()))
}

func TestRedisUsageReporterIdempotentSettlementZeroReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const prefix = "settlement-replay"
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	first := settlementUsageEvent("req-replay", "", 1_500_000)
	require.NoError(t, reporter.ReportUsage(ctx, first))

	// A retry that lost its local cost must recover the frozen first result. It
	// must neither enqueue a second event nor move the fractional carry again.
	zeroReplay := first
	zeroReplay.SettlementMicroCents = 0
	require.NoError(t, reporter.ReportUsage(ctx, zeroReplay))

	quota := NewRedisQuota(client, prefix)
	scope := actualCostAccumulatorScopeForUsageEvent(first)
	remainderKey := quota.actualCostRemainderKey(scope)
	requestKey := quota.requestKey(first.RequestID)
	remainderBeforeConflict, err := client.Get(ctx, remainderKey).Result()
	require.NoError(t, err)
	requestBeforeConflict, err := client.HGetAll(ctx, requestKey).Result()
	require.NoError(t, err)
	require.Equal(t, []int64{1}, settlementStreamResolvedCents(t, client, reporter.streamKey()))

	conflict := first
	conflict.SettlementMicroCents = 2_000_000
	err = reporter.ReportUsage(ctx, conflict)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SETTLEMENT_CONFLICT")

	remainderAfterConflict, getErr := client.Get(ctx, remainderKey).Result()
	require.NoError(t, getErr)
	require.Equal(t, remainderBeforeConflict, remainderAfterConflict)
	requestAfterConflict, getErr := client.HGetAll(ctx, requestKey).Result()
	require.NoError(t, getErr)
	require.Equal(t, requestBeforeConflict, requestAfterConflict)
	require.Equal(t, []int64{1}, settlementStreamResolvedCents(t, client, reporter.streamKey()))
}

func TestRedisUsageReporterBaseAndCorrectionOrdersConverge(t *testing.T) {
	tests := []struct {
		name            string
		correctionFirst bool
		wantStream      []int64
	}{
		{name: "base_then_correction", wantStream: []int64{1, 9}},
		{name: "correction_then_base", correctionFirst: true, wantStream: []int64{9, 9}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })

			prefix := "settlement-order-" + strings.ReplaceAll(tt.name, "_", "-")
			quota := NewRedisQuota(client, prefix)
			reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
			const (
				requestID = "req-order"
				leaseID   = "lease-order"
			)
			require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
				LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
				Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
			}))
			reserved, err := quota.Reserve(ctx, ReserveRequest{
				RequestID: requestID, KeyID: "key-order", UserID: 42, Region: "sg",
				LeaseID: leaseID, EstimatedCostCents: 1,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			base := UsageEvent{
				RequestID: requestID, UserID: 42, KeyID: "key-order", Region: "sg", GatewayID: "sg",
				ActualCents: 1, SettlementMode: string(BillingSettlementModeIdempotentV2),
			}
			correction := settlementUsageEvent(requestID, requestID, 9_500_000)
			correction.KeyID = base.KeyID

			if tt.correctionFirst {
				require.NoError(t, reporter.ReportUsage(ctx, correction))
				resolved, commitErr := reporter.CommitAndReportUsage(ctx, quota, base)
				require.NoError(t, commitErr)
				require.EqualValues(t, 9, resolved)
			} else {
				resolved, commitErr := reporter.CommitAndReportUsage(ctx, quota, base)
				require.NoError(t, commitErr)
				require.EqualValues(t, 1, resolved)
				require.NoError(t, reporter.ReportUsage(ctx, correction))
			}

			request, err := client.HGetAll(ctx, quota.requestKey(requestID)).Result()
			require.NoError(t, err)
			require.Equal(t, "committed", request["status"])
			require.Equal(t, "9", request["actual_cents"])
			require.Equal(t, "9", request["settlement_actual_cents"])

			lease, err := client.HGetAll(ctx, quota.leaseKey(42, "sg")).Result()
			require.NoError(t, err)
			require.Zero(t, ParseInt64Field(lease, "reserved_cents"))
			require.EqualValues(t, 9, ParseInt64Field(lease, "spent_cents"))
			require.Equal(t, tt.wantStream, settlementStreamResolvedCents(t, client, reporter.streamKey()))
		})
	}
}

func TestRedisUsageReporterSparseSettlementUsesRequestTTLAndNoSideKey(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const prefix = "settlement-sparse"
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	event := settlementUsageEvent("req-sparse", "", 500_000)
	require.NoError(t, reporter.ReportUsage(ctx, event))

	quota := NewRedisQuota(client, prefix)
	ttl, err := client.TTL(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
	require.LessOrEqual(t, ttl, reservationTTL)

	legacyKeys, err := client.Keys(ctx, prefix+":usage:actual_cost_settlement:*").Result()
	require.NoError(t, err)
	require.Empty(t, legacyKeys)
	request, err := client.HGetAll(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "500000", request["settlement_micro_cents"])
	require.Equal(t, "0", request["settlement_actual_cents"])
}

func TestRedisUsageReporterSettlementWrongTypesDoNotAdvanceMoney(t *testing.T) {
	tests := []struct {
		name       string
		poisonKey  func(*RedisQuota, *RedisUsageReporter, UsageEvent) string
		poisonKind string
	}{
		{
			name: "stream",
			poisonKey: func(_ *RedisQuota, reporter *RedisUsageReporter, _ UsageEvent) string {
				return reporter.streamKey()
			},
			poisonKind: "string",
		},
		{
			name: "settlement_request",
			poisonKey: func(quota *RedisQuota, _ *RedisUsageReporter, event UsageEvent) string {
				return quota.requestKey(event.RequestID)
			},
			poisonKind: "string",
		},
		{
			name: "quota_request",
			poisonKey: func(quota *RedisQuota, _ *RedisUsageReporter, event UsageEvent) string {
				return quota.requestKey(event.QuotaRequestID)
			},
			poisonKind: "string",
		},
		{
			name: "remainder",
			poisonKey: func(quota *RedisQuota, _ *RedisUsageReporter, event UsageEvent) string {
				return quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(event))
			},
			poisonKind: "hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })

			prefix := "settlement-wrongtype-" + strings.ReplaceAll(tt.name, "_", "-")
			quota := NewRedisQuota(client, prefix)
			reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
			require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
				LeaseID: "lease-wrongtype", UserID: 42, Region: "sg", AllocatedCents: 100,
				Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
			}))
			reserved, err := quota.Reserve(ctx, ReserveRequest{
				RequestID: "quota-request", KeyID: "key-wrongtype", UserID: 42, Region: "sg",
				LeaseID: "lease-wrongtype", EstimatedCostCents: 1,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			event := settlementUsageEvent("settlement-request", "quota-request", 1_500_000)
			event.KeyID = "key-wrongtype"
			remainderKey := quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(event))
			require.NoError(t, client.Set(ctx, remainderKey, 250_000, time.Hour).Err())

			poisonedKey := tt.poisonKey(quota, reporter, event)
			require.NoError(t, client.Del(ctx, poisonedKey).Err())
			switch tt.poisonKind {
			case "hash":
				require.NoError(t, client.HSet(ctx, poisonedKey, "sentinel", "unchanged").Err())
			default:
				require.NoError(t, client.Set(ctx, poisonedKey, "wrong-type", 0).Err())
			}

			err = reporter.ReportUsage(ctx, event)
			require.Error(t, err)
			require.Contains(t, err.Error(), "WRONGTYPE")

			lease, leaseErr := client.HGetAll(ctx, quota.leaseKey(42, "sg")).Result()
			require.NoError(t, leaseErr)
			require.EqualValues(t, 1, ParseInt64Field(lease, "reserved_cents"))
			require.Zero(t, ParseInt64Field(lease, "spent_cents"))

			if tt.name == "remainder" {
				sentinel, getErr := client.HGet(ctx, remainderKey, "sentinel").Result()
				require.NoError(t, getErr)
				require.Equal(t, "unchanged", sentinel)
			} else {
				remainder, getErr := client.Get(ctx, remainderKey).Int64()
				require.NoError(t, getErr)
				require.EqualValues(t, 250_000, remainder)
			}

			if tt.name == "stream" {
				value, getErr := client.Get(ctx, reporter.streamKey()).Result()
				require.NoError(t, getErr)
				require.Equal(t, "wrong-type", value)
			} else {
				require.Zero(t, mustStreamLength(t, client, reporter.streamKey()))
			}

			settlementType, typeErr := client.Type(ctx, quota.requestKey(event.RequestID)).Result()
			require.NoError(t, typeErr)
			if tt.name == "settlement_request" {
				require.Equal(t, "string", settlementType)
			} else {
				require.Equal(t, "none", settlementType)
			}
		})
	}
}

func settlementUsageEvent(requestID, quotaRequestID string, microCents int64) UsageEvent {
	return UsageEvent{
		RequestID: requestID, QuotaRequestID: quotaRequestID,
		UserID: 42, KeyID: "key-settlement", Region: "sg", GatewayID: "sg",
		SettlementMode:       string(BillingSettlementModeIdempotentV2),
		SettlementPending:    true,
		SettlementMicroCents: microCents,
	}
}

func settlementStreamResolvedCents(t *testing.T, client *redis.Client, streamKey string) []int64 {
	t.Helper()
	messages, err := client.XRange(context.Background(), streamKey, "-", "+").Result()
	require.NoError(t, err)
	resolved := make([]int64, 0, len(messages))
	for _, message := range messages {
		value, ok := message.Values["resolved_actual_cents"]
		require.True(t, ok, "stream message %s has no resolved_actual_cents", message.ID)
		cents, parseErr := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		require.NoError(t, parseErr)
		resolved = append(resolved, cents)
	}
	return resolved
}
