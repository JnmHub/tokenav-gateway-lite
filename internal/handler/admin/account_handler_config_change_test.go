package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type handlerConfigChangedClientFunc func(context.Context, gatewaylite.GatewayConfigChangedRequest) (*gatewaylite.GatewayConfigChangedResponse, error)

func (f handlerConfigChangedClientFunc) ReportGatewayConfigChanged(ctx context.Context, req gatewaylite.GatewayConfigChangedRequest) (*gatewaylite.GatewayConfigChangedResponse, error) {
	return f(ctx, req)
}

func TestAccountHandlerSetSchedulableReportsConfigChange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	requests := make([]gatewaylite.GatewayConfigChangedRequest, 0)
	notifier := gatewaylite.NewConfigChangeNotifier(handlerConfigChangedClientFunc(func(_ context.Context, req gatewaylite.GatewayConfigChangedRequest) (*gatewaylite.GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return &gatewaylite.GatewayConfigChangedResponse{OK: true}, nil
	}), gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{GatewayCode: "test-gw", Region: "test"}), time.Millisecond, 50*time.Millisecond)
	gatewaylite.SetDefaultConfigChangeNotifier(notifier)

	adminSvc := newStubAdminService()
	router := gin.New()
	accountHandler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/:id/schedulable", accountHandler.SetSchedulable)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/3/schedulable", bytes.NewBufferString(`{"schedulable":false}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 1
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "accounts", requests[0].Resource)
	require.Equal(t, "schedulable", requests[0].Action)
	require.EqualValues(t, 3, requests[0].ResourceID)
	require.Equal(t, "test-gw", requests[0].GatewayCode)
}

func TestAccountHandlerUpdateReportsConfigChange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var mu sync.Mutex
	requests := make([]gatewaylite.GatewayConfigChangedRequest, 0)
	notifier := gatewaylite.NewConfigChangeNotifier(handlerConfigChangedClientFunc(func(_ context.Context, req gatewaylite.GatewayConfigChangedRequest) (*gatewaylite.GatewayConfigChangedResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		return &gatewaylite.GatewayConfigChangedResponse{OK: true}, nil
	}), gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{GatewayCode: "test-gw", Region: "test"}), time.Millisecond, 50*time.Millisecond)
	gatewaylite.SetDefaultConfigChangeNotifier(notifier)

	adminSvc := newStubAdminService()
	router := gin.New()
	accountHandler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.PUT("/api/v1/admin/accounts/:id", accountHandler.Update)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/3", bytes.NewBufferString(`{"status":"inactive"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(requests) == 1
	}, time.Second, 10*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "accounts", requests[0].Resource)
	require.Equal(t, "update", requests[0].Action)
	require.EqualValues(t, 3, requests[0].ResourceID)
}
