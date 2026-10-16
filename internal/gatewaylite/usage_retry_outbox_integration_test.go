//go:build integration

package gatewaylite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestDurableUsageReporterRecoversFinalizeAndCorrectionAfterFullRedisOutage(t *testing.T) {
	tests := []struct {
		name            string
		correctionFirst bool
		wantStream      []int64
		wantModels      []string
	}{
		{
			name:       "base_then_correction",
			wantStream: []int64{1, 9}, wantModels: []string{"base", "correction"},
		},
		{
			name:            "correction_then_base",
			correctionFirst: true,
			wantStream:      []int64{9, 9}, wantModels: []string{"correction", "base"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()

			client, redisContainer := startUsageOutboxIntegrationRedis(t, ctx)
			db := startUsageOutboxIntegrationPostgres(t, ctx)
			prefix := "durable-usage-outbox-" + tt.name

			var sinkMu sync.Mutex
			reported := make([]UsageEvent, 0, 2)
			inner := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(_ context.Context, event UsageEvent) error {
				sinkMu.Lock()
				reported = append(reported, event)
				sinkMu.Unlock()
				return nil
			}))
			warmUsageCommitBatcher(t, ctx, client, inner, prefix)

			quota := NewRedisQuota(client, prefix)
			const (
				requestID = "request-outbox-recovery"
				leaseID   = "lease-outbox-recovery"
			)
			require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
				LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
				Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
			}))
			reserved, err := quota.Reserve(ctx, ReserveRequest{
				RequestID: requestID, KeyID: "key-settlement", UserID: 42, Region: "sg",
				LeaseID: leaseID, EstimatedCostCents: 1,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			reporter := NewDurableUsageReporter(db, inner)
			base := UsageEvent{
				RequestID: requestID, UserID: 42, KeyID: "key-settlement", Region: "sg", GatewayID: "sg",
				Model: "base", ActualCents: 1, SettlementMode: string(BillingSettlementModeIdempotentV2),
			}
			correction := settlementUsageEvent(requestID, requestID, 9_500_000)
			correction.Model = "correction"

			stopUsageOutboxIntegrationRedis(t, ctx, redisContainer)
			if tt.correctionFirst {
				require.NoError(t, reporter.ReportUsage(ctx, correction))
				time.Sleep(10 * time.Millisecond)
				actual, commitErr := reporter.CommitAndReportUsage(ctx, quota, base)
				require.NoError(t, commitErr)
				require.EqualValues(t, 1, actual)
			} else {
				actual, commitErr := reporter.CommitAndReportUsage(ctx, quota, base)
				require.NoError(t, commitErr)
				require.EqualValues(t, 1, actual)
				time.Sleep(10 * time.Millisecond)
				require.NoError(t, reporter.ReportUsage(ctx, correction))
			}

			var operationKinds []string
			rows, err := db.QueryContext(ctx, `
				SELECT operation_kind
				FROM gateway_lite_usage_retry_outbox
				WHERE request_id = $1
				ORDER BY created_at ASC, operation_kind ASC
			`, requestID)
			require.NoError(t, err)
			for rows.Next() {
				var operationKind string
				require.NoError(t, rows.Scan(&operationKind))
				operationKinds = append(operationKinds, operationKind)
			}
			require.NoError(t, rows.Err())
			require.NoError(t, rows.Close())
			if tt.correctionFirst {
				require.Equal(t, []string{"correction", "finalize"}, operationKinds)
			} else {
				require.Equal(t, []string{"finalize", "correction"}, operationKinds)
			}

			client = resumeUsageOutboxIntegrationRedis(t, ctx, redisContainer)
			// Docker Desktop may assign a new published port after restart. Rebind
			// every production-path holder to the restored real Redis instance.
			inner.client = client
			inner.commitBatcher = newUsageCommitBatcher(client)
			quota.client = client
			require.NoError(t, reporter.retryDue(ctx))

			var queued int
			require.NoError(t, db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM gateway_lite_usage_retry_outbox WHERE request_id=$1",
				requestID,
			).Scan(&queued))
			require.Zero(t, queued)

			request, err := client.HGetAll(ctx, quota.requestKey(requestID)).Result()
			require.NoError(t, err)
			require.Equal(t, "committed", request["status"])
			require.Equal(t, "9", request["actual_cents"])
			require.Equal(t, "9", request["settlement_actual_cents"])
			require.Equal(t, "1", request["usage_commit_enqueued"])
			require.Equal(t, "1", request["settlement_usage_enqueued"])

			lease, err := client.HGetAll(ctx, quota.leaseKey(42, "sg")).Result()
			require.NoError(t, err)
			require.Zero(t, ParseInt64Field(lease, "reserved_cents"))
			require.EqualValues(t, 9, ParseInt64Field(lease, "spent_cents"))

			scope := actualCostAccumulatorScopeForUsageEvent(correction)
			remainder, err := client.Get(ctx, quota.actualCostRemainderKey(scope)).Int64()
			require.NoError(t, err)
			require.EqualValues(t, 500_000, remainder)
			require.Equal(t, tt.wantStream, settlementStreamResolvedCents(t, client, inner.streamKey()))
			require.EqualValues(t, 2, mustStreamLength(t, client, inner.streamKey()))

			// 空 outbox 再跑一轮不会重复推进零钱罐、额度或 Redis Stream。
			require.NoError(t, reporter.retryDue(ctx))
			require.Equal(t, tt.wantStream, settlementStreamResolvedCents(t, client, inner.streamKey()))
			remainder, err = client.Get(ctx, quota.actualCostRemainderKey(scope)).Int64()
			require.NoError(t, err)
			require.EqualValues(t, 500_000, remainder)

			// 使用真实 Redis consumer group 消费两条事件，模拟上报主站；每个
			// operation 只投递一次，第二轮读取为空。
			require.NoError(t, inner.ensureGroupForPrefix(ctx, prefix))
			require.True(t, inner.readWorkerPrefix(ctx, 0, "outbox-integration", prefix, 20*time.Millisecond))
			sinkMu.Lock()
			require.Len(t, reported, 2)
			models := []string{reported[0].Model, reported[1].Model}
			sinkMu.Unlock()
			require.Equal(t, tt.wantModels, models)
			require.False(t, inner.readWorkerPrefix(ctx, 0, "outbox-integration", prefix, 20*time.Millisecond))
			sinkMu.Lock()
			require.Len(t, reported, 2)
			sinkMu.Unlock()
		})
	}
}

