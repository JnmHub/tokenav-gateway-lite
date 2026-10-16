package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type grokVideoTransformsFailRepo struct {
	*compositeRuntimeSettingRepo
	setErr error
}

func (r *grokVideoTransformsFailRepo) Set(ctx context.Context, key, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	return r.compositeRuntimeSettingRepo.Set(ctx, key, value)
}

func TestGrokVideoTransformsSettingsUsesConfigUntilPersisted(t *testing.T) {
	repo := &compositeRuntimeSettingRepo{}
	cfg := &config.Config{}
	cfg.Gateway.GrokVideoTransformsEnabled = true
	svc := NewSettingService(repo, cfg)

	settings, err := svc.GetGrokVideoTransformsSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.True(t, svc.GrokVideoTransformsEnabled())

	// 保存 false 必须同时落库并立刻改变请求热路径可见的值（不需要重启）。
	require.NoError(t, svc.SetGrokVideoTransformsSettings(context.Background(), &GrokVideoTransformsSettings{Enabled: false}))
	require.Equal(t, "false", repo.value)
	require.False(t, svc.GrokVideoTransformsEnabled())

	// 重启后（新实例 + Warm）以数据库为准，而不是又回到启动配置。
	restarted := NewSettingService(repo, cfg)
	require.NoError(t, restarted.WarmGrokVideoTransformsSettings(context.Background()))
	require.False(t, restarted.GrokVideoTransformsEnabled())
}

func TestGrokVideoTransformsSettingsDefaultsToDisabled(t *testing.T) {
	svc := NewSettingService(&compositeRuntimeSettingRepo{}, &config.Config{})
	require.False(t, svc.GrokVideoTransformsEnabled())

	settings, err := svc.GetGrokVideoTransformsSettings(context.Background())
	require.NoError(t, err)
	require.False(t, settings.Enabled)

	// nil 接收者与 nil 依赖都必须安全：路由门禁在测试与降级路径上会调用它。
	var nilService *SettingService
	require.False(t, nilService.GrokVideoTransformsEnabled())
}

func TestGrokVideoTransformsSettingsFailedWriteKeepsLastRuntimeValue(t *testing.T) {
	repo := &grokVideoTransformsFailRepo{compositeRuntimeSettingRepo: &compositeRuntimeSettingRepo{}, setErr: errors.New("write failed")}
	cfg := &config.Config{}
	cfg.Gateway.GrokVideoTransformsEnabled = true
	svc := NewSettingService(repo, cfg)

	require.Error(t, svc.SetGrokVideoTransformsSettings(context.Background(), &GrokVideoTransformsSettings{Enabled: false}))
	// 写库失败时不能让页面显示“已开”而热路径仍是旧值：保持最后一次确认值。
	require.True(t, svc.GrokVideoTransformsEnabled())
}

func TestGrokVideoTransformsSettingsRejectsCorruptPersistedValue(t *testing.T) {
	repo := &compositeRuntimeSettingRepo{value: "not-a-bool", found: true}
	cfg := &config.Config{}
	cfg.Gateway.GrokVideoTransformsEnabled = true
	svc := NewSettingService(repo, cfg)

	require.Error(t, svc.WarmGrokVideoTransformsSettings(context.Background()))
	require.True(t, svc.GrokVideoTransformsEnabled())
}

type grokVideoTransformsFlakyRepo struct {
	*compositeRuntimeSettingRepo
	failures int
}

func (r *grokVideoTransformsFlakyRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.failures > 0 {
		r.failures--
		return "", errors.New("database temporarily unavailable")
	}
	return r.compositeRuntimeSettingRepo.GetValue(ctx, key)
}

func TestGrokVideoTransformsWarmUntilReadyRecoversFromTransientFailure(t *testing.T) {
	repo := &grokVideoTransformsFlakyRepo{compositeRuntimeSettingRepo: &compositeRuntimeSettingRepo{value: "true", found: true}, failures: 1}
	cfg := &config.Config{}
	svc := NewSettingService(repo, cfg)
	require.False(t, svc.GrokVideoTransformsEnabled())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	svc.WarmGrokVideoTransformsSettingsUntilReady(ctx, 10*time.Millisecond)
	require.True(t, svc.GrokVideoTransformsEnabled())
}
