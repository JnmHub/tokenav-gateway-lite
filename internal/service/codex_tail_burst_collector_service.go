package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/alitto/pond/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// CodexTailBurstCollector 周期性轮询 /wham/usage 并持久化用量快照。
//
// 快照写入账号 Extra（codex_5h_used_percent / codex_7d_used_percent /
// codex_<w>_reset_at / codex_usage_updated_at），与响应头探针共用同一组键，
// 调度阈值与 tail-burst 评估都读这套数据。多实例部署时用 Redis 锁保证
// 同一时间只有一个实例在轮询（DB 写入本身幂等，无锁时最多是重复轮询）。
type CodexTailBurstCollector struct {
	cfg            *config.Config
	settingService *SettingService // 运行时开关（UI 高级设置），nil 时回退 yaml
	accountRepo    AccountRepository
	usageLogRepo   UsageLogRepository // 超额会话结算（可选：nil 时跳过退出结算）
	quotaService   *OpenAIQuotaService
	redis          redis.Cmdable // 可选：多实例 leader 锁
	redisPrefix    string        // 网关专属命名空间，避免共享 Redis 的实例互相阻塞

	pool         pond.Pool
	parentCtx    context.Context
	parentCancel context.CancelFunc
	wg           sync.WaitGroup
	mu           sync.Mutex
	// roundMu prevents overlapping collectOnce calls. Start normally invokes
	// one round at a time, but tests, manual triggers, or future schedulers may
	// call it concurrently; overlapping rounds would race the captured Redis
	// lease key and duplicate every upstream quota probe.
	roundMu      sync.Mutex
	started      bool
	leaderLockMu sync.Once
	leaderOwner  string
	// leaderLeaseMu protects the key captured by the currently held lease.
	// Redis prefix settings may change while a long collection round is in
	// flight; renewal and release must continue using the key acquired at the
	// start of that round.
	leaderLeaseMu  sync.RWMutex
	leaderLeaseKey string
}

const (
	defaultCodexTailBurstCollectorInterval = 45 * time.Second
	defaultCodexTailBurstCollectorTimeout  = 8 * time.Second
	codexTailBurstCollectorLockKey         = "codex:tail-burst:collector-lock"
	// 锁 TTL 兜底。正常一轮采集远短于此；若整轮超过 TTL（超大账号池），
	// 其他实例可能并发轮询一轮——快照写入幂等，影响只是重复上游请求。
	codexTailBurstCollectorLockTTL = 60 * time.Second
)

// NewCodexTailBurstCollector 构造采集器。redis 传 nil 时跳过 leader 锁
// （单实例部署或纯本地测试）。settingService 传 nil 时启用判定回退到 yaml 配置；
// usageLogRepo 传 nil 时跳过超额会话的退出结算与增量刷新。
func NewCodexTailBurstCollector(cfg *config.Config, settingService *SettingService, accountRepo AccountRepository, usageLogRepo UsageLogRepository, quotaService *OpenAIQuotaService, redis redis.Cmdable) *CodexTailBurstCollector {
	ctx, cancel := context.WithCancel(context.Background())
	concurrency := 4
	if cfg != nil && cfg.CodexTailBurst.CollectorConcurrency > 0 {
		concurrency = cfg.CodexTailBurst.CollectorConcurrency
	}
	if concurrency > 16 {
		concurrency = 16
	}
	return &CodexTailBurstCollector{
		cfg:            cfg,
		settingService: settingService,
		accountRepo:    accountRepo,
		usageLogRepo:   usageLogRepo,
		quotaService:   quotaService,
		redis:          redis,
		redisPrefix:    codexTailBurstCollectorRedisPrefix(cfg),
		pool:           pond.NewPool(concurrency),
		parentCtx:      ctx,
		parentCancel:   cancel,
	}
}

// Start 启动后台轮询循环。调用方保证只调一次（wire Provide 内）。
func (c *CodexTailBurstCollector) Start() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return
	}
	c.started = true
	c.wg.Add(1)
	go c.run()
}

