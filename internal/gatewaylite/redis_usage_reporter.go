package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultUsageReportTimeout = 10 * time.Second
	defaultUsageQueueWorkers  = 4
	// 主站会在同一事务内逐条保留 usage/ledger 明细；小批次可缩短热点用户、租约行的持锁时间。
	// 4 个 worker 并行后每轮仍可处理 100 条，不需要用大事务换吞吐。
	defaultUsageBatchSize  = 25
	maxUsageBatchSize      = 500
	maxUsageReportAttempts = 10
	maxUsageRetryDelay     = 30 * time.Second
	// 正常 worker 最长可能先等待 10 秒主站上报超时，再等待 30 秒重试退避。
	// stale pending 必须明显超过这段完整处理窗口，避免健康但较慢的 worker
	// 仍在处理消息时被另一个 worker 误认领，造成不必要的重复上报。
	usagePendingRecoveryMinIdle   = defaultUsageReportTimeout + maxUsageRetryDelay + 20*time.Second
	usagePendingRecoveryInterval  = 15 * time.Second
	usagePendingRecoveryScanLimit = 500
	// idempotent_v2 的 Lua 同时冻结金额、修正额度并写入 Stream；同一个
	// request_id 可以安全重放。这里只在 Redis 短暂断连时做极短重试，
	// 正常请求不增加任何等待，也不会退回另一套本地零钱累计。
	usageSettlementRedisAttempts = 3
	usageSettlementRetryDelay    = 25 * time.Millisecond
)

type UsageReportClient interface {
	ReportUsage(ctx context.Context, event UsageEvent) error
}

type UsageBatchReportClient interface {
	ReportUsageBatch(ctx context.Context, events []UsageEvent) error
}

// AtomicUsageSettlementReporter marks a reporter that can freeze fractional
// cents, adjust the local lease and durably enqueue the correction in one
// Redis transaction. Non-Redis reporters are settled by the bounded local
// fallback before ReportUsage is called.
type AtomicUsageSettlementReporter interface {
	SupportsAtomicUsageSettlement() bool
}

// UsageCommitReporter atomically commits a successful local reservation and
// hands its usage event to the durable Redis Stream. Implementations may use
// this fast path only when quota and usage queue share the same Redis prefix.
type UsageCommitReporter interface {
	CommitAndReportUsage(ctx context.Context, quota *RedisQuota, event UsageEvent) (int64, error)
}

type RedisUsageReporter struct {
	workers              workerLifetime
	client               *redis.Client
	commitBatcher        *usageCommitBatcher
	pendingRecovery      *usagePendingRecoveryState
	prefixValue          atomic.Value
	batchSize            atomic.Int64
	prefixMu             sync.RWMutex
	prefixes             map[string]struct{}
	sink                 UsageReportClient
	postUsageLeaseRefill func(*RedisQuota, leaseRefillSignal, string)
}

type usagePendingRecoveryState struct {
	mu     sync.Mutex
	next   map[string]time.Time
	cursor map[string]string
}

type UsageQueueStats struct {
	StreamLength int64
	PendingCount int64
	DeadCount    int64
}

type queuedUsageEvent struct {
	Event      UsageEvent `json:"event"`
	Attempts   int        `json:"attempts"`
	EnqueuedAt int64      `json:"enqueued_at"`
}

func NewRedisUsageReporter(client *redis.Client, prefix string, sink UsageReportClient) *RedisUsageReporter {
	reporter := &RedisUsageReporter{
		client:               client,
		sink:                 sink,
		postUsageLeaseRefill: schedulePostUsageLeaseRefill,
		pendingRecovery: &usagePendingRecoveryState{
			next:   make(map[string]time.Time),
			cursor: make(map[string]string),
		},
	}
	if client != nil {
		reporter.commitBatcher = newUsageCommitBatcher(client)
	}
	reporter.batchSize.Store(defaultUsageBatchSize)
	reporter.SetPrefix(prefix)
	return reporter
}

func (r *RedisUsageReporter) Enabled() bool {
	return r != nil && r.client != nil
}

func (r *RedisUsageReporter) SupportsAtomicUsageSettlement() bool {
	return r.Enabled()
}

func (r *RedisUsageReporter) WithPrefix(prefix string) *RedisUsageReporter {
	if r == nil {
		return nil
	}
	prefix = NormalizeRedisPrefix(prefix)
	if prefix == r.prefix() {
		return r
	}
	reporter := &RedisUsageReporter{
		client:               r.client,
		commitBatcher:        r.commitBatcher,
		pendingRecovery:      r.pendingRecovery,
		sink:                 r.sink,
		postUsageLeaseRefill: r.postUsageLeaseRefill,
	}
	reporter.batchSize.Store(r.currentBatchSize())
	reporter.SetPrefix(prefix)
	return reporter
}

