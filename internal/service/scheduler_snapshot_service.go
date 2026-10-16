package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"golang.org/x/sync/singleflight"
)

var (
	ErrSchedulerCacheNotReady           = errors.New("scheduler cache not ready")
	ErrSchedulerFallbackLimited         = errors.New("scheduler db fallback limited")
	ErrSchedulerGroupLifecycleLeaseBusy = errors.New("scheduler group lifecycle lease busy")
	ErrSchedulerBucketRebuildBusy       = errors.New("scheduler bucket rebuild busy")
)

const (
	outboxEventTimeout                    = 2 * time.Minute
	schedulerOutboxCleanupBatch           = 5000
	schedulerGroupLifecycleTimeout        = 30 * time.Second
	schedulerGroupLifecycleLeaseTTL       = 60 * time.Second
	schedulerGroupLifecycleReleaseTimeout = 2 * time.Second
	outboxRebuildRetryBaseDelay           = 5 * time.Second
	outboxRebuildRetryMaxDelay            = 5 * time.Minute
	outboxMaxIDErrorLogSampleInterval     = time.Minute
	// 账号和分组变更会主动失效本地热缓存；500ms 只作为漏掉失效事件时的最终上限。
	// 该窗口可以合并连续并发波次的 Redis 快照读取，避免 4C4G 节点反复解析同一份调度数据。
	schedulerHotReadTTL = 500 * time.Millisecond
)

type schedulerHotSnapshotEntry struct {
	accounts  []Account
	expiresAt int64
}

type schedulerHotAccountEntry struct {
	account   Account
	expiresAt int64
}

type schedulerHotGroupEntry struct {
	group     Group
	expiresAt int64
}

type schedulerSnapshotReadResult struct {
	accounts []Account
	hit      bool
}

// batchSeenKey tracks completed per-platform rebuilds and group lifecycle work
// within one pollOutbox call.
type batchSeenKey struct {
	groupID   int64
	platform  string
	lifecycle bool
}

type schedulerBucketWriteTask struct {
	bucket SchedulerBucket
	token  SchedulerBucketWriteToken
}

type schedulerAccountQueryKey struct {
	groupID  int64
	platform string
}

// schedulerAccountQueryCache 只在一次 rebuild（重建）批次内复用 single/forced 查询结果。
// mixed（混合调度）和历史未知模式不复用，避免不同语义的桶共享错误账号集。
type schedulerAccountQueryCache struct {
	remaining          map[schedulerAccountQueryKey]int
	accounts           map[schedulerAccountQueryKey][]Account
	snapshotAccountIDs map[schedulerAccountQueryKey][]int64
}

// schedulerSnapshotAccountIDWriter 是 SchedulerCache 的可选批次优化能力。
// 首个桶完整写入并完成 fencing（写入防护）后返回实际可编码账号 ID，
// 同一查询结果的后续桶只复用这些 ID，避免重复 JSON 序列化和全局账号键写入。
type schedulerSnapshotAccountIDWriter interface {
	SetSnapshotAndReturnAccountIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accounts []Account) ([]int64, error)
	SetSnapshotByAccountIDs(ctx context.Context, bucket SchedulerBucket, token SchedulerBucketWriteToken, accountIDs []int64) error
}

func newSchedulerAccountQueryCache(taskSets ...[]schedulerBucketWriteTask) *schedulerAccountQueryCache {
	queries := &schedulerAccountQueryCache{
		remaining:          make(map[schedulerAccountQueryKey]int),
		accounts:           make(map[schedulerAccountQueryKey][]Account),
		snapshotAccountIDs: make(map[schedulerAccountQueryKey][]int64),
	}
	for _, tasks := range taskSets {
		for _, task := range tasks {
			if key, ok := schedulerAccountQueryKeyForBucket(task.bucket); ok {
				queries.remaining[key]++
			}
		}
	}
	return queries
}

func schedulerAccountQueryKeyForBucket(bucket SchedulerBucket) (schedulerAccountQueryKey, bool) {
	if bucket.Mode != SchedulerModeSingle && bucket.Mode != SchedulerModeForced {
		return schedulerAccountQueryKey{}, false
	}
	return schedulerAccountQueryKey{groupID: bucket.GroupID, platform: bucket.Platform}, true
}

func (c *schedulerAccountQueryCache) release(bucket SchedulerBucket) {
	if c == nil {
		return
	}
	key, ok := schedulerAccountQueryKeyForBucket(bucket)
	if !ok {
		return
	}
	remaining := c.remaining[key] - 1
	if remaining <= 0 {
		delete(c.remaining, key)
		delete(c.accounts, key)
		delete(c.snapshotAccountIDs, key)
		return
	}
	c.remaining[key] = remaining
}

type schedulerGroupLifecyclePlan struct {
	active bool
	tasks  []schedulerBucketWriteTask
}

type schedulerActiveGroupIDLister interface {
	ListActiveIDs(ctx context.Context) ([]int64, error)
}

type schedulerOutboxPendingReader interface {
	FirstCreatedAtAfter(ctx context.Context, afterID int64) (time.Time, bool, error)
}

type SchedulerSnapshotService struct {
	cache                        SchedulerCache
	outboxRepo                   SchedulerOutboxRepository
	accountRepo                  AccountRepository
	groupRepo                    GroupRepository
	cfg                          *config.Config
	stopCh                       chan struct{}
	stopOnce                     sync.Once
	wg                           sync.WaitGroup
	fallbackLimit                *fallbackLimiter
	lagMu                        sync.Mutex
	lagFailures                  int
	outboxRebuildLatched         bool
	outboxRebuildRunning         bool
	outboxRebuildFailures        int
	outboxRebuildRetryAt         time.Time
	outboxRebuildRetryReason     string
	outboxLagWarningActive       bool
	outboxMaxIDErrorLastLoggedAt time.Time

	fullRebuildRunMu     sync.Mutex
	fullRebuildStateMu   sync.Mutex
	fullRebuildRequested uint64
	fullRebuildCompleted uint64
	fullRebuildLastErr   error

	// gateway-lite 的 Redis scheduler snapshot（调度快照）是跨实例权威；这里仅做
	// 500ms 进程内 L1 读取合并。账号并发、限流和 runtime block 仍逐请求查 Redis。
	hotMu        sync.RWMutex
	hotSnapshots map[SchedulerBucket]schedulerHotSnapshotEntry
	hotAccounts  map[int64]schedulerHotAccountEntry
	hotGroups    map[int64]schedulerHotGroupEntry
	hotReadSF    singleflight.Group
}

func NewSchedulerSnapshotService(
	cache SchedulerCache,
	outboxRepo SchedulerOutboxRepository,
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	cfg *config.Config,
) *SchedulerSnapshotService {
	maxQPS := 0
	if cfg != nil {
		maxQPS = cfg.Gateway.Scheduling.DbFallbackMaxQPS
	}
	return &SchedulerSnapshotService{
		cache:         cache,
		outboxRepo:    outboxRepo,
		accountRepo:   accountRepo,
		groupRepo:     groupRepo,
		cfg:           cfg,
		stopCh:        make(chan struct{}),
		fallbackLimit: newFallbackLimiter(maxQPS),
		hotSnapshots:  make(map[SchedulerBucket]schedulerHotSnapshotEntry),
		hotAccounts:   make(map[int64]schedulerHotAccountEntry),
		hotGroups:     make(map[int64]schedulerHotGroupEntry),
	}
}

func (s *SchedulerSnapshotService) Start() {
	if s == nil || s.cache == nil {
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runInitialRebuild()
	}()

	interval := s.outboxPollInterval()
	if s.outboxRepo != nil && interval > 0 {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runOutboxWorker(interval)
		}()
	}

	fullInterval := s.fullRebuildInterval()
	if fullInterval > 0 {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runFullRebuildWorker(fullInterval)
		}()
	}
}

func (s *SchedulerSnapshotService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *SchedulerSnapshotService) schedulerHotCacheEnabled() bool {
	return s != nil && s.isRunModeGatewayLite()
}

func (s *SchedulerSnapshotService) getHotSnapshot(bucket SchedulerBucket) ([]Account, bool) {
	if !s.schedulerHotCacheEnabled() {
		return nil, false
	}
	now := time.Now().UnixNano()
	s.hotMu.RLock()
	entry, ok := s.hotSnapshots[bucket]
	s.hotMu.RUnlock()
	if !ok || now >= entry.expiresAt {
		if ok {
			s.hotMu.Lock()
			if current, exists := s.hotSnapshots[bucket]; exists && now >= current.expiresAt {
				delete(s.hotSnapshots, bucket)
			}
			s.hotMu.Unlock()
		}
		return nil, false
	}
	return cloneSchedulerHotAccounts(entry.accounts), true
}

func (s *SchedulerSnapshotService) storeHotSnapshot(bucket SchedulerBucket, accounts []Account) {
	if !s.schedulerHotCacheEnabled() {
		return
	}
	expiresAt := time.Now().Add(schedulerHotReadTTL).UnixNano()
	stored := cloneSchedulerHotAccounts(accounts)
	s.hotMu.Lock()
	s.hotSnapshots[bucket] = schedulerHotSnapshotEntry{accounts: stored, expiresAt: expiresAt}
	// GetSnapshot 返回的是 sched:meta（调度候选元数据），其中故意不包含 Token、
	// Agent Identity 私钥等转发凭据。不得把它写入 full-account hot cache（完整账号热缓存），
	// 否则选中账号后的 hydrate（凭据补全）会误命中元数据并跳过 sched:acc 完整账号。
	s.hotMu.Unlock()
}

