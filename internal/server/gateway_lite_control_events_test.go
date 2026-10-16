package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteControlEventHandlerRequiresSharedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/internal/control-plane/events", gatewayLiteControlEventHandler(&config.Config{
		GatewayLite: config.GatewayLiteConfig{ControlPlaneToken: "shared-token"},
	}))
	body, err := json.Marshal(gatewaylite.ControlPlaneEventPushRequest{
		DeliveryID: 1,
		Event:      gatewaylite.CacheInvalidationEvent{ID: 10, Scope: "key:snapshot"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/internal/control-plane/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGatewayLiteControlEventHandlerAcknowledgesAndDeduplicates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()
	configCache := gatewaylite.NewRedisConfigCache(redisClient, "push-handler")
	keyCache := gatewaylite.NewRedisKeyCache(redisClient, "push-handler")
	require.NoError(t, keyCache.Set(ctx, gatewaylite.KeySnapshot{KeyID: "key-1", CacheTTLSecond: 60}, "sg-1"))
	syncer := gatewaylite.NewCacheInvalidationSyncer(nil, configCache, keyCache, nil, "sg", "sg-1", time.Hour)
	gatewaylite.SetDefaultCacheInvalidationSyncer(syncer)

	r := gin.New()
	r.POST("/internal/control-plane/events", gatewayLiteControlEventHandler(&config.Config{
		GatewayLite: config.GatewayLiteConfig{ControlPlaneToken: "shared-token"},
	}))
	payload := gatewaylite.ControlPlaneEventPushRequest{
		DeliveryID: 22,
		Event: gatewaylite.CacheInvalidationEvent{
			ID:          11,
			Scope:       "key:snapshot",
			GatewayCode: "sg-1",
			Payload:     map[string]any{"key_id": "key-1"},
		},
	}
	first := postGatewayLiteControlEvent(t, r, payload, "shared-token")
	require.Equal(t, http.StatusOK, first.Code)
	var firstResponse gatewaylite.ControlPlaneEventPushResponse
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &firstResponse))
	require.True(t, firstResponse.OK)
	require.True(t, firstResponse.Acknowledged)
	require.False(t, firstResponse.AlreadyProcessed)

	second := postGatewayLiteControlEvent(t, r, payload, "shared-token")
	require.Equal(t, http.StatusOK, second.Code)
	var secondResponse gatewaylite.ControlPlaneEventPushResponse
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &secondResponse))
	require.True(t, secondResponse.AlreadyProcessed)
}

func postGatewayLiteControlEvent(t *testing.T, handler http.Handler, payload gatewaylite.ControlPlaneEventPushRequest, token string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/internal/control-plane/events", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
