package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIWSHTTPBridgeToolStateContextKey = "openai_ws_http_bridge_tool_state"

type openAIWSHTTPBridgeToolState struct {
	clientMapping apicompat.ResponsesClientToolMapping
	loweredTools  json.RawMessage
}

func openAIWSHTTPBridgeToolStateFromContext(c *gin.Context) (openAIWSHTTPBridgeToolState, bool) {
	if c == nil {
		return openAIWSHTTPBridgeToolState{}, false
	}
	value, ok := c.Get(openAIWSHTTPBridgeToolStateContextKey)
	state, typed := value.(openAIWSHTTPBridgeToolState)
	return state, ok && typed
}

func setOpenAIWSHTTPBridgeToolState(c *gin.Context, state openAIWSHTTPBridgeToolState) {
	if c == nil {
		return
	}
	state.loweredTools = append(json.RawMessage(nil), state.loweredTools...)
	c.Set(openAIWSHTTPBridgeToolStateContextKey, state)
}

func decodeOpenAIWSHTTPBridgeLoweredTools(raw json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	var tools []any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	return tools
}

func openAIWSHTTPBridgeRawField(body []byte, name string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, false
	}
	raw, present := fields[name]
	return append(json.RawMessage(nil), raw...), present
}

func openAIWSHTTPBridgeToolUpstreamName(account *Account) string {
	if account != nil && account.Platform == PlatformGrok {
		return "Grok WS HTTP bridge"
	}
	return "OpenAI WS HTTP bridge"
}

const (
	openAIWSClientReadLimitBytesDefault     int64 = 64 * 1024 * 1024
	openAIWSHTTPBridgeThresholdBytesDefault int64 = 15 * 1024 * 1024
	openAIWSHTTPBridgeErrorBodyLimitBytes         = 64 * 1024
)

func ResolveOpenAIWSClientReadLimitBytes(cfg *config.Config) int64 {
	if cfg == nil || cfg.Gateway.OpenAIWS.ClientReadLimitBytes <= 0 {
		return openAIWSClientReadLimitBytesDefault
	}
	return cfg.Gateway.OpenAIWS.ClientReadLimitBytes
}

func (s *OpenAIGatewayService) openAIWSHTTPBridgeEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.HTTPBridgeEnabled
}

func (s *OpenAIGatewayService) openAIWSHTTPBridgeThresholdBytes() int64 {
	if s == nil || s.cfg == nil || s.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes <= 0 {
		return openAIWSHTTPBridgeThresholdBytesDefault
	}
	return s.cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes
}

func forceGrokOpenAIWSHTTPBridge(account *Account) bool {
	return account != nil && account.Platform == PlatformGrok
}

func (s *OpenAIGatewayService) shouldBridgeOpenAIWSHTTP(account *Account, payloadBytes int, previousResponseID string) bool {
	if account != nil && account.Platform == PlatformGrok {
		return true
	}
	if !s.openAIWSHTTPBridgeEnabled() {
		return false
	}
	if strings.TrimSpace(previousResponseID) != "" {
		return false
	}
	threshold := s.openAIWSHTTPBridgeThresholdBytes()
	return threshold > 0 && int64(payloadBytes) >= threshold
}

func prepareOpenAIWSHTTPBridgeBody(payload []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if body == nil {
		return nil, errors.New("response.create payload must be a JSON object")
	}
	delete(body, "type")
	delete(body, "generate")
	delete(body, "previous_response_id")
	body["stream"] = true
	return json.Marshal(body)
}

type openAIWSToolCallReplayCollector struct {
	items    []json.RawMessage
	seen     map[string]struct{}
	allItems []json.RawMessage
	allSeen  map[string]struct{}
}

func (c *openAIWSToolCallReplayCollector) AddEvent(eventType string, message []byte) {
	switch strings.TrimSpace(eventType) {
	case "response.output_item.done":
		item := gjson.GetBytes(message, "item")
		c.addAllItem(item)
		c.addItem(item)
	case "response.completed", "response.done":
		output := gjson.GetBytes(message, "response.output")
		if !output.IsArray() {
			return
		}
		for _, item := range output.Array() {
			c.addAllItem(item)
			c.addItem(item)
		}
	}
}

