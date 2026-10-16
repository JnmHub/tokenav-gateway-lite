package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// isOpenAIRequestScopedContentPolicyError 只识别由本次输入触发的内容安全拒绝。
// 账号停用、权限不足等 403 仍需换号；内容安全拒绝换号没有意义，还会污染账号健康度。
func isOpenAIRequestScopedContentPolicyError(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusForbidden {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(string(responseBody)))
	if lower == "" {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "response.error.code").String()))
	}
	switch code {
	case "content_policy_violation", "content_filter", "moderation_blocked", "safety_violation":
		return true
	case "account_disabled", "organization_disabled", "permission_denied", "access_denied", "insufficient_permissions", "subscription_required", "entitlement_required":
		return false
	}
	for _, marker := range []string{
		"account_disabled", "account suspended", "account has been disabled",
		"organization_disabled", "organization has been disabled", "permission_denied",
		"access_denied", "insufficient_permissions", "subscription_required", "entitlement_required",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"rejected by the safety system", "blocked by the safety system",
		"rejected by content moderation", "blocked by content moderation",
		"violates content policy", "content policy violation",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// buildOpenAIHTTPFailoverError 统一普通 OpenAI HTTP 错误的换号语义。
// 管理员显式配置的临时不可调度规则只负责排除当前账号；它已经在
// CheckErrorPolicy 内写入模型级临时状态，因此这里不能再重复执行普通账号处置。
func (s *OpenAIGatewayService) buildOpenAIHTTPFailoverError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	responseBody []byte,
	upstreamMsg string,
	upstreamModel string,
	ordinaryFailover bool,
) *UpstreamFailoverError {
	if account == nil {
		return nil
	}
	if account.Platform == PlatformOpenAI && isOpenAIRequestScopedContentPolicyError(statusCode, responseBody) {
		return nil
	}
	if account.Platform == PlatformGrok {
		if !ordinaryFailover {
			return nil
		}
		s.handleGrokAccountUpstreamError(ctx, account, statusCode, headers, responseBody, upstreamModel)
		return &UpstreamFailoverError{StatusCode: statusCode, ResponseBody: responseBody, ResponseHeaders: headers}
	}

	tempUnscheduled := false
	if !ordinaryFailover && c != nil && !IsResponseCommitted(c) && s.rateLimitService != nil {
		tempUnscheduled = s.rateLimitService.CheckErrorPolicy(ctx, account, statusCode, responseBody, upstreamModel) == ErrorPolicyTempUnscheduled
	}
	if !ordinaryFailover && !tempUnscheduled {
		return nil
	}

	capacityShed := isOpenAIRequestScopedCapacityShed(upstreamMsg, responseBody)
	shouldDisable := tempUnscheduled
	if ordinaryFailover && !capacityShed {
		shouldDisable = s.handleOpenAIAccountUpstreamError(ctx, account, statusCode, headers, responseBody, upstreamModel)
	}
	failoverErr := &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           responseBody,
		ResponseHeaders:        headers,
		RetryableOnSameAccount: capacityShed || (!shouldDisable && account.IsPoolMode() && (account.IsPoolModeRetryableStatus(statusCode) || isOpenAITransientProcessingError(statusCode, strings.TrimSpace(upstreamMsg), responseBody))),
		RequestScopedTransient: capacityShed,
	}
	return s.decorateOpenAIOAuth429FailoverError(failoverErr, account, statusCode, shouldDisable, headers, headers, responseBody)
}
