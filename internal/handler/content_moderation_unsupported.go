package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// AIUnsupportedGate is the route-facing accessor for the authenticated gate.
func (h *OpenAIGatewayHandler) AIUnsupportedGate(protocol string) gin.HandlerFunc {
	return ContentModerationAIUnsupportedGate(h.contentModerationService, protocol)
}

// ContentModerationAIUnsupportedGate belongs after Key authentication/admission
// and before business handlers whose protocols have no fixed input extractor.
// This boundary does not reject unknown media inside an integrated protocol;
// those fields are recorded as uncovered by the ordinary text/image extractor.
func ContentModerationAIUnsupportedGate(svc *service.ContentModerationService, protocol string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Optional route fixtures and installations without the review service
		// keep their existing behavior; an active injected service owns admission.
		if svc == nil {
			c.Next()
			return
		}
		key, keyOK := middleware.GetAPIKeyFromContext(c)
		subject, subjectOK := middleware.GetAuthSubjectFromContext(c)
		if !keyOK || !subjectOK {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": gin.H{"type": "authentication_error", "code": "missing_admission", "message": "请求尚未完成身份验证"}})
			return
		}
		input := buildContentModerationInput(c, key, subject, protocol, "", nil)
		input.AdmissionID, input.Turn = nextContentModerationAdmissionTurn(c)
		input.RequestID = service.ContentModerationAdmissionTurnID(input.AdmissionID, input.Turn)
		decision := svc.CheckAIUnsupported(c.Request.Context(), input)
		if decision == nil {
			c.Next()
			return
		}
		// This is a pre-dispatch rejection. A previous accepted/uncertain task
		// keeps its reservation until the existing settlement path resolves it.
		meta, _ := gatewaylite.RequestMetaFromContext(c.Request.Context())
		if !meta.UpstreamConsumed && !meta.AsyncPersistenceUncertain {
			gatewaylite.MarkRequestReservationForRefund(c.Request.Context())
		}
		c.AbortWithStatusJSON(contentModerationStatus(decision), gin.H{"error": gin.H{
			"type": "content_moderation_error", "code": decision.ErrorCode, "message": decision.Message, "retryable": false,
		}, "request_id": contentModerationRequestID(c.Request.Context()), "admission_id": input.AdmissionID,
			"turn_id": input.RequestID, "turn": input.Turn, "verdict": decision.Verdict,
			"allowed": false, "blocked": true, "flagged": false, "shadow": decision.Shadow,
			"reason": decision.Reason, "categories": decision.Categories, "phase": decision.Phase,
			"provider_called": decision.ProviderCalled, "provider_latency_ms": decision.ProviderLatencyMS,
			"coverage": decision.Coverage, "penalty_state": "none"})
	}
}
