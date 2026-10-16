// Package orderedhttp 提供固定请求头写出顺序的 HTTP/1.1 RoundTripper。
//
// 动机（L1.3 反识别）：Go net/http 写请求头时按 map 遍历序输出，同一客户端
// 每次请求的头顺序都不同；官方客户端（reqwest/hyper）按 HeaderMap 插入序
// 固定输出。"同一客户端每次语序都打乱"本身就是可被上游建模的机器特征。
// 该包绕过 net/http 的请求序列化，自己按固定顺序写请求行、请求头与请求体，
// 响应解析仍复用标准库 http.ReadResponse（含 chunked/SSE 流式）。
//
// 请求头默认顺序依据官方 codex-rs（reqwest + hyper）源码重建：
//   - default_client.rs：originator、user-agent（默认头最先插入 HeaderMap）
//   - codex-api/src/requests/headers.rs：session-id、thread-id
//   - core/src/client.rs：x-client-request-id、x-codex-* 等请求级头
//   - 最后是 authorization（bearer_auth）、content-type（.json()）
//   - Host 由 hyper h1 客户端固定写在最前，Content-Length 由传输层写在最后
// 未列入固定序的其余头按字母序追加，保证任意输入都有确定的输出顺序。
package orderedhttp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultCodexHeaderOrder 是官方 codex CLI 请求头写出顺序。
// 数值来自 2026-09-13 对官方 codex-cli 0.154.0（Ubuntu x86_64）真实流量的
// 取证抓包（tmp/capcap/req1.raw，HTTP/1.1 原始字节），线缆顺序为：
//
//	x-codex-beta-features, x-codex-window-id, x-codex-turn-metadata,
//	x-client-request-id, session-id, thread-id, accept, content-type,
//	authorization, originator, user-agent, host, content-length
//
// Host 在 user-agent 之后（hyper 把 reqwest 追加的 host 写在头部区末尾）；
// Content-Length 固定最后。列表内的头请求里不存在时直接跳过。
var DefaultCodexHeaderOrder = []string{
	"X-Codex-Beta-Features",
	"X-Codex-Window-Id",
	"X-Codex-Turn-Metadata",
	"X-Client-Request-Id",
	"Session-Id",
	"Thread-Id",
	"Accept",
	"Content-Type",
	"Authorization",
	"Originator",
	"User-Agent",
	"Host",
}

// Config 配置固定头序 Transport。
type Config struct {
	// DialTLS 建立到上游的（可选 TLS 指纹）连接，签名与 http.Transport.DialTLSContext
	// 一致。nil 时所有请求走 Fallback。
	DialTLS func(ctx context.Context, network, addr string) (net.Conn, error)
	// Fallback 是常规 *http.Transport：非 https 请求、DialTLS 为 nil、或本包
	// 不支持的形态时安全回退。不能为 nil。
	Fallback *http.Transport
	// ResponseHeaderTimeout 等待响应头的超时；0 表示不限（OpenAI 流式路径
	// 的默认配置就是 0，不能用有限值兜底，否则长排队请求会被误杀）。
	ResponseHeaderTimeout time.Duration
	// MaxIdleConnsPerHost 每主机空闲连接数上限；<=0 时取 8。
	MaxIdleConnsPerHost int
	// HeaderOrder 覆盖默认头序；一般不需要，测试用。
	HeaderOrder []string
}

// pooledConn 一条可复用的上游连接及其读缓冲。
type pooledConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// Transport 固定头序的 HTTP/1.1 RoundTripper。
// 连接管理：每主机空闲连接池；单连接同一时刻只承载一个请求（HTTP/1.1 语义），
// 响应体读尽后连接回到池中复用（keep-alive），与官方客户端行为一致。
type Transport struct {
	cfg      Config
	order    []string
	orderIdx map[string]struct{}

	mu         sync.Mutex
	idle       map[string][]*pooledConn
	closedIdle bool
}

