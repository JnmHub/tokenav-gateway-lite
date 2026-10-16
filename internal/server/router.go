package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

const frameSrcRefreshTimeout = 5 * time.Second

// SetupRouter 配置路由器中间件和路由
func SetupRouter(
	r *gin.Engine,
	handlers *handler.Handlers,
	jwtAuth middleware2.JWTAuthMiddleware,
	adminAuth middleware2.AdminAuthMiddleware,
	apiKeyAuth middleware2.APIKeyAuthMiddleware,
	operationAudit middleware2.OperationAuditMiddleware,
	stepUpAuth *middleware2.StepUpAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	compositeResolver *service.CompositeRouteResolver,
	pricingService *service.PricingService,
	channelService *service.ChannelService,
	accountRepository service.AccountRepository,
	groupRepository service.GroupRepository,
	sqlDB *sql.DB,
	cfg *config.Config,
	redisClient *redis.Client,
) *gin.Engine {
	if cfg.RunMode == config.RunModeGatewayLite {
		var gatewayHandler *handler.GatewayHandler
		if handlers != nil {
			gatewayHandler = handlers.Gateway
		}
		overloadLimiter := middleware2.NewGatewayLiteOverloadLimiter(
			cfg.GatewayLite.OverloadEnabled,
			cfg.GatewayLite.OverloadMaxInflight,
			cfg.GatewayLite.OverloadMaxWaiting,
			time.Duration(cfg.GatewayLite.OverloadWaitTimeoutMS)*time.Millisecond,
		)
		// 媒体代理开着但没配 public_url 时，生图接口会静默返回 `mp2.…` 这样的
		// 同源相对地址。跨域调用方（浏览器里跑的前端）拿到它无法拼出可访问的
		// 完整链接，只能自己猜网关主机名，属于「配置漏了但没报错」的故障。
		// 启动时把这件事说清楚，避免又要从客户端表现倒查回服务端配置。
		if cfg.GatewayLite.MediaProxyEnabled && strings.TrimSpace(cfg.GatewayLite.MediaProxyPublicURL) == "" {
			log.Printf("gateway-lite: media proxy is enabled but gateway_lite.public_url is empty; " +
				"generated image URLs will be returned as same-origin relative paths (/v1/media/...), " +
				"which cross-origin clients cannot resolve - set gateway_lite.public_url to this node's public https origin")
		}
		log.Printf("gateway-lite: overload protection enabled=%t max_inflight=%d max_waiting=%d wait_timeout_ms=%d",
			cfg.GatewayLite.OverloadEnabled,
			cfg.GatewayLite.OverloadMaxInflight,
			cfg.GatewayLite.OverloadMaxWaiting,
			cfg.GatewayLite.OverloadWaitTimeoutMS,
		)
		apiKeyAuth = gatewayLiteAPIKeyAuthOrFallback(apiKeyAuth, apiKeyService, redisClient, sqlDB, cfg, pricingService, channelService, settingService, accountRepository, groupRepository, compositeResolver, gatewayHandler, handlers, overloadLimiter)
		setupCommonMiddlewareAndFrontend(r, settingService, cfg)
		timingController := servertiming.DefaultController()
		timingController.SetSampleBasisPoints(cfg.GatewayLite.ServerTimingSampleBasisPoints)
		r.Use(middleware2.GatewayLiteServerTiming(timingController))
		// Global Codex admission runs before overload waiting, key resolution,
		// lease reservation, dispatch, and normal usage accounting.
		if settingService != nil {
			r.Use(middleware2.GatewayLiteCodexClientAdmission(settingService.CodexClientAdmissionRuntime()))
		}
		if manager := gatewaylite.DefaultBindingManager(); manager != nil {
			manager.ConfigureDrain(sqlDB, redisClient)
		}
		r.Use(middleware2.GatewayBindingAdmission())
		r.Use(overloadLimiter.Handler())
		registerRoutes(r, handlers, jwtAuth, adminAuth, apiKeyAuth, operationAudit, stepUpAuth, apiKeyService, subscriptionService, opsService, settingService, compositeResolver, sqlDB, cfg, redisClient)
		return r
	}

	setupCommonMiddlewareAndFrontend(r, settingService, cfg)

	// 注册路由
	registerRoutes(r, handlers, jwtAuth, adminAuth, apiKeyAuth, operationAudit, stepUpAuth, apiKeyService, subscriptionService, opsService, settingService, compositeResolver, sqlDB, cfg, redisClient)

	return r
}

