//go:build embed

package web

import (
	"bytes"
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestInjectSiteTitle(t *testing.T) {
	t.Run("replaces_title_with_site_name", func(t *testing.T) {
		html := []byte(`<html><head><title>Sub2API - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":"MyCustomSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Contains(t, string(result), "<title>MyCustomSite - AI API Gateway</title>")
		assert.NotContains(t, string(result), "Sub2API")
	})

	t.Run("returns_unchanged_when_site_name_empty", func(t *testing.T) {
		html := []byte(`<html><head><title>Sub2API - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":""}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_site_name_missing", func(t *testing.T) {
		html := []byte(`<html><head><title>Sub2API - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{"other_field":"value"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_invalid_json", func(t *testing.T) {
		html := []byte(`<html><head><title>Sub2API - AI API Gateway</title></head><body></body></html>`)
		settingsJSON := []byte(`{invalid json}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_no_title_tag", func(t *testing.T) {
		html := []byte(`<html><head></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":"MyCustomSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("returns_unchanged_when_title_has_attributes", func(t *testing.T) {
		// The function looks for "<title>" literally, so attributes are not supported
		// This is acceptable since index.html uses plain <title> without attributes
		html := []byte(`<html><head><title lang="en">Sub2API</title></head><body></body></html>`)
		settingsJSON := []byte(`{"site_name":"NewSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		// Should return unchanged since <title> with attributes is not matched
		assert.Equal(t, string(html), string(result))
	})

	t.Run("preserves_rest_of_html", func(t *testing.T) {
		html := []byte(`<html><head><meta charset="UTF-8"><title>Sub2API</title><script src="app.js"></script></head><body><div id="app"></div></body></html>`)
		settingsJSON := []byte(`{"site_name":"TestSite"}`)

		result := injectSiteTitle(html, settingsJSON)

		assert.Contains(t, string(result), `<meta charset="UTF-8">`)
		assert.Contains(t, string(result), `<script src="app.js"></script>`)
		assert.Contains(t, string(result), `<div id="app"></div>`)
		assert.Contains(t, string(result), "<title>TestSite - AI API Gateway</title>")
	})
}

func TestReplaceNoncePlaceholder(t *testing.T) {
	t.Run("replaces_single_placeholder", func(t *testing.T) {
		html := []byte(`<script nonce="__CSP_NONCE_VALUE__">console.log('test');</script>`)
		nonce := "abc123xyz"

		result := replaceNoncePlaceholder(html, nonce)

		expected := `<script nonce="abc123xyz">console.log('test');</script>`
		assert.Equal(t, expected, string(result))
	})

	t.Run("replaces_multiple_placeholders", func(t *testing.T) {
		html := []byte(`<script nonce="__CSP_NONCE_VALUE__">a</script><script nonce="__CSP_NONCE_VALUE__">b</script>`)
		nonce := "nonce123"

		result := replaceNoncePlaceholder(html, nonce)

		assert.Equal(t, 2, strings.Count(string(result), `nonce="nonce123"`))
		assert.NotContains(t, string(result), NonceHTMLPlaceholder)
	})

	t.Run("handles_empty_nonce", func(t *testing.T) {
		html := []byte(`<script nonce="__CSP_NONCE_VALUE__">test</script>`)
		nonce := ""

		result := replaceNoncePlaceholder(html, nonce)

		assert.Equal(t, `<script nonce="">test</script>`, string(result))
	})

	t.Run("no_placeholder_returns_unchanged", func(t *testing.T) {
		html := []byte(`<script>console.log('test');</script>`)
		nonce := "abc123"

		result := replaceNoncePlaceholder(html, nonce)

		assert.Equal(t, string(html), string(result))
	})

	t.Run("handles_empty_html", func(t *testing.T) {
		html := []byte(``)
		nonce := "abc123"

		result := replaceNoncePlaceholder(html, nonce)

		assert.Empty(t, result)
	})
}

func TestNonceHTMLPlaceholder(t *testing.T) {
	t.Run("constant_value", func(t *testing.T) {
		assert.Equal(t, "__CSP_NONCE_VALUE__", NonceHTMLPlaceholder)
	})
}

// mockSettingsProvider implements PublicSettingsProvider for testing
type mockSettingsProvider struct {
	settings any
	err      error
	called   int
}

func (m *mockSettingsProvider) GetPublicSettingsForInjection(ctx context.Context) (any, error) {
	m.called++
	return m.settings, m.err
}

func TestFrontendServer_InjectSettings(t *testing.T) {
	t.Run("injects_settings_with_nonce_placeholder", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"key": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		settingsJSON := []byte(`{"test":"data"}`)
		result := server.injectSettings(settingsJSON)

		// Should contain the script with nonce placeholder
		assert.Contains(t, string(result), `<script nonce="__CSP_NONCE_VALUE__">`)
		assert.Contains(t, string(result), `window.__APP_CONFIG__={"test":"data"};`)
		assert.Contains(t, string(result), `</script></head>`)
	})

	t.Run("injects_before_head_close", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"key": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		settingsJSON := []byte(`{}`)
		result := server.injectSettings(settingsJSON)

		// Script should be injected before </head>
		headCloseIndex := bytes.Index(result, []byte("</head>"))
		scriptIndex := bytes.Index(result, []byte(`<script nonce="`))

		assert.True(t, scriptIndex < headCloseIndex, "script should be before </head>")
	})

	t.Run("handles_complex_settings", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]any{
				"nested": map[string]any{
					"array": []int{1, 2, 3},
				},
			},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		settingsJSON := []byte(`{"nested":{"array":[1,2,3]},"special":"<>&"}`)
		result := server.injectSettings(settingsJSON)

		assert.Contains(t, string(result), `window.__APP_CONFIG__={"nested":{"array":[1,2,3]},"special":"<>&"};`)
	})
}

func TestFrontendServer_GatewayLitePrivateLoginVariant(t *testing.T) {
	cfg := &config.Config{
		RunMode: config.RunModeGatewayLite,
		GatewayLite: config.GatewayLiteConfig{
			AdminLoginPath: "/private-console",
			LoginTitle:     "Private Gateway",
			LoginSubtitle:  "Authorized operators only",
		},
	}
	server, err := NewFrontendServer(&mockSettingsProvider{settings: map[string]string{"site_name": "Gateway"}}, cfg)
	require.NoError(t, err)

	allowedHTML, allowedETag := server.renderRequestVariant("/private-console", server.baseHTML, `"base"`)
	deniedHTML, deniedETag := server.renderRequestVariant("/login", server.baseHTML, `"base"`)

	require.Contains(t, string(allowedHTML), `window.__GATEWAY_LITE_LOGIN_ENTRY__={"enabled":true,"allowed":true,"title":"Private Gateway","subtitle":"Authorized operators only"}`)
	require.Contains(t, string(deniedHTML), `window.__GATEWAY_LITE_LOGIN_ENTRY__={"enabled":true,"allowed":false}`)
	require.NotContains(t, string(deniedHTML), "Private Gateway")
	require.NotEqual(t, allowedETag, deniedETag, "private login and neutral 404 HTML must never share an ETag")

	cfg.GatewayLite.AdminLoginPath = "/rotated-console"
	oldHTML, _ := server.renderRequestVariant("/private-console", server.baseHTML, `"base"`)
	newHTML, _ := server.renderRequestVariant("/rotated-console", server.baseHTML, `"base"`)
	require.Contains(t, string(oldHTML), `"allowed":false`)
	require.Contains(t, string(newHTML), `"allowed":true`)
}

func TestFrontendServer_ServeIndexHTML(t *testing.T) {
	t.Run("serves_html_with_nonce", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		// Create a gin context with nonce
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

		// Set nonce in context (simulating SecurityHeaders middleware)
		testNonce := "test-nonce-12345"
		c.Set(middleware.CSPNonceKey, testNonce)

		server.serveIndexHTML(c)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")

		body := w.Body.String()
		// Nonce placeholder should be replaced
		assert.NotContains(t, body, NonceHTMLPlaceholder)
		assert.Contains(t, body, `nonce="`+testNonce+`"`)
	})

	t.Run("caches_html_content", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		// First request
		w1 := httptest.NewRecorder()
		c1, _ := gin.CreateTestContext(w1)
		c1.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c1.Set(middleware.CSPNonceKey, "nonce1")

		server.serveIndexHTML(c1)
		assert.Equal(t, 1, provider.called)

		// Second request - should use cache
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c2.Set(middleware.CSPNonceKey, "nonce2")

		server.serveIndexHTML(c2)
		// Settings provider should not be called again
		assert.Equal(t, 1, provider.called)

		// But nonce should be different
		assert.Contains(t, w2.Body.String(), `nonce="nonce2"`)
	})

	t.Run("sets_etag_header", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "nonce123")

		server.serveIndexHTML(c)

		etag := w.Header().Get("ETag")
		assert.NotEmpty(t, etag)
		assert.True(t, strings.HasPrefix(etag, `"`))
		assert.True(t, strings.HasSuffix(etag, `"`))
	})

	t.Run("returns_304_for_matching_etag", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		// Use a real router for proper 304 handling
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.CSPNonceKey, "test-nonce")
			c.Next()
		})
		router.Use(server.Middleware())

		// First request to populate cache and get ETag
		w1 := httptest.NewRecorder()
		req1 := httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(w1, req1)
		etag := w1.Header().Get("ETag")
		require.NotEmpty(t, etag)

		// Second request with If-None-Match
		w2 := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, "/", nil)
		req2.Header.Set("If-None-Match", etag)
		router.ServeHTTP(w2, req2)

		assert.Equal(t, http.StatusNotModified, w2.Code)
		assert.Empty(t, w2.Body.String())
	})

	t.Run("sets_cache_control_header", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "nonce123")

		server.serveIndexHTML(c)

		assert.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	})

	t.Run("fallback_on_settings_error", func(t *testing.T) {
		provider := &mockSettingsProvider{
			err: context.DeadlineExceeded,
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		// Invalidate cache to force settings fetch
		server.InvalidateCache()

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "nonce123")

		server.serveIndexHTML(c)

		// Should still return 200 with base HTML
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	})
}

