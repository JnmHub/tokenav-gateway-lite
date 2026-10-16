package service

import (
	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"fmt"
	"encoding/hex"
	"crypto/sha256"
	"encoding/json"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"database/sql"
	"strings"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// [v0.2.5 新增] 自动合并的缺失符号（生成脚本维护，勿手工排序）

// from openai_account_scheduler.go
func (s *OpenAIGatewayService) openAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if cached, ok := ctx.Value(openAIGroupPrivacyRequirementContextKey{}).(openAIGroupPrivacyRequirement); ok && cached.groupID == derefGroupID(groupID) {
		return cached.required
	}
	return s.loadOpenAIGroupRequiresPrivacySet(ctx, groupID)
}

// from ratelimit_service.go
func (s *RateLimitService) ApplyAccountSchedulingThreshold(ctx context.Context, account *Account) bool {
	if s == nil || s.settingService == nil || s.accountRepo == nil || account == nil || account.ID <= 0 {
		return false
	}
	if !account.IsActive() || !account.Schedulable {
		return false
	}

	now := time.Now().UTC()
	thresholds := s.settingService.GetAccountSchedulingThresholds(ctx)
	decision := EvaluateAccountSchedulingThreshold(account, thresholds, now)
	if !decision.ShouldPause || decision.Until == nil || !decision.Until.After(now) {
		s.applyAnthropicFableSchedulingThreshold(ctx, account, thresholds, now)
		return false
	}

	reason := BuildDetailedAccountSchedulingThresholdReason(AccountSchedulingThresholdReasonInput{
		Platform:         decision.Platform,
		Window:           decision.Window,
		Scope:            decision.Scope,
		ThresholdPercent: decision.ThresholdPercent,
		UsedPercent:      decision.UsedPercent,
		Until:            *decision.Until,
		Now:              now,
	})

	if accountHasSameSchedulingThresholdPause(account, *decision.Until, reason) {
		return true
	}
	if !account.IsSchedulable() {
		return false
	}

	account.TempUnschedulableUntil = cloneTimePtr(decision.Until)
	account.TempUnschedulableReason = reason
	s.notifyAccountSchedulingBlocked(account, *decision.Until, "account_scheduling_threshold")

	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, *decision.Until, reason); err != nil {
		slog.Warn("account_scheduling_threshold_set_temp_unsched_failed",
			"account_id", account.ID,
			"platform", decision.Platform,
			"window", decision.Window,
			"scope", decision.Scope,
			"threshold_percent", decision.ThresholdPercent,
			"used_percent", decision.UsedPercent,
			"until", decision.Until.UTC(),
			"error", err)
	} else if s.tempUnschedCache != nil {
		if state := tempUnschedStateFromStoredReason(reason, decision.Until.Unix()); state != nil {
			if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
				slog.Warn("account_scheduling_threshold_cache_set_failed", "account_id", account.ID, "error", err)
			}
		}
	}

	slog.Info("account_scheduling_threshold_temp_unschedulable",
		"account_id", account.ID,
		"platform", decision.Platform,
		"window", decision.Window,
		"scope", decision.Scope,
		"threshold_percent", decision.ThresholdPercent,
		"used_percent", decision.UsedPercent,
		"until", decision.Until.UTC())
	return true
}

// from openai_gateway_scheduling.go
func NormalizeOpenAICompatiblePlatform(platform string) string {
	switch platform {
	case PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo:
		return platform
	default:
		return PlatformOpenAI
	}
}

// from http_upstream_profile.go
func WithHTTPUpstreamPublicHostsOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamPublicHostsOnlyContextKey{}, true)
}

// [v0.2.5 新增] 手工补充的哨兵错误与常量
var ErrStickySessionNotFound = errors.New("sticky session not found")

var ErrOpenAIUpstreamStreamTruncated = errors.New("openai upstream stream truncated")

const OpenAIUpstreamStreamTruncatedCode = "openai_upstream_stream_truncated"

const OpenAIWSIngressModeHTTPBridge = "http_bridge"

// [v0.2.5 新增]
const UpstreamProviderExtraKey = "upstream_provider"

// [v0.2.5 新增]
func isSub2APIUpstreamAccount(account *Account) bool {
	if account == nil || account.Type != AccountTypeAPIKey || account.Extra == nil ||
		(account.Platform != PlatformOpenAI && !account.IsCNProvider()) {
		return false
	}
	provider, _ := account.Extra[UpstreamProviderExtraKey].(string)
	return strings.EqualFold(strings.TrimSpace(provider), UpstreamProviderSub2API)
}

