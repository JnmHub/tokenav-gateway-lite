package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// tokenRefreshTempUnschedDuration token 刷新重试耗尽后临时不可调度的持续时间
const tokenRefreshTempUnschedDuration = 10 * time.Minute

const (
	defaultTokenRefreshCandidatePageSize        = 200
	maxTokenRefreshCandidatePageSize            = 1000
	defaultTokenRefreshProviderConcurrency      = 4
	maxTokenRefreshProviderConcurrency          = 32
	defaultTokenRefreshProviderQPS              = 2
	maxTokenRefreshProviderQPS                  = 100
	defaultTokenRefreshProviderFailureThreshold = 3
	maxTokenRefreshProviderFailureThreshold     = 100
	defaultTokenRefreshMaxRetries               = 1
	maxTokenRefreshMaxRetries                   = 10
	maxTokenRefreshRetryBackoff                 = 30 * time.Second
	defaultTokenRefreshCycleTimeout             = 4 * time.Minute
	maxTokenRefreshCycleTimeout                 = time.Hour
	defaultTokenRefreshAttemptTimeout           = 30 * time.Second
	maxTokenRefreshAttemptTimeout               = 5 * time.Minute
	defaultTokenRefreshCleanupTimeout           = 2 * time.Second
)

type tokenRefreshRegistration struct {
	platform  string
	refresher TokenRefresher
	executor  OAuthRefreshExecutor
}

// TokenRefreshService OAuth token自动刷新服务
// 定期检查并刷新即将过期的token
type TokenRefreshService struct {
	accountRepo      AccountRepository
	candidatePager   OAuthRefreshCandidatePager
	registrations    []tokenRefreshRegistration
	refreshPolicy    BackgroundRefreshPolicy
	cfg              *config.TokenRefreshConfig
	cacheInvalidator TokenCacheInvalidator
	schedulerCache   SchedulerCache   // 用于同步更新调度器缓存，解决 token 刷新后缓存不一致问题
	tempUnschedCache TempUnschedCache // 用于清除 Redis 中的临时不可调度缓存
	refreshAPI       *OAuthRefreshAPI // 统一刷新 API
	runtimeBlocker   AccountRuntimeBlocker

	// OpenAI privacy: 刷新成功后检查并设置 training opt-out
	privacyClientFactory PrivacyClientFactory
	proxyRepo            ProxyRepository

	stopCh    chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
	runCtx    context.Context
	runCancel context.CancelFunc

	candidateMu sync.Mutex
	afterID     int64
	providerMu  sync.Mutex
	// providerGates/providerPools 在后台周期之间复用，避免多入口叠加后突破单 provider 限制。
	providerGates map[string]*tokenRefreshRateGate
	providerPools map[string]*tokenRefreshConcurrencyGate
}

