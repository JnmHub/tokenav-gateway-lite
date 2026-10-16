package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserPlatformQuotasCNProvidersMigration(t *testing.T) {
	t.Parallel()

	content, err := FS.ReadFile("226_user_platform_quotas_add_cn_providers.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check")
	require.Contains(t, sql, "'kimi', 'zhipu', 'deepseek'")
	require.Contains(t, sql, "NOT VALID")
	require.Contains(t, sql, "VALIDATE CONSTRAINT user_platform_quotas_platform_check")
}
