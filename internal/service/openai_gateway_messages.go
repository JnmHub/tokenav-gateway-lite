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
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ForwardAsAnthropic accepts an Anthropic Messages request body, converts it
// to OpenAI Responses API format, forwards to the OpenAI upstream, and converts
// the response back to Anthropic Messages format. This enables Claude Code
// clients to access OpenAI models through the standard /v1/messages endpoint.
func (s *OpenAIGatewayService) ForwardAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	if next, err := s.ensureInitialOAuthTextFunding(ctx, c, account, body, gjson.GetBytes(body, "model").String(), defaultMappedModel); err != nil {
		return nil, err
	} else {
		ctx = next
	}
	if account.IsAnthropicProtocol() || account.IsAdaptiveAPIProtocol() {
		return s.forwardAnthropicViaNativeAnthropicEndpoint(ctx, c, account, body, defaultMappedModel)
	}

	// API Key 上游可能只实现 Chat Completions。这里必须在任何 Responses
	// 专属续链或缓存处理前分流，否则 /v1/messages 会被错误发往 /v1/responses。
	if shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		return s.forwardAnthropicViaRawChatCompletions(ctx, c, account, body, defaultMappedModel)
	}

	startTime := time.Now()

	// 1. Parse Anthropic request
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	requestedReasoningEffort := requestedReasoningEffortFromContextOrBody(ctx, body, "output_config.effort")
	anthropicDigestReq := cloneAnthropicRequestForDigest(&anthropicReq)
	originalModel := anthropicReq.Model
	// 该路径把 Responses 事件重新封装为 Anthropic 消息；在映射前固定客户端声明模型。
	beginClientResponseModelPolicy(c, ctx, account, originalModel)
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	clientStream := anthropicReq.Stream // client's original stream preference

	// 2. Model mapping
	billingModel := ResolveOpenAIForwardModelForRequest(ctx, nil, account, normalizedModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	SetOpsUpstreamModel(c, upstreamModel)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	apiKeyID := getAPIKeyIDFromContext(c)
	anthropicDigestChain := ""
	anthropicMatchedDigestChain := ""
	compatPromptCacheInjected := false
	if promptCacheKey == "" && shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
		promptCacheKey = promptCacheKeyFromAnthropicMetadataSession(&anthropicReq)
		if promptCacheKey == "" {
			promptCacheKey = deriveAnthropicCacheControlPromptCacheKey(&anthropicReq)
		}
		if promptCacheKey == "" {
			anthropicDigestChain = buildOpenAICompatAnthropicDigestChain(anthropicDigestReq)
			if reusedKey, matchedChain := s.findOpenAICompatAnthropicDigestPromptCacheKey(account, apiKeyID, anthropicDigestChain); reusedKey != "" {
				promptCacheKey = reusedKey
				anthropicMatchedDigestChain = matchedChain
			} else {
				promptCacheKey = promptCacheKeyFromAnthropicDigest(anthropicDigestChain)
			}
		}
		compatPromptCacheInjected = promptCacheKey != ""
	}
	compatReplayTrimmed := false
	compatReplayGuardEnabled := shouldAutoInjectPromptCacheKeyForCompat(upstreamModel)
	compatContinuationEnabled := openAICompatContinuationEnabled(account, upstreamModel)
	previousResponseID := ""
	if compatContinuationEnabled {
		previousResponseID = s.getOpenAICompatSessionResponseID(ctx, c, account, promptCacheKey)
	}
	compatContinuationDisabled := compatContinuationEnabled &&
		s.isOpenAICompatSessionContinuationDisabled(ctx, c, account, promptCacheKey)
	compatTurnState := ""
	// OAuth/Plus relies on session_id + x-codex-turn-state; trimming to a
	// sliding 12-message window makes the cached prefix stall at system/tools.
	// Keep full replay there so upstream prompt caching can grow turn by turn.
	if compatReplayGuardEnabled && account.Type != AccountTypeOAuth && previousResponseID == "" && !compatContinuationDisabled {
		compatReplayTrimmed = applyAnthropicCompatFullReplayGuard(&anthropicReq)
	}

	// 3. Convert Anthropic → Responses after compatibility-only replay guard.
	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic to responses: %w", err)
	}

	// Upstream always uses streaming (upstream may not support sync mode).
	// The client's original preference determines the response format.
	responsesReq.Stream = true
	isStream := true

	// 3b. Handle BetaFastMode → service_tier: "priority"
	if containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaFastMode) {
		responsesReq.ServiceTier = "priority"
	}

	responsesReq.Model = upstreamModel
	if responsesReq.Reasoning != nil {
		responsesReq.Reasoning.Effort = openAICompatAnthropicReasoningEffort(&anthropicReq, upstreamModel, responsesReq.Reasoning.Effort)
	}
	if previousResponseID != "" {
		responsesReq.PreviousResponseID = previousResponseID
		trimAnthropicCompatResponsesInputToLatestTurn(responsesReq)
	}
	if compatReplayGuardEnabled && account.Type != AccountTypeOAuth {
		appendOpenAICompatClaudeCodeTodoGuard(responsesReq)
	}

	logFields := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("normalized_model", normalizedModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", isStream),
	}
	if compatPromptCacheInjected {
		logFields = append(logFields,
			zap.Bool("compat_prompt_cache_key_injected", true),
			zap.String("compat_prompt_cache_key_sha256", hashSensitiveValueForLog(promptCacheKey)),
		)
	}
	if compatReplayTrimmed {
		logFields = append(logFields,
			zap.Bool("compat_full_replay_trimmed", true),
			zap.Int("compat_messages_after_trim", len(anthropicReq.Messages)),
		)
	}
	if previousResponseID != "" {
		logFields = append(logFields,
			zap.Bool("compat_previous_response_id_attached", true),
			zap.String("compat_previous_response_id", truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen)),
		)
	}
	if compatTurnState != "" {
		logFields = append(logFields, zap.Bool("compat_turn_state_attached", true))
	}
	logger.L().Debug("openai messages: model mapping applied", logFields...)

	// 4. Marshal Responses request body, then apply OAuth codex transform
	responsesBody, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal responses request: %w", err)
	}

	if account.Type == AccountTypeOAuth && account.Platform != PlatformGrok {
		var reqBody map[string]any
		if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
			return nil, fmt.Errorf("unmarshal for codex transform: %w", err)
		}
		codexResult := applyCodexOAuthTransformWithOptions(reqBody, codexOAuthTransformOptions{
			SkipDefaultInstructions: true,
			PreserveToolCallIDs:     true,
		})
		forcedTemplateText := ""
		if s.cfg != nil {
			forcedTemplateText = s.cfg.Gateway.ForcedCodexInstructionsTemplate
		}
		templateUpstreamModel := upstreamModel
		if codexResult.NormalizedModel != "" {
			templateUpstreamModel = codexResult.NormalizedModel
		}
		existingInstructions, _ := reqBody["instructions"].(string)
		if strings.TrimSpace(existingInstructions) == "" {
			existingInstructions = extractPromptLikeInstructionsFromInput(reqBody)
		}
		if _, err := applyForcedCodexInstructionsTemplate(reqBody, forcedTemplateText, forcedCodexInstructionsTemplateData{
			ExistingInstructions: strings.TrimSpace(existingInstructions),
			OriginalModel:        originalModel,
			NormalizedModel:      normalizedModel,
			BillingModel:         billingModel,
			UpstreamModel:        templateUpstreamModel,
		}); err != nil {
			return nil, err
		}
		ensureCodexOAuthInstructionsField(reqBody)
		if shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
			appendOpenAICompatClaudeCodeTodoGuardToRequestBody(reqBody)
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
		delete(reqBody, "prompt_cache_key")
		if shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
			compatTurnState = s.getOpenAICompatSessionTurnState(ctx, c, account, promptCacheKey)
		}
		// OAuth codex transform forces stream=true upstream, so always use
		// the streaming response handler regardless of what the client asked.
		isStream = true
		responsesBody, err = json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("remarshal after codex transform: %w", err)
		}
	}

	// API-key compatible upstreams and both Grok credential types use the
	// Responses API prompt_cache_key field. Grok OAuth must not follow the
	// ChatGPT Codex transform above, but still needs the xAI cache-routing key.
	if account.Type == AccountTypeAPIKey || account.Platform == PlatformGrok {
		if trimmedKey := strings.TrimSpace(promptCacheKey); trimmedKey != "" {
			var reqBody map[string]any
			if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
				return nil, fmt.Errorf("unmarshal for prompt cache key injection: %w", err)
			}
			if existing, ok := reqBody["prompt_cache_key"].(string); !ok || strings.TrimSpace(existing) == "" {
				reqBody["prompt_cache_key"] = trimmedKey
				updated, err := json.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("remarshal after prompt cache key injection: %w", err)
				}
				responsesBody = updated
			}
		}
	}
	if account.Platform == PlatformOpenAI {
		if policyBody, changed := ApplyOpenAIReasoningEffortPolicyFromContext(ctx, responsesBody); changed {
			responsesBody = policyBody
		}
	}

	// 4c. Apply OpenAI fast policy (may filter service_tier or block the request).
	// Mirrors the Claude anthropic-beta "fast-mode-2026-02-01" filter, but keyed
	// on the body-level service_tier field (priority/flex).
	updatedBody, priorityDecision, policyErr := s.applyOpenAIPriorityPolicyToBody(ctx, account, upstreamModel, responsesBody)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			errorType := "forbidden_error"
			if blocked.HTTPStatusCode() == http.StatusBadRequest {
				errorType = "invalid_request_error"
			}
			writeAnthropicError(c, blocked.HTTPStatusCode(), errorType, blocked.Message)
		}
		return nil, policyErr
	}
	responsesBody = updatedBody
	grokCacheIdentity := ""
	if account.Platform == PlatformGrok {
		grokCacheIdentity = resolveGrokCacheIdentity(c, responsesBody, promptCacheKey, upstreamModel)
		intentSourceBody := responsesBody
		patchedBody, patchErr := patchGrokResponsesBody(responsesBody, upstreamModel)
		if patchErr != nil {
			return nil, patchErr
		}
		responsesBody, patchErr = applyGrokResponsesCacheIdentity(patchedBody, intentSourceBody, grokCacheIdentity, account.IsGrokOAuth())
		if patchErr != nil {
			return nil, fmt.Errorf("apply grok prompt cache identity: %w", patchErr)
		}
		responsesBody, patchErr = applyGrokFreeMessagesFunctionToolCacheRoute(responsesBody, intentSourceBody, account, grokCacheIdentity)
		if patchErr != nil {
			return nil, fmt.Errorf("apply grok Free function-tool cache route: %w", patchErr)
		}
	}
	finalReasoningEffort := extractReasoningEffortAuditFromBody(responsesBody, "reasoning.effort", "reasoning_effort")

	// 5. Get access token
	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// 6. Build upstream request
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	var upstreamReq *http.Request
	if account.Platform == PlatformGrok {
		upstreamReq, err = buildGrokResponsesRequest(upstreamCtx, c, account, responsesBody, token, grokCacheIdentity, s.cfg)
	} else {
		upstreamReq, err = s.buildUpstreamRequest(upstreamCtx, c, account, responsesBody, token, isStream, promptCacheKey, false)
	}
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	// Override session_id with a deterministic UUID derived from the isolated
	// session key, ensuring different API keys produce different upstream sessions.
	if promptCacheKey != "" && account.Platform != PlatformGrok {
		isolatedSessionID := generateSessionUUID(isolateOpenAISessionID(apiKeyID, promptCacheKey))
		upstreamReq.Header.Set("session_id", isolatedSessionID)
		if upstreamReq.Header.Get("conversation_id") != "" {
			upstreamReq.Header.Set("conversation_id", isolatedSessionID)
		}
	}
	if account.Type == AccountTypeOAuth && account.Platform != PlatformGrok {
		// Anthropic Messages compatibility uses the ChatGPT Codex SSE endpoint.
		// Match airgate-openai's request shape: the SSE endpoint does not need
		// the Responses experimental beta header, and forcing originator can make
		// ChatGPT select a different internal continuation path.
		upstreamReq.Header.Del("OpenAI-Beta")
		upstreamReq.Header.Del("originator")
	}
	if account.Type == AccountTypeOAuth && account.Platform != PlatformGrok && promptCacheKey != "" && strings.TrimSpace(c.GetHeader("conversation_id")) == "" {
		upstreamReq.Header.Del("conversation_id")
	}
	if compatTurnState != "" && upstreamReq.Header.Get("x-codex-turn-state") == "" {
		upstreamReq.Header.Set("x-codex-turn-state", compatTurnState)
	}
	if err := s.applyOpenAICodexTicket(ctx, account, upstreamModel, upstreamReq.Header); err != nil {
		return nil, err
	}

	// 7. Send request
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	// Grok 账号或缓存身份变化时，历史 encrypted_content 可能无法解密。
	// 仅在首个 400 且尚未输出时剥离一次密文重试，绝不重放成功请求。
	var resp *http.Response
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if account.Platform != PlatformGrok {
				break
			}
			upstreamCtxRetry, releaseRetry := detachUpstreamContext(ctx)
			upstreamReq, err = buildGrokResponsesRequest(upstreamCtxRetry, c, account, responsesBody, token, grokCacheIdentity, s.cfg)
			releaseRetry()
			if err != nil {
				return nil, fmt.Errorf("build grok retry request: %w", err)
			}
		}
		resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
		if err != nil {
			// 传输层错误交回统一 failover（失败切换）链路，当前服务不能提前写死 502，
			// 否则 handler 无法选择同分组的下一个账号。
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
		}
		if account.Platform != PlatformGrok || attempt > 0 || resp.StatusCode != http.StatusBadRequest {
			break
		}
		respBody := s.readUpstreamErrorBody(resp)
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		// 换号后的 xAI 有时只返回不透明 400；只有请求中确实存在可剥离密文时才恢复。
		shouldStrip := isGrokInvalidEncryptedContentResponse(resp.StatusCode, respBody) ||
			requestHasGrokEncryptedReasoning(responsesBody)
		if !shouldStrip {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		retryBody, changed, trimErr := trimGrokInvalidEncryptedContentRetryBody(responsesBody)
		if trimErr != nil {
			return nil, fmt.Errorf("prepare Grok invalid encrypted_content retry: %w", trimErr)
		}
		if !changed {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		responsesBody = retryBody
		logger.L().Info("openai messages: retrying after stripping invalid Grok encrypted_content",
			zap.Int64("account_id", account.ID),
			zap.Bool("cache_identity_present", strings.TrimSpace(grokCacheIdentity) != ""),
			zap.String("upstream_error_preview", truncateOpenAIWSLogValue(string(respBody), 240)),
		)
	}
	defer func() { _ = resp.Body.Close() }()

	// 8. Handle error response with failover
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if account.IsOpenAIAgentIdentity() && !agentIdentityTaskRecoveryWasTried(ctx) && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, respBody) {
			expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
			if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
				return nil, fmt.Errorf("agent identity task recovery failed: %w", recoverErr)
			}
			return s.ForwardAsAnthropic(
				markAgentIdentityTaskRecoveryTried(ctx), c, account, body, promptCacheKey, defaultMappedModel,
			)
		}

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		if shouldRetryGrokUnauthorized(ctx, account, resp.StatusCode) {
			if refreshedToken, refreshErr := s.refreshGrokAfterUnauthorizedForRequest(ctx, c, account); refreshErr == nil {
				_ = resp.Body.Close()
				return s.ForwardAsAnthropic(
					contextWithGrokUnauthorizedRetry(ctx, refreshedToken),
					c,
					account,
					body,
					promptCacheKey,
					defaultMappedModel,
				)
			} else {
				return nil, refreshErr
			}
		}
		if previousResponseID != "" && (isOpenAICompatPreviousResponseNotFound(resp.StatusCode, upstreamMsg, respBody) || isOpenAICompatPreviousResponseUnsupported(resp.StatusCode, upstreamMsg, respBody)) {
			if isOpenAICompatPreviousResponseUnsupported(resp.StatusCode, upstreamMsg, respBody) {
				s.disableOpenAICompatSessionContinuation(ctx, c, account, promptCacheKey)
			} else {
				s.deleteOpenAICompatSessionResponseID(ctx, c, account, promptCacheKey)
			}
			logger.L().Info("openai messages: previous_response_id unavailable, retrying without continuation",
				zap.Int64("account_id", account.ID),
				zap.String("previous_response_id", truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen)),
				zap.String("upstream_model", upstreamModel),
			)
			return s.ForwardAsAnthropic(ctx, c, account, body, promptCacheKey, defaultMappedModel)
		}
		// 若第一次恢复仍失败，下一账号不能继续携带旧账号的 Claude thinking 签名。
		// 请求级标记保证整个 failover（失败切换）链只剥离一次，避免循环。
		if account.Platform == PlatformGrok &&
			isGrokInvalidEncryptedContentResponse(resp.StatusCode, respBody) &&
			!grokEncryptedContentStripRetried(ctx) {
			if strippedBody, ok := stripAnthropicThinkingSignatures(body); ok {
				logger.L().Info("openai messages: stripping thinking signatures for Grok failover retry",
					zap.Int64("account_id", account.ID),
				)
				return s.ForwardAsAnthropic(markGrokEncryptedContentStripRetried(ctx), c, account, strippedBody, promptCacheKey, defaultMappedModel)
			}
		}
		if account.Platform == PlatformGrok {
			s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
		}
		shouldFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
		if account.Platform == PlatformGrok {
			shouldFailover = s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody)
		}
		if failoverErr := s.buildOpenAIHTTPFailoverError(ctx, c, account, resp.StatusCode, resp.Header, respBody, upstreamMsg, upstreamModel, shouldFailover); failoverErr != nil {
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
		// Non-failover error: return Anthropic-formatted error to client
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	if account.Type == AccountTypeOAuth && account.Platform != PlatformGrok && promptCacheKey != "" {
		if turnState := strings.TrimSpace(resp.Header.Get("x-codex-turn-state")); turnState != "" {
			s.bindOpenAICompatSessionTurnState(ctx, c, account, promptCacheKey, turnState)
		}
	}

	// 9. Handle normal response
	// Upstream is always streaming; choose response format based on client preference.
	var result *OpenAIForwardResult
	var handleErr error
	if clientStream {
		result, handleErr = s.handleAnthropicStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	} else {
		// Client wants JSON: buffer the streaming response and assemble a JSON reply.
		result, handleErr = s.handleAnthropicBufferedStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	}

	// cyber_policy：标记已设、error 已按 Anthropic 格式发给客户端。丢弃 result、返回哨兵，
	// 使 handler 落入 tokens=0 免费用量行（对齐 /v1/responses），不计费、不 failover。
	if GetOpsCyberPolicy(c) != nil {
		if handleErr == nil {
			handleErr = errOpenAICyberPolicyForwarded
		}
		return nil, handleErr
	}

	// 计费和日志必须使用最终发往上游的层级，同时保留客户原始请求值。
	if result != nil {
		setOpenAIResultReasoningAudit(result, requestedReasoningEffort, finalReasoningEffort)
	}
	if handleErr == nil && result != nil {
		if compatContinuationEnabled && promptCacheKey != "" && result.ResponseID != "" {
			s.bindOpenAICompatSessionResponseID(ctx, c, account, promptCacheKey, result.ResponseID)
		}
		if promptCacheKey != "" && anthropicDigestChain != "" {
			s.bindOpenAICompatAnthropicDigestPromptCacheKey(account, apiKeyID, anthropicDigestChain, promptCacheKey, anthropicMatchedDigestChain)
		}
		applyOpenAIPriorityDecisionToResult(result, priorityDecision, nil)
	}

	// xAI returns useful rate-limit headers for both OAuth and API-key
	// accounts. Codex rate-limit snapshots remain OAuth-only because they are
	// tied to ChatGPT subscription credentials rather than API keys.
	if handleErr == nil {
		if account.Platform == PlatformGrok {
			s.updateGrokUsageSnapshot(ctx, account, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode), upstreamModel)
		} else if account.Type == AccountTypeOAuth {
			if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
				s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
			}
		}
	}

	return result, handleErr
}

