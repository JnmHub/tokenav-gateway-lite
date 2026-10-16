package service

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ProvideGrokOAuthService 构造 Grok OAuth 服务。
// 官方 v0.2.5 起构造函数显式接收 cfg；Redis 会话存储仍在此处注入（wire.go 对
// redis 依赖豁免 depguard），保证多实例部署下 PKCE 会话可共享。
func ProvideGrokOAuthService(proxyRepo ProxyRepository, oauthClient GrokOAuthClient, cfg *config.Config, redisClient *redis.Client) *GrokOAuthService {
	svc := NewGrokOAuthService(proxyRepo, oauthClient, cfg)
	// wire.go is depguard-exempt for redis; construct the Redis session store here.
	if redisClient != nil {
		svc = svc.WithSessionStore(xai.NewRedisSessionStore(redisClient))
	}
	return svc
}

// BuildInfo contains build information
type BuildInfo struct {
	Version   string
	BuildType string
}

// ProvidePricingService creates and initializes PricingService
func ProvidePricingService(cfg *config.Config, remoteClient PricingRemoteClient) (*PricingService, error) {
	svc := NewPricingService(cfg, remoteClient)
	if err := svc.Initialize(); err != nil {
		// Pricing service initialization failure should not block startup, use fallback prices
		println("[Service] Warning: Pricing service initialization failed:", err.Error())
	}
	return svc, nil
}

// ProvideKeywordCatalogService 构造本地敏感词库服务并完成一次同步初始化。
// 网关自有的内容审核链路依赖它在启动时就把词库装载进内存，否则首个请求会走空词库。
func ProvideKeywordCatalogService(cfg *config.Config, remoteClient KeywordCatalogRemoteClient) *KeywordCatalogService {
	svc := NewKeywordCatalogService(cfg, remoteClient)
	svc.Initialize()
	return svc
}

// ProvideContentModerationService 构造内容审核服务并接上本地敏感词库目录。
func ProvideContentModerationService(
	settingRepo SettingRepository,
	repo ContentModerationRepository,
	hashCache ContentModerationHashCache,
	groupRepo GroupRepository,
	userRepo UserRepository,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	emailService *EmailService,
	keywordCatalog *KeywordCatalogService,
	db *sql.DB,
) *ContentModerationService {
	svc := NewContentModerationService(settingRepo, repo, hashCache, groupRepo, userRepo, authCacheInvalidator, emailService)
	svc.SetKeywordCatalog(keywordCatalog)
	svc.SetAIStore(NewSQLContentModerationAIStore(db))
	return svc
}

// ProvideUpdateService creates UpdateService with BuildInfo
func ProvideUpdateService(cache UpdateCache, githubClient GitHubReleaseClient, buildInfo BuildInfo) *UpdateService {
	return NewUpdateService(cache, githubClient, buildInfo.Version, buildInfo.BuildType)
}

// ProvideEmailQueueService creates EmailQueueService with default worker count
func ProvideEmailQueueService(emailService *EmailService) *EmailQueueService {
	return NewEmailQueueService(emailService, 3)
}

// ProvideAuthService wires the optional captcha providers into AuthService while
// keeping NewAuthService's public constructor compatible with existing tests.
func ProvideAuthService(
	entClient *dbent.Client,
	userRepo UserRepository,
	redeemRepo RedeemCodeRepository,
	refreshTokenCache RefreshTokenCache,
	cfg *config.Config,
	settingService *SettingService,
	emailService *EmailService,
	turnstileService *TurnstileService,
	tencentCaptchaService *TencentCaptchaService,
	aliyunCaptchaService *AliyunCaptchaService,
	emailQueueService *EmailQueueService,
	promoService *PromoService,
	defaultSubAssigner DefaultSubscriptionAssigner,
	affiliateService *AffiliateService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
) *AuthService {
	svc := NewAuthService(
		entClient,
		userRepo,
		redeemRepo,
		refreshTokenCache,
		cfg,
		settingService,
		emailService,
		turnstileService,
		emailQueueService,
		promoService,
		defaultSubAssigner,
		affiliateService,
		userPlatformQuotaRepo,
	)
	svc.SetTencentCaptchaService(tencentCaptchaService)
	svc.SetAliyunCaptchaService(aliyunCaptchaService)
	return svc
}

// ProvideOAuthRefreshAPI creates OAuthRefreshAPI with the default lock TTL.
func ProvideOAuthRefreshAPI(accountRepo AccountRepository, tokenCache GeminiTokenCache) *OAuthRefreshAPI {
	return NewOAuthRefreshAPI(accountRepo, tokenCache)
}

func ProvideBatchImageModelPricingResolver(resolver *ModelPricingResolver) *BatchImageModelPricingResolver {
	return &BatchImageModelPricingResolver{Resolver: resolver}
}

func ProvideBatchImageCleanupService(repo BatchImageRepository, accountRepo AccountRepository, cfg *config.Config) *BatchImageCleanupService {
	svc := NewBatchImageCleanupService(repo, accountRepo, cfg)
	svc.Start()
	return svc
}

// ProvideOpenAIOAuthService creates OpenAIOAuthService with privacy/account enrichment support.
func ProvideOpenAIOAuthService(
	proxyRepo ProxyRepository,
	oauthClient OpenAIOAuthClient,
	privacyClientFactory PrivacyClientFactory,
) *OpenAIOAuthService {
	svc := NewOpenAIOAuthService(proxyRepo, oauthClient)
	svc.SetPrivacyClientFactory(privacyClientFactory)
	return svc
}

// ProvideTokenRefreshService creates and starts TokenRefreshService
func ProvideTokenRefreshService(
	accountRepo AccountRepository,
	oauthService *OAuthService,
	openaiOAuthService *OpenAIOAuthService,
	geminiOAuthService *GeminiOAuthService,
	antigravityOAuthService *AntigravityOAuthService,
	grokOAuthService *GrokOAuthService,
	cacheInvalidator TokenCacheInvalidator,
	schedulerCache SchedulerCache,
	cfg *config.Config,
	tempUnschedCache TempUnschedCache,
	privacyClientFactory PrivacyClientFactory,
	proxyRepo ProxyRepository,
	refreshAPI *OAuthRefreshAPI,
	runtimeBlocker AccountRuntimeBlocker,
) *TokenRefreshService {
	svc := NewTokenRefreshService(accountRepo, oauthService, openaiOAuthService, geminiOAuthService, antigravityOAuthService, cacheInvalidator, schedulerCache, cfg, tempUnschedCache, grokOAuthService)
	// 注入 OpenAI privacy opt-out 依赖
	svc.SetPrivacyDeps(privacyClientFactory, proxyRepo)
	// 注入统一 OAuth 刷新 API（消除 TokenRefreshService 与 TokenProvider 之间的竞争条件）
	svc.SetRefreshAPI(refreshAPI)
	// 调用侧显式注入后台刷新策略，避免策略漂移
	svc.SetRefreshPolicy(DefaultBackgroundRefreshPolicy())
	svc.SetAccountRuntimeBlocker(runtimeBlocker)
	svc.Start()
	return svc
}

// ProvideClaudeTokenProvider creates ClaudeTokenProvider with OAuthRefreshAPI injection
func ProvideClaudeTokenProvider(
	accountRepo AccountRepository,
	tokenCache GeminiTokenCache,
	oauthService *OAuthService,
	refreshAPI *OAuthRefreshAPI,
) *ClaudeTokenProvider {
	p := NewClaudeTokenProvider(accountRepo, tokenCache, oauthService)
	executor := NewClaudeTokenRefresher(oauthService)
	p.SetRefreshAPI(refreshAPI, executor)
	p.SetRefreshPolicy(ClaudeProviderRefreshPolicy())
	return p
}

