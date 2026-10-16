package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The admin contract is exercised over a real local HTTP handler with an
// explicit test-only store. No database, real credential or paid upstream.
type aiReviewAdminStore struct{ penalties atomic.Int64 }

func (s *aiReviewAdminStore) EnqueuePenalty(context.Context, gatewaylite.ContentModerationPenaltyRequest) error {
	s.penalties.Add(1)
	return nil
}
func (*aiReviewAdminStore) ClaimPenalty(context.Context, time.Time) (*service.ContentModerationAIPenaltyEvent, error) {
	return nil, nil
}
func (*aiReviewAdminStore) FinishPenalty(context.Context, service.ContentModerationAIPenaltyEvent, error, bool) error {
	return nil
}
func (*aiReviewAdminStore) Stats(context.Context, string, time.Time) (service.ContentModerationAIStoreStats, error) {
	return service.ContentModerationAIStoreStats{}, nil
}

func TestContentModerationAIAdminSavedConfigFingerprintContract(t *testing.T) {
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/responses", r.URL.Path)
		require.Equal(t, "Bearer synthetic-review-secret", r.Header.Get("Authorization"))
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"synthetic-review-secret private-provider-body"}`))
	}))
	defer provider.Close()
	settings := &aiAdminSettings{values: map[string]string{}}
	svc := service.NewContentModerationService(settings, nil, nil, nil, nil, nil, nil)
	store := &aiReviewAdminStore{}
	svc.SetAIStore(store)
	h := NewContentModerationHandler(svc)
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	r := gin.New()
	r.GET("/config", h.GetConfig)
	r.PUT("/config", h.UpdateConfig)
	r.POST("/ai/test", h.TestAI)
	r.GET("/ai/metrics", h.GetAIMetrics)
	invoke := func(method, path string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.NotContains(t, w.Body.String(), "synthetic-review-secret")
		require.NotContains(t, w.Body.String(), "private-provider-body")
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return w.Code, out
	}
	code, out := invoke("GET", "/ai/metrics", nil)
	require.Equal(t, 200, code)
	m := out["data"].(map[string]any)
	require.Equal(t, false, m["config_saved"])
	require.Equal(t, "not_tested", m["test"].(map[string]any)["status"])
	code, out = invoke("PUT", "/config", map[string]any{"ai": map[string]any{"adapter": "deepseek_responses", "base_url": provider.URL, "api_key": "synthetic-review-secret"}})
	require.Equal(t, 200, code)
	fingerprint := out["data"].(map[string]any)["ai"].(map[string]any)["config_fingerprint"].(string)
	require.NotEmpty(t, fingerprint)
	code, out = invoke("GET", "/config", nil)
	require.Equal(t, 200, code)
	require.Equal(t, fingerprint, out["data"].(map[string]any)["ai"].(map[string]any)["config_fingerprint"])
	code, out = invoke("POST", "/ai/test", map[string]string{"case_id": "allow", "config_fingerprint": "old-fingerprint"})
	require.Equal(t, 200, code)
	require.Equal(t, "ai_test_config_changed", out["data"].(map[string]any)["error_code"])
	require.Zero(t, calls.Load())
	code, out = invoke("POST", "/ai/test", map[string]string{"case_id": "allow", "config_fingerprint": fingerprint})
	require.Equal(t, 200, code)
	d := out["data"].(map[string]any)
	require.Equal(t, "error", d["verdict"])
	require.Equal(t, "ai_provider_not_found", d["error_code"])
	require.Equal(t, true, d["provider_called"])
	require.Equal(t, "provider", d["phase"])
	require.Equal(t, "none", d["penalty_state"])
	require.Equal(t, false, d["coverage"].(map[string]any)["complete"])
	require.EqualValues(t, 404, d["provider_http_status"])
	require.Equal(t, fingerprint, d["config_fingerprint"])
	require.Equal(t, "failed", d["test"].(map[string]any)["status"])
	require.EqualValues(t, 1, calls.Load())
	code, _ = invoke("POST", "/ai/test", map[string]string{"case_id": "allow", "base_url": provider.URL})
	require.Equal(t, 400, code)
	code, out = invoke("GET", "/ai/metrics", nil)
	require.Equal(t, 200, code)
	m = out["data"].(map[string]any)
	require.True(t, strings.HasSuffix(m["service_endpoint"].(string), "/responses"))
	require.Equal(t, "production_not_ready", m["production"].(map[string]any)["state"])
	require.EqualValues(t, 0, m["production_checked"])
	require.EqualValues(t, 0, m["production_errors"])
	require.EqualValues(t, 1, m["total_calls"])
	require.Equal(t, "failed", m["test"].(map[string]any)["status"])
	require.Zero(t, store.penalties.Load(), "synthetic admin checks must never enqueue penalties")
}

func TestContentModerationAIAdminTestRedactsSuccessfulReasons(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	const dedicatedKey = "synthetic-review-credential"
	const normalReason = "整体审查说明：正常文案保持原样。\n没有逐片判定或概率。"
	for _, adapter := range []string{service.ContentModerationAIAdapterChat, service.ContentModerationAIAdapterResponses} {
		for _, verdict := range []string{"allow", "block"} {
			for _, tc := range []struct{ name, reason, want string }{
				{"normal", normalReason, normalReason},
				{
					"secrets_at_both_ends", dedicatedKey + "；" + normalReason + "；api_key=fixtureNamedSecret123456；Bearer fixtureBearerSecret123456；sk-fixtureGenericLog1234567890；" + dedicatedKey,
					"[REDACTED]；" + normalReason + "；api_key=[已脱敏]；Bearer [已脱敏]；[已脱敏]；[REDACTED]",
				},
			} {
				t.Run(adapter+"/"+verdict+"/"+tc.name, func(t *testing.T) {
					categories := []string{}
					if verdict == "block" {
						categories = []string{"illicit/violent"}
					}
					result, err := json.Marshal(service.ContentModerationAIResult{PolicyVersion: "tokenav-text-v1", Verdict: verdict, Categories: categories, Reason: tc.reason})
					require.NoError(t, err)
					var calls atomic.Int64
					provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if r.Header.Get("Authorization") != "Bearer "+dedicatedKey {
							t.Error("review request did not use the dedicated credential")
							w.WriteHeader(http.StatusUnauthorized)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						var envelope map[string]any
						if adapter == service.ContentModerationAIAdapterChat {
							envelope = map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(result)}}}}
						} else {
							envelope = map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(result)}}}}}
						}
						if err := json.NewEncoder(w).Encode(envelope); err != nil {
							t.Errorf("write synthetic review response: %v", err)
						}
					}))
					t.Cleanup(provider.Close)
					config, err := json.Marshal(service.ContentModerationConfig{AI: service.ContentModerationAIConfig{Adapter: adapter, BaseURL: provider.URL, APIKey: dedicatedKey, PenaltiesEnabled: true}})
					require.NoError(t, err)
					settings := &aiAdminSettings{values: map[string]string{service.SettingKeyContentModerationConfig: string(config)}}
					svc := service.NewContentModerationService(settings, nil, nil, nil, nil, nil, nil)
					store := &aiReviewAdminStore{}
					svc.SetAIStore(store)
					router := gin.New()
					const path = "/api/v1/admin/risk-control/ai/test"
					router.POST(path, NewContentModerationHandler(svc).TestAI)
					body, err := json.Marshal(map[string]string{"case_id": verdict})
					require.NoError(t, err)
					req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, req)
					require.Equal(t, http.StatusOK, response.Code)
					// 断言真实 TestAI 的响应，而非已脱敏日志；两者不能互相替代泄漏边界。
					for _, secret := range []string{dedicatedKey, "fixtureNamedSecret123456", "fixtureBearerSecret123456", "sk-fixtureGenericLog1234567890"} {
						require.NotContains(t, response.Body.String(), secret)
					}
					var out struct {
						Code int                               `json:"code"`
						Data service.ContentModerationDecision `json:"data"`
					}
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &out))
					require.Zero(t, out.Code)
					d := out.Data
					require.Equal(t, tc.want, d.Reason)
					require.Equal(t, verdict, d.Verdict)
					require.Equal(t, verdict == "allow", d.Allowed)
					require.Equal(t, verdict == "block", d.Blocked)
					require.Equal(t, verdict == "block", d.Flagged)
					if verdict == "block" {
						require.Equal(t, categories, d.Categories)
					} else {
						require.Empty(t, d.Categories)
					}
					require.Zero(t, d.HighestScore, "categories must not be converted into probabilities")
					require.Empty(t, d.CategoryScores)
					require.Empty(t, d.ErrorCode)
					require.Equal(t, "completed", d.Phase)
					require.True(t, d.ProviderCalled)
					require.True(t, d.Coverage.Complete)
					require.NotNil(t, d.Test)
					require.Equal(t, "passed", d.Test.Status)
					require.Equal(t, "none", d.PenaltyState)
					require.Zero(t, store.penalties.Load())
					require.EqualValues(t, 1, calls.Load())
				})
			}
		}
	}
}
