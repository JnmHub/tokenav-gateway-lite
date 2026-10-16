package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayLiteLocalKeyResolverStub struct {
	key        *service.APIKey
	err        error
	getCalls   atomic.Int32
	touchCalls atomic.Int32
}

func (s *gatewayLiteLocalKeyResolverStub) GetByKey(_ context.Context, _ string) (*service.APIKey, error) {
	s.getCalls.Add(1)
	return s.key, s.err
}

func (s *gatewayLiteLocalKeyResolverStub) TouchLastUsed(_ context.Context, _ int64) error {
	s.touchCalls.Add(1)
	return nil
}

func TestGatewayLiteHybridAPIKeyAuth_LocalKeyNeverDelegatesToControlPlanePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	localResolver := &gatewayLiteLocalKeyResolverStub{key: &service.APIKey{
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
			ID:        groupID,
			Name:      "local-openai",
			Platform:  service.PlatformOpenAI,
			Status:    service.StatusActive,
			LocalOnly: true,
		},
	}}
	var remoteCalls atomic.Int32
	remote := APIKeyAuthMiddleware(func(c *gin.Context) {
		remoteCalls.Add(1)
		c.AbortWithStatus(http.StatusTeapot)
	})

	router := gin.New()
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteHybridAPIKeyAuthMiddleware(localResolver, remote, &config.Config{})), func(c *gin.Context) {
		apiKey, ok := GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.Equal(t, int64(101), apiKey.ID)
		require.True(t, service.IsGatewayLiteLocalAPIKeyContext(c.Request.Context()))
		ingressGroupID, ok := gatewaylite.IngressGroupIDFromContext(c.Request.Context())
		require.True(t, ok)
		require.Equal(t, int64(7), ingressGroupID)
		_, hasRequestMeta := gatewaylite.RequestMetaFromContext(c.Request.Context())
		require.False(t, hasRequestMeta, "local keys must not acquire remote billing metadata")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+service.GatewayLiteLocalAPIKeyPrefix+"local-secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int32(1), localResolver.getCalls.Load())
	require.Equal(t, int32(1), localResolver.touchCalls.Load())
	require.Equal(t, int32(0), remoteCalls.Load(), "glk_ requests must never reach control-plane authentication")
}

func TestGatewayLiteHybridAPIKeyAuth_LocalKeyKeepsClientRequestIDForMediaCorrelation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	localResolver := &gatewayLiteLocalKeyResolverStub{key: &service.APIKey{
		ID:      102,
		Key:     service.GatewayLiteLocalAPIKeyPrefix + "media-secret",
		Status:  service.StatusAPIKeyActive,
		GroupID: &groupID,
		User: &service.User{
			ID:     2,
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
	remote := APIKeyAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(http.StatusTeapot) })

	router := gin.New()
	router.Use(ClientRequestID())
	router.GET("/v1/images/generations",
		gin.HandlerFunc(NewGatewayLiteHybridAPIKeyAuthMiddleware(localResolver, remote, &config.Config{})),
		func(c *gin.Context) {
			requestID, ok := gatewaylite.CanonicalRequestIDFromContext(c.Request.Context())
			require.True(t, ok)
			require.NotEmpty(t, requestID)
			_, hasRequestMeta := gatewaylite.RequestMetaFromContext(c.Request.Context())
			require.False(t, hasRequestMeta)
			c.Status(http.StatusNoContent)
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/images/generations", nil)
	req.Header.Set("Authorization", "Bearer "+service.GatewayLiteLocalAPIKeyPrefix+"media-secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestGatewayLiteHybridAPIKeyAuth_MainSiteKeyKeepsRemotePath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	localResolver := &gatewayLiteLocalKeyResolverStub{}
	var remoteCalls atomic.Int32
	remote := APIKeyAuthMiddleware(func(c *gin.Context) {
		remoteCalls.Add(1)
		c.Status(http.StatusNoContent)
	})

	router := gin.New()
	router.GET("/v1/models", gin.HandlerFunc(NewGatewayLiteHybridAPIKeyAuthMiddleware(localResolver, remote, &config.Config{})))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer aiceo_sk_main_site_key")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, int32(0), localResolver.getCalls.Load())
	require.Equal(t, int32(1), remoteCalls.Load())
}

func TestGatewayLiteHybridAPIKeyAuth_BillingProbeDoesNotTouchLocalKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	localResolver := &gatewayLiteLocalKeyResolverStub{key: &service.APIKey{
		ID: 101, Key: service.GatewayLiteLocalAPIKeyPrefix + "local-secret", Status: service.StatusAPIKeyActive,
		GroupID: &groupID,
		User:    &service.User{ID: 1, Role: service.RoleAdmin, Status: service.StatusActive},
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
	}}
	remote := APIKeyAuthMiddleware(func(c *gin.Context) { c.AbortWithStatus(http.StatusTeapot) })

	router := gin.New()
	router.GET("/v1/sub2api/billing", gin.HandlerFunc(NewGatewayLiteHybridAPIKeyAuthMiddleware(localResolver, remote, &config.Config{})), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil)
	req.Header.Set("Authorization", "Bearer "+service.GatewayLiteLocalAPIKeyPrefix+"local-secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, int32(0), localResolver.touchCalls.Load())
}