// [v0.2.5 新增]
func validateSub2APIUpstreamMarker(platform, accountType string, extra map[string]any) error {
	if extra == nil {
		return nil
	}
	autoProbeRaw, autoProbeExists := extra[UpstreamBillingAutoProbeAccountExtraKey]
	if autoProbeExists && autoProbeRaw != nil {
		if _, ok := autoProbeRaw.(bool); !ok {
			return infraerrors.BadRequest("INVALID_UPSTREAM_BILLING_AUTO_PROBE", "upstream_billing_auto_probe_enabled must be a boolean")
		}
	}
	raw, exists := extra[UpstreamProviderExtraKey]
	if !exists || raw == nil {
		if enabled, _ := autoProbeRaw.(bool); enabled {
			return ErrUpstreamBillingProbeAccountInvalid
		}
		delete(extra, UpstreamBillingAutoProbeAccountExtraKey)
		delete(extra, UpstreamBillingProbeExtraKey)
		return nil
	}
	provider, ok := raw.(string)
	if !ok {
		return infraerrors.BadRequest("INVALID_UPSTREAM_PROVIDER", "upstream_provider must be a string")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		delete(extra, UpstreamProviderExtraKey)
		if enabled, _ := autoProbeRaw.(bool); enabled {
			return ErrUpstreamBillingProbeAccountInvalid
		}
		delete(extra, UpstreamBillingAutoProbeAccountExtraKey)
		delete(extra, UpstreamBillingProbeExtraKey)
		return nil
	}
	if provider != UpstreamProviderSub2API {
		return infraerrors.BadRequest("INVALID_UPSTREAM_PROVIDER", "unsupported upstream_provider")
	}
	if accountType != AccountTypeAPIKey || (platform != PlatformOpenAI && !IsCNProvider(platform)) {
		return infraerrors.BadRequest("INVALID_UPSTREAM_PROVIDER_ACCOUNT", "Sub2API upstream requires an OpenAI-compatible API key account")
	}
	extra[UpstreamProviderExtraKey] = UpstreamProviderSub2API
	if autoProbeRaw == nil {
		delete(extra, UpstreamBillingAutoProbeAccountExtraKey)
	}
	return nil
}

// [v0.2.5 新增]
func DecodeUpstreamBillingProbeSnapshot(extra map[string]any) *UpstreamBillingProbeSnapshot {
	if extra == nil {
		return nil
	}
	raw, ok := extra[UpstreamBillingProbeExtraKey]
	if !ok || raw == nil {
		return nil
	}
	// Scheduler snapshots created in-process already carry the typed value. Avoid
	// a JSON round trip on every candidate in the customer request path.
	switch snapshot := raw.(type) {
	case *UpstreamBillingProbeSnapshot:
		if validUpstreamBillingProbeStatus(snapshot.Status) {
			return snapshot
		}
		return nil
	case UpstreamBillingProbeSnapshot:
		if validUpstreamBillingProbeStatus(snapshot.Status) {
			copy := snapshot
			return &copy
		}
		return nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot UpstreamBillingProbeSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil
	}
	if validUpstreamBillingProbeStatus(snapshot.Status) {
		return &snapshot
	}
	return nil
}

