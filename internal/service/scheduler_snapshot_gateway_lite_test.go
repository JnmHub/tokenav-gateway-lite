package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type gatewayLiteMissSchedulerCache struct{}

func (gatewayLiteMissSchedulerCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}
func (gatewayLiteMissSchedulerCache) SetSnapshot(context.Context, SchedulerBucket, []Account) error {
	return nil
}
func (gatewayLiteMissSchedulerCache) GetAccount(context.Context, int64) (*Account, error) {
	return nil, nil
}
func (gatewayLiteMissSchedulerCache) SetAccount(context.Context, *Account) error {
	return nil
}
func (gatewayLiteMissSchedulerCache) DeleteAccount(context.Context, int64) error {
	return nil
}
func (gatewayLiteMissSchedulerCache) UpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (gatewayLiteMissSchedulerCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return false, nil
}
func (gatewayLiteMissSchedulerCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}
func (gatewayLiteMissSchedulerCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return nil, nil
}
func (gatewayLiteMissSchedulerCache) GetOutboxWatermark(context.Context) (int64, error) {
	return 0, nil
}
func (gatewayLiteMissSchedulerCache) SetOutboxWatermark(context.Context, int64) error {
	return nil
}

type gatewayLiteRegisteredEmptySchedulerCache struct {
	gatewayLiteMissSchedulerCache
	bucket SchedulerBucket
}

func (c gatewayLiteRegisteredEmptySchedulerCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return []SchedulerBucket{c.bucket}, nil
}

type gatewayLiteRebuildSchedulerCache struct {
	mu        sync.Mutex
	snapshots map[SchedulerBucket][]Account
	groups    map[int64]*Group
	written   chan SchedulerBucket
}

type gatewayLiteRebuildAccountRepo struct {
	stubOpenAIAccountRepo
}

func (r gatewayLiteRebuildAccountRepo) ListSchedulableByPlatforms(ctx context.Context, platforms []string) ([]Account, error) {
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	out := make([]Account, 0)
	for _, account := range r.accounts {
		if _, ok := platformSet[account.Platform]; ok && account.IsSchedulable() {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r gatewayLiteRebuildAccountRepo) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]Account, error) {
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	out := make([]Account, 0)
	for _, account := range r.accounts {
		if _, ok := platformSet[account.Platform]; ok && account.IsSchedulable() && len(account.AccountGroups) == 0 {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r gatewayLiteRebuildAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableUngroupedByPlatforms(ctx, []string{platform})
}

func (r gatewayLiteRebuildAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	out := make([]Account, 0)
	for _, account := range r.accounts {
		if account.Platform != platform || !account.IsSchedulable() {
			continue
		}
		for _, accountGroup := range account.AccountGroups {
			if accountGroup.GroupID == groupID {
				out = append(out, account)
				break
			}
		}
	}
	return out, nil
}

func (r gatewayLiteRebuildAccountRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error) {
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	out := make([]Account, 0)
	for _, account := range r.accounts {
		if _, ok := platformSet[account.Platform]; !ok || !account.IsSchedulable() {
			continue
		}
		for _, accountGroup := range account.AccountGroups {
			if accountGroup.GroupID == groupID {
				out = append(out, account)
				break
			}
		}
	}
	return out, nil
}

func newGatewayLiteRebuildSchedulerCache() *gatewayLiteRebuildSchedulerCache {
	return &gatewayLiteRebuildSchedulerCache{
		snapshots: make(map[SchedulerBucket][]Account),
		groups:    make(map[int64]*Group),
		written:   make(chan SchedulerBucket, 32),
	}
}

func (c *gatewayLiteRebuildSchedulerCache) GetSnapshot(_ context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	accounts, ok := c.snapshots[bucket]
	if !ok {
		return nil, false, nil
	}
	out := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		out = append(out, &account)
	}
	return out, true, nil
}
func (c *gatewayLiteRebuildSchedulerCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, accounts []Account) error {
	c.mu.Lock()
	c.snapshots[bucket] = append([]Account(nil), accounts...)
	c.mu.Unlock()
	select {
	case c.written <- bucket:
	default:
	}
	return nil
}
func (c *gatewayLiteRebuildSchedulerCache) GetAccount(context.Context, int64) (*Account, error) {
	return nil, nil
}
func (c *gatewayLiteRebuildSchedulerCache) SetAccount(context.Context, *Account) error {
	return nil
}
func (c *gatewayLiteRebuildSchedulerCache) DeleteAccount(context.Context, int64) error {
	return nil
}
func (c *gatewayLiteRebuildSchedulerCache) UpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (c *gatewayLiteRebuildSchedulerCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}
func (c *gatewayLiteRebuildSchedulerCache) UnlockBucket(context.Context, SchedulerBucket) error {
	return nil
}
func (c *gatewayLiteRebuildSchedulerCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return nil, nil
}
func (c *gatewayLiteRebuildSchedulerCache) GetOutboxWatermark(context.Context) (int64, error) {
	return 0, nil
}
func (c *gatewayLiteRebuildSchedulerCache) SetOutboxWatermark(context.Context, int64) error {
	return nil
}

func (c *gatewayLiteRebuildSchedulerCache) GetGroup(_ context.Context, groupID int64) (*Group, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	group := c.groups[groupID]
	if group == nil {
		return nil, nil
	}
	clone := *group
	return &clone, nil
}

func (c *gatewayLiteRebuildSchedulerCache) SetGroup(_ context.Context, group *Group) error {
	if group == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	clone := *group
	c.groups[group.ID] = &clone
	return nil
}

func (c *gatewayLiteRebuildSchedulerCache) DeleteGroup(_ context.Context, groupID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.groups, groupID)
	return nil
}

