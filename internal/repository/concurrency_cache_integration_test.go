//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// 测试用 TTL 配置（15 分钟，与默认值一致）
const testSlotTTLMinutes = 15

// 测试用 TTL Duration，用于 TTL 断言
var testSlotTTL = time.Duration(testSlotTTLMinutes) * time.Minute

type ConcurrencyCacheSuite struct {
	IntegrationRedisSuite
	cache    service.ConcurrencyCache
	rawCache *concurrencyCache
}

func TestConcurrencyCacheSuite(t *testing.T) {
	suite.Run(t, new(ConcurrencyCacheSuite))
}

func (s *ConcurrencyCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.rawCache = NewConcurrencyCache(s.rdb, testSlotTTLMinutes, int(testSlotTTL.Seconds())).(*concurrencyCache)
	s.cache = s.rawCache
}

func (s *ConcurrencyCacheSuite) TestOpenAIWSIngressAPIKeySlot_HardLimitRefreshAndRelease() {
	apiKeyID := int64(9011)
	firstLeaseID := "ingress-first"
	secondLeaseID := "ingress-second"

	ok, err := s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, firstLeaseID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	ok, err = s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, secondLeaseID)
	require.NoError(s.T(), err)
	require.False(s.T(), ok, "a second live session must not exceed the API key limit")

	ok, err = s.rawCache.RefreshOpenAIWSIngressLease(s.ctx, apiKeyID, firstLeaseID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "the current owner must be able to refresh its lease")

	require.NoError(s.T(), s.rawCache.ReleaseOpenAIWSIngressLease(s.ctx, apiKeyID, firstLeaseID))
	ok, err = s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, secondLeaseID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "released capacity must become available immediately")
}

func (s *ConcurrencyCacheSuite) TestOpenAIWSIngressControlPlaneKeyNamespace_HardLimitAndRelease() {
	// 主站快照 Key 使用负数命名空间，与本地数据库正数 ID 隔离；Redis
	// Lua 和 key 格式必须完整支持该值，不能把它误判成“缺少 Key”。
	apiKeyID := int64(-9013)
	ok, err := s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, "remote-first")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	ok, err = s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, "remote-second")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)

	require.NoError(s.T(), s.rawCache.ReleaseOpenAIWSIngressLease(s.ctx, apiKeyID, "remote-first"))
	ok, err = s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 1, "remote-second")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
}

func (s *ConcurrencyCacheSuite) TestOpenAIWSIngressAPIKeySlot_ReapsCrashedLeaseWithoutDeletingLiveOtherInstance() {
	apiKeyID := int64(9012)
	key := openAIWSIngressLeaseKey(apiKeyID)
	now, err := s.rdb.Time(s.ctx).Result()
	require.NoError(s.T(), err)
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, key,
		redis.Z{Score: float64(now.Unix() - openAIWSIngressLeaseTTLSeconds - 1), Member: "crashed-instance"},
		redis.Z{Score: float64(now.Unix()), Member: "live-other-instance"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, key, time.Duration(openAIWSIngressLeaseTTLSeconds)*time.Second).Err())

	ok, err := s.rawCache.AcquireOpenAIWSIngressLease(s.ctx, apiKeyID, 2, "new-instance")
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "the crashed member should be reaped before enforcing the limit")

	_, err = s.rdb.ZScore(s.ctx, key, "crashed-instance").Result()
	require.ErrorIs(s.T(), err, redis.Nil)
	_, err = s.rdb.ZScore(s.ctx, key, "live-other-instance").Result()
	require.NoError(s.T(), err, "a live lease owned by another instance must be preserved")
	count, err := s.rdb.ZCard(s.ctx, key).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(2), count)
}

