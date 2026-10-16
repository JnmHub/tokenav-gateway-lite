package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type grokReconcileTestRepo struct {
	AccountRepository

	mu               sync.Mutex
	accounts         []Account
	requests         []OAuthRefreshPageOptions
	casIDs           []int64
	forceCASConflict bool
	refreshError     error
}

func (r *grokReconcileTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, ErrAccountNotFound
}

func (r *grokReconcileTestRepo) ListOAuthRefreshCandidatePage(_ context.Context, options OAuthRefreshPageOptions) (*OAuthRefreshCandidatePage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, options)
	accounts := append([]Account(nil), r.accounts...)
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	eligible := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account.ID <= options.AfterID || account.Platform != PlatformGrok || account.Type != AccountTypeOAuth {
			continue
		}
		if options.ActiveOnly && account.Status != StatusActive {
			continue
		}
		eligible = append(eligible, account)
	}
	pageLength := min(options.Limit, len(eligible))
	page := &OAuthRefreshCandidatePage{Accounts: append([]Account(nil), eligible[:pageLength]...), HasMore: len(eligible) > pageLength}
	if page.HasMore && pageLength > 0 {
		page.NextAfterID = page.Accounts[pageLength-1].ID
	}
	return page, nil
}

func (r *grokReconcileTestRepo) FindGrokOAuthIdentityOwners(_ context.Context, identityKeys []string) (map[string]GrokOAuthIdentityOwner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wanted := make(map[string]struct{}, len(identityKeys))
	for _, key := range identityKeys {
		wanted[strings.TrimSpace(key)] = struct{}{}
	}
	result := make(map[string]GrokOAuthIdentityOwner)
	for i := range r.accounts {
		account := &r.accounts[i]
		if !account.IsGrokOAuth() || account.Status != StatusActive {
			continue
		}
		for _, key := range GrokOAuthStableIdentityKeys(account.GetCredential("email"), account.GetCredential("id_token")) {
			if _, ok := wanted[key]; !ok {
				continue
			}
			owner := result[key]
			if owner.CanonicalAccountID == 0 || account.ID < owner.CanonicalAccountID {
				owner.CanonicalAccountID = account.ID
			}
			owner.Count++
			result[key] = owner
		}
	}
	return result, nil
}

func (r *grokReconcileTestRepo) SetGrokOAuthReconcileErrorIfUnchanged(
	_ context.Context,
	id int64,
	expected GrokOAuthReconcileSnapshot,
	message string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forceCASConflict {
		r.forceCASConflict = false
		return false, nil
	}
	for i := range r.accounts {
		account := &r.accounts[i]
		if account.ID != id {
			continue
		}
		if account.Status != StatusActive || !account.Schedulable || !account.UpdatedAt.Equal(expected.UpdatedAt) ||
			account.GetCredentialAsInt64("_token_version") != expected.TokenVersion || !sameInt64Pointer(account.ProxyID, expected.ProxyID) {
			return false, nil
		}
		account.Status = StatusError
		account.Schedulable = false
		account.ErrorMessage = message
		account.UpdatedAt = account.UpdatedAt.Add(time.Second)
		r.casIDs = append(r.casIDs, id)
		return true, nil
	}
	return false, nil
}

func sameInt64Pointer(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (r *grokReconcileTestRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].Credentials = MergeCredentials(r.accounts[i].Credentials, credentials)
			r.accounts[i].UpdatedAt = r.accounts[i].UpdatedAt.Add(time.Second)
			return nil
		}
	}
	return ErrAccountNotFound
}

func (r *grokReconcileTestRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}

func (r *grokReconcileTestRepo) SetError(_ context.Context, id int64, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].Status = StatusError
			r.accounts[i].Schedulable = false
			r.accounts[i].ErrorMessage = message
			return nil
		}
	}
	return ErrAccountNotFound
}

type grokReconcileTestRefresher struct {
	err       error
	onRefresh func()
}

func (r *grokReconcileTestRefresher) CanRefresh(account *Account) bool {
	return account != nil && account.IsGrokOAuth()
}

func (r *grokReconcileTestRefresher) NeedsRefresh(*Account, time.Duration) bool { return true }

