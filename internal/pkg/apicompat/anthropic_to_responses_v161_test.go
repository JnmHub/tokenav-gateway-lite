package apicompat

import "testing"

func TestAnthropicEventToResponsesV161_TextLifecycleAndCompletedOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	idx := 0
	var events []ResponsesStreamEvent
	feed := func(event *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(event, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "Hello "}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "text_delta", Text: "world"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	positions := map[string]int{}
	for i, event := range events {
		if _, exists := positions[event.Type]; !exists {
			positions[event.Type] = i
		}
		switch event.Type {
		case "response.output_text.done":
			if event.Text != "Hello world" {
				t.Fatalf("output_text.done text=%q", event.Text)
			}
		case "response.content_part.done":
			if event.Part == nil || event.Part.Text != "Hello world" {
				t.Fatalf("content_part.done=%+v", event.Part)
			}
		case "response.completed":
			if event.Response == nil || len(event.Response.Output) != 1 ||
				len(event.Response.Output[0].Content) != 1 || event.Response.Output[0].Content[0].Text != "Hello world" {
				t.Fatalf("completed output=%+v", event.Response)
			}
		}
	}
	if positions["response.content_part.added"] >= positions["response.output_text.delta"] {
		t.Fatalf("content_part.added must precede delta: %+v", positions)
	}
}

func TestAnthropicEventToResponsesV161_ToolArgumentsReachTerminalOutput(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	idx := 0
	var events []ResponsesStreamEvent
	feed := func(event *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(event, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_1"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_1", Name: "weather"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `{"city":`}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `"SH"}`}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	for _, event := range events {
		if event.Type == "response.function_call_arguments.done" && event.Arguments != `{"city":"SH"}` {
			t.Fatalf("function_call_arguments.done=%q", event.Arguments)
		}
		if event.Type != "response.completed" {
			continue
		}
		if event.Response == nil || len(event.Response.Output) != 1 {
			t.Fatalf("completed output missing")
		}
		call := event.Response.Output[0]
		if call.Type != "function_call" || call.Name != "weather" || call.Arguments != `{"city":"SH"}` {
			t.Fatalf("completed function call=%+v", call)
		}
		return
	}
	t.Fatal("response.completed missing")
}

func TestAnthropicEventToResponsesV161_MultipleTextPartsKeepDistinctIndexes(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	firstIndex := 0
	secondIndex := 1
	var events []ResponsesStreamEvent
	feed := func(event *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(event, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_multi"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &firstIndex, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &firstIndex, Delta: &AnthropicDelta{Type: "text_delta", Text: "first"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &firstIndex})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &secondIndex, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &secondIndex, Delta: &AnthropicDelta{Type: "text_delta", Text: "second"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &secondIndex})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var addedIndexes []int
	for _, event := range events {
		switch event.Type {
		case "response.content_part.added":
			addedIndexes = append(addedIndexes, event.ContentIndex)
		case "response.completed":
			if event.Response == nil || len(event.Response.Output) != 1 || len(event.Response.Output[0].Content) != 2 {
				t.Fatalf("completed multi-part output=%+v", event.Response)
			}
			if event.Response.Output[0].Content[0].Text != "first" || event.Response.Output[0].Content[1].Text != "second" {
				t.Fatalf("completed multi-part content=%+v", event.Response.Output[0].Content)
			}
		}
	}
	if len(addedIndexes) != 2 || addedIndexes[0] != 0 || addedIndexes[1] != 1 {
		t.Fatalf("content indexes=%v", addedIndexes)
	}
}

func TestAnthropicEventToResponsesV161_ReasoningDoneCarriesFullSummary(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	idx := 0
	var events []ResponsesStreamEvent
	feed := func(event *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(event, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_reasoning"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &idx, ContentBlock: &AnthropicContentBlock{Type: "thinking"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "first "}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &idx, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "second"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &idx})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	for _, event := range events {
		if event.Type == "response.reasoning_summary_text.done" && event.Text != "first second" {
			t.Fatalf("reasoning done=%q", event.Text)
		}
		if event.Type == "response.completed" {
			if event.Response == nil || len(event.Response.Output) != 1 || len(event.Response.Output[0].Summary) != 1 || event.Response.Output[0].Summary[0].Text != "first second" {
				t.Fatalf("completed reasoning=%+v", event.Response)
			}
			return
		}
	}
	t.Fatal("response.completed missing")
}

func TestAnthropicEventToResponsesV184_InterleavedThinkingClosesMessage(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	first, second := 0, 1
	var events []ResponsesStreamEvent
	feed := func(event *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(event, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_interleaved"}})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &first, ContentBlock: &AnthropicContentBlock{Type: "text"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &first, Delta: &AnthropicDelta{Type: "text_delta", Text: "answer"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &first})
	feed(&AnthropicStreamEvent{Type: "content_block_start", Index: &second, ContentBlock: &AnthropicContentBlock{Type: "thinking"}})
	feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: &second, Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "reason"}})
	feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: &second})
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var terminal *ResponsesStreamEvent
	for i := range events {
		if events[i].Type == "response.completed" {
			terminal = &events[i]
		}
	}
	requireResponsesTerminal(t, terminal)
	if len(terminal.Response.Output) != 2 || terminal.Response.Output[0].Type != "message" || terminal.Response.Output[1].Type != "reasoning" {
		t.Fatalf("interleaved output=%+v", terminal.Response.Output)
	}
	if len(terminal.Response.Output[0].Content) != 1 || terminal.Response.Output[0].Content[0].Text != "answer" {
		t.Fatalf("message text lost: %+v", terminal.Response.Output[0])
	}
}

