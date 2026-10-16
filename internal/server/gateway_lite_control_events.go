package server

import (
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"

	"github.com/gin-gonic/gin"
)

const gatewayLiteControlEventBodyLimit = 1 << 20

func registerGatewayLiteControlPlaneRoutes(r *gin.Engine, cfg *config.Config) {
	if r == nil {
		return
	}
	r.POST("/internal/control-plane/events", gatewayLiteControlEventHandler(cfg))
}

func gatewayLiteControlEventHandler(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		expectedToken := gatewayLiteControlPlaneInboundToken(cfg)
		if expectedToken == "" {
			c.JSON(http.StatusServiceUnavailable, gatewaylite.ControlPlaneEventPushResponse{OK: false, Error: "control_plane_token_not_configured"})
			return
		}
		if !gatewayLiteControlPlaneTokenMatches(c.GetHeader("Authorization"), expectedToken) {
			c.JSON(http.StatusUnauthorized, gatewaylite.ControlPlaneEventPushResponse{OK: false, Error: "invalid_control_plane_token"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, gatewayLiteControlEventBodyLimit)
		var req gatewaylite.ControlPlaneEventPushRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gatewaylite.ControlPlaneEventPushResponse{OK: false, Error: "invalid_request_body"})
			return
		}
		if req.Event.ID <= 0 || strings.TrimSpace(req.Event.Scope) == "" {
			c.JSON(http.StatusBadRequest, gatewaylite.ControlPlaneEventPushResponse{OK: false, DeliveryID: req.DeliveryID, EventID: req.Event.ID, Error: "invalid_event"})
			return
		}
		syncer := gatewaylite.DefaultCacheInvalidationSyncer()
		if syncer == nil {
			c.JSON(http.StatusServiceUnavailable, gatewaylite.ControlPlaneEventPushResponse{OK: false, DeliveryID: req.DeliveryID, EventID: req.Event.ID, Error: "cache_invalidation_syncer_not_ready"})
			return
		}
		alreadyProcessed, err := syncer.ApplyPushedEvent(c.Request.Context(), req.Event)
		if errors.Is(err, gatewaylite.ErrControlEventGatewayMismatch) {
			c.JSON(http.StatusConflict, gatewaylite.ControlPlaneEventPushResponse{OK: false, DeliveryID: req.DeliveryID, EventID: req.Event.ID, Error: "gateway_mismatch"})
			return
		}
		if err != nil {
			log.Printf("gateway-lite: control event apply failed id=%d scope=%s reason=%s: %v", req.Event.ID, req.Event.Scope, req.Event.Reason, err)
			c.JSON(http.StatusServiceUnavailable, gatewaylite.ControlPlaneEventPushResponse{OK: false, DeliveryID: req.DeliveryID, EventID: req.Event.ID, Error: "event_apply_failed"})
			return
		}
		c.JSON(http.StatusOK, gatewaylite.ControlPlaneEventPushResponse{
			OK:               true,
			Acknowledged:     true,
			AlreadyProcessed: alreadyProcessed,
			DeliveryID:       req.DeliveryID,
			EventID:          req.Event.ID,
		})
	}
}

func gatewayLiteControlPlaneInboundToken(cfg *config.Config) string {
	if value := strings.TrimSpace(os.Getenv("GATEWAY_LITE_CONTROL_PLANE_TOKEN")); value != "" {
		return value
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.GatewayLite.ControlPlaneToken)
}

func gatewayLiteControlPlaneTokenMatches(authorization, expected string) bool {
	provided := strings.TrimSpace(authorization)
	if strings.HasPrefix(strings.ToLower(provided), "bearer ") {
		provided = strings.TrimSpace(provided[7:])
	}
	expected = strings.TrimSpace(expected)
	return provided != "" && expected != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
