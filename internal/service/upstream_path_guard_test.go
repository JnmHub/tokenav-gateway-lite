package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSanitizedUpstreamPathSuffixRejectsUnsafeSegments(t *testing.T) {
	for _, suffix := range []string{
		"/..", "/../x", "/./compact", `/..\x`, "/?a=b", "/compact#fragment",
		"/compact%2f..", "//double", "/compact/", "/a:b", "/a@b", "/模型", "compact", "/...",
	} {
		got, ok := sanitizedUpstreamPathSuffix(suffix)
		require.False(t, ok, "suffix %q must be rejected", suffix)
		require.Empty(t, got)
	}

	for suffix, want := range map[string]string{
		"": "", "/compact": "/compact", "/compact/detail": "/compact/detail", "/input_tokens": "/input_tokens",
		"/resp_68f0a1b2c3d4/cancel": "/resp_68f0a1b2c3d4/cancel", "/gemini-2.5-pro_v1.2": "/gemini-2.5-pro_v1.2",
	} {
		got, ok := sanitizedUpstreamPathSuffix(suffix)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
}

func TestSanitizedUpstreamPathSuffixEnforcesBounds(t *testing.T) {
	_, ok := sanitizedUpstreamPathSuffix("/" + strings.Repeat("a", maxUpstreamPathSegmentLen+1))
	require.False(t, ok)
	_, ok = sanitizedUpstreamPathSuffix(strings.Repeat("/a", maxUpstreamPathSegments+1))
	require.False(t, ok)
}

func TestOpenAIResponsesRequestPathSuffixGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{
		"/v1/responses/../../x", "/v1/responses/..%2f..%2fx", `/v1/responses/..\..\x`,
		"/responses/%3fa=b", "/backend-api/codex/responses//double",
	} {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequest(http.MethodPost, path, nil)
		require.False(t, IsForwardableOpenAIResponsesRequestPath(context), path)
		require.Empty(t, openAIResponsesRequestPathSuffix(context))
		require.Equal(t, chatgptCodexURL, appendOpenAIResponsesRequestPathSuffix(chatgptCodexURL, openAIResponsesRequestPathSuffix(context)))
	}

	for path, want := range map[string]string{
		"/v1/responses": "", "/v1/responses/compact": "/compact", "/backend-api/codex/responses/compact": "/compact",
	} {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequest(http.MethodPost, path, nil)
		require.True(t, IsForwardableOpenAIResponsesRequestPath(context), path)
		require.Equal(t, want, openAIResponsesRequestPathSuffix(context))
	}
}

func TestAppendOpenAIResponsesRequestPathSuffixRefusesUnsafeSuffix(t *testing.T) {
	require.Equal(t, chatgptCodexURL, appendOpenAIResponsesRequestPathSuffix(chatgptCodexURL, "/../../x"))
	require.Equal(t, chatgptCodexURL+"/compact", appendOpenAIResponsesRequestPathSuffix(chatgptCodexURL, "/compact"))
}
