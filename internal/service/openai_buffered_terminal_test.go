// 本文件记录响应体生命周期的性质测试（第一条是早期的「否定结果」，其余是回归性质）。
//
// 背景：本机压测时经网关吞吐远低于上游直压，且 3–5% 请求挂死 ≥15s；goroutine dump 里请求
// 卡在 bodyEOFSignal.Close（等 readLoop 退出）与 bodyEOFSignal.Read（等 es.mu）。
//
// 早期假说「读行 goroutine 还卡在 Read 里，导致提前 Close 时三方互等」在**单请求**隔离场景
// 下被下面两条测试否掉（都通过）：单请求缺少「并发 + 连接复用」这个要素。
//
// 真正的复现见 openai_buffered_terminal_concurrency_test.go：共享 transport + 8 并发 ×150 次、
// 上游在终止事件之后仍有未读数据，第 6 个 worker 第 14 次即挂死。机制是读端与调用方并发收尾
// 同一个响应体时，两个等待者抢同一条连接共享的 eofc 信用（transport.go:2567 / 2575 两处
// `<-eofc`），输掉的那个永久挂着且仍持有 es.mu，调用方的 Close 便卡在 es.mu 上。
// 已在 openai_gateway_messages.go 的 bufferedReadCloseGuard 中修复：响应体所有权收归读行
// goroutine（唯一的读写者与关闭者），调用方 Close 只发非阻塞停止信号。
//
// 下面两条测试的价值：把「上游停滞时读取 + Close 必须是有界的」钉成可回归的性质。
package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func bufferedTerminalTestConfig(streamIntervalSeconds int) *config.Config {
	return &config.Config{
		Gateway: config.GatewayConfig{StreamDataIntervalTimeout: streamIntervalSeconds},
	}
}

// responsesCompletedSSE 是最小可用的「上游正常结束」SSE：网关只认这个终止事件。
func responsesCompletedSSE() string {
	return "event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"r1","object":"response",` +
		`"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}` + "\n\n"
}

func responsesCreatedSSE() string {
	return "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"r1","object":"response",` +
		`"status":"in_progress","output":[]}}` + "\n\n"
}

// stallServer 起一个「发完给定 SSE 后保持连接打开、不再发数据、也不发 chunked 终止块」的上游。
// 这正是真实上游会出现的形态（半开连接 / 长思考期间无输出），
// 也是 keep-alive 复用连接的条件 —— 只有要复用连接，Go 才会走 bodyEOFSignal.earlyCloseFn。
func stallServer(t *testing.T, sse string) (*httptest.Server, func()) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("响应不支持 Flush，测试前提不成立")
			return
		}
		_, _ = fmt.Fprint(w, sse)
		flusher.Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	cleanup := func() {
		close(release)
		srv.Close()
	}
	return srv, cleanup
}

// 上游发完终止事件后停滞（keep-alive、不发终止块）时：
// 读取函数返回后，调用方**紧接着**就会 Close 响应体
// （openai_gateway_chat_completions.go 的 defer resp.Body.Close()）。
// 提前 Close 会等 readLoop 退出，而 readLoop 要等响应体读完；
// 若此时读行 goroutine 还卡在 Read 里，就会互相等待 —— 请求挂死数分钟。
// 这条测试要求「读取 + Close」整体在有界时间内返回。
func TestReadOpenAICompatBufferedTerminal_CloseDoesNotHangWhenUpstreamStalls(t *testing.T) {
	srv, cleanup := stallServer(t, responsesCompletedSSE())
	defer cleanup()

	svc := &OpenAIGatewayService{cfg: bufferedTerminalTestConfig(0)}
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("请求上游失败: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上游状态码 = %d", resp.StatusCode)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = svc.readOpenAICompatBufferedTerminal(resp, "test buffered terminal", "req-terminal")
		_ = resp.Body.Close() // 模拟调用方的 defer Close
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("读取 + Close 在 5 秒内没有返回：响应体生命周期竞态（提前 Close 与仍在进行的 Read 互等）")
	}
}

