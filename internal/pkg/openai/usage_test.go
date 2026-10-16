package openai

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestExtractResponseUsageImageToolPrecedence(t *testing.T) {
	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"usage":{"input_tokens":11,"output_tokens":22,"input_tokens_details":{"image_tokens":3},"output_tokens_details":{"image_tokens":7}},
			"tool_usage":{"image_gen":{"input_tokens":46,"output_tokens":2459,"input_tokens_details":{"image_tokens":28},"output_tokens_details":{"image_tokens":2459}}}
		}
	}`)

	usage, ok := ExtractResponseUsage(payload)
	require.True(t, ok)
	require.Equal(t, ResponseUsage{
		InputTokens:       46,
		ImageInputTokens:  28,
		OutputTokens:      2459,
		ImageOutputTokens: 2459,
	}, usage)
}

func TestExtractResponseUsageImageCallsDeduplicateAndReplaceTopLevel(t *testing.T) {
	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"usage":{"input_tokens":9,"output_tokens":4},
			"output":[
				{"id":"ig_1","type":"image_generation_call","status":"completed","result":"a","usage":{"input_tokens":10,"input_tokens_details":{"image_tokens":6},"output_tokens":20}},
				{"id":"ig_1","type":"image_generation_call","status":"completed","result":"a","usage":{"input_tokens":10,"input_tokens_details":{"image_tokens":6},"output_tokens":20}},
				{"id":"ig_2","type":"image_generation_call","status":"completed","result":"b","usage":{"input_tokens":7,"input_tokens_details":{"image_tokens":2},"output_tokens":11,"output_tokens_details":{"image_tokens":11}}}
			]
		}
	}`)

	usage, ok := ExtractResponseUsage(payload)
	require.True(t, ok)
	require.Equal(t, ResponseUsage{
		InputTokens:       17,
		ImageInputTokens:  8,
		OutputTokens:      31,
		ImageOutputTokens: 31,
	}, usage)
}

func TestExtractResponseUsageFailureIgnoresImageToolUsage(t *testing.T) {
	payload := []byte(`{
		"type":"response.failed",
		"response":{
			"usage":{"input_tokens":5,"output_tokens":1},
			"tool_usage":{"image_gen":{"input_tokens":100,"output_tokens":200,"output_tokens_details":{"image_tokens":200}}},
			"output":[{"id":"ig_failed","type":"image_generation_call","status":"failed","usage":{"input_tokens":100,"output_tokens":200}}]
		}
	}`)

	usage, ok := ExtractResponseUsage(payload)
	require.True(t, ok)
	require.Equal(t, ResponseUsage{InputTokens: 5, OutputTokens: 1}, usage)
}

func TestExtractResponseUsageCompletedEnvelopeWithFailedStatusIgnoresImageToolUsage(t *testing.T) {
	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"status":"failed",
			"usage":{"input_tokens":5,"output_tokens":1},
			"tool_usage":{"image_gen":{"input_tokens":100,"output_tokens":200,"output_tokens_details":{"image_tokens":200}}}
		}
	}`)

	usage, ok := ExtractResponseUsage(payload)
	require.True(t, ok)
	require.Equal(t, ResponseUsage{InputTokens: 5, OutputTokens: 1}, usage)
}

func TestExtractResponseUsageIntermediateEventIgnored(t *testing.T) {
	_, ok := ExtractResponseUsage([]byte(`{"type":"response.output_item.added","item":{"type":"image_generation_call","usage":{"input_tokens":10,"output_tokens":20}}}`))
	require.False(t, ok)
}

func TestExtractResponseUsageMalformedToolFallsBackAtomically(t *testing.T) {
	payload := []byte(`{
		"type":"response.completed",
		"response":{
			"usage":{"input_tokens":3,"output_tokens":4,"output_tokens_details":{"image_tokens":2}},
			"tool_usage":{"image_gen":{"input_tokens":"46","output_tokens":2459,"output_tokens_details":{"image_tokens":2459}}}
		}
	}`)

	usage, ok := ExtractResponseUsage(payload)
	require.True(t, ok)
	require.Equal(t, ResponseUsage{InputTokens: 3, OutputTokens: 4, ImageOutputTokens: 2}, usage)
}

func TestExtractResponseUsageIgnoresInactiveImageToolSummary(t *testing.T) {
	// 真实 OpenAI Responses 文本响应即使没有调用图片工具，也会携带
	// 全零 image_gen 占位块。顶层 usage 才是这次请求的权威文本用量。
	payload := []byte(`{
		"object":"response",
		"status":"completed",
		"usage":{
			"input_tokens":285929,
			"input_tokens_details":{"cache_write_tokens":0,"cached_tokens":0},
			"output_tokens":5,
			"output_tokens_details":{"reasoning_tokens":0},
			"total_tokens":285934
		},
		"tool_usage":{
			"image_gen":{
				"input_tokens":0,
				"input_tokens_details":{"image_tokens":0,"text_tokens":0},
				"output_tokens":0,
				"output_tokens_details":{"image_tokens":0,"text_tokens":0},
				"total_tokens":0
			},
			"web_search":{"num_requests":0}
		}
	}`)

	usage, ok := ExtractResponseUsage(payload)
	require.True(t, ok)
	require.Equal(t, ResponseUsage{InputTokens: 285929, OutputTokens: 5}, usage)
}

func TestParseUsageObjectCapturesImageInputTokens(t *testing.T) {
	usage, ok := ParseUsageObject(gjson.Parse(`{"input_tokens":371,"input_tokens_details":{"image_tokens":352,"text_tokens":19},"output_tokens":439,"output_tokens_details":{"image_tokens":439}}`))
	require.True(t, ok)
	require.Equal(t, 371, usage.InputTokens)
	require.Equal(t, 352, usage.ImageInputTokens)
	require.Equal(t, 439, usage.OutputTokens)
	require.Equal(t, 439, usage.ImageOutputTokens)
}

func TestBoundedJSONNonNegativeInt(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
		ok   bool
	}{
		{name: "scale reduction before accumulation", raw: `10000000000000000000e-19`, want: 1, ok: true},
		{name: "decimal scale reduction", raw: `10000000000000000000.0e-19`, want: 1, ok: true},
		{name: "fractional after scale reduction", raw: `10000000000000000001e-19`, ok: false},
		{name: "overflow after scale reduction", raw: `92233720368547758080e-1`, ok: false},
		{name: "zero with negative exponent", raw: `0e-100`, want: 0, ok: true},
		{name: "zero beyond exponent bound", raw: `0e101`, want: 0, ok: true},
		{name: "zero padded exponent", raw: `1e0000`, want: 1, ok: true},
		{name: "negative zero syntax", raw: `-0e101`, ok: false},
		{name: "hostile exponent", raw: `1e-1000`, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := BoundedJSONNonNegativeInt(gjson.Parse(tt.raw))
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}