func (s *SchedulerSnapshotService) readSnapshotWithHotCache(ctx context.Context, bucket SchedulerBucket) ([]Account, bool, error) {
	if cached, ok := s.getHotSnapshot(bucket); ok {
		return cached, true, nil
	}
	if s == nil || s.cache == nil {
		return nil, false, nil
	}
	if !s.schedulerHotCacheEnabled() {
		cached, hit, err := s.cache.GetSnapshot(ctx, bucket)
		return derefAccounts(cached), hit, err
	}
	value, err, _ := s.hotReadSF.Do("snapshot:"+bucket.String(), func() (any, error) {
		if cached, ok := s.getHotSnapshot(bucket); ok {
			return schedulerSnapshotReadResult{accounts: cached, hit: true}, nil
		}
		cached, hit, readErr := s.cache.GetSnapshot(ctx, bucket)
		if readErr != nil {
			return nil, readErr
		}
		accounts := derefAccounts(cached)
		if hit {
			s.storeHotSnapshot(bucket, accounts)
		}
		return schedulerSnapshotReadResult{accounts: accounts, hit: hit}, nil
	})
	if err != nil {
		return nil, false, err
	}
	result, ok := value.(schedulerSnapshotReadResult)
	if !ok {
		return nil, false, ErrSchedulerCacheNotReady
	}
	return cloneSchedulerHotAccounts(result.accounts), result.hit, nil
}

func (s *SchedulerSnapshotService) getHotAccount(accountID int64) (*Account, bool) {
	if !s.schedulerHotCacheEnabled() || accountID <= 0 {
		return nil, false
	}
	now := time.Now().UnixNano()
	s.hotMu.RLock()
	entry, ok := s.hotAccounts[accountID]
	s.hotMu.RUnlock()
	if !ok || now >= entry.expiresAt {
		if ok {
			s.hotMu.Lock()
			if current, exists := s.hotAccounts[accountID]; exists && now >= current.expiresAt {
				delete(s.hotAccounts, accountID)
			}
			s.hotMu.Unlock()
		}
		return nil, false
	}
	return cloneSchedulerHotAccount(&entry.account), true
}

func (s *SchedulerSnapshotService) storeHotAccount(account *Account) {
	if !s.schedulerHotCacheEnabled() || account == nil || account.ID <= 0 {
		return
	}
	cloned := cloneSchedulerHotAccount(account)
	s.hotMu.Lock()
	s.hotAccounts[account.ID] = schedulerHotAccountEntry{
		account:   *cloned,
		expiresAt: time.Now().Add(schedulerHotReadTTL).UnixNano(),
	}
	s.hotMu.Unlock()
}

func (s *SchedulerSnapshotService) getHotGroup(groupID int64) (*Group, bool) {
	if !s.schedulerHotCacheEnabled() || groupID <= 0 {
		return nil, false
	}
	now := time.Now().UnixNano()
	s.hotMu.RLock()
	entry, ok := s.hotGroups[groupID]
	s.hotMu.RUnlock()
	if !ok || now >= entry.expiresAt {
		if ok {
			s.hotMu.Lock()
			if current, exists := s.hotGroups[groupID]; exists && now >= current.expiresAt {
				delete(s.hotGroups, groupID)
			}
			s.hotMu.Unlock()
		}
		return nil, false
	}
	cloned := entry.group
	return &cloned, true
}

func (s *SchedulerSnapshotService) storeHotGroup(group *Group) {
	if !s.schedulerHotCacheEnabled() || group == nil || group.ID <= 0 {
		return
	}
	cloned := *group
	s.hotMu.Lock()
	s.hotGroups[group.ID] = schedulerHotGroupEntry{
		group:     cloned,
		expiresAt: time.Now().Add(schedulerHotReadTTL).UnixNano(),
	}
	s.hotMu.Unlock()
}

func (s *SchedulerSnapshotService) clearSchedulerHotSnapshots() {
	if s == nil {
		return
	}
	s.hotMu.Lock()
	clear(s.hotSnapshots)
	s.hotMu.Unlock()
}

func (s *SchedulerSnapshotService) invalidateSchedulerHotAccount(accountID int64) {
	s.invalidateSchedulerHotAccounts([]int64{accountID})
}

func (s *SchedulerSnapshotService) invalidateSchedulerHotAccounts(accountIDs []int64) {
	if s == nil {
		return
	}
	s.hotMu.Lock()
	for _, accountID := range accountIDs {
		if accountID > 0 {
			delete(s.hotAccounts, accountID)
		}
	}
	clear(s.hotSnapshots)
	s.hotMu.Unlock()
}

func (s *SchedulerSnapshotService) clearSchedulerHotAccountsAndSnapshots() {
	if s == nil {
		return
	}
	s.hotMu.Lock()
	clear(s.hotAccounts)
	clear(s.hotSnapshots)
	s.hotMu.Unlock()
}

func cloneSchedulerHotAccounts(accounts []Account) []Account {
	if len(accounts) == 0 {
		return []Account{}
	}
	out := make([]Account, len(accounts))
	for i := range accounts {
		out[i] = *cloneSchedulerHotAccount(&accounts[i])
	}
	return out
}

func cloneSchedulerHotAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	cloned := *account
	// 这些字段会在请求内惰性写入；跨请求共享会产生 data race（数据竞争）。
	cloned.modelMappingCache = nil
	cloned.modelMappingCacheReady = false
	cloned.modelMappingCacheCredentialsPtr = 0
	cloned.modelMappingCacheRawPtr = 0
	cloned.modelMappingCacheRawLen = 0
	cloned.modelMappingCacheRawSig = 0
	cloned.modelMappingCacheRuntimeVersion = 0
	cloned.headerOverrideCache = nil
	cloned.headerOverrideCacheReady = false
	cloned.headerOverrideCacheCredentialsPtr = 0
	cloned.headerOverrideCacheRawPtr = 0
	cloned.headerOverrideCacheRawLen = 0
	cloned.headerOverrideCacheRawSig = 0
	return &cloned
}

func (s *SchedulerSnapshotService) ListSchedulableAccounts(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]Account, bool, error) {
	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	if err := ctx.Err(); err != nil {
		return nil, useMixed, err
	}
	mode := s.resolveMode(platform, hasForcePlatform)
	bucket := s.bucketFor(groupID, platform, mode)
	var writeToken SchedulerBucketWriteToken
	var lifecycleCache SchedulerBucketLifecycleCache
	canPublish := false
	if s.cache != nil {
		if cache, ok := s.cache.(SchedulerBucketLifecycleCache); ok {
			lifecycleCache = cache
		}
	}

	if s.cache != nil {
		cached, hit, err := s.readSnapshotWithHotCache(ctx, bucket)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, useMixed, ctxErr
		}
		if err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] cache read failed: bucket=%s err=%v", bucket.String(), err)
		} else if hit {
			return cached, useMixed, nil
		} else if s.isRunModeGatewayLite() {
			registered, err := s.isBucketRegistered(ctx, bucket)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, useMixed, ctxErr
			}
			if err != nil {
				logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] bucket registry read failed: bucket=%s err=%v", bucket.String(), err)
			} else if registered {
				// gateway-lite（轻量网关）以本地调度快照为热路径权威，不允许 cache miss
				// 回退数据库。已注册 bucket（调度桶）命中空快照时，业务含义是“当前
				// 分组没有可调度账号”，不能继续向上冒泡成“调度缓存未准备好”。
				return []Account{}, useMixed, nil
			}
		}
		if lifecycleCache != nil {
			token, err := lifecycleCache.CaptureBucketWriteToken(ctx, bucket)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, useMixed, ctxErr
			}
			if err != nil {
				if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
					slog.Debug("[Scheduler] cache publish fenced", "bucket", bucket.String())
				} else {
					logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] cache publish token failed: bucket=%s err=%v", bucket.String(), err)
				}
			} else {
				writeToken = token
				canPublish = true
			}
		}
	}

	if err := s.guardFallback(ctx); err != nil {
		return nil, useMixed, err
	}

	fallbackCtx, cancel := s.withFallbackTimeout(ctx)
	defer cancel()

	accounts, err := s.loadAccountsFromDB(fallbackCtx, bucket, useMixed)
	if err != nil {
		return nil, useMixed, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, useMixed, ctxErr
	}

	if s.cache != nil {
		var err error
		if lifecycleCache != nil && canPublish {
			err = lifecycleCache.SetSnapshotWithToken(fallbackCtx, bucket, writeToken, accounts)
		} else {
			err = s.cache.SetSnapshot(fallbackCtx, bucket, accounts)
		}
		if err != nil {
			if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
				slog.Debug("[Scheduler] cache publish fenced", "bucket", bucket.String())
			} else {
				logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] cache write failed: bucket=%s err=%v", bucket.String(), err)
			}
		} else {
			s.storeHotSnapshot(bucket, accounts)
		}
	}

	return accounts, useMixed, nil
}

func (s *SchedulerSnapshotService) isBucketRegistered(ctx context.Context, bucket SchedulerBucket) (bool, error) {
	if s == nil || s.cache == nil {
		return false, nil
	}
	buckets, err := s.cache.ListBuckets(ctx)
	if err != nil {
		return false, err
	}
	for _, item := range buckets {
		if item == bucket {
			return true, nil
		}
	}
	return false, nil
}

