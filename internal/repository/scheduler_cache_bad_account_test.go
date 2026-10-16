package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type schedulerCommandCountHook struct {
	mu     sync.Mutex
	counts map[string]int
}

func newSchedulerCommandCountHook() *schedulerCommandCountHook {
	return &schedulerCommandCountHook{counts: make(map[string]int)}
}

func (h *schedulerCommandCountHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *schedulerCommandCountHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.add(cmd.Name())
		return next(ctx, cmd)
	}
}

func (h *schedulerCommandCountHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			h.add(cmd.Name())
		}
		return next(ctx, cmds)
	}
}

func (h *schedulerCommandCountHook) add(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[name]++
}

func (h *schedulerCommandCountHook) count(name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[name]
}

func TestSchedulerCacheSetSnapshotSkipsAndDeletesUnencodableAccount(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewSchedulerCache(rdb)
	ctx := context.Background()

	badID := int64(2)
	require.NoError(t, cache.SetAccount(ctx, &service.Account{ID: badID, Name: "stale", Platform: service.PlatformOpenAI}))

	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	good := service.Account{ID: 1, Name: "good", Platform: service.PlatformOpenAI}
	bad := service.Account{ID: badID, Name: "bad", Platform: service.PlatformOpenAI, Credentials: map[string]any{"invalid": make(chan int)}}
	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{good, bad}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, good.ID, snapshot[0].ID)

	stale, err := cache.GetAccount(ctx, badID)
	require.NoError(t, err)
	require.Nil(t, stale)
}

func TestSchedulerCacheMirrorMergeRunsBeforeEncodingIsolation(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewSchedulerCache(rdb)
	ctx := context.Background()

	accountID := int64(8)
	require.NoError(t, cache.SetAccount(ctx, &service.Account{
		ID: accountID, Platform: service.PlatformOpenAI,
		Credentials: map[string]any{"api_key": "local-secret", "base_url": "https://old.example/v1"},
	}))

	bucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	incoming := service.Account{
		ID: accountID, Platform: service.PlatformOpenAI,
		Credentials: map[string]any{"base_url": "https://new.example/v1"},
		Extra:       map[string]any{"gateway_upstream_mirror_id": int64(100)},
	}
	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{incoming}))

	stored, err := cache.GetAccount(ctx, accountID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "local-secret", stored.Credentials["api_key"])
	require.Equal(t, "https://new.example/v1", stored.Credentials["base_url"])
}

func TestSchedulerCacheRetireFencesStaleSnapshotAndReopenAllowsFreshWrite(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewSchedulerCache(rdb)
	lifecycle, ok := cache.(service.SchedulerBucketLifecycleCache)
	require.True(t, ok)
	ctx := context.Background()

	bucket := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	staleToken, err := lifecycle.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, lifecycle.SetSnapshotWithToken(ctx, bucket, staleToken, []service.Account{{
		ID: 1, Name: "old", Platform: service.PlatformOpenAI,
	}}))

	require.NoError(t, lifecycle.RetireBucket(ctx, bucket))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit)
	require.Nil(t, snapshot)
	buckets, err := cache.ListBuckets(ctx)
	require.NoError(t, err)
	require.NotContains(t, buckets, bucket)

	err = lifecycle.SetSnapshotWithToken(ctx, bucket, staleToken, []service.Account{{
		ID: 2, Name: "stale", Platform: service.PlatformOpenAI,
	}})
	require.True(t, errors.Is(err, service.ErrSchedulerBucketRetired) || errors.Is(err, service.ErrSchedulerBucketWriteFenced))

	freshToken, err := lifecycle.ReopenBucket(ctx, bucket)
	require.NoError(t, err)
	require.ErrorIs(t, lifecycle.SetSnapshotWithToken(ctx, bucket, staleToken, nil), service.ErrSchedulerBucketWriteFenced)
	require.NoError(t, lifecycle.SetSnapshotWithToken(ctx, bucket, freshToken, []service.Account{{
		ID: 3, Name: "fresh", Platform: service.PlatformOpenAI,
	}}))

	snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, int64(3), snapshot[0].ID)
}

