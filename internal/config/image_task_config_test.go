package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDegradesInvalidEnabledImageTasksWithoutTakingGatewayDown(t *testing.T) {
	resetViperWithEmptyConfigFile(t)
	t.Setenv("GATEWAY_IMAGE_TASKS_ENABLED", "true")
	// An empty environment value deliberately leaves the generated startup key
	// marked as ephemeral. The optional image subsystem must fail closed while
	// the rest of gateway-lite still loads normally.
	t.Setenv("TOTP_ENCRYPTION_KEY", "")

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Gateway.ImageTasks.Enabled,
		"the desired setting must remain visible so the admin can repair it")
	require.Contains(t, cfg.Gateway.ImageTasks.DegradedReason, "totp.encryption_key")
	require.Equal(t, int64(256*1024*1024), cfg.Gateway.MaxBodySize,
		"an image-only configuration error must not replace unrelated gateway settings")
}

func TestLoadKeepsValidEnabledLocalImageTasks(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("GATEWAY_IMAGE_TASKS_ENABLED", "true")
	t.Setenv("GATEWAY_IMAGE_TASKS_STORAGE_TYPE", "local")
	t.Setenv("GATEWAY_IMAGE_TASKS_STORAGE_LOCAL_PATH", t.TempDir())
	t.Setenv("TOTP_ENCRYPTION_KEY", strings.Repeat("k", 64))

	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Gateway.ImageTasks.Enabled)
	require.Empty(t, cfg.Gateway.ImageTasks.DegradedReason)
	require.NoError(t, cfg.ValidateImageTasks())
}

func TestLoadUsesSub2APIConfigFileForImageTaskAdminWrites(t *testing.T) {
	resetViperWithJWTSecret(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`gateway:
  image_tasks:
    worker_count: 7
`), 0o600))
	t.Setenv("SUB2API_CONFIG_FILE", path)

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, 7, cfg.Gateway.ImageTasks.WorkerCount)
}
