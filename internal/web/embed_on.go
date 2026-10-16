//go:build embed

package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	htmlpkg "html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

const (
	// NonceHTMLPlaceholder is the placeholder for nonce in HTML script tags
	NonceHTMLPlaceholder = "__CSP_NONCE_VALUE__"
)

//go:embed all:dist
var frontendFS embed.FS

// PublicSettingsProvider is an interface to fetch public settings
type PublicSettingsProvider interface {
	GetPublicSettingsForInjection(ctx context.Context) (any, error)
}

// FrontendServer serves the embedded frontend with settings injection
type FrontendServer struct {
	distFS      fs.FS
	fileServer  http.Handler
	baseHTML    []byte
	cache       *HTMLCache
	settings    PublicSettingsProvider
	cfg         *config.Config // [tokenav 定制] lite 登录入口需要读配置
	overrideDir string         // local file override directory
}

// NewFrontendServer creates a new frontend server with settings injection
func NewFrontendServer(settingsProvider PublicSettingsProvider, cfg *config.Config) (*FrontendServer, error) { // [tokenav 定制] 增加 cfg
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		return nil, err
	}

	// Read base HTML once
	file, err := distFS.Open("index.html")
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	baseHTML, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	cache := NewHTMLCache()
	cache.SetBaseHTML(baseHTML)

	return &FrontendServer{
		distFS:      distFS,
		fileServer:  http.FileServer(http.FS(distFS)),
		baseHTML:    baseHTML,
		cache:       cache,
		settings:    settingsProvider,
		cfg:         cfg,
		overrideDir: filepath.Join("data", "public"),
	}, nil
}

// InvalidateCache invalidates the HTML cache (call when settings change)
func (s *FrontendServer) InvalidateCache() {
	if s != nil && s.cache != nil {
		s.cache.Invalidate()
	}
}

// Middleware returns the Gin middleware handler
func (s *FrontendServer) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Skip API routes
		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		// 首页使用注入版本；本地覆盖资产优先于嵌入资产。
		if cleanPath == "index.html" {
			s.serveIndexHTML(c)
			return
		}
		if s.tryServeOverride(c, cleanPath) {
			return
		}
		if !s.fileExists(cleanPath) {
			// 缺失脚本/样式不能返回 200 HTML，否则浏览器缓存并报难以定位的语法错误。
			if isStaticAssetRequest(cleanPath) {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			s.serveIndexHTML(c)
			return
		}

		// [优化] 指纹资产优先发送构建时生成的预压缩 .gz（省约 3 倍传输）。
		// 浏览器不支持 gzip 或无 .gz 文件时回退原文件，行为不变。
		if supportsGzip(c.Request) && isPrecompressibleAsset(cleanPath) {
			if s.fileExists(cleanPath + ".gz") {
				w := c.Writer.Header()
				applyStaticAssetCacheHeaders(w, cleanPath)
				// Content-Type 必须按原扩展名设置（.gz 会被 Go mime 误判为 application/gzip）
				w.Set("Content-Type", mime.TypeByExtension(filepath.Ext(cleanPath)))
				w.Set("Content-Encoding", "gzip")
				w.Add("Vary", "Accept-Encoding")
				c.Writer.WriteHeader(http.StatusOK)
				if f, err := s.distFS.Open(cleanPath + ".gz"); err == nil {
					_, _ = io.Copy(c.Writer, f)
					_ = f.Close()
				}
				c.Abort()
				return
			}
		}

		// Serve static files normally (hashed assets get long-lived cache headers)
		applyStaticAssetCacheHeaders(c.Writer.Header(), cleanPath)
		s.fileServer.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

// isStaticAssetRequest 区分静态资源与应用深链；不按任意点号判断，
// 因为用户页面参数和私有登录入口也可能包含点号。
func isStaticAssetRequest(path string) bool {
	if path == "assets" || strings.HasPrefix(path, "assets/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".mjs", ".css", ".map", ".json", ".html", ".txt", ".xml", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".ico", ".woff", ".woff2", ".ttf", ".otf", ".wasm", ".webmanifest", ".gz":
		return true
	}
	return false
}

// supportsGzip 报告客户端是否接受 gzip 编码。
func supportsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// isPrecompressibleAsset 报告路径是否为值得预压缩的文本资产。
func isPrecompressibleAsset(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".css", ".svg", ".json", ".html", ".txt":
		return true
	}
	return false
}

