package gatewaylite

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRuntimeStatsCountsActiveUsersWithinWindow(t *testing.T) {
	stats := NewRuntimeStats()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	stats.RecordUser(1, now.Add(-time.Minute))
	stats.RecordUser(2, now.Add(-4*time.Minute))
	stats.RecordUser(3, now.Add(-6*time.Minute))

	require.Equal(t, 2, stats.OnlineUsers(now, 5*time.Minute))
	require.Equal(t, 2, stats.OnlineUsers(now, 5*time.Minute), "过期用户应被清理，重复统计应稳定")
}

func TestRuntimeStatsConcurrentRecordPreservesAllActiveUsers(t *testing.T) {
	stats := NewRuntimeStats()
	now := time.Now()
	var workers sync.WaitGroup
	for userID := int64(1); userID <= 1024; userID++ {
		workers.Add(1)
		go func(id int64) {
			defer workers.Done()
			for attempt := 0; attempt < 20; attempt++ {
				stats.RecordUser(id, now)
			}
		}(userID)
	}
	workers.Wait()
	require.Equal(t, 1024, stats.OnlineUsers(now, 5*time.Minute))
}

type legacyRuntimeStats struct {
	mu         sync.Mutex
	activeUser map[int64]time.Time
}

func (s *legacyRuntimeStats) record(userID int64, at time.Time) {
	s.mu.Lock()
	s.activeUser[userID] = at
	s.mu.Unlock()
}

func BenchmarkRuntimeStatsRecordUserParallel(b *testing.B) {
	now := time.Now()
	for _, sameUser := range []bool{true, false} {
		name := "1024_users"
		if sameUser {
			name = "same_user"
		}
		b.Run("legacy_mutex/"+name, func(b *testing.B) {
			stats := &legacyRuntimeStats{activeUser: make(map[int64]time.Time)}
			var sequence atomic.Uint64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					userID := int64(1)
					if !sameUser {
						userID = int64(sequence.Add(1)%1024 + 1)
					}
					stats.record(userID, now)
				}
			})
		})
		b.Run("sharded_atomic/"+name, func(b *testing.B) {
			stats := NewRuntimeStats()
			var sequence atomic.Uint64
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					userID := int64(1)
					if !sameUser {
						userID = int64(sequence.Add(1)%1024 + 1)
					}
					stats.RecordUser(userID, now)
				}
			})
		})
	}
}
