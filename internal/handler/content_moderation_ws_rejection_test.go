package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Keep every audit by identity; the notification only wakes the test. No worker
// ordering or destructive FIFO reads are used to associate a connection's turn.
type moderationWSRejectionRepo struct {
	contentModerationHandlerTestRepo
	mu        sync.Mutex
	records   []service.ContentModerationLog
	changed   chan struct{}
	penalties atomic.Int64
}

func (r *moderationWSRejectionRepo) CreateLog(_ context.Context, log *service.ContentModerationLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, *log)
	close(r.changed)
	r.changed = make(chan struct{})
	return nil
}

func (r *moderationWSRejectionRepo) snapshot() ([]service.ContentModerationLog, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]service.ContentModerationLog(nil), r.records...), r.changed
}

func (r *moderationWSRejectionRepo) RecordUserPenalty(context.Context, service.ContentModerationRecordUserPenaltyInput) (*service.ContentModerationUserPenalty, error) {
	r.penalties.Add(1)
	return &service.ContentModerationUserPenalty{}, nil
}

func moderationWSRejectionConfig(url string) service.ContentModerationConfig {
	return service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, GroupIDs: []int64{10},
		WorkerCount: 1, PolicyVersion: 2, KeywordBlockingMode: service.ContentModerationKeywordModeAIOnly,
		ModelFilter: service.ContentModerationModelFilter{Type: service.ContentModerationModelFilterInclude, Models: []string{"covered"}},
		AI: service.ContentModerationAIConfig{BaseURL: url, Model: "deepseek-flash", APIKey: "synthetic-ws-rejection",
			ExternalDataConsent: true, PenaltiesEnabled: true},
	}
}

func newModerationWSRejectionHandler(t *testing.T, cfg service.ContentModerationConfig, risk bool, repo service.ContentModerationRepository) (*OpenAIGatewayHandler, *moderationWSEnvelopeStore) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	svc := service.NewContentModerationService(&contentModerationHandlerSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled: fmt.Sprint(risk), service.SettingKeyContentModerationConfig: string(raw),
	}}, repo, nil, nil, nil, nil, nil)
	store := &moderationWSEnvelopeStore{}
	svc.SetAIStore(store)
	return &OpenAIGatewayHandler{contentModerationService: svc}, store
}

type moderationWSRejectionState struct {
	admission        any
	turn             any
	requestUnchanged bool
	beforeMeta       gatewaylite.RequestMeta
	beforeMetaOK     bool
	meta             gatewaylite.RequestMeta
	metaOK           bool
	model            string
	forward          []byte
	reroute          bool
	estimated        int64
	lease            string
	refund           bool
}

