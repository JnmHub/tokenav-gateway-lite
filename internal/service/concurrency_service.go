package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"golang.org/x/sync/singleflight"
)

// ConcurrencyCache 定义并发控制的缓存接口
// 使用有序集合存储槽位，按时间戳清理过期条目
type ConcurrencyCache interface {
	// 账号槽位管理
	// 键格式: concurrency:account:{accountID}（有序集合，成员为 requestID）
	AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error)
	ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error
	GetAccountConcurrency(ctx context.Context, accountID int64) (int, error)
	GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)

	// 账号等待队列（账号级）
	IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error)
	DecrementAccountWaitCount(ctx context.Context, accountID int64) error
	GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error)

	// 用户槽位管理
	// 键格式: concurrency:user:{userID}（有序集合，成员为 requestID）
	AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error)
	ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error
	GetUserConcurrency(ctx context.Context, userID int64) (int, error)

	// 等待队列计数（只在首次创建时设置 TTL）
	IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error)
	DecrementWaitCount(ctx context.Context, userID int64) error

	// 批量负载查询（只读）
	GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error)
	GetUsersLoadBatch(ctx context.Context, users []UserWithConcurrency) (map[int64]*UserLoadInfo, error)

	// 清理过期槽位（后台任务）
	CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error

	// 启动时只清理超过 TTL 的遗留槽位；其他实例的活动槽位与等待计数必须保留。
	CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error
}

// AccountSlotRelease 是一次已完成请求需要归还的账号并发槽位。
// requestID 必须原样传给 Redis，保证重复释放仍然幂等。
type AccountSlotRelease struct {
	AccountID int64
	RequestID string
}

// AccountSlotBatchReleaseCache 是账号槽位批量释放的可选能力。
// gateway-lite 使用它把同一时刻完成的请求合并成 Redis pipeline（流水线）；
// 未实现该接口的缓存仍沿用逐条同步释放，不改变标准模式行为。
type AccountSlotBatchReleaseCache interface {
	ReleaseAccountSlots(ctx context.Context, releases []AccountSlotRelease) error
}

// AccountSlotBatchAcquireCache 是严格优先级调度使用的可选原子抢槽能力。
// candidates 的顺序就是调度顺序；实现只能占用第一个有容量的账号，不能为了
// 减少往返而同时预占多个账号，否则请求取消或进程退出时会产生幽灵占用。
type AccountSlotBatchAcquireCache interface {
	AcquireFirstAvailableAccountSlot(ctx context.Context, candidates []AccountWithConcurrency, requestID string) (accountID int64, acquired bool, err error)
}

// UserAPIKeySlotRelease 是一次已完成请求需要归还的用户和 API Key 并发槽位。
// 用户槽位是严格限流，API Key 槽位只用于统计；两者共享同一个 requestID。
type UserAPIKeySlotRelease struct {
	UserID         int64
	OrganizationID int64
	GroupID        int64
	APIKeyID       int64
	RequestID      string
}

// UserAPIKeySlotBatchReleaseCache 是用户和 API Key 槽位批量释放的可选能力。
// 延迟释放只会在极短窗口内少放行，不会突破用户并发上限；未实现时仍同步释放。
type UserAPIKeySlotBatchReleaseCache interface {
	ReleaseUserAndAPIKeySlots(ctx context.Context, releases []UserAPIKeySlotRelease) error
}

// APIKeyConcurrencyCache 是 API Key（接口密钥）并发统计的可选缓存能力。
// 这只用于后台展示当前活跃请求数，不参与限流；缓存不可用时必须失败放行。
type APIKeyConcurrencyCache interface {
	TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error
	GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error)
}

// GroupIngressConcurrencyCache 保存分组入口活动请求数。它仅用于后台观测，
// 不参与用户、企业、API Key 或账号并发放行；实现缺失或 Redis 故障时必须失败放行。
// 分组 ID 始终是请求最初进入的分组，不能改用故障转移后的最终分组。
type GroupIngressConcurrencyCache interface {
	TrackGroupIngressSlot(ctx context.Context, groupID int64, requestID string) error
	ReleaseGroupIngressSlot(ctx context.Context, groupID int64, requestID string) error
	GetGroupIngressConcurrencyBatch(ctx context.Context, groupIDs []int64) (map[int64]int, error)
}

// GroupIngressConcurrencyLeaseCache 是入口实时请求统计的可选续租能力。
// 普通请求可能持续超过槽位 TTL；续租只刷新仍存在的成员，不能重新创建已
// 被释放的成员，避免释放与心跳并发时产生幽灵计数。
type GroupIngressConcurrencyLeaseCache interface {
	RefreshGroupIngressSlot(ctx context.Context, groupID int64, requestID string) (bool, error)
	GroupIngressSlotRefreshInterval() time.Duration
}

var ErrGroupIngressConcurrencyUnavailable = errors.New("group ingress concurrency is unavailable")

// FailoverGroupConcurrencyCache 只占用目标分组的“每用户/每企业成员”槽位。
// 原请求已经持有入口分组和用户总并发槽，跨组时不能重复占用用户、企业或
// API Key 槽位；否则 max=1 的合法请求会被自己拒绝。
type FailoverGroupConcurrencyCache interface {
	AcquireFailoverGroupSlot(ctx context.Context, organizationID, userID, groupID int64, maxConcurrency int, requestID string) (bool, error)
	ReleaseFailoverGroupSlot(ctx context.Context, organizationID, userID, groupID int64, requestID string) error
}

// UserAPIKeyConcurrencyCache 把严格的用户并发占槽和 stats-only（仅统计）的
// API Key 槽位合并为一次 Redis 原子操作。未实现时自动退回原有两次调用。
type UserAPIKeyConcurrencyCache interface {
	AcquireUserAndAPIKeySlot(ctx context.Context, userID int64, maxConcurrency int, apiKeyID int64, requestID string) (bool, error)
	ReleaseUserAndAPIKeySlot(ctx context.Context, userID int64, apiKeyID int64, requestID string) error
}

// UserGroupAPIKeyConcurrencyCache 在一次 Redis 原子操作内同时执行用户等级并发、
// 最终分组每用户并发和 API Key 活跃统计，避免部分占槽及多 Key 绕过。
type UserGroupAPIKeyConcurrencyCache interface {
	AcquireUserGroupAndAPIKeySlot(ctx context.Context, userID int64, userMaxConcurrency int, groupID int64, groupMaxConcurrency int, apiKeyID int64, requestID string) (bool, error)
	ReleaseUserGroupAndAPIKeySlot(ctx context.Context, userID, groupID, apiKeyID int64, requestID string) error
}

// OrganizationMemberGroupAPIKeyConcurrencyCache 把企业总并发、成员并发、
// 最终分组每成员并发与 API Key 活跃统计放进同一个 Redis 原子门禁。
// 企业总并发按 organizationID 跨员工共享；成员与 Key 使用独立上限，避免
// 同一员工的一把 Key 打满后错误占用另一把 Key 的独立容量。
type OrganizationMemberGroupAPIKeyConcurrencyCache interface {
	AcquireOrganizationMemberGroupAndAPIKeySlot(
		ctx context.Context,
		organizationID int64,
		organizationMaxConcurrency int,
		userID int64,
		memberMaxConcurrency int,
		groupID int64,
		groupMaxConcurrency int,
		apiKeyID int64,
		apiKeyMaxConcurrency int,
		requestID string,
	) (bool, error)
	ReleaseOrganizationMemberGroupAndAPIKeySlot(ctx context.Context, organizationID, userID, groupID, apiKeyID int64, requestID string) error
}

