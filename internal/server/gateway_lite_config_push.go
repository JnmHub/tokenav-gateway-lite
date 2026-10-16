package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	gatewayLiteConfigPushPageSize = 1000
	gatewayLiteConfigPushMaxPages = 1000
)

type gatewayLiteConfigPushAccountLister interface {
	List(ctx context.Context, params pagination.PaginationParams) ([]service.Account, *pagination.PaginationResult, error)
}

type gatewayLiteConfigPushGroupLister interface {
	List(ctx context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error)
}

type gatewayLiteConfigPushModelProvider interface {
	AvailableModelsFromAccounts(accounts []service.Account) []string
}

type gatewayLiteConfigPushPriceProvider interface {
	GatewayModelPrices(models []string, gatewayCode string) []gatewaylite.GatewayModelPrice
}

type gatewayLiteConfigPushRuntimeSettingsProvider interface {
	GetOpenAIFastPolicySettings(ctx context.Context) (*service.OpenAIFastPolicySettings, error)
}

type gatewayLiteConfigPushCompositeRouteProvider interface {
	ListRoutesForPolicy(ctx context.Context, groupID int64) ([]service.CompositeModelRoute, error)
}

type gatewayLiteConfigPushGroupPricingProvider interface {
	GetChannelForGroup(ctx context.Context, groupID int64) (*service.Channel, error)
}

type gatewayLiteConfigPushSnapshotProvider struct {
	accounts        gatewayLiteConfigPushAccountLister
	groups          gatewayLiteConfigPushGroupLister
	models          gatewayLiteConfigPushModelProvider
	prices          gatewayLiteConfigPushPriceProvider
	runtimeSettings gatewayLiteConfigPushRuntimeSettingsProvider
	compositeRoutes gatewayLiteConfigPushCompositeRouteProvider
	groupPricing    gatewayLiteConfigPushGroupPricingProvider
}

