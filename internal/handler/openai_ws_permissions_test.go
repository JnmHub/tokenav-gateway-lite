package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesWebSocketPermissionsEveryCreateAcrossAliases(t *testing.T) {
	for _, path := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
		for _, gate := range []string{"key", "allowlist", "legacy", "funding_strict"} {
			for _, phase := range []string{"first", "switch", "inherited"} {
				if gate == "funding_strict" && phase == "inherited" {
					continue
				}
				t.Run(path+"/"+gate+"/"+phase, func(t *testing.T) {
					gin.SetMode(gin.TestMode)
					var creates atomic.Int32
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						conn, err := coderws.Accept(w, r, nil)
						if err != nil {
							return
						}
						defer conn.CloseNow()
						for {
							ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
							_, payload, err := conn.Read(ctx)
							cancel()
							if err != nil {
								return
							}
							if gjson.GetBytes(payload, "type").String() != "response.create" {
								continue
							}
							n := creates.Add(1)
							ctx, cancel = context.WithTimeout(r.Context(), 3*time.Second)
							err = conn.Write(ctx, coderws.MessageText, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_permission_%d","model":"gpt-5.4","usage":{"input_tokens":1,"output_tokens":1}}}`, n)))
							cancel()
							if err != nil {
								return
							}
						}
					}))
					defer upstream.Close()
					groupID := int64(7)
					group := &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}
					key := &service.APIKey{ID: 0, UserID: 33, GroupID: &groupID, Group: group, User: &service.User{ID: 33}}
					switch gate {
					case "key":
						key.AllowedModels = []string{"gpt-5.4"}
					case "allowlist":
						group.ModelAllowlist = service.GroupModelAllowlist{Enabled: true, Models: []string{"gpt-5.4"}}
					case "legacy":
						group.ModelsListConfig = service.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-5.4"}}
					}
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Default.RateMultiplier = 1
					cfg.Security.URLAllowlist.AllowInsecureHTTP = true
					cfg.Gateway.OpenAIWS.Enabled = true
					cfg.Gateway.OpenAIWS.APIKeyEnabled = true
					cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
					cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
					cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
					cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
					cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
					// 账号明确支持两模型，证明拒绝来自客户权限，而非调度偶然找不到账号。
					account := service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 5,
						Credentials: map[string]any{"api_key": "test", "base_url": upstream.URL, "model_mapping": map[string]any{"gpt-5.4": "gpt-5.4", "gpt-5.5": "gpt-5.5"}},
						Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough}}
					require.True(t, account.IsModelSupported("gpt-5.5"))
					billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
					t.Cleanup(billingCache.Stop)
					gateway := service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, nil, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil)
					slots := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
					h := &OpenAIGatewayHandler{gatewayService: gateway, billingCacheService: billingCache, apiKeyService: &service.APIKeyService{}, concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, time.Second)}
					router := gin.New()
					router.Use(func(c *gin.Context) {
						if gate == "funding_strict" {
							// 卡资金在首帧前尚未选择；只安装逐轮准入闭包也必须阻断状态帧。
							c.Request = c.Request.WithContext(gatewaylite.ContextWithTurnAdmission(c.Request.Context(), func(context.Context, string, string) (gatewaylite.RequestMeta, error) {
								return gatewaylite.RequestMeta{}, nil
							}))
						}
						c.Set(string(middleware.ContextKeyAPIKey), key)
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 33, Concurrency: 5})
						c.Next()
					})
					router.GET(path, h.ResponsesWebSocket)
					server := httptest.NewServer(router)
					defer server.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
					cancel()
					require.NoError(t, err)
					defer conn.CloseNow()
					write := func(payload string) {
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
					}
					read := func() ([]byte, error) {
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						defer cancel()
						_, body, err := conn.Read(ctx)
						return body, err
					}
					if gate == "funding_strict" {
						if phase == "first" {
							write(`{"type":"session.update","session":{"model":"gpt-5.4"}}`)
						} else {
							write(`{"type":"response.create","max_output_tokens":32,"input":"model missing"}`)
						}
						_, err = read()
						require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
						require.Zero(t, creates.Load(), "卡资金握手首帧不允许状态穿透或收费")
						return
					}
					if phase != "first" {
						write(`{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
						body, err := read()
						require.NoError(t, err)
						require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
					}
					if phase == "inherited" {
						write(`{"type":"session.update","session":{"model":"gpt-5.5"}}`)
						write(`{"type":"response.create","input":"followup"}`)
					} else {
						write(`{"type":"response.create","model":"gpt-5.5","input":"denied"}`)
					}
					_, err = read()
					require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err), "必须按权限明确关闭；上游账号支持被禁止模型")
					var closeErr coderws.CloseError
					require.True(t, errors.As(err, &closeErr))
					_, reason := middleware.GatewayModelPermissionError(key, "gpt-5.5")
					require.Equal(t, reason, closeErr.Reason, "不能把其他策略关闭误判成模型权限生效")
					if phase == "first" {
						require.Zero(t, creates.Load())
					} else {
						require.EqualValues(t, 1, creates.Load(), "被拒绝的后续创建不得抵达上游")
					}
				})
			}
		}
	}
}
