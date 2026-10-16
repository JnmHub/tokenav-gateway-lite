package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokUpstreamErrorFailoverMatrix(t *testing.T) {
	svc := &OpenAIGatewayService{}
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":{"message":"invalid parameter"}}`, want: false},
		{name: "unauthorized after refresh", status: http.StatusUnauthorized, body: `{"error":{"message":"token rejected"}}`, want: true},
		{name: "payment required", status: http.StatusPaymentRequired, body: `{"error":{"message":"payment required"}}`, want: true},
		{name: "entitlement forbidden", status: http.StatusForbidden, body: `{"error":{"message":"subscription tier denied"}}`, want: true},
		{name: "content policy forbidden", status: http.StatusForbidden, body: `{"error":{"code":"content_policy_violation","message":"prompt was rejected by the safety system"}}`, want: false},
		{name: "model not found", status: http.StatusNotFound, body: `{"error":{"message":"model not found"}}`, want: false},
		{name: "request conflict", status: http.StatusConflict, body: `{"error":{"message":"task already completed"}}`, want: false},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"error":{"message":"rate limited"}}`, want: true},
		{name: "internal error", status: http.StatusInternalServerError, body: `{"error":{"message":"temporary failure"}}`, want: true},
		{name: "service unavailable", status: http.StatusServiceUnavailable, body: `{"error":{"message":"temporarily unavailable"}}`, want: true},
		{name: "overloaded", status: 529, body: `{"error":{"message":"overloaded"}}`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, svc.shouldFailoverGrokUpstreamError(tt.status, []byte(tt.body)))
		})
	}
}

func TestGrokAccountCooldownSkipsRequestScopedErrors(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 901, Platform: PlatformGrok, Type: AccountTypeAPIKey}

	svc.handleGrokAccountUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		nil,
		[]byte(`{"error":{"code":"content_policy_violation","message":"blocked by policy"}}`),
	)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.handleGrokAccountUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		nil,
		[]byte(`{"error":{"message":"payment required"}}`),
	)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	svc.ClearAccountSchedulingBlock(account.ID)

	svc.handleGrokAccountUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		nil,
		[]byte(`{"error":{"message":"model not found"}}`),
	)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.handleGrokAccountUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"45"}},
		[]byte(`{"error":{"message":"rate limited"}}`),
	)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.ClearAccountSchedulingBlock(account.ID)
	svc.handleGrokAccountUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		nil,
		[]byte(`{"error":{"message":"temporarily unavailable"}}`),
	)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGrokCapacityWordingOnClientErrorDoesNotBlockModel(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 9252, Platform: PlatformGrok, Type: AccountTypeOAuth}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusBadRequest, nil,
		[]byte(`{"error":{"message":"capacity must be a positive integer"}}`), "grok-4.5",
	)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
}

func TestGrokCapacityDetectionIgnoresUnrelatedJSONFields(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 9254, Platform: PlatformGrok, Type: AccountTypeOAuth}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusServiceUnavailable, nil,
		[]byte(`{"error":{"message":"temporary failure"},"echo":"engine_overloaded"}`), "grok-4.5",
	)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
}

func TestGrokOrdinaryServerErrorStillUsesAccountCooldown(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 9250, Platform: PlatformGrok, Type: AccountTypeOAuth}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusServiceUnavailable, nil,
		[]byte(`{"error":{"message":"temporary upstream failure"}}`), "grok-4.5",
	)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
}

func TestGrokContentPolicyNeverCreatesModelCapacityBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 9251, Platform: PlatformGrok, Type: AccountTypeOAuth}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusForbidden, nil,
		[]byte(`{"error":{"code":"content_policy_violation","message":"server busy wording in user content"}}`), "grok-4.5",
	)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
}

func TestGrokMediaErrorTypesCoverKnownStatuses(t *testing.T) {
	require.Equal(t, "invalid_request_error", grokMediaErrorType(http.StatusBadRequest))
	require.Equal(t, "authentication_error", grokMediaErrorType(http.StatusUnauthorized))
	require.Equal(t, "permission_error", grokMediaErrorType(http.StatusForbidden))
	require.Equal(t, "not_found_error", grokMediaErrorType(http.StatusNotFound))
	require.Equal(t, "conflict_error", grokMediaErrorType(http.StatusConflict))
	require.Equal(t, "rate_limit_error", grokMediaErrorType(http.StatusTooManyRequests))
	require.Equal(t, "upstream_error", grokMediaErrorType(http.StatusServiceUnavailable))
}
