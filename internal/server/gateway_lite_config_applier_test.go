package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type gatewayLiteConfigApplierGroupRepository struct {
	service.GroupRepository
	groups map[int64]*service.Group
	err    error
	calls  int
}

func (r *gatewayLiteConfigApplierGroupRepository) List(_ context.Context, params pagination.PaginationParams) ([]service.Group, *pagination.PaginationResult, error) {
	r.calls++
	if r.err != nil {
		return nil, nil, r.err
	}
	groups := make([]service.Group, 0, len(r.groups))
	for _, group := range r.groups {
		if group != nil {
			groups = append(groups, *group)
		}
	}
	return groups, &pagination.PaginationResult{Total: int64(len(groups)), Page: params.Page, PageSize: params.Limit(), Pages: 1}, nil
}

func TestGatewayLiteSchedulerConfigApplierKeepsLocalSchedulerAuthoritative(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := repository.NewSchedulerCache(client)
	applier := newGatewayLiteSchedulerConfigApplier(cache)

	localBucket := service.SchedulerBucket{
		GroupID:  7,
		Platform: service.PlatformOpenAI,
		Mode:     service.SchedulerModeSingle,
	}
	localGroup := &service.Group{
		ID:                 7,
		Name:               "local-openai",
		Platform:           service.PlatformOpenAI,
		Status:             service.StatusActive,
		RateMultiplier:     1,
		RequirePrivacySet:  false,
		DefaultMappedModel: "local-model",
		Hydrated:           true,
	}
	groupCache, ok := cache.(service.SchedulerGroupCache)
	require.True(t, ok)
	require.NoError(t, groupCache.SetGroup(ctx, localGroup))
	require.NoError(t, cache.SetAccount(ctx, &service.Account{
		ID:          2001,
		Name:        "local-openai-primary",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "local-secret"},
		GroupIDs:    []int64{7},
		Groups:      []*service.Group{localGroup},
	}))
	require.NoError(t, cache.SetSnapshot(ctx, localBucket, []service.Account{{
		ID:          2001,
		Name:        "local-openai-primary",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "local-secret"},
		GroupIDs:    []int64{7},
		Groups:      []*service.Group{localGroup},
	}}))

	require.NoError(t, applier.ApplyGatewayConfigSnapshot(ctx, gatewaylite.GatewayConfigSnapshot{
		Version: 1,
		Groups: []gatewaylite.GatewayGroupSnapshot{{
			ID:                    7,
			Name:                  "mirror-openai",
			Platform:              service.PlatformOpenAI,
			Status:                service.StatusActive,
			RateMultiplier:        1,
			RequirePrivacySet:     true,
			AllowImageGeneration:  true,
			AllowMessagesDispatch: true,
			ModelsListConfig: gatewaylite.GroupModelsListConfig{
				Enabled: true,
				Models:  []string{"gpt-5-mini"},
			},
			Config: map[string]any{
				"rpm_limit":            88,
				"default_mapped_model": "gpt-5-mini",
			},
		}},
		Accounts: []gatewaylite.GatewayAccountSnapshot{{
			ID:          1001,
			Name:        "mirror-openai-primary",
			Platform:    service.PlatformOpenAI,
			Type:        "api_key",
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    100,
			Credentials: map[string]any{"api_key": "secret"},
			GroupIDs:    []int64{7},
		}},
	}))

	got, hit, err := cache.GetSnapshot(ctx, localBucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, got, 1)
	require.EqualValues(t, 2001, got[0].ID)
	require.Equal(t, service.PlatformOpenAI, got[0].Platform)
	require.Equal(t, []int64{7}, got[0].GroupIDs)
	require.Len(t, got[0].Groups, 1)
	require.False(t, got[0].Groups[0].RequirePrivacySet)
	require.Equal(t, "local-model", got[0].Groups[0].DefaultMappedModel)

	account, err := cache.GetAccount(ctx, 1001)
	require.NoError(t, err)
	require.Nil(t, account)

	group, err := groupCache.GetGroup(ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, group)
	require.Equal(t, "local-openai", group.Name)
	require.False(t, group.RequirePrivacySet)
}

