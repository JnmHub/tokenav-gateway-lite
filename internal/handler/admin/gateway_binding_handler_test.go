package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type bindingAdminStore struct{ r gatewaylite.BindingRecord }

func (s *bindingAdminStore) Load(context.Context) (gatewaylite.BindingRecord, error) { return s.r, nil }
func (s *bindingAdminStore) Save(_ context.Context, r gatewaylite.BindingRecord, _ int64) error {
	s.r = r
	return nil
}
func TestGatewayBindingAdminStatusAndManagedConfigIdentity(t *testing.T) {
	s := &bindingAdminStore{r: gatewaylite.BindingRecord{Revision: 1, InstanceID: "fixture", Phase: "blocked", Current: &gatewaylite.BindingDestination{BindingID: "fixture-id", URL: "https://original.invalid", Credential: "credential-private", GatewayCode: "original", NodeScope: "original", RedisPrefix: "gl:original", AdminSyncKey: "sync-private", Region: "hk"}, HandshakeAt:1,SnapshotAt:1}}
	m, err := gatewaylite.NewBindingManager(context.Background(), s, config.GatewayLiteConfig{})
	require.NoError(t, err)
	require.NoError(t, m.Cancel(context.Background()))
	gatewaylite.SetDefaultBindingManager(m)
	t.Cleanup(func() { gatewaylite.SetDefaultBindingManager(nil) })
	cfg := &config.Config{}
	m.ApplyStartup(&cfg.GatewayLite)
	cfg.GatewayLite.ControlPlaneTimeoutMS = 300
	h := NewGatewayLiteConfigHandler(cfg)
	r := gin.New()
	r.GET("/status", NewGatewayBindingHandler().Status)
	r.PUT("/config", h.UpdateConfig)
	r.POST("/sync", h.SyncNow)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	require.NotContains(t, w.Body.String(), "credential-private")
	require.NotContains(t, w.Body.String(), "sync-private")
	changed := `{"region":"hk","gateway_code":"original","redis_prefix":"gl:original","control_plane_url":"https://other.invalid"}`
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/config", strings.NewReader(changed)))
	require.Equal(t, 409, w.Code)
	require.Equal(t, "https://original.invalid", cfg.GatewayLite.ControlPlaneURL)
	req := httptest.NewRequest("POST", "/sync", strings.NewReader(`{"control_plane_url":"https://other.invalid"}`))
	req.Header.Set("Authorization", "Bearer sync-private")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 409, w.Code)
}
func TestGatewayBindingVerifyFailureDoesNotResetAdministratorLogin(t *testing.T) {
	r := gin.New()
	r.POST("/verify", func(c *gin.Context) { bindingFailure(c, errors.New("unauthorized")) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/verify", nil))
	require.Equal(t, 400, w.Code)
	require.Contains(t, w.Body.String(), "unauthorized")
}
