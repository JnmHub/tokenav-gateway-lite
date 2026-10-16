package service

import (
	"math"
	"strings"
	"time"
)

// CodexQuotaHealthStatus is a read-only explanation of the latest upstream
// 5h/7d quota snapshot. It deliberately does not participate in scheduling:
// the existing quota auto-pause code remains the only source of truth for
// whether a request may run.
type CodexQuotaHealthStatus string

const (
	CodexQuotaHealthUnknown                 CodexQuotaHealthStatus = "unknown"
	CodexQuotaHealthHealthy                 CodexQuotaHealthStatus = "healthy"
	CodexQuotaHealthNearLimit               CodexQuotaHealthStatus = "near_limit"
	CodexQuotaHealthExhausted               CodexQuotaHealthStatus = "exhausted"
	CodexQuotaHealthRecoveredPendingRefresh CodexQuotaHealthStatus = "recovered_pending_refresh"
)

const (
	codexQuotaHealthNearLimitPercent = 95.0
)

// CodexQuotaWindowHealth describes one provider quota window without exposing
// credentials or response bodies.
type CodexQuotaWindowHealth struct {
	Status      CodexQuotaHealthStatus `json:"status"`
	UsedPercent *float64               `json:"used_percent,omitempty"`
	ResetAt     *time.Time             `json:"reset_at,omitempty"`
}

// CodexQuotaHealthSnapshot is derived from accounts.extra on read. Keeping it
// derived avoids an extra JSONB write for every response header and prevents a
// partial 5h/7d header update from overwriting the other window's evidence.
type CodexQuotaHealthSnapshot struct {
	Status           CodexQuotaHealthStatus  `json:"status"`
	TriggerWindow    string                  `json:"trigger_window,omitempty"`
	Reason           string                  `json:"reason,omitempty"`
	Source           string                  `json:"source,omitempty"`
	ObservedAt       *time.Time              `json:"observed_at,omitempty"`
	NextResetAt      *time.Time              `json:"next_reset_at,omitempty"`
	Stale            bool                    `json:"stale"`
	ThresholdPercent float64                 `json:"threshold_percent"`
	FiveHour         *CodexQuotaWindowHealth `json:"five_hour,omitempty"`
	SevenDay         *CodexQuotaWindowHealth `json:"seven_day,omitempty"`
}

