package repository

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newSchedulerLastUsedTestCache(t *testing.T) *schedulerCache {
	t.Helper()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return newSchedulerCacheWithChunkSizes(rdb, 64, 64).(*schedulerCache)
}

func TestSchedulerCacheUpdateLastUsedUsesSideKeyWithoutRewritingPayloads(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerLastUsedTestCache(t)
	bucket := service.SchedulerBucket{GroupID: 9, Platform: service.PlatformGrok, Mode: service.SchedulerModeSingle}
	initial := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Hour)
	account := service.Account{
		ID: 9201, Name: "grok-large-oauth", Platform: service.PlatformGrok,
		Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
		LastUsedAt:  &initial,
		Credentials: map[string]any{"access_token": strings.Repeat("a", 4096), "refresh_token": strings.Repeat("r", 4096)},
		Extra:       map[string]any{"large": strings.Repeat("x", 4096)},
	}
	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))

	id := strconv.FormatInt(account.ID, 10)
	fullBefore, err := cache.rdb.Get(ctx, schedulerAccountKey(id)).Bytes()
	require.NoError(t, err)
	metaBefore, err := cache.rdb.Get(ctx, schedulerAccountMetaKey(id)).Bytes()
	require.NoError(t, err)

	latest := initial.Add(37 * time.Second)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: latest}))

	fullAfter, err := cache.rdb.Get(ctx, schedulerAccountKey(id)).Bytes()
	require.NoError(t, err)
	metaAfter, err := cache.rdb.Get(ctx, schedulerAccountMetaKey(id)).Bytes()
	require.NoError(t, err)
	require.Equal(t, fullBefore, fullAfter)
	require.Equal(t, metaBefore, metaAfter)
	require.Equal(t, strconv.FormatInt(latest.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())

	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, latest, *cached.LastUsedAt)
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, latest, *snapshot[0].LastUsedAt)
}

func TestSchedulerCacheLastUsedSideKeyIsMonotonicAndRequiresAccount(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerLastUsedTestCache(t)
	account := service.Account{ID: 9202, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth}
	require.NoError(t, cache.SetAccount(ctx, &account))

	newer := time.Now().UTC().Truncate(time.Millisecond)
	older := newer.Add(-time.Minute)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: newer}))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: older}))

	id := strconv.FormatInt(account.ID, 10)
	require.Equal(t, strconv.FormatInt(newer.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())
	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, newer, *cached.LastUsedAt)

	const missingID int64 = 9299
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{missingID: newer}))
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey(strconv.FormatInt(missingID, 10))).Result()
	require.ErrorIs(t, err, redis.Nil)

	require.NoError(t, cache.DeleteAccount(ctx, account.ID))
	_, err = cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Result()
	require.ErrorIs(t, err, redis.Nil)
}

func TestSchedulerCacheLastUsedSideKeySurvivesStaleSnapshotWrites(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerLastUsedTestCache(t)
	bucket := service.SchedulerBucket{GroupID: 10, Platform: service.PlatformGrok, Mode: service.SchedulerModeSingle}
	embedded := time.Now().UTC().Truncate(time.Millisecond).Add(-time.Minute)
	latest := embedded.Add(30 * time.Second)
	account := service.Account{ID: 9204, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth, Schedulable: true, LastUsedAt: &embedded}
	require.NoError(t, cache.SetAccount(ctx, &account))
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: latest}))
	require.NoError(t, cache.SetAccount(ctx, &account))
	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))

	id := strconv.FormatInt(account.ID, 10)
	require.Equal(t, strconv.FormatInt(latest.UnixMilli(), 10), cache.rdb.Get(ctx, schedulerLastUsedKey(id)).Val())
	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, latest, *cached.LastUsedAt)
}

func TestSchedulerCacheUpdateLastUsedChunksLargeBatches(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerLastUsedTestCache(t)
	total := schedulerLastUsedUpdateChunkSize + 1
	accounts := make([]service.Account, 0, total)
	updates := make(map[int64]time.Time, total)
	base := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < total; i++ {
		id := int64(9300 + i)
		accounts = append(accounts, service.Account{ID: id, Platform: service.PlatformGrok})
		updates[id] = base.Add(time.Duration(i) * time.Millisecond)
	}

	written, err := cache.writeAccountIDs(ctx, accounts)
	require.NoError(t, err)
	require.Len(t, written, total)
	require.NoError(t, cache.UpdateLastUsed(ctx, updates))
	for id, usedAt := range updates {
		key := schedulerLastUsedKey(strconv.FormatInt(id, 10))
		require.Equal(t, strconv.FormatInt(usedAt.UnixMilli(), 10), cache.rdb.Get(ctx, key).Val())
	}
}
