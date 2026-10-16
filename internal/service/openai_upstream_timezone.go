package service

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// upstreamTimezoneTagPattern 匹配 OpenAI 请求体里客户端注入的环境时区声明。
// Codex / 兼容客户端会在 input 首条消息的 <environment_context> 块里携带
// <timezone>...</timezone>（内容为 IANA 时区名，可能带首尾空白）。
// 大小写不敏感、不跨标签匹配，避免误伤正文。
var upstreamTimezoneTagPattern = regexp.MustCompile(`(?i)<timezone>\s*[^<]*?</timezone>`)

// validUpstreamTimezones 缓存通过 LoadLocation 校验的时区名。
// Go 的 time.LoadLocation 每次都会读 tzdata，热路径上必须缓存。
var validUpstreamTimezones sync.Map

func isValidUpstreamTimezone(name string) bool {
	if _, ok := validUpstreamTimezones.Load(name); ok {
		return true
	}
	if _, err := time.LoadLocation(name); err != nil {
		return false
	}
	validUpstreamTimezones.Store(name, struct{}{})
	return true
}

// rewriteUpstreamTimezone 把 body 中的 <timezone> 环境声明统一改写为 timezone。
// 设计约束：
//   - timezone 为空 → 原样返回（默认透传，行为不变）；
//   - 非法时区名（LoadLocation 失败）→ 原样返回，绝不因配置错误改坏请求体；
//   - 只替换 <timezone> 标签本身，正文/代码片段里出现的同名文本不受影响
//     （标签必须完整闭合且中间不含 '<'）。
//
// 背景：客户端本地时区（如 Asia/Shanghai）与网关出口 IP 地理位置（机房常在
// 海外）不一致，是上游降智风控的候选维度；账号配置出口时区后应与之对齐。
func rewriteUpstreamTimezone(body []byte, timezone string) []byte {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || len(body) == 0 {
		return body
	}
	if strings.ContainsAny(timezone, "<>&\"'") || !isValidUpstreamTimezone(timezone) {
		return body
	}
	replacement := []byte("<timezone>" + timezone + "</timezone>")
	return upstreamTimezoneTagPattern.ReplaceAllFunc(body, func([]byte) []byte {
		// ReplaceAllFunc 返回的切片会被复用，必须拷贝常量帧。
		return append([]byte(nil), replacement...)
	})
}

// rewriteUpstreamTimezoneFromAccount 按账号配置的出口时区改写请求体。
// 三条 OpenAI 出站链路（Responses 主链路 / passthrough / raw chat completions）
// 统一在构造上游请求前调用。
//
// 时区取值优先级：credentials.upstream_timezone（手动配置）>
// 后台自动探测的出口时区（OpenAIUpstreamTimezoneSyncService，纯内存读）>
// 不改写（原样透传）。
func rewriteUpstreamTimezoneFromAccount(account *Account, body []byte) []byte {
	if account == nil || len(body) == 0 {
		return body
	}
	timezone := account.GetUpstreamTimezone()
	if timezone == "" {
		timezone = lookupAutoUpstreamTimezone(account.ID)
	}
	return rewriteUpstreamTimezone(body, timezone)
}
