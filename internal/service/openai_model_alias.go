package service

import "strings"

func lastOpenAIModelSegment(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if strings.Contains(model, "/") {
		parts := strings.Split(model, "/")
		model = parts[len(parts)-1]
	}
	return strings.TrimSpace(model)
}

func canonicalizeOpenAIModelAliasSpelling(model string) string {
	model = strings.ToLower(lastOpenAIModelSegment(model))
	if model == "" {
		return ""
	}

	normalized := strings.ReplaceAll(model, "_", "-")
	normalized = strings.Join(strings.Fields(normalized), "-")
	for strings.Contains(normalized, "--") {
		normalized = strings.ReplaceAll(normalized, "--", "-")
	}

	if strings.HasPrefix(normalized, "gpt5") {
		normalized = "gpt-5" + strings.TrimPrefix(normalized, "gpt5")
	}
	if strings.HasPrefix(normalized, "gpt6") {
		normalized = "gpt-6" + strings.TrimPrefix(normalized, "gpt6")
	}
	if !strings.HasPrefix(normalized, "gpt-") && !strings.Contains(normalized, "codex") {
		return ""
	}

	replacements := []struct {
		from string
		to   string
	}{
		{"gpt-5.4mini", "gpt-5.4-mini"},
		{"gpt-5.4nano", "gpt-5.4-nano"},
		{"gpt-5.3-codexspark", "gpt-5.3-codex-spark"},
		{"gpt-5.3codexspark", "gpt-5.3-codex-spark"},
		{"gpt-5.3codex", "gpt-5.3-codex"},
		{"gpt-6astra", "gpt-6-astra"},
		{"gpt-6astar", "gpt-6-astar"},
	}
	for _, replacement := range replacements {
		normalized = strings.ReplaceAll(normalized, replacement.from, replacement.to)
	}
	return normalized
}

func normalizeKnownOpenAICodexModel(model string) string {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if normalized == "" {
		return ""
	}

	if mapped := getNormalizedCodexModel(normalized); mapped != "" {
		return mapped
	}
	if strings.HasSuffix(normalized, "-openai-compact") {
		if mapped := getNormalizedCodexModel(strings.TrimSuffix(normalized, "-openai-compact")); mapped != "" {
			return mapped
		}
	}

	switch {
	// GPT-6 Astra：官方 API id 是 gpt-6-astra；astar 是常见误记，作为别名
	// 收敛到 astra，避免按未知型号拒绝计价。裸 gpt-6 前缀显式拒绝，防止
	// 未来 gpt-6-mini 等小型号被误按旗舰价超收。
	case matchesKnownOpenAIModelVariant(normalized, "gpt-6-astra"):
		return "gpt-6-astra"
	case normalized == "gpt-6-astar" || strings.HasPrefix(normalized, "gpt-6-astar-"):
		return "gpt-6-astra"
	case strings.HasPrefix(normalized, "gpt-6"):
		return ""
	case matchesKnownOpenAIModelVariant(normalized, "gpt-5.6-sol"):
		return "gpt-5.6-sol"
	case matchesKnownOpenAIModelVariant(normalized, "gpt-5.6-terra"):
		return "gpt-5.6-terra"
	case matchesKnownOpenAIModelVariant(normalized, "gpt-5.6-luna"):
		return "gpt-5.6-luna"
	case matchesKnownOpenAIModelVariant(normalized, "gpt-5.6"):
		// OpenAI 的无后缀通用名称当前指向 Sol；显式 terra/luna 已在上面
		// 优先命中，未知后缀仍由下一分支拒绝，避免错误猜测型号。
		return "gpt-5.6-sol"
	case strings.HasPrefix(normalized, "gpt-5.6"):
		return ""
	case matchesKnownOpenAIModelVariant(normalized, "gpt-5.5-pro"):
		return "gpt-5.5-pro"
	case matchesKnownOpenAIModelVariant(normalized, "gpt-5.5"):
		return "gpt-5.5"
	case strings.HasPrefix(normalized, "gpt-5.5"):
		return ""
	case strings.Contains(normalized, "gpt-5.4-mini"):
		return "gpt-5.4-mini"
	case strings.Contains(normalized, "gpt-5.4-nano"):
		return "gpt-5.4-nano"
	case strings.Contains(normalized, "gpt-5.4"):
		return "gpt-5.4"
	case strings.Contains(normalized, "gpt-5.2"):
		return "gpt-5.2"
	case strings.Contains(normalized, "gpt-5.3-codex-spark"):
		return "gpt-5.3-codex-spark"
	case strings.Contains(normalized, "gpt-5.3-codex"):
		return "gpt-5.3-codex"
	case strings.Contains(normalized, "gpt-5.3"):
		return "gpt-5.3-codex"
	case strings.Contains(normalized, "codex"):
		return "gpt-5.3-codex"
	case strings.Contains(normalized, "gpt-5"):
		return "gpt-5.4"
	default:
		return ""
	}
}

