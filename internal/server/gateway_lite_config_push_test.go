package server

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type configPushAccountListerFunc func(context.Context, pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error)

func (f configPushAccountListerFunc) List(ctx context.Context, params pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error) {
	return f(ctx, params)
}

type configPushGroupListerFunc func(context.Context, pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error)

func (f configPushGroupListerFunc) List(ctx context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
	return f(ctx, params)
}

type configPushModelProviderFunc func(context.Context) []string

func (f configPushModelProviderFunc) AvailableModelsFromAccounts(_ []service.Account) []string {
	return f(context.Background())
}

type configPushPriceProviderFunc func([]string, string) []gatewaylite.GatewayModelPrice

func (f configPushPriceProviderFunc) GatewayModelPrices(models []string, gatewayCode string) []gatewaylite.GatewayModelPrice {
	return f(models, gatewayCode)
}

type configPushRuntimeSettingsProviderFunc func(context.Context) (*service.OpenAIFastPolicySettings, error)

func (f configPushRuntimeSettingsProviderFunc) GetOpenAIFastPolicySettings(ctx context.Context) (*service.OpenAIFastPolicySettings, error) {
	return f(ctx)
}

func TestGatewayLiteConfigPushSnapshotIsCompleteAndRedacted(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	rate := 1.25
	load := 4
	video480 := 0.08
	video720 := 0.14
	video1080 := 0.25
	webSearch := 0.006
	provider := gatewayLiteConfigPushSnapshotProvider{
		accounts: configPushAccountListerFunc(func(_ context.Context, params pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error) {
			require.Equal(t, gatewayLiteConfigPushPageSize, params.PageSize)
			switch params.Page {
			case 1:
				return []service.Account{{
					ID:       10,
					Name:     "Grok OAuth",
					Platform: service.PlatformGrok,
					Type:     service.AccountTypeOAuth,
					Credentials: map[string]any{
						"access_token":  "secret-access",
						"refresh_token": "secret-refresh",
						"model_mapping": map[string]any{
							"grok-4.5":               "grok-4.5",
							"grok-imagine-video-1.5": "grok-imagine-video-1.5",
						},
						"metadata": map[string]any{
							"client_secret": "nested-secret",
							"tenant":        "xai",
						},
					},
					Extra: map[string]any{
						"quota_status": "available",
						"nested": map[string]any{
							"authorization": "Bearer secret",
							"plan":          "pro",
						},
					},
					Concurrency:    2,
					Priority:       100,
					RateMultiplier: &rate,
					LoadFactor:     &load,
					Status:         service.StatusActive,
					Schedulable:    true,
					UpdatedAt:      now,
					GroupIDs:       []int64{20},
				}}, &pagination.PaginationResult{Page: 1, PageSize: params.PageSize, Pages: 2, Total: 2}, nil
			case 2:
				return []service.Account{{ID: 11, Name: "Grok API", Platform: service.PlatformGrok, Type: service.AccountTypeAPIKey, Status: service.StatusDisabled, UpdatedAt: now}}, &pagination.PaginationResult{Page: 2, PageSize: params.PageSize, Pages: 2, Total: 2}, nil
			default:
				t.Fatalf("unexpected account page %d", params.Page)
				return nil, nil, nil
			}
		}),
		groups: configPushGroupListerFunc(func(_ context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
			return []service.Group{{
				ID:                    20,
				Name:                  "Grok Media",
				Platform:              service.PlatformGrok,
				Status:                service.StatusActive,
				RateMultiplier:        1.5,
				PeakRateEnabled:       true,
				PeakStart:             "22:00",
				PeakEnd:               "06:00",
				PeakRateMultiplier:    1.6,
				PeakTimezone:          "Asia/Tokyo",
				AllowImageGeneration:  true,
				VideoRateIndependent:  true,
				VideoRateMultiplier:   1.2,
				VideoPrice480P:        &video480,
				VideoPrice720P:        &video720,
				VideoPrice1080P:       &video1080,
				WebSearchPricePerCall: &webSearch,
				ModelsListConfig: service.GroupModelsListConfig{
					Enabled: true,
					Models:  []string{"grok-4.5", "grok-imagine-video-1.5"},
				},
				UpdatedAt: now,
			}}, &pagination.PaginationResult{Page: 1, PageSize: params.PageSize, Pages: 1, Total: 1}, nil
		}),
		models: configPushModelProviderFunc(func(context.Context) []string {
			return []string{"grok-4.5", "grok-imagine-video-1.5"}
		}),
		prices: configPushPriceProviderFunc(func(models []string, gatewayCode string) []gatewaylite.GatewayModelPrice {
			require.Equal(t, "jp-grok", gatewayCode)
			require.Equal(t, []string{"grok-4.5", "grok-imagine-video-1.5"}, models)
			return []gatewaylite.GatewayModelPrice{{
				GatewayCode:              gatewayCode,
				Model:                    "grok-imagine-video-1.5",
				BillingMode:              "video",
				VideoPricePerSecond480P:  video480,
				VideoPricePerSecond720P:  video720,
				VideoPricePerSecond1080P: video1080,
			}}
		}),
		runtimeSettings: configPushRuntimeSettingsProviderFunc(func(context.Context) (*service.OpenAIFastPolicySettings, error) {
			return &service.OpenAIFastPolicySettings{
				Version:                    service.OpenAIFastPolicySettingsVersion,
				DetectionByRequestEnabled:  false,
				DetectionByResponseEnabled: true,
				Rules: []service.OpenAIFastPolicyRule{{
					ID:          "priority-all",
					ServiceTier: service.OpenAIFastTierPriority,
					Action:      service.BetaPolicyActionPass,
					Scope:       service.BetaPolicyScopeAll,
				}},
			}, nil
		}),
	}

	snapshot, err := provider.BuildGatewayConfigPushSnapshot(context.Background(), "jp-grok")
	require.NoError(t, err)
	require.Len(t, snapshot.Accounts, 2)
	require.Len(t, snapshot.Groups, 1)
	require.Len(t, snapshot.ModelPrices, 1)
	require.NotNil(t, snapshot.RuntimeSettings)
	require.NotNil(t, snapshot.RuntimeSettings.OpenAIFastPolicySettings)
	require.Equal(t, service.OpenAIFastPolicySettingsVersion, snapshot.RuntimeSettings.OpenAIFastPolicySettings.Version)
	require.False(t, snapshot.RuntimeSettings.OpenAIFastPolicySettings.PriorityDetectionByRequestEnabled)
	require.True(t, snapshot.RuntimeSettings.OpenAIFastPolicySettings.PriorityDetectionByResponseEnabled)
	require.Equal(t, 1, snapshot.RuntimeSettings.OpenAIFastPolicySettings.RuleCount)
	require.Positive(t, snapshot.RuntimeSettings.OpenAIFastPolicySettings.ReportedAtMillis)

	account := snapshot.Accounts[0]
	require.EqualValues(t, 10, account.ID)
	require.NotContains(t, account.Credentials, "access_token")
	require.NotContains(t, account.Credentials, "refresh_token")
	require.True(t, account.CredentialsStatus["has_access_token"])
	require.True(t, account.CredentialsStatus["has_refresh_token"])
	require.Equal(t, map[string]any{
		"grok-4.5":               "grok-4.5",
		"grok-imagine-video-1.5": "grok-imagine-video-1.5",
	}, account.Credentials["model_mapping"])
	metadata := account.Credentials["metadata"].(map[string]any)
	require.NotContains(t, metadata, "client_secret")
	require.Equal(t, "xai", metadata["tenant"])
	nestedExtra := account.Extra["nested"].(map[string]any)
	require.NotContains(t, nestedExtra, "authorization")
	require.Equal(t, "pro", nestedExtra["plan"])
	require.Equal(t, now.UnixMilli(), account.UpdatedAtMillis)

	group := snapshot.Groups[0]
	require.Equal(t, "Grok Media", group.Name)
	require.True(t, group.PeakRateEnabled)
	require.Equal(t, "22:00", group.PeakStart)
	require.Equal(t, "06:00", group.PeakEnd)
	require.Equal(t, 1.6, group.PeakRateMultiplier)
	require.Equal(t, "Asia/Tokyo", group.PeakTimezone)
	require.True(t, group.VideoRateIndependent)
	require.Equal(t, video1080, *group.VideoPrice1080P)
	require.Equal(t, webSearch, *group.WebSearchPricePerCall)
	require.Equal(t, []string{"grok-4.5", "grok-imagine-video-1.5"}, group.ModelsListConfig.Models)
	require.True(t, group.EffectiveModelsReported)
	require.True(t, group.ModelPricesReported)
	require.Positive(t, group.EffectiveModelsVersion)
	require.Equal(t, len(group.EffectiveModels), group.EffectiveModelCount)
	require.Contains(t, group.EffectiveModels, "grok-4.5")
	require.NotContains(t, group.EffectiveModels, "gpt-5.5")
	require.Len(t, group.ModelPrices, 1)
	require.Equal(t, "grok-imagine-video-1.5", group.ModelPrices[0].Model)
	require.Empty(t, group.ModelPrices[0].GatewayCode)

	price := snapshot.ModelPrices[0]
	require.Equal(t, "video", price.BillingMode)
	require.Equal(t, video480, price.VideoPricePerSecond480P)
	require.Equal(t, video720, price.VideoPricePerSecond720P)
	require.Equal(t, video1080, price.VideoPricePerSecond1080P)
}

