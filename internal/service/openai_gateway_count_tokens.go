package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
	"go.uber.org/zap"
)

const (
	openAIResponsesInputItemTokenOverhead = 3
	openAIResponsesContentPartOverhead    = 1
	openAIInputTokensFallbackMinimum      = 1
)

type openAIInputTokensCountRequest struct {
	Model        string                    `json:"model"`
	Instructions string                    `json:"instructions,omitempty"`
	Input        json.RawMessage           `json:"input,omitempty"`
	Tools        []apicompat.ResponsesTool `json:"tools,omitempty"`
	ToolChoice   json.RawMessage           `json:"tool_choice,omitempty"`
}

type openAIInputTokensCountPrepared struct {
	Request         openAIInputTokensCountRequest
	OriginalModel   string
	NormalizedModel string
	BillingModel    string
	UpstreamModel   string
}

// ForwardResponsesInputTokens 处理原生 Responses input_tokens 预检。预检只返回
// Token 数，不创建上游生成任务，也不写 usage（用量）或账本。第三方中转和
// OpenAI-compatible（OpenAI 兼容）供应商经常没有该端点，因此直接使用本地估算。
func (s *OpenAIGatewayService) ForwardResponsesInputTokens(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
) error {
	if account == nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusServiceUnavailable, "api_error", "No available accounts")
		return fmt.Errorf("responses input_tokens: missing account")
	}

	prepared, err := prepareNativeOpenAIInputTokensCountRequest(body, account)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}
	if shouldEstimateOpenAIInputTokensLocally(account) {
		writeOpenAIResponsesInputTokensFallback(c, account, prepared, 0, "local_estimate")
		return nil
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("responses input_tokens: get access token: %w", err)
	}

	upstreamBody := ReplaceModelInBody(body, prepared.UpstreamModel)
	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, account, upstreamBody, token)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("responses input_tokens: build upstream request: %w", err)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("responses input_tokens: upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		return fmt.Errorf("responses input_tokens: read upstream response: %w", err)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if isOpenAIResponsesInputTokensUnsupported(account, resp.StatusCode, respBody) {
			writeOpenAIResponsesInputTokensFallback(c, account, prepared, resp.StatusCode, "upstream_unsupported")
			return nil
		}
		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		}
		upstreamMessage := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		setOpsUpstreamError(c, resp.StatusCode, upstreamMessage, "")
		writeOpenAIResponsesInputTokensError(c, resp.StatusCode, "upstream_error", "Upstream request failed")
		if upstreamMessage == "" {
			return fmt.Errorf("responses input_tokens: upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("responses input_tokens: upstream error: %d message=%s", resp.StatusCode, upstreamMessage)
	}

	if !gjson.GetBytes(respBody, "input_tokens").Exists() {
		writeOpenAIResponsesInputTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream response missing input_tokens")
		return fmt.Errorf("responses input_tokens: upstream response missing input_tokens")
	}
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(http.StatusOK, contentType, respBody)
	return nil
}

func prepareNativeOpenAIInputTokensCountRequest(body []byte, account *Account) (*openAIInputTokensCountPrepared, error) {
	var request openAIInputTokensCountRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("parse responses input_tokens request: %w", err)
	}
	originalModel := strings.TrimSpace(request.Model)
	if originalModel == "" {
		return nil, fmt.Errorf("parse responses input_tokens request: model is required")
	}
	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	request.Model = upstreamModel
	return &openAIInputTokensCountPrepared{
		Request:         request,
		OriginalModel:   originalModel,
		NormalizedModel: originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

func shouldEstimateOpenAIInputTokensLocally(account *Account) bool {
	if account == nil || account.IsGrok() || isCNProviderPlatformName(account.Platform) || account.Type == AccountTypeUpstream {
		return true
	}
	if account.Type != AccountTypeAPIKey {
		return false
	}
	rawBaseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if rawBaseURL == "" {
		return false
	}
	parsed, err := url.Parse(rawBaseURL)
	return err != nil || !strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func isCNProviderPlatformName(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "kimi", "zhipu", "deepseek":
		return true
	default:
		return false
	}
}

func isOpenAIResponsesInputTokensUnsupported(account *Account, statusCode int, body []byte) bool {
	if statusCode == http.StatusNotFound {
		return true
	}
	return account != nil && account.Type == AccountTypeOAuth && isOpenAIOAuthInputTokensUnsupported(statusCode, body)
}

func writeOpenAIResponsesInputTokensFallback(c *gin.Context, account *Account, prepared *openAIInputTokensCountPrepared, statusCode int, reason string) {
	estimated := openAIInputTokensFallbackMinimum
	if prepared != nil {
		if got, err := estimateOpenAIInputTokens(prepared.Request); err == nil && got > 0 {
			estimated = got
		}
	}
	accountID := int64(0)
	upstreamModel := ""
	if account != nil {
		accountID = account.ID
	}
	if prepared != nil {
		upstreamModel = prepared.UpstreamModel
	}
	logger.L().Info("openai responses input_tokens: local estimate fallback",
		zap.Int64("account_id", accountID),
		zap.Int("upstream_status", statusCode),
		zap.Int("estimated_input_tokens", estimated),
		zap.String("upstream_model", upstreamModel),
		zap.String("reason", reason),
	)
	c.JSON(http.StatusOK, gin.H{
		"object":       "response.input_tokens",
		"input_tokens": estimated,
	})
}

func writeOpenAIResponsesInputTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": message}})
}

