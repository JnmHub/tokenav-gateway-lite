package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagesJSONKeepalive_PreservesValidJSONResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	originalWriter := c.Writer

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)
	require.Equal(t, -1, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))

	c.JSON(http.StatusOK, gin.H{"data": []gin.H{{"b64_json": "aW1hZ2U="}}})
	stop()

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	require.True(t, recorder.Flushed)
	require.True(t, json.Valid(recorder.Body.Bytes()), recorder.Body.String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
	require.Greater(t, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c), 0)
	require.Same(t, originalWriter, c.Writer)
}

func TestOpenAIImagesJSONKeepalive_ReverseProxyIdleTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		// 保持 upstreamDelay 为 proxyIdle 的三倍，同时给全包测试期间的 GC
		// 和调度停顿留出一秒余量。门槛过短会把宿主机瞬时资源争抢误判为
		// keepalive（保活）失效，而生产反向代理本来也使用秒级 idle timeout。
		upstreamDelay = 3 * time.Second
		proxyIdle     = time.Second
		heartbeat     = 50 * time.Millisecond
	)

	t.Run("without heartbeat proxy times out", func(t *testing.T) {
		status, _, _, _ := runOpenAIImagesJSONKeepaliveProxyCase(t, 0, proxyIdle, upstreamDelay)
		require.Equal(t, http.StatusGatewayTimeout, status)
	})

	t.Run("heartbeat keeps slow image JSON alive", func(t *testing.T) {
		status, header, body, elapsed := runOpenAIImagesJSONKeepaliveProxyCase(t, heartbeat, proxyIdle, upstreamDelay)
		require.Equal(t, http.StatusOK, status)
		require.GreaterOrEqual(t, elapsed, upstreamDelay)
		require.Equal(t, "application/json; charset=utf-8", header.Get("Content-Type"))
		require.Equal(t, "no", header.Get("X-Accel-Buffering"))
		require.True(t, json.Valid(body), string(body))
		require.Equal(t, "aW1hZ2U=", gjson.GetBytes(body, "data.0.b64_json").String())

		jsonStart := bytes.IndexByte(body, '{')
		require.Greater(t, jsonStart, 0, string(body))
		require.GreaterOrEqual(t, strings.Count(string(body[:jsonStart]), " \n"), 2)
	})
}

