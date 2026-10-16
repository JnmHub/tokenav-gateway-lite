package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type localKeyRevealAPIKeyRepoStub struct {
	service.APIKeyRepository
	key     string
	ownerID int64
}

func (s *localKeyRevealAPIKeyRepoStub) GetKeyAndOwnerID(_ context.Context, _ int64) (string, int64, error) {
	return s.key, s.ownerID, nil
}

type localKeyRevealUserRepoStub struct {
	service.UserRepository
	admin *service.User
}

func (s *localKeyRevealUserRepoStub) GetByID(_ context.Context, _ int64) (*service.User, error) {
	clone := *s.admin
	return &clone, nil
}

func TestGatewayLiteLocalKeyDTOEmptyIPRulesMarshalAsArrays(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  *service.APIKey
	}{
		{name: "nil key", key: nil},
		{name: "nil rules", key: &service.APIKey{ID: 1, Name: "local"}},
		{name: "empty rules", key: &service.APIKey{ID: 2, Name: "local", IPWhitelist: []string{}, IPBlacklist: []string{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(gatewayLiteLocalKeyDTO(tt.key, false))
			require.NoError(t, err)

			var decoded map[string]any
			require.NoError(t, json.Unmarshal(payload, &decoded))
			require.Equal(t, []any{}, decoded["ip_whitelist"])
			require.Equal(t, []any{}, decoded["ip_blacklist"])
		})
	}
}

func TestGatewayLiteLocalKeyDTOPreservesIPRules(t *testing.T) {
	t.Parallel()

	dto := gatewayLiteLocalKeyDTO(&service.APIKey{
		ID:          3,
		Name:        "local",
		IPWhitelist: []string{"127.0.0.1", "10.0.0.0/8"},
		IPBlacklist: []string{"192.0.2.1"},
	}, false)

	require.Equal(t, []string{"127.0.0.1", "10.0.0.0/8"}, dto.IPWhitelist)
	require.Equal(t, []string{"192.0.2.1"}, dto.IPBlacklist)
}

func TestGatewayLiteLocalKeyRevealRequiresJWTAndDisablesCaching(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		adminID = int64(17)
		secret  = service.GatewayLiteLocalAPIKeyPrefix + "0123456789abcdef"
	)
	svc := service.NewAPIKeyService(
		&localKeyRevealAPIKeyRepoStub{key: secret, ownerID: adminID},
		&localKeyRevealUserRepoStub{admin: &service.User{ID: adminID, Role: service.RoleAdmin, Status: service.StatusActive}},
		nil, nil, nil, nil, &config.Config{},
	)
	handler := NewGatewayLiteLocalKeyHandler(svc)

	t.Run("browser admin can reveal without caching", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/gateway-lite/local-api-keys/9/reveal", nil)
		ctx.Params = gin.Params{{Key: "id", Value: "9"}}
		ctx.Set("auth_method", service.AuditAuthMethodJWT)
		ctx.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: adminID})

		handler.Reveal(ctx)

		require.Equal(t, http.StatusOK, recorder.Code)
		require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
		require.Equal(t, "no-cache", recorder.Header().Get("Pragma"))
		require.Contains(t, recorder.Body.String(), secret)
	})

	t.Run("machine administrator key cannot reveal", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/gateway-lite/local-api-keys/9/reveal", nil)
		ctx.Params = gin.Params{{Key: "id", Value: "9"}}
		ctx.Set("auth_method", service.AuditAuthMethodAdminAPIKey)

		handler.Reveal(ctx)

		require.Equal(t, http.StatusForbidden, recorder.Code)
		require.False(t, strings.Contains(recorder.Body.String(), secret))
	})
}
