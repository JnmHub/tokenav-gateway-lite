//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestV169MigrationsCanReapplyAfterRollbackRehearsal verifies the exact two
// additive gateway migrations introduced in this release. The test removes the
// schema and migration ledger entries on the disposable integration database,
// reapplies the real embedded migration set, then verifies a repeated run.
func TestV169MigrationsCanReapplyAfterRollbackRehearsal(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `
ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_peak_rate_multiplier_valid;
ALTER TABLE groups
  DROP COLUMN IF EXISTS peak_rate_enabled,
  DROP COLUMN IF EXISTS peak_start,
  DROP COLUMN IF EXISTS peak_end,
  DROP COLUMN IF EXISTS peak_rate_multiplier,
  DROP COLUMN IF EXISTS peak_timezone;
ALTER TABLE usage_logs DROP COLUMN IF EXISTS rate_policy_audit;
DELETE FROM schema_migrations
WHERE filename IN ('186_group_peak_rate_policy.sql', '187_usage_log_rate_policy_audit.sql');
`)
	require.NoError(t, err)

	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	require.NoError(t, ApplyMigrations(ctx, integrationDB), "repeated migration run must stay idempotent")

	tx := testTx(t)
	requireColumn(t, tx, "groups", "peak_rate_enabled", "boolean", 0, false)
	requireColumn(t, tx, "groups", "peak_start", "character varying", 5, false)
	requireColumn(t, tx, "groups", "peak_end", "character varying", 5, false)
	requireColumn(t, tx, "groups", "peak_rate_multiplier", "numeric", 0, false)
	requireColumn(t, tx, "groups", "peak_timezone", "character varying", 64, false)
	requireConstraintDefinitionContains(t, tx, "groups", "groups_peak_rate_multiplier_valid", "peak_rate_multiplier", "100")
	requireColumn(t, tx, "usage_logs", "rate_policy_audit", "jsonb", 0, true)

	var applied int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM schema_migrations
WHERE filename IN ('186_group_peak_rate_policy.sql', '187_usage_log_rate_policy_audit.sql')
`).Scan(&applied))
	require.Equal(t, 2, applied)
}
