package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/stretchr/testify/require"
)

type gatewayLiteUsageReporterFunc func(context.Context, gatewaylite.UsageEvent) error

func (f gatewayLiteUsageReporterFunc) ReportUsage(ctx context.Context, event gatewaylite.UsageEvent) error {
	return f(ctx, event)
}

type gatewayLiteUsageLogRepoStub struct {
	UsageLogRepository
	bestEffortCalls int
	queuedCalls     int
	createCalls     int
	resolveCalls    int
	resolveErr      error
	remoteUserID    int64
	remoteKeyID     string
	resolvedUserID  int64
	resolvedKeyID   int64
	lastLog         *UsageLog
}

func (s *gatewayLiteUsageLogRepoStub) CreateBestEffort(_ context.Context, log *UsageLog) error {
	s.bestEffortCalls++
	s.lastLog = log
	return nil
}

func (s *gatewayLiteUsageLogRepoStub) EnqueueBestEffort(_ context.Context, log *UsageLog) error {
	s.queuedCalls++
	s.lastLog = log
	return nil
}

func (s *gatewayLiteUsageLogRepoStub) Create(_ context.Context, log *UsageLog) (bool, error) {
	s.createCalls++
	s.lastLog = log
	return true, nil
}

func (s *gatewayLiteUsageLogRepoStub) ResolveGatewayLiteUsageIdentity(_ context.Context, remoteUserID int64, remoteKeyID string) (int64, int64, error) {
	s.resolveCalls++
	s.remoteUserID = remoteUserID
	s.remoteKeyID = remoteKeyID
	if s.resolveErr != nil {
		return 0, 0, s.resolveErr
	}
	return s.resolvedUserID, s.resolvedKeyID, nil
}

func TestWriteUsageLogBestEffortPersistsRemoteGatewayLiteKeyWithLocalShadowIdentity(t *testing.T) {
	repo := &gatewayLiteUsageLogRepoStub{resolvedUserID: 420, resolvedKeyID: 700}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	groupID := int64(99)
	subscriptionID := int64(77)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "req-1",
		KeyID:     "key123",
		UserID:    42,
		Region:    "sg",
	})

	writeUsageLogBestEffort(ctx, repo, &UsageLog{
		UserID:         42,
		APIKeyID:       0,
		GroupID:        &groupID,
		SubscriptionID: &subscriptionID,
	}, "service.gateway", cfg)

	require.Equal(t, 1, repo.resolveCalls)
	require.Equal(t, int64(42), repo.remoteUserID)
	require.Equal(t, "key123", repo.remoteKeyID)
	require.Equal(t, 1, repo.queuedCalls)
	require.Equal(t, 0, repo.bestEffortCalls)
	require.Equal(t, 0, repo.createCalls)
	require.NotNil(t, repo.lastLog)
	require.Equal(t, int64(420), repo.lastLog.UserID)
	require.Equal(t, int64(700), repo.lastLog.APIKeyID)
	require.NotNil(t, repo.lastLog.GroupID)
	require.Equal(t, groupID, *repo.lastLog.GroupID)
	require.Nil(t, repo.lastLog.SubscriptionID)
}

func TestWriteUsageLogBestEffortPersistsKeywordPolicyGroupEvidence(t *testing.T) {
	repo := &gatewayLiteUsageLogRepoStub{}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "req-keyword-route", KeyID: "local-key", UserID: 42,
		GroupID: 20, OriginalGroupID: 10, KeywordPolicyMatched: true,
		KeywordPolicyRerouted: true, KeywordPolicySource: "skill",
		KeywordPolicyReason: "rerouted", DetectedLanguages: []string{"zh", "en"},
	})

	writeUsageLogBestEffort(ctx, repo, &UsageLog{UserID: 42, APIKeyID: 7}, "service.gateway", cfg)

	require.NotNil(t, repo.lastLog)
	require.NotNil(t, repo.lastLog.GroupID)
	require.EqualValues(t, 20, *repo.lastLog.GroupID)
	require.NotNil(t, repo.lastLog.OriginalGroupID)
	require.EqualValues(t, 10, *repo.lastLog.OriginalGroupID)
	require.True(t, repo.lastLog.KeywordPolicyMatched)
	require.True(t, repo.lastLog.KeywordPolicyRerouted)
	require.Equal(t, "skill", repo.lastLog.KeywordPolicySource)
	require.Equal(t, "rerouted", repo.lastLog.KeywordPolicyReason)
	require.Equal(t, []string{"zh", "en"}, repo.lastLog.DetectedLanguages)
}

