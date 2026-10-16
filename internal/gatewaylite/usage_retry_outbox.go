package gatewaylite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	usageRetryOutboxPollInterval = time.Second
	usageRetryOutboxBatchSize    = 100
	usageRetryOutboxDBTimeout    = 3 * time.Second
	usageRetryOutboxSendTimeout  = 10 * time.Second
	usageRetryOutboxClaimTTL     = 30 * time.Second
	usageRetryOutboxMaxDelay     = 30 * time.Second
	usageRetryOutboxMaxErrorLen  = 2048
)

type usageRetryOperationKind string

const (
	usageRetryOperationFinalize   usageRetryOperationKind = "finalize"
	usageRetryOperationCorrection usageRetryOperationKind = "correction"
	usageRetryOperationReport     usageRetryOperationKind = "report"
)

// DurableUsageReporter adds a PostgreSQL outbox only to the failure path of
// idempotent_v2 settlement. Successful requests still perform zero database
// work. The legacy switch deliberately keeps its historical behavior.
type DurableUsageReporter struct {
	workers   workerLifetime
	db        *sql.DB
	inner     UsageReportClient
	startOnce sync.Once
	wake      chan struct{}
}

type usageRetryOutboxEnvelope struct {
	Event                      UsageEvent `json:"event"`
	RedisPrefix                string     `json:"redis_prefix"`
	SettlementMicroCents       int64      `json:"settlement_micro_cents"`
	SettlementPending          bool       `json:"settlement_pending"`
	QuotaRequestID             string     `json:"quota_request_id,omitempty"`
	SuppressBaseCharge         bool       `json:"suppress_base_charge,omitempty"`
	CommitAsyncReservation     bool       `json:"commit_async_reservation,omitempty"`
	AsyncReservationMicroCents int64      `json:"async_reservation_micro_cents"`
}

type usageRetryOutboxItem struct {
	requestID     string
	operationKind usageRetryOperationKind
	payload       []byte
	generation    int64
	attempts      int
	claimToken    string
}

func NewDurableUsageReporter(db *sql.DB, inner UsageReportClient) *DurableUsageReporter {
	return &DurableUsageReporter{db: db, inner: inner, wake: make(chan struct{}, 1)}
}

func (r *DurableUsageReporter) SupportsAtomicUsageSettlement() bool {
	atomicReporter, ok := r.inner.(AtomicUsageSettlementReporter)
	return ok && atomicReporter.SupportsAtomicUsageSettlement()
}

func (r *DurableUsageReporter) CommitAndReportUsage(ctx context.Context, quota *RedisQuota, event UsageEvent) (int64, error) {
	if r == nil || r.inner == nil {
		return 0, errors.New("inner usage reporter is nil")
	}
	commitReporter, ok := r.inner.(UsageCommitReporter)
	if !ok {
		return 0, errors.New("inner usage reporter does not support atomic commit")
	}
	actualCents, err := commitReporter.CommitAndReportUsage(ctx, quota, event)
	if err == nil {
		// 正常成功热路径不能访问 PostgreSQL，避免每个请求多一次数据库往返。
		return actualCents, nil
	}
	if r.db == nil || strings.TrimSpace(event.SettlementMode) != string(BillingSettlementModeIdempotentV2) {
		return 0, err
	}
	if !isDurableUsageRetryableFailure(err) {
		return 0, err
	}
	prefix := r.currentRedisPrefix()
	if quota != nil {
		prefix = quota.Prefix()
	}
	// Redis 全断时必须冻结“提交额度 + 用量入队”这个动作本身。仅保存一个
	// 普通 usage event 会让 reservation 永久停在 reserved，后续也无法追平。
	if persistErr := r.persist(usageRetryOperationFinalize, prefix, event, err); persistErr != nil {
		return 0, fmt.Errorf("usage finalize failed: %v; durable outbox failed: %w", err, persistErr)
	}
	r.signalRetry()
	// PostgreSQL 已接管该财务动作。返回成功可阻止 middleware 再生成一个
	// enqueue-only base event；恢复 worker 会用同一 request_id 原样提交。
	return event.ActualCents, nil
}

