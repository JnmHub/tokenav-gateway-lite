//go:build unit

// 调度快照 canonical 桶计数 helper：自官方 0.2.7 移植
// （官方定义于 scheduler_snapshot_service.go / scheduler_snapshot_group_lifecycle_test.go，
// 本仓库生产代码未包含这两个计数函数，仅供 unit 测试断言使用）。
package service

// schedulerCanonicalBucketCount 返回所有平台的 canonical 调度桶总数：
// 每个平台 single + forced 两桶，anthropic/gemini 额外一个 mixed 桶。
func schedulerCanonicalBucketCount() int {
	count := 0
	for _, platform := range schedulerSnapshotPlatforms() {
		count += 2
		if platform == PlatformAnthropic || platform == PlatformGemini {
			count++
		}
	}
	return count
}
