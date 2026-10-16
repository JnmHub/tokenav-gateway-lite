package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

const grokVideoStatusMaxResponseBytes = 2 << 20

type GrokVideoTaskPollError struct {
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *GrokVideoTaskPollError) Error() string {
	if e == nil {
		return "grok video task poll failed"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("grok video task poll returned status %d: %s", e.StatusCode, message)
}

// PollGrokVideoTaskStatus performs a server-side status query without a Gin
// response writer. It deliberately uses the task's original account and the
// same refresh-aware token and proxy transport as normal Grok requests.
func (s *OpenAIGatewayService) PollGrokVideoTaskStatus(
	ctx context.Context,
	account *Account,
	requestID string,
) (GrokVideoTaskStatus, error) {
	if s == nil || s.httpUpstream == nil {
		return GrokVideoTaskStatus{}, fmt.Errorf("grok video poll transport is unavailable")
	}
	if !supportsGrokMediaAccount(account) {
		return GrokVideoTaskStatus{}, fmt.Errorf("grok video poll account is invalid")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return GrokVideoTaskStatus{}, ErrGrokVideoTaskNotFound
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return GrokVideoTaskStatus{}, err
	}
	targetURL, err := buildGrokMediaURL(account, s.cfg, GrokMediaEndpointVideoStatus, requestID)
	if err != nil {
		return GrokVideoTaskStatus{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return GrokVideoTaskStatus{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "sub2api-grok-task-runner/1.0")
	account.ApplyHeaderOverrides(request.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	response, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return GrokVideoTaskStatus{}, fmt.Errorf("poll grok video upstream: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(response.Body, grokVideoStatusMaxResponseBytes+1))
	if err != nil {
		return GrokVideoTaskStatus{}, fmt.Errorf("read grok video status response: %w", err)
	}
	if len(body) > grokVideoStatusMaxResponseBytes {
		return GrokVideoTaskStatus{}, fmt.Errorf("grok video status response exceeds %d bytes", grokVideoStatusMaxResponseBytes)
	}
	snapshot := xai.ParseQuotaHeaders(response.Header, response.StatusCode)

	if response.StatusCode >= http.StatusBadRequest {
		if shouldRetryGrokUnauthorized(ctx, account, response.StatusCode) {
			if refreshedToken, refreshErr := s.refreshGrokAfterUnauthorized(ctx, account); refreshErr == nil {
				_ = response.Body.Close()
				return s.PollGrokVideoTaskStatus(contextWithGrokUnauthorizedRetry(ctx, refreshedToken), account, requestID)
			}
		}
		s.handleGrokAccountUpstreamError(ctx, account, response.StatusCode, response.Header, body)
		retryAfter := time.Duration(0)
		if snapshot != nil && snapshot.RetryAfterSeconds != nil && *snapshot.RetryAfterSeconds > 0 {
			retryAfter = time.Duration(*snapshot.RetryAfterSeconds) * time.Second
		}
		message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
		return GrokVideoTaskStatus{}, &GrokVideoTaskPollError{
			StatusCode: response.StatusCode,
			RetryAfter: retryAfter,
			Message:    message,
		}
	}
	s.updateGrokUsageSnapshot(ctx, account, snapshot)

	return ParseGrokVideoTaskStatusForAccount(account, body), nil
}
