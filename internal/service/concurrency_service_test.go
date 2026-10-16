//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubConcurrencyCacheForTest 用于并发服务单元测试的缓存桩
type stubConcurrencyCacheForTest struct {
	acquireResult        bool
	acquireErr           error
	releaseErr           error
	concurrency          int
	concurrencyErr       error
	waitAllowed          bool
	waitErr              error
	waitCount            int
	waitCountErr         error
	loadBatch            map[int64]*AccountLoadInfo
	loadBatchErr         error
	usersLoadBatch       map[int64]*UserLoadInfo
	usersLoadErr         error
	cleanupErr           error
	apiKeyTrackErr       error
	apiKeyReleaseErr     error
	apiKeyConcurrency    map[int64]int
	apiKeyConcurrencyErr error
	ingressTrackErr      error
	ingressReleaseErr    error
	ingressCounts        map[int64]int
	ingressCountsErr     error

	// 记录调用
	releasedAccountIDs        []int64
	releasedRequestIDs        []string
	loadBatchCalls            atomic.Int64
	trackedAPIKeyIDs          []int64
	trackedAPIKeyRequestIDs   []string
	releasedAPIKeyIDs         []int64
	releasedAPIKeyRequestIDs  []string
	trackedIngressGroups      []int64
	trackedIngressRequestIDs  []string
	releasedIngressGroups     []int64
	releasedIngressRequestIDs []string
}

type ingressLeaseCacheForTest struct {
	stubConcurrencyCacheForTest
	acquireIngressResult bool
	acquireIngressErr    error
	acquireIngressFn     func(context.Context, int64, int, string) (bool, error)
	refreshIngressResult bool
	refreshIngressErr    error
	refreshIngressFn     func(context.Context, int64, string) (bool, error)
	releaseIngressErr    error
	releaseIngressFn     func(context.Context, int64, string) error
	acquireIngressCalls  int
	refreshIngressCalls  int
	releaseIngressCalls  int
}

func (c *ingressLeaseCacheForTest) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	c.acquireIngressCalls++
	if c.acquireIngressFn != nil {
		return c.acquireIngressFn(ctx, apiKeyID, maxConnections, leaseID)
	}
	return c.acquireIngressResult, c.acquireIngressErr
}

func (c *ingressLeaseCacheForTest) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	c.refreshIngressCalls++
	if c.refreshIngressFn != nil {
		return c.refreshIngressFn(ctx, apiKeyID, leaseID)
	}
	return c.refreshIngressResult, c.refreshIngressErr
}

func (c *ingressLeaseCacheForTest) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	c.releaseIngressCalls++
	if c.releaseIngressFn != nil {
		return c.releaseIngressFn(ctx, apiKeyID, leaseID)
	}
	return c.releaseIngressErr
}

var _ ConcurrencyCache = (*stubConcurrencyCacheForTest)(nil)
var _ OpenAIWSIngressLeaseCache = (*ingressLeaseCacheForTest)(nil)

func (c *stubConcurrencyCacheForTest) AcquireAccountSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return c.acquireResult, c.acquireErr
}
func (c *stubConcurrencyCacheForTest) ReleaseAccountSlot(_ context.Context, accountID int64, requestID string) error {
	c.releasedAccountIDs = append(c.releasedAccountIDs, accountID)
	c.releasedRequestIDs = append(c.releasedRequestIDs, requestID)
	return c.releaseErr
}
func (c *stubConcurrencyCacheForTest) GetAccountConcurrency(_ context.Context, _ int64) (int, error) {
	return c.concurrency, c.concurrencyErr
}
func (c *stubConcurrencyCacheForTest) GetAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(accountIDs))
	for _, accountID := range accountIDs {
		if c.concurrencyErr != nil {
			return nil, c.concurrencyErr
		}
		result[accountID] = c.concurrency
	}
	return result, nil
}
func (c *stubConcurrencyCacheForTest) IncrementAccountWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return c.waitAllowed, c.waitErr
}
func (c *stubConcurrencyCacheForTest) DecrementAccountWaitCount(_ context.Context, _ int64) error {
	return nil
}
func (c *stubConcurrencyCacheForTest) GetAccountWaitingCount(_ context.Context, _ int64) (int, error) {
	return c.waitCount, c.waitCountErr
}
func (c *stubConcurrencyCacheForTest) AcquireUserSlot(_ context.Context, _ int64, _ int, _ string) (bool, error) {
	return c.acquireResult, c.acquireErr
}
func (c *stubConcurrencyCacheForTest) ReleaseUserSlot(_ context.Context, _ int64, _ string) error {
	return c.releaseErr
}
func (c *stubConcurrencyCacheForTest) GetUserConcurrency(_ context.Context, _ int64) (int, error) {
	return c.concurrency, c.concurrencyErr
}
func (c *stubConcurrencyCacheForTest) TrackAPIKeySlot(_ context.Context, apiKeyID int64, requestID string) error {
	c.trackedAPIKeyIDs = append(c.trackedAPIKeyIDs, apiKeyID)
	c.trackedAPIKeyRequestIDs = append(c.trackedAPIKeyRequestIDs, requestID)
	return c.apiKeyTrackErr
}
func (c *stubConcurrencyCacheForTest) ReleaseAPIKeySlot(_ context.Context, apiKeyID int64, requestID string) error {
	c.releasedAPIKeyIDs = append(c.releasedAPIKeyIDs, apiKeyID)
	c.releasedAPIKeyRequestIDs = append(c.releasedAPIKeyRequestIDs, requestID)
	return c.apiKeyReleaseErr
}
func (c *stubConcurrencyCacheForTest) GetAPIKeyConcurrencyBatch(_ context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	if c.apiKeyConcurrencyErr != nil {
		return nil, c.apiKeyConcurrencyErr
	}
	result := make(map[int64]int, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = c.apiKeyConcurrency[apiKeyID]
	}
	return result, nil
}
func (c *stubConcurrencyCacheForTest) TrackGroupIngressSlot(_ context.Context, groupID int64, requestID string) error {
	c.trackedIngressGroups = append(c.trackedIngressGroups, groupID)
	c.trackedIngressRequestIDs = append(c.trackedIngressRequestIDs, requestID)
	return c.ingressTrackErr
}
func (c *stubConcurrencyCacheForTest) ReleaseGroupIngressSlot(_ context.Context, groupID int64, requestID string) error {
	c.releasedIngressGroups = append(c.releasedIngressGroups, groupID)
	c.releasedIngressRequestIDs = append(c.releasedIngressRequestIDs, requestID)
	return c.ingressReleaseErr
}
func (c *stubConcurrencyCacheForTest) GetGroupIngressConcurrencyBatch(_ context.Context, groupIDs []int64) (map[int64]int, error) {
	if c.ingressCountsErr != nil {
		return nil, c.ingressCountsErr
	}
	if c.ingressCounts == nil {
		return map[int64]int{}, nil
	}
	return c.ingressCounts, nil
}
func (c *stubConcurrencyCacheForTest) IncrementWaitCount(_ context.Context, _ int64, _ int) (bool, error) {
	return c.waitAllowed, c.waitErr
}
func (c *stubConcurrencyCacheForTest) DecrementWaitCount(_ context.Context, _ int64) error {
	return nil
}
func (c *stubConcurrencyCacheForTest) GetAccountsLoadBatch(_ context.Context, _ []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.loadBatchCalls.Add(1)
	return c.loadBatch, c.loadBatchErr
}
func (c *stubConcurrencyCacheForTest) GetUsersLoadBatch(_ context.Context, _ []UserWithConcurrency) (map[int64]*UserLoadInfo, error) {
	return c.usersLoadBatch, c.usersLoadErr
}
func (c *stubConcurrencyCacheForTest) CleanupExpiredAccountSlots(_ context.Context, _ int64) error {
	return c.cleanupErr
}

