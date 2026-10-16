package middleware

import (
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type gatewayCustomErrorWriter struct {
	gin.ResponseWriter
	message string
}

func (w *gatewayCustomErrorWriter) WriteHeader(statusCode int) {
	if statusCode >= http.StatusBadRequest {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *gatewayCustomErrorWriter) WriteHeaderNow() {
	if w.Status() >= http.StatusBadRequest {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *gatewayCustomErrorWriter) Flush() {
	if w.Status() >= http.StatusBadRequest {
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.Flush()
}

func (w *gatewayCustomErrorWriter) Write(payload []byte) (int, error) {
	rewritten := w.rewrite(payload)
	written, err := w.ResponseWriter.Write(rewritten)
	return gatewayCustomErrorWriteResult(len(payload), len(rewritten), written, err)
}

func (w *gatewayCustomErrorWriter) WriteString(payload string) (int, error) {
	rewritten := string(w.rewrite([]byte(payload)))
	written, err := w.ResponseWriter.WriteString(rewritten)
	return gatewayCustomErrorWriteResult(len(payload), len(rewritten), written, err)
}

// ResponseWriter.Write reports bytes consumed from the caller's original
// buffer, not bytes produced after protocol rewriting. Returning the rewritten
// length would violate io.Writer and makes io.Copy report short/invalid writes.
func gatewayCustomErrorWriteResult(originalLen, rewrittenLen, written int, err error) (int, error) {
	if originalLen == rewrittenLen {
		if written != rewrittenLen && err == nil {
			err = io.ErrShortWrite
		}
		return written, err
	}
	if written == rewrittenLen {
		return originalLen, err
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	return 0, err
}

func (w *gatewayCustomErrorWriter) rewrite(payload []byte) []byte {
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		if rewritten, ok := service.RewriteGatewayClientErrorSSE(payload, w.message); ok {
			return rewritten
		}
		return payload
	}
	if w.Status() < http.StatusBadRequest && !strings.Contains(contentType, "json") {
		return payload
	}
	if w.Status() >= http.StatusBadRequest {
		// An upstream may have supplied a Content-Length for the original body.
		// The custom text can differ in size, so force chunked/connection framing.
		w.Header().Del("Content-Length")
		if rewritten, ok := service.RewriteGatewayClientErrorJSON(payload, w.message); ok {
			return rewritten
		}
	}
	return payload
}

func isGatewayClientRequestPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	for _, prefix := range []string{
		"/v1",
		"/v1beta",
		"/models",
		"/backend-api/codex",
		"/responses",
		"/chat/completions",
		"/messages",
		"/embeddings",
		"/images",
		"/videos",
		"/alpha/search",
		"/antigravity",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// GatewayCustomErrorMessage freezes one settings snapshot per customer API
// request. When disabled it adds only one atomic load and one empty check.
func GatewayCustomErrorMessage(settings *service.SettingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		if settings == nil || !isGatewayClientRequestPath(c.Request.URL.Path) {
			c.Next()
			return
		}
		message := settings.CurrentGatewayCustomErrorMessage()
		if message == "" {
			c.Next()
			return
		}
		ctx := service.WithGatewayCustomErrorMessage(c.Request.Context(), message)
		c.Request = c.Request.WithContext(ctx)
		c.Writer = &gatewayCustomErrorWriter{ResponseWriter: c.Writer, message: message}
		c.Next()
	}
}
