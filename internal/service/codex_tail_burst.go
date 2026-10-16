package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Codex tail-burst（超额）状态评估。
//
// 与本地预防性停调（OpenAI quota auto-pause）配合：当账号进入尾部爆发状态时，
// 本地预防性停调被跳过，账号继续承接请求，直到上游返回权威配额拒绝
// （429 usage_limit_reached / 403 insufficient_quota 等）才真正冻结。
// 硬冻结（限流、过载、其他原因临时停调）不受影响，优先级高于尾部放行。

const (
	defaultCodexTailBurstTriggerRemainingRatio = 0.02
	defaultCodexTailBurstExpiryWindow          = 10 * time.Minute
	defaultCodexTailBurstSnapshotTTL           = 90 * time.Second

	codexTailBurstWindow5h = "5h"
	codexTailBurstWindow7d = "7d"

	codexTailBurstReasonUsedRatio     = "used_ratio"
	codexTailBurstReasonResetSoon     = "reset_soon"
	codexTailBurstReasonAccountExpiry = "account_expiry"

	// codex_tail_burst_disabled 账号级超额开关：显式 true 时该账号完全退出
	// tail-burst（采集快照、尾部放行、会话结算都不再生效）；缺失或 false
	// 时跟随全局开关（系统设置 → 功能开关）。
	codexTailBurstExtraDisabled = "codex_tail_burst_disabled"
)

// codexTailBurstAccountDisabled 报告账号是否被显式关闭超额。
func codexTailBurstAccountDisabled(extra map[string]any) bool {
	return resolveAccountExtraBool(extra, codexTailBurstExtraDisabled)
}

// CodexTailBurstState 是一次评估的尾部爆发状态。
type CodexTailBurstState struct {
	Active bool   `json:"active"`
	Reason string `json:"reason,omitempty"`
	Window string `json:"window,omitempty"`
}

// codexTailBurstSettings 解析配置节，缺省值保持与官方 Codex 客户端行为一致
// （剩余 2%、窗口/订阅到期前 10 分钟、快照 90 秒新鲜）。
func codexTailBurstSettings(cfg *config.Config) (triggerRemainingRatio float64, expiryWindow, snapshotTTL time.Duration) {
	triggerRemainingRatio = defaultCodexTailBurstTriggerRemainingRatio
	expiryWindow = defaultCodexTailBurstExpiryWindow
	snapshotTTL = defaultCodexTailBurstSnapshotTTL
	if cfg == nil {
		return triggerRemainingRatio, expiryWindow, snapshotTTL
	}
	if ratio := cfg.CodexTailBurst.TriggerRemainingRatio; ratio > 0 && ratio < 1 {
		triggerRemainingRatio = ratio
	}
	if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.CodexTailBurst.ExpiryWindow)); err == nil && parsed > 0 {
		expiryWindow = parsed
	}
	if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.CodexTailBurst.SnapshotTTL)); err == nil && parsed > 0 {
		snapshotTTL = parsed
	}
	return triggerRemainingRatio, expiryWindow, snapshotTTL
}

