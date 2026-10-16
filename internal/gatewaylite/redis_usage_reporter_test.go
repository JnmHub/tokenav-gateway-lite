package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type usageSinkFunc func(context.Context, UsageEvent) error

func (f usageSinkFunc) ReportUsage(ctx context.Context, event UsageEvent) error {
	return f(ctx, event)
}

type usageBatchSink struct {
	mu      sync.Mutex
	batches [][]UsageEvent
}

type transientUsageBatchSink struct {
	attempts  atomic.Int32
	recovered chan []UsageEvent
}

func (s *transientUsageBatchSink) ReportUsage(context.Context, UsageEvent) error {
	return nil
}

func (s *transientUsageBatchSink) ReportUsageBatch(_ context.Context, events []UsageEvent) error {
	if s.attempts.Add(1) == 1 {
		return errors.New("temporary batch failure")
	}
	s.recovered <- append([]UsageEvent(nil), events...)
	return nil
}

type healthReportSink struct {
	mu      sync.Mutex
	reports []GatewayHealthReportRequest
}

func (s *healthReportSink) ReportGatewayHealth(_ context.Context, req GatewayHealthReportRequest) (*GatewayHealthReportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports = append(s.reports, req)
	return &GatewayHealthReportResponse{OK: true}, nil
}

func (s *healthReportSink) lastReport() (GatewayHealthReportRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reports) == 0 {
		return GatewayHealthReportRequest{}, false
	}
	return s.reports[len(s.reports)-1], true
}

func (s *usageBatchSink) ReportUsage(context.Context, UsageEvent) error {
	return nil
}

func (s *usageBatchSink) ReportUsageBatch(_ context.Context, events []UsageEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := append([]UsageEvent(nil), events...)
	s.batches = append(s.batches, copied)
	return nil
}

func (s *usageBatchSink) batchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

func (s *usageBatchSink) firstBatch() []UsageEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.batches) == 0 {
		return nil
	}
	return append([]UsageEvent(nil), s.batches[0]...)
}

func TestRedisUsageReporterQueuesAndFlushes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	reported := make(chan UsageEvent, 1)
	reporter := NewRedisUsageReporter(client, "test", usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		reported <- event
		return nil
	}))
	reporter.Start(ctx)

	event := UsageEvent{
		RequestID:   "req1",
		UserID:      42,
		KeyID:       "key1",
		Region:      "sg",
		ActualCents: 1,
	}
	require.NoError(t, reporter.ReportUsage(ctx, event))

	select {
	case got := <-reported:
		require.Equal(t, event.RequestID, got.RequestID)
		require.Equal(t, event.UserID, got.UserID)
	case <-time.After(2 * time.Second):
		t.Fatal("expected queued usage report to flush")
	}

	require.Eventually(t, func() bool {
		streamLen, err := client.XLen(ctx, "test:usage:stream").Result()
		return err == nil && streamLen == 0
	}, 2*time.Second, 20*time.Millisecond)
	pending, err := client.XPending(ctx, "test:usage:stream", "test-usage-reporters").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, pending.Count)
}

func TestRedisUsageReporterAckDoesNotTrimUnconsumedBacklog(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	reporter := NewRedisUsageReporter(client, "no-trim", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, reporter.ensureGroup(ctx))

	const total = 11000
	pipe := client.Pipeline()
	commands := make([]*redis.StringCmd, 0, total)
	for i := 0; i < total; i++ {
		commands = append(commands, pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: "no-trim:usage:stream",
			Values: map[string]any{"event": `{}`},
		}))
	}
	_, err := pipe.Exec(ctx)
	require.NoError(t, err)

	streams, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: "no-trim-usage-reporters", Consumer: "test-worker",
		Streams: []string{"no-trim:usage:stream", ">"}, Count: 1,
	}).Result()
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Messages, 1)
	require.NotEmpty(t, commands[0].Val())

	reporter.ack(ctx, []string{streams[0].Messages[0].ID}, "no-trim")
	streamLen, err := client.XLen(ctx, "no-trim:usage:stream").Result()
	require.NoError(t, err)
	require.EqualValues(t, total-1, streamLen)
}