// NewTokenRefreshService 创建token刷新服务
func NewTokenRefreshService(
	accountRepo AccountRepository,
	oauthService *OAuthService,
	openaiOAuthService *OpenAIOAuthService,
	geminiOAuthService *GeminiOAuthService,
	antigravityOAuthService *AntigravityOAuthService,
	cacheInvalidator TokenCacheInvalidator,
	schedulerCache SchedulerCache,
	cfg *config.Config,
	tempUnschedCache TempUnschedCache,
	grokOAuthServices ...*GrokOAuthService,
) *TokenRefreshService {
	refreshCfg := &config.TokenRefreshConfig{}
	if cfg != nil {
		refreshCfg = &cfg.TokenRefresh
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	s := &TokenRefreshService{
		accountRepo:      accountRepo,
		refreshPolicy:    DefaultBackgroundRefreshPolicy(),
		cfg:              refreshCfg,
		cacheInvalidator: cacheInvalidator,
		schedulerCache:   schedulerCache,
		tempUnschedCache: tempUnschedCache,
		stopCh:           make(chan struct{}),
		runCtx:           runCtx,
		runCancel:        runCancel,
	}
	if pager, ok := accountRepo.(OAuthRefreshCandidatePager); ok {
		s.candidatePager = pager
	}

	openAIRefresher := NewOpenAITokenRefresher(openaiOAuthService, accountRepo)

	claudeRefresher := NewClaudeTokenRefresher(oauthService)
	geminiRefresher := NewGeminiTokenRefresher(geminiOAuthService)
	agRefresher := NewAntigravityTokenRefresher(antigravityOAuthService)
	var grokOAuthService *GrokOAuthService
	if len(grokOAuthServices) > 0 {
		grokOAuthService = grokOAuthServices[0]
	}
	grokRefresher := NewGrokTokenRefresher(grokOAuthService)

	// 平台、刷新器和 executor 只在一处注册，候选查询与执行资格因此不会漂移。
	s.registrations = []tokenRefreshRegistration{
		{platform: PlatformAnthropic, refresher: claudeRefresher, executor: claudeRefresher},
		{platform: PlatformOpenAI, refresher: openAIRefresher, executor: openAIRefresher},
		{platform: PlatformGemini, refresher: geminiRefresher, executor: geminiRefresher},
		{platform: PlatformAntigravity, refresher: agRefresher, executor: agRefresher},
		{platform: PlatformGrok, refresher: grokRefresher, executor: grokRefresher},
	}

	return s
}

func (s *TokenRefreshService) eligiblePlatforms() []string {
	platforms := make([]string, 0, len(s.registrations))
	for _, registration := range s.registrations {
		if registration.platform != "" && registration.refresher != nil {
			platforms = append(platforms, registration.platform)
		}
	}
	return platforms
}

func (s *TokenRefreshService) candidateAfterID() int64 {
	s.candidateMu.Lock()
	defer s.candidateMu.Unlock()
	return s.afterID
}

func (s *TokenRefreshService) setCandidateAfterID(afterID int64) {
	s.candidateMu.Lock()
	s.afterID = afterID
	s.candidateMu.Unlock()
}

// SetPrivacyDeps 注入 OpenAI privacy opt-out 所需依赖
func (s *TokenRefreshService) SetPrivacyDeps(factory PrivacyClientFactory, proxyRepo ProxyRepository) {
	s.privacyClientFactory = factory
	s.proxyRepo = proxyRepo
}

// SetRefreshAPI 注入统一的 OAuth 刷新 API
func (s *TokenRefreshService) SetRefreshAPI(api *OAuthRefreshAPI) {
	s.refreshAPI = api
}

// SetRefreshPolicy 注入后台刷新调用侧策略（用于显式化平台/场景差异行为）。
func (s *TokenRefreshService) SetRefreshPolicy(policy BackgroundRefreshPolicy) {
	s.refreshPolicy = policy
}

func (s *TokenRefreshService) SetAccountRuntimeBlocker(blocker AccountRuntimeBlocker) {
	s.runtimeBlocker = blocker
}

func (s *TokenRefreshService) notifyAccountSchedulingBlocked(account *Account, until time.Time, reason string) {
	if s == nil || s.runtimeBlocker == nil || account == nil {
		return
	}
	s.runtimeBlocker.BlockAccountScheduling(account, until, reason)
}

func (s *TokenRefreshService) notifyAccountSchedulingBlockCleared(accountID int64) {
	if s == nil || s.runtimeBlocker == nil || accountID <= 0 {
		return
	}
	s.runtimeBlocker.ClearAccountSchedulingBlock(accountID)
}

// Start 启动后台刷新服务
func (s *TokenRefreshService) Start() {
	if s.cfg == nil || !s.cfg.Enabled {
		slog.Info("token_refresh.service_disabled")
		return
	}

	s.wg.Add(1)
	go s.refreshLoop()

	slog.Info("token_refresh.service_started",
		"check_interval_minutes", s.cfg.CheckIntervalMinutes,
		"refresh_before_expiry_hours", s.cfg.RefreshBeforeExpiryHours,
		"candidate_page_size", s.candidatePageSize(),
		"provider_concurrency", s.providerConcurrency(),
		"provider_qps", s.providerQPS(),
		"provider_failure_threshold", s.providerFailureThreshold(),
		"cycle_timeout_seconds", int(s.cycleTimeout()/time.Second),
		"attempt_timeout_seconds", int(s.attemptTimeout()/time.Second),
	)
}

// Stop 停止刷新服务（可安全多次调用）
func (s *TokenRefreshService) Stop() {
	s.stopOnce.Do(func() {
		if s.runCancel != nil {
			s.runCancel()
		}
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	s.wg.Wait()
	slog.Info("token_refresh.service_stopped")
}

// refreshLoop 刷新循环
func (s *TokenRefreshService) refreshLoop() {
	defer s.wg.Done()
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}

	// 计算检查间隔
	checkInterval := time.Duration(s.cfg.CheckIntervalMinutes) * time.Minute
	if checkInterval < time.Minute {
		checkInterval = 5 * time.Minute
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// 启动时立即执行一次检查
	s.processRefreshContext(ctx)

	for {
		select {
		case <-ticker.C:
			s.processRefreshContext(ctx)
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		}
	}
}

type tokenRefreshPageStats struct {
	total        int
	oauth        int
	needsRefresh int
	refreshed    int
	skipped      int
	failed       int
}

type tokenRefreshRateGate struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func newTokenRefreshRateGate(qps int) *tokenRefreshRateGate {
	if qps <= 0 {
		return &tokenRefreshRateGate{}
	}
	return newTokenRefreshRateGateWithInterval(time.Second / time.Duration(qps))
}

// newTokenRefreshRateGateWithInterval 只是精确测试时间槽与取消语义的小缝隙。
func newTokenRefreshRateGateWithInterval(interval time.Duration) *tokenRefreshRateGate {
	return &tokenRefreshRateGate{interval: interval}
}

func (g *tokenRefreshRateGate) acquire(ctx context.Context) (func(), error) {
	if g == nil || g.interval <= 0 {
		return func() {}, nil
	}

	// 按实际签发时间推进 next，确保系统调度或 GC 导致 timer 晚醒时，
	// 后续刷新不会“追赶”旧时间槽并在短时间内突发请求服务商。
	// 等待必须放在锁外；否则一个慢等待者会让后续已取消任务卡在互斥锁上。
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		g.mu.Lock()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return nil, err
		}
		now := time.Now()
		if !now.Before(g.next) {
			g.next = now.Add(g.interval)
			g.mu.Unlock()
			return func() {}, nil
		}
		wait := g.next.Sub(now)
		g.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

type tokenRefreshConcurrencyGate struct {
	slots chan struct{}
}

func newTokenRefreshConcurrencyGate(concurrency int) *tokenRefreshConcurrencyGate {
	if concurrency < 1 {
		concurrency = 1
	}
	return &tokenRefreshConcurrencyGate{slots: make(chan struct{}, concurrency)}
}

func (g *tokenRefreshConcurrencyGate) acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type tokenRefreshProviderState struct {
	service      *TokenRefreshService
	registration tokenRefreshRegistration
	rateGate     *tokenRefreshRateGate
	poolGate     *tokenRefreshConcurrencyGate

	mu                  sync.Mutex
	consecutiveFailures int
	tripped             bool
}

func (p *tokenRefreshProviderState) isTripped() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tripped
}

func (p *tokenRefreshProviderState) acquireAttempt(ctx context.Context) (func(), error) {
	if p == nil || p.isTripped() {
		return nil, errRefreshSkipped
	}
	release, err := p.poolGate.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if p.isTripped() {
		release()
		return nil, errRefreshSkipped
	}
	return release, nil
}

func (p *tokenRefreshProviderState) acquireRate(ctx context.Context) (func(), error) {
	if p == nil || p.isTripped() {
		return nil, errRefreshSkipped
	}
	release, err := p.rateGate.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if p.isTripped() {
		release()
		return nil, errRefreshSkipped
	}
	return release, nil
}

// recordResult 只将连续的临时上游失败计入 provider 阈值。
// 单账号的永久凭据失效不代表整个 provider 不健康，否则几个被撤销账号会误伤健康账号。
func (p *tokenRefreshProviderState) recordResult(err error) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		p.consecutiveFailures = 0
		return false
	}
	if errors.Is(err, errRefreshSkipped) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var providerConfigErr *providerConfigurationRefreshError
	var providerContainmentErr *providerCycleContainmentRefreshError
	if errors.As(err, &providerConfigErr) || errors.As(err, &providerContainmentErr) {
		if !p.tripped {
			p.tripped = true
			return true
		}
		return false
	}
	if isNonRetryableRefreshError(err) {
		p.consecutiveFailures = 0
		return false
	}
	p.consecutiveFailures++
	if p.consecutiveFailures >= p.service.providerFailureThreshold() && !p.tripped {
		p.tripped = true
		return true
	}
	return false
}

