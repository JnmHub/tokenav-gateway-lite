package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRewriteGatewayClientErrorJSON(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       string
		wantChange bool
	}{
		{name: "disabled", input: `{"error":{"message":"real","code":"x"}}`, want: `{"error":{"message":"real","code":"x"}}`},
		{name: "compact", input: `{"code":"INVALID_API_KEY","message":"real"}`, want: `{"code":"INVALID_API_KEY","message":"活动入口：https://example.com"}`, wantChange: true},
		{name: "openai", input: `{"error":{"message":"real","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, want: `{"error":{"code":"rate_limit_exceeded","message":"活动入口：https://example.com","type":"rate_limit_error"}}`, wantChange: true},
		{name: "code only error object", input: `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`, want: `{"error":{"code":"rate_limit_exceeded","message":"活动入口：https://example.com","type":"rate_limit_error"}}`, wantChange: true},
		{name: "responses", input: `{"type":"response.failed","response":{"status":"failed","error":{"code":"upstream_error","message":"real"}}}`, want: `{"response":{"error":{"code":"upstream_error","message":"活动入口：https://example.com"},"status":"failed"},"type":"response.failed"}`, wantChange: true},
		{name: "type only envelope", input: `{"type":"error","code":"upstream_error"}`, want: `{"code":"upstream_error","message":"活动入口：https://example.com","type":"error"}`, wantChange: true},
		{name: "string error", input: `{"error":"authentication required"}`, want: `{"error":"活动入口：https://example.com"}`, wantChange: true},
		{name: "nested business message untouched", input: `{"error":{"message":"real","code":"x","details":{"message":"diagnostic"}},"output":{"message":"business value"}}`, want: `{"error":{"message":"活动入口：https://example.com","code":"x","details":{"message":"diagnostic"}},"output":{"message":"business value"}}`, wantChange: true},
		{name: "nullable error success untouched", input: `{"type":"response.completed","response":{"status":"completed","error":null,"output":[{"type":"message"}]}}`, want: `{"type":"response.completed","response":{"status":"completed","error":null,"output":[{"type":"message"}]}}`},
		{name: "success untouched", input: `{"message":"success","data":{"message":"model output"}}`, want: `{"message":"success","data":{"message":"model output"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := "活动入口：https://example.com"
			if test.name == "disabled" {
				message = ""
			}
			got, changed := RewriteGatewayClientErrorJSON([]byte(test.input), message)
			require.Equal(t, test.wantChange, changed)
			require.JSONEq(t, test.want, string(got))
		})
	}
}

func TestRewriteGatewayClientErrorSSE(t *testing.T) {
	input := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"upstream_error\",\"message\":\"real\"}}}\n\n"
	got, changed := RewriteGatewayClientErrorSSE([]byte(input), "请联系管理员")
	require.True(t, changed)
	require.Contains(t, string(got), `"delta":"hello"`)
	require.Contains(t, string(got), `"code":"upstream_error"`)
	require.Contains(t, string(got), `"message":"请联系管理员"`)
}

func TestGatewayClientErrorMessageContextAndUTF8Truncation(t *testing.T) {
	ctx := WithGatewayCustomErrorMessage(context.Background(), "  自定义说明  ")
	require.Equal(t, "自定义说明", GatewayClientErrorMessage(ctx, "real"))
	require.Equal(t, "real", GatewayClientErrorMessage(context.Background(), "real"))
	require.Equal(t, "中文", TruncateGatewayClientErrorReason("中文说明", len("中文")))
	require.True(t, strings.HasPrefix("中文说明", TruncateGatewayClientErrorReason("中文说明", 7)))
}

func TestNormalizeGatewayCustomErrorMessageLimit(t *testing.T) {
	message, err := normalizeGatewayCustomErrorMessage(strings.Repeat("广", GatewayCustomErrorMessageMaxRunes))
	require.NoError(t, err)
	require.Len(t, []rune(message), GatewayCustomErrorMessageMaxRunes)
	_, err = normalizeGatewayCustomErrorMessage(strings.Repeat("广", GatewayCustomErrorMessageMaxRunes+1))
	require.Error(t, err)
}
