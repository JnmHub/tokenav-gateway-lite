package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	grokVideoTransformIdempotencyTTL          = 7 * 24 * time.Hour
	grokVideoTaskRegistrationFailedContextKey = "_grok_video_task_registration_failed"
)

// grokMediaIdempotentHTTPResponse is the only transform response persisted in
// idempotency_records. It intentionally excludes arbitrary upstream headers so
// cookies, credentials, signed URLs, or debug bodies cannot become replay data.
type grokMediaIdempotentHTTPResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body"`
}

type grokMediaBufferedHTTPError struct {
	response *grokMediaIdempotentHTTPResponse
}

func (e *grokMediaBufferedHTTPError) Error() string {
	if e == nil || e.response == nil {
		return "grok media request failed"
	}
	return fmt.Sprintf("grok media request failed with status %d", e.response.Status)
}

// handleGrokVideoTransformIdempotent prevents a client retry from creating a
// second paid xAI task. The response stays buffered until both the durable task
// registration and the idempotency success marker are committed. A process or
// database failure after an uncertain external side effect keeps the claim
// locked for the full replay window instead of automatically retrying upstream.
func (h *OpenAIGatewayHandler) handleGrokVideoTransformIdempotent(c *gin.Context, endpoint service.GrokMediaEndpoint) {
	h.handleGrokVideoTransformIdempotentWithExecutor(c, endpoint, func() {
		h.handleGrokMedia(c, endpoint, "")
	})
}

func (h *OpenAIGatewayHandler) handleGrokVideoTransformIdempotentWithExecutor(c *gin.Context, endpoint service.GrokMediaEndpoint, execute func()) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	idempotencyKey, err := service.NormalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if idempotencyKey == "" {
		response.ErrorFrom(c, service.ErrIdempotencyKeyRequired)
		return
	}
	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		response.ErrorFrom(c, service.ErrIdempotencyStoreUnavail)
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
		return
	}
	restoreRequestBody(c, body, c.Request.Context())

	canonicalRoute := EndpointVideosEdits
	if endpoint == service.GrokMediaEndpointVideosExtensions {
		canonicalRoute = EndpointVideosExtensions
	}
	scope := fmt.Sprintf("gateway.grok_video_transform.%s.api_key.%d", endpoint.VideoOperation(), apiKey.ID)
	actorScope := fmt.Sprintf("user:%d:api_key:%d", subject.UserID, apiKey.ID)

	result, execErr := coordinator.Execute(c.Request.Context(), service.IdempotencyExecuteOptions{
		Scope:             scope,
		ActorScope:        actorScope,
		Method:            http.MethodPost,
		Route:             canonicalRoute,
		IdempotencyKey:    idempotencyKey,
		Payload:           payload,
		TTL:               grokVideoTransformIdempotencyTTL,
		RequireKey:        true,
		ProcessingTimeout: grokVideoTransformIdempotencyTTL,
	}, func(ctx context.Context) (any, error) {
		restoreRequestBody(c, body, ctx)
		originalWriter := c.Writer
		buffered := newGrokMediaBufferedWriter(originalWriter)
		c.Writer = buffered
		func() {
			defer func() { c.Writer = originalWriter }()
			execute()
		}()

		captured := buffered.idempotentResponse()
		if failed, _ := c.Get(grokVideoTaskRegistrationFailedContextKey); failed == true {
			captured = &grokMediaIdempotentHTTPResponse{
				Status:  http.StatusServiceUnavailable,
				Headers: map[string][]string{"Content-Type": {"application/json; charset=utf-8"}},
				Body:    `{"error":{"type":"grok_video_task_registration_failed","message":"The upstream task was accepted but durable registration failed; reuse the same Idempotency-Key to receive this stable outcome"}}`,
			}
			// Upstream acceptance is an irreversible outcome. Store and replay this
			// stable 503 rather than marking it retryable and creating a second task.
			return captured, nil
		}
		if captured.Status < http.StatusOK || captured.Status >= http.StatusMultipleChoices {
			return nil, &grokMediaBufferedHTTPError{response: captured}
		}
		return captured, nil
	})
	if execErr != nil {
		var bufferedErr *grokMediaBufferedHTTPError
		if errors.As(execErr, &bufferedErr) && bufferedErr.response != nil {
			writeGrokMediaIdempotentResponse(c, bufferedErr.response)
			return
		}
		if retryAfter := service.RetryAfterSecondsFromError(execErr); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, execErr)
		return
	}
	captured, err := decodeGrokMediaIdempotentResponse(result.Data)
	if err != nil {
		response.ErrorFrom(c, service.ErrIdempotencyStoreUnavail.WithCause(err))
		return
	}
	if result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	writeGrokMediaIdempotentResponse(c, captured)
}

