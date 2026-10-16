package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUserRPMCacheForTest(t *testing.T) (*userRPMCacheImpl, *miniredis.Miniredis) {
	t.Helper()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewUserRPMCache(rdb)
	impl, ok := cache.(*userRPMCacheImpl)
	require.True(t, ok)
	return impl, mini
}

func TestUserRPMCache_CombinedRPMAndTPMIsAtomicAndDoesNotPolluteRejectedTPM(t *testing.T) {
	cache, mini := newUserRPMCacheForTest(t)
	ctx := context.Background()
	const userID int64 = 42

	combined, ok := any(cache).(service.UserRPMTPMCombinedCache)
	require.True(t, ok)

	rpm, tpm, err := combined.IncrementUserRPMAndTPM(ctx, userID, 2, 100)
	require.NoError(t, err)
	require.Equal(t, 1, rpm)
	require.Equal(t, 100, tpm)

	rpm, tpm, err = combined.IncrementUserRPMAndTPM(ctx, userID, 2, 50)
	require.NoError(t, err)
	require.Equal(t, 2, rpm)
	require.Equal(t, 150, tpm)

	// 第三次请求超过 RPM：RPM 按旧语义继续累计，但 TPM 不能增加。
	rpm, tpm, err = combined.IncrementUserRPMAndTPM(ctx, userID, 2, 999)
	require.NoError(t, err)
	require.Equal(t, 3, rpm)
	require.Equal(t, 150, tpm)

	storedRPM, err := cache.GetUserRPM(ctx, userID)
	require.NoError(t, err)
	storedTPM, err := cache.GetUserTPM(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, 3, storedRPM)
	require.Equal(t, 150, storedTPM)
	require.Greater(t, mini.TTL(userRateKey(userID)), time.Duration(0))
}

func TestUserRPMCache_CombinedGroupRPMAndUserLimitsPreserveRejectionOrder(t *testing.T) {
	cache, mini := newUserRPMCacheForTest(t)
	ctx := context.Background()
	const userID int64 = 44

	combined, ok := any(cache).(service.UserGroupRPMTPMCombinedCache)
	require.True(t, ok)

	groupRPM, userRPM, userTPM, err := combined.IncrementUserGroupRPMAndUserRPMTPM(ctx, userID, 10, 2, 2, 100)
	require.NoError(t, err)
	require.Equal(t, 1, groupRPM)
	require.Equal(t, 1, userRPM)
	require.Equal(t, 100, userTPM)

	groupRPM, userRPM, userTPM, err = combined.IncrementUserGroupRPMAndUserRPMTPM(ctx, userID, 10, 2, 2, 50)
	require.NoError(t, err)
	require.Equal(t, 2, groupRPM)
	require.Equal(t, 2, userRPM)
	require.Equal(t, 150, userTPM)

	// 分组先超限：只允许分组 RPM 继续累计，用户 RPM/TPM 必须保持不变。
	groupRPM, userRPM, userTPM, err = combined.IncrementUserGroupRPMAndUserRPMTPM(ctx, userID, 10, 2, 2, 999)
	require.NoError(t, err)
	require.Equal(t, 3, groupRPM)
	require.Equal(t, 2, userRPM)
	require.Equal(t, 150, userTPM)
	require.Greater(t, mini.TTL(userRateKey(userID)), time.Duration(0))
}

func TestUserRPMCache_CombinedGroupRPMDoesNotPolluteTPMWhenUserRPMRejects(t *testing.T) {
	cache, _ := newUserRPMCacheForTest(t)
	ctx := context.Background()
	const userID int64 = 45
	combined := any(cache).(service.UserGroupRPMTPMCombinedCache)

	_, _, _, err := combined.IncrementUserGroupRPMAndUserRPMTPM(ctx, userID, 10, 100, 1, 100)
	require.NoError(t, err)
	groupRPM, userRPM, userTPM, err := combined.IncrementUserGroupRPMAndUserRPMTPM(ctx, userID, 10, 100, 1, 999)
	require.NoError(t, err)
	require.Equal(t, 2, groupRPM)
	require.Equal(t, 2, userRPM)
	require.Equal(t, 100, userTPM)
}

