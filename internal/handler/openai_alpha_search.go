package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// AlphaSearch proxies the standalone search endpoint used by Codex.
func (h *OpenAIGatewayHandler) AlphaSearch(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)
	setOpenAIClientTransportHTTP(c)
	requestStart := time.Now()

	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Codex alpha search is only available for OpenAI groups")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.alpha_search",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONTextRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	if !gjson.ValidBytes(body) {
		logRequestBodyParseFailure(reqLog, body, nil)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || strings.TrimSpace(modelResult.String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	requestedModel := strings.TrimSpace(modelResult.String())
	// Alpha Search 也可能经过模型映射；保存映射前模型，避免对外泄露
	// 实际上游模型声明。
	c.Request = c.Request.WithContext(service.WithClientRequestedResponseModel(c.Request.Context(), requestedModel))
	ensureCompositeTargetPlatform(c, apiKey, requestedModel)
	if !compositeTargetPlatformAllowed(c, apiKey, requestedModel, service.PlatformOpenAI) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by Codex alpha search for composite groups")
		return
	}
	if h.isModelDisabled(requestedModel) {
		h.errorResponse(c, http.StatusForbidden, "model_disabled", "model is disabled")
		return
	}
	reqLog = reqLog.With(zap.String("model", requestedModel))
	setOpsRequestContext(c, requestedModel, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeSync))
	if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, requestedModel, body); decision != nil && decision.Blocked {
		h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	body = contentModerationForwardBody(c, body)

	routingGroupID := apiKey.SchedulerGroupID()
	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), routingGroupID, requestedModel)
	forwardBody := openAIModelMappedBody(body, channelMapping.Mapped, channelMapping.MappedModel, h.gatewayService.ReplaceModelInBody)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())

	userRelease, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userRelease != nil {
		defer userRelease()
	}

	if !h.shouldSkipGatewayLiteBillingEligibility(c) {
		if err := h.billingCacheService.CheckBillingEligibilityForRequest(
			c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription,
			service.QuotaPlatform(c.Request.Context(), apiKey), service.EstimateRequestTokensForLimit(body),
		); err != nil {
			status, code, message, retryAfter := billingErrorDetails(err)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.errorResponse(c, status, code, message)
			return
		}
	}

	searchID := strings.TrimSpace(gjson.GetBytes(body, "id").String())
	sessionHash := h.gatewayService.GenerateSessionHashWithFallback(c, nil, searchID)
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	switchCount := 0
	routingStart := time.Now()

	for {
		selection, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(), routingGroupID, "", sessionHash, requestedModel,
			failedAccountIDs, service.OpenAIUpstreamTransportHTTPSSE,
			service.OpenAIEndpointCapabilityAlphaSearch, false, service.PlatformOpenAI,
		)
		if err != nil || selection == nil || selection.Account == nil {
			if len(failedAccountIDs) == 0 {
				cls := classifyNoAccountErrorFromGin(c, h.gatewayService, routingGroupID, requestedModel, requestedModel, service.PlatformOpenAI)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, false)
			} else {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			}
			return
		}

		account := selection.Account
		setOpsSelectedAccount(c, account.ID, account.Platform)
		accountRelease, acquired := h.acquireResponsesAccountSlot(c, routingGroupID, sessionHash, selection, false, &streamStarted, reqLog)
		if !acquired {
			return
		}
		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		writerSizeBeforeForward := c.Writer.Size()
		forwardStart := time.Now()
		var result *service.OpenAIForwardResult
		result, err = func() (*service.OpenAIForwardResult, error) {
			if accountRelease != nil {
				defer accountRelease()
			}
			return h.gatewayService.ForwardAlphaSearch(c.Request.Context(), c, account, forwardBody)
		}()
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, time.Since(forwardStart).Milliseconds())

		if err == nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, channelMapping, requestedModel))
			if result != nil {
				h.recordAlphaSearchUsage(c, apiKey, account, subscription, channelMapping, requestedModel, body, result, subject.UserID)
			}
			return
		}

		var failoverErr *service.UpstreamFailoverError
		if !errors.As(err, &failoverErr) {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, channelMapping, requestedModel))
			if c.Writer.Size() == writerSizeBeforeForward {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			}
			reqLog.Warn("openai_alpha_search.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			return
		}

		if failoverRequestCanceled(c.Request.Context()) {
			return
		}
		if c.Writer.Size() != writerSizeBeforeForward {
			h.handleFailoverExhausted(c, failoverErr, true)
			return
		}
		if failoverErr.ShouldReportAccountScheduleFailure() {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, channelMapping, requestedModel))
		}
		if !failoverErr.ShouldRetryNextAccount() {
			h.handleFailoverExhausted(c, failoverErr, false)
			return
		}
		if failoverErr.RetryableOnSameAccount {
			retryLimit := account.GetPoolModeRetryCount()
			if sameAccountRetryAllowedWithError(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
				sameAccountRetryCount[account.ID]++
				retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
				reqLog.Warn("openai_alpha_search.pool_mode_same_account_retry",
					zap.Int64("account_id", account.ID),
					zap.Int("upstream_status", failoverErr.StatusCode),
					zap.Int("retry_limit", retryLimit),
					zap.Int("retry_count", sameAccountRetryCount[account.ID]),
					zap.Duration("retry_delay", retryDelay),
				)
				select {
				case <-c.Request.Context().Done():
					return
				case <-time.After(retryDelay):
				}
				continue
			}
		}
		failedAccountIDs[account.ID] = struct{}{}
		lastFailoverErr = failoverErr
		switchBudget := openAIFailoverSwitchBudget(account, failoverErr, h.maxAccountSwitches)
		if switchCount >= switchBudget {
			h.handleFailoverExhausted(c, failoverErr, false)
			return
		}
		switchCount++
		if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
			h.handleFailoverExhausted(c, failoverErr, false)
			return
		}
		if !waitOpenAIFailoverSwitch(c.Request.Context(), account, failoverErr, switchCount) {
			return
		}
		h.gatewayService.RecordOpenAIAccountSwitch()
		reqLog.Warn("openai_alpha_search.upstream_failover_switching",
			zap.Int64("account_id", account.ID),
			zap.Int("upstream_status", failoverErr.StatusCode),
			zap.Int("switch_count", switchCount),
			zap.Int("max_switches", switchBudget),
		)
	}
}

// recordAlphaSearchUsage records one per-call charge only after a successful
// upstream search. Mandatory submission prevents a full worker queue from
// silently dropping the charge.
func (h *OpenAIGatewayHandler) recordAlphaSearchUsage(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	subscription *service.UserSubscription,
	channelMapping service.ChannelMappingResult,
	requestedModel string,
	body []byte,
	result *service.OpenAIForwardResult,
	userID int64,
) {
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	requestPayloadHash := service.HashUsageRequestPayload(body)
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	if result != nil && strings.TrimSpace(result.UpstreamEndpoint) != "" {
		upstreamEndpoint = strings.TrimSpace(result.UpstreamEndpoint)
	}
	sessionID := service.ExtractClientSessionID(c)

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
			SessionID:          sessionID,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			ChannelUsageFields: clientRequestedUsageFields(c, channelMapping, requestedModel, result.UpstreamModel),
		}); err != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.alpha_search"),
				zap.Int64("user_id", userID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("model", requestedModel),
				zap.Int64("account_id", account.ID),
			).Error("openai_alpha_search.record_usage_failed", zap.Error(err))
		}
	})
}
