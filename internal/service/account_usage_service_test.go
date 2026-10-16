package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type openAICodexProbeLockStub struct {
	mu     sync.Mutex
	owners map[string]string
	err    error
	keys   []string
	ttls   []time.Duration
}

func (s *openAICodexProbeLockStub) TryAcquireLeaderLock(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	s.ttls = append(s.ttls, ttl)
	if s.err != nil {
		return false, s.err
	}
	if s.owners == nil {
		s.owners = make(map[string]string)
	}
	if _, exists := s.owners[key]; exists {
		return false, nil
	}
	s.owners[key] = owner
	return true, nil
}

func (s *openAICodexProbeLockStub) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owners[key] == owner {
		delete(s.owners, key)
	}
	return nil
}

type accountUsageCodexProbeRepo struct {
	stubOpenAIAccountRepo
	updateExtraCh chan map[string]any
	rateLimitCh   chan time.Time
}

func (r *accountUsageCodexProbeRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	if r.updateExtraCh != nil {
		copied := make(map[string]any, len(updates))
		for k, v := range updates {
			copied[k] = v
		}
		r.updateExtraCh <- copied
	}
	return nil
}

func (r *accountUsageCodexProbeRepo) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	if r.rateLimitCh != nil {
		r.rateLimitCh <- resetAt
	}
	return nil
}

func TestShouldRefreshOpenAICodexSnapshot(t *testing.T) {
	t.Parallel()

	rateLimitedUntil := time.Now().Add(5 * time.Minute)
	now := time.Now()
	usage := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 0},
		SevenDay: &UsageProgress{Utilization: 0},
	}

	if !shouldRefreshOpenAICodexSnapshot(&Account{RateLimitResetAt: &rateLimitedUntil}, usage, now) {
		t.Fatal("expected rate-limited account to force codex snapshot refresh")
	}

	if shouldRefreshOpenAICodexSnapshot(&Account{}, usage, now) {
		t.Fatal("expected complete non-rate-limited usage to skip codex snapshot refresh")
	}

	if !shouldRefreshOpenAICodexSnapshot(&Account{}, &UsageInfo{FiveHour: nil, SevenDay: &UsageProgress{}}, now) {
		t.Fatal("expected missing 5h snapshot to require refresh")
	}

	staleAt := now.Add(-(openAIProbeCacheTTL + time.Minute)).Format(time.RFC3339)
	if !shouldRefreshOpenAICodexSnapshot(&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"codex_usage_updated_at":                       staleAt,
		},
	}, usage, now) {
		t.Fatal("expected stale ws snapshot to trigger refresh")
	}
}

func TestAccountUsageService_OpenAICodexProbeUsesCrossInstanceLock(t *testing.T) {
	lock := &openAICodexProbeLockStub{}
	first := NewAccountUsageService(nil, nil, nil, nil, nil, NewUsageCache(), nil, nil)
	second := NewAccountUsageService(nil, nil, nil, nil, nil, NewUsageCache(), nil, nil)
	first.SetOpenAICodexUsageProbeLock(lock)
	second.SetOpenAICodexUsageProbeLock(lock)

	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	releaseFirst, acquiredFirst := first.tryAcquireOpenAICodexSnapshotProbe(context.Background(), 42, now, false)
	if !acquiredFirst {
		t.Fatal("first instance should acquire the Codex quota probe lock")
	}
	if _, acquiredForced := first.tryAcquireOpenAICodexSnapshotProbe(context.Background(), 42, now, true); acquiredForced {
		t.Fatal("same-instance force refresh must not overlap an in-flight Codex quota probe")
	}

	_, acquiredSecond := second.tryAcquireOpenAICodexSnapshotProbe(context.Background(), 42, now, false)
	if acquiredSecond {
		t.Fatal("second instance must not start a duplicate Codex quota probe")
	}

	releaseFirst()
	releaseSecond, acquiredSecond := second.tryAcquireOpenAICodexSnapshotProbe(
		context.Background(), 42, now.Add(openAIProbePeerLockRetryTTL+time.Millisecond), false,
	)
	if !acquiredSecond {
		t.Fatal("peer lock suppression should expire quickly instead of becoming a 10-minute local cooldown")
	}
	releaseSecond()

	lock.mu.Lock()
	defer lock.mu.Unlock()
	if len(lock.keys) != 3 {
		t.Fatalf("lock attempts = %d, want 3", len(lock.keys))
	}
	if got, want := lock.keys[0], "codex-quota-probe:account:42"; got != want {
		t.Fatalf("lock key = %q, want %q", got, want)
	}
	if lock.ttls[0] < 45*time.Second {
		t.Fatalf("lock TTL = %s, want at least 45s", lock.ttls[0])
	}
}

