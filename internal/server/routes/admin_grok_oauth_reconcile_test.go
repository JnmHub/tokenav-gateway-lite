package routes

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRegisterGrokOAuthRoutesIncludesReconciliation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	admin := router.Group("/api/v1/admin")
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{
		GrokOAuth: adminhandler.NewGrokOAuthHandler(nil, nil, nil),
	}}

	registerGrokOAuthRoutes(admin, handlers)

	found := false
	for _, route := range router.Routes() {
		if route.Method == http.MethodPost && route.Path == "/api/v1/admin/grok/oauth/reconcile" {
			found = true
			break
		}
	}
	require.True(t, found)
}
