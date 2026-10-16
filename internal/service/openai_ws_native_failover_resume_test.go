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
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSNativeCurrentTurnFailoverReplaysCompleteContextToSecondAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)

	accountAConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_native_a","model":"gpt-5.6-sol","output":[{"type":"function_call","id":"fc_native_a","call_id":"call_native_a","name":"exec","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1}}}`),
		[]byte(`{"type":"error","error":{"code":"rate_limit_exceeded","type":"rate_limit_error","message":"limited"}}`),
	}}
	accountBConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_native_b","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":3,"output_tokens":1}}}`),
	}}

	newService := func(conn openAIWSClientConn) (*OpenAIGatewayService, *openAIWSConnPool) {
		cfg := &config.Config{}
		cfg.Security.URLAllowlist.Enabled = false
		cfg.Security.URLAllowlist.AllowInsecureHTTP = true
		cfg.Gateway.OpenAIWS.Enabled = true
		cfg.Gateway.OpenAIWS.APIKeyEnabled = true
		cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
		cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
		cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
		cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
		cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
		cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
		cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
		pool := newOpenAIWSConnPool(cfg)
		pool.setClientDialerForTest(&openAIWSSingleConnDialer{conn: conn})
		return &OpenAIGatewayService{
			cfg:              cfg,
			httpUpstream:     &httpUpstreamRecorder{},
			cache:            &stubGatewayCache{},
			openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
			toolCorrector:    NewCodexToolCorrector(),
			openaiWSPool:     pool,
		}, pool
	}
	newAccount := func(id int64) *Account {
		return &Account{
			ID: id, Name: "native-failover-account", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-upstream"},
			Extra:       map[string]any{"responses_websockets_v2_enabled": true},
		}
	}

	serviceA, poolA := newService(accountAConn)
	defer poolA.Close()
	serviceB, poolB := newService(accountBConn)
	defer poolB.Close()

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer conn.CloseNow()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		messageType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}
		ginCtxA, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtxA.Request = r.Clone(r.Context())
		err = serviceA.ProxyResponsesWebSocketFromClient(
			r.Context(), ginCtxA, conn, newAccount(301), "sk-a", firstMessage, nil, messageType,
		)
		var failoverErr *UpstreamFailoverError
		if !errors.As(err, &failoverErr) {
			serverErrCh <- err
			return
		}
		retryPayload, failedTurn, ok := OpenAIWSCurrentTurnRetryPayload(err)
		if !ok || failedTurn != 2 {
			serverErrCh <- errors.New("native websocket second turn did not expose a safe retry payload")
			return
		}

		ginCtxB, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtxB.Request = r.Clone(r.Context())
		serverErrCh <- serviceB.ProxyResponsesWebSocketFromClient(
			r.Context(), ginCtxB, conn, newAccount(302), "sk-b", retryPayload, nil, messageType,
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer clientConn.CloseNow()

	write := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelWrite()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	read := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		_, event, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		return event
	}

	write(`{"type":"response.create","model":"gpt-5.6-sol","stream":true,"input":"first"}`)
	require.Equal(t, "resp_native_a", gjson.GetBytes(read(), "response.id").String())
	write(`{"type":"response.create","model":"gpt-5.6-sol","stream":true,"previous_response_id":"resp_native_a","input":[{"type":"function_call_output","call_id":"call_native_a","output":"ok"}]}`)
	require.Equal(t, "resp_native_b", gjson.GetBytes(read(), "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待原生 WebSocket 双账号换号测试结束超时")
	}

	accountAConn.mu.Lock()
	accountAWrites := append([]map[string]any(nil), accountAConn.writes...)
	accountAConn.mu.Unlock()
	require.Len(t, accountAWrites, 2)
	require.Equal(t, "first", gjson.Get(requestToJSONString(accountAWrites[0]), "input").String())
	require.True(t, gjson.Get(requestToJSONString(accountAWrites[1]), "previous_response_id").Exists())

	accountBConn.mu.Lock()
	accountBWrites := append([]map[string]any(nil), accountBConn.writes...)
	accountBConn.mu.Unlock()
	require.Len(t, accountBWrites, 1)
	retryRequest := requestToJSONString(accountBWrites[0])
	require.False(t, gjson.Get(retryRequest, "previous_response_id").Exists())
	replayedInput := gjson.Get(retryRequest, "input").Array()
	require.Len(t, replayedInput, 3)
	require.Equal(t, "first", replayedInput[0].String())
	require.Equal(t, "function_call", replayedInput[1].Get("type").String())
	require.Equal(t, "function_call_output", replayedInput[2].Get("type").String())
	require.Equal(t, "call_native_a", replayedInput[2].Get("call_id").String())
}