func TestRedisUsageReporterRecoversStalePendingAfterRestart(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	now := time.Now()
	mr.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	const prefix = "restart-recovery"
	oldReporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, oldReporter.ensureGroup(ctx))
	baseEvent := UsageEvent{RequestID: "req-before-restart", UserID: 42, KeyID: "key-1", ActualCents: 0}
	correctionEvent := baseEvent
	correctionEvent.ActualCents = 3
	for _, event := range []UsageEvent{baseEvent, correctionEvent} {
		body, err := json.Marshal(queuedUsageEvent{Event: event, EnqueuedAt: now.UnixMilli()})
		require.NoError(t, err)
		require.NoError(t, client.XAdd(ctx, &redis.XAddArgs{
			Stream: oldReporter.streamKey(),
			Values: map[string]any{"event": string(body)},
		}).Err())
	}

	// 模拟旧进程已从 Stream 取走消息、但在上报或 ACK 前退出。
	streams, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    oldReporter.groupName(),
		Consumer: "old-worker",
		Streams:  []string{oldReporter.streamKey(), ">"},
		Count:    2,
	}).Result()
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Messages, 2)

	var deliveries atomic.Int32
	var actualCents []int64
	newReporter := NewRedisUsageReporter(client, prefix, usageSinkFunc(func(_ context.Context, got UsageEvent) error {
		deliveries.Add(1)
		require.Equal(t, baseEvent.RequestID, got.RequestID)
		actualCents = append(actualCents, got.ActualCents)
		return nil
	}))

	// 新 worker 不会抢仍处于 10 秒上报 + 30 秒退避保护窗口内的消息。
	require.False(t, newReporter.recoverStalePending(ctx, 0, "worker-0", prefix))
	require.EqualValues(t, 0, deliveries.Load())
	pending, err := client.XPending(ctx, oldReporter.streamKey(), oldReporter.groupName()).Result()
	require.NoError(t, err)
	require.EqualValues(t, 2, pending.Count)

	// 超过保护窗口后，新进程自动认领旧 consumer 的 pending，并沿用正常
	// handleMessages 路径完成投递、XACK 和 XDEL。
	mr.SetTime(now.Add(usagePendingRecoveryMinIdle + time.Second))
	newReporter.pendingRecovery.mu.Lock()
	newReporter.pendingRecovery.next[prefix] = time.Time{}
	newReporter.pendingRecovery.mu.Unlock()
	require.True(t, newReporter.recoverStalePending(ctx, 0, "worker-0", prefix))
	require.EqualValues(t, 2, deliveries.Load())
	require.Equal(t, []int64{0, 3}, actualCents)

	streamLen, err := client.XLen(ctx, oldReporter.streamKey()).Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, streamLen)
	pending, err = client.XPending(ctx, oldReporter.streamKey(), oldReporter.groupName()).Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, pending.Count)

	// 即使下一轮恢复扫描再次从头开始，已 ACK + XDEL 的消息也不会重复投递。
	newReporter.pendingRecovery.mu.Lock()
	newReporter.pendingRecovery.next[prefix] = time.Time{}
	newReporter.pendingRecovery.mu.Unlock()
	require.False(t, newReporter.recoverStalePending(ctx, 0, "worker-0", prefix))
	require.EqualValues(t, 2, deliveries.Load())
}

func TestRedisUsageReporterQueuesWithoutSynchronousSinkCall(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	called := make(chan struct{}, 1)
	reporter := NewRedisUsageReporter(client, "async", usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		called <- struct{}{}
		return nil
	}))

	require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{
		RequestID:   "req-async",
		UserID:      42,
		KeyID:       "key-async",
		Region:      "sg",
		ActualCents: 3,
	}))

	select {
	case <-called:
		t.Fatal("usage sink must not be called synchronously while Redis usage queue is enabled")
	default:
	}
	streamLen, err := client.XLen(ctx, "async:usage:stream").Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, streamLen)
}

