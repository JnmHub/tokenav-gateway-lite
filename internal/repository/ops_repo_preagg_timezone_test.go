package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestUpsertDailyMetricsGroupsBucketsByShanghaiDate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer db.Close()

	start := time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	mock.ExpectExec(`(?s)INSERT INTO ops_metrics_daily .*AT TIME ZONE 'Asia/Shanghai'.*`).
		WithArgs(start, end).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := &opsRepository{db: db}
	require.NoError(t, repo.UpsertDailyMetrics(context.Background(), start, end))
	require.NoError(t, mock.ExpectationsWereMet())
}
