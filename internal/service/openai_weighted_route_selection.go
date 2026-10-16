package service

import (
	"context"
	"log/slog"
	"strings"
)

// 智能分流在 OpenAI 调度器内的候选轮转。
//
// 触发条件与 openAIGroupFallbackModelForSelection 完全一致（文本请求、
// 非 previous_response_id 粘性、非图片/compact、OpenAI 平台），保证两条
// 兜底路径的边界相同；区别只是候选来源是分组的加权目标列表而非单个
// FallbackModel，且尝试顺序按权重降序确定。
//
// "无可用账号"只轮转目标、不记健康失败：账号全忙/全被禁用不代表目标模型
// 本身坏了，把它写成目标冷却会让健康目标被误禁用。

// nextWeightedRouteCandidateForSelection 返回下一个可尝试的分流目标及
// 带转发覆盖的 ctx。exclude 是本请求已尝试过的目标（含当前目标）。
func (s *OpenAIGatewayService) nextWeightedRouteCandidateForSelection(
	ctx context.Context,
	groupID *int64,
	originalModel, currentModel, previousResponseID, requiredImageModel string,
	requireCompact bool,
	platform string,
	exclude map[string]struct{},
) (context.Context, string, bool) {
	if !openAIGroupModelFallbackEnabled(ctx) || openAIGroupModelFallbackPolicySuppressed(ctx) ||
		groupID == nil || *groupID <= 0 || strings.TrimSpace(previousResponseID) != "" ||
		strings.TrimSpace(requiredImageModel) != "" || requireCompact ||
		normalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI {
		return ctx, "", false
	}
	group := finalMappingGroupFromContext(ctx, *groupID)
	if group == nil {
		var err error
		group, err = s.getOpenAISelectionGroup(ctx, *groupID)
		if err != nil {
			slog.Warn("openai_scheduler_weighted_route_group_lookup_failed", "group_id", *groupID, "error", err)
			return ctx, "", false
		}
	}
	if group == nil || group.Platform != PlatformOpenAI {
		return ctx, "", false
	}
	targets, ok := group.WeightedModelTargetsFor(originalModel)
	if !ok {
		return ctx, "", false
	}
	if exclude == nil {
		exclude = make(map[string]struct{})
	}
	if current := strings.TrimSpace(currentModel); current != "" {
		exclude[strings.ToLower(current)] = struct{}{}
	}
	for _, candidate := range orderedWeightedModelCandidates(targets, exclude) {
		// 冷却中的目标跳过（不消耗半开探测租约；探测租约留给正常选号路径）。
		if isModelRouteCooling(group.ID, originalModel, candidate) {
			continue
		}
		return WithOpenAIGroupModelFallback(ctx, *groupID, originalModel, originalModel, candidate), candidate, true
	}
	return ctx, "", false
}

// tryWeightedRouteCandidatesForSelection 在原模型无可用账号时逐个尝试剩余
// 分流目标。全部耗尽时清掉 ctx 内的转发目标值（不设 attempted 标记），
// 让分组 FallbackModel 仍按原有语义做最后兜底，并返回原始错误。
func (s *OpenAIGatewayService) tryWeightedRouteCandidatesForSelection(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	originalModel, currentModel, requiredImageModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
) (context.Context, *AccountSelectionResult, OpenAIAccountScheduleDecision) {
	tried := map[string]struct{}{}
	for {
		candidateCtx, candidate, ok := s.nextWeightedRouteCandidateForSelection(
			ctx, groupID, originalModel, currentModel, previousResponseID, requiredImageModel,
			requireCompact, platform, tried,
		)
		if !ok {
			break
		}
		tried[strings.ToLower(candidate)] = struct{}{}
		candidateSelection, candidateDecision, candidateErr := s.selectAccountWithSchedulerOnce(
			candidateCtx, groupID, previousResponseID, sessionHash, candidate, "",
			excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, false, platform,
		)
		if candidateErr == nil && candidateSelection != nil && candidateSelection.Account != nil {
			annotateOpenAISelectionModel(candidateSelection, candidate, candidateCtx)
			slog.Info("openai_scheduler_weighted_route_selected",
				"group_id", derefGroupID(groupID),
				"source_model", originalModel,
				"target_model", candidate,
				"account_id", selectionAccountID(candidateSelection),
			)
			return ctx, candidateSelection, candidateDecision
		}
		if candidateErr == nil {
			candidateErr = ErrNoAvailableAccounts
		}
		slog.Info("openai_scheduler_weighted_route_unavailable",
			"group_id", derefGroupID(groupID),
			"source_model", originalModel,
			"target_model", candidate,
			"error", candidateErr.Error(),
		)
	}
	return clearOpenAIGroupModelFallbackValue(ctx), nil, OpenAIAccountScheduleDecision{}
}
