package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAITempUnschedulableRepo struct {
	stubOpenAIAccountRepo
	accountID int64
	modelKey  string
}

func (r *openAITempUnschedulableRepo) SetModelRateLimit(_ context.Context, accountID int64, modelKey string, _ time.Time, _ ...string) error {
	r.accountID = accountID
	r.modelKey = modelKey
	return nil
}

func newOpenAIFailoverTestContext(t *testing.T, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func openAIFailoverTestAccount() *Account {
	return &Account{
		ID: 17103, Name: "v0171-failover", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                    "sk-test",
			"base_url":                   "http://upstream.example",
			"model_provider":             "env-openai",
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code": float64(http.StatusBadRequest), "keywords": []any{"temporary scheduling block"}, "duration_minutes": float64(1),
			}},
		},
	}
}

func TestOpenAIHTTPFailoverPolicyCapacityShedAndContentRejection(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := openAIFailoverTestAccount()

	for _, code := range []string{"server_is_overloaded", "slow_down"} {
		body := []byte(`{"error":{"code":"` + code + `","message":"retry later"}}`)
		err := svc.buildOpenAIHTTPFailoverError(context.Background(), nil, account, http.StatusServiceUnavailable, nil, body, "retry later", "gpt-5.6", true)
		require.NotNil(t, err, code)
		require.True(t, err.RetryableOnSameAccount, code)
		require.True(t, err.RequestScopedTransient, code)
		require.False(t, err.ShouldReportAccountScheduleFailure(), code)
	}

	for _, message := range []string{
		"Our server is overloaded. Please retry later.",
		"Our servers are overloaded. Please retry later.",
		"Our servers are currently overloaded. Please retry later.",
	} {
		body := []byte(`{"error":{"message":"` + message + `"}}`)
		err := svc.buildOpenAIHTTPFailoverError(context.Background(), nil, account, http.StatusBadGateway, nil, body, message, "gpt-5.6", true)
		require.NotNil(t, err, message)
		require.True(t, err.RetryableOnSameAccount, message)
		require.True(t, err.RequestScopedTransient, message)
		require.False(t, err.ShouldReportAccountScheduleFailure(), message)
	}

	contentBody := []byte(`{"error":{"code":"content_policy_violation","message":"prompt was rejected by the safety system"}}`)
	require.True(t, isOpenAIRequestScopedContentPolicyError(http.StatusForbidden, contentBody))
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusForbidden, "prompt rejected", contentBody))
	require.Nil(t, svc.buildOpenAIHTTPFailoverError(context.Background(), nil, account, http.StatusForbidden, nil, contentBody, "prompt rejected", "gpt-5.6", true))

	accountBody := []byte(`{"error":{"code":"account_disabled","message":"organization has been disabled"}}`)
	require.False(t, isOpenAIRequestScopedContentPolicyError(http.StatusForbidden, accountBody))
	require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusForbidden, "organization disabled", accountBody))
}

func TestOpenAIWSNativeCapacityShedClassification(t *testing.T) {
	for _, code := range []string{"server_is_overloaded", "slow_down"} {
		payload := []byte(`{"type":"error","error":{"code":"` + code + `","message":"retry later"}}`)
		require.True(t, isOpenAIUpstreamCapacityShedEvent(payload), code)
		require.Equal(t, http.StatusServiceUnavailable, openAIWSErrorHTTPStatusFromRaw(code, ""), code)
		require.False(t, isOpenAIWSRateLimitError(code, "", "retry later"), "容量降载不能污染限额状态")
	}

	payload := []byte(`{"type":"error","error":{"message":"Our servers are currently overloaded. Please retry later."}}`)
	require.True(t, isOpenAIUpstreamCapacityShedEvent(payload))
	require.True(t, openAIStreamFailedEventShouldFailover(payload, ""))
}

func TestOpenAIGatewayServiceForwardResponsesTempUnschedulableReturnsFailover(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","input":"hello","stream":false}`)
	c, recorder := newOpenAIFailoverTestContext(t, "/v1/responses", body)
	upstreamBody := []byte(`{"error":{"message":"temporary scheduling block"}}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(upstreamBody)),
	}}
	repo := &openAITempUnschedulableRepo{}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
		httpUpstream: upstream, rateLimitService: NewRateLimitService(repo, nil, &config.Config{}, nil, nil),
	}
	account := openAIFailoverTestAccount()

	_, err := svc.Forward(context.Background(), c, account, body)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Equal(t, account.ID, repo.accountID)
	require.Equal(t, "gpt-5.6", repo.modelKey)
	require.False(t, IsResponseCommitted(c))
	require.Empty(t, recorder.Body.String())
}

func TestOpenAIMessagesAndFallbackContentPolicyDoNotFailover(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	blockedBody := `{"error":{"code":"content_filter","message":"request rejected by content moderation"}}`

	for _, testCase := range []struct {
		name          string
		forceFallback bool
	}{
		{name: "messages responses path"},
		{name: "messages chat fallback", forceFallback: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			c, recorder := newOpenAIFailoverTestContext(t, "/v1/messages", body)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusForbidden, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(blockedBody)),
			}}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false, AllowInsecureHTTP: true}}},
				httpUpstream: upstream,
			}
			account := openAIFailoverTestAccount()
			if testCase.forceFallback {
				account.Credentials["force_chat_completions"] = true
			}

			_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, http.StatusForbidden, recorder.Code)
			require.Contains(t, recorder.Body.String(), "request rejected by content moderation")
		})
	}
}
