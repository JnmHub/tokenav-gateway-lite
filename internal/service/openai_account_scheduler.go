package service

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	mathrand "math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	openAIAccountScheduleLayerPreviousResponse = "previous_response_id"
	openAIAccountScheduleLayerSessionSticky    = "session_hash"
	openAIAccountScheduleLayerLoadBalance      = "load_balance"
	openAIAccountScheduleLayerGroupRandom      = "group_random"
	openAIAdvancedSchedulerSettingKey          = SettingKeyOpenAIAdvancedSchedulerEnabled
)

// ErrKeywordProtectedPreviousResponseAccount keeps a Responses chain on its
// original account. Silently choosing another account would send a foreign
// previous_response_id upstream and corrupt the conversation boundary.
var ErrKeywordProtectedPreviousResponseAccount = errors.New("previous_response_id is bound to a keyword-protected account; reconnect without the previous response")

const (
	openAIAdvancedSchedulerSettingCacheTTL  = 5 * time.Second
	openAIAdvancedSchedulerSettingDBTimeout = 2 * time.Second
)

type cachedOpenAIAdvancedSchedulerSetting struct {
	enabled   bool
	expiresAt int64
}

var openAIAdvancedSchedulerSettingCache atomic.Value // *cachedOpenAIAdvancedSchedulerSetting
var openAIAdvancedSchedulerSettingSF singleflight.Group

type OpenAIAccountScheduleRequest struct {
	GroupID                 *int64
	Platform                string
	SessionHash             string
	StickyAccountID         int64
	PreserveStickyBinding   bool
	PreviousResponseID      string
	RequestedModel          string
	RequiredImageModel      string
	RequiredTransport       OpenAIUpstreamTransport
	RequiredCapability      OpenAIEndpointCapability
	RequiredImageCapability OpenAIImagesCapability
	RequireCompact          bool
	ExcludedIDs             map[int64]struct{}
	// RuntimePolicy 在请求开始时冻结；调度过程中不得再次读取设置或数据库。
	RuntimePolicy  *OpenAISchedulerRuntimePolicy
	SchedulingMode string
}

type OpenAISchedulerScoreBreakdown struct {
	Total            float64 `json:"total"`
	Priority         float64 `json:"priority"`
	Load             float64 `json:"load"`
	Queue            float64 `json:"queue"`
	ErrorRate        float64 `json:"error_rate"`
	TTFT             float64 `json:"ttft"`
	SoonestReset     float64 `json:"soonest_reset"`
	QuotaHeadroom    float64 `json:"quota_headroom"`
	UpstreamCost     float64 `json:"upstream_cost"`
	SessionSticky    float64 `json:"session_sticky"`
	SubscriptionPool float64 `json:"subscription_pool"`
}

type OpenAIAccountScheduleDecision struct {
	Layer               string
	StickyPreviousHit   bool
	StickySessionHit    bool
	CandidateCount      int
	TopK                int
	LatencyMs           int64
	LoadSkew            float64
	SelectedAccountID   int64
	SelectedAccountType string
	PolicyRevision      int64
	StickySessionSoft   bool
	Score               *OpenAISchedulerScoreBreakdown
}

type OpenAIAccountSchedulerMetricsSnapshot struct {
	SelectTotal              int64
	StickyPreviousHitTotal   int64
	StickySessionHitTotal    int64
	LoadBalanceSelectTotal   int64
	AccountSwitchTotal       int64
	SchedulerLatencyMsTotal  int64
	SchedulerLatencyMsAvg    float64
	StickyHitRatio           float64
	AccountSwitchRate        float64
	LoadSkewAvg              float64
	RuntimeStatsAccountCount int
}

type OpenAIAccountScheduler interface {
	Select(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error)
	ReportResult(accountID int64, success bool, firstTokenMs *int)
	ReportSwitch()
	SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot
}

type openAIAccountSchedulerMetrics struct {
	selectTotal            atomic.Int64
	stickyPreviousHitTotal atomic.Int64
	stickySessionHitTotal  atomic.Int64
	loadBalanceSelectTotal atomic.Int64
	accountSwitchTotal     atomic.Int64
	latencyMsTotal         atomic.Int64
	loadSkewMilliTotal     atomic.Int64
}

type openAIAccountLoadPlan struct {
	allCandidates             []openAIAccountCandidateScore
	candidates                []openAIAccountCandidateScore
	staleSnapshotCompactRetry []openAIAccountCandidateScore
	selectionOrder            []openAIAccountCandidateScore
	candidateCount            int
	topK                      int
	loadSkew                  float64
}

func (m *openAIAccountSchedulerMetrics) recordSelect(decision OpenAIAccountScheduleDecision) {
	if m == nil {
		return
	}
	m.selectTotal.Add(1)
	m.latencyMsTotal.Add(decision.LatencyMs)
	m.loadSkewMilliTotal.Add(int64(math.Round(decision.LoadSkew * 1000)))
	if decision.StickyPreviousHit {
		m.stickyPreviousHitTotal.Add(1)
	}
	if decision.StickySessionHit {
		m.stickySessionHitTotal.Add(1)
	}
	if decision.Layer == openAIAccountScheduleLayerLoadBalance {
		m.loadBalanceSelectTotal.Add(1)
	}
}

func (m *openAIAccountSchedulerMetrics) recordSwitch() {
	if m == nil {
		return
	}
	m.accountSwitchTotal.Add(1)
}

type openAIAccountRuntimeStats struct {
	accounts     sync.Map
	accountCount atomic.Int64
}

type openAIAccountRuntimeStat struct {
	errorRateEWMABits atomic.Uint64
	ttftEWMABits      atomic.Uint64
}

func newOpenAIAccountRuntimeStats() *openAIAccountRuntimeStats {
	return &openAIAccountRuntimeStats{}
}

func (s *openAIAccountRuntimeStats) loadOrCreate(accountID int64) *openAIAccountRuntimeStat {
	if value, ok := s.accounts.Load(accountID); ok {
		stat, _ := value.(*openAIAccountRuntimeStat)
		if stat != nil {
			return stat
		}
	}

	stat := &openAIAccountRuntimeStat{}
	stat.ttftEWMABits.Store(math.Float64bits(math.NaN()))
	actual, loaded := s.accounts.LoadOrStore(accountID, stat)
	if !loaded {
		s.accountCount.Add(1)
		return stat
	}
	existing, _ := actual.(*openAIAccountRuntimeStat)
	if existing != nil {
		return existing
	}
	return stat
}

func updateEWMAAtomic(target *atomic.Uint64, sample float64, alpha float64) {
	for {
		oldBits := target.Load()
		oldValue := math.Float64frombits(oldBits)
		newValue := alpha*sample + (1-alpha)*oldValue
		if target.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
			return
		}
	}
}

func (s *openAIAccountRuntimeStats) report(accountID int64, success bool, firstTokenMs *int) {
	if s == nil || accountID <= 0 {
		return
	}
	const alpha = 0.2
	stat := s.loadOrCreate(accountID)

	errorSample := 1.0
	if success {
		errorSample = 0.0
	}
	updateEWMAAtomic(&stat.errorRateEWMABits, errorSample, alpha)

	if firstTokenMs != nil && *firstTokenMs > 0 {
		ttft := float64(*firstTokenMs)
		ttftBits := math.Float64bits(ttft)
		for {
			oldBits := stat.ttftEWMABits.Load()
			oldValue := math.Float64frombits(oldBits)
			if math.IsNaN(oldValue) {
				if stat.ttftEWMABits.CompareAndSwap(oldBits, ttftBits) {
					break
				}
				continue
			}
			newValue := alpha*ttft + (1-alpha)*oldValue
			if stat.ttftEWMABits.CompareAndSwap(oldBits, math.Float64bits(newValue)) {
				break
			}
		}
	}
}

func (s *openAIAccountRuntimeStats) snapshot(accountID int64) (errorRate float64, ttft float64, hasTTFT bool) {
	if s == nil || accountID <= 0 {
		return 0, 0, false
	}
	value, ok := s.accounts.Load(accountID)
	if !ok {
		return 0, 0, false
	}
	stat, _ := value.(*openAIAccountRuntimeStat)
	if stat == nil {
		return 0, 0, false
	}
	errorRate = clamp01(math.Float64frombits(stat.errorRateEWMABits.Load()))
	ttftValue := math.Float64frombits(stat.ttftEWMABits.Load())
	if math.IsNaN(ttftValue) {
		return errorRate, 0, false
	}
	return errorRate, ttftValue, true
}

func (s *openAIAccountRuntimeStats) size() int {
	if s == nil {
		return 0
	}
	return int(s.accountCount.Load())
}

type defaultOpenAIAccountScheduler struct {
	service                *OpenAIGatewayService
	metrics                openAIAccountSchedulerMetrics
	stats                  *openAIAccountRuntimeStats
	grokFreeQuotaGateCache sync.Map // key: int64(accountID), value: grokFreeQuotaGateCacheEntry
}

type openAIStickyEscapeConfig struct {
	enabled   bool
	ttftMs    float64
	errorRate float64
}

func newDefaultOpenAIAccountScheduler(service *OpenAIGatewayService, stats *openAIAccountRuntimeStats) OpenAIAccountScheduler {
	if stats == nil {
		stats = newOpenAIAccountRuntimeStats()
	}
	return &defaultOpenAIAccountScheduler{
		service: service,
		stats:   stats,
	}
}

func (s *defaultOpenAIAccountScheduler) Select(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{}
	policy := req.RuntimePolicy
	if policy == nil {
		policy = s.service.currentOpenAISchedulerRuntimePolicy(ctx)
		req.RuntimePolicy = policy
	}
	if policy != nil {
		decision.PolicyRevision = policy.Revision
	}
	start := time.Now()
	defer func() {
		decision.LatencyMs = time.Since(start).Milliseconds()
		s.metrics.recordSelect(decision)
	}()

	previousResponseID := strings.TrimSpace(req.PreviousResponseID)
	// previous_response_id（上次响应编号）只属于 OpenAI 自身的续链状态。
	// Grok 使用 HTTP/SSE 兼容转发，不能拿该编号反查或粘到 OpenAI 账号。
	if shouldUseOpenAIPreviousResponseBinding(req) {
		selection, err := s.service.selectAccountByPreviousResponseIDForCapability(
			ctx,
			req.GroupID,
			previousResponseID,
			req.RequestedModel,
			req.ExcludedIDs,
			req.RequiredCapability,
			req.RequireCompact,
		)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			if !s.isAccountTransportCompatible(selection.Account, req.RequiredTransport) {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				selection = nil
			}
		}
		if selection != nil && selection.Account != nil {
			decision.Layer = openAIAccountScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			if req.SessionHash != "" {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selection.Account.ID)
			}
			return selection, decision, nil
		}
	}

	if req.SchedulingMode == GroupSchedulingModeStrictPriority {
		// strict_priority（严格优先级）下，普通 session_hash 不能把请求固定在
		// 较低优先级层。它只作为当前层内的首选；previous_response_id 仍在
		// 上方作为协议强绑定优先处理。
		if req.StickyAccountID <= 0 && strings.TrimSpace(req.SessionHash) != "" && s.service != nil {
			if accountID, lookupErr := s.service.getStickySessionAccountID(ctx, req.GroupID, req.SessionHash); lookupErr == nil {
				req.StickyAccountID = accountID
			}
		}
		if req.StickyAccountID > 0 && s.service != nil {
			if _, _, _, shouldEscape := s.shouldEscapeStickyAccount(req.StickyAccountID, s.service.openAIStickyEscapeConfig()); shouldEscape {
				req.PreserveStickyBinding = true
				req.StickyAccountID = 0
			}
		}
	} else if req.SchedulingMode != GroupSchedulingModeRandom && (policy == nil || !policy.StickySoftScoringEnabled) {
		selection, escapedSticky, err := s.selectBySessionHash(ctx, req)
		if err != nil {
			return nil, decision, err
		}
		if selection != nil && selection.Account != nil {
			decision.Layer = openAIAccountScheduleLayerSessionSticky
			decision.StickySessionHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			return selection, decision, nil
		}
		if escapedSticky {
			req.PreserveStickyBinding = true
		}
	} else if req.SchedulingMode != GroupSchedulingModeRandom {
		// 实验模式不直接占用粘性账号槽位，只把仍健康的绑定带入候选打分。
		// 慢账号/高错误账号先逃逸，保证粘性加分永远不能压过健康保护。
		if req.StickyAccountID <= 0 && strings.TrimSpace(req.SessionHash) != "" && s.service != nil {
			if accountID, lookupErr := s.service.getStickySessionAccountID(ctx, req.GroupID, req.SessionHash); lookupErr == nil {
				req.StickyAccountID = accountID
			}
		}
		if req.StickyAccountID > 0 && s.service != nil {
			if _, _, _, shouldEscape := s.shouldEscapeStickyAccount(req.StickyAccountID, s.service.openAIStickyEscapeConfig()); shouldEscape {
				req.PreserveStickyBinding = true
				req.StickyAccountID = 0
			}
		}
	}

	selection, candidateCount, topK, loadSkew, err := s.selectByLoadBalance(ctx, req)
	if req.SchedulingMode == GroupSchedulingModeRandom {
		decision.Layer = openAIAccountScheduleLayerGroupRandom
	} else {
		decision.Layer = openAIAccountScheduleLayerLoadBalance
	}
	decision.CandidateCount = candidateCount
	decision.TopK = topK
	decision.LoadSkew = loadSkew
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		decision.Score = selection.SchedulerScore
		if selection.SchedulerScore != nil && selection.SchedulerScore.SessionSticky > 0 {
			decision.StickySessionHit = true
			decision.StickySessionSoft = true
		}
	}
	return selection, decision, nil
}

