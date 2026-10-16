//go:build unit

package service

import (
	"errors"
	"net/http"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestGrokMediaGenerationEligibilityThreeStateAndIsolation(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	forbiddenBilling := &xai.BillingSummary{
		StatusCode:        http.StatusOK,
		WeeklyStatusCode:  http.StatusForbidden,
		MonthlyStatusCode: http.StatusOK,
		UpdatedAt:         now.Format(time.RFC3339),
	}
	staleFalse := &GrokMediaEligibilitySnapshot{
		Eligible:          boolPointer(false),
		Source:            "billing_probe",
		ObservedAt:        now.Add(-time.Hour).Format(time.RFC3339),
		ProbedAt:          now.Add(-time.Hour).Format(time.RFC3339),
		WeeklyStatusCode:  http.StatusForbidden,
		MonthlyStatusCode: http.StatusOK,
	}
	mediaForbidden := buildGrokMediaRequestForbiddenSnapshot(nil, now.Add(-time.Minute))

	tests := []struct {
		name       string
		account    *Account
		want       bool
		wantState  string
		wantReason string
		wantStale  bool
	}{
		{name: "nil account", account: nil, want: false, wantState: "unknown", wantReason: "not_grok"},
		{name: "non grok", account: &Account{Platform: PlatformOpenAI}, want: false, wantState: "unknown", wantReason: "not_grok"},
		{name: "api key defaults eligible", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}, want: true, wantState: "eligible", wantReason: "non_oauth"},
		{name: "oauth without evidence remains compatible unknown", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}, want: true, wantState: "unknown", wantReason: "billing_unobserved"},
		{name: "legacy weekly forbidden is isolated", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{grokBillingExtraKey: forbiddenBilling}}, want: false, wantState: "ineligible", wantReason: "billing_forbidden"},
		{name: "stale false evidence remains isolated", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{GrokMediaEligibilitySnapshotExtraKey: staleFalse}}, want: false, wantState: "ineligible", wantReason: "billing_forbidden", wantStale: true},
		{name: "explicit false wins", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{GrokMediaEligibleExtraKey: false}}, want: false, wantState: "ineligible", wantReason: "override_disabled"},
		{name: "explicit true wins over forbidden", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{GrokMediaEligibleExtraKey: true, grokBillingExtraKey: forbiddenBilling}}, want: true, wantState: "eligible", wantReason: "override_enabled"},
		{name: "api key media 403 overrides non oauth default", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Extra: map[string]any{GrokMediaEligibilitySnapshotExtraKey: mediaForbidden}}, want: false, wantState: "ineligible", wantReason: "media_request_forbidden"},
		{name: "malformed override falls back", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Extra: map[string]any{GrokMediaEligibleExtraKey: "false"}}, want: true, wantState: "unknown", wantReason: "billing_unobserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := tt.account.GrokMediaGenerationEligibilityDetails(now)
			require.Equal(t, tt.want, details.EffectiveEligible)
			require.Equal(t, tt.wantState, details.State)
			require.Equal(t, tt.wantReason, details.Reason)
			require.Equal(t, tt.wantStale, details.Stale)
		})
	}
}