func TestResponsesWSStrictRejectionFramesAndAudit(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	var external atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { external.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	repo := &moderationWSRejectionRepo{changed: make(chan struct{})}
	// Share the one real service. Its existing audit workers have no Stop API;
	// cleanup below waits only for sockets/handlers owned by this test.
	cfg := moderationWSRejectionConfig(provider.URL)
	h, store := newModerationWSRejectionHandler(t, cfg, true, repo)
	cases := []struct {
		name, extra, code string
		cancelled, closed bool
	}{
		{name: "history", extra: `,"previous_response_id":"resp_hidden"`, code: "ai_strict_frame_rejected"},
		{name: "conversation", extra: `,"conversation":"hidden"`, code: "ai_strict_frame_rejected"},
		{name: "prompt reference", extra: `,"prompt":{"id":"hidden"}`, code: "ai_strict_frame_rejected"},
		{name: "strict fallback", extra: `,"background":true`, code: "ai_strict_frame_rejected"},
		{name: "invalid envelope", extra: `,"previous_response_id":"resp_hidden","event_id":{}`, code: "ai_invalid_ws_envelope"},
		{name: "cancelled write", extra: `,"previous_response_id":"resp_hidden"`, code: "ai_strict_frame_rejected", cancelled: true},
		{name: "closed socket", extra: `,"previous_response_id":"resp_hidden"`, code: "ai_strict_frame_rejected", closed: true},
	}
	textTail := strings.Repeat("完整上下文 ", 300) + "rejected full text tail"
	text := "private synthetic text " + cfg.AI.APIKey + " token=synthetic-customer-token " + textTail
	redactedText := "private synthetic text [REDACTED] token=[已脱敏] " + textTail
	wantLogs := map[string]string{}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admission := fmt.Sprintf("server-owned-ws-rejection-%d", index)
			id := service.ContentModerationAdmissionTurnID(admission, 2)
			wantLogs[id] = tc.code
			body := []byte(fmt.Sprintf(`{"type":"response.create","model":"covered","input":%q,"max_output_tokens":32%s}`, text, tc.extra))
			reservation := gatewaylite.NewTurnRequestReservation(17, "previous-turn-lease")
			meta := gatewaylite.RequestMeta{RequestID: "business-canonical:ws-turn:1", KeyID: "synthetic-main-key", UserID: 7,
				OrganizationID: 12, BillingSubjectType: "organization", BillingSubjectID: 12, GatewayID: "synthetic-node", Region: "synthetic-region",
				Reservation: reservation, UpstreamConsumed: true, KeywordPolicyReason: "previous-turn-route"}
			states, done := make(chan moderationWSRejectionState, 1), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				requestCtx := gatewaylite.ContextWithRequestMeta(r.Context(), meta)
				requestCtx = context.WithValue(requestCtx, ctxkey.RequestedPublicModel, "previous-outside-model")
				requestCtx = context.WithValue(requestCtx, ctxkey.RequestID, "repeated-client-trace")
				c.Request = r.WithContext(requestCtx)
				originalRequest := c.Request
				c.Set("content_moderation_admission_id", admission)
				c.Set("content_moderation_admission_turn", uint64(1))
				c.Set(contentModerationForwardBodyContextKey, []byte("previous-forward-body"))
				c.Set("content_moderation_disable_keyword_reroute", true)
				writeCtx, cancelWrite := context.WithCancel(r.Context())
				defer cancelWrite()
				if tc.cancelled {
					cancelWrite()
				}
				if tc.closed {
					_ = conn.CloseNow()
				}
				group := int64(10)
				// The production context normalizes default rates and reads the lease
				// from Reservation. Compare the same accessor before and after notification.
				beforeMeta, beforeMetaOK := gatewaylite.RequestMetaFromContext(c.Request.Context())
				h.notifyResponsesWSStrictRejection(writeCtx, c, conn, &service.APIKey{ID: 8, GroupID: &group}, middleware.AuthSubject{UserID: 7}, "covered", body)
				state := moderationWSRejectionState{requestUnchanged: c.Request == originalRequest,
					beforeMeta: beforeMeta, beforeMetaOK: beforeMetaOK,
					forward: append([]byte(nil), contentModerationForwardBody(c, nil)...), reroute: c.GetBool("content_moderation_disable_keyword_reroute")}
				state.admission, _ = c.Get("content_moderation_admission_id")
				state.turn, _ = c.Get("content_moderation_admission_turn")
				state.meta, state.metaOK = gatewaylite.RequestMetaFromContext(c.Request.Context())
				state.model, _ = service.RequestedPublicModelFromContext(c.Request.Context())
				state.estimated, state.lease, state.refund = reservation.Snapshot()
				states <- state
				closeOpenAIClientWSWithContext(r.Context(), conn, coderws.StatusPolicyViolation, "original strict rejection")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
			require.NoError(t, err)
			defer func() {
				_ = client.CloseNow()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("owned handler did not stop")
				}
			}()
			if !tc.cancelled && !tc.closed {
				kind, frame, readErr := client.Read(ctx)
				require.NoError(t, readErr)
				require.Equal(t, coderws.MessageText, kind)
				require.Equal(t, "error", gjson.GetBytes(frame, "type").String())
				require.Equal(t, tc.code, gjson.GetBytes(frame, "error.code").String())
				require.False(t, gjson.GetBytes(frame, "error.retryable").Bool())
				require.NotContains(t, string(frame), "private synthetic text")
				_, _, readErr = client.Read(ctx)
				require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(readErr))
			} else {
				_, frame, readErr := client.Read(ctx)
				// An already-cancelled context can race a ready socket write. Either
				// outcome must return; the closed-socket case proves definite failure.
				if readErr == nil {
					require.True(t, tc.cancelled)
					require.Equal(t, tc.code, gjson.GetBytes(frame, "error.code").String())
					_, _, readErr = client.Read(ctx)
				}
				require.Error(t, readErr)
			}
			var state moderationWSRejectionState
			select {
			case state = <-states:
			case <-ctx.Done():
				t.Fatal("notification did not return after write attempt")
			}
			require.Equal(t, admission, state.admission)
			require.Equal(t, uint64(2), state.turn, "exactly one rejected turn identity")
			require.True(t, state.requestUnchanged)
			require.True(t, state.beforeMetaOK, "previous-turn context must exist before notification")
			require.True(t, state.metaOK, "notification must preserve previous-turn context")
			require.Equal(t, state.beforeMeta, state.meta)
			require.Equal(t, "previous-outside-model", state.model)
			require.Equal(t, "previous-forward-body", string(state.forward))
			require.True(t, state.reroute)
			require.EqualValues(t, 17, state.estimated)
			require.Equal(t, "previous-turn-lease", state.lease)
			require.False(t, state.refund)
			for {
				logs, changed := repo.snapshot()
				matches := 0
				for _, log := range logs {
					if log.RequestID != id {
						continue
					}
					matches++
					require.Equal(t, tc.code, log.Error)
					require.Equal(t, "error", log.Action)
					require.False(t, log.Flagged)
					if tc.code == "ai_invalid_ws_envelope" {
						require.Empty(t, log.InputExcerpt)
						require.Empty(t, log.FullInputText)
					} else {
						require.Contains(t, log.FullInputText, redactedText)
						require.Contains(t, log.InputExcerpt, "private synthetic text [REDACTED] token=[已脱敏]")
						require.NotContains(t, log.InputExcerpt, "rejected full text tail")
					}
					require.Empty(t, log.ReviewTextExcerpt, "strict rejection must not claim the text was sent for review")
					require.Nil(t, log.UpstreamLatencyMS)
					require.Equal(t, false, log.AIReviewDetail["provider_called"])
					require.Equal(t, "preparation", log.AIReviewDetail["phase"])
					require.Equal(t, tc.code, log.AIReviewDetail["error_code"])
					coverage, ok := log.AIReviewDetail["coverage"].(service.ContentModerationAICoverage)
					require.True(t, ok)
					require.False(t, coverage.Complete)
					require.Zero(t, coverage.ReviewedCount)
					loggedText := log.InputExcerpt + log.FullInputText + log.ReviewTextExcerpt + fmt.Sprint(log.AIReviewDetail)
					require.NotContains(t, loggedText, cfg.AI.APIKey)
					require.NotContains(t, loggedText, "synthetic-customer-token")
					require.Equal(t, "covered", log.Model)
					require.Equal(t, "/v1/responses", log.Endpoint)
					require.Equal(t, meta.KeyID, log.ControlPlaneKeyID)
					require.Equal(t, meta.OrganizationID, log.OrganizationID)
					require.NotContains(t, log.RequestID, "repeated-client-trace")
				}
				if matches > 0 {
					require.Equal(t, 1, matches)
					break
				}
				select {
				case <-changed:
				case <-ctx.Done():
					t.Fatal("missing rejected-turn audit")
				}
			}
		})
	}
	logs, _ := repo.snapshot()
	require.Len(t, logs, len(cases))
	seen := map[string]bool{}
	for _, log := range logs {
		require.False(t, seen[log.RequestID])
		seen[log.RequestID] = true
		require.Equal(t, wantLogs[log.RequestID], log.Error)
	}
	require.Zero(t, store.snapshot())
	require.Zero(t, repo.penalties.Load())
	require.Zero(t, external.Load())
}

