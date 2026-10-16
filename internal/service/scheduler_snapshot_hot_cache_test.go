package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type schedulerHotCacheTestCache struct {
	SchedulerCache
	mu       sync.Mutex
	account  *Account
	group    *Group
	snapshot []*Account

	snapshotReads atomic.Int64
	accountReads  atomic.Int64
	groupReads    atomic.Int64
}

func (c *schedulerHotCacheTestCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	c.snapshotReads.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Account, 0, len(c.snapshot))
	for _, account := range c.snapshot {
		out = append(out, cloneSchedulerHotAccount(account))
	}
	return out, true, nil
}

func (c *schedulerHotCacheTestCache) GetAccount(context.Context, int64) (*Account, error) {
	c.accountReads.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneSchedulerHotAccount(c.account), nil
}

func (c *schedulerHotCacheTestCache) SetAccount(_ context.Context, account *Account) error {
	c.mu.Lock()
	c.account = cloneSchedulerHotAccount(account)
	c.mu.Unlock()
	return nil
}

func (c *schedulerHotCacheTestCache) GetGroup(context.Context, int64) (*Group, error) {
	c.groupReads.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.group == nil {
		return nil, nil
	}
	cloned := *c.group
	return &cloned, nil
}

func (c *schedulerHotCacheTestCache) SetGroup(_ context.Context, group *Group) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if group == nil {
		c.group = nil
		return nil
	}
	cloned := *group
	c.group = &cloned
	return nil
}

func (c *schedulerHotCacheTestCache) DeleteGroup(context.Context, int64) error {
	c.mu.Lock()
	c.group = nil
	c.mu.Unlock()
	return nil
}

func newSchedulerHotCacheTestService(cache SchedulerCache) *SchedulerSnapshotService {
	return NewSchedulerSnapshotService(cache, nil, nil, nil, &config.Config{RunMode: config.RunModeGatewayLite})
}

func TestSchedulerSnapshotHotCacheCoalescesConcurrentSnapshotReads(t *testing.T) {
	cache := &schedulerHotCacheTestCache{snapshot: []*Account{{
		ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
	}}}
	svc := newSchedulerHotCacheTestService(cache)
	groupID := int64(7)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			accounts, _, err := svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, true)
			require.NoError(t, err)
			require.Len(t, accounts, 1)
		}()
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, 1, cache.snapshotReads.Load())
}

func TestSchedulerSnapshotHotCacheDoesNotTreatCandidateMetadataAsFullAccount(t *testing.T) {
	metadata := &Account{
		ID:          10,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"auth_mode":     OpenAIAuthModeAgentIdentity,
			"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
		},
	}
	full := *metadata
	full.Credentials = map[string]any{
		"auth_mode":         OpenAIAuthModeAgentIdentity,
		"agent_runtime_id":  "runtime-full",
		"agent_private_key": "private-key-full",
		"task_id":           "task-full",
		"model_mapping":     map[string]any{"gpt-5.5": "gpt-5.5"},
	}
	cache := &schedulerHotCacheTestCache{
		snapshot: []*Account{metadata},
		account:  &full,
	}
	svc := newSchedulerHotCacheTestService(cache)
	groupID := int64(7)

	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, true)
	require.NoError(t, err)
	require.Len(t, accounts, 1)

	hydrated, err := svc.GetAccount(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, "runtime-full", hydrated.GetCredential("agent_runtime_id"))
	require.Equal(t, "private-key-full", hydrated.GetCredential("agent_private_key"))
	require.EqualValues(t, 1, cache.accountReads.Load(), "hydrate must read the full account cache once")
}

func TestSchedulerSnapshotHotCacheReusesAccountAndGroupThenExpires(t *testing.T) {
	cache := &schedulerHotCacheTestCache{
		account: &Account{ID: 11, Name: "account-a", Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true},
		group:   &Group{ID: 22, Name: "group-a", Status: StatusActive},
	}
	svc := newSchedulerHotCacheTestService(cache)

	account, err := svc.GetAccount(context.Background(), 11)
	require.NoError(t, err)
	account.Name = "request-local-change"
	accountAgain, err := svc.GetAccount(context.Background(), 11)
	require.NoError(t, err)
	require.Equal(t, "account-a", accountAgain.Name)

	group, err := svc.GetGroupByID(context.Background(), 22)
	require.NoError(t, err)
	group.Name = "request-local-change"
	groupAgain, err := svc.GetGroupByID(context.Background(), 22)
	require.NoError(t, err)
	require.Equal(t, "group-a", groupAgain.Name)
	require.EqualValues(t, 1, cache.accountReads.Load())
	require.EqualValues(t, 1, cache.groupReads.Load())

	time.Sleep(schedulerHotReadTTL + 25*time.Millisecond)
	_, err = svc.GetAccount(context.Background(), 11)
	require.NoError(t, err)
	_, err = svc.GetGroupByID(context.Background(), 22)
	require.NoError(t, err)
	require.EqualValues(t, 2, cache.accountReads.Load())
	require.EqualValues(t, 2, cache.groupReads.Load())
}

func TestSchedulerSnapshotHotCacheAccountUpdateInvalidatesCandidateBuckets(t *testing.T) {
	cache := &schedulerHotCacheTestCache{snapshot: []*Account{{
		ID: 31, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true,
	}}}
	svc := newSchedulerHotCacheTestService(cache)
	groupID := int64(9)

	_, _, err := svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, true)
	require.NoError(t, err)
	require.NoError(t, svc.UpdateAccountInCache(context.Background(), &Account{
		ID: 31, Platform: PlatformOpenAI, Status: StatusDisabled, Schedulable: false,
	}))
	_, _, err = svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, true)
	require.NoError(t, err)
	require.EqualValues(t, 2, cache.snapshotReads.Load())
}
