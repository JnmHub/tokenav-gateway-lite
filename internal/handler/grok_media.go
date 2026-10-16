package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// GrokImages handles xAI image generation/editing through Grok groups.
func (h *OpenAIGatewayHandler) GrokImages(c *gin.Context) {
	endpoint := service.GrokMediaEndpointImagesGenerations
	if strings.Contains(c.Request.URL.Path, "/images/edits") {
		endpoint = service.GrokMediaEndpointImagesEdits
	}
	h.handleGrokMedia(c, endpoint, "")
}

// GrokVideoGeneration handles xAI video generation through Grok groups.
func (h *OpenAIGatewayHandler) GrokVideoGeneration(c *gin.Context) {
	h.handleGrokMedia(c, service.GrokMediaEndpointVideosGenerations, "")
}

// GrokVideoEdit handles asynchronous xAI video edits through Grok groups.
func (h *OpenAIGatewayHandler) GrokVideoEdit(c *gin.Context) {
	h.handleGrokVideoTransformIdempotent(c, service.GrokMediaEndpointVideosEdits)
}

// GrokVideoExtension handles asynchronous xAI video extensions through Grok groups.
func (h *OpenAIGatewayHandler) GrokVideoExtension(c *gin.Context) {
	h.handleGrokVideoTransformIdempotent(c, service.GrokMediaEndpointVideosExtensions)
}

// GrokVideoStatus handles xAI video status retrieval through Grok groups.
func (h *OpenAIGatewayHandler) GrokVideoStatus(c *gin.Context) {
	h.handleGrokMedia(c, service.GrokMediaEndpointVideoStatus, c.Param("request_id"))
}

// GrokVideoContent keeps protected xAI video URLs behind the gateway and uses
// the original task account. The durable task ownership check happens before
// any upstream request.
func (h *OpenAIGatewayHandler) GrokVideoContent(c *gin.Context) {
	h.handleGrokMedia(c, service.GrokMediaEndpointVideoContent, c.Param("request_id"))
}