type rateLimitedOAuthRefreshExecutor struct {
	OAuthRefreshExecutor
	acquireRate func(context.Context) (func(), error)
}

func (e *rateLimitedOAuthRefreshExecutor) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if e == nil || e.OAuthRefreshExecutor == nil {
		return nil, errors.New("OAuth refresh executor is not configured")
	}
	release := func() {}
	if e.acquireRate != nil {
		var err error
		release, err = e.acquireRate(ctx)
		if err != nil {
			return nil, err
		}
	}
	defer release()
	return e.OAuthRefreshExecutor.Refresh(ctx, account)
}

// processRefresh 保留现有测试和包内调用面，生产循环会传入可取消的父 context。
func (s *TokenRefreshService) processRefresh() {
	s.processRefreshContext(context.Background())
}

// processRefreshContext 执行一个有总超时、可从上次游标继续的刷新周期。
func (s *TokenRefreshService) processRefreshContext(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, s.cycleTimeout())
	defer cancel()

	pager := s.candidatePager
	if pager == nil && s.accountRepo != nil {
		pager, _ = s.accountRepo.(OAuthRefreshCandidatePager)
	}
	if pager == nil {
		slog.Error("token_refresh.candidate_pager_missing")
		return
	}
	platforms := s.eligiblePlatforms()
	if len(platforms) == 0 {
		slog.Error("token_refresh.provider_registry_empty")
		return
	}

	refreshWindow := time.Duration(s.refreshBeforeExpiryHours() * float64(time.Hour))
	pageSize := s.candidatePageSize()
	providerStates := make(map[string]*tokenRefreshProviderState, len(s.registrations))
	for i := range s.registrations {
		registration := s.registrations[i]
		providerStates[registration.platform] = &tokenRefreshProviderState{
			service:      s,
			registration: registration,
			rateGate:     s.providerRateGate(registration.platform),
			poolGate:     s.providerConcurrencyGate(registration.platform),
		}
	}

	stats := tokenRefreshPageStats{}
	afterID := s.candidateAfterID()
	for {
		if ctx.Err() != nil {
			slog.Warn("token_refresh.cycle_stopped", "error", ctx.Err(), "resume_after_id", afterID)
			break
		}
		page, err := pager.ListOAuthRefreshCandidatePage(ctx, OAuthRefreshPageOptions{
			Platforms:            platforms,
			AfterID:              afterID,
			Limit:                pageSize,
			ActiveOnly:           true,
			IncludeSetupToken:    true,
			RequireRefreshToken:  true,
			ExcludeRetryCooldown: true,
		})
		if err != nil {
			slog.Error("token_refresh.list_accounts_failed", "error", err, "after_id", afterID)
			break
		}
		if page == nil {
			slog.Error("token_refresh.nil_candidate_page", "after_id", afterID)
			break
		}
		if !page.HasMore && page.NextAfterID == 0 && len(page.Accounts) == 0 {
			s.setCandidateAfterID(0)
			break
		}
		if page.NextAfterID <= afterID {
			slog.Error("token_refresh.invalid_candidate_page_metadata", "after_id", afterID, "next_after_id", page.NextAfterID)
			break
		}
		if !isStrictlyIncreasingAccountPage(page.Accounts, afterID) {
			slog.Error("token_refresh.invalid_candidate_page", "after_id", afterID, "count", len(page.Accounts))
			break
		}

		pageStats := s.processCandidatePage(ctx, page.Accounts, providerStates, refreshWindow)
		stats.total += pageStats.total
		stats.oauth += pageStats.oauth
		stats.needsRefresh += pageStats.needsRefresh
		stats.refreshed += pageStats.refreshed
		stats.skipped += pageStats.skipped
		stats.failed += pageStats.failed

		// 不越过未完整处理的页。重读安全，因为 OAuthRefreshAPI 会在锁内重读 DB 并再次判断过期时间。
		if ctx.Err() != nil {
			slog.Warn("token_refresh.cycle_stopped", "error", ctx.Err(), "resume_after_id", afterID)
			break
		}
		afterID = page.NextAfterID
		s.setCandidateAfterID(afterID)
		if !page.HasMore {
			s.setCandidateAfterID(0)
			break
		}
	}

	if stats.needsRefresh == 0 && stats.failed == 0 {
		slog.Debug("token_refresh.cycle_completed",
			"total", stats.total, "oauth", stats.oauth,
			"needs_refresh", stats.needsRefresh, "refreshed", stats.refreshed,
			"skipped", stats.skipped, "failed", stats.failed)
	} else {
		slog.Info("token_refresh.cycle_completed",
			"total", stats.total, "oauth", stats.oauth,
			"needs_refresh", stats.needsRefresh, "refreshed", stats.refreshed,
			"skipped", stats.skipped, "failed", stats.failed,
		)
	}
}

