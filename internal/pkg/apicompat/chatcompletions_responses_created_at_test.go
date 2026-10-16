package apicompat

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChatCompletionsResponseToResponses_PreservesCreatedAt(t *testing.T) {
	created := int64(1700000000)
	// [tokenav 定制] 官方 v0.2.5 为 ChatCompletionsResponseToResponses 增加了
	// namespacedToolNames（命名空间工具名映射）参数，本地测试同步补 nil。
	out := ChatCompletionsResponseToResponses(&ChatCompletionsResponse{
		ID:      "chatcmpl_created",
		Created: created,
		Model:   "upstream-model",
	}, "requested-model", nil, nil, false, nil)

	require.Equal(t, created, out.CreatedAt)
}

func TestChatCompletionsResponseToResponses_SynthesizesCreatedAt(t *testing.T) {
	before := time.Now().Unix()
	out := ChatCompletionsResponseToResponses(nil, "requested-model", nil, nil, false, nil)
	after := time.Now().Unix()

	require.GreaterOrEqual(t, out.CreatedAt, before)
	require.LessOrEqual(t, out.CreatedAt, after)
}

func TestChatCompletionsResponsesStream_UsesStableCreatedAt(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("stream-model")
	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		Created: 1700000042,
		Choices: []ChatChunkChoice{{Delta: ChatDelta{Content: testStringPtr("hello")}}},
	}, state)
	events = append(events, FinalizeChatCompletionsResponsesStream(state)...)

	var createdAt int64
	var completedAt int64
	for _, event := range events {
		switch event.Type {
		case "response.created":
			require.NotNil(t, event.Response)
			createdAt = event.Response.CreatedAt
		case "response.completed":
			require.NotNil(t, event.Response)
			completedAt = event.Response.CreatedAt
		}
	}
	require.NotZero(t, createdAt)
	require.Equal(t, state.Created, createdAt)
	require.Equal(t, createdAt, completedAt)
}

func testStringPtr(value string) *string { return &value }
