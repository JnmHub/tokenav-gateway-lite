package repository

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepository_SetTempUnschedulable_NoRowsAffectedDoesNotWriteOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	until := time.Now().Add(10 * time.Minute)

	err := repo.SetTempUnschedulable(context.Background(), 42, until, "retry")
	require.NoError(t, err)
	require.Len(t, exec.execQueries, 1)
	require.Contains(t, exec.execQueries[0], "UPDATE accounts")
	require.NotContains(t, strings.Join(exec.execQueries, "\n"), "scheduler_outbox")
}

func TestAccountRepository_ClearGrokSchedulingThresholdPauseIfMatch_UsesReasonUntilAndAtomicOutbox(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	until := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	reason := `{"source":"grok_scheduling_threshold","until_unix":123}`

	cleared, err := repo.ClearGrokSchedulingThresholdPauseIfMatch(context.Background(), 42, until, reason)
	require.NoError(t, err)
	require.True(t, cleared)
	require.Len(t, exec.execQueries, 1)
	normalized := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, normalized, "WITH updated AS")
	require.Contains(t, normalized, "temp_unschedulable_until = $2")
	require.Contains(t, normalized, "temp_unschedulable_reason = $3")
	require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	require.Equal(t, []any{int64(42), until, reason, service.SchedulerOutboxEventAccountChanged}, exec.execArgs[0])
}

func TestAccountRepository_GrokCredentialCASMutationsAreAtomicWithOutbox(t *testing.T) {
	updatedAt := time.Now().Add(-time.Minute).UTC()
	snapshot := service.GrokCredentialMutationSnapshot{UpdatedAt: updatedAt}

	t.Run("permanent failure", func(t *testing.T) {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
		repo := newAccountRepositoryWithSQL(nil, exec, nil)
		updated, err := repo.SetGrokCredentialErrorIfMatch(context.Background(), 41, snapshot, string(service.GrokCredentialReasonRevoked))
		require.NoError(t, err)
		require.True(t, updated)
		require.Len(t, exec.execQueries, 1)
		normalized := normalizeSQLWhitespace(exec.execQueries[0])
		require.Contains(t, normalized, "WITH updated AS")
		require.Contains(t, normalized, "a.updated_at = $7")
		require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	})

	t.Run("temporary failure", func(t *testing.T) {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
		repo := newAccountRepositoryWithSQL(nil, exec, nil)
		updated, err := repo.SetGrokCredentialTempUnschedulableIfMatch(context.Background(), 42, snapshot, time.Now().Add(10*time.Minute), string(service.GrokCredentialReasonRefreshTransient))
		require.NoError(t, err)
		require.True(t, updated)
		normalized := normalizeSQLWhitespace(exec.execQueries[0])
		require.Contains(t, normalized, "a.updated_at = $7")
		require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	})

	t.Run("refresh success", func(t *testing.T) {
		exec := &recordingSQLExecutor{result: rowsAffectedResult(1)}
		repo := newAccountRepositoryWithSQL(nil, exec, nil)
		updated, err := repo.UpdateGrokOAuthCredentialsIfUnchanged(context.Background(), 43, 1, nil, map[string]any{"access_token": "new", "_token_version": int64(2)})
		require.NoError(t, err)
		require.True(t, updated)
		normalized := normalizeSQLWhitespace(exec.execQueries[0])
		require.Contains(t, normalized, "a.credentials->>'_token_version'")
		require.Contains(t, normalized, "= $5::text")
		require.NotContains(t, normalized, "a.updated_at = $5")
		require.Contains(t, normalized, "INSERT INTO scheduler_outbox")
	})
}