func isStrictlyIncreasingAccountPage(accounts []Account, afterID int64) bool {
	previous := afterID
	for i := range accounts {
		if accounts[i].ID <= previous {
			return false
		}
		previous = accounts[i].ID
	}
	return true
}

func (s *TokenRefreshService) processCandidatePage(
	ctx context.Context,
	accounts []Account,
	providerStates map[string]*tokenRefreshProviderState,
	refreshWindow time.Duration,
) tokenRefreshPageStats {
	stats := tokenRefreshPageStats{total: len(accounts)}
	groups := make(map[string][]*Account)
	for i := range accounts {
		account := &accounts[i]
		state := providerStates[account.Platform]
		if state == nil || state.registration.refresher == nil || !state.registration.refresher.CanRefresh(account) {
			continue
		}
		stats.oauth++
		if !state.registration.refresher.NeedsRefresh(account, refreshWindow) {
			continue
		}
		stats.needsRefresh++
		groups[account.Platform] = append(groups[account.Platform], account)
	}

	type providerResult struct {
		refreshed int
		skipped   int
		failed    int
	}
	results := make(chan providerResult, len(groups))
	var wg sync.WaitGroup
	for platform, group := range groups {
		state := providerStates[platform]
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshed, skipped, failed := s.processProviderAccounts(ctx, state, group, refreshWindow)
			results <- providerResult{refreshed: refreshed, skipped: skipped, failed: failed}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		stats.refreshed += result.refreshed
		stats.skipped += result.skipped
		stats.failed += result.failed
	}
	return stats
}