func (s *SchedulerSnapshotService) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	if accountID <= 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.cache != nil {
		if account, ok := s.getHotAccount(accountID); ok {
			return account, nil
		}
		var account *Account
		var err error
		if s.schedulerHotCacheEnabled() {
			value, readErr, _ := s.hotReadSF.Do("account:"+strconv.FormatInt(accountID, 10), func() (any, error) {
				if cached, ok := s.getHotAccount(accountID); ok {
					return cached, nil
				}
				return s.cache.GetAccount(ctx, accountID)
			})
			err = readErr
			if value != nil {
				account, _ = value.(*Account)
			}
		} else {
			account, err = s.cache.GetAccount(ctx, accountID)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] account cache read failed: id=%d err=%v", accountID, err)
		} else if account != nil {
			s.storeHotAccount(account)
			return cloneSchedulerHotAccount(account), nil
		}
	}

	if err := s.guardFallback(ctx); err != nil {
		return nil, err
	}
	fallbackCtx, cancel := s.withFallbackTimeout(ctx)
	defer cancel()
	return s.accountRepo.GetByID(fallbackCtx, accountID)
}

// GetAccountConcurrencyLimits 在严格优先级整层满载的异常路径中批量复核并发上限。
// 正常选号不会调用；singleflight（并发合并）会合并同一波满载请求，但不保留
// 过期值，确保管理员刚提高并发上限后不会错误降到低优先级层。
func (s *SchedulerSnapshotService) GetAccountConcurrencyLimits(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	uniqueIDs := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		uniqueIDs = append(uniqueIDs, accountID)
	}
	if len(uniqueIDs) == 0 {
		return map[int64]int{}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	sort.Slice(uniqueIDs, func(i, j int) bool { return uniqueIDs[i] < uniqueIDs[j] })
	var key strings.Builder
	key.WriteString("account-concurrency:")
	for index, accountID := range uniqueIDs {
		if index > 0 {
			key.WriteByte(',')
		}
		key.WriteString(strconv.FormatInt(accountID, 10))
	}

	value, err, _ := s.hotReadSF.Do(key.String(), func() (any, error) {
		switch cache := s.cache.(type) {
		case SchedulerAccountConcurrencyBatchCache:
			return cache.GetAccountConcurrencyLimits(ctx, uniqueIDs)
		case nil:
			return map[int64]int{}, nil
		default:
			// 非 Redis 测试替身若未实现批量能力，宁可跳过一致性复核，也不能
			// 回退为 N 次单账号读取，破坏大账号池优化的性能边界。
			if s.accountRepo == nil {
				return map[int64]int{}, nil
			}
			accounts, readErr := s.accountRepo.GetByIDs(ctx, uniqueIDs)
			if readErr != nil {
				return nil, readErr
			}
			limits := make(map[int64]int, len(accounts))
			for _, account := range accounts {
				if account != nil && account.ID > 0 {
					limits[account.ID] = account.Concurrency
				}
			}
			return limits, nil
		}
	})
	if err != nil {
		return nil, err
	}
	limits, ok := value.(map[int64]int)
	if !ok {
		return nil, ErrSchedulerCacheNotReady
	}
	return limits, nil
}

// GetGroupByID 获取分组信息（供调度器使用）
func (s *SchedulerSnapshotService) GetGroupByID(ctx context.Context, groupID int64) (*Group, error) {
	if s.isRunModeGatewayLite() {
		if groupCache, ok := s.cache.(SchedulerGroupCache); ok {
			if group, hit := s.getHotGroup(groupID); hit {
				return group, nil
			}
			value, err, _ := s.hotReadSF.Do("group:"+strconv.FormatInt(groupID, 10), func() (any, error) {
				if group, hit := s.getHotGroup(groupID); hit {
					return group, nil
				}
				group, cacheErr := groupCache.GetGroup(ctx, groupID)
				if cacheErr == nil && group != nil {
					return group, nil
				}
				if s.groupRepo == nil {
					return nil, cacheErr
				}

				// Redis 重启、历史漏写或人工清理可能只丢失分组元数据。只在缓存异常路径
				// 回源一次数据库并立即修复 Redis；singleflight 防止并发请求击穿数据库。
				fallbackCtx, cancel := s.withFallbackTimeout(ctx)
				defer cancel()
				fallbackGroup, fallbackErr := s.groupRepo.GetByIDLite(fallbackCtx, groupID)
				if fallbackErr != nil {
					return nil, fallbackErr
				}
				if fallbackGroup == nil {
					return nil, cacheErr
				}
				if setErr := groupCache.SetGroup(fallbackCtx, fallbackGroup); setErr != nil {
					slog.Warn("failed to repair scheduler group cache", "group_id", groupID, "error", setErr)
				}
				return fallbackGroup, nil
			})
			if err != nil || value == nil {
				return nil, err
			}
			group, _ := value.(*Group)
			if group == nil {
				return nil, nil
			}
			s.storeHotGroup(group)
			cloned := *group
			return &cloned, nil
		}
		return nil, nil
	}
	if s.groupRepo == nil {
		return nil, nil
	}
	return s.groupRepo.GetByID(ctx, groupID)
}

// ListActiveGroups 返回当前调度快照中的活跃分组。关键词命中属于低频异常路径，
// 这里复用已有分组缓存读取；普通请求不会调用，也不会增加热路径开销。
func (s *SchedulerSnapshotService) ListActiveGroups(ctx context.Context) ([]Group, error) {
	if s == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	ids, err := s.listActiveSchedulerGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(ids))
	for _, id := range ids {
		group, getErr := s.GetGroupByID(ctx, id)
		if getErr != nil || group == nil || !group.IsActive() {
			continue
		}
		out = append(out, *group)
	}
	return out, nil
}

// UpdateAccountInCache 立即更新 Redis 中单个账号的数据（用于模型限流后立即生效）
func (s *SchedulerSnapshotService) UpdateAccountInCache(ctx context.Context, account *Account) error {
	if s.cache == nil || account == nil {
		return nil
	}
	if err := s.cache.SetAccount(ctx, account); err != nil {
		return err
	}
	s.storeHotAccount(account)
	// 账号状态、并发上限或分组绑定变化后，候选桶必须在下一请求重新取权威快照。
	s.clearSchedulerHotSnapshots()
	return nil
}

func (s *SchedulerSnapshotService) runInitialRebuild() {
	if s.cache == nil {
		return
	}
	_ = s.coalesceFullRebuild(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.rebuildFullSnapshot(ctx, "startup"); err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] rebuild startup failed: %v", err)
			return err
		}
		return nil
	})
}

func (s *SchedulerSnapshotService) runOutboxWorker(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.pollOutbox()
	for {
		select {
		case <-ticker.C:
			s.pollOutbox()
		case <-s.stopCh:
			return
		}
	}
}

func (s *SchedulerSnapshotService) runFullRebuildWorker(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.triggerFullRebuild("interval"); err != nil {
				logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] full rebuild failed: %v", err)
			}
		case <-s.stopCh:
			return
		}
	}
}

func (s *SchedulerSnapshotService) pollOutbox() {
	if s.outboxRepo == nil || s.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	watermark, err := s.cache.GetOutboxWatermark(ctx)
	if err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox watermark read failed: %v", err)
		return
	}

	events, err := s.outboxRepo.ListAfterAndReleaseDedup(ctx, watermark, 200)
	if err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox poll failed: %v", err)
		return
	}
	if len(events) == 0 {
		s.clearOutboxDegradedEpisode()
		return
	}

	seen := make(map[batchSeenKey]struct{})
	for _, event := range events {
		eventCtx, cancel := context.WithTimeout(context.Background(), outboxEventTimeout)
		err := s.handleOutboxEvent(eventCtx, event, seen)
		cancel()
		if err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox handle failed: id=%d type=%s err=%v", event.ID, event.EventType, err)
			return
		}
	}

	lastID := events[len(events)-1].ID
	var wmErr error
	for i := range 3 {
		wmCtx, wmCancel := context.WithTimeout(context.Background(), 5*time.Second)
		wmErr = s.cache.SetOutboxWatermark(wmCtx, lastID)
		wmCancel()
		if wmErr == nil {
			break
		}
		if i < 2 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if wmErr != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox watermark write failed: %v", wmErr)
	} else {
		s.cleanupConsumedOutbox(lastID)
		lagCtx, lagCancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.checkOutboxLag(lagCtx, lastID)
		lagCancel()
	}
}

func (s *SchedulerSnapshotService) cleanupConsumedOutbox(watermark int64) {
	if s == nil || s.outboxRepo == nil || watermark <= 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	lease, acquired, err := s.outboxRepo.TryAcquireCleanupLock(ctx)
	if err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox cleanup lock failed: %v", err)
		return
	}
	if !acquired {
		return
	}
	defer lease.Release()

	for {
		deleted, err := s.outboxRepo.DeleteConsumedUpTo(ctx, watermark, schedulerOutboxCleanupBatch)
		if err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox cleanup failed: watermark=%d err=%v", watermark, err)
			return
		}
		if deleted == 0 || deleted < schedulerOutboxCleanupBatch {
			return
		}
	}
}

