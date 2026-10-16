package gatewaylite

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type asyncReservationHarness struct {
	ctx          context.Context
	client       *redis.Client
	quota        *RedisQuota
	reporter     *RedisUsageReporter
	event        UsageEvent
	leaseKey     string
	requestKey   string
	remainderKey string
}

func newAsyncReservationHarness(t *testing.T, mode BillingSettlementMode, organization bool) *asyncReservationHarness {
	t.Helper()
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	quota := NewRedisQuota(client, "async-reservation")
	reporter := NewRedisUsageReporter(client, quota.Prefix(), usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	reporter.postUsageLeaseRefill = nil
	event := UsageEvent{RequestID: "original-create", QuotaRequestID: "original-create", KeyID: "original-key",
		UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33, Region: "original-gw", GatewayID: "original-gw",
		LeaseID: "original-lease", SettlementMode: string(mode), CommitAsyncReservation: true, AsyncReservationMicroCents: 6000 * actualCostMicroCentsScale}
	if organization {
		event.OrganizationID = 91
		event.BillingSubjectType, event.BillingSubjectID = "organization", event.OrganizationID
	}
	if mode == BillingSettlementModeIdempotentV2 {
		event.SettlementPending, event.SettlementMicroCents = true, event.AsyncReservationMicroCents
	}
	h := &asyncReservationHarness{ctx: context.Background(), client: client, quota: quota, reporter: reporter, event: event}
	lease := LeaseSnapshot{LeaseID: event.LeaseID, UserID: event.UserID, OrganizationID: event.OrganizationID,
		BillingSubjectType: event.BillingSubjectType, BillingSubjectID: event.BillingSubjectID, Region: event.Region,
		AllocatedCents: 1000000, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, quota.EnsureLease(h.ctx, lease))
	h.leaseKey = quota.leaseKeyForSubject(lease.Subject(), event.Region)
	h.requestKey = quota.requestKey(event.RequestID)
	h.remainderKey = quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(event))
	h.reserve(t, event, 2)
	return h
}

func (h *asyncReservationHarness) reserve(t *testing.T, event UsageEvent, cents int64) {
	t.Helper()
	reserved, err := h.quota.Reserve(h.ctx, ReserveRequest{RequestID: event.RequestID, KeyID: event.KeyID,
		UserID: event.UserID, OrganizationID: event.OrganizationID, BillingSubjectType: event.BillingSubjectType,
		BillingSubjectID: event.BillingSubjectID, Region: event.Region, LeaseID: event.LeaseID, EstimatedCostCents: cents})
	require.NoError(t, err)
	require.True(t, reserved)
}

func asyncEventCost(event UsageEvent, micro int64) UsageEvent {
	event.AsyncReservationMicroCents = micro
	if event.SettlementPending {
		event.SettlementMicroCents = micro
	}
	return event
}

func (h *asyncReservationHarness) snapshot(t *testing.T) map[string]string {
	t.Helper()
	keys, err := h.client.Keys(h.ctx, h.quota.Prefix()+":*").Result()
	require.NoError(t, err)
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		kind, err := h.client.Type(h.ctx, key).Result()
		require.NoError(t, err)
		var value any
		switch kind {
		case "hash":
			value, err = h.client.HGetAll(h.ctx, key).Result()
		case "string":
			value, err = h.client.Get(h.ctx, key).Result()
		case "stream":
			entries, readErr := h.client.XRange(h.ctx, key, "-", "+").Result()
			require.NoError(t, readErr)
			info, infoErr := h.client.XInfoStream(h.ctx, key).Result()
			err = infoErr
			value = struct {
				Entries []redis.XMessage
				Info    *redis.XInfoStream
			}{Entries: entries, Info: info}
		default:
			t.Fatalf("unexpected test key type %s", kind)
		}
		require.NoError(t, err)
		body, err := json.Marshal(value)
		require.NoError(t, err)
		result[key] = kind + ":" + string(body)
	}
	return result
}

