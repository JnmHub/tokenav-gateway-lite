package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestImageTaskSuccessfulUpstreamRetriesOnlySettlement(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	store := &imageTaskMemoryStore{}
	billing := &imageTaskBillingStub{}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}}
	executor := &imageTaskExecutorStub{settleFailures: 1}
	svc, err := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	now := time.Now().UTC()
	task := &ImageTask{
		ID: "6d802aa8-6ab2-42aa-b903-069ab7c5be54", UserID: 7, APIKeyID: 8,
		Model: "gpt-image-1", Endpoint: "/v1/images/generations", ContentType: "application/json",
		ImageCount: 1, EncryptedRequest: `{"model":"gpt-image-1","prompt":"test"}`,
		State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, task.EncryptedRequest)
	store.task = task

	svc.process("worker-1", task)
	require.Equal(t, 1, executor.executeCalls, "the upstream must be called exactly once")
	require.Equal(t, 1, executor.settleCalls)
	require.Equal(t, ImageTaskStateSucceeded, task.State)
	require.Equal(t, ImageTaskSettlementPending, task.SettlementState)
	require.Equal(t, "settlement_retry", task.ErrorCode)
	require.Len(t, task.Artifacts, 1)

	// Simulate the next database claim after the settlement backoff. A
	// succeeded task enters finishSettlement directly and must never Execute.
	svc.process("worker-2", task)
	require.Equal(t, 1, executor.executeCalls)
	require.Equal(t, 2, executor.settleCalls)
	require.Equal(t, ImageTaskSettlementSettled, task.SettlementState)
	require.True(t, task.ExecutorSettled)
	require.Empty(t, task.ErrorCode)
}

func TestImageTaskCancelQueuedReleasesReservationOnce(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "97e26f4a-dacd-46e3-91f1-da53d122066d", UserID: 10, APIKeyID: 11,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, task.EncryptedRequest)
	store := &imageTaskMemoryStore{task: task}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	got, err := svc.Cancel(context.Background(), task.ID, task.UserID, task.APIKeyID, "")
	require.NoError(t, err)
	require.Equal(t, ImageTaskStateCancelled, got.State)
	require.Equal(t, ImageTaskSettlementReleased, got.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)

	got, err = svc.Cancel(context.Background(), task.ID, task.UserID, task.APIKeyID, "")
	require.NoError(t, err)
	require.Equal(t, ImageTaskSettlementReleased, got.SettlementState)
	require.Equal(t, 1, billing.releaseCalls, "repeat cancel must not release twice")
}

func TestImageTaskSubmitReplaysBeforeQuotaOrCapacityChecks(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	payload := []byte(`{"model":"gpt-image-1","prompt":"same"}`)
	digest := sha256.Sum256(payload)
	existing := &ImageTask{
		ID: "1a759231-8807-4542-898f-4758c4790c60", IdempotencyKey: "request-1",
		PayloadHash: hex.EncodeToString(digest[:]), UserID: 21, APIKeyID: 22,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
	}
	store := &imageTaskMemoryStore{task: existing}
	billing := &imageTaskBillingStub{reserveErr: errors.New("balance changed after original submission")}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	got, replayed, err := svc.Submit(context.Background(), ImageTaskCreateInput{
		IdempotencyKey: "request-1", UserID: 21, APIKeyID: 22, Model: "gpt-image-1",
		ImageCount: 1, RequestPayload: payload, MaxReservedCents: 50,
	})
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, existing.ID, got.ID)
	require.Zero(t, billing.reserveCalls, "an idempotent read must not create another reservation")

	_, _, err = svc.Submit(context.Background(), ImageTaskCreateInput{
		IdempotencyKey: "request-1", UserID: 21, APIKeyID: 22, Model: "gpt-image-1",
		ImageCount: 1, RequestPayload: []byte(`{"model":"gpt-image-1","prompt":"different"}`), MaxReservedCents: 50,
	})
	require.ErrorIs(t, err, ErrImageTaskConflict)
	require.Zero(t, billing.reserveCalls)
}

