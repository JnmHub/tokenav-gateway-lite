package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryGetCodexTailBurstStatsGroupsTrendByShanghaiDate(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filter := service.CodexTailBurstStatsFilter{
		StartTime: start,
		EndTime:   end,
		Page:      1,
		PageSize:  20,
	}

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM codex_tail_burst_sessions s`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"sessions", "active", "settled", "accounts", "requests", "tokens", "delta"}).
			AddRow(int64(1), int64(0), int64(1), int64(1), int64(4), int64(120), 1.5))

	trendRows := sqlmock.NewRows([]string{"day", "sessions", "requests", "tokens", "delta"}).
		AddRow("2026-08-01", int64(1), int64(4), int64(120), 1.5)
	mock.ExpectQuery(`(?s)TO_CHAR\(\(s\.started_at AT TIME ZONE 'Asia/Shanghai'\)::date.*GROUP BY \(s\.started_at AT TIME ZONE 'Asia/Shanghai'\)::date.*ORDER BY \(s\.started_at AT TIME ZONE 'Asia/Shanghai'\)::date`).
		WithArgs(start, end).
		WillReturnRows(trendRows)

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM codex_tail_burst_sessions s`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))

	mock.ExpectQuery(`(?s)SELECT s\.session_id.*FROM codex_tail_burst_sessions s.*LIMIT \$3 OFFSET \$4`).
		WithArgs(start, end, 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"session_id", "account_id", "account_name", "started_at", "ended_at", "window", "reason",
			"entry_percent", "delta_percent", "requests", "tokens", "status",
		}).AddRow("session-1", int64(7), "Team", start, nil, "5h", "used_ratio", 98.0, 1.5, int64(4), int64(120), "settled"))

	result, err := repo.GetCodexTailBurstStats(context.Background(), filter)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Trend, 1)
	require.Equal(t, "2026-08-01", result.Trend[0].Date)
	require.Len(t, result.Items, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}
