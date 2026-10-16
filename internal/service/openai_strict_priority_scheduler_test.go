package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type strictPriorityConcurrencyCache struct {
	schedulerTestConcurrencyCache
	mu             sync.Mutex
	attempts       []int64
	batchCalls     [][]int64
	released       []int64
	minConcurrency map[int64]int
}

type strictPrioritySnapshotCache struct {
	*openAISnapshotCacheStub
}

func (c *strictPrioritySnapshotCache) GetAccountConcurrencyLimits(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	limits := make(map[int64]int, len(accountIDs))
	for _, accountID := range accountIDs {
		if account := c.accountsByID[accountID]; account != nil {
			limits[accountID] = account.Concurrency
		}
	}
	return limits, nil
}

func (c *strictPriorityConcurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	c.mu.Lock()
	c.attempts = append(c.attempts, accountID)
	c.mu.Unlock()
	return c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, accountID, maxConcurrency, requestID)
}

func (c *strictPriorityConcurrencyCache) AcquireFirstAvailableAccountSlot(_ context.Context, candidates []AccountWithConcurrency, _ string) (int64, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	batchIDs := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		batchIDs = append(batchIDs, candidate.ID)
	}
	c.batchCalls = append(c.batchCalls, batchIDs)
	for _, candidate := range candidates {
		c.attempts = append(c.attempts, candidate.ID)
		if c.acquireResults != nil {
			if acquired, exists := c.acquireResults[candidate.ID]; exists && !acquired {
				continue
			}
		}
		if required, exists := c.minConcurrency[candidate.ID]; exists && candidate.MaxConcurrency < required {
			continue
		}
		return candidate.ID, true, nil
	}
	return 0, false, nil
}

func (c *strictPriorityConcurrencyCache) ReleaseAccountSlot(_ context.Context, accountID int64, _ string) error {
	c.mu.Lock()
	c.released = append(c.released, accountID)
	c.mu.Unlock()
	return nil
}

func (c *strictPriorityConcurrencyCache) attempted(accountID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, attemptedID := range c.attempts {
		if attemptedID == accountID {
			return true
		}
	}
	return false
}

func (c *strictPriorityConcurrencyCache) batchSizes() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	sizes := make([]int, len(c.batchCalls))
	for index := range c.batchCalls {
		sizes[index] = len(c.batchCalls[index])
	}
	return sizes
}

func (c *strictPriorityConcurrencyCache) releasedAccount(accountID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, releasedID := range c.released {
		if releasedID == accountID {
			return true
		}
	}
	return false
}

func strictPriorityAccount(id, groupID int64, globalPriority, groupPriority int) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    globalPriority,
		GroupIDs:    []int64{groupID},
		AccountGroups: []AccountGroup{
			{AccountID: id, GroupID: groupID, Priority: groupPriority},
		},
	}
}

func newStrictPrioritySchedulerTestService(accounts []Account, concurrencyCache ConcurrencyCache) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.LBTopK = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	return &OpenAIGatewayService{
		accountRepo: schedulerGroupAwareOpenAIAccountRepo{
			schedulerTestOpenAIAccountRepo{accounts: accounts},
		},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
	}
}

func selectStrictPriorityAccount(t *testing.T, svc *OpenAIGatewayService, groupID int64, sessionHash string, stickyAccountID int64) *AccountSelectionResult {
	t.Helper()
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil)
	selection, _, err := scheduler.Select(context.Background(), OpenAIAccountScheduleRequest{
		GroupID:           &groupID,
		Platform:          PlatformOpenAI,
		SessionHash:       sessionHash,
		StickyAccountID:   stickyAccountID,
		RequestedModel:    "gpt-5.1",
		SchedulingMode:    GroupSchedulingModeStrictPriority,
		RequiredTransport: OpenAIUpstreamTransportAny,
	})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	t.Cleanup(func() {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	})
	return selection
}

func TestBuildOpenAIStrictPriorityLayers_UsesGlobalAccountPriority(t *testing.T) {
	groupID := int64(7001)
	globalFirst := strictPriorityAccount(1, groupID, 1, 99)
	globalSecond := strictPriorityAccount(2, groupID, 2, 1)

	layers := buildOpenAIStrictPriorityLayers([]*Account{&globalSecond, &globalFirst})
	require.Len(t, layers, 2)
	require.Equal(t, 1, layers[0].priority)
	require.Equal(t, globalFirst.ID, layers[0].accounts[0].ID)
	require.Equal(t, 2, layers[1].priority)
	require.Equal(t, globalSecond.ID, layers[1].accounts[0].ID)
}

