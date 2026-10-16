package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Kimi 将单账号并发达到上限返回为 403；这只是短暂的容量状态，不能按
// 权限失效处理，否则连续请求会把仍然有效的账号永久移出调度池。
const kimiConcurrentRequestLimitMessage = "You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."

const cnConcurrencyLimitReasonPrefix = "cn_concurrency_limit"

func isCNProviderConcurrencyLimit403(account *Account, upstreamMsg string) bool {
	return account != nil && account.Platform == PlatformKimi &&
		strings.TrimSpace(upstreamMsg) == kimiConcurrentRequestLimitMessage
}

// handleCNProviderConcurrencyLimit403 临时冷却 Kimi 账号并保留故障转移。
// 先写运行时阻塞，再写持久化状态，即使数据库短暂失败也不会在当前进程内
// 立即重新选中同一账号；同时不触碰 OpenAI 403 累计计数器。
func (s *RateLimitService) handleCNProviderConcurrencyLimit403(ctx context.Context, account *Account) {
	until := time.Now().Add(time.Duration(openAI403CooldownMinutesDefault) * time.Minute)
	reason := cnConcurrencyLimitReasonPrefix + ": " + kimiConcurrentRequestLimitMessage
	s.notifyAccountSchedulingBlocked(account, until, cnConcurrencyLimitReasonPrefix)
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("cn_concurrency_limit_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info("cn_provider_concurrency_limited",
		"account_id", account.ID,
		"platform", account.Platform,
		"until", until.UTC(),
	)
}

// [v0.2.5 新增] cnBalanceLowReasonPrefix 是余额不足临时停调 reason 的稳定前缀。
// 周期余额检测任务据此识别「是我们停调的」并在余额恢复后安全清除。
const cnBalanceLowReasonPrefix = "cn_balance_low"

// cnQuotaExhausted403ErrorType 是 Kimi Coding Plan 配额窗口耗尽的 403 错误类型。
// 与并发限制 403（见 isCNProviderConcurrencyLimit403）不同，这是 5h/weekly
// 窗口耗尽的限流信号：窗口到期后自动恢复，绝不能落入通用 403 升级计数
// （连续 3 次永久 SetError），必须按 429 口径冷却到真实窗口重置点。
// [v0.2.7] 官方 1a9d49e1 合并。
const cnQuotaExhausted403ErrorType = "access_terminated_error"

// cnQuotaExhaustedReasonPrefix 是配额耗尽 403 临时停调 reason 的稳定前缀。
const cnQuotaExhaustedReasonPrefix = "cn_quota_exhausted"

// isCNProviderQuotaExhausted403 识别 CN 供应商配额窗口耗尽的 403。
// 先做廉价的文案子串匹配（usage limit / quota will reset），未命中再解析
// 响应体匹配结构化错误类型（Kimi 的 access_terminated_error），以兼容同族
// 供应商的文案变体。与并发限制文案互斥，两类信号不会误判。
// 仅限 Coding Plan 账号：滚动窗口快照（含重置时间）只有 Coding Plan 账号才有，
// 非 Coding Plan 账号的同类 403 语义不明（可能是需要升级套餐的硬上限），
// 应回退到通用 403 升级逻辑而非无限临时冷却。
func isCNProviderQuotaExhausted403(account *Account, responseBody []byte, upstreamMsg string) bool {
	if account == nil || !account.IsCNProvider() || !account.IsCodingPlan() {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(upstreamMsg))
	if strings.Contains(msg, "usage limit") || strings.Contains(msg, "quota will reset") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(gjson.GetBytes(responseBody, "error.type").String()), cnQuotaExhausted403ErrorType)
}

