package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestGetAccountUsageStatsGroupsByShanghaiBusinessDay(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	start := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	mock.ExpectQuery(`(?s)TO_CHAR\(created_at AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD'\).*WHERE account_id = \$1`).
		WithArgs(int64(7), start, end).
		WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "tokens", "cost", "actual_cost", "user_cost"}).
			AddRow("2026-08-03", int64(2), int64(300), 1.2, 1.1, 1.3))
	mock.ExpectQuery(`SELECT COALESCE\(AVG\(duration_ms\), 0\).*WHERE account_id = \$1`).
		WithArgs(int64(7), start, end).
		WillReturnRows(sqlmock.NewRows([]string{"avg_duration_ms"}).AddRow(250.0))

	repo := newUsageLogRepositoryWithSQL(nil, db)
	stats, err := repo.GetAccountUsageStats(context.Background(), 7, start, end)
	require.NoError(t, err)
	require.Len(t, stats.History, 1)
	require.Equal(t, "2026-08-03", stats.History[0].Date)
	require.Equal(t, 250.0, stats.Summary.AvgDurationMs)
	require.NoError(t, mock.ExpectationsWereMet())
}
