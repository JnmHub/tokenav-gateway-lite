package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountsDuplicateOperationIDUsesOnlinePartialUniqueIndex(t *testing.T) {
	content, err := FS.ReadFile("165_accounts_duplicate_operation_id_unique_notx.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_duplicate_operation_id_unique")
	require.Contains(t, sql, "ON accounts ((extra ->> 'duplicate_operation_id'))")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")
	require.Contains(t, sql, "NULLIF(btrim(extra ->> 'duplicate_operation_id'), '') IS NOT NULL")
}
