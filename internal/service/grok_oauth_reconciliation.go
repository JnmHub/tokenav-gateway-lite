package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	defaultGrokOAuthReconcilePageSize = 50
	maxGrokOAuthReconcilePageSize     = 500
	maxGrokOAuthReconcileWindow       = 24 * time.Hour

	GrokOAuthReconcileReasonMissingRefreshToken = "missing_refresh_token"
	GrokOAuthReconcileReasonMissingAccessToken  = "missing_access_token"
	GrokOAuthReconcileReasonMissingExpiry       = "missing_expiry"
	GrokOAuthReconcileReasonInvalidExpiry       = "invalid_expiry"
	GrokOAuthReconcileReasonNearExpiry          = "near_expiry"
	GrokOAuthReconcileReasonMissingIdentity     = "missing_stable_identity"
	GrokOAuthReconcileReasonOwnerMismatch       = "owner_mismatch"
	GrokOAuthReconcileReasonOwnerChanged        = "owner_changed"
	GrokOAuthReconcileReasonDuplicateIdentity   = "duplicate_stable_identity"
	GrokOAuthReconcileReasonRefreshFailed       = "refresh_failed"

	GrokOAuthReconcileActionBlock   = "block_account"
	GrokOAuthReconcileActionRefresh = "refresh_credentials"

	GrokOAuthReconcileOutcomePlanned = "planned"
	GrokOAuthReconcileOutcomeApplied = "applied"
	GrokOAuthReconcileOutcomeSkipped = "skipped"
	GrokOAuthReconcileOutcomeFailed  = "failed"
	GrokOAuthReconcileOutcomePartial = "partial"
)

var (
	ErrGrokOAuthReconcileMode = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_MODE_INVALID",
		"apply requires dry_run=false and apply=true",
	)
	ErrGrokOAuthReconcileCursor = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_CURSOR_INVALID",
		"after_id must be non-negative",
	)
	ErrGrokOAuthReconcileLimit = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_LIMIT_INVALID",
		"limit is outside the allowed reconciliation page range",
	)
	ErrGrokOAuthReconcileWindow = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_WINDOW_INVALID",
		"refresh_window_seconds is outside the allowed range",
	)
	ErrGrokOAuthReconcilePreviewRequired = infraerrors.Conflict(
		"GROK_OAUTH_RECONCILE_PREVIEW_REQUIRED",
		"run a fresh preview before applying Grok OAuth repairs",
	)
	ErrGrokOAuthReconcilePreviewChanged = infraerrors.Conflict(
		"GROK_OAUTH_RECONCILE_PREVIEW_CHANGED",
		"Grok OAuth accounts changed after preview; scan again before applying",
	)
	ErrGrokOAuthReconcileSelection = infraerrors.BadRequest(
		"GROK_OAUTH_RECONCILE_SELECTION_INVALID",
		"apply requires one to 500 unique positive account_ids from the preview",
	)
)

// GrokOAuthReconciler is deliberately narrow so the admin handler can be tested
// without constructing the complete token refresh runtime.
type GrokOAuthReconciler interface {
	ReconcileGrokOAuth(ctx context.Context, input GrokOAuthReconcileInput) (*GrokOAuthReconcileResult, error)
}

// GrokOAuthReconcileSnapshot is the compare-and-set boundary used immediately
// after a final database reread. The token version, proxy and updated_at together
// prevent an old scan from quarantining an account that an administrator has
// just reauthorized or moved to another proxy.
type GrokOAuthReconcileSnapshot struct {
	UpdatedAt    time.Time
	TokenVersion int64
	ProxyID      *int64
}

type GrokOAuthReconcileCASRepository interface {
	SetGrokOAuthReconcileErrorIfUnchanged(ctx context.Context, id int64, expected GrokOAuthReconcileSnapshot, errorMsg string) (bool, error)
}

// GrokOAuthIdentityOwner contains only database IDs and counts. Stable identity
// strings never cross the service response or operation audit boundary.
type GrokOAuthIdentityOwner struct {
	CanonicalAccountID int64
	Count              int
}

type GrokOAuthIdentityOwnerRepository interface {
	FindGrokOAuthIdentityOwners(ctx context.Context, identityKeys []string) (map[string]GrokOAuthIdentityOwner, error)
}

