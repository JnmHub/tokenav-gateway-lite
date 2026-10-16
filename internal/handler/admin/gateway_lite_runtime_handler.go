package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	defaultIngressSnapshotMinutes = 60
	maxIngressSnapshotMinutes     = 24 * 60
)

type panelRateLimitRuntimeResponse struct {
	Settings     service.PanelRateLimitSettings                   `json:"settings"`
	RuntimeStats middleware.GatewayLitePanelRateLimitRuntimeStats `json:"runtime_stats"`
}

// GetPanelRateLimitSettings returns the persisted effective settings together
// with counters from the limiter currently serving the admin API.
func (h *SettingHandler) GetPanelRateLimitSettings(c *gin.Context) {
	if h == nil || h.settingService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Setting service not available")
		return
	}
	settings, err := h.settingService.GetPanelRateLimitSettings(c.Request.Context())
	if err != nil {
		// Repository errors may contain connection details. Keep them out of the
		// administrator response and leave full diagnosis to protected logs.
		response.Error(c, http.StatusInternalServerError, "Failed to get panel rate limit settings")
		return
	}
	response.Success(c, currentPanelRateLimitRuntimeResponse(settings))
}

// UpdatePanelRateLimitSettings validates presets/custom limits in the service
// before publishing the new in-memory snapshot used by subsequent requests.
func (h *SettingHandler) UpdatePanelRateLimitSettings(c *gin.Context) {
	if h == nil || h.settingService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Setting service not available")
		return
	}
	var input service.PanelRateLimitSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request body")
		return
	}
	if _, err := service.NormalizePanelRateLimitSettings(input); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	settings, err := h.settingService.SetPanelRateLimitSettings(c.Request.Context(), input)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to update panel rate limit settings")
		return
	}
	response.Success(c, currentPanelRateLimitRuntimeResponse(settings))
}

// ResetPanelRateLimitSettings removes the database override instead of writing
// a copied default value that could silently drift after a future upgrade.
func (h *SettingHandler) ResetPanelRateLimitSettings(c *gin.Context) {
	if h == nil || h.settingService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Setting service not available")
		return
	}
	settings, err := h.settingService.ResetPanelRateLimitSettings(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to reset panel rate limit settings")
		return
	}
	response.Success(c, currentPanelRateLimitRuntimeResponse(settings))
}

func currentPanelRateLimitRuntimeResponse(settings service.PanelRateLimitSettings) panelRateLimitRuntimeResponse {
	return panelRateLimitRuntimeResponse{
		Settings:     settings,
		RuntimeStats: middleware.ActiveGatewayLitePanelRateLimitStats(),
	}
}

// GetIngressRejectionSnapshot exposes minute-level aggregate counters only.
// ingress.Snapshot deliberately contains no path, IP, headers, request body or
// API Key, so this read API cannot become a second sensitive request log.
func (h *SettingHandler) GetIngressRejectionSnapshot(c *gin.Context) {
	minutes := defaultIngressSnapshotMinutes
	if raw := strings.TrimSpace(c.Query("minutes")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxIngressSnapshotMinutes {
			response.BadRequest(c, "minutes must be between 1 and 1440")
			return
		}
		minutes = parsed
	}
	response.Success(c, ingress.CurrentSnapshot(minutes))
}
