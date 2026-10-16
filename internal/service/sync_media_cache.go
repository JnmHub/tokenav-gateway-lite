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
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// SyncMediaCacheDefaultPrefix keeps synchronous results separate from the
	// durable asynchronous image-task objects.  A separate namespace lets the
	// two cleanup policies evolve independently.
	SyncMediaCacheDefaultPrefix = "image-sync"
	SyncMediaCacheMaxTTL        = 24 * time.Hour
)

var (
	errSyncMediaCacheUnavailable = errors.New("synchronous media cache is unavailable")
	errSyncMediaCacheInvalid     = errors.New("invalid synchronous media artifact")
)

// SyncMediaCacheURLFunc converts a stored object key into a client-facing
// address. In the gateway wiring this callback issues an encrypted object
// token, so both local and S3-compatible objects are read through the
// gateway's media route; the store SignedURL fallback remains available for
// isolated callers and test doubles. scope is deliberately supplied
// separately from the object key so callers can bind a URL to an API key or
// user without putting that data in storage.
type SyncMediaCacheURLFunc func(ctx context.Context, key string, ttl time.Duration, scope string) (string, error)

// SyncMediaObjectStore is the narrow object-store contract used by the
// synchronous bridge. It is intentionally a distinct interface from the
// asynchronous task store so Wire can inject both stores without ambiguity.
type SyncMediaObjectStore interface {
	Put(ctx context.Context, key string, body io.Reader, contentType string, maxBytes int64) (size int64, sha256Hex string, err error)
	Open(ctx context.Context, key string) (io.ReadCloser, string, int64, error)
	Delete(ctx context.Context, key string) error
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// SyncMediaObjectStoreJanitor is an optional lifecycle extension implemented
// by the bundled local/S3 stores. Keeping it separate from the read/write
// contract lets custom stores and existing test doubles opt out safely.
// before is compared with the store's last-modified time, and limit bounds one
// cleanup pass so a large backlog cannot monopolize the request process.
type SyncMediaObjectStoreJanitor interface {
	DeletePrefixOlderThan(ctx context.Context, prefix string, before time.Time, limit int) (deleted int, err error)
}

// SyncMediaSourceFetcher downloads one upstream image without forwarding the
// caller's credentials.  It is kept injectable so tests can use an in-memory
// source while production uses the SSRF-checked media transport.
type SyncMediaSourceFetcher func(ctx context.Context, rawURL string) ([]byte, string, error)

// SyncMediaArtifact is the public metadata returned after a synchronous image
// has been copied into the gateway.  ObjectKey is internal and must not be
// serialized into an untrusted response unless the caller explicitly needs
// it for a protected download route.
type SyncMediaArtifact struct {
	ObjectKey   string
	URL         string
	ContentType string
	SizeBytes   int64
	SHA256      string
	ExpiresAt   time.Time
}

// SyncMediaCache stores already validated image bytes before a synchronous
// response is sent to the client.  This is intentionally independent of the
// asynchronous image-task switch: a node can bridge normal image requests
// even when no durable task worker is enabled.
type SyncMediaCache struct {
	objects          SyncMediaObjectStore
	maxBytes         int64
	ttl              time.Duration
	prefix           string
	publicURL        SyncMediaCacheURLFunc
	fetcher          SyncMediaSourceFetcher
	cleanupStop      chan struct{}
	cleanupStartOnce sync.Once
	cleanupStopOnce  sync.Once
	cleanupWG        sync.WaitGroup
}

const syncMediaCacheCleanupInterval = time.Hour

// NewSyncMediaCache creates a bounded synchronous media cache.  The object
// store is shared with the existing local/S3 implementation, while publicURL
// is optional for callers that can use the store's SignedURL method directly.
func NewSyncMediaCache(objects SyncMediaObjectStore, maxBytes int64, ttl time.Duration, prefix string, publicURL SyncMediaCacheURLFunc) (*SyncMediaCache, error) {
	if objects == nil {
		return nil, errSyncMediaCacheUnavailable
	}
	if maxBytes <= 0 || maxBytes > mediaProxyMaxBodySize {
		return nil, fmt.Errorf("synchronous media cache max bytes must be between 1 and %d", mediaProxyMaxBodySize)
	}
	if ttl < time.Minute || ttl > SyncMediaCacheMaxTTL {
		return nil, errors.New("synchronous media cache ttl must be between 1 minute and 24 hours")
	}
	prefix = strings.Trim(strings.TrimSpace(strings.ReplaceAll(prefix, "\\", "/")), "/")
	if prefix == "" {
		prefix = SyncMediaCacheDefaultPrefix
	}
	if strings.Contains(prefix, "..") {
		return nil, errors.New("synchronous media cache prefix is invalid")
	}
	return &SyncMediaCache{
		objects:   objects,
		maxBytes:  maxBytes,
		ttl:       ttl,
		prefix:    prefix,
		publicURL: publicURL,
		fetcher:   newSynchronousMediaSourceFetcher(),
	}, nil
}

// SetSourceFetcher replaces the controlled source downloader.  Production
// callers should leave the default in place; the setter exists for tests and
// for deployments that provide a separately managed, audited egress client.
func (c *SyncMediaCache) SetSourceFetcher(fetcher SyncMediaSourceFetcher) {
	if c != nil {
		c.fetcher = fetcher
	}
}

// Start begins bounded background cleanup for synchronous image objects. The
// cleanup is independent from the asynchronous image-task worker because
// synchronous responses do not create task rows.
func (c *SyncMediaCache) Start() {
	if c == nil || c.objects == nil {
		return
	}
	if _, ok := c.objects.(SyncMediaObjectStoreJanitor); !ok {
		return
	}
	c.cleanupStartOnce.Do(func() {
		c.cleanupStop = make(chan struct{})
		c.cleanupWG.Add(1)
		go c.cleanupLoop()
	})
}

// Stop terminates the cleanup loop and waits for an in-flight bounded pass.
func (c *SyncMediaCache) Stop() {
	if c == nil {
		return
	}
	c.cleanupStopOnce.Do(func() {
		if c.cleanupStop != nil {
			close(c.cleanupStop)
		}
	})
	c.cleanupWG.Wait()
}

func (c *SyncMediaCache) cleanupLoop() {
	defer c.cleanupWG.Done()
	c.cleanupOnce()
	ticker := time.NewTicker(syncMediaCacheCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.cleanupOnce()
		case <-c.cleanupStop:
			return
		}
	}
}