// isOpenAIGPT56Model accepts raw, mapped, dated, and reasoning-suffixed names.
// GPT-5.6 and GPT-6 Astra are the current families where OpenAI defines "max"
// separately from "xhigh", so callers must know the model before normalizing
// effort.
func isOpenAIGPT56Model(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	if matchesKnownOpenAIModelVariant(normalized, "gpt-5.6") {
		return true
	}
	if matchesKnownOpenAIModelVariant(normalized, "gpt-6-astra") || normalized == "gpt-6-astar" {
		return true
	}
	for _, prefix := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"-") {
			return true
		}
	}
	return false
}

func matchesKnownOpenAIModelVariant(model, base string) bool {
	if model == base {
		return true
	}
	if !strings.HasPrefix(model, base+"-") {
		return false
	}
	suffix := strings.TrimPrefix(model, base+"-")
	parts := strings.Split(suffix, "-")
	if len(parts) == 1 {
		return isOpenAIReasoningVariant(parts[0]) || isEightDigitDate(parts[0])
	}
	if len(parts) == 3 && len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2 {
		return allDigits(parts[0]) && allDigits(parts[1]) && allDigits(parts[2])
	}
	if len(parts) == 4 && len(parts[0]) == 4 && len(parts[1]) == 2 && len(parts[2]) == 2 {
		return allDigits(parts[0]) && allDigits(parts[1]) && allDigits(parts[2]) && isOpenAIReasoningVariant(parts[3])
	}
	return false
}

func isOpenAIReasoningVariant(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func isEightDigitDate(value string) bool {
	return len(value) == 8 && allDigits(value)
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func appendUsageBillingModelCandidate(candidates []string, seen map[string]struct{}, model string) []string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return candidates
	}
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}

	add(trimmed)
	if canonical := canonicalizeOpenAIModelAliasSpelling(trimmed); canonical != "" {
		add(canonical)
	}
	if normalized := normalizeKnownOpenAICodexModel(trimmed); normalized != "" {
		add(normalized)
	}
	return candidates
}

func usageBillingModelCandidates(primary string, alternates ...string) []string {
	seen := make(map[string]struct{}, 1+len(alternates))
	candidates := appendUsageBillingModelCandidate(nil, seen, primary)
	for _, alternate := range alternates {
		candidates = appendUsageBillingModelCandidate(candidates, seen, alternate)
	}
	return candidates
}

func firstUsageBillingModel(candidates []string) string {
	for _, candidate := range candidates {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// [v0.2.5 新增] GPT-6 Astra 及带日期/提供商前缀变体识别；公开别名 gpt-6 路由到 Astra。
func isOpenAIGPT6AstraModel(model string) bool {
	normalized := canonicalizeOpenAIModelAliasSpelling(model)
	return normalized == "gpt-6" || normalized == "gpt-6-astra" || strings.HasPrefix(normalized, "gpt-6-astra-")
}
