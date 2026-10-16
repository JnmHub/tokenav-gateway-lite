//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type grokCredentialCASRepo struct {
	*mockAccountRepoForPlatform
	errorMutations   int
	tempMutations    int
	successMutations int
	forceCASMiss     bool
}

func (r *grokCredentialCASRepo) SetGrokCredentialErrorIfMatch(
	_ context.Context,
	id int64,
	snapshot GrokCredentialMutationSnapshot,
	reason string,
) (bool, error) {
	r.errorMutations++
	account := r.accountsByID[id]
	if r.forceCASMiss || account == nil || !account.UpdatedAt.Equal(snapshot.UpdatedAt) || !grokCredentialProxyIDsEqual(account.ProxyID, snapshot.ProxyID) {
		return false, nil
	}
	account.Status = StatusError
	account.Schedulable = false
	account.ErrorMessage = reason
	account.UpdatedAt = account.UpdatedAt.Add(time.Millisecond)
	return true, nil
}

func (r *grokCredentialCASRepo) SetGrokCredentialTempUnschedulableIfMatch(
	_ context.Context,
	id int64,
	snapshot GrokCredentialMutationSnapshot,
	until time.Time,
	reason string,
) (bool, error) {
	r.tempMutations++
	account := r.accountsByID[id]
	if r.forceCASMiss || account == nil || !account.UpdatedAt.Equal(snapshot.UpdatedAt) || !grokCredentialProxyIDsEqual(account.ProxyID, snapshot.ProxyID) {
		return false, nil
	}
	account.TempUnschedulableUntil = &until
	account.TempUnschedulableReason = reason
	account.UpdatedAt = account.UpdatedAt.Add(time.Millisecond)
	return true, nil
}

func (r *grokCredentialCASRepo) UpdateGrokOAuthCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	expectedTokenVersion int64,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	r.successMutations++
	account := r.accountsByID[id]
	if r.forceCASMiss || account == nil || account.GetCredentialAsInt64("_token_version") != expectedTokenVersion || !grokCredentialProxyIDsEqual(account.ProxyID, expectedProxyID) {
		return false, nil
	}
	account.Credentials = cloneCredentials(credentials)
	account.UpdatedAt = account.UpdatedAt.Add(time.Millisecond)
	return true, nil
}

func newGrokCredentialTestAccount(id int64, updatedAt time.Time) *Account {
	return &Account{
		ID: id, Name: "grok-oauth", Platform: PlatformGrok, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, UpdatedAt: updatedAt,
		Credentials: map[string]any{
			"access_token":  "access-old",
			"refresh_token": "refresh-old",
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
}

func newGrokCredentialTestService(account *Account, repo *grokCredentialCASRepo, cache GrokTokenCache) *OpenAIGatewayService {
	provider := NewGrokTokenProvider(repo, cache)
	return &OpenAIGatewayService{accountRepo: repo, grokTokenProvider: provider}
}

func TestGetRequestCredentialPermanentlyQuarantinesMissingExpiredCredentialWithCAS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	updatedAt := time.Now().Add(-time.Minute).UTC()
	account := newGrokCredentialTestAccount(701, updatedAt)
	delete(account.Credentials, "refresh_token")
	account.Credentials["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	repo := &grokCredentialCASRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
	cache := &grokTokenCacheForProviderTest{}
	svc := newGrokCredentialTestService(account, repo, cache)
	c, _ := gin.CreateTestContext(nil)

	_, _, err := svc.getRequestCredential(context.Background(), c, account)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.IsCredentialFailure())
	require.Equal(t, GatewayFailureScopeAccount, failoverErr.Scope)
	require.Equal(t, GrokCredentialReasonMissing, failoverErr.Reason)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Equal(t, 1, repo.errorMutations)
	require.Equal(t, StatusError, account.Status)
	require.False(t, account.Schedulable)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events := rawEvents.([]*OpsUpstreamErrorEvent)
	require.Equal(t, string(GrokCredentialReasonMissing), events[len(events)-1].Reason)
}

func TestGetRequestCredentialTransientRefreshFailureUsesCASCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	updatedAt := time.Now().Add(-time.Minute).UTC()
	account := newGrokCredentialTestAccount(702, updatedAt)
	account.Credentials["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	repo := &grokCredentialCASRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
	cache := &grokTokenCacheForProviderTest{lockResult: true}
	svc := newGrokCredentialTestService(account, repo, cache)
	svc.grokTokenProvider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), &tokenRefresherStub{err: errors.New("temporary refresh failure")})
	c, _ := gin.CreateTestContext(nil)

	_, _, err := svc.getRequestCredential(context.Background(), c, account)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, GrokCredentialReasonRefreshTransient, failoverErr.Reason)
	require.Equal(t, 1, repo.tempMutations)
	require.NotNil(t, account.TempUnschedulableUntil)
	require.Equal(t, string(GrokCredentialReasonRefreshTransient), account.TempUnschedulableReason)
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGetRequestCredentialRecoversConcurrentRefreshWithoutQuarantine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	baseline := time.Now().Add(-2 * time.Minute).UTC()
	stale := newGrokCredentialTestAccount(703, baseline)
	delete(stale.Credentials, "refresh_token")
	stale.Credentials["expires_at"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	latest := newGrokCredentialTestAccount(703, baseline.Add(time.Minute))
	latest.Credentials["access_token"] = "access-new"
	latest.Credentials["refresh_token"] = "refresh-new"
	latest.Credentials["_token_version"] = time.Now().UnixMilli()
	repo := &grokCredentialCASRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{latest.ID: latest}}}
	svc := newGrokCredentialTestService(latest, repo, nil)
	c, _ := gin.CreateTestContext(nil)

	token, kind, err := svc.getRequestCredential(context.Background(), c, stale)
	require.NoError(t, err)
	require.Equal(t, "access-new", token)
	require.Equal(t, "oauth", kind)
	require.Zero(t, repo.errorMutations)
	require.Zero(t, repo.tempMutations)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(latest))
}

func TestGetRequestCredentialBudgetAndProviderFailuresStopWithoutAccountMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGrokCredentialTestAccount(704, time.Now().Add(-time.Minute).UTC())
	repo := &grokCredentialCASRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
	svc := newGrokCredentialTestService(account, repo, nil)

	t.Run("budget exhausted", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		c.Set(grokCredentialFailoverDeadlineKey, time.Now().Add(-time.Second))
		_, _, err := svc.getRequestCredential(context.Background(), c, account)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, GrokCredentialReasonFailoverTimeout, failoverErr.Reason)
		require.False(t, failoverErr.ShouldRetryNextAccount())
	})

	t.Run("provider missing", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		missingProviderSvc := &OpenAIGatewayService{accountRepo: repo}
		_, _, err := missingProviderSvc.getRequestCredential(context.Background(), c, account)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, GatewayFailureScopeProvider, failoverErr.Scope)
		require.Equal(t, GrokCredentialReasonProviderConfig, failoverErr.Reason)
		require.False(t, failoverErr.ShouldRetryNextAccount())
	})

	require.Zero(t, repo.errorMutations)
	require.Zero(t, repo.tempMutations)
}

func TestGetRequestCredentialAPIKeyBypassesOAuthRecovery(t *testing.T) {
	account := &Account{ID: 705, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "xai-key"}}
	svc := &OpenAIGatewayService{}
	token, kind, err := svc.getRequestCredential(context.Background(), nil, account)
	require.NoError(t, err)
	require.Equal(t, "xai-key", token)
	require.Equal(t, "apikey", kind)
}
