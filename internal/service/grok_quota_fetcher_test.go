//go:build unit

package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func grokInt64PtrForTest(v int64) *int64 { return &v }
func grokIntPtrForTest(v int) *int       { return &v }

func TestGrokQuotaFetcherBuildUsageInfoUnknownUntilFirstSnapshot(t *testing.T) {
	t.Parallel()

	usage := NewGrokQuotaFetcher().BuildUsageInfo(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth})
	require.Equal(t, "passive", usage.Source)
	require.Equal(t, "quota_unknown", usage.ErrorCode)
	require.Contains(t, usage.Error, "unknown until billing is probed")
}

func TestGrokQuotaFetcherPrefersAccessTokenTier(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":      grokTierJWT(map[string]any{"tier": 0}),
			"subscription_tier": "supergrok_heavy",
		},
		Extra: map[string]any{grokBillingExtraKey: &xai.BillingSummary{
			Plan: "SuperGrok Heavy", StatusCode: http.StatusOK,
		}},
	}

	usage := NewGrokQuotaFetcher().BuildUsageInfo(account)
	require.Equal(t, "free", usage.SubscriptionTier)
	require.Equal(t, "free", usage.SubscriptionTierRaw)
}

func TestGrokQuotaFetcherInfersHeavyOnlyFromFreshGrok45Window(t *testing.T) {
	t.Parallel()

	heavyRequests := int64(8_300)
	fresh := time.Now().UTC().Format(time.RFC3339)
	stale := time.Now().Add(-xai.GrokQuotaSignalMaxAge - time.Hour).UTC().Format(time.RFC3339)
	tests := []struct {
		name       string
		model      string
		observedAt string
		want       string
	}{
		{name: "fresh grok 4.5", model: "grok-4.5", observedAt: fresh, want: "supergrok_heavy"},
		{name: "different model", model: "grok-4.6", observedAt: fresh, want: "supergrok"},
		{name: "stale grok 4.5", model: "grok-4.5", observedAt: stale, want: "supergrok"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"subscription_tier": "SuperGrokPro",
				},
				Extra: map[string]any{grokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
					Model: tt.model, Requests: &xai.QuotaWindow{Limit: &heavyRequests},
					HeadersObserved: true, LastHeadersSeenAt: tt.observedAt, UpdatedAt: tt.observedAt,
				}},
			}
			require.Equal(t, tt.want, NewGrokQuotaFetcher().BuildUsageInfo(account).SubscriptionTier)
		})
	}
}

func TestGrokQuotaFetcherJWTAlwaysBeatsQuotaInference(t *testing.T) {
	t.Parallel()

	heavyRequests := int64(8_300)
	fresh := time.Now().UTC().Format(time.RFC3339)
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":      grokTierJWT(map[string]any{"tier": 1}),
			"subscription_tier": "SuperGrokPro",
		},
		Extra: map[string]any{grokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
			Model: "grok-4.5", Requests: &xai.QuotaWindow{Limit: &heavyRequests},
			HeadersObserved: true, LastHeadersSeenAt: fresh, UpdatedAt: fresh,
		}},
	}

	require.Equal(t, "supergrok", NewGrokQuotaFetcher().BuildUsageInfo(account).SubscriptionTier)
}

func TestGrokQuotaFetcherBuildUsageInfoFromSnapshot(t *testing.T) {
	t.Parallel()

	updatedAt := "2030-01-01T00:00:00Z"
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: &xai.QuotaSnapshot{
				Requests: &xai.QuotaWindow{
					Limit:     grokInt64PtrForTest(100),
					Remaining: grokInt64PtrForTest(12),
					ResetAt:   updatedAt,
				},
				Tokens: &xai.QuotaWindow{
					Limit:     grokInt64PtrForTest(1000),
					Remaining: grokInt64PtrForTest(900),
				},
				RetryAfterSeconds: grokIntPtrForTest(30),
				SubscriptionTier:  "supergrok",
				EntitlementStatus: "active",
				StatusCode:        http.StatusTooManyRequests,
				LastProbeAt:       updatedAt,
				LastHeadersSeenAt: updatedAt,
				UpdatedAt:         updatedAt,
			},
		},
	}

	usage := NewGrokQuotaFetcher().BuildUsageInfo(account)
	require.Equal(t, "passive", usage.Source)
	require.Equal(t, "rate_limited", usage.ErrorCode)
	require.Equal(t, "observed", usage.GrokQuotaSnapshotState)
	require.Equal(t, "supergrok", usage.SubscriptionTier)
	require.Equal(t, "active", usage.GrokEntitlementStatus)
	require.Equal(t, int64(100), *usage.GrokRequestQuota.Limit)
	require.Equal(t, int64(12), *usage.GrokRequestQuota.Remaining)
	require.Equal(t, 30, *usage.GrokRetryAfterSeconds)
	require.NotNil(t, usage.UpdatedAt)
	require.Equal(t, updatedAt, usage.GrokLastQuotaProbeAt)
	require.Equal(t, updatedAt, usage.GrokLastHeadersSeenAt)
	require.Equal(t, http.StatusTooManyRequests, usage.GrokLastStatusCode)
	require.True(t, usage.UpdatedAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)))
}

func TestGrokQuotaFetcherBuildUsageInfoFromNoHeadersProbe(t *testing.T) {
	t.Parallel()

	probedAt := "2030-01-01T00:00:00Z"
	account := &Account{
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
				StatusCode:        http.StatusOK,
				HeadersObserved:   false,
				ObservationSource: "active_probe",
				LastProbeAt:       probedAt,
				UpdatedAt:         probedAt,
			},
		},
	}

	usage := NewGrokQuotaFetcher().BuildUsageInfo(account)
	require.Equal(t, "quota_unknown", usage.ErrorCode)
	require.Equal(t, "no_headers", usage.GrokQuotaSnapshotState)
	require.Contains(t, usage.Error, "No xAI quota headers observed")
	require.Equal(t, probedAt, usage.GrokLastQuotaProbeAt)
	require.Empty(t, usage.GrokLastHeadersSeenAt)
	require.Equal(t, http.StatusOK, usage.GrokLastStatusCode)
	require.Nil(t, usage.GrokRequestQuota)
	require.Nil(t, usage.GrokTokenQuota)
}

func TestGrokQuotaFetcherClassifiesForbiddenAndReauth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		statusCode  int
		wantReauth  bool
		wantForbid  bool
		wantCode    string
		wantEntitle string
	}{
		{name: "reauth", statusCode: http.StatusUnauthorized, wantReauth: true, wantCode: "unauthenticated"},
		{name: "forbidden", statusCode: http.StatusForbidden, wantForbid: true, wantCode: "forbidden", wantEntitle: "forbidden"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			account := &Account{
				Platform: PlatformGrok,
				Type:     AccountTypeOAuth,
				Extra: map[string]any{
					grokQuotaSnapshotExtraKey: xai.QuotaSnapshot{
						StatusCode:      tt.statusCode,
						HeadersObserved: true,
						UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
					},
				},
			}
			usage := NewGrokQuotaFetcher().BuildUsageInfo(account)
			require.Equal(t, tt.wantReauth, usage.NeedsReauth)
			require.Equal(t, tt.wantForbid, usage.IsForbidden)
			require.Equal(t, tt.wantCode, usage.ErrorCode)
			require.Equal(t, tt.wantEntitle, usage.GrokEntitlementStatus)
		})
	}
}
