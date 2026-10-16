package admin

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestUpdateBillingCacheRuntimeConfigPersistsPresetAndRejectsStaleRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server:\n  port: 8080\n"), 0600))
	t.Setenv("SUB2API_CONFIG_FILE", configPath)

	svc := newBillingCacheServiceForRuntimeTest(t, config.BillingCacheWritePresetLow)
	h := NewGatewayLiteConfigHandler(&config.Config{})
	h.SetBillingCacheService(svc)
	router := gin.New()
	router.PUT("/runtime/billing-cache", h.UpdateBillingCacheRuntimeConfig)
	router.GET("/runtime/billing-cache", h.GetBillingCacheRuntimeConfig)

	updated := httptest.NewRecorder()
	router.ServeHTTP(updated, billingCacheRuntimeRequest(`{"expected_revision":1,"preset":"high"}`))
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	require.Contains(t, updated.Body.String(), `"critical_queue_capacity":8192`)
	require.Contains(t, updated.Body.String(), `"best_effort_queue_capacity":2048`)
	require.Equal(t, config.BillingCacheWritePresetHigh, svc.CurrentCacheWriteConfig().Preset)
	require.EqualValues(t, 2, svc.RuntimeStats().Revision)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)
	root := map[string]any{}
	require.NoError(t, yaml.Unmarshal(data, &root))
	require.Contains(t, root, "server")
	billing := root["billing"].(map[string]any)
	cacheWrite := billing["cache_write"].(map[string]any)
	require.Equal(t, config.BillingCacheWritePresetHigh, cacheWrite["preset"])
	require.NotContains(t, cacheWrite, "critical_worker_count")

	stale := httptest.NewRecorder()
	router.ServeHTTP(stale, billingCacheRuntimeRequest(`{"expected_revision":1,"preset":"standard"}`))
	require.Equal(t, http.StatusConflict, stale.Code, stale.Body.String())
	require.Equal(t, config.BillingCacheWritePresetHigh, svc.CurrentCacheWriteConfig().Preset)
}

func TestUpdateBillingCacheRuntimeConfigRollsBackWhenPersistenceFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newBillingCacheServiceForRuntimeTest(t, config.BillingCacheWritePresetLow)
	h := NewGatewayLiteConfigHandler(&config.Config{})
	h.SetBillingCacheService(svc)
	h.billingCacheConfigWriter = func(string, config.BillingCacheWriteConfig) error {
		return errors.New("forced persistence failure")
	}
	router := gin.New()
	router.PUT("/runtime/billing-cache", h.UpdateBillingCacheRuntimeConfig)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, billingCacheRuntimeRequest(`{"expected_revision":1,"preset":"high"}`))
	require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
	require.Equal(t, config.BillingCacheWritePresetLow, svc.CurrentCacheWriteConfig().Preset)
	require.EqualValues(t, 3, svc.RuntimeStats().Revision)
	require.EqualValues(t, 2, svc.RuntimeStats().ReloadTotal)
}

func newBillingCacheServiceForRuntimeTest(t *testing.T, preset string) *service.BillingCacheService {
	t.Helper()
	cfg := &config.Config{}
	cfg.Billing.CacheWrite = config.BillingCacheWriteConfig{Preset: preset}
	svc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)
	return svc
}

func billingCacheRuntimeRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPut, "/runtime/billing-cache", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}
