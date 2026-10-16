//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestImageAsyncTaskStorePostgreSQLIdempotencyClaimAndCleanup(t *testing.T) {
	ctx := context.Background()
	store := NewImageAsyncTaskStore(integrationDB)
	userID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM image_async_tasks WHERE user_id=$1`, userID)
	})
	now := time.Now().UTC().Add(-time.Second)
	task := &service.ImageTask{
		ID: uuid.NewString(), IdempotencyKey: "same-request", PayloadHash: fmt.Sprintf("%064d", userID),
		UserID: userID, APIKeyID: userID + 1, Model: "gpt-image-1", Endpoint: "/v1/images/generations",
		ContentType: "application/json", ImageCount: 1, EncryptedRequest: "ciphertext",
		State: service.ImageTaskStateQueued, SettlementState: service.ImageTaskSettlementPending,
		MaxAttempts: 3, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	created, replayed, err := store.Create(ctx, task, 10000, 2)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, task.ID, created.ID)

	replay := *task
	replay.ID = uuid.NewString()
	got, replayed, err := store.Create(ctx, &replay, 10000, 2)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, task.ID, got.ID)
	lookedUp, err := store.FindByIdempotency(ctx, userID, task.APIKeyID, "", task.IdempotencyKey)
	require.NoError(t, err)
	require.Equal(t, task.ID, lookedUp.ID)

	conflict := replay
	conflict.ID = uuid.NewString()
	conflict.PayloadHash = fmt.Sprintf("%064d", userID+1)
	_, _, err = store.Create(ctx, &conflict, 10000, 2)
	require.ErrorIs(t, err, service.ErrImageTaskConflict)

	claimed, err := store.ClaimDue(ctx, "worker-test", time.Now().UTC(), 500, time.Minute)
	require.NoError(t, err)
	var claimedTask *service.ImageTask
	for _, candidate := range claimed {
		if candidate.ID == task.ID {
			claimedTask = candidate
			break
		}
	}
	require.NotNil(t, claimedTask)
	require.Equal(t, "worker-test", claimedTask.ClaimOwner)

	items, _, err := store.ListByOwner(ctx, userID, task.APIKeyID, "", 10, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, task.ID, items[0].ID)
	require.Empty(t, items[0].EncryptedRequest, "list queries must not transfer encrypted request bodies")

	completed := time.Now().UTC()
	claimedTask.State = service.ImageTaskStateFailed
	claimedTask.SettlementState = service.ImageTaskSettlementReleased
	claimedTask.CompletedAt = &completed
	claimedTask.UpdatedAt = completed
	require.NoError(t, store.SaveClaimed(ctx, claimedTask, "worker-test"))
	require.NoError(t, store.DeleteFinal(ctx, claimedTask.ID))
	_, err = store.Get(ctx, claimedTask.ID)
	require.True(t, errors.Is(err, service.ErrImageTaskNotFound))
}

func TestImageAsyncTaskStoreEnforcesAtomicGlobalAndPerUserCapacity(t *testing.T) {
	ctx := context.Background()
	store := NewImageAsyncTaskStore(integrationDB)
	userID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM image_async_tasks WHERE user_id IN ($1,$2)`, userID, userID+100)
	})
	var initialDepth int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_async_tasks WHERE state IN ('queued','running') OR settlement_state='pending'`).Scan(&initialDepth))

	first := integrationImageTask(userID, userID+1, "capacity-one", "1")
	_, replayed, err := store.Create(ctx, first, initialDepth+1, 1)
	require.NoError(t, err)
	require.False(t, replayed)

	// Idempotency is checked before either capacity gate, so retries remain
	// readable after the queue has reached its configured ceiling.
	retry := *first
	retry.ID = uuid.NewString()
	got, replayed, err := store.Create(ctx, &retry, initialDepth+1, 1)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, first.ID, got.ID)

	globalOverflow := integrationImageTask(userID+100, userID+101, "capacity-two", "2")
	_, _, err = store.Create(ctx, globalOverflow, initialDepth+1, 1)
	require.ErrorIs(t, err, service.ErrImageTaskQueueFull)

	perUserOverflow := integrationImageTask(userID, userID+2, "capacity-three", "3")
	_, _, err = store.Create(ctx, perUserOverflow, 100000, 1)
	require.ErrorIs(t, err, service.ErrImageTaskQueueFull)
}

func TestImageAsyncTaskStoreUserIsolationAndTwoHundredItemPage(t *testing.T) {
	ctx := context.Background()
	store := NewImageAsyncTaskStore(integrationDB)
	userID := time.Now().UnixNano()
	otherUserID := userID + 1000
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM image_async_tasks WHERE user_id IN ($1,$2)`, userID, otherUserID)
	})

	var first *service.ImageTask
	for index := 0; index < 105; index++ {
		task := integrationImageTask(userID, userID+int64(index)+1, fmt.Sprintf("page-%03d", index), fmt.Sprintf("%d", index%10))
		created, replayed, err := store.Create(ctx, task, 100000, 200)
		require.NoError(t, err)
		require.False(t, replayed)
		if first == nil {
			first = created
		}
	}
	items, next, err := store.ListByUserState(ctx, userID, service.ImageTaskStateQueued, 200, "")
	require.NoError(t, err)
	require.Len(t, items, 105, "repository must not silently downgrade a 200-item request to 20")
	require.Empty(t, next)

	owned, err := store.GetByUser(ctx, first.ID, userID)
	require.NoError(t, err)
	require.Equal(t, userID, owned.UserID)
	_, err = store.GetByUser(ctx, first.ID, otherUserID)
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	_, err = store.RequestCancelForUser(ctx, first.ID, otherUserID, time.Now().UTC())
	require.ErrorIs(t, err, service.ErrImageTaskNotFound)
	cancelled, err := store.RequestCancelForUser(ctx, first.ID, userID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, service.ImageTaskStateCancelled, cancelled.State)
}

