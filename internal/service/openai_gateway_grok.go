package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	grokComposerImageBridgeVisionModel     = "grok-build-0.1"
	grokComposerImageBridgeMaxOutputTokens = 512
	grokRateLimitFallbackCooldown          = 2 * time.Minute
)

func (s *OpenAIGatewayService) forwardGrokResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel string,
	reqStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	if account.Type != AccountTypeOAuth && account.Type != AccountTypeAPIKey {
		return nil, fmt.Errorf("unsupported grok account type: %s", account.Type)
	}

	upstreamModel := MappedAccountModelForRequest(ctx, nil, account, originalModel)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = "grok-4.5"
	}
	if isGrokImageGenerationModel(upstreamModel) {
		message := fmt.Sprintf("model %s is an image model and is not available on /v1/responses; use /v1/images/generations instead", upstreamModel)
		// This is a client endpoint mismatch, not an upstream failure. Commit the
		// actionable 400 here so the handler cannot replace it with a generic 502.
		if c != nil {
			MarkResponseCommitted(c)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": message,
				},
			})
		}
		return nil, fmt.Errorf("%s", message)
	}
	// 记录最终发往 Grok 的模型，避免上游在响应生成前失败时丢失映射信息。
	SetOpsUpstreamModel(c, upstreamModel)
	patchedBody, clientToolMapping, err := patchGrokResponsesBodyWithClientTools(body, upstreamModel)
	if err != nil {
		setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
		MarkResponseCommitted(c)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": err.Error(), "param": "tools",
		}})
		return nil, err
	}
	setGrokResponsesClientToolMapping(c, clientToolMapping)
	// xAI 没有原生 /responses/compact；把它转成普通 Responses 总结请求，
	// 响应返回时再还原为 OpenAI compaction（上下文压缩）对象。
	if isOpenAIResponsesCompactPath(c) {
		patchedBody, err = buildGrokCompactRequestBody(patchedBody)
		if err != nil {
			return nil, err
		}
	}
	cacheIdentity := resolveGrokCacheIdentity(c, patchedBody, "", upstreamModel)
	mixedCacheIntentBody := append([]byte(nil), patchedBody...)
	patchedBody, err = applyGrokResponsesCacheIdentity(patchedBody, body, cacheIdentity, account.IsGrokOAuth())
	if err != nil {
		return nil, fmt.Errorf("apply grok prompt cache identity: %w", err)
	}
	patchedBody, err = applyGrokFreeRequestToolCacheRoute(c, patchedBody, mixedCacheIntentBody, account, cacheIdentity)
	if err != nil {
		return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", err)
	}

	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	upstreamStart := time.Now()
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		upstreamReq, buildErr := buildGrokResponsesRequest(upstreamCtx, c, account, patchedBody, token, cacheIdentity, s.cfg)
		if buildErr != nil {
			return nil, buildErr
		}
		resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		if err != nil {
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
		}
		if attempt > 0 || resp.StatusCode != http.StatusBadRequest {
			break
		}

		respBody := s.readUpstreamErrorBody(resp)
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if !isGrokInvalidEncryptedContentResponse(resp.StatusCode, respBody) {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		retryBody, changed, trimErr := trimGrokInvalidEncryptedContentRetryBody(patchedBody)
		if trimErr != nil {
			return nil, fmt.Errorf("prepare Grok invalid encrypted_content retry: %w", trimErr)
		}
		if !changed {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		patchedBody = retryBody
		slog.Info("grok_invalid_encrypted_content_retry", "account_id", account.ID, "cache_identity_present", cacheIdentity != "")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if shouldRetryGrokUnauthorized(ctx, account, resp.StatusCode) {
			if refreshedToken, refreshErr := s.refreshGrokAfterUnauthorizedForRequest(ctx, c, account); refreshErr == nil {
				_ = resp.Body.Close()
				return s.forwardGrokResponses(
					contextWithGrokUnauthorizedRetry(ctx, refreshedToken),
					c,
					account,
					body,
					originalModel,
					reqStream,
					startTime,
				)
			} else {
				return nil, refreshErr
			}
		}
		upstreamMsg := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))
		if upstreamMsg == "" {
			upstreamMsg = fmt.Sprintf("xAI upstream returned status %d", resp.StatusCode)
		}
		kind := "http_error"
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			kind = "failover"
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			Kind:               kind,
			Message:            upstreamMsg,
		})
		s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
		if resp.StatusCode == http.StatusTooManyRequests && !isGrokModelCapacityFailure(resp.StatusCode, respBody) {
			markGrokTeamModelRateLimit(account, upstreamModel, resolveGrokTeamRateLimitUntil(time.Now().Add(grokTeamRateLimitDefaultTTL), time.Now()))
		}
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		// Request-scoped 4xx errors must remain actionable to the client. Rewrapping
		// an invalid parameter or policy rejection as 502 incorrectly reports a
		// gateway outage and can make SDKs retry a request that cannot succeed.
		if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError {
			MarkResponseCommitted(c)
			writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
			contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
			if contentType == "" {
				contentType = "application/json"
			}
			c.Data(resp.StatusCode, contentType, respBody)
			return nil, fmt.Errorf("xAI upstream request error: %d message=%s", resp.StatusCode, upstreamMsg)
		}
		return s.handleErrorResponse(ctx, resp, c, account, patchedBody, upstreamModel)
	}

	s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode), upstreamModel)

	var usage *OpenAIUsage
	var firstTokenMs *int
	responseID := ""
	if reqStream {
		maxLineSize := defaultMaxLineSize
		if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
			maxLineSize = s.cfg.Gateway.MaxLineSize
		}
		resp.Body = newGrokResponsesBillingPingFilterBody(resp.Body, account, maxLineSize)
		if hasGrokResponsesClientToolMapping(clientToolMapping) {
			resp.Body = newGrokResponsesClientToolStreamBody(resp.Body, clientToolMapping, maxLineSize)
		}
		streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, upstreamModel)
		if err != nil {
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
		responseID = strings.TrimSpace(streamResult.responseID)
	} else {
		nonStreamResult, err := s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, upstreamModel)
		if err != nil {
			return nil, err
		}
		usage = nonStreamResult.usage
		responseID = strings.TrimSpace(nonStreamResult.responseID)
	}

	if usage == nil {
		usage = &OpenAIUsage{}
	}
	requestedReasoningEffort := requestedReasoningEffortFromContextOrBody(ctx, body, "reasoning.effort", "reasoning_effort")
	reasoningEffort := extractReasoningEffortAuditFromBody(patchedBody, "reasoning.effort", "reasoning_effort")
	return &OpenAIForwardResult{
		RequestID:                firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
		ResponseID:               responseID,
		Usage:                    *usage,
		Model:                    originalModel,
		UpstreamModel:            upstreamModel,
		RequestedReasoningEffort: requestedReasoningEffort,
		ReasoningEffort:          reasoningEffort,
		Stream:                   reqStream,
		OpenAIWSMode:             false,
		ResponseHeaders:          resp.Header.Clone(),
		Duration:                 time.Since(startTime),
		FirstTokenMs:             firstTokenMs,
	}, nil
}

func isGrokInvalidEncryptedContentResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}

	// xAI 既返回过扁平错误，也返回过 OpenAI 风格的嵌套错误；这里只匹配
	// 明确的 encrypted_content 解密失败，避免把其他 400 请求重放一遍。
	code := strings.TrimSpace(gjson.GetBytes(body, "code").String())
	message := ""
	errNode := gjson.GetBytes(body, "error")
	switch {
	case errNode.Type == gjson.String:
		message = errNode.String()
	case errNode.IsObject():
		message = firstNonEmpty(errNode.Get("message").String(), errNode.Get("error").String())
		if code == "" {
			code = strings.TrimSpace(errNode.Get("code").String())
		}
	default:
		message = gjson.GetBytes(body, "message").String()
	}
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if strings.EqualFold(code, "invalid_encrypted_content") {
		return true
	}
	if !strings.EqualFold(code, "invalid-argument") && code != "" {
		return false
	}
	if code == "" && !strings.Contains(normalized, "decrypt") {
		return false
	}
	return strings.Contains(normalized, "encrypted_content") &&
		(strings.Contains(normalized, "decrypt") || strings.Contains(normalized, "unmodified"))
}

// requestHasGrokEncryptedReasoning 只检查可以安全剥离的 reasoning 密文。
// 它用于 Messages 桥接收到不透明 400 时的一次性恢复，不放宽普通重试。
func requestHasGrokEncryptedReasoning(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	items := input.Array()
	if input.IsObject() {
		items = []gjson.Result{input}
	}
	for _, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		enc := item.Get("encrypted_content")
		if enc.Exists() && enc.Type != gjson.Null && strings.TrimSpace(enc.String()) != "" {
			return true
		}
	}
	return false
}

type grokEncryptedContentStripRetriedKey struct{}

func markGrokEncryptedContentStripRetried(ctx context.Context) context.Context {
	return context.WithValue(ctx, grokEncryptedContentStripRetriedKey{}, true)
}

func grokEncryptedContentStripRetried(ctx context.Context) bool {
	v, _ := ctx.Value(grokEncryptedContentStripRetriedKey{}).(bool)
	return v
}

// stripAnthropicThinkingSignatures 仅删除历史 thinking block 的 signature。
// Grok OAuth 换号后旧账号生成的加密签名无法解密；保留 thinking 文本和其他字段，
// 让同一客户端请求可以安全地在新账号上继续，而不会循环重试。
func stripAnthropicThinkingSignatures(body []byte) ([]byte, bool) {
	if len(body) == 0 || !bytes.Contains(body, []byte(`"signature"`)) {
		return body, false
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return body, false
	}
	changed := false
	for _, rawMsg := range messages {
		msg, ok := rawMsg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := block["type"].(string); typ != "thinking" {
				continue
			}
			if _, has := block["signature"]; has {
				delete(block, "signature")
				changed = true
			}
		}
	}
	if !changed {
		return body, false
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body, false
	}
	return out, true
}

func trimGrokInvalidEncryptedContentRetryBody(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	items := input.Array()
	if input.IsObject() {
		items = []gjson.Result{input}
	}
	hasEncryptedReasoning := false
	for _, item := range items {
		if strings.TrimSpace(item.Get("type").String()) == "reasoning" && item.Get("encrypted_content").Exists() {
			hasEncryptedReasoning = true
			break
		}
	}
	if !hasEncryptedReasoning {
		return body, false, nil
	}
	var requestBody map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&requestBody); err != nil {
		return nil, false, err
	}
	if !trimOpenAIEncryptedReasoningItems(requestBody) {
		return body, false, nil
	}
	retryBody, err := marshalOpenAIUpstreamJSON(requestBody)
	if err != nil {
		return nil, false, err
	}
	return retryBody, true, nil
}

func patchGrokResponsesBody(body []byte, upstreamModel string) ([]byte, error) {
	return patchGrokResponsesBodyBase(body, upstreamModel)
}

func patchGrokResponsesBodyWithClientTools(body []byte, upstreamModel string) ([]byte, apicompat.ResponsesClientToolMapping, error) {
	if !json.Valid(body) {
		return nil, apicompat.ResponsesClientToolMapping{}, fmt.Errorf("invalid json request body")
	}
	promoted, err := sanitizeGrokResponsesInput(body)
	if err != nil {
		return nil, apicompat.ResponsesClientToolMapping{}, err
	}
	adapted, mapping, err := adaptGrokResponsesClientTools(promoted)
	if err != nil {
		return nil, apicompat.ResponsesClientToolMapping{}, err
	}
	patched, err := patchGrokResponsesBodyBase(adapted, upstreamModel)
	if err != nil {
		return nil, apicompat.ResponsesClientToolMapping{}, err
	}
	return patched, mapping, nil
}

