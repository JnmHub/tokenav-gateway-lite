package routes

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RegisterGatewayRoutes 注册 API 网关路由（Claude/OpenAI/Gemini 兼容）
func RegisterGatewayRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	compositeResolver *service.CompositeRouteResolver,
	cfg *config.Config,
) {
	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	textBodyLimit := middleware.RequestBodyLimit(cfg.Gateway.TextMaxBodySize)
	clientRequestID := middleware.ClientRequestID()
	opsErrorLogger := handler.OpsErrorLoggerMiddleware(opsService)
	// 临时请求结构调试仅保留内存中的字段形状，不记录正文值。
	r.Use(middleware.RequestDebugSchema(nil))
	endpointNorm := handler.InboundEndpointMiddleware()
	// 分组模型白名单准入：官方在每条网关链的 apiKeyAuth 之后挂载，本仓此前遗漏，
	// 导致开启白名单的分组在请求入口不生效（仅列表接口过滤）。
	groupModelAllowlist := middleware.GroupModelAllowlist()
	compositeGate := compositeGroupsRuntimeGate(settingService, cfg)
	compositeTarget := compositeTargetPlatformMiddlewareWithGate(compositeResolver, compositeGate)
	compositeGeminiTarget := compositeGeminiTargetPlatformMiddlewareWithGate(compositeResolver, compositeGate)
	keyBillingLimiter := middleware.NewKeyBillingRateLimiter(60, time.Minute)
	// Generated image URLs are loaded after the authenticated API request has
	// completed.  This endpoint belongs only to the regional gateway: the main
	// control-plane server must not accidentally expose a media capability just
	// because the shared configuration carries the gateway-lite defaults.
	// Register it outside the API-key middleware chain; MediaProxy itself
	// authenticates and expires the token.
	if cfg != nil && cfg.RunMode == config.RunModeGatewayLite && cfg.GatewayLite.MediaProxyEnabled && h != nil && h.OpenAIGateway != nil {
		r.GET("/v1/media/:token", h.OpenAIGateway.ServeMediaProxy)
	}

	// 未分组 Key 拦截中间件（按协议格式区分错误响应）
	requireGroupAnthropic := middleware.RequireGroupAssignment(settingService, middleware.AnthropicErrorWriter)
	requireGroupGoogle := middleware.RequireGroupAssignment(settingService, middleware.GoogleErrorWriter)
	// 无法完整审查的协议在准入后拒绝，防止媒体绕过 AI 强审查。
	aiUnsupportedEmbedding := h.OpenAIGateway.AIUnsupportedGate("openai_embeddings")
	aiUnsupportedLive := h.OpenAIGateway.AIUnsupportedGate("openai_live")
	aiUnsupportedAudio := h.OpenAIGateway.AIUnsupportedGate("grok_audio_input")
	aiUnsupportedVideo := h.OpenAIGateway.AIUnsupportedGate("grok_video")
	aiUnsupportedSeedance := h.OpenAIGateway.AIUnsupportedGate("seedance_video")
	googleAPIKeyAuth := middleware.APIKeyAuthWithSubscriptionGoogle(apiKeyService, subscriptionService, cfg)
	if cfg.RunMode == config.RunModeGatewayLite {
		googleAPIKeyAuth = gin.HandlerFunc(apiKeyAuth)
	}
	imagesHandler := func(c *gin.Context) {
		switch getGroupPlatform(c) {
		case service.PlatformOpenAI:
			h.OpenAIGateway.Images(c)
		case service.PlatformGrok:
			h.OpenAIGateway.GrokImages(c)
		default:
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Images API is not supported for this platform",
				},
			})
		}
	}
	imageTaskUnavailable := func(c *gin.Context) {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "image_tasks_disabled", "message": "Asynchronous image tasks are unavailable"}})
	}
	imageTaskList := imageTaskUnavailable
	imageTaskGet := imageTaskUnavailable
	imageTaskCancel := imageTaskUnavailable
	imageTaskArtifact := imageTaskUnavailable
	if h != nil && h.ImageTask != nil {
		imageTaskList = h.ImageTask.APIList
		imageTaskGet = h.ImageTask.APIGet
		imageTaskCancel = h.ImageTask.APICancel
		imageTaskArtifact = h.ImageTask.APIArtifact
	}
	videoGenerationHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok || getGroupPlatform(c) == service.PlatformComposite {
			h.OpenAIGateway.GrokVideoGeneration(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Videos API is not supported for this platform",
			},
		})
	}
	videoStatusHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok || getGroupPlatform(c) == service.PlatformComposite {
			h.OpenAIGateway.GrokVideoStatus(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Videos API is not supported for this platform",
			},
		})
	}
	videoContentHandler := func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok || getGroupPlatform(c) == service.PlatformComposite {
			h.OpenAIGateway.GrokVideoContent(c)
			return
		}
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"type":    "not_found_error",
				"message": "Videos API is not supported for this platform",
			},
		})
	}
	// Grok 视频编辑/扩展是受热开关控制的能力：路由始终注册，关闭时在这里返回 404，
	// 与“路由不存在”对客户端完全等价，但管理员改开关后无需重启进程即可生效。
	grokVideoTransformsGate := grokVideoTransformsRuntimeGate(settingService, cfg)
	videoTransformNotFound := func(c *gin.Context, message string) gin.H {
		service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
		return gin.H{"error": gin.H{"type": "not_found_error", "message": message}}
	}
	videoEditHandler := func(c *gin.Context) {
		if !grokVideoTransformsGate() {
			c.JSON(http.StatusNotFound, videoTransformNotFound(c, "Video edits API is not supported for this platform"))
			return
		}
		if getGroupPlatform(c) == service.PlatformGrok {
			h.OpenAIGateway.GrokVideoEdit(c)
			return
		}
		c.JSON(http.StatusNotFound, videoTransformNotFound(c, "Video edits API is not supported for this platform"))
	}
	videoExtensionHandler := func(c *gin.Context) {
		if !grokVideoTransformsGate() {
			c.JSON(http.StatusNotFound, videoTransformNotFound(c, "Video extensions API is not supported for this platform"))
			return
		}
		if getGroupPlatform(c) == service.PlatformGrok {
			h.OpenAIGateway.GrokVideoExtension(c)
			return
		}
		c.JSON(http.StatusNotFound, videoTransformNotFound(c, "Video extensions API is not supported for this platform"))
	}
	countTokensHandler := func(c *gin.Context) {
		switch getGroupPlatform(c) {
		case service.PlatformOpenAI, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek:
			h.OpenAIGateway.CountTokens(c)
		case service.PlatformGrok:
			h.OpenAIGateway.GrokCountTokens(c)
		default:
			h.Gateway.CountTokens(c)
		}
	}
	modelsHandler := func(c *gin.Context) {
		if c.Query("client_version") != "" {
			switch getGroupPlatform(c) {
			case service.PlatformOpenAI, service.PlatformComposite:
				h.OpenAIGateway.CodexModels(c)
				return
			}
		}
		h.Gateway.Models(c)
	}
	guardResponsesSubpath := func(next gin.HandlerFunc) gin.HandlerFunc {
		return func(c *gin.Context) {
			if !service.IsForwardableOpenAIResponsesRequestPath(c) {
				ingress.Record(ingress.RejectInvalidPath)
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": gin.H{
					"type": "not_found_error", "message": "Unsupported responses subpath",
				}})
				return
			}
			if service.IsOpenAIResponsesInputTokensRequestPath(c) && isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
				h.OpenAIGateway.ResponsesInputTokens(c)
				return
			}
			next(c)
		}
	}

	// API网关（Claude API兼容）
	gateway := r.Group("/v1")
	gateway.Use(bodyLimit)
	gateway.Use(clientRequestID)
	gateway.Use(opsErrorLogger)
	gateway.Use(endpointNorm)
	gateway.Use(gin.HandlerFunc(apiKeyAuth))
	gateway.Use(groupModelAllowlist)
	gateway.Use(compositeTarget)
	gateway.Use(requireGroupAnthropic)
	{
		gateway.GET("/sub2api/billing", keyBillingLimiter.Handler(), handler.GatewayKeyBillingInfoHandler(apiKeyService, cfg))
		// /v1/messages: auto-route based on group platform
		gateway.POST("/messages", func(c *gin.Context) {
			if isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
				h.OpenAIGateway.Messages(c)
				return
			}
			h.Gateway.Messages(c)
		})
		// /v1/messages/count_tokens: OpenAI groups bridge to OpenAI Responses input_tokens,
		// while non-OpenAI groups keep the existing Anthropic-compatible passthrough.
		gateway.POST("/messages/count_tokens", countTokensHandler)
		gateway.GET("/models", modelsHandler)
		gateway.POST("/live", aiUnsupportedLive, h.OpenAIGateway.Live)
		gateway.GET("/live/:call_id", aiUnsupportedLive, h.OpenAIGateway.LiveSideband)
		gateway.GET("/usage", h.Gateway.Usage)
		// OpenAI Responses API: auto-route based on group platform
		gateway.POST("/responses", func(c *gin.Context) {
			if isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
				h.OpenAIGateway.Responses(c)
				return
			}
			h.Gateway.Responses(c)
		})
		gateway.POST("/responses/*subpath", guardResponsesSubpath(func(c *gin.Context) {
			if isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
				h.OpenAIGateway.Responses(c)
				return
			}
			h.Gateway.Responses(c)
		}))
		gateway.POST("/alpha/search", textBodyLimit, h.OpenAIGateway.AlphaSearch)
		gateway.GET("/responses", h.OpenAIGateway.ResponsesWebSocket)
		// OpenAI Chat Completions API: auto-route based on group platform
		gateway.POST("/chat/completions", func(c *gin.Context) {
			if isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
				h.OpenAIGateway.ChatCompletions(c)
				return
			}
			h.Gateway.ChatCompletions(c)
		})
		gateway.POST("/embeddings", textBodyLimit, aiUnsupportedEmbedding, func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformOpenAI {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{
					"error": gin.H{
						"type":    "not_found_error",
						"message": "Embeddings API is not supported for this platform",
					},
				})
				return
			}
			h.OpenAIGateway.Embeddings(c)
		})
		gateway.POST("/images/generations", imagesHandler)
		gateway.POST("/images/edits", imagesHandler)
		gateway.GET("/images/tasks", imageTaskList)
		gateway.GET("/images/tasks/:id", imageTaskGet)
		gateway.POST("/images/tasks/:id/cancel", imageTaskCancel)
		gateway.GET("/images/tasks/:id/artifacts/:index", imageTaskArtifact)
		gateway.POST("/videos/generations", aiUnsupportedVideo, videoGenerationHandler)
		// 官方同款别名：OpenAI 兼容客户端可直接往 /videos 创建；xAI SDK 用 /videos/{generations,edits,extensions}/{id}[/content] 查询。
		gateway.POST("/videos", aiUnsupportedVideo, videoGenerationHandler)
		// 编辑/扩展常驻注册，开关状态由 grokVideoTransformsGate 在 handler 内判定，
		// 保证管理员保存后立即生效（关闭时仍返回与未注册一致的 404）。
		gateway.POST("/videos/edits", aiUnsupportedVideo, videoEditHandler)
		gateway.POST("/videos/extensions", aiUnsupportedVideo, videoExtensionHandler)
		gateway.GET("/videos/:request_id", videoStatusHandler)
		gateway.GET("/videos/:request_id/content", videoContentHandler)
		for _, prefix := range []string{"generations", "edits", "extensions"} {
			gateway.GET("/videos/"+prefix+"/:request_id", videoStatusHandler)
			gateway.GET("/videos/"+prefix+"/:request_id/content", videoContentHandler)
		}
		// [v0.2.7] Seedance 原生视频任务（Ark 协议透传，OpenAI/composite 分组，handler 内校验）
		gateway.POST("/contents/generations/tasks", aiUnsupportedSeedance, h.OpenAIGateway.SeedanceTasks)
		gateway.GET("/contents/generations/tasks/:task_id", h.OpenAIGateway.SeedanceTasks)
		gateway.DELETE("/contents/generations/tasks/:task_id", h.OpenAIGateway.SeedanceTasks)
		gateway.POST("/tts", func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, "tts") })
		gateway.POST("/stt", aiUnsupportedAudio, func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, "stt") })
		gateway.POST("/custom-voices", aiUnsupportedAudio, func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, "custom-voices") })
		gateway.GET("/custom-voices", func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, "custom-voices") })
		gateway.GET("/custom-voices/:voice_id/audio", func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c)) })
		gateway.GET("/custom-voices/:voice_id", func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c)) })
		gateway.PATCH("/custom-voices/:voice_id", aiUnsupportedAudio, func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c)) })
		gateway.DELETE("/custom-voices/:voice_id", func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c)) })
		gateway.GET("/realtime", aiUnsupportedAudio, h.OpenAIGateway.GrokRealtime)
		gateway.POST("/web_search", h.Gateway.WebSearch)
		gateway.POST("/x_search", func(c *gin.Context) {
			if getGroupPlatform(c) != service.PlatformGrok {
				service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "X Search API is not supported for this platform"}})
				return
			}
			h.Gateway.XSearch(c)
		})
	}

	// Gemini 原生 API 兼容层（Gemini SDK/CLI 直连）
	gemini := r.Group("/v1beta")
	gemini.Use(bodyLimit)
	gemini.Use(clientRequestID)
	gemini.Use(opsErrorLogger)
	gemini.Use(endpointNorm)
	gemini.Use(googleAPIKeyAuth)
	gemini.Use(groupModelAllowlist)
	gemini.Use(compositeGeminiTarget)
	gemini.Use(requireGroupGoogle)
	{
		gemini.GET("/models", h.Gateway.GeminiV1BetaListModels)
		gemini.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		// Gin treats ":" as a param marker, but Gemini uses "{model}:{action}" in the same segment.
		gemini.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

	// OpenAI Responses API（不带v1前缀的别名）— auto-route based on group platform
	responsesHandler := func(c *gin.Context) {
		if isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.Responses(c)
			return
		}
		h.Gateway.Responses(c)
	}
	r.POST("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, responsesHandler)
	r.POST("/responses/*subpath", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, guardResponsesSubpath(responsesHandler))
	r.POST("/alpha/search", textBodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.AlphaSearch)
	// WebSocket（网络长连接）在第一帧才能取得模型；聚合中间件先把同一只读
	// 路由解析器放进 Gin 上下文，处理器收到 response.create 后再固化路由。
	r.GET("/responses", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, h.OpenAIGateway.ResponsesWebSocket)
	r.GET("/models", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, modelsHandler)
	r.POST("/messages/count_tokens", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, countTokensHandler)
	codexDirect := r.Group("/backend-api/codex")
	codexDirect.Use(bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic)
	{
		codexDirect.POST("/realtime/calls", aiUnsupportedLive, h.OpenAIGateway.Live)
		codexDirect.POST("/responses", responsesHandler)
		codexDirect.POST("/responses/*subpath", guardResponsesSubpath(responsesHandler))
		codexDirect.POST("/alpha/search", textBodyLimit, h.OpenAIGateway.AlphaSearch)
		codexDirect.GET("/responses", h.OpenAIGateway.ResponsesWebSocket)
		codexDirect.GET("/models", h.OpenAIGateway.CodexModels)
		// 官方 Codex 返回 /backend-api/codex/{call_id} 作为 Sideband
		// WebSocket（旁路长连接）；静态路由继续由 Gin 优先匹配。
		codexDirect.GET("/:call_id", aiUnsupportedLive, h.OpenAIGateway.LiveSideband)
	}
	// OpenAI Chat Completions API（不带v1前缀的别名）— auto-route based on group platform
	r.POST("/chat/completions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, func(c *gin.Context) {
		if isOpenAICompatibleGatewayPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.ChatCompletions(c)
			return
		}
		h.Gateway.ChatCompletions(c)
	})
	r.POST("/embeddings", textBodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedEmbedding, func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformOpenAI {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "not_found_error",
					"message": "Embeddings API is not supported for this platform",
				},
			})
			return
		}
		h.OpenAIGateway.Embeddings(c)
	})
	r.POST("/images/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, imagesHandler)
	r.POST("/images/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, imagesHandler)
	r.GET("/images/tasks", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, imageTaskList)
	r.GET("/images/tasks/:id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, imageTaskGet)
	r.POST("/images/tasks/:id/cancel", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, imageTaskCancel)
	r.GET("/images/tasks/:id/artifacts/:index", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, imageTaskArtifact)
	r.POST("/videos/generations", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedVideo, videoGenerationHandler)
	r.POST("/videos", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedVideo, videoGenerationHandler)
	// 编辑/扩展常驻注册（裸路径前缀），关闭时由 handler 内热门禁返回 404。
	r.POST("/videos/edits", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedVideo, videoEditHandler)
	r.POST("/videos/extensions", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedVideo, videoExtensionHandler)
	r.GET("/videos/:request_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoStatusHandler)
	r.GET("/videos/:request_id/content", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, videoContentHandler)
	// [v0.2.7] Seedance 原生视频任务（Ark 协议透传，OpenAI/composite 分组，handler 内校验）。
	// 官方对齐：四前缀中的 /v1 由上方 gateway 组（/v1 前缀）承载，此处补 Ark SDK 原生
	// /api/v3、/v3 与裸路径三前缀，避免与组内注册重复。
	for _, prefix := range []string{"/api/v3", "/v3", ""} {
		r.POST(prefix+"/contents/generations/tasks", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedSeedance, h.OpenAIGateway.SeedanceTasks)
		r.GET(prefix+"/contents/generations/tasks/:task_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.OpenAIGateway.SeedanceTasks)
		r.DELETE(prefix+"/contents/generations/tasks/:task_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.OpenAIGateway.SeedanceTasks)
	}
	voiceHandler := func(endpoint string) gin.HandlerFunc {
		return func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, endpoint) }
	}
	r.POST("/tts", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, voiceHandler("tts"))
	r.POST("/stt", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedAudio, voiceHandler("stt"))
	r.POST("/custom-voices", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedAudio, voiceHandler("custom-voices"))
	r.GET("/custom-voices", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, voiceHandler("custom-voices"))
	customVoiceHandler := func(c *gin.Context) { h.OpenAIGateway.GrokVoice(c, grokCustomVoiceEndpoint(c)) }
	r.GET("/custom-voices/:voice_id/audio", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, customVoiceHandler)
	r.GET("/custom-voices/:voice_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, customVoiceHandler)
	r.PATCH("/custom-voices/:voice_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedAudio, customVoiceHandler)
	r.DELETE("/custom-voices/:voice_id", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, customVoiceHandler)
	r.GET("/realtime", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, aiUnsupportedAudio, h.OpenAIGateway.GrokRealtime)
	r.POST("/web_search", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, h.Gateway.WebSearch)
	r.POST("/x_search", bodyLimit, clientRequestID, opsErrorLogger, endpointNorm, gin.HandlerFunc(apiKeyAuth), groupModelAllowlist, compositeTarget, requireGroupAnthropic, func(c *gin.Context) {
		if getGroupPlatform(c) != service.PlatformGrok {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "not_found_error", "message": "X Search API is not supported for this platform"}})
			return
		}
		h.Gateway.XSearch(c)
	})

	// Antigravity 模型列表
	r.GET("/antigravity/models", gin.HandlerFunc(apiKeyAuth), requireGroupAnthropic, h.Gateway.AntigravityModels)

	// Antigravity 专用路由（仅使用 antigravity 账户，不混合调度）
	antigravityV1 := r.Group("/antigravity/v1")
	antigravityV1.Use(bodyLimit)
	antigravityV1.Use(clientRequestID)
	antigravityV1.Use(opsErrorLogger)
	antigravityV1.Use(endpointNorm)
	antigravityV1.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1.Use(gin.HandlerFunc(apiKeyAuth))
	antigravityV1.Use(groupModelAllowlist)
	antigravityV1.Use(requireGroupAnthropic)
	{
		antigravityV1.POST("/messages", h.Gateway.Messages)
		antigravityV1.POST("/messages/count_tokens", h.Gateway.CountTokens)
		antigravityV1.GET("/models", h.Gateway.AntigravityModels)
		antigravityV1.GET("/usage", h.Gateway.Usage)
	}

	antigravityV1Beta := r.Group("/antigravity/v1beta")
	antigravityV1Beta.Use(bodyLimit)
	antigravityV1Beta.Use(clientRequestID)
	antigravityV1Beta.Use(opsErrorLogger)
	antigravityV1Beta.Use(endpointNorm)
	antigravityV1Beta.Use(middleware.ForcePlatform(service.PlatformAntigravity))
	antigravityV1Beta.Use(googleAPIKeyAuth)
	antigravityV1Beta.Use(groupModelAllowlist)
	antigravityV1Beta.Use(requireGroupGoogle)
	{
		antigravityV1Beta.GET("/models", h.Gateway.GeminiV1BetaListModels)
		antigravityV1Beta.GET("/models/:model", h.Gateway.GeminiV1BetaGetModel)
		antigravityV1Beta.POST("/models/*modelAction", h.Gateway.GeminiV1BetaModels)
	}

}

func grokCustomVoiceEndpoint(c *gin.Context) string {
	endpoint := "custom-voices/" + c.Param("voice_id")
	if strings.HasSuffix(c.FullPath(), "/:voice_id/audio") {
		endpoint += "/audio"
	}
	return endpoint
}

// getGroupPlatform extracts the group platform from the API Key stored in context.
func getGroupPlatform(c *gin.Context) string {
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey.Group == nil {
		return ""
	}
	if apiKey.Group.Platform == service.PlatformComposite && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform
		}
	}
	return apiKey.Group.Platform
}

