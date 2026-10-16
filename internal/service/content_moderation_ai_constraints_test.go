package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestContentModerationAIProtocolShapeAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body string
	}{
		{"wrong chat array", ContentModerationProtocolOpenAIChat, `{"messages":{},"stop":["safe"]}`},
		{"wrong responses input", ContentModerationProtocolOpenAIResponses, `{"input":{},"instructions":"preserved instruction"}`},
		{"missing anthropic messages", ContentModerationProtocolAnthropicMessages, `{"system":"preserved instruction"}`},
		{"gemini wrong primary", ContentModerationProtocolGemini, `{"input":"preserved alias"}`},
		{"image prompt object", ContentModerationProtocolOpenAIImages, `{"prompt":{"id":"remote"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := CanonicalContentModerationAIInput(tc.protocol, []byte(tc.body), defaultContentModerationAIConfig())
			require.Error(t, err)
			require.NotNil(t, input, "failed extraction retains evidence")
			require.Equal(t, tc.body, string(input.ForwardBody))
			require.NotEmpty(t, input.InputHash)
			code, _, _ := contentModerationAIErrorDetails(err)
			require.Equal(t, "ai_empty_or_uncovered_input", code)
		})
	}
	// 协议形状成立后，AI 不再按角色、空白或可送文本数增设业务拒绝门槛。
	for _, body := range []string{
		`{"messages":[],"instructions":"submitted instruction"}`,
		`{"input":"","instructions":"submitted instruction"}`,
		`{"messages":[{"role":"user","content":""}]}`,
		`{"messages":[{"role":"user","content":" \n\t"}]}`,
		`{"messages":[{"role":"system","content":"system only"}]}`,
		`{"input":[{"type":"item_reference","id":"remote"}]}`,
		`{"input":[{"type":"function_call_output","output":""}]}`,
	} {
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIChat, []byte(body), defaultContentModerationAIConfig())
		require.NoError(t, err, body)
		require.Equal(t, body, string(input.ForwardBody))
	}
}

func TestContentModerationAIFullOriginalFieldsWithoutSlicing(t *testing.T) {
	long := strings.Repeat("安全文本", 300000) + "forbidden synthetic tail"
	fields := []string{long}
	messages := []any{map[string]any{"role": "system", "content": long}}
	for i := 0; i < 140; i++ {
		text := strings.Repeat("history text ", 10)
		fields = append(fields, text)
		messages = append(messages, map[string]any{"role": "assistant", "content": text})
	}
	body := []byte(aiJSON(map[string]any{"messages": messages}))
	input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIChat, body, defaultContentModerationAIConfig())
	require.NoError(t, err)
	require.Len(t, input.Fragments, len(fields))
	require.Equal(t, body, input.ForwardBody)
	for i, fragment := range input.Fragments {
		require.Equal(t, fields[i], fragment.Text)
		require.Zero(t, fragment.StartRune)
		require.Equal(t, len([]rune(fields[i])), fragment.EndRune)
	}
	require.Contains(t, input.Fragments[0].Text, "forbidden synthetic tail")
}

func TestContentModerationAIToolPayloadPreservesOriginalField(t *testing.T) {
	for _, value := range []any{"actual tool data", " \n\t", 0, false, map[string]any{"name": "payload", "result": []any{0, false}}, []any{}, map[string]any{}} {
		body := aiJSON(map[string]any{"input": []any{map[string]any{"type": "function_call_output", "output": value}}})
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(body), defaultContentModerationAIConfig())
		require.NoError(t, err)
		require.Len(t, input.Fragments, 1)
		expected, ok := value.(string)
		if !ok {
			expected = aiJSON(value)
		}
		require.Equal(t, expected, input.Fragments[0].Text, "string payload must not gain JSON quotes")
		require.Equal(t, body, string(input.ForwardBody))
	}
}

func TestContentModerationAIConstraintsAndUnknownExtensions(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, body, term string
	}{
		{"chat schema", ContentModerationProtocolOpenAIChat, `{"input":"safe","response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"properties":{"image":{"enum":["full schema value"]}}}}}}`, "full schema value"},
		{"responses schema", ContentModerationProtocolOpenAIResponses, `{"input":"safe","TEXT":{"FORMAT":{"type":"json_schema","name":"answer","schema":{"const":"forbidden synthetic"}}}}`, "forbidden synthetic"},
		{"anthropic format", ContentModerationProtocolAnthropicMessages, `{"messages":[{"role":"user","content":"safe","output_config":{"format":{"type":"json_schema","schema":{"enum":["forbidden synthetic"]}}}}]}`, "forbidden synthetic"},
		{"gemini generation", ContentModerationProtocolGemini, `{"contents":[{"parts":[{"text":"safe"}]}],"generationConfig":{"stopSequences":["forbidden synthetic"],"responseJsonSchema":{"properties":{"image":{"const":"schema image is text"}}}}}`, "schema image is text"},
		{"chat prediction", ContentModerationProtocolOpenAIChat, `{"messages":[{"role":"assistant","content":"prefill","prefix":true}],"prediction":{"type":"content","content":[{"type":"text","text":"complete prediction"}]}}`, "complete prediction"},
		{"tool schema", ContentModerationProtocolOpenAIChat, `{"input":"safe","tools":[{"type":"function","function":{"name":"fn","parameters":{"properties":{"image":{"description":"full tool definition"}}}}}]}`, "full tool definition"},
		{"tool output", ContentModerationProtocolOpenAIResponses, `{"input":[{"type":"function_call_output","output":{"odd_key":"full output"}}]}`, "full output"},
		{"reasoning history", ContentModerationProtocolOpenAIChat, `{"messages":[{"role":"assistant","reasoning_content":"full thought","content":"safe"}]}`, "full thought"},
		{"anthropic thinking", ContentModerationProtocolAnthropicMessages, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"full thought","signature":"opaque proof"},{"type":"text","text":"safe"}]}]}`, "full thought"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := CanonicalContentModerationAIInput(tc.protocol, []byte(tc.body), defaultContentModerationAIConfig())
			require.NoError(t, err)
			require.Contains(t, aiJSON(input.Fragments), tc.term)
			require.Equal(t, tc.body, string(input.ForwardBody))
			repeat, err := CanonicalContentModerationAIInput(tc.protocol, []byte(tc.body), defaultContentModerationAIConfig())
			require.NoError(t, err)
			require.Equal(t, input.InputHash, repeat.InputHash)
		})
	}
	for _, tc := range []struct{ body, uncovered string }{
		{`{"input":"safe","unknown_prompt":"hidden"}`, "field:unknown_prompt"},
		{`{"messages":[{"role":"user","content":"safe","hidden_prompt":"hidden"}]}`, "content_field:hidden_prompt"},
		{`{"input":"safe","TEXT":{"format":{"type":"vendor_format","description":"current local format"}}}`, "generation_constraint:text"},
		{`{"input":"safe","previous_response_id":"remote"}`, "history:previous_response_id"},
		{`{"input":"safe","conversation":{"id":"remote"}}`, "history:conversation"},
		{`{"input":"safe","cachedContent":"remote"}`, "history:cachedContent"},
		{`{"input":"safe","prompt":{"id":"remote","variables":{"unknown":"value"}}}`, "history:prompt"},
		{`{"input":"safe","tools":[{"type":"web_search"}]}`, "tool_definition:tools/0"},
		{`{"input":"safe","tools":[{"type":"function","parameters":{"$ref":"https://example.invalid/schema"}}]}`, "schema_reference:tools"},
		{`{"input":"safe","truncation":"auto"}`, "generation_constraint:truncation"},
		{`{"input":"safe","modalities":["audio"]}`, "media:modalities"},
		{`{"input":[{"role":"assistant","content":"safe","encrypted_content":"opaque"}]}`, "history:encrypted_content"},
		{`{"input":[{"role":"user","content":[{"type":"vendor_extension","text":"safe"}]}]}`, "content_type:vendor_extension"},
	} {
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(tc.body), defaultContentModerationAIConfig())
		require.NoError(t, err, tc.body)
		require.Contains(t, input.UncoveredFields, tc.uncovered)
		require.Contains(t, aiJSON(input.Fragments), "safe")
	}
	for _, body := range []string{
		`{"input":"safe","response_format":{"type":"other"}}`,
		`{"input":"safe","tools":[{"parameters":"opaque schema"}]}`,
		`{"input":"safe","stop":42,"temperature":"vendor setting"}`,
	} {
		_, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(body), defaultContentModerationAIConfig())
		require.NoError(t, err, "AI extractor must not become a second protocol validator")
	}
	for _, body := range []string{
		`{"input":"safe","INPUT":"hidden"}`,
		`{"input":"safe"} {"input":"trailing"}`,
		`{"input":"safe"`,
	} {
		input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, []byte(body), defaultContentModerationAIConfig())
		require.Error(t, err)
		require.NotNil(t, input)
		require.Equal(t, body, string(input.ForwardBody))
		require.NotEmpty(t, input.InputHash)
	}
}

func TestContentModerationAIConstraintsAlwaysCallRealHTTP(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		t.Run(adapter, func(t *testing.T) {
			provider, calls, business := aiSyntheticProvider(t, adapter, nil, 200, 0)
			defer provider.Close()
			cfg := aiTestConfig(provider.URL, adapter)
			cfg.AI.PenaltiesEnabled = true
			store := &aiTestStore{}
			svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
			svc.SetAIStore(store)
			in := aiTestInput(`{"input":"safe synthetic"}`)
			in.Protocol = ContentModerationProtocolOpenAIChat
			in.ControlPlaneKeyID, in.BillingSubjectType, in.BillingSubjectID = "stable-key", "user", in.UserID
			first := svc.checkAI(context.Background(), in, cfg, false)
			require.Equal(t, "allow", first.Verdict)
			in.Turn++
			require.Equal(t, "allow", svc.checkAI(context.Background(), in, cfg, false).Verdict)
			require.EqualValues(t, 2, calls.Load(), "same content is reviewed anew on every turn")
			in.Body = []byte(`{"input":"safe synthetic","response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"enum":["forbidden synthetic"]}}}}`)
			in.Turn++
			d := svc.checkAI(context.Background(), in, cfg, false)
			require.Equal(t, "block", d.Verdict)
			require.NotEqual(t, first.InputHash, d.InputHash)
			require.Contains(t, d.Categories, "illicit/violent")
			require.Len(t, store.penalties, 1)
			in.Turn++
			require.Equal(t, "block", svc.checkAI(context.Background(), in, cfg, false).Verdict)
			require.EqualValues(t, 4, calls.Load())
			require.Len(t, store.penalties, 2)
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			in.Turn++
			d = svc.checkAI(cancelled, in, cfg, false)
			require.Equal(t, "error", d.Verdict)
			require.Equal(t, "none", d.PenaltyState)
			require.EqualValues(t, 4, calls.Load())
			require.Len(t, store.penalties, 2)
			require.Zero(t, business.Load())
		})
	}
}

