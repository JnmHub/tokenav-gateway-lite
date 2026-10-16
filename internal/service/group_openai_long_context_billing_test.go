//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAILongContextBillingAccountCountsUsesPersistentEligibility(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	group := &Group{
		Platform:          PlatformOpenAI,
		RequireOAuthOnly:  true,
		RequirePrivacySet: true,
	}
	accounts := []Account{
		{
			Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Extra:            map[string]any{openAILongContextBillingEnabledKey: true, "privacy_mode": PrivacyModeTrainingOff},
			RateLimitResetAt: &future, OverloadUntil: &future, TempUnschedulableUntil: &future,
		},
		{
			Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff},
		},
		{
			Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
			Extra: map[string]any{openAILongContextBillingEnabledKey: true, "privacy_mode": PrivacyModeTrainingOff},
		},
		{
			Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Extra: map[string]any{openAILongContextBillingEnabledKey: true},
		},
		{
			Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
			Extra:     map[string]any{openAILongContextBillingEnabledKey: true, "privacy_mode": PrivacyModeTrainingOff},
			ExpiresAt: &past, AutoPauseOnExpired: true,
		},
	}

	longContext, standard := openAILongContextBillingAccountCounts(group, accounts, now)
	require.EqualValues(t, 1, longContext, "temporary 429/overload/cooldown must not remove persistent eligibility")
	require.EqualValues(t, 1, standard)
}

func TestValidateOpenAILongContextBillingGroup(t *testing.T) {
	valid := []Account{{
		Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true,
		Extra: map[string]any{openAILongContextBillingEnabledKey: true},
	}}

	require.NoError(t, validateOpenAILongContextBillingGroup(&Group{
		Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true,
	}, valid))
	require.ErrorIs(t, validateOpenAILongContextBillingGroup(&Group{
		Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true,
	}, nil), ErrOpenAILongContextBillingAccountUnavailable)
	require.ErrorIs(t, validateOpenAILongContextBillingGroup(&Group{
		Platform: PlatformGrok, AllowOpenAILongContextBilling: true,
	}, valid), ErrOpenAILongContextBillingPlatformInvalid)
	require.NoError(t, validateOpenAILongContextBillingGroup(&Group{
		Platform: PlatformOpenAI, AllowOpenAILongContextBilling: false,
	}, nil))
}

func TestGroupRefreshOpenAILongContextBillingStatus(t *testing.T) {
	tests := []struct {
		name  string
		group Group
		want  string
	}{
		{name: "non openai", group: Group{Platform: PlatformGrok, AllowOpenAILongContextBilling: true}, want: GroupOpenAILongContextBillingStatusNotApplicable},
		{name: "enabled", group: Group{Platform: PlatformOpenAI, AllowOpenAILongContextBilling: true, OpenAILongContextBillingAccountCount: 1}, want: GroupOpenAILongContextBillingStatusEnabled},
		{name: "standard only", group: Group{Platform: PlatformOpenAI, OpenAIStandardBillingAccountCount: 1}, want: GroupOpenAILongContextBillingStatusStandardOnly},
		{name: "standard only with provider long-context account", group: Group{Platform: PlatformOpenAI, OpenAILongContextBillingAccountCount: 1}, want: GroupOpenAILongContextBillingStatusStandardOnly},
		{name: "unavailable", group: Group{Platform: PlatformOpenAI}, want: GroupOpenAILongContextBillingStatusUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.group.RefreshOpenAILongContextBillingStatus()
			require.Equal(t, tt.want, tt.group.OpenAILongContextBillingStatus)
			if tt.group.Platform != PlatformOpenAI {
				require.False(t, tt.group.AllowOpenAILongContextBilling)
			}
		})
	}
}

func TestAdminServiceCreateGroupRejectsLongContextWithoutEligibleAccount(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "openai", Platform: PlatformOpenAI, RateMultiplier: 1,
		AllowOpenAILongContextBilling: true,
	})
	require.ErrorIs(t, err, ErrOpenAILongContextBillingAccountUnavailable)
	require.Nil(t, repo.created)
}

func TestAdminServiceUpdateGroupValidatesLongContextAgainstBoundAccounts(t *testing.T) {
	group := &Group{ID: 7, Name: "openai", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1}
	groupRepo := &groupRepoStubForAdmin{getByID: group}
	accountRepo := &accountRepoStubForBulkUpdate{listByGroupData: map[int64][]Account{
		7: {{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Extra: map[string]any{openAILongContextBillingEnabledKey: true}}},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accountRepo}
	enabled := true

	updated, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{AllowOpenAILongContextBilling: &enabled})
	require.NoError(t, err)
	require.True(t, groupRepo.updated.AllowOpenAILongContextBilling)
	require.True(t, updated.AllowOpenAILongContextBilling)
}

func TestSchedulerGroupConfigChangeFromPayload(t *testing.T) {
	resource, action, ok := schedulerGroupConfigChangeFromPayload(map[string]any{
		"config_change_resource": " groups ",
		"config_change_action":   " auto_disable_long_context ",
	})
	require.True(t, ok)
	require.Equal(t, "groups", resource)
	require.Equal(t, "auto_disable_long_context", action)

	_, _, ok = schedulerGroupConfigChangeFromPayload(nil)
	require.False(t, ok)
}
