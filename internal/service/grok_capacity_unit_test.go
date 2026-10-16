//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokModelCapacityBlocksOnlyObservedModel(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "engine overloaded", body: `{"error":{"message":"engine_overloaded"}}`},
		{name: "model capacity", body: `{"error":{"code":"model_capacity","message":"model capacity exhausted"}}`},
		{name: "plain overloaded", body: `{"error":{"message":"overloaded"}}`},
		{name: "server busy", body: `{"error":{"message":"server_busy"}}`},
		{name: "concurrency capacity", body: `{"error":{"message":"too many concurrent requests for this model"}}`},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &grokQuotaAccountRepo{}
			svc := &OpenAIGatewayService{accountRepo: repo}
			account := &Account{ID: int64(9200 + index), Platform: PlatformGrok, Type: AccountTypeOAuth}

			svc.handleGrokAccountUpstreamError(
				context.Background(), account, http.StatusTooManyRequests,
				http.Header{"Retry-After": []string{"60"}}, []byte(tt.body), "grok-4.5",
			)

			require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
			require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.6"))
			require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
			require.Zero(t, repo.rateLimitedCalls)
			require.Zero(t, repo.tempUnschedCalls)
			require.Equal(t, 1, repo.updateCalls)
		})
	}
}

func TestGrokPoolModeCapacityPersistsSnapshotWithoutLocalCooldown(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{
		ID: 9253, Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"pool_mode": true},
	}

	svc.handleGrokAccountUpstreamError(
		context.Background(), account, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"60"}},
		[]byte(`{"error":{"message":"engine_overloaded"}}`), "grok-4.5",
	)

	require.Equal(t, 1, repo.updateCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.tempUnschedCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
}

func TestGrokHTTPFailoverPolicyKeepsCapacityModelScoped(t *testing.T) {
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{accountRepo: repo}
	account := &Account{ID: 17104, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"model_capacity","message":"engine_overloaded"}}`)

	err := svc.buildOpenAIHTTPFailoverError(
		context.Background(), nil, account, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"60"}}, body, "engine_overloaded", "grok-4.5", true,
	)

	require.NotNil(t, err)
	require.True(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.5"))
	require.False(t, svc.isOpenAIAccountModelRuntimeBlocked(account, "grok-4.6"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Zero(t, repo.rateLimitedCalls)
}