func TestContentModerationAIResponseEnvelopeActionsNeverDispatch(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		for _, tc := range []string{"action", "root action", "mixed case tool", "incomplete", "root error"} {
			t.Run(adapter+"/"+tc, func(t *testing.T) {
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					result := ContentModerationAIResult{PolicyVersion: "tokenav-text-v1", Verdict: "allow", Categories: []string{}, Reason: "Safe synthetic content."}
					message := map[string]any{"role": "assistant", "content": aiJSON(result)}
					if tc == "action" {
						message["action"] = "deduct"
					} else if tc == "mixed case tool" {
						message["TOOL_CALLS"] = []any{}
					}
					var response map[string]any
					if adapter == ContentModerationAIAdapterChat {
						finish := "stop"
						if tc == "incomplete" {
							finish = "length"
						}
						response = map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": message}}}
					} else {
						message["type"], message["status"] = "message", "completed"
						message["content"] = []any{map[string]any{"type": "output_text", "text": aiJSON(result)}}
						status := "completed"
						if tc == "incomplete" {
							status = "incomplete"
						}
						response = map[string]any{"status": status, "output": []any{message}}
					}
					if tc == "root error" {
						response["error"] = map[string]string{"code": "synthetic_error"}
					} else if tc == "root action" {
						response["action"] = "deduct"
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer provider.Close()
				cfg := aiTestConfig(provider.URL, adapter)
				cfg.AI.PenaltiesEnabled = true
				store := &aiTestStore{}
				svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
				svc.SetAIStore(store)
				in := aiTestInput(`{"input":"safe"}`)
				in.ControlPlaneKeyID, in.BillingSubjectType, in.BillingSubjectID = "stable-key", "user", in.UserID
				d := svc.checkAI(context.Background(), in, cfg, false)
				require.Equal(t, "error", d.Verdict)
				require.False(t, d.Allowed)
				require.False(t, d.Flagged)
				require.Equal(t, "none", d.PenaltyState)
				require.Empty(t, store.penalties)
			})
		}
	}
}

