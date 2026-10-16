package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

type userGroupRateResolverRepoStub struct {
	UserGroupRateRepository

	rate  *float64
	err   error
	calls int
}

func (s *userGroupRateResolverRepoStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.rate, nil
}

func TestNewUserGroupRateResolver_Defaults(t *testing.T) {
	resolver := newUserGroupRateResolver(nil, nil, 0, nil, "")

	require.NotNil(t, resolver)
	require.NotNil(t, resolver.cache)
	require.Equal(t, defaultUserGroupRateCacheTTL, resolver.cacheTTL)
	require.NotNil(t, resolver.sf)
	require.Equal(t, "service.gateway", resolver.logComponent)
}

func TestUserGroupRateResolverResolve_FallbackForNilResolverAndInvalidIDs(t *testing.T) {
	var nilResolver *userGroupRateResolver
	got, err := nilResolver.Resolve(context.Background(), 101, 202, 1.4)
	require.NoError(t, err)
	require.Equal(t, 1.4, got)

	resolver := newUserGroupRateResolver(nil, nil, time.Second, nil, "service.test")
	got, err = resolver.Resolve(context.Background(), 0, 202, 1.4)
	require.NoError(t, err)
	require.Equal(t, 1.4, got)
	got, err = resolver.Resolve(context.Background(), 101, 0, 1.4)
	require.NoError(t, err)
	require.Equal(t, 1.4, got)
}

func TestUserGroupRateResolverResolve_InvalidCacheEntryLoadsRepoAndCaches(t *testing.T) {
	resetGatewayHotpathStatsForTest()

	rate := 1.7
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	cache := gocache.New(time.Minute, time.Minute)
	cache.Set("101:202", "bad-cache", time.Minute)
	resolver := newUserGroupRateResolver(repo, cache, time.Minute, nil, "service.test")

	got, err := resolver.Resolve(context.Background(), 101, 202, 1.2)
	require.NoError(t, err)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)

	cached, ok := cache.Get("101:202")
	require.True(t, ok)
	require.Equal(t, rate, cached)

	hit, miss, load, _, fallback := GatewayUserGroupRateCacheStats()
	require.Equal(t, int64(0), hit)
	require.Equal(t, int64(1), miss)
	require.Equal(t, int64(1), load)
	require.Equal(t, int64(0), fallback)
}

func TestGatewayServiceGetUserGroupRateMultiplier_FallbacksAndUsesExistingResolver(t *testing.T) {
	var nilSvc *GatewayService
	got, err := nilSvc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.3)
	require.NoError(t, err)
	require.Equal(t, 1.3, got)

	rate := 1.9
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.gateway")
	svc := &GatewayService{userGroupRateResolver: resolver}

	got, err = svc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.2)
	require.NoError(t, err)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)
}

func TestGatewayServiceGetUserGroupRateMultiplier_RemoteGatewayKeyUsesSnapshotRate(t *testing.T) {
	rate := 1.9
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	svc := &GatewayService{
		cfg:                   &config.Config{RunMode: config.RunModeGatewayLite},
		userGroupRateResolver: newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.gateway"),
	}

	// 未标记为本地 Key 的 gateway-lite 请求来自主站，倍率已经包含在 Key 快照中。
	got, err := svc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.2)
	require.NoError(t, err)
	require.Equal(t, 1.2, got)
	require.Equal(t, 0, repo.calls)

	localCtx := ContextWithGatewayLiteLocalAPIKey(context.Background())
	got, err = svc.getUserGroupRateMultiplier(localCtx, 101, 202, 1.2)
	require.NoError(t, err)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)
}

func TestUserGroupRateResolverResolve_RepoErrorIsFailClosedAndNotCached(t *testing.T) {
	cache := gocache.New(time.Minute, time.Minute)
	repo := &userGroupRateResolverRepoStub{err: context.DeadlineExceeded}
	resolver := newUserGroupRateResolver(repo, cache, time.Minute, nil, "service.test")

	for range 2 {
		got, err := resolver.Resolve(context.Background(), 101, 202, 1.2)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Zero(t, got)
	}

	require.Equal(t, 2, repo.calls, "失败结果不能被缓存为分组默认倍率")
	_, cached := cache.Get("101:202")
	require.False(t, cached)
}
