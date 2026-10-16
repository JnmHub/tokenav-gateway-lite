package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func newCompactBodySignalTestContext(path string, body []byte) *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c
}

func TestNormalizeOpenAIResponsesCompactRequestKeepsNativeV2(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":true,
		"store":true,
		"prompt_cache_key":"compact-seed",
		"input":[{"type":"message"},{"type":"compaction_trigger"}]
	}`)
	c := newCompactBodySignalTestContext("/v1/responses", body)
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses", c.Request.URL.Path)
	require.Equal(t, body, normalized)
	require.True(t, gjson.GetBytes(normalized, "stream").Bool())
	require.True(t, isOpenAIRemoteCompactionV2Request(normalized))
	require.Equal(t, service.OpenAIEndpointCapabilityResponses,
		openAIResponsesRequiredCapabilityForRequest(false, true, service.PlatformOpenAI))
}

func TestNormalizeOpenAIResponsesCompactRequestPromotesNonStreamingBodySignal(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"store":true,
		"prompt_cache_key":"compact-seed",
		"input":[{"type":"message"},{"type":"compaction_trigger"}]
	}`)
	c := newCompactBodySignalTestContext("/v1/responses", body)
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/compact", c.Request.URL.Path)
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	require.False(t, gjson.GetBytes(normalized, "store").Exists())
	require.False(t, gjson.GetBytes(normalized, "prompt_cache_key").Exists())
	seed, exists := c.Get(service.OpenAICompactSessionSeedKeyForTest())
	require.True(t, exists)
	require.Equal(t, "compact-seed", seed)
}

func TestNormalizeOpenAIResponsesCompactRequestDoesNotPromoteResponseSubpath(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	body := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"compaction_trigger"}]}`)
	c := newCompactBodySignalTestContext("/v1/responses/resp_123/cancel", body)
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.Equal(t, "/v1/responses/resp_123/cancel", c.Request.URL.Path)
	require.Equal(t, body, normalized)
}

func TestNormalizeOpenAIResponsesCompactRequestPathBasedStaysUnary(t *testing.T) {
	h := &OpenAIGatewayHandler{}
	body := []byte(`{"model":"gpt-5.6-sol","stream":true,"input":[{"type":"message"}]}`)
	c := newCompactBodySignalTestContext("/v1/responses/compact", body)
	normalized, ok := h.normalizeOpenAIResponsesCompactRequest(c, zap.NewNop(), body)
	require.True(t, ok)
	require.False(t, gjson.GetBytes(normalized, "stream").Exists())
	_, marked := c.Get(service.OpenAICompactClientStreamKeyForTest())
	require.False(t, marked)
}

func TestIsBareOpenAIResponsesPathRejectsLookalikeSuffix(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol"}`)
	require.True(t, isBareOpenAIResponsesPath(newCompactBodySignalTestContext("/backend-api/codex/responses", body)))
	require.False(t, isBareOpenAIResponsesPath(newCompactBodySignalTestContext("/fake/responses", body)))
	require.False(t, isBareOpenAIResponsesPath(newCompactBodySignalTestContext("/v1/responses/resp_1/cancel", body)))
}
