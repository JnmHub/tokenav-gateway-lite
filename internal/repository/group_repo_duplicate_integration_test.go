//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCreateGroupFromSourceCopiesExactPrioritiesAndOutbox(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()
	operationID := fmt.Sprintf("%064x", suffix)

	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-source-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetRateMultiplier(1.25).
		Save(ctx)
	require.NoError(t, err)
	accountA, err := client.Account.Create().
		SetName(fmt.Sprintf("duplicate-account-a-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		Save(ctx)
	require.NoError(t, err)
	accountB, err := client.Account.Create().
		SetName(fmt.Sprintf("duplicate-account-b-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(accountA.ID).SetGroupID(source.ID).SetPriority(37).Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(accountB.ID).SetGroupID(source.ID).SetPriority(8).Save(ctx)
	require.NoError(t, err)

	duplicateName := fmt.Sprintf("duplicate-copy-%d", suffix)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id IN (SELECT id FROM groups WHERE name IN ($1, $2))", source.Name, duplicateName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id IN ($1, $2)", accountA.ID, accountB.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id IN ($1, $2)", accountA.ID, accountB.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name IN ($1, $2)", source.Name, duplicateName)
	})

	duplicate := &service.Group{
		Name:                 duplicateName,
		Platform:             source.Platform,
		RateMultiplier:       source.RateMultiplier,
		Status:               "inactive",
		SubscriptionType:     service.SubscriptionTypeStandard,
		DuplicateOperationID: operationID,
		ModelRouting:         map[string][]int64{"gpt-*": {accountA.ID, accountB.ID}},
	}
	require.NoError(t, repo.CreateFromSource(ctx, duplicate, source.ID))
	require.NotZero(t, duplicate.ID)
	require.EqualValues(t, 2, duplicate.AccountCount)

	recovered, err := repo.FindByDuplicateOperationID(ctx, operationID)
	require.NoError(t, err)
	require.NotNil(t, recovered)
	require.Equal(t, duplicate.ID, recovered.ID)
	require.Equal(t, "inactive", recovered.Status)

	rows, err := integrationDB.QueryContext(ctx, `SELECT account_id, priority FROM account_groups WHERE group_id = $1 ORDER BY priority ASC`, duplicate.ID)
	require.NoError(t, err)
	defer rows.Close()
	got := make(map[int64]int)
	for rows.Next() {
		var accountID int64
		var priority int
		require.NoError(t, rows.Scan(&accountID, &priority))
		got[accountID] = priority
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[int64]int{accountA.ID: 37, accountB.ID: 8}, got)

	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM scheduler_outbox
		WHERE group_id = $1 AND event_type = $2 AND payload->>'action' = 'duplicate'`,
		duplicate.ID, service.SchedulerOutboxEventGroupChanged,
	).Scan(&outboxCount))
	require.Equal(t, 1, outboxCount)
}

func TestCreateCompositeGroupFromSourceCopiesMixedAccountsAndRoutesAtomically(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()

	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-composite-source-%d", suffix)).
		SetPlatform(service.PlatformComposite).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	openAI, err := client.Account.Create().SetName(fmt.Sprintf("duplicate-composite-openai-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeOAuth).Save(ctx)
	require.NoError(t, err)
	grok, err := client.Account.Create().SetName(fmt.Sprintf("duplicate-composite-grok-%d", suffix)).
		SetPlatform(service.PlatformGrok).SetType(service.AccountTypeAPIKey).Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(openAI.ID).SetGroupID(source.ID).SetPriority(7).Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(grok.ID).SetGroupID(source.ID).SetPriority(9).Save(ctx)
	require.NoError(t, err)
	_, err = client.CompositeModelRoute.Create().SetGroupID(source.ID).SetPublicModel("fast-code").
		SetMatchType(service.CompositeRouteMatchExact).SetTargetPlatform(service.PlatformOpenAI).
		SetUpstreamModel("gpt-5.6-sol").SetEndpoint(service.CompositeRouteEndpointResponses).
		SetPriority(17).SetEnabled(true).SetNotes("copied route").Save(ctx)
	require.NoError(t, err)

	duplicateName := fmt.Sprintf("duplicate-composite-copy-%d", suffix)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id IN (SELECT id FROM groups WHERE name IN ($1, $2))", source.Name, duplicateName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name IN ($1, $2)", source.Name, duplicateName)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id IN ($1, $2)", openAI.ID, grok.ID)
	})

	duplicate := &service.Group{Name: duplicateName, Platform: service.PlatformComposite, Status: "inactive", RateMultiplier: 1}
	require.NoError(t, repo.CreateFromSource(ctx, duplicate, source.ID))
	require.EqualValues(t, 2, duplicate.AccountCount)

	var routeCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM composite_model_routes
		WHERE group_id = $1 AND deleted_at IS NULL
			AND public_model = 'fast-code' AND target_platform = 'openai'
			AND upstream_model = 'gpt-5.6-sol' AND priority = 17 AND enabled = TRUE`, duplicate.ID).Scan(&routeCount))
	require.Equal(t, 1, routeCount)
}