// OpenAIWSIngressLeaseCache 管理客户端 WebSocket 连接的短期分布式租约。
// 它与请求并发槽位分离，因为两轮请求之间的空闲连接不应占用 turn 槽位。
type OpenAIWSIngressLeaseCache interface {
	AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error)
	RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error)
	ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error
}

const (
	openAIWSIngressLeaseTTL             = 60 * time.Second
	openAIWSIngressLeaseRefreshInterval = 20 * time.Second
	openAIWSIngressLeaseOperationTO     = 2 * time.Second
)

var ErrOpenAIWSIngressLeaseLost = errors.New("openai websocket ingress lease lost")

// OpenAIWSIngressLease 维持 Redis（缓存服务）租约；若一个完整 TTL 内无法确认所有权，
// 会取消连接上下文，避免 Redis 故障时无限突破同一 API Key 的连接上限。
type OpenAIWSIngressLease struct {
	ctx      context.Context
	cancel   context.CancelCauseFunc
	cache    OpenAIWSIngressLeaseCache
	apiKeyID int64
	leaseID  string

	stopOnce    sync.Once
	stopCh      chan struct{}
	refreshDone chan struct{}
}

func (l *OpenAIWSIngressLease) Context() context.Context {
	if l == nil || l.ctx == nil {
		return context.Background()
	}
	return l.ctx
}

func (l *OpenAIWSIngressLease) Release() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() {
		if l.stopCh != nil {
			close(l.stopCh)
		}
		if l.cancel != nil {
			l.cancel(nil)
		}
		// 等待正在执行的续租结束后再删除成员，防止“先删除、后续租”把已释放租约复活。
		if l.refreshDone != nil {
			<-l.refreshDone
		}
		if l.cache == nil || l.apiKeyID == 0 || l.leaseID == "" {
			return
		}
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), openAIWSIngressLeaseOperationTO)
		defer releaseCancel()
		if err := l.cache.ReleaseOpenAIWSIngressLease(releaseCtx, l.apiKeyID, l.leaseID); err != nil {
			logger.LegacyPrintf("service.concurrency", "Warning: failed to release OpenAI WebSocket ingress lease for api key %d: %v", l.apiKeyID, err)
		}
	})
}

func (l *OpenAIWSIngressLease) refreshLoop() {
	defer func() {
		if l != nil && l.refreshDone != nil {
			close(l.refreshDone)
		}
	}()
	if l == nil || l.cache == nil {
		return
	}
	ticker := time.NewTicker(openAIWSIngressLeaseRefreshInterval)
	defer ticker.Stop()
	lastConfirmedAt := time.Now()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-ticker.C:
			var lost bool
			lastConfirmedAt, lost = l.refresh(lastConfirmedAt)
			if lost {
				l.cancel(ErrOpenAIWSIngressLeaseLost)
				return
			}
		}
	}
}

// refresh 不会在成员丢失后重新创建租约；临时 Redis 错误最多容忍一个租约 TTL。
func (l *OpenAIWSIngressLease) refresh(lastConfirmedAt time.Time) (time.Time, bool) {
	refreshCtx, refreshCancel := context.WithTimeout(context.Background(), openAIWSIngressLeaseOperationTO)
	owned, err := l.cache.RefreshOpenAIWSIngressLease(refreshCtx, l.apiKeyID, l.leaseID)
	refreshCancel()
	if err == nil && owned {
		return time.Now(), false
	}
	if err == nil {
		err = ErrOpenAIWSIngressLeaseLost
	}
	elapsed := time.Since(lastConfirmedAt)
	logger.LegacyPrintf("service.concurrency", "Warning: OpenAI WebSocket ingress lease refresh failed for api key %d (unconfirmed=%s): %v", l.apiKeyID, elapsed, err)
	if errors.Is(err, ErrOpenAIWSIngressLeaseLost) || elapsed >= openAIWSIngressLeaseTTL {
		return lastConfirmedAt, true
	}
	return lastConfirmedAt, false
}

var (
	requestIDPrefix  = initRequestIDPrefix()
	requestIDCounter atomic.Uint64
)

func initRequestIDPrefix() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return "r" + strconv.FormatUint(binary.BigEndian.Uint64(b), 36)
	}
	fallback := uint64(time.Now().UnixNano()) ^ (uint64(os.Getpid()) << 16)
	return "r" + strconv.FormatUint(fallback, 36)
}

func RequestIDPrefix() string {
	return requestIDPrefix
}

func generateRequestID() string {
	seq := requestIDCounter.Add(1)
	return requestIDPrefix + "-" + strconv.FormatUint(seq, 36)
}

func (s *ConcurrencyService) CleanupStaleProcessSlots(ctx context.Context) error {
	if s == nil || s.cache == nil {
		return nil
	}
	return s.cache.CleanupStaleProcessSlots(ctx, RequestIDPrefix())
}

const (
	defaultAccountLoadBatchCacheTTL    = 200 * time.Millisecond
	accountLoadBatchFetchTimeout       = 3 * time.Second
	maxAccountLoadBatchCacheEntries    = 256
	apiKeyConcurrencyFetchTimeout      = 3 * time.Second
	apiKeySlotTrackTimeout             = 2 * time.Second
	groupIngressFetchTimeout           = 3 * time.Second
	groupIngressTrackTimeout           = 150 * time.Millisecond
	groupIngressRefreshTimeout         = 2 * time.Second
	defaultAccountReleaseQueueSize     = 32768
	defaultAccountReleaseBatchSize     = 256
	defaultAccountReleaseFlushDelay    = 2 * time.Millisecond
	accountReleaseOperationTimeout     = 5 * time.Second
	defaultUserAPIKeyReleaseQueueSize  = 32768
	defaultUserAPIKeyReleaseBatchSize  = 256
	defaultUserAPIKeyReleaseFlushDelay = 2 * time.Millisecond
	userAPIKeyReleaseOperationTimeout  = 5 * time.Second
	userAPIKeyReleaseRetryDelay        = 10 * time.Millisecond
)

// ConcurrencyService 管理账号和用户的并发限制。
type ConcurrencyService struct {
	cache ConcurrencyCache

	accountLoadCacheTTL atomic.Int64
	accountLoadCacheMu  sync.RWMutex
	accountLoadCache    map[string]cachedAccountLoadBatch
	accountLoadGroup    singleflight.Group

	accountReleaseStartOnce sync.Once
	accountReleaseQueue     chan AccountSlotRelease
	accountReleaseBatch     AccountSlotBatchReleaseCache
	accountReleaseBatchSize int
	accountReleaseDelay     time.Duration
	accountReleaseEnqueued  atomic.Uint64
	accountReleaseBatches   atomic.Uint64
	accountReleaseCompleted atomic.Uint64
	accountReleaseFailures  atomic.Uint64
	accountReleaseQueueFull atomic.Uint64
	accountReleaseFallbacks atomic.Uint64

	userAPIKeyReleaseStartOnce sync.Once
	userAPIKeyReleaseQueue     chan UserAPIKeySlotRelease
	userAPIKeyReleaseBatch     UserAPIKeySlotBatchReleaseCache
	userAPIKeyReleaseBatchSize int
	userAPIKeyReleaseDelay     time.Duration
	userAPIKeyReleaseEnqueued  atomic.Uint64
	userAPIKeyReleaseBatches   atomic.Uint64
	userAPIKeyReleaseCompleted atomic.Uint64
	userAPIKeyReleaseFailures  atomic.Uint64
	userAPIKeyReleaseRetries   atomic.Uint64
	userAPIKeyReleaseQueueFull atomic.Uint64
	userAPIKeyReleaseFallbacks atomic.Uint64
}