func TestRedisUsageReporterUsesBatchSink(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	sink := &usageBatchSink{}
	reporter := NewRedisUsageReporter(client, "batch", sink)
	for i := 0; i < 3; i++ {
		require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{
			RequestID:   fmt.Sprintf("req-batch-%d", i),
			UserID:      int64(40 + i),
			KeyID:       "key1",
			Region:      "sg",
			ActualCents: 1,
		}))
	}
	reporter.Start(ctx)

	require.Eventually(t, func() bool {
		return sink.batchCount() > 0
	}, 2*time.Second, 20*time.Millisecond)
	first := sink.firstBatch()
	require.Len(t, first, 3)
	require.EqualValues(t, 40, first[0].UserID)
	require.EqualValues(t, 42, first[2].UserID)
}

func TestRedisUsageReporterHonorsConfiguredBatchSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	sink := &usageBatchSink{}
	reporter := NewRedisUsageReporter(client, "batch-size", sink)
	for i := 0; i < 5; i++ {
		require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{RequestID: fmt.Sprintf("req-batch-size-%d", i), UserID: int64(i + 1)}))
	}
	reporter.StartWorkersWithBatchSize(ctx, 1, 2)

	require.Eventually(t, func() bool { return sink.batchCount() >= 3 }, 2*time.Second, 20*time.Millisecond)
	require.Len(t, sink.firstBatch(), 2)
}

func TestRedisUsageReporterRetriesFailedBatchAfterOneSharedDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	sink := &transientUsageBatchSink{recovered: make(chan []UsageEvent, 1)}
	reporter := NewRedisUsageReporter(client, "batch-retry", sink)
	for i := 0; i < 3; i++ {
		require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{RequestID: fmt.Sprintf("req-batch-retry-%d", i), UserID: int64(i + 1)}))
	}
	started := time.Now()
	reporter.StartWorkersWithBatchSize(ctx, 1, 200)

	select {
	case events := <-sink.recovered:
		require.Len(t, events, 3)
		require.Less(t, time.Since(started), 2500*time.Millisecond)
	case <-time.After(3 * time.Second):
		t.Fatal("failed usage batch was not retried as one batch")
	}
}

func TestRedisUsageReporterRetriesTransientControlPlaneFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	var attempts atomic.Int32
	reported := make(chan UsageEvent, 1)
	reporter := NewRedisUsageReporter(client, "retry", usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		if attempts.Add(1) == 1 {
			return errors.New("temporary control-plane timeout")
		}
		reported <- event
		return nil
	}))
	reporter.Start(ctx)

	event := UsageEvent{RequestID: "req-retry", UserID: 42, KeyID: "key-retry", Region: "sg", ActualCents: 3}
	require.NoError(t, reporter.ReportUsage(ctx, event))

	select {
	case got := <-reported:
		require.Equal(t, event.RequestID, got.RequestID)
		require.EqualValues(t, 3, got.ActualCents)
	case <-time.After(4 * time.Second):
		t.Fatal("expected usage report to recover after a transient failure")
	}
	require.EqualValues(t, 2, attempts.Load())
	require.Eventually(t, func() bool {
		streamLen, err := client.XLen(ctx, "retry:usage:stream").Result()
		return err == nil && streamLen == 0
	}, 2*time.Second, 20*time.Millisecond)
}

func TestRedisUsageReporterUsageQueueStats(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	reporter := NewRedisUsageReporter(client, "stats", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{RequestID: "req1", UserID: 42}))
	require.NoError(t, client.XAdd(ctx, &redis.XAddArgs{
		Stream: "stats:usage:dead",
		Values: map[string]any{"event": "bad", "reason": "test"},
	}).Err())

	stats, err := reporter.UsageQueueStats(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.StreamLength)
	require.EqualValues(t, 1, stats.DeadCount)
}

func TestUsageQueueHealthMonitorReportsCriticalWhenDeadStreamHasEvents(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	reporter := NewRedisUsageReporter(client, "monitor", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.NoError(t, client.XAdd(ctx, &redis.XAddArgs{
		Stream: "monitor:usage:dead",
		Values: map[string]any{"event": "bad", "reason": "test"},
	}).Err())
	sink := &healthReportSink{}
	monitor := NewUsageQueueHealthMonitor(sink, reporter, "sg", "sg-1", time.Second, 1000, 1)
	monitor.reportOnce(ctx)

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "sg-1", report.GatewayCode)
	require.Equal(t, "sg-1", report.Region)
	require.Equal(t, "healthy", report.HealthStatus)
	require.Equal(t, "critical", report.UsageQueueStatus)
	require.EqualValues(t, 1, report.UsageQueueDeadCount)
}

