package gatewaylite

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

type asyncReservationEnvelopeCapture struct{ payload []byte }

func (c *asyncReservationEnvelopeCapture) Match(value driver.Value) bool {
	switch v := value.(type) {
	case []byte:
		c.payload = append([]byte(nil), v...)
	case string:
		c.payload = []byte(v)
	default:
		return false
	}
	var envelope usageRetryOutboxEnvelope
	return json.Unmarshal(c.payload, &envelope) == nil && envelope.CommitAsyncReservation &&
		envelope.AsyncReservationMicroCents == 6000000000 && !envelope.Event.CommitAsyncReservation && envelope.Event.AsyncReservationMicroCents == 0
}

func TestDurableAsyncReservationPreservesIntentAndReplaysOnce(t *testing.T) {
	h := newAsyncReservationHarness(t, BillingSettlementModeIdempotentV2, false)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, h.reporter)
	fault := &settlementEvalFaultHook{}
	h.client.AddHook(fault)
	capture := &asyncReservationEnvelopeCapture{}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_usage_retry_outbox")).
		WithArgs(h.event.RequestID, string(usageRetryOperationCorrection), capture, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	fault.arm(evalFaultBeforeExecution, usageSettlementRedisAttempts)
	require.NoError(t, reporter.ReportUsage(h.ctx, h.event), "v2失败时持久封套接管完整提交意图")
	require.Equal(t, "reserved", h.client.HGet(h.ctx, h.requestKey, "status").Val())
	item := usageRetryOutboxItem{requestID: h.event.RequestID, operationKind: usageRetryOperationCorrection, payload: capture.payload, generation: 1, claimToken: "owner"}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(item.requestID, string(item.operationKind), item.generation, item.claimToken).WillReturnError(errors.New("delete acknowledgement lost"))
	require.ErrorContains(t, reporter.retryItem(h.ctx, item), "delete acknowledgement lost")
	h.assertSettled(t, 6000, 1)
	before := h.snapshot(t)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_usage_retry_outbox")).
		WithArgs(item.requestID, string(item.operationKind), item.generation, item.claimToken).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, reporter.retryItem(h.ctx, item))
	require.Equal(t, before, h.snapshot(t), "删除outbox失败后重放不重复推进零钱、扣费或入队")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableAsyncReservationLegacyFailureDoesNotUseV2Outbox(t *testing.T) {
	h := newAsyncReservationHarness(t, BillingSettlementModeLegacy, false)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, h.reporter)
	fault := &settlementEvalFaultHook{}
	h.client.AddHook(fault)
	before := h.snapshot(t)
	fault.arm(evalFaultBeforeExecution, usageSettlementRedisAttempts)
	require.ErrorIs(t, reporter.ReportUsage(h.ctx, h.event), errSimulatedRedisUnavailable)
	require.Equal(t, before, h.snapshot(t))
	require.NoError(t, mock.ExpectationsWereMet(), "不能把legacy偷偷切换为v2持久失败队列")
	require.NoError(t, reporter.ReportUsage(h.ctx, h.event))
	h.assertSettled(t, 6000, 1)
}

func TestDurableAsyncReservationOutboxCollision(t *testing.T) {
	for _, scenario := range []string{
		"identical", "telemetry_only", "raw", "request", "key", "user", "organization", "subject",
		"lease", "region", "gateway", "mode", "old_correction", "prefix", "disappeared", "corrupt",
	} {
		t.Run(scenario, func(t *testing.T) {
			h := newAsyncReservationHarness(t, BillingSettlementModeIdempotentV2, false)
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			reporter := NewDurableUsageReporter(db, h.reporter)
			fault := &settlementEvalFaultHook{}
			h.client.AddHook(fault)
			capture := &asyncReservationEnvelopeCapture{}
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_usage_retry_outbox")).
				WithArgs(h.event.RequestID, string(usageRetryOperationCorrection), capture, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(1, 1))
			fault.arm(evalFaultBeforeExecution, usageSettlementRedisAttempts)
			require.NoError(t, reporter.ReportUsage(h.ctx, h.event))
			var stored usageRetryOutboxEnvelope
			require.NoError(t, json.Unmarshal(capture.payload, &stored))
			switch scenario {
			case "telemetry_only":
				stored.Event.LatencyMillis, stored.Event.EndedAtMillis = 99, 123456789
			case "raw":
				stored.AsyncReservationMicroCents++
				stored.SettlementMicroCents++
			case "request":
				stored.Event.RequestID, stored.QuotaRequestID = "other-request", "other-request"
			case "key":
				stored.Event.KeyID = "other-key"
			case "user":
				stored.Event.UserID, stored.Event.BillingSubjectID = 34, 34
			case "organization":
				stored.Event.OrganizationID, stored.Event.BillingSubjectID = 91, 91
				stored.Event.BillingSubjectType = "organization"
			case "subject":
				stored.Event.BillingSubjectID = 34
			case "lease":
				stored.Event.LeaseID = "replacement"
			case "region":
				stored.Event.Region = "other-region"
			case "gateway":
				stored.Event.GatewayID = "other-gateway"
			case "mode":
				stored.Event.SettlementMode = string(BillingSettlementModeLegacy)
			case "old_correction":
				stored.CommitAsyncReservation, stored.AsyncReservationMicroCents = false, 0
			case "prefix":
				stored.RedisPrefix = "other-prefix"
			}
			payload, err := json.Marshal(stored)
			require.NoError(t, err)
			if scenario == "corrupt" {
				payload = []byte("not-json")
			}
			// 只允许 INSERT DO NOTHING 后读取原封套；没有 UPDATE/覆盖期望。
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_usage_retry_outbox")).
				WithArgs(h.event.RequestID, string(usageRetryOperationCorrection), &asyncReservationEnvelopeCapture{}, sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 0))
			rows := sqlmock.NewRows([]string{"event_payload"})
			if scenario != "disappeared" {
				rows.AddRow(payload)
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT event_payload FROM gateway_lite_usage_retry_outbox")).
				WithArgs(h.event.RequestID, string(usageRetryOperationCorrection)).WillReturnRows(rows)
			before := h.snapshot(t)
			fault.arm(evalFaultBeforeExecution, usageSettlementRedisAttempts)
			err = reporter.ReportUsage(h.ctx, h.event)
			if scenario == "identical" || scenario == "telemetry_only" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "SETTLEMENT_CONFLICT")
			}
			require.Equal(t, before, h.snapshot(t))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDurableAsyncReservationConflictDoesNotBecomeSuccess(t *testing.T) {
	h := newAsyncReservationHarness(t, BillingSettlementModeIdempotentV2, false)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableUsageReporter(db, h.reporter)
	require.NoError(t, h.client.Del(h.ctx, h.requestKey).Err())
	before := h.snapshot(t)
	require.ErrorContains(t, reporter.ReportUsage(h.ctx, h.event), "SETTLEMENT_CONFLICT")
	require.Equal(t, before, h.snapshot(t))
	require.NoError(t, mock.ExpectationsWereMet(), "永久身份错误不能经outbox存储后被当作已可靠结算")
}
