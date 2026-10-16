package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A previously cached ordinary snapshot must never become proof for a delegated
// token with the same backing Key ID. Exercise the real middleware, HTTP client,
// Redis hot cache and credential branches together, without an upstream call.
func TestGatewayBindingRuntimeOrdinaryCacheDoesNotAuthorizeInvocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := gatewaylite.NewRedisKeyCache(rdb, "binding-credential-isolation")
	code, keyID, secret := "binding-cache-node", "scope-cache-key", "ordinary-fixture-secret"
	hash := sha256.Sum256([]byte(secret))
	ordinary := gatewaylite.KeySnapshot{
		KeyID: keyID, UserID: 42, SecretHash: hex.EncodeToString(hash[:]), Status: "active", Platform: service.PlatformOpenAI,
		GroupID: 9, KeyDefaultGatewayNodeID: 7, GatewayStrategy: "fixed_group", GatewayAccessEnforced: true,
		AvailableGateways: []gatewaylite.GatewayRouteSummary{{ID: 7, Code: code, Region: "hk"}},
		Group:             &gatewaylite.GatewayGroupSnapshot{ID: 9, LocalGroupID: 1, GatewayNodeID: 7, GatewayCode: code, Platform: service.PlatformOpenAI, Status: "active", RateMultiplier: 1},
		AllowedModels:     []string{"*"}, CacheTTLSecond: 300,
	}
	require.NoError(t, cache.Set(ctx, ordinary, code))
	delegated := ordinary
	delegated.SecretHash = ""
	delegated.AllowedModels = []string{"gpt-5.4-mini"}
	delegated.CacheTTLSecond = 0
	projection := gatewaylite.OAuthInvocationResolveResponse{
		OK: true, DelegationID: "binding-cache-delegation", Key: &delegated, ExpiresAtMillis: time.Now().Add(time.Minute).UnixMilli(),
		Invocation: &gatewaylite.OAuthInvocation{GatewayNodeID: 7, GroupID: 9, Models: []string{"gpt-5.4-mini"}, MaxCostCents: 10, ExpiresIn: 60, RateLimitRPM: 60, Concurrency: 1, CostConsent: true},
	}
	invocationToken := "tokenav_oa_" + strings.Repeat("t", 43)
	nodeToken := "gb_fixture-cache-control"
	var ordinaryResolve, invocationResolve, unexpected atomic.Int32
	var revoked atomic.Bool
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+nodeToken {
			unexpected.Add(1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/internal/key/resolve":
			ordinaryResolve.Add(1)
			_ = json.NewEncoder(w).Encode(gatewaylite.ResolveKeyResponse{OK: true, Key: ordinary})
		case "/internal/oauth/invocations/resolve":
			invocationResolve.Add(1)
			var in gatewaylite.OAuthInvocationResolveRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.InvocationToken != invocationToken || in.GatewayCode != code || revoked.Load() {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_token"})
				return
			}
			_ = json.NewEncoder(w).Encode(projection)
		default:
			unexpected.Add(1)
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	t.Cleanup(control.Close)
	client, err := gatewaylite.NewControlPlaneClient(control.URL, nodeToken, 2*time.Second)
	require.NoError(t, err)
	router := gin.New()
	router.Use(gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddleware(client, "hk", code, nil, cache, nil)))
	handler := func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		c.JSON(http.StatusOK, gin.H{"models": apiKey.AllowedModels, "delegated": gatewaylite.HasOAuthInvocationCredential(c.Request.Context())})
	}
	router.GET("/v1/models", handler)
	router.POST("/v1/responses", handler)
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	ordinaryToken := "tokenav_sk_" + keyID + "_" + secret
	allowedBody := `{"model":"gpt-5.4-mini","input":"synthetic fixture"}`
	for i := 0; i < 2; i++ {
		rec := call(http.MethodGet, "/v1/models", ordinaryToken, "")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"delegated":false`)
	}
	require.Zero(t, ordinaryResolve.Load(), "ordinary positive cache remains usable")
	rec := call(http.MethodPost, "/v1/responses", invocationToken, allowedBody)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"delegated":true`)
	require.Contains(t, rec.Body.String(), `"models":["gpt-5.4-mini"]`)
	rec = call(http.MethodPost, "/v1/responses", invocationToken, `{"model":"unconsented-model"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	revoked.Store(true)
	rec = call(http.MethodPost, "/v1/responses", invocationToken, allowedBody)
	require.Equal(t, http.StatusForbidden, rec.Code, "a still-active ordinary snapshot cannot revive revoked consent")
	require.EqualValues(t, 3, invocationResolve.Load(), "each delegated request must recheck its actual credential")
	for _, token := range []string{nodeToken, "ge_fixture-enrolment", "tokenav_sk_" + keyID + "_" + ordinary.SecretHash} {
		require.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "/v1/models", token, "").Code)
	}
	cached, found := cache.PeekHotImmutable(keyID, code)
	require.True(t, found)
	require.Equal(t, []string{"*"}, cached.AllowedModels, "delegate must not overwrite the ordinary cache")
	require.NoError(t, cache.DeleteKey(ctx, keyID, code))
	require.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/models", ordinaryToken, "").Code)
	require.EqualValues(t, 1, ordinaryResolve.Load(), "ordinary cache miss keeps its original main-site resolution")
	require.Zero(t, unexpected.Load(), "authentication-only fixture must not allocate quota, funding or usage")
}
