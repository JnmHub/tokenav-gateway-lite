package repository

import "testing"

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_CompactCapabilityKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_compact_supported":  true,
		"openai_compact_checked_at": "2026-04-10T10:00:00Z",
	}

	if !shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected compact capability updates to enqueue scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_OpenAIResponsesCapabilityKeysAreRelevant(t *testing.T) {
	updates := map[string]any{
		"openai_responses_mode":      "force_chat_completions",
		"openai_responses_supported": false,
	}

	if !shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("expected responses capability updates to enqueue scheduler outbox")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_GrokBillingSnapshotIsNeutral(t *testing.T) {
	updates := map[string]any{
		"grok_billing_snapshot": map[string]any{
			"usage_percent": 12.5,
			"updated_at":    "2026-07-15T00:00:00Z",
		},
	}

	if shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("billing-only Grok snapshots must not rebuild scheduler buckets")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_TailBurstObservationsAreNeutral(t *testing.T) {
	updates := map[string]any{
		"codex_tail_burst_active":             true,
		"codex_tail_burst_session_id":         "session-1",
		"codex_tail_burst_since":              "2026-08-30T01:00:00Z",
		"codex_tail_burst_window":             "5h",
		"codex_tail_burst_reason":             "used_ratio",
		"codex_tail_burst_entry_percent":      98.5,
		"codex_tail_burst_entry_reset_at":     "2026-08-30T02:00:00Z",
		"codex_tail_burst_delta_percent":      1.2,
		"codex_tail_burst_last_until":         "2026-08-30T03:00:00Z",
		"codex_tail_burst_last_delta_percent": 1.2,
		"codex_tail_burst_last_requests":      int64(3),
		"codex_tail_burst_last_tokens":        int64(40),
		"codex_tail_burst_last_window":        "5h",
	}

	if shouldEnqueueSchedulerOutboxForExtraUpdates(updates) {
		t.Fatalf("tail-burst observation updates must not rebuild scheduler buckets")
	}
}

func TestShouldEnqueueSchedulerOutboxForExtraUpdates_TailBurstDisableRemainsRelevant(t *testing.T) {
	if !shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_tail_burst_disabled": true,
	}) {
		t.Fatalf("account-level tail-burst disable must refresh scheduling policy")
	}
}
