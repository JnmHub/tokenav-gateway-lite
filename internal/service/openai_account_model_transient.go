package service

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// 失败串由成功请求清零；此 TTL 只负责回收长期不用的账号+模型状态。
	// 短窗口会让低流量坏账号每次都回到 streak=1，永远无法进入冷却。
	openAIModelTransientStreakTTL     = 30 * time.Minute
	openAIModelTransientShortCooldown = 10 * time.Second
	openAIModelTransientLongCooldown  = 45 * time.Second
	// 冷却结束后的恢复探测必须有独立租约。否则大量并发请求会在同一毫秒
	// 同时冲击刚恢复的上游账号+模型，把一次故障立即放大成新的故障风暴。
	openAIModelTransientProbeTTL      = 30 * time.Second
	openAIModelTransientDefaultMax    = 4096
	openAIModelTransientMaxModelBytes = 512
)

type openAIAccountModelKey struct {
	AccountID int64
	Model     string
}

type openAIAccountModelTransientEntry struct {
	failureStreak int
	lastFailure   time.Time
	lastSuccess   time.Time
	blockUntil    time.Time
	probeUntil    time.Time
	lastTouched   time.Time
}

type openAIAccountModelTransientDecision struct {
	FailureStreak int
	Cooldown      time.Duration
	BlockUntil    time.Time
}

// OpenAIAccountModelHealthState 是账号+模型的运行时健康状态。它只反映
// 当前网关进程观测到的真实请求结果；管理员配置的模型声明由账号配置单独提供，
// 两者不能混为“可用”。
type OpenAIAccountModelHealthState string

const (
	OpenAIAccountModelHealthHealthy    OpenAIAccountModelHealthState = "healthy"
	OpenAIAccountModelHealthFailing    OpenAIAccountModelHealthState = "failing"
	OpenAIAccountModelHealthCooling    OpenAIAccountModelHealthState = "cooling"
	OpenAIAccountModelHealthRecovering OpenAIAccountModelHealthState = "recovering"
)

// OpenAIAccountModelHealth 为管理端提供不含凭据和上游正文的只读健康摘要。
// 最近成功为空代表“当前进程尚无成功证据”，而不是该模型一定不可用。
type OpenAIAccountModelHealth struct {
	AccountID           int64                         `json:"account_id"`
	Model               string                        `json:"model"`
	State               OpenAIAccountModelHealthState `json:"state"`
	ConsecutiveFailures int                           `json:"consecutive_failures"`
	LastSuccessAt       *time.Time                    `json:"last_success_at,omitempty"`
	LastFailureAt       *time.Time                    `json:"last_failure_at,omitempty"`
	CooldownUntil       *time.Time                    `json:"cooldown_until,omitempty"`
	ProbeUntil          *time.Time                    `json:"probe_until,omitempty"`
}

// openAIAccountModelTransientState keeps very short model-scoped cooldowns in
// process memory. These failures must not be persisted as account health: a
// single overloaded upstream model must not evict an otherwise healthy account.
// The state is deliberately bounded and fail-open, so Redis/cache outages cannot
// turn a transient upstream problem into a gateway-wide scheduling outage.
type openAIAccountModelTransientState struct {
	mu         sync.Mutex
	entries    map[openAIAccountModelKey]openAIAccountModelTransientEntry
	maxEntries int
}

func newOpenAIAccountModelTransientState(maxEntries int) *openAIAccountModelTransientState {
	if maxEntries <= 0 {
		maxEntries = openAIModelTransientDefaultMax
	}
	return &openAIAccountModelTransientState{
		entries:    make(map[openAIAccountModelKey]openAIAccountModelTransientEntry),
		maxEntries: maxEntries,
	}
}

func normalizeOpenAIAccountModelTransientModel(model string) string {
	model = strings.TrimSpace(model)
	if len(model) > openAIModelTransientMaxModelBytes {
		return ""
	}
	return strings.ToLower(model)
}

func openAIAccountModelTransientKey(accountID int64, model string) (openAIAccountModelKey, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if accountID <= 0 || model == "" {
		return openAIAccountModelKey{}, false
	}
	return openAIAccountModelKey{AccountID: accountID, Model: model}, true
}