func TestSchedulerCacheSnapshotAccountIDReuseKeepsAcceptedMembers(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, ok := NewSchedulerCache(rdb).(*schedulerCache)
	require.True(t, ok)
	ctx := context.Background()

	single := service.SchedulerBucket{GroupID: 9, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	forced := service.SchedulerBucket{GroupID: 9, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}
	good := service.Account{ID: 10, Name: "good", Platform: service.PlatformOpenAI}
	bad := service.Account{ID: 11, Name: "bad", Platform: service.PlatformOpenAI, Credentials: map[string]any{"invalid": make(chan int)}}

	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	acceptedIDs, err := cache.SetSnapshotAndReturnAccountIDs(ctx, single, singleToken, []service.Account{good, bad})
	require.NoError(t, err)
	require.Equal(t, []int64{good.ID}, acceptedIDs)

	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByAccountIDs(ctx, forced, forcedToken, acceptedIDs))

	for _, bucket := range []service.SchedulerBucket{single, forced} {
		snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, snapshot, 1)
		require.Equal(t, good.ID, snapshot[0].ID)
	}
	stale, err := cache.GetAccount(ctx, bad.ID)
	require.NoError(t, err)
	require.Nil(t, stale)
}

func TestSchedulerCacheSnapshotAccountIDReusePreservesGatewayMirrorCredentials(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, ok := NewSchedulerCache(rdb).(*schedulerCache)
	require.True(t, ok)
	ctx := context.Background()

	accountID := int64(18)
	require.NoError(t, cache.SetAccount(ctx, &service.Account{
		ID: accountID, Platform: service.PlatformOpenAI,
		Credentials: map[string]any{"api_key": "local-test-secret", "base_url": "https://old.example/v1"},
	}))

	bucket := service.SchedulerBucket{GroupID: 10, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	ids, err := cache.SetSnapshotAndReturnAccountIDs(ctx, bucket, token, []service.Account{{
		ID: accountID, Platform: service.PlatformOpenAI,
		Credentials: map[string]any{"base_url": "https://new.example/v1"},
		Extra:       map[string]any{"gateway_upstream_mirror_id": int64(100)},
	}})
	require.NoError(t, err)
	require.Equal(t, []int64{accountID}, ids)

	stored, err := cache.GetAccount(ctx, accountID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "local-test-secret", stored.Credentials["api_key"])
	require.Equal(t, "https://new.example/v1", stored.Credentials["base_url"])
}

func TestSchedulerCacheSnapshotAccountIDReuseAvoidsRepeatedAccountWrites(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	hook := newSchedulerCommandCountHook()
	rdb.AddHook(hook)
	cache, ok := NewSchedulerCache(rdb).(*schedulerCache)
	require.True(t, ok)
	ctx := context.Background()

	accounts := make([]service.Account, 0, 100)
	for id := int64(1); id <= 100; id++ {
		accounts = append(accounts, service.Account{ID: id, Name: "account", Platform: service.PlatformOpenAI})
	}
	single := service.SchedulerBucket{GroupID: 20, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	forced := service.SchedulerBucket{GroupID: 20, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}

	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	beforeFull := hook.count("set")
	ids, err := cache.SetSnapshotAndReturnAccountIDs(ctx, single, singleToken, accounts)
	require.NoError(t, err)
	require.Len(t, ids, 100)
	fullSets := hook.count("set") - beforeFull
	require.Equal(t, 200, fullSets, "首次完整写每个账号只写 full/meta 两个全局键")

	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	beforeReuse := hook.count("set")
	require.NoError(t, cache.SetSnapshotByAccountIDs(ctx, forced, forcedToken, ids))
	require.Zero(t, hook.count("set")-beforeReuse, "ID-only 快照不得重复写账号 JSON 和元数据键")
}
