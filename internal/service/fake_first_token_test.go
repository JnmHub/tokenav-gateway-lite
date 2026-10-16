package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newFakeFirstTokenSettingService 返回一个已缓存「开启伪造首字」配置的设置服务，
// 绕过 DB 直接命中 30s 进程内缓存。
func newFakeFirstTokenSettingService() *SettingService {
	svc := &SettingService{}
	svc.fakeFirstTokenCache.Store(&cachedFakeFirstToken{
		value:     FakeFirstTokenSettings{Enabled: true, MinSeconds: 0.05, MaxSeconds: 0.08},
		expiresAt: time.Now().Add(time.Minute).UnixNano(),
	})
	return svc
}

func TestFakeFirstTokenSettings_Validate(t *testing.T) {
	// min>max 自动交换；非法值回退默认。
	s := FakeFirstTokenSettings{Enabled: true, MinSeconds: 2, MaxSeconds: 1}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.MinSeconds != 1 || s.MaxSeconds != 2 {
		t.Fatalf("min/max 应交换: %+v", s)
	}
	s = FakeFirstTokenSettings{Enabled: true, MinSeconds: -1, MaxSeconds: 100}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.MinSeconds <= 0 || s.MaxSeconds > 30 {
		t.Fatalf("非法值应回退默认: %+v", s)
	}
}

func TestScheduleFakeFirstToken_RangeAndOff(t *testing.T) {
	// 关闭时返回 nil 通道。
	if ch, stop := ScheduleFakeFirstToken(FakeFirstTokenSettings{Enabled: false}); ch != nil {
		t.Fatal("关闭时应返回 nil 通道")
	} else {
		stop()
	}
	// 开启时延迟落在 [min, max] 内（放宽调度误差）。
	for i := 0; i < 20; i++ {
		ch, stop := ScheduleFakeFirstToken(FakeFirstTokenSettings{Enabled: true, MinSeconds: 0.01, MaxSeconds: 0.05})
		start := time.Now()
		<-ch
		elapsed := time.Since(start)
		stop()
		if elapsed < 8*time.Millisecond || elapsed > 2*time.Second {
			t.Fatalf("延迟越界: %v", elapsed)
		}
	}
}

func TestFakeFirstTokenComment_IsSSEComment(t *testing.T) {
	// 必须是 SSE 注释行（冒号开头，含结尾空行）：对 EventSource 与各 SDK 无害。
	if !strings.HasPrefix(fakeFirstTokenComment, ":") || !strings.HasSuffix(fakeFirstTokenComment, "\n\n") {
		t.Fatalf("伪造首字必须是 SSE 注释行: %q", fakeFirstTokenComment)
	}
}

// TestHandleStreamingResponse_FakeFirstTokenPreemptsStagedUpstream 覆盖
// OpenAI 首输出暂存（stageFirstOutput）路径：上游 600ms 内只发不产生可见内容的
// 前导事件，暂存逻辑会把它们扣在网关内部，客户端此时收不到任何字节。
// 伪造首字必须在这种场景下先出站，且记录 fakeFirstTokenMs。
func TestHandleStreamingResponse_FakeFirstTokenPreemptsStagedUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &OpenAIGatewayService{
		cfg:            &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		settingService: newFakeFirstTokenSettingService(),
	}

	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		// 前导事件：网关暂存，不下发，客户端此刻拿不到字节。
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fake\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_fake\"}}\n\n"))
		time.Sleep(600 * time.Millisecond)
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fake\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	response := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{}}

	result, err := service.handleStreamingResponse(
		c.Request.Context(), response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model",
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	body := recorder.Body.String()
	require.True(t, strings.HasPrefix(body, fakeFirstTokenComment), "伪造首字必须最先出站，实际开头: %q", firstN(body, 40))
	require.Contains(t, body, "response.created")
	require.Contains(t, body, "response.output_text.delta")

	require.NotNil(t, result.fakeFirstTokenMs, "必须记录伪造首字时刻")
	require.Less(t, *result.fakeFirstTokenMs, 400, "伪造首字应在随机延迟附近出站")
	require.NotNil(t, result.firstTokenMs, "真实首字时刻仍要记录")
	require.GreaterOrEqual(t, *result.firstTokenMs, 500, "真实首字应仍按上游节奏统计")
}

