//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type grokRuntimeSettingRepo struct {
	settingUpdateRepoStub
	values map[string]string
}

func (repo *grokRuntimeSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = repo.values[key]
	}
	return result, nil
}

func TestWarmGrokModelMappingSettingsRestoresRuntime(t *testing.T) {
	previous := xai.RuntimeModelMappingOptions()
	t.Cleanup(func() { xai.SetRuntimeModelMappingOptions(previous) })

	repo := &grokRuntimeSettingRepo{values: map[string]string{
		SettingKeyGrokDefaultTextModel:           "grok-build-0.1",
		SettingKeyGrokCrossClientModelMapEnabled: "true",
	}}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.WarmGrokModelMappingSettings(context.Background()))
	require.Equal(t, "grok-build-0.1", xai.DefaultModelMapping()["claude-*"])
}
