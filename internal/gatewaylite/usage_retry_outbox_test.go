package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type usageOutboxCommitSink struct {
	report func(context.Context, UsageEvent) error
	commit func(context.Context, *RedisQuota, UsageEvent) (int64, error)
}

func (s usageOutboxCommitSink) ReportUsage(ctx context.Context, event UsageEvent) error {
	if s.report == nil {
		return nil
	}
	return s.report(ctx, event)
}

func (s usageOutboxCommitSink) CommitAndReportUsage(ctx context.Context, quota *RedisQuota, event UsageEvent) (int64, error) {
	if s.commit == nil {
		return event.ActualCents, nil
	}
	return s.commit(ctx, quota, event)
}

func TestDurableUsageReporterSuccessfulHotPathDoesNotTouchDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))

	require.NoError(t, reporter.ReportUsage(context.Background(), UsageEvent{
		RequestID: "req-success", SettlementMode: string(BillingSettlementModeIdempotentV2),
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageReporterSuccessfulFinalizeHotPathDoesNotTouchDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, usageOutboxCommitSink{
		commit: func(context.Context, *RedisQuota, UsageEvent) (int64, error) { return 7, nil },
	})

	actual, err := reporter.CommitAndReportUsage(context.Background(), nil, UsageEvent{
		RequestID: "req-success-finalize", SettlementMode: string(BillingSettlementModeIdempotentV2), ActualCents: 1,
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, actual)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageReporterPersistsOnlyIdempotentFailure(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		wantStored bool
	}{
		{name: "idempotent v2", mode: string(BillingSettlementModeIdempotentV2), wantStored: true},
		{name: "legacy remains unchanged", mode: string(BillingSettlementModeLegacy)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			reporter := NewDurableUsageReporter(db, usageSinkFunc(func(context.Context, UsageEvent) error {
				return errors.New("redis unavailable")
			}))
			event := UsageEvent{
				RequestID: "req-failed", SettlementMode: tt.mode,
				SettlementPending: true, SettlementMicroCents: 1_500_000,
				QuotaRequestID: "quota-request", SuppressBaseCharge: true,
			}
			if tt.wantStored {
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_usage_retry_outbox")).
					WithArgs(event.RequestID, string(usageRetryOperationCorrection), sqlmock.AnyArg(), "redis unavailable").
					WillReturnResult(sqlmock.NewResult(1, 1))
				require.NoError(t, reporter.ReportUsage(context.Background(), event))
			} else {
				require.ErrorContains(t, reporter.ReportUsage(context.Background(), event), "redis unavailable")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDurableUsageReporterPersistsFinalizeIntentWithoutFallbackReport(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, usageOutboxCommitSink{
		commit: func(context.Context, *RedisQuota, UsageEvent) (int64, error) {
			return 0, errors.New("redis unavailable")
		},
	})
	event := UsageEvent{
		RequestID: "req-finalize-failed", SettlementMode: string(BillingSettlementModeIdempotentV2), ActualCents: 1,
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_usage_retry_outbox")).
		WithArgs(event.RequestID, string(usageRetryOperationFinalize), sqlmock.AnyArg(), "redis unavailable").
		WillReturnResult(sqlmock.NewResult(1, 1))

	actual, err := reporter.CommitAndReportUsage(context.Background(), nil, event)
	require.NoError(t, err)
	require.EqualValues(t, 1, actual)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageReporterDoesNotPersistPermanentSettlementFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, usageSinkFunc(func(context.Context, UsageEvent) error {
		return errors.New("WRONGTYPE usage stream key is not a stream")
	}))

	err = reporter.ReportUsage(context.Background(), UsageEvent{
		RequestID: "req-wrong-type", SettlementMode: string(BillingSettlementModeIdempotentV2),
	})
	require.ErrorContains(t, err, "WRONGTYPE")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageReporterRetryRestoresGatewayLocalSettlementFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var got UsageEvent
	reporter := NewDurableUsageReporter(db, usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		got = event
		return nil
	}))
	event := UsageEvent{
		RequestID: "req-retry", SettlementMode: string(BillingSettlementModeIdempotentV2),
		SettlementPending: true, SettlementMicroCents: 2_500_000,
		QuotaRequestID: "quota-retry", SuppressBaseCharge: true,
	}
	payload, err := json.Marshal(usageRetryOutboxEnvelope{
		Event: event, SettlementPending: event.SettlementPending,
		SettlementMicroCents: event.SettlementMicroCents,
		QuotaRequestID:       event.QuotaRequestID, SuppressBaseCharge: event.SuppressBaseCharge,
	})
	require.NoError(t, err)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(event.RequestID, string(usageRetryOperationReport), int64(7), "claim-retry").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, reporter.retryItem(context.Background(), usageRetryOutboxItem{
		requestID: event.RequestID, operationKind: usageRetryOperationReport,
		payload: payload, generation: 7, claimToken: "claim-retry",
	}))
	require.Equal(t, event.RequestID, got.RequestID)
	require.True(t, got.SettlementPending)
	require.EqualValues(t, 2_500_000, got.SettlementMicroCents)
	require.Equal(t, "quota-retry", got.QuotaRequestID)
	require.True(t, got.SuppressBaseCharge)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageFinalizeReplayAfterDeleteFailureDoesNotDuplicateStream(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const prefix = "outbox-delete-replay"
	quota := NewRedisQuota(client, prefix)
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-delete-replay", UserID: 42, Region: "sg", AllocatedCents: 100,
		SpentCents: 80, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(), RefillAtCents: 10,
	}))
	reserved, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-delete-replay", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-delete-replay", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	inner := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	var refillSignals atomic.Int32
	inner.postUsageLeaseRefill = func(*RedisQuota, leaseRefillSignal, string) { refillSignals.Add(1) }
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, inner)
	event := UsageEvent{
		RequestID: "req-delete-replay", UserID: 42, KeyID: "key", Region: "sg", GatewayID: "sg",
		ActualCents: 10, SettlementMode: string(BillingSettlementModeIdempotentV2),
	}
	payload, err := json.Marshal(usageRetryOutboxEnvelope{Event: event, RedisPrefix: prefix})
	require.NoError(t, err)

	first := usageRetryOutboxItem{
		requestID: event.RequestID, operationKind: usageRetryOperationFinalize,
		payload: payload, generation: 1, claimToken: "claim-first",
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(event.RequestID, string(usageRetryOperationFinalize), int64(1), first.claimToken).
		WillReturnError(errors.New("simulated outbox delete failure"))
	require.ErrorContains(t, reporter.retryItem(ctx, first), "delete failure")
	require.EqualValues(t, 1, mustStreamLength(t, client, inner.streamKey()))

	second := first
	second.claimToken = "claim-after-restart"
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(event.RequestID, string(usageRetryOperationFinalize), int64(1), second.claimToken).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, reporter.retryItem(ctx, second))
	require.EqualValues(t, 1, mustStreamLength(t, client, inner.streamKey()))
	require.EqualValues(t, 2, refillSignals.Load(), "幂等重放仍需返回 currently-low lease 信号")

	request, err := client.HGetAll(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "committed", request["status"])
	require.Equal(t, "10", request["actual_cents"])
	require.Equal(t, "1", request["usage_commit_enqueued"])
	lease, err := client.HGetAll(ctx, quota.leaseKey(42, "sg")).Result()
	require.NoError(t, err)
	require.Zero(t, ParseInt64Field(lease, "reserved_cents"))
	require.EqualValues(t, 90, ParseInt64Field(lease, "spent_cents"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableUsageReportReplayAfterDeleteFailureDoesNotDuplicateStream(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const prefix = "outbox-report-delete-replay"
	inner := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, inner)
	event := UsageEvent{
		RequestID: "req-report-delete-replay", UserID: 42, KeyID: "key", Region: "sg", GatewayID: "sg",
		ActualCents: 3, SettlementMode: string(BillingSettlementModeIdempotentV2),
	}
	payload, err := json.Marshal(usageRetryOutboxEnvelope{Event: event, RedisPrefix: prefix})
	require.NoError(t, err)

	first := usageRetryOutboxItem{
		requestID: event.RequestID, operationKind: usageRetryOperationReport,
		payload: payload, generation: 1, claimToken: "claim-report-first",
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(event.RequestID, string(usageRetryOperationReport), int64(1), first.claimToken).
		WillReturnError(errors.New("simulated report outbox delete failure"))
	require.ErrorContains(t, reporter.retryItem(ctx, first), "delete failure")
	require.EqualValues(t, 1, mustStreamLength(t, client, inner.streamKey()))

	second := first
	second.claimToken = "claim-report-after-restart"
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(event.RequestID, string(usageRetryOperationReport), int64(1), second.claimToken).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, reporter.retryItem(ctx, second))
	require.EqualValues(t, 1, mustStreamLength(t, client, inner.streamKey()))

	quota := NewRedisQuota(client, prefix)
	request, err := client.HGetAll(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1", request["usage_report_enqueued"])
	ttl, err := client.TTL(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Positive(t, ttl, "没有 reservation 的稀疏 report marker 必须自动过期")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageRetryDelayIsBounded(t *testing.T) {
	require.Equal(t, usageOutboxRetryDelay(0), usageOutboxRetryDelay(1))
	require.Equal(t, usageRetryOutboxMaxDelay, usageOutboxRetryDelay(100))
}
