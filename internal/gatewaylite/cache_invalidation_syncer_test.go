package gatewaylite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type cacheInvalidationClientFunc func(context.Context, CacheInvalidationRequest) (*CacheInvalidationResponse, error)

func (f cacheInvalidationClientFunc) FetchCacheInvalidations(ctx context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
	return f(ctx, req)
}

type configRefresherFunc func(context.Context)

func (f configRefresherFunc) SyncFull(ctx context.Context) error {
	f(ctx)
	return nil
}

type failingConfigRefresher struct{}

func (f failingConfigRefresher) SyncFull(context.Context) error {
	return errors.New("refresh failed")
}

func TestCacheInvalidationSyncerRefreshesConfigAndDeletesKeys(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "inv")
	keyCache := NewRedisKeyCache(client, "inv")
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "key-sg", CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "key-us", CacheTTLSecond: 60}, "us"))

	refreshes := 0
	requests := make([]CacheInvalidationRequest, 0)
	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		requests = append(requests, req)
		if len(req.AckIDs) > 0 {
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		require.Equal(t, "sg-1", req.GatewayCode)
		require.Equal(t, "sg-1", req.Region)
		require.EqualValues(t, 0, req.SinceID)
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 2,
			Events: []CacheInvalidationEvent{
				{ID: 1, Scope: "config:snapshot", GatewayCode: "sg-1", Region: "sg"},
				{ID: 2, Scope: "key:snapshot", GatewayCode: "sg-1", Region: "sg"},
			},
		}, nil
	}), configCache, keyCache, configRefresherFunc(func(context.Context) {
		refreshes++
	}), "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	require.Equal(t, 1, refreshes)
	cursor, err := configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, cursor)
	_, ok, err := keyCache.Get(ctx, "key-sg", "sg-1")
	require.NoError(t, err)
	require.False(t, ok)
	got, ok, err := keyCache.Get(ctx, "key-us", "us")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "key-us", got.KeyID)
	require.Len(t, requests, 2)
	require.Equal(t, []int64{1, 2}, requests[1].AckIDs)
}

func TestCacheInvalidationSyncerCoalescesConfigRefreshesWithinPollingBatch(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "coalesced-config")

	refreshes := 0
	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 3,
			Events: []CacheInvalidationEvent{
				{ID: 1, Scope: "config:snapshot", GatewayCode: "sg-1"},
				{ID: 2, Scope: "config:snapshot", GatewayCode: "sg-1"},
				{ID: 3, Scope: "config:snapshot", GatewayCode: "sg-1"},
			},
		}, nil
	}), configCache, nil, configRefresherFunc(func(context.Context) {
		refreshes++
	}), "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	require.Equal(t, 1, refreshes)
	for eventID := int64(1); eventID <= 3; eventID++ {
		processed, err := configCache.ControlEventProcessed(ctx, eventID)
		require.NoError(t, err)
		require.True(t, processed)
	}
	cursor, err := configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, cursor)
}

func TestCacheInvalidationSyncerDeletesSingleKeyWhenPayloadHasKeyID(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "single-key")
	keyCache := NewRedisKeyCache(client, "single-key")
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "changed-key", CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "other-key", CacheTTLSecond: 60}, "sg-1"))

	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 3,
			Events: []CacheInvalidationEvent{
				{ID: 3, Scope: "key:snapshot", GatewayCode: "sg-1", Region: "sg", Payload: map[string]any{"key_id": "changed-key"}},
			},
		}, nil
	}), configCache, keyCache, nil, "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	_, ok, err := keyCache.Get(ctx, "changed-key", "sg-1")
	require.NoError(t, err)
	require.False(t, ok)
	got, ok, err := keyCache.Get(ctx, "other-key", "sg-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "other-key", got.KeyID)
}

func TestCacheInvalidationSyncerDeletesUserKeysWhenPayloadHasUserID(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "user-keys")
	keyCache := NewRedisKeyCache(client, "user-keys")
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "user-key-1", UserID: 42, CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "user-key-2", UserID: 42, CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "other-user-key", UserID: 99, CacheTTLSecond: 60}, "sg-1"))

	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 4,
			Events: []CacheInvalidationEvent{
				{ID: 4, Scope: "key:snapshot", GatewayCode: "sg-1", Region: "sg", Payload: map[string]any{"user_id": float64(42)}},
			},
		}, nil
	}), configCache, keyCache, nil, "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	_, ok, err := keyCache.Get(ctx, "user-key-1", "sg-1")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = keyCache.Get(ctx, "user-key-2", "sg-1")
	require.NoError(t, err)
	require.False(t, ok)
	got, ok, err := keyCache.Get(ctx, "other-user-key", "sg-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "other-user-key", got.KeyID)
}

