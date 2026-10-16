package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyCacheCleanupStaleProcessSlotsPreservesHealthyInstances(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()
	key := groupUserSlotKey(7, 42)
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)

	require.NoError(t, rdb.ZAdd(ctx, key,
		redis.Z{Score: float64(now.Unix()), Member: "other-healthy-process-request"},
		redis.Z{Score: float64(now.Unix()), Member: "active-process-request"},
		redis.Z{Score: float64(now.Unix() - 61), Member: "expired-process-request"},
	).Err())

	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "active-process-"))
	members, err := rdb.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"active-process-request", "other-healthy-process-request"}, members,
		"启动清理只能移除过期成员，不能删除其他健康实例的槽位")
}

func TestConcurrencyCacheGroupIngressTracksOriginalGroupAndReleases(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	require.NoError(t, cache.TrackGroupIngressSlot(ctx, 7, "request-a"))
	require.NoError(t, cache.TrackGroupIngressSlot(ctx, 7, "request-b"))
	require.NoError(t, cache.TrackGroupIngressSlot(ctx, 8, "request-c"))

	counts, err := cache.GetGroupIngressConcurrencyBatch(ctx, []int64{7, 8, 9})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{7: 2, 8: 1, 9: 0}, counts)

	require.NoError(t, cache.ReleaseGroupIngressSlot(ctx, 7, "request-a"))
	counts, err = cache.GetGroupIngressConcurrencyBatch(ctx, []int64{7, 8})
	require.NoError(t, err)
	require.Equal(t, 1, counts[7])
	require.Equal(t, 1, counts[8], "释放入口分组 A 的请求不能影响其他入口分组")
}

func TestConcurrencyCacheGroupIngressReportsThirtySeventyTrafficSplit(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	var waitGroup sync.WaitGroup
	errorsCh := make(chan error, 100)
	track := func(groupID int64, prefix string, count int) {
		for index := 0; index < count; index++ {
			waitGroup.Add(1)
			go func(index int) {
				defer waitGroup.Done()
				errorsCh <- cache.TrackGroupIngressSlot(ctx, groupID, fmt.Sprintf("%s-%d", prefix, index))
			}(index)
		}
	}
	track(101, "group-a", 30)
	track(202, "group-b", 70)
	waitGroup.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}

	counts, err := cache.GetGroupIngressConcurrencyBatch(ctx, []int64{101, 202})
	require.NoError(t, err)
	require.Equal(t, 30, counts[101])
	require.Equal(t, 70, counts[202])
}

func TestConcurrencyCacheGroupIngressRefreshOnlyRenewsExistingMember(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	require.NoError(t, cache.TrackGroupIngressSlot(ctx, 7, "request-a"))
	owned, err := cache.RefreshGroupIngressSlot(ctx, 7, "request-a")
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, cache.ReleaseGroupIngressSlot(ctx, 7, "request-a"))
	owned, err = cache.RefreshGroupIngressSlot(ctx, 7, "request-a")
	require.NoError(t, err)
	require.False(t, owned, "续租不能复活已经释放的入口统计槽位")
}

func TestConcurrencyCacheCleanupStaleProcessSlotsPreservesOtherIngressInstances(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()
	key := groupIngressSlotKey(7)
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)

	require.NoError(t, rdb.ZAdd(ctx, key,
		redis.Z{Score: float64(now.Unix()), Member: "other-healthy-process-request"},
		redis.Z{Score: float64(now.Unix()), Member: "active-process-request"},
		redis.Z{Score: float64(now.Unix() - 61), Member: "expired-process-request"},
	).Err())
	require.NoError(t, cache.CleanupStaleProcessSlots(ctx, "active-process-"))
	members, err := rdb.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"active-process-request", "other-healthy-process-request"}, members)
}

func TestConcurrencyCacheFailoverGroupSlotHonorsPersonalAndOrganizationLimits(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	acquired, err := cache.AcquireFailoverGroupSlot(ctx, 0, 7, 20, 1, "personal-1")
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireFailoverGroupSlot(ctx, 0, 7, 20, 1, "personal-2")
	require.NoError(t, err)
	require.False(t, acquired)
	require.NoError(t, cache.ReleaseFailoverGroupSlot(ctx, 0, 7, 20, "personal-1"))
	acquired, err = cache.AcquireFailoverGroupSlot(ctx, 0, 7, 20, 1, "personal-2")
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = cache.AcquireFailoverGroupSlot(ctx, 88, 9, 20, 1, "org-1")
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireFailoverGroupSlot(ctx, 88, 9, 20, 1, "org-2")
	require.NoError(t, err)
	require.False(t, acquired)
}