func patchGrokResponsesBodyBase(body []byte, upstreamModel string) ([]byte, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid json request body")
	}
	out, err := sjson.SetBytes(append([]byte(nil), body...), "model", upstreamModel)
	if err != nil {
		return nil, err
	}
	out, err = normalizeGrokResponsesReasoningEffort(out, upstreamModel)
	if err != nil {
		return nil, err
	}
	out, err = sanitizeGrokResponsesModelCapabilities(out, upstreamModel)
	if err != nil {
		return nil, err
	}
	for _, unsupportedField := range []string{"prompt_cache_retention", "safety_identifier"} {
		if gjson.GetBytes(out, unsupportedField).Exists() {
			out, err = sjson.DeleteBytes(out, unsupportedField)
			if err != nil {
				return nil, err
			}
		}
	}
	if strings.EqualFold(upstreamModel, "grok-4.5") {
		for _, unsupportedField := range []string{"presence_penalty", "presencePenalty", "frequency_penalty", "frequencyPenalty", "stop"} {
			if gjson.GetBytes(out, unsupportedField).Exists() {
				out, err = sjson.DeleteBytes(out, unsupportedField)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	out, err = sanitizeGrokResponsesUnsupportedFields(out)
	if err != nil {
		return nil, err
	}
	out, err = convertOpenAICompactInputsForGrok(out)
	if err != nil {
		return nil, err
	}
	out, err = sanitizeGrokResponsesInput(out)
	if err != nil {
		return nil, err
	}
	out, err = stripRedundantGrokViewImageTool(out)
	if err != nil {
		return nil, err
	}
	out, err = sanitizeGrokReasoningNullContent(out)
	if err != nil {
		return nil, err
	}
	out, err = sanitizeGrokResponsesTools(out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeGrokResponsesReasoningEffort(body []byte, upstreamModel string) ([]byte, error) {
	supportsEffort := grokSupportsReasoningEffort(upstreamModel)
	out := body
	var err error
	for _, field := range []string{"reasoning.effort", "reasoning_effort"} {
		value := gjson.GetBytes(out, field)
		if !value.Exists() {
			continue
		}
		normalized, keep := normalizeGrokReasoningEffortValue(value.String(), upstreamModel)
		if !supportsEffort || !keep {
			out, err = sjson.DeleteBytes(out, field)
		} else {
			out, err = sjson.SetBytes(out, field, normalized)
		}
		if err != nil {
			return nil, fmt.Errorf("normalize Grok reasoning field %s: %w", field, err)
		}
	}
	if camel := gjson.GetBytes(out, "reasoningEffort"); camel.Exists() {
		normalized, keep := normalizeGrokReasoningEffortValue(camel.String(), upstreamModel)
		out, err = sjson.DeleteBytes(out, "reasoningEffort")
		if err != nil {
			return nil, fmt.Errorf("remove Grok reasoningEffort: %w", err)
		}
		if supportsEffort && keep && !gjson.GetBytes(out, "reasoning_effort").Exists() {
			out, err = sjson.SetBytes(out, "reasoning_effort", normalized)
			if err != nil {
				return nil, fmt.Errorf("set Grok reasoning_effort: %w", err)
			}
		}
	}
	if reasoning := gjson.GetBytes(out, "reasoning"); reasoning.Exists() && reasoning.IsObject() && len(reasoning.Map()) == 0 {
		out, err = sjson.DeleteBytes(out, "reasoning")
		if err != nil {
			return nil, fmt.Errorf("remove empty Grok reasoning: %w", err)
		}
	}
	return out, nil
}

func normalizeGrokChatReasoningEffort(body []byte, upstreamModel string) ([]byte, error) {
	raw := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(body, "reasoningEffort").String())
	}
	normalized, keep := normalizeGrokReasoningEffortValue(raw, upstreamModel)
	keep = keep && grokSupportsReasoningEffort(upstreamModel)
	out := body
	var err error
	if gjson.GetBytes(out, "reasoningEffort").Exists() {
		out, err = sjson.DeleteBytes(out, "reasoningEffort")
		if err != nil {
			return nil, err
		}
	}
	if !keep {
		if gjson.GetBytes(out, "reasoning_effort").Exists() {
			out, err = sjson.DeleteBytes(out, "reasoning_effort")
		}
		return out, err
	}
	return sjson.SetBytes(out, "reasoning_effort", normalized)
}

func normalizeGrokReasoningEffortValue(raw, model string) (string, bool) {
	value := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "none", "low", "medium", "high":
		return value, true
	case "minimal":
		return "low", true
	case "xhigh", "extrahigh":
		if grokSupportsXHighReasoningEffort(model) {
			return "xhigh", true
		}
		return "high", true
	case "max", "ultra":
		return "high", true
	default:
		return "", false
	}
}

func grokSupportsXHighReasoningEffort(model string) bool {
	model = strings.ToLower(xai.StripGrokProviderPrefix(strings.TrimSpace(model)))
	return model == "grok-4.6" || model == "grok-4.6-latest"
}

func grokSupportsReasoningEffort(model string) bool {
	model = strings.ToLower(xai.StripGrokProviderPrefix(strings.TrimSpace(model)))
	switch model {
	case xai.DefaultTextModel, "grok-4.5-latest", "grok-4.6-latest",
		"grok-4.3", "grok-4.3-latest", "grok-3-mini", "grok-3-mini-fast",
		"grok-4.20-0309-reasoning", "grok-4.20-reasoning", "grok-4.20-multi-agent-0309":
		return true
	default:
		return false
	}
}

func sanitizeGrokResponsesModelCapabilities(body []byte, upstreamModel string) ([]byte, error) {
	if !grokModelRejectsReasoningEffort(upstreamModel) {
		return body, nil
	}

	out := body
	for _, field := range []string{"reasoning", "reasoning_effort", "reasoningEffort"} {
		if !gjson.GetBytes(out, field).Exists() {
			continue
		}
		var err error
		out, err = sjson.DeleteBytes(out, field)
		if err != nil {
			return nil, fmt.Errorf("remove unsupported Grok Composer %s: %w", field, err)
		}
	}
	return out, nil
}

func grokModelRejectsReasoningEffort(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = strings.TrimSpace(model[slash+1:])
	}
	switch model {
	case "grok-composer", "grok-composer-2.5-fast", "composer-2.5":
		return true
	default:
		return false
	}
}

