package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

type keywordLocalOnlyGroupRepoStub struct {
	GroupRepository
	groups          []Group
	listActiveCalls int
}

func (s *keywordLocalOnlyGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	s.listActiveCalls++
	return s.groups, nil
}

func TestKeywordRouteCandidatesFiltersPermissionsCapabilitiesAndSorts(t *testing.T) {
	original := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	key := &APIKey{
		User:                            &User{AllowedGroups: []int64{40}},
		KeywordRouteGroupAccessEnforced: true,
		AllowedKeywordRouteGroupIDs:     []int64{20, 30, 40, 50, 60, 70},
	}
	groups := []Group{
		{ID: 10, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true}, // 套餐不允许
		{ID: 20, Platform: PlatformOpenAI, Status: StatusDisabled, AllowBlockedKeywordRequests: true},
		{ID: 30, Platform: PlatformOpenAI, Status: StatusActive}, // 未开启关键词承接
		{ID: 40, Platform: PlatformOpenAI, Status: StatusActive, IsExclusive: true, AllowBlockedKeywordRequests: true, SortOrder: 3},
		{ID: 50, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, SortOrder: 1,
			ModelsListConfig: domain.GroupModelsListConfig{Enabled: true, Models: []string{"gpt-other"}}},
		{ID: 60, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, SortOrder: 2},
		{ID: 70, Platform: PlatformGrok, Status: StatusActive, AllowBlockedKeywordRequests: true, SortOrder: 0},
	}

	got := keywordRouteCandidates(key, original, groups, "gpt-test", ContentModerationProtocolOpenAIResponses, nil)
	require.Equal(t, []int64{60, 40, 70}, keywordRouteGroupIDs(got))
}

func TestKeywordRouteCandidatesRequiresImagePermission(t *testing.T) {
	original := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	key := &APIKey{User: &User{}}
	groups := []Group{
		{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true},
		{ID: 3, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true, AllowImageGeneration: true},
	}

	got := keywordRouteCandidates(key, original, groups, "gpt-image-2", ContentModerationProtocolOpenAIImages, []byte(`{"prompt":"x"}`))
	require.Equal(t, []int64{3}, keywordRouteGroupIDs(got))
}

func TestKeywordRouteCandidatesNeverReusesOriginalOrBypassesEnforcedEmptyAccess(t *testing.T) {
	original := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true}
	key := &APIKey{User: &User{}, KeywordRouteGroupAccessEnforced: true}
	groups := []Group{
		*original,
		{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true},
	}
	require.Empty(t, keywordRouteCandidates(key, original, groups, "gpt-test", ContentModerationProtocolOpenAIChat, nil))
}

func TestResolveBlockedKeywordRequestGroup_RemoteKeyCannotRouteToLocalOnlyGroup(t *testing.T) {
	localOnly := Group{
		ID:                          2,
		Platform:                    PlatformOpenAI,
		Status:                      StatusActive,
		LocalOnly:                   true,
		AllowBlockedKeywordRequests: true,
	}
	svc := &GatewayService{groupRepo: &keywordLocalOnlyGroupRepoStub{groups: []Group{localOnly}}}
	apiKey := &APIKey{
		User:  &User{},
		Group: &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive},
	}

	result, err := svc.ResolveBlockedKeywordRequestGroup(
		context.Background(),
		apiKey,
		"gpt-test",
		ContentModerationProtocolOpenAIResponses,
		nil,
		true,
	)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrKeywordRouteNoAccount)
	require.False(t, GroupAccessibleFromRequest(context.Background(), &localOnly))
	require.True(t, GroupAccessibleFromRequest(ContextWithGatewayLiteLocalAPIKey(context.Background()), &localOnly))
}

func TestResolveBlockedKeywordRequestGroup_LocalKeyNeverCrossesGroup(t *testing.T) {
	repo := &keywordLocalOnlyGroupRepoStub{groups: []Group{{
		ID: 2, Platform: PlatformOpenAI, Status: StatusActive, AllowBlockedKeywordRequests: true,
	}}}
	svc := &GatewayService{groupRepo: repo}
	apiKey := &APIKey{
		Key:   GatewayLiteLocalAPIKeyPrefix + "bound-group-only",
		User:  &User{},
		Group: &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive},
	}

	result, err := svc.ResolveBlockedKeywordRequestGroup(
		ContextWithGatewayLiteLocalAPIKey(context.Background()), apiKey, "gpt-test",
		ContentModerationProtocolOpenAIResponses, nil, true,
	)

	require.Nil(t, result)
	require.ErrorIs(t, err, ErrKeywordRouteNoAccount)
	require.Zero(t, repo.listActiveCalls, "local keys must reject before enumerating cross-group candidates")
}

func TestKeywordRouteCandidatesCapsLargeAllowedGroupSetsDeterministically(t *testing.T) {
	original := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	key := &APIKey{User: &User{}}
	groups := make([]Group, 0, 100)
	for index := 100; index > 0; index-- {
		groups = append(groups, Group{
			ID: int64(index + 1), Platform: PlatformOpenAI, Status: StatusActive,
			AllowBlockedKeywordRequests: true, SortOrder: index % 7,
		})
	}

	got := keywordRouteCandidates(key, original, groups, "gpt-test", ContentModerationProtocolOpenAIResponses, nil)
	require.Len(t, got, maxKeywordRouteCandidates)
	for index := 1; index < len(got); index++ {
		previous, current := got[index-1], got[index]
		require.True(t, previous.SortOrder < current.SortOrder || (previous.SortOrder == current.SortOrder && previous.ID < current.ID))
	}
}

func keywordRouteGroupIDs(groups []Group) []int64 {
	ids := make([]int64, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func BenchmarkKeywordRouteCandidates100Groups(b *testing.B) {
	original := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	key := &APIKey{User: &User{}}
	groups := make([]Group, 0, 100)
	for index := 0; index < 100; index++ {
		groups = append(groups, Group{
			ID: int64(index + 2), Platform: PlatformOpenAI, Status: StatusActive,
			AllowBlockedKeywordRequests: true, SortOrder: (index * 13) % 17,
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = keywordRouteCandidates(key, original, groups, "gpt-test", ContentModerationProtocolOpenAIResponses, nil)
	}
}
