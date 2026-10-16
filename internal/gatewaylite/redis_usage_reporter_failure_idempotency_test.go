package gatewaylite

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisUsageReporterSettlementLeaseWrongTypeChangesNothing(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	const (
		prefix         = "settlement-lease-wrongtype"
		quotaRequestID = "quota-request"
		settlementID   = "settlement-request"
		leaseID        = "lease-wrongtype"
		keyID          = "key-wrongtype"
	)
	quota := NewRedisQuota(client, prefix)
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: leaseID, UserID: 42, Region: "sg", AllocatedCents: 100,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	reserved, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: quotaRequestID, KeyID: keyID, UserID: 42, Region: "sg",
		LeaseID: leaseID, EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	event := webSocketTurnUsageEvent(settlementID, quotaRequestID, keyID, 1_500_000)
	remainderKey := quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(event))
	require.NoError(t, client.Set(ctx, remainderKey, 250_000, time.Hour).Err())
	quotaRequestBefore, err := client.HGetAll(ctx, quota.requestKey(quotaRequestID)).Result()
	require.NoError(t, err)

	leaseKey := quota.leaseKey(42, "sg")
	require.NoError(t, client.Del(ctx, leaseKey).Err())
	require.NoError(t, client.Set(ctx, leaseKey, "wrong-type", 0).Err())

	err = reporter.ReportUsage(ctx, event)
	require.Error(t, err)
	require.Contains(t, err.Error(), "WRONGTYPE")

	// Lua errors do not roll back prior writes. A lease type check therefore has
	// to happen before freezing the fractional carry or creating the event hash.
	remainder, getErr := client.Get(ctx, remainderKey).Int64()
	require.NoError(t, getErr)
	require.EqualValues(t, 250_000, remainder)
	quotaRequestAfter, getErr := client.HGetAll(ctx, quota.requestKey(quotaRequestID)).Result()
	require.NoError(t, getErr)
	require.Equal(t, quotaRequestBefore, quotaRequestAfter)
	settlementType, getErr := client.Type(ctx, quota.requestKey(settlementID)).Result()
	require.NoError(t, getErr)
	require.Equal(t, "none", settlementType)
	leaseValue, getErr := client.Get(ctx, leaseKey).Result()
	require.NoError(t, getErr)
	require.Equal(t, "wrong-type", leaseValue)
	require.Zero(t, mustStreamLength(t, client, reporter.streamKey()))
}

func TestRedisUsageReporterSettlementRetryAfterPreExecutionRedisFailure(t *testing.T) {
	h := newSettlementFailureHarness(t, "settlement-pre-execution-failure")
	// Exhaust the reporter's bounded internal retries so the caller must retry
	// the same request_id after Redis becomes available again.
	h.fault.arm(evalFaultBeforeExecution, usageSettlementRedisAttempts)

	err := h.reporter.ReportUsage(h.ctx, h.event)
	require.ErrorIs(t, err, errSimulatedRedisUnavailable)
	assertSettlementFailureHarnessUntouched(t, h)

	require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
	require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
	assertSettlementFailureHarnessAppliedOnce(t, h)
}

func TestRedisUsageReporterSettlementRetryAfterSuccessfulLuaResponseLoss(t *testing.T) {
	h := newSettlementFailureHarness(t, "settlement-response-loss")
	h.fault.arm(evalFaultAfterExecution, usageSettlementRedisAttempts)

	// The caller sees an error even though Redis has already committed all
	// writes. Retrying the same financial event must recover that frozen result.
	err := h.reporter.ReportUsage(h.ctx, h.event)
	require.ErrorIs(t, err, errSimulatedRedisResponseLost)
	assertSettlementFailureHarnessAppliedOnce(t, h)

	require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
	zeroReplay := h.event
	zeroReplay.SettlementMicroCents = 0
	require.NoError(t, h.reporter.ReportUsage(h.ctx, zeroReplay))
	assertSettlementFailureHarnessAppliedOnce(t, h)
}

type settlementFailureHarness struct {
	ctx      context.Context
	client   *redis.Client
	quota    *RedisQuota
	reporter *RedisUsageReporter
	fault    *settlementEvalFaultHook
	event    UsageEvent
}

