package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayService_SelectAccountWithScheduler_UsesGroupFallbackModel(t *testing.T) {
	groupID := int64(47001)
	account := &Account{
		ID:          47002,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 4,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"},
		},
	}
	snapshot := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{account},
		accountsByID:     map[int64]*Account{account.ID: account},
		groups: map[int64]*Group{
			groupID: {
				ID:            groupID,
				Name:          "fallback-test",
				Platform:      PlatformOpenAI,
				Status:        StatusActive,
				FallbackModel: "gpt-5.6-sol",
			},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		cfg: cfg,
		schedulerSnapshot: NewSchedulerSnapshotService(
			snapshot,
			nil,
			nil,
			nil,
			cfg,
		),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	ctx := WithOpenAIGroupModelFallbackEnabled(context.Background())
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.6-luna",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Equal(t, "gpt-5.6-sol", selection.ScheduledModel)
	require.True(t, selection.GroupFallbackUsed)
	require.Equal(t, "gpt-5.6-luna", selection.GroupFallbackSource)
	require.Equal(t, "gpt-5.6-sol", selection.GroupFallbackModel)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_PreservesAvailableRequestedModel(t *testing.T) {
	groupID := int64(47006)
	account := &Account{
		ID:          47007,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 4,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.6-luna": "gpt-5.6-luna",
				"gpt-5.6-sol":  "gpt-5.6-sol",
			},
		},
	}
	snapshot := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{account},
		accountsByID:     map[int64]*Account{account.ID: account},
		groups: map[int64]*Group{
			groupID: {ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, FallbackModel: "gpt-5.6-sol"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		cfg:               cfg,
		schedulerSnapshot: NewSchedulerSnapshotService(snapshot, nil, nil, nil, cfg),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		WithOpenAIGroupModelFallbackEnabled(context.Background()),
		&groupID,
		"",
		"",
		"gpt-5.6-luna",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, "gpt-5.6-luna", selection.ScheduledModel)
	require.False(t, selection.GroupFallbackUsed)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_RequiresHandlerOptIn(t *testing.T) {
	groupID := int64(47008)
	account := &Account{
		ID:          47009,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 4,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"},
		},
	}
	snapshot := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{account},
		accountsByID:     map[int64]*Account{account.ID: account},
		groups: map[int64]*Group{
			groupID: {ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, FallbackModel: "gpt-5.6-sol"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		cfg:               cfg,
		schedulerSnapshot: NewSchedulerSnapshotService(snapshot, nil, nil, nil, cfg),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		context.Background(),
		&groupID,
		"",
		"",
		"gpt-5.6-luna",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses,
		false,
	)
	require.Error(t, err)
	require.Nil(t, selection)
}

func TestOpenAIGatewayService_SelectAccountWithScheduler_DoesNotFallbackContinuationRequests(t *testing.T) {
	groupID := int64(47011)
	account := &Account{
		ID:          47012,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 4,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"},
		},
	}
	snapshot := &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{account},
		accountsByID:     map[int64]*Account{account.ID: account},
		groups: map[int64]*Group{
			groupID: {ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, FallbackModel: "gpt-5.6-sol"},
		},
	}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{
		cfg: cfg,
		schedulerSnapshot: NewSchedulerSnapshotService(snapshot, nil, nil, nil, cfg),
		rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("true"),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		WithOpenAIGroupModelFallbackEnabled(context.Background()),
		&groupID,
		"resp_existing",
		"",
		"gpt-5.6-luna",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses,
		false,
	)
	require.Error(t, err)
	require.Nil(t, selection)
	require.False(t, openAIGroupModelFallbackActiveForRequest(context.Background()))
}

func TestWithoutOpenAIGroupModelFallbackPreservesAttemptedMarkerWithoutTarget(t *testing.T) {
	ctx := WithOpenAIGroupModelFallback(context.Background(), 47021, "gpt-5.6-luna", "gpt-5.6-luna", "gpt-5.6-sol")
	require.True(t, openAIGroupModelFallbackActiveForRequest(ctx))
	cleared := WithoutOpenAIGroupModelFallback(ctx)
	require.True(t, openAIGroupModelFallbackActiveForRequest(cleared))
	_, ok := openAIGroupModelFallbackTargetForModel(cleared, 47021, "gpt-5.6-luna")
	require.False(t, ok, "the next failover account must use the original model")
	clearedAgain := WithoutOpenAIGroupModelFallback(cleared)
	require.Same(t, cleared, clearedAgain, "clearing an already-cleared target should avoid context churn")
}
