package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/codexadmission"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexAdmissionSettingRepo struct{ values map[string]string }

func (r *codexAdmissionSettingRepo) Get(context.Context, string) (*Setting, error) { panic("unused") }
func (r *codexAdmissionSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

type codexAdmissionFlakyRepo struct {
	*codexAdmissionSettingRepo
	failReads atomic.Int32
}

func (r *codexAdmissionFlakyRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.failReads.Add(-1) >= 0 {
		return "", errors.New("temporary database failure")
	}
	return r.codexAdmissionSettingRepo.GetValue(ctx, key)
}
func (r *codexAdmissionSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *codexAdmissionSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unused")
}
func (r *codexAdmissionSettingRepo) SetMultiple(context.Context, map[string]string) error {
	panic("unused")
}
func (r *codexAdmissionSettingRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unused")
}
func (r *codexAdmissionSettingRepo) Delete(context.Context, string) error { panic("unused") }

func TestCodexClientAdmissionSettingsPersistAndWarmMemorySnapshot(t *testing.T) {
	repo := &codexAdmissionSettingRepo{values: map[string]string{}}
	service := NewSettingService(repo, &config.Config{})

	initial, err := service.GetCodexClientAdmissionSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, codexadmission.ModeAuditOnly, initial.Settings.Mode)

	settings := codexadmission.DefaultSettings()
	settings.Mode = codexadmission.ModeEnforce
	settings.MinVersion = "0.144.1"
	settings.DeniedClients = []string{codexadmission.ClientCLI}
	updated, err := service.SetCodexClientAdmissionSettings(context.Background(), settings)
	require.NoError(t, err)
	require.Equal(t, codexadmission.ModeEnforce, updated.Settings.Mode)
	require.Equal(t, codexadmission.ModeEnforce, service.CodexClientAdmissionRuntime().Current().Mode)
	require.NotEmpty(t, repo.values[SettingKeyCodexClientAdmissionSettings])

	restarted := NewSettingService(repo, &config.Config{})
	require.Equal(t, codexadmission.ModeAuditOnly, restarted.CodexClientAdmissionRuntime().Current().Mode)
	require.NoError(t, restarted.WarmCodexClientAdmissionSettings(context.Background()))
	require.Equal(t, codexadmission.ModeEnforce, restarted.CodexClientAdmissionRuntime().Current().Mode)
}

func TestWarmCodexClientAdmissionSettingsUntilReadyKeepsAuditDefaultThenRecovers(t *testing.T) {
	base := &codexAdmissionSettingRepo{values: map[string]string{
		SettingKeyCodexClientAdmissionSettings: `{"mode":"enforce","allowed_clients":["desktop"],"denied_clients":["cli"],"app_server_enabled":false}`,
	}}
	repo := &codexAdmissionFlakyRepo{codexAdmissionSettingRepo: base}
	repo.failReads.Store(1)
	svc := NewSettingService(repo, &config.Config{})
	require.Error(t, svc.WarmCodexClientAdmissionSettings(context.Background()))
	require.Equal(t, codexadmission.ModeAuditOnly, svc.CodexClientAdmissionRuntime().Current().Mode)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	svc.WarmCodexClientAdmissionSettingsUntilReady(ctx, time.Millisecond)
	require.Equal(t, codexadmission.ModeEnforce, svc.CodexClientAdmissionRuntime().Current().Mode)
}