// EstimateGrokCountTokens locally estimates Anthropic-compatible count_tokens.
// Grok has no compatible upstream endpoint, so this deliberately performs no
// account selection, credential access, lease reservation, usage or ledger write.
func EstimateGrokCountTokens(body []byte) (int, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return 0, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}
	if strings.TrimSpace(anthropicReq.Model) == "" {
		return 0, fmt.Errorf("parse anthropic count_tokens request: model is required")
	}
	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return 0, fmt.Errorf("convert anthropic request to responses: %w", err)
	}
	estimated, err := estimateOpenAIInputTokens(openAIInputTokensCountRequest{
		Model:        anthropicReq.Model,
		Instructions: responsesReq.Instructions,
		Input:        responsesReq.Input,
		Tools:        responsesReq.Tools,
		ToolChoice:   responsesReq.ToolChoice,
	})
	if err != nil {
		return 0, fmt.Errorf("estimate grok input tokens: %w", err)
	}
	if estimated < openAIInputTokensFallbackMinimum {
		estimated = openAIInputTokensFallbackMinimum
	}
	return estimated, nil
}

type openAIInputTokensEstimateEnvelope struct {
	Instructions string          `json:"instructions,omitempty"`
	System       json.RawMessage `json:"system,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Messages     json.RawMessage `json:"messages,omitempty"`
	Tools        json.RawMessage `json:"tools,omitempty"`
	ToolChoice   json.RawMessage `json:"tool_choice,omitempty"`
	Functions    json.RawMessage `json:"functions,omitempty"`
	FunctionCall json.RawMessage `json:"function_call,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`
}

