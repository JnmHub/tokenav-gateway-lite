package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/net/http2"
	"golang.org/x/sync/singleflight"
)

// chatgptCodexModelsURL is a variable so tests can replace the upstream endpoint.
var chatgptCodexModelsURL = "https://chatgpt.com/backend-api/codex/models"

const (
	// 兼容旧测试的默认常量；生产读取上限由 gateway.models_list_read_max_bytes 控制。
	codexModelsManifestBodyLimit       int64 = 8 << 20
	codexModelsManifestCacheBodyLimit        = 1 << 20
	codexModelsManifestCacheMaxEntries       = 64
	codexModelsManifestCacheTTL              = 30 * time.Second
	codexModelsManifestCacheStaleTTL         = 5 * time.Minute
	codexModelsManifestRequestTimeout        = 15 * time.Second
)

type CodexModelsManifestCacheState string

const (
	CodexModelsManifestCacheStateUpstream CodexModelsManifestCacheState = "upstream"
	CodexModelsManifestCacheStateFresh    CodexModelsManifestCacheState = "fresh-cache"
	CodexModelsManifestCacheStateStale    CodexModelsManifestCacheState = "last-known-good"
)

// CodexModelsManifest contains the response body and cache metadata returned to Codex.
type CodexModelsManifest struct {
	Body         []byte
	ETag         string
	upstreamETag string
	NotModified  bool
	CacheState   CodexModelsManifestCacheState
}

type codexModelsManifestUpstreamError struct {
	err        error
	retryable  bool
	statusCode int
	headers    http.Header
	body       []byte
}

func (e *codexModelsManifestUpstreamError) Error() string { return e.err.Error() }

func (e *codexModelsManifestUpstreamError) Unwrap() error { return e.err }

// IsRetryableCodexModelsManifestError reports whether the handler may safely
// retry the same manifest request with another account in the fixed group.
// Configuration errors and ordinary 4xx responses are intentionally excluded.
// ChatGPT Codex manifest 的 401 来自已选 OAuth 账号，而不是已经在本地
// 验证过的客户 Key，因此允许换号；自定义 API Key 上游仍保持不换号。
func IsRetryableCodexModelsManifestError(err error) bool {
	var upstreamErr *codexModelsManifestUpstreamError
	return errors.As(err, &upstreamErr) && upstreamErr.retryable
}

func isRetryableCodexModelsManifestTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var goAwayErr http2.GoAwayError
	if errors.As(err, &goAwayErr) {
		return true
	}
	var streamErr http2.StreamError
	if errors.As(err, &streamErr) {
		return true
	}
	var connectionErr http2.ConnectionError
	if errors.As(err, &connectionErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// net/http uses unexported HTTP/2 error types in several paths, so retain
	// the official string fallbacks for GOAWAY and refused-stream failures.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "http2:") &&
		(strings.Contains(message, "goaway") ||
			strings.Contains(message, "refused_stream") ||
			strings.Contains(message, "frame too large")) {
		return true
	}
	if strings.Contains(message, "stream error: stream id ") {
		return true
	}
	for _, code := range []http2.ErrCode{
		http2.ErrCodeNo,
		http2.ErrCodeProtocol,
		http2.ErrCodeInternal,
		http2.ErrCodeFlowControl,
		http2.ErrCodeSettingsTimeout,
		http2.ErrCodeStreamClosed,
		http2.ErrCodeFrameSize,
		http2.ErrCodeRefusedStream,
		http2.ErrCodeCancel,
		http2.ErrCodeCompression,
		http2.ErrCodeConnect,
		http2.ErrCodeEnhanceYourCalm,
		http2.ErrCodeInadequateSecurity,
		http2.ErrCodeHTTP11Required,
	} {
		if strings.Contains(message, "connection error: "+strings.ToLower(code.String())) {
			return true
		}
	}
	return false
}

type codexModelsManifestRequest struct {
	url                string
	headers            http.Header
	proxyURL           string
	accountID          int64
	credentialAccount  *Account
	accountConcurrency int
	useAPIKeyUpstream  bool
}

type codexModelsManifestCacheEntry struct {
	manifest   *CodexModelsManifest
	order      uint64
	expiresAt  time.Time
	staleUntil time.Time
}

