package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newGrokVideoTaskStoreMock(t *testing.T) (*grokVideoTaskStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &grokVideoTaskStore{db: db}, mock
}

func testGrokVideoTask() *service.GrokVideoTask {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	next := now.Add(service.GrokVideoTaskPollInterval)
	return &service.GrokVideoTask{
		RequestID:       "request/with:unsafe?chars",
		APIKeyID:        2,
		UserID:          3,
		AccountID:       4,
		RequestedModel:  "grok-imagine-video",
		BillingModel:    "grok-imagine-video",
		UpstreamModel:   "grok-imagine-video",
		Resolution:      "720p",
		DurationSeconds: 8,
		VideoCount:      1,
		State:           service.GrokVideoTaskStatePending,
		CreatedAt:       now,
		UpdatedAt:       now,
		NextPollAt:      &next,
	}
}

func TestGrokVideoTaskStoreSaveAndGet(t *testing.T) {
	store, mock := newGrokVideoTaskStoreMock(t)
	task := testGrokVideoTask()

	mock.ExpectExec("INSERT INTO grok_video_tasks").
		WithArgs(
			task.RequestID,
			task.State,
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			task.CreatedAt,
			sqlmock.AnyArg(),
			task.CompletedAt,
			task.BilledAt,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	require.NoError(t, store.Save(context.Background(), task))

	payload, err := json.Marshal(task)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT task_data").
		WithArgs(task.RequestID).
		WillReturnRows(sqlmock.NewRows([]string{"task_data"}).AddRow(payload))
	got, err := store.Get(context.Background(), task.RequestID)
	require.NoError(t, err)
	require.Equal(t, task.RequestID, got.RequestID)
	require.Equal(t, task.AccountID, got.AccountID)
	require.Equal(t, 8, got.DurationSeconds)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGrokVideoTaskStoreListDueAndLocks(t *testing.T) {
	store, mock := newGrokVideoTaskStoreMock(t)
	task := testGrokVideoTask()
	payload, err := json.Marshal(task)
	require.NoError(t, err)
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT task_data[\\s\\S]+state IN").
		WithArgs(now, 25).
		WillReturnRows(sqlmock.NewRows([]string{"task_data"}).AddRow(payload))
	tasks, err := store.ListDue(context.Background(), now, 25)
	require.NoError(t, err)
	require.Len(t, tasks, 1)

	mock.ExpectQuery("UPDATE grok_video_tasks[\\s\\S]+poll_locked_until[\\s\\S]+RETURNING request_id").
		WithArgs(task.RequestID, int64((30 * time.Second).Milliseconds())).
		WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow(task.RequestID))
	locked, err := store.TryLockPolling(context.Background(), task.RequestID, 30*time.Second)
	require.NoError(t, err)
	require.True(t, locked)

	mock.ExpectQuery("UPDATE grok_video_tasks[\\s\\S]+settlement_locked_until[\\s\\S]+RETURNING request_id").
		WithArgs(task.RequestID, int64(time.Minute.Milliseconds())).
		WillReturnRows(sqlmock.NewRows([]string{"request_id"}))
	locked, err = store.TryLockSettlement(context.Background(), task.RequestID, time.Minute)
	require.NoError(t, err)
	require.False(t, locked)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE grok_video_tasks SET poll_locked_until = NULL WHERE request_id = $1")).
		WithArgs(task.RequestID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, store.UnlockPolling(context.Background(), task.RequestID))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGrokVideoTaskStoreReturnsNotFoundAndCleansFinalized(t *testing.T) {
	store, mock := newGrokVideoTaskStoreMock(t)
	mock.ExpectQuery("SELECT task_data").
		WithArgs("missing").
		WillReturnRows(sqlmock.NewRows([]string{"task_data"}))
	_, err := store.Get(context.Background(), "missing")
	require.ErrorIs(t, err, service.ErrGrokVideoTaskNotFound)

	before := time.Now().UTC().Add(-service.GrokVideoTaskFinalRetention)
	mock.ExpectExec("DELETE FROM grok_video_tasks").
		WithArgs(before).
		WillReturnResult(sqlmock.NewResult(0, 3))
	removed, err := store.CleanupFinalized(context.Background(), before)
	require.NoError(t, err)
	require.Equal(t, int64(3), removed)
	require.NoError(t, mock.ExpectationsWereMet())
}
