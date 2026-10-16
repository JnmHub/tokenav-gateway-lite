package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGrokCustomVoiceStoreGetAndOwnerList(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer db.Close()
	store := &grokCustomVoiceStore{db: db}
	now := time.Now().UTC()
	columns := []string{"voice_id", "user_id", "api_key_id", "gateway_key_id", "group_id", "account_id", "created_at", "updated_at"}

	mock.ExpectQuery(regexp.QuoteMeta("FROM grok_custom_voice_bindings")).
		WithArgs("voice123").
		WillReturnRows(sqlmock.NewRows(columns).AddRow("voice123", int64(7), int64(11), "", int64(5), int64(23), now, now))
	binding, err := store.Get(context.Background(), "voice123")
	require.NoError(t, err)
	require.Equal(t, int64(23), binding.AccountID)

	mock.ExpectQuery(regexp.QuoteMeta("FROM grok_custom_voice_bindings")).
		WithArgs(int64(7), int64(11), "").
		WillReturnRows(sqlmock.NewRows(columns).AddRow("voice123", int64(7), int64(11), "", int64(5), int64(23), now, now))
	bindings, err := store.ListForOwner(context.Background(), 7, 11, "")
	require.NoError(t, err)
	require.Len(t, bindings, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGrokCustomVoiceStoreRequiresOneKeyOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	store := &grokCustomVoiceStore{db: db}

	err = store.Save(context.Background(), &service.GrokCustomVoiceBinding{VoiceID: "voice123", UserID: 7, AccountID: 23})
	require.ErrorContains(t, err, "incomplete")
	require.NoError(t, mock.ExpectationsWereMet())
}
