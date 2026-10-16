package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const statusClientClosedRequest = 499

func concurrencyErrorResponse(err error, slotType string) (int, string, string) {
	var concurrencyErr *ConcurrencyError
	if errors.As(err, &concurrencyErr) {
		if concurrencyErr.SlotType != "" {
			slotType = concurrencyErr.SlotType
		}
		if concurrencyErr.IsQueueFull {
			return http.StatusTooManyRequests, "rate_limit_error",
				"Too many pending requests, please retry later"
		}
		if slotType == "group_user" {
			return http.StatusTooManyRequests, "rate_limit_error",
				"Per-user concurrency limit for this group exceeded, please retry later"
		}
		return http.StatusTooManyRequests, "rate_limit_error",
			fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType)
	}

	if errors.Is(err, context.Canceled) {
		return statusClientClosedRequest, "api_error", "context canceled"
	}
	if errors.Is(err, service.ErrFallbackQueueFull) {
		return http.StatusTooManyRequests, "rate_limit_error",
			"Too many pending requests, please retry later"
	}

	return http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable, please retry later"
}