// [v0.2.5 新增]
func upstreamBillingProbeConfigFingerprint(account *Account) string {
	if account == nil {
		return ""
	}
	identity := map[string]any{
		"platform":    account.Platform,
		"type":        account.Type,
		"credentials": account.Credentials,
		"proxy_id":    account.ProxyID,
		"provider":    strings.ToLower(strings.TrimSpace(extraString(account.Extra, UpstreamProviderExtraKey))),
	}
	if account.Proxy != nil {
		identity["proxy"] = map[string]any{
			"id": account.Proxy.ID, "protocol": account.Proxy.Protocol, "host": account.Proxy.Host,
			"port": account.Proxy.Port, "username": account.Proxy.Username, "password": account.Proxy.Password,
			"status": account.Proxy.Status,
		}
	}
	payload, err := json.Marshal(identity)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// [v0.2.5 新增]
type openAIGroupPrivacyRequirementContextKey struct{}

// [v0.2.5 新增]
type openAIGroupPrivacyRequirement struct {
	groupID  int64
	required bool
}

// [v0.2.5 新增]
func parseMinutes(hhmm string) (int, bool) {
	colon := strings.IndexByte(hhmm, ':')
	if (colon != 1 && colon != 2) || len(hhmm)-colon-1 != 2 {
		return 0, false
	}
	h := 0
	for i := 0; i < colon; i++ {
		d := hhmm[i] - '0'
		if d > 9 {
			return 0, false
		}
		h = h*10 + int(d)
	}
	m1, m2 := hhmm[colon+1]-'0', hhmm[colon+2]-'0'
	if m1 > 9 || m2 > 9 {
		return 0, false
	}
	m := int(m1)*10 + int(m2)
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// [v0.2.5 新增]
func tryAcquireDBAdvisoryLockWithError(ctx context.Context, db *sql.DB, lockID int64) (func(), bool, error) {
	if db == nil {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("open advisory-lock connection: %w", err)
	}

	acquired := false
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("query advisory lock: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}

	release := func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", lockID)
		_ = conn.Close()
	}
	return release, true, nil
}

// [v0.2.5 新增] loadOpenAIGroupRequiresPrivacySet
func (s *OpenAIGatewayService) loadOpenAIGroupRequiresPrivacySet(ctx context.Context, groupID *int64) bool {
	if s == nil || groupID == nil || s.schedulerSnapshot == nil {
		return false
	}
	group, err := s.schedulerSnapshot.GetGroupByID(ctx, *groupID)
	if err != nil {
		return true
	}
	return group != nil && group.RequirePrivacySet
}

// [v0.2.5 新增] GetAccountSchedulingThresholds
func (s *SettingService) GetAccountSchedulingThresholds(ctx context.Context) map[string]int {
	if s == nil || s.settingRepo == nil {
		return defaultAccountSchedulingThresholds()
	}
	if cached, ok := accountSchedulingThresholdsCache.Load().(*cachedAccountSchedulingThresholds); ok {
		if cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
			return cloneAccountSchedulingThresholds(cached.thresholds)
		}
	}

	result, err, _ := accountSchedulingThresholdsSF.Do(SettingKeyAccountSchedulingThresholds, func() (any, error) {
		if cached, ok := accountSchedulingThresholdsCache.Load().(*cachedAccountSchedulingThresholds); ok {
			if cached != nil && len(cached.thresholds) > 0 && time.Now().UnixNano() < cached.expiresAt {
				return cloneAccountSchedulingThresholds(cached.thresholds), nil
			}
		}

		thresholds := defaultAccountSchedulingThresholds()
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accountSchedulingThresholdsDBTimeout)
		defer cancel()

		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyAccountSchedulingThresholds)
		if err != nil {
			if errors.Is(err, ErrSettingNotFound) {
				accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{
					thresholds: cloneAccountSchedulingThresholds(thresholds),
					expiresAt:  time.Now().Add(accountSchedulingThresholdsCacheTTL).UnixNano(),
				})
				return cloneAccountSchedulingThresholds(thresholds), nil
			}
			slog.Warn("failed to get account scheduling thresholds, falling back to defaults", "error", err)
			accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{
				thresholds: cloneAccountSchedulingThresholds(thresholds),
				expiresAt:  time.Now().Add(accountSchedulingThresholdsErrorTTL).UnixNano(),
			})
			return cloneAccountSchedulingThresholds(thresholds), nil
		}

		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			if parsed, err := parseAccountSchedulingThresholdsSetting(trimmed); err != nil {
				slog.Warn("failed to parse account scheduling thresholds, falling back to defaults", "error", err)
			} else {
				thresholds = parsed
			}
		}

		accountSchedulingThresholdsCache.Store(&cachedAccountSchedulingThresholds{
			thresholds: cloneAccountSchedulingThresholds(thresholds),
			expiresAt:  time.Now().Add(accountSchedulingThresholdsCacheTTL).UnixNano(),
		})
		return cloneAccountSchedulingThresholds(thresholds), nil
	})
	if err != nil {
		return defaultAccountSchedulingThresholds()
	}
	if thresholds, ok := result.(map[string]int); ok {
		return cloneAccountSchedulingThresholds(thresholds)
	}
	return defaultAccountSchedulingThresholds()
}

