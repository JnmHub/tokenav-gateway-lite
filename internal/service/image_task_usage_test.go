package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageTaskUsageSnapshotPreservesIndependentReasoningEfforts(t *testing.T) {
	requested := "max"
	upstream := "high"
	result := &OpenAIForwardResult{
		Model:                    "gpt-image-test",
		ImageCount:               1,
		RequestedReasoningEffort: &requested,
		ReasoningEffort:          &upstream,
	}
	account := &Account{ID: 42, Platform: PlatformOpenAI}

	snapshot := NewImageTaskUsageSnapshot(result, account, "", "", "", "", "", "", "", ChannelUsageFields{})
	require.NotNil(t, snapshot)
	payload, err := snapshot.Marshal()
	require.NoError(t, err)
	decoded, err := DecodeImageTaskUsage(payload)
	require.NoError(t, err)

	cloned := decoded.Clone()
	restored := cloned.ResultValue()
	require.Equal(t, "max", *restored.RequestedReasoningEffort)
	require.Equal(t, "high", *restored.ReasoningEffort)

	requested = "low"
	upstream = "minimal"
	require.Equal(t, "max", *restored.RequestedReasoningEffort)
	require.Equal(t, "high", *restored.ReasoningEffort)
}
