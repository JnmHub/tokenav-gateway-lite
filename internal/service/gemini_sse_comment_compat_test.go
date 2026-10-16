package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiClientRejectsSSEComments(t *testing.T) {
	cases := []struct {
		name string
		hint string
		want bool
	}{
		{"go-genai (Antigravity CLI)", "google-genai-sdk/1.71.0 gl-go/go1.28-20260721-RC03 cl/951519500 +3ebc191975 X:fieldtrack,boringcrypto", true},
		{"python-genai", "google-genai-sdk/1.20.0 gl-python/3.12.4", true},
		{"js-genai tolerates comments", "google-genai-sdk/1.9.0 gl-node/22.3.0", false},
		{"gemini-cli", "GeminiCLI/0.60.0 (darwin; arm64)", false},
		{"curl", "curl/8.7.1", false},
		{"empty", "", false},
		{"case-insensitive", "Google-GenAI-SDK/1.0.0 GL-Go/go1.27", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, geminiClientRejectsSSEComments(tc.hint))
		})
	}
}

func TestDownstreamRejectsSSECommentsReadsBothHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// fork 定制：官方测试用 antigravity compat 测试辅助构造上下文；本仓库的
	// antigravity 网关是单文件定制架构，直接用 gin 测试上下文等价构造。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.8-flash:streamGenerateContent", nil)
	require.False(t, downstreamRejectsSSEComments(c))

	c.Request.Header.Set("X-Goog-Api-Client", "google-genai-sdk/1.71.0 gl-go/1.28")
	require.True(t, downstreamRejectsSSEComments(c))

	c.Request.Header.Del("X-Goog-Api-Client")
	c.Request.Header.Set("User-Agent", "google-genai-sdk/1.71.0 gl-go/1.28")
	require.True(t, downstreamRejectsSSEComments(c))

	require.False(t, downstreamRejectsSSEComments(nil))
}

// 说明：官方 0.2.7 的两个 keepalive 集成测试
// （TestAntigravityGeminiStreamKeepsCommentKeepaliveForOrdinaryClients /
//  TestAntigravityGeminiStreamSkipsCommentKeepaliveForGoGenai）依赖官方拆分式
// antigravity compat 流式架构（handleGeminiStreamingResponse），本仓库的
// antigravity 网关是单文件定制架构，无法直接复用；跳过逻辑已接入本仓库
// keepalive 循环，集成验证随 antigravity 模块回归覆盖。
