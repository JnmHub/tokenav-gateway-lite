//go:build unit

package repository

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildSchedulerMetadataAccount_KeepsOpenAIWSFlags(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"openai_oauth_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			"openai_ws_force_http":                         true,
			"openai_responses_mode":                        "force_chat_completions",
			"openai_responses_supported":                   false,
			"mixed_scheduling":                             true,
			"unused_large_field":                           "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, true, got.Extra["openai_oauth_responses_websockets_v2_enabled"])
	require.Equal(t, service.OpenAIWSIngressModePassthrough, got.Extra["openai_oauth_responses_websockets_v2_mode"])
	require.Equal(t, true, got.Extra["openai_ws_force_http"])
	require.Equal(t, "force_chat_completions", got.Extra["openai_responses_mode"])
	require.Equal(t, false, got.Extra["openai_responses_supported"])
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Nil(t, got.Extra["unused_large_field"])
}

func TestBuildSchedulerMetadataAccount_KeepsKeywordPolicyMode(t *testing.T) {
	account := service.Account{
		ID:       45,
		Platform: service.PlatformOpenAI,
		Extra: map[string]any{
			service.KeywordPolicyModeExtraKey: service.KeywordPolicyModeAllow,
			"unused_large_field":              "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, service.KeywordPolicyModeAllow, got.KeywordPolicyMode())
	require.Nil(t, got.Extra["unused_large_field"])
}

func TestBuildSchedulerMetadataAccount_KeepsGrokMediaEligibilityEvidence(t *testing.T) {
	account := service.Account{
		ID:       44,
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			service.GrokMediaEligibleExtraKey: false,
			service.GrokMediaEligibilitySnapshotExtraKey: map[string]any{
				"eligible":           false,
				"weekly_status_code": 403,
			},
			"grok_billing_snapshot": map[string]any{
				"status_code":         200,
				"weekly_status_code":  403,
				"monthly_status_code": 200,
			},
			"unused_large_field": "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)
	eligible, reason := got.GrokMediaGenerationEligibility()
	require.False(t, eligible)
	require.Equal(t, "override_disabled", reason)
	require.Equal(t, false, got.Extra[service.GrokMediaEligibleExtraKey])
	require.NotNil(t, got.Extra[service.GrokMediaEligibilitySnapshotExtraKey])
	require.NotNil(t, got.Extra["grok_billing_snapshot"])
	require.Nil(t, got.Extra["unused_large_field"])
}

func TestBuildSchedulerMetadataAccount_KeepsAPIKeyBaseURL(t *testing.T) {
	account := service.Account{
		ID:       43,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":          "sk-local",
			"base_url":         "http://127.0.0.1:18099",
			"large_debug_blob": "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, "sk-local", got.Credentials["api_key"])
	require.Equal(t, "http://127.0.0.1:18099", got.Credentials["base_url"])
	require.Nil(t, got.Credentials["large_debug_blob"])
}

func TestBuildSchedulerMetadataAccount_KeepsSlimGroupMembership(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformAnthropic,
		GroupIDs: []int64{7, 9, 7, 0},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 42,
				GroupID:   7,
				Priority:  2,
				Account:   &service.Account{ID: 42, Name: "drop-from-metadata"},
				Group:     &service.Group{ID: 7, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   11,
				Priority:  3,
				Group:     &service.Group{ID: 11, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   0,
				Priority:  4,
			},
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, []int64{7, 9, 11}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 2)
	require.Equal(t, int64(42), got.AccountGroups[0].AccountID)
	require.Equal(t, int64(7), got.AccountGroups[0].GroupID)
	require.Equal(t, 2, got.AccountGroups[0].Priority)
	require.Nil(t, got.AccountGroups[0].Account)
	require.Nil(t, got.AccountGroups[0].Group)
	require.Equal(t, int64(11), got.AccountGroups[1].GroupID)
	require.Nil(t, got.Groups)
}

func TestBuildSchedulerMetadataAccount_KeepsQuotaAutoPauseFields(t *testing.T) {
	account := service.Account{
		ID: 88,
		Extra: map[string]any{
			"codex_5h_used_percent":        12.34,
			"codex_7d_used_percent":        56.78,
			"codex_5h_reset_at":            "2026-05-29T10:00:00Z",
			"codex_7d_reset_at":            "2026-06-01T10:00:00Z",
			"codex_5h_reset_after_seconds": 300,
			"codex_7d_reset_after_seconds": 600,
			"codex_usage_updated_at":       "2026-05-29T09:00:00Z",
			"auto_pause_5h_threshold":      0.95,
			"auto_pause_7d_threshold":      0.96,
			"auto_pause_5h_disabled":       true,
			"auto_pause_7d_disabled":       false,
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, 12.34, got.Extra["codex_5h_used_percent"])
	require.Equal(t, 56.78, got.Extra["codex_7d_used_percent"])
	require.Equal(t, "2026-05-29T10:00:00Z", got.Extra["codex_5h_reset_at"])
	require.Equal(t, "2026-06-01T10:00:00Z", got.Extra["codex_7d_reset_at"])
	require.Equal(t, 300, got.Extra["codex_5h_reset_after_seconds"])
	require.Equal(t, 600, got.Extra["codex_7d_reset_after_seconds"])
	require.Equal(t, "2026-05-29T09:00:00Z", got.Extra["codex_usage_updated_at"])
	require.Equal(t, 0.95, got.Extra["auto_pause_5h_threshold"])
	require.Equal(t, 0.96, got.Extra["auto_pause_7d_threshold"])
	require.Equal(t, true, got.Extra["auto_pause_5h_disabled"])
	require.Equal(t, false, got.Extra["auto_pause_7d_disabled"])
}

func TestBuildSchedulerMetadataAccount_KeepsModelRateLimits(t *testing.T) {
	account := service.Account{
		ID:       90,
		Platform: service.PlatformAntigravity,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				"gemini-3-flash": map[string]any{
					"rate_limit_reset_at": "2026-05-30T10:10:00Z",
				},
				"antigravity:gemini": map[string]any{
					"rate_limit_reset_at": "2026-05-30T10:10:00Z",
				},
			},
			"unused_large_field": "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	limits, ok := got.Extra["model_rate_limits"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, limits, "gemini-3-flash")
	require.Contains(t, limits, "antigravity:gemini")
	require.Nil(t, got.Extra["unused_large_field"])
}

// 调度投影必须保留 OpenAI 透传开关。
//
// 候选过滤走 ListSchedulableAccounts，读的是 buildSchedulerMetadataAccount 产出的精简投影；
// Account.IsModelSupported 又靠 extra 上的透传开关短路 model_mapping 白名单。
// 一旦投影把开关裁掉、却保留了白名单，透传账号在选号阶段就会退回白名单判定并被误判成
// model_not_supported，而转发阶段（读完整账号）仍按透传工作 —— 表现为"单独测这个账号能通、
// 走网关却报 no available accounts"。
func TestBuildSchedulerMetadataAccount_KeepsOpenAIPassthroughForModelGate(t *testing.T) {
	for _, key := range []string{"openai_passthrough", "openai_oauth_passthrough"} {
		t.Run(key, func(t *testing.T) {
			account := service.Account{
				ID:       383,
				Platform: service.PlatformOpenAI,
				Type:     service.AccountTypeOAuth,
				Credentials: map[string]any{
					// 账号从白名单模式切到透传后常见的残留映射，未列出请求的模型。
					"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
					"access_token":  "drop-me",
				},
				Extra: map[string]any{key: true},
			}
			require.True(t, account.IsModelSupported("gpt-5.6-sol"),
				"前置条件：透传账号本应放行白名单外的模型")

			meta := buildSchedulerMetadataAccount(account)

			// 走一遍真实的序列化/反序列化路径（写入 sched:meta 再由 decodeCachedAccount 读回）。
			payload, err := json.Marshal(meta)
			require.NoError(t, err)
			var restored service.Account
			require.NoError(t, json.Unmarshal(payload, &restored))

			require.Equal(t, true, restored.Extra[key])
			require.True(t, restored.IsOpenAIPassthroughEnabled())
			require.True(t, restored.IsModelSupported("gpt-5.6-sol"),
				"投影裁掉透传开关会让透传账号在候选过滤阶段被误判为 model_not_supported")
			// 白名单本身仍需保留：非透传账号依赖它做模型门。
			require.Equal(t, map[string]any{"gpt-5.5": "gpt-5.5"}, restored.Credentials["model_mapping"])
		})
	}
}