func TestImageTaskSubmitRejectsOversizedEncryptedRowsBeforeReservation(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	cfg.MaxRequestBytes = 16 << 10
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(&imageTaskMemoryStore{}, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	_, _, err = svc.Submit(context.Background(), ImageTaskCreateInput{
		UserID: 25, APIKeyID: 26, Model: "gpt-image-1", ImageCount: 1,
		RequestPayload: bytes.Repeat([]byte("x"), int(cfg.MaxRequestBytes)+1), MaxReservedCents: 10,
	})
	require.ErrorContains(t, err, "request exceeds")
	require.Zero(t, billing.reserveCalls)
}

func TestImageTaskSubmitRecoversCommittedCreateAcknowledgementLossAndRestartReplay(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	store := &imageTaskCreateAmbiguityStore{imageTaskMemoryStore: &imageTaskMemoryStore{}, commitBeforeError: true}
	billing := &imageTaskBillingStub{}
	newService := func() *ImageTaskService {
		svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
		require.NoError(t, err)
		return svc
	}
	input := ImageTaskCreateInput{
		IdempotencyKey: "commit-ack-lost", UserID: 27, APIKeyID: 28, Model: "gpt-image-1",
		ImageCount: 1, RequestPayload: []byte(`{"model":"gpt-image-1","prompt":"recover"}`), MaxReservedCents: 50,
	}
	created, replayed, err := newService().Submit(context.Background(), input)
	require.NoError(t, err)
	require.False(t, replayed)
	require.NotNil(t, created)
	require.Equal(t, 1, billing.reserveCalls)
	require.Zero(t, billing.releaseCalls, "a committed task must retain its reservation")

	// A process restart and same-key client retry read the durable task before
	// quota/capacity checks and therefore neither reserve nor release again.
	restarted, replayed, err := newService().Submit(context.Background(), input)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, created.ID, restarted.ID)
	require.Equal(t, 1, billing.reserveCalls)
	require.Zero(t, billing.releaseCalls)
}

func TestImageTaskSubmitUncertainCreateReadFailureKeepsReservation(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	store := &imageTaskCreateAmbiguityStore{
		imageTaskMemoryStore: &imageTaskMemoryStore{},
		lookupErr:            errors.New("primary read unavailable"),
	}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	_, _, err = svc.Submit(context.Background(), ImageTaskCreateInput{
		IdempotencyKey: "uncertain-create", UserID: 29, APIKeyID: 30, Model: "gpt-image-1",
		ImageCount: 1, RequestPayload: []byte(`{"model":"gpt-image-1","prompt":"hold"}`), MaxReservedCents: 50,
	})
	require.ErrorIs(t, err, ErrImageTaskPersistenceUncertain)
	require.Equal(t, 1, billing.reserveCalls)
	require.Zero(t, billing.releaseCalls, "an uncertain primary read must fail closed")
}

func TestImageTaskSubmitConfirmedCreateRollbackReleasesReservation(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	store := &imageTaskCreateAmbiguityStore{imageTaskMemoryStore: &imageTaskMemoryStore{}}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	_, _, err = svc.Submit(context.Background(), ImageTaskCreateInput{
		IdempotencyKey: "confirmed-rollback", UserID: 31, APIKeyID: 32, Model: "gpt-image-1",
		ImageCount: 1, RequestPayload: []byte(`{"model":"gpt-image-1","prompt":"release"}`), MaxReservedCents: 50,
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrImageTaskPersistenceUncertain)
	require.Equal(t, 1, billing.releaseCalls)
}

func TestImageTaskConfigRequiresClaimSafetyBudget(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.ClaimTTL = cfg.ExecutionTimeout + 3*time.Minute - time.Millisecond
	require.ErrorContains(t, cfg.Validate(), "at least 3m")
	cfg.ClaimTTL = cfg.ExecutionTimeout + 3*time.Minute
	require.NoError(t, cfg.Validate())
}

func TestImageTaskTerminalReleaseRetryNeverRegeneratesImage(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "31189771-7587-4135-a4c5-39ab4b570196", UserID: 31, APIKeyID: 32,
		Model: "gpt-image-1", State: ImageTaskStateFailed, SettlementState: ImageTaskSettlementPending,
		ErrorCode: "release_retry", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	store := &imageTaskMemoryStore{task: task}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-recovery", task)
	require.Zero(t, executor.executeCalls, "a terminal task may only retry its financial release")
	require.Equal(t, 1, billing.releaseCalls)
	require.Equal(t, ImageTaskStateFailed, task.State)
	require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
}

func TestImageTaskSuccessfulSettlementIsNotAppliedTwice(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "a8f68b74-731f-47ae-b319-bf498c2da8bf", UserID: 41, APIKeyID: 42,
		Model: "gpt-image-1", EncryptedRequest: `{}`, State: ImageTaskStateQueued,
		SettlementState: ImageTaskSettlementPending, MaxAttempts: 3, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, task.EncryptedRequest)
	store := &imageTaskMemoryStore{task: task}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, &imageTaskBillingStub{}, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-one", task)
	svc.process("worker-two", task)
	require.Equal(t, 1, executor.executeCalls)
	require.Equal(t, 1, executor.settleCalls)
	require.Equal(t, ImageTaskSettlementSettled, task.SettlementState)
}

