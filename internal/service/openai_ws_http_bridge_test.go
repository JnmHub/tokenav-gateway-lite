package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPrepareOpenAIWSHTTPBridgeBodyStripsWSFields(t *testing.T) {
	body, err := prepareOpenAIWSHTTPBridgeBody([]byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":false,"previous_response_id":"resp_prev","input":"hi"}`))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(body, "type").Exists())
	require.False(t, gjson.GetBytes(body, "generate").Exists())
	require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
	require.Equal(t, "gpt-5", gjson.GetBytes(body, "model").String())
	require.True(t, gjson.GetBytes(body, "stream").Bool())
	require.Equal(t, "hi", gjson.GetBytes(body, "input").String())
}

func TestProxyOpenAIWSHTTPBridgeAPIKeyRestoresClientTools(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sse := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","status":"in_progress"}}`,
		"",
		`event: response.function_call_arguments.done`,
		`data: {"type":"response.function_call_arguments.done","sequence_number":1,"output_index":0,"item_id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"}`,
		"",
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}","status":"completed"}}`,
		"",
		`event: response.done`,
		`data: {"type":"response.done","sequence_number":3,"response":{"id":"resp_tools","status":"completed","output":[{"type":"function_call","id":"item_exec","call_id":"call_exec","name":"exec","arguments":"{\"input\":\"pwd\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID: 918002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-client-tools-test"},
	}
	payload := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":"run tools","tools":[{"type":"custom","name":"exec"}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	var events [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-client-tools-test", payload, len(payload),
		"gpt-5.6-sol", "", "", "", "", 1,
		func(message []byte) error {
			events = append(events, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.Equal(t, "exec", gjson.GetBytes(upstream.lastBody, "tools.0.name").String())
	var outputDone, terminal []byte
	for _, event := range events {
		switch gjson.GetBytes(event, "type").String() {
		case "response.output_item.done":
			outputDone = event
		case "response.done":
			terminal = event
		}
	}
	require.Equal(t, "custom_tool_call", gjson.GetBytes(outputDone, "item.type").String())
	require.Equal(t, "pwd", gjson.GetBytes(outputDone, "item.input").String())
	require.Equal(t, "custom_tool_call", gjson.GetBytes(terminal, "response.output.0.type").String())
	require.Equal(t, "pwd", gjson.GetBytes(terminal, "response.output.0.input").String())
}

func TestProxyOpenAIWSHTTPBridgeSeparatesClientAndUpstreamResponseModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const requestedModel = "gpt-5.6-sol"
	const mappedModel = "gpt-5.6-luna"
	sse := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_model_policy","model":"` + mappedModel + `","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID: 918003, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-model-policy-test"},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestCtx := context.WithValue(context.Background(), ctxkey.Group, &Group{
		ModelsListConfig: GroupModelsListConfig{ResponseModelMode: ResponseModelModeRequest},
	})
	requestCtx = WithClientRequestedResponseModel(requestCtx, requestedModel)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(requestCtx)
	beginClientResponseModelPolicy(c, requestCtx, account, requestedModel)

	var events [][]byte
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		requestCtx, c, account, "sk-model-policy-test",
		[]byte(`{"type":"response.create","model":"`+mappedModel+`","input":"hi"}`),
		128, requestedModel, "", "", "", "", 1,
		func(message []byte) error {
			events = append(events, append([]byte(nil), message...))
			return nil
		},
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, events, 1)
	require.Equal(t, requestedModel, gjson.GetBytes(events[0], "response.model").String(),
		"客户端响应必须保持用户请求模型，不能泄露映射后的上游模型")
	require.Equal(t, mappedModel, result.UpstreamResponseModel)
	require.Equal(t, mappedModel, result.UpstreamModel)
	require.NotNil(t, result.UpstreamModelMismatch)
	require.False(t, *result.UpstreamModelMismatch,
		"实际发送模型与上游声明一致时不应标记 mismatch")
}

func TestOpenAIWSHTTPBridgeDecisionKeepsSmallFramesOnWS(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAIWS: config.GatewayOpenAIWSConfig{
					HTTPBridgeEnabled:        true,
					HTTPBridgeThresholdBytes: 100,
				},
			},
		},
	}

	require.False(t, svc.shouldBridgeOpenAIWSHTTP(nil, 99, ""))
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(nil, 100, ""))
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(nil, 1000, "resp_existing"))

	svc.cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = false
	require.False(t, svc.shouldBridgeOpenAIWSHTTP(nil, 1000, ""))
	require.True(t, svc.shouldBridgeOpenAIWSHTTP(&Account{Platform: PlatformGrok}, 1, "resp_existing"))
}

func TestForceGrokOpenAIWSHTTPBridgeRequiresGrokAccount(t *testing.T) {
	require.True(t, forceGrokOpenAIWSHTTPBridge(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}))
	require.True(t, forceGrokOpenAIWSHTTPBridge(&Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}))
	require.False(t, forceGrokOpenAIWSHTTPBridge(&Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}))
	require.False(t, forceGrokOpenAIWSHTTPBridge(&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
}

func TestOpenAIWSHTTPBridgeCapacityShedDoesNotCooldownAccountOrModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newOverloadResponse := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"server_is_overloaded","message":"try again"}}`)),
		}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{newOverloadResponse(), newOverloadResponse()}}
	svc := &OpenAIGatewayService{
		cfg:              &config.Config{},
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
	}
	account := &Account{
		ID:          8,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"api_key":       "sk-test",
			"model_mapping": map[string]any{"customer-alias": "gpt-5.6-sol"},
		},
	}
	payload := []byte(`{"type":"response.create","model":"customer-alias","input":"hi"}`)

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
			context.Background(), c, account, "sk-test", payload, len(payload),
			"customer-alias", "", "", "", "", i+1, func([]byte) error { return nil },
		)
		require.Error(t, err)
	}

	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "customer-alias"))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIWSHTTPBridgeCapacityShedReturnsRequestScopedSameAccountRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{"server_is_overloaded", "slow_down"} {
		t.Run(code, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"` + code + `","message":"try again"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: &RateLimitService{}}
			account := &Account{ID: 81, Name: "ws-capacity", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			written := 0

			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5.6","input":"hi"}`), 64,
				"gpt-5.6", "", "", "", "", 1,
				func([]byte) error { written++; return nil },
			)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.True(t, failoverErr.RequestScopedTransient)
			require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
			require.Zero(t, written, "容量降载必须在透明重试前保持对客户端不可见")
			require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6"))
		})
	}
}

