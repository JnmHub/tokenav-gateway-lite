package service

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ProvideImageTaskConfig translates human-friendly integer configuration into
// the bounded duration values consumed by the durable worker. Keeping this in
// one place prevents the HTTP and worker layers from interpreting units
// differently.
func ProvideImageTaskConfig(cfg *config.Config) ImageTaskConfig {
	defaults := DefaultImageTaskConfig()
	if cfg == nil {
		return defaults
	}
	raw := cfg.Gateway.ImageTasks
	if strings.TrimSpace(raw.DegradedReason) != "" {
		// Keep the invalid desired YAML visible to the admin page, but give the
		// runtime a known-valid disabled configuration so text serving can start.
		return defaults
	}
	return ImageTaskConfig{
		Enabled:              raw.Enabled,
		Workers:              raw.WorkerCount,
		QueueCapacity:        raw.QueueCapacity,
		BatchSize:            raw.ClaimBatchSize,
		PerUserConcurrency:   raw.PerUserConcurrency,
		MaxImagesPerTask:     raw.MaxImagesPerTask,
		MaxAttempts:          raw.MaxAttempts,
		ExecutionTimeout:     time.Duration(raw.ExecutionTimeoutSeconds) * time.Second,
		ClaimTTL:             time.Duration(raw.ClaimTTLSeconds) * time.Second,
		PollInterval:         time.Duration(raw.PollIntervalMillis) * time.Millisecond,
		ResultRetention:      time.Duration(raw.ResultRetentionDays) * 24 * time.Hour,
		DownloadURLTTL:       time.Duration(raw.DownloadURLTTLSeconds) * time.Second,
		MaxRequestBytes:      raw.MaxRequestBytes,
		MaxArtifactBytes:     raw.MaxArtifactBytes,
		MaxTaskArtifactBytes: raw.MaxTaskArtifactBytes,
	}
}
