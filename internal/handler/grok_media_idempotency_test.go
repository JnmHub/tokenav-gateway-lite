package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGrokVideoTransformIdempotencyReplaysWithoutSecondPaidCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := service.DefaultIdempotencyCoordinator()
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = true
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(newUserMemoryIdempotencyRepoStub(), cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })

	h := &OpenAIGatewayHandler{}
	var creates atomic.Int32
	perform := func(body, key string, registrationFailed bool) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/edits", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		if key != "" {
			c.Request.Header.Set("Idempotency-Key", key)
		}
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 71, User: &service.User{ID: 42}})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42, Concurrency: 3})
		h.handleGrokVideoTransformIdempotentWithExecutor(c, service.GrokMediaEndpointVideosEdits, func() {
			creates.Add(1)
			if registrationFailed {
				c.Set(grokVideoTaskRegistrationFailedContextKey, true)
			}
			c.JSON(http.StatusOK, gin.H{"request_id": "video-edit-idempotent-1"})
		})
		return recorder
	}

	body := `{"model":"grok-imagine-video","prompt":"add a hat","video":{"url":"https://example.com/source.mp4"}}`
	first := perform(body, "video-edit-operation-1", false)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.JSONEq(t, `{"request_id":"video-edit-idempotent-1"}`, first.Body.String())
	require.Empty(t, first.Header().Get("X-Idempotency-Replayed"))

	second := perform(body, "video-edit-operation-1", false)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.EqualValues(t, 1, creates.Load(), "same key and payload must not create a second upstream task")

	conflict := perform(strings.Replace(body, "add a hat", "add glasses", 1), "video-edit-operation-1", false)
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	require.EqualValues(t, 1, creates.Load())

	missing := perform(body, "", false)
	require.Equal(t, http.StatusBadRequest, missing.Code, missing.Body.String())
	require.Contains(t, missing.Body.String(), "IDEMPOTENCY_KEY_REQUIRED")
	require.EqualValues(t, 1, creates.Load())

	acceptedButUntracked := perform(body, "video-edit-operation-untracked", true)
	require.Equal(t, http.StatusServiceUnavailable, acceptedButUntracked.Code, acceptedButUntracked.Body.String())
	require.Contains(t, acceptedButUntracked.Body.String(), "grok_video_task_registration_failed")
	replayedUntracked := perform(body, "video-edit-operation-untracked", false)
	require.Equal(t, http.StatusServiceUnavailable, replayedUntracked.Code, replayedUntracked.Body.String())
	require.Equal(t, "true", replayedUntracked.Header().Get("X-Idempotency-Replayed"))
	require.EqualValues(t, 2, creates.Load(), "accepted but untracked outcome must be stable and never call upstream again")
}

func TestGrokVideoTransformIdempotencyFailsClosedWithoutStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previous) })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos/extensions", strings.NewReader(`{"model":"grok-imagine-video"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Idempotency-Key", "video-extension-no-store")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 72, User: &service.User{ID: 43}})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 43})

	var called atomic.Bool
	(&OpenAIGatewayHandler{}).handleGrokVideoTransformIdempotentWithExecutor(c, service.GrokMediaEndpointVideosExtensions, func() {
		called.Store(true)
	})

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Body.String(), "IDEMPOTENCY_STORE_UNAVAILABLE")
	require.False(t, called.Load())
}
