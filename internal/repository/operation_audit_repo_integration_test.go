//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestOperationAuditRepositoryConcurrentInsertAndStableCursor(t *testing.T) {
	ctx := context.Background()
	repo := NewOperationAuditRepository(integrationDB)
	prefix := fmt.Sprintf("test.audit.concurrent.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM operation_audit_logs WHERE action LIKE $1`, prefix+"%")
	})

	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Insert(ctx, &service.OperationAuditLog{
				Action: prefix, ResourceType: "account", ResourceID: fmt.Sprint(i),
				Result: service.AuditResultSuccess, Summary: map[string]any{"fields": []string{"status"}},
			})
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	first, err := repo.List(ctx, &service.OperationAuditFilter{Action: prefix, Limit: 5})
	require.NoError(t, err)
	require.Len(t, first.Items, 5)
	require.NotZero(t, first.NextCursor)
	second, err := repo.List(ctx, &service.OperationAuditFilter{Action: prefix, Limit: 20, BeforeID: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, second.Items, 7)
	for _, item := range second.Items {
		require.Less(t, item.ID, first.NextCursor)
	}
}

func TestOperationAuditRepositoryClearRollsBackWhenTailInsertFails(t *testing.T) {
	ctx := context.Background()
	repo := NewOperationAuditRepository(integrationDB)
	action := fmt.Sprintf("test.audit.rollback.%d", time.Now().UnixNano())
	requestID := fmt.Sprintf("reject-audit-clear-%d", time.Now().UnixNano())
	triggerName := fmt.Sprintf("test_operation_audit_reject_%d", time.Now().UnixNano())
	functionName := triggerName + "_fn"
	id, err := repo.Insert(ctx, &service.OperationAuditLog{Action: action, Result: service.AuditResultSuccess})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger AS $$
BEGIN
    IF NEW.request_id = %s THEN
        RAISE EXCEPTION 'forced operation audit tail failure';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER %s BEFORE INSERT ON operation_audit_logs
FOR EACH ROW EXECUTE FUNCTION %s();`,
		pq.QuoteIdentifier(functionName), pq.QuoteLiteral(requestID),
		pq.QuoteIdentifier(triggerName), pq.QuoteIdentifier(functionName)))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON operation_audit_logs", pq.QuoteIdentifier(triggerName)))
		_, _ = integrationDB.ExecContext(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", pq.QuoteIdentifier(functionName)))
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM operation_audit_logs WHERE action=$1`, action)
	})

	_, err = repo.ClearAllWithTrace(ctx, &service.OperationAuditLog{
		Action:    service.AuditActionAuditLogClear,
		RequestID: requestID,
	})
	require.Error(t, err)
	item, err := repo.GetByID(ctx, id)
	require.NoError(t, err, "TRUNCATE must roll back when the tail record fails")
	require.Equal(t, action, item.Action)
}

func TestOperationAuditRepositoryRetentionIsBatchLimited(t *testing.T) {
	ctx := context.Background()
	repo := NewOperationAuditRepository(integrationDB)
	action := fmt.Sprintf("test.audit.retention.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM operation_audit_logs WHERE action=$1`, action)
	})
	for i := 0; i < 5; i++ {
		_, err := repo.Insert(ctx, &service.OperationAuditLog{
			CreatedAt: time.Now().UTC().Add(-48 * time.Hour), Action: action,
			Result: service.AuditResultSuccess,
		})
		require.NoError(t, err)
	}
	deleted, err := repo.DeleteBefore(ctx, time.Now().UTC().Add(-24*time.Hour), 2)
	require.NoError(t, err)
	require.EqualValues(t, 2, deleted)
	remaining, err := repo.List(ctx, &service.OperationAuditFilter{Action: action, Limit: 10})
	require.NoError(t, err)
	require.Len(t, remaining.Items, 3)
}