func shouldUseOpenAIPreviousResponseBinding(req OpenAIAccountScheduleRequest) bool {
	return strings.TrimSpace(req.PreviousResponseID) != "" &&
		req.SchedulingMode != GroupSchedulingModeRandom &&
		normalizeOpenAICompatiblePlatform(req.Platform) == PlatformOpenAI
}

func (s *defaultOpenAIAccountScheduler) selectBySessionHash(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, bool, error) {
	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" || s == nil || s.service == nil || s.service.cache == nil {
		return nil, false, nil
	}

	accountID := req.StickyAccountID
	if accountID <= 0 {
		var err error
		accountID, err = s.service.getStickySessionAccountID(ctx, req.GroupID, sessionHash)
		if err != nil || accountID <= 0 {
			return nil, false, nil
		}
	}
	if accountID <= 0 {
		return nil, false, nil
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[accountID]; excluded {
			return nil, false, nil
		}
	}

	accountResolveStart := time.Now()
	account, err := s.service.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, nil
	}
	if shouldClearStickySession(account, req.RequestedModel) || account.Platform != normalizeOpenAICompatiblePlatform(req.Platform) || !account.IsOpenAICompatible() || !account.IsSchedulable() {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, nil
	}
	if !openAIAccountMatchesLongContextRouting(ctx, account) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, nil
	}
	if !s.isAccountRequestCompatible(ctx, account, req) {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, nil
	}
	if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, nil
	}
	account = s.service.recheckSelectedOpenAIAccountFromDBForGroup(ctx, account, req.GroupID, req.RequestedModel, req.RequireCompact, req.RequiredCapability, req.Platform)
	if account == nil || !openAIStickyAccountMatchesGroup(account, req.GroupID) || !s.isAccountTransportCompatible(account, req.RequiredTransport) {
		_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, nil
	}
	if normalizeOpenAICompatiblePlatform(req.Platform) == PlatformGrok {
		// 粘性会话也必须服从 Grok 免费档与团队模型冷却，避免绕过正常候选池保护。
		if len(s.filterGrokFreeQuotaAccounts(ctx, []Account{*account})) == 0 {
			_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
			AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
			return nil, false, nil
		}
		if len(filterGrokTeamModelRateLimitedAccountsWithContext(ctx, []Account{*account}, req.RequestedModel, time.Now())) == 0 {
			_ = s.service.deleteStickySessionAccountID(ctx, req.GroupID, sessionHash)
			AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
			return nil, false, nil
		}
	}
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
	escapeCfg := s.service.openAIStickyEscapeConfig()
	if reason, errorRate, ttft, shouldEscape := s.shouldEscapeStickyAccount(accountID, escapeCfg); shouldEscape {
		slog.Info("sticky_escape_triggered",
			"account_id", accountID,
			"reason", reason,
			"error_rate", errorRate,
			"ttft", ttft,
		)
		return nil, true, nil
	}
	// 会话粘性不能绕过账号+实际上游模型的 half-open（半开）探测。
	// 恢复探测只允许立即抢到账号槽位的一个请求继续；若账号繁忙，不能把
	// 多个等待请求堆到同一模型上，否则槽位释放后会同时冲击刚恢复的上游。
	recoveryProbeRequired := s.service.requiresOpenAIAccountModelRecoveryProbeForGroup(account, req.GroupID, req.RequestedModel)
	if !s.service.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(account, req.GroupID, req.RequestedModel) {
		return nil, true, nil
	}
	accountSlotStart := time.Now()
	result, acquireErr := s.service.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountSlot, time.Since(accountSlotStart))
	if acquireErr == nil && result != nil && result.Acquired {
		// Tail-Burst entry is a durable accounting boundary.  Commit it only
		// after the sticky account slot is held; a failed admission must release
		// both leases and let normal scheduling try another candidate.
		if s.service.rateLimitService != nil && !s.service.rateLimitService.admitCodexTailBurstAfterSlot(ctx, account) {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, req.GroupID, req.RequestedModel)
			return nil, true, nil
		}
		stickyRefreshStart := time.Now()
		_ = s.service.refreshStickySessionTTL(ctx, req.GroupID, sessionHash, s.service.openAIWSSessionStickyTTL())
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageStickyRefresh, time.Since(stickyRefreshStart))
		return &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}, false, nil
	}
	// 未实际占用账号槽位时，探测尚未到达上游，必须立即归还。对于
	// recovery（恢复）状态，直接跳过等待队列并回退普通候选池。
	s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, req.GroupID, req.RequestedModel)
	if recoveryProbeRequired {
		return nil, true, nil
	}

	cfg := s.service.schedulingConfig(ctx)
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	if s.service.concurrencyService != nil {
		if escapeCfg.enabled && acquireErr == nil && result != nil && !result.Acquired {
			errorRate, ttft, _ := s.stats.snapshot(accountID)
			slog.Info("sticky_escape_triggered",
				"account_id", accountID,
				"reason", "concurrency_full",
				"error_rate", errorRate,
				"ttft", ttft,
			)
			return nil, true, nil
		}
		// WaitPlan（等待计划）唤醒时只负责重新获取账号槽位，无法携带
		// Tail-Burst 的持久化准入边界。超额候选不能排队，立即回到普通
		// 候选池，避免唤醒后出现未记账放行。
		if s.service.rateLimitService != nil && s.service.rateLimitService.codexTailBurstEligible(ctx, account) {
			return nil, true, nil
		}
		return &AccountSelectionResult{
			Account: account,
			WaitPlan: &AccountWaitPlan{
				AccountID:      accountID,
				MaxConcurrency: account.Concurrency,
				Timeout:        cfg.StickySessionWaitTimeout,
				MaxWaiting:     cfg.StickySessionMaxWaiting,
			},
		}, false, nil
	}
	return nil, false, nil
}

func openAIStickyAccountMatchesGroup(account *Account, groupID *int64) bool {
	if account == nil {
		return false
	}
	if groupID == nil {
		return len(account.AccountGroups) == 0 && len(account.GroupIDs) == 0
	}
	for _, accountGroupID := range account.GroupIDs {
		if accountGroupID == *groupID {
			return true
		}
	}
	for _, accountGroup := range account.AccountGroups {
		if accountGroup.GroupID == *groupID {
			return true
		}
	}
	return false
}

func (s *defaultOpenAIAccountScheduler) shouldEscapeStickyAccount(accountID int64, cfg openAIStickyEscapeConfig) (reason string, errorRate float64, ttft float64, shouldEscape bool) {
	if !cfg.enabled || s == nil || s.stats == nil || accountID <= 0 {
		return "", 0, 0, false
	}
	errorRate, ttft, hasTTFT := s.stats.snapshot(accountID)
	if hasTTFT && ttft > cfg.ttftMs {
		return "ttft", errorRate, ttft, true
	}
	if errorRate > cfg.errorRate {
		return "error_rate", errorRate, ttft, true
	}
	return "", errorRate, ttft, false
}

type openAIAccountCandidateScore struct {
	account  *Account
	loadInfo *AccountLoadInfo
	// schedulingPriority 统一来自账号全局 Priority；分组只限定候选范围。
	schedulingPriority    int
	schedulingPrioritySet bool
	score                 float64
	errorRate             float64
	ttft                  float64
	hasTTFT               bool
	upstreamCostFactor    float64
	quotaHeadroomFactor   float64
	soonestResetFactor    float64
	upstreamCostValue     float64
	upstreamCostKnown     bool
	soonestResetAt        time.Time
	soonestResetKnown     bool
	breakdown             OpenAISchedulerScoreBreakdown
}

func openAIAccountCandidateSchedulingPriority(candidate openAIAccountCandidateScore) int {
	if candidate.schedulingPrioritySet {
		return candidate.schedulingPriority
	}
	if candidate.account == nil {
		return 0
	}
	return candidate.account.Priority
}

type openAIAccountCandidateHeap []openAIAccountCandidateScore

func (h openAIAccountCandidateHeap) Len() int {
	return len(h)
}

func (h openAIAccountCandidateHeap) Less(i, j int) bool {
	// 最小堆根节点保存“最差”候选，便于 O(log k) 维护 topK。
	return isOpenAIAccountCandidateBetter(h[j], h[i])
}

func (h openAIAccountCandidateHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *openAIAccountCandidateHeap) Push(x any) {
	candidate, ok := x.(openAIAccountCandidateScore)
	if !ok {
		panic("openAIAccountCandidateHeap: invalid element type")
	}
	*h = append(*h, candidate)
}

func (h *openAIAccountCandidateHeap) Pop() any {
	old := *h
	n := len(old)
	last := old[n-1]
	*h = old[:n-1]
	return last
}

func isOpenAIAccountCandidateBetter(left openAIAccountCandidateScore, right openAIAccountCandidateScore) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	leftPriority := openAIAccountCandidateSchedulingPriority(left)
	rightPriority := openAIAccountCandidateSchedulingPriority(right)
	if leftPriority != rightPriority {
		return leftPriority < rightPriority
	}
	if left.loadInfo.LoadRate != right.loadInfo.LoadRate {
		return left.loadInfo.LoadRate < right.loadInfo.LoadRate
	}
	if left.loadInfo.WaitingCount != right.loadInfo.WaitingCount {
		return left.loadInfo.WaitingCount < right.loadInfo.WaitingCount
	}
	return left.account.ID < right.account.ID
}

func isOpenAISubscriptionAccount(account *Account) bool {
	if account == nil || !account.IsOAuth() || account.Credentials == nil {
		return false
	}
	planType, _ := account.Credentials["plan_type"].(string)
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "", "free", "unknown", "none":
		return false
	default:
		// OAuth 的非免费明确档位（Plus/Pro/Team/Business/Enterprise 等）
		// 才属于订阅池；未知空值不能被猜成付费账号。
		return true
	}
}

func openAISchedulerScorePointer(score OpenAISchedulerScoreBreakdown) *OpenAISchedulerScoreBreakdown {
	copy := score
	return &copy
}

func selectTopKOpenAICandidates(candidates []openAIAccountCandidateScore, topK int) []openAIAccountCandidateScore {
	if len(candidates) == 0 {
		return nil
	}
	if topK <= 0 {
		topK = 1
	}
	if topK >= len(candidates) {
		ranked := append([]openAIAccountCandidateScore(nil), candidates...)
		sort.Slice(ranked, func(i, j int) bool {
			return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
		})
		return ranked
	}

	best := make(openAIAccountCandidateHeap, 0, topK)
	for _, candidate := range candidates {
		if len(best) < topK {
			heap.Push(&best, candidate)
			continue
		}
		if isOpenAIAccountCandidateBetter(candidate, best[0]) {
			best[0] = candidate
			heap.Fix(&best, 0)
		}
	}

	ranked := make([]openAIAccountCandidateScore, len(best))
	copy(ranked, best)
	sort.Slice(ranked, func(i, j int) bool {
		return isOpenAIAccountCandidateBetter(ranked[i], ranked[j])
	})
	return ranked
}

type openAISelectionRNG struct {
	state uint64
}

func newOpenAISelectionRNG(seed uint64) openAISelectionRNG {
	if seed == 0 {
		seed = 0x9e3779b97f4a7c15
	}
	return openAISelectionRNG{state: seed}
}

func (r *openAISelectionRNG) nextUint64() uint64 {
	// xorshift64*
	x := r.state
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	r.state = x
	return x * 2685821657736338717
}

