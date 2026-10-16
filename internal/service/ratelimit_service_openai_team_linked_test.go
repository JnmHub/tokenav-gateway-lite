//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const teamLinkedDeactivatedBody = `{"detail":{"code":"deactivated_workspace","message":"This workspace has been deactivated."}}`

type teamLinkedAccountRepoStub struct {
	mockAccountRepoForGemini
	teamAccounts []Account
	listErr      error
	listCalls    int
	setErrorIDs  []int64
	setErrorMsgs map[int64]string
	failSetError map[int64]error
}

func (r *teamLinkedAccountRepoStub) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	accounts := make([]Account, 0, len(r.teamAccounts))
	for _, account := range r.teamAccounts {
		if account.Platform == platform && account.Status == StatusActive {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *teamLinkedAccountRepoStub) SetError(_ context.Context, id int64, errorMsg string) error {
	if err, ok := r.failSetError[id]; ok {
		return err
	}
	r.setErrorIDs = append(r.setErrorIDs, id)
	if r.setErrorMsgs == nil {
		r.setErrorMsgs = make(map[int64]string)
	}
	r.setErrorMsgs[id] = errorMsg
	return nil
}

func newTeamLinkedAccount(id int64, teamID string) Account {
	return Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": teamID},
	}
}

func newTeamLinkedFixture() []Account {
	parentID := int64(1)
	shadow := Account{
		ID:              5,
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		Status:          StatusActive,
		ParentAccountID: &parentID,
	}
	apiKey := newTeamLinkedAccount(4, "team-A")
	apiKey.Type = AccountTypeAPIKey
	erroredSibling := newTeamLinkedAccount(7, "team-A")
	erroredSibling.Status = StatusError
	return []Account{
		newTeamLinkedAccount(1, "team-A"),
		newTeamLinkedAccount(2, "team-A"),
		newTeamLinkedAccount(3, "team-B"),
		apiKey,
		shadow,
		newTeamLinkedAccount(6, "team-A"),
		erroredSibling,
	}
}

func newTeamLinkedTestService(repo *teamLinkedAccountRepoStub) (*RateLimitService, *runtimeBlockRecorder) {
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	blocker := &runtimeBlockRecorder{}
	service.SetAccountRuntimeBlocker(blocker)
	return service, blocker
}

func TestTeamLinkedError_FanoutMarksSameTeamAccounts(t *testing.T) {
	repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
	service, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedAccount(1, "team-A")

	shouldDisable := service.HandleUpstreamError(context.Background(), &trigger, http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody))

	require.True(t, shouldDisable)
	require.Equal(t, []int64{2, 6, 1}, repo.setErrorIDs)
	require.Contains(t, repo.setErrorMsgs[2], "team-linked error triggered by account #1")
	require.Contains(t, repo.setErrorMsgs[6], "team-linked error triggered by account #1")
	require.Contains(t, repo.setErrorMsgs[1], "Workspace deactivated (402)")
	require.NotContains(t, repo.setErrorMsgs[1], "team-linked")
	require.Equal(t, []string{openAITeamLinkedErrorBlockReason, openAITeamLinkedErrorBlockReason, "auth_error"}, blocker.reasons)
	require.Equal(t, int64(2), blocker.accounts[0].ID)
	require.Equal(t, int64(6), blocker.accounts[1].ID)
	require.Equal(t, int64(1), blocker.accounts[2].ID)
}

func TestTeamLinkedError_GenericPaymentErrorDoesNotFanout(t *testing.T) {
	repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
	service, _ := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedAccount(1, "team-A")

	service.HandleUpstreamError(context.Background(), &trigger, http.StatusPaymentRequired, http.Header{}, []byte(`{"error":{"message":"insufficient balance"}}`))

	require.Equal(t, []int64{1}, repo.setErrorIDs)
	require.Contains(t, repo.setErrorMsgs[1], "Payment required (402)")
	require.Zero(t, repo.listCalls)
}

