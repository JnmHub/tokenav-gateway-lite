package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

const liveSoakDurationEnv = "SUB2API_LIVE_SOAK_DURATION"

type liveSoakStore struct {
	GatewayCache
	mu          sync.Mutex
	controllers map[string]string
	owners      map[string]string
	closed      map[string]bool
	closeCount  atomic.Int64
}

func newLiveSoakStore() *liveSoakStore {
	return &liveSoakStore{
		controllers: make(map[string]string),
		owners:      make(map[string]string),
		closed:      make(map[string]bool),
	}
}

func (s *liveSoakStore) SaveLiveCall(context.Context, *LiveCallRecord, time.Duration) error {
	return nil
}

func (s *liveSoakStore) GetLiveCall(context.Context, string) (*LiveCallRecord, error) {
	return nil, ErrLiveCallNotFound
}

func (s *liveSoakStore) ClaimLiveController(_ context.Context, callHash, controller, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed[callHash] {
		return false, nil
	}
	s.controllers[callHash] = controller
	s.owners[callHash] = owner
	return true, nil
}

func (s *liveSoakStore) ReleaseLiveController(_ context.Context, callHash, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed[callHash] || s.owners[callHash] != owner {
		return false, nil
	}
	s.controllers[callHash] = LiveControllerPending
	delete(s.owners, callHash)
	return true, nil
}

func (s *liveSoakStore) GetLiveController(_ context.Context, callHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed[callHash] {
		return LiveControllerClosed, nil
	}
	return s.controllers[callHash], nil
}

func (s *liveSoakStore) MarkLiveCallClosed(_ context.Context, callHash string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed[callHash] {
		return false, nil
	}
	s.closed[callHash] = true
	s.controllers[callHash] = LiveControllerClosed
	delete(s.owners, callHash)
	s.closeCount.Add(1)
	return true, nil
}

type liveSoakConcurrencyCache struct {
	ConcurrencyCache
	releaseCount atomic.Int64
}

func (c *liveSoakConcurrencyCache) AcquireLiveLease(context.Context, int64, int, int64, int, int64, int, int64, string, bool) (bool, error) {
	return true, nil
}

func (c *liveSoakConcurrencyCache) RefreshLiveLease(context.Context, int64, int64, int64, int64, string) (bool, error) {
	return true, nil
}

func (c *liveSoakConcurrencyCache) ReleaseLiveLease(context.Context, int64, int64, int64, int64, string) error {
	c.releaseCount.Add(1)
	return nil
}

type liveSoakFrame struct {
	messageType coderws.MessageType
	payload     []byte
}

type liveSoakFrameConn struct {
	frames    chan liveSoakFrame
	done      chan struct{}
	closeOnce sync.Once
}

func newLiveSoakFrameConn() *liveSoakFrameConn {
	return &liveSoakFrameConn{
		frames: make(chan liveSoakFrame, 4),
		done:   make(chan struct{}),
	}
}

func (c *liveSoakFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return coderws.MessageText, nil, ctx.Err()
	case <-c.done:
		return coderws.MessageText, nil, netClosedError{}
	case frame := <-c.frames:
		return frame.messageType, frame.payload, nil
	}
}

func (c *liveSoakFrameConn) WriteFrame(ctx context.Context, messageType coderws.MessageType, payload []byte) error {
	frame := liveSoakFrame{messageType: messageType, payload: append([]byte(nil), payload...)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return netClosedError{}
	case c.frames <- frame:
		return nil
	}
}