func TestGatewayLiteGroupSnapshotToServiceGroupPreservesDirectPeakRatePolicy(t *testing.T) {
	group := gatewayLiteGroupSnapshotToServiceGroup(gatewaylite.GatewayGroupSnapshot{
		ID:                 7,
		Name:               "peak-direct",
		Platform:           service.PlatformOpenAI,
		Status:             service.StatusActive,
		RateMultiplier:     1.25,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "06:00",
		PeakRateMultiplier: 1.6,
		PeakTimezone:       "Asia/Shanghai",
	})

	require.True(t, group.PeakRateEnabled)
	require.Equal(t, "22:00", group.PeakStart)
	require.Equal(t, "06:00", group.PeakEnd)
	require.Equal(t, 1.6, group.PeakRateMultiplier)
	require.Equal(t, "Asia/Shanghai", group.PeakTimezone)
}

func TestGatewayLiteGroupSnapshotToServiceGroupFallsBackToLegacyPeakRateConfig(t *testing.T) {
	group := gatewayLiteGroupSnapshotToServiceGroup(gatewaylite.GatewayGroupSnapshot{
		ID:             8,
		Name:           "peak-config-fallback",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		RateMultiplier: 1,
		Config: map[string]any{
			"peak_rate_enabled":    true,
			"peak_start":           "09:30",
			"peak_end":             "18:30",
			"peak_rate_multiplier": 1.8,
			"peak_timezone":        "UTC",
		},
	})

	require.True(t, group.PeakRateEnabled)
	require.Equal(t, "09:30", group.PeakStart)
	require.Equal(t, "18:30", group.PeakEnd)
	require.Equal(t, 1.8, group.PeakRateMultiplier)
	require.Equal(t, "UTC", group.PeakTimezone)
}

func TestGatewayLiteGroupSnapshotToServiceGroupPreservesBillingAndReasoningPolicy(t *testing.T) {
	group := gatewayLiteGroupSnapshotToServiceGroup(gatewaylite.GatewayGroupSnapshot{
		ID:                 9,
		Platform:           service.PlatformOpenAI,
		AllowLive:          true,
		BillingModelSource: service.BillingModelSourceRequested,
		MaxReasoningEffort: "high",
		ReasoningEffortMappings: []gatewaylite.ReasoningEffortMapping{
			{From: "max", To: "high"},
		},
	})

	require.True(t, group.AllowLive)
	require.Equal(t, service.BillingModelSourceRequested, group.BillingModelSource)
	require.Equal(t, "high", group.MaxReasoningEffort)
	require.Equal(t, []service.ReasoningEffortMapping{{From: "max", To: "high"}}, group.ReasoningEffortMappings)
}

func TestGatewayLiteSchedulerConfigApplierDoesNotClearLocalAccountsMissingFromSnapshot(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := repository.NewSchedulerCache(client)
	applier := newGatewayLiteSchedulerConfigApplier(cache)

	localGroup := &service.Group{
		ID:             1,
		Name:           "local-openai",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		RateMultiplier: 1,
		Hydrated:       true,
	}
	require.NoError(t, cache.SetAccount(ctx, &service.Account{
		ID:          7,
		Name:        "local-7",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{1},
		Groups:      []*service.Group{localGroup},
	}))
	require.NoError(t, cache.SetAccount(ctx, &service.Account{
		ID:          8,
		Name:        "local-8",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{1},
		Groups:      []*service.Group{localGroup},
	}))
	oldBucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	require.NoError(t, cache.SetSnapshot(ctx, oldBucket, []service.Account{
		{
			ID:          7,
			Name:        "local-7",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			GroupIDs:    []int64{1},
			Groups:      []*service.Group{localGroup},
		},
		{
			ID:          8,
			Name:        "local-8",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			GroupIDs:    []int64{1},
			Groups:      []*service.Group{localGroup},
		},
	}))

	oldAccounts, hit, err := cache.GetSnapshot(ctx, oldBucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, oldAccounts, 2)

	require.NoError(t, applier.ApplyGatewayConfigSnapshot(ctx, gatewaylite.GatewayConfigSnapshot{
		Version: 2,
		Groups: []gatewaylite.GatewayGroupSnapshot{
			{
				ID:               1,
				Name:             "openai-empty",
				Platform:         service.PlatformOpenAI,
				Status:           service.StatusActive,
				RateMultiplier:   1,
				SubscriptionType: "standard",
			},
			{
				ID:               2,
				Name:             "openai-new",
				Platform:         service.PlatformOpenAI,
				Status:           service.StatusActive,
				RateMultiplier:   1,
				SubscriptionType: "standard",
			},
		},
		Accounts: []gatewaylite.GatewayAccountSnapshot{{
			ID:          9,
			Name:        "new-9",
			Platform:    service.PlatformOpenAI,
			Type:        "api_key",
			Status:      service.StatusActive,
			Schedulable: true,
			GroupIDs:    []int64{2},
		}},
	}))

	oldAccounts, hit, err = cache.GetSnapshot(ctx, oldBucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, oldAccounts, 2)

	oldAccount7, err := cache.GetAccount(ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, oldAccount7)
	oldAccount8, err := cache.GetAccount(ctx, 8)
	require.NoError(t, err)
	require.NotNil(t, oldAccount8)

	newAccounts, hit, err := cache.GetSnapshot(ctx, service.SchedulerBucket{GroupID: 2, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle})
	require.NoError(t, err)
	require.False(t, hit)
	require.Empty(t, newAccounts)
}

