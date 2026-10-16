package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSplitGatewayLiteAPIKeySupportsTokenavAndLegacyPrefixes(t *testing.T) {
	for _, prefix := range []string{"tokenav", "tokeav", "aiceo", "fm"} {
		keyID, secret, ok := splitGatewayLiteAPIKey(prefix + "_sk_key_part_secret")
		require.True(t, ok, prefix)
		require.Equal(t, "key_part", keyID)
		require.Equal(t, "secret", secret)
	}

	_, _, ok := splitGatewayLiteAPIKey("unknown_sk_key_part_secret")
	require.False(t, ok)
}

func TestGatewayLiteRequestMetaFreezesMainSiteRateAtRequestStartWithoutLocalUserOverride(t *testing.T) {
	startedAt := time.Date(2026, time.August, 1, 23, 30, 0, 0, time.UTC)
	group := &service.Group{
		ID:                 17,
		Name:               "main-site-group",
		RateMultiplier:     1.5,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "06:00",
		PeakRateMultiplier: 2,
		PeakTimezone:       "UTC",
		UpdatedAt:          startedAt.Add(-time.Hour),
	}

	meta := gatewayLiteRequestMeta(
		"request-1", "key-1", 42, "lease-1", "us", "us-1", group,
		nil, gatewaylite.BillingSettlementModeIdempotentV2, startedAt,
	)

	require.NotNil(t, meta.RatePolicyAudit)
	require.Nil(t, meta.RatePolicyAudit.UserRateMultiplier,
		"主站 KeySnapshot 不支持 user×group 倍率，不能读取网关本地用户倍率冒充支持")
	require.Equal(t, startedAt.UnixMilli(), meta.RatePolicyAudit.ResolvedAtMillis)
	require.InDelta(t, 1.5, meta.RatePolicyAudit.GroupRateMultiplier, 1e-12)
	require.InDelta(t, 2.0, meta.RatePolicyAudit.AppliedPeakMultiplier, 1e-12)
	require.InDelta(t, 3.0, meta.RateMultiplier, 1e-12)
}

func TestGatewayLiteRequestMetaFreezesMainSiteUserOverrideBeforePeak(t *testing.T) {
	startedAt := time.Date(2026, time.August, 1, 23, 30, 0, 0, time.UTC)
	group := &service.Group{
		ID: 17, Name: "main-site-group", RateMultiplier: 1.5,
		PeakRateEnabled: true, PeakStart: "22:00", PeakEnd: "06:00",
		PeakRateMultiplier: 2, PeakTimezone: "UTC", UpdatedAt: startedAt.Add(-time.Hour),
	}
	userRate := 0.8
	meta := gatewayLiteRequestMeta(
		"request-user-rate", "key-user-rate", 42, "lease-user-rate", "us", "us-1", group,
		&userRate, gatewaylite.BillingSettlementModeIdempotentV2, startedAt,
	)

	require.NotNil(t, meta.RatePolicyAudit)
	require.NotNil(t, meta.RatePolicyAudit.UserRateMultiplier)
	require.InDelta(t, 1.5, meta.RatePolicyAudit.GroupRateMultiplier, 1e-12)
	require.InDelta(t, 0.8, meta.RatePolicyAudit.ResolvedRateMultiplier, 1e-12)
	require.InDelta(t, 2.0, meta.RatePolicyAudit.AppliedPeakMultiplier, 1e-12)
	require.InDelta(t, 1.6, meta.RateMultiplier, 1e-12)
}

func TestGatewayLiteRequestMetaPreservesExplicitZeroUserOverride(t *testing.T) {
	startedAt := time.Date(2026, time.August, 1, 23, 30, 0, 0, time.UTC)
	group := &service.Group{ID: 17, Name: "free-user", RateMultiplier: 1.5}
	zero := 0.0
	meta := gatewayLiteRequestMeta(
		"request-free", "key-free", 42, "lease-free", "us", "us-1", group,
		&zero, gatewaylite.BillingSettlementModeIdempotentV2, startedAt,
	)
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
	frozen, ok := gatewaylite.RequestMetaFromContext(ctx)
	require.True(t, ok)
	require.Zero(t, frozen.RateMultiplier)
	require.NotNil(t, frozen.RatePolicyAudit)
	require.NotNil(t, frozen.RatePolicyAudit.UserRateMultiplier)
	require.Zero(t, *frozen.RatePolicyAudit.UserRateMultiplier)
}

func TestGatewayLiteRefreshLeaseCarriesFrozenSalesEstimate(t *testing.T) {
	var acquired gatewaylite.AcquireLeaseRequest
	stub := gatewayLiteControlStub{
		lease: gatewaylite.LeaseSnapshot{
			LeaseID: "lease-rate-estimate", UserID: 42, Region: "us",
			AllocatedCents: 20, Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		},
		acquireFn: func(req gatewaylite.AcquireLeaseRequest) { acquired = req },
	}

	lease, err := gatewayLiteRefreshLease(
		context.Background(), stub, nil, gatewaylite.KeySnapshot{UserID: 42}, "us", 4,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.EqualValues(t, 4, acquired.EstimatedCostCents)
}

func TestGatewayLiteFreeSalesRateRefundsOneCentSafetyPlaceholder(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "free-rate")
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-free-rate", UserID: 42, Region: "us", AllocatedCents: 10,
		Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}))
	_, _, reserved, err := quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
		RequestID: "request-free-rate", KeyID: "key-free-rate", UserID: 42, Region: "us",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	zero := 0.0
	policy := gatewaylite.RatePolicyAudit{
		SchemaVersion: 1, BillingScope: "token", GroupRateMultiplier: 1,
		UserRateMultiplier: &zero, ResolvedRateMultiplier: 0,
		AppliedPeakMultiplier: 1, EffectiveRateMultiplier: 0, Timezone: "UTC",
		ResolvedAtMillis: time.Now().UnixMilli(), Version: "v1:0123456789abcdef",
	}
	require.True(t, policy.Valid())
	reported := make(chan gatewaylite.UsageEvent, 1)
	reporter := gatewayLiteControlStub{reportFn: func(event gatewaylite.UsageEvent) { reported <- event }}
	gatewayLiteFinalizeUsage(reporter, quota, gatewayLiteFinalizeInput{
		RequestID: "request-free-rate", KeyID: "key-free-rate", UserID: 42,
		LeaseID: "lease-free-rate", Region: "us", GatewayID: "us-1",
		GroupID: 7, GroupName: "free", RateMultiplier: 0, RatePolicyAudit: &policy,
		Method: http.MethodPost, Path: "/v1/responses", Model: "gpt-test", Status: http.StatusOK,
		EstimatedCents: 1, StartedAt: time.Now().Add(-time.Millisecond), EndedAt: time.Now(),
	})

	lease, found, err := quota.LoadLease(ctx, 42, "us")
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, lease.ReservedCents)
	require.Zero(t, lease.SpentCents)
	select {
	case event := <-reported:
		require.EqualValues(t, 1, event.EstimatedCents)
		require.Zero(t, event.ActualCents)
		require.Zero(t, event.RateMultiplier)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for free-rate usage event")
	}
}

func TestGatewayLiteUncertainAsyncPersistenceKeepsReservationOnRetryableError(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "uncertain-image-task")
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-uncertain", UserID: 52, Region: "us", AllocatedCents: 100,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	_, _, reserved, err := quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
		RequestID: "request-uncertain", KeyID: "key-uncertain", UserID: 52, Region: "us",
		EstimatedCostCents: 25,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	gatewayLiteFinalizeUsage(gatewayLiteControlStub{}, quota, gatewayLiteFinalizeInput{
		RequestID: "request-uncertain", UserID: 52, LeaseID: "lease-uncertain", Region: "us",
		Status: http.StatusServiceUnavailable, EstimatedCents: 25,
		AsyncSettlement: true, AsyncPersistenceUncertain: true,
	})
	lease, found, err := quota.LoadLease(ctx, 52, "us")
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, 25, lease.ReservedCents)
	require.Zero(t, lease.SpentCents)
}

func TestGatewayLitePostUpstreamPolicyRejectionStillCommitsUsage(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "post-upstream-policy")
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-post-upstream", UserID: 42, Region: "us", AllocatedCents: 20,
		Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}))
	_, _, reserved, err := quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
		RequestID: "request-post-upstream", KeyID: "key-post-upstream", UserID: 42, Region: "us",
		EstimatedCostCents: 3,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	reported := make(chan gatewaylite.UsageEvent, 1)
	reporter := gatewayLiteControlStub{reportFn: func(event gatewaylite.UsageEvent) { reported <- event }}
	gatewayLiteFinalizeUsage(reporter, quota, gatewayLiteFinalizeInput{
		RequestID: "request-post-upstream", KeyID: "key-post-upstream", UserID: 42,
		LeaseID: "lease-post-upstream", Region: "us", Status: http.StatusForbidden,
		EstimatedCents: 3, UpstreamConsumed: true,
	})

	lease, found, err := quota.LoadLease(ctx, 42, "us")
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, lease.ReservedCents)
	require.EqualValues(t, 3, lease.SpentCents)
	select {
	case event := <-reported:
		require.EqualValues(t, 3, event.ActualCents)
		require.Equal(t, http.StatusForbidden, event.Status)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for post-upstream usage event")
	}
}