func (r *grokReconcileTestRefresher) CacheKey(account *Account) string {
	return fmt.Sprintf("grok-reconcile:%d", account.ID)
}

func (r *grokReconcileTestRefresher) Refresh(_ context.Context, account *Account) (map[string]any, error) {
	if r.onRefresh != nil {
		r.onRefresh()
	}
	if r.err != nil {
		return nil, r.err
	}
	return map[string]any{
		"access_token":  "rotated-access",
		"refresh_token": "rotated-refresh",
		"expires_at":    time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339),
		"email":         account.GetCredential("email"),
	}, nil
}

type grokReconcileTestInvalidator struct {
	mu  sync.Mutex
	ids []int64
}

func (i *grokReconcileTestInvalidator) InvalidateToken(_ context.Context, account *Account) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ids = append(i.ids, account.ID)
	return nil
}

func newGrokReconcileTestService(repo *grokReconcileTestRepo, refresher *grokReconcileTestRefresher) *TokenRefreshService {
	return &TokenRefreshService{
		accountRepo:      repo,
		candidatePager:   repo,
		cacheInvalidator: &grokReconcileTestInvalidator{},
		registrations: []tokenRefreshRegistration{{
			platform:  PlatformGrok,
			refresher: refresher,
			executor:  refresher,
		}},
		refreshPolicy: DefaultBackgroundRefreshPolicy(),
		cfg: &config.TokenRefreshConfig{
			MaxRetries:               1,
			ProviderConcurrency:      1,
			ProviderQPS:              100,
			ProviderFailureThreshold: 3,
			AttemptTimeoutSeconds:    1,
		},
	}
}

func grokReconcileApplyInputFromPreview(
	t *testing.T,
	svc *TokenRefreshService,
	input GrokOAuthReconcileInput,
) GrokOAuthReconcileInput {
	t.Helper()
	input.Apply = false
	input.DryRun = false
	input.PreviewToken = ""
	input.AccountIDs = nil
	preview, err := svc.ReconcileGrokOAuth(context.Background(), input)
	require.NoError(t, err)
	require.NotEmpty(t, preview.PreviewToken)
	accountIDs := make([]int64, 0, len(preview.Items))
	for _, item := range preview.Items {
		accountIDs = append(accountIDs, item.AccountID)
	}
	require.NotEmpty(t, accountIDs)
	input.Apply = true
	input.PreviewToken = preview.PreviewToken
	input.AccountIDs = accountIDs
	return input
}

func grokReconcileTestAccount(id int64, email string, credentials map[string]any) Account {
	merged := map[string]any{"email": email, "_token_version": int64(1)}
	for key, value := range credentials {
		merged[key] = value
	}
	return Account{
		ID:          id,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		UpdatedAt:   time.Unix(1_720_000_000+id, 0).UTC(),
		Credentials: merged,
	}
}

func unsignedGrokIDToken(subject, email string) string {
	payload, _ := json.Marshal(map[string]string{"sub": subject, "email": email})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestGrokOAuthReconciliationDefaultsToDryRunAndSanitizesResponse(t *testing.T) {
	now := time.Now().UTC()
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "secret-access"}),
		grokReconcileTestAccount(2, "two@example.com", map[string]any{"refresh_token": "secret-refresh", "expires_at": now.Add(4 * time.Hour).Format(time.RFC3339)}),
		grokReconcileTestAccount(3, "three@example.com", map[string]any{"access_token": "secret-access", "refresh_token": "secret-refresh", "expires_at": now.Add(10 * time.Minute).Format(time.RFC3339)}),
		grokReconcileTestAccount(4, "four@example.com", map[string]any{"access_token": "secret-access", "refresh_token": "secret-refresh", "expires_at": now.Add(4 * time.Hour).Format(time.RFC3339)}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})

	result, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})

	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.Equal(t, 4, result.Scanned)
	require.Equal(t, 3, result.Actionable)
	require.Equal(t, 1, result.WouldBlock)
	require.Equal(t, 2, result.WouldRefresh)
	require.Empty(t, repo.casIDs)
	payload, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(payload), "secret-access")
	require.NotContains(t, string(payload), "secret-refresh")
	require.NotContains(t, string(payload), "example.com")
	require.NotContains(t, string(payload), `"credentials":`)
}

