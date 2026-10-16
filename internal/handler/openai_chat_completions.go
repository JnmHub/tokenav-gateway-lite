package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ChatCompletions handles OpenAI Chat Completions API requests.
// POST /v1/chat/completions
func (h *OpenAIGatewayHandler) ChatCompletions(c *gin.Context) {
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
		"handler.openai_gateway.chat_completions",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
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
	if !modelResult.Exists() || modelResult.Type != gjson.String || modelResult.String() == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := modelResult.String()
	// Preserve the model supplied by the client before group/account mapping.
	// The compatibility forwarder may receive a body whose model has already
	// been rewritten for the upstream, but request-mode response declarations
	// must still expose the client's original model.
	c.Request = c.Request.WithContext(service.WithClientRequestedResponseModel(c.Request.Context(), reqModel))
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}
	if h.isModelDisabled(reqModel) {
		h.errorResponse(c, http.StatusForbidden, "model_disabled", "model is disabled")
		return
	}
	reqStream, ok := parseOpenAICompatibleStream(body)
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", invalidStreamFieldTypeMessage)
		return
	}

	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", reqStream))

	setOpsRequestContext(c, reqModel, reqStream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(reqStream, false)))

	if decision := h.checkContentModeration(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIChat, reqModel, body); decision != nil && decision.Blocked {
		h.errorResponse(c, contentModerationStatus(decision), contentModerationErrorCode(decision), decision.Message)
		return
	}
	body = contentModerationForwardBody(c, body)
	// 推理档位属于实际计费分组策略，不能在关键词切组前用原分组改写请求。
	// composite（组合分组）也必须先确认本次路由的具体目标确实是 OpenAI。
	if policyBody, changed := applyOpenAIReasoningEffortPolicyForRequest(c, apiKey, body); changed {
		body = policyBody
	}
	if h.rejectIfCyberSessionBlocked(c, apiKey, body, reqModel, cyberBlockFormatChat) {
		return
	}

	// 解析渠道级模型映射
	// billingChannelMapping 始终保留请求入口分组的映射。跨组故障转移只
	// 改变路由映射，不能让目标分组的价格或计费模型覆盖原分组的冻结策略。
	billingChannelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	requestPlatform := openAICompatibleRequestPlatformFromContext(c.Request.Context(), apiKey)
	endpointModel := reqModel
	if billingChannelMapping.Mapped && billingChannelMapping.MappedModel != "" {
		endpointModel = billingChannelMapping.MappedModel
	}
	if requestPlatform == service.PlatformOpenAI && service.IsOpenAIImageGenerationModel(endpointModel) {
		// 图片模型不能被当成普通文本账号故障后继续换号；在占用并发和
		// 额度资格检查前返回稳定 400，引导客户使用 Images 端点。
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Image generation models are not available on /v1/chat/completions; use /v1/images/generations instead")
		return
	}

	if h.errorPassthroughService != nil {
		service.BindErrorPassthroughService(c, h.errorPassthroughService)
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	longContextModels := []string{reqModel}
	if billingChannelMapping.Mapped {
		longContextModels = append([]string{billingChannelMapping.MappedModel}, longContextModels...)
	}
	routingCtx, _, routingErr := h.gatewayService.PrepareOpenAILongContextRouting(
		c.Request.Context(), apiKey.Group, requestPlatform, body, longContextModels...,
	)
	if routingErr != nil {
		status, errType, code, message, _ := service.OpenAILongContextRoutingErrorDetails(routingErr)
		h.handleStreamingAwareErrorWithCode(c, status, errType, code, message, streamStarted)
		return
	}
	c.Request = c.Request.WithContext(routingCtx)
	c.Request = c.Request.WithContext(service.WithOpenAIGroupModelFallbackEnabled(c.Request.Context()))

	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	routingStart := time.Now()

	userReleaseFunc, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	if !h.shouldSkipGatewayLiteBillingEligibility(c) {
		if err := h.billingCacheService.CheckBillingEligibilityForRequest(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey), service.EstimateRequestTokensForLimit(body)); err != nil {
			reqLog.Info("openai_chat_completions.billing_eligibility_check_failed", zap.Error(err))
			status, code, message, retryAfter := billingErrorDetails(err)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.handleStreamingAwareError(c, status, code, message, streamStarted)
			return
		}
	}

	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	promptCacheKey := h.gatewayService.ExtractSessionID(c, body)

	maxAccountSwitches := h.maxAccountSwitches
	switchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	sameAccountRetryCount := make(map[int64]int)
	var lastFailoverErr *service.UpstreamFailoverError
	routingGroupID := apiKey.SchedulerGroupID()
	routingChannelMapping := billingChannelMapping
	sourceRequestPlatform := requestPlatform
	failoverStage := "source"

	// 解析一次跨组计划。目标分组只用于资格检查和账号调度，计费仍以
	// billingChannelMapping 与请求开始时冻结的 A 分组策略为准。
	failoverPlan, failoverPlanErr := h.gatewayService.ResolveOpenAIGroupFailoverPlan(
		c.Request.Context(), apiKey.Group, service.OpenAIGroupFailoverRequest{
			Endpoint:       service.OpenAIGroupFailoverEndpointChatCompletions,
			RequestedModel: reqModel,
			APIKey:         apiKey,
		},
	)
	if failoverPlanErr != nil {
		reqLog.Warn("openai_chat_completions.group_failover_plan_failed", zap.Error(failoverPlanErr))
		failoverPlan = service.OpenAIGroupFailoverPlan{}
	} else if failoverPlan.Eligible {
		// original_model_first 必须先耗尽 A 的原模型；同组模型兜底
		// 由跨组状态机在 B 失败后恢复 A 时单独放行。
		if failoverPlan.TryCrossGroupBeforeSourceFallback {
			c.Request = c.Request.WithContext(service.WithoutOpenAIGroupModelFallbackPolicy(c.Request.Context()))
		}
		reqLog.Debug("openai_chat_completions.group_failover_plan_ready",
			zap.String("strategy", failoverPlan.Strategy),
			zap.Int64("source_group_id", failoverPlan.SourceGroup.ID),
			zap.Int64("target_group_id", failoverPlan.TargetGroup.ID),
		)
	}

	resetAccountAttempts := func() {
		switchCount = 0
		failedAccountIDs = make(map[int64]struct{})
		sameAccountRetryCount = make(map[int64]int)
		lastFailoverErr = nil
	}
	var targetGroupRelease func()
	defer func() {
		if targetGroupRelease != nil {
			targetGroupRelease()
		}
	}()
	tryGroupFailover := func(failure error) bool {
		if !failoverPlan.Eligible || failure == nil || c.Writer.Written() || c.Request.Context().Err() != nil ||
			!service.ShouldOpenAIGroupFailover(failoverPlan, failure) {
			return false
		}
		switch failoverStage {
		case "source":
			if failoverPlan.TrySourceFallbackBeforeCrossGroup {
				// current_group_first 要先完整尝试 A 的兜底模型。这里显式
				// 进入 fallback-only（仅兜底模型），避免账号级切换预算较小
				// 时从 A 原模型直接跳到 B。
				fallbackCtx := service.WithOpenAIGroupModelFallbackPolicy(c.Request.Context())
				fallbackCtx = service.WithOpenAIGroupModelFallbackOnly(fallbackCtx)
				c.Request = c.Request.WithContext(fallbackCtx)
				failoverStage = "source_fallback_before_target"
				resetAccountAttempts()
				reqLog.Warn("openai_chat_completions.group_failover_trying_source_fallback",
					zap.Int64("source_group_id", failoverPlan.SourceGroup.ID), zap.Error(failure))
				return true
			}
			fallthrough
		case "source_fallback_before_target":
			candidateRelease, acquired, acquireErr := h.concurrencyHelper.TryAcquireFailoverGroupSlot(
				c.Request.Context(), apiKey.User, subject.UserID, failoverPlan.TargetGroup,
			)
			if acquireErr != nil || !acquired {
				reqLog.Warn("openai_chat_completions.group_failover_target_concurrency_rejected",
					zap.Int64("target_group_id", failoverPlan.TargetGroup.ID), zap.Error(acquireErr))
				return false
			}
			failedOverCtx, targetID, applyErr := service.ApplyOpenAIGroupFailover(c.Request.Context(), failoverPlan)
			if applyErr != nil {
				if candidateRelease != nil {
					candidateRelease()
				}
				reqLog.Warn("openai_chat_completions.group_failover_apply_failed", zap.Error(applyErr))
				return false
			}
			targetGroupRelease = candidateRelease
			c.Request = c.Request.WithContext(failedOverCtx)
			failoverStage = "target"
			targetGroupID := targetID
			routingGroupID = &targetGroupID
			routingChannelMapping, _ = h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), routingGroupID, reqModel)
			requestPlatform = service.PlatformOpenAI
			resetAccountAttempts()
			reqLog.Warn("openai_chat_completions.group_failover_switching",
				zap.String("from", "source"), zap.String("to", "target"), zap.Error(failure))
			return true
		case "target":
			if !failoverPlan.TryCrossGroupBeforeSourceFallback {
				return false
			}
			restoredCtx, sourceID, restoreErr := service.RestoreOpenAIGroupForModelFallback(c.Request.Context(), failoverPlan)
			if restoreErr != nil {
				reqLog.Warn("openai_chat_completions.group_failover_restore_failed", zap.Error(restoreErr))
				return false
			}
			if targetGroupRelease != nil {
				targetGroupRelease()
				targetGroupRelease = nil
			}
			c.Request = c.Request.WithContext(restoredCtx)
			failoverStage = "source_fallback"
			sourceFallbackGroupID := sourceID
			routingGroupID = &sourceFallbackGroupID
			routingChannelMapping = billingChannelMapping
			requestPlatform = sourceRequestPlatform
			resetAccountAttempts()
			reqLog.Warn("openai_chat_completions.group_failover_restoring_source_fallback",
				zap.Int64("source_group_id", sourceID), zap.Error(failure))
			return true
		default:
			return false
		}
	}

	for {
		c.Request = c.Request.WithContext(service.WithoutOpenAIGroupModelFallback(c.Request.Context()))
		reqLog.Debug("openai_chat_completions.account_selecting", zap.Int("excluded_account_count", len(failedAccountIDs)))
		selection, scheduleDecision, err := h.gatewayService.SelectAccountWithSchedulerForCapability(
			c.Request.Context(),
			routingGroupID,
			"",
			sessionHash,
			reqModel,
			failedAccountIDs,
			service.OpenAIUpstreamTransportAny,
			service.OpenAIEndpointCapabilityChatCompletions,
			false,
			requestPlatform,
		)
		if err != nil {
			reqLog.Warn("openai_chat_completions.account_select_failed",
				zap.Error(err),
				zap.Int("excluded_account_count", len(failedAccountIDs)),
			)
			if tryGroupFailover(err) {
				continue
			}
			if len(failedAccountIDs) == 0 {
				if status, errType, code, message, ok := service.OpenAILongContextRoutingErrorDetails(err); ok {
					markOpsRoutingCapacityLimited(c)
					h.handleStreamingAwareErrorWithCode(c, status, errType, code, message, streamStarted)
					return
				}
				if errors.Is(err, service.ErrFallbackQueueFull) {
					h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
					return
				}
				markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "Service temporarily unavailable", streamStarted)
				return
			} else {
				if lastFailoverErr != nil {
					h.handleFailoverExhausted(c, lastFailoverErr, streamStarted)
				} else {
					h.handleStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
				}
				return
			}
		}
		if selection == nil || selection.Account == nil {
			if tryGroupFailover(service.ErrNoAvailableAccounts) {
				continue
			}
			markOpsRoutingCapacityLimited(c)
			h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", streamStarted)
			return
		}
		applyOpenAIGroupModelFallbackSelection(c, reqLog, routingGroupID, reqModel, selection)
		if failoverStage == "source" && failoverPlan.TrySourceFallbackBeforeCrossGroup && selection.GroupFallbackUsed {
			// 调度器已自然从 A 原模型落到 A 兜底模型；后续若该次
			// 上游请求也失败，应直接切 B，不能重复执行同一兜底阶段。
			failoverStage = "source_fallback_before_target"
		}
		account := selection.Account
		sessionHash = ensureOpenAIPoolModeSessionHash(sessionHash, account)
		reqLog.Debug("openai_chat_completions.account_selected", zap.Int64("account_id", account.ID), zap.String("account_name", account.Name))
		_ = scheduleDecision
		setOpsSelectedAccount(c, account.ID, account.Platform)

		accountReleaseFunc, acquired := h.acquireResponsesAccountSlot(c, routingGroupID, sessionHash, selection, reqStream, &streamStarted, reqLog)
		if !acquired {
			return
		}

		service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
		forwardStart := time.Now()

		forwardBody := body
		if routingChannelMapping.Mapped {
			forwardBody = h.gatewayService.ReplaceModelInBody(body, routingChannelMapping.MappedModel)
		}
		writerSizeBeforeForward := c.Writer.Size()
		// 伪造流式首字：上游可能很久才回响应头，服务层流式写入被调用时已经太晚；
		// 这里在真正请求上游前武装定时器，随机延迟后若客户端仍未收到真实字节就
		// 抢先提交 200 + SSE 注释行。注释行计入 keepalive 字节，不算真实正文，
		// 因此不阻止安全换号；同一请求只武装一次。
		if reqStream {
			stopFakeFirstToken := armFakeFirstTokenBeforeUpstream(c, h.gatewayService.FakeFirstTokenSettings(), requestStart)
			defer stopFakeFirstToken()
		}
		result, err := func() (*service.OpenAIForwardResult, error) {
			defer func() {
				if accountReleaseFunc != nil {
					accountReleaseFunc()
				}
			}()
			return h.gatewayService.ForwardAsChatCompletions(c.Request.Context(), c, account, forwardBody, promptCacheKey, "")
		}()
		cyberBlockKeyChat := ""
		if service.GetOpsCyberPolicy(c) != nil {
			cyberBlockKeyChat = h.findCyberSessionBlockKeyForAPIKey(c, apiKey, body)
		}
		h.recordCyberPolicyIfMarked(c, apiKey, account, subscription, reqModel, err != nil, cyberBlockKeyChat, billingChannelMapping.ToUsageFields(reqModel, ""), service.HashUsageRequestPayload(body))
		recordWeightedRouteForwardResult(c, routingGroupID, reqModel, selection, err)

		forwardDurationMs := time.Since(forwardStart).Milliseconds()
		upstreamLatencyMs, _ := getContextInt64(c, service.OpsUpstreamLatencyMsKey)
		responseLatencyMs := forwardDurationMs
		if upstreamLatencyMs > 0 && forwardDurationMs > upstreamLatencyMs {
			responseLatencyMs = forwardDurationMs - upstreamLatencyMs
		}
		service.SetOpsLatencyMs(c, service.OpsResponseLatencyMsKey, responseLatencyMs)
		if err == nil && result != nil && result.FirstTokenMs != nil {
			service.SetOpsLatencyMs(c, service.OpsTimeToFirstTokenMsKey, int64(*result.FirstTokenMs))
		}
		if err != nil {
			if result != nil && result.ImageCount > 0 {
				reqLog.Warn("openai_chat_completions.forward_partial_error_with_image_result",
					zap.Int64("account_id", account.ID),
					zap.Int("image_count", result.ImageCount),
					zap.Error(err),
				)
			} else {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					if failoverRequestCanceled(c.Request.Context()) {
						return
					}
					if c.Writer.Size() != writerSizeBeforeForward {
						h.handleFailoverExhausted(c, failoverErr, true)
						return
					}
					if !failoverErr.ShouldRetryNextAccount() {
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					if failoverErr.ShouldReportAccountScheduleFailure() {
						h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
					}
					// Pool mode: retry on the same account
					if failoverErr.RetryableOnSameAccount {
						retryLimit := account.GetPoolModeRetryCount()
						if sameAccountRetryAllowedWithError(failoverErr, sameAccountRetryCount[account.ID], retryLimit) {
							sameAccountRetryCount[account.ID]++
							retryDelay := sameAccountRetryDelayFor(failoverErr, sameAccountRetryCount[account.ID])
							reqLog.Warn("openai_chat_completions.pool_mode_same_account_retry",
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
					switchBudget := openAIFailoverSwitchBudget(account, failoverErr, maxAccountSwitches)
					if switchCount >= switchBudget {
						if tryGroupFailover(failoverErr) {
							continue
						}
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					switchCount++
					if h.gatewayService.ShouldStopOpenAIOAuth429Failover(account, failoverErr.StatusCode, switchCount) {
						if tryGroupFailover(failoverErr) {
							continue
						}
						h.handleFailoverExhausted(c, failoverErr, streamStarted)
						return
					}
					if !waitOpenAIFailoverSwitch(c.Request.Context(), account, failoverErr, switchCount) {
						return
					}
					h.gatewayService.RecordOpenAIAccountSwitch()
					reqLog.Warn("openai_chat_completions.upstream_failover_switching",
						zap.Int64("account_id", account.ID),
						zap.Int("upstream_status", failoverErr.StatusCode),
						zap.Int("switch_count", switchCount),
						zap.Int("max_switches", switchBudget),
					)
					continue
				}
				h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
				if tryGroupFailover(err) {
					continue
				}
				upstreamErrorAlreadyCommunicated := openAIForwardErrorAlreadyCommunicated(c, writerSizeBeforeForward, err)
				wroteFallback := false
				if !upstreamErrorAlreadyCommunicated {
					wroteFallback = h.ensureOpenAIStreamReadErrorResponse(c, err, streamStarted)
					if !wroteFallback {
						wroteFallback = h.ensureForwardErrorResponse(c, streamStarted)
					}
				}
				reqLog.Warn("openai_chat_completions.forward_failed",
					zap.Int64("account_id", account.ID),
					zap.Bool("fallback_error_response_written", wroteFallback),
					zap.Bool("upstream_error_response_already_written", upstreamErrorAlreadyCommunicated),
					zap.Error(err),
				)
				return
			}
		}
		if result != nil {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, result.FirstTokenMs, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
		} else {
			h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, true, nil, mappedOpenAIAccountScheduleModel(c.Request.Context(), account, routingChannelMapping, reqModel))
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		inboundEndpoint := GetInboundEndpoint(c)
		upstreamEndpoint := resolveRawCCUpstreamEndpoint(c, account)
		sessionID := service.ExtractClientSessionID(c)
		usageFields := clientRequestedUsageFields(c, billingChannelMapping, reqModel, result.UpstreamModel)
		if failoverStage == "target" {
			// 审计链展示 B 实际执行的映射，但 ChannelID（渠道编号）、
			// ChannelMappedModel（计费映射模型）和 BillingModelSource（计费模型来源）
			// 继续使用 A，防止跨组恢复能力改变客户价格。
			routingUsageFields := clientRequestedUsageFields(c, routingChannelMapping, reqModel, result.UpstreamModel)
			usageFields.ModelMappingChain = routingUsageFields.ModelMappingChain
		}

		cyberBlocked := service.GetOpsCyberPolicy(c) != nil
		h.submitOpenAIUsageRecordTask(c.Request.Context(), result, func(ctx context.Context) {
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
				APIKeyService:      h.apiKeyService,
				ChannelUsageFields: usageFields,
				CyberBlocked:       cyberBlocked,
			}); err != nil {
				logger.L().With(
					zap.String("component", "handler.openai_gateway.chat_completions"),
					zap.Int64("user_id", subject.UserID),
					zap.Int64("api_key_id", apiKey.ID),
					zap.Any("group_id", apiKey.GroupID),
					zap.String("model", reqModel),
					zap.Int64("account_id", account.ID),
				).Error("openai_chat_completions.record_usage_failed", zap.Error(err))
			}
		})
		reqLog.Debug("openai_chat_completions.request_completed",
			zap.Int64("account_id", account.ID),
			zap.Int("switch_count", switchCount),
		)
		return
	}
}

// resolveRawCCUpstreamEndpoint returns the actual upstream endpoint for
// OpenAI Chat Completions requests. For APIKey accounts whose upstream
// is forced or probed to not support the Responses API, the request is
// forwarded directly to /v1/chat/completions — not through the default
// CC→Responses conversion path.
func resolveRawCCUpstreamEndpoint(c *gin.Context, account *service.Account) string {
	if account != nil && account.Type == service.AccountTypeAPIKey &&
		!openai_compat.ShouldUseResponsesAPI(account.Extra) {
		return "/v1/chat/completions"
	}
	return GetUpstreamEndpoint(c, account.Platform)
}
