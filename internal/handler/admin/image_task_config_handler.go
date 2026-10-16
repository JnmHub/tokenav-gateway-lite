package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

type imageTaskS3ConfigView struct {
	Endpoint                  string `json:"endpoint"`
	Region                    string `json:"region"`
	Bucket                    string `json:"bucket"`
	Prefix                    string `json:"prefix"`
	ForcePathStyle            bool   `json:"force_path_style"`
	AccessKeyIDConfigured     bool   `json:"access_key_id_configured"`
	SecretAccessKeyConfigured bool   `json:"secret_access_key_configured"`
}

type imageTaskStorageConfigView struct {
	Type      string                `json:"type"`
	LocalPath string                `json:"local_path"`
	S3        imageTaskS3ConfigView `json:"s3"`
}

type imageTaskConfigView struct {
	Enabled                 bool                       `json:"enabled"`
	WorkerCount             int                        `json:"worker_count"`
	QueueCapacity           int                        `json:"queue_capacity"`
	ClaimBatchSize          int                        `json:"claim_batch_size"`
	PerUserConcurrency      int                        `json:"per_user_concurrency"`
	MaxImagesPerTask        int                        `json:"max_images_per_task"`
	MaxAttempts             int                        `json:"max_attempts"`
	ExecutionTimeoutSeconds int                        `json:"execution_timeout_seconds"`
	ClaimTTLSeconds         int                        `json:"claim_ttl_seconds"`
	PollIntervalMillis      int                        `json:"poll_interval_milliseconds"`
	ResultRetentionDays     int                        `json:"result_retention_days"`
	DownloadURLTTLSeconds   int                        `json:"download_url_ttl_seconds"`
	MaxRequestBytes         int64                      `json:"max_request_bytes"`
	MaxArtifactBytes        int64                      `json:"max_artifact_bytes"`
	MaxTaskArtifactBytes    int64                      `json:"max_task_artifact_bytes"`
	Storage                 imageTaskStorageConfigView `json:"storage"`
}

type imageTaskS3ConfigUpdate struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	ForcePathStyle  bool   `json:"force_path_style"`
}

type imageTaskStorageConfigUpdate struct {
	Type      string                  `json:"type"`
	LocalPath string                  `json:"local_path"`
	S3        imageTaskS3ConfigUpdate `json:"s3"`
}

type imageTaskConfigUpdate struct {
	Enabled                 bool                         `json:"enabled"`
	WorkerCount             int                          `json:"worker_count"`
	QueueCapacity           int                          `json:"queue_capacity"`
	ClaimBatchSize          int                          `json:"claim_batch_size"`
	PerUserConcurrency      int                          `json:"per_user_concurrency"`
	MaxImagesPerTask        int                          `json:"max_images_per_task"`
	MaxAttempts             int                          `json:"max_attempts"`
	ExecutionTimeoutSeconds int                          `json:"execution_timeout_seconds"`
	ClaimTTLSeconds         int                          `json:"claim_ttl_seconds"`
	PollIntervalMillis      int                          `json:"poll_interval_milliseconds"`
	ResultRetentionDays     int                          `json:"result_retention_days"`
	DownloadURLTTLSeconds   int                          `json:"download_url_ttl_seconds"`
	MaxRequestBytes         int64                        `json:"max_request_bytes"`
	MaxArtifactBytes        int64                        `json:"max_artifact_bytes"`
	MaxTaskArtifactBytes    int64                        `json:"max_task_artifact_bytes"`
	Storage                 imageTaskStorageConfigUpdate `json:"storage"`
}

type imageTaskConfigUpdateRequest struct {
	ExpectedRevision      *string               `json:"expected_revision"`
	Desired               imageTaskConfigUpdate `json:"desired"`
	GenerateEncryptionKey bool                  `json:"generate_encryption_key"`
}

type imageTaskConfigResponse struct {
	Revision                string              `json:"revision"`
	Desired                 imageTaskConfigView `json:"desired"`
	EffectiveEnabled        bool                `json:"effective_enabled"`
	EncryptionKeyConfigured bool                `json:"encryption_key_configured"`
	EncryptionKeyReadOnly   bool                `json:"encryption_key_read_only"`
	RestartRequired         bool                `json:"restart_required"`
	ConfigPath              string              `json:"config_path"`
	ConfigWritable          bool                `json:"config_writable"`
	ReadOnly                bool                `json:"read_only"`
	EnvironmentOverrides    []string            `json:"env_overrides"`
	DegradedReason          string              `json:"degraded_reason,omitempty"`
}

