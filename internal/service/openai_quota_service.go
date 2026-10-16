package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/imroc/req/v3"
)

var ErrSparkShadowResetNotSupported = infraerrors.New(
	http.StatusConflict,
	"SPARK_SHADOW_RESET_NOT_SUPPORTED",
	"Spark shadow uses an independent read-only quota window; reset the parent account directly",
)

// Endpoints used by the OpenAI/ChatGPT/Codex quota query and reset feature.
const (
	chatGPTUsageURL             = "https://chatgpt.com/backend-api/wham/usage"
	chatGPTRateLimitCreditsURL  = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	chatGPTRateLimitResetURL    = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
	openaiQuotaUpstreamTimeout  = 20 * time.Second
	openaiQuotaCodexLanguageTag = "zh-CN"
	openaiQuotaSecFetchSite     = "none"
	openaiQuotaSecFetchMode     = "no-cors"
	openaiQuotaSecFetchDest     = "empty"
	openaiQuotaResetCreditsKey  = "codex_reset_credit_snapshot"
)

// OpenAIRateLimitWindow describes a single rate-limit window returned by
// /wham/usage. The upstream returns an explicit `null` window when the slot
// is unused, so consumers should treat a nil pointer as "no data".
type OpenAIRateLimitWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// OpenAIRateLimit is a rate-limit envelope (primary + optional secondary window).
type OpenAIRateLimit struct {
	Allowed         bool                   `json:"allowed"`
	LimitReached    bool                   `json:"limit_reached"`
	PrimaryWindow   *OpenAIRateLimitWindow `json:"primary_window,omitempty"`
	SecondaryWindow *OpenAIRateLimitWindow `json:"secondary_window,omitempty"`
}

// OpenAIAdditionalRateLimit describes a per-feature rate limit (e.g. Codex Spark).
type OpenAIAdditionalRateLimit struct {
	LimitName      string           `json:"limit_name"`
	MeteredFeature string           `json:"metered_feature"`
	RateLimit      *OpenAIRateLimit `json:"rate_limit,omitempty"`
}

// OpenAIRateLimitResetCredits captures the "available_count" surfaced for the
// rate_limit_reset_credit grant type, which the reset action consumes.
type OpenAIRateLimitResetCredits struct {
	AvailableCount int                                `json:"available_count"`
	Credits        []OpenAIRateLimitResetCreditDetail `json:"credits,omitempty"`
}

// OpenAIQuotaUsage is the typed projection of /wham/usage we expose to the UI.
// Fields not relevant to the quota card are intentionally omitted to keep the
// surface narrow; full upstream payload preservation is unnecessary.
type OpenAIQuotaUsage struct {
	UserID                string                       `json:"user_id,omitempty"`
	AccountID             string                       `json:"account_id,omitempty"`
	Email                 string                       `json:"email,omitempty"`
	PlanType              string                       `json:"plan_type,omitempty"`
	RateLimit             *OpenAIRateLimit             `json:"rate_limit,omitempty"`
	AdditionalRateLimits  []OpenAIAdditionalRateLimit  `json:"additional_rate_limits,omitempty"`
	RateLimitResetCredits *OpenAIRateLimitResetCredits `json:"rate_limit_reset_credits,omitempty"`
	FetchedAt             int64                        `json:"fetched_at"`
	autoResetCandidates []openAIAutoResetCreditCandidate // [v0.2.5 新增字段]
}