func TestGatewayLiteSchedulerConfigApplierLegacySnapshotAuthorityRestoresOldBehavior(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := repository.NewSchedulerCache(client)
	applier := newGatewayLiteSchedulerConfigApplier(cache).
		WithGroupRepository(&gatewayLiteConfigApplierGroupRepository{}).
		WithLegacySnapshotSchedulerAuthority(true)

	localGroup := &service.Group{
		ID:             1,
		Name:           "local-openai",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		RateMultiplier: 1,
		Hydrated:       true,
	}
	localBucket := service.SchedulerBucket{GroupID: 1, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	require.NoError(t, cache.SetAccount(ctx, &service.Account{
		ID:          7,
		Name:        "local-7",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{1},
		Groups:      []*service.Group{localGroup},
	}))
	require.NoError(t, cache.SetSnapshot(ctx, localBucket, []service.Account{{
		ID:          7,
		Name:        "local-7",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		GroupIDs:    []int64{1},
		Groups:      []*service.Group{localGroup},
	}}))

	require.NoError(t, applier.ApplyGatewayConfigSnapshot(ctx, gatewaylite.GatewayConfigSnapshot{
		Version: 3,
		Groups: []gatewaylite.GatewayGroupSnapshot{{
			ID:             2,
			Name:           "mirror-openai",
			Platform:       service.PlatformOpenAI,
			Status:         service.StatusActive,
			RateMultiplier: 1.2,
		}},
		Accounts: []gatewaylite.GatewayAccountSnapshot{{
			ID:          9,
			Name:        "mirror-9",
			Platform:    service.PlatformOpenAI,
			Type:        "api_key",
			Status:      service.StatusActive,
			Schedulable: true,
			GroupIDs:    []int64{2},
		}},
	}))

	oldAccount, err := cache.GetAccount(ctx, 7)
	require.NoError(t, err)
	require.Nil(t, oldAccount)

	groupCache, ok := cache.(service.SchedulerGroupCache)
	require.True(t, ok)
	mirrorGroup, err := groupCache.GetGroup(ctx, 2)
	require.NoError(t, err)
	require.NotNil(t, mirrorGroup)
	require.Equal(t, "mirror-openai", mirrorGroup.Name)
	require.Equal(t, 1.2, mirrorGroup.RateMultiplier)

	newAccounts, hit, err := cache.GetSnapshot(ctx, service.SchedulerBucket{GroupID: 2, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle})
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, newAccounts, 1)
	require.EqualValues(t, 9, newAccounts[0].ID)
	require.Equal(t, "mirror-9", newAccounts[0].Name)
	require.Len(t, newAccounts[0].Groups, 1)
	require.Equal(t, "mirror-openai", newAccounts[0].Groups[0].Name)
}

