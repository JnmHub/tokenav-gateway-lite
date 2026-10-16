// 死锁探针：并发 + 连接复用下，「读到终止事件后提前返回并 Close」会导致真死锁。
//
// 与 openai_buffered_terminal_test.go 的区别（那两条是单请求、无复用，结果是绿的）：
//   * 共享同一个 http.Client / Transport → 连接会被复用；
//   * 8 个 goroutine 各自的请求交错；
//   * 上游按真实形态发：response.created → response.completed → [DONE] → 正常结束
//     （即终止事件之后**还有**未读数据）。
//
// 已实测复现（2026-10-03，本机）：8×150 次请求会在数十次后挂死，栈为
//
//	net/http.(*persistConn).readLoop.func4(...)   transport.go:2575   [chan receive]  ← <-eofc
//	net/http.(*bodyEOFSignal).condfn(...)         transport.go:3269
//	net/http.(*bodyEOFSignal).Read(...)           transport.go:3245
//	bufio.(*Scanner).Scan(...)
//	readOpenAICompatBufferedTerminal.func3()      openai_gateway_messages.go:756
//
// 即：读行 goroutine 在 Read 命中 EOF 时进入 fn 的 `<-eofc`（等 readLoop 退出），
// **且此时持有 es.mu**；readLoop 在 keep-alive 连接上不会退出（eofc 只在其函数返回时关闭）；
// 而能关掉连接的 Close() 又要先拿 es.mu —— 三者构成循环等待，请求永不返回。
//
// 【2026-10-03 已修复】修法见 openai_gateway_messages.go 的 bufferedReadCloseGuard：
// 响应体所有权收归读行 goroutine 一家（它也是唯一关闭响应体的人），调用方 Close 降级成
// 非阻塞停止信号，于是不会再出现第二个等待者去抢同一条连接的 eofc 信用。
// 修复后本用例稳定 1200/1200 完成（p50≈0ms / p90 1.0ms / p99 3.0ms / max 7.8ms，整轮 0.07s）。
//
// 它现在是**常开回归用例**：每次迭代仍有 5 秒看门狗，一旦回归立即打印全部 goroutine 栈并 fail，
// 不会把 `go test` 挂满 -timeout。
package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

func repeatedTerminalServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("x-request-id", "req-concurrency")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		fmt.Fprint(w, responsesCreatedSSE())
		if fl != nil {
			fl.Flush()
		}
		fmt.Fprint(w, responsesCompletedSSE())
		if fl != nil {
			fl.Flush()
		}
		// 终止事件之后仍有未读数据，模拟真实上游（随后还有 [DONE] 与分块终止）。
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
}

func TestReadOpenAICompatBufferedTerminal_ConcurrentWithConnectionReuse(t *testing.T) {
	srv := repeatedTerminalServer(t)
	defer srv.Close()

	transport := &http.Transport{
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}

	svc := &OpenAIGatewayService{cfg: bufferedTerminalTestConfig(0)}

	const workers = 8
	const perWorker = 150
	const iterationWatchdog = 5 * time.Second

	var mu sync.Mutex
	durations := make([]time.Duration, 0, workers*perWorker)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				start := time.Now()
				done := make(chan struct{})
				go func() {
					defer close(done)
					resp, err := client.Get(srv.URL)
					if err != nil {
						return
					}
					_, _, _, _ = svc.readOpenAICompatBufferedTerminal(resp, "test concurrent",
						fmt.Sprintf("req-%d-%d", id, i))
					_ = resp.Body.Close()
				}()

				select {
				case <-done:
					d := time.Since(start)
					mu.Lock()
					durations = append(durations, d)
					mu.Unlock()
				case <-time.After(iterationWatchdog):
					// 复现到了：打印全部 goroutine 栈后立即失败，不再挂满 -timeout。
					buf := make([]byte, 1<<20)
					n := runtime.Stack(buf, true)
					t.Errorf("第 %d 个 worker 的第 %d 次请求超过 %v 未返回（挂死）。goroutine 栈：\n%s",
						id, i, iterationWatchdog, buf[:n])
					return
				}
			}
		}(w)
	}
	wg.Wait()

	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
	pct := func(p float64) time.Duration {
		if len(durations) == 0 {
			return 0
		}
		return durations[int(float64(len(durations)-1)*p)]
	}
	t.Logf("完成 %d 次（预期 %d）p50=%v p90=%v p99=%v max=%v",
		len(durations), workers*perWorker, pct(0.5), pct(0.9), pct(0.99), durations[len(durations)-1])
}