// ProvideOpenAITokenProvider creates OpenAITokenProvider with OAuthRefreshAPI injection
func ProvideOpenAITokenProvider(
	accountRepo AccountRepository,
	tokenCache GeminiTokenCache,
	openaiOAuthService *OpenAIOAuthService,
	refreshAPI *OAuthRefreshAPI,
) *OpenAITokenProvider {
	p := NewOpenAITokenProvider(accountRepo, tokenCache, openaiOAuthService)
	executor := NewOpenAITokenRefresher(openaiOAuthService, accountRepo)
	p.SetRefreshAPI(refreshAPI, executor)
	p.SetRefreshPolicy(OpenAIProviderRefreshPolicy())
	return p
}

// ProvideOpenAIQuotaService wires the OpenAI quota query/reset service.
// It depends on the OpenAI token provider for refreshed access tokens and the
// privacy client factory for the impersonated upstream HTTP client.
func ProvideOpenAIQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	tokenProvider *OpenAITokenProvider,
	privacyClientFactory PrivacyClientFactory,
	openAIGatewayService *OpenAIGatewayService,
) *OpenAIQuotaService {
	service := NewOpenAIQuotaService(accountRepo, proxyRepo, tokenProvider, privacyClientFactory)
	service.agentIdentityWS = openAIGatewayService
	return service
}

// ProvideOpenAIQuotaAutoResetService 启动账号级自动用卡队列与补偿扫描。
func ProvideOpenAIQuotaAutoResetService(
	accountRepo AccountRepository,
	quotaService *OpenAIQuotaService,
	rateLimitService *RateLimitService,
	idempotency *IdempotencyCoordinator,
	audit *AuditLogService,
	settingService *SettingService,
	leaderLock LeaderLockCache,
) *OpenAIQuotaAutoResetService {
	service := NewOpenAIQuotaAutoResetService(
		accountRepo,
		quotaService,
		rateLimitService,
		idempotency,
		audit,
		settingService,
		leaderLock,
	)
	service.Start()
	return service
}

// ProvideCodexTailBurstCollector 创建并启动 Codex tail-burst 用量快照采集器。
// 它把 /wham/usage 轮询进账号 Extra，使调度阈值与 tail-burst 评估共用同一组键。
// Redis 为 nil 时跳过跨实例 leader lock（单实例或测试场景）。
func ProvideCodexTailBurstCollector(
	cfg *config.Config,
	settingService *SettingService,
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	openAIQuotaService *OpenAIQuotaService,
	redisClient *redis.Client,
) *CodexTailBurstCollector {
	collector := NewCodexTailBurstCollector(cfg, settingService, accountRepo, usageLogRepo, openAIQuotaService, redisClient)
	collector.Start()
	return collector
}

// ProvideAccountUsageService 保持旧构造函数对测试的兼容，同时把 Grok 被动额度快照
// 与 OpenAI 探测锁注入到运行时实例。
func ProvideAccountUsageService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	usageFetcher ClaudeUsageFetcher,
	geminiQuotaService *GeminiQuotaService,
	antigravityQuotaFetcher *AntigravityQuotaFetcher,
	grokQuotaFetcher *GrokQuotaFetcher,
	grokQuotaService *GrokQuotaService,
	openAIQuotaService *OpenAIQuotaService,
	cache *UsageCache,
	identityCache IdentityCache,
	tlsFPProfileService *TLSFingerprintProfileService,
	openAIProbeLock LeaderLockCache,
	openAIGatewayService *OpenAIGatewayService,
) *AccountUsageService {
	svc := NewAccountUsageService(accountRepo, usageLogRepo, usageFetcher, geminiQuotaService, antigravityQuotaFetcher, cache, identityCache, tlsFPProfileService)
	svc.SetGrokQuotaFetcher(grokQuotaFetcher)
	svc.SetGrokQuotaService(grokQuotaService)
	svc.SetOpenAIQuotaService(openAIQuotaService)
	svc.SetOpenAICodexUsageProbeLock(openAIProbeLock)
	svc.agentIdentityWS = openAIGatewayService
	return svc
}

// ProvidePluginManager 构造插件宿主，并在装配阶段注入「账号目录」能力
// （插件据此按自身声明的能力枚举账号、解析出站身份；见 PluginAccountDirectory）。
//
// 这段注入原先手写在 cmd/server/wire_gen.go 里，重新生成 wire 时会静默丢失，
// 插件将拿不到账号目录而只返回空表；改成 provider 后依赖图可被 wire 复现。
func ProvidePluginManager(
	repo PluginRepository,
	encryptor SecretEncryptor,
	cfg *config.Config,
	hostInfo PluginHostInfo,
	kvStore PluginKVStore,
	accountDirectory *OpenAIGatewayService,
) *PluginManager {
	manager := NewPluginManager(repo, encryptor, cfg, hostInfo, kvStore)
	if accountDirectory != nil {
		manager.SetAccountDirectory(accountDirectory)
	}
	return manager
}

// ProvideAccountTestService 注入 OpenAI 与 Grok 的刷新感知 token provider 到
// 账号健康检查与实时模型同步操作中。
func ProvideAccountTestService(
	accountRepo AccountRepository,
	openAITokenProvider *OpenAITokenProvider,
	geminiTokenProvider *GeminiTokenProvider,
	claudeTokenProvider *ClaudeTokenProvider,
	antigravityGatewayService *AntigravityGatewayService,
	grokTokenProvider *GrokTokenProvider,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
	tlsFPProfileService *TLSFingerprintProfileService,
	openAIGatewayService *OpenAIGatewayService,
	settingService *SettingService,
	pluginManager *PluginManager,
) *AccountTestService {
	svc := NewAccountTestService(accountRepo, openAITokenProvider, geminiTokenProvider, claudeTokenProvider, antigravityGatewayService, httpUpstream, cfg, tlsFPProfileService)
	svc.SetGrokTokenProvider(grokTokenProvider)
	svc.agentIdentityWS = openAIGatewayService
	svc.SetOpenAIGatewayService(openAIGatewayService)
	svc.SetSettingService(settingService)
	svc.SetPluginManager(pluginManager)
	return svc
}

// ProvideGrokQuotaService wires active xAI quota probing through the same
// proxy-aware upstream transport used by real gateway requests.
func ProvideGrokQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	tokenProvider *GrokTokenProvider,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
	usageLogRepo UsageLogRepository,
	settingService *SettingService,
) *GrokQuotaService {
	service := NewGrokQuotaService(accountRepo, proxyRepo, tokenProvider, httpUpstream, cfg, usageLogRepo)
	service.SetSettingService(settingService)
	return service
}

// ProvideCNProviderQuotaService 构造国产供应商 Coding Plan 额度探测服务。
func ProvideCNProviderQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
) *CNProviderQuotaService {
	return NewCNProviderQuotaService(accountRepo, proxyRepo, httpUpstream, cfg)
}

// ProvideCNProviderBalanceService 构造国产供应商余额探测服务。
func ProvideCNProviderBalanceService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
) *CNProviderBalanceService {
	return NewCNProviderBalanceService(accountRepo, proxyRepo, httpUpstream, cfg)
}