func TestGatewayLiteSchedulerConfigApplierLegacySnapshotAuthorityPreservesLocalOnlyBoundary(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := repository.NewSchedulerCache(client)
	localGroup := &service.Group{
		ID:             7,
		Name:           "local-authoritative",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		RateMultiplier: 1.4,
		LocalOnly:      true,
		Hydrated:       true,
	}
	hiddenFromSnapshotGroup := &service.Group{
		ID:             11,
		Name:           "local-not-in-snapshot",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		RateMultiplier: 1,
		LocalOnly:      true,
		Hydrated:       true,
	}
	disabledLocalGroup := &service.Group{
		ID:             12,
		Name:           "disabled-local-authoritative",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusDisabled,
		RateMultiplier: 1,
		LocalOnly:      true,
		Hydrated:       true,
	}
	repo := &gatewayLiteConfigApplierGroupRepository{groups: map[int64]*service.Group{
		7:  localGroup,
		11: hiddenFromSnapshotGroup,
		12: disabledLocalGroup,
	}}
	applier := newGatewayLiteSchedulerConfigApplier(cache).
		WithGroupRepository(repo).
		WithLegacySnapshotSchedulerAuthority(true)

	groupCache, ok := cache.(service.SchedulerGroupCache)
	require.True(t, ok)
	require.NoError(t, groupCache.SetGroup(ctx, &service.Group{
		ID:             7,
		Name:           "stale-mirror",
		Platform:       service.PlatformOpenAI,
		Status:         service.StatusActive,
		RateMultiplier: 1,
		LocalOnly:      false,
		Hydrated:       true,
	}))
	localAccount := service.Account{
		ID:          2001,
		Name:        "local-account",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "local-secret"},
		GroupIDs:    []int64{7},
		Groups:      []*service.Group{localGroup},
	}
	localBucket := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	require.NoError(t, cache.SetAccount(ctx, &localAccount))
	require.NoError(t, cache.SetSnapshot(ctx, localBucket, []service.Account{localAccount}))
	hiddenFromSnapshotAccount := service.Account{
		ID:          2011,
		Name:        "local-account-not-in-snapshot",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "local-secret-11"},
		GroupIDs:    []int64{11},
		Groups:      []*service.Group{hiddenFromSnapshotGroup},
	}
	hiddenFromSnapshotBucket := service.SchedulerBucket{GroupID: 11, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	require.NoError(t, groupCache.SetGroup(ctx, hiddenFromSnapshotGroup))
	require.NoError(t, groupCache.SetGroup(ctx, &service.Group{
		ID: 12, Name: "stale-public-active", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, RateMultiplier: 1, Hydrated: true,
	}))
	require.NoError(t, cache.SetAccount(ctx, &hiddenFromSnapshotAccount))
	require.NoError(t, cache.SetSnapshot(ctx, hiddenFromSnapshotBucket, []service.Account{hiddenFromSnapshotAccount}))

	require.NoError(t, applier.ApplyGatewayConfigSnapshot(ctx, gatewaylite.GatewayConfigSnapshot{
		Version: 4,
		Groups: []gatewaylite.GatewayGroupSnapshot{
			{
				ID:             7,
				Name:           "legacy-mirror-without-local-only",
				Platform:       service.PlatformOpenAI,
				Status:         service.StatusActive,
				RateMultiplier: 1,
			},
			{
				ID:             8,
				Name:           "public-group",
				Platform:       service.PlatformOpenAI,
				Status:         service.StatusActive,
				RateMultiplier: 1,
			},
			{
				ID:             12,
				Name:           "stale-public-active",
				Platform:       service.PlatformOpenAI,
				Status:         service.StatusActive,
				RateMultiplier: 1,
			},
		},
		Accounts: []gatewaylite.GatewayAccountSnapshot{
			{
				ID:          1001,
				Name:        "local-only-mirror-account",
				Platform:    service.PlatformOpenAI,
				Type:        "api_key",
				Status:      service.StatusActive,
				Schedulable: true,
				GroupIDs:    []int64{7},
			},
			{
				ID:          1002,
				Name:        "mixed-mirror-account",
				Platform:    service.PlatformOpenAI,
				Type:        "api_key",
				Status:      service.StatusActive,
				Schedulable: true,
				GroupIDs:    []int64{7, 8},
			},
		},
	}))

	cachedLocalGroup, err := groupCache.GetGroup(ctx, 7)
	require.NoError(t, err)
	require.NotNil(t, cachedLocalGroup)
	require.True(t, cachedLocalGroup.LocalOnly)
	require.Equal(t, "local-authoritative", cachedLocalGroup.Name)
	require.Equal(t, 1.4, cachedLocalGroup.RateMultiplier)
	cachedDisabledLocalGroup, err := groupCache.GetGroup(ctx, 12)
	require.NoError(t, err)
	require.NotNil(t, cachedDisabledLocalGroup)
	require.True(t, cachedDisabledLocalGroup.LocalOnly)
	require.Equal(t, service.StatusDisabled, cachedDisabledLocalGroup.Status)

	localAccounts, hit, err := cache.GetSnapshot(ctx, localBucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, localAccounts, 1)
	require.EqualValues(t, 2001, localAccounts[0].ID)
	require.Equal(t, []int64{7}, localAccounts[0].GroupIDs)
	require.True(t, localAccounts[0].Groups[0].LocalOnly)
	hiddenFromSnapshotAccounts, hit, err := cache.GetSnapshot(ctx, hiddenFromSnapshotBucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, hiddenFromSnapshotAccounts, 1)
	require.EqualValues(t, 2011, hiddenFromSnapshotAccounts[0].ID)
	require.True(t, hiddenFromSnapshotAccounts[0].Groups[0].LocalOnly)
	hiddenFromSnapshotCachedAccount, err := cache.GetAccount(ctx, 2011)
	require.NoError(t, err)
	require.NotNil(t, hiddenFromSnapshotCachedAccount)

	localOnlyMirrorAccount, err := cache.GetAccount(ctx, 1001)
	require.NoError(t, err)
	require.Nil(t, localOnlyMirrorAccount)
	publicAccounts, hit, err := cache.GetSnapshot(ctx, service.SchedulerBucket{GroupID: 8, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle})
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, publicAccounts, 1)
	require.EqualValues(t, 1002, publicAccounts[0].ID)
	require.Equal(t, []int64{8}, publicAccounts[0].GroupIDs)
	require.Len(t, publicAccounts[0].Groups, 1)
	require.EqualValues(t, 8, publicAccounts[0].Groups[0].ID)
}

