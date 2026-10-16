package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheGroupPreservesPeakRatePolicy(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewSchedulerCache(rdb).(service.SchedulerGroupCache)

	group := &service.Group{
		ID:                 27,
		Name:               "peak-rate",
		Platform:           service.PlatformOpenAI,
		Status:             service.StatusActive,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "06:00",
		PeakRateMultiplier: 1.75,
		PeakTimezone:       "Asia/Shanghai",
	}
	require.NoError(t, cache.SetGroup(ctx, group))

	got, err := cache.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.PeakRateEnabled)
	require.Equal(t, "22:00", got.PeakStart)
	require.Equal(t, "06:00", got.PeakEnd)
	require.Equal(t, 1.75, got.PeakRateMultiplier)
	require.Equal(t, "Asia/Shanghai", got.PeakTimezone)
}

func TestSchedulerCacheGroupPreservesReasoningEffortPolicy(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewSchedulerCache(rdb).(service.SchedulerGroupCache)

	group := &service.Group{
		ID:                 28,
		Name:               "reasoning-policy",
		Platform:           service.PlatformOpenAI,
		Status:             service.StatusActive,
		MaxReasoningEffort: "high",
		ReasoningEffortMappings: []service.ReasoningEffortMapping{
			{From: "max", To: "medium"},
		},
	}
	require.NoError(t, cache.SetGroup(ctx, group))

	got, err := cache.GetGroup(ctx, group.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "high", got.MaxReasoningEffort)
	require.Equal(t, group.ReasoningEffortMappings, got.ReasoningEffortMappings)
}

func TestSchedulerCacheGroupIgnoresLegacyReasoningPolicySnapshot(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewSchedulerCache(rdb).(service.SchedulerGroupCache)

	require.NoError(t, rdb.Set(ctx, "sched:group:30", `{"ID":30,"Platform":"openai","Status":"active"}`, 0).Err())
	got, err := cache.GetGroup(ctx, 30)
	require.NoError(t, err)
	require.Nil(t, got, "升级前缺少思考策略字段的永久缓存必须回源重建")

	fresh := &service.Group{ID: 30, Platform: service.PlatformOpenAI, Status: service.StatusActive, MaxReasoningEffort: "high"}
	require.NoError(t, cache.SetGroup(ctx, fresh))
	require.True(t, mini.Exists("sched:group:v2:30"))
}

func TestBuildSchedulerMetadataGroupDeepCopiesReasoningEffortMappings(t *testing.T) {
	group := service.Group{
		ID:                 29,
		MaxReasoningEffort: "high",
		ReasoningEffortMappings: []service.ReasoningEffortMapping{
			{From: "max", To: "medium"},
		},
	}

	metadata := buildSchedulerMetadataGroup(group)
	group.ReasoningEffortMappings[0].To = "low"

	require.Equal(t, "high", metadata.MaxReasoningEffort)
	require.Equal(t, []service.ReasoningEffortMapping{{From: "max", To: "medium"}}, metadata.ReasoningEffortMappings)
}

func TestMergeGatewayLiteMirrorCredentialsPreservesLocalSecrets(t *testing.T) {
	existing := &service.Account{
		Credentials: map[string]any{
			"api_key":       "local-secret",
			"base_url":      "https://old.example/v1",
			"model_mapping": map[string]any{"old": "old"},
		},
	}
	incoming := map[string]any{
		"base_url":      "https://mimo.example/v1",
		"model_mapping": map[string]any{"mimo-v2.5-pro": "mimo-v2.5-pro"},
	}

	merged := mergeGatewayLiteMirrorCredentials(incoming, existing)

	require.Equal(t, "local-secret", merged["api_key"])
	require.Equal(t, "https://mimo.example/v1", merged["base_url"])
	require.Equal(t, map[string]any{"mimo-v2.5-pro": "mimo-v2.5-pro"}, merged["model_mapping"])
}

func TestGatewayLiteMirrorSnapshotAccountRequiresMirrorMarker(t *testing.T) {
	require.False(t, gatewayLiteMirrorSnapshotAccount(service.Account{}))
	require.True(t, gatewayLiteMirrorSnapshotAccount(service.Account{Extra: map[string]any{"gateway_upstream_mirror_id": int64(10)}}))
}

func TestMergeGatewayLiteMirrorExtraPreservesLocalGrokMediaEvidence(t *testing.T) {
	existing := &service.Account{Extra: map[string]any{
		service.GrokMediaEligibleExtraKey: false,
		service.GrokMediaEligibilitySnapshotExtraKey: map[string]any{
			"eligible": false, "weekly_status_code": 403,
		},
		"grok_billing_snapshot": map[string]any{"weekly_status_code": 403},
		"local_only":            "must-not-spread",
	}}
	incoming := map[string]any{
		"gateway_upstream_mirror_id": int64(10),
		service.GrokMediaEligibilitySnapshotExtraKey: map[string]any{
			"eligible": true, "weekly_status_code": 200,
		},
	}

	merged := mergeGatewayLiteMirrorExtra(incoming, existing)
	require.Equal(t, false, merged[service.GrokMediaEligibleExtraKey])
	require.Equal(t, map[string]any{"eligible": false, "weekly_status_code": 403}, merged[service.GrokMediaEligibilitySnapshotExtraKey])
	require.Equal(t, map[string]any{"weekly_status_code": 403}, merged["grok_billing_snapshot"])
	require.NotContains(t, merged, "local_only")
}

func TestMergeGatewayLiteMirrorExtraAllowsExplicitOverrideAndClear(t *testing.T) {
	existing := &service.Account{Extra: map[string]any{service.GrokMediaEligibleExtraKey: false}}
	require.Equal(t, true, mergeGatewayLiteMirrorExtra(map[string]any{service.GrokMediaEligibleExtraKey: true}, existing)[service.GrokMediaEligibleExtraKey])
	require.NotContains(t, mergeGatewayLiteMirrorExtra(map[string]any{service.GrokMediaEligibleExtraKey: nil}, existing), service.GrokMediaEligibleExtraKey)
}