func (p gatewayLiteConfigPushSnapshotProvider) BuildGatewayConfigPushSnapshot(ctx context.Context, gatewayCode string) (gatewaylite.GatewayConfigPushSnapshot, error) {
	if p.accounts == nil || p.groups == nil {
		return gatewaylite.GatewayConfigPushSnapshot{}, fmt.Errorf("account and group repositories are required")
	}
	if p.models == nil || p.prices == nil {
		return gatewaylite.GatewayConfigPushSnapshot{}, fmt.Errorf("model and price providers are required")
	}

	sourceAccounts, err := p.listAccounts(ctx)
	if err != nil {
		return gatewaylite.GatewayConfigPushSnapshot{}, err
	}
	groups, err := p.listGroups(ctx)
	if err != nil {
		return gatewaylite.GatewayConfigPushSnapshot{}, err
	}
	accounts := make([]gatewaylite.GatewayAccountSnapshot, 0, len(sourceAccounts))
	for i := range sourceAccounts {
		accounts = append(accounts, gatewayLiteAccountPushSnapshot(&sourceAccounts[i]))
	}
	accounts = gatewayLiteConfigPushFilterAccountGroups(accounts, groups)
	// 目录与账号必须来自同一批读取；再次查仓库或读取客户缓存都可能混入另一代配置。
	models := p.models.AvailableModelsFromAccounts(sourceAccounts)
	prices := p.prices.GatewayModelPrices(models, strings.TrimSpace(gatewayCode))
	runtimeSettings, err := p.runtimeSettingsSnapshot(ctx)
	if err != nil {
		return gatewaylite.GatewayConfigPushSnapshot{}, err
	}
	effectiveAccounts := gatewayLiteConfigPushEffectiveAccounts(accounts)
	effectiveModelsVersion := time.Now().UnixMilli()
	compositeRoutesByGroup := make(map[int64][]service.CompositeModelRoute)
	for i := range groups {
		group := gatewayLiteConfigPushEffectiveGroup(groups[i])
		var effectiveModels []string
		if group.Platform == service.PlatformComposite {
			routes := []service.CompositeModelRoute{}
			if p.compositeRoutes != nil {
				routes, err = p.compositeRoutes.ListRoutesForPolicy(ctx, group.ID)
				if err != nil {
					return gatewaylite.GatewayConfigPushSnapshot{}, fmt.Errorf("list composite routes for group %d: %w", group.ID, err)
				}
			}
			compositeRoutesByGroup[group.ID] = routes
			effectiveModels = service.EffectiveModelsForCompositeGroup(&group, effectiveAccounts, models, routes)
		} else {
			effectiveModels = service.EffectiveModelsForGroup(&group, effectiveAccounts, models)
		}
		groups[i].EffectiveModels = append([]string{}, effectiveModels...)
		groups[i].EffectiveModelCount = len(effectiveModels)
		groups[i].EffectiveModelsReported = true
		groups[i].EffectiveModelsVersion = effectiveModelsVersion
		var pricingChannel *service.Channel
		if p.groupPricing != nil {
			pricingChannel, err = p.groupPricing.GetChannelForGroup(ctx, group.ID)
			if err != nil {
				return gatewaylite.GatewayConfigPushSnapshot{}, fmt.Errorf("load pricing for group %d: %w", group.ID, err)
			}
		}
		if pricingChannel != nil && pricingChannel.RestrictModels {
			effectiveModels = gatewayLiteGroupRestrictedModels(
				groups[i], effectiveModels, prices, compositeRoutesByGroup[group.ID], pricingChannel, accounts,
			)
			groups[i].EffectiveModels = append([]string{}, effectiveModels...)
			groups[i].EffectiveModelCount = len(effectiveModels)
		}
		// 分组合同在控制面推送的冷路径一次性展开；用户请求只读取本地缓存，
		// 不会因为主站需要展示价格而增加 SQL 或远程请求。
		groups[i].ModelPrices = buildGatewayLiteGroupModelPrices(
			groups[i], prices, compositeRoutesByGroup[group.ID], pricingChannel, accounts,
		)
		// 空数组也可能是映射歧义后的权威结果。显式标记可防止主站把它误当成
		// 旧节点未上报，再回退到节点目录并展示不准确的价格。
		groups[i].ModelPricesReported = true
	}
	effectivePolicies, err := buildGatewayLiteEffectivePolicies(
		strings.TrimSpace(gatewayCode), accounts, groups, nil, effectiveModelsVersion, compositeRoutesByGroup,
	)
	if err != nil {
		return gatewaylite.GatewayConfigPushSnapshot{}, fmt.Errorf("build effective policies: %w", err)
	}

	return gatewaylite.GatewayConfigPushSnapshot{
		Accounts:          accounts,
		Groups:            groups,
		ModelPrices:       prices,
		EffectivePolicies: effectivePolicies,
		RuntimeSettings:   runtimeSettings,
	}, nil
}

func (p gatewayLiteConfigPushSnapshotProvider) runtimeSettingsSnapshot(ctx context.Context) (*gatewaylite.GatewayRuntimeSettings, error) {
	if p.runtimeSettings == nil {
		return nil, nil
	}
	fastPolicy, err := p.runtimeSettings.GetOpenAIFastPolicySettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get openai fast policy settings: %w", err)
	}
	if fastPolicy == nil {
		return nil, nil
	}
	return &gatewaylite.GatewayRuntimeSettings{
		OpenAIFastPolicySettings: &gatewaylite.OpenAIFastPolicyRuntimeSettings{
			Version:                            fastPolicy.Version,
			PriorityDetectionByRequestEnabled:  fastPolicy.DetectionByRequestEnabled,
			PriorityDetectionByResponseEnabled: fastPolicy.DetectionByResponseEnabled,
			RuleCount:                          len(fastPolicy.Rules),
			ReportedAtMillis:                   time.Now().UnixMilli(),
		},
	}, nil
}