func (r *openAISelectionRNG) nextFloat64() float64 {
	// [0,1)
	return float64(r.nextUint64()>>11) / (1 << 53)
}

func deriveOpenAISelectionSeed(req OpenAIAccountScheduleRequest) uint64 {
	hasher := fnv.New64a()
	writeValue := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		_, _ = hasher.Write([]byte(trimmed))
		_, _ = hasher.Write([]byte{0})
	}

	writeValue(req.SessionHash)
	writeValue(req.PreviousResponseID)
	writeValue(req.RequestedModel)
	if req.GroupID != nil {
		_, _ = hasher.Write([]byte(strconv.FormatInt(*req.GroupID, 10)))
	}

	seed := hasher.Sum64()
	// 对“无会话锚点”的纯负载均衡请求引入时间熵，避免固定命中同一账号。
	if strings.TrimSpace(req.SessionHash) == "" && strings.TrimSpace(req.PreviousResponseID) == "" {
		seed ^= uint64(time.Now().UnixNano())
	}
	if seed == 0 {
		seed = uint64(time.Now().UnixNano()) ^ 0x9e3779b97f4a7c15
	}
	return seed
}

func buildOpenAIWeightedSelectionOrder(
	candidates []openAIAccountCandidateScore,
	req OpenAIAccountScheduleRequest,
) []openAIAccountCandidateScore {
	if len(candidates) <= 1 {
		return append([]openAIAccountCandidateScore(nil), candidates...)
	}

	pool := append([]openAIAccountCandidateScore(nil), candidates...)
	weights := make([]float64, len(pool))
	minScore := pool[0].score
	for i := 1; i < len(pool); i++ {
		if pool[i].score < minScore {
			minScore = pool[i].score
		}
	}
	for i := range pool {
		// 将 top-K 分值平移到正区间，避免“单一最高分账号”长期垄断。
		weight := (pool[i].score - minScore) + 1.0
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
			weight = 1.0
		}
		weights[i] = weight
	}

	order := make([]openAIAccountCandidateScore, 0, len(pool))
	rng := newOpenAISelectionRNG(deriveOpenAISelectionSeed(req))
	for len(pool) > 0 {
		total := 0.0
		for _, w := range weights {
			total += w
		}

		selectedIdx := 0
		if total > 0 {
			r := rng.nextFloat64() * total
			acc := 0.0
			for i, w := range weights {
				acc += w
				if r <= acc {
					selectedIdx = i
					break
				}
			}
		} else {
			selectedIdx = int(rng.nextUint64() % uint64(len(pool)))
		}

		order = append(order, pool[selectedIdx])
		pool = append(pool[:selectedIdx], pool[selectedIdx+1:]...)
		weights = append(weights[:selectedIdx], weights[selectedIdx+1:]...)
	}
	return order
}

func prioritizeStrictPriorityStickyCandidate(
	selectionOrder []openAIAccountCandidateScore,
	req OpenAIAccountScheduleRequest,
) []openAIAccountCandidateScore {
	if req.StickyAccountID <= 0 || req.PreserveStickyBinding || len(selectionOrder) < 2 {
		return selectionOrder
	}
	stickyIndex := -1
	for index := range selectionOrder {
		if selectionOrder[index].account != nil && selectionOrder[index].account.ID == req.StickyAccountID {
			stickyIndex = index
			break
		}
	}
	if stickyIndex <= 0 {
		return selectionOrder
	}
	sticky := selectionOrder[stickyIndex]
	copy(selectionOrder[1:stickyIndex+1], selectionOrder[:stickyIndex])
	selectionOrder[0] = sticky
	return selectionOrder
}

type openAIStrictPriorityLayer struct {
	priority int
	accounts []*Account
}

const openAIStrictPriorityAcquireBatchSize = 32

func buildOpenAIStrictPriorityLayers(accounts []*Account) []openAIStrictPriorityLayer {
	if len(accounts) == 0 {
		return nil
	}
	byPriority := make(map[int][]*Account)
	priorities := make([]int, 0)
	for _, account := range accounts {
		// 分组只限定哪些账号参与；严格优先级始终使用账号全局优先级，
		// 避免同一账号加入多个分组后出现多套难以理解的调度顺序。
		priority := account.Priority
		if _, exists := byPriority[priority]; !exists {
			priorities = append(priorities, priority)
		}
		byPriority[priority] = append(byPriority[priority], account)
	}
	sort.Ints(priorities)
	layers := make([]openAIStrictPriorityLayer, 0, len(priorities))
	for _, priority := range priorities {
		layers = append(layers, openAIStrictPriorityLayer{
			priority: priority,
			accounts: byPriority[priority],
		})
	}
	return layers
}

func (s *defaultOpenAIAccountScheduler) strictPriorityPlanMetrics(
	req OpenAIAccountScheduleRequest,
	accounts []*Account,
	loadMap map[int64]*AccountLoadInfo,
) (candidateCount, topK int, loadSkew float64, hasSelectionCandidate bool) {
	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	staleCompactCandidates := 0
	for _, account := range accounts {
		if req.RequireCompact && openAICompactSupportTier(account) == 0 {
			staleCompactCandidates++
			continue
		}
		candidateCount++
		loadInfo := loadMap[account.ID]
		if loadInfo == nil {
			loadInfo = &AccountLoadInfo{AccountID: account.ID}
		}
		loadRate := float64(loadInfo.LoadRate)
		loadRateSum += loadRate
		loadRateSumSquares += loadRate * loadRate
	}
	loadSkew = calcLoadSkewByMoments(loadRateSum, loadRateSumSquares, candidateCount)
	if candidateCount > 0 {
		topK = s.service.openAIWSLBTopK()
		if policy := req.RuntimePolicy; policy != nil && policy.Revision > 0 {
			topK = policy.TopK
		}
		if topK > candidateCount {
			topK = candidateCount
		}
		if topK <= 0 {
			topK = 1
		}
	}
	hasSelectionCandidate = candidateCount > 0 ||
		(staleCompactCandidates > 0 && s.service.schedulerSnapshot != nil)
	return candidateCount, topK, loadSkew, hasSelectionCandidate
}

func (s *defaultOpenAIAccountScheduler) buildOpenAIAccountLoadPlan(
	req OpenAIAccountScheduleRequest,
	filtered []*Account,
	loadMap map[int64]*AccountLoadInfo,
) openAIAccountLoadPlan {
	allCandidates := make([]openAIAccountCandidateScore, 0, len(filtered))
	for _, account := range filtered {
		loadInfo := loadMap[account.ID]
		if loadInfo == nil {
			loadInfo = &AccountLoadInfo{AccountID: account.ID}
		}
		errorRate, ttft, hasTTFT := 0.0, 0.0, false
		if s.stats != nil {
			errorRate, ttft, hasTTFT = s.stats.snapshot(account.ID)
		}
		allCandidates = append(allCandidates, openAIAccountCandidateScore{
			account:               account,
			loadInfo:              loadInfo,
			schedulingPriority:    account.Priority,
			schedulingPrioritySet: true,
			errorRate:             errorRate,
			ttft:                  ttft,
			hasTTFT:               hasTTFT,
		})
	}

	candidates := allCandidates
	staleSnapshotCompactRetry := make([]openAIAccountCandidateScore, 0, len(allCandidates))
	if req.RequireCompact {
		candidates = make([]openAIAccountCandidateScore, 0, len(allCandidates))
		for _, candidate := range allCandidates {
			if openAICompactSupportTier(candidate.account) == 0 {
				staleSnapshotCompactRetry = append(staleSnapshotCompactRetry, candidate)
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	plan := openAIAccountLoadPlan{
		allCandidates:             allCandidates,
		candidates:                candidates,
		staleSnapshotCompactRetry: staleSnapshotCompactRetry,
		candidateCount:            len(candidates),
	}
	if len(candidates) == 0 {
		plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
		return plan
	}

	minPriority := openAIAccountCandidateSchedulingPriority(candidates[0])
	maxPriority := minPriority
	maxWaiting := 1
	loadRateSum := 0.0
	loadRateSumSquares := 0.0
	minTTFT, maxTTFT := 0.0, 0.0
	hasTTFTSample := false
	for _, candidate := range candidates {
		candidatePriority := openAIAccountCandidateSchedulingPriority(candidate)
		if candidatePriority < minPriority {
			minPriority = candidatePriority
		}
		if candidatePriority > maxPriority {
			maxPriority = candidatePriority
		}
		if candidate.loadInfo.WaitingCount > maxWaiting {
			maxWaiting = candidate.loadInfo.WaitingCount
		}
		if candidate.hasTTFT && candidate.ttft > 0 {
			if !hasTTFTSample {
				minTTFT, maxTTFT = candidate.ttft, candidate.ttft
				hasTTFTSample = true
			} else {
				if candidate.ttft < minTTFT {
					minTTFT = candidate.ttft
				}
				if candidate.ttft > maxTTFT {
					maxTTFT = candidate.ttft
				}
			}
		}
		loadRate := float64(candidate.loadInfo.LoadRate)
		loadRateSum += loadRate
		loadRateSumSquares += loadRate * loadRate
	}
	plan.loadSkew = calcLoadSkewByMoments(loadRateSum, loadRateSumSquares, len(candidates))

	policy := req.RuntimePolicy
	weights := s.service.openAIWSSchedulerWeights()
	if policy != nil && policy.Revision > 0 {
		// revision=0 代表升级前只有 advanced enabled 旧开关、尚未保存过
		// runtime 策略。此时继续沿用启动配置，避免升级后悄悄改变 Top-K
		// 和权重；管理员首次保存后才由原子快照接管。
		weights = GatewayOpenAIWSSchedulerScoreWeightsView{
			Priority:  policy.PriorityWeight,
			Load:      policy.LoadWeight,
			Queue:     policy.QueueWeight,
			ErrorRate: policy.ErrorRateWeight,
			TTFT:      policy.TTFTWeight,
		}
	}
	for i := range candidates {
		item := &candidates[i]
		priorityFactor := 1.0
		if maxPriority > minPriority {
			priorityFactor = 1 - float64(openAIAccountCandidateSchedulingPriority(*item)-minPriority)/float64(maxPriority-minPriority)
		}
		loadFactor := 1 - clamp01(float64(item.loadInfo.LoadRate)/100.0)
		queueFactor := 1 - clamp01(float64(item.loadInfo.WaitingCount)/float64(maxWaiting))
		errorFactor := 1 - clamp01(item.errorRate)
		ttftFactor := 0.5
		if item.hasTTFT && hasTTFTSample && maxTTFT > minTTFT {
			ttftFactor = 1 - clamp01((item.ttft-minTTFT)/(maxTTFT-minTTFT))
		}

		item.breakdown.Priority = weights.Priority * priorityFactor
		item.breakdown.Load = weights.Load * loadFactor
		item.breakdown.Queue = weights.Queue * queueFactor
		item.breakdown.ErrorRate = weights.ErrorRate * errorFactor
		item.breakdown.TTFT = weights.TTFT * ttftFactor
		item.score = item.breakdown.Priority + item.breakdown.Load + item.breakdown.Queue +
			item.breakdown.ErrorRate + item.breakdown.TTFT
	}
	softSettings := CurrentUpstreamBillingRuntimeSettings()
	if policy != nil {
		softSettings.CostSchedulingEnabled = policy.UpstreamCostEnabled
		softSettings.UpstreamCostWeight = policy.UpstreamCostWeight
		softSettings.QuotaHeadroomWeight = policy.QuotaHeadroomWeight
		softSettings.PreferSoonestResetWeight = policy.PreferSoonestResetWeight
	}
	applyOpenAISchedulerSoftSignals(candidates, softSettings, time.Now())
	for i := range candidates {
		item := &candidates[i]
		if policy != nil && policy.PreferSubscriptionPool && isOpenAISubscriptionAccount(item.account) {
			// 订阅池偏好只做小幅软加分，不排除 API Key/免费账号，也不绕过健康门禁。
			item.breakdown.SubscriptionPool = 0.35
			item.score += item.breakdown.SubscriptionPool
		}
		if policy != nil && policy.StickySoftScoringEnabled && !req.PreserveStickyBinding &&
			req.StickyAccountID > 0 && item.account != nil && item.account.ID == req.StickyAccountID {
			item.breakdown.SessionSticky = policy.SessionStickyWeight
			item.score += item.breakdown.SessionSticky
		}
		item.breakdown.Total = item.score
	}
	plan.candidates = candidates

	plan.topK = s.service.openAIWSLBTopK()
	if policy != nil && policy.Revision > 0 {
		plan.topK = policy.TopK
	}
	if plan.topK > len(candidates) {
		plan.topK = len(candidates)
	}
	if plan.topK <= 0 {
		plan.topK = 1
	}

	plan.selectionOrder = s.buildOpenAISelectionOrder(req, plan)
	if req.SchedulingMode == GroupSchedulingModeStrictPriority {
		plan.selectionOrder = prioritizeStrictPriorityStickyCandidate(plan.selectionOrder, req)
	}
	return plan
}

func (s *defaultOpenAIAccountScheduler) buildOpenAISelectionOrder(
	req OpenAIAccountScheduleRequest,
	plan openAIAccountLoadPlan,
) []openAIAccountCandidateScore {
	if req.SchedulingMode == GroupSchedulingModeRandom {
		// plan.candidates 已完成平台、模型、额度、运行时保护和并发负载门禁。
		// 此处对全部合格账号洗牌，不再使用 TopK、优先级或软粘性分数。
		pool := append([]openAIAccountCandidateScore(nil), plan.candidates...)
		mathrand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		return pool
	}
	buildSelectionOrder := func(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
		if len(pool) == 0 || plan.topK <= 0 {
			return nil
		}
		groupTopK := plan.topK
		if req.SchedulingMode == GroupSchedulingModeStrictPriority {
			// 严格模式必须原子尝试完当前优先级层的所有合格账号，不能因
			// Top-K 截断而在同层仍有空闲账号时提前降到下一层。
			groupTopK = len(pool)
		}
		if groupTopK > len(pool) {
			groupTopK = len(pool)
		}
		ranked := selectTopKOpenAICandidates(pool, groupTopK)
		return buildOpenAIWeightedSelectionOrder(ranked, req)
	}

	if req.RequireCompact {
		supported := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
		unknown := make([]openAIAccountCandidateScore, 0, len(plan.candidates))
		for _, candidate := range plan.candidates {
			switch openAICompactSupportTier(candidate.account) {
			case 2:
				supported = append(supported, candidate)
			case 1:
				unknown = append(unknown, candidate)
			}
		}
		selectionOrder := make([]openAIAccountCandidateScore, 0, len(plan.allCandidates))
		selectionOrder = append(selectionOrder, buildSelectionOrder(supported)...)
		selectionOrder = append(selectionOrder, buildSelectionOrder(unknown)...)
		if len(plan.staleSnapshotCompactRetry) > 0 && s.service.schedulerSnapshot != nil {
			selectionOrder = append(selectionOrder, sortOpenAICompactRetryCandidates(plan.staleSnapshotCompactRetry)...)
		}
		return selectionOrder
	}

	return buildSelectionOrder(plan.candidates)
}

func sortOpenAICompactRetryCandidates(pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
	if len(pool) == 0 {
		return nil
	}
	ordered := append([]openAIAccountCandidateScore(nil), pool...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		aPriority := openAIAccountCandidateSchedulingPriority(a)
		bPriority := openAIAccountCandidateSchedulingPriority(b)
		if aPriority != bPriority {
			return aPriority < bPriority
		}
		if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
			return a.loadInfo.LoadRate < b.loadInfo.LoadRate
		}
		if a.loadInfo.WaitingCount != b.loadInfo.WaitingCount {
			return a.loadInfo.WaitingCount < b.loadInfo.WaitingCount
		}
		switch {
		case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
			return true
		case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
			return false
		case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
			return false
		default:
			return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
		}
	})
	return ordered
}

func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAISelectionOrder(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	selectionOrder []openAIAccountCandidateScore,
) (*AccountSelectionResult, bool, error) {
	compactBlocked := false
	for i := 0; i < len(selectionOrder); i++ {
		candidate := selectionOrder[i]
		fresh := s.service.resolveFreshSchedulableOpenAIAccountForGroup(ctx, candidate.account, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDBForGroup(ctx, fresh, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		// 冷却结束后的账号+模型处于 half-open（半开）状态。只在真正即将
		// 占用账号槽位时竞争一个恢复探测租约，避免并发选号阶段错误地占住租约。
		if !s.service.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel) {
			continue
		}
		result, acquireErr := s.service.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
		if acquireErr != nil {
			s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel)
			return nil, compactBlocked, acquireErr
		}
		if result != nil && result.Acquired {
			if s.service.rateLimitService != nil && !s.service.rateLimitService.admitCodexTailBurstAfterSlot(ctx, fresh) {
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel)
				continue
			}
			if req.SchedulingMode != GroupSchedulingModeRandom && req.SessionHash != "" && !req.PreserveStickyBinding {
				_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, fresh.ID)
			}
			return &AccountSelectionResult{
				Account:        fresh,
				Acquired:       true,
				ReleaseFunc:    result.ReleaseFunc,
				SchedulerScore: openAISchedulerScorePointer(candidate.breakdown),
			}, compactBlocked, nil
		}
		s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel)
	}
	return nil, compactBlocked, nil
}

