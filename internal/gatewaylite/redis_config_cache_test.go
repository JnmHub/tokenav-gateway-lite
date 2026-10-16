package gatewaylite

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type configClientFunc func(context.Context, GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error)

func (f configClientFunc) FetchGatewayConfigSnapshot(ctx context.Context, req GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error) {
	return f(ctx, req)
}

func TestRedisConfigCacheGetSet(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisConfigCache(client, "test")

	snapshot := GatewayConfigSnapshot{
		Version:           3,
		GeneratedAtMillis: time.Now().UnixMilli(),
		Accounts: []GatewayAccountSnapshot{{
			ID:          10,
			Name:        "openai-1",
			Platform:    "openai",
			Type:        "api_key",
			Status:      "active",
			Schedulable: true,
			Concurrency: 5,
			GroupIDs:    []int64{7},
		}},
		Groups: []GatewayGroupSnapshot{{
			ID:             7,
			Name:           "default",
			Platform:       "openai",
			Status:         "active",
			RateMultiplier: 1,
		}},
		GatewayNodes: []GatewayNodeSnapshot{{
			ID:           100,
			Code:         "sg-1",
			Name:         "Singapore",
			Region:       "sg",
			Format:       "openai",
			RequiredTier: 1,
		}},
		GatewayCategories: []GatewayCategorySnapshot{{
			ID:        200,
			Code:      "vip",
			Name:      "VIP",
			Status:    "active",
			SortOrder: 1,
		}},
		GatewayCategoryNodes: []GatewayCategoryNodeSnapshot{{
			CategoryID:    200,
			GatewayNodeID: 100,
			Priority:      10,
			Weight:        20,
		}},
		PlanGatewayRules: []PlanGatewayRuleSnapshot{{
			ID:             300,
			PlanID:         400,
			GatewayNodeID:  int64Ptr(100),
			GatewayGroupID: int64Ptr(7),
			Allowed:        boolPtr(true),
			Priority:       5,
		}},
		GatewayUnlockRules: []GatewayUnlockRuleSnapshot{{
			ID:                 500,
			Code:               "unlock-t1",
			Tier:               1,
			Name:               "T1",
			Status:             "active",
			RequiredPlanRank:   1,
			MinTotalTopupCents: 100,
		}},
	}
	require.NoError(t, cache.Set(ctx, snapshot))

	version, err := cache.Version(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, version)

	got, ok, err := cache.Get(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 10, got.Accounts[0].ID)
	require.EqualValues(t, 7, got.Groups[0].ID)
	require.EqualValues(t, 100, got.GatewayNodes[0].ID)
	require.EqualValues(t, 200, got.GatewayCategories[0].ID)
	require.EqualValues(t, 200, got.GatewayCategoryNodes[0].CategoryID)
	require.EqualValues(t, 300, got.PlanGatewayRules[0].ID)
	require.EqualValues(t, 7, *got.PlanGatewayRules[0].GatewayGroupID)
	require.EqualValues(t, 500, got.GatewayUnlockRules[0].ID)
}

func TestRedisConfigCacheInvalidationCursor(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisConfigCache(client, "cursor")

	got, err := cache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, got)

	require.NoError(t, cache.SetInvalidationCursor(ctx, 123))
	got, err = cache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 123, got)
}

func TestConfigSyncerWritesSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisConfigCache(client, "sync")

	calls := 0
	syncer := NewConfigSyncer(configClientFunc(func(_ context.Context, req GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error) {
		calls++
		require.Equal(t, "sg", req.Region)
		return &GatewayConfigSnapshotResponse{
			OK: true,
			Snapshot: GatewayConfigSnapshot{
				Version:           1,
				GeneratedAtMillis: time.Now().UnixMilli(),
				Accounts:          []GatewayAccountSnapshot{{ID: 1, Platform: "openai", Status: "active"}},
			},
		}, nil
	}), cache, "sg", time.Hour)

	syncer.syncOnce(ctx)
	require.Equal(t, 1, calls)
	got, ok, err := cache.Get(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 1, got.Version)
	require.Len(t, got.Accounts, 1)
}

func TestConfigSyncerSerializesConcurrentSyncs(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisConfigCache(client, "serialized-sync")

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	configClient := configClientFunc(func(ctx context.Context, _ GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error) {
		call := calls.Add(1)
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			maximum := maxInFlight.Load()
			if current <= maximum || maxInFlight.CompareAndSwap(maximum, current) {
				break
			}
		}

		if call == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &GatewayConfigSnapshotResponse{
			OK: true,
			Snapshot: GatewayConfigSnapshot{
				Version:  int64(call),
				Accounts: []GatewayAccountSnapshot{{ID: int64(call), Platform: "openai", Status: "active"}},
			},
		}, nil
	})
	syncer := NewConfigSyncer(configClient, cache, "sg", time.Hour)

	firstResult := make(chan error, 1)
	go func() { firstResult <- syncer.SyncFull(ctx) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first config sync did not start")
	}

	secondResult := make(chan error, 1)
	go func() { secondResult <- syncer.SyncFull(ctx) }()
	require.Never(t, func() bool { return calls.Load() > 1 }, 50*time.Millisecond, 5*time.Millisecond)
	require.EqualValues(t, 1, maxInFlight.Load())

	close(releaseFirst)
	require.NoError(t, <-firstResult)
	require.NoError(t, <-secondResult)
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 1, maxInFlight.Load())

	got, ok, err := cache.Get(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 2, got.Version)
	require.EqualValues(t, 2, got.Accounts[0].ID)
}

func TestConfigSyncerLockWaitRespectsContext(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := NewRedisConfigCache(client, "sync-wait-context")

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	configClient := configClientFunc(func(ctx context.Context, _ GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error) {
		call := calls.Add(1)
		if call == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &GatewayConfigSnapshotResponse{OK: true, Snapshot: GatewayConfigSnapshot{Version: int64(call)}}, nil
	})
	syncer := NewConfigSyncer(configClient, cache, "sg", time.Hour)

	firstResult := make(chan error, 1)
	go func() { firstResult <- syncer.SyncFull(context.Background()) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first config sync did not start")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := syncer.SyncFull(waitCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualValues(t, 1, calls.Load(), "canceled waiter must not call the control plane")

	close(releaseFirst)
	require.NoError(t, <-firstResult)
}

func int64Ptr(value int64) *int64 {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
