package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type aiRouteSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *aiRouteSettings) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		if value, ok := s.values[k]; ok {
			out[k] = value
		}
	}
	return out, nil
}
func (s *aiRouteSettings) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

type aiRouteStore struct {
	penalties atomic.Int64
}

func (s *aiRouteStore) EnqueuePenalty(context.Context, gatewaylite.ContentModerationPenaltyRequest) error {
	s.penalties.Add(1)
	return nil
}
func (*aiRouteStore) ClaimPenalty(context.Context, time.Time) (*service.ContentModerationAIPenaltyEvent, error) {
	return nil, nil
}
func (*aiRouteStore) FinishPenalty(context.Context, service.ContentModerationAIPenaltyEvent, error, bool) error {
	return nil
}
func (s *aiRouteStore) Stats(context.Context, string, time.Time) (service.ContentModerationAIStoreStats, error) {
	return service.ContentModerationAIStoreStats{PenaltyPending: s.penalties.Load()}, nil
}

type aiRouteUnreadBody struct{ reads atomic.Int64 }

func (b *aiRouteUnreadBody) Read([]byte) (int, error) { b.reads.Add(1); return 0, io.EOF }
func (b *aiRouteUnreadBody) Close() error             { return nil }

type aiModerationRouteCase struct{ method, path, protocol string }

func aiModerationUnsupportedRoutes() []aiModerationRouteCase {
	routes := []aiModerationRouteCase{
		{http.MethodPost, "/v1/live", "openai_live"},
		{http.MethodGet, "/v1/live/call-synthetic", "openai_live"},
		{http.MethodPost, "/backend-api/codex/realtime/calls", "openai_live"},
		{http.MethodGet, "/backend-api/codex/call-synthetic", "openai_live"},
	}
	for _, prefix := range []string{"/v1", ""} {
		routes = append(routes,
			aiModerationRouteCase{http.MethodPost, prefix + "/embeddings", "openai_embeddings"},
			aiModerationRouteCase{http.MethodPost, prefix + "/stt", "grok_audio_input"},
			aiModerationRouteCase{http.MethodPost, prefix + "/custom-voices", "grok_audio_input"},
			aiModerationRouteCase{http.MethodPatch, prefix + "/custom-voices/voice-synthetic", "grok_audio_input"},
			aiModerationRouteCase{http.MethodGet, prefix + "/realtime", "grok_audio_input"})
		for _, action := range []string{"", "/generations", "/edits", "/extensions"} {
			routes = append(routes, aiModerationRouteCase{http.MethodPost, prefix + "/videos" + action, "grok_video"})
		}
	}
	for _, prefix := range []string{"/v1", "/api/v3", "/v3", ""} {
		routes = append(routes, aiModerationRouteCase{http.MethodPost, prefix + "/contents/generations/tasks", "seedance_video"})
	}
	return routes
}

func aiModerationRegisteredRouter(t *testing.T, engine, mode, providerURL string, store *aiRouteStore, platform string) *gin.Engine {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeGatewayLite, Gateway: config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024}}
	moderation := service.ContentModerationConfig{Enabled: true, Mode: mode, KeywordBlockingMode: engine, PolicyVersion: 2, AllGroups: true,
		AI: service.ContentModerationAIConfig{APIKey: "synthetic-key", BaseURL: providerURL, Model: "deepseek-flash", ExternalDataConsent: true, PenaltiesEnabled: true}}
	raw, err := json.Marshal(moderation)
	require.NoError(t, err)
	svc := service.NewContentModerationService(&aiRouteSettings{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw)}}, nil, nil, nil, nil, nil, nil)
	svc.SetAIStore(store)
	openai := handler.NewOpenAIGatewayHandler(nil, nil, nil, nil, nil, nil, svc, nil, nil, cfg)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(servermiddleware.RequestLogger())
	RegisterGatewayRoutes(router, &handler.Handlers{OpenAIGateway: openai, Gateway: &handler.GatewayHandler{}}, servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		group := int64(10)
		c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{ID: 8, GroupID: &group, Group: &service.Group{ID: group, Platform: platform}})
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 7})
		c.Next()
	}), nil, nil, nil, nil, nil, cfg)
	return router
}

