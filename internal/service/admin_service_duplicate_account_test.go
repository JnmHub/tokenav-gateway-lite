//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type duplicateAccountRepoStub struct {
	*accountRepoStub
	accounts map[int64]*Account
	nextID   int64
	groups   map[int64][]AccountGroup
}

func newDuplicateAccountRepoStub(source *Account) *duplicateAccountRepoStub {
	return &duplicateAccountRepoStub{
		accountRepoStub: &accountRepoStub{},
		accounts:        map[int64]*Account{source.ID: source},
		nextID:          source.ID + 1,
		groups:          make(map[int64][]AccountGroup),
	}
}

func (s *duplicateAccountRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	account := s.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	return account, nil
}

func (s *duplicateAccountRepoStub) FindByExtraField(_ context.Context, key string, value any) ([]Account, error) {
	var matches []Account
	for _, account := range s.accounts {
		if account.Extra != nil && account.Extra[key] == value {
			matches = append(matches, *account)
		}
	}
	return matches, nil
}

func (s *duplicateAccountRepoStub) CreateWithAccountGroups(_ context.Context, account *Account, groups []AccountGroup) error {
	account.ID = s.nextID
	s.nextID++
	clonedGroups := append([]AccountGroup(nil), groups...)
	for i := range clonedGroups {
		clonedGroups[i].AccountID = account.ID
	}
	account.AccountGroups = clonedGroups
	s.groups[account.ID] = clonedGroups
	s.accounts[account.ID] = account
	return nil
}

type duplicateAccountUniqueConflictRepoStub struct {
	*duplicateAccountRepoStub
}

type duplicateAccountCancelAwareRepoStub struct {
	*duplicateAccountRepoStub
}

type duplicateAccountDelayedRecoveryRepoStub struct {
	*duplicateAccountRepoStub
	findCalls       int
	revealAfter     int
	winner          *Account
	createErr       error
	findErr         error
	publishOnCreate bool
	cancelOnCreate  context.CancelFunc
}

func (s *duplicateAccountDelayedRecoveryRepoStub) FindByExtraField(ctx context.Context, _ string, _ any) ([]Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.findCalls++
	if s.findErr != nil {
		return nil, s.findErr
	}
	if s.winner != nil && s.findCalls >= s.revealAfter {
		return []Account{*s.winner}, nil
	}
	return nil, nil
}

func (s *duplicateAccountDelayedRecoveryRepoStub) CreateWithAccountGroups(_ context.Context, account *Account, groups []AccountGroup) error {
	if s.cancelOnCreate != nil {
		s.cancelOnCreate()
	}
	if s.publishOnCreate {
		winner := *account
		winner.ID = s.nextID
		s.nextID++
		winnerGroups := append([]AccountGroup(nil), groups...)
		for i := range winnerGroups {
			winnerGroups[i].AccountID = winner.ID
		}
		winner.AccountGroups = winnerGroups
		s.winner = &winner
	}
	return s.createErr
}

func (s *duplicateAccountCancelAwareRepoStub) FindByExtraField(ctx context.Context, key string, value any) ([]Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.duplicateAccountRepoStub.FindByExtraField(ctx, key, value)
}

func (s *duplicateAccountUniqueConflictRepoStub) CreateWithAccountGroups(_ context.Context, account *Account, groups []AccountGroup) error {
	// 模拟另一个节点先提交同一个 operation id，当前 INSERT 随后命中唯一索引。
	winner := *account
	winner.ID = s.nextID
	s.nextID++
	winnerGroups := append([]AccountGroup(nil), groups...)
	for i := range winnerGroups {
		winnerGroups[i].AccountID = winner.ID
	}
	winner.AccountGroups = winnerGroups
	s.accounts[winner.ID] = &winner
	s.groups[winner.ID] = winnerGroups
	return errors.New("duplicate operation id unique constraint")
}