// tryAcquireOpenAIStrictPriorityLayer 把同一优先级层的普通并发抢槽按固定小批次
// 合并为 Redis（缓存服务）原子操作。half-open（半开恢复）候选仍走旧的单探测
// 路径，避免探测竞争把批量优化退化成 N 次短暂抢槽；普通候选才进入批次。
func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAIStrictPriorityLayer(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	selectionOrder []openAIAccountCandidateScore,
) (*AccountSelectionResult, bool, error) {
	compactBlocked := false
	concurrencyRefreshed := false
	for {
		optimisticOrdinaryTried := false
		for index := 0; index < len(selectionOrder); {
			candidate := selectionOrder[index]
			if candidate.account == nil {
				index++
				continue
			}
			if s.service.requiresOpenAIAccountModelRecoveryProbeForGroup(candidate.account, req.GroupID, req.RequestedModel) {
				selection, probeBlocked, acquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, []openAIAccountCandidateScore{candidate})
				compactBlocked = compactBlocked || probeBlocked
				if acquireErr != nil {
					return nil, compactBlocked, acquireErr
				}
				if selection != nil {
					return selection, compactBlocked, nil
				}
				index++
				continue
			}
			if !optimisticOrdinaryTried {
				// 正常情况下排序首位账号就有容量。先沿用单账号原子脚本，
				// 避免每个健康请求都让 Redis 扫描整批 32 个账号；只有首位
				// 满载或失效后，才启用后续批量抢槽优化。
				optimisticOrdinaryTried = true
				selection, optimisticBlocked, acquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, []openAIAccountCandidateScore{candidate})
				compactBlocked = compactBlocked || optimisticBlocked
				if acquireErr != nil {
					return nil, compactBlocked, acquireErr
				}
				if selection != nil {
					return selection, compactBlocked, nil
				}
				index++
				continue
			}

			batchEnd := index
			for batchEnd < len(selectionOrder) && batchEnd-index < openAIStrictPriorityAcquireBatchSize {
				batchCandidate := selectionOrder[batchEnd]
				if batchCandidate.account == nil || s.service.requiresOpenAIAccountModelRecoveryProbeForGroup(batchCandidate.account, req.GroupID, req.RequestedModel) {
					break
				}
				batchEnd++
			}
			selection, batchCompactBlocked, acquireErr := s.tryAcquireOpenAIStrictPriorityBatch(ctx, req, selectionOrder[index:batchEnd])
			compactBlocked = compactBlocked || batchCompactBlocked
			if acquireErr != nil {
				return nil, compactBlocked, acquireErr
			}
			if selection != nil {
				return selection, compactBlocked, nil
			}
			if batchEnd == index {
				index++
			} else {
				index = batchEnd
			}
		}

		if concurrencyRefreshed || s.service.schedulerSnapshot == nil {
			return nil, compactBlocked, nil
		}
		refreshed, changed, refreshErr := s.refreshOpenAIStrictPriorityConcurrencyLimits(ctx, selectionOrder)
		if refreshErr != nil {
			slog.Warn("strict_priority_concurrency_refresh_failed",
				"group_id", derefGroupID(req.GroupID),
				"candidate_count", len(selectionOrder),
				"error", refreshErr,
			)
			return nil, compactBlocked, nil
		}
		concurrencyRefreshed = true
		if !changed {
			return nil, compactBlocked, nil
		}
		selectionOrder = refreshed
	}
}

func (s *defaultOpenAIAccountScheduler) refreshOpenAIStrictPriorityConcurrencyLimits(
	ctx context.Context,
	selectionOrder []openAIAccountCandidateScore,
) ([]openAIAccountCandidateScore, bool, error) {
	accountIDs := make([]int64, 0, len(selectionOrder))
	for _, candidate := range selectionOrder {
		if candidate.account != nil {
			accountIDs = append(accountIDs, candidate.account.ID)
		}
	}
	limits, err := s.service.schedulerSnapshot.GetAccountConcurrencyLimits(ctx, accountIDs)
	if err != nil {
		return nil, false, err
	}
	refreshed := append([]openAIAccountCandidateScore(nil), selectionOrder...)
	changed := false
	changedAccountIDs := make([]int64, 0)
	for index := range refreshed {
		candidate := refreshed[index]
		if candidate.account == nil {
			continue
		}
		maxConcurrency, ok := limits[candidate.account.ID]
		if !ok || maxConcurrency == candidate.account.Concurrency {
			continue
		}
		fresh := *candidate.account
		fresh.Concurrency = maxConcurrency
		refreshed[index].account = &fresh
		changed = true
		changedAccountIDs = append(changedAccountIDs, candidate.account.ID)
	}
	if changed {
		// 完整账号 L1（进程内缓存）可能仍保留旧并发上限。只在确认变化时失效，
		// 让随后真正选中的账号从 Redis 取得一致的完整配置。
		s.service.schedulerSnapshot.invalidateSchedulerHotAccounts(changedAccountIDs)
	}
	return refreshed, changed, nil
}

