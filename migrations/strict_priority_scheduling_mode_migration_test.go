package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrictPrioritySchedulingModeMigrationAllowsNewMode(t *testing.T) {
	content, err := FS.ReadFile("221_group_strict_priority_scheduling_mode.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS groups_scheduling_mode_check")
	require.Contains(t, sql, "CHECK (scheduling_mode IN ('default', 'random', 'strict_priority'))")
}
