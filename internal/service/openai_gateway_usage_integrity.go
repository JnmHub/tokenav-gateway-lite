package service

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	grokMissingUsageErrorCode = "grok_missing_usage"
	grokMissingUsageMessage   = "xAI upstream returned a successful chat completion without billable usage"
)

// hasBillableGrokChatUsage 只接受最终账单使用的聚合 token 桶。
// 细分字段单独存在不能证明这次成功响应可以安全结算。
func hasBillableGrokChatUsage(usage OpenAIUsage) bool {
	return usage.InputTokens > 0 ||
		usage.OutputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 ||
		usage.CacheReadInputTokens > 0
}

// requiresBillableGrokChatUsage 同时按账号平台和最终模型识别 Grok。
// Grok 可能挂在 OpenAI-compatible（OpenAI 兼容）账号下，不能只看账号平台。
func requiresBillableGrokChatUsage(account *Account, models ...string) bool {
	if account != nil && account.Platform == PlatformGrok {
		return true
	}
	for _, model := range models {
		normalized := strings.ToLower(strings.TrimSpace(model))
		if separator := strings.LastIndex(normalized, "/"); separator >= 0 {
			normalized = strings.TrimSpace(normalized[separator+1:])
		}
		if normalized == "grok" || strings.HasPrefix(normalized, "grok-") {
			return true
		}
	}
	return false
}

// newGrokMissingUsageFailoverError 在响应写给客户端前返回可切号错误，防止内容已交付但无法计费。
func newGrokMissingUsageFailoverError(c *gin.Context, account *Account, upstreamRequestID string) *UpstreamFailoverError {
	accountID := int64(0)
	accountName := ""
	if account != nil {
		accountID = account.ID
		accountName = account.Name
	}

	setOpsUpstreamError(c, http.StatusBadGateway, grokMissingUsageMessage, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           PlatformGrok,
		AccountID:          accountID,
		AccountName:        accountName,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               "failover",
		Message:            grokMissingUsageMessage,
	})

	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"code":    grokMissingUsageErrorCode,
			"message": grokMissingUsageMessage,
		},
	})
	headers := http.Header{}
	if requestID := strings.TrimSpace(upstreamRequestID); requestID != "" {
		headers.Set("x-request-id", requestID)
	}
	return &UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    body,
		ResponseHeaders: headers,
	}
}