func (c *stubConcurrencyCacheForTest) CleanupStaleProcessSlots(_ context.Context, _ string) error {
	return c.cleanupErr
}

type trackingConcurrencyCache struct {
	stubConcurrencyCacheForTest
	cleanupPrefix string
}

type orderedAccountAcquireCacheForTest struct {
	stubConcurrencyCacheForTest
	results  map[int64]bool
	attempts []int64
}

func (c *orderedAccountAcquireCacheForTest) AcquireAccountSlot(_ context.Context, accountID int64, _ int, _ string) (bool, error) {
	c.attempts = append(c.attempts, accountID)
	return c.results[accountID], nil
}

type batchAccountAcquireCacheForTest struct {
	stubConcurrencyCacheForTest
	selectedID int64
	acquired   bool
	err        error
	candidates []AccountWithConcurrency
	requestID  string
}

func (c *batchAccountAcquireCacheForTest) AcquireFirstAvailableAccountSlot(_ context.Context, candidates []AccountWithConcurrency, requestID string) (int64, bool, error) {
	c.candidates = append([]AccountWithConcurrency(nil), candidates...)
	c.requestID = requestID
	return c.selectedID, c.acquired, c.err
}

type groupIngressLeaseCacheForTest struct {
	stubConcurrencyCacheForTest
	mu              sync.Mutex
	refreshInterval time.Duration
	refreshCalls    int
	releaseCalls    int
	trackedGroupID  int64
	trackedRequest  string
	released        chan struct{}
	releaseOnce     sync.Once
}

func (c *groupIngressLeaseCacheForTest) TrackGroupIngressSlot(_ context.Context, groupID int64, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.trackedGroupID = groupID
	c.trackedRequest = requestID
	return nil
}

func (c *groupIngressLeaseCacheForTest) RefreshGroupIngressSlot(_ context.Context, groupID int64, requestID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshCalls++
	return groupID == c.trackedGroupID && requestID == c.trackedRequest, nil
}

func (c *groupIngressLeaseCacheForTest) GroupIngressSlotRefreshInterval() time.Duration {
	return c.refreshInterval
}

func (c *groupIngressLeaseCacheForTest) ReleaseGroupIngressSlot(_ context.Context, groupID int64, requestID string) error {
	c.mu.Lock()
	c.releaseCalls++
	matched := groupID == c.trackedGroupID && requestID == c.trackedRequest
	c.mu.Unlock()
	if matched && c.released != nil {
		c.releaseOnce.Do(func() { close(c.released) })
	}
	return nil
}

type failoverConcurrencyCacheForTest struct {
	stubConcurrencyCacheForTest
	mu           sync.Mutex
	acquired     bool
	releaseCalls int
	released     chan struct{}
	releaseOnce  sync.Once
}

func (c *failoverConcurrencyCacheForTest) AcquireFailoverGroupSlot(context.Context, int64, int64, int64, int, string) (bool, error) {
	return c.acquired, nil
}

func (c *failoverConcurrencyCacheForTest) ReleaseFailoverGroupSlot(context.Context, int64, int64, int64, string) error {
	c.mu.Lock()
	c.releaseCalls++
	c.mu.Unlock()
	if c.released != nil {
		c.releaseOnce.Do(func() { close(c.released) })
	}
	return nil
}

type combinedUserAPIKeyConcurrencyCacheForTest struct {
	stubConcurrencyCacheForTest
	mu                    sync.Mutex
	combinedAcquireResult bool
	combinedAcquireErr    error
	combinedReleaseErr    error
	combinedAcquireCalls  int
	combinedReleaseCalls  int
	combinedUserID        int64
	combinedMax           int
	combinedAPIKeyID      int64
	combinedRequestID     string
	releasedUserID        int64
	releasedAPIKeyID      int64
	releasedRequestID     string
	batchReleaseCalls     int
	batchReleases         []UserAPIKeySlotRelease
	batchReleaseErrors    []error
	batchStarted          chan struct{}
	batchContinue         chan struct{}
	batchStartOnce        sync.Once
}

type userGroupAPIKeyConcurrencyCacheForTest struct {
	combinedUserAPIKeyConcurrencyCacheForTest
	groupAcquireResult bool
	groupAcquireCalls  int
}

var _ UserAPIKeyConcurrencyCache = (*combinedUserAPIKeyConcurrencyCacheForTest)(nil)
var _ UserAPIKeySlotBatchReleaseCache = (*combinedUserAPIKeyConcurrencyCacheForTest)(nil)
var _ UserGroupAPIKeyConcurrencyCache = (*userGroupAPIKeyConcurrencyCacheForTest)(nil)