// AccountSlotReleaseRuntimeStats 是账号槽位异步释放队列的只读运行指标。
type AccountSlotReleaseRuntimeStats struct {
	QueueLength       int
	QueueCapacity     int
	EnqueuedTotal     uint64
	BatchTotal        uint64
	CompletedTotal    uint64
	BatchFailureTotal uint64
	QueueFullTotal    uint64
	SyncFallbackTotal uint64
}

// UserAPIKeySlotReleaseRuntimeStats 是用户/API Key 槽位异步释放队列的只读运行指标。
type UserAPIKeySlotReleaseRuntimeStats struct {
	QueueLength       int
	QueueCapacity     int
	EnqueuedTotal     uint64
	BatchTotal        uint64
	CompletedTotal    uint64
	BatchFailureTotal uint64
	RetryTotal        uint64
	QueueFullTotal    uint64
	SyncFallbackTotal uint64
}

type cachedAccountLoadBatch struct {
	loadMap   map[int64]*AccountLoadInfo
	expiresAt time.Time
}

// NewConcurrencyService 创建并发控制服务。
func NewConcurrencyService(cache ConcurrencyCache) *ConcurrencyService {
	svc := &ConcurrencyService{
		cache:            cache,
		accountLoadCache: make(map[string]cachedAccountLoadBatch),
	}
	svc.SetAccountLoadBatchCacheTTL(defaultAccountLoadBatchCacheTTL)
	return svc
}

// StartAccountSlotReleaseWorker 启动 gateway-lite 的账号槽位批量释放工作协程。
// 只有底层缓存实现批量接口时才启用；服务启动后、接收请求前调用一次即可。
func (s *ConcurrencyService) StartAccountSlotReleaseWorker() {
	s.startAccountSlotReleaseWorker(defaultAccountReleaseQueueSize, defaultAccountReleaseBatchSize, defaultAccountReleaseFlushDelay)
}

func (s *ConcurrencyService) startAccountSlotReleaseWorker(queueSize, batchSize int, flushDelay time.Duration) {
	if s == nil || queueSize <= 0 || batchSize <= 0 || flushDelay <= 0 {
		return
	}
	batchCache, ok := s.cache.(AccountSlotBatchReleaseCache)
	if !ok || batchCache == nil {
		return
	}
	s.accountReleaseStartOnce.Do(func() {
		s.accountReleaseBatch = batchCache
		s.accountReleaseBatchSize = batchSize
		s.accountReleaseDelay = flushDelay
		s.accountReleaseQueue = make(chan AccountSlotRelease, queueSize)
		go s.runAccountSlotReleaseWorker()
	})
}

// AccountSlotReleaseRuntimeStats 返回内存指标，不访问 Redis。
func (s *ConcurrencyService) AccountSlotReleaseRuntimeStats() AccountSlotReleaseRuntimeStats {
	if s == nil {
		return AccountSlotReleaseRuntimeStats{}
	}
	stats := AccountSlotReleaseRuntimeStats{
		EnqueuedTotal:     s.accountReleaseEnqueued.Load(),
		BatchTotal:        s.accountReleaseBatches.Load(),
		CompletedTotal:    s.accountReleaseCompleted.Load(),
		BatchFailureTotal: s.accountReleaseFailures.Load(),
		QueueFullTotal:    s.accountReleaseQueueFull.Load(),
		SyncFallbackTotal: s.accountReleaseFallbacks.Load(),
	}
	if queue := s.accountReleaseQueue; queue != nil {
		stats.QueueLength = len(queue)
		stats.QueueCapacity = cap(queue)
	}
	return stats
}

// StartUserAPIKeySlotReleaseWorker 启动用户/API Key 槽位批量释放工作协程。
func (s *ConcurrencyService) StartUserAPIKeySlotReleaseWorker() {
	s.startUserAPIKeySlotReleaseWorker(defaultUserAPIKeyReleaseQueueSize, defaultUserAPIKeyReleaseBatchSize, defaultUserAPIKeyReleaseFlushDelay)
}

func (s *ConcurrencyService) startUserAPIKeySlotReleaseWorker(queueSize, batchSize int, flushDelay time.Duration) {
	if s == nil || queueSize <= 0 || batchSize <= 0 || flushDelay <= 0 {
		return
	}
	batchCache, batchOK := s.cache.(UserAPIKeySlotBatchReleaseCache)
	singleCache, singleOK := s.cache.(UserAPIKeyConcurrencyCache)
	if !batchOK || batchCache == nil || !singleOK || singleCache == nil {
		return
	}
	s.userAPIKeyReleaseStartOnce.Do(func() {
		s.userAPIKeyReleaseBatch = batchCache
		s.userAPIKeyReleaseBatchSize = batchSize
		s.userAPIKeyReleaseDelay = flushDelay
		s.userAPIKeyReleaseQueue = make(chan UserAPIKeySlotRelease, queueSize)
		go s.runUserAPIKeySlotReleaseWorker()
	})
}

// UserAPIKeySlotReleaseRuntimeStats 返回内存指标，不访问 Redis。
func (s *ConcurrencyService) UserAPIKeySlotReleaseRuntimeStats() UserAPIKeySlotReleaseRuntimeStats {
	if s == nil {
		return UserAPIKeySlotReleaseRuntimeStats{}
	}
	stats := UserAPIKeySlotReleaseRuntimeStats{
		EnqueuedTotal:     s.userAPIKeyReleaseEnqueued.Load(),
		BatchTotal:        s.userAPIKeyReleaseBatches.Load(),
		CompletedTotal:    s.userAPIKeyReleaseCompleted.Load(),
		BatchFailureTotal: s.userAPIKeyReleaseFailures.Load(),
		RetryTotal:        s.userAPIKeyReleaseRetries.Load(),
		QueueFullTotal:    s.userAPIKeyReleaseQueueFull.Load(),
		SyncFallbackTotal: s.userAPIKeyReleaseFallbacks.Load(),
	}
	if queue := s.userAPIKeyReleaseQueue; queue != nil {
		stats.QueueLength = len(queue)
		stats.QueueCapacity = cap(queue)
	}
	return stats
}

func (s *ConcurrencyService) runUserAPIKeySlotReleaseWorker() {
	queue := s.userAPIKeyReleaseQueue
	if queue == nil || s.userAPIKeyReleaseBatch == nil {
		return
	}
	batch := make([]UserAPIKeySlotRelease, 0, s.userAPIKeyReleaseBatchSize)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for first := range queue {
		batch = append(batch[:0], first)
		timer.Reset(s.userAPIKeyReleaseDelay)
	collect:
		for len(batch) < s.userAPIKeyReleaseBatchSize {
			select {
			case release := <-queue:
				batch = append(batch, release)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.flushUserAPIKeySlotReleases(batch)
	}
}

func (s *ConcurrencyService) flushUserAPIKeySlotReleases(batch []UserAPIKeySlotRelease) {
	if len(batch) == 0 || s.userAPIKeyReleaseBatch == nil {
		return
	}
	s.userAPIKeyReleaseBatches.Add(1)
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), userAPIKeyReleaseOperationTimeout)
		err = s.userAPIKeyReleaseBatch.ReleaseUserAndAPIKeySlots(ctx, batch)
		cancel()
		if err == nil {
			s.userAPIKeyReleaseCompleted.Add(uint64(len(batch)))
			// 释放即通知：批量释放已落 Redis，逐个用户唤醒本进程里等待该槽位的请求。
			notifyReleasedUserSlots(batch)
			return
		}
		if attempt == 0 {
			// 批量删除是幂等的；一次短延迟重试可吸收瞬时 Redis 错误，
			// 同时避免失败时逐条重放形成 Redis 请求风暴。
			s.userAPIKeyReleaseRetries.Add(1)
			time.Sleep(userAPIKeyReleaseRetryDelay)
		}
	}
	s.userAPIKeyReleaseFailures.Add(1)
	logger.LegacyPrintf("service.concurrency", "Warning: failed to batch release %d user/api key slots after retry: %v", len(batch), err)
}

