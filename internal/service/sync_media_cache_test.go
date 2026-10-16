package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type syncMediaCacheStoreStub struct {
	objects map[string][]byte
	puts    int
	deletes int
}

func newSyncMediaCacheStoreStub() *syncMediaCacheStoreStub {
	return &syncMediaCacheStoreStub{objects: make(map[string][]byte)}
}

func (s *syncMediaCacheStoreStub) Put(_ context.Context, key string, body io.Reader, _ string, maxBytes int64) (int64, string, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return 0, "", err
	}
	if int64(len(data)) > maxBytes {
		return 0, "", errors.New("too large")
	}
	s.objects[key] = append([]byte(nil), data...)
	s.puts++
	return int64(len(data)), "digest", nil
}

func (s *syncMediaCacheStoreStub) Open(_ context.Context, key string) (io.ReadCloser, string, int64, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, "", 0, errors.New("missing")
	}
	return io.NopCloser(strings.NewReader(string(data))), "image/png", int64(len(data)), nil
}

func (s *syncMediaCacheStoreStub) Delete(_ context.Context, key string) error {
	delete(s.objects, key)
	s.deletes++
	return nil
}

func (s *syncMediaCacheStoreStub) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "/v1/media/object/" + key, nil
}

func TestSyncMediaCachePutUsesProtectedURLAndDeterministicSafeKey(t *testing.T) {
	store := newSyncMediaCacheStoreStub()
	cache, err := NewSyncMediaCache(store, 1<<20, time.Minute, "sync-images", func(_ context.Context, key string, ttl time.Duration, scope string) (string, error) {
		require.Equal(t, time.Minute, ttl)
		require.Equal(t, "key:7", scope)
		return "/v1/media/object/" + key, nil
	})
	require.NoError(t, err)
	body := []byte("\x89PNG\r\n\x1a\n")
	first, err := cache.Put(context.Background(), "request/with?unsafe", 2, body, "image/png; charset=binary", "key:7")
	require.NoError(t, err)
	second, err := cache.Put(context.Background(), "request/with?unsafe", 2, body, "image/png", "key:7")
	require.NoError(t, err)
	require.Equal(t, first.ObjectKey, second.ObjectKey)
	require.NotContains(t, first.ObjectKey, "request")
	require.Equal(t, "/v1/media/object/"+first.ObjectKey, first.URL)
	require.Equal(t, int64(len(body)), first.SizeBytes)
	require.Equal(t, "image/png", first.ContentType)
	require.Equal(t, 2, store.puts)
}

func TestSyncMediaCacheRejectsUnsafeContentAndCleansOnURLFailure(t *testing.T) {
	store := newSyncMediaCacheStoreStub()
	cache, err := NewSyncMediaCache(store, 1<<20, time.Minute, "", func(context.Context, string, time.Duration, string) (string, error) {
		return "", errors.New("route unavailable")
	})
	require.NoError(t, err)
	_, err = cache.Put(context.Background(), "request-1", 0, []byte("not an image"), "image/png", "")
	require.Error(t, err)
	require.Zero(t, store.puts)

	_, err = cache.Put(context.Background(), "request-2", 0, []byte("\x89PNG\r\n\x1a\n"), "image/png", "")
	require.Error(t, err)
	require.Equal(t, 1, store.puts)
	require.Equal(t, 1, store.deletes)
	require.Empty(t, store.objects)
}

func TestNewSyncMediaCacheValidatesBounds(t *testing.T) {
	store := newSyncMediaCacheStoreStub()
	_, err := NewSyncMediaCache(store, 0, time.Minute, "", nil)
	require.Error(t, err)
	_, err = NewSyncMediaCache(store, mediaProxyMaxBodySize+1, time.Minute, "", nil)
	require.Error(t, err)
	_, err = NewSyncMediaCache(store, 1<<20, 30*time.Second, "", nil)
	require.Error(t, err)
	_, err = NewSyncMediaCache(store, 1<<20, 25*time.Hour, "", nil)
	require.Error(t, err)
	_, err = NewSyncMediaCache(store, 1<<20, time.Minute, "../escape", nil)
	require.Error(t, err)
}

func TestSyncMediaCacheCleanupExpiredUsesOptionalJanitor(t *testing.T) {
	store := &syncMediaCacheJanitorStub{}
	cache, err := NewSyncMediaCache(store, 1<<20, time.Minute, "image-sync", nil)
	require.NoError(t, err)
	deleted, err := cache.CleanupExpired(context.Background(), 20)
	require.NoError(t, err)
	require.Equal(t, 7, deleted)
	require.Equal(t, "image-sync", store.prefix)
	require.Equal(t, 20, store.limit)
	require.WithinDuration(t, time.Now().UTC().Add(-time.Minute), store.before, 2*time.Second)
}

