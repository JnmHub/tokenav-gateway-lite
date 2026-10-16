package service

// [tokenav 定制] 本文件自官方 v0.2.5 引入（官方 gateway_tool_rewrite.go / gateway_billing_header.go
// 中的新增能力）。引入原因：这些符号在我们仓库中不存在，属于纯新增，无重复定义风险。
//
// 与本地实现的关系（重要）：
//   - 我们的 gateway_tool_rewrite.go 已有 toolDeferredLoading()，语义比官方更严格
//     （只认 JSON boolean true）。官方 isDeferredLoadingTool 用 gjson.True 判断，
//     实际等价，但为避免重复定义，这里不重复引入官方同名函数。
//   - 我们的 applyToolsLastCacheBreakpoint() 只在「末尾连续延迟工具」范围内剥离
//     cache_control（遇到最后一个可立即加载的工具就 break）。官方
//     stripDeferredToolCacheControl() 则会扫描全部工具、剥掉每一个延迟工具上的
//     cache_control——覆盖了我们遗漏的「中间位置延迟工具」场景。两者互补，
//     故以新函数引入，不修改我们的既有实现。

import (
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// stripDeferredToolCacheControl 移除所有 deferred 工具上的 cache_control 标记。
//
// 为什么必须剥：延迟加载的工具首轮并不可用，Anthropic 认为其上带 cache 断点是
// 非法请求，会导致整个请求 400。我们的 applyToolsLastCacheBreakpoint 只处理了
// 尾部连续段，中间的延迟工具会漏掉，因此补上全量扫描。
func stripDeferredToolCacheControl(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	for idx, tool := range tools.Array() {
		if !toolDeferredLoading(tool) || !tool.Get("cache_control").Exists() {
			continue
		}
		if next, err := sjson.DeleteBytes(body, fmt.Sprintf("tools.%d.cache_control", idx)); err == nil {
			body = next
		}
	}
	return body
}

// effectiveBillingUserAgent 决定计费头同步时使用的 User-Agent。
//
// 逻辑：oauth + 伪装 Claude Code 时用 Claude Code 官方 UA（保证 cc_version 与
// billing header 一致）；否则用客户端指纹的 UA；没有指纹则返回空串表示不同步。
func effectiveBillingUserAgent(tokenType string, mimicClaudeCode bool, fingerprint *Fingerprint) string {
	if tokenType == "oauth" && mimicClaudeCode {
		return claude.DefaultHeaders["User-Agent"]
	}
	if fingerprint == nil {
		return ""
	}
	return fingerprint.UserAgent
}
