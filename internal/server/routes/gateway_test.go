package routes

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAICompatibleGatewayPlatformIncludesGrok(t *testing.T) {
	require.True(t, isOpenAICompatibleGatewayPlatform(service.PlatformOpenAI))
	require.True(t, isOpenAICompatibleGatewayPlatform(service.PlatformGrok))
	require.False(t, isOpenAICompatibleGatewayPlatform(service.PlatformAnthropic))
}

func TestGatewayRoutesRegisterMediaProxyOnlyForEnabledGatewayLite(t *testing.T) {
	tests := []struct {
		name      string
		runMode   string
		enabled   bool
		wantMedia bool
	}{
		{name: "gateway lite enabled", runMode: config.RunModeGatewayLite, enabled: true, wantMedia: true},
		{name: "gateway lite disabled", runMode: config.RunModeGatewayLite, enabled: false, wantMedia: false},
		{name: "standard server", runMode: config.RunModeStandard, enabled: true, wantMedia: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				RunMode:     tt.runMode,
				Gateway:     config.GatewayConfig{MaxBodySize: 1024 * 1024, TextMaxBodySize: 1024 * 1024},
				GatewayLite: config.GatewayLiteConfig{MediaProxyEnabled: tt.enabled},
			}
			router := newGatewayRoutesTestRouterWithConfig(cfg, service.PlatformOpenAI)
			found := false
			for _, route := range router.Routes() {
				if route.Method == http.MethodGet && route.Path == "/v1/media/:token" {
					found = true
					break
				}
			}
			require.Equal(t, tt.wantMedia, found)
		})
	}
}

func newGatewayRoutesTestRouter(platform ...string) *gin.Engine {
	return newGatewayRoutesTestRouterWithConfig(&config.Config{
		Gateway: config.GatewayConfig{
			MaxBodySize:     1024 * 1024,
			TextMaxBodySize: 1024 * 1024,
		},
	}, platform...)
}

func newGatewayRoutesTestRouterWithConfig(cfg *config.Config, platform ...string) *gin.Engine {
	return newGatewayRoutesTestRouterWithHandler(cfg, &handler.OpenAIGatewayHandler{}, false, platform...)
}

func newGatewayRoutesBodyLimitTestRouter(cfg *config.Config, platform ...string) *gin.Engine {
	openAIGatewayHandler := handler.NewOpenAIGatewayHandler(
		&service.OpenAIGatewayService{},
		service.NewConcurrencyService(nil),
		&service.BillingCacheService{},
		&service.APIKeyService{},
		nil, nil, nil, nil, nil,
		cfg,
	)
	return newGatewayRoutesTestRouterWithHandler(cfg, openAIGatewayHandler, true, platform...)
}

func newGatewayRoutesTestRouterWithHandler(
	cfg *config.Config,
	openAIGatewayHandler *handler.OpenAIGatewayHandler,
	withAuthSubject bool,
	platform ...string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	groupPlatform := service.PlatformOpenAI
	if len(platform) > 0 && platform[0] != "" {
		groupPlatform = platform[0]
	}
	RegisterGatewayRoutes(
		router,
		&handler.Handlers{
			Gateway:       &handler.GatewayHandler{},
			OpenAIGateway: openAIGatewayHandler,
		},
		servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
			groupID := int64(1)
			c.Set(string(servermiddleware.ContextKeyAPIKey), &service.APIKey{
				GroupID: &groupID,
				Group:   &service.Group{Platform: groupPlatform},
			})
			if withAuthSubject {
				c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{UserID: 1})
			}
			c.Next()
		}),
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
	)

	return router
}

func TestGatewayRoutesUseIndependentTextAndMediaBodyLimits(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxBodySize:     512,
		TextMaxBodySize: 128,
	}}
	router := newGatewayRoutesBodyLimitTestRouter(cfg, service.PlatformOpenAI)

	textBody := `{"model":"text-embedding-3-small","input":"` + strings.Repeat("t", 96) + `"}`
	for _, path := range []string{
		"/v1/alpha/search",
		"/alpha/search",
		"/backend-api/codex/alpha/search",
		"/v1/embeddings",
		"/embeddings",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(textBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "text path=%s", path)
	}

	// Content-Encoding 只会缩小线上传输字节数，解压和宽松 JSON 归一化后
	// 仍必须受 text_max_body_size 约束。
	compressedJSON := `{"model":"text-embedding-3-small","input":"` + strings.Repeat("z", 320) + `"}`
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	_, err := gzipWriter.Write([]byte(compressedJSON))
	require.NoError(t, err)
	require.NoError(t, gzipWriter.Close())
	require.Less(t, compressed.Len(), int(cfg.Gateway.TextMaxBodySize))
	for _, path := range []string{"/v1/alpha/search", "/v1/embeddings"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(compressed.Bytes()))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "gzip")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "compressed text path=%s", path)
	}

	// 图片、视频和可携带内联媒体的 Responses 仍使用较大的 max_body_size，
	// 不能因纯文本端点的 64 字节保护而被误拒绝。
	mediaBody := `{"model":"gpt-image-2","prompt":"` + strings.Repeat("m", 96) + `"}`
	for _, path := range []string{
		"/v1/images/generations",
		"/images/generations",
		"/v1/videos/generations",
		"/videos/generations",
		"/v1/responses",
		"/responses",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(mediaBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusRequestEntityTooLarge, w.Code, "media-capable path=%s", path)
	}

	tooLargeMediaBody := `{"model":"gpt-image-2","prompt":"` + strings.Repeat("m", 600) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(tooLargeMediaBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestGatewayRoutesBodyLimitsDoNotRejectBodylessGETOrWebSocketRoute(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{
		MaxBodySize:     512,
		TextMaxBodySize: 128,
	}}
	router := newGatewayRoutesBodyLimitTestRouter(cfg, service.PlatformOpenAI)

	for _, path := range []string{"/v1/responses", "/responses"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusRequestEntityTooLarge, w.Code, "GET path=%s", path)
	}
}

