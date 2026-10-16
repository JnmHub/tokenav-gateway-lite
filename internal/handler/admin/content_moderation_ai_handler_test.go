package admin

import (
	"bytes"
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

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type aiAdminSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *aiAdminSettings) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}
func (s *aiAdminSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := s.values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}
func (s *aiAdminSettings) SetMultiple(_ context.Context, values map[string]string) error {
	for k, v := range values {
		s.values[k] = v
	}
	return nil
}

func aiAdminTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PROMPT_AUDIT_TEST_POSTGRES_DSN"))
	require.NotEmpty(t, dsn, "PROMPT_AUDIT_TEST_POSTGRES_DSN is required for real SQL tests")
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("open PostgreSQL maintenance connection failed")
	}
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var sharedDatabase string
	require.NoError(t, admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&sharedDatabase))

	// 共享连接只用于创建/删除本次随机库；成功创建后才登记清理，绝不按前缀批量删库。
	database := "cm_ai_admin_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NotEqual(t, sharedDatabase, database)
	quoted := pq.QuoteIdentifier(database)
	_, err = admin.ExecContext(ctx, `CREATE DATABASE `+quoted+` TEMPLATE template0`)
	require.NoError(t, err)
	t.Cleanup(func() {
		// DROP DATABASE 可能等待本机共享 PostgreSQL 的检查点；保留足够清理窗口。
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, err := admin.ExecContext(cleanupCtx, `DROP DATABASE `+quoted+` WITH (FORCE)`)
		require.NoError(t, err, "remove only the database created by this test")
		var remaining int
		require.NoError(t, admin.QueryRowContext(cleanupCtx, `SELECT COUNT(*) FROM pg_database WHERE datname = $1`, database).Scan(&remaining))
		require.Zero(t, remaining, "isolated test database must be removed")
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse PostgreSQL test connection failed")
		}
		u.Path, u.RawPath = "/"+database, ""
		query := u.Query()
		query.Del("dbname")
		query.Del("database")
		query.Set("search_path", "public")
		u.RawQuery = query.Encode()
		dsn = u.String()
	} else {
		dsn += " dbname=" + database + " search_path=public"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("open isolated PostgreSQL test connection failed")
	}
	// Cleanup 后进先出：先关闭测试连接，再删除已登记的唯一数据库，最后关闭维护连接。
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	db.SetMaxOpenConns(4)
	var connected string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&connected))
	require.Equal(t, database, connected, "verify isolation before any schema or data writes")
	return db
}