func newSettlementFailureHarness(t *testing.T, prefix string) settlementFailureHarness {
	t.Helper()
	ctx := context.Background()
	mr := miniredis.RunT(t)
	fault := &settlementEvalFaultHook{}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	client.AddHook(fault)
	t.Cleanup(func() { _ = client.Close() })

	quota := NewRedisQuota(client, prefix)
	reporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-failure", UserID: 42, Region: "sg", AllocatedCents: 100,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	reserved, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "quota-request", KeyID: "key-failure", UserID: 42, Region: "sg",
		LeaseID: "lease-failure", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, reserved)

	return settlementFailureHarness{
		ctx:      ctx,
		client:   client,
		quota:    quota,
		reporter: reporter,
		fault:    fault,
		event:    webSocketTurnUsageEvent("settlement-request", "quota-request", "key-failure", 1_500_000),
	}
}

func assertSettlementFailureHarnessUntouched(t *testing.T, h settlementFailureHarness) {
	t.Helper()
	settlementType, err := h.client.Type(h.ctx, h.quota.requestKey(h.event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "none", settlementType)
	remainderType, err := h.client.Type(h.ctx, h.quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(h.event))).Result()
	require.NoError(t, err)
	require.Equal(t, "none", remainderType)
	require.Zero(t, mustStreamLength(t, h.client, h.reporter.streamKey()))

	quotaRequest, err := h.client.HGetAll(h.ctx, h.quota.requestKey(h.event.QuotaRequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "reserved", quotaRequest["status"])
	require.Equal(t, "1", quotaRequest["estimated_cents"])
	require.Empty(t, quotaRequest["child_actual_cents"])
	lease, err := h.client.HGetAll(h.ctx, h.quota.leaseKey(42, "sg")).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, ParseInt64Field(lease, "reserved_cents"))
	require.Zero(t, ParseInt64Field(lease, "spent_cents"))
}

func assertSettlementFailureHarnessAppliedOnce(t *testing.T, h settlementFailureHarness) {
	t.Helper()
	request, err := h.client.HGetAll(h.ctx, h.quota.requestKey(h.event.RequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1500000", request["settlement_micro_cents"])
	require.Equal(t, "1", request["settlement_actual_cents"])
	require.Equal(t, "1", request["settlement_quota_applied"])
	require.Equal(t, "1", request["settlement_usage_enqueued"])

	remainder, err := h.client.Get(h.ctx, h.quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(h.event))).Int64()
	require.NoError(t, err)
	require.EqualValues(t, 500_000, remainder)
	require.Equal(t, []int64{1}, settlementStreamResolvedCents(t, h.client, h.reporter.streamKey()))

	quotaRequest, err := h.client.HGetAll(h.ctx, h.quota.requestKey(h.event.QuotaRequestID)).Result()
	require.NoError(t, err)
	require.Equal(t, "1", quotaRequest["child_actual_cents"])
	require.Equal(t, "1", quotaRequest["corrected_actual_cents"])
	lease, err := h.client.HGetAll(h.ctx, h.quota.leaseKey(42, "sg")).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, ParseInt64Field(lease, "reserved_cents"))
	require.Zero(t, ParseInt64Field(lease, "spent_cents"))
}

var (
	errSimulatedRedisUnavailable  = errors.New("simulated Redis unavailable before execution")
	errSimulatedRedisResponseLost = errors.New("simulated Redis response lost after execution")
)

type evalFaultMode int32

const (
	evalFaultDisabled evalFaultMode = iota
	evalFaultBeforeExecution
	evalFaultAfterExecution
)

type settlementEvalFaultHook struct {
	mode      atomic.Int32
	remaining atomic.Int32
}

func (h *settlementEvalFaultHook) arm(mode evalFaultMode, failures int) {
	h.remaining.Store(int32(failures))
	h.mode.Store(int32(mode))
}

func (h *settlementEvalFaultHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *settlementEvalFaultHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		name := strings.ToLower(cmd.Name())
		if name != "eval" && name != "evalsha" {
			return next(ctx, cmd)
		}

		mode := evalFaultMode(h.mode.Load())
		if mode == evalFaultBeforeExecution && h.consume(mode) {
			return errSimulatedRedisUnavailable
		}
		err := next(ctx, cmd)
		if err == nil && mode == evalFaultAfterExecution && h.consume(mode) {
			return errSimulatedRedisResponseLost
		}
		return err
	}
}

func (h *settlementEvalFaultHook) consume(mode evalFaultMode) bool {
	for h.mode.Load() == int32(mode) {
		remaining := h.remaining.Load()
		if remaining <= 0 {
			return false
		}
		if !h.remaining.CompareAndSwap(remaining, remaining-1) {
			continue
		}
		if remaining == 1 {
			h.mode.CompareAndSwap(int32(mode), int32(evalFaultDisabled))
		}
		return true
	}
	return false
}

func (h *settlementEvalFaultHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
