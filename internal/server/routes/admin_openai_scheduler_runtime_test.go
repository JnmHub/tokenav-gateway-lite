package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type schedulerRuntimeRouteRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func newSchedulerRuntimeRouteRepo() *schedulerRuntimeRouteRepo {
	return &schedulerRuntimeRouteRepo{values: make(map[string]string)}
}

func (r *schedulerRuntimeRouteRepo) Get(_ context.Context, key string) (*service.Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Key: key, Value: value}, nil
}

func (r *schedulerRuntimeRouteRepo) GetValue(ctx context.Context, key string) (string, error) {
	value, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return value.Value, nil
}

func (r *schedulerRuntimeRouteRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *schedulerRuntimeRouteRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *schedulerRuntimeRouteRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *schedulerRuntimeRouteRepo) GetAll(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *schedulerRuntimeRouteRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

func (r *schedulerRuntimeRouteRepo) CompareAndSwapMultiple(_ context.Context, revisionKey, expectedRevision, nextRevision string, values map[string]string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.values[revisionKey]
	if current == "" {
		current = "0"
	}
	if current != expectedRevision {
		return false, nil
	}
	for key, value := range values {
		r.values[key] = value
	}
	r.values[revisionKey] = nextRevision
	return true, nil
}

func schedulerRuntimeUpdate(revision int64) service.OpenAISchedulerRuntimePolicyUpdate {
	return service.OpenAISchedulerRuntimePolicyUpdate{
		ExpectedRevision:         revision,
		Preset:                   service.OpenAISchedulerPolicyPresetBalanced,
		AdvancedEnabled:          true,
		StickySoftScoringEnabled: false,
		TopK:                     10,
		PreferSubscriptionPool:   true,
		PriorityWeight:           0.8,
		LoadWeight:               1.2,
		QueueWeight:              1,
		ErrorRateWeight:          1,
		TTFTWeight:               0.8,
		PreferSoonestResetWeight: 0.4,
		QuotaHeadroomWeight:      0.7,
		UpstreamCostEnabled:      true,
		UpstreamCostWeight:       0.6,
		SessionStickyWeight:      0.5,
	}
}

func TestOpenAISchedulerRuntimeRoutesRequireAdminAndEnforceRevisionCAS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := newSchedulerRuntimeRouteRepo()
	settingService := service.NewSettingService(repo, &config.Config{})
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
		Setting: adminhandler.NewSettingHandler(settingService, nil, nil, nil, nil, nil, nil),
	}}

	router := gin.New()
	auth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("X-Test-Administrator") != "allowed" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false})
			return
		}
		c.Next()
	})
	admin := router.Group("/api/v1/admin")
	admin.Use(gatewayLiteAdminAuth(auth, &config.Config{}))
	registerGatewayLiteRuntimeRoutes(admin, handlers)

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/runtime/openai-scheduler", nil))
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/runtime/openai-scheduler", nil)
	request.Header.Set("X-Test-Administrator", "allowed")
	router.ServeHTTP(authorized, request)
	require.Equal(t, http.StatusOK, authorized.Code, authorized.Body.String())

	updateBody, err := json.Marshal(schedulerRuntimeUpdate(0))
	require.NoError(t, err)
	updated := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/runtime/openai-scheduler", bytes.NewReader(updateBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Administrator", "allowed")
	router.ServeHTTP(updated, request)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	require.Contains(t, updated.Body.String(), `"revision":1`)

	conflict := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/runtime/openai-scheduler", bytes.NewReader(updateBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Administrator", "allowed")
	router.ServeHTTP(conflict, request)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())

	resetBody := bytes.NewBufferString(`{"expected_revision":1}`)
	reset := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/ops/runtime/openai-scheduler/reset", resetBody)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Administrator", "allowed")
	router.ServeHTTP(reset, request)
	require.Equal(t, http.StatusOK, reset.Code, reset.Body.String())
	require.Contains(t, reset.Body.String(), `"revision":2`)
	require.Contains(t, reset.Body.String(), `"advanced_enabled":false`)
}
