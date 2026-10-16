package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func isBillingIntrospectionRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || c.Request.Method != http.MethodGet {
		return false
	}
	return strings.TrimRight(c.Request.URL.Path, "/") == "/v1/sub2api/billing"
}
