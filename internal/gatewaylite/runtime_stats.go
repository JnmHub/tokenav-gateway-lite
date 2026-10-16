package gatewaylite

import (
	"sync"
	"sync/atomic"
	"time"
)

// RuntimeStats tracks local gateway activity for lightweight health reports.
type RuntimeStats struct {
	shards [runtimeStatsShardCount]runtimeStatsShard
}

const runtimeStatsShardCount = 64

type runtimeStatsShard struct {
	mu         sync.RWMutex
	activeUser map[int64]*atomic.Int64
}

var defaultRuntimeStats atomic.Value

func NewRuntimeStats() *RuntimeStats {
	stats := &RuntimeStats{}
	for index := range stats.shards {
		stats.shards[index].activeUser = make(map[int64]*atomic.Int64)
	}
	return stats
}

func (s *RuntimeStats) RecordUser(userID int64, at time.Time) {
	if s == nil || userID <= 0 {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	shard := &s.shards[runtimeStatsShardIndex(userID)]
	shard.mu.RLock()
	activity := shard.activeUser[userID]
	if activity != nil {
		activity.Store(at.UnixNano())
		shard.mu.RUnlock()
		return
	}
	shard.mu.RUnlock()

	shard.mu.Lock()
	activity = shard.activeUser[userID]
	if activity == nil {
		activity = &atomic.Int64{}
		shard.activeUser[userID] = activity
	}
	activity.Store(at.UnixNano())
	shard.mu.Unlock()
}

func (s *RuntimeStats) OnlineUsers(now time.Time, window time.Duration) int {
	if s == nil {
		return 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	cutoff := now.Add(-window)
	cutoffUnixNano := cutoff.UnixNano()
	count := 0
	for index := range s.shards {
		shard := &s.shards[index]
		shard.mu.Lock()
		for userID, activity := range shard.activeUser {
			if activity == nil || activity.Load() < cutoffUnixNano {
				delete(shard.activeUser, userID)
				continue
			}
			count++
		}
		shard.mu.Unlock()
	}
	return count
}

func runtimeStatsShardIndex(userID int64) uint64 {
	// 乘法散列避免连续 user_id 总是集中在相邻 cache line；64 为固定 2 次幂。
	return (uint64(userID) * 11400714819323198485) & (runtimeStatsShardCount - 1)
}

func SetDefaultRuntimeStats(stats *RuntimeStats) {
	defaultRuntimeStats.Store(stats)
}

func DefaultRuntimeStats() *RuntimeStats {
	value := defaultRuntimeStats.Load()
	if value == nil {
		return nil
	}
	stats, _ := value.(*RuntimeStats)
	return stats
}
