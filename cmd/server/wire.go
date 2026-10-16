//go:build wireinject
// +build wireinject

package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/server"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/google/wire"
	"github.com/redis/go-redis/v9"
)

type Application struct {
	Server        *http.Server
	PromptAudit   *securityaudit.PromptService
	PluginManager *service.PluginManager
	Cleanup       func()
}

func initializeApplication(buildInfo handler.BuildInfo) (*Application, error) {
	wire.Build(
		// Infrastructure layer ProviderSets
		config.ProviderSet,

		// Business layer ProviderSets
		repository.ProviderSet,
		service.ProviderSet,
		securityaudit.ProviderSet,
		payment.ProviderSet,
		middleware.ProviderSet,
		handler.ProviderSet,

		// Server layer ProviderSet
		server.ProviderSet,

		// Privacy client factory for OpenAI training opt-out
		providePrivacyClientFactory,

		// BuildInfo provider
		provideServiceBuildInfo,
		providePluginHostInfo,

		// Cleanup function provider
		provideCleanup,

		// Application struct
		wire.Struct(new(Application), "Server", "PromptAudit", "PluginManager", "Cleanup"),
	)
	return nil, nil
}

func providePrivacyClientFactory() service.PrivacyClientFactory {
	return repository.CreatePrivacyReqClient
}

func provideServiceBuildInfo(buildInfo handler.BuildInfo) service.BuildInfo {
	return service.BuildInfo{
		Version:   buildInfo.Version,
		BuildType: buildInfo.BuildType,
	}
}

func providePluginHostInfo(buildInfo handler.BuildInfo) service.PluginHostInfo {
	return service.PluginHostInfo{
		Version:   buildInfo.Version,
		BuildType: buildInfo.BuildType,
	}
}

