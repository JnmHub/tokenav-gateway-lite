package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type localIngressKeyResolverStub struct {
	key *service.APIKey
}

func (s *localIngressKeyResolverStub) GetByKey(context.Context, string) (*service.APIKey, error) {
	return s.key, nil
}

func (s *localIngressKeyResolverStub) TouchLastUsed(context.Context, int64) error {
	return nil
}

func TestGatewayLiteLocalKeyTracksIngressWithoutRemoteBillingMeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(77)
	resolver := &localIngressKeyResolverStub{key: &service.APIKey{
		ID:      101,
		Key:     service.GatewayLiteLocalAPIKeyPrefix + "local-secret",
		Status:  service.StatusAPIKeyActive,
		GroupID: &groupID,
		User: &service.User{
			ID:     1,
			Role:   service.RoleAdmin,
			Status: service.StatusActive,
		},
		Group: &service.Group{
			ID:       groupID,
			Name:     "local-openai",
			Platform: service.PlatformOpenAI,
			Status:   service.StatusActive,
		},
	}}
	remote := middleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		c.AbortWithStatus(http.StatusTeapot)
	})
	cache := &helperConcurrencyCacheStub{userSeq: []bool{true}}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 5*time.Millisecond)

	router := gin.New()
	router.POST(
		"/v1/responses",
		gin.HandlerFunc(middleware.NewGatewayLiteHybridAPIKeyAuthMiddleware(resolver, remote, &config.Config{})),
		func(c *gin.Context) {
			_, hasMeta := gatewaylite.RequestMetaFromContext(c.Request.Context())
			require.False(t, hasMeta)
			streamStarted := false
			release, err := helper.AcquireUserSlotWithWait(c, 1, 0, false, &streamStarted)
			require.NoError(t, err)
			require.NotNil(t, release)
			cache.mu.Lock()
			require.Equal(t, []int64{groupID}, cache.ingressGroups)
			cache.mu.Unlock()
			release()
			c.Status(http.StatusNoContent)
		},
	)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer "+service.GatewayLiteLocalAPIKeyPrefix+"local-secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Len(t, cache.releasedIngressIDs, 1)
}
