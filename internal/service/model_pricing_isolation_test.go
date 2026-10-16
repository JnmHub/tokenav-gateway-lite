package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/stretchr/testify/require"
)

func TestGetModelPricingWithChannelDoesNotMutateSharedFallback(t *testing.T) {
	base := &ModelPricing{
		InputPricePerToken:            1,
		OutputPricePerToken:           2,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   3,
		LongContextInputPricePerToken: 2,
	}
	svc := &BillingService{fallbackPrices: map[string]*ModelPricing{"claude-sonnet-4": base}}
	override := 9.0

	groupPricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", &ChannelModelPricing{InputPrice: &override})
	require.NoError(t, err)
	require.Equal(t, 9.0, groupPricing.InputPricePerToken)
	require.Equal(t, 18.0, groupPricing.LongContextInputPricePerToken)
	require.Equal(t, 1.0, base.InputPricePerToken)
	require.Equal(t, 2.0, base.LongContextInputPricePerToken)

	fallbackPricing, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)
	require.Same(t, base, fallbackPricing)
	require.Equal(t, 1.0, fallbackPricing.InputPricePerToken)
}

func TestGetModelPricingWithChannelConcurrentOverridesAreIsolated(t *testing.T) {
	base := &ModelPricing{InputPricePerToken: 1, OutputPricePerToken: 2}
	svc := &BillingService{fallbackPrices: map[string]*ModelPricing{"claude-sonnet-4": base}}

	const workers = 64
	errors := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 1; i <= workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			override := float64(i)
			pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", &ChannelModelPricing{InputPrice: &override})
			if err != nil {
				errors <- err
				return
			}
			if pricing.InputPricePerToken != override || pricing.OutputPricePerToken != 2 {
				errors <- fmt.Errorf("worker %d observed input=%v output=%v", i, pricing.InputPricePerToken, pricing.OutputPricePerToken)
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 1.0, base.InputPricePerToken)
	require.Equal(t, 2.0, base.OutputPricePerToken)
}

func TestModelPricingResolverGroupOverridesAreIsolated(t *testing.T) {
	base := &ModelPricing{
		InputPricePerToken:            1,
		ImageOutputPricePerToken:      4,
		LongContextInputMultiplier:    2,
		LongContextInputPricePerToken: 2,
	}
	resolver := &ModelPricingResolver{}
	groupOne := &ResolvedPricing{BasePricing: base}
	groupTwo := &ResolvedPricing{BasePricing: base}
	override := 7.0

	resolver.applyTokenOverrides(&ChannelModelPricing{InputPrice: &override}, groupOne)

	require.NotSame(t, base, groupOne.BasePricing)
	require.Equal(t, 7.0, groupOne.BasePricing.InputPricePerToken)
	require.Equal(t, 14.0, groupOne.BasePricing.LongContextInputPricePerToken)
	require.Same(t, base, groupTwo.BasePricing)
	require.Equal(t, 1.0, groupTwo.BasePricing.InputPricePerToken)
	require.Equal(t, 4.0, groupTwo.BasePricing.ImageOutputPricePerToken)
}

func TestModelPricingResolverIntervalFallbackIsIsolated(t *testing.T) {
	base := &ModelPricing{InputPricePerToken: 1, ImageOutputPricePerToken: 4}
	resolver := &ModelPricingResolver{}
	resolved := &ResolvedPricing{BasePricing: base}
	intervalPrice := 3.0

	resolver.applyTokenOverrides(&ChannelModelPricing{
		Intervals: []PricingInterval{{MinTokens: 0, InputPrice: &intervalPrice}},
	}, resolved)

	require.NotSame(t, base, resolved.BasePricing)
	require.Equal(t, 0.0, resolved.BasePricing.ImageOutputPricePerToken)
	require.Equal(t, 4.0, base.ImageOutputPricePerToken)
}

