package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type blockingSchedulerSettingRepo struct {
	*fakeSettingRepo
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingSchedulerSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return r.fakeSettingRepo.GetValue(ctx, key)
}

func TestFallbackQueueSettingsServesStaleValueDuringRefresh(t *testing.T) {
	repo := &blockingSchedulerSettingRepo{
		fakeSettingRepo: &fakeSettingRepo{vals: map[string]string{
			SettingKeyFallbackQueueSettings: `{"enabled":true,"max_waiting":321,"wait_timeout_seconds":12}`,
		}},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc := &SettingService{settingRepo: repo}
	stale := FallbackQueueSettings{Enabled: false, MaxWaiting: 7, WaitTimeoutSeconds: 9}
	svc.fallbackQueueSettingsCache.Store(&cachedFallbackQueueSettings{settings: stale, expiresAt: time.Now().Add(-time.Second).UnixNano()})

	startedAt := time.Now()
	got, err := svc.GetFallbackQueueSettings(context.Background())
	require.NoError(t, err)
	require.Less(t, time.Since(startedAt), 100*time.Millisecond)
	require.Equal(t, stale, *got)
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("background fallback settings refresh did not start")
	}
	close(repo.release)
	require.Eventually(t, func() bool {
		cached, _ := svc.fallbackQueueSettingsCache.Load().(*cachedFallbackQueueSettings)
		return cached != nil && cached.settings.Enabled && cached.settings.MaxWaiting == 321
	}, time.Second, 10*time.Millisecond)
}

func TestAdvancedSchedulerSettingServesStaleValueDuringRefresh(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	repo := &blockingSchedulerSettingRepo{
		fakeSettingRepo: &fakeSettingRepo{vals: map[string]string{openAIAdvancedSchedulerSettingKey: "true"}},
		started:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{settingService: &SettingService{settingRepo: repo}}}
	openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{enabled: false, expiresAt: time.Now().Add(-time.Second).UnixNano()})

	startedAt := time.Now()
	require.False(t, svc.isOpenAIAdvancedSchedulerEnabled(context.Background()))
	require.Less(t, time.Since(startedAt), 100*time.Millisecond)
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("background advanced scheduler refresh did not start")
	}
	close(repo.release)
	require.Eventually(t, func() bool {
		cached, _ := openAIAdvancedSchedulerSettingCache.Load().(*cachedOpenAIAdvancedSchedulerSetting)
		return cached != nil && cached.enabled
	}, time.Second, 10*time.Millisecond)
}
