package service

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Codex tail-burst（超额）会话追踪。
//
// 进入尾部放行时在账号 Extra 上标记活跃会话（codex_tail_burst_active 等），
// 采集器每轮刷新超额增量（delta_percent），退出时（进入时所在窗口重置 /
// 上游权威配额拒绝冻结）结算会话期间的请求数与 tokens，写入"上次超额"小结。
// 全部走 Extra 键值持久化，多实例幂等；前端只以 active 布尔为准，
// 结算后的旧键残留不影响展示。

const (
	codexTailBurstExtraActive       = "codex_tail_burst_active"
	codexTailBurstExtraSessionID    = "codex_tail_burst_session_id"
	codexTailBurstExtraSince        = "codex_tail_burst_since"
	codexTailBurstExtraWindow       = "codex_tail_burst_window"
	codexTailBurstExtraReason       = "codex_tail_burst_reason"
	codexTailBurstExtraEntryPercent = "codex_tail_burst_entry_percent"
	codexTailBurstExtraEntryResetAt = "codex_tail_burst_entry_reset_at"
	codexTailBurstExtraDeltaPercent = "codex_tail_burst_delta_percent"

	codexTailBurstExtraLastUntil    = "codex_tail_burst_last_until"
	codexTailBurstExtraLastDelta    = "codex_tail_burst_last_delta_percent"
	codexTailBurstExtraLastRequests = "codex_tail_burst_last_requests"
	codexTailBurstExtraLastTokens   = "codex_tail_burst_last_tokens"
	codexTailBurstExtraLastWindow   = "codex_tail_burst_last_window"
)

// codexTailBurstActive 报告账号当前是否处于超额会话中。
func codexTailBurstActive(extra map[string]any) bool {
	return resolveAccountExtraBool(extra, codexTailBurstExtraActive)
}

// codexTailBurstActiveWindow 返回活跃会话绑定的窗口（5h/7d；订阅到期场景为空串）。
func codexTailBurstActiveWindow(extra map[string]any) string {
	if len(extra) == 0 {
		return ""
	}
	window, _ := extra[codexTailBurstExtraWindow].(string)
	return window
}

func codexTailBurstActiveReason(extra map[string]any) string {
	if len(extra) == 0 {
		return ""
	}
	reason, _ := extra[codexTailBurstExtraReason].(string)
	return reason
}

// codexTailBurstEntryPercent 返回进入超额时的窗口用量百分比（0 = 未知）。
func codexTailBurstEntryPercent(extra map[string]any) float64 {
	if len(extra) == 0 {
		return 0
	}
	if value, ok := resolveAccountExtraNumber(extra, codexTailBurstExtraEntryPercent); ok {
		return value
	}
	return 0
}

// codexTailBurstEntryUpdates 构造超额会话开始标记的 Extra 更新。
// since / entry_reset_at 统一用 UTC RFC3339，与既有 codex_* 键时间格式一致。
// entry_reset_at 是进入时所在窗口的重置时刻：会话结束判定以它为准，
// 窗口滚过后旧标记自然失效（新窗口的快照 reset_at 指向未来，无法区分）。
func codexTailBurstEntryUpdates(window, reason string, entryPercent float64, entryResetAt *time.Time, now time.Time) map[string]any {
	updates := map[string]any{
		codexTailBurstExtraActive:       true,
		codexTailBurstExtraSessionID:    uuid.NewString(),
		codexTailBurstExtraSince:        now.UTC().Format(time.RFC3339),
		codexTailBurstExtraWindow:       window,
		codexTailBurstExtraReason:       reason,
		codexTailBurstExtraEntryPercent: entryPercent,
	}
	if entryResetAt != nil && !entryResetAt.IsZero() {
		updates[codexTailBurstExtraEntryResetAt] = entryResetAt.UTC().Format(time.RFC3339)
	}
	return updates
}

