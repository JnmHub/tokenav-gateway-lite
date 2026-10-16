//go:build unit

package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type grokOAuthReconcileHandlerStub struct {
	input  service.GrokOAuthReconcileInput
	result *service.GrokOAuthReconcileResult
	err    error
}

func (s *grokOAuthReconcileHandlerStub) ReconcileGrokOAuth(_ context.Context, input service.GrokOAuthReconcileInput) (*service.GrokOAuthReconcileResult, error) {
	s.input = input
	if s.result != nil || s.err != nil {
		return s.result, s.err
	}
	return &service.GrokOAuthReconcileResult{DryRun: !input.Apply, Items: []service.GrokOAuthReconcileItem{}}, nil
}

func runGrokOAuthReconcileHandler(t *testing.T, body string, stub *grokOAuthReconcileHandlerStub, audit *service.OperationAuditService) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewGrokOAuthHandler(nil, nil, nil)
	handler.SetReconciler(stub)
	handler.SetOperationAuditService(audit)
	router := gin.New()
	router.POST("/api/v1/admin/grok/oauth/reconcile", handler.ReconcileOAuthAccounts)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/grok/oauth/reconcile", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestGrokOAuthReconcileHandlerDefaultsToDryRunAndAudits(t *testing.T) {
	stub := &grokOAuthReconcileHandlerStub{result: &service.GrokOAuthReconcileResult{
		DryRun:  true,
		Scanned: 2,
		Items: []service.GrokOAuthReconcileItem{{
			AccountID: 7,
			Reason:    service.GrokOAuthReconcileReasonMissingRefreshToken,
			Action:    service.GrokOAuthReconcileActionBlock,
			Outcome:   service.GrokOAuthReconcileOutcomePlanned,
		}},
	}}
	audit := service.NewOperationAuditService(nil, nil)
	recorder := runGrokOAuthReconcileHandler(t, `{}`, stub, audit)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, stub.input.DryRun)
	require.False(t, stub.input.Apply)
	require.Equal(t, 1, audit.Stats().Queued)
	require.NotContains(t, recorder.Body.String(), `"access_token":`)
	require.NotContains(t, recorder.Body.String(), `"refresh_token":`)
}

func TestGrokOAuthReconcileHandlerRequiresExplicitApplyPair(t *testing.T) {
	stub := &grokOAuthReconcileHandlerStub{}
	invalid := runGrokOAuthReconcileHandler(t, `{"apply":true}`, stub, nil)
	require.Equal(t, http.StatusBadRequest, invalid.Code)

	valid := runGrokOAuthReconcileHandler(t, `{"apply":true,"dry_run":false,"after_id":9,"limit":50,"preview_token":"preview-1","account_ids":[11,12]}`, stub, nil)
	require.Equal(t, http.StatusOK, valid.Code)
	require.True(t, stub.input.Apply)
	require.False(t, stub.input.DryRun)
	require.Equal(t, int64(9), stub.input.AfterID)
	require.Equal(t, 50, stub.input.Limit)
	require.Equal(t, "preview-1", stub.input.PreviewToken)
	require.Equal(t, []int64{11, 12}, stub.input.AccountIDs)
}
