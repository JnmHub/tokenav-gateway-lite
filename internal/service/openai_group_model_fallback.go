package service

import (
	"context"
	"fmt"
	"strings"
)

const openAIGroupFallbackModelMaxBytes = 100

func normalizeOpenAIGroupFallbackModel(platform, raw string) (string, error) {
	model := strings.TrimSpace(raw)
	if len(model) > openAIGroupFallbackModelMaxBytes {
		return "", fmt.Errorf("fallback model must be at most %d bytes", openAIGroupFallbackModelMaxBytes)
	}
	if model != "" && normalizeOpenAICompatiblePlatform(platform) != PlatformOpenAI {
		return "", fmt.Errorf("fallback model is only supported by OpenAI groups")
	}
	return model, nil
}

type openAIGroupModelFallbackContextKey struct{}
type openAIGroupModelFallbackEnabledContextKey struct{}
type openAIGroupModelFallbackAttemptedContextKey struct{}
type openAIGroupModelFallbackSuppressedContextKey struct{}
type openAIGroupModelFallbackOnlyContextKey struct{}

// openAIGroupModelFallbackSelection stores the public model and the final
// group target separately. Keeping both values lets forwarding preserve the
// client's model while account mapping uses the fallback target.
type openAIGroupModelFallbackSelection struct {
	GroupID       int64
	OriginalModel string
	GroupModel    string
	TargetModel   string
}

// WithOpenAIGroupModelFallbackEnabled opts a text request into the group
// fallback policy. The opt-in keeps images, embeddings and WebSocket paths
// unchanged until they have their own protocol-specific verification.
func WithOpenAIGroupModelFallbackEnabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIGroupModelFallbackEnabledContextKey{}, true)
}

// WithoutOpenAIGroupModelFallbackPolicy disables automatic same-group model
// fallback for the current request. Cross-group failover uses this on the
// initial group so the configured order can be honored (original model in the
// fallback group first, then the original group's fallback model).
func WithoutOpenAIGroupModelFallbackPolicy(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIGroupModelFallbackSuppressedContextKey{}, true)
}

// WithOpenAIGroupModelFallbackPolicy re-enables same-group model fallback
// after a cross-group attempt has been exhausted.
func WithOpenAIGroupModelFallbackPolicy(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIGroupModelFallbackSuppressedContextKey{}, false)
}

func openAIGroupModelFallbackPolicySuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	suppressed, _ := ctx.Value(openAIGroupModelFallbackSuppressedContextKey{}).(bool)
	return suppressed
}

// WithOpenAIGroupModelFallbackOnly forces the next scheduler pass to skip the
// original model and try only this group's configured fallback model. It is
// used after a cross-group target has also failed, so A is not retried from the
// beginning before reaching its fallback model.
func WithOpenAIGroupModelFallbackOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIGroupModelFallbackOnlyContextKey{}, true)
}

func openAIGroupModelFallbackOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	forced, _ := ctx.Value(openAIGroupModelFallbackOnlyContextKey{}).(bool)
	return forced
}

func openAIGroupModelFallbackEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(openAIGroupModelFallbackEnabledContextKey{}).(bool)
	return enabled
}

// WithOpenAIGroupModelFallback records a request-scoped fallback decision.
// It must only be set after the scheduler has verified the target account.
func WithOpenAIGroupModelFallback(ctx context.Context, groupID int64, originalModel, groupModel, targetModel string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if groupID <= 0 || strings.TrimSpace(targetModel) == "" {
		return ctx
	}
	return context.WithValue(ctx, openAIGroupModelFallbackContextKey{}, openAIGroupModelFallbackSelection{
		GroupID:       groupID,
		OriginalModel: strings.TrimSpace(originalModel),
		GroupModel:    strings.TrimSpace(groupModel),
		TargetModel:   strings.TrimSpace(targetModel),
	})
}

// WithoutOpenAIGroupModelFallback clears the forwarding target before a
// failover loop selects another account, while retaining an attempted marker.
// This prevents a failed fallback account from being retried on every loop and
// prevents the next original-model account from inheriting the fallback target.
func WithoutOpenAIGroupModelFallback(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, active := openAIGroupModelFallbackFromContext(ctx); !active {
		return ctx
	}
	ctx = context.WithValue(ctx, openAIGroupModelFallbackContextKey{}, nil)
	return context.WithValue(ctx, openAIGroupModelFallbackAttemptedContextKey{}, true)
}

// clearOpenAIGroupModelFallbackValue 清除请求内已生效的分组转发目标值与
// attempted 标记。智能分流候选全部无可用账号时用它复位上下文，让分组兜底
// 模型（FallbackModel）仍能按原有语义承接最后一次尝试；attempted 标记是
// 为"单个兜底账号失败后不再重试"设计的，对多目标分流场景会把最后的
// FallbackModel 也一起挡掉，所以这里必须一并清除。
// 与 WithoutOpenAIGroupModelFallback 的区别：后者只清值并保留 attempted，
// 彻底关闭本请求的兜底策略，那是故障转移循环换账号时才需要的行为。
func clearOpenAIGroupModelFallbackValue(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, openAIGroupModelFallbackContextKey{}, nil)
	return context.WithValue(ctx, openAIGroupModelFallbackAttemptedContextKey{}, false)
}

func openAIGroupModelFallbackFromContext(ctx context.Context) (openAIGroupModelFallbackSelection, bool) {
	if ctx == nil {
		return openAIGroupModelFallbackSelection{}, false
	}
	selection, ok := ctx.Value(openAIGroupModelFallbackContextKey{}).(openAIGroupModelFallbackSelection)
	if !ok || selection.GroupID <= 0 || strings.TrimSpace(selection.TargetModel) == "" {
		return openAIGroupModelFallbackSelection{}, false
	}
	return selection, true
}

func openAIGroupModelFallbackTargetForModel(ctx context.Context, groupID int64, model string) (string, bool) {
	selection, ok := openAIGroupModelFallbackFromContext(ctx)
	if !ok || selection.GroupID != groupID {
		return "", false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	for _, source := range []string{selection.OriginalModel, selection.GroupModel, selection.TargetModel} {
		if source != "" && strings.EqualFold(source, model) {
			return selection.TargetModel, true
		}
	}
	return "", false
}

func openAIGroupModelFallbackActiveForRequest(ctx context.Context) bool {
	if _, ok := openAIGroupModelFallbackFromContext(ctx); ok {
		return true
	}
	if ctx == nil {
		return false
	}
	attempted, _ := ctx.Value(openAIGroupModelFallbackAttemptedContextKey{}).(bool)
	return attempted
}
