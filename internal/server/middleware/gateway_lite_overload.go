package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// GatewayLiteOverloadLimiter 给单个轻量网关实例设置明确的接单边界。
// 槽位包住完整 handler 生命周期，所以 SSE（事件流）和 WebSocket（长连接）
// 都会在真实连接结束后才释放，避免只限制了鉴权阶段却没有限制上游连接。
type GatewayLiteOverloadLimiter struct {
	enabled    bool
	slots      chan struct{}
	maxWaiting int64
	wait       time.Duration

	inflight atomic.Int64
	waiting  atomic.Int64
	accepted atomic.Uint64
	rejected atomic.Uint64
	waited   atomic.Uint64
	waitMS   atomic.Uint64
	waitMax  atomic.Uint64
}

func NewGatewayLiteOverloadLimiter(enabled bool, maxInflight, maxWaiting int, wait time.Duration) *GatewayLiteOverloadLimiter {
	if maxInflight <= 0 {
		maxInflight = 1
	}
	if maxWaiting < 0 {
		maxWaiting = 0
	}
	if wait <= 0 {
		wait = time.Second
	}
	return &GatewayLiteOverloadLimiter{
		enabled:    enabled,
		slots:      make(chan struct{}, maxInflight),
		maxWaiting: int64(maxWaiting),
		wait:       wait,
	}
}

func (l *GatewayLiteOverloadLimiter) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if l == nil || !l.enabled || !isGatewayLiteModelRequest(c.Request.URL.Path) {
			c.Next()
			return
		}
		acquired, waitDuration := l.acquire(c.Request.Context())
		if !acquired {
			if c.Request.Context().Err() != nil {
				c.Abort()
				return
			}
			l.rejected.Add(1)
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"type":    "gateway_overloaded",
					"code":    "gateway_overloaded",
					"message": "网关当前请求过多，请稍后重试",
				},
			})
			return
		}
		// 暴露进入业务处理前的过载排队时间，避免把这段时间误判为鉴权或上游慢。
		c.Header("X-Gateway-Overload-Wait-Ms", strconv.FormatInt(waitDuration.Milliseconds(), 10))
		l.accepted.Add(1)
		l.inflight.Add(1)
		defer func() {
			l.inflight.Add(-1)
			<-l.slots
		}()
		c.Next()
	}
}

func (l *GatewayLiteOverloadLimiter) acquire(ctx context.Context) (bool, time.Duration) {
	select {
	case l.slots <- struct{}{}:
		return true, 0
	default:
	}
	if l.maxWaiting == 0 || l.waiting.Add(1) > l.maxWaiting {
		if l.maxWaiting > 0 {
			l.waiting.Add(-1)
		}
		return false, 0
	}
	defer l.waiting.Add(-1)
	l.waited.Add(1)
	startedAt := time.Now()
	timer := time.NewTimer(l.wait)
	defer timer.Stop()
	var acquired bool
	select {
	case l.slots <- struct{}{}:
		acquired = true
	case <-ctx.Done():
	case <-timer.C:
	}
	waitDuration := time.Since(startedAt)
	waitMS := uint64(waitDuration.Milliseconds())
	l.waitMS.Add(waitMS)
	for current := l.waitMax.Load(); waitMS > current && !l.waitMax.CompareAndSwap(current, waitMS); current = l.waitMax.Load() {
	}
	return acquired, waitDuration
}

// RuntimeMetrics 会合并进 gateway-lite 的定时健康上报，供主站观察是否需要扩容。
func (l *GatewayLiteOverloadLimiter) RuntimeMetrics(ctx context.Context) map[string]any {
	if l == nil || (ctx != nil && ctx.Err() != nil) {
		return map[string]any{}
	}
	return map[string]any{
		"overload_enabled":         l.enabled,
		"overload_inflight":        l.inflight.Load(),
		"overload_waiting":         l.waiting.Load(),
		"overload_accepted_total":  l.accepted.Load(),
		"overload_rejected_total":  l.rejected.Load(),
		"overload_waited_total":    l.waited.Load(),
		"overload_wait_ms_total":   l.waitMS.Load(),
		"overload_wait_ms_max":     l.waitMax.Load(),
		"overload_max_inflight":    cap(l.slots),
		"overload_max_waiting":     l.maxWaiting,
		"overload_wait_timeout_ms": l.wait.Milliseconds(),
	}
}

func isGatewayLiteModelRequest(requestPath string) bool {
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "/models" {
		return true
	}
	prefixes := [...]string{
		"/v1/",
		"/v1beta/",
		"/responses",
		"/messages",
		"/chat/",
		"/embeddings",
		"/images/",
		"/videos/",
		"/alpha/",
		"/backend-api/codex/",
		"/antigravity/",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(requestPath, prefix) {
			return true
		}
	}
	return false
}
