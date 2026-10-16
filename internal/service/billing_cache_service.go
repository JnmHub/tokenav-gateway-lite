package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"golang.org/x/sync/singleflight"
)

// 错误定义
// 注：ErrInsufficientBalance在redeem_service.go中定义
// 注：ErrDailyLimitExceeded/ErrWeeklyLimitExceeded/ErrMonthlyLimitExceeded在subscription_service.go中定义
// errBillingCacheUnavailable 内部哨兵：用于 quota 校验路径在 cache==nil 时
// 与"Redis 故障"走同一条 fail-open + DB 一次性检查的分支。
var errBillingCacheUnavailable = fmt.Errorf("billing cache unavailable")

var ErrBillingCacheWriteRevisionConflict = errors.New("billing cache write revision conflict")

var (
	ErrSubscriptionInvalid       = infraerrors.Forbidden("SUBSCRIPTION_INVALID", "subscription is invalid or expired")
	ErrBillingServiceUnavailable = infraerrors.ServiceUnavailable("BILLING_SERVICE_ERROR", "Billing service temporarily unavailable. Please retry later.")
	// RPM 超限错误。gateway_handler 负责映射为 HTTP 429。
	ErrGroupRPMExceeded              = infraerrors.TooManyRequests("GROUP_RPM_EXCEEDED", "group requests-per-minute limit exceeded")
	ErrUserRPMExceeded               = infraerrors.TooManyRequests("USER_RPM_EXCEEDED", "user requests-per-minute limit exceeded")
	ErrUserTPMExceeded               = infraerrors.TooManyRequests("USER_TPM_EXCEEDED", "user tokens-per-minute limit exceeded")
	ErrOrganizationRPMExceeded       = infraerrors.TooManyRequests("ORGANIZATION_RPM_EXCEEDED", "企业每分钟请求数已达到上限")
	ErrOrganizationTPMExceeded       = infraerrors.TooManyRequests("ORGANIZATION_TPM_EXCEEDED", "企业每分钟 Token 数已达到上限")
	ErrOrganizationMemberRPMExceeded = infraerrors.TooManyRequests("ORGANIZATION_MEMBER_RPM_EXCEEDED", "企业成员每分钟请求数已达到上限")
	ErrOrganizationMemberTPMExceeded = infraerrors.TooManyRequests("ORGANIZATION_MEMBER_TPM_EXCEEDED", "企业成员每分钟 Token 数已达到上限")
	ErrOrganizationKeyRPMExceeded    = infraerrors.TooManyRequests("ORGANIZATION_KEY_RPM_EXCEEDED", "企业 API Key 每分钟请求数已达到上限")
	ErrOrganizationKeyTPMExceeded    = infraerrors.TooManyRequests("ORGANIZATION_KEY_TPM_EXCEEDED", "企业 API Key 每分钟 Token 数已达到上限")

	// user × platform quota（HTTP 429 Too Many Requests + Retry-After header）。
	// 选用 429 而非 403：限额耗尽属于"暂时性资源用尽，重试可恢复"的场景（RFC 6585），
	// 大量 SDK（如 OpenAI 兼容客户端）只对 429 触发自动退避并读取 Retry-After，
	// 用 403 会被视为"权限不足，重试无意义"导致客户端直接报错且不退避。
	ErrUserPlatformDailyQuotaExhausted   = infraerrors.TooManyRequests("USER_PLATFORM_DAILY_QUOTA_EXHAUSTED", "Daily usage quota exhausted for this platform.")
	ErrUserPlatformWeeklyQuotaExhausted  = infraerrors.TooManyRequests("USER_PLATFORM_WEEKLY_QUOTA_EXHAUSTED", "Weekly usage quota exhausted for this platform.")
	ErrUserPlatformMonthlyQuotaExhausted = infraerrors.TooManyRequests("USER_PLATFORM_MONTHLY_QUOTA_EXHAUSTED", "Monthly usage quota exhausted for this platform.")
)

// subscriptionCacheData 订阅缓存数据结构（内部使用）
type subscriptionCacheData struct {
	Status       string
	ExpiresAt    time.Time
	DailyUsage   float64
	WeeklyUsage  float64
	MonthlyUsage float64
	Version      int64
}

// 缓存写入任务类型
type cacheWriteKind int

const (
	cacheWriteSetBalance cacheWriteKind = iota
	cacheWriteSetSubscription
	cacheWriteUpdateSubscriptionUsage
	cacheWriteDeductBalance
	cacheWriteUpdateRateLimitUsage
)

const (
	defaultCriticalCacheWriteWorkerCount   = 8
	defaultCriticalCacheWriteBufferSize    = 2048
	defaultBestEffortCacheWriteWorkerCount = 2
	defaultBestEffortCacheWriteBufferSize  = 512
	defaultCacheWriteTimeout               = 2 * time.Second
	cacheWriteOrderingStripeCount          = 256
	cacheWriteDropLogInterval              = 5 * time.Second // 丢弃日志节流间隔
	balanceLoadTimeout                     = 3 * time.Second
)

type cacheWriteClass uint8

const (
	cacheWriteClassBestEffort cacheWriteClass = iota
	cacheWriteClassCritical
)

type cacheWriteEnqueueResult uint8

const (
	cacheWriteEnqueueAccepted cacheWriteEnqueueResult = iota
	cacheWriteEnqueueDeferred
	cacheWriteEnqueueFull
	cacheWriteEnqueueClosed
)

type cacheWriteOrderDomain uint8

const (
	cacheWriteOrderBalance cacheWriteOrderDomain = iota + 1
	cacheWriteOrderSubscription
	cacheWriteOrderAPIKeyRateLimit
)

type cacheWriteOrderKey struct {
	domain   cacheWriteOrderDomain
	userID   int64
	groupID  int64
	apiKeyID int64
}

type cacheWriteOrderState struct {
	submitMu     sync.Mutex
	mu           sync.Mutex
	cond         *sync.Cond
	nextAssigned uint64
	nextToRun    uint64
	skipped      map[uint64]struct{}
	pending      int
}

type cacheWriteOrderStripe struct {
	mu     sync.Mutex
	states map[cacheWriteOrderKey]*cacheWriteOrderState
}

type cacheWritePoolOptions struct {
	preset            string
	criticalWorkers   int
	criticalBuffer    int
	bestEffortWorkers int
	bestEffortBuffer  int
	timeout           time.Duration
}

// cacheWriteTask 缓存写入任务
type cacheWriteTask struct {
	kind             cacheWriteKind
	userID           int64
	groupID          int64
	apiKeyID         int64
	balance          float64
	amount           float64
	subscriptionData *subscriptionCacheData
	sequence         uint64
	orderKey         cacheWriteOrderKey
	orderState       *cacheWriteOrderState
}

// BillingCacheRuntimeStats 是计费缓存写入池的运行时指标。
// 它只读取内存状态，不触发 Redis/DB 访问，可安全放进健康上报。
type BillingCacheRuntimeStats struct {
	Preset              string
	Revision            uint64
	TimeoutMilliseconds int64
	QueueLength         int
	QueueCapacity       int
	WorkerCount         int
	ActiveWorkerCount   int64
	DroppedFullTotal    uint64
	DroppedClosedTotal  uint64
	DroppedFullRecent   uint64
	DroppedClosedRecent uint64
	Stopped             bool

	CriticalQueueLength         int
	CriticalQueueCapacity       int
	CriticalWorkerCount         int
	CriticalActiveWorkerCount   int64
	CriticalQueueFullTotal      uint64
	CriticalSyncFallbackTotal   uint64
	CriticalSyncFallbackFailed  uint64
	BestEffortQueueLength       int
	BestEffortQueueCapacity     int
	BestEffortWorkerCount       int
	BestEffortActiveWorkerCount int64
	BestEffortDeferredLength    int
	BestEffortDeferredCapacity  int
	BestEffortDeferredTotal     uint64
	BestEffortMergedTotal       uint64
	BestEffortSyncFallbackTotal uint64
	BestEffortFallbackFailed    uint64
	BestEffortDroppedFullTotal  uint64
	BestEffortDroppedCloseTotal uint64
	OrderingWaitTotal           uint64
	ReloadTotal                 uint64
}

// apiKeyRateLimitLoader defines the interface for loading rate limit data from DB.
type apiKeyRateLimitLoader interface {
	GetRateLimitData(ctx context.Context, keyID int64) (*APIKeyRateLimitData, error)
}

// subscriptionCacheInvalidationPubSub 是订阅 L1 缓存跨实例失效的可选能力。
// 用可选接口而不是直接加进 BillingCache，是为了让非 Redis 实现（内存缓存、测试替身）
// 不必实现广播；不支持时自动退化为单实例失效，不影响主流程。
type subscriptionCacheInvalidationPubSub interface {
	PublishSubscriptionCacheInvalidation(ctx context.Context, cacheKey string) error
	SubscribeSubscriptionCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error
}

// BillingCacheService 计费缓存服务
// 负责余额和订阅数据的缓存管理，提供高性能的计费资格检查
type BillingCacheService struct {
	cache                 BillingCache
	userRepo              UserRepository
	subRepo               UserSubscriptionRepository
	apiKeyRateLimitLoader apiKeyRateLimitLoader
	userRPMCache          UserRPMCache
	userGroupRateRepo     UserGroupRateRepository
	cfg                   *config.Config
	circuitBreaker        *billingCircuitBreaker
	userPlatformQuotaRepo UserPlatformQuotaRepository

	criticalCacheWriteChan   chan cacheWriteTask
	bestEffortCacheWriteChan chan cacheWriteTask
	cacheWriteOptions        cacheWritePoolOptions
	cacheWriteWg             sync.WaitGroup
	cacheWriteCurrentWG      *sync.WaitGroup
	cacheWriteStopOnce       sync.Once
	cacheWriteMu             sync.RWMutex
	stopped                  atomic.Bool
	balanceLoadSF            singleflight.Group
	quotaLoadSF              singleflight.Group
	// 丢弃日志节流计数器（减少高负载下日志噪音）
	cacheWriteDropFullCount           uint64
	cacheWriteDropFullTotal           uint64
	cacheWriteDropFullLastLog         int64
	cacheWriteDropClosedCount         uint64
	cacheWriteDropClosedTotal         uint64
	cacheWriteDropClosedLastLog       int64
	cacheWriteCriticalFullTotal       uint64
	cacheWriteCriticalFallbackTotal   uint64
	cacheWriteCriticalFallbackFailed  uint64
	cacheWriteBestEffortDeferredTotal uint64
	cacheWriteBestEffortMergedTotal   uint64
	cacheWriteBestEffortFallbackTotal uint64
	cacheWriteBestEffortFallbackFail  uint64
	cacheWriteReloadTotal             uint64
	cacheWriteRevision                uint64
	cacheWriteCriticalActiveWorkers   int64
	cacheWriteBestEffortActiveWorkers int64
	bestEffortDeferredMu              sync.Mutex
	bestEffortDeferred                map[cacheWriteOrderKey]cacheWriteTask
	bestEffortDeferredLimit           int
	cacheWriteOrderingWaitTotal       uint64
	cacheWriteOrdering                [cacheWriteOrderingStripeCount]cacheWriteOrderStripe
}

