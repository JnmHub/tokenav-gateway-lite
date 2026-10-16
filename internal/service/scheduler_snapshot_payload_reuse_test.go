//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type payloadReuseLifecycleCache struct {
	*lifecycleTestCache

	reuseMu     sync.Mutex
	fullCalls   map[SchedulerBucket]int
	idOnlyCalls map[SchedulerBucket]int
	fullError   map[SchedulerBucket]error
	idOnlyError map[SchedulerBucket]error
	lockBusy    map[SchedulerBucket]bool
	returnEmpty bool
}

func newPayloadReuseLifecycleCache() *payloadReuseLifecycleCache {
	return &payloadReuseLifecycleCache{
		lifecycleTestCache: newLifecycleTestCache(),
		fullCalls:          make(map[SchedulerBucket]int),
		idOnlyCalls:        make(map[SchedulerBucket]int),
		fullError:          make(map[SchedulerBucket]error),
		idOnlyError:        make(map[SchedulerBucket]error),
		lockBusy:           make(map[SchedulerBucket]bool),
	}
}

func (c *payloadReuseLifecycleCache) SetSnapshotAndReturnAccountIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) ([]int64, error) {
	c.reuseMu.Lock()
	c.fullCalls[bucket]++
	err := c.fullError[bucket]
	returnEmpty := c.returnEmpty
	c.reuseMu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := c.lifecycleTestCache.SetSnapshotWithToken(ctx, bucket, token, accounts); err != nil {
		return nil, err
	}
	if returnEmpty {
		return []int64{}, nil
	}
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids, nil
}

func (c *payloadReuseLifecycleCache) SetSnapshotByAccountIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accountIDs []int64) error {
	c.reuseMu.Lock()
	c.idOnlyCalls[bucket]++
	err := c.idOnlyError[bucket]
	c.reuseMu.Unlock()
	if err != nil {
		return err
	}
	accounts := make([]Account, 0, len(accountIDs))
	for _, id := range accountIDs {
		accounts = append(accounts, Account{ID: id})
	}
	return c.lifecycleTestCache.SetSnapshotWithToken(ctx, bucket, token, accounts)
}

func (c *payloadReuseLifecycleCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	c.reuseMu.Lock()
	busy := c.lockBusy[bucket]
	c.reuseMu.Unlock()
	if busy {
		return false, nil
	}
	return c.lifecycleTestCache.TryLockBucket(ctx, bucket, ttl)
}

func (c *payloadReuseLifecycleCache) reuseCounts(bucket SchedulerBucket) (full, idOnly int) {
	c.reuseMu.Lock()
	defer c.reuseMu.Unlock()
	return c.fullCalls[bucket], c.idOnlyCalls[bucket]
}

func newPayloadReuseTestService(cache SchedulerCache, repo AccountRepository, runMode string) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, repo, nil, &config.Config{RunMode: runMode})
}

func (r *lifecycleQueryRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	accounts := make([]Account, 0, len(platforms))
	for _, platform := range platforms {
		accounts = append(accounts, Account{ID: groupID*100 + int64(len(accounts)+1), Platform: platform, Status: StatusActive, Schedulable: true})
	}
	return accounts, nil
}

func TestSchedulerRebuildBatchReusesAccountPayloadForSingleForced(t *testing.T) {
	const groupID int64 = 211
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newPayloadReuseLifecycleCache()
	repo := newLifecycleQueryRepo()
	svc := newPayloadReuseTestService(cache, repo, config.RunModeStandard)

	for run := 1; run <= 2; run++ {
		require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "reuse"))
		full, idOnly := cache.reuseCounts(single)
		require.Equal(t, run, full)
		require.Zero(t, idOnly)
		full, idOnly = cache.reuseCounts(forced)
		require.Zero(t, full)
		require.Equal(t, run, idOnly)
	}
	require.Equal(t, 2, repo.callCount(schedulerAccountQueryKey{groupID: groupID, platform: PlatformOpenAI}), "账号载荷不得跨重建批次复用")
}

func TestSchedulerRebuildBatchDoesNotReuseAfterFirstWriterFailure(t *testing.T) {
	const groupID int64 = 212
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("snapshot write failed")
	cache := newPayloadReuseLifecycleCache()
	cache.fullError[single] = wantErr
	svc := newPayloadReuseTestService(cache, newLifecycleQueryRepo(), config.RunModeStandard)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "failure")

	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(single)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	require.Equal(t, 1, cache.writeCount(forced), "首次完整写失败后，后续桶必须走原完整写入")
}

