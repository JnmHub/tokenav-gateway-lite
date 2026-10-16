//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyQualityV0162IncludesGrokAndTreatsUnauthorizedAsReachable(t *testing.T) {
	var grokTarget *proxyQualityTarget
	for i := range proxyQualityTargets {
		if proxyQualityTargets[i].Target == "grok" {
			grokTarget = &proxyQualityTargets[i]
			break
		}
	}
	require.NotNil(t, grokTarget)
	require.Equal(t, "https://api.x.ai/v1/models", grokTarget.URL)
	_, ok := grokTarget.AllowedStatuses[http.StatusUnauthorized]
	require.True(t, ok)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	item := runProxyQualityTarget(context.Background(), server.Client(), proxyQualityTarget{
		Target: "grok",
		URL:    server.URL,
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized: {},
		},
	})
	require.Equal(t, "pass", item.Status)
	require.Equal(t, http.StatusUnauthorized, item.HTTPStatus)
}
