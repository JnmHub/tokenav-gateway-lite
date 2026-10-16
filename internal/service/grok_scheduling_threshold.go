package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

const (
	grokSchedulingThresholdReasonSource = "grok_scheduling_threshold"
	grokMaxSchedulingResetHorizon       = 25 * time.Hour
)

type grokSchedulingThresholdDecision struct {
	ShouldPause      bool
	ThresholdPercent int
	UsedPercent      float64
	Until            time.Time
}

type grokSchedulingThresholdReason struct {
	Source           string  `json:"source"`
	Platform         string  `json:"platform"`
	ThresholdPercent int     `json:"threshold_percent"`
	UsedPercent      float64 `json:"used_percent"`
	UntilUnix        int64   `json:"until_unix"`
	TriggeredAtUnix  int64   `json:"triggered_at_unix"`
	ErrorMessage     string  `json:"error_message"`
}

type grokSchedulingPauseState struct {
	untilUnix int64
	reason    string
}

type grokSchedulingThresholdConditionalClearer interface {
	ClearGrokSchedulingThresholdPauseIfMatch(ctx context.Context, accountID int64, until time.Time, reason string) (bool, error)
}

func evaluateGrokSchedulingThreshold(account *Account, threshold int, now time.Time) grokSchedulingThresholdDecision {
	decision := grokSchedulingThresholdDecision{ThresholdPercent: threshold}
	if account == nil || !account.IsGrok() || threshold < 1 || threshold >= 100 {
		return decision
	}
	usedPercent, ok := numberAsFloat64(account.Extra["grok_sched_utilization"])
	if !ok || usedPercent < float64(threshold) {
		return decision
	}
	until, ok := schedulingResetTime(account.Extra["grok_sched_reset_at"])
	if !ok || !until.After(now) {
		return decision
	}
	decision.ShouldPause = true
	decision.UsedPercent = usedPercent
	decision.Until = until
	return decision
}

func buildGrokSchedulerExtraUpdates(snapshot *xai.QuotaSnapshot, now time.Time) map[string]any {
	usedPercent, resetAt, ok := grokSnapshotUtilization(snapshot)
	if !ok {
		return nil
	}
	updates := map[string]any{
		"grok_sched_utilization":      usedPercent,
		"grok_sched_usage_updated_at": now.UTC().Format(time.RFC3339),
		// JSONB 合并中的 nil 会覆盖旧值；新响应没有 reset 时必须清理旧窗口，
		// 否则账号会按已经失效的重置时间继续停调。
		"grok_sched_reset_at": nil,
	}
	if resetAt != nil && resetAt.After(now) {
		capped := *resetAt
		if horizon := now.Add(grokMaxSchedulingResetHorizon); capped.After(horizon) {
			capped = horizon
		}
		updates["grok_sched_reset_at"] = capped.UTC().Format(time.RFC3339)
	}
	return updates
}

// grokSnapshotUtilization 只使用 xAI 响应头投影出的请求数和 Token 配额，
// 不根据本站消费额或本地 Token 统计猜测上游容量。
func grokSnapshotUtilization(snapshot *xai.QuotaSnapshot) (float64, *time.Time, bool) {
	if snapshot == nil {
		return 0, nil, false
	}
	best := -1.0
	var bestReset *time.Time
	consider := func(window *xai.QuotaWindow) {
		if window == nil || window.Limit == nil || window.Remaining == nil || *window.Limit <= 0 {
			return
		}
		remaining := *window.Remaining
		if remaining < 0 {
			remaining = 0
		}
		used := (1 - float64(remaining)/float64(*window.Limit)) * 100
		if used < 0 {
			used = 0
		}
		if used > 100 {
			used = 100
		}
		if used <= best {
			return
		}
		best = used
		bestReset = quotaWindowResetTime(window)
	}
	consider(snapshot.Requests)
	consider(snapshot.Tokens)
	if best < 0 {
		return 0, nil, false
	}
	return best, bestReset, true
}

func quotaWindowResetTime(window *xai.QuotaWindow) *time.Time {
	if window == nil {
		return nil
	}
	if window.ResetUnix != nil {
		if value, ok := schedulingResetTime(*window.ResetUnix); ok {
			value = value.UTC()
			return &value
		}
	}
	if value, ok := schedulingResetTime(window.ResetAt); ok {
		value = value.UTC()
		return &value
	}
	return nil
}