func (s *SchedulerSnapshotService) handleOutboxEvent(ctx context.Context, event SchedulerOutboxEvent, seen map[batchSeenKey]struct{}) error {
	switch event.EventType {
	case SchedulerOutboxEventAccountLastUsed:
		return s.handleLastUsedEvent(ctx, event.Payload)
	case SchedulerOutboxEventAccountBulkChanged:
		return s.handleBulkAccountEvent(ctx, event.Payload, seen)
	case SchedulerOutboxEventAccountGroupsChanged:
		return s.handleAccountEvent(ctx, event.AccountID, event.Payload, seen)
	case SchedulerOutboxEventAccountChanged:
		return s.handleAccountEvent(ctx, event.AccountID, event.Payload, seen)
	case SchedulerOutboxEventGroupChanged:
		return s.handleGroupEvent(ctx, event.GroupID, event.Payload, seen)
	case SchedulerOutboxEventFullRebuild:
		return s.triggerFullRebuild("outbox")
	default:
		return nil
	}
}

func (s *SchedulerSnapshotService) handleLastUsedEvent(ctx context.Context, payload map[string]any) error {
	if s.cache == nil || payload == nil {
		return nil
	}
	raw, ok := payload["last_used"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	updates := make(map[int64]time.Time, len(raw))
	for key, value := range raw {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		sec, ok := toInt64(value)
		if !ok || sec <= 0 {
			continue
		}
		updates[id] = time.Unix(sec, 0)
	}
	if len(updates) == 0 {
		return nil
	}
	return s.cache.UpdateLastUsed(ctx, updates)
}

func (s *SchedulerSnapshotService) handleBulkAccountEvent(ctx context.Context, payload map[string]any, seen map[batchSeenKey]struct{}) error {
	if payload == nil {
		return nil
	}
	if s.accountRepo == nil {
		return nil
	}

	rawIDs := parseInt64Slice(payload["account_ids"])
	if len(rawIDs) == 0 {
		return nil
	}

	ids := make([]int64, 0, len(rawIDs))
	seenIDs := make(map[int64]struct{}, len(rawIDs))
	for _, id := range rawIDs {
		if id <= 0 {
			continue
		}
		if _, exists := seenIDs[id]; exists {
			continue
		}
		seenIDs[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	s.clearSchedulerHotAccountsAndSnapshots()

	preloadGroupIDs := parseInt64Slice(payload["group_ids"])
	accounts, err := s.accountRepo.GetByIDs(ctx, ids)
	if err != nil {
		return err
	}

	found := make(map[int64]struct{}, len(accounts))
	rebuildGroupSet := make(map[int64]struct{}, len(preloadGroupIDs))
	for _, gid := range preloadGroupIDs {
		if gid > 0 {
			rebuildGroupSet[gid] = struct{}{}
		}
	}

	for _, account := range accounts {
		if account == nil || account.ID <= 0 {
			continue
		}
		found[account.ID] = struct{}{}
		if s.cache != nil {
			if err := s.cache.SetAccount(ctx, account); err != nil {
				return err
			}
		}
		for _, gid := range account.GroupIDs {
			if gid > 0 {
				rebuildGroupSet[gid] = struct{}{}
			}
		}
	}

	allAccountsFound := true
	for _, id := range ids {
		if _, ok := found[id]; ok {
			continue
		}
		allAccountsFound = false
		if s.cache != nil {
			if err := s.cache.DeleteAccount(ctx, id); err != nil {
				return err
			}
		}
	}

	rebuildGroupIDs := make([]int64, 0, len(rebuildGroupSet))
	for gid := range rebuildGroupSet {
		rebuildGroupIDs = append(rebuildGroupIDs, gid)
	}

	// 缺失账号无法确定其旧平台，必须保守重建全部平台，避免留下旧快照。
	if !allAccountsFound {
		return s.rebuildByGroupIDs(ctx, rebuildGroupIDs, "account_bulk_change", seen)
	}

	platformGroupSets := make(map[string]map[int64]struct{}, len(accounts))
	addPlatformGroups := func(platform string, groupIDs []int64) {
		groupSet := platformGroupSets[platform]
		if groupSet == nil {
			groupSet = make(map[int64]struct{}, len(groupIDs))
			platformGroupSets[platform] = groupSet
		}
		for _, groupID := range groupIDs {
			groupSet[groupID] = struct{}{}
		}
	}
	for _, account := range accounts {
		if account == nil || account.ID <= 0 {
			continue
		}
		accountGroupIDs := s.normalizeGroupIDs(account.GroupIDs)
		switch account.Platform {
		case PlatformAnthropic, PlatformGemini, PlatformOpenAI, PlatformGrok,
			PlatformKimi, PlatformZhipu, PlatformDeepseek:
			addPlatformGroups(account.Platform, accountGroupIDs)
		case PlatformAntigravity:
			// fresh 数据可能刚关闭 mixed_scheduling；兼容平台仍需重建以清理旧快照。
			addPlatformGroups(PlatformAntigravity, accountGroupIDs)
			addPlatformGroups(PlatformAnthropic, accountGroupIDs)
			addPlatformGroups(PlatformGemini, accountGroupIDs)
		default:
			return s.rebuildByGroupIDs(ctx, rebuildGroupIDs, "account_bulk_change", seen)
		}
	}

	// payload 保存的是更新前分组。只扩散到本事件涉及的平台，避免平台间交叉重建。
	if len(preloadGroupIDs) > 0 {
		preloadGroupIDs = s.normalizeGroupIDs(preloadGroupIDs)
		for platform := range platformGroupSets {
			addPlatformGroups(platform, preloadGroupIDs)
		}
	}

	bucketCapacity := 0
	for _, groupSet := range platformGroupSets {
		bucketCapacity += len(groupSet) * 3
	}
	buckets := make([]SchedulerBucket, 0, bucketCapacity)
	for _, platform := range schedulerSnapshotPlatforms() {
		groupSet, ok := platformGroupSets[platform]
		if !ok {
			continue
		}
		platformGroupIDs := make([]int64, 0, len(groupSet))
		for groupID := range groupSet {
			platformGroupIDs = append(platformGroupIDs, groupID)
		}
		sort.Slice(platformGroupIDs, func(i, j int) bool { return platformGroupIDs[i] < platformGroupIDs[j] })
		buckets = append(buckets, s.bucketsForPlatform(platform, platformGroupIDs, seen)...)
	}
	return s.rebuildBuckets(ctx, buckets, "account_bulk_change")
}

func (s *SchedulerSnapshotService) handleAccountEvent(ctx context.Context, accountID *int64, payload map[string]any, seen map[batchSeenKey]struct{}) error {
	if accountID == nil || *accountID <= 0 {
		return nil
	}
	if s.accountRepo == nil {
		return nil
	}
	s.invalidateSchedulerHotAccount(*accountID)

	var groupIDs []int64
	if payload != nil {
		groupIDs = parseInt64Slice(payload["group_ids"])
	}

	account, err := s.accountRepo.GetByID(ctx, *accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			if s.cache != nil {
				if err := s.cache.DeleteAccount(ctx, *accountID); err != nil {
					return err
				}
			}
			return s.rebuildByGroupIDs(ctx, groupIDs, "account_miss", seen)
		}
		return err
	}
	if s.cache != nil {
		if err := s.cache.SetAccount(ctx, account); err != nil {
			return err
		}
	}
	if len(groupIDs) == 0 {
		groupIDs = account.GroupIDs
	}
	return s.rebuildByAccount(ctx, account, groupIDs, "account_change", seen)
}

func (s *SchedulerSnapshotService) handleGroupEvent(ctx context.Context, groupID *int64, payload map[string]any, seen map[batchSeenKey]struct{}) error {
	if groupID == nil || *groupID <= 0 {
		return nil
	}
	if lifecycleCache, ok := s.cache.(SchedulerBucketLifecycleCache); ok && !s.isRunModeSimple() && s.groupRepo != nil {
		if err := s.syncSchedulerGroupCache(ctx, *groupID); err != nil {
			return err
		}
		if seen != nil {
			if _, ok := seen[batchSeenKey{groupID: *groupID, lifecycle: true}]; ok {
				if resource, action, ok := schedulerGroupConfigChangeFromPayload(payload); ok {
					gatewaylite.NotifyDefaultConfigChanged(resource, action, *groupID)
				}
				return nil
			}
		}
		if err := s.reconcileGroupLifecycle(ctx, lifecycleCache, *groupID, seen); err != nil {
			return err
		}
		if resource, action, ok := schedulerGroupConfigChangeFromPayload(payload); ok {
			gatewaylite.NotifyDefaultConfigChanged(resource, action, *groupID)
		}
		return nil
	}

	// gateway-lite（轻量网关）已经把本地分组作为调度权威，因此 group_changed
	// 不能只重建账号桶，还必须同步分组元数据缓存。否则新建分组在数据库中存在，
	// 但固定分组 Key 会被热路径误判为 group not found；删除分组则可能残留旧元数据。
	if err := s.syncSchedulerGroupCache(ctx, *groupID); err != nil {
		return err
	}
	groupIDs := []int64{*groupID}
	if err := s.rebuildByGroupIDs(ctx, groupIDs, "group_change", seen); err != nil {
		return err
	}
	if resource, action, ok := schedulerGroupConfigChangeFromPayload(payload); ok {
		gatewaylite.NotifyDefaultConfigChanged(resource, action, *groupID)
	}
	return nil
}

func (s *SchedulerSnapshotService) syncSchedulerGroupCache(ctx context.Context, groupID int64) error {
	groupCache, ok := s.cache.(SchedulerGroupCache)
	if !ok || s.groupRepo == nil || groupID <= 0 {
		return nil
	}
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if errors.Is(err, ErrGroupNotFound) {
		if deleteErr := groupCache.DeleteGroup(ctx, groupID); deleteErr != nil {
			return deleteErr
		}
		s.hotMu.Lock()
		delete(s.hotGroups, groupID)
		s.hotMu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	if group != nil {
		if setErr := groupCache.SetGroup(ctx, group); setErr != nil {
			return setErr
		}
		s.storeHotGroup(group)
		return nil
	}
	return nil
}

func (s *SchedulerSnapshotService) reconcileGroupLifecycle(ctx context.Context, lifecycleCache SchedulerBucketLifecycleCache, groupID int64, seen map[batchSeenKey]struct{}) error {
	plan, err := s.prepareGroupLifecycle(ctx, lifecycleCache, groupID, nil)
	if err != nil {
		return err
	}
	if plan.active {
		queries := newSchedulerAccountQueryCache(plan.tasks)
		for _, task := range plan.tasks {
			if err := s.rebuildBucketWithTokenPolicyAndQueryCache(ctx, lifecycleCache, task, "group_change", true, queries); err != nil {
				return err
			}
		}
	}
	markGroupLifecycleSeen(seen, groupID)
	return nil
}

func (s *SchedulerSnapshotService) prepareGroupLifecycle(ctx context.Context, lifecycleCache SchedulerBucketLifecycleCache, groupID int64, knownHistorical []SchedulerBucket) (plan schedulerGroupLifecyclePlan, retErr error) {
	if groupID <= 0 || s.isRunModeSimple() {
		return schedulerGroupLifecyclePlan{}, nil
	}
	if lifecycleCache == nil || s.groupRepo == nil {
		return schedulerGroupLifecyclePlan{}, ErrSchedulerCacheNotReady
	}

	lifecycleCtx, cancel := context.WithTimeout(ctx, schedulerGroupLifecycleTimeout)
	defer cancel()
	lease, acquired, err := lifecycleCache.TryAcquireGroupLifecycleLease(lifecycleCtx, groupID, schedulerGroupLifecycleLeaseTTL)
	if err != nil {
		return schedulerGroupLifecyclePlan{}, err
	}
	if !acquired {
		return schedulerGroupLifecyclePlan{}, fmt.Errorf("%w: group=%d", ErrSchedulerGroupLifecycleLeaseBusy, groupID)
	}
	leaseHeld := true
	defer func() {
		if leaseHeld {
			retErr = errors.Join(retErr, s.releaseGroupLifecycleLease(lifecycleCache, lease))
		}
	}()

	group, err := s.groupRepo.GetByIDLite(lifecycleCtx, groupID)
	missing := errors.Is(err, ErrGroupNotFound)
	if err != nil && !missing {
		return schedulerGroupLifecyclePlan{}, err
	}
	if err == nil && (group == nil || group.ID != groupID || !group.Hydrated) {
		return schedulerGroupLifecyclePlan{}, fmt.Errorf("untrusted scheduler group lifecycle state: group=%d", groupID)
	}

	plan = schedulerGroupLifecyclePlan{active: !missing && group.IsActive()}
	if plan.active {
		buckets := schedulerBucketsForGroup(groupID)
		plan.tasks = make([]schedulerBucketWriteTask, 0, len(buckets))
		for _, bucket := range buckets {
			token, err := lifecycleCache.ReopenBucket(lifecycleCtx, bucket)
			if err != nil {
				return schedulerGroupLifecyclePlan{}, err
			}
			plan.tasks = append(plan.tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
		}
	} else {
		registered := knownHistorical
		if registered == nil {
			registered, err = s.cache.ListBuckets(lifecycleCtx)
			if err != nil {
				return schedulerGroupLifecyclePlan{}, err
			}
		}
		buckets := schedulerBucketsForGroup(groupID)
		for _, bucket := range registered {
			if bucket.GroupID == groupID {
				buckets = append(buckets, bucket)
			}
		}
		for _, bucket := range dedupeBuckets(buckets) {
			if err := lifecycleCache.RetireBucket(lifecycleCtx, bucket); err != nil {
				return schedulerGroupLifecyclePlan{}, err
			}
		}
	}

	releaseErr := s.releaseGroupLifecycleLease(lifecycleCache, lease)
	leaseHeld = false
	if releaseErr != nil {
		return schedulerGroupLifecyclePlan{}, releaseErr
	}
	return plan, nil
}

func (s *SchedulerSnapshotService) releaseGroupLifecycleLease(lifecycleCache SchedulerBucketLifecycleCache, lease SchedulerGroupLifecycleLease) error {
	releaseCtx, cancel := context.WithTimeout(context.Background(), schedulerGroupLifecycleReleaseTimeout)
	defer cancel()
	return lifecycleCache.ReleaseGroupLifecycleLease(releaseCtx, lease)
}

func markGroupLifecycleSeen(seen map[batchSeenKey]struct{}, groupID int64) {
	if seen == nil {
		return
	}
	seen[batchSeenKey{groupID: groupID, lifecycle: true}] = struct{}{}
	for _, platform := range schedulerSnapshotPlatforms() {
		seen[batchSeenKey{groupID: groupID, platform: platform}] = struct{}{}
	}
}

func schedulerGroupConfigChangeFromPayload(payload map[string]any) (resource, action string, ok bool) {
	resource, _ = payload["config_change_resource"].(string)
	action, _ = payload["config_change_action"].(string)
	resource = strings.TrimSpace(resource)
	action = strings.TrimSpace(action)
	return resource, action, resource != "" && action != ""
}

func (s *SchedulerSnapshotService) rebuildByAccount(ctx context.Context, account *Account, groupIDs []int64, reason string, seen map[batchSeenKey]struct{}) error {
	if account == nil {
		return nil
	}
	groupIDs = s.normalizeGroupIDs(groupIDs)
	if len(groupIDs) == 0 {
		return nil
	}

	buckets := s.bucketsForPlatform(account.Platform, groupIDs, seen)
	if account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled() {
		buckets = append(buckets, s.bucketsForPlatform(PlatformAnthropic, groupIDs, seen)...)
		buckets = append(buckets, s.bucketsForPlatform(PlatformGemini, groupIDs, seen)...)
	}
	return s.rebuildBuckets(ctx, buckets, reason)
}

func schedulerSnapshotPlatforms() [8]string {
	return [8]string{
		PlatformAnthropic, PlatformGemini, PlatformOpenAI, PlatformAntigravity,
		PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek,
	}
}

func schedulerBucketsForGroup(groupID int64) []SchedulerBucket {
	if groupID <= 0 {
		return nil
	}
	return schedulerCanonicalBuckets(groupID)
}

func schedulerCanonicalBuckets(groupID int64) []SchedulerBucket {
	buckets := make([]SchedulerBucket, 0, 18)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets,
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle},
			SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeForced},
		)
		if platform == PlatformAnthropic || platform == PlatformGemini {
			buckets = append(buckets, SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeMixed})
		}
	}
	return buckets
}

