package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_gateway_passthrough.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)


func hasOpenAIResponsesClientToolMapping(mapping apicompat.ResponsesClientToolMapping) bool {
	return len(mapping.CustomTools) > 0 || mapping.ToolSearch || len(mapping.NamespaceTools) > 0
}

func adaptOpenAIResponsesClientTools(body []byte) ([]byte, apicompat.ResponsesClientToolMapping, error) {
	if !needsOpenAIResponsesClientToolAdaptation(body) {
		return body, apicompat.ResponsesClientToolMapping{}, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var requestBody map[string]any
	if err := decoder.Decode(&requestBody); err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, fmt.Errorf("decode OpenAI Responses client tools: %w", err)
	}
	var trailingValue any
	if err := decoder.Decode(&trailingValue); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return body, apicompat.ResponsesClientToolMapping{}, fmt.Errorf("decode OpenAI Responses client tools trailing data: %w", err)
	}
	mapping, changed, err := apicompat.AdaptResponsesClientTools(requestBody)
	if err != nil || !changed {
		return body, mapping, err
	}
	rebuilt, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return body, apicompat.ResponsesClientToolMapping{}, fmt.Errorf("encode OpenAI Responses client tools: %w", err)
	}
	return rebuilt, mapping, nil
}

func needsOpenAIResponsesClientToolAdaptation(body []byte) bool {
	needsAdaptation := false
	var visit func(gjson.Result) bool
	visit = func(value gjson.Result) bool {
		if value.IsObject() {
			switch strings.TrimSpace(value.Get("type").String()) {
			case "custom", "custom_tool_call", "custom_tool_call_output",
				"tool_search", "tool_search_call", "tool_search_output":
				needsAdaptation = true
				return false
			}
		}
		if value.IsObject() || value.IsArray() {
			value.ForEach(func(_, child gjson.Result) bool {
				return visit(child)
			})
		}
		return !needsAdaptation
	}
	visit(gjson.ParseBytes(body))
	return needsAdaptation
}

func writeOpenAIPassthroughErrorHeaders(dst, src http.Header) {
	if dst == nil {
		return
	}
	dst.Set("Content-Type", "application/json; charset=utf-8")
	dst.Set("Cache-Control", "no-store")
	dst.Del("Retry-After")
	if src == nil {
		return
	}
	rawRetryAfter := strings.TrimSpace(src.Get("Retry-After"))
	if validOpenAIPassthroughRetryAfter(rawRetryAfter, time.Now()) {
		dst.Set("Retry-After", rawRetryAfter)
	}
}

func validOpenAIPassthroughRetryAfter(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	delaySeconds := true
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			delaySeconds = false
			break
		}
	}
	if delaySeconds {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		return err == nil && seconds > 0
	}
	parsed, err := http.ParseTime(raw)
	return err == nil && parsed.After(now)
}

func writeSanitizedOpenAIPassthroughError(c *gin.Context, upstreamStatus int, upstreamHeaders http.Header) {
	downstreamStatus := upstreamStatus
	message := "Upstream request failed"
	switch upstreamStatus {
	case http.StatusUnauthorized:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream authentication failed"
	case http.StatusForbidden:
		downstreamStatus = http.StatusBadGateway
		message = "Upstream access denied"
	default:
		if upstreamStatus >= http.StatusInternalServerError {
			message = "Upstream service temporarily unavailable"
		}
	}
	writeOpenAIPassthroughErrorEnvelope(c, downstreamStatus, upstreamHeaders, message)
}

// writeOpenAIPassthroughErrorEnvelope 以本地 JSON 信封 + 净化后的头策略写出
// 错误响应；message 由调用方决定（净化通用文案或脱敏后的上游消息）。
func writeOpenAIPassthroughErrorEnvelope(c *gin.Context, downstreamStatus int, upstreamHeaders http.Header, message string) {
	if c == nil {
		return
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	if writeOpenAICompactSSEBridge(c, downstreamStatus, body) {
		return
	}
	writeOpenAIPassthroughErrorHeaders(c.Writer.Header(), upstreamHeaders)
	c.Data(downstreamStatus, "application/json; charset=utf-8", body)
}

// openAIStreamDataStartsSemanticTTFT 保留 900194fab 之前的 first_token_ms
// 口径：跳过 Responses preamble 后，首个语义 SSE 事件即视为首 token。
func openAIStreamDataStartsSemanticTTFT(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || trimmed == "[DONE]" {
		return false
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" && gjson.Valid(trimmed) {
		eventType = strings.TrimSpace(gjson.Get(trimmed, "type").String())
	}
	switch eventType {
	case "response.failed":
		return false
	case "error":
		payload := []byte(trimmed)
		return !openAIStreamFailedEventShouldFailover(payload, extractOpenAISSEErrorMessage(payload))
	default:
		return !openAIStreamEventIsPreamble(eventType)
	}
}

func (s *OpenAIGatewayService) openAITTFTMode(ctx context.Context) string {
	mode := OpenAITTFTModeSemantic
	if s != nil && s.settingService != nil {
		mode = s.settingService.GetOpenAITTFTMode(ctx)
	} else if cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings); ok && cached != nil {
		if cached.expiresAt == 0 || time.Now().UnixNano() < cached.expiresAt {
			mode = normalizeOpenAITTFTMode(cached.openAITTFTMode)
		}
	}
	return normalizeOpenAITTFTMode(mode)
}