// ForwardCountTokensAsAnthropic bridges Anthropic /v1/messages/count_tokens to
// OpenAI POST /v1/responses/input_tokens and returns Anthropic-compatible output.
// 这个接口只做 token 预估，不产生用量记录；OAuth 上游缺少 input_tokens 权限时用本地估算兜底。
func (s *OpenAIGatewayService) ForwardCountTokensAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) error {
	if account == nil {
		writeAnthropicCountTokensError(c, http.StatusServiceUnavailable, "api_error", "No available OpenAI accounts")
		return fmt.Errorf("count_tokens: missing account")
	}

	prepared, err := prepareOpenAIInputTokensCountRequest(ctx, body, account, defaultMappedModel)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return err
	}

	upstreamBody, err := marshalOpenAIUpstreamJSON(prepared.Request)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("marshal openai input_tokens body: %w", err)
	}

	logger.L().Debug("openai count_tokens: model mapping applied",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", prepared.OriginalModel),
		zap.String("normalized_model", prepared.NormalizedModel),
		zap.String("billing_model", prepared.BillingModel),
		zap.String("upstream_model", prepared.UpstreamModel),
	)

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return fmt.Errorf("get access token: %w", err)
	}

	upstreamReq, err := s.buildInputTokensUpstreamRequest(ctx, c, account, upstreamBody, token)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return fmt.Errorf("build input_tokens request: %w", err)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return fmt.Errorf("openai input_tokens upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		return fmt.Errorf("read input_tokens response: %w", err)
	}

	if resp.StatusCode >= 400 {
		respBody = s.redactAgentIdentitySensitiveBody(ctx, account, respBody)
		if account.IsOpenAIAgentIdentity() && !agentIdentityTaskRecoveryWasTried(ctx) && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, respBody) {
			expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
			if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
				writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Agent Identity task recovery failed")
				return fmt.Errorf("Agent Identity task recovery failed: %w", recoverErr)
			}
			return s.ForwardCountTokensAsAnthropic(markAgentIdentityTaskRecoveryTried(ctx), c, account, body, defaultMappedModel)
		}
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		if account.Type == AccountTypeOAuth && isOpenAIOAuthInputTokensUnsupported(resp.StatusCode, respBody) {
			writeOpenAIOAuthInputTokensFallback(c, account, prepared, resp.StatusCode)
			return nil
		}

		if s.rateLimitService != nil {
			s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		}

		if isOpenAIInputTokensUnsupported(resp.StatusCode, respBody) {
			writeAnthropicCountTokensError(c, http.StatusNotFound, "not_found_error", "Token counting is not supported by upstream")
			return nil
		}

		upstreamDetail := ""
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
			if maxBytes <= 0 {
				maxBytes = 2048
			}
			upstreamDetail = truncateString(string(respBody), maxBytes)
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

		errMsg := "Upstream request failed"
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			errMsg = "Rate limit exceeded"
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
			errMsg = "Upstream service temporarily unavailable"
		}
		writeAnthropicCountTokensError(c, resp.StatusCode, "upstream_error", errMsg)
		if upstreamMsg == "" {
			return fmt.Errorf("input_tokens upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("input_tokens upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	inputTokens := gjson.GetBytes(respBody, "input_tokens")
	if !inputTokens.Exists() {
		writeAnthropicCountTokensError(c, http.StatusBadGateway, "upstream_error", "Upstream response missing input_tokens")
		return fmt.Errorf("input_tokens response missing input_tokens field")
	}

	c.JSON(http.StatusOK, gin.H{"input_tokens": int(inputTokens.Int())})
	return nil
}

func prepareOpenAIInputTokensCountRequest(
	ctx context.Context,
	body []byte,
	account *Account,
	defaultMappedModel string,
) (*openAIInputTokensCountPrepared, error) {
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic count_tokens request: %w", err)
	}

	originalModel := anthropicReq.Model
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	billingModel := ResolveOpenAIForwardModelForRequest(ctx, nil, account, normalizedModel, strings.TrimSpace(defaultMappedModel))
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)

	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic request to responses: %w", err)
	}

	return &openAIInputTokensCountPrepared{
		Request: openAIInputTokensCountRequest{
			Model:        upstreamModel,
			Instructions: responsesReq.Instructions,
			Input:        responsesReq.Input,
			Tools:        responsesReq.Tools,
			ToolChoice:   responsesReq.ToolChoice,
		},
		OriginalModel:   originalModel,
		NormalizedModel: normalizedModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

func (s *OpenAIGatewayService) buildInputTokensUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, error) {
	targetURL := openaiPlatformAPIInputTokensURL
	if account.Type == AccountTypeAPIKey {
		if baseURL := account.GetOpenAIBaseURL(); strings.TrimSpace(baseURL) != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesInputTokensURL(validatedURL)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lower := strings.ToLower(strings.TrimSpace(key))
			if lower != "user-agent" && lower != "accept-language" {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}

	return req, nil
}

func writeAnthropicCountTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func isOpenAIInputTokensUnsupported(statusCode int, body []byte) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	return strings.Contains(msg, "input_tokens") && strings.Contains(msg, "not found")
}

func writeOpenAIOAuthInputTokensFallback(c *gin.Context, account *Account, prepared *openAIInputTokensCountPrepared, statusCode int) {
	estimated := openAIInputTokensFallbackMinimum
	if got, err := estimateOpenAIInputTokens(prepared.Request); err == nil {
		if got > 0 {
			estimated = got
		}
		logger.L().Info("openai count_tokens: oauth fallback to local tiktoken estimate",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", statusCode),
			zap.Int("estimated_input_tokens", estimated),
			zap.String("upstream_model", prepared.UpstreamModel),
		)
	} else {
		logger.L().Warn("openai count_tokens: oauth local tiktoken fallback failed, using minimum estimate",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", statusCode),
			zap.Int("estimated_input_tokens", estimated),
			zap.String("upstream_model", prepared.UpstreamModel),
			zap.Error(err),
		)
	}

	c.JSON(http.StatusOK, gin.H{"input_tokens": estimated})
}

func isOpenAIOAuthInputTokensUnsupported(statusCode int, body []byte) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
	default:
		return false
	}

	bodyLower := strings.ToLower(string(body))
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body)))

	if code == "missing_scope" ||
		strings.Contains(bodyLower, "api.responses.write") ||
		strings.Contains(bodyLower, "missing scopes") ||
		strings.Contains(bodyLower, "insufficient_scope") {
		return true
	}

	if statusCode == http.StatusNotFound && isOpenAIInputTokensUnsupported(statusCode, body) {
		return true
	}

	return strings.Contains(msg, "input_tokens") &&
		(strings.Contains(msg, "not found") ||
			strings.Contains(msg, "not supported") ||
			strings.Contains(msg, "unsupported"))
}

