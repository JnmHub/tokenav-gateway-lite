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

func TestOpenAI429FastPath_KeepsOAuthAccountSchedulableDuringRetryWindow(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	shouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, nil)
	apiKeyShouldDisable := svc.handleOpenAIAccountUpstreamError(context.Background(), apiKeyAccount, http.StatusTooManyRequests, http.Header{}, nil)

	require.False(t, shouldDisable)
	require.False(t, apiKeyShouldDisable)
	// 普通 429 没有配额耗尽或 reset 信号时，先保留账号可调度，
	// 由请求级同账号重试窗口吸收短暂抖动；只有窗口耗尽才会冷却账号。
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.True(t, svc.ShouldRetryOpenAIOAuth429(account, http.Header{}, nil))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(apiKeyAccount))
}

func TestOpenAI429FastPath_SetupTokenUsesCodex429Disposition(t *testing.T) {
	setupToken := &Account{ID: 4242, Platform: PlatformOpenAI, Type: AccountTypeSetupToken}
	svc := &OpenAIGatewayService{}

	// 没有配额窗口信号时，Setup Token 与 OAuth 一样进入短暂同账号重试窗口。
	require.True(t, svc.ShouldRetryOpenAIOAuth429(setupToken, http.Header{}, nil))
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(setupToken))

	// 明确的 5 小时窗口耗尽必须立即隔离，不能继续同账号重试。
	headers := http.Header{}
	headers.Set("x-codex-secondary-used-percent", "100")
	headers.Set("x-codex-secondary-reset-after-seconds", "18000")
	headers.Set("x-codex-secondary-window-minutes", "300")
	svc.markOpenAIOAuth429RateLimited(context.Background(), setupToken, headers, nil)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(setupToken))
	require.False(t, svc.ShouldRetryOpenAIOAuth429(setupToken, headers, nil))
}

func TestOpenAIRuntimeBlock_AppliesToOpenAIAPIKeyWhenRateLimitServiceStopsScheduling(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	svc.BlockAccountScheduling(account, time.Time{}, "custom_error_code")

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlock_DoesNotApplyToOtherPlatforms(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 45, Platform: PlatformGemini, Type: AccountTypeOAuth}

	svc.BlockAccountScheduling(account, time.Time{}, "custom_error_code")

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIRuntimeBlocker_IgnoresNonOpenAIFromRateLimitService(t *testing.T) {
	gateway := &OpenAIGatewayService{}
	repo := &rateLimitAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitService.SetAccountRuntimeBlocker(gateway)
	account := &Account{ID: 45, Platform: PlatformGemini, Type: AccountTypeOAuth}

	shouldDisable := rateLimitService.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte("forbidden"))

	require.True(t, shouldDisable)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
}

