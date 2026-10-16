package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type billingCacheWorkerStub struct {
	balanceUpdates      int64
	subscriptionUpdates int64
	rateLimitUpdates    int64
	balanceValue        int64
	subscriptionValue   int64
}

func (b *billingCacheWorkerStub) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	atomic.StoreInt64(&b.balanceValue, int64(balance))
	return nil
}

func (b *billingCacheWorkerStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	atomic.AddInt64(&b.balanceUpdates, 1)
	atomic.AddInt64(&b.balanceValue, -int64(amount))
	return nil
}

func (b *billingCacheWorkerStub) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetSubscriptionCache(ctx context.Context, userID, groupID int64) (*SubscriptionCacheData, error) {
	return nil, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetSubscriptionCache(ctx context.Context, userID, groupID int64, data *SubscriptionCacheData) error {
	atomic.AddInt64(&b.subscriptionUpdates, 1)
	if data != nil {
		atomic.StoreInt64(&b.subscriptionValue, int64(data.DailyUsage))
	}
	return nil
}

func (b *billingCacheWorkerStub) UpdateSubscriptionUsage(ctx context.Context, userID, groupID int64, cost float64) error {
	atomic.AddInt64(&b.subscriptionUpdates, 1)
	atomic.AddInt64(&b.subscriptionValue, int64(cost))
	return nil
}

func (b *billingCacheWorkerStub) InvalidateSubscriptionCache(ctx context.Context, userID, groupID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetAPIKeyRateLimit(ctx context.Context, keyID int64) (*APIKeyRateLimitCacheData, error) {
	return nil, errors.New("not implemented")
}

func (b *billingCacheWorkerStub) SetAPIKeyRateLimit(ctx context.Context, keyID int64, data *APIKeyRateLimitCacheData) error {
	return nil
}

func (b *billingCacheWorkerStub) UpdateAPIKeyRateLimitUsage(ctx context.Context, keyID int64, cost float64) error {
	atomic.AddInt64(&b.rateLimitUpdates, 1)
	return nil
}

func (b *billingCacheWorkerStub) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	return nil
}

func (b *billingCacheWorkerStub) GetUserPlatformQuotaCache(ctx context.Context, userID int64, platform string) (*UserPlatformQuotaCacheEntry, bool, error) {
	return nil, false, nil
}

func (b *billingCacheWorkerStub) SetUserPlatformQuotaCache(ctx context.Context, userID int64, platform string, entry *UserPlatformQuotaCacheEntry, ttl time.Duration) error {
	return nil
}

func (b *billingCacheWorkerStub) DeleteUserPlatformQuotaCache(ctx context.Context, userID int64, platform string) error {
	return nil
}

func (b *billingCacheWorkerStub) IncrUserPlatformQuotaUsageCache(ctx context.Context, userID int64, platform string, cost float64, ttl time.Duration, markDirty bool) error {
	return nil
}

func (b *billingCacheWorkerStub) PopDirtyUserPlatformQuotaKeys(ctx context.Context, n int) ([]UserPlatformQuotaKey, error) {
	return nil, nil
}

func (b *billingCacheWorkerStub) ReaddDirtyUserPlatformQuotaKeys(ctx context.Context, keys []UserPlatformQuotaKey) error {
	return nil
}

func (b *billingCacheWorkerStub) BatchGetUserPlatformQuotaCache(ctx context.Context, keys []UserPlatformQuotaKey) ([]*UserPlatformQuotaCacheEntry, error) {
	return nil, nil
}

type billingCacheDeadlineStub struct {
	billingCacheWorkerStub
}

func (b *billingCacheDeadlineStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	<-ctx.Done()
	return ctx.Err()
}

type billingCacheBlockingSetStub struct {
	billingCacheWorkerStub
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *billingCacheBlockingSetStub) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.release:
		return b.billingCacheWorkerStub.SetUserBalance(ctx, userID, balance)
	case <-ctx.Done():
		return ctx.Err()
	}
}

