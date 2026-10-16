//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRefreshTokenCacheDeleteFamilyRemovesWebSessionIndex(t *testing.T) {
	ctx := context.Background()
	cache := NewRefreshTokenCache(integrationRedis).(*refreshTokenCache)
	suffix := time.Now().UnixNano()
	familyID := fmt.Sprintf("test-family-%d", suffix)
	tokenHash := fmt.Sprintf("test-token-%d", suffix)
	userID := suffix
	ttl := time.Minute
	t.Cleanup(func() {
		_ = integrationRedis.Del(ctx,
			refreshTokenKey(tokenHash), tokenFamilyKey(familyID), webSessionKey(familyID), userWebSessionsKey(userID),
		).Err()
	})

	require.NoError(t, cache.StoreRefreshToken(ctx, tokenHash, &service.RefreshTokenData{
		UserID: userID, FamilyID: familyID, ExpiresAt: time.Now().Add(ttl),
	}, ttl))
	require.NoError(t, cache.AddToFamilyTokenSet(ctx, familyID, tokenHash, ttl))
	require.NoError(t, cache.UpsertWebSession(ctx, &service.WebSession{
		SessionID: familyID, UserID: userID, BindingHash: "binding", CreatedAt: time.Now(), LastSeenAt: time.Now(), ExpiresAt: time.Now().Add(ttl),
	}, ttl))
	require.True(t, integrationRedis.SIsMember(ctx, userWebSessionsKey(userID), familyID).Val())

	require.NoError(t, cache.DeleteTokenFamily(ctx, familyID))
	require.False(t, integrationRedis.SIsMember(ctx, userWebSessionsKey(userID), familyID).Val())
	require.Zero(t, integrationRedis.Exists(ctx, webSessionKey(familyID), tokenFamilyKey(familyID), refreshTokenKey(tokenHash)).Val())
}
