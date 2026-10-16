package repository

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func http2KeepAliveTestPoolSettings() poolSettings {
	return poolSettings{
		maxIdleConns:          10,
		maxIdleConnsPerHost:   5,
		maxConnsPerHost:       10,
		idleConnTimeout:       90 * time.Second,
		responseHeaderTimeout: time.Minute,
	}
}

// requireHTTP2Enabled 断言 transport 已启用 HTTP/2。
//
// 注意不能用 transport.TLSNextProto["h2"] != nil 判断：x/net 在 go1.27 的
// transport_wrap 实现里改为通过 http.Transport.Protocols 启用 h2
// （并在 RegisterProtocol("http/2", …) 注册），不再写 TLSNextProto；
// 本机 toolchain 解析为 go1.27，因此 TLSNextProto 恒为 nil，旧断言已过期。
// Protocols 是公开字段，用它判断才与 net/http 的实际启用口径一致。
func requireHTTP2Enabled(t *testing.T, transport *http.Transport) {
	t.Helper()
	require.NotNil(t, transport.Protocols, "期望 h2 通过 transport.Protocols 启用")
	require.True(t, transport.Protocols.HTTP2(), "HTTP/2 未启用")
	require.NotNil(t, transport.TLSClientConfig, "启用 h2 时 TLS 配置不应为空")
}

func TestEnableOpenAIHTTP2KeepAlive_EnablesPingHealthCheck(t *testing.T) {
	transport := &http.Transport{}

	h2, err := enableOpenAIHTTP2KeepAlive(transport)
	require.NoError(t, err)
	require.NotNil(t, h2)
	require.Equal(t, openAIHTTP2ReadIdleTimeout, h2.ReadIdleTimeout)
	require.Equal(t, openAIHTTP2PingTimeout, h2.PingTimeout)
	requireHTTP2Enabled(t, transport)
}

func TestBuildUpstreamTransport_OpenAIH2_EnablesPingHealthCheck(t *testing.T) {
	transport, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	require.True(t, transport.ForceAttemptHTTP2)
	requireHTTP2Enabled(t, transport)
}

func TestBuildUpstreamTransport_NonOpenAIH2_NotEagerlyConfigured(t *testing.T) {
	transport, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeDefault)
	require.NoError(t, err)
	// 非 OpenAI H2 模式不得预先启用 h2：要么没设 Protocols，要么其中不含 HTTP/2。
	if transport.Protocols != nil {
		require.False(t, transport.Protocols.HTTP2(), "默认模式不应预先启用 HTTP/2")
	}
	require.Nil(t, transport.TLSNextProto["h2"])
}

func TestBuildUpstreamTransport_OpenAIH2_WithHTTPProxy_EnablesKeepAlive(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:8080")
	require.NoError(t, err)

	transport, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), proxyURL, upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	require.True(t, transport.ForceAttemptHTTP2)
	requireHTTP2Enabled(t, transport)
	require.NotNil(t, transport.Proxy)
}

// TestBuildUpstreamTransport_OpenAIH2_SpeaksHTTP2AndReusesConnection 是上面几条断言的
// 行为版护栏：不再盯实现细节（TLSNextProto / Protocols），而是用真实请求证明
// 「生产入口构造出的 transport 确实走 HTTP/2，且连接可复用」。
func TestBuildUpstreamTransport_OpenAIH2_SpeaksHTTP2AndReusesConnection(t *testing.T) {
	var connectionCount atomic.Int32
	server := newHTTP2BehaviorServer(t, &connectionCount, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))

	transport, err := buildUpstreamTransport(http2KeepAliveTestPoolSettings(), nil, upstreamProtocolModeOpenAIH2)
	require.NoError(t, err)
	t.Cleanup(transport.CloseIdleConnections)
	// 仅测试：信任 httptest 自签证书。
	require.NotNil(t, transport.TLSClientConfig)
	transport.TLSClientConfig.InsecureSkipVerify = true

	client := &http.Client{Transport: transport}
	requireHTTPResponse(t, client, server.URL, 2, "ok")
	requireHTTPResponse(t, client, server.URL, 2, "ok")
	require.Equal(t, int32(1), connectionCount.Load(), "两次请求应复用同一条 HTTP/2 连接")
}

func TestOpenAIHTTP2KeepAlive_HealthyIdlePingKeepsConnectionReusable(t *testing.T) {
	const (
		readIdleTimeout = 25 * time.Millisecond
		pingTimeout     = 50 * time.Millisecond
	)

	var connectionCount atomic.Int32
	server := newHTTP2BehaviorServer(t, &connectionCount, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))

	transport := newHTTP2BehaviorTransport(t, readIdleTimeout, pingTimeout)
	client := &http.Client{Transport: transport}

	requireHTTPResponse(t, client, server.URL, 2, "ok")
	require.Equal(t, int32(1), connectionCount.Load())

	// Wait beyond both health-check deadlines. A healthy server ACKs the PING,
	// so the second request must reuse the original HTTP/2 connection.
	time.Sleep(readIdleTimeout + pingTimeout + 75*time.Millisecond)
	requireHTTPResponse(t, client, server.URL, 2, "ok")
	require.Equal(t, int32(1), connectionCount.Load())
}

