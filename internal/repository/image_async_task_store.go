package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type imageAsyncTaskStore struct {
	db *sql.DB
}

func NewImageAsyncTaskStore(db *sql.DB) service.ImageTaskStore {
	return &imageAsyncTaskStore{db: db}
}

func (s *imageAsyncTaskStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("image task database is unavailable")
	}
	return s.db.PingContext(ctx)
}

func (s *imageAsyncTaskStore) FindByIdempotency(ctx context.Context, userID, apiKeyID int64, gatewayKeyID, idempotencyKey string) (*service.ImageTask, error) {
	if s == nil || s.db == nil || userID <= 0 || strings.TrimSpace(idempotencyKey) == "" {
		return nil, service.ErrImageTaskNotFound
	}
	return scanImageTask(s.db.QueryRowContext(
		ctx,
		imageTaskSelect+` WHERE owner_key=$1 AND idempotency_key=$2`,
		imageTaskOwnerKey(userID, apiKeyID, gatewayKeyID),
		strings.TrimSpace(idempotencyKey),
	))
}

func imageTaskOwnerKey(userID, apiKeyID int64, gatewayKeyID string) string {
	if remote := strings.TrimSpace(gatewayKeyID); remote != "" {
		return fmt.Sprintf("u:%d:r:%s", userID, remote)
	}
	return fmt.Sprintf("u:%d:l:%d", userID, apiKeyID)
}

