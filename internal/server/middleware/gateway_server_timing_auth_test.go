package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGatewayLiteServerTimingIncludesAuthQuotaAndSettlementStages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "secret-value"
	hash := sha256.Sum256([]byte(secret))
	rawKey := "aiceo_sk_timing_" + secret
	stub := gatewayLiteControlStub{
		key: gatewaylite.KeySnapshot{
			KeyID: "timing", UserID: 42, SecretHash: hex.EncodeToString(hash[:]),
			Status: service.StatusActive, Platform: service.PlatformOpenAI, GroupID: 7,
			AllowedModels: []string{"*"},
			Group: &gatewaylite.GatewayGroupSnapshot{
				ID: 7, LocalGroupID: 7, Name: "openai", Platform: service.PlatformOpenAI,
				Status: service.StatusActive, RateMultiplier: 1,
			},
		},
		lease: gatewaylite.LeaseSnapshot{
			LeaseID: "lease_timing", UserID: 42, Region: "sg",
			AllocatedCents: 100, ExpiresAt: time.Now().Add(time.Minute).Unix(),
		},
	}
	localGroups := gatewayLiteLocalGroupReaderStub{group: &service.Group{
		ID: 7, Name: "openai", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, RateMultiplier: 1, Hydrated: true,
	}}
	runtimeRef := gatewaylite.NewRuntimeConfigRef(gatewaylite.RuntimeConfig{Region: "sg", GatewayCode: "sg-1"})

	router := gin.New()
	router.Use(ClientRequestID())
	router.Use(GatewayLiteServerTiming(servertiming.NewController(10_000)))
	router.POST("/v1/responses", gin.HandlerFunc(NewGatewayLiteAPIKeyAuthMiddlewareWithRuntimeConfigAndLocalGroups(
		stub, runtimeRef, nil, nil, stub, localGroups,
	)), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
	request.Header.Set("Authorization", "Bearer "+rawKey)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	header := recorder.Result().Header.Get(servertiming.HeaderName)
	require.Contains(t, header, "auth;dur=")
	require.Contains(t, header, "quota;dur=")
	require.Contains(t, recorder.Result().Trailer.Get(servertiming.FinalHeaderName), "settle;dur=")
}