func (s *ConcurrencyCacheSuite) TestAccountSlot_AcquireAndRelease() {
	accountID := int64(10)
	reqID1, reqID2, reqID3 := "req1", "req2", "req3"

	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 2, reqID1)
	require.NoError(s.T(), err, "AcquireAccountSlot 1")
	require.True(s.T(), ok)

	ok, err = s.cache.AcquireAccountSlot(s.ctx, accountID, 2, reqID2)
	require.NoError(s.T(), err, "AcquireAccountSlot 2")
	require.True(s.T(), ok)

	ok, err = s.cache.AcquireAccountSlot(s.ctx, accountID, 2, reqID3)
	require.NoError(s.T(), err, "AcquireAccountSlot 3")
	require.False(s.T(), ok, "expected third acquire to fail")

	cur, err := s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err, "GetAccountConcurrency")
	require.Equal(s.T(), 2, cur, "concurrency mismatch")

	require.NoError(s.T(), s.cache.ReleaseAccountSlot(s.ctx, accountID, reqID1), "ReleaseAccountSlot")

	cur, err = s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err, "GetAccountConcurrency after release")
	require.Equal(s.T(), 1, cur, "expected 1 after release")
}

func (s *ConcurrencyCacheSuite) TestAccountSlot_TTL() {
	accountID := int64(11)
	reqID := "req_ttl_test"
	slotKey := fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)

	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 5, reqID)
	require.NoError(s.T(), err, "AcquireAccountSlot")
	require.True(s.T(), ok)

	ttl, err := s.rdb.TTL(s.ctx, slotKey).Result()
	require.NoError(s.T(), err, "TTL")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)
}

func (s *ConcurrencyCacheSuite) TestAccountSlot_DuplicateReqID() {
	accountID := int64(12)
	reqID := "dup-req"

	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 2, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Acquiring with same reqID should be idempotent
	ok, err = s.cache.AcquireAccountSlot(s.ctx, accountID, 2, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	cur, err := s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, cur, "expected concurrency=1 (idempotent)")
}

func (s *ConcurrencyCacheSuite) TestAccountSlot_ReleaseIdempotent() {
	accountID := int64(13)
	reqID := "release-test"

	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 1, reqID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	require.NoError(s.T(), s.cache.ReleaseAccountSlot(s.ctx, accountID, reqID), "ReleaseAccountSlot")
	// Releasing again should not error
	require.NoError(s.T(), s.cache.ReleaseAccountSlot(s.ctx, accountID, reqID), "ReleaseAccountSlot again")
	// Releasing non-existent should not error
	require.NoError(s.T(), s.cache.ReleaseAccountSlot(s.ctx, accountID, "non-existent"), "ReleaseAccountSlot non-existent")

	cur, err := s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cur)
}

func (s *ConcurrencyCacheSuite) TestAccountSlot_MaxZero() {
	accountID := int64(14)
	reqID := "max-zero-test"

	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 0, reqID)
	require.NoError(s.T(), err)
	require.False(s.T(), ok, "expected acquire to fail with max=0")
}

func (s *ConcurrencyCacheSuite) TestUserSlot_AcquireAndRelease() {
	userID := int64(42)
	reqID1, reqID2 := "req1", "req2"

	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 1, reqID1)
	require.NoError(s.T(), err, "AcquireUserSlot")
	require.True(s.T(), ok)

	ok, err = s.cache.AcquireUserSlot(s.ctx, userID, 1, reqID2)
	require.NoError(s.T(), err, "AcquireUserSlot 2")
	require.False(s.T(), ok, "expected second acquire to fail at max=1")

	cur, err := s.cache.GetUserConcurrency(s.ctx, userID)
	require.NoError(s.T(), err, "GetUserConcurrency")
	require.Equal(s.T(), 1, cur, "expected concurrency=1")

	require.NoError(s.T(), s.cache.ReleaseUserSlot(s.ctx, userID, reqID1), "ReleaseUserSlot")
	// Releasing a non-existent slot should not error
	require.NoError(s.T(), s.cache.ReleaseUserSlot(s.ctx, userID, "non-existent"), "ReleaseUserSlot non-existent")

	cur, err = s.cache.GetUserConcurrency(s.ctx, userID)
	require.NoError(s.T(), err, "GetUserConcurrency after release")
	require.Equal(s.T(), 0, cur, "expected concurrency=0 after release")
}