func (s *TokenRefreshService) processProviderAccounts(
	ctx context.Context,
	state *tokenRefreshProviderState,
	accounts []*Account,
	refreshWindow time.Duration,
) (refreshed, skipped, failed int) {
	if state == nil || len(accounts) == 0 {
		return 0, 0, 0
	}
	type refreshResult struct {
		accountID int64
		err       error
	}
	jobs := make(chan *Account, len(accounts))
	results := make(chan refreshResult, len(accounts))
	workerCount := s.providerConcurrency()
	if workerCount > len(accounts) {
		workerCount = len(accounts)
	}
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for account := range jobs {
				if ctx.Err() != nil || state.isTripped() {
					results <- refreshResult{accountID: account.ID, err: errRefreshSkipped}
					continue
				}
				err := s.refreshWithRetryWithProviderState(
					ctx,
					account,
					state.registration.refresher,
					state.registration.executor,
					refreshWindow,
					state,
				)
				if state.recordResult(err) {
					slog.Warn("token_refresh.provider_failure_threshold_reached",
						"platform", state.registration.platform,
						"threshold", s.providerFailureThreshold(),
					)
				}
				results <- refreshResult{accountID: account.ID, err: err}
			}
		}()
	}
	for _, account := range accounts {
		jobs <- account
	}
	close(jobs)
	wg.Wait()
	close(results)

	for result := range results {
		switch {
		case result.err == nil:
			refreshed++
			slog.Info("token_refresh.account_refreshed", "account_id", result.accountID, "platform", state.registration.platform)
		case errors.Is(result.err, errRefreshSkipped), errors.Is(result.err, context.Canceled), errors.Is(result.err, context.DeadlineExceeded):
			skipped++
		default:
			failed++
			slog.Warn("token_refresh.account_refresh_failed",
				"account_id", result.accountID,
				"platform", state.registration.platform,
				"error", result.err,
			)
		}
	}
	return refreshed, skipped, failed
}

func (s *TokenRefreshService) refreshBeforeExpiryHours() float64 {
	if s.cfg == nil {
		return 0
	}
	return s.cfg.RefreshBeforeExpiryHours
}

func (s *TokenRefreshService) candidatePageSize() int {
	if s.cfg != nil && s.cfg.CandidatePageSize > 0 {
		return min(s.cfg.CandidatePageSize, maxTokenRefreshCandidatePageSize)
	}
	return defaultTokenRefreshCandidatePageSize
}

func (s *TokenRefreshService) providerConcurrency() int {
	if s.cfg != nil && s.cfg.ProviderConcurrency > 0 {
		return min(s.cfg.ProviderConcurrency, maxTokenRefreshProviderConcurrency)
	}
	return defaultTokenRefreshProviderConcurrency
}

func (s *TokenRefreshService) providerQPS() int {
	if s.cfg != nil && s.cfg.ProviderQPS > 0 {
		return min(s.cfg.ProviderQPS, maxTokenRefreshProviderQPS)
	}
	return defaultTokenRefreshProviderQPS
}

func (s *TokenRefreshService) providerFailureThreshold() int {
	if s.cfg != nil && s.cfg.ProviderFailureThreshold > 0 {
		return min(s.cfg.ProviderFailureThreshold, maxTokenRefreshProviderFailureThreshold)
	}
	return defaultTokenRefreshProviderFailureThreshold
}

func (s *TokenRefreshService) cycleTimeout() time.Duration {
	if s.cfg != nil && s.cfg.CycleTimeoutSeconds > 0 {
		seconds := min(s.cfg.CycleTimeoutSeconds, int(maxTokenRefreshCycleTimeout/time.Second))
		return time.Duration(seconds) * time.Second
	}
	return defaultTokenRefreshCycleTimeout
}

func (s *TokenRefreshService) attemptTimeout() time.Duration {
	if s.cfg != nil && s.cfg.AttemptTimeoutSeconds > 0 {
		seconds := min(s.cfg.AttemptTimeoutSeconds, int(maxTokenRefreshAttemptTimeout/time.Second))
		return time.Duration(seconds) * time.Second
	}
	return defaultTokenRefreshAttemptTimeout
}

func (s *TokenRefreshService) maxRetries() int {
	if s.cfg != nil && s.cfg.MaxRetries > 0 {
		return min(s.cfg.MaxRetries, maxTokenRefreshMaxRetries)
	}
	return defaultTokenRefreshMaxRetries
}

func (s *TokenRefreshService) retryBackoff(attempt int) time.Duration {
	if s.cfg == nil || s.cfg.RetryBackoffSeconds <= 0 || attempt <= 0 {
		return 0
	}
	maxSeconds := int(maxTokenRefreshRetryBackoff / time.Second)
	if s.cfg.RetryBackoffSeconds >= maxSeconds {
		return maxTokenRefreshRetryBackoff
	}
	backoff := time.Duration(s.cfg.RetryBackoffSeconds) * time.Second
	for i := 1; i < attempt && backoff < maxTokenRefreshRetryBackoff; i++ {
		backoff *= 2
	}
	if backoff > maxTokenRefreshRetryBackoff {
		return maxTokenRefreshRetryBackoff
	}
	return backoff
}

func (s *TokenRefreshService) providerRateGate(platform string) *tokenRefreshRateGate {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.providerGates == nil {
		s.providerGates = make(map[string]*tokenRefreshRateGate)
	}
	if gate := s.providerGates[platform]; gate != nil {
		return gate
	}
	gate := newTokenRefreshRateGate(s.providerQPS())
	s.providerGates[platform] = gate
	return gate
}

