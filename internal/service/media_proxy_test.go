package service

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/stretchr/testify/require"
)

func TestMediaProxyTokenHidesTargetAndValidatesExpiry(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "https://gateway.example.com/api")
	require.NoError(t, err)
	token, err := p.IssueToken("https://aipro.hk.cn/image.png?sig=secret-value", "user:42")
	require.NoError(t, err)
	require.NotContains(t, token, "aipro.hk.cn")
	require.NotContains(t, token, "secret-value")
	require.Contains(t, p.URLForToken(token), "https://gateway.example.com/api/v1/media/")
	_, scope, err := p.Claims(token, "user:42")
	require.NoError(t, err)
	require.Equal(t, "user:42", scope)
	_, _, err = p.Claims(token, "user:43")
	require.ErrorIs(t, err, errMediaProxyTokenInvalid)

	parts := strings.SplitN(token, ".", 2)
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, decodeErr)
	decoded[len(decoded)-1] ^= 1
	parts[1] = base64.RawURLEncoding.EncodeToString(decoded)
	_, _, err = p.Claims(strings.Join(parts, "."), "")
	require.ErrorIs(t, err, errMediaProxyTokenInvalid)
}

func TestMediaProxyRewriteImageOutputJSON(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "")
	require.NoError(t, err)
	body := []byte(`{"created":1,"data":[{"url":"https://aipro.hk.cn/a.png?token=x"},{"b64_json":"abc"}],"input":{"url":"https://example.com/input.png"}}`)
	rewritten, err := RewriteImageOutputJSON(body, func(raw string) (string, error) { return p.IssueURL(raw, "key:1") })
	require.NoError(t, err)
	text := string(rewritten)
	require.NotContains(t, text, "aipro.hk.cn")
	require.Contains(t, text, `"b64_json":"abc"`)
	require.Contains(t, text, `"url":"https://example.com/input.png"`)
	require.Contains(t, text, `"url":"/v1/media/`)
}

func TestMediaProxyRewriteImageOutputJSONPreservesLargeNumbers(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "")
	require.NoError(t, err)
	body := []byte(`{"created":9223372036854775807,"data":[{"url":"https://aipro.hk.cn/generated.png"}],"usage":{"total_tokens":9007199254740993}}`)
	rewritten, err := RewriteImageOutputJSON(body, func(raw string) (string, error) { return p.IssueURL(raw, "key:large") })
	require.NoError(t, err)
	text := string(rewritten)
	require.Contains(t, text, `"created":9223372036854775807`)
	require.Contains(t, text, `"total_tokens":9007199254740993`)
	require.Contains(t, text, `"url":"/v1/media/`)
}

func TestMediaProxyRewriteImageOutputJSONBestEffortPreservesLargeNumbers(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "")
	require.NoError(t, err)
	body := []byte(`{"created":9223372036854775807,"data":[{"url":"https://aipro.hk.cn/generated.png"}],"usage":{"total_tokens":9007199254740993}}`)
	rewritten := RewriteImageOutputJSONBestEffort(body, func(raw string) (string, error) { return p.IssueURL(raw, "key:large") })
	text := string(rewritten)
	require.Contains(t, text, `"created":9223372036854775807`)
	require.Contains(t, text, `"total_tokens":9007199254740993`)
	require.Contains(t, text, `"url":"/v1/media/`)
}

func TestMediaProxyFetchBlocksNonImageAndReturnsImage(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "")
	require.NoError(t, err)
	_, err = p.IssueToken("http://example.com/image.png", "")
	require.Error(t, err)
	token, err := p.IssueToken("https://example.com/image.png", "")
	require.NoError(t, err)
	p.client = &http.Client{Transport: mediaProxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"image/png"}},
			Body:       io.NopCloser(strings.NewReader("\x89PNG\r\n\x1a\n")),
			Request:    req,
		}, nil
	})}
	result, err := p.Fetch(context.Background(), token, "")
	require.NoError(t, err)
	require.Equal(t, "image/png", result.ContentType)
	require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), result.Body)

	// A malformed/expired token must never trigger a network request.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = p.Fetch(ctx, "mp1.invalid", "")
	require.ErrorIs(t, err, errMediaProxyTokenInvalid)
}

func TestMediaProxyFetchObjectTokenReadsCachedImage(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "")
	require.NoError(t, err)
	store := newSyncMediaCacheStoreStub()
	_, _, err = store.Put(context.Background(), "image-sync/test/0000", strings.NewReader("\x89PNG\r\n\x1a\n"), "image/png", 1<<20)
	require.NoError(t, err)
	p.SetObjectStore(store)
	url, err := p.IssueObjectURL("image-sync/test/0000", "key:1")
	require.NoError(t, err)
	require.NotContains(t, url, "image-sync/test")
	token := strings.TrimPrefix(url, "/v1/media/")
	result, err := p.Fetch(context.Background(), token, "key:1")
	require.NoError(t, err)
	require.Equal(t, "image/png", result.ContentType)
	require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), result.Body)
	_, err = p.Fetch(context.Background(), token, "key:2")
	require.ErrorIs(t, err, errMediaProxyTokenInvalid)
}