func TestStrictPriorityScheduler_ExhaustsCurrentLayerBeforeLowerPriority(t *testing.T) {
	groupID := int64(7002)
	accounts := []Account{
		strictPriorityAccount(101, groupID, 1, 99),
		strictPriorityAccount(102, groupID, 1, 98),
		strictPriorityAccount(201, groupID, 2, 0),
	}
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{101: false, 102: true, 201: true},
		loadMap: map[int64]*AccountLoadInfo{
			101: {AccountID: 101, LoadRate: 99},
			102: {AccountID: 102, LoadRate: 98},
			201: {AccountID: 201, LoadRate: 0},
		},
	}}
	svc := newStrictPrioritySchedulerTestService(accounts, cache)

	selection := selectStrictPriorityAccount(t, svc, groupID, "", 0)
	require.Equal(t, 1, selection.Account.Priority)
	require.False(t, cache.attempted(201), "同层仍有账号抢到槽位时不得尝试低优先级账号")
}

func TestStrictPriorityScheduler_FallsThroughOnlyAfterCurrentLayerIsFull(t *testing.T) {
	groupID := int64(7003)
	accounts := []Account{
		strictPriorityAccount(301, groupID, 1, 99),
		strictPriorityAccount(302, groupID, 1, 98),
		strictPriorityAccount(401, groupID, 2, 0),
	}
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{301: false, 302: false, 401: true},
	}}
	svc := newStrictPrioritySchedulerTestService(accounts, cache)

	selection := selectStrictPriorityAccount(t, svc, groupID, "", 0)
	require.Equal(t, int64(401), selection.Account.ID)
	require.True(t, cache.attempted(301))
	require.True(t, cache.attempted(302))
}

func TestStrictPriorityScheduler_HealthyFirstUsesSingleAcquire(t *testing.T) {
	groupID := int64(7009)
	first := strictPriorityAccount(901, groupID, 1, 1)
	second := strictPriorityAccount(902, groupID, 1, 1)
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{first.ID: true, second.ID: true},
	}}
	svc := newStrictPrioritySchedulerTestService([]Account{first, second}, cache)
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil).(*defaultOpenAIAccountScheduler)

	selection, _, err := scheduler.tryAcquireOpenAIStrictPriorityLayer(context.Background(), OpenAIAccountScheduleRequest{
		GroupID:           &groupID,
		Platform:          PlatformOpenAI,
		RequestedModel:    "gpt-5.1",
		SchedulingMode:    GroupSchedulingModeStrictPriority,
		RequiredTransport: OpenAIUpstreamTransportAny,
	}, []openAIAccountCandidateScore{{account: &first}, {account: &second}})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, first.ID, selection.Account.ID)
	require.Equal(t, []int64{first.ID}, cache.attempts)
	require.Empty(t, cache.batchSizes(), "首账号健康时不得让 Redis 扫描整个候选批次")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestStrictPriorityScheduler_BatchesHundredFullAccountsBeforeLowerLayer(t *testing.T) {
	groupID := int64(7010)
	accounts := make([]Account, 0, 101)
	acquireResults := make(map[int64]bool, 101)
	for index := 0; index < 100; index++ {
		accountID := int64(10000 + index)
		accounts = append(accounts, strictPriorityAccount(accountID, groupID, 1, 1))
		acquireResults[accountID] = false
	}
	lower := strictPriorityAccount(20000, groupID, 2, 2)
	accounts = append(accounts, lower)
	acquireResults[lower.ID] = true
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: acquireResults,
	}}
	svc := newStrictPrioritySchedulerTestService(accounts, cache)

	selection := selectStrictPriorityAccount(t, svc, groupID, "", 0)
	require.Equal(t, lower.ID, selection.Account.ID)
	require.Equal(t, []int{32, 32, 32, 3}, cache.batchSizes(), "先乐观检查首账号后，剩余 99 个满载账号应只产生 4 次批量抢槽")
}