func (c *openAIWSToolCallReplayCollector) AllItems() []json.RawMessage {
	return cloneOpenAIWSRawMessages(c.allItems)
}

func (c *openAIWSToolCallReplayCollector) addAllItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") || strings.TrimSpace(item.Get("type").String()) == "" {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = raw
	}
	if c.allSeen == nil {
		c.allSeen = make(map[string]struct{})
	}
	if _, ok := c.allSeen[key]; ok {
		return
	}
	c.allSeen[key] = struct{}{}
	c.allItems = append(c.allItems, json.RawMessage(raw))
}

func (c *openAIWSToolCallReplayCollector) Items() []json.RawMessage {
	return cloneOpenAIWSRawMessages(c.items)
}

func (c *openAIWSToolCallReplayCollector) addItem(item gjson.Result) {
	if !item.Exists() || item.Type != gjson.JSON {
		return
	}
	raw := strings.TrimSpace(item.Raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return
	}
	if !isCodexToolCallContextItemType(item.Get("type").String()) {
		return
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("call_id").String())
	}
	if key == "" {
		key = raw
	}
	if c.seen == nil {
		c.seen = make(map[string]struct{})
	}
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	c.items = append(c.items, json.RawMessage(raw))
}

func buildOpenAIWSHTTPBridgeErrorEvent(statusCode int, message string) []byte {
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(statusCode)
	}
	if message == "" {
		message = "upstream request failed"
	}
	event := map[string]any{
		"type":   "error",
		"status": statusCode,
		"error": map[string]any{
			"type":    "upstream_error",
			"message": message,
		},
	}
	body, err := json.Marshal(event)
	if err != nil {
		return []byte(`{"type":"error","error":{"type":"upstream_error","message":"upstream request failed"}}`)
	}
	return body
}

