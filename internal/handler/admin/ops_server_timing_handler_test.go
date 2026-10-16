package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func serverTimingHandlerData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	data, ok := envelope.Data.(map[string]any)
	require.True(t, ok)
	return data
}

func TestOpsHandlerServerTimingTemporaryPresets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	controller := servertiming.DefaultController()
	controller.DisableTemporary()
	t.Cleanup(controller.DisableTemporary)
	handler := &OpsHandler{}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/ops/runtime/server-timing/enable", bytes.NewBufferString(`{"preset":"15m"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.EnableServerTimingTemporary(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	data := serverTimingHandlerData(t, recorder)
	require.Equal(t, true, data["temporary_enabled"])
	until, err := time.Parse(time.RFC3339Nano, data["temporary_until"].(string))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(15*time.Minute), until, 2*time.Second)

	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/ops/runtime/server-timing/disable", nil)
	handler.DisableServerTimingTemporary(c)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, false, serverTimingHandlerData(t, recorder)["temporary_enabled"])
}

func TestOpsHandlerServerTimingRejectsFreeFormDuration(t *testing.T) {
	handler := &OpsHandler{}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/ops/runtime/server-timing/enable", bytes.NewBufferString(`{"preset":"999m"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.EnableServerTimingTemporary(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
