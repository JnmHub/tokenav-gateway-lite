// [tokenav 定制] 保留本地实现，官方 v0.2.5 对应改动见 commit 说明。
//
// 本文件承载 tokenav 对 Codex 官方客户端「粗粒度家族」判定的导出 API。
// 官方 v0.2.5 把同等能力内联为 request.go 中的小写私有函数/常量
// （codexOfficialClientUAPrefixes / codexOfficialClientOriginators /
// codexOfficialClientFamilyPrefix），不再对外导出。
//
// 但 tokenav 的核心定制包 internal/codexadmission/runtime.go 依赖以下 3 个
// **导出** 函数（runtime.go:397 / :400 / :438）：
//   - CodexOfficialClientFamilyFromUserAgent
//   - CodexOfficialClientFamilyFromOriginator
//   - IsClaudeCodeClientClaim（定义于 allowed_client.go）
//
// 因此这里以兼容层的形式保留原语义与导出状态，内部改为复用官方 v0.2.5 已加固的
// 数据表与匹配函数，避免维护两份可能漂移的客户端清单。
package openai

import "strings"

// Codex 客户端家族常量：tokenav 用粗粒度家族做全局策略分发（与 codex_cli_only
// 的逐前缀判定解耦）。官方 v0.2.5 未导出等价常量，故在此保留。
const (
	CodexClientFamilyDesktop = "desktop"
	CodexClientFamilyCLI     = "cli"
	CodexClientFamilyVSCode  = "vscode"
)

// CodexOfficialClientFamilyFromUserAgent 返回 User-Agent 所属的官方客户端家族。
// 与 codex_cli_only 使用同一套官方身份集合，但归并为稳定的粗粒度家族，
// 供全局策略（如 codexadmission 准入）使用；无法识别时返回空字符串。
func CodexOfficialClientFamilyFromUserAgent(userAgent string) string {
	ua := normalizeCodexClientHeader(userAgent)
	switch {
	case matchCodexFamilyUserAgentPrefixes(ua, codexVSCodeUserAgentPrefixes):
		return CodexClientFamilyVSCode
	case matchCodexFamilyUserAgentPrefixes(ua, codexDesktopUserAgentPrefixes):
		return CodexClientFamilyDesktop
	case matchCodexFamilyUserAgentPrefixes(ua, codexTerminalUserAgentPrefixes):
		return CodexClientFamilyCLI
	default:
		return ""
	}
}

// matchCodexFamilyUserAgentPrefixes 保留形如 "codex " 的尾随空格语义。
// 遗留的通用匹配器会 TrimSpace 掉前缀中的空格，从而把该身份退化成宽泛的
// "codex" 前缀，导致未知的 codex 类 UA 被误判为 CLI 家族。
func matchCodexFamilyUserAgentPrefixes(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.ToLower(prefix)
		if prefix != "" && (strings.HasPrefix(value, prefix) || strings.Contains(value, prefix)) {
			return true
		}
	}
	return false
}

// CodexOfficialClientFamilyFromOriginator 使用与 request_identity.go 相同的
// 已知官方身份集合推导家族。未知的 codex 类字符串不会被提升为某个家族，
// 避免单个可伪造的 originator 直接选中一条允许规则。
func CodexOfficialClientFamilyFromOriginator(originator string) string {
	value := normalizeCodexClientHeader(originator)
	switch value {
	case "codex_vscode", "codex_vscode_copilot":
		return CodexClientFamilyVSCode
	case "codex_app", "codex_chatgpt_desktop", "codex_atlas", "codex desktop":
		return CodexClientFamilyDesktop
	case "codex_cli_rs", "codex-tui", "codex_exec", "codex_sdk_ts":
		return CodexClientFamilyCLI
	default:
		if strings.HasPrefix(value, codexIdentityFamilyPrefix) {
			return CodexClientFamilyCLI
		}
		return ""
	}
}

// tokenav 家族判定所用的 UA 前缀表。
// 说明：官方 v0.2.5 的 codexOfficialClientUAPrefixes 是「官方身份」总表（用于
// 逐字透传判定），粒度与家族划分不同，故这里保留 tokenav 自己的家族划分表。
var (
	codexVSCodeUserAgentPrefixes = []string{
		"codex_vscode/",
		"codex_vscode_copilot/",
	}
	codexDesktopUserAgentPrefixes = []string{
		"codex_app/",
		"codex_chatgpt_desktop/",
		"codex_atlas/",
		"codex desktop/",
	}
	codexTerminalUserAgentPrefixes = []string{
		"codex_cli_rs/",
		"codex-tui/",
		"codex_exec/",
		"codex_sdk_ts/",
		"codex ",
	}
)
