//go:build !embed

// Package web provides embedded web assets for the application.
package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// PublicSettingsProvider is an interface to fetch public settings
// This stub is needed for compilation when frontend is not embedded
type PublicSettingsProvider interface {
	GetPublicSettingsForInjection(ctx context.Context) (any, error)
}

// FrontendServer is a stub for non-embed builds
type FrontendServer struct{}

// NewFrontendServer returns an error when frontend is not embedded
func NewFrontendServer(settingsProvider PublicSettingsProvider, cfg ...*config.Config) (*FrontendServer, error) {
	return nil, errors.New("frontend not embedded")
}

// InvalidateCache is a no-op for non-embed builds
func (s *FrontendServer) InvalidateCache() {}

// Middleware returns a handler that returns 404 for non-embed builds
func (s *FrontendServer) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.String(http.StatusNotFound, "Frontend not embedded. Do not build with -tags noembed if you need the admin UI.")
		c.Abort()
	}
}

func ServeEmbeddedFrontend(cfg ...*config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.String(http.StatusNotFound, "Frontend not embedded. Do not build with -tags noembed if you need the admin UI.")
		c.Abort()
	}
}

func HasEmbeddedFrontend() bool {
	return false
}
