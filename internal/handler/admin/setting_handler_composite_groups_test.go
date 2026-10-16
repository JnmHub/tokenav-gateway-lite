package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func compositeGroupsResponseData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	data, ok := body.Data.(map[string]any)
	require.True(t, ok)
	return data
}

func TestSettingHandlerCompositeGroupsRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyCompositeGroupsEnabled: "false"}}
	svc := service.NewSettingService(repo, &config.Config{})
	handler := NewSettingHandler(svc, nil, nil, nil, nil, nil, nil)

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/composite-groups", nil)
	handler.GetCompositeGroupsSettings(getContext)
	require.Equal(t, http.StatusOK, getRecorder.Code)
	require.Equal(t, false, compositeGroupsResponseData(t, getRecorder)["enabled"])

	putRecorder := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/composite-groups", bytes.NewBufferString(`{"enabled":true}`))
	putContext.Request.Header.Set("Content-Type", "application/json")
	handler.UpdateCompositeGroupsSettings(putContext)
	require.Equal(t, http.StatusOK, putRecorder.Code)
	require.Equal(t, true, compositeGroupsResponseData(t, putRecorder)["enabled"])
	require.Equal(t, "true", repo.values[service.SettingKeyCompositeGroupsEnabled])
	require.True(t, svc.CompositeGroupsEnabled())
}

func TestSettingHandlerCompositeGroupsRejectsMissingBoolean(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyCompositeGroupsEnabled: "false"}}
	handler := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":`} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/composite-groups", bytes.NewBufferString(body))
		context.Request.Header.Set("Content-Type", "application/json")
		handler.UpdateCompositeGroupsSettings(context)
		require.Equal(t, http.StatusBadRequest, recorder.Code, "body=%s", body)
	}
}
