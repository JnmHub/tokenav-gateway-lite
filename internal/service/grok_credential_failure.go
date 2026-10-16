package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

const (
	grokCredentialFailoverDeadlineKey = "grok_credential_failover_deadline"
	grokCredentialFailoverBudget      = 15 * time.Second
	grokCredentialMutationTimeout     = 5 * time.Second
	grokCredentialMutationConfirmWait = 250 * time.Millisecond
	grokCredentialCacheCleanupTimeout = 500 * time.Millisecond

	GrokCredentialUnavailableClientMessage = "No healthy Grok OAuth account is currently available"

	GrokCredentialReasonRevoked          GatewayFailureReason = "grok_oauth_credential_revoked"
	GrokCredentialReasonMissing          GatewayFailureReason = "grok_oauth_credentials_missing"
	GrokCredentialReasonEntitlement      GatewayFailureReason = "grok_oauth_entitlement_action_required"
	GrokCredentialReasonProxyInvalid     GatewayFailureReason = "grok_oauth_proxy_invalid"
	GrokCredentialReasonRefreshTransient GatewayFailureReason = "grok_oauth_refresh_transient"
	GrokCredentialReasonProviderConfig   GatewayFailureReason = "grok_oauth_provider_config"
	GrokCredentialReasonProviderDown     GatewayFailureReason = "grok_oauth_provider_unavailable"
	GrokCredentialReasonAccountChanged   GatewayFailureReason = "grok_oauth_account_state_changed"
	GrokCredentialReasonStateUpdate      GatewayFailureReason = "grok_oauth_account_state_update_failed"
	GrokCredentialReasonFailoverTimeout  GatewayFailureReason = "grok_oauth_failover_timeout"
)

var (
	errGrokCredentialStateUpdateFailed = errors.New("grok oauth account state update failed")
	errGrokOAuthRefreshNotConfigured   = errors.New("grok oauth refresh is not configured")
	errGrokOAuthRefreshTokenMissing    = errors.New("grok oauth refresh token is missing")
	errGrokOAuthAccessTokenMissing     = errors.New("grok oauth access token is missing")
	errGrokOAuthAccessTokenExpired     = errors.New("grok oauth access token is expired")
	errGrokOAuthConfiguredProxyMiss    = errors.New("grok oauth configured proxy is missing")
)

type grokCredentialFailureClass struct {
	scope     GatewayFailureScope
	reason    GatewayFailureReason
	action    NextAccountAction
	permanent bool
	transient bool
	message   string
	snapshot  *GrokCredentialMutationSnapshot
}

// GrokCredentialMutationSnapshot 是请求选中账号时看到的状态身份。
// Tokenav 的敏感凭据在 PostgreSQL 中随机 nonce 加密，不能重新加密后直接比较，
// 因此 CAS 使用 updated_at + proxy_id；token/version 字段仅用于识别并发刷新恢复。
type GrokCredentialMutationSnapshot struct {
	AccessToken  string
	RefreshToken string
	TokenVersion int64
	UpdatedAt    time.Time
	ProxyID      *int64
}

type grokCredentialFailureSnapshotError struct {
	cause    error
	snapshot GrokCredentialMutationSnapshot
}

func (e *grokCredentialFailureSnapshotError) Error() string { return e.cause.Error() }
func (e *grokCredentialFailureSnapshotError) Unwrap() error { return e.cause }

func withGrokCredentialFailureSnapshot(err error, account *Account) error {
	if err == nil || account == nil || !account.IsGrokOAuth() {
		return err
	}
	var existing *grokCredentialFailureSnapshotError
	if errors.As(err, &existing) {
		return err
	}
	return &grokCredentialFailureSnapshotError{cause: err, snapshot: grokCredentialMutationSnapshot(account)}
}

func grokCredentialFailureSnapshot(err error) (GrokCredentialMutationSnapshot, bool) {
	var snapshotErr *grokCredentialFailureSnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr == nil {
		return GrokCredentialMutationSnapshot{}, false
	}
	return snapshotErr.snapshot, true
}

