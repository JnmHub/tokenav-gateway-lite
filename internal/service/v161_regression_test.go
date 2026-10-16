package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type v161RateLimitRepo struct {
	AccountRepository
	modelCalls int
	model      string
	tempCalls  int
}

func (r *v161RateLimitRepo) SetModelRateLimit(_ context.Context, _ int64, model string, _ time.Time, _ ...string) error {
	r.modelCalls++
	r.model = model
	return nil
}

func (r *v161RateLimitRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempCalls++
	return nil
}

func v161TempRuleAccount(poolMode bool) *Account {
	return &Account{
		ID:       161,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":                  poolMode,
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{
				map[string]any{
					"error_code":       float64(http.StatusServiceUnavailable),
					"keywords":         []any{"overloaded"},
					"duration_minutes": float64(10),
				},
			},
		},
	}
}

func TestV161TempRuleUsesModelCooldownInPoolMode(t *testing.T) {
	repo := &v161RateLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := v161TempRuleAccount(true)

	disable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"error":"upstream overloaded"}`),
		"gpt-5.6-sol",
	)

	require.True(t, disable)
	require.Equal(t, 1, repo.modelCalls)
	require.Equal(t, "gpt-5.6-sol", repo.model)
	require.Zero(t, repo.tempCalls, "known-model failures must not block the whole account")
}

func TestV161PoolModeUnauthorizedDoesNotApplyTempRule(t *testing.T) {
	repo := &v161RateLimitRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := v161TempRuleAccount(true)

	disable := svc.HandleUpstreamError(
		context.Background(), account, http.StatusUnauthorized, http.Header{},
		[]byte(`{"error":"overloaded"}`), "gpt-5.6-sol",
	)

	require.False(t, disable)
	require.Zero(t, repo.modelCalls)
	require.Zero(t, repo.tempCalls)
}

type v161AvailabilityRepo struct {
	AccountRepository
	called bool
}

func (r *v161AvailabilityRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	r.called = true
	return []Account{{
		ID:       1,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"},
		},
	}}, nil
}

func (r *v161AvailabilityRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return nil, errors.New("transient scheduler snapshot is empty")
}

func TestV161ModelAvailabilityUsesPersistentCandidates(t *testing.T) {
	repo := &v161AvailabilityRepo{}
	groupID := int64(7)
	svc := &OpenAIGatewayService{accountRepo: repo}

	diagnosis := svc.DiagnoseModelAvailabilityForPlatform(
		context.Background(), &groupID, "gpt-5.6-sol", PlatformOpenAI,
	)

	require.True(t, repo.called)
	require.True(t, diagnosis.HasAccountsInPool)
	require.True(t, diagnosis.HasModelSupport)
}

type v161GenericAvailabilityRepo struct {
	AccountRepository
	called bool
}

func (r *v161GenericAvailabilityRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	r.called = true
	return []Account{{
		ID:       2,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"},
		},
	}}, nil
}

func (r *v161GenericAvailabilityRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	return nil, errors.New("transient scheduler snapshot is empty")
}

func TestV161GenericAvailabilityUsesPersistentCandidates(t *testing.T) {
	repo := &v161GenericAvailabilityRepo{}
	groupID := int64(8)
	svc := &GatewayService{
		accountRepo: repo,
		cfg:         &config.Config{RunMode: config.RunModeStandard},
	}

	diagnosis := svc.DiagnoseModelAvailabilityForPlatform(
		context.Background(), &groupID, "claude-sonnet-4-6", PlatformAnthropic,
	)

	require.True(t, repo.called)
	require.True(t, diagnosis.HasAccountsInPool)
	require.True(t, diagnosis.HasModelSupport)
}

func TestV161ClaudeLongContextSuffixNormalization(t *testing.T) {
	require.Equal(t, "claude-sonnet-4-6", normalizeClaudeCodeLongContextModel("claude-sonnet-4-6[1m]"))
	require.Equal(t, "claude-sonnet-4-6", normalizeClaudeCodeLongContextModel("claude-sonnet-4-6[1M][1m]"))
	require.Equal(t, "claude-sonnet-4-6", normalizeClaudeCodeLongContextModel("claude-sonnet-4-6"))
	require.Equal(t, "kimi-k3", normalizeClaudeCodeLongContextModel("kimi-k3[1m]"))
	require.Equal(t, "k3-256k", normalizeClaudeCodeLongContextModel("k3-256k[1M]"))
}

func TestV161AnthropicMonitorAggregatesAllTextBlocks(t *testing.T) {
	body := []byte(`{"content":[{"type":"thinking","thinking":"hidden"},{"type":"text","text":"first"},{"type":"tool_use","name":"lookup"},{"type":"text","text":"second"}]}`)
	require.Equal(t, "first\nsecond", extractAnthropicMonitorText(body))
}

func TestV161AntigravityAbnormalKeepsPaidPlan(t *testing.T) {
	result := NormalizeAntigravitySubscription(&antigravity.LoadCodeAssistResponse{
		PaidTier:        &antigravity.PaidTierInfo{ID: "g1-pro-tier"},
		IneligibleTiers: []*antigravity.IneligibleTier{{ReasonMessage: "temporarily unavailable"}},
	})

	require.Equal(t, "Pro", result.PlanType)
	require.Equal(t, antigravitySubscriptionAbnormal, result.SubscriptionStatus)
	require.Equal(t, "temporarily unavailable", result.SubscriptionError)
}

func TestV161StreamReadErrorsAreStableAndSanitized(t *testing.T) {
	err := newOpenAIUpstreamStreamReadError(errors.New("http2: stream error: stream ID 31; INTERNAL_ERROR"))
	code, message, ok := OpenAIUpstreamStreamReadErrorDetails(err)

	require.True(t, ok)
	require.Equal(t, OpenAIUpstreamHTTP2StreamErrorCode, code)
	require.Equal(t, "Upstream HTTP/2 stream failed", message)
	require.NotContains(t, message, "31")
	require.NotContains(t, message, "INTERNAL_ERROR")
}

func TestV161GrokInvalidEncryptedContentRetryPreservesOtherFields(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[{"type":"reasoning","encrypted_content":"bad","summary":[{"type":"summary_text","text":"keep"}]},{"type":"message","nonce":9007199254740993,"content":"hello"}]}`)
	retryBody, changed, err := trimGrokInvalidEncryptedContentRetryBody(body)

	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(retryBody, "input.0.encrypted_content").Exists())
	require.Equal(t, "keep", gjson.GetBytes(retryBody, "input.0.summary.0.text").String())
	require.Equal(t, "9007199254740993", gjson.GetBytes(retryBody, "input.1.nonce").Raw)
}

