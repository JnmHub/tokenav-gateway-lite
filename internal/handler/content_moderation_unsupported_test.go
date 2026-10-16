package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type aiUnsupportedUnreadBody struct{ reads atomic.Int64 }

func (b *aiUnsupportedUnreadBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, io.EOF
}
func (b *aiUnsupportedUnreadBody) Close() error { return nil }

func TestContentModerationAIUnsupportedGateWithoutServicePreservesExistingRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &OpenAIGatewayHandler{}
	router.POST("/disabled", h.AIUnsupportedGate("openai_embeddings"), func(c *gin.Context) { c.Status(http.StatusNotFound) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/disabled", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestContentModerationAIUnsupportedGateScopeRefundAndZeroExternalWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, engine, mode                                string
		protocol, method, path                            string
		riskEnabled, enabled, groupMatches, authenticated bool
		consumed, uncertain                               bool
		modelFilter                                       service.ContentModerationModelFilter
		model                                             string
		wantStatus                                        int
		wantRefund                                        bool
	}{
		{name: "active", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "live session", protocol: "openai_live", method: "POST", path: "/v1/live", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "live sideband", protocol: "openai_live", method: "GET", path: "/v1/live/synthetic", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "codex live alias", protocol: "openai_live", method: "POST", path: "/backend-api/codex/realtime/calls", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "audio upload", protocol: "grok_audio_input", method: "POST", path: "/v1/stt", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "custom voice update", protocol: "grok_audio_input", method: "PATCH", path: "/v1/custom-voices/synthetic", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "realtime handshake", protocol: "grok_audio_input", method: "GET", path: "/v1/realtime", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "video generation", protocol: "grok_video", method: "POST", path: "/v1/videos/generations", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "video edit alias", protocol: "grok_video", method: "POST", path: "/videos/edits", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "video extension", protocol: "grok_video", method: "POST", path: "/v1/videos/extensions", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "seedance task alias", protocol: "seedance_video", method: "POST", path: "/api/v3/contents/generations/tasks", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "shadow error", engine: "ai_only", mode: "shadow", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 503, wantRefund: true},
		{name: "keyword only", engine: "keyword_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 204},
		{name: "disabled", engine: "ai_only", mode: "pre_block", riskEnabled: true, groupMatches: true, authenticated: true, wantStatus: 204},
		{name: "risk disabled", engine: "ai_only", mode: "pre_block", enabled: true, groupMatches: true, authenticated: true, wantStatus: 204},
		{name: "off", engine: "ai_only", mode: "off", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, wantStatus: 204},
		{name: "other group", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, authenticated: true, wantStatus: 204},
		{name: "already consumed", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, consumed: true, wantStatus: 503},
		{name: "uncertain persistence", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, uncertain: true, wantStatus: 503},
		{name: "unknown model remains covered", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, modelFilter: service.ContentModerationModelFilter{Type: "include", Models: []string{"covered-model"}}, wantStatus: 503, wantRefund: true},
		{name: "known out of scope model", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, authenticated: true, modelFilter: service.ContentModerationModelFilter{Type: "include", Models: []string{"covered-model"}}, model: "other-model", wantStatus: 204},
		{name: "not admitted", engine: "ai_only", mode: "pre_block", riskEnabled: true, enabled: true, groupMatches: true, wantStatus: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protocol, method, path := tc.protocol, tc.method, tc.path
			if protocol == "" {
				protocol = "openai_embeddings"
			}
			if method == "" {
				method = http.MethodPost
			}
			if path == "" {
				path = "/v1/embeddings"
			}
			var external atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { external.Add(1); w.WriteHeader(500) }))
			defer provider.Close()
			cfg := service.ContentModerationConfig{Enabled: tc.enabled, Mode: tc.mode, AllGroups: false, GroupIDs: []int64{10}, PolicyVersion: 2, KeywordBlockingMode: tc.engine, ModelFilter: tc.modelFilter,
				AI: service.ContentModerationAIConfig{BaseURL: provider.URL, Model: "deepseek-flash", APIKey: "synthetic-key", ExternalDataConsent: true, PenaltiesEnabled: true}}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			risk := "false"
			if tc.riskEnabled {
				risk = "true"
			}
			svc := service.NewContentModerationService(&contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: risk, service.SettingKeyContentModerationConfig: string(raw)}}, nil, nil, nil, nil, nil, nil)
			store := &aiHandlerStore{}
			svc.SetAIStore(store)
			h := &OpenAIGatewayHandler{contentModerationService: svc}
			router := gin.New()
			router.Use(middleware.RequestLogger())
			var business int
			router.Use(func(c *gin.Context) {
				if tc.authenticated {
					group := int64(10)
					if !tc.groupMatches {
						group = 11
					}
					c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 8, GroupID: &group})
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
				}
				c.Next()
			})
			router.Handle(method, path, h.AIUnsupportedGate(protocol), func(c *gin.Context) { business++; c.Status(204) })
			var turnIDs, serverIDs []string
			for i := 0; i < 2; i++ {
				reservation := gatewaylite.NewRequestReservation(10, "synthetic-lease")
				body := &aiUnsupportedUnreadBody{}
				r := httptest.NewRequest(method, path, nil)
				r.Body = body
				r.Header.Set("X-Request-ID", "reused-client-id")
				ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "server-reservation-id", UserID: 7, Reservation: reservation, UpstreamConsumed: tc.consumed, AsyncPersistenceUncertain: tc.uncertain})
				if tc.model != "" {
					ctx = context.WithValue(ctx, ctxkey.RequestedPublicModel, tc.model)
				}
				r = r.WithContext(ctx)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				require.Equal(t, tc.wantStatus, w.Code)
				require.Zero(t, body.reads.Load(), "unsupported gate must not consume private input")
				_, _, refund := reservation.Snapshot()
				require.Equal(t, tc.wantRefund, refund)
				if w.Code == 503 {
					var response map[string]any
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
					require.Equal(t, "error", response["verdict"])
					require.Equal(t, false, response["allowed"])
					require.Equal(t, true, response["blocked"])
					require.Equal(t, false, response["flagged"])
					require.Equal(t, "none", response["penalty_state"])
					require.Equal(t, tc.mode == "shadow", response["shadow"])
					require.Equal(t, "ai_unsupported_protocol", response["error"].(map[string]any)["code"])
					require.Equal(t, "preparation", response["phase"])
					require.NotEmpty(t, response["reason"])
					require.Equal(t, []any{}, response["categories"])
					require.Equal(t, false, response["provider_called"])
					require.EqualValues(t, 0, response["provider_latency_ms"])
					coverage := response["coverage"].(map[string]any)
					require.Equal(t, false, coverage["complete"])
					require.Equal(t, []any{"protocol:" + protocol}, coverage["uncovered_fields"])
					for _, removed := range []string{"estimated_cost_usd_micros", "cache_hit", "fragments"} {
						require.NotContains(t, response, removed)
					}
					turnIDs = append(turnIDs, response["turn_id"].(string))
					serverIDs = append(serverIDs, response["request_id"].(string))
					require.True(t, strings.HasPrefix(turnIDs[i], "cm:"))
					require.Equal(t, w.Header().Get("X-Request-ID"), serverIDs[i])
				}
			}
			if tc.wantStatus == 204 {
				require.Equal(t, 2, business)
			} else {
				require.Zero(t, business)
			}
			if len(turnIDs) == 2 {
				require.NotEqual(t, turnIDs[0], turnIDs[1])
				require.NotEqual(t, serverIDs[0], serverIDs[1])
				require.NotEqual(t, "reused-client-id", serverIDs[0])
			}
			require.Zero(t, external.Load())
			require.Zero(t, store.penalties.Load())
		})
	}
}