func (s *imageAsyncTaskStore) Create(ctx context.Context, task *service.ImageTask, queueCapacity, perUserConcurrency int) (*service.ImageTask, bool, error) {
	if task == nil || strings.TrimSpace(task.ID) == "" || s == nil || s.db == nil {
		return nil, false, service.ErrImageTaskNotFound
	}
	if queueCapacity <= 0 || perUserConcurrency <= 0 {
		return nil, false, errors.New("image task queue limits are invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	ownerKey := imageTaskOwnerKey(task.UserID, task.APIKeyID, task.GatewayKeyID)
	// The two advisory locks make capacity checks and insertion one atomic
	// decision across all gateway instances without serializing different users.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x494d4751)); err != nil {
		return nil, false, err
	}
	userLockKey := fmt.Sprintf("u:%d", task.UserID)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userLockKey); err != nil {
		return nil, false, err
	}
	if task.IdempotencyKey != "" {
		existing, err := getImageTaskByIdempotency(ctx, tx, ownerKey, task.IdempotencyKey)
		if err == nil {
			if existing.PayloadHash != task.PayloadHash {
				return nil, false, fmt.Errorf("%w: idempotency key was used for a different request", service.ErrImageTaskConflict)
			}
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			return existing, true, nil
		}
		if !errors.Is(err, service.ErrImageTaskNotFound) {
			return nil, false, err
		}
	}
	var depth int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM image_async_tasks
		WHERE state IN ('queued','running') OR settlement_state = 'pending'
	`).Scan(&depth); err != nil {
		return nil, false, err
	}
	if depth >= queueCapacity {
		return nil, false, service.ErrImageTaskQueueFull
	}
	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM image_async_tasks
		WHERE user_id = $1 AND state IN ('queued','running')
	`, task.UserID).Scan(&active); err != nil {
		return nil, false, err
	}
	if active >= perUserConcurrency {
		return nil, false, fmt.Errorf("%w: user image task concurrency limit reached", service.ErrImageTaskQueueFull)
	}
	payload, err := marshalImageTask(task)
	if err != nil {
		return nil, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO image_async_tasks (
			id, owner_key, user_id, api_key_id, gateway_key_id, idempotency_key,
			payload_hash, state, settlement_state, encrypted_request, task_data,
			next_attempt_at, claim_owner, claim_until, created_at, updated_at,
			completed_at, expires_at
		) VALUES (
			$1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,
			$12,'',NULL,$13,$14,$15,$16
		)
	`, task.ID, ownerKey, task.UserID, task.APIKeyID, task.GatewayKeyID, task.IdempotencyKey,
		task.PayloadHash, task.State, task.SettlementState, task.EncryptedRequest, payload,
		task.NextAttemptAt, task.CreatedAt, task.UpdatedAt, task.CompletedAt, task.ExpiresAt)
	if err != nil {
		return nil, false, fmt.Errorf("insert image task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return cloneImageTask(task), false, nil
}

func (s *imageAsyncTaskStore) Get(ctx context.Context, id string) (*service.ImageTask, error) {
	id = strings.TrimSpace(id)
	if s == nil || s.db == nil || !validImageTaskID(id) {
		return nil, service.ErrImageTaskNotFound
	}
	return scanImageTask(s.db.QueryRowContext(ctx, imageTaskSelect+` WHERE id = $1::uuid`, id))
}

func (s *imageAsyncTaskStore) GetByUser(ctx context.Context, id string, userID int64) (*service.ImageTask, error) {
	id = strings.TrimSpace(id)
	if s == nil || s.db == nil || !validImageTaskID(id) || userID <= 0 {
		return nil, service.ErrImageTaskNotFound
	}
	// Ownership is part of the SQL predicate so a caller cannot distinguish a
	// missing task from a task owned by another user.
	return scanImageTask(s.db.QueryRowContext(
		ctx,
		imageTaskSelect+` WHERE id = $1::uuid AND user_id = $2`,
		id, userID,
	))
}

func getImageTaskByIdempotency(ctx context.Context, tx *sql.Tx, ownerKey, idempotencyKey string) (*service.ImageTask, error) {
	return scanImageTask(tx.QueryRowContext(ctx, imageTaskSelect+` WHERE owner_key=$1 AND idempotency_key=$2`, ownerKey, idempotencyKey))
}

const imageTaskSelect = `
	SELECT task_data, encrypted_request, claim_owner, claim_until
	FROM image_async_tasks`

const imageTaskMetadataSelect = `
	SELECT task_data, claim_owner, claim_until
	FROM image_async_tasks`

type imageTaskScanner interface {
	Scan(dest ...any) error
}

func scanImageTask(row imageTaskScanner) (*service.ImageTask, error) {
	var payload []byte
	var encrypted, claimOwner string
	var claimUntil sql.NullTime
	if err := row.Scan(&payload, &encrypted, &claimOwner, &claimUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrImageTaskNotFound
		}
		return nil, err
	}
	var task service.ImageTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, fmt.Errorf("decode image task: %w", err)
	}
	task.EncryptedRequest = encrypted
	task.ClaimOwner = claimOwner
	if claimUntil.Valid {
		value := claimUntil.Time.UTC()
		task.ClaimUntil = &value
	}
	return &task, nil
}

// scanImageTaskMetadata deliberately omits encrypted_request. Status pages
// never need to decrypt a request, and selecting dozens of bounded-but-large
// ciphertext rows would waste database bandwidth and heap memory.
func scanImageTaskMetadata(row imageTaskScanner) (*service.ImageTask, error) {
	var payload []byte
	var claimOwner string
	var claimUntil sql.NullTime
	if err := row.Scan(&payload, &claimOwner, &claimUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrImageTaskNotFound
		}
		return nil, err
	}
	var task service.ImageTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, fmt.Errorf("decode image task metadata: %w", err)
	}
	task.ClaimOwner = claimOwner
	if claimUntil.Valid {
		value := claimUntil.Time.UTC()
		task.ClaimUntil = &value
	}
	return &task, nil
}

func (s *imageAsyncTaskStore) ListByOwner(ctx context.Context, userID, apiKeyID int64, gatewayKeyID string, limit int, cursor string) ([]*service.ImageTask, string, error) {
	if s == nil || s.db == nil {
		return nil, "", errors.New("image task database is unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ownerKey := imageTaskOwnerKey(userID, apiKeyID, gatewayKeyID)
	return s.list(ctx, `owner_key=$1`, []any{ownerKey}, limit, cursor)
}

func (s *imageAsyncTaskStore) ListByUser(ctx context.Context, userID int64, limit int, cursor string) ([]*service.ImageTask, string, error) {
	return s.ListByUserState(ctx, userID, "", limit, cursor)
}

func (s *imageAsyncTaskStore) ListByUserState(ctx context.Context, userID int64, state string, limit int, cursor string) ([]*service.ImageTask, string, error) {
	if s == nil || s.db == nil {
		return nil, "", errors.New("image task database is unavailable")
	}
	if userID <= 0 {
		return nil, "", service.ErrImageTaskNotFound
	}
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return s.list(ctx, `user_id=$1`, []any{userID}, limit, cursor)
	}
	if !validImageTaskState(state) {
		return nil, "", fmt.Errorf("%w: invalid image task state %q", service.ErrImageTaskInvalidInput, state)
	}
	return s.list(ctx, `user_id=$1 AND state=$2`, []any{userID, state}, limit, cursor)
}

func (s *imageAsyncTaskStore) ListAll(ctx context.Context, state string, limit int, cursor string) ([]*service.ImageTask, string, error) {
	if s == nil || s.db == nil {
		return nil, "", errors.New("image task database is unavailable")
	}
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return s.list(ctx, `TRUE`, nil, limit, cursor)
	}
	if !validImageTaskState(state) {
		return nil, "", fmt.Errorf("%w: invalid image task state %q", service.ErrImageTaskInvalidInput, state)
	}
	return s.list(ctx, `state=$1`, []any{state}, limit, cursor)
}

func (s *imageAsyncTaskStore) list(ctx context.Context, where string, args []any, limit int, cursor string) ([]*service.ImageTask, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := imageTaskMetadataSelect + ` WHERE ` + where
	if strings.TrimSpace(cursor) != "" {
		createdAt, id, err := decodeImageTaskCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		createdPlaceholder := len(args) + 1
		idPlaceholder := len(args) + 2
		query += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d::uuid)`, createdPlaceholder, idPlaceholder)
		args = append(args, createdAt, id)
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]*service.ImageTask, 0, limit+1)
	for rows.Next() {
		task, err := scanImageTaskMetadata(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, task)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		next = encodeImageTaskCursor(items[limit-1].CreatedAt, items[limit-1].ID)
		items = items[:limit]
	}
	return items, next, nil
}