// NewBillingCacheService 创建计费缓存服务
func NewBillingCacheService(
	cache BillingCache,
	userRepo UserRepository,
	subRepo UserSubscriptionRepository,
	apiKeyRepo APIKeyRepository,
	userRPMCache UserRPMCache,
	userGroupRateRepo UserGroupRateRepository,
	cfg *config.Config,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
) *BillingCacheService {
	svc := &BillingCacheService{
		cache:                 cache,
		userRepo:              userRepo,
		subRepo:               subRepo,
		apiKeyRateLimitLoader: apiKeyRepo,
		userRPMCache:          userRPMCache,
		userGroupRateRepo:     userGroupRateRepo,
		cfg:                   cfg,
		userPlatformQuotaRepo: userPlatformQuotaRepo,
	}
	svc.circuitBreaker = newBillingCircuitBreaker(cfg.Billing.CircuitBreaker)
	svc.startCacheWriteWorkers()
	return svc
}

// Stop 关闭缓存写入工作池
func (s *BillingCacheService) Stop() {
	s.cacheWriteStopOnce.Do(func() {
		s.stopped.Store(true)

		s.cacheWriteMu.Lock()
		criticalCh := s.criticalCacheWriteChan
		bestEffortCh := s.bestEffortCacheWriteChan
		if criticalCh != nil {
			close(criticalCh)
		}
		if bestEffortCh != nil {
			close(bestEffortCh)
		}
		s.cacheWriteMu.Unlock()

		s.cacheWriteWg.Wait()
		s.drainBestEffortDeferred()

		s.cacheWriteMu.Lock()
		if s.criticalCacheWriteChan == criticalCh {
			s.criticalCacheWriteChan = nil
		}
		if s.bestEffortCacheWriteChan == bestEffortCh {
			s.bestEffortCacheWriteChan = nil
		}
		s.cacheWriteMu.Unlock()
	})
}

func (s *BillingCacheService) startCacheWriteWorkers() {
	opts := cacheWriteOptionsFromConfig(s.cfg)
	s.cacheWriteMu.Lock()
	s.installCacheWritePoolLocked(opts)
	atomic.StoreUint64(&s.cacheWriteRevision, 1)
	s.cacheWriteMu.Unlock()
}

func (s *BillingCacheService) installCacheWritePoolLocked(opts cacheWritePoolOptions) {
	criticalCh := make(chan cacheWriteTask, opts.criticalBuffer)
	bestEffortCh := make(chan cacheWriteTask, opts.bestEffortBuffer)
	generationWG := &sync.WaitGroup{}
	s.cacheWriteOptions = opts
	s.criticalCacheWriteChan = criticalCh
	s.bestEffortCacheWriteChan = bestEffortCh
	s.cacheWriteCurrentWG = generationWG
	s.bestEffortDeferredMu.Lock()
	if s.bestEffortDeferred == nil {
		s.bestEffortDeferred = make(map[cacheWriteOrderKey]cacheWriteTask)
	}
	s.bestEffortDeferredLimit = opts.bestEffortBuffer
	s.bestEffortDeferredMu.Unlock()
	for i := 0; i < opts.criticalWorkers; i++ {
		s.cacheWriteWg.Add(1)
		generationWG.Add(1)
		go s.cacheWriteWorker(criticalCh, cacheWriteClassCritical, generationWG)
	}
	for i := 0; i < opts.bestEffortWorkers; i++ {
		s.cacheWriteWg.Add(1)
		generationWG.Add(1)
		go s.cacheWriteWorker(bestEffortCh, cacheWriteClassBestEffort, generationWG)
	}
}

func cacheWriteOptionsFromConfig(cfg *config.Config) cacheWritePoolOptions {
	opts := cacheWritePoolOptions{
		preset:            config.BillingCacheWritePresetStandard,
		criticalWorkers:   defaultCriticalCacheWriteWorkerCount,
		criticalBuffer:    defaultCriticalCacheWriteBufferSize,
		bestEffortWorkers: defaultBestEffortCacheWriteWorkerCount,
		bestEffortBuffer:  defaultBestEffortCacheWriteBufferSize,
		timeout:           defaultCacheWriteTimeout,
	}
	if cfg == nil {
		return opts
	}
	configured := cfg.Billing.CacheWrite
	if resolved, err := config.ResolveBillingCacheWriteConfig(configured); err == nil {
		configured = resolved
	}
	if configured.Preset != "" {
		opts.preset = configured.Preset
	}
	if configured.CriticalWorkerCount > 0 {
		opts.criticalWorkers = configured.CriticalWorkerCount
	}
	if configured.CriticalBufferSize > 0 {
		opts.criticalBuffer = configured.CriticalBufferSize
	}
	if configured.BestEffortWorkerCount > 0 {
		opts.bestEffortWorkers = configured.BestEffortWorkerCount
	}
	if configured.BestEffortBufferSize > 0 {
		opts.bestEffortBuffer = configured.BestEffortBufferSize
	}
	if configured.TimeoutMilliseconds > 0 {
		opts.timeout = time.Duration(configured.TimeoutMilliseconds) * time.Millisecond
	}
	return opts
}

// ReloadCacheWriteConfig atomically publishes a new bounded pool, then drains
// the previous generation. Per-key sequence guards span both generations, so
// a hot reload cannot reorder an absolute cache refresh and a billing delta.
func (s *BillingCacheService) ReloadCacheWriteConfig(settings config.BillingCacheWriteConfig) error {
	return s.reloadCacheWriteConfig(settings, nil)
}

// ReloadCacheWriteConfigCAS applies an administrator change only if the UI
// still holds the current revision. This prevents two tabs from silently
// overwriting each other while preserving the same drain semantics as reload.
func (s *BillingCacheService) ReloadCacheWriteConfigCAS(settings config.BillingCacheWriteConfig, expectedRevision uint64) error {
	return s.reloadCacheWriteConfig(settings, &expectedRevision)
}

func (s *BillingCacheService) reloadCacheWriteConfig(settings config.BillingCacheWriteConfig, expectedRevision *uint64) error {
	if s == nil {
		return fmt.Errorf("billing cache service is nil")
	}
	resolved, err := config.ResolveBillingCacheWriteConfig(settings)
	if err != nil {
		return err
	}
	if err := validateCacheWritePoolSettings(resolved); err != nil {
		return err
	}
	temporary := &config.Config{}
	temporary.Billing.CacheWrite = resolved
	opts := cacheWriteOptionsFromConfig(temporary)

	s.cacheWriteMu.Lock()
	if s.stopped.Load() {
		s.cacheWriteMu.Unlock()
		return fmt.Errorf("billing cache service is stopped")
	}
	currentRevision := atomic.LoadUint64(&s.cacheWriteRevision)
	if expectedRevision != nil && *expectedRevision != currentRevision {
		s.cacheWriteMu.Unlock()
		return fmt.Errorf("%w: expected %d, current %d", ErrBillingCacheWriteRevisionConflict, *expectedRevision, currentRevision)
	}
	if cacheWritePoolOptionsEqual(s.cacheWriteOptions, opts) {
		s.cacheWriteMu.Unlock()
		return nil
	}
	oldCritical := s.criticalCacheWriteChan
	oldBestEffort := s.bestEffortCacheWriteChan
	oldWG := s.cacheWriteCurrentWG
	s.installCacheWritePoolLocked(opts)
	if oldCritical != nil {
		close(oldCritical)
	}
	if oldBestEffort != nil {
		close(oldBestEffort)
	}
	atomic.AddUint64(&s.cacheWriteReloadTotal, 1)
	atomic.AddUint64(&s.cacheWriteRevision, 1)
	s.cacheWriteMu.Unlock()

	if oldWG != nil {
		oldWG.Wait()
	}
	return nil
}

// CurrentCacheWriteConfig returns only the effective bounded values. It never
// exposes Redis credentials or unrelated process configuration.
func (s *BillingCacheService) CurrentCacheWriteConfig() config.BillingCacheWriteConfig {
	if s == nil {
		return config.BillingCacheWriteConfig{}
	}
	s.cacheWriteMu.RLock()
	options := s.cacheWriteOptions
	s.cacheWriteMu.RUnlock()
	timeout := options.timeout
	if timeout <= 0 {
		timeout = defaultCacheWriteTimeout
	}
	preset := options.preset
	if preset == "" {
		preset = config.BillingCacheWritePresetStandard
	}
	return config.BillingCacheWriteConfig{
		Preset:                preset,
		CriticalWorkerCount:   options.criticalWorkers,
		CriticalBufferSize:    options.criticalBuffer,
		BestEffortWorkerCount: options.bestEffortWorkers,
		BestEffortBufferSize:  options.bestEffortBuffer,
		TimeoutMilliseconds:   int(timeout.Milliseconds()),
	}
}

func validateCacheWritePoolSettings(settings config.BillingCacheWriteConfig) error {
	checks := []struct {
		ok  bool
		msg string
	}{
		{settings.CriticalWorkerCount >= 1 && settings.CriticalWorkerCount <= 128, "critical_worker_count must be between 1 and 128"},
		{settings.BestEffortWorkerCount >= 1 && settings.BestEffortWorkerCount <= 128, "best_effort_worker_count must be between 1 and 128"},
		{settings.CriticalBufferSize >= 1 && settings.CriticalBufferSize <= 1048576, "critical_buffer_size must be between 1 and 1048576"},
		{settings.BestEffortBufferSize >= 1 && settings.BestEffortBufferSize <= 1048576, "best_effort_buffer_size must be between 1 and 1048576"},
		{settings.TimeoutMilliseconds >= 10 && settings.TimeoutMilliseconds <= 30000, "timeout_milliseconds must be between 10 and 30000"},
	}
	for _, check := range checks {
		if !check.ok {
			return fmt.Errorf("billing.cache_write.%s", check.msg)
		}
	}
	return nil
}

func cacheWritePoolOptionsEqual(left, right cacheWritePoolOptions) bool {
	return left == right
}

func (s *BillingCacheService) cacheWriteTimeout() time.Duration {
	if s == nil {
		return defaultCacheWriteTimeout
	}
	s.cacheWriteMu.RLock()
	timeout := s.cacheWriteOptions.timeout
	s.cacheWriteMu.RUnlock()
	if timeout > 0 {
		return timeout
	}
	return defaultCacheWriteTimeout
}