// 上游停滞且没有终止事件时，必须由停滞超时兜底返回，并且返回过程同样不能挂起
// （注意 openai_gateway_messages.go 的超时分支自己也会 Close 一次响应体）。
func TestReadOpenAICompatBufferedTerminal_StallTimeoutReturnsWithoutHanging(t *testing.T) {
	srv, cleanup := stallServer(t, responsesCreatedSSE())
	defer cleanup()

	svc := &OpenAIGatewayService{cfg: bufferedTerminalTestConfig(1)}
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("请求上游失败: %v", err)
	}

	type result struct {
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		_, _, _, readErr := svc.readOpenAICompatBufferedTerminal(resp, "test buffered terminal", "req-stall")
		_ = resp.Body.Close()
		resCh <- result{err: readErr}
	}()

	select {
	case r := <-resCh:
		if r.err == nil {
			t.Fatal("上游停滞且无终止事件时应返回错误")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("停滞超时后仍未返回（1 秒的间隔超时形同虚设）")
	}
}

// bufferedReadCloseGuard 的核心契约：读端还卡在传输层读取时，调用方的 Close 也必须立即返回。
// 修复前，调用方 Close 会在 bodyEOFSignal.Close 里等 readLoop 发放 eofc 信用，而读端此刻正持有
// es.mu 等同一份信用 —— 两边互等，请求永不返回。
func TestBufferedReadCloseGuard_CloseNeverWaitsForTransport(t *testing.T) {
	raw := &blockingReadCloser{release: make(chan struct{}), entered: make(chan struct{})}
	guard := &bufferedReadCloseGuard{raw: raw, done: make(chan struct{})}

	go func() {
		buf := make([]byte, 8)
		_, _ = guard.Read(buf) // 模拟读行 goroutine 卡在传输层读取
	}()
	select {
	case <-raw.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("读端没有进入传输层读取，测试前提不成立")
	}

	closed := make(chan error, 1)
	go func() { closed <- guard.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("读端仍卡在传输层读取时 guard.Close 返回错误: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(raw.release)
		t.Fatal("读端仍卡在传输层读取时 guard.Close 没有立即返回（响应体生命周期死锁回归）")
	}

	// 停止信号生效后，读端不能再碰传输层（尤其是连接可能已被复用的情况）。
	if _, err := guard.Read(make([]byte, 8)); !errors.Is(err, errBufferedReadStopped) {
		t.Fatalf("停止后 Read 应返回 errBufferedReadStopped，实际 %v", err)
	}
	// 转发层 defer Close 与读端退出各会关一次，必须幂等。
	if err := guard.Close(); err != nil {
		t.Fatalf("重复 Close 应无副作用，实际 %v", err)
	}
	close(raw.release)
}

type blockingReadCloser struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return 0, io.EOF
}

func (b *blockingReadCloser) Close() error { return nil }

// 端到端形态的回归：连接池上限 + 连接复用 + 上游发完终止事件后仍保持连接打开（不发 chunked
// 终止块）。读取函数返回后调用方紧接着 Close，等价于转发层的 defer resp.Body.Close()。
func TestReadOpenAICompatBufferedTerminal_RepeatedCallerCloseWithConnectionReuse(t *testing.T) {
	srv, cleanup := stallServer(t, responsesCreatedSSE()+responsesCompletedSSE())
	defer cleanup()

	transport := &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	svc := &OpenAIGatewayService{cfg: bufferedTerminalTestConfig(0)}

	const iterations = 24
	for i := 0; i < iterations; i++ {
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("第 %d 次请求上游失败: %v", i, err)
		}
		done := make(chan struct{})
		go func(id int) {
			defer close(done)
			_, _, _, _ = svc.readOpenAICompatBufferedTerminal(resp, "test reused conn", fmt.Sprintf("req-%d", id))
			_ = resp.Body.Close() // 转发层就是在这里 Close 响应体
		}(i)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 次：读取 + Close 在 3 秒内没有返回（响应体生命周期死锁回归）", i)
		}
	}
}
