package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GrokRealtime exposes xAI's native Voice Realtime WebSocket.
// Only Grok-platform API keys may use this endpoint.
func (h *OpenAIGatewayHandler) GrokRealtime(c *gin.Context) {
	if c == nil || c.Request == nil || !isOpenAIWSUpgradeRequest(c.Request) {
		h.errorResponse(c, http.StatusUpgradeRequired, "invalid_request_error", "WebSocket upgrade required (Upgrade: websocket)")
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Realtime API is not supported for this platform")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	if !h.ensureResponsesDependencies(c, nil) {
		return
	}
	var streamStarted bool
	reqLog := requestLogger(c, "handler.openai_gateway.grok_realtime")
	userRelease, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userRelease != nil {
		defer userRelease()
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	selection, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
		c.Request.Context(),
		apiKey.GroupID,
		"",
		"",
		"grok-4.5",
		nil,
		service.OpenAIUpstreamTransportHTTPSSE,
		// Grok only advertises chat_completions + media capabilities on HEAD.
		service.OpenAIEndpointCapabilityChatCompletions,
		false,
		service.PlatformGrok,
	)
	if err != nil || selection == nil || selection.Account == nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available Grok accounts")
		return
	}

	release, slotStatus := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &streamStarted, reqLog)
	if !slotStatus {
		return
	}
	defer release()

	token, _, err := h.gatewayService.GetRequestCredential(c.Request.Context(), c, selection.Account)
	if err != nil {
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Grok credential unavailable")
		return
	}

	conn, err := coderws.Accept(c.Writer, c.Request, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
	if err != nil {
		return
	}
	defer func() { _ = conn.CloseNow() }()

	model := c.Query("model")
	if strings.TrimSpace(model) == "" {
		model = "grok-voice-latest"
	}
	model = strings.TrimSpace(model)
	// Realtime billing must use the same final wire model as the WebSocket
	// dial. Keep the client-facing model separate so an account/group mapping
	// cannot inflate or understate the account procurement cost.
	upstreamModel := service.MappedAccountModelForRequest(c.Request.Context(), apiKey.GroupID, selection.Account, model)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = model
	}
	service.SetOpsUpstreamModel(c, upstreamModel)
	// Realtime is event-scoped and the account is already bound. Never reuse
	// blocked history or reroute this live connection to another account/group.
	c.Set(contentModerationLocalPolicyOnlyKey, true)
	c.Set(contentModerationDisableHistoryIsolationKey, true)
	c.Set("content_moderation_disable_keyword_reroute", true)
	c.Set(contentModerationBoundAccountAllowsBlockedKeywordsKey, selection.Account.AcceptsBlockedKeywords(apiKey.Group.AllowBlockedKeywordRequests))
	frameHook := func(_ context.Context, payload []byte) error {
		auditBody := extractGrokRealtimeAuditBody(payload)
		if len(auditBody) == 0 {
			return nil
		}
		decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, model, auditBody)
		if decision == nil || !decision.Blocked {
			return nil
		}
		errorEvent, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{
			"type": contentModerationErrorCode(decision), "code": "content_policy_violation", "message": decision.Message,
		}})
		writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = conn.Write(writeCtx, coderws.MessageText, errorEvent)
		cancel()
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, service.TruncateGatewayClientErrorReason(decision.Message, 120), nil)
	}
	started := time.Now()
	audioObserved, proxyErr := h.gatewayService.ProxyGrokRealtime(c.Request.Context(), c, conn, selection.Account, token, upstreamModel, frameHook)
	elapsed := time.Since(started)
	if proxyErr != nil {
		var closeErr *service.OpenAIWSClientCloseError
		if errors.As(proxyErr, &closeErr) {
			reqLog.Info("grok_realtime.policy_closed", zap.Int("status", int(closeErr.StatusCode())))
			_ = conn.Close(closeErr.StatusCode(), closeErr.Reason())
		} else if !isExpectedGrokRealtimeClose(proxyErr) {
			reqLog.Info("grok_realtime.proxy_failed", zap.Error(proxyErr))
			_ = conn.Close(coderws.StatusInternalError, "upstream realtime websocket failed")
			return
		}
	}
	if result := grokRealtimeBillingResult(model, upstreamModel, elapsed, audioObserved); result != nil {
		h.recordGrokVoiceUsage(c, apiKey, selection.Account, subscription, "realtime", nil, result)
	}
}

