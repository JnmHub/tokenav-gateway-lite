//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexVersionSyncRepo struct {
	mu        sync.Mutex
	values    map[string]string
	updatedAt map[string]time.Time
	setCalls  int
}

func newCodexVersionSyncRepo() *codexVersionSyncRepo {
	return &codexVersionSyncRepo{values: make(map[string]string), updatedAt: make(map[string]time.Time)}
}

func (r *codexVersionSyncRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value, UpdatedAt: r.updatedAt[key]}, nil
}

func (r *codexVersionSyncRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[key], nil
}

func (r *codexVersionSyncRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	r.updatedAt[key] = time.Now()
	r.setCalls++
	return nil
}

func (r *codexVersionSyncRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *codexVersionSyncRepo) SetMultiple(ctx context.Context, values map[string]string) error {
	for key, value := range values {
		if err := r.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *codexVersionSyncRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *codexVersionSyncRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	delete(r.updatedAt, key)
	return nil
}

type codexVersionSyncGitHub struct {
	latest       *GitHubRelease
	recent       []*GitHubRelease
	err          error
	latestCalls  int
	recentCalls  int
	requestStart chan struct{}
}

func (c *codexVersionSyncGitHub) FetchLatestRelease(ctx context.Context, _ string) (*GitHubRelease, error) {
	c.latestCalls++
	if c.requestStart != nil {
		select {
		case c.requestStart <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.latest, c.err
}

func (c *codexVersionSyncGitHub) FetchRecentReleases(context.Context, string, int) ([]*GitHubRelease, error) {
	c.recentCalls++
	return c.recent, c.err
}

func (c *codexVersionSyncGitHub) DownloadFile(context.Context, string, string, int64) error {
	return errors.New("not implemented")
}

func (c *codexVersionSyncGitHub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAICodexVersionSyncSelectsLatestStableAndOnlyMovesForward(t *testing.T) {
	repo := newCodexVersionSyncRepo()
	repo.values[SettingKeyOpenAICodexVersionAutoSyncEnabled] = "true"
	repo.values[SettingKeyOpenAICodexClientVersionSynced] = "0.146.0"
	client := &codexVersionSyncGitHub{recent: []*GitHubRelease{
		{TagName: "rust-v0.148.0-alpha.1", Prerelease: true},
		{TagName: "rust-v0.147.0"},
		{TagName: "rust-v0.145.0"},
	}}
	service := NewOpenAICodexVersionSyncService(repo, nil, client, time.Hour)
	client.err = errors.New("latest unavailable")
	service.runOnce()
	require.Equal(t, "0.146.0", repo.values[SettingKeyOpenAICodexClientVersionSynced])
	require.Zero(t, repo.setCalls)

	client.err = nil
	client.latest = &GitHubRelease{TagName: "rust-v0.147.0"}
	service.runOnce()
	require.Equal(t, "0.147.0", repo.values[SettingKeyOpenAICodexClientVersionSynced])
	require.Equal(t, 1, repo.setCalls)
}

func TestOpenAICodexVersionSyncHonorsDisabledAndFreshStartupDebounce(t *testing.T) {
	repo := newCodexVersionSyncRepo()
	repo.values[SettingKeyOpenAICodexVersionAutoSyncEnabled] = "false"
	client := &codexVersionSyncGitHub{latest: &GitHubRelease{TagName: "rust-v0.147.0"}}
	service := NewOpenAICodexVersionSyncService(repo, nil, client, time.Hour)
	service.runOnce()
	require.Zero(t, client.latestCalls)

	repo.values[SettingKeyOpenAICodexVersionAutoSyncEnabled] = "true"
	repo.values[SettingKeyOpenAICodexClientVersionSynced] = "0.146.0"
	repo.updatedAt[SettingKeyOpenAICodexClientVersionSynced] = time.Now()
	service.runInitial()
	require.Zero(t, client.latestCalls)
}

func TestOpenAICodexVersionSyncStopCancelsInflightFetch(t *testing.T) {
	repo := newCodexVersionSyncRepo()
	repo.values[SettingKeyOpenAICodexVersionAutoSyncEnabled] = "true"
	started := make(chan struct{}, 1)
	client := &codexVersionSyncGitHub{requestStart: started}
	service := NewOpenAICodexVersionSyncService(repo, nil, client, time.Hour)
	service.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("version sync did not start")
	}
	done := make(chan struct{})
	go func() {
		service.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel the in-flight GitHub request")
	}
}

func TestSettingServiceCodexVersionPriority(t *testing.T) {
	repo := newCodexVersionSyncRepo()
	repo.values[SettingKeyOpenAICodexClientVersionSynced] = "0.147.0"
	service := NewSettingService(repo, &config.Config{})
	require.Equal(t, "0.147.0", service.GetOpenAICodexClientVersion(context.Background()))

	repo.values[SettingKeyOpenAICodexClientVersion] = "0.148.0"
	service.InvalidateOpenAICodexClientVersionCache()
	require.Equal(t, "0.148.0", service.GetOpenAICodexClientVersion(context.Background()))

	delete(repo.values, SettingKeyOpenAICodexClientVersion)
	delete(repo.values, SettingKeyOpenAICodexClientVersionSynced)
	service.InvalidateOpenAICodexClientVersionCache()
	require.Equal(t, codexCLIVersion, service.GetOpenAICodexClientVersion(context.Background()))
}
