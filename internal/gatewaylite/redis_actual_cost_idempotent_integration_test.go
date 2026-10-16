//go:build integration

package gatewaylite

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

const actualCostIntegrationRedisImage = "redis:8.4-alpine"

func TestRedisQuotaActualCostIdempotentAccumulatorAgainstRealRedis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := startActualCostIntegrationRedis(t, ctx)
	quota := NewRedisQuota(client, "actual-cost-integration")

	t.Run("same request is idempotent under 1000 concurrent retries", func(t *testing.T) {
		const (
			workers = 1000
			scope   = "sg:key-concurrent:42"
		)

		type result struct {
			cents int64
			err   error
		}
		results := make(chan result, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				<-start
				cents, err := quota.AccumulateActualCostMicroCentsIdempotent(
					ctx,
					scope,
					"same-request",
					1_500_000,
					1_000_000,
				)
				results <- result{cents: cents, err: err}
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		for result := range results {
			require.NoError(t, result.err)
			require.EqualValues(t, 1, result.cents)
		}
		remainder, err := client.Get(ctx, quota.actualCostRemainderKey(scope)).Int64()
		require.NoError(t, err)
		require.EqualValues(t, 500_000, remainder)

		cents, err := quota.AccumulateActualCostMicroCentsIdempotent(
			ctx,
			scope,
			"next-request",
			500_000,
			1_000_000,
		)
		require.NoError(t, err)
		require.EqualValues(t, 1, cents)
	})

	t.Run("different requests atomically carry the shared remainder", func(t *testing.T) {
		const (
			requests = 100
			scope    = "sg:key-distinct:42"
		)

		type result struct {
			cents int64
			err   error
		}
		results := make(chan result, requests)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(requests)
		for i := range requests {
			requestID := fmt.Sprintf("request-%d", i)
			go func() {
				defer wg.Done()
				<-start
				cents, err := quota.AccumulateActualCostMicroCentsIdempotent(
					ctx,
					scope,
					requestID,
					600_000,
					1_000_000,
				)
				results <- result{cents: cents, err: err}
			}()
		}
		close(start)
		wg.Wait()
		close(results)

		var emitted int64
		for result := range results {
			require.NoError(t, result.err)
			emitted += result.cents
		}
		require.EqualValues(t, 60, emitted)
		remainder, err := client.Get(ctx, quota.actualCostRemainderKey(scope)).Int64()
		require.NoError(t, err)
		require.Zero(t, remainder)
	})
}

func TestRedisUsageReporterIdempotentSettlementAgainstRealRedis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := startActualCostIntegrationRedis(t, ctx)
	const (
		prefix  = "actual-cost-reporter-integration"
		workers = 1000
	)
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	event := settlementUsageEvent("request-reported-once", "", 1_500_000)

	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
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

	// Model a lost client response after Redis already executed the Lua script:
	// retrying with no local amount must recover the first result without a
	// second XADD or a second remainder advance.
	replay := event
	replay.SettlementMicroCents = 0
	require.NoError(t, reporter.ReportUsage(ctx, replay))
	require.EqualValues(t, 1, mustStreamLength(t, client, reporter.streamKey()))
	require.Equal(t, []int64{1}, settlementStreamResolvedCents(t, client, reporter.streamKey()))

	quota := NewRedisQuota(client, prefix)
	scope := actualCostAccumulatorScopeForUsageEvent(event)
	remainder, err := client.Get(ctx, quota.actualCostRemainderKey(scope)).Int64()
	require.NoError(t, err)
	require.EqualValues(t, 500_000, remainder)
	request, err := client.HGetAll(ctx, quota.requestKey(event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1", request["settlement_actual_cents"])
	require.Equal(t, "1", request["settlement_usage_enqueued"])
}

func TestRedisKeyInvalidationConvergesAcrossInstancesAgainstRealRedis(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := startActualCostIntegrationRedis(t, ctx)
	const (
		prefix  = "key-invalidation-integration"
		region  = "us-1"
		keyID   = "acl-key"
		userID  = int64(42)
		eventID = int64(701)
	)

	// Two independent RedisKeyCache values model two gateway processes. They
	// share Redis but deliberately do not share the 500ms process-local hot map.
	cacheA := NewRedisKeyCache(client, prefix)
	cacheB := NewRedisKeyCache(client, prefix)
	require.NoError(t, cacheA.Set(ctx, KeySnapshot{
		KeyID:          keyID,
		UserID:         userID,
		IPWhitelist:    []string{"203.0.113.0/24"},
		CacheTTLSecond: 60,
	}, region))
	_, ok, err := cacheB.Get(ctx, keyID, region)
	require.NoError(t, err)
	require.True(t, ok, "second instance must warm its own L1 cache before invalidation")

	event := CacheInvalidationEvent{
		ID:          eventID,
		Scope:       "key:snapshot",
		GatewayCode: region,
		Payload:     map[string]any{"key_id": keyID, "user_id": float64(userID)},
	}
	syncerA := NewCacheInvalidationSyncer(nil, NewRedisConfigCache(client, prefix), cacheA, nil, region, region, time.Hour)
	syncerB := NewCacheInvalidationSyncer(nil, NewRedisConfigCache(client, prefix), cacheB, nil, region, region, time.Hour)

	already, err := syncerA.ApplyPushedEvent(ctx, event)
	require.NoError(t, err)
	require.False(t, already)
	already, err = syncerB.ApplyPushedEvent(ctx, event)
	require.NoError(t, err)
	require.True(t, already, "shared Redis marker must make duplicate delivery idempotent")

	redisKey := cacheA.cacheKey(keyID, region)
	exists, err := client.Exists(ctx, redisKey).Result()
	require.NoError(t, err)
	require.Zero(t, exists, "the shared L2 snapshot must be removed immediately")
	require.Eventually(t, func() bool {
		_, hit, getErr := cacheB.Get(ctx, keyID, region)
		return getErr == nil && !hit
	}, 2*time.Second, 20*time.Millisecond, "a peer process may retain only the bounded 500ms L1 snapshot")
}

func startActualCostIntegrationRedis(t *testing.T, ctx context.Context) *redis.Client {
	t.Helper()
	ensureActualCostIntegrationDocker(t, ctx)

	container, err := tcredis.Run(ctx, actualCostIntegrationRedisImage)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(cleanupCtx)
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", host, port.Int()),
		PoolSize: 128,
	})
	require.NoError(t, client.Ping(ctx).Err())
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func ensureActualCostIntegrationDocker(t *testing.T, ctx context.Context) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "info")
	cmd.Env = os.Environ()
	if err := cmd.Run(); err == nil {
		return
	} else if os.Getenv("CI") != "" {
		require.NoError(t, err, "Docker must be available for integration tests in CI")
	}
	t.Skip("Docker 未启用，跳过依赖 testcontainers 的真实 Redis 集成测试")
}
