//go:build integration

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)

	bucket := service.SchedulerBucket{GroupID: 2, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	now := time.Now().UTC().Truncate(time.Second)
	limitReset := now.Add(10 * time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	tempUnschedUntil := now.Add(3 * time.Minute)
	windowEnd := now.Add(5 * time.Hour)

	account := service.Account{
		ID:          101,
		Name:        "gemini-heavy",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    7,
		LastUsedAt:  &now,
		Credentials: map[string]any{
			"api_key":       "gemini-api-key",
			"access_token":  "secret-access-token",
			"project_id":    "proj-1",
			"oauth_type":    "ai_studio",
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
			"huge_blob":     strings.Repeat("x", 4096),
		},
		Extra: map[string]any{
			"mixed_scheduling":                true,
			service.KeywordPolicyModeExtraKey: service.KeywordPolicyModeAllow,
			"window_cost_limit":               12.5,
			"window_cost_sticky_reserve":      8.0,
			"max_sessions":                    4,
			"session_idle_timeout_minutes":    11,
			"unused_large_field":              strings.Repeat("y", 4096),
		},
		RateLimitResetAt:       &limitReset,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		SessionWindowStart:     &now,
		SessionWindowEnd:       &windowEnd,
		SessionWindowStatus:    "active",
		GroupIDs:               []int64{bucket.GroupID},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 101,
				GroupID:   bucket.GroupID,
				Priority:  5,
				Group:     &service.Group{ID: bucket.GroupID, Name: "gemini-group"},
			},
		},
	}

	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)

	got := snapshot[0]
	require.NotNil(t, got)
	require.Equal(t, "gemini-api-key", got.GetCredential("api_key"))
	require.Equal(t, "proj-1", got.GetCredential("project_id"))
	require.Equal(t, "ai_studio", got.GetCredential("oauth_type"))
	require.NotEmpty(t, got.GetModelMapping())
	require.Empty(t, got.GetCredential("access_token"))
	require.Empty(t, got.GetCredential("huge_blob"))
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Equal(t, service.KeywordPolicyModeAllow, got.KeywordPolicyMode())
	require.Equal(t, 12.5, got.GetWindowCostLimit())
	require.Equal(t, 8.0, got.GetWindowCostStickyReserve())
	require.Equal(t, 4, got.GetMaxSessions())
	require.Equal(t, 11, got.GetSessionIdleTimeoutMinutes())
	require.Nil(t, got.Extra["unused_large_field"])
	require.Equal(t, []int64{bucket.GroupID}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 1)
	require.Equal(t, account.ID, got.AccountGroups[0].AccountID)
	require.Equal(t, bucket.GroupID, got.AccountGroups[0].GroupID)
	require.Nil(t, got.AccountGroups[0].Group)

	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
	require.Equal(t, strings.Repeat("x", 4096), full.GetCredential("huge_blob"))
	require.Len(t, full.AccountGroups, 1)
	require.NotNil(t, full.AccountGroups[0].Group)

	batchCache, ok := cache.(service.SchedulerAccountConcurrencyBatchCache)
	require.True(t, ok)
	limits, err := batchCache.GetAccountConcurrencyLimits(ctx, []int64{account.ID, account.ID, 999})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{account.ID: account.Concurrency}, limits)
}

func TestSchedulerCacheMirrorUpdateKeepsLocalGrokMediaEligibility(t *testing.T) {
	ctx := context.Background()
	cache := NewSchedulerCache(testRedis(t))
	accountID := int64(102)
	local := &service.Account{
		ID: accountID, Platform: service.PlatformGrok, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "local-secret"},
		Extra: map[string]any{
			service.GrokMediaEligibleExtraKey: false,
			service.GrokMediaEligibilitySnapshotExtraKey: map[string]any{
				"eligible": false, "weekly_status_code": 403,
			},
		},
	}
	require.NoError(t, cache.SetAccount(ctx, local))

	incoming := *local
	incoming.Credentials = map[string]any{"base_url": "https://api.x.ai/v1"}
	incoming.Extra = map[string]any{
		"gateway_upstream_mirror_id": int64(88),
		service.GrokMediaEligibilitySnapshotExtraKey: map[string]any{
			"eligible": true, "weekly_status_code": 200,
		},
	}
	require.NoError(t, cache.SetAccount(ctx, &incoming))

	full, err := cache.GetAccount(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, "local-secret", full.GetCredential("access_token"))
	require.Equal(t, false, full.Extra[service.GrokMediaEligibleExtraKey])
	details := full.GrokMediaGenerationEligibilityDetails()
	require.False(t, details.EffectiveEligible)
	require.Equal(t, "override_disabled", details.Reason)

	bucket := service.SchedulerBucket{GroupID: 9, Platform: service.PlatformGrok, Mode: service.SchedulerModeForced}
	incoming.GroupIDs = []int64{bucket.GroupID}
	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{incoming}))
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.Equal(t, false, snapshot[0].Extra[service.GrokMediaEligibleExtraKey])
}