// startCodexTailBurstSessionIfEligible enters a Tail-Burst session after an
// independent quota evaluation.  Tail-Burst is deliberately decoupled from
// the normal auto-pause threshold: the latter is an account scheduling guard,
// while this marker is the accounting boundary for requests accepted in the
// remaining quota tail.  The durable marker is written before a request is
// allowed through so a later settlement can account for the whole session.
//
// The returned updates are the marker fields newly written by this call.  A
// caller that already has a merged, newer snapshot can overlay them locally;
// an existing active marker returns true with no updates.
func startCodexTailBurstSessionIfEligible(
	ctx context.Context,
	accountRepo AccountRepository,
	usageRepo UsageLogRepository,
	account *Account,
	extra map[string]any,
	tail CodexTailBurstState,
	now time.Time,
) (bool, map[string]any) {
	if account == nil || accountRepo == nil || !tail.Active ||
		!account.IsOpenAIOAuth() || account.IsShadow() || !account.IsActive() ||
		strings.TrimSpace(account.GetChatGPTAccountID()) == "" ||
		codexTailBurstAccountDisabled(extra) {
		return false, nil
	}
	if codexTailBurstActive(extra) {
		return true, nil
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	entryPercent := readOpenAIQuotaUsedPercent(extra, tail.Window)
	var entryResetAt *time.Time
	if resetAt, ok := codexTailBurstWindowResetAt(extra, tail.Window, now); ok {
		entryResetAt = &resetAt
	}
	updates := codexTailBurstEntryUpdates(tail.Window, tail.Reason, entryPercent, entryResetAt, now)
	marked := false
	if casRepo, ok := accountRepo.(AccountCodexTailBurstCASRepository); ok {
		applied, err := casRepo.MarkCodexTailBurstStartedIfInactive(ctx, account.ID, updates)
		if err != nil {
			slog.Warn("codex_tail_burst_entry_mark_failed", "account_id", account.ID, "error", err)
			// The durable marker is the accounting boundary.  Failing closed here
			// prevents an untracked request from bypassing the quota guard.
			return false, nil
		}
		if !applied {
			// A concurrent request may have won the entry race. Re-read the row
			// before rejecting this request: if the marker is now active, this
			// request belongs to the same durable session and may proceed. Without
			// this read-back every loser of a legitimate CAS race is incorrectly
			// rejected (and can make a healthy tail appear unavailable under load).
			latest, readErr := accountRepo.GetByID(ctx, account.ID)
			if readErr != nil || latest == nil || !codexTailBurstActive(latest.Extra) ||
				!latest.IsOpenAIOAuth() || latest.IsShadow() || !latest.IsActive() ||
				strings.TrimSpace(latest.GetChatGPTAccountID()) == "" ||
				codexTailBurstAccountDisabled(latest.Extra) {
				slog.Debug("codex_tail_burst_entry_mark_skipped",
					"account_id", account.ID,
					"reason", "already_active_or_unavailable",
					"read_error", readErr,
				)
				return false, nil
			}
			account.Extra = mergeExtraMap(account.Extra, latest.Extra)
			slog.Debug("codex_tail_burst_entry_mark_reused",
				"account_id", account.ID,
				"session_id", latest.Extra[codexTailBurstExtraSessionID],
			)
			return true, nil
		}
		marked = true
	} else if err := accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("codex_tail_burst_entry_mark_failed", "account_id", account.ID, "error", err)
		// Older repository implementations still need a durable marker before
		// the request can be admitted; otherwise settlement cannot be retried.
		return false, nil
	} else {
		marked = true
	}
	if !marked {
		return false, nil
	}

	account.Extra = mergeExtraMap(account.Extra, updates)
	// History is observability only; a bounded write failure must not reject the
	// request after the accounting marker has been durably committed.
	recordCodexTailBurstHistoryStart(ctx, usageRepo, account, account.Extra)
	slog.Info("codex_tail_burst_session_started",
		"account_id", account.ID,
		"window", tail.Window,
		"reason", tail.Reason,
		"entry_percent", entryPercent)
	return true, updates
}

// codexTailBurstSessionMarkerFromExtra extracts only the fields that identify
// an active session.  Settlement uses this marker for a compare-and-set
// update, so a collector holding an old account snapshot cannot clear a newer
// session started by another request.
func codexTailBurstSessionMarkerFromExtra(extra map[string]any) CodexTailBurstSessionMarker {
	marker := CodexTailBurstSessionMarker{}
	if len(extra) == 0 {
		return marker
	}
	if value, ok := extra[codexTailBurstExtraSessionID].(string); ok {
		marker.SessionID = strings.TrimSpace(value)
	}
	if value, ok := extra[codexTailBurstExtraSince].(string); ok {
		marker.Since = value
	}
	if value, ok := extra[codexTailBurstExtraWindow].(string); ok {
		marker.Window = value
	}
	if value, ok := extra[codexTailBurstExtraReason].(string); ok {
		marker.Reason = value
	}
	if value, ok := resolveAccountExtraNumber(extra, codexTailBurstExtraEntryPercent); ok {
		marker.EntryPercent = &value
	}
	if value, ok := extra[codexTailBurstExtraEntryResetAt].(string); ok && strings.TrimSpace(value) != "" {
		marker.EntryResetAt = &value
	}
	return marker
}

// codexTailBurstDeltaUpdates 计算当前用量相对进入时的超额增量（百分点）。
// 仅在会话活跃、窗口已知且当前用量有效时返回更新；增量最小为 0（窗口内
// 用量单调，负值只可能是快照抖动，展示时以 0 兜底更直观）。
func codexTailBurstDeltaUpdates(extra map[string]any, currentPercent float64) (map[string]any, bool) {
	if !codexTailBurstActive(extra) {
		return nil, false
	}
	if codexTailBurstActiveWindow(extra) == "" || currentPercent <= 0 {
		return nil, false
	}
	entry := codexTailBurstEntryPercent(extra)
	if entry <= 0 {
		// 进入时无用量数据（订阅到期触发），以当前用量为基准。
		entry = currentPercent
	}
	delta := roundCodexTailBurstPercent(currentPercent - entry)
	if delta < 0 {
		delta = 0
	}
	return map[string]any{codexTailBurstExtraDeltaPercent: delta}, true
}