type codexModelsManifestCacheState uint8

const (
	codexModelsManifestCacheMiss codexModelsManifestCacheState = iota
	codexModelsManifestCacheFresh
	codexModelsManifestCacheStale
)

type codexModelsManifestCache struct {
	mu        sync.Mutex
	entries   map[string]codexModelsManifestCacheEntry
	nextOrder uint64
	refresh   singleflight.Group
}

func (c *codexModelsManifestCache) get(key string, now time.Time) (*CodexModelsManifest, codexModelsManifestCacheState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, codexModelsManifestCacheMiss
	}
	if !now.Before(entry.staleUntil) {
		delete(c.entries, key)
		return nil, codexModelsManifestCacheMiss
	}
	if now.Before(entry.expiresAt) {
		return entry.manifest, codexModelsManifestCacheFresh
	}
	return entry.manifest, codexModelsManifestCacheStale
}

func (c *codexModelsManifestCache) set(key string, manifest *CodexModelsManifest, now time.Time) {
	if manifest == nil || len(manifest.Body) > codexModelsManifestCacheBodyLimit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]codexModelsManifestCacheEntry)
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= codexModelsManifestCacheMaxEntries {
		oldestKey := ""
		var oldestOrder uint64
		for candidateKey, entry := range c.entries {
			if !now.Before(entry.staleUntil) {
				delete(c.entries, candidateKey)
				continue
			}
			if oldestKey == "" || entry.order < oldestOrder {
				oldestKey = candidateKey
				oldestOrder = entry.order
			}
		}
		if len(c.entries) >= codexModelsManifestCacheMaxEntries && oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.nextOrder++
	c.entries[key] = codexModelsManifestCacheEntry{
		// CacheState 描述的是“本次如何取得结果”，不能固化进缓存对象；
		// 否则一次 stale 读取会污染后续 fresh 命中的运维来源标记。
		manifest:   cloneCodexModelsManifest(manifest, ""),
		order:      c.nextOrder,
		expiresAt:  now.Add(codexModelsManifestCacheTTL),
		staleUntil: now.Add(codexModelsManifestCacheStaleTTL),
	}
}

type codexManifestModelPolicy struct {
	allowOriginalModels bool
	aliasesByUpstream   map[string][]string
	customModels        map[string]struct{}
	customModelsEnabled bool
	allowedPatterns     []string
	allowImages         bool
	scoped              bool
}

// FetchCodexModelsManifest fetches the live ChatGPT Codex manifest and scopes it to the
// current API Key. The upstream schema is preserved, but model entries are filtered or
// cloned for local aliases so the picker matches the models that routing can really serve.
func (s *OpenAIGatewayService) FetchCodexModelsManifest(
	ctx context.Context,
	account *Account,
	routingGroupID *int64,
	group *Group,
	allowedModels []string,
	clientVersion string,
	ifNoneMatch string,
) (*CodexModelsManifest, error) {
	if account == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_ACCOUNT_REQUIRED", "account is required")
	}

	policy, err := s.buildCodexManifestModelPolicy(ctx, routingGroupID, group, allowedModels)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_POLICY_FAILED", "build codex models policy: %v", err)
	}

	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		clientVersion = codexCanonicalClientVersion()
	}
	request, err := s.buildCodexModelsManifestRequest(ctx, account, clientVersion)
	if err != nil {
		return nil, err
	}

	upstreamIfNoneMatch := ""
	if !policy.scoped {
		upstreamIfNoneMatch = ifNoneMatch
	}
	var manifest *CodexModelsManifest
	if request.useAPIKeyUpstream {
		manifest, err = s.fetchCachedAPIKeyCodexModelsManifest(ctx, request, upstreamIfNoneMatch)
	} else {
		manifest, err = s.fetchCodexModelsManifestUpstream(ctx, request, upstreamIfNoneMatch)
	}
	if err != nil {
		if account.IsOpenAIAgentIdentity() && isAgentIdentityTaskInvalidCodexModelsError(err) {
			expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
			if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
				return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_AUTH_FAILED", "Agent Identity task recovery failed: %v", recoverErr)
			}
			request, err = s.buildCodexModelsManifestRequest(ctx, account, clientVersion)
			if err == nil {
				manifest, err = s.fetchCodexModelsManifestUpstream(ctx, request, upstreamIfNoneMatch)
			}
			if err == nil {
				goto manifestReady
			}
		}
		s.handleCodexModelsManifestAccountAuthError(ctx, account, err)
		return nil, err
	}