func (s *BillingCacheService) RuntimeStats() BillingCacheRuntimeStats {
	if s == nil {
		return BillingCacheRuntimeStats{}
	}
	s.cacheWriteMu.RLock()
	options := s.cacheWriteOptions
	timeout := options.timeout
	if timeout <= 0 {
		timeout = defaultCacheWriteTimeout
	}
	stats := BillingCacheRuntimeStats{
		Preset:                      options.preset,
		Revision:                    atomic.LoadUint64(&s.cacheWriteRevision),
		TimeoutMilliseconds:         timeout.Milliseconds(),
		WorkerCount:                 options.criticalWorkers + options.bestEffortWorkers,
		ActiveWorkerCount:           atomic.LoadInt64(&s.cacheWriteCriticalActiveWorkers) + atomic.LoadInt64(&s.cacheWriteBestEffortActiveWorkers),
		DroppedFullTotal:            atomic.LoadUint64(&s.cacheWriteDropFullTotal),
		DroppedClosedTotal:          atomic.LoadUint64(&s.cacheWriteDropClosedTotal),
		DroppedFullRecent:           atomic.LoadUint64(&s.cacheWriteDropFullCount),
		DroppedClosedRecent:         atomic.LoadUint64(&s.cacheWriteDropClosedCount),
		Stopped:                     s.stopped.Load(),
		CriticalWorkerCount:         options.criticalWorkers,
		CriticalActiveWorkerCount:   atomic.LoadInt64(&s.cacheWriteCriticalActiveWorkers),
		CriticalQueueFullTotal:      atomic.LoadUint64(&s.cacheWriteCriticalFullTotal),
		CriticalSyncFallbackTotal:   atomic.LoadUint64(&s.cacheWriteCriticalFallbackTotal),
		CriticalSyncFallbackFailed:  atomic.LoadUint64(&s.cacheWriteCriticalFallbackFailed),
		BestEffortWorkerCount:       options.bestEffortWorkers,
		BestEffortActiveWorkerCount: atomic.LoadInt64(&s.cacheWriteBestEffortActiveWorkers),
		BestEffortDeferredTotal:     atomic.LoadUint64(&s.cacheWriteBestEffortDeferredTotal),
		BestEffortMergedTotal:       atomic.LoadUint64(&s.cacheWriteBestEffortMergedTotal),
		BestEffortSyncFallbackTotal: atomic.LoadUint64(&s.cacheWriteBestEffortFallbackTotal),
		BestEffortFallbackFailed:    atomic.LoadUint64(&s.cacheWriteBestEffortFallbackFail),
		BestEffortDroppedFullTotal:  atomic.LoadUint64(&s.cacheWriteDropFullTotal),
		BestEffortDroppedCloseTotal: atomic.LoadUint64(&s.cacheWriteDropClosedTotal),
		OrderingWaitTotal:           atomic.LoadUint64(&s.cacheWriteOrderingWaitTotal),
		ReloadTotal:                 atomic.LoadUint64(&s.cacheWriteReloadTotal),
	}
	if ch := s.criticalCacheWriteChan; ch != nil {
		stats.CriticalQueueLength = len(ch)
		stats.CriticalQueueCapacity = cap(ch)
	}
	if ch := s.bestEffortCacheWriteChan; ch != nil {
		stats.BestEffortQueueLength = len(ch)
		stats.BestEffortQueueCapacity = cap(ch)
	}
	s.cacheWriteMu.RUnlock()
	s.bestEffortDeferredMu.Lock()
	stats.BestEffortDeferredLength = len(s.bestEffortDeferred)
	stats.BestEffortDeferredCapacity = s.bestEffortDeferredLimit
	s.bestEffortDeferredMu.Unlock()
	stats.QueueLength = stats.CriticalQueueLength + stats.BestEffortQueueLength
	stats.QueueLength += stats.BestEffortDeferredLength
	stats.QueueCapacity = stats.CriticalQueueCapacity + stats.BestEffortQueueCapacity + stats.BestEffortDeferredCapacity
	return stats
}

func cacheWriteClassForKind(kind cacheWriteKind) cacheWriteClass {
	switch kind {
	case cacheWriteDeductBalance, cacheWriteUpdateSubscriptionUsage, cacheWriteUpdateRateLimitUsage:
		return cacheWriteClassCritical
	default:
		return cacheWriteClassBestEffort
	}
}

func cacheWriteOrderKeyForTask(task cacheWriteTask) (cacheWriteOrderKey, bool) {
	switch task.kind {
	case cacheWriteSetBalance, cacheWriteDeductBalance:
		return cacheWriteOrderKey{domain: cacheWriteOrderBalance, userID: task.userID}, task.userID > 0
	case cacheWriteSetSubscription, cacheWriteUpdateSubscriptionUsage:
		return cacheWriteOrderKey{domain: cacheWriteOrderSubscription, userID: task.userID, groupID: task.groupID}, task.userID > 0 && task.groupID > 0
	case cacheWriteUpdateRateLimitUsage:
		return cacheWriteOrderKey{domain: cacheWriteOrderAPIKeyRateLimit, apiKeyID: task.apiKeyID}, task.apiKeyID > 0
	default:
		return cacheWriteOrderKey{}, false
	}
}

func cacheWriteStripeIndex(key cacheWriteOrderKey) uint64 {
	// A small integer mixer keeps adjacent user and group IDs from piling into
	// one stripe while avoiding allocations on the request completion path.
	value := uint64(key.domain)*0x9e3779b97f4a7c15 ^ uint64(key.userID)
	value ^= uint64(key.groupID) * 0xbf58476d1ce4e5b9
	value ^= uint64(key.apiKeyID) * 0x94d049bb133111eb
	value ^= value >> 30
	value *= 0xbf58476d1ce4e5b9
	value ^= value >> 27
	return value % cacheWriteOrderingStripeCount
}

// prepareCacheWriteTask registers one pending task before it can enter either
// queue. Both queues share the same per-key state so a delayed absolute cache
// warm cannot overwrite a newer balance/subscription increment.
func (s *BillingCacheService) prepareCacheWriteTask(task *cacheWriteTask) {
	if task == nil || task.orderState != nil {
		return
	}
	key, ok := cacheWriteOrderKeyForTask(*task)
	if !ok {
		return
	}
	stripe := &s.cacheWriteOrdering[cacheWriteStripeIndex(key)]
	stripe.mu.Lock()
	if stripe.states == nil {
		stripe.states = make(map[cacheWriteOrderKey]*cacheWriteOrderState)
	}
	state := stripe.states[key]
	if state == nil {
		state = &cacheWriteOrderState{nextToRun: 1}
		state.cond = sync.NewCond(&state.mu)
		stripe.states[key] = state
	}
	// Hold the per-key submission lock until the selected channel accepted (or
	// rejected) the task. This keeps cross-queue enqueue order identical to the
	// sequence below even when two request completions race.
	state.submitMu.Lock()
	state.pending++
	state.nextAssigned++
	task.sequence = state.nextAssigned
	task.orderKey = key
	task.orderState = state
	stripe.mu.Unlock()
}

func releaseCacheWriteSubmission(task cacheWriteTask) {
	if task.orderState != nil {
		task.orderState.submitMu.Unlock()
	}
}

func completeCacheWriteSequence(task cacheWriteTask) {
	state := task.orderState
	if state == nil || task.sequence == 0 {
		return
	}
	state.mu.Lock()
	if task.sequence == state.nextToRun {
		state.nextToRun++
		for {
			if _, ok := state.skipped[state.nextToRun]; !ok {
				break
			}
			delete(state.skipped, state.nextToRun)
			state.nextToRun++
		}
	} else if task.sequence > state.nextToRun {
		if state.skipped == nil {
			state.skipped = make(map[uint64]struct{})
		}
		state.skipped[task.sequence] = struct{}{}
	}
	state.cond.Broadcast()
	state.mu.Unlock()
}

func (s *BillingCacheService) finishCacheWriteTask(task cacheWriteTask) {
	if task.orderState == nil {
		return
	}
	stripe := &s.cacheWriteOrdering[cacheWriteStripeIndex(task.orderKey)]
	stripe.mu.Lock()
	state := stripe.states[task.orderKey]
	if state == task.orderState {
		state.pending--
		if state.pending <= 0 {
			delete(stripe.states, task.orderKey)
		}
	}
	stripe.mu.Unlock()
}

// submitCacheWrite keeps customer-facing request completion non-blocking while
// preserving correctness. Best-effort refreshes overflow into a bounded,
// per-target latest-value map; only when both bounded layers are full does the
// caller synchronously write the cache. Critical mutations always fall back
// synchronously when their queue is full.
func (s *BillingCacheService) submitCacheWrite(task cacheWriteTask) {
	s.prepareCacheWriteTask(&task)
	class := cacheWriteClassForKind(task.kind)
	result := s.enqueueCacheWrite(task, class)
	releaseCacheWriteSubmission(task)
	if result == cacheWriteEnqueueAccepted || result == cacheWriteEnqueueDeferred {
		return
	}
	// Stop 后拒绝所有新任务。只有运行中的关键队列真正满了才同步
	// 兜底，避免关闭流程仍产生新的 Redis 写入，保证 Stop 的排空边界清晰。
	if result == cacheWriteEnqueueClosed {
		completeCacheWriteSequence(task)
		s.finishCacheWriteTask(task)
		return
	}
	if class == cacheWriteClassBestEffort {
		atomic.AddUint64(&s.cacheWriteBestEffortFallbackTotal, 1)
		if err := s.executeCacheWriteTask(task); err != nil {
			atomic.AddUint64(&s.cacheWriteBestEffortFallbackFail, 1)
			s.logCacheWriteFailure(task, "best_effort_sync_fallback", err)
		}
		return
	}
	atomic.AddUint64(&s.cacheWriteCriticalFallbackTotal, 1)
	if err := s.executeCacheWriteTask(task); err != nil {
		atomic.AddUint64(&s.cacheWriteCriticalFallbackFailed, 1)
		s.logCacheWriteFailure(task, "sync_fallback", err)
	}
}

func (s *BillingCacheService) enqueueCacheWrite(task cacheWriteTask, class cacheWriteClass) cacheWriteEnqueueResult {
	if s.stopped.Load() {
		if class == cacheWriteClassBestEffort {
			s.logCacheWriteDrop(task, "closed")
		}
		return cacheWriteEnqueueClosed
	}

	s.cacheWriteMu.RLock()
	defer s.cacheWriteMu.RUnlock()
	// Stop 可能在上面的原子读取之后拿到写锁并关闭 channel；在读锁内
	// 再检查一次，确保这里绝不会向已经关闭的 channel 发送。
	if s.stopped.Load() {
		if class == cacheWriteClassBestEffort {
			s.logCacheWriteDrop(task, "closed")
		}
		return cacheWriteEnqueueClosed
	}
	var ch chan cacheWriteTask
	if class == cacheWriteClassCritical {
		ch = s.criticalCacheWriteChan
	} else {
		ch = s.bestEffortCacheWriteChan
	}
	if ch == nil {
		if class == cacheWriteClassBestEffort {
			s.logCacheWriteDrop(task, "closed")
		}
		return cacheWriteEnqueueClosed
	}
	select {
	case ch <- task:
		return cacheWriteEnqueueAccepted
	default:
		if class == cacheWriteClassCritical {
			atomic.AddUint64(&s.cacheWriteCriticalFullTotal, 1)
			return cacheWriteEnqueueFull
		}
		return s.deferBestEffortCacheWrite(task)
	}
}

func (s *BillingCacheService) deferBestEffortCacheWrite(task cacheWriteTask) cacheWriteEnqueueResult {
	key, ok := cacheWriteOrderKeyForTask(task)
	if !ok {
		return cacheWriteEnqueueFull
	}
	s.bestEffortDeferredMu.Lock()
	if s.bestEffortDeferred == nil {
		s.bestEffortDeferred = make(map[cacheWriteOrderKey]cacheWriteTask)
	}
	limit := s.bestEffortDeferredLimit
	if limit <= 0 {
		limit = s.cacheWriteOptions.bestEffortBuffer
	}
	if previous, exists := s.bestEffortDeferred[key]; exists {
		s.bestEffortDeferred[key] = task
		s.bestEffortDeferredMu.Unlock()
		// The superseded absolute refresh must leave the shared ordering state;
		// otherwise the replacement would wait forever for a task that no worker
		// can execute. The latest task remains pending and preserves final state.
		completeCacheWriteSequence(previous)
		s.finishCacheWriteTask(previous)
		atomic.AddUint64(&s.cacheWriteBestEffortMergedTotal, 1)
		return cacheWriteEnqueueDeferred
	}
	if limit > 0 && len(s.bestEffortDeferred) < limit {
		s.bestEffortDeferred[key] = task
		s.bestEffortDeferredMu.Unlock()
		atomic.AddUint64(&s.cacheWriteBestEffortDeferredTotal, 1)
		return cacheWriteEnqueueDeferred
	}
	s.bestEffortDeferredMu.Unlock()
	return cacheWriteEnqueueFull
}

