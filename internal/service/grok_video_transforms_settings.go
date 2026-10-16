package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GrokVideoTransformsSettings 是 Grok 视频编辑（edits）/扩展（extensions）的节点级开关。
//
// 为什么做成数据库设置而不是只看 config.yaml：这两条路由原来只在启动时按
// `gateway.grok_video_transforms_enabled` 决定是否注册，改一次就要重启进程；而视频编辑
// 属于随时可能开关的灰度能力。现在路由常驻注册、handler 内按内存快照判定，保存即生效。
type GrokVideoTransformsSettings struct {
	Enabled bool `json:"enabled"`
}

// GetGrokVideoTransformsSettings 读取持久化值；尚未保存时沿用启动配置，
// 让升级前已经通过 YAML 或环境变量开启的节点保持原行为。
func (s *SettingService) GetGrokVideoTransformsSettings(ctx context.Context) (*GrokVideoTransformsSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	enabled := s.grokVideoTransformsConfigDefault
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGrokVideoTransformsEnabled)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, fmt.Errorf("get grok video transforms settings: %w", err)
	}
	if err == nil {
		parsed, parseErr := strconv.ParseBool(strings.TrimSpace(raw))
		if parseErr != nil {
			return nil, fmt.Errorf("parse grok video transforms settings: %w", parseErr)
		}
		enabled = parsed
	}

	// 管理页面 GET 也是一次运行状态校准：启动阶段数据库短暂不可用时，页面与
	// 请求门禁可能不一致，管理员刷新一次即可对齐。
	s.grokVideoTransformsEnabled.Store(enabled)
	return &GrokVideoTransformsSettings{Enabled: enabled}, nil
}

// SetGrokVideoTransformsSettings 先持久化，再发布运行时快照；写库失败时当前进程
// 继续使用最后一次确认值，不能出现“页面已开、实际仍 404”的半状态。
func (s *SettingService) SetGrokVideoTransformsSettings(ctx context.Context, settings *GrokVideoTransformsSettings) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("setting repository is unavailable")
	}
	if settings == nil {
		return errors.New("grok video transforms settings are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGrokVideoTransformsEnabled, strconv.FormatBool(settings.Enabled)); err != nil {
		return fmt.Errorf("set grok video transforms settings: %w", err)
	}
	s.grokVideoTransformsEnabled.Store(settings.Enabled)
	return nil
}

// WarmGrokVideoTransformsSettings 在启动阶段恢复持久化开关。
func (s *SettingService) WarmGrokVideoTransformsSettings(ctx context.Context) error {
	_, err := s.GetGrokVideoTransformsSettings(ctx)
	return err
}

// WarmGrokVideoTransformsSettingsUntilReady 只在启动读取失败时后台重试；
// 成功后管理员 PUT 与 GET 校准负责维护快照。
func (s *SettingService) WarmGrokVideoTransformsSettingsUntilReady(ctx context.Context, interval time.Duration) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := s.WarmGrokVideoTransformsSettings(readCtx)
			cancel()
			if err == nil {
				return
			}
			timer.Reset(interval)
		}
	}
}

// GrokVideoTransformsEnabled 仅做 atomic（原子）读取，可安全用于请求热路径与路由门禁。
func (s *SettingService) GrokVideoTransformsEnabled() bool {
	return s != nil && s.grokVideoTransformsEnabled.Load()
}