func provideCleanup(
	entClient *ent.Client,
	rdb *redis.Client,
	opsMetricsCollector *service.OpsMetricsCollector,
	opsAggregation *service.OpsAggregationService,
	opsAlertEvaluator *service.OpsAlertEvaluatorService,
	opsCleanup *service.OpsCleanupService,
	opsScheduledReport *service.OpsScheduledReportService,
	opsSystemLogSink *service.OpsSystemLogSink,
	opsService *service.OpsService,
	opsIngressReject *service.OpsIngressRejectAggregator,
	apiKeyService *service.APIKeyService,
	authCacheInvalidationWorker *service.AuthCacheInvalidationWorker,
	schedulerSnapshot *service.SchedulerSnapshotService,
	tokenRefresh *service.TokenRefreshService,
	accountExpiry *service.AccountExpiryService,
	cnProviderBalanceCheck *service.CNProviderBalanceCheckService,
	codexVersionSync *service.OpenAICodexVersionSyncService,
	proxyExpiry *service.ProxyExpiryService,
	subscriptionExpiry *service.SubscriptionExpiryService,
	usageCleanup *service.UsageCleanupService,
	idempotencyCleanup *service.IdempotencyCleanupService,
	batchImageCleanup *service.BatchImageCleanupService,
	batchImageWorker *service.BatchImageWorkerRuntime,
	pricing *service.PricingService,
	emailQueue *service.EmailQueueService,
	billingCache *service.BillingCacheService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	subscriptionService *service.SubscriptionService,
	oauth *service.OAuthService,
	openaiOAuth *service.OpenAIOAuthService,
	geminiOAuth *service.GeminiOAuthService,
	antigravityOAuth *service.AntigravityOAuthService,
	grokOAuth *service.GrokOAuthService,
	openAIGateway *service.OpenAIGatewayService,
	scheduledTestRunner *service.ScheduledTestRunnerService,
	backupSvc *service.BackupService,
	paymentOrderExpiry *service.PaymentOrderExpiryService,
	channelMonitorRunner *service.ChannelMonitorRunner,
	channelMonitorV2Aggregator *service.ChannelMonitorV2Aggregator,
	quotaFlusher *service.UserPlatformQuotaUsageFlusher,
	upstreamBillingProbe *service.UpstreamBillingProbeService,
	ollamaCloudUsage *service.OllamaCloudUsageService,
	auditLog *service.AuditLogService,
	openAIAutoReset *service.OpenAIQuotaAutoResetService,
	promptAudit *securityaudit.PromptService,
	pluginManager *service.PluginManager,
	// 网关（gateway-lite）独有后台任务：随进程生命周期启停。
	keywordCatalog *service.KeywordCatalogService,
	syncMediaCache *service.SyncMediaCache,
	grokVideoTaskRunner *service.GrokVideoTaskRunner,
	operationAudit *service.OperationAuditService,
	upstreamBillingProbeRunner *service.UpstreamBillingProbeRunner,
	openAIUpstreamTimezoneSync *service.OpenAIUpstreamTimezoneSyncService,
	codexTailBurstCollector *service.CodexTailBurstCollector,
	accountTestSvc *service.AccountTestService,
	imageTaskHandler *handler.ImageTaskHandler,
	contentModeration *service.ContentModerationService,
) func() {
	if contentModeration != nil {
		contentModeration.StartAIOutboxWorker(context.Background())
	}
	if syncMediaCache != nil {
		// Synchronous image results do not create durable task rows, so their
		// object namespace needs an explicit lifecycle hook of its own.
		syncMediaCache.Start()
	}
	if openAIUpstreamTimezoneSync != nil {
		// 出口时区自动探测后台循环：只访问 ip-api 并写进程内缓存，
		// 不参与请求热路径；随进程生命周期启停。
		openAIUpstreamTimezoneSync.Start()
	}
	if accountTestSvc != nil {
		// OpenAI 账号 WS 支持探测后台循环：只做握手复检并写账号 extra，
		// 不改变任何转发行为；随进程生命周期启停。
		accountTestSvc.StartOpenAIWSSupportProbeLoop()
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		type cleanupStep struct {
			name string
			fn   func() error
		}

		// 应用层清理步骤可并行执行，基础设施资源（Redis/Ent）最后按顺序关闭。
		parallelSteps := []cleanupStep{
			{"ContentModerationAIOutbox", func() error {
				if contentModeration != nil {
					contentModeration.StopAIOutboxWorker()
				}
				return nil
			}},
			{"PluginManager", func() error {
				if pluginManager != nil {
					pluginManager.Stop()
				}
				return nil
			}},
			{"OpenAIQuotaAutoResetService", func() error {
				if openAIAutoReset != nil {
					openAIAutoReset.Stop()
				}
				return nil
			}},
			{"OpsIngressRejectAggregator", func() error {
				if opsIngressReject != nil {
					opsIngressReject.Stop()
				}
				return nil
			}},
			{"AuthCacheInvalidationWorker", func() error {
				if authCacheInvalidationWorker != nil {
					authCacheInvalidationWorker.Stop()
				}
				return nil
			}},
			{"AuthCacheInvalidationSubscriber", func() error {
				if apiKeyService != nil {
					apiKeyService.StopAuthCacheInvalidationSubscriber()
				}
				return nil
			}},
			{"OpsRuntimeSettingsRefresh", func() error {
				if opsService != nil {
					opsService.StopRuntimeSettingsRefresh()
				}
				return nil
			}},
			{"PromptAuditService", func() error {
				if promptAudit != nil {
					return promptAudit.Shutdown(ctx)
				}
				return nil
			}},
			// —— 网关独有清理项 ——
			{"KeywordCatalogService", func() error {
				if keywordCatalog != nil {
					keywordCatalog.Stop()
				}
				return nil
			}},
			{"ContentModerationPenaltyOutbox", func() error {
				if reporter := gatewaylite.DefaultContentModerationPenaltyReporter(); reporter != nil {
					reporter.Stop()
				}
				return nil
			}},
			{"OpenAIUpstreamTimezoneSyncService", func() error {
				if openAIUpstreamTimezoneSync != nil {
					openAIUpstreamTimezoneSync.Stop()
				}
				return nil
			}},
			{"UpstreamBillingProbeRunner", func() error {
				if upstreamBillingProbeRunner != nil {
					upstreamBillingProbeRunner.Stop()
				}
				return nil
			}},
			{"OpenAIWSSupportProbeLoop", func() error {
				if accountTestSvc != nil {
					accountTestSvc.StopOpenAIWSSupportProbeLoop()
				}
				return nil
			}},
			{"OperationAuditService", func() error {
				if operationAudit != nil {
					operationAudit.Stop()
				}
				return nil
			}},
			{"SyncMediaCache", func() error {
				if syncMediaCache != nil {
					syncMediaCache.Stop()
				}
				return nil
			}},
			{"ImageTaskService", func() error {
				if imageTaskHandler != nil {
					imageTaskHandler.Stop()
				}
				return nil
			}},
			{"GrokVideoTaskRunner", func() error {
				if grokVideoTaskRunner != nil {
					grokVideoTaskRunner.Stop()
				}
				return nil
			}},
			{"CodexTailBurstCollector", func() error {
				if codexTailBurstCollector != nil {
					codexTailBurstCollector.Stop()
				}
				return nil
			}},
			// —— 官方清理项 ——
			{"OpsScheduledReportService", func() error {
				if opsScheduledReport != nil {
					opsScheduledReport.Stop()
				}
				return nil
			}},
			{"OpsCleanupService", func() error {
				if opsCleanup != nil {
					opsCleanup.Stop()
				}
				return nil
			}},
			{"OpsSystemLogSink", func() error {
				if opsSystemLogSink != nil {
					opsSystemLogSink.Stop()
				}
				return nil
			}},
			{"AuditLogService", func() error {
				if auditLog != nil {
					auditLog.Stop()
				}
				return nil
			}},
			{"OpsAlertEvaluatorService", func() error {
				if opsAlertEvaluator != nil {
					opsAlertEvaluator.Stop()
				}
				return nil
			}},
			{"OpsAggregationService", func() error {
				if opsAggregation != nil {
					opsAggregation.Stop()
				}
				return nil
			}},
			{"OpsMetricsCollector", func() error {
				if opsMetricsCollector != nil {
					opsMetricsCollector.Stop()
				}
				return nil
			}},
			{"SchedulerSnapshotService", func() error {
				if schedulerSnapshot != nil {
					schedulerSnapshot.Stop()
				}
				return nil
			}},
			{"UsageCleanupService", func() error {
				if usageCleanup != nil {
					usageCleanup.Stop()
				}
				return nil
			}},
			{"IdempotencyCleanupService", func() error {
				if idempotencyCleanup != nil {
					idempotencyCleanup.Stop()
				}
				return nil
			}},
			{"BatchImageCleanupService", func() error {
				if batchImageCleanup != nil {
					batchImageCleanup.Stop()
				}
				return nil
			}},
			{"BatchImageWorkerRuntime", func() error {
				if batchImageWorker != nil {
					batchImageWorker.Stop()
				}
				return nil
			}},
			{"TokenRefreshService", func() error {
				tokenRefresh.Stop()
				return nil
			}},
			{"AccountExpiryService", func() error {
				accountExpiry.Stop()
				return nil
			}},
			{"CNProviderBalanceCheckService", func() error {
				if cnProviderBalanceCheck != nil {
					cnProviderBalanceCheck.Stop()
				}
				return nil
			}},
			{"OpenAICodexVersionSyncService", func() error {
				codexVersionSync.Stop()
				return nil
			}},
			{"ProxyExpiryService", func() error {
				proxyExpiry.Stop()
				return nil
			}},
			{"SubscriptionExpiryService", func() error {
				subscriptionExpiry.Stop()
				return nil
			}},
			{"SubscriptionService", func() error {
				if subscriptionService != nil {
					subscriptionService.Stop()
				}
				return nil
			}},
			{"PricingService", func() error {
				pricing.Stop()
				return nil
			}},
			{"EmailQueueService", func() error {
				emailQueue.Stop()
				return nil
			}},
			{"BillingCacheService", func() error {
				billingCache.Stop()
				return nil
			}},
			{"UsageRecordWorkerPool", func() error {
				if usageRecordWorkerPool != nil {
					usageRecordWorkerPool.Stop()
				}
				return nil
			}},
			{"OAuthService", func() error {
				oauth.Stop()
				return nil
			}},
			{"OpenAIOAuthService", func() error {
				openaiOAuth.Stop()
				return nil
			}},
			{"GeminiOAuthService", func() error {
				geminiOAuth.Stop()
				return nil
			}},
			{"AntigravityOAuthService", func() error {
				antigravityOAuth.Stop()
				return nil
			}},
			{"GrokOAuthService", func() error {
				if grokOAuth != nil {
					grokOAuth.Stop()
				}
				return nil
			}},
			{"OpenAIWSPool", func() error {
				if openAIGateway != nil {
					openAIGateway.CloseOpenAIWSPool()
				}
				return nil
			}},
			{"ScheduledTestRunnerService", func() error {
				if scheduledTestRunner != nil {
					scheduledTestRunner.Stop()
				}
				return nil
			}},
			{"BackupService", func() error {
				if backupSvc != nil {
					backupSvc.Stop()
				}
				return nil
			}},
			{"PaymentOrderExpiryService", func() error {
				if paymentOrderExpiry != nil {
					paymentOrderExpiry.Stop()
				}
				return nil
			}},
			{"ChannelMonitorV2Aggregator", func() error {
				if channelMonitorV2Aggregator != nil {
					channelMonitorV2Aggregator.Stop()
				}
				return nil
			}},
			{"ChannelMonitorRunner", func() error {
				if channelMonitorRunner != nil {
					channelMonitorRunner.Stop()
				}
				return nil
			}},
			{"UserPlatformQuotaUsageFlusher", func() error {
				if quotaFlusher != nil {
					quotaFlusher.Stop()
				}
				return nil
			}},
			{"UpstreamBillingProbeService", func() error {
				if upstreamBillingProbe != nil {
					upstreamBillingProbe.Stop()
				}
				return nil
			}},
			{"OllamaCloudUsageService", func() error {
				if ollamaCloudUsage != nil {
					ollamaCloudUsage.Stop()
				}
				return nil
			}},
			{"OpsErrLogQueueDrain", func() error {
				// 运维错误日志走的是内存异步队列（ops_error_logger.go）：进程退出前必须显式排空，
				// 否则每次重启/发版都会丢掉仍在队列里的条目。该函数幂等，队列从未创建时直接返回 true；
				// 排空超时只记录告警，不阻塞后续的基础设施关停。
				//
				// 命名注意：步骤名会被拼进 "[Cleanup] <name> succeeded"，而标准库日志桥
				// （internal/pkg/logger.inferStdLogLevel）只要消息里含 "error" 就判为 ERROR 级。
				// 若这里写成 "OpsErrorLogWorkers"，每次优雅停机都会打一条假 ERROR，污染错误率与告警。
				// 故刻意缩写为 Err，并有 cmd/server 的用例扫描步骤名兜底。
				if !handler.StopOpsErrorLogWorkers() {
					log.Printf("[Cleanup] ops error log queue did not drain in time; some entries may be lost")
				}
				return nil
			}},
		}

		infraSteps := []cleanupStep{
			{"Redis", func() error {
				if rdb == nil {
					return nil
				}
				return rdb.Close()
			}},
			{"Ent", func() error {
				if entClient == nil {
					return nil
				}
				return entClient.Close()
			}},
		}

		runParallel := func(steps []cleanupStep) {
			var wg sync.WaitGroup
			for i := range steps {
				step := steps[i]
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := step.fn(); err != nil {
						log.Printf("[Cleanup] %s failed: %v", step.name, err)
						return
					}
					log.Printf("[Cleanup] %s succeeded", step.name)
				}()
			}
			wg.Wait()
		}

		runSequential := func(steps []cleanupStep) {
			for i := range steps {
				step := steps[i]
				if err := step.fn(); err != nil {
					log.Printf("[Cleanup] %s failed: %v", step.name, err)
					continue
				}
				log.Printf("[Cleanup] %s succeeded", step.name)
			}
		}

		runParallel(parallelSteps)
		if outbox := gatewaylite.DefaultOAuthInvocationRecoveryOutbox(); outbox != nil {
			// 恢复消费者可能仍在写原资金事实，退出确认失败时不能关闭它正在使用的连接。
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := outbox.Stop(stopCtx)
			stopCancel()
			if err != nil {
				log.Printf("[Cleanup] OAuthInvocationRecoveryOutbox failed; infrastructure remains open: %v", err)
				return
			}
			gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(nil)
		}
		// Queue-producing services flush first; then cancel/join every worker that
		// owns the frozen binding before closing Redis and the database.
		joinCtx, joinCancel := context.WithTimeout(context.Background(), 15*time.Second)
		joinErr := gatewaylite.StopBindingWorkers(joinCtx)
		joinCancel()
		if joinErr != nil { markBindingGenerationUnhealthy(); log.Printf("[Cleanup] BindingWorkers did not stop; infrastructure remains open"); return }
		runSequential(infraSteps)

		// Check if context timed out
		select {
		case <-ctx.Done():
			log.Printf("[Cleanup] Warning: cleanup timed out after 10 seconds")
		default:
			log.Printf("[Cleanup] All cleanup steps completed")
		}
	}
}
