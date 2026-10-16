//go:build integration

package repository

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type TotpCacheIntegrationSuite struct {
	IntegrationRedisSuite
	cache *TotpCache
}

func TestTotpCacheIntegrationSuite(t *testing.T) {
	suite.Run(t, new(TotpCacheIntegrationSuite))
}

func (s *TotpCacheIntegrationSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewTotpCache(s.rdb).(*TotpCache)
}

func (s *TotpCacheIntegrationSuite) TestStepUpGrantConcurrentConsumeSucceedsExactlyOnce() {
	tokenHash := "integration-concurrent-token-hash"
	grant := &service.StepUpGrant{
		UserID:       42,
		SessionID:    "session-42",
		BindingHash:  "binding-42",
		ActionScope:  service.StepUpScopeAccountsExport,
		Role:         service.RoleAdmin,
		TokenVersion: 3,
		VerifiedAt:   time.Now().UTC(),
		ExpiresAt:    time.Now().UTC().Add(time.Minute),
	}
	require.NoError(s.T(), s.cache.StoreStepUpGrant(s.ctx, tokenHash, grant, time.Minute))

	const consumers = 32
	var successes atomic.Int32
	var wg sync.WaitGroup
	errCh := make(chan error, consumers)
	for range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumed, err := s.cache.ConsumeStepUpGrant(s.ctx, tokenHash)
			if err != nil {
				errCh <- err
				return
			}
			if consumed != nil {
				successes.Add(1)
				if consumed.UserID != grant.UserID || consumed.ActionScope != grant.ActionScope {
					errCh <- fmt.Errorf("unexpected consumed grant: user_id=%d scope=%s", consumed.UserID, consumed.ActionScope)
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(s.T(), err)
	}
	require.EqualValues(s.T(), 1, successes.Load())

	replayed, err := s.cache.ConsumeStepUpGrant(s.ctx, tokenHash)
	require.NoError(s.T(), err)
	require.Nil(s.T(), replayed, "已消费的 step-up grant 必须永久拒绝重放")
}

func (s *TotpCacheIntegrationSuite) TestInvalidStoredGrantIsDeletedBeforeDecodeFailure() {
	tokenHash := "integration-invalid-token-hash"
	require.NoError(s.T(), s.rdb.Set(s.ctx, stepUpGrantKey(tokenHash), "not-json", time.Minute).Err())

	grant, err := s.cache.ConsumeStepUpGrant(s.ctx, tokenHash)
	require.Error(s.T(), err)
	require.Nil(s.T(), grant)

	grant, err = s.cache.ConsumeStepUpGrant(s.ctx, tokenHash)
	require.NoError(s.T(), err)
	require.Nil(s.T(), grant, "格式错误的授权也不能保留给第二次尝试")
}