func TestWriteUsageLogBestEffortSkipsRemoteGatewayLiteKeyWhenShadowIdentityFails(t *testing.T) {
	repo := &gatewayLiteUsageLogRepoStub{resolveErr: errors.New("db unavailable")}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}

	writeUsageLogBestEffort(context.Background(), repo, &UsageLog{
		UserID:   42,
		APIKeyID: 0,
	}, "service.gateway", cfg)

	require.Equal(t, 1, repo.resolveCalls)
	require.Equal(t, 0, repo.bestEffortCalls)
	require.Equal(t, 0, repo.queuedCalls)
	require.Equal(t, 0, repo.createCalls)
}

func TestWriteUsageLogBestEffortKeepsLocalKeyInGatewayLite(t *testing.T) {
	repo := &gatewayLiteUsageLogRepoStub{}
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}

	writeUsageLogBestEffort(context.Background(), repo, &UsageLog{
		UserID:   42,
		APIKeyID: 7,
	}, "service.gateway", cfg)

	require.Equal(t, 1, repo.bestEffortCalls)
	require.Equal(t, 0, repo.queuedCalls)
	require.Equal(t, 0, repo.createCalls)
}

func TestGatewayLiteUsageCorrectionEventCarriesImageBillingSnapshot(t *testing.T) {
	groupID := int64(9)
	imageSize := "4K"
	billingMode := string(BillingModeImage)
	billingTier := "4K"
	serviceTier := "priority"
	sentServiceTier := "priority"
	responseServiceTier := "default"
	requestedServiceTier := "priority"
	priorityPolicyMode := OpenAIPriorityModeFollowRequest
	priorityDetectionSource := "response"
	priorityPolicyAction := BetaPolicyActionPass
	priorityPolicyRuleID := "rule_priority_default"
	imageInputPriceSource := "channel"
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:      "req-image-billing",
		KeyID:          "key-image",
		UserID:         42,
		Region:         "sg",
		GatewayID:      "openai-sg",
		GroupID:        groupID,
		GroupName:      "default",
		RateMultiplier: 1.25,
	})

	event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{
		RequestID:                 "resp-image-billing",
		AccountID:                 17,
		Model:                     "gpt-image-2",
		GroupID:                   &groupID,
		InputTokens:               11,
		OutputTokens:              22,
		BillingMode:               &billingMode,
		BillingTier:               &billingTier,
		ServiceTier:               &serviceTier,
		SentServiceTier:           &sentServiceTier,
		ResponseServiceTier:       &responseServiceTier,
		RequestedServiceTier:      &requestedServiceTier,
		PriorityPolicyMode:        &priorityPolicyMode,
		PriorityDetectionSource:   &priorityDetectionSource,
		PriorityPolicyAction:      &priorityPolicyAction,
		PriorityPolicyRuleID:      &priorityPolicyRuleID,
		PriorityPolicyFallback:    true,
		LongContextBillingApplied: true,
		ImageCount:                2,
		ImageSize:                 &imageSize,
		ImageInputTokens:          28,
		ImageInputCost:            0.007,
		ImageInputPrice:           0.00025,
		ImageInputPriceSource:     &imageInputPriceSource,
		ImageOutputTokens:         64,
		ImageOutputCost:           0.32,
		ActualCost:                0.42,
		RateMultiplier:            1.25,
	})
	require.True(t, ok)
	require.Equal(t, "resp-image-billing", event.RequestID)
	require.Equal(t, "17", event.UpstreamID)
	require.Equal(t, "image", event.BillingMode)
	require.Equal(t, "4K", event.BillingTier)
	require.Equal(t, int64(2), event.ImageCount)
	require.Equal(t, "4K", event.ImageSize)
	require.Equal(t, int64(28), event.ImageInputTokens)
	require.InDelta(t, 0.007, event.ImageInputCost, 1e-12)
	require.InDelta(t, 0.00025, event.ImageInputPrice, 1e-12)
	require.Equal(t, "channel", event.ImageInputPriceSource)
	require.Equal(t, int64(64), event.ImageOutputTokens)
	require.InDelta(t, 0.32, event.ImageOutputCost, 1e-12)
	require.Equal(t, int64(11), event.TokensIn)
	require.Equal(t, int64(22), event.TokensOut)
	require.Equal(t, groupID, event.GatewayGroupID)
	require.Equal(t, serviceTier, event.ServiceTier)
	require.Equal(t, sentServiceTier, event.SentServiceTier)
	require.Equal(t, responseServiceTier, event.ResponseServiceTier)
	require.Equal(t, requestedServiceTier, event.RequestedServiceTier)
	require.Equal(t, priorityPolicyMode, event.PriorityPolicyMode)
	require.Equal(t, priorityDetectionSource, event.PriorityDetectionSource)
	require.Equal(t, priorityPolicyAction, event.PriorityPolicyAction)
	require.Equal(t, priorityPolicyRuleID, event.PriorityPolicyRuleID)
	require.True(t, event.PriorityPolicyFallback)
	require.True(t, event.LongContextBillingApplied)
}

