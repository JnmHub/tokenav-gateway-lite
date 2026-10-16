//go:build unit

package service

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type bulkEventAccountRepo struct {
	AccountRepository
	accounts []*Account
}

func (r *bulkEventAccountRepo) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return append([]*Account(nil), r.accounts...), nil
}

func (r *bulkEventAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, nil
}

func (r *bulkEventAccountRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	return nil, nil
}

func (r *bulkEventAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}

func (r *bulkEventAccountRepo) ListSchedulableByPlatforms(context.Context, []string) ([]Account, error) {
	return nil, nil
}

type bulkEventSnapshotCache struct {
	*lifecycleTestCache

	accountMu        sync.Mutex
	setAccountIDs    []int64
	setAccounts      []Account
	deleteAccountIDs []int64
}

func newBulkEventSnapshotCache() *bulkEventSnapshotCache {
	return &bulkEventSnapshotCache{lifecycleTestCache: newLifecycleTestCache()}
}

func (c *bulkEventSnapshotCache) SetAccount(_ context.Context, account *Account) error {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	c.setAccountIDs = append(c.setAccountIDs, account.ID)
	copy := *account
	copy.Extra = shallowCopyMap(account.Extra)
	c.setAccounts = append(c.setAccounts, copy)
	return nil
}

func (c *bulkEventSnapshotCache) DeleteAccount(_ context.Context, accountID int64) error {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	c.deleteAccountIDs = append(c.deleteAccountIDs, accountID)
	return nil
}

func (c *bulkEventSnapshotCache) accountWrites() (set []int64, deleted []int64) {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	return append([]int64(nil), c.setAccountIDs...), append([]int64(nil), c.deleteAccountIDs...)
}

func (c *bulkEventSnapshotCache) lastAccountWrite() *Account {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	if len(c.setAccounts) == 0 {
		return nil
	}
	copy := c.setAccounts[len(c.setAccounts)-1]
	return &copy
}

func (c *bulkEventSnapshotCache) writtenBuckets() []SchedulerBucket {
	c.mu.Lock()
	defer c.mu.Unlock()
	buckets := make([]SchedulerBucket, 0, len(c.writes))
	for bucket := range c.writes {
		buckets = append(buckets, bucket)
	}
	return buckets
}

func newBulkEventTestService(cache SchedulerCache, accounts AccountRepository, runMode string) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, accounts, nil, &config.Config{RunMode: runMode})
}

func bulkEventPayload(accountIDs []int64, groupIDs []int64) map[string]any {
	accountValues := make([]any, 0, len(accountIDs))
	for _, id := range accountIDs {
		accountValues = append(accountValues, id)
	}
	groupValues := make([]any, 0, len(groupIDs))
	for _, id := range groupIDs {
		groupValues = append(groupValues, id)
	}
	return map[string]any{"account_ids": accountValues, "group_ids": groupValues}
}

func schedulerBucketsForTest(groupIDs []int64, platforms ...string) []SchedulerBucket {
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*len(platforms)*3)
	for _, platform := range platforms {
		for _, groupID := range groupIDs {
			buckets = append(buckets,
				SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
				SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
			)
			if platform == PlatformAnthropic || platform == PlatformGemini {
				buckets = append(buckets, SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeMixed})
			}
		}
	}
	return buckets
}

func TestSchedulerBulkAccountEventScopesRebuildToFreshPlatform(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{{ID: 1, Platform: PlatformOpenAI, GroupIDs: []int64{12}}}}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{1}, []int64{11}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{11, 12}, PlatformOpenAI), cache.writtenBuckets())
	set, deleted := cache.accountWrites()
	require.Equal(t, []int64{1}, set)
	require.Empty(t, deleted)
}

func TestSchedulerBulkAccountEventDoesNotCrossCurrentGroupsBetweenPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{
		{ID: 9, Platform: PlatformOpenAI, GroupIDs: []int64{61}},
		{ID: 10, Platform: PlatformGrok, GroupIDs: []int64{62}},
	}}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{9, 10}, []int64{63}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	want := append(schedulerBucketsForTest([]int64{61, 63}, PlatformOpenAI), schedulerBucketsForTest([]int64{62, 63}, PlatformGrok)...)
	require.ElementsMatch(t, want, cache.writtenBuckets())
}

func TestSchedulerBulkAccountEventConservativelyExpandsAntigravityPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{{ID: 2, Platform: PlatformAntigravity, GroupIDs: []int64{22}}}}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{2}, []int64{21}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t,
		schedulerBucketsForTest([]int64{21, 22}, PlatformAnthropic, PlatformGemini, PlatformAntigravity),
		cache.writtenBuckets(),
	)
}

func TestSchedulerBulkAccountEventMissingAccountFallsBackToAllPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{{ID: 3, Platform: PlatformOpenAI, GroupIDs: []int64{32}}}}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{3, 4}, []int64{31}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	platforms := schedulerSnapshotPlatforms()
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{31, 32}, platforms[:]...), cache.writtenBuckets())
	set, deleted := cache.accountWrites()
	require.Equal(t, []int64{3}, set)
	require.Equal(t, []int64{4}, deleted)
}

func TestSchedulerBulkAccountEventUnknownPlatformFallsBackToAllPlatforms(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{{ID: 5, Platform: "unknown", GroupIDs: []int64{42}}}}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{5}, []int64{41}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	platforms := schedulerSnapshotPlatforms()
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{41, 42}, platforms[:]...), cache.writtenBuckets())
}

func TestSchedulerBulkAccountEventHundredAccountsStayOnOnePlatform(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	accounts := make([]*Account, 0, 100)
	ids := make([]int64, 0, 100)
	for id := int64(1); id <= 100; id++ {
		ids = append(ids, id)
		accounts = append(accounts, &Account{ID: id, Platform: PlatformOpenAI, GroupIDs: []int64{77}})
	}
	repo := &bulkEventAccountRepo{accounts: accounts}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload(ids, nil), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{77}, PlatformOpenAI), cache.writtenBuckets())
	set, deleted := cache.accountWrites()
	require.Len(t, set, 100)
	require.Empty(t, deleted)
}

func TestSchedulerBulkAccountEventUsesGroupZeroInSimpleMode(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{{ID: 11, Platform: PlatformOpenAI, GroupIDs: []int64{71}}}}
	svc := newBulkEventTestService(cache, repo, config.RunModeSimple)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{11}, []int64{72}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	require.ElementsMatch(t, schedulerBucketsForTest([]int64{0}, PlatformOpenAI), cache.writtenBuckets())
}

func TestSchedulerBulkAccountEventKeepsGrokMediaEligibilityEvidence(t *testing.T) {
	cache := newBulkEventSnapshotCache()
	repo := &bulkEventAccountRepo{accounts: []*Account{{
		ID: 12, Platform: PlatformGrok, GroupIDs: []int64{73},
		Extra: map[string]any{
			GrokMediaEligibleExtraKey: false,
			GrokMediaEligibilitySnapshotExtraKey: map[string]any{
				"eligible": false, "weekly_status_code": 403,
			},
		},
	}}}
	svc := newBulkEventTestService(cache, repo, config.RunModeStandard)

	err := svc.handleBulkAccountEvent(context.Background(), bulkEventPayload([]int64{12}, []int64{73}), make(map[batchSeenKey]struct{}))

	require.NoError(t, err)
	written := cache.lastAccountWrite()
	require.NotNil(t, written)
	require.Equal(t, false, written.Extra[GrokMediaEligibleExtraKey])
	require.NotNil(t, written.Extra[GrokMediaEligibilitySnapshotExtraKey])
}