func TestOpenAIWSHTTPBridgeCapacityShedSSEEventReturnsRequestScopedRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, code := range []string{"server_is_overloaded", "slow_down"} {
		t.Run(code, func(t *testing.T) {
			sse := "data: {\"type\":\"error\",\"error\":{\"code\":\"" + code + "\",\"message\":\"try again\"}}\n\n"
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, rateLimitService: &RateLimitService{}}
			account := &Account{ID: 82, Name: "ws-capacity-sse", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			written := 0

			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test",
				[]byte(`{"type":"response.create","model":"gpt-5.6","input":"hi"}`), 64,
				"gpt-5.6", "", "", "", "", 1,
				func([]byte) error { written++; return nil },
			)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.True(t, failoverErr.RequestScopedTransient)
			require.Zero(t, written)
			require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.6"))
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnRewritesCapacityShedCodeForClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		turn    int
		body    string
		wantErr bool
	}{
		{
			name:    "turn2_error_frame",
			turn:    2,
			body:    "data: {\"type\":\"error\",\"error\":{\"type\":\"service_unavailable_error\",\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}\n\n",
			wantErr: true,
		},
		{
			name: "turn2_response_failed",
			turn: 2,
			body: "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_shed\",\"status\":\"failed\",\"error\":{\"code\":\"slow_down\",\"message\":\"Our servers are currently overloaded. Please try again later.\"}}}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 83, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
			payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
			var writes [][]byte

			_, err := svc.proxyOpenAIWSHTTPBridgeTurn(
				context.Background(), c, account, "sk-test", payload, len(payload),
				"gpt-5", "", "", "", "", tt.turn,
				func(message []byte) error {
					writes = append(writes, append([]byte(nil), message...))
					return nil
				},
			)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, writes, 1)
			require.Contains(t, string(writes[0]), `"code":"server_error"`)
			require.NotContains(t, string(writes[0]), "server_is_overloaded")
			require.NotContains(t, string(writes[0]), "slow_down")
			require.Contains(t, string(writes[0]), "Our servers are currently overloaded")
		})
	}
}

