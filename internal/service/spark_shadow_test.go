package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type sparkTestAccountRepo struct {
	AccountRepository
	accounts           map[int64]*Account
	updatedAccountID   int64
	updatedCredentials map[string]any
}

func (r *sparkTestAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	account := r.accounts[id]
	if account == nil {
		return nil, errors.New("account not found")
	}
	return account, nil
}

func (r *sparkTestAccountRepo) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	r.updatedAccountID = id
	r.updatedCredentials = cloneCredentials(credentials)
	if account := r.accounts[id]; account != nil {
		account.Credentials = cloneCredentials(credentials)
	}
	return nil
}

func TestResolveShadowRuntimeAccountKeepsShadowIdentityAndUsesParentCredentials(t *testing.T) {
	parentID := int64(11)
	proxyID := int64(7)
	parent := &Account{
		ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, ProxyID: &proxyID,
		Credentials: map[string]any{"access_token": "parent-token", "refresh_token": "parent-refresh"},
	}
	shadow := &Account{
		ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark,
		Credentials: map[string]any{"model_mapping": defaultSparkShadowModelMapping()},
	}
	repo := &sparkTestAccountRepo{accounts: map[int64]*Account{parentID: parent, shadow.ID: shadow}}

	runtime, resolvedParent, err := resolveShadowRuntimeAccount(context.Background(), repo, shadow)
	require.NoError(t, err)
	require.Same(t, parent, resolvedParent)
	require.Equal(t, shadow.ID, runtime.ID, "并发、日志和计费必须仍归影子 ID")
	require.Equal(t, &proxyID, runtime.ProxyID)
	require.Equal(t, "parent-token", runtime.GetOpenAIAccessToken())
	require.Equal(t, "parent-refresh", runtime.GetOpenAIRefreshToken())
	require.NotEmpty(t, runtime.Credentials["model_mapping"])
	require.Empty(t, shadow.GetOpenAIAccessToken(), "持久化影子本身不应持有 Token")
}

func TestResolveCredentialAccountRejectsNestedShadow(t *testing.T) {
	rootID := int64(1)
	parentID := int64(2)
	parent := &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &rootID}
	shadow := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}
	repo := &sparkTestAccountRepo{accounts: map[int64]*Account{parentID: parent}}

	_, err := resolveCredentialAccount(context.Background(), repo, shadow)
	require.ErrorContains(t, err, "itself a shadow")
}

func TestSparkShadowAgentIdentityTaskPersistsOnlyOnParent(t *testing.T) {
	key, privateKey := newTestAgentIdentityKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"task_id":"spark-parent-task"}`))
	}))
	defer server.Close()
	oldBase := openAIAgentIdentityAuthAPIBaseURL
	openAIAgentIdentityAuthAPIBaseURL = server.URL
	t.Cleanup(func() { openAIAgentIdentityAuthAPIBaseURL = oldBase })

	parentID := int64(31)
	parent := &Account{
		ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{
			"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": key.runtimeID,
			"agent_private_key": privateKey,
		},
	}
	shadow := &Account{
		ID: 32, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark,
		Credentials: map[string]any{"model_mapping": defaultSparkShadowModelMapping()},
	}
	repo := &sparkTestAccountRepo{accounts: map[int64]*Account{parentID: parent, shadow.ID: shadow}}
	runtime, _, err := resolveShadowRuntimeAccount(context.Background(), repo, shadow)
	require.NoError(t, err)

	require.NoError(t, ensureAgentIdentityTaskForAccount(context.Background(), repo, nil, &sync.Mutex{}, runtime, ""))
	require.Equal(t, parentID, repo.updatedAccountID)
	require.Equal(t, "spark-parent-task", repo.updatedCredentials["task_id"])
	require.Equal(t, "spark-parent-task", runtime.GetCredential("task_id"))
	require.NotContains(t, shadow.Credentials, "task_id")
	require.NotContains(t, runtime.Credentials, "task_id")
}

func TestSparkShadowParentHealthSeparatesCredentialHealthFromGlobalQuota(t *testing.T) {
	parentID := int64(20)
	shadow := &Account{ID: 21, ParentAccountID: &parentID, QuotaDimension: QuotaDimensionSpark}
	now := time.Now()
	globalLimit := now.Add(time.Hour)
	overload := now.Add(time.Hour)
	parent := &Account{
		ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		RateLimitResetAt: &globalLimit, OverloadUntil: &overload,
	}
	lookup := func(int64) *Account { return parent }

	require.True(t, parentHealthyForShadow(shadow, lookup), "global 429/overload 不应连坐 Spark 独立额度")
	parent.Status = StatusDisabled
	require.False(t, parentHealthyForShadow(shadow, lookup))
	parent.Status = StatusActive
	credentialCooldown := now.Add(time.Minute)
	parent.TempUnschedulableUntil = &credentialCooldown
	require.False(t, parentHealthyForShadow(shadow, lookup), "401/传输冷却代表共享凭据不可用")
}

func TestSparkShadowCredentialsOnlyKeepModelMapping(t *testing.T) {
	mapping := defaultSparkShadowModelMapping()
	require.NotEmpty(t, mapping)
	for model, target := range mapping {
		require.Equal(t, model, target)
		require.Contains(t, model, "spark")
	}

	sanitized := sanitizeSparkShadowCredentials(map[string]any{
		"model_mapping": mapping,
		"access_token":  "must-not-copy",
		"refresh_token": "must-not-copy",
	})
	require.Equal(t, map[string]any{"model_mapping": mapping}, sanitized)
	require.True(t, isAllowedSparkShadowCredentialsUpdate(map[string]any{"model_mapping": mapping}))
	require.False(t, isAllowedSparkShadowCredentialsUpdate(map[string]any{"access_token": "x"}))
	require.False(t, isAllowedSparkShadowCredentialsUpdate(map[string]any{
		"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-sol"},
	}), "非 Spark 模型不能借用 Spark 独立配额")
	require.Equal(t, map[string]any{"model_mapping": mapping}, sanitizeSparkShadowCredentials(nil),
		"空映射必须恢复 Spark 安全默认值，不能回退到 OpenAI 全模型")
}

func TestBuildCodexSparkWindowExtraUpdatesOnlyUsesBengalfox(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	usage := &OpenAIQuotaUsage{AdditionalRateLimits: []OpenAIAdditionalRateLimit{
		{
			MeteredFeature: "other_feature",
			RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent: 99, LimitWindowSeconds: 3600, ResetAfterSeconds: 1,
			}},
		},
		{
			MeteredFeature: "codex_bengalfox",
			RateLimit: &OpenAIRateLimit{
				PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 12.5, LimitWindowSeconds: 5 * 60 * 60, ResetAfterSeconds: 120},
				SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 34.5, LimitWindowSeconds: 7 * 24 * 60 * 60, ResetAfterSeconds: 240},
			},
		},
	}}

	updates := buildCodexSparkWindowExtraUpdates(usage, now)
	require.Equal(t, 12.5, updates["codex_5h_used_percent"])
	require.Equal(t, 34.5, updates["codex_7d_used_percent"])
	require.Equal(t, 120, updates["codex_5h_reset_after_seconds"])
	require.Equal(t, 240, updates["codex_7d_reset_after_seconds"])
	require.Equal(t, now.Format(time.RFC3339), updates["codex_usage_updated_at"])
}
