package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepository_MarkCodexTailBurstStartedIfInactiveUsesAtomicGuard(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	updates := map[string]any{
		"codex_tail_burst_active":        true,
		"codex_tail_burst_since":         "2026-08-30T01:02:03Z",
		"codex_tail_burst_window":        "5h",
		"codex_tail_burst_reason":        "used_ratio",
		"codex_tail_burst_entry_percent": 98.5,
	}

	applied, err := repo.MarkCodexTailBurstStartedIfInactive(context.Background(), 42, updates)
	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "UPDATE accounts AS a")
	require.Contains(t, normalized, "(COALESCE(a.extra, '{}'::jsonb) - 'codex_tail_burst_entry_reset_at') || $1::jsonb")
	require.Contains(t, normalized, "a.deleted_at IS NULL")
	require.Contains(t, normalized, "a.status = 'active'")
	require.Contains(t, normalized, "a.platform = 'openai'")
	require.Contains(t, normalized, "a.type = 'oauth'")
	require.Contains(t, normalized, "a.parent_account_id IS NULL")
	require.Contains(t, normalized, "btrim(COALESCE(a.credentials->>'chatgpt_account_id', '')) <> ''")
	require.Contains(t, normalized, "COALESCE(lower(btrim(a.extra->>'codex_tail_burst_disabled')), '') NOT IN ('true', '1')")
	require.Contains(t, normalized, "codex_tail_burst_active")
	require.Contains(t, normalized, "NOT IN ('true', '1')")
	require.Equal(t, int64(42), exec.execArgs[0][1])
}

func TestAccountRepository_MarkCodexTailBurstStartedIfInactiveReportsRaceLoss(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	applied, err := repo.MarkCodexTailBurstStartedIfInactive(context.Background(), 42, map[string]any{
		"codex_tail_burst_active": true,
	})
	require.NoError(t, err)
	require.False(t, applied)
}

func TestAccountRepository_SettleCodexTailBurstIfMarkerMatchesUsesAllMarkerFields(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	entryPercent := 98.5
	resetAt := "2026-08-30T02:00:00Z"
	marker := service.CodexTailBurstSessionMarker{
		SessionID:    "2b5f3b7a-13e0-4ce4-8e09-4d4bbf0e8a0c",
		Since:        "2026-08-30T01:02:03Z",
		Window:       "5h",
		Reason:       "used_ratio",
		EntryPercent: &entryPercent,
		EntryResetAt: &resetAt,
	}

	applied, err := repo.SettleCodexTailBurstIfMarkerMatches(context.Background(), 42, marker, map[string]any{
		"codex_tail_burst_active":     false,
		"codex_tail_burst_last_until": "2026-08-30T03:00:00Z",
	})
	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "UPDATE accounts AS a")
	require.Contains(t, normalized, "COALESCE(lower(btrim(a.extra->>'codex_tail_burst_active')), '') IN ('true', '1')")
	require.Contains(t, normalized, "codex_tail_burst_since")
	require.Contains(t, normalized, "codex_tail_burst_window")
	require.Contains(t, normalized, "codex_tail_burst_reason")
	require.Contains(t, normalized, "codex_tail_burst_session_id")
	require.Contains(t, normalized, "entry_percent")
	require.Contains(t, normalized, "entry_reset_at")
	require.Contains(t, normalized, "IS NOT DISTINCT FROM $9::text")
	require.Equal(t, int64(42), exec.execArgs[0][1])
	require.Equal(t, marker.SessionID, exec.execArgs[0][2])
	require.Equal(t, marker.Since, exec.execArgs[0][3])
	require.Equal(t, marker.Window, exec.execArgs[0][4])
	require.Equal(t, marker.Reason, exec.execArgs[0][5])
	require.Equal(t, true, exec.execArgs[0][6])
	require.Equal(t, entryPercent, exec.execArgs[0][7])
	require.Equal(t, resetAt, exec.execArgs[0][8])
}

func TestAccountRepository_UpdateCodexTailBurstDeltaIfMarkerMatchesUsesAtomicGuard(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	entryPercent := 98.5
	resetAt := "2026-08-30T02:00:00Z"
	marker := service.CodexTailBurstSessionMarker{
		SessionID:    "session-delta",
		Since:        "2026-08-30T01:02:03Z",
		Window:       "5h",
		Reason:       "used_ratio",
		EntryPercent: &entryPercent,
		EntryResetAt: &resetAt,
	}

	applied, err := repo.UpdateCodexTailBurstDeltaIfMarkerMatches(context.Background(), 42, marker, map[string]any{
		"codex_tail_burst_delta_percent": 1.2,
	})
	require.NoError(t, err)
	require.True(t, applied)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "UPDATE accounts AS a")
	require.Contains(t, normalized, "COALESCE(lower(btrim(a.extra->>'codex_tail_burst_active')), '') IN ('true', '1')")
	require.Contains(t, normalized, "codex_tail_burst_session_id")
	require.Contains(t, normalized, "entry_percent")
	require.Contains(t, normalized, "entry_reset_at")
	require.Equal(t, int64(42), exec.execArgs[0][1])
	require.Equal(t, marker.SessionID, exec.execArgs[0][2])
}

func TestAccountRepository_UpdateCodexTailBurstDeltaIfMarkerMatchesReportsRaceLoss(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	applied, err := repo.UpdateCodexTailBurstDeltaIfMarkerMatches(context.Background(), 42, service.CodexTailBurstSessionMarker{
		SessionID: "stale-session",
	}, map[string]any{"codex_tail_burst_delta_percent": 9.9})
	require.NoError(t, err)
	require.False(t, applied)
}

func TestAccountRepository_SettleCodexTailBurstIfMarkerMatchesLegacyMarkerUsesFallbackFields(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	applied, err := repo.SettleCodexTailBurstIfMarkerMatches(context.Background(), 42, service.CodexTailBurstSessionMarker{
		Since:  "2026-08-30T01:02:03Z",
		Window: "5h",
		Reason: "used_ratio",
	}, map[string]any{"codex_tail_burst_active": false})
	require.NoError(t, err)
	require.True(t, applied)
	require.Contains(t, normalizeSQLWhitespace(exec.execQueries[0]), "COALESCE(a.extra->>'codex_tail_burst_session_id', '') = ''")
	require.Equal(t, "", exec.execArgs[0][2])
}

func TestAccountRepository_SettleCodexTailBurstIfMarkerMatchesReportsMismatch(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)

	applied, err := repo.SettleCodexTailBurstIfMarkerMatches(context.Background(), 42, service.CodexTailBurstSessionMarker{
		Since:  "old",
		Window: "5h",
		Reason: "used_ratio",
	}, map[string]any{"codex_tail_burst_active": false})
	require.NoError(t, err)
	require.False(t, applied)
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}
