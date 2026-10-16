package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_gateway_upstream_errors.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"github.com/tidwall/gjson"
	"net/http"
	"strings"
)


func isOpenAICompatibleModelNotFound400(respBody []byte) bool {
	code := strings.TrimSpace(extractUpstreamErrorCode(respBody))
	if code != "" {
		return strings.EqualFold(code, "model_not_found")
	}

	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if msg == "" && !gjson.ValidBytes(respBody) {
		msg = strings.ToLower(strings.TrimSpace(string(respBody)))
	}
	return strings.Contains(msg, "unknown provider for model") ||
		strings.Contains(msg, "model not found") ||
		strings.Contains(msg, "model is not supported")
}

// IsOpenAICompatibleModelNotFound400 reports whether an OpenAI-compatible 400
// is an account-specific missing-model response eligible for failover.
func IsOpenAICompatibleModelNotFound400(respBody []byte) bool {
	return isOpenAICompatibleModelNotFound400(respBody)
}

func newOpenAIUpstreamFailoverError(
	statusCode int,
	responseHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	retryableOnSameAccount bool,
) *UpstreamFailoverError {
	requestScopedCapacity := isOpenAIRequestScopedCapacityShed(upstreamMsg, responseBody)
	failoverErr := &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           responseBody,
		ResponseHeaders:        responseHeaders.Clone(),
		RetryableOnSameAccount: retryableOnSameAccount || requestScopedCapacity,
		RequestScopedTransient: requestScopedCapacity,
	}
	if isOpenAIRequestBodyTooLargeError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameAccount = false
		failoverErr.RequestScopedTransient = false
		failoverErr.Scope = GatewayFailureScopeAccount
		failoverErr.Reason = openAIRequestBodyTooLargeReason
		failoverErr.NextAccountAction = NextAccountRetry
		failoverErr.ClientStatusCode = http.StatusRequestEntityTooLarge
		failoverErr.ClientMessage = OpenAIRequestBodyTooLargeClientMessage
	}
	if isOpenAIHTTPUpstreamAccessStateError(statusCode, upstreamMsg, responseBody) {
		failoverErr.RetryableOnSameAccount = false
		failoverErr.RequestScopedTransient = false
		failoverErr.Stage = GatewayFailureStageAccountAuth
		failoverErr.Scope = GatewayFailureScopeAccount
		failoverErr.Reason = OpenAIUpstreamAccessStateReason
		failoverErr.NextAccountAction = NextAccountRetry
		failoverErr.ClientStatusCode = http.StatusBadGateway
		failoverErr.ClientMessage = openAIUpstreamAccessUnavailableClientMessage
	} else if requestScopedCapacity {
		// Preserve the provider's actionable overload message after gateway
		// retries are exhausted, but expose it as a retryable server_error.
		failoverErr.ClientStatusCode = http.StatusServiceUnavailable
		failoverErr.ClientMessage = openAICapacityShedClientMessage(upstreamMsg, responseBody)
	}
	return failoverErr
}

func (s *OpenAIGatewayService) newOpenAIAccountFailoverError(
	account *Account,
	statusCode int,
	responseHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	shouldDisable bool,
	retryableOnSameAccount bool,
) *UpstreamFailoverError {
	return s.newOpenAIAccountFailoverErrorWithClassificationHeaders(account, statusCode, responseHeaders, responseHeaders, responseBody, upstreamMsg, shouldDisable, retryableOnSameAccount)
}

func (s *OpenAIGatewayService) newOpenAIAccountFailoverErrorWithClassificationHeaders(
	account *Account,
	statusCode int,
	responseHeaders http.Header,
	classificationHeaders http.Header,
	responseBody []byte,
	upstreamMsg string,
	shouldDisable bool,
	retryableOnSameAccount bool,
) *UpstreamFailoverError {
	oauth429Retry := s.shouldRetryOpenAIOAuth429OnSameAccountWithResponse(account, statusCode, shouldDisable, classificationHeaders, responseBody)
	failoverErr := newOpenAIUpstreamFailoverError(
		statusCode,
		responseHeaders,
		responseBody,
		upstreamMsg,
		retryableOnSameAccount || oauth429Retry,
	)
	if oauth429Retry {
		failoverErr.SameAccountRetryDeadline = s.openAIOAuth429RetryDeadline(account)
		failoverErr.SameAccountRetryDelay = openAIOAuth429SameAccountRetryDelay(responseHeaders, failoverErr.SameAccountRetryDeadline)
	}
	return failoverErr
}

const (
	openAIUpstreamAccessUnavailableClientMessage = "Upstream access is temporarily unavailable, please retry later"
	// OpenAIUpstreamAccessStateReason marks a provider credential whose
	// account, workspace, or organization is unavailable.
	OpenAIUpstreamAccessStateReason = GatewayFailureReason("openai_upstream_access_state")
	// OpenAIHTTPContinuationUnsupportedReason identifies accounts that cannot
	// preserve an official Responses HTTP continuation without dropping state.
	OpenAIHTTPContinuationUnsupportedReason = GatewayFailureReason("openai_http_continuation_unsupported")
)

// isOpenAIUpstreamAccessStateError recognizes provider-side credential state
// failures only from explicit structured codes. Free-form messages may contain
// echoed user input, including inside stream terminal error.message fields.
func isOpenAIUpstreamAccessStateError(_ string, body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	for _, path := range []string{"error.code", "response.error.code", "detail.code", "code"} {
		if isOpenAIUpstreamAccessStateCode(gjson.GetBytes(body, path).String()) {
			return true
		}
	}
	return false
}

func isOpenAIUpstreamAccessStateCode(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "deactivated_workspace" {
		return true
	}
	for _, subject := range []string{"workspace", "account", "organization", "org"} {
		for _, state := range []string{"deactivated", "disabled", "suspended"} {
			if value == subject+"_"+state || value == state+"_"+subject {
				return true
			}
		}
	}
	return false
}

// isOpenAIHTTPUpstreamAccessStateError is deliberately status-independent:
// known provider codes are durable evidence, while 401/403 messages without
// such a code must flow through the existing authentication/403 policies.
func isOpenAIHTTPUpstreamAccessStateError(_ int, _ string, body []byte) bool {
	return isOpenAIUpstreamAccessStateError("", body)
}

func openAICapacityShedClientMessage(upstreamMsg string, body []byte) string {
	for _, candidate := range []string{
		upstreamMsg,
		gjson.GetBytes(body, "error.message").String(),
		gjson.GetBytes(body, "response.error.message").String(),
		gjson.GetBytes(body, "message").String(),
	} {
		candidate = sanitizeUpstreamErrorMessage(strings.TrimSpace(candidate))
		if candidate != "" && isOpenAICapacityShedMessage(candidate) {
			return candidate
		}
	}
	return "Upstream service is temporarily overloaded, please retry later"
}

// IsOpenAICapacityShed reports whether typed client fields were derived from a
// recognized provider overload rather than supplied by an unrelated failure.
func (e *UpstreamFailoverError) IsOpenAICapacityShed() bool {
	return e != nil && e.RequestScopedTransient && isOpenAIRequestScopedCapacityShed("", e.ResponseBody)
}

