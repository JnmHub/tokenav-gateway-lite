//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIModelTransient_ProgressiveCooldownAndIsolation(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)

	first := state.recordFailure(35, "gpt-5.6-sol", now)
	require.Equal(t, 1, first.FailureStreak)
	require.Zero(t, first.Cooldown)

	second := state.recordFailure(35, "GPT-5.6-SOL", now.Add(time.Second))
	require.Equal(t, 2, second.FailureStreak)
	require.Equal(t, openAIModelTransientShortCooldown, second.Cooldown)
	require.True(t, state.isBlocked(35, "gpt-5.6-sol", now.Add(2*time.Second)))
	require.False(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	require.False(t, state.isBlocked(47, "gpt-5.6-sol", now.Add(2*time.Second)))

	third := state.recordFailure(35, "gpt-5.6-sol", now.Add(3*time.Second))
	require.Equal(t, 3, third.FailureStreak)
	require.Equal(t, openAIModelTransientLongCooldown, third.Cooldown)
	require.True(t, state.isBlocked(35, "gpt-5.6-sol", now.Add(40*time.Second)))
	require.False(t, state.isBlocked(35, "gpt-5.6-sol", now.Add(49*time.Second)))
}

func TestOpenAIModelTransient_SuccessAndStaleWindowResetState(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	state.recordFailure(35, "gpt-5.5", now)
	state.recordFailure(35, "gpt-5.5", now.Add(time.Second))
	require.True(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))

	state.recordSuccess(35, "gpt-5.5")
	require.False(t, state.isBlocked(35, "gpt-5.5", now.Add(2*time.Second)))
	require.Equal(t, 1, state.recordFailure(35, "gpt-5.5", now.Add(3*time.Second)).FailureStreak)

	stale := state.recordFailure(35, "gpt-5.5", now.Add(openAIModelTransientStreakTTL+5*time.Second))
	require.Equal(t, 1, stale.FailureStreak)
	require.Zero(t, stale.Cooldown)
}

func TestOpenAIModelTransient_StreakSurvivesSparseTraffic(t *testing.T) {
	state := newOpenAIAccountModelTransientState(128)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	gap := 5 * time.Minute

	first := state.recordFailure(35, "gpt-5.5", now)
	second := state.recordFailure(35, "gpt-5.5", now.Add(gap))
	third := state.recordFailure(35, "gpt-5.5", now.Add(2*gap))

	require.Equal(t, 1, first.FailureStreak)
	require.Equal(t, 2, second.FailureStreak)
	require.Equal(t, openAIModelTransientShortCooldown, second.Cooldown)
	require.Equal(t, 3, third.FailureStreak)
	require.Equal(t, openAIModelTransientLongCooldown, third.Cooldown)
}

func TestOpenAIModelTransient_InvalidAndConcurrentStateIsBounded(t *testing.T) {
	const maxEntries = 16
	state := newOpenAIAccountModelTransientState(maxEntries)
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)

	require.Zero(t, state.recordFailure(0, "gpt-5.5", now).FailureStreak)
	require.Zero(t, state.recordFailure(35, " ", now).FailureStreak)
	require.Zero(t, state.recordFailure(35, strings.Repeat("m", openAIModelTransientMaxModelBytes+1), now).FailureStreak)

	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("gpt-test-%d", i)
			state.recordFailure(int64(i+1), model, now.Add(time.Duration(i)*time.Millisecond))
			_ = state.isBlocked(int64(i+1), model, now.Add(time.Second))
		}(i)
	}
	wg.Wait()
	require.LessOrEqual(t, state.size(), maxEntries)
}

func TestOpenAIModelTransient_APIKey503UsesCanonicalModelAndKeepsOtherModelsSchedulable(t *testing.T) {
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{}}
	account := &Account{
		ID:          201,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"customer-alias": "gpt-5.6-sol"},
		},
	}
	canonical := canonicalOpenAIAccountSchedulingModel(account, "customer-alias")
	require.Equal(t, "gpt-5.6-sol", canonical)

	for i := 0; i < 2; i++ {
		disabled := svc.handleOpenAIAccountUpstreamError(
			context.Background(), account, http.StatusServiceUnavailable, http.Header{},
			[]byte(`{"error":{"code":"upstream_unavailable","message":"temporary upstream failure"}}`), canonical,
		)
		require.False(t, disabled)
	}

	require.True(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "customer-alias"))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "gpt-5.5"))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "模型瞬态故障不能升级成账号级停用")

	svc.ReportOpenAIAccountScheduleResult(account.ID, true, nil, canonical)
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(account, "customer-alias"))
}

