package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newSparkShadowHandlerRouter(stub *stubAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewOpenAIOAuthHandler(nil, stub, nil, nil)
	router := gin.New()
	router.POST("/accounts/:id/shadow", h.CreateSparkShadow)
	router.GET("/accounts/:id/shadows", h.ListSparkShadows)
	router.POST("/accounts/:id/shadow/sync", h.SyncSparkShadow)
	return router
}

func TestCreateSparkShadowReturnsRelationshipWithoutCredentialSecrets(t *testing.T) {
	stub := &stubAdminService{}
	router := newSparkShadowHandlerRouter(stub)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/accounts/42/shadow", strings.NewReader(`{
		"name":"us-spark","priority":80,"concurrency":3,"group_ids":[7]
	}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, float64(42), payload.Data["parent_account_id"])
	require.Equal(t, service.QuotaDimensionSpark, payload.Data["quota_dimension"])
	credentials, ok := payload.Data["credentials"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, credentials, "access_token")
	require.NotContains(t, credentials, "refresh_token")
}

func TestListAndSyncSparkShadow(t *testing.T) {
	parentID := int64(42)
	shadow := &service.Account{
		ID: 99, Name: "us-spark", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		ParentAccountID: &parentID, QuotaDimension: service.QuotaDimensionSpark,
	}
	stub := &stubAdminService{sparkShadows: []*service.Account{shadow}}
	router := newSparkShadowHandlerRouter(stub)

	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, httptest.NewRequest(http.MethodGet, "/accounts/42/shadows", nil))
	require.Equal(t, http.StatusOK, listRecorder.Code)
	require.Contains(t, listRecorder.Body.String(), `"quota_dimension":"spark"`)

	syncRecorder := httptest.NewRecorder()
	router.ServeHTTP(syncRecorder, httptest.NewRequest(http.MethodPost, "/accounts/99/shadow/sync", nil))
	require.Equal(t, http.StatusOK, syncRecorder.Code)
	require.Contains(t, syncRecorder.Body.String(), `"parent_account_id":42`)
}

func TestSparkShadowHandlersRejectInvalidID(t *testing.T) {
	router := newSparkShadowHandlerRouter(&stubAdminService{})
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/accounts/not-number/shadow"},
		{http.MethodGet, "/accounts/not-number/shadows"},
		{http.MethodPost, "/accounts/not-number/shadow/sync"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

func TestRefreshSingleAccountRejectsSparkShadowBeforeCallingOAuth(t *testing.T) {
	parentID := int64(42)
	handler := &AccountHandler{}
	_, _, err := handler.refreshSingleAccount(context.Background(), &service.Account{
		ID: 99, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		ParentAccountID: &parentID, QuotaDimension: service.QuotaDimensionSpark,
	})
	require.ErrorContains(t, err, "managed by its parent")
}