func setupCommonMiddlewareAndFrontend(r *gin.Engine, settingService *service.SettingService, cfg *config.Config) {
	// 缓存 iframe 页面的 origin 列表，用于动态注入 CSP frame-src
	var cachedFrameOrigins atomic.Pointer[[]string]
	emptyOrigins := []string{}
	cachedFrameOrigins.Store(&emptyOrigins)

	refreshFrameOrigins := func() {
		if settingService == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), frameSrcRefreshTimeout)
		defer cancel()
		origins, err := settingService.GetFrameSrcOrigins(ctx)
		if err != nil {
			// 获取失败时保留已有缓存，避免 frame-src 被意外清空
			return
		}
		cachedFrameOrigins.Store(&origins)
	}
	refreshFrameOrigins() // 启动时初始化

	// 应用中间件
	r.Use(middleware2.RequestLogger())
	r.Use(middleware2.Logger(cfg))
	r.Use(middleware2.CORS(cfg.CORS))
	r.Use(middleware2.SecurityHeaders(cfg.Security.CSP, func() []string {
		if p := cachedFrameOrigins.Load(); p != nil {
			return *p
		}
		return nil
	}))

	// Serve embedded frontend with settings injection if available
	if web.HasEmbeddedFrontend() {
		frontendServer, err := web.NewFrontendServer(settingService, cfg)
		if err != nil {
			log.Printf("Warning: Failed to create frontend server with settings injection: %v, using legacy mode", err)
			r.Use(web.ServeEmbeddedFrontend(cfg))
			if settingService != nil {
				settingService.SetOnUpdateCallback(refreshFrameOrigins)
			}
		} else {
			// Register combined callback: invalidate HTML cache + refresh frame origins
			if settingService != nil {
				settingService.SetOnUpdateCallback(func() {
					frontendServer.InvalidateCache()
					refreshFrameOrigins()
				})
			}
			r.Use(frontendServer.Middleware())
		}
	} else if settingService != nil {
		settingService.SetOnUpdateCallback(refreshFrameOrigins)
	}
}

