package gatewaylite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	invalidAuthAbuseShardCount       = 64
	invalidAuthRedisNegativeCacheTTL = time.Second
	invalidAuthRedisSuspicionMax     = 16
)

// InvalidAuthAbuseConfig keeps invalid-key protection independent from normal
// user RPM/concurrency limits. A valid request never consumes these counters.
type InvalidAuthAbuseConfig struct {
	Enabled      bool
	Threshold    int
	Window       time.Duration
	Block        time.Duration
	Capacity     int64
	RedisTimeout time.Duration
}

func DefaultInvalidAuthAbuseConfig() InvalidAuthAbuseConfig {
	return InvalidAuthAbuseConfig{
		Enabled:      true,
		Threshold:    120,
		Window:       time.Minute,
		Block:        time.Minute,
		Capacity:     16_384,
		RedisTimeout: 25 * time.Millisecond,
	}
}

func normalizeInvalidAuthAbuseConfig(cfg InvalidAuthAbuseConfig) InvalidAuthAbuseConfig {
	defaults := DefaultInvalidAuthAbuseConfig()
	if cfg.Threshold <= 0 {
		cfg.Threshold = defaults.Threshold
	}
	if cfg.Window <= 0 {
		cfg.Window = defaults.Window
	}
	if cfg.Block <= 0 {
		cfg.Block = defaults.Block
	}
	if cfg.Capacity <= 0 {
		cfg.Capacity = defaults.Capacity
	}
	if cfg.RedisTimeout <= 0 {
		cfg.RedisTimeout = defaults.RedisTimeout
	}
	return cfg
}

type invalidAuthAbuseEntry struct {
	failures            int
	windowStart         time.Time
	blockedUntil        time.Time
	redisCheckNotBefore time.Time
}

type invalidAuthAbuseShard struct {
	mu      sync.RWMutex
	entries map[invalidAuthSubject]*invalidAuthAbuseEntry
}

type invalidAuthSubject struct {
	kind        byte
	fingerprint [16]byte
}

// InvalidAuthAbuseLimiter is deliberately two-layered:
//   - bounded in-process shards reject a repeated attacker without Redis I/O;
//   - Redis combines failures seen by multiple gateway instances.
//
// Redis is never authoritative for valid authentication. If it is unavailable,
// the local layer remains active and requests fail open to the normal Key check.
type InvalidAuthAbuseLimiter struct {
	cfg    InvalidAuthAbuseConfig
	redis  *redis.Client
	prefix string
	shards [invalidAuthAbuseShardCount]invalidAuthAbuseShard
	now    func() time.Time

	tracked          atomic.Int64
	recorded         atomic.Uint64
	blocks           atomic.Uint64
	rejected         atomic.Uint64
	expired          atomic.Uint64
	overflowed       atomic.Uint64
	redisChecks      atomic.Uint64
	redisChecksSaved atomic.Uint64
	redisBlocks      atomic.Uint64
	redisFailures    atomic.Uint64
	cleanupCursor    atomic.Uint32
	cleanupNotBefore atomic.Int64
}

func NewInvalidAuthAbuseLimiter(client *redis.Client, prefix string, cfg InvalidAuthAbuseConfig) *InvalidAuthAbuseLimiter {
	cfg = normalizeInvalidAuthAbuseConfig(cfg)
	limiter := &InvalidAuthAbuseLimiter{
		cfg:    cfg,
		redis:  client,
		prefix: NormalizeRedisPrefix(prefix),
		now:    time.Now,
	}
	for i := range limiter.shards {
		limiter.shards[i].entries = make(map[invalidAuthSubject]*invalidAuthAbuseEntry)
	}
	return limiter
}

// Check returns a retry duration only for a source or Key fingerprint that has
// already produced invalid authentication failures. Raw keys and source
// addresses are never retained or sent to Redis.
func (l *InvalidAuthAbuseLimiter) Check(ctx context.Context, sourceAddress, rawKey string) (time.Duration, bool) {
	if l == nil || !l.cfg.Enabled {
		return 0, false
	}
	now := l.now()
	subjects, subjectCount := invalidAuthSubjects(sourceAddress, rawKey)
	tracked := false
	redisCheckDue := false
	for index := 0; index < subjectCount; index++ {
		subject := subjects[index]
		retry, blocked, exists, checkDue := l.checkLocal(subject, now)
		tracked = tracked || exists
		redisCheckDue = redisCheckDue || checkDue
		if blocked {
			l.rejected.Add(1)
			return retry, true
		}
	}
	// Valid traffic that has never failed authentication stays entirely local.
	// Once a subject is suspicious, one Redis MGET observes cluster-wide blocks.
	if !tracked || l.redis == nil || subjectCount == 0 {
		return 0, false
	}
	// 一次输错 Key 只保留本地小计数，不应让同 NAT 下每个合法请求都多一次
	// Redis MGET。只有本地失败达到合理怀疑阈值时才跨节点确认；一次负结果
	// 再缓存 1 秒，把可疑来源的查询成本也限制为每秒至多一次/进程。
	if !redisCheckDue {
		l.redisChecksSaved.Add(1)
		return 0, false
	}
	retry, blocked, err := l.checkRedis(ctx, subjects, subjectCount, now)
	if err != nil {
		l.redisFailures.Add(1)
		l.deferRedisCheck(subjects, subjectCount, now.Add(invalidAuthRedisNegativeCacheTTL))
		return 0, false
	}
	if blocked {
		l.redisBlocks.Add(1)
		l.rejected.Add(1)
		l.blockLocal(subjects, subjectCount, now.Add(retry))
		return retry, true
	}
	l.deferRedisCheck(subjects, subjectCount, now.Add(invalidAuthRedisNegativeCacheTTL))
	return 0, false
}

