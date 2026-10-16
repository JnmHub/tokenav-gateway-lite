package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/redis/go-redis/v9"
)

// grokTeamModelRateLimitStore keeps a local fallback and optionally mirrors the
// same cooldown into Redis. The local copy is always written first, so a Redis
// outage never makes the instance immediately retry a team that it just saw 429.
type grokTeamModelRateLimit struct {
	Until time.Time
}

type grokTeamModelRateLimitStore struct {
	mu          sync.Mutex
	items       map[string]grokTeamModelRateLimit
	redisClient *redis.Client
	redisPrefix string
}

var globalGrokTeamModelRateLimits atomic.Pointer[grokTeamModelRateLimitStore]

const (
	grokTeamRateLimitDefaultTTL = 10 * time.Minute
	grokTeamRateLimitMaxTTL     = time.Hour
	grokTeamRateLimitMinTTL     = 30 * time.Second
	grokTeamRateLimitRedisTTL   = grokTeamRateLimitMaxTTL
	grokTeamRateLimitRedisWait  = 100 * time.Millisecond
)

var grokTeamRateLimitSetScript = redis.NewScript(`
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
local incoming = tonumber(ARGV[1]) or 0
if current >= incoming then
  return current
end
redis.call("PSETEX", KEYS[1], ARGV[2], incoming)
return incoming
`)

func init() {
	globalGrokTeamModelRateLimits.Store(newGrokTeamModelRateLimitStore(nil, ""))
}

func newGrokTeamModelRateLimitStore(redisClient *redis.Client, redisPrefix string) *grokTeamModelRateLimitStore {
	return &grokTeamModelRateLimitStore{
		items:       make(map[string]grokTeamModelRateLimit),
		redisClient: redisClient,
		redisPrefix: gatewaylite.NormalizeRedisPrefix(redisPrefix) + ":grok:team_model_cooldown",
	}
}

// configureGrokTeamModelRateLimitStore is called once by runtime dependency
// injection. Constructors used by unit tests remain unchanged and local-only.
func configureGrokTeamModelRateLimitStore(redisClient *redis.Client, redisPrefix string) {
	globalGrokTeamModelRateLimits.Store(newGrokTeamModelRateLimitStore(redisClient, redisPrefix))
}

func currentGrokTeamModelRateLimitStore() *grokTeamModelRateLimitStore {
	if store := globalGrokTeamModelRateLimits.Load(); store != nil {
		return store
	}
	store := newGrokTeamModelRateLimitStore(nil, "")
	globalGrokTeamModelRateLimits.CompareAndSwap(nil, store)
	return globalGrokTeamModelRateLimits.Load()
}

func grokTeamFingerprint(teamID string) string {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(teamID)))
	return hex.EncodeToString(sum[:8])
}

func grokTeamModelRateLimitKey(teamFingerprint, model string) string {
	return teamFingerprint + "|" + strings.ToLower(strings.TrimSpace(model))
}

func accountGrokTeamID(account *Account) string {
	if account == nil {
		return ""
	}
	return strings.TrimSpace(account.GetCredential("team_id"))
}

// markGrokTeamModelRateLimit records that this team+model pair should be skipped
// until until. No-op when team_id or model is empty.
func markGrokTeamModelRateLimit(account *Account, model string, until time.Time) {
	currentGrokTeamModelRateLimitStore().mark(context.Background(), account, model, until)
}

func (s *grokTeamModelRateLimitStore) mark(ctx context.Context, account *Account, model string, until time.Time) {
	if account == nil || !account.IsGrokOAuth() {
		return
	}
	fp := grokTeamFingerprint(accountGrokTeamID(account))
	model = strings.TrimSpace(model)
	if fp == "" || model == "" || until.IsZero() {
		return
	}
	now := time.Now()
	if !until.After(now) {
		until = now.Add(grokTeamRateLimitDefaultTTL)
	}
	maxUntil := now.Add(grokTeamRateLimitMaxTTL)
	if until.After(maxUntil) {
		until = maxUntil
	}
	key := grokTeamModelRateLimitKey(fp, model)
	s.markLocal(key, until, now)
	if s.redisClient == nil {
		return
	}
	redisCtx, cancel := grokTeamRateLimitRedisContext(ctx)
	defer cancel()
	ttl := until.Sub(now)
	if ttl <= 0 || ttl > grokTeamRateLimitRedisTTL {
		ttl = grokTeamRateLimitRedisTTL
	}
	ttlMillis := ttl.Milliseconds()
	if ttlMillis < 1 {
		ttlMillis = 1
	}
	_, _ = grokTeamRateLimitSetScript.Run(
		redisCtx,
		s.redisClient,
		[]string{s.redisKey(key)},
		until.UTC().UnixMilli(),
		ttlMillis,
	).Result()
}