func (c *userGroupAPIKeyConcurrencyCacheForTest) AcquireUserGroupAndAPIKeySlot(_ context.Context, _ int64, _ int, _ int64, _ int, _ int64, _ string) (bool, error) {
	c.groupAcquireCalls++
	return c.groupAcquireResult, nil
}

func (c *userGroupAPIKeyConcurrencyCacheForTest) ReleaseUserGroupAndAPIKeySlot(_ context.Context, _, _, _ int64, _ string) error {
	return nil
}

func (c *combinedUserAPIKeyConcurrencyCacheForTest) AcquireUserAndAPIKeySlot(_ context.Context, userID int64, maxConcurrency int, apiKeyID int64, requestID string) (bool, error) {
	c.combinedAcquireCalls++
	c.combinedUserID = userID
	c.combinedMax = maxConcurrency
	c.combinedAPIKeyID = apiKeyID
	c.combinedRequestID = requestID
	return c.combinedAcquireResult, c.combinedAcquireErr
}

func (c *combinedUserAPIKeyConcurrencyCacheForTest) ReleaseUserAndAPIKeySlot(_ context.Context, userID int64, apiKeyID int64, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.combinedReleaseCalls++
	c.releasedUserID = userID
	c.releasedAPIKeyID = apiKeyID
	c.releasedRequestID = requestID
	return c.combinedReleaseErr
}

func (c *combinedUserAPIKeyConcurrencyCacheForTest) ReleaseUserAndAPIKeySlots(_ context.Context, releases []UserAPIKeySlotRelease) error {
	if c.batchStarted != nil {
		c.batchStartOnce.Do(func() { close(c.batchStarted) })
	}
	if c.batchContinue != nil {
		<-c.batchContinue
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchReleaseCalls++
	c.batchReleases = append(c.batchReleases, releases...)
	if len(c.batchReleaseErrors) >= c.batchReleaseCalls {
		return c.batchReleaseErrors[c.batchReleaseCalls-1]
	}
	return nil
}

func (c *trackingConcurrencyCache) CleanupStaleProcessSlots(_ context.Context, prefix string) error {
	c.cleanupPrefix = prefix
	return c.cleanupErr
}

func TestCleanupStaleProcessSlots_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}
	require.NoError(t, svc.CleanupStaleProcessSlots(context.Background()))
}

func TestCleanupStaleProcessSlots_DelegatesPrefix(t *testing.T) {
	cache := &trackingConcurrencyCache{}
	svc := NewConcurrencyService(cache)
	require.NoError(t, svc.CleanupStaleProcessSlots(context.Background()))
	require.Equal(t, RequestIDPrefix(), cache.cleanupPrefix)
}

func TestAcquireAccountSlot_Success(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireAccountSlot(context.Background(), 1, 5)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
}

func TestTrackGroupIngressSlotUsesInternalIDAndReleases(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{}
	svc := NewConcurrencyService(cache)
	release := svc.TrackGroupIngressSlot(context.Background(), 42)
	require.Len(t, cache.trackedIngressGroups, 1)
	require.Equal(t, int64(42), cache.trackedIngressGroups[0])
	require.NotEmpty(t, cache.trackedIngressRequestIDs[0])
	require.NotContains(t, cache.trackedIngressRequestIDs[0], "client")
	release()
	require.Equal(t, cache.trackedIngressRequestIDs[0], cache.releasedIngressRequestIDs[0])
}

func TestTrackGroupIngressSlotFailsOpenOnCacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{ingressTrackErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)
	release := svc.TrackGroupIngressSlot(context.Background(), 42)
	require.NotPanics(t, release)
	release()
	require.Empty(t, cache.releasedIngressRequestIDs)
}

func TestTrackGroupIngressSlotRefreshesAndReleasesOnCancellation(t *testing.T) {
	cache := &groupIngressLeaseCacheForTest{
		refreshInterval: 5 * time.Millisecond,
		released:        make(chan struct{}),
	}
	svc := NewConcurrencyService(cache)
	ctx, cancel := context.WithCancel(context.Background())
	release := svc.TrackGroupIngressSlot(ctx, 42)
	require.Eventually(t, func() bool {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		return cache.refreshCalls > 0
	}, time.Second, 5*time.Millisecond)

	cancel()
	select {
	case <-cache.released:
	case <-time.After(time.Second):
		t.Fatal("请求取消后入口统计槽位未及时释放")
	}
	release()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, 1, cache.releaseCalls, "显式释放与取消回调必须幂等")
}

func TestGetGroupIngressConcurrencyBatchReturnsCounts(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{ingressCounts: map[int64]int{42: 7}}
	svc := NewConcurrencyService(cache)
	got, err := svc.GetGroupIngressConcurrencyBatch(context.Background(), []int64{42, 99})
	require.NoError(t, err)
	require.Equal(t, 7, got[42])
	require.Equal(t, 0, got[99])
}

func TestGetGroupIngressConcurrencyBatchPropagatesUnknownState(t *testing.T) {
	wantErr := errors.New("redis down")
	cache := &stubConcurrencyCacheForTest{ingressCountsErr: wantErr}
	svc := NewConcurrencyService(cache)
	got, err := svc.GetGroupIngressConcurrencyBatch(context.Background(), []int64{42, 99})
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, got, "读取失败不能伪装成全部为零")
}

func TestAcquireFailoverGroupSlotReleasesOnCancellation(t *testing.T) {
	cache := &failoverConcurrencyCacheForTest{acquired: true, released: make(chan struct{})}
	svc := NewConcurrencyService(cache)
	ctx, cancel := context.WithCancel(context.Background())
	result, err := svc.AcquireFailoverGroupSlot(ctx, nil, 7, 20, 1)
	require.NoError(t, err)
	require.True(t, result.Acquired)

	cancel()
	select {
	case <-cache.released:
	case <-time.After(time.Second):
		t.Fatal("请求取消后兜底分组槽位未及时释放")
	}
	result.ReleaseFunc()
	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, 1, cache.releaseCalls, "显式释放与取消回调必须幂等")
}

func TestAcquireAccountSlot_Failure(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: false}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireAccountSlot(context.Background(), 1, 5)
	require.NoError(t, err)
	require.False(t, result.Acquired)
	require.Nil(t, result.ReleaseFunc)
}

