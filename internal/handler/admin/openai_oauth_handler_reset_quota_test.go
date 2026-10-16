//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIQuotaWorkflowStub struct {
	resetResult *service.OpenAIQuotaResetResult
	queryResult *service.OpenAIQuotaUsage
	cacheErr    error

	queryContextError error
	cacheContextError error
}

func (stub *openAIQuotaWorkflowStub) ResetCredit(context.Context, int64) (*service.OpenAIQuotaResetResult, error) {
	return stub.resetResult, nil
}

func (stub *openAIQuotaWorkflowStub) QueryUsage(ctx context.Context, _ int64) (*service.OpenAIQuotaUsage, error) {
	stub.queryContextError = ctx.Err()
	return stub.queryResult, nil
}

func (stub *openAIQuotaWorkflowStub) CacheResetCreditsSnapshot(ctx context.Context, _ int64, _ *service.OpenAIRateLimitResetCredits) error {
	stub.cacheContextError = ctx.Err()
	return stub.cacheErr
}

type openAIAccountStateRecovererStub struct {
	err             error
	calls           int
	contextError    error
	hasDeadline     bool
	invalidateToken bool
}

func (stub *openAIAccountStateRecovererStub) RecoverAccountState(ctx context.Context, _ int64, options service.AccountRecoveryOptions) (*service.SuccessfulTestRecoveryResult, error) {
	stub.calls++
	stub.contextError = ctx.Err()
	_, stub.hasDeadline = ctx.Deadline()
	stub.invalidateToken = options.InvalidateToken
	return &service.SuccessfulTestRecoveryResult{}, stub.err
}

type openAIResetAdminServiceStub struct {
	service.AdminService
	account *service.Account
}

func (stub *openAIResetAdminServiceStub) GetAccount(context.Context, int64) (*service.Account, error) {
	return stub.account, nil
}

type openAIQuotaResetEnvelope struct {
	Data openAIQuotaResetResponse `json:"data"`
}

func successfulOpenAIQuotaWorkflow() *openAIQuotaWorkflowStub {
	return &openAIQuotaWorkflowStub{
		resetResult: &service.OpenAIQuotaResetResult{Code: "success", WindowsReset: 1},
		queryResult: &service.OpenAIQuotaUsage{
			FetchedAt:             time.Now().Unix(),
			RateLimitResetCredits: &service.OpenAIRateLimitResetCredits{AvailableCount: 0},
		},
	}
}

func performOpenAIQuotaReset(t *testing.T, handler *OpenAIOAuthHandler, ctx context.Context) (int, openAIQuotaResetEnvelope) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/v1/admin/openai/accounts/:id/reset-quota", handler.ResetQuota)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/openai/accounts/42/reset-quota", nil)
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	var envelope openAIQuotaResetEnvelope
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	return recorder.Code, envelope
}

func TestOpenAIResetQuotaPostProcessingSurvivesClientCancellation(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflow()
	recoverer := &openAIAccountStateRecovererStub{}
	handler := &OpenAIOAuthHandler{
		adminService: &openAIResetAdminServiceStub{account: &service.Account{
			ID: 42, Name: "recovered", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		}},
		quotaService:     quota,
		rateLimitService: recoverer,
	}
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()

	status, envelope := performOpenAIQuotaReset(t, handler, requestContext)

	require.Equal(t, http.StatusOK, status)
	require.True(t, envelope.Data.AccountStateRecovered)
	require.True(t, envelope.Data.CacheRefreshed)
	require.Equal(t, 1, recoverer.calls)
	require.NoError(t, recoverer.contextError)
	require.True(t, recoverer.hasDeadline)
	require.True(t, recoverer.invalidateToken)
	require.NoError(t, quota.queryContextError)
	require.NoError(t, quota.cacheContextError)
}

func TestOpenAIResetQuotaRecoveryFailureReturnsPartialSuccessWarning(t *testing.T) {
	quota := successfulOpenAIQuotaWorkflow()
	recoverer := &openAIAccountStateRecovererStub{err: errors.New("database unavailable")}
	handler := &OpenAIOAuthHandler{
		adminService:     &openAIResetAdminServiceStub{},
		quotaService:     quota,
		rateLimitService: recoverer,
	}

	status, envelope := performOpenAIQuotaReset(t, handler, context.Background())

	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "success", envelope.Data.Code)
	require.Equal(t, openAIQuotaResetWarningAccountRecoveryFailed, envelope.Data.WarningCode)
	require.False(t, envelope.Data.AccountStateRecovered)
}
