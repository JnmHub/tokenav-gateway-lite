package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type lifecycleTestCache struct {
	SchedulerCache

	mu       sync.Mutex
	next     int64
	locks    map[SchedulerBucket]int
	writes   map[SchedulerBucket][][]Account
	retired  map[SchedulerBucket]int
	buckets  []SchedulerBucket
	reopened []SchedulerBucket
}

func newLifecycleTestCache(buckets ...SchedulerBucket) *lifecycleTestCache {
	return &lifecycleTestCache{
		locks:   make(map[SchedulerBucket]int),
		writes:  make(map[SchedulerBucket][][]Account),
		retired: make(map[SchedulerBucket]int),
		buckets: buckets,
	}
}

func (c *lifecycleTestCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

func (c *lifecycleTestCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: c.next}, nil
}

func (c *lifecycleTestCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, accounts []Account) error {
	token, err := c.CaptureBucketWriteToken(context.Background(), bucket)
	if err != nil {
		return err
	}
	return c.SetSnapshotWithToken(context.Background(), bucket, token, accounts)
}

func (c *lifecycleTestCache) SetSnapshotWithToken(_ context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) error {
	if !token.ValidFor(bucket) {
		return ErrSchedulerBucketWriteFenced
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes[bucket] = append(c.writes[bucket], append([]Account(nil), accounts...))
	return nil
}

func (c *lifecycleTestCache) RetireBucket(_ context.Context, bucket SchedulerBucket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retired[bucket]++
	return nil
}

func (c *lifecycleTestCache) ReopenBucket(ctx context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	c.mu.Lock()
	c.reopened = append(c.reopened, bucket)
	c.mu.Unlock()
	return c.CaptureBucketWriteToken(ctx, bucket)
}

func (c *lifecycleTestCache) TryAcquireGroupLifecycleLease(_ context.Context, groupID int64, _ time.Duration) (SchedulerGroupLifecycleLease, bool, error) {
	return SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: "owner"}, true, nil
}

func (c *lifecycleTestCache) ReleaseGroupLifecycleLease(context.Context, SchedulerGroupLifecycleLease) error {
	return nil
}

func (c *lifecycleTestCache) GetAccount(context.Context, int64) (*Account, error) { return nil, nil }
func (c *lifecycleTestCache) SetAccount(context.Context, *Account) error          { return nil }
func (c *lifecycleTestCache) DeleteAccount(context.Context, int64) error          { return nil }
func (c *lifecycleTestCache) UpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (c *lifecycleTestCache) TryLockBucket(_ context.Context, bucket SchedulerBucket, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locks[bucket]++
	return true, nil
}
func (c *lifecycleTestCache) UnlockBucket(context.Context, SchedulerBucket) error { return nil }
func (c *lifecycleTestCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return append([]SchedulerBucket(nil), c.buckets...), nil
}
func (c *lifecycleTestCache) GetOutboxWatermark(context.Context) (int64, error) { return 0, nil }
func (c *lifecycleTestCache) SetOutboxWatermark(context.Context, int64) error   { return nil }

func (c *lifecycleTestCache) writeCount(bucket SchedulerBucket) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes[bucket])
}

func (c *lifecycleTestCache) retireCount(bucket SchedulerBucket) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.retired[bucket]
}

type lifecycleQueryRepo struct {
	AccountRepository

	mu    sync.Mutex
	calls map[schedulerAccountQueryKey]int
}

func newLifecycleQueryRepo() *lifecycleQueryRepo {
	return &lifecycleQueryRepo{calls: make(map[schedulerAccountQueryKey]int)}
}

func (r *lifecycleQueryRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	key := schedulerAccountQueryKey{groupID: groupID, platform: platform}
	r.mu.Lock()
	r.calls[key]++
	r.mu.Unlock()
	return []Account{{ID: 7001, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

func (r *lifecycleQueryRepo) ListSchedulableByPlatform(_ context.Context, platform string) ([]Account, error) {
	key := schedulerAccountQueryKey{platform: platform}
	r.mu.Lock()
	r.calls[key]++
	r.mu.Unlock()
	return []Account{{ID: 7002, Platform: platform, Status: StatusActive, Schedulable: true}}, nil
}

func (r *lifecycleQueryRepo) callCount(key schedulerAccountQueryKey) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

type lifecycleGroupRepo struct {
	GroupRepository
	group *Group
	err   error
}

func (r lifecycleGroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	return r.GetByIDLite(ctx, id)
}

func (r lifecycleGroupRepo) GetByIDLite(context.Context, int64) (*Group, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.group == nil {
		return nil, ErrGroupNotFound
	}
	clone := *r.group
	return &clone, nil
}

func TestSchedulerRebuildBatchReusesSingleForcedAccountQuery(t *testing.T) {
	groupID := int64(201)
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newLifecycleTestCache()
	repo := newLifecycleQueryRepo()
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{})

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "test"))

	require.Equal(t, 1, repo.callCount(schedulerAccountQueryKey{groupID: groupID, platform: PlatformOpenAI}))
	require.Equal(t, 1, cache.writeCount(single))
	require.Equal(t, 1, cache.writeCount(forced))
}

func TestSchedulerGroupEventInactiveRetiresCanonicalAndHistoricalBuckets(t *testing.T) {
	groupID := int64(55)
	historical := SchedulerBucket{GroupID: groupID, Platform: "legacy", Mode: "obsolete"}
	cache := newLifecycleTestCache(historical)
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		newLifecycleQueryRepo(),
		lifecycleGroupRepo{group: &Group{ID: groupID, Status: StatusDisabled, Hydrated: true}},
		&config.Config{},
	)
	seen := make(map[batchSeenKey]struct{})

	require.NoError(t, svc.handleGroupEvent(context.Background(), &groupID, nil, seen))

	require.Equal(t, 1, cache.retireCount(historical))
	for _, bucket := range schedulerBucketsForGroup(groupID) {
		require.Equal(t, 1, cache.retireCount(bucket), bucket.String())
	}
	_, ok := seen[batchSeenKey{groupID: groupID, lifecycle: true}]
	require.True(t, ok)
}
