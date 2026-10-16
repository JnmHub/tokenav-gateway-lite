package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	openAICodexVersionSyncInterval  = 6 * time.Hour
	openAICodexVersionSyncTimeout   = 30 * time.Second
	openAICodexVersionSyncRepo      = "openai/codex"
	openAICodexVersionSyncPerPage   = 30
	openAICodexVersionTagPrefix     = "rust-v"
)

// OpenAICodexVersionSyncService 每 6 小时同步官方 Codex 最新稳定版。
// 同步失败保留旧值，管理员固定版本始终优先，避免网络波动影响请求热路径。
type OpenAICodexVersionSyncService struct {
	settingRepo    SettingRepository
	settingService *SettingService
	githubClient   GitHubReleaseClient
	interval       time.Duration
	stopCh         chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	startOnce      sync.Once
	stopOnce       sync.Once
	wg             sync.WaitGroup
}

func NewOpenAICodexVersionSyncService(settingRepo SettingRepository, settingService *SettingService, githubClient GitHubReleaseClient, interval time.Duration) *OpenAICodexVersionSyncService {
	ctx, cancel := context.WithCancel(context.Background())
	return &OpenAICodexVersionSyncService{settingRepo: settingRepo, settingService: settingService, githubClient: githubClient, interval: interval, stopCh: make(chan struct{}), ctx: ctx, cancel: cancel}
}

func (s *OpenAICodexVersionSyncService) Start() {
	if s == nil || s.settingRepo == nil || s.githubClient == nil || s.interval <= 0 {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			s.runInitial()
			for {
				select {
				case <-ticker.C:
					s.runOnce()
				case <-s.stopCh:
					return
				}
			}
		}()
	})
}

func (s *OpenAICodexVersionSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		close(s.stopCh)
	})
	s.wg.Wait()
}

func (s *OpenAICodexVersionSyncService) runInitial() {
	if !s.syncedWithinInterval() {
		s.runOnce()
	}
}

func (s *OpenAICodexVersionSyncService) syncedWithinInterval() bool {
	ctx, cancel := context.WithTimeout(s.baseContext(), openAICodexVersionSyncTimeout)
	defer cancel()
	setting, err := s.settingRepo.Get(ctx, SettingKeyOpenAICodexClientVersionSynced)
	return err == nil && setting != nil && !setting.UpdatedAt.IsZero() && NormalizeCodexClientVersion(setting.Value) != "" && time.Since(setting.UpdatedAt) < s.interval
}

func (s *OpenAICodexVersionSyncService) runOnce() {
	ctx, cancel := context.WithTimeout(s.baseContext(), openAICodexVersionSyncTimeout)
	defer cancel()
	if !s.autoSyncEnabled(ctx) {
		return
	}
	latest := s.fetchLatestStableVersion(ctx)
	if latest == "" {
		return
	}
	current := NormalizeCodexClientVersion(s.currentSyncedVersion(ctx))
	if current != "" && CompareVersions(latest, current) <= 0 {
		return
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAICodexClientVersionSynced, latest); err != nil {
		slog.Warn("openai_codex_version_sync_persist_failed", "version", latest, "error", err)
		return
	}
	if s.settingService != nil {
		s.settingService.InvalidateOpenAICodexClientVersionCache()
	}
}

func (s *OpenAICodexVersionSyncService) baseContext() context.Context {
	if s != nil && s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func (s *OpenAICodexVersionSyncService) fetchLatestStableVersion(ctx context.Context) string {
	release, err := s.githubClient.FetchLatestRelease(ctx, openAICodexVersionSyncRepo)
	if err != nil {
		slog.Warn("openai_codex_version_sync_latest_fetch_failed", "error", err)
	} else if version := latestCodexStableReleaseVersion([]*GitHubRelease{release}); version != "" {
		return version
	}
	releases, err := s.githubClient.FetchRecentReleases(ctx, openAICodexVersionSyncRepo, openAICodexVersionSyncPerPage)
	if err != nil {
		slog.Warn("openai_codex_version_sync_fetch_failed", "error", err)
		return ""
	}
	return latestCodexStableReleaseVersion(releases)
}

func (s *OpenAICodexVersionSyncService) autoSyncEnabled(ctx context.Context) bool {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexVersionAutoSyncEnabled)
	return err != nil || strings.TrimSpace(value) == "" || strings.TrimSpace(value) == "true"
}

func (s *OpenAICodexVersionSyncService) currentSyncedVersion(ctx context.Context) string {
	value, _ := s.settingRepo.GetValue(ctx, SettingKeyOpenAICodexClientVersionSynced)
	return value
}

func latestCodexStableReleaseVersion(releases []*GitHubRelease) string {
	best := ""
	for _, release := range releases {
		if release == nil || release.Draft || release.Prerelease {
			continue
		}
		tag := strings.TrimSpace(release.TagName)
		if !strings.HasPrefix(tag, openAICodexVersionTagPrefix) {
			continue
		}
		version := NormalizeCodexClientVersion(strings.TrimPrefix(tag, openAICodexVersionTagPrefix))
		if version == "" || strings.Contains(version, "-") {
			continue
		}
		if best == "" || CompareVersions(version, best) > 0 {
			best = version
		}
	}
	return best
}
