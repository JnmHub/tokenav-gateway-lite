//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCalculateWebSearchCostDefaultAndOverride(t *testing.T) {
	t.Parallel()
	s := &BillingService{}

	cost := s.CalculateWebSearchCost(1, nil, 1)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.01, cost.ActualCost, 1e-12)
	require.Equal(t, string(BillingModePerRequest), cost.BillingMode)

	cost = s.CalculateWebSearchCost(1, float64Ptr(0.02), 2.5)
	require.InDelta(t, 0.02, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.05, cost.ActualCost, 1e-12)

	cost = s.CalculateWebSearchCost(1, float64Ptr(0), 3)
	require.Zero(t, cost.TotalCost)
	require.Zero(t, cost.ActualCost)

	cost = s.CalculateWebSearchCost(1, nil, -1)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.Zero(t, cost.ActualCost)

	cost = s.CalculateWebSearchCost(0, float64Ptr(0.02), 1)
	require.Zero(t, cost.TotalCost)
	require.Empty(t, cost.BillingMode)
}

func TestCalculateOpenAIRecordUsageCostWebSearchPerCall(t *testing.T) {
	t.Parallel()
	svc := &OpenAIGatewayService{billingService: &BillingService{}}
	groupID := int64(11)
	apiKey := &APIKey{ID: 1, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI}}
	result := &OpenAIForwardResult{Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", WebSearchCalls: 1}

	longContextDisabled := false
	cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), result, apiKey, []string{"gpt-5.6-sol"}, 2, 1, 1, UsageTokens{}, "", &longContextDisabled)
	require.NoError(t, err)
	require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
	require.InDelta(t, 0.01, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.02, cost.ActualCost, 1e-12)

	apiKey.Group.WebSearchPricePerCall = float64Ptr(0.005)
	cost, err = svc.calculateOpenAIRecordUsageCost(context.Background(), result, apiKey, []string{"gpt-5.6-sol"}, 1, 1, 1, UsageTokens{}, "", &longContextDisabled)
	require.NoError(t, err)
	require.InDelta(t, 0.005, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.005, cost.ActualCost, 1e-12)
}

func TestAPIKeyServiceSnapshotRoundTripPreservesWebSearchPrice(t *testing.T) {
	svc := NewAPIKeyService(nil, nil, nil, nil, nil, nil, &config.Config{})
	groupID := int64(9)
	apiKey := &APIKey{
		ID: 1, UserID: 2, GroupID: &groupID, Key: "k-websearch", Status: StatusActive,
		User: &User{ID: 2, Status: StatusActive, Role: RoleUser},
		Group: &Group{
			ID: groupID, Name: "openai", Platform: PlatformOpenAI, Status: StatusActive,
			SubscriptionType: SubscriptionTypeStandard, RateMultiplier: 1,
			WebSearchPricePerCall: float64Ptr(0.008),
		},
	}

	snapshot, err := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.NoError(t, err)
	roundTrip := svc.snapshotToAPIKey(apiKey.Key, snapshot)

	require.NotNil(t, roundTrip)
	require.NotNil(t, roundTrip.Group)
	require.NotNil(t, roundTrip.Group.WebSearchPricePerCall)
	require.InDelta(t, 0.008, *roundTrip.Group.WebSearchPricePerCall, 1e-12)
}
