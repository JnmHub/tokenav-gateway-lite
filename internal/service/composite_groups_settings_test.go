package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type compositeGroupsFailRepo struct {
	*compositeRuntimeSettingRepo
	setErr error
}

func (r *compositeGroupsFailRepo) Set(ctx context.Context, key, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	return r.compositeRuntimeSettingRepo.Set(ctx, key, value)
}

func TestCompositeGroupsSettingsUsesConfigUntilPersisted(t *testing.T) {
	repo := &compositeRuntimeSettingRepo{}
	svc := NewSettingService(repo, &config.Config{GatewayLite: config.GatewayLiteConfig{CompositeGroupsEnabled: true}})

	settings, err := svc.GetCompositeGroupsSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.True(t, svc.CompositeGroupsEnabled())

	require.NoError(t, svc.SetCompositeGroupsSettings(context.Background(), &CompositeGroupsSettings{Enabled: false}))
	require.Equal(t, "false", repo.value)
	require.False(t, svc.CompositeGroupsEnabled())

	restarted := NewSettingService(repo, &config.Config{GatewayLite: config.GatewayLiteConfig{CompositeGroupsEnabled: true}})
	require.NoError(t, restarted.WarmCompositeGroupsSettings(context.Background()))
	require.False(t, restarted.CompositeGroupsEnabled())
}

func TestCompositeGroupsSettingsFailedWriteKeepsLastRuntimeValue(t *testing.T) {
	repo := &compositeGroupsFailRepo{compositeRuntimeSettingRepo: &compositeRuntimeSettingRepo{}, setErr: errors.New("write failed")}
	svc := NewSettingService(repo, &config.Config{GatewayLite: config.GatewayLiteConfig{CompositeGroupsEnabled: true}})

	require.Error(t, svc.SetCompositeGroupsSettings(context.Background(), &CompositeGroupsSettings{Enabled: false}))
	require.True(t, svc.CompositeGroupsEnabled())
}

func TestCompositeGroupsSettingsRejectsCorruptPersistedValue(t *testing.T) {
	repo := &compositeRuntimeSettingRepo{value: "not-a-bool", found: true}
	svc := NewSettingService(repo, &config.Config{GatewayLite: config.GatewayLiteConfig{CompositeGroupsEnabled: true}})

	require.Error(t, svc.WarmCompositeGroupsSettings(context.Background()))
	require.True(t, svc.CompositeGroupsEnabled())
}