func (s *OpenAIGatewayService) proxyOpenAIWSHTTPBridgeTurn(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	token string,
	payload []byte,
	payloadBytes int,
	originalModel string,
	imageBillingModel string,
	imageSizeTier string,
	imageInputSize string,
	grokCacheIdentity string,
	turn int,
	writeClientMessage func([]byte) error,
) (*OpenAIForwardResult, error) {
	if s == nil {
		return nil, errors.New("service is nil")
	}
	if s.httpUpstream == nil {
		return nil, errors.New("openai http upstream is nil")
	}
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if writeClientMessage == nil {
		return nil, errors.New("client websocket writer is nil")
	}
	// HTTP bridge 直接由 WebSocket 入站调用，和普通 HTTP 转发不同，调用方
	// 可能尚未初始化上游模型观察器。先建立观察器，后续在改写客户响应前
	// 记录原始上游事件，避免把 request（跟随请求）策略改写后的模型误当成
	// 上游声明模型写入审计。
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}

	body, err := prepareOpenAIWSHTTPBridgeBody(payload)
	if err != nil {
		return nil, fmt.Errorf("prepare http bridge body: %w", err)
	}
	ctx, rejectedFieldRetryState := openAIResponsesRejectedFieldRetryStateForContext(ctx, body)
	grokIntentSourceBody := append([]byte(nil), body...)
	_, grokExplicitToolsField := openAIWSHTTPBridgeRawField(grokIntentSourceBody, "tools")
	grokExplicitToolIntent := account.Platform == PlatformGrok && hasGrokResponsesToolIntent(grokIntentSourceBody)
	var clientToolMapping apicompat.ResponsesClientToolMapping
	functionToolUpstream := (account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey) || account.Platform == PlatformGrok
	if functionToolUpstream {
		if account.Platform == PlatformGrok {
			body, err = sanitizeGrokResponsesInput(body)
			if err != nil {
				return nil, fmt.Errorf("sanitize Grok WS HTTP bridge input: %w", err)
			}
		}
		inheritedState, _ := openAIWSHTTPBridgeToolStateFromContext(c)
		inheritedLoweredTools := decodeOpenAIWSHTTPBridgeLoweredTools(inheritedState.loweredTools)
		body, clientToolMapping, err = adaptResponsesClientToolsForFunctionUpstreamWithMapping(
			body,
			openAIWSHTTPBridgeToolUpstreamName(account),
			inheritedState.clientMapping,
			inheritedLoweredTools,
		)
		if err != nil {
			return nil, fmt.Errorf("adapt %s client tools: %w", openAIWSHTTPBridgeToolUpstreamName(account), err)
		}
		// Internal rejected-field retries re-enter with an already lowered tools
		// array. They must retain the first attempt's response mapping, while a
		// real client turn with tools: [] intentionally clears session tools.
		if !hasResponsesClientToolMapping(clientToolMapping) && rejectedFieldRetryState.attempted() {
			clientToolMapping = inheritedState.clientMapping
		}
		if account.Platform == PlatformGrok && !grokExplicitToolsField && !grokExplicitToolIntent && len(inheritedLoweredTools) > 0 && hasGrokResponsesToolIntent(body) {
			grokIntentSourceBody = append(grokIntentSourceBody[:0], body...)
		}
		loweredTools := inheritedState.loweredTools
		if currentTools, present := openAIWSHTTPBridgeRawField(body, "tools"); present {
			loweredTools = currentTools
		}
		setOpenAIWSHTTPBridgeToolState(c, openAIWSHTTPBridgeToolState{
			clientMapping: clientToolMapping,
			loweredTools:  loweredTools,
		})
		if account.Platform == PlatformGrok {
			setGrokResponsesClientToolMapping(c, clientToolMapping)
		} else {
			setOpenAIResponsesClientToolMapping(c, clientToolMapping)
		}
	}
	requestedReasoningEffort := extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort")
	sentPriority := normalizeBillingServiceTier(gjson.GetBytes(body, "service_tier").String()) == OpenAIFastTierPriority
	upstreamModel := ""

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	var upstreamReq *http.Request
	if account.Platform == PlatformGrok {
		upstreamModel = strings.TrimSpace(gjson.GetBytes(body, "model").String())
		if originalModel != "" {
			if mappedModel := normalizeOpenAIModelForUpstream(account, MappedAccountModelForRequest(ctx, nil, account, originalModel)); mappedModel != "" {
				upstreamModel = mappedModel
			}
		}
		if upstreamModel == "" {
			upstreamModel = "grok-4.5"
		}
		body, err = patchGrokResponsesBody(body, upstreamModel)
		if err == nil {
			grokMixedCacheIntentBody := append([]byte(nil), body...)
			body, err = applyGrokResponsesCacheIdentity(body, grokIntentSourceBody, grokCacheIdentity, account.IsGrokOAuth())
			if err == nil {
				body, err = applyGrokFreeRequestToolCacheRoute(c, body, grokMixedCacheIntentBody, account, grokCacheIdentity)
			}
		}
		if err == nil {
			upstreamReq, err = buildGrokResponsesRequest(upstreamCtx, c, account, body, token, grokCacheIdentity, s.cfg)
		}
	} else {
		upstreamReq, err = s.buildUpstreamRequestOpenAIPassthrough(upstreamCtx, c, account, body, token)
	}
	releaseUpstreamCtx()
	if err != nil {
		appendOpenAIResponsesRejectedFieldRetryEvent(c, account, rejectedFieldRetryState, "field_retry_failed", 0, "", "request_build_failed")
		return nil, err
	}
	if account.Platform != PlatformGrok && isOpenAIResponsesLiteWebSocketPayload(payload) {
		upstreamReq.Header.Set(responsesLiteHeader, "true")
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	if c != nil {
		c.Set("openai_passthrough", true)
		c.Set("openai_ws_http_bridge", true)
	}

	turnStart := time.Now()
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		appendOpenAIResponsesRejectedFieldRetryEvent(c, account, rejectedFieldRetryState, "field_retry_failed", 0, "", "transport_failed")
		// 只有第一轮且尚未向客户端输出业务事件时可以透明换号；后续轮次
		// 已依赖前轮状态，重放会重复工具调用或产生双份计费。
		if turn == 1 {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
		}
		// 仍更新账号健康状态，但把返回值降为普通错误，阻止外层把后续轮次
		// 当作一个可以在其他账号重放的新请求。
		_ = s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(http.StatusBadGateway, "Upstream request failed"))
		return nil, fmt.Errorf("upstream http bridge request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, openAIWSHTTPBridgeErrorBodyLimitBytes))
		rawUpstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg := sanitizeUpstreamErrorMessage(rawUpstreamMsg)
		if upstreamMsg == "" {
			upstreamMsg = http.StatusText(resp.StatusCode)
		}
		shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
		if account.Platform == PlatformGrok {
			shouldFailover = s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody)
		}
		if rejectedFieldRetryState.attempted() {
			appendOpenAIResponsesRejectedFieldRetryEvent(
				c, account, rejectedFieldRetryState, "field_retry_failed", resp.StatusCode,
				resp.Header.Get("x-request-id"), "followup_rejected",
			)
		}
		if shouldRetryGrokUnauthorized(ctx, account, resp.StatusCode) {
			if refreshedToken, refreshErr := s.refreshGrokAfterUnauthorizedForRequest(ctx, c, account); refreshErr == nil {
				_ = resp.Body.Close()
				return s.proxyOpenAIWSHTTPBridgeTurn(
					contextWithGrokUnauthorizedRetry(ctx, refreshedToken),
					c,
					account,
					refreshedToken,
					payload,
					payloadBytes,
					originalModel,
					imageBillingModel,
					imageSizeTier,
					imageInputSize,
					grokCacheIdentity,
					turn,
					writeClientMessage,
				)
			} else {
				return nil, refreshErr
			}
		}
		if account.Platform == PlatformGrok {
			s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
			if shouldFailover && (turn == 1 || resp.StatusCode == http.StatusTooManyRequests) {
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					ProxyHost:          safeProxyHost(proxyURL),
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
					Kind:               "failover",
					Message:            upstreamMsg,
				})
				return nil, &UpstreamFailoverError{
					StatusCode:             resp.StatusCode,
					ResponseBody:           respBody,
					ResponseHeaders:        resp.Header.Clone(),
					RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
				}
			}
		} else if decision, changed, retryErr := normalizeOpenAIResponsesRejectedFieldRetryBody(resp.StatusCode, body, respBody); retryErr != nil {
			return nil, fmt.Errorf("normalize WS HTTP bridge rejected field retry body: %w", retryErr)
		} else if changed && rejectedFieldRetryState.Allow(decision) {
			appendOpenAIResponsesRejectedFieldRetryEvent(
				c, account, rejectedFieldRetryState, "field_retry_started", resp.StatusCode,
				resp.Header.Get("x-request-id"), "retrying",
			)
			result, retryErr := s.proxyOpenAIWSHTTPBridgeTurn(
				ctx, c, account, token, decision.Body, len(decision.Body), originalModel,
				imageBillingModel, imageSizeTier, imageInputSize, grokCacheIdentity, turn, writeClientMessage,
			)
			if retryErr == nil {
				appendOpenAIResponsesRejectedFieldRetryEvent(
					c, account, rejectedFieldRetryState, "field_retry_succeeded", http.StatusOK,
					"", "accepted",
				)
			}
			return result, retryErr
		} else if shouldFailover {
			capacityShed := isOpenAIUpstreamCapacityShedResponse(resp.StatusCode, respBody)
			if !capacityShed {
				canonicalModel := canonicalOpenAIAccountSchedulingModel(account, originalModel)
				s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, canonicalModel)
			}
			if turn == 1 || resp.StatusCode == http.StatusTooManyRequests {
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					ProxyHost:          safeProxyHost(proxyURL),
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
					Kind:               "failover",
					Message:            upstreamMsg,
				})
				return nil, &UpstreamFailoverError{
					StatusCode:             resp.StatusCode,
					ResponseBody:           respBody,
					ResponseHeaders:        resp.Header.Clone(),
					RetryableOnSameAccount: capacityShed,
					RequestScopedTransient: capacityShed,
				}
			}
		} else if shouldCooldownOpenAITransientUpstreamError(resp.StatusCode, respBody) {
			// WS HTTP bridge must feed the same account+model transient state as
			// normal HTTP/SSE paths; otherwise a bridge-only overload immediately
			// selects the same account again on the next turn.
			canonicalModel := canonicalOpenAIAccountSchedulingModel(account, originalModel)
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, canonicalModel)
		}
		if sentPriority && openAIServiceTierUnsupportedResponse(resp.StatusCode, extractUpstreamErrorCode(respBody), rawUpstreamMsg, respBody) {
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				ProxyHost:          safeProxyHost(proxyURL),
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
				Kind:               "http_error",
				Message:            upstreamMsg,
			})
			return nil, wrapOpenAIWSIngressTurnError(
				openAIWSIngressStageServiceTierUnsupported,
				fmt.Errorf("upstream http bridge error: status=%d message=%s", resp.StatusCode, upstreamMsg),
				false,
			)
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			ProxyHost:          safeProxyHost(proxyURL),
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
			Kind:               "http_error",
			Message:            upstreamMsg,
		})
		_ = writeClientMessage(buildOpenAIWSHTTPBridgeErrorEvent(resp.StatusCode, upstreamMsg))
		return nil, fmt.Errorf("upstream http bridge error: status=%d message=%s", resp.StatusCode, upstreamMsg)
	}
	if account.Platform == PlatformGrok {
		s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode), upstreamModel)
	}

	responseID := ""
	usage := OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	var firstTokenMs *int
	var observedServiceTier *string
	reqStream := openAIWSPayloadBoolFromRaw(body, "stream", true)
	eventCount := 0
	tokenEventCount := 0
	terminalEventCount := 0
	replayCollector := &openAIWSToolCallReplayCollector{}
	firstEventType := ""
	lastEventType := ""
	sawDone := false
	wroteDownstream := false
	pendingClientMessages := make([][]byte, 0, 4)
	pendingClientMessageBytes := int64(0)
	capacityFailoverSuppressedLogged := false
	clientDisconnected := false
	mappedModel := ""
	if originalModel != "" {
		mappedModel = strings.TrimSpace(gjson.GetBytes(body, "model").String())
		if mappedModel == "" {
			mappedModel = normalizeOpenAIModelForUpstream(account, MappedAccountModelForRequest(ctx, nil, account, originalModel))
		}
	}

	resultWithUsage := func() *OpenAIForwardResult {
		imageCount := imageCounter.Count()
		observedModel := observedUpstreamResponseModel(c)
		result := &OpenAIForwardResult{
			RequestID:                     responseID,
			Usage:                         usage,
			Model:                         originalModel,
			UpstreamModel:                 mappedModel,
			UpstreamResponseModel:         observedModel,
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			UpstreamModelMismatch:         upstreamModelMismatch(firstNonEmpty(upstreamModel, mappedModel, originalModel), observedModel),
			ServiceTier:                   observedServiceTier,
			// body 已是本轮最终通过 HTTP bridge 发给上游的 payload；日志只记录
			// 其中明确存在的档次，不按模型名称或 thinking 开关猜测默认值。
			RequestedReasoningEffort: cloneOptionalString(requestedReasoningEffort),
			ReasoningEffort:          extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort"),
			Stream:                   reqStream,
			OpenAIWSMode:             true,
			ResponseHeaders:          cloneHeader(resp.Header),
			Duration:                 time.Since(turnStart),
			FirstTokenMs:             firstTokenMs,
		}
		if replayInput := replayCollector.Items(); len(replayInput) > 0 {
			result.wsReplayInput = replayInput
			result.wsReplayInputExists = true
		}
		result.wsAccountFailoverReplayInput = replayCollector.AllItems()
		if imageCount > 0 {
			result.ImageCount = imageCount
			result.ImageSize = imageSizeTier
			result.ImageInputSize = imageInputSize
			result.ImageOutputSizes = imageCounter.Sizes()
			result.BillingModel = imageBillingModel
		}
		return result
	}

	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	if hasResponsesClientToolMapping(clientToolMapping) {
		resp.Body = newResponsesClientToolStreamBody(resp.Body, clientToolMapping, maxLineSize)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	defer putSSEScannerBuf64K(scanBuf)

	for scanner.Scan() {
		line := scanner.Text()
		data, ok := extractOpenAISSEDataLine(line)
		if !ok {
			continue
		}
		trimmedData := strings.TrimSpace(data)
		if trimmedData == "" {
			continue
		}
		if trimmedData == "[DONE]" {
			sawDone = true
			continue
		}

		upstreamMessage := []byte(trimmedData)
		if normalized, changed := normalizeCompletedImageGenerationStatus(upstreamMessage); changed {
			upstreamMessage = normalized
		}
		if tier := extractOpenAIServiceTierFromResponseJSONBytes(upstreamMessage); tier != nil {
			observedServiceTier = tier
		}
		eventType, eventResponseID, _ := parseOpenAIWSEventEnvelope(upstreamMessage)
		if responseID == "" && eventResponseID != "" {
			responseID = eventResponseID
		}
		if eventType != "" {
			eventCount++
			if firstEventType == "" {
				firstEventType = eventType
			}
			lastEventType = eventType
		}
		if isOpenAIWSTokenEvent(eventType) {
			tokenEventCount++
			if firstTokenMs == nil {
				ms := int(time.Since(turnStart).Milliseconds())
				firstTokenMs = &ms
			}
		}
		if openAIWSEventShouldParseUsage(eventType) {
			parseOpenAIWSResponseUsageFromCompletedEvent(upstreamMessage, &usage)
		}
		imageCounter.AddSSEData(upstreamMessage)

		// 必须在任何客户端响应模型改写之前观察原始事件；否则 request/custom
		// 策略会覆盖 response.model，导致后台无法判断上游实际声明。
		observeUpstreamOpenAIResponseModel(c, upstreamMessage, eventType)

		// 改写所有合法 JSON 事件中已经存在的模型字段。不能依赖
		// response.* 白名单，否则 session.* 或上游新增事件会泄露真实模型。
		upstreamMessage = rewriteResponseModelForClient(c, account, originalModel, upstreamMessage)
		if s.toolCorrector != nil && openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(upstreamMessage) {
			if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(upstreamMessage); changed {
				upstreamMessage = corrected
			}
		}
		replayCollector.AddEvent(eventType, upstreamMessage)

		var upstreamEventErr error
		if eventType == "error" || eventType == "response.failed" {
			errMessage := extractOpenAISSEErrorMessage(upstreamMessage)
			if errMessage == "" {
				errMessage = "upstream error event"
			}
			statusCode := openAIStreamFailureStatus(upstreamMessage, errMessage)
			shouldFailover := openAIStreamFailedEventShouldFailover(upstreamMessage, errMessage)
			errCodeRaw := ""
			errTypeRaw := ""
			errMsgRaw := ""
			if eventType == "error" {
				errCodeRaw, errTypeRaw, errMsgRaw = parseOpenAIWSErrorEventFields(upstreamMessage)
				statusCode = openAIWSErrorHTTPStatusFromRaw(errCodeRaw, errTypeRaw)
				shouldFailover = s.shouldFailoverOpenAIUpstreamResponse(statusCode, errMessage, upstreamMessage)
			}
			capacityShed := isOpenAIUpstreamCapacityShedEvent(upstreamMessage)
			if account.Platform == PlatformGrok && eventType == "error" {
				// SSE 错误没有 HTTP 状态，new_sensitive 等内容策略码可能被默认
				// 映射成 502；先按 403 内容拒绝识别，避免错误切换健康账号。
				if isGrokContentPolicyRejection(http.StatusForbidden, upstreamMessage) {
					shouldFailover = false
				} else {
					shouldFailover = s.shouldFailoverGrokUpstreamError(statusCode, upstreamMessage)
					s.handleGrokAccountUpstreamError(ctx, account, statusCode, resp.Header, upstreamMessage, upstreamModel)
				}
			} else if eventType == "error" && shouldFailover {
				accountStatus := statusCode
				if transientStatus := openAIWSPayloadTransientStatus(upstreamMessage); transientStatus != 0 {
					accountStatus = transientStatus
				}
				if !capacityShed {
					canonicalModel := canonicalOpenAIAccountSchedulingModel(account, originalModel)
					s.handleOpenAIAccountUpstreamError(ctx, account, accountStatus, resp.Header, upstreamMessage, canonicalModel)
				}
			}
			if eventType == "error" {
				if reason, _ := classifyOpenAIWSErrorEventFromRaw(errCodeRaw, errTypeRaw, errMsgRaw); sentPriority && reason == openAIWSIngressStageServiceTierUnsupported {
					return resultWithUsage(), wrapOpenAIWSIngressTurnError(
						openAIWSIngressStageServiceTierUnsupported,
						errors.New(errMessage),
						wroteDownstream,
					)
				}
			}
			if !wroteDownstream && shouldFailover && (turn == 1 || statusCode == http.StatusTooManyRequests) {
				return nil, s.newOpenAIStreamFailoverError(
					c,
					account,
					true,
					resp.Header.Get("x-request-id"),
					upstreamMessage,
					errMessage,
					resp.Header,
				)
			}
			if wroteDownstream && capacityShed && !capacityFailoverSuppressedLogged {
				logOpenAICapacityFailoverSuppressed(ctx, account, "ws_http_bridge", resp.Header.Get("x-request-id"), eventType)
				capacityFailoverSuppressedLogged = true
			}
			if eventType == "error" {
				upstreamEventErr = errors.New(errMessage)
			}
		}

		// Codex 会把 server_is_overloaded / slow_down 当作致命错误。调度与
		// 账号健康仍使用上游原文，只改写发给客户端的副本，让客户端按
		// server_error 执行内置重试。
		clientMessage := upstreamMessage
		if eventType == "error" || eventType == "response.failed" {
			if rewritten, changed := sanitizeOpenAICapacityShedErrorCodeForClient(clientMessage); changed {
				clientMessage = rewritten
			}
		}

		if !clientDisconnected {
			// 第一轮在真正的文本、工具参数或媒体输出出现前暂存协议前导帧。
			// 这样 response.created 后若立即过载，仍能完整换号，不会把两次
			// 上游响应拼接给客户端；8 MiB 上限避免异常上游无限占用内存。
			stageBeforeSemanticOutput := turn == 1 && account.Platform == PlatformOpenAI && !wroteDownstream
			commitStagedMessages := !stageBeforeSemanticOutput ||
				openAIStreamDataStartsClientOutput(string(clientMessage), eventType) ||
				isOpenAIWSTerminalEvent(eventType)
			if stageBeforeSemanticOutput && !commitStagedMessages {
				if pendingClientMessageBytes+int64(len(clientMessage)) > openAIFirstOutputStageMaxBytes {
					return nil, s.newOpenAIStreamFailoverError(
						c,
						account,
						true,
						resp.Header.Get("x-request-id"),
						nil,
						"OpenAI WS HTTP bridge first-output staging limit exceeded",
						resp.Header,
					)
				}
				pendingClientMessages = append(pendingClientMessages, append([]byte(nil), clientMessage...))
				pendingClientMessageBytes += int64(len(clientMessage))
			} else {
				messages := append(pendingClientMessages, clientMessage)
				pendingClientMessages = nil
				pendingClientMessageBytes = 0
				for _, message := range messages {
					if err := writeClientMessage(message); err != nil {
						if isOpenAIWSClientDisconnectError(err) {
							clientDisconnected = true
							closeStatus, closeReason := summarizeOpenAIWSReadCloseError(err)
							logOpenAIWSModeInfo(
								"ingress_ws_http_bridge_client_disconnected_drain account_id=%d turn=%d close_status=%s close_reason=%s",
								account.ID,
								turn,
								closeStatus,
								truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
							)
							break
						}
						return nil, wrapOpenAIWSIngressTurnError(
							"write_client",
							fmt.Errorf("write client websocket event: %w", err),
							wroteDownstream,
						)
					}
					wroteDownstream = true
				}
			}
		}

		if upstreamEventErr != nil {
			return resultWithUsage(), upstreamEventErr
		}
		if isOpenAIWSTerminalEvent(eventType) {
			terminalEventCount++
			firstTokenMsValue := -1
			if firstTokenMs != nil {
				firstTokenMsValue = *firstTokenMs
			}
			logOpenAIWSModeInfo(
				"ingress_ws_http_bridge_turn_completed account_id=%d turn=%d response_id=%s payload_bytes=%d duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
				payloadBytes,
				time.Since(turnStart).Milliseconds(),
				eventCount,
				tokenEventCount,
				terminalEventCount,
				truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
				firstTokenMsValue,
				clientDisconnected,
			)
			return resultWithUsage(), nil
		}
	}
	if err := scanner.Err(); err != nil {
		streamErr := fmt.Errorf("read upstream http bridge stream: %w", err)
		if turn == 1 && !wroteDownstream {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, streamErr, true)
		}
		return resultWithUsage(), streamErr
	}
	terminalErr := errors.New("upstream http bridge stream ended before terminal event")
	if sawDone {
		terminalErr = errors.New("upstream http bridge stream sent [DONE] before terminal event")
	}
	if turn == 1 && !wroteDownstream {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, terminalErr, true)
	}
	return resultWithUsage(), terminalErr
}