// EvaluateCodexTailBurst 评估账号是否处于尾部爆发状态：
//   - 账号订阅（expires_at）即将到期（expiry_window 内）→ 尾部放行（不要求快照新鲜，
//     与上游租约到期语义一致：租约本身就是权威信号）；
//   - 任一窗口（5h/7d）已用比例 >= 1-triggerRemainingRatio（默认 98%），且快照新鲜；
//   - 任一窗口的 reset_at 在 expiry_window 内即将重置，且快照新鲜。
//
// 快照过期时不再据此放行（不知道当前真实用量，宁可不放行）。
// 上游用量端点的百分比是四舍五入值：已采样到 100% 的窗口仍留在尾部通道，
// 直到上游返回真实的配额错误——否则每个账号最后不足一个百分点的额度都会被浪费。
func EvaluateCodexTailBurst(account *Account, now time.Time, triggerRemainingRatio float64, expiryWindow, snapshotTTL time.Duration) CodexTailBurstState {
	state := CodexTailBurstState{}
	if account == nil || !account.IsOpenAIOAuth() || account.IsShadow() {
		return state
	}
	if codexTailBurstAccountDisabled(account.Extra) {
		return state
	}

	// 只把“尚未到期且将在窗口内到期”的账号纳入尾部放行；已经到期的账号
	// 必须继续遵守正常停调规则，不能因为历史 expires_at 绕过过期保护。
	if account.ExpiresAt != nil && !account.ExpiresAt.IsZero() &&
		account.ExpiresAt.After(now) && !account.ExpiresAt.After(now.Add(expiryWindow)) {
		state.Active = true
		state.Reason = codexTailBurstReasonAccountExpiry
		return state
	}

	triggerRatio := 1 - triggerRemainingRatio
	for _, window := range []string{codexTailBurstWindow5h, codexTailBurstWindow7d} {
		// 每个限流窗口必须使用自己的采样时间判断新鲜度。不能因为 5h
		// 窗口刚更新，就把已经过期的 7d 快照也当成可信数据，反之亦然。
		if !codexTailBurstSnapshotFresh(account, window, now, snapshotTTL) {
			continue
		}
		usedPercent := readOpenAIQuotaUsedPercent(account.Extra, window)
		if usedPercent <= 0 {
			continue
		}
		if usedPercent/100 >= triggerRatio {
			state.Active = true
			state.Reason = codexTailBurstReasonUsedRatio
			state.Window = window
			return state
		}
		if resetAt, ok := codexTailBurstWindowResetAt(account.Extra, window, now); ok &&
			!resetAt.Before(now) && !resetAt.After(now.Add(expiryWindow)) {
			state.Active = true
			state.Reason = codexTailBurstReasonResetSoon
			state.Window = window
			return state
		}
	}
	return state
}

// codexTailBurstSnapshotFresh 判断指定限流窗口的用量快照是否在 snapshotTTL
// 内。新版本优先读取 codex_<window>_usage_updated_at；旧数据只有统一的
// codex_usage_updated_at 时才回退该字段。窗口参数不能省略，否则不同窗口的
// 更新时间会互相污染 Tail-Burst（超额放行）判定。
func codexTailBurstSnapshotFresh(account *Account, window string, now time.Time, ttl time.Duration) bool {
	if account == nil || len(account.Extra) == 0 || ttl <= 0 {
		return false
	}
	window = strings.TrimSpace(window)
	if window != codexTailBurstWindow5h && window != codexTailBurstWindow7d {
		return false
	}
	updatedRaw, ok := account.Extra["codex_"+window+"_usage_updated_at"]
	if !ok {
		// 只对完全没有窗口级时间戳的旧快照回退统一时间戳。若另一窗口
		// 已存在独立时间戳，说明这是新格式的部分快照；此时缺失窗口必须
		// 视为过期，不能借用另一窗口或全局时间戳放行旧数据。
		_, hasFiveHourTimestamp := account.Extra["codex_"+codexTailBurstWindow5h+"_usage_updated_at"]
		_, hasSevenDayTimestamp := account.Extra["codex_"+codexTailBurstWindow7d+"_usage_updated_at"]
		if hasFiveHourTimestamp || hasSevenDayTimestamp {
			return false
		}
		updatedRaw, ok = account.Extra["codex_usage_updated_at"]
	}
	if !ok {
		return false
	}
	updatedAt, err := parseTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return false
	}
	// A clock-skewed writer can leave a future timestamp. Treat it as stale
	// instead of allowing an unverified snapshot to bypass the pause guard.
	if updatedAt.After(now) {
		return false
	}
	return now.Sub(updatedAt) <= ttl
}

// codexTailBurstWindowResetAt 返回窗口的绝对重置时间。优先 codex_<w>_reset_at，
// 回退到 codex_<w>_reset_after_seconds 锚定 codex_usage_updated_at
// （与 openAIQuotaWindowReset 相同的键语义，但返回时间而非 bool）。
func codexTailBurstWindowResetAt(extra map[string]any, window string, now time.Time) (time.Time, bool) {
	if len(extra) == 0 {
		return time.Time{}, false
	}
	if resetAtRaw, ok := extra["codex_"+window+"_reset_at"]; ok {
		if resetAt, err := parseTime(fmt.Sprint(resetAtRaw)); err == nil {
			return resetAt, true
		}
	}
	resetAfter := parseExtraInt(extra["codex_"+window+"_reset_after_seconds"])
	if resetAfter <= 0 {
		return time.Time{}, false
	}
	base := now
	if updatedAt, ok := codexWindowUpdatedAt(extra, window); ok && !updatedAt.After(now) {
		base = updatedAt
	}
	return base.Add(time.Duration(resetAfter) * time.Second), true
}
