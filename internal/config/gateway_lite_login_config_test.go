package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayLiteAdminLoginPathNormalizationAndValidation(t *testing.T) {
	require.Equal(t, DefaultGatewayLiteAdminLoginPath, NormalizeGatewayLiteAdminLoginPath(""))
	require.Equal(t, "/private_console", NormalizeGatewayLiteAdminLoginPath("private_console/"))
	require.NoError(t, ValidateGatewayLiteAdminLoginPath("/private-console-2026"))
	require.Error(t, ValidateGatewayLiteAdminLoginPath("/login"))
	require.Error(t, ValidateGatewayLiteAdminLoginPath("/admin"))
	require.Error(t, ValidateGatewayLiteAdminLoginPath("/two/segments"))
	require.Error(t, ValidateGatewayLiteAdminLoginPath("/ab"))
}

func TestGatewayLiteLoginPresentationDefaults(t *testing.T) {
	require.Equal(t, DefaultGatewayLiteLoginTitle, NormalizeGatewayLiteLoginTitle(""))
	require.Equal(t, DefaultGatewayLiteLoginSubtitle, NormalizeGatewayLiteLoginSubtitle("  "))
	require.Equal(t, "Private Console", NormalizeGatewayLiteLoginTitle(" Private Console "))
}