func TestGatewayLitePreDispatchPolicyRejectionStillRefunds(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "pre-dispatch-policy")
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease-pre-dispatch", UserID: 42, Region: "us", AllocatedCents: 20,
		Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}))
	_, _, reserved, err := quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
		RequestID: "request-pre-dispatch", KeyID: "key-pre-dispatch", UserID: 42, Region: "us",
		EstimatedCostCents: 3,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	gatewayLiteFinalizeUsage(gatewayLiteControlStub{}, quota, gatewayLiteFinalizeInput{
		RequestID: "request-pre-dispatch", UserID: 42, LeaseID: "lease-pre-dispatch", Region: "us",
		Status: http.StatusForbidden, EstimatedCents: 3,
	})
	lease, found, err := quota.LoadLease(ctx, 42, "us")
	require.NoError(t, err)
	require.True(t, found)
	require.Zero(t, lease.ReservedCents)
	require.Zero(t, lease.SpentCents)
}

func TestGatewayLiteFreeTokenRateKeepsIndependentMediaCostAcrossFinalizeOrder(t *testing.T) {
	tests := []struct {
		name             string
		billingMode      string
		correctionBefore bool
	}{
		{name: "image_before_finalize", billingMode: "image", correctionBefore: true},
		{name: "image_after_finalize", billingMode: "image"},
		{name: "video_before_finalize", billingMode: "video", correctionBefore: true},
		{name: "video_after_finalize", billingMode: "video"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			prefix := "free-token-media-" + strings.ReplaceAll(tt.name, "_", "-")
			quota := gatewaylite.NewRedisQuota(client, prefix)
			reporter := gatewaylite.NewRedisUsageReporter(client, prefix, gatewayLiteControlStub{})
			const (
				requestID = "request-free-token-media"
				leaseID   = "lease-free-token-media"
			)
			require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
				LeaseID: leaseID, UserID: 42, Region: "us", AllocatedCents: 20,
				Version: 1, ExpiresAt: time.Now().Add(time.Minute).Unix(),
			}))
			_, _, reserved, err := quota.ReserveActiveLease(ctx, gatewaylite.ReserveRequest{
				RequestID: requestID, KeyID: "key-free-token-media", UserID: 42, Region: "us",
				EstimatedCostCents: 1,
			})
			require.NoError(t, err)
			require.True(t, reserved)

			zero := 0.0
			policy := gatewaylite.RatePolicyAudit{
				SchemaVersion: 1, BillingScope: "token", GroupRateMultiplier: 1,
				UserRateMultiplier: &zero, ResolvedRateMultiplier: 0,
				AppliedPeakMultiplier: 1, EffectiveRateMultiplier: 0, Timezone: "UTC",
				ResolvedAtMillis: time.Now().UnixMilli(), Version: "v1:0123456789abcdef",
			}
			require.True(t, policy.Valid())
			correction := gatewaylite.UsageEvent{
				RequestID: requestID, QuotaRequestID: requestID, UserID: 42,
				KeyID: "key-free-token-media", Region: "us", GatewayID: "us-1",
				SettlementMode:    string(gatewaylite.BillingSettlementModeIdempotentV2),
				SettlementPending: true, SettlementMicroCents: 7_000_000,
				BillingMode: tt.billingMode, RateMultiplier: 1.75, RatePolicyAudit: &policy,
			}
			if tt.billingMode == "image" {
				correction.ImageCount = 1
			} else {
				correction.VideoCount = 1
			}
			finalize := func() {
				gatewayLiteFinalizeUsage(reporter, quota, gatewayLiteFinalizeInput{
					RequestID: requestID, KeyID: "key-free-token-media", UserID: 42,
					LeaseID: leaseID, Region: "us", GatewayID: "us-1",
					GroupID: 7, GroupName: "free-token", RateMultiplier: 0, RatePolicyAudit: &policy,
					Method: http.MethodPost, Path: "/v1/responses", Model: "gpt-test", Status: http.StatusOK,
					EstimatedCents: 1, StartedAt: time.Now().Add(-time.Millisecond), EndedAt: time.Now(),
				})
			}

			if tt.correctionBefore {
				require.NoError(t, reporter.ReportUsage(ctx, correction))
				finalize()
			} else {
				finalize()
				require.NoError(t, reporter.ReportUsage(ctx, correction))
			}

			lease, found, err := quota.LoadLease(ctx, 42, "us")
			require.NoError(t, err)
			require.True(t, found)
			require.Zero(t, lease.ReservedCents)
			require.EqualValues(t, 7, lease.SpentCents,
				"independent media cost must remain in the regional lease")
		})
	}
}

type gatewayLiteControlStub struct {
	key            gatewaylite.KeySnapshot
	lease          gatewaylite.LeaseSnapshot
	resolveResp    *gatewaylite.ResolveKeyResponse
	acquireResp    *gatewaylite.AcquireLeaseResponse
	rebalanceResp  *gatewaylite.RebalanceLeaseResponse
	resolveCount   *atomic.Int32
	acquireCount   *atomic.Int32
	rebalanceCount *atomic.Int32
	resolveFn      func(gatewaylite.ResolveKeyRequest)
	acquireFn      func(gatewaylite.AcquireLeaseRequest)
	acquireRespFn  func(gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error)
	rebalanceFn    func(gatewaylite.RebalanceLeaseRequest)
	reportFn       func(gatewaylite.UsageEvent)
	preserveKey    bool
}

type gatewayLiteLocalGroupReaderStub struct {
	group *service.Group
	err   error
}

type gatewayLiteSettlementPolicyStub struct {
	enabled atomic.Bool
}

func (s *gatewayLiteSettlementPolicyStub) GatewayLiteIdempotentSettlementEnabled() bool {
	return s != nil && s.enabled.Load()
}

func (s gatewayLiteLocalGroupReaderStub) GetGroup(_ context.Context, _ int64) (*service.Group, error) {
	return s.group, s.err
}

func TestGatewayLiteQuotaErrorMessageUsesDetail(t *testing.T) {
	require.Contains(t, gatewayLiteQuotaErrorMessage("window_5h"), "5-hour")
	require.Contains(t, gatewayLiteQuotaErrorMessage("window_day"), "daily")
	require.Contains(t, gatewayLiteQuotaErrorMessage("window_week"), "7-day")
	require.Contains(t, gatewayLiteQuotaErrorMessage("period_quota"), "period")
	require.Equal(t, "Insufficient quota", gatewayLiteQuotaErrorMessage(""))
}

func TestGatewayLiteRequestIDMatchesUsageBillingCanonicalID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.ClientRequestID, "shared-request-id"))

	requestID := gatewayLiteRequestID(c)
	require.Equal(t, "client:shared-request-id", requestID)

	ctx := gatewaylite.ContextWithRequestMeta(c.Request.Context(), gatewaylite.RequestMeta{RequestID: requestID})
	canonicalID, ok := gatewaylite.CanonicalRequestIDFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, requestID, canonicalID)
}

func TestGatewayLiteSnapshotToAPIKeyPreservesGatewayRoutingStrategy(t *testing.T) {
	groupID := int64(7)
	userRate := 0.8
	apiKey := gatewayLiteSnapshotToAPIKey("raw-key", gatewaylite.KeySnapshot{
		UserEmail:                "joe@example.com",
		Username:                 "Joe",
		KeyID:                    "key-1",
		UserID:                   42,
		GroupID:                  groupID,
		GroupName:                "openai",
		Platform:                 "openai",
		GatewayStrategy:          "fixed_group",
		KeyDefaultGatewayNodeID:  9,
		UserRateMultiplier:       &userRate,
		UserGroupRateMultipliers: map[int64]float64{7: 0.8, 8: 1.2},
	})
	require.Equal(t, "joe@example.com", apiKey.User.Email)
	require.Equal(t, "Joe", apiKey.User.Username)

	require.Equal(t, "fixed_group", apiKey.GatewayStrategy)
	require.Equal(t, int64(9), apiKey.KeyDefaultGatewayNodeID)
	require.NotNil(t, apiKey.User)
	require.True(t, apiKey.User.UserGroupRPMOverrideResolved, "主站快照已权威确认不支持 user-group override")
	require.NotNil(t, apiKey.SchedulerGroupID())
	require.Equal(t, groupID, *apiKey.SchedulerGroupID())
	require.NotNil(t, apiKey.UserRateMultiplier)
	require.InDelta(t, 0.8, *apiKey.UserRateMultiplier, 1e-12)
	require.Equal(t, map[int64]float64{7: 0.8, 8: 1.2}, apiKey.UserGroupRateMultipliers)
}

