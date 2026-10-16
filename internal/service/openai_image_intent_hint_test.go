package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newOpenAIImageIntentHintTestContext(transport OpenAIClientTransport) *gin.Context {
	c := &gin.Context{}
	SetOpenAIClientTransport(c, transport)
	return c
}

func countingOpenAIImageIntentClassifier(calls *atomic.Int64) openAIImageIntentClassifier {
	return func(endpoint string, requestedModel string, body []byte) bool {
		calls.Add(1)
		return IsImageGenerationIntent(endpoint, requestedModel, body)
	}
}

func TestResolveOpenAIImageIntentHintCachesTrueAndFalse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "true", body: []byte("{\"model\":\"gpt-5.4\",\"tools\":[{\"type\":\"image_generation\"}]}"), want: true},
		{name: "false is known", body: []byte("{\"model\":\"gpt-5.4\",\"input\":\"write code\"}")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
			var calls atomic.Int64
			classify := countingOpenAIImageIntentClassifier(&calls)

			require.Equal(t, tt.want, resolveOpenAIImageIntentHint(c, "gpt-5.4", tt.body, classify))
			require.Equal(t, tt.want, resolveOpenAIImageIntentHint(c, "gpt-5.4", tt.body, classify))
			require.Equal(t, int64(1), calls.Load())
			cached, known := getOpenAIImageIntentHint(c)
			require.True(t, known)
			require.Equal(t, tt.want, cached)
		})
	}
}

func TestResolveOpenAIPassthroughImageIntentKeepsAttemptLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
	canonicalBody := []byte("{\"model\":\"gpt-5.4\",\"tools\":[{\"type\":\"image_generation\"}]}")
	strippedBody := []byte("{\"model\":\"gpt-5.4\",\"tools\":[]}")
	var calls atomic.Int64
	classify := countingOpenAIImageIntentClassifier(&calls)

	require.False(t, resolveOpenAIPassthroughImageIntent(c, "gpt-5.4", canonicalBody, "gpt-5.4", strippedBody, true, classify))
	require.Equal(t, int64(2), calls.Load())
	cached, known := getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached)

	require.True(t, resolveOpenAIPassthroughImageIntent(c, "gpt-5.4", canonicalBody, "gpt-5.4", canonicalBody, false, classify))
	require.Equal(t, int64(2), calls.Load())
}

func TestResolveOpenAIPassthroughImageIntentCompactMappingIsAttemptLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := newOpenAIImageIntentHintTestContext(OpenAIClientTransportHTTP)
	canonicalBody := []byte("{\"model\":\"draw-alias\",\"input\":\"draw\"}")
	compactBody := []byte("{\"model\":\"gpt-image-2\",\"input\":\"draw\"}")
	var calls atomic.Int64
	classify := countingOpenAIImageIntentClassifier(&calls)

	require.True(t, resolveOpenAIPassthroughImageIntent(c, "draw-alias", canonicalBody, "gpt-image-2", compactBody, true, classify))
	cached, known := getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.False(t, cached)

	require.False(t, resolveOpenAIPassthroughImageIntent(c, "draw-alias", canonicalBody, "draw-alias", canonicalBody, false, classify))
	require.Equal(t, int64(2), calls.Load())
}

func TestResolveOpenAIImageIntentHintExcludesNonHTTPTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte("{\"model\":\"gpt-5.4\",\"input\":\"write code\"}")
	for _, transport := range []OpenAIClientTransport{OpenAIClientTransportWS, OpenAIClientTransportUnknown} {
		c := newOpenAIImageIntentHintTestContext(transport)
		var calls atomic.Int64
		classify := countingOpenAIImageIntentClassifier(&calls)

		require.False(t, resolveOpenAIImageIntentHint(c, "gpt-5.4", body, classify))
		require.False(t, resolveOpenAIImageIntentHint(c, "gpt-5.4", body, classify))
		require.Equal(t, int64(2), calls.Load())
		_, known := getOpenAIImageIntentHint(c)
		require.False(t, known)
	}
}

func TestOpenAIGatewayServicePassthroughCompactImageIntentIsAttemptLocal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name           string
		canonicalModel string
		compactModel   string
		wantRejected   bool
		wantCanonical  bool
	}{
		{name: "text to image rejects", canonicalModel: "gpt-5.4", compactModel: "gpt-image-2", wantRejected: true},
		{name: "image to text reaches upstream", canonicalModel: "gpt-image-2", compactModel: "gpt-5.4", wantCanonical: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader("{\"id\":\"resp_compact\",\"model\":\"" + tt.compactModel + "\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")),
			}}
			svc := newOpenAIImageGenerationControlTestService(upstream)
			c, recorder := newOpenAIImageGenerationControlTestContext(false, "unit-test-agent/1.0")
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses/compact", nil)
			SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
			account := newOpenAIImageGenerationControlTestAccount()
			account.Extra = map[string]any{"openai_passthrough": true}
			account.Credentials = map[string]any{
				"api_key": "sk-test",
				"compact_model_mapping": map[string]any{
					tt.canonicalModel: tt.compactModel,
				},
			}
			body := []byte("{\"model\":\"" + tt.canonicalModel + "\",\"stream\":false,\"input\":\"draw\"}")

			result, err := svc.Forward(context.Background(), c, account, body)

			cached, known := getOpenAIImageIntentHint(c)
			require.True(t, known)
			require.Equal(t, tt.wantCanonical, cached)
			if tt.wantRejected {
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, http.StatusForbidden, recorder.Code)
				require.Nil(t, upstream.lastReq)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, tt.compactModel, gjson.GetBytes(upstream.lastBody, "model").String())
		})
	}
}

func TestOpenAIGatewayServicePassthroughStripDoesNotPolluteCanonicalHint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader("{\"id\":\"resp_strip\",\"model\":\"gpt-5.4\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}")),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, _ := newOpenAIImageGenerationControlTestContext(false, "codex_cli_rs/0.98.0")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{
		"openai_passthrough":                             true,
		featureKeyCodexImageGenerationExplicitToolPolicy: codexImageGenerationExplicitToolPolicyStrip,
	}
	body := []byte("{\"model\":\"gpt-5.4\",\"stream\":false,\"input\":\"draw\",\"tools\":[{\"type\":\"image_generation\"},{\"type\":\"function\",\"name\":\"shell\"}],\"tool_choice\":{\"type\":\"image_generation\"}}")

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.False(t, gjson.GetBytes(upstream.lastBody, "tools.#(type==\"image_generation\")").Exists())
	require.Equal(t, "function", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "tool_choice").Exists())
	cached, known := getOpenAIImageIntentHint(c)
	require.True(t, known)
	require.True(t, cached)
}