// tryAcquireOpenAIStrictPriorityBatch 在一个不超过 32 个普通候选的批次中
// 原子抢占首个可用账号。候选已在入口过滤过；只有真正被脚本选中的账号才
// 做完整快照复核，避免满载账号产生逐个 Redis 读取。
func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAIStrictPriorityBatch(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	selectionOrder []openAIAccountCandidateScore,
) (*AccountSelectionResult, bool, error) {
	compactBlocked := false
	batch := make([]openAIAccountCandidateScore, 0, len(selectionOrder))
	for _, candidate := range selectionOrder {
		if candidate.account != nil {
			batch = append(batch, candidate)
		}
	}

	for len(batch) > 0 {
		slotCandidates := make([]AccountWithConcurrency, len(batch))
		for index, candidate := range batch {
			slotCandidates[index] = AccountWithConcurrency{
				ID:             candidate.account.ID,
				MaxConcurrency: candidate.account.Concurrency,
			}
		}

		selectedAccountID, result, acquireErr := s.service.tryAcquireFirstAvailableAccountSlot(ctx, slotCandidates)
		if acquireErr != nil {
			return nil, compactBlocked, acquireErr
		}
		if result == nil || !result.Acquired {
			break
		}

		selectedIndex := -1
		for index := range batch {
			if batch[index].account.ID == selectedAccountID {
				selectedIndex = index
				break
			}
		}
		if selectedIndex < 0 {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			return nil, compactBlocked, errors.New("batch account slot selection returned an unknown account")
		}

		selected := batch[selectedIndex]
		fresh := s.service.resolveFreshSchedulableOpenAIAccountForGroup(ctx, selected.account, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh != nil {
			fresh = s.service.recheckSelectedOpenAIAccountFromDBForGroup(ctx, fresh, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		}
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
			continue
		}
		// 候选快照的并发上限可能刚被管理员修改。先归还批量槽位，再用
		// 完整快照中的新上限重新抢一次当前账号，避免短暂降到低优先级层。
		if selected.account.Concurrency != fresh.Concurrency {
			oldConcurrency := selected.account.Concurrency
			// 上限变大或变为“不限”时，已经占到的旧槽位仍然合法，
			// 保留它可避免异步释放尚未落 Redis 就被下一次抢槽误判为满。
			keepExistingSlot := oldConcurrency > 0 && (fresh.Concurrency <= 0 || fresh.Concurrency >= oldConcurrency)
			if keepExistingSlot {
				selected.account = fresh
				if !s.service.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(selected.account, req.GroupID, req.RequestedModel) {
					result.releaseImmediately()
					batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
					continue
				}
				if s.service.rateLimitService != nil && !s.service.rateLimitService.admitCodexTailBurstAfterSlot(ctx, selected.account) {
					result.releaseImmediately()
					s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(selected.account, req.GroupID, req.RequestedModel)
					batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
					continue
				}
				if req.SchedulingMode != GroupSchedulingModeRandom && req.SessionHash != "" && !req.PreserveStickyBinding {
					_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selected.account.ID)
				}
				return &AccountSelectionResult{
					Account:        selected.account,
					Acquired:       true,
					ReleaseFunc:    result.ReleaseFunc,
					SchedulerScore: openAISchedulerScorePointer(selected.breakdown),
				}, compactBlocked, nil
			}

			// 上限变小，或旧快照认为“不限”而最新配置变为有限：
			// 先同步归还旧槽位，再按新上限抢槽，避免批量释放队列的短暂竞态。
			result.releaseImmediately()
			if fresh.Concurrency <= 0 {
				if !s.service.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel) {
					batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
					continue
				}
				if s.service.rateLimitService != nil && !s.service.rateLimitService.admitCodexTailBurstAfterSlot(ctx, fresh) {
					s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel)
					batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
					continue
				}
				if req.SchedulingMode != GroupSchedulingModeRandom && req.SessionHash != "" && !req.PreserveStickyBinding {
					_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, fresh.ID)
				}
				return &AccountSelectionResult{
					Account:        fresh,
					Acquired:       true,
					ReleaseFunc:    func() {},
					SchedulerScore: openAISchedulerScorePointer(selected.breakdown),
				}, compactBlocked, nil
			}
			freshResult, freshAcquireErr := s.service.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
			if freshAcquireErr != nil {
				return nil, compactBlocked, freshAcquireErr
			}
			if freshResult != nil && freshResult.Acquired {
				selected.account = fresh
				if !s.service.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(selected.account, req.GroupID, req.RequestedModel) {
					freshResult.releaseImmediately()
					batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
					continue
				}
				if s.service.rateLimitService != nil && !s.service.rateLimitService.admitCodexTailBurstAfterSlot(ctx, selected.account) {
					freshResult.releaseImmediately()
					s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(selected.account, req.GroupID, req.RequestedModel)
					batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
					continue
				}
				if req.SchedulingMode != GroupSchedulingModeRandom && req.SessionHash != "" && !req.PreserveStickyBinding {
					_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selected.account.ID)
				}
				return &AccountSelectionResult{
					Account:        selected.account,
					Acquired:       true,
					ReleaseFunc:    freshResult.ReleaseFunc,
					SchedulerScore: openAISchedulerScorePointer(selected.breakdown),
				}, compactBlocked, nil
			}
			batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
			continue
		}
		selected.account = fresh
		// 先原子拿到一个账号槽位，再只为最终选中的账号竞争 recovery probe
		//（恢复探测）。未拿到单探测时立即归还槽位并继续批内下一账号，
		// 防止一次批量选择同时放行多个 half-open（半开恢复）请求。
		if !s.service.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(selected.account, req.GroupID, req.RequestedModel) {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
			continue
		}
		if s.service.rateLimitService != nil && !s.service.rateLimitService.admitCodexTailBurstAfterSlot(ctx, selected.account) {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			s.service.ReleaseOpenAIAccountModelRecoveryProbeForGroup(selected.account, req.GroupID, req.RequestedModel)
			batch = append(batch[:selectedIndex], batch[selectedIndex+1:]...)
			continue
		}

		if req.SchedulingMode != GroupSchedulingModeRandom && req.SessionHash != "" && !req.PreserveStickyBinding {
			_ = s.service.BindStickySession(ctx, req.GroupID, req.SessionHash, selected.account.ID)
		}
		return &AccountSelectionResult{
			Account:        selected.account,
			Acquired:       true,
			ReleaseFunc:    result.ReleaseFunc,
			SchedulerScore: openAISchedulerScorePointer(selected.breakdown),
		}, compactBlocked, nil
	}

	return nil, compactBlocked, nil
}

// openAISelectionFilterStats 记录候选账号在哪一道门禁被排除。
// reasons 延迟分配，正常选中账号的热路径不增加额外对象；错误详情保持固定顺序，
// 方便日志聚合直接定位“有账号但为什么一个都不能用”。
type openAISelectionFilterStats struct {
	pool    int
	reasons map[string]int
}

func (s *openAISelectionFilterStats) exclude(reason string) {
	if reason == "" {
		reason = "request_incompatible"
	}
	if s.reasons == nil {
		s.reasons = make(map[string]int, 4)
	}
	s.reasons[reason]++
}

