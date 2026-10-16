package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestLiveLeaseSharesRegularLimitsAndSupportsRemoteKeyNamespace(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	const (
		accountID = int64(101)
		userID    = int64(202)
		groupID   = int64(404)
		apiKeyID  = int64(-303) // 主站 Key 的稳定负数命名空间。
	)
	require.True(t, mustAcquireAccountSlot(t, cache, ctx, accountID, 1, "create-request"))
	acquired, err := cache.AcquireUserGroupAndAPIKeySlot(ctx, userID, 1, groupID, 1, apiKeyID, "create-request")
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = cache.AcquireLiveLease(ctx, accountID, 1, userID, 1, groupID, 1, apiKeyID, "live-lease", true)
	require.NoError(t, err)
	require.True(t, acquired, "创建阶段只允许把正在持有的普通槽位原子替换成 Live 租约")
	require.NoError(t, cache.ReleaseAccountSlot(ctx, accountID, "create-request"))
	require.NoError(t, cache.ReleaseUserGroupAndAPIKeySlot(ctx, userID, groupID, apiKeyID, "create-request"))

	accountCount, err := cache.GetAccountConcurrency(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, 1, accountCount)
	userCount, err := cache.GetUserConcurrency(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, 1, userCount)
	keyCounts, err := cache.GetAPIKeyConcurrencyBatch(ctx, []int64{apiKeyID})
	require.NoError(t, err)
	require.Equal(t, 1, keyCounts[apiKeyID])

	require.False(t, mustAcquireAccountSlot(t, cache, ctx, accountID, 1, "ordinary-while-live"))
	acquired, err = cache.AcquireUserAndAPIKeySlot(ctx, userID, 1, apiKeyID, "ordinary-while-live")
	require.NoError(t, err)
	require.False(t, acquired, "Live 必须占用同一个用户并发上限")
	acquired, err = cache.AcquireUserGroupAndAPIKeySlot(ctx, userID, 10, groupID, 1, apiKeyID-1, "group-ordinary-while-live")
	require.NoError(t, err)
	require.False(t, acquired, "Live 必须占用同一个分组每用户并发上限")

	refreshed, err := cache.RefreshLiveLease(ctx, accountID, userID, groupID, apiKeyID, "live-lease")
	require.NoError(t, err)
	require.True(t, refreshed)
	require.NoError(t, rdb.ZRem(ctx, liveGroupUserSlotKey(groupID, userID), "live-lease").Err())
	refreshed, err = cache.RefreshLiveLease(ctx, accountID, userID, groupID, apiKeyID, "live-lease")
	require.NoError(t, err)
	require.False(t, refreshed, "任一主体租约丢失后不得被续租脚本重新创建")

	require.NoError(t, cache.ReleaseLiveLease(ctx, accountID, userID, groupID, apiKeyID, "live-lease"))
	require.EqualValues(t, 0, rdb.ZCard(ctx, liveAccountSlotKey(accountID)).Val())
	require.EqualValues(t, 0, rdb.ZCard(ctx, liveUserSlotKey(userID)).Val())
	require.EqualValues(t, 0, rdb.ZCard(ctx, liveGroupUserSlotKey(groupID, userID)).Val())
	require.EqualValues(t, 0, rdb.ZCard(ctx, liveAPIKeySlotKey(apiKeyID)).Val())
}

func TestLiveLeaseRejectsSecondKeyForSameUserAndGroup(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	acquired, err := cache.AcquireLiveLease(ctx, 501, 10, 601, 10, 701, 1, -801, "first-live", false)
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = cache.AcquireLiveLease(ctx, 502, 10, 601, 10, 701, 1, -802, "second-live", false)
	require.NoError(t, err)
	require.False(t, acquired, "同一用户即使换 API Key 和上游账号，也不能绕过最终分组上限")

	require.NoError(t, cache.ReleaseLiveLease(ctx, 501, 601, 701, -801, "first-live"))
	acquired, err = cache.AcquireLiveLease(ctx, 502, 10, 601, 10, 701, 1, -802, "second-live", false)
	require.NoError(t, err)
	require.True(t, acquired, "释放第一条 Live 会话后应立即恢复容量")
}