func gatewayLiteConfigPushEffectiveAccounts(accounts []gatewaylite.GatewayAccountSnapshot) []service.Account {
	out := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, service.Account{
			ID:          account.ID,
			Platform:    account.Platform,
			Type:        account.Type,
			Status:      account.Status,
			Schedulable: account.Schedulable,
			Credentials: account.Credentials,
			Extra:       account.Extra,
			GroupIDs:    cloneGatewayLiteInt64Slice(account.GroupIDs),
			AccountGroups: gatewayLiteServiceAccountGroups(
				account.ID,
				account.GroupIDs,
				account.GroupPriorities,
				account.GroupModelMappings,
				account.Priority,
			),
		})
	}
	return out
}

func gatewayLiteConfigPushEffectiveGroup(group gatewaylite.GatewayGroupSnapshot) service.Group {
	return service.Group{
		ID:                      group.ID,
		PerUserConcurrency:      group.PerUserConcurrency,
		Platform:                group.Platform,
		SchedulingMode:          group.SchedulingMode,
		AllowImageGeneration:    group.AllowImageGeneration,
		SupportedModelScopes:    cloneGatewayLiteStringSlice(group.SupportedModelScopes),
		RequireOAuthOnly:        group.RequireOAuthOnly,
		RequirePrivacySet:       group.RequirePrivacySet,
		ModelMapping:            cloneGatewayLiteStringMap(group.ModelMapping),
		WeightedModelMapping:    weightedModelMappingFromGatewayLite(group.WeightedModelMapping),
		FallbackModel:           group.FallbackModel,
		FailoverGroupID:         cloneGatewayLiteInt64Ptr(group.FailoverGroupID),
		FailoverStrategy:        group.FailoverStrategy,
		BillingModelSource:      group.BillingModelSource,
		AllowLive:               group.AllowLive,
		MaxReasoningEffort:      group.MaxReasoningEffort,
		ReasoningEffortMappings: gatewayLiteServiceReasoningEffortMappings(group.ReasoningEffortMappings),
		ModelsListConfig: service.GroupModelsListConfig{
			Enabled:             group.ModelsListConfig.Enabled,
			Models:              cloneGatewayLiteStringSlice(group.ModelsListConfig.Models),
			ResponseModelMode:   group.ModelsListConfig.ResponseModelMode,
			ResponseModelCustom: group.ModelsListConfig.ResponseModelCustom,
		},
	}
}

func (p gatewayLiteConfigPushSnapshotProvider) listAccounts(ctx context.Context) ([]service.Account, error) {
	out := make([]service.Account, 0)
	for page := 1; page <= gatewayLiteConfigPushMaxPages; page++ {
		items, result, err := p.accounts.List(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  gatewayLiteConfigPushPageSize,
			SortBy:    "id",
			SortOrder: pagination.SortOrderAsc,
		})
		if err != nil {
			return nil, fmt.Errorf("list accounts page %d: %w", page, err)
		}
		out = append(out, items...)
		if len(items) == 0 || result == nil || result.Pages <= page {
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			return out, nil
		}
	}
	return nil, fmt.Errorf("account snapshot exceeds %d pages", gatewayLiteConfigPushMaxPages)
}

func (p gatewayLiteConfigPushSnapshotProvider) listGroups(ctx context.Context) ([]gatewaylite.GatewayGroupSnapshot, error) {
	out := make([]gatewaylite.GatewayGroupSnapshot, 0)
	for page := 1; page <= gatewayLiteConfigPushMaxPages; page++ {
		items, result, err := p.groups.List(ctx, pagination.PaginationParams{
			Page:      page,
			PageSize:  gatewayLiteConfigPushPageSize,
			SortBy:    "id",
			SortOrder: pagination.SortOrderAsc,
		})
		if err != nil {
			return nil, fmt.Errorf("list groups page %d: %w", page, err)
		}
		for i := range items {
			if items[i].LocalOnly {
				continue
			}
			out = append(out, gatewayLiteGroupPushSnapshot(&items[i]))
		}
		if len(items) == 0 || result == nil || result.Pages <= page {
			sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
			return out, nil
		}
	}
	return nil, fmt.Errorf("group snapshot exceeds %d pages", gatewayLiteConfigPushMaxPages)
}