func TestFrontendServer_InvalidateCache(t *testing.T) {
	t.Run("invalidates_cache", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		// First request to populate cache
		w1 := httptest.NewRecorder()
		c1, _ := gin.CreateTestContext(w1)
		c1.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c1.Set(middleware.CSPNonceKey, "nonce1")

		server.serveIndexHTML(c1)
		assert.Equal(t, 1, provider.called)

		// Invalidate cache
		server.InvalidateCache()

		// Update settings
		provider.settings = map[string]string{"test": "new_value"}

		// Second request should fetch new settings
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c2.Set(middleware.CSPNonceKey, "nonce2")

		server.serveIndexHTML(c2)
		assert.Equal(t, 2, provider.called)
	})

	t.Run("handles_nil_server", func(t *testing.T) {
		var server *FrontendServer
		// Should not panic
		assert.NotPanics(t, func() {
			server.InvalidateCache()
		})
	})

	t.Run("handles_nil_cache", func(t *testing.T) {
		server := &FrontendServer{}
		// Should not panic
		assert.NotPanics(t, func() {
			server.InvalidateCache()
		})
	})
}

func TestFrontendServer_Middleware(t *testing.T) {
	t.Run("skips_api_routes", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		apiPaths := []string{
			"/api/v1/users",
			"/internal/control-plane/events",
			"/models",
			"/v1/models",
			"/v1beta/chat",
			"/backend-api/codex/responses",
			"/backend-api/codex/responses/compact",
			"/antigravity/test",
			"/setup/init",
			"/health",
			"/ready",
			"/responses",
			"/responses/compact",
			"/images/generations",
			"/videos/generations",
			"/videos/request-123",
			// [v0.2.14 对齐] 根级裸别名不得被 SPA 回退吞掉（曾返回 200 + text/html）
			"/chat/completions",
			"/embeddings",
			"/messages/count_tokens",
			"/web_search",
			"/x_search",
			"/tts",
			"/stt",
			"/realtime",
			"/custom-voices",
			"/custom-voices/voice-123",
			"/custom-voices/voice-123/audio",
			"/videos",
		}

		for _, path := range apiPaths {
			t.Run(path, func(t *testing.T) {
				router := gin.New()
				router.Use(server.Middleware())
				nextCalled := false
				router.GET(path, func(c *gin.Context) {
					nextCalled = true
					c.String(http.StatusOK, "ok")
				})

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.True(t, nextCalled, "next handler should be called for API route")
			})
		}
	})

	t.Run("unregistered_video_api_route_returns_404_instead_of_spa_html", func(t *testing.T) {
		server, err := NewFrontendServer(&mockSettingsProvider{settings: map[string]string{"test": "value"}}, nil)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/videos/edits", strings.NewReader(`{"model":"grok-imagine-video"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.NotContains(t, w.Header().Get("Content-Type"), "text/html")
	})

	t.Run("skips_responses_compact_post_routes", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())
		nextCalled := false
		router.POST("/responses/compact", func(c *gin.Context) {
			nextCalled = true
			c.String(http.StatusOK, `{"ok":true}`)
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/responses/compact", strings.NewReader(`{"model":"gpt-5"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.True(t, nextCalled, "next handler should be called for compact API route")
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"ok":true}`, w.Body.String())
	})

	t.Run("skips_alpha_search_post_route", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())
		nextCalled := false
		router.POST("/alpha/search", func(c *gin.Context) {
			nextCalled = true
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/alpha/search", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)

		assert.True(t, nextCalled, "next handler should be called for alpha search API route")
		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"ok":true}`, w.Body.String())
	})

	t.Run("serves_index_for_spa_routes", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(middleware.CSPNonceKey, "test-nonce")
			c.Next()
		})
		router.Use(server.Middleware())

		spaPaths := []string{
			"/",
			"/dashboard",
			"/users/123",
			"/settings/profile",
		}

		for _, path := range spaPaths {
			t.Run(path, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			})
		}
	})

	t.Run("serves_static_files", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		router := gin.New()
		router.Use(server.Middleware())

		// Request for existing static file
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/logo-mark.png", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "image/png")
	})
}

