package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingHandlerRevealAdminAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const key = "admin-0123456789abcdef"

	t.Run("JWT admin session can reveal without caching", func(t *testing.T) {
		repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyAdminAPIKey: key}}
		svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
		handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/admin-api-key/reveal", nil)
		ctx.Set("auth_method", service.AuditAuthMethodJWT)

		handler.RevealAdminAPIKey(ctx)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		require.Equal(t, "no-cache", rec.Header().Get("Pragma"))
		var body response.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		data, ok := body.Data.(map[string]any)
		require.True(t, ok)
		require.Equal(t, key, data["key"])
	})

	t.Run("machine credential cannot reveal the stored value", func(t *testing.T) {
		repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyAdminAPIKey: key}}
		svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
		handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/admin-api-key/reveal", nil)
		ctx.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

		handler.RevealAdminAPIKey(ctx)

		require.Equal(t, http.StatusForbidden, rec.Code)
		require.False(t, strings.Contains(rec.Body.String(), key))
	})

	t.Run("missing key returns not found", func(t *testing.T) {
		repo := &settingHandlerRepoStub{values: map[string]string{}}
		svc := service.NewSettingService(repo, &config.Config{Default: config.DefaultConfig{UserConcurrency: 5}})
		handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/admin-api-key/reveal", nil)
		ctx.Set("auth_method", service.AuditAuthMethodJWT)

		handler.RevealAdminAPIKey(ctx)

		require.Equal(t, http.StatusNotFound, rec.Code)
	})
}