func grokRealtimeBillingResult(model, upstreamModel string, elapsed time.Duration, audioObserved bool) *service.OpenAIForwardResult {
	if !audioObserved || elapsed <= 0 {
		return nil
	}
	model = strings.TrimSpace(model)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if upstreamModel == "" {
		upstreamModel = model
	}
	return &service.OpenAIForwardResult{
		RequestID:     service.StableGrokRealtimeBillingRequestID(""),
		Model:         model,
		UpstreamModel: upstreamModel,
		Duration:      elapsed,
		AudioUsage:    &service.AudioUsage{Mode: "realtime", DurationOrUnits: elapsed.Minutes()},
	}
}

func isExpectedGrokRealtimeClose(err error) bool {
	if err == nil {
		return true
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusNormalClosure, coderws.StatusGoingAway,
		coderws.StatusNoStatusRcvd, coderws.StatusAbnormalClosure:
		return true
	default:
		return false
	}
}

// GrokVoice handles xAI Voice HTTP endpoints. endpoint is "tts", "stt", or "custom-voices".
func (h *OpenAIGatewayHandler) GrokVoice(c *gin.Context, endpoint string) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformGrok {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Voice API is not supported for this platform")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	if !h.ensureResponsesDependencies(c, nil) {
		return
	}
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	body, err := readGrokVoiceGatewayBody(c)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if endpoint == "stt" {
		c.Set(contentModerationLocalPolicyOnlyKey, true)
		c.Set(contentModerationDisableHistoryIsolationKey, true)
		if auditBody := extractGrokSTTRequestAuditBody(body, c.GetHeader("Content-Type")); len(auditBody) > 0 {
			reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
			if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "stt", auditBody); decision != nil && decision.Blocked {
				c.JSON(contentModerationStatus(decision), gin.H{"error": gin.H{"type": contentModerationErrorCode(decision), "message": decision.Message}})
				return
			}
		}
	}
	if endpoint == "tts" {
		reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
		// TTS bodies use {"input":"..."} (and variants). Normalize to chat messages so
		// content moderation extractors see the spoken text.
		auditBody := body
		if input := extractGrokTTSInputText(body); input != "" {
			if b, err := json.Marshal(map[string]any{
				"messages": []map[string]any{{"role": "user", "content": input}},
			}); err == nil {
				auditBody = b
			}
		}
		if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "grok-4.5", auditBody); decision != nil && decision.Blocked {
			c.JSON(contentModerationStatus(decision), gin.H{"error": gin.H{"type": contentModerationErrorCode(decision), "message": decision.Message}})
			return
		}
	}
	var streamStarted bool
	reqLog := requestLogger(c, "handler.openai_gateway.grok_voice", zap.String("endpoint", endpoint))
	userRelease, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userRelease != nil {
		defer userRelease()
	}
	contentType := c.GetHeader("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}

	failed := map[int64]struct{}{}
	var last *service.UpstreamFailoverError
	selectionModel := "grok-4.5"
	requestCtx := c.Request.Context()
	voiceOperation := classifyGrokCustomVoiceOperation(c, endpoint)
	var voiceBinding *service.GrokCustomVoiceBinding
	var voiceBindings []*service.GrokCustomVoiceBinding
	if voiceOperation != grokCustomVoiceOperationNone {
		readyCtx, cancel := context.WithTimeout(requestCtx, 2*time.Second)
		readyErr := h.gatewayService.CheckGrokCustomVoiceStore(readyCtx)
		cancel()
		if readyErr != nil {
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Custom voice service is temporarily unavailable")
			return
		}
		gatewayKeyID := grokVoiceGatewayKeyID(requestCtx)
		switch voiceOperation {
		case grokCustomVoiceOperationGet, grokCustomVoiceOperationUpdate, grokCustomVoiceOperationDelete, grokCustomVoiceOperationAudio:
			voiceID := grokCustomVoiceID(endpoint)
			voiceBinding, err = h.gatewayService.GetGrokCustomVoiceBinding(requestCtx, voiceID)
			if err != nil {
				if errors.Is(err, service.ErrGrokCustomVoiceNotFound) {
					h.errorResponse(c, http.StatusNotFound, "not_found_error", "Custom voice not found")
				} else {
					h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Custom voice state is temporarily unavailable")
				}
				return
			}
			if !voiceBinding.BelongsTo(subject.UserID, apiKey.ID, gatewayKeyID) {
				h.errorResponse(c, http.StatusNotFound, "not_found_error", "Custom voice not found")
				return
			}
		case grokCustomVoiceOperationList:
			voiceBindings, err = h.gatewayService.ListGrokCustomVoiceBindings(requestCtx, subject.UserID, apiKey.ID, gatewayKeyID)
			if err != nil {
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Custom voice state is temporarily unavailable")
				return
			}
		}
	}

	if voiceOperation == grokCustomVoiceOperationList {
		h.listOwnedGrokCustomVoices(c, apiKey, subject, voiceBindings, contentType)
		return
	}
	if voiceBinding != nil {
		h.forwardBoundGrokCustomVoice(c, apiKey, subject, subscription, voiceBinding, endpoint, body, contentType, reqLog)
		return
	}

	// 与其他协议共用 max_account_switches，避免语音端点绕开全局失败切换预算。
	maxAttempts := h.maxAccountSwitches + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	for attempts := 0; attempts < maxAttempts; attempts++ {
		selection, _, selectErr := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			apiKey.GroupID,
			"",
			"",
			selectionModel,
			failed,
			service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			service.PlatformGrok,
		)
		if selectErr != nil || selection == nil || selection.Account == nil {
			if last != nil {
				h.handleFailoverExhausted(c, last, false)
			} else {
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available Grok accounts")
			}
			return
		}
		account := selection.Account
		if endpoint == "stt" {
			// A selected Voice account is immutable for this HTTP request. Its account
			// override decides whether strong keyword hits are allowed.
			c.Set("content_moderation_disable_keyword_reroute", true)
			c.Set(contentModerationBoundAccountAllowsBlockedKeywordsKey, account.AcceptsBlockedKeywords(apiKey.Group.AllowBlockedKeywordRequests))
		}
		var started bool
		release, status := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &started, reqLog)
		if !status {
			return
		}
		result, forwardErr := func() (*service.OpenAIForwardResult, error) {
			defer release()
			return h.gatewayService.ForwardGrokVoice(c.Request.Context(), c, account, endpoint, body, contentType, voiceOperation == grokCustomVoiceOperationCreate || endpoint == "stt")
		}()
		if forwardErr == nil {
			if endpoint == "stt" {
				// xAI has completed billable work. Freeze that fact before post-response
				// policy checks so a local 4xx cannot release the regional lease.
				c.Request = c.Request.WithContext(gatewaylite.ContextWithUpstreamConsumed(c.Request.Context()))
				if auditBody := extractGrokSTTResponseAuditBody(result.ResponseBody); len(auditBody) > 0 {
					if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, "stt", auditBody); decision != nil && decision.Blocked {
						h.recordGrokVoiceUsage(c, apiKey, account, subscription, endpoint, body, result)
						c.JSON(contentModerationStatus(decision), gin.H{"error": gin.H{"type": contentModerationErrorCode(decision), "message": decision.Message}})
						return
					}
				}
				h.gatewayService.WriteDeferredGrokVoiceResponse(c, result)
			}
			if voiceOperation == grokCustomVoiceOperationCreate {
				voiceID := grokCustomVoiceResponseID(result)
				if voiceID == "" {
					reqLog.Error("grok_custom_voice.create_missing_voice_id", zap.Int64("account_id", account.ID))
					h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream custom voice response did not contain voice_id")
					return
				}
				binding := &service.GrokCustomVoiceBinding{
					VoiceID: voiceID, UserID: subject.UserID, APIKeyID: apiKey.ID,
					GatewayKeyID: grokVoiceGatewayKeyID(requestCtx), GroupID: apiKey.GroupID, AccountID: account.ID,
				}
				if saveErr := h.gatewayService.SaveGrokCustomVoiceBinding(requestCtx, binding); saveErr != nil {
					reqLog.Error("grok_custom_voice.binding_save_failed", zap.String("voice_id", voiceID), zap.Int64("account_id", account.ID), zap.Error(saveErr))
					if _, cleanupErr := h.gatewayService.DeleteGrokCustomVoiceFromUpstream(requestCtx, account, voiceID); cleanupErr != nil {
						reqLog.Error("grok_custom_voice.orphan_cleanup_failed", zap.String("voice_id", voiceID), zap.Int64("account_id", account.ID), zap.Error(cleanupErr))
					}
					// Fail closed: without the local owner/account binding every later
					// operation would either leak the resource or route to the wrong account.
					h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Custom voice was created upstream but local ownership could not be saved; contact the administrator")
					return
				}
				c.Data(http.StatusOK, "application/json", result.ResponseBody)
			}
			h.recordGrokVoiceUsage(c, apiKey, account, subscription, endpoint, body, result)
			return
		}
		var failoverErr *service.UpstreamFailoverError
		if errors.As(forwardErr, &failoverErr) && failoverErr.ShouldRetryNextAccount() {
			h.gatewayService.RecordOpenAIAccountSwitch()
			failed[account.ID] = struct{}{}
			last = failoverErr
			continue
		}
		// Non-failover errors: handleGrokMediaErrorResponse / transport already wrote response.
		return
	}
	if last != nil {
		h.handleFailoverExhausted(c, last, false)
	}
}

