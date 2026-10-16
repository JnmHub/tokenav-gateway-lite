package orderedhttp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// teeConn 把从连接读到的字节镜像到内部缓冲，供捕获"原始请求字节"（验证写出顺序）。
type teeConn struct {
	net.Conn
	mu  sync.Mutex
	buf []byte
}

func (c *teeConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.buf = append(c.buf, p[:n]...)
		c.mu.Unlock()
	}
	return n, err
}

func (c *teeConn) take() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buf
	c.buf = nil
	return b
}

// rawCaptureServer 是最小 TLS HTTP/1.1 服务器：按请求记录原始字节，可配置
// 常规 JSON 响应 / SSE 流 / 挂起不响应。
type rawCaptureServer struct {
	ln        net.Listener
	addr      string
	mu        sync.Mutex
	rawReqs   [][]byte
	connCount atomic.Int64
	sse       bool
	hang      bool
	closed    chan struct{}
	closeOnce sync.Once
}

func newRawCaptureServer(t *testing.T) *rawCaptureServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &rawCaptureServer{ln: ln, addr: ln.Addr().String(), closed: make(chan struct{})}
	go s.serve()
	t.Cleanup(func() { s.closeOnce.Do(func() { _ = ln.Close(); close(s.closed) }) })
	return s
}

func (s *rawCaptureServer) serve() {
	cert := selfSignedCert()
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.connCount.Add(1)
		go s.handleConn(tls.Server(conn, tlsCfg))
	}
}

func (s *rawCaptureServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tc := &teeConn{Conn: conn}
	br := bufio.NewReader(tc)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		// 读尽请求体（Content-Length/chunked），确保 tee 捕获完整请求字节。
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
		s.mu.Lock()
		s.rawReqs = append(s.rawReqs, tc.take())
		s.mu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

		if s.hang {
			// 挂起：不写响应，直到连接被客户端关闭/超时。
			<-s.closed
			return
		}
		if s.sse {
			writeSSE(conn)
			continue
		}
		body := `{"ok":true}`
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n" + body))
	}
}

func (s *rawCaptureServer) requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.rawReqs))
	copy(out, s.rawReqs)
	return out
}

func (s *rawCaptureServer) lastRequestRaw() []byte {
	reqs := s.requests()
	if len(reqs) == 0 {
		return nil
	}
	return reqs[len(reqs)-1]
}

