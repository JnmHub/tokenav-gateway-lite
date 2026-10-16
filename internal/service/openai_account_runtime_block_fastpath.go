package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	openAIAccountStateUpdateTimeout       = 5 * time.Second
	openAIOAuth429FallbackCooldown        = 5 * time.Second
	openAIOAuth429RetryWindow             = 2 * time.Minute
	openAIOAuth429RetryDelay              = 500 * time.Millisecond
	openAIOAuth429MaxRetryDelay           = 8 * time.Second
	openAIOAuth429MaxAccountAttempts      = 3
	openAIStopSchedulingBridgeCooldown    = 2 * time.Minute
	openAIOAuth429StormWindow             = 10 * time.Second
	openAIOAuth429StormThreshold          = 20
	openAIOAuth429StormMaxAccountSwitches = 1

	// 单次用户请求不能因一个上游故障把整个账号池依次打穿。这里的数字表示
	// 初始账号失败后，最多还允许切换多少个其他账号；仍会被全局配置上限截断。
	// 模型临时故障由账号+模型熔断负责隔离，只给一次健康账号兜底机会；额度和
	// 凭据故障更可能是账号特有问题，因此允许略多的替换次数寻找可用账号。
	openAIFailoverModelTransientSwitches = 1
	openAIFailoverAccountSwitches        = 2
	openAIFailoverCredentialSwitches     = 3

	// 切换前的短退避只用于上游容量/账号状态类错误，避免一批并发请求在同一
	// 时刻继续冲向相同故障域。凭据错误无需等待，应尽快切到下一账号。
	openAIFailoverModelTransientBackoff = 100 * time.Millisecond
	openAIFailoverAccountBackoffBase    = 100 * time.Millisecond
	openAIFailoverAccountBackoffMax     = 400 * time.Millisecond
)

type openAIUpstreamFailureClass string

const (
	openAIUpstreamFailureNonRetryable   openAIUpstreamFailureClass = "non_retryable"
	openAIUpstreamFailureModelTransient openAIUpstreamFailureClass = "model_transient"
	openAIUpstreamFailureAccount        openAIUpstreamFailureClass = "account"
	openAIUpstreamFailureCredential     openAIUpstreamFailureClass = "credential"
)

type openAIOAuth429Disposition uint8

const (
	openAIOAuth429Transient openAIOAuth429Disposition = iota
	openAIOAuth429Quota5h
	openAIOAuth429Quota7d
	openAIOAuth429QuotaReset
)

// classifyOpenAIOAuth429 区分 Codex 5 小时/7 天配额耗尽与普通瞬时 429。
// 没有明确耗尽或重置信号时才进入短暂同账号重试窗口，避免把正常抖动误判为长期停调。
func classifyOpenAIOAuth429(headers http.Header, responseBody []byte) (openAIOAuth429Disposition, *time.Time) {
	if snapshot := ParseCodexRateLimitHeaders(headers); snapshot != nil {
		if normalized := snapshot.Normalize(); normalized != nil {
			if normalized.Used7dPercent != nil && *normalized.Used7dPercent >= 100 {
				if normalized.Reset7dSeconds != nil {
					now := time.Now()
					resetAt := now.Add(time.Duration(*normalized.Reset7dSeconds) * time.Second)
					return openAIOAuth429Quota7d, &resetAt
				}
				return openAIOAuth429Quota7d, nil
			}
			if normalized.Used5hPercent != nil && *normalized.Used5hPercent >= 100 {
				if normalized.Reset5hSeconds != nil {
					now := time.Now()
					resetAt := now.Add(time.Duration(*normalized.Reset5hSeconds) * time.Second)
					return openAIOAuth429Quota5h, &resetAt
				}
				return openAIOAuth429Quota5h, nil
			}
		}
	}
	if resetAt := calculateOpenAI429ResetTime(headers); resetAt != nil {
		return openAIOAuth429QuotaReset, resetAt
	}
	if resetUnix := parseOpenAIRateLimitResetTime(responseBody); resetUnix != nil {
		resetAt := time.Unix(*resetUnix, 0)
		return openAIOAuth429QuotaReset, &resetAt
	}
	return openAIOAuth429Transient, nil
}

func openAIAccountStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, openAIAccountStateUpdateTimeout)
}

func isOpenAIOAuthAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike()
}

func supportsOpenAIAccountRuntimeBlock(account *Account) bool {
	if account == nil {
		return false
	}
	return account.Platform == PlatformOpenAI || account.Platform == PlatformGrok
}

// handleOpenAIAccountUpstreamError expects canonicalModel to be the model sent
// upstream after applying the account model mapping exactly once.
func (s *OpenAIGatewayService) handleOpenAIAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte, canonicalModel ...string) bool {
	if isOpenAIRequestScopedCapacityShed("", responseBody) {
		// 容量降载只描述本次请求，不代表账号或模型失效。
		return false
	}
	if account != nil && account.Platform == PlatformOpenAI && isOpenAIRequestScopedContentPolicyError(statusCode, responseBody) {
		// 内容策略拒绝属于本次输入，不得污染账号或模型的运行时可用状态。
		return false
	}
	if account != nil && account.Platform == PlatformGrok && isGrokContentPolicyRejection(statusCode, responseBody) {
		// 内容策略拒绝属于本次输入，不得污染账号或模型的运行时可用状态。
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()

	if account != nil && account.Platform == PlatformOpenAI && isOpenAIContextWindowError("", responseBody) {
		return false
	}

	if isOpenAIImageRateLimitError(statusCode, responseBody) {
		if s != nil && s.rateLimitService != nil {
			_ = s.rateLimitService.HandleOpenAIImageRateLimit(stateCtx, account, statusCode, headers, responseBody)
		}
		return false
	}

	if s == nil || account == nil {
		return false
	}
	// 先处理 Team 联动熔断，避免模型级冷却或临时规则提前返回时遗漏同 Team 账号。
	if s.rateLimitService != nil {
		s.rateLimitService.maybeHandleOpenAITeamLinkedError(stateCtx, account, statusCode, responseBody)
	}
	stateCtx = withTempUnschedulableModel(stateCtx, canonicalModel)
	if s.rateLimitService != nil && len(canonicalModel) > 0 && s.rateLimitService.HandleUpstreamModelNotFound(stateCtx, account, canonicalModel[0], statusCode, responseBody) {
		return true
	}
	if s.rateLimitService != nil && statusCode != http.StatusUnauthorized && len(canonicalModel) > 0 && strings.TrimSpace(canonicalModel[0]) != "" &&
		s.rateLimitService.HandleTempUnschedulable(stateCtx, account, statusCode, responseBody, canonicalModel[0]) {
		return true
	}
	if statusCode == http.StatusTooManyRequests {
		s.markOpenAIOAuth429RateLimited(stateCtx, account, headers, responseBody)
	}
	if s.rateLimitService == nil {
		return false
	}
	shouldDisable := s.rateLimitService.HandleUpstreamError(stateCtx, account, statusCode, headers, responseBody)
	modelTempMatched := statusCode != http.StatusUnauthorized && tempUnschedulableModel(stateCtx, nil) != "" &&
		len(matchTempUnschedulableRules(account, statusCode, responseBody)) > 0
	if shouldDisable && !modelTempMatched {
		s.BlockAccountScheduling(account, time.Time{}, "upstream_disable")
	}
	// Only API Key accounts use model-scoped cooling for generic transient
	// failures. Authentication, quota and ban signals remain account-scoped in
	// RateLimitService and must never be downgraded to a short model cooldown.
	// pool mode 已用请求内预算约束同账号重试。这里若先写账号+模型冷却，
	// 下一次获准重试会被本地冷却提前挡住。
	poolModeRetryable := account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode)
	if !shouldDisable && account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey &&
		shouldCooldownOpenAITransientUpstreamError(statusCode, responseBody) && !poolModeRetryable {
		model := ""
		if len(canonicalModel) > 0 {
			model = canonicalModel[0]
		}
		decision := s.recordOpenAIAccountModelTransientFailure(account, model, time.Now())
		if decision.FailureStreak > 0 {
			slog.Warn("openai_model_transient_state",
				"account_id", account.ID,
				"model", openAIAccountModelTransientModel(model),
				"failure_streak", decision.FailureStreak,
				"cooldown_ms", decision.Cooldown.Milliseconds(),
				"block_scope", "account_model",
			)
		}
	}
	return shouldDisable
}