// [v0.2.5 新增] applyAnthropicFableSchedulingThreshold
func (s *RateLimitService) applyAnthropicFableSchedulingThreshold(ctx context.Context, account *Account, thresholds map[string]int, now time.Time) {
	decision := evaluateAnthropicFableSchedulingThreshold(account, thresholds, now)
	if !decision.ShouldPause || decision.Until == nil || !decision.Until.After(now) {
		return
	}
	if account.isRateLimitActiveForKey(anthropicFableRateLimitKey) {
		return
	}

	reason := BuildDetailedAccountSchedulingThresholdReason(AccountSchedulingThresholdReasonInput{
		Platform:         decision.Platform,
		Window:           decision.Window,
		Scope:            decision.Scope,
		ThresholdPercent: decision.ThresholdPercent,
		UsedPercent:      decision.UsedPercent,
		Until:            *decision.Until,
		Now:              now,
	})
	setAccountModelRateLimitSnapshot(account, anthropicFableRateLimitKey, *decision.Until, reason, now)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, anthropicFableRateLimitKey, *decision.Until, reason); err != nil {
		slog.Warn("anthropic_fable_scheduling_threshold_set_model_limit_failed",
			"account_id", account.ID,
			"threshold_percent", decision.ThresholdPercent,
			"used_percent", decision.UsedPercent,
			"until", decision.Until.UTC(),
			"error", err)
		return
	}

	slog.Info("anthropic_fable_scheduling_threshold_model_limited",
		"account_id", account.ID,
		"scope", anthropicFableRateLimitKey,
		"threshold_percent", decision.ThresholdPercent,
		"used_percent", decision.UsedPercent,
		"until", decision.Until.UTC())
}

// [v0.2.5 新增]
func accountHasSameSchedulingThresholdPause(account *Account, until time.Time, reason string) bool {
	if account == nil || account.TempUnschedulableUntil == nil {
		return false
	}
	if account.TempUnschedulableUntil.UTC().Unix() != until.UTC().Unix() {
		return false
	}

	existing, ok := parseTempUnschedReasonPayload(account.TempUnschedulableReason)
	if !ok || existing.Source != AccountSchedulingThresholdReasonSource {
		return false
	}
	next, ok := parseTempUnschedReasonPayload(reason)
	if !ok || next.Source != AccountSchedulingThresholdReasonSource {
		return false
	}

	existing.TriggeredAtUnix = 0
	next.TriggeredAtUnix = 0
	return existing == next
}

// [v0.2.5 新增]
const UpstreamProviderSub2API  = "sub2api"

// [tokenav 定制] 本地实现
// 词表必须与 upstream_billing_probe.go 的状态常量保持一致：探针实际写入的是
// ok / unsupported / failed（"error" / "pending" 是早期遗留写法，保留兼容）。
// 这里一度只认 ok/error/pending，导致 status=failed 的快照在解码阶段就被判为无效、
// 直接丢掉 —— 探针失败但仍带着「上一次成功成本读数」时，调度器拿不到任何成本信息，
// 「failed + 新鲜 last-good 仍可用」这条降级路径实际从未生效。
func validUpstreamBillingProbeStatus(status string) bool {
	switch status {
	case "", "error", "pending": // 历史/缺省写法
		return true
	case UpstreamBillingProbeStatusOK, UpstreamBillingProbeStatusUnsupported, UpstreamBillingProbeStatusFailed:
		return true
	default:
		return false
	}
}

// [v0.2.5 新增]
var accountSchedulingThresholdsCache atomic.Value // *cachedAccountSchedulingThresholds

// [v0.2.5 新增]
var accountSchedulingThresholdsSF singleflight.Group

// [v0.2.5 新增]
const accountSchedulingThresholdsCacheTTL = 60 * time.Second

// [v0.2.5 新增]
const accountSchedulingThresholdsErrorTTL = 5 * time.Second

// [v0.2.5 新增]
const accountSchedulingThresholdsDBTimeout = 5 * time.Second

type cachedAccountSchedulingThresholds struct {
	thresholds map[string]int
	expiresAt  int64 // unix nano
}

