package service

import (
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const maxPersistedSessionIDLength = 255

var clientSessionIDHeaders = append(
	append([]string(nil), explicitOpenAIHeaderSessionNames...),
	claudeCodeSessionHeader,
)

// ExtractClientSessionID 只读取客户端明确提供的会话头。它只进入用量审计，
// 不读取 prompt_cache_key，不改变 sticky（粘性调度）、request_id 或计费幂等。
func ExtractClientSessionID(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	for _, header := range clientSessionIDHeaders {
		if sessionID := sanitizeSessionID(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	if isGrokRequestContext(c) {
		return sanitizeSessionID(c.GetHeader(grokConversationIDHeader))
	}
	return ""
}

func sanitizeSessionID(raw string) string {
	if !utf8.ValidString(raw) {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	count := 0
	for _, char := range trimmed {
		if char < 0x20 || char == 0x7f {
			return ""
		}
		count++
		if count > maxPersistedSessionIDLength {
			return ""
		}
	}
	return trimmed
}

// [v0.2.5 新增]
func ClaudeCodeSessionIDFromHeader(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return sanitizeSessionID(c.GetHeader(claudeCodeSessionHeader))
}
