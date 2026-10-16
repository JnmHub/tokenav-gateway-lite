package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type operationAuditRepository struct {
	db *sql.DB
}

func NewOperationAuditRepository(db *sql.DB) service.OperationAuditRepository {
	return &operationAuditRepository{db: db}
}

type auditExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type auditQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const operationAuditColumns = `
created_at, actor_user_id, actor_email, actor_role, auth_method,
action, resource_type, resource_id, result, method, path, request_id,
client_ip, ua_hash, status_code, latency_ms, summary, extra`

func normalizeOperationAudit(entry *service.OperationAuditLog) {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	entry.ActorEmail = truncateAuditString(entry.ActorEmail, 255)
	entry.ActorRole = truncateAuditString(entry.ActorRole, 32)
	entry.AuthMethod = truncateAuditString(entry.AuthMethod, 32)
	entry.Action = truncateAuditString(entry.Action, 128)
	entry.ResourceType = truncateAuditString(entry.ResourceType, 64)
	entry.ResourceID = truncateAuditString(entry.ResourceID, 128)
	entry.Result = truncateAuditString(entry.Result, 24)
	entry.Method = truncateAuditString(entry.Method, 12)
	entry.Path = truncateAuditString(entry.Path, 512)
	entry.RequestID = truncateAuditString(entry.RequestID, 128)
	entry.ClientIP = truncateAuditString(entry.ClientIP, 64)
	entry.UAHash = truncateAuditString(entry.UAHash, 64)
	// Repository-level defense in depth: future call sites cannot bypass the
	// service and persist request values or credentials in JSONB.
	entry.Summary = service.SanitizeOperationAuditSummary(entry.Summary)
	entry.Extra = service.SanitizeOperationAuditExtra(entry.Extra)
}

func insertOperationAudit(ctx context.Context, exec auditQueryRower, entry *service.OperationAuditLog) (int64, error) {
	if entry == nil {
		return 0, errors.New("nil operation audit log")
	}
	normalizeOperationAudit(entry)
	summary, err := json.Marshal(nonNilAuditMap(entry.Summary))
	if err != nil {
		return 0, err
	}
	extra, err := json.Marshal(nonNilAuditMap(entry.Extra))
	if err != nil {
		return 0, err
	}
	var id int64
	err = exec.QueryRowContext(ctx, `INSERT INTO operation_audit_logs (`+operationAuditColumns+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17::jsonb,$18::jsonb)
RETURNING id`, entry.CreatedAt, nullableAuditID(entry.ActorUserID), entry.ActorEmail, entry.ActorRole, entry.AuthMethod,
		entry.Action, entry.ResourceType, entry.ResourceID, entry.Result, entry.Method, entry.Path, entry.RequestID,
		entry.ClientIP, entry.UAHash, entry.StatusCode, entry.LatencyMs, string(summary), string(extra)).Scan(&id)
	return id, err
}

func (r *operationAuditRepository) Insert(ctx context.Context, entry *service.OperationAuditLog) (int64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("nil operation audit repository")
	}
	return insertOperationAudit(ctx, r.db, entry)
}

func (r *operationAuditRepository) BatchInsert(ctx context.Context, entries []*service.OperationAuditLog) (int64, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var inserted int64
	for _, entry := range entries {
		if _, err := insertOperationAudit(ctx, tx, entry); err != nil {
			return inserted, err
		}
		inserted++
	}
	return inserted, tx.Commit()
}

func (r *operationAuditRepository) UpdateResult(ctx context.Context, id int64, result string, statusCode int, latencyMs int64, summary map[string]any) error {
	raw, err := json.Marshal(nonNilAuditMap(service.SanitizeOperationAuditSummary(summary)))
	if err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE operation_audit_logs
SET result=$2, status_code=$3, latency_ms=$4, summary=summary || $5::jsonb
WHERE id=$1`, id, truncateAuditString(result, 24), statusCode, latencyMs, string(raw))
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected != 1 {
		return service.ErrOperationAuditNotFound
	}
	return nil
}

const operationAuditSelect = `
id, created_at, actor_user_id, actor_email, actor_role, auth_method,
action, resource_type, resource_id, result, method, path, request_id,
client_ip, ua_hash, status_code, latency_ms, summary::text, extra::text`

