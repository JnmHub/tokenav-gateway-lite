package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	PanelRateLimitPresetDefault = "default"
	PanelRateLimitPresetRelaxed = "relaxed"
	PanelRateLimitPresetStrict  = "strict"
	PanelRateLimitPresetCustom  = "custom"
)

const (
	panelRateLimitMaxRPM    = 100000
	panelRateLimitMinWindow = 10
	panelRateLimitMaxWindow = 3600
	panelRateLimitCacheTTL  = 60 * time.Second
	panelRateLimitErrorTTL  = 5 * time.Second
	panelRateLimitDBTimeout = 3 * time.Second
)

// PanelRateLimitSettings 是 gateway-lite 后台 API 的独立运行限流配置。
// preset 为常用档位，custom 才使用自定义数字，前端可以直接做成按钮而非复杂表达式。
type PanelRateLimitSettings struct {
	Enabled       bool   `json:"enabled"`
	Preset        string `json:"preset"`
	UserRPM       int    `json:"user_rpm"`
	HeavyRPM      int    `json:"heavy_rpm"`
	WindowSeconds int    `json:"window_seconds"`
	ExemptAdmin   bool `json:"exempt_admin"`   // [v0.2.5 新增字段]
	PublicIPRPM   int  `json:"public_ip_rpm"`   // [v0.2.5 新增字段]
}

type cachedPanelRateLimitSettings struct {
	settings  PanelRateLimitSettings
	expiresAt int64
}

func DefaultPanelRateLimitSettings() PanelRateLimitSettings {
	return PanelRateLimitSettings{
		Enabled:       true,
		Preset:        PanelRateLimitPresetDefault,
		UserRPM:       240,
		HeavyRPM:      60,
		WindowSeconds: 60,
	}
}

func PanelRateLimitPresetSettings(preset string) (PanelRateLimitSettings, error) {
	preset = strings.ToLower(strings.TrimSpace(preset))
	switch preset {
	case "", PanelRateLimitPresetDefault:
		return DefaultPanelRateLimitSettings(), nil
	case PanelRateLimitPresetRelaxed:
		return PanelRateLimitSettings{
			Enabled: true, Preset: PanelRateLimitPresetRelaxed,
			UserRPM: 600, HeavyRPM: 180, WindowSeconds: 60,
		}, nil
	case PanelRateLimitPresetStrict:
		return PanelRateLimitSettings{
			Enabled: true, Preset: PanelRateLimitPresetStrict,
			UserRPM: 120, HeavyRPM: 30, WindowSeconds: 60,
		}, nil
	case PanelRateLimitPresetCustom:
		return PanelRateLimitSettings{Enabled: true, Preset: PanelRateLimitPresetCustom}, nil
	default:
		return PanelRateLimitSettings{}, fmt.Errorf("unknown panel rate limit preset %q", preset)
	}
}

// NormalizePanelRateLimitSettings 返回实际生效配置。
// 非 custom 档位忽略客户端携带的数字，避免界面档位与实际限额不一致。
func NormalizePanelRateLimitSettings(input PanelRateLimitSettings) (PanelRateLimitSettings, error) {
	preset := strings.ToLower(strings.TrimSpace(input.Preset))
	if preset == "" {
		preset = PanelRateLimitPresetCustom
	}
	if preset != PanelRateLimitPresetCustom {
		resolved, err := PanelRateLimitPresetSettings(preset)
		if err != nil {
			return PanelRateLimitSettings{}, err
		}
		resolved.Enabled = input.Enabled
		return resolved, nil
	}
	if input.UserRPM < 1 || input.UserRPM > panelRateLimitMaxRPM {
		return PanelRateLimitSettings{}, fmt.Errorf("user_rpm must be between 1 and %d", panelRateLimitMaxRPM)
	}
	if input.HeavyRPM < 1 || input.HeavyRPM > panelRateLimitMaxRPM {
		return PanelRateLimitSettings{}, fmt.Errorf("heavy_rpm must be between 1 and %d", panelRateLimitMaxRPM)
	}
	if input.HeavyRPM > input.UserRPM {
		return PanelRateLimitSettings{}, fmt.Errorf("heavy_rpm cannot exceed user_rpm")
	}
	if input.WindowSeconds < panelRateLimitMinWindow || input.WindowSeconds > panelRateLimitMaxWindow {
		return PanelRateLimitSettings{}, fmt.Errorf("window_seconds must be between %d and %d", panelRateLimitMinWindow, panelRateLimitMaxWindow)
	}
	input.Preset = PanelRateLimitPresetCustom
	return input, nil
}