func TestAccountUsageService_OpenAICodexProbeConcurrentInstancesOnlyOneWins(t *testing.T) {
	lock := &openAICodexProbeLockStub{}
	services := []*AccountUsageService{
		NewAccountUsageService(nil, nil, nil, nil, nil, NewUsageCache(), nil, nil),
		NewAccountUsageService(nil, nil, nil, nil, nil, NewUsageCache(), nil, nil),
	}
	for _, svc := range services {
		svc.SetOpenAICodexUsageProbeLock(lock)
	}

	type result struct {
		release  func()
		acquired bool
	}
	start := make(chan struct{})
	results := make(chan result, len(services))
	for _, svc := range services {
		go func(svc *AccountUsageService) {
			<-start
			release, acquired := svc.tryAcquireOpenAICodexSnapshotProbe(context.Background(), 99, time.Now(), false)
			results <- result{release: release, acquired: acquired}
		}(svc)
	}
	close(start)

	acquiredCount := 0
	releases := make([]func(), 0, 1)
	for range services {
		result := <-results
		if result.acquired {
			acquiredCount++
			releases = append(releases, result.release)
		}
	}
	for _, release := range releases {
		release()
	}
	if acquiredCount != 1 {
		t.Fatalf("concurrent probe winners = %d, want exactly 1", acquiredCount)
	}
}

func TestAccountUsageService_OpenAICodexProbeLockFailureFallsBackToLocalCooldown(t *testing.T) {
	lock := &openAICodexProbeLockStub{err: errors.New("redis unavailable")}
	svc := NewAccountUsageService(nil, nil, nil, nil, nil, NewUsageCache(), nil, nil)
	svc.SetOpenAICodexUsageProbeLock(lock)
	now := time.Date(2026, 8, 19, 11, 0, 0, 0, time.UTC)

	release, acquired := svc.tryAcquireOpenAICodexSnapshotProbe(context.Background(), 7, now, false)
	if !acquired {
		t.Fatal("Redis lock failure must not block the usage page probe")
	}
	release()
	if _, acquired := svc.tryAcquireOpenAICodexSnapshotProbe(context.Background(), 7, now.Add(time.Second), false); acquired {
		t.Fatal("local cooldown must still protect the upstream when Redis is unavailable")
	}
}

func TestExtractOpenAICodexProbeUpdatesAccepts429WithCodexHeaders(t *testing.T) {
	t.Parallel()

	headers := make(http.Header)
	headers.Set("x-codex-primary-used-percent", "100")
	headers.Set("x-codex-primary-reset-after-seconds", "604800")
	headers.Set("x-codex-primary-window-minutes", "10080")
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "18000")
	headers.Set("x-codex-secondary-window-minutes", "300")

	updates, err := extractOpenAICodexProbeUpdates(&http.Response{StatusCode: http.StatusTooManyRequests, Header: headers})
	if err != nil {
		t.Fatalf("extractOpenAICodexProbeUpdates() error = %v", err)
	}
	if len(updates) == 0 {
		t.Fatal("expected codex probe updates from 429 headers")
	}
	if got := updates["codex_5h_used_percent"]; got != 100.0 {
		t.Fatalf("codex_5h_used_percent = %v, want 100", got)
	}
	if got := updates["codex_7d_used_percent"]; got != 100.0 {
		t.Fatalf("codex_7d_used_percent = %v, want 100", got)
	}
}

