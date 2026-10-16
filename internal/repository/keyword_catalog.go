package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type keywordCatalogRemoteClient struct {
	httpClient *http.Client
}

type keywordCatalogRemoteClientError struct {
	err error
}

func (c *keywordCatalogRemoteClientError) Fetch(context.Context, string, string, int64) (service.KeywordCatalogRemoteResponse, error) {
	return service.KeywordCatalogRemoteResponse{}, c.err
}

// NewKeywordCatalogRemoteClient uses the same explicitly configured outbound
// proxy policy as price/update downloads. Invalid proxy configuration remains
// fail-closed unless the operator has opted into direct fallback.
func NewKeywordCatalogRemoteClient(proxyURL string, allowDirectOnProxyError bool) service.KeywordCatalogRemoteClient {
	client, err := httpclient.GetClient(httpclient.Options{Timeout: 30 * time.Second, ProxyURL: proxyURL})
	if err != nil {
		if strings.TrimSpace(proxyURL) != "" && !allowDirectOnProxyError {
			return &keywordCatalogRemoteClientError{err: fmt.Errorf("keyword catalog proxy client initialization failed: %w", err)}
		}
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Clone before changing redirect policy because the shared HTTP client may
	// also be used by unrelated integrations. Catalog URLs are validated before
	// the request; refusing every redirect prevents a trusted GitHub URL from
	// redirecting to another origin or a private network address afterward.
	catalogClient := *client
	catalogClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &keywordCatalogRemoteClient{httpClient: &catalogClient}
}

func (c *keywordCatalogRemoteClient) Fetch(ctx context.Context, rawURL, etag string, maxBytes int64) (service.KeywordCatalogRemoteResponse, error) {
	if maxBytes <= 0 {
		return service.KeywordCatalogRemoteResponse{}, fmt.Errorf("keyword catalog response limit must be positive")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return service.KeywordCatalogRemoteResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sub2api-keyword-catalog/1.0")
	if strings.TrimSpace(etag) != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return service.KeywordCatalogRemoteResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified {
		return service.KeywordCatalogRemoteResponse{ETag: resp.Header.Get("ETag"), NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return service.KeywordCatalogRemoteResponse{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return service.KeywordCatalogRemoteResponse{}, fmt.Errorf("response content length %d exceeds limit %d", resp.ContentLength, maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return service.KeywordCatalogRemoteResponse{}, err
	}
	if int64(len(body)) > maxBytes {
		return service.KeywordCatalogRemoteResponse{}, fmt.Errorf("response body exceeds limit %d", maxBytes)
	}
	return service.KeywordCatalogRemoteResponse{Body: body, ETag: resp.Header.Get("ETag")}, nil
}