func TestOpenAIHTTP2KeepAlive_UnresponsivePingClosesConnectionAndRebuilds(t *testing.T) {
	const (
		readIdleTimeout = 25 * time.Millisecond
		pingTimeout     = 50 * time.Millisecond
	)

	var upstreamConnectionCount atomic.Int32
	server := newHTTP2BehaviorServer(t, &upstreamConnectionCount, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	proxy := newPingAckDroppingTCPProxy(t, strings.TrimPrefix(server.URL, "https://"))

	transport := newHTTP2BehaviorTransport(t, readIdleTimeout, pingTimeout)
	client := &http.Client{Transport: transport}
	proxyURL := "https://" + proxy.address()

	requireHTTPResponse(t, client, proxyURL, 2, "ok")
	require.Equal(t, int32(1), proxy.acceptedConnections())
	select {
	case <-proxy.firstConnectionClosed():
		t.Fatal("first HTTP/2 connection closed before the PING ACK was dropped")
	default:
	}
	proxy.dropFirstServerWrites()

	select {
	case <-proxy.firstConnectionClosed():
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/2 transport did not close the connection after the PING ACK was dropped")
	}

	requireHTTPResponse(t, client, proxyURL, 2, "ok")
	require.Equal(t, int32(2), proxy.acceptedConnections())
	require.Equal(t, int32(2), upstreamConnectionCount.Load())
}

func TestOpenAIHTTP2ProxyFallback_UsesHTTP1ThroughLocalProxy(t *testing.T) {
	var upstreamProtocol atomic.Int32
	upstream := newHTTP2BehaviorServer(t, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamProtocol.Store(int32(r.ProtoMajor))
		_, _ = io.WriteString(w, "proxied")
	}))

	for _, tc := range []struct {
		name     string
		tlsProxy bool
	}{
		{name: "HTTP proxy", tlsProxy: false},
		{name: "HTTPS proxy", tlsProxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxyRequests := &atomic.Int32{}
			proxy := newConnectProxyServer(t, tc.tlsProxy, proxyRequests)
			cfg := &config.Config{Gateway: config.GatewayConfig{
				ConnectionPoolIsolation: config.ConnectionPoolIsolationAccountProxy,
				OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
					Enabled:                   true,
					AllowProxyFallbackToHTTP1: true,
					FallbackErrorThreshold:    1,
					FallbackWindowSeconds:     60,
					FallbackTTLSeconds:        60,
				},
			}}
			svc := &httpUpstreamService{cfg: cfg, clients: make(map[string]*upstreamClientEntry)}

			// The compatibility classifier is covered separately. Here the local proxy
			// verifies that an activated fallback performs a real CONNECT request and
			// negotiates HTTP/1.1 even though the upstream also supports HTTP/2.
			svc.recordOpenAIHTTP2Failure(
				service.HTTPUpstreamProfileOpenAI,
				upstreamProtocolModeOpenAIH2,
				proxy.URL,
				errors.New("http2: protocol error"),
			)
			require.True(t, svc.isOpenAIHTTP2FallbackActive(proxy.URL))

			entry, err := svc.getClientEntry(proxy.URL, 101, 1, service.HTTPUpstreamProfileOpenAI, false, false)
			require.NoError(t, err)
			require.Equal(t, upstreamProtocolModeOpenAIH1Fallback, entry.protocolMode)
			transport, ok := entry.client.Transport.(*http.Transport)
			require.True(t, ok)
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test-only local certificates

			ctx := service.WithHTTPUpstreamProfile(t.Context(), service.HTTPUpstreamProfileOpenAI)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
			require.NoError(t, err)
			resp, err := svc.Do(req, proxy.URL, 101, 1)
			require.NoError(t, err)
			body, readErr := io.ReadAll(resp.Body)
			require.NoError(t, readErr)
			require.NoError(t, resp.Body.Close())
			require.Equal(t, 1, resp.ProtoMajor)
			require.Equal(t, "proxied", string(body))
			require.Equal(t, int32(1), upstreamProtocol.Load())
			require.Equal(t, int32(1), proxyRequests.Load())
		})
	}
}

func newHTTP2BehaviorServer(t *testing.T, connectionCount *atomic.Int32, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = true
	if connectionCount != nil {
		server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				connectionCount.Add(1)
			}
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func newHTTP2BehaviorTransport(t *testing.T, readIdleTimeout, pingTimeout time.Duration) *http.Transport {
	t.Helper()
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		MaxConnsPerHost:       2,
		IdleConnTimeout:       5 * time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true}, // test-only local certificates
		ResponseHeaderTimeout: time.Second,
	}
	h2, err := enableOpenAIHTTP2KeepAlive(transport)
	require.NoError(t, err)
	h2.ReadIdleTimeout = readIdleTimeout
	h2.PingTimeout = pingTimeout
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func requireHTTPResponse(t *testing.T, client *http.Client, rawURL string, expectedProtoMajor int, expectedBody string) {
	t.Helper()
	resp, err := client.Get(rawURL)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, expectedProtoMajor, resp.ProtoMajor)
	require.Equal(t, expectedBody, string(body))
}

