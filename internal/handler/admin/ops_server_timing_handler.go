package admin

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

type serverTimingTemporaryRequest struct {
	Preset string `json:"preset"`
}

// GetServerTimingRuntime returns value-only state. It never exposes collected
// request paths, account IDs, upstream hosts or individual timing samples.
func (h *OpsHandler) GetServerTimingRuntime(c *gin.Context) {
	response.Success(c, servertiming.DefaultController().Status(time.Now()))
}

// EnableServerTimingTemporary accepts UI-friendly presets so an administrator
// can click a duration instead of entering an arbitrary expression.
func (h *OpsHandler) EnableServerTimingTemporary(c *gin.Context) {
	var input serverTimingTemporaryRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	preset := strings.ToLower(strings.TrimSpace(input.Preset))
	if preset == "" {
		preset = "15m"
	}
	duration, ok := map[string]time.Duration{
		"5m": 5 * time.Minute, "15m": 15 * time.Minute,
		"30m": 30 * time.Minute, "60m": time.Hour,
	}[preset]
	if !ok {
		response.BadRequest(c, "preset must be one of 5m, 15m, 30m, 60m")
		return
	}
	controller := servertiming.DefaultController()
	controller.EnableTemporary(time.Now(), duration)
	middleware.SetAuditAction(c, "admin.runtime.server_timing.enable")
	middleware.SetAuditResource(c, "runtime_config", "server_timing")
	middleware.SetAuditSummary(c, map[string]any{"mode": preset, "configured": true})
	response.Success(c, controller.Status(time.Now()))
}

func (h *OpsHandler) DisableServerTimingTemporary(c *gin.Context) {
	controller := servertiming.DefaultController()
	controller.DisableTemporary()
	middleware.SetAuditAction(c, "admin.runtime.server_timing.disable")
	middleware.SetAuditResource(c, "runtime_config", "server_timing")
	middleware.SetAuditSummary(c, map[string]any{"configured": false})
	response.Success(c, controller.Status(time.Now()))
}