manifestReady:
	if manifest == nil {
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest returned no response")
	}
	if manifest.NotModified {
		if !policy.scoped {
			return manifest, nil
		}
		return nil, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest returned 304 without a filterable body")
	}

	body := manifest.Body

	if policy.scoped {
		body, err = filterCodexModelsManifest(body, policy)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_FILTER_FAILED", "filter Codex models manifest: %v", err)
		}
		etag := codexModelsManifestETag(body, routingGroupID)
		if codexETagMatches(ifNoneMatch, etag) {
			return &CodexModelsManifest{ETag: etag, NotModified: true, CacheState: manifest.CacheState}, nil
		}
		return &CodexModelsManifest{Body: body, ETag: etag, CacheState: manifest.CacheState}, nil
	}

	return &CodexModelsManifest{Body: body, ETag: manifest.ETag, CacheState: manifest.CacheState}, nil
}

// handleCodexModelsManifestAccountAuthError 把 ChatGPT Codex manifest 的
// OAuth 401 纳入既有账号状态机。API Key 上游的 /models 鉴权语义可能与
// 请求端点不同，不能据此停用账号；本轻量网关不接收 Agent Identity
// 凭据，因此这里只处理普通 OpenAI OAuth 账号。
func (s *OpenAIGatewayService) handleCodexModelsManifestAccountAuthError(ctx context.Context, account *Account, err error) {
	if s == nil || account == nil || !account.IsOpenAIOAuth() || account.IsOpenAIAgentIdentity() || err == nil {
		return
	}
	var upstreamErr *codexModelsManifestUpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.statusCode != http.StatusUnauthorized {
		return
	}
	headers := upstreamErr.headers
	if headers == nil {
		headers = http.Header{}
	}
	s.handleOpenAIAccountUpstreamError(ctx, account, upstreamErr.statusCode, headers, upstreamErr.body)
}

func isAgentIdentityTaskInvalidCodexModelsError(err error) bool {
	var upstreamErr *codexModelsManifestUpstreamError
	return errors.As(err, &upstreamErr) && upstreamErr != nil &&
		isAgentIdentityTaskInvalidHTTPResponse(upstreamErr.statusCode, upstreamErr.body)
}