type gatewayLiteSchedulerGroupRepo struct {
	GroupRepository
	group *Group
	err   error
	reads int
}

func (r gatewayLiteSchedulerGroupRepo) GetByID(context.Context, int64) (*Group, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.group == nil {
		return nil, ErrGroupNotFound
	}
	clone := *r.group
	return &clone, nil
}

func (r *gatewayLiteSchedulerGroupRepo) GetByIDLite(context.Context, int64) (*Group, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	if r.group == nil {
		return nil, nil
	}
	clone := *r.group
	return &clone, nil
}

func (c *gatewayLiteRebuildSchedulerCache) snapshot(bucket SchedulerBucket) []Account {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Account(nil), c.snapshots[bucket]...)
}

func TestSchedulerSnapshotGatewayLiteDoesNotFallbackToDB(t *testing.T) {
	svc := NewSchedulerSnapshotService(
		gatewayLiteMissSchedulerCache{},
		nil,
		nil,
		nil,
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.ErrorIs(t, err, ErrSchedulerCacheNotReady)
	require.Nil(t, accounts)

	account, err := svc.GetAccount(context.Background(), 1001)
	require.ErrorIs(t, err, ErrSchedulerCacheNotReady)
	require.Nil(t, account)

	group, err := svc.GetGroupByID(context.Background(), 7)
	require.NoError(t, err)
	require.Nil(t, group)
}

func TestSchedulerSnapshotGatewayLiteRepairsMissingGroupCacheFromDB(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	repo := &gatewayLiteSchedulerGroupRepo{group: &Group{ID: 7, Name: "recovered", Platform: PlatformOpenAI, Status: StatusActive}}
	svc := NewSchedulerSnapshotService(cache, nil, nil, repo, &config.Config{RunMode: config.RunModeGatewayLite})

	group, err := svc.GetGroupByID(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "recovered", group.Name)
	require.Equal(t, 1, repo.reads)

	groupAgain, err := svc.GetGroupByID(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "recovered", groupAgain.Name)
	require.Equal(t, 1, repo.reads, "repaired group must be served from hot/Redis cache")
	require.Contains(t, cache.groups, int64(7))
}

func TestSchedulerSnapshotGatewayLiteRegisteredEmptyBucketMeansNoAccounts(t *testing.T) {
	groupID := int64(7)
	bucket := SchedulerBucket{GroupID: groupID, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	svc := NewSchedulerSnapshotService(
		gatewayLiteRegisteredEmptySchedulerCache{bucket: bucket},
		nil,
		nil,
		nil,
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	accounts, useMixed, err := svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, false)

	require.NoError(t, err)
	require.False(t, useMixed)
	require.Empty(t, accounts)
}

func TestSchedulerSnapshotGatewayLiteStartRebuildsLocalAccountBuckets(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		gatewayLiteRebuildAccountRepo{
			stubOpenAIAccountRepo: stubOpenAIAccountRepo{
				accounts: []Account{{
					ID:          1001,
					Name:        "gateway-lite-openai",
					Platform:    PlatformOpenAI,
					Type:        AccountTypeAPIKey,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 200,
				}},
			},
		},
		nil,
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	svc.Start()
	defer svc.Stop()

	target := SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	deadline := time.After(2 * time.Second)
	for {
		if got := cache.snapshot(target); len(got) == 1 && got[0].ID == 1001 {
			return
		}
		select {
		case <-cache.written:
		case <-deadline:
			t.Fatalf("gateway-lite scheduler startup rebuild did not populate bucket %s", target.String())
		}
	}
}

func TestSchedulerSnapshotGatewayLiteGroupEventRefreshesGroupCache(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	group := &Group{ID: 88, Name: "高速", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 2}
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		gatewayLiteRebuildAccountRepo{},
		&gatewayLiteSchedulerGroupRepo{group: group},
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	err := svc.handleGroupEvent(context.Background(), &group.ID, nil, nil)
	require.NoError(t, err)

	cached, err := cache.GetGroup(context.Background(), group.ID)
	require.NoError(t, err)
	require.NotNil(t, cached)
	require.Equal(t, group.Name, cached.Name)
	require.Equal(t, group.RateMultiplier, cached.RateMultiplier)
}

func TestSchedulerSnapshotGatewayLiteDeletedGroupEventClearsGroupCache(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	groupID := int64(89)
	require.NoError(t, cache.SetGroup(context.Background(), &Group{
		ID: groupID, Name: "已删除", Platform: PlatformOpenAI, Status: StatusActive,
	}))
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		gatewayLiteRebuildAccountRepo{},
		&gatewayLiteSchedulerGroupRepo{err: ErrGroupNotFound},
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	err := svc.handleGroupEvent(context.Background(), &groupID, nil, nil)
	require.NoError(t, err)

	cached, err := cache.GetGroup(context.Background(), groupID)
	require.NoError(t, err)
	require.Nil(t, cached)
}

func TestSchedulerSnapshotGatewayLiteUsesRequestedGroupBucketWhenGroupProvided(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	defaultAccount := Account{
		ID:          1001,
		Name:        "gateway-lite-openai",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 200,
	}
	groupAccount := Account{
		ID:            2002,
		Name:          "gateway-lite-openai-vip",
		Platform:      PlatformOpenAI,
		Type:          AccountTypeAPIKey,
		Status:        StatusActive,
		Schedulable:   true,
		Concurrency:   200,
		AccountGroups: []AccountGroup{{GroupID: 1}},
	}
	require.NoError(t, cache.SetSnapshot(context.Background(), SchedulerBucket{
		GroupID:  0,
		Platform: PlatformOpenAI,
		Mode:     SchedulerModeSingle,
	}, []Account{defaultAccount}))
	require.NoError(t, cache.SetSnapshot(context.Background(), SchedulerBucket{
		GroupID:  1,
		Platform: PlatformOpenAI,
		Mode:     SchedulerModeSingle,
	}, []Account{groupAccount}))

	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		nil,
		nil,
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	mainGroupID := int64(1)
	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), &mainGroupID, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(2002), accounts[0].ID)

	defaultAccounts, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, defaultAccounts, 1)
	require.Equal(t, int64(1001), defaultAccounts[0].ID)
}