// Stop 停止轮询并等待在途任务结束。
func (c *CodexTailBurstCollector) Stop() {
	if c == nil {
		return
	}
	c.parentCancel()
	c.wg.Wait()
	c.pool.StopAndWait()
}

func (c *CodexTailBurstCollector) run() {
	defer c.wg.Done()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-c.parentCtx.Done():
			return
		case <-timer.C:
		}
		// 即使总开关已关闭，也要扫描并结算遗留会话。否则进程重启或
		// 管理员关闭功能后，账号 Extra 中的 active 标记会一直残留，
		// 后台仍会显示“超额中”。collectOnce 会在关闭分支跳过配额探针。
		c.collectOnce(c.parentCtx)
		timer.Reset(c.interval())
	}
}

func (c *CodexTailBurstCollector) enabled() bool {
	if c == nil || c.cfg == nil {
		return false
	}
	if c.settingService != nil {
		if settings := c.settingService.GetCodexTailBurstSettings(c.parentCtx); settings.Enabled != nil {
			return *settings.Enabled
		}
	}
	return c.cfg.CodexTailBurst.Enabled
}

func (c *CodexTailBurstCollector) interval() time.Duration {
	if c == nil || c.cfg == nil {
		return defaultCodexTailBurstCollectorInterval
	}
	if parsed, err := time.ParseDuration(strings.TrimSpace(c.cfg.CodexTailBurst.CollectorInterval)); err == nil && parsed > 0 {
		return parsed
	}
	return defaultCodexTailBurstCollectorInterval
}

func (c *CodexTailBurstCollector) timeout() time.Duration {
	if c == nil || c.cfg == nil {
		return defaultCodexTailBurstCollectorTimeout
	}
	if parsed, err := time.ParseDuration(strings.TrimSpace(c.cfg.CodexTailBurst.CollectorTimeout)); err == nil && parsed > 0 {
		return parsed
	}
	return defaultCodexTailBurstCollectorTimeout
}

func (c *CodexTailBurstCollector) collectOnce(ctx context.Context) {
	if c == nil || c.accountRepo == nil {
		return
	}
	if !c.roundMu.TryLock() {
		slog.Debug("codex_tail_burst_collector_round_skipped", "reason", "already_running")
		return
	}
	defer c.roundMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if !c.acquireLeaderLock(ctx) {
		return
	}
	leaseCtx, stopRenewal := c.startLeaderLockRenewal(ctx)
	defer func() {
		stopRenewal()
		// The request context may have been canceled by shutdown or by a lost
		// lease. Release with a short independent context so an owned lease is
		// deleted immediately instead of waiting for its TTL.
		releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.releaseLeaderLock(releaseCtx)
	}()

	accounts, err := c.listAccountsForTailBurst(leaseCtx)
	if err != nil {
		slog.Warn("codex_tail_burst_collector_list_failed", "error", err)
		return
	}
	if !c.enabled() {
		c.cleanupActiveSessions(leaseCtx, accounts)
		return
	}

	eligible := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !codexTailBurstCollectorAccountEligible(&account) {
			// 账号可能在进入会话后被停用、转为影子账号、改成非 OAuth、
			// 清掉 chatgpt_account_id，或关闭账号级开关。所有失格账号都
			// 必须结算遗留会话，否则 active 标记会永久悬挂在后台。
			if codexTailBurstActive(account.Extra) {
				if _, err := settleCodexTailBurstSession(leaseCtx, c.accountRepo, c.usageLogRepo, &account, time.Now()); err != nil {
					slog.Debug("codex_tail_burst_collector_settle_ineligible_failed", "account_id", account.ID, "error", err)
				}
			}
			continue
		}
		eligible = append(eligible, account)
	}
	if c.quotaService == nil {
		return
	}
	if len(eligible) == 0 {
		return
	}

	timeout := c.timeout()
	var wg sync.WaitGroup
	for i := range eligible {
		account := eligible[i]
		wg.Add(1)
		if _, submitted := c.pool.TrySubmit(func() {
			defer wg.Done()
			c.collectAccount(leaseCtx, &account, timeout)
		}); !submitted {
			wg.Done()
			slog.Warn("codex_tail_burst_collector_submit_failed", "account_id", account.ID)
		}
	}
	wg.Wait()
	slog.Debug("codex_tail_burst_collector_round_done", "eligible", len(eligible))
}

