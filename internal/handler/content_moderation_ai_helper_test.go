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

type aiHandlerStore struct {
	service.ContentModerationAIStore
	penalties atomic.Int64
}

func (s *aiHandlerStore) EnqueuePenalty(context.Context, gatewaylite.ContentModerationPenaltyRequest) error {
	s.penalties.Add(1)
	return nil
}

func TestRunContentModerationAIErrorNeverDispatchesAndRefundsEveryTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body     string
		providerStatus int
		key, consent   bool
	}{
		{"not configured", `{"input":"safe synthetic"}`, 200, false, true},
		{"not authorized", `{"input":"safe synthetic"}`, 200, true, false},
		{"rate limited", `{"input":"safe synthetic"}`, 429, true, true},
		{"provider failure", `{"input":"safe synthetic"}`, 503, true, true},
		{"invalid schema", `{"input":"safe synthetic"}`, 200, true, true},
		{"cancelled parent", `{"input":"safe synthetic"}`, 200, true, true},
		{"duplicate", `{"input":"safe synthetic","INPUT":"hidden"}`, 200, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var moderation, business atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/business" {
					business.Add(1)
					return
				}
				moderation.Add(1)
				w.WriteHeader(tc.providerStatus)
				_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"allow"}}]}`)
			}))
			defer provider.Close()
			cfg := service.ContentModerationConfig{Enabled: true, Mode: service.ContentModerationModePreBlock, AllGroups: true, PolicyVersion: 2, KeywordBlockingMode: service.ContentModerationKeywordModeAIOnly,
				AI: service.ContentModerationAIConfig{BaseURL: provider.URL, Model: "deepseek-flash", ExternalDataConsent: tc.consent, PenaltiesEnabled: true}}
			if tc.key {
				cfg.AI.APIKey = "synthetic-key"
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			svc := service.NewContentModerationService(&contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw)}}, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil)
			store := &aiHandlerStore{}
			svc.SetAIStore(store)
			groupID := int64(10)
			key := &service.APIKey{ID: 8, GroupID: &groupID, User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "test", Status: service.StatusActive}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
			var ids []string
			for turn := 0; turn < 2; turn++ {
				reservation := gatewaylite.NewRequestReservation(1, "lease")
				ctx := context.WithValue(context.Background(), ctxkey.RequestID, "same-client-id")
				ctx = gatewaylite.ContextWithRequestMeta(ctx, gatewaylite.RequestMeta{RequestID: "same-client-id", UserID: 7, KeyID: "stable-key", BillingSubjectType: "user", BillingSubjectID: 7, Reservation: reservation})
				if tc.name == "cancelled parent" {
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				}
				c.Request = c.Request.WithContext(ctx)
				d := runContentModeration(c, nil, svc, nil, key, middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses, "model", []byte(tc.body))
				require.NotNil(t, d)
				require.True(t, d.Blocked)
				require.False(t, d.Allowed)
				require.False(t, d.Flagged)
				require.Equal(t, "error", d.Verdict)
				require.Equal(t, "none", d.PenaltyState)
				called := tc.key && tc.consent && tc.name != "duplicate" && tc.name != "cancelled parent"
				require.Equal(t, called, d.ProviderCalled)
				if called {
					require.Equal(t, "provider", d.Phase)
				} else {
					require.Equal(t, "preparation", d.Phase)
				}
				if d.Allowed {
					resp, err := http.Get(provider.URL + "/business")
					require.NoError(t, err)
					resp.Body.Close()
				}
				_, _, refund := reservation.Snapshot()
				require.True(t, refund)
				admission, _ := c.Get("content_moderation_admission_id")
				turnValue, _ := c.Get("content_moderation_admission_turn")
				ids = append(ids, service.ContentModerationAdmissionTurnID(admission.(string), turnValue.(uint64)))
			}
			require.NotEqual(t, ids[0], ids[1])
			require.NotEqual(t, "same-client-id", ids[0])
			require.Zero(t, business.Load())
			require.Zero(t, store.penalties.Load())
			if !tc.key || !tc.consent || tc.name == "duplicate" || tc.name == "cancelled parent" {
				require.Zero(t, moderation.Load())
			} else {
				require.EqualValues(t, 2, moderation.Load(), "each error turn must be independently reviewed")
			}
		})
	}
}

func TestRunContentModerationAIOverallVerdictWithoutStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, body, verdict string
		shadow, uncovered   bool
	}{
		{name: "all text in one review", body: `{"instructions":"safe synthetic instructions","input":"safe synthetic input"}`, verdict: "allow"},
		{name: "unknown extension", body: `{"input":"safe synthetic input","vendor_extension":{"note":"unrecognized"}}`, verdict: "allow", uncovered: true},
		{name: "remote history", body: `{"input":"safe synthetic input","previous_response_id":"resp_hidden"}`, verdict: "allow", uncovered: true},
		{name: "unknown content block", body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"safe synthetic input"},{"type":"vendor_blob","opaque":"unrecognized"}]}]}`, verdict: "allow", uncovered: true},
		{name: "external image", body: `{"input":[{"role":"user","content":[{"type":"input_text","text":"safe synthetic input"},{"type":"input_image","image_url":"https://example.invalid/synthetic.png"}]}]}`, verdict: "allow", uncovered: true},
		{name: "block without penalties", body: `{"input":"violent attack synthetic input"}`, verdict: "block"},
		{name: "shadow block without penalties", body: `{"input":"violent attack synthetic input"}`, verdict: "block", shadow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
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
				if tc.name == "all text in one review" {
					require.GreaterOrEqual(t, len(input.Fragments), 2)
					require.Contains(t, parts[0].Text, "safe synthetic instructions")
				}
				categories := []string{}
				if tc.verdict == "block" {
					categories = []string{"illicit/violent"}
				}
				result, err := json.Marshal(map[string]any{"policy_version": "tokenav-text-v1", "verdict": tc.verdict, "categories": categories, "reason": "synthetic overall review"})
				require.NoError(t, err)
				response := map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(result)}}}}
				// Compatible providers may omit model/usage or report their upstream
				// model alias; neither changes a valid overall policy verdict.
				if call == 2 {
					response["model"] = "provider-upstream-alias"
				}
				require.NoError(t, json.NewEncoder(w).Encode(response))
			}))
			defer provider.Close()
			mode := service.ContentModerationModePreBlock
			if tc.shadow {
				mode = service.ContentModerationModeShadow
			}
			cfg := service.ContentModerationConfig{Enabled: true, Mode: mode, AllGroups: true, PolicyVersion: 2, KeywordBlockingMode: service.ContentModerationKeywordModeAIOnly,
				AI: service.ContentModerationAIConfig{BaseURL: provider.URL, Model: "deepseek-flash", APIKey: "synthetic-key", ExternalDataConsent: true, PenaltiesEnabled: tc.verdict == "allow" || tc.shadow}}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			// Ordinary reviews and shadow decisions must work without a SQL
			// penalty store; only a real punishment needs durable storage.
			svc := service.NewContentModerationService(&contentModerationHandlerSettingRepo{values: map[string]string{service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw)}}, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil)
			groupID := int64(10)
			key := &service.APIKey{ID: 8, GroupID: &groupID, User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Status: service.StatusActive}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			for turn := 0; turn < 2; turn++ {
				reservation := gatewaylite.NewRequestReservation(1, "synthetic-lease")
				ctx := gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{RequestID: "synthetic-request", UserID: 7, KeyID: "stable-key", BillingSubjectType: "user", BillingSubjectID: 7, Reservation: reservation})
				c.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(tc.body)).WithContext(ctx)
				d := runContentModeration(c, nil, svc, nil, key, middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses, "business-model", []byte(tc.body))
				require.NotNil(t, d)
				require.Equal(t, tc.verdict, d.Verdict)
				require.Equal(t, tc.verdict == "allow" || tc.shadow, d.Allowed)
				require.Equal(t, tc.verdict == "block" && !tc.shadow, d.Blocked)
				require.Equal(t, tc.verdict == "block" && !tc.shadow, d.Flagged)
				require.Equal(t, "synthetic overall review", d.Reason)
				require.Equal(t, "none", d.PenaltyState)
				require.True(t, d.ProviderCalled)
				require.GreaterOrEqual(t, d.ProviderLatencyMS, int64(0))
				if tc.verdict == "block" {
					require.Equal(t, []string{"illicit/violent"}, d.Categories)
				} else {
					require.Empty(t, d.Categories)
				}
				if tc.uncovered {
					require.NotEmpty(t, d.Coverage.UncoveredFields)
				}
				require.Equal(t, tc.body, string(contentModerationForwardBody(c, []byte(tc.body))))
				_, _, refund := reservation.Snapshot()
				require.Equal(t, tc.verdict == "block" && !tc.shadow, refund)
				encoded, err := json.Marshal(d)
				require.NoError(t, err)
				var response map[string]any
				require.NoError(t, json.Unmarshal(encoded, &response))
				for _, removed := range []string{"estimated_cost_usd_micros", "cache_hit", "fragments"} {
					require.NotContains(t, response, removed)
				}
			}
			require.EqualValues(t, 2, calls.Load(), "each turn makes one review call without retries or cached verdicts")
		})
	}
}

func TestRunContentModerationAIServerAdmissionRejectsClientReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var serverIDs []string
	router := gin.New()
	router.Use(middleware.RequestLogger())
	router.POST("/admit", func(c *gin.Context) {
		id, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
		serverIDs = append(serverIDs, id)
		client, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		require.Equal(t, "reused-client-id", client)
		c.Status(204)
	})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/admit", nil)
		r.Header.Set("X-Request-ID", "reused-client-id")
		router.ServeHTTP(w, r)
		require.Equal(t, 204, w.Code)
	}
	require.NotEqual(t, serverIDs[0], serverIDs[1])
	require.NotEqual(t, "reused-client-id", serverIDs[0])
}