// handleCNProviderQuotaExhausted403 把 CN 供应商配额窗口耗尽的 403 按 429
// 同口径处理：有配额快照时冷却到最早的未来窗口重置点（SetRateLimited，
// 窗口过期后调度自动恢复）；快照缺失（额度探测尚未刷新）时兜底默认 403
// 冷却时长的临时停调，避免过度停调到数天后的窗口。无论哪条路径都不写
// 账号 error 状态。
func (s *RateLimitService) handleCNProviderQuotaExhausted403(
	ctx context.Context,
	account *Account,
	upstreamMsg string,
) {
	if s.cooldownCNProviderToQuotaSnapshotReset(ctx, account, cnQuotaExhaustedReasonPrefix, "cn_quota_exhausted_rate_limited") != nil {
		return
	}

	// upstreamMsg 由调用方（HandleUpstreamError）从同一响应体提取，此处直接复用；
	// 为空时仅存前缀，保证 reason 稳定可检索。
	reason := cnQuotaExhaustedReasonPrefix
	if msg := strings.TrimSpace(upstreamMsg); msg != "" {
		reason += ": " + msg
	}
	until := time.Now().Add(time.Duration(openAI403CooldownMinutesDefault) * time.Minute)
	s.setCNProviderTempUnschedulable(ctx, account, until, cnQuotaExhaustedReasonPrefix, reason, "cn_quota_exhausted_temp_unschedulable")
}

// cnProviderQuotaSnapshotReset 返回配额快照中最早的未来窗口重置点（5h/weekly，
// OpenCodeGo 另含 monthly）。无快照或均已过期时返回 nil。
// [v0.2.7] 随配额耗尽 403 修复一并搬回（fork 之前的网关轻量化裁剪中移除）。
func cnProviderQuotaSnapshotReset(account *Account, now time.Time) *time.Time {
	if account == nil || len(account.Extra) == 0 {
		return nil
	}
	if !account.IsOpenCodeGo() && (!account.IsCNProvider() || !account.IsCodingPlan()) {
		return nil
	}
	provider := account.Platform
	suffixes := []string{cnExtraSuffix5hReset, cnExtraSuffixWeeklyReset}
	if account.IsOpenCodeGo() {
		suffixes = append(suffixes, cnExtraSuffixMonthlyReset)
	}
	var earliest *time.Time
	for _, suffix := range suffixes {
		t := parseSchedulingResetAt(account.Extra[cnExtraKey(provider, suffix)])
		if t == nil || !t.After(now) {
			continue
		}
		if earliest == nil || t.Before(*earliest) {
			earliest = t
		}
	}
	return earliest
}

// cooldownCNProviderToQuotaSnapshotReset 把账号冷却到配额快照中最早的未来窗口
// 重置点（SetRateLimited，窗口过期后调度自动恢复），供 429（OpenCodeGo /
// Coding Plan）与配额耗尽 403 共用。仅在冷却成功持久化后返回非 nil；
// 无快照或写库失败时返回 nil，由调用方落入各自的兜底逻辑（避免写库失败
// 时账号完全失去持久冷却）。
func (s *RateLimitService) cooldownCNProviderToQuotaSnapshotReset(ctx context.Context, account *Account, reason, logEvent string) *time.Time {
	until := cnProviderQuotaSnapshotReset(account, time.Now())
	if until == nil {
		return nil
	}
	s.notifyAccountSchedulingBlocked(account, *until, reason)
	if err := s.accountRepo.SetRateLimited(ctx, account.ID, *until); err != nil {
		slog.Warn("rate_limit_set_failed", "account_id", account.ID, "error", err)
		return nil
	}
	slog.Info(logEvent,
		"account_id", account.ID,
		"platform", account.Platform,
		"reset_at", until.UTC(),
	)
	return until
}

// setCNProviderTempUnschedulable 写临时停调（含调度层通知与日志），供 CN 403
// 各分支（并发限制、配额耗尽兜底）共用。告警 key 由 notifyReason 派生，保持
// 各分支既有日志 key 稳定（<prefix>_set_temp_unschedulable_failed）。
func (s *RateLimitService) setCNProviderTempUnschedulable(ctx context.Context, account *Account, until time.Time, notifyReason, storeReason, logEvent string) {
	s.notifyAccountSchedulingBlocked(account, until, notifyReason)
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, storeReason); err != nil {
		slog.Warn(notifyReason+"_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Info(logEvent,
		"account_id", account.ID,
		"platform", account.Platform,
		"until", until.UTC(),
	)
}

// [v0.2.5 新增] cnBalanceLowReason 构造余额不足临时停调的 reason（带稳定前缀）。
func cnBalanceLowReason(upstreamMsg string) string {
	if upstreamMsg = strings.TrimSpace(upstreamMsg); upstreamMsg != "" {
		return cnBalanceLowReasonPrefix + ": " + upstreamMsg
	}
	return cnBalanceLowReasonPrefix + ": 余额不足，账号临时停调"
}
