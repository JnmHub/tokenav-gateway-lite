package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newHTTPClientWSBridgeTestService(enabled bool) *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{
		Gateway: config.GatewayConfig{
			OpenAIWS: config.GatewayOpenAIWSConfig{
				HTTPClientWSBridgeEnabled: enabled,
			},
		},
	}}
}

func TestOpenAIHTTPClientWSBridgeEnabled(t *testing.T) {
	require.True(t, newHTTPClientWSBridgeTestService(true).openAIHTTPClientWSBridgeEnabled())
	require.False(t, newHTTPClientWSBridgeTestService(false).openAIHTTPClientWSBridgeEnabled())

	// nil service / nil cfg 必须安全返回 false，保持默认「仅 WS 入站走 WS 上游」。
	var nilSvc *OpenAIGatewayService
	require.False(t, nilSvc.openAIHTTPClientWSBridgeEnabled())
	require.False(t, (&OpenAIGatewayService{}).openAIHTTPClientWSBridgeEnabled())
}

func TestShouldOpenAIWSBridgeFallbackToHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	wsErr := wrapOpenAIWSFallback("dial_failed", errors.New("dial tcp: connection refused"))

	newContext := func(alreadyWritten bool) *gin.Context {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		if alreadyWritten {
			// 必须通过 gin 的 Writer 写出，Written() 才能感知（直接写
			// httptest.Recorder 会绕过 gin 的状态追踪）。
			c.Writer.WriteHeader(200)
			c.Writer.WriteHeaderNow()
		}
		return c
	}

	t.Run("bridge_disabled_never_falls_back", func(t *testing.T) {
		svc := newHTTPClientWSBridgeTestService(false)
		require.False(t, svc.shouldOpenAIWSBridgeFallbackToHTTP(newContext(false), OpenAIClientTransportHTTP, wsErr))
	})

	t.Run("bridge_enabled_http_client_unwritten_falls_back", func(t *testing.T) {
		svc := newHTTPClientWSBridgeTestService(true)
		require.True(t, svc.shouldOpenAIWSBridgeFallbackToHTTP(newContext(false), OpenAIClientTransportHTTP, wsErr))
	})

	t.Run("already_written_bytes_never_replays", func(t *testing.T) {
		// 流中途已写出字节时无法安全重放，必须保持原错误返回。
		svc := newHTTPClientWSBridgeTestService(true)
		require.False(t, svc.shouldOpenAIWSBridgeFallbackToHTTP(newContext(true), OpenAIClientTransportHTTP, wsErr))
	})

	t.Run("ws_client_keeps_original_behavior", func(t *testing.T) {
		// 原生 WS 客户端失败不悄悄换成 HTTP 上游。
		svc := newHTTPClientWSBridgeTestService(true)
		require.False(t, svc.shouldOpenAIWSBridgeFallbackToHTTP(newContext(false), OpenAIClientTransportWS, wsErr))
	})

	t.Run("nil_error_never_falls_back", func(t *testing.T) {
		svc := newHTTPClientWSBridgeTestService(true)
		require.False(t, svc.shouldOpenAIWSBridgeFallbackToHTTP(newContext(false), OpenAIClientTransportHTTP, nil))
	})

	t.Run("nil_context_never_falls_back", func(t *testing.T) {
		svc := newHTTPClientWSBridgeTestService(true)
		require.False(t, svc.shouldOpenAIWSBridgeFallbackToHTTP(nil, OpenAIClientTransportHTTP, wsErr))
	})
}

