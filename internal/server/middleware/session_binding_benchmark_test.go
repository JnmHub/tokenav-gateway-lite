//go:build unit

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSessionBindingContextCanBeScopedAwayFromAPIKeyGatewayPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	management := router.Group("/api/v1")
	management.Use(SessionBindingContext(&config.Config{}))
	management.GET("/probe", func(c *gin.Context) {
		require.NotNil(t, service.SessionBindingFromContext(c.Request.Context()))
		c.Status(http.StatusNoContent)
	})
	router.GET("/v1/probe", func(c *gin.Context) {
		require.Nil(t, service.SessionBindingFromContext(c.Request.Context()))
		c.Status(http.StatusNoContent)
	})

	for _, path := range []string{"/api/v1/probe", "/v1/probe"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("User-Agent", "scope-test/1.0")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusNoContent, recorder.Code)
	}
}

func TestSessionBindingContextKeepsBrowserAPIBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	c.Request.RemoteAddr = "203.0.113.8:43120"
	c.Request.Header.Set("User-Agent", "browser/1.0")

	SessionBindingContext(&config.Config{})(c)

	binding := service.SessionBindingFromContext(c.Request.Context())
	require.NotNil(t, binding)
	require.Equal(t, "203.0.113.8", binding.IP)
	require.NotEmpty(t, binding.UAHash)
	require.Equal(t, binding.IP, c.GetString(ContextKeySecurityClientIP))
}

func BenchmarkSessionBindingContext(b *testing.B) {
	gin.SetMode(gin.TestMode)
	middleware := SessionBindingContext(&config.Config{})

	b.Run("Baseline", func(b *testing.B) {
		benchmarkSessionBindingRouter(b, "/v1/responses")
	})
	b.Run("APIKeyHotPath", func(b *testing.B) {
		// Production registers the middleware only on /api/v1, so customer routes
		// intentionally benchmark the router without SessionBindingContext.
		benchmarkSessionBindingRouter(b, "/v1/responses")
	})
	b.Run("BrowserAPI", func(b *testing.B) {
		benchmarkSessionBindingRouter(b, "/api/v1/auth/login", middleware)
	})
}

type benchmarkResponseWriter struct {
	header http.Header
}

func (w *benchmarkResponseWriter) Header() http.Header            { return w.header }
func (w *benchmarkResponseWriter) Write(body []byte) (int, error) { return len(body), nil }
func (w *benchmarkResponseWriter) WriteHeader(int)                {}

func benchmarkSessionBindingRouter(b *testing.B, path string, middleware ...gin.HandlerFunc) {
	router := gin.New()
	router.Use(middleware...)
	router.POST(path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.RemoteAddr = "203.0.113.8:43120"
	request.Header.Set("User-Agent", "benchmark-client/1.0")
	w := &benchmarkResponseWriter{header: make(http.Header)}
	router.ServeHTTP(w, request) // warm the gin context pool before measuring
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		router.ServeHTTP(w, request)
	}
}