type billingCacheFailingSetStub struct {
	billingCacheWorkerStub
}

func (b *billingCacheFailingSetStub) SetUserBalance(context.Context, int64, float64) error {
	return errors.New("forced cache write failure")
}

func TestBillingCacheServiceQueueHighLoad(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	start := time.Now()
	for i := 0; i < defaultCriticalCacheWriteBufferSize*2; i++ {
		svc.QueueDeductBalance(1, 1)
	}
	require.Less(t, time.Since(start), 2*time.Second)

	svc.QueueUpdateSubscriptionUsage(1, 2, 1.5)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.balanceUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&cache.subscriptionUpdates) > 0
	}, 2*time.Second, 10*time.Millisecond)
}

func TestBillingCacheServiceEnqueueAfterStopReturnsFalse(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	svc.Stop()

	task := cacheWriteTask{
		kind:   cacheWriteDeductBalance,
		userID: 1,
		amount: 1,
	}
	svc.prepareCacheWriteTask(&task)
	result := svc.enqueueCacheWrite(task, cacheWriteClassCritical)
	releaseCacheWriteSubmission(task)
	completeCacheWriteSequence(task)
	svc.finishCacheWriteTask(task)
	require.Equal(t, cacheWriteEnqueueClosed, result)
	require.Zero(t, atomic.LoadInt64(&cache.balanceUpdates))
}

func TestBillingCacheServiceQueueAfterStopDoesNotFallback(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	svc.Stop()

	svc.QueueDeductBalance(1, 2)

	require.Zero(t, atomic.LoadInt64(&cache.balanceUpdates))
	require.Zero(t, atomic.LoadUint64(&svc.cacheWriteCriticalFallbackTotal))
}

func TestBillingCacheServiceConcurrentStopAndSubmit(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	start := make(chan struct{})
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func(userID int64) {
			<-start
			for j := 0; j < 100; j++ {
				svc.QueueDeductBalance(userID, 1)
			}
			done <- struct{}{}
		}(int64(i + 1))
	}
	close(start)
	time.Sleep(time.Millisecond)
	svc.Stop()
	for i := 0; i < 16; i++ {
		<-done
	}

	require.True(t, svc.RuntimeStats().Stopped)
}

func TestBillingCacheServiceRuntimeStats(t *testing.T) {
	svc := &BillingCacheService{
		criticalCacheWriteChan:   make(chan cacheWriteTask, 2),
		bestEffortCacheWriteChan: make(chan cacheWriteTask, 3),
		cacheWriteOptions: cacheWritePoolOptions{
			criticalWorkers: 4, bestEffortWorkers: 2,
		},
	}
	svc.criticalCacheWriteChan <- cacheWriteTask{kind: cacheWriteDeductBalance, userID: 1, amount: 1}
	svc.bestEffortCacheWriteChan <- cacheWriteTask{kind: cacheWriteSetBalance, userID: 2, balance: 4}
	svc.logCacheWriteDrop(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1}, "full")
	svc.logCacheWriteDrop(cacheWriteTask{kind: cacheWriteSetBalance, userID: 2}, "closed")
	atomic.AddUint64(&svc.cacheWriteCriticalFullTotal, 2)
	atomic.AddUint64(&svc.cacheWriteCriticalFallbackTotal, 2)
	svc.stopped.Store(true)

	stats := svc.RuntimeStats()
	require.Equal(t, 2, stats.QueueLength)
	require.Equal(t, 5, stats.QueueCapacity)
	require.Equal(t, 6, stats.WorkerCount)
	require.Equal(t, 1, stats.CriticalQueueLength)
	require.Equal(t, 1, stats.BestEffortQueueLength)
	require.EqualValues(t, 2, stats.CriticalQueueFullTotal)
	require.EqualValues(t, 2, stats.CriticalSyncFallbackTotal)
	require.EqualValues(t, 1, stats.DroppedFullTotal)
	require.EqualValues(t, 1, stats.DroppedClosedTotal)
	require.True(t, stats.Stopped)
}

