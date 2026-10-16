package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexAdmissionHandlerRepo struct{ values map[string]string }

func (r *codexAdmissionHandlerRepo) Get(context.Context, string) (*service.Setting, error) {
	panic("unused")
}
func (r *codexAdmissionHandlerRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}
func (r *codexAdmissionHandlerRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *codexAdmissionHandlerRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unused")
}
func (r *codexAdmissionHandlerRepo) SetMultiple(context.Context, map[string]string) error {
	panic("unused")
}
func (r *codexAdmissionHandlerRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unused")
}
func (r *codexAdmissionHandlerRepo) Delete(context.Context, string) error { panic("unused") }

func TestSettingHandlerCodexClientAdmissionGetAndPut(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &codexAdmissionHandlerRepo{values: map[string]string{}}
	settingService := service.NewSettingService(repo, &config.Config{})
	handler := NewSettingHandler(settingService, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/settings", handler.GetCodexClientAdmissionSettings)
	router.PUT("/settings", handler.UpdateCodexClientAdmissionSettings)

	getRecorder := httptest.NewRecorder()
	router.ServeHTTP(getRecorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	require.Equal(t, http.StatusOK, getRecorder.Code)
	require.Contains(t, getRecorder.Body.String(), `"mode":"audit_only"`)
	require.NotContains(t, getRecorder.Body.String(), "User-Agent")

	valid := []byte(`{"mode":"enforce","min_version":"0.144.1","max_version":"0.150.0","allowed_clients":["desktop"],"denied_clients":["cli"],"app_server_enabled":false}`)
	putRecorder := httptest.NewRecorder()
	router.ServeHTTP(putRecorder, httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(valid)))
	require.Equal(t, http.StatusOK, putRecorder.Code)
	require.Equal(t, "enforce", settingService.CodexClientAdmissionRuntime().Current().Mode)

	invalid := []byte(`{"mode":"enforce","allowed_clients":["free-form-header"],"denied_clients":[]}`)
	invalidRecorder := httptest.NewRecorder()
	router.ServeHTTP(invalidRecorder, httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(invalid)))
	require.Equal(t, http.StatusBadRequest, invalidRecorder.Code)
}
