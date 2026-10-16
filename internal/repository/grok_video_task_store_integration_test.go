//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGrokVideoTaskStorePersistsAndClaimsDueTask(t *testing.T) {
	ctx := context.Background()
	store := &grokVideoTaskStore{db: integrationDB}
	requestID := fmt.Sprintf("integration-grok-video-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM grok_video_tasks WHERE request_id = $1", requestID)
	})

	now := time.Now().UTC()
	nextPoll := now.Add(-time.Second)
	task := &service.GrokVideoTask{
		RequestID:       requestID,
		APIKeyID:        101,
		UserID:          102,
		AccountID:       103,
		RequestedModel:  "grok-imagine-video",
		BillingModel:    "grok-imagine-video",
		UpstreamModel:   "grok-imagine-video",
		Resolution:      "720p",
		DurationSeconds: 8,
		VideoCount:      1,
		State:           service.GrokVideoTaskStatePending,
		CreatedAt:       now,
		NextPollAt:      &nextPoll,
	}
	require.NoError(t, store.Save(ctx, task))

	loaded, err := store.Get(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, requestID, loaded.RequestID)
	require.Equal(t, int64(103), loaded.AccountID)

	due, err := store.ListDue(ctx, time.Now().UTC(), 1000)
	require.NoError(t, err)
	found := false
	for _, candidate := range due {
		if candidate.RequestID == requestID {
			found = true
			break
		}
	}
	require.True(t, found, "persisted pending task should be recoverable after a process restart")

	locked, err := store.TryLockPolling(ctx, requestID, time.Minute)
	require.NoError(t, err)
	require.True(t, locked)
	locked, err = store.TryLockPolling(ctx, requestID, time.Minute)
	require.NoError(t, err)
	require.False(t, locked, "second instance must not acquire an active row lease")
	require.NoError(t, store.UnlockPolling(ctx, requestID))
}
