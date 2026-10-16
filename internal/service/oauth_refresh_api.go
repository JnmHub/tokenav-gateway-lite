package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OAuthRefreshExecutor 各平台实现的 OAuth 刷新执行器
// TokenRefresher 接口的超集：增加了 CacheKey 方法用于分布式锁
type OAuthRefreshExecutor interface {
	TokenRefresher

	// CacheKey 返回用于分布式锁的缓存键（与 TokenProvider 使用的一致）
	CacheKey(account *Account) string
}

// GrokOAuthRefreshSuccessRepository 是 Grok 令牌轮换的 CAS（比较并交换）持久化边界。
// 实现必须同时比较刷新开始时的凭据版本和代理，避免覆盖管理员或并发 worker 的新凭据。
type GrokOAuthRefreshSuccessRepository interface {
	UpdateGrokOAuthCredentialsIfUnchanged(
		ctx context.Context,
		id int64,
		expectedTokenVersion int64,
		expectedProxyID *int64,
		credentials map[string]any,
	) (bool, error)
}

const (
	defaultRefreshLockTTL                   = 60 * time.Second
	defaultRefreshLockReleaseTimeout        = 2 * time.Second
	defaultRefreshPostPersistCleanupTimeout = 2 * time.Second
)

var (
	errOAuthRefreshAccountRereadFailed = errors.New("oauth refresh account reread failed")
	errOAuthRefreshAccountStateChanged = errors.New("oauth refresh account state changed")
	errOAuthRefreshCredentialPersist   = errors.New("oauth refresh credential persistence failed")
)

type oauthRefreshRequestPathKey struct{}

func withOAuthRefreshRequestPath(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauthRefreshRequestPathKey{}, true)
}

func isOAuthRefreshRequestPath(ctx context.Context) bool {
	requestPath, _ := ctx.Value(oauthRefreshRequestPathKey{}).(bool)
	return requestPath
}

// contextMutex 让等待同账号本地刷新锁也受请求 context 和 15 秒预算约束。
type contextMutex struct {
	token chan struct{}
}

type oauthRefreshLocalLock = contextMutex

func newContextMutex() *contextMutex {
	return &contextMutex{token: make(chan struct{}, 1)}
}

func newOAuthRefreshLocalLock() *oauthRefreshLocalLock { return newContextMutex() }

func (m *contextMutex) Lock(ctx context.Context) error {
	select {
	case m.token <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *contextMutex) Unlock() { <-m.token }

type providerConfigurationRefreshError struct{ err error }

func (e *providerConfigurationRefreshError) Error() string {
	return "OAuth refresh provider configuration is unavailable"
}
func (e *providerConfigurationRefreshError) Unwrap() error { return e.err }

type providerCycleContainmentRefreshError struct{ err error }

func (e *providerCycleContainmentRefreshError) Error() string {
	return "OAuth refresh provider state is unavailable"
}
func (e *providerCycleContainmentRefreshError) Unwrap() error { return e.err }

// OAuthRefreshResult 统一刷新结果
type OAuthRefreshResult struct {
	Refreshed      bool           // 实际执行了刷新
	NewCredentials map[string]any // 刷新后的 credentials（nil 表示未刷新）
	Account        *Account       // 从 DB 重新读取的最新 account
	LockHeld       bool           // 锁被其他 worker 持有（未执行刷新）
}

func snapshotOAuthRefreshAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	snapshot := *account
	snapshot.Credentials = cloneCredentials(account.Credentials)
	if account.ProxyID != nil {
		proxyID := *account.ProxyID
		snapshot.ProxyID = &proxyID
	}
	return &snapshot
}

// nextOAuthTokenVersion 生成严格递增的凭据版本。毫秒时间戳便于排障；同一毫秒内
// 连续更新时至少加一，避免管理员提交新凭据后仍与刷新 worker 的旧快照版本相同。
func nextOAuthTokenVersion(current int64) int64 {
	next := time.Now().UnixMilli()
	if next <= current {
		return current + 1
	}
	return next
}

// OAuthRefreshAPI 统一的 OAuth Token 刷新入口
// 封装分布式锁、进程内互斥锁、DB 重读、已刷新检查、竞争恢复等通用逻辑
type OAuthRefreshAPI struct {
	accountRepo AccountRepository
	tokenCache  GeminiTokenCache // 可选，nil = 无分布式锁
	lockTTL     time.Duration
	localLocks  sync.Map // key: cacheKey string -> value: *contextMutex
}