func TestImageTaskStopCancelsWorkerWithoutMakingDispatchedTaskRetryable(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	cfg.Workers = 1
	cfg.BatchSize = 1
	cfg.PollInterval = 100 * time.Millisecond
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "36bea515-609b-4b81-a32e-037324e77c37", UserID: 51, APIKeyID: 52,
		Model: "gpt-image-1", EncryptedRequest: `{}`, State: ImageTaskStateQueued,
		SettlementState: ImageTaskSettlementPending, MaxAttempts: 3, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, task.EncryptedRequest)
	store := &imageTaskMemoryStore{task: task, claimEnabled: true}
	executor := &blockingImageTaskExecutor{started: make(chan struct{})}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)
	svc.Start()
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("image worker did not start")
	}
	stopped := make(chan struct{})
	go func() { svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("image worker shutdown waited for the full upstream timeout")
	}
	require.Equal(t, ImageTaskStateFailed, task.State)
	require.Equal(t, ImageTaskExecutionPhaseDispatched, task.ExecutionPhase)
	require.Equal(t, "execution_outcome_unknown", task.ErrorCode)
	require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)
	require.Equal(t, 1, executor.executeCalls)
}

func TestImageTaskWorkerNeverPreclaimsAStaleBatch(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	cfg.BatchSize = 16
	store := &imageTaskMemoryStore{}
	svc, err := NewImageTaskService(store, &imageTaskBillingStub{}, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.runBatch("worker-single-claim")
	require.Equal(t, []int{1}, store.claimLimits, "a worker must claim the next task only after the current one finishes")
}

func TestImageTaskRecoveredDispatchedRunningTaskNeverExecutesAgain(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "8ff3f097-196a-44f8-b37d-f682011672bd", UserID: 53, APIKeyID: 54,
		Model: "gpt-image-1", State: ImageTaskStateRunning, ExecutionPhase: ImageTaskExecutionPhaseDispatched,
		SettlementState: ImageTaskSettlementPending, Attempt: 1, MaxAttempts: 3,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	store := &imageTaskMemoryStore{task: task}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-recovered", task)
	require.Zero(t, executor.executeCalls)
	require.Equal(t, ImageTaskStateFailed, task.State)
	require.Equal(t, "execution_outcome_unknown", task.ErrorCode)
	require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)
}

func TestImageTaskObjectPersistenceFailureNeverRegenerates(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "b314cb0f-6734-4a62-9c74-6e7b2ca9b49e", UserID: 55, APIKeyID: 56,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, `{"model":"gpt-image-1","prompt":"store once"}`)
	store := &imageTaskMemoryStore{task: task}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}, putFailures: 1}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-store-failure", task)
	svc.process("worker-recovery", task)
	require.Equal(t, 1, executor.executeCalls)
	require.Equal(t, ImageTaskExecutionPhaseDispatched, executor.executionPhaseAtRun)
	require.Equal(t, ImageTaskStateFailed, task.State)
	require.Equal(t, "object_persist_failed", task.ErrorCode)
	require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)
}

func TestImageTaskTerminalSaveFailureNeverRegenerates(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "95531c7b-cf5f-4661-8a08-5ddd37a441b9", UserID: 57, APIKeyID: 58,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, `{"model":"gpt-image-1","prompt":"save once"}`)
	store := &imageTaskSaveAmbiguityStore{
		imageTaskMemoryStore: &imageTaskMemoryStore{}, persisted: cloneImageTaskForTest(task), failAtCall: 2,
	}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-save-failure", task)
	failed, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, 1, executor.executeCalls)
	require.Equal(t, ImageTaskStateFailed, failed.State)
	require.Equal(t, "object_persist_failed", failed.ErrorCode)
	require.Equal(t, ImageTaskSettlementReleased, failed.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)
	require.Empty(t, objects.objects, "a terminal-row failure must not leave a downloadable orphan")

	recovered := *failed
	recovered.State = ImageTaskStateRunning
	recovered.ExecutionPhase = ImageTaskExecutionPhaseDispatched
	recovered.SettlementState = ImageTaskSettlementPending
	recovered.ClaimOwner = "worker-after-claim-expiry"
	store.persisted = &recovered
	svc.process("worker-after-claim-expiry", &recovered)
	require.Equal(t, 1, executor.executeCalls, "claim recovery must not repeat upstream generation")
}