func (s *TokenRefreshService) providerConcurrencyGate(platform string) *tokenRefreshConcurrencyGate {
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	if s.providerPools == nil {
		s.providerPools = make(map[string]*tokenRefreshConcurrencyGate)
	}
	if gate := s.providerPools[platform]; gate != nil {
		return gate
	}
	gate := newTokenRefreshConcurrencyGate(s.providerConcurrency())
	s.providerPools[platform] = gate
	return gate
}

// refreshWithRetry 带重试的刷新
func (s *TokenRefreshService) refreshWithRetry(ctx context.Context, account *Account, refresher TokenRefresher, executor OAuthRefreshExecutor, refreshWindow time.Duration) error {
	return s.refreshWithRetryWithProviderState(ctx, account, refresher, executor, refreshWindow, nil)
}

func (s *TokenRefreshService) refreshWithRetryWithProviderState(
	ctx context.Context,
	account *Account,
	refresher TokenRefresher,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
	providerState *tokenRefreshProviderState,
) error {
	var lastErr error
	maxRetries := s.maxRetries()

	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		releaseAttempt := func() {}
		if providerState != nil {
			var err error
			releaseAttempt, err = providerState.acquireAttempt(ctx)
			if err != nil {
				return err
			}
		}

		var newCredentials map[string]any
		var err error
		shortCircuit := false
		// The cycle deadline stops the whole scan; this child deadline prevents
		// one provider call from occupying a worker for the rest of that cycle.
		attemptTimeout := s.attemptTimeout()
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)

		// 优先使用统一 API（带分布式锁 + DB 重读保护）
		if s.refreshAPI != nil && executor != nil {
			actualExecutor := executor
			if providerState != nil {
				actualExecutor = &rateLimitedOAuthRefreshExecutor{
					OAuthRefreshExecutor: executor,
					acquireRate:          providerState.acquireRate,
				}
			}
			result, refreshErr := s.refreshAPI.RefreshIfNeeded(attemptCtx, account, actualExecutor, refreshWindow)
			if refreshErr != nil {
				err = refreshErr
			} else if result == nil {
				err = errors.New("oauth refresh returned an empty result")
			} else if result.LockHeld {
				// 锁被其他 worker 持有，由调用侧策略决定如何计数
				err = s.refreshPolicy.handleLockHeld()
				shortCircuit = true
			} else if !result.Refreshed {
				// 已被其他路径刷新，由调用侧策略决定如何计数
				err = s.refreshPolicy.handleAlreadyRefreshed()
				shortCircuit = true
			} else {
				if result.Account != nil {
					account = result.Account
				}
				_ = result.NewCredentials // 统一 API 已设置 _token_version 并更新 DB，无需重复操作
			}
		} else {
			// 降级：直接调用 refresher（兼容旧路径）
			releaseRate := func() {}
			if providerState != nil {
				releaseRate, err = providerState.acquireRate(ctx)
			}
			if err == nil {
				newCredentials, err = refresher.Refresh(attemptCtx, account)
			}
			if releaseRate != nil {
				releaseRate()
			}
			if err == nil && newCredentials != nil {
				newCredentials["_token_version"] = nextOAuthTokenVersion(account.GetCredentialAsInt64("_token_version"))
				if saveErr := persistAccountCredentials(attemptCtx, s.accountRepo, account, newCredentials); saveErr != nil {
					err = fmt.Errorf("failed to save credentials: %w", saveErr)
				}
			}
		}
		cancelAttempt()
		releaseAttempt()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("token refresh attempt timed out after %s: %w", attemptTimeout, err)
		}
		if providerState != nil {
			err = wrapProviderConfigurationRefreshError(err)
		}

		if shortCircuit {
			return err
		}
		if err == nil {
			actionCtx := ctx
			cancelActions := func() {}
			if ctx.Err() != nil {
				// token 可能恰好在周期截止点前持久化。用独立短超时同步缓存，
				// 避免已成功的刷新因父 context 取消而留下旧 token 缓存。
				actionCtx, cancelActions = context.WithTimeout(context.Background(), defaultTokenRefreshCleanupTimeout)
			}
			s.postRefreshActions(actionCtx, account)
			cancelActions()
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// 周期超时只停止扫描，不能将账号误标记为刷新失败或冷却。
			return ctxErr
		}
		if errors.Is(err, errRefreshSkipped) {
			return errRefreshSkipped
		}
		var providerConfigErr *providerConfigurationRefreshError
		var providerContainmentErr *providerCycleContainmentRefreshError
		if errors.As(err, &providerConfigErr) || errors.As(err, &providerContainmentErr) {
			// provider 级配置/状态错误不是单账号故障，由周期熔断处理，
			// 禁止继续重试或批量改写账号状态。
			return err
		}

		// 不可重试错误（invalid_grant/invalid_client 等）直接标记 error 状态并返回
		if isNonRetryableRefreshError(err) {
			errorMsg := fmt.Sprintf("Token refresh failed (non-retryable): %v", err)
			s.notifyAccountSchedulingBlocked(account, time.Time{}, "token_refresh_non_retryable")
			if setErr := s.accountRepo.SetError(ctx, account.ID, errorMsg); setErr != nil {
				slog.Error("token_refresh.set_error_status_failed",
					"account_id", account.ID,
					"error", setErr,
				)
			}
			return err
		}

		lastErr = err
		slog.Warn("token_refresh.retry_attempt_failed",
			"account_id", account.ID,
			"attempt", attempt,
			"max_retries", maxRetries,
			"error", err,
		)

		// 如果还有重试机会，等待后重试
		if attempt < maxRetries {
			backoff := s.retryBackoff(attempt)
			if backoff > 0 {
				timer := time.NewTimer(backoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// 可重试错误耗尽：临时标记账号不可调度，避免请求路径反复命中已知失败的账号
	slog.Warn("token_refresh.retry_exhausted",
		"account_id", account.ID,
		"platform", account.Platform,
		"max_retries", maxRetries,
		"error", lastErr,
	)

	// 设置临时不可调度 10 分钟（不标记 error，保持 status=active 让下个刷新周期能继续尝试）
	until := time.Now().Add(tokenRefreshTempUnschedDuration)
	reason := fmt.Sprintf("token refresh retry exhausted: %v", lastErr)
	s.notifyAccountSchedulingBlocked(account, until, "token_refresh_retry_exhausted")
	if setErr := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); setErr != nil {
		slog.Warn("token_refresh.set_temp_unschedulable_failed",
			"account_id", account.ID,
			"error", setErr,
		)
	} else {
		slog.Info("token_refresh.temp_unschedulable_set",
			"account_id", account.ID,
			"until", until.Format(time.RFC3339),
		)
	}

	return lastErr
}

// postRefreshActions 刷新成功后的后续动作（清除错误状态、缓存失效、调度器同步等）
func (s *TokenRefreshService) postRefreshActions(ctx context.Context, account *Account) {
	// Antigravity 账户：如果之前是因为缺少 project_id 而标记为 error，现在成功获取到了，清除错误状态
	if account.Platform == PlatformAntigravity &&
		account.Status == StatusError &&
		strings.Contains(account.ErrorMessage, "missing_project_id:") {
		if clearErr := s.accountRepo.ClearError(ctx, account.ID); clearErr != nil {
			slog.Warn("token_refresh.clear_account_error_failed",
				"account_id", account.ID,
				"error", clearErr,
			)
		} else {
			slog.Info("token_refresh.cleared_missing_project_id_error", "account_id", account.ID)
			s.notifyAccountSchedulingBlockCleared(account.ID)
		}
	}
	// 刷新成功后清除临时不可调度状态（处理 OAuth 401 恢复场景）
	if account.TempUnschedulableUntil != nil && time.Now().Before(*account.TempUnschedulableUntil) {
		if clearErr := s.accountRepo.ClearTempUnschedulable(ctx, account.ID); clearErr != nil {
			slog.Warn("token_refresh.clear_temp_unschedulable_failed",
				"account_id", account.ID,
				"error", clearErr,
			)
		} else {
			slog.Info("token_refresh.cleared_temp_unschedulable", "account_id", account.ID)
			s.notifyAccountSchedulingBlockCleared(account.ID)
		}
		// 同步清除 Redis 缓存，避免调度器读到过期的临时不可调度状态
		if s.tempUnschedCache != nil {
			if clearErr := s.tempUnschedCache.DeleteTempUnsched(ctx, account.ID); clearErr != nil {
				slog.Warn("token_refresh.clear_temp_unsched_cache_failed",
					"account_id", account.ID,
					"error", clearErr,
				)
			}
		}
	}
	// 对所有 OAuth 账号调用缓存失效（InvalidateToken 内部根据平台判断是否需要处理）
	if s.cacheInvalidator != nil && account.Type == AccountTypeOAuth {
		if err := s.cacheInvalidator.InvalidateToken(ctx, account); err != nil {
			slog.Warn("token_refresh.invalidate_token_cache_failed",
				"account_id", account.ID,
				"error", err,
			)
		} else {
			slog.Debug("token_refresh.token_cache_invalidated", "account_id", account.ID)
		}
	}
	// 同步更新调度器缓存，确保调度获取的 Account 对象包含最新的 credentials
	if s.schedulerCache != nil {
		if err := s.schedulerCache.SetAccount(ctx, account); err != nil {
			slog.Warn("token_refresh.sync_scheduler_cache_failed",
				"account_id", account.ID,
				"error", err,
			)
		} else {
			slog.Debug("token_refresh.scheduler_cache_synced", "account_id", account.ID)
		}
	}
	// OpenAI OAuth: 刷新成功后，检查是否已设置 privacy_mode，未设置则尝试关闭训练数据共享
	s.ensureOpenAIPrivacy(ctx, account)
	// Antigravity OAuth: 刷新成功后，检查是否已设置 privacy_mode，未设置则调用 setUserSettings
	s.ensureAntigravityPrivacy(ctx, account)
}

// errRefreshSkipped 表示刷新被跳过（锁竞争或已被其他路径刷新），不计入 failed 或 refreshed
var errRefreshSkipped = fmt.Errorf("refresh skipped")

// wrapProviderConfigurationRefreshError 将 OAuth 服务商返回的原始客户端配置错误
// 提升为 provider（服务商）级终止错误。client_id/client_secret 属于共享配置，
// 逐账号重试或写 SetError 既无效，也会批量污染健康账号状态。
func wrapProviderConfigurationRefreshError(err error) error {
	if err == nil {
		return nil
	}
	var providerConfigErr *providerConfigurationRefreshError
	if errors.As(err, &providerConfigErr) {
		return err
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "invalid_client") || strings.Contains(message, "unauthorized_client") {
		return &providerConfigurationRefreshError{err: err}
	}
	return err
}

// isNonRetryableRefreshError 判断是否为不可重试的刷新错误
// 这些错误通常表示凭证已失效或配置确实缺失，需要用户重新授权
// 注意：missing_project_id 错误只在真正缺失（从未获取过）时返回，临时获取失败不会返回此错误
func isNonRetryableRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	nonRetryable := []string{
		"invalid_grant",          // refresh_token 已失效
		"invalid_refresh_token",  // refresh_token 无效, team 账号工作区被删除会出现
		"app_session_terminated", // refresh_token team 账号工作区被删除
		"refresh_token_reused",   // OpenAI refresh_token 已被使用，必须重新授权
		"invalid_client",         // 客户端配置错误
		"unauthorized_client",    // 客户端未授权
		"access_denied",          // 访问被拒绝
		"missing_project_id",     // 缺少 project_id
		"no refresh token available",
	}
	for _, needle := range nonRetryable {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// ensureOpenAIPrivacy 检查 OpenAI OAuth 账号是否已设置 privacy_mode，
// 未设置则调用 disableOpenAITraining 并持久化结果到 Extra。
func (s *TokenRefreshService) ensureOpenAIPrivacy(ctx context.Context, account *Account) {
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return
	}
	if s.privacyClientFactory == nil {
		return
	}
	if shouldSkipOpenAIPrivacyEnsure(account.Extra) {
		return
	}

	token, _ := account.Credentials["access_token"].(string)
	if token == "" {
		return
	}

	var proxyURL string
	if account.ProxyID != nil && s.proxyRepo != nil {
		if p, err := s.proxyRepo.GetByID(ctx, *account.ProxyID); err == nil && p != nil {
			proxyURL = p.URL()
		}
	}

	mode := disableOpenAITraining(ctx, s.privacyClientFactory, token, proxyURL)
	if mode == "" {
		return
	}

	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{"privacy_mode": mode}); err != nil {
		slog.Warn("token_refresh.update_privacy_mode_failed",
			"account_id", account.ID,
			"error", err,
		)
	} else {
		slog.Info("token_refresh.privacy_mode_set",
			"account_id", account.ID,
			"privacy_mode", mode,
		)
	}
}