func gatewayLiteAPIKeyAuthOrFallback(fallback middleware2.APIKeyAuthMiddleware, apiKeyService *service.APIKeyService, redisClient *redis.Client, sqlDB *sql.DB, cfg *config.Config, pricingService *service.PricingService, channelService *service.ChannelService, settingService *service.SettingService, accountRepository service.AccountRepository, groupRepository service.GroupRepository, compositeResolver *service.CompositeRouteResolver, gatewayHandler *handler.GatewayHandler, handlers *handler.Handlers, overloadLimiter *middleware2.GatewayLiteOverloadLimiter) middleware2.APIKeyAuthMiddleware {
	liteCfg := config.GatewayLiteConfig{}
	if cfg != nil {
		liteCfg = cfg.GatewayLite
	}
	baseURL := gatewayLiteStringFromEnv("GATEWAY_LITE_CONTROL_PLANE_URL", liteCfg.ControlPlaneURL)
	region := gatewayLiteStringFromEnv("GATEWAY_LITE_REGION", liteCfg.Region)
	if gatewaylite.DefaultBindingManager() != nil { baseURL, region = liteCfg.ControlPlaneURL, liteCfg.Region }
	if region == "" {
		region = "default"
	}
	timeout := time.Duration(gatewayLiteIntFromEnv("GATEWAY_LITE_CONTROL_PLANE_TIMEOUT_MS", liteCfg.ControlPlaneTimeoutMS, 300)) * time.Millisecond
	var client *gatewaylite.ControlPlaneClient
	if baseURL == "" {
		log.Printf("gateway-lite: control plane URL is empty; initializing runtime in pending mode")
	} else {
		var err error
		token := gatewayLiteStringFromEnv("GATEWAY_LITE_CONTROL_PLANE_TOKEN", liteCfg.ControlPlaneToken)
		if gatewaylite.DefaultBindingManager() != nil { token = liteCfg.ControlPlaneToken }
		client, err = gatewaylite.NewControlPlaneClient(baseURL, token, timeout)
		if err != nil {
			log.Printf("gateway-lite: invalid control plane client config: %v; initializing runtime in pending mode", err)
		}
	}
	clientRef := gatewaylite.NewControlPlaneClientRef(client)
	gatewaylite.SetDefaultControlPlaneClientRef(clientRef)
	// 委托变更和派发前必须留下可恢复的资金事实；消费队列不能替代准入阶段的持久栅栏。
	invocationRecovery := gatewaylite.NewOAuthInvocationRecoveryOutbox(sqlDB, clientRef)
	gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(invocationRecovery)
	invocationRecovery.Start(context.Background())
	penaltyReporter := gatewaylite.NewDurableContentModerationPenaltyReporter(sqlDB, clientRef)
	gatewaylite.SetDefaultContentModerationPenaltyReporter(penaltyReporter)
	penaltyReporter.StartRetryWorker(context.Background())
	redisPrefix := gatewayLiteStringFromEnv("GATEWAY_LITE_REDIS_PREFIX", liteCfg.RedisPrefix)
	if gatewaylite.DefaultBindingManager() != nil { redisPrefix = liteCfg.RedisPrefix }
	quota := gatewaylite.NewRedisQuota(redisClient, redisPrefix)
	keyCache := gatewaylite.NewRedisKeyCache(redisClient, redisPrefix)
	usageSink := gatewaylite.NewOAuthInvocationUsageSink(clientRef, clientRef)
	usageReporter := gatewaylite.NewRedisUsageReporter(redisClient, redisPrefix, usageSink)
	durableUsageReporter := gatewaylite.NewDurableUsageReporter(sqlDB, usageReporter)
	gatewaylite.SetDefaultUsageReporter(durableUsageReporter)
	runtimeStats := gatewaylite.NewRuntimeStats()
	gatewaylite.SetDefaultRuntimeStats(runtimeStats)
	usageReporter.StartWorkersWithBatchSize(context.Background(), liteCfg.UsageReportWorkers, liteCfg.UsageReportBatchSize)
	durableUsageReporter.StartRetryWorker(context.Background())
	usageQueuePendingThreshold := gatewayLiteInt64FromEnv("GATEWAY_LITE_USAGE_QUEUE_PENDING_ALERT_THRESHOLD", liteCfg.UsageQueuePendingAlertThreshold, 1000)
	usageQueueDeadThreshold := gatewayLiteInt64FromEnv("GATEWAY_LITE_USAGE_QUEUE_DEAD_ALERT_THRESHOLD", liteCfg.UsageQueueDeadAlertThreshold, 1)
	gatewayCode := gatewayLiteStringFromEnv("GATEWAY_LITE_GATEWAY_CODE", liteCfg.GatewayCode)
	if gatewaylite.DefaultBindingManager() != nil { gatewayCode = liteCfg.GatewayCode }
	if gatewayCode == "" {
		gatewayCode = region
	}
	ingress.SetNode(gatewayCode)
	runtimeHealthInterval := gatewayLiteDurationFromEnv("GATEWAY_LITE_RUNTIME_HEALTH_INTERVAL_SECONDS", liteCfg.RuntimeHealthIntervalSeconds, 15*time.Second)
	runtimeModelProbeModel := gatewayLiteStringFromEnv("GATEWAY_LITE_HEALTH_PROBE_MODEL", "")
	runtimeModelProbeInterval := gatewayLiteDurationFromEnv("GATEWAY_LITE_MODEL_PROBE_INTERVAL_SECONDS", 0, 5*time.Minute)
	configSyncInterval := time.Duration(gatewayLiteIntFromEnv("GATEWAY_LITE_CONFIG_SYNC_INTERVAL_SECONDS", liteCfg.ConfigSyncIntervalSeconds, 30)) * time.Second
	invalidationInterval := time.Duration(gatewayLiteIntFromEnv("GATEWAY_LITE_CACHE_INVALIDATION_INTERVAL_SECONDS", liteCfg.CacheInvalidationIntervalSeconds, 5)) * time.Second
	runtimeActiveWindow := gatewayLiteDurationFromEnv("GATEWAY_LITE_RUNTIME_ACTIVE_WINDOW_SECONDS", liteCfg.RuntimeActiveWindowSeconds, 5*time.Minute)
	effectivePolicyRefreshInterval := gatewayLiteDurationFromEnv("GATEWAY_LITE_EFFECTIVE_POLICY_REFRESH_INTERVAL_SECONDS", 0, 5*time.Minute)
	legacySnapshotSchedulerAuthority := gatewayLiteBoolFromEnv("GATEWAY_LITE_LEGACY_SNAPSHOT_SCHEDULER_AUTHORITY", liteCfg.LegacySnapshotSchedulerAuthority)
	invalidAuthLimiter := gatewaylite.NewInvalidAuthAbuseLimiter(redisClient, redisPrefix, gatewaylite.InvalidAuthAbuseConfig{
		Enabled:      gatewayLiteBoolFromEnv("GATEWAY_LITE_INVALID_AUTH_ABUSE_ENABLED", liteCfg.InvalidAuthAbuseEnabled),
		Threshold:    gatewayLiteIntFromEnv("GATEWAY_LITE_INVALID_AUTH_ABUSE_THRESHOLD", liteCfg.InvalidAuthAbuseThreshold, 120),
		Window:       gatewayLiteDurationFromEnv("GATEWAY_LITE_INVALID_AUTH_ABUSE_WINDOW_SECONDS", liteCfg.InvalidAuthAbuseWindowSeconds, time.Minute),
		Block:        gatewayLiteDurationFromEnv("GATEWAY_LITE_INVALID_AUTH_ABUSE_BLOCK_SECONDS", liteCfg.InvalidAuthAbuseBlockSeconds, time.Minute),
		Capacity:     gatewayLiteInt64FromEnv("GATEWAY_LITE_INVALID_AUTH_ABUSE_CAPACITY", liteCfg.InvalidAuthAbuseCapacity, 16_384),
		RedisTimeout: time.Duration(gatewayLiteIntFromEnv("GATEWAY_LITE_INVALID_AUTH_ABUSE_REDIS_TIMEOUT_MS", liteCfg.InvalidAuthAbuseRedisTimeoutMS, 25)) * time.Millisecond,
	})
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{
		Region:                           region,
		GatewayCode:                      gatewayCode,
		RedisPrefix:                      redisPrefix,
		RuntimeHealthIntervalSeconds:     int(runtimeHealthInterval / time.Second),
		RuntimeActiveWindowSeconds:       int(runtimeActiveWindow / time.Second),
		ConfigSyncIntervalSeconds:        int(configSyncInterval / time.Second),
		CacheInvalidationIntervalSeconds: int(invalidationInterval / time.Second),
		LegacySnapshotSchedulerAuthority: legacySnapshotSchedulerAuthority,
		UsageQueuePendingAlertThreshold:  usageQueuePendingThreshold,
		UsageQueueDeadAlertThreshold:     usageQueueDeadThreshold,
		AllowLive:                        liteCfg.AllowLive,
	})
	gatewaylite.SetDefaultRuntimeConfigRef(runtimeRef)
	runtimeHealthSettings := gatewaylite.NewRuntimeHealthSettings(runtimeHealthInterval)
	runtimeHealthSettings.SetModelProbeInterval(runtimeModelProbeInterval)
	runtimeHealthSettings.SetModelProbeModel(runtimeModelProbeModel)
	gatewaylite.SetDefaultRuntimeHealthSettings(runtimeHealthSettings)
	configCache := gatewaylite.NewRedisConfigCache(redisClient, redisPrefix)
	configSyncer := gatewaylite.NewConfigSyncer(clientRef, configCache, gatewayCode, configSyncInterval)
	schedulerAuthorityCache := repository.NewSchedulerCache(redisClient)
	schedulerAuthorityGroupCache, _ := schedulerAuthorityCache.(service.SchedulerGroupCache)
	configSyncer.SetApplier(newGatewayLiteSchedulerConfigApplier(schedulerAuthorityCache).
		WithRuntimeConfig(runtimeRef, runtimeHealthSettings).
		WithGroupRepository(groupRepository).
		WithLegacySnapshotSchedulerAuthority(legacySnapshotSchedulerAuthority))
	gatewaylite.SetDefaultConfigSyncer(configSyncer)
	gatewaylite.SetDefaultRedisQuota(quota)
	gatewaylite.SetDefaultRedisKeyCache(keyCache)
	gatewaylite.SetDefaultRedisConfigCache(configCache)
	// 独立节点没有 control-plane（主站控制面）时，本地数据库就是配置权威来源。
	// 不启动同步器可避免空客户端反复产生失败日志，也不会触发任何主站请求。
	if clientRef.Configured() {
		configSyncer.Start(context.Background())
		if manager := gatewaylite.DefaultBindingManager(); manager != nil {
			gatewaylite.ResetBindingActivationWorkers()
			manager.StartActivation(configSyncer.SyncFull)
		}
	}
	availableModels := gatewayLiteStringSliceFromEnv("GATEWAY_LITE_AVAILABLE_MODELS", liteCfg.AvailableModels)
	var openAIGatewayHandler *handler.OpenAIGatewayHandler
	if handlers != nil {
		openAIGatewayHandler = handlers.OpenAIGateway
	}
	availableModelsProvider := gatewayLiteAvailableModelsProvider{
		staticModels: availableModels,
		handler:      gatewayHandler,
	}
	modelPriceProvider := gatewayLiteModelPriceProvider{pricingService: pricingService}
	configPushProvider := gatewayLiteConfigPushSnapshotProvider{
		accounts:        accountRepository,
		groups:          groupRepository,
		models:          availableModelsProvider,
		prices:          modelPriceProvider,
		runtimeSettings: settingService,
		compositeRoutes: compositeResolver,
		groupPricing:    channelService,
	}
	configChangeNotifier := gatewaylite.NewConfigChangeNotifier(clientRef, runtimeRef, 2*time.Second, 5*time.Second).
		WithConfigPush(clientRef, configPushProvider)
	gatewaylite.SetDefaultConfigChangeNotifier(configChangeNotifier)
	if clientRef.Configured() {
		// 启动后主动推送一次完整镜像，确保节点重启或主站清库后也能自动收敛。
		configChangeNotifier.Notify("mixed", "startup", 0)
		configChangeNotifier.StartPeriodicSnapshot(context.Background(), effectivePolicyRefreshInterval)
	}
	runtimeHealthMonitor := gatewaylite.NewRuntimeHealthMonitor(clientRef, runtimeStats, region, gatewayCode, runtimeHealthInterval, runtimeActiveWindow).
		WithIntervalSource(runtimeHealthSettings).
		WithUsageQueue(usageReporter, usageQueuePendingThreshold, usageQueueDeadThreshold).
		WithAvailableModelsProvider(availableModelsProvider).
		WithModelProbe(gatewayLiteModelProbeProvider{handler: openAIGatewayHandler}, runtimeModelProbeModel, runtimeModelProbeInterval).
		WithModelProbeSettings(runtimeHealthSettings).
		WithModelPriceProvider(modelPriceProvider).
		WithRuntimeMetricsProvider(gatewayLiteAuthorityRuntimeMetrics{runtimeRef: runtimeRef}).
		WithRuntimeMetricsProvider(handlers).
		WithRuntimeMetricsProvider(configChangeNotifier).
		WithRuntimeMetricsProvider(gatewayLiteRedisRuntimeMetricsProvider{client: redisClient}).
		WithRuntimeMetricsProvider(gatewayLiteDBRuntimeMetricsProvider{db: sqlDB}).
		WithRuntimeMetricsProvider(&gatewayLiteUpstreamRuntimeMetricsProvider{db: sqlDB}).
		WithRuntimeMetricsProvider(invalidAuthLimiter).
		WithRuntimeMetricsProvider(gatewayLiteIngressRuntimeMetricsProvider{}).
		WithRuntimeMetricsProvider(gatewayLitePanelRateLimitRuntimeMetricsProvider{}).
		WithRuntimeMetricsProvider(overloadLimiter)
	gatewaylite.SetDefaultRuntimeHealthMonitor(runtimeHealthMonitor)
	if clientRef.Configured() {
		runtimeHealthMonitor.Start(context.Background())
	}
	cacheInvalidationSyncer := gatewaylite.NewCacheInvalidationSyncer(clientRef, configCache, keyCache, configSyncer, region, gatewayCode, invalidationInterval).
		WithLeaseInvalidator(quota)
	gatewaylite.SetDefaultCacheInvalidationSyncer(cacheInvalidationSyncer)
	if clientRef.Configured() {
		cacheInvalidationSyncer.Start(context.Background())
	}
	if quota.Enabled() {
		log.Printf("gateway-lite: remote key/quota auth enabled for region=%s with local Redis key and quota cache", region)
	} else {
		log.Printf("gateway-lite: remote key/quota auth enabled for region=%s without local Redis key/quota cache", region)
	}
	gatewayAuth := middleware2.NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter(
		clientRef,
		runtimeRef,
		quota,
		keyCache,
		durableUsageReporter,
		&gatewayLiteLocalGroupReader{cache: schedulerAuthorityGroupCache, repo: groupRepository},
		invalidAuthLimiter,
		settingService,
	)
	if !clientRef.Configured() {
		log.Printf("gateway-lite: control plane client is pending; stock API key auth will be used until config sync")
	}
	remoteOrFallback := middleware2.APIKeyAuthMiddleware(func(c *gin.Context) {
		if clientRef.Configured() {
			gatewayAuth(c)
			return
		}
		fallback(c)
	})
	var localResolver middleware2.GatewayLiteLocalAPIKeyResolver
	if apiKeyService != nil {
		localResolver = apiKeyService
	}
	return middleware2.NewGatewayLiteHybridAPIKeyAuthMiddleware(localResolver, remoteOrFallback, cfg)
}

