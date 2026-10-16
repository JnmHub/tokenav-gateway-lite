package gatewaylite

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAsyncReservationUsagePreflightChangesNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *asyncReservationHarness)
	}{
		{"missing_request", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.Del(h.ctx, h.requestKey).Err())
		}},
		{"request_wrong_type", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.Del(h.ctx, h.requestKey).Err())
			require.NoError(t, h.client.Set(h.ctx, h.requestKey, "wrong", 0).Err())
		}},
		{"large_identity_not_rounded", func(t *testing.T, h *asyncReservationHarness) {
			h.event.UserID, h.event.BillingSubjectID = 9007199254740993, 9007199254740993
			// 让原租约键确实存在，仅员工字符串差 1；double 会把两者混同。
			leaseKey := h.quota.leaseKeyForSubject(LeaseSubject{UserID: h.event.UserID}, h.event.Region)
			require.NoError(t, h.client.Rename(h.ctx, h.leaseKey, leaseKey).Err())
			h.leaseKey = leaseKey
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "lease_key", leaseKey,
				"user_id", "9007199254740992", "billing_subject_id", "9007199254740993").Err())
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey,
				"user_id", "9007199254740992", "billing_subject_id", "9007199254740993").Err())
		}},
		{"missing_lease", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.Del(h.ctx, h.leaseKey).Err())
		}},
		{"replacement_lease", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "lease_id", "replacement").Err())
		}},
		{"key_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "key_id", "other-key").Err())
		}},
		{"user_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "user_id", "34").Err())
		}},
		{"organization_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "organization_id", "91").Err())
		}},
		{"subject_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "billing_subject_id", "34").Err())
		}},
		{"lease_subject_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "billing_subject_type", "organization").Err())
		}},
		{"region_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "region", "other-gw").Err())
		}},
		{"lease_pointer_mismatch", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "lease_key", h.leaseKey+":other").Err())
		}},
		{"refunded", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "status", "refunded").Err())
		}},
		{"stream_wrong_type", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.Set(h.ctx, h.reporter.streamKey(), "wrong", 0).Err())
		}},
		{"stream_id_exhausted", func(t *testing.T, h *asyncReservationHarness) {
			_, err := h.client.XAdd(h.ctx, &redis.XAddArgs{Stream: h.reporter.streamKey(),
				ID: "18446744073709551615-18446744073709551615", Values: map[string]any{"sentinel": "keep"}}).Result()
			require.NoError(t, err)
		}},
		{"empty_stream_id_exhausted", func(t *testing.T, h *asyncReservationHarness) {
			id, err := h.client.XAdd(h.ctx, &redis.XAddArgs{Stream: h.reporter.streamKey(),
				ID: "18446744073709551615-18446744073709551615", Values: map[string]any{"sentinel": "keep"}}).Result()
			require.NoError(t, err)
			require.NoError(t, h.client.XDel(h.ctx, h.reporter.streamKey(), id).Err())
		}},
		{"lease_wrong_type", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.Del(h.ctx, h.leaseKey).Err())
			require.NoError(t, h.client.Set(h.ctx, h.leaseKey, "wrong", 0).Err())
		}},
		{"remainder_wrong_type", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.remainderKey, "wrong", "type").Err())
		}},
		{"bad_numeric_counter", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "spent_cents", "nan").Err())
		}},
		{"negative_counter", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "reserved_cents", "-1").Err())
		}},
		{"hold_missing", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "reserved_cents", "1").Err())
		}},
		{"capacity_short", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "allocated_cents", "5999").Err())
		}},
		{"unsafe_counter", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.leaseKey, "allocated_cents", "9007199254740992").Err())
		}},
		{"bad_remainder", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.Set(h.ctx, h.remainderKey, "1000000", 0).Err())
		}},
		{"historical_no_raw", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "corrected_actual_cents", "6000", "usage_report_enqueued", "1").Err())
		}},
		{"funded_request", func(t *testing.T, h *asyncReservationHarness) {
			require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "funding_authorization_id", "card").Err())
		}},
	}
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		for _, tc := range cases {
			t.Run(string(mode)+"_"+tc.name, func(t *testing.T) {
				h := newAsyncReservationHarness(t, mode, false)
				tc.mutate(t, h)
				before := h.snapshot(t)
				require.ErrorContains(t, h.reporter.ReportUsage(h.ctx, h.event), "SETTLEMENT_CONFLICT")
				require.Equal(t, before, h.snapshot(t), "所有业务拒绝必须发生在零钱、账务、队列写入之前")
			})
		}
	}
}

func TestAsyncReservationUsageStreamIDBoundary(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			h := newAsyncReservationHarness(t, mode, false)
			_, err := h.client.XAdd(h.ctx, &redis.XAddArgs{Stream: h.reporter.streamKey(),
				ID: "18446744073709551615-18446744073709551614", Values: map[string]any{"sentinel": "keep"}}).Result()
			require.NoError(t, err)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event), "尚余一个编号时必须正常提交")
			h.assertSettled(t, 6000, 2)
			info, err := h.client.XInfoStream(h.ctx, h.reporter.streamKey()).Result()
			require.NoError(t, err)
			require.Equal(t, "18446744073709551615-18446744073709551615", info.LastGeneratedID)
			before := h.snapshot(t)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event), "原事件已入队的精确重放不需要新编号")
			require.Equal(t, before, h.snapshot(t))
		})
	}
}

