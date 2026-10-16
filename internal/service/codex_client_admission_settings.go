package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/codexadmission"
)

type CodexClientAdmissionView struct {
	Settings       codexadmission.Settings `json:"settings"`
	Stats          codexadmission.Snapshot `json:"stats"`
	KnownClients   []string                `json:"known_clients"`
	VersionPresets []string                `json:"version_presets"`
}

func (s *SettingService) CodexClientAdmissionRuntime() *codexadmission.Runtime {
	if s == nil {
		return nil
	}
	if s.codexClientAdmission == nil {
		s.codexClientAdmission = codexadmission.NewRuntime(codexadmission.DefaultSettings())
	}
	return s.codexClientAdmission
}

func (s *SettingService) WarmCodexClientAdmissionSettings(ctx context.Context) error {
	_, err := s.loadCodexClientAdmissionSettings(ctx)
	return err
}

func (s *SettingService) WarmCodexClientAdmissionSettingsUntilReady(ctx context.Context, interval time.Duration) {
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
			err := s.WarmCodexClientAdmissionSettings(readCtx)
			cancel()
			if err == nil {
				return
			}
			timer.Reset(interval)
		}
	}
}

func (s *SettingService) GetCodexClientAdmissionSettings(ctx context.Context) (*CodexClientAdmissionView, error) {
	settings, err := s.loadCodexClientAdmissionSettings(ctx)
	if err != nil {
		return nil, err
	}
	return s.codexClientAdmissionView(settings), nil
}

func (s *SettingService) SetCodexClientAdmissionSettings(ctx context.Context, settings codexadmission.Settings) (*CodexClientAdmissionView, error) {
	normalized, err := codexadmission.NormalizeSettings(settings)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode Codex client admission settings: %w", err)
	}
	if s == nil || s.settingRepo == nil {
		return nil, errors.New("setting repository is unavailable")
	}
	if err := s.settingRepo.Set(ctx, SettingKeyCodexClientAdmissionSettings, string(payload)); err != nil {
		return nil, fmt.Errorf("save Codex client admission settings: %w", err)
	}
	if err := s.CodexClientAdmissionRuntime().Update(normalized); err != nil {
		return nil, err
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return s.codexClientAdmissionView(normalized), nil
}

func (s *SettingService) loadCodexClientAdmissionSettings(ctx context.Context) (codexadmission.Settings, error) {
	defaults := codexadmission.DefaultSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, errors.New("setting repository is unavailable")
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCodexClientAdmissionSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			_ = s.CodexClientAdmissionRuntime().Update(defaults)
			return defaults, nil
		}
		return defaults, fmt.Errorf("load Codex client admission settings: %w", err)
	}
	settings := defaults
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return defaults, fmt.Errorf("decode Codex client admission settings: %w", err)
	}
	normalized, err := codexadmission.NormalizeSettings(settings)
	if err != nil {
		return defaults, fmt.Errorf("normalize Codex client admission settings: %w", err)
	}
	if err := s.CodexClientAdmissionRuntime().Update(normalized); err != nil {
		return defaults, err
	}
	return normalized, nil
}

func (s *SettingService) codexClientAdmissionView(settings codexadmission.Settings) *CodexClientAdmissionView {
	runtime := s.CodexClientAdmissionRuntime()
	stats := codexadmission.Snapshot{}
	if runtime != nil {
		stats = runtime.Snapshot()
	}
	presets := []string{"", "0.144.1", "0.150.0"}
	seen := map[string]struct{}{"": {}, "0.144.1": {}, "0.150.0": {}}
	for _, item := range stats.Versions {
		if _, exists := seen[item.Key]; exists {
			continue
		}
		seen[item.Key] = struct{}{}
		presets = append(presets, item.Key)
	}
	return &CodexClientAdmissionView{
		Settings:       settings,
		Stats:          stats,
		KnownClients:   codexadmission.KnownClients(),
		VersionPresets: presets,
	}
}