func (s *OpenAIGatewayService) buildCodexModelsManifestRequest(ctx context.Context, account *Account, clientVersion string) (codexModelsManifestRequest, error) {
	requestEndpoint := chatgptCodexModelsURL
	authToken := ""
	useAPIKeyUpstream := false
	appendModelsPath := false

	switch {
	case account.IsOpenAIOAuth():
		if !account.IsOpenAIAgentIdentity() {
			accessToken, _, err := s.GetAccessToken(ctx, account)
			if err != nil {
				return codexModelsManifestRequest{}, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_FAILED", "get Codex access token: %v", err)
			}
			authToken = strings.TrimSpace(accessToken)
			if authToken == "" {
				return codexModelsManifestRequest{}, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_TOKEN_MISSING", "account has no Codex backend access token")
			}
		}
	case account.IsOpenAIApiKey():
		baseURL := strings.TrimSpace(account.GetCredential("base_url"))
		if baseURL == "" || isOfficialOpenAIModelsBaseURL(baseURL) {
			return codexModelsManifestRequest{}, infraerrors.New(
				http.StatusBadGateway,
				"OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_UNSUPPORTED",
				"Codex models manifest requires a custom API key upstream base URL",
			)
		}
		authToken = strings.TrimSpace(account.GetOpenAIApiKey())
		if authToken == "" {
			return codexModelsManifestRequest{}, infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_MISSING", "account has no API key for the Codex models upstream")
		}
		normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return codexModelsManifestRequest{}, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID", "invalid Codex models upstream base URL: %v", err)
		}
		requestEndpoint = normalizedBaseURL
		useAPIKeyUpstream = true
		appendModelsPath = true
	default:
		return codexModelsManifestRequest{}, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_ACCOUNT_TYPE_UNSUPPORTED", "account type %q cannot fetch the Codex models manifest", account.Type)
	}

	requestURL, err := buildCodexModelsManifestURL(requestEndpoint, appendModelsPath, clientVersion)
	if err != nil {
		if useAPIKeyUpstream {
			return codexModelsManifestRequest{}, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_API_KEY_UPSTREAM_INVALID", "invalid Codex models upstream base URL: %v", err)
		}
		return codexModelsManifestRequest{}, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "parse Codex models request URL: %v", err)
	}

	headers := make(http.Header)
	if useAPIKeyUpstream {
		headers.Set("Authorization", "Bearer "+authToken)
	} else {
		authHeaders, authErr := s.buildOpenAIAuthenticationHeaders(ctx, account, authToken)
		if authErr != nil {
			return codexModelsManifestRequest{}, infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_AUTH_FAILED", "build Codex models authentication: %v", authErr)
		}
		for key, values := range authHeaders {
			for _, value := range values {
				headers.Add(key, value)
			}
		}
	}
	headers.Set("Accept", "application/json")
	headers.Set("Originator", "codex_cli_rs")
	headers.Set("Version", codexCanonicalClientVersion())
	if useAPIKeyUpstream {
		headers.Set("User-Agent", codexCLIUserAgent)
		account.ApplyHeaderOverrides(headers)
	} else {
		if accountID := strings.TrimSpace(account.GetChatGPTAccountID()); accountID != "" {
			headers.Set("chatgpt-account-id", accountID)
		}
		if userAgent := strings.TrimSpace(account.GetOpenAIUserAgent()); userAgent != "" {
			headers.Set("User-Agent", userAgent)
		} else {
			headers.Set("User-Agent", codexCLIUserAgent)
		}
	}
	enforceCodexIdentityHeadersWithUA(headers, account.GetOpenAIUserAgent())

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	return codexModelsManifestRequest{
		url:                requestURL.String(),
		headers:            headers,
		proxyURL:           proxyURL,
		accountID:          account.ID,
		credentialAccount:  account,
		accountConcurrency: account.Concurrency,
		useAPIKeyUpstream:  useAPIKeyUpstream,
	}, nil
}

func (s *OpenAIGatewayService) fetchCachedAPIKeyCodexModelsManifest(ctx context.Context, request codexModelsManifestRequest, ifNoneMatch string) (*CodexModelsManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cacheKey := buildCodexModelsManifestCacheKey(request)
	manifest, state := s.codexModelsManifestCache.get(cacheKey, time.Now())
	if state == codexModelsManifestCacheFresh {
		return codexModelsManifestForClient(manifest, ifNoneMatch, CodexModelsManifestCacheStateFresh), nil
	}
	resultCh := s.refreshCachedAPIKeyCodexModelsManifest(cacheKey, request)
	if state == codexModelsManifestCacheStale {
		return codexModelsManifestForClient(manifest, ifNoneMatch, CodexModelsManifestCacheStateStale), nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		manifest, ok := result.Val.(*CodexModelsManifest)
		if !ok || manifest == nil {
			return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "invalid shared Codex models manifest result")
		}
		return codexModelsManifestForClient(manifest, ifNoneMatch, CodexModelsManifestCacheStateUpstream), nil
	}
}

func (s *OpenAIGatewayService) refreshCachedAPIKeyCodexModelsManifest(cacheKey string, request codexModelsManifestRequest) <-chan singleflight.Result {
	return s.codexModelsManifestCache.refresh.DoChan(cacheKey, func() (any, error) {
		cached, _ := s.codexModelsManifestCache.get(cacheKey, time.Now())
		ifNoneMatch := ""
		if cached != nil {
			ifNoneMatch = cached.upstreamETag
		}
		manifest, err := s.fetchCodexModelsManifestUpstream(context.Background(), request, ifNoneMatch)
		if err != nil {
			return nil, err
		}
		if manifest.NotModified && cached != nil {
			s.codexModelsManifestCache.set(cacheKey, cached, time.Now())
			return cached, nil
		}
		if !manifest.NotModified {
			s.codexModelsManifestCache.set(cacheKey, manifest, time.Now())
		}
		return manifest, nil
	})
}

