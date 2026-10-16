package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestProvideImageTaskConfigDisablesDegradedDesiredConfiguration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.ImageTasks.Enabled = true
	cfg.Gateway.ImageTasks.DegradedReason = "persistent encryption key is missing"

	runtimeConfig := ProvideImageTaskConfig(cfg)

	require.False(t, runtimeConfig.Enabled,
		"a degraded desired configuration must never start image workers")
}