func (s *ConcurrencyCacheSuite) TestUserAndAPIKeySlot_AtomicLimitIdempotencyAndRelease() {
	userID := int64(43)
	apiKeyID := int64(77)
	requestID := "combined-req-1"

	ok, err := s.rawCache.AcquireUserAndAPIKeySlot(s.ctx, userID, 1, apiKeyID, requestID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	userCount, err := s.rdb.ZCard(s.ctx, userSlotKey(userID)).Result()
	require.NoError(s.T(), err)
	keyCount, err := s.rdb.ZCard(s.ctx, apiKeySlotKey(apiKeyID)).Result()
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(1), userCount)
	require.Equal(s.T(), int64(1), keyCount)

	// 同一个 request_id 重试必须幂等，不能因为自身已经占槽而被并发上限拒绝，
	// 也不能在用户或 API Key 集合中产生第二个成员。
	ok, err = s.rawCache.AcquireUserAndAPIKeySlot(s.ctx, userID, 1, apiKeyID, requestID)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(apiKeyID)))

	// 新请求超过用户上限时必须严格拒绝，同时不能污染仅用于展示的 Key 活跃统计。
	ok, err = s.rawCache.AcquireUserAndAPIKeySlot(s.ctx, userID, 1, apiKeyID, "combined-req-2")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(apiKeyID)))

	require.NoError(s.T(), s.rawCache.ReleaseUserAndAPIKeySlot(s.ctx, userID, apiKeyID, requestID))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(apiKeyID)))
	// pipeline（流水线）释放是幂等的，重复执行不应报错。
	require.NoError(s.T(), s.rawCache.ReleaseUserAndAPIKeySlot(s.ctx, userID, apiKeyID, requestID))
}

func (s *ConcurrencyCacheSuite) TestUserGroupAndAPIKeySlot_SharedAcrossKeysAndReleasedImmediately() {
	userID := int64(4401)
	groupID := int64(5501)
	firstKeyID := int64(6601)
	secondKeyID := int64(6602)

	ok, err := s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 10, groupID, 1, firstKeyID, "group-first")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// 分组上限按主站用户聚合，同一用户不能通过换一个 API Key 绕过。
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 10, groupID, 1, secondKeyID, "group-second")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, groupUserSlotKey(groupID, userID)))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(secondKeyID)))

	require.NoError(s.T(), s.rawCache.ReleaseUserGroupAndAPIKeySlot(s.ctx, userID, groupID, firstKeyID, "group-first"))
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 10, groupID, 1, secondKeyID, "group-second")
	require.NoError(s.T(), err)
	require.True(s.T(), ok, "released group capacity must be available immediately")
}

func (s *ConcurrencyCacheSuite) TestUserGroupAndAPIKeySlot_IsolatedByUserAndGroup() {
	firstUserID := int64(4402)
	secondUserID := int64(4403)
	firstGroupID := int64(5502)
	secondGroupID := int64(5503)

	ok, err := s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, firstUserID, 10, firstGroupID, 1, 6603, "isolation-first")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// 同一用户访问另一个分组，以及另一用户访问同一分组，都有各自独立的容量。
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, firstUserID, 10, secondGroupID, 1, 6604, "isolation-other-group")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, secondUserID, 10, firstGroupID, 1, 6605, "isolation-other-user")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
}

func (s *ConcurrencyCacheSuite) TestUserGroupAndAPIKeySlot_AtomicWhenEitherLimitIsReached() {
	userID := int64(4404)
	groupID := int64(5504)

	// 先占满等级并发，再尝试进入分组；失败后不能残留分组或 Key 槽位。
	ok, err := s.rawCache.AcquireUserSlot(s.ctx, userID, 1, "user-limit-holder")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 1, groupID, 5, 6606, "blocked-by-user")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, groupUserSlotKey(groupID, userID)))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(6606)))
	require.NoError(s.T(), s.rawCache.ReleaseUserSlot(s.ctx, userID, "user-limit-holder"))

	// 再占满分组并发；第二次失败不能污染用户总槽位和另一个 Key 的统计。
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 10, groupID, 1, 6607, "group-limit-holder")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 10, groupID, 1, 6608, "blocked-by-group")
	require.NoError(s.T(), err)
	require.False(s.T(), ok)
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(1), mustZCard(s.T(), s.rdb, s.ctx, groupUserSlotKey(groupID, userID)))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(6608)))
}

