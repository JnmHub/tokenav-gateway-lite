package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// slotCapacityCacheStub 用「真实容量」语义替代既有桩的脚本式应答：
// 容量内允许获取、超出返回不可获取、释放后立即可再次获取 —— 这样才能测「释放→唤醒」的时延。
type slotCapacityCacheStub struct {
	helperConcurrencyCacheStub

	mu   sync.Mutex
	held map[int64]int
}

func newSlotCapacityCacheStub() *slotCapacityCacheStub {
	return &slotCapacityCacheStub{held: map[int64]int{}}
}

func (s *slotCapacityCacheStub) AcquireUserSlot(_ context.Context, userID int64, maxConcurrency int, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held[userID] >= maxConcurrency {
		return false, nil
	}
	s.held[userID]++
	return true, nil
}

func (s *slotCapacityCacheStub) ReleaseUserSlot(_ context.Context, userID int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held[userID] > 0 {
		s.held[userID]--
	}
	return nil
}

// 释放即通知：槽位刚被释放时，等待者必须立刻拿到槽位，而不是睡满 100ms 起步的轮询退避。
// 修复前这条路径只能靠轮询（首轮退避 100ms 到期才重试），所以 60ms 的阈值能区分两种行为；
// 这也是「刚腾出的容量被空置到秒级」那个延迟放大问题的回归护栏。
func TestWaitForSlotWithPingTimeout_WakesUpOnSlotRelease(t *testing.T) {
	cache := newSlotCapacityCacheStub()
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 5*time.Millisecond)

	holderCtx, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	holderStarted := false
	releaseHolder, err := helper.waitForSlotWithPingTimeout(holderCtx, "user", 707, 1, time.Second, 0, false, &holderStarted, true)
	require.NoError(t, err)
	require.NotNil(t, releaseHolder)

	waiterCtx, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	waiterStarted := false
	acquired := make(chan error, 1)
	go func() {
		release, waitErr := helper.waitForSlotWithPingTimeout(waiterCtx, "user", 707, 1, 2*time.Second, 0, false, &waiterStarted, false)
		if waitErr == nil && release != nil {
			defer release()
		}
		acquired <- waitErr
	}()

	// 让等待者先进入等待：它的首个退避是 100ms，此时还远未到期。
	time.Sleep(30 * time.Millisecond)

	start := time.Now()
	releaseHolder()
	select {
	case waitErr := <-acquired:
		require.NoError(t, waitErr)
		elapsed := time.Since(start)
		require.Less(t, elapsed, 50*time.Millisecond,
			"槽位释放后等待者应立刻被唤醒（实测 %v；修复前实测 69.8ms —— 那正是首轮 100ms 退避到期）", elapsed)
	case <-time.After(2 * time.Second):
		t.Fatal("等待者始终没有拿到槽位")
	}
}