func TestNewFrontendServer(t *testing.T) {
	t.Run("creates_server_successfully", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)

		require.NoError(t, err)
		assert.NotNil(t, server)
		assert.NotNil(t, server.distFS)
		assert.NotNil(t, server.fileServer)
		assert.NotNil(t, server.baseHTML)
		assert.NotNil(t, server.cache)
		assert.Equal(t, provider, server.settings)
	})

	t.Run("reads_base_html", func(t *testing.T) {
		provider := &mockSettingsProvider{
			settings: map[string]string{"test": "value"},
		}

		server, err := NewFrontendServer(provider, nil)
		require.NoError(t, err)

		assert.NotEmpty(t, server.baseHTML)
		assert.Contains(t, strings.ToLower(string(server.baseHTML)), "<!doctype html>")
	})
}

func TestHasEmbeddedFrontend(t *testing.T) {
	t.Run("returns_true_when_frontend_embedded", func(t *testing.T) {
		result := HasEmbeddedFrontend()
		assert.True(t, result)
	})
}

// Tests for legacy ServeEmbeddedFrontend function
func TestServeEmbeddedFrontend(t *testing.T) {
	t.Run("serves_static_files", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		router := gin.New()
		router.Use(middleware)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/logo-mark.png", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "image/png")
	})

	t.Run("serves_index_html_for_root", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		router := gin.New()
		router.Use(middleware)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
		assert.Contains(t, strings.ToLower(w.Body.String()), "<!doctype html>")
	})

	t.Run("serves_index_html_for_spa_routes", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		router := gin.New()
		router.Use(middleware)

		spaPaths := []string{"/dashboard", "/users/123", "/settings"}

		for _, path := range spaPaths {
			t.Run(path, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
			})
		}
	})

	t.Run("skips_api_routes", func(t *testing.T) {
		middleware := ServeEmbeddedFrontend()

		apiPaths := []string{
			"/api/users",
			"/internal/control-plane/events",
			"/models",
			"/v1/models",
			"/v1beta/chat",
			"/backend-api/codex/responses",
			"/backend-api/codex/responses/compact",
			"/antigravity/test",
			"/setup/init",
			"/health",
			"/ready",
			"/responses",
			"/responses/compact",
			"/alpha/search",
		}

		for _, path := range apiPaths {
			t.Run(path, func(t *testing.T) {
				nextCalled := false
				router := gin.New()
				router.Use(middleware)
				router.GET(path, func(c *gin.Context) {
					nextCalled = true
					c.String(http.StatusOK, "ok")
				})

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				router.ServeHTTP(w, req)

				assert.True(t, nextCalled, "next handler should be called for API route")
			})
		}
	})
}

