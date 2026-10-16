package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	maxGroupNameRunes            = 100
	duplicateGroupInactiveStatus = "inactive"
)

func duplicateGroupOperationID(sourceID int64, actorScope, operationKey string) string {
	operationKey = strings.TrimSpace(operationKey)
	if operationKey == "" {
		return ""
	}
	actorScope = strings.TrimSpace(actorScope)
	if actorScope == "" {
		actorScope = "admin:0"
	}
	payload := "admin.groups.duplicate\x00" + actorScope + "\x00" + strconv.FormatInt(sourceID, 10) + "\x00" + operationKey
	digest := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", digest)
}

func duplicateGroupName(sourceName string, copyNumber int) string {
	if copyNumber < 1 {
		copyNumber = 1
	}
	suffix := " (Copy)"
	if copyNumber > 1 {
		suffix = fmt.Sprintf(" (Copy %d)", copyNumber)
	}
	baseRunes := []rune(strings.TrimSpace(sourceName))
	maxBaseRunes := maxGroupNameRunes - len([]rune(suffix))
	if maxBaseRunes < 0 {
		maxBaseRunes = 0
	}
	if len(baseRunes) > maxBaseRunes {
		baseRunes = baseRunes[:maxBaseRunes]
	}
	return string(baseRunes) + suffix
}

func cloneGroupValuePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneGroupVideoModelPrices(value map[string]map[string]float64) map[string]map[string]float64 {
	return NormalizeVideoModelPrices(value)
}

func cloneGroupModelRouting(value map[string][]int64) map[string][]int64 {
	if value == nil {
		return nil
	}
	cloned := make(map[string][]int64, len(value))
	for model, accountIDs := range value {
		cloned[model] = append([]int64(nil), accountIDs...)
	}
	return cloned
}

func cloneGroupMessagesDispatchModelConfig(value OpenAIMessagesDispatchModelConfig) OpenAIMessagesDispatchModelConfig {
	cloned := value
	if value.ExactModelMappings != nil {
		cloned.ExactModelMappings = make(map[string]string, len(value.ExactModelMappings))
		for requestedModel, mappedModel := range value.ExactModelMappings {
			cloned.ExactModelMappings[requestedModel] = mappedModel
		}
	}
	return cloned
}

func cloneGroupForDuplicate(source *Group, operationID string) *Group {
	return &Group{
		Name:                            duplicateGroupName(source.Name, 1),
		Description:                     source.Description,
		Platform:                        source.Platform,
		DisplayBrand:                    source.DisplayBrand,
		DisplayIcon:                     source.DisplayIcon,
		RateMultiplier:                  source.RateMultiplier,
		PeakRateEnabled:                 source.PeakRateEnabled,
		PeakStart:                       source.PeakStart,
		PeakEnd:                         source.PeakEnd,
		PeakRateMultiplier:              source.PeakRateMultiplier,
		PeakTimezone:                    source.PeakTimezone,
		IsExclusive:                     source.IsExclusive,
		LocalOnly:                       source.LocalOnly,
		SchedulingMode:                  source.SchedulingMode,
		PerUserConcurrency:              source.PerUserConcurrency,
		Status:                          duplicateGroupInactiveStatus,
		DuplicateOperationID:            operationID,
		SubscriptionType:                source.SubscriptionType,
		DailyLimitUSD:                   cloneGroupValuePointer(source.DailyLimitUSD),
		WeeklyLimitUSD:                  cloneGroupValuePointer(source.WeeklyLimitUSD),
		MonthlyLimitUSD:                 cloneGroupValuePointer(source.MonthlyLimitUSD),
		DefaultValidityDays:             source.DefaultValidityDays,
		AllowImageGeneration:            source.AllowImageGeneration,
		ImageRateIndependent:            source.ImageRateIndependent,
		ImageRateMultiplier:             source.ImageRateMultiplier,
		ImagePrice1K:                    cloneGroupValuePointer(source.ImagePrice1K),
		ImagePrice2K:                    cloneGroupValuePointer(source.ImagePrice2K),
		ImagePrice4K:                    cloneGroupValuePointer(source.ImagePrice4K),
		VideoRateIndependent:            source.VideoRateIndependent,
		VideoRateMultiplier:             source.VideoRateMultiplier,
		VideoPrice480P:                  cloneGroupValuePointer(source.VideoPrice480P),
		VideoPrice720P:                  cloneGroupValuePointer(source.VideoPrice720P),
		VideoPrice1080P:                 cloneGroupValuePointer(source.VideoPrice1080P),
		VideoModelPrices:                cloneGroupVideoModelPrices(source.VideoModelPrices),
		WebSearchPricePerCall:           cloneGroupValuePointer(source.WebSearchPricePerCall),
		SearchPricePer1k:                cloneGroupValuePointer(source.SearchPricePer1k),
		AudioRealtimePricePerMin:        cloneGroupValuePointer(source.AudioRealtimePricePerMin),
		AudioTTSPricePerMillionChars:    cloneGroupValuePointer(source.AudioTTSPricePerMillionChars),
		AudioSTTPricePerHour:            cloneGroupValuePointer(source.AudioSTTPricePerHour),
		AllowOpenAILongContextBilling:   source.AllowOpenAILongContextBilling,
		AllowBlockedKeywordRequests:     source.AllowBlockedKeywordRequests,
		ClaudeCodeOnly:                  source.ClaudeCodeOnly,
		FallbackGroupID:                 cloneGroupValuePointer(source.FallbackGroupID),
		FallbackGroupIDOnInvalidRequest: cloneGroupValuePointer(source.FallbackGroupIDOnInvalidRequest),
		FailoverGroupID:                 cloneGroupValuePointer(source.FailoverGroupID),
		FailoverStrategy:                source.FailoverStrategy,
		ModelRouting:                    cloneGroupModelRouting(source.ModelRouting),
		ModelMapping:                    cloneGroupModelMapping(source.ModelMapping),
		WeightedModelMapping:            CloneWeightedModelMapping(source.WeightedModelMapping),
		FallbackModel:                   source.FallbackModel,
		BillingModelSource:              source.BillingModelSource,
		ModelRoutingEnabled:             source.ModelRoutingEnabled,
		MCPXMLInject:                    source.MCPXMLInject,
		SupportedModelScopes:            append([]string(nil), source.SupportedModelScopes...),
		SortOrder:                       source.SortOrder,
		AllowMessagesDispatch:           source.AllowMessagesDispatch,
		RequireOAuthOnly:                source.RequireOAuthOnly,
		RequirePrivacySet:               source.RequirePrivacySet,
		DefaultMappedModel:              source.DefaultMappedModel,
		MessagesDispatchModelConfig:     cloneGroupMessagesDispatchModelConfig(source.MessagesDispatchModelConfig),
		ModelsListConfig: GroupModelsListConfig{
			Enabled:             source.ModelsListConfig.Enabled,
			Models:              append([]string(nil), source.ModelsListConfig.Models...),
			ResponseModelMode:   source.ModelsListConfig.ResponseModelMode,
			ResponseModelCustom: source.ModelsListConfig.ResponseModelCustom,
		},
		RPMLimit:                source.RPMLimit,
		MaxReasoningEffort:      source.MaxReasoningEffort,
		ReasoningEffortMappings: append([]ReasoningEffortMapping(nil), source.ReasoningEffortMappings...),
	}
}