func isOpenAICompatibleGatewayPlatform(platform string) bool {
	return platform == service.PlatformOpenAI || platform == service.PlatformGrok || service.IsCNProvider(platform)
}

// compositeTargetPlatformMiddleware 在鉴权后只为聚合分组读取一次请求体，
// 固化本次请求的目标平台和上游模型。普通分组立即跳过，不增加热路径分配。
func compositeTargetPlatformMiddleware(resolver *service.CompositeRouteResolver, enabled ...bool) gin.HandlerFunc {
	featureEnabled := len(enabled) == 0 || enabled[0]
	return compositeTargetPlatformMiddlewareWithGate(resolver, func() bool { return featureEnabled })
}

func compositeTargetPlatformMiddlewareWithGate(resolver *service.CompositeRouteResolver, enabled func() bool) gin.HandlerFunc {
	if enabled == nil {
		enabled = func() bool { return false }
	}
	if resolver == nil {
		resolver = service.NewCompositeRouteResolver(nil)
	}
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
			// 当前处理器没有后置逻辑，直接返回即可让 Gin 外层处理链继续执行；
			// 避免普通分组为聚合能力多一次递归 Next 调用。
			return
		}
		if !enabled() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "server_error", "message": "Composite groups are disabled on this gateway"}})
			c.Abort()
			return
		}
		// WebSocket（网络长连接）要等第一帧才知道模型；把只读解析器交给处理器，
		// 使显式别名与普通 HTTP 请求使用同一份快照和优先级规则。
		c.Set(service.CompositeRouteResolverGinContextKey, resolver)
		if c.Request == nil || c.Request.Method == http.MethodGet {
			return
		}

		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			status := http.StatusBadRequest
			message := "Failed to read request body"
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				status = http.StatusRequestEntityTooLarge
				message = "Request body is too large"
			}
			c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
			c.Abort()
			return
		}

		model := compositeRequestModelFromBody(c.GetHeader("Content-Type"), body)
		if model != "" {
			decision, resolveErr := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, compositeRouteEndpointForPath(c.Request.URL.Path))
			if resolveErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": "Failed to resolve composite model route"}})
				c.Abort()
				return
			}
			if !decision.Matched {
				c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Model is not supported by composite groups"}})
				c.Abort()
				return
			}
			logCompositeRouteDecision(decision)
			c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
			if upstreamModel := strings.TrimSpace(decision.UpstreamModel); upstreamModel != "" && upstreamModel != model && gjson.ValidBytes(body) {
				if _, modelPath := compositeJSONRequestModel(body); modelPath != "" {
					if rewritten, rewriteErr := sjson.SetBytes(body, modelPath, upstreamModel); rewriteErr == nil {
						body = rewritten
					}
				}
			}
		}
		resetRequestBody(c, body)
	}
}