func TestGatewayLiteSnapshotToAPIKeyPreservesGroupPerUserConcurrency(t *testing.T) {
	t.Run("typed snapshot field", func(t *testing.T) {
		apiKey := gatewayLiteSnapshotToAPIKey("raw-key", gatewaylite.KeySnapshot{
			UserID:  42,
			GroupID: 7,
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID:                 7,
				LocalGroupID:       7,
				Name:               "openai",
				Platform:           service.PlatformOpenAI,
				Status:             service.StatusActive,
				PerUserConcurrency: 3,
			},
		})

		require.NotNil(t, apiKey.Group)
		require.Equal(t, 3, apiKey.Group.PerUserConcurrency,
			"主站 KeySnapshot 的分组并发限制必须进入请求热路径")
	})

	t.Run("config projection", func(t *testing.T) {
		apiKey := gatewayLiteSnapshotToAPIKey("raw-key", gatewaylite.KeySnapshot{
			UserID:  42,
			GroupID: 7,
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID:           7,
				LocalGroupID: 7,
				Name:         "openai",
				Platform:     service.PlatformOpenAI,
				Status:       service.StatusActive,
				Config:       map[string]any{"per_user_concurrency": float64(5)},
			},
		})

		require.NotNil(t, apiKey.Group)
		require.Equal(t, 5, apiKey.Group.PerUserConcurrency,
			"投影 config 中的分组并发限制也必须被解析")
	})
}

func TestGatewayLiteSnapshotToAPIKeyClonesSharedHotSnapshotMutableFields(t *testing.T) {
	minimumTier := 1
	dailyLimit := 10.0
	imagePrice := 0.25
	fallbackGroupID := int64(9)
	snapshot := gatewaylite.KeySnapshot{
		UserID: 42, GroupID: 7,
		AllowedModels:               []string{"gpt-5.6-sol"},
		UserGroupRateMultipliers:    map[int64]float64{7: 1.2},
		AllowedKeywordRouteGroupIDs: []int64{8},
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID: 7, LocalGroupID: 7, Name: "OpenAI", Platform: service.PlatformOpenAI, Status: service.StatusActive,
			MinimumTier: &minimumTier, DailyLimitUSD: &dailyLimit, ImagePrice1K: &imagePrice,
			FallbackGroupID:      &fallbackGroupID,
			ModelRouting:         map[string][]int64{"gpt-5.6-sol": {11, 12}},
			SupportedModelScopes: []string{"responses"},
			MessagesDispatchModelConfig: gatewaylite.OpenAIMessagesDispatchModelConfig{
				ExactModelMappings: map[string]string{"claude-opus": "gpt-5.6-sol"},
			},
			ModelsListConfig: gatewaylite.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.6-sol"}},
		},
	}

	first := gatewayLiteSnapshotToAPIKey("raw-key", snapshot)
	second := gatewayLiteSnapshotToAPIKey("raw-key", snapshot)
	require.NotNil(t, first.Group)
	require.NotNil(t, second.Group)

	first.AllowedModels[0] = "changed"
	first.UserGroupRateMultipliers[7] = 9
	first.AllowedKeywordRouteGroupIDs[0] = 99
	*first.Group.MinimumTier = 3
	*first.Group.DailyLimitUSD = 99
	*first.Group.ImagePrice1K = 99
	*first.Group.FallbackGroupID = 99
	first.Group.ModelRouting["gpt-5.6-sol"][0] = 99
	first.Group.SupportedModelScopes[0] = "changed"
	first.Group.MessagesDispatchModelConfig.ExactModelMappings["claude-opus"] = "changed"
	first.Group.ModelsListConfig.Models[0] = "changed"

	require.Equal(t, "gpt-5.6-sol", second.AllowedModels[0])
	require.InDelta(t, 1.2, second.UserGroupRateMultipliers[7], 1e-12)
	require.EqualValues(t, 8, second.AllowedKeywordRouteGroupIDs[0])
	require.Equal(t, 1, *second.Group.MinimumTier)
	require.InDelta(t, 10, *second.Group.DailyLimitUSD, 1e-12)
	require.InDelta(t, 0.25, *second.Group.ImagePrice1K, 1e-12)
	require.EqualValues(t, 9, *second.Group.FallbackGroupID)
	require.EqualValues(t, 11, second.Group.ModelRouting["gpt-5.6-sol"][0])
	require.Equal(t, "responses", second.Group.SupportedModelScopes[0])
	require.Equal(t, "gpt-5.6-sol", second.Group.MessagesDispatchModelConfig.ExactModelMappings["claude-opus"])
	require.Equal(t, "gpt-5.6-sol", second.Group.ModelsListConfig.Models[0])

	// 共享 Redis hot snapshot 本体也不能被任一请求改写。
	require.Equal(t, 1, *snapshot.Group.MinimumTier)
	require.EqualValues(t, 11, snapshot.Group.ModelRouting["gpt-5.6-sol"][0])
}

func TestGatewayLiteRemoteAPIKeyIPACLUsesFrozenTrustedClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := gatewayLiteSnapshotToAPIKey("raw-key", gatewaylite.KeySnapshot{
		IPWhitelist: []string{"198.51.100.0/24"},
		// 黑名单必须优先于同时命中的白名单。
		IPBlacklist: []string{"198.51.100.9"},
	})

	contextFor := func(remoteAddr, forwarded string, trustForwarded, customAllowed bool) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Request.RemoteAddr = remoteAddr
		c.Request.Header.Set("X-Verified-Client-IP", forwarded)
		// 本仓库 internal/pkg/ip.SetForwardedIPSettings 为 3 参（无 customAllowed 开关）：
		// 命中 headers 列表的可信代理头一律作为候选客户 IP，语义覆盖 customAllowed=true。
		_ = customAllowed
		ip.SetForwardedIPSettings(c, trustForwarded, []string{"X-Verified-Client-IP"})
		return c
	}

	require.True(t, gatewayLiteRemoteAPIKeyIPAllowed(
		contextFor("198.51.100.8:1234", "203.0.113.8", false, false), apiKey,
	), "关闭转发 IP 信任时必须使用 TCP 对端")
	require.True(t, gatewayLiteRemoteAPIKeyIPAllowed(
		contextFor("127.0.0.1:1234", "198.51.100.8", true, true), apiKey,
	), "可信代理核验后的自定义头应作为客户 IP")
	require.False(t, gatewayLiteRemoteAPIKeyIPAllowed(
		contextFor("127.0.0.1:1234", "198.51.100.9", true, true), apiKey,
	), "黑名单应优先于白名单")
	require.False(t, gatewayLiteRemoteAPIKeyIPAllowed(
		contextFor("127.0.0.1:1234", "203.0.113.8", true, true), apiKey,
	), "有白名单时，未命中的客户 IP 必须拒绝")
	require.True(t, gatewayLiteRemoteAPIKeyIPAllowed(contextFor("203.0.113.8:1234", "", false, false), &service.APIKey{}),
		"空 ACL 不限制请求")
}

func TestGatewayLiteRemoteAPIKeyIPACLRejectsBeforeLeaseAndUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]), Status: service.StatusActive,
			Platform: service.PlatformOpenAI, GroupID: 7, GroupName: "openai", Concurrency: 3,
			AllowedModels: []string{"*"}, IPWhitelist: []string{"198.51.100.0/24"},
		},
		acquireCount: &acquireCount,
	}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(
		stub, runtimeRef, nil, nil, stub, nil,
	)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.RemoteAddr = "203.0.113.8:1234"
	req.Header.Set("Authorization", "Bearer aiceo_sk_key123_"+secret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "ACCESS_DENIED")
	require.Zero(t, acquireCount.Load(), "IP ACL 拒绝必须发生在额度租约和上游调度之前")
}

func TestGatewayLiteSnapshotToAPIKeyUsesLocalGroupIDForScheduling(t *testing.T) {
	const (
		globalGroupID = int64(3000000003)
		localGroupID  = int64(3)
	)
	apiKey := gatewayLiteSnapshotToAPIKey("raw-key", gatewaylite.KeySnapshot{
		KeyID:                   "key-global-group",
		UserID:                  42,
		GroupID:                 globalGroupID,
		GroupName:               "高速",
		Platform:                "openai",
		GatewayStrategy:         "fixed_group",
		KeyDefaultGatewayNodeID: 9,
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID:           globalGroupID,
			LocalGroupID: localGroupID,
			Name:         "高速",
			Platform:     "openai",
			Status:       service.StatusActive,
		},
	})

	require.NotNil(t, apiKey.Group)
	require.Equal(t, localGroupID, apiKey.Group.ID)
	require.NotNil(t, apiKey.SchedulerGroupID())
	require.Equal(t, localGroupID, *apiKey.SchedulerGroupID())
}

