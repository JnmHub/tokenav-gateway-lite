package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCommonRoutesReadinessReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterCommonRoutes(router,
		ReadinessProbe{Name: "database", Check: func(context.Context) error { return nil }},
		ReadinessProbe{Name: "redis", Check: func(context.Context) error { return nil }},
	)

	response := performCommonRequest(router, "/ready")
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, map[string]any{
		"status": "ready",
		"checks": map[string]any{"database": "ok", "redis": "ok"},
	}, decodeCommonResponse(t, response))
}

func TestCommonRoutesReadinessUnavailableDoesNotLeakError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterCommonRoutes(router,
		ReadinessProbe{Name: "database", Check: func(context.Context) error {
			return errors.New("postgres://admin:secret@example.invalid/database")
		}},
		ReadinessProbe{Name: "redis", Check: func(context.Context) error { return nil }},
	)

	response := performCommonRequest(router, "/ready")
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, map[string]any{
		"status": "not_ready",
		"checks": map[string]any{"database": "unavailable", "redis": "ok"},
	}, decodeCommonResponse(t, response))
	require.NotContains(t, response.Body.String(), "secret")
}

func TestCommonRoutesHealthRemainsLiveWhenDependencyFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterCommonRoutes(router, ReadinessProbe{
		Name:  "database",
		Check: func(context.Context) error { return errors.New("down") },
	})

	response := performCommonRequest(router, "/health")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{"status":"ok"}`, response.Body.String())
}

func TestRunReadinessProbesUsesStableUniqueNames(t *testing.T) {
	checks, ready := runReadinessProbes(context.Background(), []ReadinessProbe{
		{Name: "redis", Check: func(context.Context) error { return nil }},
		{Name: "redis_3", Check: func(context.Context) error { return nil }},
		{Name: "redis", Check: func(context.Context) error { return nil }},
		{Name: "", Check: func(context.Context) error { return nil }},
	})

	require.True(t, ready)
	require.Equal(t, map[string]string{
		"redis":      "ok",
		"redis_3":    "ok",
		"redis_4":    "ok",
		"dependency": "ok",
	}, checks)
}

func performCommonRequest(router http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	router.ServeHTTP(response, request)
	return response
}

func decodeCommonResponse(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &payload))
	return payload
}
