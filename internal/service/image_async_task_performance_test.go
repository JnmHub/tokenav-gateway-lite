//go:build performance

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestImageTaskWorkerPerformance is an opt-in capacity gate. It deliberately
// uses an in-memory durable-queue double so the result measures the bounded
// image worker, encryption, artifact persistence and settlement pipeline rather
// than Docker or PostgreSQL startup time. Real PostgreSQL/Redis/MinIO behavior
// is covered separately by integration tests.
func TestImageTaskWorkerPerformance(t *testing.T) {
	taskCount := performanceEnvInt(t, "IMAGE_PERF_TASKS", 5000, 100, 100000)
	workers := performanceEnvInt(t, "IMAGE_PERF_WORKERS", 8, 1, 64)
	delayMillis := performanceEnvInt(t, "IMAGE_PERF_EXEC_DELAY_MS", 5, 0, 1000)

	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	cfg.Workers = workers
	cfg.QueueCapacity = taskCount
	cfg.BatchSize = 64
	cfg.PerUserConcurrency = 16
	cfg.ExecutionTimeout = 30 * time.Second
	cfg.ClaimTTL = 4 * time.Minute
	cfg.PollInterval = 100 * time.Millisecond

	store := newImageTaskPerformanceStore(taskCount)
	billing := &imageTaskPerformanceBilling{}
	objects := newImageTaskPerformanceObjectStore(taskCount)
	executor := &imageTaskPerformanceExecutor{
		delay:   time.Duration(delayMillis) * time.Millisecond,
		payload: bytes.Repeat([]byte{0x5a}, 1024),
	}
	service, err := NewImageTaskService(store, billing, objects, executor, imageTaskPerformanceEncryptor{}, cfg)
	if err != nil {
		t.Fatalf("create image task service: %v", err)
	}

	ctx := context.Background()
	for index := 0; index < taskCount; index++ {
		payload := []byte(fmt.Sprintf(`{"model":"gpt-image-1","prompt":"performance-%d"}`, index))
		_, replayed, submitErr := service.Submit(ctx, ImageTaskCreateInput{
			IdempotencyKey:   fmt.Sprintf("performance-%d", index),
			UserID:           int64(index%500 + 1),
			APIKeyID:         int64(index%500 + 1001),
			GatewayKeyID:     fmt.Sprintf("performance-key-%d", index%500),
			Model:            "gpt-image-1",
			Endpoint:         "/v1/images/generations",
			ContentType:      "application/json",
			ImageCount:       1,
			RequestPayload:   payload,
			MaxReservedCents: 100,
		})
		if submitErr != nil || replayed {
			t.Fatalf("submit task %d: replayed=%v err=%v", index, replayed, submitErr)
		}
	}

	peakQueue := store.queueDepth()
	beforeGoroutines := runtime.NumGoroutine()
	var peakHeap atomic.Uint64
	var peakGoroutines atomic.Int64
	monitorDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-ticker.C:
				var stats runtime.MemStats
				runtime.ReadMemStats(&stats)
				updateAtomicMaxUint64(&peakHeap, stats.HeapAlloc)
				updateAtomicMaxInt64(&peakGoroutines, int64(runtime.NumGoroutine()))
			}
		}
	}()

	startedAt := time.Now()
	service.Start()
	latencies := make([]time.Duration, 0, taskCount)
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()
	for len(latencies) < taskCount {
		select {
		case latency := <-store.completed:
			latencies = append(latencies, latency)
		case <-timeout.C:
			service.Stop()
			close(monitorDone)
			t.Fatalf("image task pressure timed out: completed=%d/%d queue=%d", len(latencies), taskCount, store.queueDepth())
		}
	}
	service.Stop()
	elapsed := time.Since(startedAt)
	close(monitorDone)

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := performancePercentile(latencies, 0.50)
	p95 := performancePercentile(latencies, 0.95)
	p99 := performancePercentile(latencies, 0.99)
	if store.queueDepth() != 0 {
		t.Fatalf("queue did not drain: %d", store.queueDepth())
	}
	if billing.reserved.Load() != int64(taskCount) || billing.settled.Load() != int64(taskCount) || billing.released.Load() != 0 {
		t.Fatalf("billing mismatch: reserved=%d settled=%d released=%d tasks=%d", billing.reserved.Load(), billing.settled.Load(), billing.released.Load(), taskCount)
	}
	if executor.calls.Load() != int64(taskCount) {
		t.Fatalf("executor calls=%d, want %d", executor.calls.Load(), taskCount)
	}
	if objects.count() != taskCount {
		t.Fatalf("stored artifacts=%d, want %d", objects.count(), taskCount)
	}

	// Stop waits for every worker and cleanup goroutine. A small grace period
	// makes the leak assertion stable across slower CI hosts.
	time.Sleep(100 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	if afterGoroutines > beforeGoroutines+2 {
		t.Fatalf("worker goroutine leak: before=%d after=%d", beforeGoroutines, afterGoroutines)
	}
	t.Logf("IMAGE_WORKER_METRICS tasks=%d workers=%d delay_ms=%d elapsed=%s throughput=%.2f/s p50=%s p95=%s p99=%s peak_queue=%d peak_heap_bytes=%d peak_goroutines=%d goroutines_after_stop=%d",
		taskCount, workers, delayMillis, elapsed, float64(taskCount)/elapsed.Seconds(), p50, p95, p99,
		peakQueue, peakHeap.Load(), peakGoroutines.Load(), afterGoroutines)
}