func encodeImageTaskCursor(createdAt time.Time, id string) string {
	raw := strconv.FormatInt(createdAt.UTC().UnixNano(), 10) + ":" + strings.TrimSpace(id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeImageTaskCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: invalid image task cursor", service.ErrImageTaskInvalidInput)
	}
	timestamp, id, ok := strings.Cut(string(raw), ":")
	if !ok || strings.TrimSpace(id) == "" {
		return time.Time{}, "", fmt.Errorf("%w: invalid image task cursor", service.ErrImageTaskInvalidInput)
	}
	nanos, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || nanos <= 0 {
		return time.Time{}, "", fmt.Errorf("%w: invalid image task cursor", service.ErrImageTaskInvalidInput)
	}
	id = strings.TrimSpace(id)
	if !validImageTaskID(id) {
		return time.Time{}, "", fmt.Errorf("%w: invalid image task cursor", service.ErrImageTaskInvalidInput)
	}
	return time.Unix(0, nanos).UTC(), id, nil
}

func validImageTaskID(id string) bool {
	_, err := uuid.Parse(strings.TrimSpace(id))
	return err == nil
}

func validImageTaskState(state string) bool {
	switch state {
	case service.ImageTaskStateQueued, service.ImageTaskStateRunning, service.ImageTaskStateSucceeded,
		service.ImageTaskStateFailed, service.ImageTaskStateCancelled, service.ImageTaskStateExpired:
		return true
	default:
		return false
	}
}