func TestDuplicateAccountCopiesConfigurationAndClearsRuntimeState(t *testing.T) {
	notes := "保留备注"
	proxyID := int64(17)
	originalProxyID := int64(11)
	rateMultiplier := 1.25
	loadFactor := 9
	expiresAt := time.Date(2027, time.March, 4, 5, 6, 7, 0, time.UTC)
	runtimeAt := time.Now().Add(time.Hour)
	source := &Account{
		ID:                      42,
		Name:                    "primary",
		Notes:                   &notes,
		Platform:                PlatformAnthropic,
		Type:                    AccountTypeAPIKey,
		ProxyID:                 &proxyID,
		ProxyFallbackOriginID:   &originalProxyID,
		Concurrency:             6,
		Priority:                40,
		RateMultiplier:          &rateMultiplier,
		LoadFactor:              &loadFactor,
		Status:                  StatusError,
		Schedulable:             true,
		ErrorMessage:            "upstream unavailable",
		ExpiresAt:               &expiresAt,
		AutoPauseOnExpired:      false,
		RateLimitedAt:           &runtimeAt,
		RateLimitResetAt:        &runtimeAt,
		OverloadUntil:           &runtimeAt,
		TempUnschedulableUntil:  &runtimeAt,
		TempUnschedulableReason: "maintenance",
		SessionWindowStart:      &runtimeAt,
		SessionWindowEnd:        &runtimeAt,
		SessionWindowStatus:     "active",
		Credentials: map[string]any{
			"api_key": "secret",
			"nested":  map[string]any{"token": "source-token"},
		},
		Extra: map[string]any{
			"config":                     map[string]any{"region": "us-east-1"},
			"quota_limit":                1000,
			"quota_used":                 450,
			"model_rate_limits":          map[string]any{"gpt-5": "2099-01-01T00:00:00Z"},
			"grok_usage_snapshot":        map[string]any{"status_code": 429},
			"openai_responses_supported": false,
			"crs_account_id":             "remote-42",
		},
		GroupIDs:      []int64{7, 3},
		AccountGroups: []AccountGroup{{GroupID: 7, Priority: 50}, {GroupID: 3, Priority: 7}},
	}
	repo := newDuplicateAccountRepoStub(source)
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

	duplicate, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:1", "copy-42")

	require.NoError(t, err)
	require.NotEqual(t, source.ID, duplicate.ID)
	require.Equal(t, "primary (Copy)", duplicate.Name)
	require.Equal(t, source.Credentials, duplicate.Credentials)
	require.Equal(t, source.ProxyFallbackOriginID, duplicate.ProxyID)
	require.Equal(t, source.GroupIDs, duplicate.GroupIDs)
	require.Equal(t, []AccountGroup{{AccountID: duplicate.ID, GroupID: 7, Priority: 50}, {AccountID: duplicate.ID, GroupID: 3, Priority: 7}}, repo.groups[duplicate.ID])
	require.Equal(t, StatusActive, duplicate.Status)
	require.False(t, duplicate.Schedulable)
	require.Empty(t, duplicate.ErrorMessage)
	require.Nil(t, duplicate.LastUsedAt)
	require.Nil(t, duplicate.RateLimitedAt)
	require.Nil(t, duplicate.RateLimitResetAt)
	require.Nil(t, duplicate.OverloadUntil)
	require.Nil(t, duplicate.TempUnschedulableUntil)
	require.Empty(t, duplicate.TempUnschedulableReason)
	require.Nil(t, duplicate.SessionWindowStart)
	require.Nil(t, duplicate.SessionWindowEnd)
	require.Empty(t, duplicate.SessionWindowStatus)
	require.Contains(t, duplicate.Extra, "config")
	require.Contains(t, duplicate.Extra, "quota_limit")
	require.Contains(t, duplicate.Extra, duplicateAccountOperationIDExtraKey)
	for _, key := range []string{"quota_used", "model_rate_limits", "grok_usage_snapshot", "openai_responses_supported", "crs_account_id"} {
		require.NotContains(t, duplicate.Extra, key)
	}

	duplicate.Credentials["nested"].(map[string]any)["token"] = "changed"
	duplicate.Extra["config"].(map[string]any)["region"] = "changed"
	require.Equal(t, "source-token", source.Credentials["nested"].(map[string]any)["token"])
	require.Equal(t, "us-east-1", source.Extra["config"].(map[string]any)["region"])
}

func TestDuplicateAccountReturnsExistingCopyForSameOperationKey(t *testing.T) {
	source := &Account{ID: 9, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "secret"}}
	repo := newDuplicateAccountRepoStub(source)
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

	first, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:7", "stable-key")
	require.NoError(t, err)
	second, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:7", "stable-key")
	require.NoError(t, err)
	otherAdmin, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:8", "stable-key")
	require.NoError(t, err)

	require.Equal(t, first.ID, second.ID)
	require.NotEqual(t, first.ID, otherAdmin.ID)
	require.Len(t, repo.accounts, 3)
}

func TestDuplicateAccountRecoversWinnerAfterDatabaseUniqueConflict(t *testing.T) {
	source := &Account{ID: 19, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "secret"}}
	baseRepo := newDuplicateAccountRepoStub(source)
	repo := &duplicateAccountUniqueConflictRepoStub{duplicateAccountRepoStub: baseRepo}
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

	duplicate, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:7", "concurrent-key")

	require.NoError(t, err)
	require.NotNil(t, duplicate)
	require.Equal(t, int64(20), duplicate.ID)
	require.Equal(t, duplicateAccountOperationID(source.ID, "admin:7", "concurrent-key"), duplicate.Extra[duplicateAccountOperationIDExtraKey])
	require.Len(t, repo.accounts, 2)
}