type GrokOAuthReconcileInput struct {
	DryRun        bool
	Apply         bool
	AfterID       int64
	Limit         int
	RefreshWindow time.Duration
	PreviewToken  string
	AccountIDs    []int64
}

// GrokOAuthReconcileItem is metadata-only by design. Never add credentials,
// provider bodies, email addresses or raw errors to this response.
type GrokOAuthReconcileItem struct {
	AccountID      int64  `json:"account_id"`
	Reason         string `json:"reason"`
	Action         string `json:"action"`
	Outcome        string `json:"outcome"`
	OwnerAccountID int64  `json:"owner_account_id,omitempty"`
}

type GrokOAuthReconcileResult struct {
	DryRun       bool                     `json:"dry_run"`
	Scanned      int                      `json:"scanned"`
	Actionable   int                      `json:"actionable"`
	WouldBlock   int                      `json:"would_block"`
	WouldRefresh int                      `json:"would_refresh"`
	Blocked      int                      `json:"blocked"`
	Refreshed    int                      `json:"refreshed"`
	Skipped      int                      `json:"skipped"`
	Failed       int                      `json:"failed"`
	Partial      int                      `json:"partial"`
	Items        []GrokOAuthReconcileItem `json:"items"`
	NextAfterID  int64                    `json:"next_after_id"`
	HasMore      bool                     `json:"has_more"`
	PreviewToken string                   `json:"preview_token"`
}

type grokStableIdentity struct {
	Key        string
	Email      string
	TokenEmail string
}

type grokIdentityClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

func normalizeGrokIdentityEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func parseGrokIdentityClaims(token string) grokIdentityClaims {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return grokIdentityClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return grokIdentityClaims{}
	}
	var claims grokIdentityClaims
	if json.Unmarshal(payload, &claims) != nil {
		return grokIdentityClaims{}
	}
	claims.Subject = strings.TrimSpace(claims.Subject)
	claims.Email = normalizeGrokIdentityEmail(claims.Email)
	return claims
}

func grokAccountStableIdentity(account *Account) grokStableIdentity {
	if account == nil {
		return grokStableIdentity{}
	}
	email := normalizeGrokIdentityEmail(account.GetCredential("email"))
	claims := parseGrokIdentityClaims(account.GetCredential("id_token"))
	identity := grokStableIdentity{Email: email, TokenEmail: claims.Email}
	if claims.Subject != "" {
		identity.Key = "sub:" + claims.Subject
	} else if email != "" {
		identity.Key = "email:" + email
	} else if claims.Email != "" {
		identity.Key = "email:" + claims.Email
	}
	return identity
}

