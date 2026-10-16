package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAILongContextRoutingDecisionBoundaries(t *testing.T) {
	group := &Group{Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true}
	for _, tc := range []struct {
		name       string
		tokens     int
		wantActive bool
	}{
		{name: "below", tokens: 271999, wantActive: false},
		{name: "equal", tokens: 272000, wantActive: false},
		{name: "above", tokens: 272001, wantActive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision := openAILongContextRoutingDecisionForTokens(group, "gpt-5.5", 272000, tc.tokens)
			require.Equal(t, tc.wantActive, decision.Active)
			if tc.wantActive {
				require.True(t, decision.RequireProviderLongContextCost)
			}
		})
	}

	group.AllowOpenAILongContextBilling = false
	decision := openAILongContextRoutingDecisionForTokens(group, "gpt-5.5", 272000, 272001)
	require.True(t, decision.Active)
	require.False(t, decision.RequireProviderLongContextCost)
}

func TestOpenAILongContextRoutingDoesNotFilterOrdinaryRequests(t *testing.T) {
	standard := openAILongContextRoutingTestAccount(1, 9, false, 1)
	providerLong := openAILongContextRoutingTestAccount(2, 9, true, 2)
	ctx := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:               false,
		InputTokenThreshold:  272000,
		EstimatedInputTokens: 272000,
	})

	require.True(t, openAIAccountMatchesLongContextRouting(ctx, &standard))
	require.True(t, openAIAccountMatchesLongContextRouting(ctx, &providerLong))
}

func TestOpenAILongContextRoutingOnlyFiltersWhenGroupRequiresProviderCost(t *testing.T) {
	standard := openAILongContextRoutingTestAccount(1, 9, false, 1)
	providerLong := openAILongContextRoutingTestAccount(2, 9, true, 2)

	longContext := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: true,
	})
	require.False(t, openAIAccountMatchesLongContextRouting(longContext, &standard))
	require.True(t, openAIAccountMatchesLongContextRouting(longContext, &providerLong))

	standardPricing := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: false,
	})
	require.True(t, openAIAccountMatchesLongContextRouting(standardPricing, &standard))
	require.True(t, openAIAccountMatchesLongContextRouting(standardPricing, &providerLong))
}

func TestOpenAILongContextSchedulerBypassesMismatchedStickyAccount(t *testing.T) {
	groupID := int64(9)
	standard := openAILongContextRoutingTestAccount(1, groupID, false, 1)
	providerLong := openAILongContextRoutingTestAccount(2, groupID, true, 9)
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"sticky": standard.ID}}
	svc := openAILongContextRoutingTestService(cache, standard, providerLong)
	ctx := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: true,
	})

	selection, _, err := svc.selectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"sticky",
		"gpt-5.5",
		"",
		nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		"",
		false,
		PlatformOpenAI,
	)

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, providerLong.ID, selection.Account.ID)
	reboundID, reboundErr := svc.getStickySessionAccountID(ctx, &groupID, "sticky")
	require.NoError(t, reboundErr)
	require.Equal(t, providerLong.ID, reboundID)
}

func TestOpenAILongContextSchedulerAllowsProviderCostAccountWhenGroupClosed(t *testing.T) {
	groupID := int64(9)
	providerLong := openAILongContextRoutingTestAccount(2, groupID, true, 1)
	svc := openAILongContextRoutingTestService(&schedulerTestGatewayCache{}, providerLong)
	ctx := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: false,
	})

	selection, _, err := svc.selectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.5", "", nil,
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, "", false, PlatformOpenAI,
	)

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, providerLong.ID, selection.Account.ID)
}

func TestOpenAILongContextSchedulerFailoverStaysWithinCostClass(t *testing.T) {
	groupID := int64(9)
	standard := openAILongContextRoutingTestAccount(1, groupID, false, 1)
	firstProviderLong := openAILongContextRoutingTestAccount(2, groupID, true, 1)
	secondProviderLong := openAILongContextRoutingTestAccount(3, groupID, true, 2)
	svc := openAILongContextRoutingTestService(
		&schedulerTestGatewayCache{},
		standard,
		firstProviderLong,
		secondProviderLong,
	)
	ctx := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: true,
	})

	selection, _, err := svc.selectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.5",
		"",
		map[int64]struct{}{firstProviderLong.ID: {}},
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		"",
		false,
		PlatformOpenAI,
	)

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, secondProviderLong.ID, selection.Account.ID)

	selection, _, err = svc.selectAccountWithScheduler(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.5",
		"",
		map[int64]struct{}{firstProviderLong.ID: {}, secondProviderLong.ID: {}},
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		"",
		false,
		PlatformOpenAI,
	)

	require.Nil(t, selection)
	require.Error(t, err)
	status, _, code, _, ok := OpenAILongContextRoutingErrorDetails(err)
	require.True(t, ok)
	require.Equal(t, 503, status)
	require.Equal(t, OpenAILongContextBillingAccountUnavailableCode, code)
}

