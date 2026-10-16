package gatewaylite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func oauthInvocationRecoverySQL(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth-recovery.sqlite")
	db := oauthInvocationOpenRecoverySQL(t, path)
	migration, err := migrations.FS.ReadFile("241_gateway_lite_oauth_invocation_recovery.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return db, path
}
func oauthInvocationOpenRecoverySQL(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func oauthInvocationInstallRecovery(t *testing.T, o *OAuthInvocationRecoveryOutbox) {
	t.Helper()
	previous := DefaultOAuthInvocationRecoveryOutbox()
	SetDefaultOAuthInvocationRecoveryOutbox(o)
	t.Cleanup(func() { SetDefaultOAuthInvocationRecoveryOutbox(previous) })
}

func TestOAuthInvocationSQLRecoveryReplyLossAfterRestart(t *testing.T) {
	for _, phase := range oauthInvocationRecoveryReplyLossPhases() {
		t.Run(phase, func(t *testing.T) {
			db, path := oauthInvocationRecoverySQL(t)
			_, quota := oauthInvocationMiniQuota(t)
			runOAuthInvocationSQLRecoveryReplyLoss(t, phase, db, func() *sql.DB {
				return oauthInvocationOpenRecoverySQL(t, path)
			}, quota)
		})
	}
}

func oauthInvocationRecoveryReplyLossPhases() []string {
	return []string{"reserve", "authorize", "bind", "funding_resize", "delegation_resize", "funding_release", "delegation_release"}
}

func runOAuthInvocationSQLRecoveryReplyLoss(t *testing.T, phase string, db *sql.DB, reopen func() *sql.DB, quota *RedisQuota) {
	t.Helper()
	s, control := newOAuthInvocationTestServer(t)
	outbox := NewOAuthInvocationRecoveryOutbox(db, control)
	oauthInvocationInstallRecovery(t, outbox)
	reqCtx := oauthInvocationTestContext(t, s, "sql-lost-reply")
	remotePath := map[string]string{"reserve": "/internal/oauth/invocations/reserve", "authorize": "/internal/billing/authorize", "bind": "/internal/oauth/invocations/bind",
		"funding_resize": "/internal/billing/resize-authorization", "delegation_resize": "/internal/oauth/invocations/resize",
		"funding_release": "/internal/billing/release-authorization", "delegation_release": "/internal/oauth/invocations/release"}[phase]
	if phase == "reserve" || phase == "authorize" || phase == "bind" {
		s.failAfter[remotePath] = 1
	}
	reqCtx, err := EnsureOAuthInvocationUpperBound(reqCtx, control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
	if strings.HasSuffix(phase, "release") || strings.HasSuffix(phase, "resize") {
		require.NoError(t, err)
		s.failAfter[remotePath] = 1
		if strings.HasSuffix(phase, "release") {
			err = ReleaseOAuthInvocationRequest(reqCtx, true)
		} else {
			_, err = EnsureOAuthInvocationFundingForContext(reqCtx, 700_000, "text:model-test,output_tokens=64")
		}
	}
	require.ErrorIs(t, err, ErrOAuthInvocationUnknown)
	require.NoError(t, FinishOAuthInvocationRecovery(reqCtx))
	var payload, persistedPhase string
	var dispatch bool
	require.NoError(t, db.QueryRow(`SELECT snapshot_payload,phase,may_dispatch FROM gateway_lite_oauth_invocation_recovery`).Scan(&payload, &persistedPhase, &dispatch))
	require.Equal(t, phase, persistedPhase)
	require.False(t, dispatch)
	require.NotContains(t, payload, oauthInvocationTestToken)
	var original OAuthInvocationRequestSnapshot
	require.NoError(t, json.Unmarshal([]byte(payload), &original))
	// Reconnect to the real persisted database and construct a new production
	// consumer; the live request context is never passed into financial recovery.
	require.NoError(t, db.Close())
	reopened := reopen()
	restarted := NewOAuthInvocationRecoveryOutbox(reopened, control)
	count, err := restarted.RecoverDue(context.Background(), 4)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var state string
	require.NoError(t, reopened.QueryRow(`SELECT state,snapshot_payload FROM gateway_lite_oauth_invocation_recovery`).Scan(&state, &payload))
	require.Equal(t, "completed", state)
	var recovered OAuthInvocationRequestSnapshot
	require.NoError(t, json.Unmarshal([]byte(payload), &recovered))
	require.Equal(t, original.ReserveRequest, recovered.ReserveRequest)
	require.Equal(t, original.FundingRequest, recovered.FundingRequest)
	require.Equal(t, original.PendingBind, recovered.PendingBind)
	require.Equal(t, original.PendingFundingResize, recovered.PendingFundingResize)
	require.Equal(t, original.PendingResize, recovered.PendingResize)
	if phase == "funding_release" {
		require.Equal(t, original.PendingFundingRelease, recovered.PendingFundingRelease)
	}
	if phase == "delegation_release" {
		require.Equal(t, original.PendingRelease, recovered.PendingRelease)
	}
	// A duplicate Finish must not overwrite a row already claimed/completed by a
	// reconstructed consumer, and neither the old nor restored request may send.
	require.NoError(t, FinishOAuthInvocationRecovery(reqCtx))
	require.Error(t, MarkOAuthInvocationExecutionStarted(reqCtx))
	restored, err := RestoreOAuthInvocationRequest(context.Background(), recovered, control, quota)
	require.NoError(t, err)
	require.Error(t, MarkOAuthInvocationExecutionStarted(restored))
	count, err = restarted.RecoverDue(context.Background(), 4)
	require.NoError(t, err)
	require.Zero(t, count)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, "released", s.reservations["sql-lost-reply"].Status)
	if funding, exists := s.funding["sql-lost-reply"]; exists {
		require.Equal(t, "released", funding.Status)
	}
	require.Zero(t, s.spent)
	fundingRelease, delegationRelease := -1, -1
	for i, path := range s.calls {
		if path == "/internal/billing/release-authorization" && fundingRelease == -1 {
			fundingRelease = i
		}
		if path == "/internal/oauth/invocations/release" && delegationRelease == -1 {
			delegationRelease = i
		}
	}
	if _, exists := s.funding["sql-lost-reply"]; exists {
		require.Greater(t, delegationRelease, fundingRelease, "original funding must release before delegation")
	}
}

func TestOAuthInvocationSQLRecoveryDispatchFenceAndFailureClosed(t *testing.T) {
	t.Run("dispatch_and_release_mutually_exclusive", func(t *testing.T) {
		db, _ := oauthInvocationRecoverySQL(t)
		s, control := newOAuthInvocationTestServer(t)
		_, quota := oauthInvocationMiniQuota(t)
		oauthInvocationInstallRecovery(t, NewOAuthInvocationRecoveryOutbox(db, control))
		ctx := oauthInvocationTestAdmit(t, s, control, quota, "sql-dispatched", 500_000)
		require.NoError(t, MarkOAuthInvocationExecutionStarted(ctx))
		require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
		require.ErrorIs(t, ReleaseOAuthInvocationRequest(ctx, true), ErrOAuthInvocationUnknown)
		require.NoError(t, FinishOAuthInvocationRecovery(ctx))
		count, err := NewOAuthInvocationRecoveryOutbox(db, control).RecoverDue(context.Background(), 4)
		require.NoError(t, err)
		require.Equal(t, 1, count)
		var state string
		var fence bool
		require.NoError(t, db.QueryRow(`SELECT state,may_dispatch FROM gateway_lite_oauth_invocation_recovery`).Scan(&state, &fence))
		require.True(t, fence)
		require.Equal(t, "attention", state)
		s.mu.Lock()
		defer s.mu.Unlock()
		require.Equal(t, "active", s.funding["sql-dispatched"].Status)
		require.Equal(t, "active", s.reservations["sql-dispatched"].Status)
		require.NotContains(t, s.calls, "/internal/billing/release-authorization")
	})
	t.Run("sql_failure_before_dispatch", func(t *testing.T) {
		db, _ := oauthInvocationRecoverySQL(t)
		s, control := newOAuthInvocationTestServer(t)
		_, quota := oauthInvocationMiniQuota(t)
		oauthInvocationInstallRecovery(t, NewOAuthInvocationRecoveryOutbox(db, control))
		ctx := oauthInvocationTestAdmit(t, s, control, quota, "sql-failure", 500_000)
		require.NoError(t, db.Close())
		require.ErrorIs(t, MarkOAuthInvocationExecutionStarted(ctx), ErrOAuthInvocationUnavailable)
		r, _ := OAuthInvocationRequestFromContext(ctx)
		require.False(t, r.Snapshot().ExecutionStarted)
	})
}

func TestOAuthInvocationSQLRecoveryFencesLateLiveOwnerAndStopsWorker(t *testing.T) {
	db, _ := oauthInvocationRecoverySQL(t)
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	outbox := NewOAuthInvocationRecoveryOutbox(db, control)
	oauthInvocationInstallRecovery(t, outbox)
	ctx := oauthInvocationTestAdmit(t, s, control, quota, "late-live-owner", 500_000)
	// Lease expiration alone is not evidence. The worker's conditional claim
	// changes the owner/generation, so the late live dispatch must fail its CAS.
	_, err := db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET owner_until_millis=0`)
	require.NoError(t, err)
	item, err := outbox.claimNext(context.Background())
	require.NoError(t, err)
	require.NotNil(t, item)
	require.ErrorIs(t, MarkOAuthInvocationExecutionStarted(ctx), ErrOAuthInvocationUnavailable)
	require.NoError(t, outbox.recoverItem(context.Background(), item))
	outbox.Start(context.Background())
	stop, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, outbox.Stop(stop))
}

func TestOAuthInvocationSQLRecoveryConcurrentClaimAndMarkRelease(t *testing.T) {
	db, _ := oauthInvocationRecoverySQL(t)
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	outbox := NewOAuthInvocationRecoveryOutbox(db, control)
	oauthInvocationInstallRecovery(t, outbox)
	ctx := oauthInvocationTestAdmit(t, s, control, quota, "concurrent-mark-release", 500_000)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); results <- MarkOAuthInvocationExecutionStarted(ctx) }()
	go func() { defer wg.Done(); results <- ReleaseOAuthInvocationRequest(ctx, true) }()
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.True(t, errors.Is(err, ErrOAuthInvocationUnknown) || errors.Is(err, ErrOAuthInvocationConflict))
		}
	}
	require.Equal(t, 1, successes, "send and release cannot both pass the same mutex")
}

// Each PostgreSQL case installs the actual migration in an isolated schema. The
// reconnect closure opens a new SQL pool; cleanup never touches production rows.
func oauthInvocationRecoveryPostgreSQL(t *testing.T) (*sql.DB, func() *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("SUB2API_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("SUB2API_TEST_DATABASE_URL is required for real PostgreSQL verification")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	schema := fmt.Sprintf("oauth_recovery_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, cleanupErr := admin.ExecContext(cleanup, "DROP SCHEMA IF EXISTS "+pq.QuoteIdentifier(schema)+" CASCADE")
		require.NoError(t, cleanupErr)
		require.NoError(t, admin.Close())
	})
	open := func() *sql.DB {
		db, openErr := sql.Open("postgres", dsn)
		require.NoError(t, openErr)
		db.SetMaxOpenConns(1)
		_, openErr = db.Exec("SET search_path TO " + pq.QuoteIdentifier(schema))
		require.NoError(t, openErr)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	migration, err := migrations.FS.ReadFile("241_gateway_lite_oauth_invocation_recovery.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	return db, open
}

func oauthInvocationRecoveryRealRedis(t *testing.T) *RedisQuota {
	t.Helper()
	redisURL := strings.TrimSpace(os.Getenv("TOKENAV_TEST_REDIS_URL"))
	if redisURL == "" {
		t.Skip("TOKENAV_TEST_REDIS_URL is required for real Redis verification")
	}
	options, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(ctx).Err())
	prefix := oauthInvocationTestPrefix(t)
	t.Cleanup(func() { oauthInvocationDeletePrefix(t, client, prefix) })
	return NewRedisQuota(client, prefix)
}

func TestOAuthInvocationPostgreSQLRecoveryReplyLossAfterRestart(t *testing.T) {
	for _, phase := range oauthInvocationRecoveryReplyLossPhases() {
		t.Run(phase, func(t *testing.T) {
			quota := oauthInvocationRecoveryRealRedis(t)
			db, reopen := oauthInvocationRecoveryPostgreSQL(t)
			runOAuthInvocationSQLRecoveryReplyLoss(t, phase, db, reopen, quota)
		})
	}
}

func TestOAuthInvocationPostgreSQLRecoveryOwnershipAndWorkerRestart(t *testing.T) {
	db, reopen := oauthInvocationRecoveryPostgreSQL(t)
	quota := oauthInvocationRecoveryRealRedis(t)
	s, control := newOAuthInvocationTestServer(t)
	outbox := NewOAuthInvocationRecoveryOutbox(db, control)
	oauthInvocationInstallRecovery(t, outbox)
	requestCtx := oauthInvocationTestAdmit(t, s, control, quota, "postgres-owner-race", 500_000)
	_, err := db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET owner_until_millis=0`)
	require.NoError(t, err)
	// Two independent database pools race the exact SQL CAS, not a shared Go lock.
	competitor := NewOAuthInvocationRecoveryOutbox(reopen(), control)
	items := make(chan *oauthInvocationRecoveryItem, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for _, worker := range []*OAuthInvocationRecoveryOutbox{outbox, competitor} {
		wg.Add(1)
		go func(worker *OAuthInvocationRecoveryOutbox) {
			defer wg.Done()
			item, claimErr := worker.claimNext(context.Background())
			items <- item
			errorsCh <- claimErr
		}(worker)
	}
	wg.Wait()
	close(items)
	close(errorsCh)
	for claimErr := range errorsCh {
		require.NoError(t, claimErr)
	}
	var claimed *oauthInvocationRecoveryItem
	for item := range items {
		if item != nil {
			require.Nil(t, claimed, "one SQL generation has exactly one consumer")
			claimed = item
		}
	}
	require.NotNil(t, claimed)
	require.ErrorIs(t, MarkOAuthInvocationExecutionStarted(requestCtx), ErrOAuthInvocationUnavailable)
	require.ErrorIs(t, ReleaseOAuthInvocationRequest(requestCtx, true), ErrOAuthInvocationUnavailable)
	_, err = RecoverOAuthInvocationRequest(requestCtx)
	require.ErrorIs(t, err, ErrOAuthInvocationUnavailable)
	// Simulate a consumer dying after its claim. A reconstructed consumer fences
	// the old claim generation, so stale writes cannot alter financial facts.
	_, err = db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET claim_until_millis=0`)
	require.NoError(t, err)
	restarted := NewOAuthInvocationRecoveryOutbox(reopen(), control)
	fresh, err := restarted.claimNext(context.Background())
	require.NoError(t, err)
	require.NotNil(t, fresh)
	var snapshot OAuthInvocationRequestSnapshot
	require.NoError(t, json.Unmarshal(claimed.payload, &snapshot))
	require.ErrorIs(t, outbox.saveClaim(context.Background(), claimed, snapshot, "pending", nil), ErrOAuthInvocationConflict)
	require.NoError(t, restarted.recoverItem(context.Background(), fresh))
	var state string
	require.NoError(t, db.QueryRow(`SELECT state FROM gateway_lite_oauth_invocation_recovery`).Scan(&state))
	require.Equal(t, "completed", state)
	// Start/Stop is reentrant across completed runs, with a separate done channel.
	for i := 0; i < 2; i++ {
		restarted.Start(context.Background())
		restarted.mu.Lock()
		done := restarted.done
		restarted.mu.Unlock()
		require.NotNil(t, done)
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		require.NoError(t, restarted.Stop(stop))
		cancel()
		select {
		case <-done:
		default:
			t.Fatal("Stop returned before the actual worker exited")
		}
		restarted.mu.Lock()
		require.Nil(t, restarted.cancel)
		restarted.mu.Unlock()
	}
}

func TestOAuthInvocationPostgreSQLRecoveryDispatchFenceAfterCrash(t *testing.T) {
	db, reopen := oauthInvocationRecoveryPostgreSQL(t)
	quota := oauthInvocationRecoveryRealRedis(t)
	s, control := newOAuthInvocationTestServer(t)
	oauthInvocationInstallRecovery(t, NewOAuthInvocationRecoveryOutbox(db, control))
	requestCtx := oauthInvocationTestAdmit(t, s, control, quota, "postgres-dispatched-crash", 500_000)
	require.NoError(t, MarkOAuthInvocationExecutionStarted(requestCtx))
	// A later financial checkpoint with dispatch=false must never clear the SQL
	// fence. No Finish is called: the live process dies after it might have sent.
	request, ok := OAuthInvocationRequestFromContext(requestCtx)
	require.True(t, ok)
	request.mu.Lock()
	err := request.checkpointLocked(requestCtx, false)
	request.mu.Unlock()
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET owner_until_millis=0`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	reopened := reopen()
	restarted := NewOAuthInvocationRecoveryOutbox(reopened, control)
	restarted.Start(context.Background())
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, restarted.Stop(stop))
	})
	require.Eventually(t, func() bool {
		var state string
		queryErr := reopened.QueryRow(`SELECT state FROM gateway_lite_oauth_invocation_recovery`).Scan(&state)
		return queryErr == nil && state == "attention"
	}, 5*time.Second, 20*time.Millisecond)
	stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	require.NoError(t, restarted.Stop(stop))
	cancel()
	var fence, finished bool
	var code, payload string
	require.NoError(t, reopened.QueryRow(`SELECT may_dispatch,finished,last_error,snapshot_payload FROM gateway_lite_oauth_invocation_recovery`).Scan(&fence, &finished, &code, &payload))
	require.True(t, fence)
	require.False(t, finished, "recovery does not require a successful Finish on a dead process")
	require.Equal(t, "execution_outcome_unknown", code)
	var snapshot OAuthInvocationRequestSnapshot
	require.NoError(t, json.Unmarshal([]byte(payload), &snapshot))
	restored, err := RestoreOAuthInvocationRequest(context.Background(), snapshot, control, quota)
	require.NoError(t, err)
	require.Error(t, MarkOAuthInvocationExecutionStarted(restored))
	require.Error(t, MarkOAuthInvocationExecutionStarted(requestCtx))
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, "active", s.reservations["postgres-dispatched-crash"].Status)
	require.Equal(t, "active", s.funding["postgres-dispatched-crash"].Status)
	require.NotContains(t, s.calls, "/internal/billing/release-authorization")
	require.NotContains(t, s.calls, "/internal/oauth/invocations/release")
	require.Zero(t, s.spent)
}

