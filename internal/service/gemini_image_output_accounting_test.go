//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const geminiAccountingTestPNG = "iVBORw0KGgoAAAANSUhEUg=="
const geminiAccountingTestPNG2 = "iVBORw0KGgoAAAANSUhEUh=="

func newGeminiImageAccountingContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/nana-banana-2:generateContent", strings.NewReader("{}"))
	return c
}

func geminiAccountingImageResponse(parts string) string {
	return `{"candidates":[{"content":{"role":"model","parts":[` + parts + `]},"finishReason":"STOP"}]}`
}

func TestCountGeminiInlineImageOutputs(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    int
	}{
		{name: "camel case", payload: geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}}`), want: 1},
		{name: "snake case", payload: geminiAccountingImageResponse(`{"inline_data":{"mime_type":"image/png","data":"` + geminiAccountingTestPNG + `"}}`), want: 1},
		{name: "multiple images", payload: geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}},{"inlineData":{"mimeType":"image/webp","data":"` + geminiAccountingTestPNG + `"}}`), want: 2},
		{name: "uppercase mime", payload: geminiAccountingImageResponse(`{"inlineData":{"mimeType":"IMAGE/JPEG","data":"` + geminiAccountingTestPNG + `"}}`), want: 1},
		{name: "text only", payload: geminiAccountingImageResponse(`{"text":"hello"}`), want: 0},
		{name: "non image", payload: geminiAccountingImageResponse(`{"inlineData":{"mimeType":"audio/mpeg","data":"` + geminiAccountingTestPNG + `"}}`), want: 0},
		{name: "empty data", payload: geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":""}}`), want: 0},
		{name: "invalid json", payload: "not-json", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, countGeminiInlineImageOutputs([]byte(tt.payload)))
		})
	}
}

func TestObserveGeminiImageOutputsDeduplicatesSnapshotsAndAccumulatesDeltas(t *testing.T) {
	c := newGeminiImageAccountingContext(t)
	beginGeminiImageOutputObservation(c)

	oneImage := geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}}`)
	for range 4 {
		observeGeminiImageOutputs(c, []byte(oneImage))
	}
	require.Equal(t, 1, observedGeminiImageOutputs(c), "累积式 SSE 不应重复计费")

	secondImageDelta := geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG2 + `"}}`)
	observeGeminiImageOutputs(c, []byte(secondImageDelta))
	require.Equal(t, 2, observedGeminiImageOutputs(c), "不同增量分片里的图片应累计")

	twoImages := geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}},{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG2 + `"}}`)
	observeGeminiImageOutputs(c, []byte(twoImages))
	observeGeminiImageOutputs(c, []byte(twoImages))
	observeGeminiImageOutputs(c, []byte(`{"usageMetadata":{"promptTokenCount":9}}`))
	require.Equal(t, 2, observedGeminiImageOutputs(c), "包含旧图的累积快照不应再次计费")
}

func TestObserveGeminiImageOutputsDeduplicatesSameImageInSinglePayload(t *testing.T) {
	c := newGeminiImageAccountingContext(t)
	beginGeminiImageOutputObservation(c)

	twoCopies := geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}},{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}}`)
	observeGeminiImageOutputs(c, []byte(twoCopies))
	observeGeminiImageOutputs(c, []byte(twoCopies))

	require.Equal(t, 1, observedGeminiImageOutputs(c), "相同图片内容只按一个指纹计数")
}

func TestResolveGeminiImageCount(t *testing.T) {
	t.Run("observed custom model", func(t *testing.T) {
		c := newGeminiImageAccountingContext(t)
		beginGeminiImageOutputObservation(c)
		observeGeminiImageOutputs(c, []byte(geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"`+geminiAccountingTestPNG+`"}}`)))

		require.False(t, isImageGenerationModel("nana-banana-2"))
		require.Equal(t, 1, resolveGeminiImageCount(c, "nana-banana-2", "nana-banana-2"))
	})

	t.Run("mapped model fallback", func(t *testing.T) {
		c := newGeminiImageAccountingContext(t)
		beginGeminiImageOutputObservation(c)
		require.Equal(t, 1, resolveGeminiImageCount(c, "my-image-alias", "gemini-2.5-flash-image"))
	})

	t.Run("text model", func(t *testing.T) {
		c := newGeminiImageAccountingContext(t)
		beginGeminiImageOutputObservation(c)
		require.Zero(t, resolveGeminiImageCount(c, "gemini-2.5-pro", "gemini-2.5-pro"))
	})
}

func TestBeginGeminiImageOutputObservationResetsPerForward(t *testing.T) {
	c := newGeminiImageAccountingContext(t)
	oneImage := []byte(geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}}`))

	beginGeminiImageOutputObservation(c)
	observeGeminiImageOutputs(c, oneImage)
	require.Equal(t, 1, observedGeminiImageOutputs(c))

	beginGeminiImageOutputObservation(c)
	require.Zero(t, observedGeminiImageOutputs(c))
}

func TestHandleNativeNonStreamingResponseFeedsImageCounter(t *testing.T) {
	c := newGeminiImageAccountingContext(t)
	beginGeminiImageOutputObservation(c)
	body := geminiAccountingImageResponse(`{"inlineData":{"mimeType":"image/png","data":"` + geminiAccountingTestPNG + `"}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	svc := &GeminiMessagesCompatService{}
	usage, err := svc.handleNativeNonStreamingResponse(c, resp, false)
	require.NoError(t, err)
	require.NotNil(t, usage)
	require.Equal(t, 1, observedGeminiImageOutputs(c))
}
