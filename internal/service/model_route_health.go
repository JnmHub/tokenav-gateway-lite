package service

import (
	"strings"
	"sync"
	"time"
)

// 智能分流目标健康组件：按 (分组, 公开模型, 目标模型) 维护进程内健康状态。
//
// 设计边界（与 openai_account_model_transient.go 同族）：
//   - 只反映当前网关进程观测到的请求结果，不落库、不进 Redis；
//     多网关部署时各自独立收敛，代价是每台最多多付一次失败探测。
//   - fail-open：组件自身异常或条目超限时按健康处理，绝不因为健康状态
//     缺失把"所有目标都不可选"放大成拒绝服务。
//   - 账号级故障（401/403/限流）由既有账号禁用链路处理，这里只记录
//     归因到目标模型的信号，两层互不越界。

const (
	modelRouteStreakTTL           = 30 * time.Minute
	modelRouteProbeTTL            = 30 * time.Second
	modelRouteDefaultMaxEntries   = 4096
	modelRouteMaxModelBytes       = 200
	modelRouteNotFoundCooldown    = 30 * time.Minute
	modelRouteShortCooldown       = 10 * time.Second
	modelRouteMediumCooldown      = 45 * time.Second
	modelRouteLongCooldown        = 5 * time.Minute
	modelRouteMaxBackoffCooldown  = 30 * time.Minute
	modelRouteMinTargetsPerKey    = 2
	modelRouteMaxConsecutiveStep1 = 2
	modelRouteMaxConsecutiveStep2 = 5
	modelRouteMaxConsecutiveStep3 = 8
)

// ModelRouteFailureSignal 区分目标级失败的严重程度。
type ModelRouteFailureSignal string

const (
	// ModelRouteSignalModelNotFound：上游明确表示该模型不存在 / 当前套餐不可用
	//（404 model not found、Codex plan-gated 400）。模型本身短期不会恢复，
	// 直接施加强冷却（对齐 HandleUpstreamModelNotFound 的 30 分钟先例）。
	ModelRouteSignalModelNotFound ModelRouteFailureSignal = "model_not_found"
	// ModelRouteSignalGeneric：无法归因到账号的瞬时失败（5xx、529、网络类）。
	// 单次失败不禁用，连续失败才渐进冷却，避免把上游抖动误判成模型下线。
	ModelRouteSignalGeneric ModelRouteFailureSignal = "generic"
	// ModelRouteSignalAuth：401/403/429 等账号级信号。账号禁用链路已处理，
	// 目标健康不重复记录，防止健康账号切换后目标被误冷却。
	ModelRouteSignalAuth ModelRouteFailureSignal = "auth"
)

// ModelRouteHealthState 是目标在健康组件里的运行时状态。
type ModelRouteHealthState string

const (
	ModelRouteHealthHealthy    ModelRouteHealthState = "healthy"
	ModelRouteHealthFailing    ModelRouteHealthState = "failing"
	ModelRouteHealthCooling    ModelRouteHealthState = "cooling"
	ModelRouteHealthRecovering ModelRouteHealthState = "recovering"
)

type modelRouteKey struct {
	GroupID     int64
	PublicModel string
	TargetModel string
}

type modelRouteHealthEntry struct {
	failureStreak int
	lastFailure   time.Time
	lastSuccess   time.Time
	cooldownUntil time.Time
	cooldownFloor time.Duration // 半开探测失败后的退避下限，按上次冷却翻倍
	probeUntil    time.Time
	lastTouched   time.Time
	lastReason    string
}

// ModelRouteHealthView 是管理端只读摘要（不含任何凭据或上游正文）。
type ModelRouteHealthView struct {
	GroupID           int64                  `json:"group_id"`
	PublicModel       string                 `json:"public_model"`
	TargetModel       string                 `json:"target_model"`
	State             ModelRouteHealthState  `json:"state"`
	ConsecutiveFail   int                    `json:"consecutive_failures"`
	LastReason        string                 `json:"last_reason,omitempty"`
	LastSuccessAt     *time.Time             `json:"last_success_at,omitempty"`
	LastFailureAt     *time.Time             `json:"last_failure_at,omitempty"`
	CooldownUntil     *time.Time             `json:"cooldown_until,omitempty"`
	ProbeUntil        *time.Time             `json:"probe_until,omitempty"`
	CooldownRemaining int64                  `json:"cooldown_remaining_ms,omitempty"`
}

