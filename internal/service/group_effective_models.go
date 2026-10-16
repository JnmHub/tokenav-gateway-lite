package service

import (
	"context"
	"sort"
	"strings"
)

// GetAvailableGroupMappedModels returns only customer-facing exact aliases whose
// targets are backed by a persisted active account in the final group. It keeps
// the public model list aligned with the same effective model projection sent to
// the control-plane without turning temporary cooldowns into model removals.
func (s *GatewayService) GetAvailableGroupMappedModels(ctx context.Context, group *Group) []string {
	if s == nil || s.accountRepo == nil || group == nil || group.ID <= 0 || len(group.ModelMapping) == 0 {
		return nil
	}
	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, group.ID)
	if err != nil || len(accounts) == 0 {
		return nil
	}
	effective := EffectiveModelsForGroup(group, accounts, nil)
	effectiveSet := make(map[string]struct{}, len(effective))
	for _, model := range effective {
		effectiveSet[model] = struct{}{}
	}
	aliases := make([]string, 0, len(group.ModelMapping))
	for source := range group.ModelMapping {
		source = strings.TrimSpace(source)
		if source == "" || strings.Contains(source, "*") {
			continue
		}
		if _, ok := effectiveSet[source]; ok {
			aliases = append(aliases, source)
		}
	}
	sort.Strings(aliases)
	return aliases
}

// EffectiveModelsForGroup returns the stable customer-facing model set for a
// gateway group. Runtime-only cooldowns (for example a short 429 window) are
// intentionally ignored: only persisted account status and schedulable state
// may add or remove a model from the control-plane mirror.
func EffectiveModelsForGroup(group *Group, accounts []Account, knownModels []string) []string {
	models := make(map[string]struct{})
	if group == nil || group.ID <= 0 {
		return []string{}
	}

	defaultCandidates := normalizeEffectiveModelIDs(defaultModelsListCandidateIDs(group.Platform))
	knownCandidates := normalizeEffectiveModelIDs(knownModels)
	for i := range accounts {
		account := &accounts[i]
		if !accountEligibleForEffectiveGroup(group, account) {
			continue
		}

		bindingMapping := account.AccountGroupModelMapping(group.ID)
		accountMapping := account.GetModelMapping()
		if len(bindingMapping) == 0 && len(accountMapping) == 0 {
			// An empty mapping means the account accepts the platform defaults. Do
			// not feed the node-wide catalog into this path because it may contain
			// models from other providers.
			for _, model := range defaultCandidates {
				addEffectiveGroupModel(models, group, account, model)
			}
			continue
		}

		// A binding mapping has precedence for matching requests, but unlisted
		// models still fall back to the account-wide mapping. Publish exact keys
		// from both scopes so /v1/models and the control-plane mirror describe
		// the same reachable customer models as the scheduler.
		for _, mapping := range []map[string]string{bindingMapping, accountMapping} {
			// Exact mapping keys are already customer-facing model IDs. Wildcard
			// keys are expanded against known concrete IDs and are never exposed as
			// fake model names such as "gpt-5.*".
			for pattern := range mapping {
				pattern = strings.TrimSpace(pattern)
				if pattern == "" || strings.Contains(pattern, "*") {
					continue
				}
				addEffectiveGroupModel(models, group, account, pattern)
			}
		}
		for _, model := range defaultCandidates {
			addEffectiveGroupModel(models, group, account, model)
		}
		for _, model := range knownCandidates {
			addEffectiveGroupModel(models, group, account, model)
		}
	}

	// 分组映射公开的是客户可请求的源模型，但只有目标模型在真实可调度
	// 账号中存在时才发布，避免主站把无法路由的别名展示为可用。
	for source, target := range group.ModelMapping {
		if strings.Contains(source, "*") || strings.TrimSpace(source) == "" {
			continue
		}
		for i := range accounts {
			account := &accounts[i]
			if accountEligibleForEffectiveGroup(group, account) && account.IsModelSupportedForGroup(group.ID, target) {
				if group.AllowImageGeneration || !isEffectiveImageOrVideoModel(target) {
					models[strings.TrimSpace(source)] = struct{}{}
				}
				break
			}
		}
	}

	// Keep the effective-model mirror consistent with /v1/models: an enabled
	// custom list is a real allowlist. New empty lists are rejected at save time;
	// legacy empty lists remain fail-closed and therefore publish no models.
	if group.CustomModelsListEnabled() {
		allowed := normalizeEffectiveModelIDs(group.ModelsListConfig.Models)
		for model := range models {
			if !effectiveModelMatchesAny(allowed, model) {
				delete(models, model)
			}
		}
	}

	out := make([]string, 0, len(models))
	for model := range models {
		out = append(out, model)
	}
	sort.Strings(out)
	if out == nil {
		return []string{}
	}
	return out
}

