//go:build unit

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type operationAuditRepoStub struct {
	insertErr error
	inserted  []*service.OperationAuditLog
}

func (s *operationAuditRepoStub) Insert(_ context.Context, entry *service.OperationAuditLog) (int64, error) {
	if s.insertErr != nil {
		return 0, s.insertErr
	}
	s.inserted = append(s.inserted, entry)
	return int64(len(s.inserted)), nil
}
func (s *operationAuditRepoStub) BatchInsert(_ context.Context, entries []*service.OperationAuditLog) (int64, error) {
	s.inserted = append(s.inserted, entries...)
	return int64(len(entries)), nil
}
func (s *operationAuditRepoStub) UpdateResult(context.Context, int64, string, int, int64, map[string]any) error {
	return nil
}
func (s *operationAuditRepoStub) List(context.Context, *service.OperationAuditFilter) (*service.OperationAuditList, error) {
	return nil, nil
}
func (s *operationAuditRepoStub) GetByID(context.Context, int64) (*service.OperationAuditLog, error) {
	return nil, service.ErrOperationAuditNotFound
}
func (s *operationAuditRepoStub) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}
func (s *operationAuditRepoStub) ClearAllWithTrace(context.Context, *service.OperationAuditLog) (int64, error) {
	return 0, nil
}

func TestCriticalOperationAuditFailureClosesBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &operationAuditRepoStub{insertErr: errors.New("database unavailable")}
	audit := service.NewOperationAuditService(repo, &config.Config{})
	router := gin.New()
	called := false
	router.GET("/api/v1/admin/accounts/data", func(c *gin.Context) {
		c.Set(string(ContextKeyUser), AuthSubject{UserID: 1})
		c.Set(string(ContextKeyUserRole), service.RoleAdmin)
		c.Set("auth_method", "jwt")
		c.Next()
	}, gin.HandlerFunc(NewOperationAuditMiddleware(audit)), func(c *gin.Context) { called = true; c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil)
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.False(t, called)
	require.Contains(t, recorder.Body.String(), "AUDIT_LOG_UNAVAILABLE")
}

type stepUpConsumerStub struct {
	err      error
	expected service.StepUpChallenge
	token    string
	calls    int
}

func (s *stepUpConsumerStub) ConsumeStepUpGrant(_ context.Context, token string, expected service.StepUpChallenge, _ *service.User) error {
	s.calls++
	s.token = token
	s.expected = expected
	return s.err
}

func TestStepUpRejectsMachineCredentialBeforeGrantConsumption(t *testing.T) {
	c, recorder := newStepUpContext(t)
	c.Set("auth_method", "admin_api_key")
	consumer := &stepUpConsumerStub{}
	ok := enforceStepUp(c, consumer, stepUpUserReaderStub{user: &service.User{ID: 7, Role: service.RoleAdmin, TotpEnabled: true}}, service.StepUpScopeBackupS3Update)
	require.False(t, ok)
	require.Zero(t, consumer.calls)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN")
}

func TestStepUpMissingTokenCannotReachProtectedOperation(t *testing.T) {
	c, recorder := newStepUpContext(t)
	consumer := &stepUpConsumerStub{err: service.ErrStepUpRequired}
	ok := enforceStepUp(c, consumer, stepUpUserReaderStub{user: &service.User{ID: 7, Role: service.RoleAdmin, TotpEnabled: true}}, service.StepUpScopeBackupS3Update)
	require.False(t, ok)
	require.Equal(t, 1, consumer.calls)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "STEP_UP_REQUIRED")
}

type stepUpUserReaderStub struct {
	user *service.User
	err  error
}

func (s stepUpUserReaderStub) GetByID(context.Context, int64) (*service.User, error) {
	return s.user, s.err
}

func newStepUpContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/backups/1", nil)
	c.Request.RemoteAddr = "1.2.3.4:1234"
	c.Request.Header.Set("User-Agent", "Browser/1")
	c.Set(string(ContextKeyUser), AuthSubject{UserID: 7})
	c.Set(string(ContextKeyUserRole), service.RoleAdmin)
	c.Set(ContextKeySessionID, "session-1")
	c.Set("auth_method", "jwt")
	return c, recorder
}

func TestStepUpFailsClosedWhenTotpNotEnabled(t *testing.T) {
	c, recorder := newStepUpContext(t)
	ok := enforceStepUp(c, &stepUpConsumerStub{}, stepUpUserReaderStub{user: &service.User{ID: 7, Role: service.RoleAdmin}}, service.StepUpScopeBackupDelete)
	require.False(t, ok)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "STEP_UP_TOTP_NOT_ENABLED")
}

func TestStepUpBindsScopeSessionAndFingerprint(t *testing.T) {
	c, _ := newStepUpContext(t)
	c.Request.Header.Set(StepUpTokenHeader, "su_once")
	consumer := &stepUpConsumerStub{}
	ok := enforceStepUp(c, consumer, stepUpUserReaderStub{user: &service.User{ID: 7, Role: service.RoleAdmin, TotpEnabled: true}}, service.StepUpScopeBackupDelete)
	require.True(t, ok)
	require.Equal(t, "su_once", consumer.token)
	require.Equal(t, "session-1", consumer.expected.SessionID)
	require.Equal(t, service.StepUpScopeBackupDelete, consumer.expected.ActionScope)
	require.NotEmpty(t, consumer.expected.BindingHash)
	require.Equal(t, service.StepUpSourceHash("1.2.3.4"), consumer.expected.SourceHash)
}