func (r *RedisUsageReporter) ReportUsage(ctx context.Context, event UsageEvent) error {
	if r == nil || r.sink == nil {
		return errors.New("usage reporter sink is nil")
	}
	if event.CommitAsyncReservation {
		return r.reportAsyncReservationUsage(ctx, event)
	}
	if event.FundingAuthorizationID != "" {
		_, err := r.reportFundedUsage(ctx, event)
		return err
	}
	if event.Final && (event.SettlementMode != string(BillingSettlementModeIdempotentV2) || !event.SettlementPending || strings.TrimSpace(event.RequestID) == "" || event.RequestID != event.QuotaRequestID || event.SuppressBaseCharge) {
		return errors.New("final balance usage requires its original per-turn idempotent settlement")
	}
	if event.ActualCents < 0 {
		return errors.New("actual_cents cannot be negative")
	}
	if event.SettlementPending {
		if event.SettlementMode != string(BillingSettlementModeIdempotentV2) {
			return errors.New("pending settlement requires idempotent_v2 mode")
		}
		if strings.TrimSpace(event.RequestID) == "" {
			return errors.New("pending settlement requires request_id")
		}
		if event.SettlementMicroCents < 0 {
			return errors.New("settlement_micro_cents cannot be negative")
		}
	}
	reportStarted := time.Now()
	if !r.Enabled() {
		if event.ReportLatencyMillis <= 0 {
			event.ReportLatencyMillis = time.Since(reportStarted).Milliseconds()
		}
		return r.sink.ReportUsage(ctx, event)
	}
	if event.ReportLatencyMillis <= 0 {
		event.ReportLatencyMillis = time.Since(reportStarted).Milliseconds()
	}
	body, err := json.Marshal(queuedUsageEvent{
		Event:      event,
		Attempts:   0,
		EnqueuedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	quota := NewRedisQuota(r.client, r.prefix())
	quotaRequestID := strings.TrimSpace(event.QuotaRequestID)
	if quotaRequestID == "" || !event.SettlementPending {
		// legacy corrections keep their historical absolute request-id behavior;
		// only idempotent_v2 understands a child settlement linked to a separate
		// connection-level quota request.
		quotaRequestID = event.RequestID
	}
	settlementPending := 0
	if event.SettlementPending {
		settlementPending = 1
	}
	settlementScope := actualCostAccumulatorScopeForUsageEvent(event)
	keys := []string{
		quota.requestKey(event.RequestID),
		quota.requestKey(quotaRequestID),
		r.streamKey(),
		quota.actualCostRemainderKey(settlementScope),
	}
	args := []any{
		event.ActualCents,
		body,
		event.RequestID,
		quotaRequestID,
		settlementPending,
		event.SettlementMicroCents,
		actualCostMicroCentsScale,
		int((30 * 24 * time.Hour).Seconds()),
		int(reservationTTL.Seconds()),
		settlementScope,
		boolToRedisInt(event.SuppressBaseCharge),
		boolToRedisInt(event.Final),
	}
	attempts := 1
	if event.SettlementPending {
		attempts = usageSettlementRedisAttempts
	}
	var values []any
	for attempt := 0; attempt < attempts; attempt++ {
		values, err = adjustAndQueueUsageScript.Run(ctx, r.client, keys, args...).Slice()
		if err == nil || attempt+1 >= attempts || !isRetryableUsageSettlementRedisError(err) {
			break
		}
		delay := usageSettlementRetryDelay * time.Duration(1<<attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err != nil {
		return err
	}
	if len(values) == 0 {
		return errors.New("empty usage correction result")
	}
	refill := parseLeaseRefillSignal(values)
	// Adjustment, durable queue handoff and the low-watermark decision are one
	// Redis script. A process exit can no longer leave corrected quota without
	// the corresponding usage event.
	r.triggerPostUsageLeaseRefill(quota, refill, event.GatewayID)
	return nil
}

func isRetryableUsageSettlementRedisError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := strings.ToUpper(strings.TrimSpace(err.Error()))
	// 数据类型、结算冲突和权限/脚本错误不会通过等待自行恢复；重试它们
	// 只会放大故障日志。连接重置、EOF、超时、LOADING/TRYAGAIN 等短暂错误
	// 则允许用同一个 request_id 重放幂等 Lua。
	for _, permanent := range []string{
		"WRONGTYPE",
		"SETTLEMENT_CONFLICT",
		"FUNDING_CONFLICT",
		"FUNDING_STALE",
		"FUNDING_FINAL",
		"NOAUTH",
		"NOPERM",
		"UNKNOWN COMMAND",
		"WRONG NUMBER OF ARGUMENTS",
	} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	return true
}

func actualCostAccumulatorScopeForUsageEvent(event UsageEvent) string {
	parts := []string{
		strings.TrimSpace(event.Region),
		strings.TrimSpace(event.KeyID),
	}
	if event.UserID > 0 {
		parts = append(parts, strconvFormatInt(event.UserID))
	}
	return strings.Join(parts, ":")
}

// CommitAndReportUsage replaces the former CommitResolved -> AdjustCommitted
// -> XADD sequence with one Redis script. The stream carries the resolved cost
// as a separate field so an earlier asynchronous correction is preserved
// without rebuilding JSON in Lua.
func (r *RedisUsageReporter) CommitAndReportUsage(ctx context.Context, quota *RedisQuota, event UsageEvent) (int64, error) {
	if event.FundingAuthorizationID != "" {
		return r.reportFundedUsage(ctx, event)
	}
	if r == nil || r.sink == nil {
		return 0, errors.New("usage reporter sink is nil")
	}
	if quota == nil || !quota.Enabled() || !r.Enabled() {
		actual := event.ActualCents
		if quota != nil {
			resolved, err := quota.CommitResolved(ctx, UsageCommit{RequestID: event.RequestID, ActualCents: actual})
			if err != nil {
				return 0, err
			}
			actual = resolved
		}
		event.ActualCents = actual
		if event.SuppressBaseCharge {
			event.ActualCents = 0
		}
		return actual, r.ReportUsage(ctx, event)
	}
	if strings.TrimSpace(event.RequestID) == "" {
		return 0, errors.New("request_id is required")
	}
	if event.ActualCents < 0 {
		return 0, errors.New("actual_cents cannot be negative")
	}
	if quota.Prefix() != r.Prefix() {
		return 0, errors.New("quota and usage reporter redis prefixes differ")
	}
	if event.ReportLatencyMillis <= 0 {
		event.ReportLatencyMillis = 0
	}
	body, err := json.Marshal(queuedUsageEvent{
		Event:      event,
		Attempts:   0,
		EnqueuedAt: time.Now().UnixMilli(),
	})
	if err != nil {
		return 0, err
	}
	requestKey := quota.requestKey(event.RequestID)
	streamKey := r.streamKey()
	result := usageCommitBatchResult{}
	if r.commitBatcher != nil {
		result, err = r.commitBatcher.commitWithRefillSignal(ctx, requestKey, streamKey, event.ActualCents, body, event.SuppressBaseCharge)
	} else {
		var values []any
		values, err = commitAndQueueUsageScript.Run(
			ctx,
			r.client,
			[]string{requestKey, streamKey},
			event.ActualCents,
			body,
			boolToRedisInt(event.SuppressBaseCharge),
		).Slice()
		if err == nil {
			if len(values) == 0 {
				err = errors.New("empty usage commit result")
			} else {
				result.actualCents = redisScriptInt64(values[0])
				result.refill = parseLeaseRefillSignal(values)
			}
		}
	}
	if err != nil {
		return 0, err
	}
	// The Lua script commits quota and appends the Stream event atomically, so a
	// returned signal is already safe to turn into an asynchronous refill.
	r.triggerPostUsageLeaseRefill(quota, result.refill, event.GatewayID)
	return result.actualCents, nil
}

func (r *RedisUsageReporter) triggerPostUsageLeaseRefill(quota *RedisQuota, signal leaseRefillSignal, gatewayCode string) {
	if r == nil || r.postUsageLeaseRefill == nil || !signal.Needed {
		return
	}
	r.postUsageLeaseRefill(quota, signal, gatewayCode)
}

func (r *RedisUsageReporter) Start(ctx context.Context) {
	r.StartWorkers(ctx, defaultUsageQueueWorkers)
}

func (r *RedisUsageReporter) StartWorkers(ctx context.Context, workers int) {
	r.StartWorkersWithBatchSize(ctx, workers, defaultUsageBatchSize)
}

func (r *RedisUsageReporter) StartWorkersWithBatchSize(ctx context.Context, workers int, batchSize int) {
	if !r.Enabled() || r.sink == nil {
		return
	}
	if workers <= 0 {
		workers = defaultUsageQueueWorkers
	}
	if batchSize <= 0 {
		batchSize = defaultUsageBatchSize
	}
	if batchSize > maxUsageBatchSize {
		batchSize = maxUsageBatchSize
	}
	r.batchSize.Store(int64(batchSize))
	if err := r.ensureGroup(ctx); err != nil {
		log.Printf("gateway-lite: usage reporter group init failed: %v", err)
		return
	}
	for i := 0; i < workers; i++ {
		r.workers.Go(ctx, func(workerCtx context.Context) { r.runWorker(workerCtx, i) })
	}
}

func (r *RedisUsageReporter) UsageQueueStats(ctx context.Context) (UsageQueueStats, error) {
	if !r.Enabled() {
		return UsageQueueStats{}, nil
	}
	var stats UsageQueueStats
	streamLength, err := r.client.XLen(ctx, r.streamKey()).Result()
	if err != nil {
		return stats, err
	}
	stats.StreamLength = streamLength
	deadCount, err := r.client.XLen(ctx, r.deadKey()).Result()
	if err != nil {
		return stats, err
	}
	stats.DeadCount = deadCount
	pending, err := r.client.XPending(ctx, r.streamKey(), r.groupName()).Result()
	if err != nil {
		if strings.Contains(err.Error(), "NOGROUP") {
			return stats, nil
		}
		return stats, err
	}
	stats.PendingCount = pending.Count
	return stats, nil
}

func (r *RedisUsageReporter) runWorker(ctx context.Context, workerID int) {
	consumer := fmt.Sprintf("worker-%d", workerID)
	for {
		prefixes := r.activePrefixes()
		if len(prefixes) == 0 {
			prefixes = []string{NormalizeRedisPrefix("")}
		}
		block := 2 * time.Second
		if len(prefixes) > 1 {
			block = 500 * time.Millisecond
		}
		handled := false
		for _, prefix := range prefixes {
			// 只让每组的第一个 worker 执行恢复扫描；状态再按 prefix 节流，
			// 因而空闲队列每个前缀每 15 秒最多增加一次 Redis 命令。
			if workerID == 0 && r.recoverStalePending(ctx, workerID, consumer, prefix) {
				handled = true
			}
			if r.readWorkerPrefix(ctx, workerID, consumer, prefix, block) {
				handled = true
			}
			if ctx.Err() != nil {
				return
			}
		}
		if !handled && len(prefixes) == 0 {
			time.Sleep(time.Second)
		}
	}
}

func (r *RedisUsageReporter) recoverStalePending(ctx context.Context, workerID int, consumer string, prefix string) bool {
	if r == nil || r.client == nil || r.pendingRecovery == nil {
		return false
	}
	prefix = NormalizeRedisPrefix(prefix)
	start, due := r.pendingRecovery.claimCursor(prefix, time.Now())
	if !due {
		return false
	}

	// 生产 Redis 仍为 6.0：不能使用 6.2 才提供的 XAUTOCLAIM，也不能使用
	// XPENDING IDLE 过滤。先用 Redis 5+ 的扩展 XPENDING 分页读取，在 Go 侧
	// 筛出超龄 ID；后续 XCLAIM 的 MinIdle 会在 Redis 内再次原子复核，避免
	// 查询和认领之间原 worker 已恢复处理时发生误抢。
	pending, err := r.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: r.streamKeyForPrefix(prefix),
		Group:  r.groupNameForPrefix(prefix),
		Idle:   0,
		Start:  start,
		End:    "+",
		Count:  usagePendingRecoveryScanLimit,
	}).Result()
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if strings.Contains(err.Error(), "NOGROUP") {
			if groupErr := r.ensureGroupForPrefix(ctx, prefix); groupErr != nil {
				log.Printf("gateway-lite: usage reporter worker=%d stale pending group init failed prefix=%s: %v", workerID, prefix, groupErr)
			}
			return false
		}
		log.Printf("gateway-lite: usage reporter worker=%d stale pending scan failed prefix=%s: %v", workerID, prefix, err)
		return false
	}
	claimIDs := make([]string, 0, min(len(pending), int(r.currentBatchSize())))
	inspected := 0
	for _, item := range pending {
		inspected++
		if item.Idle < usagePendingRecoveryMinIdle {
			continue
		}
		claimIDs = append(claimIDs, item.ID)
		if int64(len(claimIDs)) >= r.currentBatchSize() {
			break
		}
	}
	r.pendingRecovery.storeCursor(prefix, nextPendingScanCursor(pending, inspected))
	if len(claimIDs) == 0 {
		return false
	}
	messages, err := r.client.XClaim(ctx, &redis.XClaimArgs{
		Stream:   r.streamKeyForPrefix(prefix),
		Group:    r.groupNameForPrefix(prefix),
		Consumer: consumer,
		MinIdle:  usagePendingRecoveryMinIdle,
		Messages: claimIDs,
	}).Result()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("gateway-lite: usage reporter worker=%d stale pending claim failed prefix=%s: %v", workerID, prefix, err)
		}
		return false
	}
	if len(messages) == 0 {
		return false
	}
	log.Printf("gateway-lite: usage reporter worker=%d recovered stale pending prefix=%s events=%d", workerID, prefix, len(messages))
	// 复用正常消费路径，成功后仍执行 XACK + XDEL；主站继续依赖原始
	// request_id 做幂等，覆盖 Redis 返回成功但进程未收到 ACK 的重放边界。
	r.handleMessages(ctx, messages, workerID, prefix)
	return true
}

