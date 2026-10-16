package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCodexCallIDV0162BoundsAndPairs(t *testing.T) {
	longID := "call_" + strings.Repeat("a", 200)
	normalized := normalizeCodexCallID(longID)
	require.Len(t, normalized, codexCallIDMaxLength)
	require.True(t, strings.HasPrefix(normalized, codexCallIDPrefix))
	require.Equal(t, normalized, normalizeCodexCallID(longID), "同一调用及结果必须得到稳定编号")
	require.NotEqual(t, normalized, normalizeCodexCallID("call_"+strings.Repeat("b", 200)))
	require.Equal(t, "fc_short", normalizeCodexCallID("call_short"))
}

func TestExtractSystemMessagesFromInputV0162LosslessPromotion(t *testing.T) {
	t.Run("pure text is promoted once and omitted", func(t *testing.T) {
		req := map[string]any{"input": []any{
			map[string]any{"role": "system", "content": "system guidance"},
			map[string]any{"role": "user", "content": "hello"},
		}}
		require.True(t, extractSystemMessagesFromInput(req, true))
		require.Equal(t, "system guidance", req["instructions"])
		input := req["input"].([]any)
		require.Len(t, input, 1)
		require.Equal(t, "user", input[0].(map[string]any)["role"])
	})

	t.Run("mixed content remains as developer", func(t *testing.T) {
		req := map[string]any{"input": []any{
			map[string]any{"role": "system", "content": []any{
				map[string]any{"type": "input_text", "text": "keep text"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
			}},
		}}
		require.True(t, extractSystemMessagesFromInput(req, true))
		input := req["input"].([]any)
		require.Len(t, input, 1)
		require.Equal(t, "developer", input[0].(map[string]any)["role"])
		require.Equal(t, "keep text", req["instructions"])
	})

	t.Run("structured object remains without fabricated text", func(t *testing.T) {
		req := map[string]any{"input": []any{
			map[string]any{"role": "system", "content": map[string]any{"policy": "strict"}},
		}}
		require.True(t, extractSystemMessagesFromInput(req, true))
		input := req["input"].([]any)
		require.Len(t, input, 1)
		require.Equal(t, "developer", input[0].(map[string]any)["role"])
		_, hasInstructions := req["instructions"]
		require.False(t, hasInstructions)
	})
}
