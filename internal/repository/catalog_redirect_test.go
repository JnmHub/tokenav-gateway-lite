package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogClientsRejectHTTPRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		_, _ = w.Write([]byte(`{"unexpected":true}`))
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	pricing := NewPricingRemoteClient("", false)
	_, err := pricing.FetchPricingJSON(context.Background(), redirect.URL)
	require.ErrorContains(t, err, "HTTP 302")

	keywords := NewKeywordCatalogRemoteClient("", false)
	_, err = keywords.Fetch(context.Background(), redirect.URL, "", 1024)
	require.ErrorContains(t, err, "HTTP 302")
	require.Zero(t, targetCalls.Load(), "目录下载不得跟随重定向访问第二个地址")
}
