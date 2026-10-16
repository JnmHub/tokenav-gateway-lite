package service

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const GroupDisplayBrandMaxRunes = 40

var groupDisplayIconKeys = map[string]struct{}{
	"openai":    {},
	"claude":    {},
	"gemini":    {},
	"grok":      {},
	"kimi":      {},
	"glm":       {},
	"deepseek":  {},
	"qwen":      {},
	"doubao":    {},
	"minimax":   {},
	"universal": {},
	"composite": {},
}

func DefaultGroupDisplayBrand(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformOpenAI:
		return "OpenAI"
	case PlatformAnthropic:
		return "Claude"
	case PlatformGemini:
		return "Gemini"
	case PlatformGrok:
		return "Grok"
	case PlatformKimi:
		return "Kimi"
	case PlatformZhipu:
		return "智谱 GLM"
	case PlatformDeepseek:
		return "DeepSeek"
	case PlatformComposite:
		return "聚合模型"
	default:
		return "通用模型"
	}
}

func DefaultGroupDisplayIcon(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformOpenAI:
		return "openai"
	case PlatformAnthropic:
		return "claude"
	case PlatformGemini:
		return "gemini"
	case PlatformGrok:
		return "grok"
	case PlatformKimi:
		return "kimi"
	case PlatformZhipu:
		return "glm"
	case PlatformDeepseek:
		return "deepseek"
	case PlatformComposite:
		return "composite"
	default:
		return "universal"
	}
}

// NormalizeGroupDisplayIdentity 只处理用户界面展示字段；platform 仍是调度权威，二者不能互相覆盖。
func NormalizeGroupDisplayIdentity(platform, brand, icon string) (string, string, error) {
	brand = strings.TrimSpace(brand)
	if brand == "" {
		brand = DefaultGroupDisplayBrand(platform)
	}
	if utf8.RuneCountInString(brand) > GroupDisplayBrandMaxRunes {
		return "", "", fmt.Errorf("display_brand must be at most %d characters", GroupDisplayBrandMaxRunes)
	}

	icon = strings.ToLower(strings.TrimSpace(icon))
	if icon == "" {
		icon = DefaultGroupDisplayIcon(platform)
	}
	if _, ok := groupDisplayIconKeys[icon]; !ok {
		return "", "", fmt.Errorf("display_icon is not a supported built-in icon")
	}
	return brand, icon, nil
}