func shouldCooldownOpenAITransientUpstreamError(statusCode int, responseBody []byte) bool {
	if isOpenAIRequestScopedCapacityShed("", responseBody) {
		return false
	}
	return classifyOpenAIUpstreamFailure(statusCode, responseBody) == openAIUpstreamFailureModelTransient
}

// classifyOpenAIUpstreamFailure is the shared safety boundary between short
// account+model cooling and the existing durable account/credential policy.
// It intentionally classifies only strong signals; unknown client errors stay
// non-retryable and are never converted into a model cooldown by message text.
func classifyOpenAIUpstreamFailure(statusCode int, responseBody []byte) openAIUpstreamFailureClass {
	if isOpenAIContextWindowError("", responseBody) {
		return openAIUpstreamFailureNonRetryable
	}
	switch statusCode {
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		return openAIUpstreamFailureModelTransient
	case http.StatusBadRequest:
		if isOpenAITransientProcessingError(statusCode, "", responseBody) {
			return openAIUpstreamFailureModelTransient
		}
		lower := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(responseBody)))
		if strings.Contains(lower, "organization has been disabled") || strings.Contains(lower, "identity verification is required") {
			return openAIUpstreamFailureCredential
		}
		return openAIUpstreamFailureNonRetryable
	case http.StatusUnauthorized, http.StatusForbidden:
		return openAIUpstreamFailureCredential
	case http.StatusPaymentRequired, http.StatusTooManyRequests, 529:
		return openAIUpstreamFailureAccount
	default:
		return openAIUpstreamFailureNonRetryable
	}
}

// OpenAIFailoverSwitchBudget returns the request-local maximum number of
// account switches allowed after this failure. It intentionally only changes
// native OpenAI account paths: Grok/other compatible providers keep their
// existing scheduler semantics until they gain their own error classifier.
//
// Request-scoped capacity shedding is retried on the same account with the
// existing bounded backoff, but never fan-outs to other accounts. A request
// validation error is also not replayed against another credential. This keeps
// a malformed or provider-wide request from multiplying upstream cost.
func OpenAIFailoverSwitchBudget(account *Account, failoverErr *UpstreamFailoverError, configuredMax int) int {
	if configuredMax <= 0 || failoverErr == nil || !failoverErr.ShouldRetryNextAccount() {
		return 0
	}
	if account == nil || account.Platform != PlatformOpenAI {
		return configuredMax
	}
	if failoverErr.RequestScopedTransient {
		return 0
	}
	// 这两类错误是账号或其专属代理的能力边界：相同可重放请求在另一账号
	// 可能成功，因此只给一次受控切换。不能把它们和普通 4xx 参数错误混为一谈。
	if failoverErr.IsOpenAIRequestBodyTooLarge() {
		return minOpenAIFailoverSwitchBudget(configuredMax, openAIFailoverModelTransientSwitches)
	}
	if failoverErr.StatusCode == http.StatusNotFound || failoverErr.StatusCode == http.StatusMethodNotAllowed {
		// 404/405 在 OpenAI-compatible（OpenAI 兼容）账号池中常表示当前
		// 账号未实现某模型或独立端点；仅允许一次替换账号，避免逐个打穿账号池。
		return minOpenAIFailoverSwitchBudget(configuredMax, openAIFailoverModelTransientSwitches)
	}

	switch classifyOpenAIUpstreamFailure(failoverErr.StatusCode, failoverErr.ResponseBody) {
	case openAIUpstreamFailureModelTransient:
		return minOpenAIFailoverSwitchBudget(configuredMax, openAIFailoverModelTransientSwitches)
	case openAIUpstreamFailureAccount:
		return minOpenAIFailoverSwitchBudget(configuredMax, openAIFailoverAccountSwitches)
	case openAIUpstreamFailureCredential:
		return minOpenAIFailoverSwitchBudget(configuredMax, openAIFailoverCredentialSwitches)
	default:
		return 0
	}
}

