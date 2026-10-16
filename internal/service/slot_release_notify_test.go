package service

import (
	"testing"
	"time"
)

// 释放通知的基本语义：通知唤醒「当前代」的等待者，并且等待者随后会拿到新一代 channel。
// 这条性质是等槽从「轮询退避」改成「释放即通知」的地基：若通知后仍返回同一个已关闭的
// channel，等待者会一直空转重试（惊群 + 无效 Redis 往返）。
func TestSlotReleaseNotifyWakesCurrentWaitersAndRotatesGeneration(t *testing.T) {
	const id = 9001

	current := SlotReleaseChannel(SlotReleaseTypeAccount, id)
	select {
	case <-current:
		t.Fatal("尚未释放时不应收到通知")
	default:
	}

	NotifySlotReleased(SlotReleaseTypeAccount, id)
	select {
	case <-current:
	case <-time.After(time.Second):
		t.Fatal("槽位释放后没有唤醒等待者")
	}

	next := SlotReleaseChannel(SlotReleaseTypeAccount, id)
	if next == current {
		t.Fatal("通知后应换成新一代 channel，否则等待者会一直空转")
	}
	select {
	case <-next:
		t.Fatal("新一代 channel 不应一开始就是「已通知」状态")
	default:
	}

	// 通知必须按 (slotType,id) 精确匹配：别的槽位释放不能唤醒本槽位等待者。
	other := SlotReleaseChannel(SlotReleaseTypeUser, id)
	NotifySlotReleased(SlotReleaseTypeUser, id+1)
	select {
	case <-other:
		t.Fatal("通知了别的槽位，却唤醒了本槽位等待者")
	case <-time.After(20 * time.Millisecond):
	}
}

// 无等待者时通知是安全空操作（不能 panic），批量通知按账号/用户去重且必须唤醒等待者。
func TestSlotReleaseNotifyBatchIsSafeAndDeduped(t *testing.T) {
	NotifySlotReleased(SlotReleaseTypeAccount, 424242) // 无等待者
	notifyReleasedAccountSlots(nil)
	notifyReleasedUserSlots(nil)

	accountCh := SlotReleaseChannel(SlotReleaseTypeAccount, 7)
	userCh := SlotReleaseChannel(SlotReleaseTypeUser, 7)
	notifyReleasedAccountSlots([]AccountSlotRelease{{AccountID: 7}, {AccountID: 7}, {AccountID: 8}})
	notifyReleasedUserSlots([]UserAPIKeySlotRelease{{UserID: 7}, {UserID: 7}})

	select {
	case <-accountCh:
	case <-time.After(time.Second):
		t.Fatal("批量释放账号槽后没有唤醒账号等待者")
	}
	select {
	case <-userCh:
	case <-time.After(time.Second):
		t.Fatal("批量释放用户槽后没有唤醒用户等待者")
	}
}
