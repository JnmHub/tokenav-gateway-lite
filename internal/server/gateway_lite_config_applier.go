package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type gatewayLiteSchedulerConfigApplier struct {
	cache                            service.SchedulerCache
	groupRepository                  service.GroupRepository
	gatewayCode                      string
	region                           string
	runtimeRef                       *gatewaylite.RuntimeConfigRef
	healthSettings                   *gatewaylite.RuntimeHealthSettings
	legacySnapshotSchedulerAuthority bool
}

func newGatewayLiteSchedulerConfigApplier(cache service.SchedulerCache) *gatewayLiteSchedulerConfigApplier {
	return &gatewayLiteSchedulerConfigApplier{cache: cache}
}

func (a *gatewayLiteSchedulerConfigApplier) WithRuntimeHealthSettings(gatewayCode, region string, settings *gatewaylite.RuntimeHealthSettings) *gatewayLiteSchedulerConfigApplier {
	if a == nil {
		return nil
	}
	a.gatewayCode = strings.TrimSpace(gatewayCode)
	a.region = strings.TrimSpace(region)
	a.healthSettings = settings
	return a
}

func (a *gatewayLiteSchedulerConfigApplier) WithRuntimeConfig(ref *gatewaylite.RuntimeConfigRef, settings *gatewaylite.RuntimeHealthSettings) *gatewayLiteSchedulerConfigApplier {
	if a == nil {
		return nil
	}
	a.runtimeRef = ref
	a.healthSettings = settings
	return a
}

func (a *gatewayLiteSchedulerConfigApplier) WithLegacySnapshotSchedulerAuthority(enabled bool) *gatewayLiteSchedulerConfigApplier {
	if a == nil {
		return nil
	}
	a.legacySnapshotSchedulerAuthority = enabled
	return a
}

func (a *gatewayLiteSchedulerConfigApplier) WithGroupRepository(repository service.GroupRepository) *gatewayLiteSchedulerConfigApplier {
	if a == nil {
		return nil
	}
	a.groupRepository = repository
	return a
}

func (a *gatewayLiteSchedulerConfigApplier) ApplyGatewayConfigSnapshot(ctx context.Context, snapshot gatewaylite.GatewayConfigSnapshot) error {
	if a == nil {
		return nil
	}
	a.applyRuntimeHealthSettings(snapshot)
	// 账号池、分组和模型价格已经改为 gateway-lite 本地权威。主站 snapshot
	// 里的 accounts/groups 只保留兼容和诊断意义，不能再写入 scheduler cache，
	// 否则主站旧镜像会把网关后台已关闭的账号重新恢复为可调度。
	if !a.legacySnapshotSchedulerAuthorityEnabled() {
		return nil
	}
	return a.applyLegacySnapshotSchedulerAuthority(ctx, snapshot)
}

func (a *gatewayLiteSchedulerConfigApplier) legacySnapshotSchedulerAuthorityEnabled() bool {
	if a == nil {
		return false
	}
	if a.runtimeRef != nil {
		return a.runtimeRef.Current().LegacySnapshotSchedulerAuthority
	}
	return a.legacySnapshotSchedulerAuthority
}

