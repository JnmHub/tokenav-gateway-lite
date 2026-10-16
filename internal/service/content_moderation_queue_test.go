package service

import (
	"testing"
	"time"
)

// 回归护栏：内容审核队列缓冲不得再按 maxContentModerationQueueSize(100000) 预分配。
//
// 该缓冲在构造时无条件分配，与内容审核是否启用无关（生产实测 enabled=false 仍占用）；
// 单个 contentModerationTask 约 416 字节，按上限分配约 40MB。
// 而入队准入用的是运行时设置 cfg.QueueSize（默认 32768），默认配置下
// 32768 以上的槽位永远不可达 —— 即那部分是纯浪费。
func TestContentModerationQueueCapacityUsesDefaultNotMax(t *testing.T) {
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	if svc == nil || svc.asyncQueue == nil {
		t.Fatal("asyncQueue 未初始化")
	}
	got := cap(svc.asyncQueue)
	if got == maxContentModerationQueueSize {
		t.Fatalf("队列容量退回上限 %d，每个网关进程会白占约 40MB", maxContentModerationQueueSize)
	}
	if got != defaultContentModerationQueueSize {
		t.Fatalf("队列容量 = %d，期望 %d（默认准入深度）", got, defaultContentModerationQueueSize)
	}
}

// 缓冲容量必须容纳默认准入深度，否则「未满就丢弃」的语义会退化。
func TestContentModerationBufferFitsDefaultAdmissionLimit(t *testing.T) {
	if defaultContentModerationQueueSize > maxContentModerationQueueSize {
		t.Fatal("默认队列深度不应大于配置上限")
	}
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	if defaultContentModerationQueueSize > cap(svc.asyncQueue) {
		t.Fatalf("默认准入深度 %d 超过缓冲容量 %d", defaultContentModerationQueueSize, cap(svc.asyncQueue))
	}
}

// 队列满时必须是「丢弃并计数」，绝不能阻塞请求路径。
// 这条断言直接支撑构造处注释里「超出缓冲走既有 default 分支丢弃」的说法。
func TestContentModerationEnqueueDropsInsteadOfBlockingWhenFull(t *testing.T) {
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	cfg := &ContentModerationConfig{QueueSize: defaultContentModerationQueueSize}

	for i := 0; i < cap(svc.asyncQueue); i++ {
		svc.asyncQueue <- contentModerationTask{}
	}
	before := svc.asyncDropped.Load()

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.enqueueAsync(ContentModerationCheckInput{UserID: 1}, cfg, ContentModerationInput{}, "hash")
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("队列已满时 enqueueAsync 阻塞了请求路径（应改为丢弃）")
	}

	if svc.asyncDropped.Load() <= before {
		t.Fatal("队列满时未记录丢弃计数")
	}
}