func (s *SchedulerSnapshotService) rebuildByGroupIDs(ctx context.Context, groupIDs []int64, reason string, seen map[batchSeenKey]struct{}) error {
	groupIDs = s.normalizeGroupIDs(groupIDs)
	if len(groupIDs) == 0 {
		return nil
	}
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*18)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets, s.bucketsForPlatform(platform, groupIDs, seen)...)
	}
	return s.rebuildBuckets(ctx, buckets, reason)
}

func (s *SchedulerSnapshotService) bucketsForPlatform(platform string, groupIDs []int64, seen map[batchSeenKey]struct{}) []SchedulerBucket {
	if platform == "" {
		return nil
	}
	buckets := make([]SchedulerBucket, 0, len(groupIDs)*3)
	for _, gid := range groupIDs {
		// Within a single poll batch, skip (groupID, platform) pairs that were
		// already rebuilt. The first rebuild loads fresh DB data for all accounts
		// in the group, so subsequent rebuilds for the same group+platform within
		// the same batch are redundant.
		if seen != nil {
			key := batchSeenKey{groupID: gid, platform: platform}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		buckets = append(buckets, SchedulerBucket{GroupID: gid, Platform: platform, Mode: SchedulerModeSingle})
		buckets = append(buckets, SchedulerBucket{GroupID: gid, Platform: platform, Mode: SchedulerModeForced})
		if platform == PlatformAnthropic || platform == PlatformGemini {
			buckets = append(buckets, SchedulerBucket{GroupID: gid, Platform: platform, Mode: SchedulerModeMixed})
		}
	}
	return buckets
}

func (s *SchedulerSnapshotService) rebuildBuckets(ctx context.Context, buckets []SchedulerBucket, reason string) error {
	if lifecycleCache, ok := s.cache.(SchedulerBucketLifecycleCache); ok {
		tasks, firstErr := s.prepareBucketWriteTasks(ctx, lifecycleCache, buckets)
		queries := newSchedulerAccountQueryCache(tasks)
		if err := s.rebuildPreparedBucketTasks(ctx, lifecycleCache, tasks, reason, false, queries); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	var firstErr error
	for _, bucket := range buckets {
		if err := s.rebuildBucket(ctx, bucket, reason); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *SchedulerSnapshotService) prepareBucketWriteTasks(ctx context.Context, lifecycleCache SchedulerBucketLifecycleCache, buckets []SchedulerBucket) ([]schedulerBucketWriteTask, error) {
	if lifecycleCache == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	tasks := make([]schedulerBucketWriteTask, 0, len(buckets))
	var firstErr error
	for _, bucket := range buckets {
		token, err := lifecycleCache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
			if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		tasks = append(tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
	}
	return tasks, firstErr
}

func (s *SchedulerSnapshotService) rebuildPreparedBucketTasks(
	ctx context.Context,
	lifecycleCache SchedulerBucketLifecycleCache,
	tasks []schedulerBucketWriteTask,
	reason string,
	strict bool,
	queries *schedulerAccountQueryCache,
) error {
	var firstErr error
	for _, task := range tasks {
		if err := s.rebuildBucketWithTokenPolicyAndQueryCache(ctx, lifecycleCache, task, reason, strict, queries); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *SchedulerSnapshotService) rebuildBucketWithTokenPolicyAndQueryCache(
	ctx context.Context,
	lifecycleCache SchedulerBucketLifecycleCache,
	task schedulerBucketWriteTask,
	reason string,
	strict bool,
	queries *schedulerAccountQueryCache,
) error {
	if queries != nil {
		defer queries.release(task.bucket)
	}
	if lifecycleCache == nil {
		return ErrSchedulerCacheNotReady
	}
	bucket := task.bucket
	ok, err := s.cache.TryLockBucket(ctx, bucket, 30*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		if strict {
			return fmt.Errorf("%w: bucket=%s", ErrSchedulerBucketRebuildBusy, bucket.String())
		}
		return nil
	}
	defer func() {
		_ = s.cache.UnlockBucket(ctx, bucket)
	}()

	rebuildCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	accounts, err := s.loadAccountsForRebuild(rebuildCtx, bucket, queries)
	if err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] rebuild failed: bucket=%s reason=%s err=%v", bucket.String(), reason, err)
		return err
	}
	if err := s.setRebuildSnapshot(rebuildCtx, lifecycleCache, task, accounts, queries); err != nil {
		if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
			slog.Debug("[Scheduler] rebuild fenced", "bucket", bucket.String(), "reason", reason)
			if strict {
				return err
			}
			return nil
		}
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] rebuild cache failed: bucket=%s reason=%s err=%v", bucket.String(), reason, err)
		return err
	}
	slog.Debug("[Scheduler] rebuild ok", "bucket", bucket.String(), "reason", reason, "size", len(accounts))
	return nil
}

func (s *SchedulerSnapshotService) setRebuildSnapshot(
	ctx context.Context,
	lifecycleCache SchedulerBucketLifecycleCache,
	task schedulerBucketWriteTask,
	accounts []Account,
	queries *schedulerAccountQueryCache,
) error {
	writer, ok := s.cache.(schedulerSnapshotAccountIDWriter)
	key, reusable := schedulerAccountQueryKeyForBucket(task.bucket)
	if !ok || queries == nil || !reusable {
		return lifecycleCache.SetSnapshotWithToken(ctx, task.bucket, task.token, accounts)
	}

	if accountIDs, exists := queries.snapshotAccountIDs[key]; exists {
		return writer.SetSnapshotByAccountIDs(ctx, task.bucket, task.token, accountIDs)
	}
	if queries.remaining[key] <= 1 {
		return lifecycleCache.SetSnapshotWithToken(ctx, task.bucket, task.token, accounts)
	}

	accountIDs, err := writer.SetSnapshotAndReturnAccountIDs(ctx, task.bucket, task.token, accounts)
	if err != nil {
		return err
	}
	if queries.remaining[key] > 1 {
		// 只能复用首次写入实际接受的有序 ID；重新从原切片推导会让不可编码账号
		// 在后续桶中重新出现，破坏同一查询对应快照的成员一致性。
		queries.snapshotAccountIDs[key] = accountIDs
	}
	return nil
}

func (s *SchedulerSnapshotService) rebuildBucket(ctx context.Context, bucket SchedulerBucket, reason string) error {
	if lifecycleCache, ok := s.cache.(SchedulerBucketLifecycleCache); ok {
		token, err := lifecycleCache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
			if errors.Is(err, ErrSchedulerBucketRetired) || errors.Is(err, ErrSchedulerBucketWriteFenced) {
				slog.Debug("[Scheduler] rebuild fenced", "bucket", bucket.String(), "reason", reason)
				return nil
			}
			return err
		}
		return s.rebuildBucketWithTokenPolicyAndQueryCache(ctx, lifecycleCache, schedulerBucketWriteTask{bucket: bucket, token: token}, reason, false, nil)
	}
	if s.cache == nil {
		return ErrSchedulerCacheNotReady
	}
	ok, err := s.cache.TryLockBucket(ctx, bucket, 30*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer func() {
		_ = s.cache.UnlockBucket(ctx, bucket)
	}()

	rebuildCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	accounts, err := s.loadAccountsFromDB(rebuildCtx, bucket, bucket.Mode == SchedulerModeMixed)
	if err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] rebuild failed: bucket=%s reason=%s err=%v", bucket.String(), reason, err)
		return err
	}
	if err := s.cache.SetSnapshot(rebuildCtx, bucket, accounts); err != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] rebuild cache failed: bucket=%s reason=%s err=%v", bucket.String(), reason, err)
		return err
	}
	slog.Debug("[Scheduler] rebuild ok", "bucket", bucket.String(), "reason", reason, "size", len(accounts))
	return nil
}

