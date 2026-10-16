package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSeedanceBillingClaimOwnershipAndRecovery(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	ctx := context.Background()
	claimed, err := cache.ClaimSeedanceBilling(ctx, "task-a", "worker-a", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = cache.ClaimSeedanceBilling(ctx, "task-a", "worker-b", time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)
	released, err := cache.ReleaseSeedanceBilling(ctx, "task-a", "worker-b")
	require.NoError(t, err)
	require.False(t, released)

	// 旧 worker 超时后，新 worker 可以接管；旧 worker 不能释放或确认新认领。
	mini.FastForward(2 * time.Minute)
	claimed, err = cache.ClaimSeedanceBilling(ctx, "task-a", "worker-b", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	released, err = cache.ReleaseSeedanceBilling(ctx, "task-a", "worker-a")
	require.NoError(t, err)
	require.False(t, released)
	confirmed, err := cache.ConfirmSeedanceBilling(ctx, "task-a", "worker-a", time.Hour)
	require.NoError(t, err)
	require.False(t, confirmed)
	confirmed, err = cache.ConfirmSeedanceBilling(ctx, "task-a", "worker-b", time.Hour)
	require.NoError(t, err)
	require.True(t, confirmed)
	released, err = cache.ReleaseSeedanceBilling(ctx, "task-a", "worker-b")
	require.NoError(t, err)
	require.False(t, released, "完成状态不能作为认领删除")
	claimed, err = cache.ClaimSeedanceBilling(ctx, "task-a", "worker-c", time.Minute)
	require.NoError(t, err)
	require.False(t, claimed)

	claimed, err = cache.ClaimSeedanceBilling(ctx, "task-retry", "worker-a", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	released, err = cache.ReleaseSeedanceBilling(ctx, "task-retry", "worker-a")
	require.NoError(t, err)
	require.True(t, released)
	claimed, err = cache.ClaimSeedanceBilling(ctx, "task-retry", "worker-b", time.Minute)
	require.NoError(t, err)
	require.True(t, claimed, "失败且仅释放自身认领后应可重新结算")
}

func TestSeedanceBillingClaimConcurrent(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &gatewayCache{rdb: client}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := cache.ClaimSeedanceBilling(context.Background(), "shared-task", "worker", time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if claimed {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, winners.Load())
}