func (s *ConcurrencyService) releaseUserAPIKeySlot(cache UserAPIKeyConcurrencyCache, userID, apiKeyID int64, requestID string) {
	if s == nil || cache == nil {
		return
	}
	release := UserAPIKeySlotRelease{UserID: userID, APIKeyID: apiKeyID, RequestID: requestID}
	if queue := s.userAPIKeyReleaseQueue; queue != nil {
		select {
		case queue <- release:
			s.userAPIKeyReleaseEnqueued.Add(1)
			return
		default:
			// 队列满时不能丢释放，否则用户可能被限制到槽位 TTL 到期。
			s.userAPIKeyReleaseQueueFull.Add(1)
			s.userAPIKeyReleaseFallbacks.Add(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), userAPIKeyReleaseOperationTimeout)
	defer cancel()
	if err := cache.ReleaseUserAndAPIKeySlot(ctx, userID, apiKeyID, requestID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to release combined user/api key slot user=%d api_key=%d req=%s: %v", userID, apiKeyID, requestID, err)
		return
	}
	NotifySlotReleased(SlotReleaseTypeUser, userID)
}

func (s *ConcurrencyService) runAccountSlotReleaseWorker() {
	queue := s.accountReleaseQueue
	if queue == nil || s.accountReleaseBatch == nil {
		return
	}
	batch := make([]AccountSlotRelease, 0, s.accountReleaseBatchSize)
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for first := range queue {
		batch = append(batch[:0], first)
		timer.Reset(s.accountReleaseDelay)
	collect:
		for len(batch) < s.accountReleaseBatchSize {
			select {
			case release := <-queue:
				batch = append(batch, release)
			case <-timer.C:
				break collect
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		s.flushAccountSlotReleases(batch)
	}
}

func (s *ConcurrencyService) flushAccountSlotReleases(batch []AccountSlotRelease) {
	if len(batch) == 0 || s.accountReleaseBatch == nil {
		return
	}
	s.accountReleaseBatches.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), accountReleaseOperationTimeout)
	err := s.accountReleaseBatch.ReleaseAccountSlots(ctx, batch)
	cancel()
	if err != nil {
		s.accountReleaseFailures.Add(1)
		logger.LegacyPrintf("service.concurrency", "Warning: failed to batch release %d account slots: %v", len(batch), err)
		return
	}
	s.accountReleaseCompleted.Add(uint64(len(batch)))
	// 释放即通知：批量释放已落 Redis，逐个账号唤醒本进程里等待该槽位的请求。
	notifyReleasedAccountSlots(batch)
}

func (s *ConcurrencyService) releaseAccountSlot(accountID int64, requestID string) {
	if s == nil || s.cache == nil {
		return
	}
	release := AccountSlotRelease{AccountID: accountID, RequestID: requestID}
	if queue := s.accountReleaseQueue; queue != nil {
		select {
		case queue <- release:
			s.accountReleaseEnqueued.Add(1)
			return
		default:
			// 有界队列满时绝不能丢释放；同步执行会增加当前请求延迟，
			// 但可避免账号槽位一直占用到 TTL 到期。
			s.accountReleaseQueueFull.Add(1)
			s.accountReleaseFallbacks.Add(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountReleaseOperationTimeout)
	defer cancel()
	if err := s.cache.ReleaseAccountSlot(ctx, accountID, requestID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to release account slot for %d (req=%s): %v", accountID, requestID, err)
		return
	}
	// 队列满时的同步兜底释放同样要通知等待者，见 slot_release_notify.go。
	NotifySlotReleased(SlotReleaseTypeAccount, accountID)
}

// releaseAccountSlotImmediately 绕过短暂的批量释放窗口。只用于选号阶段
// 替换刚抢到、但因并发上限已变化而不能继续使用的槽位；正常请求完成仍走
// releaseAccountSlot，避免把高吞吐释放路径退化成逐条 Redis 往返。
func (s *ConcurrencyService) releaseAccountSlotImmediately(accountID int64, requestID string) {
	if s == nil || s.cache == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountReleaseOperationTimeout)
	defer cancel()
	if err := s.cache.ReleaseAccountSlot(ctx, accountID, requestID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to immediately release account slot for %d (req=%s): %v", accountID, requestID, err)
		return
	}
	// 释放即通知：唤醒本进程里等待该账号槽位的请求，见 slot_release_notify.go。
	NotifySlotReleased(SlotReleaseTypeAccount, accountID)
}

// AcquireOpenAIWSIngressLease 为整条客户端 WebSocket 连接预留一个跨实例槽位。
// 正数上限下缓存缺失或 Redis 获取失败必须 fail-closed（失败关闭），不能绕过连接上限。
func (s *ConcurrencyService) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int) (*OpenAIWSIngressLease, bool, error) {
	if maxConnections <= 0 {
		return nil, true, nil
	}
	if s == nil || s.cache == nil || apiKeyID == 0 {
		return nil, false, errors.New("openai websocket ingress lease cache is unavailable")
	}
	cache, ok := s.cache.(OpenAIWSIngressLeaseCache)
	if !ok {
		return nil, false, errors.New("openai websocket ingress lease cache is unsupported")
	}
	leaseID := generateRequestID()
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	acquireCtx, acquireCancel := context.WithTimeout(baseCtx, openAIWSIngressLeaseOperationTO)
	acquired, err := cache.AcquireOpenAIWSIngressLease(acquireCtx, apiKeyID, maxConnections, leaseID)
	acquireCancel()
	if err != nil || !acquired {
		return nil, acquired, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leaseCtx, leaseCancel := context.WithCancelCause(ctx)
	lease := &OpenAIWSIngressLease{
		ctx:         leaseCtx,
		cancel:      leaseCancel,
		cache:       cache,
		apiKeyID:    apiKeyID,
		leaseID:     leaseID,
		stopCh:      make(chan struct{}),
		refreshDone: make(chan struct{}),
	}
	go lease.refreshLoop()
	return lease, true, nil
}

// SetAccountLoadBatchCacheTTL 设置账号负载批量读取的极短 TTL 缓存；非正数表示禁用缓存。
func (s *ConcurrencyService) SetAccountLoadBatchCacheTTL(ttl time.Duration) {
	if s == nil {
		return
	}
	s.accountLoadCacheTTL.Store(int64(ttl))
	if ttl <= 0 {
		s.accountLoadCacheMu.Lock()
		s.accountLoadCache = make(map[string]cachedAccountLoadBatch)
		s.accountLoadCacheMu.Unlock()
	}
}

// AcquireResult represents the result of acquiring a concurrency slot
type AcquireResult struct {
	Acquired    bool
	ReleaseFunc func() // Must be called when done (typically via defer)

	// releaseImmediateFunc is reserved for selection-time slot replacement.
	// Normal request completion keeps using ReleaseFunc so the batched release
	// worker can coalesce Redis（缓存服务）writes.
	releaseImmediateFunc func()
}

func (r *AcquireResult) releaseImmediately() {
	if r == nil {
		return
	}
	if r.releaseImmediateFunc != nil {
		r.releaseImmediateFunc()
		return
	}
	if r.ReleaseFunc != nil {
		r.ReleaseFunc()
	}
}

type AccountWithConcurrency struct {
	ID             int64
	MaxConcurrency int
}

type UserWithConcurrency struct {
	ID             int64
	MaxConcurrency int
}

type AccountLoadInfo struct {
	AccountID          int64
	CurrentConcurrency int
	WaitingCount       int
	LoadRate           int // 0-100+ (percent)
}

type UserLoadInfo struct {
	UserID             int64
	CurrentConcurrency int
	WaitingCount       int
	LoadRate           int // 0-100+ (percent)
}

// AcquireAccountSlot attempts to acquire a concurrency slot for an account.
// If the account is at max concurrency, it waits until a slot is available or timeout.
// Returns a release function that MUST be called when the request completes.
func (s *ConcurrencyService) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (*AcquireResult, error) {
	// If maxConcurrency is 0 or negative, no limit
	if maxConcurrency <= 0 {
		return &AcquireResult{
			Acquired:             true,
			ReleaseFunc:          func() {}, // no-op
			releaseImmediateFunc: func() {},
		}, nil
	}

	// Generate unique request ID for this slot
	requestID := generateRequestID()

	acquired, err := s.cache.AcquireAccountSlot(ctx, accountID, maxConcurrency, requestID)
	if err != nil {
		return nil, err
	}

	if acquired {
		return &AcquireResult{
			Acquired: true,
			ReleaseFunc: func() {
				s.releaseAccountSlot(accountID, requestID)
			},
			releaseImmediateFunc: func() {
				s.releaseAccountSlotImmediately(accountID, requestID)
			},
		}, nil
	}

	return &AcquireResult{
		Acquired:    false,
		ReleaseFunc: nil,
	}, nil
}

// AcquireFirstAvailableAccountSlot 按给定顺序原子占用第一个可用账号槽位。
// Redis（缓存服务）实现可把一批候选合并为一次网络往返；未实现批量能力的
// 测试桩和兼容缓存仍逐个尝试，保证调用方不会因性能能力缺失而改变调度结果。
func (s *ConcurrencyService) AcquireFirstAvailableAccountSlot(ctx context.Context, candidates []AccountWithConcurrency) (int64, *AcquireResult, error) {
	if len(candidates) == 0 {
		return 0, &AcquireResult{Acquired: false}, nil
	}

	// 不限并发账号不需要 Redis（缓存服务）。如果它位于有限并发候选之后，
	// 只把它前面的有限候选交给批量脚本；前缀全部满载后直接返回该账号，
	// 保持旧逻辑在 Redis 故障时对首个不限并发账号的 fail-open（失败放行）语义。
	firstUnlimited := -1
	for index, candidate := range candidates {
		if candidate.MaxConcurrency <= 0 {
			firstUnlimited = index
			break
		}
	}
	if firstUnlimited == 0 {
		return candidates[0].ID, &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	if s == nil || s.cache == nil {
		return 0, nil, errors.New("account concurrency cache is unavailable")
	}
	batchCandidates := candidates
	if firstUnlimited > 0 {
		batchCandidates = candidates[:firstUnlimited]
	}

	if batchCache, ok := s.cache.(AccountSlotBatchAcquireCache); ok {
		requestID := generateRequestID()
		accountID, acquired, err := batchCache.AcquireFirstAvailableAccountSlot(ctx, batchCandidates, requestID)
		if err != nil {
			return 0, nil, err
		}
		if !acquired {
			if firstUnlimited > 0 {
				return candidates[firstUnlimited].ID, &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
			}
			return 0, &AcquireResult{Acquired: false}, nil
		}

		for _, candidate := range batchCandidates {
			if candidate.ID != accountID {
				continue
			}
			if candidate.MaxConcurrency <= 0 {
				return accountID, &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
			}
			return accountID, &AcquireResult{
				Acquired: true,
				ReleaseFunc: func() {
					s.releaseAccountSlot(accountID, requestID)
				},
				releaseImmediateFunc: func() {
					s.releaseAccountSlotImmediately(accountID, requestID)
				},
			}, nil
		}
		return 0, nil, errors.New("account concurrency cache returned an unknown account")
	}

	for _, candidate := range candidates {
		result, err := s.AcquireAccountSlot(ctx, candidate.ID, candidate.MaxConcurrency)
		if err != nil {
			return 0, nil, err
		}
		if result != nil && result.Acquired {
			return candidate.ID, result, nil
		}
	}
	return 0, &AcquireResult{Acquired: false}, nil
}

// AcquireUserSlot attempts to acquire a concurrency slot for a user.
// If the user is at max concurrency, it waits until a slot is available or timeout.
// Returns a release function that MUST be called when the request completes.
func (s *ConcurrencyService) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int) (*AcquireResult, error) {
	// If maxConcurrency is 0 or negative, no limit
	if maxConcurrency <= 0 {
		return &AcquireResult{
			Acquired:    true,
			ReleaseFunc: func() {}, // no-op
		}, nil
	}

	// Generate unique request ID for this slot
	requestID := generateRequestID()

	acquired, err := s.cache.AcquireUserSlot(ctx, userID, maxConcurrency, requestID)
	if err != nil {
		return nil, err
	}

	if acquired {
		return &AcquireResult{
			Acquired: true,
			ReleaseFunc: func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := s.cache.ReleaseUserSlot(bgCtx, userID, requestID); err != nil {
					logger.LegacyPrintf("service.concurrency", "Warning: failed to release user slot for %d (req=%s): %v", userID, requestID, err)
					return
				}
				// 释放即通知：唤醒本进程里等待该用户槽位的请求，见 slot_release_notify.go。
				NotifySlotReleased(SlotReleaseTypeUser, userID)
			},
		}, nil
	}

	return &AcquireResult{
		Acquired:    false,
		ReleaseFunc: nil,
	}, nil
}

// AcquireUserSlotForAPIKey 优先用一次 Redis 往返同时完成用户限流与 Key 活跃统计。
// 用户槽位是强约束；只有 Redis 原子操作成功才放行。API Key 统计不单独影响结果。
func (s *ConcurrencyService) AcquireUserSlotForAPIKey(ctx context.Context, userID int64, maxConcurrency int, apiKeyID int64) (*AcquireResult, error) {
	return s.AcquireUserGroupSlotForAPIKey(ctx, userID, maxConcurrency, 0, 0, apiKeyID)
}

// AcquireOrganizationMemberGroupSlotForAPIKey 对企业请求执行完整的共享并发门禁。
// 所有维度先检查、后一起写入；任一维度已满时 Redis 不留下部分槽位。
func (s *ConcurrencyService) AcquireOrganizationMemberGroupSlotForAPIKey(
	ctx context.Context,
	organizationID int64,
	organizationMaxConcurrency int,
	userID int64,
	memberMaxConcurrency int,
	groupID int64,
	groupMaxConcurrency int,
	apiKeyID int64,
	apiKeyMaxConcurrency int,
) (*AcquireResult, error) {
	if s == nil || s.cache == nil {
		return nil, errors.New("organization concurrency service is unavailable")
	}
	if organizationID <= 0 || userID <= 0 || apiKeyID == 0 {
		return nil, errors.New("organization concurrency identity is unavailable")
	}
	cache, ok := s.cache.(OrganizationMemberGroupAPIKeyConcurrencyCache)
	if !ok {
		return nil, errors.New("organization concurrency cache is unavailable")
	}
	requestID := generateRequestID()
	acquired, err := cache.AcquireOrganizationMemberGroupAndAPIKeySlot(
		ctx,
		organizationID,
		organizationMaxConcurrency,
		userID,
		memberMaxConcurrency,
		groupID,
		groupMaxConcurrency,
		apiKeyID,
		apiKeyMaxConcurrency,
		requestID,
	)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return &AcquireResult{Acquired: false}, nil
	}
	return &AcquireResult{Acquired: true, ReleaseFunc: func() {
		s.releaseOrganizationMemberGroupAPIKeySlot(cache, organizationID, userID, groupID, apiKeyID, requestID)
	}}, nil
}