func (s *SettingService) GetPanelRateLimitSettings(ctx context.Context) (PanelRateLimitSettings, error) {
	defaults := DefaultPanelRateLimitSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeyPanelRateLimitSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, nil
		}
		return PanelRateLimitSettings{}, fmt.Errorf("get panel rate limit settings: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return defaults, nil
	}
	var settings PanelRateLimitSettings
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		slog.Warn("invalid panel rate limit settings, using defaults", "error", err)
		return defaults, nil
	}
	normalized, err := NormalizePanelRateLimitSettings(settings)
	if err != nil {
		slog.Warn("unsafe panel rate limit settings, using defaults", "error", err)
		return defaults, nil
	}
	return normalized, nil
}

func (s *SettingService) SetPanelRateLimitSettings(ctx context.Context, input PanelRateLimitSettings) (PanelRateLimitSettings, error) {
	if s == nil || s.settingRepo == nil {
		return PanelRateLimitSettings{}, fmt.Errorf("setting repository is unavailable")
	}
	settings, err := NormalizePanelRateLimitSettings(input)
	if err != nil {
		return PanelRateLimitSettings{}, err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return PanelRateLimitSettings{}, fmt.Errorf("marshal panel rate limit settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyPanelRateLimitSettings, string(data)); err != nil {
		return PanelRateLimitSettings{}, err
	}
	s.storePanelRateLimitSettings(settings, panelRateLimitCacheTTL)
	return settings, nil
}

func (s *SettingService) ApplyPanelRateLimitPreset(ctx context.Context, preset string, enabled bool) (PanelRateLimitSettings, error) {
	settings, err := PanelRateLimitPresetSettings(preset)
	if err != nil {
		return PanelRateLimitSettings{}, err
	}
	if settings.Preset == PanelRateLimitPresetCustom {
		return PanelRateLimitSettings{}, fmt.Errorf("custom preset requires explicit limits")
	}
	settings.Enabled = enabled
	return s.SetPanelRateLimitSettings(ctx, settings)
}

// ResetPanelRateLimitSettings 删除覆盖值即恢复默认，不留下一份容易过时的“默认副本”。
func (s *SettingService) ResetPanelRateLimitSettings(ctx context.Context) (PanelRateLimitSettings, error) {
	settings := DefaultPanelRateLimitSettings()
	if s == nil || s.settingRepo == nil {
		return PanelRateLimitSettings{}, fmt.Errorf("setting repository is unavailable")
	}
	if err := s.settingRepo.Delete(ctx, SettingKeyPanelRateLimitSettings); err != nil && !errors.Is(err, ErrSettingNotFound) {
		return PanelRateLimitSettings{}, err
	}
	s.storePanelRateLimitSettings(settings, panelRateLimitCacheTTL)
	return settings, nil
}

// GetPanelRateLimitSettingsCached 使用 stale-on-error（失败保留旧值）策略，
// 数据库短暂异常时不会把后台突然放开或锁死。
func (s *SettingService) GetPanelRateLimitSettingsCached(ctx context.Context) PanelRateLimitSettings {
	defaults := DefaultPanelRateLimitSettings()
	if s == nil || s.settingRepo == nil {
		return defaults
	}
	if cached, ok := s.panelRateLimitSettingsCache.Load().(*cachedPanelRateLimitSettings); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.settings
	}
	value, _, _ := s.panelRateLimitSettingsSF.Do(SettingKeyPanelRateLimitSettings, func() (any, error) {
		if cached, ok := s.panelRateLimitSettingsCache.Load().(*cachedPanelRateLimitSettings); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
			return cached.settings, nil
		}
		if ctx == nil {
			ctx = context.Background()
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), panelRateLimitDBTimeout)
		defer cancel()
		settings, err := s.GetPanelRateLimitSettings(dbCtx)
		if err != nil {
			fallback := defaults
			if cached, ok := s.panelRateLimitSettingsCache.Load().(*cachedPanelRateLimitSettings); ok && cached != nil {
				fallback = cached.settings
			}
			s.storePanelRateLimitSettings(fallback, panelRateLimitErrorTTL)
			return fallback, nil
		}
		s.storePanelRateLimitSettings(settings, panelRateLimitCacheTTL)
		return settings, nil
	})
	if settings, ok := value.(PanelRateLimitSettings); ok {
		return settings
	}
	return defaults
}

func (s *SettingService) storePanelRateLimitSettings(settings PanelRateLimitSettings, ttl time.Duration) {
	if s == nil {
		return
	}
	s.panelRateLimitSettingsCache.Store(&cachedPanelRateLimitSettings{
		settings: settings, expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
