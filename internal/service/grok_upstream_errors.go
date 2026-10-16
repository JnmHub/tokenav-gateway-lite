package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// isGrokContentPolicyRejection 只识别“本次输入内容”导致的 403。
// 账号封禁、订阅不足等错误即使也包含 policy 字样，仍必须走账号故障转移，
// 否则坏账号会继续接收请求。
func isGrokContentPolicyRejection(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusForbidden || len(responseBody) == 0 {
		return false
	}
	if grokAccountAccessMessage(string(responseBody)) {
		return false
	}

	var payload any
	if json.Unmarshal(responseBody, &payload) == nil {
		if grokStructuredAccountAccessMarker(payload) {
			return false
		}
		if grokStructuredContentPolicyMarker(payload) {
			return true
		}
	}
	return grokContentPolicyMessage(string(responseBody))
}

func grokStructuredAccountAccessMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			switch normalizeGrokErrorMarker(key) {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && isGrokAccountAccessCode(marker) {
					return true
				}
			}
			if grokStructuredAccountAccessMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredAccountAccessMarker(child) {
				return true
			}
		}
	}
	return false
}

func grokStructuredContentPolicyMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			switch normalizeGrokErrorMarker(key) {
			case "code", "error_code", "type", "category", "reason":
				if marker, ok := child.(string); ok && isGrokContentPolicyCode(marker) {
					return true
				}
			}
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	}
	return false
}

func normalizeGrokErrorMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	return strings.ReplaceAll(value, " ", "_")
}

func isGrokContentPolicyCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "content_filter", "content_policy", "content_policy_violation", "content_moderation", "cyber_policy", "new_sensitive":
		return true
	default:
		return false
	}
}

func isGrokAccountAccessCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "account_suspended", "account_disabled", "user_suspended", "user_disabled",
		"subscription_required", "entitlement_required", "not_entitled", "plan_required", "permission_denied":
		return true
	default:
		return false
	}
}

func grokAccountAccessMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, phrase := range []string{
		"account suspended", "account has been suspended", "account disabled", "account has been disabled",
		"user suspended", "user has been suspended", "subscription required", "entitlement required", "not entitled",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func grokContentPolicyMessage(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, phrase := range []string{
		"the moderation feature is not available", "image is sensitive", "text is sensitive",
		"prohibited content", "forbidden content", "content policy violation", "content policy rejection",
		"content policy rejected", "content moderation rejection", "content moderation rejected",
		"content moderation blocked", "request blocked by content moderation", "request rejected by content moderation",
		"request blocked by policy", "request rejected by policy", "request violates policy",
		"prompt violates content policy", "prompt violates policy", "input violates content policy", "input violates policy",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func grokContentPolicyClientMessage(responseBody []byte) string {
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
	if message == "" {
		return "Request blocked by upstream content policy"
	}
	return message
}

// applyGrokForbiddenPolicy 让管理员配置的临时停用规则优先于默认 30 分钟冷却。
func (s *OpenAIGatewayService) applyGrokForbiddenPolicy(ctx context.Context, account *Account, responseBody []byte) bool {
	if account == nil || !account.IsTempUnschedulableEnabled() {
		return false
	}
	matches := matchTempUnschedulableRules(account, http.StatusForbidden, responseBody)
	if len(matches) == 0 {
		return false
	}
	match := matches[0]
	if s != nil && s.rateLimitService != nil && s.rateLimitService.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		handled := s.rateLimitService.tryTempUnschedulable(stateCtx, account, http.StatusForbidden, responseBody)
		cancel()
		if handled {
			return true
		}
	}
	cooldown := time.Duration(match.rule.DurationMinutes) * time.Minute
	if cooldown > 0 {
		s.tempUnscheduleGrok(ctx, account, cooldown, "grok configured forbidden rule")
	}
	return true
}