func nextPendingScanCursor(pending []redis.XPendingExt, inspected int) string {
	if inspected <= 0 || inspected > len(pending) {
		return "-"
	}
	// 当前页已完整扫描且不足上限，说明已经到 PEL 尾部；下一轮从头复查。
	// 如果因达到 claim batch 提前结束，则从最后检查过的下一条继续，避免
	// 大量 stale pending 每轮都只盯着同一页开头。
	if inspected == len(pending) && len(pending) < usagePendingRecoveryScanLimit {
		return "-"
	}
	last := pending[inspected-1].ID
	parts := strings.SplitN(last, "-", 2)
	if len(parts) != 2 {
		return "-"
	}
	milliseconds, errMS := strconv.ParseUint(parts[0], 10, 64)
	sequence, errSequence := strconv.ParseUint(parts[1], 10, 64)
	if errMS != nil || errSequence != nil {
		return "-"
	}
	if sequence < ^uint64(0) {
		return fmt.Sprintf("%d-%d", milliseconds, sequence+1)
	}
	if milliseconds < ^uint64(0) {
		return fmt.Sprintf("%d-0", milliseconds+1)
	}
	return "-"
}

func (s *usagePendingRecoveryState) claimCursor(prefix string, now time.Time) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if next := s.next[prefix]; !next.IsZero() && now.Before(next) {
		return "", false
	}
	s.next[prefix] = now.Add(usagePendingRecoveryInterval)
	start := s.cursor[prefix]
	if start == "" {
		start = "-"
	}
	return start, true
}