func resolveGrokWSCacheIdentity(c *gin.Context, account *Account, seedPayload, currentPayload []byte, originalModel string) (string, error) {
	return resolveGrokWSCacheIdentityForRequest(nil, c, account, seedPayload, currentPayload, originalModel)
}

func resolveGrokWSCacheIdentityForRequest(ctx context.Context, c *gin.Context, account *Account, seedPayload, currentPayload []byte, originalModel string) (string, error) {
	body, err := prepareOpenAIWSHTTPBridgeBody(seedPayload)
	if err != nil {
		return "", err
	}
	upstreamModel := resolveGrokWSUpstreamModelForRequest(ctx, account, currentPayload, originalModel)
	body, err = patchGrokResponsesBody(body, upstreamModel)
	if err != nil {
		return "", err
	}
	return resolveGrokCacheIdentity(c, body, "", upstreamModel), nil
}

// openAIWSPayloadTransientStatus extracts a durable HTTP-equivalent status
// from an SSE/WebSocket error payload so the shared account cooldown state is
// updated with the same semantics as ordinary HTTP responses.
func openAIWSPayloadTransientStatus(payload []byte) int {
	if len(payload) == 0 {
		return 0
	}
	status := int(gjson.GetBytes(payload, "response.error.status_code").Int())
	if status == 0 {
		status = int(gjson.GetBytes(payload, "response.error.status").Int())
	}
	if status == 0 {
		status = int(gjson.GetBytes(payload, "error.status_code").Int())
	}
	if status == 0 {
		status = int(gjson.GetBytes(payload, "error.status").Int())
	}
	if shouldCooldownOpenAITransientUpstreamError(status, payload) {
		return status
	}
	if status != 0 {
		return 0
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	switch {
	case code == "server_is_overloaded", code == "slow_down":
		return http.StatusServiceUnavailable
	case strings.Contains(code, "server_error"),
		strings.Contains(code, "internal_error"),
		strings.Contains(code, "upstream_error"),
		strings.Contains(errType, "server_error"),
		strings.Contains(errType, "internal_error"),
		strings.Contains(errType, "upstream_error"):
		return http.StatusInternalServerError
	default:
		return 0
	}
}

func resolveGrokWSUpstreamModel(account *Account, body []byte, originalModel string) string {
	return resolveGrokWSUpstreamModelForRequest(nil, account, body, originalModel)
}

func resolveGrokWSUpstreamModelForRequest(ctx context.Context, account *Account, body []byte, originalModel string) string {
	upstreamModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	originalModel = strings.TrimSpace(originalModel)
	if account != nil && originalModel != "" && (upstreamModel == "" || upstreamModel == originalModel) {
		if mappedModel := normalizeOpenAIModelForUpstream(account, MappedAccountModelForRequest(ctx, nil, account, originalModel)); mappedModel != "" {
			upstreamModel = mappedModel
		}
	}
	if upstreamModel == "" {
		upstreamModel = "grok-4.5"
	}
	return upstreamModel
}