// isHTMLResponse 只识别明确的 HTML 文档前缀，避免把普通文本或结构化 JSON
// 错误误判为代理/CDN 页面。
func isHTMLResponse(body []byte) bool {
	trimmed := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(trimmed, "<!doctype html") ||
		strings.HasPrefix(trimmed, "<html")
}

func estimateOpenAIInputTokens(req openAIInputTokensCountRequest) (int, error) {
	codec, err := openAIInputTokensCodecForModel(req.Model)
	if err != nil {
		return 0, err
	}

	total := 0
	addCount := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	if err := addCount(req.Instructions); err != nil {
		return 0, err
	}
	inputTokens, err := estimateOpenAIInputTokensForInput(codec, req.Input)
	if err != nil {
		return 0, err
	}
	total += inputTokens

	for _, tool := range req.Tools {
		raw, err := marshalOpenAIUpstreamJSON(tool)
		if err != nil {
			return 0, err
		}
		if err := addCount(string(raw)); err != nil {
			return 0, err
		}
	}
	if len(req.ToolChoice) > 0 {
		compacted, err := compactOpenAIInputTokensJSON(req.ToolChoice)
		if err != nil {
			return 0, err
		}
		if err := addCount(compacted); err != nil {
			return 0, err
		}
	}

	if total < 0 {
		return 0, nil
	}
	return total, nil
}

// estimateOpenAIInputTokensFromRequestBody 为 Priority（优先处理）长上下文
// 前置校验提供本地估算。它同时覆盖 Responses、Chat Completions 以及
// response.create WebSocket 帧；估算失败时调用方应记录告警并交给上游校验。
func estimateOpenAIInputTokensFromRequestBody(model string, body []byte) (int, error) {
	var envelope openAIInputTokensEstimateEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, fmt.Errorf("parse openai request for token estimate: %w", err)
	}
	if len(bytes.TrimSpace(envelope.Input)) == 0 && len(bytes.TrimSpace(envelope.Messages)) == 0 && len(bytes.TrimSpace(envelope.Response)) > 0 {
		return estimateOpenAIInputTokensFromRequestBody(model, envelope.Response)
	}

	if len(bytes.TrimSpace(envelope.Input)) > 0 {
		total, err := estimateOpenAIInputTokens(openAIInputTokensCountRequest{
			Model:        model,
			Instructions: envelope.Instructions,
			Input:        envelope.Input,
		})
		if err != nil {
			return 0, err
		}
		codec, err := openAIInputTokensCodecForModel(model)
		if err != nil {
			return 0, err
		}
		for _, raw := range []json.RawMessage{envelope.Tools, envelope.ToolChoice} {
			count, err := estimateOpenAIRawJSONTokens(codec, raw)
			if err != nil {
				return 0, err
			}
			total += count
		}
		return total, nil
	}

	codec, err := openAIInputTokensCodecForModel(model)
	if err != nil {
		return 0, err
	}
	total, err := estimateOpenAIChatMessagesTokens(codec, envelope.Messages)
	if err != nil {
		return 0, err
	}
	if envelope.Instructions != "" {
		count, countErr := codec.Count(envelope.Instructions)
		if countErr != nil {
			return 0, countErr
		}
		total += count
	}
	for _, raw := range []json.RawMessage{envelope.System, envelope.Tools, envelope.ToolChoice, envelope.Functions, envelope.FunctionCall} {
		count, countErr := estimateOpenAIRawJSONTokens(codec, raw)
		if countErr != nil {
			return 0, countErr
		}
		total += count
	}
	return total, nil
}

func estimateOpenAIChatMessagesTokens(codec tokenizer.Codec, raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}
	var messages []apicompat.ChatMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return estimateOpenAIRawJSONTokens(codec, raw)
	}

	total := 0
	for _, message := range messages {
		total += openAIResponsesInputItemTokenOverhead
		for _, value := range []string{message.Role, message.Name, message.ReasoningContent, message.ToolCallID} {
			count, err := codec.Count(value)
			if err != nil {
				return 0, err
			}
			total += count
		}

		if len(bytes.TrimSpace(message.Content)) > 0 {
			var contentText string
			if err := json.Unmarshal(message.Content, &contentText); err == nil {
				count, countErr := codec.Count(contentText)
				if countErr != nil {
					return 0, countErr
				}
				total += count
			} else {
				var parts []apicompat.ChatContentPart
				if err := json.Unmarshal(message.Content, &parts); err == nil {
					for _, part := range parts {
						total += openAIResponsesContentPartOverhead
						value := part.Text
						if part.ImageURL != nil {
							value += estimateOpenAIInputImageText(part.ImageURL.URL)
						}
						count, countErr := codec.Count(value)
						if countErr != nil {
							return 0, countErr
						}
						total += count
					}
				} else {
					count, countErr := estimateOpenAIRawJSONTokens(codec, message.Content)
					if countErr != nil {
						return 0, countErr
					}
					total += count
				}
			}
		}

		for _, value := range []any{message.ToolCalls, message.FunctionCall} {
			rawValue, err := marshalOpenAIUpstreamJSON(value)
			if err != nil {
				return 0, err
			}
			count, err := estimateOpenAIRawJSONTokens(codec, rawValue)
			if err != nil {
				return 0, err
			}
			total += count
		}
	}
	return total, nil
}