// 当前首页引用随构建变化，验证真实嵌入文件与响应，避免硬编码指纹文件名。
func TestEmbeddedFrontendCurrentAssetContract(t *testing.T) {
	base, err := frontendFS.ReadFile("dist/index.html")
	require.NoError(t, err)
	tokenizer := html.NewTokenizer(bytes.NewReader(base))
	assets := map[string]bool{}
	for {
		tokenType := tokenizer.Next()
		if tokenType == html.ErrorToken {
			require.ErrorIs(t, tokenizer.Err(), io.EOF)
			break
		}
		if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
			continue
		}
		token := tokenizer.Token()
		if token.Data != "script" && token.Data != "link" {
			continue
		}
		for _, attr := range token.Attr {
			if attr.Key != "src" && attr.Key != "href" {
				continue
			}
			target, err := url.Parse(attr.Val)
			require.NoError(t, err)
			if target.IsAbs() || target.Host != "" || target.Path == "" {
				continue
			}
			assets["/"+strings.TrimPrefix(target.Path, "/")] = true
		}
	}
	require.NotEmpty(t, assets)
	var hasJS, hasCSS bool
	for asset := range assets {
		hasJS = hasJS || path.Ext(asset) == ".js"
		hasCSS = hasCSS || path.Ext(asset) == ".css"
	}
	require.True(t, hasJS, "首页必须引用应用脚本")
	require.True(t, hasCSS, "首页必须引用应用样式")
	for _, mode := range []string{"injected", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			router := gin.New()
			if mode == "injected" {
				server, err := NewFrontendServer(nil, nil)
				require.NoError(t, err)
				server.overrideDir = ""
				router.Use(server.Middleware())
			} else {
				router.Use(ServeEmbeddedFrontend())
			}
			for asset := range assets {
				t.Run(asset, func(t *testing.T) {
					expected, err := frontendFS.ReadFile("dist" + asset)
					require.NoError(t, err)
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, asset, nil))
					require.Equal(t, http.StatusOK, response.Code)
					require.Equal(t, expected, response.Body.Bytes())
					actualType, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
					require.NoError(t, err)
					expectedType, _, err := mime.ParseMediaType(mime.TypeByExtension(path.Ext(asset)))
					require.NoError(t, err)
					require.Equal(t, expectedType, actualType)
					require.NotEqual(t, "text/html", actualType)
				})
			}
			for _, missing := range []string{"/assets/missing.js", "/assets/missing.css", "/assets/missing-chunk", "/missing.png", "/missing.wasm", "/missing.woff2"} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, missing, nil))
				require.Equal(t, http.StatusNotFound, response.Code, missing)
				require.NotContains(t, strings.ToLower(response.Body.String()), "<!doctype html>", missing)
			}
			for _, page := range []string{"/", "/dashboard", "/settings/gateway", "/users/example.com"} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, page, nil))
				require.Equal(t, http.StatusOK, response.Code, page)
				require.Contains(t, response.Header().Get("Content-Type"), "text/html")
			}
			for _, api := range []string{"/chat/completions", "/embeddings", "/messages/count_tokens", "/web_search", "/x_search", "/tts", "/stt", "/realtime", "/custom-voices", "/images/generations", "/videos", "/internal/control-plane/events"} {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, api, strings.NewReader(`{}`)))
				require.Equal(t, http.StatusNotFound, response.Code, api)
				require.NotContains(t, response.Header().Get("Content-Type"), "text/html", api)
			}
		})
	}
}

