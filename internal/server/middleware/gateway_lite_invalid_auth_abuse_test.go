package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteInvalidAuthLimiterRejectsBeforeLeaseAndUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	var resolveCount atomic.Int32
	var acquireCount atomic.Int32
	var usageCount atomic.Int32
	control := gatewayLiteControlStub{
		resolveResp:  &gatewaylite.ResolveKeyResponse{OK: false, Error: "not_found"},
		resolveCount: &resolveCount,
		acquireCount: &acquireCount,
		reportFn: func(gatewaylite.UsageEvent) {
			usageCount.Add(1)
		},
	}
	limiter := gatewaylite.NewInvalidAuthAbuseLimiter(client, "middleware-test", gatewaylite.InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 1, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: time.Second,
	})
	auth := NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter(
		control,
		gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1", RedisPrefix: "middleware-test"}),
		nil,
		gatewaylite.NewRedisKeyCache(client, "middleware-test"),
		control,
		nil,
		limiter,
	)
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(auth), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	rawKey := "tokenav_sk_unknown_secret"
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
		req.RemoteAddr = "203.0.113.80:4567"
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := request()
	require.Equal(t, http.StatusUnauthorized, first.Code)
	second := request()
	require.Equal(t, http.StatusTooManyRequests, second.Code)
	require.Equal(t, "60", second.Header().Get("Retry-After"))
	require.Contains(t, second.Body.String(), "INVALID_AUTH_RATE_LIMITED")
	require.EqualValues(t, 1, resolveCount.Load(), "429 必须在主站 Key snapshot 查询前拒绝")
	require.Zero(t, acquireCount.Load(), "401/429 不得申请 lease（额度租约）")
	require.Zero(t, usageCount.Load(), "401/429 不得产生用量或计费上报")
}

func TestGatewayLiteInvalidAuthLimiterDoesNotCountPolicyRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	var resolveCount atomic.Int32
	control := gatewayLiteControlStub{
		resolveResp:  &gatewaylite.ResolveKeyResponse{OK: false, Error: "fixed_group_required"},
		resolveCount: &resolveCount,
	}
	limiter := gatewaylite.NewInvalidAuthAbuseLimiter(client, "policy-test", gatewaylite.InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 1, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: time.Second,
	})
	auth := NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter(
		control,
		gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1", RedisPrefix: "policy-test"}),
		nil,
		gatewaylite.NewRedisKeyCache(client, "policy-test"),
		control,
		nil,
		limiter,
	)
	router := gin.New()
	router.POST("/v1/responses", gin.HandlerFunc(auth))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
		req.RemoteAddr = "203.0.113.81:4567"
		req.Header.Set("Authorization", "Bearer tokenav_sk_valid_but_unbound")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code)
	}
	require.EqualValues(t, 2, resolveCount.Load(), "有效 Key 的分组策略错误不是非法鉴权")
}

func TestGatewayLiteInvalidAuthLimiterHotPositiveKeySkipsRedisCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	const (
		prefix = "hot-positive-test"
		region = "sg-1"
		keyID  = "cached-key"
		secret = "cached-secret"
	)
	hash := sha256.Sum256([]byte(secret))
	keyCache := gatewaylite.NewRedisKeyCache(client, prefix)
	require.NoError(t, keyCache.Set(context.Background(), gatewaylite.KeySnapshot{
		KeyID:                   keyID,
		UserID:                  42,
		SecretHash:              hex.EncodeToString(hash[:]),
		Status:                  "active",
		Tier:                    1,
		Platform:                "openai",
		GroupID:                 7,
		GroupName:               "openai",
		AllowedModels:           []string{"*"},
		GatewayStrategy:         "fixed_group",
		KeyDefaultGatewayNodeID: 5,
		GatewayAccessEnforced:   true,
		AvailableGateways: []gatewaylite.GatewayRouteSummary{
			{ID: 5, Code: region},
		},
		Group: &gatewaylite.GatewayGroupSnapshot{
			ID: 7, LocalGroupID: 7, Name: "openai", Platform: "openai",
			Status: "active", RateMultiplier: 1,
		},
	}, region))

	limiter := gatewaylite.NewInvalidAuthAbuseLimiter(client, prefix, gatewaylite.InvalidAuthAbuseConfig{
		Enabled: true, Threshold: 8, Window: time.Minute, Block: time.Minute, Capacity: 256, RedisTimeout: time.Second,
	})
	// 两次失败已达到 Redis 合并检查的合理怀疑阈值；修复前，同 NAT 的
	// 合法 Key 即使命中本地正缓存也会执行一次 MGET。
	limiter.RecordFailure(context.Background(), "203.0.113.82", "tokenav_sk_bad_1")
	limiter.RecordFailure(context.Background(), "203.0.113.82", "tokenav_sk_bad_2")

	var resolveCount atomic.Int32
	control := gatewayLiteControlStub{resolveCount: &resolveCount}
	auth := NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigLocalGroupsAndInvalidAuthLimiter(
		control,
		gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: region, RedisPrefix: prefix}),
		nil, keyCache, control, nil, limiter,
	)
	router := gin.New()
	router.GET("/v1/models", gin.HandlerFunc(auth), func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "203.0.113.82:4567"
	req.Header.Set("Authorization", "Bearer tokenav_sk_"+keyID+"_"+secret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	require.Zero(t, resolveCount.Load(), "本地正缓存命中不得回源主站")
	metrics := limiter.RuntimeMetrics(context.Background())
	require.Zero(t, metrics["invalid_auth_abuse_redis_checks_total"].(uint64),
		"本地正缓存已确认合法的 Key 不得等待 invalid-auth Redis")
}
