//go:build unit

package middleware

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupDuplicateHasStableOperationAuditContract(t *testing.T) {
	spec, ok := auditRouteSpecs["POST /api/v1/admin/groups/:id/duplicate"]
	require.True(t, ok)
	require.Equal(t, "admin.groups.duplicate", spec.Action)
	require.Equal(t, "group", spec.ResourceType)
	require.False(t, spec.Critical, "停用副本由事务和幂等保护，不应强制 step-up")
}

func TestChannelMonitorDuplicateHasStableOperationAuditContract(t *testing.T) {
	spec, ok := auditRouteSpecs["POST /api/v1/admin/channel-monitors/:id/duplicate"]
	require.True(t, ok)
	require.Equal(t, "admin.channel_monitors.duplicate", spec.Action)
	require.Equal(t, "channel_monitor", spec.ResourceType)
	require.False(t, spec.Critical, "副本默认停用，且浏览器不接触密钥明文")
}
