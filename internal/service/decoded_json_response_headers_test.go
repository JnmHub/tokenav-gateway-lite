package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteDecodedJSONResponseHeadersDropsStaleWireMetadata(t *testing.T) {
	dst := make(http.Header)
	src := http.Header{
		"Content-Type":     []string{"application/json"},
		"Content-Encoding": []string{"gzip"},
		"Content-Length":   []string{"9876"},
		"X-Request-Id":     []string{"rid-json"},
	}

	writeDecodedJSONResponseHeaders(dst, src, responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{}))

	require.Equal(t, "application/json", dst.Get("Content-Type"))
	require.Equal(t, "rid-json", dst.Get("X-Request-Id"))
	require.Empty(t, dst.Get("Content-Encoding"))
	require.Empty(t, dst.Get("Content-Length"))
}

func TestWriteDecodedJSONResponseHeadersPreservesCodexHeaders(t *testing.T) {
	dst := make(http.Header)
	src := http.Header{
		"Content-Encoding":             []string{"br"},
		"X-Codex-Turn-State":           []string{"turn-state"},
		"X-Codex-Primary-Used-Percent": []string{"12"},
	}

	writeDecodedJSONResponseHeaders(dst, src, nil)

	require.Equal(t, "turn-state", dst.Get(openAICodexTurnStateHeader))
	require.Equal(t, "12", dst.Get("X-Codex-Primary-Used-Percent"))
	require.Empty(t, dst.Get("Content-Encoding"))
}

func TestOpenAIImagesNonStreamingResponseDropsStaleWireMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	svc := &OpenAIGatewayService{
		cfg:                  &config.Config{},
		responseHeaderFilter: responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{}),
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"application/json"},
			"Content-Encoding": []string{"gzip"},
			"Content-Length":   []string{"1234"},
		},
		Body: io.NopCloser(bytes.NewReader([]byte(`{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`))),
	}

	_, _, _, err := svc.handleOpenAIImagesNonStreamingResponse(resp, c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, recorder.Header().Get("Content-Encoding"))
	require.Empty(t, recorder.Header().Get("Content-Length"))
	require.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json"))
}

func TestGrokMediaJSONResponseDropsStaleWireMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type":     []string{"application/json"},
			"Content-Encoding": []string{"br"},
			"Content-Length":   []string{"4321"},
		},
		Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://example.com/image.png"}]}`)),
	}

	writeGrokMediaResponse(c, resp, []byte(`{"data":[{"url":"https://example.com/image.png"}]}`), responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{}))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Empty(t, recorder.Header().Get("Content-Encoding"))
	require.Empty(t, recorder.Header().Get("Content-Length"))
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
}
