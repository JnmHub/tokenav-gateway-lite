package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// syncMediaE2EObjectStore is deliberately small: the test exercises the same
// SyncMediaObjectStore contract used by local/S3 storage, while keeping the
// HTTP path independent from a filesystem or external object service.
type syncMediaE2EObjectStore struct {
	mu      sync.Mutex
	objects map[string]syncMediaE2EObject
}

type syncMediaE2EObject struct {
	body        []byte
	contentType string
}

func newSyncMediaE2EObjectStore() *syncMediaE2EObjectStore {
	return &syncMediaE2EObjectStore{objects: make(map[string]syncMediaE2EObject)}
}

func (s *syncMediaE2EObjectStore) Put(_ context.Context, key string, body io.Reader, contentType string, maxBytes int64) (int64, string, error) {
	if body == nil || maxBytes <= 0 {
		return 0, "", errors.New("invalid object input")
	}
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if int64(len(data)) > maxBytes {
		return 0, "", errors.New("object too large")
	}
	s.mu.Lock()
	s.objects[key] = syncMediaE2EObject{body: append([]byte(nil), data...), contentType: contentType}
	s.mu.Unlock()
	return int64(len(data)), "e2e-digest", nil
}

func (s *syncMediaE2EObjectStore) Open(_ context.Context, key string) (io.ReadCloser, string, int64, error) {
	s.mu.Lock()
	object, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, "", 0, errors.New("object not found")
	}
	return io.NopCloser(bytes.NewReader(object.body)), object.contentType, int64(len(object.body)), nil
}

func (s *syncMediaE2EObjectStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}

func (s *syncMediaE2EObjectStore) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "/unused/" + key, nil
}

func (s *syncMediaE2EObjectStore) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}
	return keys
}

func expectedSyncMediaE2EObjectKey(requestID string, index int) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(requestID)))
	return "image-sync/" + hex.EncodeToString(digest[:16]) + "/" + formatSyncMediaE2EIndex(index)
}

func formatSyncMediaE2EIndex(index int) string {
	return fmt.Sprintf("%04d", index)
}

type syncMediaE2EUpstream struct {
	mu          sync.Mutex
	status      int
	contentType string
	body        string
	requestIDs  []string
}

func (u *syncMediaE2EUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	if req != nil {
		u.requestIDs = append(u.requestIDs, req.URL.Path)
	}
	u.mu.Unlock()
	return &http.Response{
		StatusCode: u.status,
		Header: http.Header{
			"Content-Type": []string{u.contentType},
			"X-Request-Id": []string{"upstream-media-id"},
		},
		Body: io.NopCloser(strings.NewReader(u.body)),
	}, nil
}

