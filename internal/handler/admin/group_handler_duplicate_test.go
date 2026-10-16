//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type duplicateGroupAdminServiceStub struct {
	service.AdminService
	group        *service.Group
	calls        int
	groupID      int64
	actorScope   string
	operationKey string
	created      bool
	recoverCalls int
}

func (s *duplicateGroupAdminServiceStub) DuplicateGroup(_ context.Context, groupID int64, actorScope, operationKey string) (*service.Group, error) {
	s.calls++
	s.groupID = groupID
	s.actorScope = actorScope
	s.operationKey = operationKey
	s.created = true
	return s.group, nil
}

func (s *duplicateGroupAdminServiceStub) RecoverDuplicateGroup(_ context.Context, _ int64, _, _ string) (*service.Group, error) {
	s.recoverCalls++
	if !s.created {
		return nil, nil
	}
	return s.group, nil
}

func setupDuplicateGroupRouter(t *testing.T, svc service.AdminService) *gin.Engine {
	t.Helper()
	previousCoordinator := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previousCoordinator) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
		c.Next()
	})
	handler := &GroupHandler{adminService: svc}
	router.POST("/api/v1/admin/groups/:id/duplicate", handler.Duplicate)
	return router
}

func TestDuplicateGroupHandlerReplaysWithoutCreatingAnotherGroup(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{group: &service.Group{
		ID: 43, Name: "primary (Copy)", Platform: service.PlatformOpenAI,
		Status: "inactive", Hydrated: true, AccountCount: 2,
	}}
	router := setupDuplicateGroupRouter(t, svc)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-group-42")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, int64(42), svc.groupID)
	require.Equal(t, "admin:77", svc.actorScope)
	require.Equal(t, "duplicate-group-42", svc.operationKey)
	require.Contains(t, first.Body.String(), `"status":"inactive"`)
	require.Contains(t, first.Body.String(), `"account_count":2`)
	require.NotContains(t, first.Body.String(), "duplicate_operation_id")
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
}

func TestDuplicateGroupHandlerRejectsInvalidID(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{}
	router := setupDuplicateGroupRouter(t, svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/not-a-number/duplicate", nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, svc.calls)
}

func TestDuplicateGroupHandlerRequiresValidIdempotencyKeyWithoutCoordinator(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{}
	router := setupDuplicateGroupRouter(t, svc)
	service.SetDefaultIdempotencyCoordinator(nil)

	tests := []struct {
		name string
		key  string
		code string
	}{
		{name: "missing", code: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "invalid", key: "bad key with spaces", code: "IDEMPOTENCY_KEY_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)
			if tt.key != "" {
				request.Header.Set("Idempotency-Key", tt.key)
			}

			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Contains(t, recorder.Body.String(), tt.code)
			require.Zero(t, svc.calls)
		})
	}
}

func TestDuplicateGroupHandlerRecoversAfterResultPersistenceFailure(t *testing.T) {
	svc := &duplicateGroupAdminServiceStub{group: &service.Group{
		ID: 43, Name: "primary (Copy)", Platform: service.PlatformAnthropic, Status: "inactive", Hydrated: true,
	}}
	router := setupDuplicateGroupRouter(t, svc)
	repo := &failOnceMarkSucceededRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub(), failNext: true}
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-group-42-recovery")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", first.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, 1, svc.calls, "结果落库不确定时不能重复创建分组")
	require.Equal(t, 2, svc.recoverCalls)
}

func TestDuplicateGroupHandlerReportsPreciseConfigChange(t *testing.T) {
	var mu sync.Mutex
	requests := make([]gatewaylite.GatewayConfigChangedRequest, 0, 1)
	notifier := gatewaylite.NewConfigChangeNotifier(handlerConfigChangedClientFunc(func(_ context.Context, req gatewaylite.GatewayConfigChangedRequest) (*gatewaylite.GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return &gatewaylite.GatewayConfigChangedResponse{OK: true}, nil
	}), gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{GatewayCode: "test-gw", Region: "test"}), time.Millisecond, 50*time.Millisecond)
	gatewaylite.SetDefaultConfigChangeNotifier(notifier)
	t.Cleanup(func() { gatewaylite.SetDefaultConfigChangeNotifier(nil) })

	svc := &duplicateGroupAdminServiceStub{group: &service.Group{
		ID: 43, Name: "primary (Copy)", Platform: service.PlatformOpenAI, Status: "inactive", Hydrated: true,
	}}
	router := setupDuplicateGroupRouter(t, svc)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/groups/42/duplicate", nil)
	request.Header.Set("Idempotency-Key", "duplicate-group-config-change")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 1
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "groups", requests[0].Resource)
	require.Equal(t, "duplicate", requests[0].Action)
	require.EqualValues(t, 43, requests[0].ResourceID)
}