func (a *gatewayLiteSchedulerConfigApplier) applyLegacySnapshotSchedulerAuthority(ctx context.Context, snapshot gatewaylite.GatewayConfigSnapshot) error {
	if a == nil || a.cache == nil {
		return nil
	}
	localOnlyGroups, err := a.loadLegacySnapshotLocalOnlyGroups(ctx)
	if err != nil {
		return err
	}
	localOnlyAccounts, err := a.collectLegacyLocalOnlyAccounts(ctx, localOnlyGroups)
	if err != nil {
		return err
	}
	staleAccountIDs := a.collectSnapshotAccountIDs(ctx)
	groups := make(map[int64]*service.Group, len(snapshot.Groups))
	if groupCache, ok := a.cache.(service.SchedulerGroupCache); ok {
		// local_only 是网关本地安全边界。即使显式启用了旧主站快照权威模式，
		// 也必须先把数据库中的本地权威值写回缓存，不能让缺少该字段的旧快照降级成 false。
		for _, group := range localOnlyGroups {
			if err := groupCache.SetGroup(ctx, group); err != nil {
				return err
			}
		}
		for _, item := range snapshot.Groups {
			if localOnlyGroups[item.ID] != nil {
				continue
			}
			group := gatewayLiteGroupSnapshotToServiceGroup(item)
			groups[group.ID] = group
			if err := groupCache.SetGroup(ctx, group); err != nil {
				return err
			}
		}
	} else {
		if len(localOnlyGroups) > 0 {
			return errors.New("legacy snapshot scheduler cache cannot preserve local-only groups")
		}
		for _, item := range snapshot.Groups {
			group := gatewayLiteGroupSnapshotToServiceGroup(item)
			groups[group.ID] = group
		}
	}
	accounts := make([]service.Account, 0, len(snapshot.Accounts))
	currentAccountIDs := make(map[int64]struct{}, len(snapshot.Accounts))
	for _, item := range snapshot.Accounts {
		groupIDs, removedLocalOnly := gatewayLiteExcludeLocalOnlyGroupIDs(item.GroupIDs, localOnlyGroups)
		// 仅声明属于 local_only 分组的主站账号不得降级成无分组账号，
		// 否则它仍可能从 group=0 的兼容桶进入远程请求热路径。
		if removedLocalOnly && len(groupIDs) == 0 && len(item.GroupIDs) > 0 {
			continue
		}
		item.GroupIDs = groupIDs
		account := gatewayLiteAccountSnapshotToServiceAccount(item, groups)
		accounts = append(accounts, account)
		if account.ID > 0 {
			currentAccountIDs[account.ID] = struct{}{}
			if err := a.cache.SetAccount(ctx, &account); err != nil {
				return err
			}
		}
	}
	for _, bucket := range gatewayLiteBuildSchedulerBuckets(groups, accounts) {
		if err := a.cache.SetSnapshot(ctx, bucket, gatewayLiteAccountsForBucket(accounts, bucket)); err != nil {
			return err
		}
	}
	// 快照账号可能与本地账号同 ID。桶发布后恢复 local_only 分组原有账号元数据，
	// 同时把它们从过期清理集合中排除，保证本地 glk_ 密钥仍可持续使用。
	for accountID, account := range localOnlyAccounts {
		currentAccountIDs[accountID] = struct{}{}
		if err := a.cache.SetAccount(ctx, account); err != nil {
			return err
		}
	}
	if err := a.clearStaleAccounts(ctx, staleAccountIDs, currentAccountIDs); err != nil {
		return err
	}
	return nil
}

func (a *gatewayLiteSchedulerConfigApplier) loadLegacySnapshotLocalOnlyGroups(ctx context.Context) (map[int64]*service.Group, error) {
	if a == nil || a.groupRepository == nil {
		return nil, errors.New("legacy snapshot scheduler authority requires local group repository")
	}
	groups := make(map[int64]*service.Group)
	for page := 1; ; page++ {
		localGroups, result, err := a.groupRepository.List(ctx, pagination.PaginationParams{Page: page, PageSize: 1000})
		if err != nil {
			// 新主站快照不会携带 local_only 分组，必须直接读取本地权威全集；
			// 无法确认边界时拒绝整个旧快照，不能冒险清理本地账号或覆盖缓存。
			return nil, fmt.Errorf("list local groups before applying legacy snapshot: %w", err)
		}
		for i := range localGroups {
			if localGroups[i].ID > 0 && localGroups[i].LocalOnly {
				group := localGroups[i]
				groups[group.ID] = &group
			}
		}
		if result == nil || page >= result.Pages || len(localGroups) == 0 {
			break
		}
	}
	return groups, nil
}