func TestOAuthInvocationSQLRecoveryDispatchCheckpointReplyLoss(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			db, _ := oauthInvocationRecoverySQL(t)
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			outbox := NewOAuthInvocationRecoveryOutbox(db, control)
			oauthInvocationInstallRecovery(t, outbox)
			ctx := oauthInvocationTestAdmit(t, s, control, quota, "dispatch-checkpoint-lost", 500_000)
			request, _ := OAuthInvocationRequestFromContext(ctx)
			// Simulate a driver returning an error after an UPDATE may have committed:
			// the live caller keeps its old generation, exactly as checkpoint does.
			request.mu.Lock()
			request.snapshot.DispatchUncertain = true
			if committed {
				_, err := db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET may_dispatch=TRUE,generation=generation+1`)
				require.NoError(t, err)
			}
			request.blocked, request.ready = true, false
			request.mu.Unlock()
			s.mu.Lock()
			before := len(s.calls)
			s.mu.Unlock()
			require.Error(t, ReleaseOAuthInvocationRequest(ctx, true))
			require.Error(t, MarkOAuthInvocationExecutionStarted(ctx))
			s.mu.Lock()
			require.Len(t, s.calls, before, "a live uncertain dispatch must not release")
			s.mu.Unlock()
			if committed {
				require.ErrorIs(t, FinishOAuthInvocationRecovery(ctx), ErrOAuthInvocationUnavailable)
				_, err := db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET owner_until_millis=0`)
				require.NoError(t, err)
			} else {
				require.NoError(t, FinishOAuthInvocationRecovery(ctx))
			}
			count, err := outbox.RecoverDue(context.Background(), 4)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			var state string
			require.NoError(t, db.QueryRow(`SELECT state FROM gateway_lite_oauth_invocation_recovery`).Scan(&state))
			s.mu.Lock()
			defer s.mu.Unlock()
			if committed {
				require.Equal(t, "attention", state)
				require.Equal(t, "active", s.funding["dispatch-checkpoint-lost"].Status)
			} else {
				require.Equal(t, "completed", state)
				require.Equal(t, "released", s.funding["dispatch-checkpoint-lost"].Status)
			}
		})
	}
}