func (c *CodexTailBurstCollector) listAccountsForTailBurst(ctx context.Context) ([]Account, error) {
	if c == nil || c.accountRepo == nil {
		return nil, nil
	}
	if repo, ok := c.accountRepo.(AccountCodexTailBurstSessionRepository); ok {
		return repo.ListByPlatformForCodexTailBurst(ctx, PlatformOpenAI)
	}
	// Older repository implementations expose only the active listing. It is
	// still safe to use it as a fallback; concrete SQL repositories implement
	// the broader capability above so inactive-session cleanup is complete.
	return c.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
}

// cleanupActiveSessions 在全局功能关闭时结算所有遗留会话。
// 不按当前账号资格过滤：账号可能在进入会话后被停用、转为影子账号或
// 清掉 chatgpt_account_id，仍需清掉 active 标记，避免状态永久悬挂。
// settleCodexTailBurstSession 在用量仓储不可用时会保留标记，待依赖恢复后重试，
// 从而避免为了清理 UI 状态而丢失会话统计。
func (c *CodexTailBurstCollector) cleanupActiveSessions(ctx context.Context, accounts []Account) {
	if c == nil || c.accountRepo == nil || c.usageLogRepo == nil {
		return
	}
	for index := range accounts {
		account := &accounts[index]
		if !codexTailBurstActive(account.Extra) {
			continue
		}
		if _, err := settleCodexTailBurstSession(ctx, c.accountRepo, c.usageLogRepo, account, time.Now().UTC()); err != nil {
			slog.Debug("codex_tail_burst_collector_settle_disabled_failed", "account_id", account.ID, "error", err)
		}
	}
}

// codexTailBurstCollectorAccountEligible 采集对象：OpenAI OAuth 账号，
// 状态活跃、非影子账号、具备 chatgpt_account_id、且账号级超额开关未关闭。
func codexTailBurstCollectorAccountEligible(account *Account) bool {
	if account == nil {
		return false
	}
	if !account.IsOpenAIOAuth() || account.IsShadow() || !account.IsActive() {
		return false
	}
	if codexTailBurstAccountDisabled(account.Extra) {
		return false
	}
	return strings.TrimSpace(account.GetCredential("chatgpt_account_id")) != ""
}

func (c *CodexTailBurstCollector) collectAccount(ctx context.Context, account *Account, timeout time.Duration) {
	if c == nil || account == nil || c.quotaService == nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	now := time.Now().UTC()
	usage, err := c.quotaService.QueryUsage(callCtx, account.ID)
	if err != nil {
		// 抓取失败时仍可依据已存快照判定会话退出（进入时窗口重置 / 订阅到期）。
		c.maybeExitTailBurstSession(ctx, account, account.Extra, now)
		slog.Debug("codex_tail_burst_collector_fetch_failed", "account_id", account.ID, "error", err)
		return
	}
	updates := buildCodexMainWindowExtraUpdates(usage, now)
	// Evaluate the newly fetched snapshot independently of the normal account
	// auto-pause threshold.  The collector is the first reliable place to open a
	// Tail-Burst session when an account crosses the configured remaining-quota
	// boundary, even if auto-pause is disabled or configured at another level.
	storedExtra := account.Extra
	merged := mergeExtraMap(storedExtra, updates)
	if c.maybeExitTailBurstSession(ctx, account, merged, now) {
		return
	}
	account.Extra = merged
	triggerRatio, expiryWindow, snapshotTTL := codexTailBurstSettings(c.cfg)
	tail := EvaluateCodexTailBurst(account, now, triggerRatio, expiryWindow, snapshotTTL)
	if started, _ := startCodexTailBurstSessionIfEligible(ctx, c.accountRepo, c.usageLogRepo, account, merged, tail, now); started {
		// startCodexTailBurstSessionIfEligible merges the durable marker into the
		// request-local snapshot. Keep the following delta calculation and any
		// usage write based on that same coherent view.
		merged = account.Extra
	}
	c.maybeUpdateTailBurstDelta(ctx, account, merged)
	if len(updates) == 0 {
		// A successful probe can legitimately omit rate-limit windows (for example
		// while the upstream is refreshing the subscription). The existing session
		// was checked above, and an expiry-based new session may still have been
		// opened here; there is no usage snapshot to persist in this case.
		return
	}

	if usageValuesEqual(storedExtra, updates) {
		// 用量未变化时只刷新新鲜度时间戳，避免每轮全量写。5h/7d
		// 时间戳必须分别维护：不能因为一个窗口刚刷新，就让另一个窗口的
		// 旧快照看起来仍然新鲜。缺失的窗口时间戳也要补写，便于新逻辑
		// 逐窗口判断，同时保留全局时间戳供旧版本读取。
		if touchUpdates := codexTailBurstCollectorTouchUpdates(storedExtra, updates, now); len(touchUpdates) > 0 {
			if err := c.accountRepo.UpdateExtra(ctx, account.ID, touchUpdates); err != nil {
				slog.Warn("codex_tail_burst_collector_touch_failed", "account_id", account.ID, "error", err)
			}
		}
		return
	}
	if err := c.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("codex_tail_burst_collector_persist_failed", "account_id", account.ID, "error", err)
		return
	}
	slog.Debug("codex_tail_burst_collector_persisted", "account_id", account.ID)
}