func (a *gatewayLiteSchedulerConfigApplier) collectLegacyLocalOnlyAccounts(ctx context.Context, localOnlyGroups map[int64]*service.Group) (map[int64]*service.Account, error) {
	accounts := make(map[int64]*service.Account)
	if a == nil || a.cache == nil || len(localOnlyGroups) == 0 {
		return accounts, nil
	}
	buckets, err := a.cache.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scheduler buckets before applying legacy snapshot: %w", err)
	}
	for _, bucket := range buckets {
		if localOnlyGroups[bucket.GroupID] == nil {
			continue
		}
		bucketAccounts, hit, err := a.cache.GetSnapshot(ctx, bucket)
		if err != nil {
			return nil, fmt.Errorf("load local-only scheduler bucket %s: %w", bucket.String(), err)
		}
		if !hit {
			continue
		}
		for _, account := range bucketAccounts {
			if account != nil && account.ID > 0 {
				accounts[account.ID] = account
			}
		}
	}
	return accounts, nil
}

func gatewayLiteExcludeLocalOnlyGroupIDs(groupIDs []int64, localOnlyGroups map[int64]*service.Group) ([]int64, bool) {
	if len(groupIDs) == 0 || len(localOnlyGroups) == 0 {
		return groupIDs, false
	}
	filtered := make([]int64, 0, len(groupIDs))
	removed := false
	for _, groupID := range groupIDs {
		if localOnlyGroups[groupID] != nil {
			removed = true
			continue
		}
		filtered = append(filtered, groupID)
	}
	return filtered, removed
}

func (a *gatewayLiteSchedulerConfigApplier) collectSnapshotAccountIDs(ctx context.Context) map[int64]struct{} {
	out := map[int64]struct{}{}
	if a == nil || a.cache == nil {
		return out
	}
	buckets, err := a.cache.ListBuckets(ctx)
	if err != nil {
		return out
	}
	for _, bucket := range buckets {
		accounts, hit, err := a.cache.GetSnapshot(ctx, bucket)
		if err != nil || !hit {
			continue
		}
		for _, account := range accounts {
			if account != nil && account.ID > 0 {
				out[account.ID] = struct{}{}
			}
		}
	}
	return out
}

func (a *gatewayLiteSchedulerConfigApplier) clearStaleAccounts(ctx context.Context, staleAccountIDs map[int64]struct{}, currentAccountIDs map[int64]struct{}) error {
	if a == nil || a.cache == nil || len(staleAccountIDs) == 0 {
		return nil
	}
	for accountID := range staleAccountIDs {
		if _, ok := currentAccountIDs[accountID]; ok {
			continue
		}
		// gateway-lite 的控制面快照是热路径调度缓存的来源。删除快照中已经消失的账号，
		// 避免旧 sticky session（会话粘连）继续读取过期的单账号缓存。
		if err := a.cache.DeleteAccount(ctx, accountID); err != nil {
			return err
		}
	}
	return nil
}

func (a *gatewayLiteSchedulerConfigApplier) applyRuntimeHealthSettings(snapshot gatewaylite.GatewayConfigSnapshot) {
	if a == nil || a.healthSettings == nil {
		return
	}
	gatewayCode := a.gatewayCode
	region := a.region
	if a.runtimeRef != nil {
		cfg := a.runtimeRef.Current()
		gatewayCode = cfg.GatewayCode
		region = cfg.Region
	}
	var selected *gatewaylite.GatewayNodeSnapshot
	for i := range snapshot.GatewayNodes {
		node := &snapshot.GatewayNodes[i]
		if gatewayCode != "" && strings.EqualFold(node.Code, gatewayCode) {
			selected = node
			break
		}
		if selected == nil && region != "" && strings.EqualFold(node.Region, region) {
			selected = node
		}
	}
	if selected == nil {
		return
	}
	seconds := gatewayLiteMetadataInt(selected.Metadata, "health_probe_interval_seconds")
	if seconds > 0 {
		// 兼容旧配置：节点健康间隔仍调整 runtime report（运行时上报），
		// 同时也作为真实模型 probe（探测）间隔，避免页面健康桶和真实测活节奏脱节。
		interval := time.Duration(seconds) * time.Second
		a.healthSettings.SetInterval(interval)
		a.healthSettings.SetModelProbeInterval(interval)
	}
	if model := gatewayLiteMetadataString(selected.Metadata, "health_probe_model"); model != "" {
		a.healthSettings.SetModelProbeModel(model)
	}
}

