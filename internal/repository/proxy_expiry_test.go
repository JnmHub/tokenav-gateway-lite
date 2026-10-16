package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSortedUniqueAccountIDs(t *testing.T) {
	tests := []struct {
		name  string
		input []int64
		want  []int64
	}{
		{name: "unsorted duplicates", input: []int64{12, 3, 12, 8, 3}, want: []int64{3, 8, 12}},
		{name: "already sorted", input: []int64{3, 8, 12}, want: []int64{3, 8, 12}},
		{name: "single", input: []int64{3}, want: []int64{3}},
		{name: "empty", input: []int64{}, want: []int64{}},
		{name: "nil", input: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, sortedUniqueAccountIDs(tt.input))
		})
	}
}

func TestSweepOneExpiredProxyOnExecReturnsAffectedAccountIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	proxyID := int64(7)
	targetID := int64(9)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE proxies SET status=$1, updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL")).
		WithArgs(service.StatusExpired, proxyID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)UPDATE accounts SET proxy_id=\$2.*RETURNING id`).
		WithArgs(proxyID, targetID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(21)).AddRow(int64(34)))

	repo := newProxyRepositoryWithSQL(nil, db)
	accountIDs, err := repo.sweepOneExpiredProxyOnExec(context.Background(), db, proxyID, &targetID, true)

	require.NoError(t, err)
	require.Equal(t, []int64{21, 34}, accountIDs)
	require.NoError(t, mock.ExpectationsWereMet())
}