func (s *openAIAccountModelTransientState) recordFailure(accountID int64, model string, now time.Time) openAIAccountModelTransientDecision {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return openAIAccountModelTransientDecision{}
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIAccountModelKey]openAIAccountModelTransientEntry)
	}
	if s.maxEntries <= 0 {
		s.maxEntries = openAIModelTransientDefaultMax
	}

	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	if !exists || entryExpired(entry, now) || entry.lastFailure.IsZero() || now.Sub(entry.lastFailure) > openAIModelTransientStreakTTL || now.Before(entry.lastFailure) {
		entry.failureStreak = 0
		entry.blockUntil = time.Time{}
		entry.probeUntil = time.Time{}
	}
	entry.failureStreak++
	entry.lastFailure = now
	entry.lastTouched = now
	entry.probeUntil = time.Time{}

	cooldown := time.Duration(0)
	switch {
	case entry.failureStreak >= 3:
		cooldown = openAIModelTransientLongCooldown
	case entry.failureStreak == 2:
		cooldown = openAIModelTransientShortCooldown
	}
	if cooldown > 0 {
		entry.blockUntil = now.Add(cooldown)
	} else {
		entry.blockUntil = time.Time{}
	}
	s.entries[key] = entry
	return openAIAccountModelTransientDecision{
		FailureStreak: entry.failureStreak,
		Cooldown:      cooldown,
		BlockUntil:    entry.blockUntil,
	}
}

// blockImmediately records an explicit provider capacity window without
// waiting for the generic two-failure streak. Grok returns model-scoped
// engine_overloaded signals, so keeping this state on account+model avoids
// removing healthy sibling models from scheduling.
func (s *openAIAccountModelTransientState) blockImmediately(accountID int64, model string, now time.Time, cooldown time.Duration) openAIAccountModelTransientDecision {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok || cooldown <= 0 {
		return openAIAccountModelTransientDecision{}
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIAccountModelKey]openAIAccountModelTransientEntry)
	}
	if s.maxEntries <= 0 {
		s.maxEntries = openAIModelTransientDefaultMax
	}
	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	blockUntil := now.Add(cooldown)
	if entry.blockUntil.After(blockUntil) {
		blockUntil = entry.blockUntil
	}
	if entry.failureStreak < 1 {
		entry.failureStreak = 1
	}
	entry.lastFailure = now
	entry.lastTouched = now
	entry.blockUntil = blockUntil
	entry.probeUntil = time.Time{}
	s.entries[key] = entry
	return openAIAccountModelTransientDecision{
		FailureStreak: entry.failureStreak,
		Cooldown:      blockUntil.Sub(now),
		BlockUntil:    blockUntil,
	}
}

func (s *openAIAccountModelTransientState) recordSuccess(accountID int64, model string, observedAt ...time.Time) {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return
	}
	now := time.Time{}
	if len(observedAt) > 0 {
		now = observedAt[0]
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[openAIAccountModelKey]openAIAccountModelTransientEntry)
	}
	if s.maxEntries <= 0 {
		s.maxEntries = openAIModelTransientDefaultMax
	}
	entry, exists := s.entries[key]
	if !exists {
		s.evictOldestLocked()
	}
	entry.failureStreak = 0
	entry.lastSuccess = now
	entry.blockUntil = time.Time{}
	entry.probeUntil = time.Time{}
	entry.lastTouched = now
	s.entries[key] = entry
}

func (s *openAIAccountModelTransientState) isBlocked(accountID int64, model string, now time.Time) bool {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return false
	}
	if entryExpired(entry, now) {
		delete(s.entries, key)
		return false
	}
	if !entry.probeUntil.IsZero() && !now.Before(entry.probeUntil) {
		entry.probeUntil = time.Time{}
	}
	entry.lastTouched = now
	s.entries[key] = entry
	return !entry.blockUntil.IsZero() && now.Before(entry.blockUntil)
}