func cloneAccountSchedulingThresholds(input map[string]int) map[string]int {
	if len(input) == 0 {
		return defaultAccountSchedulingThresholds()
	}
	cloned := make(map[string]int, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

// [tokenav 定制] defaultAccountSchedulingThresholds：新版 master 符号的本地实现（返回空表 = 全部关闭提前停调）。
func defaultAccountSchedulingThresholds() map[string]int {
	return map[string]int{}
}

// [v0.2.5 新增]
func setAccountModelRateLimitSnapshot(account *Account, scope string, resetAt time.Time, reason string, now time.Time) {
	if account == nil || strings.TrimSpace(scope) == "" {
		return
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	limits, ok := account.Extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		limits = make(map[string]any)
		account.Extra[modelRateLimitsKey] = limits
	}
	payload := map[string]any{
		"rate_limited_at":     now.UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		payload["reason"] = reason
	}
	limits[scope] = payload
}

// [v0.2.5 新增]
func parseAccountSchedulingThresholdsSetting(raw string) (map[string]int, error) {
	thresholds := defaultAccountSchedulingThresholds()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return thresholds, nil
	}
	parsed := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return thresholds, err
	}
	for _, platform := range AllowedSchedulingThresholdPlatforms {
		if value, ok := parsed[platform]; ok {
			thresholds[platform] = boundedIntOrDefault(value, 1, 100, 100)
		}
	}
	return thresholds, nil
}

// [v0.2.5 新增]
func boundedIntOrDefault(value, minValue, maxValue, defaultValue int) int {
	if value < minValue || value > maxValue {
		return defaultValue
	}
	return value
}

// [v0.2.5 新增]
func (s *SettingService) LoadForwardedClientIPSettings(ctx context.Context) error {
	if s == nil || s.cfg == nil || s.settingRepo == nil {
		return nil
	}

	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyAPIKeyACLTrustForwardedIP,
		SettingKeyForwardedClientIPHeaders,
		settingKeyForwardedClientIPModeV2,
	})
	if err != nil {
		s.cfg.SetForwardedClientIPSettings(false, nil)
		return fmt.Errorf("get forwarded client ip settings: %w", err)
	}

	enabled := s.cfg.Security.TrustForwardedIPForAPIKeyACL
	headers := s.cfg.ForwardedClientIPSettings().Headers
	storedValue, hasStoredValue := values[SettingKeyAPIKeyACLTrustForwardedIP]
	if hasStoredValue {
		enabled = storedValue == "true"
	}

	var headersErr error
	if storedHeaders, ok := values[SettingKeyForwardedClientIPHeaders]; ok {
		headers, headersErr = parseForwardedClientIPHeadersSetting(storedHeaders)
		if headersErr != nil {
			enabled = false
			headers = []string{}
			headersErr = fmt.Errorf("load forwarded client ip headers: %w", headersErr)
		}
	}

	updates := make(map[string]string)
	if _, hasStoredHeaders := values[SettingKeyForwardedClientIPHeaders]; !hasStoredHeaders {
		headersJSON, marshalErr := json.Marshal(headers)
		if marshalErr != nil {
			headers = []string{}
			headersErr = errors.Join(headersErr, fmt.Errorf("marshal forwarded client ip headers: %w", marshalErr))
			headersJSON = []byte("[]")
		}
		updates[SettingKeyForwardedClientIPHeaders] = string(headersJSON)
	}
	if values[settingKeyForwardedClientIPModeV2] != "true" {
		updates[settingKeyForwardedClientIPModeV2] = "true"
		// Before this migration, new installations persisted false by default.
		// Restore compatibility only when no trusted-proxy policy was configured.
		if headersErr == nil && hasStoredValue && !enabled && !s.cfg.Server.TrustedProxiesConfigured {
			enabled = true
			updates[SettingKeyAPIKeyACLTrustForwardedIP] = "true"
		}
	}
	if len(updates) > 0 {
		if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
			s.cfg.SetForwardedClientIPSettings(enabled, headers)
			return errors.Join(headersErr, fmt.Errorf("migrate forwarded client ip setting: %w", err))
		}
	}

	s.cfg.SetForwardedClientIPSettings(enabled, headers)
	return headersErr
}

// [v0.2.5 新增]
func (s *SettingService) MigrateOpenAIAllowClaudeCodeCodexPluginSetting(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexRestrictionPolicyDBTimeout)
	defer cancel()

	legacyValue, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAIAllowClaudeCodeCodexPlugin)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil
		}
		return fmt.Errorf("get deprecated %s setting: %w", SettingKeyOpenAIAllowClaudeCodeCodexPlugin, err)
	}
	if strings.TrimSpace(legacyValue) != "true" {
		return nil
	}

	rawWhitelist, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyWhitelist)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("get %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
	}

	var entries []openai.AllowedClientEntry
	if strings.TrimSpace(rawWhitelist) != "" {
		if err := json.Unmarshal([]byte(rawWhitelist), &entries); err != nil {
			return fmt.Errorf("parse %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
		}
	}
	if codexClientEntriesContain(entries, legacyClaudeCodeCodexWhitelistEntry) {
		return nil
	}

	entries = append(entries, legacyClaudeCodeCodexWhitelistEntry)
	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
	}
	if err := s.settingRepo.Set(dbCtx, SettingKeyCodexCLIOnlyWhitelist, string(encoded)); err != nil {
		return fmt.Errorf("set %s setting: %w", SettingKeyCodexCLIOnlyWhitelist, err)
	}
	s.codexRestrictionPolicySF.Forget("codex_restriction_policy")
	s.codexRestrictionPolicyCache.Store(&cachedCodexRestrictionPolicy{expiresAt: 0})
	return nil
}

