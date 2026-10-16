package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAIStreamFailedEventPassthroughBody 把 Responses SSE（流式响应）的
// response.failed/error 事件整理为普通 {"error": ...} 响应，使现有错误透传
// 规则可按状态码、关键词和上游消息统一匹配。
func openAIStreamFailedEventPassthroughBody(payload []byte, failedMessage string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}
	if gjson.GetBytes(payload, "error").Exists() {
		return payload
	}
	responseError := gjson.GetBytes(payload, "response.error")
	if !responseError.Exists() {
		if strings.TrimSpace(failedMessage) == "" {
			return payload
		}
		body, err := marshalOpenAIUpstreamJSON(gin.H{
			"error": gin.H{"message": failedMessage},
		})
		if err != nil {
			return payload
		}
		return body
	}

	errorPayload := gin.H{}
	if errType := strings.TrimSpace(gjson.Get(responseError.Raw, "type").String()); errType != "" {
		errorPayload["type"] = errType
	}
	if code := strings.TrimSpace(gjson.Get(responseError.Raw, "code").String()); code != "" {
		errorPayload["code"] = code
	}
	if param := strings.TrimSpace(gjson.Get(responseError.Raw, "param").String()); param != "" {
		errorPayload["param"] = param
	}
	message := strings.TrimSpace(gjson.Get(responseError.Raw, "message").String())
	if message == "" {
		message = strings.TrimSpace(failedMessage)
	}
	if message != "" {
		errorPayload["message"] = message
	}
	if len(errorPayload) == 0 {
		return payload
	}
	body, err := marshalOpenAIUpstreamJSON(gin.H{"error": errorPayload})
	if err != nil {
		return payload
	}
	return body
}

// applyOpenAIStreamFailedErrorPassthroughRule 让 HTTP 200 内承载的流式失败
// 进入与普通 HTTP 错误相同的管理员规则链；平台必须使用真实账号平台，避免
// OpenAI 与 Grok 共用 Responses 处理器时命中错误的平台规则。
func applyOpenAIStreamFailedErrorPassthroughRule(
	c *gin.Context,
	platform string,
	payload []byte,
	failedMessage string,
) (status int, errType string, errMsg string, matched bool) {
	return applyErrorPassthroughRule(
		c,
		platform,
		openAIStreamFailedEventSemanticStatus(payload, failedMessage),
		openAIStreamFailedEventPassthroughBody(payload, failedMessage),
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	)
}