// gatewayLiteConfigPushFilterAccountGroups 只保留本次可发布分组集合。
// 这样主站不会在账号镜像中看到 local_only 分组的孤立编号，同时 groups 和
// effective policy 仍来自同一权威集合。
func gatewayLiteConfigPushFilterAccountGroups(accounts []gatewaylite.GatewayAccountSnapshot, groups []gatewaylite.GatewayGroupSnapshot) []gatewaylite.GatewayAccountSnapshot {
	publishable := make(map[int64]struct{}, len(groups))
	for i := range groups {
		if groups[i].ID > 0 {
			publishable[groups[i].ID] = struct{}{}
		}
	}
	for i := range accounts {
		filtered := make([]int64, 0, len(accounts[i].GroupIDs))
		filteredPriorities := make(map[int64]int)
		filteredMappings := make(map[int64]map[string]string)
		for _, groupID := range accounts[i].GroupIDs {
			if _, ok := publishable[groupID]; ok {
				filtered = append(filtered, groupID)
				if priority, exists := accounts[i].GroupPriorities[groupID]; exists {
					filteredPriorities[groupID] = priority
				}
				if mapping := accounts[i].GroupModelMappings[groupID]; len(mapping) > 0 {
					filteredMappings[groupID] = cloneGatewayLiteStringMap(mapping)
				}
			}
		}
		accounts[i].GroupIDs = filtered
		accounts[i].GroupPriorities = filteredPriorities
		accounts[i].GroupModelMappings = filteredMappings
	}
	return accounts
}

func gatewayLiteAccountPushSnapshot(account *service.Account) gatewaylite.GatewayAccountSnapshot {
	if account == nil {
		return gatewaylite.GatewayAccountSnapshot{}
	}
	credentials, credentialsStatus := dto.RedactCredentials(account.Credentials)
	credentials = gatewayLiteConfigPushSanitizeMap(credentials)
	updatedAtMillis := account.UpdatedAt.UnixMilli()
	if account.UpdatedAt.IsZero() {
		updatedAtMillis = time.Now().UnixMilli()
	}
	return gatewaylite.GatewayAccountSnapshot{
		ID:                 account.ID,
		Name:               account.Name,
		Platform:           account.Platform,
		Type:               account.Type,
		Status:             account.Status,
		Schedulable:        account.Schedulable,
		Concurrency:        account.Concurrency,
		Priority:           account.Priority,
		RateMultiplier:     cloneGatewayLiteFloat64Ptr(account.RateMultiplier),
		LoadFactor:         cloneGatewayLiteIntPtr(account.LoadFactor),
		Credentials:        credentials,
		CredentialsStatus:  credentialsStatus,
		Extra:              gatewayLiteConfigPushSanitizeMap(account.Extra),
		GroupIDs:           cloneGatewayLiteInt64Slice(account.GroupIDs),
		GroupPriorities:    gatewayLiteAccountGroupPriorities(account),
		GroupModelMappings: gatewayLiteAccountGroupModelMappings(account),
		Runtime:            gatewayLiteAccountRuntimeSnapshot(account),
		Version:            updatedAtMillis,
		UpdatedAtMillis:    updatedAtMillis,
	}
}

