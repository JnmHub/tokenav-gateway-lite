package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestOpenAIStreamPreOutputClassification(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		eventType string
		want      bool
	}{
		{name: "overload error frame", data: `{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later."}}`, eventType: "error", want: false},
		{name: "invalid request remains visible", data: `{"type":"error","error":{"type":"invalid_request_error","message":"invalid request"}}`, eventType: "error", want: true},
		{name: "empty reasoning item", data: `{"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, eventType: "response.output_item.added", want: false},
		{name: "encrypted reasoning item", data: `{"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"ciphertext"}}`, eventType: "response.output_item.added", want: true},
		{name: "empty reasoning summary", data: `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`, eventType: "response.reasoning_summary_part.added", want: false},
		{name: "reasoning summary text", data: `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":"thinking"}}`, eventType: "response.reasoning_summary_part.added", want: true},
		{name: "empty output content", data: `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`, eventType: "response.content_part.added", want: false},
		{name: "empty text delta", data: `{"type":"response.output_text.delta","delta":""}`, eventType: "response.output_text.delta", want: false},
		{name: "empty text done", data: `{"type":"response.output_text.done","text":""}`, eventType: "response.output_text.done", want: false},
		{name: "text delta", data: `{"type":"response.output_text.delta","delta":"hi"}`, eventType: "response.output_text.delta", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, openAIStreamDataStartsClientOutput(test.data, test.eventType))
		})
	}
}

func TestOpenAIFirstOutputGuardBoundsScannerAndQueue(t *testing.T) {
	var guard atomic.Bool
	guard.Store(true)
	split := openAIFirstOutputDynamicScanLines(&guard)
	oversized := bytes.Repeat([]byte{'x'}, openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance)

	_, _, err := split(oversized, false)
	require.ErrorIs(t, err, errOpenAIFirstOutputScannerLimit)
	require.Equal(t, 1, openAIFirstOutputEventQueueSize(true))
	require.Equal(t, 16, openAIFirstOutputEventQueueSize(false))
}

func TestOpenAIStreamSemanticOutputLocksAttemptBeforeFollowingOverload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stream := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
		`data: {"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_1","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		MaxLineSize:               defaultMaxLineSize,
		StreamDataIntervalTimeout: 5,
	}}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	core, observed := observer.New(zap.WarnLevel)
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	c.Request = request.WithContext(logger.IntoContext(request.Context(), zap.New(core)))
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(stream)),
		Header:     http.Header{"X-Request-Id": []string{"rid-locked-attempt"}},
	}

	_, err := service.handleStreamingResponse(c.Request.Context(), response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "已经产生语义输出后不能切到另一个账号拼接响应")
	require.Contains(t, recorder.Body.String(), `"delta":"partial"`)
	require.Contains(t, recorder.Body.String(), `"code":"server_error"`)
	require.NotContains(t, recorder.Body.String(), "server_is_overloaded")
	require.Equal(t, "rid-locked-attempt", recorder.Header().Get("X-Request-Id"))
	entries := observed.FilterMessage("gateway.failover_suppressed_after_semantic_output").All()
	require.Len(t, entries, 1)
	require.Equal(t, "native_sse", entries[0].ContextMap()["path"])
	require.Equal(t, "error", entries[0].ContextMap()["event_type"])
	require.Equal(t, "rid-locked-attempt", entries[0].ContextMap()["upstream_request_id"])
}

func TestSanitizeOpenAICapacityShedErrorCodeForClient(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantChanged bool
		wantCode    string
	}{
		{
			name:        "response failed nested code",
			payload:     `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"overloaded"}}}`,
			wantChanged: true,
			wantCode:    `"code":"server_error"`,
		},
		{
			name:        "bare error code",
			payload:     `{"type":"error","error":{"code":"slow_down","message":"slow down"}}`,
			wantChanged: true,
			wantCode:    `"code":"server_error"`,
		},
		{
			name:        "message only adds retryable code",
			payload:     `{"type":"error","error":{"message":"Server is overloaded. Please try again later."}}`,
			wantChanged: true,
			wantCode:    `"code":"server_error"`,
		},
		{
			name:        "rate limit remains unchanged",
			payload:     `{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"retry in 3s"}}}`,
			wantChanged: false,
			wantCode:    `"code":"rate_limit_exceeded"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, changed := sanitizeOpenAICapacityShedErrorCodeForClient([]byte(test.payload))
			require.Equal(t, test.wantChanged, changed)
			require.Contains(t, string(output), test.wantCode)
		})
	}
}

