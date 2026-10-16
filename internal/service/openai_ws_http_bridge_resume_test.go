package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIWSCurrentTurnRetryPayloadRebuildsContext(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"mapped","previous_response_id":"resp_old"}`)
	fullInput := []json.RawMessage{
		json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
	}

	retryPayload, retrySafe, err := buildOpenAIWSCurrentTurnRetryPayload(payload, fullInput, true, "gpt-requested")

	require.NoError(t, err)
	require.True(t, retrySafe)
	require.False(t, gjson.GetBytes(retryPayload, "previous_response_id").Exists())
	require.Equal(t, "gpt-requested", gjson.GetBytes(retryPayload, "model").String())
	require.Len(t, gjson.GetBytes(retryPayload, "input").Array(), 2)
}

func TestBuildOpenAIWSCurrentTurnRetryPayloadRejectsOrphanToolOutput(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"mapped","previous_response_id":"resp_old"}`)
	fullInput := []json.RawMessage{
		json.RawMessage(`{"type":"function_call_output","call_id":"missing_call","output":"done"}`),
	}

	retryPayload, retrySafe, err := buildOpenAIWSCurrentTurnRetryPayload(payload, fullInput, true, "gpt-requested")

	require.NoError(t, err)
	require.False(t, retrySafe)
	require.Nil(t, retryPayload)
}

func TestOpenAIWSHTTPBridgeCurrentTurnFailoverReplaysCompleteContextToSecondAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	firstCompleted := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_a","model":"gpt-5.6-sol","output":[{"type":"function_call","id":"fc_a","call_id":"call_a","name":"exec","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1}}}`,
		"",
	}, "\n")
	secondCompleted := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_b","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":3,"output_tokens":1}}}`,
		"",
	}, "\n")
	accountAUpstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(firstCompleted)),
		},
		{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"rate_limit_error","message":"limited"}}`)),
		},
	}}
	accountBUpstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(secondCompleted)),
	}}
	newService := func(upstream HTTPUpstream) *OpenAIGatewayService {
		cfg := &config.Config{}
		cfg.Security.URLAllowlist.Enabled = false
		cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		cfg.Gateway.OpenAIWS.Enabled = true
		cfg.Gateway.OpenAIWS.APIKeyEnabled = true
		cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
		cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
		cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
		return &OpenAIGatewayService{
			cfg:              cfg,
			httpUpstream:     upstream,
			cache:            &stubGatewayCache{},
			openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
			toolCorrector:    NewCodexToolCorrector(),
		}
	}
	newAccount := func(id int64) *Account {
		return &Account{
			ID: id, Name: "failover-account", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-upstream"},
			Extra:       map[string]any{"responses_websockets_v2_enabled": true},
		}
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}
		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r.Clone(r.Context())
		err = newService(accountAUpstream).ProxyResponsesWebSocketFromClient(
			r.Context(), ginCtx, conn, newAccount(101), "sk-a", firstMessage, nil, msgType,
		)
		var failoverErr *UpstreamFailoverError
		if !errors.As(err, &failoverErr) {
			serverErrCh <- err
			return
		}
		retryPayload, failedTurn, ok := OpenAIWSCurrentTurnRetryPayload(err)
		if !ok || failedTurn != 2 {
			serverErrCh <- errors.New("second turn did not produce a safe current-turn failover payload")
			return
		}
		ginCtxB, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtxB.Request = r.Clone(r.Context())
		serverErrCh <- newService(accountBUpstream).ProxyResponsesWebSocketFromClient(
			r.Context(), ginCtxB, conn, newAccount(202), "sk-b", retryPayload, nil, msgType,
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	write := func(payload string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(ctx, coderws.MessageText, []byte(payload)))
	}
	read := func() []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, event, readErr := clientConn.Read(ctx)
		require.NoError(t, readErr)
		return event
	}
	write(`{"type":"response.create","model":"gpt-5.6-sol","stream":true,"input":"first"}`)
	require.Equal(t, "resp_a", gjson.GetBytes(read(), "response.id").String())
	write(`{"type":"response.create","model":"gpt-5.6-sol","stream":true,"previous_response_id":"resp_a","input":[{"type":"function_call_output","call_id":"call_a","output":"ok"}]}`)
	require.Equal(t, "resp_b", gjson.GetBytes(read(), "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待双账号 WebSocket 换号测试结束超时")
	}
	require.Len(t, accountAUpstream.bodies, 2)
	require.Len(t, accountBUpstream.bodies, 1)
	require.False(t, gjson.GetBytes(accountBUpstream.bodies[0], "previous_response_id").Exists())
	replayedInput := gjson.GetBytes(accountBUpstream.bodies[0], "input").Array()
	require.Len(t, replayedInput, 3)
	require.Equal(t, "first", replayedInput[0].String())
	require.Equal(t, "function_call", replayedInput[1].Get("type").String())
	require.Equal(t, "function_call_output", replayedInput[2].Get("type").String())
	require.Equal(t, "call_a", replayedInput[2].Get("call_id").String())
}