func TestGatewayLiteUsageCorrectionEventUsesEffectiveGroupAfterFailover(t *testing.T) {
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:         "req-group-failover",
		KeyID:             "key-group-failover",
		UserID:            42,
		GroupID:           20,
		GroupName:         "兜底分组",
		OriginalGroupID:   10,
		OriginalGroupName: "入口分组",
	})
	sourceGroupID := int64(10)
	event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{
		RequestID:  "resp-group-failover",
		UserID:     42,
		APIKeyID:   7,
		GroupID:    &sourceGroupID,
		Model:      "gpt-5.6-sol",
		ActualCost: 0.01,
	})
	require.True(t, ok)
	require.Equal(t, int64(20), event.GatewayGroupID)
	require.Equal(t, "兜底分组", event.GatewayGroupName)
	require.Equal(t, int64(10), event.RequestedGatewayGroupID)
	require.Equal(t, "入口分组", event.RequestedGatewayGroupName)
}

func TestGatewayLiteUsageCorrectionEventUsesPrecomputedAsyncCents(t *testing.T) {
	gatewaylite.SetDefaultRedisQuota(gatewaylite.NewRedisQuota(nil, ""))
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:             "req-video-async",
		KeyID:                 "key-video-override-does-not-advance",
		UserID:                42,
		Region:                "jp",
		GatewayID:             "grok-jp",
		AsyncSettlement:       true,
		BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
	})
	ctx = gatewaylite.ContextWithActualCentsOverride(ctx, 37)
	billingMode := string(BillingModeVideo)

	event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{
		RequestID:   "req-video-async",
		Model:       "grok-imagine-video",
		BillingMode: &billingMode,
		ActualCost:  0.015,
		VideoCount:  1,
	})
	require.True(t, ok)
	require.EqualValues(t, 37, event.ActualCents)
	require.Equal(t, "idempotent_v2", event.SettlementMode)
	// The integer result was prepared earlier, but the final Redis transaction
	// still has to reuse that frozen decision and mark the usage event enqueued.
	require.True(t, event.SettlementPending)
	require.EqualValues(t, 1_500_000, event.SettlementMicroCents)
	require.Equal(t, "req-video-async", event.QuotaRequestID)

	// The override is already the frozen result. It must not leave a 0.5-cent
	// remainder that makes the next real request emit a phantom cent.
	next := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:             "req-video-after-override",
		KeyID:                 "key-video-override-does-not-advance",
		UserID:                42,
		Region:                "jp",
		BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
	})
	require.Zero(t, gatewaylite.ActualCostToCentsForContext(next, 0.005))
}

func TestGatewayLiteUsageCorrectionEventOmitsMissingUpstreamAccount(t *testing.T) {
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "req-no-account",
		KeyID:     "key-no-account",
		UserID:    42,
		Region:    "jp",
		GatewayID: "openai-jp",
	})

	event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{RequestID: "req-no-account"})
	require.True(t, ok)
	require.Empty(t, event.UpstreamID)
}

