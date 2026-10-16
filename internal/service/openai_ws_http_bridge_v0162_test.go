package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProxyOpenAIWSHTTPBridgeTurnV0162TransportFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name         string
		turn         int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first turn", turn: 1, wantFailover: true},
		{name: "later turn", turn: 2, wantWrites: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{err: io.EOF}}
			account := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5", "", "", "", "", tt.turn, func(message []byte) error {
				writes = append(writes, append([]byte(nil), message...))
				return nil
			})
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
			if len(writes) > 0 {
				require.Equal(t, "error", gjson.GetBytes(writes[0], "type").String())
			}
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnV0162HTTPFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name         string
		turn         int
		status       int
		wantFailover bool
		wantWrites   int
	}{
		{name: "first 401", turn: 1, status: http.StatusUnauthorized, wantFailover: true},
		{name: "first 429", turn: 1, status: http.StatusTooManyRequests, wantFailover: true},
		{name: "first 500", turn: 1, status: http.StatusInternalServerError, wantFailover: true},
		{name: "later 500", turn: 2, status: http.StatusInternalServerError, wantWrites: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"server_error","message":"temporary upstream failure"}}`))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5", "", "", "", "", tt.turn, func(message []byte) error {
				writes = append(writes, append([]byte(nil), message...))
				return nil
			})
			require.Nil(t, result)
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, tt.status, failoverErr.StatusCode)
			} else {
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnV0162SSEErrorFailoverSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, turn := range []int{1, 2} {
		t.Run(fmt.Sprintf("turn_%d", turn), func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"message\":\"limited\"}}\n\n"))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5", "", "", "", "", turn, func(message []byte) error {
				writes = append(writes, append([]byte(nil), message...))
				return nil
			})
			var failoverErr *UpstreamFailoverError
			require.Nil(t, result)
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
			require.Empty(t, writes, "429 在首个下游事件前可以安全换号，不能先把错误写给客户端")
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnV0162RequiresTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name         string
		body         string
		wantFailover bool
		wantWrites   int
	}{
		{name: "done only", body: "data: [DONE]\n\n", wantFailover: true},
		{name: "created then done", body: "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_truncated\"}}\n\ndata: [DONE]\n\n", wantFailover: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte
			result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5", "", "", "", "", 1, func(message []byte) error {
				writes = append(writes, append([]byte(nil), message...))
				return nil
			})
			var failoverErr *UpstreamFailoverError
			if tt.wantFailover {
				require.Nil(t, result)
				require.ErrorAs(t, err, &failoverErr)
			} else {
				require.NotNil(t, result)
				require.Error(t, err)
				require.False(t, errors.As(err, &failoverErr))
			}
			require.Len(t, writes, tt.wantWrites)
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnExplicitEmptyToolsClearsInheritedState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reset\",\"output\":[{\"type\":\"function_call\",\"name\":\"exec\",\"call_id\":\"call_reset\",\"arguments\":\"{\\\"input\\\":\\\"pwd\\\"}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	setOpenAIWSHTTPBridgeToolState(c, openAIWSHTTPBridgeToolState{
		clientMapping: apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}},
		loweredTools:  json.RawMessage(`[{"type":"function","name":"exec"}]`),
	})
	setOpenAIResponsesClientToolMapping(c, apicompat.ResponsesClientToolMapping{CustomTools: map[string]bool{"exec": true}})
	payload := []byte(`{"type":"response.create","model":"gpt-5","tools":[],"input":"continue"}`)
	var events [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5", "", "", "", "", 2, func(message []byte) error {
		events = append(events, append([]byte(nil), message...))
		return nil
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, gjson.GetBytes(upstream.lastBody, "tools").Array(), 0)
	require.Len(t, events, 1)
	require.Equal(t, "function_call", gjson.GetBytes(events[0], "response.output.0.type").String(), "explicit reset must not restore an obsolete custom tool mapping")
	state, ok := openAIWSHTTPBridgeToolStateFromContext(c)
	require.True(t, ok)
	require.Empty(t, state.clientMapping)
}

func TestProxyOpenAIWSHTTPBridgeTurnRejectedFieldRetryKeepsClientToolMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"unsupported_parameter","param":"max_output_tokens","message":"unsupported parameter"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retry\",\"output\":[{\"type\":\"function_call\",\"name\":\"exec\",\"call_id\":\"call_retry\",\"arguments\":\"{\\\"input\\\":\\\"pwd\\\"}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
			)),
		},
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","max_output_tokens":128,"tools":[{"type":"custom","name":"exec"}],"input":"run"}`)
	var events [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), "gpt-5", "", "", "", "", 1, func(message []byte) error {
		events = append(events, append([]byte(nil), message...))
		return nil
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.False(t, gjson.GetBytes(upstream.bodies[1], "max_output_tokens").Exists())
	require.Equal(t, "function", gjson.GetBytes(upstream.bodies[1], "tools.0.type").String())
	require.Len(t, events, 1)
	require.Equal(t, "custom_tool_call", gjson.GetBytes(events[0], "response.output.0.type").String())
}
