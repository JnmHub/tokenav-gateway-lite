package service

import "testing"

// 回归护栏：构造时不得再按 maxContentModerationWorkerCount(32) 预置 worker 数。
//
// 生产 content_moderation_config.worker_count = 4，而历史实现无条件起 32 个 goroutine ——
// 多出来的 28 个只会在「读运行时快照 → 睡 1s」之间空转，纯属为「运行时可能调大」预留的
// 固定开销（与队列按上限预分配那次是同一类问题）。现在按默认值起，由 workerSupervisor 补齐。
func TestContentModerationWorkerCountNotPresetToMax(t *testing.T) {
	svc := NewContentModerationService(nil, nil, nil, nil, nil, nil, nil)
	if svc == nil {
		t.Fatal("构造函数返回 nil")
	}
	if svc.workerCount == maxContentModerationWorkerCount {
		t.Fatalf("workerCount 又按上限预置为 %d，每个网关进程会白起 %d 个空转 goroutine",
			maxContentModerationWorkerCount, maxContentModerationWorkerCount-defaultContentModerationWorkerCount)
	}
	if svc.workerCount != 0 {
		t.Fatalf("workerCount 初始值 = %d，期望 0（全部由 ensureWorkers 起）", svc.workerCount)
	}
	if defaultContentModerationWorkerCount >= maxContentModerationWorkerCount {
		t.Fatalf("默认 worker 数 %d 不应达到上限 %d", defaultContentModerationWorkerCount, maxContentModerationWorkerCount)
	}
}

// ensureWorkers 的契约：只增不减（减少由 worker 自身的 id 门控处理），且按上限夹取。
func TestContentModerationEnsureWorkersGrowsOnlyAndClamps(t *testing.T) {
	svc := &ContentModerationService{}

	svc.ensureWorkers(3)
	if svc.workerCount != 3 {
		t.Fatalf("ensureWorkers(3) 后 workerCount = %d，期望 3", svc.workerCount)
	}
	// 调小：不回收 id（worker 自己按 cfg.WorkerCount 空转），保持已启动数不变。
	svc.ensureWorkers(1)
	if svc.workerCount != 3 {
		t.Fatalf("ensureWorkers(1) 后 workerCount = %d，期望仍为 3（只增不减）", svc.workerCount)
	}
	// 超过上限：夹取到 max，绝不能按请求值起无限 goroutine。
	svc.ensureWorkers(maxContentModerationWorkerCount + 100)
	if svc.workerCount != maxContentModerationWorkerCount {
		t.Fatalf("ensureWorkers(超上限) 后 workerCount = %d，期望夹取到 %d",
			svc.workerCount, maxContentModerationWorkerCount)
	}
	// 幂等：再调一次不应继续增长。
	svc.ensureWorkers(maxContentModerationWorkerCount)
	if svc.workerCount != maxContentModerationWorkerCount {
		t.Fatalf("重复 ensureWorkers 后 workerCount = %d，期望仍为 %d",
			svc.workerCount, maxContentModerationWorkerCount)
	}
}

// 补齐依赖运行时设置；拿不到设置仓库时必须是安全的空操作（不能 panic、不能凭空起 worker）。
func TestContentModerationReconcileWithoutSettingRepoIsNoop(t *testing.T) {
	svc := &ContentModerationService{}
	svc.reconcileWorkerCount()
	if svc.workerCount != 0 {
		t.Fatalf("无设置仓库时不应起 worker，实际 workerCount = %d", svc.workerCount)
	}
}