func (r *DurableUsageReporter) SetPrefix(prefix string) {
	if setter, ok := r.inner.(interface{ SetPrefix(string) }); ok {
		setter.SetPrefix(prefix)
	}
}

func (r *DurableUsageReporter) ReportUsage(ctx context.Context, event UsageEvent) error {
	if r == nil || r.inner == nil {
		return errors.New("inner usage reporter is nil")
	}
	if event.CommitAsyncReservation {
		if !r.SupportsAtomicAsyncReservation() {
			return ErrRequestQuotaUnavailable
		}
		if err := validateAsyncReservationEvent(event); err != nil {
			return err
		}
	}
	err := r.inner.ReportUsage(ctx, event)
	if err == nil {
		return nil
	}
	if r.db == nil || strings.TrimSpace(event.SettlementMode) != string(BillingSettlementModeIdempotentV2) {
		return err
	}
	if !isDurableUsageRetryableFailure(err) {
		return err
	}
	operationKind := usageRetryOperationReport
	if event.SettlementPending {
		operationKind = usageRetryOperationCorrection
	}
	if persistErr := r.persist(operationKind, r.currentRedisPrefix(), event, err); persistErr != nil {
		return fmt.Errorf("usage report failed: %v; durable outbox failed: %w", err, persistErr)
	}
	r.signalRetry()
	return nil
}

func (r *DurableUsageReporter) StartRetryWorker(ctx context.Context) {
	if r == nil || r.db == nil || r.inner == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.startOnce.Do(func() {
		r.workers.Go(ctx, r.runRetryWorker)
	})
}