func TestContentModerationAIUncoveredIdentityAndSubmittedPromptVariables(t *testing.T) {
	body := []byte(`{"input":"safe","prompt":{"id":"remote-template","variables":{"z":"submitted last value","a":{"type":"input_text","text":"submitted first value"}}},"vendor_a":{},"vendor_z":{},"previous_response_id":"remote","text":{"format":{"type":"vendor_format"}},"tools":[{"type":"function","parameters":{"$ref":"https://example.invalid/schema"}}]}`)
	cfg := defaultContentModerationAIConfig()
	input, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, body, cfg)
	require.NoError(t, err)
	require.Contains(t, aiJSON(input.Fragments), "submitted first value")
	require.Contains(t, aiJSON(input.Fragments), "submitted last value")
	require.Contains(t, input.UncoveredFields, "history:prompt")
	require.Contains(t, input.UncoveredFields, "generation_constraint:text")
	require.Contains(t, input.UncoveredFields, "schema_reference:tools")
	require.True(t, sort.StringsAreSorted(input.UncoveredFields))
	for i := 0; i < 40; i++ {
		repeated, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, body, cfg)
		require.NoError(t, err)
		require.Equal(t, input.InputHash, repeated.InputHash)
		require.Equal(t, input.UncoveredFields, repeated.UncoveredFields)
	}
}

