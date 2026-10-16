package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// 捕获真实本地 HTTP 请求，包括错误响应前收到的正文，避免只验证日志自述已送审。
func aiLogCapturingProvider(t *testing.T, adapter string, status int, mutate func(*ContentModerationAIResult)) (*httptest.Server, <-chan []byte, *atomic.Int64) {
	t.Helper()
	provider, calls, _ := aiSyntheticProvider(t, adapter, mutate, status, 0)
	t.Cleanup(provider.Close)
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("capture review request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case captured <- raw:
		default:
			t.Error("one review sent more than one provider request")
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		provider.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, captured, calls
}

func aiLogSubmittedInput(t *testing.T, adapter string, captured <-chan []byte) (ContentModerationAICanonicalInput, []string) {
	t.Helper()
	require.Len(t, captured, 1, "the provider must receive exactly one request")
	var request map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(<-captured, &request))
	return aiTestProviderData(t, adapter, request)
}

func aiLogCheckQueued(t *testing.T, server *httptest.Server, input ContentModerationCheckInput, cfg *ContentModerationConfig) (*ContentModerationDecision, *ContentModerationLog) {
	t.Helper()
	// 不启动持久化 worker；直接消费正式入队路径，既不连接数据库，也不让日志被抢走。
	s := &ContentModerationService{
		repo:       &aiLogRepo{},
		httpClient: server.Client(),
		asyncQueue: make(chan contentModerationTask, 1),
	}
	originalBody := append([]byte(nil), input.Body...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	decision := s.checkAI(ctx, input, cfg, false)
	require.NotNil(t, decision)
	require.Equal(t, originalBody, input.Body, "logging must not clear or rewrite the business request")
	require.Len(t, s.asyncQueue, 1, "checkAI must enqueue one audit record before returning")
	task := <-s.asyncQueue
	require.NotNil(t, task.log)
	// 全文证据已在 log 中，队列不能继续持有未脱敏正文或内联图片的原始字节。
	require.Nil(t, task.input.Body, "audit queue retained the original request body")
	require.Equal(t, input.RequestID, task.input.RequestID)
	require.Equal(t, input.RequestID, task.log.RequestID)
	require.False(t, task.recordHash)
	require.False(t, task.applySideEffects)
	require.EqualValues(t, 1, s.asyncEnqueued.Load())
	require.Zero(t, s.asyncDropped.Load())
	detail := task.log.AIReviewDetail
	require.NotNil(t, detail)
	require.Equal(t, decision.Verdict, detail["verdict"])
	require.Equal(t, decision.Phase, detail["phase"])
	require.Equal(t, decision.ProviderCalled, detail["provider_called"])
	require.Equal(t, decision.ProviderHTTPStatus, detail["provider_http_status"])
	require.Equal(t, decision.InputHash, detail["input_hash"])
	require.NotEmpty(t, detail["input_hash"])
	require.Equal(t, decision.Coverage, detail["coverage"])
	require.Equal(t, cfg.AI.Adapter, detail["adapter"])
	require.Equal(t, cfg.AI.Model, detail["model"])
	require.Equal(t, cfg.AI.PolicyVersion, detail["policy_version"])
	return decision, task.log
}

func TestContentModerationAILogFullTextSurvivesProviderResult(t *testing.T) {
	longText := strings.Repeat("完整的合成审查上下文。", 500) + " forbidden synthetic 尾部必须完整保留"
	instruction, history := "合成系统说明", "合成历史答复"
	body := aiJSON(map[string]any{
		"instructions": instruction,
		"input": []any{
			map[string]string{"role": "user", "content": longText},
			map[string]string{"role": "assistant", "content": history},
		},
	})
	wantFull := "[instructions|customer_instruction] " + instruction + "\n[input/0/content|user] " + longText + "\n[input/1/content|assistant] " + history
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		for _, tc := range []struct {
			name, verdict, phase, errorCode string
			status                          int
			complete                        bool
		}{
			{"completed", "block", "completed", "", http.StatusOK, true},
			{"provider unavailable", "error", "provider", "ai_provider_http_error", http.StatusServiceUnavailable, false},
			{"provider rate limited", "error", "provider", "ai_provider_rate_limit", http.StatusTooManyRequests, false},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				server, captured, calls := aiLogCapturingProvider(t, adapter, tc.status, nil)
				cfg := aiTestConfig(server.URL, adapter)
				d, log := aiLogCheckQueued(t, server, aiTestInput(body), cfg)
				require.Equal(t, tc.verdict, d.Verdict)
				require.Equal(t, tc.phase, d.Phase)
				require.True(t, d.ProviderCalled)
				require.Equal(t, tc.status, d.ProviderHTTPStatus)
				require.Equal(t, tc.errorCode, log.Error)
				require.Equal(t, tc.errorCode, log.AIReviewDetail["error_code"])
				require.EqualValues(t, 1, calls.Load())
				require.Equal(t, wantFull, log.FullInputText)
				require.Equal(t, wantFull, log.ReviewTextExcerpt, "provider errors must not erase submitted evidence")
				require.Greater(t, utf8.RuneCountInString(log.FullInputText), 4000)
				require.Equal(t, string([]rune(wantFull)[:maxModerationExcerptRunes]), log.InputExcerpt)
				require.NotContains(t, log.InputExcerpt, "尾部必须完整保留")
				require.NotNil(t, log.UpstreamLatencyMS, "a zero-millisecond provider call still has latency evidence")
				require.EqualValues(t, d.ProviderLatencyMS, *log.UpstreamLatencyMS)
				require.Equal(t, tc.complete, d.Coverage.Complete)
				require.Equal(t, 3, d.Coverage.FragmentCount)
				require.Equal(t, utf8.RuneCountInString(instruction+longText+history), d.Coverage.TextRunes)
				if tc.complete {
					require.Equal(t, 3, d.Coverage.ReviewedCount)
					require.True(t, log.Flagged)
				} else {
					require.Zero(t, d.Coverage.ReviewedCount)
					require.False(t, log.Flagged, "provider failure is not a policy violation")
				}
				submitted, images := aiLogSubmittedInput(t, adapter, captured)
				require.Empty(t, images)
				require.Equal(t, d.InputHash, submitted.InputHash)
				require.Len(t, submitted.Fragments, 3)
				for i, want := range []string{instruction, longText, history} {
					require.Equal(t, want, submitted.Fragments[i].Text, "fragment %d was truncated before review", i)
				}
			})
		}
	}
}