func TestDeleteCompositeGroupSoftDeletesRoutesInSameTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()
	group, err := client.Group.Create().
		SetName(fmt.Sprintf("delete-composite-routes-%d", suffix)).
		SetPlatform(service.PlatformComposite).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	route, err := client.CompositeModelRoute.Create().SetGroupID(group.ID).SetPublicModel("delete-alias").
		SetMatchType(service.CompositeRouteMatchExact).SetTargetPlatform(service.PlatformOpenAI).
		SetUpstreamModel("gpt-5.6-sol").SetEndpoint(service.CompositeRouteEndpointResponses).
		SetEnabled(true).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM composite_model_routes WHERE id = $1", route.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", group.ID)
	})

	_, err = repo.DeleteCascade(ctx, group.ID)
	require.NoError(t, err)
	var groupDeletedAt, routeDeletedAt *time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT deleted_at FROM groups WHERE id = $1", group.ID).Scan(&groupDeletedAt))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT deleted_at FROM composite_model_routes WHERE id = $1", route.ID).Scan(&routeDeletedAt))
	require.NotNil(t, groupDeletedAt)
	require.NotNil(t, routeDeletedAt)
}

func TestCreateGroupFromSourceRejectsIncompatibleBindingWithoutHalfGroup(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()

	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-invalid-source-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName(fmt.Sprintf("duplicate-invalid-account-%d", suffix)).
		SetPlatform(service.PlatformAnthropic).
		SetType(service.AccountTypeOAuth).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(source.ID).SetPriority(12).Save(ctx)
	require.NoError(t, err)
	duplicateName := fmt.Sprintf("duplicate-invalid-copy-%d", suffix)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id = $1", source.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name IN ($1, $2)", source.Name, duplicateName)
	})

	duplicate := &service.Group{
		Name: duplicateName, Platform: service.PlatformOpenAI, RateMultiplier: 1,
		Status: "inactive", SubscriptionType: service.SubscriptionTypeStandard,
		DuplicateOperationID: fmt.Sprintf("%064x", suffix+1),
	}
	err = repo.CreateFromSource(ctx, duplicate, source.ID)
	require.ErrorIs(t, err, service.ErrGroupDuplicateBindingInvalid)
	require.ErrorContains(t, err, "expected_platform=openai")

	var groupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE name = $1", duplicateName).Scan(&groupCount))
	require.Zero(t, groupCount)
}

func TestCreateGroupFromSourceRejectsSoftDeletedBinding(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()
	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-deleted-source-%d", suffix)).
		SetPlatform(service.PlatformGemini).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName(fmt.Sprintf("duplicate-deleted-account-%d", suffix)).
		SetPlatform(service.PlatformGemini).
		SetType(service.AccountTypeOAuth).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(source.ID).SetPriority(4).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.Account.DeleteOneID(account.ID).Exec(ctx))
	duplicateName := fmt.Sprintf("duplicate-deleted-copy-%d", suffix)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id = $1", source.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name IN ($1, $2)", source.Name, duplicateName)
	})

	duplicate := &service.Group{
		Name: duplicateName, Platform: source.Platform, RateMultiplier: 1,
		Status: "inactive", SubscriptionType: service.SubscriptionTypeStandard,
		DuplicateOperationID: fmt.Sprintf("%064x", suffix+3),
	}
	err = repo.CreateFromSource(ctx, duplicate, source.ID)
	require.ErrorIs(t, err, service.ErrGroupDuplicateBindingInvalid)
	require.ErrorContains(t, err, "deleted or missing")

	var groupCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE name = $1", duplicateName).Scan(&groupCount))
	require.Zero(t, groupCount)
}