func TestResponsesWSStrictRejectionScopeIdentity(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	for _, tc := range []struct {
		name, model, fallback, prior string
		wantTurn                     uint64
	}{
		{"active", "covered", "", "outside", 2},
		{"shadow", "covered", "", "outside", 2},
		{"disabled", "covered", "", "covered", 1},
		{"risk off", "covered", "", "covered", 1},
		{"off", "covered", "", "covered", 1},
		{"keyword", "covered", "", "covered", 1},
		{"other group", "covered", "", "covered", 1},
		{"current outside overrides prior covered", "outside", "", "covered", 1},
		{"current covered overrides prior outside", "covered", "", "outside", 2},
		{"missing uses session model", "", "covered", "outside", 2},
		{"missing keeps session outside after prior covered", "", "outside", "covered", 1},
		{"unknown remains covered", "", "", "outside", 2},
		{"nil service", "covered", "", "covered", 1},
		{"nil handler", "covered", "", "covered", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := moderationWSRejectionConfig("https://unused.invalid")
			risk, group := true, int64(10)
			switch tc.name {
			case "shadow":
				cfg.Mode = service.ContentModerationModeShadow
			case "disabled":
				cfg.Enabled = false
			case "risk off":
				risk = false
			case "off":
				cfg.Mode = service.ContentModerationModeOff
			case "keyword":
				cfg.KeywordBlockingMode = service.ContentModerationKeywordModeKeywordOnly
			case "other group":
				group = 11
			}
			// No audit repository is needed for scope checks, so no workers start.
			h, store := newModerationWSRejectionHandler(t, cfg, risk, nil)
			if tc.name == "nil service" {
				h.contentModerationService = nil
			}
			if tc.name == "nil handler" {
				h = nil
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/responses", nil)
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestedPublicModel, tc.prior))
			originalRequest := c.Request
			c.Set("content_moderation_admission_id", "server-scope-identity")
			c.Set("content_moderation_admission_turn", uint64(1))
			body := []byte(`{"type":"response.create","input":"safe","previous_response_id":"resp_hidden"}`)
			if tc.model != "" {
				body = []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":"safe","previous_response_id":"resp_hidden"}`, tc.model))
			}
			resolvedModel := tc.model
			if resolvedModel == "" {
				resolvedModel = tc.fallback
			}
			h.notifyResponsesWSStrictRejection(c.Request.Context(), c, nil, &service.APIKey{GroupID: &group}, middleware.AuthSubject{UserID: 7}, resolvedModel, body)
			admission, _ := c.Get("content_moderation_admission_id")
			turn, _ := c.Get("content_moderation_admission_turn")
			require.Equal(t, "server-scope-identity", admission)
			require.Equal(t, tc.wantTurn, turn)
			require.Same(t, originalRequest, c.Request)
			prior, _ := service.RequestedPublicModelFromContext(c.Request.Context())
			require.Equal(t, tc.prior, prior)
			require.Zero(t, store.snapshot())
		})
	}
}
