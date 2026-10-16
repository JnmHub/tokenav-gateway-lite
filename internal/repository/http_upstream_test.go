package repository

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/orderedhttp"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type grokRoundTripFunc func(*http.Request) (*http.Response, error)

func (f grokRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDoWithUpstreamDialRetry_ReplaysOnlyDialFailure(t *testing.T) {
	payload := []byte(`{"model":"gpt-5-mini","input":"hello"}`)
	var calls int
	client := &http.Client{Transport: grokRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		}
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, payload, body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    req,
		}, nil
	})}
	req, err := http.NewRequest(http.MethodPost, "http://upstream.example/v1/responses", bytes.NewReader(payload))
	require.NoError(t, err)

	resp, err := doWithUpstreamDialRetry(client, req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, calls)
	require.NoError(t, resp.Body.Close())
}

func TestDoWithUpstreamDialRetry_DoesNotReplayAmbiguousFailure(t *testing.T) {
	var calls int
	client := &http.Client{Transport: grokRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("connection reset after write")
	})}
	req, err := http.NewRequest(http.MethodPost, "http://upstream.example/v1/responses", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)

	_, err = doWithUpstreamDialRetry(client, req)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestDoWithUpstreamDialRetry_RequiresReplayableBody(t *testing.T) {
	var calls int
	client := &http.Client{Transport: grokRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})}
	req, err := http.NewRequest(http.MethodPost, "http://upstream.example/v1/responses", io.NopCloser(strings.NewReader(`{}`)))
	require.NoError(t, err)
	require.Nil(t, req.GetBody)

	_, err = doWithUpstreamDialRetry(client, req)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestDoWithUpstreamDialRetry_StopsAfterBoundedAttempts(t *testing.T) {
	var calls int
	client := &http.Client{Transport: grokRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	})}
	req, err := http.NewRequest(http.MethodPost, "http://upstream.example/v1/responses", bytes.NewReader([]byte(`{}`)))
	require.NoError(t, err)

	_, err = doWithUpstreamDialRetry(client, req)
	require.Error(t, err)
	require.Equal(t, upstreamDialMaxRetries+1, calls)
}

func TestGrokCLIAccessDeniedFallbackToOfficialAPI(t *testing.T) {
	payload := []byte(`{"model":"grok-4.5","input":"hello"}`)
	var calls int
	transport := &grokAccessDeniedFallbackTransport{base: grokRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		requestBody, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, payload, requestBody)
		if calls == 1 {
			require.Equal(t, grokCLIProxyHost, req.URL.Hostname())
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"Access denied"}`)),
				Request:    req,
			}, nil
		}
		require.Equal(t, grokOfficialAPIHost, req.URL.Hostname())
		require.Equal(t, "/v1/responses", req.URL.Path)
		require.Equal(t, "Bearer oauth-token", req.Header.Get("Authorization"))
		require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
		require.Empty(t, req.Header.Get("User-Agent"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"response-ok"}`)),
			Request:    req,
		}, nil
	})}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer oauth-token")
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	req.Header.Set("User-Agent", xai.CLIUserAgent(xai.CLIClientVersion))

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, calls)
}

func TestGrokCLIStructuredPermissionDeniedFallbackToOfficialAPI(t *testing.T) {
	payload := []byte(`{"model":"grok-4.5","input":"hello"}`)
	var calls int
	transport := &grokAccessDeniedFallbackTransport{base: grokRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":"permission_denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials. If you believe this is a mistake, please contact support."}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"response-ok"}`)),
			Request:    req,
		}, nil
	})}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer oauth-token")
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, calls)
}

func TestHTTPClientForUpstreamRequestCanDisableRedirectsPerRequest(t *testing.T) {
	base := &http.Client{}
	plainReq, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	require.Nil(t, httpClientForUpstreamRequest(base, plainReq).CheckRedirect)

	lockedReq := plainReq.WithContext(service.WithHTTPUpstreamRedirectsDisabled(context.Background()))
	lockedClient := httpClientForUpstreamRequest(base, lockedReq)
	require.NotSame(t, base, lockedClient)
	require.ErrorIs(t, lockedClient.CheckRedirect(plainReq, nil), http.ErrUseLastResponse)
	require.Nil(t, base.CheckRedirect, "the shared client must remain unchanged")
}

