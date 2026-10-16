package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type operationAuditHandlerRepo struct {
	listCalls int
	preview   *service.OperationAuditClearPreview
}

func (r *operationAuditHandlerRepo) Insert(context.Context, *service.OperationAuditLog) (int64, error) {
	return 1, nil
}
func (r *operationAuditHandlerRepo) BatchInsert(context.Context, []*service.OperationAuditLog) (int64, error) {
	return 0, nil
}
func (r *operationAuditHandlerRepo) UpdateResult(context.Context, int64, string, int, int64, map[string]any) error {
	return nil
}
func (r *operationAuditHandlerRepo) List(context.Context, *service.OperationAuditFilter) (*service.OperationAuditList, error) {
	r.listCalls++
	return &service.OperationAuditList{}, nil
}
func (r *operationAuditHandlerRepo) GetByID(context.Context, int64) (*service.OperationAuditLog, error) {
	return nil, service.ErrOperationAuditNotFound
}
func (r *operationAuditHandlerRepo) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}
func (r *operationAuditHandlerRepo) ClearAllWithTrace(context.Context, *service.OperationAuditLog) (int64, error) {
	return 0, nil
}
func (r *operationAuditHandlerRepo) PreviewClear(context.Context) (*service.OperationAuditClearPreview, error) {
	return r.preview, nil
}

func TestOperationAuditListRejectsReversedTimeRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &operationAuditHandlerRepo{}
	handler := NewOperationAuditHandler(service.NewOperationAuditService(repo, nil))
	router := gin.New()
	router.GET("/audit-logs", handler.List)

	request := httptest.NewRequest(http.MethodGet, "/audit-logs?start_time=2026-08-02T00:00:00Z&end_time=2026-08-01T00:00:00Z", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, repo.listCalls)
}

func TestOperationAuditClearPreviewReturnsCountAndRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldest := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	repo := &operationAuditHandlerRepo{preview: &service.OperationAuditClearPreview{Rows: 7, OldestAt: &oldest, NewestAt: &newest}}
	handler := NewOperationAuditHandler(service.NewOperationAuditService(repo, nil))
	router := gin.New()
	router.GET("/audit-logs/clear/preview", handler.PreviewClear)

	request := httptest.NewRequest(http.MethodGet, "/audit-logs/clear/preview", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"rows":7`)
	require.Contains(t, response.Body.String(), "2026-07-01")
	require.Contains(t, response.Body.String(), "2026-08-01")
}
