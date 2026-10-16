package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type modelAvailabilityCandidateRepository interface {
	ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error)
}

// DiagnoseModelAvailabilityForPlatform reports whether the requested model is
// allowed by any OpenAI-compatible account in the current scheduling pool. It
// intentionally mirrors OpenAI account selection's model_mapping check, but it
// does not treat temporary overload, quota auto-pause or runtime blocks as a
// model-not-found condition.
func (s *OpenAIGatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	if s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	platform = normalizeOpenAICompatiblePlatform(platform)
	queryGroupID := groupID
	includeGrouped := false
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		queryGroupID = nil
		includeGrouped = true
	}
	var accounts []Account
	var err error
	if repo, ok := s.accountRepo.(modelAvailabilityCandidateRepository); ok {
		accounts, err = repo.ListModelAvailabilityCandidates(ctx, queryGroupID, []string{platform}, includeGrouped)
	} else {
		// 测试桩或旧外部实现没有专用查询时保持保守回退；生产仓库实现上面的
		// 稳定候选查询，不会再由瞬时调度状态决定 404。
		accounts, err = s.listSchedulableAccounts(ctx, groupID, platform)
	}
	if err != nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	for i := range accounts {
		if strings.TrimSpace(platform) != "" && accounts[i].Platform != platform {
			continue
		}
		diag.HasAccountsInPool = true
		if accounts[i].IsModelSupported(requestedModel) {
			diag.HasModelSupport = true
			return diag
		}
	}
	return diag
}
