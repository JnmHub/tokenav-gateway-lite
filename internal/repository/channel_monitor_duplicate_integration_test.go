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

func TestChannelMonitorDuplicateKeepsCiphertextAndIsolatesHistory(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewChannelMonitorRepository(client, integrationDB)
	svc := service.NewChannelMonitorService(repo, nil)
	suffix := time.Now().UnixNano()
	sourceName := fmt.Sprintf("monitor-duplicate-source-%d", suffix)

	source := &service.ChannelMonitor{
		Name: sourceName, Provider: service.MonitorProviderOpenAI,
		APIMode: service.MonitorAPIModeResponses, Endpoint: "https://example.com/v1",
		APIKey: "encrypted:integration-secret", PrimaryModel: "gpt-5.6-sol",
		ExtraModels: []string{"gpt-5.5"}, GroupName: "production", Enabled: true,
		IntervalSeconds: 60, JitterSeconds: 5, CreatedBy: 1,
		ExtraHeaders:     map[string]string{"X-Test": "duplicate"},
		BodyOverrideMode: service.MonitorBodyOverrideModeMerge,
		BodyOverride:     map[string]any{"metadata": map[string]any{"source": "integration"}},
	}
	require.NoError(t, repo.Create(ctx, source))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM channel_monitors WHERE name LIKE $1", sourceName+"%")
	})

	checkedAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, repo.MarkChecked(ctx, source.ID, checkedAt))
	require.NoError(t, repo.InsertHistoryBatch(ctx, []*service.ChannelMonitorHistoryRow{{
		MonitorID: source.ID, Model: source.PrimaryModel, Status: "failed",
		Message: "source-only-error", CheckedAt: checkedAt,
	}}))

	duplicate, err := svc.Duplicate(ctx, source.ID, 77, "admin:77", "integration-operation")
	require.NoError(t, err)
	require.NotZero(t, duplicate.ID)
	require.False(t, duplicate.Enabled)
	require.Nil(t, duplicate.LastCheckedAt)
	require.Empty(t, duplicate.APIKey)

	var encryptedKey string
	var enabled bool
	var lastCheckedAt *time.Time
	var operationID string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT api_key_encrypted, enabled, last_checked_at, duplicate_operation_id
		FROM channel_monitors WHERE id = $1`, duplicate.ID,
	).Scan(&encryptedKey, &enabled, &lastCheckedAt, &operationID))
	require.Equal(t, "encrypted:integration-secret", encryptedKey)
	require.False(t, enabled)
	require.Nil(t, lastCheckedAt)
	require.Len(t, operationID, 64)

	var sourceHistoryCount, duplicateHistoryCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_monitor_histories WHERE monitor_id = $1", source.ID).Scan(&sourceHistoryCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_monitor_histories WHERE monitor_id = $1", duplicate.ID).Scan(&duplicateHistoryCount))
	require.Equal(t, 1, sourceHistoryCount)
	require.Zero(t, duplicateHistoryCount)

	replayed, err := svc.Duplicate(ctx, source.ID, 77, "admin:77", "integration-operation")
	require.NoError(t, err)
	require.Equal(t, duplicate.ID, replayed.ID)
	var duplicateCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_monitors WHERE duplicate_operation_id = $1", operationID).Scan(&duplicateCount))
	require.Equal(t, 1, duplicateCount)
}