// OpenAIFailoverSwitchBackoff returns the delay before entering another
// scheduler pass. It is deliberately independent from the model circuit
// breaker: the breaker still owns account+actual-upstream-model cooling and
// half-open probing, while this delay only smooths one request's failover loop.
func OpenAIFailoverSwitchBackoff(account *Account, failoverErr *UpstreamFailoverError, switchCount int) time.Duration {
	if account == nil || account.Platform != PlatformOpenAI || failoverErr == nil || switchCount <= 0 || failoverErr.RequestScopedTransient {
		return 0
	}

	switch classifyOpenAIUpstreamFailure(failoverErr.StatusCode, failoverErr.ResponseBody) {
	case openAIUpstreamFailureModelTransient:
		return openAIFailoverModelTransientBackoff
	case openAIUpstreamFailureAccount:
		delay := openAIFailoverAccountBackoffBase
		for retry := 1; retry < switchCount && delay < openAIFailoverAccountBackoffMax; retry++ {
			delay *= 2
		}
		if delay > openAIFailoverAccountBackoffMax {
			return openAIFailoverAccountBackoffMax
		}
		return delay
	default:
		return 0
	}
}

func minOpenAIFailoverSwitchBudget(configuredMax, categoryMax int) int {
	if configuredMax <= 0 || categoryMax <= 0 {
		return 0
	}
	if configuredMax < categoryMax {
		return configuredMax
	}
	return categoryMax
}

func (s *OpenAIGatewayService) markOpenAIOAuth429RateLimited(ctx context.Context, account *Account, headers http.Header, responseBody []byte) {
	if s == nil || !isOpenAIOAuthAccount(account) {
		return
	}
	s.recordOpenAIOAuth429()
	disposition, resetAt := classifyOpenAIOAuth429(headers, responseBody)
	if disposition == openAIOAuth429Transient && s.openAIOAuth429RetryWindowActive(account) {
		return
	}

	cooldownUntil := time.Now().Add(openAIOAuth429FallbackCooldown)
	if resetAt != nil && resetAt.After(time.Now()) {
		cooldownUntil = *resetAt
	} else if s.rateLimitService != nil {
		if cooldown, ok := s.rateLimitService.get429FallbackCooldown(ctx, account); ok && cooldown > 0 {
			cooldownUntil = time.Now().Add(cooldown)
		}
	}
	s.BlockAccountScheduling(account, cooldownUntil, "429")
	s.openaiOAuth429RetryStartedAt.Delete(account.ID)
}

func (s *OpenAIGatewayService) shouldRetryOpenAIOAuth429OnSameAccountWithResponse(account *Account, statusCode int, shouldDisable bool, headers http.Header, responseBody []byte) bool {
	if shouldDisable || statusCode != http.StatusTooManyRequests || !isOpenAIOAuthAccount(account) || account.IsShadow() || s.isOpenAIAccountRuntimeBlocked(account) {
		return false
	}
	disposition, _ := classifyOpenAIOAuth429(headers, responseBody)
	return disposition == openAIOAuth429Transient && s.openAIOAuth429RetryWindowActive(account)
}

