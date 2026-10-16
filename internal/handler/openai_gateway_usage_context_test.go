package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}

func TestOpenAIUsageRecordContextCopiesReasoningEffortAuditForLocalKey(t *testing.T) {
	parent := gatewaylite.ContextWithReasoningEffortAudit(context.Background(), "max", "high")

	var got gatewaylite.ReasoningEffortAudit
	var ok bool
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		got, ok = gatewaylite.ReasoningEffortAuditFromContext(ctx)
	})

	require.True(t, ok)
	require.Equal(t, "max", got.Requested)
	require.Equal(t, "high", got.Final)
}

func TestSubmitUsageRecordTaskCopiesGatewayLiteMeta(t *testing.T) {
	parent := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "client-request-123",
		KeyID:     "key123",
		UserID:    42,
		LeaseID:   "lease1",
		Region:    "sg",
	})

	var got gatewaylite.RequestMeta
	var ok bool
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		got, ok = gatewaylite.RequestMetaFromContext(ctx)
	})

	require.True(t, ok)
	require.Equal(t, "client-request-123", got.RequestID)
	require.Equal(t, "key123", got.KeyID)
	require.Equal(t, int64(42), got.UserID)
	require.Equal(t, "lease1", got.LeaseID)
	require.Equal(t, "sg", got.Region)
}

func TestOpenAIUsageRecordContextKeepsRequestStartRateAcrossPeakBoundaryAndAdminEdit(t *testing.T) {
	startedAt := time.Date(2026, time.August, 1, 19, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	group := &service.Group{
		ID:                 17,
		RateMultiplier:     1.25,
		PeakRateEnabled:    true,
		PeakStart:          "18:00",
		PeakEnd:            "22:00",
		PeakRateMultiplier: 2,
		PeakTimezone:       "Asia/Shanghai",
		UpdatedAt:          startedAt.Add(-time.Hour),
	}
	parent, startedPolicy, err := service.FreezeEffectiveRatePolicy(context.Background(), group, nil, startedAt)
	require.NoError(t, err)
	require.Equal(t, 2.5, startedPolicy.EffectiveRateMultiplier)

	// 模拟请求转发后管理员把标准倍率改为 9，且异步结算发生在高峰结束后。
	// worker 只能读取请求开始时不可变快照，不能观察这两个后续变化。
	group.RateMultiplier = 9
	group.PeakRateEnabled = false
	group.UpdatedAt = startedAt.Add(3 * time.Hour)

	var detached gatewaylite.RatePolicyAudit
	var ok bool
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		detached, ok = service.EffectiveRatePolicyFromContext(ctx)
	})

	require.True(t, ok)
	require.Equal(t, startedPolicy, detached)
	require.Equal(t, 2.5, detached.EffectiveRateMultiplier)
	require.NotEqual(t, group.RateMultiplier, detached.GroupRateMultiplier)
}

func TestOpenAIUsageRecordWorkerReceivesFrozenGroupUserAndPeakRate(t *testing.T) {
	startedAt := time.Date(2026, time.August, 1, 23, 30, 0, 0, time.UTC)
	userRate := 0.8
	group := &service.Group{
		ID:                 18,
		RateMultiplier:     1.25,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "06:00",
		PeakRateMultiplier: 2,
		PeakTimezone:       "UTC",
		UpdatedAt:          startedAt.Add(-time.Hour),
	}
	parent, frozen, err := service.FreezeEffectiveRatePolicy(context.Background(), group, &userRate, startedAt)
	require.NoError(t, err)
	require.InDelta(t, 1.6, frozen.EffectiveRateMultiplier, 1e-12)

	pool := newUsageRecordTestPool(t)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
	got := make(chan gatewaylite.RatePolicyAudit, 1)
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		policy, ok := service.EffectiveRatePolicyFromContext(ctx)
		if !ok {
			got <- gatewaylite.RatePolicyAudit{}
			return
		}
		got <- policy
	})

	select {
	case policy := <-got:
		require.Equal(t, frozen, policy)
		require.NotNil(t, policy.UserRateMultiplier)
		require.InDelta(t, userRate, *policy.UserRateMultiplier, 1e-12)
		require.InDelta(t, 2.0, policy.AppliedPeakMultiplier, 1e-12)
		require.InDelta(t, 1.6, policy.EffectiveRateMultiplier, 1e-12)
	case <-time.After(time.Second):
		t.Fatal("usage worker did not receive frozen rate policy")
	}
}