func TestCacheInvalidationSyncerDeletesOnlyOrganizationKeys(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "organization-keys")
	keyCache := NewRedisKeyCache(client, "organization-keys")
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "org-key-1", UserID: 42, OrganizationID: 7, CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "org-key-2", UserID: 43, OrganizationID: 7, CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "other-org-key", UserID: 42, OrganizationID: 8, CacheTTLSecond: 60}, "sg-1"))
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "personal-key", UserID: 42, CacheTTLSecond: 60}, "sg-1"))

	indexKey := keyCache.organizationIndexKey(7, "sg-1")
	require.Greater(t, mr.TTL(indexKey), time.Duration(0), "企业 Key 索引必须有界过期")
	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		return &CacheInvalidationResponse{
			OK: true, LatestID: 5,
			Events: []CacheInvalidationEvent{{
				ID: 5, Scope: "key:snapshot", GatewayCode: "sg-1", Region: "sg",
				Payload: map[string]any{"organization_id": float64(7), "user_id": float64(42)},
			}},
		}, nil
	}), configCache, keyCache, nil, "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	for _, keyID := range []string{"org-key-1", "org-key-2"} {
		_, ok, err := keyCache.Get(ctx, keyID, "sg-1")
		require.NoError(t, err)
		require.False(t, ok)
	}
	for _, keyID := range []string{"other-org-key", "personal-key"} {
		got, ok, err := keyCache.Get(ctx, keyID, "sg-1")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, keyID, got.KeyID)
	}
	require.False(t, mr.Exists(indexKey), "企业失效后应主动清理索引")
}

type regionOnlyKeyInvalidator struct {
	deleteRegionCalls int
}

func (i *regionOnlyKeyInvalidator) DeleteRegion(context.Context, string) error {
	i.deleteRegionCalls++
	return nil
}

func TestCacheInvalidationSyncerRejectsOrganizationFallbackToRegion(t *testing.T) {
	invalidator := &regionOnlyKeyInvalidator{}
	syncer := NewCacheInvalidationSyncer(nil, nil, invalidator, nil, "sg", "sg-1", time.Hour)

	err := syncer.handleEvent(context.Background(), CacheInvalidationEvent{
		Scope:   "key:snapshot",
		Payload: map[string]any{"organization_id": float64(7), "user_id": float64(42)},
	}, "sg-1")

	require.ErrorIs(t, err, ErrOrganizationScopedKeyInvalidationUnsupported)
	require.Zero(t, invalidator.deleteRegionCalls)
}

func TestCacheInvalidationSyncerSkipsOtherGatewayEvents(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "skip")

	refreshes := 0
	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 9,
			Events: []CacheInvalidationEvent{
				{ID: 9, Scope: "config:snapshot", GatewayCode: "jp-1", Region: "jp"},
			},
		}, nil
	}), configCache, nil, configRefresherFunc(func(context.Context) {
		refreshes++
	}), "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	require.Equal(t, 0, refreshes)
	cursor, err := configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 9, cursor)
}

func TestCacheInvalidationSyncerDoesNotAdvanceCursorOnRefreshFailure(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "fail")

	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			t.Fatalf("unexpected ack after failed refresh")
		}
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 7,
			Events: []CacheInvalidationEvent{
				{ID: 7, Scope: "config:snapshot", GatewayCode: "sg-1", Region: "sg"},
			},
		}, nil
	}), configCache, nil, failingConfigRefresher{}, "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)

	cursor, err := configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 0, cursor)
}

