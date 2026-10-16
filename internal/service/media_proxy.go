package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

const (
	mediaProxyTokenVersion       = "mp1"
	mediaProxyObjectTokenVersion = "mp2"
	mediaProxyPath               = "/v1/media/"
	mediaProxyDefaultTTL         = 15 * time.Minute
	mediaProxyMaxTokenSize       = 16 << 10
	mediaProxyMaxURLSize         = 8 << 10
	mediaProxyMaxBodySize        = 32 << 20
	mediaProxyMaxRedirects       = 3
)

var (
	errMediaProxyTokenInvalid = errors.New("media proxy token is invalid or expired")
	errMediaProxyTargetUnsafe = errors.New("media proxy target URL is not allowed")
)

// MediaProxy 是生图输出 URL 的受限中转器。令牌使用 AES-GCM（带认证的加密）
// 封装目标地址，因此客户端既不能篡改地址，也看不到上游签名参数。
// 该类型不接受来自请求的任意 URL；只有服务端从上游响应中签发的令牌可读取。
type MediaProxy struct {
	aead      cipher.AEAD
	publicURL string
	ttl       time.Duration
	client    *http.Client
	objects   SyncMediaObjectStore
}

// MediaProxyResponse 是中转读取结果，调用方负责将它写入 HTTP 响应。
type MediaProxyResponse struct {
	Body        []byte
	ContentType string
	ExpiresAt   time.Time
}

type mediaProxyClaims struct {
	URL       string `json:"url"`
	ObjectKey string `json:"object_key,omitempty"`
	ExpiresAt int64  `json:"exp"`
	Scope     string `json:"scope,omitempty"`
	Nonce     string `json:"nonce"`
}

// SetObjectStore attaches the protected object store used by synchronous
// image results. Object tokens are read through this process instead of
// exposing local paths or relying on a short-lived upstream URL.
func (p *MediaProxy) SetObjectStore(objects SyncMediaObjectStore) {
	if p != nil {
		p.objects = objects
	}
}

// NewMediaProxy 创建媒体中转器。secret 应使用节点稳定的高熵密钥（通常复用
// 已校验的 jwt.secret）；空密钥会拒绝启动，避免令牌在重启后可被伪造。
func NewMediaProxy(secret, publicURL string) (*MediaProxy, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, errors.New("media proxy secret is required")
	}
	if err := ValidateMediaProxyPublicURL(publicURL); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(append([]byte("tokenav-media-proxy-v1:"), []byte(secret)...))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return nil, fmt.Errorf("create media proxy cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create media proxy AEAD: %w", err)
	}
	p := &MediaProxy{
		aead:      aead,
		publicURL: strings.TrimRight(strings.TrimSpace(publicURL), "/"),
		ttl:       mediaProxyDefaultTTL,
	}
	p.client = newMediaProxyHTTPClient()
	return p, nil
}

// SetTTL 设置令牌有效期。有效期被限制在 1 分钟至 24 小时，防止误配置成
// 永久开放代理或过短导致正常客户端来不及加载图片。
func (p *MediaProxy) SetTTL(ttl time.Duration) error {
	if p == nil {
		return errors.New("media proxy is nil")
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return errors.New("media proxy ttl must be between 1 minute and 24 hours")
	}
	p.ttl = ttl
	return nil
}

// ValidateMediaProxyPublicURL 与配置加载、绑定登记共用公开地址规则。该地址
// 只用于客户端链接；HTTP 本机/内网节点可用，上游媒体抓取仍独立强制安全 HTTPS。
// 空值返回同源相对地址，绝不从 Host 或转发头推断。
func ValidateMediaProxyPublicURL(raw string) error {
	return config.ValidateGatewayLiteMediaProxyPublicURL(raw)
}

// IssueToken 为一个已经由服务端确认的图片地址签发短期令牌。
func (p *MediaProxy) IssueToken(rawURL, scope string) (string, error) {
	if p == nil || p.aead == nil {
		return "", errors.New("media proxy is unavailable")
	}
	validated, err := validateMediaProxyTarget(rawURL)
	if err != nil {
		return "", err
	}
	if len(validated) > mediaProxyMaxURLSize {
		return "", errors.New("media proxy target URL is too long")
	}
	randomNonce := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, randomNonce); err != nil {
		return "", fmt.Errorf("generate media proxy nonce: %w", err)
	}
	claims := mediaProxyClaims{
		URL:       validated,
		ExpiresAt: time.Now().Add(p.ttl).Unix(),
		Scope:     strings.TrimSpace(scope),
		Nonce:     base64.RawURLEncoding.EncodeToString(randomNonce),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode media proxy claims: %w", err)
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate media proxy encryption nonce: %w", err)
	}
	ciphertext := p.aead.Seal(nil, nonce, payload, []byte(mediaProxyTokenVersion))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, ciphertext...))
	token := mediaProxyTokenVersion + "." + encoded
	if len(token) > mediaProxyMaxTokenSize {
		return "", errors.New("media proxy token is too long")
	}
	return token, nil
}

