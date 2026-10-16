package repository

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLockAndMergeAccountExtraPreservesLatestCodexTicket(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectQuery(`(?s)SELECT.*FOR NO KEY UPDATE`).
		WithArgs(int64(41), service.PlatformOpenAI, service.AccountTypeOAuth, `{"access_token":"test"}`, nil).
		WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "ollama_group_unchanged", "ollama_proxy_unchanged", "enabled", "rate_sync_enabled", "snapshot", "ollama_session", "ollama_auto", "current_extra"}).
			AddRow(true, false, true, nil, nil, nil, nil, nil, []byte(`{"codex_turn_ticket:model":{"state":"latest-database-ticket"},"codex_route_cookies":{"route":"latest-server"},"old_admin_setting":true}`)))
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}, Extra: map[string]any{"codex_turn_ticket:model": map[string]any{"state": "stale"}, "codex_turn_ticket:injected": map[string]any{"state": "spoofed"}, "codex_route_cookies": map[string]any{"route": "injected"}, "new_admin_setting": true}}
	extra, err := lockAndMergeAccountProbeExtra(context.Background(), client, account, nil, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"state": "latest-database-ticket"}, extra["codex_turn_ticket:model"])
	require.Equal(t, map[string]any{"route": "latest-server"}, extra["codex_route_cookies"])
	require.NotContains(t, extra, "codex_turn_ticket:injected")
	require.NotContains(t, extra, "old_admin_setting")
	require.Equal(t, true, extra["new_admin_setting"])
	require.NoError(t, mock.ExpectationsWereMet())
}

// 这条路径覆盖所有平台的账号更新：extra 若是非对象 JSON，解析失败不得让整个
// 账号编辑失败，只降级放弃「保留库内门票」这一件事。
func TestLockAndMergeAccountExtraDegradesOnUnparsableExtra(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`[1,2,3]`), []byte(`"not-an-object"`), []byte(`null`), nil} {
		t.Run(string(raw), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectQuery(`(?s)SELECT.*FOR NO KEY UPDATE`).
				WithArgs(int64(41), service.PlatformOpenAI, service.AccountTypeOAuth, `{"access_token":"test"}`, nil).
				WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "ollama_group_unchanged", "ollama_proxy_unchanged", "enabled", "rate_sync_enabled", "snapshot", "ollama_session", "ollama_auto", "current_extra"}).
					AddRow(true, false, true, nil, nil, nil, nil, nil, raw))
			account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "test"}, Extra: map[string]any{"codex_turn_ticket:injected": map[string]any{"state": "spoofed"}, "codex_route_cookies": map[string]any{"route": "injected"}, "new_admin_setting": true}}
			extra, err := lockAndMergeAccountProbeExtra(context.Background(), client, account, nil, nil)
			require.NoError(t, err, "unparsable extra must not fail the account update")
			require.Equal(t, true, extra["new_admin_setting"])
			// 库内不是对象时仍要剥离全部管理端伪造的托管字段。
			require.NotContains(t, extra, "codex_turn_ticket:injected")
			require.NotContains(t, extra, "codex_route_cookies")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCodexTicketExtraIsSchedulerNeutral(t *testing.T) {
	require.True(t, isSchedulerNeutralExtraKey("codex_turn_ticket:gpt-6-astra"))
	// 门票续期不再开事务发 bucket 重建事件（快照仍由 UpdateExtra 单独同步）。
	require.False(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "x"},
	}))
	// 但门票不得掩盖同批写入的真实调度变更。
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{
		"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "x"},
		"openai_passthrough":            true,
	}))
}