func isGrokImageGenerationModel(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		model = strings.TrimSpace(model[slash+1:])
	}
	canonical := xai.CanonicalModelID(model)
	return strings.HasPrefix(canonical, "grok-imagine") &&
		!strings.HasPrefix(canonical, "grok-imagine-video")
}

var grokResponsesUnsupportedRecursiveFields = map[string]struct{}{
	"external_web_access": {},
}

func sanitizeGrokResponsesUnsupportedFields(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"external_web_access"`)) {
		return body, nil
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if !deleteJSONFields(payload, grokResponsesUnsupportedRecursiveFields) {
		return body, nil
	}
	return json.Marshal(payload)
}

// additional_tools 是 Codex/Responses Lite 的私有工具载体。xAI 会拒绝载体本身，
// 但能接受其中受支持的顶层工具；因此先按原顺序提升、去重，再由工具白名单过滤。
func sanitizeGrokResponsesInput(body []byte) ([]byte, error) {
	if !bytes.Contains(body, []byte(`"additional_tools"`)) {
		return body, nil
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, nil
	}

	rawItems := input.Array()
	filtered := make([]json.RawMessage, 0, len(rawItems))
	topLevelTools := gjson.GetBytes(body, "tools")
	mergedTools := make([]json.RawMessage, 0)
	seenTools := make(map[string]struct{})
	appendTool := func(tool gjson.Result) bool {
		key := grokResponsesToolDedupKey(tool)
		if _, exists := seenTools[key]; exists {
			return false
		}
		seenTools[key] = struct{}{}
		mergedTools = append(mergedTools, json.RawMessage(tool.Raw))
		return true
	}
	if topLevelTools.IsArray() {
		for _, tool := range topLevelTools.Array() {
			seenTools[grokResponsesToolDedupKey(tool)] = struct{}{}
			mergedTools = append(mergedTools, json.RawMessage(tool.Raw))
		}
	}

	promoted := false
	for _, item := range rawItems {
		if strings.TrimSpace(item.Get("type").String()) == "additional_tools" {
			tools := item.Get("tools")
			if tools.IsArray() {
				for _, tool := range tools.Array() {
					if appendTool(tool) {
						promoted = true
					}
				}
			}
			continue
		}
		filtered = append(filtered, json.RawMessage(item.Raw))
	}
	if len(filtered) == len(rawItems) {
		return body, nil
	}
	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetRawBytes(body, "input", encoded)
	if err != nil || !promoted {
		return body, err
	}
	encodedTools, err := json.Marshal(mergedTools)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encodedTools)
}

// 当前轮的内联图片已经能被 Grok 直接读取；继续保留客户端本地 view_image
// 会让部分模型只宣告工具调用却不继续作答。显式强制选择时必须尊重客户端意图。
func stripRedundantGrokViewImageTool(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, nil
	}
	items := input.Array()
	if len(items) == 0 {
		return body, nil
	}
	current := items[len(items)-1]
	if strings.TrimSpace(current.Get("role").String()) != "user" ||
		!openAIJSONValueMayContainImageInput(current) {
		return body, nil
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if toolChoice.IsObject() && strings.TrimSpace(toolChoice.Get("type").String()) == "function" {
		choiceName := strings.TrimSpace(toolChoice.Get("name").String())
		if choiceName == "" {
			choiceName = strings.TrimSpace(toolChoice.Get("function.name").String())
		}
		if choiceName == "view_image" {
			return body, nil
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, nil
	}
	filtered := make([]json.RawMessage, 0, len(tools.Array()))
	changed := false
	for _, tool := range tools.Array() {
		if strings.TrimSpace(tool.Get("type").String()) == "function" &&
			strings.TrimSpace(tool.Get("name").String()) == "view_image" {
			changed = true
			continue
		}
		filtered = append(filtered, json.RawMessage(tool.Raw))
	}
	if !changed || (len(filtered) == 0 && strings.TrimSpace(toolChoice.String()) == "required") {
		return body, nil
	}
	if len(filtered) == 0 {
		out, err := sjson.DeleteBytes(body, "tools")
		if err != nil {
			return nil, err
		}
		out, err = sjson.DeleteBytes(out, "parallel_tool_calls")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(toolChoice.String()) == "auto" {
			out, err = sjson.DeleteBytes(out, "tool_choice")
		}
		return out, err
	}

	encoded, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "tools", encoded)
}

func grokResponsesToolDedupKey(tool gjson.Result) string {
	toolType := strings.TrimSpace(tool.Get("type").String())
	if toolType != "" {
		if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
			return "type:" + toolType + "\x00name:" + name
		}
		if toolType == "mcp" {
			if label := strings.TrimSpace(tool.Get("server_label").String()); label != "" {
				return "type:mcp\x00server_label:" + label
			}
		}
	}
	return "json:" + normalizeCompatSeedJSON(json.RawMessage(tool.Raw))
}

// sanitizeGrokReasoningNullContent removes content:null only from reasoning
// input items. xAI rejects that optional null field with 422, while summary,
// encrypted content and non-null content must remain untouched.
func sanitizeGrokReasoningNullContent(body []byte) ([]byte, error) {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || !input.IsArray() {
		return body, nil
	}

	items := input.Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			continue
		}
		content := item.Get("content")
		if !content.Exists() || content.Type != gjson.Null {
			continue
		}
		var err error
		body, err = sjson.DeleteBytes(body, fmt.Sprintf("input.%d.content", i))
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func deleteJSONFields(value any, fields map[string]struct{}) bool {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		for field := range fields {
			if _, ok := typed[field]; ok {
				delete(typed, field)
				changed = true
			}
		}
		for _, child := range typed {
			if deleteJSONFields(child, fields) {
				changed = true
			}
		}
		return changed
	case []any:
		changed := false
		for _, child := range typed {
			if deleteJSONFields(child, fields) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

var grokResponsesSupportedToolTypes = map[string]struct{}{
	"code_execution":     {},
	"code_interpreter":   {},
	"collections_search": {},
	"file_search":        {},
	"function":           {},
	"mcp":                {},
	"shell":              {},
	"web_search":         {},
	"x_search":           {},
}

func sanitizeGrokResponsesTools(body []byte) ([]byte, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() {
		// Grok 会拒绝没有 tools 却残留 tool_choice 的 Responses 请求。
		if gjson.GetBytes(body, "tool_choice").Exists() {
			return sjson.DeleteBytes(body, "tool_choice")
		}
		return body, nil
	}
	if !tools.IsArray() {
		return body, nil
	}

	rawTools := tools.Array()
	filteredTools := make([]json.RawMessage, 0, len(rawTools))
	for _, tool := range rawTools {
		toolType := strings.TrimSpace(tool.Get("type").String())
		if _, ok := grokResponsesSupportedToolTypes[toolType]; ok {
			filteredTools = append(filteredTools, json.RawMessage(tool.Raw))
		}
	}

	var err error
	if len(filteredTools) != len(rawTools) {
		if len(filteredTools) == 0 {
			body, err = sjson.DeleteBytes(body, "tools")
		} else {
			var encoded []byte
			encoded, err = json.Marshal(filteredTools)
			if err != nil {
				return nil, err
			}
			body, err = sjson.SetRawBytes(body, "tools", encoded)
		}
		if err != nil {
			return nil, err
		}
	}

	toolChoice := gjson.GetBytes(body, "tool_choice")
	if !toolChoice.Exists() {
		return body, nil
	}
	if shouldDropGrokToolChoice(toolChoice, filteredTools) {
		body, err = sjson.DeleteBytes(body, "tool_choice")
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func shouldDropGrokToolChoice(toolChoice gjson.Result, tools []json.RawMessage) bool {
	if len(tools) == 0 {
		return true
	}
	if !toolChoice.IsObject() {
		return false
	}
	choiceType := strings.TrimSpace(toolChoice.Get("type").String())
	if choiceType == "" {
		return false
	}
	if _, ok := grokResponsesSupportedToolTypes[choiceType]; !ok {
		return true
	}
	if choiceType == "function" {
		choiceName := strings.TrimSpace(toolChoice.Get("name").String())
		if choiceName == "" {
			choiceName = strings.TrimSpace(toolChoice.Get("function.name").String())
		}
		if choiceName == "" {
			return false
		}
		for _, tool := range tools {
			var item struct {
				Type     string `json:"type"`
				Name     string `json:"name"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if err := json.Unmarshal(tool, &item); err != nil {
				continue
			}
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = strings.TrimSpace(item.Function.Name)
			}
			if strings.TrimSpace(item.Type) == "function" && name == choiceName {
				return false
			}
		}
		return true
	}
	return false
}