func (s *usagePendingRecoveryState) storeCursor(prefix string, cursor string) {
	if s == nil {
		return
	}
	if cursor == "" {
		cursor = "-"
	}
	s.mu.Lock()
	s.cursor[prefix] = cursor
	s.mu.Unlock()
}

func (r *RedisUsageReporter) readWorkerPrefix(ctx context.Context, workerID int, consumer string, prefix string, block time.Duration) bool {
	for {
		streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    r.groupNameForPrefix(prefix),
			Consumer: consumer,
			Streams:  []string{r.streamKeyForPrefix(prefix), ">"},
			Count:    r.currentBatchSize(),
			Block:    block,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				return false
			}
			if ctx.Err() != nil {
				return false
			}
			if strings.Contains(err.Error(), "NOGROUP") {
				if groupErr := r.ensureGroupForPrefix(ctx, prefix); groupErr != nil {
					log.Printf("gateway-lite: usage reporter worker=%d group init failed: %v", workerID, groupErr)
					time.Sleep(time.Second)
				}
				return false
			}
			log.Printf("gateway-lite: usage reporter worker=%d queue read failed: %v", workerID, err)
			time.Sleep(time.Second)
			return false
		}
		r.handleStreams(ctx, streams, workerID, prefix)
		return true
	}
}

func (r *RedisUsageReporter) handleStreams(ctx context.Context, streams []redis.XStream, workerID int, prefix string) {
	for _, stream := range streams {
		if len(stream.Messages) == 0 {
			continue
		}
		r.handleMessages(ctx, stream.Messages, workerID, prefix)
	}
}

func (r *RedisUsageReporter) handleMessages(ctx context.Context, messages []redis.XMessage, workerID int, prefix string) {
	items := make([]queuedUsageEvent, 0, len(messages))
	itemIDs := make([]string, 0, len(messages))
	ackIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		raw, ok := message.Values["event"].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			log.Printf("gateway-lite: usage reporter worker=%d invalid stream message id=%s", workerID, message.ID)
			r.deadLetter(ctx, queuedUsageEvent{EnqueuedAt: time.Now().UnixMilli()}, "invalid_stream_message", prefix)
			ackIDs = append(ackIDs, message.ID)
			continue
		}
		var item queuedUsageEvent
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			log.Printf("gateway-lite: usage reporter worker=%d invalid event dropped id=%s: %v", workerID, message.ID, err)
			r.deadLetterRaw(ctx, raw, "invalid_json", prefix)
			ackIDs = append(ackIDs, message.ID)
			continue
		}
		if rawActual, exists := message.Values["resolved_actual_cents"]; exists {
			if actual, parseErr := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(rawActual)), 10, 64); parseErr == nil {
				item.Event.ActualCents = actual
			}
		}
		items = append(items, item)
		itemIDs = append(itemIDs, message.ID)
	}
	if len(items) == 0 {
		r.ack(ctx, ackIDs, prefix)
		return
	}

	reportCtx, cancel := context.WithTimeout(ctx, defaultUsageReportTimeout)
	err := r.reportBatch(reportCtx, items)
	cancel()
	if err == nil {
		ackIDs = append(ackIDs, itemIDs...)
		r.ack(ctx, ackIDs, prefix)
		return
	}

	log.Printf("gateway-lite: usage reporter worker=%d batch report failed events=%d: %v", workerID, len(items), err)
	if err := r.retryBatchLater(ctx, items, prefix); err == nil {
		ackIDs = append(ackIDs, itemIDs...)
	}
	r.ack(ctx, ackIDs, prefix)
}

