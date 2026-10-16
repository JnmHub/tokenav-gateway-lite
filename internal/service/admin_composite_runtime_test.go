package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type compositeRuntimeSettingRepo struct {
	value string
	found bool
}

func (r *compositeRuntimeSettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (r *compositeRuntimeSettingRepo) GetValue(context.Context, string) (string, error) {
	if !r.found {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}
func (r *compositeRuntimeSettingRepo) Set(_ context.Context, _ string, value string) error {
	r.value, r.found = value, true
	return nil
}
func (r *compositeRuntimeSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *compositeRuntimeSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *compositeRuntimeSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *compositeRuntimeSettingRepo) Delete(context.Context, string) error { return nil }

func TestAdminServiceReadsCompositeRuntimeSetting(t *testing.T) {
	repo := &compositeRuntimeSettingRepo{}
	settings := NewSettingService(repo, &config.Config{})
	admin := &adminServiceImpl{
		settingService:         settings,
		compositeGroupsRuntime: true,
	}

	require.False(t, admin.isCompositeGroupsEnabled())
	require.NoError(t, settings.SetCompositeGroupsSettings(context.Background(), &CompositeGroupsSettings{Enabled: true}))
	require.True(t, admin.isCompositeGroupsEnabled())
	require.NoError(t, settings.SetCompositeGroupsSettings(context.Background(), &CompositeGroupsSettings{Enabled: false}))
	require.False(t, admin.isCompositeGroupsEnabled())
}
