package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	imageTaskTestEncryptionKey = "kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"
	imageTaskTestAccessKey     = "image-task-test-access-key"
	imageTaskTestSecretKey     = "image-task-test-secret-key"
)

func TestGetImageTaskConfigNeverReturnsSecrets(t *testing.T) {
	cfg, _, _ := loadImageTaskAdminTestConfig(t, false)
	handler := NewGatewayLiteConfigHandler(cfg)

	recorder := performImageTaskConfigRequest(t, http.MethodGet, nil, handler.GetImageTaskConfig)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), imageTaskTestEncryptionKey)
	require.NotContains(t, recorder.Body.String(), imageTaskTestAccessKey)
	require.NotContains(t, recorder.Body.String(), imageTaskTestSecretKey)
	var envelope struct {
		Data imageTaskConfigResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.True(t, envelope.Data.EncryptionKeyConfigured)
	require.True(t, envelope.Data.Desired.Storage.S3.AccessKeyIDConfigured)
	require.True(t, envelope.Data.Desired.Storage.S3.SecretAccessKeyConfigured)
}

func TestUpdateImageTaskConfigRejectsStaleRevisionWithoutMutation(t *testing.T) {
	cfg, path, original := loadImageTaskAdminTestConfig(t, false)
	handler := NewGatewayLiteConfigHandler(cfg)
	request := imageTaskUpdateRequest(cfg, true)
	stale := "stale-revision"
	request.ExpectedRevision = &stale

	recorder := performImageTaskConfigRequest(t, http.MethodPut, request, handler.UpdateImageTaskConfig)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.False(t, cfg.Gateway.ImageTasks.Enabled)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, current)
}

func TestUpdateImageTaskConfigRequiresEmptyQueueBeforeDisabling(t *testing.T) {
	cfg, path, original := loadImageTaskAdminTestConfig(t, true)
	handler := NewGatewayLiteConfigHandler(cfg)
	handler.SetImageTaskRuntimeState(true, func(context.Context) (int64, error) { return 1, nil })
	request := imageTaskUpdateRequest(cfg, false)

	recorder := performImageTaskConfigRequest(t, http.MethodPut, request, handler.UpdateImageTaskConfig)

	require.Equal(t, http.StatusConflict, recorder.Code)
	require.True(t, cfg.Gateway.ImageTasks.Enabled)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, current)
}

func TestUpdateImageTaskConfigPersistFailureDoesNotMutateRuntimeOrFile(t *testing.T) {
	cfg, path, original := loadImageTaskAdminTestConfig(t, false)
	handler := NewGatewayLiteConfigHandler(cfg)
	request := imageTaskUpdateRequest(cfg, true)
	request.Desired.Storage.LocalPath = filepath.Join(t.TempDir(), "artifacts")
	// Loading must use a real file, but saving to a directory reliably fails on
	// every supported OS. This proves memory is changed only after durability.
	t.Setenv("SUB2API_CONFIG_FILE", t.TempDir())

	recorder := performImageTaskConfigRequest(t, http.MethodPut, request, handler.UpdateImageTaskConfig)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.False(t, cfg.Gateway.ImageTasks.Enabled)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, current)
}

func TestUpdateImageTaskConfigPreservesUnrelatedYAMLAndExistingCredentials(t *testing.T) {
	cfg, path, _ := loadImageTaskAdminTestConfig(t, false)
	handler := NewGatewayLiteConfigHandler(cfg)
	request := imageTaskUpdateRequest(cfg, false)
	request.Desired.WorkerCount = 3

	recorder := performImageTaskConfigRequest(t, http.MethodPut, request, handler.UpdateImageTaskConfig)

	require.Equal(t, http.StatusOK, recorder.Code)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, yaml.Unmarshal(current, &root))
	require.Equal(t, "keep-me", mapValue(root["custom_extension"])["marker"])
	gateway := mapValue(root["gateway"])
	require.Equal(t, "stay", gateway["unrelated_setting"])
	s3 := mapValue(mapValue(mapValue(gateway["image_tasks"])["storage"])["s3"])
	require.Equal(t, imageTaskTestAccessKey, s3["access_key_id"])
	require.Equal(t, imageTaskTestSecretKey, s3["secret_access_key"])
}