// ensureAntigravityPrivacy 后台刷新中检查 Antigravity OAuth 账号隐私状态。
// 仅当 privacy_mode 已成功设置（"privacy_set"）时跳过；
// 未设置或之前失败（"privacy_set_failed"）均会重试。
func (s *TokenRefreshService) ensureAntigravityPrivacy(ctx context.Context, account *Account) {
	if account.Platform != PlatformAntigravity || account.Type != AccountTypeOAuth {
		return
	}
	if account.Extra != nil {
		if mode, ok := account.Extra["privacy_mode"].(string); ok && mode == AntigravityPrivacySet {
			return
		}
	}

	token, _ := account.Credentials["access_token"].(string)
	if token == "" {
		return
	}

	projectID, _ := account.Credentials["project_id"].(string)

	var proxyURL string
	if account.ProxyID != nil && s.proxyRepo != nil {
		if p, err := s.proxyRepo.GetByID(ctx, *account.ProxyID); err == nil && p != nil {
			proxyURL = p.URL()
		}
	}

	mode := setAntigravityPrivacy(ctx, token, projectID, proxyURL)
	if mode == "" {
		return
	}

	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{"privacy_mode": mode}); err != nil {
		slog.Warn("token_refresh.update_antigravity_privacy_mode_failed",
			"account_id", account.ID,
			"error", err,
		)
	} else {
		applyAntigravityPrivacyMode(account, mode)
		slog.Info("token_refresh.antigravity_privacy_mode_set",
			"account_id", account.ID,
			"privacy_mode", mode,
		)
	}
}
