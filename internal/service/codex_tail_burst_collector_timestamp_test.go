package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageValuesEqualIgnoresAllCodexWindowTimestamps(t *testing.T) {
	existing := map[string]any{
		"codex_5h_used_percent":     42.0,
		"codex_7d_used_percent":     17.0,
		"codex_usage_updated_at":    "old-global",
		"codex_5h_usage_updated_at": "old-5h",
		"codex_7d_usage_updated_at": "old-7d",
	}
	updates := map[string]any{
		"codex_5h_used_percent":     42.0,
		"codex_7d_used_percent":     17.0,
		"codex_usage_updated_at":    "new-global",
		"codex_5h_usage_updated_at": "new-5h",
		"codex_7d_usage_updated_at": "new-7d",
	}
	require.True(t, usageValuesEqual(existing, updates))
}

func TestCodexTailBurstCollectorTouchUpdatesPerWindow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]any{
		"codex_usage_updated_at":    now.Format(time.RFC3339),
		"codex_5h_usage_updated_at": now.Format(time.RFC3339),
		"codex_7d_usage_updated_at": now.Format(time.RFC3339),
	}

	// A fresh 5h timestamp must not suppress a missing 7d timestamp. The global
	// compatibility timestamp is refreshed independently as well.
	existing := map[string]any{
		"codex_usage_updated_at":    now.Add(-2 * time.Second).Format(time.RFC3339),
		"codex_5h_usage_updated_at": now.Add(-500 * time.Millisecond).Format(time.RFC3339Nano),
	}
	got := codexTailBurstCollectorTouchUpdates(existing, updates, now)
	require.Equal(t, map[string]any{
		"codex_usage_updated_at":    updates["codex_usage_updated_at"],
		"codex_7d_usage_updated_at": updates["codex_7d_usage_updated_at"],
	}, got)
}

func TestCodexTailBurstCollectorTouchUpdatesRejectsFutureTimestamp(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]any{
		"codex_5h_usage_updated_at": now.Format(time.RFC3339),
	}
	existing := map[string]any{
		"codex_5h_usage_updated_at": now.Add(time.Second).Format(time.RFC3339),
	}
	got := codexTailBurstCollectorTouchUpdates(existing, updates, now)
	require.Equal(t, updates, got)
}