func TestV161GrokPureClientFunctionsDoNotInjectSearch(t *testing.T) {
	pureClient := []byte(`{"tools":[{"type":"function","name":"read_file"}]}`)
	unchanged, err := appendMissingGrokFreeCacheNativeTools(pureClient)
	require.NoError(t, err)
	require.JSONEq(t, string(pureClient), string(unchanged))

	searchIntent := []byte(`{"tools":[{"type":"function","name":"read_file"},{"type":"web_search"}]}`)
	patched, err := appendMissingGrokFreeCacheNativeTools(searchIntent)
	require.NoError(t, err)
	require.Equal(t, "x_search", gjson.GetBytes(patched, "tools.2.type").String())
}

func TestV161GrokVideoSignedURLAndSameOriginRewrite(t *testing.T) {
	allowed, err := grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"https://vidgen.x.ai/content/video.mp4?sig=secret"}}`), "req_1")
	require.NoError(t, err)
	require.Equal(t, "https://vidgen.x.ai/content/video.mp4?sig=secret", allowed)

	_, err = grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"https://vidgen.x.ai.evil.example/video.mp4"}}`), "req_1")
	require.Error(t, err)
	_, err = grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"http://vidgen.x.ai/video.mp4"}}`), "req_1")
	require.Error(t, err)

	chained, err := grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"https://relay.example/v1/videos/req_1/content"}}`), "req_1")
	require.NoError(t, err)
	require.Empty(t, chained, "同任务上游 Sub2API 内容路径应由当前账号 base_url 安全重建")
	_, err = grokMediaSignedVideoContentURL([]byte(`{"video":{"url":"https://relay.example/v1/videos/other-task/content"}}`), "req_1")
	require.Error(t, err, "不同任务的代理路径不得跟随")

	rewritten := rewriteGrokMediaVideoContentURLs(
		[]byte(`{"video":{"url":"https://vidgen.x.ai/video.mp4"},"content_url":"https://api.x.ai/v1/videos/req_161/content"}`),
		"req_161",
		"/v1/videos/req_161/content",
	)
	require.Equal(t, "/v1/videos/req_161/content", gjson.GetBytes(rewritten, "video.url").String())
	require.Equal(t, "/v1/videos/req_161/content", gjson.GetBytes(rewritten, "content_url").String())
}

func TestV161GrokVideoContentPreservesRangeResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode:    http.StatusPartialContent,
		Header:        http.Header{"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 0-3/10"}, "Accept-Ranges": []string{"bytes"}},
		Body:          ioNopCloser{Reader: strings.NewReader("data")},
		ContentLength: 4,
	}

	err := writeGrokMediaContentResponse(c, resp)

	require.NoError(t, err)
	require.Equal(t, http.StatusPartialContent, recorder.Code)
	require.Equal(t, "bytes 0-3/10", recorder.Header().Get("Content-Range"))
	require.Equal(t, "bytes", recorder.Header().Get("Accept-Ranges"))
	require.Equal(t, "data", recorder.Body.String())
}

// ioNopCloser avoids importing io solely for a small deterministic body.
type ioNopCloser struct{ *strings.Reader }

func (ioNopCloser) Close() error { return nil }
