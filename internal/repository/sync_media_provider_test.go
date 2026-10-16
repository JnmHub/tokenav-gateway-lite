package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProvideSyncMediaObjectStoreDisabledOutsideGatewayLite(t *testing.T) {
	for _, runMode := range []string{config.RunModeStandard, config.RunModeSimple} {
		cfg := &config.Config{
			RunMode:     runMode,
			JWT:         config.JWTConfig{Secret: "a-secret-long-enough-for-media-provider-tests"},
			GatewayLite: config.GatewayLiteConfig{MediaProxyEnabled: true},
		}
		require.Nil(t, ProvideSyncMediaObjectStore(cfg), "run mode %q", runMode)
	}
}

func TestProvideSyncMediaObjectStoreGatewayLiteRequiresStableSecret(t *testing.T) {
	cfg := &config.Config{
		RunMode:     config.RunModeGatewayLite,
		GatewayLite: config.GatewayLiteConfig{MediaProxyEnabled: true},
		Gateway: config.GatewayConfig{ImageTasks: config.ImageTaskConfig{
			Storage: config.ImageTaskStorageConfig{Type: "local", LocalPath: t.TempDir()},
		}},
	}
	require.Nil(t, ProvideSyncMediaObjectStore(cfg))
}
