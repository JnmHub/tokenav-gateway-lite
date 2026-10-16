package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeReasoningEffortPolicy(t *testing.T) {
	require.Equal(t, "xhigh", NormalizeMaxReasoningEffort("x-high"))
	require.Equal(t, "max", NormalizeMaxReasoningEffort(" MAX "))
	require.Empty(t, NormalizeMaxReasoningEffort("none"))

	mappings, err := NormalizeReasoningEffortMappings(PlatformOpenAI, []ReasoningEffortMapping{
		{From: " MAX ", To: "x-high"},
		{From: "minimal", To: "high"},
	})
	require.NoError(t, err)
	require.Equal(t, []ReasoningEffortMapping{{From: "max", To: "xhigh"}, {From: "minimal", To: "high"}}, mappings)

	_, err = NormalizeReasoningEffortMappings(PlatformOpenAI, []ReasoningEffortMapping{{From: "max", To: "low"}, {From: " MAX ", To: "high"}})
	require.ErrorContains(t, err, "duplicate")
	_, err = NormalizeReasoningEffortMappings(PlatformGrok, []ReasoningEffortMapping{{From: "low", To: "high"}})
	require.Error(t, err)
	_, err = NormalizeReasoningEffortMappings(PlatformComposite, []ReasoningEffortMapping{{From: "max", To: "high"}})
	require.NoError(t, err)
}

func TestOpenAIReasoningEffortPolicyContextRequiresExplicitBinding(t *testing.T) {
	body := []byte(`{"reasoning":{"effort":"max"}}`)
	got, changed := ApplyOpenAIReasoningEffortPolicyFromContext(context.Background(), body)
	require.False(t, changed)
	require.Equal(t, body, got)

	ctx := WithOpenAIReasoningEffortPolicy(context.Background(), "medium", []ReasoningEffortMapping{{From: "max", To: "xhigh"}})
	got, changed = ApplyOpenAIReasoningEffortPolicyFromContext(ctx, body)
	require.True(t, changed)
	require.Equal(t, "medium", gjson.GetBytes(got, "reasoning.effort").String())
}

func TestApplyOpenAIReasoningEffortPolicy(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		max      string
		mappings []ReasoningEffortMapping
		path     string
		want     string
		changed  bool
	}{
		{name: "nested caps high", body: `{"reasoning":{"effort":"xhigh"}}`, max: "medium", path: "reasoning.effort", want: "medium", changed: true},
		{name: "flat caps high", body: `{"reasoning_effort":"high"}`, max: "low", path: "reasoning_effort", want: "low", changed: true},
		{name: "omitted stays omitted", body: `{"model":"gpt-5"}`, max: "low", path: "reasoning_effort", want: "", changed: false},
		{name: "lower stays lower", body: `{"reasoning_effort":"low"}`, max: "high", path: "reasoning_effort", want: "low", changed: false},
		{name: "map before cap", body: `{"reasoning":{"effort":"MAX"}}`, max: "medium", mappings: []ReasoningEffortMapping{{From: "max", To: "xhigh"}}, path: "reasoning.effort", want: "medium", changed: true},
		{name: "mapping is not chained", body: `{"reasoning_effort":"max"}`, mappings: []ReasoningEffortMapping{{From: "max", To: "xhigh"}, {From: "xhigh", To: "low"}}, path: "reasoning_effort", want: "xhigh", changed: true},
		{name: "unknown extension stays", body: `{"reasoning_effort":"future"}`, max: "low", path: "reasoning_effort", want: "future", changed: false},
		{name: "both request shapes", body: `{"reasoning":{"effort":"high"},"reasoning_effort":"xhigh"}`, max: "low", path: "reasoning.effort", want: "low", changed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := ApplyOpenAIReasoningEffortPolicy([]byte(tt.body), tt.max, tt.mappings)
			require.Equal(t, tt.changed, changed)
			require.Equal(t, tt.want, gjson.GetBytes(got, tt.path).String())
			if tt.name == "both request shapes" {
				require.Equal(t, "low", gjson.GetBytes(got, "reasoning_effort").String())
			}
		})
	}
}