// ProvideCNProviderBalanceCheckService 构造并启动周期余额/额度检测任务。
// payg 账号探余额（低余额停调）；coding plan 账号探 5h/weekly 滚动窗口
// （落 extra 快照供调度阈值评估自动停调）。
// 间隔取自 gateway.cn_providers.balance_check_interval_minutes；<=0 或关闭时不启动。
func ProvideCNProviderBalanceCheckService(
	accountRepo AccountRepository,
	balanceService *CNProviderBalanceService,
	quotaService *CNProviderQuotaService,
	cfg *config.Config,
) *CNProviderBalanceCheckService {
	minutes := 10
	if cfg != nil && cfg.Gateway.CNProviders.BalanceCheckIntervalMinutes > 0 {
		minutes = cfg.Gateway.CNProviders.BalanceCheckIntervalMinutes
	}
	svc := NewCNProviderBalanceCheckService(accountRepo, balanceService, quotaService, cfg, time.Duration(minutes)*time.Minute)
	svc.Start()
	return svc
}

// ProvideGeminiTokenProvider creates GeminiTokenProvider with OAuthRefreshAPI injection
func ProvideGeminiTokenProvider(
	accountRepo AccountRepository,
	tokenCache GeminiTokenCache,
	geminiOAuthService *GeminiOAuthService,
	refreshAPI *OAuthRefreshAPI,
) *GeminiTokenProvider {
	p := NewGeminiTokenProvider(accountRepo, tokenCache, geminiOAuthService)
	executor := NewGeminiTokenRefresher(geminiOAuthService)
	p.SetRefreshAPI(refreshAPI, executor)
	p.SetRefreshPolicy(GeminiProviderRefreshPolicy())
	return p
}

// ProvideAntigravityTokenProvider creates AntigravityTokenProvider with OAuthRefreshAPI injection
func ProvideAntigravityTokenProvider(
	accountRepo AccountRepository,
	tokenCache GeminiTokenCache,
	antigravityOAuthService *AntigravityOAuthService,
	refreshAPI *OAuthRefreshAPI,
	tempUnschedCache TempUnschedCache,
) *AntigravityTokenProvider {
	p := NewAntigravityTokenProvider(accountRepo, tokenCache, antigravityOAuthService)
	executor := NewAntigravityTokenRefresher(antigravityOAuthService)
	p.SetRefreshAPI(refreshAPI, executor)
	p.SetRefreshPolicy(AntigravityProviderRefreshPolicy())
	p.SetTempUnschedCache(tempUnschedCache)
	return p
}

// ProvideAntigravityQuotaFetcher 把可变参构造函数适配成固定签名的 Wire provider。
// Wire 无法直接满足可变参依赖（它把末参建模为 slice provider）。
func ProvideAntigravityQuotaFetcher(proxyRepo ProxyRepository, cfg *config.Config) *AntigravityQuotaFetcher {
	return NewAntigravityQuotaFetcher(proxyRepo, cfg)
}

// ProvideGrokTokenProvider wires request-path refresh, distributed locking and
// temporary unscheduling so expired Grok credentials cannot cause retry storms.
func ProvideGrokTokenProvider(
	accountRepo AccountRepository,
	tokenCache GeminiTokenCache,
	grokOAuthService *GrokOAuthService,
	refreshAPI *OAuthRefreshAPI,
	tempUnschedCache TempUnschedCache,
) *GrokTokenProvider {
	p := NewGrokTokenProvider(accountRepo, tokenCache)
	executor := NewGrokTokenRefresher(grokOAuthService)
	p.SetRefreshAPI(refreshAPI, executor)
	p.SetRefreshPolicy(GrokProviderRefreshPolicy())
	p.SetTempUnschedCache(tempUnschedCache)
	return p
}

// ProvideMediaProxy 在引导密钥与配置就绪后构造面向客户端的图片桥接。
// 对象存储可选：没有媒体存储的节点仍可启动并保持普通文本流量，图片 URL 重写
// 退化为尽力而为。
func ProvideMediaProxy(cfg *config.Config, objects SyncMediaObjectStore) *MediaProxy {
	// 媒体 URL 只由区域网关提供。即使两个进程共享同一份配置文件，控制面也必须
	// 保持关闭，否则主站会把响应重写到本地不可用的路由。
	if cfg == nil || cfg.RunMode != config.RunModeGatewayLite || !cfg.GatewayLite.MediaProxyEnabled {
		return nil
	}
	secret := strings.TrimSpace(cfg.JWT.Secret)
	if secret == "" {
		secret = strings.TrimSpace(cfg.Totp.EncryptionKey)
	}
	if secret == "" {
		return nil
	}
	proxy, err := NewMediaProxy(secret, cfg.GatewayLite.MediaProxyPublicURL)
	if err != nil {
		return nil
	}
	if cfg.GatewayLite.MediaProxyTTLSeconds > 0 {
		if err := proxy.SetTTL(time.Duration(cfg.GatewayLite.MediaProxyTTLSeconds) * time.Second); err != nil {
			return nil
		}
	}
	proxy.SetObjectStore(objects)
	return proxy
}

// ProvideSyncMediaCache wires the synchronous image copier independently of
// the durable image-task feature flag. The URL callback uses encrypted object
// tokens for both local and S3-compatible storage, so synchronous responses
// always point at this gateway's protected media route.
func ProvideSyncMediaCache(cfg *config.Config, objects SyncMediaObjectStore, proxy *MediaProxy) *SyncMediaCache {
	if cfg == nil || cfg.RunMode != config.RunModeGatewayLite || proxy == nil || objects == nil || !cfg.GatewayLite.MediaProxyEnabled {
		return nil
	}
	maxBytes := int64(mediaProxyMaxBodySize)
	if cfg.Gateway.ImageTasks.MaxArtifactBytes > 0 && cfg.Gateway.ImageTasks.MaxArtifactBytes < maxBytes {
		maxBytes = cfg.Gateway.ImageTasks.MaxArtifactBytes
	}
	ttl := mediaProxyDefaultTTL
	if cfg.GatewayLite.MediaProxyTTLSeconds > 0 {
		ttl = time.Duration(cfg.GatewayLite.MediaProxyTTLSeconds) * time.Second
	}
	cache, err := NewSyncMediaCache(objects, maxBytes, ttl, SyncMediaCacheDefaultPrefix,
		func(_ context.Context, key string, _ time.Duration, scope string) (string, error) {
			return proxy.IssueObjectURL(key, scope)
		},
	)
	if err != nil {
		return nil
	}
	return cache
}