func (s *OpenAIGatewayService) fetchCodexModelsManifestUpstream(ctx context.Context, request codexModelsManifestRequest, ifNoneMatch string) (*CodexModelsManifest, error) {
	reqCtx, cancel := context.WithTimeout(ctx, codexModelsManifestRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, request.url, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_REQUEST_FAILED", "create Codex models request: %v", err)
	}
	req.Header = request.headers.Clone()
	if ifNoneMatch = strings.TrimSpace(ifNoneMatch); ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	if !request.useAPIKeyUpstream {
		req.Host = "chatgpt.com"
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	var resp *http.Response
	if s.httpUpstream != nil {
		resp, err = s.httpUpstream.Do(req, request.proxyURL, request.accountID, request.accountConcurrency)
	} else if request.useAPIKeyUpstream {
		return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_CODEX_MODELS_UPSTREAM_NOT_CONFIGURED", "Codex models upstream HTTP client is not configured")
	} else {
		resp, err = http.DefaultClient.Do(req)
	}
	if err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest request failed: %v", err),
			retryable: isRetryableCodexModelsManifestTransportError(err),
		}
	}
	if resp == nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.New(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest returned no response"),
			retryable: true,
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return &CodexModelsManifest{ETag: resp.Header.Get("ETag"), NotModified: true, CacheState: CodexModelsManifestCacheStateUpstream}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		body = s.redactAgentIdentitySensitiveBody(reqCtx, request.credentialAccount, body)
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, &codexModelsManifestUpstreamError{
			err:        infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "Codex models manifest upstream error %d: %s", resp.StatusCode, message),
			statusCode: resp.StatusCode,
			headers:    resp.Header.Clone(),
			body:       body,
			retryable: (resp.StatusCode == http.StatusUnauthorized && !request.useAPIKeyUpstream) ||
				resp.StatusCode == http.StatusTooManyRequests ||
				(resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode < 600),
		}
	}

	bodyLimit := resolveModelsListReadLimit(s.cfg)
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_UPSTREAM_FAILED", "read Codex models manifest response: %v", err),
			retryable: isRetryableCodexModelsManifestTransportError(err),
		}
	}
	if int64(len(body)) > bodyLimit {
		return nil, &codexModelsManifestUpstreamError{
			err:       infraerrors.Newf(http.StatusBadGateway, "OPENAI_CODEX_MODELS_BODY_TOO_LARGE", "Codex models manifest exceeds the %d byte limit", bodyLimit),
			retryable: true,
		}
	}
	upstreamBody := body
	if request.useAPIKeyUpstream {
		body = convertOpenAIModelListToCodexManifest(body)
	}
	if err := validateCodexModelsManifestEnvelope(body); err != nil {
		// A 2xx response is not sufficient evidence that this endpoint returned a
		// Codex manifest. Treat malformed or ordinary OpenAI /v1/models payloads as
		// retryable so the fixed group can select another account, but never cache
		// the bad body and poison capability discovery for later clients.
		return nil, &codexModelsManifestUpstreamError{
			err: infraerrors.Newf(
				http.StatusBadGateway,
				"OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST",
				"Codex models manifest upstream returned an invalid envelope: %v",
				err,
			),
			retryable: true,
		}
	}
	if request.useAPIKeyUpstream {
		body, err = adjustAPIKeyCodexModelsManifest(body)
		if err != nil {
			return nil, &codexModelsManifestUpstreamError{
				err: infraerrors.Newf(
					http.StatusBadGateway,
					"OPENAI_CODEX_MODELS_UPSTREAM_INVALID_MANIFEST",
					"codex models manifest upstream could not be adjusted: %v",
					err,
				),
				retryable: true,
			}
		}
	}
	etag := resp.Header.Get("ETag")
	manifest := &CodexModelsManifest{Body: body, ETag: etag, CacheState: CodexModelsManifestCacheStateUpstream}
	if request.useAPIKeyUpstream {
		manifest.upstreamETag = etag
		if !bytes.Equal(body, upstreamBody) {
			manifest.ETag = codexModelsManifestBodyETag(body)
		}
	}
	return manifest, nil
}

func codexModelsManifestBodyETag(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf(`"%x"`, sum)
}

var apiKeyCodexModelsWithoutResponsesLite = map[string]struct{}{
	"gpt-5.6-sol":   {},
	"gpt-5.6-terra": {},
	"gpt-5.6-luna":  {},
}

