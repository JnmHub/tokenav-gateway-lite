package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

func WithResolvedTargetPlatform(ctx context.Context, platform string) context.Context {
	platform = strings.TrimSpace(platform)
	if ctx == nil || platform == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.ResolvedTargetPlatform, platform)
}

// WithCompositeRouteDecision 把一次路由决定固化到 context（上下文）。后续链路只能读取，
// 不再重复推断，以保证实际转发模型和计费模型完全一致。
func WithCompositeRouteDecision(ctx context.Context, decision CompositeRouteDecision) context.Context {
	if ctx == nil || !decision.Matched {
		return ctx
	}
	if platform := strings.TrimSpace(decision.TargetPlatform); platform != "" {
		ctx = context.WithValue(ctx, ctxkey.ResolvedTargetPlatform, platform)
	}
	if model := strings.TrimSpace(decision.UpstreamModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.ResolvedUpstreamModel, model)
	}
	if model := strings.TrimSpace(decision.PublicModel); model != "" {
		ctx = context.WithValue(ctx, ctxkey.RequestedPublicModel, model)
	}
	if source := strings.TrimSpace(decision.Source); source != "" {
		ctx = context.WithValue(ctx, ctxkey.CompositeRouteSource, source)
	}
	return ctx
}

func ResolvedTargetPlatformFromContext(ctx context.Context) (string, bool) {
	return compositeContextString(ctx, ctxkey.ResolvedTargetPlatform)
}

func ResolvedUpstreamModelFromContext(ctx context.Context) (string, bool) {
	return compositeContextString(ctx, ctxkey.ResolvedUpstreamModel)
}

func RequestedPublicModelFromContext(ctx context.Context) (string, bool) {
	return compositeContextString(ctx, ctxkey.RequestedPublicModel)
}

func CompositeRouteSourceFromContext(ctx context.Context) (string, bool) {
	return compositeContextString(ctx, ctxkey.CompositeRouteSource)
}

func compositeContextString(ctx context.Context, key ctxkey.Key) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, ok := ctx.Value(key).(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

// DetectModelPlatform 只识别明确模型前缀；模糊名称必须失败关闭，不能猜错平台后串价。
func DetectModelPlatform(model string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return "", false
	}
	normalized = strings.TrimPrefix(normalized, "models/")
	if slash := strings.IndexByte(normalized, '/'); slash > 0 {
		provider := strings.TrimSpace(normalized[:slash])
		rest := strings.TrimSpace(normalized[slash+1:])
		switch provider {
		case "anthropic", "claude":
			return PlatformAnthropic, true
		case "openai", "chatgpt":
			return PlatformOpenAI, true
		case "google", "google-ai-studio", "gemini":
			return PlatformGemini, true
		case "xai", "x-ai", "grok":
			return PlatformGrok, true
		case "kimi", "moonshot":
			return PlatformKimi, true
		case "zhipu", "glm", "bigmodel":
			return PlatformZhipu, true
		case "deepseek":
			return PlatformDeepseek, true
		}
		if rest != "" {
			normalized = strings.TrimPrefix(rest, "models/")
		}
	}
	switch {
	case strings.HasPrefix(normalized, "anthropic.claude-"), strings.HasPrefix(normalized, "claude-"):
		return PlatformAnthropic, true
	case strings.HasPrefix(normalized, "gpt-"), strings.HasPrefix(normalized, "chatgpt-"),
		strings.HasPrefix(normalized, "codex-"), strings.HasPrefix(normalized, "text-embedding-"),
		strings.HasPrefix(normalized, "text-moderation-"), strings.HasPrefix(normalized, "omni-moderation-"),
		strings.HasPrefix(normalized, "dall-e-"), strings.HasPrefix(normalized, "gpt-image-"),
		strings.HasPrefix(normalized, "tts-"), strings.HasPrefix(normalized, "whisper-"), hasOpenAISeriesPrefix(normalized):
		return PlatformOpenAI, true
	case strings.HasPrefix(normalized, "gemini-"), strings.HasPrefix(normalized, "learnlm-"):
		return PlatformGemini, true
	case normalized == "grok" || strings.HasPrefix(normalized, "grok-"):
		return PlatformGrok, true
	case normalized == "k3", normalized == "k3-256k",
		strings.HasPrefix(normalized, "kimi-"), strings.HasPrefix(normalized, "moonshot-"):
		return PlatformKimi, true
	case strings.HasPrefix(normalized, "glm-"):
		return PlatformZhipu, true
	case strings.HasPrefix(normalized, "deepseek-"):
		return PlatformDeepseek, true
	default:
		return "", false
	}
}

func hasOpenAISeriesPrefix(model string) bool {
	for _, prefix := range []string{"o1", "o3", "o4", "o5"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	return false
}

func isConcreteRequestPlatform(platform string) bool {
	switch platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok,
		PlatformKimi, PlatformZhipu, PlatformDeepseek:
		return true
	default:
		return false
	}
}

func (s *GatewayService) resolveCompositeRouteDecision(ctx context.Context, group *Group, requestedModel, endpoint string) (CompositeRouteDecision, bool, error) {
	if group == nil || group.Platform != PlatformComposite {
		return CompositeRouteDecision{}, false, nil
	}
	if platform, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		upstreamModel := requestedModel
		if resolvedModel, modelOK := ResolvedUpstreamModelFromContext(ctx); modelOK {
			upstreamModel = resolvedModel
		}
		publicModel := requestedModel
		if requested, requestedOK := RequestedPublicModelFromContext(ctx); requestedOK {
			publicModel = requested
		}
		source := CompositeRouteSourceDetector
		if resolvedSource, sourceOK := CompositeRouteSourceFromContext(ctx); sourceOK {
			source = resolvedSource
		}
		return CompositeRouteDecision{Matched: true, Source: source, GroupID: group.ID, PublicModel: publicModel,
			TargetPlatform: platform, UpstreamModel: upstreamModel, Endpoint: normalizeCompositeRouteEndpoint(endpoint)}, true, nil
	}
	resolver := s.compositeResolver
	if resolver == nil {
		resolver = NewCompositeRouteResolver(nil)
	}
	decision, err := resolver.Resolve(ctx, group.ID, requestedModel, endpoint)
	if err != nil {
		return decision, false, err
	}
	return decision, decision.Matched, nil
}