func TestAcquireAccountSlot_UnlimitedConcurrency(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	for _, maxConcurrency := range []int{0, -1} {
		result, err := svc.AcquireAccountSlot(context.Background(), 1, maxConcurrency)
		require.NoError(t, err)
		require.True(t, result.Acquired, "maxConcurrency=%d 应无限制通过", maxConcurrency)
		require.NotNil(t, result.ReleaseFunc, "ReleaseFunc 应为 no-op 函数")
	}
}

func TestAcquireAccountSlot_CacheError(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireAccountSlot(context.Background(), 1, 5)
	require.Error(t, err)
	require.Nil(t, result)
}

func TestAcquireFirstAvailableAccountSlotFallsBackToOrderedSingleAcquire(t *testing.T) {
	cache := &orderedAccountAcquireCacheForTest{results: map[int64]bool{
		11: false,
		12: true,
		13: true,
	}}
	svc := NewConcurrencyService(cache)

	accountID, result, err := svc.AcquireFirstAvailableAccountSlot(context.Background(), []AccountWithConcurrency{
		{ID: 11, MaxConcurrency: 1},
		{ID: 12, MaxConcurrency: 1},
		{ID: 13, MaxConcurrency: 1},
	})
	require.NoError(t, err)
	require.Equal(t, int64(12), accountID)
	require.True(t, result.Acquired)
	require.Equal(t, []int64{11, 12}, cache.attempts)
}

func TestAcquireFirstAvailableAccountSlotUsesBatchAndReleasesOnlySelected(t *testing.T) {
	cache := &batchAccountAcquireCacheForTest{selectedID: 22, acquired: true}
	svc := NewConcurrencyService(cache)
	candidates := []AccountWithConcurrency{
		{ID: 21, MaxConcurrency: 1},
		{ID: 22, MaxConcurrency: 2},
		{ID: 23, MaxConcurrency: 3},
	}

	accountID, result, err := svc.AcquireFirstAvailableAccountSlot(context.Background(), candidates)
	require.NoError(t, err)
	require.Equal(t, int64(22), accountID)
	require.True(t, result.Acquired)
	require.Equal(t, candidates, cache.candidates)
	require.NotEmpty(t, cache.requestID)

	result.ReleaseFunc()
	require.Equal(t, []int64{22}, cache.releasedAccountIDs)
	require.Equal(t, []string{cache.requestID}, cache.releasedRequestIDs)
}

func TestAcquireFirstAvailableAccountSlotFallsThroughToUnlimitedCandidate(t *testing.T) {
	cache := &batchAccountAcquireCacheForTest{acquired: false}
	svc := NewConcurrencyService(cache)
	candidates := []AccountWithConcurrency{
		{ID: 31, MaxConcurrency: 1},
		{ID: 32, MaxConcurrency: 0},
	}

	accountID, result, err := svc.AcquireFirstAvailableAccountSlot(context.Background(), candidates)
	require.NoError(t, err)
	require.Equal(t, int64(32), accountID)
	require.True(t, result.Acquired)
	require.Equal(t, []AccountWithConcurrency{{ID: 31, MaxConcurrency: 1}}, cache.candidates,
		"批量脚本只应检查不限并发账号之前的有限前缀")
}

func TestAcquireFirstAvailableAccountSlotUnlimitedDoesNotRequireCache(t *testing.T) {
	svc := NewConcurrencyService(nil)
	accountID, result, err := svc.AcquireFirstAvailableAccountSlot(context.Background(), []AccountWithConcurrency{
		{ID: 33, MaxConcurrency: 0},
	})
	require.NoError(t, err)
	require.Equal(t, int64(33), accountID)
	require.True(t, result.Acquired)
}

func TestAcquireFirstAvailableAccountSlotRejectsUnknownBatchResult(t *testing.T) {
	cache := &batchAccountAcquireCacheForTest{selectedID: 999, acquired: true}
	svc := NewConcurrencyService(cache)

	accountID, result, err := svc.AcquireFirstAvailableAccountSlot(context.Background(), []AccountWithConcurrency{{ID: 21, MaxConcurrency: 1}})
	require.ErrorContains(t, err, "unknown account")
	require.Zero(t, accountID)
	require.Nil(t, result)
}

func TestAcquireAccountSlot_ReleaseDecrements(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireAccountSlot(context.Background(), 42, 5)
	require.NoError(t, err)
	require.True(t, result.Acquired)

	// 调用 ReleaseFunc 应释放槽位
	result.ReleaseFunc()

	require.Len(t, cache.releasedAccountIDs, 1)
	require.Equal(t, int64(42), cache.releasedAccountIDs[0])
	require.Len(t, cache.releasedRequestIDs, 1)
	require.NotEmpty(t, cache.releasedRequestIDs[0], "requestID 不应为空")
}

type batchReleaseConcurrencyCache struct {
	stubConcurrencyCacheForTest
	mu             sync.Mutex
	batchCalls     int
	batchReleases  []AccountSlotRelease
	batchStarted   chan struct{}
	batchContinue  chan struct{}
	batchStartOnce sync.Once
}

func (c *batchReleaseConcurrencyCache) ReleaseAccountSlots(_ context.Context, releases []AccountSlotRelease) error {
	if c.batchStarted != nil {
		c.batchStartOnce.Do(func() { close(c.batchStarted) })
	}
	if c.batchContinue != nil {
		<-c.batchContinue
	}
	c.mu.Lock()
	c.batchCalls++
	c.batchReleases = append(c.batchReleases, releases...)
	c.mu.Unlock()
	return nil
}

func (c *batchReleaseConcurrencyCache) ReleaseAccountSlot(_ context.Context, accountID int64, requestID string) error {
	c.mu.Lock()
	c.releasedAccountIDs = append(c.releasedAccountIDs, accountID)
	c.releasedRequestIDs = append(c.releasedRequestIDs, requestID)
	c.mu.Unlock()
	return c.releaseErr
}

