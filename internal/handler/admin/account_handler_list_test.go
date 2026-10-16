package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func setupAccountListRouter() (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts", handler.List)
	return router, adminSvc
}

func TestAccountHandlerListIncludesCreatedAt(t *testing.T) {
	router, adminSvc := setupAccountListRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&sort_by=created_at&sort_order=desc", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", adminSvc.lastListAccounts.sortBy)

	var payload struct {
		Data struct {
			Items []struct {
				ID        int64  `json:"id"`
				CreatedAt string `json:"created_at"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)

	createdAt := payload.Data.Items[0].CreatedAt
	require.NotEmpty(t, createdAt)
	require.True(t, strings.HasSuffix(createdAt, "Z"), "created_at should be serialized as UTC")
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	require.NoError(t, err)
	_, offset := parsed.Zone()
	require.Equal(t, 0, offset)
}

func TestAccountHandlerListDefaultsToPriorityAscending(t *testing.T) {
	router, adminSvc := setupAccountListRouter()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "priority", adminSvc.lastListAccounts.sortBy)
	require.Equal(t, "asc", adminSvc.lastListAccounts.sortOrder)
}

func TestAccountHandlerListFullCredentialsRequireGatewayLiteSyncAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.accounts[0].Credentials = map[string]any{
		"api_key":                 "top-level-secret",
		"header_override_enabled": true,
		"header_overrides": map[string]any{
			"api-key": "nested-header-secret",
		},
	}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if authMethod := c.GetHeader("X-Test-Auth-Method"); authMethod != "" {
			c.Set("auth_method", authMethod)
		}
		c.Next()
	})
	router.GET("/api/v1/admin/accounts", handler.List)

	for _, tt := range []struct {
		name       string
		authMethod string
		wantFull   bool
	}{
		{name: "jwt remains redacted", authMethod: "jwt"},
		{name: "admin api key remains redacted", authMethod: "admin_api_key"},
		{name: "gateway sync key may receive full credentials", authMethod: "gateway_lite_admin_key", wantFull: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?lite=true&include_credentials=true", nil)
			request.Header.Set("X-Test-Auth-Method", tt.authMethod)
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			body := recorder.Body.String()
			if tt.wantFull {
				require.Contains(t, body, "top-level-secret")
				require.Contains(t, body, "nested-header-secret")
				return
			}
			require.NotContains(t, body, "top-level-secret")
			require.NotContains(t, body, "nested-header-secret")
			require.Contains(t, body, `"api-key":""`)
			require.Contains(t, body, `"has_header_overrides":true`)
		})
	}
}
