package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageTaskGatewayAdminKeyRequiresPositiveUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/image-tasks", nil)
	contextWithoutUser, _ := gin.CreateTestContext(httptest.NewRecorder())
	contextWithoutUser.Request = request
	contextWithoutUser.Set("auth_method", "gateway_lite_admin_key")
	require.True(t, imageTaskUsesGatewayAdminKey(contextWithoutUser))
	_, ok := imageTaskAdminUserID(contextWithoutUser)
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, contextWithoutUser.Writer.Status())

	contextWithUser, _ := gin.CreateTestContext(httptest.NewRecorder())
	contextWithUser.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/image-tasks?user_id=42", nil)
	contextWithUser.Set("auth_method", "gateway_lite_admin_key")
	userID, ok := imageTaskAdminUserID(contextWithUser)
	require.True(t, ok)
	require.EqualValues(t, 42, userID)

	localAdmin, _ := gin.CreateTestContext(httptest.NewRecorder())
	localAdmin.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/image-tasks", nil)
	localAdmin.Set("auth_method", "jwt")
	require.False(t, imageTaskUsesGatewayAdminKey(localAdmin), "local JWT admins keep the all-node view")
}

func TestImageTaskAdminCapabilitiesDefaultsDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	(&ImageTaskHandler{}).AdminCapabilities(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"schema_version":1,"enabled":false}`, recorder.Body.String())
}

func TestImageTaskDangerousActionsRequireExactConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		body     string
		expected string
		ok       bool
	}{
		{body: `{}`, expected: "REPLAY_IMAGE_TASK", ok: false},
		{body: `{"confirm":"wrong"}`, expected: "REPLAY_IMAGE_TASK", ok: false},
		{body: `{"confirm":"REPLAY_IMAGE_TASK"}`, expected: "REPLAY_IMAGE_TASK", ok: true},
		{body: `{"confirm":"CLEANUP_IMAGE_TASKS"}`, expected: "CLEANUP_IMAGE_TASKS", ok: true},
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(test.body))
		c.Request.Header.Set("Content-Type", "application/json")
		require.Equal(t, test.ok, imageTaskRequireConfirmation(c, test.expected))
		if !test.ok {
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		}
	}
}

func TestImageTaskDTOExposesUserIDOnlyToAdmin(t *testing.T) {
	now := time.Now().UTC()
	task := &service.ImageTask{
		ID: "95885982-c5bc-4499-850f-f6c3ef65cb32", UserID: 88,
		State: service.ImageTaskStateFailed, SettlementState: service.ImageTaskSettlementReleased,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	admin, err := imageTaskToDTO(context.Background(), nil, task, "admin")
	require.NoError(t, err)
	require.EqualValues(t, 88, admin.UserID)
	user, err := imageTaskToDTO(context.Background(), nil, task, "user")
	require.NoError(t, err)
	require.Zero(t, user.UserID)
}

func TestImageTaskDTORewritesOnlyUserFacingFailureMessage(t *testing.T) {
	now := time.Now().UTC()
	task := &service.ImageTask{
		ID: "95885982-c5bc-4499-850f-f6c3ef65cb33", UserID: 88,
		State: service.ImageTaskStateFailed, SettlementState: service.ImageTaskSettlementReleased,
		ErrorCode: "upstream_execution_failed",
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	ctx := service.WithGatewayCustomErrorMessage(context.Background(), "请稍后重试")

	user, err := imageTaskToDTO(ctx, nil, task, "user")
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStateFailed, user.State)
	require.Equal(t, "upstream_execution_failed", user.ErrorCode)
	require.Equal(t, "请稍后重试", user.ErrorMessage)

	admin, err := imageTaskToDTO(ctx, nil, task, "admin")
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStateFailed, admin.State)
	require.Equal(t, "upstream_execution_failed", admin.ErrorCode)
	require.Equal(t, "The upstream image service failed after retries", admin.ErrorMessage)
}
