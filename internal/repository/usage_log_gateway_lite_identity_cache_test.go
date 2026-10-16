package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestResolveGatewayLiteUsageIdentityCoalescesConcurrentUpserts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const remoteUserID int64 = 42
	const remoteKeyID = "remote-key-42"
	mock.ExpectQuery(`(?s)WITH upsert_user AS.*SELECT user_id, id FROM upsert_key`).
		WithArgs(
			gatewayLiteShadowUserEmail(remoteUserID),
			"",
			"",
			gatewayLiteShadowPasswordHash,
			gatewayLiteShadowAPIKeyValue(remoteUserID, remoteKeyID),
			gatewayLiteShadowAPIKeyName(remoteKeyID),
		).
		WillDelayFor(25 * time.Millisecond).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "id"}).AddRow(int64(420), int64(700)))

	repo := newUsageLogRepositoryWithSQL(nil, db)
	const callers = 32
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			userID, apiKeyID, resolveErr := repo.ResolveGatewayLiteUsageIdentity(context.Background(), remoteUserID, remoteKeyID)
			if resolveErr == nil && (userID != 420 || apiKeyID != 700) {
				resolveErr = &unexpectedGatewayLiteIdentityError{userID: userID, apiKeyID: apiKeyID}
			}
			errs <- resolveErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for resolveErr := range errs {
		require.NoError(t, resolveErr)
	}

	// 首轮完成后再读一次，确认 TTL cache（短时缓存）也不会再次执行 UPSERT。
	userID, apiKeyID, err := repo.ResolveGatewayLiteUsageIdentity(context.Background(), remoteUserID, remoteKeyID)
	require.NoError(t, err)
	require.EqualValues(t, 420, userID)
	require.EqualValues(t, 700, apiKeyID)
	require.NoError(t, mock.ExpectationsWereMet())
}

type unexpectedGatewayLiteIdentityError struct {
	userID   int64
	apiKeyID int64
}

func (e *unexpectedGatewayLiteIdentityError) Error() string {
	return "unexpected gateway-lite usage identity"
}
