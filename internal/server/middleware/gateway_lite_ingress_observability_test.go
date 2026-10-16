package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteIngressObservabilityOnlyCountsObservedEntrypoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	aggregator := ingress.NewAggregator()

	router := gin.New()
	router.Use(gatewayLiteIngressObservability(GatewayLiteApplicationHeaderLimitBytes, aggregator))
	router.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, path := range []string{"/v1/models", "/health"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, recorder.Code)
	}
	require.Equal(t, uint64(1), aggregator.Health().RequestsTotal)
}

func TestGatewayLiteIngressObservabilitySeparatesRoute404FromUpstream404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	aggregator := ingress.NewAggregator()

	router := gin.New()
	router.Use(gatewayLiteIngressObservability(GatewayLiteApplicationHeaderLimitBytes, aggregator))
	router.POST("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusNotFound) })

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	middle := aggregator.Health()
	require.Zero(t, middle.RejectionsTotal)

	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/not-a-real-route", nil))
	after := aggregator.Health()
	require.Equal(t, middle.RejectionsTotal+1, after.RejectionsTotal)
	require.Equal(t, ingress.RejectInvalidPath, aggregator.Snapshot(5).TopReasons[0].Reason)
}

func TestGatewayLiteIngressObservabilityRejectsOversizedHeaderAs431(t *testing.T) {
	gin.SetMode(gin.TestMode)
	aggregator := ingress.NewAggregator()
	handlerCalled := false

	router := gin.New()
	router.Use(gatewayLiteIngressObservability(64, aggregator))
	router.GET("/v1/models", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Large", strings.Repeat("x", 80))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusRequestHeaderFieldsTooLarge, recorder.Code)
	require.Contains(t, recorder.Body.String(), "request_header_too_large")
	require.False(t, handlerCalled)
	snapshot := aggregator.Snapshot(5)
	require.Equal(t, uint64(1), snapshot.RequestCount)
	require.Equal(t, uint64(1), snapshot.RejectionCount)
	require.Equal(t, ingress.RejectHeaderTooLarge, snapshot.TopReasons[0].Reason)
}

func TestGatewayLiteIngressObservabilityAllowsHeaderAtApplicationBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	aggregator := ingress.NewAggregator()

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-Test", "ok")
	limit := int64(2 + len("Host") + 2 + len(req.Host) + 2 + len("X-Test") + 2 + len("ok") + 2)

	router := gin.New()
	router.Use(gatewayLiteIngressObservability(limit, aggregator))
	router.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Zero(t, aggregator.Health().RejectionsTotal)
}

func TestGatewayLiteIngressObservabilityNetworkHeaderBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("application limit returns project JSON and aggregates once", func(t *testing.T) {
		aggregator := ingress.NewAggregator()
		router := gin.New()
		router.Use(gatewayLiteIngressObservability(GatewayLiteApplicationHeaderLimitBytes, aggregator))
		router.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		server := httptest.NewServer(router)
		defer server.Close()

		req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
		require.NoError(t, err)
		req.Header.Set("X-Large", strings.Repeat("x", 80<<10))
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusRequestHeaderFieldsTooLarge, resp.StatusCode)
		require.Equal(t, uint64(1), aggregator.Health().RejectionsTotal)
	})

	t.Run("net http transport ceiling rejects before application aggregation", func(t *testing.T) {
		aggregator := ingress.NewAggregator()
		router := gin.New()
		router.Use(gatewayLiteIngressObservability(GatewayLiteApplicationHeaderLimitBytes, aggregator))
		router.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusNoContent) })
		server := httptest.NewServer(router)
		defer server.Close()

		req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
		require.NoError(t, err)
		req.Header.Set("X-Large", strings.Repeat("x", 2<<20))
		resp, err := server.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusRequestHeaderFieldsTooLarge, resp.StatusCode)
		require.Zero(t, aggregator.Health().RejectionsTotal,
			"net/http 在 Gin 前拒绝的请求无法进入应用层匿名聚合")
	})
}