func compositeRequestModelFromBody(contentType string, body []byte) string {
	if model, _ := compositeJSONRequestModel(body); model != "" {
		return model
	}
	return compositeMultipartModelFromBody(contentType, body)
}

func compositeJSONRequestModel(body []byte) (string, string) {
	for _, path := range []string{"model", "session.model"} {
		model := gjson.GetBytes(body, path)
		if model.Type != gjson.String {
			continue
		}
		if value := strings.TrimSpace(model.String()); value != "" {
			return value, path
		}
	}
	return "", ""
}

func compositeMultipartModelFromBody(contentType string, body []byte) string {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return ""
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			return ""
		}
		fieldName := part.FormName()
		if part.FileName() != "" || (fieldName != "model" && fieldName != "session") {
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return ""
		}
		switch fieldName {
		case "model":
			return strings.TrimSpace(string(data))
		case "session":
			if model, _ := compositeJSONRequestModel(data); model != "" {
				return model
			}
		}
	}
}

func compositeGeminiTargetPlatformMiddleware(resolver *service.CompositeRouteResolver, enabled ...bool) gin.HandlerFunc {
	featureEnabled := len(enabled) == 0 || enabled[0]
	return compositeGeminiTargetPlatformMiddlewareWithGate(resolver, func() bool { return featureEnabled })
}

