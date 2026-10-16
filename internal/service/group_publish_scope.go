package service

import "context"

type publishableGroupsOnlyContextKey struct{}

// ContextWithPublishableGroupsOnly marks a main-site mirror/list request.
// Local gateway administration deliberately omits this marker so local-only
// groups remain manageable only from the gateway itself.
func ContextWithPublishableGroupsOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, publishableGroupsOnlyContextKey{}, true)
}

// PublishableGroupsOnly reports whether local-only groups must be excluded at
// the database query boundary so pagination totals cannot leak their presence.
func PublishableGroupsOnly(ctx context.Context) bool {
	value, _ := ctx.Value(publishableGroupsOnlyContextKey{}).(bool)
	return value
}
