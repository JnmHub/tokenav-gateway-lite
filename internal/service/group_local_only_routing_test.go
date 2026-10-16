package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type localOnlyRoutingGroupRepoStub struct {
	GroupRepository
	groups map[int64]*Group
}

func (s *localOnlyRoutingGroupRepoStub) GetByIDLite(_ context.Context, id int64) (*Group, error) {
	group, ok := s.groups[id]
	if !ok {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

func TestGatewayServiceResolveGroupByID_RejectsLocalOnlyGroupForRemoteKey(t *testing.T) {
	group := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive, LocalOnly: true}
	svc := &GatewayService{groupRepo: &localOnlyRoutingGroupRepoStub{groups: map[int64]*Group{group.ID: group}}}

	got, err := svc.resolveGroupByID(context.Background(), group.ID)

	require.Nil(t, got)
	require.ErrorIs(t, err, ErrGroupLocalOnly)
}

func TestGatewayServiceResolveGroupByID_AllowsLocalOnlyGroupForGatewayLocalKey(t *testing.T) {
	group := &Group{ID: 10, Platform: PlatformOpenAI, Status: StatusActive, LocalOnly: true}
	svc := &GatewayService{groupRepo: &localOnlyRoutingGroupRepoStub{groups: map[int64]*Group{group.ID: group}}}
	ctx := ContextWithGatewayLiteLocalAPIKey(context.Background())

	got, err := svc.resolveGroupByID(ctx, group.ID)

	require.NoError(t, err)
	require.Same(t, group, got)
}

func TestGatewayServiceResolveGatewayGroup_RemoteKeyCannotFallbackToLocalOnlyGroup(t *testing.T) {
	originalID := int64(10)
	fallbackID := int64(20)
	repo := &localOnlyRoutingGroupRepoStub{groups: map[int64]*Group{
		originalID: {
			ID:              originalID,
			Platform:        PlatformAnthropic,
			Status:          StatusActive,
			ClaudeCodeOnly:  true,
			FallbackGroupID: &fallbackID,
		},
		fallbackID: {
			ID:        fallbackID,
			Platform:  PlatformAnthropic,
			Status:    StatusActive,
			LocalOnly: true,
		},
	}}
	svc := &GatewayService{groupRepo: repo}

	group, resolvedID, err := svc.resolveGatewayGroup(context.Background(), &originalID)

	require.Nil(t, group)
	require.Nil(t, resolvedID)
	require.ErrorIs(t, err, ErrGroupLocalOnly)
}