// NewOAuthRefreshAPI 创建统一刷新 API
// 可选传入 lockTTL 覆盖默认的 60s 分布式锁 TTL
func NewOAuthRefreshAPI(accountRepo AccountRepository, tokenCache GeminiTokenCache, lockTTL ...time.Duration) *OAuthRefreshAPI {
	ttl := defaultRefreshLockTTL
	if len(lockTTL) > 0 && lockTTL[0] > 0 {
		ttl = lockTTL[0]
	}
	return &OAuthRefreshAPI{
		accountRepo: accountRepo,
		tokenCache:  tokenCache,
		lockTTL:     ttl,
	}
}

// getLocalLock 返回指定 cacheKey 的进程内互斥锁
func (api *OAuthRefreshAPI) getLocalLock(cacheKey string) *contextMutex {
	actual, _ := api.localLocks.LoadOrStore(cacheKey, newContextMutex())
	mu, ok := actual.(*contextMutex)
	if !ok {
		mu = newContextMutex()
		api.localLocks.Store(cacheKey, mu)
	}
	return mu
}

// RefreshIfNeeded 在分布式锁保护下按需刷新 OAuth token
//
// 流程:
//  1. 获取分布式锁
//  2. 从 DB 重读最新 account（防止使用过时的 refresh_token）
//  3. 二次检查是否仍需刷新
//  4. 调用 executor.Refresh() 执行平台特定刷新逻辑
//  5. 设置 _token_version + 更新 DB
//  6. 释放锁
func (api *OAuthRefreshAPI) RefreshIfNeeded(
	ctx context.Context,
	account *Account,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
) (*OAuthRefreshResult, error) {
	return api.refresh(ctx, account, executor, refreshWindow, false)
}

// RefreshNow forces one refresh under the same process-local and distributed
// locks used by background refresh. It is reserved for an upstream 401 where
// the stored expiry can no longer be trusted.
func (api *OAuthRefreshAPI) RefreshNow(
	ctx context.Context,
	account *Account,
	executor OAuthRefreshExecutor,
) (*OAuthRefreshResult, error) {
	return api.refresh(ctx, account, executor, 0, true)
}

