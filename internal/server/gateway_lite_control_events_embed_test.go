//go:build embed

package server

// 本文件测试依赖 embed（嵌入）前端资源：web.NewFrontendServer 仅在
// -tags embed 构建下返回真实前端服务，与 internal/web 包 embed_test.go
// 的构建约束组织方式保持一致。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 主站控制面（control-plane）事件推送路由必须绕过嵌入前端的 SPA 兜底：
// 未带 Token 时应返回 401 JSON 而不是前端 index.html。
func TestGatewayLiteControlEventRouteBypassesEmbeddedFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frontendServer, err := web.NewFrontendServer(nil, &config.Config{})
	require.NoError(t, err)

	r := gin.New()
	r.Use(frontendServer.Middleware())
	r.POST("/internal/control-plane/events", gatewayLiteControlEventHandler(&config.Config{
		GatewayLite: config.GatewayLiteConfig{ControlPlaneToken: "shared-token"},
	}))

	body, err := json.Marshal(gatewaylite.ControlPlaneEventPushRequest{
		DeliveryID: 1,
		Event:      gatewaylite.CacheInvalidationEvent{ID: 0, Scope: "key:snapshot"},
	})
	require.NoError(t, err)

	unauthorizedReq := httptest.NewRequest(http.MethodPost, "/internal/control-plane/events", bytes.NewReader(body))
	unauthorizedReq.Header.Set("Content-Type", "application/json")
	unauthorizedRec := httptest.NewRecorder()
	r.ServeHTTP(unauthorizedRec, unauthorizedReq)
	require.Equal(t, http.StatusUnauthorized, unauthorizedRec.Code)
	require.Contains(t, unauthorizedRec.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, unauthorizedRec.Body.String(), "<!doctype html>")

	invalidReq := httptest.NewRequest(http.MethodPost, "/internal/control-plane/events", bytes.NewReader(body))
	invalidReq.Header.Set("Content-Type", "application/json")
	invalidReq.Header.Set("Authorization", "Bearer shared-token")
	invalidRec := httptest.NewRecorder()
	r.ServeHTTP(invalidRec, invalidReq)
	require.Equal(t, http.StatusBadRequest, invalidRec.Code)
	require.Contains(t, invalidRec.Header().Get("Content-Type"), "application/json")
	require.NotContains(t, invalidRec.Body.String(), "<!doctype html>")
}
