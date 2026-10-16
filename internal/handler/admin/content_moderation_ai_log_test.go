package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func aiAdminApplyLogMigrations(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, name := range names {
		raw, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, string(raw))
		require.NoError(t, err, "apply embedded migration %s", name)
	}
}

func aiAdminLogDatabaseThrough242(t *testing.T) *sql.DB {
	t.Helper()
	db := aiAdminTestDatabase(t)
	// 仅补齐迁移所引用的基础表；日志列、约束和默认值全部来自生产迁移。
	_, err := db.Exec(`
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE users (id BIGINT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'active');
CREATE TABLE api_keys (id BIGINT PRIMARY KEY);
CREATE TABLE groups (id BIGINT PRIMARY KEY);
INSERT INTO users (id) VALUES (7);
INSERT INTO api_keys (id) VALUES (8);`)
	require.NoError(t, err)
	aiAdminApplyLogMigrations(t, db,
		"135_content_moderation.sql",
		"174_group_allow_blocked_keyword_requests.sql",
		"175_content_moderation_remote_user.sql",
		"183_content_moderation_language_evidence.sql",
		"184_content_moderation_matched_keyword.sql",
		"216_content_moderation_enterprise_identity.sql",
		"242_content_moderation_ai_review_detail.sql",
	)
	return db
}

// 用户读取使用固定测试数据；JWT 签名、验签、角色和令牌版本检查均走正式服务与中间件。
// 这里验证详情 handler 挂载正式中间件后的行为，不替代完整路由装配或登录流程测试。
type aiAdminLogUsers struct{ service.UserRepository }

func (aiAdminLogUsers) GetByID(_ context.Context, id int64) (*service.User, error) {
	role := service.RoleAdmin
	if id == 2 {
		role = service.RoleUser
	} else if id != 1 {
		return nil, service.ErrUserNotFound
	}
	return &service.User{ID: id, Role: role, Status: service.StatusActive, TokenVersion: 2, TokenVersionResolved: true}, nil
}

func (aiAdminLogUsers) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func aiAdminLogRouter(t *testing.T, svc *service.ContentModerationService) (http.Handler, map[string]string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "synthetic-log-test-signing-secret", ExpireHour: 1}}
	auth := service.NewAuthService(nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	users := aiAdminLogUsers{}
	tokens := make(map[string]string)
	for name, id := range map[string]int64{"admin": 1, "user": 2, "revoked": 1} {
		user, err := users.GetByID(context.Background(), id)
		require.NoError(t, err)
		if name == "revoked" {
			user.TokenVersion--
		}
		tokens[name], err = auth.GenerateToken(user)
		require.NoError(t, err)
	}
	router := gin.New()
	group := router.Group("/api/v1/admin/risk-control", gin.HandlerFunc(middleware.NewAdminAuthMiddleware(auth, service.NewUserService(users, nil, nil, nil), nil)))
	h := NewContentModerationHandler(svc)
	group.GET("/logs", h.ListLogs)
	group.GET("/logs/:id", h.GetLog)
	return router, tokens
}

func aiAdminLogGET(t *testing.T, router http.Handler, token, path string, status int) []byte {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, status, response.Code)
	return response.Body.Bytes()
}

