package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIPassthroughOAuthBody_RemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"prompt_cache_retention":"24h","safety_identifier":"sid","stream_options":{"include_usage":true}}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, false)
	require.NoError(t, err)
	require.True(t, changed)
	for _, field := range openAIChatGPTInternalUnsupportedFields {
		require.False(t, gjson.GetBytes(normalized, field).Exists(), "%s should be stripped", field)
	}
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.False(t, gjson.GetBytes(normalized, "store").Bool())
}

func TestNormalizeOpenAIPassthroughOAuthBody_CompactRemovesUnsupportedUser(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"hello","user":"user_123","metadata":{"user_id":"user_123"},"stream":true,"store":true}`)

	normalized, changed, err := normalizeOpenAIPassthroughOAuthBody(body, true)
	require.NoError(t, err)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(normalized, "user").Exists())
	require.False(t, gjson.GetBytes(normalized, "metadata").Exists())
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
}

func TestNormalizeOpenAIPassthroughOAuthBody_InputShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "string", body: `{"model":"gpt-5.6-sol","input":"hello"}`, want: []string{"message", "user", "hello"}},
		{name: "blank", body: `{"model":"gpt-5.6-sol","input":"  "}`, want: nil},
		{name: "object", body: `{"model":"gpt-5.6-sol","input":{"type":"message","role":"user","content":"hi"}}`, want: []string{"message", "user", "hi"}},
		{name: "array", body: `{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":"kept"}]}`, want: []string{"message", "user", "kept"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalized, _, err := normalizeOpenAIPassthroughOAuthBody([]byte(tc.body), false)
			require.NoError(t, err)
			input := gjson.GetBytes(normalized, "input")
			require.True(t, input.IsArray())
			if tc.want == nil {
				require.Empty(t, input.Array())
				return
			}
			require.Len(t, input.Array(), 1)
			require.Equal(t, tc.want[0], input.Array()[0].Get("type").String())
			require.Equal(t, tc.want[1], input.Array()[0].Get("role").String())
			require.Equal(t, tc.want[2], input.Array()[0].Get("content").String())
		})
	}
}

func TestStripOpenAITopLevelUndefinedStringFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":[{"role":"user","content":"hi"}],"instructions":"[undefined]","temperature":"[undefined]","top_p":"undefined","stream":true}`)

	normalized, changed, err := stripOpenAITopLevelUndefinedStringFields(body)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "gpt-5.5", gjson.GetBytes(normalized, "model").String())
	require.True(t, gjson.GetBytes(normalized, "input").Exists())
	require.False(t, gjson.GetBytes(normalized, "instructions").Exists())
	require.False(t, gjson.GetBytes(normalized, "temperature").Exists())
	require.False(t, gjson.GetBytes(normalized, "top_p").Exists())
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
}

func TestNormalizeOpenAIPassthroughInstructionsAfterUndefinedStrip(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hi","instructions":"[undefined]"}`)

	stripped, changed, err := stripOpenAITopLevelUndefinedStringFields(body)
	require.NoError(t, err)
	require.True(t, changed)

	normalized, changed, err := normalizeOpenAIPassthroughInstructions(stripped, "gpt-5.5")
	require.NoError(t, err)
	require.True(t, changed)
	require.NotEmpty(t, gjson.GetBytes(normalized, "instructions").String())
	require.NotEqual(t, "[undefined]", gjson.GetBytes(normalized, "instructions").String())
}
