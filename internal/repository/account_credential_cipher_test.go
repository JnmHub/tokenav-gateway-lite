package repository

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountCredentialCipherRoundTripUsesRandomNonce(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)

	credentials := map[string]any{
		"access_token":      "access-secret",
		"refresh_token":     "refresh-secret",
		"api_key":           "api-secret",
		"client_secret":     "client-secret",
		"setup_token":       "setup-secret",
		"agent_private_key": "agent-private-secret",
		"base_url":          "https://api.x.ai/v1",
		"model_mapping":     map[string]any{"grok": "grok-4.5"},
	}

	first, changed, err := cipher.EncryptMap(credentials)
	require.NoError(t, err)
	require.True(t, changed)
	second, changed, err := cipher.EncryptMap(credentials)
	require.NoError(t, err)
	require.True(t, changed)

	firstAccess, ok := first["access_token"].(string)
	require.True(t, ok)
	secondAccess, ok := second["access_token"].(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(firstAccess, accountCredentialCipherPrefix))
	require.True(t, strings.HasPrefix(secondAccess, accountCredentialCipherPrefix))
	require.NotEqual(t, firstAccess, secondAccess, "每次加密必须使用独立随机 nonce，避免相同令牌产生相同密文")
	require.NotContains(t, first["agent_private_key"], "agent-private-secret")
	require.Equal(t, "access-secret", credentials["access_token"], "加密不得原地修改调用方数据")
	require.Equal(t, credentials["base_url"], first["base_url"])
	require.Equal(t, credentials["model_mapping"], first["model_mapping"])

	decrypted, err := cipher.DecryptMap(first)
	require.NoError(t, err)
	require.Equal(t, credentials, decrypted)
}

func TestAccountCredentialCipherRejectsTamperedCiphertext(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)

	stored, _, err := cipher.EncryptMap(map[string]any{"access_token": "access-secret"})
	require.NoError(t, err)
	encrypted := stored["access_token"].(string)
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encrypted, accountCredentialCipherPrefix))
	require.NoError(t, err)
	require.NotEmpty(t, payload)
	payload[len(payload)-1] ^= 0xff
	stored["access_token"] = accountCredentialCipherPrefix + base64.RawStdEncoding.EncodeToString(payload)

	_, err = cipher.DecryptMap(stored)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decrypt account credential access_token")
}

func TestAccountCredentialCipherMigratesLegacyBrandPrefix(t *testing.T) {
	client := newSecuritySecretTestClient(t)
	cipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)
	encrypted, err := cipher.encrypt("legacy-secret")
	require.NoError(t, err)
	payload := strings.TrimPrefix(encrypted, accountCredentialCipherPrefix)
	for _, oldPrefix := range []string{transitionalCredentialCipherPrefix, legacyAccountCredentialCipherPrefix} {
		t.Run(oldPrefix, func(t *testing.T) {
			legacy := oldPrefix + payload
			updated, changed, err := cipher.EncryptMap(map[string]any{"access_token": legacy})
			require.NoError(t, err)
			require.True(t, changed)
			stored := updated["access_token"].(string)
			require.True(t, strings.HasPrefix(stored, accountCredentialCipherPrefix))
			require.False(t, strings.HasPrefix(stored, oldPrefix))

			plain, err := cipher.DecryptMap(updated)
			require.NoError(t, err)
			require.Equal(t, "legacy-secret", plain["access_token"])
		})
	}
}

func TestAccountCredentialCipherMigratesLegacyPlaintextAndRepositoryDecrypts(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	legacy, err := client.Account.Create().
		SetName("legacy-grok").
		SetPlatform(service.PlatformGrok).
		SetType(service.AccountTypeOAuth).
		SetCredentials(map[string]any{
			"access_token":  "legacy-access",
			"refresh_token": "legacy-refresh",
			"base_url":      "https://api.x.ai/v1",
		}).
		Save(ctx)
	require.NoError(t, err)

	cipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)

	raw, err := client.Account.Query().Where(dbaccount.IDEQ(legacy.ID)).Only(ctx)
	require.NoError(t, err)
	require.NotEqual(t, "legacy-access", raw.Credentials["access_token"])
	require.NotEqual(t, "legacy-refresh", raw.Credentials["refresh_token"])
	require.True(t, strings.HasPrefix(raw.Credentials["access_token"].(string), accountCredentialCipherPrefix))
	require.True(t, strings.HasPrefix(raw.Credentials["refresh_token"].(string), accountCredentialCipherPrefix))
	require.Equal(t, "https://api.x.ai/v1", raw.Credentials["base_url"])

	repo := newAccountRepositoryWithSQLAndCipher(client, nil, nil, cipher)
	account, err := repo.GetByID(ctx, legacy.ID)
	require.NoError(t, err)
	require.Equal(t, "legacy-access", account.Credentials["access_token"])
	require.Equal(t, "legacy-refresh", account.Credentials["refresh_token"])
	require.Equal(t, "https://api.x.ai/v1", account.Credentials["base_url"])
}

func TestAccountCredentialCipherDoesNotEncryptStoredCiphertextAgain(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	cipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)

	credentials, _, err := cipher.EncryptMap(map[string]any{"api_key": "grok-key"})
	require.NoError(t, err)
	before := credentials["api_key"].(string)
	account, err := client.Account.Create().
		SetName("encrypted-grok").
		SetPlatform(service.PlatformGrok).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(credentials).
		Save(ctx)
	require.NoError(t, err)

	secondCipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)
	raw, err := client.Account.Query().Where(dbaccount.IDEQ(account.ID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, before, raw.Credentials["api_key"])

	decrypted, err := secondCipher.DecryptMap(raw.Credentials)
	require.NoError(t, err)
	require.Equal(t, "grok-key", decrypted["api_key"])
}

func TestAccountCredentialCipherMigratesSoftDeletedAccounts(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	deletedAt := time.Now().UTC()
	account, err := client.Account.Create().
		SetName("deleted-grok").
		SetPlatform(service.PlatformGrok).
		SetType(service.AccountTypeOAuth).
		SetCredentials(map[string]any{"refresh_token": "deleted-refresh"}).
		SetDeletedAt(deletedAt).
		Save(ctx)
	require.NoError(t, err)

	_, err = NewAccountCredentialCipher(client)
	require.NoError(t, err)

	raw, err := client.Account.Query().
		Where(dbaccount.IDEQ(account.ID)).
		Only(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(raw.Credentials["refresh_token"].(string), accountCredentialCipherPrefix))
}

func TestAccountRepositoryEncryptsNewCredentialsAtRest(t *testing.T) {
	ctx := context.Background()
	client := newSecuritySecretTestClient(t)
	cipher, err := NewAccountCredentialCipher(client)
	require.NoError(t, err)
	repo := newAccountRepositoryWithSQLAndCipher(client, nil, nil, cipher)

	account := &service.Account{
		Name:     "new-grok",
		Platform: service.PlatformGrok,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"access_token": "new-access",
			"id_token":     "new-id-token",
			"base_url":     "https://api.x.ai/v1",
		},
	}
	require.NoError(t, repo.Create(ctx, account))

	raw, err := client.Account.Query().Where(dbaccount.IDEQ(account.ID)).Only(ctx)
	require.NoError(t, err)
	require.NotContains(t, raw.Credentials["access_token"].(string), "new-access")
	require.NotContains(t, raw.Credentials["id_token"].(string), "new-id-token")
	require.Equal(t, "https://api.x.ai/v1", raw.Credentials["base_url"])

	loaded, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "new-access", loaded.Credentials["access_token"])
	require.Equal(t, "new-id-token", loaded.Credentials["id_token"])
}