func (s *BillingCacheService) takeBestEffortDeferred() (cacheWriteTask, bool) {
	s.bestEffortDeferredMu.Lock()
	defer s.bestEffortDeferredMu.Unlock()
	for key, task := range s.bestEffortDeferred {
		delete(s.bestEffortDeferred, key)
		return task, true
	}
	return cacheWriteTask{}, false
}

func (s *BillingCacheService) drainBestEffortDeferred() {
	for {
		task, ok := s.takeBestEffortDeferred()
		if !ok {
			return
		}
		if err := s.executeCacheWriteTask(task); err != nil {
			s.logCacheWriteFailure(task, "deferred_worker", err)
		}
	}
}

func (s *BillingCacheService) cacheWriteWorker(ch <-chan cacheWriteTask, class cacheWriteClass, generationWG *sync.WaitGroup) {
	defer s.cacheWriteWg.Done()
	defer generationWG.Done()
	activeWorkers := &s.cacheWriteBestEffortActiveWorkers
	if class == cacheWriteClassCritical {
		activeWorkers = &s.cacheWriteCriticalActiveWorkers
	}
	atomic.AddInt64(activeWorkers, 1)
	defer atomic.AddInt64(activeWorkers, -1)
	for task := range ch {
		if err := s.executeCacheWriteTask(task); err != nil {
			s.logCacheWriteFailure(task, "worker", err)
		}
		if class == cacheWriteClassBestEffort {
			s.drainBestEffortDeferred()
		}
	}
	if class == cacheWriteClassBestEffort {
		s.drainBestEffortDeferred()
	}
}

func (s *BillingCacheService) executeCacheWriteTask(task cacheWriteTask) error {
	defer s.finishCacheWriteTask(task)
	state := task.orderState
	if state != nil {
		state.mu.Lock()
		if task.sequence != state.nextToRun {
			atomic.AddUint64(&s.cacheWriteOrderingWaitTotal, 1)
		}
		for task.sequence != state.nextToRun {
			state.cond.Wait()
		}
		state.mu.Unlock()
		defer completeCacheWriteSequence(task)
	}

	timeout := s.cacheWriteTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if s.cache == nil {
		return nil
	}
	switch task.kind {
	case cacheWriteSetBalance:
		return s.cache.SetUserBalance(ctx, task.userID, task.balance)
	case cacheWriteSetSubscription:
		if task.subscriptionData == nil {
			return nil
		}
		return s.cache.SetSubscriptionCache(ctx, task.userID, task.groupID, s.convertToPortsData(task.subscriptionData))
	case cacheWriteUpdateSubscriptionUsage:
		return s.cache.UpdateSubscriptionUsage(ctx, task.userID, task.groupID, task.amount)
	case cacheWriteDeductBalance:
		return s.cache.DeductUserBalance(ctx, task.userID, task.amount)
	case cacheWriteUpdateRateLimitUsage:
		return s.cache.UpdateAPIKeyRateLimitUsage(ctx, task.apiKeyID, task.amount)
	default:
		return nil
	}
}

func (s *BillingCacheService) logCacheWriteFailure(task cacheWriteTask, path string, err error) {
	logger.LegacyPrintf("service.billing_cache", "Warning: cache write failed path=%s kind=%s user=%d group=%d api_key=%d: %v",
		path, cacheWriteKindName(task.kind), task.userID, task.groupID, task.apiKeyID, err)
}

// cacheWriteKindName 用于日志中的任务类型标识，便于排查丢弃原因。
func cacheWriteKindName(kind cacheWriteKind) string {
	switch kind {
	case cacheWriteSetBalance:
		return "set_balance"
	case cacheWriteSetSubscription:
		return "set_subscription"
	case cacheWriteUpdateSubscriptionUsage:
		return "update_subscription_usage"
	case cacheWriteDeductBalance:
		return "deduct_balance"
	case cacheWriteUpdateRateLimitUsage:
		return "update_rate_limit_usage"
	default:
		return "unknown"
	}
}

// logCacheWriteDrop 使用节流方式记录丢弃情况，并汇总丢弃数量。
func (s *BillingCacheService) logCacheWriteDrop(task cacheWriteTask, reason string) {
	var (
		countPtr *uint64
		lastPtr  *int64
	)
	switch reason {
	case "full":
		countPtr = &s.cacheWriteDropFullCount
		lastPtr = &s.cacheWriteDropFullLastLog
		atomic.AddUint64(&s.cacheWriteDropFullTotal, 1)
	case "closed":
		countPtr = &s.cacheWriteDropClosedCount
		lastPtr = &s.cacheWriteDropClosedLastLog
		atomic.AddUint64(&s.cacheWriteDropClosedTotal, 1)
	default:
		return
	}

	atomic.AddUint64(countPtr, 1)
	now := time.Now().UnixNano()
	last := atomic.LoadInt64(lastPtr)
	if now-last < int64(cacheWriteDropLogInterval) {
		return
	}
	if !atomic.CompareAndSwapInt64(lastPtr, last, now) {
		return
	}
	dropped := atomic.SwapUint64(countPtr, 0)
	if dropped == 0 {
		return
	}
	logger.LegacyPrintf("service.billing_cache", "Warning: cache write queue %s, dropped %d tasks in last %s (latest kind=%s user %d group %d)",
		reason,
		dropped,
		cacheWriteDropLogInterval,
		cacheWriteKindName(task.kind),
		task.userID,
		task.groupID,
	)
}

// ============================================
// 余额缓存方法
// ============================================

// GetUserBalance 获取用户余额（优先从缓存读取）
func (s *BillingCacheService) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	if s.cache == nil {
		// Redis不可用，直接查询数据库
		return s.getUserBalanceFromDB(ctx, userID)
	}

	// 尝试从缓存读取
	balance, err := s.cache.GetUserBalance(ctx, userID)
	if err == nil {
		return balance, nil
	}

	// 缓存未命中：singleflight 合并同一 userID 的并发回源请求。
	value, err, _ := s.balanceLoadSF.Do(strconv.FormatInt(userID, 10), func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.Background(), balanceLoadTimeout)
		defer cancel()

		balance, err := s.getUserBalanceFromDB(loadCtx, userID)
		if err != nil {
			return nil, err
		}

		// 异步建立缓存
		s.submitCacheWrite(cacheWriteTask{
			kind:    cacheWriteSetBalance,
			userID:  userID,
			balance: balance,
		})
		return balance, nil
	})
	if err != nil {
		return 0, err
	}
	balance, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("unexpected balance type: %T", value)
	}
	return balance, nil
}

// getUserBalanceFromDB 从数据库获取用户余额
func (s *BillingCacheService) getUserBalanceFromDB(ctx context.Context, userID int64) (float64, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("get user balance: %w", err)
	}
	return user.Balance, nil
}

// DeductBalanceCache 扣减余额缓存（同步调用）
func (s *BillingCacheService) DeductBalanceCache(ctx context.Context, userID int64, amount float64) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.DeductUserBalance(ctx, userID, amount)
}

// QueueDeductBalance 异步扣减余额缓存
func (s *BillingCacheService) QueueDeductBalance(userID int64, amount float64) {
	if s.cache == nil {
		return
	}
	s.submitCacheWrite(cacheWriteTask{
		kind:   cacheWriteDeductBalance,
		userID: userID,
		amount: amount,
	})
}

// InvalidateUserBalance 失效用户余额缓存
func (s *BillingCacheService) InvalidateUserBalance(ctx context.Context, userID int64) error {
	if s.cache == nil {
		return nil
	}
	if err := s.cache.InvalidateUserBalance(ctx, userID); err != nil {
		logger.LegacyPrintf("service.billing_cache", "Warning: invalidate balance cache failed for user %d: %v", userID, err)
		return err
	}
	return nil
}

// ============================================
// 订阅缓存方法
// ============================================

// GetSubscriptionStatus 获取订阅状态（优先从缓存读取）
func (s *BillingCacheService) GetSubscriptionStatus(ctx context.Context, userID, groupID int64) (*subscriptionCacheData, error) {
	if s.cache == nil {
		return s.getSubscriptionFromDB(ctx, userID, groupID)
	}

	// 尝试从缓存读取
	cacheData, err := s.cache.GetSubscriptionCache(ctx, userID, groupID)
	if err == nil && cacheData != nil {
		return s.convertFromPortsData(cacheData), nil
	}

	// 缓存未命中，从数据库读取
	data, err := s.getSubscriptionFromDB(ctx, userID, groupID)
	if err != nil {
		return nil, err
	}

	// 异步建立缓存
	s.submitCacheWrite(cacheWriteTask{
		kind:             cacheWriteSetSubscription,
		userID:           userID,
		groupID:          groupID,
		subscriptionData: data,
	})

	return data, nil
}

func (s *BillingCacheService) convertFromPortsData(data *SubscriptionCacheData) *subscriptionCacheData {
	return &subscriptionCacheData{
		Status:       data.Status,
		ExpiresAt:    data.ExpiresAt,
		DailyUsage:   data.DailyUsage,
		WeeklyUsage:  data.WeeklyUsage,
		MonthlyUsage: data.MonthlyUsage,
		Version:      data.Version,
	}
}

func (s *BillingCacheService) convertToPortsData(data *subscriptionCacheData) *SubscriptionCacheData {
	return &SubscriptionCacheData{
		Status:       data.Status,
		ExpiresAt:    data.ExpiresAt,
		DailyUsage:   data.DailyUsage,
		WeeklyUsage:  data.WeeklyUsage,
		MonthlyUsage: data.MonthlyUsage,
		Version:      data.Version,
	}
}

// getSubscriptionFromDB 从数据库获取订阅数据
func (s *BillingCacheService) getSubscriptionFromDB(ctx context.Context, userID, groupID int64) (*subscriptionCacheData, error) {
	sub, err := s.subRepo.GetActiveByUserIDAndGroupID(ctx, userID, groupID)
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}

	return &subscriptionCacheData{
		Status:       sub.Status,
		ExpiresAt:    sub.ExpiresAt,
		DailyUsage:   sub.DailyUsageUSD,
		WeeklyUsage:  sub.WeeklyUsageUSD,
		MonthlyUsage: sub.MonthlyUsageUSD,
		Version:      sub.UpdatedAt.Unix(),
	}, nil
}

// UpdateSubscriptionUsage 更新订阅用量缓存（同步调用）
func (s *BillingCacheService) UpdateSubscriptionUsage(ctx context.Context, userID, groupID int64, costUSD float64) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.UpdateSubscriptionUsage(ctx, userID, groupID, costUSD)
}

// QueueUpdateSubscriptionUsage 异步更新订阅用量缓存
func (s *BillingCacheService) QueueUpdateSubscriptionUsage(userID, groupID int64, costUSD float64) {
	if s.cache == nil {
		return
	}
	s.submitCacheWrite(cacheWriteTask{
		kind:    cacheWriteUpdateSubscriptionUsage,
		userID:  userID,
		groupID: groupID,
		amount:  costUSD,
	})
}