func TestImageTaskSuccessfulSaveAcknowledgementLossKeepsObjectsAndSettles(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "41189771-7587-4135-a4c5-39ab4b570196", UserID: 35, APIKeyID: 36,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), ClaimOwner: "worker-commit",
	}
	setImageTaskTestPayload(task, `{"model":"gpt-image-1","prompt":"commit once"}`)
	store := &imageTaskSaveAmbiguityStore{
		imageTaskMemoryStore: &imageTaskMemoryStore{}, persisted: cloneImageTaskForTest(task),
		failAtCall: 2, commitBeforeError: true,
	}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-commit", task)
	persisted, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskStateSucceeded, persisted.State)
	require.Equal(t, ImageTaskSettlementSettled, persisted.SettlementState)
	require.Len(t, objects.objects, 1)
	require.Equal(t, 1, executor.executeCalls)
	require.Equal(t, 1, executor.settleCalls)
	require.Equal(t, 1, billing.settleCalls)
	require.Zero(t, billing.releaseCalls)
}

func TestImageTaskSuccessfulSaveUncertainReadPreservesUntilRestart(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "51189771-7587-4135-a4c5-39ab4b570196", UserID: 37, APIKeyID: 38,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), ClaimOwner: "worker-before-restart",
	}
	setImageTaskTestPayload(task, `{"model":"gpt-image-1","prompt":"restart"}`)
	store := &imageTaskSaveAmbiguityStore{
		imageTaskMemoryStore: &imageTaskMemoryStore{}, persisted: cloneImageTaskForTest(task),
		failAtCall: 2, commitBeforeError: true, lookupErr: errors.New("primary unavailable"),
	}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}}
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-before-restart", task)
	require.Len(t, objects.objects, 1)
	require.Zero(t, billing.settleCalls)
	require.Zero(t, billing.releaseCalls)

	store.lookupErr = nil
	recovered, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	recovered.ClaimOwner = "worker-after-restart"
	restarted, err := NewImageTaskService(store, billing, objects, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)
	restarted.process("worker-after-restart", recovered)
	final, err := store.Get(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, ImageTaskSettlementSettled, final.SettlementState)
	require.Len(t, objects.objects, 1)
	require.Equal(t, 1, executor.executeCalls, "restart must not regenerate the image")
	require.Equal(t, 1, billing.settleCalls)
	require.Zero(t, billing.releaseCalls)
}

func TestImageTaskSuccessfulSaveNotFoundReadPreservesObjectsAndReservation(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "61189771-7587-4135-a4c5-39ab4b570196", UserID: 39, APIKeyID: 40,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour), ClaimOwner: "worker-not-found",
	}
	setImageTaskTestPayload(task, `{"model":"gpt-image-1","prompt":"not found is uncertain"}`)
	store := &imageTaskSaveAmbiguityStore{
		imageTaskMemoryStore: &imageTaskMemoryStore{}, persisted: cloneImageTaskForTest(task),
		failAtCall: 2, lookupErr: ErrImageTaskNotFound,
	}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{}}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, objects, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-not-found", task)
	require.Len(t, objects.objects, 1)
	require.Zero(t, billing.settleCalls)
	require.Zero(t, billing.releaseCalls, "not-found does not prove that the success update rolled back")
}

func TestImageTaskRunningCancellationReturnsConflictWithoutRefund(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "af8ef472-28c9-4ca6-b741-22aa350c3b5d", UserID: 59, APIKeyID: 60,
		Model: "gpt-image-1", State: ImageTaskStateRunning, ExecutionPhase: ImageTaskExecutionPhaseDispatched,
		SettlementState: ImageTaskSettlementPending, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	store := &imageTaskMemoryStore{task: task}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	_, err = svc.Cancel(context.Background(), task.ID, task.UserID, task.APIKeyID, "")
	require.ErrorIs(t, err, ErrImageTaskAlreadyRunning)
	require.Equal(t, ImageTaskStateRunning, task.State)
	require.Zero(t, billing.releaseCalls)
}

