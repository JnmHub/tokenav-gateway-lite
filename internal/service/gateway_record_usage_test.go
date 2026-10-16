//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func newGatewayRecordUsageServiceForTest(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *GatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1
	return NewGatewayService(
		nil,
		nil,
		usageRepo,
		nil,
		userRepo,
		subRepo,
		nil,
		nil,
		cfg,
		nil,
		nil,
		NewBillingService(cfg, nil),
		nil,
		&BillingCacheService{},
		nil,
		nil,
		&DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // balanceNotifyService
		nil, // userPlatformQuotaRepo
	)
}

func newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo UsageLogRepository, billingRepo UsageBillingRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *GatewayService {
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	svc.usageBillingRepo = billingRepo
	return svc
}

func TestGatewayServiceRecordUsage_FrozenPeakPolicyDrivesTokenChargeAndUsageEvent(t *testing.T) {
	groupID := int64(1701)
	group := &Group{
		ID:                 groupID,
		RateMultiplier:     1.2,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "06:00",
		PeakRateMultiplier: 2,
		PeakTimezone:       "UTC",
	}
	ctx, frozen := freezePeakRatePolicyForRecordUsageTest(
		t, group, time.Date(2026, 8, 1, 23, 0, 0, 0, time.UTC), "frozen-anthropic-token",
	)
	require.InDelta(t, 2.4, frozen.EffectiveRateMultiplier, 1e-12)

	// 结束时已离开高峰窗口，且管理员已改价；已开始请求仍必须按 2.4。
	group.RateMultiplier = 0.4
	group.PeakRateEnabled = false
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(
		usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{},
	)
	usage := ClaudeUsage{InputTokens: 1000, OutputTokens: 200, CacheReadInputTokens: 100}
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "resp_frozen_anthropic_token",
			Usage:     usage,
			Model:     "claude-sonnet-4",
			Stream:    true,
			Duration:  8 * time.Hour,
		},
		APIKey: &APIKey{ID: 1101, GroupID: i64p(groupID), Group: group},
		User:   &User{ID: 2101},
		Account: &Account{
			ID: 3101,
		},
	})

	require.NoError(t, err)
	expected, calcErr := svc.billingService.CalculateCost("claude-sonnet-4", UsageTokens{
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CacheReadInputTokens,
	}, frozen.EffectiveRateMultiplier)
	require.NoError(t, calcErr)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, frozen.EffectiveRateMultiplier, usageRepo.lastLog.RateMultiplier, 1e-12)
	require.InDelta(t, expected.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.NotNil(t, billingRepo.lastCmd)
	require.InDelta(t, expected.ActualCost, billingRepo.lastCmd.BalanceCost, 1e-12)
	require.NotNil(t, usageRepo.lastLog.RatePolicyAudit)
	require.Equal(t, frozen.Version, usageRepo.lastLog.RatePolicyAudit.Version)
	event, ok := gatewayLiteUsageCorrectionEvent(ctx, usageRepo.lastLog)
	require.True(t, ok)
	require.NotNil(t, event.RatePolicyAudit)
	require.Equal(t, frozen.Version, event.RatePolicyAudit.Version)
}

type openAIRecordUsageBestEffortLogRepoStub struct {
	UsageLogRepository

	bestEffortErr   error
	createErr       error
	bestEffortCalls int
	createCalls     int
	lastLog         *UsageLog
	lastCtxErr      error
}

func (s *openAIRecordUsageBestEffortLogRepoStub) CreateBestEffort(ctx context.Context, log *UsageLog) error {
	s.bestEffortCalls++
	s.lastLog = log
	s.lastCtxErr = ctx.Err()
	return s.bestEffortErr
}

func (s *openAIRecordUsageBestEffortLogRepoStub) Create(ctx context.Context, log *UsageLog) (bool, error) {
	s.createCalls++
	s.lastLog = log
	s.lastCtxErr = ctx.Err()
	return false, s.createErr
}