// InvalidateSubscription 失效指定订阅缓存
func (s *BillingCacheService) InvalidateSubscription(ctx context.Context, userID, groupID int64) error {
	if s.cache == nil {
		return nil
	}
	if err := s.cache.InvalidateSubscriptionCache(ctx, userID, groupID); err != nil {
		logger.LegacyPrintf("service.billing_cache", "Warning: invalidate subscription cache failed for user %d group %d: %v", userID, groupID, err)
		return err
	}
	return nil
}

// PublishSubscriptionCacheInvalidation 把订阅 L1 缓存失效事件广播给其他实例。
// 缓存实现不支持广播时静默返回 nil：单实例部署下本地 L1 已失效，无需跨实例通知。
func (s *BillingCacheService) PublishSubscriptionCacheInvalidation(ctx context.Context, cacheKey string) error {
	if s.cache == nil {
		return nil
	}
	pubsub, ok := s.cache.(subscriptionCacheInvalidationPubSub)
	if !ok {
		return nil
	}
	return pubsub.PublishSubscriptionCacheInvalidation(ctx, cacheKey)
}

// SubscribeSubscriptionCacheInvalidation 订阅其他实例发出的订阅 L1 缓存失效事件。
// handler 收到的是缓存 key 原文，由订阅方自行解析并删除本地 L1 条目。
func (s *BillingCacheService) SubscribeSubscriptionCacheInvalidation(ctx context.Context, handler func(cacheKey string)) error {
	if s.cache == nil {
		return nil
	}
	pubsub, ok := s.cache.(subscriptionCacheInvalidationPubSub)
	if !ok {
		return nil
	}
	return pubsub.SubscribeSubscriptionCacheInvalidation(ctx, handler)
}

// InvalidateAPIKeyRateLimit invalidates the Redis rate-limit usage cache for an API key.
func (s *BillingCacheService) InvalidateAPIKeyRateLimit(ctx context.Context, keyID int64) error {
	if s.cache == nil {
		return nil
	}
	if err := s.cache.InvalidateAPIKeyRateLimit(ctx, keyID); err != nil {
		logger.LegacyPrintf("service.billing_cache", "Warning: invalidate api key rate limit cache failed for key %d: %v", keyID, err)
		return err
	}
	return nil
}

// ============================================
// API Key 限速缓存方法
// ============================================

// checkAPIKeyRateLimits checks rate limit windows for an API key.
// It loads usage from Redis cache (falling back to DB on cache miss),
// resets expired windows in-memory and triggers async DB reset,
// and returns an error if any window limit is exceeded.
func (s *BillingCacheService) checkAPIKeyRateLimits(ctx context.Context, apiKey *APIKey) error {
	if s.cache == nil {
		// No cache: fall back to reading from DB directly
		if s.apiKeyRateLimitLoader == nil {
			return nil
		}
		data, err := s.apiKeyRateLimitLoader.GetRateLimitData(ctx, apiKey.ID)
		if err != nil {
			return nil // Don't block requests on DB errors
		}
		return s.evaluateRateLimits(ctx, apiKey, data.Usage5h, data.Usage1d, data.Usage7d,
			data.Window5hStart, data.Window1dStart, data.Window7dStart)
	}

	cacheData, err := s.cache.GetAPIKeyRateLimit(ctx, apiKey.ID)
	if err != nil {
		// Cache miss: load from DB and populate cache
		if s.apiKeyRateLimitLoader == nil {
			return nil
		}
		dbData, dbErr := s.apiKeyRateLimitLoader.GetRateLimitData(ctx, apiKey.ID)
		if dbErr != nil {
			return nil // Don't block requests on DB errors
		}
		// Build cache entry from DB data
		cacheEntry := &APIKeyRateLimitCacheData{
			Usage5h: dbData.Usage5h,
			Usage1d: dbData.Usage1d,
			Usage7d: dbData.Usage7d,
		}
		if dbData.Window5hStart != nil {
			cacheEntry.Window5h = dbData.Window5hStart.Unix()
		}
		if dbData.Window1dStart != nil {
			cacheEntry.Window1d = dbData.Window1dStart.Unix()
		}
		if dbData.Window7dStart != nil {
			cacheEntry.Window7d = dbData.Window7dStart.Unix()
		}
		_ = s.cache.SetAPIKeyRateLimit(ctx, apiKey.ID, cacheEntry)
		cacheData = cacheEntry
	}

	var w5h, w1d, w7d *time.Time
	if cacheData.Window5h > 0 {
		t := time.Unix(cacheData.Window5h, 0)
		w5h = &t
	}
	if cacheData.Window1d > 0 {
		t := time.Unix(cacheData.Window1d, 0)
		w1d = &t
	}
	if cacheData.Window7d > 0 {
		t := time.Unix(cacheData.Window7d, 0)
		w7d = &t
	}
	return s.evaluateRateLimits(ctx, apiKey, cacheData.Usage5h, cacheData.Usage1d, cacheData.Usage7d, w5h, w1d, w7d)
}

// evaluateRateLimits checks usage against limits, triggering async resets for expired windows.
func (s *BillingCacheService) evaluateRateLimits(ctx context.Context, apiKey *APIKey, usage5h, usage1d, usage7d float64, w5h, w1d, w7d *time.Time) error {
	needsReset := false

	// Reset expired windows in-memory for check purposes
	if IsWindowExpired(w5h, RateLimitWindow5h) {
		usage5h = 0
		needsReset = true
	}
	if IsWindowExpired(w1d, RateLimitWindow1d) {
		usage1d = 0
		needsReset = true
	}
	if IsWindowExpired(w7d, RateLimitWindow7d) {
		usage7d = 0
		needsReset = true
	}

	// Trigger async DB reset if any window expired
	if needsReset {
		keyID := apiKey.ID
		go func() {
			resetCtx, cancel := context.WithTimeout(context.Background(), s.cacheWriteTimeout())
			defer cancel()
			if s.apiKeyRateLimitLoader != nil {
				// Use the repo directly - reset then reload cache
				if loader, ok := s.apiKeyRateLimitLoader.(interface {
					ResetRateLimitWindows(ctx context.Context, id int64) error
				}); ok {
					if err := loader.ResetRateLimitWindows(resetCtx, keyID); err != nil {
						logger.LegacyPrintf("service.billing_cache", "Warning: reset rate limit windows failed for api key %d: %v", keyID, err)
					}
				}
			}
			// Invalidate cache so next request loads fresh data
			if s.cache != nil {
				if err := s.cache.InvalidateAPIKeyRateLimit(resetCtx, keyID); err != nil {
					logger.LegacyPrintf("service.billing_cache", "Warning: invalidate rate limit cache failed for api key %d: %v", keyID, err)
				}
			}
		}()
	}

	// Check limits
	if apiKey.RateLimit5h > 0 && usage5h >= apiKey.RateLimit5h {
		return ErrAPIKeyRateLimit5hExceeded
	}
	if apiKey.RateLimit1d > 0 && usage1d >= apiKey.RateLimit1d {
		return ErrAPIKeyRateLimit1dExceeded
	}
	if apiKey.RateLimit7d > 0 && usage7d >= apiKey.RateLimit7d {
		return ErrAPIKeyRateLimit7dExceeded
	}
	return nil
}

// QueueUpdateAPIKeyRateLimitUsage asynchronously updates rate limit usage in the cache.
func (s *BillingCacheService) QueueUpdateAPIKeyRateLimitUsage(apiKeyID int64, cost float64) {
	if s.cache == nil {
		return
	}
	s.submitCacheWrite(cacheWriteTask{
		kind:     cacheWriteUpdateRateLimitUsage,
		apiKeyID: apiKeyID,
		amount:   cost,
	})
}

// IncrementUserPlatformQuotaUsage 同步累加 user × platform usage 到 Redis 缓存。
//
// 设计：同步写入而非异步入队。同步写确保下次 preflight 立即看到最新 usage，
// 把 TOCTOU 超支窗口限制在并发 in-flight 请求数量内（而非随时间无限累积）。
// 写延迟通常 < 1ms（本地 Redis），换取 quota 视图实时性的取舍合理。
//
// Redis 写失败用 ALERT 级 log；DB 持久化由 caller 单独 goroutine 兜底（gateway_service.go）。
func (s *BillingCacheService) IncrementUserPlatformQuotaUsage(userID int64, platform string, cost float64) {
	if s.cache == nil {
		return
	}
	if platform == "" || cost <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cacheWriteTimeout())
	defer cancel()
	ttl := time.Duration(s.cfg.Billing.UserPlatformQuotaCacheTTLSeconds) * time.Second
	markDirty := s.cfg.Database.UserPlatformQuotaFlusherEnabled
	if err := s.cache.IncrUserPlatformQuotaUsageCache(ctx, userID, platform, cost, ttl, markDirty); err != nil {
		logger.LegacyPrintf("service.billing_cache",
			"ALERT: incr user platform quota cache failed user=%d platform=%s cost=%f: %v",
			userID, platform, cost, err)
	}
}

// ============================================
// 统一检查方法
// ============================================

// CheckBillingEligibility 检查用户是否有资格发起请求
// 余额模式：检查缓存余额 > 0
// 订阅模式：检查缓存用量未超过限额（Group限额从参数传入）
// platform 为请求的目标平台（如 "anthropic"），传空串 "" 时跳过 user × platform quota 检查。
func (s *BillingCacheService) CheckBillingEligibility(ctx context.Context, user *User, apiKey *APIKey, group *Group, subscription *UserSubscription, platform string) error {
	return s.CheckBillingEligibilityForRequest(ctx, user, apiKey, group, subscription, platform, 0)
}