func TestAccountRepository_ListOAuthRefreshCandidates_SQLFilter(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var capturedSQL string
	mock.ExpectQuery("SELECT id").
		WillReturnRows(sqlmock.NewRows([]string{"id"})).
		WillDelayFor(0)

	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	accounts, err := repo.ListOAuthRefreshCandidates(context.Background())
	require.NoError(t, err)
	require.Empty(t, accounts)

	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "status = 'active'")
	require.Contains(t, normalized, "schedulable = TRUE")
	require.Contains(t, normalized, "type IN ('oauth', 'setup-token')")
	require.Contains(t, normalized, "platform IN ('anthropic', 'openai', 'gemini', 'antigravity')")
	require.Contains(t, normalized, "credentials ? 'refresh_token'")
	require.Contains(t, normalized, "btrim(credentials->>'refresh_token') <> ''")
	require.Contains(t, normalized, "temp_unschedulable_until > NOW()")
	require.Contains(t, normalized, "temp_unschedulable_reason LIKE 'token refresh retry exhausted:%'")
	require.Contains(t, normalized, "IS NOT TRUE",
		"must use IS NOT TRUE so accounts with NULL temp_unschedulable_until are not silently excluded by PG 3-valued logic")
	require.NotContains(t, normalized, "AND NOT (",
		"plain NOT (...) excludes NULL temp_unschedulable_until rows (the common healthy case)")
	require.Contains(t, normalized, "ORDER BY priority ASC, id ASC")
	require.NotContains(t, normalized, "credentials->>'expires_at'")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepository_ListOAuthRefreshCandidatePage_SQLFilter(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	var capturedSQL string
	mock.ExpectQuery("SELECT id").
		WithArgs(sqlmock.AnyArg(), int64(17), 200).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	repo := newAccountRepositoryWithSQL(nil, captureQuerySQL{db: db, captured: &capturedSQL}, nil)

	page, err := repo.ListOAuthRefreshCandidatePage(context.Background(), service.OAuthRefreshPageOptions{
		Platforms:            []string{"openai", "grok"},
		AfterID:              17,
		Limit:                200,
		ActiveOnly:           true,
		IncludeSetupToken:    true,
		RequireRefreshToken:  true,
		ExcludeRetryCooldown: true,
	})
	require.NoError(t, err)
	require.NotNil(t, page)
	require.Empty(t, page.Accounts)
	require.Zero(t, page.NextAfterID)
	require.False(t, page.HasMore)

	normalized := normalizeSQLWhitespace(capturedSQL)
	require.Contains(t, normalized, "deleted_at IS NULL")
	require.Contains(t, normalized, "platform = ANY($1)")
	require.Contains(t, normalized, "schedulable = TRUE")
	require.Contains(t, normalized, "id > $2")
	require.Contains(t, normalized, "status = 'active'")
	require.Contains(t, normalized, "type IN ('oauth', 'setup-token')")
	require.Contains(t, normalized, "credentials ? 'refresh_token'")
	require.Contains(t, normalized, "IS NOT TRUE")
	require.Contains(t, normalized, "ORDER BY id ASC LIMIT $3")
	require.NotContains(t, normalized, "OFFSET")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepository_ListOAuthRefreshCandidatePage_ValidatesBounds(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, &recordingSQLExecutor{}, nil)

	_, err := repo.ListOAuthRefreshCandidatePage(context.Background(), service.OAuthRefreshPageOptions{Limit: 10})
	require.ErrorContains(t, err, "platforms cannot be empty")

	_, err = repo.ListOAuthRefreshCandidatePage(context.Background(), service.OAuthRefreshPageOptions{
		Platforms: []string{"openai"},
		Limit:     1001,
	})
	require.ErrorContains(t, err, "limit must be between 1 and 1000")
}

type captureQuerySQL struct {
	db       *sql.DB
	captured *string
}

func (c captureQuerySQL) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, query, args...)
}

func (c captureQuerySQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if c.captured != nil {
		*c.captured = query
	}
	return c.db.QueryContext(ctx, query, args...)
}

func normalizeSQLWhitespace(sql string) string {
	return strings.Join(regexp.MustCompile(`\s+`).Split(strings.TrimSpace(sql), -1), " ")
}

type rowsAffectedResult int64

func (r rowsAffectedResult) LastInsertId() (int64, error) { return 0, nil }
func (r rowsAffectedResult) RowsAffected() (int64, error) { return int64(r), nil }

type recordingSQLExecutor struct {
	result      sql.Result
	err         error
	execQueries []string
	execArgs    [][]any
}

func (e *recordingSQLExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.execQueries = append(e.execQueries, query)
	e.execArgs = append(e.execArgs, append([]any(nil), args...))
	if e.err != nil {
		return nil, e.err
	}
	return e.result, nil
}

func (e *recordingSQLExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}