func TestOpenAILongContextSchedulerReturnsExplicitNoMatchError(t *testing.T) {
	groupID := int64(9)
	standard := openAILongContextRoutingTestAccount(1, groupID, false, 1)
	svc := openAILongContextRoutingTestService(&schedulerTestGatewayCache{}, standard)
	ctx := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: true,
	})

	selection, _, err := svc.selectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.5", "", nil,
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, "", false, PlatformOpenAI,
	)

	require.Nil(t, selection)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.False(t, errors.Is(err, ErrGatewayGroupUnavailable))
	status, _, code, _, ok := OpenAILongContextRoutingErrorDetails(err)
	require.True(t, ok)
	require.Equal(t, 503, status)
	require.Equal(t, OpenAILongContextBillingAccountUnavailableCode, code)
}

func TestOpenAILongContextTemporaryBlockDoesNotMutateGroupPolicy(t *testing.T) {
	groupID := int64(9)
	group := &Group{ID: groupID, Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true}
	providerLong := openAILongContextRoutingTestAccount(2, groupID, true, 1)
	svc := openAILongContextRoutingTestService(&schedulerTestGatewayCache{}, providerLong)
	svc.BlockAccountScheduling(&providerLong, maxTime(), "temporary_429")
	ctx := withOpenAILongContextRoutingDecision(context.Background(), OpenAILongContextRoutingDecision{
		Active:                         true,
		RequireProviderLongContextCost: true,
	})

	selection, _, err := svc.selectAccountWithScheduler(
		ctx, &groupID, "", "", "gpt-5.5", "", nil,
		OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, "", false, PlatformOpenAI,
	)

	require.Nil(t, selection)
	require.Error(t, err)
	require.True(t, group.AllowOpenAILongContextBilling)
}

func TestResolveOpenAILongContextThresholdUsesStructuredPricing(t *testing.T) {
	svc := &OpenAIGatewayService{}
	model, threshold := svc.resolveOpenAILongContextThreshold("unknown-alias", "gpt-5.5")
	require.Equal(t, "gpt-5.5", model)
	require.Equal(t, 272000, threshold)
}

func TestPrepareOpenAILongContextRoutingReturnsTypedEstimateError(t *testing.T) {
	svc := &OpenAIGatewayService{}
	group := &Group{Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true}
	_, _, err := svc.PrepareOpenAILongContextRouting(
		context.Background(), group, PlatformOpenAI, []byte(`{"input":`), "gpt-5.5",
	)

	require.Error(t, err)
	status, _, code, _, ok := OpenAILongContextRoutingErrorDetails(err)
	require.True(t, ok)
	require.Equal(t, 400, status)
	require.Equal(t, OpenAILongContextInputEstimateFailedCode, code)
}

func TestOpenAILongContextInputEstimateSupportsWebSocketEnvelope(t *testing.T) {
	direct, err := estimateOpenAIInputTokensFromRequestBody("gpt-5.5", []byte(`{"input":"hello world"}`))
	require.NoError(t, err)
	nested, err := estimateOpenAIInputTokensFromRequestBody("gpt-5.5", []byte(`{"type":"response.create","response":{"input":"hello world"}}`))
	require.NoError(t, err)
	require.Equal(t, direct, nested)
}

func TestOpenAILongContextInputEstimateCountsAnthropicSystemPrompt(t *testing.T) {
	withoutSystem, err := estimateOpenAIInputTokensFromRequestBody("gpt-5.5", []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	withSystem, err := estimateOpenAIInputTokensFromRequestBody("gpt-5.5", []byte(`{"system":"follow these extra system instructions","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.Greater(t, withSystem, withoutSystem)
}

func TestShouldApplyOpenAILongContextBillingRequiresGroupPermission(t *testing.T) {
	account := openAILongContextRoutingTestAccount(1, 9, true, 1)
	require.True(t, shouldApplyOpenAILongContextBilling(&APIKey{}, &account), "standalone mode keeps account-level behavior")
	require.False(t, shouldApplyOpenAILongContextBilling(&APIKey{Group: &Group{Platform: PlatformOpenAI}}, &account))
	require.True(t, shouldApplyOpenAILongContextBilling(&APIKey{Group: &Group{Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true}}, &account))
}

func openAILongContextRoutingTestAccount(id, groupID int64, providerLongContext bool, priority int) Account {
	return Account{
		ID:          id,
		Name:        "account",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Priority:    priority,
		GroupIDs:    []int64{groupID},
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
		},
		Extra: map[string]any{openAILongContextBillingEnabledKey: providerLongContext},
	}
}

func openAILongContextRoutingTestService(cache GatewayCache, accounts ...Account) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	repo := schedulerGroupAwareOpenAIAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
	return &OpenAIGatewayService{
		accountRepo:        repo,
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}
}

func maxTime() time.Time {
	return time.Now().Add(10 * time.Minute)
}