func (api *OAuthRefreshAPI) refresh(
	ctx context.Context,
	account *Account,
	executor OAuthRefreshExecutor,
	refreshWindow time.Duration,
	force bool,
) (*OAuthRefreshResult, error) {
	if api == nil || api.accountRepo == nil {
		return nil, errors.New("oauth refresh account repository is not configured")
	}
	if account == nil {
		return nil, errors.New("oauth refresh account is nil")
	}
	if executor == nil {
		return nil, errors.New("oauth refresh executor is nil")
	}
	requestPath := isOAuthRefreshRequestPath(ctx)
	cacheKey := executor.CacheKey(account)

	// 0. 获取进程内互斥锁（防止同一进程内的并发刷新竞争）
	localMu := api.getLocalLock(cacheKey)
	if err := localMu.Lock(ctx); err != nil {
		return nil, fmt.Errorf("oauth refresh local lock: %w", err)
	}
	defer localMu.Unlock()

	// 1. 获取分布式锁
	if api.tokenCache != nil {
		acquired, lockErr := api.tokenCache.AcquireRefreshLock(ctx, cacheKey, api.lockTTL)
		if lockErr != nil {
			// Redis 错误，降级为无锁刷新（进程内互斥锁仍生效）
			slog.Warn("oauth_refresh_lock_failed_degraded",
				"account_id", account.ID,
				"cache_key", cacheKey,
				"error", lockErr,
			)
		} else if !acquired {
			// 锁被其他 worker 持有
			return &OAuthRefreshResult{LockHeld: true}, nil
		} else {
			defer api.releaseRefreshLock(ctx, cacheKey)
		}
	}

	// 2. 从 DB 重读最新 account（锁保护下，确保使用最新的 refresh_token）
	freshAccount, err := api.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		if requestPath {
			return nil, fmt.Errorf("%w: %v", errOAuthRefreshAccountRereadFailed, err)
		}
		slog.Warn("oauth_refresh_db_reread_failed", "account_id", account.ID, "error", err)
		freshAccount = account // 后台刷新保留既有降级行为。
	} else if freshAccount == nil {
		if requestPath {
			return nil, fmt.Errorf("%w: account not found", errOAuthRefreshAccountStateChanged)
		}
		freshAccount = account
	}
	if freshAccount.ID != account.ID {
		return nil, fmt.Errorf("%w: account identity mismatch", errOAuthRefreshAccountRereadFailed)
	}
	if requestPath && freshAccount.IsGrokOAuth() {
		if eligibilityErr := grokOAuthRequestAccountEligibilityError(freshAccount); eligibilityErr != nil {
			return nil, withGrokCredentialFailureSnapshot(eligibilityErr, freshAccount)
		}
		if !executor.CanRefresh(freshAccount) {
			return nil, withGrokCredentialFailureSnapshot(errGrokOAuthRefreshTokenMissing, freshAccount)
		}
	}

	// 3. 二次检查是否仍需刷新（另一条路径可能已刷新）
	if !force && !executor.NeedsRefresh(freshAccount, refreshWindow) {
		return &OAuthRefreshResult{
			Account: freshAccount,
		}, nil
	}

	// 4. 执行平台特定刷新逻辑
	attemptedAccount := snapshotOAuthRefreshAccount(freshAccount)
	newCredentials, refreshErr := executor.Refresh(ctx, freshAccount)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if refreshErr != nil {
		// 竞争恢复：invalid_grant 可能是另一个 worker 已消费了旧 refresh_token
		// 重新读取 DB，如果 refresh_token 已更新则说明是竞争，返回成功
		if isInvalidGrantError(refreshErr) {
			if recoveredAccount, recovered := api.tryRecoverFromRefreshRace(ctx, freshAccount); recovered {
				if requestPath && recoveredAccount.IsGrokOAuth() {
					if eligibilityErr := grokOAuthRequestAccountEligibilityError(recoveredAccount); eligibilityErr != nil {
						return nil, withGrokCredentialFailureSnapshot(eligibilityErr, recoveredAccount)
					}
				}
				slog.Info("oauth_refresh_race_recovered",
					"account_id", freshAccount.ID,
					"platform", freshAccount.Platform,
				)
				return &OAuthRefreshResult{
					Account: recoveredAccount,
				}, nil
			}
		}
		result := &OAuthRefreshResult{Account: attemptedAccount}
		if requestPath && attemptedAccount.IsGrokOAuth() {
			return result, withGrokCredentialFailureSnapshot(refreshErr, attemptedAccount)
		}
		return nil, refreshErr
	}

	// 5. 设置版本号 + 更新 DB
	if newCredentials != nil {
		newCredentials["_token_version"] = nextOAuthTokenVersion(attemptedAccount.GetCredentialAsInt64("_token_version"))
		if freshAccount.IsGrokOAuth() {
			conditionalRepo, ok := api.accountRepo.(GrokOAuthRefreshSuccessRepository)
			if !ok {
				if requestPath {
					return nil, &providerConfigurationRefreshError{err: errors.New("grok OAuth refresh success CAS repository is not configured")}
				}
				// 测试替身和旧后台刷新实现保留兼容降级；真实请求路径必须具备 CAS。
				if updateErr := persistAccountCredentials(ctx, api.accountRepo, freshAccount, newCredentials); updateErr != nil {
					return nil, fmt.Errorf("%w: DB update failed: %v", errOAuthRefreshCredentialPersist, updateErr)
				}
			} else {
				applied, updateErr := conditionalRepo.UpdateGrokOAuthCredentialsIfUnchanged(
					ctx,
					freshAccount.ID,
					attemptedAccount.GetCredentialAsInt64("_token_version"),
					attemptedAccount.ProxyID,
					newCredentials,
				)
				if updateErr != nil {
					slog.Error("oauth_refresh_update_failed", "account_id", freshAccount.ID, "platform", freshAccount.Platform, "error", updateErr)
					return nil, &providerCycleContainmentRefreshError{err: fmt.Errorf("OAuth refresh succeeded but credential persistence failed: %w", updateErr)}
				}
				if !applied {
					currentAccount, readErr := api.accountRepo.GetByID(ctx, freshAccount.ID)
					if readErr != nil || currentAccount == nil {
						if readErr == nil {
							readErr = errors.New("account not found after Grok OAuth success CAS miss")
						}
						return nil, &providerCycleContainmentRefreshError{err: readErr}
					}
					return &OAuthRefreshResult{Account: currentAccount}, nil
				}
				durableAccount, readErr := api.loadGrokDurableAccountAfterPersist(ctx, cacheKey, freshAccount.ID)
				if readErr != nil || durableAccount == nil {
					if readErr == nil {
						readErr = errors.New("account not found after Grok OAuth success CAS")
					}
					return nil, &providerCycleContainmentRefreshError{err: readErr}
				}
				freshAccount = durableAccount
			}
		} else if updateErr := persistAccountCredentials(ctx, api.accountRepo, freshAccount, newCredentials); updateErr != nil {
			slog.Error("oauth_refresh_update_failed",
				"account_id", freshAccount.ID,
				"error", updateErr,
			)
			return nil, fmt.Errorf("%w: DB update failed: %v", errOAuthRefreshCredentialPersist, updateErr)
		}
	}

	if requestPath && freshAccount.IsGrokOAuth() {
		if eligibilityErr := grokOAuthRequestAccountEligibilityError(freshAccount); eligibilityErr != nil {
			return nil, withGrokCredentialFailureSnapshot(eligibilityErr, freshAccount)
		}
	}

	return &OAuthRefreshResult{
		Refreshed:      true,
		NewCredentials: newCredentials,
		Account:        freshAccount,
	}, nil
}