// tryAcquireRecoveryProbe atomically grants the first request after a model
// cooldown a temporary probe lease. Normal models and first failures do not
// need a probe; only a model that actually entered cooldown is serialized.
func (s *openAIAccountModelTransientState) tryAcquireRecoveryProbe(accountID int64, model string, now time.Time) bool {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return true
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return true
	}
	if entryExpired(entry, now) {
		delete(s.entries, key)
		return true
	}
	if !entry.blockUntil.IsZero() && now.Before(entry.blockUntil) {
		return false
	}
	if entry.failureStreak < 2 {
		return true
	}
	if !entry.probeUntil.IsZero() && now.Before(entry.probeUntil) {
		return false
	}
	entry.probeUntil = now.Add(openAIModelTransientProbeTTL)
	entry.lastTouched = now
	s.entries[key] = entry
	return true
}

// requiresRecoveryProbe reports whether this account+model has completed a
// cooldown and therefore may only proceed through the single recovery-probe
// path. Queueing such a request would let several waiters reach upstream after
// one slot becomes free, defeating the half-open protection.
func (s *openAIAccountModelTransientState) requiresRecoveryProbe(accountID int64, model string, now time.Time) bool {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return false
	}
	if entryExpired(entry, now) {
		delete(s.entries, key)
		return false
	}
	entry.lastTouched = now
	s.entries[key] = entry
	return entry.failureStreak >= 2 && (entry.blockUntil.IsZero() || !now.Before(entry.blockUntil))
}

func (s *openAIAccountModelTransientState) releaseRecoveryProbe(accountID int64, model string) {
	key, ok := openAIAccountModelTransientKey(accountID, model)
	if s == nil || !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return
	}
	entry.probeUntil = time.Time{}
	entry.lastTouched = time.Now()
	s.entries[key] = entry
}

func (s *openAIAccountModelTransientState) snapshotForAccount(accountID int64, now time.Time) []OpenAIAccountModelHealth {
	if s == nil || accountID <= 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]OpenAIAccountModelHealth, 0)
	for key, entry := range s.entries {
		if key.AccountID != accountID {
			continue
		}
		if entryExpired(entry, now) {
			delete(s.entries, key)
			continue
		}
		if !entry.probeUntil.IsZero() && !now.Before(entry.probeUntil) {
			entry.probeUntil = time.Time{}
			s.entries[key] = entry
		}
		health := OpenAIAccountModelHealth{
			AccountID:           key.AccountID,
			Model:               key.Model,
			State:               OpenAIAccountModelHealthHealthy,
			ConsecutiveFailures: entry.failureStreak,
		}
		if !entry.lastSuccess.IsZero() {
			value := entry.lastSuccess
			health.LastSuccessAt = &value
		}
		if !entry.lastFailure.IsZero() {
			value := entry.lastFailure
			health.LastFailureAt = &value
		}
		if !entry.blockUntil.IsZero() && now.Before(entry.blockUntil) {
			value := entry.blockUntil
			health.CooldownUntil = &value
			health.State = OpenAIAccountModelHealthCooling
		} else if !entry.probeUntil.IsZero() && now.Before(entry.probeUntil) {
			value := entry.probeUntil
			health.ProbeUntil = &value
			health.State = OpenAIAccountModelHealthRecovering
		} else if entry.failureStreak > 0 {
			health.State = OpenAIAccountModelHealthFailing
		}
		result = append(result, health)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Model < result[j].Model
	})
	return result
}

func (s *openAIAccountModelTransientState) size() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *openAIAccountModelTransientState) evictOldestLocked() {
	if len(s.entries) < s.maxEntries {
		return
	}
	var oldestKey openAIAccountModelKey
	var oldestTime time.Time
	found := false
	for key, entry := range s.entries {
		if !found || entry.lastTouched.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastTouched
			found = true
		}
	}
	if found {
		delete(s.entries, oldestKey)
	}
}

func entryExpired(entry openAIAccountModelTransientEntry, now time.Time) bool {
	lastEvent := entry.lastSuccess
	if entry.lastFailure.After(lastEvent) {
		lastEvent = entry.lastFailure
	}
	return !lastEvent.IsZero() && now.Sub(lastEvent) > openAIModelTransientStreakTTL
}