// adjustAPIKeyCodexModelsManifest 禁止自定义 API Key 上游为 GPT-5.6
// 宣告 Responses Lite。该模式不会安装 web.run，继续宣告会让 Codex
// 选择一条缺少网页搜索工具的错误请求路径。
func adjustAPIKeyCodexModelsManifest(body []byte) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode JSON object: %w", err)
	}
	var models []json.RawMessage
	if err := json.Unmarshal(envelope["models"], &models); err != nil {
		return nil, fmt.Errorf("decode top-level models array: %w", err)
	}

	changed := false
	for index, rawModel := range models {
		var model map[string]json.RawMessage
		if err := json.Unmarshal(rawModel, &model); err != nil || model == nil {
			continue
		}
		var slug string
		if err := json.Unmarshal(model["slug"], &slug); err != nil {
			continue
		}
		if _, targeted := apiKeyCodexModelsWithoutResponsesLite[slug]; !targeted {
			continue
		}
		var useResponsesLite bool
		if err := json.Unmarshal(model["use_responses_lite"], &useResponsesLite); err != nil || !useResponsesLite {
			continue
		}
		model["use_responses_lite"] = json.RawMessage("false")
		adjusted, err := json.Marshal(model)
		if err != nil {
			return nil, fmt.Errorf("encode model %q: %w", slug, err)
		}
		models[index] = adjusted
		changed = true
	}
	if !changed {
		return body, nil
	}

	adjustedModels, err := json.Marshal(models)
	if err != nil {
		return nil, fmt.Errorf("encode top-level models array: %w", err)
	}
	envelope["models"] = adjustedModels
	adjusted, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode JSON object: %w", err)
	}
	return adjusted, nil
}

// convertOpenAIModelListToCodexManifest 只转换标准 OpenAI
// {"object":"list","data":[{"id":"..."}]} 模型列表。已有 manifest、
// 空列表、畸形结构和无有效 ID 的正文原样返回，继续交给严格 envelope
// 校验，避免凭空伪造上游能力。
func convertOpenAIModelListToCodexManifest(body []byte) []byte {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil || envelope == nil {
		return body
	}
	if _, ok := envelope["models"]; ok {
		return body
	}
	data, ok := envelope["data"]
	if !ok {
		return body
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return body
	}
	type codexModelEntry struct {
		Slug string `json:"slug"`
	}
	models := make([]codexModelEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, codexModelEntry{Slug: id})
	}
	if len(models) == 0 {
		return body
	}
	converted, err := json.Marshal(map[string][]codexModelEntry{"models": models})
	if err != nil {
		return body
	}
	return converted
}

