package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOperationAuditRepositoryPreviewClear(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	oldest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM operation_audit_logs")).
		WillReturnRows(sqlmock.NewRows([]string{"count", "min", "max"}).AddRow(12, oldest, newest))

	repo, ok := NewOperationAuditRepository(db).(service.OperationAuditPreviewRepository)
	require.True(t, ok)
	preview, err := repo.PreviewClear(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(12), preview.Rows)
	require.Equal(t, oldest, *preview.OldestAt)
	require.Equal(t, newest, *preview.NewestAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
