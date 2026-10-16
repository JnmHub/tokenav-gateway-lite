package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	grokTokenCacheSkew          = 5 * time.Minute
	grokRequestRefreshTimeout   = 8 * time.Second
	grokRefreshLockWaitTimeout  = 2 * time.Second
	grokRefreshLockPollInterval = 25 * time.Millisecond
)

type GrokTokenCache = GeminiTokenCache

type GrokTokenProvider struct {
	accountRepo      AccountRepository
	tokenCache       GrokTokenCache
	refreshAPI       *OAuthRefreshAPI
	executor         OAuthRefreshExecutor
	refreshPolicy    ProviderRefreshPolicy
	tempUnschedCache TempUnschedCache
}

func NewGrokTokenProvider(
	accountRepo AccountRepository,
	tokenCache GrokTokenCache,
) *GrokTokenProvider {
	return &GrokTokenProvider{
		accountRepo:   accountRepo,
		tokenCache:    tokenCache,
		refreshPolicy: GrokProviderRefreshPolicy(),
	}
}

func (p *GrokTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

func (p *GrokTokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

func (p *GrokTokenProvider) SetTempUnschedCache(cache TempUnschedCache) {
	p.tempUnschedCache = cache
}

func (p *GrokTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return "", errors.New("not a grok oauth account")
	}
	requestPath := isOAuthRefreshRequestPath(ctx)
	selectedProxyID := cloneGrokProxyID(account.ProxyID)
	if requestPath {
		if eligibilityErr := grokOAuthRequestAccountEligibilityError(account); eligibilityErr != nil {
			return "", withGrokCredentialFailureSnapshot(eligibilityErr, account)
		}
	}

	expiresAt := account.GetCredentialAsTime("expires_at")
	accountAccessToken := strings.TrimSpace(account.GetGrokAccessToken())
	if accountAccessToken == "" {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenMissing, account)
	}
	if strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		// 已签发的 access token 在有效期内仍可使用；缺 refresh token 只在
		// token 已过期/无到期证据时变成永久凭据故障。
		if expiresAt == nil || !time.Now().Before(*expiresAt) {
			return "", withGrokCredentialFailureSnapshot(errGrokOAuthRefreshTokenMissing, account)
		}
		return accountAccessToken, nil
	}

	cacheKey := GrokTokenCacheKey(account)
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil {
			cachedToken := strings.TrimSpace(token)
			if cachedToken != "" && cachedToken == accountAccessToken && expiresAt != nil && time.Until(*expiresAt) > grokTokenRefreshSkew {
				return cachedToken, nil
			}
		}
	}

	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= grokTokenRefreshSkew
	if needsRefresh {
		if p.refreshAPI == nil || p.executor == nil {
			// 兼容已有测试与手工构造服务：当前 access token 尚未过期时可继续
			// 使用；只有已经失效且无法刷新时才视为 provider 配置故障。
			if expiresAt == nil || !time.Now().Before(*expiresAt) {
				return "", errGrokOAuthRefreshNotConfigured
			}
			needsRefresh = false
		} else {
			refreshCtx, cancel := context.WithTimeout(ctx, grokRequestRefreshTimeout)
			defer cancel()
			refreshCallCtx := refreshCtx
			if requestPath {
				refreshCallCtx = withOAuthRefreshRequestPath(refreshCtx)
			}
			result, err := p.refreshAPI.RefreshIfNeeded(refreshCallCtx, account, p.executor, grokTokenRefreshSkew)
			if err != nil {
				if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
					return "", err
				}
			} else if result != nil && result.LockHeld {
				if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache {
					return p.waitForRefreshedToken(refreshCtx, account, cacheKey)
				}
				if expiresAt == nil || !time.Now().Before(*expiresAt) {
					return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenExpired, account)
				}
			} else if result != nil && result.Account != nil {
				if eligibilityErr := grokOAuthRequestAccountEligibilityError(result.Account); eligibilityErr != nil {
					return "", withGrokCredentialFailureSnapshot(eligibilityErr, result.Account)
				}
				if !grokCredentialProxyIDsEqual(result.Account.ProxyID, selectedProxyID) {
					return "", withGrokCredentialFailureSnapshot(errOAuthRefreshAccountStateChanged, result.Account)
				}
				account = result.Account
				expiresAt = account.GetCredentialAsTime("expires_at")
			}
		}
	}

	accessToken := account.GetGrokAccessToken()
	if strings.TrimSpace(accessToken) == "" {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenMissing, account)
	}
	if expiresAt == nil || !time.Now().Before(*expiresAt) {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenExpired, account)
	}

	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			if eligibilityErr := grokOAuthRequestAccountEligibilityError(latestAccount); eligibilityErr != nil {
				return "", withGrokCredentialFailureSnapshot(eligibilityErr, latestAccount)
			}
			if !grokCredentialProxyIDsEqual(latestAccount.ProxyID, selectedProxyID) {
				return "", withGrokCredentialFailureSnapshot(errOAuthRefreshAccountStateChanged, latestAccount)
			}
			accessToken = latestAccount.GetGrokAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenMissing, latestAccount)
			}
			latestExpiry := latestAccount.GetCredentialAsTime("expires_at")
			if latestExpiry == nil || !time.Now().Before(*latestExpiry) {
				return "", withGrokCredentialFailureSnapshot(errGrokOAuthAccessTokenExpired, latestAccount)
			}
		} else {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > grokTokenCacheSkew:
					ttl = until - grokTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			_ = p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

// GetAccessTokenForManualTest bypasses only production scheduling eligibility
// so an administrator can diagnose disabled, rate-limited or cooling accounts.
// Credential/proxy integrity and the shared refresh lock remain enforced.
func (p *GrokTokenProvider) GetAccessTokenForManualTest(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return "", errors.New("not a grok oauth account")
	}
	if account.ProxyID != nil && account.Proxy == nil {
		return "", errGrokOAuthConfiguredProxyMiss
	}
	accessToken := strings.TrimSpace(account.GetGrokAccessToken())
	expiresAt := account.GetCredentialAsTime("expires_at")
	tokenValid := accessToken != "" && expiresAt != nil && time.Now().Before(*expiresAt)
	if tokenValid && time.Until(*expiresAt) > grokTokenRefreshSkew {
		return accessToken, nil
	}
	if strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		// 与真实请求凭据语义一致：仍有效的 access token 可以被管理员探测；
		// 只有确实需要刷新时才要求 refresh token。
		if tokenValid {
			return accessToken, nil
		}
		return "", errGrokOAuthRefreshTokenMissing
	}
	if p.refreshAPI == nil || p.executor == nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", errGrokOAuthRefreshNotConfigured
	}

	refreshCtx, cancel := context.WithTimeout(ctx, grokRequestRefreshTimeout)
	defer cancel()
	result, err := p.refreshAPI.RefreshIfNeeded(refreshCtx, account, p.executor, grokTokenRefreshSkew)
	if err != nil {
		if tokenValid {
			return accessToken, nil
		}
		return "", err
	}
	if result != nil && result.LockHeld {
		if tokenValid {
			return accessToken, nil
		}
		return "", errors.New("token refresh is already in progress; retry shortly")
	}
	if result != nil && result.Account != nil {
		account = result.Account
	}
	accessToken = strings.TrimSpace(account.GetGrokAccessToken())
	if accessToken == "" {
		return "", errGrokOAuthAccessTokenMissing
	}
	if latestExpiry := account.GetCredentialAsTime("expires_at"); latestExpiry != nil && !time.Now().Before(*latestExpiry) {
		return "", errGrokOAuthAccessTokenExpired
	}
	return accessToken, nil
}