func (c *SyncMediaCache) cleanupOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if deleted, err := c.CleanupExpired(ctx, 1000); err != nil {
		logger.LegacyPrintf("service.sync_media_cache", "synchronous media cleanup failed: %v", err)
	} else if deleted > 0 {
		logger.LegacyPrintf("service.sync_media_cache", "synchronous media cleanup removed %d objects", deleted)
	}
}

// PutRemote downloads and stores one upstream image.  The URL is validated
// before the request and the response is validated by bytes (not only by the
// remote Content-Type), so an HTML error page can never become an image
// object.  No Authorization or Cookie headers are copied to this request.
func (c *SyncMediaCache) PutRemote(ctx context.Context, requestID string, index int, rawURL, scope string) (SyncMediaArtifact, error) {
	if c == nil || c.fetcher == nil {
		return SyncMediaArtifact{}, errSyncMediaCacheUnavailable
	}
	validatedURL, err := validateMediaProxyTarget(rawURL)
	if err != nil {
		return SyncMediaArtifact{}, err
	}
	body, contentType, err := c.fetcher(ctx, validatedURL)
	if err != nil {
		return SyncMediaArtifact{}, fmt.Errorf("download synchronous media: %w", err)
	}
	return c.Put(ctx, requestID, index, body, contentType, scope)
}

// RewriteOutputURLs copies remote URLs found only in generated-image output
// containers into this gateway and returns the rewritten JSON.  It is
// best-effort by design: a failed copy leaves that one original URL intact,
// while successfully copied images in the same response remain usable.
// firstErr is returned for internal observability and must not be sent to the
// client because it may contain upstream transport details.
func (c *SyncMediaCache) RewriteOutputURLs(ctx context.Context, body []byte, requestID, scope string) ([]byte, error) {
	if c == nil || c.objects == nil || strings.TrimSpace(requestID) == "" || len(body) == 0 {
		return body, nil
	}
	root, err := decodeMediaJSON(body)
	if err != nil {
		return body, nil
	}
	index := 0
	firstErr := error(nil)
	seen := make(map[string]string)
	failed := make(map[string]struct{})
	_, _ = rewriteMediaOutputNodeBestEffort(&root, false, func(rawURL string) (string, error) {
		rawURL = strings.TrimSpace(rawURL)
		if cached, ok := seen[rawURL]; ok {
			return cached, nil
		}
		// Do not retry the same failed origin repeatedly within one response.
		// A response may repeat one URL in several output entries; retrying it
		// for every entry would amplify an upstream outage and add needless
		// latency while still leaving all copies unchanged.
		if _, ok := failed[rawURL]; ok {
			return rawURL, nil
		}
		artifact, err := c.PutRemote(ctx, requestID, index, rawURL, scope)
		index++
		if err != nil {
			failed[rawURL] = struct{}{}
			if firstErr == nil {
				firstErr = err
			}
			return rawURL, nil
		}
		seen[rawURL] = artifact.URL
		return artifact.URL, nil
	})
	if len(seen) == 0 {
		return body, firstErr
	}
	rewritten, err := json.Marshal(root)
	if err != nil {
		return body, fmt.Errorf("encode synchronous media response: %w", err)
	}
	return rewritten, firstErr
}