func TestBillingCacheServiceUsesConfiguredPools(t *testing.T) {
	cfg := &config.Config{}
	cfg.Billing.CacheWrite = config.BillingCacheWriteConfig{
		CriticalWorkerCount: 3, CriticalBufferSize: 17,
		BestEffortWorkerCount: 2, BestEffortBufferSize: 9,
		TimeoutMilliseconds: 75,
	}
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)

	stats := svc.RuntimeStats()
	require.Equal(t, 3, stats.CriticalWorkerCount)
	require.Equal(t, 17, stats.CriticalQueueCapacity)
	require.Equal(t, 2, stats.BestEffortWorkerCount)
	require.Equal(t, 9, stats.BestEffortQueueCapacity)
	require.Equal(t, 75*time.Millisecond, svc.cacheWriteTimeout())
}

func TestBillingCacheServiceCriticalQueueFullFallsBack(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := &BillingCacheService{
		cache:                  cache,
		criticalCacheWriteChan: make(chan cacheWriteTask, 1),
		cacheWriteOptions:      cacheWritePoolOptions{criticalWorkers: 1, criticalBuffer: 1, timeout: time.Second},
	}
	svc.criticalCacheWriteChan <- cacheWriteTask{kind: cacheWriteDeductBalance, userID: 99}

	svc.QueueDeductBalance(1, 2)
	svc.QueueUpdateAPIKeyRateLimitUsage(7, 1)

	require.EqualValues(t, 2, atomic.LoadUint64(&svc.cacheWriteCriticalFullTotal))
	require.EqualValues(t, 2, atomic.LoadUint64(&svc.cacheWriteCriticalFallbackTotal))
	require.EqualValues(t, 0, atomic.LoadUint64(&svc.cacheWriteCriticalFallbackFailed))
	require.EqualValues(t, 1, atomic.LoadInt64(&cache.balanceUpdates))
	require.EqualValues(t, 1, atomic.LoadInt64(&cache.rateLimitUpdates))
}

func TestBillingCacheServiceBestEffortQueueFullKeepsLatestRefresh(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := &BillingCacheService{
		cache:                    cache,
		bestEffortCacheWriteChan: make(chan cacheWriteTask, 1),
		cacheWriteOptions:        cacheWritePoolOptions{bestEffortWorkers: 1, bestEffortBuffer: 1, timeout: time.Second},
	}
	svc.bestEffortCacheWriteChan <- cacheWriteTask{kind: cacheWriteSetBalance, userID: 99}

	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 20})
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 30})

	stats := svc.RuntimeStats()
	require.EqualValues(t, 0, stats.BestEffortDroppedFullTotal)
	require.EqualValues(t, 1, stats.BestEffortDeferredLength)
	require.EqualValues(t, 1, stats.BestEffortDeferredTotal)
	require.EqualValues(t, 1, stats.BestEffortMergedTotal)

	latest, ok := svc.takeBestEffortDeferred()
	require.True(t, ok)
	require.Equal(t, float64(30), latest.balance)
	require.NoError(t, svc.executeCacheWriteTask(latest))
	require.EqualValues(t, 30, atomic.LoadInt64(&cache.balanceValue))
}

func TestBillingCacheServiceBestEffortOverflowBoundFallsBackSynchronously(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := &BillingCacheService{
		cache:                    cache,
		bestEffortCacheWriteChan: make(chan cacheWriteTask, 1),
		cacheWriteOptions:        cacheWritePoolOptions{bestEffortWorkers: 1, bestEffortBuffer: 1, timeout: time.Second},
	}
	svc.bestEffortCacheWriteChan <- cacheWriteTask{kind: cacheWriteSetBalance, userID: 99}

	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 20})
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 2, balance: 40})

	stats := svc.RuntimeStats()
	require.EqualValues(t, 1, stats.BestEffortDeferredLength)
	require.EqualValues(t, 1, stats.BestEffortSyncFallbackTotal)
	require.EqualValues(t, 0, stats.BestEffortFallbackFailed)
	require.EqualValues(t, 1, atomic.LoadInt64(&cache.balanceUpdates))
}

