package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildGrokCompactRequestBodyUsesResponsesCompactionTurn(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],"tools":[{"type":"function","name":"shell"}],"stream":true}`)
	patched, err := buildGrokCompactRequestBody(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(patched, "stream").Bool())
	require.False(t, gjson.GetBytes(patched, "store").Bool())
	require.Equal(t, "none", gjson.GetBytes(patched, "tool_choice").String())
	require.Equal(t, "reasoning.encrypted_content", gjson.GetBytes(patched, "include.0").String())
	require.Equal(t, "hello", gjson.GetBytes(patched, "input.0.content.0.text").String())
	require.Contains(t, gjson.GetBytes(patched, "input.1.content.0.text").String(), "1. Primary Request and Intent")
}

func TestConvertGrokResponseToOpenAICompact(t *testing.T) {
	body := []byte(`{
		"id":"resp_grok_1","model":"grok-4.5",
		"output":[
			{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"grok-encrypted-state"},
			{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"summary text"}]}
		],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}
	}`)
	converted, err := convertGrokResponseToOpenAICompact(body)
	require.NoError(t, err)
	require.Equal(t, "resp_grok_1", gjson.GetBytes(converted, "id").String())
	require.Len(t, gjson.GetBytes(converted, "output").Array(), 1)
	require.Equal(t, "compaction", gjson.GetBytes(converted, "output.0.type").String())
	require.Equal(t, "grok-encrypted-state", gjson.GetBytes(converted, "output.0.encrypted_content").String())
	require.Equal(t, "summary text", gjson.GetBytes(converted, "output.0.summary.0.text").String())
	require.Equal(t, int64(14), gjson.GetBytes(converted, "usage.total_tokens").Int())
}

func TestConvertOpenAICompactInputsForGrok(t *testing.T) {
	body := []byte(`{"input":[{"type":"compaction","encrypted_content":"grok-encrypted-state","summary":[{"type":"summary_text","text":"summary text"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}]}`)
	patched, err := convertOpenAICompactInputsForGrok(body)
	require.NoError(t, err)
	require.Equal(t, "reasoning", gjson.GetBytes(patched, "input.0.type").String())
	require.Equal(t, "grok-encrypted-state", gjson.GetBytes(patched, "input.0.encrypted_content").String())
	require.Equal(t, "message", gjson.GetBytes(patched, "input.1.type").String())
	require.Contains(t, gjson.GetBytes(patched, "input.1.content.0.text").String(), "summary text")
	require.Equal(t, "continue", gjson.GetBytes(patched, "input.2.content.0.text").String())
}

func TestConvertGrokResponseToOpenAICompactRequiresEncryptedContent(t *testing.T) {
	_, err := convertGrokResponseToOpenAICompact([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"summary"}]}]}`))
	require.ErrorContains(t, err, "reasoning.encrypted_content")
}
