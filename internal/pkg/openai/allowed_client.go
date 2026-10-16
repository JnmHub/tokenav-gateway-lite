package openai

import "strings"

// AllowedClientEntry 描述一个被额外放行的非官方 Codex 客户端签名。
// Originator 必须精确等值匹配（归一化后）。
// UAContains 为必填字段：列表为空，或列表中存在任何空白 marker，均视为非法配置，
// 整体安全失败（return false）；每一项都必须出现在 User-Agent 中。
// 这确保双因子匹配不会因缺失 UA 声明而退化为仅凭可伪造的 originator 单因子放行。
// SkipEngineFingerprint 仅对白名单条目有意义：命中此条则跳过引擎指纹门（管理员显式承担
// "纯 UA+originator、无引擎兜底"的后门风险，默认 false）。黑名单忽略此字段。
type AllowedClientEntry struct {
	Originator            string   `json:"originator"`
	UAContains            []string `json:"ua_contains"`
	SkipEngineFingerprint bool     `json:"skip_engine_fingerprint"`
}

// IsWhitelistable 报告该条目作为白名单条目是否「有可能命中」——镜像 IsAllowedClientMatch 的结构性
// 前置：originator 非空、ua_contains 至少一项、且无任何空白 marker（空白 marker 会让整条永不命中）。
// 仅供管理端写入校验，避免存入静默失效的白名单规则。黑名单（OR 宽 deny，允许 originator-only）不受此约束。
func (e AllowedClientEntry) IsWhitelistable() bool {
	if normalizeCodexClientHeader(e.Originator) == "" {
		return false
	}
	if len(e.UAContains) == 0 {
		return false
	}
	for _, marker := range e.UAContains {
		if normalizeCodexClientHeader(marker) == "" {
			return false
		}
	}
	return true
}

// IsAllowedClientMatch 判断请求头是否命中给定的额外客户端签名。
// originator 必须精确等值（归一化后）；UAContains 中每一项都必须出现在 UA 中。
// UAContains 为必填：列表为空或含任何空白 marker 均视为非法配置，整体安全失败。
func IsAllowedClientMatch(userAgent, originator string, entry AllowedClientEntry) bool {
	wantOriginator := normalizeCodexClientHeader(entry.Originator)
	if wantOriginator == "" {
		return false
	}
	if normalizeCodexClientHeader(originator) != wantOriginator {
		return false
	}
	// 预设必须声明 UA 特征：否则将退化为仅凭可伪造的 originator 单因子匹配。
	if len(entry.UAContains) == 0 {
		return false
	}
	ua := normalizeCodexClientHeader(userAgent)
	for _, marker := range entry.UAContains {
		normalizedMarker := normalizeCodexClientHeader(marker)
		if normalizedMarker == "" {
			// 空白 marker 让该项失去校验能力，会让双因子退化为仅 originator
			// 单因子；视为非法配置，安全失败。
			return false
		}
		if !strings.Contains(ua, normalizedMarker) {
			return false
		}
	}
	return true
}

// MatchClientEntry 同 MatchClientEntries（双因子 AND，复用 IsAllowedClientMatch），但回传命中的
// 那条条目，供调用方读取 SkipEngineFingerprint 等条目级配置。未命中返回零值 + false。
func MatchClientEntry(userAgent, originator string, entries []AllowedClientEntry) (AllowedClientEntry, bool) {
	for _, e := range entries {
		if IsAllowedClientMatch(userAgent, originator, e) {
			return e, true
		}
	}
	return AllowedClientEntry{}, false
}

// MatchClientEntries 判断请求头是否命中任一白名单自由条目（双因子 AND）。薄封装 MatchClientEntry。
// 用于 codex_cli_only 全局白名单：放行官方集未覆盖的 app-server 新 client。
func MatchClientEntries(userAgent, originator string, entries []AllowedClientEntry) bool {
	_, ok := MatchClientEntry(userAgent, originator, entries)
	return ok
}

