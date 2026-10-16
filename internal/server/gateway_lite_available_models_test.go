package server

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type gatewayLiteAvailableModelsHandlerFunc func(context.Context) []string

func (f gatewayLiteAvailableModelsHandlerFunc) GlobalAvailableModels(ctx context.Context) []string {
	return f(ctx)
}

func TestGatewayLiteSnapshotModelsUsePersistedAccountBatch(t *testing.T) {
	provider := gatewayLiteAvailableModelsProvider{
		staticModels: []string{"static-only"},
		handler: gatewayLiteAvailableModelsHandlerFunc(func(context.Context) []string {
			t.Fatal("snapshot must not requery runtime accounts")
			return nil
		}),
	}
	cooldown := time.Now().Add(time.Hour)
	accounts := []service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, RateLimitResetAt: &cooldown,
			Credentials: map[string]any{"model_mapping": map[string]any{"live-model": "live-model", "grok-4.5": "grok-4.5"}}},
		{ID: 2, Platform: service.PlatformOpenAI, Status: service.StatusDisabled, Schedulable: true,
			Credentials: map[string]any{"model_mapping": map[string]any{"disabled-only": "disabled-only"}}},
		{ID: 3, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: false,
			Credentials: map[string]any{"model_mapping": map[string]any{"unschedulable-only": "unschedulable-only"}}},
	}
	require.Equal(t, []string{"live-model"}, provider.AvailableModelsFromAccounts(accounts))
	fallback := provider.AvailableModelsFromAccounts(nil)
	require.Equal(t, []string{"static-only"}, fallback)
	fallback[0] = "changed"
	require.Equal(t, []string{"static-only"}, provider.staticModels)
	require.Equal(t, []string{"static-only"}, provider.AvailableModelsFromAccounts([]service.Account{{
		ID: 4, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true,
	}}))
}

func TestGatewayLiteAvailableModelsProviderUsesStaticFallbackWithoutHandler(t *testing.T) {
	provider := gatewayLiteAvailableModelsProvider{staticModels: []string{"gpt-static"}}
	require.Equal(t, []string{"gpt-static"}, provider.AvailableModels(context.Background()))
}

func TestGatewayLiteAvailableModelsProviderPrefersDynamicAccountModels(t *testing.T) {
	provider := gatewayLiteAvailableModelsProvider{
		staticModels: []string{"gpt-static", "gpt-shared"},
		handler: gatewayLiteAvailableModelsHandlerFunc(func(context.Context) []string {
			return []string{"gpt-dynamic", "gpt-shared"}
		}),
	}
	require.Equal(t, []string{"gpt-dynamic", "gpt-shared"}, provider.AvailableModels(context.Background()))
}

func TestGatewayLiteAvailableModelsProviderFallsBackWhenNoDynamicAccountModels(t *testing.T) {
	provider := gatewayLiteAvailableModelsProvider{
		staticModels: []string{"gpt-static"},
		handler: gatewayLiteAvailableModelsHandlerFunc(func(context.Context) []string {
			return nil
		}),
	}
	require.Equal(t, []string{"gpt-static"}, provider.AvailableModels(context.Background()))
}
