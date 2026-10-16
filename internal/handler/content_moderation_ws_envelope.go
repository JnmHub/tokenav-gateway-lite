package handler

import (
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Only response.create handlers choose this entry; an HTTP body or Upgrade header
// cannot grant the envelope exception. The service revalidates the complete JSON
// before excluding transport metadata from its ordinary field allowlist.
func (h *OpenAIGatewayHandler) checkResponsesWSContentModeration(c *gin.Context, reqLog *zap.Logger, apiKey *service.APIKey, subject middleware2.AuthSubject, model string, body []byte) *service.ContentModerationDecision {
	if h == nil || h.contentModerationService == nil {
		return nil
	}
	return runContentModerationWithEnvelope(c, reqLog, h.contentModerationService, h.gatewayService, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, model, body, true)
}
