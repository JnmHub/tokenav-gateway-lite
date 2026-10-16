package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type aiWSRejectionSettings struct{ SettingRepository }
type aiWSRejectionRepo struct{ ContentModerationRepository }

// A frozen runtime snapshot and a real bounded audit queue exercise local
// admission without starting the production workers (which have no Stop API).
func newAIWSRejectionService(cfg *ContentModerationConfig, risk bool) *ContentModerationService {
	s := &ContentModerationService{settingRepo: &aiWSRejectionSettings{}, repo: &aiWSRejectionRepo{},
		asyncQueue: make(chan contentModerationTask, 32), runtimeCacheTTL: time.Hour}
	s.runtimeSnapshot.Store(&contentModerationRuntimeSnapshot{riskControlEnabled: risk, config: cfg,
		loadedAt: time.Now(), catalogDigest: s.keywordCatalogDigest()})
	return s
}

func TestContentModerationAIWSStrictRejectionClassification(t *testing.T) {
	var external atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { external.Add(1); w.WriteHeader(500) }))
	defer provider.Close()
	cfg := aiTestConfig(provider.URL, ContentModerationAIAdapterChat)
	cfg.AllGroups, cfg.AI.PenaltiesEnabled = true, true
	svc := newAIWSRejectionService(cfg, true)
	store := &aiTestStore{}
	svc.SetAIStore(store)
	cases := []struct {
		name, extra, code string
		extracted         bool
	}{
		{"history", `,"previous_response_id":"resp_hidden"`, "ai_strict_frame_rejected", true},
		{"conversation", `,"conversation":"hidden"`, "ai_strict_frame_rejected", true},
		{"prompt reference", `,"prompt":{"id":"hidden"}`, "ai_strict_frame_rejected", true},
		// 可提取但未覆盖的字段不能推翻传输层的拒绝；格式错误仍保留具体分类。
		// 未知字段本身不触发资金帧拒绝；用 background 保持“传输已拒绝”的真实前提。
		{"unknown field on rejected frame", `,"customer_extension":"hidden","background":true`, "ai_strict_frame_rejected", true},
		{"invalid envelope", `,"event_id":{}`, "ai_invalid_ws_envelope", false},
		{"duplicate", `,"INPUT":"hidden"`, "ai_duplicate_json_key", false},
		{"canonical but strict background", `,"background":true`, "ai_strict_frame_rejected", true},
		{"canonical cannot authorize", "", "ai_strict_frame_rejected", true},
	}
	// 超过列表摘要长度，确保管理员日志保留全文尾部，且凭据只保留脱敏形式。
	textTail := strings.Repeat("完整上下文 ", 300) + "rejected full text tail"
	text := "private synthetic text " + cfg.AI.APIKey + " token=synthetic-customer-token " + textTail
	redactedText := "private synthetic text [REDACTED] token=[已脱敏] " + textTail
	ids := map[string]bool{}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"type":"response.create","model":"synthetic","input":%q,"max_output_tokens":32%s}`, text, tc.extra))
			input := aiTestInput(string(body))
			input.Turn, input.RequestID = uint64(index+2), "reused-client-trace"
			input.Endpoint, input.ControlPlaneKeyID = "/responses", "synthetic-key-id"
			if tc.extracted {
				_, err := canonicalContentModerationAIInput(input.Protocol, body, cfg.AI, true)
				require.NoError(t, err, "canonical extraction must not override a prior transport rejection")
				if tc.name != "canonical cannot authorize" {
					require.Error(t, validateOpenAIWSStrictClientFrame(coderws.MessageText, body, true))
				}
			}
			admissions := 0
			decision := svc.RecordAIWSStrictRejection(context.Background(), input, func() (string, uint64) {
				admissions++
				return input.AdmissionID, input.Turn
			})
			require.Equal(t, 1, admissions)
			require.NotNil(t, decision)
			require.Equal(t, tc.code, decision.ErrorCode)
			require.False(t, decision.Allowed)
			require.True(t, decision.Blocked)
			require.False(t, decision.Flagged)
			require.Equal(t, "error", decision.Action)
			require.Equal(t, "error", decision.Verdict)
			require.Equal(t, "none", decision.PenaltyState)
			require.False(t, decision.Coverage.Complete)
			require.Zero(t, decision.Coverage.ReviewedCount)
			require.False(t, decision.ProviderCalled)
			require.Equal(t, "preparation", decision.Phase)
			require.Zero(t, decision.ProviderLatencyMS)
			require.Len(t, svc.asyncQueue, 1)
			task := <-svc.asyncQueue
			id := ContentModerationAdmissionTurnID(input.AdmissionID, input.Turn)
			require.Equal(t, id, task.log.RequestID)
			require.Equal(t, id, task.input.RequestID)
			require.False(t, ids[id])
			ids[id] = true
			require.NotContains(t, id, input.RequestID)
			require.Empty(t, task.input.Body, "queued audit cannot retain rejected input")
			require.Empty(t, task.content.Segments)
			if tc.extracted {
				require.Contains(t, task.log.FullInputText, redactedText)
				require.Contains(t, task.log.InputExcerpt, "private synthetic text [REDACTED] token=[已脱敏]")
				require.NotContains(t, task.log.InputExcerpt, "rejected full text tail")
				require.Greater(t, len(task.log.FullInputText), len(task.log.InputExcerpt))
			} else {
				// 信封或 JSON 解析失败时不能把原始帧伪装成已抽取的文本。
				require.Empty(t, task.log.FullInputText)
				require.Empty(t, task.log.InputExcerpt)
			}
			require.Empty(t, task.log.ReviewTextExcerpt, "rejected text was never submitted to the provider")
			require.Nil(t, task.log.UpstreamLatencyMS)
			require.Equal(t, false, task.log.AIReviewDetail["provider_called"])
			require.Equal(t, "preparation", task.log.AIReviewDetail["phase"])
			require.Equal(t, tc.code, task.log.AIReviewDetail["error_code"])
			require.Equal(t, decision.InputHash, task.log.AIReviewDetail["input_hash"])
			require.Equal(t, decision.Coverage, task.log.AIReviewDetail["coverage"])
			require.Equal(t, cfg.AI.Model, task.log.AIReviewDetail["model"])
			require.Equal(t, cfg.AI.Adapter, task.log.AIReviewDetail["adapter"])
			require.Equal(t, cfg.AI.PolicyVersion, task.log.AIReviewDetail["policy_version"])
			loggedText := task.log.InputExcerpt + task.log.FullInputText + task.log.ReviewTextExcerpt + aiJSON(task.log.AIReviewDetail)
			require.NotContains(t, loggedText, cfg.AI.APIKey)
			require.NotContains(t, loggedText, "synthetic-customer-token")
			require.Empty(t, task.inputHash)
			require.False(t, task.recordHash)
			require.False(t, task.applySideEffects)
			require.False(t, task.log.Flagged)
			require.Equal(t, tc.code, task.log.Error)
			require.Equal(t, input.Endpoint, task.log.Endpoint)
			require.Equal(t, input.ControlPlaneKeyID, task.log.ControlPlaneKeyID)
			require.Equal(t, body, input.Body, "classification must not mutate caller bytes")
		})
	}
	require.Zero(t, external.Load())
	require.Empty(t, store.penalties)
	runtime := svc.aiRuntime()
	require.EqualValues(t, len(cases), runtime.checked)
	require.EqualValues(t, len(cases), runtime.errors)
	require.EqualValues(t, len(cases), runtime.productionChecked)
	require.EqualValues(t, len(cases), runtime.productionErrors)
	require.Zero(t, runtime.calls)
	require.Zero(t, runtime.active)
	require.Zero(t, runtime.allowed)
	require.Zero(t, runtime.blocked)
	require.Zero(t, runtime.productionAllowed)
	require.Zero(t, runtime.productionBlocked)
}

func TestContentModerationAIWSStrictRejectionScope(t *testing.T) {
	for _, name := range []string{"active", "shadow", "disabled", "risk off", "off", "keyword", "other group", "other model", "unknown model", "missing admission", "missing turn", "config unavailable", "nil service"} {
		t.Run(name, func(t *testing.T) {
			cfg := aiTestConfig("https://review.invalid", ContentModerationAIAdapterChat)
			cfg.AllGroups, cfg.GroupIDs = false, []int64{10}
			cfg.ModelFilter = ContentModerationModelFilter{Type: "include", Models: []string{"covered"}}
			group := int64(10)
			input := aiTestInput(`{"type":"response.create","input":"safe","previous_response_id":"resp_hidden","max_output_tokens":32}`)
			input.GroupID, input.Model = &group, "covered"
			risk, wantNil, wantAudit := true, false, true
			wantCode := "ai_strict_frame_rejected"
			switch name {
			case "shadow":
				cfg.Mode = ContentModerationModeShadow
			case "disabled":
				cfg.Enabled, wantNil, wantAudit = false, true, false
			case "risk off":
				risk, wantNil, wantAudit = false, true, false
			case "off":
				cfg.Mode, wantNil, wantAudit = ContentModerationModeOff, true, false
			case "keyword":
				cfg.KeywordBlockingMode, wantNil, wantAudit = ContentModerationKeywordModeKeywordOnly, true, false
			case "other group":
				group, wantNil, wantAudit = 11, true, false
			case "other model":
				input.Model, wantNil, wantAudit = "outside", true, false
			case "unknown model":
				input.Model = ""
			case "missing admission":
				input.AdmissionID, wantAudit, wantCode = "", false, "ai_missing_admission"
			case "missing turn":
				input.Turn, wantAudit, wantCode = 0, false, "ai_missing_admission"
			case "config unavailable", "nil service":
				wantAudit, wantCode = false, "ai_config_unavailable"
			}
			svc := newAIWSRejectionService(cfg, risk)
			store := &aiTestStore{}
			svc.SetAIStore(store)
			if name == "config unavailable" {
				svc.settingRepo = nil
			}
			if name == "nil service" {
				svc = nil
			}
			admissions := 0
			decision := svc.RecordAIWSStrictRejection(context.Background(), input, func() (string, uint64) {
				admissions++
				return input.AdmissionID, input.Turn
			})
			wantAdmissions := 1
			if wantNil || wantCode == "ai_config_unavailable" {
				wantAdmissions = 0
			}
			require.Equal(t, wantAdmissions, admissions, "only confirmed scope may allocate an identity")
			if wantNil {
				require.Nil(t, decision)
			} else {
				require.NotNil(t, decision)
				require.Equal(t, wantCode, decision.ErrorCode)
				require.False(t, decision.Allowed)
				require.True(t, decision.Blocked)
				require.False(t, decision.Flagged)
				require.Equal(t, name == "shadow", decision.Shadow)
			}
			if svc != nil {
				want := 0
				if wantAudit {
					want = 1
				}
				require.Len(t, svc.asyncQueue, want, fmt.Sprintf("unexpected audit for %s", name))
			}
			require.Empty(t, store.penalties)
		})
	}
}