func TestGatewayServiceRecordUsage_BillingUsesDetachedContext(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false, err: context.DeadlineExceeded}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordUsage(reqCtx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_detached_ctx",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    501,
			Quota: 100,
		},
		User:          &User{ID: 601},
		Account:       &Account{ID: 701},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.NoError(t, userRepo.lastCtxErr)
	require.Equal(t, 1, quotaSvc.quotaCalls)
	require.NoError(t, quotaSvc.lastQuotaCtxErr)
}

func TestGatewayServiceRecordUsage_GatewayLiteLocalKeySkipsBillingAndCorrection(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo)
	svc.cfg.RunMode = config.RunModeGatewayLite

	err := svc.RecordUsage(ContextWithGatewayLiteLocalAPIKey(context.Background()), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_lite_local_key_claude",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:   510,
			Key:  GatewayLiteLocalAPIKeyPrefix + "claude-local",
			User: &User{ID: 610},
		},
		User:          &User{ID: 610},
		Account:       &Account{ID: 710},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 0, billingRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
	require.Equal(t, 0, quotaSvc.quotaCalls)
	require.Equal(t, 0, quotaSvc.rateLimitCalls)
}

func TestApplyUsageBilling_LegacyFallbackSchedulesLastUsed(t *testing.T) {
	deferred := &DeferredService{}
	accountID := int64(70101)

	applied, err := applyUsageBilling(context.Background(), "legacy-last-used", &UsageLog{}, &postUsageBillingParams{
		Cost:    &CostBreakdown{},
		APIKey:  &APIKey{ID: 50101},
		User:    &User{ID: 60101},
		Account: &Account{ID: accountID},
	}, &billingDeps{
		deferredService: deferred,
	}, nil)

	require.NoError(t, err)
	require.True(t, applied)
	_, ok := deferred.lastUsedUpdates.Load(accountID)
	require.True(t, ok, "legacy fallback billing must still refresh account last_used_at")
}

func TestGatewayServiceRecordUsage_BillingFingerprintIncludesRequestPayloadHash(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	payloadHash := HashUsageRequestPayload([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_payload_hash",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:             &APIKey{ID: 501, Quota: 100},
		User:               &User{ID: 601},
		Account:            &Account{ID: 701},
		RequestPayloadHash: payloadHash,
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, payloadHash, billingRepo.lastCmd.RequestPayloadHash)
}

func TestGatewayServiceRecordUsage_BillingFingerprintFallsBackToContextRequestID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-local-123")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_payload_fallback",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})
	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, "local:req-local-123", billingRepo.lastCmd.RequestPayloadHash)
}

func TestGatewayServiceRecordUsage_PreservesRequestedAndUpstreamModels(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	mappedModel := "claude-sonnet-4-20250514"

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "gateway_models_split",
			Usage:         ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:         "claude-sonnet-4",
			UpstreamModel: mappedModel,
			Duration:      time.Second,
		},
		APIKey:  &APIKey{ID: 501, Quota: 100},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "claude-sonnet-4", usageRepo.lastLog.Model)
	require.Equal(t, "claude-sonnet-4", usageRepo.lastLog.RequestedModel)
	require.NotNil(t, usageRepo.lastLog.UpstreamModel)
	require.Equal(t, mappedModel, *usageRepo.lastLog.UpstreamModel)
}

func TestGatewayServiceRecordUsage_EmptyImageSizeDefaultsBeforeBillingAndPersistence(t *testing.T) {
	imagePrice2K := 0.19
	groupID := int64(901)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:      "gateway_image_default_size",
			Model:          "gemini-image",
			ImageCount:     1,
			ImageInputSize: "auto",
			Duration:       time.Second,
		},
		APIKey: &APIKey{
			ID:      801,
			GroupID: i64p(groupID),
			Group: &Group{
				ID:             groupID,
				RateMultiplier: 1.0,
				ImagePrice2K:   &imagePrice2K,
			},
		},
		User:    &User{ID: 601},
		Account: &Account{ID: 701},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 1, usageRepo.lastLog.ImageCount)
	require.NotNil(t, usageRepo.lastLog.ImageSize)
	require.Equal(t, ImageBillingSize2K, *usageRepo.lastLog.ImageSize)
	require.NotNil(t, usageRepo.lastLog.ImageInputSize)
	require.Equal(t, "auto", *usageRepo.lastLog.ImageInputSize)
	require.NotNil(t, usageRepo.lastLog.ImageSizeSource)
	require.Equal(t, ImageSizeSourceDefault, *usageRepo.lastLog.ImageSizeSource)
	require.InDelta(t, 0.19, usageRepo.lastLog.TotalCost, 1e-12)
	require.InDelta(t, 0.19, usageRepo.lastLog.ActualCost, 1e-12)
}