func gatewayLiteGroupSnapshotToServiceGroup(item gatewaylite.GatewayGroupSnapshot) *service.Group {
	group := &service.Group{
		ID:                              item.ID,
		Name:                            item.Name,
		Platform:                        item.Platform,
		DisplayBrand:                    item.DisplayBrand,
		DisplayIcon:                     item.DisplayIcon,
		Status:                          item.Status,
		SchedulingMode:                  item.SchedulingMode,
		IsExclusive:                     item.IsExclusive,
		PerUserConcurrency:              item.PerUserConcurrency,
		SubscriptionType:                item.SubscriptionType,
		RateMultiplier:                  item.RateMultiplier,
		PeakRateEnabled:                 item.PeakRateEnabled,
		PeakStart:                       item.PeakStart,
		PeakEnd:                         item.PeakEnd,
		PeakRateMultiplier:              item.PeakRateMultiplier,
		PeakTimezone:                    item.PeakTimezone,
		DailyLimitUSD:                   item.DailyLimitUSD,
		WeeklyLimitUSD:                  item.WeeklyLimitUSD,
		MonthlyLimitUSD:                 item.MonthlyLimitUSD,
		AllowImageGeneration:            item.AllowImageGeneration,
		ImageRateIndependent:            item.ImageRateIndependent,
		ImageRateMultiplier:             item.ImageRateMultiplier,
		ImagePrice1K:                    item.ImagePrice1K,
		ImagePrice2K:                    item.ImagePrice2K,
		ImagePrice4K:                    item.ImagePrice4K,
		VideoRateIndependent:            item.VideoRateIndependent,
		VideoRateMultiplier:             item.VideoRateMultiplier,
		VideoPrice480P:                  item.VideoPrice480P,
		VideoPrice720P:                  item.VideoPrice720P,
		VideoPrice1080P:                 item.VideoPrice1080P,
		VideoModelPrices:                service.NormalizeVideoModelPrices(item.VideoModelPrices),
		WebSearchPricePerCall:           item.WebSearchPricePerCall,
		SearchPricePer1k:                item.SearchPricePer1k,
		AudioRealtimePricePerMin:        item.AudioRealtimePricePerMin,
		AudioTTSPricePerMillionChars:    item.AudioTtsPricePerMillionChars,
		AudioSTTPricePerHour:            item.AudioSttPricePerHour,
		AllowOpenAILongContextBilling:   item.AllowOpenAILongContextBilling,
		AllowBlockedKeywordRequests:     item.AllowBlockedKeywordRequests,
		ClaudeCodeOnly:                  item.ClaudeCodeOnly,
		FallbackGroupID:                 item.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: item.FallbackGroupIDOnInvalidRequest,
		FailoverGroupID:                 item.FailoverGroupID,
		FailoverStrategy:                item.FailoverStrategy,
		ModelRouting:                    item.ModelRouting,
		ModelRoutingEnabled:             item.ModelRoutingEnabled,
		ModelMapping:                    item.ModelMapping,
		FallbackModel:                   item.FallbackModel,
		BillingModelSource:              item.BillingModelSource,
		MCPXMLInject:                    item.MCPXMLInject,
		SupportedModelScopes:            item.SupportedModelScopes,
		AllowMessagesDispatch:           item.AllowMessagesDispatch,
		AllowLive:                       item.AllowLive,
		RequireOAuthOnly:                item.RequireOAuthOnly,
		RequirePrivacySet:               item.RequirePrivacySet,
		DefaultMappedModel:              item.DefaultMappedModel,
		MessagesDispatchModelConfig:     gatewayLiteMessagesDispatchConfig(item.MessagesDispatchModelConfig),
		ModelsListConfig:                gatewayLiteModelsListConfig(item.ModelsListConfig),
		RPMLimit:                        item.RPMLimit,
		MaxReasoningEffort:              item.MaxReasoningEffort,
		ReasoningEffortMappings:         gatewayLiteServiceReasoningEffortMappings(item.ReasoningEffortMappings),
		Hydrated:                        true,
	}
	gatewayLiteApplyGroupConfig(group, item.Config)
	if group.Status == "" {
		group.Status = service.StatusActive
	}
	if group.RateMultiplier == 0 {
		group.RateMultiplier = 1
	}
	if group.ImageRateMultiplier == 0 {
		group.ImageRateMultiplier = 1
	}
	if group.VideoRateMultiplier == 0 {
		group.VideoRateMultiplier = 1
	}
	return group
}

