package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardAnthropicViaRawChatCompletions 为只支持 /v1/chat/completions 的
// OpenAI 兼容上游提供 /v1/messages。这里使用 Anthropic Messages ↔ Chat
// Completions 直接桥，避免每个流式事件重复经过 Responses 两套状态机。
func (s *OpenAIGatewayService) forwardAnthropicViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	originalModel := strings.TrimSpace(anthropicReq.Model)
	if originalModel == "" {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	// Messages → Chat 的回退会生成新的 Anthropic 响应，不能沿用已经映射的 body 模型。
	beginClientResponseModelPolicy(c, ctx, account, originalModel)
	// Keep the raw Chat Completions declaration separate from the synthetic
	// Anthropic response model.  The former is needed for administrator audit;
	// the latter must follow response_model_mode and may intentionally expose the
	// caller's alias instead of the mapped upstream model.
	beginUpstreamResponseModelObservation(c)
	applyOpenAICompatModelNormalization(&anthropicReq)
	clientStream := anthropicReq.Stream
	requestedReasoningEffort := requestedReasoningEffortFromContextOrBody(ctx, body, "output_config.effort")

	chatReq, err := apicompat.AnthropicToChatCompletionsRequest(&anthropicReq)
	if err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert anthropic to chat completions: %w", err)
	}
	billingModel := ResolveOpenAIForwardModelForRequest(ctx, nil, account, anthropicReq.Model, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	SetOpsUpstreamModel(c, upstreamModel)
	chatReq.Model = upstreamModel
	chatReq.ReasoningEffort = openAICompatAnthropicReasoningEffort(&anthropicReq, upstreamModel, chatReq.ReasoningEffort)
	if containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaFastMode) {
		chatReq.ServiceTier = "priority"
	}
	chatReq.Stream = clientStream
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions request: %w", err)
	}
	if normalizedBody, normalized := NormalizeGLMOpenAIReasoningEffort(chatBody, upstreamModel); normalized {
		chatBody = normalizedBody
	}
	chatBody, priorityDecision, err := s.applyOpenAIPriorityPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			errorType := "forbidden_error"
			if blocked.HTTPStatusCode() == http.StatusBadRequest {
				errorType = "invalid_request_error"
			}
			writeAnthropicError(c, blocked.HTTPStatusCode(), errorType, blocked.Message)
		}
		return nil, err
	}
	finalReasoningEffort := extractReasoningEffortAuditFromBody(chatBody, "reasoning.effort", "reasoning_effort")

	logger.L().Debug("openai messages: forwarding via raw chat completions",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	apiKey := account.GetOpenAIProtocolAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("account %d missing api_key", account.ID)
	}
	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base_url: %w", err)
	}
	targetURL := buildOpenAIChatCompletionsURL(validatedURL)

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(chatBody))
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	upstreamReq = upstreamReq.WithContext(WithHTTPUpstreamProfile(upstreamReq.Context(), HTTPUpstreamProfileOpenAI))
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+apiKey)
	if clientStream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}
	// 只透传明确允许的功能头，不能把客户的 Authorization 或 X-Api-Key
	// 带给上游，否则会覆盖轻量网关账号凭据。
	for key, values := range c.Request.Header {
		if !openaiCCRawAllowedHeaders[strings.ToLower(key)] {
			continue
		}
		for _, value := range values {
			upstreamReq.Header.Add(key, value)
		}
	}
	if customUA := account.GetOpenAIUserAgent(); customUA != "" {
		upstreamReq.Header.Set("user-agent", customUA)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		upstreamCode := extractUpstreamErrorCode(respBody)
		if openAIServiceTierUnsupportedResponse(resp.StatusCode, upstreamCode, upstreamMsg, respBody) && openAIPriorityCanRetryStandard(ctx, priorityDecision) {
			logOpenAIPriorityStandardRetry(ctx, account, upstreamModel, "service_tier_unsupported")
			return s.forwardAnthropicViaRawChatCompletions(
				contextWithOpenAIPriorityStandardRetry(ctx, priorityDecision, "service_tier_unsupported"),
				c,
				account,
				body,
				defaultMappedModel,
			)
		}
		ordinaryFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
		if failoverErr := s.buildOpenAIHTTPFailoverError(ctx, c, account, resp.StatusCode, resp.Header, respBody, upstreamMsg, upstreamModel, ordinaryFailover); failoverErr != nil {
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			return nil, failoverErr
		}
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	var result *OpenAIForwardResult
	var forwardErr error
	if clientStream {
		result, forwardErr = s.streamChatCompletionsAsAnthropic(c, resp, originalModel, billingModel, upstreamModel, finalReasoningEffort, startTime)
	} else {
		result, forwardErr = s.bufferChatCompletionsAsAnthropic(c, resp, originalModel, billingModel, upstreamModel, finalReasoningEffort, startTime)
	}
	if result != nil {
		setOpenAIResultReasoningAudit(result, requestedReasoningEffort, finalReasoningEffort)
		observedServiceTier := result.ServiceTier
		applyOpenAIPriorityDecisionToResult(result, priorityDecision, observedServiceTier)
	}
	return result, forwardErr
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsAnthropic(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}

	var chatResp apicompat.ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Failed to parse upstream response")
		return nil, fmt.Errorf("parse chat completions response: %w", err)
	}
	// Observe before converting the envelope so model audit remains tied to the
	// actual upstream JSON, not the client-facing model synthesized below.
	observeUpstreamOpenAIResponseModel(c, respBody, "")
	responseModel := clientResponseModelForSyntheticOutput(c, nil, originalModel, upstreamModel)
	anthropicResp := apicompat.ChatCompletionsResponseToAnthropic(&chatResp, responseModel)

	usage := OpenAIUsage{}
	if parsed, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsed
	}
	observedServiceTier := extractOpenAIServiceTierFromResponseJSONBytes(respBody)
	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.JSON(http.StatusOK, anthropicResp)

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamModelMismatch:         upstreamModelMismatch(upstreamModel, observedUpstreamResponseModel(c)),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   observedServiceTier,
		Stream:                        false,
		Duration:                      time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsAnthropic(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	// The caller initializes this before sending the request; keep this helper
	// nil-safe for direct tests and legacy callers.
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}
	headersWritten := false
	writeStreamHeaders := func() {
		if headersWritten {
			return
		}
		headersWritten = true
		if s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
	}

	responseModel := clientResponseModelForSyntheticOutput(c, nil, originalModel, upstreamModel)
	anthropicState := apicompat.NewChatCompletionsToAnthropicStreamState(responseModel)
	usage := OpenAIUsage{}
	var firstTokenMs *int
	var observedServiceTier *string
	clientDisconnected := false
	sawDone := false

	emitChunk := func(chunk *apicompat.ChatCompletionsChunk) {
		anthropicEvents := apicompat.ChatCompletionsChunkToAnthropicEvents(chunk, anthropicState)
		if firstTokenMs == nil && chatChunkStartsResponsesOutput(chunk) {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if clientDisconnected {
			return
		}
		for _, anthropicEvent := range anthropicEvents {
			sse, err := apicompat.ResponsesAnthropicEventToSSE(anthropicEvent)
			if err != nil {
				logger.L().Warn("openai messages chat fallback: failed to marshal stream event", zap.Error(err), zap.String("request_id", requestID))
				continue
			}
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				logger.L().Debug("openai messages chat fallback: client disconnected, continuing to drain upstream for billing", zap.Error(err), zap.String("request_id", requestID))
				break
			}
		}
		if !clientDisconnected && len(anthropicEvents) > 0 {
			c.Writer.Flush()
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	for scanner.Scan() {
		payload, ok := extractOpenAISSEDataLine(scanner.Text())
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		// Audit the raw Chat Completions chunk before it is transformed into
		// Anthropic SSE, whose model field is synthesized by response policy.
		observeUpstreamOpenAIResponseModel(c, []byte(payload), "")
		if tier := extractOpenAIServiceTierFromResponseJSONBytes([]byte(payload)); tier != nil {
			observedServiceTier = tier
		}
		if parsedUsage := extractCCStreamUsage(payload); parsedUsage != nil {
			usage = *parsedUsage
		}
		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			logger.L().Warn("openai messages chat fallback: failed to parse chat stream chunk", zap.Error(err), zap.String("request_id", requestID))
			continue
		}
		emitChunk(&chunk)
	}

	if scanErr := scanner.Err(); scanErr != nil {
		if !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
			logger.L().Warn("openai messages chat fallback: stream read error", zap.Error(scanErr), zap.String("request_id", requestID))
		}
		return &OpenAIForwardResult{
			RequestID:                     requestID,
			Usage:                         usage,
			Model:                         originalModel,
			BillingModel:                  billingModel,
			UpstreamModel:                 upstreamModel,
			UpstreamResponseModel:         observedUpstreamResponseModel(c),
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			UpstreamModelMismatch:         upstreamModelMismatch(upstreamModel, observedUpstreamResponseModel(c)),
			ReasoningEffort:               reasoningEffort,
			ServiceTier:                   observedServiceTier,
			Stream:                        true,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  firstTokenMs,
			ClientDisconnect:              clientDisconnected,
		}, fmt.Errorf("stream usage incomplete: %w", scanErr)
	}

	finalEvents := apicompat.FinalizeChatCompletionsAnthropicStream(anthropicState)
	if !clientDisconnected {
		for _, anthropicEvent := range finalEvents {
			sse, err := apicompat.ResponsesAnthropicEventToSSE(anthropicEvent)
			if err != nil {
				continue
			}
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				break
			}
		}
		c.Writer.Flush()
	}
	if !sawDone {
		logger.L().Debug("openai messages chat fallback: upstream stream ended without done sentinel", zap.String("request_id", requestID))
	}

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamModelMismatch:         upstreamModelMismatch(upstreamModel, observedUpstreamResponseModel(c)),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   observedServiceTier,
		Stream:                        true,
		Duration:                      time.Since(startTime),
		FirstTokenMs:                  firstTokenMs,
		ClientDisconnect:              clientDisconnected,
	}, nil
}