// TestStreamChatCompletionsAsResponses_FakeFirstToken 覆盖 chat→responses 转换
// 路径（上游只支持 /v1/chat/completions 时）：上游静默 600ms，伪造首字先出站。
func TestStreamChatCompletionsAsResponses_FakeFirstToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const upstreamModel = "gpt-5.6-luna"
	c, recorder := newFakeFirstTokenContext(t, upstreamModel)

	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		time.Sleep(600 * time.Millisecond)
		_, _ = writer.Write([]byte(`data: {"id":"chat-fake","object":"chat.completion.chunk","model":"` + upstreamModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}` + "\n\n"))
		_, _ = writer.Write([]byte(`data: {"id":"chat-fake","object":"chat.completion.chunk","model":"` + upstreamModel + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}
	defer resp.Body.Close()

	service := &OpenAIGatewayService{settingService: newFakeFirstTokenSettingService()}
	result, err := service.streamChatCompletionsAsResponses(
		c, resp, upstreamModel, nil, false, nil, upstreamModel, upstreamModel, nil, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	body := recorder.Body.String()
	require.True(t, strings.HasPrefix(body, fakeFirstTokenComment), "伪造首字必须最先出站，实际开头: %q", firstN(body, 40))
	require.Contains(t, body, "response.output_text.delta")

	require.NotNil(t, result.FakeFirstTokenMs, "必须记录伪造首字时刻")
	require.Less(t, *result.FakeFirstTokenMs, 400)
	require.NotNil(t, result.FirstTokenMs)
	require.GreaterOrEqual(t, *result.FirstTokenMs, 500)
}

// TestHandleStreamingResponse_HandlerCommittedMsReachesResult 验证 handler 层
// 抢先提交的时刻能透过 gin context 进入流式结果（最终落到用量日志）。
func TestHandleStreamingResponse_HandlerCommittedMsReachesResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}

	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		_, _ = writer.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ctx\"}}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
		_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ctx\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}()

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	// 模拟 handler 层已抢先提交伪造首字。
	StoreCommittedFakeFirstTokenMs(c, 137)
	response := &http.Response{StatusCode: http.StatusOK, Body: reader, Header: http.Header{}}

	result, err := service.handleStreamingResponse(
		c.Request.Context(), response, c, &Account{ID: 1, Platform: PlatformOpenAI}, time.Now(), "model", "model",
	)
	require.NoError(t, err)
	require.NotNil(t, result.fakeFirstTokenMs, "handler 抢先提交的时刻必须进入结果")
	require.Equal(t, 137, *result.fakeFirstTokenMs)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// TestArmFakeFirstToken_CommitsBeforeUpstream 覆盖 handler 层「请求上游之前先
// 武装」的路径：上游十几秒不回响应头时，客户端应在随机延迟附近先收到注释行。
func TestArmFakeFirstToken_CommitsBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := FakeFirstTokenSettings{Enabled: true, MinSeconds: 0.03, MaxSeconds: 0.05}

	// 用例一：到点前没有任何真实写出 → 必须补发注释行并记录时刻。
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop := ArmFakeFirstToken(c, cfg, time.Now())
	defer stop()
	time.Sleep(250 * time.Millisecond)
	body := recorder.Body.String()
	require.True(t, strings.HasPrefix(body, fakeFirstTokenComment), "应抢先写出注释行，实际: %q", firstN(body, 40))
	require.NotEmpty(t, recorder.Header().Get("Content-Type"))
	ms := CommittedFakeFirstTokenMs(c)
	require.NotNil(t, ms, "必须记录伪造首字时刻")
	require.Less(t, *ms, 300)

	// 用例二：服务层已开始写真实字节（Claim）后，定时器不得再补发。
	recorder2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(recorder2)
	c2.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop2 := ArmFakeFirstToken(c2, cfg, time.Now())
	defer stop2()
	ClaimFakeFirstTokenOutput(c2)
	time.Sleep(250 * time.Millisecond)
	require.Empty(t, recorder2.Body.String(), "已接管真实输出后不得写入注释行")
	require.Nil(t, CommittedFakeFirstTokenMs(c2))

	// 用例三：请求结束前 stop（gin 会回收 writer）→ 不得写入。
	recorder3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(recorder3)
	c3.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stop3 := ArmFakeFirstToken(c3, cfg, time.Now())
	stop3()
	time.Sleep(250 * time.Millisecond)
	require.Empty(t, recorder3.Body.String(), "stop 之后不得写入")
}

// newFakeFirstTokenContext 构造与 HTTP handler 一致的响应模型上下文，
// 让 streamChatCompletionsAsResponses 能正常推导对外模型名。
func newFakeFirstTokenContext(t *testing.T, requestedModel string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	group := &Group{ID: 78, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	ctx = WithClientRequestedResponseModel(ctx, requestedModel)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	return c, recorder
}