func (r *DurableUsageReporter) runRetryWorker(ctx context.Context) {
	ticker := time.NewTicker(usageRetryOutboxPollInterval)
	defer ticker.Stop()
	for {
		if err := r.retryDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("gateway-lite durable usage outbox retry failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
	}
}

func (r *DurableUsageReporter) persist(operationKind usageRetryOperationKind, redisPrefix string, event UsageEvent, reportErr error) error {
	if operationKind != usageRetryOperationFinalize && operationKind != usageRetryOperationCorrection && operationKind != usageRetryOperationReport {
		return errors.New("invalid durable usage operation kind")
	}
	envelope := usageRetryOutboxEnvelope{
		Event:                      event,
		RedisPrefix:                NormalizeRedisPrefix(redisPrefix),
		SettlementMicroCents:       event.SettlementMicroCents,
		SettlementPending:          event.SettlementPending,
		QuotaRequestID:             event.QuotaRequestID,
		SuppressBaseCharge:         event.SuppressBaseCharge,
		CommitAsyncReservation:     event.CommitAsyncReservation,
		AsyncReservationMicroCents: event.AsyncReservationMicroCents,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	requestID := strings.TrimSpace(event.RequestID)
	if requestID == "" {
		return errors.New("request_id is required for durable usage outbox")
	}
	lastError := truncateUsageRetryError(reportErr)
	ctx, cancel := context.WithTimeout(context.Background(), usageRetryOutboxDBTimeout)
	defer cancel()
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO gateway_lite_usage_retry_outbox
			(request_id, operation_kind, event_payload, generation, attempts, next_retry_at, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, 1, 0, NOW(), $4, NOW(), NOW())
		ON CONFLICT (request_id, operation_kind) DO NOTHING
	`, requestID, string(operationKind), payload, lastError)
	if err != nil || !event.CommitAsyncReservation {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		return nil
	}
	// 同编号的旧 correction 可能未包含原预留提交意图。只有原封套承接了
	// 完全相同的财务事实才能确认任务，冲突时不得覆盖原行或静默成功。
	return r.verifyAsyncReservationOutbox(ctx, operationKind, envelope)
}

func (r *DurableUsageReporter) retryDue(ctx context.Context) error {
	if r == nil || r.db == nil || r.inner == nil {
		return nil
	}
	// 每次只领取一行。一次领取 100 行再串行发送时，后排任务可能在真正
	// 执行前就超过 claim TTL，被另一节点重复领取并产生财务副作用。
	for processed := 0; processed < usageRetryOutboxBatchSize; processed++ {
		item, ok, err := r.claimNext(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if retryErr := r.retryItem(ctx, item); retryErr != nil && !errors.Is(retryErr, context.Canceled) {
			slog.Warn("gateway-lite durable usage event retry deferred", "request_id", item.requestID, "operation_kind", item.operationKind, "error", retryErr)
		}
	}
	return nil
}

func (r *DurableUsageReporter) claimNext(ctx context.Context) (usageRetryOutboxItem, bool, error) {
	claimToken := uuid.NewString()
	queryCtx, cancel := context.WithTimeout(ctx, usageRetryOutboxDBTimeout)
	defer cancel()
	row := r.db.QueryRowContext(queryCtx, `
		WITH due AS (
			SELECT request_id, operation_kind
			FROM gateway_lite_usage_retry_outbox
			WHERE next_retry_at <= NOW()
			  AND (
				claim_token = '' OR claimed_at IS NULL OR
				claimed_at <= NOW() - ($1 * INTERVAL '1 millisecond')
			  )
			ORDER BY next_retry_at ASC, created_at ASC, request_id ASC, operation_kind ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE gateway_lite_usage_retry_outbox AS outbox
		SET claim_token = $2, claimed_at = NOW(), updated_at = NOW()
		FROM due
		WHERE outbox.request_id = due.request_id
		  AND outbox.operation_kind = due.operation_kind
		RETURNING outbox.request_id, outbox.operation_kind, outbox.event_payload,
			outbox.generation, outbox.attempts
	`, usageRetryOutboxClaimTTL.Milliseconds(), claimToken)
	item := usageRetryOutboxItem{claimToken: claimToken}
	if err := row.Scan(&item.requestID, &item.operationKind, &item.payload, &item.generation, &item.attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return usageRetryOutboxItem{}, false, nil
		}
		return usageRetryOutboxItem{}, false, err
	}
	return item, true, nil
}

func (r *DurableUsageReporter) retryItem(parent context.Context, item usageRetryOutboxItem) error {
	var envelope usageRetryOutboxEnvelope
	if err := json.Unmarshal(item.payload, &envelope); err != nil {
		return r.deferItem(parent, item, fmt.Errorf("decode outbox event: %w", err))
	}
	event := envelope.Event
	event.SettlementMicroCents = envelope.SettlementMicroCents
	event.SettlementPending = envelope.SettlementPending
	event.QuotaRequestID = envelope.QuotaRequestID
	event.SuppressBaseCharge = envelope.SuppressBaseCharge
	event.CommitAsyncReservation = envelope.CommitAsyncReservation
	event.AsyncReservationMicroCents = envelope.AsyncReservationMicroCents

	target, quota, targetErr := r.replayTarget(envelope.RedisPrefix)
	if targetErr != nil {
		return r.deferItem(parent, item, targetErr)
	}
	sendCtx, cancel := context.WithTimeout(parent, usageRetryOutboxSendTimeout)
	var err error
	switch item.operationKind {
	case usageRetryOperationFinalize:
		commitReporter, ok := target.(UsageCommitReporter)
		if !ok {
			err = errors.New("usage retry target does not support atomic commit")
		} else if quota == nil {
			err = errors.New("usage retry target cannot rebuild redis quota")
		} else {
			_, err = commitReporter.CommitAndReportUsage(sendCtx, quota, event)
		}
	case usageRetryOperationCorrection, usageRetryOperationReport:
		err = target.ReportUsage(sendCtx, event)
	default:
		err = fmt.Errorf("unknown durable usage operation kind %q", item.operationKind)
	}
	cancel()
	if err != nil {
		return r.deferItem(parent, item, err)
	}
	dbCtx, dbCancel := context.WithTimeout(parent, usageRetryOutboxDBTimeout)
	defer dbCancel()
	_, err = r.db.ExecContext(dbCtx, `
		DELETE FROM gateway_lite_usage_retry_outbox
		WHERE request_id = $1 AND operation_kind = $2 AND generation = $3 AND claim_token = $4
	`, item.requestID, string(item.operationKind), item.generation, item.claimToken)
	return err
}

func (r *DurableUsageReporter) deferItem(parent context.Context, item usageRetryOutboxItem, retryErr error) error {
	nextAttempts := item.attempts + 1
	delay := usageOutboxRetryDelay(nextAttempts)
	dbCtx, cancel := context.WithTimeout(parent, usageRetryOutboxDBTimeout)
	defer cancel()
	_, err := r.db.ExecContext(dbCtx, `
		UPDATE gateway_lite_usage_retry_outbox
		SET attempts = attempts + 1,
			next_retry_at = NOW() + ($5 * INTERVAL '1 millisecond'),
			last_error = $6,
			claim_token = '',
			claimed_at = NULL,
			updated_at = NOW()
		WHERE request_id = $1 AND operation_kind = $2 AND generation = $3 AND claim_token = $4
	`, item.requestID, string(item.operationKind), item.generation, item.claimToken, delay.Milliseconds(), truncateUsageRetryError(retryErr))
	if err != nil {
		return fmt.Errorf("usage retry failed: %v; update retry state: %w", retryErr, err)
	}
	return retryErr
}

func (r *DurableUsageReporter) currentRedisPrefix() string {
	if r != nil && r.inner != nil {
		if provider, ok := r.inner.(interface{ Prefix() string }); ok {
			return NormalizeRedisPrefix(provider.Prefix())
		}
	}
	return NormalizeRedisPrefix("")
}

func (r *DurableUsageReporter) replayTarget(prefix string) (UsageReportClient, *RedisQuota, error) {
	if r == nil || r.inner == nil {
		return nil, nil, errors.New("inner usage reporter is nil")
	}
	prefix = NormalizeRedisPrefix(prefix)
	if redisReporter, ok := r.inner.(*RedisUsageReporter); ok {
		if redisReporter.client == nil {
			return nil, nil, errors.New("redis usage reporter client is nil")
		}
		// SetPrefix 会改变当前请求使用的 prefix，不能在恢复线程调用；这里只把
		// 冻结 prefix 注册给原 worker，确保配置切换后旧 Stream 仍会被消费。
		redisReporter.prefixMu.Lock()
		if redisReporter.prefixes == nil {
			redisReporter.prefixes = map[string]struct{}{}
		}
		redisReporter.prefixes[prefix] = struct{}{}
		redisReporter.prefixMu.Unlock()
		return redisReporter.WithPrefix(prefix), NewRedisQuota(redisReporter.client, prefix), nil
	}
	// 非生产测试 reporter 没有 Redis client；普通 report 可以直接重放，
	// finalize 则由测试 reporter 自己决定如何解释 nil quota。
	return r.inner, nil, nil
}

func isDurableUsageRetryableFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(strings.TrimSpace(err.Error()))
	// deadline/连接断开属于可恢复故障，必须交给 outbox；结构损坏、权限、
	// 参数和结算冲突则不会靠等待恢复，不能吞掉并无限重试。
	for _, permanent := range []string{
		"WRONGTYPE",
		"SETTLEMENT_CONFLICT",
		"NOAUTH",
		"NOPERM",
		"UNKNOWN COMMAND",
		"WRONG NUMBER OF ARGUMENTS",
		"ACTUAL_CENTS CANNOT BE NEGATIVE",
		"SETTLEMENT_MICRO_CENTS CANNOT BE NEGATIVE",
		"PENDING SETTLEMENT REQUIRES",
		"REQUEST_ID IS REQUIRED",
		"REDIS PREFIXES DIFFER",
	} {
		if strings.Contains(message, permanent) {
			return false
		}
	}
	return true
}

func (r *DurableUsageReporter) signalRetry() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func usageOutboxRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for i := 1; i < attempt && delay < usageRetryOutboxMaxDelay; i++ {
		delay *= 2
	}
	if delay > usageRetryOutboxMaxDelay {
		return usageRetryOutboxMaxDelay
	}
	return delay
}

func truncateUsageRetryError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > usageRetryOutboxMaxErrorLen {
		message = message[:usageRetryOutboxMaxErrorLen]
	}
	return message
}