func newSynchronousMediaSourceFetcher() SyncMediaSourceFetcher {
	client := newMediaProxyHTTPClient()
	return func(ctx context.Context, rawURL string) ([]byte, string, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, "", err
		}
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*;q=0.9")
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("upstream returned status %d", resp.StatusCode)
		}
		if resp.ContentLength > mediaProxyMaxBodySize {
			return nil, "", errors.New("upstream image is too large")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, mediaProxyMaxBodySize+1))
		if err != nil {
			return nil, "", err
		}
		if int64(len(data)) > mediaProxyMaxBodySize {
			return nil, "", errors.New("upstream image is too large")
		}
		contentType, err := validateMediaImageContent(data, resp.Header.Get("Content-Type"))
		if err != nil {
			return nil, "", err
		}
		return data, contentType, nil
	}
}

// Put validates and stores one generated image, then creates its client URL.
// If URL creation fails, the object is removed immediately so a failed
// response cannot accumulate unreachable files.
func (c *SyncMediaCache) Put(ctx context.Context, requestID string, index int, body []byte, declaredContentType, scope string) (SyncMediaArtifact, error) {
	if c == nil || c.objects == nil {
		return SyncMediaArtifact{}, errSyncMediaCacheUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(requestID) == "" || index < 0 || index > 9999 || len(body) == 0 {
		return SyncMediaArtifact{}, errSyncMediaCacheInvalid
	}
	contentType, err := validateMediaImageContent(body, declaredContentType)
	if err != nil {
		return SyncMediaArtifact{}, fmt.Errorf("validate synchronous media artifact: %w", err)
	}
	key := c.objectKey(requestID, index)
	size, digest, err := c.objects.Put(ctx, key, bytes.NewReader(body), contentType, c.maxBytes)
	if err != nil {
		return SyncMediaArtifact{}, fmt.Errorf("store synchronous media artifact: %w", err)
	}
	url, err := c.signedURL(ctx, key, scope)
	if err != nil {
		_ = c.objects.Delete(context.Background(), key)
		return SyncMediaArtifact{}, fmt.Errorf("sign synchronous media artifact: %w", err)
	}
	if strings.TrimSpace(url) == "" {
		_ = c.objects.Delete(context.Background(), key)
		return SyncMediaArtifact{}, errors.New("sign synchronous media artifact: empty URL")
	}
	return SyncMediaArtifact{
		ObjectKey:   key,
		URL:         url,
		ContentType: contentType,
		SizeBytes:   size,
		SHA256:      digest,
		ExpiresAt:   time.Now().UTC().Add(c.ttl),
	}, nil
}

// Delete removes a cached result.  It is safe to call during response-error
// cleanup because object stores already treat a missing key as idempotent.
func (c *SyncMediaCache) Delete(ctx context.Context, key string) error {
	if c == nil || c.objects == nil {
		return errSyncMediaCacheUnavailable
	}
	if strings.TrimSpace(key) == "" {
		return errSyncMediaCacheInvalid
	}
	return c.objects.Delete(ctx, key)
}

// CleanupExpired removes stale synchronous media objects when the underlying
// store exposes the optional janitor contract. The cutoff intentionally uses
// the same TTL as issued URLs: once no URL can be valid, retaining the bytes
// only wastes local disk or object-storage space. Stores without a janitor are
// left untouched rather than failing an otherwise healthy image response.
func (c *SyncMediaCache) CleanupExpired(ctx context.Context, limit int) (int, error) {
	if c == nil || c.objects == nil {
		return 0, nil
	}
	janitor, ok := c.objects.(SyncMediaObjectStoreJanitor)
	if !ok {
		return 0, nil
	}
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return janitor.DeletePrefixOlderThan(ctx, c.prefix, time.Now().UTC().Add(-c.ttl), limit)
}

func (c *SyncMediaCache) signedURL(ctx context.Context, key, scope string) (string, error) {
	if c.publicURL != nil {
		return c.publicURL(ctx, key, c.ttl, strings.TrimSpace(scope))
	}
	return c.objects.SignedURL(ctx, key, c.ttl)
}

func (c *SyncMediaCache) objectKey(requestID string, index int) string {
	// Hashing the request identifier makes the key deterministic for retries
	// while preventing user-controlled slashes, query strings, or identifiers
	// from becoming an object-store path component.
	digest := sha256.Sum256([]byte(strings.TrimSpace(requestID)))
	return fmt.Sprintf("%s/%s/%04d", c.prefix, hex.EncodeToString(digest[:16]), index)
}
