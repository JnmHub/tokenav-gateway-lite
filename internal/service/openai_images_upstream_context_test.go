package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardOpenAIImagesAPIKey_NonStreamDetachesUpstreamContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = req

	encoded := encodeOpenAIImageTestPNG(t, 2, 2)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
			`{"created":1710000000,"data":[{"b64_json":%q}]}`, encoded,
		))),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)
	require.False(t, parsed.Stream)

	clientCtx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := svc.ForwardImages(clientCtx, c, &Account{
		ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com/v1"},
	}, body, parsed, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount, "断开后仍需返回可计费的图片数量")
	require.NotNil(t, upstream.lastReq)
	deadline, ok := upstream.lastReq.Context().Deadline()
	require.True(t, ok, "生图上游 context 必须带独立硬超时")
	require.WithinDuration(t, time.Now().Add(openAIImageUpstreamTimeout), deadline, 5*time.Second)
}

func TestForwardOpenAIImagesOAuth_DetachesUpstreamContextWithHardTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = req

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"usage\":{\"input_tokens\":1,\"output_tokens\":2},\"tool_usage\":{\"image_gen\":{\"images\":1}},\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}]}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	clientCtx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := svc.ForwardImages(clientCtx, c, &Account{
		ID: 32, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token-test", "chatgpt_account_id": "acct-test"},
	}, body, parsed, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.ImageCount, "断开后仍需返回可计费的图片数量")
	require.NotNil(t, upstream.lastReq)
	deadline, ok := upstream.lastReq.Context().Deadline()
	require.True(t, ok, "OAuth 生图上游 context 必须带独立硬超时")
	require.WithinDuration(t, time.Now().Add(openAIImageUpstreamTimeout), deadline, 5*time.Second)
}

func TestDetachOpenAIImageUpstreamContextIgnoresClientCancelAndReusesDeadline(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), upstreamContextTestKey("image-key"), "image-value"))
	upstreamCtx, release := detachOpenAIImageUpstreamContext(parent)
	defer release()

	deadline, ok := upstreamCtx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(openAIImageUpstreamTimeout), deadline, 5*time.Second)
	cancelParent()
	require.NoError(t, upstreamCtx.Err(), "客户端断开不应取消已计费的生图上游请求")
	require.Equal(t, "image-value", upstreamCtx.Value(upstreamContextTestKey("image-key")))

	retryCtx, releaseRetry := detachOpenAIImageUpstreamContext(upstreamCtx)
	defer releaseRetry()
	retryDeadline, ok := retryCtx.Deadline()
	require.True(t, ok)
	require.Equal(t, deadline, retryDeadline, "恢复重试必须沿用原始硬超时")
}

func TestDetachOpenAIImageUpstreamContextHardTimeoutExpires(t *testing.T) {
	upstreamCtx, release := detachOpenAIImageUpstreamContextWithTimeout(context.Background(), 20*time.Millisecond)
	defer release()

	select {
	case <-upstreamCtx.Done():
		require.ErrorIs(t, upstreamCtx.Err(), context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("生图上游 context 未在独立硬超时后结束")
	}
}