func TestImageTaskRejectsSwappedCiphertextBeforeDispatch(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "d083023f-8421-4fa4-97be-eae17fdb31d6", UserID: 63, APIKeyID: 64,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, `{"prompt":"owner-a"}`)
	task.EncryptedRequest = `{"prompt":"owner-b"}`
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(&imageTaskMemoryStore{task: task}, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-integrity", task)
	require.Zero(t, executor.executeCalls)
	require.Equal(t, ImageTaskStateFailed, task.State)
	require.Equal(t, "request_integrity_failed", task.ErrorCode)
	require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
}

func TestImageTaskDecryptFailureStopsAtMaxAttemptsAndReleasesQuota(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "95885982-c5bc-4499-850f-f6c3ef65cb32", UserID: 73, APIKeyID: 74,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, `{}`)
	billing := &imageTaskBillingStub{}
	executor := &imageTaskExecutorStub{}
	svc, err := NewImageTaskService(
		&imageTaskMemoryStore{task: task}, billing,
		&imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor,
		imageTaskFailingDecryptor{}, cfg,
	)
	require.NoError(t, err)

	for attempt := 1; attempt <= task.MaxAttempts; attempt++ {
		svc.process("worker-decrypt", task)
		require.Equal(t, attempt, task.Attempt)
	}
	require.Zero(t, executor.executeCalls, "decrypt failures must never reach upstream")
	require.Equal(t, ImageTaskStateFailed, task.State)
	require.Equal(t, "request_decrypt_failed", task.ErrorCode)
	require.Equal(t, ImageTaskSettlementReleased, task.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)
}

