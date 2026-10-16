package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newOpenAICompactTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, recorder
}

func TestHasCompactionTriggerInInput(t *testing.T) {
	require.True(t, HasCompactionTriggerInInput([]byte(`{"model":"gpt-5.6-sol","input":[{"type":"message"},{"type":"compaction_trigger"}]}`)))
	require.False(t, HasCompactionTriggerInInput([]byte(`{"model":"gpt-5.6-sol","input":[{"type":"message"}]}`)))
	require.False(t, HasCompactionTriggerInInput([]byte(`{"model":"gpt-5.6-sol","input":"compaction_trigger"}`)))
}

func TestBuildOpenAICompactSSEPayloadPreservesTerminalAndOutput(t *testing.T) {
	body := []byte(`{
		"id":"resp_compact",
		"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"opaque"}],
		"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}
	}`)
	payload, ok := buildOpenAICompactSSEPayload(body)
	require.True(t, ok)
	text := string(payload)
	require.Contains(t, text, "event: response.output_item.done")
	require.Contains(t, text, `"type":"compaction"`)
	require.Contains(t, text, `"encrypted_content":"opaque"`)
	require.Contains(t, text, "event: response.completed")
	require.Contains(t, text, `"id":"resp_compact"`)
}

func TestBuildOpenAICompactSSEPayloadDropsIncompleteUsage(t *testing.T) {
	payload, ok := buildOpenAICompactSSEPayload([]byte(`{"output":[{"type":"compaction"}],"usage":{"input_tokens":10}}`))
	require.True(t, ok)
	require.NotContains(t, string(payload), `"usage"`)
	require.Contains(t, string(payload), `"id":"resp_`)
}

func TestReconstructResponseOutputFromSSEPreservesRawCompactionItem(t *testing.T) {
	stream := strings.Join([]string{
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"id":"cmp_1","type":"compaction","encrypted_content":"opaque","future_field":{"ok":true}}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[]}}`,
		``,
	}, "\n")
	output, ok := reconstructResponseOutputFromSSE(stream)
	require.True(t, ok)
	require.Equal(t, "compaction", gjson.GetBytes(output, "0.type").String())
	require.Equal(t, "opaque", gjson.GetBytes(output, "0.encrypted_content").String())
	require.True(t, gjson.GetBytes(output, "0.future_field.ok").Bool())
}

func TestSupplementCompactionItemFromSSEWhenTerminalOutputOmitsIt(t *testing.T) {
	c, _ := newOpenAICompactTestContext("/v1/responses/compact")
	finalResponse := []byte(`{"id":"resp_1","output":[{"id":"msg_1","type":"message"}]}`)
	stream := "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"cmp_1\",\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}}\n\n"
	patched := supplementCompactionItemFromSSE(c, finalResponse, stream)
	require.Equal(t, int64(2), gjson.GetBytes(patched, "output.#").Int())
	require.Equal(t, "compaction", gjson.GetBytes(patched, "output.1.type").String())
}

func TestCompactRepeatedTerminalUsageIsNotAccumulatedTwice(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":20,"output_tokens":4,"input_tokens_details":{"cached_tokens":5,"cache_write_tokens":3}}}}`,
		``,
		`data: {"type":"response.done","response":{"id":"resp_1","usage":{"input_tokens":20,"output_tokens":4,"input_tokens_details":{"cached_tokens":5,"cache_write_tokens":3}}}}`,
		``,
	}, "\n")
	usage := (&OpenAIGatewayService{}).parseSSEUsageFromBody(stream)
	require.Equal(t, 20, usage.InputTokens)
	require.Equal(t, 4, usage.OutputTokens)
	require.Equal(t, 5, usage.CacheReadInputTokens)
	require.Equal(t, 3, usage.CacheCreationInputTokens)
}

func TestOpenAICompactKeepaliveDoesNotCountAsSemanticResponse(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	stop := StartOpenAICompactSSEKeepalive(c, 5*time.Millisecond)
	t.Cleanup(stop)
	// ResponseRecorder 的 Body 不是并发安全的；通过带锁 writer 观察首拍，
	// 再在停止心跳后读取正文，保持测试与生产写回的串行语义一致。
	require.Eventually(t, func() bool { return c.Writer.Size() > 0 }, time.Second, 5*time.Millisecond)
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Contains(t, recorder.Body.String(), ": keepalive")
	_, wrapped := c.Writer.(*openAICompactKeepaliveWriter)
	require.True(t, wrapped, "停止心跳后仍需保留 writer 包装器以串行化后续写回")
}

func TestOpenAICompactCommittedFailureUsesResponseFailed(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, 5*time.Millisecond)
	t.Cleanup(stop)
	require.Eventually(t, func() bool { return c.Writer.Size() > 0 }, time.Second, 5*time.Millisecond)

	require.True(t, writeOpenAICompactSSEBridge(c, http.StatusBadGateway, []byte(`{"error":{"message":"upstream exploded"}}`)))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "event: response.failed")
	require.Contains(t, recorder.Body.String(), "upstream exploded")
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
}
