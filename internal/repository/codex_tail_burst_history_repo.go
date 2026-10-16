package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CreateCodexTailBurstSession records the immutable beginning of a tail-burst
// session. The session id is generated at the account CAS boundary, so an
// INSERT conflict is an expected idempotent retry rather than an error.
func (r *usageLogRepository) CreateCodexTailBurstSession(ctx context.Context, session service.CodexTailBurstSessionStart) error {
	if r == nil || r.sql == nil {
		return service.ErrCodexTailBurstHistoryUnavailable
	}
	if strings.TrimSpace(session.SessionID) == "" || session.AccountID <= 0 || session.StartedAt.IsZero() {
		return fmt.Errorf("invalid codex tail-burst session start")
	}
	_, err := r.sql.ExecContext(ctx, `
INSERT INTO codex_tail_burst_sessions
    (session_id, account_id, account_name, started_at, quota_window, reason, entry_percent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (session_id) DO NOTHING
`, strings.TrimSpace(session.SessionID), session.AccountID, strings.TrimSpace(session.AccountName),
		session.StartedAt.UTC(), strings.TrimSpace(session.Window), strings.TrimSpace(session.Reason), session.EntryPercent)
	return err
}

// UpdateCodexTailBurstSession refreshes the observed delta without changing a
// settled row. A missing row is deliberately ignored: a later settlement path
// creates it first, which keeps the request hot path resilient during rollout.
func (r *usageLogRepository) UpdateCodexTailBurstSession(ctx context.Context, session service.CodexTailBurstSessionUpdate) error {
	if r == nil || r.sql == nil {
		return service.ErrCodexTailBurstHistoryUnavailable
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return fmt.Errorf("invalid codex tail-burst session id")
	}
	// Upsert before updating so a transient failure on the initial start write
	// cannot make the entire session disappear from historical statistics.
	if session.AccountID > 0 && !session.StartedAt.IsZero() {
		if _, err := r.sql.ExecContext(ctx, `
INSERT INTO codex_tail_burst_sessions
    (session_id, account_id, account_name, started_at, quota_window, reason, entry_percent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (session_id) DO NOTHING
`, strings.TrimSpace(session.SessionID), session.AccountID, strings.TrimSpace(session.AccountName),
			session.StartedAt.UTC(), strings.TrimSpace(session.Window), strings.TrimSpace(session.Reason), session.EntryPercent); err != nil {
			return err
		}
	}
	_, err := r.sql.ExecContext(ctx, `
UPDATE codex_tail_burst_sessions
SET delta_percent = $2, updated_at = NOW()
WHERE session_id = $1 AND status = 'active'
`, strings.TrimSpace(session.SessionID), session.DeltaPercent)
	return err
}

// SettleCodexTailBurstSession closes an active history row. The status guard
// makes repeated collector/error-handler settlement calls idempotent.
func (r *usageLogRepository) SettleCodexTailBurstSession(ctx context.Context, session service.CodexTailBurstSessionSettlement) error {
	if r == nil || r.sql == nil {
		return service.ErrCodexTailBurstHistoryUnavailable
	}
	if strings.TrimSpace(session.SessionID) == "" || session.EndedAt.IsZero() {
		return fmt.Errorf("invalid codex tail-burst session settlement")
	}
	// Ensure a row exists before closing it. This compensates for a dropped
	// best-effort start write while preserving ON CONFLICT idempotency.
	if session.AccountID > 0 && !session.StartedAt.IsZero() {
		if _, err := r.sql.ExecContext(ctx, `
INSERT INTO codex_tail_burst_sessions
    (session_id, account_id, account_name, started_at, quota_window, reason, entry_percent)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (session_id) DO NOTHING
`, strings.TrimSpace(session.SessionID), session.AccountID, strings.TrimSpace(session.AccountName),
			session.StartedAt.UTC(), strings.TrimSpace(session.Window), strings.TrimSpace(session.Reason), session.EntryPercent); err != nil {
			return err
		}
	}
	_, err := r.sql.ExecContext(ctx, `
UPDATE codex_tail_burst_sessions
SET ended_at = $2,
    delta_percent = $3,
    requests = $4,
    tokens = $5,
    status = 'settled',
    updated_at = NOW()
WHERE session_id = $1 AND status = 'active'
`, strings.TrimSpace(session.SessionID), session.EndedAt.UTC(), session.DeltaPercent,
		session.Requests, session.Tokens)
	return err
}

var codexTailBurstHistoryWindows = map[string]struct{}{
	"5h": {},
	"7d": {},
}

var codexTailBurstHistoryReasons = map[string]struct{}{
	"used_ratio":     {},
	"reset_soon":     {},
	"account_expiry": {},
}

var codexTailBurstHistoryStatuses = map[string]struct{}{
	"active":  {},
	"settled": {},
}

func validateCodexTailBurstHistoryFilter(filter service.CodexTailBurstStatsFilter) error {
	if !filter.StartTime.IsZero() && !filter.EndTime.IsZero() && filter.StartTime.After(filter.EndTime) {
		return fmt.Errorf("invalid codex tail-burst history time range")
	}
	if filter.AccountID != nil && *filter.AccountID <= 0 {
		return fmt.Errorf("account_id must be greater than zero")
	}
	if filter.Window != "" {
		if _, ok := codexTailBurstHistoryWindows[strings.ToLower(strings.TrimSpace(filter.Window))]; !ok {
			return fmt.Errorf("invalid window")
		}
	}
	if filter.Reason != "" {
		if _, ok := codexTailBurstHistoryReasons[strings.ToLower(strings.TrimSpace(filter.Reason))]; !ok {
			return fmt.Errorf("invalid reason")
		}
	}
	if filter.Status != "" {
		if _, ok := codexTailBurstHistoryStatuses[strings.ToLower(strings.TrimSpace(filter.Status))]; !ok {
			return fmt.Errorf("invalid status")
		}
	}
	return nil
}