func TestGatewayLiteSnapshotToAPIKeyPreservesKeywordPolicyAuthority(t *testing.T) {
	apiKey := gatewayLiteSnapshotToAPIKey("raw-key", gatewaylite.KeySnapshot{
		UserID:                          7,
		GroupID:                         9001,
		AllowedKeywordRouteGroupIDs:     []int64{20, 30},
		KeywordRouteGroupAccessEnforced: true,
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID:                          9001,
			LocalGroupID:                10,
			Name:                        "keyword-allowed",
			Platform:                    service.PlatformOpenAI,
			Status:                      service.StatusActive,
			AllowBlockedKeywordRequests: true,
		},
	})

	require.NotNil(t, apiKey.Group)
	require.True(t, apiKey.Group.AllowBlockedKeywordRequests)
	require.True(t, apiKey.KeywordRouteGroupAccessEnforced)
	require.Equal(t, []int64{20, 30}, apiKey.AllowedKeywordRouteGroupIDs)
}

func TestGatewayLiteAPIKeyAuthUsesGatewayLocalGroupAuthority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{key: gatewaylite.KeySnapshot{
		KeyID:      "key123",
		UserID:     42,
		SecretHash: hex.EncodeToString(hash[:]),
		Status:     service.StatusActive,
		Platform:   service.PlatformOpenAI,
		GroupID:    7,
		GroupName:  "stale-control-plane-group",
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID:                    7,
			LocalGroupID:          7,
			Name:                  "stale-control-plane-group",
			Platform:              service.PlatformOpenAI,
			Status:                service.StatusActive,
			RateMultiplier:        1,
			AllowMessagesDispatch: false,
		},
	}}
	localGroups := gatewayLiteLocalGroupReaderStub{group: &service.Group{
		ID:                    7,
		Name:                  "local-authority-group",
		Platform:              service.PlatformOpenAI,
		Status:                service.StatusActive,
		RateMultiplier:        2,
		PeakRateEnabled:       true,
		PeakStart:             "22:00",
		PeakEnd:               "06:00",
		PeakRateMultiplier:    1.5,
		PeakTimezone:          "Asia/Shanghai",
		AllowMessagesDispatch: true,
		MaxReasoningEffort:    "high",
		ReasoningEffortMappings: []service.ReasoningEffortMapping{
			{From: "max", To: "medium"},
		},
		Hydrated: true,
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})

	router := gin.New()
	router.Use(ClientRequestID())
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(stub, runtimeRef, nil, nil, stub, localGroups)), func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.NotNil(t, apiKey)
		require.NotNil(t, apiKey.Group)
		c.JSON(http.StatusOK, gin.H{
			"group_name":                apiKey.Group.Name,
			"rate_multiplier":           apiKey.Group.RateMultiplier,
			"peak_rate_enabled":         apiKey.Group.PeakRateEnabled,
			"peak_rate_multiplier":      apiKey.Group.PeakRateMultiplier,
			"allow_messages_dispatch":   apiKey.Group.AllowMessagesDispatch,
			"max_reasoning_effort":      apiKey.Group.MaxReasoningEffort,
			"reasoning_effort_mappings": apiKey.Group.ReasoningEffortMappings,
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"group_name":"local-authority-group","rate_multiplier":2,"peak_rate_enabled":true,"peak_rate_multiplier":1.5,"allow_messages_dispatch":true,"max_reasoning_effort":"high","reasoning_effort_mappings":[{"from":"max","to":"medium"}]}`, rec.Body.String())
}

func TestGatewayLiteAPIKeyAuthFreezesSettlementModePerRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{key: gatewaylite.KeySnapshot{
		KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]),
		Status: service.StatusActive, Platform: service.PlatformOpenAI, GroupID: 7,
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID: 7, LocalGroupID: 7, Name: "openai", Platform: service.PlatformOpenAI,
			Status: service.StatusActive, RateMultiplier: 1,
		},
	}}
	localGroups := gatewayLiteLocalGroupReaderStub{group: &service.Group{
		ID: 7, Name: "openai", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, RateMultiplier: 1, Hydrated: true,
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})
	policy := &gatewayLiteSettlementPolicyStub{}
	policy.enabled.Store(true)

	router := gin.New()
	router.Use(ClientRequestID())
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(stub, runtimeRef, nil, nil, stub, localGroups, policy)), func(c *gin.Context) {
		before, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
		require.True(t, ok)
		policy.enabled.Store(false)
		after, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{"before": before.BillingSettlementMode, "after": after.BillingSettlementMode})
	})

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	first := request()
	require.Equal(t, http.StatusOK, first.Code)
	require.JSONEq(t, `{"before":"idempotent_v2","after":"idempotent_v2"}`, first.Body.String())
	second := request()
	require.Equal(t, http.StatusOK, second.Code)
	require.JSONEq(t, `{"before":"legacy","after":"legacy"}`, second.Body.String())
}

func TestGatewayLiteAPIKeyAuthRejectsMissingGatewayLocalGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{key: gatewaylite.KeySnapshot{
		KeyID:      "key123",
		UserID:     42,
		SecretHash: hex.EncodeToString(hash[:]),
		Status:     service.StatusActive,
		Platform:   service.PlatformOpenAI,
		GroupID:    7,
		GroupName:  "deleted-group",
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})
	router := gin.New()
	router.Use(ClientRequestID())
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(stub, runtimeRef, nil, nil, stub, gatewayLiteLocalGroupReaderStub{})), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), service.GatewayGroupUnavailableCode)
}

func TestGatewayLiteAPIKeyAuthPreservesNodeTierAndRejectsBeforeLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, Tier: 0, SecretHash: hex.EncodeToString(hash[:]),
			Status: service.StatusActive, Platform: service.PlatformOpenAI, GroupID: 7,
			AllowedModels: []string{"*"},
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID: 7, LocalGroupID: 7, Name: "node-t1", Platform: service.PlatformOpenAI,
				Status: service.StatusActive, EffectiveRequiredTier: 1,
			},
		},
		acquireCount: &acquireCount,
	}
	localGroups := gatewayLiteLocalGroupReaderStub{group: &service.Group{
		ID: 7, Name: "local-inherit", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true,
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(stub, runtimeRef, nil, nil, stub, localGroups)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "GROUP_TIER_REQUIRED")
	require.Zero(t, acquireCount.Load(), "等级不足必须在申请额度租约前拦截")
}

func TestGatewayLiteAPIKeyAuthAllowsEnterpriseT0KeyForAuthorizedT2Group(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, OrganizationID: 88, Tier: 0,
			SecretHash: hex.EncodeToString(hash[:]), Status: service.StatusActive,
			Platform: service.PlatformOpenAI, GroupID: 7, AllowedModels: []string{"*"},
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID: 7, LocalGroupID: 7, Name: "enterprise-t2", Platform: service.PlatformOpenAI,
				Status: service.StatusActive, EffectiveRequiredTier: 2,
			},
		},
		acquireCount: &acquireCount,
	}
	localGroups := gatewayLiteLocalGroupReaderStub{group: &service.Group{
		ID: 7, Name: "enterprise-t2", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, EffectiveRequiredTier: 2, Hydrated: true,
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})
	var downstreamCount atomic.Int32
	router := gin.New()
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(stub, runtimeRef, nil, nil, stub, localGroups)), func(c *gin.Context) {
		downstreamCount.Add(1)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Equal(t, int32(1), downstreamCount.Load(), "enterprise request should reach the authorized handler")
}

func TestGatewayLiteAPIKeyAuthRejectsLocalOnlyGroupBeforeLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, Tier: 2, SecretHash: hex.EncodeToString(hash[:]),
			Status: service.StatusActive, Platform: service.PlatformOpenAI, GroupID: 7,
			AllowedModels: []string{"*"},
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID: 7, LocalGroupID: 7, Name: "stale-public-group", Platform: service.PlatformOpenAI,
				Status: service.StatusActive,
			},
		},
		acquireCount: &acquireCount,
	}
	localGroups := gatewayLiteLocalGroupReaderStub{group: &service.Group{
		ID: 7, Name: "gateway-local", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, LocalOnly: true, Hydrated: true,
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})
	var downstreamCount atomic.Int32
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(stub, runtimeRef, nil, nil, stub, localGroups)), func(c *gin.Context) {
		downstreamCount.Add(1)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "GROUP_LOCAL_ONLY")
	require.Zero(t, acquireCount.Load(), "仅本地分组必须在申请额度租约前拦截主站密钥")
	require.Zero(t, downstreamCount.Load(), "被拒绝的主站密钥不得进入请求处理链")
}

func (s gatewayLiteControlStub) ResolveKey(_ context.Context, req gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	if s.resolveCount != nil {
		s.resolveCount.Add(1)
	}
	if s.resolveFn != nil {
		s.resolveFn(req)
	}
	if s.resolveResp != nil {
		return s.resolveResp, nil
	}
	key := s.key
	if !s.preserveKey {
		key = gatewayLiteBoundTestSnapshot(key, req)
	}
	return &gatewaylite.ResolveKeyResponse{OK: true, Key: key}, nil
}

func gatewayLiteBoundTestSnapshot(snapshot gatewaylite.KeySnapshot, req gatewaylite.ResolveKeyRequest) gatewaylite.KeySnapshot {
	if snapshot.GroupID <= 0 {
		snapshot.GroupID = 7
	}
	if snapshot.KeyDefaultGatewayNodeID <= 0 {
		snapshot.KeyDefaultGatewayNodeID = 9
	}
	snapshot.GatewayStrategy = "fixed_group"
	if snapshot.Group == nil {
		snapshot.Group = &gatewaylite.GatewayGroupSnapshot{
			ID:       snapshot.GroupID,
			Name:     snapshot.GroupName,
			Platform: snapshot.Platform,
			Status:   service.StatusActive,
		}
	}
	if !snapshot.GatewayAccessEnforced {
		snapshot.GatewayAccessEnforced = true
		snapshot.AvailableGateways = []gatewaylite.GatewayRouteSummary{{
			ID:     snapshot.KeyDefaultGatewayNodeID,
			Code:   req.GatewayCode,
			Region: req.Region,
		}}
	} else {
		for i := range snapshot.AvailableGateways {
			if snapshot.AvailableGateways[i].ID <= 0 {
				snapshot.AvailableGateways[i].ID = snapshot.KeyDefaultGatewayNodeID
			}
		}
	}
	return snapshot
}

func (s gatewayLiteControlStub) AcquireLease(_ context.Context, req gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	if s.acquireCount != nil {
		s.acquireCount.Add(1)
	}
	if s.acquireFn != nil {
		s.acquireFn(req)
	}
	if s.acquireRespFn != nil {
		return s.acquireRespFn(req)
	}
	if s.acquireResp != nil {
		return s.acquireResp, nil
	}
	return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: s.lease}, nil
}

func (s gatewayLiteControlStub) RebalanceLease(_ context.Context, req gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	if s.rebalanceCount != nil {
		s.rebalanceCount.Add(1)
	}
	if s.rebalanceFn != nil {
		s.rebalanceFn(req)
	}
	if s.rebalanceResp != nil {
		return s.rebalanceResp, nil
	}
	return &gatewaylite.RebalanceLeaseResponse{OK: false, Error: "not_configured"}, nil
}

func (s gatewayLiteControlStub) ReportUsage(_ context.Context, event gatewaylite.UsageEvent) error {
	if s.reportFn != nil {
		s.reportFn(event)
	}
	return nil
}

func TestGatewayLiteAPIKeyAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stats := gatewaylite.NewRuntimeStats()
	gatewaylite.SetDefaultRuntimeStats(stats)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			Concurrency:   3,
			RateLimitRPM:  120,
			RateLimitTPM:  100000,
			AllowedModels: []string{"*"},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_1",
			UserID:         42,
			Region:         "sg",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
	}

	router := gin.New()
	router.GET("/probe", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, int64(42), apiKey.User.ID)
		require.Equal(t, service.PlatformOpenAI, apiKey.Group.Platform)
		require.Equal(t, []string{"*"}, apiKey.AllowedModels)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, stats.OnlineUsers(time.Now(), time.Minute))
}

func TestGatewayLiteAPIKeyAuthMiddlewareAcceptsLegacyFMKeyPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "fm_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			AllowedModels: []string{"*"},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_1",
			UserID:         42,
			Region:         "sg",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
	}

	router := gin.New()
	router.GET("/probe", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGatewayLiteAPIKeyAuthMiddlewareDoesNotBillModelsList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	resolveRequests := make(chan gatewaylite.ResolveKeyRequest, 1)
	reported := make(chan gatewaylite.UsageEvent, 1)
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			Concurrency:   3,
			AllowedModels: []string{"*"},
		},
		acquireCount: &acquireCount,
		resolveFn: func(req gatewaylite.ResolveKeyRequest) {
			resolveRequests <- req
		},
		reportFn: func(event gatewaylite.UsageEvent) {
			reported <- event
		},
	}

	router := gin.New()
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, int64(42), apiKey.User.ID)
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": []any{}})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 0, acquireCount.Load())
	select {
	case req := <-resolveRequests:
		require.True(t, req.BillingExempt)
	default:
		t.Fatal("expected resolve request")
	}
	select {
	case event := <-reported:
		t.Fatalf("models list should not report usage: %+v", event)
	default:
	}
}

func TestGatewayLiteAPIKeyAuthMiddlewareBillingIntrospectionHasNoUsageSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	resolveRequests := make(chan gatewaylite.ResolveKeyRequest, 1)
	reported := make(chan gatewaylite.UsageEvent, 1)
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]), Status: service.StatusActive,
			Platform: service.PlatformOpenAI, GroupID: 7, GroupName: "openai", Concurrency: 3,
			AllowedModels: []string{"*"},
		},
		acquireCount: &acquireCount,
		resolveFn:    func(req gatewaylite.ResolveKeyRequest) { resolveRequests <- req },
		reportFn:     func(event gatewaylite.UsageEvent) { reported <- event },
	}
	router := gin.New()
	router.GET("/v1/sub2api/billing", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Zero(t, acquireCount.Load())
	select {
	case request := <-resolveRequests:
		require.True(t, request.BillingExempt)
		require.True(t, request.SkipUsageTouch, "倍率自省的主站 cache miss 也不能更新 Key 最近使用时间")
	default:
		t.Fatal("expected resolve request")
	}
	select {
	case event := <-reported:
		t.Fatalf("billing introspection should not report usage: %+v", event)
	default:
	}
}

func TestGatewayLiteAPIKeyAuthMiddlewareDoesNotBillCodexBackendModelsList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			secret := "secret-value"
			hash := sha256.Sum256([]byte(secret))
			key := "aiceo_sk_key123_" + secret
			var acquireCount atomic.Int32
			resolveRequests := make(chan gatewaylite.ResolveKeyRequest, 1)
			reported := make(chan gatewaylite.UsageEvent, 1)
			stub := gatewayLiteControlStub{
				key: gatewaylite.KeySnapshot{
					KeyID:         "key123",
					UserID:        42,
					SecretHash:    hex.EncodeToString(hash[:]),
					Status:        service.StatusActive,
					Platform:      service.PlatformOpenAI,
					GroupID:       7,
					GroupName:     "openai",
					Concurrency:   3,
					AllowedModels: []string{"*"},
				},
				acquireCount: &acquireCount,
				resolveFn: func(req gatewaylite.ResolveKeyRequest) {
					resolveRequests <- req
				},
				reportFn: func(event gatewaylite.UsageEvent) {
					reported <- event
				},
			}

			router := gin.New()
			router.Handle(method, "/backend-api/codex/models", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
				_, ok := GetAPIKeyFromContext(c)
				require.True(t, ok)
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(method, "/backend-api/codex/models", nil)
			req.Header.Set("Authorization", "Bearer "+key)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code)
			require.EqualValues(t, 0, acquireCount.Load())
			select {
			case req := <-resolveRequests:
				require.True(t, req.BillingExempt)
			default:
				t.Fatal("expected resolve request")
			}
			select {
			case event := <-reported:
				t.Fatalf("Codex models list should not report usage: %+v", event)
			default:
			}
		})
	}
}

func TestGatewayLiteRequestBillableExemptsVideoStatusQueries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{method: http.MethodGet, path: "/models", want: false},
		{method: http.MethodHead, path: "/models", want: false},
		{method: http.MethodGet, path: "/backend-api/codex/models", want: false},
		{method: http.MethodHead, path: "/backend-api/codex/models", want: false},
		{method: http.MethodGet, path: "/v1/models", want: false},
		{method: http.MethodGet, path: "/v1/sub2api/billing", want: false},
		{method: http.MethodHead, path: "/v1/sub2api/billing", want: false},
		{method: http.MethodGet, path: "/v1/videos/video-task-1", want: false},
		{method: http.MethodGet, path: "/v1/videos/video-task-1/content", want: false},
		{method: http.MethodHead, path: "/videos/video-task-2", want: false},
		{method: http.MethodPost, path: "/v1/live", want: false},
		{method: http.MethodPost, path: "/backend-api/codex/realtime/calls", want: false},
		{method: http.MethodGet, path: "/v1/live/call-1", want: false},
		{method: http.MethodGet, path: "/backend-api/codex/call-2", want: false},
		{method: http.MethodPost, path: "/v1/videos/generations", want: true},
		{method: http.MethodGet, path: "/v1/responses", want: true},
	}
	for _, prefix := range []string{"/v1", "/api/v3", "/v3", ""} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
			tests = append(tests, struct {
				method string
				path   string
				want   bool
			}{method, prefix + "/contents/generations/tasks/task-1", false})
		}
		tests = append(tests, struct {
			method string
			path   string
			want   bool
		}{http.MethodPost, prefix + "/contents/generations/tasks", true})
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(tt.method, tt.path, nil)
			require.Equal(t, tt.want, gatewayLiteRequestBillable(c))
		})
	}
}

func TestGatewayLiteAPIKeyAuthMiddlewareSeedanceLookupsHaveNoBillingSideEffects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, prefix := range []string{"/v1", "/api/v3", "/v3", ""} {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodDelete} {
			path := prefix + "/contents/generations/tasks/task-1"
			t.Run(method+" "+path, func(t *testing.T) {
				secret := "test-seedance-secret"
				hash := sha256.Sum256([]byte(secret))
				var acquired, reported atomic.Int32
				var resolved gatewaylite.ResolveKeyRequest
				stub := gatewayLiteControlStub{
					key: gatewaylite.KeySnapshot{KeyID: "seedance-key", UserID: 42, SecretHash: hex.EncodeToString(hash[:]),
						Status: service.StatusActive, Platform: service.PlatformOpenAI, GroupID: 7, AllowedModels: []string{"*"}},
					acquireCount: &acquired,
					resolveFn:    func(req gatewaylite.ResolveKeyRequest) { resolved = req },
					reportFn:     func(gatewaylite.UsageEvent) { reported.Add(1) },
				}
				mini := miniredis.RunT(t)
				client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
				t.Cleanup(func() { _ = client.Close() })
				quota := gatewaylite.NewRedisQuota(client, "seedance-free-auth")
				router := gin.New()
				router.Handle(method, path, gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
					key, ok := GetAPIKeyFromContext(c)
					require.True(t, ok)
					require.Zero(t, key.ID, "远程密钥不需要本地数据库编号")
					meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
					require.True(t, ok)
					require.Equal(t, "seedance-key", meta.KeyID)
					require.Empty(t, meta.LeaseID)
					require.Nil(t, meta.Reservation)
					require.Nil(t, meta.Funding)
					c.Status(http.StatusNoContent)
				})
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer tokenav_sk_seedance-key_"+secret)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
				require.True(t, resolved.BillingExempt, "查询不依赖余额或额度申请")
				require.Zero(t, acquired.Load(), "免费请求不得续租")
				require.Zero(t, reported.Load(), "免费请求不得上报基础一美分用量")
				require.Empty(t, mini.Keys(), "免费请求不得留下金额预留或计费队列")
			})
		}
	}
}

func TestGatewayLiteAPIKeyAuthMiddlewareRejectsUnavailableGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:                 "key123",
			UserID:                42,
			SecretHash:            hex.EncodeToString(hash[:]),
			Status:                service.StatusActive,
			Platform:              service.PlatformOpenAI,
			GroupID:               7,
			GroupName:             "openai",
			GatewayAccessEnforced: true,
			AvailableGateways: []gatewaylite.GatewayRouteSummary{
				{Code: "sg-1", Region: "sg"},
			},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_hk_1",
			UserID:         42,
			Region:         "hk",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
	}

	router := gin.New()
	router.GET("/probe", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "hk", "hk-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestGatewayLiteAPIKeyAuthMiddlewareRejectsMissingFixedGroupBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		preserveKey: true,
		key: gatewaylite.KeySnapshot{
			KeyID:      "key123",
			UserID:     42,
			SecretHash: hex.EncodeToString(hash[:]),
			Status:     service.StatusActive,
			GroupID:    7,
		},
	}

	router := gin.New()
	router.GET("/probe", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "hk", "hk-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "FIXED_GROUP_REQUIRED")
}

func TestGatewayLiteAPIKeyAuthMiddlewareRejectsAnotherAllowedGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		preserveKey: true,
		key: gatewaylite.KeySnapshot{
			KeyID:                   "key123",
			UserID:                  42,
			SecretHash:              hex.EncodeToString(hash[:]),
			Status:                  service.StatusActive,
			GatewayStrategy:         "fixed_group",
			KeyDefaultGatewayNodeID: 9,
			GroupID:                 7,
			Group:                   &gatewaylite.GatewayGroupSnapshot{ID: 7, Status: service.StatusActive},
			GatewayAccessEnforced:   true,
			AvailableGateways: []gatewaylite.GatewayRouteSummary{
				{ID: 9, Code: "sg-1", Region: "sg"},
				{ID: 10, Code: "hk-1", Region: "hk"},
			},
		},
	}

	router := gin.New()
	router.GET("/probe", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "hk", "hk-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "GATEWAY_NOT_ALLOWED")
}

func TestGatewayLiteAPIKeyAuthMiddlewareRejectsPhoneVerificationRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := "aiceo_sk_key123_secret-value"
	stub := gatewayLiteControlStub{
		resolveResp: &gatewaylite.ResolveKeyResponse{OK: false, Error: "phone_verification_required"},
	}

	router := gin.New()
	router.GET("/probe", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "hk", "hk-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "PHONE_VERIFICATION_REQUIRED")
}

func TestGatewayLiteAPIKeyAuthMiddlewareUnknownQuotaErrorIsNotInvalidAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "quotaerr")

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			Concurrency:   3,
			RateLimitRPM:  120,
			RateLimitTPM:  100000,
			AllowedModels: []string{"*"},
		},
		acquireResp: &gatewaylite.AcquireLeaseResponse{
			OK:    false,
			Error: `pq: duplicate key value violates unique constraint "quota_leases_user_id_region_key"`,
		},
	}

	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "QUOTA_UNAVAILABLE")
	require.NotContains(t, rec.Body.String(), "INVALID_API_KEY")
}

func TestGatewayLiteAPIKeyAuthMiddlewareRejectsDisallowedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			AllowedModels: []string{"gpt-4o-mini"},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_1",
			UserID:         42,
			Region:         "sg",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
		acquireCount: &acquireCount,
	}

	router := gin.New()
	router.POST("/v1/chat/completions", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.1","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "MODEL_NOT_ALLOWED")
	require.EqualValues(t, 0, acquireCount.Load())
}

func TestGatewayLiteAPIKeyAuthMiddlewareRejectsGroupModelNotInCustomList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]), Status: service.StatusActive,
			Platform: service.PlatformOpenAI, GroupID: 7, GroupName: "openai", AllowedModels: []string{"*"},
			Group: &gatewaylite.GatewayGroupSnapshot{ID: 7, Name: "openai", Platform: service.PlatformOpenAI, Status: service.StatusActive,
				ModelsListConfig: gatewaylite.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.6-sol"}}},
		},
	}
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "GROUP_MODEL_NOT_ALLOWED")
}

func TestGatewayLiteAPIKeyAuthMiddlewareAllowsGroupCustomModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{key: gatewaylite.KeySnapshot{
		KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]), Status: service.StatusActive,
		Platform: service.PlatformOpenAI, GroupID: 7, GroupName: "openai", AllowedModels: []string{"*"},
		Group: &gatewaylite.GatewayGroupSnapshot{ID: 7, Name: "openai", Platform: service.PlatformOpenAI, Status: service.StatusActive,
			ModelsListConfig: gatewaylite.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.6-*"}}},
	}, lease: gatewaylite.LeaseSnapshot{
		LeaseID: "lease_sg_1", UserID: 42, Region: "sg", AllocatedCents: 100,
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}}
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGatewayLiteRequestModelCoversCompatibleProtocolShapes(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		want        string
	}{
		{name: "responses session model", method: http.MethodPost, path: "/v1/live", contentType: "application/json", body: `{"session":{"model":"gpt-live"}}`, want: "gpt-live"},
		{name: "query model", method: http.MethodPost, path: "/v1/realtime?model=grok-4.6", contentType: "application/json", body: `{}`, want: "grok-4.6"},
		{name: "gemini path model", method: http.MethodPost, path: "/v1beta/models/gemini-3.0:generateContent", contentType: "application/json", body: `{}`, want: "gemini-3.0"},
		{name: "urlencoded model", method: http.MethodPost, path: "/v1/live", contentType: "application/x-www-form-urlencoded", body: "model=gpt-live", want: "gpt-live"},
		{name: "multipart model", method: http.MethodPost, path: "/v1/live", contentType: "multipart/form-data; boundary=model-boundary", body: "--model-boundary\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\ngpt-live\r\n--model-boundary--\r\n", want: "gpt-live"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			model, err := gatewayLiteRequestModel(ginCreateContextForRequestTest(req))
			require.NoError(t, err)
			require.Equal(t, tt.want, model)
		})
	}
}

func ginCreateContextForRequestTest(req *http.Request) *gin.Context {
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req
	return ctx
}

func TestGatewayLiteAPIKeyAuthMiddlewareAllowsModelAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			AllowedModels: []string{"gpt-5*"},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_1",
			UserID:         42,
			Region:         "sg",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
	}

	router := gin.New()
	router.POST("/v1/chat/completions", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", nil, nil, stub)), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"model":"gpt-5.1","messages":[]}`, string(body))
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.1","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGatewayLiteAPIKeyAuthMiddlewareCommitsRedisQuotaAndReportsUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "mwtest")

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	reported := make(chan gatewaylite.UsageEvent, 1)
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:        "key123",
			UserID:       42,
			SecretHash:   hex.EncodeToString(hash[:]),
			Status:       service.StatusActive,
			Platform:     service.PlatformOpenAI,
			GroupID:      7,
			GroupName:    "openai",
			Concurrency:  3,
			RateLimitRPM: 120,
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_1",
			UserID:         42,
			Region:         "sg",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
		reportFn: func(event gatewaylite.UsageEvent) {
			reported <- event
		},
	}

	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/chat/completions", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	// ServeHTTP 返回前必须已经完成 Redis Stream 持久入队，不能留在临时 goroutine 中。
	require.Equal(t, 1, len(reported))
	fields, err := client.HGetAll(ctx, "mwtest:lease:42:sg-1").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, gatewaylite.ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 1, gatewaylite.ParseInt64Field(fields, "spent_cents"))

	select {
	case event := <-reported:
		require.Equal(t, "client:"+rec.Header().Get("X-Client-Request-ID"), event.RequestID)
		require.Equal(t, "key123", event.KeyID)
		require.Equal(t, int64(42), event.UserID)
		require.Equal(t, "lease_sg_1", event.LeaseID)
		require.Equal(t, int64(1), event.ActualCents)
		require.Equal(t, http.StatusOK, event.Status)
	case <-time.After(time.Second):
		t.Fatal("expected usage report")
	}
}