func (h *OpenAIGatewayHandler) handleGrokMedia(c *gin.Context, endpoint service.GrokMediaEndpoint, requestID string) {
	platform := service.PlatformGrok
	noAccountCode, noAccountMessage := "grok_media_no_eligible_account", "No eligible Grok media accounts"
	if endpoint.IsSeedance() {
		platform = service.PlatformOpenAI
		noAccountCode, noAccountMessage = "seedance_no_eligible_account", "No eligible Seedance accounts"
	}
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)

	requestStart := time.Now()
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	reqLog := requestLogger(
		c,
		"handler.openai_gateway.grok_media",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
		zap.String("endpoint", string(endpoint)),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	var body []byte
	var err error
	if endpoint.RequiresRequestBody() {
		body, err = pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
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
	}

	contentType := c.GetHeader("Content-Type")
	if err := service.ValidateGrokMediaRequestContext(c.Request.Context(), endpoint, contentType, body); err != nil {
		reqLog.Warn("grok_media.request_validation_failed", zap.Error(err))
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	requestInfo := service.ParseGrokMediaRequest(contentType, body)
	// Seedance（Ark 原生任务）的正文是 content[]，必须用 Ark 解析器取 model/prompt/图片输入，
	// 否则 prompt 不会进入内容审计、图片输入也不会走统一校验。
	if endpoint == service.SeedanceEndpointCreate {
		parsed, parseErr := service.ParseSeedanceRequest(body)
		if parseErr != nil {
			reqLog.Warn("grok_media.seedance_request_parse_failed", zap.Error(parseErr))
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", parseErr.Error())
			return
		}
		requestInfo = parsed
	}
	requestModel := requestInfo.Model
	// Media forwarding can bypass the ordinary chat/responses policy setup;
	// preserve the original public model before any routing or account mapping.
	c.Request = c.Request.WithContext(service.WithClientRequestedResponseModel(c.Request.Context(), clientRequestedModel(c, requestModel)))
	if endpoint.IsGenerationRequest() {
		ensureCompositeTargetPlatform(c, apiKey, requestModel)
		if !compositeTargetPlatformAllowed(c, apiKey, requestModel, platform) {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by Grok media endpoints for composite groups")
			return
		}
	}
	routingModel := service.ResolveGrokMediaRoutingModel(endpoint, requestInfo)
	if endpoint.IsGenerationRequest() && strings.TrimSpace(requestModel) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if endpoint.IsVideoCreationRequest() && !xai.IsVideoModel(requestModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model must be a Grok video model")
		return
	}
	// Seedance 使用 seedance: 命名空间和 Ark 任务编号，不能套 Grok 的 request_id 规则。
	// Ark 路径片段在 ForwardSeedance/buildSeedanceURL 内独立校验，归属仍先从任务快照读取。
	if endpoint.IsVideoLookupRequest() && !endpoint.IsSeedance() {
		validatedRequestID, validateErr := xai.ValidateVideoRequestID(requestID)
		if validateErr != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "request_id is invalid")
			return
		}
		requestID = validatedRequestID
	}
	if endpoint.IsVideoCreationRequest() {
		readyCtx, readyCancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		readyErr := h.gatewayService.CheckGrokVideoTaskStore(readyCtx)
		readyCancel()
		if readyErr != nil {
			reqLog.Warn("grok_media.video_task_store_unavailable", zap.Error(readyErr))
			h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video task service is temporarily unavailable")
			return
		}
	}

	var videoTask *service.GrokVideoTask
	var videoTaskAccount *service.Account
	if endpoint.IsVideoLookupRequest() {
		if endpoint.IsSeedance() {
			// Seedance 归属走创建时写入的 Redis 快照（同一用户 + 同一 API Key），
			// 账号固定为创建时绑定的账号；缺失即 fail-closed，不要复用 Grok 的数据库任务表。
			videoTaskAccount, err = h.resolveSeedanceTaskAccount(c, apiKey, subject, requestID)
			switch {
			case err == nil:
			case errors.Is(err, ErrSeedanceTaskNotFound):
				h.errorResponse(c, http.StatusNotFound, "not_found_error", "Seedance task not found")
				return
			default:
				reqLog.Warn("grok_media.seedance_task_load_failed", zap.String("request_id", requestID), zap.Error(err))
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Seedance task state is temporarily unavailable")
				return
			}
		} else {
			videoTask, err = h.gatewayService.GetGrokVideoTask(c.Request.Context(), requestID)
			switch {
			case err == nil:
				// A media task is private to the API Key that created it. The upstream
				// request ID alone must not become a capability token for another key.
				if !grokVideoTaskBelongsToCaller(c.Request.Context(), videoTask, apiKey, subject.UserID) {
					h.errorResponse(c, http.StatusForbidden, "permission_error", "Video task does not belong to this API key")
					return
				}
				videoTaskAccount, err = h.gatewayService.GetGrokVideoTaskAccount(c.Request.Context(), videoTask)
				if err != nil {
					reqLog.Warn("grok_media.video_task_account_load_failed", zap.String("request_id", requestID), zap.Error(err))
					h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video task account is unavailable")
					return
				}
			case errors.Is(err, service.ErrGrokVideoTaskNotFound):
				// request_id 不能成为跨用户访问能力；没有持久所有者记录就拒绝。
				h.errorResponse(c, http.StatusNotFound, "not_found_error", "Video task not found")
				return
			case err != nil:
				reqLog.Warn("grok_media.video_task_load_failed", zap.String("request_id", requestID), zap.Error(err))
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video task state is temporarily unavailable")
				return
			}
		}
	}

	reqLog = reqLog.With(zap.String("model", requestModel))
	setOpsRequestContext(c, requestModel, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeSync))

	if endpoint.IsGenerationRequest() {
		if moderationBody := requestInfo.ModerationBody(); len(moderationBody) > 0 {
			decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIImages, requestModel, moderationBody)
			if decision != nil && decision.Blocked {
				h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
				return
			}
		}
		// 图片/视频权限与计费都以关键词策略选择后的实际分组为准。
		if !service.GroupAllowsImageGeneration(apiKey.Group) {
			h.errorResponse(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
			return
		}
		imageReleaseFunc, acquired := h.acquireImageGenerationSlot(c, streamStarted)
		if !acquired {
			return
		}
		if imageReleaseFunc != nil {
			defer imageReleaseFunc()
		}
	}

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	var billingErr error
	if h.shouldSkipGatewayLiteBillingEligibility(c) {
		// 异步图片任务已在用户提交入口完成限额门禁和额度扩容；worker
		// 执行或重试不能被当成新的用户请求再次消费 RPM/TPM。
		billingErr = nil
	} else if endpoint.IsGenerationRequest() {
		// 图片/视频生成、编辑和扩展都是新的付费提交，各消费一次 RPM/TPM。
		billingErr = h.billingCacheService.CheckBillingEligibilityForRequest(
			c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription,
			service.QuotaPlatform(c.Request.Context(), apiKey), service.EstimateRequestTokensForLimit(body),
		)
	} else {
		// 视频状态和内容查询属于既有任务的轮询，不得反复污染提交限额。
		billingErr = h.billingCacheService.CheckBillingEligibilityWithoutRateConsumption(
			c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription,
			service.QuotaPlatform(c.Request.Context(), apiKey),
		)
	}
	if billingErr != nil {
		reqLog.Info("grok_media.billing_eligibility_check_failed", zap.Error(billingErr))
		status, code, message, retryAfter := billingErrorDetails(billingErr)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	var videoBillingSnapshot *service.GrokVideoTaskBillingSnapshot
	if endpoint.IsVideoCreationRequest() {
		videoBillingSnapshot, _, err = h.gatewayService.PrepareGrokVideoTaskBilling(
			c.Request.Context(),
			apiKey,
			routingModel,
			service.GrokMediaBillingResolution(endpoint, requestInfo),
			service.GrokMediaReservationDurationSeconds(endpoint, requestInfo),
			1,
		)
		if err != nil {
			reqLog.Warn("grok_media.video_quota_reservation_failed", zap.Error(err))
			switch {
			case errors.Is(err, gatewaylite.ErrRequestQuotaInsufficient):
				h.errorResponse(c, http.StatusTooManyRequests, "insufficient_quota", "Insufficient quota for this video request")
			default:
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Video billing service is temporarily unavailable")
			}
			return
		}
	}

	sessionSeed := body
	if len(sessionSeed) == 0 && strings.TrimSpace(requestID) != "" {
		sessionSeed = []byte(requestID)
	}
	sessionHash := h.gatewayService.GenerateExplicitSessionHash(c, sessionSeed)
	if endpoint.IsVideoLookupRequest() {
		sessionHash = service.GrokMediaVideoRequestSessionHash(requestID)
	}
	requestCtx := c.Request.Context()
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	switchCount := 0
	maxAccountSwitches := h.maxAccountSwitches
	if maxAccountSwitches <= 0 {
		maxAccountSwitches = 3
	}
	routingStart := time.Now()
	requiredCapability := grokMediaRequiredCapability(endpoint)

	for {
		var selection *service.AccountSelectionResult
		var scheduleDecision service.OpenAIAccountScheduleDecision
		if videoTaskAccount != nil {
			// Video task status is account-affine: another account cannot observe a
			// task ID created under the original xAI credential, even when normal
			// generation scheduling for that account has since been disabled.
			maxConcurrency := videoTaskAccount.Concurrency
			if maxConcurrency <= 0 {
				maxConcurrency = 1
			}
			selection = &service.AccountSelectionResult{
				Account: videoTaskAccount,
				WaitPlan: &service.AccountWaitPlan{
					AccountID:      videoTaskAccount.ID,
					MaxConcurrency: maxConcurrency,
					Timeout:        30 * time.Second,
					MaxWaiting:     3,
				},
			}
			scheduleDecision.Layer = "grok_video_task"
			scheduleDecision.CandidateCount = 1
			scheduleDecision.TopK = 1
			scheduleDecision.SelectedAccountID = videoTaskAccount.ID
			scheduleDecision.SelectedAccountType = videoTaskAccount.Type
		} else {
			selection, scheduleDecision, err = h.gatewayService.SelectAccountWithSchedulerForCapabilityAndImageModel(
				requestCtx,
				apiKey.GroupID,
				"",
				sessionHash,
				requestModel,
				routingModel,
				failedAccountIDs,
				service.OpenAIUpstreamTransportHTTPSSE,
				requiredCapability,
				service.OpenAIImagesCapabilityBasic,
				false,
				platform,
			)
		}
		if err != nil {
			reqLog.Warn("grok_media.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if endpoint.IsGenerationRequest() && len(failedAccountIDs) == 0 && errors.Is(err, service.ErrNoAvailableAccounts) {
				markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				h.errorResponse(c, http.StatusServiceUnavailable, noAccountCode, noAccountMessage)
				return
			}
			if len(failedAccountIDs) == 0 {
				cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey.GroupID, requestModel, routingModel, service.PlatformGrok)
				if !cls.ModelNotFound {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				}
				h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
				return
			}
			if lastFailoverErr != nil {
				h.handleFailoverExhausted(c, lastFailoverErr, false)
			} else {
				h.errorResponse(c, http.StatusBadGateway, "api_error", "Upstream request failed")
			}
			return
		}
		if selection == nil || selection.Account == nil {
			if endpoint.IsGenerationRequest() {
				markOpsRoutingCapacityLimited(c)
				h.errorResponse(c, http.StatusServiceUnavailable, noAccountCode, noAccountMessage)
				return
			}
			cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey.GroupID, requestModel, routingModel, service.PlatformGrok)
			if !cls.ModelNotFound {
				markOpsRoutingCapacityLimited(c)
			}
			h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
			return
		}

		reqLog.Debug("grok_media.account_schedule_decision",
			zap.String("layer", scheduleDecision.Layer),
			zap.Bool("sticky_session_hit", scheduleDecision.StickySessionHit),
			zap.Int("candidate_count", scheduleDecision.CandidateCount),
			zap.Int("top_k", scheduleDecision.TopK),
			zap.Int64("latency_ms", scheduleDecision.LatencyMs),
			zap.Float64("load_skew", scheduleDecision.LoadSkew),
		)

		account := selection.Account
		if endpoint == service.SeedanceEndpointCreate {
			prepared, prepareErr := h.gatewayService.PrepareSeedanceTaskCreation(requestCtx, apiKey, subject.UserID, account, requestModel, clientRequestedModel(c, requestModel))
			if prepareErr != nil {
				reqLog.Warn("seedance.task_admission_unavailable", zap.Error(prepareErr))
				h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Seedance task state is temporarily unavailable")
				return
			}
			requestCtx = prepared
		}
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, accountAcquired := h.acquireResponsesAccountSlot(c, apiKey.GroupID, sessionHash, selection, false, &streamStarted, reqLog)
		if !accountAcquired {
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()
		writerSizeBeforeForward := c.Writer.Size()
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			// [v0.2.7] Seedance 端点走 Ark 原生透传，其余走 Grok 媒体转发。
			if endpoint.IsSeedance() {
				return h.gatewayService.ForwardSeedance(requestCtx, c, account, endpoint, requestID, body)
			}
			return h.gatewayService.ForwardGrokMedia(requestCtx, c, account, endpoint, requestID, body, contentType)
		}()

		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)

		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				if failoverRequestCanceled(requestCtx) {
					return
				}
				if failoverErr.ShouldReportAccountScheduleFailure() {
					h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
				}
				if videoTaskAccount != nil {
					h.handleFailoverExhausted(c, failoverErr, c.Writer.Size() != writerSizeBeforeForward)
					return
				}
				if c.Writer.Size() != writerSizeBeforeForward {
					h.handleFailoverExhausted(c, failoverErr, true)
					return
				}
				if !failoverErr.ShouldRetryNextAccount() {
					h.handleFailoverExhausted(c, failoverErr, false)
					return
				}
				if failoverErr.RetryableOnSameAccount {
					retryLimit := account.GetPoolModeRetryCount()
					if sameAccountRetryAllowedWithError(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
						sameAccountRetryCount[account.ID]++
						reqLog.Warn("grok_media.pool_mode_same_account_retry",
							zap.Int64("account_id", account.ID),
							zap.Int("upstream_status", failoverErr.StatusCode),
							zap.Int("retry_limit", retryLimit),
							zap.Int("retry_count", sameAccountRetryCount[account.ID]),
						)
						select {
						case <-requestCtx.Done():
							return
						case <-time.After(sameAccountRetryDelay):
						}
						continue
					}
				}
				h.gatewayService.RecordOpenAIAccountSwitch()
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverErr = failoverErr
				if switchCount >= maxAccountSwitches {
					h.handleFailoverExhausted(c, failoverErr, false)
					return
				}
				switchCount++
				reqLog.Warn("grok_media.upstream_failover_switching",
					zap.Int64("account_id", account.ID),
					zap.Int("upstream_status", failoverErr.StatusCode),
					zap.Int("switch_count", switchCount),
					zap.Int("max_switches", maxAccountSwitches),
				)
				continue
			}
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
			if c.Writer.Size() == writerSizeBeforeForward {
				h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
			}
			reqLog.Warn("grok_media.forward_failed",
				zap.Int64("account_id", account.ID),
				zap.Error(err),
			)
			return
		}

		h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil)
		videoUsageDeferred := false
		if endpoint.IsVideoCreationRequest() && strings.TrimSpace(result.ResponseID) != "" {
			// A task ID means completion is asynchronous. Never fall back to the
			// synchronous successful-request charge when durable registration fails.
			videoUsageDeferred = true
			if err := h.gatewayService.BindGrokMediaVideoRequestAccount(requestCtx, apiKey.GroupID, result.ResponseID, account.ID); err != nil {
				reqLog.Warn("grok_media.bind_video_request_account_failed",
					zap.Int64("account_id", account.ID),
					zap.String("request_id", result.ResponseID),
					zap.Error(err),
				)
			}
			payloadHash := service.HashUsageRequestPayload(body)
			_, taskErr := h.gatewayService.RegisterGrokVideoTask(requestCtx, service.GrokVideoTaskRegistration{
				APIKey:             apiKey,
				Account:            account,
				Subscription:       subscription,
				Result:             result,
				RequestedModel:     clientRequestedModel(c, requestModel),
				InboundEndpoint:    GetInboundEndpoint(c),
				UpstreamEndpoint:   GetUpstreamEndpoint(c, account.Platform),
				UserAgent:          c.GetHeader("User-Agent"),
				IPAddress:          ip.GetClientIP(c),
				SessionID:          service.ExtractClientSessionID(c),
				RequestPayloadHash: payloadHash,
				ChannelUsageFields: service.ChannelUsageFields{
					OriginalModel:      clientRequestedModel(c, requestModel),
					ChannelMappedModel: firstNonEmptyGrokMediaValue(result.UpstreamModel, requestModel),
				},
				BillingSnapshot: videoBillingSnapshot,
				Operation:       endpoint.VideoOperation(),
			})
			if taskErr != nil {
				gatewaylite.MarkRequestReservationForRefund(requestCtx)
				c.Set(grokVideoTaskRegistrationFailedContextKey, true)
				reqLog.Error("grok_media.register_video_task_failed",
					zap.Int64("account_id", account.ID),
					zap.String("request_id", result.ResponseID),
					zap.Error(taskErr),
				)
			}
		}
		// Seedance 的任务归属与资金快照已在 ForwardSeedance 返回成功之前持久化；
		// 原账号直接保存在任务快照，查询不依赖更短期的调度粘性绑定。

		if endpoint == service.GrokMediaEndpointVideoStatus && videoTask != nil {
			status := service.GrokVideoTaskStatus{
				State:           result.MediaTaskState,
				Message:         result.MediaTaskMessage,
				VideoCount:      result.VideoCount,
				Resolution:      result.VideoResolution,
				DurationSeconds: result.VideoDurationSeconds,
			}
			if err := h.gatewayService.FinalizeGrokVideoTask(requestCtx, videoTask, status, apiKey, subscription, account, h.apiKeyService); err != nil {
				reqLog.Error("grok_media.finalize_video_task_failed",
					zap.Int64("account_id", account.ID),
					zap.String("request_id", requestID),
					zap.String("task_state", status.State),
					zap.Error(err),
				)
			}
		}
		// 终态按原创建资金身份同步确认可靠记账；轮询本身保持免费。
		if endpoint == service.SeedanceEndpointStatus {
			if err := recordSeedanceCompletionUsage(c, h, apiKey, subject, subscription, account, requestID, result); err != nil {
				reqLog.Error("seedance.completion_billing_retryable", zap.String("task_id", requestID), zap.Error(err))
			}
		}
		if shouldRecordGrokMediaUsage(endpoint, requestModel) && !videoUsageDeferred {
			recordGrokMediaUsage(c, h, reqLog, apiKey, subject, subscription, account, result, requestModel, body, requestID)
		}
		reqLog.Debug("grok_media.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

func grokVideoTaskBelongsToCaller(ctx context.Context, task *service.GrokVideoTask, apiKey *service.APIKey, userID int64) bool {
	if task == nil || apiKey == nil || task.UserID != userID {
		return false
	}
	if gatewayKeyID := strings.TrimSpace(task.GatewayKeyID); gatewayKeyID != "" {
		meta, ok := gatewaylite.RequestMetaFromContext(ctx)
		return ok && strings.TrimSpace(meta.KeyID) == gatewayKeyID
	}
	return task.APIKeyID == apiKey.ID
}

func grokMediaRequiredCapability(endpoint service.GrokMediaEndpoint) service.OpenAIEndpointCapability {
	if endpoint.IsSeedance() {
		return service.OpenAIEndpointCapabilitySeedance
	}
	if endpoint.IsGenerationRequest() {
		return service.OpenAIEndpointCapabilityGrokMediaGeneration
	}
	return ""
}

// shouldRecordGrokMediaUsage 决定「同步成功即入账」是否适用。
// 异步视频创建（Grok 生成/编辑/扩展与 Seedance 创建）都在这里排除：
// Grok 由数据库任务在终态结算，Seedance 由状态轮询按 completion_tokens 结算；
// 查询/删除是既有任务的轮询，也绝不入账。
func shouldRecordGrokMediaUsage(endpoint service.GrokMediaEndpoint, requestModel string) bool {
	if endpoint.IsVideoCreationRequest() || endpoint == service.SeedanceEndpointCreate || endpoint.IsVideoLookupRequest() {
		return false
	}
	return endpoint.IsGenerationRequest() && strings.TrimSpace(requestModel) != ""
}

func firstNonEmptyGrokMediaValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func recordGrokMediaUsage(
	c *gin.Context,
	h *OpenAIGatewayHandler,
	reqLog *zap.Logger,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	account *service.Account,
	result *service.OpenAIForwardResult,
	requestModel string,
	body []byte,
	requestID string,
) {
	userAgent := c.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(c)
	payloadForHash := body
	if len(payloadForHash) == 0 && strings.TrimSpace(requestID) != "" {
		payloadForHash = []byte(requestID)
	}
	inboundEndpoint := GetInboundEndpoint(c)
	upstreamEndpoint := GetUpstreamEndpoint(c, account.Platform)
	sessionID := service.ExtractClientSessionID(c)
	channelUsageFields := service.ChannelUsageFields{
		OriginalModel:      clientRequestedModel(c, requestModel),
		ChannelMappedModel: requestModel,
	}
	billingSnapshot, executionCapture, asyncExecution := service.ImageTaskExecutionFromContext(c.Request.Context())
	if asyncExecution && result != nil {
		executionCapture.CaptureUsage(service.NewImageTaskUsageSnapshot(
			result,
			account,
			inboundEndpoint,
			upstreamEndpoint,
			userAgent,
			clientIP,
			sessionID,
			service.HashUsageRequestPayload(payloadForHash),
			service.QuotaPlatform(c.Request.Context(), apiKey),
			channelUsageFields,
		))
	}
	record := func(ctx context.Context) {
		if asyncExecution {
			if result == nil || result.ImageCount <= 0 {
				executionCapture.Complete(0, 0, errors.New("asynchronous Grok image execution returned no billable image result"))
				return
			}
			if billingSnapshot.CostFor(result.ImageCount) == nil {
				executionCapture.Complete(0, result.ImageCount, errors.New("asynchronous Grok frozen price is unavailable"))
				return
			}
			// The durable task owns settlement. Charging here would make an S3
			// failure bill the customer before any downloadable artifact exists.
			executionCapture.Complete(0, result.ImageCount, nil)
			return
		}
		var costOverride *service.CostBreakdown
		var rateOverride, imageRateOverride *float64
		recordErr := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:                      result,
			APIKey:                      apiKey,
			User:                        apiKey.User,
			Account:                     account,
			Subscription:                subscription,
			InboundEndpoint:             inboundEndpoint,
			UpstreamEndpoint:            upstreamEndpoint,
			UserAgent:                   userAgent,
			IPAddress:                   clientIP,
			SessionID:                   sessionID,
			RequestPayloadHash:          service.HashUsageRequestPayload(payloadForHash),
			APIKeyService:               h.apiKeyService,
			CostOverride:                costOverride,
			RateMultiplierOverride:      rateOverride,
			ImageRateMultiplierOverride: imageRateOverride,
			ChannelUsageFields:          channelUsageFields,
		})
		if recordErr != nil {
			logger.L().With(
				zap.String("component", "handler.openai_gateway.grok_media"),
				zap.Int64("user_id", subject.UserID),
				zap.Int64("api_key_id", apiKey.ID),
				zap.Any("group_id", apiKey.GroupID),
				zap.String("model", requestModel),
				zap.Int64("account_id", account.ID),
			).Error("grok_media.record_usage_failed", zap.Error(recordErr))
			reqLog.Debug("grok_media.record_usage_failed", zap.Error(recordErr))
		}
	}
	if asyncExecution {
		h.submitMandatoryUsageRecordTask(c.Request.Context(), record)
		return
	}
	h.submitOpenAIUsageRecordTask(c.Request.Context(), result, record)
}
