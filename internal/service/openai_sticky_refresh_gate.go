package service

import (
	"strconv"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"golang.org/x/sync/singleflight"
)

const (
	// 粘性绑定本身保留一小时；同一活跃会话每 30 秒刷新一次已经足够，
	// 无需让千并发请求全部同步写 Redis 的新旧两套兼容键。
	openAIStickyRefreshMinInterval        = 30 * time.Second
	openAIStickyRefreshShardCount         = 64
	openAIStickyRefreshMaxEntriesPerShard = 512
	openAIStickyBindingLocalTTL           = 2 * time.Second
)

type openAIStickyRefreshKey struct {
	groupID     int64
	sessionHash string
}

type openAIStickyRefreshShard struct {
	mu          sync.Mutex
	nextRefresh map[openAIStickyRefreshKey]time.Time
	bindings    map[openAIStickyRefreshKey]openAIStickyBindingEntry
}

type openAIStickyBindingEntry struct {
	accountID int64
	expiresAt time.Time
}

// openAIStickyRefreshGate 合并 TTL 刷新写入，并短暂缓存粘性账号 ID；
// 账号状态、模型能力和并发槽仍在后续实时校验，不缓存最终调度结果。
// 固定分片和每片容量使内存有明确上限；满载时淘汰最早记录，最坏情况
// 只是多读写一次 Redis，不会放宽调度条件。
type openAIStickyRefreshGate struct {
	interval           time.Duration
	maxEntriesPerShard int
	shards             [openAIStickyRefreshShardCount]openAIStickyRefreshShard
	bindingLoads       singleflight.Group
}

func newOpenAIStickyRefreshGate(interval time.Duration, maxEntriesPerShard int) *openAIStickyRefreshGate {
	if interval <= 0 {
		interval = openAIStickyRefreshMinInterval
	}
	if maxEntriesPerShard <= 0 {
		maxEntriesPerShard = openAIStickyRefreshMaxEntriesPerShard
	}
	return &openAIStickyRefreshGate{
		interval:           interval,
		maxEntriesPerShard: maxEntriesPerShard,
	}
}

func (g *openAIStickyRefreshGate) shardFor(key openAIStickyRefreshKey) *openAIStickyRefreshShard {
	if g == nil {
		return nil
	}
	hash := xxhash.Sum64String(key.sessionHash) ^ uint64(key.groupID)
	return &g.shards[hash%openAIStickyRefreshShardCount]
}

func (g *openAIStickyRefreshGate) tryReserve(key openAIStickyRefreshKey, now time.Time, stickyTTL time.Duration) bool {
	shard := g.shardFor(key)
	if shard == nil || key.sessionHash == "" {
		return true
	}

	shard.mu.Lock()
	defer shard.mu.Unlock()
	if next, ok := shard.nextRefresh[key]; ok && now.Before(next) {
		return false
	}
	if shard.nextRefresh == nil {
		shard.nextRefresh = make(map[openAIStickyRefreshKey]time.Time)
	}
	if _, exists := shard.nextRefresh[key]; !exists && len(shard.nextRefresh) >= g.maxEntriesPerShard {
		g.evictOneLocked(shard, now)
	}
	interval := g.interval
	// 配置允许缩短粘性 TTL；节流窗口不能比 TTL 本身更长，否则低 TTL
	// 环境可能在下一次允许刷新前过期。最多使用 TTL 的三分之一。
	if stickyTTL > 0 {
		safeInterval := stickyTTL / 3
		if safeInterval > 0 && safeInterval < interval {
			interval = safeInterval
		}
	}
	shard.nextRefresh[key] = now.Add(interval)
	return true
}

func (g *openAIStickyRefreshGate) invalidateRefresh(key openAIStickyRefreshKey) {
	shard := g.shardFor(key)
	if shard == nil || key.sessionHash == "" {
		return
	}
	shard.mu.Lock()
	delete(shard.nextRefresh, key)
	shard.mu.Unlock()
}

func (g *openAIStickyRefreshGate) lookupBinding(key openAIStickyRefreshKey, now time.Time) (int64, bool) {
	shard := g.shardFor(key)
	if shard == nil || key.sessionHash == "" {
		return 0, false
	}
	shard.mu.Lock()
	defer shard.mu.Unlock()
	entry, ok := shard.bindings[key]
	if !ok {
		return 0, false
	}
	if !now.Before(entry.expiresAt) {
		delete(shard.bindings, key)
		return 0, false
	}
	return entry.accountID, entry.accountID > 0
}

func (g *openAIStickyRefreshGate) storeBinding(key openAIStickyRefreshKey, accountID int64, now time.Time) {
	shard := g.shardFor(key)
	if shard == nil || key.sessionHash == "" || accountID <= 0 {
		return
	}
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if shard.bindings == nil {
		shard.bindings = make(map[openAIStickyRefreshKey]openAIStickyBindingEntry)
	}
	if _, exists := shard.bindings[key]; !exists && len(shard.bindings) >= g.maxEntriesPerShard {
		g.evictOneBindingLocked(shard, now)
	}
	shard.bindings[key] = openAIStickyBindingEntry{
		accountID: accountID,
		expiresAt: now.Add(openAIStickyBindingLocalTTL),
	}
}

func (g *openAIStickyRefreshGate) deleteBinding(key openAIStickyRefreshKey) {
	shard := g.shardFor(key)
	if shard == nil || key.sessionHash == "" {
		return
	}
	shard.mu.Lock()
	delete(shard.bindings, key)
	shard.mu.Unlock()
	// Forget only affects a future load; an already running load may finish, but
	// account eligibility is still revalidated before the account can be used.
	g.bindingLoads.Forget(g.bindingLoadKey(key))
}

func (g *openAIStickyRefreshGate) loadBinding(
	key openAIStickyRefreshKey,
	loader func() (int64, error),
) (int64, error) {
	if g == nil {
		return loader()
	}
	value, err, _ := g.bindingLoads.Do(g.bindingLoadKey(key), func() (any, error) {
		if accountID, ok := g.lookupBinding(key, time.Now()); ok {
			return accountID, nil
		}
		accountID, loadErr := loader()
		if loadErr == nil && accountID > 0 {
			g.storeBinding(key, accountID, time.Now())
		}
		return accountID, loadErr
	})
	if err != nil {
		return 0, err
	}
	accountID, _ := value.(int64)
	return accountID, nil
}

func (g *openAIStickyRefreshGate) bindingLoadKey(key openAIStickyRefreshKey) string {
	return strconv.FormatInt(key.groupID, 10) + ":" + key.sessionHash
}

func (g *openAIStickyRefreshGate) evictOneLocked(shard *openAIStickyRefreshShard, now time.Time) {
	var oldestKey openAIStickyRefreshKey
	var oldestTime time.Time
	for key, next := range shard.nextRefresh {
		if !now.Before(next) {
			delete(shard.nextRefresh, key)
			return
		}
		if oldestTime.IsZero() || next.Before(oldestTime) {
			oldestKey = key
			oldestTime = next
		}
	}
	if !oldestTime.IsZero() {
		delete(shard.nextRefresh, oldestKey)
	}
}

func (g *openAIStickyRefreshGate) evictOneBindingLocked(shard *openAIStickyRefreshShard, now time.Time) {
	var oldestKey openAIStickyRefreshKey
	var oldestTime time.Time
	for key, entry := range shard.bindings {
		if !now.Before(entry.expiresAt) {
			delete(shard.bindings, key)
			return
		}
		if oldestTime.IsZero() || entry.expiresAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.expiresAt
		}
	}
	if !oldestTime.IsZero() {
		delete(shard.bindings, oldestKey)
	}
}