type grokCredentialConditionalStateRepository interface {
	SetGrokCredentialErrorIfMatch(context.Context, int64, GrokCredentialMutationSnapshot, string) (bool, error)
	SetGrokCredentialTempUnschedulableIfMatch(context.Context, int64, GrokCredentialMutationSnapshot, time.Time, string) (bool, error)
}

// GetRequestCredential 仅在真实请求路径应用 Grok OAuth 凭据恢复契约。
// API Key 和其他 provider（服务商）保持原 GetAccessToken 行为。
func (s *OpenAIGatewayService) GetRequestCredential(ctx context.Context, c *gin.Context, account *Account) (string, string, error) {
	return s.getRequestCredential(ctx, c, account)
}

func (s *OpenAIGatewayService) getRequestCredential(ctx context.Context, c *gin.Context, account *Account) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if account == nil {
		return "", "", errors.New("account is nil")
	}
	if !account.IsGrokOAuth() {
		return s.GetAccessToken(ctx, account)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if s == nil || s.grokTokenProvider == nil {
		return "", "", s.newGrokCredentialFailover(c, account, grokCredentialFailureClass{
			scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonProviderConfig,
			action: NextAccountStop, message: "Grok OAuth credential provider is unavailable",
		})
	}
	if s.isOpenAIAccountRuntimeBlocked(account) {
		return "", "", s.newGrokCredentialFailover(c, account, grokCredentialFailureClass{
			scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonAccountChanged,
			action: NextAccountRetry, message: "Grok OAuth account is not currently schedulable",
		})
	}

	credentialCtx, cancel, budgetExpired := grokCredentialAcquisitionContext(ctx, c)
	if cancel != nil {
		defer cancel()
	}
	if budgetExpired {
		return "", "", s.grokCredentialBudgetError(c, account)
	}

	token, kind, err := s.GetAccessToken(withOAuthRefreshRequestPath(credentialCtx), account)
	if err == nil {
		if s.isOpenAIAccountRuntimeBlocked(account) {
			return "", "", s.newGrokCredentialFailover(c, account, grokCredentialFailureClass{
				scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonAccountChanged,
				action: NextAccountRetry, message: "Grok OAuth account is not currently schedulable",
			})
		}
		return token, kind, nil
	}
	if parentErr := ctx.Err(); parentErr != nil {
		return "", "", parentErr
	}
	if credentialCtx.Err() != nil {
		return "", "", s.grokCredentialBudgetError(c, account)
	}
	return s.resolveGrokCredentialFailure(credentialCtx, c, account, err)
}

func grokCredentialAcquisitionContext(ctx context.Context, c *gin.Context) (context.Context, context.CancelFunc, bool) {
	if c == nil {
		return ctx, nil, false
	}
	deadline := time.Time{}
	if raw, ok := c.Get(grokCredentialFailoverDeadlineKey); ok {
		deadline, _ = raw.(time.Time)
	}
	if deadline.IsZero() {
		deadline = time.Now().Add(grokCredentialFailoverBudget)
		c.Set(grokCredentialFailoverDeadlineKey, deadline)
	}
	if !time.Now().Before(deadline) {
		return ctx, nil, true
	}
	acquireCtx, cancel := context.WithDeadline(ctx, deadline)
	return acquireCtx, cancel, false
}

func (s *OpenAIGatewayService) grokCredentialBudgetError(c *gin.Context, account *Account) error {
	return s.newGrokCredentialFailover(c, account, grokCredentialFailureClass{
		scope: GatewayFailureScopeRequest, reason: GrokCredentialReasonFailoverTimeout,
		action: NextAccountStop, message: "Grok OAuth credential failover budget exhausted",
	})
}