func compositeGeminiTargetPlatformMiddlewareWithGate(resolver *service.CompositeRouteResolver, enabled func() bool) gin.HandlerFunc {
	if enabled == nil {
		enabled = func() bool { return false }
	}
	if resolver == nil {
		resolver = service.NewCompositeRouteResolver(nil)
	}
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if ok && apiKey != nil && apiKey.Group != nil && apiKey.Group.Platform == service.PlatformComposite {
			if !enabled() {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "server_error", "message": "Composite groups are disabled on this gateway"}})
				c.Abort()
				return
			}
			model := compositeGeminiModelFromParams(c)
			if model != "" {
				decision, err := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, service.CompositeRouteEndpointGemini)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "server_error", "message": "Failed to resolve composite model route"}})
					c.Abort()
					return
				}
				if !decision.Matched || decision.TargetPlatform != service.PlatformGemini {
					c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Model is not supported by the Gemini endpoint for composite groups"}})
					c.Abort()
					return
				}
				logCompositeRouteDecision(decision)
				c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				if upstream := strings.TrimSpace(decision.UpstreamModel); upstream != "" && upstream != model {
					rewriteCompositeGeminiModelParam(c, upstream)
				}
			}
		}
	}
}

func compositeGroupsRuntimeGate(settingService *service.SettingService, cfg *config.Config) func() bool {
	fallback := cfg != nil && cfg.GatewayLite.CompositeGroupsEnabled
	if settingService == nil {
		return func() bool { return fallback }
	}
	return settingService.CompositeGroupsEnabled
}