// AcquireUserGroupSlotForAPIKey 同时检查用户等级并发和最终分组并发。
// 两个上限任一为 0 都只跳过该维度；API Key 槽位始终只用于统计。
func (s *ConcurrencyService) AcquireUserGroupSlotForAPIKey(ctx context.Context, userID int64, maxConcurrency int, groupID int64, groupMaxConcurrency int, apiKeyID int64) (*AcquireResult, error) {
	if s == nil {
		return nil, errors.New("concurrency service is unavailable")
	}
	if groupID > 0 && groupMaxConcurrency > 0 {
		// 分组硬限制必须能稳定识别用户和 API Key，不能在异常鉴权上下文下
		// 静默退回旧路径，否则同一用户可借由缺失身份绕过跨 Key 聚合上限。
		if userID <= 0 || apiKeyID == 0 {
			return nil, errors.New("group user concurrency identity is unavailable")
		}
		if cache, ok := s.cache.(UserGroupAPIKeyConcurrencyCache); ok {
			requestID := generateRequestID()
			acquired, err := cache.AcquireUserGroupAndAPIKeySlot(ctx, userID, maxConcurrency, groupID, groupMaxConcurrency, apiKeyID, requestID)
			if err != nil {
				return nil, err
			}
			if !acquired {
				return &AcquireResult{Acquired: false}, nil
			}
			return &AcquireResult{Acquired: true, ReleaseFunc: func() {
				s.releaseUserGroupAPIKeySlot(cache, userID, groupID, apiKeyID, requestID)
			}}, nil
		}
		return nil, errors.New("group user concurrency cache is unavailable")
	}
	if s.cache == nil || apiKeyID == 0 || maxConcurrency <= 0 {
		result, err := s.AcquireUserSlot(ctx, userID, maxConcurrency)
		if err != nil || result == nil || !result.Acquired || apiKeyID == 0 {
			return result, err
		}
		apiKeyRelease := s.TrackAPIKeySlot(ctx, apiKeyID)
		baseRelease := result.ReleaseFunc
		result.ReleaseFunc = func() {
			if baseRelease != nil {
				baseRelease()
			}
			if apiKeyRelease != nil {
				apiKeyRelease()
			}
		}
		return result, nil
	}
	cache, ok := s.cache.(UserAPIKeyConcurrencyCache)
	if !ok {
		result, err := s.AcquireUserSlot(ctx, userID, maxConcurrency)
		if err != nil || result == nil || !result.Acquired {
			return result, err
		}
		apiKeyRelease := s.TrackAPIKeySlot(ctx, apiKeyID)
		baseRelease := result.ReleaseFunc
		result.ReleaseFunc = func() {
			if baseRelease != nil {
				baseRelease()
			}
			if apiKeyRelease != nil {
				apiKeyRelease()
			}
		}
		return result, nil
	}

	requestID := generateRequestID()
	acquired, err := cache.AcquireUserAndAPIKeySlot(ctx, userID, maxConcurrency, apiKeyID, requestID)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return &AcquireResult{Acquired: false}, nil
	}
	return &AcquireResult{
		Acquired: true,
		ReleaseFunc: func() {
			s.releaseUserAPIKeySlot(cache, userID, apiKeyID, requestID)
		},
	}, nil
}