func TestGatewayLiteUsageCorrectionPreservesIndependentMediaAndZeroAccountRates(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })

	zero := 0.0
	policy := gatewaylite.RatePolicyAudit{
		SchemaVersion: 1, BillingScope: "token", GroupRateMultiplier: 1,
		UserRateMultiplier: &zero, ResolvedRateMultiplier: 0,
		AppliedPeakMultiplier: 1, EffectiveRateMultiplier: 0, Timezone: "UTC",
		ResolvedAtMillis: time.Now().UnixMilli(), Version: "v1:0123456789abcdef",
	}
	require.True(t, policy.Valid())
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "req-free-token-image", KeyID: "key-free-token-image", UserID: 42,
		Region: "us", GatewayID: "us-1", GroupID: 7, GroupName: "free-token",
		RateMultiplier: 0, RatePolicyAudit: &policy,
	})
	billingMode := string(BillingModeImage)
	var captured gatewaylite.UsageEvent
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(_ context.Context, event gatewaylite.UsageEvent) error {
		captured = event
		return nil
	}))

	require.True(t, reportGatewayLiteUsageCorrection(ctx, &UsageLog{
		RequestID: "req-free-token-image", Model: "gpt-image-test", BillingMode: &billingMode,
		ImageCount: 1, RateMultiplier: 1.75, AccountRateMultiplier: &zero,
	}))
	require.InDelta(t, 1.75, captured.RateMultiplier, 1e-12,
		"independent image multiplier must not be overwritten by the 0x Token policy")
	require.NotNil(t, captured.RatePolicyAudit)
	require.Zero(t, captured.RatePolicyAudit.EffectiveRateMultiplier)
	require.NotNil(t, captured.AccountRateMultiplier)
	require.Zero(t, *captured.AccountRateMultiplier,
		"an explicit free procurement account must not be rewritten to 1x")
}

func TestGatewayLiteUsageCorrectionPreservesPerRequestAndExplicitZeroMediaRates(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })

	policy := gatewaylite.RatePolicyAudit{
		SchemaVersion: 1, BillingScope: "token", GroupRateMultiplier: 1.25,
		ResolvedRateMultiplier: 1.25, AppliedPeakMultiplier: 1,
		EffectiveRateMultiplier: 1.25, Timezone: "UTC",
		ResolvedAtMillis: time.Now().UnixMilli(), Version: "v1:8899aabbccddeeff",
	}
	require.True(t, policy.Valid())

	for _, test := range []struct {
		name        string
		billingMode BillingMode
		mediaRate   float64
		imageCount  int
		videoCount  int
		wantRate    float64
	}{
		{name: "per_request_image", billingMode: BillingModePerRequest, mediaRate: 1.75, imageCount: 1, wantRate: 1.75},
		{name: "free_independent_image", billingMode: BillingModeImage, mediaRate: 0, imageCount: 1, wantRate: 0},
		{name: "free_independent_video", billingMode: BillingModeVideo, mediaRate: 0, videoCount: 1, wantRate: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
				RequestID: "req-" + test.name, KeyID: "key-media-rate", UserID: 42,
				Region: "us", GatewayID: "us-1", GroupID: 7, GroupName: "media",
				RateMultiplier: policy.EffectiveRateMultiplier, RatePolicyAudit: &policy,
			})
			mode := string(test.billingMode)
			var captured gatewaylite.UsageEvent
			gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(_ context.Context, event gatewaylite.UsageEvent) error {
				captured = event
				return nil
			}))

			require.True(t, reportGatewayLiteUsageCorrection(ctx, &UsageLog{
				RequestID: "req-" + test.name, Model: "gpt-media-test", BillingMode: &mode,
				ImageCount: test.imageCount, VideoCount: test.videoCount, RateMultiplier: test.mediaRate,
			}))
			require.InDelta(t, test.wantRate, captured.RateMultiplier, 1e-12)
			require.Equal(t, test.imageCount, int(captured.ImageCount))
			require.Equal(t, test.videoCount, int(captured.VideoCount))
		})
	}
}

