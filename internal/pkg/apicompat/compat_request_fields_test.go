package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestChatCompletionsToResponsesPreservesExplicitParallelToolCallsFalse(t *testing.T) {
	value := false
	req := &ChatCompletionsRequest{
		Model:             "gpt-5.6-sol",
		Messages:          []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		ParallelToolCalls: &value,
	}

	out, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, out.ParallelToolCalls)
	require.False(t, *out.ParallelToolCalls)

	roundTrip, err := ResponsesToChatCompletionsRequest(out)
	require.NoError(t, err)
	require.NotNil(t, roundTrip.ParallelToolCalls)
	require.False(t, *roundTrip.ParallelToolCalls)
}

func TestResponseFormatJSONSchemaRoundTrip(t *testing.T) {
	req := &ChatCompletionsRequest{
		Model:    "gpt-5.6-sol",
		Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		ResponseFormat: json.RawMessage(`{
			"type":"json_schema",
			"json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"ok":{"type":"boolean"}}}}
		}`),
	}

	responsesReq, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.NotNil(t, responsesReq.Text)
	require.Equal(t, "json_schema", gjson.GetBytes(responsesReq.Text.Format, "type").String())
	require.Equal(t, "answer", gjson.GetBytes(responsesReq.Text.Format, "name").String())
	require.True(t, gjson.GetBytes(responsesReq.Text.Format, "strict").Bool())
	require.Equal(t, "object", gjson.GetBytes(responsesReq.Text.Format, "schema.type").String())

	roundTrip, err := ResponsesToChatCompletionsRequest(responsesReq)
	require.NoError(t, err)
	require.Equal(t, "json_schema", gjson.GetBytes(roundTrip.ResponseFormat, "type").String())
	require.Equal(t, "answer", gjson.GetBytes(roundTrip.ResponseFormat, "json_schema.name").String())
	require.True(t, gjson.GetBytes(roundTrip.ResponseFormat, "json_schema.strict").Bool())
	require.Equal(t, "object", gjson.GetBytes(roundTrip.ResponseFormat, "json_schema.schema.type").String())
}

func TestResponseFormatJSONObjectPassesThrough(t *testing.T) {
	req := &ChatCompletionsRequest{
		Model:          "gpt-5.6-sol",
		Messages:       []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		ResponseFormat: json.RawMessage(`{"type":"json_object"}`),
	}

	responsesReq, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"json_object"}`, string(responsesReq.Text.Format))

	roundTrip, err := ResponsesToChatCompletionsRequest(responsesReq)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"json_object"}`, string(roundTrip.ResponseFormat))
}