func (p *GrokTokenProvider) waitForRefreshedToken(ctx context.Context, account *Account, cacheKey string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, grokRefreshLockWaitTimeout)
	defer cancel()

	initialToken := strings.TrimSpace(account.GetGrokAccessToken())
	initialVersion := account.GetCredentialAsInt64("_token_version")
	selectedProxyID := cloneGrokProxyID(account.ProxyID)
	sawAuthoritativeState := false
	var lastReadErr error
	ticker := time.NewTicker(grokRefreshLockPollInterval)
	defer ticker.Stop()

	for {
		if p.accountRepo != nil {
			latest, err := p.accountRepo.GetByID(waitCtx, account.ID)
			if err != nil {
				lastReadErr = err
			} else if latest == nil {
				return "", errOAuthRefreshAccountStateChanged
			} else {
				sawAuthoritativeState = true
				if eligibilityErr := grokOAuthRequestAccountEligibilityError(latest); eligibilityErr != nil {
					return "", withGrokCredentialFailureSnapshot(eligibilityErr, latest)
				}
				if !grokCredentialProxyIDsEqual(latest.ProxyID, selectedProxyID) {
					return "", withGrokCredentialFailureSnapshot(errOAuthRefreshAccountStateChanged, latest)
				}
				token := strings.TrimSpace(latest.GetGrokAccessToken())
				version := latest.GetCredentialAsInt64("_token_version")
				expiresAt := latest.GetCredentialAsTime("expires_at")
				changed := token != initialToken || (version > 0 && version > initialVersion)
				if token != "" && changed && expiresAt != nil && time.Now().Before(*expiresAt) {
					p.cacheForcedToken(waitCtx, latest, token)
					return token, nil
				}
			}
		}

		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if !sawAuthoritativeState {
				if lastReadErr == nil {
					lastReadErr = waitCtx.Err()
				}
				return "", fmt.Errorf("%w: %v", errOAuthRefreshAccountRereadFailed, lastReadErr)
			}
			return "", errOAuthRefreshAccountStateChanged
		case <-ticker.C:
		}
	}
}