func TestContentModerationAIFullTextSQLAndAdminDetail(t *testing.T) {
	db := aiAdminLogDatabaseThrough242(t)
	aiAdminApplyLogMigrations(t, db, "243_content_moderation_ai_full_text.sql")
	repo := repository.NewContentModerationRepository(db)
	const dedicatedKey = "synthetic-review-key"
	const tail = "审查长尾终点 LONG TAIL EVIDENCE"
	// 超过旧摘要、旧审查输入以及 256K 字符边界，且包含多字节文本和末尾证据。
	prefix := "首部 " + dedicatedKey + "; api_key=sk-proj-synthetic123456789; Bearer abcdefghijklmnopqrstuv; https://audit.example.test/private\n"
	redactedPrefix := "首部 [REDACTED]; api_key=[已脱敏]; Bearer [已脱敏]; [已脱敏]\n"
	longText := strings.Repeat("长文证据 safe context.\n", 16000) + tail
	body, err := json.Marshal(map[string]any{
		"model": "synthetic-business-model", "instructions": "客户规则完整保留",
		"messages": []any{
			map[string]string{"role": "system", "content": "客户系统文本"},
			map[string]string{"role": "assistant", "content": "历史回复完整保留"},
			map[string]string{"role": "tool", "content": "工具输出全文保留"},
			map[string]string{"role": "user", "content": prefix + longText},
		},
	})
	require.NoError(t, err)
	const contextText = "[instructions|customer_instruction] 客户规则完整保留\n[messages/0/content|system] 客户系统文本\n[messages/1/content|assistant] 历史回复完整保留\n[messages/2/content|tool] 工具输出全文保留\n[messages/3/content|user] "
	expectedFull := contextText + redactedPrefix + longText
	require.Greater(t, utf8.RuneCountInString(expectedFull), 256*1024)

	observed := make(chan service.ContentModerationAICanonicalInput, 1)
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer "+dedicatedKey {
			t.Error("unexpected reviewer endpoint or authorization")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			t.Error("invalid reviewer request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Messages[1].Content, &parts); err != nil || len(parts) != 1 || parts[0].Type != "text" || request.Messages[1].Role != "user" {
			t.Error("reviewer must receive customer text as data")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var canonical service.ContentModerationAICanonicalInput
		if err := json.Unmarshal([]byte(parts[0].Text), &canonical); err != nil {
			t.Error("invalid reviewer canonical data")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case observed <- canonical:
		default:
			t.Error("unexpected duplicate reviewer call")
		}
		result, _ := json.Marshal(map[string]any{
			"policy_version": "tokenav-text-v1", "verdict": "allow", "categories": []string{},
			"reason": "safe synthetic text " + dedicatedKey,
		})
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(result)}}}}); err != nil {
			t.Errorf("write synthetic reviewer response: %v", err)
		}
	}))
	t.Cleanup(provider.Close)
	cfg, err := json.Marshal(service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, AllGroups: true, RecordNonHits: true,
		KeywordBlockingMode: service.ContentModerationKeywordModeAIOnly,
		AI:                  service.ContentModerationAIConfig{Adapter: service.ContentModerationAIAdapterChat, BaseURL: provider.URL, APIKey: dedicatedKey, ExternalDataConsent: true},
	})
	require.NoError(t, err)
	// 在服务启动前写完固定配置，避免后台审计 worker 与可变设置替身并发读写。
	settings := &aiAdminSettings{values: map[string]string{service.SettingKeyContentModerationConfig: string(cfg), service.SettingKeyRiskControlEnabled: "true"}}
	svc := service.NewContentModerationService(settings, repo, nil, nil, nil, nil, nil)
	router, tokens := aiAdminLogRouter(t, svc)

	for _, sent := range []bool{true, false} {
		t.Run(fmt.Sprintf("provider_called_%t", sent), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			requestID := fmt.Sprintf("full-text-sent-%t", sent)
			decision, err := svc.Check(ctx, service.ContentModerationCheckInput{
				AdmissionID: requestID, Turn: 1, RequestID: requestID, UserID: 7, APIKeyID: 8,
				Protocol: service.ContentModerationProtocolOpenAIChat, Endpoint: "/v1/chat/completions", Body: body,
				LocalPolicyOnly: !sent,
			})
			require.NoError(t, err)
			require.NotNil(t, decision)
			require.Equal(t, sent, decision.ProviderCalled)
			expectedReview := ""
			if sent {
				require.Equal(t, "allow", decision.Verdict)
				require.Equal(t, "completed", decision.Phase)
				expectedReview = expectedFull
				select {
				case actual := <-observed:
					require.Len(t, actual.Fragments, 5)
					var sentText strings.Builder
					for _, fragment := range actual.Fragments {
						fmt.Fprintf(&sentText, "[%s|%s] %s\n", fragment.ParentID, fragment.Role, fragment.Text)
					}
					require.Equal(t, contextText+prefix+longText, strings.TrimSpace(sentText.String()), "compare against the complete text actually received over HTTP")
				case <-ctx.Done():
					t.Fatal("reviewer did not receive the full request")
				}
			} else {
				require.Equal(t, "error", decision.Verdict)
				require.Equal(t, "ai_unsupported_protocol", decision.ErrorCode)
				require.Equal(t, "preparation", decision.Phase)
			}

			// 等待真实异步审计队列落库；不向仓储替身手工塞入预期日志。
			var id int64
			require.Eventually(t, func() bool {
				err := db.QueryRowContext(ctx, `SELECT id FROM content_moderation_logs WHERE request_id = $1`, requestID).Scan(&id)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("query persisted audit record: %v", err)
				}
				return err == nil
			}, 5*time.Second, 10*time.Millisecond, "production audit queue must persist the log")
			var full, review, excerpt string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT full_input_text, review_text_excerpt, input_excerpt FROM content_moderation_logs WHERE id = $1`, id).Scan(&full, &review, &excerpt))
			require.Equal(t, expectedFull, full)
			require.Equal(t, expectedReview, review)
			require.Equal(t, string([]rune(expectedFull)[:240]), excerpt)
			require.NotContains(t, excerpt, tail)
			stored, err := repo.GetLog(ctx, id)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, full, stored.FullInputText)
			require.Equal(t, review, stored.ReviewTextExcerpt)
			require.Equal(t, "active", stored.UserStatus)
			require.Equal(t, sent, stored.AIReviewDetail["provider_called"])
			if sent {
				require.Equal(t, "safe synthetic text [REDACTED]", stored.AIReviewDetail["reason"])
			}
			response := aiAdminLogGET(t, router, tokens["admin"], fmt.Sprintf("/api/v1/admin/risk-control/logs/%d", id), http.StatusOK)
			var detail struct {
				Data service.ContentModerationLog `json:"data"`
			}
			require.NoError(t, json.Unmarshal(response, &detail))
			require.Equal(t, id, detail.Data.ID)
			require.Equal(t, expectedFull, detail.Data.FullInputText)
			require.Equal(t, expectedReview, detail.Data.ReviewTextExcerpt)
			for _, secret := range []string{dedicatedKey, "sk-proj-synthetic123456789", "abcdefghijklmnopqrstuv", "https://audit.example.test/private"} {
				require.NotContains(t, string(response), secret, "administrator detail must remain redacted")
			}
		})
	}
	require.EqualValues(t, 1, calls.Load(), "unsubmitted extraction must not trigger a provider call")
	// 长尾只在全文中出现：可搜索命中，但仓储结果与 HTTP 分页均不得携带两份全文。
	items, page, err := repo.ListLogs(context.Background(), service.ContentModerationLogFilter{Search: tail, Pagination: pagination.PaginationParams{Page: 1, PageSize: 10}})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, items, 2)
	for _, item := range items {
		require.Empty(t, item.FullInputText)
		require.Empty(t, item.ReviewTextExcerpt)
	}
	response := aiAdminLogGET(t, router, tokens["admin"], "/api/v1/admin/risk-control/logs?search="+url.QueryEscape(tail), http.StatusOK)
	var list struct {
		Data struct {
			Total int64                        `json:"total"`
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response, &list))
	require.EqualValues(t, 2, list.Data.Total)
	require.Len(t, list.Data.Items, 2)
	for _, item := range list.Data.Items {
		require.NotContains(t, item, "full_input_text")
		require.NotContains(t, item, "review_text_excerpt")
		require.Contains(t, item, "input_excerpt")
		require.Contains(t, item, "ai_review_detail")
	}
	require.NotContains(t, string(response), tail)
}

func TestContentModerationAIFullTextMigration243PreservesHistoryAndReplays(t *testing.T) {
	db := aiAdminLogDatabaseThrough242(t)
	ctx := context.Background()
	var fullColumns int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'content_moderation_logs' AND column_name = 'full_input_text'`).Scan(&fullColumns))
	require.Zero(t, fullColumns, "fixture must start at the real 242 schema")
	_, err := db.ExecContext(ctx, `INSERT INTO content_moderation_logs (request_id, input_excerpt, review_text_excerpt, ai_review_detail) VALUES
('legacy-review', '旧输入摘要（已截断）', '旧送审摘要（已截断）', '{"verdict":"allow"}'),
('legacy-no-review', '仅有旧输入摘要', '', '{}')`)
	require.NoError(t, err)
	legacy := make(map[int64]string)
	for _, requestID := range []string{"legacy-review", "legacy-no-review"} {
		var id int64
		var row string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT id, to_jsonb(l)::text FROM content_moderation_logs l WHERE request_id = $1`, requestID).Scan(&id, &row))
		legacy[id] = row
	}
	aiAdminApplyLogMigrations(t, db, "243_content_moderation_ai_full_text.sql")
	repo := repository.NewContentModerationRepository(db)
	fresh := &service.ContentModerationLog{
		RequestID: "after-243", InputExcerpt: "新的列表预览", FullInputText: "完整提取 " + strings.Repeat("原文 ", 5000) + "提取长尾",
		ReviewTextExcerpt: "实际送审 " + strings.Repeat("送审 ", 5000) + "送审长尾",
		CategoryScores:    map[string]float64{}, ThresholdSnapshot: map[string]float64{},
	}
	require.NoError(t, repo.CreateLog(ctx, fresh))
	var freshSnapshot string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT to_jsonb(l)::text FROM content_moderation_logs l WHERE id = $1`, fresh.ID).Scan(&freshSnapshot))
	for replay := 0; replay < 3; replay++ {
		t.Run(fmt.Sprintf("replay_%d", replay), func(t *testing.T) {
			if replay > 0 {
				aiAdminApplyLogMigrations(t, db, "243_content_moderation_ai_full_text.sql")
			}
			for id, snapshot := range legacy {
				var oldColumns, full string
				require.NoError(t, db.QueryRowContext(ctx, `SELECT (to_jsonb(l) - 'full_input_text')::text, full_input_text FROM content_moderation_logs l WHERE id = $1`, id).Scan(&oldColumns, &full))
				require.Equal(t, snapshot, oldColumns, "migration must preserve every historical field, including the truncated review")
				require.Empty(t, full, "never backfill full input from a historical excerpt")
				detail, err := repo.GetLog(ctx, id)
				require.NoError(t, err)
				require.NotNil(t, detail)
				require.Empty(t, detail.FullInputText)
				if detail.RequestID == "legacy-review" {
					require.Equal(t, "旧送审摘要（已截断）", detail.ReviewTextExcerpt)
				} else {
					require.Empty(t, detail.ReviewTextExcerpt)
				}
			}
			var actualSnapshot string
			require.NoError(t, db.QueryRowContext(ctx, `SELECT to_jsonb(l)::text FROM content_moderation_logs l WHERE id = $1`, fresh.ID).Scan(&actualSnapshot))
			require.Equal(t, freshSnapshot, actualSnapshot, "replay must not overwrite new full text or metadata")
			detail, err := repo.GetLog(ctx, fresh.ID)
			require.NoError(t, err)
			require.NotNil(t, detail)
			require.Equal(t, fresh.FullInputText, detail.FullInputText)
			require.Equal(t, fresh.ReviewTextExcerpt, detail.ReviewTextExcerpt, "two distinct texts must not be swapped or reconstructed from the preview")
		})
	}
	var dataType, nullable, defaultValue string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'content_moderation_logs' AND column_name = 'full_input_text'`).Scan(&dataType, &nullable, &defaultValue))
	require.Equal(t, "text", dataType)
	require.Equal(t, "NO", nullable)
	require.Equal(t, "''::text", defaultValue)
}

func TestContentModerationAILogDetailValidationAndRealAdminMiddleware(t *testing.T) {
	db := aiAdminLogDatabaseThrough242(t)
	aiAdminApplyLogMigrations(t, db, "243_content_moderation_ai_full_text.sql")
	repo := repository.NewContentModerationRepository(db)
	svc := service.NewContentModerationService(nil, repo, nil, nil, nil, nil, nil)
	router, tokens := aiAdminLogRouter(t, svc)
	const path = "/api/v1/admin/risk-control/logs/"
	for _, id := range []string{"abc", "0", "-1", "1.5", "9223372036854775808"} {
		t.Run("invalid_"+id, func(t *testing.T) {
			response := aiAdminLogGET(t, router, tokens["admin"], path+id, http.StatusBadRequest)
			require.Contains(t, string(response), "Invalid log id")
		})
	}
	t.Run("missing_record", func(t *testing.T) {
		missing, err := repo.GetLog(context.Background(), 999999)
		require.NoError(t, err)
		require.Nil(t, missing)
		response := aiAdminLogGET(t, router, tokens["admin"], path+"999999", http.StatusNotFound)
		require.Contains(t, string(response), "Content moderation log not found")
	})
	for _, tc := range []struct {
		name, token, reason string
		status              int
	}{
		{"anonymous", "", "UNAUTHORIZED", http.StatusUnauthorized},
		{"invalid_signature", "invalid-token", "INVALID_TOKEN", http.StatusUnauthorized},
		{"ordinary_user", tokens["user"], "FORBIDDEN", http.StatusForbidden},
		{"revoked_token", tokens["revoked"], "TOKEN_REVOKED", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, target := range []string{strings.TrimSuffix(path, "/"), path + "999999", path + "abc"} {
				response := aiAdminLogGET(t, router, tc.token, target, tc.status)
				require.Contains(t, string(response), tc.reason, "real middleware must reject before log lookup or parameter validation")
				require.NotContains(t, string(response), "full_input_text")
				require.NotContains(t, string(response), "review_text_excerpt")
			}
		})
	}
}