// ProvideOpenAIGatewayService keeps the existing constructor stable for tests
// and injects Grok's refresh-aware token provider into the shared
// OpenAI-compatible（OpenAI 兼容）request service used at runtime.
func ProvideOpenAIGatewayService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	grokTokenProvider *GrokTokenProvider,
	grokVideoTaskStore GrokVideoTaskStore,
	grokCustomVoiceStore GrokCustomVoiceStore,
	resolver *ModelPricingResolver,
	channelService *ChannelService,
	balanceNotifyService *BalanceNotifyService,
	settingService *SettingService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
	syncMediaCache *SyncMediaCache,
	redisClient *redis.Client,
	tlsFPProfileService *TLSFingerprintProfileService,
) *OpenAIGatewayService {
	svc := NewOpenAIGatewayService(
		accountRepo,
		usageLogRepo,
		usageBillingRepo,
		userRepo,
		userSubRepo,
		userGroupRateRepo,
		cache,
		cfg,
		schedulerSnapshot,
		concurrencyService,
		billingService,
		rateLimitService,
		billingCacheService,
		httpUpstream,
		deferredService,
		openAITokenProvider,
		resolver,
		channelService,
		balanceNotifyService,
		settingService,
		userPlatformQuotaRepo,
	)
	svc.SetGrokTokenProvider(grokTokenProvider)
	svc.SetGrokVideoTaskStore(grokVideoTaskStore)
	svc.SetGrokCustomVoiceStore(grokCustomVoiceStore)
	svc.SetSyncMediaCache(syncMediaCache)
	// OpenAI OAuth 热路径按账号可选启用 TLS 指纹伪装（连接层识别面收窄）。
	svc.SetTLSFingerprintProfileService(tlsFPProfileService)
	redisPrefix := ""
	if cfg != nil {
		redisPrefix = cfg.GatewayLite.RedisPrefix
	}
	configureGrokTeamModelRateLimitStore(redisClient, redisPrefix)
	// [v0.2.6] 常驻启动 Codex 一小时票据 harvester：循环内每周期都检查运行时开关
	// （系统设置界面的开关实时生效，关=空转零开销，开=自动按 (账号,模型) 打票），无需重启服务。
	svc.StartOpenAICodexTicketHarvester()
	return svc
}

// ProvideIPv6EgressService 构建本机 IPv6 出口管理器并在启用时启动
// （识别网卡全局 IPv6 → 源绑定测通 → 注册本地 CONNECT 代理）。
// 启动判断用服务内已合并持久化配置的值（界面保存过的热配置优先于 yaml），
// 否则"界面开启 + 服务重启"会静默停跑。
func ProvideIPv6EgressService(cfg *config.Config, proxyService *ProxyService) *IPv6EgressService {
	svc := NewIPv6EgressService(cfg, proxyService)
	if svc.RuntimeConfig().Enabled {
		svc.Start(context.Background())
	}
	return svc
}

// ProvideGrokVideoTaskRunner starts durable background polling so video tasks
// are settled even when the original API client never calls the status route.
func ProvideGrokVideoTaskRunner(
	store GrokVideoTaskStore,
	gateway *OpenAIGatewayService,
	apiKeyRepo APIKeyRepository,
	userSubRepo UserSubscriptionRepository,
	apiKeyService *APIKeyService,
) *GrokVideoTaskRunner {
	runner := NewGrokVideoTaskRunner(store, gateway, apiKeyRepo, userSubRepo, apiKeyService, GrokVideoTaskPollInterval)
	runner.Start()
	return runner
}

// ProvideDashboardAggregationService 创建并启动仪表盘聚合服务
func ProvideDashboardAggregationService(repo DashboardAggregationRepository, timingWheel *TimingWheelService, lockCache LeaderLockCache, db *sql.DB, cfg *config.Config) *DashboardAggregationService {
	svc := NewDashboardAggregationService(repo, timingWheel, cfg)
	svc.SetLeaderLock(lockCache, db)
	svc.Start()
	return svc
}

// ProvideUsageCleanupService 创建并启动使用记录清理任务服务
func ProvideUsageCleanupService(repo UsageCleanupRepository, timingWheel *TimingWheelService, dashboardAgg *DashboardAggregationService, cfg *config.Config) *UsageCleanupService {
	svc := NewUsageCleanupService(repo, timingWheel, dashboardAgg, cfg)
	svc.Start()
	return svc
}

// ProvideAccountExpiryService creates and starts AccountExpiryService.
func ProvideAccountExpiryService(accountRepo AccountRepository) *AccountExpiryService {
	svc := NewAccountExpiryService(accountRepo, time.Minute)
	svc.Start()
	return svc
}

// ProvideOpenAICodexVersionSyncService creates and starts OpenAICodexVersionSyncService.
// 出站 Codex 身份的版本号靠它跟随官方发布，无需为了跟版本而发新版本；面板可关闭。
func ProvideOpenAICodexVersionSyncService(
	settingRepo SettingRepository,
	settingService *SettingService,
	githubClient GitHubReleaseClient,
) *OpenAICodexVersionSyncService {
	svc := NewOpenAICodexVersionSyncService(settingRepo, settingService, githubClient, openAICodexVersionSyncInterval)
	svc.Start()
	return svc
}

// ProvideProxyExpiryService creates and starts ProxyExpiryService.
func ProvideProxyExpiryService(proxyRepo ProxyRepository) *ProxyExpiryService {
	svc := NewProxyExpiryService(proxyRepo, time.Minute)
	svc.Start()
	return svc
}

// ProvideSubscriptionExpiryService creates and starts SubscriptionExpiryService.
func ProvideSubscriptionExpiryService(userSubRepo UserSubscriptionRepository, settingRepo SettingRepository, notificationEmailService *NotificationEmailService, lockCache LeaderLockCache, db *sql.DB) *SubscriptionExpiryService {
	svc := NewSubscriptionExpiryService(userSubRepo, time.Minute)
	svc.SetSettingRepository(settingRepo)
	svc.SetNotificationEmailService(notificationEmailService)
	svc.SetLeaderLock(lockCache, db)
	svc.Start()
	return svc
}

// ProvideTimingWheelService creates and starts TimingWheelService
func ProvideTimingWheelService() (*TimingWheelService, error) {
	svc, err := NewTimingWheelService()
	if err != nil {
		return nil, err
	}
	svc.Start()
	return svc, nil
}

// ProvideDeferredService creates and starts DeferredService
func ProvideDeferredService(accountRepo AccountRepository, timingWheel *TimingWheelService) *DeferredService {
	svc := NewDeferredService(accountRepo, timingWheel, 10*time.Second)
	svc.Start()
	return svc
}

// ProvideConcurrencyService creates ConcurrencyService and starts slot cleanup worker.
func ProvideConcurrencyService(cache ConcurrencyCache, accountRepo AccountRepository, cfg *config.Config) *ConcurrencyService {
	svc := NewConcurrencyService(cache)
	if cfg != nil && cfg.RunMode == config.RunModeGatewayLite {
		// 请求完成后的槽位释放不参与响应关键路径；队列满时服务内部会同步兜底，绝不丢释放。
		svc.StartAccountSlotReleaseWorker()
		svc.StartUserAPIKeySlotReleaseWorker()
	}
	if err := svc.CleanupStaleProcessSlots(context.Background()); err != nil {
		logger.LegacyPrintf("service.concurrency", "Warning: startup cleanup stale process slots failed: %v", err)
	}
	if cfg != nil {
		svc.SetAccountLoadBatchCacheTTL(time.Duration(cfg.Gateway.Scheduling.LoadBatchCacheTTLMS) * time.Millisecond)
		svc.StartSlotCleanupWorker(accountRepo, cfg.Gateway.Scheduling.SlotCleanupInterval)
	}
	return svc
}

// ProvideUserMessageQueueService 创建用户消息串行队列服务并启动清理 worker
func ProvideUserMessageQueueService(cache UserMsgQueueCache, rpmCache RPMCache, cfg *config.Config) *UserMessageQueueService {
	svc := NewUserMessageQueueService(cache, rpmCache, &cfg.Gateway.UserMessageQueue)
	if cfg.Gateway.UserMessageQueue.CleanupIntervalSeconds > 0 {
		svc.StartCleanupWorker(time.Duration(cfg.Gateway.UserMessageQueue.CleanupIntervalSeconds) * time.Second)
	}
	return svc
}