// New 创建固定头序 Transport。
func New(cfg Config) (*Transport, error) {
	if cfg.Fallback == nil {
		return nil, errors.New("orderedhttp: Fallback transport is required")
	}
	order := cfg.HeaderOrder
	if len(order) == 0 {
		order = DefaultCodexHeaderOrder
	}
	idx := make(map[string]struct{}, len(order))
	for _, name := range order {
		idx[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	if cfg.MaxIdleConnsPerHost <= 0 {
		cfg.MaxIdleConnsPerHost = 8
	}
	return &Transport{
		cfg:      cfg,
		order:    order,
		orderIdx: idx,
		idle:     make(map[string][]*pooledConn),
	}, nil
}

// Unwrap 返回底层回退 Transport，供请求级包装器（如 SSRF 固定 IP 探测）取回
// 常规 *http.Transport 使用。
func (t *Transport) Unwrap() http.RoundTripper { return t.cfg.Fallback }

// CloseIdleConnections 关闭全部空闲连接（http.Client.CloseIdleConnections 会调用）。
func (t *Transport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for addr, conns := range t.idle {
		for _, pc := range conns {
			_ = pc.conn.Close()
		}
		delete(t.idle, addr)
	}
	t.closedIdle = true
}

func (t *Transport) takeIdleConn(addr string) *pooledConn {
	t.mu.Lock()
	defer t.mu.Unlock()
	conns := t.idle[addr]
	if len(conns) == 0 {
		return nil
	}
	pc := conns[len(conns)-1]
	t.idle[addr] = conns[:len(conns)-1]
	return pc
}

func (t *Transport) putIdleConn(addr string, pc *pooledConn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closedIdle || len(t.idle[addr]) >= t.cfg.MaxIdleConnsPerHost {
		_ = pc.conn.Close()
		return
	}
	t.idle[addr] = append(t.idle[addr], pc)
}

// RoundTrip 实现 http.RoundTripper：固定头序写出 HTTP/1.1 请求。
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("orderedhttp: request or url is nil")
	}
	// 只接管 https 请求；其余（http 明文等）交给常规 Transport 保持既有行为。
	if t.cfg.DialTLS == nil || !strings.EqualFold(req.URL.Scheme, "https") {
		return t.cfg.Fallback.RoundTrip(req)
	}

	addr := canonicalAddr(req)
	for attempt := 0; ; attempt++ {
		pc := t.takeIdleConn(addr)
		fromIdle := pc != nil
		if pc == nil {
			conn, err := t.cfg.DialTLS(req.Context(), "tcp", addr)
			if err != nil {
				// 建连失败直接返回：连接未建立，上游不可能收到请求。
				return nil, err
			}
			pc = &pooledConn{conn: conn, br: bufio.NewReader(conn)}
		}
		resp, err := t.roundTripOnConn(req, pc, addr, fromIdle)
		if err == nil {
			return resp, nil
		}
		// 仅"从空闲池取出的连接在写出阶段就失败"（对端已静默关闭，请求字节
		// 未被服务端处理）时换新连接重试一次；其余错误原样返回，绝不重放
		// 可能已被处理的请求。
		if !fromIdle || !isStaleConnWriteError(err) || attempt >= 1 {
			_ = pc.conn.Close()
			return nil, err
		}
		_ = pc.conn.Close()
	}
}

// isStaleConnWriteError 判定是否为"空闲连接已被对端关闭"导致的写失败。
// 此时请求尚未被服务端接收，换连接重试是安全的（无重复请求风险）。
func isStaleConnWriteError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// "stale idle conn" 是本包自己包装的确定性标记；其余按常见传输错误文本匹配
	// （Windows 上是 "forcibly closed"，Linux 上是 "connection reset"）。
	for _, marker := range []string{"stale idle conn", "eof", "connection reset", "broken pipe", "use of closed", "forcibly closed"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// roundTripOnConn 在一条连接上完成一次请求/响应交换。
func (t *Transport) roundTripOnConn(req *http.Request, pc *pooledConn, addr string, fromIdle bool) (*http.Response, error) {
	// ctx 取消/超时中断：把 deadline 置到过去，强制阻塞中的读写立刻失败。
	watchDone := make(chan struct{})
	defer close(watchDone)
	if ctx := req.Context(); ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = pc.conn.SetDeadline(time.Now().Add(-time.Second))
			case <-watchDone:
			}
		}()
	}

	if err := t.writeRequest(pc, req); err != nil {
		if fromIdle {
			// 空闲连接上首个写失败：包装标记，供上层安全换连接重试。
			return nil, fmt.Errorf("stale idle conn: %w", err)
		}
		return nil, err
	}

	if t.cfg.ResponseHeaderTimeout > 0 {
		_ = pc.conn.SetReadDeadline(time.Now().Add(t.cfg.ResponseHeaderTimeout))
	}
	resp, err := http.ReadResponse(pc.br, req)
	if err != nil {
		return nil, err
	}
	// 响应头已到：清掉响应头超时，流式响应体不能被它截断。
	_ = pc.conn.SetReadDeadline(time.Time{})

	// 响应或本请求要求关闭连接：读尽后直接关。
	if resp.Close {
		resp.Body = newTrackingBody(resp.Body, func(reused bool) { _ = pc.conn.Close() })
		return resp, nil
	}
	// keep-alive：响应体读尽（EOF）后连接回池；未读尽就 Close 视为脏连接，直接关闭，
	// 避免把半截响应留给下一个请求（与 net/http 连接池语义一致）。
	resp.Body = newTrackingBody(resp.Body, func(reused bool) {
		if reused {
			t.putIdleConn(addr, pc)
		} else {
			_ = pc.conn.Close()
		}
	})
	return resp, nil
}