func TestSchedulerSnapshotGatewayLiteDefaultBucketIncludesGroupedAccounts(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		gatewayLiteRebuildAccountRepo{
			stubOpenAIAccountRepo: stubOpenAIAccountRepo{
				accounts: []Account{
					{
						ID:          1001,
						Name:        "gateway-lite-openai-default",
						Platform:    PlatformOpenAI,
						Type:        AccountTypeAPIKey,
						Status:      StatusActive,
						Schedulable: true,
						Concurrency: 200,
					},
					{
						ID:            2002,
						Name:          "gateway-lite-openai-vip",
						Platform:      PlatformOpenAI,
						Type:          AccountTypeAPIKey,
						Status:        StatusActive,
						Schedulable:   true,
						Concurrency:   200,
						AccountGroups: []AccountGroup{{GroupID: 88}},
					},
				},
			},
		},
		nil,
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	require.NoError(t, svc.rebuildBucket(context.Background(), SchedulerBucket{
		GroupID:  0,
		Platform: PlatformOpenAI,
		Mode:     SchedulerModeSingle,
	}, "test"))

	defaultAccounts := cache.snapshot(SchedulerBucket{GroupID: 0, Platform: PlatformOpenAI, Mode: SchedulerModeSingle})
	require.Len(t, defaultAccounts, 2)
	require.Equal(t, int64(1001), defaultAccounts[0].ID)
	require.Equal(t, int64(2002), defaultAccounts[1].ID)

	require.NoError(t, svc.rebuildBucket(context.Background(), SchedulerBucket{
		GroupID:  88,
		Platform: PlatformOpenAI,
		Mode:     SchedulerModeSingle,
	}, "test"))
	groupAccounts := cache.snapshot(SchedulerBucket{GroupID: 88, Platform: PlatformOpenAI, Mode: SchedulerModeSingle})
	require.Len(t, groupAccounts, 1)
	require.Equal(t, int64(2002), groupAccounts[0].ID)
}

