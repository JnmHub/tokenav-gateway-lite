package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type updateUpstreamBillingAutoProbeRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *AccountHandler) SetUpstreamBillingProbeService(probe *service.UpstreamBillingProbeService) {
	if h != nil {
		h.upstreamBillingProbe = probe
	}
}

// ProbeUpstreamBilling 执行一次手动探测；不受未来自动 runner（定时任务）开关影响。
func (h *AccountHandler) ProbeUpstreamBilling(c *gin.Context) {
	if h == nil || h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	snapshot, err := h.upstreamBillingProbe.ProbeAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.UpstreamBillingProbeResult{AccountID: accountID, Snapshot: snapshot})
}

// UpdateUpstreamBillingAutoProbe 切换单账号自动探测。手动探测始终独立可用。
func (h *AccountHandler) UpdateUpstreamBillingAutoProbe(c *gin.Context) {
	if h == nil || h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBillingProbeUnavailable)
		return
	}
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req updateUpstreamBillingAutoProbeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.upstreamBillingProbe.SetAutoProbeEnabled(c.Request.Context(), accountID, req.Enabled); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"account_id": accountID, "enabled": req.Enabled})
}
