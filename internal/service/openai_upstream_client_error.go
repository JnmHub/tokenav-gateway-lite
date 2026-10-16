package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAIUpstreamClientErrorFallbackType    = "invalid_request_error"
	openAIUpstreamClientErrorFallbackMessage = "Upstream rejected the request"
)

// isOpenAIDeterministicClientError 只认 400。它表示请求本身不合法，同一请求
// 换账号重试仍会失败；401/403 等运营方账号问题继续由既有逻辑隐藏处理。
func isOpenAIDeterministicClientError(statusCode int) bool {
	return statusCode == http.StatusBadRequest
}

// writeOpenAIUpstreamClientError 保留上游的 type/code/param，方便客户端定位
// 具体非法字段；message 已由调用方清洗，避免绕过敏感信息脱敏。
func writeOpenAIUpstreamClientError(c *gin.Context, statusCode int, body []byte, upstreamMsg string) {
	errorPayload := gin.H{"type": openAIUpstreamClientErrorFallbackType}
	if errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String()); errType != "" {
		errorPayload["type"] = errType
	}
	if code := strings.TrimSpace(extractUpstreamErrorCode(body)); code != "" {
		errorPayload["code"] = code
	}
	if param := strings.TrimSpace(gjson.GetBytes(body, "error.param").String()); param != "" {
		errorPayload["param"] = param
	}
	message := strings.TrimSpace(upstreamMsg)
	if message == "" {
		message = openAIUpstreamClientErrorFallbackMessage
	}
	errorPayload["message"] = message
	c.JSON(statusCode, gin.H{"error": errorPayload})
}