func TestContentModerationAIAdminRealHTTPWithSQLAndSyntheticAdapter(t *testing.T) {
	db := aiAdminTestDatabase(t)
	store := service.NewSQLContentModerationAIStore(db)
	require.NoError(t, store.EnsureSchema(context.Background()))
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/chat/completions", r.URL.Path)
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Len(t, request.Messages, 2)
		require.Equal(t, "user", request.Messages[1].Role)
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		require.NoError(t, json.Unmarshal(request.Messages[1].Content, &parts))
		require.NotEmpty(t, parts)
		require.Equal(t, "text", parts[0].Type)
		var input struct {
			Fragments []service.ContentModerationAIFragment `json:"fragments"`
		}
		require.NoError(t, json.Unmarshal([]byte(parts[0].Text), &input))
		require.NotEmpty(t, input.Fragments)
		verdict, categories, reason := "allow", []string{}, "Synthetic text is safe"
		for _, f := range input.Fragments {
			if strings.Contains(f.Text, "violent attack") {
				verdict, categories, reason = "block", []string{"illicit/violent"}, "Synthetic request asks for a violent attack"
			}
		}
		result := map[string]any{"policy_version": "tokenav-text-v1", "verdict": verdict, "categories": categories, "reason": reason}
		text, err := json.Marshal(result)
		require.NoError(t, err)
		// The overall verdict is sufficient; compatible providers need not echo
		// the configured model or supply usage for the gateway to accept it.
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(text)}}}})
	}))
	defer provider.Close()
	settings := &aiAdminSettings{values: map[string]string{}}
	svc := service.NewContentModerationService(settings, nil, nil, nil, nil, nil, nil)
	svc.SetAIStore(store)
	h := NewContentModerationHandler(svc)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/config", h.GetConfig)
	r.PUT("/config", h.UpdateConfig)
	r.POST("/ai/test", h.TestAI)
	r.GET("/ai/metrics", h.GetAIMetrics)
	r.GET("/status", h.GetStatus)
	invoke := func(method, path string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var response map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		return w.Code, response
	}
	code, out := invoke("POST", "/ai/test", map[string]string{"case_id": "allow"})
	require.Equal(t, 200, code)
	data := out["data"].(map[string]any)
	require.Equal(t, "ai_not_configured", data["error_code"])
	require.Zero(t, calls.Load())
	code, out = invoke("PUT", "/config", map[string]any{"keyword_blocking_mode": "ai_only", "mode": "shadow", "ai": map[string]any{"adapter": "deepseek_chat", "base_url": provider.URL, "api_key": "synthetic-key", "external_data_consent": false, "penalties_enabled": false}})
	require.Equal(t, 200, code)
	data = out["data"].(map[string]any)
	require.Equal(t, "ai_only", data["keyword_blocking_mode"])
	ai := data["ai"].(map[string]any)
	require.Equal(t, true, ai["api_key_configured"])
	require.Equal(t, false, ai["external_data_consent"])
	require.NotContains(t, fmt.Sprint(out), "synthetic-key")
	for _, removed := range []string{
		"total_timeout_ms", "max_concurrency", "tenant_max_concurrency", "queue_size", "rpm", "tenant_rpm",
		"daily_budget_usd_micros", "tenant_daily_budget_usd_micros", "daily_token_budget", "tenant_daily_token_budget",
		"input_price_per_million_usd_micros", "output_price_per_million_usd_micros", "max_input_bytes", "batch_input_runes",
		"fragment_runes", "fragment_overlap_runes", "max_output_tokens", "retry_count", "cache_ttl_seconds",
		"circuit_failure_threshold", "circuit_open_seconds", "max_images_per_request", "max_image_bytes",
	} {
		require.NotContains(t, ai, removed)
	}
	for _, tc := range []struct{ id, want string }{{"allow", "allow"}, {"block", "block"}, {"injection", "allow"}, {"long_tail", "block"}, {"unsupported_media", "allow"}, {"uncovered_field", "allow"}} {
		t.Run(tc.id, func(t *testing.T) {
			code, out := invoke("POST", "/ai/test", map[string]string{"case_id": tc.id})
			require.Equal(t, 200, code)
			data := out["data"].(map[string]any)
			require.Equal(t, tc.want, data["verdict"])
			require.Equal(t, "none", data["penalty_state"])
			require.Equal(t, true, data["provider_called"])
			require.Equal(t, "completed", data["phase"])
			require.NotEmpty(t, data["reason"])
			coverage := data["coverage"].(map[string]any)
			require.Equal(t, true, coverage["complete"])
			if tc.id == "unsupported_media" || tc.id == "uncovered_field" {
				require.NotEmpty(t, coverage["uncovered_fields"])
			}
			if tc.want == "block" {
				require.Equal(t, []any{"illicit/violent"}, data["categories"])
			} else {
				require.Empty(t, data["categories"])
			}
			for _, removed := range []string{"estimated_cost_usd_micros", "cache_hit", "fragments"} {
				require.NotContains(t, data, removed)
			}
		})
	}
	require.EqualValues(t, 6, calls.Load(), "each built-in case makes exactly one provider call")
	code, _ = invoke("POST", "/ai/test", map[string]string{"case_id": "allow", "prompt": "private customer content"})
	require.Equal(t, 400, code)
	code, out = invoke("GET", "/ai/metrics", nil)
	require.Equal(t, 200, code)
	metrics := out["data"].(map[string]any)
	require.Equal(t, true, metrics["store_ready"])
	require.EqualValues(t, 0, metrics["penalty_pending"])
	require.EqualValues(t, 0, metrics["penalty_dead"])
	for _, removed := range []string{"day_reserved_usd_micros", "day_reserved_tokens", "cache_hits", "queue_length", "circuit_open_until"} {
		require.NotContains(t, metrics, removed)
	}
	require.GreaterOrEqual(t, metrics["checked"].(float64), float64(7))
	code, out = invoke("GET", "/status", nil)
	require.Equal(t, 200, code)
	require.Contains(t, out["data"].(map[string]any), "ai")
	stats, err := store.Stats(context.Background(), "standalone", time.Now())
	require.NoError(t, err)
	require.Zero(t, stats.PenaltyPending)
	require.Zero(t, stats.PenaltyDead)
}