func openAIStreamDataStartsTTFT(data, eventType string, forceOutput bool, mode string) bool {
	if mode == OpenAITTFTModeVisible {
		return openAIStreamDataStartsVisibleOutput(data, eventType)
	}
	return forceOutput || openAIStreamDataStartsSemanticTTFT(data, eventType)
}

// openAIStreamCredentialAuthFailure distinguishes credential failures from
// request/content permission denials carried inside an HTTP 200 stream. Do not
// infer credential health from free-form 403 messages: providers also use
// permission_error/forbidden/access denied for request-scoped policy failures.
func openAIStreamCredentialAuthFailure(payload []byte) bool {
	if len(bytes.TrimSpace(payload)) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	for _, path := range []string{"response.error.status_code", "error.status_code", "status_code"} {
		if int(gjson.GetBytes(payload, path).Int()) == http.StatusUnauthorized {
			return true
		}
	}
	for _, path := range []string{"response.error.type", "error.type", "type"} {
		errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String()))
		if errType == "authentication_error" || errType == "authentication_failed" || errType == "unauthorized_error" {
			return true
		}
	}
	for _, path := range []string{"response.error.code", "error.code", "code"} {
		switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, path).String())) {
		case "invalid_api_key", "api_key_disabled", "unauthorized", "authentication_error",
			"invalid_token", "access_token_invalid", "token_revoked", "token_invalidated",
			"invalid_credentials", "credential_invalid":
			return true
		}
	}
	return false
}

func openAIStream403AccountFailure(payload []byte, message string) bool {
	return isOpenAIUpstreamAccessStateError(message, payload) || openAIStreamCredentialAuthFailure(payload)
}

func (s *OpenAIGatewayService) handleOpenAIStreamTerminalAccountSideEffects(
	c *gin.Context,
	account *Account,
	payload []byte,
	message string,
	headers http.Header,
	canonicalModel ...string,
) (int, bool) {
	statusCode := openAIStreamFailureStatus(payload, message)
	switch statusCode {
	case http.StatusForbidden:
		if !openAIStream403AccountFailure(payload, message) {
			return statusCode, false
		}
		fallthrough
	case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		model := firstNonEmpty(canonicalModel...)
		if model == "" {
			model = firstNonEmpty(gjson.GetBytes(payload, "model").String(), gjson.GetBytes(payload, "response.model").String())
		}
		accountHeaders := headers
		if statusCode == http.StatusTooManyRequests {
			// 普通模型的流式 429 不能继承外层 HTTP 200 的全局 quota 快照；
			// 只有 OAuth/SetupToken 的 Spark 配额 429 才需要保留 headers 读取明确的 5h/7d reset。
			accountHeaders = openAIWSSemantic429Headers(account, model, headers)
		}
		return statusCode, s.handleOpenAIAccountUpstreamError(ctx, account, statusCode, accountHeaders, payload, model)
	default:
		return statusCode, false
	}
}

func openAIStreamFailedEventRetryableOnSameAccount(account *Account, payload []byte, message string) bool {
	if account == nil {
		return false
	}
	// 容量降载是请求级信号，不是账号级故障：上游只是让本次请求稍后再试。
	// 换账号并不改变被降载的因素（客户端身份、模型容量都与账号无关），
	// 只会让单个请求把整池账号逐个消耗掉，最终仍以同一个错误告终。
	// 因此先在同一账号上做有界重试，用尽后才按常规流程切号。
	if isOpenAIUpstreamCapacityShedEvent(payload) {
		return true
	}
	if !account.IsPoolMode() {
		return false
	}
	semanticStatus := openAIStreamFailedEventSemanticStatus(payload, message)
	return account.IsPoolModeRetryableStatus(semanticStatus) ||
		isOpenAITransientProcessingError(http.StatusBadRequest, message, payload)
}