func (s openAISelectionFilterStats) summary(extra string) string {
	var b strings.Builder
	b.WriteString("pool=")
	b.WriteString(strconv.Itoa(s.pool))
	if len(s.reasons) > 0 {
		reasons := make([]string, 0, len(s.reasons))
		for reason := range s.reasons {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		b.WriteString(", filtered:")
		for _, reason := range reasons {
			b.WriteByte(' ')
			b.WriteString(reason)
			b.WriteByte('=')
			b.WriteString(strconv.Itoa(s.reasons[reason]))
		}
	}
	if extra != "" {
		b.WriteString(", ")
		b.WriteString(extra)
	}
	return b.String()
}

func (s *defaultOpenAIAccountScheduler) selectByLoadBalance(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
) (*AccountSelectionResult, int, int, float64, error) {
	candidateResolutionStart := time.Now()
	accounts, err := s.service.listSchedulableAccounts(ctx, req.GroupID, req.Platform)
	if err != nil {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return nil, 0, 0, 0, err
	}
	if len(accounts) == 0 {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return nil, 0, 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false, openAISelectionFilterStats{}.summary(""))
	}
	if normalizeOpenAICompatiblePlatform(req.Platform) == PlatformGrok {
		accounts = s.filterGrokFreeQuotaAccounts(ctx, accounts)
		if len(accounts) == 0 {
			AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
			return nil, 0, 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false, openAISelectionFilterStats{}.summary("grok_free_quota_soft_gate"))
		}
		accounts = filterGrokTeamModelRateLimitedAccountsWithContext(ctx, accounts, req.RequestedModel, time.Now())
		if len(accounts) == 0 {
			AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
			return nil, 0, 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false, openAISelectionFilterStats{}.summary("grok_team_model_rate_limit"))
		}
	}

	// require_privacy_set: 获取分组信息
	var schedGroup *Group
	if req.GroupID != nil && s.service.schedulerSnapshot != nil {
		schedGroup, _ = s.service.schedulerSnapshot.GetGroupByID(ctx, *req.GroupID)
	}

	filterStats := openAISelectionFilterStats{pool: len(accounts)}
	filtered := make([]*Account, 0, len(accounts))
	loadReq := make([]AccountWithConcurrency, 0, len(accounts))
	diagExcluded := 0
	diagNotSchedulableOrOpenAI := 0
	diagRuntimeBlocked := 0
	diagPrivacyBlocked := 0
	diagRequestIncompatible := 0
	diagTransportIncompatible := 0
	for i := range accounts {
		account := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				filterStats.exclude("excluded")
				diagExcluded++
				continue
			}
		}
		if !account.IsSchedulable() {
			filterStats.exclude("not_schedulable")
			diagNotSchedulableOrOpenAI++
			continue
		}
		if account.Platform != normalizeOpenAICompatiblePlatform(req.Platform) || !account.IsOpenAICompatible() {
			filterStats.exclude("platform_mismatch")
			diagNotSchedulableOrOpenAI++
			continue
		}
		if s.service.isOpenAIAccountRequestRuntimeBlockedForGroup(account, req.GroupID, req.RequestedModel) {
			filterStats.exclude("runtime_blocked")
			diagRuntimeBlocked++
			continue
		}
		if s.service.isOpenAIProxyStreamQuarantined(ctx, account) {
			filterStats.exclude("proxy_stream_quarantined")
			diagRuntimeBlocked++
			continue
		}
		// require_privacy_set: 跳过 privacy 未设置的账号并标记异常
		if schedGroup != nil && schedGroup.RequirePrivacySet && !account.IsPrivacySet() {
			filterStats.exclude("privacy_not_set")
			diagPrivacyBlocked++
			s.service.BlockAccountScheduling(account, time.Time{}, "privacy_not_set")
			_ = s.service.accountRepo.SetError(ctx, account.ID,
				fmt.Sprintf("Privacy not set, required by group [%s]", schedGroup.Name))
			continue
		}
		if compatible, reason := s.isAccountRequestCompatibleReason(ctx, account, req); !compatible {
			filterStats.exclude(reason)
			diagRequestIncompatible++
			continue
		}
		if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
			filterStats.exclude("transport_incompatible")
			diagTransportIncompatible++
			continue
		}
		filtered = append(filtered, account)
		loadReq = append(loadReq, AccountWithConcurrency{
			ID:             account.ID,
			MaxConcurrency: account.EffectiveLoadFactor(),
		})
	}
	if len(filtered) == 0 {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		slog.Warn("openai_scheduler_no_filtered_candidates",
			"group_id", derefGroupID(req.GroupID),
			"model", req.RequestedModel,
			"total_accounts", len(accounts),
			"excluded", diagExcluded,
			"not_schedulable_or_openai", diagNotSchedulableOrOpenAI,
			"runtime_blocked", diagRuntimeBlocked,
			"privacy_blocked", diagPrivacyBlocked,
			"request_incompatible", diagRequestIncompatible,
			"transport_incompatible", diagTransportIncompatible,
			"required_transport", string(req.RequiredTransport),
			"required_capability", string(req.RequiredCapability),
			"required_image_capability", string(req.RequiredImageCapability),
			"require_compact", req.RequireCompact,
		)
		return nil, 0, 0, 0, noAvailableOpenAISelectionError(req.RequestedModel, false, filterStats.summary(""))
	}

	// 单账号池没有“比较负载后选谁”的问题，账号槽位 Lua 仍会执行严格并发门禁。
	// 跳过负载快照可减少一次 Redis 往返；compact 请求保留原路径，以维持能力探测回退语义。
	singleAccountFastPath := len(filtered) == 1 && !req.RequireCompact
	loadMap := map[int64]*AccountLoadInfo{}
	if s.service.concurrencyService != nil && !singleAccountFastPath {
		if batchLoad, loadErr := s.service.concurrencyService.GetAccountsLoadBatch(ctx, loadReq); loadErr == nil {
			loadMap = batchLoad
		}
	}
	if req.SchedulingMode == GroupSchedulingModeStrictPriority {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return s.selectByStrictPriorityLayers(ctx, req, filtered, loadMap, filterStats)
	}

	plan := s.buildOpenAIAccountLoadPlan(req, filtered, loadMap)
	candidateCount := plan.candidateCount
	topK := plan.topK
	loadSkew := plan.loadSkew
	selectionOrder := plan.selectionOrder
	if req.RequireCompact && len(plan.candidates) == 0 && len(plan.staleSnapshotCompactRetry) == 0 {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return nil, 0, 0, 0, ErrNoAvailableCompactAccounts
	}
	if req.RequireCompact && len(selectionOrder) == 0 && s.service.schedulerSnapshot == nil {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return nil, candidateCount, topK, loadSkew, ErrNoAvailableCompactAccounts
	}
	if len(selectionOrder) == 0 {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		slog.Warn("openai_scheduler_empty_selection_order",
			"group_id", derefGroupID(req.GroupID),
			"model", req.RequestedModel,
			"filtered", len(filtered),
			"candidate_count", candidateCount,
			"all_candidates", len(plan.allCandidates),
			"compact_retry_candidates", len(plan.staleSnapshotCompactRetry),
			"top_k", topK,
			"required_transport", string(req.RequiredTransport),
			"required_capability", string(req.RequiredCapability),
			"required_image_capability", string(req.RequiredImageCapability),
			"require_compact", req.RequireCompact,
		)
		return nil, candidateCount, topK, loadSkew, noAvailableOpenAISelectionError(req.RequestedModel, req.RequireCompact && len(plan.allCandidates) > 0, filterStats.summary("selection_order_empty"))
	}

	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
	accountSlotStart := time.Now()
	result, compactBlocked, acquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, selectionOrder)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountSlot, time.Since(accountSlotStart))
	if acquireErr != nil {
		return nil, candidateCount, topK, loadSkew, acquireErr
	}
	if result != nil {
		return result, candidateCount, topK, loadSkew, nil
	}

	if s.service.concurrencyService != nil && !singleAccountFastPath {
		if freshLoadMap, loadErr := s.service.concurrencyService.GetAccountsLoadBatchFresh(ctx, loadReq); loadErr == nil {
			freshPlan := s.buildOpenAIAccountLoadPlan(req, filtered, freshLoadMap)
			if len(freshPlan.selectionOrder) > 0 {
				freshAccountSlotStart := time.Now()
				freshResult, freshCompactBlocked, freshAcquireErr := s.tryAcquireOpenAISelectionOrder(ctx, req, freshPlan.selectionOrder)
				AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountSlot, time.Since(freshAccountSlotStart))
				if freshAcquireErr != nil {
					return nil, candidateCount, topK, loadSkew, freshAcquireErr
				}
				if freshResult != nil {
					return freshResult, freshPlan.candidateCount, freshPlan.topK, freshPlan.loadSkew, nil
				}
				compactBlocked = compactBlocked || freshCompactBlocked
				selectionOrder = freshPlan.selectionOrder
				candidateCount = freshPlan.candidateCount
				topK = freshPlan.topK
				loadSkew = freshPlan.loadSkew
			}
		}
	}

	cfg := s.service.schedulingConfig(ctx)
	fallbackQueueFull := false
	// WaitPlan.MaxConcurrency 使用 Concurrency（非 EffectiveLoadFactor），因为 WaitPlan 控制的是 Redis 实际并发槽位等待。
	for _, candidate := range selectionOrder {
		fresh := s.service.resolveFreshSchedulableOpenAIAccountForGroup(ctx, candidate.account, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDBForGroup(ctx, fresh, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		// 回退等待队列无法在唤醒时原子提交 Tail-Burst 会话；超额候选
		// 必须留给成功占槽后的准入路径处理。
		if s.service.rateLimitService != nil && s.service.rateLimitService.codexTailBurstEligible(ctx, fresh) {
			continue
		}
		// 回退等待队列不能承载 half-open（半开）模型。等待者在未来被
		// 唤醒时无法再原子竞争恢复探测，会绕过单探测保护。
		if s.service.requiresOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel) {
			continue
		}
		if !s.service.canUseFallbackQueue(ctx, cfg, fresh.ID) {
			if s.service.isFallbackQueueFull(ctx, cfg, fresh.ID) {
				fallbackQueueFull = true
			}
			continue
		}
		return &AccountSelectionResult{
			Account:        fresh,
			SchedulerScore: openAISchedulerScorePointer(candidate.breakdown),
			WaitPlan: &AccountWaitPlan{
				AccountID:      fresh.ID,
				MaxConcurrency: fresh.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		}, candidateCount, topK, loadSkew, nil
	}

	if fallbackQueueFull {
		return nil, candidateCount, topK, loadSkew, ErrFallbackQueueFull
	}
	slog.Warn("openai_scheduler_selection_exhausted",
		"group_id", derefGroupID(req.GroupID),
		"model", req.RequestedModel,
		"total_accounts", len(accounts),
		"filtered", len(filtered),
		"candidate_count", candidateCount,
		"selection_order", len(selectionOrder),
		"top_k", topK,
		"compact_blocked", compactBlocked,
		"required_transport", string(req.RequiredTransport),
		"required_capability", string(req.RequiredCapability),
		"required_image_capability", string(req.RequiredImageCapability),
		"require_compact", req.RequireCompact,
	)
	return nil, candidateCount, topK, loadSkew, noAvailableOpenAISelectionError(req.RequestedModel, compactBlocked, filterStats.summary("selection_order_exhausted"))
}

// selectByStrictPriorityLayers 逐层原子竞争账号槽位。只有当前层所有合格账号
// 都无法立即占槽时才会进入下一层；等待计划必须推迟到所有层都尝试完之后，
// 否则高优先级满载会把请求困在队列里，无法使用低优先级的空闲容量。
func (s *defaultOpenAIAccountScheduler) selectByStrictPriorityLayers(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	filtered []*Account,
	loadMap map[int64]*AccountLoadInfo,
	filterStats openAISelectionFilterStats,
) (*AccountSelectionResult, int, int, float64, error) {
	candidateCount, topK, loadSkew, hasSelectionCandidate := s.strictPriorityPlanMetrics(req, filtered, loadMap)
	if req.RequireCompact && !hasSelectionCandidate {
		return nil, 0, 0, 0, ErrNoAvailableCompactAccounts
	}

	layers := buildOpenAIStrictPriorityLayers(filtered)
	fallbackOrder := make([]openAIAccountCandidateScore, 0, len(filtered))
	compactBlocked := false
	for _, layer := range layers {
		plan := s.buildOpenAIAccountLoadPlan(req, layer.accounts, loadMap)
		if len(plan.selectionOrder) == 0 {
			continue
		}
		fallbackOrder = append(fallbackOrder, plan.selectionOrder...)
		selection, layerCompactBlocked, acquireErr := s.tryAcquireOpenAIStrictPriorityLayer(ctx, req, plan.selectionOrder)
		compactBlocked = compactBlocked || layerCompactBlocked
		if acquireErr != nil {
			return nil, candidateCount, topK, loadSkew, acquireErr
		}
		if selection != nil {
			return selection, candidateCount, topK, loadSkew, nil
		}

		// tryAcquireOpenAIStrictPriorityLayer 已分批原子尝试当前层全部合格账号；
		// 任一账号未满都会直接成功，因此无需立刻重复 Redis 竞争。
	}

	if req.RequireCompact && len(fallbackOrder) == 0 {
		return nil, candidateCount, topK, loadSkew, ErrNoAvailableCompactAccounts
	}
	cfg := s.service.schedulingConfig(ctx)
	fallbackQueueFull := false
	for _, candidate := range fallbackOrder {
		fresh := s.service.resolveFreshSchedulableOpenAIAccountForGroup(ctx, candidate.account, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		fresh = s.service.recheckSelectedOpenAIAccountFromDBForGroup(ctx, fresh, req.GroupID, req.RequestedModel, false, req.RequiredCapability, req.Platform)
		if fresh == nil || !s.isAccountTransportCompatible(fresh, req.RequiredTransport) || !s.isAccountRequestCompatible(ctx, fresh, req) {
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		// 严格优先级层全部满载后才会进入回退等待；Tail-Burst 账号不能
		// 通过未来唤醒绕过成功占槽后的持久化准入。
		if s.service.rateLimitService != nil && s.service.rateLimitService.codexTailBurstEligible(ctx, fresh) {
			continue
		}
		if s.service.requiresOpenAIAccountModelRecoveryProbeForGroup(fresh, req.GroupID, req.RequestedModel) {
			continue
		}
		if !s.service.canUseFallbackQueue(ctx, cfg, fresh.ID) {
			if s.service.isFallbackQueueFull(ctx, cfg, fresh.ID) {
				fallbackQueueFull = true
			}
			continue
		}
		return &AccountSelectionResult{
			Account:        fresh,
			SchedulerScore: openAISchedulerScorePointer(candidate.breakdown),
			WaitPlan: &AccountWaitPlan{
				AccountID:      fresh.ID,
				MaxConcurrency: fresh.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		}, candidateCount, topK, loadSkew, nil
	}
	if fallbackQueueFull {
		return nil, candidateCount, topK, loadSkew, ErrFallbackQueueFull
	}
	return nil, candidateCount, topK, loadSkew, noAvailableOpenAISelectionError(
		req.RequestedModel,
		compactBlocked,
		filterStats.summary("strict_priority_layers_exhausted"),
	)
}

func (s *defaultOpenAIAccountScheduler) isAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || s.service == nil {
		return false
	}
	return s.service.isOpenAIAccountTransportCompatible(account, requiredTransport)
}

func (s *defaultOpenAIAccountScheduler) isAccountRequestCompatible(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) bool {
	compatible, _ := s.isAccountRequestCompatibleReason(ctx, account, req)
	return compatible
}

// isAccountRequestCompatibleReason 返回具体拦截点，用于调度失败诊断；
// 不改变原有筛选规则，也不会把内部账号状态暴露给客户端响应。
func (s *defaultOpenAIAccountScheduler) isAccountRequestCompatibleReason(ctx context.Context, account *Account, req OpenAIAccountScheduleRequest) (bool, string) {
	platform := normalizeOpenAICompatiblePlatform(req.Platform)
	if account == nil {
		return false, "account_nil"
	}
	if account.Platform != platform || !account.IsOpenAICompatible() {
		return false, "platform_mismatch"
	}
	if !openAIAccountMatchesLongContextRouting(ctx, account) {
		return false, "long_context_routing"
	}
	if s != nil && s.service != nil && s.service.isOpenAIAccountRequestRuntimeBlockedForGroup(account, req.GroupID, req.RequestedModel) {
		return false, "runtime_blocked"
	}
	if s != nil && s.service != nil && s.service.isOpenAIProxyStreamQuarantined(ctx, account) {
		return false, "proxy_stream_quarantined"
	}
	if s != nil && s.service != nil && !s.service.isShadowParentHealthy(ctx, account) {
		return false, "shadow_parent_unhealthy"
	}
	var quotaPaused bool
	var quotaPauseDecision openAIQuotaAutoPauseDecision
	// Quota auto-pause must be evaluated during the initial filter too. Without it the
	// TopK candidate pool can be filled with paused accounts and the later fresh/DB
	// rechecks won't reach healthy accounts that fell outside TopK — manifesting as
	// "no available accounts" even though healthy ones exist.
	if account.IsGrok() {
		if paused, decision := shouldAutoPauseGrokAccountByQuota(account); paused {
			reason := "quota_auto_pause"
			if decision.window != "" {
				reason += "_" + decision.window
			}
			return false, reason
		}
	} else {
		quotaPaused, quotaPauseDecision = shouldAutoPauseOpenAIAccountByQuota(ctx, account)
	}
	if req.RequestedModel != "" && !account.IsModelSupportedForGroupID(req.GroupID, req.RequestedModel) {
		return false, "model_not_supported"
	}
	if req.GroupID != nil && s != nil && s.service != nil &&
		s.service.needsUpstreamChannelRestrictionCheck(ctx, req.GroupID) &&
		s.service.isUpstreamModelRestrictedByChannel(ctx, *req.GroupID, account, req.RequestedModel, req.RequireCompact) {
		return false, "channel_upstream_restricted"
	}
	if !accountSupportsOpenAICapabilities(account, req.RequiredCapability, req.RequiredImageCapability, req.RequiredImageModel) {
		return false, "capability_mismatch"
	}
	// Start/check Tail-Burst only after the request has passed model, channel and
	// capability gates. This prevents an incompatible request from opening an
	// accounting session for an account that would never receive the request.
	tailBurstAllowed := false
	if s != nil && s.service != nil && s.service.rateLimitService != nil {
		// Candidate filtering must remain read-only.  Durable Tail-Burst entry is
		// committed only after this candidate wins an account slot race.
		tailBurstAllowed = s.service.rateLimitService.codexTailBurstEligible(ctx, account)
	}
	if quotaPaused && !tailBurstAllowed {
		reason := "quota_auto_pause"
		if quotaPauseDecision.window != "" {
			reason += "_" + quotaPauseDecision.window
		}
		return false, reason
	}
	return true, ""
}

func (s *defaultOpenAIAccountScheduler) ReportResult(accountID int64, success bool, firstTokenMs *int) {
	if s == nil || s.stats == nil {
		return
	}
	s.stats.report(accountID, success, firstTokenMs)
}

func (s *defaultOpenAIAccountScheduler) ReportSwitch() {
	if s == nil {
		return
	}
	s.metrics.recordSwitch()
}

func (s *defaultOpenAIAccountScheduler) SnapshotMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	if s == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}

	selectTotal := s.metrics.selectTotal.Load()
	prevHit := s.metrics.stickyPreviousHitTotal.Load()
	sessionHit := s.metrics.stickySessionHitTotal.Load()
	switchTotal := s.metrics.accountSwitchTotal.Load()
	latencyTotal := s.metrics.latencyMsTotal.Load()
	loadSkewTotal := s.metrics.loadSkewMilliTotal.Load()

	snapshot := OpenAIAccountSchedulerMetricsSnapshot{
		SelectTotal:              selectTotal,
		StickyPreviousHitTotal:   prevHit,
		StickySessionHitTotal:    sessionHit,
		LoadBalanceSelectTotal:   s.metrics.loadBalanceSelectTotal.Load(),
		AccountSwitchTotal:       switchTotal,
		SchedulerLatencyMsTotal:  latencyTotal,
		RuntimeStatsAccountCount: s.stats.size(),
	}
	if selectTotal > 0 {
		snapshot.SchedulerLatencyMsAvg = float64(latencyTotal) / float64(selectTotal)
		snapshot.StickyHitRatio = float64(prevHit+sessionHit) / float64(selectTotal)
		snapshot.AccountSwitchRate = float64(switchTotal) / float64(selectTotal)
		snapshot.LoadSkewAvg = float64(loadSkewTotal) / 1000 / float64(selectTotal)
	}
	return snapshot
}

func (s *OpenAIGatewayService) openAIAdvancedSchedulerSettingRepo() SettingRepository {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.settingService == nil {
		return nil
	}
	return s.rateLimitService.settingService.settingRepo
}

func (s *OpenAIGatewayService) currentOpenAISchedulerRuntimePolicy(ctx context.Context) *OpenAISchedulerRuntimePolicy {
	if s != nil && s.rateLimitService != nil && s.rateLimitService.settingService != nil {
		return s.rateLimitService.settingService.CurrentOpenAISchedulerRuntimePolicy(ctx)
	}
	settings := &SettingService{}
	if s != nil {
		settings.cfg = s.cfg
	}
	defaults := settings.defaultOpenAISchedulerRuntimePolicy()
	return &defaults
}

func (s *OpenAIGatewayService) isOpenAIAdvancedSchedulerEnabled(ctx context.Context) bool {
	if cached, ok := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.enabled
		}
		// This setting is read on every request. Expiration must not turn one
		// database refresh into a global barrier for all concurrent selectors.
		// Serve the last confirmed value and refresh once in the background.
		openAIAdvancedSchedulerSettingSF.DoChan(openAIAdvancedSchedulerSettingKey, func() (any, error) {
			return s.loadOpenAIAdvancedSchedulerSetting(context.Background()), nil
		})
		return cached.enabled
	}

	result, _, _ := openAIAdvancedSchedulerSettingSF.Do(openAIAdvancedSchedulerSettingKey, func() (any, error) {
		return s.loadOpenAIAdvancedSchedulerSetting(ctx), nil
	})

	enabled, _ := result.(bool)
	return enabled
}