func gatewayLiteMetadataInt(metadata map[string]any, key string) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return 0
}

func gatewayLiteMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func gatewayLiteAccountSnapshotToServiceAccount(item gatewaylite.GatewayAccountSnapshot, groups map[int64]*service.Group) service.Account {
	accountGroups := make([]service.AccountGroup, 0, len(item.GroupIDs))
	serviceGroups := make([]*service.Group, 0, len(item.GroupIDs))
	for _, groupID := range item.GroupIDs {
		priority, ok := item.GroupPriorities[groupID]
		if !ok {
			// Keep old control-plane snapshots compatible while ensuring every
			// restored binding has a deterministic scheduling priority.
			priority = item.Priority
		}
		accountGroups = append(accountGroups, service.AccountGroup{
			AccountID:    item.ID,
			GroupID:      groupID,
			Priority:     priority,
			ModelMapping: cloneGatewayLiteSnapshotStringMap(item.GroupModelMappings[groupID]),
		})
		if group := groups[groupID]; group != nil {
			serviceGroups = append(serviceGroups, group)
		}
	}
	return service.Account{
		ID:             item.ID,
		Name:           item.Name,
		Platform:       item.Platform,
		Type:           gatewayLiteNormalizeAccountType(item.Type),
		Status:         item.Status,
		Schedulable:    item.Schedulable,
		Concurrency:    item.Concurrency,
		Priority:       item.Priority,
		RateMultiplier: item.RateMultiplier,
		LoadFactor:     item.LoadFactor,
		Credentials:    item.Credentials,
		Extra:          item.Extra,
		GroupIDs:       item.GroupIDs,
		AccountGroups:  accountGroups,
		Groups:         serviceGroups,
	}
}

func cloneGatewayLiteSnapshotStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for source, target := range values {
		result[source] = target
	}
	return result
}

func gatewayLiteNormalizeAccountType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "api-key":
		return service.AccountTypeAPIKey
	default:
		return value
	}
}

func gatewayLiteMessagesDispatchConfig(in gatewaylite.OpenAIMessagesDispatchModelConfig) service.OpenAIMessagesDispatchModelConfig {
	return service.OpenAIMessagesDispatchModelConfig{
		OpusMappedModel:    in.OpusMappedModel,
		SonnetMappedModel:  in.SonnetMappedModel,
		HaikuMappedModel:   in.HaikuMappedModel,
		ExactModelMappings: in.ExactModelMappings,
	}
}

func gatewayLiteModelsListConfig(in gatewaylite.GroupModelsListConfig) service.GroupModelsListConfig {
	return service.GroupModelsListConfig{
		Enabled:             in.Enabled,
		Models:              in.Models,
		ResponseModelMode:   in.ResponseModelMode,
		ResponseModelCustom: in.ResponseModelCustom,
	}
}