var adjustAndQueueUsageScript = redis.NewScript(`
local settlement_req_key = KEYS[1]
local quota_req_key = KEYS[2]
local stream_key = KEYS[3]
local remainder_key = KEYS[4]
local actual = tonumber(ARGV[1]) or 0
local body = ARGV[2]
local settlement_request_id = ARGV[3] or ""
local quota_request_id = ARGV[4] or settlement_request_id
local settlement_pending = tonumber(ARGV[5]) or 0
local settlement_micro = tonumber(ARGV[6]) or 0
local settlement_scale = tonumber(ARGV[7]) or 1000000
local remainder_ttl = tonumber(ARGV[8]) or 2592000
local sparse_request_ttl = tonumber(ARGV[9]) or 86400
local settlement_scope = ARGV[10] or "global"
local skip_quota_adjustment = tonumber(ARGV[11]) or 0
local finalize_turn = tonumber(ARGV[12]) or 0
local settled_lease_key = ""
local settlement_already_enqueued = false
local settlement_quota_applied = false
local preflight_quota_status = ""
local preflight_lease_key = ""

local function redis_type(key)
  local reply = redis.call("TYPE", key)
  if type(reply) == "table" then
    return reply["ok"]
  end
  return reply
end

-- Redis scripts are atomic but do not roll back writes after a runtime error.
-- Reject a wrong-type stream before touching request or lease counters so a
-- predictable XADD failure cannot strand corrected quota without its event.
local stream_type = redis_type(stream_key)
if stream_type ~= "none" and stream_type ~= "stream" then
  return redis.error_reply("WRONGTYPE usage stream key is not a stream")
end

if settlement_pending == 1 then
  local settlement_req_type = redis_type(settlement_req_key)
  if settlement_req_type ~= "none" and settlement_req_type ~= "hash" then
    return redis.error_reply("WRONGTYPE settlement request key is not a hash")
  end
  local quota_req_type = redis_type(quota_req_key)
  if quota_req_type ~= "none" and quota_req_type ~= "hash" then
    return redis.error_reply("WRONGTYPE quota request key is not a hash")
  end

  -- Redis Lua 遇到运行时错误不会回滚此前写入。先解析并检查可能要修改的
  -- lease（额度租约）键，避免先推进零钱罐、再因损坏的 lease 类型失败。
  if quota_req_type == "hash" then
    preflight_quota_status = redis.call("HGET", quota_req_key, "status") or ""
    if preflight_quota_status == "reserved" or preflight_quota_status == "committed" then
      local user_id = redis.call("HGET", quota_req_key, "user_id") or ""
      local region = redis.call("HGET", quota_req_key, "region") or ""
      preflight_lease_key = redis.call("HGET", quota_req_key, "lease_key") or ""
      if preflight_lease_key == "" then
        preflight_lease_key = string.gsub(quota_req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
      end
      local lease_type = redis_type(preflight_lease_key)
      if lease_type ~= "none" and lease_type ~= "hash" then
        return redis.error_reply("WRONGTYPE quota lease key is not a hash")
      end
    end
  end

  if finalize_turn == 1 then
    if settlement_req_key ~= quota_req_key or skip_quota_adjustment ~= 0 then
      return redis.error_reply("SETTLEMENT_CONFLICT invalid per-turn financial identity")
    end
    if preflight_quota_status ~= "reserved" and preflight_quota_status ~= "committed" then
      return redis.error_reply("SETTLEMENT_UNAVAILABLE original turn reservation is missing")
    end
    if redis.call("HGET", quota_req_key, "funding_authorization_id") then
      return redis.error_reply("SETTLEMENT_CONFLICT funded turn cannot commit a balance lease")
    end
    local request_lease_id = redis.call("HGET", quota_req_key, "lease_id") or ""
    if request_lease_id == "" or redis.call("HGET", preflight_lease_key, "lease_id") ~= request_lease_id then
      return redis.error_reply("SETTLEMENT_UNAVAILABLE original turn lease is missing")
    end
  end

  local settled = redis.call("HGET", settlement_req_key, "settlement_actual_cents")
  if settled then
    local settled_micro = tonumber(redis.call("HGET", settlement_req_key, "settlement_micro_cents") or "0")
    local settled_scope = redis.call("HGET", settlement_req_key, "settlement_scope") or settlement_scope
    -- A zero-cost replay is allowed to recover the first result. Two different
    -- non-zero facts for one request are rejected instead of silently changing
    -- a financial decision that may already be queued.
    if settlement_micro > 0 and (settled_micro ~= settlement_micro or settled_scope ~= settlement_scope) then
      return redis.error_reply("SETTLEMENT_CONFLICT request already has different actual cost")
    end
    actual = tonumber(settled) or 0
    settlement_already_enqueued = redis.call("HGET", settlement_req_key, "settlement_usage_enqueued") == "1"
    settlement_quota_applied = redis.call("HGET", settlement_req_key, "settlement_quota_applied") == "1"
  else
    local remainder_type = redis_type(remainder_key)
    if remainder_type ~= "none" and remainder_type ~= "string" then
      return redis.error_reply("WRONGTYPE settlement remainder key is not a string")
    end
    local current = tonumber(redis.call("GET", remainder_key) or "0")
    local total = current + settlement_micro
    actual = math.floor(total / settlement_scale)
    local remainder = total - (actual * settlement_scale)
    redis.call("SET", remainder_key, remainder, "EX", remainder_ttl)
    local request_exists = redis.call("EXISTS", settlement_req_key)
    redis.call("HSET", settlement_req_key,
      "settlement_micro_cents", settlement_micro,
      "settlement_actual_cents", actual,
      "settlement_scope", settlement_scope,
      "settlement_mode", "idempotent_v2"
    )
    if request_exists == 0 then
      redis.call("EXPIRE", settlement_req_key, sparse_request_ttl)
    end
  end
end

if settlement_pending == 0 and redis.call("EXISTS", settlement_req_key) == 1 then
  -- 普通 report 也可能在 Redis 已成功、PostgreSQL outbox 删除失败后重放。
  -- 额度调整本身是幂等的，但没有 marker 时仍会重复 XADD 和请求主站。
  local report_req_type = redis_type(settlement_req_key)
  if report_req_type ~= "hash" then
    return redis.error_reply("WRONGTYPE usage report request key is not a hash")
  end
  settlement_already_enqueued = redis.call("HGET", settlement_req_key, "usage_report_enqueued") == "1"
end

local function result_with_refill()
  if settled_lease_key == "" or redis.call("EXISTS", settled_lease_key) == 0 then
    return {1, 0}
  end
  local values = redis.call("HMGET", settled_lease_key,
    "lease_id", "user_id", "region", "allocated_cents", "reserved_cents",
    "spent_cents", "version", "expires_at", "target_cents", "refill_at_cents")
  local refill_at = tonumber(values[10] or "0")
  if refill_at <= 0 then
    return {1, 0}
  end
  local allocated = tonumber(values[4] or "0")
  local reserved = tonumber(values[5] or "0")
  local spent = tonumber(values[6] or "0")
  if reserved < 0 or spent < 0 or allocated - reserved - spent > refill_at then
    return {1, 0}
  end
  return {
    1, 1,
    values[1] or "", values[2] or "0", values[3] or "",
    allocated, reserved, spent,
    tonumber(values[7] or "0"), tonumber(values[8] or "0"),
    tonumber(values[9] or "0"), refill_at
  }
end

-- A WebSocket connection base event is deliberately reported as zero because
-- its child resp_* events carry the user charge. In the atomic-finalize
-- fallback, CommitResolved has already committed the child aggregate; this
-- enqueue-only replay must not reinterpret the zero report as a quota refund.
if skip_quota_adjustment == 0 and (settlement_pending == 0 or not settlement_quota_applied) and quota_request_id ~= "" and redis.call("EXISTS", quota_req_key) == 1 then
  local status = preflight_quota_status
  if status == "" then
    status = redis.call("HGET", quota_req_key, "status")
  end
  if status == "reserved" or status == "committed" then
  local user_id = redis.call("HGET", quota_req_key, "user_id")
  local region = redis.call("HGET", quota_req_key, "region")
  local lease_key = preflight_lease_key
  if lease_key == "" then
    lease_key = redis.call("HGET", quota_req_key, "lease_key")
  end
  if not lease_key or lease_key == "" then
    lease_key = string.gsub(quota_req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
  end
  local request_lease_id = redis.call("HGET", quota_req_key, "lease_id") or ""
  local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
  local lease_matches = request_lease_id ~= "" and request_lease_id == current_lease_id
  local child_settlement = settlement_req_key ~= quota_req_key

  if status == "reserved" then
    local target = actual
    if child_settlement then
      target = tonumber(redis.call("HGET", quota_req_key, "child_actual_cents") or "0") + actual
    end
    local current = tonumber(redis.call("HGET", quota_req_key, "estimated_cents") or "0")
    local delta = target - current
    if finalize_turn == 1 then
      -- No connection-level HTTP finalizer owns this turn. Freeze fractional
      -- cents, turn its original hold into spent quota, and queue once here.
      local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
      local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
      redis.call("HSET", lease_key,
        "reserved_cents", math.max(current_reserved - math.max(current, 0), 0),
        "spent_cents", current_spent + math.max(target, 0))
      redis.call("HSET", quota_req_key, "status", "committed", "actual_cents", target)
    elseif delta ~= 0 and lease_matches then
      local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
      local next_reserved = current_reserved + delta
      if next_reserved < 0 then
        next_reserved = 0
      end
      redis.call("HSET", lease_key, "reserved_cents", next_reserved)
    end
    redis.call("HSET", quota_req_key,
      "estimated_cents", target,
      "corrected_actual_cents", target
    )
    if child_settlement then
      redis.call("HSET", quota_req_key, "child_actual_cents", target, "has_child_settlements", 1)
    end
    if lease_matches then
      settled_lease_key = lease_key
    end
  elseif status == "committed" then
    local target = actual
    if child_settlement then
      target = tonumber(redis.call("HGET", quota_req_key, "child_actual_cents") or "0") + actual
    end
    local current = tonumber(redis.call("HGET", quota_req_key, "actual_cents") or "0")
    local delta = target - current
    if delta ~= 0 and lease_matches then
      local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
      local next_spent = current_spent + delta
      if next_spent < 0 then
        next_spent = 0
      end
      redis.call("HSET", lease_key, "spent_cents", next_spent)
    end
    if delta ~= 0 then
      redis.call("HSET", quota_req_key, "actual_cents", target)
    end
    if child_settlement then
      redis.call("HSET", quota_req_key, "child_actual_cents", target, "has_child_settlements", 1)
    end
    if lease_matches then
      settled_lease_key = lease_key
    end
  end
  if settlement_pending == 1 then
    redis.call("HSET", settlement_req_key, "settlement_quota_applied", 1)
    settlement_quota_applied = true
  end
  end
end

if not settlement_already_enqueued then
  redis.call("XADD", stream_key, "*", "event", body, "resolved_actual_cents", actual)
  if settlement_pending == 1 then
    redis.call("HSET", settlement_req_key, "settlement_usage_enqueued", 1)
  else
    -- 预调度失败可能没有 reservation hash。创建带 TTL 的稀疏 marker，
    -- 既保证 crash-safe 重放只入队一次，也不会永久占用 Redis 内存。
    local report_request_exists = redis.call("EXISTS", settlement_req_key)
    redis.call("HSET", settlement_req_key, "usage_report_enqueued", 1)
    if report_request_exists == 0 then
      redis.call("EXPIRE", settlement_req_key, sparse_request_ttl)
    end
  end
end
-- Idempotent retries still surface an already-low current lease after the
-- event is durably queued; the distributed refill lock coalesces duplicates.
return result_with_refill()
`)