func codexTailBurstHistoryWhere(filter service.CodexTailBurstStatsFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := make([]any, 0, 6)
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if !filter.StartTime.IsZero() {
		add("s.started_at >= $%d", filter.StartTime.UTC())
	}
	if !filter.EndTime.IsZero() {
		add("s.started_at < $%d", filter.EndTime.UTC())
	}
	if filter.AccountID != nil {
		add("s.account_id = $%d", *filter.AccountID)
	}
	if value := strings.TrimSpace(strings.ToLower(filter.Window)); value != "" {
		add("s.quota_window = $%d", value)
	}
	if value := strings.TrimSpace(strings.ToLower(filter.Reason)); value != "" {
		add("s.reason = $%d", value)
	}
	if value := strings.TrimSpace(strings.ToLower(filter.Status)); value != "" {
		add("s.status = $%d", value)
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// GetCodexTailBurstStats reads the historical table with bounded, indexed
// queries. It intentionally lives on opsRepository but is exposed through an
// optional service capability, so existing OpsRepository test doubles remain
// source-compatible.
func (r *opsRepository) GetCodexTailBurstStats(ctx context.Context, filter service.CodexTailBurstStatsFilter) (*service.CodexTailBurstStatsResponse, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrCodexTailBurstHistoryUnavailable
	}
	if err := validateCodexTailBurstHistoryFilter(filter); err != nil {
		return nil, err
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	where, args := codexTailBurstHistoryWhere(filter)

	var summary service.CodexTailBurstStatsSummary
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COUNT(*) FILTER (WHERE s.status = 'active'),
       COUNT(*) FILTER (WHERE s.status = 'settled'),
       COUNT(DISTINCT s.account_id),
       COALESCE(SUM(s.requests), 0),
       COALESCE(SUM(s.tokens), 0),
       COALESCE(SUM(s.delta_percent), 0)
FROM codex_tail_burst_sessions s
`+where, args...).Scan(&summary.Sessions, &summary.ActiveSessions, &summary.SettledSessions,
		&summary.Accounts, &summary.Requests, &summary.Tokens, &summary.DeltaPercent); err != nil {
		return nil, err
	}

	trendRows, err := r.db.QueryContext(ctx, `
SELECT TO_CHAR((s.started_at AT TIME ZONE 'Asia/Shanghai')::date, 'YYYY-MM-DD') AS day,
       COUNT(*), COALESCE(SUM(s.requests), 0), COALESCE(SUM(s.tokens), 0),
       COALESCE(SUM(s.delta_percent), 0)
FROM codex_tail_burst_sessions s
`+where+`
GROUP BY (s.started_at AT TIME ZONE 'Asia/Shanghai')::date
ORDER BY (s.started_at AT TIME ZONE 'Asia/Shanghai')::date
`, args...)
	if err != nil {
		return nil, err
	}
	trend := make([]service.CodexTailBurstTrendPoint, 0)
	for trendRows.Next() {
		var point service.CodexTailBurstTrendPoint
		if err := trendRows.Scan(&point.Date, &point.Sessions, &point.Requests, &point.Tokens, &point.DeltaPercent); err != nil {
			_ = trendRows.Close()
			return nil, err
		}
		trend = append(trend, point)
	}
	if err := trendRows.Err(); err != nil {
		_ = trendRows.Close()
		return nil, err
	}
	if err := trendRows.Close(); err != nil {
		return nil, err
	}

	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_tail_burst_sessions s `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	itemsArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	itemRows, err := r.db.QueryContext(ctx, `
SELECT s.session_id, s.account_id, COALESCE(s.account_name, ''), s.started_at,
       s.ended_at, COALESCE(s.quota_window, ''), COALESCE(s.reason, ''),
       s.entry_percent, s.delta_percent, s.requests, s.tokens, s.status
FROM codex_tail_burst_sessions s
`+where+`
ORDER BY s.started_at DESC, s.id DESC
LIMIT $`+itoa(len(args)+1)+` OFFSET $`+itoa(len(args)+2), itemsArgs...)
	if err != nil {
		return nil, err
	}
	items := make([]service.CodexTailBurstSessionItem, 0)
	for itemRows.Next() {
		var item service.CodexTailBurstSessionItem
		var started time.Time
		var ended sql.NullTime
		if err := itemRows.Scan(&item.SessionID, &item.AccountID, &item.AccountName, &started, &ended,
			&item.Window, &item.Reason, &item.EntryPercent, &item.DeltaPercent, &item.Requests, &item.Tokens, &item.Status); err != nil {
			_ = itemRows.Close()
			return nil, err
		}
		item.StartedAt = started.UTC().Format(time.RFC3339Nano)
		if ended.Valid {
			value := ended.Time.UTC().Format(time.RFC3339Nano)
			item.EndedAt = &value
		}
		items = append(items, item)
	}
	if err := itemRows.Err(); err != nil {
		_ = itemRows.Close()
		return nil, err
	}
	if err := itemRows.Close(); err != nil {
		return nil, err
	}
	pages := int64(1)
	if total > 0 {
		pages = int64(math.Ceil(float64(total) / float64(pageSize)))
	}
	return &service.CodexTailBurstStatsResponse{
		Summary: summary, Trend: trend, Items: items, Total: total,
		Page: page, PageSize: pageSize, Pages: int(pages),
	}, nil
}