func (s *ConcurrencyService) releaseUserGroupAPIKeySlot(cache UserGroupAPIKeyConcurrencyCache, userID, groupID, apiKeyID int64, requestID string) {
	if s == nil || cache == nil {
		return
	}
	release := UserAPIKeySlotRelease{UserID: userID, GroupID: groupID, APIKeyID: apiKeyID, RequestID: requestID}
	if queue := s.userAPIKeyReleaseQueue; queue != nil {
		select {
		case queue <- release:
			s.userAPIKeyReleaseEnqueued.Add(1)
			return
		default:
			s.userAPIKeyReleaseQueueFull.Add(1)
			s.userAPIKeyReleaseFallbacks.Add(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), userAPIKeyReleaseOperationTimeout)
	defer cancel()
	if err := cache.ReleaseUserGroupAndAPIKeySlot(ctx, userID, groupID, apiKeyID, requestID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to release user/group/api key slot user=%d group=%d api_key=%d req=%s: %v", userID, groupID, apiKeyID, requestID, err)
		return
	}
	NotifySlotReleased(SlotReleaseTypeUser, userID)
}

func (s *ConcurrencyService) releaseOrganizationMemberGroupAPIKeySlot(cache OrganizationMemberGroupAPIKeyConcurrencyCache, organizationID, userID, groupID, apiKeyID int64, requestID string) {
	if s == nil || cache == nil {
		return
	}
	release := UserAPIKeySlotRelease{OrganizationID: organizationID, UserID: userID, GroupID: groupID, APIKeyID: apiKeyID, RequestID: requestID}
	if queue := s.userAPIKeyReleaseQueue; queue != nil {
		select {
		case queue <- release:
			s.userAPIKeyReleaseEnqueued.Add(1)
			return
		default:
			s.userAPIKeyReleaseQueueFull.Add(1)
			s.userAPIKeyReleaseFallbacks.Add(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), userAPIKeyReleaseOperationTimeout)
	defer cancel()
	if err := cache.ReleaseOrganizationMemberGroupAndAPIKeySlot(ctx, organizationID, userID, groupID, apiKeyID, requestID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to release organization/member/group/api key slot organization=%d user=%d group=%d api_key=%d req=%s: %v", organizationID, userID, groupID, apiKeyID, requestID, err)
	}
}

// TrackAPIKeySlot 记录一个 API Key 当前活跃请求槽位。
// 这个槽位是 stats-only（仅统计），不能影响真实请求；Redis 或实现缺失时返回 no-op。
func (s *ConcurrencyService) TrackAPIKeySlot(ctx context.Context, apiKeyID int64) func() {
	if s == nil || s.cache == nil || apiKeyID == 0 {
		return func() {}
	}
	cache, ok := s.cache.(APIKeyConcurrencyCache)
	if !ok {
		return func() {}
	}

	requestID := generateRequestID()
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	trackCtx, cancel := context.WithTimeout(baseCtx, apiKeySlotTrackTimeout)
	err := cache.TrackAPIKeySlot(trackCtx, apiKeyID, requestID)
	cancel()
	if err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to track api key slot for %d (req=%s): %v", apiKeyID, requestID, err)
		return func() {}
	}

	return func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := cache.ReleaseAPIKeySlot(bgCtx, apiKeyID, requestID); err != nil {
			logger.LegacyPrintf("service.concurrency", "Warning: failed to release api key slot for %d (req=%s): %v", apiKeyID, requestID, err)
		}
	}
}

// GetAPIKeyConcurrencyBatch 批量读取 API Key 当前活跃请求数。
// 统计数据是 best-effort（尽力而为）：缓存不可用或 Redis 错误时返回全 0，不影响接口可用性。
func (s *ConcurrencyService) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	result := zeroAPIKeyConcurrencyMap(apiKeyIDs)
	if len(apiKeyIDs) == 0 {
		return result, nil
	}
	if s == nil || s.cache == nil {
		return result, nil
	}
	cache, ok := s.cache.(APIKeyConcurrencyCache)
	if !ok {
		return result, nil
	}

	redisCtx, cancel := context.WithTimeout(context.Background(), apiKeyConcurrencyFetchTimeout)
	defer cancel()

	counts, err := cache.GetAPIKeyConcurrencyBatch(redisCtx, apiKeyIDs)
	if err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: get api key concurrency batch failed: %v", err)
		return result, nil
	}
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = counts[apiKeyID]
	}
	return result, nil
}