func (s *imageAsyncTaskStore) QueueDepth(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("image task database is unavailable")
	}
	var count int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM image_async_tasks
		WHERE state IN ('queued','running') OR settlement_state='pending'
	`).Scan(&count)
	return count, err
}

func (s *imageAsyncTaskStore) ActiveForUser(ctx context.Context, userID int64) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("image task database is unavailable")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_async_tasks WHERE user_id=$1 AND state IN ('queued','running')`, userID).Scan(&count)
	return count, err
}

func (s *imageAsyncTaskStore) ClaimDue(ctx context.Context, owner string, now time.Time, limit int, ttl time.Duration) ([]*service.ImageTask, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("image task database is unavailable")
	}
	if limit <= 0 {
		limit = 10
	}
	if ttl <= 0 {
		return nil, errors.New("image task claim ttl must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM image_async_tasks
			WHERE next_attempt_at <= $1
			  AND (claim_until IS NULL OR claim_until <= $1)
			  AND (
				state IN ('queued','running')
				OR settlement_state = 'pending'
			  )
			ORDER BY next_attempt_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE image_async_tasks t
		SET claim_owner=$3, claim_until=$1 + ($4 * interval '1 millisecond'), updated_at=$1
		FROM candidates c
		WHERE t.id=c.id
		RETURNING t.task_data, t.encrypted_request, t.claim_owner, t.claim_until
	`, now.UTC(), limit, owner, ttl.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*service.ImageTask, 0, limit)
	for rows.Next() {
		task, err := scanImageTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *imageAsyncTaskStore) SaveClaimed(ctx context.Context, task *service.ImageTask, owner string) error {
	if s == nil || s.db == nil || task == nil {
		return service.ErrImageTaskNotFound
	}
	payload, err := marshalImageTask(task)
	if err != nil {
		return err
	}
	keepClaim := task.State == service.ImageTaskStateRunning ||
		(task.State == service.ImageTaskStateSucceeded && task.SettlementState == service.ImageTaskSettlementPending && task.ErrorCode == "")
	claimOwner := ""
	var claimUntil any
	if keepClaim {
		claimOwner = owner
		claimUntil = task.ClaimUntil
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE image_async_tasks SET
			state=$1, settlement_state=$2, task_data=$3::jsonb,
			next_attempt_at=$4, claim_owner=$5, claim_until=$6,
			updated_at=$7, completed_at=$8, expires_at=$9
		WHERE id=$10::uuid AND claim_owner=$11
	`, task.State, task.SettlementState, payload, task.NextAttemptAt, claimOwner, claimUntil,
		task.UpdatedAt, task.CompletedAt, task.ExpiresAt, task.ID, owner)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrImageTaskConflict
	}
	if !keepClaim {
		task.ClaimOwner = ""
		task.ClaimUntil = nil
	}
	return nil
}

func (s *imageAsyncTaskStore) SaveUnclaimedTerminal(ctx context.Context, task *service.ImageTask) error {
	if s == nil || s.db == nil || task == nil || !task.Terminal() {
		return service.ErrImageTaskConflict
	}
	payload, err := marshalImageTask(task)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE image_async_tasks SET
			state=$1, settlement_state=$2, task_data=$3::jsonb,
			next_attempt_at=$4, updated_at=$5, completed_at=$6, expires_at=$7
		WHERE id=$8::uuid AND (claim_until IS NULL OR claim_until <= NOW())
	`, task.State, task.SettlementState, payload, task.NextAttemptAt,
		task.UpdatedAt, task.CompletedAt, task.ExpiresAt, task.ID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrImageTaskConflict
	}
	return nil
}

func (s *imageAsyncTaskStore) RequestCancel(ctx context.Context, id string, userID, apiKeyID int64, gatewayKeyID string, now time.Time) (*service.ImageTask, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("image task database is unavailable")
	}
	id = strings.TrimSpace(id)
	if !validImageTaskID(id) {
		return nil, service.ErrImageTaskNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	task, err := scanImageTask(tx.QueryRowContext(ctx, imageTaskSelect+` WHERE id=$1::uuid FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if !task.BelongsTo(userID, apiKeyID, gatewayKeyID) {
		return nil, service.ErrImageTaskForbidden
	}
	return cancelImageTaskInTransaction(ctx, tx, task, now)
}