func TestGatewayLiteAPIKeyAuthMiddlewareReportsCorrectionThatArrivedBeforeFinalize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "mw-corrected")

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	reported := make(chan gatewaylite.UsageEvent, 1)
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:         "key123",
			UserID:        42,
			SecretHash:    hex.EncodeToString(hash[:]),
			Status:        service.StatusActive,
			Platform:      service.PlatformOpenAI,
			GroupID:       7,
			GroupName:     "openai",
			Concurrency:   3,
			AllowedModels: []string{"*"},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_corrected",
			UserID:         42,
			Region:         "sg-1",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
		reportFn: func(event gatewaylite.UsageEvent) {
			reported <- event
		},
	}

	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
		requestID, ok := gatewaylite.CanonicalRequestIDFromContext(c.Request.Context())
		require.True(t, ok)
		require.NoError(t, quota.AdjustCommitted(c.Request.Context(), gatewaylite.UsageCommit{RequestID: requestID, ActualCents: 9}))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-mini"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	fields, err := client.HGetAll(ctx, "mw-corrected:lease:42:sg-1").Result()
	require.NoError(t, err)
	require.EqualValues(t, 9, gatewaylite.ParseInt64Field(fields, "spent_cents"))

	select {
	case event := <-reported:
		require.EqualValues(t, 9, event.ActualCents)
		require.Equal(t, http.StatusOK, event.Status)
	case <-time.After(time.Second):
		t.Fatal("expected corrected usage report")
	}
}