func (s *SchedulerSnapshotService) triggerFullRebuild(reason string) error {
	if s.cache == nil {
		return ErrSchedulerCacheNotReady
	}
	return s.coalesceFullRebuild(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return s.rebuildFullSnapshot(ctx, reason)
	})
}

func (s *SchedulerSnapshotService) rebuildFullSnapshot(ctx context.Context, reason string) error {
	if s.cache == nil {
		return ErrSchedulerCacheNotReady
	}
	lifecycleCache, hasLifecycle := s.cache.(SchedulerBucketLifecycleCache)
	if !hasLifecycle || (!s.isRunModeSimple() && s.groupRepo == nil) {
		buckets, err := s.cache.ListBuckets(ctx)
		if err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] list buckets failed: %v", err)
		}
		if len(buckets) == 0 {
			buckets, err = s.defaultBuckets(ctx)
			if err != nil {
				logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] default buckets failed: %v", err)
				return err
			}
		}
		return s.rebuildBuckets(ctx, buckets, reason)
	}

	registered, err := s.cache.ListBuckets(ctx)
	if err != nil {
		return err
	}
	registered = dedupeBuckets(registered)

	if s.isRunModeSimple() {
		buckets := dedupeBuckets(append(schedulerCanonicalBuckets(0), registered...))
		tasks, firstErr := s.prepareBucketWriteTasks(ctx, lifecycleCache, buckets)
		queries := newSchedulerAccountQueryCache(tasks)
		if err := s.rebuildPreparedBucketTasks(ctx, lifecycleCache, tasks, reason, false, queries); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}

	activeGroupIDs, err := s.listActiveSchedulerGroupIDs(ctx)
	if err != nil {
		return err
	}
	activeSet := make(map[int64]struct{}, len(activeGroupIDs))
	for _, groupID := range activeGroupIDs {
		activeSet[groupID] = struct{}{}
	}

	registeredByGroup := make(map[int64][]SchedulerBucket)
	for _, bucket := range registered {
		registeredByGroup[bucket.GroupID] = append(registeredByGroup[bucket.GroupID], bucket)
	}

	capturedBuckets := dedupeBuckets(append(schedulerCanonicalBuckets(0), registeredByGroup[0]...))
	for groupID, buckets := range registeredByGroup {
		if groupID < 0 {
			capturedBuckets = append(capturedBuckets, buckets...)
		}
	}
	captured, firstErr := s.prepareBucketWriteTasks(ctx, lifecycleCache, capturedBuckets)
	if firstErr != nil {
		return firstErr
	}

	reopened := make([]schedulerBucketWriteTask, 0)
	for _, groupID := range activeGroupIDs {
		plan, err := s.prepareGroupLifecycle(ctx, lifecycleCache, groupID, registeredByGroup[groupID])
		if err != nil {
			return err
		}
		if plan.active {
			reopened = append(reopened, plan.tasks...)
			captured = append(captured, mustCaptureHistoricalBuckets(ctx, lifecycleCache, registeredByGroup[groupID], schedulerBucketsForGroup(groupID))...)
		}
	}

	staleGroupIDs := make([]int64, 0)
	for groupID := range registeredByGroup {
		if groupID <= 0 {
			continue
		}
		if _, active := activeSet[groupID]; !active {
			staleGroupIDs = append(staleGroupIDs, groupID)
		}
	}
	sort.Slice(staleGroupIDs, func(i, j int) bool { return staleGroupIDs[i] < staleGroupIDs[j] })
	for _, groupID := range staleGroupIDs {
		plan, err := s.prepareGroupLifecycle(ctx, lifecycleCache, groupID, registeredByGroup[groupID])
		if err != nil {
			return err
		}
		if plan.active {
			reopened = append(reopened, plan.tasks...)
		}
	}

	queries := newSchedulerAccountQueryCache(reopened, captured)
	if err := s.rebuildPreparedBucketTasks(ctx, lifecycleCache, reopened, reason, true, queries); err != nil {
		firstErr = err
	}
	if err := s.rebuildPreparedBucketTasks(ctx, lifecycleCache, captured, reason, false, queries); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func mustCaptureHistoricalBuckets(ctx context.Context, lifecycleCache SchedulerBucketLifecycleCache, registered []SchedulerBucket, canonical []SchedulerBucket) []schedulerBucketWriteTask {
	if len(registered) == 0 {
		return nil
	}
	canonicalSet := make(map[SchedulerBucket]struct{}, len(canonical))
	for _, bucket := range canonical {
		canonicalSet[bucket] = struct{}{}
	}
	tasks := make([]schedulerBucketWriteTask, 0)
	for _, bucket := range registered {
		if _, ok := canonicalSet[bucket]; ok {
			continue
		}
		token, err := lifecycleCache.CaptureBucketWriteToken(ctx, bucket)
		if err != nil {
			continue
		}
		tasks = append(tasks, schedulerBucketWriteTask{bucket: bucket, token: token})
	}
	return tasks
}

