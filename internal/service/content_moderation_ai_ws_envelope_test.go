package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentModerationAIWSEnvelopeCompleteInput(t *testing.T) {
	cfg := defaultContentModerationAIConfig()
	body := []byte(`{
  "type": "response.create", "event_id": "repeated-client-event",
  "model": "synthetic-model", "max_output_tokens": 32,
  "instructions": "full customer instruction",
  "tools": [{"type":"function","name":"synthetic_tool","description":"full tool definition","parameters":{"type":"object","properties":{"value":{"type":"string","description":"full parameter constraint"}}}}],
  "text": {"format":{"type":"json_schema","name":"answer","schema":{"type":"object","description":"full output constraint"}}},
  "input": [
    {"role":"user","content":"full submitted history"},
    {"type":"function_call","name":"synthetic_tool","arguments":"full submitted arguments"},
    {"type":"function_call_output","output":"full submitted tool result"},
    {"role":"user","content":"full latest input"}
  ]
}`)
	canonical, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, body, cfg, true)
	require.NoError(t, err)
	require.Equal(t, body, canonical.ForwardBody, "forwarding must retain the complete original envelope and whitespace")

	parsed, err := strictJSON(body)
	require.NoError(t, err)
	business := parsed.(map[string]any)
	delete(business, "type")
	delete(business, "event_id")
	httpInput, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(aiJSON(business)), cfg)
	require.NoError(t, err)
	require.Equal(t, httpInput.Fragments, canonical.Fragments, "the envelope must not remove any business coverage")
	require.NotEqual(t, httpInput.InputHash, canonical.InputHash, "HTTP and WS evidence identities retain their transport domain")
	require.NotContains(t, aiJSON(canonical.Fragments), "repeated-client-event", "event metadata is not a customer instruction")

	for _, term := range []string{
		"full customer instruction", "full tool definition", "full parameter constraint", "full output constraint",
		"full submitted history", "full submitted arguments", "full submitted tool result", "full latest input",
	} {
		t.Run(term, func(t *testing.T) {
			require.Contains(t, aiJSON(canonical.Fragments), term)
			changed := strings.Replace(string(body), term, term+" changed", 1)
			next, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(changed), cfg, true)
			require.NoError(t, err)
			require.Contains(t, aiJSON(next.Fragments), term+" changed")
			require.NotEqual(t, canonical.Fragments, next.Fragments)
			require.NotEqual(t, canonical.InputHash, next.InputHash)
			require.Equal(t, changed, string(next.ForwardBody))
		})
	}
	for _, term := range []string{"repeated-client-event", "synthetic-model"} {
		changed := strings.Replace(string(body), term, term+"-changed", 1)
		next, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(changed), cfg, true)
		require.NoError(t, err)
		require.Equal(t, canonical.Fragments, next.Fragments)
		require.NotEqual(t, canonical.InputHash, next.InputHash, "full-tree hash must retain metadata outside text fragments")
	}
	changed := strings.Replace(string(body), `"max_output_tokens": 32`, `"max_output_tokens": 64`, 1)
	next, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(changed), cfg, true)
	require.NoError(t, err)
	require.NotEqual(t, canonical.Fragments, next.Fragments)
	require.NotEqual(t, canonical.InputHash, next.InputHash)

	// The returned forwarding bytes must not alias a mutable caller buffer.
	body[0] = '['
	require.Equal(t, byte('{'), canonical.ForwardBody[0])
}

func TestContentModerationAIWSEnvelopeHTTPUnchanged(t *testing.T) {
	cfg := defaultContentModerationAIConfig()
	// 用户裁决（2026-10-15）：HTTP 路径里的 WS 传输字段不再整单拒绝，记录进
	// UncoveredFields 后继续送审；WS 专用业务域仍只在 responsesWebSocket=true
	// 的送审哈希中生效（见 RuntimeOptIn 的哈希隔离断言）。
	for _, fields := range []string{
		`"type":"response.create",`,
		`"type":"anything",`,
		`"event_id":"client-event",`,
		`"type":"response.create","event_id":"client-event",`,
		`"ResponsesWebSocket":true,`,
		`"responses_web_socket":true,`,
	} {
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(`{`+fields+`"input":"safe","max_output_tokens":32}`), cfg)
		require.NoError(t, err)
		require.NotEmpty(t, input.UncoveredFields)
		require.NotEmpty(t, input.Fragments, "primary text must still be reviewed")
	}
	_, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(`{"input":"safe","max_output_tokens":32}`), cfg)
	require.NoError(t, err)
}