func TestMediaProxyFetchRejectsActiveOrMismatchedContent(t *testing.T) {
	p, err := NewMediaProxy(strings.Repeat("s", 32), "")
	require.NoError(t, err)
	token, err := p.IssueToken("https://example.com/image.png", "")
	require.NoError(t, err)

	for _, tc := range []struct {
		name        string
		contentType string
		body        string
	}{
		{name: "svg", contentType: "image/svg+xml", body: `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		{name: "html claimed png", contentType: "image/png", body: "<html>not an image</html>"},
		{name: "jpeg claimed png", contentType: "image/png", body: "\xff\xd8\xff\xe0jpeg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.client = &http.Client{Transport: mediaProxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{tc.contentType}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Request:    req,
				}, nil
			})}
			_, fetchErr := p.Fetch(context.Background(), token, "")
			require.Error(t, fetchErr)
		})
	}
}

type mediaProxyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mediaProxyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestMediaProxyPublicURLValidation(t *testing.T) {
	for _, value := range []string{"ftp://gateway.example.com", "https://user:pass@gateway.example.com", "https://gateway.example.com?a=1", "https://gateway.example.com?", "https://gateway.example.com/#fragment", "https://gateway.example.com:99999", "https://gateway.example.com:", "http://:18083", "//gateway.example.com"} {
		require.Error(t, ValidateMediaProxyPublicURL(value), value)
	}
	for _, value := range []string{"", "https://gateway.example.com/base", "http://127.0.0.1:18083", "http://192.168.1.43:18083", "http://[::1]:18083", "http://gateway.example.com/base"} {
		require.NoError(t, ValidateMediaProxyPublicURL(value), value)
	}
}

// Persisted binding injection happens after configuration loading. Exercise the
// actual provider with that destination so activation cannot hide a nil proxy.
type bindingMediaStartupStore struct{ record gatewaylite.BindingRecord }

func (s *bindingMediaStartupStore) Load(context.Context) (gatewaylite.BindingRecord, error) {
	return s.record, nil
}
func (s *bindingMediaStartupStore) Save(_ context.Context, record gatewaylite.BindingRecord, expected int64) error {
	if s.record.Revision != expected {
		return gatewaylite.ErrBindingConflict
	}
	s.record = record
	return nil
}

func TestBindingHTTPPublicURLInitializesMediaProviderAndServesObject(t *testing.T) {
	for _, publicURL := range []string{"http://127.0.0.1:18083", "http://192.168.1.43:18083", "http://[::1]:18083", "https://gateway.example.com/base"} {
		t.Run(publicURL, func(t *testing.T) {
			ctx := context.Background()
			bindingStore := &bindingMediaStartupStore{record: gatewaylite.BindingRecord{Revision: 1, InstanceID: "media-startup-fixture", Phase: "activating", Pending: &gatewaylite.BindingDestination{BindingID: "media-binding-fixture", URL: "http://127.0.0.1:18081", GatewayCode: "media-gateway-fixture", NodeScope: "media-gateway-fixture", RedisPrefix: "gl:binding:media-fixture", PublicURL: publicURL, Region: "local"}}}
			manager, err := gatewaylite.NewBindingManager(ctx, bindingStore, config.GatewayLiteConfig{})
			require.NoError(t, err)
			cfg := &config.Config{RunMode: config.RunModeGatewayLite, GatewayLite: config.GatewayLiteConfig{MediaProxyEnabled: true, MediaProxyTTLSeconds: 900}}
			cfg.JWT.Secret = "media-fixture-" + t.Name()
			manager.ApplyStartup(&cfg.GatewayLite)
			require.Equal(t, publicURL, cfg.GatewayLite.MediaProxyPublicURL)
			require.NoError(t, config.ValidateGatewayLiteMediaProxyPublicURL(cfg.GatewayLite.MediaProxyPublicURL))
			objects := newSyncMediaCacheStoreStub()
			proxy := ProvideMediaProxy(cfg, objects)
			require.NotNil(t, proxy, "accepted binding origin must not silently disable media initialization")
			_, _, err = objects.Put(ctx, "image-sync/local-fixture/0000", strings.NewReader("\x89PNG\r\n\x1a\n"), "image/png", 1<<20)
			require.NoError(t, err)
			link, err := proxy.IssueObjectURL("image-sync/local-fixture/0000", "key:fixture")
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(link, strings.TrimRight(publicURL, "/")+"/v1/media/"))
			u, err := url.Parse(link)
			require.NoError(t, err)
			token := u.Path[strings.LastIndex(u.Path, "/")+1:]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				result, err := proxy.Fetch(request.Context(), token, "key:fixture")
				if err != nil {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.Header().Set("Content-Type", result.ContentType)
				_, _ = w.Write(result.Body)
			}))
			defer server.Close()
			response, err := server.Client().Get(server.URL + "/v1/media/" + token)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "image/png", response.Header.Get("Content-Type"))
			require.Equal(t, []byte("\x89PNG\r\n\x1a\n"), body)
			for _, target := range []string{"http://example.com/image.png", "http://127.0.0.1:18083/image.png", "https://127.0.0.1/image.png", "https://192.168.1.43/image.png", "https://169.254.169.254/latest/meta-data"} {
				_, err := proxy.IssueToken(target, "key:fixture")
				require.Error(t, err, "public HTTP configuration must not relax upstream URL safety: "+target)
			}
			_, err = proxy.IssueToken("https://example.com/image.png", "key:fixture")
			require.NoError(t, err)
			_, err = proxy.Fetch(ctx, token, "key:other")
			require.ErrorIs(t, err, errMediaProxyTokenInvalid)
		})
	}
}