func performanceEnvInt(t *testing.T, name string, fallback, minimum, maximum int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		t.Fatalf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return value
}

func performancePercentile(values []time.Duration, percentile float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * percentile)
	return values[index]
}

func updateAtomicMaxUint64(target *atomic.Uint64, value uint64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func updateAtomicMaxInt64(target *atomic.Int64, value int64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

type imageTaskPerformanceStore struct {
	mu        sync.Mutex
	tasks     map[string]*ImageTask
	order     []string
	nextClaim int
	claimed   map[string]string
	finalized map[string]struct{}
	completed chan time.Duration
}

func newImageTaskPerformanceStore(capacity int) *imageTaskPerformanceStore {
	return &imageTaskPerformanceStore{
		tasks: make(map[string]*ImageTask, capacity), order: make([]string, 0, capacity),
		claimed: make(map[string]string, capacity), finalized: make(map[string]struct{}, capacity),
		completed: make(chan time.Duration, capacity),
	}
}

func (s *imageTaskPerformanceStore) Ready(context.Context) error { return nil }

func (s *imageTaskPerformanceStore) FindByIdempotency(_ context.Context, userID, apiKeyID int64, gatewayKeyID, idempotencyKey string) (*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if task.IdempotencyKey == strings.TrimSpace(idempotencyKey) && task.BelongsTo(userID, apiKeyID, gatewayKeyID) {
			return performanceCloneImageTask(task), nil
		}
	}
	return nil, ErrImageTaskNotFound
}

func (s *imageTaskPerformanceStore) Create(_ context.Context, task *ImageTask, queueCapacity, _ int) (*ImageTask, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks)-len(s.finalized) >= queueCapacity {
		return nil, false, ErrImageTaskQueueFull
	}
	cloned := performanceCloneImageTask(task)
	s.tasks[cloned.ID] = cloned
	s.order = append(s.order, cloned.ID)
	return performanceCloneImageTask(cloned), false, nil
}

func (s *imageTaskPerformanceStore) Get(_ context.Context, id string) (*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task := s.tasks[id]
	if task == nil {
		return nil, ErrImageTaskNotFound
	}
	return performanceCloneImageTask(task), nil
}

func (s *imageTaskPerformanceStore) GetByUser(ctx context.Context, id string, userID int64) (*ImageTask, error) {
	task, err := s.Get(ctx, id)
	if err != nil || task.UserID != userID {
		return nil, ErrImageTaskNotFound
	}
	return task, nil
}

func (s *imageTaskPerformanceStore) ListByOwner(context.Context, int64, int64, string, int, string) ([]*ImageTask, string, error) {
	return nil, "", nil
}
func (s *imageTaskPerformanceStore) ListByUser(context.Context, int64, int, string) ([]*ImageTask, string, error) {
	return nil, "", nil
}
func (s *imageTaskPerformanceStore) ListByUserState(context.Context, int64, string, int, string) ([]*ImageTask, string, error) {
	return nil, "", nil
}
func (s *imageTaskPerformanceStore) ListAll(context.Context, string, int, string) ([]*ImageTask, string, error) {
	return nil, "", nil
}

func (s *imageTaskPerformanceStore) QueueDepth(context.Context) (int64, error) {
	return s.queueDepth(), nil
}

func (s *imageTaskPerformanceStore) queueDepth() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.tasks) - len(s.finalized))
}

func (s *imageTaskPerformanceStore) ActiveForUser(_ context.Context, userID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for id, task := range s.tasks {
		if task.UserID == userID {
			if _, done := s.finalized[id]; !done {
				count++
			}
		}
	}
	return count, nil
}

func (s *imageTaskPerformanceStore) ClaimDue(_ context.Context, owner string, now time.Time, _ int, ttl time.Duration) ([]*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.nextClaim < len(s.order) {
		id := s.order[s.nextClaim]
		s.nextClaim++
		task := s.tasks[id]
		if task == nil || task.NextAttemptAt.After(now) {
			continue
		}
		if _, done := s.finalized[id]; done {
			continue
		}
		s.claimed[id] = owner
		until := now.Add(ttl)
		claimed := performanceCloneImageTask(task)
		claimed.ClaimOwner = owner
		claimed.ClaimUntil = &until
		return []*ImageTask{claimed}, nil
	}
	return nil, nil
}

