package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPartialStreamUsageResultPreservesObservedUsage(t *testing.T) {
	startedAt := time.Now().Add(-time.Second)
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("x-request-id", "req-partial")
	streamResult := &streamingResult{
		usage:        &ClaudeUsage{InputTokens: 12, CacheReadInputTokens: 4},
		firstTokenMs: intPtr(35),
	}

	requestedEffort := "max"
	upstreamEffort := "high"
	result := partialStreamUsageResult(
		resp,
		streamResult,
		"claude-sonnet",
		"upstream-sonnet",
		&requestedEffort,
		&upstreamEffort,
		startedAt,
		errors.New("unexpected EOF"),
	)
	require.NotNil(t, result)
	require.Equal(t, "req-partial", result.RequestID)
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 4, result.Usage.CacheReadInputTokens)
	require.Equal(t, "max", *result.RequestedReasoningEffort)
	require.Equal(t, "high", *result.ReasoningEffort)
	require.True(t, result.Stream)
}

func TestPartialStreamUsageResultSkipsFailoverAndEmptyUsage(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	require.Nil(t, partialStreamUsageResult(resp, &streamingResult{usage: &ClaudeUsage{}}, "m", "u", nil, nil, time.Now(), errors.New("EOF")))
	require.Nil(t, partialStreamUsageResult(
		resp,
		&streamingResult{usage: &ClaudeUsage{InputTokens: 1}},
		"m",
		"u",
		nil,
		nil,
		time.Now(),
		&UpstreamFailoverError{StatusCode: http.StatusBadGateway},
	))
}

func TestOpenAIStreamRateLimitPromotesSemanticStatus(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"error":{"type":"invalid_request_error","code":"rate_limit_exceeded","message":"Concurrency limit exceeded"}}}`)
	require.Equal(t, http.StatusTooManyRequests, openAIStreamFailureStatus(payload, "Concurrency limit exceeded"))
	require.True(t, openAIStreamFailedEventShouldFailover(payload, "Concurrency limit exceeded"))

	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(http.StatusTooManyRequests)},
	}}
	headers := http.Header{"Retry-After": []string{"1"}}
	err := (&OpenAIGatewayService{}).newOpenAIStreamFailoverError(nil, account, true, "", payload, "Concurrency limit exceeded", headers)
	require.Equal(t, http.StatusTooManyRequests, err.StatusCode)
	require.True(t, err.RetryableOnSameAccount)
	require.Equal(t, "1", err.ResponseHeaders.Get("Retry-After"))
	require.Equal(t, "rate_limit_error", gjson.GetBytes(err.ResponseBody, "error.type").String())
}

func TestOpenAIStreamingResponseFailedRateLimitUsesPoolRetryPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req-rate-limit"}, "Retry-After": []string{"1"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			"event: response.failed",
			`data: {"type":"response.failed","response":{"id":"resp_1","error":{"type":"invalid_request_error","code":"rate_limit_exceeded","message":"Concurrency limit exceeded"}}}`,
			"",
		}, "\n"))),
	}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(http.StatusTooManyRequests)},
	}}

	_, err := service.handleStreamingResponse(ctx.Request.Context(), response, ctx, account, time.Now(), "gpt-5.6-sol", "gpt-5.6-sol")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, "1", failoverErr.ResponseHeaders.Get("Retry-After"))
	require.False(t, ctx.Writer.Written())
}

func TestEncryptedCompactionRecoveryDropsAccountBoundItems(t *testing.T) {
	input := map[string]any{
		"type":              "compaction_summary",
		"encrypted_content": "account-bound",
		"summary":           []any{map[string]any{"type": "summary_text", "text": "keep only on the same account"}},
	}
	next, changed, keep := sanitizeEncryptedReasoningInputItem(input)
	require.Nil(t, next)
	require.True(t, changed)
	require.False(t, keep)
}