type gatewayLiteLocalGroupReader struct {
	cache service.SchedulerGroupCache
	repo  service.GroupRepository
	reads singleflight.Group
}

func (r *gatewayLiteLocalGroupReader) GetGroup(ctx context.Context, groupID int64) (*service.Group, error) {
	if groupID <= 0 {
		return nil, nil
	}
	value, err, _ := r.reads.Do(strconv.FormatInt(groupID, 10), func() (any, error) {
		// 这里只合并同一时刻的 Redis 读取，不增加 TTL（缓存时间）；分组更新或
		// 删除后，下一次独立请求仍会立即读取权威快照。
		if r.cache != nil {
			group, readErr := r.cache.GetGroup(ctx, groupID)
			if readErr == nil && group != nil {
				return group, nil
			}
			if readErr != nil && r.repo == nil {
				return nil, readErr
			}
		}
		if r.repo == nil {
			return nil, nil
		}
		group, readErr := r.repo.GetByIDLite(ctx, groupID)
		if errors.Is(readErr, service.ErrGroupNotFound) {
			return nil, nil
		}
		if readErr != nil {
			return nil, readErr
		}
		if r.cache != nil {
			if writeErr := r.cache.SetGroup(ctx, group); writeErr != nil {
				return nil, writeErr
			}
		}
		return group, nil
	})
	if err != nil || value == nil {
		return nil, err
	}
	group, _ := value.(*service.Group)
	if group == nil {
		return nil, nil
	}
	// 中间件会为本次请求合并 EffectiveRequiredTier；返回浅拷贝避免并发请求
	// 修改 singleflight（并发合并）共享的同一个结构体。
	cloned := *group
	return &cloned, nil
}