func TestGrokOAuthReconciliationDetectsOwnerMismatchAndCrossPageDuplicate(t *testing.T) {
	now := time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339)
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "same@example.com", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": now, "id_token": unsignedGrokIDToken("subject-one", "same@example.com")}),
		grokReconcileTestAccount(2, "saved@example.com", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": now, "id_token": unsignedGrokIDToken("two", "token@example.com")}),
		grokReconcileTestAccount(3, "same@example.com", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": now, "id_token": unsignedGrokIDToken("subject-three", "same@example.com")}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})

	first, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{Limit: 2})
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.Equal(t, int64(2), first.NextAfterID)
	require.Len(t, first.Items, 1)
	require.Equal(t, GrokOAuthReconcileReasonOwnerMismatch, first.Items[0].Reason)

	second, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{AfterID: first.NextAfterID, Limit: 2})
	require.NoError(t, err)
	require.False(t, second.HasMore)
	require.Len(t, second.Items, 1)
	require.Equal(t, GrokOAuthReconcileReasonDuplicateIdentity, second.Items[0].Reason)
	require.Equal(t, int64(1), second.Items[0].OwnerAccountID)
}

func TestGrokOAuthReconciliationDetectsSubjectDuplicateAcrossPagesWithoutEmail(t *testing.T) {
	now := time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339)
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": now, "id_token": unsignedGrokIDToken("same-subject", "")}),
		grokReconcileTestAccount(2, "", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": now, "id_token": unsignedGrokIDToken("same-subject", "")}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})

	first, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{Limit: 1})
	require.NoError(t, err)
	require.Empty(t, first.Items, "canonical subject owner remains active")
	require.True(t, first.HasMore)

	second, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{AfterID: first.NextAfterID, Limit: 1})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Equal(t, GrokOAuthReconcileReasonDuplicateIdentity, second.Items[0].Reason)
	require.Equal(t, int64(1), second.Items[0].OwnerAccountID)
}

func TestGrokOAuthReconciliationApplyUsesCASAndIsIdempotent(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "access"}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})

	applyInput := grokReconcileApplyInputFromPreview(t, svc, GrokOAuthReconcileInput{})
	first, err := svc.ReconcileGrokOAuth(context.Background(), applyInput)
	require.NoError(t, err)
	require.False(t, first.DryRun)
	require.Equal(t, 1, first.Blocked)
	require.Equal(t, []int64{1}, repo.casIDs)

	second, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})
	require.NoError(t, err)
	require.Zero(t, second.Actionable)
	require.Equal(t, []int64{1}, repo.casIDs)
}

func TestGrokOAuthReconciliationSkipsLostCASRace(t *testing.T) {
	repo := &grokReconcileTestRepo{
		accounts:         []Account{grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "access"})},
		forceCASConflict: true,
	}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})

	applyInput := grokReconcileApplyInputFromPreview(t, svc, GrokOAuthReconcileInput{})
	result, err := svc.ReconcileGrokOAuth(context.Background(), applyInput)

	require.NoError(t, err)
	require.Zero(t, result.Blocked)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, GrokOAuthReconcileOutcomeSkipped, result.Items[0].Outcome)
	require.Empty(t, repo.casIDs)
}

func TestGrokOAuthReconciliationRejectsUnsafeApplyMode(t *testing.T) {
	svc := newGrokReconcileTestService(&grokReconcileTestRepo{}, &grokReconcileTestRefresher{})
	_, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{Apply: true, DryRun: true})
	require.ErrorIs(t, err, ErrGrokOAuthReconcileMode)
}

func TestGrokOAuthReconciliationRequiresMatchingPreviewAndSelection(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "access"}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})

	_, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{Apply: true, AccountIDs: []int64{1}})
	require.ErrorIs(t, err, ErrGrokOAuthReconcilePreviewRequired)
	_, err = svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{Apply: true, PreviewToken: "stale", AccountIDs: []int64{1}})
	require.ErrorIs(t, err, ErrGrokOAuthReconcilePreviewChanged)
	_, err = svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{Apply: true, PreviewToken: "stale"})
	require.ErrorIs(t, err, ErrGrokOAuthReconcileSelection)
	preview, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})
	require.NoError(t, err)
	_, err = svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{
		Apply: true, PreviewToken: preview.PreviewToken, AccountIDs: []int64{999},
	})
	require.ErrorIs(t, err, ErrGrokOAuthReconcileSelection)
}

