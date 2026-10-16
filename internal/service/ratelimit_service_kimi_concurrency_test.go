//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHandle403_KimiConcurrencyLimitBypassesPermanentCounter(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{openAI403DisableThreshold}}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	service.SetAccountRuntimeBlocker(blocker)
	account := &Account{ID: 1803, Platform: PlatformKimi, Type: AccountTypeAPIKey}

	shouldDisable := service.HandleUpstreamError(
		context.Background(), account, http.StatusForbidden, http.Header{},
		[]byte(`{"error":{"message":"You've reached your concurrent request limit. Please wait for your ongoing requests to finish and try again."}}`),
	)

	require.True(t, shouldDisable, "当前请求应切换其他账号")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, cnConcurrencyLimitReasonPrefix)
	require.Equal(t, []int64{openAI403DisableThreshold}, counter.counts,
		"Kimi 并发 403 不应消耗通用 403 永久禁用计数")
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, cnConcurrencyLimitReasonPrefix, blocker.reasons[0])
	require.True(t, blocker.until[0].After(time.Now()))
}

func TestHandle403_KimiConcurrencyLimitRequiresExactMessage(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{openAI403DisableThreshold}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{ID: 1804, Platform: PlatformKimi, Type: AccountTypeAPIKey}

	shouldDisable := service.HandleUpstreamError(
		context.Background(), account, http.StatusForbidden, http.Header{},
		[]byte(`{"error":{"message":"concurrent request limit reached"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "非精确文案仍按普通 403 策略处理")
	require.Equal(t, 0, repo.tempCalls)
	require.Empty(t, counter.counts)
}

func TestIsCNProviderConcurrencyLimit403OnlyMatchesKimi(t *testing.T) {
	require.True(t, isCNProviderConcurrencyLimit403(
		&Account{Platform: PlatformKimi}, kimiConcurrentRequestLimitMessage,
	))
	require.True(t, isCNProviderConcurrencyLimit403(
		&Account{Platform: PlatformKimi}, "  "+kimiConcurrentRequestLimitMessage+"\n",
	))
	require.False(t, isCNProviderConcurrencyLimit403(
		&Account{Platform: PlatformZhipu}, kimiConcurrentRequestLimitMessage,
	))
	require.False(t, isCNProviderConcurrencyLimit403(
		&Account{Platform: PlatformKimi}, "concurrent request limit reached",
	))
}