// [v0.2.5 新增]
func (s *SettingService) MigrateCodexBodyFingerprintToSignals(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexRestrictionPolicyDBTimeout)
	defer cancel()

	if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyEngineFingerprintSignals); err == nil && strings.TrimSpace(v) != "" {
		return nil // 已配置/已迁移
	} else if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("get %s setting: %w", SettingKeyCodexCLIOnlyEngineFingerprintSignals, err)
	}

	bodyOn := false
	if v, err := s.settingRepo.GetValue(dbCtx, SettingKeyCodexCLIOnlyAllowBodyEngineFingerprint); err == nil {
		bodyOn = strings.TrimSpace(v) == "true"
	} else if !errors.Is(err, ErrSettingNotFound) {
		return fmt.Errorf("get deprecated %s setting: %w", SettingKeyCodexCLIOnlyAllowBodyEngineFingerprint, err)
	}

	seed := make([]openai.EngineFingerprintSignal, len(openai.DefaultEngineFingerprintSignals))
	copy(seed, openai.DefaultEngineFingerprintSignals)
	if bodyOn {
		for i := range seed {
			if seed[i].Type == openai.FingerprintSignalBodyPath {
				seed[i].Required = true
			}
		}
	}
	encoded, err := json.Marshal(seed)
	if err != nil {
		return fmt.Errorf("marshal %s setting: %w", SettingKeyCodexCLIOnlyEngineFingerprintSignals, err)
	}
	if err := s.settingRepo.Set(dbCtx, SettingKeyCodexCLIOnlyEngineFingerprintSignals, string(encoded)); err != nil {
		return fmt.Errorf("set %s setting: %w", SettingKeyCodexCLIOnlyEngineFingerprintSignals, err)
	}
	s.codexRestrictionPolicySF.Forget("codex_restriction_policy")
	s.codexRestrictionPolicyCache.Store(&cachedCodexRestrictionPolicy{expiresAt: 0})
	return nil
}

// [v0.2.5 新增]
func (s *SettingService) MigrateGrokDefaultTextModel(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexRestrictionPolicyDBTimeout)
	defer cancel()

	value, err := s.settingRepo.GetValue(dbCtx, SettingKeyGrokDefaultTextModel)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil
		}
		return fmt.Errorf("get %s setting: %w", SettingKeyGrokDefaultTextModel, err)
	}
	// Only migrate the value that was previously shipped as the built-in
	// default. Any other value is an explicit operator choice or a future
	// default and must remain unchanged.
	if strings.TrimSpace(value) != "grok-4.5" {
		return nil
	}
	if err := s.settingRepo.Set(dbCtx, SettingKeyGrokDefaultTextModel, "grok-4.6"); err != nil {
		return fmt.Errorf("set %s setting: %w", SettingKeyGrokDefaultTextModel, err)
	}
	return nil
}

// [v0.2.5 新增]
const codexRestrictionPolicyDBTimeout = 5 * time.Second

// [v0.2.5 新增]
var legacyClaudeCodeCodexWhitelistEntry = openai.AllowedClientEntry{
	Originator: "Claude Code",
	UAContains: []string{"Claude Code/"},
}

