//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

// tailBurst403AccountRepo records the durable marker update while reusing the
// normal rate-limit test repository for the 403 handling side effects.
type tailBurst403AccountRepo struct {
	rateLimitAccountRepoStub
	updates []map[string]any
}

func (r *tailBurst403AccountRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updates = append(r.updates, updates)
	return nil
}

func TestIsOpenAIOAuthQuotaExhausted403(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	tests := []struct {
		name  string
		body  string
		valid bool
	}{
		{
			name:  "structured insufficient quota code",
			body:  `{"error":{"code":"insufficient_quota","message":"quota unavailable"}}`,
			valid: true,
		},
		{
			name:  "structured usage limit type",
			body:  `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`,
			valid: true,
		},
		{
			name:  "explicit quota message",
			body:  `{"error":{"message":"You exceeded your current quota"}}`,
			valid: true,
		},
		{
			name:  "permission denied is not quota",
			body:  `{"error":{"code":"permission_denied","message":"model access denied"}}`,
			valid: false,
		},
		{
			name:  "html edge response is not quota",
			body:  `<!doctype html><html><body>quota exceeded</body></html>`,
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.valid, isOpenAIOAuthQuotaExhausted403(account, []byte(tt.body)))
		})
	}

	apiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.False(t, isOpenAIOAuthQuotaExhausted403(apiKey, []byte(tests[0].body)))
}

func TestHandleUpstreamError_OpenAIOAuth403QuotaSettlesTailBurst(t *testing.T) {
	repo := &tailBurst403AccountRepo{}
	usage := &tailBurstTestUsageRepo{stats: &usagestats.UsageStats{TotalRequests: 7, TotalTokens: 123}}
	service := NewRateLimitService(repo, usage, nil, nil, nil)
	service.SetOpenAI403CounterCache(&openAI403CounterCacheStub{counts: []int64{1}})

	account := newTailBurstAccount()
	account.Extra = codexTailBurstEntryUpdates("5h", codexTailBurstReasonUsedRatio, 98, nil, time.Now().UTC().Add(-time.Minute))

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"code":"insufficient_quota","message":"quota exhausted"}}`),
	)

	require.True(t, shouldDisable, "403 仍需触发当前账号的受控换号")
	require.Equal(t, 1, usage.calls, "额度拒绝应立即结算尾部会话")
	require.Len(t, repo.updates, 1)
	require.Equal(t, false, repo.updates[0][codexTailBurstExtraActive])
	require.Equal(t, int64(7), repo.updates[0][codexTailBurstExtraLastRequests])
	require.Equal(t, int64(123), repo.updates[0][codexTailBurstExtraLastTokens])
}

func TestHandleUpstreamError_OpenAIOAuth403PermissionDoesNotSettleTailBurst(t *testing.T) {
	repo := &tailBurst403AccountRepo{}
	usage := &tailBurstTestUsageRepo{stats: &usagestats.UsageStats{TotalRequests: 7, TotalTokens: 123}}
	service := NewRateLimitService(repo, usage, nil, nil, nil)
	service.SetOpenAI403CounterCache(&openAI403CounterCacheStub{counts: []int64{1}})

	account := newTailBurstAccount()
	account.Extra = codexTailBurstEntryUpdates("5h", codexTailBurstReasonUsedRatio, 98, nil, time.Now().UTC().Add(-time.Minute))

	service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"code":"permission_denied","message":"model access denied"}}`),
	)

	require.Zero(t, usage.calls, "普通权限 403 不能被当成额度结算边界")
	require.Empty(t, repo.updates)
}