// URLForToken 返回客户端可访问的地址。未配置 public_url 时返回同源相对路径。
func (p *MediaProxy) URLForToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	base := ""
	if p != nil {
		base = p.publicURL
	}
	return base + mediaProxyPath + url.PathEscape(token)
}

// IssueURL 签发令牌并直接返回客户端地址。
func (p *MediaProxy) IssueURL(rawURL, scope string) (string, error) {
	token, err := p.IssueToken(rawURL, scope)
	if err != nil {
		return "", err
	}
	return p.URLForToken(token), nil
}

// IssueObjectURL signs an object-store key for client access. The key is
// encrypted inside the token and is never accepted directly from a request.
func (p *MediaProxy) IssueObjectURL(key, scope string) (string, error) {
	if p == nil || p.aead == nil || p.objects == nil {
		return "", errors.New("media object proxy is unavailable")
	}
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") || len(key) > mediaProxyMaxURLSize {
		return "", errors.New("media object key is invalid")
	}
	claims := mediaProxyClaims{
		ObjectKey: key,
		ExpiresAt: time.Now().Add(p.ttl).Unix(),
		Scope:     strings.TrimSpace(scope),
	}
	token, err := p.sealClaims(mediaProxyObjectTokenVersion, claims)
	if err != nil {
		return "", err
	}
	return p.URLForToken(token), nil
}

func (p *MediaProxy) sealClaims(version string, claims mediaProxyClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode media proxy claims: %w", err)
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate media proxy encryption nonce: %w", err)
	}
	ciphertext := p.aead.Seal(nil, nonce, payload, []byte(version))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, ciphertext...))
	token := version + "." + encoded
	if len(token) > mediaProxyMaxTokenSize {
		return "", errors.New("media proxy token is too long")
	}
	return token, nil
}

// Claims 校验并解密令牌。expectedScope 非空时会绑定到签发时的 scope，
// 适合把图片地址绑定到某个 API Key 或用户。
func (p *MediaProxy) Claims(token, expectedScope string) (expiresAt time.Time, scope string, err error) {
	claims, _, err := p.decodeClaims(token, expectedScope)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.Unix(claims.ExpiresAt, 0), claims.Scope, nil
}