func runOpenAIImagesJSONKeepaliveProxyCase(
	t *testing.T,
	heartbeatInterval time.Duration,
	proxyIdleTimeout time.Duration,
	upstreamDelay time.Duration,
) (int, http.Header, []byte, time.Duration) {
	t.Helper()

	// 无心跳场景必须等代理真实超时后才释放上游，避免机器调度停顿让
	// time.Sleep 与 socket deadline 的先后顺序发生随机翻转。
	releaseBlockedUpstream := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(releaseBlockedUpstream) }) }
	defer releaseUpstream()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if heartbeatInterval <= 0 {
			<-releaseBlockedUpstream
		} else {
			time.Sleep(upstreamDelay)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1710000000,"data":[{"b64_json":"aW1hZ2U="}]}`)
	}))
	defer upstream.Close()

	upstreamClient := &http.Client{Timeout: 5 * time.Second}
	router := gin.New()
	router.POST("/v1/images/generations", func(c *gin.Context) {
		stop := StartOpenAIImagesJSONKeepalive(c, heartbeatInterval)
		defer stop()

		resp, err := upstreamClient.Get(upstream.URL)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
			return
		}
		c.Data(resp.StatusCode, "application/json; charset=utf-8", body)
	})

	gateway := httptest.NewServer(router)
	defer gateway.Close()
	target, err := url.Parse(gateway.URL)
	require.NoError(t, err)

	dialer := &net.Dialer{Timeout: time.Second}
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, dialErr := dialer.DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, dialErr
			}
			return &openAIImagesIdleReadConn{Conn: conn, timeout: proxyIdleTimeout}, nil
		},
	}
	defer transport.CloseIdleConnections()

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "upstream idle timeout", http.StatusGatewayTimeout)
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	request, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/images/generations", strings.NewReader(`{"model":"gpt-image-2","prompt":"draw a cat"}`))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")

	started := time.Now()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	releaseUpstream()
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, response.Header.Clone(), body, time.Since(started)
}

type openAIImagesIdleReadConn struct {
	net.Conn
	timeout time.Duration
}

func (c *openAIImagesIdleReadConn) Read(p []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func TestOpenAIImagesJSONKeepalive_DisabledIsNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	originalWriter := c.Writer

	stop := StartOpenAIImagesJSONKeepalive(c, 0)
	stop()
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "invalid request"}})

	require.Same(t, originalWriter, c.Writer)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, "invalid request", gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestOpenAIImagesJSONKeepalive_FastErrorPreservesStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, time.Second)
	wrote := writeOpenAIImagesUpstreamErrorResponse(c, &OpenAIImagesUpstreamError{
		StatusCode: http.StatusBadRequest,
		ErrorType:  "invalid_request_error",
		Message:    "invalid size",
	})
	stop()

	require.True(t, wrote)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.False(t, strings.HasPrefix(recorder.Body.String(), " \n"))
	require.Equal(t, "invalid size", gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestOpenAIImagesJSONKeepalive_LateErrorRemainsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	waitForOpenAIImagesJSONKeepalive(t, c)

	wrote := writeOpenAIImagesUpstreamErrorResponse(c, &OpenAIImagesUpstreamError{
		StatusCode: http.StatusBadRequest,
		ErrorType:  "image_generation_user_error",
		Code:       "moderation_blocked",
		Message:    "request rejected",
	})

	require.True(t, wrote)
	require.Equal(t, http.StatusOK, recorder.Code, "heartbeat already committed the status")
	require.True(t, json.Valid(recorder.Body.Bytes()), recorder.Body.String())
	require.Equal(t, "moderation_blocked", gjson.Get(recorder.Body.String(), "error.code").String())
	require.Equal(t, "request rejected", gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestOpenAIImagesJSONKeepalive_DoesNotBlockFailoverDetection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)

	before := OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c)
	require.Equal(t, -1, before)
	require.True(t, c.Writer.Written())
	require.Equal(t, before, OpenAIImagesJSONKeepaliveAdjustedWrittenSize(c))
	stop()
	require.Empty(t, strings.TrimSpace(recorder.Body.String()))
}

func TestOpenAIImagesJSONKeepalive_KeepsOAuthNonStreamResponseValid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)

	reader, writer := io.Pipe()
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(writer,
			"data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\",\"output_format\":\"png\"}]}}\n\n"+
				"data: [DONE]\n\n",
		)
		_ = writer.Close()
	}()

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	waitForOpenAIImagesJSONKeepalive(t, c)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}
	svc := &OpenAIGatewayService{}
	_, imageCount, _, err := svc.handleOpenAIImagesOAuthNonStreamingResponse(resp, c, "b64_json", "gpt-image-2")
	stop()

	require.NoError(t, err)
	require.Equal(t, 1, imageCount)
	require.True(t, recorder.Flushed)
	require.True(t, strings.HasPrefix(recorder.Body.String(), " \n"), recorder.Body.String())
	require.True(t, json.Valid(recorder.Body.Bytes()), recorder.Body.String())
	require.Equal(t, "aW1hZ2U=", gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
}

func TestOpenAIImagesJSONKeepalive_CoversAccountAndEndpointMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name        string
		accountType string
		endpoint    string
	}{
		{name: "api key generation", accountType: AccountTypeAPIKey, endpoint: "/v1/images/generations"},
		{name: "api key edit", accountType: AccountTypeAPIKey, endpoint: "/v1/images/edits"},
		{name: "oauth generation", accountType: AccountTypeOAuth, endpoint: "/v1/images/generations"},
		{name: "oauth edit", accountType: AccountTypeOAuth, endpoint: "/v1/images/edits"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, contentType := openAIImagesKeepaliveTestRequest(t, tt.endpoint)
			req := httptest.NewRequest(http.MethodPost, tt.endpoint, bytes.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = req
			c.Set("api_key", &APIKey{ID: 9001})

			upstreamBody := `{"created":1710000000,"data":[{"b64_json":"aW1hZ2U="}]}`
			upstreamContentType := "application/json"
			if tt.accountType == AccountTypeOAuth {
				upstreamContentType = "text/event-stream"
				upstreamBody = "data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1710000000,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aW1hZ2U=\",\"output_format\":\"png\"}]}}\n\n" +
					"data: [DONE]\n\n"
			}
			reader, writer := io.Pipe()
			go func() {
				time.Sleep(20 * time.Millisecond)
				_, _ = io.WriteString(writer, upstreamBody)
				_ = writer.Close()
			}()

			svc := &OpenAIGatewayService{
				cfg: &config.Config{},
				httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{upstreamContentType}},
					Body:       reader,
				}},
			}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)

			account := &Account{
				ID:       99,
				Name:     tt.name,
				Platform: PlatformOpenAI,
				Type:     tt.accountType,
				Credentials: map[string]any{
					"api_key":      "test-api-key",
					"access_token": "test-access-token",
					"base_url":     "https://image-upstream.example/v1",
				},
			}

			stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
			waitForOpenAIImagesJSONKeepalive(t, c)
			result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			stop()

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, result.ImageCount)
			require.True(t, strings.HasPrefix(recorder.Body.String(), " \n"), recorder.Body.String())
			require.True(t, json.Valid(recorder.Body.Bytes()), recorder.Body.String())
			require.Equal(t, "aW1hZ2U=", gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
		})
	}
}

func openAIImagesKeepaliveTestRequest(t *testing.T, endpoint string) ([]byte, string) {
	t.Helper()
	if strings.HasSuffix(endpoint, "/edits") {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		require.NoError(t, writer.WriteField("model", "gpt-image-2"))
		require.NoError(t, writer.WriteField("prompt", "replace background"))
		part, err := writer.CreateFormFile("image", "source.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("png-image-content"))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		return body.Bytes(), writer.FormDataContentType()
	}
	return []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`), "application/json"
}