func (api *OAuthRefreshAPI) releaseRefreshLock(parent context.Context, cacheKey string) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshLockReleaseTimeout)
	defer cancel()
	if err := api.tokenCache.ReleaseRefreshLock(ctx, cacheKey); err != nil {
		slog.Warn("oauth_refresh_lock_release_failed", "cache_key", cacheKey, "error", err)
	}
}

func (api *OAuthRefreshAPI) loadGrokDurableAccountAfterPersist(parent context.Context, cacheKey string, accountID int64) (*Account, error) {
	cleanupParent := context.Background()
	if parent != nil {
		cleanupParent = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(cleanupParent, defaultRefreshPostPersistCleanupTimeout)
	defer cancel()
	if api.tokenCache != nil {
		if err := api.tokenCache.DeleteAccessToken(ctx, cacheKey); err != nil {
			slog.Warn("oauth_refresh_post_persist_cache_delete_failed", "account_id", accountID, "cache_key", cacheKey, "error", err)
		}
	}
	return api.accountRepo.GetByID(ctx, accountID)
}

// isInvalidGrantError 检查错误是否为 invalid_grant
func isInvalidGrantError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_grant")
}

// tryRecoverFromRefreshRace 在 invalid_grant 错误后尝试竞争恢复
// 重新读取 DB，如果 refresh_token 已改变（说明另一个 worker 成功刷新），则返回更新后的 account
func (api *OAuthRefreshAPI) tryRecoverFromRefreshRace(ctx context.Context, usedAccount *Account) (*Account, bool) {
	if api.accountRepo == nil {
		return nil, false
	}
	reReadAccount, err := api.accountRepo.GetByID(ctx, usedAccount.ID)
	if err != nil || reReadAccount == nil {
		return nil, false
	}
	usedRT := usedAccount.GetCredential("refresh_token")
	currentRT := reReadAccount.GetCredential("refresh_token")
	if usedRT == "" || currentRT == "" {
		return nil, false
	}
	// refresh_token 不同 → 另一个 worker 已成功刷新
	if usedRT != currentRT {
		return reReadAccount, true
	}
	return nil, false
}

// MergeCredentials 将旧 credentials 中不存在于新 map 的字段保留到新 map 中
func MergeCredentials(oldCreds, newCreds map[string]any) map[string]any {
	if newCreds == nil {
		newCreds = make(map[string]any)
	}
	for k, v := range oldCreds {
		if _, exists := newCreds[k]; !exists {
			newCreds[k] = v
		}
	}
	return newCreds
}

// BuildClaudeAccountCredentials 为 Claude 平台构建 OAuth credentials map
// 消除 Claude 平台没有 BuildAccountCredentials 方法的问题
func BuildClaudeAccountCredentials(tokenInfo *TokenInfo) map[string]any {
	creds := map[string]any{
		"access_token": tokenInfo.AccessToken,
		"token_type":   tokenInfo.TokenType,
		"expires_in":   strconv.FormatInt(tokenInfo.ExpiresIn, 10),
		"expires_at":   strconv.FormatInt(tokenInfo.ExpiresAt, 10),
	}
	if tokenInfo.RefreshToken != "" {
		creds["refresh_token"] = tokenInfo.RefreshToken
	}
	if tokenInfo.Scope != "" {
		creds["scope"] = tokenInfo.Scope
	}
	return creds
}