// IsDeniedClientMatch 黑名单单条 OR 语义：已声明字段中任一命中即 deny。
// originator 精确等值命中，或任一非空 ua_contains marker 出现在 UA 中。
// 全空字段（originator 与 ua_contains 均空）→ 不 deny（安全忽略）。
// 与白名单 AND 非对称：deny 应宽（挡可疑），allow 应严（防伪造）。
func IsDeniedClientMatch(userAgent, originator string, entry AllowedClientEntry) bool {
	if want := normalizeCodexClientHeader(entry.Originator); want != "" {
		if normalizeCodexClientHeader(originator) == want {
			return true
		}
	}
	ua := normalizeCodexClientHeader(userAgent)
	for _, marker := range entry.UAContains {
		if m := normalizeCodexClientHeader(marker); m != "" && strings.Contains(ua, m) {
			return true
		}
	}
	return false
}

// MatchDenyEntries 判断请求头是否命中任一黑名单条目（OR）。
func MatchDenyEntries(userAgent, originator string, entries []AllowedClientEntry) bool {
	for _, e := range entries {
		if IsDeniedClientMatch(userAgent, originator, e) {
			return true
		}
	}
	return false
}

// IsClaudeCodeClientClaim 报告两个身份头中是否有任一宣称自己是内置的 Claude Code 客户端。
//
// [tokenav 定制] 保留本地实现，官方 v0.2.5 对应改动见 commit 说明。
// 官方 v0.2.5 移除了内置客户端注册表（改为配置驱动），不再提供该导出函数；
// 但 tokenav 的核心定制包 internal/codexadmission/runtime.go:438 依赖它。
// 故在此以自包含形式保留，语义与 tokenav 原实现完全一致。
//
// 判定刻意比白名单匹配更宽：白名单是正向放行（要求 UA + originator 双因子精确命中），
// 而这里是负向保护——防止「部分宣称 Claude Code」的身份被提升为未列名的 Codex App Server。
// 真正能否放行 Claude Code，仍由账号级的命名客户端策略单独决定。
func IsClaudeCodeClientClaim(userAgent, originator string) bool {
	ua := normalizeCodexClientHeader(userAgent)
	origin := normalizeCodexClientHeader(originator)
	if origin == "claude code" {
		return true
	}
	return strings.HasPrefix(ua, "claude code/") ||
		(strings.HasPrefix(ua, "claude-cli/") && strings.Contains(ua, "claude code"))
}

// [v0.2.5 新增]
// AllowedClientClaudeCode 是 Claude Code codex 插件的客户端预设名。
const AllowedClientClaudeCode = "claude-code"

// namedAllowedClientPresets 是命名客户端预设注册表：预设键 → 双因子签名条目。
// 配置（账号级 codex_cli_only_allowed_clients / 全局放行列表）只能引用这里的
// 预设键，不能自定义签名规则——避免管理员误配出"仅凭可伪造 originator 放行"的
// 单因子后门。匹配语义统一复用 IsAllowedClientMatch（originator 精确等值 +
// UA 包含全部 marker）。
var namedAllowedClientPresets = map[string]AllowedClientEntry{
	AllowedClientClaudeCode: {
		Originator: "Claude Code",
		UAContains: []string{"Claude Code"},
	},
}

// normalizeAllowedClientPresetName 归一化预设键：小写 + 去空白，并把下划线、
// 空格统一折叠为横线（"claude_code"/"claude code"/"claude-code" 等价），
// 兼容前端/存量配置对同一预设的不同书写习惯。
func normalizeAllowedClientPresetName(name string) string {
	normalized := normalizeCodexClientHeader(name)
	normalized = strings.ReplaceAll(normalized, "_", "-")
	return strings.ReplaceAll(normalized, " ", "-")
}

// AllowedClientPresetEntry 返回预设键对应的签名条目；未知预设键返回 false。
func AllowedClientPresetEntry(name string) (AllowedClientEntry, bool) {
	entry, ok := namedAllowedClientPresets[normalizeAllowedClientPresetName(name)]
	return entry, ok
}

// MatchNamedAllowedClients 判断请求头是否命中任一命名预设（双因子 AND）。
// 未注册的预设键安全忽略（不会放行），与白名单"应严不放宽"的口径一致。
func MatchNamedAllowedClients(userAgent, originator string, names []string) bool {
	for _, name := range names {
		if entry, ok := AllowedClientPresetEntry(name); ok && IsAllowedClientMatch(userAgent, originator, entry) {
			return true
		}
	}
	return false
}