func TestSchedulerRebuildBatchDoesNotReuseAfterLockBusy(t *testing.T) {
	const groupID int64 = 213
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newPayloadReuseLifecycleCache()
	cache.lockBusy[single] = true
	svc := newPayloadReuseTestService(cache, newLifecycleQueryRepo(), config.RunModeStandard)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "busy"))
	full, idOnly := cache.reuseCounts(single)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	require.Equal(t, 1, cache.writeCount(forced))
}

func TestSchedulerRebuildBatchKeepsMixedAndDifferentQueriesOnFullWrites(t *testing.T) {
	const groupID int64 = 214
	openAISingle := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	openAIForced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	anthropicSingle := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeSingle}
	anthropicMixed := SchedulerBucket{GroupID: groupID, Platform: PlatformAnthropic, Mode: SchedulerModeMixed}
	cache := newPayloadReuseLifecycleCache()
	svc := newPayloadReuseTestService(cache, newLifecycleQueryRepo(), config.RunModeStandard)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{openAISingle, openAIForced, anthropicSingle, anthropicMixed}, "scope"))
	full, idOnly := cache.reuseCounts(openAISingle)
	require.Equal(t, 1, full)
	require.Zero(t, idOnly)
	full, idOnly = cache.reuseCounts(openAIForced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly)
	full, idOnly = cache.reuseCounts(anthropicSingle)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	require.Equal(t, 1, cache.writeCount(anthropicSingle))
	full, idOnly = cache.reuseCounts(anthropicMixed)
	require.Zero(t, full)
	require.Zero(t, idOnly)
	require.Equal(t, 1, cache.writeCount(anthropicMixed))
}

func TestSchedulerRebuildBatchPropagatesAccountIDOnlyWriteFailure(t *testing.T) {
	const groupID int64 = 215
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	wantErr := errors.New("id-only write failed")
	cache := newPayloadReuseLifecycleCache()
	cache.idOnlyError[forced] = wantErr
	svc := newPayloadReuseTestService(cache, newLifecycleQueryRepo(), config.RunModeStandard)

	err := svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "id-error")

	require.ErrorIs(t, err, wantErr)
	full, idOnly := cache.reuseCounts(forced)
	require.Zero(t, full, "ID-only 写失败不得静默回退为完整写")
	require.Equal(t, 1, idOnly)
}

func TestSchedulerRebuildBatchReusesSuccessfulEmptyAccountIDs(t *testing.T) {
	const groupID int64 = 217
	single := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	forced := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeForced}
	cache := newPayloadReuseLifecycleCache()
	cache.returnEmpty = true
	svc := newPayloadReuseTestService(cache, newLifecycleQueryRepo(), config.RunModeStandard)

	require.NoError(t, svc.rebuildBuckets(context.Background(), []SchedulerBucket{single, forced}, "empty"))
	full, idOnly := cache.reuseCounts(forced)
	require.Zero(t, full)
	require.Equal(t, 1, idOnly, "成功缓存的空 ID 集也必须按 map presence 复用")
	require.Equal(t, 1, cache.writeCount(forced))
}

func TestSchedulerAccountQueryCacheReleasesSnapshotAccountIDs(t *testing.T) {
	single := schedulerBucketWriteTask{bucket: SchedulerBucket{GroupID: 218, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}}
	forced := schedulerBucketWriteTask{bucket: SchedulerBucket{GroupID: 218, Platform: PlatformOpenAI, Mode: SchedulerModeForced}}
	queries := newSchedulerAccountQueryCache([]schedulerBucketWriteTask{single, forced})
	key, ok := schedulerAccountQueryKeyForBucket(single.bucket)
	require.True(t, ok)
	queries.snapshotAccountIDs[key] = []int64{1, 2}

	queries.release(single.bucket)
	require.Contains(t, queries.snapshotAccountIDs, key)
	queries.release(forced.bucket)
	require.NotContains(t, queries.snapshotAccountIDs, key)
	require.Empty(t, queries.remaining)
	require.Empty(t, queries.accounts)
}
