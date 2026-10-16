package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// [官方 v0.2.13 移植] cyber 用户白名单在网关准入侧的短路。
//
// 为什么需要在 handler 这一层：会话屏蔽的判定点有三处（HTTP responses、
// chat/completions、WS 握手），共用同一份「派生 key → 查屏蔽表」逻辑。白名单
// 必须在这三处**都**生效，否则用户换成 chat 端点或重开 WS 连接就又被拦。
// 统一收在这里，避免以后新增入口时漏挂（此前正是散落的多份 inline 调用）。

// cyberPolicyLogOnly 判断该 API Key 所属用户是否在 cyber 白名单里（只记录不拦截）。
func (h *OpenAIGatewayHandler) cyberPolicyLogOnly(c *gin.Context, apiKey *service.APIKey) bool {
	return h != nil && c != nil && c.Request != nil && h.gatewayService != nil &&
		h.gatewayService.CyberPolicyLogOnly(c.Request.Context(), apiKey)
}

// findCyberSessionBlockKeyForAPIKey 返回该请求应检查的会话屏蔽 key；
// 白名单用户返回空串（视为未被屏蔽），调用方据此直接放行。
//
// 注意：这里只做「派生 + 白名单短路」，真正查屏蔽表仍由调用方执行，保持
// 「开关关闭时连 key 派生都不做」的既有成本特征。
func (h *OpenAIGatewayHandler) findCyberSessionBlockKeyForAPIKey(c *gin.Context, apiKey *service.APIKey, body []byte) string {
	if apiKey == nil || h.cyberPolicyLogOnly(c, apiKey) {
		return ""
	}
	return service.CyberSessionBlockKey(apiKey.ID, c, body)
}
