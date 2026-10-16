package service

import (
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	GroupOpenAILongContextBillingStatusNotApplicable = "not_applicable"
	GroupOpenAILongContextBillingStatusEnabled       = "enabled"
	GroupOpenAILongContextBillingStatusStandardOnly  = "standard_only"
	GroupOpenAILongContextBillingStatusUnavailable   = "unavailable"
)

var (
	ErrOpenAILongContextBillingPlatformInvalid = infraerrors.BadRequest(
		"OPENAI_LONG_CONTEXT_BILLING_PLATFORM_INVALID",
		"long-context billing is only supported for OpenAI groups",
	)
	ErrOpenAILongContextBillingAccountUnavailable = infraerrors.Conflict(
		"OPENAI_LONG_CONTEXT_BILLING_ACCOUNT_UNAVAILABLE",
		"at least one active and schedulable OpenAI account with provider long-context billing enabled is required",
	)
)

// openAILongContextPersistentCandidate intentionally ignores temporary
// rate-limit, overload and cooldown windows. Those runtime conditions affect
// only the current scheduling attempt and must never rewrite persisted policy.
func openAILongContextPersistentCandidate(account *Account, now time.Time) bool {
	if account == nil || account.Platform != PlatformOpenAI || !account.IsActive() || !account.Schedulable {
		return false
	}
	if account.ExpiresAt != nil && !account.ExpiresAt.After(now) && account.AutoPauseOnExpired {
		return false
	}
	return true
}

func openAILongContextBillingAccountCounts(group *Group, accounts []Account, now time.Time) (longContext, standard int64) {
	for i := range accounts {
		account := &accounts[i]
		if !openAILongContextPersistentCandidate(account, now) {
			continue
		}
		if group != nil && group.RequireOAuthOnly && account.Type == AccountTypeAPIKey {
			continue
		}
		if group != nil && group.RequirePrivacySet && !account.IsPrivacySet() {
			continue
		}
		if account.IsOpenAILongContextBillingEnabled() {
			longContext++
		} else {
			standard++
		}
	}
	return longContext, standard
}

func validateOpenAILongContextBillingGroup(group *Group, accounts []Account) error {
	if group == nil || !group.AllowOpenAILongContextBilling {
		return nil
	}
	if group.Platform != PlatformOpenAI {
		return ErrOpenAILongContextBillingPlatformInvalid
	}
	longContext, _ := openAILongContextBillingAccountCounts(group, accounts, time.Now())
	if longContext == 0 {
		return ErrOpenAILongContextBillingAccountUnavailable
	}
	return nil
}

func (g *Group) RefreshOpenAILongContextBillingStatus() {
	if g == nil {
		return
	}
	if g.Platform != PlatformOpenAI {
		g.AllowOpenAILongContextBilling = false
		g.OpenAILongContextBillingStatus = GroupOpenAILongContextBillingStatusNotApplicable
		return
	}
	if g.AllowOpenAILongContextBilling {
		if g.OpenAILongContextBillingAccountCount > 0 {
			g.OpenAILongContextBillingStatus = GroupOpenAILongContextBillingStatusEnabled
		} else {
			g.OpenAILongContextBillingStatus = GroupOpenAILongContextBillingStatusUnavailable
		}
		return
	}
	// 关闭分组长上下文加价只改变用户计费口径，不限制账号的厂商成本类型。
	// 因此两类账号任意一种存在，都代表长上下文可以继续按标准价售卖。
	if g.OpenAIStandardBillingAccountCount+g.OpenAILongContextBillingAccountCount > 0 {
		g.OpenAILongContextBillingStatus = GroupOpenAILongContextBillingStatusStandardOnly
		return
	}
	g.OpenAILongContextBillingStatus = GroupOpenAILongContextBillingStatusUnavailable
}