func TestOpenAIModelTransient_FixedGroupSelectionSkipsOnlyBlockedAccountModelWhenCacheUnavailable(t *testing.T) {
	// cache=nil simulates Redis/cache unavailability. Model cooldown state is a
	// bounded local safety hint and must keep fixed-group selection available.
	svc := &OpenAIGatewayService{cache: nil}
	blocked := Account{ID: 301, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 0}
	healthy := Account{ID: 302, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Priority: 10}
	groupID := int64(77)
	now := time.Now()
	svc.recordOpenAIAccountModelTransientFailure(&blocked, "gpt-5.6-sol", now)
	svc.recordOpenAIAccountModelTransientFailure(&blocked, "gpt-5.6-sol", now.Add(time.Millisecond))

	selected, _ := svc.selectBestAccount(context.Background(), &groupID, []Account{blocked, healthy}, "gpt-5.6-sol", nil, false, "")
	require.NotNil(t, selected)
	require.Equal(t, healthy.ID, selected.ID)

	selected, _ = svc.selectBestAccount(context.Background(), &groupID, []Account{blocked, healthy}, "gpt-5.5", nil, false, "")
	require.NotNil(t, selected)
	require.Equal(t, blocked.ID, selected.ID, "同账号其他模型仍应按原优先级参与调度")
}

func TestShouldCooldownOpenAITransientUpstreamError_DoesNotDowngradeCredentialOrClientErrors(t *testing.T) {
	require.True(t, shouldCooldownOpenAITransientUpstreamError(http.StatusBadGateway, nil))
	require.True(t, shouldCooldownOpenAITransientUpstreamError(http.StatusBadRequest, []byte(`{"error":{"message":"Selected model is at capacity"}}`)))
	require.False(t, shouldCooldownOpenAITransientUpstreamError(http.StatusUnauthorized, []byte(`{"error":{"code":"token_revoked"}}`)))
	require.False(t, shouldCooldownOpenAITransientUpstreamError(http.StatusForbidden, nil))
	require.False(t, shouldCooldownOpenAITransientUpstreamError(http.StatusTooManyRequests, nil))
	require.False(t, shouldCooldownOpenAITransientUpstreamError(http.StatusBadRequest, []byte(`{"error":{"message":"maximum context length exceeded"}}`)))
}

func TestClassifyOpenAIUpstreamFailure_SeparatesModelAccountCredentialAndClientErrors(t *testing.T) {
	require.Equal(t, openAIUpstreamFailureModelTransient, classifyOpenAIUpstreamFailure(http.StatusServiceUnavailable, nil))
	require.Equal(t, openAIUpstreamFailureModelTransient, classifyOpenAIUpstreamFailure(http.StatusBadRequest, []byte(`{"error":{"code":"server_is_overloaded"}}`)))
	require.Equal(t, openAIUpstreamFailureCredential, classifyOpenAIUpstreamFailure(http.StatusUnauthorized, []byte(`{"error":{"code":"token_revoked"}}`)))
	require.Equal(t, openAIUpstreamFailureCredential, classifyOpenAIUpstreamFailure(http.StatusForbidden, []byte(`{"error":{"message":"account banned"}}`)))
	require.Equal(t, openAIUpstreamFailureAccount, classifyOpenAIUpstreamFailure(http.StatusTooManyRequests, []byte(`{"error":{"message":"quota exhausted"}}`)))
	require.Equal(t, openAIUpstreamFailureNonRetryable, classifyOpenAIUpstreamFailure(http.StatusBadRequest, []byte(`{"error":{"message":"unknown field"}}`)))
	require.Equal(t, openAIUpstreamFailureNonRetryable, classifyOpenAIUpstreamFailure(http.StatusBadRequest, []byte(`{"error":{"message":"maximum context length exceeded"}}`)))
}
