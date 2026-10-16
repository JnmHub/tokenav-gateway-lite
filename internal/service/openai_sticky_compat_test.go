package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestGetStickySessionAccountID_FallbackToLegacyKey(t *testing.T) {
	beforeFallbackTotal, beforeFallbackHit, _ := openAIStickyCompatStats()

	cache := &stubGatewayCache{
		sessionBindings: map[string]int64{
			"openai:legacy-hash": 42,
		},
	}
	svc := &OpenAIGatewayService{
		cache: cache,
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					SessionHashReadOldFallback: true,
				},
			},
		},
	}

	ctx := withOpenAILegacySessionHash(context.Background(), "legacy-hash")
	accountID, err := svc.getStickySessionAccountID(ctx, nil, "new-hash")
	require.NoError(t, err)
	require.Equal(t, int64(42), accountID)

	afterFallbackTotal, afterFallbackHit, _ := openAIStickyCompatStats()
	require.Equal(t, beforeFallbackTotal+1, afterFallbackTotal)
	require.Equal(t, beforeFallbackHit+1, afterFallbackHit)
}

func TestSetStickySessionAccountID_DualWriteOldEnabled(t *testing.T) {
	_, _, beforeDualWriteTotal := openAIStickyCompatStats()

	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	svc := &OpenAIGatewayService{
		cache: cache,
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					SessionHashDualWriteOld: true,
				},
			},
		},
	}

	ctx := withOpenAILegacySessionHash(context.Background(), "legacy-hash")
	err := svc.setStickySessionAccountID(ctx, nil, "new-hash", 9, openaiStickySessionTTL)
	require.NoError(t, err)
	require.Equal(t, int64(9), cache.sessionBindings["openai:new-hash"])
	require.Equal(t, int64(9), cache.sessionBindings["openai:legacy-hash"])

	_, _, afterDualWriteTotal := openAIStickyCompatStats()
	require.Equal(t, beforeDualWriteTotal+1, afterDualWriteTotal)
}

func TestSetStickySessionAccountID_DualWriteOldDisabled(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	svc := &OpenAIGatewayService{
		cache: cache,
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					SessionHashDualWriteOld: false,
				},
			},
		},
	}

	ctx := withOpenAILegacySessionHash(context.Background(), "legacy-hash")
	err := svc.setStickySessionAccountID(ctx, nil, "new-hash", 9, openaiStickySessionTTL)
	require.NoError(t, err)
	require.Equal(t, int64(9), cache.sessionBindings["openai:new-hash"])
	_, exists := cache.sessionBindings["openai:legacy-hash"]
	require.False(t, exists)
}

func TestRefreshStickySessionTTL_CoalescesConcurrentWritesAndInvalidatesOnMutation(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	svc := &OpenAIGatewayService{
		cache:                   cache,
		cfg:                     &config.Config{},
		openaiStickyRefreshGate: newOpenAIStickyRefreshGate(time.Minute, 8),
	}

	const workers = 100
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			errs <- svc.refreshStickySessionTTL(context.Background(), nil, "same-session", time.Hour)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	cache.refreshMu.Lock()
	require.Equal(t, 1, cache.refreshCalls["openai:same-session"])
	cache.refreshMu.Unlock()

	require.NoError(t, svc.setStickySessionAccountID(context.Background(), nil, "same-session", 7, time.Hour))
	require.NoError(t, svc.refreshStickySessionTTL(context.Background(), nil, "same-session", time.Hour))
	require.NoError(t, svc.deleteStickySessionAccountID(context.Background(), nil, "same-session"))
	require.NoError(t, svc.refreshStickySessionTTL(context.Background(), nil, "same-session", time.Hour))

	cache.refreshMu.Lock()
	require.Equal(t, 3, cache.refreshCalls["openai:same-session"])
	cache.refreshMu.Unlock()
}

func TestRefreshStickySessionTTL_RetriesAfterCacheFailure(t *testing.T) {
	cache := &stubGatewayCache{refreshErr: fmt.Errorf("redis unavailable")}
	svc := &OpenAIGatewayService{
		cache:                   cache,
		cfg:                     &config.Config{},
		openaiStickyRefreshGate: newOpenAIStickyRefreshGate(time.Minute, 8),
	}

	require.Error(t, svc.refreshStickySessionTTL(context.Background(), nil, "retry-session", time.Hour))
	require.Error(t, svc.refreshStickySessionTTL(context.Background(), nil, "retry-session", time.Hour))

	cache.refreshMu.Lock()
	require.Equal(t, 2, cache.refreshCalls["openai:retry-session"])
	cache.refreshMu.Unlock()
}

