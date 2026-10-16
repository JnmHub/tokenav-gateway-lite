package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestDeriveOpenAIContentSessionSeed_EmptyInputs(t *testing.T) {
	require.Empty(t, deriveOpenAIContentSessionSeed(nil))
	require.Empty(t, deriveOpenAIContentSessionSeed([]byte{}))
	require.Empty(t, deriveOpenAIContentSessionSeed([]byte(`{}`)))
}

func TestDeriveOpenAIContentSessionSeed_ModelOnly(t *testing.T) {
	seed := deriveOpenAIContentSessionSeed([]byte(`{"model":"gpt-5.4"}`))
	require.Contains(t, seed, contentSessionSeedPrefix)
	require.Contains(t, seed, "model=gpt-5.4")
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_StableAcrossTurns(t *testing.T) {
	turn1 := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	turn2 := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		]
	}`)
	s1 := deriveOpenAIContentSessionSeed(turn1)
	s2 := deriveOpenAIContentSessionSeed(turn2)
	require.Equal(t, s1, s2, "seed should be stable across later turns")
	require.NotEmpty(t, s1)
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentFirstUserDiffers(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Question A"}]}`)
	req2 := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Question B"}]}`)
	s1 := deriveOpenAIContentSessionSeed(req1)
	s2 := deriveOpenAIContentSessionSeed(req2)
	require.NotEqual(t, s1, s2)
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentSystemDiffers(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"A"},{"role":"user","content":"Hi"}]}`)
	req2 := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"B"},{"role":"user","content":"Hi"}]}`)
	s1 := deriveOpenAIContentSessionSeed(req1)
	s2 := deriveOpenAIContentSessionSeed(req2)
	require.NotEqual(t, s1, s2)
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_IgnoresDynamicSystemAfterFirstUser(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"stable"},{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"},{"role":"system","content":"request metadata A"}]}`)
	req2 := []byte(`{"model":"gpt-5.4","messages":[{"role":"system","content":"stable"},{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"},{"role":"system","content":"request metadata B"}]}`)
	require.Equal(t, deriveOpenAIContentSessionSeed(req1), deriveOpenAIContentSessionSeed(req2))
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DifferentModelDiffers(t *testing.T) {
	req1 := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hi"}]}`)
	req2 := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`)
	s1 := deriveOpenAIContentSessionSeed(req1)
	s2 := deriveOpenAIContentSessionSeed(req2)
	require.NotEqual(t, s1, s2)
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_WithTools(t *testing.T) {
	withTools := []byte(`{
		"model": "gpt-5.4",
		"tools": [{"type":"function","function":{"name":"get_weather"}}],
		"messages": [{"role": "user", "content": "Hello"}]
	}`)
	withoutTools := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role": "user", "content": "Hello"}]
	}`)
	s1 := deriveOpenAIContentSessionSeed(withTools)
	s2 := deriveOpenAIContentSessionSeed(withoutTools)
	require.NotEqual(t, s1, s2, "tools should affect the seed")
	require.Contains(t, s1, "|tools=")
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_WithFunctions(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"functions": [{"name":"get_weather","parameters":{}}],
		"messages": [{"role": "user", "content": "Hello"}]
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|functions=")
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_DeveloperRole(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "developer", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|system=")
	require.Contains(t, seed, "|first_user=")
}

func TestDeriveOpenAIContentSessionSeed_ChatCompletions_StructuredContent(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "user", "content": [{"type":"text","text":"Hello"}]}
		]
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.NotEmpty(t, seed)
	require.Contains(t, seed, "|first_user=")
}

func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputString(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"Hello, how are you?"}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|input=Hello, how are you?")
}

func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputArray(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|system=")
	require.Contains(t, seed, "|first_user=")
}

func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_WithInstructions(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"instructions": "You are a coding assistant.",
		"input": "Write a hello world"
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|instructions=You are a coding assistant.")
	require.Contains(t, seed, "|input=Write a hello world")
}

