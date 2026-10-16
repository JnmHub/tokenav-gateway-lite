package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesToChatCompletionsRequestIncludesAdditionalTools(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-test",
		Tools: []ResponsesTool{{
			Type:       "function",
			Name:       "wait",
			Parameters: json.RawMessage(`{"type":"object"}`),
		}},
		Input: json.RawMessage(`[
			{"type":"additional_tools","role":"developer","tools":[
				{"type":"custom","name":"exec","description":"Run PowerShell"},
				{"type":"namespace","name":"collaboration","tools":[
					{"type":"function","name":"send_message","parameters":{"type":"object"}}
				]}
			]},
			{"type":"message","role":"user","content":"hello"}
		]`),
		ToolChoice: json.RawMessage(`"auto"`),
	}

	effective, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, effective, 3)
	require.True(t, CustomToolNames(effective)["exec"])
	require.Equal(t,
		NamespacedToolName{Namespace: "collaboration", Name: "send_message"},
		NamespaceToolNames(effective)["collaboration__send_message"],
	)

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 3)
	require.Equal(t, "wait", out.Tools[0].Function.Name)
	require.Equal(t, "exec", out.Tools[1].Function.Name)
	require.Equal(t, "collaboration__send_message", out.Tools[2].Function.Name)
	require.JSONEq(t, `"auto"`, string(out.ToolChoice))
	require.Len(t, out.Messages, 1, "additional_tools 载体不能变成上游聊天消息")
	require.Equal(t, "user", out.Messages[0].Role)
}

func TestEffectiveResponsesToolsSkipsNonObjectInputItems(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`[
			"plain input",
			42,
			{"type":"message","role":"user","content":"hello"},
			{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]}
		]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Equal(t, "exec", tools[0].Name)
}

func TestEffectiveResponsesToolsScopesMalformedToolsToAdditionalCarrier(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`[
			{"type":"message","role":"user","tools":"not-an-array","content":"hello"},
			{"type":"additional_tools","tools":[{"type":"custom","name":"exec"}]}
		]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err, "普通消息上的同名 tools 字段不能被误判为 additional_tools")
	require.Len(t, tools, 1)
	require.Equal(t, "exec", tools[0].Name)
}

func TestEffectiveResponsesToolsRejectsMalformedAdditionalCarrier(t *testing.T) {
	req := &ResponsesRequest{
		Input: json.RawMessage(`[{"type":"additional_tools","tools":"not-an-array"}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.ErrorContains(t, err, "parse responses additional tools item")
	require.Empty(t, tools)
}
