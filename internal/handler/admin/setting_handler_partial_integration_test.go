//go:build integration

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// 接入已迁移的原生测试库，每个用例复制生产 settings 表至独立 schema。
// CAS 必须自行开启事务，不能把 Ent 客户端套在外层回滚事务中。
// 不读取私密文件、不打印 DSN/驱动错误；未配置可跳过，显式配置无效或不可达必须失败。
func newPostgresPartialSettingsRepo(t *testing.T) (service.SettingRepository, *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TOKENAV_REPOSITORY_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TOKENAV_REPOSITORY_TEST_DATABASE_URL not configured; PostgreSQL handler regression not executed")
	}
	target, err := url.Parse(dsn)
	if err != nil || target == nil {
		t.Fatal("invalid PostgreSQL test connection configuration")
	}
	address := net.ParseIP(target.Hostname())
	loopback := strings.EqualFold(target.Hostname(), "localhost") || (address != nil && address.IsLoopback())
	if (target.Scheme != "postgres" && target.Scheme != "postgresql") || !loopback || !strings.Contains(strings.ToLower(strings.TrimPrefix(target.Path, "/")), "test") {
		t.Fatal("PostgreSQL handler regression requires an explicitly named loopback test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	baseDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("could not open PostgreSQL test database")
	}
	t.Cleanup(func() { _ = baseDB.Close() })
	if err := baseDB.PingContext(ctx); err != nil {
		t.Fatal("PostgreSQL test database is unreachable")
	}
	schema := fmt.Sprintf("settings_handler_test_%d", time.Now().UnixNano())
	if _, err := baseDB.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal("could not create isolated settings test schema")
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := baseDB.ExecContext(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error("could not clean isolated settings test schema")
		}
	})
	if _, err := baseDB.ExecContext(ctx, "CREATE TABLE "+schema+".settings (LIKE public.settings INCLUDING ALL)"); err != nil {
		t.Fatal("production settings table must be migrated before handler integration regression")
	}
	query := target.Query()
	query.Set("search_path", schema)
	query.Set("connect_timeout", "5")
	target.RawQuery = query.Encode()
	testDB, err := sql.Open("postgres", target.String())
	if err != nil {
		t.Fatal("could not open isolated settings test schema")
	}
	testDB.SetMaxOpenConns(4)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, testDB)))
	t.Cleanup(func() { _ = client.Close() })
	repo := repository.NewSettingRepository(client)
	if err := repo.SetMultiple(ctx, nondefaultPartialSettings()); err != nil {
		t.Fatal("could not seed synthetic settings fixture")
	}
	return repo, testDB
}

func postgresPartialSettingsValues(t *testing.T, repo service.SettingRepository) map[string]string {
	t.Helper()
	values, err := repo.GetAll(context.Background())
	if err != nil {
		t.Fatal("could not read isolated settings fixture")
	}
	return values
}

