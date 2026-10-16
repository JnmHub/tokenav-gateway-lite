//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type channelMonitorDuplicateHandlerRepo struct {
	service.ChannelMonitorRepository
	mu          sync.Mutex
	source      *service.ChannelMonitor
	stored      *service.ChannelMonitor
	createCalls int
}

func (r *channelMonitorDuplicateHandlerRepo) GetByID(_ context.Context, id int64) (*service.ChannelMonitor, error) {
	if r.source == nil || r.source.ID != id {
		return nil, service.ErrChannelMonitorNotFound
	}
	cloned := *r.source
	return &cloned, nil
}

func (r *channelMonitorDuplicateHandlerRepo) Create(_ context.Context, monitor *service.ChannelMonitor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stored != nil {
		return errors.New("duplicate key value violates unique constraint")
	}
	r.createCalls++
	monitor.ID = 99
	monitor.CreatedAt = time.Unix(99, 0).UTC()
	monitor.UpdatedAt = monitor.CreatedAt
	cloned := *monitor
	r.stored = &cloned
	return nil
}

func (r *channelMonitorDuplicateHandlerRepo) FindByDuplicateOperationID(_ context.Context, operationID string) (*service.ChannelMonitor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stored == nil || r.stored.DuplicateOperationID != operationID {
		return nil, nil
	}
	cloned := *r.stored
	return &cloned, nil
}

func setupChannelMonitorDuplicateRouter(t *testing.T, repo *channelMonitorDuplicateHandlerRepo) *gin.Engine {
	t.Helper()
	previousCoordinator := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previousCoordinator) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
		c.Next()
	})
	handler := NewChannelMonitorHandler(service.NewChannelMonitorService(repo, nil))
	router.POST("/api/v1/admin/channel-monitors/:id/duplicate", handler.Duplicate)
	return router
}

func TestDuplicateChannelMonitorHandlerReplaysMaskedDisabledCopy(t *testing.T) {
	repo := &channelMonitorDuplicateHandlerRepo{source: &service.ChannelMonitor{
		ID: 42, Name: "Primary", Provider: service.MonitorProviderOpenAI,
		APIMode: service.MonitorAPIModeResponses, Endpoint: "https://example.com",
		APIKey: "encrypted:never-return-this", PrimaryModel: "gpt-5.6-sol",
		Enabled: true, IntervalSeconds: 60, BodyOverrideMode: service.MonitorBodyOverrideModeOff,
	}}
	router := setupChannelMonitorDuplicateRouter(t, repo)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/channel-monitors/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-monitor-42")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 1, repo.createCalls)
	require.NotNil(t, repo.stored)
	require.Equal(t, int64(77), repo.stored.CreatedBy)
	require.False(t, repo.stored.Enabled)
	require.Contains(t, first.Body.String(), `"name":"Primary (Copy)"`)
	require.Contains(t, first.Body.String(), `"api_key_masked":"***"`)
	require.Contains(t, first.Body.String(), `"enabled":false`)
	require.NotContains(t, first.Body.String(), "encrypted:never-return-this")
	require.NotContains(t, first.Body.String(), "duplicate_operation_id")
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
}

func TestDuplicateChannelMonitorHandlerRequiresIdempotencyKey(t *testing.T) {
	repo := &channelMonitorDuplicateHandlerRepo{source: &service.ChannelMonitor{ID: 42}}
	router := setupChannelMonitorDuplicateRouter(t, repo)
	service.SetDefaultIdempotencyCoordinator(nil)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/channel-monitors/42/duplicate", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	require.Zero(t, repo.createCalls)
}