func TestFundingDetachedUsageWorkerQueuesOriginalDelegationWithoutExecutionAuthority(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	control, ref := newOAuthInvocationSocketControl(t)
	quota := gatewaylite.NewRedisQuota(client, "usage-delegation-test")
	reporter := gatewaylite.NewRedisUsageReporter(client, "usage-delegation-test", ref)
	previous := gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(previous) })
	parent := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "server-financial-turn", KeyID: "od_socket", UserID: 33, GatewayID: "socket-node", Region: "socket-region",
		EntryGatewayNodeID: 5, EntryGlobalGroupID: 50, RateMultiplier: 1, BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
	})
	parent, err := gatewaylite.ContextWithOAuthInvocationCredential(parent, oauthInvocationSocketToken, "socket-node", "socket-region", &control.resolved)
	require.NoError(t, err)
	parent, err = gatewaylite.EnsureOAuthInvocationUpperBound(parent, ref, quota, 10_000_000, "text:gpt-5.4,output_tokens=100,n=1", "gpt-5.4", "/v1/responses")
	require.NoError(t, err)
	original, ok := gatewaylite.OAuthInvocationReservationFromContext(parent)
	require.True(t, ok)
	pool := newUsageRecordTestPool(t)
	h := &OpenAIGatewayHandler{usageRecordWorkerPool: pool}
	type outcome struct {
		credential, executable bool
		reservation            gatewaylite.OAuthInvocationReservation
		err                    error
	}
	done := make(chan outcome, 1)
	h.submitOpenAIUsageRecordTask(parent, &service.OpenAIForwardResult{}, func(ctx context.Context) {
		reservation, _ := gatewaylite.OAuthInvocationReservationFromContext(ctx)
		_, executable := gatewaylite.OAuthInvocationRequestFromContext(ctx)
		a, _ := gatewaylite.FundingFromContext(ctx)
		require.ErrorIs(t, gatewaylite.PrepareFundingDispatch(ctx), gatewaylite.ErrRequestQuotaUnavailable, "后台资金快照不能再次派发")
		event := gatewaylite.UsageEvent{Model: "gpt-5.4", SettlementMode: string(gatewaylite.BillingSettlementModeIdempotentV2)}
		gatewaylite.AttachFundingUsage(&event, a, 2_000_001, 2, true)
		done <- outcome{gatewaylite.HasOAuthInvocationCredential(ctx), executable, reservation, gatewaylite.ReportUsageCorrection(ctx, event)}
	})
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.False(t, got.credential, "a financial worker cannot inherit a raw invocation token")
		require.False(t, got.executable, "the copied reservation is read-only usage evidence")
		require.Equal(t, original, got.reservation)
	case <-time.After(3 * time.Second):
		t.Fatal("delegated usage worker did not queue its original financial snapshot")
	}
	messages, err := client.XRange(context.Background(), "usage-delegation-test:usage:stream", "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, messages, 1)
	raw := messages[0].Values["event"].(string)
	require.NotContains(t, raw, oauthInvocationSocketToken)
	var queued struct {
		Event gatewaylite.UsageEvent `json:"event"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &queued))
	require.Equal(t, &original, queued.Event.OAuthInvocationReservation)
	require.Equal(t, original.FundingAuthorizationID, queued.Event.FundingAuthorizationID)
	require.Equal(t, original.RequestID, queued.Event.RequestID)
}