func TestStrictPriorityScheduler_FullLayerDoesNotHydrateEveryAccount(t *testing.T) {
	groupID := int64(7012)
	accounts := make([]Account, 100)
	accountPointers := make([]*Account, 100)
	accountsByID := make(map[int64]*Account, 100)
	acquireResults := make(map[int64]bool, 100)
	selectionOrder := make([]openAIAccountCandidateScore, 100)
	for index := range accounts {
		accountID := int64(22000 + index)
		accounts[index] = strictPriorityAccount(accountID, groupID, 1, 1)
		accountPointers[index] = &accounts[index]
		accountsByID[accountID] = &accounts[index]
		acquireResults[accountID] = false
		selectionOrder[index] = openAIAccountCandidateScore{account: &accounts[index]}
	}
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: accountPointers,
		accountsByID:     accountsByID,
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: acquireResults,
	}}
	svc := &OpenAIGatewayService{
		cfg:                cfg,
		cache:              &schedulerTestGatewayCache{},
		schedulerSnapshot:  NewSchedulerSnapshotService(snapshotCache, nil, nil, nil, cfg),
		concurrencyService: NewConcurrencyService(cache),
	}
	scheduler, ok := newDefaultOpenAIAccountScheduler(svc, nil).(*defaultOpenAIAccountScheduler)
	require.True(t, ok)

	selection, _, err := scheduler.tryAcquireOpenAIStrictPriorityLayer(context.Background(), OpenAIAccountScheduleRequest{
		GroupID:           &groupID,
		Platform:          PlatformOpenAI,
		RequestedModel:    "gpt-5.1",
		SchedulingMode:    GroupSchedulingModeStrictPriority,
		RequiredTransport: OpenAIUpstreamTransportAny,
	}, selectionOrder)
	require.NoError(t, err)
	require.Nil(t, selection)
	accountReads, _ := snapshotCache.readCounts()
	require.Equal(t, 1, accountReads, "只允许乐观检查首账号，剩余满载候选不得逐个读取完整调度快照")
	require.Equal(t, []int{32, 32, 32, 3}, cache.batchSizes())
}

func TestStrictPriorityScheduler_ProbeContentionSkipsProbeAndTriesNextAccount(t *testing.T) {
	groupID := int64(7011)
	recovering := strictPriorityAccount(21001, groupID, 1, 1)
	healthy := strictPriorityAccount(21002, groupID, 1, 1)
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{recovering.ID: true, healthy.ID: true},
	}}
	svc := newStrictPrioritySchedulerTestService([]Account{recovering, healthy}, cache)
	cooledAt := time.Now().Add(-openAIModelTransientShortCooldown - time.Second)
	svc.recordOpenAIAccountModelTransientFailure(&recovering, "gpt-5.1", cooledAt.Add(-time.Millisecond))
	svc.recordOpenAIAccountModelTransientFailure(&recovering, "gpt-5.1", cooledAt)
	require.True(t, svc.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(&recovering, &groupID, "gpt-5.1"))
	t.Cleanup(func() {
		svc.ReleaseOpenAIAccountModelRecoveryProbeForGroup(&recovering, &groupID, "gpt-5.1")
	})

	selection := selectStrictPriorityAccount(t, svc, groupID, "probe-contention", recovering.ID)
	require.Equal(t, healthy.ID, selection.Account.ID)
	require.False(t, cache.attempted(recovering.ID), "半开探测冲突的账号不应进入批量并发抢槽")
	require.False(t, cache.releasedAccount(recovering.ID), "半开探测未获取时不应产生并发槽位释放")
	require.Empty(t, cache.batchSizes(), "剩余首个普通账号应走低开销乐观抢槽")
}