// refreshGrokAfterUnauthorizedForRequest 把 401 后的强制刷新纳入同一 15 秒预算
// 和失败分类；这样 invalid_grant 不会只被短暂冷却后继续反复入池。
func (s *OpenAIGatewayService) refreshGrokAfterUnauthorizedForRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
) (string, error) {
	credentialCtx, cancel, budgetExpired := grokCredentialAcquisitionContext(ctx, c)
	if cancel != nil {
		defer cancel()
	}
	if budgetExpired {
		return "", s.grokCredentialBudgetError(c, account)
	}
	token, err := s.refreshGrokAfterUnauthorized(credentialCtx, account)
	if err == nil {
		return token, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if credentialCtx.Err() != nil {
		return "", s.grokCredentialBudgetError(c, account)
	}
	_, _, mappedErr := s.resolveGrokCredentialFailure(credentialCtx, c, account, err)
	return "", mappedErr
}

func (s *OpenAIGatewayService) resolveGrokCredentialFailure(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	err error,
) (string, string, error) {
	class := classifyGrokCredentialFailure(account, err)
	if snapshot, ok := grokCredentialFailureSnapshot(err); ok {
		class.snapshot = &snapshot
	}
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if class.permanent || class.transient {
		freshToken, mutationErr := s.applyGrokCredentialAccountFailure(ctx, account, class)
		if freshToken != "" {
			return freshToken, "oauth", nil
		}
		if mutationErr != nil {
			switch {
			case errors.Is(mutationErr, errOAuthRefreshAccountStateChanged):
				class = grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonAccountChanged, action: NextAccountRetry, message: "Grok OAuth account eligibility changed"}
			case errors.Is(mutationErr, errOAuthRefreshAccountRereadFailed):
				class = grokCredentialFailureClass{scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonProviderDown, action: NextAccountStop, message: "Grok OAuth account state is temporarily unavailable"}
			default:
				class = grokCredentialFailureClass{scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonStateUpdate, action: NextAccountStop, message: "Grok OAuth account state could not be updated safely"}
			}
		}
	}
	return "", "", s.newGrokCredentialFailover(c, account, class)
}

func classifyGrokCredentialFailure(account *Account, err error) grokCredentialFailureClass {
	stableReason := strings.ToLower(strings.TrimSpace(infraerrors.Reason(err)))
	message := ""
	if err != nil {
		message = strings.ToLower(err.Error())
	}
	contains := func(values ...string) bool {
		for _, value := range values {
			if strings.Contains(stableReason, value) || strings.Contains(message, value) {
				return true
			}
		}
		return false
	}
	var providerConfigErr *providerConfigurationRefreshError
	var containmentErr *providerCycleContainmentRefreshError

	switch {
	case errors.Is(err, errGrokOAuthRefreshTokenMissing), errors.Is(err, errGrokOAuthAccessTokenMissing), errors.Is(err, errGrokOAuthAccessTokenExpired):
		return grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonMissing, action: NextAccountRetry, permanent: true, message: "Grok OAuth credentials are missing or expired"}
	case contains("invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_reused", "refresh_token_invalidated", "app_session_terminated"):
		return grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonRevoked, action: NextAccountRetry, permanent: true, message: "Grok OAuth credentials require account action"}
	case contains("grok_oauth_entitlement_denied", "entitlement_denied", "access_denied", "subscription required", "no active grok subscription"):
		return grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonEntitlement, action: NextAccountRetry, permanent: true, message: "Grok OAuth entitlement requires account action"}
	case errors.Is(err, errGrokOAuthConfiguredProxyMiss), contains("grok_oauth_proxy_not_found"):
		return grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonProxyInvalid, action: NextAccountRetry, permanent: true, message: "Grok OAuth account proxy configuration is invalid"}
	case errors.Is(err, errOAuthRefreshAccountRereadFailed), errors.Is(err, errOAuthRefreshCredentialPersist), errors.As(err, &containmentErr):
		return grokCredentialFailureClass{scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonProviderDown, action: NextAccountStop, message: "Grok OAuth provider state is temporarily unavailable"}
	case errors.Is(err, errOAuthRefreshAccountStateChanged):
		return grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonAccountChanged, action: NextAccountRetry, message: "Grok OAuth account eligibility changed"}
	case errors.As(err, &providerConfigErr), errors.Is(err, errGrokOAuthRefreshNotConfigured), contains("invalid_client", "unauthorized_client", "invalid_scope", "unknown scope", "grok oauth service is not configured", "grok_oauth_proxy_not_available"):
		return grokCredentialFailureClass{scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonProviderConfig, action: NextAccountStop, message: "Grok OAuth provider configuration is unavailable"}
	case contains("grok_oauth_proxy_lookup_failed", "grok_oauth_request_failed") && (account == nil || account.ProxyID == nil):
		return grokCredentialFailureClass{scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonProviderDown, action: NextAccountStop, message: "Grok OAuth provider is temporarily unavailable"}
	case contains("status 429", "status 500", "status 502", "status 503", "status 504") && (account == nil || account.ProxyID == nil):
		return grokCredentialFailureClass{scope: GatewayFailureScopeProvider, reason: GrokCredentialReasonProviderDown, action: NextAccountStop, message: "Grok OAuth provider is temporarily unavailable"}
	default:
		return grokCredentialFailureClass{scope: GatewayFailureScopeAccount, reason: GrokCredentialReasonRefreshTransient, action: NextAccountRetry, transient: true, message: "Grok OAuth credential refresh is temporarily unavailable"}
	}
}

