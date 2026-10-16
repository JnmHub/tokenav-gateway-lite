package routes

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const readinessTimeout = 2 * time.Second

// ReadinessProbe describes one dependency required before the process should
// receive user traffic. Check errors are reduced to a status so connection
// details and credentials never leak through the public route.
type ReadinessProbe struct {
	Name  string
	Check func(context.Context) error
}

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine, readinessProbes ...ReadinessProbe) {
	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "gateway-lite",
			"health":  "/health",
			"ready":   "/ready",
			"api":     "/v1",
		})
	})

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 就绪检查：关键依赖不可用时停止接收新流量，但不影响 /health 的进程存活判断。
	r.GET("/ready", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), readinessTimeout)
		defer cancel()

		checks, ready := runReadinessProbes(ctx, readinessProbes)
		statusCode := http.StatusOK
		status := "ready"
		if !ready {
			statusCode = http.StatusServiceUnavailable
			status = "not_ready"
		}
		c.JSON(statusCode, gin.H{
			"status": status,
			"checks": checks,
		})
	})

	// Claude Code 遥测日志（忽略，直接返回200）
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}

type readinessProbeResult struct {
	name string
	err  error
}

func runReadinessProbes(ctx context.Context, probes []ReadinessProbe) (map[string]string, bool) {
	checks := make(map[string]string, len(probes))
	if len(probes) == 0 {
		return checks, true
	}

	results := make(chan readinessProbeResult, len(probes))
	pending := make(map[string]struct{}, len(probes))
	for index, probe := range probes {
		name := uniqueReadinessProbeName(probe.Name, index, pending)
		pending[name] = struct{}{}
		check := probe.Check
		go func() {
			var err error
			if check == nil {
				err = context.Canceled
			} else {
				err = check(ctx)
			}
			results <- readinessProbeResult{name: name, err: err}
		}()
	}

	ready := true
	for len(pending) > 0 {
		select {
		case result := <-results:
			if _, ok := pending[result.name]; !ok {
				continue
			}
			delete(pending, result.name)
			if result.err != nil {
				checks[result.name] = "unavailable"
				ready = false
			} else {
				checks[result.name] = "ok"
			}
		case <-ctx.Done():
			for name := range pending {
				checks[name] = "timeout"
			}
			return checks, false
		}
	}
	return checks, ready
}

func uniqueReadinessProbeName(raw string, index int, existing map[string]struct{}) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = "dependency"
	}
	if _, ok := existing[name]; !ok {
		return name
	}
	for suffix := index + 1; ; suffix++ {
		candidate := name + "_" + strconv.Itoa(suffix)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}