func TestAccountUsageService_PersistOpenAICodexProbeSnapshotOnlyUpdatesExtra(t *testing.T) {
	t.Parallel()

	repo := &accountUsageCodexProbeRepo{
		updateExtraCh: make(chan map[string]any, 1),
		rateLimitCh:   make(chan time.Time, 1),
	}
	svc := &AccountUsageService{accountRepo: repo}
	svc.persistOpenAICodexProbeSnapshot(321, map[string]any{
		"codex_7d_used_percent": 100.0,
		"codex_7d_reset_at":     time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
	})

	select {
	case updates := <-repo.updateExtraCh:
		if got := updates["codex_7d_used_percent"]; got != 100.0 {
			t.Fatalf("codex_7d_used_percent = %v, want 100", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待 codex 探测快照写入 extra 超时")
	}

	select {
	case got := <-repo.rateLimitCh:
		t.Fatalf("不应将探测快照写入运行时限流状态: %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestAccountUsageService_GetOpenAIUsage_DoesNotPromoteCodexExtraToRateLimit(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().Add(6 * 24 * time.Hour).UTC().Truncate(time.Second)
	repo := &accountUsageCodexProbeRepo{
		rateLimitCh: make(chan time.Time, 1),
	}
	svc := &AccountUsageService{accountRepo: repo}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_5h_used_percent": 1.0,
			"codex_5h_reset_at":     time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
			"codex_7d_used_percent": 100.0,
			"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
		},
	}

	usage, err := svc.getOpenAIUsage(context.Background(), account, false)
	if err != nil {
		t.Fatalf("getOpenAIUsage() error = %v", err)
	}
	if usage.SevenDay == nil || usage.SevenDay.Utilization != 100.0 {
		t.Fatalf("预期 7 天用量仍然可见，实际为 %#v", usage.SevenDay)
	}
	if account.RateLimitResetAt != nil {
		t.Fatalf("不应让已耗尽的 codex extra 改写运行时限流状态: %v", account.RateLimitResetAt)
	}
	select {
	case got := <-repo.rateLimitCh:
		t.Fatalf("不应将已耗尽的 codex extra 持久化为运行时限流状态: %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBuildCodexUsageProgressFromExtra_ZerosExpiredWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)

	t.Run("expired 5h window zeroes utilization", func(t *testing.T) {
		extra := map[string]any{
			"codex_5h_used_percent": 42.0,
			"codex_5h_reset_at":     "2026-03-16T10:00:00Z", // 2h ago
		}
		progress := buildCodexUsageProgressFromExtra(extra, "5h", now)
		if progress == nil {
			t.Fatal("expected non-nil progress")
		}
		if progress.Utilization != 0 {
			t.Fatalf("expected Utilization=0 for expired window, got %v", progress.Utilization)
		}
		if progress.RemainingSeconds != 0 {
			t.Fatalf("expected RemainingSeconds=0, got %v", progress.RemainingSeconds)
		}
	})

	t.Run("active 5h window keeps utilization", func(t *testing.T) {
		resetAt := now.Add(2 * time.Hour).Format(time.RFC3339)
		extra := map[string]any{
			"codex_5h_used_percent": 42.0,
			"codex_5h_reset_at":     resetAt,
		}
		progress := buildCodexUsageProgressFromExtra(extra, "5h", now)
		if progress == nil {
			t.Fatal("expected non-nil progress")
		}
		if progress.Utilization != 42.0 {
			t.Fatalf("expected Utilization=42, got %v", progress.Utilization)
		}
	})

	t.Run("expired 7d window zeroes utilization", func(t *testing.T) {
		extra := map[string]any{
			"codex_7d_used_percent": 88.0,
			"codex_7d_reset_at":     "2026-03-15T00:00:00Z", // yesterday
		}
		progress := buildCodexUsageProgressFromExtra(extra, "7d", now)
		if progress == nil {
			t.Fatal("expected non-nil progress")
		}
		if progress.Utilization != 0 {
			t.Fatalf("expected Utilization=0 for expired 7d window, got %v", progress.Utilization)
		}
	})
}
