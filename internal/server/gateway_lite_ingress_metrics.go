package server

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/ingress"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
)

// 这两个 provider（指标提供器）在上报时动态读取当前实例。
// runtime monitor（运行监控器）创建早于路由注册，不能在启动阶段缓存 nil limiter。
type gatewayLiteIngressRuntimeMetricsProvider struct{}

func (gatewayLiteIngressRuntimeMetricsProvider) RuntimeMetrics(context.Context) map[string]any {
	return ingress.RuntimeMetrics()
}

type gatewayLitePanelRateLimitRuntimeMetricsProvider struct{}

func (gatewayLitePanelRateLimitRuntimeMetricsProvider) RuntimeMetrics(ctx context.Context) map[string]any {
	if limiter := servermiddleware.ActiveGatewayLitePanelRateLimiter(); limiter != nil {
		return limiter.RuntimeMetrics(ctx)
	}
	return map[string]any{
		"panel_rate_limit_enabled": false,
		"panel_rate_limit_status":  "not_initialized",
	}
}
