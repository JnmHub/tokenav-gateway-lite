package service

// [tokenav 定制] 本文件由 v0.2.5 合并工单生成：
// 来源：官方开源版 sub2api v0.2.5 的 openai_gateway_scheduling.go，
// 仅提取我方仓库 internal/service 中尚不存在的声明（新功能补齐），
// 我方已有的同名实现一律保留，不在此文件重复定义。

import (
	"log/slog"
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/tidwall/gjson"
	"strings"
	"time"
)


// openAIRequestPayloadView unwraps Responses WebSocket event envelopes while
// leaving ordinary HTTP objects untouched even when they contain a response
// field for another purpose.
func openAIRequestPayloadView(body []byte) gjson.Result {
	root := parseRawJSONView(body)
	eventType := strings.ToLower(strings.TrimSpace(root.Get("type").String()))
	if strings.HasPrefix(eventType, "response.") {
		if response := root.Get("response"); response.Exists() && response.IsObject() {
			return response
		}
	}
	return root
}

// grokPreviousResponseSessionSeed returns a stable sticky seed from a Responses
// previous_response_id. Only resp_* response ids are accepted; message ids and
// unknown shapes must not pin sticky routing or prompt-cache identity.
func grokPreviousResponseSessionSeed(body []byte) string {
	id := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if id == "" {
		return ""
	}
	if ClassifyOpenAIPreviousResponseIDKind(id) != OpenAIPreviousResponseIDKindResponseID {
		return ""
	}
	// Namespace so content-derived seeds never collide with response ids.
	return "grok-prev-resp:" + id
}

// grokStickyAffinitySeed scopes sticky routing by model without changing the
// upstream prompt_cache_key written by applyGrokResponsesCacheIdentity.
func grokStickyAffinitySeed(sessionID string, body []byte) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	model := ""
	if len(body) > 0 {
		model = strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
	}
	if model == "" {
		return "grok-affinity:v1:" + sessionID
	}
	return "grok-affinity:v1:" + model + ":" + sessionID
}

// SelectAccountForTokenCount selects an account for a non-billable token-count
// request. It applies the normal platform, model, capability, and runtime
// eligibility checks without acquiring or waiting for a generation slot.
func (s *OpenAIGatewayService) SelectAccountForTokenCount(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	requiredCapability OpenAIEndpointCapability,
	platform string,
) (*Account, error) {
	ctx = WithOpenAIProfitControlSuppressed(ctx)
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	// [tokenav 定制] 按我们的 selectAccountForModelWithExclusions 签名重排参数
	return s.selectAccountForModelWithExclusions(
		ctx,
		groupID,
		sessionHash,
		requestedModel,
		nil,
		false,
		0,
		requiredCapability,
		platform,
	)
}

// openAICompatibleAccountEligibilityFailureReason mirrors the legacy boolean
// eligibility check while naming its first veto point. Load-batch selection uses
// the reason only for server-side no-account diagnostics; the admission behavior
// remains unchanged.
func openAICompatibleAccountEligibilityFailureReason(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) string {
	if reason := openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability); reason != "" {
		return reason
	}
	// 分组利润控制：legacy 引擎的粘性/候选循环与 DB recheck 共用
	// 本判定，任何 fallback 都不能把利润不合格账号重新放回候选。
	if vetoed, reason := openAIProfitControlVetoReason(ctx, account); vetoed {
		return reason
	}
	return ""
}

// isOpenAICompatibleAccountEligibleForRequestBeforeProfit applies every
// ordinary scheduling gate. Legacy selection uses it before classifying the
// profit veto so earlier failures retain their actual reason.
func isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) bool {
	return openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability) == ""
}

func openAICompatibleAccountEligibilityFailureReasonBeforeProfit(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) string {
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if account == nil {
		return "account_nil"
	}
	if account.Platform != platform || !account.IsOpenAICompatible() {
		return "platform_mismatch"
	}
	if !account.IsSchedulableForModelWithContext(ctx, requestedModel) {
		if account.IsSchedulable() {
			return "model_rate_limited"
		}
		return "not_schedulable"
	}
	if account.IsOpenAI() {
		if paused, reason := shouldAutoPauseOpenAIAccountByQuota(ctx, account); paused {
			// Debug level: this fires per-candidate on the scheduling hot path, so Info
			// would amplify into log spam once several accounts cross the threshold.
			slog.Debug("account_auto_paused_by_quota",
				"account_id", account.ID,
				"window", reason.window,
				"threshold", reason.threshold,
				"utilization", reason.utilization,
			)
			if reason.window != "" {
				return "quota_auto_pause_" + reason.window
			}
			return "quota_auto_pause"
		}
	}
	if account.IsGrok() {
		if paused, reason := shouldAutoPauseGrokAccountByQuota(account); paused {
			slog.Debug("grok_account_auto_paused_by_quota",
				"account_id", account.ID,
				"window", reason.window,
				"threshold", reason.threshold,
				"utilization", reason.utilization,
			)
			if reason.window != "" {
				return "quota_auto_pause_" + reason.window
			}
			return "quota_auto_pause"
		}
	}
	if requestedModel != "" && !account.IsModelSupported(requestedModel) {
		return "model_not_supported"
	}
	if !account.SupportsOpenAIEndpointCapability(requiredCapability) {
		if account.IsGrok() && requiredCapability == OpenAIEndpointCapabilityGrokMediaGeneration {
			_, reason := account.GrokMediaGenerationEligibility()
			slog.Debug("grok_media_account_ineligible", "account_id", account.ID, "reason", reason)
		}
		return "capability_mismatch"
	}
	if requireCompact && openAICompactSupportTier(account) == 0 {
		return "compact_unsupported"
	}
	return ""
}

