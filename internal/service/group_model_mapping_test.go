//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGroupModelMappingDeterministic(t *testing.T) {
	mapping := map[string]string{
		"gpt-*":     "gpt-5.5",
		"gpt-5.*":   "gpt-5.6-sol",
		"gpt-5.4":   "gpt-5.6-sol-exact",
		"unrelated": "other",
	}
	model, mapped := ResolveGroupModelMapping(mapping, "GPT-5.4")
	require.True(t, mapped)
	require.Equal(t, "gpt-5.6-sol-exact", model)
	model, mapped = ResolveGroupModelMapping(mapping, "gpt-5.3")
	require.True(t, mapped)
	require.Equal(t, "gpt-5.6-sol", model)
}

func TestResolveGroupModelForSelectionAppliesOnlyOnceWithinGroup(t *testing.T) {
	group := &Group{ID: 7, ModelMapping: map[string]string{
		"customer-alias": "gpt-5.6-sol",
		"gpt-5.6-sol":    "gpt-5.6-sol-upstream",
	}}
	ctx, first := resolveGroupModelForSelection(context.Background(), group, "customer-alias")
	_, nested := resolveGroupModelForSelection(ctx, group, first)
	require.Equal(t, "gpt-5.6-sol", first)
	require.Equal(t, first, nested, "nested selectors must not apply a second mapping step")
}

func TestResolveGroupModelForSelectionReevaluatesAfterGroupChange(t *testing.T) {
	firstGroup := &Group{ID: 7, ModelMapping: map[string]string{"customer-alias": "gpt-5.6-sol"}}
	secondGroup := &Group{ID: 8, ModelMapping: map[string]string{"customer-alias": "gpt-5.5"}}
	ctx, _ := resolveGroupModelForSelection(context.Background(), firstGroup, "customer-alias")
	_, target := resolveGroupModelForSelection(ctx, secondGroup, "customer-alias")
	require.Equal(t, "gpt-5.5", target, "keyword rerouting must use the destination group's mapping")
}

func TestNormalizeGroupModelMappingRejectsUnsafeRules(t *testing.T) {
	_, err := NormalizeGroupModelMapping(PlatformOpenAI, map[string]string{"gpt-5.4": "claude-sonnet-4-6"})
	require.ErrorContains(t, err, "does not belong")
	_, err = NormalizeGroupModelMapping(PlatformOpenAI, map[string]string{"gpt-5.4": "gpt-5.5", "gpt-5.5": "gpt-5.4"})
	require.ErrorContains(t, err, "cycle")
	_, err = NormalizeGroupModelMapping(PlatformOpenAI, map[string]string{"gpt-*oops*": "gpt-5.5"})
	require.ErrorContains(t, err, "trailing wildcard")
}

func TestNormalizeGroupModelMappingAllowsAliasesAndTrailingWildcard(t *testing.T) {
	mapping, err := NormalizeGroupModelMapping(PlatformOpenAI, map[string]string{
		" My-Alias ": "gpt-5.6-sol",
		"GPT-5.*":    "gpt-5.6-sol",
	})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"my-alias": "gpt-5.6-sol", "gpt-5.*": "gpt-5.6-sol"}, mapping)
}