func TestReportGatewayLiteUsageCorrectionUsesIndependentEnqueueContext(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })

	called := false
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(ctx context.Context, event gatewaylite.UsageEvent) error {
		called = true
		require.NoError(t, ctx.Err())
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(gatewayLiteUsageEnqueueTimeout), deadline, time.Second)
		require.Equal(t, "req-detached-enqueue", event.RequestID)
		return nil
	}))

	parent, cancel := context.WithCancel(context.Background())
	parent = gatewaylite.ContextWithRequestMeta(parent, gatewaylite.RequestMeta{
		RequestID: "req-detached-enqueue",
		KeyID:     "key-detached-enqueue",
		UserID:    42,
	})
	cancel()

	require.True(t, reportGatewayLiteUsageCorrection(parent, &UsageLog{RequestID: "req-detached-enqueue"}))
	require.True(t, called)
}

func TestReportGatewayLiteUsageCorrectionReturnsFalseWhenPersistentEnqueueFails(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })

	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(context.Context, gatewaylite.UsageEvent) error {
		return errors.New("redis stream unavailable")
	}))
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "req-enqueue-failed",
		KeyID:     "key-enqueue-failed",
		UserID:    42,
	})

	require.False(t, reportGatewayLiteUsageCorrection(ctx, &UsageLog{RequestID: "req-enqueue-failed"}))
}

func TestReportGatewayLiteUsageCorrectionRetryReusesIdempotentCents(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })
	gatewaylite.SetDefaultRedisQuota(gatewaylite.NewRedisQuota(nil, ""))

	var calls int
	var got []int64
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(_ context.Context, event gatewaylite.UsageEvent) error {
		calls++
		got = append(got, event.ActualCents)
		if calls == 1 {
			return errors.New("first enqueue failed")
		}
		return nil
	}))
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:             "req-idempotent-report-retry",
		KeyID:                 "key-idempotent-report-retry",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
	})
	usage := &UsageLog{RequestID: "req-idempotent-report-retry", ActualCost: 0.015}
	require.False(t, reportGatewayLiteUsageCorrection(ctx, usage))
	require.True(t, reportGatewayLiteUsageCorrection(ctx, usage))
	require.Equal(t, []int64{1, 1}, got)

	next := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:             "req-idempotent-report-next",
		KeyID:                 "key-idempotent-report-retry",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
	})
	event, ok := gatewayLiteUsageCorrectionEvent(next, &UsageLog{RequestID: "req-idempotent-report-next", ActualCost: 0.005})
	require.True(t, ok)
	require.Zero(t, event.ActualCents)
	require.True(t, event.SettlementPending)
	require.EqualValues(t, 500_000, event.SettlementMicroCents)
}

func TestReportGatewayLiteUsageCorrectionUsesFinancialEventIDForWebSocketTurns(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })
	gatewaylite.SetDefaultRedisQuota(gatewaylite.NewRedisQuota(nil, ""))

	var got []gatewaylite.UsageEvent
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(_ context.Context, event gatewaylite.UsageEvent) error {
		got = append(got, event)
		return nil
	}))
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID:             "client:websocket-connection",
		KeyID:                 "key-websocket-turns",
		UserID:                42,
		Region:                "sg",
		BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
	})

	require.True(t, reportGatewayLiteUsageCorrection(ctx, &UsageLog{RequestID: "resp_turn_1", ActualCost: 0.006}))
	require.True(t, reportGatewayLiteUsageCorrection(ctx, &UsageLog{RequestID: "resp_turn_1", ActualCost: 0.006}))
	require.True(t, reportGatewayLiteUsageCorrection(ctx, &UsageLog{RequestID: "resp_turn_2", ActualCost: 0.004}))
	require.Len(t, got, 3)
	require.Equal(t, []string{"resp_turn_1", "resp_turn_1", "resp_turn_2"}, []string{got[0].RequestID, got[1].RequestID, got[2].RequestID})
	require.Equal(t, []int64{0, 0, 1}, []int64{got[0].ActualCents, got[1].ActualCents, got[2].ActualCents})
}