// RecordFailure updates both dimensions. It must only be called for malformed,
// unknown, inactive, or secret-mismatched keys, never for policy/quota errors.
func (l *InvalidAuthAbuseLimiter) RecordFailure(ctx context.Context, sourceAddress, rawKey string) {
	if l == nil || !l.cfg.Enabled {
		return
	}
	subjects, subjectCount := invalidAuthSubjects(sourceAddress, rawKey)
	if subjectCount == 0 {
		return
	}
	l.recorded.Add(1)
	now := l.now()
	for index := 0; index < subjectCount; index++ {
		l.recordLocal(subjects[index], now)
	}
	if l.redis == nil {
		return
	}
	blockedUntil, err := l.recordRedis(ctx, subjects, subjectCount, now)
	if err != nil {
		// Redis failure must not turn a valid Key into a 503/429. The bounded local
		// limiter above still protects this process while Redis recovers.
		l.redisFailures.Add(1)
		return
	}
	if blockedUntil.After(now) {
		l.redisBlocks.Add(1)
		l.blockLocal(subjects, subjectCount, blockedUntil)
	}
}

func (l *InvalidAuthAbuseLimiter) RuntimeMetrics(context.Context) map[string]any {
	if l == nil {
		return map[string]any{"invalid_auth_abuse_enabled": false}
	}
	return map[string]any{
		"invalid_auth_abuse_enabled":                  l.cfg.Enabled,
		"invalid_auth_abuse_tracked":                  l.tracked.Load(),
		"invalid_auth_abuse_capacity":                 l.cfg.Capacity,
		"invalid_auth_abuse_recorded_total":           l.recorded.Load(),
		"invalid_auth_abuse_blocks_total":             l.blocks.Load(),
		"invalid_auth_abuse_rejected_total":           l.rejected.Load(),
		"invalid_auth_abuse_expired_total":            l.expired.Load(),
		"invalid_auth_abuse_overflowed_total":         l.overflowed.Load(),
		"invalid_auth_abuse_redis_checks_total":       l.redisChecks.Load(),
		"invalid_auth_abuse_redis_checks_saved_total": l.redisChecksSaved.Load(),
		"invalid_auth_abuse_redis_blocks_total":       l.redisBlocks.Load(),
		"invalid_auth_abuse_redis_failures_total":     l.redisFailures.Load(),
	}
}

func (l *InvalidAuthAbuseLimiter) checkLocal(subject invalidAuthSubject, now time.Time) (time.Duration, bool, bool, bool) {
	shard := l.shard(subject)
	shard.mu.RLock()
	entry := shard.entries[subject]
	if entry == nil {
		shard.mu.RUnlock()
		return 0, false, false, false
	}
	blockedUntil := entry.blockedUntil
	expired := invalidAuthEntryExpired(entry, now, l.cfg.Window)
	redisCheckDue := entry.failures >= invalidAuthRedisSuspicionThreshold(l.cfg.Threshold) &&
		!entry.redisCheckNotBefore.After(now)
	shard.mu.RUnlock()
	if expired {
		l.deleteLocalIfExpired(shard, subject, now)
		return 0, false, false, false
	}
	if blockedUntil.After(now) {
		return blockedUntil.Sub(now), true, true, false
	}
	return 0, false, true, redisCheckDue
}

func invalidAuthRedisSuspicionThreshold(blockThreshold int) int {
	if blockThreshold <= 1 {
		return 1
	}
	threshold := blockThreshold / 4
	if threshold < 2 {
		threshold = 2
	}
	if threshold > invalidAuthRedisSuspicionMax {
		threshold = invalidAuthRedisSuspicionMax
	}
	return threshold
}