func (s *ConcurrencyCacheSuite) TestUserGroupAndAPIKeySlot_BatchReleaseClearsEveryDimension() {
	userID := int64(4405)
	groupID := int64(5505)
	apiKeyID := int64(6609)
	releases := []service.UserAPIKeySlotRelease{
		{UserID: userID, GroupID: groupID, APIKeyID: apiKeyID, RequestID: "batch-group-1"},
		{UserID: userID, GroupID: groupID, APIKeyID: apiKeyID, RequestID: "batch-group-2"},
	}
	for _, release := range releases {
		ok, err := s.rawCache.AcquireUserGroupAndAPIKeySlot(s.ctx, userID, 10, groupID, 2, apiKeyID, release.RequestID)
		require.NoError(s.T(), err)
		require.True(s.T(), ok)
	}

	require.NoError(s.T(), s.rawCache.ReleaseUserAndAPIKeySlots(s.ctx, releases))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, groupUserSlotKey(groupID, userID)))
	require.Equal(s.T(), int64(0), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(apiKeyID)))
}

func (s *ConcurrencyCacheSuite) TestUserAndAPIKeySlot_AsyncBatchReleaseEventuallyClearsAllSlots() {
	const requestCount = 200
	userID := int64(4301)
	apiKeyID := int64(7701)
	svc := service.NewConcurrencyService(s.rawCache)
	svc.StartUserAPIKeySlotReleaseWorker()
	releases := make([]func(), 0, requestCount)

	for i := 0; i < requestCount; i++ {
		result, err := svc.AcquireUserSlotForAPIKey(s.ctx, userID, requestCount, apiKeyID)
		require.NoError(s.T(), err)
		require.True(s.T(), result.Acquired)
		releases = append(releases, result.ReleaseFunc)
	}
	require.Equal(s.T(), int64(requestCount), mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)))
	require.Equal(s.T(), int64(requestCount), mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(apiKeyID)))

	for _, release := range releases {
		release()
	}
	require.Eventually(s.T(), func() bool {
		return mustZCard(s.T(), s.rdb, s.ctx, userSlotKey(userID)) == 0 &&
			mustZCard(s.T(), s.rdb, s.ctx, apiKeySlotKey(apiKeyID)) == 0 &&
			svc.UserAPIKeySlotReleaseRuntimeStats().CompletedTotal == requestCount
	}, 3*time.Second, 5*time.Millisecond)
	stats := svc.UserAPIKeySlotReleaseRuntimeStats()
	require.Equal(s.T(), uint64(requestCount), stats.EnqueuedTotal)
	require.Zero(s.T(), stats.QueueFullTotal)
	require.Zero(s.T(), stats.BatchFailureTotal)
}

func mustZCard(t *testing.T, client redis.UniversalClient, ctx context.Context, key string) int64 {
	t.Helper()
	count, err := client.ZCard(ctx, key).Result()
	require.NoError(t, err)
	return count
}

func (s *ConcurrencyCacheSuite) TestUserSlot_TTL() {
	userID := int64(200)
	reqID := "req_ttl_test"
	slotKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)

	ok, err := s.cache.AcquireUserSlot(s.ctx, userID, 5, reqID)
	require.NoError(s.T(), err, "AcquireUserSlot")
	require.True(s.T(), ok)

	ttl, err := s.rdb.TTL(s.ctx, slotKey).Result()
	require.NoError(s.T(), err, "TTL")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)
}

func (s *ConcurrencyCacheSuite) TestWaitQueue_IncrementAndDecrement() {
	userID := int64(20)
	waitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)

	ok, err := s.cache.IncrementWaitCount(s.ctx, userID, 2)
	require.NoError(s.T(), err, "IncrementWaitCount 1")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementWaitCount(s.ctx, userID, 2)
	require.NoError(s.T(), err, "IncrementWaitCount 2")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementWaitCount(s.ctx, userID, 2)
	require.NoError(s.T(), err, "IncrementWaitCount 3")
	require.False(s.T(), ok, "expected wait increment over max to fail")

	ttl, err := s.rdb.TTL(s.ctx, waitKey).Result()
	require.NoError(s.T(), err, "TTL waitKey")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)

	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount")

	val, err := s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey")
	}
	require.Equal(s.T(), 1, val, "expected wait count 1")
}