func grokOAuthRequestAccountEligibilityError(account *Account) error {
	if account == nil || !account.IsGrokOAuth() || !grokOAuthAccountRequestSchedulable(account) {
		return errOAuthRefreshAccountStateChanged
	}
	if account.ProxyID != nil && account.Proxy == nil {
		return errGrokOAuthConfiguredProxyMiss
	}
	return nil
}

func grokOAuthAccountRequestSchedulable(account *Account) bool {
	if account == nil {
		return false
	}
	// 兼容单元测试和少量手工构造的零值账号；生产从 DB/调度快照加载的
	// 账号总有明确 status，仍严格执行完整 IsSchedulable 判断。
	if strings.TrimSpace(account.Status) == "" {
		return true
	}
	return account.IsSchedulable()
}

func cloneGrokProxyID(proxyID *int64) *int64 {
	if proxyID == nil {
		return nil
	}
	value := *proxyID
	return &value
}

// RefreshAfterUnauthorized forces a refresh after xAI rejects a token with
// 401. The normal expiry check is intentionally bypassed because the provider's
// expiry timestamp may still look valid while the upstream token is revoked.
func (p *GrokTokenProvider) RefreshAfterUnauthorized(ctx context.Context, account *Account) (string, error) {
	if p == nil || account == nil {
		return "", errors.New("grok token provider or account is nil")
	}
	if account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
		return "", errors.New("grok unauthorized refresh requires an oauth account")
	}
	if p.refreshAPI == nil || p.executor == nil {
		return "", errGrokOAuthRefreshNotConfigured
	}
	if strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		return "", withGrokCredentialFailureSnapshot(errGrokOAuthRefreshTokenMissing, account)
	}

	cacheKey := GrokTokenCacheKey(account)
	if p.tokenCache != nil {
		_ = p.tokenCache.DeleteAccessToken(ctx, cacheKey)
	}

	refreshCtx, cancel := context.WithTimeout(ctx, grokRequestRefreshTimeout)
	defer cancel()
	refreshCallCtx := refreshCtx
	if isOAuthRefreshRequestPath(ctx) {
		refreshCallCtx = withOAuthRefreshRequestPath(refreshCtx)
	}
	result, err := p.refreshAPI.RefreshNow(refreshCallCtx, account, p.executor)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", errors.New("grok oauth refresh returned no result")
	}
	if result.LockHeld {
		return p.waitForRefreshedToken(refreshCtx, account, cacheKey)
	}

	accessToken := ""
	if result.NewCredentials != nil {
		if token, ok := result.NewCredentials["access_token"].(string); ok {
			accessToken = strings.TrimSpace(token)
		}
	}
	if accessToken == "" && result.Account != nil {
		accessToken = strings.TrimSpace(result.Account.GetGrokAccessToken())
	}
	if accessToken == "" {
		return "", errors.New("grok oauth refresh returned an empty access_token")
	}
	cacheAccount := result.Account
	if cacheAccount == nil {
		cacheAccount = account
	}
	p.cacheForcedToken(refreshCtx, cacheAccount, accessToken)
	return accessToken, nil
}

func (p *GrokTokenProvider) InvalidateToken(ctx context.Context, account *Account) error {
	if p == nil || p.tokenCache == nil || account == nil {
		return nil
	}
	return p.tokenCache.DeleteAccessToken(ctx, GrokTokenCacheKey(account))
}

func (p *GrokTokenProvider) cacheForcedToken(ctx context.Context, account *Account, accessToken string) {
	if p == nil || p.tokenCache == nil || strings.TrimSpace(accessToken) == "" {
		return
	}
	ttl := 30 * time.Minute
	if account != nil {
		if expiresAt := account.GetCredentialAsTime("expires_at"); expiresAt != nil {
			if until := time.Until(*expiresAt); until > 0 {
				ttl = until
			}
		}
	}
	_ = p.tokenCache.SetAccessToken(ctx, GrokTokenCacheKey(account), accessToken, ttl)
}

func GrokTokenCacheKey(account *Account) string {
	if account == nil {
		return "grok:account:0"
	}
	return "grok:account:" + strconv.FormatInt(account.ID, 10)
}