func TestDeriveOpenAIContentSessionSeed_Deterministic(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [
			{"role": "system", "content": "You are helpful."},
			{"role": "user", "content": "Hello"}
		]
	}`)
	s1 := deriveOpenAIContentSessionSeed(body)
	s2 := deriveOpenAIContentSessionSeed(body)
	require.Equal(t, s1, s2, "seed must be deterministic")
}

func TestDeriveOpenAIContentSessionSeed_PrefixPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hi"}]}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.True(t, len(seed) > len(contentSessionSeedPrefix))
	require.Equal(t, contentSessionSeedPrefix, seed[:len(contentSessionSeedPrefix)])
}

func TestDeriveOpenAIContentSessionSeed_EmptyToolsIgnored(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[],"messages":[{"role":"user","content":"Hi"}]}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.NotContains(t, seed, "|tools=")
}

func TestDeriveOpenAIContentSessionSeed_MessagesPreferredOverInput(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"messages": [{"role": "user", "content": "from messages"}],
		"input": "from input"
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|first_user=")
	require.NotContains(t, seed, "|input=")
}

func TestDeriveOpenAIContentSessionSeed_JSONCanonicalisation(t *testing.T) {
	compact := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather"}}],"messages":[{"role":"user","content":"Hi"}]}`)
	spaced := []byte(`{
		"model": "gpt-5.4",
		"tools": [
			{ "type" : "function", "function": { "description": "Get weather", "name": "get_weather" } }
		],
		"messages": [ { "role": "user", "content": "Hi" } ]
	}`)
	s1 := deriveOpenAIContentSessionSeed(compact)
	s2 := deriveOpenAIContentSessionSeed(spaced)
	require.Equal(t, s1, s2, "different formatting of identical JSON should produce the same seed")
}

func TestDeriveOpenAIContentSessionSeed_SingleScanMatchesLegacyBytes(t *testing.T) {
	largeValue := strings.Repeat("payload", 1<<17)
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "large chat completions",
			body: []byte(`{"metadata":"` + largeValue + `","model":"gpt-5.4","tools":[{"type":"function","function":{"name":"lookup"}}],"functions":[{"name":"legacy_lookup"}],"messages":[{"role":"system","content":"System prompt"},{"role":"user","content":"Hello"}]}`),
		},
		{
			name: "large responses",
			body: []byte(`{"metadata":"` + largeValue + `","model":"gpt-5.4","instructions":"Be concise.","tools":[{"type":"function","name":"lookup"}],"input":[{"role":"system","content":"System prompt"},{"role":"user","content":"Hello"}]}`),
		},
		{
			name: "fields in reverse order",
			body: []byte(`{"input":"fallback input","messages":[{"role":"user","content":"chat wins"}],"instructions":"Be concise.","functions":[{"name":"lookup"}],"tools":[{"type":"function","name":"lookup"}],"model":"gpt-5.4"}`),
		},
		{
			name: "duplicate fields keep first value",
			body: []byte(`{"model":"first","model":"second","tools":[{"name":"first"}],"tools":[{"name":"second"}],"messages":null,"messages":[{"role":"user","content":"second"}],"input":"first input","input":"second input"}`),
		},
		{
			name: "trailing object fields stay outside root",
			body: []byte(`{"foo":1}{"model":"trailing","input":"trailing input"}`),
		},
		{
			name: "leading garbage before root",
			body: []byte(`garbage{"model":"gpt-5.4","input":"Hello"}`),
		},
		{
			name: "root array stays unsupported",
			body: []byte(`[{"model":"nested"}]{"model":"trailing","input":"trailing input"}`),
		},
		{
			name: "lenient truncated body",
			body: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Hello"}]`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, legacyDeriveOpenAIContentSessionSeed(test.body), deriveOpenAIContentSessionSeed(test.body))
		})
	}
}