func (s *OpenAIGatewayService) bridgeGrokComposerImageInputs(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) ([]byte, OpenAIUsage, bool, error) {
	if !shouldBridgeGrokComposerImageInputs(body) {
		return body, OpenAIUsage{}, false, nil
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body, OpenAIUsage{}, false, fmt.Errorf("parse grok composer image bridge request: %w", err)
	}

	imageURLs := collectGrokComposerImageURLs(reqBody)
	if len(imageURLs) == 0 {
		return body, OpenAIUsage{}, false, nil
	}

	descriptions := make([]string, 0, len(imageURLs))
	var bridgeUsage OpenAIUsage
	for index, imageURL := range imageURLs {
		description, usage, err := s.describeGrokComposerImage(ctx, c, account, token, imageURL, index+1)
		if err != nil {
			return body, bridgeUsage, false, err
		}
		descriptions = append(descriptions, description)
		addOpenAIUsage(&bridgeUsage, usage)
	}

	if !rewriteGrokComposerImagesAsText(reqBody, descriptions) {
		return body, bridgeUsage, false, nil
	}
	bridgedBody, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, bridgeUsage, false, fmt.Errorf("serialize grok composer image bridge request: %w", err)
	}
	return bridgedBody, bridgeUsage, true, nil
}

func shouldBridgeGrokComposerImageInputs(body []byte) bool {
	if len(body) == 0 || !isGrokComposerModel(gjson.GetBytes(body, "model").String()) {
		return false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() {
		return false
	}
	return openAIJSONValueMayContainImageInput(messages)
}

func isGrokComposerModel(model string) bool {
	model = strings.TrimSpace(strings.ToLower(model))
	if model == "" {
		return false
	}
	if strings.Contains(model, "/") {
		parts := strings.Split(model, "/")
		model = strings.TrimSpace(parts[len(parts)-1])
	}
	return strings.Contains(model, "composer")
}

func collectGrokComposerImageURLs(reqBody map[string]any) []string {
	messages, ok := reqBody["messages"].([]any)
	if !ok {
		return nil
	}

	var imageURLs []string
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			if imageURL := grokComposerImageURLFromPart(part); imageURL != "" {
				imageURLs = append(imageURLs, imageURL)
			}
		}
	}
	return imageURLs
}

func grokComposerImageURLFromPart(part any) string {
	partMap, ok := part.(map[string]any)
	if !ok {
		return ""
	}
	if strings.TrimSpace(strings.ToLower(fmt.Sprint(partMap["type"]))) != "image_url" {
		return ""
	}
	switch imageURL := partMap["image_url"].(type) {
	case string:
		return normalizeGrokComposerImageURL(imageURL)
	case map[string]any:
		raw, _ := imageURL["url"].(string)
		return normalizeGrokComposerImageURL(raw)
	default:
		return ""
	}
}

func normalizeGrokComposerImageURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isEmptyBase64DataURI(trimmed) {
		return ""
	}
	return trimmed
}