// ProvideSchedulerSnapshotService creates and starts SchedulerSnapshotService.
func ProvideSchedulerSnapshotService(
	cache SchedulerCache,
	outboxRepo SchedulerOutboxRepository,
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	cfg *config.Config,
) *SchedulerSnapshotService {
	svc := NewSchedulerSnapshotService(cache, outboxRepo, accountRepo, groupRepo, cfg)
	svc.Start()
	return svc
}

// ProvideRateLimitService creates RateLimitService with optional dependencies.
func ProvideRateLimitService(
	accountRepo AccountRepository,
	usageRepo UsageLogRepository,
	cfg *config.Config,
	geminiQuotaService *GeminiQuotaService,
	tempUnschedCache TempUnschedCache,
	timeoutCounterCache TimeoutCounterCache,
	openAI403CounterCache OpenAI403CounterCache,
	settingService *SettingService,
	tokenCacheInvalidator TokenCacheInvalidator,
	ollamaCloudUsage *OllamaCloudUsageService,
) *RateLimitService {
	svc := NewRateLimitService(accountRepo, usageRepo, cfg, geminiQuotaService, tempUnschedCache)
	if healthCache, ok := tempUnschedCache.(OpenAIAPIKeyHealthCache); ok {
		svc.SetOpenAIAPIKeyHealthCache(healthCache)
	}
	svc.SetTimeoutCounterCache(timeoutCounterCache)
	svc.SetOpenAI403CounterCache(openAI403CounterCache)
	svc.SetSettingService(settingService)
	svc.SetTokenCacheInvalidator(tokenCacheInvalidator)
	svc.SetOllamaCloudUsageProbeScheduler(ollamaCloudUsage)
	return svc
}

// ProvideOpsMetricsCollector creates and starts OpsMetricsCollector.
func ProvideOpsMetricsCollector(
	opsRepo OpsRepository,
	settingRepo SettingRepository,
	accountRepo AccountRepository,
	concurrencyService *ConcurrencyService,
	db *sql.DB,
	redisClient *redis.Client,
	cfg *config.Config,
) *OpsMetricsCollector {
	collector := NewOpsMetricsCollector(opsRepo, settingRepo, accountRepo, concurrencyService, db, redisClient, cfg)
	collector.Start()
	return collector
}

// ProvideOpsAggregationService creates and starts OpsAggregationService (hourly/daily pre-aggregation).
func ProvideOpsAggregationService(
	opsRepo OpsRepository,
	settingRepo SettingRepository,
	db *sql.DB,
	redisClient *redis.Client,
	cfg *config.Config,
) *OpsAggregationService {
	svc := NewOpsAggregationService(opsRepo, settingRepo, db, redisClient, cfg)
	svc.Start()
	return svc
}

// ProvideOpsAlertEvaluatorService creates and starts OpsAlertEvaluatorService.
func ProvideOpsAlertEvaluatorService(
	opsService *OpsService,
	opsRepo OpsRepository,
	emailService *EmailService,
	redisClient *redis.Client,
	cfg *config.Config,
	proxyRepo ProxyRepository,
) *OpsAlertEvaluatorService {
	svc := NewOpsAlertEvaluatorService(opsService, opsRepo, emailService, redisClient, cfg, proxyRepo)
	svc.Start()
	return svc
}

// ProvideOpsCleanupService creates and starts OpsCleanupService (cron scheduled).
// channelMonitorSvc 让维护任务（聚合 + 历史/聚合软删）跟随 ops 清理 cron 一起跑，
// 共享 leader lock + heartbeat。
// settingRepo 让 cleanup service 自己读 ops_advanced_settings.data_retention 覆盖 cfg；
// opsService 用来反向注入 cleanup hook，以便 UI 改清理设置时能 Reload cron。
func ProvideOpsCleanupService(
	opsRepo OpsRepository,
	db *sql.DB,
	redisClient *redis.Client,
	cfg *config.Config,
	channelMonitorSvc *ChannelMonitorService,
	settingRepo SettingRepository,
	opsService *OpsService,
) *OpsCleanupService {
	svc := NewOpsCleanupService(opsRepo, db, redisClient, cfg, channelMonitorSvc, settingRepo)
	svc.Start()
	if opsService != nil {
		opsService.SetCleanupReloader(svc)
	}
	return svc
}

func ProvideOpsSystemLogSink(opsRepo OpsRepository) *OpsSystemLogSink {
	sink := NewOpsSystemLogSink(opsRepo)
	sink.Start()
	logger.SetSink(sink)
	return sink
}

// ProvideAuditLogService 创建操作审计日志服务并启动异步写入与保留期清理协程。
// 停止逻辑挂在 cmd/server 的 provideCleanup。
func ProvideAuditLogService(repo AuditLogRepository, settingService *SettingService) *AuditLogService {
	svc := NewAuditLogService(repo, settingService)
	svc.Start()
	return svc
}

func buildIdempotencyConfig(cfg *config.Config) IdempotencyConfig {
	idempotencyCfg := DefaultIdempotencyConfig()
	if cfg != nil {
		if cfg.Idempotency.DefaultTTLSeconds > 0 {
			idempotencyCfg.DefaultTTL = time.Duration(cfg.Idempotency.DefaultTTLSeconds) * time.Second
		}
		if cfg.Idempotency.SystemOperationTTLSeconds > 0 {
			idempotencyCfg.SystemOperationTTL = time.Duration(cfg.Idempotency.SystemOperationTTLSeconds) * time.Second
		}
		if cfg.Idempotency.ProcessingTimeoutSeconds > 0 {
			idempotencyCfg.ProcessingTimeout = time.Duration(cfg.Idempotency.ProcessingTimeoutSeconds) * time.Second
		}
		if cfg.Idempotency.FailedRetryBackoffSeconds > 0 {
			idempotencyCfg.FailedRetryBackoff = time.Duration(cfg.Idempotency.FailedRetryBackoffSeconds) * time.Second
		}
		if cfg.Idempotency.MaxStoredResponseLen > 0 {
			idempotencyCfg.MaxStoredResponseLen = cfg.Idempotency.MaxStoredResponseLen
		}
		idempotencyCfg.ObserveOnly = cfg.Idempotency.ObserveOnly
	}
	return idempotencyCfg
}

func ProvideIdempotencyCoordinator(repo IdempotencyRepository, cfg *config.Config) *IdempotencyCoordinator {
	coordinator := NewIdempotencyCoordinator(repo, buildIdempotencyConfig(cfg))
	SetDefaultIdempotencyCoordinator(coordinator)
	return coordinator
}

func ProvideSystemOperationLockService(repo IdempotencyRepository, cfg *config.Config) *SystemOperationLockService {
	return NewSystemOperationLockService(repo, buildIdempotencyConfig(cfg))
}

func ProvideIdempotencyCleanupService(repo IdempotencyRepository, cfg *config.Config) *IdempotencyCleanupService {
	svc := NewIdempotencyCleanupService(repo, cfg)
	svc.Start()
	return svc
}

// ProvideScheduledTestService creates ScheduledTestService.
func ProvideScheduledTestService(
	planRepo ScheduledTestPlanRepository,
	resultRepo ScheduledTestResultRepository,
) *ScheduledTestService {
	return NewScheduledTestService(planRepo, resultRepo)
}

// ProvideScheduledTestRunnerService creates and starts ScheduledTestRunnerService.
func ProvideScheduledTestRunnerService(
	planRepo ScheduledTestPlanRepository,
	scheduledSvc *ScheduledTestService,
	accountTestSvc *AccountTestService,
	rateLimitSvc *RateLimitService,
	cfg *config.Config,
) *ScheduledTestRunnerService {
	svc := NewScheduledTestRunnerService(planRepo, scheduledSvc, accountTestSvc, rateLimitSvc, cfg)
	svc.Start()
	return svc
}