// CheckBillingEligibilityWithoutRateConsumption 只复核不会消费计数器的资格条件。
// fallback（兜底）、状态轮询和内部重试已经属于同一个用户提交，若再次调用完整
// 门禁会污染 RPM/TPM；这些路径仍必须检查余额、订阅、平台额度和旧式 Key 窗口。
func (s *BillingCacheService) CheckBillingEligibilityWithoutRateConsumption(ctx context.Context, user *User, apiKey *APIKey, group *Group, subscription *UserSubscription, platform string) error {
	// 简易模式：跳过所有计费检查
	runMode := ""
	if s.cfg != nil {
		runMode = s.cfg.RunMode
	}
	if runMode == config.RunModeSimple {
		return nil
	}
	if runMode == config.RunModeGatewayLite {
		// gateway-lite 的余额权威在入口 lease（额度租约）；这里除 RPM/TPM 外
		// 没有本地资格项可复核，因此内部重试直接保持原提交结果。
		return nil
	}
	if s.circuitBreaker != nil && !s.circuitBreaker.Allow() {
		return ErrBillingServiceUnavailable
	}

	// 判断计费模式
	isSubscriptionMode := group != nil && group.IsSubscriptionType() && subscription != nil

	if isSubscriptionMode {
		if err := s.checkSubscriptionEligibility(ctx, user.ID, group, subscription); err != nil {
			return err
		}
	} else {
		if err := s.checkBalanceEligibility(ctx, user.ID); err != nil {
			return err
		}
	}

	// user × platform quota 仅在 standard（余额）模式生效；订阅模式豁免
	if !isSubscriptionMode {
		if err := s.checkUserPlatformQuotaEligibility(ctx, user.ID, platform); err != nil {
			return err
		}
	}

	// Check API Key rate limits (applies to both billing modes)
	if apiKey != nil && apiKey.HasRateLimits() {
		if err := s.checkAPIKeyRateLimits(ctx, apiKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *BillingCacheService) CheckBillingEligibilityForRequest(ctx context.Context, user *User, apiKey *APIKey, group *Group, subscription *UserSubscription, platform string, estimatedTokens int) error {
	if err := s.CheckBillingEligibilityWithoutRateConsumption(ctx, user, apiKey, group, subscription, platform); err != nil {
		return err
	}
	if s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return nil
	}
	// RPM 限流：级联回落（Override → Group → User），放在最后以避免为注定失败的请求增加计数。
	return s.checkRPMAndTPM(ctx, user, apiKey, group, estimatedTokens)
}

func (s *BillingCacheService) checkTPM(ctx context.Context, user *User, estimatedTokens int) error {
	if s == nil || s.userRPMCache == nil || user == nil || user.TPMLimit <= 0 || estimatedTokens <= 0 {
		return nil
	}
	count, err := s.userRPMCache.AddUserTPM(ctx, user.ID, estimatedTokens)
	if err != nil {
		logger.LegacyPrintf(
			"service.billing_cache",
			"Warning: tpm increment (user) failed for user=%d: %v",
			user.ID, err,
		)
		return nil // fail-open, consistent with RPM cache failure
	}
	if count > user.TPMLimit {
		return ErrUserTPMExceeded
	}
	return nil
}

// checkRPM 执行并行 RPM 限流，所有适用的限制同时生效，任一超限即拒绝：
//
//  1. (用户, 分组) rpm_override       — 最细粒度：管理员为特定用户在特定分组设定的专属限额。
//     override=0 表示该用户在该分组免检（绿灯），但 user 级全局上限仍然生效。
//  2. group.rpm_limit                 — 分组级：该分组的统一 RPM 容量（仅当无 override 时生效）。
//  3. user.rpm_limit                  — 用户级全局硬上限：无论 override/group 如何配置，始终生效。
//
// 与旧版"级联互斥"设计不同，新版确保 user.rpm_limit 作为全局天花板不会被 group 或 override 覆盖。
// Redis 故障一律 fail-open（打 warning，不阻塞业务）。
func (s *BillingCacheService) checkRPM(ctx context.Context, user *User, group *Group) error {
	if s == nil || s.userRPMCache == nil || user == nil {
		return nil
	}
	if err := s.checkGroupRPM(ctx, user, group); err != nil {
		return err
	}
	return s.checkUserRPM(ctx, user)
}

func (s *BillingCacheService) checkRPMAndTPM(ctx context.Context, user *User, apiKey *APIKey, group *Group, estimatedTokens int) error {
	if s == nil || s.userRPMCache == nil || user == nil {
		return nil
	}
	groupRPMLimit := s.resolveGroupRPMLimit(ctx, user, group)
	if user.OrganizationID > 0 {
		combined, ok := s.userRPMCache.(OrganizationMemberGroupRPMTPMCombinedCache)
		if !ok {
			// 企业限制是共享硬门禁，不能在实现缺失时退回按 user_id 的旧桶。
			return ErrBillingServiceUnavailable
		}
		groupID := int64(0)
		apiKeyID := int64(0)
		apiKeyRPMLimit := 0
		apiKeyTPMLimit := 0
		if group != nil {
			groupID = group.ID
		}
		if apiKey != nil {
			apiKeyID = apiKey.WSIngressConcurrencyID(ctx)
			apiKeyRPMLimit = apiKey.KeyRPMLimit
			apiKeyTPMLimit = apiKey.KeyTPMLimit
		}
		if apiKeyID == 0 {
			return ErrBillingServiceUnavailable
		}
		tokens := estimatedTokens
		if tokens < 0 {
			tokens = 0
		}
		_, _, _, _, _, rejectionReason, err := combined.IncrementOrganizationMemberGroupRPMAndTPM(
			ctx,
			user.OrganizationID,
			user.OrganizationRPMLimit,
			user.OrganizationTPMLimit,
			user.ID,
			user.RPMLimit,
			user.TPMLimit,
			groupID,
			groupRPMLimit,
			apiKeyID,
			apiKeyRPMLimit,
			apiKeyTPMLimit,
			tokens,
		)
		if err != nil {
			logger.LegacyPrintf("service.billing_cache", "Warning: organization/member rate increment failed organization=%d user=%d: %v", user.OrganizationID, user.ID, err)
			return ErrBillingServiceUnavailable
		}
		switch rejectionReason {
		case 1:
			return ErrOrganizationRPMExceeded
		case 2:
			return ErrOrganizationTPMExceeded
		case 3:
			return ErrOrganizationMemberRPMExceeded
		case 4:
			return ErrOrganizationMemberTPMExceeded
		case 5:
			return ErrGroupRPMExceeded
		case 6:
			return ErrOrganizationKeyRPMExceeded
		case 7:
			return ErrOrganizationKeyTPMExceeded
		}
		return nil
	}
	if groupRPMLimit > 0 {
		// 分组、用户 RPM 和 TPM 都是同一个 user hash 中的 fail-open 限流计数。
		// 只合并这三项可以保持拒绝顺序，同时避免跨越 quota（额度租约）和
		// concurrency（并发槽位）的 fail-closed 边界。
		if combined, ok := s.userRPMCache.(UserGroupRPMTPMCombinedCache); ok {
			tokens := 0
			if user.TPMLimit > 0 && estimatedTokens > 0 {
				tokens = estimatedTokens
			}
			groupCount, rpmCount, tpmCount, err := combined.IncrementUserGroupRPMAndUserRPMTPM(
				ctx,
				user.ID,
				group.ID,
				groupRPMLimit,
				user.RPMLimit,
				tokens,
			)
			if err != nil {
				logger.LegacyPrintf(
					"service.billing_cache",
					"Warning: combined group/rpm/tpm increment failed for user=%d group=%d: %v",
					user.ID, group.ID, err,
				)
				return nil // 整个限流缓存仍按原规则 fail-open
			}
			if groupCount > groupRPMLimit {
				return ErrGroupRPMExceeded
			}
			if user.RPMLimit > 0 && rpmCount > user.RPMLimit {
				return ErrUserRPMExceeded
			}
			if tokens > 0 && tpmCount > user.TPMLimit {
				return ErrUserTPMExceeded
			}
			return nil
		}

		count, err := s.userRPMCache.IncrementUserGroupRPM(ctx, user.ID, group.ID)
		if err != nil {
			logger.LegacyPrintf(
				"service.billing_cache",
				"Warning: rpm increment (group) failed for user=%d group=%d: %v",
				user.ID, group.ID, err,
			)
			// fail-open，继续执行用户级限制。
		} else if count > groupRPMLimit {
			return ErrGroupRPMExceeded
		}
	}

	// 用户 RPM 与 TPM 同时启用时使用一次原子 Redis 操作。可选接口让测试桩和旧实现
	// 仍安全回退原有路径；真实 Redis 实现会把四次网络往返压缩成一次。
	if combined, ok := s.userRPMCache.(UserRPMTPMCombinedCache); ok &&
		user.RPMLimit > 0 && user.TPMLimit > 0 && estimatedTokens > 0 {
		rpmCount, tpmCount, err := combined.IncrementUserRPMAndTPM(ctx, user.ID, user.RPMLimit, estimatedTokens)
		if err != nil {
			logger.LegacyPrintf(
				"service.billing_cache",
				"Warning: combined rpm/tpm increment failed for user=%d: %v",
				user.ID, err,
			)
			return nil // fail-open，与原 RPM 缓存故障语义一致
		}
		if rpmCount > user.RPMLimit {
			return ErrUserRPMExceeded
		}
		if tpmCount > user.TPMLimit {
			return ErrUserTPMExceeded
		}
		return nil
	}

	if err := s.checkUserRPM(ctx, user); err != nil {
		return err
	}
	return s.checkTPM(ctx, user, estimatedTokens)
}

func (s *BillingCacheService) checkGroupRPM(ctx context.Context, user *User, group *Group) error {
	if s == nil || s.userRPMCache == nil || user == nil || group == nil {
		return nil
	}
	limit := s.resolveGroupRPMLimit(ctx, user, group)
	if limit <= 0 {
		return nil
	}
	count, err := s.userRPMCache.IncrementUserGroupRPM(ctx, user.ID, group.ID)
	if err != nil {
		logger.LegacyPrintf(
			"service.billing_cache",
			"Warning: rpm increment (group) failed for user=%d group=%d: %v",
			user.ID, group.ID, err,
		)
		return nil // fail-open
	}
	if count > limit {
		return ErrGroupRPMExceeded
	}
	return nil
}

// resolveGroupRPMLimit 只解析最终分组实际生效的限额，不修改任何计数。
// override=0 表示跳过分组限制；用户级全局 RPM 仍由后续步骤独立执行。
func (s *BillingCacheService) resolveGroupRPMLimit(ctx context.Context, user *User, group *Group) int {
	if s == nil || user == nil || group == nil {
		return 0
	}

	// ── 第一层：分组级检查（override 或 group.rpm_limit） ──
	// 解析 override：优先使用 auth cache snapshot。已确认的 nil 代表确实无 override，
	// 不能再回退 DB，否则高并发下每个请求都会制造一次无意义的数据库往返。
	var override *int
	if user.UserGroupRPMOverride != nil {
		override = user.UserGroupRPMOverride
	} else if !user.UserGroupRPMOverrideResolved && s.userGroupRateRepo != nil {
		dbOverride, err := s.userGroupRateRepo.GetRPMOverrideByUserAndGroup(ctx, user.ID, group.ID)
		if err != nil {
			logger.LegacyPrintf(
				"service.billing_cache",
				"Warning: rpm override lookup failed for user=%d group=%d: %v",
				user.ID, group.ID, err,
			)
		} else {
			override = dbOverride
		}
	}

	if override != nil {
		if *override > 0 {
			return *override
		}
		return 0
	}
	if group.RPMLimit > 0 {
		return group.RPMLimit
	}
	return 0
}

func (s *BillingCacheService) checkUserRPM(ctx context.Context, user *User) error {
	if s == nil || s.userRPMCache == nil || user == nil {
		return nil
	}
	// ── 第二层：用户级全局硬上限（始终生效） ──
	if user.RPMLimit > 0 {
		count, err := s.userRPMCache.IncrementUserRPM(ctx, user.ID)
		if err != nil {
			logger.LegacyPrintf(
				"service.billing_cache",
				"Warning: rpm increment (user) failed for user=%d: %v",
				user.ID, err,
			)
			return nil // fail-open
		}
		if count > user.RPMLimit {
			return ErrUserRPMExceeded
		}
	}

	return nil
}

// minimumBalanceReserve 返回配置的余额保守下限（未配置或非正数时为 0，即不启用）。
// [tokenav 定制] v0.2.5 合并引入：官方余额预检预留额。
func (s *BillingCacheService) minimumBalanceReserve() float64 {
	if s == nil || s.cfg == nil || s.cfg.Billing.MinimumBalanceReserve <= 0 {
		return 0
	}
	return s.cfg.Billing.MinimumBalanceReserve
}

// balanceBelowEligibilityThreshold 判定余额是否不足以放行请求：
// 非正数一律拒绝；配置了预留额时，低于预留额也拒绝。
func (s *BillingCacheService) balanceBelowEligibilityThreshold(balance float64) bool {
	if balance <= 0 {
		return true
	}
	minimumReserve := s.minimumBalanceReserve()
	return minimumReserve > 0 && balance < minimumReserve
}

// checkBalanceEligibility 检查余额模式资格
func (s *BillingCacheService) checkBalanceEligibility(ctx context.Context, userID int64) error {
	balance, err := s.GetUserBalance(ctx, userID)
	if err != nil {
		if s.circuitBreaker != nil {
			s.circuitBreaker.OnFailure(err)
		}
		logger.LegacyPrintf("service.billing_cache", "ALERT: billing balance check failed for user %d: %v", userID, err)
		return ErrBillingServiceUnavailable.WithCause(err)
	}
	if s.circuitBreaker != nil {
		s.circuitBreaker.OnSuccess()
	}

	// [tokenav 定制] v0.2.5 合并引入：官方最低余额保留额（MinimumBalanceReserve）。
	// 余额低于该保守下限即视为不资格，避免把一个请求放到扣费时才发现余额不足。
	if s.balanceBelowEligibilityThreshold(balance) {
		return ErrInsufficientBalance
	}

	return nil
}

// checkSubscriptionEligibility 检查订阅模式资格
func (s *BillingCacheService) checkSubscriptionEligibility(ctx context.Context, userID int64, group *Group, subscription *UserSubscription) error {
	// 获取订阅缓存数据
	subData, err := s.GetSubscriptionStatus(ctx, userID, group.ID)
	if err != nil {
		if s.circuitBreaker != nil {
			s.circuitBreaker.OnFailure(err)
		}
		logger.LegacyPrintf("service.billing_cache", "ALERT: billing subscription check failed for user %d group %d: %v", userID, group.ID, err)
		return ErrBillingServiceUnavailable.WithCause(err)
	}
	if s.circuitBreaker != nil {
		s.circuitBreaker.OnSuccess()
	}

	// 检查订阅状态
	if subData.Status != SubscriptionStatusActive {
		return ErrSubscriptionInvalid
	}

	// 检查是否过期
	if time.Now().After(subData.ExpiresAt) {
		return ErrSubscriptionInvalid
	}

	// 检查限额（使用传入的Group限额配置）
	if group.HasDailyLimit() && subData.DailyUsage >= *group.DailyLimitUSD {
		return ErrDailyLimitExceeded
	}

	if group.HasWeeklyLimit() && subData.WeeklyUsage >= *group.WeeklyLimitUSD {
		return ErrWeeklyLimitExceeded
	}

	if group.HasMonthlyLimit() && subData.MonthlyUsage >= *group.MonthlyLimitUSD {
		return ErrMonthlyLimitExceeded
	}

	return nil
}

type billingCircuitBreakerState int

const (
	billingCircuitClosed billingCircuitBreakerState = iota
	billingCircuitOpen
	billingCircuitHalfOpen
)

type billingCircuitBreaker struct {
	mu                sync.Mutex
	state             billingCircuitBreakerState
	failures          int
	openedAt          time.Time
	failureThreshold  int
	resetTimeout      time.Duration
	halfOpenRequests  int
	halfOpenRemaining int
}

func newBillingCircuitBreaker(cfg config.CircuitBreakerConfig) *billingCircuitBreaker {
	if !cfg.Enabled {
		return nil
	}
	resetTimeout := time.Duration(cfg.ResetTimeoutSeconds) * time.Second
	if resetTimeout <= 0 {
		resetTimeout = 30 * time.Second
	}
	halfOpen := cfg.HalfOpenRequests
	if halfOpen <= 0 {
		halfOpen = 1
	}
	threshold := cfg.FailureThreshold
	if threshold <= 0 {
		threshold = 5
	}
	return &billingCircuitBreaker{
		state:            billingCircuitClosed,
		failureThreshold: threshold,
		resetTimeout:     resetTimeout,
		halfOpenRequests: halfOpen,
	}
}

func (b *billingCircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case billingCircuitClosed:
		return true
	case billingCircuitOpen:
		if time.Since(b.openedAt) < b.resetTimeout {
			return false
		}
		b.state = billingCircuitHalfOpen
		b.halfOpenRemaining = b.halfOpenRequests
		logger.LegacyPrintf("service.billing_cache", "ALERT: billing circuit breaker entering half-open state")
		fallthrough
	case billingCircuitHalfOpen:
		if b.halfOpenRemaining <= 0 {
			return false
		}
		b.halfOpenRemaining--
		return true
	default:
		return false
	}
}

