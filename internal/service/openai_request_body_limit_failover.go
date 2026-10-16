package service

import (
	"net/http"
	"strings"
)

// OpenAIRequestBodyTooLargeClientMessage is intentionally fixed so an upstream
// proxy limit response cannot leak provider topology, account names or secrets.
const OpenAIRequestBodyTooLargeClientMessage = "Request payload is too large"

const openAIRequestBodyTooLargeReason = GatewayFailureReason("openai_request_body_too_large")

func isOpenAIRequestBodyTooLargeError(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if isOpenAIContextWindowError(upstreamMsg, upstreamBody) {
		return false
	}
	if statusCode == http.StatusRequestEntityTooLarge {
		return true
	}
	if statusCode != http.StatusBadRequest || len(upstreamBody) == 0 {
		return false
	}

	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(upstreamBody)))
	switch code {
	case "body_too_large", "content_too_large", "payload_too_large", "request_body_too_large", "request_entity_too_large", "request_too_large":
		return true
	}
	message := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(upstreamBody)))
	if message == "" {
		message = strings.ToLower(strings.TrimSpace(upstreamMsg))
	}
	hasSizeFailure := strings.Contains(message, "too large") || strings.Contains(message, "exceed") || strings.Contains(message, "larger than")
	hasBodySubject := strings.Contains(message, "request body") || strings.Contains(message, "request payload") || strings.Contains(message, "payload size")
	hasSerializedSizeSignal := strings.Contains(message, "body size") ||
		strings.Contains(message, "payload size") ||
		strings.Contains(message, "content-length") ||
		strings.Contains(message, "content length") ||
		strings.Contains(message, " byte") ||
		strings.Contains(message, "kb") ||
		strings.Contains(message, "mb") ||
		strings.Contains(message, "gb")
	return hasSizeFailure && hasBodySubject && hasSerializedSizeSignal
}

func newOpenAIRequestBodyTooLargeFailoverError(statusCode int, responseHeaders http.Header, responseBody []byte) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           responseBody,
		ResponseHeaders:        responseHeaders.Clone(),
		RetryableOnSameAccount: false,
		Scope:                  GatewayFailureScopeAccount,
		Reason:                 openAIRequestBodyTooLargeReason,
		NextAccountAction:      NextAccountRetry,
		ClientStatusCode:       http.StatusRequestEntityTooLarge,
		ClientMessage:          OpenAIRequestBodyTooLargeClientMessage,
	}
}

// IsOpenAIRequestBodyTooLarge reports whether a different account may accept
// the same replayable JSON request after the selected account rejected its size.
func (e *UpstreamFailoverError) IsOpenAIRequestBodyTooLarge() bool {
	return e != nil && e.Reason == openAIRequestBodyTooLargeReason
}