func ensureCodexOAuthInstructionsField(reqBody map[string]any) {
	if reqBody == nil {
		return
	}
	if value, ok := reqBody["instructions"]; !ok || value == nil {
		reqBody["instructions"] = ""
		return
	}
	if _, ok := reqBody["instructions"].(string); !ok {
		reqBody["instructions"] = ""
	}
}

// handleAnthropicErrorResponse reads an upstream error and returns it in
// Anthropic error format.
func (s *OpenAIGatewayService) handleAnthropicErrorResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestedModel ...string,
) (*OpenAIForwardResult, error) {
	return s.handleCompatErrorResponse(resp, c, account, writeAnthropicError, requestedModel...)
}

// handleAnthropicBufferedStreamingResponse reads all Responses SSE events from
// the upstream streaming response, finds the terminal event (response.completed
// / response.incomplete / response.failed), converts the complete response to
// Anthropic Messages JSON format, and writes it to the client.
// This is used when the client requested stream=false but the upstream is always
// streaming.
func (s *OpenAIGatewayService) handleAnthropicBufferedStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	finalResponse, usage, acc, err := s.readOpenAICompatBufferedTerminal(resp, "openai messages buffered", requestID)
	if err != nil {
		var readErr *openAICompatBufferedReadError
		if errors.As(err, &readErr) && readErr != nil {
			return nil, readErr.cause
		}
		return nil, err
	}

	if finalResponse == nil {
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream stream ended without a terminal response event")
		return nil, fmt.Errorf("upstream stream ended without terminal event")
	}

	if strings.TrimSpace(finalResponse.Status) == "failed" {
		payload, _ := json.Marshal(gin.H{"type": "response.failed", "response": finalResponse})
		if hit, code, msg := detectOpenAICyberPolicy(payload); hit {
			MarkOpsCyberPolicy(c, CyberPolicyMark{
				Code:           code,
				Message:        msg,
				Body:           truncateString(string(payload), 4096),
				UpstreamStatus: http.StatusOK,
				UpstreamInTok:  usage.InputTokens,
				UpstreamOutTok: usage.OutputTokens,
			})
			clientMsg := msg
			if clientMsg == "" {
				clientMsg = "Request blocked by upstream cyber-security policy"
			}
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
			return nil, fmt.Errorf("openai cyber_policy: %s", msg)
		}
		message := openAICompatFailedResponseMessage(finalResponse)
		if openAIStreamFailedEventShouldFailover(payload, message) {
			return nil, s.newOpenAIStreamFailoverError(c, account, false, requestID, payload, message, resp.Header)
		}
		message = s.recordOpenAIStreamUpstreamError(c, account, false, requestID, "http_error", payload, message)
		if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(
			c, account.Platform, payload, message,
		); matched {
			if errMsg == "" {
				errMsg = message
			}
			MarkResponseCommitted(c)
			writeAnthropicError(c, status, errType, errMsg)
			return nil, fmt.Errorf("upstream response failed (passthrough): %s", errMsg)
		}
		writeAnthropicError(c, http.StatusBadGateway, "api_error", message)
		return nil, fmt.Errorf("upstream response failed: %s", message)
	}

	// When the terminal event has an empty output array, reconstruct from
	// accumulated delta events so the client receives the full content.
	acc.SupplementResponseOutput(finalResponse)

	responseModel := clientResponseModelForSyntheticOutput(c, account, originalModel, upstreamModel)
	anthropicResp := apicompat.ResponsesToAnthropic(finalResponse, responseModel)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.JSON(http.StatusOK, anthropicResp)

	return &OpenAIForwardResult{
		RequestID:     requestID,
		ResponseID:    finalResponse.ID,
		Usage:         usage,
		Model:         originalModel,
		BillingModel:  billingModel,
		UpstreamModel: upstreamModel,
		Stream:        false,
		Duration:      time.Since(startTime),
	}, nil
}

func isOpenAICompatResponsesTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) recordOpenAIMessagesStreamUpstreamError(c *gin.Context, account *Account, upstreamRequestID, kind, message string) {
	if c == nil {
		return
	}
	message = sanitizeUpstreamErrorMessage(message)
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	event := OpsUpstreamErrorEvent{
		Platform:           PlatformOpenAI,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               kind,
		Message:            message,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func isOpenAICompatDoneSentinelLine(line string) bool {
	payload, ok := extractOpenAISSEDataLine(line)
	return ok && strings.TrimSpace(payload) == "[DONE]"
}

// openAICompatBufferedReadError 只标记错误发生在上游响应体读取阶段；
// 具体端点自行决定能否安全重放，避免共享读取器扩大故障转移范围。
type openAICompatBufferedReadError struct {
	cause error
}

func (e *openAICompatBufferedReadError) Error() string { return e.cause.Error() }
func (e *openAICompatBufferedReadError) Unwrap() error { return e.cause }

// errBufferedReadStopped 是「调用方已经放弃响应体」时读端自己用的哨兵错误：
// 它只可能在 readOpenAICompatBufferedTerminal 内部出现，不会透传给上层。
var errBufferedReadStopped = errors.New("upstream response body read stopped by caller")

// bufferedReadCloseGracePeriod 是调用方放弃响应体后，留给读行 goroutine 自然读完响应体的宽限期。
// 正常情况下（上游发完终止事件就结束响应）读端会立刻读到 EOF、连接正常归还复用池；
// 只有上游发完终止事件却一直不结束响应时，才会走到兜底强制关闭。
const bufferedReadCloseGracePeriod = 2 * time.Second

// bufferedReadCloseGuard 把「调用方 Close 响应体」降级成一次性停止信号，并把响应体的读与关
// 全部收归同一个 goroutine（readOpenAICompatBufferedTerminal 的读行 goroutine）。
//
// 为什么必须这样（本机复现 + 标准库两端证据）：
//   - net/http 的 bodyEOFSignal 对每个响应体有两条收尾路径：读端读到 io.EOF 走 fn
//     （transport.go:2571-2580，内部 `<-eofc`），提前 Close 走 earlyCloseFn
//     （transport.go:2564-2569，同样 `<-eofc`）；而每条连接的 readLoop 每轮只发出一份
//     eofc 信用（transport.go:2607 / 2616）。
//   - 一旦「读端仍在读」与「调用方 Close」并发，两个等待者会抢同一份信用：输掉的那个永久
//     挂在 `<-eofc` 上，且此刻仍持有 bodyEOFSignal.mu，于是调用方的 Close 又卡在 `es.mu`
//     上 —— 请求永不返回（本机探针 8 并发 × 150 次，第 6 个 worker 第 14 次即挂死）。
//   - 连接被复用后，残留等待者还会抢走后续请求的 eofc 信用，把偶发挂死扩散成压测中
//     3–5% 的请求超时。
//
// 因此：读行 goroutine 是唯一的读写者（退出时自己关闭传输层响应体），调用方只发信号并立即
// 返回，绝不与读端并发 Close 同一个响应体。
type bufferedReadCloseGuard struct {
	raw  io.ReadCloser
	done chan struct{}
	once sync.Once
}

// Read 在调用方已经放弃后立刻返回哨兵错误，避免读端再去碰传输层（尤其是连接已被复用的情况）。
func (g *bufferedReadCloseGuard) Read(p []byte) (int, error) {
	select {
	case <-g.done:
		return 0, errBufferedReadStopped
	default:
	}
	return g.raw.Read(p)
}

// Close 只发停止信号并立即返回：绝不等待传输层的 bodyEOFSignal 收尾，从根上避免上述死锁。
func (g *bufferedReadCloseGuard) Close() error {
	g.once.Do(func() { close(g.done) })
	return nil
}

func (s *OpenAIGatewayService) readOpenAICompatBufferedTerminal(
	resp *http.Response,
	logPrefix string,
	requestID string,
) (*apicompat.ResponsesResponse, OpenAIUsage, *apicompat.BufferedResponseAccumulator, error) {
	acc := apicompat.NewBufferedResponseAccumulator()
	var usage OpenAIUsage
	if resp == nil || resp.Body == nil {
		return nil, usage, acc, errors.New("upstream response body is nil")
	}

	// 响应体所有权收归下面的读行 goroutine 一家：调用方（含转发层的 defer resp.Body.Close()）
	// 只通过 guard 发停止信号，绝不在调用方 goroutine 上与读端并发 Close 同一个响应体。
	rawBody := resp.Body
	guard := &bufferedReadCloseGuard{raw: rawBody, done: make(chan struct{})}
	resp.Body = guard

	scanner := bufio.NewScanner(guard)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var timeoutCh <-chan time.Time
	var timeoutTimer *time.Timer
	resetTimeout := func() {
		if streamInterval <= 0 {
			return
		}
		if timeoutTimer == nil {
			timeoutTimer = time.NewTimer(streamInterval)
			timeoutCh = timeoutTimer.C
			return
		}
		if !timeoutTimer.Stop() {
			select {
			case <-timeoutTimer.C:
			default:
			}
		}
		timeoutTimer.Reset(streamInterval)
	}
	stopTimeout := func() {
		if timeoutTimer == nil {
			return
		}
		if !timeoutTimer.Stop() {
			select {
			case <-timeoutTimer.C:
			default:
			}
		}
	}
	resetTimeout()
	defer stopTimeout()

	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	readerDone := make(chan struct{})
	go func() {
		// 读行 goroutine 是响应体的唯一读写者：退出时由它关闭传输层响应体，
		// 于是 net/http 的 bodyEOFSignal 只会遇到「读端 EOF」或「读端提前关闭」其中一条路径，
		// 不会再出现两个等待者抢同一条连接 eofc 信用的死锁组合（详见 bufferedReadCloseGuard 注释）。
		defer func() {
			_ = rawBody.Close() // 唯一读写者负责关闭响应体
			close(readerDone)   // 告知兜底 goroutine：不必再强制关闭
		}()
		defer close(events)
		for scanner.Scan() {
			select {
			case events <- scanEvent{line: scanner.Text()}:
			case <-guard.done:
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, errBufferedReadStopped) {
			select {
			case events <- scanEvent{err: err}:
			case <-guard.done:
			}
		}
	}()
	// 兜底释放：调用方已经放弃、读行 goroutine 却仍卡在传输层读取（上游发完终止事件却不结束响应、
	// 或停滞超时后上游仍不关连接）时，由独立 goroutine 在宽限期后强制关闭响应体，
	// 避免连接被永久占住。它不在任何请求 goroutine 上等待，因此不会让请求挂死。
	go func() {
		select {
		case <-guard.done:
		case <-readerDone:
			return
		}
		select {
		case <-readerDone:
			return
		case <-time.After(bufferedReadCloseGracePeriod):
		}
		logger.L().Warn(logPrefix+": upstream response body still open after caller stopped reading, force closing",
			zap.String("request_id", requestID),
			zap.Duration("grace", bufferedReadCloseGracePeriod),
		)
		_ = rawBody.Close()
	}()
	// 调用方（含转发层的 defer resp.Body.Close()）只发停止信号：绝不在调用方 goroutine 上
	// 与读端并发 Close 同一个响应体 —— 那是死锁的另一半。
	defer func() { _ = guard.Close() }()

	var parser openAICompatSSEFrameParser
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if frame, ok := parser.Finish(); ok {
					payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)
					var event apicompat.ResponsesStreamEvent
					if err := json.Unmarshal([]byte(payload), &event); err == nil {
						acc.ProcessEvent(&event)
						if isOpenAICompatResponsesTerminalEvent(event.Type) && event.Response != nil {
							if event.Usage != nil {
								usage = copyOpenAIUsageFromResponsesUsage(event.Usage)
								if event.Response.Usage == nil {
									event.Response.Usage = event.Usage
								}
							}
							if event.Response.Usage != nil {
								usage = copyOpenAIUsageFromResponsesUsage(event.Response.Usage)
							}
							return event.Response, usage, acc, nil
						}
					}
				}
				return nil, usage, acc, nil
			}
			resetTimeout()
			if ev.err != nil {
				if !errors.Is(ev.err, context.Canceled) && !errors.Is(ev.err, context.DeadlineExceeded) {
					logger.L().Warn(logPrefix+": read error",
						zap.Error(ev.err),
						zap.String("request_id", requestID),
					)
				}
				return nil, usage, acc, &openAICompatBufferedReadError{cause: ev.err}
			}

			if isOpenAICompatDoneSentinelLine(ev.line) {
				return nil, usage, acc, nil
			}
			frame, ok := parser.AddLine(ev.line)
			if !ok {
				continue
			}
			payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)

			var event apicompat.ResponsesStreamEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				logger.L().Warn(logPrefix+": failed to parse event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}

			acc.ProcessEvent(&event)

			if isOpenAICompatResponsesTerminalEvent(event.Type) && event.Response != nil {
				if event.Usage != nil {
					usage = copyOpenAIUsageFromResponsesUsage(event.Usage)
					if event.Response.Usage == nil {
						event.Response.Usage = event.Usage
					}
				}
				if event.Response.Usage != nil {
					usage = copyOpenAIUsageFromResponsesUsage(event.Response.Usage)
				}
				return event.Response, usage, acc, nil
			}

		case <-timeoutCh:
			// 只发停止信号（非阻塞）；响应体由读行 goroutine / 兜底 goroutine 关闭，见 guard 注释。
			_ = guard.Close()
			logger.L().Warn(logPrefix+": data interval timeout",
				zap.String("request_id", requestID),
				zap.Duration("interval", streamInterval),
			)
			return nil, usage, acc, fmt.Errorf("stream data interval timeout")
		}
	}
}