func TestStrictPriorityScheduler_ConcurrencyIncreaseKeepsSlotAndStickyBinding(t *testing.T) {
	groupID := int64(7013)
	busy := strictPriorityAccount(23000, groupID, 1, 1)
	stale := strictPriorityAccount(23001, groupID, 1, 1)
	fresh := stale
	fresh.Concurrency = 10
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&busy, &stale},
		accountsByID:     map[int64]*Account{busy.ID: &busy, fresh.ID: &fresh},
	}
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{busy.ID: false, fresh.ID: true},
	}}
	gatewayCache := &schedulerTestGatewayCache{}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	svc := &OpenAIGatewayService{
		cfg:   cfg,
		cache: gatewayCache,
		schedulerSnapshot: NewSchedulerSnapshotService(
			snapshotCache,
			nil,
			nil,
			nil,
			cfg,
		),
		concurrencyService: NewConcurrencyService(cache),
	}
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil).(*defaultOpenAIAccountScheduler)
	selection, _, err := scheduler.tryAcquireOpenAIStrictPriorityLayer(context.Background(), OpenAIAccountScheduleRequest{
		GroupID:           &groupID,
		Platform:          PlatformOpenAI,
		RequestedModel:    "gpt-5.1",
		SchedulingMode:    GroupSchedulingModeStrictPriority,
		SessionHash:       "dynamic-increase",
		RequiredTransport: OpenAIUpstreamTransportAny,
	}, []openAIAccountCandidateScore{{account: &busy}, {account: &stale}})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, 10, selection.Account.Concurrency)
	require.Equal(t, fresh.ID, gatewayCache.sessionBindings["openai:dynamic-increase"],
		"并发上限变化后仍选中原账号时必须保留会话粘性")
	require.Empty(t, cache.released, "上限变大时旧槽位仍然有效，不应异步释放后重抢")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestStrictPriorityScheduler_RefreshesFullBatchAfterConcurrencyIncrease(t *testing.T) {
	groupID := int64(7015)
	busy := strictPriorityAccount(23005, groupID, 1, 1)
	stale := strictPriorityAccount(23006, groupID, 1, 1)
	fresh := stale
	fresh.Concurrency = 10
	snapshotCache := &strictPrioritySnapshotCache{openAISnapshotCacheStub: &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&busy, &stale},
		accountsByID:     map[int64]*Account{busy.ID: &busy, stale.ID: &fresh},
	}}
	cache := &strictPriorityConcurrencyCache{
		schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{acquireResults: map[int64]bool{busy.ID: false, stale.ID: true}},
		minConcurrency:                map[int64]int{stale.ID: 10},
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	svc := &OpenAIGatewayService{
		cfg:                cfg,
		cache:              &schedulerTestGatewayCache{},
		schedulerSnapshot:  NewSchedulerSnapshotService(snapshotCache, nil, nil, nil, cfg),
		concurrencyService: NewConcurrencyService(cache),
	}
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil).(*defaultOpenAIAccountScheduler)
	selection, _, err := scheduler.tryAcquireOpenAIStrictPriorityLayer(context.Background(), OpenAIAccountScheduleRequest{
		GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-5.1",
		SchedulingMode: GroupSchedulingModeStrictPriority, RequiredTransport: OpenAIUpstreamTransportAny,
	}, []openAIAccountCandidateScore{{account: &busy}, {account: &stale}})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, stale.ID, selection.Account.ID)
	require.Equal(t, 10, selection.Account.Concurrency, "整批满载时应复核刚提高的并发上限")
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestStrictPriorityScheduler_ConcurrencyDecreaseReacquiresWithFreshLimit(t *testing.T) {
	groupID := int64(7014)
	busy := strictPriorityAccount(23003, groupID, 1, 1)
	stale := strictPriorityAccount(23002, groupID, 1, 1)
	fresh := stale
	fresh.Concurrency = 1
	stale.Concurrency = 5
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&busy, &stale},
		accountsByID:     map[int64]*Account{busy.ID: &busy, fresh.ID: &fresh},
	}
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{busy.ID: false, fresh.ID: true},
	}}
	gatewayCache := &schedulerTestGatewayCache{}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	svc := &OpenAIGatewayService{
		cfg:   cfg,
		cache: gatewayCache,
		schedulerSnapshot: NewSchedulerSnapshotService(
			snapshotCache,
			nil,
			nil,
			nil,
			cfg,
		),
		concurrencyService: NewConcurrencyService(cache),
	}
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil).(*defaultOpenAIAccountScheduler)
	selection, _, err := scheduler.tryAcquireOpenAIStrictPriorityLayer(context.Background(), OpenAIAccountScheduleRequest{
		GroupID:           &groupID,
		Platform:          PlatformOpenAI,
		RequestedModel:    "gpt-5.1",
		SchedulingMode:    GroupSchedulingModeStrictPriority,
		SessionHash:       "dynamic-decrease",
		RequiredTransport: OpenAIUpstreamTransportAny,
	}, []openAIAccountCandidateScore{{account: &busy}, {account: &stale}})
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, 1, selection.Account.Concurrency)
	require.True(t, cache.releasedAccount(stale.ID), "批量选中后发现上限变小时必须同步归还旧快照槽位")
	require.Equal(t, fresh.ID, gatewayCache.sessionBindings["openai:dynamic-decrease"])
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestStrictPriorityScheduler_OrdinaryStickyCannotCrossPriorityLayer(t *testing.T) {
	groupID := int64(7004)
	high := strictPriorityAccount(501, groupID, 1, 99)
	lowSticky := strictPriorityAccount(601, groupID, 2, 0)
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{501: true, 601: true},
	}}
	svc := newStrictPrioritySchedulerTestService([]Account{high, lowSticky}, cache)
	gatewayCache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:strict-sticky": lowSticky.ID}}
	svc.cache = gatewayCache

	selection := selectStrictPriorityAccount(t, svc, groupID, "strict-sticky", lowSticky.ID)
	require.Equal(t, high.ID, selection.Account.ID)
	require.False(t, cache.attempted(lowSticky.ID))
	require.Equal(t, high.ID, gatewayCache.sessionBindings["openai:strict-sticky"], "回到高优先级后应迁移普通粘性绑定")
}