func validateCodexModelsManifestEnvelope(body []byte) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode JSON object: %w", err)
	}
	if document == nil {
		return errors.New("expected a JSON object")
	}

	rawModels, ok := document["models"]
	if !ok {
		return errors.New("missing top-level models array")
	}
	rawModels = bytes.TrimSpace(rawModels)
	if len(rawModels) == 0 || rawModels[0] != '[' {
		return errors.New("top-level models field is not an array")
	}

	var models []json.RawMessage
	if err := json.Unmarshal(rawModels, &models); err != nil {
		return fmt.Errorf("decode top-level models array: %w", err)
	}
	seen := make(map[string]struct{}, len(models))
	for index, rawModel := range models {
		modelID, err := codexManifestModelID(rawModel)
		if err != nil {
			return fmt.Errorf("models[%d]: %w", index, err)
		}
		normalized := normalizeCodexModelID(modelID)
		if _, duplicate := seen[normalized]; duplicate {
			return fmt.Errorf("models[%d]: duplicate model id %q", index, modelID)
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func buildCodexModelsManifestCacheKey(request codexModelsManifestRequest) string {
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%d\n%s\n%s\n", request.accountID, request.proxyURL, request.url)
	headerNames := make([]string, 0, len(request.headers))
	for name := range request.headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		_, _ = fmt.Fprintf(hasher, "%s\n", strings.ToLower(name))
		for _, value := range request.headers[name] {
			_, _ = fmt.Fprintf(hasher, "%s\n", value)
		}
	}
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func codexModelsManifestForClient(manifest *CodexModelsManifest, ifNoneMatch string, cacheState CodexModelsManifestCacheState) *CodexModelsManifest {
	if manifest == nil {
		return nil
	}
	if codexETagMatches(ifNoneMatch, manifest.ETag) {
		return &CodexModelsManifest{ETag: manifest.ETag, NotModified: true, CacheState: cacheState}
	}
	return cloneCodexModelsManifest(manifest, cacheState)
}

func cloneCodexModelsManifest(manifest *CodexModelsManifest, cacheState CodexModelsManifestCacheState) *CodexModelsManifest {
	if manifest == nil {
		return nil
	}
	return &CodexModelsManifest{
		Body:         append([]byte(nil), manifest.Body...),
		ETag:         manifest.ETag,
		upstreamETag: manifest.upstreamETag,
		NotModified:  manifest.NotModified,
		CacheState:   cacheState,
	}
}

func isOfficialOpenAIModelsBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	hostname := strings.TrimSuffix(parsed.Hostname(), ".")
	return strings.EqualFold(hostname, "api.openai.com")
}

func buildCodexModelsManifestURL(endpoint string, appendModelsPath bool, clientVersion string) (*url.URL, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if requestURL.Fragment != "" {
		return nil, fmt.Errorf("URL fragments are not supported")
	}

	query := requestURL.Query()
	requestURL.RawQuery = ""
	requestURL.ForceQuery = false
	if appendModelsPath {
		requestURL, err = url.Parse(buildOpenAIModelsURL(requestURL.String()))
		if err != nil {
			return nil, err
		}
	}
	query.Set("client_version", clientVersion)
	requestURL.RawQuery = query.Encode()
	return requestURL, nil
}

func (s *OpenAIGatewayService) buildCodexManifestModelPolicy(ctx context.Context, groupID *int64, group *Group, allowedModels []string) (codexManifestModelPolicy, error) {
	policy := codexManifestModelPolicy{
		aliasesByUpstream: make(map[string][]string),
		customModels:      make(map[string]struct{}),
		allowedPatterns:   normalizeCodexModelPatterns(allowedModels),
		allowImages:       GroupAllowsImageGeneration(group),
		scoped:            groupID != nil || len(normalizeCodexModelPatterns(allowedModels)) > 0 || !GroupAllowsImageGeneration(group),
	}
	if group != nil && group.CustomModelsListEnabled() {
		policy.customModelsEnabled = true
		for _, model := range group.ModelsListConfig.Models {
			if normalized := normalizeCodexModelID(model); normalized != "" {
				policy.customModels[normalized] = struct{}{}
			}
		}
		policy.scoped = true
	}

	accounts, err := s.listSchedulableAccounts(ctx, groupID)
	if err != nil {
		return policy, err
	}
	for i := range accounts {
		account := &accounts[i]
		if !account.IsOpenAI() || !account.IsSchedulable() {
			continue
		}
		mapping := account.GetModelMapping()
		if len(mapping) == 0 {
			policy.allowOriginalModels = true
			continue
		}
		for requestedModel, upstreamModel := range mapping {
			requested := strings.TrimSpace(requestedModel)
			upstream := normalizeCodexModelID(upstreamModel)
			if requested == "" || upstream == "" {
				continue
			}
			policy.aliasesByUpstream[upstream] = append(policy.aliasesByUpstream[upstream], requested)
		}
	}
	for upstream, aliases := range policy.aliasesByUpstream {
		sort.Slice(aliases, func(i, j int) bool {
			return strings.ToLower(aliases[i]) < strings.ToLower(aliases[j])
		})
		policy.aliasesByUpstream[upstream] = dedupeCodexModelIDs(aliases)
	}
	return policy, nil
}

func filterCodexModelsManifest(body []byte, policy codexManifestModelPolicy) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	var models []json.RawMessage
	if rawModels, ok := document["models"]; !ok {
		return nil, fmt.Errorf("manifest does not contain models")
	} else if err := json.Unmarshal(rawModels, &models); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	filtered := make([]json.RawMessage, 0, len(models))
	seen := make(map[string]struct{})
	for _, rawModel := range models {
		upstreamID, err := codexManifestModelID(rawModel)
		if err != nil {
			return nil, err
		}
		candidates := make([]string, 0, 1+len(policy.aliasesByUpstream[normalizeCodexModelID(upstreamID)]))
		if policy.allowOriginalModels {
			candidates = append(candidates, upstreamID)
		}
		candidates = append(candidates, policy.aliasesByUpstream[normalizeCodexModelID(upstreamID)]...)
		for _, candidate := range candidates {
			candidate = strings.TrimSpace(candidate)
			normalized := normalizeCodexModelID(candidate)
			if normalized == "" || !codexManifestModelAllowed(normalized, upstreamID, policy) {
				continue
			}
			if _, exists := seen[normalized]; exists {
				continue
			}
			seen[normalized] = struct{}{}
			if strings.EqualFold(candidate, upstreamID) {
				filtered = append(filtered, rawModel)
				continue
			}
			aliased, err := cloneCodexManifestModel(rawModel, candidate)
			if err != nil {
				return nil, err
			}
			filtered = append(filtered, aliased)
		}
	}

	encodedModels, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	document["models"] = encodedModels
	return json.Marshal(document)
}