func (s *OpenAIGatewayService) loadOpenAIAdvancedSchedulerSetting(ctx context.Context) bool {
	enabled := false
	if repo := s.openAIAdvancedSchedulerSettingRepo(); repo != nil {
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAdvancedSchedulerSettingDBTimeout)
		defer cancel()
		value, err := repo.GetValue(dbCtx, openAIAdvancedSchedulerSettingKey)
		if err == nil {
			enabled = strings.EqualFold(strings.TrimSpace(value), "true")
		}
	}
	openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{
		enabled:   enabled,
		expiresAt: time.Now().Add(openAIAdvancedSchedulerSettingCacheTTL).UnixNano(),
	})
	return enabled
}

func (s *OpenAIGatewayService) getOpenAIAccountScheduler(ctx context.Context) OpenAIAccountScheduler {
	if s == nil {
		return nil
	}
	settingsStart := time.Now()
	enabled := s.isOpenAIAdvancedSchedulerEnabled(ctx)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageSettings, time.Since(settingsStart))
	if !enabled {
		return nil
	}
	return s.ensureOpenAIAccountScheduler()
}

func (s *OpenAIGatewayService) ensureOpenAIAccountScheduler() OpenAIAccountScheduler {
	if s == nil {
		return nil
	}
	s.openaiSchedulerOnce.Do(func() {
		if s.openaiAccountStats == nil {
			s.openaiAccountStats = newOpenAIAccountRuntimeStats()
		}
		if s.openaiScheduler == nil {
			s.openaiScheduler = newDefaultOpenAIAccountScheduler(s, s.openaiAccountStats)
		}
	})
	return s.openaiScheduler
}

func (s *OpenAIGatewayService) openAIGroupSchedulingMode(ctx context.Context, groupID *int64) string {
	if groupID == nil {
		return GroupSchedulingModeDefault
	}
	group := finalMappingGroupFromContext(ctx, *groupID)
	if group == nil {
		group, _ = s.getOpenAISelectionGroup(ctx, *groupID)
	}
	if group == nil {
		return GroupSchedulingModeDefault
	}
	mode, ok := NormalizeGroupSchedulingMode(group.SchedulingMode)
	if !ok {
		return GroupSchedulingModeDefault
	}
	return mode
}

func resetOpenAIAdvancedSchedulerSettingCacheForTest() {
	openAIAdvancedSchedulerSettingCache = atomic.Value{}
	openAIAdvancedSchedulerSettingSF = singleflight.Group{}
}

func (s *OpenAIGatewayService) SelectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requireCompact bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, "", excludedIDs, requiredTransport, "", "", requireCompact, PlatformOpenAI)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForCapability(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requireCompact bool,
	platformOverride ...string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, "", excludedIDs, requiredTransport, requiredCapability, "", requireCompact, platform)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForCapabilityAndImageModel(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	requiredImageModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platformOverride ...string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	return s.selectAccountWithScheduler(ctx, groupID, previousResponseID, sessionHash, requestedModel, requiredImageModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform)
}

func (s *OpenAIGatewayService) SelectAccountWithSchedulerForImages(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredCapability OpenAIImagesCapability,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	selection, decision, err := s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, "", requiredCapability, false, PlatformOpenAI)
	if err == nil && selection != nil && selection.Account != nil {
		return selection, decision, nil
	}
	// 如果要求 native 能力（如指定了模型）但没有可用的 APIKey 账号，回退到 basic（OAuth 账号）
	if requiredCapability == OpenAIImagesCapabilityNative {
		return s.selectAccountWithScheduler(ctx, groupID, "", sessionHash, requestedModel, requestedModel, excludedIDs, OpenAIUpstreamTransportHTTPSSE, "", OpenAIImagesCapabilityBasic, false, PlatformOpenAI)
	}
	return selection, decision, err
}

func (s *OpenAIGatewayService) selectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	requiredImageModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	excludedIDs = MergeKeywordProtectedAccountIDs(ctx, excludedIDs)
	originalRequestedModel := strings.TrimSpace(requestedModel)
	var mappingErr error
	ctx, requestedModel, mappingErr = s.resolveOpenAIGroupModelForSelection(ctx, groupID, requestedModel)
	if mappingErr != nil {
		return nil, OpenAIAccountScheduleDecision{}, mappingErr
	}
	if strings.EqualFold(strings.TrimSpace(requiredImageModel), strings.TrimSpace(originalRequestedModel)) {
		requiredImageModel = requestedModel
	}
	if openAIGroupModelFallbackOnly(ctx) {
		fallbackModel, ok := s.openAIGroupFallbackModelForSelection(
			ctx, groupID, originalRequestedModel, requestedModel, previousResponseID,
			requiredImageModel, requireCompact, platform,
		)
		if !ok {
			return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
		}
		fallbackCtx := WithOpenAIGroupModelFallback(ctx, *groupID, originalRequestedModel, requestedModel, fallbackModel)
		selection, decision, err := s.selectAccountWithSchedulerOnce(
			fallbackCtx, groupID, previousResponseID, sessionHash, fallbackModel, "",
			excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, false, platform,
		)
		if err != nil {
			return selection, decision, err
		}
		if selection == nil || selection.Account == nil {
			return nil, decision, ErrNoAvailableAccounts
		}
		annotateOpenAISelectionModel(selection, fallbackModel, fallbackCtx)
		return selection, decision, nil
	}
	selection, decision, err := s.selectAccountWithSchedulerOnce(
		ctx, groupID, previousResponseID, sessionHash, requestedModel, requiredImageModel,
		excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform,
	)
	if err == nil {
		annotateOpenAISelectionModel(selection, requestedModel, ctx)
		return selection, decision, nil
	}
	if openAIProxyStreamQuarantineBypassed(ctx) {
		return selection, decision, err
	}
	if !errors.Is(err, ErrNoAvailableAccounts) && !errors.Is(err, ErrNoAvailableCompactAccounts) {
		return selection, decision, err
	}
	// 智能分流：原模型（分流首选目标）无可用账号时，先在分组内按权重
	// 顺序轮转其余健康目标；全部耗尽才落到下面的 FallbackModel 最后兜底。
	// 该路径不写目标健康——无账号只说明账号侧暂时没有容量。
	ctx, weightedSelection, weightedDecision := s.tryWeightedRouteCandidatesForSelection(
		ctx, groupID, previousResponseID, sessionHash,
		originalRequestedModel, requestedModel, requiredImageModel,
		excludedIDs, requiredTransport, requiredCapability, requiredImageCapability,
		requireCompact, platform,
	)
	if weightedSelection != nil && weightedSelection.Account != nil {
		return weightedSelection, weightedDecision, nil
	}
	if fallbackModel, ok := s.openAIGroupFallbackModelForSelection(ctx, groupID, originalRequestedModel, requestedModel, previousResponseID, requiredImageModel, requireCompact, platform); ok {
		fallbackCtx := WithOpenAIGroupModelFallback(ctx, *groupID, originalRequestedModel, requestedModel, fallbackModel)
		fallbackSelection, fallbackDecision, fallbackErr := s.selectAccountWithSchedulerOnce(
			fallbackCtx, groupID, previousResponseID, sessionHash, fallbackModel, "",
			excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, false, platform,
		)
		if fallbackErr == nil && fallbackSelection != nil && fallbackSelection.Account != nil {
			annotateOpenAISelectionModel(fallbackSelection, fallbackModel, fallbackCtx)
			slog.Info("openai_scheduler_group_model_fallback_selected",
				"group_id", derefGroupID(groupID),
				"source_model", requestedModel,
				"fallback_model", fallbackModel,
				"account_id", selectionAccountID(fallbackSelection),
			)
			return fallbackSelection, fallbackDecision, nil
		}
		if fallbackErr == nil {
			// A scheduler is not allowed to report success without an account. Treat
			// this defensive edge case as unavailable and preserve the original
			// request error below instead of sending a nil selection to the handler.
			fallbackErr = ErrNoAvailableAccounts
		}
		// 兜底只是一次受控尝试；无法承接时保留原模型的错误，避免把第二次
		// 选号异常误报成客户请求模型的问题。
		slog.Info("openai_scheduler_group_model_fallback_unavailable",
			"group_id", derefGroupID(groupID),
			"source_model", requestedModel,
			"fallback_model", fallbackModel,
			"error", fallbackErr.Error(),
		)
	}
	// 断流熔断只会隔离 OpenAI 账号，兼容平台不应被误重试。
	if normalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI {
		return selection, decision, err
	}
	blocked := s.getOpenAIProxyStreamCircuit().activeBlockCount(time.Now())
	if blocked == 0 {
		return selection, decision, err
	}
	// 隔离是优先级而非硬闸：只在首轮已无任何容量时放行隔离代理，
	// 宁可尝试已降级的上游，也不能因熔断本身造成“无可用账号”。
	s.logOpenAIProxyStreamQuarantineFailOpen(requestedModel, blocked)
	return s.selectAccountWithSchedulerOnce(
		withOpenAIProxyStreamQuarantineBypass(ctx), groupID, previousResponseID, sessionHash,
		requestedModel, requiredImageModel, excludedIDs, requiredTransport, requiredCapability,
		requiredImageCapability, requireCompact, platform,
	)
}

func (s *OpenAIGatewayService) openAIGroupFallbackModelForSelection(
	ctx context.Context,
	groupID *int64,
	originalModel, groupModel, previousResponseID, requiredImageModel string,
	requireCompact bool,
	platform string,
) (string, bool) {
	if !openAIGroupModelFallbackEnabled(ctx) || openAIGroupModelFallbackPolicySuppressed(ctx) ||
		openAIGroupModelFallbackActiveForRequest(ctx) ||
		groupID == nil || *groupID <= 0 || strings.TrimSpace(previousResponseID) != "" ||
		strings.TrimSpace(requiredImageModel) != "" || requireCompact ||
		normalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI {
		return "", false
	}
	group := finalMappingGroupFromContext(ctx, *groupID)
	if group == nil {
		var err error
		group, err = s.getOpenAISelectionGroup(ctx, *groupID)
		if err != nil {
			slog.Warn("openai_scheduler_group_model_fallback_group_lookup_failed", "group_id", *groupID, "error", err)
			return "", false
		}
	}
	if group == nil || group.Platform != PlatformOpenAI {
		return "", false
	}
	fallbackModel := strings.TrimSpace(group.FallbackModel)
	if fallbackModel == "" || strings.EqualFold(fallbackModel, strings.TrimSpace(groupModel)) ||
		strings.EqualFold(fallbackModel, strings.TrimSpace(originalModel)) {
		return "", false
	}
	return fallbackModel, true
}

