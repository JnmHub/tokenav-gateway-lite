package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrCodexTailBurstHistoryUnavailable = errors.New("codex tail-burst history is unavailable")

// CodexTailBurstSessionStart is the immutable portion of a Tail-Burst session.
// The history row is keyed by SessionID so retries from multiple gateway
// goroutines cannot create duplicate sessions.
type CodexTailBurstSessionStart struct {
	SessionID    string
	AccountID    int64
	AccountName  string
	StartedAt    time.Time
	Window       string
	Reason       string
	EntryPercent float64
}

// CodexTailBurstSessionUpdate carries the current observed delta for an active
// session. Requests and token totals are finalized only when the session ends.
type CodexTailBurstSessionUpdate struct {
	SessionID    string
	DeltaPercent float64
	// The immutable fields are carried on updates as well so a collector can
	// repair a history row when the initial start write was lost during a
	// transient database outage.
	AccountID    int64
	AccountName  string
	StartedAt    time.Time
	Window       string
	Reason       string
	EntryPercent float64
}

type CodexTailBurstSessionSettlement struct {
	SessionID    string
	EndedAt      time.Time
	DeltaPercent float64
	Requests     int64
	Tokens       int64
	// These fields let settlement atomically create the history row when the
	// best-effort start write did not succeed. They are ignored on conflicts
	// with an already-settled row, preserving idempotency.
	AccountID    int64
	AccountName  string
	StartedAt    time.Time
	Window       string
	Reason       string
	EntryPercent float64
}

// CodexTailBurstHistoryWriter is optional so older integrations and unit test
// repositories can continue to run while the durable history table is rolled
// out. The SQL usage repository implements it in production.
type CodexTailBurstHistoryWriter interface {
	CreateCodexTailBurstSession(ctx context.Context, session CodexTailBurstSessionStart) error
	UpdateCodexTailBurstSession(ctx context.Context, session CodexTailBurstSessionUpdate) error
	SettleCodexTailBurstSession(ctx context.Context, session CodexTailBurstSessionSettlement) error
}

const codexTailBurstHistoryWriteTimeout = 2 * time.Second

// codexTailBurstHistoryWriter returns the optional history capability without
// widening UsageLogRepository. Older repositories (and rolling upgrades)
// simply return nil, so the core quota path keeps its existing behavior.
func codexTailBurstHistoryWriter(repo UsageLogRepository) CodexTailBurstHistoryWriter {
	if repo == nil {
		return nil
	}
	writer, _ := repo.(CodexTailBurstHistoryWriter)
	return writer
}

func codexTailBurstHistoryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	// History is observability data. Detach it from client cancellation, but keep
	// a hard bound so a slow database cannot hold the request indefinitely.
	return context.WithTimeout(context.WithoutCancel(ctx), codexTailBurstHistoryWriteTimeout)
}

func codexTailBurstSessionStartFromAccount(account *Account, extra map[string]any) (CodexTailBurstSessionStart, bool) {
	if account == nil {
		return CodexTailBurstSessionStart{}, false
	}
	marker := codexTailBurstSessionMarkerFromExtra(extra)
	if strings.TrimSpace(marker.SessionID) == "" {
		return CodexTailBurstSessionStart{}, false
	}
	startedAt, err := parseTime(marker.Since)
	if err != nil || startedAt.IsZero() {
		return CodexTailBurstSessionStart{}, false
	}
	entry := 0.0
	if marker.EntryPercent != nil {
		entry = *marker.EntryPercent
	}
	return CodexTailBurstSessionStart{
		SessionID: marker.SessionID, AccountID: account.ID, AccountName: account.Name,
		StartedAt: startedAt, Window: marker.Window, Reason: marker.Reason,
		EntryPercent: entry,
	}, true
}

// recordCodexTailBurstHistoryStart is intentionally best effort. A history
// outage must never turn a quota bypass into a failed customer request.
func recordCodexTailBurstHistoryStart(ctx context.Context, repo UsageLogRepository, account *Account, extra map[string]any) {
	writer := codexTailBurstHistoryWriter(repo)
	if writer == nil {
		return
	}
	session, ok := codexTailBurstSessionStartFromAccount(account, extra)
	if !ok {
		return
	}
	writeCtx, cancel := codexTailBurstHistoryContext(ctx)
	defer cancel()
	_ = writer.CreateCodexTailBurstSession(writeCtx, session)
}

