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
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// forwardResponsesViaRawChatCompletions serves /v1/responses clients through an
// upstream that only supports /v1/chat/completions.
func (s *OpenAIGatewayService) forwardResponsesViaRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "Failed to parse request body",
			},
		})
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "model is required",
			},
		})
		return nil, fmt.Errorf("missing model in request")
	}
	// 回退会重建 Responses 响应，必须先固定本次客户端可见的模型声明。
	beginClientResponseModelPolicy(c, ctx, account, originalModel)

	clientStream := responsesReq.Stream
	// Chat Completions 无法直接表达 Responses 的自由文本、工具搜索和
	// namespace 工具，先记录原始声明，回程时再恢复为 Codex 认识的类型。
	effectiveTools, err := apicompat.EffectiveResponsesTools(&responsesReq)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": err.Error(),
			},
		})
		return nil, fmt.Errorf("resolve responses tools: %w", err)
	}
	customTools := apicompat.CustomToolNames(effectiveTools)
	toolSearch := apicompat.HasToolSearchTool(effectiveTools)
	namespaceTools := apicompat.NamespaceToolNames(effectiveTools)
	// 明文 history 可刷新缓存；encrypted-only history 再按 item ID 回查。
	s.recacheReasoningItemsFromInput(responsesReq.Input)
	chatReq, err := apicompat.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, &apicompat.ResponsesToChatOptions{
		ReasoningContentByID: s.reasoningContentByID,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": err.Error(),
			},
		})
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := ResolveOpenAIForwardModelForRequest(ctx, nil, account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	requestedReasoningEffort := requestedReasoningEffortFromContextOrBody(ctx, body, "reasoning.effort", "reasoning_effort")
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, priorityDecision, err := s.applyOpenAIPriorityPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	finalReasoningEffort := extractReasoningEffortAuditFromBody(chatBody, "reasoning.effort", "reasoning_effort")
	// [v0.2.7] DeepSeek thinking mode：优先级策略之后、出站之前补占位（缓存回注
	// 已在上游 convert 阶段完成，这里只填仍为空的缺口，不覆盖已有内容）。
	chatBody = ensureDeepSeekChatReasoningPlaceholders(account, chatBody)
	logger.L().Debug("openai responses: forwarding via raw chat completions",
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
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if openaiCCRawAllowedHeaders[lowerKey] {
			for _, v := range values {
				upstreamReq.Header.Add(key, v)
			}
		}
	}
	if customUA := account.GetOpenAIUserAgent(); customUA != "" {
		upstreamReq.Header.Set("user-agent", customUA)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	SetOpsUpstreamModel(c, upstreamModel)
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		// Transport-level failure (proxy/DNS/TCP/TLS — no HTTP response). Convert to
		// a failover so the handler switches to a healthy account, and temporarily
		// unschedule the account on durable faults (e.g. rejected proxy credentials).
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		upstreamCode := extractUpstreamErrorCode(respBody)
		if openAIServiceTierUnsupportedResponse(resp.StatusCode, upstreamCode, upstreamMsg, respBody) && openAIPriorityCanRetryStandard(ctx, priorityDecision) {
			logOpenAIPriorityStandardRetry(ctx, account, upstreamModel, "service_tier_unsupported")
			return s.forwardResponsesViaRawChatCompletions(
				contextWithOpenAIPriorityStandardRetry(ctx, priorityDecision, "service_tier_unsupported"),
				c,
				account,
				body,
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
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	var result *OpenAIForwardResult
	var forwardErr error
	if clientStream {
		result, forwardErr = s.streamChatCompletionsAsResponses(c, resp, originalModel, customTools, toolSearch, namespaceTools, billingModel, upstreamModel, finalReasoningEffort, startTime)
	} else {
		result, forwardErr = s.bufferChatCompletionsAsResponses(c, resp, originalModel, customTools, toolSearch, namespaceTools, billingModel, upstreamModel, finalReasoningEffort, startTime)
	}
	if result != nil {
		setOpenAIResultReasoningAudit(result, requestedReasoningEffort, finalReasoningEffort)
		observedServiceTier := result.ServiceTier
		applyOpenAIPriorityDecisionToResult(result, priorityDecision, observedServiceTier)
	}
	return result, forwardErr
}

func (s *OpenAIGatewayService) bufferChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	customTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	beginUpstreamResponseModelObservation(c)
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": gin.H{
					"type":    "api_error",
					"message": "Failed to read upstream response",
				},
			})
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}

	var ccResp apicompat.ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &ccResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "api_error",
				"message": "Failed to parse upstream response",
			},
		})
		return nil, fmt.Errorf("parse chat completions response: %w", err)
	}
	// 先观察 Chat 上游原始声明，再构造 Responses 响应；客户可见模型改写
	// 不能污染管理员审计中的真实上游模型。
	observeUpstreamOpenAIResponseModel(c, respBody, "")
	responseModel := clientResponseModelForSyntheticOutput(c, nil, originalModel, upstreamModel)
	responsesResp := apicompat.ChatCompletionsResponseToResponses(&ccResp, responseModel, customTools, nil, toolSearch, namespaceTools)
	s.cacheReasoningItemsFromOutput(responsesResp.Output)

	usage := OpenAIUsage{}
	if parsed, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsed
	}
	observedServiceTier := extractOpenAIServiceTierFromResponseJSONBytes(respBody)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.JSON(http.StatusOK, responsesResp)

	observedModel := observedUpstreamResponseModel(c)
	return &OpenAIForwardResult{
		RequestID:                     requestID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedModel,
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamModelMismatch:         upstreamModelMismatch(upstreamModel, observedModel),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   observedServiceTier,
		Stream:                        false,
		Duration:                      time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamChatCompletionsAsResponses(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	customTools map[string]bool,
	toolSearch bool,
	namespaceTools map[string]apicompat.NamespacedToolName,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	beginUpstreamResponseModelObservation(c)
	// 伪造流式首字（chat→responses 转换路径）：随机延迟后先发 SSE 注释行，
	// 抢在上游 chat 流之前产出字节。clientWriteMu 串行化全部客户端写出，
	// 避免 goroutine 与主循环并发写 gin writer。
	var clientWriteMu sync.Mutex
	// handler 层可能已在上游请求之前抢先提交伪造首字，此时不再重复排程。
	fakeFirstTokenMs := CommittedFakeFirstTokenMs(c)
	// handler 层已武装过就接管：本函数开始写真实字节，定时器不再补发注释行。
	ClaimFakeFirstTokenOutput(c)
	var fakeOutputStarted atomic.Bool
	headersWritten := false
	writeStreamHeadersLocked := func() {
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
	state := apicompat.NewChatCompletionsToResponsesStreamState(responseModel)
	state.CustomTools = customTools
	state.ToolSearchDeclared = toolSearch
	state.NamespaceTools = namespaceTools
	var usage OpenAIUsage
	var firstTokenMs *int
	var observedServiceTier *string
	clientDisconnected := false
	sawDone := false

	writeEvents := func(events []apicompat.ResponsesStreamEvent) {
		if clientDisconnected || len(events) == 0 {
			return
		}
		// 上游已产出内容：伪造首字不再需要；整体持锁写出，保证事件帧不被插入。
		fakeOutputStarted.Store(true)
		clientWriteMu.Lock()
		defer clientWriteMu.Unlock()
		writeStreamHeadersLocked()
		for _, event := range events {
			sse, err := apicompat.ResponsesEventToSSE(event)
			if err != nil {
				logger.L().Warn("openai responses chat fallback: failed to marshal stream event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}
			if _, err := fmt.Fprint(c.Writer, sse); err != nil {
				clientDisconnected = true
				logger.L().Debug("openai responses chat fallback: client disconnected, continuing to drain upstream for billing",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				return
			}
		}
		c.Writer.Flush()
	}

	// 伪造首字排程：随机延迟后若上游仍未产出，先写 SSE 注释行并 Flush。
	fakeFTDone := make(chan struct{})
	defer close(fakeFTDone)
	if fakeFirstTokenMs == nil && !FakeFirstTokenArmedByHandler(c) {
		fakeFTCfg := fakeFirstTokenSettingsFor(s.settingService)
		if fakeFTCh, stopFakeFT := ScheduleFakeFirstToken(fakeFTCfg); fakeFTCh != nil {
			defer stopFakeFT()
			go func() {
				select {
				case <-fakeFTCh:
				case <-fakeFTDone:
					return
				}
				if fakeOutputStarted.Load() {
					return
				}
				clientWriteMu.Lock()
				defer clientWriteMu.Unlock()
				if fakeOutputStarted.Load() {
					return
				}
				writeStreamHeadersLocked()
				if _, err := c.Writer.WriteString(fakeFirstTokenComment); err == nil {
					c.Writer.Flush()
					ms := int(time.Since(startTime).Milliseconds())
					fakeFirstTokenMs = &ms
				}
			}()
		}
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	for scanner.Scan() {
		line := scanner.Text()
		noteFakeFirstTokenFromUpstreamLine(line, &fakeFirstTokenMs, startTime)
		payload, ok := extractOpenAISSEDataLine(line)
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
		observeUpstreamOpenAIResponseModel(c, []byte(payload), "")
		if tier := extractOpenAIServiceTierFromResponseJSONBytes([]byte(payload)); tier != nil {
			observedServiceTier = tier
		}

		if u := extractCCStreamUsage(payload); u != nil {
			usage = *u
		}

		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			logger.L().Warn("openai responses chat fallback: failed to parse chat stream chunk",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			continue
		}
		if firstTokenMs == nil && !isOpenAIChatUsageOnlyStreamChunk(payload) && chatChunkStartsResponsesOutput(&chunk) {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		events := apicompat.ChatCompletionsChunkToResponsesEvents(&chunk, state)
		s.cacheReasoningItemsFromEvents(events)
		writeEvents(events)
	}

	if err := scanner.Err(); err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai responses chat fallback: stream read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
		observedModel := observedUpstreamResponseModel(c)
		return &OpenAIForwardResult{
			RequestID:                     requestID,
			Usage:                         usage,
			Model:                         originalModel,
			BillingModel:                  billingModel,
			UpstreamModel:                 upstreamModel,
			UpstreamResponseModel:         observedModel,
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			UpstreamModelMismatch:         upstreamModelMismatch(upstreamModel, observedModel),
			ReasoningEffort:               reasoningEffort,
			ServiceTier:                   observedServiceTier,
			Stream:                        true,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  firstTokenMs,
			FakeFirstTokenMs:              fakeFirstTokenMs,
		}, fmt.Errorf("stream usage incomplete: %w", err)
	}

	finalEvents := apicompat.FinalizeChatCompletionsResponsesStream(state)
	s.cacheReasoningItemsFromEvents(finalEvents)
	writeEvents(finalEvents)
	if !clientDisconnected {
		clientWriteMu.Lock()
		writeStreamHeadersLocked()
		if _, err := fmt.Fprint(c.Writer, "data: [DONE]\n\n"); err != nil {
			clientDisconnected = true
		}
		if !clientDisconnected {
			c.Writer.Flush()
		}
		clientWriteMu.Unlock()
	}
	if !sawDone {
		logger.L().Debug("openai responses chat fallback: upstream stream ended without done sentinel",
			zap.String("request_id", requestID),
		)
	}

	observedModel := observedUpstreamResponseModel(c)
	return &OpenAIForwardResult{
		RequestID:                     requestID,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedModel,
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamModelMismatch:         upstreamModelMismatch(upstreamModel, observedModel),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   observedServiceTier,
		Stream:                        true,
		Duration:                      time.Since(startTime),
		FirstTokenMs:                  firstTokenMs,
		FakeFirstTokenMs:              fakeFirstTokenMs,
	}, nil
}

func chatChunkStartsResponsesOutput(chunk *apicompat.ChatCompletionsChunk) bool {
	if chunk == nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != nil || choice.Delta.ReasoningContent != nil || len(choice.Delta.ToolCalls) > 0 {
			return true
		}
	}
	return false
}


// deepSeekChatReasoningPlaceholderText 是 Chat Completions 侧 thinking-mode
// 占位明文。必须是单个空格：DeepSeek 拒绝空串，非空即可通过；LiteLLM 同样注入
// 单个空格。
const deepSeekChatReasoningPlaceholderText = " "

// targetsDeepSeekAPIHost 报告该账号实际上游是否是 DeepSeek 官方 API。
// platform=deepseek 直接命中；platform=openai 但 base_url 指向
// api.deepseek.com 的映射账号同样命中（Codex 把 GPT 模型名映射到 DeepSeek
// 时的典型接入方式）。
func targetsDeepSeekAPIHost(account *Account) bool {
	if account == nil {
		return false
	}
	if account.Platform == PlatformDeepseek {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(account.GetOpenAIBaseURL()))
	if err != nil {
		return false
	}
	ds, err := url.Parse(DefaultDeepseekBaseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), ds.Hostname())
}

// ensureDeepSeekChatReasoningPlaceholders 给缺 reasoning_content 的 assistant
// 消息补单个空格占位。DeepSeek thinking mode 要求历史里每条产生过思维的
// assistant 消息都回传该字段，否则 400。
//
// 桥接会从 summary / 缓存回注真实明文；这里只填仍为空的缺口，不覆盖已有内容。
// 非 DeepSeek 上游原样返回（字节不变）。
func ensureDeepSeekChatReasoningPlaceholders(account *Account, body []byte) []byte {
	if !targetsDeepSeekAPIHost(account) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	updated := body
	changed := false
	for i, msg := range messages.Array() {
		if strings.TrimSpace(msg.Get("role").String()) != "assistant" {
			continue
		}
		if msg.Get("reasoning_content").String() != "" {
			continue
		}
		next, err := sjson.SetBytes(updated, "messages."+strconv.Itoa(i)+".reasoning_content", deepSeekChatReasoningPlaceholderText)
		if err != nil {
			return body
		}
		updated = next
		changed = true
	}
	if !changed {
		return body
	}
	return updated
}
