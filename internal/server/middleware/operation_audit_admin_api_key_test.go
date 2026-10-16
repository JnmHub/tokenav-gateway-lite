package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminAPIKeyRevealIsCriticalAndValuePoorAudit(t *testing.T) {
	spec, ok := auditRouteSpecs["POST /api/v1/admin/settings/admin-api-key/reveal"]
	require.True(t, ok)
	require.True(t, spec.Critical)
	require.Equal(t, "admin.admin_api_key.reveal", spec.Action)
	require.Equal(t, "admin_api_key", spec.ResourceType)
}

func TestGatewayLiteLocalAPIKeyRevealIsCriticalAndValuePoorAudit(t *testing.T) {
	spec, ok := auditRouteSpecs["POST /api/v1/admin/gateway-lite/local-api-keys/:id/reveal"]
	require.True(t, ok)
	require.True(t, spec.Critical)
	require.Equal(t, "admin.gateway_lite_local_api_key.reveal", spec.Action)
	require.Equal(t, "gateway_lite_local_api_key", spec.ResourceType)
}