func recordCodexTailBurstHistoryUpdate(ctx context.Context, repo UsageLogRepository, account *Account, extra map[string]any, delta float64) {
	writer := codexTailBurstHistoryWriter(repo)
	if writer == nil || account == nil {
		return
	}
	start, ok := codexTailBurstSessionStartFromAccount(account, extra)
	if !ok {
		return
	}
	writeCtx, cancel := codexTailBurstHistoryContext(ctx)
	defer cancel()
	_ = writer.UpdateCodexTailBurstSession(writeCtx, CodexTailBurstSessionUpdate{
		SessionID: start.SessionID, DeltaPercent: delta,
		AccountID: start.AccountID, AccountName: start.AccountName,
		StartedAt: start.StartedAt, Window: start.Window, Reason: start.Reason,
		EntryPercent: start.EntryPercent,
	})
}

func recordCodexTailBurstHistorySettlement(ctx context.Context, repo UsageLogRepository, account *Account, extra map[string]any, endedAt time.Time, delta float64, requests, tokens int64) {
	writer := codexTailBurstHistoryWriter(repo)
	if writer == nil || account == nil {
		return
	}
	start, ok := codexTailBurstSessionStartFromAccount(account, extra)
	if !ok || endedAt.IsZero() {
		return
	}
	writeCtx, cancel := codexTailBurstHistoryContext(ctx)
	defer cancel()
	_ = writer.SettleCodexTailBurstSession(writeCtx, CodexTailBurstSessionSettlement{
		SessionID: start.SessionID, EndedAt: endedAt,
		DeltaPercent: delta, Requests: requests, Tokens: tokens,
		AccountID: start.AccountID, AccountName: start.AccountName,
		StartedAt: start.StartedAt, Window: start.Window, Reason: start.Reason,
		EntryPercent: start.EntryPercent,
	})
}

// CodexTailBurstHistoryReader is implemented by the Ops repository used by the
// administrator API. Keeping reads separate from the hot-path writer avoids
// widening existing repository test doubles and constructors.
type CodexTailBurstHistoryReader interface {
	GetCodexTailBurstStats(ctx context.Context, filter CodexTailBurstStatsFilter) (*CodexTailBurstStatsResponse, error)
}

type CodexTailBurstStatsFilter struct {
	StartTime time.Time
	EndTime   time.Time
	AccountID *int64
	Window    string
	Reason    string
	Status    string
	Page      int
	PageSize  int
}

type CodexTailBurstStatsSummary struct {
	Sessions        int64   `json:"sessions"`
	ActiveSessions  int64   `json:"active_sessions"`
	SettledSessions int64   `json:"settled_sessions"`
	Accounts        int64   `json:"accounts"`
	Requests        int64   `json:"requests"`
	Tokens          int64   `json:"tokens"`
	DeltaPercent    float64 `json:"delta_percent"`
}

type CodexTailBurstTrendPoint struct {
	Date         string  `json:"date"`
	Sessions     int64   `json:"sessions"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	DeltaPercent float64 `json:"delta_percent"`
}

type CodexTailBurstSessionItem struct {
	SessionID    string  `json:"session_id"`
	AccountID    int64   `json:"account_id"`
	AccountName  string  `json:"account_name"`
	StartedAt    string  `json:"started_at"`
	EndedAt      *string `json:"ended_at,omitempty"`
	Window       string  `json:"window,omitempty"`
	Reason       string  `json:"reason,omitempty"`
	EntryPercent float64 `json:"entry_percent"`
	DeltaPercent float64 `json:"delta_percent"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Status       string  `json:"status"`
}

type CodexTailBurstStatsResponse struct {
	Summary  CodexTailBurstStatsSummary  `json:"summary"`
	Trend    []CodexTailBurstTrendPoint  `json:"trend"`
	Items    []CodexTailBurstSessionItem `json:"items"`
	Total    int64                       `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"page_size"`
	Pages    int                         `json:"pages"`
}