func gatewayLiteGroupPushSnapshot(group *service.Group) gatewaylite.GatewayGroupSnapshot {
	if group == nil {
		return gatewaylite.GatewayGroupSnapshot{}
	}
	updatedAtMillis := group.UpdatedAt.UnixMilli()
	return gatewaylite.GatewayGroupSnapshot{
		ID:                                   group.ID,
		LocalGroupID:                         group.ID,
		Name:                                 group.Name,
		Description:                          group.Description,
		Platform:                             group.Platform,
		DisplayBrand:                         group.DisplayBrand,
		DisplayIcon:                          group.DisplayIcon,
		Status:                               group.Status,
		SortOrder:                            group.SortOrder,
		SchedulingMode:                       group.SchedulingMode,
		IsExclusive:                          group.IsExclusive,
		MinimumTier:                          group.MinimumTier,
		PerUserConcurrency:                   group.PerUserConcurrency,
		SubscriptionType:                     group.SubscriptionType,
		RateMultiplier:                       group.RateMultiplier,
		PeakRateEnabled:                      group.PeakRateEnabled,
		PeakStart:                            group.PeakStart,
		PeakEnd:                              group.PeakEnd,
		PeakRateMultiplier:                   group.PeakRateMultiplier,
		PeakTimezone:                         group.PeakTimezone,
		DailyLimitUSD:                        cloneGatewayLiteFloat64Ptr(group.DailyLimitUSD),
		WeeklyLimitUSD:                       cloneGatewayLiteFloat64Ptr(group.WeeklyLimitUSD),
		MonthlyLimitUSD:                      cloneGatewayLiteFloat64Ptr(group.MonthlyLimitUSD),
		AllowImageGeneration:                 group.AllowImageGeneration,
		ImageRateIndependent:                 group.ImageRateIndependent,
		ImageRateMultiplier:                  group.ImageRateMultiplier,
		ImagePrice1K:                         cloneGatewayLiteFloat64Ptr(group.ImagePrice1K),
		ImagePrice2K:                         cloneGatewayLiteFloat64Ptr(group.ImagePrice2K),
		ImagePrice4K:                         cloneGatewayLiteFloat64Ptr(group.ImagePrice4K),
		VideoRateIndependent:                 group.VideoRateIndependent,
		VideoRateMultiplier:                  group.VideoRateMultiplier,
		VideoPrice480P:                       cloneGatewayLiteFloat64Ptr(group.VideoPrice480P),
		VideoPrice720P:                       cloneGatewayLiteFloat64Ptr(group.VideoPrice720P),
		VideoPrice1080P:                      cloneGatewayLiteFloat64Ptr(group.VideoPrice1080P),
		VideoModelPrices:                     service.NormalizeVideoModelPrices(group.VideoModelPrices),
		WebSearchPricePerCall:                cloneGatewayLiteFloat64Ptr(group.WebSearchPricePerCall),
		SearchPricePer1k:                     cloneGatewayLiteFloat64Ptr(group.SearchPricePer1k),
		AudioRealtimePricePerMin:             cloneGatewayLiteFloat64Ptr(group.AudioRealtimePricePerMin),
		AudioTtsPricePerMillionChars:         cloneGatewayLiteFloat64Ptr(group.AudioTTSPricePerMillionChars),
		AudioSttPricePerHour:                 cloneGatewayLiteFloat64Ptr(group.AudioSTTPricePerHour),
		AllowOpenAILongContextBilling:        group.AllowOpenAILongContextBilling,
		AllowBlockedKeywordRequests:          group.AllowBlockedKeywordRequests,
		OpenAILongContextBillingAccountCount: group.OpenAILongContextBillingAccountCount,
		OpenAIStandardBillingAccountCount:    group.OpenAIStandardBillingAccountCount,
		OpenAILongContextBillingStatus:       group.OpenAILongContextBillingStatus,
		ClaudeCodeOnly:                       group.ClaudeCodeOnly,
		FallbackGroupID:                      cloneGatewayLiteInt64Ptr(group.FallbackGroupID),
		FallbackGroupIDOnInvalidRequest:      cloneGatewayLiteInt64Ptr(group.FallbackGroupIDOnInvalidRequest),
		FailoverGroupID:                      cloneGatewayLiteInt64Ptr(group.FailoverGroupID),
		FailoverStrategy:                     group.FailoverStrategy,
		ModelRouting:                         cloneGatewayLiteModelRouting(group.ModelRouting),
		ModelRoutingEnabled:                  group.ModelRoutingEnabled,
		ModelMapping:                         cloneGatewayLiteStringMap(group.ModelMapping),
		WeightedModelMapping:               cloneGatewayLiteWeightedModelMapping(group.WeightedModelMapping),
		FallbackModel:                        group.FallbackModel,
		BillingModelSource:                   group.BillingModelSource,
		MCPXMLInject:                         group.MCPXMLInject,
		SupportedModelScopes:                 cloneGatewayLiteStringSlice(group.SupportedModelScopes),
		AllowMessagesDispatch:                group.AllowMessagesDispatch,
		AllowLive:                            group.AllowLive,
		RequireOAuthOnly:                     group.RequireOAuthOnly,
		RequirePrivacySet:                    group.RequirePrivacySet,
		DefaultMappedModel:                   group.DefaultMappedModel,
		MessagesDispatchModelConfig: gatewaylite.OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:    group.MessagesDispatchModelConfig.OpusMappedModel,
			SonnetMappedModel:  group.MessagesDispatchModelConfig.SonnetMappedModel,
			HaikuMappedModel:   group.MessagesDispatchModelConfig.HaikuMappedModel,
			ExactModelMappings: cloneGatewayLiteStringMap(group.MessagesDispatchModelConfig.ExactModelMappings),
		},
		ModelsListConfig: gatewaylite.GroupModelsListConfig{
			Enabled:             group.ModelsListConfig.Enabled,
			Models:              cloneGatewayLiteStringSlice(group.ModelsListConfig.Models),
			ResponseModelMode:   group.ModelsListConfig.ResponseModelMode,
			ResponseModelCustom: group.ModelsListConfig.ResponseModelCustom,
		},
		RPMLimit:                group.RPMLimit,
		MaxReasoningEffort:      group.MaxReasoningEffort,
		ReasoningEffortMappings: gatewayLiteSnapshotReasoningEffortMappings(group.ReasoningEffortMappings),
		Version:                 updatedAtMillis,
		UpdatedAtMillis:         updatedAtMillis,
	}
}

