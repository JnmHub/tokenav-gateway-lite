package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	refreshTokenKeyPrefix   = "refresh_token:"
	userRefreshTokensPrefix = "user_refresh_tokens:"
	tokenFamilyPrefix       = "token_family:"
	webSessionPrefix        = "web_session:"
	userWebSessionsPrefix   = "user_web_sessions:"
)

// refreshTokenKey generates the Redis key for a refresh token.
func refreshTokenKey(tokenHash string) string {
	return refreshTokenKeyPrefix + tokenHash
}

// userRefreshTokensKey generates the Redis key for user's token set.
func userRefreshTokensKey(userID int64) string {
	return fmt.Sprintf("%s%d", userRefreshTokensPrefix, userID)
}

// tokenFamilyKey generates the Redis key for token family set.
func tokenFamilyKey(familyID string) string {
	return tokenFamilyPrefix + familyID
}

func webSessionKey(sessionID string) string {
	return webSessionPrefix + sessionID
}

func userWebSessionsKey(userID int64) string {
	return fmt.Sprintf("%s%d", userWebSessionsPrefix, userID)
}

type refreshTokenCache struct {
	rdb *redis.Client
}

// NewRefreshTokenCache creates a new RefreshTokenCache implementation.
func NewRefreshTokenCache(rdb *redis.Client) service.RefreshTokenCache {
	return &refreshTokenCache{rdb: rdb}
}

func (c *refreshTokenCache) StoreRefreshToken(ctx context.Context, tokenHash string, data *service.RefreshTokenData, ttl time.Duration) error {
	key := refreshTokenKey(tokenHash)
	val, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal refresh token data: %w", err)
	}
	return c.rdb.Set(ctx, key, val, ttl).Err()
}

func (c *refreshTokenCache) GetRefreshToken(ctx context.Context, tokenHash string) (*service.RefreshTokenData, error) {
	key := refreshTokenKey(tokenHash)
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrRefreshTokenNotFound
		}
		return nil, err
	}
	var data service.RefreshTokenData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, fmt.Errorf("unmarshal refresh token data: %w", err)
	}
	return &data, nil
}

func (c *refreshTokenCache) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	key := refreshTokenKey(tokenHash)
	return c.rdb.Del(ctx, key).Err()
}

func (c *refreshTokenCache) DeleteUserRefreshTokens(ctx context.Context, userID int64) error {
	// Get all token hashes for this user
	tokenHashes, err := c.GetUserTokenHashes(ctx, userID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get user token hashes: %w", err)
	}

	// Refresh token 与服务端 Web session 必须统一撤销；即使 token set 为空也继续清理 session set。
	if len(tokenHashes) == 0 {
		return c.DeleteUserWebSessions(ctx, userID)
	}

	// Build keys to delete
	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
	}
	keys = append(keys, userRefreshTokensKey(userID))

	// Delete all keys in a pipeline
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}
	return c.DeleteUserWebSessions(ctx, userID)
}

func (c *refreshTokenCache) DeleteTokenFamily(ctx context.Context, familyID string) error {
	// Get all token hashes in this family
	tokenHashes, err := c.GetFamilyTokenHashes(ctx, familyID)
	if err != nil && err != redis.Nil {
		return fmt.Errorf("get family token hashes: %w", err)
	}

	// 先删除服务端 Web session，并同步移除 user_web_sessions 索引。
	// 即使后续 token key 清理暂时失败，访问令牌也会因 session 缺失立即失效，保持 fail-closed。
	if err := c.DeleteWebSession(ctx, familyID); err != nil {
		return fmt.Errorf("delete web session: %w", err)
	}
	if len(tokenHashes) == 0 {
		return nil
	}

	// Build keys to delete
	keys := make([]string, 0, len(tokenHashes)+1)
	for _, hash := range tokenHashes {
		keys = append(keys, refreshTokenKey(hash))
	}
	keys = append(keys, tokenFamilyKey(familyID))

	// Delete all keys in a pipeline
	pipe := c.rdb.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) AddToUserTokenSet(ctx context.Context, userID int64, tokenHash string, ttl time.Duration) error {
	key := userRefreshTokensKey(userID)
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) AddToFamilyTokenSet(ctx context.Context, familyID string, tokenHash string, ttl time.Duration) error {
	key := tokenFamilyKey(familyID)
	pipe := c.rdb.Pipeline()
	pipe.SAdd(ctx, key, tokenHash)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) GetUserTokenHashes(ctx context.Context, userID int64) ([]string, error) {
	key := userRefreshTokensKey(userID)
	return c.rdb.SMembers(ctx, key).Result()
}