// GrokOAuthStableIdentityKeys returns only normalized comparison keys. It is
// exported for the repository adapter so a reconciliation page can compare its
// JWT subject identities with every active Grok OAuth account in PostgreSQL.
// Raw tokens, subjects and emails never cross the administrator API boundary.
func GrokOAuthStableIdentityKeys(email, idToken string) []string {
	email = normalizeGrokIdentityEmail(email)
	claims := parseGrokIdentityClaims(idToken)
	keys := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	appendKey := func(key string) {
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	if claims.Subject != "" {
		appendKey("sub:" + claims.Subject)
	} else if email != "" {
		appendKey("email:" + email)
	} else if claims.Email != "" {
		appendKey("email:" + claims.Email)
	}
	// Stored email remains a secondary duplicate signal when a subject exists.
	appendKey("email:" + email)
	return keys
}

func collectGrokIdentityOwners(
	ctx context.Context,
	repo AccountRepository,
	accounts []Account,
) (map[string]GrokOAuthIdentityOwner, error) {
	owners := make(map[string]GrokOAuthIdentityOwner)
	addOwner := func(key string, accountID int64) {
		if key == "" {
			return
		}
		owner := owners[key]
		if owner.CanonicalAccountID == 0 || accountID < owner.CanonicalAccountID {
			owner.CanonicalAccountID = accountID
		}
		owner.Count++
		owners[key] = owner
	}
	identityKeys := make([]string, 0, len(accounts)*2)
	seenKeys := make(map[string]struct{}, len(accounts)*2)
	for i := range accounts {
		identity := grokAccountStableIdentity(&accounts[i])
		addOwner(identity.Key, accounts[i].ID)
		emailKey := "email:" + identity.Email
		if identity.Email != "" && emailKey != identity.Key {
			// Subject is the strongest identity, but duplicate imports with the
			// same provider email must still reconcile even if their ID tokens
			// expose different or rotated subjects.
			addOwner(emailKey, accounts[i].ID)
		}
		for _, key := range GrokOAuthStableIdentityKeys(
			accounts[i].GetCredential("email"),
			accounts[i].GetCredential("id_token"),
		) {
			if _, exists := seenKeys[key]; !exists {
				seenKeys[key] = struct{}{}
				identityKeys = append(identityKeys, key)
			}
		}
	}
	identityRepo, ok := repo.(GrokOAuthIdentityOwnerRepository)
	if !ok || len(identityKeys) == 0 {
		return owners, nil
	}
	durable, err := identityRepo.FindGrokOAuthIdentityOwners(ctx, identityKeys)
	if err != nil {
		return nil, err
	}
	for key, owner := range durable {
		owners[key] = owner
	}
	return owners, nil
}

func classifyGrokOAuthReconcileAccount(
	account *Account,
	refreshWindow time.Duration,
	owners map[string]GrokOAuthIdentityOwner,
) (reason, action string, ownerAccountID int64, actionable bool) {
	if account == nil || !account.IsGrokOAuth() || account.Status != StatusActive || !account.Schedulable {
		return "", "", 0, false
	}
	if strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		return GrokOAuthReconcileReasonMissingRefreshToken, GrokOAuthReconcileActionBlock, 0, true
	}
	identity := grokAccountStableIdentity(account)
	if identity.Email != "" && identity.TokenEmail != "" && identity.Email != identity.TokenEmail {
		return GrokOAuthReconcileReasonOwnerMismatch, GrokOAuthReconcileActionBlock, 0, true
	}
	if identity.Key == "" {
		return GrokOAuthReconcileReasonMissingIdentity, GrokOAuthReconcileActionRefresh, 0, true
	}
	owner := owners[identity.Key]
	if identity.Email != "" {
		if emailOwner := owners["email:"+identity.Email]; emailOwner.Count > 1 &&
			(owner.Count <= 1 || emailOwner.CanonicalAccountID < owner.CanonicalAccountID) {
			owner = emailOwner
		}
	}
	if owner.Count > 1 && owner.CanonicalAccountID > 0 && owner.CanonicalAccountID != account.ID {
		return GrokOAuthReconcileReasonDuplicateIdentity, GrokOAuthReconcileActionBlock, owner.CanonicalAccountID, true
	}
	if strings.TrimSpace(account.GetGrokAccessToken()) == "" {
		return GrokOAuthReconcileReasonMissingAccessToken, GrokOAuthReconcileActionRefresh, 0, true
	}
	rawExpiry := strings.TrimSpace(account.GetCredential("expires_at"))
	if rawExpiry == "" {
		return GrokOAuthReconcileReasonMissingExpiry, GrokOAuthReconcileActionRefresh, 0, true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return GrokOAuthReconcileReasonInvalidExpiry, GrokOAuthReconcileActionRefresh, 0, true
	}
	if time.Until(*expiresAt) <= refreshWindow {
		return GrokOAuthReconcileReasonNearExpiry, GrokOAuthReconcileActionRefresh, 0, true
	}
	return "", "", 0, false
}

func grokOAuthReconcilePreviewToken(
	afterID int64,
	limit int,
	refreshWindow time.Duration,
	accounts []Account,
	owners map[string]GrokOAuthIdentityOwner,
) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "grok-oauth-reconcile:v1|after=%d|limit=%d|window=%d\n", afterID, limit, refreshWindow.Milliseconds())
	for i := range accounts {
		account := &accounts[i]
		reason, action, ownerID, actionable := classifyGrokOAuthReconcileAccount(account, refreshWindow, owners)
		proxyID := int64(0)
		if account.ProxyID != nil {
			proxyID = *account.ProxyID
		}
		// The digest deliberately contains only a one-way fingerprint of the
		// minimum CAS/classification state. No credential or identity value is
		// returned to the administrator response.
		_, _ = fmt.Fprintf(hash, "%d|%d|%d|%d|%t|%s|%s|%d|%t\n",
			account.ID,
			account.UpdatedAt.UnixNano(),
			account.GetCredentialAsInt64("_token_version"),
			proxyID,
			account.Schedulable,
			reason,
			action,
			ownerID,
			actionable,
		)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func normalizeGrokOAuthReconcileSelection(accountIDs []int64) (map[int64]struct{}, error) {
	if len(accountIDs) == 0 || len(accountIDs) > maxGrokOAuthReconcilePageSize {
		return nil, ErrGrokOAuthReconcileSelection
	}
	selected := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return nil, ErrGrokOAuthReconcileSelection
		}
		if _, exists := selected[accountID]; exists {
			return nil, ErrGrokOAuthReconcileSelection
		}
		selected[accountID] = struct{}{}
	}
	return selected, nil
}

