//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *AccountRepoSuite) TestList_DefaultSortByPriorityAsc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "higher-number", Priority: 10})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "lower-number", Priority: 1})

	accounts, _, err := s.repo.List(s.ctx, pagination.PaginationParams{Page: 1, PageSize: 10})
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("lower-number", accounts[0].Name)
	s.Require().Equal("higher-number", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_SortByPriorityDesc() {
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "low-priority", Priority: 10})
	mustCreateAccount(s.T(), s.client, &service.Account{Name: "high-priority", Priority: 90})

	accounts, _, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
		Page:      1,
		PageSize:  10,
		SortBy:    "priority",
		SortOrder: "desc",
	}, "", "", "", "", 0, "", "")
	s.Require().NoError(err)
	s.Require().Len(accounts, 2)
	s.Require().Equal("high-priority", accounts[0].Name)
	s.Require().Equal("low-priority", accounts[1].Name)
}

func (s *AccountRepoSuite) TestListWithFilters_GroupFilterKeepsGlobalPrioritySort() {
	group := mustCreateGroup(s.T(), s.client, &service.Group{Name: "group-priority-sort"})
	globalFirst := mustCreateAccount(s.T(), s.client, &service.Account{Name: "global-first", Priority: 1})
	groupFirst := mustCreateAccount(s.T(), s.client, &service.Account{Name: "group-first", Priority: 99})
	legacyFallback := mustCreateAccount(s.T(), s.client, &service.Account{Name: "legacy-fallback", Priority: 2})
	mustBindAccountToGroup(s.T(), s.client, globalFirst.ID, group.ID, 20)
	mustBindAccountToGroup(s.T(), s.client, groupFirst.ID, group.ID, 1)
	mustBindAccountToGroup(s.T(), s.client, legacyFallback.ID, group.ID, 0)

	tests := []struct {
		name      string
		sortOrder string
		wantIDs   []int64
	}{
		{name: "ascending", sortOrder: "asc", wantIDs: []int64{globalFirst.ID, legacyFallback.ID, groupFirst.ID}},
		{name: "descending", sortOrder: "desc", wantIDs: []int64{groupFirst.ID, legacyFallback.ID, globalFirst.ID}},
	}
	for _, test := range tests {
		s.Run(test.name, func() {
			for page, wantID := range test.wantIDs {
				accounts, result, err := s.repo.ListWithFilters(s.ctx, pagination.PaginationParams{
					Page:      page + 1,
					PageSize:  1,
					SortBy:    "priority",
					SortOrder: test.sortOrder,
				}, "", "", "", "", group.ID, "", "")
				s.Require().NoError(err)
				s.Require().Equal(int64(3), result.Total)
				s.Require().Len(accounts, 1)
				s.Require().Equal(wantID, accounts[0].ID, "global account priority must be applied before pagination")
			}
		})
	}
}
