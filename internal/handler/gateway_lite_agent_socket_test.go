package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const agentWSKey = "tokenav_sk_agentws_synthetic"

// 只公开普通 Key 的真实 HTTP 解析/租约能力；测试不得走委托凭证逻辑。
type agentWSControl struct {
	client *gatewaylite.ControlPlaneClient
}

func (c agentWSControl) ResolveKey(ctx context.Context, in gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	return c.client.ResolveKey(ctx, in)
}
func (c agentWSControl) AcquireLease(ctx context.Context, in gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	return c.client.AcquireLease(ctx, in)
}
func (c agentWSControl) RebalanceLease(ctx context.Context, in gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	return c.client.RebalanceLease(ctx, in)
}
func (c agentWSControl) ReportUsage(ctx context.Context, in gatewaylite.UsageEvent) error {
	return c.client.ReportUsage(ctx, in)
}

type agentWSSnapshotServer struct {
	mu       sync.Mutex
	key      gatewaylite.KeySnapshot
	resolves []gatewaylite.ResolveKeyRequest
	leases   int
	fail     bool
}

func (s *agentWSSnapshotServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/internal/key/resolve":
		var in gatewaylite.ResolveKeyRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.KeyID != "agentws" || in.Region != "agent-ws-node" || in.GatewayCode != "agent-ws-node" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.resolves = append(s.resolves, in)
		if s.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(gatewaylite.ResolveKeyResponse{OK: true, Key: s.key})
	case "/internal/quota/acquire-lease":
		var in gatewaylite.AcquireLeaseRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.KeyID != "agentws" || in.UserID != 33 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.leases++
		_ = json.NewEncoder(w).Encode(gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
			LeaseID: "agent-ws-lease", UserID: 33, BillingSubjectType: "user", BillingSubjectID: 33,
			Region: in.Region, AllocatedCents: 100000, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		}})
	case "/internal/usage/report":
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *agentWSSnapshotServer) change(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch state {
	case "app_revoked", "grant_revoked", "user_disabled", "key_revoked", "before_first_turn":
		s.key.Status = "inactive"
		// 无线路是撤销后的正常形态；不可覆盖 inactive 判断。
		s.key.Group, s.key.AvailableGateways = nil, nil
	case "model_scope":
		s.key.AllowedModels = []string{"different-model"}
	case "line_scope":
		s.key.AvailableGateways = nil
	case "line_binding":
		s.key.Group.ID++
	case "secret_rotated":
		digest := sha256.Sum256([]byte("different-synthetic-secret"))
		s.key.SecretHash = hex.EncodeToString(digest[:])
	case "expired":
		s.key.ExpiresAt = time.Now().Add(-time.Second).Unix()
	case "unavailable":
		s.fail = true
	}
}

func TestGatewayLiteAgentResponsesWebSocketLiveTurns(t *testing.T) {
	cases := []string{"two_allowed_turns", "app_revoked", "grant_revoked", "user_disabled", "key_revoked", "model_scope", "line_scope", "line_binding", "secret_rotated", "expired", "unavailable", "before_first_turn", "revoke_inflight", "ordinary_cache", "unknown_use_cache"}
	for _, path := range []string{"/v1/responses", "/responses"} {
		for _, state := range cases {
			t.Run(path+"/"+state, func(t *testing.T) { runAgentWSLiveTurns(t, path, state) })
		}
	}
}