func TestGatewayRoutesGrokVideoTransformsRequireFeatureFlag(t *testing.T) {
	paths := []string{"/v1/videos/edits", "/videos/edits", "/v1/videos/extensions", "/videos/extensions"}
	disabled := newGatewayRoutesTestRouter(service.PlatformGrok)
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"grok-imagine-video","prompt":"change it","video":{"url":"https://example.com/source.mp4"}}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		disabled.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "path=%s must stay unregistered while feature flag is off", path)
	}

	enabledCfg := &config.Config{}
	enabledCfg.Gateway.GrokVideoTransformsEnabled = true
	enabled := newGatewayRoutesTestRouterWithConfig(enabledCfg, service.PlatformGrok)
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"grok-imagine-video","prompt":"change it","video":{"url":"https://example.com/source.mp4"}}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		enabled.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should reach Grok transform handler", path)
	}
}

func TestGatewayRoutesRegisterBothLiveProtocols(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /v1/live",
		"GET /v1/live/:call_id",
		"POST /backend-api/codex/realtime/calls",
		"GET /backend-api/codex/:call_id",
	} {
		require.True(t, registered[route], "missing Live route %s", route)
	}
}

func TestGatewayRoutesGrokImagesAndVideosPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformGrok)

	for _, path := range []string{
		"/v1/images/generations",
		"/v1/images/edits",
		"/images/generations",
		"/images/edits",
		"/v1/videos/generations",
		"/videos/generations",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"grok-imagine","prompt":"draw a cat"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit Grok media handler", path)
		require.NotContains(t, w.Body.String(), "not supported for this platform")
	}

	for _, path := range []string{
		"/v1/videos/request-123",
		"/videos/request-123",
		"/v1/videos/request-123/content",
		"/videos/request-123/content",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit Grok video handler", path)
		require.NotContains(t, w.Body.String(), "not supported for this platform")
	}
}

func TestGatewayRoutesRegisterGrokVoiceAndSearch(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformGrok)
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /v1/tts", "POST /v1/stt", "GET /v1/realtime", "POST /v1/web_search", "POST /v1/x_search", "POST /x_search",
		"POST /v1/custom-voices", "GET /v1/custom-voices", "GET /v1/custom-voices/:voice_id",
		"PATCH /v1/custom-voices/:voice_id", "DELETE /v1/custom-voices/:voice_id", "GET /v1/custom-voices/:voice_id/audio",
	} {
		require.True(t, registered[route], "missing Grok route %s", route)
	}
}

func TestGatewayRoutesXSearchRejectsNonGrokGroup(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)
	for _, path := range []string{"/v1/x_search", "/x_search"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"query":"latest xAI post"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "path=%s", path)
		require.Contains(t, w.Body.String(), "X Search API is not supported for this platform")
	}
}

func TestGatewayRoutesOpenAIVideosAreRejectedAtPlatformGate(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/videos/generations", `{"model":"grok-imagine-video","prompt":"waves"}`},
		{http.MethodPost, "/videos/generations", `{"model":"grok-imagine-video","prompt":"waves"}`},
		{http.MethodGet, "/v1/videos/request-123", ""},
		{http.MethodGet, "/videos/request-123", ""},
		{http.MethodGet, "/v1/videos/request-123/content", ""},
		{http.MethodGet, "/videos/request-123/content", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "method=%s path=%s", tc.method, tc.path)
		require.Contains(t, w.Body.String(), "Videos API is not supported for this platform")
	}
}

func TestGatewayRoutesAlphaSearchAliasesAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformOpenAI)

	for _, path := range []string{
		"/v1/alpha/search",
		"/alpha/search",
		"/backend-api/codex/alpha/search",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5.6-sol"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit alpha search handler", path)
	}
}

func TestGatewayRoutesUnsupportedPlatformVideosAreRejectedAtPlatformGate(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformAnthropic)

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/videos/generations", `{"model":"grok-imagine-video-1.5","prompt":"waves"}`},
		{http.MethodPost, "/videos/generations", `{"model":"grok-imagine-video-1.5","prompt":"waves"}`},
		{http.MethodGet, "/v1/videos/request-123", ""},
		{http.MethodGet, "/videos/request-123", ""},
		{http.MethodGet, "/v1/videos/request-123/content", ""},
		{http.MethodGet, "/videos/request-123/content", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "method=%s path=%s", tc.method, tc.path)
		require.Contains(t, w.Body.String(), "Videos API is not supported for this platform")
	}
}

func TestGatewayRoutesOpenAIResponsesCompactPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, path := range []string{
		"/v1/responses/compact",
		"/responses/compact",
		"/backend-api/codex/responses",
		"/backend-api/codex/responses/compact",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit OpenAI responses handler", path)
	}
}

func TestGatewayRoutesOpenAIImagesPathsAreRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	for _, path := range []string{
		"/v1/images/generations",
		"/v1/images/edits",
		"/images/generations",
		"/images/edits",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a cat"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, "path=%s should hit OpenAI images handler", path)
	}
}

func TestGatewayRoutesOpenAICountTokensPathIsRegistered(t *testing.T) {
	router := newGatewayRoutesTestRouter()

	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusNotFound, w.Code, "OpenAI groups should reach the count_tokens bridge instead of the old local 404 gate")
	require.NotContains(t, w.Body.String(), "Token counting is not supported for this platform")
}
