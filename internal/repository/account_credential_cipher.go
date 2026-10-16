package repository

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	securitySecretKeyAccountCredentials = "account_credentials_aes_key"
	accountCredentialCipherPrefix       = "enc:tokenav:v1:"
	transitionalCredentialCipherPrefix  = "enc:tokeav:v1:"
	legacyAccountCredentialCipherPrefix = "enc:aiceo:v1:"
	accountCredentialMigrationPageSize  = 200
	accountCredentialMigrationTimeout   = 10 * time.Minute
)

var accountSensitiveCredentialKeys = func() map[string]struct{} {
	keys := make(map[string]struct{}, len(service.SensitiveCredentialKeys))
	for _, key := range service.SensitiveCredentialKeys {
		keys[key] = struct{}{}
	}
	return keys
}()

// AccountCredentialCipher encrypts only authentication secrets inside the
// credentials JSON. Routing controls such as base_url and model_mapping stay
// queryable, while tokens are never persisted in plaintext.
type AccountCredentialCipher struct {
	aead cipher.AEAD
}

func NewAccountCredentialCipher(client *dbent.Client) (*AccountCredentialCipher, error) {
	if client == nil {
		return nil, fmt.Errorf("nil ent client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountCredentialMigrationTimeout)
	defer cancel()

	encodedKey, _, err := getOrCreateGeneratedSecuritySecret(ctx, client, securitySecretKeyAccountCredentials, 32)
	if err != nil {
		return nil, fmt.Errorf("ensure account credential encryption key: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid persisted account credential encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create account credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create account credential gcm: %w", err)
	}
	result := &AccountCredentialCipher{aead: aead}
	if err := result.migrateLegacyPlaintext(ctx, client); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *AccountCredentialCipher) EncryptMap(credentials map[string]any) (map[string]any, bool, error) {
	out := copyJSONMap(credentials)
	if out == nil {
		out = map[string]any{}
	}
	changed := false
	for key := range accountSensitiveCredentialKeys {
		value, ok := out[key].(string)
		if !ok || strings.TrimSpace(value) == "" || strings.HasPrefix(value, accountCredentialCipherPrefix) {
			continue
		}
		if strings.HasPrefix(value, transitionalCredentialCipherPrefix) || strings.HasPrefix(value, legacyAccountCredentialCipherPrefix) {
			plain, err := c.decrypt(value)
			if err != nil {
				return nil, false, fmt.Errorf("migrate legacy account credential %s: %w", key, err)
			}
			value = plain
		}
		encrypted, err := c.encrypt(value)
		if err != nil {
			return nil, false, fmt.Errorf("encrypt account credential %s: %w", key, err)
		}
		out[key] = encrypted
		changed = true
	}
	return out, changed, nil
}

func (c *AccountCredentialCipher) DecryptMap(credentials map[string]any) (map[string]any, error) {
	out := copyJSONMap(credentials)
	if out == nil {
		return nil, nil
	}
	for key := range accountSensitiveCredentialKeys {
		value, ok := out[key].(string)
		if !ok || (!strings.HasPrefix(value, accountCredentialCipherPrefix) && !strings.HasPrefix(value, transitionalCredentialCipherPrefix) && !strings.HasPrefix(value, legacyAccountCredentialCipherPrefix)) {
			continue
		}
		decrypted, err := c.decrypt(value)
		if err != nil {
			// Fail closed: forwarding ciphertext as an API token would create noisy
			// unauthorized requests and could leak the storage representation.
			return nil, fmt.Errorf("decrypt account credential %s: %w", key, err)
		}
		out[key] = decrypted
	}
	return out, nil
}

func (c *AccountCredentialCipher) encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, ciphertext...)
	return accountCredentialCipherPrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

func (c *AccountCredentialCipher) decrypt(stored string) (string, error) {
	var encoded string
	switch {
	case strings.HasPrefix(stored, accountCredentialCipherPrefix):
		encoded = strings.TrimPrefix(stored, accountCredentialCipherPrefix)
	case strings.HasPrefix(stored, transitionalCredentialCipherPrefix):
		encoded = strings.TrimPrefix(stored, transitionalCredentialCipherPrefix)
	case strings.HasPrefix(stored, legacyAccountCredentialCipherPrefix):
		encoded = strings.TrimPrefix(stored, legacyAccountCredentialCipherPrefix)
	default:
		return "", fmt.Errorf("unsupported account credential cipher prefix")
	}
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(payload) < c.aead.NonceSize() {
		return "", fmt.Errorf("ciphertext is too short")
	}
	nonce, ciphertext := payload[:c.aead.NonceSize()], payload[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (c *AccountCredentialCipher) migrateLegacyPlaintext(ctx context.Context, client *dbent.Client) error {
	var cursor int64
	var migrated int
	for {
		// 历史软删除账号仍可能包含可用令牌；迁移必须覆盖它们，不能让回收站
		// 成为明文凭据的长期存放点。
		accounts, err := client.Account.Query().
			Where(dbaccount.IDGT(cursor)).
			Order(dbaccount.ByID()).
			Limit(accountCredentialMigrationPageSize).
			All(mixins.SkipSoftDelete(ctx))
		if err != nil {
			return fmt.Errorf("list legacy account credentials: %w", err)
		}
		if len(accounts) == 0 {
			break
		}
		for _, account := range accounts {
			cursor = account.ID
			stored, changed, err := c.EncryptMap(account.Credentials)
			if err != nil {
				return fmt.Errorf("migrate account %d credentials: %w", account.ID, err)
			}
			if !changed {
				continue
			}
			if _, err := client.Account.UpdateOneID(account.ID).SetCredentials(stored).Save(ctx); err != nil {
				return fmt.Errorf("persist encrypted account %d credentials: %w", account.ID, err)
			}
			migrated++
		}
		if len(accounts) < accountCredentialMigrationPageSize {
			break
		}
	}
	if migrated > 0 {
		logger.LegacyPrintf("repository.account", "encrypted legacy account credentials: count=%d", migrated)
	}
	return nil
}
