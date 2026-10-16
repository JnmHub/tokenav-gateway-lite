package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CompositeGroupsSettings 是聚合分组的节点级总开关。
type CompositeGroupsSettings struct {
	Enabled bool `json:"enabled"`
}

// GetCompositeGroupsSettings 读取持久化值；尚未保存时沿用启动配置，
// 让升级前已经通过 YAML 或环境变量启用的节点保持原行为。
func (s *SettingService) GetCompositeGroupsSettings(ctx context.Context) (*CompositeGroupsSettings, error) {
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	enabled := s.compositeGroupsConfigDefault
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCompositeGroupsEnabled)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, fmt.Errorf("get composite groups settings: %w", err)
	}
	if err == nil {
		parsed, parseErr := strconv.ParseBool(strings.TrimSpace(raw))
		if parseErr != nil {
			return nil, fmt.Errorf("parse composite groups settings: %w", parseErr)
		}
		enabled = parsed
	}

	// 管理页面 GET 也是一次运行状态校准，避免启动阶段数据库短暂不可用后
	// 页面与请求门禁长期不一致。
	s.compositeGroupsEnabled.Store(enabled)
	return &CompositeGroupsSettings{Enabled: enabled}, nil
}

// SetCompositeGroupsSettings 先持久化，再发布运行时快照；写库失败时当前
// 进程继续使用最后一次确认值，不能出现“按钮已开、实际未开”的半状态。
func (s *SettingService) SetCompositeGroupsSettings(ctx context.Context, settings *CompositeGroupsSettings) error {
	if s == nil || s.settingRepo == nil {
		return errors.New("setting repository is unavailable")
	}
	if settings == nil {
		return errors.New("composite groups settings are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.settingRepo.Set(ctx, SettingKeyCompositeGroupsEnabled, strconv.FormatBool(settings.Enabled)); err != nil {
		return fmt.Errorf("set composite groups settings: %w", err)
	}
	s.compositeGroupsEnabled.Store(settings.Enabled)
	return nil
}

// WarmCompositeGroupsSettings 在启动阶段恢复持久化开关。
func (s *SettingService) WarmCompositeGroupsSettings(ctx context.Context) error {
	_, err := s.GetCompositeGroupsSettings(ctx)
	return err
}

// WarmCompositeGroupsSettingsUntilReady 只在启动读取失败时后台重试；成功后
// 管理员 PUT 与 GET 校准负责维护快照。
func (s *SettingService) WarmCompositeGroupsSettingsUntilReady(ctx context.Context, interval time.Duration) {
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
			err := s.WarmCompositeGroupsSettings(readCtx)
			cancel()
			if err == nil {
				return
			}
			timer.Reset(interval)
		}
	}
}

// CompositeGroupsEnabled 仅做 atomic（原子）读取，可安全用于请求热路径。
func (s *SettingService) CompositeGroupsEnabled() bool {
	return s != nil && s.compositeGroupsEnabled.Load()
}
