package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheReasoningContent(t *testing.T) {
	miniRedis := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: miniRedis.Addr()})
	store, ok := NewGatewayCache(client).(service.ReasoningContentStore)
	require.True(t, ok)
	ctx := context.Background()

	_, err := store.GetReasoningContent(ctx, "item_missing")
	require.ErrorIs(t, err, service.ErrReasoningContentNotFound)

	require.NoError(t, store.SetReasoningContent(ctx, "item_abc", "think hard", time.Minute))
	content, err := store.GetReasoningContent(ctx, "item_abc")
	require.NoError(t, err)
	require.Equal(t, "think hard", content)

	require.NoError(t, store.SetReasoningContent(ctx, "item_ttl", "x", 0))
	ttl := miniRedis.TTL(reasoningContentPrefix + "item_ttl")
	require.Greater(t, ttl, 6*24*time.Hour)
	require.LessOrEqual(t, ttl, reasoningContentDefaultTTL)

	require.NoError(t, store.SetReasoningContent(ctx, "", "x", time.Minute))
	require.NoError(t, store.SetReasoningContent(ctx, "item_empty", "", time.Minute))
	_, err = store.GetReasoningContent(ctx, "item_empty")
	require.ErrorIs(t, err, service.ErrReasoningContentNotFound)
}