func TestGatewayLiteSchedulerConfigApplierLegacySnapshotAuthorityFailsClosedOnLocalGroupLookupError(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	cache := repository.NewSchedulerCache(client)
	lookupErr := errors.New("database unavailable")
	repo := &gatewayLiteConfigApplierGroupRepository{err: lookupErr}
	applier := newGatewayLiteSchedulerConfigApplier(cache).
		WithGroupRepository(repo).
		WithLegacySnapshotSchedulerAuthority(true)

	err := applier.ApplyGatewayConfigSnapshot(ctx, gatewaylite.GatewayConfigSnapshot{
		Groups: []gatewaylite.GatewayGroupSnapshot{{
			ID:             9,
			Name:           "must-not-be-written",
			Platform:       service.PlatformOpenAI,
			Status:         service.StatusActive,
			RateMultiplier: 1,
		}},
		Accounts: []gatewaylite.GatewayAccountSnapshot{{
			ID:          9001,
			Name:        "must-not-be-written",
			Platform:    service.PlatformOpenAI,
			Type:        "api_key",
			Status:      service.StatusActive,
			Schedulable: true,
			GroupIDs:    []int64{9},
		}},
	})
	require.ErrorIs(t, err, lookupErr)
	require.Equal(t, 1, repo.calls)

	groupCache, ok := cache.(service.SchedulerGroupCache)
	require.True(t, ok)
	group, err := groupCache.GetGroup(ctx, 9)
	require.NoError(t, err)
	require.Nil(t, group)
	account, err := cache.GetAccount(ctx, 9001)
	require.NoError(t, err)
	require.Nil(t, account)
}

func TestGatewayLiteSchedulerConfigApplierUpdatesRuntimeHealthInterval(t *testing.T) {
	settings := gatewaylite.NewRuntimeHealthSettings(15 * time.Second)
	applier := newGatewayLiteSchedulerConfigApplier(nil).
		WithRuntimeHealthSettings("openai-sg-t1", "sg", settings)

	applier.applyRuntimeHealthSettings(gatewaylite.GatewayConfigSnapshot{
		GatewayNodes: []gatewaylite.GatewayNodeSnapshot{
			{Code: "openai-hk-t1", Region: "hk", Metadata: map[string]any{"health_probe_interval_seconds": 180}},
			{Code: "openai-sg-t1", Region: "sg", Metadata: map[string]any{"health_probe_interval_seconds": 60}},
		},
	})

	require.Equal(t, 60*time.Second, settings.RuntimeHealthInterval())
}
