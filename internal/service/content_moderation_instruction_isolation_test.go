package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchedInstructionContributorsKeepUnrelatedSafeControls(t *testing.T) {
	segments := []ContentModerationSegment{
		{Source: ContentModerationSourceInstructions, Text: "safe project policy"},
		{Source: ContentModerationSourceInstructions, Text: "forbidden"},
		{Source: ContentModerationSourceInstructions, Text: "directive"},
		{Source: ContentModerationSourceInstructions, Text: "safe output format"},
	}

	contributors := contentModerationMatchedInstructionContributors(segments, "forbidden directive")

	require.Equal(t, []string{"forbidden", "directive"}, contributors)
}

func TestMatchedInstructionContributorsHonorASCIIWordBoundaries(t *testing.T) {
	segments := []ContentModerationSegment{
		{Source: ContentModerationSourceInstructions, Text: "source"},
		{Source: ContentModerationSourceInstructions, Text: "rce"},
	}

	contributors := contentModerationMatchedInstructionContributors(segments, "rce")

	// The first occurrence is inside "source" and must not contribute. Only the
	// standalone final token matches the same boundary rules as the hot path.
	require.Equal(t, []string{"rce"}, contributors)
}

func TestMatchedInstructionContributorsCoverNoSeparatorAnthropicAndGeminiBlocks(t *testing.T) {
	for _, protocol := range []string{ContentModerationProtocolAnthropicMessages, ContentModerationProtocolGemini} {
		t.Run(protocol, func(t *testing.T) {
			segments := []ContentModerationSegment{
				{Source: ContentModerationSourceUser, Protocol: protocol, Text: "safe request"},
				{Source: ContentModerationSourceInstructions, Protocol: protocol, InstructionOrigin: contentModerationInstructionSystem, Text: "safe system rule"},
				{Source: ContentModerationSourceInstructions, Protocol: protocol, InstructionOrigin: contentModerationInstructionSystem, Text: "forbid"},
				{Source: ContentModerationSourceInstructions, Protocol: protocol, InstructionOrigin: contentModerationInstructionSystem, Text: "den directive"},
				{Source: ContentModerationSourceInstructions, Protocol: protocol, InstructionOrigin: contentModerationInstructionSystem, Text: "safe output rule"},
			}

			base := contentModerationPolicyText(ContentModerationInput{Segments: segments})
			require.NotContains(t, base, "forbidden directive")
			keywordText := contentModerationKeywordPolicyText(ContentModerationInput{Segments: segments}, base)
			require.Contains(t, keywordText, "forbidden directive")
			require.Equal(t, []string{"forbid", "den directive"}, contentModerationMatchedInstructionContributors(segments, "forbidden directive"))
			excerpt := contentModerationMatchedExcerpt(ContentModerationInput{Segments: segments}, "forbidden directive")
			require.Contains(t, excerpt, "forbidden directive", "evidence must use the same no-separator matching view")
			require.NotContains(t, excerpt, "safe request", "ordinary user text must never replace instruction evidence")
		})
	}
}

func TestMatchedInstructionContributorsDoNotNoSeparatorJoinResponsesControls(t *testing.T) {
	segments := []ContentModerationSegment{
		{Source: ContentModerationSourceInstructions, Protocol: ContentModerationProtocolOpenAIResponses, InstructionOrigin: contentModerationInstructionTopLevel, Text: "forbid"},
		{Source: ContentModerationSourceInstructions, Protocol: ContentModerationProtocolOpenAIResponses, InstructionOrigin: contentModerationInstructionSystem, Text: "den directive"},
	}

	require.Empty(t, contentModerationMatchedInstructionContributors(segments, "forbidden directive"))
}