// GetImageTaskConfig returns desired values and runtime state separately. It
// never returns the persistent encryption key or either S3 credential.
func (h *GatewayLiteConfigHandler) GetImageTaskConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		response.Error(c, http.StatusServiceUnavailable, "image_task_config_unavailable")
		return
	}
	h.imageTaskConfigMu.Lock()
	defer h.imageTaskConfigMu.Unlock()
	response.Success(c, h.imageTaskConfigResponse())
}

// UpdateImageTaskConfig validates and durably saves desired startup settings.
// Object store, encryption and worker ownership are process-lifetime objects,
// so the running service is intentionally not mutated; restart_required tells
// the page exactly when a controlled restart is needed.
func (h *GatewayLiteConfigHandler) UpdateImageTaskConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		response.Error(c, http.StatusServiceUnavailable, "image_task_config_unavailable")
		return
	}
	var request imageTaskConfigUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.ExpectedRevision == nil {
		response.Error(c, http.StatusBadRequest, "invalid_image_task_config_request")
		return
	}

	h.imageTaskConfigMu.Lock()
	defer h.imageTaskConfigMu.Unlock()
	if current := imageTaskConfigFingerprint(h.cfg); current != strings.TrimSpace(*request.ExpectedRevision) {
		response.Error(c, http.StatusConflict, "image_task_config_revision_conflict")
		return
	}
	if imageTaskConfigManagedByEnvironment() {
		response.Error(c, http.StatusConflict, "image_task_config_managed_by_environment")
		return
	}
	if request.GenerateEncryptionKey && imageTaskEncryptionKeyManagedByEnvironment() {
		response.Error(c, http.StatusConflict, "image_task_encryption_key_managed_by_environment")
		return
	}

	previous := h.cfg.Gateway.ImageTasks
	next := imageTaskConfigFromUpdate(request.Desired, previous)
	next.DegradedReason = ""
	candidate := *h.cfg
	candidate.Gateway.ImageTasks = next
	generatedKey := ""
	if request.GenerateEncryptionKey {
		if candidate.Totp.EncryptionKeyConfigured {
			response.Error(c, http.StatusConflict, "image_task_encryption_key_already_configured")
			return
		}
		key, err := generatePersistentImageTaskKey()
		if err != nil {
			response.Error(c, http.StatusInternalServerError, "image_task_encryption_key_generation_failed")
			return
		}
		generatedKey = key
		candidate.Totp.EncryptionKey = key
		candidate.Totp.EncryptionKeyConfigured = true
	}
	if err := candidate.ValidateImageTasks(); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := candidate.Validate(); err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if h.imageTaskRuntimeEnabled && !next.Enabled && h.imageTaskRuntimeProbe != nil {
		active, err := h.imageTaskRuntimeProbe(c.Request.Context())
		if err != nil {
			response.Error(c, http.StatusServiceUnavailable, "image_task_queue_status_unavailable")
			return
		}
		if active > 0 {
			response.Error(c, http.StatusConflict, "image_task_queue_must_be_empty_before_disabling")
			return
		}
	}
	if next.Enabled && (!previous.Enabled || imageTaskStorageFingerprint(previous) != imageTaskStorageFingerprint(next)) {
		if err := probeImageTaskStorage(c.Request.Context(), &candidate); err != nil {
			response.Error(c, http.StatusBadGateway, "image_task_storage_probe_failed")
			return
		}
	}

	path := gatewayLiteConfigPath()
	if err := writeImageTaskConfig(path, next, request.Desired.Storage.S3, generatedKey); err != nil {
		if errors.Is(err, os.ErrPermission) {
			response.Error(c, http.StatusConflict, "image_task_config_file_not_writable")
			return
		}
		response.Error(c, http.StatusInternalServerError, "image_task_config_persist_failed")
		return
	}
	h.cfg.Gateway.ImageTasks = next
	if generatedKey != "" {
		h.cfg.Totp.EncryptionKey = generatedKey
		h.cfg.Totp.EncryptionKeyConfigured = true
	}
	response.Success(c, h.imageTaskConfigResponse())
}

