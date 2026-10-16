package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// FakeFirstTokenHandler 伪造流式首字配置（总开关）：
// GET/PUT /api/v1/admin/ops/runtime/fake-first-token —— 保存即热生效（30s
// 进程内缓存立即刷新），无需重启。
type FakeFirstTokenHandler struct {
	settingService *service.SettingService
}

func NewFakeFirstTokenHandler(settingService *service.SettingService) *FakeFirstTokenHandler {
	return &FakeFirstTokenHandler{settingService: settingService}
}

func (h *FakeFirstTokenHandler) Get(c *gin.Context) {
	if h.settingService == nil {
		response.BadRequest(c, "设置服务不可用")
		return
	}
	response.Success(c, h.settingService.GetFakeFirstTokenSettings(c.Request.Context()))
}

func (h *FakeFirstTokenHandler) Update(c *gin.Context) {
	if h.settingService == nil {
		response.BadRequest(c, "设置服务不可用")
		return
	}
	var req service.FakeFirstTokenSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求体格式错误: "+err.Error())
		return
	}
	if err := h.settingService.SetFakeFirstTokenSettings(c.Request.Context(), req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, h.settingService.GetFakeFirstTokenSettings(c.Request.Context()))
}
