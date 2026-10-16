package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type blockingTokenRefresher struct{}

func (blockingTokenRefresher) CanRefresh(*Account) bool                  { return true }
func (blockingTokenRefresher) NeedsRefresh(*Account, time.Duration) bool { return true }
func (blockingTokenRefresher) CacheKey(*Account) string                  { return "test:blocking" }
func (blockingTokenRefresher) Refresh(ctx context.Context, _ *Account) (map[string]any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTokenRefreshAttemptTimeoutStopsSlowAccount(t *testing.T) {
	repo := &tokenRefreshPoolRepo{}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:            1,
			AttemptTimeoutSeconds: 1,
		},
	}, nil)
	startedAt := time.Now()
	err := svc.refreshWithRetry(context.Background(), &Account{ID: 901, Platform: PlatformOpenAI}, blockingTokenRefresher{}, nil, time.Hour)
	elapsed := time.Since(startedAt)

	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Contains(t, err.Error(), "token refresh attempt timed out")
	require.GreaterOrEqual(t, elapsed, 900*time.Millisecond)
	require.Less(t, elapsed, 2*time.Second)
	_, _, _, setTempUnschedCalls := repo.snapshot()
	require.Equal(t, 1, setTempUnschedCalls)
}

func TestTokenRefreshAttemptTimeoutBoundsAndDefaults(t *testing.T) {
	defaults := &TokenRefreshService{cfg: &config.TokenRefreshConfig{}}
	require.Equal(t, defaultTokenRefreshAttemptTimeout, defaults.attemptTimeout())

	bounded := &TokenRefreshService{cfg: &config.TokenRefreshConfig{AttemptTimeoutSeconds: int((maxTokenRefreshAttemptTimeout / time.Second) + 100)}}
	require.Equal(t, maxTokenRefreshAttemptTimeout, bounded.attemptTimeout())
}