func (c *liveSoakFrameConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

func (c *liveSoakFrameConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, payload, err := c.ReadFrame(ctx)
	return payload, err
}

func (c *liveSoakFrameConn) Ping(context.Context) error { return nil }

func (c *liveSoakFrameConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}

type netClosedError struct{}

func (netClosedError) Error() string { return "connection closed" }

type liveSoakDialer struct {
	dialCount atomic.Int64
}

func (d *liveSoakDialer) Dial(context.Context, string, http.Header, string) (openAIWSClientConn, int, http.Header, error) {
	d.dialCount.Add(1)
	return newLiveSoakFrameConn(), http.StatusSwitchingProtocols, nil, nil
}

type liveSoakMetrics struct {
	heapAlloc  uint64
	maxHeap    uint64
	maxRoutine int
	maxFD      int
}

func sampleLiveSoakMetrics(metrics *liveSoakMetrics) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	if memory.HeapAlloc > metrics.maxHeap {
		metrics.maxHeap = memory.HeapAlloc
	}
	if count := runtime.NumGoroutine(); count > metrics.maxRoutine {
		metrics.maxRoutine = count
	}
	if count := liveSoakFDCount(); count > metrics.maxFD {
		metrics.maxFD = count
	}
}

func liveSoakFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func liveSoakDuration(t *testing.T) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(liveSoakDurationEnv))
	if raw == "" {
		t.Skipf("set %s to run the Live WebSocket stability gate", liveSoakDurationEnv)
	}
	duration, err := time.ParseDuration(raw)
	require.NoError(t, err)
	require.Positive(t, duration)
	return duration
}

func liveSoakSessions() int {
	const defaultSessions = 32
	raw := strings.TrimSpace(os.Getenv("SUB2API_LIVE_SOAK_SESSIONS"))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultSessions
	}
	return value
}

// TestOpenAILiveWebSocketSoak 是显式启用的发布门禁，不会进入普通测试时长。
// 它使用真实本地 WebSocket 连接持续双向传帧，并验证断开后控制器、并发槽、
// goroutine（协程）和 FD（文件句柄）全部收敛；媒体仍不经过 gateway-lite。
func TestOpenAILiveWebSocketSoak(t *testing.T) {
	duration := liveSoakDuration(t)
	sessions := liveSoakSessions()
	if testing.Short() {
		t.Skip("Live soak is disabled in short mode")
	}

	runtime.GC()
	var baselineMemory runtime.MemStats
	runtime.ReadMemStats(&baselineMemory)
	baselineRoutines := runtime.NumGoroutine()
	baselineFD := liveSoakFDCount()
	metrics := liveSoakMetrics{
		heapAlloc:  baselineMemory.HeapAlloc,
		maxHeap:    baselineMemory.HeapAlloc,
		maxRoutine: baselineRoutines,
		maxFD:      baselineFD,
	}

	store := newLiveSoakStore()
	concurrency := &liveSoakConcurrencyCache{}
	dialer := &liveSoakDialer{}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "live-soak-secret"}}
	cipher := newLiveAttestationCipher(cfg)
	attestation, err := cipher.Encrypt(`{"v":1,"test":"live-soak"}`)
	require.NoError(t, err)
	account := Account{
		ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "live-soak-token"},
		Concurrency: sessions, Status: StatusActive, Schedulable: true,
	}
	service := &OpenAIGatewayService{
		accountRepo:               stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:                     store,
		concurrencyService:        NewConcurrencyService(concurrency),
		cfg:                       cfg,
		liveAttestationCipher:     cipher,
		openaiWSPassthroughDialer: dialer,
	}

	var handlerWG sync.WaitGroup
	var handlerErrors atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, acceptErr := coderws.Accept(writer, request, nil)
		if acceptErr != nil {
			handlerErrors.Add(1)
			return
		}
		defer func() { _ = connection.CloseNow() }()
		callID := request.URL.Query().Get("call_id")
		record := &LiveCallRecord{
			CallID: callID, CallHash: hashLiveCallID(callID), AccountID: account.ID,
			APIKeyID: -31, UserID: 41, GroupID: 51, LeaseID: "lease-" + callID,
			Model: "gpt-live", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(duration + time.Minute),
			Controller: LiveControllerPending, AttestationCiphertext: attestation,
		}
		proxyErr := service.ProxyLiveSideband(request.Context(), record, connection)
		if proxyErr != nil && !errors.Is(proxyErr, ErrLiveCallNotFound) {
			handlerErrors.Add(1)
		}
		handlerWG.Done()
	}))

	var clientWG sync.WaitGroup
	var readyWG sync.WaitGroup
	var clientErrors atomic.Int64
	var frames atomic.Int64
	start := make(chan struct{})
	clientWG.Add(sessions)
	readyWG.Add(sessions)
	handlerWG.Add(sessions)
	for index := 0; index < sessions; index++ {
		go func(index int) {
			defer clientWG.Done()
			url := "ws" + strings.TrimPrefix(server.URL, "http") + "?call_id=" + fmt.Sprintf("call-%d", index)
			connection, _, dialErr := coderws.Dial(context.Background(), url, nil)
			if dialErr != nil {
				clientErrors.Add(1)
				readyWG.Done()
				handlerWG.Done()
				return
			}
			defer func() { _ = connection.CloseNow() }()
			readyWG.Done()
			<-start
			deadline := time.Now().Add(duration)
			sequence := 0
			for time.Now().Before(deadline) {
				payload := []byte(fmt.Sprintf(`{"type":"session.update","sequence":%d}`, sequence))
				frameCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				writeErr := connection.Write(frameCtx, coderws.MessageText, payload)
				if writeErr == nil {
					_, _, writeErr = connection.Read(frameCtx)
				}
				cancel()
				if writeErr != nil {
					clientErrors.Add(1)
					return
				}
				frames.Add(1)
				sequence++
				time.Sleep(100 * time.Millisecond)
			}
			terminalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			terminalErr := connection.Write(terminalCtx, coderws.MessageText, []byte(`{"type":"session.ended"}`))
			if terminalErr == nil {
				_, _, terminalErr = connection.Read(terminalCtx)
			}
			cancel()
			if terminalErr != nil {
				clientErrors.Add(1)
			}
		}(index)
	}
	readyWG.Wait()
	close(start)

	done := make(chan struct{})
	go func() {
		clientWG.Wait()
		close(done)
	}()
	sampleTicker := time.NewTicker(time.Minute)
	defer sampleTicker.Stop()
	for {
		select {
		case <-sampleTicker.C:
			sampleLiveSoakMetrics(&metrics)
			t.Logf("Live WebSocket soak progress: frames=%d heap=%d goroutines=%d fd=%d", frames.Load(), metrics.maxHeap, runtime.NumGoroutine(), liveSoakFDCount())
		case <-done:
			goto completed
		}
	}

