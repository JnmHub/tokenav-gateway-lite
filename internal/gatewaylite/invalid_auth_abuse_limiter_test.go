package gatewaylite

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestInvalidAuthAbuseLimiterSeparatesSourceAndKeyDimensions(t *testing.T) {
	limiter := NewInvalidAuthAbuseLimiter(nil, "test", InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 2, Window: time.Minute, Block: time.Minute, Capacity: 256,
	})
	ctx := context.Background()

	limiter.RecordFailure(ctx, "203.0.113.10", "tokenav_sk_bad_one")
	_, blocked := limiter.Check(ctx, "203.0.113.10", "tokenav_sk_bad_two")
	require.False(t, blocked)

	// Rotating the Key does not evade the source-address dimension.
	limiter.RecordFailure(ctx, "203.0.113.10", "tokenav_sk_bad_two")
	retry, blocked := limiter.Check(ctx, "203.0.113.10", "tokenav_sk_bad_three")
	require.True(t, blocked)
	require.Positive(t, retry)

	// The source block does not contaminate an unrelated source.
	_, blocked = limiter.Check(ctx, "198.51.100.20", "tokenav_sk_bad_three")
	require.False(t, blocked)

	// Reusing one bad Key from different sources is caught by its own fingerprint.
	sharedKey := "tokenav_sk_shared_invalid"
	limiter.RecordFailure(ctx, "198.51.100.21", sharedKey)
	limiter.RecordFailure(ctx, "198.51.100.22", sharedKey)
	_, blocked = limiter.Check(ctx, "198.51.100.23", sharedKey)
	require.True(t, blocked)
}

func TestInvalidAuthAbuseLimiterRedisCombinesGatewayFailuresWithoutRawSecrets(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cfg := InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 3, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: time.Second,
	}
	first := NewInvalidAuthAbuseLimiter(client, "cluster", cfg)
	second := NewInvalidAuthAbuseLimiter(client, "cluster", cfg)
	ctx := context.Background()
	rawKey := "tokenav_sk_never-store-this-secret"

	first.RecordFailure(ctx, "203.0.113.31", rawKey)
	first.RecordFailure(ctx, "203.0.113.32", rawKey)
	second.RecordFailure(ctx, "203.0.113.33", rawKey)

	retry, blocked := second.Check(ctx, "203.0.113.34", rawKey)
	require.True(t, blocked)
	require.Positive(t, retry)
	for _, key := range mr.Keys() {
		require.NotContains(t, key, rawKey)
		value, err := mr.Get(key)
		if err == nil {
			require.NotContains(t, value, rawKey)
		}
	}
}

func TestInvalidAuthAbuseLimiterRedisFailureIsOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	limiter := NewInvalidAuthAbuseLimiter(client, "fail-open", InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 20, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: 10 * time.Millisecond,
	})
	mr.Close()
	ctx := context.Background()
	rawKey := "tokenav_sk_invalid_after-redis-loss"
	limiter.RecordFailure(ctx, "203.0.113.40", rawKey)

	_, blocked := limiter.Check(ctx, "203.0.113.40", rawKey)
	require.False(t, blocked, "Redis failure cannot deny a potentially valid request")
	metrics := limiter.RuntimeMetrics(ctx)
	require.True(t, metrics["invalid_auth_abuse_enabled"].(bool))
	require.GreaterOrEqual(t, metrics["invalid_auth_abuse_redis_failures_total"].(uint64), uint64(1))
	for key := range metrics {
		require.False(t, strings.Contains(key, rawKey))
	}
}

func TestInvalidAuthAbuseLimiterOneTypoDoesNotAddRedisToValidNATTraffic(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	limiter := NewInvalidAuthAbuseLimiter(client, "nat-typo", InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 120, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: time.Second,
	})
	ctx := context.Background()
	limiter.RecordFailure(ctx, "203.0.113.50", "tokenav_sk_one_typo")

	for i := 0; i < 100; i++ {
		_, blocked := limiter.Check(ctx, "203.0.113.50", "tokenav_sk_valid_"+strconv.Itoa(i))
		require.False(t, blocked)
	}
	metrics := limiter.RuntimeMetrics(ctx)
	require.Zero(t, metrics["invalid_auth_abuse_redis_checks_total"].(uint64),
		"一次手误不能让同 NAT 合法请求进入 Redis 检查")
}

func TestInvalidAuthAbuseLimiterCachesNegativeRedisCheck(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	limiter := NewInvalidAuthAbuseLimiter(client, "negative-cache", InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 8, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: time.Second,
	})
	now := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	ctx := context.Background()
	limiter.RecordFailure(ctx, "203.0.113.51", "tokenav_sk_bad_1")
	limiter.RecordFailure(ctx, "203.0.113.51", "tokenav_sk_bad_2")

	for i := 0; i < 100; i++ {
		_, blocked := limiter.Check(ctx, "203.0.113.51", "tokenav_sk_candidate")
		require.False(t, blocked)
	}
	metrics := limiter.RuntimeMetrics(ctx)
	require.Equal(t, uint64(1), metrics["invalid_auth_abuse_redis_checks_total"],
		"合理怀疑后的负结果应在短时间内合并")
}

func BenchmarkInvalidAuthAbuseLimiterCleanCheck(b *testing.B) {
	limiter := NewInvalidAuthAbuseLimiter(nil, "benchmark", InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 120, Window: time.Minute, Block: time.Minute, Capacity: 16_384,
	})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, blocked := limiter.Check(ctx, "203.0.113.90", "tokenav_sk_benchmark_high_entropy_secret"); blocked {
			b.Fatal("clean subject unexpectedly blocked")
		}
	}
}