func TestUsageQueueHealthMonitorDoesNotWarnOnStreamLengthOnly(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	reporter := NewRedisUsageReporter(client, "monitor-len", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	for i := 0; i < 3; i++ {
		require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{RequestID: fmt.Sprintf("req-len-%d", i), UserID: int64(i + 1)}))
	}
	sink := &healthReportSink{}
	monitor := NewUsageQueueHealthMonitor(sink, reporter, "sg", "sg-1", time.Second, 3, 1)
	monitor.reportOnce(ctx)

	report, ok := sink.lastReport()
	require.True(t, ok)
	require.Equal(t, "healthy", report.UsageQueueStatus)
	require.EqualValues(t, 3, report.UsageQueueStreamLength)
	require.EqualValues(t, 0, report.UsageQueuePendingCount)
}

func TestRedisUsageReporterAdjustsLocalCommittedQuota(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "test")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req1",
		KeyID:              "key1",
		UserID:             42,
		Region:             "sg",
		LeaseID:            "lease1",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req1", ActualCents: 1}))

	reporter := NewRedisUsageReporter(client, "test", usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		return nil
	}))
	require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{RequestID: "req1", ActualCents: 7}))

	fields, err := client.HGetAll(ctx, "test:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 7, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisUsageReporterAtomicallyCommitsAndQueuesResolvedCorrection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "atomic")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 100,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID:          "req-atomic",
		KeyID:              "key1",
		UserID:             42,
		Region:             "sg",
		LeaseID:            "lease1",
		EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)
	// 模拟流式用量先于 HTTP 中间件结束到达；最终入队必须携带 9 分，
	// 不能被最初的一分钱预估覆盖。
	require.NoError(t, quota.AdjustCommitted(ctx, UsageCommit{RequestID: "req-atomic", ActualCents: 9}))

	reported := make(chan UsageEvent, 1)
	reporter := NewRedisUsageReporter(client, "atomic", usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		reported <- event
		return nil
	}))
	reporter.Start(ctx)
	actual, err := reporter.CommitAndReportUsage(ctx, quota, UsageEvent{
		RequestID:   "req-atomic",
		UserID:      42,
		ActualCents: 1,
	})
	require.NoError(t, err)
	require.EqualValues(t, 9, actual)

	select {
	case event := <-reported:
		require.EqualValues(t, 9, event.ActualCents)
	case <-time.After(2 * time.Second):
		t.Fatal("expected atomically queued usage report")
	}
	fields, err := client.HGetAll(ctx, "atomic:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 9, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisUsageReporterBatchesConcurrentAtomicCommitsWithoutLosingUsage(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	quota := NewRedisQuota(client, "batch-atomic")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID:        "lease1",
		UserID:         42,
		Region:         "sg",
		AllocatedCents: 1000,
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
	}))

	const requests = 128
	for i := range requests {
		ok, err := quota.Reserve(ctx, ReserveRequest{
			RequestID:          fmt.Sprintf("req-batch-atomic-%d", i),
			KeyID:              "key1",
			UserID:             42,
			Region:             "sg",
			LeaseID:            "lease1",
			EstimatedCostCents: 1,
		})
		require.NoError(t, err)
		require.True(t, ok)
	}
	reporter := NewRedisUsageReporter(client, "batch-atomic", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	require.Same(t, reporter, reporter.WithPrefix("batch-atomic"))

	errs := make(chan error, requests)
	var wg sync.WaitGroup
	wg.Add(requests)
	for i := range requests {
		go func(index int) {
			defer wg.Done()
			actual, err := reporter.CommitAndReportUsage(ctx, quota, UsageEvent{
				RequestID:   fmt.Sprintf("req-batch-atomic-%d", index),
				UserID:      42,
				ActualCents: 1,
			})
			if err == nil && actual != 1 {
				err = fmt.Errorf("actual cents = %d, want 1", actual)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	streamLength, err := client.XLen(ctx, "batch-atomic:usage:stream").Result()
	require.NoError(t, err)
	require.EqualValues(t, requests, streamLength)
	fields, err := client.HGetAll(ctx, "batch-atomic:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 0, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, requests, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisUsageReporterFallsBackWithoutRedis(t *testing.T) {
	called := false
	reporter := NewRedisUsageReporter(nil, "test", usageSinkFunc(func(_ context.Context, event UsageEvent) error {
		called = event.RequestID == "req1"
		return nil
	}))

	require.NoError(t, reporter.ReportUsage(context.Background(), UsageEvent{RequestID: "req1"}))
	require.True(t, called)
}

func TestRedisUsageReporterCorrectionSignalsRefillWhileReserved(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "correction-reserved")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-reserved", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 80, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-reserved", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-reserved", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	reporter := NewRedisUsageReporter(client, "correction-reserved", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	triggered := make(chan leaseRefillSignal, 1)
	reporter.postUsageLeaseRefill = func(_ *RedisQuota, signal leaseRefillSignal, _ string) { triggered <- signal }
	require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{
		RequestID: "req-reserved", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	}))

	select {
	case signal := <-triggered:
		require.True(t, signal.Needed)
		require.EqualValues(t, 10, signal.Lease.ReservedCents)
		require.EqualValues(t, 80, signal.Lease.SpentCents)
		require.EqualValues(t, 10, signal.Lease.AvailableCents())
	case <-time.After(time.Second):
		t.Fatal("expected reserved correction to signal lease refill")
	}
}

func TestRedisUsageReporterCorrectionSignalsRefillAfterCommit(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "correction-committed")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-committed", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 80, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-committed", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-committed", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req-committed", ActualCents: 1}))

	reporter := NewRedisUsageReporter(client, "correction-committed", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	triggered := make(chan leaseRefillSignal, 1)
	reporter.postUsageLeaseRefill = func(_ *RedisQuota, signal leaseRefillSignal, _ string) { triggered <- signal }
	require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{
		RequestID: "req-committed", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	}))

	select {
	case signal := <-triggered:
		require.True(t, signal.Needed)
		require.Zero(t, signal.Lease.ReservedCents)
		require.EqualValues(t, 90, signal.Lease.SpentCents)
		require.EqualValues(t, 10, signal.Lease.AvailableCents())
	case <-time.After(time.Second):
		t.Fatal("expected committed correction to signal lease refill")
	}
}