func (h *asyncReservationHarness) assertSettled(t *testing.T, actual int64, events int64) {
	t.Helper()
	request, err := h.client.HGetAll(h.ctx, h.requestKey).Result()
	require.NoError(t, err)
	require.Equal(t, "committed", request["status"])
	require.Equal(t, strconv.FormatInt(actual, 10), request["actual_cents"])
	require.Equal(t, "1", request["async_reservation_committed"])
	require.Equal(t, h.event.LeaseID, request["lease_id"])
	lease, found, err := h.quota.LoadLeaseForSubject(h.ctx, LeaseSubject{UserID: h.event.UserID, OrganizationID: h.event.OrganizationID,
		BillingSubjectType: h.event.BillingSubjectType, BillingSubjectID: h.event.BillingSubjectID}, h.event.Region)
	require.NoError(t, err)
	require.True(t, found)
	require.EqualValues(t, actual, lease.SpentCents)
	require.Zero(t, lease.ReservedCents)
	require.EqualValues(t, events, mustStreamLength(t, h.client, h.reporter.streamKey()))
}

func TestAsyncReservationUsageCommitsOriginalHold(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		for _, subject := range []string{"user", "organization"} {
			t.Run(string(mode)+"_"+subject, func(t *testing.T) {
				h := newAsyncReservationHarness(t, mode, subject == "organization")
				require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
				h.assertSettled(t, 6000, 1)
				before := h.snapshot(t)
				require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
				require.Equal(t, before, h.snapshot(t), "同请求重放不能改账务或再入队")
				messages, err := h.client.XRange(h.ctx, h.reporter.streamKey(), "-", "+").Result()
				require.NoError(t, err)
				var queued queuedUsageEvent
				require.NoError(t, json.Unmarshal([]byte(messages[0].Values["event"].(string)), &queued))
				require.False(t, queued.Event.CommitAsyncReservation)
				require.Zero(t, queued.Event.AsyncReservationMicroCents)
				require.Empty(t, queued.Event.QuotaRequestID)
				require.False(t, queued.Event.Final)
				require.Equal(t, string(mode), queued.Event.SettlementMode)
				require.Equal(t, h.event.RequestID, queued.Event.RequestID)
				require.Equal(t, "6000", messages[0].Values["resolved_actual_cents"])
			})
		}
	}
}

func TestAsyncReservationUsageFractionalZeroAndConflict(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			h := newAsyncReservationHarness(t, mode, false)
			first := asyncEventCost(h.event, 600000)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, first))
			h.assertSettled(t, 0, 1)
			before := h.snapshot(t)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, first))
			require.Equal(t, before, h.snapshot(t))
			for _, micro := range []int64{0, 700000} {
				require.ErrorContains(t, h.reporter.ReportUsage(h.ctx, asyncEventCost(first, micro)), "SETTLEMENT_CONFLICT")
				require.Equal(t, before, h.snapshot(t), "原始金额不同不能改零钱或冻结结果")
			}
			second := asyncEventCost(h.event, 400000)
			second.RequestID, second.QuotaRequestID = "second", "second"
			h.reserve(t, second, 1)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, second))
			zero := asyncEventCost(h.event, 0)
			zero.RequestID, zero.QuotaRequestID = "zero", "zero"
			h.reserve(t, zero, 1)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, zero))
			require.NoError(t, h.reporter.ReportUsage(h.ctx, zero))
			lease, _, err := h.quota.LoadLease(h.ctx, h.event.UserID, h.event.Region)
			require.NoError(t, err)
			require.EqualValues(t, 1, lease.SpentCents)
			require.Zero(t, lease.ReservedCents)
			require.Equal(t, "0", h.client.Get(h.ctx, h.remainderKey).Val())
			require.EqualValues(t, 3, mustStreamLength(t, h.client, h.reporter.streamKey()))
		})
	}
}

func TestAsyncReservationUsageConcurrentReplay(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			h := newAsyncReservationHarness(t, mode, false)
			var wg sync.WaitGroup
			errs := make(chan error, 24)
			for i := 0; i < cap(errs); i++ {
				wg.Add(1)
				go func() { defer wg.Done(); errs <- h.reporter.ReportUsage(h.ctx, h.event) }()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			h.assertSettled(t, 6000, 1)
		})
	}
}

func TestAsyncReservationUsageResponseLossReplay(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			h := newAsyncReservationHarness(t, mode, false)
			fault := &settlementEvalFaultHook{}
			h.client.AddHook(fault)
			fault.arm(evalFaultAfterExecution, usageSettlementRedisAttempts)
			require.ErrorIs(t, h.reporter.ReportUsage(h.ctx, h.event), errSimulatedRedisResponseLost)
			h.assertSettled(t, 6000, 1)
			before := h.snapshot(t)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
			require.Equal(t, before, h.snapshot(t))
		})
	}
}