func (h *GatewayLiteConfigHandler) imageTaskConfigResponse() imageTaskConfigResponse {
	path := gatewayLiteConfigPath()
	overrides := imageTaskEnvironmentOverrides()
	currentHash := imageTaskConfigFingerprint(h.cfg)
	return imageTaskConfigResponse{
		Revision:                currentHash,
		Desired:                 imageTaskConfigViewFromConfig(h.cfg.Gateway.ImageTasks),
		EffectiveEnabled:        h.imageTaskRuntimeEnabled,
		EncryptionKeyConfigured: h.cfg.Totp.EncryptionKeyConfigured,
		EncryptionKeyReadOnly:   imageTaskEncryptionKeyManagedByEnvironment(),
		RestartRequired:         currentHash != h.imageTaskRuntimeHash,
		ConfigPath:              path,
		ConfigWritable:          configFileWritable(path),
		ReadOnly:                imageTaskConfigManagedByEnvironment(),
		EnvironmentOverrides:    overrides,
		DegradedReason:          h.cfg.Gateway.ImageTasks.DegradedReason,
	}
}

func imageTaskConfigViewFromConfig(raw config.ImageTaskConfig) imageTaskConfigView {
	return imageTaskConfigView{
		Enabled: raw.Enabled, WorkerCount: raw.WorkerCount, QueueCapacity: raw.QueueCapacity,
		ClaimBatchSize: raw.ClaimBatchSize, PerUserConcurrency: raw.PerUserConcurrency,
		MaxImagesPerTask: raw.MaxImagesPerTask, MaxAttempts: raw.MaxAttempts,
		ExecutionTimeoutSeconds: raw.ExecutionTimeoutSeconds, ClaimTTLSeconds: raw.ClaimTTLSeconds,
		PollIntervalMillis: raw.PollIntervalMillis, ResultRetentionDays: raw.ResultRetentionDays,
		DownloadURLTTLSeconds: raw.DownloadURLTTLSeconds, MaxRequestBytes: raw.MaxRequestBytes,
		MaxArtifactBytes: raw.MaxArtifactBytes, MaxTaskArtifactBytes: raw.MaxTaskArtifactBytes,
		Storage: imageTaskStorageConfigView{
			Type: raw.Storage.Type, LocalPath: raw.Storage.LocalPath,
			S3: imageTaskS3ConfigView{
				Endpoint: raw.Storage.S3.Endpoint, Region: raw.Storage.S3.Region,
				Bucket: raw.Storage.S3.Bucket, Prefix: raw.Storage.S3.Prefix,
				ForcePathStyle:            raw.Storage.S3.ForcePathStyle,
				AccessKeyIDConfigured:     strings.TrimSpace(raw.Storage.S3.AccessKeyID) != "",
				SecretAccessKeyConfigured: strings.TrimSpace(raw.Storage.S3.SecretAccessKey) != "",
			},
		},
	}
}

func imageTaskConfigFromUpdate(input imageTaskConfigUpdate, previous config.ImageTaskConfig) config.ImageTaskConfig {
	accessKeyID := strings.TrimSpace(input.Storage.S3.AccessKeyID)
	if accessKeyID == "" {
		accessKeyID = previous.Storage.S3.AccessKeyID
	}
	secretAccessKey := strings.TrimSpace(input.Storage.S3.SecretAccessKey)
	if secretAccessKey == "" {
		secretAccessKey = previous.Storage.S3.SecretAccessKey
	}
	return config.ImageTaskConfig{
		Enabled: input.Enabled, WorkerCount: input.WorkerCount, QueueCapacity: input.QueueCapacity,
		ClaimBatchSize: input.ClaimBatchSize, PerUserConcurrency: input.PerUserConcurrency,
		MaxImagesPerTask: input.MaxImagesPerTask, MaxAttempts: input.MaxAttempts,
		ExecutionTimeoutSeconds: input.ExecutionTimeoutSeconds, ClaimTTLSeconds: input.ClaimTTLSeconds,
		PollIntervalMillis: input.PollIntervalMillis, ResultRetentionDays: input.ResultRetentionDays,
		DownloadURLTTLSeconds: input.DownloadURLTTLSeconds, MaxRequestBytes: input.MaxRequestBytes,
		MaxArtifactBytes: input.MaxArtifactBytes, MaxTaskArtifactBytes: input.MaxTaskArtifactBytes,
		Storage: config.ImageTaskStorageConfig{
			Type: strings.ToLower(strings.TrimSpace(input.Storage.Type)), LocalPath: strings.TrimSpace(input.Storage.LocalPath),
			S3: config.ImageTaskS3Config{
				Endpoint: strings.TrimSpace(input.Storage.S3.Endpoint), Region: strings.TrimSpace(input.Storage.S3.Region),
				Bucket: strings.TrimSpace(input.Storage.S3.Bucket), Prefix: strings.Trim(strings.TrimSpace(input.Storage.S3.Prefix), "/"),
				AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey, ForcePathStyle: input.Storage.S3.ForcePathStyle,
			},
		},
	}
}

