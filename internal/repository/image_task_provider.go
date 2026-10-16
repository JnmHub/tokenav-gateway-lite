package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func ProvideImageTaskObjectStore(cfg *config.Config) (service.ImageTaskObjectStore, error) {
	if cfg == nil || !cfg.Gateway.ImageTasks.Enabled || strings.TrimSpace(cfg.Gateway.ImageTasks.DegradedReason) != "" {
		return nil, nil
	}
	raw := cfg.Gateway.ImageTasks.Storage
	switch strings.ToLower(strings.TrimSpace(raw.Type)) {
	case "local":
		// Derive a purpose-separated signing key from the persistent encryption
		// key. The source secret is never written to task rows or returned by APIs.
		signingKey := sha256.Sum256([]byte("tokenav:image-task-download:v1\n" + cfg.Totp.EncryptionKey))
		store, err := NewLocalImageTaskObjectStore(raw.LocalPath, signingKey[:], "")
		if err != nil {
			return disableImageTasksForObjectStore(cfg, err), nil
		}
		return store, nil
	case "s3":
		s3cfg := raw.S3
		store, err := NewS3ImageTaskObjectStore(context.Background(), &service.BackupS3Config{
			Endpoint:        strings.TrimSpace(s3cfg.Endpoint),
			Region:          strings.TrimSpace(s3cfg.Region),
			Bucket:          strings.TrimSpace(s3cfg.Bucket),
			Prefix:          strings.Trim(strings.TrimSpace(s3cfg.Prefix), "/"),
			AccessKeyID:     strings.TrimSpace(s3cfg.AccessKeyID),
			SecretAccessKey: strings.TrimSpace(s3cfg.SecretAccessKey),
			ForcePathStyle:  s3cfg.ForcePathStyle,
		})
		if err != nil {
			return disableImageTasksForObjectStore(cfg, err), nil
		}
		return store, nil
	default:
		return disableImageTasksForObjectStore(cfg, errors.New("unsupported image task object storage type")), nil
	}
}

func disableImageTasksForObjectStore(cfg *config.Config, err error) service.ImageTaskObjectStore {
	if cfg != nil {
		cfg.Gateway.ImageTasks.DegradedReason = "object storage initialization failed"
	}
	// Object-store details may include endpoints or filesystem paths. Keep the
	// administrator-facing reason generic and leave the protected log for diagnosis.
	slog.Error("async image task object storage unavailable; subsystem disabled", "error", err)
	return nil
}
