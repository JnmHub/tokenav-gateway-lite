package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type panelRateLimitSettingRepoStub struct {
	values map[string]string
}

func (s *panelRateLimitSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (s *panelRateLimitSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}

func (s *panelRateLimitSettingRepoStub) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *panelRateLimitSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (s *panelRateLimitSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (s *panelRateLimitSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}

func (s *panelRateLimitSettingRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func TestPanelRateLimitPresetsAndCustomValidation(t *testing.T) {
	strict, err := PanelRateLimitPresetSettings(PanelRateLimitPresetStrict)
	require.NoError(t, err)
	require.Equal(t, 120, strict.UserRPM)
	require.Equal(t, 30, strict.HeavyRPM)

	resolved, err := NormalizePanelRateLimitSettings(PanelRateLimitSettings{
		Enabled: false, Preset: PanelRateLimitPresetRelaxed,
		UserRPM: 1, HeavyRPM: 1, WindowSeconds: 10,
	})
	require.NoError(t, err)
	require.False(t, resolved.Enabled)
	require.Equal(t, 600, resolved.UserRPM, "preset values must not be overridden by hidden numeric inputs")

	_, err = NormalizePanelRateLimitSettings(PanelRateLimitSettings{
		Enabled: true, Preset: PanelRateLimitPresetCustom,
		UserRPM: 50, HeavyRPM: 51, WindowSeconds: 60,
	})
	require.ErrorContains(t, err, "cannot exceed")
}

func TestPanelRateLimitSettingsPersistPresetAndResetDefault(t *testing.T) {
	repo := &panelRateLimitSettingRepoStub{values: map[string]string{}}
	svc := &SettingService{settingRepo: repo}

	strict, err := svc.ApplyPanelRateLimitPreset(context.Background(), PanelRateLimitPresetStrict, true)
	require.NoError(t, err)
	require.Equal(t, PanelRateLimitPresetStrict, strict.Preset)
	require.Equal(t, strict, svc.GetPanelRateLimitSettingsCached(context.Background()))
	require.Contains(t, repo.values[SettingKeyPanelRateLimitSettings], `"preset":"strict"`)

	reset, err := svc.ResetPanelRateLimitSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultPanelRateLimitSettings(), reset)
	require.NotContains(t, repo.values, SettingKeyPanelRateLimitSettings)
}