func TestGatewayLiteAPIKeyAuthMiddlewareUsesRedisFastPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "fast")
	keyCache := gatewaylite.NewRedisKeyCache(client, "fast")

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var resolveCount atomic.Int32
	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:          "key123",
			UserID:         42,
			SecretHash:     hex.EncodeToString(hash[:]),
			Status:         service.StatusActive,
			Platform:       service.PlatformOpenAI,
			GroupID:        7,
			GroupName:      "openai",
			Concurrency:    3,
			CacheTTLSecond: 60,
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID:        "lease_sg_1",
			UserID:         42,
			Region:         "sg",
			AllocatedCents: 100,
			ExpiresAt:      time.Now().Add(time.Minute).Unix(),
		},
		resolveCount: &resolveCount,
		acquireCount: &acquireCount,
	}

	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/messages", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, keyCache, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code)
	}

	require.EqualValues(t, 1, resolveCount.Load())
	require.EqualValues(t, 1, acquireCount.Load())
}

func TestGatewayLiteAPIKeyAuthMiddlewareRecoversLeaseAfterRedisDataLoss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "redis-recovery")
	keyCache := gatewaylite.NewRedisKeyCache(client, "redis-recovery")

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var resolveCount atomic.Int32
	var acquireCount atomic.Int32
	var authoritativeSpent atomic.Int64
	reported := make(chan gatewaylite.UsageEvent, 2)
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:          "key123",
			UserID:         42,
			SecretHash:     hex.EncodeToString(hash[:]),
			Status:         service.StatusActive,
			Platform:       service.PlatformOpenAI,
			GroupID:        7,
			GroupName:      "openai",
			Concurrency:    3,
			CacheTTLSecond: 60,
		},
		resolveCount: &resolveCount,
		acquireCount: &acquireCount,
		acquireRespFn: func(_ gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
			spent := authoritativeSpent.Load()
			return &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
				LeaseID:        "lease_sg_1",
				UserID:         42,
				Region:         "sg-1",
				AllocatedCents: 100,
				SpentCents:     spent,
				Version:        spent + 1,
				ExpiresAt:      time.Now().Add(time.Minute).Unix(),
			}}, nil
		},
		reportFn: func(event gatewaylite.UsageEvent) {
			authoritativeSpent.Add(event.ActualCents)
			reported <- event
		},
	}

	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, keyCache, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := func() gatewaylite.UsageEvent {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-mini"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		select {
		case event := <-reported:
			return event
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for usage report")
			return gatewaylite.UsageEvent{}
		}
	}

	first := request()
	require.EqualValues(t, 1, first.ActualCents)
	require.EqualValues(t, 1, authoritativeSpent.Load())
	require.EqualValues(t, 1, resolveCount.Load())
	require.EqualValues(t, 1, acquireCount.Load())

	// Redis may restart independently of the control plane. The next request
	// after the bounded process hot-cache window must reload both the key and
	// the authoritative cumulative lease counters.
	require.NoError(t, client.FlushDB(ctx).Err())
	time.Sleep(500*time.Millisecond + 25*time.Millisecond)
	second := request()
	require.EqualValues(t, 1, second.ActualCents)
	require.NotEqual(t, first.RequestID, second.RequestID)
	require.EqualValues(t, 2, authoritativeSpent.Load())
	require.EqualValues(t, 2, resolveCount.Load())
	require.EqualValues(t, 2, acquireCount.Load())

	lease, ok, err := quota.LoadLease(ctx, 42, "sg-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 2, lease.SpentCents)
}