type grokCustomVoiceOperation uint8

const (
	grokCustomVoiceOperationNone grokCustomVoiceOperation = iota
	grokCustomVoiceOperationCreate
	grokCustomVoiceOperationList
	grokCustomVoiceOperationGet
	grokCustomVoiceOperationUpdate
	grokCustomVoiceOperationDelete
	grokCustomVoiceOperationAudio
)

func classifyGrokCustomVoiceOperation(c *gin.Context, endpoint string) grokCustomVoiceOperation {
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	if endpoint == "custom-voices" {
		if c != nil && c.Request != nil && c.Request.Method == http.MethodGet {
			return grokCustomVoiceOperationList
		}
		if c != nil && c.Request != nil && c.Request.Method == http.MethodPost {
			return grokCustomVoiceOperationCreate
		}
		return grokCustomVoiceOperationNone
	}
	if !strings.HasPrefix(endpoint, "custom-voices/") || c == nil || c.Request == nil {
		return grokCustomVoiceOperationNone
	}
	if strings.HasSuffix(endpoint, "/audio") && c.Request.Method == http.MethodGet {
		return grokCustomVoiceOperationAudio
	}
	switch c.Request.Method {
	case http.MethodGet:
		return grokCustomVoiceOperationGet
	case http.MethodPatch:
		return grokCustomVoiceOperationUpdate
	case http.MethodDelete:
		return grokCustomVoiceOperationDelete
	default:
		return grokCustomVoiceOperationNone
	}
}