func TestSchedulerSnapshotGatewayLiteRebuildExcludesDisabledAndUnschedulableAccounts(t *testing.T) {
	cache := newGatewayLiteRebuildSchedulerCache()
	svc := NewSchedulerSnapshotService(
		cache,
		nil,
		gatewayLiteRebuildAccountRepo{
			stubOpenAIAccountRepo: stubOpenAIAccountRepo{
				accounts: []Account{
					{
						ID:            3001,
						Name:          "gateway-lite-openai-ready",
						Platform:      PlatformOpenAI,
						Type:          AccountTypeAPIKey,
						Status:        StatusActive,
						Schedulable:   true,
						Concurrency:   200,
						AccountGroups: []AccountGroup{{GroupID: 99}},
					},
					{
						ID:            3002,
						Name:          "gateway-lite-openai-disabled",
						Platform:      PlatformOpenAI,
						Type:          AccountTypeAPIKey,
						Status:        StatusDisabled,
						Schedulable:   true,
						Concurrency:   200,
						AccountGroups: []AccountGroup{{GroupID: 99}},
					},
					{
						ID:            3003,
						Name:          "gateway-lite-openai-unschedulable",
						Platform:      PlatformOpenAI,
						Type:          AccountTypeAPIKey,
						Status:        StatusActive,
						Schedulable:   false,
						Concurrency:   200,
						AccountGroups: []AccountGroup{{GroupID: 99}},
					},
				},
			},
		},
		nil,
		&config.Config{RunMode: config.RunModeGatewayLite},
	)

	require.NoError(t, svc.rebuildBucket(context.Background(), SchedulerBucket{
		GroupID:  99,
		Platform: PlatformOpenAI,
		Mode:     SchedulerModeSingle,
	}, "test"))

	groupAccounts := cache.snapshot(SchedulerBucket{GroupID: 99, Platform: PlatformOpenAI, Mode: SchedulerModeSingle})
	require.Len(t, groupAccounts, 1)
	require.Equal(t, int64(3001), groupAccounts[0].ID)
}