func TestContentModerationAILogMissingKeyKeepsExtractedTextOnly(t *testing.T) {
	server, captured, calls := aiLogCapturingProvider(t, ContentModerationAIAdapterChat, http.StatusOK, nil)
	cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
	cfg.AI.APIKey = ""
	text := strings.Repeat("未配置密钥时仍需保留完整证据。", 400) + "准备阶段尾部"
	input := aiTestInput(aiJSON(map[string]string{"input": text, "previous_response_id": "synthetic-history"}))
	d, log := aiLogCheckQueued(t, server, input, cfg)
	require.Equal(t, "error", d.Verdict)
	require.Equal(t, "preparation", d.Phase)
	require.Equal(t, "ai_not_configured", log.Error)
	require.False(t, d.ProviderCalled)
	require.Zero(t, d.ProviderHTTPStatus)
	require.Zero(t, calls.Load())
	require.Empty(t, captured)
	require.Equal(t, "[input|user] "+text, log.FullInputText)
	require.Empty(t, log.ReviewTextExcerpt, "extraction does not mean the provider received the text")
	require.Nil(t, log.UpstreamLatencyMS)
	require.False(t, log.Flagged)
	require.False(t, d.Coverage.Complete)
	require.Zero(t, d.Coverage.ReviewedCount)
	require.Equal(t, 1, d.Coverage.FragmentCount)
	require.Equal(t, utf8.RuneCountInString(text), d.Coverage.TextRunes)
	require.Equal(t, []string{"history:previous_response_id"}, log.AIReviewDetail["uncovered_fields"])
}

func TestContentModerationAILogSkippedKeepsUncoveredEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		uncovered  []string
	}{
		{"empty input", `{"input":""}`, nil},
		{
			"only unselected content",
			`{"previous_response_id":"synthetic-history","vendor_extension":{"note":"opaque-extension"},"input":[{"type":"input_image","image_url":"data:image/png;base64,%%%"},{"type":"vendor_blob","data":"opaque-body"}]}`,
			[]string{"field:vendor_extension", "history:previous_response_id", "image_skipped:input/0", "content_type:vendor_blob", "content_shape:input/1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, captured, calls := aiLogCapturingProvider(t, ContentModerationAIAdapterChat, http.StatusOK, nil)
			cfg := aiTestConfig(server.URL, ContentModerationAIAdapterChat)
			cfg.AI.ImageReviewEnabled = false
			d, log := aiLogCheckQueued(t, server, aiTestInput(tc.body), cfg)
			require.Equal(t, "skipped", d.Phase)
			require.True(t, d.Allowed)
			require.Equal(t, "allow", log.Action)
			require.Equal(t, "allow", log.AIReviewDetail["verdict"])
			require.False(t, d.ProviderCalled)
			require.Zero(t, calls.Load())
			require.Empty(t, captured)
			require.Empty(t, log.FullInputText)
			require.Empty(t, log.ReviewTextExcerpt)
			require.Nil(t, log.UpstreamLatencyMS)
			require.Empty(t, log.Error)
			require.Contains(t, log.AIReviewDetail["reason"], "未送审")
			require.False(t, d.Coverage.Complete)
			require.Zero(t, d.Coverage.FragmentCount)
			require.Zero(t, d.Coverage.ImageCount)
			require.Zero(t, d.Coverage.ReviewedCount)
			require.ElementsMatch(t, tc.uncovered, d.Coverage.UncoveredFields)
			require.ElementsMatch(t, tc.uncovered, log.AIReviewDetail["uncovered_fields"])
			require.Empty(t, log.AIReviewDetail["fragments"])
			require.Empty(t, log.AIReviewDetail["images"])
			raw, err := json.Marshal(log)
			require.NoError(t, err)
			for _, omitted := range []string{"data:image/", "opaque-extension", "opaque-body"} {
				require.NotContains(t, string(raw), omitted)
			}
		})
	}
}