func grokCustomVoiceID(endpoint string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(endpoint), "/"), "/")
	if len(parts) < 2 || parts[0] != "custom-voices" {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func grokVoiceGatewayKeyID(ctx context.Context) string {
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		return strings.TrimSpace(meta.KeyID)
	}
	return ""
}

func grokCustomVoiceResponseID(result *service.OpenAIForwardResult) string {
	if result == nil || len(result.ResponseBody) == 0 {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(result.ResponseBody, &payload) != nil {
		return ""
	}
	voiceID, _ := payload["voice_id"].(string)
	return strings.TrimSpace(voiceID)
}

func (h *OpenAIGatewayHandler) forwardBoundGrokCustomVoice(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, subscription *service.UserSubscription, binding *service.GrokCustomVoiceBinding, endpoint string, body []byte, contentType string, reqLog *zap.Logger) {
	account, err := h.gatewayService.GetGrokCustomVoiceAccount(c.Request.Context(), binding)
	if err != nil {
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Custom voice account is unavailable")
		return
	}
	selection := &service.AccountSelectionResult{Account: account, WaitPlan: &service.AccountWaitPlan{AccountID: account.ID, MaxConcurrency: max(account.Concurrency, 1), Timeout: 30 * time.Second, MaxWaiting: 3}}
	var started bool
	release, acquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, "", selection, false, &started, reqLog)
	if !acquired {
		return
	}
	if release != nil {
		defer release()
	}
	result, err := h.gatewayService.ForwardGrokVoice(c.Request.Context(), c, account, endpoint, body, contentType)
	if err != nil {
		var failoverErr *service.UpstreamFailoverError
		if errors.As(err, &failoverErr) {
			h.handleFailoverExhausted(c, failoverErr, c.Writer.Written())
		}
		return
	}
	if c.Request.Method == http.MethodDelete {
		if deleteErr := h.gatewayService.DeleteGrokCustomVoiceBinding(c.Request.Context(), binding.VoiceID); deleteErr != nil && !errors.Is(deleteErr, service.ErrGrokCustomVoiceNotFound) {
			reqLog.Error("grok_custom_voice.binding_delete_failed", zap.String("voice_id", binding.VoiceID), zap.Error(deleteErr))
		}
	}
	h.recordGrokVoiceUsage(c, apiKey, account, subscription, endpoint, body, result)
	_ = subject
}

