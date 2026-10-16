package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestShouldSkipGatewayLiteBillingEligibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	h := &OpenAIGatewayHandler{cfg: &config.Config{RunMode: config.RunModeGatewayLite}}
	require.False(t, h.shouldSkipGatewayLiteBillingEligibility(c))

	c.Set(string(middleware2.ContextKeyGatewayLiteLease), gatewaylite.LeaseSnapshot{
		LeaseID:   "lease1",
		UserID:    42,
		Region:    "local",
		ExpiresAt: 4102444800,
	})
	require.False(t, h.shouldSkipGatewayLiteBillingEligibility(c))

	h.cfg.RunMode = config.RunModeStandard
	require.False(t, h.shouldSkipGatewayLiteBillingEligibility(c))
}

func TestShouldSkipGatewayLiteBillingEligibilityRejectsExpiredLease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(middleware2.ContextKeyGatewayLiteLease), gatewaylite.LeaseSnapshot{
		LeaseID:   "lease1",
		UserID:    42,
		Region:    "local",
		ExpiresAt: 1,
	})

	h := &GatewayHandler{cfg: &config.Config{RunMode: config.RunModeGatewayLite}}
	require.False(t, h.shouldSkipGatewayLiteBillingEligibility(c))
}