func TestGatewayLiteConfigPushSnapshotReportsKnownEmptyEffectiveModels(t *testing.T) {
	provider := gatewayLiteConfigPushSnapshotProvider{
		accounts: configPushAccountListerFunc(func(_ context.Context, params pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error) {
			return nil, &pagination.PaginationResult{Page: params.Page, PageSize: params.PageSize, Pages: 1}, nil
		}),
		groups: configPushGroupListerFunc(func(_ context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
			return []service.Group{{ID: 99, Name: "Empty Grok", Platform: service.PlatformGrok, Status: service.StatusActive}}, &pagination.PaginationResult{Page: 1, PageSize: params.PageSize, Pages: 1, Total: 1}, nil
		}),
		models: configPushModelProviderFunc(func(context.Context) []string { return []string{"grok-4.5"} }),
		prices: configPushPriceProviderFunc(func([]string, string) []gatewaylite.GatewayModelPrice { return nil }),
	}

	snapshot, err := provider.BuildGatewayConfigPushSnapshot(context.Background(), "empty-grok")
	require.NoError(t, err)
	require.Len(t, snapshot.Groups, 1)
	require.True(t, snapshot.Groups[0].EffectiveModelsReported)
	require.True(t, snapshot.Groups[0].ModelPricesReported)
	require.NotNil(t, snapshot.Groups[0].EffectiveModels)
	require.NotNil(t, snapshot.Groups[0].ModelPrices)
	require.Empty(t, snapshot.Groups[0].EffectiveModels)
	require.Empty(t, snapshot.Groups[0].ModelPrices)
	require.Zero(t, snapshot.Groups[0].EffectiveModelCount)
}