completed:
	handlerWG.Wait()
	server.Close()
	runtime.GC()
	time.Sleep(200 * time.Millisecond)
	runtime.GC()
	sampleLiveSoakMetrics(&metrics)
	var finalMemory runtime.MemStats
	runtime.ReadMemStats(&finalMemory)
	finalRoutines := runtime.NumGoroutine()
	finalFD := liveSoakFDCount()

	require.Zero(t, clientErrors.Load())
	require.Zero(t, handlerErrors.Load())
	require.Equal(t, int64(sessions), store.closeCount.Load())
	require.Equal(t, int64(sessions), concurrency.releaseCount.Load())
	require.Equal(t, int64(sessions), dialer.dialCount.Load())
	require.Greater(t, frames.Load(), int64(sessions))
	require.LessOrEqual(t, finalMemory.HeapAlloc, metrics.heapAlloc+24*1024*1024)
	require.LessOrEqual(t, finalRoutines, baselineRoutines+12)
	if baselineFD >= 0 && finalFD >= 0 {
		require.LessOrEqual(t, finalFD, baselineFD+4)
		require.LessOrEqual(t, metrics.maxFD, baselineFD+3*sessions+64)
	}
	require.LessOrEqual(t, metrics.maxHeap, metrics.heapAlloc+128*1024*1024)
	require.LessOrEqual(t, metrics.maxRoutine, baselineRoutines+8*sessions+32)
	t.Logf("Live WebSocket soak passed: duration=%s sessions=%d frames=%d heap_baseline=%d heap_peak=%d heap_final=%d goroutines=%d/%d/%d fd=%d/%d/%d", duration, sessions, frames.Load(), metrics.heapAlloc, metrics.maxHeap, finalMemory.HeapAlloc, baselineRoutines, metrics.maxRoutine, finalRoutines, baselineFD, metrics.maxFD, finalFD)
}
