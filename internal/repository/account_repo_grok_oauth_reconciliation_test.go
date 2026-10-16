package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryGrokOAuthReconcileCASWritesSchedulerOutboxAtomically(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer db.Close()

	updatedAt := time.Unix(1_720_000_000, 123_000_000).UTC()
	mock.ExpectExec(`(?s)WITH updated AS \(.*UPDATE accounts AS a.*INSERT INTO scheduler_outbox`).
		WithArgs(
			service.StatusError,
			"Grok OAuth reconciliation: missing_refresh_token",
			int64(17),
			service.PlatformGrok,
			service.AccountTypeOAuth,
			service.StatusActive,
			updatedAt,
			nil,
			int64(3),
			service.SchedulerOutboxEventAccountChanged,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	applied, err := repo.SetGrokOAuthReconcileErrorIfUnchanged(context.Background(), 17, service.GrokOAuthReconcileSnapshot{
		UpdatedAt:    updatedAt,
		TokenVersion: 3,
	}, "Grok OAuth reconciliation: missing_refresh_token")

	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryFindGrokOAuthIdentityOwnersAggregatesAcrossPages(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer db.Close()

	identityKeys := []string{"sub:same-subject", "email:same@example.com"}
	sameToken := repositoryUnsignedGrokIDToken("same-subject", "same@example.com")
	mock.ExpectQuery(`(?s)SELECT id,.*credentials->>'email'.*credentials->>'id_token'.*ORDER BY id ASC`).
		WithArgs(service.PlatformGrok, service.AccountTypeOAuth, service.StatusActive).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "id_token"}).
			AddRow(int64(2), "same@example.com", sameToken).
			AddRow(int64(8), "same@example.com", sameToken).
			AddRow(int64(9), "single@example.com", repositoryUnsignedGrokIDToken("single-subject", "single@example.com")))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	owners, err := repo.FindGrokOAuthIdentityOwners(context.Background(), identityKeys)

	require.NoError(t, err)
	require.Equal(t, service.GrokOAuthIdentityOwner{CanonicalAccountID: 2, Count: 2}, owners["sub:same-subject"])
	require.Equal(t, service.GrokOAuthIdentityOwner{CanonicalAccountID: 2, Count: 2}, owners["email:same@example.com"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func repositoryUnsignedGrokIDToken(subject, email string) string {
	payload, _ := json.Marshal(map[string]string{"sub": subject, "email": email})
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