func TestContentModerationAIAdapterRetainsHTTP200OnInvalidResults(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		for _, tc := range []struct{ name, kind, code string }{
			{"outer invalid JSON", "json", "ai_invalid_json"},
			{"truncated response body", "read", "ai_response_read"},
			{"outer wrong shape", "shape", "ai_response_shape"},
			{"outer trailing data", "trailing", "ai_json_trailing_data"},
			{"incomplete envelope", "incomplete", "ai_response_incomplete_or_action"},
			{"inner invalid JSON", "inner_json", "ai_invalid_result_json"},
			{"extra result field", "extra", "ai_result_schema"},
			{"unknown category", "category", "ai_result_category"},
			{"inconsistent verdict", "inconsistent", "ai_result_inconsistent"},
		} {
			t.Run(adapter+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch tc.kind {
					case "read":
						w.Header().Set("Content-Length", "100")
						_, _ = io.WriteString(w, `{}`)
						return
					case "json":
						_, _ = io.WriteString(w, `{`)
						return
					case "shape":
						_, _ = io.WriteString(w, `[]`)
						return
					case "trailing":
						_, _ = io.WriteString(w, `{} {}`)
						return
					}
					result := map[string]any{"policy_version": "tokenav-text-v1", "verdict": "allow", "categories": []string{}, "reason": "safe"}
					switch tc.kind {
					case "extra":
						result["fragments"] = []any{}
					case "category":
						result["verdict"], result["categories"] = "block", []string{"unrecognized"}
					case "inconsistent":
						result["verdict"] = "block"
					}
					text := aiJSON(result)
					if tc.kind == "inner_json" {
						text = "not-json"
					}
					var response any
					if adapter == ContentModerationAIAdapterChat {
						finish := "stop"
						if tc.kind == "incomplete" {
							finish = "length"
						}
						response = map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"role": "assistant", "content": text}}}}
					} else {
						status := "completed"
						if tc.kind == "incomplete" {
							status = "incomplete"
						}
						response = map[string]any{"status": status, "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": text}}}}}
					}
					_ = json.NewEncoder(w).Encode(response)
				}))
				defer server.Close()
				svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
				d := svc.checkAI(context.Background(), aiTestInput(`{"input":"safe"}`), aiTestConfig(server.URL, adapter), false)
				require.Equal(t, "error", d.Verdict)
				require.Equal(t, tc.code, d.ErrorCode)
				require.Equal(t, http.StatusOK, d.ProviderHTTPStatus)
				require.True(t, d.ProviderCalled)
				require.False(t, d.Flagged)
			})
		}
	}
}

func TestContentModerationAIAdapterInheritsOnlyParentDeadline(t *testing.T) {
	for _, adapter := range []string{ContentModerationAIAdapterChat, ContentModerationAIAdapterResponses} {
		t.Run(adapter, func(t *testing.T) {
			provider, calls, _ := aiSyntheticProvider(t, adapter, nil, http.StatusOK, 30*time.Millisecond)
			defer provider.Close()
			svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
			client := &http.Client{Timeout: time.Nanosecond}
			svc.httpClient = client
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cfg := aiTestConfig(provider.URL, adapter)
			d := svc.checkAI(ctx, aiTestInput(`{"input":"safe"}`), cfg, false)
			require.Equal(t, "allow", d.Verdict)
			require.EqualValues(t, 1, calls.Load())
			require.Equal(t, time.Nanosecond, client.Timeout, "shared client must not be mutated")
			cancel()
			d = svc.checkAI(ctx, aiTestInput(`{"input":"safe"}`), cfg, false)
			require.Equal(t, "ai_deadline", d.ErrorCode)
			require.False(t, d.ProviderCalled)
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestContentModerationAIChatResponsesConversionKeepsConstraintsRealHTTP(t *testing.T) {
	body := []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"safe"}],"response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"image":{"description":"forbidden synthetic schema"}}}}},"tools":[{"type":"function","function":{"name":"fn","parameters":{"description":"full tool schema","type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"fn"}}}`)
	var chat apicompat.ChatCompletionsRequest
	require.NoError(t, json.Unmarshal(body, &chat))
	responses, err := apicompat.ChatCompletionsToResponses(&chat)
	require.NoError(t, err)
	forward, err := json.Marshal(responses)
	require.NoError(t, err)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, forward, raw)
		canonical, err := CanonicalContentModerationAIInput(ContentModerationProtocolOpenAIResponses, raw, defaultContentModerationAIConfig())
		require.NoError(t, err)
		require.Contains(t, aiJSON(canonical.Fragments), "forbidden synthetic schema")
		require.Contains(t, aiJSON(canonical.Fragments), "full tool schema")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	resp, err := http.Post(upstream.URL+"/responses", "application/json", bytes.NewReader(forward))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}