func TestRedisUsageReporterNonLowCorrectionDoesNotTriggerRefill(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "correction-not-low")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-not-low", UserID: 42, Region: "sg",
		AllocatedCents: 100, Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(),
		TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-not-low", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-not-low", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, quota.Commit(ctx, UsageCommit{RequestID: "req-not-low", ActualCents: 1}))

	reporter := NewRedisUsageReporter(client, "correction-not-low", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	var triggerCount atomic.Int32
	reporter.postUsageLeaseRefill = func(*RedisQuota, leaseRefillSignal, string) { triggerCount.Add(1) }
	require.NoError(t, reporter.ReportUsage(ctx, UsageEvent{
		RequestID: "req-not-low", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	}))
	require.Zero(t, triggerCount.Load())
}

func TestRedisUsageReporterAtomicCommitSignalsRefill(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "commit-low")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-commit-low", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 80, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-commit-low", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-commit-low", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	reporter := NewRedisUsageReporter(client, "commit-low", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	triggered := make(chan leaseRefillSignal, 1)
	reporter.postUsageLeaseRefill = func(_ *RedisQuota, signal leaseRefillSignal, _ string) { triggered <- signal }
	actual, err := reporter.CommitAndReportUsage(ctx, quota, UsageEvent{
		RequestID: "req-commit-low", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	})
	require.NoError(t, err)
	require.EqualValues(t, 10, actual)

	select {
	case signal := <-triggered:
		require.EqualValues(t, 90, signal.Lease.SpentCents)
		require.EqualValues(t, 10, signal.Lease.AvailableCents())
	case <-time.After(time.Second):
		t.Fatal("expected atomic commit to signal lease refill")
	}
}