func TestSettingHandlerPartialUpdatesPostgres(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method+" preserves all omitted settings", func(t *testing.T) {
			repo, _ := newPostgresPartialSettingsRepo(t)
			handler := newPartialSettingsHandler(repo)
			expected := nondefaultPartialSettings()
			for index, change := range []struct{ field, value, stored string }{
				{"site_name", `"数据库通用设置"`, "数据库通用设置"},
				{"channel_monitor_enabled", `false`, "false"},
				{"api_key_acl_trust_forwarded_ip", `false`, "false"},
				{"gateway_custom_error_message", `"数据库网关设置"`, "数据库网关设置"},
			} {
				rec := performPartialSettingsRequest(handler, method, fmt.Sprintf(`{"version":"%d",%q:%s}`, index, change.field, change.value))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				expected[change.field] = change.stored
				expected[service.SettingKeySystemSettingsVersion] = fmt.Sprint(index + 1)
				require.Equal(t, expected, postgresPartialSettingsValues(t, repo))
				require.Equal(t, fmt.Sprint(index+1), partialSettingsData(t, rec)["version"])
				require.NotContains(t, rec.Body.String(), "synthetic-")
			}
		})
	}
	t.Run("explicit false zero empty arrays and secrets", func(t *testing.T) {
		repo, _ := newPostgresPartialSettingsRepo(t)
		handler := newPartialSettingsHandler(repo)
		rec := performPartialSettingsRequest(handler, http.MethodPatch, `{"version":"0","default_balance":0,"default_user_rpm_limit":0,"default_subscriptions":[],"enable_model_fallback":false,"enable_identity_patch":false,"backend_mode_enabled":false,"allow_ungrouped_key_scheduling":false,"smtp_password":""}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		values := postgresPartialSettingsValues(t, repo)
		require.Equal(t, "0.00000000", values["default_balance"])
		require.Equal(t, "0", values["default_user_rpm_limit"])
		require.Equal(t, "[]", values["default_subscriptions"])
		for _, key := range []string{"enable_model_fallback", "enable_identity_patch", "backend_mode_enabled", "allow_ungrouped_key_scheduling"} {
			require.Equal(t, "false", values[key])
		}
		require.Equal(t, "17", values["default_concurrency"])
		require.Equal(t, "synthetic-mail-secret", values["smtp_password"])
		require.NotContains(t, rec.Body.String(), "synthetic-")
	})
	t.Run("two administrators reject stale full snapshot", func(t *testing.T) {
		repo, _ := newPostgresPartialSettingsRepo(t)
		first, second := newPartialSettingsHandler(repo), newPartialSettingsHandler(repo)
		stale := partialSettingsData(t, performPartialSettingsRequest(second, http.MethodGet, ""))
		rec := performPartialSettingsRequest(first, http.MethodPut, `{"version":"0","site_name":"first administrator"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		before := postgresPartialSettingsValues(t, repo)
		stale["gateway_custom_error_message"] = "second administrator"
		raw, err := json.Marshal(stale)
		require.NoError(t, err)
		rec = performPartialSettingsRequest(second, http.MethodPut, string(raw))
		require.Equal(t, http.StatusConflict, rec.Code)
		require.Contains(t, rec.Body.String(), "SETTINGS_VERSION_CONFLICT")
		require.Equal(t, before, postgresPartialSettingsValues(t, repo))
		rec = performPartialSettingsRequest(second, http.MethodPatch, `{"version":"1","gateway_custom_error_message":"second administrator"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "first administrator", postgresPartialSettingsValues(t, repo)["site_name"])
	})
	t.Run("write constraint failure rolls back fields and revision", func(t *testing.T) {
		repo, testDB := newPostgresPartialSettingsRepo(t)
		if _, err := testDB.Exec(`ALTER TABLE settings ADD CONSTRAINT reject_test_site CHECK (key <> 'site_name' OR value <> 'rejected-by-db')`); err != nil {
			t.Fatal("could not install synthetic database failure constraint")
		}
		before := postgresPartialSettingsValues(t, repo)
		rec := performPartialSettingsRequest(newPartialSettingsHandler(repo), http.MethodPut, `{"version":"0","site_name":"rejected-by-db","gateway_custom_error_message":"must rollback","openai_fast_policy_settings":{"version":1,"priority_detection_by_request_enabled":true,"priority_detection_by_response_enabled":false,"rules":[]}}`)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Equal(t, before, postgresPartialSettingsValues(t, repo), "事务失败不能留下字段、策略或初次修订行")
	})
	for _, startingRevision := range []string{"0", "7"} {
		t.Run("simultaneous production CAS revision "+startingRevision, func(t *testing.T) {
			repo, _ := newPostgresPartialSettingsRepo(t)
			if startingRevision != "0" {
				require.NoError(t, repo.Set(context.Background(), service.SettingKeySystemSettingsVersion, startingRevision))
			}
			gate := &postgresPartialSettingsCASGate{SettingRepository: repo, arrived: make(chan struct{}, 2), release: make(chan struct{})}
			results := make(chan *httptest.ResponseRecorder, 2)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			t.Cleanup(release)
			for _, name := range []string{"administrator-a", "administrator-b"} {
				handler := newPartialSettingsHandler(gate)
				go func(name string) {
					results <- performPartialSettingsRequest(handler, http.MethodPut, fmt.Sprintf(`{"version":%q,"site_name":%q}`, startingRevision, name))
				}(name)
			}
			for range 2 {
				select {
				case <-gate.arrived:
				case rec := <-results:
					t.Fatalf("request returned before production CAS: status=%d", rec.Code)
				case <-time.After(10 * time.Second):
					t.Fatal("both administrators must reach production CAS")
				}
			}
			release()
			codes := map[int]int{}
			for range 2 {
				select {
				case rec := <-results:
					codes[rec.Code]++
				case <-time.After(10 * time.Second):
					t.Fatal("production CAS requests did not complete")
				}
			}
			require.Equal(t, map[int]int{http.StatusOK: 1, http.StatusConflict: 1}, codes)
			values := postgresPartialSettingsValues(t, repo)
			nextRevision := "1"
			if startingRevision == "7" {
				nextRevision = "8"
			}
			require.Equal(t, nextRevision, values[service.SettingKeySystemSettingsVersion])
			require.Contains(t, []string{"administrator-a", "administrator-b"}, values["site_name"])
			require.Equal(t, "17", values["default_concurrency"])
		})
	}
}

// 只控制请求到达时机，比较、事务锁和写入全部委托给生产仓库。
type postgresPartialSettingsCASGate struct {
	service.SettingRepository
	arrived chan struct{}
	release chan struct{}
}

func (r *postgresPartialSettingsCASGate) CompareAndSwapMultiple(ctx context.Context, key, expected, next string, updates map[string]string) (bool, error) {
	r.arrived <- struct{}{}
	select {
	case <-r.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return r.SettingRepository.(service.OpenAISchedulerPolicyCASRepository).CompareAndSwapMultiple(ctx, key, expected, next, updates)
}