var commitAndQueueUsageScript = redis.NewScript(`
local req_key = KEYS[1]
local stream_key = KEYS[2]
local actual = tonumber(ARGV[1]) or 0
local body = ARGV[2]
local suppress_report_actual = tonumber(ARGV[3]) or 0
local settled_lease_key = ""
local request_exists = redis.call("EXISTS", req_key) == 1
local usage_already_enqueued = false

local stream_type_reply = redis.call("TYPE", stream_key)
local stream_type = stream_type_reply
if type(stream_type_reply) == "table" then
  stream_type = stream_type_reply["ok"]
end
if stream_type ~= "none" and stream_type ~= "stream" then
  return redis.error_reply("WRONGTYPE usage stream key is not a stream")
end

local function result_with_refill()
  if settled_lease_key == "" or redis.call("EXISTS", settled_lease_key) == 0 then
    return {actual, 0}
  end
  local values = redis.call("HMGET", settled_lease_key,
    "lease_id", "user_id", "region", "allocated_cents", "reserved_cents",
    "spent_cents", "version", "expires_at", "target_cents", "refill_at_cents")
  local refill_at = tonumber(values[10] or "0")
  if refill_at <= 0 then
    return {actual, 0}
  end
  local allocated = tonumber(values[4] or "0")
  local reserved = tonumber(values[5] or "0")
  local spent = tonumber(values[6] or "0")
  if reserved < 0 or spent < 0 then
    return {actual, 0}
  end
  if allocated - reserved - spent > refill_at then
    return {actual, 0}
  end
  return {
    actual, 1,
    values[1] or "", values[2] or "0", values[3] or "",
    allocated, reserved, spent,
    tonumber(values[7] or "0"), tonumber(values[8] or "0"),
    tonumber(values[9] or "0"), refill_at
  }
end

if request_exists then
  usage_already_enqueued = redis.call("HGET", req_key, "usage_commit_enqueued") == "1"
  local status = redis.call("HGET", req_key, "status")
  if status == "committed" then
    actual = tonumber(redis.call("HGET", req_key, "actual_cents") or actual)
    local user_id = redis.call("HGET", req_key, "user_id")
    local region = redis.call("HGET", req_key, "region")
    local lease_key = redis.call("HGET", req_key, "lease_key")
    if not lease_key or lease_key == "" then
      lease_key = string.gsub(req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
    end
    local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
    local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
    if request_lease_id ~= "" and request_lease_id == current_lease_id then
      settled_lease_key = lease_key
    end
  elseif status == "reserved" then
    local user_id = redis.call("HGET", req_key, "user_id")
    local region = redis.call("HGET", req_key, "region")
    local estimated = tonumber(redis.call("HGET", req_key, "estimated_cents") or "0")
    local corrected = redis.call("HGET", req_key, "corrected_actual_cents")
    if corrected and corrected ~= "" then
      actual = tonumber(corrected)
    end
    local lease_key = redis.call("HGET", req_key, "lease_key")
    if not lease_key or lease_key == "" then
      lease_key = string.gsub(req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
    end
    local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
    local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
    if request_lease_id ~= "" and request_lease_id == current_lease_id then
      settled_lease_key = lease_key
      local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
      local next_reserved = current_reserved - math.max(estimated, 0)
      if next_reserved < 0 then
        next_reserved = 0
      end
      local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
      local next_spent = current_spent + math.max(actual, 0)
      if next_spent < 0 then
        next_spent = 0
      end
      redis.call("HSET", lease_key, "reserved_cents", next_reserved, "spent_cents", next_spent)
    end
    redis.call("HSET", req_key, "status", "committed", "actual_cents", actual)
  end
end
local reported_actual = actual
if suppress_report_actual == 1 then
  reported_actual = 0
end
if not usage_already_enqueued then
  redis.call("XADD", stream_key, "*", "event", body, "resolved_actual_cents", reported_actual)
  -- 和 XADD 放在同一 Lua 原子边界内。Redis 已成功但 outbox 删除失败时，
  -- finalize 重放只恢复 currently-low lease 信号，不会再产生第二条基础账单。
  if request_exists then
    redis.call("HSET", req_key, "usage_commit_enqueued", 1)
  end
end
-- If Redis committed and queued this request before the process exited, an
-- idempotent retry must still report a currently-low lease. The distributed
-- refill lock prevents duplicate control-plane calls.
return result_with_refill()
`)

