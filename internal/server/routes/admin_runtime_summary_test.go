package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBuildGatewayRuntimeSummarySelectsOnlySafeEffectiveValues(t *testing.T) {
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	previousStartedAt := gatewayLiteStartedAt
	gatewayLiteStartedAt = now.Add(-2 * time.Minute)
	t.Cleanup(func() { gatewayLiteStartedAt = previousStartedAt })

	cfg := &config.Config{}
	cfg.Database.Host = "db.internal"
	cfg.Database.User = "operator"
	cfg.Database.Password = "do-not-leak-database"
	cfg.Database.MaxOpenConns = 96
	cfg.Database.MaxIdleConns = 32
	cfg.Redis.Host = "cache.internal"
	cfg.Redis.Username = "cache-user"
	cfg.Redis.Password = "do-not-leak-cache"
	cfg.Redis.PoolSize = 256
	cfg.Redis.MinIdleConns = 24
	cfg.Billing.CacheWrite.Preset = config.BillingCacheWritePresetLow
	cfg.Billing.CacheWrite.CriticalWorkerCount = 8
	cfg.Billing.CacheWrite.CriticalBufferSize = 4096
	cfg.Billing.CacheWrite.BestEffortWorkerCount = 4
	cfg.Billing.CacheWrite.BestEffortBufferSize = 1024
	cfg.Billing.CacheWrite.TimeoutMilliseconds = 1500
	cfg.Gateway.UsageRecord.WorkerCount = 12
	cfg.Gateway.UsageRecord.QueueSize = 3000
	cfg.Gateway.UsageRecord.OverflowPolicy = "sync"
	cfg.GatewayLite.ControlPlaneURL = "http://admin:do-not-leak@example.invalid"
	cfg.GatewayLite.ControlPlaneToken = "do-not-leak-control"
	cfg.GatewayLite.AdminSyncKey = "do-not-leak-admin"
	cfg.GatewayLite.UsageReportWorkers = 6
	cfg.GatewayLite.UsageReportBatchSize = 80
	cfg.GatewayLite.CompositeGroupsEnabled = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.ForceHTTP = false
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 6
	cfg.Gateway.OpenAIScheduler.StickyEscapeEnabled = true
	cfg.Gateway.Scheduling.FallbackQueueEnabled = true
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 500
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 15 * time.Second

	metrics := map[string]any{
		"billing_cache_preset":                           config.BillingCacheWritePresetHigh,
		"billing_cache_timeout_milliseconds":             int64(900),
		"billing_cache_critical_worker_count":            9,
		"billing_cache_critical_active_worker_count":     8,
		"billing_cache_critical_queue_capacity":          5000,
		"billing_cache_critical_queue_length":            7,
		"billing_cache_critical_sync_fallback_total":     uint64(2),
		"billing_cache_best_effort_worker_count":         5,
		"billing_cache_best_effort_active_worker_count":  4,
		"billing_cache_best_effort_queue_capacity":       1200,
		"billing_cache_best_effort_queue_length":         3,
		"billing_cache_best_effort_dropped_full_total":   int64(4),
		"billing_cache_best_effort_dropped_closed_total": int64(1),
		"usage_record_max_concurrency":                   14,
		"usage_record_queue_capacity":                    3200,
		"usage_record_waiting_tasks":                     11,
		"usage_record_completed_tasks_total":             int64(900),
	}

	result := buildGatewayRuntimeSummary(cfg, metrics, true, true, now)
	require.True(t, result.ReadOnly)
	require.Equal(t, int64(120), result.Service.UptimeSeconds)
	require.Equal(t, 96, result.Database.MaxOpen)
	require.Equal(t, 256, result.Cache.PoolSize)
	require.Equal(t, int64(9), result.Billing.CriticalWorkers)
	require.Equal(t, int64(8), result.Billing.CriticalActiveWorkers)
	require.Equal(t, int64(4), result.Billing.BestEffortActiveWorkers)
	require.Equal(t, config.BillingCacheWritePresetHigh, result.Billing.Preset)
	require.Equal(t, 900, result.Billing.WriteTimeoutMS)
	require.Equal(t, int64(5), result.Billing.BestEffortDroppedTotal)
	require.Equal(t, int64(14), result.Usage.LocalWorkers)
	require.True(t, result.WebSocket.EffectiveEnabled)
	require.True(t, result.Scheduling.AdvancedEnabled)
	require.True(t, result.CompositeGroupsEnabled)
	assertGatewayRuntimeSummaryHasNoSecrets(t, result)
}

func TestGatewayRuntimeSummaryRouteRequiresAdministratorAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	auth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("X-Test-Administrator") != "allowed" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false})
			return
		}
		c.Next()
	})
	v1 := router.Group("/api/v1")
	admin := v1.Group("/admin")
	admin.Use(gatewayLiteAdminAuth(auth, &config.Config{}))
	registerGatewayLiteRuntimeSummaryRoute(admin, nil, &config.Config{}, nil)

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/runtime/summary", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/runtime/summary", nil)
	request.Header.Set("X-Test-Administrator", "allowed")
	router.ServeHTTP(authorized, request)
	require.Equal(t, http.StatusOK, authorized.Code, authorized.Body.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(authorized.Body.Bytes(), &body))
	assertGatewayRuntimeSummaryHasNoSecrets(t, body)
}

func TestGatewayRuntimeSummaryReadsCompositeRuntimeSetting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newSchedulerRuntimeRouteRepo()
	settingService := service.NewSettingService(repo, &config.Config{})
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	registerGatewayLiteRuntimeSummaryRoute(admin, nil, &config.Config{}, settingService)

	readEnabled := func() bool {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/runtime/summary", nil))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var body struct {
			Data struct {
				CompositeGroupsEnabled bool `json:"composite_groups_enabled"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		return body.Data.CompositeGroupsEnabled
	}

	require.False(t, readEnabled())
	require.NoError(t, settingService.SetCompositeGroupsSettings(t.Context(), &service.CompositeGroupsSettings{Enabled: true}))
	require.True(t, readEnabled())
	require.NoError(t, settingService.SetCompositeGroupsSettings(t.Context(), &service.CompositeGroupsSettings{Enabled: false}))
	require.False(t, readEnabled())
}

var gatewayRuntimeCredentialPattern = regexp.MustCompile(`(?i)(?:postgres(?:ql)?|mysql|redis)://|://[^/@\s]+:[^/@\s]+@|\b(?:bearer|basic)\s+[a-z0-9+/._=-]+`)

func assertGatewayRuntimeSummaryHasNoSecrets(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	forbiddenKeys := []string{
		"password", "passwd", "secret", "token", "api_key", "apikey",
		"cookie", "authorization", "credential", "private_key", "dsn",
		"database_url", "control_plane_url", "proxy_url",
	}
	var visit func(path string, current any)
	visit = func(path string, current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				lower := strings.ToLower(key)
				for _, forbidden := range forbiddenKeys {
					if strings.Contains(lower, forbidden) {
						t.Errorf("forbidden runtime response key at %s: %s", path, key)
					}
				}
				visit(path+"."+key, child)
			}
		case []any:
			for index, child := range typed {
				visit(fmt.Sprintf("%s[%d]", path, index), child)
			}
		case string:
			if gatewayRuntimeCredentialPattern.MatchString(typed) {
				t.Errorf("credential-like value at %s", path)
			}
		}
	}
	visit("response", decoded)
}