// 绝对时间优先；相对倒计时必须锚定快照采样时间，不能随每次评分向后滑动。
func openAICodexWindowResetAt(extra map[string]any, window string) (time.Time, bool) {
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
	updatedAt, err := parseTime(fmt.Sprint(extra["codex_usage_updated_at"]))
	if err != nil {
		return time.Time{}, false
	}
	return updatedAt.Add(time.Duration(resetAfter) * time.Second), true
}

// resolveOpenAIForwardMappedModels is the shared account mapping chain for
// Forward callers. billingModel retains the ordinary mapping used for usage
// accounting, while upstreamModel is the model the scheduler has admitted.
func resolveOpenAIForwardMappedModels(account *Account, requestedModel string, requireCompact bool) (billingModel, upstreamModel string) {
	requestedModel = strings.TrimSpace(requestedModel)
	if account != nil && account.IsOpenAIPassthroughEnabled() {
		billingModel = requestedModel
	} else if account != nil {
		billingModel = strings.TrimSpace(account.GetMappedModel(requestedModel))
	}
	if billingModel == "" {
		billingModel = requestedModel
	}
	upstreamModel = resolveOpenAIAccountUpstreamModelForRequest(context.Background(), nil, account, requestedModel, requireCompact)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = billingModel
	}
	return billingModel, upstreamModel
}

func resolveOpenAIErrorSchedulingModel(billingModel, upstreamModel string) string {
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return strings.TrimSpace(billingModel)
}

func (s *OpenAIGatewayService) resolveFreshSchedulableOpenAIAccountBeforeProfit(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	if account == nil {
		return nil
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)

	fresh := account
	if s.schedulerSnapshot != nil {
		current, err := s.getSchedulableAccount(ctx, account.ID)
		if err != nil || current == nil {
			return nil
		}
		fresh = current
	}

	if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, fresh, platform, requestedModel, requireCompact, requiredCapability) {
		return nil
	}
	if !parentHealthyForShadow(fresh, s.parentAccountLookup(ctx)) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlocked(fresh, requestedModel) {
		return nil
	}
	if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, fresh) {
		return nil
	}
	if s.isOpenAIProxyStreamQuarantined(ctx, fresh) {
		return nil
	}
	return fresh
}

// parentAccountLookup 返回供 parentHealthyForShadow 使用的母账号解析闭包:经 accountRepo
// 按 ID 取当前 Account(repo 为空时 fail-closed 返回 nil)。统一调度/粘连各路径的母账号解析,
// 取代各调用点重复内联的同一闭包(历史上 recheck 等路径还漏写过 accountRepo==nil 守卫)。
// L2 候选循环改用带 per-pass 缓存的 parentLookupL2,不走此方法。
func (s *OpenAIGatewayService) parentAccountLookup(ctx context.Context) func(int64) *Account {
	return func(id int64) *Account {
		if s.accountRepo == nil {
			return nil
		}
		a, _ := s.accountRepo.GetByID(ctx, id)
		return a
	}
}

func (s *OpenAIGatewayService) recheckSelectedOpenAIAccountFromDBBeforeProfit(ctx context.Context, account *Account, groupID *int64, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) *Account {
	if account == nil {
		return nil
	}
	platform = NormalizeOpenAICompatiblePlatform(platform)
	if s.schedulerSnapshot == nil || s.accountRepo == nil {
		if s.openAIGroupRequiresPrivacySet(ctx, groupID) && !account.IsPrivacySet() {
			return nil
		}
		if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, account, platform, requestedModel, requireCompact, requiredCapability) {
			return nil
		}
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, account) {
			return nil
		}
		if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) {
			return nil
		}
		if s.isOpenAIProxyStreamQuarantined(ctx, account) {
			return nil
		}
		return account
	}

	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || latest == nil {
		return nil
	}
	if !s.openAIAccountMatchesSchedulingGroup(latest, groupID) {
		return nil
	}
	if s.openAIGroupRequiresPrivacySet(ctx, groupID) && !latest.IsPrivacySet() {
		return nil
	}
	if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, latest, platform, requestedModel, requireCompact, requiredCapability) {
		return nil
	}
	if !parentHealthyForShadow(latest, s.parentAccountLookup(ctx)) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlocked(latest, requestedModel) {
		return nil
	}
	if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, latest) {
		return nil
	}
	if s.isOpenAIProxyStreamQuarantined(ctx, latest) {
		return nil
	}
	return latest
}

func (s *OpenAIGatewayService) openAIAccountMatchesSchedulingGroup(account *Account, groupID *int64) bool {
	if s != nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return account != nil
	}
	return openAIStickyAccountMatchesGroup(account, groupID)
}

// filterGrokFreeQuotaAccountsForOpenAI applies the same local free soft-gate as
// GatewayService / advanced scheduler, for OpenAI-compatible legacy selection.
func (s *OpenAIGatewayService) filterGrokFreeQuotaAccountsForOpenAI(ctx context.Context, accounts []Account) []Account {
	if s == nil {
		return accounts
	}
	return filterGrokFreeQuotaAccountsCore(ctx, s.cfg, s.usageLogRepo, &openaiGrokFreeQuotaGateCache, accounts)
}

func (s *OpenAIGatewayService) filterOpenAIAccountsBySchedulingThreshold(ctx context.Context, accounts []Account) []Account {
	if len(accounts) == 0 {
		return accounts
	}

	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, &accounts[i]) {
			continue
		}
		filtered = append(filtered, accounts[i])
	}
	return filtered
}

func (s *OpenAIGatewayService) isOpenAIAccountBlockedBySchedulingThreshold(ctx context.Context, account *Account) bool {
	if s == nil || s.rateLimitService == nil || account == nil {
		return false
	}
	return s.rateLimitService.ApplyAccountSchedulingThreshold(ctx, account)
}