func (s *OpenAIGatewayService) describeGrokComposerImage(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	token string,
	imageURL string,
	index int,
) (string, OpenAIUsage, error) {
	body, err := buildGrokComposerImageDescriptionBody(imageURL, index)
	if err != nil {
		return "", OpenAIUsage{}, err
	}

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := buildGrokResponsesRequest(upstreamCtx, c, account, body, token, "", s.cfg)
	releaseUpstreamCtx()
	if err != nil {
		return "", OpenAIUsage{}, fmt.Errorf("build grok composer image bridge request: %w", err)
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return "", OpenAIUsage{}, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		upstreamMsg := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))
		if upstreamMsg == "" {
			upstreamMsg = fmt.Sprintf("xAI image bridge upstream returned status %d", resp.StatusCode)
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			Kind:               "failover",
			Message:            upstreamMsg,
		})
		s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, grokComposerImageBridgeVisionModel)
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			return "", OpenAIUsage{}, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
			}
		}
		return "", OpenAIUsage{}, fmt.Errorf("grok composer image bridge upstream error: %s", upstreamMsg)
	}

	s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode), grokComposerImageBridgeVisionModel)
	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, nil)
	if err != nil {
		return "", OpenAIUsage{}, fmt.Errorf("read grok composer image bridge response: %w", err)
	}

	var parsed apicompat.ResponsesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", OpenAIUsage{}, fmt.Errorf("parse grok composer image bridge response: %w", err)
	}
	description := strings.TrimSpace(grokResponsesOutputText(&parsed))
	if description == "" {
		return "", copyOpenAIUsageFromResponsesUsage(parsed.Usage), fmt.Errorf("grok composer image bridge returned empty description")
	}
	return description, copyOpenAIUsageFromResponsesUsage(parsed.Usage), nil
}

func buildGrokComposerImageDescriptionBody(imageURL string, index int) ([]byte, error) {
	prompt := fmt.Sprintf("Describe image %d in concise, factual text for a downstream coding/composer model. Include visible text, UI elements, diagrams, errors, and spatial relationships. Do not mention that you are an image analysis bridge.", index)
	req := map[string]any{
		"model":             grokComposerImageBridgeVisionModel,
		"stream":            false,
		"store":             false,
		"max_output_tokens": grokComposerImageBridgeMaxOutputTokens,
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": prompt},
					map[string]any{"type": "input_image", "image_url": imageURL},
				},
			},
		},
	}
	return marshalOpenAIUpstreamJSON(req)
}