func TestStrictPriorityScheduler_PreservesOrdinaryStickyInsideSameLayer(t *testing.T) {
	groupID := int64(7005)
	first := strictPriorityAccount(701, groupID, 1, 1)
	sticky := strictPriorityAccount(702, groupID, 1, 1)
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{701: true, 702: true},
		loadMap: map[int64]*AccountLoadInfo{
			701: {AccountID: 701, LoadRate: 0},
			702: {AccountID: 702, LoadRate: 95},
		},
	}}
	svc := newStrictPrioritySchedulerTestService([]Account{first, sticky}, cache)

	selection := selectStrictPriorityAccount(t, svc, groupID, "same-layer-sticky", sticky.ID)
	require.Equal(t, sticky.ID, selection.Account.ID)
}

func TestStrictPriorityScheduler_PreservesPreviousResponseHardBinding(t *testing.T) {
	groupID := int64(7006)
	high := strictPriorityAccount(751, groupID, 1, 99)
	boundLow := strictPriorityAccount(752, groupID, 2, 0)
	boundLow.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
	cache := &strictPriorityConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
		acquireResults: map[int64]bool{high.ID: true, boundLow.ID: true},
	}}
	svc := newStrictPrioritySchedulerTestService([]Account{high, boundLow}, cache)
	store := svc.getOpenAIWSStateStore()
	require.NoError(t, store.BindResponseAccount(context.Background(), groupID, "resp_strict_priority", boundLow.ID, time.Hour))

	req := OpenAIAccountScheduleRequest{
		GroupID:            &groupID,
		Platform:           PlatformOpenAI,
		PreviousResponseID: "resp_strict_priority",
		SchedulingMode:     GroupSchedulingModeStrictPriority,
		RequestedModel:     "gpt-5.1",
		RequiredTransport:  OpenAIUpstreamTransportAny,
	}
	require.True(t, shouldUseOpenAIPreviousResponseBinding(req))
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil)
	selection, decision, err := scheduler.Select(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, boundLow.ID, selection.Account.ID, "协议强绑定必须优先于严格优先级")
	require.True(t, decision.StickyPreviousHit)
	require.False(t, cache.attempted(high.ID))
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestStrictPriorityScheduler_WorksWhenAdvancedSchedulerIsDisabled(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	groupID := int64(7007)
	high := strictPriorityAccount(801, groupID, 1, 99)
	low := strictPriorityAccount(802, groupID, 2, 0)
	snapshotCache := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&high, &low},
		accountsByID:     map[int64]*Account{high.ID: &high, low.ID: &low},
		groups: map[int64]*Group{
			groupID: {
				ID:             groupID,
				Platform:       PlatformOpenAI,
				Status:         StatusActive,
				SchedulingMode: GroupSchedulingModeStrictPriority,
			},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	svc := &OpenAIGatewayService{
		cfg:   cfg,
		cache: &schedulerTestGatewayCache{},
		schedulerSnapshot: NewSchedulerSnapshotService(
			snapshotCache,
			nil,
			nil,
			nil,
			cfg,
		),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquireResults: map[int64]bool{high.ID: true, low.ID: true},
		}),
	}

	selection, _, err := svc.SelectAccountWithScheduler(
		context.Background(), &groupID, "", "", "gpt-5.1", nil,
		OpenAIUpstreamTransportAny, false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, high.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}
