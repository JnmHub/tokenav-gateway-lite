package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGroupSchedulingMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "", want: GroupSchedulingModeDefault, ok: true},
		{input: " DEFAULT ", want: GroupSchedulingModeDefault, ok: true},
		{input: "random", want: GroupSchedulingModeRandom, ok: true},
		{input: " STRICT_PRIORITY ", want: GroupSchedulingModeStrictPriority, ok: true},
		{input: "weighted", want: "", ok: false},
	}
	for _, test := range tests {
		got, ok := NormalizeGroupSchedulingMode(test.input)
		require.Equal(t, test.ok, ok)
		require.Equal(t, test.want, got)
	}
}

func TestGroupSchedulingModeSupportsPlatform(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		platform string
		want     bool
	}{
		{name: "default supports every platform", mode: GroupSchedulingModeDefault, platform: PlatformAnthropic, want: true},
		{name: "random supports every platform", mode: GroupSchedulingModeRandom, platform: PlatformGemini, want: true},
		{name: "strict supports openai", mode: GroupSchedulingModeStrictPriority, platform: PlatformOpenAI, want: true},
		{name: "strict supports grok", mode: GroupSchedulingModeStrictPriority, platform: PlatformGrok, want: true},
		{name: "strict rejects anthropic", mode: GroupSchedulingModeStrictPriority, platform: PlatformAnthropic, want: false},
		{name: "strict rejects composite", mode: GroupSchedulingModeStrictPriority, platform: PlatformComposite, want: false},
		{name: "invalid mode rejects", mode: "weighted", platform: PlatformOpenAI, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, GroupSchedulingModeSupportsPlatform(test.mode, test.platform))
		})
	}
}

func TestBuildOpenAISelectionOrderRandomUsesWholeEligiblePool(t *testing.T) {
	scheduler := &defaultOpenAIAccountScheduler{}
	plan := openAIAccountLoadPlan{topK: 1}
	for id := int64(1); id <= 5; id++ {
		plan.candidates = append(plan.candidates, openAIAccountCandidateScore{account: &Account{ID: id}})
	}

	order := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{
		SchedulingMode: GroupSchedulingModeRandom,
	}, plan)

	require.Len(t, order, 5, "完全随机不得受 TopK 限制")
	seen := make(map[int64]struct{}, len(order))
	for _, candidate := range order {
		seen[candidate.account.ID] = struct{}{}
	}
	require.Len(t, seen, 5)
}

func TestRandomSchedulingDoesNotUsePreviousResponseBinding(t *testing.T) {
	req := OpenAIAccountScheduleRequest{
		Platform:           PlatformOpenAI,
		PreviousResponseID: "resp_bound",
		SchedulingMode:     GroupSchedulingModeRandom,
	}

	require.False(t, shouldUseOpenAIPreviousResponseBinding(req))
}

func TestCloneGroupForDuplicatePreservesSchedulingMode(t *testing.T) {
	duplicate := cloneGroupForDuplicate(&Group{
		Name:           "随机池",
		SchedulingMode: GroupSchedulingModeRandom,
	}, "operation")

	require.Equal(t, GroupSchedulingModeRandom, duplicate.SchedulingMode)
}