// Tests for HTMLCache
func TestHTMLCache(t *testing.T) {
	t.Run("new_cache_returns_nil", func(t *testing.T) {
		cache := NewHTMLCache()
		assert.Nil(t, cache.Get())
	})

	t.Run("set_and_get", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		html := []byte("<html><body>test</body></html>")
		settings := []byte(`{"key":"value"}`)
		cache.Set(html, settings)

		result := cache.Get()
		require.NotNil(t, result)
		assert.Equal(t, html, result.Content)
		assert.NotEmpty(t, result.ETag)
	})

	t.Run("invalidate_clears_cache", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		html := []byte("<html><body>test</body></html>")
		settings := []byte(`{"key":"value"}`)
		cache.Set(html, settings)

		require.NotNil(t, cache.Get())

		cache.Invalidate()

		assert.Nil(t, cache.Get())
	})

	t.Run("etag_changes_with_settings", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		html := []byte("<html><body>test</body></html>")

		cache.Set(html, []byte(`{"v":1}`))
		etag1 := cache.Get().ETag

		cache.Invalidate()
		cache.Set(html, []byte(`{"v":2}`))
		etag2 := cache.Get().ETag

		assert.NotEqual(t, etag1, etag2)
	})

	t.Run("etag_format", func(t *testing.T) {
		cache := NewHTMLCache()
		cache.SetBaseHTML([]byte("<html></html>"))

		cache.Set([]byte("<html></html>"), []byte(`{}`))
		result := cache.Get()

		// ETag should be quoted
		assert.True(t, strings.HasPrefix(result.ETag, `"`))
		assert.True(t, strings.HasSuffix(result.ETag, `"`))
		// Should contain dash separator
		assert.Contains(t, result.ETag[1:len(result.ETag)-1], "-")
	})
}

