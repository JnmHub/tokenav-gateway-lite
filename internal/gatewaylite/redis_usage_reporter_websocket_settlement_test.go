package gatewaylite

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisUsageReporterWebSocketTurnsConvergeAcrossBaseOrder(t *testing.T) {
	tests := []struct {
		name             string
		turnsBeforeBase  bool
		wantRequestOrder []string
		wantResolved     []int64
		wantBaseResult   int64
	}{
		{
			name:             "base_then_three_turns",
			wantRequestOrder: []string{"ws-connection", "resp_1", "resp_2", "resp_3"},
			wantResolved:     []int64{0, 0, 1, 1},
		},
		{
			name:             "three_turns_then_base",
			turnsBeforeBase:  true,
			wantRequestOrder: []string{"resp_1", "resp_2", "resp_3", "ws-connection"},
			wantResolved:     []int64{0, 1, 1, 0},
			wantBaseResult:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })

			prefix := "ws-settlement-" + tt.name
			quota := NewRedisQuota(client, prefix)
			reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
			const (
				connectionID = "ws-connection"
				leaseID      = "lease-websocket"
				keyID        = "key-websocket"
			)
			require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
				LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
				Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
			}))
			reserved, err := quota.Reserve(ctx, ReserveRequest{
				RequestID: connectionID, KeyID: keyID, UserID: 42, Region: "sg",
				LeaseID: leaseID, EstimatedCostCents: 1,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			base := webSocketBaseUsageEvent(connectionID, keyID)
			turns := []UsageEvent{
				webSocketTurnUsageEvent("resp_1", connectionID, keyID, 600_000),
				webSocketTurnUsageEvent("resp_2", connectionID, keyID, 600_000),
				webSocketTurnUsageEvent("resp_3", connectionID, keyID, 900_000),
			}
			commitBase := func() {
				resolved, commitErr := reporter.CommitAndReportUsage(ctx, quota, base)
				require.NoError(t, commitErr)
				require.Equal(t, tt.wantBaseResult, resolved)
			}
			reportTurns := func() {
				for _, turn := range turns {
					require.NoError(t, reporter.ReportUsage(ctx, turn))
				}
			}

			if tt.turnsBeforeBase {
				reportTurns()
				commitBase()
			} else {
				commitBase()
				reportTurns()
			}

			assertWebSocketQuotaTotals(t, ctx, client, quota, connectionID, 2)
			assertWebSocketTurnSettlements(t, ctx, client, quota, []struct {
				requestID string
				micro     int64
				cents     int64
			}{
				{requestID: "resp_1", micro: 600_000, cents: 0},
				{requestID: "resp_2", micro: 600_000, cents: 1},
				{requestID: "resp_3", micro: 900_000, cents: 1},
			})

			remainder, err := client.Get(ctx, quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(turns[0]))).Int64()
			require.NoError(t, err)
			require.EqualValues(t, 100_000, remainder)

			entries := webSocketSettlementStreamEntries(t, client, reporter.streamKey())
			require.Len(t, entries, 4)
			gotRequestOrder := make([]string, 0, len(entries))
			gotResolved := make([]int64, 0, len(entries))
			for _, entry := range entries {
				gotRequestOrder = append(gotRequestOrder, entry.requestID)
				gotResolved = append(gotResolved, entry.resolvedCents)
				if entry.requestID == connectionID {
					// The local commit may resolve the aggregate to two cents, but the
					// control-plane event must stay zero because resp_* already reports it.
					require.Zero(t, entry.resolvedCents)
					require.Zero(t, entry.bodyActualCents)
				}
			}
			require.Equal(t, tt.wantRequestOrder, gotRequestOrder)
			require.Equal(t, tt.wantResolved, gotResolved)
		})
	}
}