func selfSignedCert() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "orderedhttp-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"orderedhttp-test"},
	}
	der, _ := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, key.Public(), key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func writeSSE(conn net.Conn) {
	_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"))
	for i := 0; i < 2; i++ {
		payload := []byte("data: chunk-" + itoa(i) + "\n\n")
		// chunked 长度必须是十六进制。
		_, _ = conn.Write([]byte(fmt.Sprintf("%x\r\n", len(payload))))
		_, _ = conn.Write(payload)
		_, _ = conn.Write([]byte("\r\n"))
	}
	_, _ = conn.Write([]byte("0\r\n\r\n"))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func newTestTransport(t *testing.T, srvAddr string) (*Transport, *http.Client) {
	t.Helper()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{}
		raw, err := d.DialContext(ctx, "tcp", srvAddr)
		if err != nil {
			return nil, err
		}
		return tls.Client(raw, &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         "orderedhttp-test",
			NextProtos:         []string{"http/1.1"},
		}), nil
	}
	tr, err := New(Config{DialTLS: dial, Fallback: http.DefaultTransport.(*http.Transport).Clone()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr, &http.Client{Transport: tr}
}

// headerNamesInOrder 从原始请求字节解析头名出现顺序（只解析到空行，不含请求体）。
func headerNamesInOrder(raw []byte) []string {
	var names []string
	for i, line := range strings.Split(string(raw), "\r\n") {
		if i == 0 {
			continue
		}
		if line == "" {
			break
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		names = append(names, line[:idx])
	}
	return names
}

func TestHeaderOrderIsDeterministic(t *testing.T) {
	srv := newRawCaptureServer(t)
	_, client := newTestTransport(t, srv.addr)

	var first []byte
	for i := 0; i < 20; i++ {
		body := strings.NewReader(`{"prompt":"hi"}`)
		req, _ := http.NewRequest(http.MethodPost, "https://orderedhttp-test/v1/responses?x=1", body)
		req.Host = "orderedhttp-test"
		req.ContentLength = int64(body.Len())
		// 故意以固定 Set 顺序构造——Header map 的遍历序本来就随机，
		// 若写出器依赖 map 序，20 轮内必然出现字节差异。
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("User-Agent", "codex_cli_rs/0.146.0 (Ubuntu 22.4.0; x86_64) xterm-256color")
		req.Header.Set("Originator", "codex_cli_rs")
		req.Header.Set("Session-Id", "0199aaaa-0000-7abc-8def-001122334455")
		req.Header.Set("Thread-Id", "0199bbbb-0000-7abc-8def-001122334455")
		req.Header.Set("X-Client-Request-Id", "0199bbbb-0000-7abc-8def-001122334455")
		req.Header.Set("Openai-Beta", "responses=experimental")
		req.Header.Set("Chatgpt-Account-Id", "acct-1")
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("roundtrip %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		raw := srv.lastRequestRaw()
		if raw == nil {
			t.Fatalf("roundtrip %d: no raw capture", i)
		}
		if first == nil {
			first = raw
			continue
		}
		if string(first) != string(raw) {
			t.Fatalf("请求写出字节不稳定:\n--- 第一次 ---\n%s\n--- 第 %d 次 ---\n%s", first, i, raw)
		}
	}

	names := headerNamesInOrder(first)
	// 官方取证顺序（codex 0.154.0 真实流量）：x-codex-* → session/thread →
	// accept → content-type → authorization → originator → user-agent → host，
	// 兜底头在 Host 后，Content-Length 最后。序表内不存在的头（本请求未带
	// x-codex-* 与 accept）自动跳过。
	want := []string{
		"X-Client-Request-Id", "Session-Id", "Thread-Id",
		"Content-Type", "Authorization", "Originator", "User-Agent", "Host",
		"Accept-Encoding", "Chatgpt-Account-Id", "Openai-Beta",
		"Content-Length",
	}
	if len(names) != len(want) {
		t.Fatalf("头集合不符: got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("头顺序[%d] = %s, want %s (full: %v)", i, names[i], want[i], names)
		}
	}
}

func TestConnectionReuse(t *testing.T) {
	srv := newRawCaptureServer(t)
	_, client := newTestTransport(t, srv.addr)

	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://orderedhttp-test/health", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("req %d: %v", i, err)
		}
		// 读尽响应体后关闭：连接才能回池复用（未读尽视为脏连接直接关闭，
		// 与 net/http 连接池语义一致）。
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if got := srv.connCount.Load(); got != 1 {
		t.Fatalf("keep-alive 复用失败: 建立了 %d 条连接, want 1", got)
	}
}

func TestSSEStreaming(t *testing.T) {
	srv := newRawCaptureServer(t)
	srv.sse = true
	_, client := newTestTransport(t, srv.addr)

	req, _ := http.NewRequest(http.MethodPost, "https://orderedhttp-test/v1/responses", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read sse: %v", err)
	}
	if !strings.Contains(string(data), "chunk-0") || !strings.Contains(string(data), "chunk-1") {
		t.Fatalf("SSE 内容不完整: %q", data)
	}
}

func TestChunkedRequestBody(t *testing.T) {
	srv := newRawCaptureServer(t)
	_, client := newTestTransport(t, srv.addr)

	// ContentLength 未知（-1）触发 chunked 请求体。
	body := io.NopCloser(strings.NewReader(`{"stream":true}`))
	req, _ := http.NewRequest(http.MethodPost, "https://orderedhttp-test/v1/responses", body)
	req.ContentLength = -1
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("chunked: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	raw := srv.lastRequestRaw()
	if !strings.Contains(string(raw), "Transfer-Encoding: chunked") {
		t.Fatalf("未知长度请求体应走 chunked:\n%s", raw)
	}
}

func TestContextCancelAbortsPendingResponse(t *testing.T) {
	srv := newRawCaptureServer(t)
	srv.hang = true
	_, client := newTestTransport(t, srv.addr)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://orderedhttp-test/hang", nil)
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("挂起响应 + ctx 取消应返回错误")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消未中断挂起的 RoundTrip")
	}
}

func TestFallbackForPlainHTTP(t *testing.T) {
	// 明文 http 请求必须走 Fallback（不经过 DialTLS）。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fallback-ok"))
	}))
	defer ts.Close()

	tr, _ := newTestTransport(t, "127.0.0.1:1")
	client := &http.Client{Transport: tr}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "fallback-ok" {
		t.Fatalf("fallback 响应异常: %q", data)
	}
	if tr.Unwrap() == nil {
		t.Fatal("Unwrap 应返回 fallback transport")
	}
}