func estimateOpenAIRawJSONTokens(codec tokenizer.Codec, raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return 0, nil
	}
	compacted, err := compactOpenAIInputTokensJSON(raw)
	if err != nil {
		return 0, err
	}
	return codec.Count(compacted)
}

func estimateOpenAIInputTokensForInput(codec tokenizer.Codec, raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, nil
	}

	var plainText string
	if err := json.Unmarshal(raw, &plainText); err == nil {
		return codec.Count(plainText)
	}

	var items []apicompat.ResponsesInputItem
	if err := json.Unmarshal(raw, &items); err == nil {
		return estimateOpenAIInputTokensForInputItems(codec, items)
	}

	compacted, err := compactOpenAIInputTokensJSON(raw)
	if err != nil {
		return 0, err
	}
	return codec.Count(compacted)
}

func estimateOpenAIInputTokensForInputItems(codec tokenizer.Codec, items []apicompat.ResponsesInputItem) (int, error) {
	total := 0
	countText := func(text string) error {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil
		}
		n, err := codec.Count(text)
		if err != nil {
			return err
		}
		total += n
		return nil
	}

	for _, item := range items {
		total += openAIResponsesInputItemTokenOverhead
		if err := countText(item.Role); err != nil {
			return 0, err
		}
		if item.Type != "" && item.Type != "message" {
			if err := countText(item.Type); err != nil {
				return 0, err
			}
		}
		if err := countText(item.Name); err != nil {
			return 0, err
		}
		if err := countText(item.Arguments); err != nil {
			return 0, err
		}
		if err := countText(item.Output); err != nil {
			return 0, err
		}
		if err := countText(item.CallID); err != nil {
			return 0, err
		}
		if err := countText(item.ID); err != nil {
			return 0, err
		}

		if len(bytes.TrimSpace(item.Content)) == 0 {
			continue
		}

		var contentText string
		if err := json.Unmarshal(item.Content, &contentText); err == nil {
			if err := countText(contentText); err != nil {
				return 0, err
			}
			continue
		}

		var parts []apicompat.ResponsesContentPart
		if err := json.Unmarshal(item.Content, &parts); err == nil {
			for _, part := range parts {
				total += openAIResponsesContentPartOverhead
				switch part.Type {
				case "input_text", "output_text", "text":
					if err := countText(part.Text); err != nil {
						return 0, err
					}
				case "input_image":
					if err := countText(estimateOpenAIInputImageText(part.ImageURL)); err != nil {
						return 0, err
					}
				default:
					if err := countText(part.Type); err != nil {
						return 0, err
					}
				}
			}
			continue
		}

		compacted, err := compactOpenAIInputTokensJSON(item.Content)
		if err != nil {
			return 0, err
		}
		if err := countText(compacted); err != nil {
			return 0, err
		}
	}

	return total, nil
}

func estimateOpenAIInputImageText(imageURL string) string {
	trimmed := strings.TrimSpace(imageURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		if comma := strings.Index(trimmed, ","); comma > 0 {
			return trimmed[:comma]
		}
	}
	return trimmed
}

func compactOpenAIInputTokensJSON(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func openAIInputTokensCodecForModel(model string) (tokenizer.Codec, error) {
	switch openAIInputTokensEncodingForModel(model) {
	case tokenizer.Cl100kBase:
		return tokenizer.Get(tokenizer.Cl100kBase)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

func openAIInputTokensEncodingForModel(model string) tokenizer.Encoding {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "gpt-3.5"),
		(strings.HasPrefix(normalized, "gpt-4") &&
			!strings.HasPrefix(normalized, "gpt-4o") &&
			!strings.HasPrefix(normalized, "gpt-4.1")),
		strings.HasPrefix(normalized, "text-embedding-"):
		return tokenizer.Cl100kBase
	default:
		return tokenizer.O200kBase
	}
}
