package gatewaylite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisKeyCacheGetSet(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	key := KeySnapshot{
		KeyID:          "key123",
		UserID:         42,
		SecretHash:     "hash",
		Status:         "active",
		Platform:       "openai",
		GroupID:        7,
		GroupName:      "openai",
		IPWhitelist:    []string{"198.51.100.0/24"},
		IPBlacklist:    []string{"198.51.100.9"},
		CacheTTLSecond: 60,
	}
	require.NoError(t, cache.Set(ctx, key, "sg"))

	got, ok, err := cache.Get(ctx, "key123", "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, key.KeyID, got.KeyID)
	require.Equal(t, key.UserID, got.UserID)
	require.Equal(t, key.SecretHash, got.SecretHash)
	require.Equal(t, key.IPWhitelist, got.IPWhitelist)
	require.Equal(t, key.IPBlacklist, got.IPBlacklist)
}

func TestRedisKeyCacheExpiresSnapshotByTTL(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "expiring-key", CacheTTLSecond: 2}, "sg"))
	_, ok, err := cache.Get(ctx, "expiring-key", "sg")
	require.NoError(t, err)
	require.True(t, ok)

	mr.FastForward(3 * time.Second)
	time.Sleep(keySnapshotHotReadTTL + 25*time.Millisecond)

	_, ok, err = cache.Get(ctx, "expiring-key", "sg")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRedisKeyCacheHotReadSharedByScopedCacheAndActivelyInvalidated(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")
	scoped := cache.WithPrefix("test")

	key := KeySnapshot{KeyID: "hot-key", UserID: 42, Status: "active", CacheTTLSecond: 60}
	require.NoError(t, cache.Set(ctx, key, "sg"))
	// 删除 Redis 原值后，短时间内仍能证明 WithPrefix 复用了同一份进程热缓存。
	require.NoError(t, client.Del(ctx, cache.cacheKey(key.KeyID, "sg")).Err())
	got, ok, err := scoped.Get(ctx, key.KeyID, "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, key.UserID, got.UserID)

	// 主站停用或删除 Key 时会走 DeleteKey；它必须立即清掉所有 scoped clone 的热值。
	require.NoError(t, cache.DeleteKey(ctx, key.KeyID, "sg"))
	_, ok, err = scoped.Get(ctx, key.KeyID, "sg")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRedisKeyCacheHotReadExpiresWithoutInvalidationEvent(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	key := KeySnapshot{KeyID: "hot-expiry", UserID: 42, Status: "active", CacheTTLSecond: 60}
	require.NoError(t, cache.Set(ctx, key, "sg"))
	require.NoError(t, client.Del(ctx, cache.cacheKey(key.KeyID, "sg")).Err())
	time.Sleep(keySnapshotHotReadTTL + 25*time.Millisecond)

	_, ok, err := cache.Get(ctx, key.KeyID, "sg")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRedisKeyCacheImmutableHotReadSharesDecodedSnapshotAndStillInvalidates(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "immutable")

	require.NoError(t, cache.Set(ctx, KeySnapshot{
		KeyID: "shared-key", UserID: 42, SecretHash: "hash", Status: "active",
		AllowedModels: []string{"gpt-5.6-sol"}, CacheTTLSecond: 60,
	}, "sg"))
	first, ok := cache.PeekHotImmutable("shared-key", "sg")
	require.True(t, ok)
	second, ok, err := cache.GetImmutable(ctx, "shared-key", "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.Same(t, first, second)
	require.Equal(t, []string{"gpt-5.6-sol"}, second.AllowedModels)

	require.NoError(t, cache.DeleteKey(ctx, "shared-key", "sg"))
	_, ok = cache.PeekHotImmutable("shared-key", "sg")
	require.False(t, ok)
}

func TestRedisKeyCacheUsesDefaultTTLWhenSnapshotTTLInvalid(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "default-ttl-key"}, "sg"))
	ttl := mr.TTL(cache.cacheKey("default-ttl-key", "sg"))
	require.Greater(t, ttl, time.Duration(0))
	require.LessOrEqual(t, ttl, defaultKeySnapshotTTL)
}

func TestRedisKeyCacheDeleteRegion(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "sg-key", CacheTTLSecond: 60}, "sg"))
	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "us-key", CacheTTLSecond: 60}, "us"))

	require.NoError(t, cache.DeleteRegion(ctx, "sg"))
	_, ok, err := cache.Get(ctx, "sg-key", "sg")
	require.NoError(t, err)
	require.False(t, ok)

	got, ok, err := cache.Get(ctx, "us-key", "us")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "us-key", got.KeyID)
}

