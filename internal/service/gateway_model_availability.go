package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// ModelAvailabilityDiagnosis describes whether the requested model can be
// served by any configured account in the current scheduling pool. It ignores
// transient runtime state such as rate limits and temporary account blocks.
type ModelAvailabilityDiagnosis struct {
	HasAccountsInPool bool
	HasModelSupport   bool
}

// ModelAvailabilityDiagnoser is implemented by gateway services that can
// distinguish "model not configured anywhere" from "accounts exist but are
// temporarily unavailable" on the no-account error path.
type ModelAvailabilityDiagnoser interface {
	DiagnoseModelAvailabilityForPlatform(ctx context.Context, groupID *int64, requestedModel string, platform string) ModelAvailabilityDiagnosis
}

// DiagnoseModelAvailabilityForPlatform inspects accounts enabled by persistent
// configuration. Production uses a dedicated query that ignores transient
// rate-limit, overload, expiry and runtime-block state; an uncertain diagnosis
// remains conservative so it can never turn a temporary outage into a 404.
func (s *GatewayService) DiagnoseModelAvailabilityForPlatform(
	ctx context.Context,
	groupID *int64,
	requestedModel string,
	platform string,
) ModelAvailabilityDiagnosis {
	if s == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" || strings.TrimSpace(platform) == "" {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	if s.accountRepo == nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	useMixed := platform == PlatformAnthropic || platform == PlatformGemini
	platforms := []string{platform}
	if useMixed {
		platforms = append(platforms, PlatformAntigravity)
	}
	queryGroupID := groupID
	includeGrouped := false
	if useMixed {
		if groupID == nil && s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
			includeGrouped = true
		}
	} else if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		queryGroupID = nil
		includeGrouped = true
	}

	var accounts []Account
	var err error
	if repo, ok := s.accountRepo.(modelAvailabilityCandidateRepository); ok {
		accounts, err = repo.ListModelAvailabilityCandidates(ctx, queryGroupID, platforms, includeGrouped)
	} else {
		// Narrow test doubles and older external implementations keep a
		// conservative compatibility path. The production repository implements
		// the stable candidate query above.
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, platform, false)
	}
	if err != nil {
		return ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}

	diag := ModelAvailabilityDiagnosis{}
	for i := range accounts {
		if useMixed && accounts[i].Platform == PlatformAntigravity && !accounts[i].IsMixedSchedulingEnabled() {
			continue
		}
		diag.HasAccountsInPool = true
		if s.isModelSupportedByAccountWithContext(ctx, &accounts[i], requestedModel) {
			diag.HasModelSupport = true
			return diag
		}
	}
	return diag
}