func TestImageAsyncTaskStoreClaimLeaseRecoversAndPreventsDuplicateWorkers(t *testing.T) {
	ctx := context.Background()
	store := NewImageAsyncTaskStore(integrationDB)
	userID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM image_async_tasks WHERE user_id IN ($1,$2)`, userID, userID+10)
	})

	first := integrationImageTask(userID, userID+1, "claim-recovery", "4")
	_, _, err := store.Create(ctx, first, 100000, 2)
	require.NoError(t, err)
	now := time.Now().UTC()
	claimed, err := store.ClaimDue(ctx, "worker-a", now, 500, time.Minute)
	require.NoError(t, err)
	require.True(t, imageTaskListContains(claimed, first.ID))
	claimed, err = store.ClaimDue(ctx, "worker-b", now.Add(30*time.Second), 500, time.Minute)
	require.NoError(t, err)
	require.False(t, imageTaskListContains(claimed, first.ID))
	claimed, err = store.ClaimDue(ctx, "worker-b", now.Add(time.Minute+time.Millisecond), 500, time.Minute)
	require.NoError(t, err)
	require.True(t, imageTaskListContains(claimed, first.ID), "an expired claim must be recoverable after process restart")

	second := integrationImageTask(userID+10, userID+11, "claim-once", "5")
	_, _, err = store.Create(ctx, second, 100000, 2)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan bool, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"worker-c", "worker-d"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			items, claimErr := store.ClaimDue(ctx, owner, time.Now().UTC(), 500, time.Minute)
			results <- claimErr == nil && imageTaskListContains(items, second.ID)
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for won := range results {
		if won {
			winners++
		}
	}
	require.Equal(t, 1, winners, "FOR UPDATE SKIP LOCKED and claim_until must allow only one worker")
}

func integrationImageTask(userID, apiKeyID int64, idempotencyKey, digestSuffix string) *service.ImageTask {
	now := time.Now().UTC().Add(-time.Second)
	return &service.ImageTask{
		ID: uuid.NewString(), IdempotencyKey: idempotencyKey, PayloadHash: strings.Repeat("0", 64-len(digestSuffix)) + digestSuffix,
		UserID: userID, APIKeyID: apiKeyID, Model: "gpt-image-1", Endpoint: "/v1/images/generations",
		ContentType: "application/json", ImageCount: 1, EncryptedRequest: "ciphertext",
		State: service.ImageTaskStateQueued, SettlementState: service.ImageTaskSettlementPending,
		MaxAttempts: 3, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func imageTaskListContains(items []*service.ImageTask, id string) bool {
	for _, item := range items {
		if item != nil && item.ID == id {
			return true
		}
	}
	return false
}