func TestUserRPMCache_CombinedGroupRPMParallelBoundaryIsAtomic(t *testing.T) {
	cache, _ := newUserRPMCacheForTest(t)
	ctx := context.Background()
	const (
		userID     int64 = 46
		requests         = 64
		groupLimit       = 32
	)
	combined := any(cache).(service.UserGroupRPMTPMCombinedCache)

	start := make(chan struct{})
	errCh := make(chan error, requests)
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _, _, err := combined.IncrementUserGroupRPMAndUserRPMTPM(ctx, userID, 10, groupLimit, requests, 1)
			errCh <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	groupRPM, err := cache.GetUserGroupRPM(ctx, userID, 10)
	require.NoError(t, err)
	userRPM, err := cache.GetUserRPM(ctx, userID)
	require.NoError(t, err)
	userTPM, err := cache.GetUserTPM(ctx, userID)
	require.NoError(t, err)
	require.Equal(t, requests, groupRPM, "所有尝试仍应计入最外层分组 RPM")
	require.Equal(t, groupLimit, userRPM, "只有通过分组门禁的请求才能进入用户 RPM")
	require.Equal(t, groupLimit, userTPM, "只有通过分组和用户 RPM 的请求才能进入 TPM")
}

func TestUserRPMCache_GroupAndUserCountersShareOneClusterSafeHash(t *testing.T) {
	cache, mini := newUserRPMCacheForTest(t)
	ctx := context.Background()
	const userID int64 = 43

	groupOne, err := cache.IncrementUserGroupRPM(ctx, userID, 10)
	require.NoError(t, err)
	groupTwo, err := cache.IncrementUserGroupRPM(ctx, userID, 20)
	require.NoError(t, err)
	userCount, err := cache.IncrementUserRPM(ctx, userID)
	require.NoError(t, err)

	require.Equal(t, 1, groupOne)
	require.Equal(t, 1, groupTwo)
	require.Equal(t, 1, userCount)
	require.True(t, mini.Exists(userRateKey(userID)))
	require.Equal(t, []string{userRateKey(userID)}, mini.Keys())
}

func TestUserRPMCache_OrganizationSharedRateGateRejectsWithoutPollution(t *testing.T) {
	cache, mini := newUserRPMCacheForTest(t)
	ctx := context.Background()
	combined := any(cache).(service.OrganizationMemberGroupRPMTPMCombinedCache)

	_, _, _, _, _, reason, err := combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 50, 1, 1000, 101, 5, 1000, 7, 5, -1001, 1, 1000, 100)
	require.NoError(t, err)
	require.Zero(t, reason)

	// 企业 RPM=1 已满，另一个员工被拒绝；他的成员 Hash 不得被创建。
	organizationRPM, organizationTPM, memberRPM, memberTPM, groupRPM, reason, err := combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 50, 1, 1000, 202, 5, 1000, 7, 5, -2002, 1, 1000, 999)
	require.NoError(t, err)
	require.Equal(t, 1, reason)
	require.Equal(t, 1, organizationRPM)
	require.Equal(t, 100, organizationTPM)
	require.Zero(t, memberRPM)
	require.Zero(t, memberTPM)
	require.Zero(t, groupRPM)
	require.False(t, mini.Exists(organizationMemberRateKey(50, 202)))

	// 不同企业即使 userID 相同也使用独立共享桶。
	_, _, _, _, _, reason, err = combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 60, 1, 1000, 101, 5, 1000, 7, 5, -3003, 1, 1000, 100)
	require.NoError(t, err)
	require.Zero(t, reason)
}

func TestUserRPMCache_OrganizationAPIKeyRPMAndTPMAreIndependent(t *testing.T) {
	cache, mini := newUserRPMCacheForTest(t)
	ctx := context.Background()
	combined := any(cache).(service.OrganizationMemberGroupRPMTPMCombinedCache)

	_, _, _, _, _, reason, err := combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 50, 100, 10000, 101, 100, 10000, 7, 100, -1001, 1, 1000, 100)
	require.NoError(t, err)
	require.Zero(t, reason)

	_, _, _, _, _, reason, err = combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 50, 100, 10000, 101, 100, 10000, 7, 100, -1001, 1, 1000, 100)
	require.NoError(t, err)
	require.Equal(t, 6, reason, "Key A 应命中自己的 RPM 上限")

	_, _, _, _, _, reason, err = combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 50, 100, 10000, 101, 100, 10000, 7, 100, -1002, 1, 1000, 100)
	require.NoError(t, err)
	require.Zero(t, reason, "Key B 不得继承 Key A 的 RPM 计数")

	_, _, _, _, _, reason, err = combined.IncrementOrganizationMemberGroupRPMAndTPM(ctx, 50, 100, 10000, 101, 100, 10000, 7, 100, -1003, 100, 50, 51)
	require.NoError(t, err)
	require.Equal(t, 7, reason, "Key TPM 应在写入其它计数前拒绝")
	require.False(t, mini.Exists(organizationAPIKeyRateKey(50, -1003)))
}
