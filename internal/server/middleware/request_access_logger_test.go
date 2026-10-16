package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type testLogSink struct {
	mu     sync.Mutex
	events []*logger.LogEvent
}

func (s *testLogSink) WriteLogEvent(event *logger.LogEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *testLogSink) list() []*logger.LogEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*logger.LogEvent, len(s.events))
	copy(out, s.events)
	return out
}

func initMiddlewareTestLogger(t *testing.T) *testLogSink {
	return initMiddlewareTestLoggerWithLevel(t, "debug")
}

func initMiddlewareTestLoggerWithLevel(t *testing.T, level string) *testLogSink {
	t.Helper()
	level = strings.TrimSpace(level)
	if level == "" {
		level = "debug"
	}
	if err := logger.Init(logger.InitOptions{
		Level:       level,
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: logger.OutputOptions{
			ToStdout: false,
			ToFile:   false,
		},
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}
	sink := &testLogSink{}
	logger.SetSink(sink)
	t.Cleanup(func() {
		logger.SetSink(nil)
	})
	return sink
}

func TestRequestLogger_GenerateAndPropagateRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/t", func(c *gin.Context) {
		reqID, ok := c.Request.Context().Value(ctxkey.RequestID).(string)
		if !ok || reqID == "" {
			t.Fatalf("request_id missing in context")
		}
		if got := c.Writer.Header().Get(requestIDHeader); got != reqID {
			t.Fatalf("response header request_id mismatch, header=%q ctx=%q", got, reqID)
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if w.Header().Get(requestIDHeader) == "" {
		t.Fatalf("X-Request-ID should be set")
	}
}

func TestRequestLogger_PreservesClientTraceAndGeneratesServerID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/t", func(c *gin.Context) {
		reqID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
		if reqID == "" || reqID == "rid-fixed" {
			t.Fatalf("server request_id must be generated independently: %q", reqID)
		}
		clientID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)
		if clientID != "rid-fixed" {
			t.Fatalf("client tracing ID=%q", clientID)
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/t", nil)
	req.Header.Set(requestIDHeader, "rid-fixed")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	if got := w.Header().Get(requestIDHeader); got == "" || got == "rid-fixed" {
		t.Fatalf("server header must not reuse client ID: %q", got)
	}
}

func TestLogger_AccessLogIncludesCoreFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)
	successfulModelAccessLogCounter.Store(0)

	r := gin.New()
	r.Use(Logger())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, ctxkey.AccountID, int64(101))
		ctx = context.WithValue(ctx, ctxkey.Platform, "openai")
		ctx = context.WithValue(ctx, ctxkey.Model, "gpt-5")
		ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "effective-client-id")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/api/test", func(c *gin.Context) {
		c.Status(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"model":"gpt-5"}`))
	req.Host = "gateway.example.test"
	req.Header.Set("User-Agent", "CherryStudio/1.0")
	req.Header.Set("Referer", "https://app.example.test/chat")
	req.Header.Set(requestIDHeader, "rid-header")
	req.Header.Set(clientRequestIDHeader, "client-header")
	req.Header.Set("Authorization", "Bearer should-not-be-logged")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d", w.Code)
	}

	events := sink.list()
	if len(events) == 0 {
		t.Fatalf("expected at least one log event")
	}
	found := false
	for _, event := range events {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		found = true
		switch v := event.Fields["status_code"].(type) {
		case int:
			if v != http.StatusCreated {
				t.Fatalf("status_code field mismatch: %v", v)
			}
		case int64:
			if v != int64(http.StatusCreated) {
				t.Fatalf("status_code field mismatch: %v", v)
			}
		default:
			t.Fatalf("status_code type mismatch: %T", v)
		}
		switch v := event.Fields["account_id"].(type) {
		case int64:
			if v != 101 {
				t.Fatalf("account_id field mismatch: %v", v)
			}
		case int:
			if v != 101 {
				t.Fatalf("account_id field mismatch: %v", v)
			}
		default:
			t.Fatalf("account_id type mismatch: %T", v)
		}
		if event.Fields["platform"] != "openai" || event.Fields["model"] != "gpt-5" {
			t.Fatalf("platform/model mismatch: %+v", event.Fields)
		}
		if event.Fields["user_agent"] != "CherryStudio/1.0" {
			t.Fatalf("user_agent mismatch: %+v", event.Fields)
		}
		if event.Fields["host"] != "gateway.example.test" || event.Fields["referer"] != "https://app.example.test/chat" {
			t.Fatalf("host/referer mismatch: %+v", event.Fields)
		}
		if event.Fields["x_request_id_header"] != "rid-header" || event.Fields["x_client_request_id_header"] != "client-header" {
			t.Fatalf("request id headers mismatch: %+v", event.Fields)
		}
		if event.Fields["effective_client_request_id"] != "effective-client-id" {
			t.Fatalf("effective_client_request_id mismatch: %+v", event.Fields)
		}
		switch v := event.Fields["content_length"].(type) {
		case int64:
			if v <= 0 {
				t.Fatalf("content_length should be positive: %+v", event.Fields)
			}
		case int:
			if v <= 0 {
				t.Fatalf("content_length should be positive: %+v", event.Fields)
			}
		default:
			t.Fatalf("content_length type mismatch: %T", v)
		}
		if _, exists := event.Fields["authorization"]; exists {
			t.Fatalf("authorization must not be logged: %+v", event.Fields)
		}
	}
	if !found {
		t.Fatalf("access log event not found")
	}
}

func TestLogger_SamplesSuccessfulModelRequestsButKeepsFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)
	successfulModelAccessLogCounter.Store(0)

	r := gin.New()
	r.Use(Logger())
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), ctxkey.Platform, "openai")
		ctx = context.WithValue(ctx, ctxkey.Model, "gpt-5")
		c.Request = c.Request.WithContext(ctx)
	})
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/failed", func(c *gin.Context) { c.Status(http.StatusBadGateway) })

	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ok", nil))
		require.Equal(t, http.StatusOK, w.Code)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/failed", nil))
	require.Equal(t, http.StatusBadGateway, w.Code)

	completed := 0
	failedFound := false
	for _, event := range sink.list() {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		completed++
		switch status := event.Fields["status_code"].(type) {
		case int:
			failedFound = failedFound || status == http.StatusBadGateway
		case int64:
			failedFound = failedFound || status == int64(http.StatusBadGateway)
		}
	}
	require.Equal(t, 2, completed, "100 个成功模型请求保留 1 条样本，失败请求完整保留")
	require.True(t, failedFound)
}

func TestLogger_PerfDiagnosticsReportsAccessLogTrailer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initMiddlewareTestLogger(t)

	r := gin.New()
	r.Use(Logger())
	r.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Gateway-Perf-Diagnostics", "1")
	r.ServeHTTP(w, req)

	response := w.Result()
	startedAt, err := strconv.ParseInt(response.Header.Get("X-Gateway-Handler-Started-At-Unix-Nano"), 10, 64)
	if err != nil || startedAt <= 0 {
		t.Fatalf("handler start diagnostic header missing or invalid: %q (%v)", response.Header.Get("X-Gateway-Handler-Started-At-Unix-Nano"), err)
	}
	wroteAt, err := strconv.ParseInt(response.Header.Get("X-Gateway-Response-Write-At-Unix-Nano"), 10, 64)
	if err != nil || wroteAt < startedAt {
		t.Fatalf("response write diagnostic header missing or invalid: %q (%v)", response.Header.Get("X-Gateway-Response-Write-At-Unix-Nano"), err)
	}
	if _, err := strconv.ParseFloat(response.Trailer.Get("X-Gateway-Access-Log-Ms"), 64); err != nil {
		t.Fatalf("access log latency trailer missing or invalid: %q (%v)", response.Trailer.Get("X-Gateway-Access-Log-Ms"), err)
	}
}

func TestLogger_AccessLogUsesSecurityClientIPFromTrustedProxyChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)
	cfg := &config.Config{}
	cfg.SetTrustForwardedIPForAPIKeyACL(true)

	r := gin.New()
	if err := r.SetTrustedProxies([]string{"104.23.251.120"}); err != nil {
		t.Fatal(err)
	}
	r.Use(Logger(cfg))
	r.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "104.23.251.120:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.42")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	// v0.2.5 对齐后：trust 开启 = 转发头优先（CF-Connecting-IP > X-Real-IP >
	// X-Forwarded-For）。本用例保留 XFF 可信代理链场景：来自可信代理的请求，
	// 访问日志应记录链路中的真实客户端地址而非代理地址。
	for _, event := range sink.list() {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		if got := event.Fields["client_ip"]; got != "203.0.113.42" {
			t.Fatalf("client_ip=%q, want real forwarded ip", got)
		}
		return
	}
	t.Fatalf("access log event not found")
}

func TestLogger_AccessLogIgnoresForwardedHeadersWhenTrustDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLogger(t)

	r := gin.New()
	// v0.2.5 对齐后：trust 关闭 = gin server.trusted_proxies 链口径。引擎未配置
	// 任何可信代理时，转发头一律忽略，client_ip 取直连对端地址（RemoteAddr）。
	if err := r.SetTrustedProxies(nil); err != nil {
		t.Fatal(err)
	}
	r.Use(Logger(&config.Config{}))
	r.GET("/api/test", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "198.51.100.7:443"
	req.Header.Set("CF-Connecting-IP", "203.0.113.42")
	req.Header.Set("X-Forwarded-For", "203.0.113.43")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}

	for _, event := range sink.list() {
		if event == nil || event.Message != "http request completed" {
			continue
		}
		if got := event.Fields["client_ip"]; got != "198.51.100.7" {
			t.Fatalf("client_ip=%q, want direct peer", got)
		}
		return
	}
	t.Fatalf("access log event not found")
}

func TestLogger_ProbePathsSkipped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			sink := initMiddlewareTestLogger(t)

			r := gin.New()
			r.Use(Logger())
			r.GET(path, func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d", w.Code)
			}
			if len(sink.list()) != 0 {
				t.Fatalf("probe endpoint %s should not write access log", path)
			}
		})
	}
}

func TestLogger_AccessLogDroppedWhenLevelWarn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sink := initMiddlewareTestLoggerWithLevel(t, "warn")

	r := gin.New()
	r.Use(RequestLogger())
	r.Use(Logger())
	r.GET("/api/test", func(c *gin.Context) {
		c.Status(http.StatusCreated)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d", w.Code)
	}

	events := sink.list()
	for _, event := range events {
		if event != nil && event.Message == "http request completed" {
			t.Fatalf("access log should not be indexed when level=warn: %+v", event)
		}
	}
}
