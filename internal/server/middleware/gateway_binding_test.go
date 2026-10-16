package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type bindingAdmissionStore struct{ record gatewaylite.BindingRecord }

func (s *bindingAdmissionStore) Load(context.Context) (gatewaylite.BindingRecord, error) {
	return s.record, nil
}
func (s *bindingAdmissionStore) Save(_ context.Context, r gatewaylite.BindingRecord, _ int64) error {
	s.record = r
	return nil
}
func bindingAdmissionManager(t *testing.T) *gatewaylite.BindingManager {
	t.Helper()
	s := &bindingAdmissionStore{record: gatewaylite.BindingRecord{Revision: 1, InstanceID: "fixture", Phase: "blocked", Current: &gatewaylite.BindingDestination{BindingID: "old", URL: "https://old.invalid", GatewayCode: "old", Credential: "private"}, HandshakeAt: 1, SnapshotAt: 1}}
	m, err := gatewaylite.NewBindingManager(context.Background(), s, config.GatewayLiteConfig{})
	require.NoError(t, err)
	require.NoError(t, m.Cancel(context.Background()))
	gatewaylite.SetDefaultBindingManager(m)
	t.Cleanup(func() { gatewaylite.SetDefaultBindingManager(nil) })
	return m
}
func TestBindingAdmissionAliasSeedanceAndWebSocketRoutes(t *testing.T) {
	paths := []string{"/v1/responses", "/responses", "/responses/", "/backend-api/codex/responses", "/v1/messages", "/antigravity/v1/messages", "/antigravity/v1beta/models/m:generateContent", "/backend-api/codex/realtime/calls", "/chat/completions", "/v1beta/models/m:generateContent", "/api/v3/contents/generations/tasks", "/v3/contents/generations/tasks", "/contents/generations/tasks"}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			m := bindingAdmissionManager(t)
			_, err := m.Disconnect(context.Background())
			require.Error(t, err)
			r := gin.New()
			r.Use(GatewayBindingAdmission())
			r.POST(path, func(c *gin.Context) { t.Fatal("paid alias bypassed drain") })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
			require.Equal(t, 503, w.Code)
		})
	}
	for _, path := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses", "/v1/realtime"} {
		t.Run("ws"+path, func(t *testing.T) {
			m := bindingAdmissionManager(t)
			_, err := m.Disconnect(context.Background())
			require.Error(t, err)
			r := gin.New()
			r.Use(GatewayBindingAdmission())
			r.GET(path, func(c *gin.Context) { t.Fatal("ws handshake bypassed drain") })
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Upgrade", "websocket")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, 503, w.Code)
		})
	}
}
func TestBindingUnauthenticatedInvalidOrDisabledSeedanceDoesNotTaint(t *testing.T) {
	for _, status := range []int{401, 400, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			m := bindingAdmissionManager(t)
			r := gin.New()
			r.Use(GatewayBindingAdmission())
			r.POST("/api/v3/contents/generations/tasks", func(c *gin.Context) { c.AbortWithStatus(status) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("POST", "/api/v3/contents/generations/tasks", strings.NewReader("invalid payload")))
			require.Equal(t, status, w.Code)
			require.False(t, m.Record().MediaUnknown)
			require.Equal(t, "bound", m.Status().Phase)
		})
	}
}
func TestBindingAdmissionRealSocketKeepsInflightAndRejectsNextTurn1013(t *testing.T) {
	m := bindingAdmissionManager(t)
	r := gin.New()
	r.Use(GatewayBindingAdmission())
	entered := make(chan struct{})
	finished := make(chan struct{})
	calls := 0
	r.GET("/responses", func(c *gin.Context) {
		defer close(finished)
		conn, err := coderws.Accept(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := gatewaylite.ContextWithTurnAdmission(c.Request.Context(), func(ctx context.Context, id, model string) (gatewaylite.RequestMeta, error) {
			calls++
			return gatewaylite.RequestMeta{RequestID: id, UserID: 1, GatewayID: "old", Region: "old"}, nil
		})
		close(entered)
		for {
			_, _, err := conn.Read(c.Request.Context())
			if err != nil {
				return
			}
			admitted, err := gatewaylite.AdmitFundingTurn(ctx, "turn", "m")
			if err != nil {
				_ = conn.Close(coderws.StatusTryAgainLater, err.Error())
				return
			}
			meta, _ := gatewaylite.RequestMetaFromContext(admitted)
			raw, _ := json.Marshal(meta.GatewayID)
			_ = conn.Write(c.Request.Context(), coderws.MessageText, raw)
		}
	})
	server := httptest.NewServer(r)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/responses", nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	<-entered
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("first")))
	_, body, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, `"old"`, string(body))
	_, err = m.Disconnect(ctx)
	require.Error(t, err)
	_, ok := m.EnterRequest(ctx, false)
	require.False(t, ok)
	require.NoError(t, m.Cancel(ctx))
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("after cancel")))
	_, _, err = conn.Read(ctx)
	require.NoError(t, err)
	_, err = m.Disconnect(ctx)
	require.Error(t, err)
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("blocked turn")))
	_, _, err = conn.Read(ctx)
	require.Error(t, err)
	require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
	<-finished
	require.Equal(t, 2, calls)
}
func TestBindingTurnGuardRetainsFundingContextAndManagerGeneration(t *testing.T) {
	m := bindingAdmissionManager(t)
	base := context.WithValue(context.Background(), "fixture", "original")
	ctx := gatewaylite.ContextWithBindingTurnGuard(base, m)
	ctx = gatewaylite.ContextWithTurnAdmission(ctx, func(ctx context.Context, id, model string) (gatewaylite.RequestMeta, error) {
		require.Equal(t, "original", ctx.Value("fixture"))
		return gatewaylite.RequestMeta{RequestID: id, UserID: 1, GatewayID: "old"}, nil
	})
	gatewaylite.SetDefaultBindingManager(nil)
	admitted, err := gatewaylite.AdmitFundingTurn(ctx, "one", "m")
	require.NoError(t, err)
	meta, _ := gatewaylite.RequestMetaFromContext(admitted)
	require.Equal(t, "old", meta.GatewayID)
	_, err = m.Disconnect(context.Background())
	require.Error(t, err)
	_, err = gatewaylite.AdmitFundingTurn(ctx, "two", "m")
	require.True(t, errors.Is(err, gatewaylite.ErrBindingDraining))
}