func TestOAuthInvocationSQLRecoveryDoesNotReplayKnownClosedReserve(t *testing.T) {
	db, _ := oauthInvocationRecoverySQL(t)
	s, control := newOAuthInvocationTestServer(t)
	_, quota := oauthInvocationMiniQuota(t)
	outbox := NewOAuthInvocationRecoveryOutbox(db, control)
	oauthInvocationInstallRecovery(t, outbox)
	s.deny["/internal/oauth/invocations/reserve"] = "scope_denied"
	ctx, err := EnsureOAuthInvocationUpperBound(oauthInvocationTestContext(t, s, "denied-reserve"), control, quota, 500_000, "text:model-test,output_tokens=32", "model-test", "/v1/responses")
	require.ErrorIs(t, err, ErrOAuthInvocationDenied)
	require.NoError(t, ReleaseOAuthInvocationRequest(ctx, true))
	require.NoError(t, FinishOAuthInvocationRecovery(ctx))
	s.mu.Lock()
	before := len(s.calls)
	delete(s.deny, "/internal/oauth/invocations/reserve")
	s.mu.Unlock()
	count, err := outbox.RecoverDue(context.Background(), 4)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Len(t, s.calls, before, "closed denial must not create a hold during recovery")
	require.Empty(t, s.reservations)
	require.Empty(t, s.funding)
}