func TestRedisKeyCacheDeleteKey(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "target-key", CacheTTLSecond: 60}, "sg"))
	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "other-key", CacheTTLSecond: 60}, "sg"))
	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "target-key", CacheTTLSecond: 60}, "us"))

	require.NoError(t, cache.DeleteKey(ctx, "target-key", "sg"))
	_, ok, err := cache.Get(ctx, "target-key", "sg")
	require.NoError(t, err)
	require.False(t, ok)

	got, ok, err := cache.Get(ctx, "other-key", "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "other-key", got.KeyID)

	got, ok, err = cache.Get(ctx, "target-key", "us")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "target-key", got.KeyID)
}

func TestRedisKeyCacheDeleteUser(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisKeyCache(client, "test")

	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "user-key-1", UserID: 42, CacheTTLSecond: 60}, "sg"))
	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "user-key-2", UserID: 42, CacheTTLSecond: 60}, "sg"))
	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "other-user-key", UserID: 99, CacheTTLSecond: 60}, "sg"))
	require.NoError(t, cache.Set(ctx, KeySnapshot{KeyID: "same-user-other-region", UserID: 42, CacheTTLSecond: 60}, "us"))

	require.NoError(t, cache.DeleteUser(ctx, 42, "sg"))
	_, ok, err := cache.Get(ctx, "user-key-1", "sg")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = cache.Get(ctx, "user-key-2", "sg")
	require.NoError(t, err)
	require.False(t, ok)

	got, ok, err := cache.Get(ctx, "other-user-key", "sg")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "other-user-key", got.KeyID)

	got, ok, err = cache.Get(ctx, "same-user-other-region", "us")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "same-user-other-region", got.KeyID)
}

func TestRedisKeyCacheRejectsInFlightSnapshotAfterRegionInvalidation(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer clientA.Close()
	defer clientB.Close()
	cacheA := NewRedisKeyCache(clientA, "generation")
	cacheB := NewRedisKeyCache(clientB, "generation")

	fillToken, err := cacheA.BeginFill(ctx, "sg")
	require.NoError(t, err)
	require.NoError(t, cacheB.DeleteRegion(ctx, "sg"))

	written, err := cacheA.SetIfGenerationUnchanged(ctx, KeySnapshot{
		KeyID: "stale-key", UserID: 42, OrganizationID: 9, Status: "active", CacheTTLSecond: 300,
	}, "sg", fillToken)
	require.NoError(t, err)
	require.False(t, written)
	require.EqualValues(t, 0, clientA.Exists(ctx, cacheA.cacheKey("stale-key", "sg")).Val())
	require.EqualValues(t, 0, clientA.Exists(ctx, cacheA.userIndexKey(42, "sg")).Val())
	require.EqualValues(t, 0, clientA.Exists(ctx, cacheA.organizationIndexKey(9, "sg")).Val())
	_, ok := cacheA.PeekHotImmutable("stale-key", "sg")
	require.False(t, ok)
}

func TestRedisKeyCacheAgentNeverWritesPositiveSnapshot(t *testing.T) {
	for _, ttl := range []int{0, -1, 300} {
		t.Run(time.Duration(ttl).String(), func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cache := NewRedisKeyCache(client, "agent-cache")
			key := KeySnapshot{KeyID: "agent", UserID: 42, OrganizationID: 9, Status: "active", TokenUse: "agent_api_key", CacheTTLSecond: ttl}
			token, err := cache.BeginFill(ctx, "test-node")
			require.NoError(t, err)
			written, err := cache.SetIfGenerationUnchanged(ctx, key, "test-node", token)
			require.NoError(t, err)
			require.False(t, written, "首次未知用途的回源完成后也必须拒绝 Agent 回填")
			require.NoError(t, cache.Set(ctx, key, "test-node"))
			raw, err := json.Marshal(key)
			require.NoError(t, err)
			cache.setHot(cache.cacheKey(key.KeyID, "test-node"), string(raw), &key, "test-node")
			require.False(t, cache.setHotIfGenerationUnchanged(cache.cacheKey(key.KeyID, "test-node"), string(raw), &key, token))
			require.Empty(t, mr.Keys(), "Agent 不应生成正缓存或身份索引")
			_, found := cache.PeekHotImmutable(key.KeyID, "test-node")
			require.False(t, found)
		})
	}
}