// handleAnthropicStreamingResponse reads Responses SSE events from upstream,
// converts each to Anthropic SSE events, and writes them to the client.
// When StreamKeepaliveInterval is configured, it uses a goroutine + channel
// pattern to send Anthropic ping events during periods of upstream silence,
// preventing proxy/client timeout disconnections.
func (s *OpenAIGatewayService) handleAnthropicStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

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

	state := apicompat.NewResponsesEventToAnthropicState()
	state.Model = clientResponseModelForSyntheticOutput(c, account, originalModel, upstreamModel)
	var usage OpenAIUsage
	responseID := ""
	var firstTokenMs *int
	firstChunk := true
	clientDisconnected := false
	clientOutputStarted := false
	var streamFailoverErr error
	var streamNonFailoverErr error

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// resultWithUsage builds the final result snapshot.
	resultWithUsage := func() *OpenAIForwardResult {
		return &OpenAIForwardResult{
			RequestID:        requestID,
			ResponseID:       responseID,
			Usage:            usage,
			Model:            originalModel,
			BillingModel:     billingModel,
			UpstreamModel:    upstreamModel,
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     firstTokenMs,
			ClientDisconnect: clientDisconnected,
		}
	}

	// processDataLine handles a single "data: ..." SSE line from upstream.
	processDataLine := func(payload string) bool {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			logger.L().Warn("openai messages stream: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			return false
		}

		isTerminalEvent := isOpenAICompatResponsesTerminalEvent(event.Type)
		if isTerminalEvent {
			if event.Response != nil {
				if id := strings.TrimSpace(event.Response.ID); id != "" {
					responseID = id
				}
				if event.Response.Usage != nil {
					usage = copyOpenAIUsageFromResponsesUsage(event.Response.Usage)
				}
			}
			if event.Usage != nil {
				usage = copyOpenAIUsageFromResponsesUsage(event.Usage)
			}
			// cyber_policy 致命不可重试：标记供 handler 事后记录；以 Anthropic SSE error 事件
			// 回写让客户端感知并停止重试（F4），丢弃后续转换输出。
			if strings.TrimSpace(event.Type) == "response.failed" {
				payloadBytes := []byte(payload)
				if hit, code, msg := detectOpenAICyberPolicy(payloadBytes); hit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(payload, 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
					if !clientDisconnected {
						writeStreamHeaders()
						clientMsg := msg
						if clientMsg == "" {
							clientMsg = "Request blocked by upstream cyber-security policy"
						}
						if _, err := fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE("invalid_request_error", clientMsg)); err == nil {
							c.Writer.Flush()
						}
						clientDisconnected = true
					}
					return true
				}
				message := extractOpenAISSEErrorMessage(payloadBytes)
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) && openAIStreamFailedEventShouldFailover(payloadBytes, message) {
					streamFailoverErr = s.newOpenAIStreamFailoverError(c, account, false, requestID, payloadBytes, message, resp.Header)
					return true
				}
				message = s.recordOpenAIStreamUpstreamError(c, account, false, requestID, "http_error", payloadBytes, message)
				errStatus, errType, errMsg := http.StatusBadGateway, "api_error", message
				if status, matchedType, matchedMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(
					c, account.Platform, payloadBytes, message,
				); matched {
					if matchedMsg == "" {
						matchedMsg = errMsg
					}
					errStatus, errType, errMsg = status, matchedType, matchedMsg
					MarkResponseCommitted(c)
				}
				if !clientDisconnected {
					if !clientOutputStarted {
						writeAnthropicError(c, errStatus, errType, errMsg)
						clientOutputStarted = true
					} else {
						writeStreamHeaders()
						if _, err := fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE(errType, errMsg)); err == nil {
							c.Writer.Flush()
						} else {
							clientDisconnected = true
						}
					}
				}
				streamNonFailoverErr = fmt.Errorf("upstream response failed: %s", errMsg)
				return true
			}
		}

		// Convert to Anthropic events
		events := apicompat.ResponsesEventToAnthropicEvents(&event, state)
		if !clientDisconnected {
			for _, evt := range events {
				sse, err := apicompat.ResponsesAnthropicEventToSSE(evt)
				if err != nil {
					logger.L().Warn("openai messages stream: failed to marshal event",
						zap.Error(err),
						zap.String("request_id", requestID),
					)
					continue
				}
				writeStreamHeaders()
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai messages stream: client disconnected, continuing to drain upstream for billing",
						zap.String("request_id", requestID),
					)
					break
				}
				clientOutputStarted = true
			}
		}
		if len(events) > 0 && !clientDisconnected {
			c.Writer.Flush()
		}
		return isTerminalEvent
	}

	// finalizeStream sends any remaining Anthropic events and returns the result.
	finalizeStream := func() (*OpenAIForwardResult, error) {
		if streamFailoverErr != nil {
			return resultWithUsage(), streamFailoverErr
		}
		if streamNonFailoverErr != nil {
			return resultWithUsage(), streamNonFailoverErr
		}
		if finalEvents := apicompat.FinalizeResponsesAnthropicStream(state); len(finalEvents) > 0 && !clientDisconnected {
			for _, evt := range finalEvents {
				sse, err := apicompat.ResponsesAnthropicEventToSSE(evt)
				if err != nil {
					continue
				}
				writeStreamHeaders()
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai messages stream: client disconnected during final flush",
						zap.String("request_id", requestID),
					)
					break
				}
				clientOutputStarted = true
			}
			if !clientDisconnected {
				c.Writer.Flush()
			}
		}
		return resultWithUsage(), nil
	}

	// handleScanErr logs scanner errors if meaningful.
	handleScanErr := func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai messages stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	missingTerminalErr := func() (*OpenAIForwardResult, error) {
		result := resultWithUsage()
		if clientDisconnected {
			return result, fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		message := "OpenAI messages stream ended before a terminal event"
		if !clientOutputStarted {
			return result, s.newOpenAIStreamFailoverError(c, account, false, requestID, nil, message)
		}
		s.recordOpenAIMessagesStreamUpstreamError(c, account, requestID, "stream_missing_terminal", message)
		return result, fmt.Errorf("stream usage incomplete: missing terminal event")
	}
	processFrame := func(frame openAICompatSSEFrame) bool {
		payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)
		return processDataLine(payload)
	}

	// ── Determine keepalive interval ──
	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}

	// ── No keepalive: fast synchronous path (no goroutine overhead) ──
	if streamInterval <= 0 && keepaliveInterval <= 0 {
		var parser openAICompatSSEFrameParser
		for scanner.Scan() {
			line := scanner.Text()
			if isOpenAICompatDoneSentinelLine(line) {
				return missingTerminalErr()
			}
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		if err := scanner.Err(); err != nil {
			handleScanErr(err)
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
		}
		if frame, ok := parser.Finish(); ok {
			if strings.TrimSpace(frame.Data) == "[DONE]" {
				return missingTerminalErr()
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		return missingTerminalErr()
	}

	// ── With keepalive: goroutine + channel + select ──
	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	go func() {
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}()
	defer close(done)

	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()
	var parser openAICompatSSEFrameParser

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// Upstream closed
				if frame, ok := parser.Finish(); ok {
					if strings.TrimSpace(frame.Data) == "[DONE]" {
						return missingTerminalErr()
					}
					if processFrame(frame) {
						return finalizeStream()
					}
				}
				return missingTerminalErr()
			}
			if ev.err != nil {
				handleScanErr(ev.err)
				return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", ev.err)
			}
			lastDataAt = time.Now()
			line := ev.line
			if isOpenAICompatDoneSentinelLine(line) {
				return missingTerminalErr()
			}
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if processFrame(frame) {
				return finalizeStream()
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.L().Warn("openai messages stream: data interval timeout",
				zap.String("request_id", requestID),
				zap.String("model", originalModel),
				zap.Duration("interval", streamInterval),
			)
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Send Anthropic-format ping event
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n"); err != nil {
				// Client disconnected
				logger.L().Info("openai messages stream: client disconnected during keepalive",
					zap.String("request_id", requestID),
				)
				clientDisconnected = true
				continue
			}
			clientOutputStarted = true
			c.Writer.Flush()
		}
	}
}

// writeAnthropicError writes an error response in Anthropic Messages API format.
func writeAnthropicError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// buildAnthropicStreamErrorSSE builds one Anthropic SSE `error` event so a
// streaming response can terminate with a visible error (e.g. upstream
// cyber_policy) and programmatic clients stop retrying.
// Marshal 失败的兜底仅保留固定提示。
func buildAnthropicStreamErrorSSE(errType, message string) string {
	payload, err := json.Marshal(gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	if err != nil {
		return "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"" + errType + "\",\"message\":\"upstream error\"}}\n\n"
	}
	return "event: error\ndata: " + string(payload) + "\n\n"
}

func copyOpenAIUsageFromResponsesUsage(usage *apicompat.ResponsesUsage) OpenAIUsage {
	if usage == nil {
		return OpenAIUsage{}
	}
	result := OpenAIUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}
	if usage.InputTokensDetails != nil {
		result.CacheReadInputTokens = usage.InputTokensDetails.CachedTokens
		result.CacheCreationInputTokens = usage.InputTokensDetails.CacheWriteTokens
	}
	return result
}