func (b *billingCircuitBreaker) OnFailure(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case billingCircuitOpen:
		return
	case billingCircuitHalfOpen:
		b.state = billingCircuitOpen
		b.openedAt = time.Now()
		b.halfOpenRemaining = 0
		logger.LegacyPrintf("service.billing_cache", "ALERT: billing circuit breaker opened after half-open failure: %v", err)
		return
	default:
		b.failures++
		if b.failures >= b.failureThreshold {
			b.state = billingCircuitOpen
			b.openedAt = time.Now()
			b.halfOpenRemaining = 0
			logger.LegacyPrintf("service.billing_cache", "ALERT: billing circuit breaker opened after %d failures: %v", b.failures, err)
		}
	}
}

func (b *billingCircuitBreaker) OnSuccess() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	previousState := b.state
	previousFailures := b.failures

	b.state = billingCircuitClosed
	b.failures = 0
	b.halfOpenRemaining = 0

	// 只有状态真正发生变化时才记录日志
	if previousState != billingCircuitClosed {
		logger.LegacyPrintf("service.billing_cache", "ALERT: billing circuit breaker closed (was %s)", circuitStateString(previousState))
	} else if previousFailures > 0 {
		logger.LegacyPrintf("service.billing_cache", "INFO: billing circuit breaker failures reset from %d", previousFailures)
	}
}