// grokVideoTransformsRuntimeGate 解析 Grok 视频编辑/扩展开关。settingService 可用时读
// 数据库热开关的内存快照（保存即生效，不需要重启）；服务缺失时回退启动配置，保持
// 未配置数据库的部署与既有测试行为不变。
func grokVideoTransformsRuntimeGate(settingService *service.SettingService, cfg *config.Config) func() bool {
	fallback := cfg != nil && cfg.Gateway.GrokVideoTransformsEnabled
	if settingService == nil {
		return func() bool { return fallback }
	}
	return settingService.GrokVideoTransformsEnabled
}

func logCompositeRouteDecision(decision service.CompositeRouteDecision) {
	routeID := int64(0)
	if decision.Route != nil {
		routeID = decision.Route.ID
	}
	// 只记录路由事实，不记录账号凭据、API Key 或 OAuth Token。
	slog.Debug("composite route resolved",
		"group_id", decision.GroupID,
		"public_model", decision.PublicModel,
		"target_platform", decision.TargetPlatform,
		"upstream_model", decision.UpstreamModel,
		"route_source", decision.Source,
		"route_id", routeID,
	)
}

func compositeGeminiModelFromParams(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if model := strings.TrimSpace(c.Param("model")); model != "" {
		return model
	}
	modelAction := strings.TrimPrefix(strings.TrimSpace(c.Param("modelAction")), "/")
	if modelAction == "" {
		return ""
	}
	if idx := strings.LastIndex(modelAction, ":"); idx >= 0 {
		return strings.TrimSpace(modelAction[:idx])
	}
	return modelAction
}