// trackingBody 记录响应体是否被完整读取（EOF）：只有读尽的连接才可回池复用。
type trackingBody struct {
	inner    io.ReadCloser
	sawEOF   bool
	once     sync.Once
	onClosed func(reused bool)
}

func newTrackingBody(inner io.ReadCloser, onClosed func(bool)) *trackingBody {
	return &trackingBody{inner: inner, onClosed: onClosed}
}

func (b *trackingBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if err == io.EOF {
		b.sawEOF = true
	}
	return n, err
}

func (b *trackingBody) Close() error {
	err := b.inner.Close()
	b.once.Do(func() {
		if b.onClosed != nil {
			b.onClosed(b.sawEOF)
		}
	})
	return err
}

// canonicalAddr 规范化连接地址（https 默认端口 443）。
func canonicalAddr(req *http.Request) string {
	addr := req.URL.Host
	if !strings.Contains(addr, ":") {
		addr = net.JoinHostPort(addr, "443")
	}
	return addr
}

// writeRequest 按固定顺序写出 HTTP/1.1 请求（请求行 + 头 + 体）。
func (t *Transport) writeRequest(pc *pooledConn, req *http.Request) error {
	bw := bufio.NewWriter(pc.conn)
	if err := t.writeHead(bw, req); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return t.writeBody(pc.conn, req)
}

func (t *Transport) writeHead(bw *bufio.Writer, req *http.Request) error {
	uri := req.URL.RequestURI()
	if uri == "" || !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	if _, err := fmt.Fprintf(bw, "%s %s HTTP/1.1\r\n", req.Method, uri); err != nil {
		return err
	}

	// Host 值：按取证顺序在序列表内输出（官方 hyper 把 host 追加在头部区
	// 末尾，位于 user-agent 之后、content-length 之前）。
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	// 1) 固定序头：按官方客户端取证顺序输出（含 Host 的落位）。
	for _, name := range t.order {
		if strings.EqualFold(name, "Host") {
			if _, err := fmt.Fprintf(bw, "Host: %s\r\n", host); err != nil {
				return err
			}
			continue
		}
		values := req.Header.Values(name)
		for _, v := range values {
			if err := writeHeaderLine(bw, name, v); err != nil {
				return err
			}
		}
	}
	// 2) 其余头：确定性兜底（字母序），保证任何输入都有稳定输出；
	// 位于固定序（含 Host）之后、Content-Length 之前。
	extra := make([]string, 0, len(req.Header))
	for name := range req.Header {
		canon := http.CanonicalHeaderKey(name)
		if _, ordered := t.orderIdx[canon]; ordered {
			continue
		}
		switch canon {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection",
			"Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Upgrade":
			// 逐跳/传输层管理头：由写出器统一管理，跳过调用方传入值。
			continue
		}
		extra = append(extra, name)
	}
	sort.Strings(extra)
	for _, name := range extra {
		for _, v := range req.Header.Values(name) {
			if err := writeHeaderLine(bw, name, v); err != nil {
				return err
			}
		}
	}

	// 3) Content-Length / Transfer-Encoding 固定在最后（hyper 传输层行为）。
	if req.Body == nil {
		if methodExpectsBody(req.Method) {
			// POST/PUT/PATCH 无体时补 content-length: 0。
			if _, err := bw.WriteString("Content-Length: 0\r\n"); err != nil {
				return err
			}
		}
		_, err := bw.WriteString("\r\n")
		return err
	}
	if req.ContentLength > 0 {
		_, err := fmt.Fprintf(bw, "Content-Length: %d\r\n\r\n", req.ContentLength)
		return err
	}
	// 长度未知：chunked（hyper 对未知长度流式体的行为）。
	_, err := bw.WriteString("Transfer-Encoding: chunked\r\n\r\n")
	return err
}

func (t *Transport) writeBody(conn net.Conn, req *http.Request) error {
	if req.Body == nil {
		return nil
	}
	defer func() { _ = req.Body.Close() }()
	if req.ContentLength > 0 {
		// 写足 ContentLength 字节；网关上游请求体都是完整缓冲，直接定量拷贝。
		_, err := io.CopyN(conn, req.Body, req.ContentLength)
		return err
	}
	// chunked 写出。
	buf := make([]byte, 32*1024)
	for {
		n, err := req.Body.Read(buf)
		if n > 0 {
			if _, werr := fmt.Fprintf(conn, "%x\r\n", n); werr != nil {
				return werr
			}
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return werr
			}
			if _, werr := io.WriteString(conn, "\r\n"); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			_, werr := io.WriteString(conn, "0\r\n\r\n")
			return werr
		}
		if err != nil {
			return err
		}
	}
}

func writeHeaderLine(bw *bufio.Writer, name, value string) error {
	_, err := fmt.Fprintf(bw, "%s: %s\r\n", name, strings.TrimRight(value, "\r\n"))
	return err
}

func methodExpectsBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}