func TestGatewayServiceRecordUsage_UsageLogWriteErrorDoesNotSkipBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false, err: MarkUsageLogCreateNotPersisted(context.Canceled)}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_not_persisted",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    503,
			Quota: 100,
		},
		User:          &User{ID: 603},
		Account:       &Account{ID: 703},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, quotaSvc.quotaCalls)
}

func TestGatewayServiceRecordUsageWithLongContext_BillingUsesDetachedContext(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false, err: context.DeadlineExceeded}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	reqCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.RecordUsageWithLongContext(reqCtx, &RecordUsageLongContextInput{
		Result: &ForwardResult{
			RequestID: "gateway_long_context_detached_ctx",
			Usage: ClaudeUsage{
				InputTokens:  12,
				OutputTokens: 8,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    502,
			Quota: 100,
		},
		User:                  &User{ID: 602},
		Account:               &Account{ID: 702},
		LongContextThreshold:  200000,
		LongContextMultiplier: 2,
		APIKeyService:         quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.NoError(t, userRepo.lastCtxErr)
	require.Equal(t, 1, quotaSvc.quotaCalls)
	require.NoError(t, quotaSvc.lastQuotaCtxErr)
}

func TestGatewayServiceRecordUsage_UsesFallbackRequestIDForUsageLog(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "gateway-local-fallback")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 504},
		User:    &User{ID: 604},
		Account: &Account{ID: 704},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "local:gateway-local-fallback", usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_PrefersClientRequestIDOverUpstreamRequestID(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-stable-123")
	ctx = context.WithValue(ctx, ctxkey.RequestID, "req-local-ignored")
	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "upstream-volatile-456",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 506},
		User:    &User{ID: 606},
		Account: &Account{ID: 706},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.Equal(t, "client:client-stable-123", billingRepo.lastCmd.RequestID)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "client:client-stable-123", usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_GeneratesRequestIDWhenAllSourcesMissing(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 507},
		User:    &User{ID: 607},
		Account: &Account{ID: 707},
	})

	require.NoError(t, err)
	require.NotNil(t, billingRepo.lastCmd)
	require.True(t, strings.HasPrefix(billingRepo.lastCmd.RequestID, "generated:"))
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, billingRepo.lastCmd.RequestID, usageRepo.lastLog.RequestID)
}

func TestGatewayServiceRecordUsage_DroppedUsageLogDoesNotSyncFallback(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{
		bestEffortErr: MarkUsageLogCreateDropped(errors.New("usage log best-effort queue full")),
	}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_drop_usage_log",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 508},
		User:    &User{ID: 608},
		Account: &Account{ID: 708},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.bestEffortCalls)
	require.Equal(t, 0, usageRepo.createCalls)
}

func TestGatewayServiceRecordUsage_BillingErrorWritesUnsettledUsageLog(t *testing.T) {
	sink, restoreLog := captureStructuredLog(t)
	defer restoreLog()

	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingErr := errors.New("billing tx failed")
	billingRepo := &openAIRecordUsageBillingRepoStub{err: billingErr}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_billing_fail",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 505},
		User:    &User{ID: 605},
		Account: &Account{ID: 705},
	})

	require.ErrorIs(t, err, billingErr)
	require.Equal(t, 1, billingRepo.calls)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 10, usageRepo.lastLog.InputTokens)
	require.Equal(t, 6, usageRepo.lastLog.OutputTokens)
	require.Greater(t, usageRepo.lastLog.InputCost, 0.0)
	require.Greater(t, usageRepo.lastLog.OutputCost, 0.0)
	require.Greater(t, usageRepo.lastLog.TotalCost, 0.0)
	require.Zero(t, usageRepo.lastLog.ActualCost)
	require.True(t, sink.ContainsMessageAtLevel("usage.billing_persistence_failed_recorded_unsettled", "error"))
	require.True(t, sink.ContainsFieldValue("request_id", "gateway_billing_fail"))
}

