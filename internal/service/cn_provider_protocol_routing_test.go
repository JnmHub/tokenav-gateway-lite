package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func cnProtocolTestContext(path string, body []byte) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func cnProtocolTestAccount(protocol string) *Account {
	return &Account{
		ID:          701,
		Name:        "deepseek-protocol-test",
		Platform:    PlatformDeepseek,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": protocol,
		},
	}
}

func cnProtocolTestConfig() *config.Config {
	return &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
	}
}

type cnProtocolIngressCase struct {
	name    string
	path    string
	body    []byte
	forward func(*OpenAIGatewayService, *gin.Context, *Account, []byte) error
}

func cnFixedProtocolIngressCases() []cnProtocolIngressCase {
	return []cnProtocolIngressCase{
		{
			name: "Chat Completions 入口",
			path: "/v1/chat/completions",
			body: []byte(`{"model":"deepseek-chat","messages":[{"role":"user","content":"hello"}],"stream":false}`),
			forward: func(service *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := service.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				return err
			},
		},
		{
			name: "Messages 入口",
			path: "/v1/messages",
			body: []byte(`{"model":"deepseek-chat","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":false}`),
			forward: func(service *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := service.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
				return err
			},
		},
		{
			name: "Responses 入口",
			path: "/v1/responses",
			body: []byte(`{"model":"deepseek-chat","input":"hello","stream":false}`),
			forward: func(service *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := service.Forward(context.Background(), c, account, body)
				return err
			},
		},
	}
}

func TestFixedCNChatProtocolOverridesStaleResponsesMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range cnFixedProtocolIngressCases() {
		t.Run(test.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
			service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
			account := cnProtocolTestAccount(APIProtocolChatCompletions)
			account.Credentials["base_url"] = "http://chat.example"
			account.Extra = map[string]any{
				openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceResponses),
			}

			err := test.forward(service, cnProtocolTestContext(test.path, test.body), account, test.body)

			require.Error(t, err)
			require.NotNil(t, upstream.lastReq, "forward error: %v", err)
			require.Equal(t, "http://chat.example/v1/chat/completions", upstream.lastReq.URL.String())
		})
	}
}

func TestFixedCNResponsesProtocolOverridesStaleChatMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range cnFixedProtocolIngressCases() {
		t.Run(test.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
			service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
			account := cnProtocolTestAccount(APIProtocolResponses)
			account.Credentials["base_url"] = "http://responses.example"
			account.Extra = map[string]any{
				openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions),
			}

			err := test.forward(service, cnProtocolTestContext(test.path, test.body), account, test.body)

			require.Error(t, err)
			require.NotNil(t, upstream.lastReq, "forward error: %v", err)
			require.Equal(t, "http://responses.example/responses", upstream.lastReq.URL.String())
		})
	}
}

func TestAdaptiveDeepSeekResponsesKeepsNativeFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"deepseek-v4","input":"hello","instructions":"client instruction","max_output_tokens":32,"store":true,"previous_response_id":"resp_old","stream":false}`)
	upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
	service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
	account := cnProtocolTestAccount(APIProtocolAdaptive)
	account.Credentials["api_base_urls"] = map[string]any{
		APIProtocolResponses: "http://responses.example",
	}

	_, err := service.Forward(context.Background(), cnProtocolTestContext("/v1/responses", body), account, body)

	require.Error(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://responses.example/responses", upstream.lastReq.URL.String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
	require.Equal(t, "client instruction", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, int64(32), gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int())
}

// Kimi adaptive 账号的 Responses 请求同样路由到原生 Responses 端点（/v1 前缀
// 由 base URL 携带），并应用无状态归一化（store=false、清 previous_response_id）。
func TestAdaptiveKimiResponsesRoutesToNativeResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"kimi-k2.5","input":"hello","instructions":"client instruction","max_output_tokens":32,"store":true,"previous_response_id":"resp_old","stream":false}`)
	upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
	service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream}
	account := &Account{
		ID:          703,
		Name:        "kimi-adaptive-native-responses-test",
		Platform:    PlatformKimi,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": APIProtocolAdaptive,
			"api_base_urls": map[string]any{
				APIProtocolResponses: "http://responses.example/v1",
			},
		},
	}

	_, err := service.Forward(context.Background(), cnProtocolTestContext("/v1/responses", body), account, body)

	require.Error(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://responses.example/v1/responses", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "messages").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "previous_response_id").Exists())
	require.Equal(t, "client instruction", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, int64(32), gjson.GetBytes(upstream.lastBody, "max_output_tokens").Int())
}

func TestAdaptiveCNResponsesShapedChatIngressRestoresEncryptedReasoning(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model":"kimi-k2.5",
		"input":[
			{"type":"reasoning","id":"item_encrypted","summary":[],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}
		],
		"stream":false
	}`)
	upstream := &httpUpstreamRecorder{err: errors.New("stop after capture")}
	cache := &reasoningContentCacheTestStore{contents: map[string]string{"item_encrypted": "cached thinking"}}
	service := &OpenAIGatewayService{cfg: cnProtocolTestConfig(), httpUpstream: upstream, cache: cache}
	// 智谱无原生 Responses 端点，adaptive 下 Responses 形状仍走 Chat 桥接，
	// 才能覆盖桥接路径的 encrypted reasoning 还原（Kimi 已直连原生 Responses）。
	account := &Account{
		ID:          702,
		Name:        "zhipu-adaptive-reasoning-test",
		Platform:    PlatformZhipu,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":      "sk-test",
			"api_protocol": APIProtocolAdaptive,
			"api_base_urls": map[string]any{
				APIProtocolChatCompletions: "http://chat.example",
			},
		},
	}

	_, err := service.ForwardAsChatCompletions(
		context.Background(),
		cnProtocolTestContext("/v1/chat/completions", body),
		account,
		body,
		"",
		"",
	)

	require.Error(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "http://chat.example/v1/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, "cached thinking", gjson.GetBytes(upstream.lastBody, "messages.0.reasoning_content").String())
}