func TestCrossGroupPricingIsolationKeepsCostLogDeductionAndReportConsistent(t *testing.T) {
	const model = "gpt-5.6-luna"
	const (
		groupOneID  int64 = 7101
		groupTwoID  int64 = 7102
		baseGroupID int64 = 7103
	)

	billing := NewBillingService(&config.Config{}, nil)
	cache := newEmptyChannelCache()
	groupOnePricing := ChannelModelPricing{
		Models:      []string{model},
		BillingMode: BillingModeToken,
		InputPrice:  testPtrFloat64(2e-6),
		OutputPrice: testPtrFloat64(8e-6),
	}
	groupTwoPricing := ChannelModelPricing{
		Models:      []string{model},
		BillingMode: BillingModeToken,
		InputPrice:  testPtrFloat64(3e-6),
		OutputPrice: testPtrFloat64(10e-6),
	}
	cache.pricingByGroupModel[channelModelKey{groupID: groupOneID, platform: PlatformOpenAI, model: model}] = &groupOnePricing
	cache.pricingByGroupModel[channelModelKey{groupID: groupTwoID, platform: PlatformOpenAI, model: model}] = &groupTwoPricing
	cache.channelByGroupID[groupOneID] = &Channel{ID: 8101, Status: StatusActive}
	cache.channelByGroupID[groupTwoID] = &Channel{ID: 8102, Status: StatusActive}
	cache.groupPlatform[groupOneID] = PlatformOpenAI
	cache.groupPlatform[groupTwoID] = PlatformOpenAI
	cache.loadedAt = time.Now()
	channelService := &ChannelService{}
	channelService.cache.Store(cache)
	resolver := NewModelPricingResolver(channelService, billing)

	tokens := UsageTokens{InputTokens: 100_000, OutputTokens: 10_000}
	wantCost := map[int64]float64{
		groupOneID:  0.28,
		groupTwoID:  0.40,
		baseGroupID: 0.032,
	}
	wantCents := map[int64]int64{
		groupOneID:  28,
		groupTwoID:  40,
		baseGroupID: 3,
	}

	const workers = 90
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	groupIDs := []int64{groupOneID, groupTwoID, baseGroupID}
	for i := 0; i < workers; i++ {
		groupID := groupIDs[i%len(groupIDs)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			cost, err := billing.CalculateCostUnified(CostInput{
				Ctx:            context.Background(),
				Model:          model,
				GroupID:        &groupID,
				Tokens:         tokens,
				RateMultiplier: 1,
				Resolver:       resolver,
			})
			if err != nil {
				errCh <- err
				return
			}
			if diff := cost.ActualCost - wantCost[groupID]; diff < -1e-12 || diff > 1e-12 {
				errCh <- fmt.Errorf("group %d actual cost=%v want=%v", groupID, cost.ActualCost, wantCost[groupID])
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	for index, groupID := range groupIDs {
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		userRepo := &openAIRecordUsageUserRepoStub{}
		svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
		svc.billingService = billing
		svc.resolver = resolver
		svc.channelService = channelService
		svc.userGroupRateResolver = nil

		err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result: &OpenAIForwardResult{
				RequestID: fmt.Sprintf("cross_group_price_%d", groupID),
				Model:     model,
				Usage: OpenAIUsage{
					InputTokens:  tokens.InputTokens,
					OutputTokens: tokens.OutputTokens,
				},
				Duration: time.Second,
			},
			APIKey: &APIKey{
				ID:      int64(9100 + index),
				GroupID: i64p(groupID),
				Group:   &Group{ID: groupID, RateMultiplier: 1},
			},
			User:    &User{ID: int64(9200 + index)},
			Account: &Account{ID: int64(9300 + index)},
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		require.InDelta(t, wantCost[groupID], usageRepo.lastLog.ActualCost, 1e-12)
		require.InDelta(t, wantCost[groupID], userRepo.lastAmount, 1e-12)
		require.NotNil(t, usageRepo.lastLog.GroupID)
		require.Equal(t, groupID, *usageRepo.lastLog.GroupID)

		reportCtx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
			RequestID: fmt.Sprintf("cross_group_report_%d", groupID),
			KeyID:     fmt.Sprintf("key_%d", groupID),
			UserID:    int64(9200 + index),
			Region:    "test",
		})
		event, ok := gatewayLiteUsageCorrectionEvent(reportCtx, usageRepo.lastLog)
		require.True(t, ok)
		require.Equal(t, groupID, event.GatewayGroupID)
		require.EqualValues(t, tokens.InputTokens, event.TokensIn)
		require.EqualValues(t, tokens.OutputTokens, event.TokensOut)
		require.EqualValues(t, wantCents[groupID], event.ActualCents)
	}
}