func TestAnthropicEventToResponsesV184_MultipleTextBlocksAdvanceContentIndex(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.Model = "claude-sonnet-4-5"
	first, second := 0, 1
	var events []ResponsesStreamEvent
	feed := func(event *AnthropicStreamEvent) {
		events = append(events, AnthropicEventToResponsesEvents(event, state)...)
	}

	feed(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_parts"}})
	for _, block := range []struct {
		index *int
		text  string
	}{{&first, "first"}, {&second, "second"}} {
		feed(&AnthropicStreamEvent{Type: "content_block_start", Index: block.index, ContentBlock: &AnthropicContentBlock{Type: "text"}})
		feed(&AnthropicStreamEvent{Type: "content_block_delta", Index: block.index, Delta: &AnthropicDelta{Type: "text_delta", Text: block.text}})
		feed(&AnthropicStreamEvent{Type: "content_block_stop", Index: block.index})
	}
	feed(&AnthropicStreamEvent{Type: "message_stop"})

	var indexes []int
	var terminal *ResponsesStreamEvent
	for i := range events {
		switch events[i].Type {
		case "response.content_part.added":
			indexes = append(indexes, events[i].ContentIndex)
		case "response.completed":
			terminal = &events[i]
		}
	}
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("content indexes=%v, want [0 1]", indexes)
	}
	requireResponsesTerminal(t, terminal)
	if len(terminal.Response.Output) != 1 || len(terminal.Response.Output[0].Content) != 2 {
		t.Fatalf("terminal parts=%+v", terminal.Response.Output)
	}
}

func TestAnthropicEventToResponsesV184_FinalizePreservesMaxTokens(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_finalize"}}, state)
	AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_delta", Delta: &AnthropicDelta{StopReason: "max_tokens"}}, state)

	events := FinalizeAnthropicResponsesStream(state)
	if len(events) != 1 || events[0].Type != "response.incomplete" || events[0].Response == nil {
		t.Fatalf("finalize events=%+v", events)
	}
	if events[0].Response.IncompleteDetails == nil || events[0].Response.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("incomplete details=%+v", events[0].Response.IncompleteDetails)
	}
	if len(FinalizeAnthropicResponsesStream(state)) != 0 {
		t.Fatal("finalization must be idempotent")
	}
}

func requireResponsesTerminal(t *testing.T, event *ResponsesStreamEvent) {
	t.Helper()
	if event == nil || event.Response == nil {
		t.Fatalf("response.completed missing: %+v", event)
	}
}