func TestUpdateImageTaskConfigGeneratesKeyWithoutReturningIt(t *testing.T) {
	cfg, path, _ := loadImageTaskAdminTestConfig(t, false)
	cfg.Totp.EncryptionKey = ""
	cfg.Totp.EncryptionKeyConfigured = false
	handler := NewGatewayLiteConfigHandler(cfg)
	request := imageTaskUpdateRequest(cfg, true)
	request.GenerateEncryptionKey = true

	recorder := performImageTaskConfigRequest(t, http.MethodPut, request, handler.UpdateImageTaskConfig)

	require.Equal(t, http.StatusOK, recorder.Code)
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, yaml.Unmarshal(current, &root))
	generated, _ := mapValue(root["totp"])["encryption_key"].(string)
	require.Len(t, generated, 64)
	require.NotContains(t, recorder.Body.String(), generated)
	require.True(t, cfg.Totp.EncryptionKeyConfigured)
}

func TestEmptyImageTaskEnvironmentValuesDoNotLockAdminConfig(t *testing.T) {
	for _, key := range imageTaskEnvironmentKeys {
		t.Setenv(key, "")
	}
	t.Setenv("TOTP_ENCRYPTION_KEY", "")

	require.False(t, imageTaskConfigManagedByEnvironment())
	require.False(t, imageTaskEncryptionKeyManagedByEnvironment())
	require.Empty(t, imageTaskEnvironmentOverrides())
}

func loadImageTaskAdminTestConfig(t *testing.T, enabled bool) (*config.Config, string, []byte) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	for _, key := range imageTaskEnvironmentKeys {
		t.Setenv(key, "")
	}
	t.Setenv("TOTP_ENCRYPTION_KEY", "")
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	localPath := filepath.ToSlash(filepath.Join(root, "image-artifacts"))
	content := []byte(fmt.Sprintf(`jwt:
  secret: "%s"
totp:
  encryption_key: "%s"
custom_extension:
  marker: keep-me
gateway:
  unrelated_setting: stay
  image_tasks:
    enabled: %t
    storage:
      type: local
      local_path: "%s"
      s3:
        access_key_id: "%s"
        secret_access_key: "%s"
`, strings.Repeat("j", 64), imageTaskTestEncryptionKey, enabled, localPath, imageTaskTestAccessKey, imageTaskTestSecretKey))
	require.NoError(t, os.WriteFile(path, content, 0o600))
	t.Setenv("SUB2API_CONFIG_FILE", path)
	t.Setenv("CONFIG_FILE", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	return cfg, path, original
}

func imageTaskUpdateRequest(cfg *config.Config, enabled bool) imageTaskConfigUpdateRequest {
	desired := imageTaskConfigViewFromConfig(cfg.Gateway.ImageTasks)
	revision := imageTaskConfigFingerprint(cfg)
	return imageTaskConfigUpdateRequest{
		ExpectedRevision: &revision,
		Desired: imageTaskConfigUpdate{
			Enabled: enabled, WorkerCount: desired.WorkerCount, QueueCapacity: desired.QueueCapacity,
			ClaimBatchSize: desired.ClaimBatchSize, PerUserConcurrency: desired.PerUserConcurrency,
			MaxImagesPerTask: desired.MaxImagesPerTask, MaxAttempts: desired.MaxAttempts,
			ExecutionTimeoutSeconds: desired.ExecutionTimeoutSeconds, ClaimTTLSeconds: desired.ClaimTTLSeconds,
			PollIntervalMillis: desired.PollIntervalMillis, ResultRetentionDays: desired.ResultRetentionDays,
			DownloadURLTTLSeconds: desired.DownloadURLTTLSeconds, MaxRequestBytes: desired.MaxRequestBytes,
			MaxArtifactBytes: desired.MaxArtifactBytes, MaxTaskArtifactBytes: desired.MaxTaskArtifactBytes,
			Storage: imageTaskStorageConfigUpdate{
				Type: desired.Storage.Type, LocalPath: desired.Storage.LocalPath,
				S3: imageTaskS3ConfigUpdate{
					Endpoint: desired.Storage.S3.Endpoint, Region: desired.Storage.S3.Region,
					Bucket: desired.Storage.S3.Bucket, Prefix: desired.Storage.S3.Prefix,
					ForcePathStyle: desired.Storage.S3.ForcePathStyle,
				},
			},
		},
	}
}

func performImageTaskConfigRequest(
	t *testing.T,
	method string,
	body any,
	handler func(*gin.Context),
) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/v1/admin/image-tasks/config", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	return recorder
}