func grokResponsesOutputText(resp *apicompat.ResponsesResponse) string {
	if resp == nil {
		return ""
	}
	var parts []string
	for _, output := range resp.Output {
		for _, content := range output.Content {
			if content.Type == "output_text" || content.Type == "text" || content.Type == "input_text" {
				if text := strings.TrimSpace(content.Text); text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func rewriteGrokComposerImagesAsText(reqBody map[string]any, descriptions []string) bool {
	messages, ok := reqBody["messages"].([]any)
	if !ok {
		return false
	}

	imageIndex := 0
	changed := false
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		var textParts []string
		messageChanged := false
		for _, part := range parts {
			if imageURL := grokComposerImageURLFromPart(part); imageURL != "" {
				if imageIndex < len(descriptions) {
					textParts = append(textParts, fmt.Sprintf("Image %d description: %s", imageIndex+1, strings.TrimSpace(descriptions[imageIndex])))
				}
				imageIndex++
				messageChanged = true
				continue
			}
			if text := grokComposerTextFromPart(part); text != "" {
				textParts = append(textParts, text)
			}
		}
		if messageChanged {
			msgMap["content"] = strings.Join(textParts, "\n\n")
			changed = true
		}
	}
	return changed
}

func grokComposerTextFromPart(part any) string {
	partMap, ok := part.(map[string]any)
	if !ok {
		return ""
	}
	partType := strings.TrimSpace(strings.ToLower(fmt.Sprint(partMap["type"])))
	switch partType {
	case "text", "input_text":
		text, _ := partMap["text"].(string)
		return strings.TrimSpace(text)
	default:
		return ""
	}
}

func addOpenAIUsage(dst *OpenAIUsage, usage OpenAIUsage) {
	if dst == nil {
		return
	}
	dst.InputTokens += usage.InputTokens
	dst.ImageInputTokens += usage.ImageInputTokens
	dst.OutputTokens += usage.OutputTokens
	dst.CacheCreationInputTokens += usage.CacheCreationInputTokens
	dst.CacheReadInputTokens += usage.CacheReadInputTokens
	dst.ImageOutputTokens += usage.ImageOutputTokens
}

func buildGrokResponsesRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token, cacheIdentity string, cfg *config.Config) (*http.Request, error) {
	targetURL, err := buildGrokResponsesURL(account, cfg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if account.IsGrokOAuth() {
		applyGrokCLIHeaders(req.Header)
	}
	applyGrokCacheHeaders(req.Header, cacheIdentity)
	if c != nil {
		if v := c.GetHeader("OpenAI-Beta"); strings.TrimSpace(v) != "" {
			req.Header.Set("OpenAI-Beta", v)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	return req, nil
}

func applyGrokCLIHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	version := xai.ResolveCLIVersion()
	headers.Set("User-Agent", xai.CLIUserAgent(version))
	headers.Set("X-Grok-Client-Version", version)
	headers.Set("x-grok-client-version", version)
	headers.Set("x-grok-client-identifier", xai.CLIClientIdentifier)
	headers.Set("X-Grok-Client-Mode", "interactive")
}

func (s *OpenAIGatewayService) updateGrokUsageSnapshot(ctx context.Context, account *Account, snapshot *xai.QuotaSnapshot, observedModel ...string) {
	s.updateGrokUsageSnapshotWithPolicy(ctx, account, snapshot, true, observedModel...)
}

// updateGrokUsageSnapshotWithPolicy always persists the sanitized quota
// snapshot, but can suppress account-wide scheduling changes. Grok sometimes
// returns 429 for one overloaded model; treating that as account exhaustion
// would incorrectly remove healthy sibling models from scheduling.
func (s *OpenAIGatewayService) updateGrokUsageSnapshotWithPolicy(ctx context.Context, account *Account, snapshot *xai.QuotaSnapshot, allowAccountLimit bool, observedModel ...string) {
	if s == nil || account == nil || account.ID <= 0 || snapshot == nil {
		return
	}
	model := ""
	if len(observedModel) > 0 {
		model = observedModel[0]
	}
	stampGrokQuotaSnapshotForPlan(account, snapshot, model)
	accountID := account.ID
	now := time.Now()
	resetAt, hasActiveLimit := grokRateLimitResetAt(snapshot, now)
	if hasActiveLimit {
		normalizeGrokExhaustedWindowResets(snapshot, resetAt, now)
	}
	critical := snapshot.StatusCode == http.StatusTooManyRequests || hasActiveLimit
	if s.codexSnapshotThrottle != nil {
		allowed := s.codexSnapshotThrottle.Allow(accountID, now)
		if !critical && !allowed {
			return
		}
	}

	stateCtx := ctx
	if allowAccountLimit && hasActiveLimit {
		var cancel context.CancelFunc
		stateCtx, cancel = openAIAccountStateContext(ctx)
		defer cancel()
	}
	updates := map[string]any{grokQuotaSnapshotExtraKey: snapshot}
	if allowAccountLimit {
		for key, value := range buildGrokSchedulerExtraUpdates(snapshot, now) {
			updates[key] = value
		}
	}
	// 调度快照中的 Account 可能被多个并发请求共享。只在本次调用的副本上合并
	// Extra，避免 map 并发写和把观测字段泄漏进其他请求的只读快照。
	accountSnapshot := *account
	accountSnapshot.Extra = make(map[string]any, len(account.Extra)+len(updates))
	for key, value := range account.Extra {
		accountSnapshot.Extra[key] = value
	}
	mergeAccountExtra(&accountSnapshot, updates)
	if s.accountRepo != nil {
		_ = s.accountRepo.UpdateExtra(stateCtx, accountID, updates)
	}
	if allowAccountLimit && s.rateLimitService != nil {
		s.rateLimitService.ApplyGrokSchedulingThreshold(stateCtx, &accountSnapshot)
	}
	// A successful request can consume the last request/token. Persist that
	// exhausted window immediately instead of waiting for a later 429.
	if allowAccountLimit && hasActiveLimit {
		s.rateLimitGrok(stateCtx, account, resetAt)
	}
}

func parseGrokQuotaSnapshot(headers http.Header, statusCode int, now time.Time) *xai.QuotaSnapshot {
	snapshot := xai.ParseQuotaHeaders(headers, statusCode)
	if snapshot == nil && statusCode == http.StatusTooManyRequests {
		return &xai.QuotaSnapshot{
			StatusCode: statusCode,
			UpdatedAt:  now.UTC().Format(time.RFC3339),
		}
	}
	return snapshot
}

func normalizeGrokExhaustedWindowResets(snapshot *xai.QuotaSnapshot, resetAt, now time.Time) {
	if snapshot == nil || !resetAt.After(now) {
		return
	}
	for _, window := range []*xai.QuotaWindow{snapshot.Requests, snapshot.Tokens} {
		if window == nil || window.Remaining == nil || *window.Remaining > 0 {
			continue
		}
		candidate := time.Time{}
		if window.ResetUnix != nil && *window.ResetUnix > 0 {
			candidate = time.Unix(*window.ResetUnix, 0)
		} else if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(window.ResetAt)); err == nil {
			candidate = parsed
		}
		if !candidate.After(now) {
			candidate = resetAt
		}
		resetUnix := candidate.Unix()
		window.ResetUnix = &resetUnix
		window.ResetAt = candidate.UTC().Format(time.RFC3339)
	}
}

func grokRateLimitResetAt(snapshot *xai.QuotaSnapshot, now time.Time) (time.Time, bool) {
	if snapshot == nil {
		return time.Time{}, false
	}

	retryAfterExpired := false
	var resetAt time.Time
	if snapshot.RetryAfterSeconds != nil && *snapshot.RetryAfterSeconds > 0 {
		observedAt := now
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(snapshot.UpdatedAt)); err == nil {
			observedAt = parsed
		}
		retryAfterResetAt := observedAt.Add(time.Duration(*snapshot.RetryAfterSeconds) * time.Second)
		if retryAfterResetAt.After(now) {
			resetAt = retryAfterResetAt
		} else {
			retryAfterExpired = true
		}
	}

	exhausted := false
	for _, window := range []*xai.QuotaWindow{snapshot.Requests, snapshot.Tokens} {
		if window == nil || window.Remaining == nil || *window.Remaining > 0 {
			continue
		}
		exhausted = true
		candidate := time.Time{}
		if window.ResetUnix != nil && *window.ResetUnix > 0 {
			candidate = time.Unix(*window.ResetUnix, 0)
		} else if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(window.ResetAt)); err == nil {
			candidate = parsed
		}
		if candidate.After(now) && candidate.After(resetAt) {
			resetAt = candidate
		}
	}
	if !resetAt.IsZero() {
		return resetAt, true
	}
	if retryAfterExpired {
		return time.Time{}, false
	}
	if exhausted || snapshot.StatusCode == http.StatusTooManyRequests {
		return now.Add(grokRateLimitFallbackCooldown), true
	}
	return time.Time{}, false
}

func normalizeGrokRateLimitResetAt(account *Account, resetAt, now time.Time) time.Time {
	if !resetAt.After(now) {
		resetAt = now.Add(grokRateLimitFallbackCooldown)
	}
	if account != nil && account.RateLimitResetAt != nil && account.RateLimitResetAt.After(resetAt) {
		resetAt = *account.RateLimitResetAt
	}
	return resetAt
}

type grokRateLimitExtendingRepository interface {
	SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error
}

func persistGrokRateLimit(ctx context.Context, repo AccountRepository, account *Account, resetAt time.Time) {
	if repo == nil || account == nil || account.ID <= 0 {
		return
	}
	resetAt = normalizeGrokRateLimitResetAt(account, resetAt, time.Now())
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	var err error
	if extendingRepo, ok := repo.(grokRateLimitExtendingRepository); ok {
		err = extendingRepo.SetRateLimitedIfLater(stateCtx, account.ID, resetAt)
	} else {
		err = repo.SetRateLimited(stateCtx, account.ID, resetAt)
	}
	if err != nil {
		slog.Warn("persist_grok_rate_limit_failed", "account_id", account.ID, "reset_at", resetAt.UTC(), "error", err)
	}
}

func (s *OpenAIGatewayService) rateLimitGrok(ctx context.Context, account *Account, resetAt time.Time) {
	if s == nil || account == nil {
		return
	}
	now := time.Now()
	resetAt = normalizeGrokRateLimitResetAt(account, resetAt, now)
	runtimeUntil := resetAt
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(runtimeUntil) {
		runtimeUntil = *account.TempUnschedulableUntil
	}
	s.BlockAccountScheduling(account, runtimeUntil, "429")
	persistGrokRateLimit(ctx, s.accountRepo, account, resetAt)
}

func (s *OpenAIGatewayService) handleGrokAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte, observedModel ...string) {
	if s == nil || account == nil {
		return
	}
	// Prompt/content safety rejection belongs to this request, not to the
	// credential. Cooling the account would make an unrelated healthy account
	// disappear from scheduling and switching accounts would not fix the input.
	if isGrokRequestScopedPolicyError(statusCode, responseBody) {
		return
	}
	now := time.Now()
	model := ""
	if len(observedModel) > 0 {
		model = strings.TrimSpace(observedModel[0])
	}
	modelCapacity := model != "" && isGrokModelCapacityFailure(statusCode, responseBody)
	if modelCapacity {
		// Keep the response headers for diagnostics and tier inference, but do not
		// let a model-scoped 429 install a durable account-wide rate limit.
		s.updateGrokUsageSnapshotWithPolicy(ctx, account, parseGrokQuotaSnapshot(headers, statusCode, now), false, model)
		// pool mode（账号池模式）由上游池和请求内重试处理，不写本地账号
		// 或模型健康状态；这样既不会误停整个池，也不会挡住同账号重试。
		if account.IsPoolMode() {
			return
		}
		decision := s.blockOpenAIAccountModelImmediately(account, model, now, grokModelCapacityCooldown)
		if !decision.BlockUntil.IsZero() {
			slog.Warn("grok_model_capacity_cooldown",
				"account_id", account.ID,
				"model", openAIAccountModelTransientModel(model),
				"cooldown_ms", decision.Cooldown.Milliseconds(),
				"block_scope", "account_model",
			)
			return
		}
	}
	s.updateGrokUsageSnapshot(ctx, account, parseGrokQuotaSnapshot(headers, statusCode, now), model)
	switch statusCode {
	case http.StatusUnauthorized:
		s.tempUnscheduleGrok(ctx, account, 10*time.Minute, "grok oauth token unauthorized")
	case http.StatusPaymentRequired:
		// 402 表示当前 Grok 账号没有可用付款资格。短暂移出调度，避免
		// 同一个失败账号在多账号 failover 中被反复选中。
		s.tempUnscheduleGrok(ctx, account, 30*time.Minute, "grok payment required")
	case http.StatusForbidden:
		if !s.applyGrokForbiddenPolicy(ctx, account, responseBody) {
			s.tempUnscheduleGrok(ctx, account, 30*time.Minute, "grok entitlement or subscription tier denied")
		}
	case http.StatusTooManyRequests:
		// updateGrokUsageSnapshot installs both runtime and durable rate-limit state.
	default:
		// pool mode（账号池模式）的 5xx 只代表本次上游失败；池内地址可能
		// 已由上游轮换，不能把整个账号冷却两分钟。
		if statusCode >= 500 && !account.IsPoolMode() {
			s.tempUnscheduleGrok(ctx, account, 2*time.Minute, "grok upstream temporary error")
		}
	}
	_ = responseBody
}