func TestRedisKeyCacheAgentRejectsSeededRedisAndHotSnapshots(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewRedisKeyCache(client, "agent-seeded")
	key := KeySnapshot{KeyID: "agent", UserID: 42, Status: "active", TokenUse: "agent_api_key", CacheTTLSecond: 300}
	raw, err := json.Marshal(key)
	require.NoError(t, err)
	cacheKey := cache.cacheKey(key.KeyID, "test-node")
	require.NoError(t, client.Set(ctx, cacheKey, raw, time.Minute).Err())
	for _, get := range []func(context.Context, string, string) (*KeySnapshot, bool, error){cache.Get, cache.GetImmutable} {
		snapshot, found, err := get(ctx, key.KeyID, "test-node")
		require.NoError(t, err)
		require.False(t, found)
		require.Nil(t, snapshot)
	}
	seedHot := func() {
		cache.hot.mu.Lock()
		cache.hot.entries[cacheKey] = keySnapshotHotEntry{raw: string(raw), snapshot: &key, expiresAt: time.Now().Add(time.Minute).UnixNano()}
		cache.hot.mu.Unlock()
	}
	for _, peek := range []func(string, string) (*KeySnapshot, bool){cache.PeekHot, cache.PeekHotImmutable} {
		seedHot()
		snapshot, found := peek(key.KeyID, "test-node")
		require.False(t, found)
		require.Nil(t, snapshot)
	}
	for _, get := range []func(context.Context, string, string) (*KeySnapshot, bool, error){cache.Get, cache.GetImmutable} {
		seedHot()
		snapshot, found, err := get(ctx, key.KeyID, "test-node")
		require.NoError(t, err)
		require.False(t, found)
		require.Nil(t, snapshot)
	}
}

func TestRedisKeyCacheOrdinaryAndUnknownTokenUseKeepDefaultTTL(t *testing.T) {
	for _, use := range []string{"", "api_key", "unknown_future_use", "AGENT_API_KEY"} {
		t.Run(use, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cache := NewRedisKeyCache(client, "ordinary-cache")
			key := KeySnapshot{KeyID: "ordinary", Status: "active", TokenUse: use}
			require.NoError(t, cache.Set(ctx, key, "test-node"))
			require.Greater(t, mr.TTL(cache.cacheKey(key.KeyID, "test-node")), time.Duration(0))
			cached, found := cache.PeekHotImmutable(key.KeyID, "test-node")
			require.True(t, found)
			require.Equal(t, use, cached.TokenUse)
			require.NoError(t, client.Del(ctx, cache.cacheKey(key.KeyID, "test-node")).Err())
			_, found, err := cache.GetImmutable(ctx, key.KeyID, "test-node")
			require.NoError(t, err)
			require.True(t, found, "普通用途继续复用热缓存")
		})
	}
}

func BenchmarkRedisKeyCacheHotAuthenticationSnapshot(b *testing.B) {
	ctx := context.Background()
	mr := miniredis.RunT(b)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b.Cleanup(func() { _ = client.Close() })
	cache := NewRedisKeyCache(client, "bench")
	snapshot := KeySnapshot{
		KeyID: "hot-key", UserID: 42, SecretHash: strings.Repeat("a", 64), Status: "active",
		Tier: 2, Platform: "openai", GroupID: 7, GroupName: "OpenAI",
		IPWhitelist: []string{"198.51.100.0/24"}, IPBlacklist: []string{"198.51.100.9"},
		AllowedModels:            []string{"gpt-5.6-sol", "gpt-5.4", "gpt-5.4-mini"},
		UserGroupRateMultipliers: map[int64]float64{7: 1.2, 8: 0.8},
		Group: &GatewayGroupSnapshot{
			ID: 7, LocalGroupID: 3, Name: "OpenAI", Platform: "openai", Status: "active",
			RateMultiplier: 1.2, EffectiveModels: []string{"gpt-5.6-sol", "gpt-5.4"},
			ModelRouting: map[string][]int64{"gpt-5.6-sol": {11, 12}},
		},
		CacheTTLSecond: 60,
	}
	require.NoError(b, cache.Set(ctx, snapshot, "sg"))
	cacheKey := cache.cacheKey(snapshot.KeyID, "sg")
	cache.hot.mu.Lock()
	entry := cache.hot.entries[cacheKey]
	entry.expiresAt = time.Now().Add(time.Minute).UnixNano()
	cache.hot.entries[cacheKey] = entry
	cache.hot.mu.Unlock()

	b.Run("legacy_double_json_decode", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, ok := cache.PeekHot(snapshot.KeyID, "sg"); !ok {
				b.Fatal("hot snapshot missing")
			}
			if _, ok, err := cache.Get(ctx, snapshot.KeyID, "sg"); err != nil || !ok {
				b.Fatalf("get failed: ok=%v err=%v", ok, err)
			}
		}
	})
	b.Run("immutable_decoded_hot_path", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, ok := cache.PeekHotImmutable(snapshot.KeyID, "sg"); !ok {
				b.Fatal("hot snapshot missing")
			}
			if _, ok, err := cache.GetImmutable(ctx, snapshot.KeyID, "sg"); err != nil || !ok {
				b.Fatalf("get failed: ok=%v err=%v", ok, err)
			}
		}
	})
}
