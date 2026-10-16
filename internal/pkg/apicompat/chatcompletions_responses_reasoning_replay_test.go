package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToChatChainedToolCallsReplayCurrentTurnReasoning(t *testing.T) {
	req := &ResponsesRequest{
		Model: "deepseek-reasoner",
		Input: json.RawMessage(`[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"turn thinking"}]},
			{"type":"function_call","call_id":"call_a","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_a","output":"ok"},
			{"type":"function_call","call_id":"call_b","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_b","output":"ok"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"second turn"}]},
			{"type":"function_call","call_id":"call_c","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_c","output":"ok"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)

	byCallID := make(map[string]ChatMessage)
	for _, message := range out.Messages {
		for _, toolCall := range message.ToolCalls {
			byCallID[toolCall.ID] = message
		}
	}
	require.Equal(t, "turn thinking", byCallID["call_a"].ReasoningContent)
	require.Equal(t, "turn thinking", byCallID["call_b"].ReasoningContent)
	require.Equal(t, "second turn", byCallID["call_c"].ReasoningContent)
}
