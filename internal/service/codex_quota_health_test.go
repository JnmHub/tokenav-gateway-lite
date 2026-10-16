package service

import (
	"testing"
	"time"
)

func TestCodexQuotaHealthClassifiesFreshWindows(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	reset5h := now.Add(2 * time.Hour).Format(time.RFC3339)
	reset7d := now.Add(24 * time.Hour).Format(time.RFC3339)
	used5h := 96.0
	used7d := 42.0
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Format(time.RFC3339),
			"codex_5h_used_percent":  used5h,
			"codex_5h_reset_at":      reset5h,
			"codex_7d_used_percent":  used7d,
			"codex_7d_reset_at":      reset7d,
		},
	}

	got := account.CodexQuotaHealth(now)
	if got == nil {
		t.Fatal("expected health snapshot")
	}
	if got.Status != CodexQuotaHealthNearLimit || got.TriggerWindow != "5h" {
		t.Fatalf("status=%q trigger=%q, want near_limit/5h", got.Status, got.TriggerWindow)
	}
	if got.FiveHour == nil || got.FiveHour.Status != CodexQuotaHealthNearLimit {
		t.Fatalf("5h window=%+v, want near_limit", got.FiveHour)
	}
	if got.SevenDay == nil || got.SevenDay.Status != CodexQuotaHealthHealthy {
		t.Fatalf("7d window=%+v, want healthy", got.SevenDay)
	}
}

func TestCodexQuotaHealthMarksExhaustedWindow(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Format(time.RFC3339),
			"codex_5h_used_percent":  100.0,
			"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
			"codex_7d_used_percent":  100.0,
			"codex_7d_reset_at":      now.Add(48 * time.Hour).Format(time.RFC3339),
		},
	}

	got := account.CodexQuotaHealth(now)
	if got == nil || got.Status != CodexQuotaHealthExhausted || got.TriggerWindow != "5h,7d" {
		t.Fatalf("got=%+v, want exhausted for both windows", got)
	}
}

func TestCodexQuotaHealthUsesRecoveredPendingRefreshAfterReset(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-10 * time.Minute).Format(time.RFC3339),
			"codex_5h_used_percent":  100.0,
			"codex_5h_reset_at":      now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	got := account.CodexQuotaHealth(now)
	if got == nil || got.Status != CodexQuotaHealthRecoveredPendingRefresh {
		t.Fatalf("got=%+v, want recovered_pending_refresh", got)
	}
	if got.FiveHour == nil || got.FiveHour.Status != CodexQuotaHealthRecoveredPendingRefresh {
		t.Fatalf("5h window=%+v, want recovered_pending_refresh", got.FiveHour)
	}
}

func TestCodexQuotaHealthTreatsStaleSnapshotAsUnknown(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-openAICodexAutoPauseStaleAfter - time.Minute).Format(time.RFC3339),
			"codex_5h_used_percent":  100.0,
			"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
		},
	}

	got := account.CodexQuotaHealth(now)
	if got == nil || got.Status != CodexQuotaHealthUnknown || !got.Stale {
		t.Fatalf("got=%+v, want stale unknown", got)
	}
	if got.FiveHour == nil || got.FiveHour.Status != CodexQuotaHealthUnknown {
		t.Fatalf("5h window=%+v, want unknown", got.FiveHour)
	}
}

func TestCodexQuotaHealthRequiresSnapshotTimestamp(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Extra: map[string]any{"codex_5h_used_percent": 99.0}}
	if got := account.CodexQuotaHealth(time.Now()); got != nil {
		t.Fatalf("got=%+v, want nil without observed timestamp", got)
	}
}

func TestCodexQuotaHealthDoesNotBorrowTimestampAcrossModernWindows(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_usage_updated_at":       now.Format(time.RFC3339),
			"codex_5h_usage_updated_at":    now.Format(time.RFC3339),
			"codex_5h_used_percent":        20.0,
			"codex_5h_reset_at":            now.Add(time.Hour).Format(time.RFC3339),
			"codex_7d_used_percent":        99.0,
			"codex_7d_reset_after_seconds": 3600,
		},
	}

	got := account.CodexQuotaHealth(now)
	if got == nil || got.SevenDay == nil {
		t.Fatalf("got=%+v, want a 7d window", got)
	}
	if got.SevenDay.Status != CodexQuotaHealthUnknown {
		t.Fatalf("7d status=%q, want unknown without its own timestamp", got.SevenDay.Status)
	}
}

func TestCodexQuotaHealthTreatsFutureWindowTimestampAsStale(t *testing.T) {
	now := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			"codex_5h_usage_updated_at": now.Add(time.Minute).Format(time.RFC3339),
			"codex_5h_used_percent":     100.0,
			"codex_5h_reset_at":         now.Add(time.Hour).Format(time.RFC3339),
		},
	}

	got := account.CodexQuotaHealth(now)
	if got == nil || got.FiveHour == nil || got.FiveHour.Status != CodexQuotaHealthUnknown || !got.Stale {
		t.Fatalf("got=%+v, want stale unknown 5h window", got)
	}
}
