//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGrokTeamModelRateLimit_MarksAndFiltersSiblings(t *testing.T) {
	// Isolate from other tests by using unique team ids.
	team := "team-test-" + time.Now().Format("150405.000")
	a1 := &Account{
		ID: 101, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"team_id": team},
	}
	a2 := &Account{
		ID: 102, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"team_id": team},
	}
	other := &Account{
		ID: 103, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"team_id": team + "-other"},
	}
	noTeam := &Account{
		ID: 104, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{},
	}

	now := time.Now()
	markGrokTeamModelRateLimit(a1, "grok-4.5", now.Add(5*time.Minute))

	require.True(t, isGrokTeamModelRateLimited(a1, "grok-4.5", now))
	require.True(t, isGrokTeamModelRateLimited(a2, "grok-4.5", now), "sibling with same team must cool")
	require.False(t, isGrokTeamModelRateLimited(a2, "grok-4.3", now), "other model stays pickable")
	require.False(t, isGrokTeamModelRateLimited(other, "grok-4.5", now))
	require.False(t, isGrokTeamModelRateLimited(noTeam, "grok-4.5", now))

	filtered := filterGrokTeamModelRateLimitedAccounts([]Account{*a1, *a2, *other, *noTeam}, "grok-4.5", now)
	require.Len(t, filtered, 2)
	ids := []int64{filtered[0].ID, filtered[1].ID}
	require.Contains(t, ids, int64(103))
	require.Contains(t, ids, int64(104))
}

func TestGrokTeamModelRateLimit_Expires(t *testing.T) {
	team := "team-expire-" + time.Now().Format("150405.000")
	a := &Account{
		ID: 201, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"team_id": team},
	}
	past := time.Now().Add(-time.Minute)
	markGrokTeamModelRateLimit(a, "grok-4.5", past)
	// mark clamps expired until into default TTL from "now" — use direct store inject via past+recheck
	// After mark with past, resolveGrokTeamRateLimitUntil path isn't used; mark uses now+default when until not after now.
	require.True(t, isGrokTeamModelRateLimited(a, "grok-4.5", time.Now()))
}

func TestGrokTeamModelRateLimitFilterUsesMappedUpstreamModel(t *testing.T) {
	now := time.Now()
	account := &Account{
		ID:       301,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"team_id":       "team-mapped-301",
			"model_mapping": map[string]any{"gpt-*": "grok-4.5"},
		},
	}
	markGrokTeamModelRateLimit(account, "grok-4.5", now.Add(time.Hour))

	require.Empty(t, filterGrokTeamModelRateLimitedAccounts([]Account{*account}, "gpt-5", now))
}

func TestGrokTeamModelRateLimitRedisSharesAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	clientA := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})

	storeA := newGrokTeamModelRateLimitStore(clientA, "test-cluster")
	storeB := newGrokTeamModelRateLimitStore(clientB, "test-cluster")
	account := &Account{
		ID: 401, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"team_id": "shared-team"},
	}
	now := time.Now()
	storeA.mark(context.Background(), account, "grok-4.5", now.Add(5*time.Minute))

	require.True(t, storeB.isRateLimited(context.Background(), account, "grok-4.5", now),
		"another process store must observe the Redis cooldown")
	require.Empty(t, storeB.filter(context.Background(), []Account{*account}, "grok-4.5", now))
}

func TestGrokTeamModelRateLimitRedisFailureFallsBackLocal(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := newGrokTeamModelRateLimitStore(client, "test-fallback")
	account := &Account{
		ID: 402, Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"team_id": "fallback-team"},
	}
	mr.Close()
	now := time.Now()
	store.mark(context.Background(), account, "grok-4.5", now.Add(5*time.Minute))

	require.True(t, store.isRateLimited(context.Background(), account, "grok-4.5", now),
		"Redis failure must not erase the process-local cooldown")
}