// maybeExitTailBurstSession 检测超额会话是否应当结束并结算：
//   - 订阅到期触发：账号 expires_at 已过；
//   - 窗口触发：进入时所在窗口的重置时刻已过（新窗口快照的 reset_at 指向
//     未来，无法区分滚过与否，因此以进入时刻记录的 entry_reset_at 为准）。
//
// 返回 true 表示已结算（活跃标记已清除），本轮不再刷新 delta / 快照。
func (c *CodexTailBurstCollector) maybeExitTailBurstSession(ctx context.Context, account *Account, extra map[string]any, now time.Time) bool {
	if c == nil || account == nil || c.accountRepo == nil || c.usageLogRepo == nil {
		return false
	}
	if !codexTailBurstActive(extra) {
		return false
	}
	if codexTailBurstActiveReason(extra) == codexTailBurstReasonAccountExpiry {
		if account.ExpiresAt != nil && !account.ExpiresAt.After(now) {
			settled, _ := settleCodexTailBurstSessionWithExtra(ctx, c.accountRepo, c.usageLogRepo, account, extra, now)
			return settled
		}
		return false
	}
	if raw, ok := extra[codexTailBurstExtraEntryResetAt].(string); ok {
		if resetAt, err := parseTime(raw); err == nil && !resetAt.After(now) {
			settled, _ := settleCodexTailBurstSessionWithExtra(ctx, c.accountRepo, c.usageLogRepo, account, extra, now)
			return settled
		}
	}
	return false
}

// maybeUpdateTailBurstDelta 刷新活跃超额会话的增量（当前用量 - 进入用量）。
// 用本轮合并后的快照计算；未活跃 / 无窗口 / 无有效用量时跳过。
func (c *CodexTailBurstCollector) maybeUpdateTailBurstDelta(ctx context.Context, account *Account, merged map[string]any) {
	if c == nil || account == nil || c.accountRepo == nil {
		return
	}
	window := codexTailBurstActiveWindow(merged)
	if window == "" {
		return
	}
	current := readOpenAIQuotaUsedPercent(merged, window)
	updates, ok := codexTailBurstDeltaUpdates(merged, current)
	if !ok {
		return
	}
	marker := codexTailBurstSessionMarkerFromExtra(merged)
	if casRepo, ok := c.accountRepo.(AccountCodexTailBurstCASRepository); ok {
		applied, err := casRepo.UpdateCodexTailBurstDeltaIfMarkerMatches(ctx, account.ID, marker, updates)
		if err != nil {
			slog.Warn("codex_tail_burst_delta_update_failed", "account_id", account.ID, "error", err)
			return
		}
		if applied {
			account.Extra = mergeExtraMap(merged, updates)
			recordCodexTailBurstHistoryUpdate(ctx, c.usageLogRepo, account, merged, resolveAccountExtraNumberOrZero(updates, codexTailBurstExtraDeltaPercent))
		}
		return
	}
	if err := c.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("codex_tail_burst_delta_update_failed", "account_id", account.ID, "error", err)
		return
	}
	account.Extra = mergeExtraMap(merged, updates)
	recordCodexTailBurstHistoryUpdate(ctx, c.usageLogRepo, account, merged, resolveAccountExtraNumberOrZero(updates, codexTailBurstExtraDeltaPercent))
}