type modelRouteHealthState struct {
	mu         sync.Mutex
	entries    map[modelRouteKey]modelRouteHealthEntry
	maxEntries int
}

var defaultModelRouteHealth = newModelRouteHealthState(modelRouteDefaultMaxEntries)

func newModelRouteHealthState(maxEntries int) *modelRouteHealthState {
	if maxEntries <= 0 {
		maxEntries = modelRouteDefaultMaxEntries
	}
	return &modelRouteHealthState{
		entries:    make(map[modelRouteKey]modelRouteHealthEntry),
		maxEntries: maxEntries,
	}
}

func normalizeModelRouteName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > modelRouteMaxModelBytes {
		return ""
	}
	return strings.ToLower(value)
}

func modelRouteKeyFor(groupID int64, publicModel, targetModel string) (modelRouteKey, bool) {
	public := normalizeModelRouteName(publicModel)
	target := normalizeModelRouteName(targetModel)
	if groupID <= 0 || public == "" || target == "" || public == target {
		return modelRouteKey{}, false
	}
	return modelRouteKey{GroupID: groupID, PublicModel: public, TargetModel: target}, true
}

func modelRouteEntryExpired(entry modelRouteHealthEntry, now time.Time) bool {
	if now.Sub(entry.lastTouched) > modelRouteStreakTTL {
		return true
	}
	// 冷却与探测租约都过期且无失败串：条目已无调度意义。
	if !entry.cooldownUntil.IsZero() && now.Before(entry.cooldownUntil) {
		return false
	}
	if !entry.probeUntil.IsZero() && now.Before(entry.probeUntil) {
		return false
	}
	return entry.failureStreak == 0
}

func modelRouteCooldownFor(streak int) time.Duration {
	switch {
	case streak >= modelRouteMaxConsecutiveStep3:
		return modelRouteLongCooldown
	case streak >= modelRouteMaxConsecutiveStep2:
		return modelRouteMediumCooldown
	case streak >= modelRouteMaxConsecutiveStep1:
		return modelRouteShortCooldown
	default:
		return 0
	}
}

// RecordModelRouteFailure 记录一次归因到分流目标的失败并返回是否进入冷却。
// 401/403/429 等账号级信号直接忽略（ModelRouteSignalAuth）。
func RecordModelRouteFailure(groupID int64, publicModel, targetModel string, signal ModelRouteFailureSignal) bool {
	return defaultModelRouteHealth.recordFailure(groupID, publicModel, targetModel, signal, time.Now())
}

