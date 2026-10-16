package service

import (
	"context"
	"strings"
)

// EffectiveAccountMappingGroupID returns the group that is actually active for
// the request. The context is authoritative because keyword moderation and
// fallback routing can replace the API key's original group after admission.
// The explicit fallback is kept for protocol adapters and legacy callers that
// do not carry a validated group object in their context.
func EffectiveAccountMappingGroupID(ctx context.Context, fallback *int64) *int64 {
	if groupID := AccountGroupIDFromRequestContext(ctx); groupID != nil {
		return groupID
	}
	if fallback != nil && *fallback > 0 {
		id := *fallback
		return &id
	}
	return nil
}

// ResolveAccountModelForRequest applies an account's group-specific mapping
// before its account-wide fallback mapping. All forwarding paths use this
// helper so a request cannot silently use a mapping from a different group.
func ResolveAccountModelForRequest(ctx context.Context, fallback *int64, account *Account, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if account == nil || model == "" {
		return model, false
	}
	if groupID := EffectiveAccountMappingGroupID(ctx, fallback); groupID != nil {
		if target, ok := openAIGroupModelFallbackTargetForModel(ctx, *groupID, model); ok {
			model = target
		}
	}
	return account.ResolveModelForGroupID(EffectiveAccountMappingGroupID(ctx, fallback), model)
}

// MappedAccountModelForRequest is the string-only form used by request body
// rewriting. An empty mapping result is never allowed to erase the caller's
// model; unsupported-model decisions remain the scheduler's responsibility.
func MappedAccountModelForRequest(ctx context.Context, fallback *int64, account *Account, model string) string {
	model = strings.TrimSpace(model)
	mapped, _ := ResolveAccountModelForRequest(ctx, fallback, account, model)
	if strings.TrimSpace(mapped) == "" {
		return model
	}
	return strings.TrimSpace(mapped)
}

// ResolveOpenAIForwardModelForRequest preserves the legacy OpenAI compatibility
// fallback while making the account mapping group-aware. The default mapped
// model is only a Messages compatibility dispatch hint; it must not override an
// explicit account/group mapping.
func ResolveOpenAIForwardModelForRequest(ctx context.Context, fallback *int64, account *Account, requestedModel, defaultMappedModel string) string {
	requestedModel = strings.TrimSpace(requestedModel)
	if account == nil {
		if defaultMappedModel != "" && claudeMessagesDispatchFamily(requestedModel) != "" {
			return defaultMappedModel
		}
		return requestedModel
	}

	mappedModel, matched := ResolveAccountModelForRequest(ctx, fallback, account, requestedModel)
	if !matched && defaultMappedModel != "" && claudeMessagesDispatchFamily(requestedModel) != "" {
		return defaultMappedModel
	}
	return mappedModel
}