func TestOpenAIStreamKeepaliveDoesNotSplitSSEEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: 1,
		MaxLineSize:             defaultMaxLineSize,
	}}}
	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		_, _ = writer.Write([]byte("event: response.output_text.delta\n"))
		time.Sleep(1100 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ready\"}\n\n"))
		_, _ = writer.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ready\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	response := &http.Response{StatusCode: http.StatusOK, Body: reader}

	_, err := service.handleStreamingResponse(c.Request.Context(), response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
	require.NoError(t, err)
	body := recorder.Body.String()
	require.NotContains(t, body, "event: response.output_text.delta\n:\n\ndata:", "心跳不能插进尚未闭合的 SSE 事件")
	require.Contains(t, body, `"delta":"ready"`)
}

func TestOpenAIStreamMetadataAndMessageOnlyOverloadFailOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	largeMetadata := strings.Repeat("x", 16*1024)
	stream := strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","metadata":{"padding":"` + largeMetadata + `"}}}`,
		"",
		"event: response.output_item.added",
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`,
		"",
		"event: response.reasoning_summary_part.added",
		`data: {"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"type":"service_unavailable_error","message":"Our servers are currently overloaded. Please try again later."}}`,
		"",
	}, "\n")

	tests := []struct {
		name string
		run  func(*OpenAIGatewayService, *gin.Context, *http.Response, *Account) error
	}{
		{
			name: "native",
			run: func(service *OpenAIGatewayService, c *gin.Context, response *http.Response, account *Account) error {
				_, err := service.handleStreamingResponse(c.Request.Context(), response, c, account, time.Now(), "model", "model")
				return err
			},
		},
		{
			name: "passthrough",
			run: func(service *OpenAIGatewayService, c *gin.Context, response *http.Response, account *Account) error {
				_, err := service.handleStreamingResponsePassthrough(c.Request.Context(), response, c, account, time.Now(), "model", "model")
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			response := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(stream)),
				Header:     http.Header{"X-Request-Id": []string{"rid-message-only-overload"}},
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Name: "acc"}

			err := test.run(service, c, response, account)
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.True(t, errors.As(err, &failoverErr))
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.True(t, failoverErr.RequestScopedTransient)
			require.False(t, c.Writer.Written())
			require.Empty(t, recorder.Body.String())
			require.Empty(t, recorder.Header().Get("X-Request-Id"), "失败 attempt（尝试）的响应头不能泄漏给客户端")
		})
	}
}

func TestOpenAIStreamKeepaliveKeepsPreamblePrivateAndStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval: 1,
		MaxLineSize:             defaultMaxLineSize,
	}}}

	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_stalled\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_stalled\"}}\n\n"))
		time.Sleep(2100 * time.Millisecond)
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	response := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{"X-Request-Id": []string{"rid-stalled"}}}

	_, err := service.handleStreamingResponse(c.Request.Context(), response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model")
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Contains(t, recorder.Body.String(), ":\n\n")
	require.NotContains(t, recorder.Body.String(), "response.created")
	require.NotContains(t, recorder.Body.String(), "response.in_progress")
	require.Empty(t, recorder.Header().Get("X-Request-Id"), "心跳可以提交，但失败 attempt（尝试）的账号响应头必须保持私有")
}
