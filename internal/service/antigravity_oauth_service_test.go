package service

import (
	"context"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAntigravityOAuthGenerateAuthURLConfig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		clientID   string
		secret     string
		missingEnv string
	}{
		{"missing_client_id", "", "invalid-antigravity-client-secret", antigravity.AntigravityOAuthClientIDEnv},
		{"missing_secret", "invalid-antigravity-client-id", "", antigravity.AntigravityOAuthClientSecretEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(antigravity.AntigravityOAuthClientIDEnv, tc.clientID)
			t.Setenv(antigravity.AntigravityOAuthClientSecretEnv, tc.secret)
			// 空会话仓库验证缺配置路径不会尝试存储无效会话。
			svc := &AntigravityOAuthService{}
			result, err := svc.GenerateAuthURL(context.Background(), nil)
			require.Nil(t, result)
			require.ErrorContains(t, err, tc.missingEnv)
		})
	}

	t.Run("configured", func(t *testing.T) {
		t.Setenv(antigravity.AntigravityOAuthClientIDEnv, "invalid-antigravity-client-id")
		t.Setenv(antigravity.AntigravityOAuthClientSecretEnv, "invalid-antigravity-client-secret")
		svc := NewAntigravityOAuthService(nil)
		defer svc.sessionStore.Stop()
		result, err := svc.GenerateAuthURL(context.Background(), nil)
		require.NoError(t, err)
		parsed, err := url.Parse(result.AuthURL)
		require.NoError(t, err)
		require.Equal(t, "invalid-antigravity-client-id", parsed.Query().Get("client_id"))
		require.NotContains(t, result.AuthURL, "invalid-antigravity-client-secret")
		session, ok := svc.sessionStore.Get(result.SessionID)
		require.True(t, ok)
		require.Equal(t, result.State, session.State)
		require.Equal(t, antigravity.GenerateCodeChallenge(session.CodeVerifier), parsed.Query().Get("code_challenge"))
	})
}

func TestAntigravityOAuthMissingConfigIsNotRetried(t *testing.T) {
	for _, env := range []string{antigravity.AntigravityOAuthClientIDEnv, antigravity.AntigravityOAuthClientSecretEnv} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(antigravity.AntigravityOAuthClientIDEnv, "invalid-antigravity-client-id")
			t.Setenv(antigravity.AntigravityOAuthClientSecretEnv, "invalid-antigravity-client-secret")
			t.Setenv(env, "")
			_, configErr := antigravity.BuildAuthorizationURL("state", "challenge")
			require.Error(t, configErr)
			require.True(t, isNonRetryableAntigravityOAuthError(configErr))
			svc := &AntigravityOAuthService{}
			result, err := svc.RefreshToken(context.Background(), "invalid-refresh-token", "")
			require.Nil(t, result)
			require.ErrorContains(t, err, env)
			// 未进入重试耗尽的错误包装，保留原始配置错误供接口返回。
			require.IsType(t, &infraerrors.ApplicationError{}, err)
		})
	}
}

func TestResolveDefaultTierID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		loadRaw map[string]any
		want    string
	}{
		{
			name:    "nil loadRaw",
			loadRaw: nil,
			want:    "",
		},
		{
			name: "missing allowedTiers",
			loadRaw: map[string]any{
				"paidTier": map[string]any{"id": "g1-pro-tier"},
			},
			want: "",
		},
		{
			name:    "empty allowedTiers",
			loadRaw: map[string]any{"allowedTiers": []any{}},
			want:    "",
		},
		{
			name: "tier missing id field",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"isDefault": true},
				},
			},
			want: "",
		},
		{
			name: "allowedTiers but no default",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "free-tier", "isDefault": false},
					map[string]any{"id": "standard-tier", "isDefault": false},
				},
			},
			want: "",
		},
		{
			name: "default tier found",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "free-tier", "isDefault": true},
					map[string]any{"id": "standard-tier", "isDefault": false},
				},
			},
			want: "free-tier",
		},
		{
			name: "default tier id with spaces",
			loadRaw: map[string]any{
				"allowedTiers": []any{
					map[string]any{"id": "  standard-tier  ", "isDefault": true},
				},
			},
			want: "standard-tier",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := resolveDefaultTierID(tc.loadRaw)
			if got != tc.want {
				t.Fatalf("resolveDefaultTierID() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAntigravityBuildAccountCredentialsOmitsEmptyRefreshToken(t *testing.T) {
	t.Parallel()

	svc := NewAntigravityOAuthService(nil)

	credentials := svc.BuildAccountCredentials(&AntigravityTokenInfo{
		AccessToken:  "new-access-token",
		RefreshToken: "   ",
		ExpiresAt:    1900000000,
		TokenType:    "Bearer",
	})

	require.Equal(t, "new-access-token", credentials["access_token"])
	require.NotContains(t, credentials, "refresh_token")
}

func TestAntigravityRefreshMergePreservesManualRefreshTokenWhenUpstreamOmits(t *testing.T) {
	t.Parallel()

	svc := NewAntigravityOAuthService(nil)
	existing := map[string]any{
		"access_token":  "old-access-token",
		"refresh_token": "manual-refresh-token",
		"project_id":    "project-1",
	}
	incoming := svc.BuildAccountCredentials(&AntigravityTokenInfo{
		AccessToken: "new-access-token",
		ExpiresAt:   1900000000,
		TokenType:   "Bearer",
	})

	merged := MergeCredentials(existing, incoming)

	require.Equal(t, "new-access-token", merged["access_token"])
	require.Equal(t, "manual-refresh-token", merged["refresh_token"])
	require.Equal(t, "project-1", merged["project_id"])
}
