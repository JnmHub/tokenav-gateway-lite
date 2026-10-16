//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type updateAccountCredsRepoStub struct {
	mockAccountRepoForGemini
	account     *Account
	updateCalls int
}

type createAccountCredsRepoStub struct {
	updateAccountCredsRepoStub
	created *Account
}

func (r *createAccountCredsRepoStub) Create(_ context.Context, account *Account) error {
	r.created = account
	return nil
}

func (r *updateAccountCredsRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *updateAccountCredsRepoStub) Update(ctx context.Context, account *Account) error {
	r.updateCalls++
	r.account = account
	return nil
}

func TestUpdateAccount_PreservesSensitiveCredsWhenIncomingOmits(t *testing.T) {
	accountID := int64(202)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
				"access_token":  "at-existing",
				"id_token":      "id-existing",
				"base_url":      "https://old.example.com",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	// 模拟前端编辑：仅修改 base_url，没有传 token（脱敏后前端 spread 拿不到敏感键）
	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"base_url": "https://new.example.com",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)

	// 敏感键应保留
	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"])
	require.Equal(t, "at-existing", repo.account.Credentials["access_token"])
	require.Equal(t, "id-existing", repo.account.Credentials["id_token"])
	// 非敏感键被替换
	require.Equal(t, "https://new.example.com", repo.account.Credentials["base_url"])
}

func TestUpdateAccount_ExplicitNewTokenOverwrites(t *testing.T) {
	accountID := int64(203)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-old",
				"api_key":       "sk-old",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"refresh_token": "rt-new",
			// api_key 没传 → 应保留旧值
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)

	require.Equal(t, "rt-new", repo.account.Credentials["refresh_token"])
	require.Equal(t, "sk-old", repo.account.Credentials["api_key"])
}

func TestUpdateAccount_EmptyCredentialsSkipsUpdate(t *testing.T) {
	accountID := int64(204)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{}, // len == 0 → 闸门跳过
		Name:        "renamed",
	})
	require.NoError(t, err)

	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"], "空 credentials 不应触碰已有 token")
	require.Equal(t, "renamed", repo.account.Name)
}

func TestUpdateAccount_AntigravityEditPreservesManualRefreshToken(t *testing.T) {
	accountID := int64(205)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "manual-antigravity-rt",
				"access_token":  "old-access-token",
				"project_id":    "project-old",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"project_id": "project-new",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "manual-antigravity-rt", repo.account.Credentials["refresh_token"])
	require.Equal(t, "old-access-token", repo.account.Credentials["access_token"])
	require.Equal(t, "project-new", repo.account.Credentials["project_id"])
}

func TestUpdateAccount_GrokOAuthCredentialsAdvanceServerTokenVersion(t *testing.T) {
	accountID := int64(206)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token":  "refresh-old",
				"_token_version": int64(100),
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"refresh_token":  "refresh-admin",
			"_token_version": int64(1), // 内部版本不能由客户端回退或伪造。
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "refresh-admin", repo.account.GetGrokRefreshToken())
	require.Greater(t, repo.account.GetCredentialAsInt64("_token_version"), int64(100))
}

func TestUpdateAccount_GrokOAuthNonCredentialEditKeepsTokenVersion(t *testing.T) {
	accountID := int64(207)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Name:     "before",
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token":  "refresh-existing",
				"_token_version": int64(101),
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{Name: "after"})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "after", repo.account.Name)
	require.Equal(t, int64(101), repo.account.GetCredentialAsInt64("_token_version"))
}

func TestUpdateAccount_GrokOAuthPreservesRoutingAndHeaderOverrides(t *testing.T) {
	accountID := int64(208)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "refresh-existing",
				"base_url":      "https://cli-chat-proxy.grok.com/v1",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"base_url":                "https://us-west-2.api.x.ai/v1/",
			"header_override_enabled": true,
			HeaderOverridesCredentialKey: map[string]any{
				"X-Tenant": " tenant-a ",
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "refresh-existing", repo.account.GetGrokRefreshToken())
	require.Equal(t, "https://us-west-2.api.x.ai/v1", repo.account.Credentials["base_url"])
	require.Equal(t, true, repo.account.Credentials["header_override_enabled"])
	require.Equal(t, map[string]any{"x-tenant": "tenant-a"}, repo.account.Credentials[HeaderOverridesCredentialKey])
}

func TestCreateAccount_GrokOAuthPersistsRoutingAndHeaderOverrides(t *testing.T) {
	repo := &createAccountCredsRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "Grok regional OAuth",
		Platform:             PlatformGrok,
		Type:                 AccountTypeOAuth,
		SkipDefaultGroupBind: true,
		Credentials: map[string]any{
			"access_token":            "access-token",
			"refresh_token":           "refresh-token",
			"base_url":                "https://us-east-1.api.x.ai/v1/",
			"header_override_enabled": true,
			HeaderOverridesCredentialKey: map[string]any{
				"X-Tenant": " tenant-a ",
			},
		},
	})

	require.NoError(t, err)
	require.Same(t, repo.created, created)
	require.Equal(t, "https://us-east-1.api.x.ai/v1", created.Credentials["base_url"])
	require.Equal(t, true, created.Credentials["header_override_enabled"])
	require.Equal(t, map[string]any{"x-tenant": "tenant-a"}, created.Credentials[HeaderOverridesCredentialKey])
}