func (s *ConcurrencyCacheSuite) TestWaitQueue_DecrementNoNegative() {
	userID := int64(300)
	waitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)

	// Test decrement on non-existent key - should not error and should not create negative value
	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount on non-existent key")

	// Verify no key was created or it's not negative
	val, err := s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey")
	}
	require.GreaterOrEqual(s.T(), val, 0, "expected non-negative wait count after decrement on empty")

	// Set count to 1, then decrement twice
	ok, err := s.cache.IncrementWaitCount(s.ctx, userID, 5)
	require.NoError(s.T(), err, "IncrementWaitCount")
	require.True(s.T(), ok)

	// Decrement once (1 -> 0)
	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount")

	// Decrement again on 0 - should not go negative
	require.NoError(s.T(), s.cache.DecrementWaitCount(s.ctx, userID), "DecrementWaitCount on zero")

	// Verify count is 0, not negative
	val, err = s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey after double decrement")
	}
	require.GreaterOrEqual(s.T(), val, 0, "expected non-negative wait count")
}

func (s *ConcurrencyCacheSuite) TestAccountWaitQueue_IncrementAndDecrement() {
	accountID := int64(30)
	waitKey := fmt.Sprintf("%s%d", accountWaitKeyPrefix, accountID)

	ok, err := s.cache.IncrementAccountWaitCount(s.ctx, accountID, 2)
	require.NoError(s.T(), err, "IncrementAccountWaitCount 1")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementAccountWaitCount(s.ctx, accountID, 2)
	require.NoError(s.T(), err, "IncrementAccountWaitCount 2")
	require.True(s.T(), ok)

	ok, err = s.cache.IncrementAccountWaitCount(s.ctx, accountID, 2)
	require.NoError(s.T(), err, "IncrementAccountWaitCount 3")
	require.False(s.T(), ok, "expected account wait increment over max to fail")

	ttl, err := s.rdb.TTL(s.ctx, waitKey).Result()
	require.NoError(s.T(), err, "TTL account waitKey")
	s.AssertTTLWithin(ttl, 1*time.Second, testSlotTTL)

	require.NoError(s.T(), s.cache.DecrementAccountWaitCount(s.ctx, accountID), "DecrementAccountWaitCount")

	val, err := s.rdb.Get(s.ctx, waitKey).Int()
	if !errors.Is(err, redis.Nil) {
		require.NoError(s.T(), err, "Get waitKey")
	}
	require.Equal(s.T(), 1, val, "expected account wait count 1")
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots() {
	accountID := int64(901)
	userID := int64(902)
	accountKey := fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)
	userKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
	userWaitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
	accountWaitKey := fmt.Sprintf("%s%d", accountWaitKeyPrefix, accountID)

	nowTime, err := s.rdb.Time(s.ctx).Result()
	require.NoError(s.T(), err)
	now := nowTime.Unix()
	expired := now - int64(testSlotTTL.Seconds()) - 1
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, accountKey,
		redis.Z{Score: float64(now), Member: "otherproc-1"},
		redis.Z{Score: float64(now), Member: "keep-1"},
		redis.Z{Score: float64(expired), Member: "expired-1"},
	).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userKey,
		redis.Z{Score: float64(now), Member: "otherproc-2"},
		redis.Z{Score: float64(now), Member: "keep-2"},
		redis.Z{Score: float64(expired), Member: "expired-2"},
	).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, userWaitKey, 3, time.Minute).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, accountWaitKey, 2, time.Minute).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "keep-"))

	accountMembers, err := s.rdb.ZRange(s.ctx, accountKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.ElementsMatch(s.T(), []string{"keep-1", "otherproc-1"}, accountMembers)

	userMembers, err := s.rdb.ZRange(s.ctx, userKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.ElementsMatch(s.T(), []string{"keep-2", "otherproc-2"}, userMembers)

	userWait, err := s.rdb.Get(s.ctx, userWaitKey).Int()
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, userWait)

	accountWait, err := s.rdb.Get(s.ctx, accountWaitKey).Int()
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, accountWait)
}