func runAgentWSLiveTurns(t *testing.T, path, state string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	digest := sha256.Sum256([]byte("synthetic"))
	fake := &agentWSSnapshotServer{key: gatewaylite.KeySnapshot{
		KeyID: "agentws", UserID: 33, TokenUse: "agent_api_key", CacheTTLSecond: 0,
		SecretHash: hex.EncodeToString(digest[:]), Status: "active", Platform: service.PlatformOpenAI,
		BillingSubjectType: "user", BillingSubjectID: 33, Concurrency: 4, AllowedModels: []string{"gpt-5.4"},
		GroupID: 50, KeyDefaultGatewayNodeID: 5, GatewayStrategy: "fixed_group", GatewayAccessEnforced: true,
		AvailableGateways: []gatewaylite.GatewayRouteSummary{{ID: 5, Code: "agent-ws-node"}},
		Group: &gatewaylite.GatewayGroupSnapshot{ID: 50, LocalGroupID: 7, GatewayNodeID: 5, GatewayCode: "agent-ws-node",
			Name: "synthetic-line", Platform: service.PlatformOpenAI, Status: "active", RateMultiplier: 1},
	}}
	if state == "ordinary_cache" {
		fake.key.TokenUse = "api_key"
	} else if state == "unknown_use_cache" {
		fake.key.TokenUse = "unknown_future_use"
	}
	controlServer := httptest.NewServer(http.HandlerFunc(fake.serve))
	defer controlServer.Close()
	client, err := gatewaylite.NewControlPlaneClient(controlServer.URL, "synthetic-control", time.Second)
	require.NoError(t, err)
	control := agentWSControl{client}
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()
	const prefix = "agent-ws-test"
	quota := gatewaylite.NewRedisQuota(redisClient, prefix)
	keyCache := gatewaylite.NewRedisKeyCache(redisClient, prefix)
	reporter := gatewaylite.NewRedisUsageReporter(redisClient, prefix, control)
	runtime := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "synthetic", GatewayCode: "agent-ws-node", RedisPrefix: prefix})
	oldControl, oldQuota, oldRuntime, oldReporter := gatewaylite.DefaultControlPlaneClientRef(), gatewaylite.DefaultRedisQuota(), gatewaylite.DefaultRuntimeConfigRef(), gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultControlPlaneClientRef(nil)
	gatewaylite.SetDefaultRedisQuota(quota)
	gatewaylite.SetDefaultRuntimeConfigRef(runtime)
	gatewaylite.SetDefaultUsageReporter(reporter)
	defer func() {
		gatewaylite.SetDefaultControlPlaneClientRef(oldControl)
		gatewaylite.SetDefaultRedisQuota(oldQuota)
		gatewaylite.SetDefaultRuntimeConfigRef(oldRuntime)
		gatewaylite.SetDefaultUsageReporter(oldReporter)
	}()
	var creates atomic.Int32
	dispatched := make(chan []byte, 4)
	finishFirst := make(chan struct{})
	var finishOnce sync.Once
	releaseFirst := func() { finishOnce.Do(func() { close(finishFirst) }) }
	defer releaseFirst()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			readCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			_, payload, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			if gjson.GetBytes(payload, "type").String() != "response.create" {
				continue
			}
			n := creates.Add(1)
			dispatched <- append([]byte(nil), payload...)
			if state == "revoke_inflight" && n == 1 {
				select {
				case <-finishFirst:
				case <-r.Context().Done():
					return
				}
			}
			writeCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			err = conn.Write(writeCtx, coderws.MessageText, []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_agent_%d","model":"gpt-5.4","usage":{"input_tokens":2000,"output_tokens":1000}}}`, n)))
			cancel()
			if err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{RunMode: config.RunModeGatewayLite}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled = true, true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds, cfg.Gateway.OpenAIWS.ReadTimeoutSeconds, cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3, 5, 3
	account := service.Account{ID: 4242, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 4, GroupIDs: []int64{7},
		Credentials: map[string]any{"api_key": "synthetic-upstream", "base_url": upstream.URL, "model_mapping": map[string]any{"gpt-5.4": "gpt-5.4"}},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough}}
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	defer billingCache.Stop()
	logs := &oauthInvocationSocketLogs{created: make(chan *service.UsageLog, 4)}
	gateway := service.NewOpenAIGatewayService(&openAIWSUsageHandlerAccountRepoStub{account: account}, logs, &oauthInvocationSocketUsageRepo{}, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil)
	defer gateway.CloseOpenAIWSPool()
	slots := &concurrencyCacheMock{acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }, acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil }}
	h := &OpenAIGatewayHandler{gatewayService: gateway, billingCacheService: billingCache, apiKeyService: &service.APIKeyService{}, cfg: cfg,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(slots), SSEPingFormatNone, time.Second)}
	router := gin.New()
	router.Use(gin.HandlerFunc(middleware.NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfig(control, runtime, quota, keyCache, reporter)))
	handlerDone := make(chan struct{})
	router.GET(path, func(c *gin.Context) { defer close(handlerDone); h.ResponsesWebSocket(c) })
	server := httptest.NewServer(router)
	defer server.Close()
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	conn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &coderws.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + agentWSKey}}})
	cancel()
	require.NoError(t, err)
	defer func() {
		releaseFirst()
		_ = conn.CloseNow()
		select {
		case <-handlerDone:
		case <-time.After(5 * time.Second):
			t.Error("普通 Responses WebSocket 处理器未退出")
		}
	}()
	require.Zero(t, redisClient.XLen(context.Background(), prefix+":usage:stream").Val(), "握手不能创建收费轮次")
	write := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.4","max_output_tokens":2048,"input":"synthetic"}`)))
	}
	read := func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, payload, err := conn.Read(ctx)
		return payload, err
	}
	if state == "before_first_turn" {
		fake.change(state)
		write()
		_, err := read()
		require.Error(t, err)
		require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
		require.Zero(t, creates.Load())
		fake.mu.Lock()
		require.Len(t, fake.resolves, 2, "首轮也必须重新检查握手后的撤销")
		require.Zero(t, fake.leases)
		fake.mu.Unlock()
		return
	}
	write()
	if state == "revoke_inflight" {
		select {
		case payload := <-dispatched:
			require.Equal(t, "gpt-5.4", gjson.GetBytes(payload, "model").String())
		case <-time.After(3 * time.Second):
			t.Fatal("等待已准入真实上游轮次超时")
		}
		fake.change("grant_revoked")
		releaseFirst()
	}
	body, err := read()
	require.NoError(t, err, "撤销不能取消已在途轮次")
	require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
	collect := func(expected int) (gatewaylite.UsageEvent, int64) {
		select {
		case log := <-logs.created:
			require.Greater(t, log.ActualCost, 0.0)
			require.Equal(t, 2000, log.InputTokens)
		case <-time.After(3 * time.Second):
			t.Fatal("实际已完成轮次未生成用量")
		}
		var messages []redis.XMessage
		require.Eventually(t, func() bool {
			var err error
			messages, err = redisClient.XRange(context.Background(), prefix+":usage:stream", "-", "+").Result()
			return err == nil && len(messages) == expected
		}, 3*time.Second, 10*time.Millisecond)
		var queued struct {
			Event gatewaylite.UsageEvent `json:"event"`
		}
		require.NoError(t, json.Unmarshal([]byte(messages[expected-1].Values["event"].(string)), &queued))
		event := queued.Event
		require.Equal(t, "agentws", event.KeyID)
		require.EqualValues(t, 33, event.UserID)
		require.Equal(t, "agent-ws-lease", event.LeaseID)
		require.EqualValues(t, 7, event.GatewayGroupID)
		require.Nil(t, event.OAuthInvocationReservation, "Agent 普通 Key 不得走委托凭证结算")
		request, err := redisClient.HGetAll(context.Background(), prefix+":req:"+event.RequestID).Result()
		require.NoError(t, err)
		require.Equal(t, "committed", request["status"], "已在途轮次必须提交自己的原预留")
		lease, found, err := quota.LoadLease(context.Background(), 33, "agent-ws-node")
		require.NoError(t, err)
		require.True(t, found)
		require.Zero(t, lease.ReservedCents)
		require.Greater(t, lease.SpentCents, int64(0))
		return event, lease.SpentCents
	}
	first, firstSpent := collect(1)
	if state == "ordinary_cache" || state == "unknown_use_cache" {
		fake.change("grant_revoked")
	} else if state != "revoke_inflight" {
		fake.change(state)
	}
	write()
	body, err = read()
	allowed := state == "two_allowed_turns" || state == "ordinary_cache" || state == "unknown_use_cache"
	if allowed {
		require.NoError(t, err)
		require.Equal(t, "response.completed", gjson.GetBytes(body, "type").String())
		second, secondSpent := collect(2)
		require.NotEqual(t, first.RequestID, second.RequestID)
		require.Greater(t, secondSpent, firstSpent)
		require.EqualValues(t, 2, creates.Load())
	} else {
		require.Error(t, err)
		status := coderws.StatusPolicyViolation
		if state == "unavailable" {
			status = coderws.StatusTryAgainLater
		}
		require.Equal(t, status, coderws.CloseStatus(err), "拒绝必须来自真实核验，不能是测试读超时")
		require.EqualValues(t, 1, creates.Load(), "撤销/缩权之后不能派发下一轮")
		lease, found, loadErr := quota.LoadLease(context.Background(), 33, "agent-ws-node")
		require.NoError(t, loadErr)
		require.True(t, found)
		require.Zero(t, lease.ReservedCents, "拒绝下一轮不能创建新预留")
		require.Equal(t, firstSpent, lease.SpentCents, "撤销不能退款或重记已准入轮次")
		require.EqualValues(t, 1, redisClient.XLen(context.Background(), prefix+":usage:stream").Val())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if state == "ordinary_cache" || state == "unknown_use_cache" {
		require.Len(t, fake.resolves, 1, "普通/未知用途仍复用原缓存")
	} else {
		require.Len(t, fake.resolves, 3, "握手及每个新轮次都必须真实主站解析")
		for _, req := range fake.resolves {
			require.False(t, req.BillingExempt)
			require.False(t, req.SkipUsageTouch)
		}
		require.Zero(t, redisClient.Exists(context.Background(), prefix+":key:agent-ws-node:agentws").Val())
		_, found := keyCache.PeekHotImmutable("agentws", "agent-ws-node")
		require.False(t, found)
	}
}