func codexManifestModelID(raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", fmt.Errorf("decode model entry: %w", err)
	}
	for _, field := range []string{"slug", "id"} {
		var value string
		if rawValue, ok := fields[field]; ok && json.Unmarshal(rawValue, &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("manifest model entry has no slug or id")
}

func cloneCodexManifestModel(raw json.RawMessage, modelID string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	encodedID, _ := json.Marshal(modelID)
	if _, ok := fields["slug"]; ok {
		fields["slug"] = encodedID
	} else {
		fields["id"] = encodedID
	}
	if _, ok := fields["display_name"]; ok {
		fields["display_name"] = encodedID
	}
	return json.Marshal(fields)
}

func codexManifestModelAllowed(candidate, upstream string, policy codexManifestModelPolicy) bool {
	if policy.customModelsEnabled {
		if _, ok := policy.customModels[candidate]; !ok {
			return false
		}
	}
	if len(policy.allowedPatterns) > 0 && !codexModelMatchesAnyPattern(candidate, policy.allowedPatterns) {
		return false
	}
	if !policy.allowImages && (isOpenAIImageGenerationModel(candidate) || isOpenAIImageGenerationModel(upstream)) {
		return false
	}
	return true
}

func normalizeCodexModelPatterns(patterns []string) []string {
	result := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern = strings.ToLower(strings.TrimSpace(pattern)); pattern != "" && pattern != "*" {
			result = append(result, pattern)
		}
	}
	return dedupeCodexModelIDs(result)
}

func codexModelMatchesAnyPattern(model string, patterns []string) bool {
	for _, pattern := range patterns {
		if codexModelPatternMatch(model, pattern) {
			return true
		}
	}
	return false
}

func codexModelPatternMatch(model, pattern string) bool {
	model = normalizeCodexModelID(model)
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return false
	}
	if pattern == "*" || model == pattern {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	cursor := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		pos := strings.Index(model[cursor:], part)
		if pos < 0 || (i == 0 && !strings.HasPrefix(pattern, "*") && pos != 0) {
			return false
		}
		cursor += pos + len(part)
	}
	if !strings.HasSuffix(pattern, "*") {
		last := parts[len(parts)-1]
		return last == "" || strings.HasSuffix(model, last)
	}
	return true
}

func normalizeCodexModelID(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func dedupeCodexModelIDs(models []string) []string {
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		normalized := normalizeCodexModelID(model)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, strings.TrimSpace(model))
	}
	return result
}

func codexModelsManifestETag(body []byte, groupID *int64) string {
	hash := sha256.New()
	_, _ = hash.Write(body)
	_, _ = fmt.Fprintf(hash, "|group=%d", derefGroupID(groupID))
	return `W/"codex-` + hex.EncodeToString(hash.Sum(nil)) + `"`
}

func codexETagMatches(ifNoneMatch, etag string) bool {
	etag = strings.TrimSpace(etag)
	if etag == "" {
		return false
	}
	normalize := func(value string) string {
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.EqualFold(value[:2], "W/") {
			value = strings.TrimSpace(value[2:])
		}
		return value
	}
	want := normalize(etag)
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || normalize(candidate) == want {
			return true
		}
	}
	return false
}