// [v0.2.5 新增]
func codexClientEntriesContain(entries []openai.AllowedClientEntry, want openai.AllowedClientEntry) bool {
	wantOriginator := strings.TrimSpace(want.Originator)
	if wantOriginator == "" {
		return false
	}
	wantMarkers := normalizedCodexClientMarkers(want.UAContains)
	if len(wantMarkers) == 0 {
		return false
	}
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.Originator), wantOriginator) {
			continue
		}
		gotMarkers := normalizedCodexClientMarkers(entry.UAContains)
		if len(gotMarkers) != len(wantMarkers) {
			continue
		}
		matched := true
		for marker := range wantMarkers {
			if _, ok := gotMarkers[marker]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// [v0.2.5 新增]
func parseForwardedClientIPHeadersSetting(value string) ([]string, error) {
	var headers []string
	if err := json.Unmarshal([]byte(value), &headers); err != nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: %w", err)
	}
	if headers == nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: value must be a JSON array")
	}
	normalized, err := config.NormalizeForwardedClientIPHeaders(headers)
	if err != nil {
		return nil, fmt.Errorf("parse forwarded_client_ip_headers: %w", err)
	}
	return normalized, nil
}

// [v0.2.5 新增]
type cachedCodexRestrictionPolicy struct {
	value     CodexRestrictionPolicy
	expiresAt int64
}

// [v0.2.5 新增]
func normalizedCodexClientMarkers(markers []string) map[string]struct{} {
	out := make(map[string]struct{}, len(markers))
	for _, m := range markers {
		m = strings.ToLower(strings.TrimSpace(m))
		if m != "" {
			out[m] = struct{}{}
		}
	}
	return out
}

// [v0.2.5 新增]
type CodexRestrictionPolicy struct {
	Whitelist                []openai.AllowedClientEntry      // 全局自由白名单（双因子 AND，放行官方集未覆盖的 app-server client）
	Blacklist                []openai.AllowedClientEntry      // 全局自由黑名单（OR，宽 deny）
	MinCodexVersion          string                           // 最低 Codex 引擎版本 semver；""=不校验
	MaxCodexVersion          string                           // 最高 Codex 引擎版本 semver；""=不校验
	AllowAppServerClients    bool                             // App Server 开关：对未列名客户端开闸（仍受引擎门约束）
	EngineFingerprintSignals []openai.EngineFingerprintSignal // 引擎指纹门信号列表（勾选 AND / 行内变体 OR）；缺省=默认种子(只勾 x-codex-)
}

// [v0.2.5 新增]
func profitControlPlatformSupported(platform string) bool {
	switch platform {
	case PlatformOpenAI, PlatformAnthropic, PlatformGemini, PlatformGrok, PlatformAntigravity:
		return true
	default:
		return false
	}
}

// [v0.2.5 新增] 记录型图片任务存储（与异步 ImageTaskStore 并存，重命名避免冲突）
type ImageTaskRecord struct {
	ID          string          `json:"id"`
	UserID      int64           `json:"user_id"`
	APIKeyID    int64           `json:"api_key_id"`
	Status      string          `json:"status"`
	HTTPStatus  int             `json:"http_status,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       json.RawMessage `json:"error,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	CompletedAt *int64          `json:"completed_at,omitempty"`
	ExpiresAt   int64           `json:"expires_at"`
}

type ImageRecordTaskStore interface {
	Save(ctx context.Context, task *ImageTaskRecord, ttl time.Duration) error
	Get(ctx context.Context, id string) (*ImageTaskRecord, error)
}

// [v0.2.5 新增]
var ErrGroupNotEmpty = errors.New("group not empty")

// [v0.2.5 新增] 邮箱别名去重/域名注册限制（本地实现）
var ErrEmailDomainRegistrationLimit = errors.New("email domain registration limit reached")

type emailAliasProbe struct {
	Local  string
	Domain string
}

// EmailAliasDedupProbes 返回用于去重探测的邮箱形态（本尊 + 去除 +后缀 变体）。
func EmailAliasDedupProbes(email string) []emailAliasProbe {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return nil
	}
	local, domain := email[:at], email[at+1:]
	probes := []emailAliasProbe{{Local: local, Domain: domain}}
	if plus := strings.Index(local, "+"); plus > 0 {
		probes = append(probes, emailAliasProbe{Local: local[:plus], Domain: domain})
	}
	return probes
}

func NormalizeEmailForAliasDedup(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func NormalizeRegistrationEmailDomain(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if at := strings.LastIndex(email, "@"); at >= 0 {
		return email[at+1:]
	}
	return ""
}

// [v0.2.5 新增] GetModelPlazaRuntime
func (s *SettingService) GetModelPlazaRuntime(ctx context.Context) ModelPlazaRuntime {
	vals, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyModelPlazaEnabled,
		SettingKeyModelPlazaRequireAuth,
		SettingKeyModelPlazaDescription,
	})
	if err != nil {
		return ModelPlazaRuntime{Enabled: false}
	}
	return ModelPlazaRuntime{
		Enabled:     vals[SettingKeyModelPlazaEnabled] == "true",
		RequireAuth: vals[SettingKeyModelPlazaRequireAuth] == "true",
		Description: vals[SettingKeyModelPlazaDescription],
	}
}