func (s *OpenAIGatewayService) newOpenAIStreamFailoverErrorWithModel(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	payload []byte,
	message string,
	canonicalModel string,
	responseHeaders ...http.Header,
) *UpstreamFailoverError {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI stream disconnected before completion"
	}
	var headers http.Header
	if len(responseHeaders) > 0 && responseHeaders[0] != nil {
		headers = responseHeaders[0].Clone()
	}
	statusCode, shouldDisable := s.handleOpenAIStreamTerminalAccountSideEffects(c, account, payload, message, headers, canonicalModel)
	// 流内 failed 事件承载于 HTTP 200；使用事件的语义状态更新账号健康，
	// 再由 failover 引擎按 StatusCode/RetryableOnSameAccount 决定恢复策略。
	message = s.recordOpenAIStreamUpstreamError(c, account, passthrough, upstreamRequestID, "failover", payload, message)
	errType := "upstream_error"
	if statusCode == http.StatusTooManyRequests {
		errType = "rate_limit_error"
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	retryableOnSameAccount := openAIStreamFailedEventRetryableOnSameAccount(account, payload, message)
	// 流终止事件承载在 HTTP 200 内，外层响应头描述的是成功流状态，而不是语义上的
	// 429 事件。仅在配额分类时忽略这些头；故障转移错误仍保留它们，使 Retry-After
	// 和请求 ID 能继续传递给后续处理。
	classificationHeaders := headers
	if statusCode == http.StatusTooManyRequests {
		classificationHeaders = nil
	}
	failoverErr := s.newOpenAIAccountFailoverErrorWithClassificationHeaders(account, statusCode, headers, classificationHeaders, payload, message, shouldDisable, retryableOnSameAccount)
	if failoverErr.IsCredentialFailure() || failoverErr.RequestScopedTransient {
		return failoverErr
	}
	// Preserve the existing generic envelope for unclassified stream failures;
	// only typed access/capacity failures need the original payload downstream.
	failoverErr.ResponseBody = body
	return failoverErr
}

// nonStreamingTerminalFailureFailover applies the streaming path's terminal-event
// verdict to a stream=false request whose upstream answered with SSE anyway
// (other sub2api instances and several OpenAI-compatible upstreams do this).
//
// Both handleSSEToJSON and handlePassthroughSSEToJSON collapsed every terminal
// `response.failed` / `error` frame into writeOpenAINonStreamingProtocolError, a
// fixed 502. The streaming readers sitting a few hundred lines away classify the
// very same frame with openAIStreamFailedEventShouldFailover /
// openAIStreamErrorEventShouldFailover and return an UpstreamFailoverError, so an
// upstream capacity error switched accounts when stream=true and was handed to the
// caller verbatim when stream=false — with other schedulable accounts still in the
// pool. Same event, same upstream, opposite outcome, decided only by a request flag
// the upstream never saw.
//
// Dispatching on terminalType keeps the two verdicts distinct exactly as the
// streaming readers do: a bare `error` frame goes through the conservative
// classifier that fails over only on positively transient markers, while
// `response.failed` uses the fuller one.
//
// Replay is safe here because the body was fully buffered by
// ReadUpstreamResponseBody and this runs before any semantic byte is written.
// Whether a failover actually happens stays the handler's call:
// openAIForwardMayFailover compares OpenAICompactKeepaliveAdjustedWrittenSize
// against its pre-Forward snapshot, so a request that already emitted output is
// refused there (#3887). This function only declines to propose failover once the
// service has explicitly committed a response, and never re-implements the
// keepalive accounting — a second copy of that rule would drift from the handler's.
//
// A nil account means there is nothing to fail over from: newOpenAIStreamFailoverError
// records ops attribution and account health against it, so those callers keep the
// protocol-error behaviour.
func (s *OpenAIGatewayService) nonStreamingTerminalFailureFailover(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	passthrough bool,
	terminalType string,
	payload []byte,
	message string,
	canonicalModel ...string,
) *UpstreamFailoverError {
	if account == nil || IsResponseCommitted(c) {
		return nil
	}
	shouldFailover := openAIStreamFailedEventShouldFailover(payload, message)
	if terminalType == "error" {
		shouldFailover = openAIStreamErrorEventShouldFailover(payload, message)
	}
	if !shouldFailover {
		return nil
	}
	var headers http.Header
	upstreamRequestID := ""
	if resp != nil {
		headers = resp.Header
		upstreamRequestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
	}
	return s.newOpenAIStreamFailoverErrorWithModel(c, account, passthrough, upstreamRequestID, payload, message, firstNonEmpty(canonicalModel...), headers)
}

