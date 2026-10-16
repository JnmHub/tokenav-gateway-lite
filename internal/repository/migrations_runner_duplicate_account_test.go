package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestPrepareAccountsDuplicateOperationIDUniqueMigrationRejectsDuplicates(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)WITH active_operations AS .*duplicate_operation_id.*HAVING COUNT\(\*\) > 1`).
		WillReturnRows(sqlmock.NewRows([]string{"operation_prefix", "duplicate_count", "account_ids"}).
			AddRow("0123456789abcdef", 2, "41,42"))

	err = prepareAccountsDuplicateOperationIDUniqueMigration(context.Background(), db)

	require.Error(t, err)
	require.Contains(t, err.Error(), accountsDuplicateOperationIDUniqueMigration)
	require.Contains(t, err.Error(), "account_ids=41,42")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareAccountsDuplicateOperationIDUniqueMigrationDropsStaleInvalidIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)WITH active_operations AS .*duplicate_operation_id.*HAVING COUNT\(\*\) > 1`).
		WillReturnRows(sqlmock.NewRows([]string{"operation_prefix", "duplicate_count", "account_ids"}))
	mock.ExpectQuery(`(?s)SELECT i\.indisvalid,.*pg_get_expr.*idx\.relname = \$1`).
		WithArgs(accountsDuplicateOperationIDUniqueIndex).
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "indnkeyatts", "indexprs", "indpred"}).
			AddRow(false, true, 1, `(extra ->> 'duplicate_operation_id'::text)`, `(deleted_at IS NULL)`))
	mock.ExpectExec(`DROP INDEX CONCURRENTLY IF EXISTS idx_accounts_duplicate_operation_id_unique`).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, prepareAccountsDuplicateOperationIDUniqueMigration(context.Background(), db))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareAccountsDuplicateOperationIDUniqueMigrationRejectsWrongValidIndexDefinition(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)WITH active_operations AS .*duplicate_operation_id.*HAVING COUNT\(\*\) > 1`).
		WillReturnRows(sqlmock.NewRows([]string{"operation_prefix", "duplicate_count", "account_ids"}))
	mock.ExpectQuery(`(?s)SELECT i\.indisvalid,.*pg_get_expr.*idx\.relname = \$1`).
		WithArgs(accountsDuplicateOperationIDUniqueIndex).
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "indnkeyatts", "indexprs", "indpred"}).
			AddRow(true, false, 1, `(extra ->> 'duplicate_operation_id'::text)`, `(deleted_at IS NULL)`))

	err = prepareAccountsDuplicateOperationIDUniqueMigration(context.Background(), db)
	require.ErrorContains(t, err, "unexpected definition")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareAccountsDuplicateOperationIDUniqueMigrationRejectsCompositeUniqueIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)WITH active_operations AS .*duplicate_operation_id.*HAVING COUNT\(\*\) > 1`).
		WillReturnRows(sqlmock.NewRows([]string{"operation_prefix", "duplicate_count", "account_ids"}))
	mock.ExpectQuery(`(?s)SELECT i\.indisvalid,.*pg_get_expr.*idx\.relname = \$1`).
		WithArgs(accountsDuplicateOperationIDUniqueIndex).
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "indnkeyatts", "indexprs", "indpred"}).
			AddRow(
				true,
				true,
				2,
				`(extra ->> 'duplicate_operation_id'::text)`,
				`((deleted_at IS NULL) AND (NULLIF(btrim((extra ->> 'duplicate_operation_id'::text)), ''::text) IS NOT NULL))`,
			))

	err = prepareAccountsDuplicateOperationIDUniqueMigration(context.Background(), db)
	require.ErrorContains(t, err, "unexpected definition")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareAccountsDuplicateOperationIDUniqueMigrationAcceptsExpectedValidIndex(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)WITH active_operations AS .*duplicate_operation_id.*HAVING COUNT\(\*\) > 1`).
		WillReturnRows(sqlmock.NewRows([]string{"operation_prefix", "duplicate_count", "account_ids"}))
	mock.ExpectQuery(`(?s)SELECT i\.indisvalid,.*pg_get_expr.*idx\.relname = \$1`).
		WithArgs(accountsDuplicateOperationIDUniqueIndex).
		WillReturnRows(sqlmock.NewRows([]string{"indisvalid", "indisunique", "indnkeyatts", "indexprs", "indpred"}).
			AddRow(
				true,
				true,
				1,
				`(extra ->> 'duplicate_operation_id'::text)`,
				`((deleted_at IS NULL) AND (NULLIF(btrim((extra ->> 'duplicate_operation_id'::text)), ''::text) IS NOT NULL))`,
			))

	require.NoError(t, prepareAccountsDuplicateOperationIDUniqueMigration(context.Background(), db))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPrepareAccountsDuplicateOperationIDUniqueMigrationPropagatesPrecheckFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)WITH active_operations AS .*duplicate_operation_id`).
		WillReturnError(errors.New("database unavailable"))

	err = prepareAccountsDuplicateOperationIDUniqueMigration(context.Background(), db)
	require.ErrorContains(t, err, "precheck duplicate account operation ids")
	require.NoError(t, mock.ExpectationsWereMet())
}