func gatewayLiteSnapshotReasoningEffortMappings(values []service.ReasoningEffortMapping) []gatewaylite.ReasoningEffortMapping {
	if len(values) == 0 {
		return nil
	}
	out := make([]gatewaylite.ReasoningEffortMapping, len(values))
	for index := range values {
		out[index] = gatewaylite.ReasoningEffortMapping{From: values[index].From, To: values[index].To}
	}
	return out
}

func gatewayLiteServiceReasoningEffortMappings(values []gatewaylite.ReasoningEffortMapping) []service.ReasoningEffortMapping {
	if len(values) == 0 {
		return nil
	}
	out := make([]service.ReasoningEffortMapping, len(values))
	for index := range values {
		out[index] = service.ReasoningEffortMapping{From: values[index].From, To: values[index].To}
	}
	return out
}

func gatewayLiteAccountRuntimeSnapshot(account *service.Account) map[string]any {
	if account == nil {
		return nil
	}
	out := map[string]any{}
	if strings.TrimSpace(account.ErrorMessage) != "" {
		out["error_message"] = account.ErrorMessage
	}
	if strings.TrimSpace(account.TempUnschedulableReason) != "" {
		out["temp_unschedulable_reason"] = account.TempUnschedulableReason
	}
	for key, value := range map[string]*time.Time{
		"last_used_at":             account.LastUsedAt,
		"expires_at":               account.ExpiresAt,
		"rate_limited_at":          account.RateLimitedAt,
		"rate_limit_reset_at":      account.RateLimitResetAt,
		"overload_until":           account.OverloadUntil,
		"temp_unschedulable_until": account.TempUnschedulableUntil,
		"session_window_start":     account.SessionWindowStart,
		"session_window_end":       account.SessionWindowEnd,
	} {
		if value != nil {
			out[key] = value.UnixMilli()
		}
	}
	if strings.TrimSpace(account.SessionWindowStatus) != "" {
		out["session_window_status"] = account.SessionWindowStatus
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func gatewayLiteConfigPushSanitizeMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		if gatewayLiteConfigPushSensitiveField(key) {
			continue
		}
		out[key] = gatewayLiteConfigPushSanitizeValue(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func gatewayLiteConfigPushSanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return gatewayLiteConfigPushSanitizeMap(typed)
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, child := range typed {
			if !gatewayLiteConfigPushSensitiveField(key) {
				out[key] = child
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = gatewayLiteConfigPushSanitizeValue(typed[i])
		}
		return out
	default:
		return value
	}
}

func gatewayLiteConfigPushSensitiveField(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return service.IsSensitiveCredentialKey(key) ||
		key == "token" ||
		key == "authorization" ||
		strings.HasSuffix(key, "_token") ||
		strings.Contains(key, "secret") ||
		strings.Contains(key, "password") ||
		strings.Contains(key, "credential") ||
		strings.Contains(key, "admin_key") ||
		strings.Contains(key, "merchant_key") ||
		strings.Contains(key, "api_key")
}

func cloneGatewayLiteFloat64Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneGatewayLiteIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneGatewayLiteInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneGatewayLiteInt64Slice(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	out := make([]int64, len(values))
	copy(out, values)
	return out
}

func gatewayLiteAccountGroupModelMappings(account *service.Account) map[int64]map[string]string {
	if account == nil || len(account.AccountGroups) == 0 {
		return nil
	}
	result := make(map[int64]map[string]string)
	for _, binding := range account.AccountGroups {
		if binding.GroupID <= 0 || len(binding.ModelMapping) == 0 {
			continue
		}
		result[binding.GroupID] = cloneGatewayLiteStringMap(binding.ModelMapping)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func gatewayLiteAccountGroupPriorities(account *service.Account) map[int64]int {
	if account == nil || len(account.AccountGroups) == 0 {
		return nil
	}
	result := make(map[int64]int, len(account.AccountGroups))
	for _, binding := range account.AccountGroups {
		if binding.GroupID <= 0 {
			continue
		}
		result[binding.GroupID] = binding.Priority
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func gatewayLiteServiceAccountGroups(
	accountID int64,
	groupIDs []int64,
	priorities map[int64]int,
	mappings map[int64]map[string]string,
	fallbackPriority int,
) []service.AccountGroup {
	if len(groupIDs) == 0 {
		return nil
	}
	result := make([]service.AccountGroup, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		priority, ok := priorities[groupID]
		if !ok {
			// Older snapshots do not contain binding priorities. Falling back to
			// the account-wide value preserves their previous scheduling order.
			priority = fallbackPriority
		}
		result = append(result, service.AccountGroup{
			AccountID:    accountID,
			GroupID:      groupID,
			Priority:     priority,
			ModelMapping: cloneGatewayLiteStringMap(mappings[groupID]),
		})
	}
	return result
}

// cloneGatewayLiteWeightedModelMapping 把 service 层智能分流映射克隆成
// 上报/快照传输形态；上报丢失该字段会让主站镜像回环后清空本地分流配置。
func cloneGatewayLiteWeightedModelMapping(value map[string][]service.WeightedModelTarget) map[string][]gatewaylite.WeightedModelTarget {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string][]gatewaylite.WeightedModelTarget, len(value))
	for source, targets := range value {
		rows := make([]gatewaylite.WeightedModelTarget, 0, len(targets))
		for _, target := range targets {
			rows = append(rows, gatewaylite.WeightedModelTarget{Model: target.Model, Weight: target.Weight})
		}
		out[source] = rows
	}
	return out
}

func cloneGatewayLiteModelRouting(value map[string][]int64) map[string][]int64 {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string][]int64, len(value))
	for key, ids := range value {
		out[key] = cloneGatewayLiteInt64Slice(ids)
	}
	return out
}

func cloneGatewayLiteStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	out := make(map[string]string, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}