func (s *OpenAIGatewayService) applyGrokCredentialAccountFailure(ctx context.Context, account *Account, class grokCredentialFailureClass) (string, error) {
	if s == nil || account == nil || ctx == nil || ctx.Err() != nil {
		if ctx != nil {
			return "", ctx.Err()
		}
		return "", context.Canceled
	}
	mutationMu := s.grokCredentialMutationLock(account.ID)
	if err := mutationMu.Lock(ctx); err != nil {
		return "", err
	}
	defer mutationMu.Unlock()

	stateRepo, ok := s.accountRepo.(grokCredentialConditionalStateRepository)
	snapshot := grokCredentialMutationSnapshot(account)
	if class.snapshot != nil {
		snapshot = *class.snapshot
	}
	if token, err := s.validateCurrentGrokCredentialFailure(ctx, account.ID, snapshot, class); err != nil || token != "" {
		return token, err
	}
	if token, recovered := s.grokCredentialConcurrentlyRefreshedToken(ctx, account.ID, snapshot); recovered {
		return token, nil
	}
	if s.accountRepo == nil || !ok {
		s.blockGrokCredentialRuntime(account, time.Time{}, string(class.reason))
		return "", fmt.Errorf("%w: conditional account repository is not configured", errGrokCredentialStateUpdateFailed)
	}

	until := time.Time{}
	if class.transient {
		until = time.Now().Add(tokenRefreshTempUnschedDuration)
	}
	rollbackRuntime := s.blockGrokCredentialRuntime(account, until, string(class.reason))
	keepRuntimeBlock := false
	defer func() {
		if !keepRuntimeBlock {
			rollbackRuntime()
		}
	}()

	stateCtx, cancel := context.WithTimeout(ctx, grokCredentialMutationTimeout)
	var updated bool
	var err error
	if class.permanent {
		updated, err = stateRepo.SetGrokCredentialErrorIfMatch(stateCtx, account.ID, snapshot, string(class.reason))
	} else {
		updated, err = stateRepo.SetGrokCredentialTempUnschedulableIfMatch(stateCtx, account.ID, snapshot, until, string(class.reason))
	}
	requestErr := ctx.Err()
	cancel()
	if err != nil {
		slog.Warn("grok_credential_failure.state_update_failed", "account_id", account.ID, "reason", class.reason, "error", err)
		if (requestErr != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && s.grokCredentialMutationCommitted(account.ID, class, until) {
			updated = true
		} else if requestErr != nil {
			return "", requestErr
		} else {
			keepRuntimeBlock = true
			return "", fmt.Errorf("%w: %v", errGrokCredentialStateUpdateFailed, err)
		}
	}
	if !updated {
		rollbackRuntime()
		keepRuntimeBlock = true // 已手工回滚，阻止 defer 重复操作。
		return s.resolveGrokCredentialCASMiss(ctx, account.ID, snapshot)
	}
	keepRuntimeBlock = true

	if class.permanent && s.grokTokenProvider != nil {
		invalidateCtx, invalidateCancel := context.WithTimeout(context.WithoutCancel(ctx), grokCredentialCacheCleanupTimeout)
		err = s.grokTokenProvider.InvalidateToken(invalidateCtx, account)
		invalidateCancel()
		if err != nil {
			return "", fmt.Errorf("%w: invalidate cached credential: %v", errGrokCredentialStateUpdateFailed, err)
		}
	}
	return "", nil
}

func (s *OpenAIGatewayService) validateCurrentGrokCredentialFailure(
	ctx context.Context,
	accountID int64,
	snapshot GrokCredentialMutationSnapshot,
	class grokCredentialFailureClass,
) (string, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return "", errOAuthRefreshAccountRereadFailed
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	latest, err := s.accountRepo.GetByID(checkCtx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return "", errOAuthRefreshAccountStateChanged
		}
		return "", fmt.Errorf("%w: %v", errOAuthRefreshAccountRereadFailed, err)
	}
	if latest == nil || !latest.IsGrokOAuth() || !grokOAuthAccountRequestSchedulable(latest) || s.isOpenAIAccountRuntimeBlocked(latest) {
		return "", errOAuthRefreshAccountStateChanged
	}
	latestSnapshot := grokCredentialMutationSnapshot(latest)
	if !latestSnapshot.UpdatedAt.Equal(snapshot.UpdatedAt) || !grokCredentialProxyIDsEqual(latestSnapshot.ProxyID, snapshot.ProxyID) {
		if token, ok := s.grokCredentialConcurrentlyRefreshedToken(ctx, accountID, snapshot); ok {
			return token, nil
		}
		return "", errOAuthRefreshAccountStateChanged
	}
	if class.reason == GrokCredentialReasonProxyInvalid {
		if latest.ProxyID == nil || latest.Proxy != nil {
			return "", errOAuthRefreshAccountStateChanged
		}
	} else if latest.ProxyID != nil && latest.Proxy == nil {
		return "", errOAuthRefreshAccountStateChanged
	}
	if class.reason == GrokCredentialReasonMissing {
		expiresAt := latest.GetCredentialAsTime("expires_at")
		stillMissing := strings.TrimSpace(latest.GetGrokAccessToken()) == "" || strings.TrimSpace(latest.GetGrokRefreshToken()) == "" || expiresAt == nil || !time.Now().Before(*expiresAt)
		if !stillMissing {
			return "", errOAuthRefreshAccountStateChanged
		}
	}
	return "", nil
}