func (s *grokTeamModelRateLimitStore) markLocal(key string, until, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.items[key]; ok && cur.Until.After(until) {
		return
	}
	s.items[key] = grokTeamModelRateLimit{Until: until}
	// Opportunistic prune of expired entries.
	for k, v := range s.items {
		if !v.Until.After(now) {
			delete(s.items, k)
		}
	}
}

// isGrokTeamModelRateLimited reports whether the account's team is currently
// blocked for the requested model.
func isGrokTeamModelRateLimited(account *Account, model string, now time.Time) bool {
	return currentGrokTeamModelRateLimitStore().isRateLimited(context.Background(), account, model, now)
}

func (s *grokTeamModelRateLimitStore) isRateLimited(ctx context.Context, account *Account, model string, now time.Time) bool {
	if account == nil || !account.IsGrokOAuth() {
		return false
	}
	fp := grokTeamFingerprint(accountGrokTeamID(account))
	model = strings.TrimSpace(model)
	if fp == "" || model == "" {
		return false
	}
	key := grokTeamModelRateLimitKey(fp, model)
	if s.isLocalRateLimited(key, now) {
		return true
	}
	remote, ok := s.loadRemote(ctx, []string{key}, now)
	if !ok {
		return false
	}
	until, exists := remote[key]
	return exists && until.After(now)
}

func (s *grokTeamModelRateLimitStore) isLocalRateLimited(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.items[key]
	if ok && !cur.Until.After(now) {
		delete(s.items, key)
		return false
	}
	return ok
}

// filterGrokTeamModelRateLimitedAccounts drops candidates whose team is under a
// model-scoped rate-limit cool. Accounts without team_id pass through.
func filterGrokTeamModelRateLimitedAccounts(accounts []Account, model string, now time.Time) []Account {
	return currentGrokTeamModelRateLimitStore().filter(context.Background(), accounts, model, now)
}

func filterGrokTeamModelRateLimitedAccountsWithContext(ctx context.Context, accounts []Account, model string, now time.Time) []Account {
	return currentGrokTeamModelRateLimitStore().filter(ctx, accounts, model, now)
}

func (s *grokTeamModelRateLimitStore) filter(ctx context.Context, accounts []Account, model string, now time.Time) []Account {
	if len(accounts) == 0 || strings.TrimSpace(model) == "" {
		return accounts
	}
	logicalKeys := make([]string, len(accounts))
	uniqueKeys := make([]string, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for i := range accounts {
		upstreamModel := canonicalOpenAIAccountSchedulingModel(&accounts[i], model)
		fp := grokTeamFingerprint(accountGrokTeamID(&accounts[i]))
		if !accounts[i].IsGrokOAuth() || fp == "" || strings.TrimSpace(upstreamModel) == "" {
			continue
		}
		key := grokTeamModelRateLimitKey(fp, upstreamModel)
		logicalKeys[i] = key
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			uniqueKeys = append(uniqueKeys, key)
		}
	}
	remote, _ := s.loadRemote(ctx, uniqueKeys, now)
	out := accounts[:0]
	for i := range accounts {
		key := logicalKeys[i]
		if key != "" && (s.isLocalRateLimited(key, now) || remote[key].After(now)) {
			continue
		}
		out = append(out, accounts[i])
	}
	return out
}

func (s *grokTeamModelRateLimitStore) loadRemote(ctx context.Context, logicalKeys []string, now time.Time) (map[string]time.Time, bool) {
	if s == nil || s.redisClient == nil || len(logicalKeys) == 0 {
		return nil, false
	}
	redisKeys := make([]string, len(logicalKeys))
	for i, key := range logicalKeys {
		redisKeys[i] = s.redisKey(key)
	}
	redisCtx, cancel := grokTeamRateLimitRedisContext(ctx)
	defer cancel()
	values, err := s.redisClient.MGet(redisCtx, redisKeys...).Result()
	if err != nil {
		return nil, false
	}
	out := make(map[string]time.Time, len(logicalKeys))
	for i, raw := range values {
		if raw == nil {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			continue
		}
		millis, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if parseErr != nil {
			continue
		}
		until := time.UnixMilli(millis)
		if !until.After(now) {
			continue
		}
		out[logicalKeys[i]] = until
		s.markLocal(logicalKeys[i], until, now)
	}
	return out, true
}

func (s *grokTeamModelRateLimitStore) redisKey(logicalKey string) string {
	return s.redisPrefix + ":" + strings.ReplaceAll(logicalKey, "|", ":")
}

func grokTeamRateLimitRedisContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, grokTeamRateLimitRedisWait)
}

// resolveGrokTeamRateLimitUntil derives a team cool window from an observed
// account rate-limit reset, with sane clamps.
func resolveGrokTeamRateLimitUntil(resetAt, now time.Time) time.Time {
	if resetAt.After(now.Add(grokTeamRateLimitMinTTL)) {
		maxUntil := now.Add(grokTeamRateLimitMaxTTL)
		if resetAt.After(maxUntil) {
			return maxUntil
		}
		return resetAt
	}
	return now.Add(grokTeamRateLimitDefaultTTL)
}
