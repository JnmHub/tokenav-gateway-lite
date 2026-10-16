//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func orderedResponseModels(t *testing.T, billing *BillingService, tokens UsageTokens, first, second string) (cheaper, pricier string, cheapCost, priceyCost *CostBreakdown) {
	t.Helper()
	firstCost, err := billing.CalculateCost(first, tokens, 1.1)
	require.NoError(t, err)
	secondCost, err := billing.CalculateCost(second, tokens, 1.1)
	require.NoError(t, err)
	require.NotEqual(t, firstCost.TotalCost, secondCost.TotalCost)
	require.True(t, billing.HasIdentifiedTokenPricing(first))
	require.True(t, billing.HasIdentifiedTokenPricing(second))
	if firstCost.TotalCost < secondCost.TotalCost {
		return first, second, firstCost, secondCost
	}
	return second, first, secondCost, firstCost
}

func TestResponseModelBillingDeclarationAndAdoptionGuards(t *testing.T) {
	require.Equal(t, "gpt-5.4-nano", responseModelBillingDeclaration(BillingModelSourceResponse, " gpt-5.4-nano ", false, false))
	require.Empty(t, responseModelBillingDeclaration(BillingModelSourceRequested, "gpt-5.4-nano", false, false))
	require.Empty(t, responseModelBillingDeclaration(BillingModelSourceResponse, "gpt-5.4-nano", true, false))
	require.Empty(t, responseModelBillingDeclaration(BillingModelSourceResponse, "gpt-5.4-nano", false, true))

	cost := func(value float64) *CostBreakdown { return &CostBreakdown{TotalCost: value} }
	require.True(t, responseModelBillingAdoptable(cost(1), cost(0.5), false, false))
	require.False(t, responseModelBillingAdoptable(cost(1), cost(1.1), false, false))
	require.False(t, responseModelBillingAdoptable(cost(1), cost(0), false, false))
	require.False(t, responseModelBillingAdoptable(cost(1), cost(0.5), true, false))
	require.True(t, responseModelBillingAdoptable(cost(1), cost(0.5), true, true))
}

func TestBillingServiceIdentifiedPricingRejectsFamilyGuess(t *testing.T) {
	billing := newGatewayRecordUsageServiceForTest(&openAIRecordUsageLogRepoStub{}, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}).billingService
	require.True(t, billing.HasIdentifiedTokenPricing("claude-sonnet-4"))
	require.True(t, billing.HasIdentifiedTokenPricing("gpt-5.4-nano"))
	require.False(t, billing.HasIdentifiedTokenPricing("totally-made-up-haiku-v9"))
}

func TestGatewayResponseModelBillingOnlyAdoptsCheaperSafeModel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	cheaper, pricier, cheapCost, _ := orderedResponseModels(t, svc.billingService, tokens, "claude-sonnet-4", "claude-opus-4.8")

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:             "gateway-response-model-cheaper",
			Usage:                 ClaudeUsage{InputTokens: 100, OutputTokens: 50},
			Model:                 pricier,
			UpstreamModel:         pricier,
			UpstreamResponseModel: cheaper,
			Duration:              time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel: pricier, ChannelMappedModel: pricier, BillingModelSource: BillingModelSourceResponse,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, cheapCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, cheapCost.ActualCost, userRepo.lastAmount, 1e-12)
}

