package service

import "context"

type openAIForwardModelContextKey struct{}

type openAIForwardModel struct {
	model                  string
	useCompactModelMapping bool
}

// WithOpenAIForwardModel records the model already rewritten by group mapping.
// Native compaction v2 must keep ordinary account mapping semantics; only the
// legacy /responses/compact route may use the compact-only model mapping.
func WithOpenAIForwardModel(ctx context.Context, forwardModel string, useCompactModelMapping bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIForwardModelContextKey{}, openAIForwardModel{
		model:                  forwardModel,
		useCompactModelMapping: useCompactModelMapping,
	})
}

func openAIForwardModelFromContext(ctx context.Context) (openAIForwardModel, bool) {
	if ctx == nil {
		return openAIForwardModel{}, false
	}
	forwardModel, ok := ctx.Value(openAIForwardModelContextKey{}).(openAIForwardModel)
	return forwardModel, ok
}
