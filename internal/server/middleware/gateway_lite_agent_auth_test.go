package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 保留真实 HTTP 主站客户端；仅省略本组用例不使用的 Funding/OAuth 能力，
// 以便验证普通额度租约链，不会把 Agent 测试伪装成委托调用测试。
type agentHTTPControl struct {
	client *gatewaylite.ControlPlaneClient
}

func (c agentHTTPControl) ResolveKey(ctx context.Context, req gatewaylite.ResolveKeyRequest) (*gatewaylite.ResolveKeyResponse, error) {
	return c.client.ResolveKey(ctx, req)
}
func (c agentHTTPControl) AcquireLease(ctx context.Context, req gatewaylite.AcquireLeaseRequest) (*gatewaylite.AcquireLeaseResponse, error) {
	return c.client.AcquireLease(ctx, req)
}
func (c agentHTTPControl) RebalanceLease(ctx context.Context, req gatewaylite.RebalanceLeaseRequest) (*gatewaylite.RebalanceLeaseResponse, error) {
	return c.client.RebalanceLease(ctx, req)
}

func TestGatewayLiteAgentHTTPRevocationIsLiveWithoutInvalidation(t *testing.T) {
	for _, revoked := range []string{"app", "grant", "user", "key"} {
		t.Run(revoked, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			var state atomic.Bool
			var resolves, leases, downstream atomic.Int32
			digest := sha256.Sum256([]byte("synthetic"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/internal/key/resolve":
					var in gatewaylite.ResolveKeyRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.KeyID != "agenthttp" || in.GatewayCode != "agent-node" || in.BillingExempt {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					resolves.Add(1)
					key := gatewayLiteBoundTestSnapshot(gatewaylite.KeySnapshot{
						KeyID: in.KeyID, UserID: 42, SecretHash: hex.EncodeToString(digest[:]), Status: "active",
						TokenUse: "agent_api_key", CacheTTLSecond: 0, AllowedModels: []string{"gpt-test"},
						Platform: service.PlatformOpenAI,
					}, in)
					if state.Load() {
						// 主站已逐次核 app/grant/user/Key；失效后无需线路授权也要
						// 返回用途与 inactive，网关必须先拒绝，不能误报线路错误。
						key.Status, key.Group = revoked+"_inactive", nil
						key.GatewayAccessEnforced = false
						key.AvailableGateways = nil
					}
					_ = json.NewEncoder(w).Encode(gatewaylite.ResolveKeyResponse{OK: true, Key: key})
				case "/internal/quota/acquire-lease":
					var in gatewaylite.AcquireLeaseRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					leases.Add(1)
					_ = json.NewEncoder(w).Encode(gatewaylite.AcquireLeaseResponse{OK: true, Lease: gatewaylite.LeaseSnapshot{
						LeaseID: "agent-http-lease", UserID: in.UserID, Region: in.Region,
						AllocatedCents: 100000, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
					}})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			control, err := gatewaylite.NewControlPlaneClient(server.URL, "synthetic-control", time.Second)
			require.NoError(t, err)
			mr := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer client.Close()
			quota := gatewaylite.NewRedisQuota(client, "agent-http")
			cache := gatewaylite.NewRedisKeyCache(client, "agent-http")
			reported := make(chan gatewaylite.UsageEvent, 4)
			reporter := gatewayLiteUsageSinkFunc(func(_ context.Context, event gatewaylite.UsageEvent) error { reported <- event; return nil })
			router := gin.New()
			router.Use(gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(agentHTTPControl{control}, "synthetic", "agent-node", quota, cache, reporter)))
			router.POST("/v1/responses", func(c *gin.Context) {
				downstream.Add(1)
				meta, ok := gatewaylite.RequestMetaFromContext(c.Request.Context())
				if !ok || meta.KeyID != "agenthttp" || meta.UserID != 42 {
					c.Status(http.StatusInternalServerError)
					return
				}
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})
			request := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"synthetic"}`))
				r.Header.Set("Authorization", "Bearer tokenav_sk_agenthttp_synthetic")
				r.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, r)
				return rec
			}
			require.Equal(t, http.StatusOK, request().Code)
			state.Store(true)
			denied := request()
			require.Equal(t, http.StatusUnauthorized, denied.Code)
			require.Contains(t, denied.Body.String(), "INVALID_API_KEY")
			require.NotContains(t, denied.Body.String(), "FIXED_GROUP_REQUIRED")
			require.EqualValues(t, 2, resolves.Load(), "撤销必须直接回源，不能等待失效通知或 TTL")
			require.EqualValues(t, 1, downstream.Load())
			require.EqualValues(t, 1, leases.Load())
			_, found := cache.PeekHotImmutable("agenthttp", "agent-node")
			require.False(t, found)
			require.Zero(t, client.Exists(context.Background(), "agent-http:key:agent-node:agenthttp").Val())
			select {
			case event := <-reported:
				require.Equal(t, "agenthttp", event.KeyID)
				require.EqualValues(t, 42, event.UserID)
				require.Equal(t, "agent-http-lease", event.LeaseID)
				require.Equal(t, http.StatusOK, event.Status)
			case <-time.After(time.Second):
				t.Fatal("已准入 HTTP 请求没有按原身份结算")
			}
		})
	}
}

func TestGatewayLiteAgentConcurrentHTTPResolveDoesNotShareSnapshot(t *testing.T) {
	const requests = 12
	var resolves atomic.Int32
	firstStarted, firstReply := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := resolves.Add(1)
		if call == 1 {
			close(firstStarted)
			<-firstReply
		}
		status := "active"
		if call > 1 {
			status = "grant_revoked"
		}
		_ = json.NewEncoder(w).Encode(gatewaylite.ResolveKeyResponse{OK: true, Key: gatewaylite.KeySnapshot{
			KeyID: "concurrent-agent", UserID: 42, TokenUse: "agent_api_key", Status: status,
		}})
	}))
	defer server.Close()
	control, err := gatewaylite.NewControlPlaneClient(server.URL, "synthetic-control", time.Second)
	require.NoError(t, err)
	var ready, done sync.WaitGroup
	ready.Add(requests)
	done.Add(requests)
	start := make(chan struct{})
	results := make(chan *gatewaylite.KeySnapshot, requests)
	errors := make(chan error, requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			key, err := gatewayLiteResolveKey(context.Background(), agentHTTPControl{control}, nil, "concurrent-agent", "synthetic", "agent-node", false, false)
			results <- key
			errors <- err
		}()
	}
	ready.Wait()
	close(start)
	<-firstStarted
	// 保持首响应在途，让本波请求进入并发合并；每个 Agent 等待者仍须真实解析。
	time.Sleep(30 * time.Millisecond)
	close(firstReply)
	done.Wait()
	close(results)
	close(errors)
	active := 0
	for err := range errors {
		require.NoError(t, err)
	}
	for key := range results {
		require.NotNil(t, key)
		if key.Active() {
			active++
		}
	}
	require.EqualValues(t, requests, resolves.Load())
	require.Equal(t, 1, active, "撤销前的在途快照不能授权其它并发请求")
}