func resolveAccountExtraNumberOrZero(values map[string]any, key string) float64 {
	if value, ok := resolveAccountExtraNumber(values, key); ok {
		return value
	}
	return 0
}

func mergeExtraMap(base map[string]any, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

// usageValuesEqual 比较新快照与已存的用量值。更新时间戳是采样元数据，
// 不属于用量值本身；忽略它们可以让相同用量走轻量 touch 路径，而不是每轮
// 都触发一次全量 Extra 写入。
func usageValuesEqual(existing map[string]any, updates map[string]any) bool {
	for key, value := range updates {
		if isCodexTailBurstUsageTimestampKey(key) {
			continue
		}
		old, ok := existing[key]
		if !ok {
			return false
		}
		if !valuesEquivalent(old, value) {
			return false
		}
	}
	return true
}

func isCodexTailBurstUsageTimestampKey(key string) bool {
	switch key {
	case "codex_usage_updated_at", "codex_5h_usage_updated_at", "codex_7d_usage_updated_at":
		return true
	default:
		return false
	}
}

// codexTailBurstCollectorTouchUpdates 返回本轮需要补写的采样时间戳。
// 这里故意只检查同名窗口键，不使用 codexTailBurstSnapshotFresh 的旧字段
// 回退逻辑：如果 5h/7d 独立键缺失，即使全局兼容键很新，也必须把独立键补上。
func codexTailBurstCollectorTouchUpdates(existing, updates map[string]any, now time.Time) map[string]any {
	if len(updates) == 0 {
		return nil
	}
	const touchTTL = time.Second
	keys := [...]string{
		"codex_usage_updated_at",
		"codex_5h_usage_updated_at",
		"codex_7d_usage_updated_at",
	}
	touched := make(map[string]any, len(keys))
	for _, key := range keys {
		value, ok := updates[key]
		if !ok || codexTailBurstCollectorTimestampFresh(existing, key, now, touchTTL) {
			continue
		}
		touched[key] = value
	}
	if len(touched) == 0 {
		return nil
	}
	return touched
}

func codexTailBurstCollectorTimestampFresh(extra map[string]any, key string, now time.Time, ttl time.Duration) bool {
	if len(extra) == 0 || ttl <= 0 {
		return false
	}
	raw, ok := extra[key]
	if !ok {
		return false
	}
	updatedAt, ok := parseExtraTimestamp(raw)
	if !ok || updatedAt.After(now) {
		return false
	}
	return now.Sub(updatedAt) <= ttl
}

func valuesEquivalent(left, right any) bool {
	lf, lok := toFloat(left)
	rf, rok := toFloat(right)
	if lok && rok {
		return lf == rf
	}
	return reflect.DeepEqual(left, right)
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

// acquireLeaderLock 用 SET NX 抢占多实例轮询权；redis 未配置时视为单实例。
func (c *CodexTailBurstCollector) acquireLeaderLock(ctx context.Context) bool {
	if c == nil || c.redis == nil {
		return true
	}
	owner := c.leaderLockOwner()
	key := c.leaderLockKey()
	ok, err := c.redis.SetNX(ctx, key, owner, codexTailBurstCollectorLockTTL).Result()
	if err != nil {
		slog.Warn("codex_tail_burst_collector_lock_failed", "error", err)
		return false
	}
	if ok {
		c.leaderLeaseMu.Lock()
		c.leaderLeaseKey = key
		c.leaderLeaseMu.Unlock()
	}
	return ok
}

// startLeaderLockRenewal keeps the Redis lease alive while a large account
// batch is being probed. A fixed 60-second lease is intentionally used as the
// crash-safety bound, but without renewal a healthy collector could lose the
// lease mid-round and another instance would duplicate every upstream probe.
// The returned stop function is idempotent and waits for the renewal goroutine
// to exit before the caller releases the lock.
func (c *CodexTailBurstCollector) startLeaderLockRenewal(ctx context.Context) (context.Context, func()) {
	if c == nil || c.redis == nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return ctx, func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leaseCtx, leaseCancel := context.WithCancel(ctx)
	renewCtx, renewCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	interval := codexTailBurstCollectorLockTTL / 3
	if interval < time.Second {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				key := c.currentLeaderLeaseKey()
				renewed, err := codexTailBurstCollectorRenewScript.Run(
					renewCtx,
					c.redis,
					[]string{key},
					c.leaderLockOwner(),
					codexTailBurstCollectorLockTTL.Milliseconds(),
				).Int64()
				if err != nil {
					slog.Warn("codex_tail_burst_collector_lock_renew_failed", "error", err)
					leaseCancel()
					return
				}
				if renewed != 1 {
					// Ownership was lost; compare-and-delete release remains safe,
					// but do not keep attempting to renew a peer's lease.
					slog.Warn("codex_tail_burst_collector_lock_lost")
					leaseCancel()
					return
				}
			}
		}
	}()
	var stopOnce sync.Once
	return leaseCtx, func() {
		stopOnce.Do(func() {
			renewCancel()
			leaseCancel()
			<-done
		})
	}
}

