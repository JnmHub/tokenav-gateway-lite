package middleware

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/debugschema"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const requestDebugBodyLimit = 2 << 20

// RequestDebugSchema records bounded request/response structure snapshots only
// while the temporary controller is enabled. It deliberately skips admin paths.
func RequestDebugSchema(controller *debugschema.Controller) gin.HandlerFunc {
	if controller == nil {
		controller = debugschema.DefaultController()
	}
	return func(c *gin.Context) {
		if c == nil || c.Request == nil || (!controller.Enabled(time.Now()) && !debugschema.DefaultFullCaptureController().Enabled(time.Now())) || !strings.HasPrefix(c.Request.URL.Path, "/v1/") {
			c.Next()
			return
		}
		started := time.Now()
		var reqBody []byte
		var reqTruncated bool
		contentType := c.GetHeader("Content-Type")
		fullCtl := debugschema.DefaultFullCaptureController()
		readLimit := requestDebugBodyLimit
		if fullCtl.Enabled(time.Now()) && fullCtl.BodyLimit() > readLimit {
			readLimit = fullCtl.BodyLimit()
		}
		if c.Request.Body != nil && (fullCtl.Enabled(time.Now()) || (c.Request.ContentLength > 0 && c.Request.ContentLength <= requestDebugBodyLimit && strings.Contains(strings.ToLower(contentType), "json"))) {
			originalBody := c.Request.Body
			raw, err := io.ReadAll(io.LimitReader(originalBody, int64(readLimit)+1))
			if err == nil {
				if len(raw) > readLimit {
					reqBody = raw[:readLimit]
					reqTruncated = true
					// Restore the bytes already consumed and leave the remainder for
					// downstream handlers; truncation must never alter the request.
					c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), originalBody))
				} else {
					reqBody = raw
					c.Request.Body = io.NopCloser(bytes.NewReader(raw))
				}
			}
		}
		writeLimit := requestDebugBodyLimit
		if fullCtl.Enabled(time.Now()) && fullCtl.BodyLimit() > writeLimit {
			writeLimit = fullCtl.BodyLimit()
		}
		cw := &requestDebugWriter{ResponseWriter: c.Writer, limit: writeLimit}
		c.Writer = cw
		c.Next()
		c.Writer = cw.ResponseWriter
		if !controller.Enabled(time.Now()) && !fullCtl.Enabled(time.Now()) {
			return
		}
		captureLimit := requestDebugBodyLimit
		if fullCtl.Enabled(time.Now()) && fullCtl.BodyLimit() > captureLimit {
			captureLimit = fullCtl.BodyLimit()
		}
		respBody, respTruncated := debugschema.CopyBody(cw.buf.Bytes(), captureLimit)
		s := debugschema.Snapshot{
			ID: time.Now().UTC().Format("20060102T150405.000000000Z"), CreatedAt: started.UTC(), DurationMs: time.Since(started).Milliseconds(),
			Method: c.Request.Method, Path: c.Request.URL.Path, RequestID: strings.TrimSpace(c.GetHeader("X-Request-ID")),
			RequestHeaders: debugschema.Headers(c.Request.Header), RequestBody: debugschema.Shape(reqBody, contentType, reqTruncated),
			ResponseStatus: cw.Status(), ResponseHeaders: debugschema.Headers(cw.Header()), ResponseBody: debugschema.Shape(respBody, cw.Header().Get("Content-Type"), respTruncated),
		}
		// Authentication runs inside this middleware. Capture only stable IDs and
		// display name so group/key-scoped sessions can be filtered without
		// retaining a credential or any user payload.
		apiKey, ok := GetAPIKeyFromContext(c)
		if !ok {
			apiKey, ok = GetOpsFallbackAPIKey(c)
		}
		if ok && apiKey != nil {
			s.APIKeyID = apiKey.ID
			s.APIKeyRef = strings.TrimSpace(apiKey.Name)
			// gateway-lite snapshot authentication keeps the stable control-plane
			// key identifier in APIKey.Name while leaving APIKey.ID at zero.
			if s.APIKeyID <= 0 {
				if id, err := strconv.ParseInt(strings.TrimSpace(apiKey.Name), 10, 64); err == nil {
					s.APIKeyID = id
				}
			}
			if apiKey.Group != nil {
				s.GroupID = apiKey.Group.ID
				s.GroupName = strings.TrimSpace(apiKey.Group.Name)
			} else if apiKey.GroupID != nil {
				s.GroupID = *apiKey.GroupID
			}
		}
		if controller.Enabled(time.Now()) {
			controller.Add(s)
		}
		apiKey, ok = GetAPIKeyFromContext(c)
		if !ok {
			apiKey, ok = GetOpsFallbackAPIKey(c)
		}
		localKey := ok && apiKey != nil && (service.IsGatewayLiteLocalAPIKey(apiKey.Key) || service.IsGatewayLiteLocalAPIKeyContext(c.Request.Context()))
		if ok && apiKey != nil && fullCtl.Matches(apiKey.ID, apiKey.Name, localKey) {
			fullCtl.Add(debugschema.FullSnapshot{ID: s.ID, CreatedAt: s.CreatedAt, DurationMs: s.DurationMs, Method: s.Method, Path: s.Path, RequestID: s.RequestID, APIKeyID: s.APIKeyID, APIKeyRef: s.APIKeyRef, GroupID: s.GroupID, GroupName: s.GroupName, RequestHeaders: debugschema.CloneHeaderValues(c.Request.Header), RequestBody: reqBody, RequestContentType: contentType, RequestTruncated: reqTruncated, ResponseStatus: cw.Status(), ResponseHeaders: debugschema.CloneHeaderValues(cw.Header()), ResponseBody: respBody, ResponseContentType: cw.Header().Get("Content-Type"), ResponseTruncated: respTruncated})
		}
	}
}

type requestDebugWriter struct {
	gin.ResponseWriter
	buf   bytes.Buffer
	limit int
}

func (w *requestDebugWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < w.limit {
		n := w.limit - w.buf.Len()
		if len(p) < n {
			n = len(p)
		}
		_, _ = w.buf.Write(p[:n])
	}
	return w.ResponseWriter.Write(p)
}
func (w *requestDebugWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