func (h *modelRouteHealthState) recordFailure(groupID int64, publicModel, targetModel string, signal ModelRouteFailureSignal, now time.Time) bool {
	if h == nil || signal == "" || signal == ModelRouteSignalAuth {
		return false
	}
	key, ok := modelRouteKeyFor(groupID, publicModel, targetModel)
	if !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entries == nil {
		h.entries = make(map[modelRouteKey]modelRouteHealthEntry)
	}
	entry, exists := h.entries[key]
	if !exists {
		// 有界状态：超限时丢最旧条目，绝不让健康簿本无限增长。
		if len(h.entries) >= h.maxEntries {
			h.evictOldestLocked(now)
		}
	}
	// 探测租约活跃期间的失败 = 半开探测失败：退避下限必须在下面的
	// 条目重置之前抢救出来，否则 30 分钟 TTL 会把翻倍记忆清零。
	carryFloor := time.Duration(0)
	if exists && !entry.probeUntil.IsZero() && now.Before(entry.probeUntil) {
		carryFloor = entry.cooldownFloor
	}
	if !exists || modelRouteEntryExpired(entry, now) || entry.lastFailure.IsZero() ||
		now.Sub(entry.lastFailure) > modelRouteStreakTTL || now.Before(entry.lastFailure) {
		entry = modelRouteHealthEntry{}
	}

	entry.failureStreak++
	entry.lastFailure = now
	entry.lastTouched = now
	entry.probeUntil = time.Time{}

	cooldown := time.Duration(0)
	switch signal {
	case ModelRouteSignalModelNotFound:
		cooldown = modelRouteNotFoundCooldown
		entry.lastReason = string(ModelRouteSignalModelNotFound)
	default:
		cooldown = modelRouteCooldownFor(entry.failureStreak)
		entry.lastReason = string(ModelRouteSignalGeneric)
	}
	// 半开探测失败的退避：新冷却至少是历史下限的 2 倍并逐次翻倍，
	// 上限统一 30 分钟（404 硬信号首次即触顶，探测失败维持封顶值）。
	if cooldown > 0 {
		if carryFloor > 0 {
			if doubled := carryFloor * 2; cooldown < doubled {
				cooldown = doubled
			}
		}
		if cooldown > modelRouteMaxBackoffCooldown {
			cooldown = modelRouteMaxBackoffCooldown
		}
		entry.cooldownUntil = now.Add(cooldown)
		if cooldown > entry.cooldownFloor {
			entry.cooldownFloor = cooldown
		}
	} else {
		entry.cooldownUntil = time.Time{}
	}
	h.entries[key] = entry
	return cooldown > 0
}

// RecordModelRouteSuccess 成功一次即清零失败串并结束冷却/探测，
// 目标立即恢复满权重参与分流。
func RecordModelRouteSuccess(groupID int64, publicModel, targetModel string) {
	defaultModelRouteHealth.recordSuccess(groupID, publicModel, targetModel, time.Now())
}

func (h *modelRouteHealthState) recordSuccess(groupID int64, publicModel, targetModel string, now time.Time) {
	if h == nil {
		return
	}
	key, ok := modelRouteKeyFor(groupID, publicModel, targetModel)
	if !ok {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, exists := h.entries[key]
	if !exists {
		return
	}
	entry.failureStreak = 0
	entry.lastSuccess = now
	entry.lastTouched = now
	entry.cooldownUntil = time.Time{}
	entry.probeUntil = time.Time{}
	entry.cooldownFloor = 0
	entry.lastReason = ""
	h.entries[key] = entry
}

// acquireModelRouteProbe 实现半开恢复：冷却刚结束的目标只放行一个请求探测。
// 拿到 30 秒探测租约的请求正常转发（成功即恢复，失败即翻倍冷却）；
// 没拿到租约的请求把该目标视作仍在冷却。
func (h *modelRouteHealthState) acquireModelRouteProbe(key modelRouteKey, now time.Time) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, exists := h.entries[key]
	if !exists {
		return true
	}
	if !entry.cooldownUntil.IsZero() && now.Before(entry.cooldownUntil) {
		return false
	}
	if !entry.probeUntil.IsZero() && now.Before(entry.probeUntil) {
		return false
	}
	entry.probeUntil = now.Add(modelRouteProbeTTL)
	entry.lastTouched = now
	h.entries[key] = entry
	return true
}

// modelRouteSelectable 判定目标当前能否参与分流。
// 三态：从未冷却（健康）直接可选；冷却中不可选；冷却刚结束处于半开，
// 只有拿到 30 秒探测租约的那一个请求可选，其余请求继续避开该目标，
// 避免恢复瞬间的大流量把刚回来的上游再次打挂。
func (h *modelRouteHealthState) modelRouteSelectable(key modelRouteKey, now time.Time) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	entry, exists := h.entries[key]
	h.mu.Unlock()
	if !exists {
		return true
	}
	if entry.cooldownUntil.IsZero() {
		return true
	}
	if now.Before(entry.cooldownUntil) {
		return false
	}
	// 半开：尝试拿探测租约。
	return h.acquireModelRouteProbe(key, now)
}