func (l *InvalidAuthAbuseLimiter) deferRedisCheck(subjects [2]invalidAuthSubject, subjectCount int, until time.Time) {
	for index := 0; index < subjectCount; index++ {
		subject := subjects[index]
		shard := l.shard(subject)
		shard.mu.Lock()
		if entry := shard.entries[subject]; entry != nil && until.After(entry.redisCheckNotBefore) {
			entry.redisCheckNotBefore = until
		}
		shard.mu.Unlock()
	}
}

func (l *InvalidAuthAbuseLimiter) recordLocal(subject invalidAuthSubject, now time.Time) {
	l.maybeCleanupAtCapacity(now)
	shard := l.shard(subject)
	shard.mu.Lock()
	entry := shard.entries[subject]
	if entry != nil && invalidAuthEntryExpired(entry, now, l.cfg.Window) {
		delete(shard.entries, subject)
		l.tracked.Add(-1)
		l.expired.Add(1)
		entry = nil
	}
	if entry == nil {
		if !l.reserveEntry() {
			shard.mu.Unlock()
			l.overflowed.Add(1)
			return
		}
		entry = &invalidAuthAbuseEntry{windowStart: now}
		shard.entries[subject] = entry
	}
	if entry.blockedUntil.After(now) {
		shard.mu.Unlock()
		return
	}
	if entry.windowStart.After(now) || !now.Before(entry.windowStart.Add(l.cfg.Window)) {
		entry.windowStart = now
		entry.failures = 0
		entry.redisCheckNotBefore = time.Time{}
	}
	entry.failures++
	if entry.failures >= l.cfg.Threshold {
		entry.failures = 0
		entry.blockedUntil = now.Add(l.cfg.Block)
		entry.windowStart = entry.blockedUntil
		l.blocks.Add(1)
	}
	shard.mu.Unlock()
}

func (l *InvalidAuthAbuseLimiter) blockLocal(subjects [2]invalidAuthSubject, subjectCount int, until time.Time) {
	if !until.After(l.now()) {
		return
	}
	for index := 0; index < subjectCount; index++ {
		subject := subjects[index]
		shard := l.shard(subject)
		shard.mu.Lock()
		entry := shard.entries[subject]
		if entry == nil {
			if !l.reserveEntry() {
				shard.mu.Unlock()
				l.overflowed.Add(1)
				continue
			}
			entry = &invalidAuthAbuseEntry{}
			shard.entries[subject] = entry
		}
		if until.After(entry.blockedUntil) {
			entry.blockedUntil = until
			entry.windowStart = until
			entry.failures = 0
			entry.redisCheckNotBefore = time.Time{}
		}
		shard.mu.Unlock()
	}
}