func (s *SchedulerSnapshotService) listActiveSchedulerGroupIDs(ctx context.Context) ([]int64, error) {
	if s.groupRepo == nil {
		return nil, nil
	}
	var groupIDs []int64
	if lister, ok := s.groupRepo.(schedulerActiveGroupIDLister); ok {
		ids, err := lister.ListActiveIDs(ctx)
		if err != nil {
			return nil, err
		}
		groupIDs = ids
	} else {
		groups, err := s.groupRepo.ListActive(ctx)
		if err != nil {
			return nil, err
		}
		groupIDs = make([]int64, 0, len(groups))
		for _, group := range groups {
			groupIDs = append(groupIDs, group.ID)
		}
	}
	seen := make(map[int64]struct{}, len(groupIDs))
	normalized := make([]int64, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		normalized = append(normalized, groupID)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized, nil
}

// coalesceFullRebuild 把执行期间到达的重建请求合并为最多一轮尾随重建。
// 这样既避免并发重建风暴，也保证晚于当前事务提交的变更不会被当前轮错误覆盖。
func (s *SchedulerSnapshotService) coalesceFullRebuild(run func() error) error {
	s.fullRebuildStateMu.Lock()
	s.fullRebuildRequested++
	requestID := s.fullRebuildRequested
	s.fullRebuildStateMu.Unlock()

	s.fullRebuildRunMu.Lock()
	defer s.fullRebuildRunMu.Unlock()

	s.fullRebuildStateMu.Lock()
	if s.fullRebuildCompleted >= requestID {
		err := s.fullRebuildLastErr
		s.fullRebuildStateMu.Unlock()
		return err
	}
	coveredThrough := s.fullRebuildRequested
	s.fullRebuildStateMu.Unlock()

	err := run()

	s.fullRebuildStateMu.Lock()
	s.fullRebuildCompleted = coveredThrough
	s.fullRebuildLastErr = err
	s.fullRebuildStateMu.Unlock()
	return err
}

func (s *SchedulerSnapshotService) checkOutboxLag(ctx context.Context, watermark int64) {
	if s.cfg == nil || s.outboxRepo == nil {
		return
	}

	now := time.Now()
	var oldestCreatedAt time.Time
	var hasPending bool
	if reader, ok := s.outboxRepo.(schedulerOutboxPendingReader); ok {
		createdAt, ok, err := reader.FirstCreatedAtAfter(ctx, watermark)
		if err != nil {
			logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox pending event read failed: %v", err)
			return
		}
		oldestCreatedAt = createdAt
		hasPending = ok && !createdAt.IsZero()
	}
	var lag time.Duration
	if hasPending {
		lag = now.Sub(oldestCreatedAt)
	}
	lagSeconds := int(lag.Seconds())
	lagWarning := hasPending &&
		s.cfg.Gateway.Scheduling.OutboxLagWarnSeconds > 0 &&
		lagSeconds >= s.cfg.Gateway.Scheduling.OutboxLagWarnSeconds
	lagDegraded := hasPending &&
		s.cfg.Gateway.Scheduling.OutboxLagRebuildSeconds > 0 &&
		lagSeconds >= s.cfg.Gateway.Scheduling.OutboxLagRebuildSeconds

	backlogThreshold := s.cfg.Gateway.Scheduling.OutboxBacklogRebuildRows
	backlogKnown := true
	var backlog int64
	if backlogThreshold > 0 {
		maxID, err := s.outboxRepo.MaxID(ctx)
		if err != nil {
			backlogKnown = false
			if s.shouldLogOutboxMaxIDError(now) {
				logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox max id read failed: %v", err)
			}
		} else {
			backlog = maxID - watermark
		}
	}
	backlogDegraded := backlogKnown && backlogThreshold > 0 && backlog >= int64(backlogThreshold)

	logLagWarning := s.shouldLogOutboxLagWarning(lagWarning)
	s.lagMu.Lock()
	fullyRecovered := !lagDegraded && backlogKnown && !backlogDegraded
	if fullyRecovered {
		s.lagFailures = 0
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
	}

	if s.outboxRebuildRetryReason != "" {
		retryReasonActive := (s.outboxRebuildRetryReason == "outbox_lag" && lagDegraded) ||
			(s.outboxRebuildRetryReason == "outbox_backlog" && (!backlogKnown || backlogDegraded))
		if !retryReasonActive {
			s.outboxRebuildFailures = 0
			s.outboxRebuildRetryAt = time.Time{}
			s.outboxRebuildRetryReason = ""
		}
	}

	lagRetryPending := s.outboxRebuildRetryReason == "outbox_lag" && !s.outboxRebuildRetryAt.IsZero()
	if lagDegraded {
		if !s.outboxRebuildLatched && !s.outboxRebuildRunning && !lagRetryPending {
			s.lagFailures++
		}
	} else {
		s.lagFailures = 0
	}
	failures := s.lagFailures
	lagReady := lagDegraded && failures >= s.cfg.Gateway.Scheduling.OutboxLagRebuildFailures
	retryDue := s.outboxRebuildRetryReason != "" &&
		!s.outboxRebuildRetryAt.IsZero() && !now.Before(s.outboxRebuildRetryAt)

	reason := ""
	lagCanPreemptRetry := lagReady && s.outboxRebuildRetryReason != "outbox_lag"
	if !s.outboxRebuildLatched && !s.outboxRebuildRunning &&
		(s.outboxRebuildRetryAt.IsZero() || retryDue || lagCanPreemptRetry) {
		switch {
		case lagReady || (retryDue && s.outboxRebuildRetryReason == "outbox_lag" && lagDegraded):
			if s.outboxRebuildRetryReason != "" && s.outboxRebuildRetryReason != "outbox_lag" {
				s.outboxRebuildFailures = 0
				s.outboxRebuildRetryAt = time.Time{}
				s.outboxRebuildRetryReason = ""
			}
			reason = "outbox_lag"
			s.lagFailures = 0
		case backlogDegraded && (s.outboxRebuildRetryReason == "" ||
			(retryDue && s.outboxRebuildRetryReason == "outbox_backlog")):
			reason = "outbox_backlog"
		}
		if reason != "" {
			s.outboxRebuildRunning = true
		}
	}
	s.lagMu.Unlock()

	if logLagWarning {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox lag warning: %ds", lagSeconds)
	}

	if reason == "" {
		return
	}

	var rebuildErr error
	switch reason {
	case "outbox_lag":
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox lag rebuild triggered: lag=%s failures=%d", lag, failures)
		rebuildErr = s.triggerFullRebuild(reason)
	case "outbox_backlog":
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] outbox backlog rebuild triggered: backlog=%d", backlog)
		rebuildErr = s.triggerFullRebuild(reason)
	}

	s.lagMu.Lock()
	s.outboxRebuildRunning = false
	if rebuildErr == nil {
		s.outboxRebuildLatched = true
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
	} else {
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures++
		s.outboxRebuildRetryAt = time.Now().Add(outboxRebuildRetryDelay(s.outboxRebuildFailures))
		s.outboxRebuildRetryReason = reason
	}
	s.lagMu.Unlock()

	if rebuildErr != nil {
		logger.LegacyPrintf("service.scheduler_snapshot", "[Scheduler] %s rebuild failed: %v", reason, rebuildErr)
	}
}

func outboxRebuildRetryDelay(failures int) time.Duration {
	delay := outboxRebuildRetryBaseDelay
	for i := 1; i < failures && delay < outboxRebuildRetryMaxDelay; i++ {
		delay *= 2
		if delay >= outboxRebuildRetryMaxDelay {
			return outboxRebuildRetryMaxDelay
		}
	}
	return delay
}

func (s *SchedulerSnapshotService) clearOutboxDegradedEpisode() {
	if s == nil {
		return
	}
	s.lagMu.Lock()
	if s.lagFailures != 0 || s.outboxRebuildLatched || s.outboxRebuildRunning ||
		s.outboxRebuildFailures != 0 || !s.outboxRebuildRetryAt.IsZero() ||
		s.outboxRebuildRetryReason != "" || s.outboxLagWarningActive {
		s.lagFailures = 0
		s.outboxRebuildLatched = false
		s.outboxRebuildFailures = 0
		s.outboxRebuildRetryAt = time.Time{}
		s.outboxRebuildRetryReason = ""
		s.outboxLagWarningActive = false
	}
	s.lagMu.Unlock()
}