func (r *RedisUsageReporter) reportBatch(ctx context.Context, items []queuedUsageEvent) error {
	events := make([]UsageEvent, 0, len(items))
	for _, item := range items {
		events = append(events, item.Event)
	}
	if batchSink, ok := r.sink.(UsageBatchReportClient); ok {
		return batchSink.ReportUsageBatch(ctx, events)
	}
	for _, event := range events {
		if err := r.sink.ReportUsage(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (r *RedisUsageReporter) retryBatchLater(ctx context.Context, items []queuedUsageEvent, prefix string) error {
	if len(items) == 0 {
		return nil
	}
	maxAttempts := 0
	for i := range items {
		items[i].Attempts++
		if items[i].Attempts > maxAttempts {
			maxAttempts = items[i].Attempts
		}
	}
	// 同一失败批次只等待一次，避免 200 条事件串行 sleep 数百秒。
	delay := usageRetryDelay(maxAttempts)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	pipe := r.client.Pipeline()
	for _, item := range items {
		body, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if item.Attempts >= maxUsageReportAttempts {
			pipe.XAdd(ctx, &redis.XAddArgs{
				Stream: r.deadKeyForPrefix(prefix),
				Values: map[string]any{
					"event":   string(body),
					"reason":  "retry_exhausted",
					"dead_at": time.Now().UnixMilli(),
				},
			})
			continue
		}
		pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: r.streamKeyForPrefix(prefix),
			Values: map[string]any{"event": body},
		})
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (r *RedisUsageReporter) ensureGroup(ctx context.Context) error {
	return r.ensureGroupForPrefix(ctx, r.prefix())
}

func (r *RedisUsageReporter) ensureGroupForPrefix(ctx context.Context, prefix string) error {
	err := r.client.XGroupCreateMkStream(ctx, r.streamKeyForPrefix(prefix), r.groupNameForPrefix(prefix), "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (r *RedisUsageReporter) ack(ctx context.Context, ids []string, prefix string) {
	if len(ids) == 0 {
		return
	}
	streamKey := r.streamKeyForPrefix(prefix)
	// 两条维护命令按顺序在同一 pipeline（流水线）发送，减少网络往返。
	// 即使网络响应丢失，主站 request_id 幂等也能承受消息重复投递。
	// 不能再按固定长度 XTRIM：生产速度短暂超过消费速度时，固定裁剪会删除尚未
	// 进入 consumer group 的旧计费消息。已确认的消息逐条 XDEL 后，队列长度自然回落。
	pipe := r.client.Pipeline()
	pipe.XAck(ctx, streamKey, r.groupNameForPrefix(prefix), ids...)
	pipe.XDel(ctx, streamKey, ids...)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Printf("gateway-lite: usage reporter ack/cleanup pipeline failed ids=%d: %v", len(ids), err)
	}
}

func (r *RedisUsageReporter) currentBatchSize() int64 {
	if r == nil {
		return defaultUsageBatchSize
	}
	size := r.batchSize.Load()
	if size <= 0 {
		return defaultUsageBatchSize
	}
	if size > maxUsageBatchSize {
		return maxUsageBatchSize
	}
	return size
}

func (r *RedisUsageReporter) deadLetter(ctx context.Context, item queuedUsageEvent, reason string, prefix string) error {
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return r.deadLetterRaw(ctx, string(body), reason, prefix)
}

func (r *RedisUsageReporter) deadLetterRaw(ctx context.Context, raw string, reason string, prefix string) error {
	return r.client.XAdd(ctx, &redis.XAddArgs{
		Stream: r.deadKeyForPrefix(prefix),
		Values: map[string]any{
			"event":   raw,
			"reason":  reason,
			"dead_at": time.Now().UnixMilli(),
		},
	}).Err()
}

func usageRetryDelay(attempts int) time.Duration {
	if attempts <= 0 {
		return time.Second
	}
	delay := time.Duration(attempts) * time.Second
	if delay > maxUsageRetryDelay {
		return maxUsageRetryDelay
	}
	return delay
}

func (r *RedisUsageReporter) streamKey() string {
	return r.streamKeyForPrefix(r.prefix())
}

func (r *RedisUsageReporter) deadKey() string {
	return r.deadKeyForPrefix(r.prefix())
}

func (r *RedisUsageReporter) groupName() string {
	return r.groupNameForPrefix(r.prefix())
}

func (r *RedisUsageReporter) streamKeyForPrefix(prefix string) string {
	return fmt.Sprintf("%s:usage:stream", NormalizeRedisPrefix(prefix))
}

func (r *RedisUsageReporter) deadKeyForPrefix(prefix string) string {
	return fmt.Sprintf("%s:usage:dead", NormalizeRedisPrefix(prefix))
}

func (r *RedisUsageReporter) groupNameForPrefix(prefix string) string {
	return fmt.Sprintf("%s-usage-reporters", NormalizeRedisPrefix(prefix))
}

func (r *RedisUsageReporter) SetPrefix(prefix string) {
	if r != nil {
		prefix = NormalizeRedisPrefix(prefix)
		r.prefixValue.Store(prefix)
		r.prefixMu.Lock()
		if r.prefixes == nil {
			r.prefixes = map[string]struct{}{}
		}
		r.prefixes[prefix] = struct{}{}
		r.prefixMu.Unlock()
	}
}

func (r *RedisUsageReporter) prefix() string {
	if r == nil {
		return NormalizeRedisPrefix("")
	}
	if value := r.prefixValue.Load(); value != nil {
		if prefix, ok := value.(string); ok && prefix != "" {
			return prefix
		}
	}
	return NormalizeRedisPrefix("")
}

func (r *RedisUsageReporter) Prefix() string {
	return r.prefix()
}

func (r *RedisUsageReporter) activePrefixes() []string {
	if r == nil {
		return nil
	}
	current := r.prefix()
	r.prefixMu.RLock()
	out := make([]string, 0, len(r.prefixes)+1)
	seen := map[string]struct{}{}
	for prefix := range r.prefixes {
		prefix = NormalizeRedisPrefix(prefix)
		out = append(out, prefix)
		seen[prefix] = struct{}{}
	}
	r.prefixMu.RUnlock()
	if _, ok := seen[current]; !ok {
		out = append(out, current)
	}
	return out
}
