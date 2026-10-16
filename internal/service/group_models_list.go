package service

import (
	"fmt"
	"strings"
)

func normalizeGroupModelsListConfig(cfg GroupModelsListConfig) GroupModelsListConfig {
	out := GroupModelsListConfig{
		Enabled:             cfg.Enabled,
		ResponseModelMode:   normalizeGroupResponseModelMode(cfg.ResponseModelMode),
		ResponseModelCustom: strings.TrimSpace(cfg.ResponseModelCustom),
	}
	if len(cfg.Models) == 0 {
		return out
	}

	seen := make(map[string]struct{}, len(cfg.Models))
	out.Models = make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out.Models = append(out.Models, model)
	}
	if len(out.Models) == 0 {
		out.Models = nil
	}
	return out
}

func validateGroupModelsListConfig(cfg GroupModelsListConfig) error {
	if cfg.Enabled && len(normalizeGroupModelsListConfig(cfg).Models) == 0 {
		return fmt.Errorf("models is required when the custom model list is enabled")
	}
	mode := normalizeGroupResponseModelMode(cfg.ResponseModelMode)
	if !validGroupResponseModelMode(mode) {
		return fmt.Errorf("response_model_mode must be request, upstream, or custom")
	}
	custom := strings.TrimSpace(cfg.ResponseModelCustom)
	if len([]rune(custom)) > responseModelCustomMaxRunes {
		return fmt.Errorf("response_model_custom must not exceed %d characters", responseModelCustomMaxRunes)
	}
	if mode == ResponseModelModeCustom && custom == "" {
		return fmt.Errorf("response_model_custom is required when response_model_mode is custom")
	}
	return nil
}

func (g *Group) CustomModelsListEnabled() bool {
	return g != nil && g.ModelsListConfig.Enabled
}

// IsModelAllowedByCustomList checks the request-side model gate for a group.
// The same exact/prefix wildcard rules are used by /v1/models so a model that
// is hidden from discovery cannot be called through a manually crafted body.
func (g *Group) IsModelAllowedByCustomList(model string) bool {
	model = strings.TrimSpace(model)
	if g == nil || model == "" || !g.ModelsListConfig.Enabled {
		return true
	}
	for _, pattern := range g.ModelsListConfig.Models {
		if ModelPatternMatches(pattern, model) {
			return true
		}
	}
	return false
}

// ModelPatternMatches 是模型白名单和模型列表展示共用的匹配规则。
// 统一大小写与尾部通配符语义，避免“列表显示了但请求被拒绝”或反向不一致。
func ModelPatternMatches(pattern, model string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	if pattern == "" || model == "" {
		return false
	}
	if pattern == "*" || pattern == model {
		return true
	}
	return strings.HasSuffix(pattern, "*") && strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
}
