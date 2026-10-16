package service

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSeedanceAsyncReservationIntentUsesOriginalIdentity(t *testing.T) {
	for _, mode := range []gatewaylite.BillingSettlementMode{gatewaylite.BillingSettlementModeLegacy, gatewaylite.BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			meta := gatewaylite.RequestMeta{RequestID: "original", KeyID: "owner-key", UserID: 33,
				OrganizationID: 91, BillingSubjectType: "organization", BillingSubjectID: 91,
				LeaseID: "original-lease", Region: "original-gw", GatewayID: "original-gw", BillingSettlementMode: mode,
				Reservation: gatewaylite.NewRequestReservation(2, "original-lease")}
			pending := &GrokVideoPendingBilling{RequestMeta: &meta}
			poll := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "free-poll", LeaseID: "new-lease"})
			ctx := SeedanceCompletionBillingContext(poll, pending)
			restored, ok := gatewaylite.RequestMetaFromContext(ctx)
			require.True(t, ok)
			require.True(t, restored.CommitAsyncReservation)
			require.True(t, restored.AsyncSettlement)
			require.Nil(t, restored.Reservation)
			require.NotNil(t, meta.Reservation, "原快照不被完成上下文原地修改")
			require.Equal(t, meta.RequestID, restored.RequestID)
			require.Equal(t, meta.LeaseID, restored.LeaseID)
			require.Equal(t, meta.OrganizationID, restored.OrganizationID)
			require.Equal(t, meta.BillingSubjectID, restored.BillingSubjectID)
			require.Equal(t, mode, restored.BillingSettlementMode)
			event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{RequestID: "original", ActualCost: 60})
			require.True(t, ok)
			require.True(t, event.CommitAsyncReservation)
			require.EqualValues(t, 6000000000, event.AsyncReservationMicroCents)
			require.Zero(t, event.ActualCents)
			require.False(t, event.Final)
			require.Equal(t, mode == gatewaylite.BillingSettlementModeIdempotentV2, event.SettlementPending)
			require.Equal(t, string(mode), event.SettlementMode)
		})
	}
}

func TestSeedanceAsyncReservationIntentDoesNotLeakToOtherPaths(t *testing.T) {
	for _, path := range []string{"local", "funded", "ordinary_async", "ordinary_http", "turn"} {
		t.Run(path, func(t *testing.T) {
			meta := gatewaylite.RequestMeta{RequestID: "request", KeyID: "key", UserID: 33,
				BillingSubjectType: "user", BillingSubjectID: 33, LeaseID: "lease", Region: "gw", GatewayID: "gw",
				BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2}
			ctx := gatewaylite.ContextWithRequestMeta(context.Background(), meta)
			switch path {
			case "local":
				ctx = SeedanceCompletionBillingContext(context.Background(), &GrokVideoPendingBilling{TaskID: "local-task"})
				_, ok := gatewaylite.RequestMetaFromContext(ctx)
				require.False(t, ok)
				return
			case "funded":
				auth := gatewaylite.FundingAuthorization{AuthorizationID: "auth", RequestID: meta.RequestID, KeyID: meta.KeyID,
					UserID: meta.UserID, BillingSubjectType: "user", BillingSubjectID: meta.UserID, Version: 1}
				ctx = SeedanceCompletionBillingContext(ctx, &GrokVideoPendingBilling{RequestMeta: &meta, FundingSnapshot: &auth})
			case "ordinary_async":
				ctx = gatewaylite.ContextWithAsyncSettlement(ctx)
			case "turn":
				meta.Reservation = gatewaylite.NewTurnRequestReservation(1, "lease")
				ctx = gatewaylite.ContextWithRequestMeta(ctx, meta)
			}
			event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{RequestID: "request", ActualCost: 0.015})
			require.True(t, ok)
			require.False(t, event.CommitAsyncReservation)
			require.Zero(t, event.AsyncReservationMicroCents)
			require.Equal(t, path == "funded" || path == "turn", event.Final)
			if path == "funded" {
				require.Empty(t, event.LeaseID)
				require.Equal(t, "auth", event.FundingAuthorizationID)
			}
		})
	}
}

func TestSeedanceAsyncReservationEventDoesNotAdvanceLegacyCarry(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	previous := gatewaylite.DefaultRedisQuota()
	gatewaylite.SetDefaultRedisQuota(gatewaylite.NewRedisQuota(client, "seedance-event"))
	t.Cleanup(func() { gatewaylite.SetDefaultRedisQuota(previous) })
	ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "original", KeyID: "key", UserID: 33,
		BillingSubjectType: "user", BillingSubjectID: 33, LeaseID: "lease", Region: "gw", GatewayID: "gw",
		AsyncSettlement: true, CommitAsyncReservation: true, BillingSettlementMode: gatewaylite.BillingSettlementModeLegacy})
	for i := 0; i < 3; i++ {
		event, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{RequestID: "original", ActualCost: 0.006})
		require.True(t, ok)
		require.EqualValues(t, 600000, event.AsyncReservationMicroCents)
		require.Zero(t, event.ActualCents)
	}
	keys, err := client.Keys(context.Background(), "*").Result()
	require.NoError(t, err)
	require.Empty(t, keys, "事件构造不得在Lua身份预检前修改共享零钱")
}

func TestSeedanceAsyncReservationRejectsInvalidCost(t *testing.T) {
	for _, tc := range []struct {
		name string
		cost float64
	}{{"negative", -1}, {"nan", math.NaN()}, {"infinite", math.Inf(1)}, {"unsafe", 1e20}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "original", CommitAsyncReservation: true})
			_, ok := gatewayLiteUsageCorrectionEvent(ctx, &UsageLog{RequestID: "original", ActualCost: tc.cost})
			require.False(t, ok)
		})
	}
}

func TestSeedanceAsyncReservationIntentIsNotPersistedInTaskMeta(t *testing.T) {
	meta := gatewaylite.RequestMeta{RequestID: "original", CommitAsyncReservation: true, AsyncSettlement: true}
	body, err := json.Marshal(meta)
	require.NoError(t, err)
	var restored gatewaylite.RequestMeta
	require.NoError(t, json.Unmarshal(body, &restored))
	require.False(t, restored.CommitAsyncReservation, "完成意图只能由已认领任务的恢复路径重新生成")
	require.Equal(t, meta.RequestID, restored.RequestID)
}
