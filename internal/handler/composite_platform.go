package handler

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ensureCompositeTargetPlatform 是处理器的兜底：正常请求由路由中间件写入完整
// 决策；单元测试或绕过标准路由的调用仍可按明确模型前缀解析，未知模型绝不猜测。
func ensureCompositeTargetPlatform(c *gin.Context, apiKey *service.APIKey, model string) {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
		return
	}
	if _, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
		return
	}
	if value, exists := c.Get(service.CompositeRouteResolverGinContextKey); exists && apiKey.Group.ID > 0 {
		if resolver, ok := value.(*service.CompositeRouteResolver); ok && resolver != nil {
			decision, err := resolver.Resolve(c.Request.Context(), apiKey.Group.ID, model, compositeHandlerEndpoint(c))
			if err == nil && decision.Matched {
				c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), decision))
				return
			}
		}
	}
	if platform, ok := service.DetectModelPlatform(model); ok {
		c.Request = c.Request.WithContext(service.WithResolvedTargetPlatform(c.Request.Context(), platform))
	}
}

func compositeHandlerEndpoint(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return service.CompositeRouteEndpointAny
	}
	path := c.Request.URL.Path
	switch {
	case strings.Contains(path, "/messages/count_tokens"):
		return service.CompositeRouteEndpointCountTokens
	case strings.Contains(path, "/messages"):
		return service.CompositeRouteEndpointMessages
	case strings.Contains(path, "/responses"):
		return service.CompositeRouteEndpointResponses
	case strings.Contains(path, "/chat/completions"):
		return service.CompositeRouteEndpointChatCompletions
	case strings.Contains(path, "/embeddings"):
		return service.CompositeRouteEndpointEmbeddings
	case strings.Contains(path, "/images/"):
		return service.CompositeRouteEndpointImages
	case strings.Contains(path, "/v1beta/"):
		return service.CompositeRouteEndpointGemini
	default:
		return service.CompositeRouteEndpointAny
	}
}

func compositeTargetPlatformAllowed(c *gin.Context, apiKey *service.APIKey, model string, allowed ...string) bool {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
		return true
	}
	ensureCompositeTargetPlatform(c, apiKey, model)
	platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	if !ok {
		return false
	}
	for _, allowedPlatform := range allowed {
		if platform == allowedPlatform {
			return true
		}
	}
	return false
}

// geminiNativeEndpointAllowed 统一 Gemini 原生协议的分组校验。聚合分组不能只看
// group.platform，因为它的真实平台由本次模型路由决定；同时必须失败关闭，避免把
// 映射到 OpenAI 等其他平台的模型误送进 Gemini 账号池。
func geminiNativeEndpointAllowed(c *gin.Context, apiKey *service.APIKey, model string) bool {
	if middleware.HasForcePlatform(c) {
		return true
	}
	if apiKey == nil || apiKey.Group == nil {
		return false
	}
	switch apiKey.Group.Platform {
	case service.PlatformGemini:
		return true
	case service.PlatformComposite:
		return compositeTargetPlatformAllowed(c, apiKey, model, service.PlatformGemini)
	default:
		return false
	}
}

func compositeTargetPlatformResolved(c *gin.Context, apiKey *service.APIKey, model string) bool {
	if c == nil || c.Request == nil || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformComposite {
		return true
	}
	ensureCompositeTargetPlatform(c, apiKey, model)
	_, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
	return ok
}

func effectiveAPIKeyPlatform(c *gin.Context, apiKey *service.APIKey) string {
	if c != nil && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform
		}
	}
	if apiKey == nil || apiKey.Group == nil {
		return ""
	}
	return apiKey.Group.Platform
}

func openAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey) (string, []service.ReasoningEffortMapping, bool) {
	if apiKey == nil || apiKey.Group == nil {
		return "", nil, false
	}
	if apiKey.Group.Platform != service.PlatformOpenAI && apiKey.Group.Platform != service.PlatformComposite {
		return "", nil, false
	}
	if effectiveAPIKeyPlatform(c, apiKey) != service.PlatformOpenAI {
		return "", nil, false
	}
	return apiKey.Group.MaxReasoningEffort, apiKey.Group.ReasoningEffortMappings, true
}

func applyOpenAIReasoningEffortPolicyForRequest(c *gin.Context, apiKey *service.APIKey, body []byte) ([]byte, bool) {
	if c != nil && c.Request != nil {
		if requested := service.ExtractRequestedOpenAIReasoningEffort(body); requested != nil {
			c.Request = c.Request.WithContext(gatewaylite.ContextWithReasoningEffortAudit(c.Request.Context(), *requested, ""))
		}
	}
	maxEffort, mappings, ok := openAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return body, false
	}
	return service.ApplyOpenAIReasoningEffortPolicy(body, maxEffort, mappings)
}

func bindOpenAIReasoningEffortPolicyForMessagesRequest(c *gin.Context, apiKey *service.APIKey, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	// Messages 转换器会为部分请求生成上游默认档位。只有客户端明确传入
	// output_config.effort 时才绑定分组策略，不能把上游默认值误当成用户选择。
	effort := gjson.GetBytes(body, "output_config.effort")
	if !effort.Exists() || effort.Type != gjson.String || strings.TrimSpace(effort.String()) == "" {
		return
	}
	requested := service.NormalizeReasoningEffortAudit(effort.String())
	if requested != "" {
		c.Request = c.Request.WithContext(gatewaylite.ContextWithReasoningEffortAudit(c.Request.Context(), requested, ""))
	}
	maxEffort, mappings, ok := openAIReasoningEffortPolicyForRequest(c, apiKey)
	if !ok {
		return
	}
	c.Request = c.Request.WithContext(service.WithOpenAIReasoningEffortPolicy(c.Request.Context(), maxEffort, mappings))
}

func openAICompatibleRequestPlatformFromContext(ctx context.Context, apiKey *service.APIKey) string {
	if platform, ok := service.ResolvedTargetPlatformFromContext(ctx); ok {
		switch platform {
		case service.PlatformGrok, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek:
			return platform
		}
		return service.PlatformOpenAI
	}
	if apiKey != nil && apiKey.Group != nil {
		switch apiKey.Group.Platform {
		case service.PlatformGrok, service.PlatformKimi, service.PlatformZhipu, service.PlatformDeepseek:
			return apiKey.Group.Platform
		}
	}
	return service.PlatformOpenAI
}
