package gatewaylite

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func newPenaltyControlPlaneRef(t *testing.T, status int, body string) *ControlPlaneClientRef {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := NewControlPlaneClient(server.URL, "", time.Second)
	require.NoError(t, err)
	return NewControlPlaneClientRef(client)
}

func penaltyRequest(requestID string) ContentModerationPenaltyRequest {
	return ContentModerationPenaltyRequest{
		RequestID: requestID,
		UserID:    42,
		KeyID:     "key-enterprise",
		Action:    "keyword_block",
	}
}

func TestDurableContentModerationPenaltySuccessfulHotPathDoesNotTouchDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableContentModerationPenaltyReporter(db, newPenaltyControlPlaneRef(t, http.StatusOK, `{"ok":true}`))

	require.NoError(t, reporter.Report(context.Background(), penaltyRequest("penalty-success")))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyPersistsTransientFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableContentModerationPenaltyReporter(db, newPenaltyControlPlaneRef(t, http.StatusServiceUnavailable, `{"ok":false,"error":"temporarily_unavailable"}`))
	req := penaltyRequest("penalty-transient")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_content_moderation_penalty_outbox")).
		WithArgs(req.RequestID, sqlmock.AnyArg(), "pending", "control plane /internal/content-moderation/penalty returned 503: temporarily_unavailable", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	require.NoError(t, reporter.Report(context.Background(), req))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyMarksPermanentFailureDead(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableContentModerationPenaltyReporter(db, newPenaltyControlPlaneRef(t, http.StatusForbidden, `{"ok":false,"error":"content_moderation_subject_mismatch"}`))
	req := penaltyRequest("penalty-permanent")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_content_moderation_penalty_outbox")).
		WithArgs(req.RequestID, sqlmock.AnyArg(), "dead", "control plane /internal/content-moderation/penalty returned 403: content_moderation_subject_mismatch", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	require.ErrorContains(t, reporter.Report(context.Background(), req), "returned 403")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyPersistIsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableContentModerationPenaltyReporter(db, nil)
	req := penaltyRequest("penalty-idempotent")
	for range 2 {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_content_moderation_penalty_outbox")).
			WithArgs(req.RequestID, sqlmock.AnyArg(), "pending", "context deadline exceeded", nil).
			WillReturnResult(sqlmock.NewResult(0, 1))
		require.NoError(t, reporter.persist(req, context.DeadlineExceeded, true))
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyPersistRejectsPayloadConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableContentModerationPenaltyReporter(db, nil)
	req := penaltyRequest("penalty-conflict")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO gateway_lite_content_moderation_penalty_outbox")).
		WithArgs(req.RequestID, sqlmock.AnyArg(), "pending", "context deadline exceeded", nil).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = reporter.persist(req, context.DeadlineExceeded, true)
	require.ErrorIs(t, err, errContentModerationPenaltyPayloadConflict)
	require.ErrorContains(t, err, req.RequestID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyRetrySuccessDeletesClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	reporter := NewDurableContentModerationPenaltyReporter(db, newPenaltyControlPlaneRef(t, http.StatusOK, `{"ok":true}`))
	req := penaltyRequest("penalty-retry-success")
	payload := []byte(`{"request_id":"penalty-retry-success","user_id":42,"key_id":"key-enterprise","action":"keyword_block"}`)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM gateway_lite_content_moderation_penalty_outbox")).
		WithArgs(req.RequestID, "claim-success").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, reporter.retryItem(context.Background(), contentModerationPenaltyOutboxItem{
		requestID: req.RequestID, payload: payload, claimToken: "claim-success",
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyRetryClassifiesFailures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantDead   bool
		wantStatus string
	}{
		{name: "server failure remains pending", status: http.StatusBadGateway, body: `{"ok":false,"error":"upstream_failed"}`, wantStatus: "returned 502"},
		{name: "validation failure becomes dead", status: http.StatusBadRequest, body: `{"ok":false,"error":"invalid_content_moderation_penalty"}`, wantDead: true, wantStatus: "returned 400"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			reporter := NewDurableContentModerationPenaltyReporter(db, newPenaltyControlPlaneRef(t, tt.status, tt.body))
			item := contentModerationPenaltyOutboxItem{
				requestID: "penalty-retry-classify",
				payload:   []byte(`{"request_id":"penalty-retry-classify","user_id":42,"key_id":"key-enterprise","action":"keyword_block"}`),
				attempts:  2, claimToken: "claim-classify",
			}
			if tt.wantDead {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE gateway_lite_content_moderation_penalty_outbox")).
					WithArgs(item.requestID, item.claimToken, sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE gateway_lite_content_moderation_penalty_outbox")).
					WithArgs(item.requestID, item.claimToken, contentModerationPenaltyRetryDelay(3).Milliseconds(), sqlmock.AnyArg()).
					WillReturnResult(sqlmock.NewResult(0, 1))
			}
			require.ErrorContains(t, reporter.retryItem(context.Background(), item), tt.wantStatus)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDurableContentModerationPenaltyClaimsAreConcurrentSafe(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2)
	mock.MatchExpectationsInOrder(false)
	claimQuery := "(?s)WITH due AS.*status = 'pending'.*FOR UPDATE SKIP LOCKED.*UPDATE gateway_lite_content_moderation_penalty_outbox"
	for _, requestID := range []string{"claim-a", "claim-b"} {
		mock.ExpectQuery(claimQuery).
			WithArgs(contentModerationPenaltyClaimTTL.Milliseconds(), sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"request_id", "event_payload", "attempts"}).
				AddRow(requestID, []byte(`{}`), 0))
	}
	reporter := NewDurableContentModerationPenaltyReporter(db, nil)
	items := make(chan contentModerationPenaltyOutboxItem, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			item, ok, claimErr := reporter.claimNext(context.Background())
			if claimErr != nil {
				errors <- claimErr
				return
			}
			if !ok {
				errors <- context.Canceled
				return
			}
			items <- item
		}()
	}
	workers.Wait()
	close(items)
	close(errors)
	for claimErr := range errors {
		require.NoError(t, claimErr)
	}
	seenTokens := map[string]struct{}{}
	for item := range items {
		require.NotEmpty(t, item.claimToken)
		seenTokens[item.claimToken] = struct{}{}
	}
	require.Len(t, seenTokens, 2)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableContentModerationPenaltyWorkerStopsWithApplication(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("(?s)WITH due AS.*FOR UPDATE SKIP LOCKED").
		WithArgs(contentModerationPenaltyClaimTTL.Milliseconds(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"request_id", "event_payload", "attempts"}))
	reporter := NewDurableContentModerationPenaltyReporter(db, NewControlPlaneClientRef(nil))
	ctx, cancel := context.WithCancel(context.Background())
	reporter.StartRetryWorker(ctx)
	require.Eventually(t, func() bool { return mock.ExpectationsWereMet() == nil }, time.Second, 10*time.Millisecond)
	cancel()
	done := make(chan struct{})
	go func() {
		reporter.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("penalty retry worker did not stop")
	}
}