func TestHTTPClientForUpstreamRequestPinsValidatedAddressAndPreservesHost(t *testing.T) {
	var receivedHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		receivedHost = req.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	requestURL := "http://billing.rebind.invalid:" + upstreamURL.Port() + "/v1/sub2api/billing"
	ctx := service.WithHTTPUpstreamPinnedTarget(context.Background(), "billing.rebind.invalid", []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	require.NoError(t, err)

	base := &http.Client{Transport: &http.Transport{}}
	client := httpClientForUpstreamRequest(base, req)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "billing.rebind.invalid", resp.Request.URL.Hostname())
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "billing.rebind.invalid:"+upstreamURL.Port(), receivedHost)
}

func TestHTTPClientForUpstreamRequestPinsHTTPSAddressThroughHTTPProxy(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)

	connectTarget := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, http.MethodConnect, req.Method)
		connectTarget <- req.Host
		upstreamConn, dialErr := net.Dial("tcp", req.Host)
		require.NoError(t, dialErr)
		hijacker, ok := w.(http.Hijacker)
		require.True(t, ok)
		clientConn, _, hijackErr := hijacker.Hijack()
		require.NoError(t, hijackErr)
		_, writeErr := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		require.NoError(t, writeErr)
		go func() {
			_, _ = io.Copy(upstreamConn, clientConn)
			_ = upstreamConn.Close()
		}()
		go func() {
			_, _ = io.Copy(clientConn, upstreamConn)
			_ = clientConn.Close()
		}()
	}))
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	require.NoError(t, err)
	transport := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // test server certificate only
	}
	ctx := service.WithHTTPUpstreamPinnedTarget(context.Background(), "billing.rebind.invalid", []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://billing.rebind.invalid:"+upstreamURL.Port()+"/v1/sub2api/billing", nil)
	require.NoError(t, err)

	resp, err := httpClientForUpstreamRequest(&http.Client{Transport: transport}, req).Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "127.0.0.1:"+upstreamURL.Port(), <-connectTarget)
}

func TestHTTPClientForUpstreamRequestPreservesTLSServerName(t *testing.T) {
	serverName := make(chan string, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	upstream.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			serverName <- hello.ServerName
			return nil, nil
		},
	}
	upstream.StartTLS()
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	ctx := service.WithHTTPUpstreamPinnedTarget(context.Background(), "billing.rebind.invalid", []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://billing.rebind.invalid:"+upstreamURL.Port()+"/v1/sub2api/billing", nil)
	require.NoError(t, err)
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // test server certificate only

	resp, err := httpClientForUpstreamRequest(&http.Client{Transport: transport}, req).Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, "billing.rebind.invalid", <-serverName)
}

func TestHTTPClientForUpstreamRequestFailsClosedForHostMismatchAndHTTPSProxy(t *testing.T) {
	ctx := service.WithHTTPUpstreamPinnedTarget(context.Background(), "expected.invalid", []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://changed.invalid/v1/sub2api/billing", nil)
	require.NoError(t, err)
	_, err = httpClientForUpstreamRequest(&http.Client{Transport: &http.Transport{}}, req).Do(req)
	require.ErrorIs(t, err, errPinnedTargetHostMismatch)

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://expected.invalid/v1/sub2api/billing", nil)
	require.NoError(t, err)
	httpsProxy, err := url.Parse("https://proxy.invalid:8443")
	require.NoError(t, err)
	_, err = httpClientForUpstreamRequest(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(httpsProxy)}}, req).Do(req)
	require.ErrorIs(t, err, errPinnedTargetHTTPSProxyUnsupported)

	plainHTTPProxy, err := url.Parse("http://proxy.invalid:8080")
	require.NoError(t, err)
	plainReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://expected.invalid/v1/sub2api/billing", nil)
	require.NoError(t, err)
	_, err = httpClientForUpstreamRequest(&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(plainHTTPProxy)}}, plainReq).Do(plainReq)
	require.ErrorIs(t, err, errPinnedTargetForwardProxyUnsupported)
}