func TestBillingCacheServiceBestEffortFallbackFailureIsObservable(t *testing.T) {
	cache := &billingCacheFailingSetStub{}
	svc := &BillingCacheService{
		cache:                    cache,
		bestEffortCacheWriteChan: make(chan cacheWriteTask, 1),
		cacheWriteOptions:        cacheWritePoolOptions{bestEffortWorkers: 1, bestEffortBuffer: 1, timeout: time.Second},
	}
	svc.bestEffortCacheWriteChan <- cacheWriteTask{kind: cacheWriteSetBalance, userID: 99}

	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 20})
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 2, balance: 40})

	stats := svc.RuntimeStats()
	require.EqualValues(t, 1, stats.BestEffortSyncFallbackTotal)
	require.EqualValues(t, 1, stats.BestEffortFallbackFailed)
	deferred, ok := svc.takeBestEffortDeferred()
	require.True(t, ok)
	completeCacheWriteSequence(deferred)
	svc.finishCacheWriteTask(deferred)
}

func TestBillingCacheServiceMergedRefreshPrecedesCriticalDelta(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := &BillingCacheService{
		cache:                    cache,
		criticalCacheWriteChan:   make(chan cacheWriteTask, 1),
		bestEffortCacheWriteChan: make(chan cacheWriteTask, 1),
		cacheWriteOptions: cacheWritePoolOptions{
			criticalWorkers: 1, criticalBuffer: 1,
			bestEffortWorkers: 1, bestEffortBuffer: 1,
			timeout: time.Second,
		},
	}
	svc.criticalCacheWriteChan <- cacheWriteTask{kind: cacheWriteDeductBalance, userID: 99}
	svc.bestEffortCacheWriteChan <- cacheWriteTask{kind: cacheWriteSetBalance, userID: 99}
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 20})
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 30})

	deltaDone := make(chan struct{})
	go func() {
		svc.QueueDeductBalance(1, 5)
		close(deltaDone)
	}()
	require.Eventually(t, func() bool {
		return atomic.LoadUint64(&svc.cacheWriteOrderingWaitTotal) > 0
	}, time.Second, time.Millisecond)

	latest, ok := svc.takeBestEffortDeferred()
	require.True(t, ok)
	require.Equal(t, float64(30), latest.balance)
	require.NoError(t, svc.executeCacheWriteTask(latest))
	select {
	case <-deltaDone:
	case <-time.After(time.Second):
		t.Fatal("critical delta did not resume after the merged refresh")
	}
	require.EqualValues(t, 25, atomic.LoadInt64(&cache.balanceValue))
	require.EqualValues(t, 1, svc.RuntimeStats().BestEffortMergedTotal)
}

func TestBillingCacheServiceCrossQueuePreservesPerKeyOrder(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := &BillingCacheService{cache: cache, cacheWriteOptions: cacheWritePoolOptions{timeout: time.Second}}
	absolute := cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 100}
	delta := cacheWriteTask{kind: cacheWriteDeductBalance, userID: 1, amount: 10}
	svc.prepareCacheWriteTask(&absolute)
	releaseCacheWriteSubmission(absolute)
	svc.prepareCacheWriteTask(&delta)
	releaseCacheWriteSubmission(delta)

	deltaDone := make(chan error, 1)
	go func() { deltaDone <- svc.executeCacheWriteTask(delta) }()
	require.Eventually(t, func() bool {
		return atomic.LoadUint64(&svc.cacheWriteOrderingWaitTotal) == 1
	}, time.Second, time.Millisecond)
	require.NoError(t, svc.executeCacheWriteTask(absolute))
	require.NoError(t, <-deltaDone)
	require.EqualValues(t, 90, atomic.LoadInt64(&cache.balanceValue))
	require.EqualValues(t, 2, atomic.LoadInt64(&cache.balanceUpdates))
}