func TestContentModerationAIWSEnvelopeRejectsInvalid(t *testing.T) {
	cfg := defaultContentModerationAIConfig()
	for _, tc := range []struct{ name, fields string }{
		{"missing type", ""},
		{"null type", `"type":null,`},
		{"numeric type", `"type":1,`},
		{"object type", `"type":{"text":"response.create"},`},
		{"array type", `"type":["response.create"],`},
		{"boolean type", `"type":true,`},
		{"other event", `"type":"response.cancel",`},
		{"type value case", `"type":"Response.Create",`},
		{"type whitespace", `"type":" response.create ",`},
		{"type key case", `"TYPE":"response.create",`},
		{"event key case", `"type":"response.create","EVENT_ID":"id",`},
		{"event object", `"type":"response.create","event_id":{"input":"hidden"},`},
		{"event array", `"type":"response.create","event_id":["hidden"],`},
		{"event number", `"type":"response.create","event_id":1,`},
		{"event boolean", `"type":"response.create","event_id":false,`},
		{"event null", `"type":"response.create","event_id":null,`},
		{"event too long", `"type":"response.create","event_id":"` + strings.Repeat("a", 513) + `",`},
		{"event byte limit", `"type":"response.create","event_id":"` + strings.Repeat("中", 171) + `",`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(`{`+tc.fields+`"input":"safe","max_output_tokens":32}`), cfg, true)
			require.Error(t, err)
			code, _, _ := contentModerationAIErrorDetails(err)
			require.Equal(t, "ai_invalid_ws_envelope", code)
		})
	}
	for _, event := range []string{"", `"event_id":"",`, `"event_id":"` + strings.Repeat("a", 512) + `",`} {
		_, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(`{"type":"response.create",`+event+`"input":"safe","max_output_tokens":32}`), cfg, true)
		require.NoError(t, err)
	}
	for _, protocol := range []string{ContentModerationProtocolOpenAIChat, ContentModerationProtocolAnthropicMessages, ContentModerationProtocolGemini, ContentModerationProtocolOpenAIImages} {
		_, err := canonicalContentModerationAIInput(protocol, []byte(`{"type":"response.create","input":"safe"}`), cfg, true)
		require.Error(t, err)
		code, _, _ := contentModerationAIErrorDetails(err)
		require.Equal(t, "ai_unsupported_protocol", code)
	}
}