func TestImageTaskPersistedErrorRedactsCredentialsBeforeSave(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "352a6f0d-63ce-446c-82ab-35ebdd42e570", UserID: 65, APIKeyID: 66,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	setImageTaskTestPayload(task, `{}`)
	executor := &imageTaskExecutorStub{executeErr: errors.New("Authorization: Bearer sk-supersecret api_key=abc123 https://alice:pw@example.com/path")}
	svc, err := NewImageTaskService(&imageTaskMemoryStore{task: task}, &imageTaskBillingStub{}, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, executor, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	svc.process("worker-redaction", task)
	require.NotContains(t, task.ErrorMessage, "supersecret")
	require.NotContains(t, task.ErrorMessage, "abc123")
	require.NotContains(t, task.ErrorMessage, "alice:pw")
	require.Contains(t, task.ErrorMessage, "***")
}

func TestImageTaskOwnedArtifactRejectsOtherUsers(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "55945062-6a0a-45bf-a0f4-1281f0d66556", UserID: 61, APIKeyID: 62,
		Model: "gpt-image-1", State: ImageTaskStateSucceeded, SettlementState: ImageTaskSettlementSettled,
		Artifacts: []ImageTaskArtifact{{ObjectKey: "image-tasks/task/00", FileName: "image.png", ContentType: "image/png", SizeBytes: 3}},
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{"image-tasks/task/00": []byte("png")}}
	svc, err := NewImageTaskService(&imageTaskMemoryStore{task: task}, &imageTaskBillingStub{}, objects, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	_, _, err = svc.OpenOwnedArtifact(context.Background(), task.ID, 0, 999, task.APIKeyID, "")
	require.ErrorIs(t, err, ErrImageTaskForbidden)
	body, artifact, err := svc.OpenOwnedArtifact(context.Background(), task.ID, 0, task.UserID, task.APIKeyID, "")
	require.NoError(t, err)
	defer body.Close()
	require.Equal(t, "image.png", artifact.FileName)
}

func TestImageTaskCancelAcceptsConcurrentWorkerRelease(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "44a60c47-c2c6-45e7-8b47-0775da57a7d2", UserID: 71, APIKeyID: 72,
		Model: "gpt-image-1", State: ImageTaskStateQueued, SettlementState: ImageTaskSettlementPending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	base := &imageTaskMemoryStore{task: task}
	store := &imageTaskCancelRaceStore{imageTaskMemoryStore: base}
	billing := &imageTaskBillingStub{}
	svc, err := NewImageTaskService(store, billing, &imageTaskObjectStoreStub{objects: map[string][]byte{}}, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	got, err := svc.Cancel(context.Background(), task.ID, task.UserID, task.APIKeyID, "")
	require.NoError(t, err)
	require.Equal(t, ImageTaskSettlementReleased, got.SettlementState)
	require.Equal(t, 1, billing.releaseCalls)
}

func TestImageTaskCleanupRetriesObjectDeletionBeforeRowRemoval(t *testing.T) {
	cfg := DefaultImageTaskConfig()
	cfg.Enabled = true
	now := time.Now().UTC()
	task := &ImageTask{
		ID: "d20df079-d3d9-4988-b24b-b353579571e1", UserID: 81, APIKeyID: 82,
		Model: "gpt-image-1", State: ImageTaskStateSucceeded, SettlementState: ImageTaskSettlementSettled,
		Artifacts: []ImageTaskArtifact{{ObjectKey: "image-tasks/task/00"}},
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now, ExpiresAt: now.Add(-time.Hour),
	}
	store := &imageTaskCleanupStore{imageTaskMemoryStore: &imageTaskMemoryStore{task: task}}
	objects := &imageTaskObjectStoreStub{objects: map[string][]byte{"image-tasks/task/00": []byte("png")}, deleteFailures: 1}
	svc, err := NewImageTaskService(store, &imageTaskBillingStub{}, objects, &imageTaskExecutorStub{}, imageTaskPlainEncryptor{}, cfg)
	require.NoError(t, err)

	removed, err := svc.Cleanup(context.Background(), 10)
	require.NoError(t, err)
	require.Zero(t, removed)
	require.Zero(t, store.deleteCalls, "the task row must remain while object deletion is incomplete")

	removed, err = svc.Cleanup(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.Equal(t, 1, store.deleteCalls)
	require.Equal(t, 2, objects.deleteCalls)
}

type imageTaskPlainEncryptor struct{}

func (imageTaskPlainEncryptor) Encrypt(value string) (string, error) { return value, nil }
func (imageTaskPlainEncryptor) Decrypt(value string) (string, error) { return value, nil }

type imageTaskFailingDecryptor struct{ imageTaskPlainEncryptor }

func (imageTaskFailingDecryptor) Decrypt(string) (string, error) {
	return "", errors.New("ciphertext is corrupt")
}

func setImageTaskTestPayload(task *ImageTask, payload string) {
	task.EncryptedRequest = payload
	digest := sha256.Sum256([]byte(payload))
	task.PayloadHash = hex.EncodeToString(digest[:])
}

type imageTaskExecutorStub struct {
	executeCalls        int
	settleCalls         int
	settleFailures      int
	executeErr          error
	executionPhaseAtRun string
}

type blockingImageTaskExecutor struct {
	started      chan struct{}
	startOnce    sync.Once
	executeCalls int
}

func (e *blockingImageTaskExecutor) Execute(ctx context.Context, _ *ImageTask, _ []byte) (ImageTaskExecutionResult, error) {
	e.executeCalls++
	e.startOnce.Do(func() { close(e.started) })
	<-ctx.Done()
	return ImageTaskExecutionResult{}, ctx.Err()
}

func (e *imageTaskExecutorStub) Execute(_ context.Context, task *ImageTask, _ []byte) (ImageTaskExecutionResult, error) {
	e.executeCalls++
	e.executionPhaseAtRun = task.ExecutionPhase
	if e.executeErr != nil {
		return ImageTaskExecutionResult{}, e.executeErr
	}
	task.ExecutionSnapshot = []byte(`{"safe":true}`)
	return ImageTaskExecutionResult{
		Artifacts:   []ImageTaskExecutionArtifact{{FileName: "image.png", ContentType: "image/png", Reader: io.NopCloser(strings.NewReader("png"))}},
		ActualCents: 23, BillingSettled: false,
	}, nil
}

func (e *imageTaskExecutorStub) Settle(_ context.Context, _ *ImageTask) error {
	e.settleCalls++
	if e.settleFailures > 0 {
		e.settleFailures--
		return errors.New("temporary ledger outage")
	}
	return nil
}

type imageTaskBillingStub struct {
	reserveCalls int
	settleCalls  int
	releaseCalls int
	reserveErr   error
}

func (b *imageTaskBillingStub) Reserve(_ context.Context, task *ImageTask, cents int64) (ImageTaskReservation, error) {
	b.reserveCalls++
	if b.reserveErr != nil {
		return ImageTaskReservation{}, b.reserveErr
	}
	return ImageTaskReservation{ID: task.ID, ReservedCents: cents}, nil
}
func (b *imageTaskBillingStub) Settle(_ context.Context, _ *ImageTask, _ int64) error {
	b.settleCalls++
	return nil
}
func (b *imageTaskBillingStub) Release(_ context.Context, _ *ImageTask) error {
	b.releaseCalls++
	return nil
}

type imageTaskObjectStoreStub struct {
	mu             sync.Mutex
	objects        map[string][]byte
	putFailures    int
	deleteCalls    int
	deleteFailures int
}

func (s *imageTaskObjectStoreStub) Put(_ context.Context, key string, body io.Reader, _ string, maxBytes int64) (int64, string, error) {
	s.mu.Lock()
	if s.putFailures > 0 {
		s.putFailures--
		s.mu.Unlock()
		return 0, "", errors.New("temporary object storage failure")
	}
	s.mu.Unlock()
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return 0, "", errors.New("object too large")
	}
	s.mu.Lock()
	s.objects[key] = append([]byte(nil), data...)
	s.mu.Unlock()
	return int64(len(data)), "digest", nil
}
func (s *imageTaskObjectStoreStub) Open(_ context.Context, key string) (io.ReadCloser, string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, "", 0, ErrImageTaskNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), "image/png", int64(len(data)), nil
}
func (s *imageTaskObjectStoreStub) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	s.deleteCalls++
	if s.deleteFailures > 0 {
		s.deleteFailures--
		s.mu.Unlock()
		return errors.New("temporary object deletion failure")
	}
	delete(s.objects, key)
	s.mu.Unlock()
	return nil
}
func (s *imageTaskObjectStoreStub) SignedURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "local-image-task://" + key, nil
}

