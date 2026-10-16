// [tokenav 定制] 保留本地实现，官方 v0.2.5 对应改动见 commit 说明。
//
// 本文件是 tokenav 的 Codex 客户端身份配对实现，被
// internal/service/openai_codex_identity.go 直接引用（3 处调用）。
// 官方 v0.2.5 已把同等能力内联进 request.go 的 PairCodexClientIdentity，
// 并在其上叠加了更严格的安全校验（validCodexUserAgentValue / httpguts 头值
// 校验、`Codex ` 家族前缀保留原大小写等）。
//
// 冲突处理：同一个 PairCodexClientIdentity 符号不能声明两次，按「我们的定制
// 优先」保留本文件实现；并把 isSaneCodexIdentity / canonicalizeCodexIdentity /
// isOfficialCodexIdentity 委托到官方在 request.go 中新增的加固逻辑
// （isSaneCodexOriginator / canonicalizeCodexOriginator /
// IsCodexOfficialClientOriginator），使本地实现同时获得官方 v0.2.5 的安全修复，
// 对外行为对调用方保持不变。
package openai

import "strings"

// codexIdentityFamilyPrefix 说明：Codex 官方客户端除精确集合外，还存在
// `Codex ` 前缀家族（含真实大小写），故保留此前缀判定。
const codexIdentityFamilyPrefix = "codex "

// PairCodexClientIdentity 根据最终出站 User-Agent 生成与之配套的 originator。
// ChatGPT Codex 上游会校验两者是否一致；错配时会返回 404。
//
// CODEX_INTERNAL_ORIGINATOR_OVERRIDE 可能只改写 User-Agent 首段，因此当首段不是
// 官方身份时，还会从末尾的 `(client; version)` 中恢复真实客户端名称并重写首段。
func PairCodexClientIdentity(userAgent string) (originator string, pairedUA string, ok bool) {
	// 先做官方 v0.2.5 的头值校验，拒绝控制字节等非法 UA，避免其被当成有效身份。
	if !validCodexUserAgentValue(userAgent) {
		return "", "", false
	}
	ua := strings.TrimSpace(userAgent)
	slash := strings.IndexByte(ua, '/')
	if slash <= 0 {
		return "", "", false
	}

	if leading := strings.TrimSpace(ua[:slash]); isSaneCodexIdentity(leading) && isOfficialCodexIdentity(leading) {
		leading = canonicalizeCodexIdentity(leading)
		return leading, leading + ua[slash:], true
	}

	trailer := codexIdentityTrailerName(ua)
	if trailer == "" || strings.ContainsRune(trailer, '/') || !isSaneCodexIdentity(trailer) || !isOfficialCodexIdentity(trailer) {
		return "", "", false
	}
	trailer = canonicalizeCodexIdentity(trailer)
	return trailer, trailer + ua[slash:], true
}

func codexIdentityTrailerName(userAgent string) string {
	last := strings.LastIndex(userAgent, "(")
	if last < 0 {
		return ""
	}
	rest := userAgent[last+1:]
	closeIndex := strings.Index(rest, ")")
	if closeIndex < 0 {
		return ""
	}
	name := strings.TrimSpace(rest[:closeIndex])
	if semicolon := strings.Index(name, ";"); semicolon >= 0 {
		name = strings.TrimSpace(name[:semicolon])
	}
	return name
}

// isOfficialCodexIdentity 委托官方 v0.2.5 的精确官方客户端判定。
// 身份配对必须使用精确集合，不能沿用访问识别中的宽松 Contains 匹配，
// 否则伪造的 User-Agent 也可能被当成可直接透传的官方身份。
func isOfficialCodexIdentity(name string) bool {
	return IsCodexOfficialClientOriginator(name) ||
		strings.HasPrefix(normalizeCodexClientHeader(name), codexIdentityFamilyPrefix)
}

// isSaneCodexIdentity 委托官方 v0.2.5 的可打印字符校验。
func isSaneCodexIdentity(name string) bool {
	return isSaneCodexOriginator(name)
}

// canonicalizeCodexIdentity 委托官方 v0.2.5 的大小写归一化实现。
func canonicalizeCodexIdentity(name string) string {
	return canonicalizeCodexOriginator(name)
}