func imageTaskConfigFingerprint(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	payload, _ := json.Marshal(struct {
		ImageTasks config.ImageTaskConfig `json:"image_tasks"`
		Key        string                 `json:"key"`
		Configured bool                   `json:"configured"`
	}{cfg.Gateway.ImageTasks, cfg.Totp.EncryptionKey, cfg.Totp.EncryptionKeyConfigured})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func imageTaskStorageFingerprint(raw config.ImageTaskConfig) string {
	payload, _ := json.Marshal(raw.Storage)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func generatePersistentImageTaskKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

func probeImageTaskStorage(parent context.Context, candidate *config.Config) error {
	if candidate == nil {
		return errors.New("candidate config is required")
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	raw := candidate.Gateway.ImageTasks.Storage
	if strings.EqualFold(strings.TrimSpace(raw.Type), "local") {
		if err := os.MkdirAll(raw.LocalPath, 0750); err != nil {
			return err
		}
		probe, err := os.CreateTemp(raw.LocalPath, ".image-task-config-probe-*")
		if err != nil {
			return err
		}
		name := probe.Name()
		defer os.Remove(name)
		if _, err := probe.Write([]byte("ok")); err != nil {
			_ = probe.Close()
			return err
		}
		if err := probe.Sync(); err != nil {
			_ = probe.Close()
			return err
		}
		return probe.Close()
	}
	s3cfg := raw.S3
	store, err := repository.NewS3ImageTaskObjectStore(ctx, &service.BackupS3Config{
		Endpoint: s3cfg.Endpoint, Region: s3cfg.Region, Bucket: s3cfg.Bucket, Prefix: s3cfg.Prefix,
		AccessKeyID: s3cfg.AccessKeyID, SecretAccessKey: s3cfg.SecretAccessKey, ForcePathStyle: s3cfg.ForcePathStyle,
	})
	if err != nil {
		return err
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	key := ".tokenav-config-probe/" + hex.EncodeToString(random)
	if _, _, err := store.Put(ctx, key, bytes.NewReader([]byte("ok")), "text/plain", 16); err != nil {
		return err
	}
	cleanupNeeded := true
	defer func() {
		if !cleanupNeeded {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
		defer cleanupCancel()
		_ = store.Delete(cleanupCtx, key)
	}()
	body, _, _, err := store.Open(ctx, key)
	if err != nil {
		return err
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, 3))
	if err != nil || string(content) != "ok" {
		return errors.New("image task S3 read-back failed")
	}
	if err := store.Delete(ctx, key); err != nil {
		return err
	}
	cleanupNeeded = false
	return nil
}

var imageTaskEnvironmentKeys = []string{
	"GATEWAY_IMAGE_TASKS_ENABLED", "GATEWAY_IMAGE_TASKS_WORKER_COUNT", "GATEWAY_IMAGE_TASKS_QUEUE_CAPACITY",
	"GATEWAY_IMAGE_TASKS_CLAIM_BATCH_SIZE", "GATEWAY_IMAGE_TASKS_PER_USER_CONCURRENCY",
	"GATEWAY_IMAGE_TASKS_MAX_IMAGES_PER_TASK", "GATEWAY_IMAGE_TASKS_MAX_ATTEMPTS",
	"GATEWAY_IMAGE_TASKS_EXECUTION_TIMEOUT_SECONDS", "GATEWAY_IMAGE_TASKS_CLAIM_TTL_SECONDS",
	"GATEWAY_IMAGE_TASKS_POLL_INTERVAL_MILLISECONDS", "GATEWAY_IMAGE_TASKS_RESULT_RETENTION_DAYS",
	"GATEWAY_IMAGE_TASKS_DOWNLOAD_URL_TTL_SECONDS", "GATEWAY_IMAGE_TASKS_MAX_REQUEST_BYTES",
	"GATEWAY_IMAGE_TASKS_MAX_ARTIFACT_BYTES", "GATEWAY_IMAGE_TASKS_MAX_TASK_ARTIFACT_BYTES",
	"GATEWAY_IMAGE_TASKS_STORAGE_TYPE", "GATEWAY_IMAGE_TASKS_STORAGE_LOCAL_PATH",
	"GATEWAY_IMAGE_TASKS_STORAGE_S3_ENDPOINT", "GATEWAY_IMAGE_TASKS_STORAGE_S3_REGION",
	"GATEWAY_IMAGE_TASKS_STORAGE_S3_BUCKET", "GATEWAY_IMAGE_TASKS_STORAGE_S3_PREFIX",
	"GATEWAY_IMAGE_TASKS_STORAGE_S3_ACCESS_KEY_ID", "GATEWAY_IMAGE_TASKS_STORAGE_S3_SECRET_ACCESS_KEY",
	"GATEWAY_IMAGE_TASKS_STORAGE_S3_FORCE_PATH_STYLE",
}

func imageTaskEnvironmentOverrides() []string {
	keys := make([]string, 0, len(imageTaskEnvironmentKeys)+1)
	for _, key := range imageTaskEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
			keys = append(keys, key)
		}
	}
	if imageTaskEncryptionKeyManagedByEnvironment() {
		keys = append(keys, "TOTP_ENCRYPTION_KEY")
	}
	return keys
}

func imageTaskConfigManagedByEnvironment() bool {
	for _, key := range imageTaskEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func imageTaskEncryptionKeyManagedByEnvironment() bool {
	value, ok := os.LookupEnv("TOTP_ENCRYPTION_KEY")
	return ok && strings.TrimSpace(value) != ""
}

func configFileWritable(path string) bool {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err == nil {
		_ = file.Close()
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	directory := filepath.Dir(path)
	for {
		info, statErr := os.Stat(directory)
		if statErr == nil {
			return info.IsDir() && info.Mode().Perm()&0200 != 0
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false
		}
		directory = parent
	}
}

func writeImageTaskConfig(path string, raw config.ImageTaskConfig, credentialUpdate imageTaskS3ConfigUpdate, generatedKey string) error {
	configFileWriteMu.Lock()
	defer configFileWriteMu.Unlock()
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		if err := yaml.Unmarshal(data, &root); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	gateway := mapValue(root["gateway"])
	storage := mapValue(mapValue(gateway["image_tasks"])["storage"])
	s3 := mapValue(storage["s3"])
	if strings.TrimSpace(credentialUpdate.AccessKeyID) != "" {
		s3["access_key_id"] = strings.TrimSpace(credentialUpdate.AccessKeyID)
	}
	if strings.TrimSpace(credentialUpdate.SecretAccessKey) != "" {
		s3["secret_access_key"] = strings.TrimSpace(credentialUpdate.SecretAccessKey)
	}
	s3["endpoint"] = raw.Storage.S3.Endpoint
	s3["region"] = raw.Storage.S3.Region
	s3["bucket"] = raw.Storage.S3.Bucket
	s3["prefix"] = raw.Storage.S3.Prefix
	s3["force_path_style"] = raw.Storage.S3.ForcePathStyle
	storage["type"] = raw.Storage.Type
	storage["local_path"] = raw.Storage.LocalPath
	storage["s3"] = s3
	gateway["image_tasks"] = map[string]any{
		"enabled": raw.Enabled, "worker_count": raw.WorkerCount, "queue_capacity": raw.QueueCapacity,
		"claim_batch_size": raw.ClaimBatchSize, "per_user_concurrency": raw.PerUserConcurrency,
		"max_images_per_task": raw.MaxImagesPerTask, "max_attempts": raw.MaxAttempts,
		"execution_timeout_seconds": raw.ExecutionTimeoutSeconds, "claim_ttl_seconds": raw.ClaimTTLSeconds,
		"poll_interval_milliseconds": raw.PollIntervalMillis, "result_retention_days": raw.ResultRetentionDays,
		"download_url_ttl_seconds": raw.DownloadURLTTLSeconds, "max_request_bytes": raw.MaxRequestBytes,
		"max_artifact_bytes": raw.MaxArtifactBytes, "max_task_artifact_bytes": raw.MaxTaskArtifactBytes,
		"storage": storage,
	}
	root["gateway"] = gateway
	if generatedKey != "" {
		totp := mapValue(root["totp"])
		totp["encryption_key"] = generatedKey
		root["totp"] = totp
	}
	data, err := yaml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal image task config: %w", err)
	}
	return replaceConfigFileAtomically(path, data, 0600)
}

func mapValue(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok && mapped != nil {
		return mapped
	}
	return map[string]any{}
}