func (s *ConcurrencyCacheSuite) TestGetAccountConcurrency_Missing() {
	// When no slots exist, GetAccountConcurrency should return 0
	cur, err := s.cache.GetAccountConcurrency(s.ctx, 999)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cur)
}

func (s *ConcurrencyCacheSuite) TestGetUserConcurrency_Missing() {
	// When no slots exist, GetUserConcurrency should return 0
	cur, err := s.cache.GetUserConcurrency(s.ctx, 999)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 0, cur)
}

func (s *ConcurrencyCacheSuite) TestGetAccountsLoadBatch() {
	s.T().Skip("TODO: Fix this test - CurrentConcurrency returns 0 instead of expected value in CI")
	// Setup: Create accounts with different load states
	account1 := int64(100)
	account2 := int64(101)
	account3 := int64(102)

	// Account 1: 2/3 slots used, 1 waiting
	ok, err := s.cache.AcquireAccountSlot(s.ctx, account1, 3, "req1")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireAccountSlot(s.ctx, account1, 3, "req2")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.IncrementAccountWaitCount(s.ctx, account1, 5)
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Account 2: 1/2 slots used, 0 waiting
	ok, err = s.cache.AcquireAccountSlot(s.ctx, account2, 2, "req3")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Account 3: 0/1 slots used, 0 waiting (idle)

	// Query batch load
	accounts := []service.AccountWithConcurrency{
		{ID: account1, MaxConcurrency: 3},
		{ID: account2, MaxConcurrency: 2},
		{ID: account3, MaxConcurrency: 1},
	}

	loadMap, err := s.cache.GetAccountsLoadBatch(s.ctx, accounts)
	require.NoError(s.T(), err)
	require.Len(s.T(), loadMap, 3)

	// Verify account1: (2 + 1) / 3 = 100%
	load1 := loadMap[account1]
	require.NotNil(s.T(), load1)
	require.Equal(s.T(), account1, load1.AccountID)
	require.Equal(s.T(), 2, load1.CurrentConcurrency)
	require.Equal(s.T(), 1, load1.WaitingCount)
	require.Equal(s.T(), 100, load1.LoadRate)

	// Verify account2: (1 + 0) / 2 = 50%
	load2 := loadMap[account2]
	require.NotNil(s.T(), load2)
	require.Equal(s.T(), account2, load2.AccountID)
	require.Equal(s.T(), 1, load2.CurrentConcurrency)
	require.Equal(s.T(), 0, load2.WaitingCount)
	require.Equal(s.T(), 50, load2.LoadRate)

	// Verify account3: (0 + 0) / 1 = 0%
	load3 := loadMap[account3]
	require.NotNil(s.T(), load3)
	require.Equal(s.T(), account3, load3.AccountID)
	require.Equal(s.T(), 0, load3.CurrentConcurrency)
	require.Equal(s.T(), 0, load3.WaitingCount)
	require.Equal(s.T(), 0, load3.LoadRate)
}

func (s *ConcurrencyCacheSuite) TestGetAccountsLoadBatch_Empty() {
	// Test with empty account list
	loadMap, err := s.cache.GetAccountsLoadBatch(s.ctx, []service.AccountWithConcurrency{})
	require.NoError(s.T(), err)
	require.Empty(s.T(), loadMap)
}

func (s *ConcurrencyCacheSuite) TestCleanupExpiredAccountSlots() {
	accountID := int64(200)
	slotKey := fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)

	// Acquire 3 slots
	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 5, "req1")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireAccountSlot(s.ctx, accountID, 5, "req2")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireAccountSlot(s.ctx, accountID, 5, "req3")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Verify 3 slots exist
	cur, err := s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, cur)

	// Manually set old timestamps for req1 and req2 (simulate expired slots)
	now := time.Now().Unix()
	expiredTime := now - int64(testSlotTTL.Seconds()) - 10 // 10 seconds past TTL
	err = s.rdb.ZAdd(s.ctx, slotKey, redis.Z{Score: float64(expiredTime), Member: "req1"}).Err()
	require.NoError(s.T(), err)
	err = s.rdb.ZAdd(s.ctx, slotKey, redis.Z{Score: float64(expiredTime), Member: "req2"}).Err()
	require.NoError(s.T(), err)

	// Run cleanup
	err = s.cache.CleanupExpiredAccountSlots(s.ctx, accountID)
	require.NoError(s.T(), err)

	// Verify only 1 slot remains (req3)
	cur, err = s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 1, cur)

	// Verify req3 still exists
	members, err := s.rdb.ZRange(s.ctx, slotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.Len(s.T(), members, 1)
	require.Equal(s.T(), "req3", members[0])
}

