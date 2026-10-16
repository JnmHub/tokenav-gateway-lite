package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type globalModelsAccountRepoStub struct {
	service.AccountRepository
	accounts []service.Account
	err      error
	calls    int
}

func (s *globalModelsAccountRepoStub) ListSchedulable(context.Context) ([]service.Account, error) {
	s.calls++
	return append([]service.Account(nil), s.accounts...), s.err
}

func globalModelsAccountForTest(id int64, model string) service.Account {
	return service.Account{
		ID: id, Platform: service.PlatformOpenAI,
		Credentials: map[string]any{"model_mapping": map[string]any{model: model}},
	}
}

func TestGatewayGlobalAvailableModelsBypassesRequestCache(t *testing.T) {
	old := globalModelsAccountForTest(1, "old-model")
	added := globalModelsAccountForTest(2, "e2e-seedance")
	for _, tc := range []struct {
		name     string
		accounts []service.Account
		err      error
		want     []string
	}{
		{name: "added", accounts: []service.Account{old, added}, want: []string{"e2e-seedance", "old-model"}},
		{name: "removed", accounts: []service.Account{added}, want: []string{"e2e-seedance"}},
		{name: "empty"},
		{name: "repository_error", err: errors.New("repository unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &globalModelsAccountRepoStub{accounts: []service.Account{old}}
			h := newGatewayModelsHandlerForTest(repo)
			ctx := context.Background()
			require.Equal(t, []string{"old-model"}, h.gatewayService.GetAvailableModels(ctx, nil, ""))
			require.Equal(t, 1, repo.calls)

			repo.accounts, repo.err = tc.accounts, tc.err
			require.Equal(t, tc.want, h.GlobalAvailableModels(ctx))
			require.Equal(t, 2, repo.calls)
			// 配置上报不读写客户短缓存，避免旧并发回填干扰本次快照。
			require.Equal(t, []string{"old-model"}, h.gatewayService.GetAvailableModels(ctx, nil, ""))
			require.Equal(t, 2, repo.calls)
		})
	}
}