func gatewayLiteStringFromEnv(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type gatewayLiteAvailableModelsHandler interface {
	GlobalAvailableModels(context.Context) []string
}

type gatewayLiteAvailableModelsProvider struct {
	staticModels []string
	handler      gatewayLiteAvailableModelsHandler
}

type gatewayLiteModelProbeProvider struct {
	handler *handler.OpenAIGatewayHandler
}

func (p gatewayLiteModelProbeProvider) ProbeRuntimeModel(ctx context.Context, preferredModel string) gatewaylite.RuntimeModelProbeResult {
	if p.handler == nil {
		return gatewaylite.RuntimeModelProbeResult{
			OK:     false,
			Target: "model-chat-completions",
			Error:  "openai gateway handler not ready",
		}
	}
	return p.handler.ProbeRuntimeModel(ctx, preferredModel)
}

func (p gatewayLiteAvailableModelsProvider) AvailableModels(ctx context.Context) []string {
	if p.handler == nil {
		return cloneGatewayLiteStringSlice(p.staticModels)
	}
	// 真实可调度账号的 model_mapping（模型映射）是运行时能力事实来源。
	// 静态列表只在没有任何动态模型时兜底，不能与动态结果取并集，否则主站会
	// 展示当前账号池实际上无法调度的模型。
	if dynamicModels := p.handler.GlobalAvailableModels(ctx); len(dynamicModels) > 0 {
		return cloneGatewayLiteStringSlice(dynamicModels)
	}
	return cloneGatewayLiteStringSlice(p.staticModels)
}

func (p gatewayLiteAvailableModelsProvider) AvailableModelsFromAccounts(accounts []service.Account) []string {
	// 配置镜像只按持久状态收缩；短暂限流不会把模型从主站目录撤下。
	eligible := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		if account.IsActive() && account.Schedulable {
			eligible = append(eligible, account)
		}
	}
	if dynamicModels := service.AvailableModelsFromAccounts(eligible, ""); len(dynamicModels) > 0 {
		return dynamicModels
	}
	return cloneGatewayLiteStringSlice(p.staticModels)
}

func cloneGatewayLiteStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

type gatewayLiteRedisRuntimeMetricsProvider struct {
	client *redis.Client
}

func (p gatewayLiteRedisRuntimeMetricsProvider) RuntimeMetrics(ctx context.Context) map[string]any {
	metrics := map[string]any{}
	if p.client == nil || (ctx != nil && ctx.Err() != nil) {
		return metrics
	}
	stats := p.client.PoolStats()
	if stats == nil {
		return metrics
	}
	metrics["redis_pool_hits_total"] = stats.Hits
	metrics["redis_pool_misses_total"] = stats.Misses
	metrics["redis_pool_timeouts_total"] = stats.Timeouts
	metrics["redis_pool_wait_count_total"] = stats.WaitCount
	metrics["redis_pool_wait_duration_ms_total"] = stats.WaitDurationNs / int64(time.Millisecond)
	metrics["redis_pool_total_conns"] = stats.TotalConns
	metrics["redis_pool_idle_conns"] = stats.IdleConns
	metrics["redis_pool_stale_conns_total"] = stats.StaleConns
	return metrics
}

func gatewayLiteIntFromEnv(name string, configured int, fallback int) int {
	raw := os.Getenv(name)
	if raw != "" {
		value, err := strconv.Atoi(raw)
		if err == nil && value > 0 {
			return value
		}
	}
	if configured > 0 {
		return configured
	}
	return fallback
}