func TestTeamLinkedError_DedupWithinTTL(t *testing.T) {
	repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
	service, _ := newTeamLinkedTestService(repo)
	first := newTeamLinkedAccount(1, "team-A")
	second := newTeamLinkedAccount(2, "team-A")

	service.HandleUpstreamError(context.Background(), &first, http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody))
	service.HandleUpstreamError(context.Background(), &second, http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody))

	require.Equal(t, []int64{2, 6, 1, 2}, repo.setErrorIDs)
	require.Equal(t, 1, repo.listCalls)
}

func TestTeamLinkedError_ConcurrentTriggersFanOutOnce(t *testing.T) {
	repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
	service, _ := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedAccount(1, "team-A")

	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			service.maybeHandleOpenAITeamLinkedError(context.Background(), &trigger, http.StatusPaymentRequired, []byte(teamLinkedDeactivatedBody))
		}()
	}
	workers.Wait()

	require.Equal(t, 1, repo.listCalls)
	require.Equal(t, []int64{2, 6}, repo.setErrorIDs)
}

func TestTeamLinkedError_APIKeyAnd429DoNotFanout(t *testing.T) {
	t.Run("api_key", func(t *testing.T) {
		repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
		service, _ := newTeamLinkedTestService(repo)
		trigger := newTeamLinkedAccount(4, "team-A")
		trigger.Type = AccountTypeAPIKey

		service.HandleUpstreamError(context.Background(), &trigger, http.StatusPaymentRequired, http.Header{}, []byte(teamLinkedDeactivatedBody))

		require.Equal(t, []int64{4}, repo.setErrorIDs)
		require.Zero(t, repo.listCalls)
	})
	t.Run("regular_429", func(t *testing.T) {
		repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
		service, _ := newTeamLinkedTestService(repo)
		trigger := newTeamLinkedAccount(1, "team-A")

		service.HandleUpstreamError(context.Background(), &trigger, http.StatusTooManyRequests, http.Header{}, []byte(teamLinkedDeactivatedBody))

		require.Empty(t, repo.setErrorIDs)
		require.Zero(t, repo.listCalls)
	})
}

func TestTeamLinkedError_FastPathTemporaryRuleStillFansOut(t *testing.T) {
	repo := &teamLinkedAccountRepoStub{teamAccounts: newTeamLinkedFixture()}
	service, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedAccount(1, "team-A")
	trigger.Credentials["temp_unschedulable_enabled"] = true
	trigger.Credentials["temp_unschedulable_rules"] = []any{
		map[string]any{
			"error_code":       http.StatusPaymentRequired,
			"keywords":         []any{"deactivated_workspace"},
			"duration_minutes": 5,
		},
	}
	gateway := &OpenAIGatewayService{rateLimitService: service}

	shouldDisable := gateway.handleOpenAIAccountUpstreamError(
		context.Background(),
		&trigger,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(teamLinkedDeactivatedBody),
		"gpt-5.6-sol",
	)

	require.True(t, shouldDisable)
	require.Equal(t, []int64{2, 6}, repo.setErrorIDs)
	require.Equal(t, []string{openAITeamLinkedErrorBlockReason, openAITeamLinkedErrorBlockReason}, blocker.reasons)
}

func TestTeamLinkedError_SetErrorFailureDoesNotAbortRemaining(t *testing.T) {
	repo := &teamLinkedAccountRepoStub{
		teamAccounts: newTeamLinkedFixture(),
		failSetError: map[int64]error{2: errors.New("database unavailable")},
	}
	service, blocker := newTeamLinkedTestService(repo)
	trigger := newTeamLinkedAccount(1, "team-A")

	service.maybeHandleOpenAITeamLinkedError(context.Background(), &trigger, http.StatusPaymentRequired, []byte(teamLinkedDeactivatedBody))

	require.Equal(t, []int64{6}, repo.setErrorIDs)
	require.Len(t, blocker.reasons, 2)
}
