package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/gin-gonic/gin"
)

type gatewayServerTimingWriter struct {
	gin.ResponseWriter
	request *http.Request
	once    sync.Once
}

func (w *gatewayServerTimingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gatewayServerTimingWriter) WriteHeader(statusCode int) {
	w.finalizeInitial()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *gatewayServerTimingWriter) WriteHeaderNow() {
	w.finalizeInitial()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *gatewayServerTimingWriter) Write(data []byte) (int, error) {
	w.finalizeInitial()
	return w.ResponseWriter.Write(data)
}

func (w *gatewayServerTimingWriter) WriteString(data string) (int, error) {
	w.finalizeInitial()
	return w.ResponseWriter.WriteString(data)
}

func (w *gatewayServerTimingWriter) Flush() {
	w.finalizeInitial()
	w.ResponseWriter.Flush()
}

func (w *gatewayServerTimingWriter) finalizeInitial() {
	if w == nil || w.request == nil {
		return
	}
	w.once.Do(func() {
		now := time.Now()
		servertiming.RecordFirstByte(w.request.Context(), now)
		if value := servertiming.HeaderValue(w.request.Context(), now); value != "" {
			w.ResponseWriter.Header().Set(servertiming.HeaderName, value)
		}
	})
}

// GatewayLiteServerTiming adds a collector only to sampled customer gateway
// requests. The disabled path does not allocate and admin/public pages are not
// accidentally exposed through this middleware.
func GatewayLiteServerTiming(controller *servertiming.Controller) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || controller == nil || !gatewayServerTimingPath(c.Request.URL.Path) || !controller.ShouldCollect(time.Now()) {
			c.Next()
			return
		}
		collector := servertiming.New(time.Now())
		c.Request = c.Request.WithContext(servertiming.WithCollector(c.Request.Context(), collector))
		// Settlement happens after a streaming response has started. A bounded
		// final trailer carries that last stage without buffering the stream.
		c.Writer.Header().Add("Trailer", servertiming.FinalHeaderName)
		writer := &gatewayServerTimingWriter{ResponseWriter: c.Writer, request: c.Request}
		c.Writer = writer
		c.Next()
		writer.finalizeInitial()
		if value := servertiming.HeaderValue(c.Request.Context(), time.Now()); value != "" {
			c.Writer.Header().Set(servertiming.FinalHeaderName, value)
		}
	}
}

func gatewayServerTimingPath(path string) bool {
	path = strings.TrimSpace(path)
	switch {
	case path == "/responses", strings.HasPrefix(path, "/responses/"),
		path == "/models", path == "/chat/completions", path == "/embeddings",
		strings.HasPrefix(path, "/v1/"), strings.HasPrefix(path, "/v1beta/"),
		strings.HasPrefix(path, "/backend-api/codex/"),
		strings.HasPrefix(path, "/images/"), strings.HasPrefix(path, "/videos/"),
		strings.HasPrefix(path, "/antigravity/"):
		return true
	default:
		return false
	}
}