func TestRedisUsageReporterCorrectionRetrySignalsRefillAfterQueueFailure(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "correction-retry")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-correction-retry", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 80, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-correction-retry", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-correction-retry", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	reporter := NewRedisUsageReporter(client, "correction-retry", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	var triggerCount atomic.Int32
	reporter.postUsageLeaseRefill = func(*RedisQuota, leaseRefillSignal, string) { triggerCount.Add(1) }
	// A wrong-type stream must be rejected before the correction changes either
	// the request record or its lease. Redis Lua does not roll back earlier
	// writes after a runtime error, so this preflight is part of the money-safe
	// boundary rather than just a nicer error message.
	require.NoError(t, client.Set(ctx, reporter.streamKey(), "wrong-type", 0).Err())
	event := UsageEvent{
		RequestID: "req-correction-retry", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	}
	require.Error(t, reporter.ReportUsage(ctx, event))
	require.Zero(t, triggerCount.Load())
	fields, err := client.HGetAll(ctx, "correction-retry:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 80, ParseInt64Field(fields, "spent_cents"))
	request, err := client.HGetAll(ctx, "correction-retry:req:req-correction-retry").Result()
	require.NoError(t, err)
	require.Equal(t, "reserved", request["status"])
	require.Equal(t, "1", request["estimated_cents"])
	require.Empty(t, request["corrected_actual_cents"])

	require.NoError(t, client.Del(ctx, reporter.streamKey()).Err())
	require.NoError(t, reporter.ReportUsage(ctx, event))
	require.EqualValues(t, 1, triggerCount.Load())

	fields, err = client.HGetAll(ctx, "correction-retry:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 10, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 80, ParseInt64Field(fields, "spent_cents"))
	request, err = client.HGetAll(ctx, "correction-retry:req:req-correction-retry").Result()
	require.NoError(t, err)
	require.Equal(t, "10", request["estimated_cents"])
	require.Equal(t, "10", request["corrected_actual_cents"])
	require.EqualValues(t, 1, mustStreamLength(t, client, reporter.streamKey()))
}

func TestRedisUsageReporterCommittedRetryStillSignalsLowLease(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "commit-retry")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-commit-retry", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 80, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-commit-retry", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-commit-retry", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	reporter := NewRedisUsageReporter(client, "commit-retry", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	// Simulate Redis succeeding immediately before the process exits: the first
	// process never gets to schedule the returned refill signal.
	reporter.postUsageLeaseRefill = nil
	event := UsageEvent{
		RequestID: "req-commit-retry", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	}
	actual, err := reporter.CommitAndReportUsage(ctx, quota, event)
	require.NoError(t, err)
	require.EqualValues(t, 10, actual)

	retriedReporter := NewRedisUsageReporter(client, "commit-retry", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	triggered := make(chan leaseRefillSignal, 1)
	retriedReporter.postUsageLeaseRefill = func(_ *RedisQuota, signal leaseRefillSignal, _ string) { triggered <- signal }
	actual, err = retriedReporter.CommitAndReportUsage(ctx, quota, event)
	require.NoError(t, err)
	require.EqualValues(t, 10, actual)
	select {
	case signal := <-triggered:
		require.True(t, signal.Needed)
		require.EqualValues(t, 90, signal.Lease.SpentCents)
		require.EqualValues(t, 10, signal.Lease.AvailableCents())
	case <-time.After(time.Second):
		t.Fatal("expected committed retry to signal lease refill")
	}

	fields, err := client.HGetAll(ctx, "commit-retry:lease:42:sg").Result()
	require.NoError(t, err)
	require.Zero(t, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 90, ParseInt64Field(fields, "spent_cents"))
}

func TestRedisUsageReporterAtomicQueueFailureRetrySignalsRefill(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "commit-queue-retry")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-commit-queue-retry", UserID: 42, Region: "sg",
		AllocatedCents: 100, SpentCents: 80, Version: 1,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 100, RefillAtCents: 10,
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-commit-queue-retry", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-commit-queue-retry", EstimatedCostCents: 1,
	})
	require.NoError(t, err)
	require.True(t, ok)

	reporter := NewRedisUsageReporter(client, "commit-queue-retry", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	var triggerCount atomic.Int32
	reporter.postUsageLeaseRefill = func(*RedisQuota, leaseRefillSignal, string) { triggerCount.Add(1) }
	require.NoError(t, client.Set(ctx, reporter.streamKey(), "wrong-type", 0).Err())
	event := UsageEvent{
		RequestID: "req-commit-queue-retry", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 10,
	}
	_, err = reporter.CommitAndReportUsage(ctx, quota, event)
	require.Error(t, err)
	require.Zero(t, triggerCount.Load())
	// The stream type is validated before financial state changes. A broken
	// outbox must leave the reservation untouched so a retry can atomically
	// commit quota and enqueue the event together.
	fields, err := client.HGetAll(ctx, "commit-queue-retry:lease:42:sg").Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 80, ParseInt64Field(fields, "spent_cents"))

	require.NoError(t, client.Del(ctx, reporter.streamKey()).Err())
	actual, err := reporter.CommitAndReportUsage(ctx, quota, event)
	require.NoError(t, err)
	require.EqualValues(t, 10, actual)
	require.EqualValues(t, 1, triggerCount.Load())
	fields, err = client.HGetAll(ctx, "commit-queue-retry:lease:42:sg").Result()
	require.NoError(t, err)
	require.Zero(t, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 90, ParseInt64Field(fields, "spent_cents"))
	require.EqualValues(t, 1, mustStreamLength(t, client, reporter.streamKey()))
}