func annotateOpenAISelectionModel(selection *AccountSelectionResult, scheduledModel string, ctx context.Context) {
	if selection == nil {
		return
	}
	selection.ScheduledModel = strings.TrimSpace(scheduledModel)
	if fallback, ok := openAIGroupModelFallbackFromContext(ctx); ok {
		selection.GroupFallbackUsed = true
		selection.GroupFallbackSource = fallback.GroupModel
		selection.GroupFallbackModel = fallback.TargetModel
	}
}

func selectionAccountID(selection *AccountSelectionResult) int64 {
	if selection == nil || selection.Account == nil {
		return 0
	}
	return selection.Account.ID
}

func (s *OpenAIGatewayService) selectAccountWithSchedulerOnce(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	requiredImageModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = withOpenAISelectionSnapshotCache(ctx)
	platform = normalizeOpenAICompatiblePlatform(platform)
	decision := OpenAIAccountScheduleDecision{}
	groupValidationStart := time.Now()
	groupValidationErr := s.validateGatewayLiteFixedOpenAIGroup(ctx, groupID)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageGroupValidation, time.Since(groupValidationStart))
	if groupValidationErr != nil {
		slog.Warn("openai_scheduler_select_failed",
			"group_id", derefGroupID(groupID),
			"model", requestedModel,
			"stage", "validate_group",
			"error", groupValidationErr.Error(),
			"required_transport", string(requiredTransport),
			"required_capability", string(requiredCapability),
			"required_image_capability", string(requiredImageCapability),
			"require_compact", requireCompact,
		)
		return nil, decision, groupValidationErr
	}
	schedulingMode := s.openAIGroupSchedulingMode(ctx, groupID)
	var scheduler OpenAIAccountScheduler
	if schedulingMode == GroupSchedulingModeStrictPriority {
		// strict_priority 是分组显式选择的正确性策略，不能因全局“高级调度”
		// 开关关闭而静默退回旧的软优先级路径。
		scheduler = s.ensureOpenAIAccountScheduler()
	} else {
		scheduler = s.getOpenAIAccountScheduler(ctx)
	}
	if scheduler == nil {
		decision.Layer = openAIAccountScheduleLayerLoadBalance
		if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
			effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
			for {
				selection, err := s.selectAccountWithLoadAwareness(ctx, groupID, sessionHash, requestedModel, effectiveExcludedIDs, requireCompact, requiredCapability, platform)
				if err != nil {
					slog.Warn("openai_scheduler_select_failed",
						"group_id", derefGroupID(groupID),
						"model", requestedModel,
						"stage", "load_awareness_without_advanced_scheduler",
						"error", err.Error(),
						"required_transport", string(requiredTransport),
						"required_capability", string(requiredCapability),
						"required_image_capability", string(requiredImageCapability),
						"require_compact", requireCompact,
						"excluded", len(effectiveExcludedIDs),
					)
					return nil, decision, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, err)
				}
				if selection == nil || selection.Account == nil {
					return selection, decision, nil
				}
				if accountSupportsOpenAICapabilities(selection.Account, requiredCapability, requiredImageCapability, requiredImageModel) {
					return selection, decision, nil
				}
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if effectiveExcludedIDs == nil {
					effectiveExcludedIDs = make(map[int64]struct{})
				}
				if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
					return nil, decision, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, ErrNoAvailableAccounts)
				}
				effectiveExcludedIDs[selection.Account.ID] = struct{}{}
			}
		}

		effectiveExcludedIDs := cloneExcludedAccountIDs(excludedIDs)
		for {
			selection, err := s.selectAccountWithLoadAwareness(ctx, groupID, sessionHash, requestedModel, effectiveExcludedIDs, requireCompact, requiredCapability, platform)
			if err != nil {
				slog.Warn("openai_scheduler_select_failed",
					"group_id", derefGroupID(groupID),
					"model", requestedModel,
					"stage", "load_awareness_transport_filter",
					"error", err.Error(),
					"required_transport", string(requiredTransport),
					"required_capability", string(requiredCapability),
					"required_image_capability", string(requiredImageCapability),
					"require_compact", requireCompact,
					"excluded", len(effectiveExcludedIDs),
				)
				return nil, decision, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, err)
			}
			if selection == nil || selection.Account == nil {
				return selection, decision, nil
			}
			if s.isOpenAIAccountTransportCompatible(selection.Account, requiredTransport) &&
				accountSupportsOpenAICapabilities(selection.Account, requiredCapability, requiredImageCapability, requiredImageModel) {
				return selection, decision, nil
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			if effectiveExcludedIDs == nil {
				effectiveExcludedIDs = make(map[int64]struct{})
			}
			if _, exists := effectiveExcludedIDs[selection.Account.ID]; exists {
				return nil, decision, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, ErrNoAvailableAccounts)
			}
			effectiveExcludedIDs[selection.Account.ID] = struct{}{}
		}
	}

	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		err := fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
		slog.Warn("openai_scheduler_select_failed",
			"group_id", derefGroupID(groupID),
			"model", requestedModel,
			"stage", "channel_pricing_restriction",
			"error", err.Error(),
			"required_transport", string(requiredTransport),
			"required_capability", string(requiredCapability),
			"required_image_capability", string(requiredImageCapability),
			"require_compact", requireCompact,
		)
		return nil, decision, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, err)
	}

	var stickyAccountID int64
	if schedulingMode != GroupSchedulingModeRandom && sessionHash != "" && s.cache != nil {
		stickyLookupStart := time.Now()
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil && accountID > 0 {
			stickyAccountID = accountID
		}
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageStickyLookup, time.Since(stickyLookupStart))
	}

	selection, decision, err := scheduler.Select(ctx, OpenAIAccountScheduleRequest{
		GroupID:                 groupID,
		Platform:                platform,
		SessionHash:             sessionHash,
		StickyAccountID:         stickyAccountID,
		PreviousResponseID:      previousResponseID,
		RequestedModel:          requestedModel,
		RequiredImageModel:      requiredImageModel,
		RequiredTransport:       requiredTransport,
		RequiredCapability:      requiredCapability,
		RequiredImageCapability: requiredImageCapability,
		RequireCompact:          requireCompact,
		ExcludedIDs:             excludedIDs,
		RuntimePolicy:           s.currentOpenAISchedulerRuntimePolicy(ctx),
		SchedulingMode:          schedulingMode,
	})
	if err != nil {
		slog.Warn("openai_scheduler_select_failed",
			"group_id", derefGroupID(groupID),
			"model", requestedModel,
			"stage", "advanced_scheduler",
			"error", err.Error(),
			"decision_layer", decision.Layer,
			"candidate_count", decision.CandidateCount,
			"top_k", decision.TopK,
			"selected_account_id", decision.SelectedAccountID,
			"required_transport", string(requiredTransport),
			"required_capability", string(requiredCapability),
			"required_image_capability", string(requiredImageCapability),
			"require_compact", requireCompact,
			"excluded", len(excludedIDs),
		)
		return nil, decision, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, err)
	}
	if decision.Score != nil {
		slog.Debug("openai_scheduler_selection",
			"policy_revision", decision.PolicyRevision,
			"selected_account_id", decision.SelectedAccountID,
			"selected_account_type", decision.SelectedAccountType,
			"previous_response_hard_hit", decision.StickyPreviousHit,
			"session_sticky_soft_hit", decision.StickySessionSoft,
			"score_total", decision.Score.Total,
			"score_priority", decision.Score.Priority,
			"score_load", decision.Score.Load,
			"score_queue", decision.Score.Queue,
			"score_error_rate", decision.Score.ErrorRate,
			"score_ttft", decision.Score.TTFT,
			"score_reset", decision.Score.SoonestReset,
			"score_quota", decision.Score.QuotaHeadroom,
			"score_upstream_cost", decision.Score.UpstreamCost,
			"score_session_sticky", decision.Score.SessionSticky,
			"score_subscription_pool", decision.Score.SubscriptionPool,
		)
	}
	return selection, decision, nil
}

func accountSupportsOpenAICapabilities(account *Account, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredImageModel string) bool {
	if account == nil {
		return false
	}
	return account.SupportsOpenAIEndpointCapability(requiredCapability) &&
		account.SupportsOpenAIImageCapability(requiredImageCapability) &&
		accountSupportsOpenAIImageRoutingModel(account, requiredImageModel)
}

func accountSupportsOpenAIImageRoutingModel(account *Account, requiredImageModel string) bool {
	requiredImageModel = strings.TrimSpace(requiredImageModel)
	if requiredImageModel == "" {
		return true
	}
	return isOpenAIAccountModelEligible(account, requiredImageModel)
}

func cloneExcludedAccountIDs(excludedIDs map[int64]struct{}) map[int64]struct{} {
	if len(excludedIDs) == 0 {
		return nil
	}
	cloned := make(map[int64]struct{}, len(excludedIDs))
	for id := range excludedIDs {
		cloned[id] = struct{}{}
	}
	return cloned
}

func (s *OpenAIGatewayService) isOpenAIAccountTransportCompatible(account *Account, requiredTransport OpenAIUpstreamTransport) bool {
	if requiredTransport == OpenAIUpstreamTransportAny || requiredTransport == OpenAIUpstreamTransportHTTPSSE {
		return true
	}
	if s == nil || account == nil {
		return false
	}
	return s.getOpenAIWSProtocolResolver().Resolve(account).Transport == requiredTransport
}

func (s *OpenAIGatewayService) ReportOpenAIAccountScheduleResult(accountID int64, success bool, firstTokenMs *int, canonicalModel ...string) {
	if len(canonicalModel) > 0 {
		if success {
			s.clearOpenAIAccountModelTransientState(accountID, canonicalModel[0])
		} else if state := s.getOpenAIAccountModelTransientState(); state != nil {
			state.releaseRecoveryProbe(accountID, canonicalModel[0])
		}
	}
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return
	}
	scheduler.ReportResult(accountID, success, firstTokenMs)
}

func (s *OpenAIGatewayService) RecordOpenAIAccountSwitch() {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return
	}
	scheduler.ReportSwitch()
}

func (s *OpenAIGatewayService) SnapshotOpenAIAccountSchedulerMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	scheduler := s.getOpenAIAccountScheduler(context.Background())
	if scheduler == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}
	return scheduler.SnapshotMetrics()
}

func (s *OpenAIGatewayService) openAIWSSessionStickyTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	return openaiStickySessionTTL
}

func (s *OpenAIGatewayService) openAIWSLBTopK() int {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.LBTopK > 0 {
		return s.cfg.Gateway.OpenAIWS.LBTopK
	}
	return 7
}

func (s *OpenAIGatewayService) openAIStickyEscapeConfig() openAIStickyEscapeConfig {
	if s != nil && s.cfg != nil {
		cfg := s.cfg.Gateway.OpenAIScheduler
		enabled := cfg.StickyEscapeEnabled
		if !enabled && cfg.StickyEscapeTTFTMs == 0 && cfg.StickyEscapeErrorRate == 0 {
			enabled = true
		}
		ttftMs := float64(cfg.StickyEscapeTTFTMs)
		if ttftMs <= 0 {
			ttftMs = 15000
		}
		errorRate := cfg.StickyEscapeErrorRate
		if errorRate < 0 || errorRate > 1 {
			errorRate = 0.5
		}
		if errorRate == 0 && cfg.StickyEscapeTTFTMs == 0 && cfg.StickyEscapeErrorRate == 0 {
			errorRate = 0.5
		}
		return openAIStickyEscapeConfig{
			enabled:   enabled,
			ttftMs:    ttftMs,
			errorRate: errorRate,
		}
	}
	return openAIStickyEscapeConfig{
		enabled:   true,
		ttftMs:    15000,
		errorRate: 0.5,
	}
}

func (s *OpenAIGatewayService) openAIWSSchedulerWeights() GatewayOpenAIWSSchedulerScoreWeightsView {
	if s != nil && s.cfg != nil {
		return GatewayOpenAIWSSchedulerScoreWeightsView{
			Priority:  s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority,
			Load:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Load,
			Queue:     s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Queue,
			ErrorRate: s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.ErrorRate,
			TTFT:      s.cfg.Gateway.OpenAIWS.SchedulerScoreWeights.TTFT,
		}
	}
	return GatewayOpenAIWSSchedulerScoreWeightsView{
		Priority:  1.0,
		Load:      1.0,
		Queue:     0.7,
		ErrorRate: 0.8,
		TTFT:      0.5,
	}
}

type GatewayOpenAIWSSchedulerScoreWeightsView struct {
	Priority  float64
	Load      float64
	Queue     float64
	ErrorRate float64
	TTFT      float64
}

func clamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func calcLoadSkewByMoments(sum float64, sumSquares float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	mean := sum / float64(count)
	variance := sumSquares/float64(count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
