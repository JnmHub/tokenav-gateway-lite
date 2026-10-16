package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAccountSchedulable(t *testing.T) {
	require.True(t, normalizeAccountSchedulable(service.StatusActive, true))
	require.False(t, normalizeAccountSchedulable(service.StatusActive, false))
	require.False(t, normalizeAccountSchedulable(service.StatusDisabled, true))
	require.False(t, normalizeAccountSchedulable(service.StatusError, true))
	require.False(t, normalizeAccountSchedulable("inactive", true))
}

func TestAccountRepository_BulkUpdateNonActiveStatusForcesUnschedulableSQL(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer db.Close()

	status := service.StatusDisabled
	schedulable := true
	mock.ExpectExec(`UPDATE accounts SET status = \$1, schedulable = FALSE, updated_at = NOW\(\) WHERE id = ANY\(\$2\) AND deleted_at IS NULL`).
		WithArgs(status, pq.Array([]int64{101, 102})).
		WillReturnResult(sqlmock.NewResult(0, 0))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	rows, err := repo.BulkUpdate(context.Background(), []int64{101, 102}, service.AccountBulkUpdate{
		Status:      &status,
		Schedulable: &schedulable,
	})

	require.NoError(t, err)
	require.Equal(t, int64(0), rows)
	require.NoError(t, mock.ExpectationsWereMet())
}