func TestOpenAIImagesJSONKeepaliveWriter_NilGuards(t *testing.T) {
	w := &openAIImagesJSONKeepaliveWriter{}
	require.NotPanics(t, func() {
		require.NotNil(t, w.Header())
		_, _ = w.Write([]byte("test"))
		_, _ = w.WriteString("test")
		w.WriteHeader(http.StatusOK)
		w.WriteHeaderNow()
		w.Flush()
		require.Equal(t, 0, w.Status())
		require.Equal(t, 0, w.Size())
		require.False(t, w.Written())
		require.Nil(t, w.Pusher())
	})

	conn, _, err := w.Hijack()
	require.Error(t, err)
	require.Nil(t, conn)
	select {
	case <-w.CloseNotify():
	default:
		t.Fatal("nil writer CloseNotify channel should be closed")
	}
}

// Heartbeat bytes from an earlier attempt must not make a retryable upstream
// error look like a business response that already reached the client.
func TestOpenAIImagesJSONKeepalive_HeartbeatBeforeForwardStillFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat","response_format":"b64_json"}`)

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = req

	svc := &OpenAIGatewayService{
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"text/event-stream"},
					"X-Request-Id": []string{"req_img_heartbeat_failover"},
				},
				Body: io.NopCloser(strings.NewReader(
					"data: {\"type\":\"response.created\",\"response\":{\"created_at\":1710000021}}\n\n" +
						"data: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\",\"message\":\"The image service is temporarily unavailable.\"}}\n\n",
				)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	stop := StartOpenAIImagesJSONKeepalive(c, 5*time.Millisecond)
	defer stop()
	waitForOpenAIImagesJSONKeepalive(t, c)

	account := &Account{
		ID:       22,
		Name:     "openai-oauth-heartbeat-failover",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "token-123",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body, parsed, "")

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Contains(t, string(failoverErr.ResponseBody), "temporarily unavailable")
	require.Empty(t, strings.TrimSpace(recorder.Body.String()), "only heartbeat whitespace may reach the client")

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "failover", events[0].Kind)
	require.Equal(t, account.ID, events[0].AccountID)
	require.Equal(t, http.StatusBadGateway, events[0].UpstreamStatusCode)
}

func waitForOpenAIImagesJSONKeepalive(t *testing.T, c *gin.Context) {
	t.Helper()
	k := openAIImagesJSONKeepaliveFromContext(c)
	require.NotNil(t, k)
	require.Eventually(t, func() bool {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.started
	}, time.Second, time.Millisecond)
}
