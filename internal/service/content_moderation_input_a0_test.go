package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentModerationInputA0RetainsReminderCaseAndInputOnly(t *testing.T) {
	for _, test := range []struct{ protocol, body string }{
		{ContentModerationProtocolOpenAIChat, `{"Input":"<system-reminder>forbidden tail</system-reminder>"}`},
		{ContentModerationProtocolOpenAIChat, `{"Messages":[{"ROLE":"user","CONTENT":[{"TYPE":"input_text","TEXT":"<system-reminder>forbidden tail</system-reminder>"}]}]}`},
		{ContentModerationProtocolOpenAIResponses, `{"Input":[{"ROLE":"user","CONTENT":[{"TYPE":"output_text","TEXT":"<system-reminder>forbidden tail</system-reminder>"}]}]}`},
		{ContentModerationProtocolAnthropicMessages, `{"messages":[{"role":"user","content":"<system-reminder>forbidden tail</system-reminder>"}]}`},
		{ContentModerationProtocolOpenAIChat, `{"messages":[{"ROLE":"tool","CONTENT":"<system-reminder>forbidden tail</system-reminder>"}]}`},
	} {
		t.Run(test.protocol+test.body, func(t *testing.T) {
			input := ExtractContentModerationInput(test.protocol, []byte(test.body))
			require.Contains(t, input.Text, "forbidden tail")
			require.Contains(t, input.Text, "system-reminder")
		})
	}
}
