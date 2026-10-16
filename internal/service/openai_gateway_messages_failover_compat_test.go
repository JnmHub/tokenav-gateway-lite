//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func buildResponsesFailedSSEStream(errType, errorMessage string) string {
	failed := fmt.Sprintf(`{"type":"response.failed","response":{"id":"resp_err","object":"response","status":"failed","error":{"type":"%s","message":"%s"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}`, errType, errorMessage)
	return fmt.Sprintf("data: %s\n\n", failed)
}

func newMessagesTestContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func TestForwardAsAnthropicTransportErrorReturnsFailoverWithoutWritingResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newMessagesTestContext(body)
	svc := &OpenAIGatewayService{
		cfg: rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{
			err: errors.New("dial tcp: connection refused"),
		},
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.True(t, errors.As(err, &failoverErr))
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, rec.Body.String())
}

func TestForwardAsAnthropicBufferedResponseFailedReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, rec := newMessagesTestContext(body)
	svc := &OpenAIGatewayService{
		cfg: rawChatCompletionsTestConfig(),
		httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(buildResponsesFailedSSEStream("invalid_request_error", "Content policy violation"))),
		}},
	}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
	require.ErrorContains(t, err, "upstream response failed")
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestForwardAsAnthropicResponseFailedRateLimitTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		stream := stream
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
			c, _ := newMessagesTestContext(body)
			svc := &OpenAIGatewayService{
				cfg: rawChatCompletionsTestConfig(),
				httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(buildResponsesFailedSSEStream("rate_limit_error", "Rate limit reached"))),
				}},
			}

			_, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.True(t, errors.As(err, &failoverErr), "got %T: %v", err, err)
		})
	}
}
