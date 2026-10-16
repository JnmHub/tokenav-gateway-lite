package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorBusinessDatesUseShanghaiTimezone(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	date := time.Date(2026, 8, 2, 16, 0, 0, 0, time.UTC)
	repo := &channelMonitorRepository{db: db}

	mock.ExpectExec(`(?s)INSERT INTO channel_monitor_daily_rollups.*AT TIME ZONE 'Asia/Shanghai'.*checked_at >= .*AT TIME ZONE 'Asia/Shanghai'`).
		WithArgs(date).
		WillReturnResult(sqlmock.NewResult(0, 3))
	affected, err := repo.UpsertDailyRollupsFor(context.Background(), date)
	require.NoError(t, err)
	require.EqualValues(t, 3, affected)

	mock.ExpectExec(`(?s)INSERT INTO channel_monitor_aggregation_watermark.*AT TIME ZONE 'Asia/Shanghai'`).
		WithArgs(date).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.UpdateAggregationWatermark(context.Background(), date))

	mock.ExpectExec(`(?s)WITH batch.*AT TIME ZONE 'Asia/Shanghai'.*DELETE FROM channel_monitor_daily_rollups`).
		WithArgs(date, channelMonitorPruneBatchSize).
		WillReturnResult(sqlmock.NewResult(0, 0))
	deleted, err := repo.DeleteRollupsBefore(context.Background(), date)
	require.NoError(t, err)
	require.Zero(t, deleted)
	require.NoError(t, mock.ExpectationsWereMet())
}
