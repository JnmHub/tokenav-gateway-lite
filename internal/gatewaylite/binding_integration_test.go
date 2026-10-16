package gatewaylite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Each run owns a randomly named database and Redis prefix. Cleanup uses only
// those exact resources; it never migrates, drops or flushes the parent's DB.
func bindingPostgres(t *testing.T) (*sql.DB, func() *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("SUB2API_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("isolated PostgreSQL environment required")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, u.Scheme, "postgres")
	adminURL := *u
	adminURL.Path = "/postgres"
	admin, err := sql.Open("postgres", adminURL.String())
	require.NoError(t, err)
	name := "gw_binding_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(name))
	require.NoError(t, err)
	testURL := *u
	testURL.Path = "/" + name
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err = admin.ExecContext(ctx, "DROP DATABASE "+pq.QuoteIdentifier(name)+" WITH (FORCE)")
		require.NoError(t, err)
		_ = admin.Close()
	})
	open := func() *sql.DB {
		db, err := sql.Open("postgres", testURL.String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	for _, file := range []string{"185_gateway_lite_usage_retry_outbox.sql", "241_gateway_lite_oauth_invocation_recovery.sql", "217_content_moderation_penalty_outbox.sql", "189_image_async_tasks.sql", "159_grok_video_tasks.sql", "159_batch_image_foundation.sql"} {
		raw, err := migrations.FS.ReadFile(file)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(raw))
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE usage_billing_dedup(request_id TEXT,api_key_id BIGINT);CREATE TABLE usage_billing_dedup_archive(request_id TEXT,api_key_id BIGINT)`)
	require.NoError(t, err)
	return db, open
}
func TestBindingBatchTerminalReleaseAndMediaRegistryEvidence(t *testing.T) {
	db, _ := bindingPostgres(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS gateway_lite_binding_media(binding_id TEXT,request_id TEXT,task_id TEXT DEFAULT '',state TEXT DEFAULT 'pending',updated_at TIMESTAMPTZ DEFAULT NOW(),PRIMARY KEY(binding_id,request_id))`)
	require.NoError(t, err)
	cases := []struct {
		status           string
		settled, release bool
		hold             int
		want             int64
	}{{"completed", true, false, 5, 0}, {"output_deleted", true, false, 5, 0}, {"failed", false, true, 5, 0}, {"cancelled", false, true, 5, 0}, {"failed", false, false, 0, 0}, {"failed", false, false, 5, 1}, {"cancelled", false, false, 5, 1}, {"running", false, false, 5, 1}, {"completed", false, false, 5, 1}}
	for i, tc := range cases {
		t.Run(tc.status+fmt.Sprint(i), func(t *testing.T) {
			id := fmt.Sprint(i)
			_, err := db.ExecContext(ctx, `INSERT INTO batch_image_jobs(batch_id,user_id,api_key_id,provider,model,item_count,status,hold_amount,settled_at) VALUES($1,1,1,'fixture','fixture',1,$2,$3,CASE WHEN $4 THEN NOW() ELSE NULL END)`, id, tc.status, tc.hold, tc.settled)
			require.NoError(t, err)
			if tc.release {
				_, err = db.ExecContext(ctx, `INSERT INTO usage_billing_dedup(request_id,api_key_id) VALUES($1,1)`, "batch_image_release:"+id)
				require.NoError(t, err)
			}
			var n int64
			require.NoError(t, db.QueryRowContext(ctx, bindingBatchImageOutstandingSQL).Scan(&n))
			require.Equal(t, tc.want, n)
			_, err = db.ExecContext(ctx, `DELETE FROM batch_image_jobs WHERE batch_id=$1`, id)
			require.NoError(t, err)
		})
	}
	m, err := NewBindingManager(ctx, SQLBindingStore{db}, config.GatewayLiteConfig{})
	require.NoError(t, err)
	m.remote = &bindingFakeRemote{counts: map[string]int64{}}
	m.restart = func() bool { return true }
	EnableBindingRestart(true)
	defer EnableBindingRestart(false)
	bindForTest(t, m)
	m.mediaDB = db
	base := ContextWithBindingTurnGuard(ctx, m)
	dispatch, err := PrepareBindingMediaDispatch(base)
	require.NoError(t, err)
	var pending int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_lite_binding_media WHERE state='pending'`).Scan(&pending))
	require.Equal(t, 1, pending)
	require.NoError(t, RecordBindingMediaTask(dispatch, "seedance:one"))
	SetDefaultBindingManager(m)
	defer SetDefaultBindingManager(nil)
	require.NoError(t, ConfirmBindingMediaSettlement(ctx, "seedance:one"))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_lite_binding_media WHERE state='pending'`).Scan(&pending))
	require.Zero(t, pending)
	second, err := PrepareBindingMediaDispatch(base)
	require.NoError(t, err)
	require.NoError(t, ReleaseBindingMediaBeforeAcceptance(second))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM gateway_lite_binding_media WHERE state='pending'`).Scan(&pending))
	require.Zero(t, pending)
	verified, err := bindingLegacyMediaEvidence(ctx, db, m.Record().Current.BindingID)
	require.NoError(t, err)
	require.False(t, verified)
	_, err = db.ExecContext(ctx, `INSERT INTO gateway_lite_binding_media_coverage(binding_id,source,evidence_sha256,complete_history,unsettled_count) VALUES($1,'provider_terminal_reconciliation:fixture',$2,true,0)`, m.Record().Current.BindingID, strings.Repeat("a", 64))
	require.NoError(t, err)
	verified, err = bindingLegacyMediaEvidence(ctx, db, m.Record().Current.BindingID)
	require.NoError(t, err)
	require.True(t, verified)
}
func TestBindingPostgreSQLPersistenceDrainRedisAndRestart(t *testing.T) {
	db, reopen := bindingPostgres(t)
	redisURL := strings.TrimSpace(os.Getenv("TOKENAV_TEST_REDIS_URL"))
	if redisURL == "" {
		t.Skip("isolated Redis environment required")
	}
	options, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	prefix := "gl:binding-test:" + uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, client.Ping(ctx).Err())
	keys := []string{prefix + ":usage:stream", prefix + ":usage:dead", prefix + ":req:reserved", prefix + ":funding:pending"}
	t.Cleanup(func() { require.NoError(t, client.Del(context.Background(), keys...).Err()) })
	m, err := NewBindingManager(ctx, SQLBindingStore{db}, config.GatewayLiteConfig{})
	require.NoError(t, err)
	m.remote = &bindingFakeRemote{counts: map[string]int64{}}
	m.restart = func() bool { return true }
	m.ConfigureDrain(db, client)
	EnableBindingRestart(true)
	defer EnableBindingRestart(false)
	in := BindingInput{URL: "https://old.invalid", Token: "only-call", Name: "n", PublicURL: "http://127.0.0.1:1000", Region: "hk"}
	_, err = m.Register(ctx, in)
	require.NoError(t, err)
	restored, err := NewBindingManager(ctx, SQLBindingStore{reopen()}, config.GatewayLiteConfig{ControlPlaneURL: "https://stale.invalid"})
	require.NoError(t, err)
	require.Equal(t, "activating", restored.Status().Phase)
	require.NoError(t, m.Activate(ctx, func(context.Context) error { return nil }))
	m.mu.Lock()
	next := cloneBindingRecord(m.record)
	next.Current.RedisPrefix = prefix
	require.NoError(t, m.saveLocked(ctx, next))
	m.mu.Unlock()
	ready, err := m.DisconnectCheck(ctx)
	require.NoError(t, err)
	require.True(t, ready.Ready)
	_, err = db.ExecContext(ctx, `INSERT INTO gateway_lite_usage_retry_outbox(request_id,operation_kind,event_payload) VALUES('fixture','report','{}')`)
	require.NoError(t, err)
	_, err = m.Disconnect(ctx)
	require.Error(t, err)
	require.Equal(t, "blocked", m.Status().Phase)
	require.NoError(t, m.Cancel(ctx))
	_, err = db.ExecContext(ctx, `DELETE FROM gateway_lite_usage_retry_outbox WHERE request_id='fixture'`)
	require.NoError(t, err)
	require.NoError(t, client.XGroupCreateMkStream(ctx, keys[0], prefix+"-usage-reporters", "0").Err())
	id, err := client.XAdd(ctx, &redis.XAddArgs{Stream: keys[0], Values: map[string]any{"event": "fixture"}}).Result()
	require.NoError(t, err)
	_, err = client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: prefix + "-usage-reporters", Consumer: "fixture", Streams: []string{keys[0], ">"}, Count: 1}).Result()
	require.NoError(t, err)
	counts, err := m.DisconnectCheck(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, counts.Counts["redis_pending"])
	require.False(t, counts.Ready)
	require.NoError(t, client.XAck(ctx, keys[0], prefix+"-usage-reporters", id).Err())
	require.NoError(t, client.XDel(ctx, keys[0], id).Err())
	require.NoError(t, client.HSet(ctx, keys[2], "status", "reserved").Err())
	counts, err = m.DisconnectCheck(ctx)
	require.NoError(t, err)
	require.False(t, counts.Ready)
	require.EqualValues(t, 1, counts.Counts["reserved_requests"])
	require.NoError(t, client.Del(ctx, keys[2]).Err())
	require.NoError(t, client.HSet(ctx, keys[3], "funding_final", "0").Err())
	counts, err = m.DisconnectCheck(ctx)
	require.NoError(t, err)
	require.False(t, counts.Ready)
	require.NoError(t, client.Del(ctx, keys[3]).Err())
	_, err = m.Disconnect(ctx)
	require.NoError(t, err)
	restored, err = NewBindingManager(ctx, SQLBindingStore{reopen()}, config.GatewayLiteConfig{ControlPlaneURL: in.URL})
	require.NoError(t, err)
	require.Equal(t, "unbound", restored.Status().Phase)
	cfg := config.GatewayLiteConfig{}
	restored.ApplyStartup(&cfg)
	require.Empty(t, cfg.ControlPlaneURL)
	var workers workerLifetime
	done := make(chan struct{})
	workers.Go(ctx, func(ctx context.Context) { defer close(done); <-ctx.Done() })
	require.NoError(t, workers.Stop(ctx))
	<-done
}

// The HTTP peer stores exact receipts in an independent SQL row. Coupled with
// the control-plane's real handler tests, this checks the gateway's SQL/CAS
// recovery and wire client without replacing the production remote transport.
func TestBindingLegacySQLAndHTTPDisconnectRecovery(t *testing.T) {
	for _, scenario := range []string{"lost_reply", "local_cas_failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, reopen := bindingPostgres(t)
			ctx := context.Background()
			_, err := db.ExecContext(ctx, `CREATE TABLE binding_remote_fixture_receipts(
 node_id BIGINT PRIMARY KEY,gateway_code TEXT,binding_id TEXT,node_scope TEXT,operation_id TEXT)`)
			require.NoError(t, err)
			remoteDB := reopen()
			code, nodeID := "legacy-"+uuid.NewString(), int64(9)
			var disconnects, handshakes atomic.Int64
			var localFailed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var in struct {
					BindingID   string `json:"binding_id"`
					GatewayCode string `json:"gateway_code"`
					NodeScope   string `json:"node_scope"`
					OperationID string `json:"operation_id"`
				}
				if request.Header.Get("Authorization") != "Bearer binding-fixture-internal" || json.NewDecoder(request.Body).Decode(&in) != nil || in.GatewayCode != code || in.NodeScope != code || in.BindingID == "" {
					w.WriteHeader(403)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "binding_scope_mismatch"})
					return
				}
				switch request.URL.Path {
				case "/internal/gateway-binding/legacy-handshake":
					handshakes.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "node_id": nodeID, "binding_id": in.BindingID, "outstanding": map[string]int64{"funding": 0}})
				case "/internal/gateway-binding/legacy-disconnect":
					disconnects.Add(1)
					if strings.TrimSpace(in.OperationID) == "" {
						w.WriteHeader(400)
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
						return
					}
					_, err := remoteDB.ExecContext(request.Context(), `INSERT INTO binding_remote_fixture_receipts(node_id,gateway_code,binding_id,node_scope,operation_id) VALUES($1,$2,$3,$4,$5)`, nodeID, in.GatewayCode, in.BindingID, in.NodeScope, in.OperationID)
					if err != nil {
						w.WriteHeader(503)
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
						return
					}
					if scenario == "lost_reply" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
							return
						}
					} else {
						// A competing durable revision makes the post-ack local CAS
						// fail without removing its already persisted operation.
						store := SQLBindingStore{remoteDB}
						record, loadErr := store.Load(request.Context())
						if loadErr == nil {
							expected := record.Revision
							record.Revision++
							localFailed.Store(store.Save(request.Context(), record, expected) == nil)
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
				case "/internal/gateway-binding/legacy-disconnect-status":
					var detached bool
					err := remoteDB.QueryRowContext(request.Context(), `SELECT EXISTS(SELECT 1 FROM binding_remote_fixture_receipts
 WHERE node_id=$1 AND gateway_code=$2 AND binding_id=$3 AND node_scope=$4 AND operation_id=$5)`, nodeID, in.GatewayCode, in.BindingID, in.NodeScope, in.OperationID).Scan(&detached)
					if err != nil {
						w.WriteHeader(503)
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "detached": detached, "operation_id": in.OperationID, "binding_id": in.BindingID})
				default:
					w.WriteHeader(404)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
				}
			}))
			defer server.Close()
			m, err := NewBindingManager(ctx, SQLBindingStore{db}, config.GatewayLiteConfig{ControlPlaneURL: server.URL, ControlPlaneToken: "binding-fixture-internal", GatewayCode: code, Region: "local", RedisPrefix: "gl:legacy:" + uuid.NewString()})
			require.NoError(t, err)
			m.restart = func() bool { return true }
			m.drain = func(context.Context, BindingRecord) (BindingDrain, error) {
				return BindingDrain{Counts: map[string]int64{}}, nil
			}
			require.NoError(t, m.Activate(ctx, func(context.Context) error { return nil }))
			old := *m.Record().Current
			_, err = m.Disconnect(ctx)
			require.Error(t, err)
			if scenario == "local_cas_failure" {
				require.True(t, localFailed.Load())
				require.ErrorIs(t, err, ErrBindingConflict)
			}
			require.Equal(t, old, *m.Record().Current)
			require.NotEmpty(t, m.Record().DisconnectOperation)
			require.Error(t, m.Cancel(ctx))
			var payload string
			require.NoError(t, remoteDB.QueryRowContext(ctx, `SELECT payload FROM gateway_lite_binding WHERE id=1`).Scan(&payload))
			var saved BindingRecord
			require.NoError(t, json.Unmarshal([]byte(payload), &saved))
			require.Equal(t, old, *saved.Current)
			require.Equal(t, m.Record().DisconnectOperation, saved.DisconnectOperation)
			require.NoError(t, db.Close())
			restored, err := NewBindingManager(ctx, SQLBindingStore{reopen()}, config.GatewayLiteConfig{ControlPlaneURL: "https://wrong-env.invalid"})
			require.NoError(t, err)
			restored.restart = m.restart
			restored.drain = func(context.Context, BindingRecord) (BindingDrain, error) {
				t.Fatal("an exact remote receipt must recover without re-handshaking an inactive node")
				return BindingDrain{}, nil
			}
			_, err = restored.Disconnect(ctx)
			require.NoError(t, err)
			require.Equal(t, "unbound", restored.Status().Phase)
			require.Nil(t, restored.Record().Current)
			require.EqualValues(t, 1, disconnects.Load())
			require.EqualValues(t, 2, handshakes.Load())
			persisted, err := SQLBindingStore{reopen()}.Load(ctx)
			require.NoError(t, err)
			require.Equal(t, "unbound", persisted.Phase)
			require.Empty(t, persisted.DisconnectOperation)
			require.Nil(t, persisted.Current)
		})
	}
}
