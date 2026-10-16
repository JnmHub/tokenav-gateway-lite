//go:build unit

package service

import (
	"context"
	"sync"
	"time"
)

// fakeLeaderLockCache 是 LeaderLockCache 的内存实现，供单元测试使用。
// 它模拟真实 Redis 实现的 compare-and-delete 释放语义：
// 只有持锁者本人才能释放锁，他人释放是 no-op。
// 自官方 0.2.7 leader_lock_test.go 移植。
type fakeLeaderLockCache struct {
	mu         sync.Mutex
	owners     map[string]string
	acquireErr error
}

func (f *fakeLeaderLockCache) TryAcquireLeaderLock(_ context.Context, key, owner string, _ time.Duration) (bool, error) {
	if f.acquireErr != nil {
		return false, f.acquireErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners == nil {
		f.owners = map[string]string{}
	}
	if _, held := f.owners[key]; held {
		return false, nil
	}
	f.owners[key] = owner
	return true, nil
}

func (f *fakeLeaderLockCache) ReleaseLeaderLock(_ context.Context, key, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.owners[key] == owner {
		delete(f.owners, key)
	}
	return nil
}

// heldBy 返回 key 当前的持锁者（测试断言用）。
func (f *fakeLeaderLockCache) heldBy(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners[key]
}
