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
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	contentModerationPenaltyPollInterval = time.Second
	contentModerationPenaltyBatchSize    = 100
	contentModerationPenaltyDBTimeout    = 3 * time.Second
	contentModerationPenaltySendTimeout  = 10 * time.Second
	contentModerationPenaltyClaimTTL     = 30 * time.Second
	contentModerationPenaltyMaxDelay     = 5 * time.Minute
	contentModerationPenaltyMaxErrorLen  = 2048
)

type contentModerationPenaltyOutboxItem struct {
	requestID  string
	payload    []byte
	attempts   int
	claimToken string
}

// DurableContentModerationPenaltyReporter keeps the successful path free of
// database writes. A transient control-plane failure is acknowledged only
// after the exact idempotent request has entered PostgreSQL.
type DurableContentModerationPenaltyReporter struct {
	db        *sql.DB
	clientRef *ControlPlaneClientRef
	startOnce sync.Once
	workerMu  sync.Mutex
	workerWG  sync.WaitGroup
	stop      context.CancelFunc
	stopped   bool
	wake      chan struct{}
}

var defaultContentModerationPenaltyReporter atomic.Pointer[DurableContentModerationPenaltyReporter]

var errContentModerationPenaltyPayloadConflict = errors.New("content moderation penalty request_id payload conflict")

func NewDurableContentModerationPenaltyReporter(db *sql.DB, clientRef *ControlPlaneClientRef) *DurableContentModerationPenaltyReporter {
	return &DurableContentModerationPenaltyReporter{db: db, clientRef: clientRef, wake: make(chan struct{}, 1)}
}

func SetDefaultContentModerationPenaltyReporter(reporter *DurableContentModerationPenaltyReporter) {
	previous := defaultContentModerationPenaltyReporter.Swap(reporter)
	if previous != nil && previous != reporter {
		previous.Stop()
	}
}

func DefaultContentModerationPenaltyReporter() *DurableContentModerationPenaltyReporter {
	return defaultContentModerationPenaltyReporter.Load()
}

func (r *DurableContentModerationPenaltyReporter) Report(ctx context.Context, req ContentModerationPenaltyRequest) error {
	if strings.TrimSpace(req.RequestID) == "" {
		return errors.New("content moderation penalty request_id is required")
	}
	var reportErr error
	if r != nil && r.clientRef != nil && r.clientRef.Configured() {
		_, reportErr = r.clientRef.ReportContentModerationPenalty(ctx, req)
		if reportErr == nil {
			return nil
		}
	} else {
		reportErr = errors.New("control plane client is not configured")
	}
	if r == nil || r.db == nil {
		return reportErr
	}
	retryable := isContentModerationPenaltyRetryableFailure(reportErr)
	if err := r.persist(req, reportErr, retryable); err != nil {
		return fmt.Errorf("content moderation penalty report failed: %v; durable outbox failed: %w", reportErr, err)
	}
	if !retryable {
		// 永久业务拒绝必须显式暴露给调用方，同时保留 dead（死信）记录供排查；
		// 返回成功会让运营误以为处罚已经在主站生效。
		return reportErr
	}
	r.signalRetry()
	return nil
}

func (r *DurableContentModerationPenaltyReporter) StartRetryWorker(ctx context.Context) {
	if r == nil || r.db == nil || r.clientRef == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.startOnce.Do(func() {
		r.workerMu.Lock()
		defer r.workerMu.Unlock()
		if r.stopped {
			return
		}
		workerCtx, cancel := context.WithCancel(ctx)
		r.stop = cancel
		r.workerWG.Add(1)
		go func() {
			defer r.workerWG.Done()
			r.runRetryWorker(workerCtx)
		}()
	})
}

// Stop 将 retry worker（重试线程）纳入应用关停流程，确保 PostgreSQL
// 连接关闭前不再领取或更新处罚事件。
func (r *DurableContentModerationPenaltyReporter) Stop() {
	if r == nil {
		return
	}
	r.workerMu.Lock()
	r.stopped = true
	cancel := r.stop
	r.workerMu.Unlock()
	if cancel != nil {
		cancel()
	}
	r.workerWG.Wait()
}