// decorateOpenAIOAuth429FailoverError adds the request-local retry window to a
// failover error only for a transient OAuth 429. Quota exhaustion remains an
// account cooldown and must never be retried against the same credential.
func (s *OpenAIGatewayService) decorateOpenAIOAuth429FailoverError(failoverErr *UpstreamFailoverError, account *Account, statusCode int, shouldDisable bool, classificationHeaders http.Header, responseHeaders http.Header, responseBody []byte) *UpstreamFailoverError {
	if failoverErr == nil || !s.shouldRetryOpenAIOAuth429OnSameAccountWithResponse(account, statusCode, shouldDisable, classificationHeaders, responseBody) {
		return failoverErr
	}
	failoverErr.RetryableOnSameAccount = true
	failoverErr.SameAccountRetryDeadline = s.openAIOAuth429RetryDeadline(account)
	failoverErr.SameAccountRetryMax = openAIOAuth429MaxAccountAttempts
	failoverErr.SameAccountRetryDelay = openAIOAuth429SameAccountRetryDelay(responseHeaders, failoverErr.SameAccountRetryDeadline)
	return failoverErr
}

// ShouldRetryOpenAIOAuth429 供 RateLimitService 在持久化配额冷却前判断瞬时 429。
func (s *OpenAIGatewayService) ShouldRetryOpenAIOAuth429(account *Account, headers http.Header, responseBody []byte) bool {
	return s.shouldRetryOpenAIOAuth429OnSameAccountWithResponse(account, http.StatusTooManyRequests, false, headers, responseBody)
}

func (s *OpenAIGatewayService) openAIOAuth429RetryWindowActive(account *Account) bool {
	if s == nil || !isOpenAIOAuthAccount(account) || account.IsShadow() {
		return false
	}
	now := time.Now()
	value, _ := s.openaiOAuth429RetryStartedAt.LoadOrStore(account.ID, now)
	startedAt, ok := value.(time.Time)
	if !ok {
		startedAt = now
		s.openaiOAuth429RetryStartedAt.Store(account.ID, startedAt)
	}
	if now.Before(startedAt.Add(openAIOAuth429RetryWindow)) {
		return true
	}
	s.openaiOAuth429RetryStartedAt.Delete(account.ID)
	return false
}

func (s *OpenAIGatewayService) openAIOAuth429RetryDeadline(account *Account) time.Time {
	if s == nil || account == nil {
		return time.Time{}
	}
	if value, ok := s.openaiOAuth429RetryStartedAt.Load(account.ID); ok {
		if startedAt, ok := value.(time.Time); ok {
			return startedAt.Add(openAIOAuth429RetryWindow)
		}
	}
	return time.Time{}
}

func openAIOAuth429SameAccountRetryDelay(headers http.Header, deadline time.Time) time.Duration {
	delay := openAIOAuth429RetryDelay
	if resetAt := parseRetryAfterResetTime(headers, time.Now()); resetAt != nil && resetAt.After(time.Now()) {
		delay = resetAt.Sub(time.Now())
	}
	if delay > openAIOAuth429MaxRetryDelay {
		delay = openAIOAuth429MaxRetryDelay
	}
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if delay > remaining {
			delay = remaining
		}
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func (s *OpenAIGatewayService) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	if s == nil || !supportsOpenAIAccountRuntimeBlock(account) {
		return
	}
	mu := s.openAIAccountRuntimeBlockLock(account.ID)
	mu.Lock()
	defer mu.Unlock()
	_, _ = s.blockAccountSchedulingLocked(account, until, reason)
}

func (s *OpenAIGatewayService) openAIAccountRuntimeBlockLock(accountID int64) *sync.Mutex {
	actual, _ := s.openaiAccountRuntimeBlockLocks.LoadOrStore(accountID, &sync.Mutex{})
	mu, ok := actual.(*sync.Mutex)
	if !ok {
		mu = &sync.Mutex{}
		s.openaiAccountRuntimeBlockLocks.Store(accountID, mu)
	}
	return mu
}