func TestAsyncReservationUsageV2IntermediateStates(t *testing.T) {
	for _, state := range []string{"frozen_only", "correction_enqueued_reserved", "correction_enqueued_exhausted_stream", "frozen_committed", "scope_conflict", "raw_conflict", "mode_conflict"} {
		t.Run(state, func(t *testing.T) {
			h := newAsyncReservationHarness(t, BillingSettlementModeIdempotentV2, false)
			require.NoError(t, h.client.Set(h.ctx, h.remainderKey, "250000", time.Hour).Err())
			actual, err := h.quota.AccumulateActualCostMicroCentsIdempotent(h.ctx, actualCostAccumulatorScopeForUsageEvent(h.event), h.event.RequestID, h.event.AsyncReservationMicroCents, actualCostMicroCentsScale)
			require.NoError(t, err)
			require.EqualValues(t, 6000, actual)
			var wantConflict bool
			switch state {
			case "correction_enqueued_reserved", "correction_enqueued_exhausted_stream":
				ordinary := h.event
				ordinary.CommitAsyncReservation, ordinary.AsyncReservationMicroCents = false, 0
				require.NoError(t, h.reporter.ReportUsage(h.ctx, ordinary))
				require.Equal(t, "reserved", h.client.HGet(h.ctx, h.requestKey, "status").Val())
				if state == "correction_enqueued_exhausted_stream" {
					id, err := h.client.XAdd(h.ctx, &redis.XAddArgs{Stream: h.reporter.streamKey(),
						ID: "18446744073709551615-18446744073709551615", Values: map[string]any{"sentinel": "remove"}}).Result()
					require.NoError(t, err)
					require.NoError(t, h.client.XDel(h.ctx, h.reporter.streamKey(), id).Err())
				}
			case "frozen_committed":
				require.NoError(t, h.quota.Commit(h.ctx, UsageCommit{RequestID: h.event.RequestID, ActualCents: actual}))
			case "scope_conflict":
				require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "settlement_scope", "other").Err())
				wantConflict = true
			case "raw_conflict":
				require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "settlement_micro_cents", "6001000000").Err())
				wantConflict = true
			case "mode_conflict":
				require.NoError(t, h.client.HSet(h.ctx, h.requestKey, "settlement_mode", "legacy").Err())
				wantConflict = true
			}
			before := h.snapshot(t)
			if wantConflict {
				require.ErrorContains(t, h.reporter.ReportUsage(h.ctx, h.event), "SETTLEMENT_CONFLICT")
				require.Equal(t, before, h.snapshot(t))
				return
			}
			require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
			h.assertSettled(t, 6000, 1)
			require.Equal(t, "250000", h.client.Get(h.ctx, h.remainderKey).Val(), "已冻结金额不得再次推进余数")
			settled := h.snapshot(t)
			require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
			require.Equal(t, settled, h.snapshot(t))
		})
	}
}

func TestAsyncReservationUsagePreservesOrdinaryCorrection(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			h := newAsyncReservationHarness(t, mode, false)
			ordinary := h.event
			ordinary.CommitAsyncReservation, ordinary.AsyncReservationMicroCents = false, 0
			if !ordinary.SettlementPending {
				ordinary.ActualCents = 6000
			}
			require.NoError(t, h.reporter.ReportUsage(h.ctx, ordinary))
			require.Equal(t, "reserved", h.client.HGet(h.ctx, h.requestKey, "status").Val())
			lease, _, err := h.quota.LoadLease(h.ctx, h.event.UserID, h.event.Region)
			require.NoError(t, err)
			require.EqualValues(t, 6000, lease.ReservedCents)
			require.Zero(t, lease.SpentCents)
			require.Empty(t, h.client.HGet(h.ctx, h.requestKey, "async_reservation_committed").Val())
		})
	}
}

func TestAsyncReservationUsagePreExecutionFailure(t *testing.T) {
	for _, mode := range []BillingSettlementMode{BillingSettlementModeLegacy, BillingSettlementModeIdempotentV2} {
		t.Run(string(mode), func(t *testing.T) {
			h := newAsyncReservationHarness(t, mode, false)
			fault := &settlementEvalFaultHook{}
			h.client.AddHook(fault)
			before := h.snapshot(t)
			fault.arm(evalFaultBeforeExecution, usageSettlementRedisAttempts)
			require.ErrorIs(t, h.reporter.ReportUsage(h.ctx, h.event), errSimulatedRedisUnavailable)
			require.Equal(t, before, h.snapshot(t))
			require.NoError(t, h.reporter.ReportUsage(h.ctx, h.event))
			h.assertSettled(t, 6000, 1)
		})
	}
}
