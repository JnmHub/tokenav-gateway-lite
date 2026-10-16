package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// IPv6EgressHandler 本机 IPv6 出口管理接口：配置读写（热生效，无需重启）
// 与手动重扫。路由挂载于 gateway-lite 运维后台。
type IPv6EgressHandler struct {
	ipv6Egress *service.IPv6EgressService
}

func NewIPv6EgressHandler(ipv6Egress *service.IPv6EgressService) *IPv6EgressHandler {
	return &IPv6EgressHandler{ipv6Egress: ipv6Egress}
}

// ipv6EgressStatusResponse 返回当前生效配置 + 运行状态（地址测通列表）。
type ipv6EgressStatusResponse struct {
	Config   config.GatewayIPv6EgressConfig `json:"config"`
	Enabled  bool                           `json:"enabled"`
	Running  bool                           `json:"running"`
	LastScan string                         `json:"last_scan,omitempty"`
	Entries  []service.IPv6EgressEntry      `json:"entries"`
	OKCount  int                            `json:"ok_count"`
}

// GetStatus GET /api/v1/admin/ipv6-egress
func (h *IPv6EgressHandler) GetStatus(c *gin.Context) {
	if h.ipv6Egress == nil {
		response.BadRequest(c, "IPv6 出口管理不可用")
		return
	}
	enabled, lastScan, entries := h.ipv6Egress.Status()
	okCount := 0
	for _, e := range entries {
		if e.Status == "ok" {
			okCount++
		}
	}
	lastScanText := ""
	if !lastScan.IsZero() {
		lastScanText = lastScan.Format("2006-01-02 15:04:05")
	}
	response.Success(c, ipv6EgressStatusResponse{
		Config:   h.ipv6Egress.RuntimeConfig(),
		Enabled:  enabled,
		Running:  h.ipv6Egress.Running(),
		LastScan: lastScanText,
		Entries:  entries,
		OKCount:  okCount,
	})
}

// ipv6EgressConfigRequest 配置更新请求（全部字段可选，仅提交变更项）。
// json tag 与 GET 响应的配置字段名保持一致（snake_case）。
type ipv6EgressConfigRequest struct {
	Enabled             *bool   `json:"enabled"`
	ListenHost         *string `json:"listen_host"`
	ListenPort         *int    `json:"listen_port"`
	ProbeTarget        *string `json:"probe_target"`
	SyncIntervalSeconds *int   `json:"sync_interval_seconds"`
	NamePrefix         *string `json:"name_prefix"`
	MaxAddresses       *int    `json:"max_addresses"`
}

// UpdateConfig PUT /api/v1/admin/ipv6-egress
// 热更新：校验→持久化（data/ipv6_egress.json）→按新配置重启监听与扫描，无需重启进程。
func (h *IPv6EgressHandler) UpdateConfig(c *gin.Context) {
	if h.ipv6Egress == nil {
		response.BadRequest(c, "IPv6 出口管理不可用")
		return
	}
	var req ipv6EgressConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求体格式错误: "+err.Error())
		return
	}
	next := h.ipv6Egress.RuntimeConfig()
	if req.Enabled != nil {
		next.Enabled = *req.Enabled
	}
	if req.ListenHost != nil {
		next.ListenHost = *req.ListenHost
	}
	if req.ListenPort != nil {
		next.ListenPort = *req.ListenPort
	}
	if req.ProbeTarget != nil {
		next.ProbeTarget = *req.ProbeTarget
	}
	if req.SyncIntervalSeconds != nil {
		next.SyncIntervalSeconds = *req.SyncIntervalSeconds
	}
	if req.NamePrefix != nil {
		next.NamePrefix = *req.NamePrefix
	}
	if req.MaxAddresses != nil {
		next.MaxAddresses = *req.MaxAddresses
	}
	if err := h.ipv6Egress.ApplyRuntimeConfig(next); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// 返回应用后的完整状态，前端保存后立即刷新展示。
	h.GetStatus(c)
}

// Rescan POST /api/v1/admin/ipv6-egress/rescan
// 立即执行一次"识别 → 测通 → 注册"全流程（同步执行，阻塞至完成）。
func (h *IPv6EgressHandler) Rescan(c *gin.Context) {
	if h.ipv6Egress == nil {
		response.BadRequest(c, "IPv6 出口管理不可用")
		return
	}
	if !h.ipv6Egress.RuntimeConfig().Enabled {
		response.BadRequest(c, "IPv6 出口管理未启用")
		return
	}
	h.ipv6Egress.Rescan(c.Request.Context())
	h.GetStatus(c)
}
