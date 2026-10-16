package server

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type configPushFreshModelsRepo struct {
	service.AccountRepository
	accounts         []service.Account
	listCalls        int
	schedulableCalls int
	clearAfterList   bool
}

func (r *configPushFreshModelsRepo) ListSchedulable(context.Context) ([]service.Account, error) {
	r.schedulableCalls++
	return append([]service.Account(nil), r.accounts...), nil
}

func (r *configPushFreshModelsRepo) List(_ context.Context, _ pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error) {
	r.listCalls++
	accounts := append([]service.Account(nil), r.accounts...)
	if r.clearAfterList {
		r.accounts = nil
	}
	return accounts, nil, nil
}

func TestGatewayLiteConfigPushFreshAccountPrices(t *testing.T) {
	account := func(id, group int64, model string) service.Account {
		return service.Account{
			ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{group},
			Credentials: map[string]any{"model_mapping": map[string]any{model: model}},
		}
	}
	old := account(1, 41, "e2e-old")
	media := account(2, 42, "e2e-seedance")
	for _, tc := range []struct {
		name           string
		before         []service.Account
		after          []service.Account
		priced         bool
		clearAfterList bool
	}{
		{name: "added", before: []service.Account{old}, after: []service.Account{old, media}, priced: true},
		{name: "removed", before: []service.Account{old, media}, after: []service.Account{old}},
		{name: "same_batch", before: []service.Account{old}, after: []service.Account{old, media}, priced: true, clearAfterList: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := &configPushFreshModelsRepo{accounts: tc.before}
			svc := service.NewGatewayService(repo,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := handler.NewGatewayHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			cached := svc.GetAvailableModels(ctx, nil, "")
			require.Contains(t, cached, "e2e-old")
			repo.accounts = tc.after
			repo.clearAfterList = tc.clearAfterList

			pricing := service.NewPricingService(&config.Config{Pricing: config.PricingConfig{DataDir: t.TempDir()}}, nil)
			_, err := pricing.UpsertModelPricingOverride("e2e-seedance", service.LiteLLMModelPricing{
				LiteLLMProvider: "openai", Mode: "chat", BillingMode: "token",
				InputCostPerToken: 5e-6, OutputCostPerToken: 30e-6,
			})
			require.NoError(t, err)
			provider := gatewayLiteConfigPushSnapshotProvider{
				accounts: repo,
				groups: configPushGroupListerFunc(func(context.Context, pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
					return []service.Group{{ID: 42, Platform: service.PlatformOpenAI, Status: service.StatusActive,
						RateMultiplier: 2, BillingModelSource: service.BillingModelSourceRequested,
						ModelsListConfig: service.GroupModelsListConfig{Enabled: true, Models: []string{"e2e-seedance"}}}}, nil, nil
				}),
				models: gatewayLiteAvailableModelsProvider{handler: h},
				prices: gatewayLiteModelPriceProvider{pricingService: pricing},
			}
			snapshot, err := provider.BuildGatewayConfigPushSnapshot(ctx, "local-test")
			require.NoError(t, err)
			require.Len(t, snapshot.Accounts, len(tc.after))
			require.Len(t, snapshot.Groups, 1)
			group := snapshot.Groups[0]
			require.True(t, group.ModelPricesReported)
			if tc.priced {
				require.Equal(t, []string{"e2e-seedance"}, group.EffectiveModels)
				require.Len(t, group.ModelPrices, 1)
				require.Equal(t, "e2e-seedance", group.ModelPrices[0].Model)
				require.Equal(t, "token", group.ModelPrices[0].BillingMode)
				require.Equal(t, 5.0, group.ModelPrices[0].InputCostPer1MTokens)
				require.Equal(t, 30.0, group.ModelPrices[0].OutputCostPer1MTokens)
			} else {
				require.Empty(t, group.EffectiveModels)
				require.Empty(t, group.ModelPrices)
				require.Empty(t, snapshot.ModelPrices)
			}
			require.Equal(t, cached, svc.GetAvailableModels(ctx, nil, ""))
			require.Equal(t, 1, repo.listCalls)
			require.Equal(t, 1, repo.schedulableCalls, "snapshot must reuse its own account batch")
		})
	}
}
