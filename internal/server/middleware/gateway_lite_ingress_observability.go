package middleware

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	"github.com/gin-gonic/gin"
)

// GatewayLiteApplicationHeaderLimitBytes 是未显式传值时的兼容默认值，
// 与 server.max_header_bytes 和官方 v0.1.169 的默认值保持一致。
const GatewayLiteApplicationHeaderLimitBytes int64 = 64 << 10

// GatewayLiteIngressObservability 只观察轻量网关的模型、鉴权入口和管理 API。
// 静态资源、公开页面与健康检查不进入分母，避免前端访问量稀释真实拒绝率。
// 热路径只增加两个 atomic（原子）计数；拒绝分类本身不保存 path、IP 或正文。
func GatewayLiteIngressObservability(maxHeaderBytes int) gin.HandlerFunc {
	return gatewayLiteIngressObservability(int64(maxHeaderBytes), ingress.Default())
}

func gatewayLiteIngressObservability(maxHeaderBytes int64, aggregator *ingress.Aggregator) gin.HandlerFunc {
	if maxHeaderBytes <= 0 {
		maxHeaderBytes = GatewayLiteApplicationHeaderLimitBytes
	}
	if aggregator == nil {
		aggregator = ingress.Default()
	}
	return func(c *gin.Context) {
		observedPath := gatewayLiteObservedIngressPath(c.Request)
		if gatewayLiteRequestHeaderTooLarge(c.Request, maxHeaderBytes) {
			// 超大头不读取、不保存任何 header 值。即使请求目标不是常规模型/后台
			// 路径，也把这一次异常请求纳入分母，保证拒绝率不会超过 100%。
			aggregator.ObserveRequest()
			aggregator.Record(ingress.RejectHeaderTooLarge)
			c.AbortWithStatusJSON(http.StatusRequestHeaderFieldsTooLarge, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"code":    "request_header_too_large",
					"message": "Request header is too large",
				},
			})
			return
		}
		if !observedPath {
			c.Next()
			return
		}

		aggregator.ObserveRequest()
		c.Next()

		// 只有 Gin 没有匹配到任何路由时才算非法路径。合法模型路由返回的
		// 上游 404 不得误记为入口攻击或客户端路径错误。
		if c.Writer.Status() == http.StatusNotFound && strings.TrimSpace(c.FullPath()) == "" {
			aggregator.Record(ingress.RejectInvalidPath)
		}
	}
}

// gatewayLiteRequestHeaderTooLarge 只累计序列化后的 header 名称和值长度，
// 不拼接字符串、不复制值，也不会把内容交给日志或指标系统。
func gatewayLiteRequestHeaderTooLarge(req *http.Request, maxBytes int64) bool {
	if req == nil || maxBytes <= 0 {
		return false
	}
	total := int64(2) // header 末尾空行 CRLF
	add := func(size int) bool {
		if size < 0 || int64(size) > maxBytes-total {
			return false
		}
		total += int64(size)
		return total <= maxBytes
	}
	addLine := func(name, value string) bool {
		// "Name: Value\r\n"
		return add(len(name)) && add(2) && add(len(value)) && add(2)
	}
	if req.Host != "" && !addLine("Host", req.Host) {
		return true
	}
	for name, values := range req.Header {
		if len(values) == 0 {
			if !addLine(name, "") {
				return true
			}
			continue
		}
		for _, value := range values {
			if !addLine(name, value) {
				return true
			}
		}
	}
	return total > maxBytes
}

func gatewayLiteObservedIngressPath(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	path := strings.ToLower(strings.TrimSpace(req.URL.Path))
	for _, prefix := range []string{
		"/api/v1/admin",
		"/api/v1/auth",
		"/v1",
		"/v1beta",
		"/models",
		"/backend-api/codex",
		"/responses",
		"/chat/completions",
		"/messages",
		"/embeddings",
		"/images",
		"/videos",
		"/alpha/search",
		"/antigravity",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