func gatewayLiteDurationFromEnv(name string, configuredSeconds int, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	if configuredSeconds > 0 {
		return time.Duration(configuredSeconds) * time.Second
	}
	return fallback
}

func gatewayLiteInt64FromEnv(name string, configured int64, fallback int64) int64 {
	raw := os.Getenv(name)
	if raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && value > 0 {
			return value
		}
	}
	if configured > 0 {
		return configured
	}
	return fallback
}

func gatewayLiteBoolFromEnv(name string, configured bool) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return configured
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return configured
	}
}

func gatewayLiteStringSliceFromEnv(name string, configured []string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		return configured
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// registerRoutes 注册所有 HTTP 路由
func registerRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	jwtAuth middleware2.JWTAuthMiddleware,
	adminAuth middleware2.AdminAuthMiddleware,
	apiKeyAuth middleware2.APIKeyAuthMiddleware,
	operationAudit middleware2.OperationAuditMiddleware,
	stepUpAuth *middleware2.StepUpAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	compositeResolver *service.CompositeRouteResolver,
	sqlDB *sql.DB,
	cfg *config.Config,
	redisClient *redis.Client,
) {
	// 通用路由（健康检查、状态等）
	routes.RegisterCommonRoutes(r, commonReadinessProbes(sqlDB, redisClient)...)

	if cfg.RunMode == config.RunModeGatewayLite {
		registerGatewayLiteControlPlaneRoutes(r, cfg)
		v1 := r.Group("/api/v1")
		// 只给后台登录与管理 API 注入浏览器会话指纹，客户 API Key 热路径不注册该中间件。
		v1.Use(middleware2.SessionBindingContext(cfg))
		// 插件隔离 UI 静态资源：短时随机能力 URL 鉴权，不走常规管理员鉴权（官方 v0.2.5 对齐）。
		if h.Admin.Plugin != nil {
			v1.GET("/plugin-ui/:token/*path", h.Admin.Plugin.ServeUIAsset)
		}
		routes.RegisterGatewayLiteAuthRoutes(v1, h, jwtAuth, operationAudit, redisClient, settingService)
		routes.RegisterGatewayLiteAdminRoutes(v1, h, adminAuth, operationAudit, stepUpAuth, settingService, apiKeyService, cfg)
		routes.RegisterGatewayRoutes(r, h, apiKeyAuth, apiKeyService, subscriptionService, opsService, settingService, compositeResolver, cfg)
		return
	}

	// API v1
	v1 := r.Group("/api/v1")
	v1.Use(middleware2.SessionBindingContext(cfg))
	// 插件隔离 UI 静态资源：短时随机能力 URL 鉴权，不走常规管理员鉴权（官方 v0.2.5 对齐）。
	if h.Admin.Plugin != nil {
		v1.GET("/plugin-ui/:token/*path", h.Admin.Plugin.ServeUIAsset)
	}

	// 注册各模块路由
	routes.RegisterAuthRoutes(v1, h, jwtAuth, operationAudit, redisClient, settingService)
	routes.RegisterUserRoutes(v1, h, jwtAuth, operationAudit, settingService)
	routes.RegisterAdminRoutes(v1, h, adminAuth, operationAudit, stepUpAuth, settingService)
	routes.RegisterGatewayRoutes(r, h, apiKeyAuth, apiKeyService, subscriptionService, opsService, settingService, compositeResolver, cfg)
	routes.RegisterPaymentRoutes(v1, h.Payment, h.PaymentWebhook, h.Admin.Payment, jwtAuth, adminAuth, settingService)

	handler.RegisterPageRoutes(v1, cfg.Pricing.DataDir, gin.HandlerFunc(jwtAuth), gin.HandlerFunc(adminAuth), settingService)
}

func commonReadinessProbes(sqlDB *sql.DB, redisClient *redis.Client) []routes.ReadinessProbe {
	return []routes.ReadinessProbe{
		{
			Name: "database",
			Check: func(ctx context.Context) error {
				if sqlDB == nil {
					return errors.New("database client is nil")
				}
				return sqlDB.PingContext(ctx)
			},
		},
		{
			Name: "redis",
			Check: func(ctx context.Context) error {
				if redisClient == nil {
					return errors.New("redis client is nil")
				}
				return redisClient.Ping(ctx).Err()
			},
		},
	}
}
