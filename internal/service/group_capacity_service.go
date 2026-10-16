package service

import (
	"context"
	"time"
)

// GroupCapacitySummary holds aggregated capacity for a single group.
type GroupCapacitySummary struct {
	GroupID             int64 `json:"group_id"`
	ConcurrencyUsed     int   `json:"concurrency_used"`
	ConcurrencyMax      int   `json:"concurrency_max"`
	SessionsUsed        int   `json:"sessions_used"`
	SessionsMax         int   `json:"sessions_max"`
	RPMUsed             int   `json:"rpm_used"`
	RPMMax              int   `json:"rpm_max"`
	IngressRequestsUsed int   `json:"ingress_requests_used"`
}

// GroupCapacityService aggregates per-group capacity from runtime data.
type GroupCapacityService struct {
	accountRepo        AccountRepository
	groupRepo          GroupRepository
	concurrencyService *ConcurrencyService
	sessionLimitCache  SessionLimitCache
	rpmCache           RPMCache
}

// NewGroupCapacityService creates a new GroupCapacityService.
func NewGroupCapacityService(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	concurrencyService *ConcurrencyService,
	sessionLimitCache SessionLimitCache,
	rpmCache RPMCache,
) *GroupCapacityService {
	return &GroupCapacityService{
		accountRepo:        accountRepo,
		groupRepo:          groupRepo,
		concurrencyService: concurrencyService,
		sessionLimitCache:  sessionLimitCache,
		rpmCache:           rpmCache,
	}
}

// GetAllGroupCapacity returns capacity summary for all active groups.
func (s *GroupCapacityService) GetAllGroupCapacity(ctx context.Context) ([]GroupCapacitySummary, error) {
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]GroupCapacitySummary, len(groups))
	groupIndex := make(map[int64]int, len(groups))
	for i := range groups {
		results[i].GroupID = groups[i].ID
		groupIndex[groups[i].ID] = i
	}
	if len(groupIndex) == 0 {
		return results, nil
	}
	// Ingress activity is independent from account capacity. A read failure must
	// remain distinguishable from a real zero; the admin UI already renders a
	// failed capacity request as unavailable, while customer traffic stays open.
	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		groupIDs = append(groupIDs, groups[i].ID)
	}
	if s.concurrencyService != nil {
		counts, ingressErr := s.concurrencyService.GetGroupIngressConcurrencyBatch(ctx, groupIDs)
		if ingressErr != nil {
			return nil, ingressErr
		}
		for i := range results {
			results[i].IngressRequestsUsed = counts[results[i].GroupID]
		}
	}

	// 分组容量页会一次展示所有分组。如果逐分组调用 ListSchedulableByGroupID，
	// 分组越多 DB 查询越多。这里一次读取全部可调度账号，再按账号绑定分组聚合。
	accounts, err := s.accountRepo.ListSchedulable(ctx)
	if err != nil {
		return nil, err
	}
	s.aggregateGroupCapacity(ctx, accounts, results, groupIndex)
	return results, nil
}

func (s *GroupCapacityService) aggregateGroupCapacity(ctx context.Context, accounts []Account, results []GroupCapacitySummary, groupIndex map[int64]int) {
	if len(accounts) == 0 || len(results) == 0 || len(groupIndex) == 0 {
		return
	}

	accountIDs := make([]int64, 0, len(accounts))
	sessionAccountIDs := make([]int64, 0, len(accounts))
	rpmAccountIDs := make([]int64, 0, len(accounts))
	sessionTimeouts := make(map[int64]time.Duration)
	seenAccounts := make(map[int64]struct{}, len(accounts))
	seenSessionAccounts := make(map[int64]struct{}, len(accounts))
	seenRPMAccounts := make(map[int64]struct{}, len(accounts))

	for i := range accounts {
		acc := &accounts[i]
		groupIDs := accountCapacityGroupIDs(acc)
		if len(groupIDs) == 0 {
			continue
		}

		for _, groupID := range groupIDs {
			idx, ok := groupIndex[groupID]
			if !ok {
				continue
			}
			results[idx].ConcurrencyMax += acc.Concurrency
			if maxSessions := acc.GetMaxSessions(); maxSessions > 0 {
				results[idx].SessionsMax += maxSessions
			}
			if rpm := acc.GetBaseRPM(); rpm > 0 {
				results[idx].RPMMax += rpm
			}
		}

		if _, ok := seenAccounts[acc.ID]; !ok {
			seenAccounts[acc.ID] = struct{}{}
			accountIDs = append(accountIDs, acc.ID)
		}
		if acc.GetMaxSessions() > 0 {
			if _, ok := seenSessionAccounts[acc.ID]; !ok {
				seenSessionAccounts[acc.ID] = struct{}{}
				sessionAccountIDs = append(sessionAccountIDs, acc.ID)
				timeout := time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
				if timeout <= 0 {
					timeout = 5 * time.Minute
				}
				sessionTimeouts[acc.ID] = timeout
			}
		}
		if acc.GetBaseRPM() > 0 {
			if _, ok := seenRPMAccounts[acc.ID]; !ok {
				seenRPMAccounts[acc.ID] = struct{}{}
				rpmAccountIDs = append(rpmAccountIDs, acc.ID)
			}
		}
	}

	concurrencyMap := map[int64]int{}
	if len(accountIDs) > 0 && s.concurrencyService != nil {
		if got, err := s.concurrencyService.GetAccountConcurrencyBatch(ctx, accountIDs); err == nil && got != nil {
			concurrencyMap = got
		}
	}

	var sessionsMap map[int64]int
	if len(sessionAccountIDs) > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, sessionAccountIDs, sessionTimeouts)
	}

	var rpmMap map[int64]int
	if len(rpmAccountIDs) > 0 && s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, rpmAccountIDs)
	}

	for i := range accounts {
		acc := &accounts[i]
		groupIDs := accountCapacityGroupIDs(acc)
		for _, groupID := range groupIDs {
			idx, ok := groupIndex[groupID]
			if !ok {
				continue
			}
			results[idx].ConcurrencyUsed += concurrencyMap[acc.ID]
			if sessionsMap != nil {
				results[idx].SessionsUsed += sessionsMap[acc.ID]
			}
			if rpmMap != nil {
				results[idx].RPMUsed += rpmMap[acc.ID]
			}
		}
	}
}

func accountCapacityGroupIDs(account *Account) []int64 {
	if account == nil {
		return nil
	}
	seen := make(map[int64]struct{}, len(account.AccountGroups)+len(account.GroupIDs))
	out := make([]int64, 0, len(account.AccountGroups)+len(account.GroupIDs))
	for _, accountGroup := range account.AccountGroups {
		if accountGroup.GroupID <= 0 {
			continue
		}
		if _, ok := seen[accountGroup.GroupID]; ok {
			continue
		}
		seen[accountGroup.GroupID] = struct{}{}
		out = append(out, accountGroup.GroupID)
	}
	for _, groupID := range account.GroupIDs {
		if groupID <= 0 {
			continue
		}
		if _, ok := seen[groupID]; ok {
			continue
		}
		seen[groupID] = struct{}{}
		out = append(out, groupID)
	}
	return out
}