func TestAcquireAccountSlotAsyncReleaseBatches(t *testing.T) {
	cache := &batchReleaseConcurrencyCache{}
	cache.acquireResult = true
	svc := NewConcurrencyService(cache)
	svc.startAccountSlotReleaseWorker(1024, 256, time.Millisecond)

	for i := 0; i < 300; i++ {
		result, err := svc.AcquireAccountSlot(context.Background(), 42, 1000)
		require.NoError(t, err)
		result.ReleaseFunc()
	}

	require.Eventually(t, func() bool {
		return svc.AccountSlotReleaseRuntimeStats().CompletedTotal == 300
	}, time.Second, time.Millisecond)
	stats := svc.AccountSlotReleaseRuntimeStats()
	require.Equal(t, uint64(300), stats.EnqueuedTotal)
	require.Equal(t, uint64(0), stats.QueueFullTotal)
	require.LessOrEqual(t, stats.BatchTotal, uint64(2))

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Len(t, cache.batchReleases, 300)
	require.Empty(t, cache.releasedAccountIDs)
}

func TestAcquireAccountSlotAsyncReleaseQueueFullFallsBackSynchronously(t *testing.T) {
	cache := &batchReleaseConcurrencyCache{
		batchStarted:  make(chan struct{}),
		batchContinue: make(chan struct{}),
	}
	cache.acquireResult = true
	svc := NewConcurrencyService(cache)
	svc.startAccountSlotReleaseWorker(1, 1, time.Millisecond)

	first, err := svc.AcquireAccountSlot(context.Background(), 42, 1000)
	require.NoError(t, err)
	first.ReleaseFunc()
	<-cache.batchStarted // worker 已取走第一条并停在批量缓存里

	second, err := svc.AcquireAccountSlot(context.Background(), 42, 1000)
	require.NoError(t, err)
	second.ReleaseFunc() // 占满容量为 1 的队列
	third, err := svc.AcquireAccountSlot(context.Background(), 42, 1000)
	require.NoError(t, err)
	third.ReleaseFunc() // 队列满，必须同步释放

	stats := svc.AccountSlotReleaseRuntimeStats()
	require.Equal(t, uint64(1), stats.QueueFullTotal)
	require.Equal(t, uint64(1), stats.SyncFallbackTotal)
	cache.mu.Lock()
	require.Len(t, cache.releasedAccountIDs, 1)
	cache.mu.Unlock()
	close(cache.batchContinue)
}

func TestAcquireUserSlot_IndependentFromAccount(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{acquireResult: true}
	svc := NewConcurrencyService(cache)

	// 用户槽位获取应独立于账户槽位
	result, err := svc.AcquireUserSlot(context.Background(), 100, 3)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
}

func TestAcquireUserSlot_UnlimitedConcurrency(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	result, err := svc.AcquireUserSlot(context.Background(), 1, 0)
	require.NoError(t, err)
	require.True(t, result.Acquired)
}

func TestAcquireUserSlotForAPIKey_UsesCombinedCache(t *testing.T) {
	cache := &combinedUserAPIKeyConcurrencyCacheForTest{combinedAcquireResult: true}
	svc := NewConcurrencyService(cache)

	result, err := svc.AcquireUserSlotForAPIKey(context.Background(), 202, 3, 77)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Acquired)
	require.NotNil(t, result.ReleaseFunc)
	require.Equal(t, 1, cache.combinedAcquireCalls)
	require.Equal(t, int64(202), cache.combinedUserID)
	require.Equal(t, 3, cache.combinedMax)
	require.Equal(t, int64(77), cache.combinedAPIKeyID)
	require.NotEmpty(t, cache.combinedRequestID)
	require.Empty(t, cache.trackedAPIKeyIDs, "combined path must not issue a second API Key tracking call")

	result.ReleaseFunc()
	require.Equal(t, 1, cache.combinedReleaseCalls)
	require.Equal(t, int64(202), cache.releasedUserID)
	require.Equal(t, int64(77), cache.releasedAPIKeyID)
	require.Equal(t, cache.combinedRequestID, cache.releasedRequestID)
	require.Empty(t, cache.releasedAPIKeyIDs, "combined path must not issue a second API Key release call")
}

func TestAcquireUserGroupSlotForAPIKey_FailsClosedWithoutStableIdentity(t *testing.T) {
	cache := &userGroupAPIKeyConcurrencyCacheForTest{groupAcquireResult: true}
	svc := NewConcurrencyService(cache)

	for _, tc := range []struct {
		name     string
		userID   int64
		apiKeyID int64
	}{
		{name: "missing user", userID: 0, apiKeyID: 77},
		{name: "missing api key", userID: 202, apiKeyID: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := svc.AcquireUserGroupSlotForAPIKey(context.Background(), tc.userID, 10, 99, 2, tc.apiKeyID)
			require.Error(t, err)
			require.Nil(t, result)
		})
	}
	require.Zero(t, cache.groupAcquireCalls)
}

func TestAcquireUserSlotForAPIKey_AsyncReleaseBatches(t *testing.T) {
	cache := &combinedUserAPIKeyConcurrencyCacheForTest{combinedAcquireResult: true}
	svc := NewConcurrencyService(cache)
	svc.startUserAPIKeySlotReleaseWorker(1024, 256, time.Millisecond)

	for i := 0; i < 300; i++ {
		result, err := svc.AcquireUserSlotForAPIKey(context.Background(), 202, 1000, 77)
		require.NoError(t, err)
		require.True(t, result.Acquired)
		result.ReleaseFunc()
	}

	require.Eventually(t, func() bool {
		return svc.UserAPIKeySlotReleaseRuntimeStats().CompletedTotal == 300
	}, time.Second, time.Millisecond)
	stats := svc.UserAPIKeySlotReleaseRuntimeStats()
	require.Equal(t, uint64(300), stats.EnqueuedTotal)
	require.Equal(t, uint64(0), stats.QueueFullTotal)
	require.LessOrEqual(t, stats.BatchTotal, uint64(2))

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Len(t, cache.batchReleases, 300)
	require.Zero(t, cache.combinedReleaseCalls, "批量队列正常时不能再逐条同步释放")
}

