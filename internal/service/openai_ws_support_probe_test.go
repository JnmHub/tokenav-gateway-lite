package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// wsProbeTestRepo 只覆写探测链路用到的方法，其余沿用 nil 接口嵌入。
type wsProbeTestRepo struct {
	stubOpenAIAccountRepo
	extraUpdates   []map[string]any
	accountByID    map[int64]*Account
	listByPlatform []Account
	listedByCalls  int
	lastListedPlat string
}

func (r *wsProbeTestRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	copied := make(map[string]any, len(updates))
	for k, v := range updates {
		copied[k] = v
	}
	r.extraUpdates = append(r.extraUpdates, copied)
	return nil
}

func (r *wsProbeTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if acc, ok := r.accountByID[id]; ok {
		return acc, nil
	}
	return nil, ErrAccountNotFound
}

func (r *wsProbeTestRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	r.listedByCalls++
	r.lastListedPlat = platform
	return r.listByPlatform, nil
}

func newWSProbeTestService(repo *wsProbeTestRepo, baseURL string) *AccountTestService {
	// 关闭 URL 白名单并允许 http，单测上游是本地 httptest 服务器。
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	return &AccountTestService{accountRepo: repo, cfg: cfg}
}

func newWSProbeAPIKeyAccount(id int64, baseURL string) *Account {
	return &Account{
		ID:       id,
		Name:     "ws-probe-test",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Status:   StatusActive,
		Credentials: map[string]any{
			"api_key":  "sk-probe-test",
			"base_url": baseURL,
		},
	}
}

func TestWSProbeResultFresh(t *testing.T) {
	now := time.Now()
	freshTS := now.Add(-1 * time.Hour).Unix()
	staleTS := now.Add(-13 * time.Hour).Unix()

	cases := []struct {
		name    string
		account *Account
		want    bool
	}{
		{"nil account", nil, false},
		{"no extra", &Account{ID: 1}, false},
		{"unknown status", &Account{ID: 1, Extra: map[string]any{
			OpenAIWSProbeStatusExtraKey:    OpenAIWSProbeStatusUnknown,
			OpenAIWSProbeCheckedAtExtraKey: float64(freshTS),
		}}, false},
		{"supported fresh", &Account{ID: 1, Extra: map[string]any{
			OpenAIWSProbeStatusExtraKey:    OpenAIWSProbeStatusSupported,
			OpenAIWSProbeCheckedAtExtraKey: float64(freshTS),
		}}, true},
		{"unsupported int64 ts fresh", &Account{ID: 1, Extra: map[string]any{
			OpenAIWSProbeStatusExtraKey:    OpenAIWSProbeStatusUnsupported,
			OpenAIWSProbeCheckedAtExtraKey: int64(freshTS),
		}}, true},
		{"supported stale", &Account{ID: 1, Extra: map[string]any{
			OpenAIWSProbeStatusExtraKey:    OpenAIWSProbeStatusSupported,
			OpenAIWSProbeCheckedAtExtraKey: float64(staleTS),
		}}, false},
		{"missing ts", &Account{ID: 1, Extra: map[string]any{
			OpenAIWSProbeStatusExtraKey: OpenAIWSProbeStatusSupported,
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, wsProbeResultFresh(tc.account, now))
		})
	}
}

func TestProbeOpenAIWSSupport_UnsupportedFromPlainHTTP404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	repo := &wsProbeTestRepo{}
	svc := newWSProbeTestService(repo, server.URL)
	account := newWSProbeAPIKeyAccount(101, server.URL)

	result := svc.probeOpenAIWSSupport(context.Background(), account)
	require.Equal(t, OpenAIWSProbeStatusUnsupported, result.Status)
	require.Contains(t, result.Detail, "handshake http 404")
	require.True(t, result.CheckedAt > 0)
}

func TestProbeOpenAIWSSupport_SupportedByRealUpgrade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, r.Header.Get("authorization"))
		require.Equal(t, openAIWSBetaV2Value, r.Header.Get("OpenAI-Beta"))
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	repo := &wsProbeTestRepo{}
	svc := newWSProbeTestService(repo, server.URL)
	account := newWSProbeAPIKeyAccount(102, server.URL)

	result := svc.probeOpenAIWSSupport(context.Background(), account)
	require.Equal(t, OpenAIWSProbeStatusSupported, result.Status)
	require.Contains(t, result.Detail, "handshake ok")
}

func TestProbeAccountWSSupport_PersistsExtraAndReturnsAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	repo := &wsProbeTestRepo{accountByID: map[int64]*Account{}}
	account := newWSProbeAPIKeyAccount(103, server.URL)
	repo.accountByID[103] = account

	svc := newWSProbeTestService(repo, server.URL)
	updated, result, err := svc.ProbeAccountWSSupport(context.Background(), 103)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, OpenAIWSProbeStatusSupported, result.Status)

	require.Len(t, repo.extraUpdates, 1)
	update := repo.extraUpdates[0]
	require.Equal(t, OpenAIWSProbeStatusSupported, update[OpenAIWSProbeStatusExtraKey])
	require.Equal(t, result.CheckedAt, update[OpenAIWSProbeCheckedAtExtraKey])
	require.NotEmpty(t, update[OpenAIWSProbeDetailExtraKey])
}

func TestProbeOpenAIWSSupport_MissingAPIKeyStaysUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &wsProbeTestRepo{}
	svc := newWSProbeTestService(repo, "")
	account := newWSProbeAPIKeyAccount(104, "")
	delete(account.Credentials, "api_key")

	result := svc.probeOpenAIWSSupport(context.Background(), account)
	require.Equal(t, OpenAIWSProbeStatusUnknown, result.Status)
	require.Contains(t, result.Detail, "no api key")
}