// blockAccountSchedulingLocked 返回本次写入的 generation（世代）和是否真正改变。
// Grok 凭据 CAS 失败时仅回滚自己安装的运行时拉黑，不能清掉并发请求的新拉黑。
func (s *OpenAIGatewayService) blockAccountSchedulingLocked(account *Account, until time.Time, _ string) (uint64, bool) {
	generation := s.openaiAccountRuntimeBlockSequence.Add(1)
	s.openaiAccountRuntimeBlockGeneration.Store(account.ID, generation)
	now := time.Now()
	blockUntil := until
	if blockUntil.IsZero() || !blockUntil.After(now) {
		blockUntil = now.Add(openAIStopSchedulingBridgeCooldown)
	}

	for {
		current, loaded := s.openaiAccountRuntimeBlockUntil.Load(account.ID)
		if !loaded {
			actual, stored := s.openaiAccountRuntimeBlockUntil.LoadOrStore(account.ID, blockUntil)
			if !stored {
				return generation, true
			}
			current = actual
		}

		currentUntil, ok := current.(time.Time)
		if !ok || currentUntil.IsZero() {
			if s.openaiAccountRuntimeBlockUntil.CompareAndSwap(account.ID, current, blockUntil) {
				return generation, true
			}
			continue
		}
		if !blockUntil.After(currentUntil) {
			return generation, false
		}
		if s.openaiAccountRuntimeBlockUntil.CompareAndSwap(account.ID, current, blockUntil) {
			return generation, true
		}
	}
}

func (s *OpenAIGatewayService) ClearAccountSchedulingBlock(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	mu := s.openAIAccountRuntimeBlockLock(accountID)
	mu.Lock()
	defer mu.Unlock()
	s.openaiAccountRuntimeBlockUntil.Delete(accountID)
	s.openaiAccountRuntimeBlockGeneration.Store(accountID, s.openaiAccountRuntimeBlockSequence.Add(1))
}

func (s *OpenAIGatewayService) isOpenAIAccountRuntimeBlocked(account *Account) bool {
	if s == nil || !supportsOpenAIAccountRuntimeBlock(account) {
		return false
	}
	mu := s.openAIAccountRuntimeBlockLock(account.ID)
	mu.Lock()
	defer mu.Unlock()
	value, ok := s.openaiAccountRuntimeBlockUntil.Load(account.ID)
	if !ok {
		return false
	}
	cooldownUntil, ok := value.(time.Time)
	if !ok || cooldownUntil.IsZero() {
		s.openaiAccountRuntimeBlockUntil.Delete(account.ID)
		s.openaiAccountRuntimeBlockGeneration.Store(account.ID, s.openaiAccountRuntimeBlockSequence.Add(1))
		return false
	}
	if time.Now().Before(cooldownUntil) {
		return true
	}
	s.openaiAccountRuntimeBlockUntil.Delete(account.ID)
	s.openaiAccountRuntimeBlockGeneration.Store(account.ID, s.openaiAccountRuntimeBlockSequence.Add(1))
	return false
}

func (s *OpenAIGatewayService) getOpenAIAccountModelTransientState() *openAIAccountModelTransientState {
	if s == nil {
		return nil
	}
	s.openaiModelTransientOnce.Do(func() {
		if s.openaiModelTransient == nil {
			s.openaiModelTransient = newOpenAIAccountModelTransientState(openAIModelTransientDefaultMax)
		}
	})
	return s.openaiModelTransient
}

func canonicalOpenAIAccountSchedulingModel(account *Account, requestedModel string) string {
	return canonicalOpenAIAccountSchedulingModelForGroup(account, nil, requestedModel)
}

// canonicalOpenAIAccountSchedulingModelForGroup resolves the exact upstream
// model key used by a scheduling decision. Account-group mappings must be part
// of this key: the same customer model can intentionally resolve to different
// upstream models in two groups, and their cooldown/health must stay isolated.
func canonicalOpenAIAccountSchedulingModelForGroup(account *Account, groupID *int64, requestedModel string) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if mapped, _ := account.ResolveModelForGroupID(groupID, model); strings.TrimSpace(mapped) != "" {
		return mapped
	}
	return model
}