// ProvideOpsScheduledReportService creates and starts OpsScheduledReportService.
func ProvideOpsScheduledReportService(
	opsService *OpsService,
	userService *UserService,
	emailService *EmailService,
	redisClient *redis.Client,
	cfg *config.Config,
) *OpsScheduledReportService {
	svc := NewOpsScheduledReportService(opsService, userService, emailService, redisClient, cfg)
	svc.Start()
	return svc
}

// ProvideAPIKeyAuthCacheInvalidator 提供 API Key 认证缓存失效能力
func ProvideAPIKeyAuthCacheInvalidator(apiKeyService *APIKeyService) APIKeyAuthCacheInvalidator {
	// Start Pub/Sub subscriber for L1 cache invalidation across instances
	apiKeyService.StartAuthCacheInvalidationSubscriber(context.Background())
	return apiKeyService
}

// ProvideImageStorageSettingService 构造异步生图对象存储的后台设置服务。
//
// config.yaml 里的 image_storage 作为回落：后台从未保存过设置时沿用它，
// 使升级前已通过配置文件开启该功能的部署不被打断。
func ProvideImageStorageSettingService(
	settingRepo SettingRepository,
	encryptor SecretEncryptor,
	backup *BackupService,
	factory ImageStorageFactory,
	cfg *config.Config,
) *ImageStorageSettingService {
	if cfg.ImageStorage.Enabled && !cfg.ImageStorage.Active() {
		// 列出具体缺失的键。若这些键其实已在环境变量里设过，说明它们没被读进来，
		// 请确认 setDefaults 中已为其注册默认值（见 config.setEnvReachableDefaults）。
		logger.L().Warn("image_storage.enabled is true in config but object storage is not fully configured; configure it in the admin UI or complete the config file",
			zap.Strings("missing_keys", cfg.ImageStorage.MissingCredentialKeys()))
	}
	return NewImageStorageSettingService(settingRepo, encryptor, backup, factory, cfg.ImageStorage)
}

// ProvideImageTaskService 构造异步图片任务服务。
//
// 对象存储是异步图片任务的启用前提：仅当开关打开且凭证齐全时功能才可用，否则整体禁用
// （handler 返回 404，不创建任务、不写 Redis），从而避免大 base64 结果撑爆 Redis。
// 启用状态由 settings 服务在运行时解析，因此后台改开关后无需重启即可生效。
func ProvideImageTaskService(store ImageTaskStore, settings *ImageStorageSettingService) *ImageTaskService {
	return NewImageTaskServiceWithResolver(store, settings.Resolver(), defaultImageTaskTTL, defaultImageTaskExecutionTimeout)
}

// ProvideBackupService creates and starts BackupService
func ProvideBackupService(
	settingRepo SettingRepository,
	cfg *config.Config,
	encryptor SecretEncryptor,
	storeFactory BackupObjectStoreFactory,
	dumper DBDumper,
	lockCache LeaderLockCache,
	db *sql.DB,
) *BackupService {
	svc := NewBackupService(settingRepo, cfg, encryptor, storeFactory, dumper)
	svc.SetLeaderLock(lockCache, db)
	svc.Start()
	return svc
}

// ProvideOpsService constructs OpsService and wires the SettingService-backed quota
// auto-pause cache sink. Mirrors the SetCleanupReloader pattern: OpsService doesn't
// hold a *SettingService reference, but wire injects a tiny callback so writes to
// ops_advanced_settings immediately propagate into the scheduler hot-path cache.
func ProvideOpsService(
	opsRepo OpsRepository,
	settingRepo SettingRepository,
	cfg *config.Config,
	accountRepo AccountRepository,
	userRepo UserRepository,
	concurrencyService *ConcurrencyService,
	gatewayService *GatewayService,
	openAIGatewayService *OpenAIGatewayService,
	geminiCompatService *GeminiMessagesCompatService,
	antigravityGatewayService *AntigravityGatewayService,
	systemLogSink *OpsSystemLogSink,
	settingService *SettingService,
	authCacheInvalidationWorker *AuthCacheInvalidationWorker,
	apiKeyService *APIKeyService,
) *OpsService {
	svc := NewOpsService(
		opsRepo,
		settingRepo,
		cfg,
		accountRepo,
		userRepo,
		concurrencyService,
		gatewayService,
		openAIGatewayService,
		geminiCompatService,
		antigravityGatewayService,
		systemLogSink,
	)
	if settingService != nil {
		svc.SetOpenAIQuotaAutoPauseSettingsSink(settingService.SetOpenAIQuotaAutoPauseSettings)
		// Optional warm-up so the first scheduled request after process start observes
		// a populated cache rather than zero defaults. Best-effort, sync-bounded.
		settingService.WarmOpenAIQuotaAutoPauseSettings(context.Background())
		// 网关自有的 tail-burst 也走同一条设置回流通道。
		svc.SetCodexTailBurstSettingsSink(settingService.SetCodexTailBurstSettings)
		// Tail-burst is consulted on the scheduler hot path. Warm its persisted value
		// before requests arrive so a restart cannot briefly use the static fallback.
		settingService.WarmCodexTailBurstSettings(context.Background())
	}
	svc.authCacheInvalidationWorker = authCacheInvalidationWorker
	svc.apiKeyService = apiKeyService
	svc.StartRuntimeSettingsRefresh(context.Background())
	return svc
}

// ProvideOpsIngressRejectAggregator starts the bounded security aggregation
// runtime and attaches it to OpsService, which is the middleware recorder.
func ProvideOpsIngressRejectAggregator(opsRepo OpsRepository, opsService *OpsService) *OpsIngressRejectAggregator {
	repo, ok := opsRepo.(OpsIngressRejectRepository)
	if !ok {
		return nil
	}
	aggregator := NewOpsIngressRejectAggregator(repo)
	aggregator.Start()
	opsService.SetIngressRejectAggregator(aggregator)
	return aggregator
}

