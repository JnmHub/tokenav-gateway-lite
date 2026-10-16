//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestImageTaskRealRedisReservationSettlementReleaseAndCrashRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcredis.Run(ctx, "redis:8.4-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = container.Terminate(cleanupCtx)
	})
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%d", host, port.Int()), PoolSize: 32})
	require.NoError(t, client.Ping(ctx).Err())
	t.Cleanup(func() { _ = client.Close() })

	prefix := fmt.Sprintf("image-task-real-%d", time.Now().UnixNano())
	if runtimeRef := gatewaylite.DefaultRuntimeConfigRef(); runtimeRef != nil {
		before := runtimeRef.Current()
		after := before
		after.RedisPrefix = prefix
		runtimeRef.Set(after)
		t.Cleanup(func() { runtimeRef.Set(before) })
	}
	quota := gatewaylite.NewRedisQuota(client, prefix)
	oldQuota := gatewaylite.DefaultRedisQuota()
	gatewaylite.SetDefaultRedisQuota(quota)
	t.Cleanup(func() {
		if oldQuota != nil {
			gatewaylite.SetDefaultRedisQuota(oldQuota)
		} else {
			gatewaylite.SetDefaultRedisQuota(gatewaylite.NewRedisQuota(nil, ""))
		}
	})

	sink := &imageTaskUsageSink{}
	reporter := gatewaylite.NewRedisUsageReporter(client, prefix, sink)
	oldReporter := gatewaylite.DefaultUsageReporter()
	gatewaylite.SetDefaultUsageReporter(reporter)
	t.Cleanup(func() { gatewaylite.SetDefaultUsageReporter(oldReporter) })
	reporterCtx, stopReporter := context.WithCancel(context.Background())
	t.Cleanup(stopReporter)
	reporter.StartWorkersWithBatchSize(reporterCtx, 1, 10)

	const (
		userID   = int64(8101)
		apiKeyID = int64(8201)
		keyID    = "image-key-real"
		region   = "image-test"
		leaseID  = "lease-image-real"
	)
	require.NoError(t, quota.EnsureLease(ctx, gatewaylite.LeaseSnapshot{
		LeaseID: leaseID, UserID: userID, Region: region, AllocatedCents: 1000,
		Version: 1, ExpiresAt: time.Now().Add(time.Hour).Unix(), TargetCents: 1000, RefillAtCents: 100,
	}))

	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	cfg.MaxAttempts = 2
	billing := GatewayImageTaskBilling{}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}}

	newTask := func(t *testing.T, requestID string, executor ImageTaskExecutor) (*ImageTaskService, *imageTaskMemoryStore, *ImageTask) {
		t.Helper()
		before, ok, loadErr := quota.LoadLease(ctx, userID, region)
		require.NoError(t, loadErr)
		require.True(t, ok)
		reserved, reserveErr := quota.Reserve(ctx, gatewaylite.ReserveRequest{
			RequestID: requestID, KeyID: keyID, UserID: userID, Region: region,
			LeaseID: leaseID, EstimatedCostCents: 1,
		})
		require.NoError(t, reserveErr)
		require.True(t, reserved)
		reservation := gatewaylite.NewRequestReservation(1, leaseID)
		requestCtx := gatewaylite.ContextWithRequestMeta(ctx, gatewaylite.RequestMeta{
			RequestID: requestID, KeyID: keyID, UserID: userID, LeaseID: leaseID,
			Region: region, GatewayID: "gw-image-real", RateMultiplier: 1,
			Reservation: reservation, AsyncSettlement: true,
			BillingSettlementMode: gatewaylite.BillingSettlementModeIdempotentV2,
		})
		store := &imageTaskMemoryStore{}
		svc, serviceErr := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
		require.NoError(t, serviceErr)
		task, replayed, submitErr := svc.Submit(requestCtx, ImageTaskCreateInput{
			UserID: userID, APIKeyID: apiKeyID, GatewayKeyID: keyID,
			Model: "gpt-image-1", Endpoint: "/v1/images/generations", ContentType: "application/json",
			ImageCount: 1, RequestPayload: []byte(`{"model":"gpt-image-1","prompt":"integration"}`),
			MaxReservedCents: 100,
		})
		require.NoError(t, submitErr)
		require.False(t, replayed)
		lease, ok, loadErr := quota.LoadLease(ctx, userID, region)
		require.NoError(t, loadErr)
		require.True(t, ok)
		require.EqualValues(t, before.ReservedCents+100, lease.ReservedCents, "submission must expand and hold the frozen maximum")
		return svc, store, task
	}

	t.Run("success settles actual amount once", func(t *testing.T) {
		executor := &imageTaskRedisExecutor{actualCents: 30}
		svc, _, task := newTask(t, "image-success", executor)
		svc.process("worker-success", task)
		require.Equal(t, ImageTaskStateSucceeded, task.State)
		require.Equal(t, ImageTaskSettlementSettled, task.SettlementState)
		require.Equal(t, 1, executor.executeCalls)
		require.Equal(t, 1, executor.settleCalls)
		// A recovered worker seeing the settled row must not regenerate or bill.
		svc.process("worker-success-retry", task)
		require.Equal(t, 1, executor.executeCalls)
		require.Equal(t, 1, executor.settleCalls)
	})

	t.Run("failed execution releases the full reservation idempotently", func(t *testing.T) {
		executor := &imageTaskRedisExecutor{executeErr: errors.New("upstream unavailable")}
		svc, _, task := newTask(t, "image-failed", executor)
		svc.process("worker-failed", task)
		require.Equal(t, ImageTaskStateFailed, task.State)
		require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
		require.Equal(t, 1, executor.executeCalls)
		require.NoError(t, billing.Release(ctx, task))
	})

	t.Run("queued cancellation releases without dispatch", func(t *testing.T) {
		executor := &imageTaskRedisExecutor{}
		svc, _, task := newTask(t, "image-cancelled", executor)
		cancelled, cancelErr := svc.CancelForUser(ctx, task.ID, userID)
		require.NoError(t, cancelErr)
		require.Equal(t, ImageTaskStateCancelled, cancelled.State)
		require.Equal(t, ImageTaskSettlementReleased, cancelled.SettlementState)
		require.Zero(t, executor.executeCalls)
		_, cancelErr = svc.CancelForUser(ctx, task.ID, userID)
		require.NoError(t, cancelErr)
	})

	t.Run("dispatched crash recovery never calls upstream twice", func(t *testing.T) {
		executor := &imageTaskRedisExecutor{}
		svc, _, task := newTask(t, "image-crash-recovery", executor)
		task.State = ImageTaskStateRunning
		task.ExecutionPhase = ImageTaskExecutionPhaseDispatched
		task.Attempt = 1
		svc.process("worker-after-restart", task)
		require.Equal(t, ImageTaskStateFailed, task.State)
		require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
		require.Zero(t, executor.executeCalls)
		svc.process("worker-after-second-restart", task)
		require.Zero(t, executor.executeCalls)
	})

	require.Eventually(t, func() bool { return sink.Count() == 4 }, 10*time.Second, 25*time.Millisecond)
	lease, ok, err := quota.LoadLease(ctx, userID, region)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, lease.ReservedCents, "all image-task holds must be financially final")
	require.EqualValues(t, 30, lease.SpentCents)
	require.EqualValues(t, lease.SpentCents, sink.ActualCents(), "Redis quota and reported ledger amount must have zero difference")
	stats, err := reporter.UsageQueueStats(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.PendingCount)
	require.Zero(t, stats.DeadCount)
}