func TestBillingCacheServiceCrossQueuePreservesSubscriptionOrder(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := &BillingCacheService{cache: cache, cacheWriteOptions: cacheWritePoolOptions{timeout: time.Second}}
	absolute := cacheWriteTask{
		kind: cacheWriteSetSubscription, userID: 1, groupID: 2,
		subscriptionData: &subscriptionCacheData{DailyUsage: 100},
	}
	delta := cacheWriteTask{kind: cacheWriteUpdateSubscriptionUsage, userID: 1, groupID: 2, amount: 10}
	svc.prepareCacheWriteTask(&absolute)
	releaseCacheWriteSubmission(absolute)
	svc.prepareCacheWriteTask(&delta)
	releaseCacheWriteSubmission(delta)

	deltaDone := make(chan error, 1)
	go func() { deltaDone <- svc.executeCacheWriteTask(delta) }()
	require.Eventually(t, func() bool {
		return atomic.LoadUint64(&svc.cacheWriteOrderingWaitTotal) == 1
	}, time.Second, time.Millisecond)
	require.NoError(t, svc.executeCacheWriteTask(absolute))
	require.NoError(t, <-deltaDone)
	require.EqualValues(t, 110, atomic.LoadInt64(&cache.subscriptionValue))
}

func TestBillingCacheServiceConfiguredTimeoutBoundsRedisWrite(t *testing.T) {
	cache := &billingCacheDeadlineStub{}
	svc := &BillingCacheService{
		cache:             cache,
		cacheWriteOptions: cacheWritePoolOptions{timeout: 25 * time.Millisecond},
	}

	started := time.Now()
	err := svc.executeCacheWriteTask(cacheWriteTask{kind: cacheWriteDeductBalance, userID: 1, amount: 1})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.GreaterOrEqual(t, time.Since(started), 20*time.Millisecond)
	require.Less(t, time.Since(started), 250*time.Millisecond)
}

func TestBillingCacheServiceStopDrainsBothQueues(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	for i := 0; i < 20; i++ {
		svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: int64(i + 1), balance: 100})
		svc.QueueDeductBalance(int64(i+1), 1)
	}
	svc.Stop()

	require.EqualValues(t, 40, atomic.LoadInt64(&cache.balanceUpdates))
	stats := svc.RuntimeStats()
	require.True(t, stats.Stopped)
	require.Zero(t, stats.QueueLength)
	require.Zero(t, stats.ActiveWorkerCount)
}

func TestBillingCacheServiceStopDrainsDeferredRefreshes(t *testing.T) {
	cache := &billingCacheBlockingSetStub{started: make(chan struct{}), release: make(chan struct{})}
	cfg := &config.Config{}
	cfg.Billing.CacheWrite = config.BillingCacheWriteConfig{
		Preset:              config.BillingCacheWritePresetLow,
		CriticalWorkerCount: 1, CriticalBufferSize: 1,
		BestEffortWorkerCount: 1, BestEffortBufferSize: 1,
		TimeoutMilliseconds: 1000,
	}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 1, balance: 10})
	select {
	case <-cache.started:
	case <-time.After(time.Second):
		t.Fatal("best-effort worker did not start")
	}
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 2, balance: 20})
	svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: 3, balance: 30})
	require.Eventually(t, func() bool {
		return svc.RuntimeStats().BestEffortDeferredLength == 1
	}, time.Second, time.Millisecond)

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()
	close(cache.release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not drain queued and deferred refreshes")
	}
	require.EqualValues(t, 3, atomic.LoadInt64(&cache.balanceUpdates))
	stats := svc.RuntimeStats()
	require.Zero(t, stats.QueueLength)
	require.Zero(t, stats.BestEffortDeferredLength)
	require.Zero(t, stats.ActiveWorkerCount)
}

