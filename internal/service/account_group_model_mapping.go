package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// NormalizeAccountGroupModelMappings validates a complete set of mappings for
// one account edit. A mapping may only target a group that will remain bound to
// the account; this prevents an orphan rule from silently affecting a later
// request after group bindings change.
func NormalizeAccountGroupModelMappings(platform string, groupIDs []int64, input map[int64]map[string]string) (map[int64]map[string]string, error) {
	if input == nil {
		return nil, nil
	}
	allowed := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID > 0 {
			allowed[groupID] = struct{}{}
		}
	}
	normalized := make(map[int64]map[string]string, len(input))
	for groupID, mapping := range input {
		if groupID <= 0 {
			return nil, fmt.Errorf("account group model mapping has invalid group id %d", groupID)
		}
		if _, ok := allowed[groupID]; !ok {
			return nil, fmt.Errorf("account group model mapping targets unbound group %d", groupID)
		}
		value, err := NormalizeAccountGroupModelMapping(platform, mapping)
		if err != nil {
			return nil, fmt.Errorf("normalize account group %d model mapping: %w", groupID, err)
		}
		normalized[groupID] = cloneGroupModelMapping(value)
	}
	return normalized, nil
}

// NormalizeAccountGroupModelMapping validates the mapping stored on one
// account_groups binding. The account platform is used for the same provider
// boundary checks as ordinary group mappings.
func NormalizeAccountGroupModelMapping(platform string, input map[string]string) (map[string]string, error) {
	return NormalizeGroupModelMapping(platform, input)
}

// AccountGroupModelMapping returns the mapping for one concrete account/group
// binding. A copy is returned so callers cannot mutate a cached scheduler
// account and accidentally alter later requests.
func (a *Account) AccountGroupModelMapping(groupID int64) map[string]string {
	if a == nil || groupID <= 0 {
		return nil
	}
	for index := range a.AccountGroups {
		binding := &a.AccountGroups[index]
		if binding.GroupID != groupID || len(binding.ModelMapping) == 0 {
			continue
		}
		result := make(map[string]string, len(binding.ModelMapping))
		for source, target := range binding.ModelMapping {
			result[source] = target
		}
		return result
	}
	return nil
}

// ResolveModelForGroup resolves the selected group's account-specific mapping
// first, then falls back to the account-wide mapping. Callers pass the model
// after ordinary group mapping has run, keeping the request chain deterministic:
// requested -> group mapping -> account/group mapping -> account mapping fallback.
//
// The two account mapping scopes are intentionally alternatives rather than a
// recursive pipeline. Otherwise a global fallback could silently rewrite the
// explicit per-group target and make the administrator's routing choice opaque.
func (a *Account) ResolveModelForGroup(groupID int64, model string) (mappedModel string, matched bool) {
	model = strings.TrimSpace(model)
	if a == nil || model == "" {
		return model, false
	}
	if mapping := a.AccountGroupModelMapping(groupID); len(mapping) > 0 {
		if target, hit := resolveAccountGroupModelMapping(mapping, model); hit {
			return target, true
		}
	}
	return a.ResolveMappedModel(model)
}

// ResolveModelForGroupID is the nil-safe form used by HTTP/protocol adapters,
// where a request may be intentionally unbound to a fixed group.
func (a *Account) ResolveModelForGroupID(groupID *int64, model string) (mappedModel string, matched bool) {
	if groupID == nil || *groupID <= 0 {
		if a == nil {
			return strings.TrimSpace(model), false
		}
		return a.ResolveMappedModel(model)
	}
	return a.ResolveModelForGroup(*groupID, model)
}

// AccountGroupIDFromRequestContext returns the final routing group attached by
// authentication/rerouting middleware. The context value is authoritative for
// the current request: a control-plane API key may not carry SchedulerGroupID
// when a local keyword policy has selected a different group. Returning a copy
// keeps callers from retaining a pointer into the shared Group snapshot.
func AccountGroupIDFromRequestContext(ctx context.Context) *int64 {
	if ctx == nil {
		return nil
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if !IsGroupContextValid(group) {
		return nil
	}
	id := group.ID
	return &id
}

// ResolveModelForRequestContext resolves the account mapping using the final
// group in request context. It is the common entry point for protocol
// adapters, so ordinary keys without SchedulerGroupID still honor a
// group-specific account mapping.
func (a *Account) ResolveModelForRequestContext(ctx context.Context, model string) (mappedModel string, matched bool) {
	return a.ResolveModelForGroupID(AccountGroupIDFromRequestContext(ctx), model)
}

// IsModelSupportedForGroup keeps candidate validation aligned with forwarding.
// A dedicated binding mapping is an explicit administrator declaration that its
// source model is supported by this account. Models without a binding rule keep
// the existing account-wide capability behavior.
func (a *Account) IsModelSupportedForGroup(groupID int64, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if a == nil {
		return false
	}
	if mapping := a.AccountGroupModelMapping(groupID); len(mapping) > 0 {
		if target, hit := resolveAccountGroupModelMapping(mapping, model); hit {
			return accountPlatformAllowsModel(a, map[string]string{model: target}, model)
		}
	}
	return a.IsModelSupported(model)
}

// IsModelSupportedForGroupID is the nil-safe counterpart used by schedulers
// that also support ungrouped legacy accounts.
func (a *Account) IsModelSupportedForGroupID(groupID *int64, model string) bool {
	if groupID == nil || *groupID <= 0 {
		return a != nil && a.IsModelSupported(model)
	}
	return a.IsModelSupportedForGroup(*groupID, model)
}

// resolveAccountGroupModelMapping preserves a no-op binding rule as a real
// match. This is different from the ordinary group mapper: on an account
// binding, `sol -> sol` intentionally masks the account-wide `sol -> luna`
// fallback for only that group.
func resolveAccountGroupModelMapping(mapping map[string]string, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" || len(mapping) == 0 {
		return model, false
	}
	normalized := strings.ToLower(model)
	if target, matched := resolveRequestedModelInMapping(mapping, normalized); matched {
		target = strings.TrimSpace(target)
		if target != "" {
			return target, true
		}
	}
	return model, false
}
