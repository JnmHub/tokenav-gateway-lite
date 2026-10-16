package gatewaylite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// AtomicAsyncReservationReporter is deliberately separate from ordinary usage
// reporting: a remote Seedance completion cannot succeed through a sink-only or
// local rounding fallback while its original Redis hold remains open.
type AtomicAsyncReservationReporter interface {
	SupportsAtomicAsyncReservation() bool
}

const asyncReservationMaxExactInteger int64 = 1<<53 - 1

func (r *RedisUsageReporter) SupportsAtomicAsyncReservation() bool {
	return r.Enabled() && r.sink != nil
}

func (r *DurableUsageReporter) SupportsAtomicAsyncReservation() bool {
	if r == nil {
		return false
	}
	inner, ok := r.inner.(AtomicAsyncReservationReporter)
	return ok && inner.SupportsAtomicAsyncReservation()
}

func (r *DurableUsageReporter) verifyAsyncReservationOutbox(ctx context.Context, operation usageRetryOperationKind, expected usageRetryOutboxEnvelope) error {
	var payload []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT event_payload FROM gateway_lite_usage_retry_outbox
		WHERE request_id = $1 AND operation_kind = $2
	`, expected.Event.RequestID, string(operation)).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		// Worker 可能刚完成并删除该行；本次缺乏可靠交接证据，留待下一次
		// 原请求重放确认，不能把查不到封套当作本次已持久化成功。
		return errors.New("SETTLEMENT_CONFLICT async reservation outbox evidence disappeared")
	}
	if err != nil {
		return err
	}
	var stored usageRetryOutboxEnvelope
	if err := json.Unmarshal(payload, &stored); err != nil {
		return fmt.Errorf("SETTLEMENT_CONFLICT invalid async reservation outbox envelope: %w", err)
	}
	if !stored.CommitAsyncReservation {
		return errors.New("SETTLEMENT_CONFLICT existing outbox does not commit the original reservation")
	}
	// 对比完整持久封套，只排除不影响财务事实的计时遥测。这样重试耗时变化
	// 不制造冲突，原身份、金额、模式、租约、价格与用量审计仍必须一致。
	canonical := func(envelope usageRetryOutboxEnvelope) ([]byte, error) {
		event := &envelope.Event
		event.LatencyMillis, event.FirstTokenMillis = 0, 0
		event.AuthLatencyMillis, event.QuotaLatencyMillis, event.RoutingLatencyMillis = 0, 0, 0
		event.RoutingBreakdownMillis = nil
		event.UpstreamLatencyMillis, event.ResponseLatencyMillis, event.ReportLatencyMillis = 0, 0, 0
		event.StartedAtMillis, event.EndedAtMillis = 0, 0
		return json.Marshal(envelope)
	}
	actual, err := canonical(stored)
	if err != nil {
		return err
	}
	wanted, err := canonical(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, wanted) {
		return errors.New("SETTLEMENT_CONFLICT async reservation outbox contains different financial facts")
	}
	return nil
}

// AsyncReservationCostMicroCents only converts units. It must not advance the
// shared fractional-cent carry before the original reservation is validated.
func AsyncReservationCostMicroCents(cost float64) (int64, error) {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return 0, errors.New("SETTLEMENT_CONFLICT invalid async reservation cost")
	}
	micro := ActualCostMicroCents(cost)
	if micro > asyncReservationMaxExactInteger-actualCostMicroCentsScale {
		return 0, errors.New("SETTLEMENT_CONFLICT async reservation cost exceeds exact range")
	}
	return micro, nil
}

func validateAsyncReservationEvent(event UsageEvent) error {
	conflict := func() error {
		return errors.New("SETTLEMENT_CONFLICT invalid original async reservation identity or amount")
	}
	if !event.CommitAsyncReservation || event.RequestID == "" || strings.TrimSpace(event.RequestID) != event.RequestID ||
		event.QuotaRequestID != event.RequestID || event.KeyID == "" || strings.TrimSpace(event.KeyID) != event.KeyID ||
		event.UserID <= 0 || event.OrganizationID < 0 || event.LeaseID == "" || strings.TrimSpace(event.LeaseID) != event.LeaseID ||
		event.Region == "" || strings.TrimSpace(event.Region) != event.Region || event.GatewayID == "" ||
		event.FundingAuthorizationID != "" || event.FundingSnapshot != nil || event.OAuthInvocationReservation != nil ||
		event.Final || event.SuppressBaseCharge || event.ActualCents != 0 || event.AsyncReservationMicroCents < 0 ||
		event.AsyncReservationMicroCents > asyncReservationMaxExactInteger-actualCostMicroCentsScale {
		return conflict()
	}
	if event.OrganizationID == 0 {
		if event.BillingSubjectType != "user" || event.BillingSubjectID != event.UserID {
			return conflict()
		}
	} else if event.BillingSubjectType != "organization" || event.BillingSubjectID != event.OrganizationID {
		return conflict()
	}
	switch BillingSettlementMode(event.SettlementMode) {
	case BillingSettlementModeLegacy:
		if event.SettlementPending || event.SettlementMicroCents != 0 {
			return conflict()
		}
	case BillingSettlementModeIdempotentV2:
		if !event.SettlementPending || event.SettlementMicroCents != event.AsyncReservationMicroCents {
			return conflict()
		}
	default:
		return conflict()
	}
	return nil
}

func (r *RedisUsageReporter) reportAsyncReservationUsage(ctx context.Context, event UsageEvent) error {
	if !r.SupportsAtomicAsyncReservation() {
		return ErrRequestQuotaUnavailable
	}
	if err := validateAsyncReservationEvent(event); err != nil {
		return err
	}
	body, err := json.Marshal(queuedUsageEvent{Event: event, EnqueuedAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	quota := NewRedisQuota(r.client, r.prefix())
	keys := []string{
		quota.requestKey(event.RequestID),
		quota.leaseKeyForSubject(LeaseSubject{UserID: event.UserID, OrganizationID: event.OrganizationID,
			BillingSubjectType: event.BillingSubjectType, BillingSubjectID: event.BillingSubjectID}, event.Region),
		r.streamKey(),
		quota.actualCostRemainderKey(actualCostAccumulatorScopeForUsageEvent(event)),
	}
	// IDs stay decimal strings throughout Lua; converting them to double would
	// allow distinct 64-bit users or organizations to compare equal.
	args := []any{body, event.RequestID, event.KeyID, strconv.FormatInt(event.UserID, 10),
		strconv.FormatInt(event.OrganizationID, 10), event.BillingSubjectType, strconv.FormatInt(event.BillingSubjectID, 10),
		event.Region, event.LeaseID, event.SettlementMode, event.AsyncReservationMicroCents,
		actualCostAccumulatorScopeForUsageEvent(event), int((30 * 24 * time.Hour).Seconds())}
	for attempt := 0; ; attempt++ {
		values, runErr := commitAsyncReservationUsageScript.Run(ctx, r.client, keys, args...).Slice()
		err = runErr
		if err == nil {
			r.triggerPostUsageLeaseRefill(quota, parseLeaseRefillSignal(values), event.GatewayID)
			return nil
		}
		if attempt+1 >= usageSettlementRedisAttempts || !isRetryableUsageSettlementRedisError(err) {
			return err
		}
		timer := time.NewTimer(usageSettlementRetryDelay * time.Duration(1<<attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