func TestOAuthInvocationSQLRecoveryRejectsCorruptOrMisplacedSnapshot(t *testing.T) {
	for _, corruption := range []string{"digest", "row_identity"} {
		t.Run(corruption, func(t *testing.T) {
			db, _ := oauthInvocationRecoverySQL(t)
			s, control := newOAuthInvocationTestServer(t)
			_, quota := oauthInvocationMiniQuota(t)
			outbox := NewOAuthInvocationRecoveryOutbox(db, control)
			oauthInvocationInstallRecovery(t, outbox)
			requestCtx := oauthInvocationTestAdmit(t, s, control, quota, "invalid-snapshot", 500_000)
			require.NoError(t, FinishOAuthInvocationRecovery(requestCtx))
			if corruption == "digest" {
				_, err := db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET snapshot_sha256='wrong'`)
				require.NoError(t, err)
			} else {
				_, err := db.Exec(`UPDATE gateway_lite_oauth_invocation_recovery SET request_id='other-row'`)
				require.NoError(t, err)
			}
			s.mu.Lock()
			callsBefore := len(s.calls)
			s.mu.Unlock()
			count, err := outbox.RecoverDue(context.Background(), 4)
			require.NoError(t, err)
			require.Equal(t, 1, count)
			var state, code string
			require.NoError(t, db.QueryRow(`SELECT state,last_error FROM gateway_lite_oauth_invocation_recovery`).Scan(&state, &code))
			require.Equal(t, "attention", state)
			require.Equal(t, "invalid_recovery_snapshot", code)
			s.mu.Lock()
			defer s.mu.Unlock()
			require.Len(t, s.calls, callsBefore, "invalid evidence cannot mutate remote finances")
			require.Equal(t, "active", s.funding["invalid-snapshot"].Status)
		})
	}
}