type imageTaskMemoryStore struct {
	mu                    sync.Mutex
	task                  *ImageTask
	claimEnabled          bool
	claimed               bool
	claimLimits           []int
	saveClaimedCalls      int
	failSaveClaimedAtCall int
}

type imageTaskCreateAmbiguityStore struct {
	*imageTaskMemoryStore
	commitBeforeError bool
	lookupErr         error
	createCalled      bool
}

func (s *imageTaskCreateAmbiguityStore) Create(_ context.Context, task *ImageTask, _, _ int) (*ImageTask, bool, error) {
	s.mu.Lock()
	s.createCalled = true
	if s.commitBeforeError {
		s.task = cloneImageTaskForTest(task)
	}
	s.mu.Unlock()
	return nil, false, errors.New("commit acknowledgement lost")
}

func (s *imageTaskCreateAmbiguityStore) FindByIdempotency(ctx context.Context, userID, apiKeyID int64, gatewayKeyID, idempotencyKey string) (*ImageTask, error) {
	s.mu.Lock()
	createCalled := s.createCalled
	s.mu.Unlock()
	if createCalled && s.lookupErr != nil {
		return nil, s.lookupErr
	}
	return s.imageTaskMemoryStore.FindByIdempotency(ctx, userID, apiKeyID, gatewayKeyID, idempotencyKey)
}

type imageTaskSaveAmbiguityStore struct {
	*imageTaskMemoryStore
	persisted         *ImageTask
	failAtCall        int
	commitBeforeError bool
	lookupErr         error
}

func (s *imageTaskSaveAmbiguityStore) SaveClaimed(_ context.Context, task *ImageTask, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveClaimedCalls++
	if task != nil {
		task.ClaimOwner = owner
	}
	if s.failAtCall > 0 && s.saveClaimedCalls == s.failAtCall {
		if s.commitBeforeError {
			s.persisted = cloneImageTaskForTest(task)
		}
		return errors.New("commit acknowledgement lost")
	}
	s.persisted = cloneImageTaskForTest(task)
	return nil
}

func (s *imageTaskSaveAmbiguityStore) Get(_ context.Context, id string) (*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if s.persisted == nil || s.persisted.ID != id {
		return nil, ErrImageTaskNotFound
	}
	return cloneImageTaskForTest(s.persisted), nil
}

