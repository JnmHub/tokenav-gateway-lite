package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteOverloadLimitsFullRequestLifetimeAndBypassesHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := NewGatewayLiteOverloadLimiter(true, 1, 1, time.Second)
	router := gin.New()
	router.Use(limiter.Handler())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	router.POST("/v1/responses", func(c *gin.Context) {
		started <- struct{}{}
		<-release
		c.Status(http.StatusOK)
	})
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		firstDone <- recorder
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not acquire overload slot")
	}

	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
		secondDone <- recorder
	}()
	require.Eventually(t, func() bool {
		return limiter.RuntimeMetrics(context.Background())["overload_waiting"] == int64(1)
	}, time.Second, 10*time.Millisecond)

	rejected := httptest.NewRecorder()
	router.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusTooManyRequests, rejected.Code)
	require.Equal(t, "1", rejected.Header().Get("Retry-After"))
	require.Contains(t, rejected.Body.String(), "gateway_overloaded")

	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, health.Code)

	close(release)
	require.Equal(t, http.StatusOK, (<-firstDone).Code)
	second := <-secondDone
	require.Equal(t, http.StatusOK, second.Code)
	require.NotEmpty(t, second.Header().Get("X-Gateway-Overload-Wait-Ms"))
	metrics := limiter.RuntimeMetrics(context.Background())
	require.EqualValues(t, 0, metrics["overload_inflight"])
	require.EqualValues(t, 0, metrics["overload_waiting"])
	require.EqualValues(t, 2, metrics["overload_accepted_total"])
	require.EqualValues(t, 1, metrics["overload_rejected_total"])
	require.EqualValues(t, 1, metrics["overload_waited_total"])
	require.GreaterOrEqual(t, metrics["overload_wait_ms_total"].(uint64), uint64(0))
	require.Equal(t, metrics["overload_wait_ms_total"], metrics["overload_wait_ms_max"])
}

func TestGatewayLiteOverloadPathScope(t *testing.T) {
	for _, requestPath := range []string{
		"/v1/responses",
		"/v1beta/models/gemini:generateContent",
		"/backend-api/codex/responses",
		"/responses",
		"/models",
		"/antigravity/v1beta/models",
	} {
		require.True(t, isGatewayLiteModelRequest(requestPath), requestPath)
	}
	for _, requestPath := range []string{
		"/health",
		"/ready",
		"/api/v1/admin/groups",
		"/gateway-admin",
		"/assets/app.js",
	} {
		require.False(t, isGatewayLiteModelRequest(requestPath), requestPath)
	}
}