func cloneGroupModelMapping(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(value))
	for source, target := range value {
		out[source] = target
	}
	return out
}

// RecoverDuplicateGroup 只读查找相同管理员、源分组和幂等键已经提交的副本。
func (s *adminServiceImpl) RecoverDuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error) {
	operationID := duplicateGroupOperationID(id, actorScope, operationKey)
	if operationID == "" {
		return nil, nil
	}
	if s.groupDuplicateRepo == nil {
		return nil, errors.New("group duplicate repository is not configured")
	}
	group, err := s.groupDuplicateRepo.FindByDuplicateOperationID(ctx, operationID)
	if err != nil {
		return nil, fmt.Errorf("find duplicate group operation: %w", err)
	}
	if group == nil {
		return nil, nil
	}
	hydrated, err := s.groupRepo.GetByID(ctx, group.ID)
	if err != nil {
		return nil, fmt.Errorf("load recovered duplicate group: %w", err)
	}
	return hydrated, nil
}

// DuplicateGroup 原子创建停用分组副本，并保留源分组每个账号绑定的优先级。
func (s *adminServiceImpl) DuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error) {
	normalizedKey, err := NormalizeIdempotencyKey(operationKey)
	if err != nil {
		return nil, err
	}
	if normalizedKey == "" {
		return nil, ErrIdempotencyKeyRequired
	}

	existing, err := s.RecoverDuplicateGroup(ctx, id, actorScope, normalizedKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	source, err := s.groupRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.groupDuplicateRepo == nil {
		return nil, errors.New("group duplicate repository is not configured")
	}

	duplicate := cloneGroupForDuplicate(source, duplicateGroupOperationID(id, actorScope, normalizedKey))
	for copyNumber := 1; ; copyNumber++ {
		duplicate.Name = duplicateGroupName(source.Name, copyNumber)
		duplicate.ID = 0
		duplicate.CreatedAt = time.Time{}
		duplicate.UpdatedAt = time.Time{}
		if err := s.groupDuplicateRepo.CreateFromSource(ctx, duplicate, source.ID); err == nil {
			if source.Platform == PlatformComposite {
				if refreshErr := s.refreshCompositeRoutes(ctx, duplicate.ID); refreshErr != nil {
					return nil, refreshErr
				}
			}
			hydrated, loadErr := s.groupRepo.GetByID(ctx, duplicate.ID)
			if loadErr != nil {
				return nil, fmt.Errorf("load duplicate group: %w", loadErr)
			}
			return hydrated, nil
		} else if !errors.Is(err, ErrGroupExists) {
			return nil, fmt.Errorf("create duplicate group: %w", err)
		}

		// 唯一冲突可能来自名称，也可能来自 operation ID。先恢复结果，
		// 只有确实是名称冲突时才尝试下一个副本编号。
		recovered, recoverErr := s.RecoverDuplicateGroup(ctx, id, actorScope, normalizedKey)
		if recoverErr != nil {
			return nil, recoverErr
		}
		if recovered != nil {
			return recovered, nil
		}
	}
}
