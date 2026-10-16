package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeriveOperationResourceTypeUsesStableManagementCategories(t *testing.T) {
	tests := map[string]string{
		"/api/v1/admin/accounts/:id":                  "account",
		"/api/v1/admin/groups/:id":                    "group",
		"/api/v1/admin/proxies/:id":                   "proxy",
		"/api/v1/admin/settings/security":             "security_setting",
		"/api/v1/admin/ops/runtime/server-timing":     "runtime_config",
		"/api/v1/admin/content-review/keyword-policy": "security_policy",
	}
	for path, expected := range tests {
		require.Equal(t, expected, deriveOperationResourceType(path), path)
	}
}
