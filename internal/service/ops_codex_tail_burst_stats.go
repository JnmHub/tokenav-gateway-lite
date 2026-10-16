package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// GetCodexTailBurstStats returns durable Tail-Burst session history for the
// administrator dashboard. The reader is optional during rolling upgrades;
// returning a service-unavailable error is clearer than silently showing an
// empty history when the migration or repository capability is missing.
func (s *OpsService) GetCodexTailBurstStats(ctx context.Context, filter CodexTailBurstStatsFilter) (*CodexTailBurstStatsResponse, error) {
	if s == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_SERVICE_UNAVAILABLE", "Ops service not available")
	}
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	reader, ok := s.opsRepo.(CodexTailBurstHistoryReader)
	if !ok || reader == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_TAIL_BURST_HISTORY_UNAVAILABLE", "Codex tail-burst history is not available")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_REQUIRED", "start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}
	if filter.EndTime.Sub(filter.StartTime) > 30*24*time.Hour {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_TOO_LARGE", "time range must not exceed 30 days")
	}
	if err := validateCodexTailBurstStatsEnums(filter); err != nil {
		return nil, err
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	return reader.GetCodexTailBurstStats(ctx, filter)
}

// validateCodexTailBurstStatsEnums validates user-supplied enum filters at the
// service boundary. Repository errors are intentionally implementation
// details; returning a typed 400 here prevents malformed filters from being
// exposed as misleading 500 responses by the HTTP handler.
func validateCodexTailBurstStatsEnums(filter CodexTailBurstStatsFilter) error {
	if filter.Window != "" {
		switch strings.ToLower(strings.TrimSpace(filter.Window)) {
		case "5h", "7d":
		default:
			return infraerrors.BadRequest("OPS_WINDOW_INVALID", "window must be 5h or 7d")
		}
	}
	if filter.Reason != "" {
		switch strings.ToLower(strings.TrimSpace(filter.Reason)) {
		case "used_ratio", "reset_soon", "account_expiry":
		default:
			return infraerrors.BadRequest("OPS_REASON_INVALID", "reason is invalid")
		}
	}
	if filter.Status != "" {
		switch strings.ToLower(strings.TrimSpace(filter.Status)) {
		case "active", "settled":
		default:
			return infraerrors.BadRequest("OPS_STATUS_INVALID", "status must be active or settled")
		}
	}
	return nil
}