type pingAckDroppingTCPProxy struct {
	listener                  net.Listener
	upstreamAddress           string
	dropFirst                 atomic.Bool
	accepted                  atomic.Int32
	firstClosed               chan struct{}
	firstClosedOnce           sync.Once
	activeConnectionsMu       sync.Mutex
	activeConnections         map[net.Conn]struct{}
	activeUpstreamConnections map[net.Conn]struct{}
	wg                        sync.WaitGroup
}

func newPingAckDroppingTCPProxy(t *testing.T, upstreamAddress string) *pingAckDroppingTCPProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	proxy := &pingAckDroppingTCPProxy{
		listener:                  listener,
		upstreamAddress:           upstreamAddress,
		firstClosed:               make(chan struct{}),
		activeConnections:         make(map[net.Conn]struct{}),
		activeUpstreamConnections: make(map[net.Conn]struct{}),
	}
	proxy.wg.Add(1)
	go proxy.serve()
	t.Cleanup(proxy.close)
	return proxy
}

func (p *pingAckDroppingTCPProxy) serve() {
	defer p.wg.Done()
	for {
		clientConn, err := p.listener.Accept()
		if err != nil {
			return
		}
		connectionID := p.accepted.Add(1)
		p.wg.Add(1)
		go p.proxyConnection(clientConn, connectionID)
	}
}

func (p *pingAckDroppingTCPProxy) proxyConnection(clientConn net.Conn, connectionID int32) {
	defer p.wg.Done()
	upstreamConn, err := net.DialTimeout("tcp", p.upstreamAddress, time.Second)
	if err != nil {
		_ = clientConn.Close()
		return
	}
	p.trackConnection(clientConn, upstreamConn, true)
	defer func() {
		p.trackConnection(clientConn, upstreamConn, false)
		_ = clientConn.Close()
		_ = upstreamConn.Close()
		if connectionID == 1 {
			p.firstClosedOnce.Do(func() { close(p.firstClosed) })
		}
	}()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstreamConn, clientConn)
		done <- struct{}{}
	}()
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := upstreamConn.Read(buffer)
			if n > 0 && !(connectionID == 1 && p.dropFirst.Load()) {
				if _, writeErr := clientConn.Write(buffer[:n]); writeErr != nil {
					break
				}
			}
			if readErr != nil {
				break
			}
		}
		done <- struct{}{}
	}()
	<-done
}

func (p *pingAckDroppingTCPProxy) trackConnection(clientConn, upstreamConn net.Conn, add bool) {
	p.activeConnectionsMu.Lock()
	defer p.activeConnectionsMu.Unlock()
	if add {
		p.activeConnections[clientConn] = struct{}{}
		p.activeUpstreamConnections[upstreamConn] = struct{}{}
		return
	}
	delete(p.activeConnections, clientConn)
	delete(p.activeUpstreamConnections, upstreamConn)
}

func (p *pingAckDroppingTCPProxy) address() string                        { return p.listener.Addr().String() }
func (p *pingAckDroppingTCPProxy) acceptedConnections() int32             { return p.accepted.Load() }
func (p *pingAckDroppingTCPProxy) dropFirstServerWrites()                 { p.dropFirst.Store(true) }
func (p *pingAckDroppingTCPProxy) firstConnectionClosed() <-chan struct{} { return p.firstClosed }

func (p *pingAckDroppingTCPProxy) close() {
	_ = p.listener.Close()
	p.activeConnectionsMu.Lock()
	for conn := range p.activeConnections {
		_ = conn.Close()
	}
	for conn := range p.activeUpstreamConnections {
		_ = conn.Close()
	}
	p.activeConnectionsMu.Unlock()
	p.wg.Wait()
}

func newConnectProxyServer(t *testing.T, tlsProxy bool, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		upstreamConn, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "upstream dial failed", http.StatusBadGateway)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			_ = upstreamConn.Close()
			http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
			return
		}
		clientConn, _, err := hijacker.Hijack()
		if err != nil {
			_ = upstreamConn.Close()
			return
		}
		requests.Add(1)
		if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			return
		}
		go func() {
			defer func() {
				_ = clientConn.Close()
				_ = upstreamConn.Close()
			}()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(upstreamConn, clientConn); done <- struct{}{} }()
			go func() { _, _ = io.Copy(clientConn, upstreamConn); done <- struct{}{} }()
			<-done
		}()
	})
	server := httptest.NewUnstartedServer(handler)
	if tlsProxy {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server
}