func gatewayLiteApplyGroupConfig(group *service.Group, config map[string]any) {
	if group == nil || len(config) == 0 {
		return
	}
	gatewayLiteConfigValue(config, "is_exclusive", &group.IsExclusive)
	gatewayLiteConfigValue(config, "subscription_type", &group.SubscriptionType)
	gatewayLiteConfigValue(config, "rate_multiplier", &group.RateMultiplier)
	// 旧版主站只把高峰倍率放在 Config 中；保留这里的读取，避免升级期间
	// 已缓存的旧快照静默退回标准倍率。
	gatewayLiteConfigValue(config, "peak_rate_enabled", &group.PeakRateEnabled)
	gatewayLiteConfigValue(config, "peak_start", &group.PeakStart)
	gatewayLiteConfigValue(config, "peak_end", &group.PeakEnd)
	gatewayLiteConfigValue(config, "peak_rate_multiplier", &group.PeakRateMultiplier)
	gatewayLiteConfigValue(config, "peak_timezone", &group.PeakTimezone)
	gatewayLiteConfigValue(config, "daily_limit_usd", &group.DailyLimitUSD)
	gatewayLiteConfigValue(config, "weekly_limit_usd", &group.WeeklyLimitUSD)
	gatewayLiteConfigValue(config, "monthly_limit_usd", &group.MonthlyLimitUSD)
	gatewayLiteConfigValue(config, "allow_image_generation", &group.AllowImageGeneration)
	gatewayLiteConfigValue(config, "image_rate_independent", &group.ImageRateIndependent)
	gatewayLiteConfigValue(config, "image_rate_multiplier", &group.ImageRateMultiplier)
	gatewayLiteConfigValue(config, "image_price_1k", &group.ImagePrice1K)
	gatewayLiteConfigValue(config, "image_price_2k", &group.ImagePrice2K)
	gatewayLiteConfigValue(config, "image_price_4k", &group.ImagePrice4K)
	gatewayLiteConfigValue(config, "video_rate_independent", &group.VideoRateIndependent)
	gatewayLiteConfigValue(config, "video_rate_multiplier", &group.VideoRateMultiplier)
	gatewayLiteConfigValue(config, "video_price_480p", &group.VideoPrice480P)
	gatewayLiteConfigValue(config, "video_price_720p", &group.VideoPrice720P)
	gatewayLiteConfigValue(config, "video_price_1080p", &group.VideoPrice1080P)
	gatewayLiteConfigValue(config, "video_model_prices", &group.VideoModelPrices)
	gatewayLiteConfigValue(config, "web_search_price_per_call", &group.WebSearchPricePerCall)
	gatewayLiteConfigValue(config, "search_price_per_1k", &group.SearchPricePer1k)
	gatewayLiteConfigValue(config, "audio_realtime_price_per_min", &group.AudioRealtimePricePerMin)
	gatewayLiteConfigValue(config, "audio_tts_price_per_million_chars", &group.AudioTTSPricePerMillionChars)
	gatewayLiteConfigValue(config, "audio_stt_price_per_hour", &group.AudioSTTPricePerHour)
	gatewayLiteConfigValue(config, "allow_openai_long_context_billing", &group.AllowOpenAILongContextBilling)
	gatewayLiteConfigValue(config, "allow_blocked_keyword_requests", &group.AllowBlockedKeywordRequests)
	gatewayLiteConfigValue(config, "claude_code_only", &group.ClaudeCodeOnly)
	gatewayLiteConfigValue(config, "fallback_group_id", &group.FallbackGroupID)
	gatewayLiteConfigValue(config, "fallback_group_id_on_invalid_request", &group.FallbackGroupIDOnInvalidRequest)
	gatewayLiteConfigValue(config, "failover_group_id", &group.FailoverGroupID)
	gatewayLiteConfigValue(config, "failover_strategy", &group.FailoverStrategy)
	gatewayLiteConfigValue(config, "model_routing", &group.ModelRouting)
	gatewayLiteConfigValue(config, "model_routing_enabled", &group.ModelRoutingEnabled)
	gatewayLiteConfigValue(config, "model_mapping", &group.ModelMapping)
	gatewayLiteConfigValue(config, "fallback_model", &group.FallbackModel)
	gatewayLiteConfigValue(config, "billing_model_source", &group.BillingModelSource)
	gatewayLiteConfigValue(config, "mcp_xml_inject", &group.MCPXMLInject)
	gatewayLiteConfigValue(config, "supported_model_scopes", &group.SupportedModelScopes)
	gatewayLiteConfigValue(config, "allow_messages_dispatch", &group.AllowMessagesDispatch)
	gatewayLiteConfigValue(config, "allow_live", &group.AllowLive)
	gatewayLiteConfigValue(config, "require_oauth_only", &group.RequireOAuthOnly)
	gatewayLiteConfigValue(config, "require_privacy_set", &group.RequirePrivacySet)
	gatewayLiteConfigValue(config, "default_mapped_model", &group.DefaultMappedModel)
	gatewayLiteConfigValue(config, "messages_dispatch_model_config", &group.MessagesDispatchModelConfig)
	gatewayLiteConfigValue(config, "models_list_config", &group.ModelsListConfig)
	gatewayLiteConfigValue(config, "rpm_limit", &group.RPMLimit)
	gatewayLiteConfigValue(config, "max_reasoning_effort", &group.MaxReasoningEffort)
	gatewayLiteConfigValue(config, "reasoning_effort_mappings", &group.ReasoningEffortMappings)
}