// TrackGroupIngressSlot records an active request at its original gateway-group
// entry point. This is deliberately stats-only: Redis failures return a no-op
// release function so observability can never reject a customer request.
// The member ID is generated locally rather than accepting a client request ID,
// because X-Request-ID and X-Client-Request-ID may be reused by callers.
func (s *ConcurrencyService) TrackGroupIngressSlot(ctx context.Context, groupID int64) func() {
	if s == nil || s.cache == nil || groupID <= 0 {
		return func() {}
	}
	cache, ok := s.cache.(GroupIngressConcurrencyCache)
	if !ok || cache == nil {
		return func() {}
	}
	requestID := generateRequestID()
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	trackCtx, cancel := context.WithTimeout(baseCtx, groupIngressTrackTimeout)
	err := cache.TrackGroupIngressSlot(trackCtx, groupID, requestID)
	cancel()
	if err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: failed to track group ingress slot group=%d req=%s: %v", groupID, requestID, err)
		return func() {}
	}

	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	heartbeatContext, cancelHeartbeat := context.WithCancel(context.Background())
	leaseCache, supportsRefresh := cache.(GroupIngressConcurrencyLeaseCache)
	refreshInterval := time.Duration(0)
	if supportsRefresh {
		refreshInterval = leaseCache.GroupIngressSlotRefreshInterval()
	}
	if refreshInterval > 0 {
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(refreshInterval)
			defer ticker.Stop()
			for {
				select {
				case <-stopHeartbeat:
					return
				case <-ticker.C:
					refreshCtx, refreshCancel := context.WithTimeout(heartbeatContext, groupIngressRefreshTimeout)
					owned, refreshErr := leaseCache.RefreshGroupIngressSlot(refreshCtx, groupID, requestID)
					refreshCancel()
					if refreshErr != nil {
						logger.LegacyPrintf("service.concurrency", "Warning: failed to refresh group ingress slot group=%d req=%s: %v", groupID, requestID, refreshErr)
						continue
					}
					if !owned {
						return
					}
				}
			}
		}()
	} else {
		close(heartbeatDone)
	}

	var releaseOnce sync.Once
	releaseNow := func() {
		releaseOnce.Do(func() {
			close(stopHeartbeat)
			cancelHeartbeat()
			<-heartbeatDone
			bgCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer releaseCancel()
			if err := cache.ReleaseGroupIngressSlot(bgCtx, groupID, requestID); err != nil {
				logger.LegacyPrintf("service.concurrency", "Warning: failed to release group ingress slot group=%d req=%s: %v", groupID, requestID, err)
			}
		})
	}
	stopOnContextDone := func() bool { return false }
	if ctx != nil {
		stopOnContextDone = context.AfterFunc(ctx, releaseNow)
	}
	return func() {
		_ = stopOnContextDone()
		releaseNow()
	}
}

// GetGroupIngressConcurrencyBatch reads the activity count for each original
// group in one Redis pipeline. This is not part of the customer request path:
// failures are returned to the admin capacity endpoint so it can show unknown
// instead of presenting an outage as a misleading zero.
func (s *ConcurrencyService) GetGroupIngressConcurrencyBatch(ctx context.Context, groupIDs []int64) (map[int64]int, error) {
	result := make(map[int64]int, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID > 0 {
			result[groupID] = 0
		}
	}
	if len(groupIDs) == 0 {
		return result, nil
	}
	if s == nil || s.cache == nil {
		return nil, ErrGroupIngressConcurrencyUnavailable
	}
	cache, ok := s.cache.(GroupIngressConcurrencyCache)
	if !ok || cache == nil {
		return nil, ErrGroupIngressConcurrencyUnavailable
	}
	fetchCtx, cancel := context.WithTimeout(context.Background(), groupIngressFetchTimeout)
	defer cancel()
	counts, err := cache.GetGroupIngressConcurrencyBatch(fetchCtx, groupIDs)
	if err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: get group ingress concurrency batch failed: %v", err)
		return nil, err
	}
	for _, groupID := range groupIDs {
		if groupID > 0 {
			result[groupID] = counts[groupID]
		}
	}
	return result, nil
}

// AcquireFailoverGroupSlot 在 A 已持有完整请求槽位的前提下，仅校验并占用 B
// 的每用户并发。目标未配置上限时返回空操作；配置了上限但 Redis 能力缺失
// 时失败关闭，避免跨组绕过管理员设置。
func (s *ConcurrencyService) AcquireFailoverGroupSlot(ctx context.Context, user *User, userID, groupID int64, maxConcurrency int) (*AcquireResult, error) {
	if maxConcurrency <= 0 {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	if s == nil || s.cache == nil || userID <= 0 || groupID <= 0 {
		return nil, errors.New("failover group concurrency identity is unavailable")
	}
	cache, ok := s.cache.(FailoverGroupConcurrencyCache)
	if !ok {
		return nil, errors.New("failover group concurrency cache is unavailable")
	}
	organizationID := int64(0)
	if user != nil {
		organizationID = user.OrganizationID
	}
	requestID := generateRequestID()
	acquired, err := cache.AcquireFailoverGroupSlot(ctx, organizationID, userID, groupID, maxConcurrency, requestID)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return &AcquireResult{Acquired: false}, nil
	}
	var releaseOnce sync.Once
	releaseNow := func() {
		releaseOnce.Do(func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if releaseErr := cache.ReleaseFailoverGroupSlot(bgCtx, organizationID, userID, groupID, requestID); releaseErr != nil {
				logger.LegacyPrintf("service.concurrency", "Warning: failed to release failover group slot group=%d user=%d: %v", groupID, userID, releaseErr)
			}
		})
	}
	stopOnContextDone := func() bool { return false }
	if ctx != nil {
		stopOnContextDone = context.AfterFunc(ctx, releaseNow)
	}
	return &AcquireResult{Acquired: true, ReleaseFunc: func() {
		_ = stopOnContextDone()
		releaseNow()
	}}, nil
}

func zeroAPIKeyConcurrencyMap(apiKeyIDs []int64) map[int64]int {
	result := make(map[int64]int, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		result[apiKeyID] = 0
	}
	return result
}

// ============================================
// Wait Queue Count Methods
// ============================================

// IncrementWaitCount attempts to increment the wait queue counter for a user.
// Returns true if successful, false if the wait queue is full.
// maxWait should be derived from the control-plane concurrency policy.
func (s *ConcurrencyService) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	if s.cache == nil {
		// Redis not available, allow request
		return true, nil
	}

	result, err := s.cache.IncrementWaitCount(ctx, userID, maxWait)
	if err != nil {
		// On error, allow the request to proceed (fail open)
		logger.LegacyPrintf("service.concurrency", "Warning: increment wait count failed for user %d: %v", userID, err)
		return true, nil
	}
	return result, nil
}

// DecrementWaitCount decrements the wait queue counter for a user.
// Should be called when a request completes or exits the wait queue.
func (s *ConcurrencyService) DecrementWaitCount(ctx context.Context, userID int64) {
	if s.cache == nil {
		return
	}

	// Use background context to ensure decrement even if original context is cancelled
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.cache.DecrementWaitCount(bgCtx, userID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: decrement wait count failed for user %d: %v", userID, err)
	}
}

// IncrementAccountWaitCount increments the wait queue counter for an account.
func (s *ConcurrencyService) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	if s.cache == nil {
		return true, nil
	}

	result, err := s.cache.IncrementAccountWaitCount(ctx, accountID, maxWait)
	if err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: increment wait count failed for account %d: %v", accountID, err)
		return true, nil
	}
	return result, nil
}

// DecrementAccountWaitCount decrements the wait queue counter for an account.
func (s *ConcurrencyService) DecrementAccountWaitCount(ctx context.Context, accountID int64) {
	if s.cache == nil {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.cache.DecrementAccountWaitCount(bgCtx, accountID); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: decrement wait count failed for account %d: %v", accountID, err)
	}
}

