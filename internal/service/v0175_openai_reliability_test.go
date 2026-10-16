//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type v0175CountingOpenAI403CounterCache struct {
	openAI403CounterCacheStub
	increments int
}

func (s *v0175CountingOpenAI403CounterCache) IncrementOpenAI403Count(ctx context.Context, accountID int64, window int) (int64, error) {
	s.increments++
	return s.openAI403CounterCacheStub.IncrementOpenAI403Count(ctx, accountID, window)
}

func TestV0175OpenAIHTML403DoesNotPenalizeAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &v0175CountingOpenAI403CounterCache{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(counter)
	account := &Account{ID: 175, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	disable := svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte("  <!DOCTYPE HTML><html>forbidden</html>"))

	require.False(t, disable)
	require.Zero(t, counter.increments)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
}

func TestV0175NestedOpenAIUsagePrecedence(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{
		"usage":{"input_tokens":1,"output_tokens":2},
		"response":{"usage":{"input_tokens":3,"output_tokens":4}},
		"data":{"usage":{"input_tokens":5,"output_tokens":6},"response":{"usage":{"input_tokens":7,"output_tokens":8}}}
	}`))
	require.True(t, ok)
	require.Equal(t, 1, usage.InputTokens)
	require.Equal(t, 2, usage.OutputTokens)

	usage, ok = extractOpenAIUsageFromJSONBytes([]byte(`{"data":{"response":{"usage":{"input_tokens":7,"output_tokens":8}}}}`))
	require.True(t, ok)
	require.Equal(t, 7, usage.InputTokens)
	require.Equal(t, 8, usage.OutputTokens)
}

func TestV0175OpenAIEmptyCompletedDetection(t *testing.T) {
	require.True(t, openAIResponsesCompletedEventIsEmpty([]byte(`{"type":"response.completed","response":{"output":[]}}`), &OpenAIUsage{}))
	require.False(t, openAIResponsesCompletedEventIsEmpty([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":1}}}`), &OpenAIUsage{}))
	require.False(t, openAIResponsesCompletedEventIsEmpty([]byte(`{"type":"response.completed","response":{"output":[{"type":"message"}]}}`), &OpenAIUsage{}))
}

func TestV0175ReasoningItemIDPrefix(t *testing.T) {
	require.True(t, shouldStripOpenAIResponsesInputItemID("reasoning", "item_bad"))
	require.False(t, shouldStripOpenAIResponsesInputItemID("reasoning", "rs_valid"))
}

func TestV0175PoolPassthroughRetryableStatus(t *testing.T) {
	account := &Account{Credentials: map[string]any{"pool_mode": true, "pool_mode_retry_status_codes": []any{float64(http.StatusUnauthorized)}}}
	require.True(t, shouldFailoverOpenAIPassthroughResponse(http.StatusUnauthorized))
	require.True(t, account.IsPoolModeRetryableStatus(http.StatusUnauthorized))
}
