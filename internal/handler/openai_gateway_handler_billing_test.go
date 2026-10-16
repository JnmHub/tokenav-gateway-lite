package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTurnBillingResultScopesUpstreamResponseIDToConnection(t *testing.T) {
	original := &service.OpenAIForwardResult{RequestID: "resp_upstream", OpenAIWSMode: true}

	got := openAIWSTurnBillingResult("connection-1", 2, original)

	require.NotSame(t, original, got)
	require.Equal(t, "connection-1:ws-turn:2", got.RequestID)
	require.Equal(t, "resp_upstream", got.ResponseID)
	require.Equal(t, "resp_upstream", original.RequestID, "billing identity must not pollute upstream continuation state")
}

func TestOpenAIWSTurnBillingResultSynthesizesStableDistinctTurnIDs(t *testing.T) {
	original := &service.OpenAIForwardResult{OpenAIWSMode: true}

	first := openAIWSTurnBillingResult("connection-1", 1, original)
	firstRetry := openAIWSTurnBillingResult("connection-1", 1, original)
	second := openAIWSTurnBillingResult("connection-1", 2, original)

	require.Equal(t, "connection-1:ws-turn:1", first.RequestID)
	require.Equal(t, first.RequestID, firstRetry.RequestID)
	require.Equal(t, "connection-1:ws-turn:2", second.RequestID)
	require.NotEqual(t, first.RequestID, second.RequestID)
	require.Empty(t, original.RequestID, "billing fallback must not pollute upstream continuation state")
	require.NotSame(t, original, first)
}

func TestOpenAIWSTurnBillingResultAvoidsCrossConnectionResponseIDCollision(t *testing.T) {
	firstConnection := openAIWSTurnBillingResult("connection-1", 1, &service.OpenAIForwardResult{RequestID: "resp_1", OpenAIWSMode: true})
	secondConnection := openAIWSTurnBillingResult("connection-2", 1, &service.OpenAIForwardResult{RequestID: "resp_1", OpenAIWSMode: true})

	require.Equal(t, "connection-1:ws-turn:1", firstConnection.RequestID)
	require.Equal(t, "connection-2:ws-turn:1", secondConnection.RequestID)
	require.NotEqual(t, firstConnection.RequestID, secondConnection.RequestID)
	require.Equal(t, "resp_1", firstConnection.ResponseID)
	require.Equal(t, "resp_1", secondConnection.ResponseID)
}

func TestOpenAIWSTurnBillingResultWithoutConnectionIDLeavesResultUntouched(t *testing.T) {
	original := &service.OpenAIForwardResult{OpenAIWSMode: true}

	got := openAIWSTurnBillingResult("", 1, original)

	require.Same(t, original, got)
	require.Empty(t, got.RequestID)
}