func cloneImageTaskForTest(task *ImageTask) *ImageTask {
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

type imageTaskCancelRaceStore struct{ *imageTaskMemoryStore }

func (s *imageTaskCancelRaceStore) SaveUnclaimedTerminal(_ context.Context, task *ImageTask) error {
	s.mu.Lock()
	s.task = task
	s.mu.Unlock()
	return ErrImageTaskConflict
}

type imageTaskCleanupStore struct {
	*imageTaskMemoryStore
	deleteCalls int
}

func (s *imageTaskCleanupStore) ListCleanupDue(context.Context, time.Time, int) ([]*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil {
		return nil, nil
	}
	return []*ImageTask{s.task}, nil
}

func (s *imageTaskCleanupStore) DeleteFinal(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || s.task.ID != id {
		return ErrImageTaskNotFound
	}
	s.deleteCalls++
	s.task = nil
	return nil
}

func (s *imageTaskMemoryStore) Ready(context.Context) error { return nil }
func (s *imageTaskMemoryStore) FindByIdempotency(_ context.Context, userID, apiKeyID int64, gatewayKeyID, idempotencyKey string) (*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || s.task.IdempotencyKey != strings.TrimSpace(idempotencyKey) || !s.task.BelongsTo(userID, apiKeyID, gatewayKeyID) {
		return nil, ErrImageTaskNotFound
	}
	return s.task, nil
}
func (s *imageTaskMemoryStore) Create(_ context.Context, task *ImageTask, _, _ int) (*ImageTask, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task = task
	return task, false, nil
}
func (s *imageTaskMemoryStore) Get(_ context.Context, id string) (*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || s.task.ID != id {
		return nil, ErrImageTaskNotFound
	}
	return s.task, nil
}
func (s *imageTaskMemoryStore) GetByUser(ctx context.Context, id string, userID int64) (*ImageTask, error) {
	task, err := s.Get(ctx, id)
	if err != nil || task.UserID != userID || userID <= 0 {
		return nil, ErrImageTaskNotFound
	}
	return task, nil
}
func (s *imageTaskMemoryStore) ListByOwner(context.Context, int64, int64, string, int, string) ([]*ImageTask, string, error) {
	return []*ImageTask{s.task}, "", nil
}
func (s *imageTaskMemoryStore) ListByUser(context.Context, int64, int, string) ([]*ImageTask, string, error) {
	return []*ImageTask{s.task}, "", nil
}
func (s *imageTaskMemoryStore) ListByUserState(context.Context, int64, string, int, string) ([]*ImageTask, string, error) {
	return []*ImageTask{s.task}, "", nil
}
func (s *imageTaskMemoryStore) ListAll(context.Context, string, int, string) ([]*ImageTask, string, error) {
	return []*ImageTask{s.task}, "", nil
}
func (s *imageTaskMemoryStore) QueueDepth(context.Context) (int64, error)         { return 0, nil }
func (s *imageTaskMemoryStore) ActiveForUser(context.Context, int64) (int, error) { return 0, nil }
func (s *imageTaskMemoryStore) ClaimDue(_ context.Context, owner string, now time.Time, limit int, ttl time.Duration) ([]*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimLimits = append(s.claimLimits, limit)
	if !s.claimEnabled || s.claimed || s.task == nil || s.task.NextAttemptAt.After(now) {
		return nil, nil
	}
	s.claimed = true
	until := now.Add(ttl)
	s.task.ClaimOwner = owner
	s.task.ClaimUntil = &until
	return []*ImageTask{s.task}, nil
}
func (s *imageTaskMemoryStore) SaveClaimed(_ context.Context, task *ImageTask, _ string) error {
	s.mu.Lock()
	s.saveClaimedCalls++
	if s.failSaveClaimedAtCall > 0 && s.saveClaimedCalls == s.failSaveClaimedAtCall {
		s.mu.Unlock()
		return errors.New("temporary task persistence failure")
	}
	s.task = task
	s.mu.Unlock()
	return nil
}
func (s *imageTaskMemoryStore) SaveUnclaimedTerminal(_ context.Context, task *ImageTask) error {
	s.mu.Lock()
	s.task = task
	s.mu.Unlock()
	return nil
}
func (s *imageTaskMemoryStore) RequestCancel(_ context.Context, id string, userID, apiKeyID int64, gatewayKeyID string, now time.Time) (*ImageTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.task == nil || s.task.ID != id {
		return nil, ErrImageTaskNotFound
	}
	if !s.task.BelongsTo(userID, apiKeyID, gatewayKeyID) {
		return nil, ErrImageTaskForbidden
	}
	if s.task.Terminal() {
		return s.task, nil
	}
	if s.task.State != ImageTaskStateQueued || s.task.ExecutionPhase == ImageTaskExecutionPhaseDispatched {
		return nil, ErrImageTaskAlreadyRunning
	}
	s.task.State = ImageTaskStateCancelled
	s.task.CancelRequested = true
	s.task.CompletedAt = &now
	return s.task, nil
}
func (s *imageTaskMemoryStore) RequestCancelForUser(ctx context.Context, id string, userID int64, now time.Time) (*ImageTask, error) {
	task, err := s.GetByUser(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	return s.RequestCancel(ctx, id, userID, task.APIKeyID, task.GatewayKeyID, now)
}
func (s *imageTaskMemoryStore) ListCleanupDue(context.Context, time.Time, int) ([]*ImageTask, error) {
	return nil, nil
}
func (s *imageTaskMemoryStore) DeleteFinal(context.Context, string) error { return nil }