func (h *modelRouteHealthState) evictOldestLocked(now time.Time) {
	var oldestKey modelRouteKey
	var oldestTouched time.Time
	first := true
	for key, entry := range h.entries {
		if first || entry.lastTouched.Before(oldestTouched) {
			oldestKey, oldestTouched, first = key, entry.lastTouched, false
		}
	}
	if !first {
		delete(h.entries, oldestKey)
	}
}

// isModelRouteCooling 只读判定目标是否处于冷却，不消耗半开探测租约；
// 供无账号轮转等"挑选候选"场景使用，正常选号路径必须走 modelRouteSelectable
// （它会消耗探测租约实现半开恢复）。
func isModelRouteCooling(groupID int64, publicModel, targetModel string) bool {
	key, ok := modelRouteKeyFor(groupID, publicModel, targetModel)
	if !ok {
		return false
	}
	return defaultModelRouteHealth.modelRouteCooling(key, time.Now())
}

func (h *modelRouteHealthState) modelRouteCooling(key modelRouteKey, now time.Time) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	entry, exists := h.entries[key]
	h.mu.Unlock()
	return exists && !entry.cooldownUntil.IsZero() && now.Before(entry.cooldownUntil)
}

// ModelRouteHealthSnapshot 输出某个分组全部分流目标的健康摘要（管理端展示用）。
func ModelRouteHealthSnapshot(groupID int64) []ModelRouteHealthView {
	return defaultModelRouteHealth.snapshot(groupID, time.Now())
}

func (h *modelRouteHealthState) snapshot(groupID int64, now time.Time) []ModelRouteHealthView {
	if h == nil || groupID <= 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	views := make([]ModelRouteHealthView, 0)
	for key, entry := range h.entries {
		if key.GroupID != groupID {
			continue
		}
		view := ModelRouteHealthView{
			GroupID:         key.GroupID,
			PublicModel:     key.PublicModel,
			TargetModel:     key.TargetModel,
			ConsecutiveFail: entry.failureStreak,
			LastReason:      entry.lastReason,
		}
		if !entry.lastSuccess.IsZero() {
			successAt := entry.lastSuccess
			view.LastSuccessAt = &successAt
		}
		if !entry.lastFailure.IsZero() {
			failureAt := entry.lastFailure
			view.LastFailureAt = &failureAt
		}
		cooling := !entry.cooldownUntil.IsZero() && now.Before(entry.cooldownUntil)
		probing := !entry.probeUntil.IsZero() && now.Before(entry.probeUntil)
		switch {
		case cooling:
			view.State = ModelRouteHealthCooling
			view.CooldownUntil = &entry.cooldownUntil
			view.CooldownRemaining = entry.cooldownUntil.Sub(now).Milliseconds()
		case probing:
			view.State = ModelRouteHealthRecovering
			view.ProbeUntil = &entry.probeUntil
		case entry.failureStreak > 0:
			view.State = ModelRouteHealthFailing
		default:
			view.State = ModelRouteHealthHealthy
		}
		views = append(views, view)
	}
	return views
}

// ResetModelRouteHealth 手动清除一个目标的冷却状态（管理端一键重置）。
func ResetModelRouteHealth(groupID int64, publicModel, targetModel string) bool {
	return defaultModelRouteHealth.reset(groupID, publicModel, targetModel)
}

func (h *modelRouteHealthState) reset(groupID int64, publicModel, targetModel string) bool {
	if h == nil {
		return false
	}
	key, ok := modelRouteKeyFor(groupID, publicModel, targetModel)
	if !ok {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, exists := h.entries[key]
	if !exists {
		return false
	}
	entry.failureStreak = 0
	entry.cooldownUntil = time.Time{}
	entry.probeUntil = time.Time{}
	entry.cooldownFloor = 0
	entry.lastReason = ""
	entry.lastTouched = time.Now()
	h.entries[key] = entry
	return true
}
