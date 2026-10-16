package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newTailBurstHandlerTest(t *testing.T, raw string) (*OpsHandler, *testSettingRepo) {
	t.Helper()
	repo := newTestSettingRepo()
	if raw != "" {
		repo.values[service.SettingKeyOpsAdvancedSettings] = raw
	}
	// Deliberately disable Ops monitoring: the narrow Tail-Burst endpoint must
	// remain usable from the main system-settings page in this state.
	svc := service.NewOpsService(nil, repo, &config.Config{
		Ops: config.OpsConfig{Enabled: false},
	}, nil, nil, nil, nil, nil, nil, nil, nil)
	return NewOpsHandler(svc), repo
}

func TestOpsHandlerCodexTailBurst_GetWorksWhenMonitoringDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTailBurstHandlerTest(t, `{"codex_tail_burst":{"enabled":true}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/codex-tail-burst", nil)

	h.GetCodexTailBurstSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":true`)
}

func TestOpsHandlerCodexTailBurst_DoesNotUnprotectFullAdvancedSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTailBurstHandlerTest(t, `{"codex_tail_burst":{"enabled":true}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/advanced-settings", nil)

	h.GetAdvancedSettings(c)

	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/advanced-settings", bytes.NewBufferString(`{"codex_tail_burst":{"enabled":false}}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateAdvancedSettings(c)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestOpsHandlerCodexTailBurst_UpdateWorksWhenMonitoringDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, repo := newTailBurstHandlerTest(t, `{"codex_tail_burst":{"enabled":false}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/codex-tail-burst", bytes.NewBufferString(`{"enabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.UpdateCodexTailBurstSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"enabled":true`)
	require.Equal(t, `{"enabled":true}`, repo.values[service.SettingKeyOpsCodexTailBurst])
	// The narrow endpoint must not rewrite the aggregate document it was given.
	require.Equal(t, `{"codex_tail_burst":{"enabled":false}}`, repo.values[service.SettingKeyOpsAdvancedSettings])
}

func TestOpsHandlerCodexTailBurst_UpdateRejectsMissingEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newTailBurstHandlerTest(t, `{"codex_tail_burst":{"enabled":false}}`)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":`} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/ops/codex-tail-burst", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")

		h.UpdateCodexTailBurstSettings(c)

		require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", body)
	}
}
