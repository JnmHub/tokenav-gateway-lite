//go:build integration

package repository

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *UserRepoSuite) TestUpdateDoesNotRevertConcurrentFinancialAndAuthorityState() {
	user := s.mustCreateUser(&service.User{
		Email: "stale-user-update@example.com", Username: "before", Balance: 10,
		Status: service.StatusActive,
	})
	stale, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)

	// These columns are owned by independent hot paths and all advance after the
	// profile snapshot was loaded.
	s.Require().NoError(s.repo.UpdateBalance(s.ctx, user.ID, 5))
	latestActive := time.Now().UTC().Truncate(time.Microsecond)
	s.Require().NoError(s.repo.UpdateUserLastActiveAt(s.ctx, user.ID, latestActive))
	banned, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	banned.Status = service.StatusDisabled
	s.Require().NoError(s.repo.Update(s.ctx, banned, service.UserUpdateFields{Status: true}))

	stale.Username = "after"
	s.Require().NoError(s.repo.Update(s.ctx, stale, service.UserUpdateFields{Username: true}))

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal("after", got.Username)
	s.Require().InDelta(15, got.Balance, 1e-9)
	s.Require().InDelta(5, got.TotalRecharged, 1e-9)
	s.Require().Equal(service.StatusDisabled, got.Status)
	s.Require().NotNil(got.LastActiveAt)
	s.Require().True(got.LastActiveAt.Equal(latestActive))
}

func (s *UserRepoSuite) TestAtomicAdministratorBalanceChanges() {
	user := s.mustCreateUser(&service.User{Email: "atomic-balance@example.com", Balance: 10})

	change, err := s.repo.AdjustBalance(s.ctx, user.ID, 5)
	s.Require().NoError(err)
	s.Require().Equal(service.BalanceChange{Old: 10, New: 15}, change)

	change, err = s.repo.SetBalance(s.ctx, user.ID, 2)
	s.Require().NoError(err)
	s.Require().Equal(service.BalanceChange{Old: 15, New: 2}, change)

	change, err = s.repo.AdjustBalance(s.ctx, user.ID, -3)
	s.Require().ErrorIs(err, service.ErrBalanceNegative)
	s.Require().Equal(service.BalanceChange{Old: 2, New: -1}, change)

	got, err := s.repo.GetByID(s.ctx, user.ID)
	s.Require().NoError(err)
	s.Require().Equal(2.0, got.Balance)
	s.Require().Zero(got.TotalRecharged, "manual administrator adjustments are not customer recharge")
}

func (s *APIKeyRepoSuite) TestUpdateDoesNotRevertConcurrentUsage() {
	user := s.mustCreateUser("stale-key-update@example.com")
	key := s.mustCreateApiKey(user.ID, "sk-stale-usage", "before", nil)
	key.Quota = 100
	s.Require().NoError(s.repo.Update(s.ctx, key, service.APIKeyUpdateFields{Quota: true}))

	stale, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	_, err = s.repo.IncrementQuotaUsedAndGetState(s.ctx, key.ID, 30)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.IncrementRateLimitUsage(s.ctx, key.ID, 12))

	stale.Name = "after"
	s.Require().NoError(s.repo.Update(s.ctx, stale, service.APIKeyUpdateFields{Name: true}))

	got, err := s.repo.GetByID(s.ctx, key.ID)
	s.Require().NoError(err)
	s.Require().Equal("after", got.Name)
	s.Require().InDelta(30, got.QuotaUsed, 1e-9)
	s.Require().InDelta(12, got.Usage5h, 1e-9)
	s.Require().InDelta(12, got.Usage1d, 1e-9)
	s.Require().InDelta(12, got.Usage7d, 1e-9)
}
