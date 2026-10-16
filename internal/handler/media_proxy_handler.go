package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MediaProxyHandler serves short-lived, signed image URLs issued by the
// gateway.  It intentionally does not use API-key authentication: generated
// image URLs are loaded by a browser after the original API request has
// finished, so the bearer token is the only credential available on that
// follow-up request.
type MediaProxyHandler struct {
	proxy *service.MediaProxy
}

// NewMediaProxyHandler creates a media reader for one gateway instance.  A
// nil proxy produces a disabled endpoint rather than a panic, which keeps
// startup safe when a node has no stable JWT secret configured yet.
func NewMediaProxyHandler(proxy *service.MediaProxy) *MediaProxyHandler {
	return &MediaProxyHandler{proxy: proxy}
}

// Serve reads and validates a signed image token, then writes only the
// validated raster bytes.  Detailed upstream failures are deliberately not
// returned to the client because the token itself is a bearer capability and
// error text could expose origin or network information.
func (h *MediaProxyHandler) Serve(c *gin.Context) {
	if h == nil || h.proxy == nil {
		c.Status(http.StatusNotFound)
		return
	}
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		c.Status(http.StatusNotFound)
		return
	}
	result, err := h.proxy.Fetch(c.Request.Context(), token, "")
	if err != nil {
		// Invalid/expired tokens and failed origin reads intentionally share the
		// same response.  This avoids turning the endpoint into a token or
		// upstream-availability oracle while keeping browser failures simple.
		c.Status(mediaProxyErrorStatus(err))
		return
	}
	if len(result.Body) == 0 || strings.TrimSpace(result.ContentType) == "" {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "inline")
	// The URL is a bearer token and is intentionally short-lived.  Private
	// caching avoids putting customer media in shared intermediary caches.
	maxAge := int(time.Until(result.ExpiresAt).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	c.Header("Cache-Control", "private, max-age="+strconv.Itoa(maxAge))
	c.Data(http.StatusOK, result.ContentType, result.Body)
}

// mediaProxyErrorStatus keeps all token/transport details out of the public
// response.  A temporarily unavailable proxy is represented as 503 so
// clients can retry, while malformed/expired tokens remain 404.
func mediaProxyErrorStatus(err error) int {
	if err == nil {
		return http.StatusNotFound
	}
	// The service currently keeps its token sentinel private; treat its
	// stable wording as invalid without exporting implementation details.
	if strings.Contains(strings.ToLower(err.Error()), "token is invalid or expired") {
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}
