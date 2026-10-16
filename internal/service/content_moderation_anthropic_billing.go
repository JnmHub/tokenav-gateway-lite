package service

import "strings"

const anthropicBillingAttributionPrefix = "x-anthropic-billing-header: "

// isStrictAnthropicBillingAttributionText 只豁免整块、纯机器字段的 Claude
// Code 计费归因文本。不能只看前缀：API Key 直通 Anthropic 时该 system
// block 会原样发给上游，攻击者可在合法头后追加自然语言来绕过系统指令审查。
func isStrictAnthropicBillingAttributionText(text string) bool {
	if !strings.HasPrefix(text, anthropicBillingAttributionPrefix) || strings.ContainsAny(text, "\r\n") {
		return false
	}
	payload := strings.TrimPrefix(text, anthropicBillingAttributionPrefix)
	fields := strings.Split(payload, ";")
	// 真实归因字段均以分号结束；尾部还有任何非空内容都必须作为普通
	// 系统指令进入语种和关键词检查。
	if len(fields) < 2 || strings.TrimSpace(fields[len(fields)-1]) != "" {
		return false
	}

	seenVersion, seenEntrypoint, seenCCH := false, false, false
	fieldCount := 0
	for _, raw := range fields[:len(fields)-1] {
		field := strings.TrimSpace(raw)
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" || value == "" || strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
			return false
		}
		switch key {
		case "cc_version":
			if seenVersion || !isStrictClaudeCodeBillingVersion(value) {
				return false
			}
			seenVersion = true
		case "cc_entrypoint":
			if seenEntrypoint || (value != "cli" && value != "sdk") {
				return false
			}
			seenEntrypoint = true
		case "cch":
			if seenCCH || !isFiveHexDigits(value) {
				return false
			}
			seenCCH = true
		default:
			return false
		}
		fieldCount++
	}
	return fieldCount > 0
}

func isStrictClaudeCodeBillingVersion(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	components := strings.Split(value, ".")
	// 兼容旧客户端只上报主版本号（例如 1），以及真实 Claude Code 的
	// major.minor.patch[.三位十六进制指纹]。不能允许任意字母数字分量，
	// 否则攻击者可把 "ignore.all.rules" 伪装成版本号而整块免审。
	if len(components) == 1 {
		return isASCIIDigits(components[0])
	}
	if len(components) != 3 && len(components) != 4 {
		return false
	}
	for _, component := range components[:3] {
		if !isASCIIDigits(component) {
			return false
		}
	}
	if len(components) == 4 {
		return len(components[3]) == 3 && isASCIIHex(components[3])
	}
	return true
}

func isFiveHexDigits(value string) bool {
	return len(value) == 5 && isASCIIHex(value)
}

func isASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isASCIIHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