func rewriteCompositeGeminiModelParam(c *gin.Context, upstream string) {
	// 上游模型只能是一个模型标识；拒绝路径控制字符，避免规则配置变成路径注入。
	if c == nil || upstream == "" || strings.ContainsAny(upstream, "\\?#\r\n") || strings.Contains(upstream, "..") {
		return
	}
	for i := range c.Params {
		switch c.Params[i].Key {
		case "model":
			c.Params[i].Value = upstream
		case "modelAction":
			value := strings.TrimPrefix(c.Params[i].Value, "/")
			action := ""
			if idx := strings.LastIndex(value, ":"); idx >= 0 {
				action = value[idx:]
			}
			c.Params[i].Value = "/" + upstream + action
		}
	}
}

func resetRequestBody(c *gin.Context, body []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func compositeRouteEndpointForPath(path string) string {
	switch {
	case strings.Contains(path, "/messages/count_tokens"):
		return service.CompositeRouteEndpointCountTokens
	case strings.Contains(path, "/messages"):
		return service.CompositeRouteEndpointMessages
	case strings.Contains(path, "/responses"),
		strings.Contains(path, "/alpha/search"),
		strings.Contains(path, "/realtime/calls"),
		strings.HasSuffix(strings.TrimRight(path, "/"), "/live"):
		return service.CompositeRouteEndpointResponses
	case strings.Contains(path, "/chat/completions"):
		return service.CompositeRouteEndpointChatCompletions
	case strings.Contains(path, "/embeddings"):
		return service.CompositeRouteEndpointEmbeddings
	case strings.Contains(path, "/images/"):
		return service.CompositeRouteEndpointImages
	case strings.Contains(path, "/v1beta/"):
		return service.CompositeRouteEndpointGemini
	default:
		return service.CompositeRouteEndpointAny
	}
}
