package service

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const openAICompactSSEKeepaliveKey = "openai_compact_sse_keepalive"

type openAICompactSSEKeepalive struct {
	mu      sync.Mutex
	writer  gin.ResponseWriter
	started bool
	stopped bool
	bytes   int
	stop    chan struct{}
}

// StartOpenAICompactSSEKeepalive writes delayed SSE comments while a body-signal
// compact request waits for the unary upstream response. The first beat is delayed
// so fast validation errors can still return their real HTTP status.
func StartOpenAICompactSSEKeepalive(c *gin.Context, interval time.Duration) func() {
	if c == nil || c.Writer == nil || interval <= 0 || !openAICompactClientWantsStream(c) {
		return func() {}
	}
	keepalive := &openAICompactSSEKeepalive{
		writer: c.Writer,
		stop:   make(chan struct{}),
	}
	c.Set(openAICompactSSEKeepaliveKey, keepalive)
	c.Writer = &openAICompactKeepaliveWriter{ResponseWriter: c.Writer, keepalive: keepalive}

	var requestDone <-chan struct{}
	if c.Request != nil {
		requestDone = c.Request.Context().Done()
	}
	go func() {
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-keepalive.stop:
				return
			case <-requestDone:
				return
			case <-timer.C:
			}
			if !keepalive.beat() {
				return
			}
			timer.Reset(interval)
		}
	}()
	return keepalive.Stop
}

func (k *openAICompactSSEKeepalive) beat() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return false
	}
	if !k.started {
		header := k.writer.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		k.writer.WriteHeader(http.StatusOK)
		k.started = true
	}
	written, err := k.writer.Write([]byte(": keepalive\n\n"))
	k.bytes += written
	if err != nil {
		k.stopped = true
		return false
	}
	k.writer.Flush()
	return true
}

func (k *openAICompactSSEKeepalive) Stop() {
	k.mu.Lock()
	k.markStoppedLocked()
	k.mu.Unlock()
}

func (k *openAICompactSSEKeepalive) markStoppedLocked() {
	if k.stopped {
		return
	}
	k.stopped = true
	close(k.stop)
}

// StopOpenAICompactSSEKeepaliveCommitted stops the heartbeat and reports whether
// it already committed a 200 response, in which case errors must be SSE terminal events.
func StopOpenAICompactSSEKeepaliveCommitted(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(openAICompactSSEKeepaliveKey)
	if !ok {
		return false
	}
	keepalive, ok := value.(*openAICompactSSEKeepalive)
	if !ok || keepalive == nil {
		return false
	}
	keepalive.mu.Lock()
	keepalive.markStoppedLocked()
	committed := keepalive.started
	keepalive.mu.Unlock()
	return committed
}

// OpenAICompactKeepaliveAdjustedWrittenSize excludes heartbeat comment bytes from
// the handler's "response already written" check so an upstream failure can still fail over.
func OpenAICompactKeepaliveAdjustedWrittenSize(c *gin.Context) int {
	if c == nil || c.Writer == nil {
		return -1
	}
	streamKeepaliveBytes := 0
	if value, ok := c.Get(openAIStreamKeepaliveBytesKey); ok {
		streamKeepaliveBytes, _ = value.(int)
	}
	size := c.Writer.Size()
	compactKeepaliveBytes := 0
	if value, ok := c.Get(openAICompactSSEKeepaliveKey); ok {
		if keepalive, valid := value.(*openAICompactSSEKeepalive); valid && keepalive != nil {
			keepalive.mu.Lock()
			size = keepalive.writer.Size()
			compactKeepaliveBytes = keepalive.bytes
			keepalive.mu.Unlock()
		}
	}
	if size < 0 {
		return size
	}
	keepaliveBytes := compactKeepaliveBytes + streamKeepaliveBytes
	if keepaliveBytes <= 0 {
		return size
	}
	if realSize := size - keepaliveBytes; realSize > 0 {
		return realSize
	}
	return -1
}

type openAICompactKeepaliveWriter struct {
	gin.ResponseWriter
	keepalive *openAICompactSSEKeepalive
}

// suspend 在真实响应接管 writer 前停止心跳。包装器会保留到请求结束，避免恢复
// c.Writer 的赋值与心跳协程或其他写回路径发生数据竞争。
func (w *openAICompactKeepaliveWriter) suspend() {
	w.keepalive.Stop()
}

func (w *openAICompactKeepaliveWriter) Header() http.Header {
	w.suspend()
	return w.ResponseWriter.Header()
}

func (w *openAICompactKeepaliveWriter) Write(data []byte) (int, error) {
	w.suspend()
	return w.ResponseWriter.Write(data)
}

func (w *openAICompactKeepaliveWriter) WriteString(value string) (int, error) {
	w.suspend()
	return w.ResponseWriter.WriteString(value)
}

func (w *openAICompactKeepaliveWriter) WriteHeader(code int) {
	w.suspend()
	w.ResponseWriter.WriteHeader(code)
}

func (w *openAICompactKeepaliveWriter) WriteHeaderNow() {
	w.suspend()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *openAICompactKeepaliveWriter) Flush() {
	w.suspend()
	w.ResponseWriter.Flush()
}

func (w *openAICompactKeepaliveWriter) Status() int {
	w.keepalive.mu.Lock()
	defer w.keepalive.mu.Unlock()
	return w.ResponseWriter.Status()
}

func (w *openAICompactKeepaliveWriter) Size() int {
	w.keepalive.mu.Lock()
	defer w.keepalive.mu.Unlock()
	return w.ResponseWriter.Size()
}

func (w *openAICompactKeepaliveWriter) Written() bool {
	w.keepalive.mu.Lock()
	defer w.keepalive.mu.Unlock()
	return w.ResponseWriter.Written()
}
