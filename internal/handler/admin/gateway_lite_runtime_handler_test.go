package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type panelRateLimitHandlerRepoStub struct {
	values    map[string]string
	getErr    error
	setErr    error
	deleteErr error
}

func (s *panelRateLimitHandlerRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (s *panelRateLimitHandlerRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *panelRateLimitHandlerRepoStub) Set(_ context.Context, key, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[key] = value
	return nil
}

func (s *panelRateLimitHandlerRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *panelRateLimitHandlerRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		if err := s.Set(context.Background(), key, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *panelRateLimitHandlerRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}

func (s *panelRateLimitHandlerRepoStub) Delete(_ context.Context, key string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.values, key)
	return nil
}

func newPanelRateLimitHandler(repo *panelRateLimitHandlerRepoStub) *SettingHandler {
	settings := service.NewSettingService(repo, &config.Config{})
	return NewSettingHandler(settings, nil, nil, nil, nil, nil, nil)
}

func decodeRuntimeHandlerData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope response.Response
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	data, ok := envelope.Data.(map[string]any)
	require.True(t, ok)
	return data
}

func newRuntimeHandlerContext(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		c.Request.Header.Set("Content-Type", "application/json")
	}
	return c, recorder
}

func TestSettingHandlerGetPanelRateLimitSettingsIncludesActiveStats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := middleware.NewGatewayLitePanelRateLimiter(2, 1, time.Minute)
	router := gin.New()
	router.Use(limiter.Handler())
	router.GET("/admin/test", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/admin/test", nil)
	req.RemoteAddr = "192.0.2.10:1000"
	router.ServeHTTP(httptest.NewRecorder(), req)

	handler := newPanelRateLimitHandler(&panelRateLimitHandlerRepoStub{values: map[string]string{}})
	c, recorder := newRuntimeHandlerContext(http.MethodGet, "/api/v1/admin/ops/runtime/panel-rate-limit", "")
	handler.GetPanelRateLimitSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeRuntimeHandlerData(t, recorder)
	settings := data["settings"].(map[string]any)
	require.Equal(t, service.PanelRateLimitPresetDefault, settings["preset"])
	require.EqualValues(t, 240, settings["user_rpm"])
	stats := data["runtime_stats"].(map[string]any)
	require.EqualValues(t, 1, stats["checked_total"])
	require.EqualValues(t, 1, stats["active_buckets"])
}

func TestSettingHandlerUpdatePanelRateLimitSettingsUsesPresetValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &panelRateLimitHandlerRepoStub{values: map[string]string{}}
	handler := newPanelRateLimitHandler(repo)
	c, recorder := newRuntimeHandlerContext(http.MethodPut, "/api/v1/admin/ops/runtime/panel-rate-limit",
		`{"enabled":false,"preset":"strict","user_rpm":99999,"heavy_rpm":99999,"window_seconds":999}`)

	handler.UpdatePanelRateLimitSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeRuntimeHandlerData(t, recorder)
	settings := data["settings"].(map[string]any)
	require.Equal(t, false, settings["enabled"])
	require.Equal(t, service.PanelRateLimitPresetStrict, settings["preset"])
	require.EqualValues(t, 120, settings["user_rpm"])
	require.EqualValues(t, 30, settings["heavy_rpm"])
	require.EqualValues(t, 60, settings["window_seconds"])
	require.Contains(t, repo.values[service.SettingKeyPanelRateLimitSettings], `"preset":"strict"`)
}

func TestSettingHandlerUpdatePanelRateLimitSettingsRejectsInvalidCustomLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &panelRateLimitHandlerRepoStub{values: map[string]string{}}
	handler := newPanelRateLimitHandler(repo)
	for _, body := range []string{
		`{}`,
		`{"enabled":true,"preset":"custom","user_rpm":10,"heavy_rpm":11,"window_seconds":60}`,
		`{"enabled":true,"preset":"custom","user_rpm":10,"heavy_rpm":5,"window_seconds":9}`,
		`{"enabled":true,"preset":`,
	} {
		c, recorder := newRuntimeHandlerContext(http.MethodPut, "/api/v1/admin/ops/runtime/panel-rate-limit", body)
		handler.UpdatePanelRateLimitSettings(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, "body=%s", body)
	}
	require.NotContains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
}

func TestSettingHandlerUpdatePanelRateLimitSettingsRedactsRepositoryError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const sensitive = "postgres://user:pass@db.example.invalid/internal"
	handler := newPanelRateLimitHandler(&panelRateLimitHandlerRepoStub{
		values: map[string]string{}, setErr: errors.New(sensitive),
	})
	c, recorder := newRuntimeHandlerContext(http.MethodPut, "/api/v1/admin/ops/runtime/panel-rate-limit",
		`{"enabled":true,"preset":"default"}`)

	handler.UpdatePanelRateLimitSettings(c)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.NotContains(t, recorder.Body.String(), sensitive)
}

func TestSettingHandlerResetPanelRateLimitSettingsRemovesOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &panelRateLimitHandlerRepoStub{values: map[string]string{
		service.SettingKeyPanelRateLimitSettings: `{"enabled":true,"preset":"strict","user_rpm":120,"heavy_rpm":30,"window_seconds":60}`,
	}}
	handler := newPanelRateLimitHandler(repo)
	c, recorder := newRuntimeHandlerContext(http.MethodPost, "/api/v1/admin/ops/runtime/panel-rate-limit/reset", "")

	handler.ResetPanelRateLimitSettings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	data := decodeRuntimeHandlerData(t, recorder)
	settings := data["settings"].(map[string]any)
	require.Equal(t, service.PanelRateLimitPresetDefault, settings["preset"])
	require.NotContains(t, repo.values, service.SettingKeyPanelRateLimitSettings)
}

func TestSettingHandlerGetIngressRejectionSnapshotBoundsAndSensitiveBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ingress.SetNode("handler-test-node")
	t.Cleanup(func() { ingress.SetNode("local") })
	ingress.ObserveRequest()
	ingress.Record(ingress.RejectMalformed)
	handler := &SettingHandler{}

	for _, test := range []struct {
		target  string
		minutes int
	}{
		{target: "/api/v1/admin/ops/ingress-rejections", minutes: 60},
		{target: "/api/v1/admin/ops/ingress-rejections?minutes=1", minutes: 1},
		{target: "/api/v1/admin/ops/ingress-rejections?minutes=1440", minutes: 1440},
	} {
		c, recorder := newRuntimeHandlerContext(http.MethodGet, test.target, "")
		handler.GetIngressRejectionSnapshot(c)
		require.Equal(t, http.StatusOK, recorder.Code, "target=%s", test.target)
		data := decodeRuntimeHandlerData(t, recorder)
		require.EqualValues(t, test.minutes, data["window_minutes"])
		require.Equal(t, "handler-test-node", data["node"])
		lowerBody := strings.ToLower(recorder.Body.String())
		for _, forbidden := range []string{"authorization", "api_key", "request_body", "client_ip", "cookie"} {
			require.NotContains(t, lowerBody, forbidden)
		}
	}

	for _, target := range []string{
		"/api/v1/admin/ops/ingress-rejections?minutes=0",
		"/api/v1/admin/ops/ingress-rejections?minutes=1441",
		"/api/v1/admin/ops/ingress-rejections?minutes=-1",
		"/api/v1/admin/ops/ingress-rejections?minutes=abc",
	} {
		c, recorder := newRuntimeHandlerContext(http.MethodGet, target, "")
		handler.GetIngressRejectionSnapshot(c)
		require.Equal(t, http.StatusBadRequest, recorder.Code, "target=%s", target)
	}
}