func openAIAccountModelTransientModel(canonicalModel string) string {
	return normalizeOpenAIAccountModelTransientModel(canonicalModel)
}

func (s *OpenAIGatewayService) recordOpenAIAccountModelTransientFailure(account *Account, canonicalModel string, now time.Time) openAIAccountModelTransientDecision {
	if s == nil || account == nil {
		return openAIAccountModelTransientDecision{}
	}
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return openAIAccountModelTransientDecision{}
	}
	return state.recordFailure(account.ID, openAIAccountModelTransientModel(canonicalModel), now)
}

func (s *OpenAIGatewayService) blockOpenAIAccountModelImmediately(account *Account, canonicalModel string, now time.Time, cooldown time.Duration) openAIAccountModelTransientDecision {
	if s == nil || account == nil {
		return openAIAccountModelTransientDecision{}
	}
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return openAIAccountModelTransientDecision{}
	}
	return state.blockImmediately(account.ID, openAIAccountModelTransientModel(canonicalModel), now, cooldown)
}

func (s *OpenAIGatewayService) clearOpenAIAccountModelTransientState(accountID int64, model string) {
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return
	}
	state.recordSuccess(accountID, model, time.Now())
}

// TryAcquireOpenAIAccountModelRecoveryProbe serializes recovery traffic for a
// cooled account+model pair. It is deliberately invoked after ordinary
// eligibility filtering and just before account-slot acquisition, so merely
// inspecting candidates cannot consume the one allowed recovery probe.
func (s *OpenAIGatewayService) TryAcquireOpenAIAccountModelRecoveryProbe(account *Account, requestedModel string) bool {
	return s.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(account, nil, requestedModel)
}

// TryAcquireOpenAIAccountModelRecoveryProbeForGroup is the group-aware form
// used by the scheduler. It prevents a recovered model in group A from
// consuming or bypassing the recovery probe lease of a different mapping in
// group B.
func (s *OpenAIGatewayService) TryAcquireOpenAIAccountModelRecoveryProbeForGroup(account *Account, groupID *int64, requestedModel string) bool {
	if s == nil || account == nil {
		return true
	}
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return true
	}
	canonicalModel := canonicalOpenAIAccountSchedulingModelForGroup(account, groupID, requestedModel)
	return state.tryAcquireRecoveryProbe(account.ID, openAIAccountModelTransientModel(canonicalModel), time.Now())
}

// requiresOpenAIAccountModelRecoveryProbeForGroup keeps wait-plan selection
// from treating a cooled account+actual-upstream-model pair as an ordinary
// queue candidate. A recovery probe is only safe when the account slot is
// acquired immediately; otherwise several queued callers could be released
// into the same half-open model together.
func (s *OpenAIGatewayService) requiresOpenAIAccountModelRecoveryProbeForGroup(account *Account, groupID *int64, requestedModel string) bool {
	if s == nil || account == nil {
		return false
	}
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return false
	}
	canonicalModel := canonicalOpenAIAccountSchedulingModelForGroup(account, groupID, requestedModel)
	return state.requiresRecoveryProbe(account.ID, openAIAccountModelTransientModel(canonicalModel), time.Now())
}

// ReleaseOpenAIAccountModelRecoveryProbe releases a probe that never reached
// upstream forwarding, such as an account slot acquisition failure. Real
// request outcomes release the probe through ReportOpenAIAccountScheduleResult.
func (s *OpenAIGatewayService) ReleaseOpenAIAccountModelRecoveryProbe(account *Account, requestedModel string) {
	s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, nil, requestedModel)
}

