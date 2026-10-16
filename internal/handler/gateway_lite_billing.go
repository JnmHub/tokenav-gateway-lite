package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) shouldSkipGatewayLiteBillingEligibility(c *gin.Context) bool {
	if h == nil {
		return false
	}
	return shouldSkipGatewayLiteBillingEligibility(h.cfg, c)
}

func (h *GatewayHandler) shouldSkipGatewayLiteBillingEligibility(c *gin.Context) bool {
	if h == nil {
		return false
	}
	return shouldSkipGatewayLiteBillingEligibility(h.cfg, c)
}

func shouldSkipGatewayLiteBillingEligibility(cfg *config.Config, c *gin.Context) bool {
	if c != nil && c.Request != nil {
		if _, _, ok := service.ImageTaskExecutionFromContext(c.Request.Context()); ok {
			// The client request already passed eligibility and durably expanded
			// its lease before enqueue. Rechecking from a worker would treat the
			// same accepted job as a second purchase.
			return true
		}
	}
	return false
}