func (s *imageTaskPerformanceStore) SaveClaimed(_ context.Context, task *ImageTask, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task == nil || s.claimed[task.ID] != owner {
		return ErrImageTaskConflict
	}
	s.tasks[task.ID] = performanceCloneImageTask(task)
	if task.FinanciallyFinal() {
		if _, recorded := s.finalized[task.ID]; !recorded {
			s.finalized[task.ID] = struct{}{}
			delete(s.claimed, task.ID)
			s.completed <- time.Since(task.CreatedAt)
		}
	}
	return nil
}

func (s *imageTaskPerformanceStore) SaveUnclaimedTerminal(_ context.Context, task *ImageTask) error {
	s.mu.Lock()
	owner := s.claimed[task.ID]
	s.mu.Unlock()
	return s.SaveClaimed(context.Background(), task, owner)
}

func (s *imageTaskPerformanceStore) RequestCancel(context.Context, string, int64, int64, string, time.Time) (*ImageTask, error) {
	return nil, ErrImageTaskConflict
}
func (s *imageTaskPerformanceStore) RequestCancelForUser(context.Context, string, int64, time.Time) (*ImageTask, error) {
	return nil, ErrImageTaskConflict
}
func (s *imageTaskPerformanceStore) ListCleanupDue(context.Context, time.Time, int) ([]*ImageTask, error) {
	return nil, nil
}
func (s *imageTaskPerformanceStore) DeleteFinal(context.Context, string) error { return nil }

func performanceCloneImageTask(task *ImageTask) *ImageTask {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.PolicySnapshot = append([]byte(nil), task.PolicySnapshot...)
	cloned.BillingSnapshot = append([]byte(nil), task.BillingSnapshot...)
	cloned.ExecutionSnapshot = append([]byte(nil), task.ExecutionSnapshot...)
	cloned.Artifacts = append([]ImageTaskArtifact(nil), task.Artifacts...)
	return &cloned
}

type imageTaskPerformanceBilling struct {
	reserved atomic.Int64
	settled  atomic.Int64
	released atomic.Int64
}

func (b *imageTaskPerformanceBilling) Reserve(_ context.Context, task *ImageTask, cents int64) (ImageTaskReservation, error) {
	b.reserved.Add(1)
	return ImageTaskReservation{ID: task.ID, ReservedCents: cents}, nil
}
func (b *imageTaskPerformanceBilling) Settle(context.Context, *ImageTask, int64) error {
	b.settled.Add(1)
	return nil
}
func (b *imageTaskPerformanceBilling) Release(context.Context, *ImageTask) error {
	b.released.Add(1)
	return nil
}

type imageTaskPerformanceExecutor struct {
	delay   time.Duration
	payload []byte
	calls   atomic.Int64
}

func (e *imageTaskPerformanceExecutor) Execute(ctx context.Context, _ *ImageTask, _ []byte) (ImageTaskExecutionResult, error) {
	e.calls.Add(1)
	if e.delay > 0 {
		timer := time.NewTimer(e.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ImageTaskExecutionResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return ImageTaskExecutionResult{
		Artifacts: []ImageTaskExecutionArtifact{{
			FileName: "performance.png", ContentType: "image/png",
			Reader: io.NopCloser(bytes.NewReader(e.payload)),
		}},
		ActualCents: 1, BillingSettled: true,
	}, nil
}

type imageTaskPerformanceObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newImageTaskPerformanceObjectStore(capacity int) *imageTaskPerformanceObjectStore {
	return &imageTaskPerformanceObjectStore{objects: make(map[string][]byte, capacity)}
}

func (s *imageTaskPerformanceObjectStore) Put(_ context.Context, key string, body io.Reader, _ string, maxBytes int64) (int64, string, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil {
		return 0, "", fmt.Errorf("bounded artifact read: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return 0, "", errors.New("artifact exceeds configured maximum")
	}
	digest := sha256.Sum256(data)
	s.mu.Lock()
	s.objects[key] = append([]byte(nil), data...)
	s.mu.Unlock()
	return int64(len(data)), hex.EncodeToString(digest[:]), nil
}
func (s *imageTaskPerformanceObjectStore) Open(_ context.Context, key string) (io.ReadCloser, string, int64, error) {
	s.mu.Lock()
	data, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, "", 0, ErrImageTaskNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), "image/png", int64(len(data)), nil
}
func (s *imageTaskPerformanceObjectStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}
func (s *imageTaskPerformanceObjectStore) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "local-image-task://" + key, nil
}
func (s *imageTaskPerformanceObjectStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

type imageTaskPerformanceEncryptor struct{}

func (imageTaskPerformanceEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (imageTaskPerformanceEncryptor) Decrypt(value string) (string, error) { return value, nil }
