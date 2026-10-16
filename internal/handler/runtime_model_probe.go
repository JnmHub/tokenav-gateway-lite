package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

// ProbeRuntimeModel 暴露给 gateway-lite（轻量网关）运行时健康监控使用。
// 该入口只做真实上游账号池测活，不经过用户 API Key（接口密钥）鉴权和计费。
func (h *OpenAIGatewayHandler) ProbeRuntimeModel(ctx context.Context, preferredModel string) gatewaylite.RuntimeModelProbeResult {
	started := time.Now()
	if h == nil || h.gatewayService == nil {
		return gatewaylite.RuntimeModelProbeResult{
			OK:      false,
			Target:  "model-chat-completions",
			Error:   "openai gateway handler not ready",
			Latency: time.Since(started),
		}
	}
	return h.gatewayService.ProbeRuntimeModel(ctx, preferredModel)
}