type syncMediaCacheJanitorStub struct {
	prefix string
	before time.Time
	limit  int
}

func (s *syncMediaCacheJanitorStub) Put(context.Context, string, io.Reader, string, int64) (int64, string, error) {
	return 0, "", nil
}
func (s *syncMediaCacheJanitorStub) Open(context.Context, string) (io.ReadCloser, string, int64, error) {
	return nil, "", 0, errors.New("unused")
}
func (s *syncMediaCacheJanitorStub) Delete(context.Context, string) error { return nil }
func (s *syncMediaCacheJanitorStub) SignedURL(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (s *syncMediaCacheJanitorStub) DeletePrefixOlderThan(_ context.Context, prefix string, before time.Time, limit int) (int, error) {
	s.prefix, s.before, s.limit = prefix, before, limit
	return 7, nil
}

func TestSyncMediaCacheRewriteOutputURLsCopiesOnlyGeneratedOutput(t *testing.T) {
	store := newSyncMediaCacheStoreStub()
	cache, err := NewSyncMediaCache(store, 1<<20, time.Minute, "", func(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
		return "/v1/media/" + key, nil
	})
	require.NoError(t, err)
	var fetched []string
	cache.SetSourceFetcher(func(_ context.Context, rawURL string) ([]byte, string, error) {
		fetched = append(fetched, rawURL)
		return []byte("\x89PNG\r\n\x1a\n"), "image/png", nil
	})
	body := []byte(`{"data":[{"url":"https://aipro.hk.cn/generated.png?sig=short"},{"b64_json":"abc"}],"input":{"url":"https://example.com/reference.png"}}`)
	rewritten, err := cache.RewriteOutputURLs(context.Background(), body, "request-123", "key:7")
	require.NoError(t, err)
	require.Equal(t, []string{"https://aipro.hk.cn/generated.png?sig=short"}, fetched)
	require.Contains(t, string(rewritten), `"url":"/v1/media/`)
	require.Contains(t, string(rewritten), `"b64_json":"abc"`)
	require.Contains(t, string(rewritten), `"url":"https://example.com/reference.png"`)
	require.Len(t, store.objects, 1)
}

func TestSyncMediaCacheRewriteOutputURLsPreservesLargeNumbers(t *testing.T) {
	store := newSyncMediaCacheStoreStub()
	cache, err := NewSyncMediaCache(store, 1<<20, time.Minute, "", func(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
		return "/v1/media/" + key, nil
	})
	require.NoError(t, err)
	cache.SetSourceFetcher(func(_ context.Context, rawURL string) ([]byte, string, error) {
		require.Equal(t, "https://aipro.hk.cn/generated.png", rawURL)
		return []byte("\x89PNG\r\n\x1a\n"), "image/png", nil
	})
	body := []byte(`{"created":9223372036854775807,"data":[{"url":"https://aipro.hk.cn/generated.png"}],"usage":{"total_tokens":9007199254740993}}`)
	rewritten, err := cache.RewriteOutputURLs(context.Background(), body, "request-large", "key:large")
	require.NoError(t, err)
	text := string(rewritten)
	require.Contains(t, text, `"created":9223372036854775807`)
	require.Contains(t, text, `"total_tokens":9007199254740993`)
	require.Contains(t, text, `"url":"/v1/media/`)
}

func TestSyncMediaCacheRewriteOutputURLsKeepsFailedCopy(t *testing.T) {
	store := newSyncMediaCacheStoreStub()
	cache, err := NewSyncMediaCache(store, 1<<20, time.Minute, "", func(_ context.Context, key string, _ time.Duration, _ string) (string, error) {
		return "/v1/media/" + key, nil
	})
	require.NoError(t, err)
	cache.SetSourceFetcher(func(context.Context, string) ([]byte, string, error) {
		return []byte("upstream failure"), "text/plain", nil
	})
	body := []byte(`{"data":[{"url":"https://aipro.hk.cn/generated.png"}]}`)
	rewritten, err := cache.RewriteOutputURLs(context.Background(), body, "request-456", "")
	require.Error(t, err)
	require.Equal(t, string(body), string(rewritten))
	require.Empty(t, store.objects)
}
