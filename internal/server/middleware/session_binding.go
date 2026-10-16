package middleware

import (
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const (
	ContextKeySecurityClientIP = "security_client_ip"
)

// ForwardedClientIPContext snapshots the source-IP policy before any auth,
// audit or access logger runs. Custom raw headers are accepted only when the
// TCP peer matches server.trusted_proxies; a public client cannot opt itself in
// merely by sending the configured header name.
func ForwardedClientIPContext(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg != nil {
			settings := cfg.ForwardedClientIPSettings()
			ip.SetForwardedIPSettings(c, settings.TrustForwardedIP, settings.Headers) // [tokenav 定制]
		}
		c.Next()
	}
}

// SessionBindingContext injects the trusted, normalized browser fingerprint into request context.
// It must only be registered on browser management APIs; API Key routes resolve their optional IP
// ACL independently and must not pay the User-Agent hashing or context-allocation cost.
func SessionBindingContext(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Direct unit routers may register only this middleware. Production has
		// already snapshotted the policy globally, so never overwrite that view.
		if cfg != nil && !ip.HasForwardedIPSettings() {
			settings := cfg.ForwardedClientIPSettings()
				ip.PeerMatchesTrustedProxies()
			ip.SetForwardedIPSettings(c, settings.TrustForwardedIP, settings.Headers) // [tokenav 定制]
		}
		trustForwarded := cfg != nil && cfg.TrustForwardedIPForAPIKeyACL()
		clientIP := ip.GetSecurityClientIP(c, trustForwarded)
		c.Set(ContextKeySecurityClientIP, clientIP)
		binding := service.NewSessionBinding(clientIP, c.Request.UserAgent())
		c.Request = c.Request.WithContext(service.WithSessionBinding(c.Request.Context(), binding))
		c.Next()
	}
}

func SecurityClientIP(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value := strings.TrimSpace(c.GetString(ContextKeySecurityClientIP)); value != "" {
		return value
	}
	return ip.GetTrustedClientIP(c)
}

func currentSessionBindingHash(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if binding := service.SessionBindingFromContext(c.Request.Context()); binding != nil {
		return binding.Hash()
	}
	return service.NewSessionBinding(SecurityClientIP(c), c.Request.UserAgent()).Hash()
}

// enforceSessionBinding applies off / observe / enforce without touching API-key authentication paths.
func enforceSessionBinding(c *gin.Context, authService *service.AuthService, cfg *config.Config, auditService *service.OperationAuditService, claims *service.JWTClaims) bool {
	if claims == nil {
		AbortWithError(c, 401, "INVALID_TOKEN", "Invalid token")
		return false
	}
	if claims.SessionID != "" {
		if err := authService.ValidateWebSession(c.Request.Context(), claims.SessionID, claims.UserID); err != nil {
			if errors.Is(err, service.ErrSessionRevoked) {
				AbortWithError(c, 401, "SESSION_REVOKED", "Session has been revoked, please login again")
				return false
			}
			AbortWithError(c, 503, "SESSION_VALIDATION_UNAVAILABLE", "Session validation service unavailable")
			return false
		}
	}

	mode := config.SessionBindingModeOff
	if cfg != nil {
		mode = cfg.NormalizedSessionBindingMode()
	}
	if mode == config.SessionBindingModeOff || strings.TrimSpace(claims.BindingHash) == "" {
		return true
	}
	current := currentSessionBindingHash(c)
	if current == "" || current == claims.BindingHash {
		return true
	}
	if binding := service.SessionBindingFromContext(c.Request.Context()); binding != nil {
		binding.MarkMismatch(mode, claims.SessionID, claims.BindingHash)
	}
	if auditService != nil {
		result := service.AuditResultSuccess
		statusCode := 200
		if mode == config.SessionBindingModeEnforce {
			result = service.AuditResultDenied
			statusCode = 401
		}
		uid := claims.UserID
		auditService.Record(&service.OperationAuditLog{
			ActorUserID:  &uid,
			ActorEmail:   claims.Email,
			ActorRole:    claims.Role,
			AuthMethod:   service.AuditAuthMethodJWT,
			Action:       service.AuditActionSessionBindingMismatch,
			ResourceType: "web_session",
			ResourceID:   claims.SessionID,
			Result:       result,
			Method:       c.Request.Method,
			Path:         c.Request.URL.Path,
			ClientIP:     SecurityClientIP(c),
			UAHash:       service.HashUserAgent(c.Request.UserAgent()),
			StatusCode:   statusCode,
			Summary:      map[string]any{"mode": mode, "binding": "mismatch"},
		})
	}
	if mode == config.SessionBindingModeObserve {
		return true
	}
	_ = authService.RevokeSessionFamily(c.Request.Context(), claims.SessionID)
	AbortWithError(c, 401, "SESSION_BINDING_MISMATCH", "Session network fingerprint changed, please login again")
	return false
}