func TestDuplicateAccountPollsUntilWinnerBecomesVisible(t *testing.T) {
	tests := []struct {
		name             string
		createErr        error
		cancelRequestCtx bool
	}{
		{name: "unique conflict", createErr: &pq.Error{Code: "23505", Message: "duplicate key value violates unique constraint"}},
		{name: "canceled uncertain commit", createErr: context.Canceled, cancelRequestCtx: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &Account{ID: 39, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "secret"}}
			baseRepo := newDuplicateAccountRepoStub(source)
			repo := &duplicateAccountDelayedRecoveryRepoStub{
				duplicateAccountRepoStub: baseRepo,
				revealAfter:              3, // 初始预查 + 一次恢复 miss 后才可见。
				createErr:                tt.createErr,
				publishOnCreate:          true,
			}
			var requestCtx context.Context = context.Background()
			if tt.cancelRequestCtx {
				var cancel context.CancelFunc
				requestCtx, cancel = context.WithCancel(requestCtx)
				repo.cancelOnCreate = cancel
			}
			svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

			duplicate, err := svc.DuplicateAccount(requestCtx, source.ID, "admin:7", "delayed-winner")

			require.NoError(t, err)
			require.NotNil(t, duplicate)
			require.Equal(t, int64(40), duplicate.ID)
			require.Equal(t, 3, repo.findCalls)
		})
	}
}

func TestRecoverDuplicateAccountIgnoresCanceledRequestContext(t *testing.T) {
	source := &Account{ID: 29, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	baseRepo := newDuplicateAccountRepoStub(source)
	operationKey := "uncertain-commit"
	operationID := duplicateAccountOperationID(source.ID, "admin:4", operationKey)
	baseRepo.accounts[30] = &Account{
		ID:       30,
		Name:     "source (Copy)",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Extra:    map[string]any{duplicateAccountOperationIDExtraKey: operationID},
	}
	repo := &duplicateAccountCancelAwareRepoStub{duplicateAccountRepoStub: baseRepo}
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()

	recovered, err := svc.RecoverDuplicateAccount(requestCtx, source.ID, "admin:4", operationKey)

	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, int64(30), recovered.ID)
}

func TestRecoverDuplicateByOperationIDTimeoutAndErrorBoundaries(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		source := &Account{ID: 49, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		repo := &duplicateAccountDelayedRecoveryRepoStub{
			duplicateAccountRepoStub: newDuplicateAccountRepoStub(source),
			revealAfter:              100,
		}
		svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()

		recovered, err := svc.recoverDuplicateByOperationID(ctx, "never-visible", true)

		require.Nil(t, recovered)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.GreaterOrEqual(t, repo.findCalls, 2)
	})

	t.Run("lookup error is not retried", func(t *testing.T) {
		source := &Account{ID: 59, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
		lookupErr := errors.New("database unavailable")
		repo := &duplicateAccountDelayedRecoveryRepoStub{
			duplicateAccountRepoStub: newDuplicateAccountRepoStub(source),
			revealAfter:              100,
			findErr:                  lookupErr,
		}
		svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

		recovered, err := svc.recoverDuplicateByOperationID(context.Background(), "lookup-error", true)

		require.Nil(t, recovered)
		require.ErrorIs(t, err, lookupErr)
		require.Equal(t, 1, repo.findCalls)
	})
}

func TestShouldPollDuplicateAccountRecoveryBoundaries(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unique violation", err: &pq.Error{Code: "23505"}, want: true},
		{name: "foreign key violation", err: &pq.Error{Code: "23503"}, want: false},
		{name: "request canceled", err: context.Canceled, want: true},
		{name: "uncertain connection", err: io.ErrUnexpectedEOF, want: true},
		{name: "definite validation failure", err: errors.New("credential validation failed"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldPollDuplicateAccountRecovery(tt.err))
		})
	}
}

func TestDuplicateAccountDoesNotPollForDefiniteNonConflictError(t *testing.T) {
	source := &Account{ID: 69, Name: "source", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "secret"}}
	repo := &duplicateAccountDelayedRecoveryRepoStub{
		duplicateAccountRepoStub: newDuplicateAccountRepoStub(source),
		revealAfter:              100,
		createErr:                errors.New("credential validation failed"),
	}
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

	duplicate, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:7", "definite-error")

	require.Nil(t, duplicate)
	require.ErrorContains(t, err, "credential validation failed")
	require.Equal(t, 2, repo.findCalls, "明确非冲突错误只做初始预查和一次即时恢复查询")
}

func TestDuplicateAccountRejectsRotatingCredentials(t *testing.T) {
	source := &Account{ID: 5, Name: "oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "secret"}}
	repo := newDuplicateAccountRepoStub(source)
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo}

	_, err := svc.DuplicateAccount(context.Background(), source.ID, "admin:1", "copy-oauth")

	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "ACCOUNT_DUPLICATE_CREDENTIAL_TYPE_UNSUPPORTED", infraerrors.Reason(err))
	require.Len(t, repo.accounts, 1)
}
