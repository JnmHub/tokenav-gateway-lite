package logger

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestCleanupStepLabelsDoNotTripLogLevelInference 守住：「清理步骤名」不能踩日志级别推断的关键词。
//
// 背景：`cmd/server` 的清理步骤统一用 `log.Printf("[Cleanup] %s succeeded", step.name)` 收尾，
// 这些行会经标准库日志桥 `inferStdLogLevel` 按关键词猜级别：
//   - 含 "error" / "panic" / "fatal" / " failed" → ERROR
//   - 含 "warn" / "warning" / "fallback" / " queue full" → WARN
//
// 因此步骤名里出现这些字样时，**成功**的清理也会被记成 ERROR/WARN。
// 真实发生过：运维错误日志排空步骤一度叫 `OpsErrorLogWorkers`，每次优雅停机都会打一条
// 假 ERROR（污染错误率与告警），故改名为 `OpsErrLogQueueDrain`。
//
// 用例直接扫描 `cmd/server/wire.go`（步骤定义的唯一来源，wire_gen.go 由它生成），
// 以后新增/改名清理步骤踩雷会立刻失败。
func TestCleanupStepLabelsDoNotTripLogLevelInference(t *testing.T) {
	// 先把规则本身钉住，避免本用例在规则变化后变成空断言。
	if got := inferStdLogLevel("[Cleanup] OpsErrorLogWorkers succeeded"); got != LevelError {
		t.Fatalf("关键词规则已变化：含 error 字样的成功日志被判为 %s，请先复核本护栏", got)
	}
	if got := inferStdLogLevel("[Cleanup] OpsErrLogQueueDrain succeeded"); got != LevelInfo {
		t.Fatalf("正常清理步骤名应判为 INFO，实际 %s", got)
	}

	src, err := os.ReadFile(filepath.Join("..", "..", "..", "cmd", "server", "wire.go"))
	if err != nil {
		t.Fatalf("读取 cmd/server/wire.go 失败（本护栏依赖该文件位置）: %v", err)
	}

	labelRe := regexp.MustCompile(`\{"([A-Za-z0-9_]+)", func\(\) error \{`)
	matches := labelRe.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("没有在 cmd/server/wire.go 里匹配到清理步骤标签，本护栏已失效（正则或写法变了）")
	}

	for _, m := range matches {
		label := m[1]
		msg := "[Cleanup] " + label + " succeeded"
		if got := inferStdLogLevel(msg); got != LevelInfo {
			t.Errorf("清理步骤名 %q 会让成功日志 %q 被判为 %s 级；请避免 error/warn/failed/fallback 等字样",
				label, msg, got)
		}
	}
}