// CodexQuotaHealth derives a stable display state from the same snapshot keys
// used by auto-pause and scheduler soft signals. It never performs I/O.
func (a *Account) CodexQuotaHealth(nowValues ...time.Time) *CodexQuotaHealthSnapshot {
	if a == nil || a.Platform != PlatformOpenAI || len(a.Extra) == 0 {
		return nil
	}

	now := time.Now().UTC()
	if len(nowValues) > 0 && !nowValues[0].IsZero() {
		now = nowValues[0].UTC()
	}

	observedAt, hasObservedAt := time.Time{}, false
	for _, key := range []string{
		"codex_usage_updated_at",
		"codex_5h_usage_updated_at",
		"codex_7d_usage_updated_at",
	} {
		if ts, ok := parseExtraTimestamp(a.Extra[key]); ok && (!hasObservedAt || ts.After(observedAt)) {
			observedAt, hasObservedAt = ts, true
		}
	}
	if !hasObservedAt {
		return nil
	}
	observedAt = observedAt.UTC()
	fiveAt, fiveTimestampOK := codexWindowUpdatedAt(a.Extra, "5h")
	sevenAt, sevenTimestampOK := codexWindowUpdatedAt(a.Extra, "7d")
	fiveStale := !fiveTimestampOK || codexQuotaHealthTimestampStale(fiveAt, now)
	sevenStale := !sevenTimestampOK || codexQuotaHealthTimestampStale(sevenAt, now)
	five := codexQuotaHealthWindow(a.Extra, "5h", fiveAt, now, fiveStale)
	seven := codexQuotaHealthWindow(a.Extra, "7d", sevenAt, now, sevenStale)
	if five == nil && seven == nil {
		return nil
	}
	// A missing timestamp is only stale when that window actually has a usage
	// value. An account with a single-window snapshot should not be marked stale
	// merely because the provider omitted the other window entirely.
	stale := (five != nil && fiveStale) || (seven != nil && sevenStale)

	snapshot := &CodexQuotaHealthSnapshot{
		Status:           CodexQuotaHealthUnknown,
		Source:           "upstream_headers",
		ObservedAt:       &observedAt,
		Stale:            stale,
		ThresholdPercent: codexQuotaHealthNearLimitPercent,
		FiveHour:         five,
		SevenDay:         seven,
	}

	known := make([]*CodexQuotaWindowHealth, 0, 2)
	for _, window := range []*CodexQuotaWindowHealth{five, seven} {
		if window == nil {
			continue
		}
		if window.ResetAt != nil && window.ResetAt.After(now) {
			if snapshot.NextResetAt == nil || window.ResetAt.Before(*snapshot.NextResetAt) {
				reset := *window.ResetAt
				snapshot.NextResetAt = &reset
			}
		}
		if window.Status != CodexQuotaHealthUnknown {
			known = append(known, window)
		}
	}
	if len(known) == 0 {
		if stale {
			snapshot.Reason = "snapshot_stale"
		} else {
			snapshot.Reason = "missing_window_data"
		}
		return snapshot
	}

	// A stricter state wins so the badge never says healthy while one window is
	// actually exhausted. This is display-only; scheduling still uses its own
	// existing threshold and reset checks.
	for _, status := range []CodexQuotaHealthStatus{
		CodexQuotaHealthExhausted,
		CodexQuotaHealthNearLimit,
		CodexQuotaHealthRecoveredPendingRefresh,
		CodexQuotaHealthHealthy,
	} {
		windows := make([]string, 0, 2)
		for _, window := range known {
			if window.Status == status {
				if window == five {
					windows = append(windows, "5h")
				} else {
					windows = append(windows, "7d")
				}
			}
		}
		if len(windows) == 0 {
			continue
		}
		snapshot.Status = status
		snapshot.TriggerWindow = strings.Join(windows, ",")
		switch status {
		case CodexQuotaHealthExhausted:
			snapshot.Reason = "window_exhausted"
		case CodexQuotaHealthNearLimit:
			snapshot.Reason = "near_threshold"
		case CodexQuotaHealthRecoveredPendingRefresh:
			snapshot.Reason = "reset_elapsed"
		default:
			snapshot.Reason = "within_limit"
		}
		break
	}
	return snapshot
}

func codexQuotaHealthWindow(extra map[string]any, window string, observedAt, now time.Time, stale bool) *CodexQuotaWindowHealth {
	used, ok := resolveAccountExtraNumber(extra, "codex_"+window+"_used_percent")
	if !ok || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
		return nil
	}
	resetAt := codexQuotaHealthResetAt(extra, window, observedAt)
	result := &CodexQuotaWindowHealth{Status: CodexQuotaHealthUnknown, UsedPercent: &used, ResetAt: resetAt}
	if stale {
		return result
	}
	if resetAt != nil && !resetAt.After(now) {
		result.Status = CodexQuotaHealthRecoveredPendingRefresh
		return result
	}
	switch {
	case used >= 100:
		result.Status = CodexQuotaHealthExhausted
	case used >= codexQuotaHealthNearLimitPercent:
		result.Status = CodexQuotaHealthNearLimit
	default:
		result.Status = CodexQuotaHealthHealthy
	}
	return result
}

func codexQuotaHealthTimestampStale(observedAt, now time.Time) bool {
	if observedAt.IsZero() || observedAt.After(now) {
		return true
	}
	return now.Sub(observedAt) > openAICodexAutoPauseStaleAfter
}

func codexQuotaHealthResetAt(extra map[string]any, window string, observedAt time.Time) *time.Time {
	if raw, ok := extra["codex_"+window+"_reset_at"]; ok {
		if parsed, ok := parseExtraTimestamp(raw); ok {
			value := parsed.UTC()
			return &value
		}
	}
	seconds, ok := resolveAccountExtraNumber(extra, "codex_"+window+"_reset_after_seconds")
	if !ok || seconds <= 0 || seconds > float64(30*24*time.Hour/time.Second) || observedAt.IsZero() {
		return nil
	}
	value := observedAt.Add(time.Duration(seconds) * time.Second).UTC()
	return &value
}