func (p *MediaProxy) decodeClaims(token, expectedScope string) (mediaProxyClaims, string, error) {
	if p == nil || p.aead == nil || len(token) == 0 || len(token) > mediaProxyMaxTokenSize {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 || (parts[0] != mediaProxyTokenVersion && parts[0] != mediaProxyObjectTokenVersion) {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	encoded, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
	if decodeErr != nil || len(encoded) <= p.aead.NonceSize() {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	nonce, ciphertext := encoded[:p.aead.NonceSize()], encoded[p.aead.NonceSize():]
	payload, openErr := p.aead.Open(nil, nonce, ciphertext, []byte(parts[0]))
	if openErr != nil {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	var claims mediaProxyClaims
	if json.Unmarshal(payload, &claims) != nil || claims.ExpiresAt <= 0 {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	if parts[0] == mediaProxyTokenVersion {
		if claims.URL == "" {
			return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
		}
		if _, validateErr := validateMediaProxyTarget(claims.URL); validateErr != nil {
			return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
		}
	} else if claims.ObjectKey == "" || strings.HasPrefix(claims.ObjectKey, "/") || strings.Contains(claims.ObjectKey, "..") {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	if claims.ExpiresAt <= time.Now().Unix() || (expectedScope != "" && claims.Scope != strings.TrimSpace(expectedScope)) {
		return mediaProxyClaims{}, "", errMediaProxyTokenInvalid
	}
	return claims, parts[0], nil
}

// Fetch 根据签名令牌安全回源并读取图片。请求不会携带上游 Authorization、
// Cookie 或客户端代理头，且每次 DNS 解析都会阻断内网地址。
func (p *MediaProxy) Fetch(ctx context.Context, token, expectedScope string) (MediaProxyResponse, error) {
	if p == nil || p.client == nil {
		return MediaProxyResponse{}, errors.New("media proxy is unavailable")
	}
	claims, version, err := p.decodeClaims(token, expectedScope)
	if err != nil {
		return MediaProxyResponse{}, err
	}
	requestCtx := ctx
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	if version == mediaProxyObjectTokenVersion {
		if p.objects == nil {
			return MediaProxyResponse{}, errors.New("media object proxy is unavailable")
		}
		body, contentType, size, openErr := p.objects.Open(requestCtx, claims.ObjectKey)
		if openErr != nil {
			return MediaProxyResponse{}, fmt.Errorf("open media object: %w", openErr)
		}
		defer body.Close()
		if size > mediaProxyMaxBodySize {
			return MediaProxyResponse{}, errors.New("media object is too large")
		}
		data, readErr := io.ReadAll(io.LimitReader(body, mediaProxyMaxBodySize+1))
		if readErr != nil {
			return MediaProxyResponse{}, fmt.Errorf("read media object: %w", readErr)
		}
		if int64(len(data)) > mediaProxyMaxBodySize {
			return MediaProxyResponse{}, errors.New("media object is too large")
		}
		validatedType, validateErr := validateMediaImageContent(data, contentType)
		if validateErr != nil {
			return MediaProxyResponse{}, validateErr
		}
		return MediaProxyResponse{Body: data, ContentType: validatedType, ExpiresAt: expiresAt}, nil
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, claims.URL, nil)
	if err != nil {
		return MediaProxyResponse{}, errMediaProxyTargetUnsafe
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*;q=0.9")
	resp, err := p.client.Do(req)
	if err != nil {
		return MediaProxyResponse{}, fmt.Errorf("media proxy upstream request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MediaProxyResponse{}, fmt.Errorf("media proxy upstream returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > mediaProxyMaxBodySize {
		return MediaProxyResponse{}, errors.New("media proxy response is too large")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaProxyMaxBodySize+1))
	if err != nil {
		return MediaProxyResponse{}, fmt.Errorf("read media proxy response: %w", err)
	}
	if int64(len(data)) > mediaProxyMaxBodySize {
		return MediaProxyResponse{}, errors.New("media proxy response is too large")
	}
	contentType, err := validateMediaImageContent(data, resp.Header.Get("Content-Type"))
	if err != nil {
		return MediaProxyResponse{}, err
	}
	return MediaProxyResponse{Body: data, ContentType: contentType, ExpiresAt: expiresAt}, nil
}

// FetchURL downloads an already server-selected upstream image URL using the
// same SSRF（服务端请求伪造） and content guards as the bearer-token route.
// Callers must only pass URLs extracted from a validated upstream image
// response; this method is intentionally not exposed as an HTTP endpoint.
func (p *MediaProxy) FetchURL(ctx context.Context, rawURL string) (MediaProxyResponse, error) {
	if p == nil || p.client == nil {
		return MediaProxyResponse{}, errors.New("media proxy is unavailable")
	}
	validated, err := validateMediaProxyTarget(rawURL)
	if err != nil {
		return MediaProxyResponse{}, err
	}
	if len(validated) > mediaProxyMaxURLSize {
		return MediaProxyResponse{}, errors.New("media proxy target URL is too long")
	}
	requestCtx := ctx
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, validated, nil)
	if err != nil {
		return MediaProxyResponse{}, errMediaProxyTargetUnsafe
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*;q=0.9")
	resp, err := p.client.Do(req)
	if err != nil {
		return MediaProxyResponse{}, fmt.Errorf("media proxy upstream request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MediaProxyResponse{}, fmt.Errorf("media proxy upstream returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > mediaProxyMaxBodySize {
		return MediaProxyResponse{}, errors.New("media proxy response is too large")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mediaProxyMaxBodySize+1))
	if err != nil {
		return MediaProxyResponse{}, fmt.Errorf("read media proxy response: %w", err)
	}
	if int64(len(data)) > mediaProxyMaxBodySize {
		return MediaProxyResponse{}, errors.New("media proxy response is too large")
	}
	contentType, err := validateMediaImageContent(data, resp.Header.Get("Content-Type"))
	if err != nil {
		return MediaProxyResponse{}, err
	}
	return MediaProxyResponse{Body: data, ContentType: contentType, ExpiresAt: time.Now().UTC().Add(p.ttl)}, nil
}

// RewriteImageOutputJSON 将生图响应中的输出图片 URL 改成网关地址。
// 非 URL、data: URL、Base64 字段以及相对地址保持不变。issueURL 由调用方
// 提供，通常绑定用户/API Key scope；它不应接受来自客户端的裸 URL。
func RewriteImageOutputJSON(body []byte, issueURL func(string) (string, error)) ([]byte, error) {
	if len(body) == 0 || issueURL == nil {
		return body, nil
	}
	root, err := decodeMediaJSON(body)
	if err != nil {
		return body, nil
	}
	changed, err := rewriteMediaOutputNode(&root, false, issueURL)
	if err != nil {
		return body, err
	}
	if !changed {
		return body, nil
	}
	rewritten, err := json.Marshal(root)
	if err != nil {
		return body, fmt.Errorf("encode rewritten media response: %w", err)
	}
	return rewritten, nil
}

// RewriteImageOutputJSONBestEffort 与严格版本相同，但单个 URL 签发失败时
// 跳过该字段，避免一个非标准附加链接让已生成的图片整体失败。
func RewriteImageOutputJSONBestEffort(body []byte, issueURL func(string) (string, error)) []byte {
	if len(body) == 0 || issueURL == nil {
		return body
	}
	root, err := decodeMediaJSON(body)
	if err != nil {
		return body
	}
	_, _ = rewriteMediaOutputNodeBestEffort(&root, false, issueURL)
	rewritten, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return rewritten
}

// decodeMediaJSON 保留响应中的 JSON 数字原文，避免 created、token 统计等
// 可能超过 float64 精度范围的字段在 URL 改写后发生数值变化。
func decodeMediaJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("media response contains multiple JSON values")
		}
		return nil, err
	}
	return root, nil
}

func rewriteMediaOutputNode(node *any, outputContext bool, issueURL func(string) (string, error)) (bool, error) {
	changed := false
	switch value := (*node).(type) {
	case []any:
		for index := range value {
			childChanged, err := rewriteMediaOutputNode(&value[index], outputContext, issueURL)
			if err != nil {
				return false, err
			}
			changed = changed || childChanged
		}
	case map[string]any:
		for key, child := range value {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if isMediaURLField(lowerKey) {
				if raw, ok := child.(string); ok && shouldRewriteMediaURL(raw, outputContext) {
					rewritten, err := issueURL(strings.TrimSpace(raw))
					if err != nil {
						return false, err
					}
					if strings.TrimSpace(rewritten) != "" && rewritten != raw {
						value[key] = rewritten
						changed = true
					}
				}
				continue
			}
			childOutput := (outputContext || isMediaOutputContainer(lowerKey)) && !isMediaInputContainer(lowerKey)
			childChanged, err := rewriteMediaOutputNode(&child, childOutput, issueURL)
			if err != nil {
				return false, err
			}
			if childChanged {
				value[key] = child
				changed = true
			}
		}
	}
	return changed, nil
}

func rewriteMediaOutputNodeBestEffort(node *any, outputContext bool, issueURL func(string) (string, error)) (bool, error) {
	changed := false
	switch value := (*node).(type) {
	case []any:
		for index := range value {
			childChanged, _ := rewriteMediaOutputNodeBestEffort(&value[index], outputContext, issueURL)
			changed = changed || childChanged
		}
	case map[string]any:
		for key, child := range value {
			lowerKey := strings.ToLower(strings.TrimSpace(key))
			if isMediaURLField(lowerKey) {
				if raw, ok := child.(string); ok && shouldRewriteMediaURL(raw, outputContext) {
					if rewritten, err := issueURL(strings.TrimSpace(raw)); err == nil && strings.TrimSpace(rewritten) != "" && rewritten != raw {
						value[key] = rewritten
						changed = true
					}
				}
				continue
			}
			childChanged, _ := rewriteMediaOutputNodeBestEffort(&child, (outputContext || isMediaOutputContainer(lowerKey)) && !isMediaInputContainer(lowerKey), issueURL)
			if childChanged {
				value[key] = child
				changed = true
			}
		}
	}
	return changed, nil
}

func isMediaURLField(key string) bool {
	switch key {
	case "url", "image_url", "download_url", "content_url", "image_uri", "src":
		return true
	default:
		return false
	}
}

func isMediaOutputContainer(key string) bool {
	switch key {
	case "data", "output", "outputs", "images", "image", "results", "result", "artifacts", "content", "choices":
		return true
	default:
		return false
	}
}

func isMediaInputContainer(key string) bool {
	switch key {
	case "input", "inputs", "prompt", "mask", "mask_image", "mask_image_url", "reference_image", "reference_images", "source_image", "source_images":
		return true
	default:
		return false
	}
}

func shouldRewriteMediaURL(raw string, outputContext bool) bool {
	if !outputContext {
		return false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(strings.ToLower(raw), "data:") || strings.HasPrefix(strings.ToLower(raw), "blob:") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.IsAbs() && (strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "http"))
}

func validateMediaProxyTarget(raw string) (string, error) {
	validated, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{})
	if err != nil {
		return "", fmt.Errorf("%w: %v", errMediaProxyTargetUnsafe, err)
	}
	u, err := url.Parse(validated)
	if err != nil || u.User != nil || u.Host == "" || strings.ContainsAny(validated, "\r\n") {
		return "", errMediaProxyTargetUnsafe
	}
	return validated, nil
}

func normalizeMediaContentType(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, _, err := mime.ParseMediaType(raw)
	if err == nil {
		return strings.ToLower(strings.TrimSpace(parsed))
	}
	return strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
}

func isAllowedImageContentType(contentType string) bool {
	// SVG and other browser-active formats must never be served by this
	// endpoint.  A signed URL can still be embedded cross-origin, so treating
	// every image/* value as safe would turn the proxy into an XSS primitive.
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "image/avif":
		return true
	default:
		return false
	}
}

// detectMediaImageContentType validates the bytes rather than trusting an
// upstream Content-Type header.  Upstream media endpoints occasionally return
// application/octet-stream, while a malicious origin can claim image/png for
// HTML/SVG.  The small signature table covers the formats exposed by the
// image-generation APIs and avoids pulling a full decoder into the gateway.
func detectMediaImageContentType(data []byte) string {
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	// AVIF is an ISO-BMFF file.  The brand may be the major brand or one of
	// the compatible brands, so inspect all complete four-byte brands in ftyp.
	if len(data) >= 16 && string(data[4:8]) == "ftyp" {
		for offset := 8; offset+4 <= len(data) && offset < 512; offset += 4 {
			switch string(data[offset : offset+4]) {
			case "avif", "avis":
				return "image/avif"
			}
		}
	}
	return ""
}

func validateMediaImageContent(data []byte, declaredType string) (string, error) {
	detectedType := detectMediaImageContentType(data)
	if detectedType == "" {
		return "", errors.New("media proxy response is not a supported raster image")
	}
	declaredType = normalizeMediaContentType(declaredType)
	// A few image origins intentionally use octet-stream. The bytes are still
	// checked against the supported raster signatures below, so this fallback
	// does not broaden the accepted content beyond actual image data.
	if declaredType == "application/octet-stream" || declaredType == "binary/octet-stream" {
		declaredType = ""
	}
	if declaredType != "" && !isAllowedImageContentType(declaredType) {
		return "", errors.New("media proxy response has an unsafe content type")
	}
	if declaredType != "" {
		// image/jpg is an alias accepted from a few upstreams.
		if declaredType == "image/jpg" {
			declaredType = "image/jpeg"
		}
		if declaredType != detectedType {
			return "", errors.New("media proxy response content type does not match its bytes")
		}
	}
	return detectedType, nil
}

func newMediaProxyHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		MaxConnsPerHost:       16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DisableCompression:    false,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("resolve media host: %w", err)
		}
		for _, resolved := range ips {
			if mediaProxyIPBlocked(resolved.IP) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
		}
		return nil, errors.New("media host resolved only to blocked addresses")
	}
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= mediaProxyMaxRedirects || req.URL == nil {
			return errors.New("too many media proxy redirects")
		}
		if _, err := validateMediaProxyTarget(req.URL.String()); err != nil {
			return err
		}
		return nil
	}
	return client
}

func mediaProxyIPBlocked(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// RFC 6598 CGNAT and the 0/8 special-use range are not public origins.
		return v4[0] == 0 || (v4[0] == 100 && v4[1]&0xc0 == 0x40)
	}
	return false
}