func warmUsageCommitBatcher(t *testing.T, ctx context.Context, client *redis.Client, reporter *RedisUsageReporter, prefix string) {
	t.Helper()
	quota := NewRedisQuota(client, prefix)
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-warm", UserID: 99, Region: "warm", AllocatedCents: 10,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	reserved, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "request-warm", KeyID: "key-warm", UserID: 99, Region: "warm",
		LeaseID: "lease-warm", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)
	_, err = reporter.CommitAndReportUsage(ctx, quota, UsageEvent{RequestID: "request-warm", ActualCents: 1})
	require.NoError(t, err)
	require.NoError(t, client.FlushDB(ctx).Err())
}

func startUsageOutboxIntegrationRedis(t *testing.T, ctx context.Context) (*redis.Client, *tcredis.RedisContainer) {
	t.Helper()
	ensureActualCostIntegrationDocker(t, ctx)

	container, err := tcredis.Run(ctx, actualCostIntegrationRedisImage)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(cleanupCtx)
	})
	client := newUsageOutboxIntegrationRedisClient(t, ctx, container)
	t.Cleanup(func() { _ = client.Close() })
	return client, container
}

func newUsageOutboxIntegrationRedisClient(t *testing.T, ctx context.Context, container *tcredis.RedisContainer) *redis.Client {
	t.Helper()
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%d", host, port.Int()),
		PoolSize:     16,
		MaxRetries:   -1,
		DialTimeout:  300 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolTimeout:  time.Second,
	})
	require.NoError(t, client.Ping(ctx).Err())
	return client
}

func stopUsageOutboxIntegrationRedis(t *testing.T, ctx context.Context, container *tcredis.RedisContainer) {
	t.Helper()
	stopTimeout := 10 * time.Second
	require.NoError(t, container.Stop(ctx, &stopTimeout))
}

func resumeUsageOutboxIntegrationRedis(t *testing.T, ctx context.Context, container *tcredis.RedisContainer) *redis.Client {
	t.Helper()
	require.NoError(t, container.Start(ctx))
	client := newUsageOutboxIntegrationRedisClient(t, ctx, container)
	t.Cleanup(func() { _ = client.Close() })
	require.Eventually(t, func() bool {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		return client.Ping(pingCtx).Err() == nil
	}, 15*time.Second, 100*time.Millisecond)
	return client
}

func startUsageOutboxIntegrationPostgres(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	ensureActualCostIntegrationDocker(t, ctx)

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		tcpostgres.WithDatabase("gateway_lite_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(cleanupCtx)
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, db.PingContext(ctx))
	t.Cleanup(func() { _ = db.Close() })

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrationPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "185_gateway_lite_usage_retry_outbox.sql")
	migration, err := os.ReadFile(migrationPath)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	return db
}
