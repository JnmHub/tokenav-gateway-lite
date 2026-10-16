package service

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/iotest"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestResolveUpstreamResponseReadLimit(t *testing.T) {
	t.Run("use default when config missing", func(t *testing.T) {
		require.Equal(t, defaultUpstreamResponseReadMaxBytes, resolveUpstreamResponseReadLimit(nil))
	})

	t.Run("use configured value", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 1234
		require.Equal(t, int64(1234), resolveUpstreamResponseReadLimit(cfg))
	})
}

func TestReadUpstreamResponseBodyLimited(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := readUpstreamResponseBodyLimited(bytes.NewReader([]byte("ok")), 2)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit", func(t *testing.T) {
		body, err := readUpstreamResponseBodyLimited(bytes.NewReader([]byte("toolong")), 3)
		require.Nil(t, body)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})
}

func TestReadUpstreamResponseBody(t *testing.T) {
	t.Run("within limit", func(t *testing.T) {
		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("ok")), nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []byte("ok"), body)
	})

	t.Run("exceeds limit calls onTooLarge", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 3

		called := false
		onTooLarge := func(_ *gin.Context) { called = true }

		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("toolong")), cfg, nil, onTooLarge)
		require.Nil(t, body)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
		require.True(t, called)
	})

	t.Run("nil onTooLarge does not panic", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.UpstreamResponseReadMaxBytes = 3

		body, err := ReadUpstreamResponseBody(bytes.NewReader([]byte("toolong")), cfg, nil, nil)
		require.Nil(t, body)
		require.True(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
	})

	t.Run("io error does not call onTooLarge", func(t *testing.T) {
		called := false
		onTooLarge := func(_ *gin.Context) { called = true }
		core, observed := observer.New(zap.WarnLevel)
		log := zap.New(core)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		req := httptest.NewRequest("POST", "/v1/responses", nil)
		c.Request = req.WithContext(logger.IntoContext(req.Context(), log))

		body, err := ReadUpstreamResponseBody(iotest.ErrReader(errors.New("disk failure")), nil, c, onTooLarge)
		require.Nil(t, body)
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrUpstreamResponseBodyTooLarge))
		require.False(t, called)
		status, exists := c.Get(OpsUpstreamStatusCodeKey)
		require.True(t, exists)
		require.Equal(t, http.StatusBadGateway, status)
		message, exists := c.Get(OpsUpstreamErrorMessageKey)
		require.True(t, exists)
		require.Equal(t, "failed to read upstream response body", message)
		detail, exists := c.Get(OpsUpstreamErrorDetailKey)
		require.True(t, exists)
		require.Equal(t, "disk failure", detail)
		entries := observed.All()
		require.Len(t, entries, 1)
		require.Equal(t, "read upstream response body failed", entries[0].Message)
		require.Equal(t, "service.upstream_response_limit", entries[0].ContextMap()["component"])
		require.Equal(t, "disk failure", entries[0].ContextMap()["error"])
	})
}
