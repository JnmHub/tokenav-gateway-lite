//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newGrokMediaTestAccount(id int64) *Account {
	return &Account{
		ID: id, Name: "grok-media", Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-api-key", "base_url": "https://api.x.ai/v1"},
	}
}

func newGrokMediaTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/1/test", nil)
	return c, recorder
}

func TestAccountTestServiceGrokImageModeEmitsPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGrokMediaTestAccount(21)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/png"}]}`)),
	}}
	svc := &AccountTestService{accountRepo: &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}, httpUpstream: upstream}
	c, recorder := newGrokMediaTestContext()

	err := svc.TestAccountConnection(c, account.ID, "grok-imagine-image", "draw a cat", AccountTestModeGrokImage)

	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/images/generations", upstream.lastReq.URL.String())
	require.Contains(t, recorder.Body.String(), `"type":"image"`)
	require.Contains(t, recorder.Body.String(), "data:image/png;base64,aGVsbG8=")
}

func TestAccountTestServiceGrokTTSModeEmitsAudioPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGrokMediaTestAccount(22)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"audio/mpeg"}},
		Body:       io.NopCloser(strings.NewReader("audio-bytes")),
	}}
	svc := &AccountTestService{accountRepo: &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}, httpUpstream: upstream}
	c, recorder := newGrokMediaTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "hello", AccountTestModeGrokTTS)

	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/tts", upstream.lastReq.URL.String())
	require.Contains(t, recorder.Body.String(), `"type":"audio"`)
	require.Contains(t, recorder.Body.String(), base64.StdEncoding.EncodeToString([]byte("audio-bytes")))
}

func TestAccountTestServiceGrokSTTModeSendsUploadedAudio(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newGrokMediaTestAccount(23)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"text":"recognized speech"}`)),
	}}
	svc := &AccountTestService{accountRepo: &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}, httpUpstream: upstream}
	c, recorder := newGrokMediaTestContext()
	upload := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(minimalSilentWAV())

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeGrokSTT, AccountTestOptions{AudioDataURL: upload})

	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/stt", upstream.lastReq.URL.String())
	require.Contains(t, upstream.lastReq.Header.Get("Content-Type"), "multipart/form-data")
	require.Contains(t, string(upstream.lastBody), "upload.wav")
	require.Contains(t, recorder.Body.String(), "recognized speech")
}

type grokAccountTestRateLimitRepo struct {
	*mockAccountRepoForGemini
	rateLimitedCalls int
	resetAt          time.Time
}

func (r *grokAccountTestRateLimitRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitedCalls++
	r.resetAt = resetAt
	return nil
}

func TestAccountTestServiceTestAccountConnectionGrokUsesXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          13,
		Name:        "grok-oauth",
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "grok-access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"model_mapping": map[string]any{
				"grok": "grok-4.5",
			},
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\"}\n\n",
		)),
	}}
	svc := &AccountTestService{
		accountRepo:       repo,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		httpUpstream:      upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/13/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok", "", AccountTestModeDefault)
	require.NoError(t, err)
	require.Equal(t, xai.DefaultCLIBaseURL+"/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-access-token", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), `"model":"grok-4.5"`)
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

func TestAccountTestServiceTestAccountConnectionGrokAPIKeyUsesXAIResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID:          14,
		Name:        "grok-apikey",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "grok-api-key",
			"base_url": "https://api.x.ai/v1",
			"model_mapping": map[string]any{
				"grok-latest": "grok-4.5",
			},
		},
	}
	repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
				"data: {\"type\":\"response.completed\"}\n\n",
		)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/14/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok-latest", "", AccountTestModeDefault)
	require.NoError(t, err)
	require.Equal(t, "https://api.x.ai/v1/responses", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer grok-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "grok-4.5", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, rec.Body.String(), `"model":"grok-4.5"`)
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

func TestAccountTestServiceGrok429PersistsRateLimitReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID: 15, Name: "grok-oauth-limited", Platform: PlatformGrok,
		Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "grok-access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	baseRepo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	repo := &grokAccountTestRateLimitRepo{mockAccountRepoForGemini: baseRepo}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"45"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
	}}
	svc := &AccountTestService{
		accountRepo:       repo,
		grokTokenProvider: NewGrokTokenProvider(repo, nil),
		httpUpstream:      upstream,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/15/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok", "", AccountTestModeDefault)

	require.Error(t, err)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, time.Now().Add(45*time.Second), repo.resetAt, time.Second)
}

func TestAccountTestServiceGrok429WithoutQuotaHeadersUsesFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID: 16, Name: "grok-oauth-limited-no-headers", Platform: PlatformGrok,
		Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "grok-access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	baseRepo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
	repo := &grokAccountTestRateLimitRepo{mockAccountRepoForGemini: baseRepo}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"quota exhausted"}}`)),
	}}
	svc := &AccountTestService{
		accountRepo: repo, grokTokenProvider: NewGrokTokenProvider(repo, nil), httpUpstream: upstream,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/16/test", nil)
	before := time.Now()

	err := svc.TestAccountConnection(c, account.ID, "grok", "", AccountTestModeDefault)

	require.Error(t, err)
	require.Equal(t, 1, repo.rateLimitedCalls)
	require.WithinDuration(t, before.Add(grokRateLimitFallbackCooldown), repo.resetAt, time.Second)
}