func (r *DurableContentModerationPenaltyReporter) runRetryWorker(ctx context.Context) {
	ticker := time.NewTicker(contentModerationPenaltyPollInterval)
	defer ticker.Stop()
	for {
		if err := r.retryDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("gateway-lite content moderation penalty retry failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
	}
}

func (r *DurableContentModerationPenaltyReporter) persist(req ContentModerationPenaltyRequest, reportErr error, retryable bool) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), contentModerationPenaltyDBTimeout)
	defer cancel()
	status := "dead"
	var deadAt any = time.Now()
	if retryable {
		status = "pending"
		deadAt = nil
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO gateway_lite_content_moderation_penalty_outbox
			(request_id, event_payload, status, attempts, next_retry_at, last_error, dead_at, created_at, updated_at)
		VALUES ($1, $2, $3, 0, NOW(), $4, $5, NOW(), NOW())
		ON CONFLICT (request_id) DO UPDATE
		SET request_id = EXCLUDED.request_id
		WHERE gateway_lite_content_moderation_penalty_outbox.event_payload - 'created_at_millis'
			= EXCLUDED.event_payload - 'created_at_millis'
	`, strings.TrimSpace(req.RequestID), payload, status, truncateContentModerationPenaltyError(reportErr), deadAt)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read content moderation penalty outbox result: %w", err)
	}
	if rowsAffected == 0 {
		// request_id 是主站处罚幂等键。相同编号若携带不同付款主体、金额或动作，
		// 必须拒绝而不是覆盖/忽略，避免多实例并发时把处罚落到错误主体。
		return fmt.Errorf("%w: request_id=%q", errContentModerationPenaltyPayloadConflict, strings.TrimSpace(req.RequestID))
	}
	return nil
}

func (r *DurableContentModerationPenaltyReporter) retryDue(ctx context.Context) error {
	for processed := 0; processed < contentModerationPenaltyBatchSize; processed++ {
		item, ok, err := r.claimNext(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if err := r.retryItem(ctx, item); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("gateway-lite content moderation penalty deferred", "request_id", item.requestID, "error", err)
		}
	}
	return nil
}

func (r *DurableContentModerationPenaltyReporter) claimNext(ctx context.Context) (contentModerationPenaltyOutboxItem, bool, error) {
	claimToken := uuid.NewString()
	dbCtx, cancel := context.WithTimeout(ctx, contentModerationPenaltyDBTimeout)
	defer cancel()
	row := r.db.QueryRowContext(dbCtx, `
		WITH due AS (
			SELECT request_id
			FROM gateway_lite_content_moderation_penalty_outbox
			WHERE status = 'pending'
			  AND next_retry_at <= NOW()
			  AND (claim_token = '' OR claimed_at IS NULL OR claimed_at <= NOW() - ($1 * INTERVAL '1 millisecond'))
			ORDER BY next_retry_at ASC, created_at ASC, request_id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE gateway_lite_content_moderation_penalty_outbox AS outbox
		SET claim_token = $2, claimed_at = NOW(), updated_at = NOW()
		FROM due
		WHERE outbox.request_id = due.request_id
		RETURNING outbox.request_id, outbox.event_payload, outbox.attempts
	`, contentModerationPenaltyClaimTTL.Milliseconds(), claimToken)
	item := contentModerationPenaltyOutboxItem{claimToken: claimToken}
	if err := row.Scan(&item.requestID, &item.payload, &item.attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contentModerationPenaltyOutboxItem{}, false, nil
		}
		return contentModerationPenaltyOutboxItem{}, false, err
	}
	return item, true, nil
}

func (r *DurableContentModerationPenaltyReporter) retryItem(parent context.Context, item contentModerationPenaltyOutboxItem) error {
	var req ContentModerationPenaltyRequest
	if err := json.Unmarshal(item.payload, &req); err != nil {
		return r.markDead(parent, item, fmt.Errorf("decode penalty outbox event: %w", err))
	}
	if r.clientRef == nil || !r.clientRef.Configured() {
		return r.deferItem(parent, item, errors.New("control plane client is not configured"))
	}
	sendCtx, cancel := context.WithTimeout(parent, contentModerationPenaltySendTimeout)
	_, err := r.clientRef.ReportContentModerationPenalty(sendCtx, req)
	cancel()
	if err != nil {
		if !isContentModerationPenaltyRetryableFailure(err) {
			return r.markDead(parent, item, err)
		}
		return r.deferItem(parent, item, err)
	}
	dbCtx, dbCancel := context.WithTimeout(parent, contentModerationPenaltyDBTimeout)
	defer dbCancel()
	_, err = r.db.ExecContext(dbCtx, `
		DELETE FROM gateway_lite_content_moderation_penalty_outbox
		WHERE request_id = $1 AND claim_token = $2
	`, item.requestID, item.claimToken)
	return err
}

func (r *DurableContentModerationPenaltyReporter) deferItem(parent context.Context, item contentModerationPenaltyOutboxItem, retryErr error) error {
	delay := contentModerationPenaltyRetryDelay(item.attempts + 1)
	dbCtx, cancel := context.WithTimeout(parent, contentModerationPenaltyDBTimeout)
	defer cancel()
	_, err := r.db.ExecContext(dbCtx, `
		UPDATE gateway_lite_content_moderation_penalty_outbox
		SET attempts = attempts + 1,
			next_retry_at = NOW() + ($3 * INTERVAL '1 millisecond'),
			last_error = $4,
			claim_token = '',
			claimed_at = NULL,
			updated_at = NOW()
		WHERE request_id = $1 AND claim_token = $2
	`, item.requestID, item.claimToken, delay.Milliseconds(), truncateContentModerationPenaltyError(retryErr))
	if err != nil {
		return fmt.Errorf("penalty retry failed: %v; update retry state: %w", retryErr, err)
	}
	return retryErr
}

func (r *DurableContentModerationPenaltyReporter) markDead(parent context.Context, item contentModerationPenaltyOutboxItem, permanentErr error) error {
	dbCtx, cancel := context.WithTimeout(parent, contentModerationPenaltyDBTimeout)
	defer cancel()
	_, err := r.db.ExecContext(dbCtx, `
		UPDATE gateway_lite_content_moderation_penalty_outbox
		SET status = 'dead',
			attempts = attempts + 1,
			last_error = $3,
			dead_at = NOW(),
			claim_token = '',
			claimed_at = NULL,
			updated_at = NOW()
		WHERE request_id = $1 AND claim_token = $2
	`, item.requestID, item.claimToken, truncateContentModerationPenaltyError(permanentErr))
	if err != nil {
		return fmt.Errorf("permanent penalty delivery failed: %v; mark dead: %w", permanentErr, err)
	}
	return permanentErr
}

func isContentModerationPenaltyRetryableFailure(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *ControlPlaneHTTPError
	if !errors.As(err, &httpErr) {
		// 网络、超时和主站尚未配置均可能在运行时恢复；未知错误按可重试
		// 处理，避免一次短暂故障直接丢失应执行的处罚。
		return true
	}
	status := httpErr.StatusCode
	return status == 408 || status == 425 || status == 429 || status >= 500
}

func (r *DurableContentModerationPenaltyReporter) signalRetry() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func contentModerationPenaltyRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second
	for i := 1; i < attempt && delay < contentModerationPenaltyMaxDelay; i++ {
		delay *= 2
	}
	if delay > contentModerationPenaltyMaxDelay {
		return contentModerationPenaltyMaxDelay
	}
	return delay
}

func truncateContentModerationPenaltyError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > contentModerationPenaltyMaxErrorLen {
		message = message[:contentModerationPenaltyMaxErrorLen]
	}
	return message
}