func (s *OpenAIGatewayService) grokCredentialMutationLock(accountID int64) *oauthRefreshLocalLock {
	actual, _ := s.grokCredentialMutationLocks.LoadOrStore(accountID, newOAuthRefreshLocalLock())
	mu, ok := actual.(*oauthRefreshLocalLock)
	if !ok {
		mu = newOAuthRefreshLocalLock()
		s.grokCredentialMutationLocks.Store(accountID, mu)
	}
	return mu
}

func (s *OpenAIGatewayService) grokCredentialMutationCommitted(accountID int64, class grokCredentialFailureClass, until time.Time) bool {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return false
	}
	confirmCtx, cancel := context.WithTimeout(context.Background(), grokCredentialMutationConfirmWait)
	defer cancel()
	latest, err := s.accountRepo.GetByID(confirmCtx, accountID)
	if err != nil || latest == nil {
		return false
	}
	if class.permanent {
		return latest.Status == StatusError && !latest.Schedulable && latest.ErrorMessage == string(class.reason)
	}
	return class.transient && latest.TempUnschedulableUntil != nil && !latest.TempUnschedulableUntil.Before(until) && latest.TempUnschedulableReason == string(class.reason)
}

func grokCredentialMutationSnapshot(account *Account) GrokCredentialMutationSnapshot {
	if account == nil {
		return GrokCredentialMutationSnapshot{}
	}
	snapshot := GrokCredentialMutationSnapshot{
		AccessToken:  strings.TrimSpace(account.GetGrokAccessToken()),
		RefreshToken: strings.TrimSpace(account.GetGrokRefreshToken()),
		TokenVersion: account.GetCredentialAsInt64("_token_version"),
		UpdatedAt:    account.UpdatedAt,
	}
	if account.ProxyID != nil {
		proxyID := *account.ProxyID
		snapshot.ProxyID = &proxyID
	}
	return snapshot
}