func (u *syncMediaE2EUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func newSyncMediaE2ECache(t *testing.T, store *syncMediaE2EObjectStore) (*service.MediaProxy, *service.SyncMediaCache) {
	t.Helper()
	proxy, err := service.NewMediaProxy(strings.Repeat("m", 32), "")
	require.NoError(t, err)
	proxy.SetObjectStore(store)
	cache, err := service.NewSyncMediaCache(store, 1<<20, time.Minute, "image-sync", func(_ context.Context, key string, _ time.Duration, scope string) (string, error) {
		return proxy.IssueObjectURL(key, scope)
	})
	require.NoError(t, err)
	cache.SetSourceFetcher(func(_ context.Context, rawURL string) ([]byte, string, error) {
		switch rawURL {
		case "https://aipro.hk.cn/generated.png?sig=short":
			return []byte("\x89PNG\r\n\x1a\n"), "image/png", nil
		case "https://aipro.hk.cn/broken.png?sig=bad":
			return nil, "", errors.New("simulated origin outage")
		default:
			return nil, "", errors.New("unexpected origin URL")
		}
	})
	return proxy, cache
}

func mediaE2ERequestContext() context.Context {
	return gatewaylite.ContextWithRequestMeta(context.Background(), gatewaylite.RequestMeta{
		RequestID: "canonical-media-e2e-request",
		KeyID:     "media-e2e-key",
		UserID:    42,
	})
}

func serveMediaE2EURL(t *testing.T, proxy *service.MediaProxy, rawURL string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v1/media/:token", NewMediaProxyHandler(proxy).Serve)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, rawURL, nil))
	return recorder
}

func TestSynchronousOpenAIImageURLProxyEndToEndUsesCanonicalRequestAndServesCachedImage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newSyncMediaE2EObjectStore()
	proxy, cache := newSyncMediaE2ECache(t, store)
	upstream := &syncMediaE2EUpstream{
		status:      http.StatusOK,
		contentType: "application/json",
		body:        `{"created":1710000000,"data":[{"url":"https://aipro.hk.cn/generated.png?sig=short"},{"url":"https://aipro.hk.cn/broken.png?sig=bad"}]}`,
	}
	svc := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil,
		upstream, nil, nil, nil, nil, nil, nil, nil,
	)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(mediaE2ERequestContext())
	c.Request = request
	svc.SetSyncMediaCache(cache)
	parsed, err := svc.ParseOpenAIImagesRequest(c, []byte(`{"model":"gpt-image-2","prompt":"draw"}`))
	require.NoError(t, err)
	_, err = svc.ForwardImages(request.Context(), c, &service.Account{
		ID:       101,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "upstream-key",
			"base_url": "https://api.openai.com/v1",
		},
	}, []byte(`{"model":"gpt-image-2","prompt":"draw"}`), parsed, "")
	require.NoError(t, err)

	rewrittenURL := gjson.GetBytes(recorder.Body.Bytes(), "data.0.url").String()
	brokenURL := gjson.GetBytes(recorder.Body.Bytes(), "data.1.url").String()
	require.NotEmpty(t, rewrittenURL)
	require.Contains(t, rewrittenURL, "/v1/media/")
	require.NotContains(t, rewrittenURL, "aipro.hk.cn")
	require.Equal(t, "https://aipro.hk.cn/broken.png?sig=bad", brokenURL, "缓存失败必须保留原始 URL")
	require.Len(t, store.keys(), 1)
	require.Equal(t, expectedSyncMediaE2EObjectKey("canonical-media-e2e-request", 0), store.keys()[0])

	mediaResponse := serveMediaE2EURL(t, proxy, rewrittenURL)
	require.Equal(t, http.StatusOK, mediaResponse.Code)
	require.Equal(t, "image/png", mediaResponse.Header().Get("Content-Type"))
	require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), mediaResponse.Body.Bytes())
	require.Equal(t, "nosniff", mediaResponse.Header().Get("X-Content-Type-Options"))
}

func TestSynchronousGrokImageURLProxyEndToEndUsesCanonicalRequestAndInvalidTokenIsNotReadable(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)
	store := newSyncMediaE2EObjectStore()
	proxy, cache := newSyncMediaE2ECache(t, store)
	upstream := &syncMediaE2EUpstream{
		status:      http.StatusOK,
		contentType: "application/json",
		body:        `{"data":[{"url":"https://aipro.hk.cn/generated.png?sig=short"}]}`,
	}
	svc := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil, nil, nil, nil, nil,
		upstream, nil, nil, nil, nil, nil, nil, nil,
	)
	svc.SetSyncMediaCache(cache)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	requestBody := []byte(`{"model":"grok-imagine-image","prompt":"draw"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(mediaE2ERequestContext())
	c.Request = request
	_, err := svc.ForwardGrokMedia(request.Context(), c, &service.Account{
		ID:          102,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-key", "base_url": "https://xai.test/v1"},
	}, service.GrokMediaEndpointImagesGenerations, "", requestBody, "application/json")
	require.NoError(t, err)

	rewrittenURL := gjson.GetBytes(recorder.Body.Bytes(), "data.0.url").String()
	require.Contains(t, rewrittenURL, "/v1/media/")
	require.NotContains(t, rewrittenURL, "aipro.hk.cn")
	mediaResponse := serveMediaE2EURL(t, proxy, rewrittenURL)
	require.Equal(t, http.StatusOK, mediaResponse.Code)
	require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), mediaResponse.Body.Bytes())

	invalidResponse := serveMediaE2EURL(t, proxy, "/v1/media/mp2.invalid-token")
	require.Equal(t, http.StatusNotFound, invalidResponse.Code)
}
