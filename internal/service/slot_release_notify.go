package service

import (
	"strconv"
	"sync"
)

// 「槽位已释放」进程内广播总线。
//
// 背景：handler 的 ConcurrencyHelper.waitForSlotWithPingTimeout 在槽满时用
// initialBackoff(100ms) → ×1.5 → maxBackoff(2s) 的轮询退避反复重试；而槽被真正释放时
// （账号槽在批量释放刷进 Redis 之后、用户槽在即时释放之后）没有任何人叫醒等待者，
// 于是刚腾出来的容量要等某个等待者的退避到期才被用上，把排队延迟放大到秒级。
//
// 这里只负责「叫醒」：等待者收到通知后立刻重试一次，抢不到就继续按原有退避等。
// 因此信号丢失（或槽位是在另一个网关实例释放的）不影响正确性 —— 只是退回原来的轮询节奏。
//
// 为什么做成包级总线，而不是挂在 ConcurrencyService 上：
//   - 释放点在 service 包内（flushAccountSlotReleases / flushUserAPIKeySlotReleases /
//     AcquireUserSlot 的 ReleaseFunc），等待者在 handler 包；
//   - 包级函数可避免改动 handler 依赖的并发服务接口与各处测试桩，
//     也不会因为存在多个 ConcurrencyService 实例而出现「等待者与释放者不在同一条总线」。
var (
	slotReleaseNotifyMu sync.Mutex
	// slotReleaseNotify 存「当前代」的通知 channel：通知时关闭并从表中删除，
	// 下一个等待者会拿到全新的 channel，不会重复消费一个已关闭的 channel。
	slotReleaseNotify = map[string]chan struct{}{}
)

// SlotReleaseTypeUser / SlotReleaseTypeAccount 是释放通知的槽位类型标识，
// 取值必须与 handler 中 waitForSlotWithPingTimeout 的 slotType 一致。
const (
	SlotReleaseTypeUser    = "user"
	SlotReleaseTypeAccount = "account"
)

func slotReleaseNotifyKey(slotType string, id int64) string {
	return slotType + ":" + strconv.FormatInt(id, 10)
}

// NotifySlotReleased 广播「某类槽位已有空闲」，唤醒本进程里正在等待该槽位的请求。
//
// 这是纯通知语义：没有任何等待者时是一次空操作；重复调用也是安全的。
func NotifySlotReleased(slotType string, id int64) {
	key := slotReleaseNotifyKey(slotType, id)
	slotReleaseNotifyMu.Lock()
	ch := slotReleaseNotify[key]
	delete(slotReleaseNotify, key)
	slotReleaseNotifyMu.Unlock()
	if ch != nil {
		// 关闭而不是发送：一次性唤醒该槽位上的所有等待者（惊群规模 = 该槽位的本地等待者数）。
		close(ch)
	}
}

// SlotReleaseChannel 返回该槽位「当前代」的释放通知 channel。
// 等待者每被唤醒一次就要重新获取，避免复用已关闭的 channel。
func SlotReleaseChannel(slotType string, id int64) <-chan struct{} {
	key := slotReleaseNotifyKey(slotType, id)
	slotReleaseNotifyMu.Lock()
	defer slotReleaseNotifyMu.Unlock()
	ch := slotReleaseNotify[key]
	if ch == nil {
		ch = make(chan struct{})
		slotReleaseNotify[key] = ch
	}
	return ch
}

// notifyReleasedAccountSlots 在账号槽批量释放成功后广播：同一批次里同账号只广播一次。
func notifyReleasedAccountSlots(batch []AccountSlotRelease) {
	if len(batch) == 0 {
		return
	}
	seen := make(map[int64]struct{}, len(batch))
	for _, release := range batch {
		if _, ok := seen[release.AccountID]; ok {
			continue
		}
		seen[release.AccountID] = struct{}{}
		NotifySlotReleased(SlotReleaseTypeAccount, release.AccountID)
	}
}

// notifyReleasedUserSlots 在「用户+API Key」槽批量释放成功后广播用户槽空闲。
// 该批量接口同时释放用户槽与 API Key 槽位，所以这里按 UserID 广播是准确的。
func notifyReleasedUserSlots(batch []UserAPIKeySlotRelease) {
	if len(batch) == 0 {
		return
	}
	seen := make(map[int64]struct{}, len(batch))
	for _, release := range batch {
		if _, ok := seen[release.UserID]; ok {
			continue
		}
		seen[release.UserID] = struct{}{}
		NotifySlotReleased(SlotReleaseTypeUser, release.UserID)
	}
}
