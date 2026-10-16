package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/gin-gonic/gin"
)

// All aliases resolve before auth; a websocket occupies inflight for its whole
// connection, while the separate per-turn guard rejects new response.create.
func GatewayBindingAdmission() gin.HandlerFunc {
	manager := gatewaylite.DefaultBindingManager()
	return func(c *gin.Context) {
		paid, _ := gatewayBindingPaidRequest(c.Request)
		if !paid || manager == nil {
			c.Next()
			return
		}
		leave, allowed := manager.EnterRequest(c.Request.Context(), false)
		if !allowed {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"type": "gateway_binding_unavailable", "message": "网关未完成主站绑定或正在断开，暂不接受新的付费请求。"}})
			return
		}
		defer leave()
		c.Request = c.Request.WithContext(gatewaylite.ContextWithBindingTurnGuard(c.Request.Context(), manager))
		c.Next()
	}
}
func gatewayBindingPaidRequest(req *http.Request) (bool, bool) {
	path := strings.TrimRight(req.URL.Path, "/")
	path = strings.TrimPrefix(path, "/antigravity")
	for _, prefix := range []string{"/backend-api/codex", "/api/v3", "/v1beta", "/v1", "/v3"} {
		if strings.HasPrefix(path, prefix+"/") {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	seedance := path == "/contents/generations/tasks" && req.Method == http.MethodPost
	if req.Method == http.MethodGet {
		upgrade := strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
		return upgrade && (path == "/responses" || path == "/realtime" || strings.HasPrefix(path, "/live")), false
	}
	if req.Method == http.MethodDelete {
		return false, false
	}
	if req.Method != http.MethodPost && req.Method != http.MethodPut && req.Method != http.MethodPatch {
		return false, false
	}
	if seedance {
		return true, true
	}
	for _, paid := range []string{"/responses", "/chat/completions", "/messages", "/embeddings", "/images/generations", "/images/edits", "/videos", "/alpha/search", "/live", "/realtime", "/tts", "/stt", "/custom-voices", "/web_search", "/x_search"} {
		if path == paid || strings.HasPrefix(path, paid+"/") {
			return true, false
		}
	}
	// Gemini actions use model-qualified generateContent/streamGenerateContent
	// paths. Queries/cancellation on old media tasks intentionally remain usable.
	return strings.HasPrefix(path, "/models/") && (strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent")), false
}