func restoreRequestBody(c *gin.Context, body []byte, ctx context.Context) {
	if c == nil || c.Request == nil {
		return
	}
	c.Request = c.Request.WithContext(ctx)
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
}

func decodeGrokMediaIdempotentResponse(data any) (*grokMediaIdempotentHTTPResponse, error) {
	if captured, ok := data.(*grokMediaIdempotentHTTPResponse); ok && captured != nil {
		return captured, nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var captured grokMediaIdempotentHTTPResponse
	if err := json.Unmarshal(raw, &captured); err != nil {
		return nil, err
	}
	if captured.Status < 100 || captured.Status > 599 {
		return nil, errors.New("stored Grok media response has an invalid status")
	}
	return &captured, nil
}

func writeGrokMediaIdempotentResponse(c *gin.Context, captured *grokMediaIdempotentHTTPResponse) {
	if c == nil || captured == nil {
		return
	}
	for name, values := range captured.Headers {
		c.Writer.Header().Del(name)
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	c.Status(captured.Status)
	_, _ = c.Writer.WriteString(captured.Body)
}

// grokMediaBufferedWriter implements gin.ResponseWriter without committing
// bytes to the client. Media transforms are JSON-only and never hijack or
// stream, but the complete interface keeps existing handler helpers unchanged.
type grokMediaBufferedWriter struct {
	original gin.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
}

func newGrokMediaBufferedWriter(original gin.ResponseWriter) *grokMediaBufferedWriter {
	return &grokMediaBufferedWriter{original: original, header: make(http.Header)}
}

func (w *grokMediaBufferedWriter) Header() http.Header { return w.header }

func (w *grokMediaBufferedWriter) WriteHeader(statusCode int) {
	if w.status != 0 {
		return
	}
	w.status = statusCode
}

func (w *grokMediaBufferedWriter) Write(data []byte) (int, error) {
	w.WriteHeaderNow()
	return w.body.Write(data)
}

func (w *grokMediaBufferedWriter) WriteString(value string) (int, error) {
	w.WriteHeaderNow()
	return w.body.WriteString(value)
}

func (w *grokMediaBufferedWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *grokMediaBufferedWriter) Size() int {
	if !w.Written() {
		return -1
	}
	return w.body.Len()
}

func (w *grokMediaBufferedWriter) Written() bool { return w.status != 0 }

func (w *grokMediaBufferedWriter) WriteHeaderNow() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
}

func (w *grokMediaBufferedWriter) Flush() { w.WriteHeaderNow() }

func (w *grokMediaBufferedWriter) CloseNotify() <-chan bool {
	if w.original != nil {
		return w.original.CloseNotify()
	}
	ch := make(chan bool)
	return ch
}

func (w *grokMediaBufferedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("Grok media response buffering does not support hijacking")
}

func (w *grokMediaBufferedWriter) Pusher() http.Pusher {
	if w.original == nil {
		return nil
	}
	return w.original.Pusher()
}

func (w *grokMediaBufferedWriter) idempotentResponse() *grokMediaIdempotentHTTPResponse {
	status := w.Status()
	if !w.Written() {
		status = http.StatusInternalServerError
	}
	headers := make(map[string][]string)
	for _, name := range []string{"Content-Type", "X-Request-Id", "Xai-Request-Id"} {
		if values := w.header.Values(name); len(values) > 0 {
			headers[name] = append([]string(nil), values...)
		}
	}
	return &grokMediaIdempotentHTTPResponse{Status: status, Headers: headers, Body: w.body.String()}
}