func TestGatewayLiteConfigPushSnapshotExcludesLocalOnlyGroupsAndRelations(t *testing.T) {
	const (
		publishedGroupID = int64(20)
		localOnlyGroupID = int64(21)
	)
	provider := gatewayLiteConfigPushSnapshotProvider{
		accounts: configPushAccountListerFunc(func(_ context.Context, params pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error) {
			return []service.Account{
				{ID: 10, Name: "shared", Platform: service.PlatformGrok, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{publishedGroupID, localOnlyGroupID}},
				{ID: 11, Name: "local-only", Platform: service.PlatformGrok, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{localOnlyGroupID}},
			}, &pagination.PaginationResult{Page: 1, PageSize: params.PageSize, Pages: 1, Total: 2}, nil
		}),
		groups: configPushGroupListerFunc(func(_ context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
			return []service.Group{
				{ID: publishedGroupID, Name: "published", Platform: service.PlatformGrok, Status: service.StatusActive, RateMultiplier: 1},
				{ID: localOnlyGroupID, Name: "gateway-local", Platform: service.PlatformGrok, Status: service.StatusActive, RateMultiplier: 1, LocalOnly: true},
			}, &pagination.PaginationResult{Page: 1, PageSize: params.PageSize, Pages: 1, Total: 2}, nil
		}),
		models: configPushModelProviderFunc(func(context.Context) []string { return []string{"grok-4.5"} }),
		prices: configPushPriceProviderFunc(func([]string, string) []gatewaylite.GatewayModelPrice {
			return []gatewaylite.GatewayModelPrice{{Model: "grok-4.5", Provider: service.PlatformGrok, Currency: "USD"}}
		}),
	}

	snapshot, err := provider.BuildGatewayConfigPushSnapshot(context.Background(), "us-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Groups, 1)
	require.Equal(t, publishedGroupID, snapshot.Groups[0].ID)
	require.NotEqual(t, localOnlyGroupID, snapshot.Groups[0].ID)
	require.Len(t, snapshot.Accounts, 2)
	require.Equal(t, []int64{publishedGroupID}, snapshot.Accounts[0].GroupIDs)
	require.Empty(t, snapshot.Accounts[1].GroupIDs)
	require.Len(t, snapshot.EffectivePolicies, 1)
	require.Equal(t, publishedGroupID, snapshot.EffectivePolicies[0].LocalGroupID)
}

func TestGatewayLiteGroupPushSnapshotIncludesOpenAILongContextStatus(t *testing.T) {
	snapshot := gatewayLiteGroupPushSnapshot(&service.Group{
		ID:                                   7,
		Platform:                             service.PlatformOpenAI,
		AllowOpenAILongContextBilling:        true,
		OpenAILongContextBillingAccountCount: 2,
		OpenAIStandardBillingAccountCount:    3,
		OpenAILongContextBillingStatus:       service.GroupOpenAILongContextBillingStatusEnabled,
	})
	require.True(t, snapshot.AllowOpenAILongContextBilling)
	require.EqualValues(t, 2, snapshot.OpenAILongContextBillingAccountCount)
	require.EqualValues(t, 3, snapshot.OpenAIStandardBillingAccountCount)
	require.Equal(t, service.GroupOpenAILongContextBillingStatusEnabled, snapshot.OpenAILongContextBillingStatus)
}

func TestGatewayLiteGroupPushSnapshotIncludesDisplayIdentity(t *testing.T) {
	snapshot := gatewayLiteGroupPushSnapshot(&service.Group{
		ID:           9,
		Description:  "面向 Kimi Code 客户\n支持代码模型",
		Platform:     service.PlatformOpenAI,
		DisplayBrand: "Kimi",
		DisplayIcon:  "kimi",
		SortOrder:    27,
	})
	require.Equal(t, service.PlatformOpenAI, snapshot.Platform)
	require.Equal(t, "面向 Kimi Code 客户\n支持代码模型", snapshot.Description)
	require.Equal(t, "Kimi", snapshot.DisplayBrand)
	require.Equal(t, "kimi", snapshot.DisplayIcon)
	require.Equal(t, 27, snapshot.SortOrder)
}

func TestGatewayLiteGroupPushSnapshotPreservesBillingAndReasoningPolicy(t *testing.T) {
	snapshot := gatewayLiteGroupPushSnapshot(&service.Group{
		ID:                 12,
		Platform:           service.PlatformOpenAI,
		AllowLive:          true,
		BillingModelSource: service.BillingModelSourceRequested,
		MaxReasoningEffort: "high",
		ReasoningEffortMappings: []service.ReasoningEffortMapping{
			{From: "max", To: "high"},
		},
	})

	require.True(t, snapshot.AllowLive)
	require.Equal(t, service.BillingModelSourceRequested, snapshot.BillingModelSource)
	require.Equal(t, "high", snapshot.MaxReasoningEffort)
	require.Equal(t, []gatewaylite.ReasoningEffortMapping{{From: "max", To: "high"}}, snapshot.ReasoningEffortMappings)
}
