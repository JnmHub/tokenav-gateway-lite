package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// resolveModelsListReadLimit returns the one model-directory limit used by all
// upstream model discovery paths. Keeping the fallback here makes partially
// initialized test services safe without weakening the production default.
func resolveModelsListReadLimit(cfg *config.Config) int64 {
	if cfg != nil && cfg.Gateway.ModelsListReadMaxBytes > 0 {
		return cfg.Gateway.ModelsListReadMaxBytes
	}
	return config.DefaultModelsListReadMaxBytes
}
