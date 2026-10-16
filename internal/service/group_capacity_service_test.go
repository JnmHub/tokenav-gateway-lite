//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type groupCapacityAccountRepoStub struct {
	stubOpenAIAccountRepo
	listSchedulableCalls        int
	listSchedulableByGroupCalls int
}

func (r *groupCapacityAccountRepoStub) ListSchedulable(context.Context) ([]Account, error) {
	r.listSchedulableCalls++
	return r.accounts, nil
}

func (r *groupCapacityAccountRepoStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	r.listSchedulableByGroupCalls++
	return nil, nil
}

type groupCapacityGroupRepoStub struct {
	GroupRepository
	groups []Group
}

func (r groupCapacityGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	return r.groups, nil
}

type groupCapacityConcurrencyCacheStub struct {
	ConcurrencyCache
	calls      int
	ids        []int64
	count      map[int64]int
	ingress    map[int64]int
	ingressErr error
}

func (c *groupCapacityConcurrencyCacheStub) GetAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	c.calls++
	c.ids = append([]int64(nil), accountIDs...)
	return c.count, nil
}

func (c *groupCapacityConcurrencyCacheStub) TrackGroupIngressSlot(context.Context, int64, string) error {
	return nil
}

func (c *groupCapacityConcurrencyCacheStub) ReleaseGroupIngressSlot(context.Context, int64, string) error {
	return nil
}

func (c *groupCapacityConcurrencyCacheStub) GetGroupIngressConcurrencyBatch(_ context.Context, groupIDs []int64) (map[int64]int, error) {
	if c.ingressErr != nil {
		return nil, c.ingressErr
	}
	result := make(map[int64]int, len(groupIDs))
	for _, groupID := range groupIDs {
		result[groupID] = c.ingress[groupID]
	}
	return result, nil
}

func TestGroupCapacityServiceReturnsUnknownIngressError(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	concurrencyCache := &groupCapacityConcurrencyCacheStub{ingressErr: wantErr}
	svc := NewGroupCapacityService(
		&groupCapacityAccountRepoStub{},
		groupCapacityGroupRepoStub{groups: []Group{{ID: 101, Status: StatusActive}}},
		NewConcurrencyService(concurrencyCache),
		nil,
		nil,
	)

	got, err := svc.GetAllGroupCapacity(context.Background())
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, got, "入口统计未知时不能返回看似有效的零值")
}

type groupCapacitySessionCacheStub struct {
	SessionLimitCache
	calls    int
	ids      []int64
	timeouts map[int64]time.Duration
	count    map[int64]int
}

func (c *groupCapacitySessionCacheStub) GetActiveSessionCountBatch(_ context.Context, accountIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	c.calls++
	c.ids = append([]int64(nil), accountIDs...)
	c.timeouts = idleTimeouts
	return c.count, nil
}

type groupCapacityRPMCacheStub struct {
	RPMCache
	calls int
	ids   []int64
	count map[int64]int
}

func (c *groupCapacityRPMCacheStub) GetRPMBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	c.calls++
	c.ids = append([]int64(nil), accountIDs...)
	return c.count, nil
}

func TestGroupCapacityServiceGetAllGroupCapacityUsesBatchAggregation(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		stubOpenAIAccountRepo: stubOpenAIAccountRepo{
			accounts: []Account{
				{
					ID:          11,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 10,
					Extra: map[string]any{
						"max_sessions":                 3,
						"session_idle_timeout_minutes": 7,
						"base_rpm":                     60,
					},
					AccountGroups: []AccountGroup{{GroupID: 101}},
				},
				{
					ID:          22,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 20,
					Extra: map[string]any{
						"base_rpm": 120,
					},
					AccountGroups: []AccountGroup{{GroupID: 101}, {GroupID: 202}},
				},
				{
					ID:          33,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 30,
					AccountGroups: []AccountGroup{
						{GroupID: 999},
					},
				},
			},
		},
	}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{count: map[int64]int{11: 1, 22: 2, 33: 3}, ingress: map[int64]int{101: 30, 202: 70}}
	sessionCache := &groupCapacitySessionCacheStub{count: map[int64]int{11: 2}}
	rpmCache := &groupCapacityRPMCacheStub{count: map[int64]int{11: 6, 22: 12}}
	svc := NewGroupCapacityService(
		accountRepo,
		groupCapacityGroupRepoStub{groups: []Group{
			{ID: 101, Status: StatusActive},
			{ID: 202, Status: StatusActive},
			{ID: 303, Status: StatusActive},
		}},
		NewConcurrencyService(concurrencyCache),
		sessionCache,
		rpmCache,
	)

	got, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, accountRepo.listSchedulableCalls)
	require.Equal(t, 0, accountRepo.listSchedulableByGroupCalls)
	require.Equal(t, 1, concurrencyCache.calls)
	require.ElementsMatch(t, []int64{11, 22, 33}, concurrencyCache.ids)
	require.Equal(t, 1, sessionCache.calls)
	require.ElementsMatch(t, []int64{11}, sessionCache.ids)
	require.Equal(t, 7*time.Minute, sessionCache.timeouts[11])
	require.Equal(t, 1, rpmCache.calls)
	require.ElementsMatch(t, []int64{11, 22}, rpmCache.ids)

	require.Len(t, got, 3)
	require.Equal(t, GroupCapacitySummary{
		GroupID:             101,
		ConcurrencyUsed:     3,
		ConcurrencyMax:      30,
		SessionsUsed:        2,
		SessionsMax:         3,
		RPMUsed:             18,
		RPMMax:              180,
		IngressRequestsUsed: 30,
	}, got[0])
	require.Equal(t, GroupCapacitySummary{
		GroupID:             202,
		ConcurrencyUsed:     2,
		ConcurrencyMax:      20,
		RPMUsed:             12,
		RPMMax:              120,
		IngressRequestsUsed: 70,
	}, got[1])
	require.Equal(t, GroupCapacitySummary{GroupID: 303}, got[2])
}