func TestCacheInvalidationSyncerRetriesFailedAckWithoutRepeatingSideEffects(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "ack-retry")

	refreshes := 0
	ackAttempts := 0
	fetchSinceIDs := make([]int64, 0, 2)
	syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
		if len(req.AckIDs) > 0 {
			ackAttempts++
			require.Equal(t, []int64{11}, req.AckIDs)
			if ackAttempts == 1 {
				return nil, errors.New("temporary ack network failure")
			}
			return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
		}
		fetchSinceIDs = append(fetchSinceIDs, req.SinceID)
		return &CacheInvalidationResponse{
			OK:       true,
			LatestID: 11,
			Events: []CacheInvalidationEvent{
				{ID: 11, Scope: "config:snapshot", GatewayCode: "sg-1"},
			},
		}, nil
	}), configCache, nil, configRefresherFunc(func(context.Context) {
		refreshes++
	}), "sg", "sg-1", time.Hour)

	syncer.SyncOnce(ctx)
	require.Equal(t, 1, refreshes)
	require.Equal(t, 1, ackAttempts)
	processed, err := configCache.ControlEventProcessed(ctx, 11)
	require.NoError(t, err)
	require.True(t, processed)
	cursor, err := configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.Zero(t, cursor, "ACK 失败前不能推进 polling cursor（轮询游标）")

	// 第二轮仍从旧游标拉同一事件；Redis processed marker（处理标记）
	// 阻止重复刷新，ACK 成功后才一次性推进游标。
	syncer.SyncOnce(ctx)
	require.Equal(t, 1, refreshes)
	require.Equal(t, 2, ackAttempts)
	require.Equal(t, []int64{0, 0}, fetchSinceIDs)
	cursor, err = configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 11, cursor)
}

func TestCacheInvalidationSyncerKeepsCursorWhenAckResponseIsRejected(t *testing.T) {
	tests := []struct {
		name     string
		response *CacheInvalidationResponse
	}{
		{name: "empty response", response: nil},
		{name: "ok false", response: &CacheInvalidationResponse{OK: false, Error: "ack rejected"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer client.Close()
			configCache := NewRedisConfigCache(client, "ack-response-"+tt.name)

			refreshes := 0
			ackAttempts := 0
			syncer := NewCacheInvalidationSyncer(cacheInvalidationClientFunc(func(_ context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
				if len(req.AckIDs) > 0 {
					ackAttempts++
					if ackAttempts == 1 {
						return tt.response, nil
					}
					return &CacheInvalidationResponse{OK: true, LatestID: req.SinceID}, nil
				}
				return &CacheInvalidationResponse{
					OK:       true,
					LatestID: 12,
					Events: []CacheInvalidationEvent{
						{ID: 12, Scope: "config:snapshot", GatewayCode: "sg-1"},
					},
				}, nil
			}), configCache, nil, configRefresherFunc(func(context.Context) {
				refreshes++
			}), "sg", "sg-1", time.Hour)

			syncer.SyncOnce(ctx)
			cursor, err := configCache.InvalidationCursor(ctx)
			require.NoError(t, err)
			require.Zero(t, cursor)
			require.Equal(t, 1, refreshes)

			syncer.SyncOnce(ctx)
			cursor, err = configCache.InvalidationCursor(ctx)
			require.NoError(t, err)
			require.EqualValues(t, 12, cursor)
			require.Equal(t, 1, refreshes)
			require.Equal(t, 2, ackAttempts)
		})
	}
}

func TestCacheInvalidationSyncerPushIsIdempotent(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "push-idempotent")
	keyCache := NewRedisKeyCache(client, "push-idempotent")
	require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: "changed-key", CacheTTLSecond: 60}, "sg-1"))

	syncer := NewCacheInvalidationSyncer(nil, configCache, keyCache, nil, "sg", "sg-1", time.Hour)
	event := CacheInvalidationEvent{
		ID:          101,
		Scope:       "key:snapshot",
		GatewayCode: "sg-1",
		Payload:     map[string]any{"key_id": "changed-key"},
	}
	alreadyProcessed, err := syncer.ApplyPushedEvent(ctx, event)
	require.NoError(t, err)
	require.False(t, alreadyProcessed)
	_, ok, err := keyCache.Get(ctx, "changed-key", "sg-1")
	require.NoError(t, err)
	require.False(t, ok)

	// 模拟节点重启：新 syncer 仍通过 Redis 中的处理标记识别重复投递。
	restartedSyncer := NewCacheInvalidationSyncer(nil, configCache, keyCache, nil, "sg", "sg-1", time.Hour)
	alreadyProcessed, err = restartedSyncer.ApplyPushedEvent(ctx, event)
	require.NoError(t, err)
	require.True(t, alreadyProcessed)
}