type imageTaskRedisExecutor struct {
	executeErr   error
	actualCents  int64
	executeCalls int
	settleCalls  int
}

func (e *imageTaskRedisExecutor) Execute(context.Context, *ImageTask, []byte) (ImageTaskExecutionResult, error) {
	e.executeCalls++
	if e.executeErr != nil {
		return ImageTaskExecutionResult{}, e.executeErr
	}
	return ImageTaskExecutionResult{
		Artifacts:   []ImageTaskExecutionArtifact{{FileName: "image.png", ContentType: "image/png", Reader: io.NopCloser(stringsReader("png"))}},
		ActualCents: e.actualCents,
	}, nil
}

func (e *imageTaskRedisExecutor) Settle(ctx context.Context, task *ImageTask) error {
	e.settleCalls++
	usageCtx := task.UsageContext(ctx)
	if err := gatewaylite.EnsureCommittedUsageCapacity(usageCtx, task.ActualCents); err != nil {
		return err
	}
	return gatewaylite.ReportUsageCorrection(usageCtx, gatewaylite.UsageEvent{
		RequestID: task.BillingRequestID(), QuotaRequestID: task.QuotaRequestID,
		UserID: task.UserID, KeyID: task.GatewayKeyID, LeaseID: task.LeaseID,
		Region: task.Region, GatewayID: task.GatewayID, Protocol: "openai_compatible",
		Model: task.Model, Method: "POST", Path: task.Endpoint, Status: 200,
		BillingMode: string(BillingModeImage), ImageCount: int64(task.ImageCount),
		EstimatedCents: task.ReservedCents, ActualCents: task.ActualCents,
		SettlementPending: true, SettlementMode: string(gatewaylite.BillingSettlementModeIdempotentV2),
		SettlementMicroCents: task.ActualCents * 1_000_000,
		StartedAtMillis:      task.CreatedAt.UnixMilli(), EndedAtMillis: time.Now().UTC().UnixMilli(),
	})
}

type imageTaskUsageSink struct {
	mu     sync.Mutex
	events []gatewaylite.UsageEvent
}

func (s *imageTaskUsageSink) ReportUsage(_ context.Context, event gatewaylite.UsageEvent) error {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	return nil
}

func (s *imageTaskUsageSink) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func (s *imageTaskUsageSink) ActualCents() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, event := range s.events {
		total += event.ActualCents
	}
	return total
}

type imageTaskStringReader struct{ value string }

func stringsReader(value string) io.Reader { return &imageTaskStringReader{value: value} }

func (r *imageTaskStringReader) Read(buffer []byte) (int, error) {
	if r.value == "" {
		return 0, io.EOF
	}
	n := copy(buffer, r.value)
	r.value = r.value[n:]
	return n, nil
}
