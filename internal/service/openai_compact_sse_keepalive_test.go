package service

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const compactKeepaliveTestInterval = 10 * time.Millisecond

// 等待至少一次心跳调度；读取 recorder 前必须先停拍，以建立并发可见性边界。
func waitForCompactKeepaliveBeat() {
	time.Sleep(20 * compactKeepaliveTestInterval)
}

func TestStartOpenAICompactSSEKeepalive_NoopWhenUnmarkedOrDisabled(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	stop := StartOpenAICompactSSEKeepalive(c, compactKeepaliveTestInterval)
	waitForCompactKeepaliveBeat()
	stop()
	require.Zero(t, recorder.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))

	c, recorder = newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	stop = StartOpenAICompactSSEKeepalive(c, 0)
	waitForCompactKeepaliveBeat()
	stop()
	require.Zero(t, recorder.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAICompactSSEKeepalive_CommitsHeadersAndComments(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, compactKeepaliveTestInterval)
	defer stop()
	waitForCompactKeepaliveBeat()

	require.True(t, StopOpenAICompactSSEKeepaliveCommitted(c))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "no", recorder.Header().Get("X-Accel-Buffering"))
	require.Contains(t, recorder.Body.String(), ": keepalive\n\n")
}

func TestOpenAICompactSSEKeepalive_StopBeforeFirstBeatKeepsWriterUntouched(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, time.Hour)
	stop()
	waitForCompactKeepaliveBeat()
	require.Zero(t, recorder.Body.Len())
	require.False(t, StopOpenAICompactSSEKeepaliveCommitted(c))
}

func TestOpenAICompactKeepaliveWriter_RequestSideWriteSuspendsBeats(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, compactKeepaliveTestInterval)
	defer stop()
	waitForCompactKeepaliveBeat()

	_, err := c.Writer.Write([]byte(`{"error":"local reject"}`))
	require.NoError(t, err)
	lengthAfterWrite := recorder.Body.Len()
	waitForCompactKeepaliveBeat()

	require.Equal(t, lengthAfterWrite, recorder.Body.Len(), "真实响应写入后不得继续追加心跳")
	require.Contains(t, recorder.Body.String(), ": keepalive\n\n")
	require.Contains(t, recorder.Body.String(), `{"error":"local reject"}`)
}

func TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesHeartbeatBytes(t *testing.T) {
	c, recorder := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	require.Equal(t, c.Writer.Size(), OpenAICompactKeepaliveAdjustedWrittenSize(c))

	stop := StartOpenAICompactSSEKeepalive(c, compactKeepaliveTestInterval)
	defer stop()
	before := OpenAICompactKeepaliveAdjustedWrittenSize(c)
	waitForCompactKeepaliveBeat()
	require.Equal(t, before, OpenAICompactKeepaliveAdjustedWrittenSize(c), "心跳字节不得阻止失败切换")

	_, err := c.Writer.Write([]byte("real-bytes"))
	require.NoError(t, err)
	require.Equal(t, len("real-bytes"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
	require.Contains(t, recorder.Body.String(), ": keepalive\n\n")
}

func TestOpenAICompactKeepaliveAdjustedWrittenSize_ExcludesRegularStreamHeartbeatBytes(t *testing.T) {
	c, _ := newOpenAICompactTestContext("/v1/responses")

	n, err := c.Writer.Write([]byte(":\n\n"))
	require.NoError(t, err)
	recordOpenAIStreamKeepaliveBytes(c, n)
	require.Equal(t, -1, OpenAICompactKeepaliveAdjustedWrittenSize(c), "普通 SSE 心跳不得阻止首输出前换号")

	_, err = c.Writer.Write([]byte("real-output"))
	require.NoError(t, err)
	require.Equal(t, len("real-output"), OpenAICompactKeepaliveAdjustedWrittenSize(c))
}

func TestOpenAICompactSSEKeepalive_ConcurrentStopIsIdempotent(t *testing.T) {
	c, _ := newOpenAICompactTestContext("/v1/responses/compact")
	MarkOpenAICompactClientStream(c)
	stop := StartOpenAICompactSSEKeepalive(c, compactKeepaliveTestInterval)

	var waitGroup sync.WaitGroup
	for i := 0; i < 16; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			stop()
		}()
	}
	waitGroup.Wait()
	require.NotNil(t, c.Writer)
}
