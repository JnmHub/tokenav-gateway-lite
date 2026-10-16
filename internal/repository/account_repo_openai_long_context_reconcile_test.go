package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestReconcileOpenAILongContextBillingGroupsDisablesUnavailableGroup(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)UPDATE groups AS g.*allow_openai_long_context_billing = FALSE.*NOT EXISTS.*RETURNING g.id`).
		WithArgs(pq.Array([]int64{7, 9})).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload, dedup_key)")).
		WithArgs(service.SchedulerOutboxEventGroupChanged, nil, int64(7), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	require.NoError(t, repo.reconcileOpenAILongContextBillingGroups(context.Background(), []int64{9, 7, 7}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpenAILongContextReconcileIgnoresTemporaryCooldownColumns(t *testing.T) {
	require.NotContains(t, strings.ToLower(groupOpenAILongContextBillingSQL), "rate_limit_reset_at")
	require.NotContains(t, strings.ToLower(groupOpenAILongContextBillingSQL), "overload_until")
	require.NotContains(t, strings.ToLower(groupOpenAILongContextBillingSQL), "temp_unschedulable_until")
}