func TestBuildGrokMediaEligibilitySnapshotPreservesLastDefinitiveEvidence(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	unknown := buildGrokMediaEligibilitySnapshot(nil, http.StatusTooManyRequests, nil, http.StatusBadGateway, errors.New("upstream"), now)
	require.Nil(t, unknown.Eligible)
	require.True(t, unknown.Stale)
	require.Equal(t, []string{"weekly", "monthly"}, unknown.FailedWindows)

	previous := &GrokMediaEligibilitySnapshot{
		Eligible:   boolPointer(true),
		ObservedAt: now.Add(-time.Hour).Format(time.RFC3339),
	}
	retained := buildGrokMediaEligibilitySnapshot(previous, http.StatusServiceUnavailable, errors.New("weekly"), 0, errors.New("timeout"), now)
	require.NotNil(t, retained.Eligible)
	require.True(t, *retained.Eligible)
	require.Equal(t, previous.ObservedAt, retained.ObservedAt)
	require.True(t, retained.Stale)

	forbidden := buildGrokMediaEligibilitySnapshot(retained, http.StatusForbidden, errors.New("forbidden"), http.StatusOK, nil, now.Add(time.Minute))
	require.NotNil(t, forbidden.Eligible)
	require.False(t, *forbidden.Eligible)
	require.False(t, forbidden.Stale)
	require.Equal(t, http.StatusForbidden, forbidden.WeeklyStatusCode)
	require.Equal(t, http.StatusOK, forbidden.MonthlyStatusCode)

	allowed := buildGrokMediaEligibilitySnapshot(forbidden, http.StatusOK, nil, http.StatusOK, nil, now.Add(2*time.Minute))
	require.NotNil(t, allowed.Eligible)
	require.True(t, *allowed.Eligible)
	require.False(t, allowed.Stale)
}

func TestGrokMediaCapabilityFiltersOnlyNewGeneration(t *testing.T) {
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra:       map[string]any{GrokMediaEligibleExtraKey: false},
	}
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityGrokMediaGeneration))
}

func TestNormalizeGrokMediaEligibilityExtra(t *testing.T) {
	t.Run("boolean override accepted", func(t *testing.T) {
		extra, err := normalizeGrokMediaEligibilityExtra(PlatformGrok, map[string]any{GrokMediaEligibleExtraKey: false})
		require.NoError(t, err)
		require.Equal(t, false, extra[GrokMediaEligibleExtraKey])
	})
	t.Run("null clears override and managed snapshots", func(t *testing.T) {
		extra, err := normalizeGrokMediaEligibilityExtra(PlatformGrok, map[string]any{
			GrokMediaEligibleExtraKey:            nil,
			grokBillingExtraKey:                  map[string]any{"status_code": 403},
			GrokMediaEligibilitySnapshotExtraKey: map[string]any{"eligible": false},
		})
		require.NoError(t, err)
		require.NotContains(t, extra, GrokMediaEligibleExtraKey)
		require.NotContains(t, extra, grokBillingExtraKey)
		require.NotContains(t, extra, GrokMediaEligibilitySnapshotExtraKey)
	})
	t.Run("malformed rejected", func(t *testing.T) {
		_, err := normalizeGrokMediaEligibilityExtra(PlatformGrok, map[string]any{GrokMediaEligibleExtraKey: "false"})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	})
	t.Run("other platform rejected", func(t *testing.T) {
		_, err := normalizeGrokMediaEligibilityExtra(PlatformOpenAI, map[string]any{GrokMediaEligibleExtraKey: false})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	})
}

func TestNormalizeGrokMediaEligibilityUpdatePreservesManagedEvidence(t *testing.T) {
	account := &Account{Platform: PlatformGrok, Extra: map[string]any{
		GrokMediaEligibleExtraKey:            false,
		grokBillingExtraKey:                  map[string]any{"weekly_status_code": 403},
		GrokMediaEligibilitySnapshotExtraKey: map[string]any{"eligible": false},
	}}
	normalized, err := normalizeGrokMediaEligibilityUpdateExtra(account, map[string]any{"custom": "new"})
	require.NoError(t, err)
	require.Equal(t, false, normalized[GrokMediaEligibleExtraKey])
	require.Contains(t, normalized, grokBillingExtraKey)
	require.Contains(t, normalized, GrokMediaEligibilitySnapshotExtraKey)

	cleared, err := normalizeGrokMediaEligibilityUpdateExtra(account, map[string]any{GrokMediaEligibleExtraKey: nil})
	require.NoError(t, err)
	require.NotContains(t, cleared, GrokMediaEligibleExtraKey)
}