func TestContentModerationAILogRedactsDedicatedAndGenericSecretsIncludingReason(t *testing.T) {
	// synthetic-key 不匹配通用密钥模式，必须由专用配置密钥的精确脱敏保护。
	credentials := "专用 synthetic-key；通用 sk-fixtureGenericLog1234567890；字段 api_key=fixtureNamedSecret123456；令牌 Bearer fixtureBearerSecret123456；证据保留"
	redacted := "专用 [REDACTED]；通用 [已脱敏]；字段 api_key=[已脱敏]；令牌 Bearer [已脱敏]；证据保留"
	padding := strings.Repeat("保留普通文本。", 600)
	text := credentials + "\n" + padding + "\n" + credentials
	wantText := "[input|user] " + redacted + "\n" + padding + "\n" + redacted
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		t.Run(adapter, func(t *testing.T) {
			server, captured, calls := aiLogCapturingProvider(t, adapter, http.StatusOK, func(result *ContentModerationAIResult) {
				result.Reason = "整体判定说明：" + credentials
			})
			cfg := aiTestConfig(server.URL, adapter)
			d, log := aiLogCheckQueued(t, server, aiTestInput(aiJSON(map[string]string{"input": text})), cfg)
			require.Equal(t, "allow", d.Verdict)
			require.Equal(t, "completed", d.Phase)
			require.EqualValues(t, 1, calls.Load())
			require.Equal(t, wantText, log.FullInputText)
			require.Equal(t, wantText, log.ReviewTextExcerpt)
			require.Equal(t, "整体判定说明："+redacted, log.AIReviewDetail["reason"])
			raw, err := json.Marshal(log)
			require.NoError(t, err)
			for _, secret := range []string{cfg.AI.APIKey, "sk-fixtureGenericLog1234567890", "fixtureNamedSecret123456", "fixtureBearerSecret123456"} {
				require.NotContains(t, string(raw), secret, "all audit fields, including reason and list excerpt, must be redacted")
			}
			submitted, _ := aiLogSubmittedInput(t, adapter, captured)
			require.Len(t, submitted.Fragments, 1)
			require.Equal(t, text, submitted.Fragments[0].Text, "audit redaction must not modify provider input")
		})
	}
}