func TestRedisUsageReporterWebSocketConcurrentTurnAdvancesQuotaAndStreamOnce(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const (
		prefix       = "ws-settlement-concurrent"
		connectionID = "ws-connection-concurrent"
		leaseID      = "lease-websocket-concurrent"
		keyID        = "key-websocket-concurrent"
		concurrency  = 1000
	)
	quota := NewRedisQuota(client, prefix)
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	reserved, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: connectionID, KeyID: keyID, UserID: 42, Region: "sg",
		LeaseID: leaseID, EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	base := webSocketBaseUsageEvent(connectionID, keyID)
	resolved, err := reporter.CommitAndReportUsage(ctx, quota, base)
	require.NoError(t, err)
	require.Zero(t, resolved)

	turn := webSocketTurnUsageEvent("resp_concurrent", connectionID, keyID, 1_500_000)
	start := make(chan struct{})
	errs := make(chan error, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			<-start
			errs <- reporter.ReportUsage(ctx, turn)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for reportErr := range errs {
		require.NoError(t, reportErr)
	}

	// A retry without a recoverable local amount still reuses the frozen result.
	zeroReplay := turn
	zeroReplay.SettlementMicroCents = 0
	require.NoError(t, reporter.ReportUsage(ctx, zeroReplay))

	assertWebSocketQuotaTotals(t, ctx, client, quota, connectionID, 1)
	assertWebSocketTurnSettlements(t, ctx, client, quota, []struct {
		requestID string
		micro     int64
		cents     int64
	}{{requestID: turn.RequestID, micro: 1_500_000, cents: 1}})
	remainder, err := client.Get(ctx, quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(turn))).Int64()
	require.NoError(t, err)
	require.EqualValues(t, 500_000, remainder)

	entries := webSocketSettlementStreamEntries(t, client, reporter.streamKey())
	require.Equal(t, []webSocketSettlementStreamEntry{
		{requestID: connectionID, resolvedCents: 0, bodyActualCents: 0},
		{requestID: turn.RequestID, resolvedCents: 1, bodyActualCents: 0},
	}, entries)
}

func webSocketBaseUsageEvent(connectionID, keyID string) UsageEvent {
	return UsageEvent{
		RequestID: connectionID, UserID: 42, KeyID: keyID, Region: "sg", GatewayID: "sg",
		SettlementMode:     string(BillingSettlementModeIdempotentV2),
		ActualCents:        0,
		SuppressBaseCharge: true,
	}
}

func webSocketTurnUsageEvent(requestID, connectionID, keyID string, microCents int64) UsageEvent {
	event := settlementUsageEvent(requestID, connectionID, microCents)
	event.KeyID = keyID
	return event
}

func assertWebSocketQuotaTotals(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	quota *RedisQuota,
	connectionID string,
	wantCents int64,
) {
	t.Helper()
	request, err := client.HGetAll(ctx, quota.requestKey(connectionID)).Result()
	require.NoError(t, err)
	require.Equal(t, "committed", request["status"])
	require.Equal(t, strconv.FormatInt(wantCents, 10), request["actual_cents"])
	require.Equal(t, strconv.FormatInt(wantCents, 10), request["child_actual_cents"])
	require.Equal(t, "1", request["has_child_settlements"])

	lease, err := client.HGetAll(ctx, quota.leaseKey(42, "sg")).Result()
	require.NoError(t, err)
	require.Zero(t, ParseInt64Field(lease, "reserved_cents"))
	require.Equal(t, wantCents, ParseInt64Field(lease, "spent_cents"))
}

func assertWebSocketTurnSettlements(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	quota *RedisQuota,
	wants []struct {
		requestID string
		micro     int64
		cents     int64
	},
) {
	t.Helper()
	for _, want := range wants {
		request, err := client.HGetAll(ctx, quota.requestKey(want.requestID)).Result()
		require.NoError(t, err)
		require.Equal(t, strconv.FormatInt(want.micro, 10), request["settlement_micro_cents"])
		require.Equal(t, strconv.FormatInt(want.cents, 10), request["settlement_actual_cents"])
		require.Equal(t, "1", request["settlement_quota_applied"])
		require.Equal(t, "1", request["settlement_usage_enqueued"])
	}
}

type webSocketSettlementStreamEntry struct {
	requestID       string
	resolvedCents   int64
	bodyActualCents int64
}

func webSocketSettlementStreamEntries(t *testing.T, client *redis.Client, streamKey string) []webSocketSettlementStreamEntry {
	t.Helper()
	messages, err := client.XRange(context.Background(), streamKey, "-", "+").Result()
	require.NoError(t, err)
	entries := make([]webSocketSettlementStreamEntry, 0, len(messages))
	for _, message := range messages {
		raw, ok := message.Values["event"].(string)
		require.True(t, ok)
		var queued queuedUsageEvent
		require.NoError(t, json.Unmarshal([]byte(raw), &queued))
		resolved, parseErr := strconv.ParseInt(fmt.Sprint(message.Values["resolved_actual_cents"]), 10, 64)
		require.NoError(t, parseErr)
		entries = append(entries, webSocketSettlementStreamEntry{
			requestID:       queued.Event.RequestID,
			resolvedCents:   resolved,
			bodyActualCents: queued.Event.ActualCents,
		})
	}
	return entries
}