// ReleaseOpenAIAccountModelRecoveryProbeForGroup releases only the lease for
// the model resolved by this group. See the acquire counterpart for why the
// group participates in the canonical model key.
func (s *OpenAIGatewayService) ReleaseOpenAIAccountModelRecoveryProbeForGroup(account *Account, groupID *int64, requestedModel string) {
	if s == nil || account == nil {
		return
	}
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return
	}
	canonicalModel := canonicalOpenAIAccountSchedulingModelForGroup(account, groupID, requestedModel)
	state.releaseRecoveryProbe(account.ID, openAIAccountModelTransientModel(canonicalModel))
}

// OpenAIAccountModelHealth reports process-local observed health. Model
// declarations remain a separate configuration fact, preventing the UI from
// presenting an untested configured model as recently proven available.
func (s *OpenAIGatewayService) OpenAIAccountModelHealth(accountID int64) []OpenAIAccountModelHealth {
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return nil
	}
	return state.snapshotForAccount(accountID, time.Now())
}

func (s *OpenAIGatewayService) isOpenAIAccountModelRuntimeBlocked(account *Account, requestedModel string) bool {
	return s.isOpenAIAccountModelRuntimeBlockedForGroup(account, nil, requestedModel)
}

func (s *OpenAIGatewayService) isOpenAIAccountModelRuntimeBlockedForGroup(account *Account, groupID *int64, requestedModel string) bool {
	if s == nil || account == nil {
		return false
	}
	state := s.getOpenAIAccountModelTransientState()
	if state == nil {
		return false
	}
	canonicalModel := canonicalOpenAIAccountSchedulingModelForGroup(account, groupID, requestedModel)
	return state.isBlocked(account.ID, openAIAccountModelTransientModel(canonicalModel), time.Now())
}

func (s *OpenAIGatewayService) isOpenAIAccountRequestRuntimeBlocked(account *Account, requestedModel string) bool {
	return s.isOpenAIAccountRequestRuntimeBlockedForGroup(account, nil, requestedModel)
}

// isOpenAIAccountRequestRuntimeBlockedForGroup keeps durable account-wide
// blocks (credential/quota) shared while keeping transient failures scoped to
// the actual account+upstream-model pair selected for this group.
func (s *OpenAIGatewayService) isOpenAIAccountRequestRuntimeBlockedForGroup(account *Account, groupID *int64, requestedModel string) bool {
	return s != nil && (s.isOpenAIAccountRuntimeBlocked(account) || s.isOpenAIAccountModelRuntimeBlockedForGroup(account, groupID, requestedModel))
}

func (s *OpenAIGatewayService) recordOpenAIOAuth429() {
	if s == nil {
		return
	}
	now := time.Now()
	windowStart := s.openaiOAuth429WindowStartUnixNano.Load()
	if windowStart == 0 || now.Sub(time.Unix(0, windowStart)) >= openAIOAuth429StormWindow {
		if s.openaiOAuth429WindowStartUnixNano.CompareAndSwap(windowStart, now.UnixNano()) {
			s.openaiOAuth429WindowCount.Store(1)
			return
		}
	}
	s.openaiOAuth429WindowCount.Add(1)
}

func (s *OpenAIGatewayService) isOpenAIOAuth429Storm() bool {
	if s == nil {
		return false
	}
	windowStart := s.openaiOAuth429WindowStartUnixNano.Load()
	if windowStart == 0 || time.Since(time.Unix(0, windowStart)) >= openAIOAuth429StormWindow {
		return false
	}
	return s.openaiOAuth429WindowCount.Load() >= openAIOAuth429StormThreshold
}

func (s *OpenAIGatewayService) ShouldStopOpenAIOAuth429Failover(account *Account, statusCode int, failedSwitches int) bool {
	if statusCode != http.StatusTooManyRequests || failedSwitches < openAIOAuth429StormMaxAccountSwitches {
		return false
	}
	if !isOpenAIOAuthAccount(account) {
		return false
	}
	return s.isOpenAIOAuth429Storm()
}
