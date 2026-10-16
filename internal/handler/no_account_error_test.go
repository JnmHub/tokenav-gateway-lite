package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type fakeModelAvailabilityDiagnoser struct {
	calls []fakeModelAvailabilityCall
	resp  service.ModelAvailabilityDiagnosis
}

type fakeModelAvailabilityCall struct {
	GroupID  *int64
	Model    string
	Platform string
}

func (f *fakeModelAvailabilityDiagnoser) DiagnoseModelAvailabilityForPlatform(
	_ context.Context,
	groupID *int64,
	model string,
	platform string,
) service.ModelAvailabilityDiagnosis {
	f.calls = append(f.calls, fakeModelAvailabilityCall{
		GroupID:  groupID,
		Model:    model,
		Platform: platform,
	})
	return f.resp
}

func handlerInt64Ptr(v int64) *int64 { return &v }

func testGinContextForNoAccountError() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c
}

func TestClassifyNoAccountError_ModelNotSupportedReturns404(t *testing.T) {
	c := testGinContextForNoAccountError()
	groupID := handlerInt64Ptr(42)
	diag := &fakeModelAvailabilityDiagnoser{
		resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: false},
	}

	cls := classifyNoAccountErrorFromGin(c, diag, groupID, "gpt-5.9", "gpt-5.9", service.PlatformOpenAI)

	require.Equal(t, http.StatusNotFound, cls.Status)
	require.Equal(t, "model_not_found", cls.ErrType)
	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "gpt-5.9")
	require.Len(t, diag.calls, 1)
	require.NotNil(t, diag.calls[0].GroupID)
	require.Equal(t, int64(42), *diag.calls[0].GroupID)
	require.Equal(t, "gpt-5.9", diag.calls[0].Model)
	require.Equal(t, service.PlatformOpenAI, diag.calls[0].Platform)
	require.True(t, service.HasOpsClientBusinessLimited(c))
	require.Equal(t, service.OpsClientBusinessLimitedReasonLocalModelConfiguration, service.OpsClientBusinessLimitedReason(c))
}

func TestClassifyNoAccountError_UsesRoutingGroupEvenWhenNil(t *testing.T) {
	c := testGinContextForNoAccountError()
	diag := &fakeModelAvailabilityDiagnoser{
		resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: false},
	}

	cls := classifyNoAccountErrorFromGin(c, diag, nil, "gpt-5.9", "gpt-5.9", service.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.Len(t, diag.calls, 1)
	require.Nil(t, diag.calls[0].GroupID, "nil routing group must diagnose the ungrouped/default scheduling pool")
}

func TestClassifyNoAccountError_NoAccountsStays503(t *testing.T) {
	c := testGinContextForNoAccountError()
	diag := &fakeModelAvailabilityDiagnoser{
		resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: false, HasModelSupport: false},
	}

	cls := classifyNoAccountErrorFromGin(c, diag, handlerInt64Ptr(7), "gpt-5.9", "gpt-5.9", service.PlatformOpenAI)

	require.Equal(t, http.StatusServiceUnavailable, cls.Status)
	require.Equal(t, "api_error", cls.ErrType)
	require.False(t, cls.ModelNotFound)
}

func TestClassifyNoAccountError_GrokDistinguishesUnsupportedModelAndUnavailablePool(t *testing.T) {
	tests := []struct {
		name       string
		diagnosis  service.ModelAvailabilityDiagnosis
		wantStatus int
		wantType   string
	}{
		{
			name:       "configured Grok accounts do not support model",
			diagnosis:  service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: false},
			wantStatus: http.StatusNotFound,
			wantType:   "model_not_found",
		},
		{
			name:       "Grok group has no schedulable account",
			diagnosis:  service.ModelAvailabilityDiagnosis{HasAccountsInPool: false, HasModelSupport: false},
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "api_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := testGinContextForNoAccountError()
			diag := &fakeModelAvailabilityDiagnoser{resp: tt.diagnosis}

			cls := classifyNoAccountErrorFromGin(c, diag, handlerInt64Ptr(17), "grok-4.5", "grok-4.5", service.PlatformGrok)

			require.Equal(t, tt.wantStatus, cls.Status)
			require.Equal(t, tt.wantType, cls.ErrType)
			require.Len(t, diag.calls, 1)
			require.Equal(t, service.PlatformGrok, diag.calls[0].Platform)
		})
	}
}

func TestClassifyNoAccountError_DisplayModelOnlyAffectsMessage(t *testing.T) {
	c := testGinContextForNoAccountError()
	diag := &fakeModelAvailabilityDiagnoser{
		resp: service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: false},
	}

	cls := classifyNoAccountErrorFromGin(c, diag, handlerInt64Ptr(7), "gpt-5.4-mini", "claude-sonnet-4-5", service.PlatformOpenAI)

	require.True(t, cls.ModelNotFound)
	require.Contains(t, cls.Message, "claude-sonnet-4-5")
	require.Equal(t, "gpt-5.4-mini", diag.calls[0].Model)
}

func TestOpenAIHandleStreamingAwareModelNotFoundJSONShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	h := &OpenAIGatewayHandler{}
	h.handleStreamingAwareError(c, http.StatusNotFound, "model_not_found", `Model "gpt-5.9" is not supported by any configured account in this gateway route`, false)

	require.Equal(t, http.StatusNotFound, w.Code)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &parsed))
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "model_not_found", errorObj["type"])
	require.Contains(t, errorObj["message"], "gpt-5.9")
}
