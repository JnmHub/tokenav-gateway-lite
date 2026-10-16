package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMediaProxyFetchRejectsExpiredObjectTokenWithoutOpeningStore(t *testing.T) {
	proxy, err := NewMediaProxy(strings.Repeat("e", 32), "")
	require.NoError(t, err)
	store := newSyncMediaCacheStoreStub()
	proxy.SetObjectStore(store)
	token, err := proxy.sealClaims(mediaProxyObjectTokenVersion, mediaProxyClaims{
		ObjectKey: "image-sync/expired/0000",
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	})
	require.NoError(t, err)

	_, err = proxy.Fetch(context.Background(), token, "")
	require.ErrorIs(t, err, errMediaProxyTokenInvalid)
	require.Empty(t, store.objects, "过期令牌在校验阶段失败，不应读取对象存储")
}

func TestMediaProxyFetchReturnsOriginFailureForValidRemoteToken(t *testing.T) {
	proxy, err := NewMediaProxy(strings.Repeat("f", 32), "")
	require.NoError(t, err)
	token, err := proxy.IssueToken("https://aipro.hk.cn/generated.png?sig=expired-upstream", "")
	require.NoError(t, err)
	proxy.client = &http.Client{Transport: mediaProxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, http.MethodGet, req.Method)
		require.Empty(t, req.Header.Get("Authorization"), "回源不能携带客户或上游凭据")
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":"origin unavailable"}`)),
			Request:    req,
		}, nil
	})}

	_, err = proxy.Fetch(context.Background(), token, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "upstream returned status 502")
}

func TestMediaProxyFetchObjectTokenAcceptsNilContext(t *testing.T) {
	proxy, err := NewMediaProxy(strings.Repeat("n", 32), "")
	require.NoError(t, err)
	store := newSyncMediaCacheStoreStub()
	_, _, err = store.Put(context.Background(), "image-sync/nil-context/0000", strings.NewReader("\x89PNG\r\n\x1a\n"), "image/png", 1<<20)
	require.NoError(t, err)
	proxy.SetObjectStore(store)
	url, err := proxy.IssueObjectURL("image-sync/nil-context/0000", "")
	require.NoError(t, err)
	token := strings.TrimPrefix(url, "/v1/media/")
	result, err := proxy.Fetch(nil, token, "")
	require.NoError(t, err)
	require.Equal(t, "image/png", result.ContentType)
}
