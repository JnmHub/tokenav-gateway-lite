package handler

import (
	"context"
	"encoding/json"
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

type moderationWSEnvelopeStore struct {
	service.ContentModerationAIStore
	penalties atomic.Int64
}

func (s *moderationWSEnvelopeStore) EnqueuePenalty(context.Context, gatewaylite.ContentModerationPenaltyRequest) error {
	s.penalties.Add(1)
	return nil
}

func (s *moderationWSEnvelopeStore) snapshot() int64 {
	return s.penalties.Load()
}

func TestResponsesWSContentModerationEnvelopeOptIn(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer envelope-synthetic" {
			t.Error("unexpected synthetic moderation endpoint or authentication")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// 多模态报文：user content 为 parts 数组（首项为 data JSON 文本）。
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) != 2 {
			t.Error("invalid synthetic moderation request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var data string
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Messages[1].Content, &parts); err == nil && len(parts) > 0 && parts[0].Type == "text" {
			data = parts[0].Text
		} else {
			_ = json.Unmarshal(request.Messages[1].Content, &data)
		}
		var input service.ContentModerationAICanonicalInput
		if err := json.Unmarshal([]byte(data), &input); err != nil || len(input.Fragments) == 0 {
			t.Error("missing complete canonical input")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := service.ContentModerationAIResult{
			PolicyVersion: "tokenav-text-v1", Verdict: "allow", Categories: []string{}, Reason: "Synthetic complete-input review passed",
		}
		raw, _ := json.Marshal(result)
		w.Header().Set("Content-Type", "application/json")
		// 别名和缺省用量信息不能影响有效的四字段整体判定。
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "synthetic-provider-alias", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(raw)}}},
		})
	}))
	t.Cleanup(provider.Close)
	cfg := service.ContentModerationConfig{
		Enabled: true, Mode: service.ContentModerationModePreBlock, AllGroups: true, WorkerCount: 1, PolicyVersion: 2, KeywordBlockingMode: service.ContentModerationKeywordModeAIOnly,
		AI: service.ContentModerationAIConfig{
			BaseURL: provider.URL, Model: "deepseek-flash", APIKey: "envelope-synthetic", ExternalDataConsent: true,
		},
	}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	// One constructor for all turns; its existing background audit workers have
	// no Stop API. This test claims only bounded completion of its own provider.
	svc := service.NewContentModerationService(&contentModerationHandlerSettingRepo{values: map[string]string{
		service.SettingKeyRiskControlEnabled: "true", service.SettingKeyContentModerationConfig: string(raw),
	}}, &contentModerationHandlerTestRepo{}, nil, nil, nil, nil, nil)
	store := &moderationWSEnvelopeStore{}
	svc.SetAIStore(store)
	h := &OpenAIGatewayHandler{contentModerationService: svc}
	groupID := int64(10)
	key := &service.APIKey{ID: 8, GroupID: &groupID, User: &service.User{ID: 7}, Group: &service.Group{ID: groupID, Name: "synthetic", Status: service.StatusActive}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("Connection", "Upgrade")
	c.Request.Header.Set("Upgrade", "websocket")
	c.Request.Header.Set("X-Request-ID", "repeated-client-event")
	c.Request.Header.Set("X-Responses-WebSocket", "true")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "repeated-client-event"))
	body := `{"type":"response.create", "event_id":"repeated-client-event", "model":"synthetic", "input":"safe synthetic", "max_output_tokens":32}`
	plain := `{"model":"synthetic","input":"safe synthetic","max_output_tokens":32}`
	serverTurns := map[string]bool{}
	hashes := map[bool]string{}
	for _, tc := range []struct {
		name       string
		ws         bool
		body, code string
	}{
		// HTTP 中的传输字段只标为未覆盖，WS 的独立哈希域用于区分审计证据。
		{"ordinary HTTP with Upgrade header", false, body, ""},
		{"explicit WS first turn", true, body, ""},
		{"explicit WS repeated event", true, body, ""},
		{"ordinary entry after WS", false, body, ""},
		{"ordinary business HTTP", false, plain, ""},
		{"WS missing type", true, plain, "ai_invalid_ws_envelope"},
		{"WS metadata object", true, strings.Replace(body, `"event_id":"repeated-client-event"`, `"event_id":{"prompt":"hidden"}`, 1), "ai_invalid_ws_envelope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := calls.Load()
			beforePenalties := store.snapshot()
			c.Request.Method = http.MethodPost
			var decision *service.ContentModerationDecision
			if tc.ws {
				c.Request.Method = http.MethodGet
				decision = h.checkResponsesWSContentModeration(c, nil, key, middleware.AuthSubject{UserID: 7}, "synthetic", []byte(tc.body))
			} else {
				decision = h.checkContentModeration(c, nil, key, middleware.AuthSubject{UserID: 7}, service.ContentModerationProtocolOpenAIResponses, "synthetic", []byte(tc.body))
			}
			require.NotNil(t, decision)
			require.Equal(t, tc.body, string(contentModerationForwardBody(c, nil)), "the helper must not strip the forwarding envelope")
			admission, _ := c.Get("content_moderation_admission_id")
			turn, _ := c.Get("content_moderation_admission_turn")
			serverID := service.ContentModerationAdmissionTurnID(admission.(string), turn.(uint64))
			require.NotEqual(t, "repeated-client-event", admission)
			require.False(t, serverTurns[serverID], "every check must use a new server turn")
			serverTurns[serverID] = true
			require.Equal(t, beforePenalties, store.snapshot())
			if tc.code != "" {
				require.True(t, decision.Blocked)
				require.False(t, decision.Allowed)
				require.Equal(t, "error", decision.Verdict)
				require.Equal(t, tc.code, decision.ErrorCode)
				require.False(t, decision.ProviderCalled)
				require.Equal(t, "preparation", decision.Phase)
				require.False(t, decision.Coverage.Complete)
				require.Equal(t, beforeCalls, calls.Load(), "rejected envelope must not call the provider")
				return
			}
			require.True(t, decision.Allowed, "code=%s", decision.ErrorCode)
			require.False(t, decision.Blocked)
			require.True(t, decision.ProviderCalled)
			require.Equal(t, "completed", decision.Phase)
			require.Empty(t, decision.Categories)
			require.Equal(t, "Synthetic complete-input review passed", decision.Reason)
			require.True(t, decision.Coverage.Complete)
			require.Equal(t, beforeCalls+1, calls.Load(), "one provider call for this turn")
			if tc.body == body {
				if previous, ok := hashes[tc.ws]; ok {
					require.Equal(t, previous, decision.InputHash)
				}
				hashes[tc.ws] = decision.InputHash
			}
		})
	}
	require.EqualValues(t, 5, calls.Load())
	require.Len(t, serverTurns, 7)
	require.Zero(t, store.snapshot())
	require.NotEqual(t, hashes[false], hashes[true], "HTTP and WS evidence must retain separate identities")
}