func TestGatewayAIModerationRegisteredUnsupportedRoutesNeverDispatch(t *testing.T) {
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { providerCalls.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	for _, mode := range []string{"pre_block", "shadow"} {
		t.Run(mode, func(t *testing.T) {
			store := &aiRouteStore{}
			router := aiModerationRegisteredRouter(t, "ai_only", mode, provider.URL, store, service.PlatformAnthropic)
			for _, route := range aiModerationUnsupportedRoutes() {
				t.Run(route.method+route.path, func(t *testing.T) {
					body := &aiRouteUnreadBody{}
					r := httptest.NewRequest(route.method, route.path, nil)
					r.Body = body
					r.Header.Set("Content-Type", "application/json")
					r.Header.Set("X-Request-ID", "client-repeat")
					reservation := gatewaylite.NewRequestReservation(2, "synthetic-lease")
					r = r.WithContext(gatewaylite.ContextWithRequestMeta(r.Context(), gatewaylite.RequestMeta{RequestID: "server-reservation", UserID: 7, Reservation: reservation}))
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					require.Equal(t, http.StatusServiceUnavailable, w.Code)
					var response map[string]any
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
					require.Equal(t, "ai_unsupported_protocol", response["error"].(map[string]any)["code"])
					require.Equal(t, "error", response["verdict"])
					require.Equal(t, false, response["allowed"])
					require.Equal(t, true, response["blocked"])
					require.Equal(t, false, response["flagged"])
					require.Equal(t, "none", response["penalty_state"])
					require.Equal(t, mode == "shadow", response["shadow"])
					require.True(t, strings.HasPrefix(response["turn_id"].(string), "cm:"))
					require.NotEqual(t, "client-repeat", response["request_id"])
					_, _, refund := reservation.Snapshot()
					require.True(t, refund)
					require.Zero(t, body.reads.Load(), "route gate must reject before private body parsing")
				})
			}
			require.Zero(t, providerCalls.Load(), "no review or business request can reach the configured HTTP endpoint")
			require.Zero(t, store.penalties.Load())
		})
	}
}

func TestGatewayAIModerationInactiveRoutesAndReadOnlyTasksKeepBehavior(t *testing.T) {
	store := &aiRouteStore{}
	for _, tc := range []struct{ engine, mode string }{{"keyword_only", "pre_block"}, {"ai_only", "off"}} {
		router := aiModerationRegisteredRouter(t, tc.engine, tc.mode, "http://127.0.0.1:1", store, service.PlatformAnthropic)
		for _, prefix := range []string{"/v1", ""} {
			for _, path := range []string{"/embeddings", "/images/generations", "/videos", "/videos/generations", "/videos/edits", "/videos/extensions"} {
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, prefix+path, nil))
				require.Equal(t, http.StatusNotFound, w.Code, tc.engine+prefix+path)
				require.NotContains(t, w.Body.String(), "ai_unsupported_protocol")
			}
		}
	}
	router := aiModerationRegisteredRouter(t, "ai_only", "pre_block", "http://127.0.0.1:1", store, service.PlatformAnthropic)
	for _, prefix := range []string{"/v1", ""} {
		for _, route := range []aiModerationRouteCase{{http.MethodGet, prefix + "/images/tasks", ""}, {http.MethodGet, prefix + "/images/tasks/task-synthetic", ""}, {http.MethodPost, prefix + "/images/tasks/task-synthetic/cancel", ""}, {http.MethodGet, prefix + "/videos/task-synthetic", ""}, {http.MethodGet, prefix + "/videos/task-synthetic/content", ""}} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			require.NotContains(t, w.Body.String(), "ai_unsupported_protocol", route.path)
			require.NotContains(t, w.Body.String(), `"verdict":"error"`, route.path)
		}
	}
	for _, prefix := range []string{"/v1", "/api/v3", "/v3", ""} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(method, prefix+"/contents/generations/tasks/task-synthetic", nil))
			require.NotContains(t, w.Body.String(), "ai_unsupported_protocol")
		}
	}
	require.Zero(t, store.penalties.Load())
}