func (s *ConcurrencyCacheSuite) TestCleanupExpiredAccountSlots_NoExpired() {
	accountID := int64(201)

	// Acquire 2 fresh slots
	ok, err := s.cache.AcquireAccountSlot(s.ctx, accountID, 5, "req1")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)
	ok, err = s.cache.AcquireAccountSlot(s.ctx, accountID, 5, "req2")
	require.NoError(s.T(), err)
	require.True(s.T(), ok)

	// Run cleanup (should not remove anything)
	err = s.cache.CleanupExpiredAccountSlots(s.ctx, accountID)
	require.NoError(s.T(), err)

	// Verify both slots still exist
	cur, err := s.cache.GetAccountConcurrency(s.ctx, accountID)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, cur)
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_RemovesOnlyExpiredSlots() {
	accountID := int64(901)
	userID := int64(902)
	accountSlotKey := fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)
	userSlotKey := fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
	userWaitKey := fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
	accountWaitKey := fmt.Sprintf("%s%d", accountWaitKeyPrefix, accountID)

	nowTime, err := s.rdb.Time(s.ctx).Result()
	require.NoError(s.T(), err)
	now := float64(nowTime.Unix())
	expired := now - testSlotTTL.Seconds() - 1
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, accountSlotKey,
		redis.Z{Score: now, Member: "oldproc-1"},
		redis.Z{Score: now, Member: "activeproc-1"},
		redis.Z{Score: expired, Member: "expiredproc-1"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, accountSlotKey, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, userSlotKey,
		redis.Z{Score: now, Member: "oldproc-2"},
		redis.Z{Score: now, Member: "activeproc-2"},
		redis.Z{Score: expired, Member: "expiredproc-2"},
	).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, userSlotKey, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, userWaitKey, 3, testSlotTTL).Err())
	require.NoError(s.T(), s.rdb.Set(s.ctx, accountWaitKey, 2, testSlotTTL).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "activeproc-"))

	accountMembers, err := s.rdb.ZRange(s.ctx, accountSlotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.ElementsMatch(s.T(), []string{"activeproc-1", "oldproc-1"}, accountMembers)

	userMembers, err := s.rdb.ZRange(s.ctx, userSlotKey, 0, -1).Result()
	require.NoError(s.T(), err)
	require.ElementsMatch(s.T(), []string{"activeproc-2", "oldproc-2"}, userMembers)

	userWait, err := s.rdb.Get(s.ctx, userWaitKey).Int()
	require.NoError(s.T(), err)
	require.Equal(s.T(), 3, userWait)
	accountWait, err := s.rdb.Get(s.ctx, accountWaitKey).Int()
	require.NoError(s.T(), err)
	require.Equal(s.T(), 2, accountWait)
}

func (s *ConcurrencyCacheSuite) TestCleanupStaleProcessSlots_DeletesEmptySlotKeys() {
	accountID := int64(903)
	accountSlotKey := fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)
	now, err := s.rdb.Time(s.ctx).Result()
	require.NoError(s.T(), err)
	expired := now.Unix() - int64(testSlotTTL.Seconds()) - 1
	require.NoError(s.T(), s.rdb.ZAdd(s.ctx, accountSlotKey, redis.Z{Score: float64(expired), Member: "oldproc-1"}).Err())
	require.NoError(s.T(), s.rdb.Expire(s.ctx, accountSlotKey, testSlotTTL).Err())

	require.NoError(s.T(), s.cache.CleanupStaleProcessSlots(s.ctx, "activeproc-"))

	exists, err := s.rdb.Exists(s.ctx, accountSlotKey).Result()
	require.NoError(s.T(), err)
	require.EqualValues(s.T(), 0, exists)
}