func TestGatewayLiteAPIKeyAuthMiddlewareRebalancesWhenAcquireCannotRefill(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "rebalance")

	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID:        "lease_sg_old",
		UserID:         42,
		Region:         "sg-1",
		AllocatedCents: 1,
		SpentCents:     1,
		ExpiresAt:      time.Now().Add(time.Minute).Unix(),
	}))

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	var rebalanceCount atomic.Int32
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID:        "key123",
			UserID:       42,
			SecretHash:   hex.EncodeToString(hash[:]),
			Status:       service.StatusActive,
			Platform:     service.PlatformOpenAI,
			GroupID:      7,
			GroupName:    "openai",
			Concurrency:  3,
			RateLimitRPM: 120,
		},
		acquireResp: &gatewaylite.AcquireLeaseResponse{OK: false, Error: "insufficient_unallocated_balance"},
		rebalanceResp: &gatewaylite.RebalanceLeaseResponse{
			OK: true,
			Lease: gatewaylite.LeaseSnapshot{
				LeaseID:        "lease_sg_old",
				UserID:         42,
				Region:         "sg",
				AllocatedCents: 50,
				ExpiresAt:      time.Now().Add(time.Minute).Unix(),
			},
			TransferredCents: 50,
			ReleasedRegions:  []string{"us"},
		},
		acquireCount:   &acquireCount,
		rebalanceCount: &rebalanceCount,
	}

	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/messages", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.EqualValues(t, 1, acquireCount.Load())
	require.EqualValues(t, 1, rebalanceCount.Load())

	fields, err := client.HGetAll(ctx, "rebalance:lease:42:sg-1").Result()
	require.NoError(t, err)
	require.Equal(t, "lease_sg_old", fields["lease_id"])
	require.EqualValues(t, 50, gatewaylite.ParseInt64Field(fields, "allocated_cents"))
	require.EqualValues(t, 0, gatewaylite.ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 2, gatewaylite.ParseInt64Field(fields, "spent_cents"))
}

