package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func aiTestSQL(t *testing.T) (*SQLContentModerationAIStore, *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PROMPT_AUDIT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("PROMPT_AUDIT_TEST_POSTGRES_DSN missing: database integration not executed")
	}
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })
	require.NoError(t, admin.Ping())
	// 仅在随机临时库里建表和写处罚事件，禁止复用 DSN 指向的业务库或共享 schema。
	database := "cm_ai_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pq.QuoteIdentifier(database)
	_, err = admin.Exec(`CREATE DATABASE ` + quoted)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(`DROP DATABASE ` + quoted + ` WITH (FORCE)`)
		if err != nil {
			t.Errorf("remove isolated test database %s: %v", database, err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		u.Path, u.RawPath = "/"+database, ""
		query := u.Query()
		query.Del("dbname")
		query.Del("database")
		query.Del("search_path")
		u.RawQuery = query.Encode()
		dsn = u.String()
	} else {
		dsn += " dbname=" + database + " search_path=public"
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(12)
	require.NoError(t, db.Ping())
	var connected string
	require.NoError(t, db.QueryRow(`SELECT current_database()`).Scan(&connected))
	require.Equal(t, database, connected, "must verify database isolation before creating any tables")
	store := NewSQLContentModerationAIStore(db)
	require.NoError(t, store.EnsureSchema(context.Background()))
	return store, db
}

func TestContentModerationAISQLServiceOutboxSurvivesControlAndDatabaseFailure(t *testing.T) {
	store, db := aiTestSQL(t)
	server, _, business := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer server.Close()
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	cfg.AI.PenaltiesEnabled = true
	s := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	s.SetAIStore(store)
	in := aiTestInput(`{"input":"forbidden synthetic"}`)
	in.ControlPlaneKeyID, in.BillingSubjectType, in.BillingSubjectID = "stable-key", "user", in.UserID
	d := s.checkAI(context.Background(), in, cfg, false)
	require.Equal(t, "block", d.Verdict)
	require.Equal(t, "pending", d.PenaltyState)
	expectedID := ContentModerationAdmissionTurnID(in.AdmissionID, in.Turn)

	var calls atomic.Int64
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/content-moderation/penalty", r.URL.Path)
		var request gatewaylite.ContentModerationPenaltyRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, expectedID, request.RequestID)
		require.EqualValues(t, 7, request.UserID)
		require.Equal(t, "user", request.BillingSubjectType)
		require.EqualValues(t, 7, request.BillingSubjectID)
		if calls.Add(1) == 1 {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"ok":false,"error":"synthetic_unavailable"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer control.Close()
	client, err := gatewaylite.NewControlPlaneClient(control.URL, "", time.Second)
	require.NoError(t, err)
	previous := gatewaylite.DefaultControlPlaneClientRef()
	gatewaylite.SetDefaultControlPlaneClientRef(gatewaylite.NewControlPlaneClientRef(client))
	defer gatewaylite.SetDefaultControlPlaneClientRef(previous)
	s.DeliverAIPenaltyOutbox(context.Background())
	require.EqualValues(t, 1, calls.Load())
	stats, err := store.Stats(context.Background(), "standalone", time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.PenaltyPending)

	// 重启后数据库不可用时保留事件；恢复后仍重放同一服务端请求身份。
	restarted := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	restarted.SetAIStore(NewSQLContentModerationAIStore(nil))
	restarted.DeliverAIPenaltyOutbox(context.Background())
	require.EqualValues(t, 1, calls.Load())
	restarted.SetAIStore(NewSQLContentModerationAIStore(db))
	_, err = db.Exec(`UPDATE content_moderation_ai_penalty_outbox SET next_retry_at=NOW()-INTERVAL '1 second' WHERE request_id=$1`, expectedID)
	require.NoError(t, err)
	restarted.DeliverAIPenaltyOutbox(context.Background())
	require.EqualValues(t, 2, calls.Load())
	stats, err = store.Stats(context.Background(), "standalone", time.Now())
	require.NoError(t, err)
	require.Zero(t, stats.PenaltyPending)
	require.Zero(t, stats.PenaltyDead)

	// 同一轮可重新审查，但已送达标记阻止重复处罚。
	d = restarted.checkAI(context.Background(), in, cfg, false)
	require.Equal(t, "block", d.Verdict)
	restarted.DeliverAIPenaltyOutbox(context.Background())
	require.EqualValues(t, 2, calls.Load())
	require.Zero(t, business.Load())
}

func TestContentModerationAISQLDurableOutboxConflictClaimRecoveryAndReplay(t *testing.T) {
	store, db := aiTestSQL(t)
	ctx := context.Background()
	now := time.Now()
	req := gatewaylite.ContentModerationPenaltyRequest{RequestID: ContentModerationAdmissionTurnID("server-a", 1), UserID: 7, KeyID: "stable-key", Action: "block", DeductBalanceCents: 5, CreatedAtMillis: now.UnixMilli()}
	require.NoError(t, store.EnqueuePenalty(ctx, req))
	req.CreatedAtMillis++
	require.NoError(t, store.EnqueuePenalty(ctx, req))
	conflict := req
	conflict.UserID = 8
	require.Error(t, store.EnqueuePenalty(ctx, conflict))
	conflict = req
	conflict.DeductBalanceCents++
	require.Error(t, store.EnqueuePenalty(ctx, conflict))
	conflict = req
	conflict.KeyID = "another-key"
	require.Error(t, store.EnqueuePenalty(ctx, conflict))
	conflict = req
	conflict.OrganizationID = 9
	require.Error(t, store.EnqueuePenalty(ctx, conflict))
	item, err := store.ClaimPenalty(ctx, now.Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, item)
	second, err := store.ClaimPenalty(ctx, now.Add(time.Second))
	require.NoError(t, err)
	require.Nil(t, second)
	// 旧认领超时后只允许新令牌回执生效，防止已崩溃进程误完成另一进程的工作。
	restarted := NewSQLContentModerationAIStore(db)
	replay, err := restarted.ClaimPenalty(ctx, now.Add(40*time.Second))
	require.NoError(t, err)
	require.NotNil(t, replay)
	require.Equal(t, item.Request.RequestID, replay.Request.RequestID)
	require.NotEqual(t, item.Token, replay.Token)
	require.NoError(t, store.FinishPenalty(ctx, *item, nil, false))
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM content_moderation_ai_penalty_outbox WHERE request_id=$1`, req.RequestID).Scan(&status))
	require.Equal(t, "pending", status)
	require.NoError(t, restarted.FinishPenalty(ctx, *replay, fmt.Errorf("synthetic control plane unavailable"), true))
	stats, err := store.Stats(ctx, "n", now)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.PenaltyPending)
	due, err := store.ClaimPenalty(ctx, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, due)
	require.NoError(t, store.FinishPenalty(ctx, *due, nil, false))
	require.NoError(t, store.EnqueuePenalty(ctx, req))
	due, err = store.ClaimPenalty(ctx, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.Nil(t, due, "delivered event replay must not send twice")
}

func TestContentModerationAISQLConcurrentClaimsDoNotDuplicatePenalty(t *testing.T) {
	store, _ := aiTestSQL(t)
	ctx := context.Background()
	const events = 12
	for i := 0; i < events; i++ {
		req := gatewaylite.ContentModerationPenaltyRequest{RequestID: ContentModerationAdmissionTurnID(fmt.Sprintf("server-%d", i), 1), UserID: 7, KeyID: "stable-key", Action: "block"}
		require.NoError(t, store.EnqueuePenalty(ctx, req))
	}
	type claim struct {
		event *ContentModerationAIPenaltyEvent
		err   error
	}
	claims := make(chan claim, 2*events)
	var wg sync.WaitGroup
	for i := 0; i < 2*events; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event, err := store.ClaimPenalty(ctx, time.Now().Add(time.Second))
			claims <- claim{event, err}
		}()
	}
	wg.Wait()
	close(claims)
	seen := map[string]bool{}
	for got := range claims {
		require.NoError(t, got.err)
		if got.event == nil {
			continue
		}
		id := got.event.Request.RequestID
		require.False(t, seen[id], "one durable event must have only one active claimant")
		seen[id] = true
		require.NoError(t, store.FinishPenalty(ctx, *got.event, nil, false))
	}
	require.Len(t, seen, events)
	stats, err := store.Stats(ctx, "unused-node", time.Now())
	require.NoError(t, err)
	require.Zero(t, stats.PenaltyPending)
	require.Zero(t, stats.PenaltyDead)
}
