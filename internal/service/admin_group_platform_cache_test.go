package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type groupPlatformRepoStub struct {
	GroupRepository
	group *Group
}

func (r *groupPlatformRepoStub) GetByID(_ context.Context, _ int64) (*Group, error) {
	cloned := *r.group
	return &cloned, nil
}

func (r *groupPlatformRepoStub) Update(_ context.Context, group *Group) error {
	r.group = group
	return nil
}

type channelCacheInvalidatorSpy struct{ calls int }

func (s *channelCacheInvalidatorSpy) InvalidateCache() { s.calls++ }

func TestUpdateGroupInvalidatesChannelCacheOnlyWhenPlatformChanges(t *testing.T) {
	tests := []struct {
		name          string
		fromPlatform  string
		inputPlatform string
		wantCalls     int
	}{
		{"platform changed", PlatformAnthropic, PlatformOpenAI, 1},
		{"platform unchanged", PlatformAnthropic, PlatformAnthropic, 0},
		{"platform omitted", PlatformAnthropic, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &groupPlatformRepoStub{group: &Group{ID: 7, Name: "g", Platform: tt.fromPlatform}}
			spy := &channelCacheInvalidatorSpy{}
			svc := &adminServiceImpl{groupRepo: repo, channelCacheInvalidator: spy, compositeGroupsEnabled: true}

			got, err := svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{Platform: tt.inputPlatform})
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, tt.wantCalls, spy.calls)
		})
	}
}
