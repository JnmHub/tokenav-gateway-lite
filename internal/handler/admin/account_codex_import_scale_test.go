package admin

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func codexIndexScaleAccounts(size int) []service.Account {
	accounts := make([]service.Account, size)
	for i := range accounts {
		accounts[i] = service.Account{
			ID: int64(i + 1), Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{
				"chatgpt_account_id": fmt.Sprintf("acct-%d", i),
				"email":              fmt.Sprintf("user-%d@example.test", i),
			},
		}
	}
	return accounts
}

func BenchmarkCodexAccountIndexScale(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		accounts := codexIndexScaleAccounts(size)
		lookup := []string{fmt.Sprintf("account:acct-%d", size-1)}
		b.Run(fmt.Sprintf("accounts_%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				index := buildCodexAccountIndex(accounts)
				if found := index.Find(lookup); found == nil {
					b.Fatal("indexed account not found")
				}
			}
		})
	}
}

func TestCodexIdentityDeduplicationRecognizesOverlappingKeys(t *testing.T) {
	seen := map[string]int{}
	markCodexIdentitySeen(seen, []string{"account:acct-1", "email:user@example.test"}, 1)
	index, ok := firstSeenCodexIdentity(seen, []string{"user:user-1", "account:acct-1"})
	require.True(t, ok)
	require.Equal(t, 1, index)
}

type concurrentCodexImportAdmin struct {
	*stubAdminService
	accountsMu sync.Mutex
}

func (s *concurrentCodexImportAdmin) ListAccounts(_ context.Context, _, _ int, platform, accountType, _, _ string, _ int64, _, _, _, _ string) ([]service.Account, int64, error) {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	out := make([]service.Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		if account.Platform == platform && account.Type == accountType {
			out = append(out, account)
		}
	}
	return out, int64(len(out)), nil
}

func (s *concurrentCodexImportAdmin) CreateAccount(_ context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	account := service.Account{
		ID: int64(len(s.accounts) + 100), Name: input.Name, Platform: input.Platform, Type: input.Type,
		Status: service.StatusActive, Schedulable: true, Credentials: input.Credentials, Extra: input.Extra,
	}
	s.accounts = append(s.accounts, account)
	return &account, nil
}

func (s *concurrentCodexImportAdmin) UpdateAccount(_ context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	s.accountsMu.Lock()
	defer s.accountsMu.Unlock()
	for index := range s.accounts {
		if s.accounts[index].ID != id {
			continue
		}
		s.accounts[index].Credentials = input.Credentials
		s.accounts[index].Extra = input.Extra
		updated := s.accounts[index]
		return &updated, nil
	}
	return nil, service.ErrAccountNotFound
}

func TestConcurrentCodexImportsCreateOneIdentity(t *testing.T) {
	base := newStubAdminService()
	base.accounts = nil
	admin := &concurrentCodexImportAdmin{stubAdminService: base}
	handler := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	token := buildCodexImportTestJWT(t, time.Now().Add(time.Hour), map[string]any{
		"chatgpt_account_id": "acct-concurrent",
		"email":              "concurrent@example.test",
	})
	entries := []codexImportEntry{{Index: 1, Value: map[string]any{"access_token": token}}}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := handler.importCodexSessions(context.Background(), CodexSessionImportRequest{}, entries)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	admin.accountsMu.Lock()
	defer admin.accountsMu.Unlock()
	require.Len(t, admin.accounts, 1)
}