func TestBillingCacheServiceReloadDrainsOldGeneration(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	cfg := &config.Config{}
	cfg.Billing.CacheWrite = config.BillingCacheWriteConfig{
		Preset: config.BillingCacheWritePresetLow,
	}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, cfg, nil)

	for i := 0; i < 200; i++ {
		svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: int64(i + 1), balance: float64(i)})
	}
	require.NoError(t, svc.ReloadCacheWriteConfig(config.BillingCacheWriteConfig{
		Preset: config.BillingCacheWritePresetHigh,
	}))
	for i := 0; i < 200; i++ {
		svc.QueueDeductBalance(int64(i+1), 1)
	}
	svc.Stop()

	stats := svc.RuntimeStats()
	require.Equal(t, config.BillingCacheWritePresetHigh, stats.Preset)
	require.EqualValues(t, 1, stats.ReloadTotal)
	require.True(t, stats.Stopped)
	require.Zero(t, stats.QueueLength)
	require.EqualValues(t, 400, atomic.LoadInt64(&cache.balanceUpdates))
}

func TestBillingCacheServiceReloadRejectsInvalidAndStopped(t *testing.T) {
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, nil, nil, nil, nil, nil, &config.Config{}, nil)
	require.ErrorContains(t, svc.ReloadCacheWriteConfig(config.BillingCacheWriteConfig{Preset: "fastest"}), "preset")
	require.ErrorContains(t, svc.ReloadCacheWriteConfig(config.BillingCacheWriteConfig{
		Preset: config.BillingCacheWritePresetLow, CriticalWorkerCount: 129,
	}), "critical_worker_count")
	svc.Stop()
	require.ErrorContains(t, svc.ReloadCacheWriteConfig(config.BillingCacheWriteConfig{Preset: config.BillingCacheWritePresetStandard}), "stopped")
}

func TestBillingCacheServiceReloadCASRejectsStaleRevision(t *testing.T) {
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)
	require.EqualValues(t, 1, svc.RuntimeStats().Revision)

	require.NoError(t, svc.ReloadCacheWriteConfigCAS(config.BillingCacheWriteConfig{
		Preset: config.BillingCacheWritePresetHigh,
	}, 1))
	require.EqualValues(t, 2, svc.RuntimeStats().Revision)
	require.Equal(t, config.BillingCacheWritePresetHigh, svc.CurrentCacheWriteConfig().Preset)

	err := svc.ReloadCacheWriteConfigCAS(config.BillingCacheWriteConfig{
		Preset: config.BillingCacheWritePresetLow,
	}, 1)
	require.ErrorIs(t, err, ErrBillingCacheWriteRevisionConflict)
	require.EqualValues(t, 2, svc.RuntimeStats().Revision)
	require.Equal(t, config.BillingCacheWritePresetHigh, svc.CurrentCacheWriteConfig().Preset)
}

func TestBillingCacheServiceConcurrentReloadSubmitAndStop(t *testing.T) {
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, nil, nil, nil, nil, nil, &config.Config{}, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for request := 0; request < 250; request++ {
				userID := int64(worker*250 + request + 1)
				svc.submitCacheWrite(cacheWriteTask{kind: cacheWriteSetBalance, userID: userID, balance: 100})
				svc.QueueDeductBalance(userID, 1)
			}
		}(worker)
	}
	for reloader := 0; reloader < 3; reloader++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			<-start
			presets := []string{
				config.BillingCacheWritePresetLow,
				config.BillingCacheWritePresetStandard,
				config.BillingCacheWritePresetHigh,
			}
			for round := 0; round < 12; round++ {
				_ = svc.ReloadCacheWriteConfig(config.BillingCacheWriteConfig{Preset: presets[(round+offset)%len(presets)]})
			}
		}(reloader)
	}
	close(start)
	time.AfterFunc(2*time.Millisecond, svc.Stop)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		svc.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent reload, submit and stop did not converge")
	}
	stats := svc.RuntimeStats()
	require.True(t, stats.Stopped)
	require.Zero(t, stats.QueueLength)
	require.Zero(t, stats.ActiveWorkerCount)
}