func (s *imageAsyncTaskStore) RequestCancelForUser(ctx context.Context, id string, userID int64, now time.Time) (*service.ImageTask, error) {
	id = strings.TrimSpace(id)
	if s == nil || s.db == nil || !validImageTaskID(id) || userID <= 0 {
		return nil, service.ErrImageTaskNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// Keep user_id in the locked lookup. A mismatched owner is deliberately a
	// 404, preventing the control plane from probing task identifiers.
	task, err := scanImageTask(tx.QueryRowContext(
		ctx,
		imageTaskSelect+` WHERE id=$1::uuid AND user_id=$2 FOR UPDATE`,
		id, userID,
	))
	if err != nil {
		return nil, err
	}
	return cancelImageTaskInTransaction(ctx, tx, task, now)
}

func cancelImageTaskInTransaction(ctx context.Context, tx *sql.Tx, task *service.ImageTask, now time.Time) (*service.ImageTask, error) {
	if task.Terminal() {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return task, nil
	}
	if task.State != service.ImageTaskStateQueued || task.ExecutionPhase == service.ImageTaskExecutionPhaseDispatched {
		// Once dispatch is durable, cancelling the worker could still leave a
		// completed provider request behind. Refusing cancellation prevents a
		// customer refund from racing real upstream spend.
		return nil, service.ErrImageTaskAlreadyRunning
	}
	task.CancelRequested = true
	task.UpdatedAt = now.UTC()
	task.NextAttemptAt = now.UTC()
	task.State = service.ImageTaskStateCancelled
	task.Progress = 100
	completed := now.UTC()
	task.CompletedAt = &completed
	payload, err := marshalImageTask(task)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE image_async_tasks SET state=$1, task_data=$2::jsonb, next_attempt_at=$3,
			updated_at=$4, completed_at=$5,
			claim_owner='', claim_until=NULL
		WHERE id=$6::uuid
	`, task.State, payload, task.NextAttemptAt, task.UpdatedAt, task.CompletedAt, task.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *imageAsyncTaskStore) ListCleanupDue(ctx context.Context, now time.Time, limit int) ([]*service.ImageTask, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, imageTaskSelect+`
		WHERE expires_at <= $1
		  AND state IN ('succeeded','failed','cancelled','expired')
		  AND settlement_state IN ('settled','released')
		ORDER BY expires_at LIMIT $2
	`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]*service.ImageTask, 0, limit)
	for rows.Next() {
		task, err := scanImageTask(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, task)
	}
	return items, rows.Err()
}

func (s *imageAsyncTaskStore) DeleteFinal(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if !validImageTaskID(id) {
		return service.ErrImageTaskNotFound
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM image_async_tasks
		WHERE id=$1::uuid
		  AND state IN ('succeeded','failed','cancelled','expired')
		  AND settlement_state IN ('settled','released')
	`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrImageTaskConflict
	}
	return nil
}

func marshalImageTask(task *service.ImageTask) ([]byte, error) {
	payload, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("encode image task: %w", err)
	}
	return payload, nil
}

func cloneImageTask(task *service.ImageTask) *service.ImageTask {
	if task == nil {
		return nil
	}
	payload, _ := json.Marshal(task)
	var cloned service.ImageTask
	_ = json.Unmarshal(payload, &cloned)
	cloned.EncryptedRequest = task.EncryptedRequest
	cloned.ClaimOwner = task.ClaimOwner
	if task.ClaimUntil != nil {
		value := *task.ClaimUntil
		cloned.ClaimUntil = &value
	}
	return &cloned
}
