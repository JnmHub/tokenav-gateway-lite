package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicMessageStartV0162UsesNullStopReason(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type:     "response.created",
		Response: &ResponsesResponse{ID: "resp_test", Model: "grok-4.5"},
	}, state)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Message)
	require.Nil(t, events[0].Message.StopReason)
	sse, err := ResponsesAnthropicEventToSSE(events[0])
	require.NoError(t, err)
	require.Contains(t, sse, `"stop_reason":null`)
	require.NotContains(t, sse, `"stop_reason":""`)
}

func TestAnthropicThinkingSignatureV0162RoundTripsAcrossTurns(t *testing.T) {
	req := &AnthropicRequest{
		Model:     "grok-4.5",
		MaxTokens: 32,
		Messages: []AnthropicMessage{
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`[{"type":"thinking","thinking":"plan","signature":"enc-roundtrip-1"},{"type":"text","text":"done"}]`)},
		},
	}
	converted, err := AnthropicToResponses(req)
	require.NoError(t, err)
	var items []ResponsesInputItem
	require.NoError(t, json.Unmarshal(converted.Input, &items))
	require.Len(t, items, 3)
	require.Equal(t, "reasoning", items[1].Type)
	require.Equal(t, "enc-roundtrip-1", items[1].EncryptedContent)

	response := ResponsesToAnthropic(&ResponsesResponse{
		ID: "resp_1", Model: "grok-4.5", Status: "completed",
		Output: []ResponsesOutput{{Type: "reasoning", EncryptedContent: "enc-roundtrip-2"}},
	}, "grok-4.5")
	require.Len(t, response.Content, 1)
	require.Equal(t, "thinking", response.Content[0].Type)
	require.Equal(t, "enc-roundtrip-2", response.Content[0].Signature)
}

func TestAnthropicThinkingSignatureV0162StreamEmitsBeforeBlockStop(t *testing.T) {
	state := NewResponsesEventToAnthropicState()
	ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type: "response.output_item.added", OutputIndex: 0,
		Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1"},
	}, state)
	require.Empty(t, ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type: "response.reasoning_summary_text.done", OutputIndex: 0,
	}, state))
	events := ResponsesEventToAnthropicEvents(&ResponsesStreamEvent{
		Type: "response.output_item.done", OutputIndex: 0,
		Item: &ResponsesOutput{Type: "reasoning", ID: "rs_1", EncryptedContent: "enc-stream-1", Status: "completed"},
	}, state)
	require.Len(t, events, 2)
	require.Equal(t, "signature_delta", events[0].Delta.Type)
	require.Equal(t, "enc-stream-1", events[0].Delta.Signature)
	require.Equal(t, "content_block_stop", events[1].Type)
}