func TestAcquireUserSlotForAPIKey_AsyncReleaseQueueFullFallsBackSynchronously(t *testing.T) {
	cache := &combinedUserAPIKeyConcurrencyCacheForTest{
		combinedAcquireResult: true,
		batchStarted:          make(chan struct{}),
		batchContinue:         make(chan struct{}),
	}
	svc := NewConcurrencyService(cache)
	svc.startUserAPIKeySlotReleaseWorker(1, 1, time.Millisecond)

	first, err := svc.AcquireUserSlotForAPIKey(context.Background(), 202, 1000, 77)
	require.NoError(t, err)
	first.ReleaseFunc()
	<-cache.batchStarted

	second, err := svc.AcquireUserSlotForAPIKey(context.Background(), 202, 1000, 77)
	require.NoError(t, err)
	second.ReleaseFunc()
	third, err := svc.AcquireUserSlotForAPIKey(context.Background(), 202, 1000, 77)
	require.NoError(t, err)
	third.ReleaseFunc()

	stats := svc.UserAPIKeySlotReleaseRuntimeStats()
	require.Equal(t, uint64(1), stats.QueueFullTotal)
	require.Equal(t, uint64(1), stats.SyncFallbackTotal)
	cache.mu.Lock()
	require.Equal(t, 1, cache.combinedReleaseCalls)
	cache.mu.Unlock()
	close(cache.batchContinue)
}

func TestAcquireUserSlotForAPIKey_AsyncReleaseRetriesTransientFailure(t *testing.T) {
	cache := &combinedUserAPIKeyConcurrencyCacheForTest{
		combinedAcquireResult: true,
		batchReleaseErrors:    []error{errors.New("temporary redis error"), nil},
	}
	svc := NewConcurrencyService(cache)
	svc.startUserAPIKeySlotReleaseWorker(8, 8, time.Millisecond)

	result, err := svc.AcquireUserSlotForAPIKey(context.Background(), 202, 3, 77)
	require.NoError(t, err)
	result.ReleaseFunc()
	require.Eventually(t, func() bool {
		return svc.UserAPIKeySlotReleaseRuntimeStats().CompletedTotal == 1
	}, time.Second, time.Millisecond)

	stats := svc.UserAPIKeySlotReleaseRuntimeStats()
	require.Equal(t, uint64(1), stats.RetryTotal)
	require.Zero(t, stats.BatchFailureTotal)
	cache.mu.Lock()
	require.Equal(t, 2, cache.batchReleaseCalls)
	cache.mu.Unlock()
}

func TestAcquireUserSlotForAPIKey_AsyncReleaseIgnoresRequestCancellation(t *testing.T) {
	cache := &combinedUserAPIKeyConcurrencyCacheForTest{combinedAcquireResult: true}
	svc := NewConcurrencyService(cache)
	svc.startUserAPIKeySlotReleaseWorker(8, 8, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	result, err := svc.AcquireUserSlotForAPIKey(ctx, 202, 3, 77)
	require.NoError(t, err)
	cancel()
	result.ReleaseFunc()

	require.Eventually(t, func() bool {
		return svc.UserAPIKeySlotReleaseRuntimeStats().CompletedTotal == 1
	}, time.Second, time.Millisecond)
}

func TestAcquireUserSlotForAPIKey_CombinedCacheDenialAndError(t *testing.T) {
	t.Run("strict user limit denies without tracking key", func(t *testing.T) {
		cache := &combinedUserAPIKeyConcurrencyCacheForTest{combinedAcquireResult: false}
		result, err := NewConcurrencyService(cache).AcquireUserSlotForAPIKey(context.Background(), 202, 1, 77)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.False(t, result.Acquired)
		require.Nil(t, result.ReleaseFunc)
		require.Equal(t, 1, cache.combinedAcquireCalls)
		require.Zero(t, cache.combinedReleaseCalls)
		require.Empty(t, cache.trackedAPIKeyIDs)
	})

	t.Run("redis error fails closed", func(t *testing.T) {
		cache := &combinedUserAPIKeyConcurrencyCacheForTest{combinedAcquireErr: errors.New("redis down")}
		result, err := NewConcurrencyService(cache).AcquireUserSlotForAPIKey(context.Background(), 202, 1, 77)
		require.Error(t, err)
		require.Nil(t, result)
		require.Equal(t, 1, cache.combinedAcquireCalls)
		require.Zero(t, cache.combinedReleaseCalls)
	})
}

func TestAcquireUserSlotForAPIKey_FallbacksPreserveSemantics(t *testing.T) {
	t.Run("unsupported combined cache uses strict user slot then key stats", func(t *testing.T) {
		cache := &stubConcurrencyCacheForTest{acquireResult: true}
		result, err := NewConcurrencyService(cache).AcquireUserSlotForAPIKey(context.Background(), 202, 3, 77)
		require.NoError(t, err)
		require.True(t, result.Acquired)
		require.Equal(t, []int64{77}, cache.trackedAPIKeyIDs)

		result.ReleaseFunc()
		require.Equal(t, []int64{77}, cache.releasedAPIKeyIDs)
	})

	t.Run("missing key keeps user enforcement without key stats", func(t *testing.T) {
		cache := &combinedUserAPIKeyConcurrencyCacheForTest{}
		cache.acquireResult = true
		result, err := NewConcurrencyService(cache).AcquireUserSlotForAPIKey(context.Background(), 202, 3, 0)
		require.NoError(t, err)
		require.True(t, result.Acquired)
		require.Zero(t, cache.combinedAcquireCalls)
		require.Empty(t, cache.trackedAPIKeyIDs)
	})

	t.Run("unlimited user still records key stats", func(t *testing.T) {
		cache := &combinedUserAPIKeyConcurrencyCacheForTest{}
		result, err := NewConcurrencyService(cache).AcquireUserSlotForAPIKey(context.Background(), 202, 0, 77)
		require.NoError(t, err)
		require.True(t, result.Acquired)
		require.Zero(t, cache.combinedAcquireCalls)
		require.Equal(t, []int64{77}, cache.trackedAPIKeyIDs)

		result.ReleaseFunc()
		require.Equal(t, []int64{77}, cache.releasedAPIKeyIDs)
	})
}

func TestTrackAPIKeySlot_ReleaseDecrements(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{}
	svc := NewConcurrencyService(cache)

	release := svc.TrackAPIKeySlot(context.Background(), 88)
	require.NotNil(t, release)
	require.Equal(t, []int64{88}, cache.trackedAPIKeyIDs)
	require.Len(t, cache.trackedAPIKeyRequestIDs, 1)
	require.NotEmpty(t, cache.trackedAPIKeyRequestIDs[0])

	release()

	require.Equal(t, []int64{88}, cache.releasedAPIKeyIDs)
	require.Equal(t, cache.trackedAPIKeyRequestIDs, cache.releasedAPIKeyRequestIDs)
}

func TestTrackAPIKeySlotSupportsControlPlaneNegativeNamespace(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{}
	svc := NewConcurrencyService(cache)

	release := svc.TrackAPIKeySlot(context.Background(), -88)
	require.Equal(t, []int64{-88}, cache.trackedAPIKeyIDs)
	release()
	require.Equal(t, []int64{-88}, cache.releasedAPIKeyIDs)
}

func TestTrackAPIKeySlot_FailOpen(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{apiKeyTrackErr: errors.New("redis down")}
	svc := NewConcurrencyService(cache)

	release := svc.TrackAPIKeySlot(context.Background(), 88)
	require.NotNil(t, release)
	require.Equal(t, []int64{88}, cache.trackedAPIKeyIDs)

	require.NotPanics(t, release)
	require.Empty(t, cache.releasedAPIKeyIDs)
}

func TestGetAPIKeyConcurrencyBatch_Fallbacks(t *testing.T) {
	t.Run("nil cache returns zeroes", func(t *testing.T) {
		svc := &ConcurrencyService{cache: nil}

		counts, err := svc.GetAPIKeyConcurrencyBatch(context.Background(), []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, map[int64]int{1: 0, 2: 0}, counts)
	})

	t.Run("redis error returns zeroes", func(t *testing.T) {
		cache := &stubConcurrencyCacheForTest{apiKeyConcurrencyErr: errors.New("redis down")}
		svc := NewConcurrencyService(cache)

		counts, err := svc.GetAPIKeyConcurrencyBatch(context.Background(), []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, map[int64]int{1: 0, 2: 0}, counts)
	})

	t.Run("success returns counts", func(t *testing.T) {
		cache := &stubConcurrencyCacheForTest{apiKeyConcurrency: map[int64]int{1: 3, 2: 0}}
		svc := NewConcurrencyService(cache)

		counts, err := svc.GetAPIKeyConcurrencyBatch(context.Background(), []int64{1, 2})
		require.NoError(t, err)
		require.Equal(t, map[int64]int{1: 3, 2: 0}, counts)
	})
}

func TestAcquireOpenAIWSIngressLease(t *testing.T) {
	t.Run("zero value release is safe", func(t *testing.T) {
		var lease OpenAIWSIngressLease
		require.NotPanics(t, lease.Release)
	})

	t.Run("disabled", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{}
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(nil, 1, 0)
		require.NoError(t, err)
		require.True(t, acquired)
		require.Nil(t, lease)
		require.Zero(t, cache.acquireIngressCalls)
	})

	t.Run("unsupported cache fails closed", func(t *testing.T) {
		lease, acquired, err := NewConcurrencyService(&stubConcurrencyCacheForTest{}).AcquireOpenAIWSIngressLease(context.Background(), 1, 1)
		require.Error(t, err)
		require.False(t, acquired)
		require.Nil(t, lease)
	})

	t.Run("redis error fails closed", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{acquireIngressErr: errors.New("redis unavailable")}
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(context.Background(), 1, 1)
		require.Error(t, err)
		require.False(t, acquired)
		require.Nil(t, lease)
	})

	t.Run("capacity rejected", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{acquireIngressResult: false}
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(context.Background(), 1, 1)
		require.NoError(t, err)
		require.False(t, acquired)
		require.Nil(t, lease)
	})

	t.Run("release returns capacity", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{acquireIngressResult: true, refreshIngressResult: true}
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(nil, 1, 1)
		require.NoError(t, err)
		require.True(t, acquired)
		require.NotNil(t, lease)
		lease.Release()
		lease.Release()
		require.Equal(t, 1, cache.releaseIngressCalls)
	})

	t.Run("negative control-plane namespace is supported", func(t *testing.T) {
		const remoteKeyID = int64(-90210)
		cache := &ingressLeaseCacheForTest{
			acquireIngressResult: true,
			refreshIngressResult: true,
			acquireIngressFn: func(_ context.Context, apiKeyID int64, _ int, _ string) (bool, error) {
				require.Equal(t, remoteKeyID, apiKeyID)
				return true, nil
			},
		}
		lease, acquired, err := NewConcurrencyService(cache).AcquireOpenAIWSIngressLease(context.Background(), remoteKeyID, 1)
		require.NoError(t, err)
		require.True(t, acquired)
		require.NotNil(t, lease)
		lease.Release()
		require.Equal(t, 1, cache.releaseIngressCalls)
	})
}

