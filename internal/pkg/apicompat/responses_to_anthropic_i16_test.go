package apicompat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToAnthropicParallelToolDoneDoesNotEmitGhostDelta(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_parallel", Model: "glm-5.2"},
	}, state)

	firstStart := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_a", Name: "tool_a"},
	}, state)
	require.Len(t, firstStart, 1)
	require.Equal(t, 0, *firstStart[0].Index)

	secondStart := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 1,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_b", Name: "tool_b"},
	}, state)
	require.Len(t, secondStart, 2)
	require.Equal(t, "content_block_stop", secondStart[0].Type)
	require.Equal(t, 0, *secondStart[0].Index)
	require.Equal(t, "content_block_start", secondStart[1].Type)
	require.Equal(t, 1, *secondStart[1].Index)

	oldDone := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Arguments:   `{"a":1}`,
	}, state)
	require.Empty(t, oldDone, "已关闭的并行工具不能补发幽灵 delta")

	currentDone := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 1,
		Arguments:   `{"b":2}`,
	}, state)
	require.Len(t, currentDone, 2)
	require.Equal(t, "content_block_delta", currentDone[0].Type)
	require.Equal(t, 1, *currentDone[0].Index)
	require.JSONEq(t, `{"b":2}`, currentDone[0].Delta.PartialJSON)
	require.Equal(t, "content_block_stop", currentDone[1].Type)
	require.Equal(t, 1, *currentDone[1].Index)
}

func TestResponsesToAnthropicReadStreamsFirstCompleteSanitizedJSON(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	state.MessageStartSent = true
	state.ContentBlockOpen = true
	state.CurrentBlockType = "tool_use"
	state.CurrentToolName = "Read"
	state.OutputIndexToBlockIdx = map[int]int{0: 0}

	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"file_path":"/tmp/te`,
	}, state)
	require.Empty(t, events)
	require.False(t, state.CurrentToolHadDelta)

	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `st.go","pages":""}`,
	}, state)
	require.Len(t, events, 1)
	require.Equal(t, "content_block_delta", events[0].Type)
	require.Equal(t, 0, *events[0].Index)
	require.JSONEq(t, `{"file_path":"/tmp/test.go"}`, events[0].Delta.PartialJSON)
	require.True(t, state.CurrentToolHadDelta)

	// 上游偶发在合法 JSON 后追加尾片段；不得把污染内容继续发给 Claude Code。
	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       ` trailing-garbage`,
	}, state)
	require.Empty(t, events)

	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Arguments:   `{"file_path":"/tmp/test.go","pages":""}`,
	}, state)
	require.Len(t, events, 1)
	require.Equal(t, "content_block_stop", events[0].Type)

	events = ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.done",
		OutputIndex: 0,
		Arguments:   `{"file_path":"/tmp/test.go","pages":""}`,
	}, state)
	require.Empty(t, events, "重复 done 必须幂等")
}

func TestResponsesToAnthropicReadCompleteDeltaSurvivesMissingArgumentsDone(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_read", Model: "gpt-5.5"},
	}, state)
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.output_item.added",
		OutputIndex: 0,
		Item:        &ResponsesOutput{Type: "function_call", CallID: "call_read", Name: "Read"},
	}, state)

	delta := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:        "response.function_call_arguments.delta",
		OutputIndex: 0,
		Delta:       `{"file_path":"/tmp/test.go","pages":""}`,
	}, state)
	require.Len(t, delta, 1)
	require.JSONEq(t, `{"file_path":"/tmp/test.go"}`, delta[0].Delta.PartialJSON)

	completed := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:     "response.completed",
		Response: &ResponsesResponse{Status: "completed"},
	}, state)
	require.Len(t, completed, 3)
	require.Equal(t, "content_block_stop", completed[0].Type)
	require.Equal(t, "tool_use", completed[1].Delta.StopReason)
	require.Equal(t, "message_stop", completed[2].Type)
}