func TestContentModerationAILogImagesContainOnlyMetadataAndOneOverallVerdict(t *testing.T) {
	payloads := []string{"first synthetic image", "second synthetic image with different size"}
	imageURLs := []string{tinyDataURL(t, payloads[0]), tinyDataURL(t, payloads[1])}
	remoteImage := "https://example.invalid/omitted-audit-image"
	body := aiJSON(map[string]any{"input": []any{map[string]any{
		"role": "user",
		"content": []any{
			map[string]string{"type": "input_text", "text": "violent"},
			map[string]string{"type": "input_text", "text": "attack"},
			map[string]string{"type": "input_image", "image_url": imageURLs[0]},
			map[string]string{"type": "input_image", "image_url": imageURLs[1]},
			map[string]string{"type": "input_image", "image_url": remoteImage},
		},
	}}})
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		t.Run(adapter, func(t *testing.T) {
			server, captured, calls := aiLogCapturingProvider(t, adapter, http.StatusOK, nil)
			cfg := aiTestConfig(server.URL, adapter)
			d, log := aiLogCheckQueued(t, server, aiTestInput(body), cfg)
			require.Equal(t, "block", d.Verdict)
			require.Equal(t, "completed", d.Phase)
			require.EqualValues(t, 1, calls.Load())
			require.True(t, d.Coverage.Complete)
			require.Equal(t, 4, d.Coverage.ReviewedCount)
			require.Equal(t, 2, d.Coverage.FragmentCount)
			require.Equal(t, 2, d.Coverage.ImageCount)
			require.Equal(t, "[input/0/content/0/text|user] violent\n[input/0/content/1/text|user] attack", log.FullInputText)
			require.Equal(t, log.FullInputText, log.ReviewTextExcerpt)
			require.Equal(t, []string{"image_not_inline:input/0/content/4"}, log.AIReviewDetail["uncovered_fields"])
			require.Equal(t, []string{"illicit/violent"}, log.AIReviewDetail["categories"])
			require.NotEmpty(t, log.AIReviewDetail["reason"])
			require.Zero(t, log.HighestScore, "a categorical result is not a probability")
			require.Empty(t, log.CategoryScores)
			// 精确限定元信息字段，防止把整体 verdict/categories 复制成每段或每图的独立结果。
			require.Equal(t, []map[string]any{
				{"id": "f0001", "parent": "input/0/content/0/text", "role": "user", "source": "customer_data", "start_rune": 0, "end_rune": 7},
				{"id": "f0002", "parent": "input/0/content/1/text", "role": "user", "source": "customer_data", "start_rune": 0, "end_rune": 6},
			}, log.AIReviewDetail["fragments"])
			require.Equal(t, []map[string]any{
				{"id": "i0001", "parent": "input/0/content/2", "role": "user", "media_type": "image/png", "bytes": len(payloads[0])},
				{"id": "i0002", "parent": "input/0/content/3", "role": "user", "media_type": "image/png", "bytes": len(payloads[1])},
			}, log.AIReviewDetail["images"])
			raw, err := json.Marshal(log)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "data:image/")
			require.NotContains(t, string(raw), remoteImage)
			for i, imageURL := range imageURLs {
				_, encoded, _ := strings.Cut(imageURL, ",")
				require.NotContains(t, string(raw), encoded)
				require.NotContains(t, string(raw), payloads[i])
			}
			submitted, submittedImages := aiLogSubmittedInput(t, adapter, captured)
			require.Len(t, submitted.Fragments, 2)
			require.Equal(t, imageURLs, submittedImages, "images must reach the provider even though the audit queue omits their bytes")
		})
	}
}

type aiLogDetailReader struct {
	ContentModerationRepository
	getLog func(context.Context, int64) (*ContentModerationLog, error)
}

func (r *aiLogDetailReader) GetLog(ctx context.Context, id int64) (*ContentModerationLog, error) {
	return r.getLog(ctx, id)
}

func TestContentModerationAILogGetLogRepositoryBoundaries(t *testing.T) {
	t.Run("repository unavailable or detail unsupported", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			service *ContentModerationService
			message string
		}{
			{"nil service", nil, "repository unavailable"},
			{"nil repository", &ContentModerationService{}, "repository unavailable"},
			{"no detail reader", &ContentModerationService{repo: &aiLogRepo{}}, "log detail unavailable"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				log, err := tc.service.GetLog(context.Background(), 1)
				require.Nil(t, log)
				require.ErrorContains(t, err, tc.message)
			})
		}
	})
	t.Run("invalid IDs never reach repository", func(t *testing.T) {
		s := &ContentModerationService{repo: &aiLogDetailReader{getLog: func(context.Context, int64) (*ContentModerationLog, error) {
			t.Fatal("invalid ID reached repository")
			return nil, nil
		}}}
		for _, id := range []int64{0, -1} {
			log, err := s.GetLog(context.Background(), id)
			require.NoError(t, err)
			require.Nil(t, log)
		}
	})
	t.Run("valid IDs preserve repository result and error", func(t *testing.T) {
		repoErr := errors.New("synthetic detail read failure")
		for _, tc := range []struct {
			name string
			id   int64
			log  *ContentModerationLog
			err  error
		}{
			{"first ID", 1, &ContentModerationLog{ID: 1, FullInputText: "complete evidence"}, nil},
			{"largest ID", 1<<63 - 1, &ContentModerationLog{ID: 1<<63 - 1}, nil},
			{"not found", 42, nil, nil},
			{"read failure", 42, nil, repoErr},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				s := &ContentModerationService{repo: &aiLogDetailReader{getLog: func(gotCtx context.Context, id int64) (*ContentModerationLog, error) {
					calls++
					require.Same(t, ctx, gotCtx)
					require.Equal(t, tc.id, id)
					return tc.log, tc.err
				}}}
				log, err := s.GetLog(ctx, tc.id)
				require.Equal(t, 1, calls)
				if tc.log == nil {
					require.Nil(t, log)
				} else {
					require.Same(t, tc.log, log)
				}
				if tc.err == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, tc.err)
				}
			})
		}
	})
}
