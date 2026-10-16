package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestApplyOpsErrorCategoryFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest("GET", "/api/v1/admin/ops/errors?category=quota", nil)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = req

	filter := &service.OpsErrorLogFilter{}
	applyOpsErrorCategoryFilter(ctx, filter)

	require.Empty(t, filter.ErrorPhasesAny)
	require.Equal(t, []string{"billing_error", "subscription_error"}, filter.ErrorTypesAny)
}