const grokModelCapacityCooldown = 3 * time.Minute

// isGrokModelCapacityFailure only accepts explicit capacity wording from the
// upstream body. A bare 5xx remains account-scoped under the existing policy;
// validation and content-policy failures never become model cooldowns.
func isGrokModelCapacityFailure(statusCode int, responseBody []byte) bool {
	if statusCode != http.StatusTooManyRequests && statusCode < http.StatusInternalServerError {
		return false
	}
	code := strings.TrimSpace(extractUpstreamErrorCode(responseBody))
	message := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	text := strings.TrimSpace(code + " " + message)
	if text == "" {
		text = strings.TrimSpace(string(responseBody))
	}
	text = strings.ToLower(text)
	for _, marker := range []string{
		"engine_overloaded",
		"model_capacity",
		"model capacity",
		"capacity exhausted",
		"overloaded",
		"server_busy",
		"server busy",
		"too many concurrent",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) shouldFailoverGrokUpstreamError(statusCode int, responseBody []byte) bool {
	if isGrokRequestScopedPolicyError(statusCode, responseBody) {
		return false
	}
	// 404 generally means an invalid model/task identifier and 409 means a
	// request-state conflict. Both are request scoped; changing credentials can
	// create duplicate work or hide the actionable upstream error.
	switch statusCode {
	case http.StatusNotFound, http.StatusConflict:
		return false
	default:
		return s.shouldFailoverUpstreamError(statusCode)
	}
}

func isGrokRequestScopedPolicyError(statusCode int, responseBody []byte) bool {
	if statusCode == http.StatusForbidden {
		return isGrokContentPolicyRejection(statusCode, responseBody)
	}
	if statusCode != http.StatusBadRequest && statusCode != http.StatusConflict {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(responseBody) + " " + extractUpstreamErrorMessage(responseBody) + " " + string(responseBody)))
	for _, marker := range []string{
		"content_policy",
		"content policy",
		"policy_violation",
		"policy violation",
		"content moderation",
		"moderation_blocked",
		"blocked by policy",
		"prompt was rejected",
		"safety violation",
		"safety system",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) tempUnscheduleGrok(ctx context.Context, account *Account, cooldown time.Duration, reason string) {
	if s == nil || account == nil {
		return
	}
	until := time.Now().Add(cooldown)
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(until) {
		until = *account.TempUnschedulableUntil
	}
	s.BlockAccountScheduling(account, until, reason)
	if s.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, reason)
	}
}