func TestCacheInvalidationSyncerPushHandlesOutOfOrderEvents(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "push-out-of-order")
	keyCache := NewRedisKeyCache(client, "push-out-of-order")
	for _, keyID := range []string{"older-key", "newer-key"} {
		require.NoError(t, keyCache.Set(ctx, KeySnapshot{KeyID: keyID, CacheTTLSecond: 60}, "sg-1"))
	}
	syncer := NewCacheInvalidationSyncer(nil, configCache, keyCache, nil, "sg", "sg-1", time.Hour)

	for _, event := range []CacheInvalidationEvent{
		{ID: 202, Scope: "key:snapshot", GatewayCode: "sg-1", Payload: map[string]any{"key_id": "newer-key"}},
		{ID: 201, Scope: "key:snapshot", GatewayCode: "sg-1", Payload: map[string]any{"key_id": "older-key"}},
	} {
		already, err := syncer.ApplyPushedEvent(ctx, event)
		require.NoError(t, err)
		require.False(t, already)
	}
	for _, keyID := range []string{"older-key", "newer-key"} {
		_, ok, err := keyCache.Get(ctx, keyID, "sg-1")
		require.NoError(t, err)
		require.False(t, ok)
	}
	cursor, err := configCache.InvalidationCursor(ctx)
	require.NoError(t, err)
	require.Zero(t, cursor, "push 乱序事件不能推进或回退 polling cursor（轮询游标）")
}

func TestCacheInvalidationSyncerPushInvalidatesMatchingLease(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	configCache := NewRedisConfigCache(client, "push-lease")
	quota := NewRedisQuota(client, "push-lease")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-1", UserID: 42, Region: "sg-1", AllocatedCents: 100,
		Version: 4, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))

	syncer := NewCacheInvalidationSyncer(nil, configCache, nil, nil, "sg", "sg-1", time.Hour).
		WithLeaseInvalidator(quota)
	alreadyProcessed, err := syncer.ApplyPushedEvent(ctx, CacheInvalidationEvent{
		ID:          102,
		Scope:       "lease:snapshot",
		GatewayCode: "sg-1",
		Payload: map[string]any{
			"user_id":  float64(42),
			"lease_id": "lease-1",
			"version":  float64(4),
		},
	})
	require.NoError(t, err)
	require.False(t, alreadyProcessed)
	_, ok, err := quota.LoadLease(ctx, 42, "sg-1")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestCacheInvalidationSyncerPushRejectsAnotherGateway(t *testing.T) {
	syncer := NewCacheInvalidationSyncer(nil, nil, nil, nil, "sg", "sg-1", time.Hour)
	_, err := syncer.ApplyPushedEvent(context.Background(), CacheInvalidationEvent{
		ID:          103,
		Scope:       "key:snapshot",
		GatewayCode: "jp-1",
	})
	require.ErrorIs(t, err, ErrControlEventGatewayMismatch)
}

func TestCacheInvalidationSyncerRequestsGatewayAuthoredPolicySnapshot(t *testing.T) {
	requested := make(chan GatewayConfigChangedRequest, 1)
	notifier := NewConfigChangeNotifier(configChangedClientFunc(func(_ context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
		requested <- req
		return &GatewayConfigChangedResponse{OK: true}, nil
	}), NewRuntimeConfigRef(RuntimeConfig{GatewayCode: "sg-1", Region: "sg-1"}), time.Millisecond, time.Second)
	SetDefaultConfigChangeNotifier(notifier)

	syncer := NewCacheInvalidationSyncer(nil, nil, nil, nil, "sg-1", "sg-1", time.Hour)
	already, err := syncer.ApplyPushedEvent(context.Background(), CacheInvalidationEvent{
		ID: 104, Scope: "effective-policy:snapshot", GatewayCode: "sg-1",
	})
	require.NoError(t, err)
	require.False(t, already)
	select {
	case req := <-requested:
		require.Equal(t, "mixed", req.Resource)
		require.Equal(t, "consistency_repair", req.Action)
	case <-time.After(time.Second):
		t.Fatal("policy reconciliation did not request a fresh gateway snapshot")
	}
}