func TestOpenAIModelNotFound_DoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAIModelNotFoundTempAccount()

	shouldDisable := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusNotFound,
		http.Header{},
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
		"gpt-5.4",
	)

	require.True(t, shouldDisable)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestOpenAICodexPlanGatedModel_DoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		rateLimitService: &RateLimitService{accountRepo: repo},
	}
	account := openAICodexPlanGatedOAuthAccount()

	shouldFailover := svc.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"detail":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}`),
		"gpt-5.6-sol",
	)

	require.True(t, shouldFailover)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
}

func TestOpenAIRuntimeBlock_DoesNotShortenExistingBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 46, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	longUntil := time.Now().Add(10 * time.Minute)

	svc.BlockAccountScheduling(account, longUntil, "oauth_401")
	svc.BlockAccountScheduling(account, time.Time{}, "upstream_disable")

	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.True(t, ok)
	actualUntil, ok := value.(time.Time)
	require.True(t, ok)
	require.WithinDuration(t, longUntil, actualUntil, time.Second)
}

func TestOpenAIRuntimeBlock_ClearAccountSchedulingBlock(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	svc.BlockAccountScheduling(account, time.Now().Add(time.Minute), "429")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))

	svc.ClearAccountSchedulingBlock(account.ID)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestShouldStopOpenAIOAuth429Failover_OnlyDuringStorm(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	apiKeyAccount := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1))

	for i := 0; i < openAIOAuth429StormThreshold; i++ {
		svc.recordOpenAIOAuth429()
	}

	require.True(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 1))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(apiKeyAccount, http.StatusTooManyRequests, 1))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusInternalServerError, 1))
	require.False(t, svc.ShouldStopOpenAIOAuth429Failover(account, http.StatusTooManyRequests, 0))
}

func TestOpenAIFailoverSwitchBudget_ClassifiesOpenAIFailures(t *testing.T) {
	openAIAccount := &Account{ID: 48, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, testCase := range []struct {
		name          string
		failoverError *UpstreamFailoverError
		configuredMax int
		want          int
	}{
		{
			name:          "500 gives one model failover",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusInternalServerError},
			configuredMax: 10,
			want:          1,
		},
		{
			name:          "503 gives one model failover",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable},
			configuredMax: 10,
			want:          1,
		},
		{
			name:          "429 gives two account failovers",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests},
			configuredMax: 10,
			want:          2,
		},
		{
			name:          "401 gives three credential failovers",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusUnauthorized},
			configuredMax: 10,
			want:          3,
		},
		{
			name:          "403 gives three credential failovers",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusForbidden},
			configuredMax: 10,
			want:          3,
		},
		{
			name:          "400 validation error never fails over",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusBadRequest, ResponseBody: []byte(`{"error":{"message":"invalid request"}}`)},
			configuredMax: 10,
			want:          0,
		},
		{
			name:          "404 account capability gets one replacement",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusNotFound},
			configuredMax: 10,
			want:          1,
		},
		{
			name:          "account body limit gets one replacement",
			failoverError: newOpenAIRequestBodyTooLargeFailoverError(http.StatusRequestEntityTooLarge, nil, nil),
			configuredMax: 10,
			want:          1,
		},
		{
			name:          "request scoped transient never fans out",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, RequestScopedTransient: true},
			configuredMax: 10,
			want:          0,
		},
		{
			name:          "configured maximum tightens credential budget",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusUnauthorized},
			configuredMax: 2,
			want:          2,
		},
		{
			name:          "configured maximum tightens account budget",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests},
			configuredMax: 1,
			want:          1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, OpenAIFailoverSwitchBudget(openAIAccount, testCase.failoverError, testCase.configuredMax))
		})
	}
}

func TestOpenAIFailoverSwitchBudget_KeepsConfiguredLimitForOtherPlatforms(t *testing.T) {
	geminiAccount := &Account{ID: 49, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	failoverError := &UpstreamFailoverError{StatusCode: http.StatusBadRequest}

	require.Equal(t, 7, OpenAIFailoverSwitchBudget(geminiAccount, failoverError, 7))
}

func TestOpenAIFailoverSwitchBackoff_ClassifiesOpenAIFailures(t *testing.T) {
	openAIAccount := &Account{ID: 50, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	for _, testCase := range []struct {
		name          string
		failoverError *UpstreamFailoverError
		switchCount   int
		want          time.Duration
	}{
		{
			name:          "model transient waits one hundred milliseconds",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusInternalServerError},
			switchCount:   1,
			want:          100 * time.Millisecond,
		},
		{
			name:          "account failure first switch waits one hundred milliseconds",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests},
			switchCount:   1,
			want:          100 * time.Millisecond,
		},
		{
			name:          "account failure second switch waits two hundred milliseconds",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests},
			switchCount:   2,
			want:          200 * time.Millisecond,
		},
		{
			name:          "account failure third switch waits four hundred milliseconds",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests},
			switchCount:   3,
			want:          400 * time.Millisecond,
		},
		{
			name:          "credential failure switches immediately",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusUnauthorized},
			switchCount:   1,
			want:          0,
		},
		{
			name:          "request scoped transient switches immediately",
			failoverError: &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, RequestScopedTransient: true},
			switchCount:   1,
			want:          0,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.want, OpenAIFailoverSwitchBackoff(openAIAccount, testCase.failoverError, testCase.switchCount))
		})
	}
}