func (l *InvalidAuthAbuseLimiter) reserveEntry() bool {
	for {
		current := l.tracked.Load()
		if current >= l.cfg.Capacity {
			return false
		}
		if l.tracked.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func (l *InvalidAuthAbuseLimiter) maybeCleanupAtCapacity(now time.Time) {
	if l.tracked.Load() < l.cfg.Capacity {
		return
	}
	nowNanos := now.UnixNano()
	for {
		next := l.cleanupNotBefore.Load()
		if nowNanos < next {
			return
		}
		if l.cleanupNotBefore.CompareAndSwap(next, now.Add(100*time.Millisecond).UnixNano()) {
			break
		}
	}
	index := l.cleanupCursor.Add(1) - 1
	shard := &l.shards[index%invalidAuthAbuseShardCount]
	shard.mu.Lock()
	for subject, entry := range shard.entries {
		if invalidAuthEntryExpired(entry, now, l.cfg.Window) {
			delete(shard.entries, subject)
			l.tracked.Add(-1)
			l.expired.Add(1)
		}
	}
	shard.mu.Unlock()
}

func (l *InvalidAuthAbuseLimiter) deleteLocalIfExpired(shard *invalidAuthAbuseShard, subject invalidAuthSubject, now time.Time) {
	shard.mu.Lock()
	if entry := shard.entries[subject]; invalidAuthEntryExpired(entry, now, l.cfg.Window) {
		delete(shard.entries, subject)
		l.tracked.Add(-1)
		l.expired.Add(1)
	}
	shard.mu.Unlock()
}

func invalidAuthEntryExpired(entry *invalidAuthAbuseEntry, now time.Time, window time.Duration) bool {
	return entry != nil && !entry.blockedUntil.After(now) && !entry.windowStart.After(now) && !now.Before(entry.windowStart.Add(window))
}

func (l *InvalidAuthAbuseLimiter) shard(subject invalidAuthSubject) *invalidAuthAbuseShard {
	hash := uint32(2166136261)
	hash ^= uint32(subject.kind)
	hash *= 16777619
	for i := 0; i < len(subject.fingerprint); i++ {
		hash ^= uint32(subject.fingerprint[i])
		hash *= 16777619
	}
	return &l.shards[hash%invalidAuthAbuseShardCount]
}

func invalidAuthSubjects(sourceAddress, rawKey string) ([2]invalidAuthSubject, int) {
	var subjects [2]invalidAuthSubject
	count := 0
	if sourceAddress = strings.TrimSpace(sourceAddress); sourceAddress != "" {
		subjects[count] = invalidAuthSubject{kind: 's', fingerprint: invalidAuthFingerprint(sourceAddress)}
		count++
	}
	if rawKey = strings.TrimSpace(rawKey); rawKey != "" {
		subjects[count] = invalidAuthSubject{kind: 'k', fingerprint: invalidAuthFingerprint(rawKey)}
		count++
	}
	return subjects, count
}

func invalidAuthFingerprint(value string) [16]byte {
	sum := sha256.Sum256([]byte(value))
	// 128 bits is enough for an operational abuse bucket while keeping Redis
	// keys compact. The original high-entropy API Key cannot be reconstructed.
	var fingerprint [16]byte
	copy(fingerprint[:], sum[:16])
	return fingerprint
}

func (l *InvalidAuthAbuseLimiter) redisBaseKey(subject invalidAuthSubject) string {
	dimension := "key"
	if subject.kind == 's' {
		dimension = "source"
	}
	return l.prefix + ":invalid-auth:" + dimension + ":" + hex.EncodeToString(subject.fingerprint[:])
}

func (l *InvalidAuthAbuseLimiter) checkRedis(ctx context.Context, subjects [2]invalidAuthSubject, subjectCount int, now time.Time) (time.Duration, bool, error) {
	keys := make([]string, 0, subjectCount)
	for index := 0; index < subjectCount; index++ {
		keys = append(keys, l.redisBaseKey(subjects[index])+":block")
	}
	if len(keys) == 0 {
		return 0, false, nil
	}
	l.redisChecks.Add(1)
	redisCtx, cancel := context.WithTimeout(ctx, l.cfg.RedisTimeout)
	defer cancel()
	values, err := l.redis.MGet(redisCtx, keys...).Result()
	if err != nil {
		return 0, false, err
	}
	nowMillis := now.UnixMilli()
	var maxUntil int64
	for _, value := range values {
		if value == nil {
			continue
		}
		raw := ""
		switch typed := value.(type) {
		case string:
			raw = typed
		case []byte:
			raw = string(typed)
		}
		until, parseErr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if parseErr == nil && until > maxUntil {
			maxUntil = until
		}
	}
	if maxUntil <= nowMillis {
		return 0, false, nil
	}
	return time.Duration(maxUntil-nowMillis) * time.Millisecond, true, nil
}

func (l *InvalidAuthAbuseLimiter) recordRedis(ctx context.Context, subjects [2]invalidAuthSubject, subjectCount int, now time.Time) (time.Time, error) {
	keys := make([]string, 0, subjectCount)
	for index := 0; index < subjectCount; index++ {
		keys = append(keys, l.redisBaseKey(subjects[index]))
	}
	if len(keys) == 0 {
		return time.Time{}, nil
	}
	redisCtx, cancel := context.WithTimeout(ctx, l.cfg.RedisTimeout)
	defer cancel()
	result, err := invalidAuthAbuseRecordScript.Run(redisCtx, l.redis, keys,
		l.cfg.Threshold, l.cfg.Window.Milliseconds(), l.cfg.Block.Milliseconds(), now.UnixMilli()).Int64()
	if err != nil {
		return time.Time{}, err
	}
	if result <= now.UnixMilli() {
		return time.Time{}, nil
	}
	return time.UnixMilli(result), nil
}

var invalidAuthAbuseRecordScript = redis.NewScript(`
local threshold = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local block_ms = tonumber(ARGV[3])
local now_ms = tonumber(ARGV[4])
local max_blocked_until = 0

for _, base_key in ipairs(KEYS) do
  local count_key = base_key .. ':count'
  local block_key = base_key .. ':block'
  local blocked_until = tonumber(redis.call('GET', block_key) or '0')
  if blocked_until <= now_ms then
    if blocked_until > 0 then
      redis.call('DEL', block_key)
    end
    local failures = redis.call('INCR', count_key)
    if failures == 1 then
      redis.call('PEXPIRE', count_key, window_ms)
    end
    if failures >= threshold then
      blocked_until = now_ms + block_ms
      redis.call('SET', block_key, tostring(blocked_until), 'PX', block_ms)
      redis.call('DEL', count_key)
    end
  end
  if blocked_until > max_blocked_until then
    max_blocked_until = blocked_until
  end
end

return max_blocked_until
`)