func circuitStateString(state billingCircuitBreakerState) string {
	switch state {
	case billingCircuitClosed:
		return "closed"
	case billingCircuitOpen:
		return "open"
	case billingCircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// checkUserPlatformQuotaEligibility 在 standard 模式下检查 user × platform 日/周/月 quota。
// 返回 nil = 允许；返回 ErrUserPlatform{Daily/Weekly/Monthly}QuotaExhausted = 拒绝（带 window_resets_at metadata）。
// checkUserPlatformQuotaEligibility 检查用户在指定平台的 USD 配额。
//
// 流程（Redis-first / DB-fallback）：
//  1. 先读 Redis cache；若命中且 SchemaVersion==1，直接用 entry 中的 limits 和 window_start 做校验，
//     免除 DB 查询。
//  2. cache MISS 或旧版 entry（SchemaVersion==0）→ 查 DB 回填完整 entry（含 limits/window_start）。
//  3. Redis 故障（err != nil）→ fail-open，查 DB 做一次性检查，不回填。
func (s *BillingCacheService) checkUserPlatformQuotaEligibility(
	ctx context.Context,
	userID int64,
	platform string,
) error {
	if platform == "" || s.userPlatformQuotaRepo == nil {
		return nil
	}

	// cache 未配置（如简化部署 / 单测路径）→ 直接走 DB 查询，避免 nil panic。
	// 其他 check* 方法（balance/subscription/rate-limit）也有类似守卫。
	var (
		entry    *UserPlatformQuotaCacheEntry
		ok       bool
		cacheErr error
	)
	if s.cache != nil {
		entry, ok, cacheErr = s.cache.GetUserPlatformQuotaCache(ctx, userID, platform)
	} else {
		// 标记为"cache 故障"分支：跳过 HIT 路径、不回填、走 DB 一次性检查
		cacheErr = errBillingCacheUnavailable
	}

	// --- cache HIT with current schema → 直接用 entry，不查 DB ---
	if cacheErr == nil && ok && entry != nil && entry.SchemaVersion == UserPlatformQuotaCacheSchemaV1 {
		now := time.Now()
		dailyUsage := entry.DailyUsageUSD
		weeklyUsage := entry.WeeklyUsageUSD
		monthlyUsage := entry.MonthlyUsageUSD
		// 若窗口已更新（DB 已重置但 cache 尚未失效）,将对应 usage 清零再做比较,
		// 同时记录新窗口起点用于后续刷新 cache entry。
		// 本次请求用本地清零值继续判断;DB 层 IncrementUsageWithReset 已有窗口自愈能力,
		// 持久化数据始终正确。
		windowExpired := false
		newDailyStart := entry.DailyWindowStart
		newWeeklyStart := entry.WeeklyWindowStart
		newMonthlyStart := entry.MonthlyWindowStart
		if quotaWindowExpired(entry.DailyWindowStart, timezone.StartOfDay(now)) {
			dailyUsage = 0
			windowExpired = true
			dayStart := timezone.StartOfDay(now)
			newDailyStart = &dayStart
		}
		if quotaWindowExpired(entry.WeeklyWindowStart, timezone.StartOfWeek(now)) {
			weeklyUsage = 0
			windowExpired = true
			weekStart := timezone.StartOfWeek(now)
			newWeeklyStart = &weekStart
		}
		if monthlyQuotaWindowExpired(entry.MonthlyWindowStart, now) {
			monthlyUsage = 0
			windowExpired = true
			monthStart := now
			newMonthlyStart = &monthStart
		}
		// 检测到任意窗口过期：用 reset 后的 entry 覆盖 Redis（而非 Delete）。
		// 旧实现 Delete 后,期间到达的 IncrUserPlatformQuotaUsage 调用让 Lua 看到
		// EXISTS=0 直接 return 0,并发请求的 cost 永久丢失,直到下次 cache MISS 回填。
		// 改为 SetCache 原子覆盖:key 不断链,Lua INCR 可在新窗口 entry 上正确累加。
		// 超时 50ms:覆盖正常路径与可接受抖动;Redis 异常时 hot path 不阻塞超过此值。
		// 用 context.Background()+短超时,避免请求 ctx 取消导致刷新丢失。
		// 显式 setCancel()(而非 defer):缩短 context 生命周期,避免 defer 延迟到函数返回。
		// isSentinel 判定「该 entry 无任何 limit」,涵盖两类,跨窗口命中时都跳过 refresh:
		//   1) A3 回填的 sentinel(DB 无行,短 TTL):refresh 会把短 TTL 误升级为 86400s,有害;
		//   2) DB 有行但三 limit 全未配置的用户(TTL 86400s):refresh 纯属无意义(TTL 升级本身无害)。
		// 两类的 enforcement(下方 limit!=nil 比较)都因 limit 全 nil 永远放行,跳过 refresh 均正确。
		isSentinel := entry.DailyLimitUSD == nil && entry.WeeklyLimitUSD == nil && entry.MonthlyLimitUSD == nil
		if windowExpired && s.cache != nil && !isSentinel {
			refreshed := &UserPlatformQuotaCacheEntry{
				DailyUsageUSD:      dailyUsage,
				WeeklyUsageUSD:     weeklyUsage,
				MonthlyUsageUSD:    monthlyUsage,
				SchemaVersion:      UserPlatformQuotaCacheSchemaV1,
				DailyLimitUSD:      entry.DailyLimitUSD,
				WeeklyLimitUSD:     entry.WeeklyLimitUSD,
				MonthlyLimitUSD:    entry.MonthlyLimitUSD,
				DailyWindowStart:   newDailyStart,
				WeeklyWindowStart:  newWeeklyStart,
				MonthlyWindowStart: newMonthlyStart,
			}
			ttl := time.Duration(s.cfg.Billing.UserPlatformQuotaCacheTTLSeconds) * time.Second
			setCtx, setCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			if setErr := s.cache.SetUserPlatformQuotaCache(setCtx, userID, platform, refreshed, ttl); setErr != nil {
				logger.LegacyPrintf("service.billing_cache",
					"Warning: refresh expired user platform quota cache failed user=%d platform=%s: %v",
					userID, platform, setErr)
			}
			setCancel()
		}
		if entry.DailyLimitUSD != nil && dailyUsage >= *entry.DailyLimitUSD {
			return withWindowResetsMetadata(ErrUserPlatformDailyQuotaExhausted, nextDailyReset(now))
		}
		if entry.WeeklyLimitUSD != nil && weeklyUsage >= *entry.WeeklyLimitUSD {
			return withWindowResetsMetadata(ErrUserPlatformWeeklyQuotaExhausted, nextWeeklyReset(now))
		}
		if entry.MonthlyLimitUSD != nil && monthlyUsage >= *entry.MonthlyLimitUSD {
			return withWindowResetsMetadata(ErrUserPlatformMonthlyQuotaExhausted, nextMonthlyResetFrom(entry.MonthlyWindowStart, now))
		}
		return nil
	}

	// --- cache MISS、旧版 entry 或 Redis 故障 → 查 DB（singleflight 合并并发回源）---
	// 使用 DoChan 而非 Do：avoid sharing the first caller's ctx among all dedupe followers.
	// 若第一个 caller 的 ctx 被取消（客户端断连），后续 caller 不受影响，仍由各自 ctx 控制超时。
	sfKey := strconv.FormatInt(userID, 10) + ":" + platform
	ch := s.quotaLoadSF.DoChan(sfKey, func() (any, error) {
		// 子查询用 detached context + 短超时，独立于任何 caller 的请求 ctx，
		// 防止"第一个 caller ctx 取消"使所有 follower 一起 fail。
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer bgCancel()
		return s.userPlatformQuotaRepo.GetByUserPlatform(bgCtx, userID, platform)
	})
	var (
		v     any
		dbErr error
	)
	select {
	case res := <-ch:
		v, dbErr = res.Val, res.Err
	case <-ctx.Done():
		// 当前 caller 的 ctx 被取消：fail-open，不阻断 (此请求已无意义)。
		logger.LegacyPrintf("service.billing_cache", "Warning: user platform quota check ctx cancelled user=%d platform=%s: %v (fail-open)", userID, platform, ctx.Err())
		return nil
	}
	if dbErr != nil {
		logger.LegacyPrintf("service.billing_cache", "Warning: load user platform quota failed user=%d platform=%s: %v (fail-open)", userID, platform, dbErr)
		return nil
	}
	rec, _ := v.(*UserPlatformQuotaRecord)
	if rec == nil {
		// 仅在 cache 可用且本次 GET 未出错时回填 sentinel:Redis GET 故障(cacheErr!=nil)
		// 时不回填,与下方 line ~1201 "Redis 故障时 fail-open:不回填" 保持一致,
		// 避免在 Redis 异常期做一次注定失败的 SET。
		if s.cache != nil && cacheErr == nil {
			now := time.Now()
			startOfDay := timezone.StartOfDay(now)
			startOfWeek := timezone.StartOfWeek(now)
			sentinel := &UserPlatformQuotaCacheEntry{
				SchemaVersion:      UserPlatformQuotaCacheSchemaV1,
				DailyWindowStart:   &startOfDay,
				WeeklyWindowStart:  &startOfWeek,
				MonthlyWindowStart: &now,
				// limits 全 nil, usage 全 0(零值)
			}
			sentinelTTL := time.Duration(s.cfg.Billing.UserPlatformQuotaSentinelTTLSeconds) * time.Second
			if sentinelTTL <= 0 {
				// 防御:TTL<=0 时 Redis EXPIRE 会立即删除整个 key(见 billing_cache.go 的 pipe.Expire),
				// sentinel 不持久化 → 每请求击穿 DB。配置缺失/误配为 0 时 fallback 到 1h。
				sentinelTTL = time.Hour
			}
			setCtx, setCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			if setErr := s.cache.SetUserPlatformQuotaCache(setCtx, userID, platform, sentinel, sentinelTTL); setErr != nil {
				userPlatformQuotaSentinelSetCacheErrorTotal.Add(1)
				logger.LegacyPrintf("service.billing_cache", "Warning: set sentinel quota cache failed user=%d platform=%s: %v", userID, platform, setErr)
			}
			setCancel()
		}
		return nil
	}

	now := time.Now()
	dailyUsage := rec.DailyUsageUSD
	weeklyUsage := rec.WeeklyUsageUSD
	monthlyUsage := rec.MonthlyUsageUSD
	if quotaWindowExpired(rec.DailyWindowStart, timezone.StartOfDay(now)) {
		dailyUsage = 0
	}
	if quotaWindowExpired(rec.WeeklyWindowStart, timezone.StartOfWeek(now)) {
		weeklyUsage = 0
	}
	if monthlyQuotaWindowExpired(rec.MonthlyWindowStart, now) {
		monthlyUsage = 0
	}

	// Redis 故障时 fail-open：不回填，直接用 DB 数据做一次性检查
	if cacheErr != nil {
		if rec.DailyLimitUSD != nil && dailyUsage >= *rec.DailyLimitUSD {
			return withWindowResetsMetadata(ErrUserPlatformDailyQuotaExhausted, nextDailyReset(now))
		}
		if rec.WeeklyLimitUSD != nil && weeklyUsage >= *rec.WeeklyLimitUSD {
			return withWindowResetsMetadata(ErrUserPlatformWeeklyQuotaExhausted, nextWeeklyReset(now))
		}
		if rec.MonthlyLimitUSD != nil && monthlyUsage >= *rec.MonthlyLimitUSD {
			return withWindowResetsMetadata(ErrUserPlatformMonthlyQuotaExhausted, nextMonthlyResetFrom(rec.MonthlyWindowStart, now))
		}
		return nil
	}

	// cache MISS 或旧版 entry → 回填完整 entry（含 limits 和 window_start）
	newEntry := &UserPlatformQuotaCacheEntry{
		DailyUsageUSD:      dailyUsage,
		WeeklyUsageUSD:     weeklyUsage,
		MonthlyUsageUSD:    monthlyUsage,
		SchemaVersion:      UserPlatformQuotaCacheSchemaV1,
		DailyLimitUSD:      rec.DailyLimitUSD,
		WeeklyLimitUSD:     rec.WeeklyLimitUSD,
		MonthlyLimitUSD:    rec.MonthlyLimitUSD,
		DailyWindowStart:   rec.DailyWindowStart,
		WeeklyWindowStart:  rec.WeeklyWindowStart,
		MonthlyWindowStart: rec.MonthlyWindowStart,
	}
	if s.cache != nil {
		ttl := time.Duration(s.cfg.Billing.UserPlatformQuotaCacheTTLSeconds) * time.Second
		// 与 HIT 过期回填路径（上文 SetCache 调用）保持一致：用 context.Background()+50ms,
		// 避免请求 ctx 提前取消（客户端断连/上游超时）导致 cache 回填失败,
		// 让下一次 preflight 仍然 MISS 并击穿到 DB（高并发下增大 DB 压力）。
		setCtx, setCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if setErr := s.cache.SetUserPlatformQuotaCache(setCtx, userID, platform, newEntry, ttl); setErr != nil {
			logger.LegacyPrintf("service.billing_cache", "Warning: set user platform quota cache failed user=%d platform=%s: %v", userID, platform, setErr)
		}
		setCancel()
	}

	if rec.DailyLimitUSD != nil && dailyUsage >= *rec.DailyLimitUSD {
		return withWindowResetsMetadata(ErrUserPlatformDailyQuotaExhausted, nextDailyReset(now))
	}
	if rec.WeeklyLimitUSD != nil && weeklyUsage >= *rec.WeeklyLimitUSD {
		return withWindowResetsMetadata(ErrUserPlatformWeeklyQuotaExhausted, nextWeeklyReset(now))
	}
	if rec.MonthlyLimitUSD != nil && monthlyUsage >= *rec.MonthlyLimitUSD {
		return withWindowResetsMetadata(ErrUserPlatformMonthlyQuotaExhausted, nextMonthlyResetFrom(rec.MonthlyWindowStart, now))
	}
	return nil
}

// withWindowResetsMetadata 给 quota error 附加 window_resets_at metadata（RFC3339）。
func withWindowResetsMetadata(err error, resetAt time.Time) error {
	appErr, ok := err.(*infraerrors.ApplicationError)
	if !ok || appErr == nil {
		return err
	}
	return appErr.WithMetadata(map[string]string{
		"window_resets_at": resetAt.Format(time.RFC3339),
	})
}

// nextDailyReset 计算下一个日窗口起点（次日全局时区 0 点）。
// 必须与 timezone.StartOfDay 同口径，否则 Retry-After 会偏差。
func nextDailyReset(now time.Time) time.Time {
	return timezone.StartOfDay(now).AddDate(0, 0, 1)
}

// nextWeeklyReset 计算下一个周窗口起点（下周一全局时区 0 点）。
// 必须与 timezone.StartOfWeek 同口径，否则 Retry-After 会偏差。
func nextWeeklyReset(now time.Time) time.Time {
	return timezone.StartOfWeek(now).AddDate(0, 0, 7)
}

// nextMonthlyResetFrom 返回 30 天滚动窗口的下次重置时间（start + 30d）。
// start 为 nil（未初始化）或已过期（now-start >= 30d，与 monthlyQuotaWindowExpired 同口径）时
// 退化为 now+30d：过期窗口会在下次 increment 时重置为 now，下次重置即 now+30d；
// 否则按 start 计算会得到一个过去的时间，使 Retry-After 落回 fallback 并触发客户端紧凑重试。
func nextMonthlyResetFrom(start *time.Time, now time.Time) time.Time {
	if start == nil || now.Sub(*start) >= 30*24*time.Hour {
		return now.Add(30 * 24 * time.Hour)
	}
	return start.Add(30 * 24 * time.Hour)
}

// quotaWindowExpired 判断窗口是否已过期：start 为 nil（未初始化）或在 currWindowStart 之前视为已过期。
func quotaWindowExpired(start *time.Time, currWindowStart time.Time) bool {
	if start == nil {
		return true
	}
	return start.Before(currWindowStart)
}

// monthlyQuotaWindowExpired 判断 30 天滚动月度窗口是否已过期。
// 过期条件：now - start >= 30×24h（与订阅模式 NeedsMonthlyReset 语义一致）。
// start 为 nil 时视为已过期（未初始化窗口）。
func monthlyQuotaWindowExpired(start *time.Time, now time.Time) bool {
	if start == nil {
		return true
	}
	return now.Sub(*start) >= 30*24*time.Hour
}

// HasUserPlatformQuotaLimit 判断该 user×platform 是否设了任一非 nil limit。
// 写入点守卫:无 limit 直接跳过 Redis 写 + 脏集标记,消除无谓写入。
// fail-safe:任何不确定(simple 模式除外)都返回 true 维持写入。
func (s *BillingCacheService) HasUserPlatformQuotaLimit(ctx context.Context, userID int64, platform string) bool {
	if s.cfg.RunMode == config.RunModeSimple {
		return false
	}
	if s.cache == nil {
		return true
	}
	entry, ok, err := s.cache.GetUserPlatformQuotaCache(ctx, userID, platform)
	if err != nil || !ok || entry == nil {
		return true
	}
	return entry.DailyLimitUSD != nil || entry.WeeklyLimitUSD != nil || entry.MonthlyLimitUSD != nil
}
