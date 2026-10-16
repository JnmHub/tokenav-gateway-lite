package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type groupModelSelectionContextKey struct{}

type groupModelSelectionResolution struct {
	groupID        int64
	requestedModel string
	targetModel    string
}

// NormalizeGroupModelMapping validates and canonicalizes a group's public-to-target mapping.
// It rejects ambiguous keys, direct/indirect cycles and obvious cross-provider mappings.
func NormalizeGroupModelMapping(platform string, input map[string]string) (map[string]string, error) {
	if len(input) == 0 {
		return map[string]string{}, nil
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	out := make(map[string]string, len(input))
	for rawSource, rawTarget := range input {
		source := strings.ToLower(strings.TrimSpace(rawSource))
		target := strings.TrimSpace(rawTarget)
		if source == "" || target == "" {
			return nil, fmt.Errorf("model_mapping source and target must not be empty")
		}
		if strings.Count(source, "*") > 1 || (strings.Contains(source, "*") && !strings.HasSuffix(source, "*")) {
			return nil, fmt.Errorf("model_mapping source %q may only use one trailing wildcard", rawSource)
		}
		if source == "*" {
			return nil, fmt.Errorf("model_mapping source wildcard must include a non-empty prefix")
		}
		if strings.Contains(target, "*") {
			return nil, fmt.Errorf("model_mapping target %q must be a concrete model", rawTarget)
		}
		if _, exists := out[source]; exists {
			return nil, fmt.Errorf("duplicate model_mapping source after normalization: %q", source)
		}
		if err := validateGroupModelMappingPlatform(platform, source, target); err != nil {
			return nil, err
		}
		out[source] = target
	}
	if err := validateGroupModelMappingCycles(out); err != nil {
		return nil, err
	}
	return out, nil
}

func validateGroupModelMappingPlatform(groupPlatform, source, target string) error {
	if groupPlatform == "" || groupPlatform == PlatformComposite || groupPlatform == PlatformAntigravity {
		return nil
	}
	source = strings.TrimSuffix(source, "*")
	sourcePlatform, sourceKnown := DetectModelPlatform(source)
	targetPlatform, targetKnown := DetectModelPlatform(target)
	if targetKnown && targetPlatform != groupPlatform {
		return fmt.Errorf("model_mapping target %q does not belong to %s", target, groupPlatform)
	}
	if sourceKnown && targetKnown && sourcePlatform != targetPlatform {
		return fmt.Errorf("model_mapping cannot cross providers: %q -> %q", source, target)
	}
	return nil
}

func validateGroupModelMappingCycles(mapping map[string]string) error {
	exact := make(map[string]string, len(mapping))
	for source, target := range mapping {
		if !strings.HasSuffix(source, "*") {
			exact[source] = strings.ToLower(strings.TrimSpace(target))
		}
	}
	for start := range exact {
		seen := map[string]struct{}{}
		current := start
		for {
			if _, ok := seen[current]; ok {
				return fmt.Errorf("model_mapping cycle detected at %q", current)
			}
			seen[current] = struct{}{}
			next, ok := exact[current]
			if !ok {
				break
			}
			// A self mapping is an intentional no-op for an ordinary group. Account
			// bindings reuse this validator and use the same rule to explicitly stop
			// falling through to an account-wide mapping, so it must not be treated
			// as an indirect cycle.
			if next == current {
				break
			}
			current = next
		}
	}
	return nil
}

// ResolveGroupModelMapping resolves one deterministic mapping step.
func ResolveGroupModelMapping(mapping map[string]string, model string) (string, bool) {
	original := strings.TrimSpace(model)
	if original == "" || len(mapping) == 0 {
		return original, false
	}
	normalized := strings.ToLower(original)
	if target := strings.TrimSpace(mapping[normalized]); target != "" {
		return target, target != original
	}
	type wildcard struct{ prefix, target string }
	wildcards := make([]wildcard, 0)
	for source, target := range mapping {
		source = strings.ToLower(strings.TrimSpace(source))
		target = strings.TrimSpace(target)
		if target == "" || !strings.HasSuffix(source, "*") {
			continue
		}
		prefix := strings.TrimSuffix(source, "*")
		if prefix != "" && strings.HasPrefix(normalized, prefix) {
			wildcards = append(wildcards, wildcard{prefix: prefix, target: target})
		}
	}
	if len(wildcards) == 0 {
		return original, false
	}
	sort.Slice(wildcards, func(i, j int) bool {
		if len(wildcards[i].prefix) == len(wildcards[j].prefix) {
			return wildcards[i].prefix < wildcards[j].prefix
		}
		return len(wildcards[i].prefix) > len(wildcards[j].prefix)
	})
	return wildcards[0].target, wildcards[0].target != original
}

func (g *Group) ResolveModelMapping(model string) (string, bool) {
	if g == nil {
		return strings.TrimSpace(model), false
	}
	return ResolveGroupModelMapping(g.ModelMapping, model)
}

// resolveGroupModelForSelection guarantees that a group mapping is applied only once
// during nested account selection. A different final group intentionally creates a new
// resolution so keyword rerouting uses the destination group's mapping.
func resolveGroupModelForSelection(ctx context.Context, group *Group, model string) (context.Context, string) {
	model = strings.TrimSpace(model)
	if group == nil || group.ID <= 0 || model == "" {
		return ctx, model
	}
	if target, ok := openAIGroupModelFallbackTargetForModel(ctx, group.ID, model); ok {
		return ctx, target
	}
	if resolved, ok := resolvedGroupModelFromSelectionContext(ctx, group.ID, model); ok {
		return ctx, resolved
	}
	if targets, ok := group.WeightedModelTargetsFor(model); ok {
		// 智能分流精确命中：按权重 + 目标健康选一个目标模型，精确分流
		// 优先于单目标通配符规则。当前目标通过分组兜底选择结构注入转发
		// 覆盖 ctx，使账号映射、请求体改写和选号标注全部复用既有链路。
		target := model
		if picked, pickedOK := pickWeightedModelTarget(group.ID, model, targets, nil); pickedOK {
			target = picked
			ctx = WithOpenAIGroupModelFallback(ctx, group.ID, model, model, target)
		}
		resolution := groupModelSelectionResolution{
			groupID:        group.ID,
			requestedModel: model,
			targetModel:    target,
		}
		return context.WithValue(ctx, groupModelSelectionContextKey{}, resolution), target
	}
	target, _ := group.ResolveModelMapping(model)
	resolution := groupModelSelectionResolution{
		groupID:        group.ID,
		requestedModel: model,
		targetModel:    target,
	}
	return context.WithValue(ctx, groupModelSelectionContextKey{}, resolution), target
}

func resolvedGroupModelFromSelectionContext(ctx context.Context, groupID int64, model string) (string, bool) {
	if ctx == nil || groupID <= 0 {
		return "", false
	}
	resolution, ok := ctx.Value(groupModelSelectionContextKey{}).(groupModelSelectionResolution)
	if !ok || resolution.groupID != groupID {
		return "", false
	}
	model = strings.TrimSpace(model)
	if !strings.EqualFold(model, resolution.requestedModel) && !strings.EqualFold(model, resolution.targetModel) {
		return "", false
	}
	return resolution.targetModel, true
}