func TestRedisUsageReporterStaleRequestQueuesUsageWithoutMutatingReplacementLease(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()

	quota := NewRedisQuota(client, "stale-usage")
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-old", UserID: 42, Region: "sg", AllocatedCents: 100,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))
	ok, err := quota.Reserve(ctx, ReserveRequest{
		RequestID: "req-stale", KeyID: "key", UserID: 42, Region: "sg",
		LeaseID: "lease-old", EstimatedCostCents: 10,
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, client.Del(ctx, "stale-usage:lease:42:sg").Err())
	require.NoError(t, quota.EnsureLease(ctx, LeaseSnapshot{
		LeaseID: "lease-new", UserID: 42, Region: "sg", AllocatedCents: 200,
		ReservedCents: 3, SpentCents: 5, Version: 2,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}))

	reporter := NewRedisUsageReporter(client, "stale-usage", usageSinkFunc(func(context.Context, UsageEvent) error { return nil }))
	var triggerCount atomic.Int32
	reporter.postUsageLeaseRefill = func(*RedisQuota, leaseRefillSignal, string) { triggerCount.Add(1) }
	actual, err := reporter.CommitAndReportUsage(ctx, quota, UsageEvent{
		RequestID: "req-stale", UserID: 42, Region: "sg", GatewayID: "sg", ActualCents: 7,
	})
	require.NoError(t, err)
	require.EqualValues(t, 7, actual)
	require.Zero(t, triggerCount.Load())
	require.EqualValues(t, 1, mustStreamLength(t, client, reporter.streamKey()))

	fields, err := client.HGetAll(ctx, "stale-usage:lease:42:sg").Result()
	require.NoError(t, err)
	require.Equal(t, "lease-new", fields["lease_id"])
	require.EqualValues(t, 3, ParseInt64Field(fields, "reserved_cents"))
	require.EqualValues(t, 5, ParseInt64Field(fields, "spent_cents"))
	request, err := client.HGetAll(ctx, "stale-usage:req:req-stale").Result()
	require.NoError(t, err)
	require.Equal(t, "committed", request["status"])
	require.Equal(t, "7", request["actual_cents"])
}

func mustStreamLength(t *testing.T, client *redis.Client, stream string) int64 {
	t.Helper()
	length, err := client.XLen(context.Background(), stream).Result()
	require.NoError(t, err)
	return length
}