func schedulingResetTime(raw any) (time.Time, bool) {
	parseUnixOrRelative := func(value int64) (time.Time, bool) {
		if value <= 0 {
			return time.Time{}, false
		}
		if value > 1_000_000_000_000 {
			return time.UnixMilli(value), true
		}
		if value < 1_000_000_000 {
			return time.Now().Add(time.Duration(value) * time.Second), true
		}
		return time.Unix(value, 0), true
	}
	switch value := raw.(type) {
	case time.Time:
		return value, !value.IsZero()
	case *time.Time:
		if value != nil {
			return *value, !value.IsZero()
		}
	case string:
		trimmed := strings.TrimSpace(value)
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return parsed, true
		}
		if unix, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return parseUnixOrRelative(unix)
		}
		if duration, err := time.ParseDuration(trimmed); err == nil && duration > 0 {
			return time.Now().Add(duration), true
		}
	case int64:
		return parseUnixOrRelative(value)
	case int:
		return parseUnixOrRelative(int64(value))
	case float64:
		return parseUnixOrRelative(int64(value))
	case json.Number:
		unix, err := value.Int64()
		if err == nil {
			return parseUnixOrRelative(unix)
		}
	}
	return time.Time{}, false
}

func numberAsFloat64(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func buildGrokSchedulingThresholdReason(decision grokSchedulingThresholdDecision, now time.Time) string {
	payload := grokSchedulingThresholdReason{
		Source:           grokSchedulingThresholdReasonSource,
		Platform:         PlatformGrok,
		ThresholdPercent: decision.ThresholdPercent,
		UsedPercent:      decision.UsedPercent,
		UntilUnix:        decision.Until.Unix(),
		TriggeredAtUnix:  now.Unix(),
		ErrorMessage: fmt.Sprintf(
			"Grok 上游配额已使用 %.1f%%，达到调度阈值 %d%%，暂停至 %s",
			decision.UsedPercent,
			decision.ThresholdPercent,
			decision.Until.UTC().Format(time.RFC3339),
		),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return payload.ErrorMessage
	}
	return string(raw)
}

func sameGrokSchedulingPause(rawReason string, until time.Time, nextReason string) bool {
	var current, next grokSchedulingThresholdReason
	if json.Unmarshal([]byte(rawReason), &current) != nil || json.Unmarshal([]byte(nextReason), &next) != nil {
		return false
	}
	if current.Source != grokSchedulingThresholdReasonSource || current.UntilUnix != until.Unix() {
		return false
	}
	current.TriggeredAtUnix = 0
	next.TriggeredAtUnix = 0
	return current == next
}

// ApplyGrokSchedulingThreshold 在账号达到管理员配置的上游配额阈值时安装
// runtime（运行时）拉黑、Redis 暂停缓存和数据库状态；本地去重避免旧调度快照
// 在高并发下反复写库。
func (s *RateLimitService) ApplyGrokSchedulingThreshold(ctx context.Context, account *Account) bool {
	if s == nil || s.settingService == nil || s.accountRepo == nil || account == nil || account.ID <= 0 || !account.IsActive() || !account.Schedulable {
		return false
	}
	now := time.Now().UTC()
	decision := evaluateGrokSchedulingThreshold(account, s.settingService.GetGrokSchedulingThresholdPercent(ctx), now)
	if !decision.ShouldPause {
		return false
	}
	reason := buildGrokSchedulingThresholdReason(decision, now)
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now) &&
		!isGrokSchedulingThresholdReason(account.TempUnschedulableReason) {
		return true
	}
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.Unix() == decision.Until.Unix() &&
		sameGrokSchedulingPause(account.TempUnschedulableReason, decision.Until, reason) {
		return true
	}

	s.grokSchedulingPauseMu.Lock()
	if previous, ok := s.grokSchedulingPauses[account.ID]; ok && previous.untilUnix == decision.Until.Unix() &&
		sameGrokSchedulingPause(previous.reason, decision.Until, reason) {
		s.grokSchedulingPauseMu.Unlock()
		return true
	}
	// 先持久化再发布本地去重和运行时状态。若数据库失败，下一次请求必须能够重试，
	// 不能因为内存里已经记过而永久跳过写库。候选状态只用于合并同账号并发写，
	// 数据库失败会按精确值撤销，其他账号也不会被本账号的数据库 I/O 阻塞。
	pending := grokSchedulingPauseState{untilUnix: decision.Until.Unix(), reason: reason}
	s.grokSchedulingPauses[account.ID] = pending
	s.grokSchedulingPauseMu.Unlock()
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, decision.Until, reason); err != nil {
		s.grokSchedulingPauseMu.Lock()
		if current, ok := s.grokSchedulingPauses[account.ID]; ok && current == pending {
			delete(s.grokSchedulingPauses, account.ID)
		}
		s.grokSchedulingPauseMu.Unlock()
		slog.Warn("grok_scheduling_threshold_persist_failed", "account_id", account.ID, "error", err)
		return true
	}
	account.TempUnschedulableUntil = &decision.Until
	account.TempUnschedulableReason = reason
	s.notifyAccountSchedulingBlocked(account, decision.Until, grokSchedulingThresholdReasonSource)
	if s.tempUnschedCache != nil {
		state := &TempUnschedState{
			UntilUnix:       decision.Until.Unix(),
			TriggeredAtUnix: now.Unix(),
			RuleIndex:       -1,
			ErrorMessage:    reason,
		}
		if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
			slog.Warn("grok_scheduling_threshold_cache_failed", "account_id", account.ID, "error", err)
		}
	}
	return true
}

