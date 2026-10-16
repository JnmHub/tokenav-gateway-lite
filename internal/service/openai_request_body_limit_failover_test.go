//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIRequestBodyTooLargeClassifier(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		message string
		body    string
		want    bool
	}{
		{
			name:   "http 413",
			status: http.StatusRequestEntityTooLarge,
			body:   `{"error":{"message":"proxy request limit"}}`,
			want:   true,
		},
		{
			name:   "413 context window is deterministic",
			status: http.StatusRequestEntityTooLarge,
			body:   `{"error":{"message":"input exceeds the context window for this model"}}`,
			want:   false,
		},
		{
			name:   "structured 400 code",
			status: http.StatusBadRequest,
			body:   `{"error":{"code":"request_body_too_large","message":"rejected"}}`,
			want:   true,
		},
		{
			name:   "strong 400 serialized size message",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"request body exceeds this account's 16MB proxy limit"}}`,
			want:   true,
		},
		{
			name:   "unknown 400 too-large text is not enough",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"request is too large"}}`,
			want:   false,
		},
		{
			name:   "request field count is not serialized size",
			status: http.StatusBadRequest,
			body:   `{"error":{"message":"request body tools exceed maximum item limit"}}`,
			want:   false,
		},
		{
			name:    "fallback sanitized message with size signal",
			status:  http.StatusBadRequest,
			message: "request payload is larger than the 8 MB limit",
			body:    `{}`,
			want:    true,
		},
		{
			name:   "same message on 500 remains generic failover",
			status: http.StatusInternalServerError,
			body:   `{"error":{"message":"request body exceeds the 16MB limit"}}`,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAIRequestBodyTooLargeError(tt.status, tt.message, []byte(tt.body)))
		})
	}
}

func TestOpenAIRequestBodyLimitFailover_HTTPAndPassthroughBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requestBody := []byte(`{"model":"gpt-5.6-sol","stream":false,"input":"hello"}`)

	for _, passthrough := range []bool{false, true} {
		name := "native_responses"
		if passthrough {
			name = "api_key_passthrough"
		}
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

			const upstreamBody = `{"error":{"message":"request body exceeds this account's 16MB proxy limit; secret=must-not-leak","type":"invalid_request_error"}}`
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusRequestEntityTooLarge,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"rid-body-limit"},
				},
				Body: ioNopCloserString(upstreamBody),
			}}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
				httpUpstream: upstream,
			}
			account := &Account{
				ID: 161, Name: name, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Concurrency: 1, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{
					"api_key":   "sk-test",
					"base_url":  "https://api.example.test",
					"pool_mode": true,
					"pool_mode_retry_status_codes": []any{
						float64(http.StatusRequestEntityTooLarge),
					},
				},
				Extra: map[string]any{
					"openai_passthrough":         passthrough,
					"openai_responses_supported": true,
				},
			}

			result, err := svc.Forward(context.Background(), c, account, requestBody)

			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.IsOpenAIRequestBodyTooLarge())
			require.Equal(t, http.StatusRequestEntityTooLarge, failoverErr.StatusCode)
			require.Equal(t, GatewayFailureScopeAccount, failoverErr.Scope)
			require.Equal(t, NextAccountRetry, failoverErr.NextAccountAction)
			require.Equal(t, http.StatusRequestEntityTooLarge, failoverErr.ClientStatusCode)
			require.Equal(t, OpenAIRequestBodyTooLargeClientMessage, failoverErr.ClientMessage)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
			require.False(t, c.Writer.Written())
			require.Empty(t, rec.Body.String())
			if passthrough {
				require.Equal(t, requestBody, upstream.lastBody)
			} else {
				require.Equal(t, "gpt-5.6-sol", gjson.GetBytes(upstream.lastBody, "model").String())
				require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "input").String())
			}
		})
	}
}

func TestOpenAIRequestBodyLimitFailover_Structured400SwitchesAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       ioNopCloserString(`{"error":{"code":"payload_too_large","message":"rejected"}}`),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{
		ID: 162, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"openai_responses_supported": true},
	}

	_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.6-sol","input":"hello"}`))

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.IsOpenAIRequestBodyTooLarge())
	require.Equal(t, http.StatusRequestEntityTooLarge, failoverErr.ClientStatusCode)
}

func TestOpenAIRequestBodyLimitFailover_ContextWindowOrWrittenResponseDoesNotSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		writeFirst bool
		body       string
	}{
		{
			name: "context window",
			body: `{"error":{"message":"input exceeds the context window for this model"}}`,
		},
		{
			name:       "downstream already written",
			writeFirst: true,
			body:       `{"error":{"message":"request body exceeds this account's 16MB proxy limit"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))
			if tt.writeFirst {
				c.String(http.StatusOK, "already-started")
			}
			svc := &OpenAIGatewayService{
				cfg: &config.Config{},
				httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusRequestEntityTooLarge,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       ioNopCloserString(tt.body),
				}},
			}
			account := &Account{
				ID: 163, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{"openai_responses_supported": true},
			}

			_, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.6-sol","input":"hello"}`))

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.True(t, c.Writer.Written())
		})
	}
}

func TestOpenAIRequestBodyLimitFailover_NotAddedToGlobalImageOrGenericFailover(t *testing.T) {
	svc := &OpenAIGatewayService{}
	body := []byte(`{"error":{"message":"request body exceeds the 16MB limit"}}`)

	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(http.StatusRequestEntityTooLarge, "", body))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(http.StatusRequestEntityTooLarge))
}

func ioNopCloserString(value string) *trackingStringReadCloser {
	return &trackingStringReadCloser{Reader: strings.NewReader(value)}
}

type trackingStringReadCloser struct {
	*strings.Reader
}

func (r *trackingStringReadCloser) Close() error { return nil }