func TestOpenAIStickyRefreshGate_RemainsBounded(t *testing.T) {
	gate := newOpenAIStickyRefreshGate(time.Minute, 2)
	now := time.Now()
	for i := range 1000 {
		key := openAIStickyRefreshKey{groupID: int64(i % 7), sessionHash: fmt.Sprintf("session-%d", i)}
		require.True(t, gate.tryReserve(key, now, time.Hour))
		gate.storeBinding(key, int64(i+1), now)
	}

	total := 0
	for i := range gate.shards {
		shard := &gate.shards[i]
		shard.mu.Lock()
		require.LessOrEqual(t, len(shard.nextRefresh), 2)
		require.LessOrEqual(t, len(shard.bindings), 2)
		total += len(shard.nextRefresh)
		shard.mu.Unlock()
	}
	require.LessOrEqual(t, total, openAIStickyRefreshShardCount*2)
}

func TestGetStickySessionAccountID_UsesShortLocalBindingCache(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:cached-session": 42}}
	svc := &OpenAIGatewayService{
		cache:                   cache,
		cfg:                     &config.Config{},
		openaiStickyRefreshGate: newOpenAIStickyRefreshGate(time.Minute, 8),
	}

	for range 2 {
		accountID, err := svc.getStickySessionAccountID(context.Background(), nil, "cached-session")
		require.NoError(t, err)
		require.Equal(t, int64(42), accountID)
	}
	cache.getMu.Lock()
	require.Equal(t, 1, cache.getCalls["openai:cached-session"])
	cache.getMu.Unlock()

	require.NoError(t, svc.setStickySessionAccountID(context.Background(), nil, "cached-session", 9, time.Hour))
	accountID, err := svc.getStickySessionAccountID(context.Background(), nil, "cached-session")
	require.NoError(t, err)
	require.Equal(t, int64(9), accountID)
	cache.getMu.Lock()
	require.Equal(t, 1, cache.getCalls["openai:cached-session"])
	cache.getMu.Unlock()
}

func TestOpenAIStickyRefreshGate_CoalescesBindingLoads(t *testing.T) {
	gate := newOpenAIStickyRefreshGate(time.Minute, 8)
	key := openAIStickyRefreshKey{groupID: 1, sessionHash: "coalesced-load"}
	const workers = 100
	type loadResult struct {
		accountID int64
		err       error
	}
	var callsMu sync.Mutex
	loaderCalls := 0
	results := make(chan loadResult, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			accountID, err := gate.loadBinding(key, func() (int64, error) {
				callsMu.Lock()
				loaderCalls++
				callsMu.Unlock()
				time.Sleep(10 * time.Millisecond)
				return 77, nil
			})
			results <- loadResult{accountID: accountID, err: err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Equal(t, int64(77), result.accountID)
	}
	callsMu.Lock()
	require.Equal(t, 1, loaderCalls)
	callsMu.Unlock()
}

func TestOpenAIStickyRefreshGate_ClampsIntervalToConfiguredTTL(t *testing.T) {
	gate := newOpenAIStickyRefreshGate(time.Minute, 8)
	key := openAIStickyRefreshKey{groupID: 1, sessionHash: "short-ttl"}
	now := time.Now()

	require.True(t, gate.tryReserve(key, now, 9*time.Second))
	require.False(t, gate.tryReserve(key, now.Add(2*time.Second), 9*time.Second))
	require.True(t, gate.tryReserve(key, now.Add(4*time.Second), 9*time.Second))
}

func TestSnapshotOpenAICompatibilityFallbackMetrics(t *testing.T) {
	before := SnapshotOpenAICompatibilityFallbackMetrics()

	ctx := context.WithValue(context.Background(), ctxkey.ThinkingEnabled, true)
	_, _ = ThinkingEnabledFromContext(ctx)

	after := SnapshotOpenAICompatibilityFallbackMetrics()
	require.GreaterOrEqual(t, after.MetadataLegacyFallbackTotal, before.MetadataLegacyFallbackTotal+1)
	require.GreaterOrEqual(t, after.MetadataLegacyFallbackThinkingEnabledTotal, before.MetadataLegacyFallbackThinkingEnabledTotal+1)
}
