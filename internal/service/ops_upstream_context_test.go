package service

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSetOpsUpstreamModelStoresTrimmedRequestScopedValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	SetOpsUpstreamModel(c, "  gpt-5.6-luna  ")
	value, ok := c.Get(OpsUpstreamModelKey)
	require.True(t, ok)
	require.Equal(t, "gpt-5.6-luna", value)

	SetOpsUpstreamModel(c, "   ")
	value, ok = c.Get(OpsUpstreamModelKey)
	require.True(t, ok, "an empty update must not erase the last attempted upstream model")
	require.Equal(t, "gpt-5.6-luna", value)
	require.Equal(t, "gpt-5.6-luna", OpsUpstreamModel(c))
}

func TestSetOpsUpstreamModelNilContextIsSafe(t *testing.T) {
	require.NotPanics(t, func() {
		SetOpsUpstreamModel(nil, "gpt-5.6-luna")
	})
}

func TestOpsUpstreamModelMissingContextReturnsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	require.Empty(t, OpsUpstreamModel(c))
	require.Empty(t, OpsUpstreamModel(nil))
}

func TestSafeUpstreamURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips query", "https://api.anthropic.com/v1/messages?beta=true", "https://api.anthropic.com/v1/messages"},
		{"strips fragment", "https://api.openai.com/v1/responses#frag", "https://api.openai.com/v1/responses"},
		{"strips both", "https://host/path?token=secret#x", "https://host/path"},
		{"no query or fragment", "https://host/path", "https://host/path"},
		{"empty string", "", ""},
		{"whitespace only", "  ", ""},
		{"query before fragment", "https://h/p?a=1#f", "https://h/p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeUpstreamURL(tt.input))
		})
	}
}

func TestSafeProxyHost(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"url with credentials", "http://user:secret@proxy.example.com:8080/path?token=x", "proxy.example.com"},
		{"host only", "proxy.example.com", "proxy.example.com"},
		{"host port", "proxy.example.com:8080", "proxy.example.com"},
		{"ipv6 url", "http://[2001:db8::1]:8080", "2001:db8::1"},
		{"empty", " ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, safeProxyHost(tt.input))
		})
	}
}