// OpenAIQuotaResetCredit captures the redeemed credit metadata returned by the
// reset endpoint.
type OpenAIQuotaResetCredit struct {
	ID              string `json:"id,omitempty"`
	ResetType       string `json:"reset_type,omitempty"`
	Status          string `json:"status,omitempty"`
	GrantedAt       string `json:"granted_at,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	RedeemStartedAt string `json:"redeem_started_at,omitempty"`
	RedeemedAt      string `json:"redeemed_at,omitempty"`
}

// OpenAIQuotaResetResult is the typed projection of /wham/rate-limit-reset-credits/consume.
// The inner Credit also carries `redeemed_at` (RFC3339 string); we deliberately do
// NOT add a top-level redeemed_at to avoid ambiguity with the nested field.
type OpenAIQuotaResetResult struct {
	Code         string                  `json:"code"`
	Credit       *OpenAIQuotaResetCredit `json:"credit,omitempty"`
	WindowsReset int                     `json:"windows_reset"`
}

// OpenAIQuotaService queries and consumes ChatGPT/Codex rate-limit reset credits
// for OpenAI OAuth accounts. It reuses the privacy client factory so all calls
// flow through the impersonated HTTP client (Cloudflare-friendly TLS fingerprint).
type OpenAIQuotaService struct {
	accountRepo          AccountRepository
	proxyRepo            ProxyRepository
	tokenProvider        *OpenAITokenProvider
	privacyClientFactory PrivacyClientFactory
	agentIdentityTaskMu  sync.Mutex
	agentIdentityWS      agentIdentityWSConnectionInvalidator
}

func (s *OpenAIQuotaService) redactQuotaErrorBody(ctx context.Context, accountID int64, body string) string {
	if s == nil || s.accountRepo == nil {
		return body
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return body
	}
	return string(redactAgentIdentitySensitiveBodyForAccount(ctx, s.accountRepo, account, []byte(body)))
}

// NewOpenAIQuotaService constructs a quota service. token provider is required —
// it ensures we always invoke upstream with a valid (refreshed-if-needed)
// access_token, sharing the same refresh/locking machinery used by the gateway.
func NewOpenAIQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	tokenProvider *OpenAITokenProvider,
	privacyClientFactory PrivacyClientFactory,
) *OpenAIQuotaService {
	return &OpenAIQuotaService{
		accountRepo:          accountRepo,
		proxyRepo:            proxyRepo,
		tokenProvider:        tokenProvider,
		privacyClientFactory: privacyClientFactory,
	}
}

// QueryUsage fetches the latest rate-limit/usage snapshot for the given OpenAI
// OAuth account. Returns infraerrors so the handler layer can map them to
// stable error codes / HTTP statuses.
func (s *OpenAIQuotaService) QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error) {
	accessToken, chatGPTAccountID, proxyURL, err := s.prepareUpstreamCall(ctx, accountID)
	if err != nil {
		return nil, err
	}

	client, err := s.privacyClientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_CLIENT_ERROR", "failed to build upstream client: %v", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	defer cancel()

	headers, expectedTaskID, isAgentIdentity, err := s.buildCodexQuotaHeaders(callCtx, accountID, accessToken, chatGPTAccountID)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", err)
	}
	var payload OpenAIQuotaUsage
	for attempt := 0; ; attempt++ {
		payload = OpenAIQuotaUsage{}
		resp, requestErr := client.R().
			SetContext(callCtx).
			SetHeaders(headers).
			SetSuccessResult(&payload).
			Get(chatGPTUsageURL)
		if requestErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_REQUEST_FAILED", "upstream request failed: %v", requestErr)
		}
		if resp.IsSuccessState() {
			break
		}
		if isAgentIdentity && attempt == 0 && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, resp.Bytes()) {
			if recoverErr := s.recoverAgentIdentityQuotaTask(callCtx, accountID, expectedTaskID); recoverErr != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "Agent Identity task recovery failed: %v", recoverErr)
			}
			headers, expectedTaskID, _, err = s.buildCodexQuotaHeaders(callCtx, accountID, "", chatGPTAccountID)
			if err != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to rebuild upstream authentication: %v", err)
			}
			continue
		}
		status := resp.StatusCode
		body := truncate(s.redactQuotaErrorBody(ctx, accountID, resp.String()), 240)
		slog.Warn("openai_quota_query_failed", "account_id", accountID, "status", status, "body", body)
		return nil, infraerrors.Newf(mapUpstreamStatus(status), "OPENAI_QUOTA_UPSTREAM_ERROR", "upstream returned %d: %s", status, body)
	}

	payload.FetchedAt = time.Now().Unix()
	if details := s.queryResetCreditDetails(callCtx, client, accessToken, chatGPTAccountID, accountID); details != nil {
		if payload.RateLimitResetCredits == nil {
			payload.RateLimitResetCredits = &OpenAIRateLimitResetCredits{}
		}
		if details.CreditListPresent {
			payload.RateLimitResetCredits.Credits = details.Credits
		}
		switch {
		case details.AvailableCount != nil:
			payload.RateLimitResetCredits.AvailableCount = *details.AvailableCount
		case details.CreditListPresent:
			payload.RateLimitResetCredits.AvailableCount = details.AvailableCreditCount
		}
	}
	return &payload, nil
}

// CacheResetCreditsSnapshot persists only reset credits whose expiration can
// still be verified. A positive count without a usable expiration must leave
// the previous snapshot untouched; otherwise an expired credit could remain
// clickable indefinitely after the upstream detail query degrades.
func (s *OpenAIQuotaService) CacheResetCreditsSnapshot(ctx context.Context, accountID int64, credits *OpenAIRateLimitResetCredits) error {
	if s == nil || s.accountRepo == nil {
		return infraerrors.New(http.StatusInternalServerError, "OPENAI_QUOTA_NOT_CONFIGURED", "openai quota service is not configured")
	}
	snapshot, err := normalizeOpenAIResetCreditsSnapshot(credits, time.Now())
	if err != nil {
		return err
	}
	if err := s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
		openaiQuotaResetCreditsKey: snapshot,
	}); err != nil {
		return infraerrors.New(
			http.StatusInternalServerError,
			"OPENAI_QUOTA_CACHE_WRITE_FAILED",
			"failed to cache reset-credit details",
		).WithCause(err)
	}
	return nil
}

func normalizeOpenAIResetCreditsSnapshot(credits *OpenAIRateLimitResetCredits, now time.Time) (*OpenAIRateLimitResetCredits, error) {
	if credits == nil {
		return nil, infraerrors.New(
			http.StatusBadGateway,
			"OPENAI_QUOTA_RESET_CREDITS_REFRESH_FAILED",
			"failed to refresh reset-credit expiration details; cached data was preserved",
		)
	}
	if credits.AvailableCount <= 0 {
		return &OpenAIRateLimitResetCredits{AvailableCount: 0}, nil
	}

	valid := make([]OpenAIRateLimitResetCreditDetail, 0, len(credits.Credits))
	for _, credit := range credits.Credits {
		expiresAt := strings.TrimSpace(credit.ExpiresAt)
		expiresAtTime, parseErr := time.Parse(time.RFC3339, expiresAt)
		if parseErr != nil || !expiresAtTime.After(now) {
			continue
		}
		valid = append(valid, OpenAIRateLimitResetCreditDetail{ExpiresAt: expiresAt})
	}
	if len(valid) == 0 {
		return nil, infraerrors.New(
			http.StatusBadGateway,
			"OPENAI_QUOTA_RESET_CREDITS_REFRESH_FAILED",
			"failed to refresh reset-credit expiration details; cached data was preserved",
		)
	}

	availableCount := credits.AvailableCount
	if availableCount > len(valid) {
		availableCount = len(valid)
	}
	return &OpenAIRateLimitResetCredits{
		AvailableCount: availableCount,
		Credits:        valid,
	}, nil
}

func (s *OpenAIQuotaService) queryResetCreditDetails(ctx context.Context, client *req.Client, accessToken, chatGPTAccountID string, accountID int64) *openAIRateLimitResetCreditDetails {
	headers, expectedTaskID, isAgentIdentity, err := s.buildCodexQuotaHeaders(ctx, accountID, accessToken, chatGPTAccountID)
	if err != nil {
		slog.Warn("openai_quota_reset_credit_details_failed", "account_id", accountID, "error", err)
		return nil
	}
	var resp *req.Response
	for attempt := 0; ; attempt++ {
		resp, err = client.R().SetContext(ctx).SetHeaders(headers).Get(chatGPTRateLimitCreditsURL)
		if err != nil {
			slog.Warn("openai_quota_reset_credit_details_failed", "account_id", accountID, "error", err)
			return nil
		}
		if resp.IsSuccessState() {
			break
		}
		if isAgentIdentity && attempt == 0 && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, resp.Bytes()) {
			if recoverErr := s.recoverAgentIdentityQuotaTask(ctx, accountID, expectedTaskID); recoverErr == nil {
				headers, expectedTaskID, _, err = s.buildCodexQuotaHeaders(ctx, accountID, "", chatGPTAccountID)
				if err == nil {
					continue
				}
			}
		}
		slog.Warn("openai_quota_reset_credit_details_failed", "account_id", accountID, "status", resp.StatusCode)
		return nil
	}

	details, err := parseOpenAIRateLimitResetCreditDetails(resp.Bytes())
	if err != nil {
		slog.Warn("openai_quota_reset_credit_details_parse_failed", "account_id", accountID, "error", err)
		return nil
	}
	if details.AvailableCount == nil && !details.CreditListPresent {
		return nil
	}
	return &details
}

// ResetCredit consumes one rate_limit_reset_credit for the given OpenAI account.
// The redeem_request_id is auto-generated (uuid-like) — upstream uses it for
// idempotency. Returns the consumed credit metadata so the UI can refresh.
func (s *OpenAIQuotaService) ResetCredit(ctx context.Context, accountID int64) (*OpenAIQuotaResetResult, error) {
	if s.accountRepo != nil {
		account, loadErr := s.accountRepo.GetByID(ctx, accountID)
		if loadErr != nil {
			return nil, infraerrors.Newf(http.StatusNotFound, "OPENAI_QUOTA_ACCOUNT_NOT_FOUND", "account not found: %v", loadErr)
		}
		if account.IsCredentialShadow() {
			return nil, ErrSparkShadowResetNotSupported
		}
	}
	accessToken, chatGPTAccountID, proxyURL, err := s.prepareUpstreamCall(ctx, accountID)
	if err != nil {
		return nil, err
	}

	redeemRequestID, err := generateRedeemRequestID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_QUOTA_REDEEM_ID_FAILED", "failed to generate redeem id: %v", err)
	}

	client, err := s.privacyClientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_CLIENT_ERROR", "failed to build upstream client: %v", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, openaiQuotaUpstreamTimeout)
	defer cancel()

	headers, expectedTaskID, isAgentIdentity, err := s.buildCodexQuotaHeaders(callCtx, accountID, accessToken, chatGPTAccountID)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to build upstream authentication: %v", err)
	}
	headers["content-type"] = "application/json"

	var payload OpenAIQuotaResetResult
	for attempt := 0; ; attempt++ {
		payload = OpenAIQuotaResetResult{}
		resp, requestErr := client.R().
			SetContext(callCtx).
			SetHeaders(headers).
			SetBody(map[string]string{"redeem_request_id": redeemRequestID}).
			SetSuccessResult(&payload).
			Post(chatGPTRateLimitResetURL)
		if requestErr != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_RESET_REQUEST_FAILED", "upstream request failed: %v", requestErr)
		}
		if resp.IsSuccessState() {
			break
		}
		if isAgentIdentity && attempt == 0 && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, resp.Bytes()) {
			if recoverErr := s.recoverAgentIdentityQuotaTask(callCtx, accountID, expectedTaskID); recoverErr != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "Agent Identity task recovery failed: %v", recoverErr)
			}
			headers, expectedTaskID, _, err = s.buildCodexQuotaHeaders(callCtx, accountID, "", chatGPTAccountID)
			if err != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_AUTH_FAILED", "failed to rebuild upstream authentication: %v", err)
			}
			headers["content-type"] = "application/json"
			continue
		}
		status := resp.StatusCode
		body := truncate(s.redactQuotaErrorBody(callCtx, accountID, resp.String()), 240)
		slog.Warn("openai_quota_reset_failed", "account_id", accountID, "status", status, "body", body)
		return nil, infraerrors.Newf(mapUpstreamStatus(status), "OPENAI_QUOTA_RESET_UPSTREAM_ERROR", "upstream returned %d: %s", status, body)
	}

	slog.Info("openai_quota_reset_success",
		"account_id", accountID,
		"code", payload.Code,
		"windows_reset", payload.WindowsReset,
	)
	return &payload, nil
}

// prepareUpstreamCall loads the account, validates it, obtains a fresh access
// token via the shared TokenProvider, and resolves the chatgpt-account-id and
// proxy URL. Centralized so QueryUsage / ResetCredit share validation.
func (s *OpenAIQuotaService) prepareUpstreamCall(ctx context.Context, accountID int64) (accessToken, chatGPTAccountID, proxyURL string, err error) {
	if s == nil || s.accountRepo == nil || s.privacyClientFactory == nil {
		return "", "", "", infraerrors.New(http.StatusInternalServerError, "OPENAI_QUOTA_NOT_CONFIGURED", "openai quota service is not configured")
	}

	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return "", "", "", infraerrors.Newf(http.StatusNotFound, "OPENAI_QUOTA_ACCOUNT_NOT_FOUND", "account not found: %v", err)
	}
	if account == nil {
		return "", "", "", infraerrors.New(http.StatusNotFound, "OPENAI_QUOTA_ACCOUNT_NOT_FOUND", "account not found")
	}
	if account.Platform != PlatformOpenAI {
		return "", "", "", infraerrors.New(http.StatusBadRequest, "OPENAI_QUOTA_INVALID_PLATFORM", "account is not an OpenAI account")
	}
	if account.Type != AccountTypeOAuth {
		return "", "", "", infraerrors.New(http.StatusBadRequest, "OPENAI_QUOTA_INVALID_TYPE", "account is not an OAuth account")
	}
	if account.IsCredentialShadow() {
		resolved, resolveErr := resolveCredentialAccount(ctx, s.accountRepo, account)
		if resolveErr != nil {
			return "", "", "", infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_SHADOW_RESOLVE_FAILED",
				"failed to resolve Spark shadow credentials: %v", resolveErr)
		}
		account = resolved
	}

	chatGPTAccountID = strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	if chatGPTAccountID == "" {
		// Fall back to organization_id — some legacy accounts only persisted poid.
		chatGPTAccountID = strings.TrimSpace(account.GetCredential("organization_id"))
	}
	if chatGPTAccountID == "" {
		return "", "", "", infraerrors.New(http.StatusBadRequest, "OPENAI_QUOTA_MISSING_ACCOUNT_ID", "chatgpt_account_id is missing; please re-authorize this account")
	}

	if !account.IsOpenAIAgentIdentity() {
		if s.tokenProvider == nil {
			return "", "", "", infraerrors.New(http.StatusInternalServerError, "OPENAI_QUOTA_NOT_CONFIGURED", "openai quota token provider is not configured")
		}
		accessToken, err = s.tokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return "", "", "", infraerrors.Newf(http.StatusBadGateway, "OPENAI_QUOTA_TOKEN_UNAVAILABLE", "failed to acquire access token: %v", err)
		}
		if strings.TrimSpace(accessToken) == "" {
			return "", "", "", infraerrors.New(http.StatusBadGateway, "OPENAI_QUOTA_TOKEN_UNAVAILABLE", "access token is empty")
		}
	}

	// account.Proxy is eager-loaded by accountRepo.GetByID (see
	// repository.accountsToService), so we can read the proxy URL directly
	// instead of round-tripping the DB again. Fall back to proxyRepo only
	// when Proxy isn't pre-populated (defensive — e.g. callers that built
	// the Account by hand).
	if account.ProxyID != nil {
		switch {
		case account.Proxy != nil:
			proxyURL = account.Proxy.URL()
		case s.proxyRepo != nil:
			if proxy, perr := s.proxyRepo.GetByID(ctx, *account.ProxyID); perr == nil && proxy != nil {
				proxyURL = proxy.URL()
			}
		}
	}

	return accessToken, chatGPTAccountID, proxyURL, nil
}

// buildCodexMainWindowExtraUpdates 映射 /wham/usage 响应的主限流窗口
// （RateLimit.PrimaryWindow / SecondaryWindow）。这是常规 Codex OAuth 账号的
// 用量信号，供超额（tail-burst）采集器消费。
func buildCodexMainWindowExtraUpdates(usage *OpenAIQuotaUsage, now time.Time) map[string]any {
	if usage == nil || usage.RateLimit == nil {
		return nil
	}
	return openAICodexRateLimitExtraUpdates(usage.RateLimit, now)
}

// openAICodexRateLimitExtraUpdates 将一组限流窗口（主/辅）转换为 codex_* 快照键。
func openAICodexRateLimitExtraUpdates(rateLimit *OpenAIRateLimit, now time.Time) map[string]any {
	if rateLimit == nil {
		return nil
	}
	snapshot := &OpenAICodexUsageSnapshot{}
	if window := rateLimit.PrimaryWindow; window != nil {
		used := window.UsedPercent
		reset := int(window.ResetAfterSeconds)
		minutes := int(window.LimitWindowSeconds / 60)
		snapshot.PrimaryUsedPercent = &used
		snapshot.PrimaryResetAfterSeconds = &reset
		snapshot.PrimaryWindowMinutes = &minutes
	}
	if window := rateLimit.SecondaryWindow; window != nil {
		used := window.UsedPercent
		reset := int(window.ResetAfterSeconds)
		minutes := int(window.LimitWindowSeconds / 60)
		snapshot.SecondaryUsedPercent = &used
		snapshot.SecondaryResetAfterSeconds = &reset
		snapshot.SecondaryWindowMinutes = &minutes
	}
	normalized := snapshot.Normalize()
	if normalized == nil {
		return nil
	}
	updates := map[string]any{}
	if normalized.Used5hPercent != nil {
		updates["codex_5h_used_percent"] = *normalized.Used5hPercent
		updates["codex_5h_usage_updated_at"] = now.UTC().Format(time.RFC3339)
	}
	if normalized.Reset5hSeconds != nil {
		updates["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
	}
	if normalized.Window5hMinutes != nil {
		updates["codex_5h_window_minutes"] = *normalized.Window5hMinutes
	}
	if normalized.Used7dPercent != nil {
		updates["codex_7d_used_percent"] = *normalized.Used7dPercent
		updates["codex_7d_usage_updated_at"] = now.UTC().Format(time.RFC3339)
	}
	if normalized.Reset7dSeconds != nil {
		updates["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
	}
	if normalized.Window7dMinutes != nil {
		updates["codex_7d_window_minutes"] = *normalized.Window7dMinutes
	}
	if resetAt := codexResetAtRFC3339(now, normalized.Reset5hSeconds); resetAt != nil {
		updates["codex_5h_reset_at"] = *resetAt
	}
	if resetAt := codexResetAtRFC3339(now, normalized.Reset7dSeconds); resetAt != nil {
		updates["codex_7d_reset_at"] = *resetAt
	}
	if len(updates) == 0 {
		return nil
	}
	updates["codex_usage_updated_at"] = now.UTC().Format(time.RFC3339)
	return updates
}

// buildCodexSparkWindowExtraUpdates 只读取 /wham/usage 中 metered_feature=codex_bengalfox
// 的独立窗口，并写入影子账号自己的 codex_* 字段。
func buildCodexSparkWindowExtraUpdates(usage *OpenAIQuotaUsage, now time.Time) map[string]any {
	if usage == nil {
		return nil
	}
	var spark *OpenAIRateLimit
	for i := range usage.AdditionalRateLimits {
		if usage.AdditionalRateLimits[i].MeteredFeature == "codex_bengalfox" {
			spark = usage.AdditionalRateLimits[i].RateLimit
			break
		}
	}
	if spark == nil {
		return nil
	}
	snapshot := &OpenAICodexUsageSnapshot{}
	if window := spark.PrimaryWindow; window != nil {
		used := window.UsedPercent
		reset := int(window.ResetAfterSeconds)
		minutes := int(window.LimitWindowSeconds / 60)
		snapshot.PrimaryUsedPercent = &used
		snapshot.PrimaryResetAfterSeconds = &reset
		snapshot.PrimaryWindowMinutes = &minutes
	}
	if window := spark.SecondaryWindow; window != nil {
		used := window.UsedPercent
		reset := int(window.ResetAfterSeconds)
		minutes := int(window.LimitWindowSeconds / 60)
		snapshot.SecondaryUsedPercent = &used
		snapshot.SecondaryResetAfterSeconds = &reset
		snapshot.SecondaryWindowMinutes = &minutes
	}
	normalized := snapshot.Normalize()
	if normalized == nil {
		return nil
	}
	updates := map[string]any{}
	if normalized.Used5hPercent != nil {
		updates["codex_5h_used_percent"] = *normalized.Used5hPercent
		updates["codex_5h_usage_updated_at"] = now.UTC().Format(time.RFC3339)
	}
	if normalized.Reset5hSeconds != nil {
		updates["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
	}
	if normalized.Window5hMinutes != nil {
		updates["codex_5h_window_minutes"] = *normalized.Window5hMinutes
	}
	if normalized.Used7dPercent != nil {
		updates["codex_7d_used_percent"] = *normalized.Used7dPercent
		updates["codex_7d_usage_updated_at"] = now.UTC().Format(time.RFC3339)
	}
	if normalized.Reset7dSeconds != nil {
		updates["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
	}
	if normalized.Window7dMinutes != nil {
		updates["codex_7d_window_minutes"] = *normalized.Window7dMinutes
	}
	if resetAt := codexResetAtRFC3339(now, normalized.Reset5hSeconds); resetAt != nil {
		updates["codex_5h_reset_at"] = *resetAt
	}
	if resetAt := codexResetAtRFC3339(now, normalized.Reset7dSeconds); resetAt != nil {
		updates["codex_7d_reset_at"] = *resetAt
	}
	if len(updates) == 0 {
		return nil
	}
	updates["codex_usage_updated_at"] = now.UTC().Format(time.RFC3339)
	return updates
}

func (s *OpenAIQuotaService) buildCodexQuotaHeaders(ctx context.Context, accountID int64, accessToken, chatGPTAccountID string) (map[string]string, string, bool, error) {
	headers := buildCodexCommonHeaders(accessToken, chatGPTAccountID)
	if s == nil || s.accountRepo == nil {
		return headers, "", false, nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		if strings.TrimSpace(accessToken) == "" {
			return nil, "", false, errors.New("Agent Identity account credentials are unavailable")
		}
		return headers, "", false, nil
	}
	if account.IsCredentialShadow() {
		account, err = resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, "", false, err
		}
	}
	if !account.IsOpenAIAgentIdentity() {
		return headers, "", false, nil
	}
	authHeaders, err := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, account)
	if err != nil {
		return nil, "", true, err
	}
	for key, values := range authHeaders {
		if len(values) > 0 {
			// headers is a plain map, so differently-cased names are distinct.
			// Remove the placeholder Bearer header (and any duplicate identity
			// header) before adding the signed value; req/v3 otherwise applies
			// the map in nondeterministic order and could send the empty Bearer.
			for existingKey := range headers {
				if strings.EqualFold(existingKey, key) {
					delete(headers, existingKey)
				}
			}
			headers[key] = values[len(values)-1]
		}
	}
	return headers, strings.TrimSpace(account.GetCredential("task_id")), true, nil
}

func (s *OpenAIQuotaService) recoverAgentIdentityQuotaTask(ctx context.Context, accountID int64, expectedTaskID string) error {
	if s == nil || s.accountRepo == nil {
		return errors.New("account repository is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || account == nil {
		return errors.New("Agent Identity account is unavailable")
	}
	if account.IsCredentialShadow() {
		account, err = resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return err
		}
	}
	if !account.IsOpenAIAgentIdentity() {
		return nil
	}
	return ensureAgentIdentityTaskForAccount(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, account, expectedTaskID)
}

// buildCodexCommonHeaders sets the request headers expected by the chatgpt.com
// backend so calls succeed past Cloudflare/WASM checks.
func buildCodexCommonHeaders(accessToken, chatGPTAccountID string) map[string]string {
	identity := resolveCodexOutboundIdentity("")
	return map[string]string{
		"authorization":      "Bearer " + accessToken,
		"chatgpt-account-id": chatGPTAccountID,
		"oai-language":       openaiQuotaCodexLanguageTag,
		"originator":         identity.originator,
		"user-agent":         identity.userAgent,
		"version":            identity.version,
		"accept":             "application/json",
		"sec-fetch-site":     openaiQuotaSecFetchSite,
		"sec-fetch-mode":     openaiQuotaSecFetchMode,
		"sec-fetch-dest":     openaiQuotaSecFetchDest,
		"priority":           "u=4, i",
	}
}

// generateRedeemRequestID produces a UUID-v4-shaped string without pulling in a
// new dependency. ChatGPT uses this as an idempotency key for the consume call.
func generateRedeemRequestID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Set version (4) and variant (RFC 4122) bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:]), nil
}

// mapUpstreamStatus collapses upstream HTTP statuses into a stable set we
// surface from the admin handler. 4xx upstream errors are surfaced as 502
// (BadGateway) so callers can distinguish "your input is bad" (400) from
// "upstream said no" (502); 401/403 are bubbled directly to hint at re-auth.
func mapUpstreamStatus(status int) int {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return status
	case status == http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case status >= 400 && status < 500:
		return http.StatusBadGateway
	case status >= 500:
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}

// [v0.2.5 新增] 限流重置积分明细
type OpenAIRateLimitResetCreditDetail struct {
	ExpiresAt string `json:"expires_at,omitempty"`
}


// [tokenav 定制] 以下为接口 openAIAutoResetQuota 的轻量适配实现，
// 复用我们已有的 QueryUsage/CacheResetCreditsSnapshot/ResetCredit。
func (s *OpenAIQuotaService) CachePostResetSnapshot(ctx context.Context, accountID int64, usage *OpenAIQuotaUsage) error {
	if usage == nil {
		return s.CacheResetCreditsSnapshot(ctx, accountID, nil)
	}
	return s.CacheResetCreditsSnapshot(ctx, accountID, usage.RateLimitResetCredits)
}

func (s *OpenAIQuotaService) ResetCreditTargeted(ctx context.Context, accountID int64, creditID, redeemRequestID string) (*OpenAIQuotaResetResult, error) {
	// [tokenav 定制] 我们的 ResetCredit 未区分 credit_id/redeem_request_id，退化为整体重置。
	return s.ResetCredit(ctx, accountID)
}
