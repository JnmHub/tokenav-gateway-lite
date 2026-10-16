package repository

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func liveRedisSoakDuration(t *testing.T) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("SUB2API_LIVE_SOAK_DURATION"))
	if raw == "" {
		t.Skip("set SUB2API_LIVE_SOAK_DURATION to run the Live Redis stability gate")
	}
	duration, err := time.ParseDuration(raw)
	require.NoError(t, err)
	require.Positive(t, duration)
	return duration
}

func liveRedisSoakSessions() int {
	const defaultSessions = 64
	raw := strings.TrimSpace(os.Getenv("SUB2API_LIVE_REDIS_SOAK_SESSIONS"))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultSessions
	}
	return value
}

func liveRedisSoakFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// TestLiveRedisLeaseSoak 持续刷新共享账号、用户和 Key 的 Live lease（实时会话租约），
// 同时制造独立租约 churn（反复占用和释放）。该门禁只在发布时显式开启，普通测试跳过。
func TestLiveRedisLeaseSoak(t *testing.T) {
	duration := liveRedisSoakDuration(t)
	sessions := liveRedisSoakSessions()
	const churnWorkers = 8
	if testing.Short() {
		t.Skip("Live Redis soak is disabled in short mode")
	}

	runtime.GC()
	var baselineMemory runtime.MemStats
	runtime.ReadMemStats(&baselineMemory)
	baselineRoutines := runtime.NumGoroutine()
	baselineFD := liveRedisSoakFDCount()
	maxHeap := baselineMemory.HeapAlloc
	maxRoutines := baselineRoutines
	maxFD := baselineFD

	mini := miniredis.RunT(t)
	poolSize := sessions + 16
	rdb := redis.NewClient(&redis.Options{
		Addr:         mini.Addr(),
		PoolSize:     poolSize,
		MinIdleConns: 4,
	})
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 90, waitQueueTTLSeconds: 90}
	ctx, cancel := context.WithCancel(context.Background())

	const accountID = int64(501)
	const userID = int64(502)
	const groupID = int64(504)
	const apiKeyID = int64(-503)
	for index := 0; index < sessions; index++ {
		leaseID := "steady-" + strconv.Itoa(index)
		acquired, err := cache.AcquireLiveLease(ctx, accountID, sessions, userID, sessions, groupID, sessions, apiKeyID, leaseID, false)
		require.NoError(t, err)
		require.True(t, acquired)
	}

	var operationErrors atomic.Int64
	var acquireErrors atomic.Int64
	var refreshErrors atomic.Int64
	var releaseErrors atomic.Int64
	var refreshOperations atomic.Int64
	var churnOperations atomic.Int64
	var workers sync.WaitGroup
	for index := 0; index < sessions; index++ {
		leaseID := "steady-" + strconv.Itoa(index)
		workers.Add(1)
		go func(leaseID string) {
			defer workers.Done()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					if err := cache.ReleaseLiveLease(context.Background(), accountID, userID, groupID, apiKeyID, leaseID); err != nil {
						operationErrors.Add(1)
						releaseErrors.Add(1)
					}
					return
				case <-ticker.C:
					refreshed, err := cache.RefreshLiveLease(ctx, accountID, userID, groupID, apiKeyID, leaseID)
					if err != nil || !refreshed {
						if ctx.Err() == nil {
							operationErrors.Add(1)
							refreshErrors.Add(1)
						}
						continue
					}
					refreshOperations.Add(1)
				}
			}
		}(leaseID)
	}

	for worker := 0; worker < churnWorkers; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			sequence := 0
			for ctx.Err() == nil {
				leaseID := "churn-" + strconv.Itoa(worker) + "-" + strconv.Itoa(sequence)
				id := int64(1000 + worker)
				acquired, err := cache.AcquireLiveLease(ctx, id, 1, id, 1, id, 1, -id, leaseID, false)
				if err != nil {
					if ctx.Err() == nil {
						operationErrors.Add(1)
						acquireErrors.Add(1)
					}
					return
				}
				if !acquired {
					operationErrors.Add(1)
					acquireErrors.Add(1)
					continue
				}
				refreshed, refreshErr := cache.RefreshLiveLease(ctx, id, id, id, -id, leaseID)
				if refreshErr != nil || !refreshed {
					if ctx.Err() == nil {
						operationErrors.Add(1)
						refreshErrors.Add(1)
					}
				}
				// Acquire 可能与总门禁取消同时完成；清理不能复用已取消的 ctx，
				// 否则测试本身会制造一个永远留在 Redis 的假租约。
				if releaseErr := cache.ReleaseLiveLease(context.Background(), id, id, id, -id, leaseID); releaseErr != nil {
					operationErrors.Add(1)
					releaseErrors.Add(1)
				}
				churnOperations.Add(1)
				sequence++
			}
		}()
	}

	deadline := time.NewTimer(duration)
	sampleTicker := time.NewTicker(time.Minute)
	for {
		select {
		case <-deadline.C:
			goto completed
		case <-sampleTicker.C:
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			if memory.HeapAlloc > maxHeap {
				maxHeap = memory.HeapAlloc
			}
			if count := runtime.NumGoroutine(); count > maxRoutines {
				maxRoutines = count
			}
			if count := liveRedisSoakFDCount(); count > maxFD {
				maxFD = count
			}
			t.Logf("Live Redis soak progress: refresh=%d churn=%d heap=%d goroutines=%d fd=%d", refreshOperations.Load(), churnOperations.Load(), memory.HeapAlloc, runtime.NumGoroutine(), liveRedisSoakFDCount())
		}
	}

