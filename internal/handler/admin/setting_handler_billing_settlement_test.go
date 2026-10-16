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

func newBillingSettlementHandlerTest(t *testing.T) (*SettingHandler, *service.SettingService) {
	t.Helper()
	repo := &settingHandlerRepoStub{values: map[string]string{}}
	svc := service.NewSettingService(repo, &config.Config{})
	return NewSettingHandler(svc, nil, nil, nil, nil, nil, nil), svc
}

func billingSettlementResponseData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data, ok := body.Data.(map[string]any)
	require.True(t, ok)
	return data
}

func TestSettingHandlerGetBillingSettlementSettings_DefaultsToLegacy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler, _ := newBillingSettlementHandlerTest(t)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/billing-settlement", nil)

	handler.GetBillingSettlementSettings(c)

	require.Equal(t, http.StatusOK, rec.Code)
	data := billingSettlementResponseData(t, rec)
	require.Equal(t, false, data["idempotent_enabled"])
	require.Equal(t, service.BillingSettlementModeLegacy, data["mode"])
	require.Equal(t, true, data["legacy_warning"])
}

func TestSettingHandlerUpdateBillingSettlementSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name          string
		body          string
		enabled       bool
		mode          string
		legacyWarning bool
	}{
		{name: "enable idempotent settlement", body: `{"idempotent_enabled":true}`, enabled: true, mode: service.BillingSettlementModeIdempotentV2},
		{name: "keep legacy settlement", body: `{"idempotent_enabled":false}`, enabled: false, mode: service.BillingSettlementModeLegacy, legacyWarning: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, svc := newBillingSettlementHandlerTest(t)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/billing-settlement", bytes.NewBufferString(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")

			handler.UpdateBillingSettlementSettings(c)

			require.Equal(t, http.StatusOK, rec.Code)
			data := billingSettlementResponseData(t, rec)
			require.Equal(t, tt.enabled, data["idempotent_enabled"])
			require.Equal(t, tt.mode, data["mode"])
			require.Equal(t, tt.legacyWarning, data["legacy_warning"])
			require.Equal(t, tt.enabled, svc.GatewayLiteIdempotentSettlementEnabled())
		})
	}
}

func TestSettingHandlerUpdateBillingSettlementSettings_RejectsInvalidBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, body := range []string{`{}`, `{"idempotent_enabled":`, `{"idempotent_enabled":null}`} {
		handler, _ := newBillingSettlementHandlerTest(t)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/billing-settlement", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")

		handler.UpdateBillingSettlementSettings(c)

		require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", body)
	}
}