func TestOpenAIWSIngressLeaseRefreshLoss(t *testing.T) {
	t.Run("missing lease is lost immediately", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{refreshIngressResult: false}
		lease := &OpenAIWSIngressLease{cache: cache, apiKeyID: 1, leaseID: "missing"}
		_, lost := lease.refresh(time.Now())
		require.True(t, lost)
		require.Equal(t, 1, cache.refreshIngressCalls)
	})

	t.Run("persistent redis errors lose lease after ttl", func(t *testing.T) {
		cache := &ingressLeaseCacheForTest{refreshIngressErr: errors.New("redis unavailable")}
		lease := &OpenAIWSIngressLease{cache: cache, apiKeyID: 1, leaseID: "unconfirmed"}
		_, lost := lease.refresh(time.Now().Add(-openAIWSIngressLeaseTTL))
		require.True(t, lost)
		require.Equal(t, 1, cache.refreshIngressCalls)
	})
}

func TestOpenAIWSIngressLeaseReleaseWaitsForInFlightRefresh(t *testing.T) {
	refreshStarted := make(chan struct{})
	allowRefresh := make(chan struct{})
	cache := &ingressLeaseCacheForTest{
		refreshIngressFn: func(context.Context, int64, string) (bool, error) {
			close(refreshStarted)
			<-allowRefresh
			return true, nil
		},
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	lease := &OpenAIWSIngressLease{
		ctx:         ctx,
		cancel:      cancel,
		cache:       cache,
		apiKeyID:    1,
		leaseID:     "in-flight-refresh",
		stopCh:      make(chan struct{}),
		refreshDone: make(chan struct{}),
	}
	go func() {
		defer close(lease.refreshDone)
		_, _ = lease.refresh(time.Now())
	}()
	<-refreshStarted

	released := make(chan struct{})
	go func() {
		lease.Release()
		close(released)
	}()

	select {
	case <-released:
		t.Fatal("release returned before the in-flight refresh completed")
	case <-time.After(20 * time.Millisecond):
	}
	require.Zero(t, cache.releaseIngressCalls)

	close(allowRefresh)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("release did not complete after the refresh returned")
	}
	require.Equal(t, 1, cache.releaseIngressCalls)
}