// [v0.2.5 新增] VerifyActionCaptchaIfEnabled
func (s *AuthService) VerifyActionCaptchaIfEnabled(ctx context.Context, proof CaptchaProof, remoteIP string) error {
	if s == nil || s.settingService == nil {
		return ErrServiceUnavailable
	}

	providerConfig, err := s.settingService.GetCaptchaProviderConfig(ctx)
	if err != nil {
		logger.LegacyPrintf("service.auth", "%s", "[Auth] Failed to read captcha provider settings")
		return ErrServiceUnavailable
	}
	tencentEnabled := providerConfig.Tencent.Enabled
	aliyunEnabled := providerConfig.Aliyun.Enabled
	if !tencentEnabled && !aliyunEnabled {
		return nil
	}
	if captchaProvidersConflict(providerConfig.TurnstileEnabled, tencentEnabled, aliyunEnabled) {
		return ErrCaptchaProviderConflict
	}
	if aliyunEnabled {
		if s.aliyunCaptchaService == nil {
			return ErrAliyunCaptchaNotConfigured
		}
		return s.aliyunCaptchaService.VerifyParamWithConfig(ctx, providerConfig.Aliyun, proof.TurnstileToken)
	}
	if s.tencentCaptchaService == nil {
		return ErrTencentCaptchaNotConfigured
	}
	return s.tencentCaptchaService.VerifyTicketWithConfig(
		ctx,
		providerConfig.Tencent,
		proof.TencentTicket,
		proof.TencentRandstr,
		remoteIP,
	)
}

// [tokenav 定制] OpsUpstreamModel 读取本次请求实际使用的上游模型（新版 master 的 getter）。
func OpsUpstreamModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v, ok := c.Get(OpsUpstreamModelKey); ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}

// [tokenav 定制] LookupDeletedKeyAudit：新版 master 符号，返回指定订单的删除Key审计记录。
// 我们仓库尚无对应审计表，返回 nil 表示无记录，调用方按"无审计"降级。
type DeletedKeyAuditRecord struct {
	UserID  int64
	KeyName string
}

// [tokenav 定制] LookupDeletedKeyAudit：新版 master 符号，返回指定前缀的删除Key审计记录。
// 我们仓库尚无对应审计表，返回 nil 表示无记录，调用方按"无审计"降级。
func (s *OpsService) LookupDeletedKeyAudit(ctx context.Context, keyPrefix string) (*DeletedKeyAuditRecord, error) {
	return nil, nil
}

// [v0.2.5 新增]
type ModelPlazaRuntime struct {
	Enabled     bool
	RequireAuth bool
	Description string
}

// [v0.2.5 新增]
type CaptchaProof struct {
	// TurnstileToken 承载 Cloudflare Turnstile token；阿里云验证码复用该字段承载 captchaVerifyParam
	TurnstileToken string
	TencentTicket  string
	TencentRandstr string
}

// [v0.2.5 新增] captchaProvidersConflict 判断是否多个验证码提供方同时启用。
func captchaProvidersConflict(turnstile, tencent, aliyun bool) bool {
	count := 0
	if turnstile {
		count++
	}
	if tencent {
		count++
	}
	if aliyun {
		count++
	}
	return count > 1
}

// [v0.2.5 新增]
func (s *SettingService) PasskeyEnabled(ctx context.Context) (bool, error) {
	if !s.passkeyConfigured() {
		return false, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPasskeyEnabled)
	if errors.Is(err, ErrSettingNotFound) {
		return true, nil // configured deployments default to enabled until the admin persists the switch
	}
	if err != nil {
		return false, fmt.Errorf("read passkey setting: %w", err)
	}
	return value == "true", nil
}

// [v0.2.5 新增]
func (s *SettingService) passkeyConfigured() bool {
	return s != nil && s.cfg != nil && s.cfg.WebAuthn.Enabled
}

// [tokenav 定制] GetGroupPlatformMap 本地实现：返回分组ID->平台映射（空实现，调用方自行兜底）。
func (s *PaymentConfigService) GetGroupPlatformMap(ctx context.Context, plans []*ent.SubscriptionPlan) (map[int64]string, error) {
	return map[int64]string{}, nil
}