func isGrokSchedulingThresholdReason(raw string) bool {
	var reason grokSchedulingThresholdReason
	return json.Unmarshal([]byte(strings.TrimSpace(raw)), &reason) == nil && reason.Source == grokSchedulingThresholdReasonSource
}

// ReconcileGrokSchedulingThreshold 只恢复由本规则暂停、且在新阈值下不再命中的账号。
// 401、429、人工停调等状态使用不同原因，绝不会被这里清除。
func (s *RateLimitService) ReconcileGrokSchedulingThreshold(threshold int) {
	if s == nil || s.accountRepo == nil {
		return
	}
	s.grokThresholdSyncMu.Lock()
	defer s.grokThresholdSyncMu.Unlock()
	clearer, ok := s.accountRepo.(grokSchedulingThresholdConditionalClearer)
	if !ok {
		slog.Warn("grok_scheduling_threshold_reconcile_unsupported")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformGrok)
	if err != nil {
		slog.Warn("grok_scheduling_threshold_reconcile_list_failed", "error", err)
		return
	}
	now := time.Now().UTC()
	for index := range accounts {
		account := &accounts[index]
		if !isGrokSchedulingThresholdReason(account.TempUnschedulableReason) {
			continue
		}
		// 旧版本或人工修复可能留下“有阈值原因但没有截止时间”的不完整状态。
		// 缺少 until 时无法执行精确 CAS，宁可保留并告警，也不能猜测后误解封账号。
		if account.TempUnschedulableUntil == nil {
			slog.Warn("grok_scheduling_threshold_reconcile_missing_until", "account_id", account.ID)
			continue
		}
		if evaluateGrokSchedulingThreshold(account, threshold, now).ShouldPause {
			continue
		}
		// 先用原因和截止时间执行数据库 CAS（比较后清除），成功后才能撤销本地阻断。
		// 若并发写入了 401/429 等新暂停，CAS 会失败；在确认新状态前绝不能留下
		// 哪怕很短的临时放行窗口，否则高并发请求可能恰好选中仍应暂停的账号。
		cleared, err := clearer.ClearGrokSchedulingThresholdPauseIfMatch(ctx, account.ID, *account.TempUnschedulableUntil, account.TempUnschedulableReason)
		if err != nil {
			slog.Warn("grok_scheduling_threshold_reconcile_clear_failed", "account_id", account.ID, "error", err)
		}
		if cleared {
			s.grokSchedulingPauseMu.Lock()
			delete(s.grokSchedulingPauses, account.ID)
			s.grokSchedulingPauseMu.Unlock()
			if s.tempUnschedCache != nil {
				if err := s.tempUnschedCache.DeleteTempUnsched(ctx, account.ID); err != nil {
					slog.Warn("grok_scheduling_threshold_reconcile_cache_failed", "account_id", account.ID, "error", err)
				}
			}
			s.notifyAccountSchedulingBlockCleared(account.ID)
			continue
		}
		state, stateErr := s.GetTempUnschedStatus(ctx, account.ID)
		if stateErr != nil {
			slog.Warn("grok_scheduling_threshold_reconcile_reload_failed", "account_id", account.ID, "error", stateErr)
			continue
		}
		if state != nil && state.UntilUnix > now.Unix() {
			s.notifyAccountSchedulingBlocked(account, time.Unix(state.UntilUnix, 0), state.ErrorMessage)
			continue
		}
		if err == nil {
			slog.Info("grok_scheduling_threshold_reconcile_state_changed", "account_id", account.ID)
		}
	}
}