func (s *OpenAIGatewayService) resolveGrokCredentialCASMiss(ctx context.Context, accountID int64, snapshot GrokCredentialMutationSnapshot) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if token, ok := s.grokCredentialConcurrentlyRefreshedToken(ctx, accountID, snapshot); ok {
		return token, nil
	}
	return "", errOAuthRefreshAccountStateChanged
}

func (s *OpenAIGatewayService) blockGrokCredentialRuntime(account *Account, until time.Time, reason string) func() {
	if s == nil || account == nil {
		return func() {}
	}
	mu := s.openAIAccountRuntimeBlockLock(account.ID)
	mu.Lock()
	before, hadBefore := s.openaiAccountRuntimeBlockUntil.Load(account.ID)
	installedGeneration, changed := s.blockAccountSchedulingLocked(account, until, reason)
	installed, installedOK := s.openaiAccountRuntimeBlockUntil.Load(account.ID)
	installedUntil, isTime := installed.(time.Time)
	mu.Unlock()
	if !changed || !installedOK || !isTime {
		return func() {}
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		generation, ok := s.openaiAccountRuntimeBlockGeneration.Load(account.ID)
		if !ok || generation != installedGeneration {
			return
		}
		current, ok := s.openaiAccountRuntimeBlockUntil.Load(account.ID)
		currentUntil, valid := current.(time.Time)
		if !ok || !valid || !currentUntil.Equal(installedUntil) {
			return
		}
		if hadBefore {
			s.openaiAccountRuntimeBlockUntil.Store(account.ID, before)
		} else {
			s.openaiAccountRuntimeBlockUntil.Delete(account.ID)
		}
		s.openaiAccountRuntimeBlockGeneration.Store(account.ID, s.openaiAccountRuntimeBlockSequence.Add(1))
	}
}

func (s *OpenAIGatewayService) grokCredentialConcurrentlyRefreshedToken(ctx context.Context, accountID int64, baseline GrokCredentialMutationSnapshot) (string, bool) {
	if s == nil || s.accountRepo == nil || accountID <= 0 || ctx == nil || ctx.Err() != nil {
		return "", false
	}
	checkCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	latest, err := s.accountRepo.GetByID(checkCtx, accountID)
	if err != nil || latest == nil || !latest.IsGrokOAuth() || !grokOAuthAccountRequestSchedulable(latest) || (latest.ProxyID != nil && latest.Proxy == nil) || s.isOpenAIAccountRuntimeBlocked(latest) {
		return "", false
	}
	latestSnapshot := grokCredentialMutationSnapshot(latest)
	if !grokCredentialProxyIDsEqual(latestSnapshot.ProxyID, baseline.ProxyID) {
		return "", false
	}
	changed := !latestSnapshot.UpdatedAt.Equal(baseline.UpdatedAt) && (latestSnapshot.AccessToken != baseline.AccessToken || latestSnapshot.RefreshToken != baseline.RefreshToken || latestSnapshot.TokenVersion > baseline.TokenVersion)
	if !changed {
		return "", false
	}
	expiresAt := latest.GetCredentialAsTime("expires_at")
	if latestSnapshot.AccessToken == "" || latestSnapshot.RefreshToken == "" || expiresAt == nil || !time.Now().Before(*expiresAt) {
		return "", false
	}
	return latestSnapshot.AccessToken, true
}

func grokCredentialProxyIDsEqual(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *OpenAIGatewayService) newGrokCredentialFailover(c *gin.Context, account *Account, class grokCredentialFailureClass) error {
	if strings.TrimSpace(class.message) == "" {
		class.message = "Grok OAuth credentials are unavailable"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform: PlatformGrok, AccountID: account.ID,
		Stage: string(GatewayFailureStageAccountAuth), Scope: string(class.scope), Reason: string(class.reason),
		Kind: "credential_failover", Message: class.message,
	})
	return &UpstreamFailoverError{
		Stage: GatewayFailureStageAccountAuth, Scope: class.scope, Reason: class.reason,
		NextAccountAction: class.action, ClientStatusCode: http.StatusServiceUnavailable,
		ClientMessage: GrokCredentialUnavailableClientMessage,
	}
}
