package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type v161StepUpConsumer struct {
	enabled bool
	calls   int
}

func (s *v161StepUpConsumer) StepUpEnabled() bool { return s.enabled }

func (s *v161StepUpConsumer) ConsumeStepUpGrant(context.Context, string, service.StepUpChallenge, *service.User) error {
	s.calls++
	return service.ErrStepUpRequired
}

type v161StepUpUserReader struct{}

func (v161StepUpUserReader) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 7, Role: service.RoleAdmin, TotpEnabled: true}, nil
}

func newV161StepUpContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/backups/1", nil)
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 7})
	c.Set("auth_method", "admin_api_key")
	return c, recorder
}

func TestV161StepUpDisabledBypassesSensitiveRouteChallenge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newV161StepUpContext()
	consumer := &v161StepUpConsumer{enabled: false}

	ok := enforceStepUp(c, consumer, v161StepUpUserReader{}, service.StepUpScopeBackupDelete)

	require.True(t, ok)
	require.Zero(t, consumer.calls)
	require.Equal(t, http.StatusOK, recorder.Code)
}

func TestV161StepUpEnabledKeepsStrongValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, recorder := newV161StepUpContext()
	consumer := &v161StepUpConsumer{enabled: true}

	ok := enforceStepUp(c, consumer, v161StepUpUserReader{}, service.StepUpScopeBackupDelete)

	require.False(t, ok)
	require.Zero(t, consumer.calls, "machine credentials are rejected before grant consumption")
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN")
}