// GetAccountWaitingCount gets current wait queue count for an account.
func (s *ConcurrencyService) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	if s.cache == nil {
		return 0, nil
	}
	return s.cache.GetAccountWaitingCount(ctx, accountID)
}

// CalculateMaxWait returns the control-plane concurrency value without adding
// gateway-local extra capacity.
func CalculateMaxWait(userConcurrency int) int {
	if userConcurrency <= 0 {
		userConcurrency = 1
	}
	return userConcurrency
}

// GetAccountsLoadBatch 批量获取账号负载信息。
func (s *ConcurrencyService) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	return s.getAccountsLoadBatch(ctx, accounts, true)
}

// GetAccountsLoadBatchFresh 绕过极短 TTL 缓存，用于抢槽失败后的实时刷新兜底。
func (s *ConcurrencyService) GetAccountsLoadBatchFresh(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	return s.getAccountsLoadBatch(ctx, accounts, false)
}

func (s *ConcurrencyService) getAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency, allowCache bool) (map[int64]*AccountLoadInfo, error) {
	if len(accounts) == 0 {
		return map[int64]*AccountLoadInfo{}, nil
	}
	if s.cache == nil {
		return map[int64]*AccountLoadInfo{}, nil
	}

	ttl := time.Duration(s.accountLoadCacheTTL.Load())
	if !allowCache || ttl <= 0 {
		return s.fetchAccountsLoadBatch(ctx, accounts)
	}

	key := accountLoadBatchCacheKey(accounts)
	if cached, ok := s.getCachedAccountLoadBatch(key, time.Now()); ok {
		return cached, nil
	}

	value, err, _ := s.accountLoadGroup.Do(key, func() (any, error) {
		now := time.Now()
		if cached, ok := s.getCachedAccountLoadBatch(key, now); ok {
			return cached, nil
		}
		loadMap, fetchErr := s.fetchAccountsLoadBatch(ctx, accounts)
		if fetchErr != nil {
			return nil, fetchErr
		}
		cached := cloneAccountLoadMap(loadMap)
		s.storeCachedAccountLoadBatch(key, cached, now.Add(ttl))
		return cached, nil
	})
	if err != nil {
		return nil, err
	}
	loadMap, _ := value.(map[int64]*AccountLoadInfo)
	if loadMap == nil {
		return map[int64]*AccountLoadInfo{}, nil
	}
	return loadMap, nil
}

func (s *ConcurrencyService) fetchAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	if s.cache == nil {
		return map[int64]*AccountLoadInfo{}, nil
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	redisCtx, cancel := context.WithTimeout(baseCtx, accountLoadBatchFetchTimeout)
	defer cancel()
	return s.cache.GetAccountsLoadBatch(redisCtx, accounts)
}

func (s *ConcurrencyService) getCachedAccountLoadBatch(key string, now time.Time) (map[int64]*AccountLoadInfo, bool) {
	s.accountLoadCacheMu.RLock()
	cached, ok := s.accountLoadCache[key]
	s.accountLoadCacheMu.RUnlock()
	if !ok {
		return nil, false
	}
	if !now.Before(cached.expiresAt) {
		s.accountLoadCacheMu.Lock()
		if current, exists := s.accountLoadCache[key]; exists && !now.Before(current.expiresAt) {
			delete(s.accountLoadCache, key)
		}
		s.accountLoadCacheMu.Unlock()
		return nil, false
	}
	return cached.loadMap, true
}

func (s *ConcurrencyService) storeCachedAccountLoadBatch(key string, loadMap map[int64]*AccountLoadInfo, expiresAt time.Time) {
	s.accountLoadCacheMu.Lock()
	if s.accountLoadCache == nil {
		s.accountLoadCache = make(map[string]cachedAccountLoadBatch)
	}
	if len(s.accountLoadCache) >= maxAccountLoadBatchCacheEntries {
		now := time.Now()
		for cacheKey, cached := range s.accountLoadCache {
			if !now.Before(cached.expiresAt) {
				delete(s.accountLoadCache, cacheKey)
			}
		}
		for len(s.accountLoadCache) >= maxAccountLoadBatchCacheEntries {
			for cacheKey := range s.accountLoadCache {
				delete(s.accountLoadCache, cacheKey)
				break
			}
		}
	}
	s.accountLoadCache[key] = cachedAccountLoadBatch{
		loadMap:   loadMap,
		expiresAt: expiresAt,
	}
	s.accountLoadCacheMu.Unlock()
}

func accountLoadBatchCacheKey(accounts []AccountWithConcurrency) string {
	hash := sha256.New()
	var buf [16]byte
	for _, account := range accounts {
		binary.LittleEndian.PutUint64(buf[:8], uint64(account.ID))
		binary.LittleEndian.PutUint64(buf[8:], uint64(int64(account.MaxConcurrency)))
		_, _ = hash.Write(buf[:])
	}
	sum := hash.Sum(nil)
	return strconv.Itoa(len(accounts)) + ":" + hex.EncodeToString(sum)
}

func cloneAccountLoadMap(loadMap map[int64]*AccountLoadInfo) map[int64]*AccountLoadInfo {
	if len(loadMap) == 0 {
		return map[int64]*AccountLoadInfo{}
	}
	clone := make(map[int64]*AccountLoadInfo, len(loadMap))
	for accountID, loadInfo := range loadMap {
		if loadInfo == nil {
			clone[accountID] = nil
			continue
		}
		copied := *loadInfo
		clone[accountID] = &copied
	}
	return clone
}

// GetUsersLoadBatch returns load info for multiple users.
func (s *ConcurrencyService) GetUsersLoadBatch(ctx context.Context, users []UserWithConcurrency) (map[int64]*UserLoadInfo, error) {
	if s.cache == nil {
		return map[int64]*UserLoadInfo{}, nil
	}
	return s.cache.GetUsersLoadBatch(ctx, users)
}

// CleanupExpiredAccountSlots removes expired slots for one account (background task).
func (s *ConcurrencyService) CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.CleanupExpiredAccountSlots(ctx, accountID)
}

// StartSlotCleanupWorker starts a background cleanup worker for expired account slots.
func (s *ConcurrencyService) StartSlotCleanupWorker(accountRepo AccountRepository, interval time.Duration) {
	if s == nil || s.cache == nil || accountRepo == nil || interval <= 0 {
		return
	}

	runCleanup := func() {
		listCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		accounts, err := accountRepo.ListSchedulable(listCtx)
		cancel()
		if err != nil {
			logger.LegacyPrintf("service.concurrency", "Warning: list schedulable accounts failed: %v", err)
			return
		}
		for _, account := range accounts {
			accountCtx, accountCancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := s.cache.CleanupExpiredAccountSlots(accountCtx, account.ID)
			accountCancel()
			if err != nil {
				logger.LegacyPrintf("service.concurrency", "Warning: cleanup expired slots failed for account %d: %v", account.ID, err)
			}
		}
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		runCleanup()
		for range ticker.C {
			runCleanup()
		}
	}()
}

// GetAccountConcurrencyBatch gets current concurrency counts for multiple accounts.
// Uses a detached context with timeout to prevent HTTP request cancellation from
// causing the entire batch to fail (which would show all concurrency as 0).
func (s *ConcurrencyService) GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	if len(accountIDs) == 0 {
		return map[int64]int{}, nil
	}
	if s.cache == nil {
		result := make(map[int64]int, len(accountIDs))
		for _, accountID := range accountIDs {
			result[accountID] = 0
		}
		return result, nil
	}

	// Use a detached context so that a cancelled HTTP request doesn't cause
	// the Redis pipeline to fail and return all-zero concurrency counts.
	redisCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	return s.cache.GetAccountConcurrencyBatch(redisCtx, accountIDs)
}
