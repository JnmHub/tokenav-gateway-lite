package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountSparkShadowMigrationEnforcesRelationshipAndOnlineUniqueness(t *testing.T) {
	schemaContent, err := FS.ReadFile("188_account_spark_shadow.sql")
	require.NoError(t, err)
	schemaSQL := strings.Join(strings.Fields(string(schemaContent)), " ")
	require.Contains(t, schemaSQL, "CHECK (quota_dimension IN ('global','spark'))")
	require.Contains(t, schemaSQL, "parent_account_id IS NULL AND quota_dimension = 'global'")
	require.Contains(t, schemaSQL, "parent_account_id IS NOT NULL AND quota_dimension = 'spark'")
	require.Contains(t, schemaSQL, "FOREIGN KEY (parent_account_id) REFERENCES accounts(id) ON DELETE RESTRICT")

	indexContent, err := FS.ReadFile("188a_account_spark_shadow_indexes_notx.sql")
	require.NoError(t, err)
	indexSQL := strings.Join(strings.Fields(string(indexContent)), " ")
	require.Contains(t, indexSQL, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_parent_account_id")
	require.Contains(t, indexSQL, "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_accounts_spark_shadow_per_parent")
	require.Contains(t, indexSQL, "WHERE parent_account_id IS NOT NULL AND quota_dimension = 'spark' AND deleted_at IS NULL")
}