func TestDuplicateGroupConcurrentSameOperationCreatesOneEmptyCopy(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()
	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-concurrent-source-%d", suffix)).
		SetPlatform(service.PlatformGrok).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id IN (SELECT id FROM groups WHERE name LIKE $1)", source.Name+"%")
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name LIKE $1", source.Name+"%")
	})

	const workers = 12
	operationID := fmt.Sprintf("%064x", suffix+7)
	results := make(chan struct {
		id  int64
		err error
	}, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := &service.Group{
				Name: source.Name + " (Copy)", Platform: source.Platform, RateMultiplier: 1,
				Status: "inactive", SubscriptionType: service.SubscriptionTypeStandard,
				DuplicateOperationID: operationID,
			}
			duplicateErr := repo.CreateFromSource(ctx, candidate, source.ID)
			results <- struct {
				id  int64
				err error
			}{id: candidate.ID, err: duplicateErr}
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	for result := range results {
		if result.err == nil {
			successes++
			continue
		}
		require.ErrorIs(t, result.err, service.ErrGroupExists)
	}
	require.Equal(t, 1, successes)
	recovered, err := repo.FindByDuplicateOperationID(ctx, operationID)
	require.NoError(t, err)
	require.NotNil(t, recovered)

	var copyCount, bindingCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE name LIKE $1 AND deleted_at IS NULL", source.Name+" (Copy%").Scan(&copyCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_groups WHERE group_id = $1", recovered.ID).Scan(&bindingCount))
	require.Equal(t, 1, copyCount)
	require.Zero(t, bindingCount)
}

func TestCreateGroupFromSourceRollsBackWhenOutboxInsertFails(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := newGroupRepositoryWithSQL(client, integrationDB)
	suffix := time.Now().UnixNano()
	operationID := strings.Repeat("b", 64)

	source, err := client.Group.Create().
		SetName(fmt.Sprintf("duplicate-rollback-source-%d", suffix)).
		SetPlatform(service.PlatformAnthropic).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName(fmt.Sprintf("duplicate-rollback-account-%d", suffix)).
		SetPlatform(service.PlatformAnthropic).
		SetType(service.AccountTypeOAuth).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(source.ID).SetPriority(29).Save(ctx)
	require.NoError(t, err)

	functionName := fmt.Sprintf("fail_group_duplicate_outbox_%d", suffix)
	triggerName := fmt.Sprintf("fail_group_duplicate_outbox_trigger_%d", suffix)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.group_id IS NOT NULL AND EXISTS (
				SELECT 1 FROM groups WHERE id = NEW.group_id AND duplicate_operation_id = '%s'
			) THEN
				RAISE EXCEPTION 'forced duplicate outbox failure';
			END IF;
			RETURN NEW;
		END;
		$$`, functionName, operationID))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE INSERT ON scheduler_outbox FOR EACH ROW EXECUTE FUNCTION %s()",
		triggerName, functionName,
	))
	require.NoError(t, err)

	duplicateName := fmt.Sprintf("duplicate-rollback-copy-%d", suffix)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON scheduler_outbox", triggerName))
		_, _ = integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", functionName))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE group_id = $1", source.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE name IN ($1, $2)", source.Name, duplicateName)
	})

	duplicate := &service.Group{
		Name: duplicateName, Platform: source.Platform, RateMultiplier: 1,
		Status: "inactive", SubscriptionType: service.SubscriptionTypeStandard,
		DuplicateOperationID: operationID,
	}
	err = repo.CreateFromSource(ctx, duplicate, source.ID)
	require.ErrorContains(t, err, "forced duplicate outbox failure")

	var groupCount, bindingCount, outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM groups WHERE name = $1", duplicateName).Scan(&groupCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_groups WHERE group_id = $1", duplicate.ID).Scan(&bindingCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE group_id = $1", duplicate.ID).Scan(&outboxCount))
	require.Zero(t, groupCount)
	require.Zero(t, bindingCount)
	require.Zero(t, outboxCount)
}