func TestGatewayLiteAPIKeyAuthMiddlewareRebalancesAfterStaleAcquireSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "stale-acquire")
	expiresAt := time.Now().Add(time.Minute).Unix()

	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease_sg", UserID: 42, Region: "sg-1", AllocatedCents: 1,
		SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	var acquireCount atomic.Int32
	var rebalanceCount atomic.Int32
	var observedAcquire gatewaylite.AcquireLeaseRequest
	var observedRebalance gatewaylite.RebalanceLeaseRequest
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]),
			Status: service.StatusActive, Platform: service.PlatformOpenAI,
			GroupID: 7, GroupName: "openai", Concurrency: 3, RateLimitRPM: 120,
		},
		acquireResp: &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
			LeaseID: "lease_sg", UserID: 42, Region: "sg-1", AllocatedCents: 1,
			Version: 1, ExpiresAt: expiresAt,
		}},
		rebalanceResp: &gatewaylite.RebalanceLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
			LeaseID: "lease_sg", UserID: 42, Region: "sg-1", AllocatedCents: 50,
			Version: 2, ExpiresAt: expiresAt,
		}},
		acquireCount:   &acquireCount,
		rebalanceCount: &rebalanceCount,
		acquireFn: func(req gatewaylite.AcquireLeaseRequest) {
			observedAcquire = req
		},
		rebalanceFn: func(req gatewaylite.RebalanceLeaseRequest) {
			observedRebalance = req
		},
	}

	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-mini"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.EqualValues(t, 1, acquireCount.Load())
	require.EqualValues(t, 1, rebalanceCount.Load())
	require.True(t, observedAcquire.LocalExhausted)
	require.Equal(t, "lease_sg", observedAcquire.ObservedLeaseID)
	require.EqualValues(t, 1, observedAcquire.ObservedSpentCents)
	require.True(t, observedRebalance.LocalExhausted)
	require.EqualValues(t, 1, observedRebalance.ObservedSpentCents)

	lease, ok, err := quota.LoadLease(ctx, 42, "sg-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 50, lease.AllocatedCents)
	require.EqualValues(t, 2, lease.SpentCents)
}

func TestGatewayLiteAPIKeyAuthMiddlewareReportsRebalanceSynchronizationFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "stale-rebalance")
	expiresAt := time.Now().Add(time.Minute).Unix()
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: "lease_sg", UserID: 42, Region: "sg-1", AllocatedCents: 1,
		SpentCents: 1, Version: 1, ExpiresAt: expiresAt,
	}))

	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	key := "aiceo_sk_key123_" + secret
	reported := make(chan gatewaylite.UsageEvent, 1)
	staleLease := gatewaylite.LeaseSnapshot{
		LeaseID: "lease_sg", UserID: 42, Region: "sg-1", AllocatedCents: 1,
		Version: 1, ExpiresAt: expiresAt,
	}
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "key123", UserID: 42, SecretHash: hex.EncodeToString(hash[:]),
			Status: service.StatusActive, Platform: service.PlatformOpenAI,
			GroupID: 7, GroupName: "openai", Concurrency: 3, RateLimitRPM: 120,
		},
		acquireResp:   &gatewaylite.AcquireLeaseResponse{OK: true, Lease: staleLease},
		rebalanceResp: &gatewaylite.RebalanceLeaseResponse{OK: true, Lease: staleLease},
		reportFn: func(event gatewaylite.UsageEvent) {
			reported <- event
		},
	}
	router := gin.New()
	router.Use(ClientRequestID())
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(stub, "sg", "sg-1", quota, nil, stub)), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-mini"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	select {
	case event := <-reported:
		require.Equal(t, http.StatusServiceUnavailable, event.Status)
		require.Zero(t, event.ActualCents)
		require.Empty(t, event.UpstreamID)
		require.Contains(t, event.ErrorMessage, "forced rebalance")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pre-dispatch failure report")
	}
}

func TestGatewayLiteLowWatermarkPrefetchCoalescesConcurrentTriggers(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := gatewaylite.NewRedisQuota(client, "prefetch")
	expiresAt := time.Now().Add(time.Hour).Unix()
	lowLease := gatewaylite.LeaseSnapshot{
		LeaseID: "lease-prefetch", UserID: 42, Region: "sg-1",
		AllocatedCents: 100, SpentCents: 80, Version: 1, ExpiresAt: expiresAt,
		TargetCents: 100, RefillAtCents: 20,
	}
	require.NoError(t, quota.EnsureLease(ctx, lowLease))

	var acquireCount atomic.Int32
	stub := gatewayLiteControlStub{
		acquireCount: &acquireCount,
		acquireResp: &gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
			LeaseID: "lease-prefetch", UserID: 42, Region: "sg-1",
			AllocatedCents: 180, Version: 2, ExpiresAt: expiresAt,
			TargetCents: 100, RefillAtCents: 20,
		}},
	}
	key := gatewaylite.KeySnapshot{KeyID: "key-prefetch", UserID: 42}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gatewayLiteMaybePrefetchLease(stub, quota, key, "sg-1", lowLease, 1)
		}()
	}
	wg.Wait()
	require.Eventually(t, func() bool {
		lease, ok, err := quota.LoadLease(ctx, 42, "sg-1")
		return err == nil && ok && lease.AllocatedCents == 180
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, acquireCount.Load())
}

func TestGatewayLiteRoutingLatencyBreakdownUsesSequentialStagesAndRemainder(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	traceContext := service.ContextWithOpsRoutingTrace(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(traceContext)
	service.AddOpsLatencyMs(c, service.OpsRoutingUserSlotLatencyMsKey, 11)
	service.AddOpsLatencyMs(c, service.OpsRoutingBillingLatencyMsKey, 7)
	service.AddOpsLatencyMs(c, service.OpsRoutingSessionPolicyLatencyMsKey, 3)
	service.AddOpsLatencyMs(c, service.OpsRoutingSelectionLatencyMsKey, 41)
	service.AddOpsLatencyMs(c, service.OpsRoutingSelectionLatencyMsKey, 5)
	service.AddOpsLatencyMs(c, service.OpsRoutingPostAccountSlotLatencyMsKey, 2)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageGroupValidation, time.Millisecond)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageStickyLookup, 2*time.Millisecond)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageAccountResolve, 3*time.Millisecond)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageAccountSlot, 4*time.Millisecond)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageStickyRefresh, 5*time.Millisecond)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageCandidateResolution, 6*time.Millisecond)
	service.AddOpsRoutingSelectionLatency(traceContext, service.OpsRoutingSelectionStageSettings, 7*time.Millisecond)

	got := gatewayLiteRoutingLatencyBreakdown(c, 80)
	require.NotNil(t, got)
	require.Equal(t, 1, got.Version)
	require.EqualValues(t, 11, got.UserSlot)
	require.EqualValues(t, 7, got.BillingRecheck)
	require.EqualValues(t, 3, got.SessionPolicy)
	require.EqualValues(t, 46, got.AccountSelection)
	require.EqualValues(t, 2, got.PostSelectionAccountSlot)
	require.EqualValues(t, 1, got.SelectionGroupValidation)
	require.EqualValues(t, 2, got.SelectionStickyLookup)
	require.EqualValues(t, 3, got.SelectionAccountResolve)
	require.EqualValues(t, 4, got.SelectionAccountSlot)
	require.EqualValues(t, 5, got.SelectionStickyRefresh)
	require.EqualValues(t, 6, got.SelectionCandidates)
	require.EqualValues(t, 7, got.SelectionSettings)
	require.EqualValues(t, 18, got.SelectionOther)
	require.EqualValues(t, 11, got.Other)
}

func TestGatewayLiteRoutingLatencyBreakdownClampsNegativeRemainder(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	service.AddOpsLatencyMs(c, service.OpsRoutingSelectionLatencyMsKey, 10)
	got := gatewayLiteRoutingLatencyBreakdown(c, 3)
	require.NotNil(t, got)
	require.Zero(t, got.Other)
}