func scanOperationAudit(scan func(...any) error) (*service.OperationAuditLog, error) {
	item := &service.OperationAuditLog{}
	var actor sql.NullInt64
	var summaryRaw, extraRaw string
	if err := scan(&item.ID, &item.CreatedAt, &actor, &item.ActorEmail, &item.ActorRole, &item.AuthMethod,
		&item.Action, &item.ResourceType, &item.ResourceID, &item.Result, &item.Method, &item.Path, &item.RequestID,
		&item.ClientIP, &item.UAHash, &item.StatusCode, &item.LatencyMs, &summaryRaw, &extraRaw); err != nil {
		return nil, err
	}
	if actor.Valid {
		id := actor.Int64
		item.ActorUserID = &id
	}
	_ = json.Unmarshal([]byte(summaryRaw), &item.Summary)
	_ = json.Unmarshal([]byte(extraRaw), &item.Extra)
	return item, nil
}

func (r *operationAuditRepository) List(ctx context.Context, filter *service.OperationAuditFilter) (*service.OperationAuditList, error) {
	if filter == nil {
		filter = &service.OperationAuditFilter{}
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 10)
	add := func(expr string, value any) {
		args = append(args, value)
		clauses = append(clauses, strings.ReplaceAll(expr, "?", "$"+strconv.Itoa(len(args))))
	}
	if filter.BeforeID > 0 {
		add("id < ?", filter.BeforeID)
	}
	if filter.ActorUserID != nil {
		add("actor_user_id = ?", *filter.ActorUserID)
	}
	if v := strings.TrimSpace(filter.ActorEmail); v != "" {
		add("actor_email = ?", v)
	}
	if v := strings.TrimSpace(filter.Action); v != "" {
		add("action = ?", v)
	}
	if v := strings.TrimSpace(filter.ResourceType); v != "" {
		add("resource_type = ?", v)
	}
	if v := strings.TrimSpace(filter.ResourceID); v != "" {
		add("resource_id = ?", v)
	}
	if v := strings.TrimSpace(filter.Result); v != "" {
		add("result = ?", v)
	}
	if filter.StartTime != nil {
		add("created_at >= ?", filter.StartTime.UTC())
	}
	if filter.EndTime != nil {
		add("created_at <= ?", filter.EndTime.UTC())
	}
	args = append(args, limit+1)
	query := `SELECT ` + operationAuditSelect + ` FROM operation_audit_logs WHERE ` + strings.Join(clauses, " AND ") + `
ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]*service.OperationAuditLog, 0, limit+1)
	for rows.Next() {
		item, err := scanOperationAudit(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := &service.OperationAuditList{Items: items}
	if len(items) > limit {
		result.NextCursor = items[limit-1].ID
		result.Items = items[:limit]
	}
	return result, nil
}

func (r *operationAuditRepository) GetByID(ctx context.Context, id int64) (*service.OperationAuditLog, error) {
	item, err := scanOperationAudit(r.db.QueryRowContext(ctx, `SELECT `+operationAuditSelect+` FROM operation_audit_logs WHERE id=$1`, id).Scan)
	if err == sql.ErrNoRows {
		return nil, service.ErrOperationAuditNotFound
	}
	return item, err
}

func (r *operationAuditRepository) PreviewClear(ctx context.Context) (*service.OperationAuditClearPreview, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil operation audit repository")
	}
	var count int64
	var oldest, newest sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM operation_audit_logs`).Scan(&count, &oldest, &newest); err != nil {
		return nil, err
	}
	preview := &service.OperationAuditClearPreview{Rows: count}
	if oldest.Valid {
		value := oldest.Time.UTC()
		preview.OldestAt = &value
	}
	if newest.Valid {
		value := newest.Time.UTC()
		preview.NewestAt = &value
	}
	return preview, nil
}

func (r *operationAuditRepository) DeleteBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	res, err := r.db.ExecContext(ctx, `WITH batch AS (
SELECT id FROM operation_audit_logs WHERE created_at < $1 ORDER BY id LIMIT $2
)
DELETE FROM operation_audit_logs WHERE id IN (SELECT id FROM batch)`, cutoff.UTC(), batchSize)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *operationAuditRepository) ClearAllWithTrace(ctx context.Context, trace *service.OperationAuditLog) (int64, error) {
	if trace == nil {
		return 0, errors.New("audit clear trace is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_audit_logs`).Scan(&count); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `TRUNCATE TABLE operation_audit_logs RESTART IDENTITY`); err != nil {
		return 0, err
	}
	trace.Action = service.AuditActionAuditLogClear
	trace.Result = service.AuditResultSuccess
	if trace.Summary == nil {
		trace.Summary = map[string]any{}
	}
	trace.Summary["deleted_rows"] = count
	if _, err := insertOperationAudit(ctx, tx, trace); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func nonNilAuditMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nullableAuditID(value *int64) any {
	if value == nil || *value <= 0 {
		return nil
	}
	return *value
}

func truncateAuditString(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && len(string(runes)) > max {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

var _ auditExecer = (*sql.Tx)(nil)
var _ = fmt.Sprintf