// [v0.2.14 对齐] 回归护栏：routes/gateway.go 里的根级裸别名必须全部放行，
// 同时保证普通前端路由不被误放行（否则 SPA 页面会 404）。
// 该名单曾两次漏改（d331d3796、c6024c328 修复同类问题），故按「注册即需放行」建表锁死。
func TestEmbeddedFrontendBypassesBareAPIAliases(t *testing.T) {
	for _, path := range []string{
		"/chat/completions",
		"/embeddings",
		"/messages/count_tokens",
		"/web_search",
		"/x_search",
		"/tts",
		"/stt",
		"/realtime",
		"/custom-voices",
		"/custom-voices/voice-123",
		"/custom-voices/voice-123/audio",
		"/videos",
		"/videos/request-123",
		"/v3/contents/generations/tasks",
		"/v3/contents/generations/tasks/task-123",
		"/contents/generations/tasks",
		"/contents/generations/tasks/task-123",
	} {
		assert.True(t, shouldBypassEmbeddedFrontend(path), "裸别名应放行: %s", path)
	}

	// 负例：这些是前端页面路由，必须留给 SPA 回退，不能当 API 放行
	for _, path := range []string{
		"/",
		"/dashboard",
		"/groups",
		"/model-plaza",
		"/settings",
	} {
		assert.False(t, shouldBypassEmbeddedFrontend(path), "前端路由不应放行: %s", path)
	}
}

// Benchmark tests
func BenchmarkReplaceNoncePlaceholder(b *testing.B) {
	html := []byte(`<!DOCTYPE html><html><head><script nonce="__CSP_NONCE_VALUE__">window.__APP_CONFIG__={"test":"data"};</script></head><body></body></html>`)
	nonce := "abcdefghijklmnop123456=="

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		replaceNoncePlaceholder(html, nonce)
	}
}

func BenchmarkFrontendServerServeIndexHTML(b *testing.B) {
	provider := &mockSettingsProvider{
		settings: map[string]string{"test": "value"},
	}

	server, _ := NewFrontendServer(provider, nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Set(middleware.CSPNonceKey, "test-nonce")

		server.serveIndexHTML(c)
	}
}
