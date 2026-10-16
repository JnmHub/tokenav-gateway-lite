package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyCacheReleaseAccountSlotsGroupsByAccount(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	require.NoError(t, rdb.ZAdd(ctx, accountSlotKey(11),
		redis.Z{Score: 1, Member: "r1"},
		redis.Z{Score: 1, Member: "r2"},
		redis.Z{Score: 1, Member: "keep"},
	).Err())
	require.NoError(t, rdb.ZAdd(ctx, accountSlotKey(22), redis.Z{Score: 1, Member: "r3"}).Err())

	err := cache.ReleaseAccountSlots(ctx, []service.AccountSlotRelease{
		{AccountID: 11, RequestID: "r1"},
		{AccountID: 11, RequestID: "r2"},
		{AccountID: 22, RequestID: "r3"},
		{AccountID: 0, RequestID: "ignored"},
	})
	require.NoError(t, err)
	account11, err := mini.ZMembers(accountSlotKey(11))
	require.NoError(t, err)
	require.Equal(t, []string{"keep"}, account11)
	require.False(t, mini.Exists(accountSlotKey(22)))
}

func TestConcurrencyCacheReleaseUserAndAPIKeySlotsGroupsByOwner(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	records := map[string][]redis.Z{
		userSlotKey(101):    {{Score: 1, Member: "r1"}, {Score: 1, Member: "r2"}, {Score: 1, Member: "keep-user"}},
		userSlotKey(202):    {{Score: 1, Member: "r3"}},
		apiKeySlotKey(1001): {{Score: 1, Member: "r1"}, {Score: 1, Member: "r2"}, {Score: 1, Member: "keep-key"}},
		apiKeySlotKey(2002): {{Score: 1, Member: "r3"}},
	}
	for key, members := range records {
		require.NoError(t, rdb.ZAdd(ctx, key, members...).Err())
	}

	err := cache.ReleaseUserAndAPIKeySlots(ctx, []service.UserAPIKeySlotRelease{
		{UserID: 101, APIKeyID: 1001, RequestID: "r1"},
		{UserID: 101, APIKeyID: 1001, RequestID: "r2"},
		{UserID: 202, APIKeyID: 2002, RequestID: "r3"},
		{UserID: 0, APIKeyID: 0, RequestID: "ignored"},
	})
	require.NoError(t, err)

	user101, err := mini.ZMembers(userSlotKey(101))
	require.NoError(t, err)
	require.Equal(t, []string{"keep-user"}, user101)
	key1001, err := mini.ZMembers(apiKeySlotKey(1001))
	require.NoError(t, err)
	require.Equal(t, []string{"keep-key"}, key1001)
	require.False(t, mini.Exists(userSlotKey(202)))
	require.False(t, mini.Exists(apiKeySlotKey(2002)))

	// ZREM 天然幂等，重复批量释放不能报错或误删保留成员。
	require.NoError(t, cache.ReleaseUserAndAPIKeySlots(ctx, []service.UserAPIKeySlotRelease{
		{UserID: 101, APIKeyID: 1001, RequestID: "r1"},
	}))
	user101, err = mini.ZMembers(userSlotKey(101))
	require.NoError(t, err)
	require.Equal(t, []string{"keep-user"}, user101)
}

func TestConcurrencyCacheOrganizationMemberGateIsAtomicAndShared(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	acquired, err := cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 1, 101, 5, 7, 5, -1001, 1, "first")
	require.NoError(t, err)
	require.True(t, acquired)

	// 另一个员工仍共享同一个企业总桶；企业已满时不能写入他的成员/分组/Key 槽位。
	acquired, err = cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 1, 202, 5, 7, 5, -2002, 1, "rejected")
	require.NoError(t, err)
	require.False(t, acquired)
	require.False(t, mini.Exists(organizationMemberSlotKey(10, 202)))
	require.False(t, mini.Exists(organizationGroupMemberSlotKey(10, 7, 202)))
	require.False(t, mini.Exists(apiKeySlotKey(-2002)))

	// 同一个 userID 在另一个企业使用独立桶，不会被企业 10 的槽位污染。
	acquired, err = cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 20, 1, 101, 5, 7, 5, -3003, 1, "other-org")
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, cache.ReleaseOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 101, 7, -1001, "first"))
	acquired, err = cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 1, 202, 5, 7, 5, -2002, 1, "after-release")
	require.NoError(t, err)
	require.True(t, acquired)
}

func TestConcurrencyCacheOrganizationAPIKeyLimitsAreIndependent(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := &concurrencyCache{rdb: rdb, slotTTLSeconds: 60, waitQueueTTLSeconds: 60}
	ctx := context.Background()

	acquired, err := cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 10, 101, 10, 7, 10, -1001, 1, "key-a-first")
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 10, 101, 10, 7, 10, -1001, 1, "key-a-second")
	require.NoError(t, err)
	require.False(t, acquired, "同一把企业 Key 达到上限后必须拒绝")

	acquired, err = cache.AcquireOrganizationMemberGroupAndAPIKeySlot(ctx, 10, 10, 101, 10, 7, 10, -1002, 1, "key-b-first")
	require.NoError(t, err)
	require.True(t, acquired, "同一员工的另一把 Key 必须使用独立容量")
	members, err := mini.ZMembers(apiKeySlotKey(-1001))
	require.NoError(t, err)
	require.Equal(t, []string{"key-a-first"}, members, "被拒请求不得污染 Key A 槽位")
}
