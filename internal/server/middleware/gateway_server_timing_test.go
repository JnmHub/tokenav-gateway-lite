package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteServerTimingSamplingScopeAndFinalTrailer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := servertiming.NewController(10_000)
	router := gin.New()
	router.Use(GatewayLiteServerTiming(controller))
	router.Any("/*path", func(c *gin.Context) {
		servertiming.Add(c.Request.Context(), servertiming.StageAuth, 2*time.Millisecond)
		servertiming.MarkUpstreamStart(c.Request.Context())
		time.Sleep(time.Millisecond)
		_, _ = c.Writer.WriteString("ok")
		servertiming.Add(c.Request.Context(), servertiming.StageSettle, 3*time.Millisecond)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusOK, response.Code)
	header := response.Header().Get(servertiming.HeaderName)
	require.Contains(t, header, "auth;dur=2.0")
	require.Contains(t, header, "firstbyte;dur=")
	result := response.Result()
	require.Contains(t, result.Trailer.Get(servertiming.FinalHeaderName), "settle;dur=3.0")
	for _, forbidden := range []string{"redis", "postgres", "account", "lease", "host"} {
		require.NotContains(t, strings.ToLower(header), forbidden)
	}

	admin := httptest.NewRecorder()
	router.ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "/api/v1/admin/users", nil))
	require.Empty(t, admin.Header().Get(servertiming.HeaderName))
}

func TestGatewayLiteServerTimingDisabledDoesNotAttachCollector(t *testing.T) {
	controller := servertiming.NewController(0)
	router := gin.New()
	router.Use(GatewayLiteServerTiming(controller))
	active := true
	router.POST("/v1/responses", func(c *gin.Context) {
		active = servertiming.Active(c.Request.Context())
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.False(t, active)
	require.Empty(t, response.Header().Get(servertiming.HeaderName))
}