func TestAsyncReservationUsageRefillKeepsOriginalSubject(t *testing.T) {
	h := newAsyncReservationHarness(t, BillingSettlementModeLegacy, true)
	require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "refill_at_cents", "995000").Err())
	var got leaseRefillSignal
	var gateway string
	h.reporter.postUsageLeaseRefill = func(_ *RedisQuota, signal leaseRefillSignal, code string) {
		got, gateway = signal, code
	}
	require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
	require.True(t, got.Needed)
	require.Equal(t, h.event.LeaseID, got.Lease.LeaseID)
	require.Equal(t, h.event.UserID, got.Lease.UserID)
	require.Equal(t, h.event.OrganizationID, got.Lease.OrganizationID)
	require.Equal(t, h.event.BillingSubjectType, got.Lease.BillingSubjectType)
	require.Equal(t, h.event.BillingSubjectID, got.Lease.BillingSubjectID)
	require.Equal(t, h.event.GatewayID, gateway)
	h.assertSettled(t, 6000, 1)
}

func TestAsyncReservationUsageRejectsIncompleteOriginalContext(t *testing.T) {
	for _, field := range []string{"request", "key", "lease", "region", "subject", "mode", "reservation"} {
		t.Run(field, func(t *testing.T) {
			h := newAsyncReservationHarness(t, BillingSettlementModeLegacy, false)
			previous := DefaultUsageReporter()
			SetDefaultUsageReporter(h.reporter)
			t.Cleanup(func() { SetDefaultUsageReporter(previous) })
			meta := RequestMeta{RequestID: h.event.RequestID, KeyID: h.event.KeyID, UserID: h.event.UserID,
				BillingSubjectType: h.event.BillingSubjectType, BillingSubjectID: h.event.BillingSubjectID,
				Region: h.event.Region, GatewayID: h.event.GatewayID, LeaseID: h.event.LeaseID,
				AsyncSettlement: true, CommitAsyncReservation: true, BillingSettlementMode: BillingSettlementModeLegacy}
			event := h.event
			switch field {
			case "request":
				meta.RequestID, event.RequestID, event.QuotaRequestID = "", "", ""
			case "key":
				meta.KeyID, event.KeyID = "", ""
			case "lease":
				meta.LeaseID, event.LeaseID = "", ""
			case "region":
				meta.Region, event.Region = "", ""
			case "subject":
				meta.BillingSubjectType, event.BillingSubjectType = "", ""
			case "mode":
				meta.BillingSettlementMode = BillingSettlementModeIdempotentV2
			case "reservation":
				meta.Reservation = NewRequestReservation(2, meta.LeaseID)
			}
			before := h.snapshot(t)
			require.ErrorContains(t, ReportUsageCorrection(ContextWithRequestMeta(h.ctx, meta), event), "SETTLEMENT_CONFLICT")
			require.Equal(t, before, h.snapshot(t))
		})
	}
}

func TestAsyncReservationUsageRequiresAtomicReporter(t *testing.T) {
	for _, name := range []string{"nil", "sink_only", "disabled_redis"} {
		t.Run(name, func(t *testing.T) {
			h := newAsyncReservationHarness(t, BillingSettlementModeLegacy, false)
			previous := DefaultUsageReporter()
			t.Cleanup(func() { SetDefaultUsageReporter(previous) })
			var reporter UsageReportClient
			sink := usageSinkFunc(func(context.Context, UsageEvent) error {
				t.Fatal("不能先直送主站再假装已提交")
				return nil
			})
			switch name {
			case "sink_only":
				reporter = sink
			case "disabled_redis":
				reporter = NewRedisUsageReporter(nil, h.quota.Prefix(), sink)
			}
			SetDefaultUsageReporter(reporter)
			meta := RequestMeta{RequestID: h.event.RequestID, KeyID: h.event.KeyID, UserID: h.event.UserID,
				BillingSubjectType: h.event.BillingSubjectType, BillingSubjectID: h.event.BillingSubjectID,
				Region: h.event.Region, GatewayID: h.event.GatewayID, LeaseID: h.event.LeaseID,
				AsyncSettlement: true, CommitAsyncReservation: true, BillingSettlementMode: BillingSettlementModeLegacy}
			before := h.snapshot(t)
			require.ErrorIs(t, ReportUsageCorrection(ContextWithRequestMeta(h.ctx, meta), h.event), ErrRequestQuotaUnavailable)
			require.Equal(t, before, h.snapshot(t))
		})
	}
}