func TestDeriveOpenAIContentSessionSeed_AllTruncationOffsetsMatchLegacyBytes(t *testing.T) {
	body := `{"model":"gpt-5.4","tools":[{"type":"function","function":{"name":"lookup"}}],"functions":[{"name":"legacy"}],"instructions":"escaped \" } text","messages":[{"role":"system","content":"System"},{"role":"user","content":[{"type":"text","text":"Hello"}]}],"input":"fallback"}`
	for end := 1; end < len(body); end++ {
		truncated := []byte(body[:end])
		require.Equalf(t, legacyDeriveOpenAIContentSessionSeed(truncated), deriveOpenAIContentSessionSeed(truncated), "truncated at byte %d", end)
	}
}

func legacyDeriveOpenAIContentSessionSeed(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var b strings.Builder
	if model := gjson.GetBytes(body, "model").String(); model != "" {
		_, _ = b.WriteString("model=")
		_, _ = b.WriteString(model)
	}
	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() && tools.Raw != "[]" {
		_, _ = b.WriteString("|tools=")
		_, _ = b.WriteString(normalizeCompatSeedJSON(json.RawMessage(tools.Raw)))
	}
	if funcs := gjson.GetBytes(body, "functions"); funcs.Exists() && funcs.IsArray() && funcs.Raw != "[]" {
		_, _ = b.WriteString("|functions=")
		_, _ = b.WriteString(normalizeCompatSeedJSON(json.RawMessage(funcs.Raw)))
	}
	if instr := gjson.GetBytes(body, "instructions").String(); instr != "" {
		_, _ = b.WriteString("|instructions=")
		_, _ = b.WriteString(instr)
	}

	firstUserCaptured := false
	msgs := gjson.GetBytes(body, "messages")
	if msgs.Exists() && msgs.IsArray() {
		msgs.ForEach(func(_, msg gjson.Result) bool {
			switch msg.Get("role").String() {
			case "system", "developer":
				_, _ = b.WriteString("|system=")
				if content := msg.Get("content"); content.Exists() {
					_, _ = b.WriteString(normalizeCompatSeedJSON(json.RawMessage(content.Raw)))
				}
			case "user":
				if !firstUserCaptured {
					_, _ = b.WriteString("|first_user=")
					if content := msg.Get("content"); content.Exists() {
						_, _ = b.WriteString(normalizeCompatSeedJSON(json.RawMessage(content.Raw)))
					}
					firstUserCaptured = true
				}
			}
			return true
		})
	} else if input := gjson.GetBytes(body, "input"); input.Exists() {
		if input.Type == gjson.String {
			_, _ = b.WriteString("|input=")
			_, _ = b.WriteString(input.String())
		} else if input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				switch item.Get("role").String() {
				case "system", "developer":
					_, _ = b.WriteString("|system=")
					if content := item.Get("content"); content.Exists() {
						_, _ = b.WriteString(normalizeCompatSeedJSON(json.RawMessage(content.Raw)))
					}
				case "user":
					if !firstUserCaptured {
						_, _ = b.WriteString("|first_user=")
						if content := item.Get("content"); content.Exists() {
							_, _ = b.WriteString(normalizeCompatSeedJSON(json.RawMessage(content.Raw)))
						}
						firstUserCaptured = true
					}
				}
				if !firstUserCaptured && item.Get("type").String() == "input_text" {
					_, _ = b.WriteString("|first_user=")
					if text := item.Get("text").String(); text != "" {
						_, _ = b.WriteString(text)
					}
					firstUserCaptured = true
				}
				return true
			})
		}
	}

	if b.Len() == 0 {
		return ""
	}
	return contentSessionSeedPrefix + b.String()
}

func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_InputTextTypedItem(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [{"type": "input_text", "text": "Hello world"}]
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|first_user=")
	require.Contains(t, seed, "Hello world")
}

func TestDeriveOpenAIContentSessionSeed_ResponsesAPI_TypedMessageItem(t *testing.T) {
	body := []byte(`{
		"model": "gpt-5.4",
		"input": [{"type": "message", "role": "user", "content": "Hello from typed message"}]
	}`)
	seed := deriveOpenAIContentSessionSeed(body)
	require.Contains(t, seed, "|first_user=")
	require.Contains(t, seed, "Hello from typed message")
}