// EffectiveModelsForCompositeGroup 汇总聚合分组内各真实平台的可调度账号，
// 并只暴露能被同一份路由规则解释的模型。显式 exact（精确）别名会按真实
// 上游模型校验账号能力；prefix（前缀）规则只展开已有的具体候选，不制造假模型名。
func EffectiveModelsForCompositeGroup(group *Group, accounts []Account, knownModels []string, routes []CompositeModelRoute) []string {
	if group == nil || group.ID <= 0 || group.Platform != PlatformComposite {
		return []string{}
	}
	models := make(map[string]struct{})
	for _, platform := range []string{
		PlatformAnthropic, PlatformGemini, PlatformOpenAI, PlatformAntigravity,
		PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek,
	} {
		providerGroup := *group
		providerGroup.Platform = platform
		for _, model := range EffectiveModelsForGroup(&providerGroup, accounts, knownModels) {
			if detected, ok := DetectModelPlatform(model); ok && detected == platform {
				models[model] = struct{}{}
			}
		}
	}

	for _, route := range routes {
		if !route.Enabled || !isConcreteRequestPlatform(route.TargetPlatform) {
			continue
		}
		publicModel := strings.TrimSpace(route.PublicModel)
		upstreamModel := strings.TrimSpace(route.UpstreamModel)
		if publicModel == "" {
			continue
		}
		if upstreamModel == "" {
			upstreamModel = publicModel
		}
		if route.MatchType == CompositeRouteMatchExact {
			if compositeGroupHasModelAccount(group, accounts, route.TargetPlatform, upstreamModel) {
				models[publicModel] = struct{}{}
			}
			continue
		}
		for _, candidate := range knownModels {
			candidate = strings.TrimSpace(candidate)
			if strings.HasPrefix(candidate, publicModel) && compositeGroupHasModelAccount(group, accounts, route.TargetPlatform, upstreamModel) {
				models[candidate] = struct{}{}
			}
		}
	}

	if group.CustomModelsListEnabled() {
		allowed := normalizeEffectiveModelIDs(group.ModelsListConfig.Models)
		for model := range models {
			if !effectiveModelMatchesAny(allowed, model) {
				delete(models, model)
			}
		}
	}
	out := make([]string, 0, len(models))
	for model := range models {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func compositeGroupHasModelAccount(group *Group, accounts []Account, platform, model string) bool {
	for i := range accounts {
		account := &accounts[i]
		if account.Platform != platform || !account.IsActive() || !account.Schedulable ||
			!effectiveAccountContainsGroup(account, group.ID) {
			continue
		}
		if group.RequireOAuthOnly && !account.IsOAuth() {
			continue
		}
		if group.RequirePrivacySet && !account.IsPrivacySet() {
			continue
		}
		if account.IsModelSupportedForGroup(group.ID, model) {
			return true
		}
	}
	return false
}

func accountEligibleForEffectiveGroup(group *Group, account *Account) bool {
	if group == nil || account == nil || !account.IsActive() || !account.Schedulable {
		return false
	}
	if strings.TrimSpace(group.Platform) != "" && account.Platform != group.Platform {
		return false
	}
	if !effectiveAccountContainsGroup(account, group.ID) {
		return false
	}
	if group.RequireOAuthOnly && !account.IsOAuth() {
		return false
	}
	if group.RequirePrivacySet && !account.IsPrivacySet() {
		return false
	}
	return true
}

func effectiveAccountContainsGroup(account *Account, groupID int64) bool {
	if account == nil || groupID <= 0 {
		return false
	}
	for _, candidate := range account.GroupIDs {
		if candidate == groupID {
			return true
		}
	}
	for _, binding := range account.AccountGroups {
		if binding.GroupID == groupID {
			return true
		}
	}
	return false
}

func addEffectiveGroupModel(models map[string]struct{}, group *Group, account *Account, model string) {
	model = strings.TrimSpace(model)
	if model == "" || strings.Contains(model, "*") || !account.IsModelSupportedForGroup(group.ID, model) {
		return
	}
	if !group.AllowImageGeneration && isEffectiveImageOrVideoModel(model) {
		return
	}
	if !effectiveModelAllowedByGroupScope(group, model) {
		return
	}
	models[model] = struct{}{}
}

func effectiveModelAllowedByGroupScope(group *Group, model string) bool {
	if group == nil || group.Platform != PlatformAntigravity || len(group.SupportedModelScopes) == 0 {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, scope := range group.SupportedModelScopes {
		switch strings.ToLower(strings.TrimSpace(scope)) {
		case "claude":
			if strings.HasPrefix(normalized, "claude-") {
				return true
			}
		case "gemini_text":
			if strings.HasPrefix(normalized, "gemini-") && !isEffectiveImageOrVideoModel(normalized) {
				return true
			}
		case "gemini_image":
			if strings.HasPrefix(normalized, "gemini-") && isEffectiveImageOrVideoModel(normalized) {
				return true
			}
		}
	}
	return false
}

func isEffectiveImageOrVideoModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalized, "gpt-image-") ||
		strings.HasPrefix(normalized, "dall-e") ||
		strings.HasPrefix(normalized, "grok-imagine") ||
		strings.HasSuffix(normalized, "-image") ||
		strings.Contains(normalized, "-image-")
}

func effectiveModelMatchesAny(patterns []string, model string) bool {
	for _, pattern := range patterns {
		if ModelPatternMatches(pattern, model) {
			return true
		}
	}
	return false
}

func normalizeEffectiveModelIDs(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	return out
}
