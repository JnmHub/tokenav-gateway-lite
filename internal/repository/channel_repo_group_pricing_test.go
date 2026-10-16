//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestReplaceGroupPricingClosesSharedChannelByDetachingOnlyCurrentGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM groups`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`SELECT channel_id FROM channel_groups`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"channel_id"}).AddRow(int64(41)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM channel_groups`).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`DELETE FROM channel_groups`).WithArgs(int64(41), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	channelID, err := repo.ReplaceGroupPricing(context.Background(), 7, nil)
	require.NoError(t, err)
	require.Zero(t, channelID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReplaceGroupPricingSplitsSharedChannelAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}
	price := 0.02
	channel := &service.Channel{
		Name: "group-pricing-7", Status: service.StatusActive,
		BillingModelSource: service.BillingModelSourceRequested,
		ModelPricing: []service.ChannelModelPricing{{
			Platform: service.PlatformOpenAI, Models: []string{"gpt-test"},
			BillingMode: service.BillingModePerRequest, PerRequestPrice: &price,
		}},
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM groups`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`SELECT channel_id FROM channel_groups`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"channel_id"}).AddRow(int64(41)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM channel_groups`).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`DELETE FROM channel_groups`).WithArgs(int64(41), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO channels`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(99)))
	mock.ExpectExec(`INSERT INTO channel_groups`).WithArgs(int64(99), int64(7)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`DELETE FROM channel_model_pricing`).WithArgs(int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`INSERT INTO channel_model_pricing`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(int64(501), time.Now(), time.Now()))
	mock.ExpectExec(`DELETE FROM channel_account_stats_pricing_rules`).WithArgs(int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	channelID, err := repo.ReplaceGroupPricing(context.Background(), 7, channel)
	require.NoError(t, err)
	require.Equal(t, int64(99), channelID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReplaceGroupPricingRollsBackSharedDetachWhenCreateFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &channelRepository{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM groups`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(`SELECT channel_id FROM channel_groups`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"channel_id"}).AddRow(int64(41)))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM channel_groups`).WithArgs(int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(`DELETE FROM channel_groups`).WithArgs(int64(41), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO channels`).WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()

	_, err = repo.ReplaceGroupPricing(context.Background(), 7, &service.Channel{
		Name: "group-pricing-7", Status: service.StatusActive,
	})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
