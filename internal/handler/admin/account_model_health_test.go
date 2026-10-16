package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelHealthAdminService struct {
	*stubAdminService
	account service.Account
}

func (s *modelHealthAdminService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	if s.account.ID == id {
		account := s.account
		return &account, nil
	}
	return s.stubAdminService.GetAccount(context.Background(), id)
}

func TestAccountHandlerGetModelHealthSeparatesDeclarationsFromObservations(t *testing.T) {
	groupID := int64(17)
	adminService := &modelHealthAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{
			ID:          42,
			Platform:    service.PlatformOpenAI,
			Status:      service.StatusActive,
			Schedulable: true,
			Credentials: map[string]any{
				"model_mapping": map[string]any{"sol": "luna"},
			},
			AccountGroups: []service.AccountGroup{{
				GroupID:      groupID,
				ModelMapping: map[string]string{"sol": "terra"},
			}},
		},
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetOpenAIGatewayService(&service.OpenAIGatewayService{})
	router.GET("/api/v1/admin/accounts/:id/model-health", handler.GetModelHealth)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/42/model-health", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var envelope struct {
		Data struct {
			AccountID         int64 `json:"account_id"`
			RuntimeAvailable  bool  `json:"runtime_available"`
			ObserverAvailable bool  `json:"runtime_observer_available"`
			Declarations      []struct {
				Requested string `json:"requested_model"`
				Upstream  string `json:"upstream_model"`
				Scope     string `json:"scope"`
				GroupID   *int64 `json:"group_id"`
			} `json:"declarations"`
			Observed []service.OpenAIAccountModelHealth `json:"observed_models"`
			Scope    string                             `json:"health_scope"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &envelope))
	require.Equal(t, int64(42), envelope.Data.AccountID)
	require.True(t, envelope.Data.RuntimeAvailable)
	require.True(t, envelope.Data.ObserverAvailable)
	require.Equal(t, "process_local", envelope.Data.Scope)
	require.Empty(t, envelope.Data.Observed)
	require.Len(t, envelope.Data.Declarations, 2)
	require.Equal(t, "account", envelope.Data.Declarations[0].Scope)
	require.Equal(t, "group", envelope.Data.Declarations[1].Scope)
	require.NotNil(t, envelope.Data.Declarations[1].GroupID)
	require.Equal(t, groupID, *envelope.Data.Declarations[1].GroupID)
}

func TestAccountHandlerGetModelHealthRejectsNonOpenAI(t *testing.T) {
	adminService := &modelHealthAdminService{
		stubAdminService: newStubAdminService(),
		account:          service.Account{ID: 9, Platform: service.PlatformGrok},
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(adminService, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts/:id/model-health", handler.GetModelHealth)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/9/model-health", nil))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