completed:
	deadline.Stop()
	sampleTicker.Stop()
	cancel()
	workers.Wait()
	t.Logf("Live Redis operation errors: total=%d acquire=%d refresh=%d release=%d", operationErrors.Load(), acquireErrors.Load(), refreshErrors.Load(), releaseErrors.Load())
	require.Zero(t, operationErrors.Load())
	require.Greater(t, refreshOperations.Load(), int64(sessions))
	require.Positive(t, churnOperations.Load())
	require.EqualValues(t, 0, rdb.ZCard(context.Background(), liveAccountSlotKey(accountID)).Val())
	require.EqualValues(t, 0, rdb.ZCard(context.Background(), liveUserSlotKey(userID)).Val())
	require.EqualValues(t, 0, rdb.ZCard(context.Background(), liveAPIKeySlotKey(apiKeyID)).Val())
	for worker := 0; worker < churnWorkers; worker++ {
		id := int64(1000 + worker)
		require.EqualValues(t, 0, rdb.ZCard(context.Background(), liveAccountSlotKey(id)).Val())
		require.EqualValues(t, 0, rdb.ZCard(context.Background(), liveUserSlotKey(id)).Val())
		require.EqualValues(t, 0, rdb.ZCard(context.Background(), liveAPIKeySlotKey(-id)).Val())
	}

	require.NoError(t, rdb.Close())
	mini.Close()
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	var finalMemory runtime.MemStats
	runtime.ReadMemStats(&finalMemory)
	finalRoutines := runtime.NumGoroutine()
	finalFD := liveRedisSoakFDCount()
	require.LessOrEqual(t, finalMemory.HeapAlloc, baselineMemory.HeapAlloc+24*1024*1024)
	require.LessOrEqual(t, maxHeap, baselineMemory.HeapAlloc+128*1024*1024)
	require.LessOrEqual(t, finalRoutines, baselineRoutines+12)
	// miniredis 与被测客户端运行在同一进程：连接池中每条活跃连接会同时产生
	// 客户端等待/读写协程和服务端连接协程。这些属于测试夹具成本，不是 Live 租约泄漏。
	maxExpectedRoutines := baselineRoutines + sessions + churnWorkers + 2*poolSize
	require.LessOrEqual(t, maxRoutines, maxExpectedRoutines)
	if baselineFD >= 0 && finalFD >= 0 {
		require.LessOrEqual(t, finalFD, baselineFD+4)
		// 同进程 miniredis 会同时持有连接的客户端和服务端 FD，因此按连接池两端预算。
		maxExpectedFD := baselineFD + 2*poolSize
		require.LessOrEqual(t, maxFD, maxExpectedFD)
	}
	t.Logf("Live Redis soak passed: duration=%s sessions=%d refresh=%d churn=%d heap=%d/%d/%d goroutines=%d/%d/%d fd=%d/%d/%d", duration, sessions, refreshOperations.Load(), churnOperations.Load(), baselineMemory.HeapAlloc, maxHeap, finalMemory.HeapAlloc, baselineRoutines, maxRoutines, finalRoutines, baselineFD, maxFD, finalFD)
}