func TestGatewayResponseModelBillingRejectsPricierAndUnidentifiedModels(t *testing.T) {
	tests := []struct {
		name       string
		responseFn func(cheaper, pricier string) string
	}{
		{name: "pricier", responseFn: func(_, pricier string) string { return pricier }},
		{name: "family_guess", responseFn: func(_, _ string) string { return "totally-made-up-haiku-v9" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
			tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
			cheaper, pricier, baselineCost, _ := orderedResponseModels(t, svc.billingService, tokens, "claude-sonnet-4", "claude-opus-4.8")

			err := svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result: &ForwardResult{
					RequestID:             "gateway-response-model-rejected-" + test.name,
					Usage:                 ClaudeUsage{InputTokens: 100, OutputTokens: 50},
					Model:                 cheaper,
					UpstreamModel:         cheaper,
					UpstreamResponseModel: test.responseFn(cheaper, pricier),
					Duration:              time.Second,
				},
				APIKey:  &APIKey{ID: 501, Quota: 100},
				User:    &User{ID: 601},
				Account: &Account{ID: 701},
				ChannelUsageFields: ChannelUsageFields{
					OriginalModel: cheaper, ChannelMappedModel: cheaper, BillingModelSource: BillingModelSourceResponse,
				},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.InDelta(t, baselineCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
			require.InDelta(t, baselineCost.ActualCost, userRepo.lastAmount, 1e-12)
		})
	}
}

func TestOpenAIResponseModelBillingFallbacks(t *testing.T) {
	tests := []struct {
		name       string
		conflict   bool
		media      bool
		responseFn func(string, string) string
	}{
		{name: "pricier", responseFn: func(_, pricier string) string { return pricier }},
		{name: "conflict", conflict: true, responseFn: func(cheaper, _ string) string { return cheaper }},
		{name: "media", media: true, responseFn: func(cheaper, _ string) string { return cheaper }},
		{name: "unknown", responseFn: func(_, _ string) string { return "totally-made-up-haiku-v9" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			userRepo := &openAIRecordUsageUserRepoStub{}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
			tokens := UsageTokens{InputTokens: 20, OutputTokens: 10}
			cheaper, pricier, cheapCost, _ := orderedResponseModels(t, svc.billingService, tokens, "gpt-5.4-nano", "gpt-5.5")
			baseline := cheaper
			result := &OpenAIForwardResult{
				RequestID:                     "openai-response-model-" + test.name,
				Model:                         baseline,
				UpstreamModel:                 baseline,
				UpstreamResponseModel:         test.responseFn(cheaper, pricier),
				UpstreamResponseModelConflict: test.conflict,
				Usage:                         OpenAIUsage{InputTokens: 20, OutputTokens: 10},
				Duration:                      time.Second,
			}
			if test.media {
				result.SearchCount = 1
			}
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: result, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: &Account{ID: 30},
				ChannelUsageFields: ChannelUsageFields{OriginalModel: baseline, ChannelMappedModel: baseline, BillingModelSource: BillingModelSourceResponse},
			})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.GreaterOrEqual(t, usageRepo.lastLog.ActualCost, cheapCost.ActualCost)
		})
	}
}

func TestResponseModelBillingAdoptionGuardsNullAndPricingSources(t *testing.T) {
	cost := func(value float64) *CostBreakdown { return &CostBreakdown{TotalCost: value, ActualCost: value} }
	require.False(t, responseModelBillingAdoptable(nil, cost(0.5), false, false))
	require.False(t, responseModelBillingAdoptable(cost(1), nil, false, false))
	require.True(t, responseModelBillingAdoptable(cost(1), cost(1+1e-13), false, false))
	require.False(t, responseModelBillingAdoptable(cost(1), cost(-1), false, false))
	require.True(t, responseModelBillingAdoptable(cost(0), cost(0), false, false))
	require.True(t, responseModelBillingAdoptable(cost(1), cost(0.5), false, true))
}

func TestGatewayResponseModelBillingSkippedForSearchSurcharge(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	cheaper, pricier, _, baselineCost := orderedResponseModels(t, svc.billingService, tokens, "claude-sonnet-4", "claude-opus-4.8")
	const searchCount = 2
	searchCost := svc.billingService.CalculateSearchCost(searchCount, nil, 1.1)
	require.NotNil(t, searchCost)
	require.Greater(t, searchCost.ActualCost, 0.0)

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:             "gateway-response-model-search-surcharge",
			Usage:                 ClaudeUsage{InputTokens: 100, OutputTokens: 50},
			Model:                 pricier,
			UpstreamModel:         pricier,
			UpstreamResponseModel: cheaper,
			SearchCount:           searchCount,
			Duration:              time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel: pricier, ChannelMappedModel: pricier, BillingModelSource: BillingModelSourceResponse,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	want := baselineCost.ActualCost + searchCost.ActualCost
	require.InDelta(t, want, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, want, userRepo.lastAmount, 1e-12)
}
