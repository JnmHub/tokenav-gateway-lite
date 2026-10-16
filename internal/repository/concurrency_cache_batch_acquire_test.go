package repository

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newBatchAcquireConcurrencyCache(t *testing.T) (*concurrencyCache, *redis.Client) {
	t.Helper()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}, rdb
}

func TestAcquireFirstAvailableAccountSlotSelectsOnlyFirstAvailable(t *testing.T) {
	cache, rdb := newBatchAcquireConcurrencyCache(t)
	ctx := context.Background()
	require.True(t, mustAcquireAccountSlot(t, cache, ctx, 101, 1, "busy-account"))

	selected, acquired, err := cache.AcquireFirstAvailableAccountSlot(ctx, []service.AccountWithConcurrency{
		{ID: 101, MaxConcurrency: 1},
		{ID: 102, MaxConcurrency: 1},
		{ID: 103, MaxConcurrency: 1},
	}, "batch-request")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, int64(102), selected)
	require.EqualValues(t, 1, rdb.ZCard(ctx, accountSlotKey(101)).Val())
	require.EqualValues(t, 1, rdb.ZCard(ctx, accountSlotKey(102)).Val())
	require.EqualValues(t, 0, rdb.ZCard(ctx, accountSlotKey(103)).Val(), "脚本不能预占未选中的账号")
}

func TestAcquireFirstAvailableAccountSlotCountsLiveLeaseAndUnlimitedAccount(t *testing.T) {
	cache, rdb := newBatchAcquireConcurrencyCache(t)
	ctx := context.Background()
	require.NoError(t, rdb.ZAdd(ctx, liveAccountSlotKey(201), redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: "live-request",
	}).Err())

	selected, acquired, err := cache.AcquireFirstAvailableAccountSlot(ctx, []service.AccountWithConcurrency{
		{ID: 201, MaxConcurrency: 1},
		{ID: 202, MaxConcurrency: 0},
		{ID: 203, MaxConcurrency: 1},
	}, "ordinary-request")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, int64(202), selected)
	require.EqualValues(t, 0, rdb.ZCard(ctx, accountSlotKey(202)).Val(), "无限并发账号不应创建槽位")
	require.EqualValues(t, 0, rdb.ZCard(ctx, accountSlotKey(203)).Val())
}

func TestAcquireFirstAvailableAccountSlotRetryFindsExistingRequestFirst(t *testing.T) {
	cache, rdb := newBatchAcquireConcurrencyCache(t)
	ctx := context.Background()
	require.True(t, mustAcquireAccountSlot(t, cache, ctx, 301, 1, "temporarily-busy"))
	candidates := []service.AccountWithConcurrency{
		{ID: 301, MaxConcurrency: 1},
		{ID: 302, MaxConcurrency: 1},
	}

	selected, acquired, err := cache.AcquireFirstAvailableAccountSlot(ctx, candidates, "idempotent-request")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, int64(302), selected)
	require.NoError(t, cache.ReleaseAccountSlot(ctx, 301, "temporarily-busy"))

	selected, acquired, err = cache.AcquireFirstAvailableAccountSlot(ctx, candidates, "idempotent-request")
	require.NoError(t, err)
	require.True(t, acquired)
	require.Equal(t, int64(302), selected, "重试必须找回原槽位，不能在已恢复的靠前账号重复占槽")
	require.EqualValues(t, 0, rdb.ZCard(ctx, accountSlotKey(301)).Val())
	require.EqualValues(t, 1, rdb.ZCard(ctx, accountSlotKey(302)).Val())
}

func TestAcquireFirstAvailableAccountSlotConcurrentRequestsDoNotExceedLimits(t *testing.T) {
	cache, rdb := newBatchAcquireConcurrencyCache(t)
	ctx := context.Background()
	candidates := []service.AccountWithConcurrency{
		{ID: 401, MaxConcurrency: 3},
		{ID: 402, MaxConcurrency: 3},
	}

	var acquiredCount atomic.Int64
	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	for index := 0; index < 64; index++ {
		wg.Add(1)
		go func(requestIndex int) {
			defer wg.Done()
			_, acquired, err := cache.AcquireFirstAvailableAccountSlot(ctx, candidates, fmt.Sprintf("concurrent-%d", requestIndex))
			if err != nil {
				errCh <- err
				return
			}
			if acquired {
				acquiredCount.Add(1)
			}
		}(index)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	require.EqualValues(t, 6, acquiredCount.Load())
	require.EqualValues(t, 3, rdb.ZCard(ctx, accountSlotKey(401)).Val())
	require.EqualValues(t, 3, rdb.ZCard(ctx, accountSlotKey(402)).Val())
}

func BenchmarkAccountSlotFullLayer(b *testing.B) {
	mini := miniredis.RunT(b)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr(), PoolSize: 128})
	b.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()
	candidates := make([]service.AccountWithConcurrency, 100)
	now := float64(time.Now().Unix())
	for index := range candidates {
		accountID := int64(5000 + index)
		candidates[index] = service.AccountWithConcurrency{ID: accountID, MaxConcurrency: 1}
		require.NoError(b, rdb.ZAdd(ctx, accountSlotKey(accountID), redis.Z{
			Score:  now,
			Member: "busy-" + strconv.Itoa(index),
		}).Err())
	}

	b.Run("sequential_100", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			for _, candidate := range candidates {
				acquired, err := cache.AcquireAccountSlot(ctx, candidate.ID, candidate.MaxConcurrency, "sequential-full")
				if err != nil || acquired {
					b.Fatalf("unexpected sequential acquire result: acquired=%v err=%v", acquired, err)
				}
			}
		}
	})

	b.Run("batched_32_100", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			for batchStart := 0; batchStart < len(candidates); batchStart += 32 {
				batchEnd := batchStart + 32
				if batchEnd > len(candidates) {
					batchEnd = len(candidates)
				}
				_, acquired, err := cache.AcquireFirstAvailableAccountSlot(ctx, candidates[batchStart:batchEnd], "batch-full")
				if err != nil || acquired {
					b.Fatalf("unexpected batch acquire result: acquired=%v err=%v", acquired, err)
				}
			}
		}
	})
}

func BenchmarkAccountSlotHealthyFirstAccount(b *testing.B) {
	mini := miniredis.RunT(b)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr(), PoolSize: 128})
	b.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()
	candidates := make([]service.AccountWithConcurrency, 32)
	for index := range candidates {
		candidates[index] = service.AccountWithConcurrency{ID: int64(7000 + index), MaxConcurrency: 1000000}
	}

	b.Run("single_first", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			requestID := "single-healthy-" + strconv.Itoa(iteration)
			acquired, err := cache.AcquireAccountSlot(ctx, candidates[0].ID, candidates[0].MaxConcurrency, requestID)
			if err != nil || !acquired {
				b.Fatalf("unexpected single acquire result: acquired=%v err=%v", acquired, err)
			}
		}
	})

	b.Run("batched_32_first", func(b *testing.B) {
		for iteration := 0; iteration < b.N; iteration++ {
			requestID := "batch-healthy-" + strconv.Itoa(iteration)
			selected, acquired, err := cache.AcquireFirstAvailableAccountSlot(ctx, candidates, requestID)
			if err != nil || !acquired || selected != candidates[0].ID {
				b.Fatalf("unexpected batch acquire result: account=%d acquired=%v err=%v", selected, acquired, err)
			}
		}
	})
}