func (s *SchedulerSnapshotService) shouldLogOutboxMaxIDError(now time.Time) bool {
	s.lagMu.Lock()
	defer s.lagMu.Unlock()
	if !s.outboxMaxIDErrorLastLoggedAt.IsZero() && now.Sub(s.outboxMaxIDErrorLastLoggedAt) < outboxMaxIDErrorLogSampleInterval {
		return false
	}
	s.outboxMaxIDErrorLastLoggedAt = now
	return true
}

func (s *SchedulerSnapshotService) shouldLogOutboxLagWarning(active bool) bool {
	s.lagMu.Lock()
	defer s.lagMu.Unlock()
	shouldLog := active && !s.outboxLagWarningActive
	s.outboxLagWarningActive = active
	return shouldLog
}

func (s *SchedulerSnapshotService) loadAccountsFromDB(ctx context.Context, bucket SchedulerBucket, useMixed bool) ([]Account, error) {
	if s.accountRepo == nil {
		return nil, ErrSchedulerCacheNotReady
	}
	groupID := bucket.GroupID
	if s.isRunModeSimple() {
		groupID = 0
	}

	if useMixed {
		platforms := []string{bucket.Platform, PlatformAntigravity}
		var accounts []Account
		var err error
		if groupID > 0 {
			accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatforms(ctx, groupID, platforms)
		} else if s.isRunModeSimple() {
			accounts, err = s.accountRepo.ListSchedulableByPlatforms(ctx, platforms)
		} else {
			// gateway-lite 模式的 system recommended（系统推荐）调度桶代表该平台
			// 全量账号池。账号强制分组后，继续只查未分组账号会导致无账号可调度。
			accounts, err = s.accountRepo.ListSchedulableByPlatforms(ctx, platforms)
		}
		if err != nil {
			return nil, err
		}
		filtered := make([]Account, 0, len(accounts))
		for _, acc := range accounts {
			if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
				continue
			}
			filtered = append(filtered, acc)
		}
		return filtered, nil
	}

	if groupID > 0 {
		return s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, bucket.Platform)
	}
	if s.isRunModeSimple() {
		return s.accountRepo.ListSchedulableByPlatform(ctx, bucket.Platform)
	}
	// gateway-lite 模式的默认调度桶用于 system recommended（系统推荐）线路，
	// 必须包含已分组账号，否则“账号必须进分组”的生产配置下无法请求。
	return s.accountRepo.ListSchedulableByPlatform(ctx, bucket.Platform)
}

func (s *SchedulerSnapshotService) loadAccountsForRebuild(ctx context.Context, bucket SchedulerBucket, queries *schedulerAccountQueryCache) ([]Account, error) {
	key, cacheable := schedulerAccountQueryKeyForBucket(bucket)
	if queries == nil || !cacheable {
		return s.loadAccountsFromDB(ctx, bucket, bucket.Mode == SchedulerModeMixed)
	}
	if accounts, ok := queries.accounts[key]; ok {
		return accounts, nil
	}
	if queries.remaining[key] <= 1 {
		return s.loadAccountsFromDB(ctx, bucket, false)
	}
	accounts, err := s.loadAccountsFromDB(ctx, bucket, false)
	if err != nil {
		return nil, err
	}
	queries.accounts[key] = accounts
	return accounts, nil
}

func (s *SchedulerSnapshotService) bucketFor(groupID *int64, platform string, mode string) SchedulerBucket {
	return SchedulerBucket{
		GroupID:  s.normalizeGroupID(groupID),
		Platform: platform,
		Mode:     mode,
	}
}

func (s *SchedulerSnapshotService) normalizeGroupID(groupID *int64) int64 {
	if s.isRunModeSimple() {
		return 0
	}
	if groupID == nil || *groupID <= 0 {
		return 0
	}
	return *groupID
}

func (s *SchedulerSnapshotService) normalizeGroupIDs(groupIDs []int64) []int64 {
	if s.isRunModeSimple() {
		return []int64{0}
	}
	if len(groupIDs) == 0 {
		return []int64{0}
	}
	seen := make(map[int64]struct{}, len(groupIDs))
	out := make([]int64, 0, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return []int64{0}
	}
	return out
}

func (s *SchedulerSnapshotService) resolveMode(platform string, hasForcePlatform bool) string {
	if hasForcePlatform {
		return SchedulerModeForced
	}
	if platform == PlatformAnthropic || platform == PlatformGemini {
		return SchedulerModeMixed
	}
	return SchedulerModeSingle
}

func (s *SchedulerSnapshotService) guardFallback(ctx context.Context) error {
	if s.isRunModeGatewayLite() {
		return ErrSchedulerCacheNotReady
	}
	if s.cfg == nil || s.cfg.Gateway.Scheduling.DbFallbackEnabled {
		if s.fallbackLimit == nil || s.fallbackLimit.Allow() {
			return nil
		}
		return ErrSchedulerFallbackLimited
	}
	return ErrSchedulerCacheNotReady
}

func (s *SchedulerSnapshotService) withFallbackTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.cfg == nil || s.cfg.Gateway.Scheduling.DbFallbackTimeoutSeconds <= 0 {
		return context.WithCancel(ctx)
	}
	timeout := time.Duration(s.cfg.Gateway.Scheduling.DbFallbackTimeoutSeconds) * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithCancel(ctx)
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *SchedulerSnapshotService) isRunModeSimple() bool {
	return s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
}

func (s *SchedulerSnapshotService) isRunModeGatewayLite() bool {
	return s.cfg != nil && s.cfg.RunMode == config.RunModeGatewayLite
}

func (s *SchedulerSnapshotService) outboxPollInterval() time.Duration {
	if s.cfg == nil {
		return time.Second
	}
	sec := s.cfg.Gateway.Scheduling.OutboxPollIntervalSeconds
	if sec <= 0 {
		return time.Second
	}
	return time.Duration(sec) * time.Second
}

func (s *SchedulerSnapshotService) fullRebuildInterval() time.Duration {
	if s.cfg == nil {
		return 0
	}
	sec := s.cfg.Gateway.Scheduling.FullRebuildIntervalSeconds
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func (s *SchedulerSnapshotService) defaultBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	buckets := make([]SchedulerBucket, 0)
	for _, platform := range schedulerSnapshotPlatforms() {
		buckets = append(buckets, SchedulerBucket{GroupID: 0, Platform: platform, Mode: SchedulerModeSingle})
		buckets = append(buckets, SchedulerBucket{GroupID: 0, Platform: platform, Mode: SchedulerModeForced})
		if platform == PlatformAnthropic || platform == PlatformGemini {
			buckets = append(buckets, SchedulerBucket{GroupID: 0, Platform: platform, Mode: SchedulerModeMixed})
		}
	}

	if s.isRunModeSimple() || s.groupRepo == nil {
		return dedupeBuckets(buckets), nil
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return dedupeBuckets(buckets), nil
	}
	for _, group := range groups {
		if group.Platform == "" {
			continue
		}
		buckets = append(buckets, SchedulerBucket{GroupID: group.ID, Platform: group.Platform, Mode: SchedulerModeSingle})
		buckets = append(buckets, SchedulerBucket{GroupID: group.ID, Platform: group.Platform, Mode: SchedulerModeForced})
		if group.Platform == PlatformAnthropic || group.Platform == PlatformGemini {
			buckets = append(buckets, SchedulerBucket{GroupID: group.ID, Platform: group.Platform, Mode: SchedulerModeMixed})
		}
	}
	return dedupeBuckets(buckets), nil
}

func dedupeBuckets(in []SchedulerBucket) []SchedulerBucket {
	seen := make(map[string]struct{}, len(in))
	out := make([]SchedulerBucket, 0, len(in))
	for _, bucket := range in {
		key := bucket.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, bucket)
	}
	return out
}

func derefAccounts(accounts []*Account) []Account {
	if len(accounts) == 0 {
		return []Account{}
	}
	out := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		out = append(out, *account)
	}
	return out
}

func parseInt64Slice(value any) []int64 {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(raw))
	for _, item := range raw {
		if v, ok := toInt64(item); ok && v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		parsed, err := strconv.ParseInt(v.String(), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

type fallbackLimiter struct {
	maxQPS int
	mu     sync.Mutex
	window time.Time
	count  int
}

func newFallbackLimiter(maxQPS int) *fallbackLimiter {
	if maxQPS <= 0 {
		return nil
	}
	return &fallbackLimiter{
		maxQPS: maxQPS,
		window: time.Now(),
	}
}

func (l *fallbackLimiter) Allow() bool {
	if l == nil || l.maxQPS <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.Sub(l.window) >= time.Second {
		l.window = now
		l.count = 0
	}
	if l.count >= l.maxQPS {
		return false
	}
	l.count++
	return true
}

// [v0.2.5 新增]
func (s *SchedulerSnapshotService) GetGroupByIDLite(ctx context.Context, groupID int64) (*Group, error) {
	if s.groupRepo == nil {
		return nil, nil
	}
	return s.groupRepo.GetByIDLite(ctx, groupID)
}