func (c *CodexTailBurstCollector) releaseLeaderLock(ctx context.Context) {
	if c == nil || c.redis == nil {
		return
	}
	key := c.currentLeaderLeaseKey()
	if err := codexTailBurstCollectorReleaseScript.Run(ctx, c.redis, []string{key}, c.leaderLockOwner()).Err(); err != nil {
		slog.Warn("codex_tail_burst_collector_unlock_failed", "error", err)
	}
	c.leaderLeaseMu.Lock()
	if c.leaderLeaseKey == key {
		c.leaderLeaseKey = ""
	}
	c.leaderLeaseMu.Unlock()
}

func (c *CodexTailBurstCollector) currentLeaderLeaseKey() string {
	if c == nil {
		return codexTailBurstCollectorLockKey
	}
	c.leaderLeaseMu.RLock()
	key := c.leaderLeaseKey
	c.leaderLeaseMu.RUnlock()
	if key != "" {
		return key
	}
	return c.leaderLockKey()
}

// leaderLockKey namespaces the lease by gateway prefix. Without this, two
// gateways sharing one Redis database would prevent each other's collectors
// from running even though their account/control-plane data is independent.
func (c *CodexTailBurstCollector) leaderLockKey() string {
	if c == nil {
		return codexTailBurstCollectorLockKey
	}
	prefix := c.redisPrefix
	// The gateway-lite settings endpoint can change the Redis namespace at
	// runtime. Read the normalized runtime value when available so a subsequent
	// collection round does not continue using the previous gateway's lock.
	if ref := gatewaylite.DefaultRuntimeConfigRef(); ref != nil {
		prefix = ref.Current().RedisPrefix
	}
	prefix = gatewaylite.NormalizeRedisPrefix(prefix)
	return prefix + ":" + codexTailBurstCollectorLockKey
}

func codexTailBurstCollectorRedisPrefix(cfg *config.Config) string {
	if cfg == nil {
		return gatewaylite.NormalizeRedisPrefix("")
	}
	return gatewaylite.NormalizeRedisPrefix(strings.Trim(strings.TrimSpace(cfg.GatewayLite.RedisPrefix), ":"))
}

// leaderLockOwner 返回每个采集器实例稳定且唯一的锁令牌。释放锁时必须
// compare-and-delete（比较后删除），避免旧实例在 TTL 过期后误删新实例的锁。
func (c *CodexTailBurstCollector) leaderLockOwner() string {
	if c == nil {
		return ""
	}
	c.leaderLockMu.Do(func() { c.leaderOwner = uuid.NewString() })
	return c.leaderOwner
}

var codexTailBurstCollectorReleaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

var codexTailBurstCollectorRenewScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)