func (c *refreshTokenCache) GetFamilyTokenHashes(ctx context.Context, familyID string) ([]string, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SMembers(ctx, key).Result()
}

func (c *refreshTokenCache) IsTokenInFamily(ctx context.Context, familyID string, tokenHash string) (bool, error) {
	key := tokenFamilyKey(familyID)
	return c.rdb.SIsMember(ctx, key, tokenHash).Result()
}

// UpsertWebSession stores normalized browser-session metadata and indexes it by user.
func (c *refreshTokenCache) UpsertWebSession(ctx context.Context, session *service.WebSession, ttl time.Duration) error {
	if session == nil || session.SessionID == "" || session.UserID <= 0 {
		return fmt.Errorf("invalid web session")
	}
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal web session: %w", err)
	}
	pipe := c.rdb.TxPipeline()
	pipe.Set(ctx, webSessionKey(session.SessionID), data, ttl)
	pipe.SAdd(ctx, userWebSessionsKey(session.UserID), session.SessionID)
	pipe.Expire(ctx, userWebSessionsKey(session.UserID), ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) GetWebSession(ctx context.Context, sessionID string) (*service.WebSession, error) {
	data, err := c.rdb.Get(ctx, webSessionKey(sessionID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, service.ErrSessionRevoked
		}
		return nil, err
	}
	var session service.WebSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("unmarshal web session: %w", err)
	}
	return &session, nil
}

func (c *refreshTokenCache) TouchWebSession(ctx context.Context, sessionID string, seenAt time.Time, ttl time.Duration) error {
	session, err := c.GetWebSession(ctx, sessionID)
	if err != nil {
		return err
	}
	session.LastSeenAt = seenAt.UTC()
	if ttl > 0 {
		session.ExpiresAt = seenAt.UTC().Add(ttl)
	}
	return c.UpsertWebSession(ctx, session, ttl)
}

func (c *refreshTokenCache) MarkWebSessionVerified(ctx context.Context, sessionID string, verifiedAt time.Time) error {
	session, err := c.GetWebSession(ctx, sessionID)
	if err != nil {
		return err
	}
	t := verifiedAt.UTC()
	session.LastVerifiedAt = &t
	ttl, err := c.rdb.TTL(ctx, webSessionKey(sessionID)).Result()
	if err != nil {
		return err
	}
	if ttl <= 0 {
		ttl = time.Until(session.ExpiresAt)
	}
	if ttl <= 0 {
		return service.ErrSessionRevoked
	}
	return c.UpsertWebSession(ctx, session, ttl)
}

func (c *refreshTokenCache) DeleteWebSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	session, err := c.GetWebSession(ctx, sessionID)
	if err != nil && err != service.ErrSessionRevoked {
		return err
	}
	pipe := c.rdb.TxPipeline()
	pipe.Del(ctx, webSessionKey(sessionID))
	if session != nil && session.UserID > 0 {
		pipe.SRem(ctx, userWebSessionsKey(session.UserID), sessionID)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (c *refreshTokenCache) DeleteUserWebSessions(ctx context.Context, userID int64) error {
	key := userWebSessionsKey(userID)
	sessionIDs, err := c.rdb.SMembers(ctx, key).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	pipe := c.rdb.TxPipeline()
	for _, sessionID := range sessionIDs {
		pipe.Del(ctx, webSessionKey(sessionID))
	}
	pipe.Del(ctx, key)
	_, err = pipe.Exec(ctx)
	return err
}