// TestOpenAIGatewayService_Forward_HTTPIngressBridgesToWSWhenEnabled 验证开启桥接后
// HTTP 入站 + 账号 WS 开启时走 WS 上游（网关做协议转换），而不是被 client_protocol_http 降级。
func TestOpenAIGatewayService_Forward_HTTPIngressBridgesToWSWhenEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var wsAttempts atomic.Int32
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsAttempts.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket failed: %v", err)
			return
		}
		defer func() {
			_ = conn.Close()
		}()

		var req map[string]any
		if err := conn.ReadJSON(&req); err != nil {
			t.Errorf("read ws request failed: %v", err)
			return
		}
		_ = conn.WriteJSON(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":    "resp_http_bridge_ws_ok",
				"model": "gpt-5.3-codex",
				"usage": map[string]any{
					"input_tokens":  3,
					"output_tokens": 4,
					"input_tokens_details": map[string]any{
						"cached_tokens": 0,
					},
				},
			},
		})
	}))
	defer wsServer.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_http_should_not_happen","usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPClientWSBridgeEnabled = true

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
	}

	account := &Account{
		ID:          121,
		Name:        "openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": wsServer.URL,
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	body := []byte(`{"model":"gpt-5.3-codex","stream":false,"input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.OpenAIWSMode, "桥接开启后 HTTP 入站应走 WS 上游")
	require.Equal(t, "resp_http_bridge_ws_ok", result.RequestID)
	require.Nil(t, upstream.lastReq, "桥接开启后不应走 HTTP 上游")
	require.Equal(t, int32(1), wsAttempts.Load(), "应命中一次 WS 上游")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "resp_http_bridge_ws_ok", gjson.Get(rec.Body.String(), "id").String())

	decision, _ := c.Get("openai_ws_transport_decision")
	reason, _ := c.Get("openai_ws_transport_reason")
	require.Equal(t, string(OpenAIUpstreamTransportResponsesWebsocketV2), decision)
	require.NotEqual(t, "client_protocol_http", reason)
}

// TestOpenAIGatewayService_Forward_HTTPIngressBridgeFallsBackToHTTPOnWSError 验证桥接模式下
// WS 失败（握手 426）且未向客户端写出字节时，回落同账号 HTTP 重放并正常返回。
func TestOpenAIGatewayService_Forward_HTTPIngressBridgeFallsBackToHTTPOnWSError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var wsAttempts atomic.Int32
	ws426Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "responses") && r.Header.Get("Upgrade") != "" {
			wsAttempts.Add(1)
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`upgrade required`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_http_bridge_fallback_ok","usage":{"input_tokens":5,"output_tokens":6,"input_tokens_details":{"cached_tokens":0}}}`))
	}))
	defer ws426Server.Close()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_http_bridge_fallback_ok","usage":{"input_tokens":5,"output_tokens":6,"input_tokens_details":{"cached_tokens":0}}}`)),
		},
	}

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPClientWSBridgeEnabled = true
	cfg.Gateway.OpenAIWS.FallbackCooldownSeconds = 1

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
	}

	account := &Account{
		ID:          122,
		Name:        "openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": ws426Server.URL,
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err, "WS 失败未写出字节时应回落 HTTP 成功")
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode, "回落后应按 HTTP 模式记录")
	require.Equal(t, int32(1), wsAttempts.Load(), "应先尝试一次 WS")
	require.NotNil(t, upstream.lastReq, "WS 失败后应回落 HTTP 上游")
	require.Equal(t, http.StatusOK, rec.Code, "客户端应收到 HTTP 200 而不是 WS 错误")
	require.Equal(t, "resp_http_bridge_fallback_ok", gjson.Get(rec.Body.String(), "id").String())
}

// TestOpenAIGatewayService_Forward_HTTPIngressBridgeOffKeepsWSClientBehavior 验证桥接关闭时
// HTTP 入站仍被降级（保护其他网关默认行为，不产生副作用）。
func TestOpenAIGatewayService_Forward_HTTPIngressBridgeOffKeepsWSClientBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_bridge_off_http_ok","usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`)),
		},
	}

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPClientWSBridgeEnabled = false

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
	}

	account := &Account{
		ID:          123,
		Name:        "openai-apikey",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "http://127.0.0.1:1", // 不可用地址，验证根本不会发起 WS
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`)
	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.OpenAIWSMode, "桥接关闭时 HTTP 入站保持 HTTP 上游")
	require.NotNil(t, upstream.lastReq, "桥接关闭时应直接命中 HTTP 上游")
}
