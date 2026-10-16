package main

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProvideServiceBuildInfo(t *testing.T) {
	in := handler.BuildInfo{
		Version:   "v-test",
		BuildType: "release",
	}
	out := provideServiceBuildInfo(in)
	require.Equal(t, in.Version, out.Version)
	require.Equal(t, in.BuildType, out.BuildType)
}

func TestProvideCleanup_WithMinimalDependencies_NoPanic(t *testing.T) {
	cfg := &config.Config{}
	recovery := gatewaylite.NewOAuthInvocationRecoveryOutbox(nil, nil)
	gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(recovery)
	t.Cleanup(func() { gatewaylite.SetDefaultOAuthInvocationRecoveryOutbox(nil) })

	oauthSvc := service.NewOAuthService(nil, nil)
	openAIOAuthSvc := service.NewOpenAIOAuthService(nil, nil)
	geminiOAuthSvc := service.NewGeminiOAuthService(nil, nil, nil, nil, cfg)
	antigravityOAuthSvc := service.NewAntigravityOAuthService(nil)

	tokenRefreshSvc := service.NewTokenRefreshService(
		nil,
		oauthSvc,
		openAIOAuthSvc,
		geminiOAuthSvc,
		antigravityOAuthSvc,
		nil,
		nil,
		cfg,
		nil,
	)
	accountExpirySvc := service.NewAccountExpiryService(nil, time.Second)
	proxyExpirySvc := service.NewProxyExpiryService(nil, time.Second)
	subscriptionExpirySvc := service.NewSubscriptionExpiryService(nil, time.Second)
	pricingSvc := service.NewPricingService(cfg, nil)
	emailQueueSvc := service.NewEmailQueueService(nil, 1)
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	idempotencyCleanupSvc := service.NewIdempotencyCleanupService(nil, cfg)
	schedulerSnapshotSvc := service.NewSchedulerSnapshotService(nil, nil, nil, nil, cfg)
	opsSystemLogSinkSvc := service.NewOpsSystemLogSink(nil)

	// 参数必须与 provideCleanup 的当前签名逐一对齐（56 个）。
	// 之前这里漏了后来新增的若干依赖，导致 cmd/server 的测试整包编译不过
	// （go build 不编译 _test.go，所以一直没暴露）。
	cleanup := provideCleanup(
		nil, // entClient
		nil, // rdb
		&service.OpsMetricsCollector{},
		&service.OpsAggregationService{},
		&service.OpsAlertEvaluatorService{},
		&service.OpsCleanupService{},
		&service.OpsScheduledReportService{},
		opsSystemLogSinkSvc, // opsSystemLogSink
		nil,                 // opsService
		nil,                 // opsIngressReject
		nil,                 // apiKeyService
		nil,                 // authCacheInvalidationWorker
		schedulerSnapshotSvc,
		tokenRefreshSvc,
		accountExpirySvc,
		nil, // cnProviderBalanceCheck
		nil, // codexVersionSync
		proxyExpirySvc,
		subscriptionExpirySvc,
		&service.UsageCleanupService{},
		idempotencyCleanupSvc,
		nil, // batchImageCleanup
		nil, // batchImageWorker
		pricingSvc,
		emailQueueSvc,
		billingCacheSvc,
		&service.UsageRecordWorkerPool{},
		&service.SubscriptionService{},
		oauthSvc,
		openAIOAuthSvc,
		geminiOAuthSvc,
		antigravityOAuthSvc,
		nil, // grokOAuth
		nil, // openAIGateway
		nil, // scheduledTestRunner
		nil, // backupSvc
		nil, // paymentOrderExpiry
		nil, // channelMonitorRunner
		nil, // channelMonitorV2Aggregator
		nil, // quotaFlusher
		nil, // upstreamBillingProbe
		nil, // ollamaCloudUsage
		nil, // auditLog
		nil, // openAIAutoReset
		nil, // promptAudit
		nil, // pluginManager
		nil, // keywordCatalog
		nil, // syncMediaCache
		nil, // grokVideoTaskRunner
		nil, // operationAudit
		nil, // upstreamBillingProbeRunner
		nil, // openAIUpstreamTimezoneSync
		nil, // codexTailBurstCollector
		nil, // accountTestSvc (WS 探测循环随进程启停)
		nil, // imageTaskHandler
		nil, // contentModeration
	)

	require.NotPanics(t, func() {
		cleanup()
	})
	require.Nil(t, gatewaylite.DefaultOAuthInvocationRecoveryOutbox())
}