func TestContentModerationAIWSEnvelopePreservesStrictCoverage(t *testing.T) {
	cfg := defaultContentModerationAIConfig()
	for _, tc := range []struct{ name, body, code string }{
		{"duplicate type", `{"type":"response.create","type":"response.cancel","input":"safe"}`, "ai_duplicate_json_key"},
		{"type case collision", `{"type":"response.create","TYPE":"response.create","input":"safe"}`, "ai_duplicate_json_key"},
		{"event case collision", `{"type":"response.create","event_id":"one","EVENT_ID":"two","input":"safe"}`, "ai_duplicate_json_key"},
		{"duplicate nested input", `{"type":"response.create","input":[{"role":"user","content":"safe","CONTENT":"hidden"}]}`, "ai_duplicate_json_key"},
		{"trailing data", `{"type":"response.create","input":"safe"}{}`, "ai_json_trailing_data"},
		{"invalid utf8", "{\"type\":\"response.create\",\"input\":\"\xff\"}", "ai_invalid_utf8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canonical, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(tc.body), cfg, true)
			require.Error(t, err)
			require.NotNil(t, canonical)
			require.Equal(t, tc.body, string(canonical.ForwardBody))
			require.NotEmpty(t, canonical.InputHash)
			code, _, _ := contentModerationAIErrorDetails(err)
			require.Equal(t, tc.code, code)
		})
	}
	// 用户裁决（2026-10-15）：未知扩展字段与图片不再整单拒绝。未知字段记录进
	// UncoveredFields；http(s) 图片链接拉不到字节，记录 image_not_inline 后继续。
	for _, tc := range []struct {
		name, body, uncovered string
	}{
		{"unknown extension", `{"type":"response.create","input":"safe","extension":{"prompt":"hidden"}}`, "field:extension"},
		{"hidden history", `{"type":"response.create","input":"safe","previous_response_id":"resp_hidden"}`, "history:previous_response_id"},
		{"hidden conversation", `{"type":"response.create","input":"safe","conversation":"hidden"}`, "history:conversation"},
		{"prompt reference", `{"type":"response.create","input":"safe","prompt":{"id":"hidden"}}`, "history:prompt"},
		{"remote tool", `{"type":"response.create","input":"safe","tools":[{"type":"web_search"}]}`, "tool_definition:tools/0"},
		{"incomplete output schema", `{"type":"response.create","input":"safe","text":{"format":{"type":"json_schema","name":"incomplete"}}}`, "generation_constraint:text"},
		{"body opt in", `{"type":"response.create","input":"safe","ResponsesWebSocket":true}`, "field:ResponsesWebSocket"},
		{"media http url", `{"type":"response.create","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image"}]}]}`, "image_not_inline:"},
	} {
		t.Run("record/"+tc.name, func(t *testing.T) {
			canonical, err := canonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(tc.body), cfg, true)
			require.NoError(t, err)
			require.NotNil(t, canonical)
			require.NotEmpty(t, canonical.UncoveredFields)
			found := false
			for _, f := range canonical.UncoveredFields {
				found = found || strings.HasPrefix(f, tc.uncovered)
			}
			require.True(t, found, "uncovered=%v want prefix %s", canonical.UncoveredFields, tc.uncovered)
		})
	}
}

func TestContentModerationAIWSEnvelopeRuntimeOptIn(t *testing.T) {
	provider, calls, _ := aiSyntheticProvider(t, ContentModerationAIAdapterChat, nil, 200, 0)
	defer provider.Close()
	cfg := aiTestConfig(provider.URL, ContentModerationAIAdapterChat)
	// 直接服务入口无需审计工作线程或处罚存储；每轮仍必须重新调用审查服务。
	svc := &ContentModerationService{}
	input := aiTestInput(`{"type":"response.create","event_id":"same-client-id","model":"synthetic","max_output_tokens":32,"input":"safe"}`)
	hashes := map[bool]string{}
	turnIDs := map[string]bool{}
	for _, flagged := range []bool{false, true, true, false} {
		input.Turn++
		input.ResponsesWebSocket = flagged
		before := calls.Load()
		decision := svc.checkAI(context.Background(), input, cfg, false)
		require.NotNil(t, decision)
		require.True(t, decision.Allowed, "code=%s", decision.ErrorCode)
		require.True(t, decision.Coverage.Complete)
		require.False(t, decision.Blocked)
		require.True(t, decision.ProviderCalled)
		require.Equal(t, "completed", decision.Phase)
		require.Empty(t, decision.Categories)
		require.NotEmpty(t, decision.Reason)
		require.Equal(t, "none", decision.PenaltyState)
		require.Equal(t, before+1, calls.Load(), "identical input must be reviewed again on each turn")
		if flagged {
			require.Empty(t, decision.Coverage.UncoveredFields)
		} else {
			// HTTP 中的传输字段只标为未覆盖，WS 的独立哈希域用于区分审计证据。
			require.Contains(t, decision.Coverage.UncoveredFields, "field:type")
			require.Contains(t, decision.Coverage.UncoveredFields, "field:event_id")
		}
		if previous, ok := hashes[flagged]; ok {
			require.Equal(t, previous, decision.InputHash, "the same transport/input retains its evidence identity")
		}
		hashes[flagged] = decision.InputHash
		turnID := ContentModerationAdmissionTurnID(input.AdmissionID, input.Turn)
		require.False(t, turnIDs[turnID])
		require.True(t, strings.HasPrefix(turnID, "cm:"))
		require.NotContains(t, turnID, "same-client-id")
		turnIDs[turnID] = true
	}
	require.EqualValues(t, 4, calls.Load())
	require.Len(t, turnIDs, 4)
	require.NotEqual(t, hashes[false], hashes[true], "HTTP and WS audit identities must remain separate")
}