func TestGatewayServiceRecordUsage_CorrectionFailureWritesUnsettledUsageLog(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })
	reporterCalls := 0
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(context.Context, gatewaylite.UsageEvent) error {
		reporterCalls++
		return errors.New("control-plane usage outbox unavailable")
	}))

	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingErr := errors.New("local billing identity unavailable")
	billingRepo := &openAIRecordUsageBillingRepoStub{err: billingErr}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(
		usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{},
	)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "gateway_correction_fail", KeyID: "remote-key", UserID: 606,
	})

	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_correction_fail",
			Usage:     ClaudeUsage{InputTokens: 12, OutputTokens: 7},
			Model:     "claude-sonnet-4",
			Duration:  time.Second,
		},
		APIKey: &APIKey{ID: 506}, User: &User{ID: 606}, Account: &Account{ID: 706},
	})

	require.ErrorIs(t, err, billingErr)
	require.Equal(t, 1, reporterCalls)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 12, usageRepo.lastLog.InputTokens)
	require.Equal(t, 7, usageRepo.lastLog.OutputTokens)
	require.Greater(t, usageRepo.lastLog.TotalCost, 0.0)
	require.Zero(t, usageRepo.lastLog.ActualCost)
}

func TestGatewayServiceRecordUsage_AsyncCorrectionFailureAfterLocalBillingWritesUnsettledUsageLog(t *testing.T) {
	previous := gatewaylite.DefaultUsageReporter()
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })
	gatewaylite.SetDefaultUsageReporter(gatewayLiteUsageReporterFunc(func(context.Context, gatewaylite.UsageEvent) error {
		return errors.New("control-plane usage outbox unavailable")
	}))

	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(
		usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{},
	)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "gateway_async_correction_fail", KeyID: "remote-key", UserID: 607, AsyncSettlement: true,
	})

	err := svc.RecordUsage(ctx, &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "gateway_async_correction_fail",
			Usage:     ClaudeUsage{InputTokens: 13, OutputTokens: 8},
			Model:     "claude-sonnet-4",
			Duration:  time.Second,
		},
		APIKey: &APIKey{ID: 507}, User: &User{ID: 607}, Account: &Account{ID: 707},
	})

	require.ErrorContains(t, err, "async usage correction was not durably enqueued")
	require.Equal(t, 1, billingRepo.calls)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 13, usageRepo.lastLog.InputTokens)
	require.Equal(t, 8, usageRepo.lastLog.OutputTokens)
	require.Greater(t, usageRepo.lastLog.TotalCost, 0.0)
	require.Zero(t, usageRepo.lastLog.ActualCost)
}

func TestGatewayServiceRecordUsage_ReasoningEffortPersisted(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	effort := "max"
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "effort_test",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:           "claude-opus-4-6",
			Duration:        time.Second,
			ReasoningEffort: &effort,
		},
		APIKey:  &APIKey{ID: 1},
		User:    &User{ID: 1},
		Account: &Account{ID: 1},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.NotNil(t, usageRepo.lastLog.ReasoningEffort)
	require.Equal(t, "max", *usageRepo.lastLog.ReasoningEffort)
}

func TestGatewayServiceRecordUsage_ReasoningEffortNil(t *testing.T) {
	usageRepo := &openAIRecordUsageBestEffortLogRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "no_effort_test",
			Usage: ClaudeUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:    "claude-sonnet-4",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1},
		User:    &User{ID: 1},
		Account: &Account{ID: 1},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Nil(t, usageRepo.lastLog.ReasoningEffort)
}
