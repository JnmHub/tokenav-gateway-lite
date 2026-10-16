package service

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func filterGrokPingTestInput(t *testing.T, input string) string {
	t.Helper()
	body := newGrokResponsesBillingPingFilterBody(
		io.NopCloser(strings.NewReader(input)),
		&Account{Platform: PlatformGrok},
		defaultMaxLineSize,
	)
	output, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	return string(output)
}

func TestGrokResponsesBillingPingFilter(t *testing.T) {
	input := strings.Join([]string{
		": upstream keepalive",
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		"event: ping",
		`data: {"type":"ping","x-opencode-type":"inference-cost","cost":2.75,"input-tokens":42}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		"",
	}, "\n")

	result := filterGrokPingTestInput(t, input)
	require.NotContains(t, result, "event: ping")
	require.NotContains(t, result, "inference-cost")
	require.Equal(t, 1, strings.Count(result, ": ping\n\n"))
	require.Contains(t, result, ": upstream keepalive\n\n")
	require.Contains(t, result, "response.output_text.delta")
	require.Contains(t, result, "response.completed")
	require.Contains(t, result, `"usage":{"input_tokens":3,"output_tokens":5}`)
}

func TestGrokResponsesBillingPingFilterPreservesNonPingFrames(t *testing.T) {
	input := strings.Join([]string{
		"event: ping",
		`data: {"type":"response.completed"}`,
		"",
		"event: custom",
		`data: {"type":"ping","x-opencode-type":"inference-cost"}`,
		"",
		`data: {"type":"ping","cost":"0"}`,
		"",
	}, "\n")
	require.Equal(t, input, filterGrokPingTestInput(t, input))
}

func TestGrokResponsesBillingPingFilterBoundsMalformedFrames(t *testing.T) {
	malformed := "event: ping\r\ndata: {not-json}\r\n\r\n"
	require.Equal(t, ": ping\n\n", filterGrokPingTestInput(t, malformed))

	lines := []string{"event: ping"}
	for index := 0; index < grokResponsesPingFrameMaxLines; index++ {
		lines = append(lines, ": filler")
	}
	lines = append(lines, `data: {"type":"ping"}`, "")
	oversized := strings.Join(lines, "\n")
	require.Equal(t, oversized, filterGrokPingTestInput(t, oversized))
}

func TestGrokResponsesBillingPingFilterSkipsOtherPlatforms(t *testing.T) {
	input := "event: ping\ndata: {\"type\":\"ping\"}\n\n"
	source := io.NopCloser(strings.NewReader(input))
	body := newGrokResponsesBillingPingFilterBody(source, &Account{Platform: PlatformOpenAI}, defaultMaxLineSize)
	output, err := io.ReadAll(body)
	require.NoError(t, err)
	require.NoError(t, body.Close())
	require.Equal(t, input, string(output))
}