func TestProxyOpenAIWSHTTPBridgeTurnStagesMetadataBeforeCapacityFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_shed"}}`,
		"",
		`data: {"type":"response.in_progress","response":{"id":"resp_shed"}}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_shed","status":"failed","error":{"message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"rid-ws-bridge-capacity"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 84, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", "", 1,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.RequestScopedTransient)
	require.Empty(t, writes)
}

func TestProxyOpenAIWSHTTPBridgeTurnDoesNotFailoverCapacityAfterSemanticOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureStructuredLog(t)
	defer restore()
	body := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_partial"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		"",
		`data: {"type":"response.failed","response":{"id":"resp_partial","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"rid-ws-bridge-post-output"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 85, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	var writes [][]byte

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", "", 1,
		func(message []byte) error {
			writes = append(writes, append([]byte(nil), message...))
			return nil
		},
	)

	require.NotNil(t, result)
	require.NoError(t, err)
	require.Len(t, writes, 3)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Contains(t, string(writes[2]), `"code":"server_error"`)
	require.NotContains(t, string(writes[2]), "server_is_overloaded")
	require.True(t, logSink.ContainsMessage("gateway.failover_suppressed_after_semantic_output"))
	require.True(t, logSink.ContainsFieldValue("path", "ws_http_bridge"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-ws-bridge-post-output"))
}

func TestProxyOpenAIWSHTTPBridgeTurnRejectsOversizedFirstOutputStage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	metadata := strings.Repeat("x", openAIFirstOutputStageMaxBytes+1)
	body := `data: {"type":"response.created","response":{"id":"resp_oversized","metadata":"` + metadata + `"}}` + "\n\n"
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"rid-ws-bridge-oversized"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 86, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"gpt-5","input":"hi"}`)
	written := 0

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c, account, "sk-test", payload, len(payload),
		"gpt-5", "", "", "", "", 1,
		func([]byte) error {
			written++
			return nil
		},
	)

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Zero(t, written)
}

func TestOpenAIWSHTTPBridgeRelaysSSEFramesAsWebSocketMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_bridge","model":"gpt-5"}}`,
		"",
		`data: {"type":"response.output_text.delta","response":{"id":"resp_bridge"},"delta":"ok"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_bridge","model":"gpt-5","output":[{"id":"ig_bridge_1","type":"image_generation_call","status":"in_progress","result":"final-image"}],"usage":{"input_tokens":3,"output_tokens":2}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				MaxLineSize: defaultMaxLineSize,
				OpenAIWS: config.GatewayOpenAIWSConfig{
					HTTPBridgeEnabled:        true,
					HTTPBridgeThresholdBytes: 1,
				},
			},
		},
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          7,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Status:      StatusActive,
	}
	payload := []byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":true,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":"hi"}`)

	type bridgeResult struct {
		result *OpenAIForwardResult
		err    error
	}
	resultCh := make(chan bridgeResult, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			resultCh <- bridgeResult{err: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		ginCtx.Request = req

		writeClient := func(message []byte) error {
			writeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			return conn.Write(writeCtx, coderws.MessageText, message)
		}
		result, bridgeErr := svc.proxyOpenAIWSHTTPBridgeTurn(
			r.Context(),
			ginCtx,
			account,
			"sk-test",
			payload,
			len(payload),
			"gpt-5",
			"",
			"",
			"",
			"",
			1,
			writeClient,
		)
		resultCh <- bridgeResult{result: result, err: bridgeErr}
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	readEvent := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	created := readEvent()
	delta := readEvent()
	completed := readEvent()

	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(delta, "type").String())
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	require.Equal(t, "completed", gjson.GetBytes(completed, "response.output.0.status").String())

	select {
	case bridge := <-resultCh:
		require.NoError(t, bridge.err)
		require.NotNil(t, bridge.result)
		require.Equal(t, "resp_bridge", bridge.result.RequestID)
		require.Equal(t, 3, bridge.result.Usage.InputTokens)
		require.Equal(t, 2, bridge.result.Usage.OutputTokens)
		require.True(t, bridge.result.OpenAIWSMode)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bridge result")
	}

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Equal(t, "true", upstream.lastReq.Header.Get(responsesLiteHeader))
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
}

