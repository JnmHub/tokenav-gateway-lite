//go:build unit

package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type duplicateAccountAdminServiceStub struct {
	service.AdminService
	account      *service.Account
	calls        int
	accountID    int64
	actorScope   string
	operationKey string
	created      bool
	recoverCalls int
}

type failOnceMarkSucceededRepo struct {
	*memoryIdempotencyRepoStub
	failNext bool
}

func (r *failOnceMarkSucceededRepo) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	if r.failNext {
		r.failNext = false
		return errors.New("mark succeeded failed")
	}
	return r.memoryIdempotencyRepoStub.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

func (s *duplicateAccountAdminServiceStub) DuplicateAccount(_ context.Context, accountID int64, actorScope, operationKey string) (*service.Account, error) {
	s.calls++
	s.accountID = accountID
	s.actorScope = actorScope
	s.operationKey = operationKey
	s.created = true
	return s.account, nil
}

func (s *duplicateAccountAdminServiceStub) RecoverDuplicateAccount(_ context.Context, _ int64, _, _ string) (*service.Account, error) {
	s.recoverCalls++
	if !s.created {
		return nil, nil
	}
	return s.account, nil
}

func setupDuplicateAccountRouter(t *testing.T, svc service.AdminService) *gin.Engine {
	t.Helper()
	previousCoordinator := service.DefaultIdempotencyCoordinator()
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previousCoordinator) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
		c.Next()
	})
	handler := &AccountHandler{adminService: svc}
	router.POST("/api/v1/admin/accounts/:id/duplicate", handler.Duplicate)
	return router
}

func TestDuplicateAccountHandlerRedactsCredentialsAndReplays(t *testing.T) {
	svc := &duplicateAccountAdminServiceStub{account: &service.Account{
		ID:          43,
		Name:        "primary (Copy)",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: false,
		Credentials: map[string]any{
			"api_key":                 "top-secret-key",
			"header_override_enabled": true,
			"header_overrides": map[string]any{
				"api-key": "nested-header-secret",
			},
		},
	}}
	router := setupDuplicateAccountRouter(t, svc)
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newMemoryIdempotencyRepoStub(), service.DefaultIdempotencyConfig()))

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-account-42")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, 1, svc.calls)
	require.Equal(t, int64(42), svc.accountID)
	require.Equal(t, "admin:77", svc.actorScope)
	require.Equal(t, "duplicate-account-42", svc.operationKey)
	require.NotContains(t, first.Body.String(), "top-secret-key")
	require.NotContains(t, first.Body.String(), "nested-header-secret")
	require.Contains(t, first.Body.String(), `"api-key":""`)
	require.Contains(t, first.Body.String(), `"has_header_overrides":true`)
	require.Contains(t, first.Body.String(), `"schedulable":false`)
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
}

func TestDuplicateAccountHandlerRejectsInvalidID(t *testing.T) {
	svc := &duplicateAccountAdminServiceStub{}
	router := setupDuplicateAccountRouter(t, svc)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/not-a-number/duplicate", nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Zero(t, svc.calls)
}

func TestDuplicateAccountHandlerRequiresValidIdempotencyKeyEvenWithoutCoordinator(t *testing.T) {
	svc := &duplicateAccountAdminServiceStub{}
	router := setupDuplicateAccountRouter(t, svc)
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
			request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/duplicate", nil)
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

func TestDuplicateAccountHandlerRecoversAfterResultPersistenceFailure(t *testing.T) {
	svc := &duplicateAccountAdminServiceStub{account: &service.Account{
		ID: 43, Name: "primary (Copy)", Platform: service.PlatformAnthropic,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: false,
	}}
	router := setupDuplicateAccountRouter(t, svc)
	repo := &failOnceMarkSucceededRepo{memoryIdempotencyRepoStub: newMemoryIdempotencyRepoStub(), failNext: true}
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, service.DefaultIdempotencyConfig()))

	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/duplicate", nil)
		request.Header.Set("Idempotency-Key", "duplicate-account-42-recovery")
		router.ServeHTTP(recorder, request)
		return recorder
	}
	first := call()
	second := call()

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", first.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Recovered"))
	require.Equal(t, 1, svc.calls, "结果落库不确定时不能重复创建账号")
	require.Equal(t, 2, svc.recoverCalls)
}
