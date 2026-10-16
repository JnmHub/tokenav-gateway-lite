package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultGrokVideoTaskBatchSize = 50

// grokVideoTaskStore intentionally uses PostgreSQL rather than Redis for the
// task payload. Video settlement is financial state: it must survive service
// restarts, Redis eviction and cache flushes. Short-lived cross-instance locks
// are persisted on the same row and expire automatically after a crash.
type grokVideoTaskStore struct {
	db *sql.DB
}

func NewGrokVideoTaskStore(db *sql.DB) service.GrokVideoTaskStore {
	return &grokVideoTaskStore{db: db}
}

func (s *grokVideoTaskStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("grok video task database is unavailable")
	}
	return s.db.PingContext(ctx)
}

func (s *grokVideoTaskStore) Save(ctx context.Context, task *service.GrokVideoTask) error {
	if task == nil || strings.TrimSpace(task.RequestID) == "" {
		return service.ErrGrokVideoTaskNotFound
	}
	if s == nil || s.db == nil {
		return errors.New("grok video task database is unavailable")
	}
	task.Touch()
	payload, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal grok video task: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO grok_video_tasks (
			request_id, state, task_data, next_poll_at,
			created_at, updated_at, completed_at, billed_at
		) VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8)
		ON CONFLICT (request_id) DO UPDATE SET
			state = EXCLUDED.state,
			task_data = EXCLUDED.task_data,
			next_poll_at = EXCLUDED.next_poll_at,
			updated_at = EXCLUDED.updated_at,
			completed_at = EXCLUDED.completed_at,
			billed_at = EXCLUDED.billed_at
	`,
		task.RequestID,
		task.State,
		payload,
		task.NextPollAt,
		task.CreatedAt,
		task.UpdatedAt,
		task.CompletedAt,
		task.BilledAt,
	)
	if err != nil {
		return fmt.Errorf("persist grok video task: %w", err)
	}
	return nil
}

func (s *grokVideoTaskStore) Get(ctx context.Context, requestID string) (*service.GrokVideoTask, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || s == nil || s.db == nil {
		return nil, service.ErrGrokVideoTaskNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_data
		FROM grok_video_tasks
		WHERE request_id = $1
	`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrGrokVideoTaskNotFound
	}
	var payload []byte
	if err := rows.Scan(&payload); err != nil {
		return nil, err
	}
	task, err := decodeGrokVideoTask(payload)
	if err != nil {
		return nil, err
	}
	if task.RequestID != requestID {
		return nil, service.ErrGrokVideoTaskNotFound
	}
	return task, nil
}

func (s *grokVideoTaskStore) ListDue(ctx context.Context, now time.Time, limit int) ([]*service.GrokVideoTask, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("grok video task database is unavailable")
	}
	if limit <= 0 {
		limit = defaultGrokVideoTaskBatchSize
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_data
		FROM grok_video_tasks
		WHERE state IN ('pending', 'running', 'succeeded')
		  AND COALESCE(next_poll_at, updated_at) <= $1
		  AND (poll_locked_until IS NULL OR poll_locked_until <= $1)
		ORDER BY COALESCE(next_poll_at, updated_at), created_at
		LIMIT $2
	`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]*service.GrokVideoTask, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		task, err := decodeGrokVideoTask(payload)
		if err != nil {
			return nil, err
		}
		if task.NeedsBackgroundWork() {
			tasks = append(tasks, task)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *grokVideoTaskStore) TryLockPolling(ctx context.Context, requestID string, ttl time.Duration) (bool, error) {
	return s.tryLock(ctx, requestID, "poll_locked_until", ttl, false)
}

func (s *grokVideoTaskStore) UnlockPolling(ctx context.Context, requestID string) error {
	return s.unlock(ctx, requestID, "poll_locked_until")
}

func (s *grokVideoTaskStore) TryLockSettlement(ctx context.Context, requestID string, ttl time.Duration) (bool, error) {
	return s.tryLock(ctx, requestID, "settlement_locked_until", ttl, true)
}

func (s *grokVideoTaskStore) UnlockSettlement(ctx context.Context, requestID string) error {
	return s.unlock(ctx, requestID, "settlement_locked_until")
}

func (s *grokVideoTaskStore) tryLock(ctx context.Context, requestID, column string, ttl time.Duration, settlement bool) (bool, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return false, service.ErrGrokVideoTaskNotFound
	}
	if s == nil || s.db == nil {
		return false, errors.New("grok video task database is unavailable")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	if column != "poll_locked_until" && column != "settlement_locked_until" {
		return false, errors.New("invalid grok video task lock column")
	}
	statePredicate := "state IN ('pending', 'running', 'succeeded')"
	if settlement {
		statePredicate = "state = 'succeeded'"
	}
	query := fmt.Sprintf(`
		UPDATE grok_video_tasks
		SET %s = NOW() + ($2 * INTERVAL '1 millisecond')
		WHERE request_id = $1
		  AND %s
		  AND (%s IS NULL OR %s <= NOW())
		RETURNING request_id
	`, column, statePredicate, column, column)
	rows, err := s.db.QueryContext(ctx, query, requestID, ttl.Milliseconds())
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	var lockedRequestID string
	if err := rows.Scan(&lockedRequestID); err != nil {
		return false, err
	}
	return strings.TrimSpace(lockedRequestID) == requestID, nil
}

func (s *grokVideoTaskStore) unlock(ctx context.Context, requestID, column string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	if s == nil || s.db == nil {
		return errors.New("grok video task database is unavailable")
	}
	if column != "poll_locked_until" && column != "settlement_locked_until" {
		return errors.New("invalid grok video task lock column")
	}
	query := fmt.Sprintf("UPDATE grok_video_tasks SET %s = NULL WHERE request_id = $1", column)
	_, err := s.db.ExecContext(ctx, query, requestID)
	return err
}

func (s *grokVideoTaskStore) CleanupFinalized(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("grok video task database is unavailable")
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM grok_video_tasks
		WHERE state IN ('failed', 'cancelled', 'billed')
		  AND COALESCE(completed_at, updated_at) < $1
	`, before.UTC())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func decodeGrokVideoTask(payload []byte) (*service.GrokVideoTask, error) {
	var task service.GrokVideoTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, fmt.Errorf("decode grok video task: %w", err)
	}
	task.Normalize()
	if strings.TrimSpace(task.RequestID) == "" {
		return nil, service.ErrGrokVideoTaskNotFound
	}
	return &task, nil
}

var _ service.GrokVideoTaskStore = (*grokVideoTaskStore)(nil)