func (s *TokenRefreshService) ReconcileGrokOAuth(ctx context.Context, input GrokOAuthReconcileInput) (*GrokOAuthReconcileResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if input.Apply && input.DryRun {
		return nil, ErrGrokOAuthReconcileMode
	}
	if input.AfterID < 0 {
		return nil, ErrGrokOAuthReconcileCursor
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultGrokOAuthReconcilePageSize
	}
	if limit < 1 || limit > maxGrokOAuthReconcilePageSize {
		return nil, ErrGrokOAuthReconcileLimit
	}
	refreshWindow := input.RefreshWindow
	if refreshWindow == 0 {
		refreshWindow = grokTokenRefreshSkew
	}
	if refreshWindow < 0 || refreshWindow > maxGrokOAuthReconcileWindow {
		return nil, ErrGrokOAuthReconcileWindow
	}
	if refreshWindow < grokTokenRefreshSkew {
		refreshWindow = grokTokenRefreshSkew
	}
	dryRun := !input.Apply
	var selected map[int64]struct{}
	if input.Apply {
		var selectionErr error
		selected, selectionErr = normalizeGrokOAuthReconcileSelection(input.AccountIDs)
		if selectionErr != nil {
			return nil, selectionErr
		}
		if strings.TrimSpace(input.PreviewToken) == "" {
			return nil, ErrGrokOAuthReconcilePreviewRequired
		}
	}

	pager := s.candidatePager
	if pager == nil && s.accountRepo != nil {
		pager, _ = s.accountRepo.(OAuthRefreshCandidatePager)
	}
	if pager == nil {
		return nil, errors.New("OAuth refresh candidate pager is not configured")
	}
	page, err := pager.ListOAuthRefreshCandidatePage(ctx, OAuthRefreshPageOptions{
		Platforms:           []string{PlatformGrok},
		AfterID:             input.AfterID,
		Limit:               limit,
		ActiveOnly:          true,
		IncludeSetupToken:   false,
		RequireRefreshToken: false,
	})
	if err != nil {
		return nil, err
	}
	if page == nil || !isStrictlyIncreasingAccountPage(page.Accounts, input.AfterID) {
		return nil, errors.New("OAuth reconciliation repository returned an invalid cursor page")
	}
	if page.HasMore && page.NextAfterID <= input.AfterID {
		return nil, errors.New("OAuth reconciliation repository returned invalid cursor metadata")
	}
	if input.Apply {
		pageIDs := make(map[int64]struct{}, len(page.Accounts))
		for i := range page.Accounts {
			pageIDs[page.Accounts[i].ID] = struct{}{}
		}
		for accountID := range selected {
			if _, ok := pageIDs[accountID]; !ok {
				return nil, ErrGrokOAuthReconcileSelection
			}
		}
	}
	owners, err := collectGrokIdentityOwners(ctx, s.accountRepo, page.Accounts)
	if err != nil {
		return nil, fmt.Errorf("load Grok OAuth stable identity owners: %w", err)
	}

	previewToken := grokOAuthReconcilePreviewToken(input.AfterID, limit, refreshWindow, page.Accounts, owners)
	if input.Apply && subtle.ConstantTimeCompare([]byte(previewToken), []byte(strings.TrimSpace(input.PreviewToken))) != 1 {
		return nil, ErrGrokOAuthReconcilePreviewChanged
	}
	result := &GrokOAuthReconcileResult{
		DryRun:       dryRun,
		Scanned:      len(page.Accounts),
		Items:        make([]GrokOAuthReconcileItem, 0, len(page.Accounts)),
		HasMore:      page.HasMore,
		NextAfterID:  page.NextAfterID,
		PreviewToken: previewToken,
	}
	var registration tokenRefreshRegistration
	var providerState *tokenRefreshProviderState
	casRepo, supportsCAS := s.accountRepo.(GrokOAuthReconcileCASRepository)
	if input.Apply && !supportsCAS {
		return nil, errors.New("grok OAuth reconciliation CAS repository is not configured")
	}
	if input.Apply {
		var ok bool
		registration, ok = s.grokRegistration()
		if !ok {
			return nil, errors.New("grok OAuth refresher is not registered")
		}
		providerState = &tokenRefreshProviderState{
			service:      s,
			registration: registration,
			rateGate:     s.providerRateGate(PlatformGrok),
			poolGate:     s.providerConcurrencyGate(PlatformGrok),
		}
	}

	for i := range page.Accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		account := &page.Accounts[i]
		if input.Apply {
			if _, ok := selected[account.ID]; !ok {
				continue
			}
		}
		reason, action, ownerID, actionable := classifyGrokOAuthReconcileAccount(account, refreshWindow, owners)
		if !actionable {
			result.Skipped++
			continue
		}
		result.Actionable++
		item := GrokOAuthReconcileItem{
			AccountID:      account.ID,
			Reason:         reason,
			Action:         action,
			Outcome:        GrokOAuthReconcileOutcomePlanned,
			OwnerAccountID: ownerID,
		}
		if action == GrokOAuthReconcileActionBlock {
			result.WouldBlock++
		} else {
			result.WouldRefresh++
		}
		if dryRun {
			result.Items = append(result.Items, item)
			continue
		}

		switch action {
		case GrokOAuthReconcileActionBlock:
			latest, readErr := s.accountRepo.GetByID(ctx, account.ID)
			if readErr != nil || latest == nil {
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
				break
			}
			latestReason, latestAction, latestOwnerID, stillActionable := classifyGrokOAuthReconcileAccount(latest, refreshWindow, owners)
			if !stillActionable || latestAction != GrokOAuthReconcileActionBlock {
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			item.Reason, item.OwnerAccountID = latestReason, latestOwnerID
			s.applyGrokOAuthReconcileBlock(ctx, casRepo, latest, item.Reason, &item, result)

		case GrokOAuthReconcileActionRefresh:
			// Keep the scan snapshot immutable. A failed provider refresh must
			// never quarantine credentials that an administrator reauthorized
			// while this reconciliation attempt was in flight.
			blockCandidate := *account
			beforeIdentity := grokAccountStableIdentity(account).Key
			force := reason == GrokOAuthReconcileReasonMissingIdentity
			refreshErr := s.refreshGrokOAuthForReconciliation(ctx, account, registration, providerState, refreshWindow, force)
			providerState.recordResult(refreshErr)
			if errors.Is(refreshErr, errRefreshSkipped) {
				item.Outcome = GrokOAuthReconcileOutcomeSkipped
				result.Skipped++
				break
			}
			latest, readErr := s.accountRepo.GetByID(ctx, account.ID)
			if readErr != nil || latest == nil {
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
				break
			}
			if refreshErr != nil {
				item.Reason = GrokOAuthReconcileReasonRefreshFailed
				item.Action = GrokOAuthReconcileActionBlock
				s.applyGrokOAuthReconcileBlock(ctx, casRepo, &blockCandidate, item.Reason, &item, result)
				break
			}
			afterIdentity := grokAccountStableIdentity(latest).Key
			if beforeIdentity != "" && afterIdentity != "" && beforeIdentity != afterIdentity {
				item.Reason = GrokOAuthReconcileReasonOwnerChanged
				item.Action = GrokOAuthReconcileActionBlock
				s.applyGrokOAuthReconcileBlock(ctx, casRepo, latest, item.Reason, &item, result)
				break
			}
			latestOwners, ownerErr := collectGrokIdentityOwners(ctx, s.accountRepo, []Account{*latest})
			if ownerErr != nil {
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
				break
			}
			latestReason, latestAction, latestOwnerID, stillActionable := classifyGrokOAuthReconcileAccount(latest, refreshWindow, latestOwners)
			if stillActionable {
				item.Reason, item.OwnerAccountID = latestReason, latestOwnerID
				if latestAction == GrokOAuthReconcileActionBlock || latestReason == GrokOAuthReconcileReasonMissingIdentity {
					item.Action = GrokOAuthReconcileActionBlock
					s.applyGrokOAuthReconcileBlock(ctx, casRepo, latest, item.Reason, &item, result)
					break
				}
				item.Outcome = GrokOAuthReconcileOutcomeFailed
				result.Failed++
				break
			}
			item.Outcome = GrokOAuthReconcileOutcomeApplied
			result.Refreshed++
		default:
			return nil, errors.New("unsupported Grok OAuth reconciliation action")
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (s *TokenRefreshService) grokRegistration() (tokenRefreshRegistration, bool) {
	for _, registration := range s.registrations {
		if registration.platform == PlatformGrok && registration.refresher != nil && registration.executor != nil {
			return registration, true
		}
	}
	return tokenRefreshRegistration{}, false
}

func (s *TokenRefreshService) refreshGrokOAuthForReconciliation(
	ctx context.Context,
	account *Account,
	registration tokenRefreshRegistration,
	providerState *tokenRefreshProviderState,
	refreshWindow time.Duration,
	force bool,
) error {
	if !force {
		return s.refreshWithRetryWithProviderState(ctx, account, registration.refresher, registration.executor, refreshWindow, providerState)
	}
	if s.refreshAPI == nil {
		return errors.New("OAuth refresh API is not configured")
	}
	releaseAttempt, err := providerState.acquireAttempt(ctx)
	if err != nil {
		return err
	}
	defer releaseAttempt()
	attemptCtx, cancel := context.WithTimeout(ctx, s.attemptTimeout())
	defer cancel()
	executor := &rateLimitedOAuthRefreshExecutor{
		OAuthRefreshExecutor: registration.executor,
		acquireRate:          providerState.acquireRate,
	}
	refreshResult, err := s.refreshAPI.RefreshNow(attemptCtx, account, executor)
	if err != nil {
		return err
	}
	if refreshResult == nil || refreshResult.LockHeld {
		return errRefreshSkipped
	}
	if refreshResult.Refreshed {
		refreshedAccount := account
		if refreshResult.Account != nil {
			refreshedAccount = refreshResult.Account
		}
		s.postRefreshActions(ctx, refreshedAccount)
	}
	return nil
}

func (s *TokenRefreshService) applyGrokOAuthReconcileBlock(
	ctx context.Context,
	repo GrokOAuthReconcileCASRepository,
	account *Account,
	reason string,
	item *GrokOAuthReconcileItem,
	result *GrokOAuthReconcileResult,
) {
	if repo == nil || account == nil || item == nil || result == nil {
		if item != nil {
			item.Outcome = GrokOAuthReconcileOutcomeFailed
		}
		if result != nil {
			result.Failed++
		}
		return
	}
	snapshot := GrokOAuthReconcileSnapshot{
		UpdatedAt:    account.UpdatedAt,
		TokenVersion: account.GetCredentialAsInt64("_token_version"),
		ProxyID:      account.ProxyID,
	}
	applied, err := repo.SetGrokOAuthReconcileErrorIfUnchanged(
		ctx,
		account.ID,
		snapshot,
		"Grok OAuth reconciliation: "+reason,
	)
	if err != nil {
		item.Outcome = GrokOAuthReconcileOutcomeFailed
		result.Failed++
		return
	}
	if !applied {
		item.Outcome = GrokOAuthReconcileOutcomeSkipped
		result.Skipped++
		return
	}
	s.notifyAccountSchedulingBlocked(account, time.Time{}, "grok_oauth_reconcile_"+reason)
	cacheFailed := s.cacheInvalidator == nil
	if s.cacheInvalidator != nil {
		if invalidateErr := s.cacheInvalidator.InvalidateToken(ctx, account); invalidateErr != nil {
			cacheFailed = true
		}
	}
	result.Blocked++
	if cacheFailed {
		item.Outcome = GrokOAuthReconcileOutcomePartial
		result.Partial++
	} else {
		item.Outcome = GrokOAuthReconcileOutcomeApplied
	}
}