func TestGrokOAuthReconciliationRejectsPreviewAfterConcurrentCredentialChange(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "access"}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})
	preview, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateCredentials(context.Background(), 1, map[string]any{
		"refresh_token": "new-refresh", "_token_version": int64(2),
	}))

	_, err = svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{
		Apply: true, PreviewToken: preview.PreviewToken, AccountIDs: []int64{1},
	})

	require.ErrorIs(t, err, ErrGrokOAuthReconcilePreviewChanged)
	latest, loadErr := repo.GetByID(context.Background(), 1)
	require.NoError(t, loadErr)
	require.Equal(t, StatusActive, latest.Status)
	require.True(t, latest.Schedulable)
}

func TestGrokOAuthReconciliationAppliesOnlySelectedPreviewAccounts(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "access"}),
		grokReconcileTestAccount(2, "two@example.com", map[string]any{"access_token": "access"}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{})
	preview, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})
	require.NoError(t, err)
	require.Len(t, preview.Items, 2)

	result, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{
		Apply: true, PreviewToken: preview.PreviewToken, AccountIDs: []int64{2},
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Blocked)
	require.Equal(t, []int64{2}, repo.casIDs)
	first, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, StatusActive, first.Status)
	require.True(t, first.Schedulable)
}

func TestGrokOAuthReconciliationDryRunDoesNotRequireRefreshRuntime(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{"access_token": "access"}),
	}}
	svc := &TokenRefreshService{accountRepo: repo, candidatePager: repo}

	result, err := svc.ReconcileGrokOAuth(context.Background(), GrokOAuthReconcileInput{})

	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.Equal(t, 1, result.Actionable)
}

func TestGrokOAuthReconciliationRefreshFailureStopsScheduling(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{
			"access_token": "access", "refresh_token": "refresh", "expires_at": time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
		}),
	}}
	svc := newGrokReconcileTestService(repo, &grokReconcileTestRefresher{err: errors.New("temporary upstream failure")})

	applyInput := grokReconcileApplyInputFromPreview(t, svc, GrokOAuthReconcileInput{})
	result, err := svc.ReconcileGrokOAuth(context.Background(), applyInput)

	require.NoError(t, err)
	require.Equal(t, 1, result.Blocked)
	require.Equal(t, GrokOAuthReconcileReasonRefreshFailed, result.Items[0].Reason)
	latest, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.False(t, latest.Schedulable)
	require.Equal(t, StatusError, latest.Status)
	require.False(t, strings.Contains(latest.ErrorMessage, "upstream failure"), "provider error details must not be stored by reconciliation")
}

func TestGrokOAuthReconciliationRefreshFailureDoesNotBlockConcurrentReauthorization(t *testing.T) {
	repo := &grokReconcileTestRepo{accounts: []Account{
		grokReconcileTestAccount(1, "one@example.com", map[string]any{
			"access_token": "old-access", "refresh_token": "old-refresh", "expires_at": time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339),
		}),
	}}
	refresher := &grokReconcileTestRefresher{err: errors.New("temporary upstream failure")}
	refresher.onRefresh = func() {
		require.NoError(t, repo.UpdateCredentials(context.Background(), 1, map[string]any{
			"access_token": "new-access", "refresh_token": "new-refresh",
			"expires_at": time.Now().UTC().Add(8 * time.Hour).Format(time.RFC3339), "_token_version": int64(2),
		}))
	}
	svc := newGrokReconcileTestService(repo, refresher)

	applyInput := grokReconcileApplyInputFromPreview(t, svc, GrokOAuthReconcileInput{})
	result, err := svc.ReconcileGrokOAuth(context.Background(), applyInput)

	require.NoError(t, err)
	require.Zero(t, result.Blocked)
	require.Equal(t, 1, result.Skipped)
	require.Equal(t, GrokOAuthReconcileOutcomeSkipped, result.Items[0].Outcome)
	latest, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, StatusActive, latest.Status)
	require.True(t, latest.Schedulable)
	require.Equal(t, "new-access", latest.GetGrokAccessToken())
}
