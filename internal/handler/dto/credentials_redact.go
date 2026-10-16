// Package dto provides data transfer objects for HTTP handlers.
package dto

import "github.com/Wei-Shaw/sub2api/internal/service"

// RedactCredentials 复制一份 in，剥离 service.SensitiveCredentialKeys 列出的所有敏感子键，
// 并产出一个 has_<key> 状态 map 表示哪些敏感键存在且非零值。
//
// 输入 nil 时返回 nil, nil（避免响应里出现空对象）。
// 不修改入参；调用方拿到的 out 可安全序列化进 JSON 返回前端。
func RedactCredentials(in map[string]any) (out map[string]any, status map[string]bool) {
	if in == nil {
		return nil, nil
	}
	out = make(map[string]any, len(in))
	for k, v := range in {
		if k == service.HeaderOverridesCredentialKey {
			redactedOverrides, present := redactHeaderOverrideValues(v)
			if redactedOverrides != nil {
				out[k] = redactedOverrides
			}
			if present {
				if status == nil {
					status = make(map[string]bool, 4)
				}
				status["has_"+k] = true
			}
			continue
		}
		if service.IsSensitiveCredentialKey(k) {
			if isCredentialValuePresent(v) {
				if status == nil {
					status = make(map[string]bool, 4)
				}
				status["has_"+k] = true
			}
			continue
		}
		out[k] = v
	}
	return out, status
}

// redactHeaderOverrideValues 保留可编辑的 header 名称，但绝不把配置值发送到浏览器。
// 空字符串同时是编辑协议中的占位符：原名称未删除时，后端 merge 会保留旧值。
func redactHeaderOverrideValues(value any) (map[string]any, bool) {
	var names []string
	switch typed := value.(type) {
	case map[string]any:
		names = make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
	case map[string]string:
		names = make([]string, 0, len(typed))
		for name := range typed {
			names = append(names, name)
		}
	default:
		// 非法历史结构也不能原样回传；存在性状态足够提示管理员重新配置。
		return nil, isCredentialValuePresent(value)
	}

	redacted := make(map[string]any, len(names))
	for _, name := range names {
		redacted[name] = ""
	}
	return redacted, len(redacted) > 0
}

// isCredentialValuePresent 判断值是否"存在且非零"。空字符串、nil、false 均视为未配置；
// 其余非零类型（数字、对象、字符串等）视为已配置。
func isCredentialValuePresent(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	default:
		return true
	}
}