func roundCodexTailBurstPercent(value float64) float64 {
	return math.Round(value*10) / 10
}

// settleCodexTailBurstSession 结算一段超额会话：查询 [since, now] 的
// 请求数与 tokens，写入"上次超额"小结并清除活跃标记。幂等：未活跃时直接
// 返回 false；结算失败（DB 写失败）时活跃标记保留，下次调用重试。
func settleCodexTailBurstSession(ctx context.Context, accountRepo AccountRepository, usageRepo UsageLogRepository, account *Account, now time.Time) (bool, error) {
	if account == nil {
		return false, nil
	}
	return settleCodexTailBurstSessionWithExtra(ctx, accountRepo, usageRepo, account, account.Extra, now)
}

// settleCodexTailBurstSessionWithExtra 使用本轮刚合并的权威快照结算会话。
// 采集器先查询用量再判断窗口是否滚动；如果继续读取查询前的 account.Extra，
// 会话增量会丢失，尤其是在 reset_at 恰好跨窗的边界时。
func settleCodexTailBurstSessionWithExtra(ctx context.Context, accountRepo AccountRepository, usageRepo UsageLogRepository, account *Account, extra map[string]any, now time.Time) (bool, error) {
	if account == nil || accountRepo == nil || usageRepo == nil {
		return false, nil
	}
	if !codexTailBurstActive(extra) {
		return false, nil
	}

	var since time.Time
	if raw, ok := extra[codexTailBurstExtraSince].(string); ok {
		if parsed, err := parseTime(raw); err == nil {
			since = parsed
		}
	}
	window := codexTailBurstActiveWindow(extra)
	delta := 0.0
	if window != "" {
		if entry := codexTailBurstEntryPercent(extra); entry > 0 {
			if current := readOpenAIQuotaUsedPercent(extra, window); current > 0 {
				delta = roundCodexTailBurstPercent(current - entry)
				if delta < 0 {
					delta = 0
				}
			}
		}
	}

	requests, tokens := int64(0), int64(0)
	if !since.IsZero() {
		stats, err := usageRepo.GetAccountStatsAggregated(ctx, account.ID, since, now)
		if err != nil {
			slog.Warn("codex_tail_burst_settle_stats_failed",
				"account_id", account.ID, "since", since.UTC(), "error", err)
			// Keep the active marker when the usage query fails. Clearing it here
			// would lose the session accounting and prevent a later collector pass
			// from retrying settlement.
			return false, err
		} else if stats != nil {
			requests, tokens = stats.TotalRequests, stats.TotalTokens
		}
	}

	updates := map[string]any{
		codexTailBurstExtraActive:       false,
		codexTailBurstExtraLastUntil:    now.UTC().Format(time.RFC3339),
		codexTailBurstExtraLastDelta:    delta,
		codexTailBurstExtraLastRequests: requests,
		codexTailBurstExtraLastTokens:   tokens,
		codexTailBurstExtraLastWindow:   window,
	}
	marker := codexTailBurstSessionMarkerFromExtra(extra)
	settled := false
	if casRepo, ok := accountRepo.(AccountCodexTailBurstCASRepository); ok {
		applied, err := casRepo.SettleCodexTailBurstIfMarkerMatches(ctx, account.ID, marker, updates)
		if err != nil {
			slog.Warn("codex_tail_burst_settle_failed", "account_id", account.ID, "error", err)
			return false, err
		}
		if !applied {
			// Another request/collector already settled this marker, or started a
			// newer session.  Do not touch local state or overwrite its summary.
			return false, nil
		}
		settled = true
	} else if err := accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("codex_tail_burst_settle_failed", "account_id", account.ID, "error", err)
		return false, err
	} else {
		settled = true
	}
	if !settled {
		return false, nil
	}
	// Keep the request-local account coherent with the durable transition. This
	// prevents a second error handler in the same request from observing the old
	// active marker and querying/settling the same session again.
	account.Extra = mergeExtraMap(extra, updates)
	// Repair a lost start write only after this marker won the settlement CAS.
	// Writing before the CAS could leave an old session as an orphaned active row
	// when a newer session has already replaced the account marker.
	recordCodexTailBurstHistoryStart(ctx, usageRepo, account, extra)
	recordCodexTailBurstHistorySettlement(ctx, usageRepo, account, extra, now, delta, requests, tokens)
	slog.Info("codex_tail_burst_session_settled",
		"account_id", account.ID,
		"window", window,
		"delta_percent", delta,
		"requests", requests,
		"tokens", tokens,
		"since", since.UTC(),
		"until", now.UTC())
	return true, nil
}