// weightedModelMappingFromGatewayLite 把快照/配置里的智能分流映射转回
// service 强类型；行缺 weight 时按 1 处理，异常行跳过而不是让整个分组加载失败。
func weightedModelMappingFromGatewayLite(value map[string][]gatewaylite.WeightedModelTarget) map[string][]service.WeightedModelTarget {
	if len(value) == 0 {
		return map[string][]service.WeightedModelTarget{}
	}
	out := make(map[string][]service.WeightedModelTarget, len(value))
	for source, targets := range value {
		rows := make([]service.WeightedModelTarget, 0, len(targets))
		for _, target := range targets {
			if strings.TrimSpace(target.Model) == "" {
				continue
			}
			weight := target.Weight
			if weight <= 0 {
				weight = 1
			}
			rows = append(rows, service.WeightedModelTarget{Model: target.Model, Weight: weight})
		}
		if len(rows) > 0 {
			out[source] = rows
		}
	}
	return out
}

func gatewayLiteConfigValue[T any](config map[string]any, key string, out *T) {
	raw, ok := config[key]
	if !ok || raw == nil || out == nil {
		return
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return
	}
	_ = json.Unmarshal(payload, out)
}

func gatewayLiteBuildSchedulerBuckets(groups map[int64]*service.Group, accounts []service.Account) []service.SchedulerBucket {
	seen := map[service.SchedulerBucket]struct{}{}
	add := func(bucket service.SchedulerBucket) {
		if bucket.Platform == "" {
			return
		}
		seen[bucket] = struct{}{}
	}
	for _, group := range groups {
		if group == nil || group.Platform == "" {
			continue
		}
		add(service.SchedulerBucket{GroupID: group.ID, Platform: group.Platform, Mode: service.SchedulerModeSingle})
		add(service.SchedulerBucket{GroupID: group.ID, Platform: group.Platform, Mode: service.SchedulerModeForced})
		if group.Platform == service.PlatformAnthropic || group.Platform == service.PlatformGemini {
			add(service.SchedulerBucket{GroupID: group.ID, Platform: group.Platform, Mode: service.SchedulerModeMixed})
		}
	}
	for _, account := range accounts {
		add(service.SchedulerBucket{GroupID: 0, Platform: account.Platform, Mode: service.SchedulerModeSingle})
		add(service.SchedulerBucket{GroupID: 0, Platform: account.Platform, Mode: service.SchedulerModeForced})
		if account.Platform == service.PlatformAnthropic || account.Platform == service.PlatformGemini {
			add(service.SchedulerBucket{GroupID: 0, Platform: account.Platform, Mode: service.SchedulerModeMixed})
		}
		for _, groupID := range account.GroupIDs {
			platform := account.Platform
			if group := groups[groupID]; group != nil && group.Platform != "" {
				platform = group.Platform
			}
			add(service.SchedulerBucket{GroupID: groupID, Platform: platform, Mode: service.SchedulerModeSingle})
			add(service.SchedulerBucket{GroupID: groupID, Platform: platform, Mode: service.SchedulerModeForced})
			if platform == service.PlatformAnthropic || platform == service.PlatformGemini {
				add(service.SchedulerBucket{GroupID: groupID, Platform: platform, Mode: service.SchedulerModeMixed})
			}
		}
	}
	out := make([]service.SchedulerBucket, 0, len(seen))
	for bucket := range seen {
		out = append(out, bucket)
	}
	return out
}

func gatewayLiteAccountsForBucket(accounts []service.Account, bucket service.SchedulerBucket) []service.Account {
	out := make([]service.Account, 0)
	for _, account := range accounts {
		if account.Platform != bucket.Platform {
			continue
		}
		if bucket.GroupID > 0 && !gatewayLiteAccountHasGroup(account, bucket.GroupID) {
			continue
		}
		out = append(out, account)
	}
	return out
}

func gatewayLiteAccountHasGroup(account service.Account, groupID int64) bool {
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}