func TestOpenAIWSHTTPBridgePriorityUnsupportedRetriesStandardBeforeOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	standardSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_standard_retry","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","code":"unsupported_parameter","message":"Unsupported parameter: service_tier"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(standardSSEBody)),
		},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          71,
		Name:        "api-key-http-bridge-priority-retry",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *OpenAIForwardResult, 1)
	hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
		if turnErr == nil && result != nil {
			resultCh <- result
		}
	}}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancelRead()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = r.Clone(r.Context())
		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"service_tier":"priority","input":"hi"}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	require.Equal(t, "resp_bridge_standard_retry", gjson.GetBytes(event, "response.id").String())
	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 HTTP bridge Priority 标准回退会话结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_bridge_standard_retry", result.RequestID)
		require.Nil(t, result.ServiceTier)
		require.Nil(t, result.SentServiceTier)
		require.Nil(t, result.ResponseServiceTier)
		require.NotNil(t, result.RequestedServiceTier)
		require.Equal(t, "priority", *result.RequestedServiceTier)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 HTTP bridge Priority 标准回退结果")
	}

	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "priority", gjson.GetBytes(upstream.bodies[0], "service_tier").String())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "service_tier").Exists())
}

func TestOpenAIWSHTTPBridgeSuppressesPriorityUnsupportedErrorEventBeforeOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	errorSSEBody := strings.Join([]string{
		`data: {"type":"error","error":{"type":"invalid_request_error","code":"unsupported_parameter","message":"Unsupported parameter: service_tier"}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(errorSSEBody)),
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          72,
		Name:        "api-key-http-bridge-priority-event",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	written := 0

	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(),
		ginCtx,
		account,
		"sk-test",
		[]byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"service_tier":"priority","input":"hi"}`),
		100,
		"gpt-5.1",
		"",
		"",
		"",
		"",
		1,
		func([]byte) error {
			written++
			return nil
		},
	)

	require.Error(t, err)
	require.True(t, isOpenAIWSIngressServiceTierUnsupported(err))
	require.NotNil(t, result)
	require.Zero(t, written, "Priority 不支持错误必须在标准重试前保持对客户端不可见")
}

func TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sseBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_large_bridge","model":"gpt-5"}}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_large_bridge","model":"gpt-5","usage":{"input_tokens":9,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_large_bridge"},
		},
		Body: io.NopCloser(strings.NewReader(sseBody)),
	}}
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
			OpenAIWS: config.GatewayOpenAIWSConfig{
				Enabled:                  true,
				APIKeyEnabled:            true,
				ResponsesWebsocketsV2:    true,
				ClientReadLimitBytes:     64 * 1024 * 1024,
				HTTPBridgeEnabled:        true,
				HTTPBridgeThresholdBytes: 15 * 1024 * 1024,
			},
		},
	}
	svc := &OpenAIGatewayService{
		cfg:           cfg,
		httpUpstream:  upstream,
		toolCorrector: NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          9,
		Name:        "api-key",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
		Concurrency: 1,
		Status:      StatusActive,
	}

	payload := []byte(`{"type":"response.create","generate":true,"model":"gpt-5","stream":true,"input":"` + strings.Repeat("x", 17*1024*1024) + `"}`)
	require.Greater(t, len(payload), 16*1024*1024)
	require.Less(t, int64(len(payload)), ResolveOpenAIWSClientReadLimitBytes(cfg))

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(ResolveOpenAIWSClientReadLimitBytes(cfg))

		readCtx, cancelRead := context.WithTimeout(r.Context(), 10*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			errCh <- NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unexpected client websocket message type", nil)
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.135.0")
		ginCtx.Request = req

		proxyCtx, cancelProxy := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancelProxy()
		errCh <- svc.ProxyResponsesWebSocketFromClient(proxyCtx, ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 20*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, payload)
	cancelWrite()
	require.NoError(t, err)

	var eventTypes []string
	for {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 10*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)

		eventType := gjson.GetBytes(event, "type").String()
		eventTypes = append(eventTypes, eventType)
		if eventType == "response.completed" {
			break
		}
	}
	require.Contains(t, eventTypes, "response.created")
	require.Contains(t, eventTypes, "response.completed")

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, http.MethodPost, upstream.lastReq.Method)
	require.Greater(t, len(upstream.lastBody), 16*1024*1024)
	require.False(t, gjson.GetBytes(upstream.lastBody, "type").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "generate").Exists())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "gpt-5", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestOpenAIWSHTTPBridgeKeepsContinuationFramesOnHTTPWithoutPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	firstSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_first","model":"gpt-5.1","output":[{"type":"function_call","id":"fc_bridge_1","call_id":"call_bridge_1","name":"shell","arguments":"{}"}],"usage":{"input_tokens":9,"output_tokens":1}}}`,
		"",
	}, "\n")
	secondSSEBody := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_bridge_second","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`,
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(firstSSEBody)),
		},
		{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(secondSSEBody)),
		},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.HTTPBridgeEnabled = true
	cfg.Gateway.OpenAIWS.HTTPBridgeThresholdBytes = 1
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     upstream,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	account := &Account{
		ID:          19,
		Name:        "api-key-bridge-handoff",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-upstream"},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
	}

	errCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			errCh <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			errCh <- err
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			errCh <- NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "unexpected client websocket message type", nil)
			return
		}

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "codex_cli_rs/0.135.0")
		ginCtx.Request = req

		errCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	writeMessage := func(payload string) {
		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelWrite()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelRead()
		msgType, event, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return event
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":"first"}`)
	firstTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurnEvent, "type").String())
	require.Equal(t, "resp_bridge_first", gjson.GetBytes(firstTurnEvent, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_bridge_first","input":[{"type":"function_call_output","call_id":"call_bridge_1","output":"ok"}]}`)
	secondTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurnEvent, "type").String())
	require.Equal(t, "resp_bridge_second", gjson.GetBytes(secondTurnEvent, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-errCh:
		require.NoError(t, proxyErr)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket bridge proxy to finish")
	}

	require.Len(t, upstream.bodies, 2, "进入 HTTP bridge 后同一客户端 WS 连接内应保持 HTTP/SSE bridge")
	require.False(t, gjson.GetBytes(upstream.bodies[0], "previous_response_id").Exists())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "previous_response_id").Exists())
	secondInput := gjson.GetBytes(upstream.bodies[1], "input").Array()
	require.Len(t, secondInput, 3)
	require.Equal(t, "first", secondInput[0].String())
	require.Equal(t, "function_call", secondInput[1].Get("type").String())
	require.Equal(t, "call_bridge_1", secondInput[1].Get("call_id").String())
	require.Equal(t, "function_call_output", secondInput[2].Get("type").String())
	require.Equal(t, "call_bridge_1", secondInput[2].Get("call_id").String())
	require.Equal(t, 0, captureDialer.DialCount())
	require.Empty(t, captureConn.writes)
}