// ProvideSettingService wires SettingService with group reader and proxy repo.
func ProvideSettingService(settingRepo SettingRepository, groupRepo GroupRepository, proxyRepo ProxyRepository, cfg *config.Config) *SettingService {
	svc := NewSettingService(settingRepo, cfg)
	svc.SetDefaultSubscriptionGroupReader(groupRepo)
	svc.SetProxyRepository(proxyRepo)
	if err := svc.LoadForwardedClientIPSettings(context.Background()); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: load forwarded client IP settings failed: %v", err)
	}
	if err := svc.MigrateOpenAIAllowClaudeCodeCodexPluginSetting(context.Background()); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: migrate openai allow Claude Code Codex plugin setting failed: %v", err)
	}
	if err := svc.MigrateCodexBodyFingerprintToSignals(context.Background()); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: migrate codex body fingerprint to signals failed: %v", err)
	}
	if err := svc.MigrateGrokDefaultTextModel(context.Background()); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: migrate Grok default text model failed: %v", err)
	}
	// 网关自有的启动预热：金额结算、组合分组、上游计费、OpenAI 调度策略、
	// Grok 模型映射、Codex 客户端准入、API Key ACL 转发 IP、自定义错误文案。
	// 这些都只读进程内快照，超时即回落到安全默认值，绝不阻塞启动。
	if err := svc.LoadAPIKeyACLTrustForwardedIPSetting(context.Background()); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: load api key acl forwarded ip setting failed: %v", err)
	}
	customErrorWarmCtx, customErrorWarmCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := svc.WarmGatewayCustomErrorMessage(customErrorWarmCtx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm gateway custom error message failed; original errors remain active: %v", err)
	}
	customErrorWarmCancel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := svc.WarmBillingSettlementSettings(ctx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm billing settlement setting failed; legacy mode remains active: %v", err)
		go svc.WarmBillingSettlementSettingsUntilReady(context.Background(), 5*time.Second)
	}
	if err := svc.WarmCompositeGroupsSettings(ctx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm composite groups setting failed; startup config remains active: %v", err)
		go svc.WarmCompositeGroupsSettingsUntilReady(context.Background(), 5*time.Second)
	}
	if err := svc.WarmGrokVideoTransformsSettings(ctx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm grok video transforms setting failed; startup config remains active: %v", err)
		go svc.WarmGrokVideoTransformsSettingsUntilReady(context.Background(), 5*time.Second)
	}
	if err := svc.WarmUpstreamBillingRuntimeSettings(ctx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm upstream billing settings failed; auto probe and cost scheduling remain disabled: %v", err)
	}
	if err := svc.WarmOpenAISchedulerRuntimePolicy(ctx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm OpenAI scheduler runtime policy failed; startup-safe defaults remain active: %v", err)
	}
	if err := svc.WarmGrokModelMappingSettings(ctx); err != nil {
		logger.LegacyPrintf("service.setting", "Warning: warm Grok model mapping settings failed; safe defaults remain active: %v", err)
	}
	codexCtx, cancelCodex := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelCodex()
	if err := svc.WarmCodexClientAdmissionSettings(codexCtx); err != nil {
		// audit-only is the constructor default, so a temporary DB failure can
		// never unexpectedly block Codex clients during startup.
		logger.LegacyPrintf("service.setting", "Warning: warm Codex client admission settings failed; audit-only default remains active: %v", err)
		go svc.WarmCodexClientAdmissionSettingsUntilReady(context.Background(), 5*time.Second)
	}
	antigravity.SetUserAgentVersionResolver(svc.GetAntigravityUserAgentVersion)
	// enforceCodexIdentityHeaders 是所有 Codex 出站路径共用的纯函数收口点，拿不到 ctx，
	// 故注入无参解析器；解析器内部自带 60s TTL 缓存，热路径不触库。
	SetCodexCanonicalUserAgentResolver(func() string {
		return svc.GetOpenAICodexCanonicalUserAgent(context.Background())
	})
	return svc
}

// ProvideUpstreamBillingProbeRunner 构造并启动上游计费探测后台任务。
func ProvideUpstreamBillingProbeRunner(
	accountRepo AccountRepository,
	probe *UpstreamBillingProbeService,
	lockCache LeaderLockCache,
	cfg *config.Config,
) *UpstreamBillingProbeRunner {
	runner := NewUpstreamBillingProbeRunner(accountRepo, probe, lockCache, cfg)
	runner.Start()
	return runner
}

// ProvideBillingCacheService wires BillingCacheService with its RPM dependencies.
func ProvideBillingCacheService(
	cache BillingCache,
	userRepo UserRepository,
	subRepo UserSubscriptionRepository,
	apiKeyRepo APIKeyRepository,
	rpmCache UserRPMCache,
	rateRepo UserGroupRateRepository,
	cfg *config.Config,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
) *BillingCacheService {
	return NewBillingCacheService(cache, userRepo, subRepo, apiKeyRepo, rpmCache, rateRepo, cfg, userPlatformQuotaRepo)
}

// ProvideAPIKeyService wires APIKeyService and connects rate-limit cache invalidation.
func ProvideAPIKeyService(
	apiKeyRepo APIKeyRepository,
	userRepo UserRepository,
	groupRepo GroupRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache APIKeyCache,
	cfg *config.Config,
	billingCacheService *BillingCacheService,
	concurrencyService *ConcurrencyService,
) *APIKeyService {
	svc := NewAPIKeyService(apiKeyRepo, userRepo, groupRepo, userSubRepo, userGroupRateRepo, cache, cfg)
	svc.SetRateLimitCacheInvalidator(billingCacheService)
	svc.SetConcurrencyService(concurrencyService)
	return svc
}

// ProviderSet is the Wire provider set for all services
var ProviderSet = wire.NewSet(
	// Core services
	ProvideAuthService,
	NewPasskeyService,
	NewUserService,
	ProvideAPIKeyService,
	ProvideAPIKeyAuthCacheInvalidator,
	ProvideAuthCacheInvalidationWorker,
	NewGroupService,
	NewCompositeRouteResolver,
	NewAccountService,
	NewProxyService,
	ProvideIPv6EgressService,
	NewRedeemService,
	NewPromoService,
	NewUsageService,
	NewDashboardService,
	ProvidePricingService,
	ProvideKeywordCatalogService,
	NewBillingService,
	ProvideBillingCacheService,
	NewAnnouncementService,
	NewAdminService,
	ProvideOperationAuditService,
	NewGatewayService,
	ProvideOpenAIGatewayService,
	ProvideMediaProxy,
	ProvideSyncMediaCache,
	ProvideGrokVideoTaskRunner,
	ProvideImageStorageSettingService,
	ProvideImageTaskService,
	ProvideBatchImageModelPricingResolver,
	NewBatchImagePublicService,
	NewBatchImageDownloadService,
	ProvideBatchImageCleanupService,
	ProvideBatchImageWorkerRuntime,
	wire.Bind(new(AccountRuntimeBlocker), new(*OpenAIGatewayService)),
	NewOAuthService,
	ProvideOpenAIOAuthService,
	ProvideGrokOAuthService,
	wire.Bind(new(GrokOAuthTokenService), new(*GrokOAuthService)),
	NewGeminiOAuthService,
	NewGeminiQuotaService,
	NewCompositeTokenCacheInvalidator,
	wire.Bind(new(TokenCacheInvalidator), new(*CompositeTokenCacheInvalidator)),
	NewAntigravityOAuthService,
	ProvideOAuthRefreshAPI,
	ProvideGeminiTokenProvider,
	NewGeminiMessagesCompatService,
	ProvideAntigravityTokenProvider,
	ProvideGrokTokenProvider,
	ProvideOpenAITokenProvider,
	ProvideOpenAIQuotaService,
	ProvideOpenAIQuotaAutoResetService,
	ProvideCodexTailBurstCollector,
	ProvideGrokQuotaService,
	ProvideCNProviderQuotaService,
	ProvideCNProviderBalanceService,
	ProvideCNProviderBalanceCheckService,
	ProvideClaudeTokenProvider,
	NewAntigravityGatewayService,
	ProvideRateLimitService,
	ProvideAccountUsageService,
	ProvideAccountTestService,
	ProvideUpstreamBillingProbeService,
	ProvideUpstreamBillingProbeRunner,
	ProvideOllamaCloudUsageService,
	ProvideSettingService,
	NewDataManagementService,
	ProvideBackupService,
	ProvideOpsSystemLogSink,
	ProvideOpsService,
	ProvideOpsIngressRejectAggregator,
	ProvideAuditLogService,
	ProvideOpsMetricsCollector,
	ProvideOpsAggregationService,
	ProvideOpsAlertEvaluatorService,
	ProvideOpsCleanupService,
	ProvideOpsScheduledReportService,
	NewEmailService,
	NewNotificationEmailService,
	ProvideEmailQueueService,
	NewTurnstileService,
	NewTencentCaptchaService,
	NewAliyunCaptchaService,
	NewSubscriptionService,
	wire.Bind(new(DefaultSubscriptionAssigner), new(*SubscriptionService)),
	ProvideConcurrencyService,
	ProvideUserMessageQueueService,
	NewUsageRecordWorkerPool,
	ProvideSchedulerSnapshotService,
	NewIdentityService,
	NewCRSSyncService,
	ProvideUpdateService,
	ProvideTokenRefreshService,
	ProvideOpenAIUpstreamTimezoneSyncService,
	wire.Bind(new(GrokOAuthReconciler), new(*TokenRefreshService)),
	ProvideAccountExpiryService,
	ProvideOpenAICodexVersionSyncService,
	ProvideProxyExpiryService,
	ProvideSubscriptionExpiryService,
	ProvideTimingWheelService,
	ProvideDashboardAggregationService,
	ProvideUsageCleanupService,
	ProvideDeferredService,
	ProvideAntigravityQuotaFetcher,
	NewGrokQuotaFetcher,
	NewUserAttributeService,
	NewUsageCache,
	NewTotpService,
	NewErrorPassthroughService,
	NewTLSFingerprintProfileService,
	ProvidePluginManager,
	NewDigestSessionStore,
	ProvideIdempotencyCoordinator,
	ProvideSystemOperationLockService,
	ProvideIdempotencyCleanupService,
	ProvideScheduledTestService,
	ProvideScheduledTestRunnerService,
	NewGroupCapacityService,
	NewChannelService,
	wire.Bind(new(ChannelCacheInvalidator), new(*ChannelService)),
	NewModelPricingResolver,
	NewModelPlazaService,
	ProvideContentModerationService,
	NewAffiliateService,
	ProvidePaymentConfigService,
	ProvidePaymentService,
	ProvidePaymentOrderExpiryService,
	ProvideBalanceNotifyService,
	ProvideChannelMonitorService,
	ProvideChannelMonitorRunner,
	NewChannelMonitorQuotaFetcher,
	ProvideChannelMonitorV2Service,
	ProvideChannelMonitorV2Aggregator,
	NewChannelMonitorRequestTemplateService,
	ProvideUserPlatformQuotaUsageFlusher,
	ProvideImageTaskConfig,
	NewGatewayImageTaskBilling,
)

