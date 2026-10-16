package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteConfigResponseIncludesLegacySnapshotAuthority(t *testing.T) {
	h := NewGatewayLiteConfigHandler(&config.Config{GatewayLite: config.GatewayLiteConfig{
		Region:                           "jp",
		GatewayCode:                      "jp-1",
		AdminLoginPath:                   "/private-jp-console",
		LoginTitle:                       "JP Gateway",
		LoginSubtitle:                    "Authorized operators only",
		LegacySnapshotSchedulerAuthority: true,
	}})

	resp := h.responseFromConfig(h.effectiveConfig(), "/tmp/config.yaml", false)

	require.True(t, resp.LegacySnapshotSchedulerAuthority)
	require.Equal(t, "jp", resp.Region)
	require.Equal(t, "jp-1", resp.GatewayCode)
	require.Equal(t, "/private-jp-console", resp.AdminLoginPath)
	require.Equal(t, "JP Gateway", resp.LoginTitle)
	require.Equal(t, "Authorized operators only", resp.LoginSubtitle)
}

func TestGatewayLiteConfigUpdateRequestLegacySnapshotAuthorityPointer(t *testing.T) {
	var req gatewayLiteConfigUpdateRequest

	require.Nil(t, req.LegacySnapshotSchedulerAuthority)

	enabled := true
	req.LegacySnapshotSchedulerAuthority = &enabled
	require.NotNil(t, req.LegacySnapshotSchedulerAuthority)
	require.True(t, *req.LegacySnapshotSchedulerAuthority)
}