func TestOrganizationLiveLeaseSharesTotalAndReleasesAllScopes(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	acquired, err := cache.AcquireOrganizationLiveLease(ctx, 1, 10, 50, 1, 101, 5, 7, 5, -1001, 1, "live-one", false)
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireOrganizationLiveLease(ctx, 2, 10, 50, 1, 202, 5, 7, 5, -2002, 1, "live-two", false)
	require.NoError(t, err)
	require.False(t, acquired)

	refreshed, err := cache.RefreshOrganizationLiveLease(ctx, 1, 50, 101, 7, -1001, "live-one")
	require.NoError(t, err)
	require.True(t, refreshed)
	require.NoError(t, cache.ReleaseOrganizationLiveLease(ctx, 1, 50, 101, 7, -1001, "live-one"))
	acquired, err = cache.AcquireOrganizationLiveLease(ctx, 2, 10, 50, 1, 202, 5, 7, 5, -2002, 1, "live-two", false)
	require.NoError(t, err)
	require.True(t, acquired)
}

func TestOrganizationLiveAndRegularRequestsShareAPIKeyLimit(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	acquired, err := cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 50, 10, 101, 10, 7, 10, -1001, 1, "regular")
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = cache.AcquireOrganizationLiveLease(ctx, 1, 10, 50, 10, 101, 10, 7, 10, -1001, 1, "live", false)
	require.NoError(t, err)
	require.False(t, acquired, "普通请求占用 Key 容量时，Live 不得另开独立额度")

	acquired, err = cache.AcquireOrganizationLiveLease(ctx, 1, 10, 50, 10, 101, 10, 7, 10, -1001, 1, "live", true)
	require.NoError(t, err)
	require.True(t, acquired, "创建请求持有的普通槽位应能原子转换为 Live 租约")
	require.NoError(t, cache.ReleaseOrganizationMemberGroupAndAPIKeySlot(ctx, 50, 101, 7, -1001, "regular"))

	acquired, err = cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 50, 10, 101, 10, 7, 10, -1001, 1, "ordinary-while-live")
	require.NoError(t, err)
	require.False(t, acquired, "Live 存活时普通请求不得绕过同一 Key 上限")
}

func TestLiveCallStoreClosesExactlyOnceAndPreservesBillingIdentity(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := &gatewayCache{rdb: rdb}
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)
	record := &service.LiveCallRecord{
		CallID: "call-1", CallHash: HashLiveCallID("call-1"), AccountID: 11,
		APIKeyID: -22, UserID: 33, OrganizationID: 77, GroupID: 44, LeaseID: "lease-1",
		Model: "gpt-live", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Controller: service.LiveControllerPending,
		GatewayMeta: gatewaylite.RequestMeta{
			RequestID: "create-request", KeyID: "remote-key", UserID: 33,
			Region: "us", GatewayID: "us-1", GroupID: 44,
			PolicyRevision: 55, PolicyChecksum: "checksum-55", PriceVersion: 54,
		},
		PolicyAudit: gatewaylite.EffectivePolicyAuditStamp{Revision: 55, Checksum: "checksum-55", PriceVersion: 54},
	}
	require.NoError(t, store.SaveLiveCall(ctx, record, time.Hour))
	loaded, err := store.GetLiveCall(ctx, record.CallHash)
	require.NoError(t, err)
	require.Equal(t, record.CallID, loaded.CallID)
	require.Equal(t, record.OrganizationID, loaded.OrganizationID)
	require.Equal(t, record.GatewayMeta.KeyID, loaded.GatewayMeta.KeyID)
	require.Equal(t, record.GatewayMeta.PolicyChecksum, loaded.GatewayMeta.PolicyChecksum)
	require.Equal(t, record.PolicyAudit, loaded.PolicyAudit)

	claimed, err := store.ClaimLiveController(ctx, record.CallHash, service.LiveControllerObserver, "observer-1")
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = store.ClaimLiveController(ctx, record.CallHash, service.LiveControllerProxy, "proxy-1")
	require.NoError(t, err)
	require.True(t, claimed)

	first, err := store.MarkLiveCallClosed(ctx, record.CallHash, time.Hour)
	require.NoError(t, err)
	require.True(t, first)
	second, err := store.MarkLiveCallClosed(ctx, record.CallHash, time.Hour)
	require.NoError(t, err)
	require.False(t, second, "重连、断线和过期竞争只能有一个终结器写 usage")
	claimed, err = store.ClaimLiveController(ctx, record.CallHash, service.LiveControllerProxy, "proxy-2")
	require.NoError(t, err)
	require.False(t, claimed, "closed 会话不能再次取得控制权")
}

func mustAcquireAccountSlot(t *testing.T, cache *concurrencyCache, ctx context.Context, accountID int64, max int, requestID string) bool {
	t.Helper()
	acquired, err := cache.AcquireAccountSlot(ctx, accountID, max, requestID)
	require.NoError(t, err)
	return acquired
}
