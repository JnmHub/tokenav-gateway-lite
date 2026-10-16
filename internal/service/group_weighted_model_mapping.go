package service

import (
	"fmt"
	"strings"
)

// WeightedModelTarget 是智能分流映射（weighted_model_mapping）的单个目标：
// 按权重参与分流，失败后由目标健康组件自动冷却禁用。
type WeightedModelTarget struct {
	Model  string `json:"model"`
	Weight int    `json:"weight"`
}

const (
	weightedModelMappingMaxTargetsPerKey = 8
	weightedModelMappingMaxTotalTargets  = 64
	weightedModelMappingMaxModelBytes    = 100
	weightedModelMappingMinWeight        = 1
	weightedModelMappingMaxWeight        = 100
)

// NormalizeWeightedModelMapping 校验并归一化分组的智能分流映射。
// 与单目标 model_mapping 的边界：
//   - 同一公开模型只能出现在其中一边（键互斥），避免优先级歧义；
//   - 单目标映射支持通配符键，分流键必须精确，命中时精确分流优先于单目标通配符；
//   - 分流目标是终端上游模型：运行时选中后直接按目标选账号，不会再套任何
//     映射（单跳），因此目标允许与其他映射的公开键同名——同名只是"公开模型"
//     与"上游模型"撞名，两条路径互不叠加；仅禁止目标等于自身源（等于没分流）。
//
// 权重只表达相对比例（1-100），选择时自动归一化，不要求和为 100。
func NormalizeWeightedModelMapping(platform string, single map[string]string, input map[string][]WeightedModelTarget) (map[string][]WeightedModelTarget, error) {
	if len(input) == 0 {
		return map[string][]WeightedModelTarget{}, nil
	}
	normalizedPlatform := strings.ToLower(strings.TrimSpace(platform))
	// 与 fallback_model 相同的平台边界：分流的目标健康与转发覆盖机制目前只
	// 在 OpenAI 兼容热路径生效，其他平台配置了也不会被执行，直接拒绝保存。
	if normalizedPlatform != PlatformOpenAI {
		return nil, fmt.Errorf("weighted_model_mapping is only supported by OpenAI groups")
	}

	singleKeys := make(map[string]struct{}, len(single))
	for source := range single {
		singleKeys[strings.ToLower(strings.TrimSpace(source))] = struct{}{}
	}

	out := make(map[string][]WeightedModelTarget, len(input))
	totalTargets := 0
	for rawSource, rawTargets := range input {
		source := strings.ToLower(strings.TrimSpace(rawSource))
		if source == "" {
			return nil, fmt.Errorf("weighted_model_mapping source must not be empty")
		}
		if strings.Contains(source, "*") {
			return nil, fmt.Errorf("weighted_model_mapping source %q must be an exact model name (no wildcard)", rawSource)
		}
		if len(source) > weightedModelMappingMaxModelBytes {
			return nil, fmt.Errorf("weighted_model_mapping source %q exceeds %d bytes", rawSource, weightedModelMappingMaxModelBytes)
		}
		if _, exists := singleKeys[source]; exists {
			return nil, fmt.Errorf("model %q is already mapped by model_mapping; a public model can only exist in one of the two mappings", source)
		}
		if len(rawTargets) < 2 {
			return nil, fmt.Errorf("weighted_model_mapping %q needs at least 2 targets (use model_mapping for a single target)", source)
		}
		if len(rawTargets) > weightedModelMappingMaxTargetsPerKey {
			return nil, fmt.Errorf("weighted_model_mapping %q exceeds %d targets", source, weightedModelMappingMaxTargetsPerKey)
		}
		totalTargets += len(rawTargets)
		if totalTargets > weightedModelMappingMaxTotalTargets {
			return nil, fmt.Errorf("weighted_model_mapping exceeds %d total targets per group", weightedModelMappingMaxTotalTargets)
		}
		if _, exists := out[source]; exists {
			return nil, fmt.Errorf("duplicate weighted_model_mapping source after normalization: %q", source)
		}

		targets := make([]WeightedModelTarget, 0, len(rawTargets))
		seenTargets := make(map[string]struct{}, len(rawTargets))
		for _, rawTarget := range rawTargets {
			target := strings.TrimSpace(rawTarget.Model)
			if target == "" {
				return nil, fmt.Errorf("weighted_model_mapping %q has an empty target model", source)
			}
			if strings.Contains(target, "*") {
				return nil, fmt.Errorf("weighted_model_mapping target %q must be a concrete model", target)
			}
			if len(target) > weightedModelMappingMaxModelBytes {
				return nil, fmt.Errorf("weighted_model_mapping target %q exceeds %d bytes", target, weightedModelMappingMaxModelBytes)
			}
			targetKey := strings.ToLower(target)
			if _, duplicate := seenTargets[targetKey]; duplicate {
				return nil, fmt.Errorf("weighted_model_mapping %q has duplicate target %q", source, target)
			}
			seenTargets[targetKey] = struct{}{}
			// 分流命中后目标即终端上游模型，运行时不会再用它查任何映射
			// （resolveGroupModelForSelection 直接返回选中目标），因此目标与
			// 单目标/分流映射的公开键同名是合法配置，不存在"第二跳"。仍禁止
			// 目标等于自身源：那等于把流量原样分回公开键，多半是配置手滑。
			if strings.EqualFold(target, source) {
				return nil, fmt.Errorf("weighted_model_mapping %q cannot target itself", source)
			}
			if rawTarget.Weight < weightedModelMappingMinWeight || rawTarget.Weight > weightedModelMappingMaxWeight {
				return nil, fmt.Errorf("weighted_model_mapping target %q weight must be between %d and %d", target, weightedModelMappingMinWeight, weightedModelMappingMaxWeight)
			}
			if err := validateGroupModelMappingPlatform(normalizedPlatform, source, target); err != nil {
				return nil, fmt.Errorf("weighted_model_mapping: %v", err)
			}
			targets = append(targets, WeightedModelTarget{Model: target, Weight: rawTarget.Weight})
		}
		out[source] = targets
	}
	return out, nil
}

// WeightedModelTargetsFor 精确命中分组智能分流映射；键在归一化时已统一小写。
func (g *Group) WeightedModelTargetsFor(model string) ([]WeightedModelTarget, bool) {
	if g == nil || len(g.WeightedModelMapping) == 0 {
		return nil, false
	}
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return nil, false
	}
	targets, ok := g.WeightedModelMapping[strings.ToLower(trimmed)]
	if !ok || len(targets) == 0 {
		return nil, false
	}
	return targets, true
}

// CloneWeightedModelMapping 深拷贝智能分流映射，供 DTO 序列化隔离内部状态。
func CloneWeightedModelMapping(mapping map[string][]WeightedModelTarget) map[string][]WeightedModelTarget {
	if mapping == nil {
		return map[string][]WeightedModelTarget{}
	}
	out := make(map[string][]WeightedModelTarget, len(mapping))
	for source, targets := range mapping {
		cloned := make([]WeightedModelTarget, len(targets))
		copy(cloned, targets)
		out[source] = cloned
	}
	return out
}

func cloneWeightedModelMapping(mapping map[string][]WeightedModelTarget) map[string][]WeightedModelTarget {
	if mapping == nil {
		return map[string][]WeightedModelTarget{}
	}
	out := make(map[string][]WeightedModelTarget, len(mapping))
	for source, targets := range mapping {
		cloned := make([]WeightedModelTarget, len(targets))
		copy(cloned, targets)
		out[source] = cloned
	}
	return out
}
