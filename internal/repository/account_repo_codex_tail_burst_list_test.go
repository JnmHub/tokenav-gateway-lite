package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestListByPlatformForCodexTailBurstFiltersUnrelatedHistoricalAccounts(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureSchedulableProjectionMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)

	mock.ExpectQuery("tail-burst account listing").WillReturnRows(sqlmock.NewRows([]string{
		"id", "created_at", "updated_at", "deleted_at", "name", "notes", "platform", "type",
		"credentials", "extra", "proxy_id", "proxy_fallback_origin_id", "concurrency", "load_factor",
		"priority", "rate_multiplier", "status", "error_message", "last_used_at", "expires_at",
		"auto_pause_on_expired", "schedulable", "rate_limited_at", "rate_limit_reset_at", "overload_until",
		"temp_unschedulable_until", "temp_unschedulable_reason", "session_window_start", "session_window_end",
		"session_window_status", "parent_account_id", "quota_dimension",
	}))

	accounts, err := repo.ListByPlatformForCodexTailBurst(context.Background(), service.PlatformOpenAI)
	require.NoError(t, err)
	require.Empty(t, accounts)
	require.NoError(t, mock.ExpectationsWereMet())

	normalized := strings.Join(strings.Fields(capturedSQL), " ")
	require.Contains(t, normalized, "platform")
	require.Contains(t, normalized, "deleted_at")
	require.Contains(t, normalized, "status")
	require.Contains(t, normalized, "codex_tail_burst_active")
	require.Contains(t, normalized, "IN ('true', '1')")
	require.Contains(t, normalized, "OR")
}