// ProvideUserPlatformQuotaUsageFlusher 创建并启动 UserPlatformQuotaUsageFlusher。
func ProvideUserPlatformQuotaUsageFlusher(cfg *config.Config, cache BillingCache, quotaRepo UserPlatformQuotaRepository, tw *TimingWheelService) *UserPlatformQuotaUsageFlusher {
	svc := NewUserPlatformQuotaUsageFlusher(cfg, cache, quotaRepo, tw)
	svc.Start()
	return svc
}

// ProvidePaymentConfigService wraps NewPaymentConfigService to accept the named
// payment.EncryptionKey type instead of raw []byte, avoiding Wire ambiguity.
func ProvidePaymentConfigService(entClient *dbent.Client, settingRepo SettingRepository, key payment.EncryptionKey) *PaymentConfigService {
	return NewPaymentConfigService(entClient, settingRepo, []byte(key))
}

// ProvideBalanceNotifyService creates BalanceNotifyService
func ProvideBalanceNotifyService(emailService *EmailService, settingRepo SettingRepository, accountRepo AccountRepository, notificationEmailService *NotificationEmailService) *BalanceNotifyService {
	svc := NewBalanceNotifyService(emailService, settingRepo, accountRepo)
	svc.SetNotificationEmailService(notificationEmailService)
	return svc
}

// ProvidePaymentService creates PaymentService and attaches notification email delivery.
func ProvidePaymentService(entClient *dbent.Client, registry *payment.Registry, loadBalancer payment.LoadBalancer, redeemService *RedeemService, subscriptionSvc *SubscriptionService, configService *PaymentConfigService, userRepo UserRepository, groupRepo GroupRepository, affiliateService *AffiliateService, notificationEmailService *NotificationEmailService) *PaymentService {
	svc := NewPaymentService(entClient, registry, loadBalancer, redeemService, subscriptionSvc, configService, userRepo, groupRepo, affiliateService)
	svc.SetNotificationEmailService(notificationEmailService)
	return svc
}

// ProvidePaymentOrderExpiryService creates and starts PaymentOrderExpiryService.
func ProvidePaymentOrderExpiryService(paymentSvc *PaymentService, lockCache LeaderLockCache, db *sql.DB) *PaymentOrderExpiryService {
	svc := NewPaymentOrderExpiryService(paymentSvc, 60*time.Second)
	svc.SetLeaderLock(lockCache, db)
	svc.Start()
	return svc
}

// ProvideChannelMonitorService 创建渠道监控服务（CRUD + RunCheck + 用户视图聚合）。
// 加密器复用 wire 中已注入的 SecretEncryptor（AES-256-GCM）。
// settingService gates RunCheck via channel_monitor_enabled + channel_monitor_mode.
func ProvideChannelMonitorService(
	repo ChannelMonitorRepository,
	encryptor SecretEncryptor,
	settingService *SettingService,
) *ChannelMonitorService {
	svc := NewChannelMonitorService(repo, encryptor)
	svc.SetRuntimeReader(settingService)
	return svc
}

// ProvideChannelMonitorRunner 创建并启动渠道监控调度器。
// 通过 SetScheduler 注入回 service 后再 Start，确保启动时加载所有 enabled monitor，
// 后续 CRUD 也能即时同步任务表。Runner.Stop 由 cleanup function 调用。
// settingService 用于 runner 每次 fire 读取功能开关。
// quotaFetcher（账号侧用量聚合）也在此注入：accountUsage/CN 服务在 wire 图中
// 晚于 channelMonitorService 构造，走 setter 注入避免调整既有构造顺序。
func ProvideChannelMonitorRunner(
	svc *ChannelMonitorService,
	settingService *SettingService,
	quotaFetcher *ChannelMonitorQuotaFetcher,
) *ChannelMonitorRunner {
	r := NewChannelMonitorRunner(svc, settingService)
	if svc != nil {
		// Ensure runtime reader is set even if ProvideChannelMonitorService
		// was constructed without settings (tests / alternate providers).
		svc.SetRuntimeReader(settingService)
		svc.SetScheduler(r)
		svc.SetQuotaFetcher(quotaFetcher)
	}
	r.Start()
	return r
}

// ProvideChannelMonitorV2Service wires settings for user-facing privacy flags
// (e.g. hide RPM/TPM throughput).
func ProvideChannelMonitorV2Service(repo ChannelMonitorV2Repository, settingService *SettingService) *ChannelMonitorV2Service {
	svc := NewChannelMonitorV2Service(repo)
	svc.SetRuntimeReader(settingService)
	return svc
}

// ProvideChannelMonitorV2Aggregator starts the passive minute-rollup worker.
// Aggregation only runs when channel_monitor_enabled=true and mode=v2 (and V2 config enabled).
// Set CHANNEL_MONITOR_V2_DISABLE_AGGREGATOR=1 to skip Start (local demo with seeded facts).
func ProvideChannelMonitorV2Aggregator(repo ChannelMonitorV2Repository, db *sql.DB, settingService *SettingService) *ChannelMonitorV2Aggregator {
	aggregator := NewChannelMonitorV2Aggregator(repo, db, settingService)
	if os.Getenv("CHANNEL_MONITOR_V2_DISABLE_AGGREGATOR") == "1" {
		return aggregator
	}
	aggregator.Start()
	return aggregator
}