func (s *FrontendServer) fileExists(path string) bool {
	file, err := s.distFS.Open(path)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

// tryServeOverride checks if a local override file exists and serves it.
// Files in overrideDir take precedence over embedded files.
func (s *FrontendServer) tryServeOverride(c *gin.Context, cleanPath string) bool {
	if s.overrideDir == "" {
		return false
	}
	filePath := filepath.Join(s.overrideDir, filepath.Clean("/"+cleanPath))
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(filePath)
	c.Abort()
	return true
}

func (s *FrontendServer) serveIndexHTML(c *gin.Context) {
	// Get nonce from context (generated by SecurityHeaders middleware)
	nonce := middleware.GetNonceFromContext(c)

	// [健壮性] 无 settings provider（如测试构造）时直接回退基础 HTML，避免 nil 解引用
	if s.settings == nil {
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	// Check cache first
	cached := s.cache.Get()
	if cached != nil {
		// [tokenav 定制] lite 登录入口变体处理
		content, etag := s.renderRequestVariant(c.Request.URL.Path, cached.Content, cached.ETag)

		// Check If-None-Match for 304 response
		if match := c.GetHeader("If-None-Match"); match == etag {
			c.Status(http.StatusNotModified)
			c.Abort()
			return
		}

		// Replace nonce placeholder with actual nonce before serving
		content = replaceNoncePlaceholder(content, nonce)

		c.Header("ETag", etag)
		c.Header("Cache-Control", "no-cache") // Must revalidate
		c.Data(http.StatusOK, "text/html; charset=utf-8", content)
		c.Abort()
		return
	}

	// Cache miss - fetch settings and render
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	settings, err := s.settings.GetPublicSettingsForInjection(ctx)
	if err != nil {
		// Fallback: serve without injection
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	settingsJSON, err := json.Marshal(settings)
	if err != nil {
		// Fallback: serve without injection
		c.Data(http.StatusOK, "text/html; charset=utf-8", s.baseHTML)
		c.Abort()
		return
	}

	rendered := s.injectSettings(settingsJSON)
	s.cache.Set(rendered, settingsJSON)
	cached = s.cache.Get()
	etag := ""
	if cached != nil {
		etag = cached.ETag
	}

	// [tokenav 定制] lite 登录入口变体处理
	rendered, _ = s.renderRequestVariant(c.Request.URL.Path, rendered, etag)

	// Replace nonce placeholder with actual nonce before serving
	content := replaceNoncePlaceholder(rendered, nonce)

	cached = s.cache.Get()
	if cached != nil {
		c.Header("ETag", cached.ETag)
	}
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func (s *FrontendServer) injectSettings(settingsJSON []byte) []byte {
	// Create the script tag to inject with nonce placeholder
	// The placeholder will be replaced with actual nonce at request time
	script := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__APP_CONFIG__=` + string(settingsJSON) + `;</script>`)

	// Inject before </head>
	headClose := []byte("</head>")
	result := bytes.Replace(s.baseHTML, headClose, append(script, headClose...), 1)

	// Apply custom branding before the browser paints the static defaults.
	result = injectSiteTitle(result, settingsJSON)
	result = injectSiteFavicon(result, settingsJSON)

	return result
}

// injectSiteFavicon replaces the static favicon with a configured, browser-safe image URL.
func injectSiteFavicon(html, settingsJSON []byte) []byte {
	var cfg struct {
		SiteLogo string `json:"site_logo"`
	}
	if err := json.Unmarshal(settingsJSON, &cfg); err != nil {
		return html
	}

	logoURL := safeImageURL(cfg.SiteLogo)
	if logoURL == "" {
		return html
	}

	linkStart := bytes.Index(html, []byte(`<link rel="icon"`))
	if linkStart == -1 {
		return html
	}
	linkEndOffset := bytes.IndexByte(html[linkStart:], '>')
	if linkEndOffset == -1 {
		return html
	}
	linkEnd := linkStart + linkEndOffset + 1
	replacement := []byte(`<link rel="icon" href="` + htmlpkg.EscapeString(logoURL) + `" />`)

	var buf bytes.Buffer
	buf.Write(html[:linkStart])
	buf.Write(replacement)
	buf.Write(html[linkEnd:])
	return buf.Bytes()
}

func safeImageURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
		return trimmed
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return trimmed
}

// injectSiteTitle replaces the static <title> in HTML with the configured site name.
// This ensures the browser tab shows the correct title before JS executes.
func injectSiteTitle(html, settingsJSON []byte) []byte {
	var cfg struct {
		SiteName string `json:"site_name"`
	}
	if err := json.Unmarshal(settingsJSON, &cfg); err != nil || cfg.SiteName == "" {
		return html
	}

	// Find and replace the existing <title>...</title>
	titleStart := bytes.Index(html, []byte("<title>"))
	titleEnd := bytes.Index(html, []byte("</title>"))
	if titleStart == -1 || titleEnd == -1 || titleEnd <= titleStart {
		return html
	}

	newTitle := []byte("<title>" + htmlpkg.EscapeString(cfg.SiteName) + " - AI API Gateway</title>")
	var buf bytes.Buffer
	buf.Write(html[:titleStart])
	buf.Write(newTitle)
	buf.Write(html[titleEnd+len("</title>"):])
	return buf.Bytes()
}

// replaceNoncePlaceholder replaces the nonce placeholder with actual nonce value
func replaceNoncePlaceholder(html []byte, nonce string) []byte {
	return bytes.ReplaceAll(html, []byte(NonceHTMLPlaceholder), []byte(nonce))
}

// ServeEmbeddedFrontend returns a middleware for serving embedded frontend
// This is the legacy function for backward compatibility when no settings provider is available
func ServeEmbeddedFrontend(configs ...*config.Config) gin.HandlerFunc { // [tokenav 定制] 可变参兼容
	distFS, err := fs.Sub(frontendFS, "dist")
	if err != nil {
		panic("failed to get dist subdirectory: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))
	overrideDir := filepath.Join("data", "public")

	return func(c *gin.Context) {
		path := c.Request.URL.Path

		if shouldBypassEmbeddedFrontend(path) {
			c.Next()
			return
		}

		cleanPath := strings.TrimPrefix(path, "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}

		if tryServeOverrideFile(c, overrideDir, cleanPath) {
			return
		}
		if file, err := distFS.Open(cleanPath); err == nil {
			_ = file.Close()
			applyStaticAssetCacheHeaders(c.Writer.Header(), cleanPath)
			fileServer.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		if isStaticAssetRequest(cleanPath) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		serveIndexHTML(c, distFS)
	}
}

// tryServeOverrideFile is a standalone version of tryServeOverride for legacy usage.
func tryServeOverrideFile(c *gin.Context, overrideDir, cleanPath string) bool {
	if overrideDir == "" {
		return false
	}
	filePath := filepath.Join(overrideDir, filepath.Clean("/"+cleanPath))
	info, err := os.Stat(filePath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(filePath)
	c.Abort()
	return true
}

func shouldBypassEmbeddedFrontend(path string) bool {
	trimmed := strings.TrimSpace(path)
	// [v0.2.6] /internal/ 为 control-plane → 网关的事件推送端点（如缓存失效），
	// 不得被嵌入前端 SPA 回退拦截。
	// [v0.2.7] /v3/ 与 /contents/generations/tasks 为 Seedance Ark 原生协议入口。
	return strings.HasPrefix(trimmed, "/internal/") ||
		strings.HasPrefix(trimmed, "/api/") ||
		strings.HasPrefix(trimmed, "/v3/") ||
		trimmed == "/contents/generations/tasks" ||
		strings.HasPrefix(trimmed, "/contents/generations/tasks/") ||
		strings.HasPrefix(trimmed, "/v1/") ||
		strings.HasPrefix(trimmed, "/v1beta/") ||
		strings.HasPrefix(trimmed, "/backend-api/") ||
		strings.HasPrefix(trimmed, "/antigravity/") ||
		strings.HasPrefix(trimmed, "/setup/") ||
		trimmed == "/health" ||
		trimmed == "/ready" ||
		trimmed == "/models" ||
		trimmed == "/responses" ||
		strings.HasPrefix(trimmed, "/responses/") ||
		trimmed == "/alpha/search" ||
		// [v0.2.14 对齐] routes/gateway.go 注册的根级裸别名（不带 /v1 前缀）。
		// 该中间件先于路由注册，漏列的别名会被嵌入前端 SPA 回退吞掉，
		// 客户端收到 200 + text/html 而不是 JSON，解析必然失败。
		// 名单按我方实际注册的别名逐条核对，不能照抄官方清单：
		// 我们多出 /v1beta/、/antigravity/，官方多出 /models/:model（我方未注册）。
		trimmed == "/chat/completions" ||
		trimmed == "/embeddings" ||
		trimmed == "/messages/count_tokens" ||
		trimmed == "/web_search" ||
		trimmed == "/x_search" ||
		trimmed == "/tts" ||
		trimmed == "/stt" ||
		trimmed == "/realtime" ||
		trimmed == "/custom-voices" ||
		strings.HasPrefix(trimmed, "/custom-voices/") ||
		strings.HasPrefix(trimmed, "/images/") ||
		trimmed == "/videos" ||
		strings.HasPrefix(trimmed, "/videos/")
}

func serveIndexHTML(c *gin.Context, fsys fs.FS) {
	file, err := fsys.Open("index.html")
	if err != nil {
		c.String(http.StatusNotFound, "Frontend not found")
		c.Abort()
		return
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(file)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to read index.html")
		c.Abort()
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", content)
	c.Abort()
}

func HasEmbeddedFrontend() bool {
	_, err := frontendFS.ReadFile("dist/index.html")
	return err == nil
}

// ===== tokenav lite 定制（升级时保留）=====

func injectGatewayLiteLoginEntry(html, payloadJSON []byte) []byte {
	script := []byte(`<script nonce="` + NonceHTMLPlaceholder + `">window.__GATEWAY_LITE_LOGIN_ENTRY__=` + string(payloadJSON) + `;</script>`)
	headClose := []byte("</head>")
	return bytes.Replace(html, headClose, append(script, headClose...), 1)
}

func gatewayLiteVariantETag(baseETag string, payloadJSON []byte) string {
	hash := sha256.Sum256(append([]byte(baseETag+":"), payloadJSON...))
	return `"` + hex.EncodeToString(hash[:12]) + `"`
}

// ===== tokenav lite 定制：登录入口注入（升级时保留）=====
type gatewayLiteLoginEntryPayload struct {
	Enabled  bool   `json:"enabled"`
	Allowed  bool   `json:"allowed"`
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
}

func (s *FrontendServer) renderRequestVariant(requestPath string, html []byte, baseETag string) ([]byte, string) {
	if s == nil || s.cfg == nil || s.cfg.RunMode != config.RunModeGatewayLite {
		return html, baseETag
	}
	configuredPath := config.NormalizeGatewayLiteAdminLoginPath(s.cfg.GatewayLite.AdminLoginPath)
	allowed := normalizeGatewayLiteRequestPath(requestPath) == configuredPath
	payload := gatewayLiteLoginEntryPayload{Enabled: true, Allowed: allowed}
	if allowed {
		payload.Title = config.NormalizeGatewayLiteLoginTitle(s.cfg.GatewayLite.LoginTitle)
		payload.Subtitle = config.NormalizeGatewayLiteLoginSubtitle(s.cfg.GatewayLite.LoginSubtitle)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		payloadJSON = []byte(`{"enabled":true,"allowed":false}`)
	}
	return injectGatewayLiteLoginEntry(html, payloadJSON), gatewayLiteVariantETag(baseETag, payloadJSON)
}

func normalizeGatewayLiteRequestPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if len(value) > 1 {
		value = strings.TrimRight(value, "/")
	}
	return value
}
