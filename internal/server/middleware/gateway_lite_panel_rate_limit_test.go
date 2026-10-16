package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLitePanelRateLimiterSeparatesHeavyAndGlobalLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := NewGatewayLitePanelRateLimiter(4, 2, time.Minute)
	now := time.Unix(1000, 0)
	limiter.now = func() time.Time { return now }
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 7})
		c.Next()
	})
	router.Use(limiter.Handler())
	router.GET("/api/v1/admin/dashboard/stats", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/api/v1/admin/groups", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(recorder, req)
		return recorder
	}
	require.Equal(t, http.StatusNoContent, request("/api/v1/admin/dashboard/stats").Code)
	require.Equal(t, http.StatusNoContent, request("/api/v1/admin/dashboard/stats").Code)
	limited := request("/api/v1/admin/dashboard/stats")
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	require.Equal(t, "60", limited.Header().Get("Retry-After"))
	// Heavy rejection does not consume another global slot.
	require.Equal(t, http.StatusNoContent, request("/api/v1/admin/groups").Code)
	require.Equal(t, http.StatusNoContent, request("/api/v1/admin/groups").Code)
	require.Equal(t, http.StatusTooManyRequests, request("/api/v1/admin/groups").Code)

	now = now.Add(time.Minute + time.Second)
	require.Equal(t, http.StatusNoContent, request("/api/v1/admin/dashboard/stats").Code)
}

func TestGatewayLitePanelHeavyPathDoesNotMatchCustomerV1(t *testing.T) {
	require.False(t, gatewayLitePanelHeavyPath("/v1/responses"))
	require.False(t, gatewayLitePanelHeavyPath("/v1/chat/completions"))
	require.True(t, gatewayLitePanelHeavyPath("/api/v1/admin/usage"))
	require.True(t, gatewayLitePanelHeavyPath("/api/v1/admin/ops/dashboard/overview"))
}

func TestGatewayLitePanelRateLimitRecoveryEndpointsStayReachable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := NewGatewayLitePanelRateLimiter(1, 1, time.Minute)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 7})
		c.Next()
	})
	router.Use(limiter.Handler())
	router.GET("/api/v1/admin/groups", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.PUT("/api/v1/admin/ops/runtime/panel-rate-limit", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.POST("/api/v1/admin/ops/runtime/panel-rate-limit/reset", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	request := func(method, path string) int {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		return recorder.Code
	}
	require.Equal(t, http.StatusNoContent, request(http.MethodGet, "/api/v1/admin/groups"))
	require.Equal(t, http.StatusTooManyRequests, request(http.MethodGet, "/api/v1/admin/groups"))
	require.Equal(t, http.StatusNoContent, request(http.MethodPut, "/api/v1/admin/ops/runtime/panel-rate-limit"))
	require.Equal(t, http.StatusNoContent, request(http.MethodPost, "/api/v1/admin/ops/runtime/panel-rate-limit/reset"))
	require.Equal(t, uint64(2), limiter.Stats().RecoveryBypass)
}
