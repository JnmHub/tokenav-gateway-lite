package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GetEmailNotificationConfig returns Ops email notification config (DB-backed).
// GET /api/v1/admin/ops/email-notification/config
func (h *OpsHandler) GetEmailNotificationConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetEmailNotificationConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get email notification config")
		return
	}
	response.Success(c, cfg)
}

// UpdateEmailNotificationConfig updates Ops email notification config (DB-backed).
// PUT /api/v1/admin/ops/email-notification/config
func (h *OpsHandler) UpdateEmailNotificationConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsEmailNotificationConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateEmailNotificationConfig(c.Request.Context(), &req)
	if err != nil {
		// Most failures here are validation errors from request payload; treat as 400.
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetAlertRuntimeSettings returns Ops alert evaluator runtime settings (DB-backed).
// GET /api/v1/admin/ops/runtime/alert
func (h *OpsHandler) GetAlertRuntimeSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetOpsAlertRuntimeSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get alert runtime settings")
		return
	}
	response.Success(c, cfg)
}

// UpdateAlertRuntimeSettings updates Ops alert evaluator runtime settings (DB-backed).
// PUT /api/v1/admin/ops/runtime/alert
func (h *OpsHandler) UpdateAlertRuntimeSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsAlertRuntimeSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateOpsAlertRuntimeSettings(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetRuntimeLogConfig returns runtime log config (DB-backed).
// GET /api/v1/admin/ops/runtime/logging
func (h *OpsHandler) GetRuntimeLogConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetRuntimeLogConfig(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get runtime log config")
		return
	}
	response.Success(c, cfg)
}

// UpdateRuntimeLogConfig updates runtime log config and applies changes immediately.
// PUT /api/v1/admin/ops/runtime/logging
func (h *OpsHandler) UpdateRuntimeLogConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsRuntimeLogConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	updated, err := h.opsService.UpdateRuntimeLogConfig(c.Request.Context(), &req, subject.UserID)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// ResetRuntimeLogConfig removes runtime override and falls back to env/yaml baseline.
// POST /api/v1/admin/ops/runtime/logging/reset
func (h *OpsHandler) ResetRuntimeLogConfig(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Unauthorized")
		return
	}

	updated, err := h.opsService.ResetRuntimeLogConfig(c.Request.Context(), subject.UserID)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetAdvancedSettings returns Ops advanced settings (DB-backed).
// GET /api/v1/admin/ops/advanced-settings
func (h *OpsHandler) GetAdvancedSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetOpsAdvancedSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get advanced settings")
		return
	}
	response.Success(c, cfg)
}

// UpdateAdvancedSettings updates Ops advanced settings (DB-backed).
// PUT /api/v1/admin/ops/advanced-settings
func (h *OpsHandler) UpdateAdvancedSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsAdvancedSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateOpsAdvancedSettings(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}

// GetCodexTailBurstSettings returns only the Tail-Burst switch.
// Unlike the complete Ops advanced-settings endpoint, this endpoint remains
// available when the Ops monitoring dashboard is disabled because the switch
// lives in the main system-settings page and is not an Ops data query.
// GET /api/v1/admin/ops/codex-tail-burst
func (h *OpsHandler) GetCodexTailBurstSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}

	settings, err := h.opsService.GetCodexTailBurstSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get Codex tail-burst settings")
		return
	}
	response.Success(c, settings)
}

type updateCodexTailBurstSettingsRequest struct {
	Enabled *bool `json:"enabled" binding:"required"`
}

// UpdateCodexTailBurstSettings updates only the Tail-Burst switch. Keeping this
// payload narrow prevents a system-settings toggle from overwriting unrelated
// Ops configuration and does not depend on the Ops monitoring master switch.
// PUT /api/v1/admin/ops/codex-tail-burst
func (h *OpsHandler) UpdateCodexTailBurstSettings(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}

	var req updateCodexTailBurstSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	settings, err := h.opsService.UpdateCodexTailBurstSettings(c.Request.Context(), *req.Enabled)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, settings)
}

// GetMetricThresholds returns Ops metric thresholds (DB-backed).
// GET /api/v1/admin/ops/settings/metric-thresholds
func (h *OpsHandler) GetMetricThresholds(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	cfg, err := h.opsService.GetMetricThresholds(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to get metric thresholds")
		return
	}
	response.Success(c, cfg)
}

// UpdateMetricThresholds updates Ops metric thresholds (DB-backed).
// PUT /api/v1/admin/ops/settings/metric-thresholds
func (h *OpsHandler) UpdateMetricThresholds(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	var req service.OpsMetricThresholds
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}

	updated, err := h.opsService.UpdateMetricThresholds(c.Request.Context(), &req)
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	response.Success(c, updated)
}
