package repository

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ProvideSyncMediaObjectStore creates the object store used by synchronous
// image responses. It deliberately does not inspect image_tasks.enabled:
// normal image requests must still be bridgeable when the durable task queue
// is disabled. A broken optional store degrades only media rewriting and does
// not prevent the text gateway from starting.
func ProvideSyncMediaObjectStore(cfg *config.Config) service.SyncMediaObjectStore {
	// The control-plane (standard/simple) process must never create a media
	// cache. Its routes intentionally do not expose /v1/media, so creating the
	// store there could produce URLs that no process can serve.
	if cfg == nil || cfg.RunMode != config.RunModeGatewayLite || !cfg.GatewayLite.MediaProxyEnabled {
		return nil
	}
	raw := cfg.Gateway.ImageTasks.Storage
	typeName := strings.ToLower(strings.TrimSpace(raw.Type))
	if typeName == "" {
		typeName = "local"
	}
	switch typeName {
	case "local":
		root := strings.TrimSpace(raw.LocalPath)
		if root == "" {
			root = "./data/image-tasks"
		}
		// JWT is persisted during database bootstrap and therefore remains stable
		// across restarts. Falling back to the configured TOTP key keeps this
		// provider usable in isolated unit tests without inventing an ephemeral
		// signing key that would strand cached objects after a restart.
		secret := strings.TrimSpace(cfg.JWT.Secret)
		if len([]byte(secret)) < 32 {
			secret = strings.TrimSpace(cfg.Totp.EncryptionKey)
		}
		if len([]byte(secret)) < 32 {
			slog.Warn("synchronous media object store disabled: no stable signing secret")
			return nil
		}
		signingKey := sha256.Sum256([]byte("tokenav:sync-image-download:v1\n" + secret))
		store, err := NewLocalImageTaskObjectStore(root, signingKey[:], "")
		if err != nil {
			slog.Warn("synchronous media object store disabled", "reason", "local storage unavailable")
			return nil
		}
		return store
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
			slog.Warn("synchronous media object store disabled", "reason", "S3 storage unavailable")
			return nil
		}
		return store
	default:
		slog.Warn("synchronous media object store disabled", "reason", "unsupported storage type")
		return nil
	}
}