func TestGenerateRequestID_UsesStablePrefixAndMonotonicCounter(t *testing.T) {
	id1 := generateRequestID()
	id2 := generateRequestID()
	require.NotEmpty(t, id1)
	require.NotEmpty(t, id2)

	p1 := strings.Split(id1, "-")
	p2 := strings.Split(id2, "-")
	require.Len(t, p1, 2)
	require.Len(t, p2, 2)
	require.Equal(t, p1[0], p2[0], "同一进程前缀应保持一致")

	n1, err := strconv.ParseUint(p1[1], 36, 64)
	require.NoError(t, err)
	n2, err := strconv.ParseUint(p2[1], 36, 64)
	require.NoError(t, err)
	require.Equal(t, n1+1, n2, "计数器应单调递增")
}

func TestGetAccountsLoadBatch_ReturnsCorrectData(t *testing.T) {
	expected := map[int64]*AccountLoadInfo{
		1: {AccountID: 1, CurrentConcurrency: 3, WaitingCount: 0, LoadRate: 60},
		2: {AccountID: 2, CurrentConcurrency: 5, WaitingCount: 2, LoadRate: 100},
	}
	cache := &stubConcurrencyCacheForTest{loadBatch: expected}
	svc := NewConcurrencyService(cache)

	accounts := []AccountWithConcurrency{
		{ID: 1, MaxConcurrency: 5},
		{ID: 2, MaxConcurrency: 5},
	}
	result, err := svc.GetAccountsLoadBatch(context.Background(), accounts)
	require.NoError(t, err)
	require.Equal(t, expected, result)
}

func TestGetAccountsLoadBatch_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	result, err := svc.GetAccountsLoadBatch(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestGetAccountsLoadBatch_UsesShortTTLCache(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{
		loadBatch: map[int64]*AccountLoadInfo{
			1: {AccountID: 1, CurrentConcurrency: 1, LoadRate: 20},
		},
	}
	svc := NewConcurrencyService(cache)
	svc.SetAccountLoadBatchCacheTTL(time.Second)

	accounts := []AccountWithConcurrency{{ID: 1, MaxConcurrency: 5}}
	first, err := svc.GetAccountsLoadBatch(context.Background(), accounts)
	require.NoError(t, err)
	require.Equal(t, 1, first[int64(1)].CurrentConcurrency)

	cache.loadBatch[1] = &AccountLoadInfo{AccountID: 1, CurrentConcurrency: 4, LoadRate: 80}
	second, err := svc.GetAccountsLoadBatch(context.Background(), accounts)
	require.NoError(t, err)
	require.Equal(t, 1, second[int64(1)].CurrentConcurrency)
	require.Equal(t, int64(1), cache.loadBatchCalls.Load())
}

func TestGetAccountsLoadBatchFresh_BypassesShortTTLCache(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{
		loadBatch: map[int64]*AccountLoadInfo{
			1: {AccountID: 1, CurrentConcurrency: 1, LoadRate: 20},
		},
	}
	svc := NewConcurrencyService(cache)
	svc.SetAccountLoadBatchCacheTTL(time.Second)

	accounts := []AccountWithConcurrency{{ID: 1, MaxConcurrency: 5}}
	_, err := svc.GetAccountsLoadBatch(context.Background(), accounts)
	require.NoError(t, err)

	cache.loadBatch[1] = &AccountLoadInfo{AccountID: 1, CurrentConcurrency: 4, LoadRate: 80}
	fresh, err := svc.GetAccountsLoadBatchFresh(context.Background(), accounts)
	require.NoError(t, err)
	require.Equal(t, 4, fresh[int64(1)].CurrentConcurrency)
	require.Equal(t, int64(2), cache.loadBatchCalls.Load())
}

func TestIncrementWaitCount_Success(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: true}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestIncrementWaitCount_QueueFull(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitAllowed: false}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestIncrementWaitCount_FailOpen(t *testing.T) {
	// Redis 错误时应 fail-open（允许请求通过）
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis timeout")}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err, "Redis 错误不应传播")
	require.True(t, allowed, "Redis 错误时应 fail-open")
}

func TestIncrementWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	allowed, err := svc.IncrementWaitCount(context.Background(), 1, 25)
	require.NoError(t, err)
	require.True(t, allowed, "nil cache 应 fail-open")
}

func TestCalculateMaxWait(t *testing.T) {
	tests := []struct {
		concurrency int
		expected    int
	}{
		{5, 5},
		{1, 1},
		{0, 1},
		{-1, 1},
		{10, 10},
	}
	for _, tt := range tests {
		result := CalculateMaxWait(tt.concurrency)
		require.Equal(t, tt.expected, result, "CalculateMaxWait(%d)", tt.concurrency)
	}
}

func TestGetAccountWaitingCount(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitCount: 5}
	svc := NewConcurrencyService(cache)

	count, err := svc.GetAccountWaitingCount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 5, count)
}

func TestGetAccountWaitingCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	count, err := svc.GetAccountWaitingCount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestGetAccountConcurrencyBatch(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{concurrency: 3}
	svc := NewConcurrencyService(cache)

	result, err := svc.GetAccountConcurrencyBatch(context.Background(), []int64{1, 2, 3})
	require.NoError(t, err)
	require.Len(t, result, 3)
	for _, id := range []int64{1, 2, 3} {
		require.Equal(t, 3, result[id])
	}
}

func TestIncrementAccountWaitCount_FailOpen(t *testing.T) {
	cache := &stubConcurrencyCacheForTest{waitErr: errors.New("redis error")}
	svc := NewConcurrencyService(cache)

	allowed, err := svc.IncrementAccountWaitCount(context.Background(), 1, 10)
	require.NoError(t, err, "Redis 错误不应传播")
	require.True(t, allowed, "Redis 错误时应 fail-open")
}

func TestIncrementAccountWaitCount_NilCache(t *testing.T) {
	svc := &ConcurrencyService{cache: nil}

	allowed, err := svc.IncrementAccountWaitCount(context.Background(), 1, 10)
	require.NoError(t, err)
	require.True(t, allowed)
}