func (h *OpenAIGatewayHandler) listOwnedGrokCustomVoices(c *gin.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, bindings []*service.GrokCustomVoiceBinding, contentType string) {
	voices := make([]json.RawMessage, 0, len(bindings))
	for _, binding := range bindings {
		if binding == nil || !binding.BelongsTo(subject.UserID, apiKey.ID, grokVoiceGatewayKeyID(c.Request.Context())) {
			continue
		}
		account, err := h.gatewayService.GetGrokCustomVoiceAccount(c.Request.Context(), binding)
		if err != nil {
			continue
		}
		result, err := h.gatewayService.FetchGrokCustomVoiceFromUpstream(c.Request.Context(), account, binding.VoiceID, contentType)
		if err != nil || result == nil || len(result.ResponseBody) == 0 || !json.Valid(result.ResponseBody) {
			continue
		}
		voices = append(voices, append(json.RawMessage(nil), result.ResponseBody...))
	}
	c.JSON(http.StatusOK, gin.H{"voices": voices, "pagination_token": nil})
}

// recordGrokVoiceUsage bills TTS/STT/realtime via group audio prices when AudioUsage is set.
func (h *OpenAIGatewayHandler) recordGrokVoiceUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	endpoint string,
	body []byte,
	result *service.OpenAIForwardResult,
) {
	if h == nil || c == nil || apiKey == nil || account == nil || result == nil {
		return
	}
	if result.AudioUsage == nil {
		return
	}
	// Ensure forced durable request ids even if callers forget (realtime/tts/stt money path).
	if mode := strings.TrimSpace(result.AudioUsage.Mode); mode == "realtime" {
		result.RequestID = service.StableGrokRealtimeBillingRequestID(result.RequestID)
	} else {
		result.RequestID = service.StableGrokAudioBillingRequestID(result.RequestID)
	}
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	sessionID := service.ExtractClientSessionID(c)
	requestPayloadHash := service.HashUsageRequestPayload(body)
	if requestPayloadHash == "" {
		requestPayloadHash = service.HashUsageRequestPayload([]byte(endpoint))
	}
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	quotaPlatform := service.QuotaPlatform(c.Request.Context(), apiKey)
	model := strings.TrimSpace(result.Model)
	if model == "" {
		model = endpoint
	}

	h.submitMandatoryUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             result,
			APIKey:             apiKey,
			User:               apiKey.User,
			Account:            account,
			Subscription:       subscription,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			ChannelUsageFields: clientRequestedUsageFields(c, service.ChannelMappingResult{}, model, result.UpstreamModel),
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.grok_voice"),
				zap.Int64("user_id", apiKey.User.ID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("endpoint", endpoint),
				zap.Int64("account_id", account.ID),
			).Error("grok_voice.record_usage_failed", zap.Error(err))
		}
	})
}

func readGrokVoiceGatewayBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, errors.New("request body is required")
	}
	if c.Request.Body == nil {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodDelete {
			return nil, nil
		}
		return nil, errors.New("request body is required")
	}
	return io.ReadAll(c.Request.Body)
}

// extractGrokTTSInputText pulls the primary spoken text from a TTS JSON body.
func extractGrokTTSInputText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"input", "text", "prompt"} {
		if v, ok := payload[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractGrokSTTRequestAuditBody(body []byte, contentType string) []byte {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "multipart/") || strings.TrimSpace(params["boundary"]) == "" {
		return nil
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	texts := make([]string, 0, 4)
	for {
		part, readErr := reader.NextPart()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil
		}
		if part.FileName() != "" {
			_ = part.Close()
			continue
		}
		value, valueErr := io.ReadAll(io.LimitReader(part, 512*1024+1))
		_ = part.Close()
		if valueErr != nil || len(value) > 512*1024 {
			continue
		}
		if text := strings.TrimSpace(string(value)); text != "" {
			texts = append(texts, text)
		}
	}
	return grokTextAuditBody(texts)
}

func extractGrokSTTResponseAuditBody(body []byte) []byte {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	texts := make([]string, 0, 3)
	for _, key := range []string{"text", "transcript", "transcription"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			texts = append(texts, value)
		}
	}
	return grokTextAuditBody(texts)
}

func extractGrokRealtimeAuditBody(frame []byte) []byte {
	var event map[string]any
	if json.Unmarshal(frame, &event) != nil {
		return nil
	}
	texts := make([]string, 0, 6)
	appendGrokRealtimeFieldText(&texts, event["instructions"])
	appendGrokRealtimeFieldText(&texts, event["input"])
	for _, parent := range []string{"session", "response"} {
		if object, ok := event[parent].(map[string]any); ok {
			appendGrokRealtimeFieldText(&texts, object["instructions"])
			appendGrokRealtimeFieldText(&texts, object["input"])
		}
	}
	if strings.EqualFold(strings.TrimSpace(stringValue(event["type"])), "conversation.item.create") {
		if item, ok := event["item"].(map[string]any); ok {
			appendGrokRealtimeFieldText(&texts, item["content"])
		}
	}
	return grokTextAuditBody(texts)
}

func appendGrokRealtimeFieldText(out *[]string, value any) {
	switch current := value.(type) {
	case string:
		if text := strings.TrimSpace(current); text != "" {
			*out = append(*out, text)
		}
	case []any:
		for _, item := range current {
			appendGrokRealtimeFieldText(out, item)
		}
	case map[string]any:
		typeName := strings.ToLower(strings.TrimSpace(stringValue(current["type"])))
		if strings.Contains(typeName, "audio") {
			return
		}
		for _, key := range []string{"text", "content", "input_text"} {
			if nested, exists := current[key]; exists {
				appendGrokRealtimeFieldText(out, nested)
			}
		}
	}
}

func grokTextAuditBody(texts []string) []byte {
	messages := make([]map[string]any, 0, len(texts))
	for _, raw := range texts {
		if text := strings.TrimSpace(raw); text != "" {
			messages = append(messages, map[string]any{"role": "user", "content": text})
		}
	}
	if len(messages) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		return nil
	}
	return body
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
