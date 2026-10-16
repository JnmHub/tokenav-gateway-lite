// Package routes provides HTTP route registration and handlers.
package routes

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterAdminRoutes 注册管理员路由
func RegisterAdminRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	adminAuth middleware.AdminAuthMiddleware,
	operationAudit middleware.OperationAuditMiddleware,
	stepUpAuth *middleware.StepUpAuthMiddleware,
	settingService *service.SettingService,
) {
	admin := v1.Group("/admin")
	admin.Use(gin.HandlerFunc(adminAuth))
	admin.Use(middleware.AdminComplianceGuard(settingService))
	if operationAudit != nil {
		admin.Use(gin.HandlerFunc(operationAudit))
	}
	// 伪造流式首字（总开关）：standard 模式同样注册（保存即热生效）。
	if settingService != nil {
		fakeFT := adminhandler.NewFakeFirstTokenHandler(settingService)
		admin.GET("/ops/runtime/fake-first-token", fakeFT.Get)
		admin.PUT("/ops/runtime/fake-first-token", fakeFT.Update)
	}
	{
		// 部署与运营合规确认
		registerAdminComplianceRoutes(admin, h)

		// 仪表盘
		registerDashboardRoutes(admin, h)

		// 用户管理
		registerUserManagementRoutes(admin, h)

		// 分组管理
		registerGroupRoutes(admin, h)

		// 账号管理
		registerAccountRoutes(admin, h, stepUpAuth)

		// 公告管理
		registerAnnouncementRoutes(admin, h)

		// OpenAI OAuth
		registerOpenAIOAuthRoutes(admin, h)

		// Gemini OAuth
		registerGeminiOAuthRoutes(admin, h)

		// Antigravity OAuth
		registerAntigravityOAuthRoutes(admin, h)

		// Grok OAuth
		registerGrokOAuthRoutes(admin, h)

		// 国产供应商额度与余额
		registerCNProviderRoutes(admin, h)

		// 代理管理
		registerProxyRoutes(admin, h, stepUpAuth)

		// 卡密管理
		registerRedeemCodeRoutes(admin, h)

		// 优惠码管理
		registerPromoCodeRoutes(admin, h)

		// 系统设置
		registerSettingsRoutes(admin, h)

		// 数据管理
		registerDataManagementRoutes(admin, h, stepUpAuth)

		// 数据库备份恢复
		registerBackupRoutes(admin, h, stepUpAuth)

		registerOperationAuditRoutes(admin, h, stepUpAuth)

		// 运维监控（Ops）
		registerOpsRoutes(admin, h)

		// 插件系统管理（官方 v0.2.5 对齐）
		registerPluginRoutes(admin, h, stepUpAuth)

		// 系统管理
		registerSystemRoutes(admin, h)

		// 订阅管理
		registerSubscriptionRoutes(admin, h)

		// 使用记录管理
		registerUsageRoutes(admin, h)

		// 用户属性管理
		registerUserAttributeRoutes(admin, h)

		// 错误透传规则管理
		registerErrorPassthroughRoutes(admin, h)

		// TLS 指纹模板管理
		registerTLSFingerprintProfileRoutes(admin, h)

		// API Key 管理
		registerAdminAPIKeyRoutes(admin, h)

		// 定时测试计划
		registerScheduledTestRoutes(admin, h)

		// 渠道管理
		registerChannelRoutes(admin, h)

		// 渠道监控
		registerChannelMonitorRoutes(admin, h)
		registerChannelMonitorV2Routes(admin, h, settingService)

		// 风控中心
		registerContentModerationRoutes(admin, h)

		// 邀请返利（专属用户管理）
		registerAffiliateRoutes(admin, h)
		registerImageTaskRoutes(admin, h)
	}
}

// RegisterGatewayLiteAdminRoutes 注册 gateway-lite（轻量网关）运维后台路由。
// 只保留管理区域网关实际需要的账号池、调度、统计、渠道和安全转发配置；
// 用户自助、支付、卡密、优惠、邀请返利等主站客户侧功能不在这里注册。
func RegisterGatewayLiteAdminRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	adminAuth middleware.AdminAuthMiddleware,
	operationAudit middleware.OperationAuditMiddleware,
	stepUpAuth *middleware.StepUpAuthMiddleware,
	settingService *service.SettingService,
	apiKeyService *service.APIKeyService,
	cfg *config.Config,
) {
	liteConfigHandler := adminhandler.NewGatewayLiteConfigHandler(cfg, settingService)
	liteConfigHandler.SetBillingCacheService(h.BillingCacheService())
	if h != nil && h.ImageTask != nil {
		liteConfigHandler.SetImageTaskRuntimeState(h.ImageTask.RuntimeEnabled(), h.ImageTask.ActiveTaskCount)
	}
	v1.POST("/gateway-lite/sync", liteConfigHandler.SyncNow)

	// IPv6 出口管理：配置热更新（免重启）与手动重扫。
	if h.IPv6Egress != nil {
		v1.GET("/admin/ipv6-egress", gatewayLiteAdminAuth(adminAuth, cfg), h.IPv6Egress.GetStatus)
		v1.PUT("/admin/ipv6-egress", gatewayLiteAdminAuth(adminAuth, cfg), h.IPv6Egress.UpdateConfig)
		v1.POST("/admin/ipv6-egress/rescan", gatewayLiteAdminAuth(adminAuth, cfg), h.IPv6Egress.Rescan)
	}
	// 伪造流式首字（总开关）：保存即热生效；两种运行模式共用。
	if settingService != nil {
		fakeFT := adminhandler.NewFakeFirstTokenHandler(settingService)
		v1.GET("/admin/ops/runtime/fake-first-token", gatewayLiteAdminAuth(adminAuth, cfg), fakeFT.Get)
		v1.PUT("/admin/ops/runtime/fake-first-token", gatewayLiteAdminAuth(adminAuth, cfg), fakeFT.Update)
	}

	admin := v1.Group("/admin")
	admin.Use(gatewayLiteAdminAuth(adminAuth, cfg))
	// 独立保护后台聚合查询；该中间件只挂载 /api/v1/admin，绝不进入客户 /v1/* 热路径。
	admin.Use(middleware.NewGatewayLitePanelRateLimiterWithSettings(settingService).Handler())
	if operationAudit != nil {
		admin.Use(gin.HandlerFunc(operationAudit))
	}
	// gateway-lite（轻量网关）只作为区域运维后台使用，不启用完整主站的管理员合规确认拦截。
	{
		registerAdminComplianceRoutes(admin, h)
		registerDashboardRoutes(admin, h)
		registerGroupRoutes(admin, h)
		registerAccountRoutes(admin, h, stepUpAuth)
		registerOpenAIOAuthRoutes(admin, h)
		registerGeminiOAuthRoutes(admin, h)
		registerAntigravityOAuthRoutes(admin, h)
		registerGrokOAuthRoutes(admin, h)
		registerCNProviderRoutes(admin, h)
		registerProxyRoutes(admin, h, stepUpAuth)
		registerSettingsRoutes(admin, h)
		registerGatewayLiteConfigRoutes(admin, liteConfigHandler)
		binding := admin.Group("/gateway-binding")
		bindingHandler := adminhandler.NewGatewayBindingHandler()
		binding.GET("/status", bindingHandler.Status)
		binding.POST("/probe", bindingHandler.Probe)
		binding.POST("/verify", bindingHandler.Verify)
		binding.POST("/register", bindingHandler.Register)
		binding.POST("/disconnect-check", bindingHandler.DisconnectCheck)
		binding.POST("/disconnect", bindingHandler.Disconnect)
		binding.POST("/cancel", bindingHandler.Cancel)
		binding.POST("/retry", bindingHandler.Retry)
		registerGatewayLiteImageTaskConfigRoutes(admin, liteConfigHandler)
		// gateway-lite only exposes the controlled restart needed to apply
		// process-lifetime configuration. Binary update/rollback remain absent.
		admin.POST("/system/restart", h.Admin.System.RestartService)
		if apiKeyService != nil {
			registerGatewayLiteLocalKeyRoutes(admin, adminhandler.NewGatewayLiteLocalKeyHandler(apiKeyService))
		}
		registerOpsRoutes(admin, h)
		registerGatewayLiteRuntimeRoutes(admin, h)
		// 插件系统管理（官方 v0.2.5 对齐）
		registerPluginRoutes(admin, h, stepUpAuth)
		registerGatewayLiteBillingCacheRuntimeRoutes(admin, liteConfigHandler)
		registerGatewayLiteRuntimeSummaryRoute(admin, h, cfg, settingService)
		registerUsageRoutes(admin, h)
		registerErrorPassthroughRoutes(admin, h)
		registerTLSFingerprintProfileRoutes(admin, h)
		registerScheduledTestRoutes(admin, h)
		registerChannelRoutes(admin, h)
		registerChannelMonitorRoutes(admin, h)
		registerChannelMonitorV2Routes(admin, h, settingService)
		registerContentModerationRoutes(admin, h)
		registerOperationAuditRoutes(admin, h, stepUpAuth)
		registerImageTaskRoutes(admin, h)
	}
}

func registerImageTaskRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.ImageTask == nil {
		return
	}
	imageTasks := admin.Group("/image-tasks")
	{
		imageTasks.GET("", h.ImageTask.AdminList)
		imageTasks.GET("/capabilities", h.ImageTask.AdminCapabilities)
		imageTasks.GET("/metrics", h.ImageTask.AdminMetrics)
		imageTasks.POST("/cleanup", h.ImageTask.AdminCleanup)
		imageTasks.GET("/:id", h.ImageTask.AdminGet)
		imageTasks.POST("/:id/cancel", h.ImageTask.AdminCancel)
		imageTasks.GET("/:id/artifacts/:index", h.ImageTask.AdminArtifact)
		imageTasks.POST("/:id/replay", h.ImageTask.AdminReplay)
	}
}

func registerGatewayLiteLocalKeyRoutes(admin *gin.RouterGroup, handler *adminhandler.GatewayLiteLocalKeyHandler) {
	localKeys := admin.Group("/gateway-lite/local-api-keys")
	{
		localKeys.GET("", handler.List)
		localKeys.POST("", handler.Create)
		localKeys.POST("/:id/reveal", handler.Reveal)
		localKeys.PUT("/:id", handler.Update)
		localKeys.DELETE("/:id", handler.Delete)
	}
}

func gatewayLiteAdminAuth(adminAuth middleware.AdminAuthMiddleware, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if validGatewayLiteAdminKeyForAccountProxy(c, cfg) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{})
			c.Set(string(middleware.ContextKeyUserRole), service.RoleAdmin)
			c.Set("auth_method", "gateway_lite_admin_key")
			c.Next()
			return
		}
		gin.HandlerFunc(adminAuth)(c)
	}
}

func validGatewayLiteAdminKeyForAccountProxy(c *gin.Context, cfg *config.Config) bool {
	if c == nil || c.Request == nil || cfg == nil {
		return false
	}
	if !gatewayLiteAdminKeyAccountProxyPathAllowed(c.Request.Method, c.Request.URL.Path) {
		return false
	}
	expected := strings.TrimSpace(cfg.GatewayLite.AdminSyncKey)
	if expected == "" {
		return false
	}
	incoming := strings.TrimSpace(c.GetHeader("X-Gateway-Lite-Admin-Key"))
	if incoming == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(incoming), []byte(expected)) == 1
}

func gatewayLiteAdminKeyAccountProxyPathAllowed(method, path string) bool {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "admin" {
		return false
	}
	// The control plane may manage one user's image tasks through the same
	// sync key. Handlers still require a positive user_id and keep ownership in
	// the SQL predicate. Replay, cleanup and metrics remain local-admin only.
	if parts[3] == "image-tasks" {
		switch {
		case len(parts) == 4:
			return method == http.MethodGet
		case len(parts) == 5:
			return method == http.MethodGet && parts[4] != "metrics"
		case len(parts) == 6:
			return method == http.MethodPost && parts[5] == "cancel"
		case len(parts) == 7:
			return method == http.MethodGet && parts[5] == "artifacts" && parts[6] != ""
		default:
			return false
		}
	}
	if parts[3] != "accounts" {
		return false
	}
	if len(parts) == 4 {
		return method == http.MethodGet || method == http.MethodPost
	}
	if len(parts) == 5 {
		return method == http.MethodPut || method == http.MethodDelete
	}
	if len(parts) == 6 && method == http.MethodPost {
		return parts[5] == "test"
	}
	if len(parts) == 7 && method == http.MethodPost {
		return parts[5] == "models" && parts[6] == "sync-upstream"
	}
	return false
}

func registerGatewayLiteConfigRoutes(admin *gin.RouterGroup, handler *adminhandler.GatewayLiteConfigHandler) {
	gatewayLite := admin.Group("/gateway-lite")
	{
		gatewayLite.GET("/config", handler.GetConfig)
		gatewayLite.PUT("/config", handler.UpdateConfig)
	}
}

func registerGatewayLiteImageTaskConfigRoutes(admin *gin.RouterGroup, handler *adminhandler.GatewayLiteConfigHandler) {
	if handler == nil {
		return
	}
	imageTasks := admin.Group("/image-tasks")
	imageTasks.GET("/config", handler.GetImageTaskConfig)
	imageTasks.PUT("/config", handler.UpdateImageTaskConfig)
}

func registerAdminComplianceRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	compliance := admin.Group("/compliance")
	{
		compliance.GET("", h.Admin.Compliance.GetStatus)
		compliance.POST("/accept", h.Admin.Compliance.Accept)
	}
}

func registerContentModerationRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	risk := admin.Group("/risk-control")
	{
		risk.GET("/config", h.Admin.ContentModeration.GetConfig)
		risk.PUT("/config", h.Admin.ContentModeration.UpdateConfig)
		risk.POST("/api-keys/test", h.Admin.ContentModeration.TestAPIKeys)
		risk.GET("/status", h.Admin.ContentModeration.GetStatus)
		risk.POST("/ai/test", h.Admin.ContentModeration.TestAI)
		risk.GET("/ai/metrics", h.Admin.ContentModeration.GetAIMetrics)
		risk.GET("/keyword-catalog/status", h.Admin.ContentModeration.GetKeywordCatalogStatus)
		risk.POST("/keyword-catalog/sync", h.Admin.ContentModeration.SyncKeywordCatalog)
			risk.GET("/logs", h.Admin.ContentModeration.ListLogs)
			risk.GET("/logs/:id", h.Admin.ContentModeration.GetLog)

		risk.POST("/users/:user_id/unban", h.Admin.ContentModeration.UnbanUser)
		risk.DELETE("/hashes", h.Admin.ContentModeration.DeleteFlaggedHash)
		risk.DELETE("/hashes/all", h.Admin.ContentModeration.ClearFlaggedHashes)
	}
}

func registerAdminAPIKeyRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	apiKeys := admin.Group("/api-keys")
	{
		apiKeys.PUT("/:id", h.Admin.APIKey.UpdateGroup)
	}
}

func registerOpsRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	ops := admin.Group("/ops")
	{
		// Realtime ops signals
		ops.GET("/concurrency", h.Admin.Ops.GetConcurrencyStats)
		ops.GET("/user-concurrency", h.Admin.Ops.GetUserConcurrencyStats)
		ops.GET("/account-availability", h.Admin.Ops.GetAccountAvailability)
		ops.GET("/realtime-traffic", h.Admin.Ops.GetRealtimeTrafficSummary)

		// Alerts (rules + events)
		ops.GET("/alert-rules", h.Admin.Ops.ListAlertRules)
		ops.POST("/alert-rules", h.Admin.Ops.CreateAlertRule)
		ops.PUT("/alert-rules/:id", h.Admin.Ops.UpdateAlertRule)
		ops.DELETE("/alert-rules/:id", h.Admin.Ops.DeleteAlertRule)
		ops.GET("/alert-events", h.Admin.Ops.ListAlertEvents)
		ops.GET("/alert-events/:id", h.Admin.Ops.GetAlertEvent)
		ops.PUT("/alert-events/:id/status", h.Admin.Ops.UpdateAlertEventStatus)
		ops.POST("/alert-silences", h.Admin.Ops.CreateAlertSilence)

		// Email notification config (DB-backed)
		ops.GET("/email-notification/config", h.Admin.Ops.GetEmailNotificationConfig)
		ops.PUT("/email-notification/config", h.Admin.Ops.UpdateEmailNotificationConfig)

		// Runtime settings (DB-backed)
		runtime := ops.Group("/runtime")
		{
			runtime.GET("/alert", h.Admin.Ops.GetAlertRuntimeSettings)
			runtime.PUT("/alert", h.Admin.Ops.UpdateAlertRuntimeSettings)
			runtime.GET("/logging", h.Admin.Ops.GetRuntimeLogConfig)
			runtime.PUT("/logging", h.Admin.Ops.UpdateRuntimeLogConfig)
			runtime.POST("/logging/reset", h.Admin.Ops.ResetRuntimeLogConfig)
			runtime.GET("/request-debug", h.Admin.Ops.GetRequestDebug)
			runtime.PUT("/request-debug", h.Admin.Ops.UpdateRequestDebug)
			runtime.GET("/request-debug/records", h.Admin.Ops.ListRequestDebugRecords)
			runtime.DELETE("/request-debug/records", h.Admin.Ops.ClearRequestDebugRecords)
			runtime.GET("/request-debug/full-config", h.Admin.Ops.GetFullRequestDebug)
			runtime.PUT("/request-debug/full-config", h.Admin.Ops.UpdateFullRequestDebug)
			runtime.GET("/request-debug/full-records", h.Admin.Ops.ListFullRequestDebugRecords)
			runtime.DELETE("/request-debug/full-records", h.Admin.Ops.ClearFullRequestDebugRecords)
			runtime.POST("/request-debug/full-config/disable", h.Admin.Ops.DisableFullRequestDebug)
		}

		// Advanced settings (DB-backed)
		ops.GET("/advanced-settings", h.Admin.Ops.GetAdvancedSettings)
		ops.PUT("/advanced-settings", h.Admin.Ops.UpdateAdvancedSettings)
		// Tail-Burst is also managed from the main system-settings page. Keep its
		// narrow endpoint usable when the Ops monitoring master switch is off.
		ops.GET("/codex-tail-burst", h.Admin.Ops.GetCodexTailBurstSettings)
		ops.PUT("/codex-tail-burst", h.Admin.Ops.UpdateCodexTailBurstSettings)
		ops.GET("/codex-tail-burst/stats", h.Admin.Ops.GetCodexTailBurstStats)

		// Settings group (DB-backed)
		settings := ops.Group("/settings")
		{
			settings.GET("/metric-thresholds", h.Admin.Ops.GetMetricThresholds)
			settings.PUT("/metric-thresholds", h.Admin.Ops.UpdateMetricThresholds)
		}

		// WebSocket realtime (QPS/TPS)
		ws := ops.Group("/ws")
		{
			ws.GET("/qps", h.Admin.Ops.QPSWSHandler)
		}

		// Error logs (legacy)
		ops.GET("/errors", h.Admin.Ops.GetErrorLogs)
		ops.GET("/errors/:id", h.Admin.Ops.GetErrorLogByID)
		ops.PUT("/errors/:id/resolve", h.Admin.Ops.UpdateErrorResolution)

		// Request errors (client-visible failures)
		ops.GET("/request-errors", h.Admin.Ops.ListRequestErrors)
		ops.GET("/request-errors/:id", h.Admin.Ops.GetRequestError)
		ops.GET("/request-errors/:id/upstream-errors", h.Admin.Ops.ListRequestErrorUpstreamErrors)
		ops.PUT("/request-errors/:id/resolve", h.Admin.Ops.ResolveRequestError)

		// Upstream errors (independent upstream failures)
		ops.GET("/upstream-errors", h.Admin.Ops.ListUpstreamErrors)
		ops.GET("/upstream-errors/:id", h.Admin.Ops.GetUpstreamError)
		ops.PUT("/upstream-errors/:id/resolve", h.Admin.Ops.ResolveUpstreamError)

		// Request drilldown (success + error)
		ops.GET("/requests", h.Admin.Ops.ListRequestDetails)

		// Indexed system logs
		ops.GET("/system-logs", h.Admin.Ops.ListSystemLogs)
		ops.POST("/system-logs/cleanup", h.Admin.Ops.CleanupSystemLogs)
		ops.GET("/system-logs/health", h.Admin.Ops.GetSystemLogIngestionHealth)

		// Dashboard (vNext - raw path for MVP)
		ops.GET("/dashboard/snapshot-v2", h.Admin.Ops.GetDashboardSnapshotV2)
		ops.GET("/dashboard/overview", h.Admin.Ops.GetDashboardOverview)
		ops.GET("/dashboard/throughput-trend", h.Admin.Ops.GetDashboardThroughputTrend)
		ops.GET("/dashboard/latency-histogram", h.Admin.Ops.GetDashboardLatencyHistogram)
		ops.GET("/dashboard/error-trend", h.Admin.Ops.GetDashboardErrorTrend)
		ops.GET("/dashboard/error-distribution", h.Admin.Ops.GetDashboardErrorDistribution)
		ops.GET("/dashboard/openai-token-stats", h.Admin.Ops.GetDashboardOpenAITokenStats)

		// 入口拒绝统计（官方 v0.2.5 对齐）：聚合查询 + 采集健康状态
		ops.GET("/ingress-rejects", h.Admin.Ops.ListIngressRejects)
		ops.GET("/ingress-rejects/health", h.Admin.Ops.GetIngressRejectHealth)
	}
}

// registerGatewayLiteRuntimeRoutes 只在区域网关后台开放；完整模式没有
// gateway-lite panel limiter（后台限流器），不能展示一份实际不生效的配置。
func registerGatewayLiteRuntimeRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	ops := admin.Group("/ops")
	ops.GET("/ingress-rejections", h.Admin.Setting.GetIngressRejectionSnapshot)
	runtime := ops.Group("/runtime")
	runtime.GET("/panel-rate-limit", h.Admin.Setting.GetPanelRateLimitSettings)
	runtime.PUT("/panel-rate-limit", h.Admin.Setting.UpdatePanelRateLimitSettings)
	runtime.POST("/panel-rate-limit/reset", h.Admin.Setting.ResetPanelRateLimitSettings)
	runtime.GET("/openai-scheduler", h.Admin.Setting.GetOpenAISchedulerRuntimePolicy)
	runtime.PUT("/openai-scheduler", h.Admin.Setting.UpdateOpenAISchedulerRuntimePolicy)
	runtime.POST("/openai-scheduler/reset", h.Admin.Setting.ResetOpenAISchedulerRuntimePolicy)
	runtime.GET("/server-timing", h.Admin.Ops.GetServerTimingRuntime)
	runtime.POST("/server-timing/enable", h.Admin.Ops.EnableServerTimingTemporary)
	runtime.POST("/server-timing/disable", h.Admin.Ops.DisableServerTimingTemporary)
}

func registerGatewayLiteBillingCacheRuntimeRoutes(admin *gin.RouterGroup, h *adminhandler.GatewayLiteConfigHandler) {
	if h == nil {
		return
	}
	runtime := admin.Group("/ops/runtime")
	runtime.GET("/billing-cache", h.GetBillingCacheRuntimeConfig)
	runtime.PUT("/billing-cache", h.UpdateBillingCacheRuntimeConfig)
}

func registerDashboardRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	dashboard := admin.Group("/dashboard")
	{
		dashboard.GET("/snapshot-v2", h.Admin.Dashboard.GetSnapshotV2)
		dashboard.GET("/stats", h.Admin.Dashboard.GetStats)
		dashboard.GET("/realtime", h.Admin.Dashboard.GetRealtimeMetrics)
		dashboard.GET("/trend", h.Admin.Dashboard.GetUsageTrend)
		dashboard.GET("/models", h.Admin.Dashboard.GetModelStats)
		dashboard.GET("/groups", h.Admin.Dashboard.GetGroupStats)
		dashboard.GET("/api-keys-trend", h.Admin.Dashboard.GetAPIKeyUsageTrend)
		dashboard.GET("/users-trend", h.Admin.Dashboard.GetUserUsageTrend)
		dashboard.GET("/users-ranking", h.Admin.Dashboard.GetUserSpendingRanking)
		dashboard.POST("/users-usage", h.Admin.Dashboard.GetBatchUsersUsage)
		dashboard.POST("/api-keys-usage", h.Admin.Dashboard.GetBatchAPIKeysUsage)
		dashboard.GET("/user-breakdown", h.Admin.Dashboard.GetUserBreakdown)
		dashboard.POST("/aggregation/backfill", h.Admin.Dashboard.BackfillAggregation)
	}
}

func registerUserManagementRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	users := admin.Group("/users")
	{
		users.GET("", h.Admin.User.List)
		users.GET("/:id", h.Admin.User.GetByID)
		users.POST("/:id/auth-identities", h.Admin.User.BindAuthIdentity)
		users.POST("", h.Admin.User.Create)
		users.PUT("/:id", h.Admin.User.Update)
		users.DELETE("/:id", h.Admin.User.Delete)
		users.POST("/:id/balance", h.Admin.User.UpdateBalance)
		users.GET("/:id/api-keys", h.Admin.User.GetUserAPIKeys)
		users.GET("/:id/usage", h.Admin.User.GetUserUsage)
		users.GET("/:id/balance-history", h.Admin.User.GetBalanceHistory)
		users.POST("/:id/replace-group", h.Admin.User.ReplaceGroup)
		users.GET("/:id/rpm-status", h.Admin.User.GetUserRPMStatus)
		users.POST("/batch-concurrency", h.Admin.User.BatchUpdateConcurrency)
		users.GET("/:id/platform-quotas", h.Admin.User.GetUserPlatformQuotas)
		users.PUT("/:id/platform-quotas", h.Admin.User.UpdateUserPlatformQuotas)
		users.POST("/:id/platform-quotas/reset", h.Admin.User.ResetUserPlatformQuotaWindow)

		// User attribute values
		users.GET("/:id/attributes", h.Admin.UserAttribute.GetUserAttributes)
		users.PUT("/:id/attributes", h.Admin.UserAttribute.UpdateUserAttributes)
	}
}

func registerGroupRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	groups := admin.Group("/groups")
	{
		groups.GET("", h.Admin.Group.List)
		groups.GET("/all", h.Admin.Group.GetAll)
		groups.GET("/usage-summary", h.Admin.Group.GetUsageSummary)
		groups.GET("/capacity-summary", h.Admin.Group.GetCapacitySummary)
		groups.PUT("/sort-order", h.Admin.Group.UpdateSortOrder)
		groups.GET("/:id/models-list-candidates", h.Admin.Group.GetModelsListCandidates)
		groups.GET("/:id/effective-models", h.Admin.Group.GetEffectiveModels)
		groups.GET("/:id/pricing", h.Admin.Group.GetPricing)
		groups.PUT("/:id/pricing", h.Admin.Group.UpdatePricing)
		groups.GET("/:id/composite-routes", h.Admin.Group.ListCompositeRoutes)
		groups.POST("/:id/composite-routes", h.Admin.Group.CreateCompositeRoute)
		groups.PUT("/:id/composite-routes/batch-status", h.Admin.Group.BatchSetCompositeRoutesEnabled)
		groups.POST("/:id/composite-routes/preview", h.Admin.Group.PreviewCompositeRoute)
		groups.PUT("/:id/composite-routes/:route_id", h.Admin.Group.UpdateCompositeRoute)
		groups.DELETE("/:id/composite-routes/:route_id", h.Admin.Group.DeleteCompositeRoute)
		groups.GET("/:id", h.Admin.Group.GetByID)
		groups.GET("/:id/model-route-health", h.Admin.Group.GetModelRouteHealth)
		groups.DELETE("/:id/model-route-health", h.Admin.Group.ResetModelRouteHealth)
		groups.POST("", h.Admin.Group.Create)
		groups.POST("/:id/duplicate", h.Admin.Group.Duplicate)
		groups.PUT("/:id", h.Admin.Group.Update)
		groups.DELETE("/:id", h.Admin.Group.Delete)
		groups.GET("/:id/stats", h.Admin.Group.GetStats)
		groups.GET("/:id/rate-multipliers", h.Admin.Group.GetGroupRateMultipliers)
		groups.PUT("/:id/rate-multipliers", h.Admin.Group.BatchSetGroupRateMultipliers)
		groups.DELETE("/:id/rate-multipliers", h.Admin.Group.ClearGroupRateMultipliers)
		groups.PUT("/:id/rpm-overrides", h.Admin.Group.BatchSetGroupRPMOverrides)
		groups.DELETE("/:id/rpm-overrides", h.Admin.Group.ClearGroupRPMOverrides)
		groups.GET("/:id/api-keys", h.Admin.Group.GetGroupAPIKeys)
		groups.POST("/:id/accounts/bind", h.Admin.Group.BindAccounts)
	}
}

func registerAccountRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth *middleware.StepUpAuthMiddleware) {
	accounts := admin.Group("/accounts")
	{
		accounts.GET("", h.Admin.Account.List)
		accounts.GET("/:id/model-health", h.Admin.Account.GetModelHealth)
		accounts.GET("/:id", h.Admin.Account.GetByID)
		accounts.POST("", h.Admin.Account.Create)
		accounts.POST("/check-mixed-channel", h.Admin.Account.CheckMixedChannel)
		accounts.POST("/validate-group-model-mappings", h.Admin.Account.ValidateGroupModelMappings)
		accounts.POST("/import/codex-session", h.Admin.Account.ImportCodexSession)
		accounts.POST("/import/agent-identity", h.Admin.Account.ImportCodexAgentIdentity)
		accounts.POST("/import/personal-access-token", h.Admin.Account.ImportCodexPAT)
		accounts.POST("/sync/crs", h.Admin.Account.SyncFromCRS)
		accounts.POST("/sync/crs/preview", h.Admin.Account.PreviewFromCRS)
		accounts.POST("/:id/duplicate", h.Admin.Account.Duplicate)
		accounts.PUT("/:id", h.Admin.Account.Update)
		accounts.DELETE("/:id", h.Admin.Account.Delete)
		accounts.POST("/:id/test", h.Admin.Account.Test)
		// OpenAI 账号 WS 握手探测：返回 supported/unsupported/unknown 并落账号 extra。
		accounts.POST("/:id/ws-probe", h.Admin.Account.WSSupportProbe)
		accounts.POST("/:id/recover-state", h.Admin.Account.RecoverState)
		accounts.POST("/:id/refresh", h.Admin.Account.Refresh)
		accounts.POST("/:id/apply-oauth-credentials", h.Admin.Account.ApplyOAuthCredentials)
		accounts.POST("/:id/set-privacy", h.Admin.Account.SetPrivacy)
		accounts.POST("/:id/refresh-tier", h.Admin.Account.RefreshTier)
		accounts.GET("/:id/stats", h.Admin.Account.GetStats)
		accounts.POST("/:id/clear-error", h.Admin.Account.ClearError)
		accounts.POST("/:id/revert-proxy-fallback", h.Admin.Account.RevertProxyFallback)
		// Spark 链接型影子账号：创建/查看/主动同步。移除复用通用 DELETE /accounts/:id。
		accounts.POST("/:id/shadow", h.Admin.OpenAIOAuth.CreateSparkShadow)
		accounts.GET("/:id/shadows", h.Admin.OpenAIOAuth.ListSparkShadows)
		accounts.POST("/:id/shadow/sync", h.Admin.OpenAIOAuth.SyncSparkShadow)
		accounts.GET("/:id/usage", h.Admin.Account.GetUsage)
		accounts.GET("/:id/today-stats", h.Admin.Account.GetTodayStats)
		accounts.POST("/today-stats/batch", h.Admin.Account.GetBatchTodayStats)
		accounts.POST("/:id/clear-rate-limit", h.Admin.Account.ClearRateLimit)
		accounts.POST("/:id/reset-quota", h.Admin.Account.ResetQuota)
		accounts.GET("/:id/temp-unschedulable", h.Admin.Account.GetTempUnschedulable)
		accounts.DELETE("/:id/temp-unschedulable", h.Admin.Account.ClearTempUnschedulable)
		accounts.POST("/:id/schedulable", h.Admin.Account.SetSchedulable)
		accounts.POST("/models/sync-upstream-preview", h.Admin.Account.SyncUpstreamModelsPreview)
		accounts.GET("/:id/models", h.Admin.Account.GetAvailableModels)
		accounts.POST("/:id/models/sync-upstream", h.Admin.Account.SyncUpstreamModels)
		accounts.POST("/:id/upstream-billing-probe", h.Admin.Account.ProbeUpstreamBilling)
		accounts.PUT("/:id/upstream-billing-probe", h.Admin.Account.UpdateUpstreamBillingAutoProbe)
		accounts.POST("/batch", h.Admin.Account.BatchCreate)
		accounts.GET("/data", stepUpAuth.ForScope(service.StepUpScopeAccountsExport), h.Admin.Account.ExportData)
		accounts.POST("/data", h.Admin.Account.ImportData)
		accounts.POST("/batch-update-credentials", h.Admin.Account.BatchUpdateCredentials)
		accounts.POST("/batch-refresh-tier", h.Admin.Account.BatchRefreshTier)
		accounts.POST("/bulk-update", h.Admin.Account.BulkUpdate)
		accounts.POST("/batch-clear-error", h.Admin.Account.BatchClearError)
		accounts.POST("/batch-refresh", h.Admin.Account.BatchRefresh)

		// Antigravity 默认模型映射
		accounts.GET("/antigravity/default-model-mapping", h.Admin.Account.GetAntigravityDefaultModelMapping)

		// Claude OAuth routes
		accounts.POST("/generate-auth-url", h.Admin.OAuth.GenerateAuthURL)
		accounts.POST("/generate-setup-token-url", h.Admin.OAuth.GenerateSetupTokenURL)
		accounts.POST("/exchange-code", h.Admin.OAuth.ExchangeCode)
		accounts.POST("/exchange-setup-token-code", h.Admin.OAuth.ExchangeSetupTokenCode)
		accounts.POST("/cookie-auth", h.Admin.OAuth.CookieAuth)
		accounts.POST("/setup-token-cookie-auth", h.Admin.OAuth.SetupTokenCookieAuth)
	}
}

func registerAnnouncementRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	announcements := admin.Group("/announcements")
	{
		announcements.GET("", h.Admin.Announcement.List)
		announcements.POST("", h.Admin.Announcement.Create)
		announcements.GET("/:id", h.Admin.Announcement.GetByID)
		announcements.PUT("/:id", h.Admin.Announcement.Update)
		announcements.DELETE("/:id", h.Admin.Announcement.Delete)
		announcements.GET("/:id/read-status", h.Admin.Announcement.ListReadStatus)
	}
}

func registerOpenAIOAuthRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	openai := admin.Group("/openai")
	{
		openai.POST("/generate-auth-url", h.Admin.OpenAIOAuth.GenerateAuthURL)
		openai.POST("/exchange-code", h.Admin.OpenAIOAuth.ExchangeCode)
		openai.POST("/refresh-token", h.Admin.OpenAIOAuth.RefreshToken)
		openai.POST("/accounts/:id/refresh", h.Admin.OpenAIOAuth.RefreshAccountToken)
		openai.POST("/create-from-oauth", h.Admin.OpenAIOAuth.CreateAccountFromOAuth)
		openai.GET("/accounts/:id/quota", h.Admin.OpenAIOAuth.QueryQuota)
		openai.POST("/accounts/:id/quota/refresh", h.Admin.OpenAIOAuth.RefreshQuota)
		openai.POST("/accounts/:id/reset-quota", h.Admin.OpenAIOAuth.ResetQuota)
	}
}

func registerGeminiOAuthRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	gemini := admin.Group("/gemini")
	{
		gemini.POST("/oauth/auth-url", h.Admin.GeminiOAuth.GenerateAuthURL)
		gemini.POST("/oauth/exchange-code", h.Admin.GeminiOAuth.ExchangeCode)
		gemini.GET("/oauth/capabilities", h.Admin.GeminiOAuth.GetCapabilities)
	}
}

func registerAntigravityOAuthRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	antigravity := admin.Group("/antigravity")
	{
		antigravity.POST("/oauth/auth-url", h.Admin.AntigravityOAuth.GenerateAuthURL)
		antigravity.POST("/oauth/exchange-code", h.Admin.AntigravityOAuth.ExchangeCode)
		antigravity.POST("/oauth/refresh-token", h.Admin.AntigravityOAuth.RefreshToken)
	}
}

func registerGrokOAuthRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	grok := admin.Group("/grok")
	{
		grok.POST("/oauth/auth-url", h.Admin.GrokOAuth.GenerateAuthURL)
		grok.POST("/oauth/exchange-code", h.Admin.GrokOAuth.ExchangeCode)
		grok.POST("/oauth/refresh-token", h.Admin.GrokOAuth.RefreshToken)
		grok.POST("/oauth/reconcile", h.Admin.GrokOAuth.ReconcileOAuthAccounts)
		grok.POST("/oauth/create-from-oauth", h.Admin.GrokOAuth.CreateAccountFromOAuth)
		grok.POST("/sso-to-oauth", h.Admin.GrokOAuth.CreateAccountsFromSSO)
		grok.POST("/accounts/:id/refresh", h.Admin.GrokOAuth.RefreshAccountToken)
		grok.GET("/accounts/:id/quota", h.Admin.GrokOAuth.QueryQuota)
		grok.POST("/accounts/:id/reset-quota", h.Admin.GrokOAuth.ResetQuota)
		grok.GET("/runtime-sanity", h.Admin.GrokOAuth.RuntimeSanity)
	}
}

// registerCNProviderRoutes 提供原生国产供应商的只读额度与余额查询。
func registerCNProviderRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	cn := admin.Group("/cn-providers")
	{
		cn.GET("/accounts/:id/quota", h.Admin.CNProvider.QueryQuota)
		cn.GET("/accounts/:id/balance", h.Admin.CNProvider.QueryBalance)
	}
}

func registerProxyRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth *middleware.StepUpAuthMiddleware) {
	proxies := admin.Group("/proxies")
	{
		proxies.GET("", h.Admin.Proxy.List)
		proxies.GET("/all", h.Admin.Proxy.GetAll)
		proxies.GET("/data", stepUpAuth.ForScope(service.StepUpScopeProxiesExport), h.Admin.Proxy.ExportData)
		proxies.POST("/data", h.Admin.Proxy.ImportData)
		proxies.GET("/:id", h.Admin.Proxy.GetByID)
		proxies.POST("", h.Admin.Proxy.Create)
		proxies.PUT("/:id", h.Admin.Proxy.Update)
		proxies.DELETE("/:id", h.Admin.Proxy.Delete)
		proxies.POST("/:id/test", h.Admin.Proxy.Test)
		proxies.POST("/:id/quality-check", h.Admin.Proxy.CheckQuality)
		proxies.GET("/:id/stats", h.Admin.Proxy.GetStats)
		proxies.GET("/:id/accounts", h.Admin.Proxy.GetProxyAccounts)
		proxies.POST("/batch-delete", h.Admin.Proxy.BatchDelete)
		proxies.POST("/batch", h.Admin.Proxy.BatchCreate)
	}
}

func registerRedeemCodeRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	codes := admin.Group("/redeem-codes")
	{
		codes.GET("", h.Admin.Redeem.List)
		codes.GET("/stats", h.Admin.Redeem.GetStats)
		codes.GET("/export", h.Admin.Redeem.Export)
		codes.GET("/:id", h.Admin.Redeem.GetByID)
		codes.POST("/create-and-redeem", h.Admin.Redeem.CreateAndRedeem)
		codes.POST("/generate", h.Admin.Redeem.Generate)
		codes.DELETE("/:id", h.Admin.Redeem.Delete)
		codes.POST("/batch-delete", h.Admin.Redeem.BatchDelete)
		codes.POST("/batch-update", h.Admin.Redeem.BatchUpdate)
		codes.POST("/:id/expire", h.Admin.Redeem.Expire)
	}
}

func registerPromoCodeRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	promoCodes := admin.Group("/promo-codes")
	{
		promoCodes.GET("", h.Admin.Promo.List)
		promoCodes.GET("/:id", h.Admin.Promo.GetByID)
		promoCodes.POST("", h.Admin.Promo.Create)
		promoCodes.PUT("/:id", h.Admin.Promo.Update)
		promoCodes.DELETE("/:id", h.Admin.Promo.Delete)
		promoCodes.GET("/:id/usages", h.Admin.Promo.GetUsages)
	}
}

func registerSettingsRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	adminSettings := admin.Group("/settings")
	{
		adminSettings.GET("", h.Admin.Setting.GetSettings)
		adminSettings.PUT("", h.Admin.Setting.UpdateSettings)
		adminSettings.PATCH("", h.Admin.Setting.UpdateSettings)
		adminSettings.POST("/test-smtp", h.Admin.Setting.TestSMTPConnection)
		adminSettings.POST("/send-test-email", h.Admin.Setting.SendTestEmail)
		adminSettings.GET("/email-templates", h.Admin.Setting.ListEmailTemplates)
		adminSettings.POST("/email-template-preview", h.Admin.Setting.PreviewEmailTemplate)
		adminSettings.GET("/email-templates/:event/:locale", h.Admin.Setting.GetEmailTemplate)
		adminSettings.PUT("/email-templates/:event/:locale", h.Admin.Setting.UpdateEmailTemplate)
		adminSettings.POST("/email-templates/:event/:locale/restore-official", h.Admin.Setting.RestoreOfficialEmailTemplate)
		// Admin API Key 管理
		adminSettings.GET("/admin-api-key", h.Admin.Setting.GetAdminAPIKey)
		adminSettings.POST("/admin-api-key/reveal", h.Admin.Setting.RevealAdminAPIKey)
		adminSettings.POST("/admin-api-key/regenerate", h.Admin.Setting.RegenerateAdminAPIKey)
		adminSettings.DELETE("/admin-api-key", h.Admin.Setting.DeleteAdminAPIKey)
		// 轻量网关零钱结算模式
		adminSettings.GET("/billing-settlement", h.Admin.Setting.GetBillingSettlementSettings)
		adminSettings.PUT("/billing-settlement", h.Admin.Setting.UpdateBillingSettlementSettings)
		// 聚合分组节点总开关：数据库持久化，保存后即时作用于管理与请求热路径。
		adminSettings.GET("/composite-groups", h.Admin.Setting.GetCompositeGroupsSettings)
		adminSettings.PUT("/composite-groups", h.Admin.Setting.UpdateCompositeGroupsSettings)
		// Grok 视频编辑/扩展热开关：数据库持久化，保存后下一个请求立即生效（无需重启）。
		adminSettings.GET("/grok-video-transforms", h.Admin.Setting.GetGrokVideoTransformsSettings)
		adminSettings.PUT("/grok-video-transforms", h.Admin.Setting.UpdateGrokVideoTransformsSettings)
		// Codex 客户端准入：设置读取/保存同时返回脱敏内存聚合。
		adminSettings.GET("/codex-client-admission", h.Admin.Setting.GetCodexClientAdmissionSettings)
		adminSettings.PUT("/codex-client-admission", h.Admin.Setting.UpdateCodexClientAdmissionSettings)
		// 529过载冷却配置
		adminSettings.GET("/overload-cooldown", h.Admin.Setting.GetOverloadCooldownSettings)
		adminSettings.PUT("/overload-cooldown", h.Admin.Setting.UpdateOverloadCooldownSettings)
		// 429默认回避配置
		adminSettings.GET("/rate-limit-429-cooldown", h.Admin.Setting.GetRateLimit429CooldownSettings)
		adminSettings.PUT("/rate-limit-429-cooldown", h.Admin.Setting.UpdateRateLimit429CooldownSettings)
		// 账号兜底排队配置
		adminSettings.GET("/fallback-queue", h.Admin.Setting.GetFallbackQueueSettings)
		adminSettings.PUT("/fallback-queue", h.Admin.Setting.UpdateFallbackQueueSettings)
		// 流超时处理配置
		adminSettings.GET("/stream-timeout", h.Admin.Setting.GetStreamTimeoutSettings)
		adminSettings.PUT("/stream-timeout", h.Admin.Setting.UpdateStreamTimeoutSettings)
		// 请求整流器配置
		adminSettings.GET("/rectifier", h.Admin.Setting.GetRectifierSettings)
		adminSettings.PUT("/rectifier", h.Admin.Setting.UpdateRectifierSettings)
		// Beta 策略配置
		adminSettings.GET("/beta-policy", h.Admin.Setting.GetBetaPolicySettings)
		adminSettings.PUT("/beta-policy", h.Admin.Setting.UpdateBetaPolicySettings)
		// Web Search 模拟配置
		adminSettings.GET("/web-search-emulation", h.Admin.Setting.GetWebSearchEmulationConfig)
		adminSettings.PUT("/web-search-emulation", h.Admin.Setting.UpdateWebSearchEmulationConfig)
		adminSettings.POST("/web-search-emulation/test", h.Admin.Setting.TestWebSearchEmulation)
		adminSettings.POST("/web-search-emulation/reset-usage", h.Admin.Setting.ResetWebSearchUsage)
	}
}

func registerDataManagementRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth *middleware.StepUpAuthMiddleware) {
	dataManagement := admin.Group("/data-management")
	{
		dataManagement.GET("/agent/health", h.Admin.DataManagement.GetAgentHealth)
		dataManagement.GET("/config", stepUpAuth.ForScope(service.StepUpScopeDataS3Read), h.Admin.DataManagement.GetConfig)
		dataManagement.PUT("/config", stepUpAuth.ForScope(service.StepUpScopeDataS3Update), h.Admin.DataManagement.UpdateConfig)
		dataManagement.GET("/sources/:source_type/profiles", h.Admin.DataManagement.ListSourceProfiles)
		dataManagement.POST("/sources/:source_type/profiles", h.Admin.DataManagement.CreateSourceProfile)
		dataManagement.PUT("/sources/:source_type/profiles/:profile_id", h.Admin.DataManagement.UpdateSourceProfile)
		dataManagement.DELETE("/sources/:source_type/profiles/:profile_id", h.Admin.DataManagement.DeleteSourceProfile)
		dataManagement.POST("/sources/:source_type/profiles/:profile_id/activate", h.Admin.DataManagement.SetActiveSourceProfile)
		dataManagement.POST("/s3/test", stepUpAuth.ForScope(service.StepUpScopeDataS3Update), h.Admin.DataManagement.TestS3)
		dataManagement.GET("/s3/profiles", stepUpAuth.ForScope(service.StepUpScopeDataS3Read), h.Admin.DataManagement.ListS3Profiles)
		dataManagement.POST("/s3/profiles", stepUpAuth.ForScope(service.StepUpScopeDataS3Update), h.Admin.DataManagement.CreateS3Profile)
		dataManagement.PUT("/s3/profiles/:profile_id", stepUpAuth.ForScope(service.StepUpScopeDataS3Update), h.Admin.DataManagement.UpdateS3Profile)
		dataManagement.DELETE("/s3/profiles/:profile_id", stepUpAuth.ForScope(service.StepUpScopeDataS3Update), h.Admin.DataManagement.DeleteS3Profile)
		dataManagement.POST("/s3/profiles/:profile_id/activate", stepUpAuth.ForScope(service.StepUpScopeDataS3Update), h.Admin.DataManagement.SetActiveS3Profile)
		dataManagement.POST("/backups", stepUpAuth.ForScope(service.StepUpScopeBackupCreate), h.Admin.DataManagement.CreateBackupJob)
		dataManagement.GET("/backups", h.Admin.DataManagement.ListBackupJobs)
		dataManagement.GET("/backups/:job_id", h.Admin.DataManagement.GetBackupJob)
	}
}

func registerBackupRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth *middleware.StepUpAuthMiddleware) {
	backup := admin.Group("/backups")
	{
		// S3 存储配置
		backup.GET("/s3-config", stepUpAuth.ForScope(service.StepUpScopeBackupS3Read), h.Admin.Backup.GetS3Config)
		backup.PUT("/s3-config", stepUpAuth.ForScope(service.StepUpScopeBackupS3Update), h.Admin.Backup.UpdateS3Config)
		backup.POST("/s3-config/test", stepUpAuth.ForScope(service.StepUpScopeBackupS3Update), h.Admin.Backup.TestS3Connection)

		// 定时备份配置
		backup.GET("/schedule", h.Admin.Backup.GetSchedule)
		backup.PUT("/schedule", h.Admin.Backup.UpdateSchedule)

		// 备份操作
		backup.POST("", stepUpAuth.ForScope(service.StepUpScopeBackupCreate), h.Admin.Backup.CreateBackup)
		backup.GET("", h.Admin.Backup.ListBackups)
		backup.GET("/:id", h.Admin.Backup.GetBackup)
		backup.DELETE("/:id", stepUpAuth.ForScope(service.StepUpScopeBackupDelete), h.Admin.Backup.DeleteBackup)
		backup.GET("/:id/download-url", stepUpAuth.ForScope(service.StepUpScopeBackupDownload), h.Admin.Backup.GetDownloadURL)

		// 恢复操作
		backup.POST("/:id/restore", stepUpAuth.ForScope(service.StepUpScopeBackupRestore), h.Admin.Backup.RestoreBackup)
	}
}

func registerOperationAuditRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth *middleware.StepUpAuthMiddleware) {
	audit := admin.Group("/audit-logs")
	{
		audit.GET("", h.Admin.OperationAudit.List)
		audit.GET("/clear/preview", h.Admin.OperationAudit.PreviewClear)
		audit.GET("/:id", h.Admin.OperationAudit.Get)
		audit.POST("/clear", stepUpAuth.ForScope(service.StepUpScopeAuditClear), h.Admin.OperationAudit.Clear)
	}
}

func registerSystemRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	system := admin.Group("/system")
	{
		system.GET("/version", h.Admin.System.GetVersion)
		system.GET("/check-updates", h.Admin.System.CheckUpdates)
		system.POST("/update", h.Admin.System.PerformUpdate)
		system.POST("/rollback", h.Admin.System.Rollback)
		system.POST("/restart", h.Admin.System.RestartService)
	}
}

func registerSubscriptionRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	subscriptions := admin.Group("/subscriptions")
	{
		subscriptions.GET("", h.Admin.Subscription.List)
		subscriptions.GET("/:id", h.Admin.Subscription.GetByID)
		subscriptions.GET("/:id/progress", h.Admin.Subscription.GetProgress)
		subscriptions.POST("/assign", h.Admin.Subscription.Assign)
		subscriptions.POST("/bulk-assign", h.Admin.Subscription.BulkAssign)
		subscriptions.POST("/:id/extend", h.Admin.Subscription.Extend)
		subscriptions.POST("/:id/reset-quota", h.Admin.Subscription.ResetQuota)
		subscriptions.DELETE("/:id", h.Admin.Subscription.Revoke)
	}

	// 分组下的订阅列表
	admin.GET("/groups/:id/subscriptions", h.Admin.Subscription.ListByGroup)

	// 用户下的订阅列表
	admin.GET("/users/:id/subscriptions", h.Admin.Subscription.ListByUser)
}

func registerUsageRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	usage := admin.Group("/usage")
	{
		usage.GET("", h.Admin.Usage.List)
		usage.GET("/stats", h.Admin.Usage.Stats)
		usage.GET("/search-users", h.Admin.Usage.SearchUsers)
		usage.GET("/search-api-keys", h.Admin.Usage.SearchAPIKeys)
		usage.GET("/cleanup-tasks", h.Admin.Usage.ListCleanupTasks)
		usage.POST("/cleanup-tasks", h.Admin.Usage.CreateCleanupTask)
		usage.POST("/cleanup-tasks/:id/cancel", h.Admin.Usage.CancelCleanupTask)
	}
}

func registerUserAttributeRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	attrs := admin.Group("/user-attributes")
	{
		attrs.GET("", h.Admin.UserAttribute.ListDefinitions)
		attrs.POST("", h.Admin.UserAttribute.CreateDefinition)
		attrs.POST("/batch", h.Admin.UserAttribute.GetBatchUserAttributes)
		attrs.PUT("/reorder", h.Admin.UserAttribute.ReorderDefinitions)
		attrs.PUT("/:id", h.Admin.UserAttribute.UpdateDefinition)
		attrs.DELETE("/:id", h.Admin.UserAttribute.DeleteDefinition)
	}
}

func registerScheduledTestRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	plans := admin.Group("/scheduled-test-plans")
	{
		plans.POST("", h.Admin.ScheduledTest.Create)
		plans.PUT("/:id", h.Admin.ScheduledTest.Update)
		plans.DELETE("/:id", h.Admin.ScheduledTest.Delete)
		plans.GET("/:id/results", h.Admin.ScheduledTest.ListResults)
	}
	// Nested under accounts
	admin.GET("/accounts/:id/scheduled-test-plans", h.Admin.ScheduledTest.ListByAccount)
}

func registerErrorPassthroughRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	rules := admin.Group("/error-passthrough-rules")
	{
		rules.GET("", h.Admin.ErrorPassthrough.List)
		rules.GET("/:id", h.Admin.ErrorPassthrough.GetByID)
		rules.POST("", h.Admin.ErrorPassthrough.Create)
		rules.PUT("/:id", h.Admin.ErrorPassthrough.Update)
		rules.DELETE("/:id", h.Admin.ErrorPassthrough.Delete)
	}
}

func registerTLSFingerprintProfileRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	profiles := admin.Group("/tls-fingerprint-profiles")
	{
		profiles.GET("", h.Admin.TLSFingerprintProfile.List)
		profiles.GET("/:id", h.Admin.TLSFingerprintProfile.GetByID)
		profiles.POST("", h.Admin.TLSFingerprintProfile.Create)
		profiles.PUT("/:id", h.Admin.TLSFingerprintProfile.Update)
		profiles.DELETE("/:id", h.Admin.TLSFingerprintProfile.Delete)
	}
}

func registerChannelRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	modelPrices := admin.Group("/model-prices")
	{
		modelPrices.GET("", h.Admin.Channel.ListModelPrices)
		modelPrices.GET("/sync/status", h.Admin.Channel.GetModelPriceSyncStatus)
		modelPrices.POST("/sync", h.Admin.Channel.SyncModelPrices)
		modelPrices.PUT("", h.Admin.Channel.UpsertModelPrice)
		modelPrices.DELETE("", h.Admin.Channel.DeleteModelPrice)
	}

	channels := admin.Group("/channels")
	{
		channels.GET("", h.Admin.Channel.List)
		channels.GET("/model-pricing", h.Admin.Channel.GetModelDefaultPricing)
		channels.GET("/pricing/sync-models", h.Admin.Channel.SyncPricingModels)
		channels.GET("/:id", h.Admin.Channel.GetByID)
		channels.POST("", h.Admin.Channel.Create)
		channels.PUT("/:id", h.Admin.Channel.Update)
		channels.DELETE("/:id", h.Admin.Channel.Delete)
	}
}

func registerChannelMonitorRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	monitors := admin.Group("/channel-monitors")
	{
		monitors.GET("", h.Admin.ChannelMonitor.List)
		monitors.POST("", h.Admin.ChannelMonitor.Create)
		monitors.GET("/:id", h.Admin.ChannelMonitor.Get)
		monitors.POST("/:id/duplicate", h.Admin.ChannelMonitor.Duplicate)
		monitors.PUT("/:id", h.Admin.ChannelMonitor.Update)
		monitors.DELETE("/:id", h.Admin.ChannelMonitor.Delete)
		monitors.POST("/:id/run", h.Admin.ChannelMonitor.Run)
		monitors.GET("/:id/history", h.Admin.ChannelMonitor.History)
	}

	templates := admin.Group("/channel-monitor-templates")
	{
		templates.GET("", h.Admin.ChannelMonitorTemplate.List)
		templates.POST("", h.Admin.ChannelMonitorTemplate.Create)
		templates.GET("/:id", h.Admin.ChannelMonitorTemplate.Get)
		templates.PUT("/:id", h.Admin.ChannelMonitorTemplate.Update)
		templates.DELETE("/:id", h.Admin.ChannelMonitorTemplate.Delete)
		templates.GET("/:id/monitors", h.Admin.ChannelMonitorTemplate.AssociatedMonitors)
		templates.POST("/:id/apply", h.Admin.ChannelMonitorTemplate.Apply)
	}
}

func registerChannelMonitorV2Routes(admin *gin.RouterGroup, h *handler.Handlers, settingService *service.SettingService) {
	if h.ChannelMonitorV2 == nil {
		return
	}
	monitor := admin.Group("/channel-monitor-v2")
	monitor.Use(channelMonitorAdminFeatureGuard(settingService))
	{
		monitor.GET("/config", h.ChannelMonitorV2.GetConfig)
		monitor.PUT("/config", h.ChannelMonitorV2.UpdateConfig)
	}
	reads := monitor.Group("")
	reads.Use(channelMonitorModeV2Guard(settingService))
	{
		reads.GET("/dimensions", h.ChannelMonitorV2.Dimensions)
		reads.GET("/snapshot", h.ChannelMonitorV2.AdminSnapshot)
		reads.GET("/models", h.ChannelMonitorV2.AdminModels)
		reads.GET("/matrix", h.ChannelMonitorV2.AdminMatrix)
		reads.GET("/errors", h.ChannelMonitorV2.Errors)
		reads.GET("/users", h.ChannelMonitorV2.AdminUsers)
	}
}

func channelMonitorAdminFeatureGuard(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService != nil && settingService.GetChannelMonitorRuntime(c.Request.Context()).Enabled {
			c.Next()
			return
		}
		response.ErrorFrom(c, service.ErrChannelMonitorDisabled)
		c.Abort()
	}
}

func channelMonitorModeV2Guard(settingService *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settingService == nil {
			response.ErrorFrom(c, service.ErrChannelMonitorDisabled)
			c.Abort()
			return
		}
		runtime := settingService.GetChannelMonitorRuntime(c.Request.Context())
		if !runtime.Enabled {
			response.ErrorFrom(c, service.ErrChannelMonitorDisabled)
			c.Abort()
			return
		}
		if !runtime.PassiveAggregationAllowed() {
			response.ErrorFrom(c, service.ErrChannelMonitorModeMismatch)
			c.Abort()
			return
		}
		c.Next()
	}
}

// registerAffiliateRoutes 注册邀请返利的管理端路由（专属用户配置）
func registerAffiliateRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	affiliates := admin.Group("/affiliates")
	{
		affiliates.GET("/invites", h.Admin.Affiliate.ListInviteRecords)
		affiliates.GET("/rebates", h.Admin.Affiliate.ListRebateRecords)
		affiliates.GET("/transfers", h.Admin.Affiliate.ListTransferRecords)

		users := affiliates.Group("/users")
		{
			users.GET("", h.Admin.Affiliate.ListUsers)
			users.GET("/lookup", h.Admin.Affiliate.LookupUsers)
			users.POST("/batch-rate", h.Admin.Affiliate.BatchSetRate)
			users.GET("/:user_id/overview", h.Admin.Affiliate.GetUserOverview)
			users.PUT("/:user_id", h.Admin.Affiliate.UpdateUserSettings)
			users.DELETE("/:user_id", h.Admin.Affiliate.ClearUserSettings)
		}
	}
}

// registerPluginRoutes 注册插件系统管理路由（官方 v0.2.7 对齐）。
// 状态接口只读，不消耗 step-up（二次验证）票据；上传、启停、删除、保存和测试
// 按官方高风险操作挂 step-up。开关关闭时中间件保持放行，开启后才强制校验。
func registerPluginRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUpAuth *middleware.StepUpAuthMiddleware) {
	if h == nil || h.Admin.Plugin == nil {
		return
	}
	plugins := admin.Group("/plugins")
	{
		plugins.GET("", h.Admin.Plugin.List)
		plugins.GET("/:id", h.Admin.Plugin.Get)
		plugins.GET("/:id/status", h.Admin.Plugin.Status)
		plugins.GET("/:id/config", h.Admin.Plugin.GetConfig)
		plugins.POST("/:id/ui-session", h.Admin.Plugin.CreateUISession)
		if stepUpAuth != nil {
			plugins.POST("/upload", stepUpAuth.ForScope("plugin.upload"), h.Admin.Plugin.Upload)
			plugins.POST("/:id/enable", stepUpAuth.ForScope("plugin.enable"), h.Admin.Plugin.Enable)
			plugins.POST("/:id/disable", stepUpAuth.ForScope("plugin.disable"), h.Admin.Plugin.Disable)
			plugins.DELETE("/:id", stepUpAuth.ForScope("plugin.delete"), h.Admin.Plugin.Delete)
			plugins.PUT("/:id/config", stepUpAuth.ForScope("plugin.config"), h.Admin.Plugin.SaveConfig)
			plugins.POST("/:id/test", stepUpAuth.ForScope("plugin.test"), h.Admin.Plugin.Test)
		} else {
			plugins.POST("/upload", h.Admin.Plugin.Upload)
			plugins.POST("/:id/enable", h.Admin.Plugin.Enable)
			plugins.POST("/:id/disable", h.Admin.Plugin.Disable)
			plugins.DELETE("/:id", h.Admin.Plugin.Delete)
			plugins.PUT("/:id/config", h.Admin.Plugin.SaveConfig)
			plugins.POST("/:id/test", h.Admin.Plugin.Test)
		}
	}
}