func TestGrokCLIAccessDeniedFallbackRejectsUnsafeCandidates(t *testing.T) {
	tests := []struct {
		name string
		url  string
		body io.Reader
	}{
		{name: "media endpoint", url: "https://cli-chat-proxy.grok.com/v1/images/generations", body: strings.NewReader(`{}`)},
		{name: "non replayable body", url: "https://cli-chat-proxy.grok.com/v1/responses", body: io.NopCloser(strings.NewReader(`{}`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			transport := &grokAccessDeniedFallbackTransport{base: grokRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"error":"Access denied"}`)),
					Request:    req,
				}, nil
			})}
			req, err := http.NewRequest(http.MethodPost, tt.url, tt.body)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer oauth-token")
			req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
			resp, err := transport.RoundTrip(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			require.Equal(t, 1, calls)
		})
	}
}

func TestGrokCLIAccessDeniedFallbackFailureReturnsOriginal403(t *testing.T) {
	payload := []byte(`{"model":"grok-4.5","input":"hello"}`)
	var calls int
	transport := &grokAccessDeniedFallbackTransport{base: grokRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Header:     http.Header{"X-Original": []string{"cli"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"Access denied","source":"cli"}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"official unavailable"}`)),
			Request:    req,
		}, nil
	})}

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer oauth-token")
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "cli", resp.Header.Get("X-Original"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"error":"Access denied","source":"cli"}`, string(body))
}

func TestApplyGrokCLIProxyHeaders(t *testing.T) {
	t.Run("uses pinned stable version for cli proxy", func(t *testing.T) {
		t.Setenv(grokCLIVersionOverride, "")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "sub2api-grok/1.0")

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
		require.Equal(t, "xai-grok-cli", req.Header.Get("X-XAI-Token-Auth"))
		require.Equal(t, "xai-grok-workspace/"+xai.CLIClientVersion, req.Header.Get("User-Agent"))
	})

	t.Run("accepts newer canonical version", func(t *testing.T) {
		t.Setenv(grokCLIVersionOverride, "0.2.121-alpha.1")
		req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/chat/completions", nil)
		require.NoError(t, err)

		applyGrokCLIProxyHeaders(req)

		require.Equal(t, "0.2.121-alpha.1", req.Header.Get("x-grok-client-version"))
	})

	for _, version := range []string{"0.2.119", "0.2.120-beta.1", "01.2.121", "0.2.121\r\nX-Injected:true"} {
		t.Run("rejects unsafe or unsupported override "+version, func(t *testing.T) {
			t.Setenv(grokCLIVersionOverride, version)
			req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
			require.NoError(t, err)

			applyGrokCLIProxyHeaders(req)

			require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
			require.Empty(t, req.Header.Get("X-Injected"))
		})
	}

	t.Run("does not modify direct xai api traffic", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
		require.NoError(t, err)
		req.Header.Set("User-Agent", "custom-agent")

		applyGrokCLIProxyHeaders(req)

		require.Empty(t, req.Header.Get("x-grok-client-version"))
		require.Equal(t, "custom-agent", req.Header.Get("User-Agent"))
	})
}

// HTTPUpstreamSuite HTTP 上游服务测试套件
// 使用 testify/suite 组织测试，支持 SetupTest 初始化
type HTTPUpstreamSuite struct {
	suite.Suite
	cfg *config.Config // 测试用配置
}

// SetupTest 每个测试用例执行前的初始化
// 创建空配置，各测试用例可按需覆盖
func (s *HTTPUpstreamSuite) SetupTest() {
	s.cfg = &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				AllowPrivateHosts: true,
			},
		},
	}
}

// newService 创建测试用的 httpUpstreamService 实例
// 返回具体类型以便访问内部状态进行断言
func (s *HTTPUpstreamSuite) newService() *httpUpstreamService {
	up := NewHTTPUpstream(s.cfg)
	svc, ok := up.(*httpUpstreamService)
	require.True(s.T(), ok, "expected *httpUpstreamService")
	return svc
}

// TestDefaultResponseHeaderTimeout 测试默认响应头超时配置
// 验证显式 0 会禁用等待响应头超时
func (s *HTTPUpstreamSuite) TestDefaultResponseHeaderTimeout() {
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestNilConfigResponseHeaderTimeoutFallback 验证 nil 配置使用代码级兜底值。
func (s *HTTPUpstreamSuite) TestNilConfigResponseHeaderTimeoutFallback() {
	up := NewHTTPUpstream(nil)
	svc, ok := up.(*httpUpstreamService)
	require.True(s.T(), ok, "expected *httpUpstreamService")
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 300*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestCustomResponseHeaderTimeout 测试自定义响应头超时配置
// 验证配置值能正确应用到 Transport
func (s *HTTPUpstreamSuite) TestCustomResponseHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 7}
	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 0, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 7*time.Second, transport.ResponseHeaderTimeout, "ResponseHeaderTimeout mismatch")
}

// TestGetOrCreateClient_InvalidURLReturnsError 测试无效代理 URL 返回错误
// 验证解析失败时拒绝回退到直连模式
func (s *HTTPUpstreamSuite) TestGetOrCreateClient_InvalidURLReturnsError() {
	svc := s.newService()
	_, err := svc.getClientEntry("://bad-proxy-url", 1, 1, service.HTTPUpstreamProfileDefault, false, false)
	require.Error(s.T(), err, "expected error for invalid proxy URL")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileDefaultsToHTTP2AndNoHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout: 600,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), time.Duration(0), transport.ResponseHeaderTimeout, "OpenAI profile should not inherit generic header timeout")
	require.True(s.T(), transport.ForceAttemptHTTP2, "OpenAI profile should prefer HTTP/2")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH2, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileCustomHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout:       600,
		OpenAIResponseHeaderTimeout: 1800,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 1800*time.Second, transport.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileTLSFingerprintDoesNotInheritGenericHeaderTimeout() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout: 600,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: true,
		},
	}
	svc := s.newService()
	entry, err := svc.getClientEntryWithTLS("", 1, 1, &tlsfingerprint.Profile{Name: "test"}, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	// OpenAI TLS 指纹路径使用固定头序写出器（L1.3），内部回退到常规 Transport。
	ordered, ok := entry.client.Transport.(*orderedhttp.Transport)
	require.True(s.T(), ok, "expected *orderedhttp.Transport")
	fallback, ok := ordered.Unwrap().(*http.Transport)
	require.True(s.T(), ok, "expected fallback *http.Transport")
	require.Equal(s.T(), time.Duration(0), fallback.ResponseHeaderTimeout, "OpenAI TLS path should not inherit generic header timeout")
}

func (s *HTTPUpstreamSuite) TestOpenAIProfileHTTP2DisabledUsesHTTP1Transport() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: false},
	}
	svc := s.newService()
	entry, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2, "OpenAI HTTP/2 disabled should not force H2")
	require.NotNil(s.T(), transport.TLSNextProto, "HTTP/1 mode should disable automatic H2 negotiation")
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestOpenAIHeaderTimeoutChangeRebuildsClient() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{Enabled: true},
	}
	svc := s.newService()
	entry1, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)

	s.cfg.Gateway.OpenAIResponseHeaderTimeout = 1800
	entry2, err := svc.getClientEntry("", 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	require.NotSame(s.T(), entry1, entry2, "OpenAI header timeout changes must rebuild cached client")
	transport, ok := entry2.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 1800*time.Second, transport.ResponseHeaderTimeout)
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2TimeoutDoesNotActivateProxyFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, errors.New("http2: timeout awaiting response headers"))
	require.False(s.T(), svc.isOpenAIHTTP2FallbackActive(proxyURL), "header timeout should not be treated as H2 compatibility failure")
}

func (s *HTTPUpstreamSuite) TestOpenAIHTTP2ProxyCompatibilityErrorActivatesFallback() {
	s.cfg.Gateway = config.GatewayConfig{
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled:                   true,
			AllowProxyFallbackToHTTP1: true,
			FallbackErrorThreshold:    1,
			FallbackWindowSeconds:     60,
			FallbackTTLSeconds:        600,
		},
	}
	svc := s.newService()
	proxyURL := "http://proxy.local:8080"
	svc.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, upstreamProtocolModeOpenAIH2, proxyURL, errors.New("http2: protocol error"))
	require.True(s.T(), svc.isOpenAIHTTP2FallbackActive(proxyURL))

	entry, err := svc.getClientEntry(proxyURL, 1, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), err)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.False(s.T(), transport.ForceAttemptHTTP2)
	require.NotNil(s.T(), transport.TLSNextProto)
	require.Equal(s.T(), upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode)
}

func (s *HTTPUpstreamSuite) TestDo_ResponseHeaderTimeoutWithFakeUpstream() {
	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 1}
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "late")
	}))
	s.T().Cleanup(upstream.Close)

	svc := s.newService()
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/slow", nil)
	require.NoError(s.T(), err)

	start := time.Now()
	resp, err := svc.Do(req, "", 101, 1)
	elapsed := time.Since(start)

	require.Error(s.T(), err)
	require.Nil(s.T(), resp)
	require.True(s.T(), isUpstreamTimeoutError(err), "expected timeout error, got %v", err)
	require.Less(s.T(), elapsed, 1900*time.Millisecond, "request should fail before fake upstream writes response headers")

	entry := mustGetOrCreateClient(s.T(), svc, "", 101, 1)
	require.Equal(s.T(), int64(0), atomic.LoadInt64(&entry.inFlight), "failed request should release in-flight counter")
}

func (s *HTTPUpstreamSuite) TestDo_OpenAIProfileResponseHeaderTimeoutWithFakeUpstream() {
	s.cfg.Gateway = config.GatewayConfig{
		ResponseHeaderTimeout:       600,
		OpenAIResponseHeaderTimeout: 1,
		OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
			Enabled: false,
		},
	}
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "late-openai")
	}))
	s.T().Cleanup(upstream.Close)

	svc := s.newService()
	ctx := service.WithHTTPUpstreamProfile(context.Background(), service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/v1/responses", nil)
	require.NoError(s.T(), err)

	start := time.Now()
	resp, err := svc.Do(req, "", 202, 1)
	elapsed := time.Since(start)

	require.Error(s.T(), err)
	require.Nil(s.T(), resp)
	require.True(s.T(), isUpstreamTimeoutError(err), "expected OpenAI timeout error, got %v", err)
	require.Less(s.T(), elapsed, 1900*time.Millisecond, "OpenAI request should honor openai_response_header_timeout")

	entry, entryErr := svc.getClientEntry("", 202, 1, service.HTTPUpstreamProfileOpenAI, false, false)
	require.NoError(s.T(), entryErr)
	require.Equal(s.T(), int64(0), atomic.LoadInt64(&entry.inFlight), "failed OpenAI request should release in-flight counter")
}

func (s *HTTPUpstreamSuite) TestDo_ClientCancellationReleasesInFlightAndUpstreamRequest() {
	requestStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = io.WriteString(w, "ok")
			return
		}
		close(requestStarted)
		<-r.Context().Done()
		close(upstreamCanceled)
	}))
	s.T().Cleanup(upstream.Close)

	svc := s.newService()
	entry := mustGetOrCreateClient(s.T(), svc, "", 303, 1)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL+"/cancel", nil)
	require.NoError(s.T(), err)

	requestErr := make(chan error, 1)
	go func() {
		resp, doErr := svc.Do(req, "", 303, 1)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		requestErr <- doErr
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		s.T().Fatal("fake upstream did not receive request")
	}
	require.Eventually(s.T(), func() bool {
		return atomic.LoadInt64(&entry.inFlight) == 1
	}, time.Second, 10*time.Millisecond, "request should occupy one upstream slot before cancellation")

	cancel()
	select {
	case err := <-requestErr:
		require.ErrorIs(s.T(), err, context.Canceled)
	case <-time.After(2 * time.Second):
		s.T().Fatal("canceled request did not return")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		s.T().Fatal("upstream request context was not canceled")
	}
	require.Eventually(s.T(), func() bool {
		return atomic.LoadInt64(&entry.inFlight) == 0
	}, time.Second, 10*time.Millisecond, "client cancellation should release upstream in-flight slot")

	// 同一账号随后仍可正常请求，证明取消没有把连接池留在占用状态。
	nextReq, err := http.NewRequest(http.MethodGet, upstream.URL+"/ok", nil)
	require.NoError(s.T(), err)
	nextResp, err := svc.Do(nextReq, "", 303, 1)
	require.NoError(s.T(), err)
	_, err = io.ReadAll(nextResp.Body)
	require.NoError(s.T(), err)
	require.NoError(s.T(), nextResp.Body.Close())
	require.Equal(s.T(), int64(0), atomic.LoadInt64(&entry.inFlight))
}

// TestNormalizeProxyURL_Canonicalizes 测试代理 URL 规范化
// 验证等价地址能够映射到同一缓存键
func (s *HTTPUpstreamSuite) TestNormalizeProxyURL_Canonicalizes() {
	key1, _, err1 := normalizeProxyURL("http://proxy.local:8080")
	require.NoError(s.T(), err1)
	key2, _, err2 := normalizeProxyURL("http://proxy.local:8080/")
	require.NoError(s.T(), err2)
	require.Equal(s.T(), key1, key2, "expected normalized proxy keys to match")
}

// TestAcquireClient_OverLimitReturnsError 测试连接池缓存上限保护
// 验证超限且无可淘汰条目时返回错误
func (s *HTTPUpstreamSuite) TestAcquireClient_OverLimitReturnsError() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
		MaxUpstreamClients:      1,
	}
	svc := s.newService()
	entry1, err := svc.acquireClient("http://proxy-a:8080", 1, 1)
	require.NoError(s.T(), err, "expected first acquire to succeed")
	require.NotNil(s.T(), entry1, "expected entry")

	entry2, err := svc.acquireClient("http://proxy-b:8080", 2, 1)
	require.Error(s.T(), err, "expected error when cache limit reached")
	require.Nil(s.T(), entry2, "expected nil entry when cache limit reached")
}

// TestDo_WithoutProxy_GoesDirect 测试无代理时直连
// 验证空代理 URL 时请求直接发送到目标服务器
func (s *HTTPUpstreamSuite) TestDo_WithoutProxy_GoesDirect() {
	// 创建模拟上游服务器
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct")
	}))
	s.T().Cleanup(upstream.Close)

	up := NewHTTPUpstream(s.cfg)

	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/x", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct", string(b), "unexpected body")
}

// TestDo_WithHTTPProxy_UsesProxy 测试 HTTP 代理功能
// 验证请求通过代理服务器转发，使用绝对 URI 格式
func (s *HTTPUpstreamSuite) TestDo_WithHTTPProxy_UsesProxy() {
	// 用于接收代理请求的通道
	seen := make(chan string, 1)
	// 创建模拟代理服务器
	proxySrv := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.RequestURI // 记录请求 URI
		_, _ = io.WriteString(w, "proxied")
	}))
	s.T().Cleanup(proxySrv.Close)

	s.cfg.Gateway = config.GatewayConfig{ResponseHeaderTimeout: 1}
	up := NewHTTPUpstream(s.cfg)

	// 发送请求到外部地址，应通过代理
	req, err := http.NewRequest(http.MethodGet, "http://example.com/test", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, proxySrv.URL, 1, 1)
	require.NoError(s.T(), err, "Do")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "proxied", string(b), "unexpected body")

	// 验证代理收到的是绝对 URI 格式（HTTP 代理规范要求）
	select {
	case uri := <-seen:
		require.Equal(s.T(), "http://example.com/test", uri, "expected absolute-form request URI")
	default:
		require.Fail(s.T(), "expected proxy to receive request")
	}
}

// TestDo_EmptyProxy_UsesDirect 测试空代理字符串
// 验证空字符串代理等同于直连
func (s *HTTPUpstreamSuite) TestDo_EmptyProxy_UsesDirect() {
	upstream := newLocalTestServer(s.T(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "direct-empty")
	}))
	s.T().Cleanup(upstream.Close)

	up := NewHTTPUpstream(s.cfg)
	req, err := http.NewRequest(http.MethodGet, upstream.URL+"/y", nil)
	require.NoError(s.T(), err, "NewRequest")
	resp, err := up.Do(req, "", 1, 1)
	require.NoError(s.T(), err, "Do with empty proxy")
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(s.T(), "direct-empty", string(b))
}

// TestAccountIsolation_DifferentAccounts 测试账户隔离模式
// 验证不同账户使用独立的连接池
func (s *HTTPUpstreamSuite) TestAccountIsolation_DifferentAccounts() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 同一代理，不同账户
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy.local:8080", 2, 3)
	require.NotSame(s.T(), entry1, entry2, "不同账号不应共享连接池")
	require.Equal(s.T(), 2, len(svc.clients), "账号隔离应缓存两个客户端")
}

// TestAccountProxyIsolation_DifferentProxy 测试账户+代理组合隔离模式
// 验证同一账户使用不同代理时创建独立连接池
func (s *HTTPUpstreamSuite) TestAccountProxyIsolation_DifferentProxy() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy}
	svc := s.newService()
	// 同一账户，不同代理
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1, entry2, "账号+代理隔离应区分不同代理")
	require.Equal(s.T(), 2, len(svc.clients), "账号+代理隔离应缓存两个客户端")
}

// TestAccountModeProxyChangeClearsPool 测试账户模式下代理变更
// 验证账户切换代理时清理旧连接池，避免复用错误代理
func (s *HTTPUpstreamSuite) TestAccountModeProxyChangeClearsPool() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 同一账户，先后使用不同代理
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 3)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 1, 3)
	require.NotSame(s.T(), entry1, entry2, "账号切换代理应创建新连接池")
	require.Equal(s.T(), 1, len(svc.clients), "账号模式下应仅保留一个连接池")
	require.False(s.T(), hasEntry(svc, entry1), "旧连接池应被清理")
}

// TestAccountConcurrencyOverridesPoolSettings 测试账户并发数覆盖连接池配置
// 验证账户隔离模式下，连接池大小与账户并发数对应
func (s *HTTPUpstreamSuite) TestAccountConcurrencyOverridesPoolSettings() {
	s.cfg.Gateway = config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount}
	svc := s.newService()
	// 账户并发数为 12
	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 12)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	// 连接池参数应与并发数一致
	require.Equal(s.T(), 12, transport.MaxConnsPerHost, "MaxConnsPerHost mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConns, "MaxIdleConns mismatch")
	require.Equal(s.T(), 12, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost mismatch")
}

// TestAccountConcurrencyFallbackToDefault 测试账户并发数为 0 时回退到默认配置
// 验证未指定并发数时使用全局配置值
func (s *HTTPUpstreamSuite) TestAccountConcurrencyFallbackToDefault() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
		MaxIdleConns:            77,
		MaxIdleConnsPerHost:     55,
		MaxConnsPerHost:         66,
	}
	svc := s.newService()
	// 账户并发数为 0，应使用全局配置
	entry := mustGetOrCreateClient(s.T(), svc, "", 1, 0)
	transport, ok := entry.client.Transport.(*http.Transport)
	require.True(s.T(), ok, "expected *http.Transport")
	require.Equal(s.T(), 66, transport.MaxConnsPerHost, "MaxConnsPerHost fallback mismatch")
	require.Equal(s.T(), 77, transport.MaxIdleConns, "MaxIdleConns fallback mismatch")
	require.Equal(s.T(), 55, transport.MaxIdleConnsPerHost, "MaxIdleConnsPerHost fallback mismatch")
}

// TestEvictOverLimitRemovesOldestIdle 测试超出数量限制时的 LRU 淘汰
// 验证优先淘汰最久未使用的空闲客户端
func (s *HTTPUpstreamSuite) TestEvictOverLimitRemovesOldestIdle() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
		MaxUpstreamClients:      2, // 最多缓存 2 个客户端
	}
	svc := s.newService()
	// 创建两个客户端，设置不同的最后使用时间
	entry1 := mustGetOrCreateClient(s.T(), svc, "http://proxy-a:8080", 1, 1)
	entry2 := mustGetOrCreateClient(s.T(), svc, "http://proxy-b:8080", 2, 1)
	atomic.StoreInt64(&entry1.lastUsed, time.Now().Add(-2*time.Hour).UnixNano()) // 最久
	atomic.StoreInt64(&entry2.lastUsed, time.Now().Add(-time.Hour).UnixNano())
	// 创建第三个客户端，触发淘汰
	_ = mustGetOrCreateClient(s.T(), svc, "http://proxy-c:8080", 3, 1)

	require.LessOrEqual(s.T(), len(svc.clients), 2, "应保持在缓存上限内")
	require.False(s.T(), hasEntry(svc, entry1), "最久未使用的连接池应被清理")
}

// TestIdleTTLDoesNotEvictActive 测试活跃请求保护
// 验证有进行中请求的客户端不会被空闲超时淘汰
func (s *HTTPUpstreamSuite) TestIdleTTLDoesNotEvictActive() {
	s.cfg.Gateway = config.GatewayConfig{
		ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
		ClientIdleTTLSeconds:    1, // 1 秒空闲超时
	}
	svc := s.newService()
	entry1 := mustGetOrCreateClient(s.T(), svc, "", 1, 1)
	// 设置为很久之前使用，但有活跃请求
	atomic.StoreInt64(&entry1.lastUsed, time.Now().Add(-2*time.Minute).UnixNano())
	atomic.StoreInt64(&entry1.inFlight, 1) // 模拟有活跃请求
	// 创建新客户端，触发淘汰检查
	_, _ = svc.getOrCreateClient("", 2, 1)

	require.True(s.T(), hasEntry(svc, entry1), "有活跃请求时不应回收")
}

// TestHTTPUpstreamSuite 运行测试套件
func TestHTTPUpstreamSuite(t *testing.T) {
	suite.Run(t, new(HTTPUpstreamSuite))
}

// mustGetOrCreateClient 测试辅助函数，调用 getOrCreateClient 并断言无错误
func mustGetOrCreateClient(t *testing.T, svc *httpUpstreamService, proxyURL string, accountID int64, concurrency int) *upstreamClientEntry {
	t.Helper()
	entry, err := svc.getOrCreateClient(proxyURL, accountID, concurrency)
	require.NoError(t, err, "getOrCreateClient(%q, %d, %d)", proxyURL, accountID, concurrency)
	return entry
}

// hasEntry 检查客户端是否存在于缓存中
// 辅助函数，用于验证淘汰逻辑
func hasEntry(svc *httpUpstreamService, target *upstreamClientEntry) bool {
	for _, entry := range svc.clients {
		if entry == target {
			return true
		}
	}
	return false
}
