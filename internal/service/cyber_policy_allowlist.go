package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// [官方 v0.2.13 移植] cyber 风控「用户白名单」（官方称 log-only）。
//
// 为什么需要它：cyber（网络攻击类）命中会写会话屏蔽键，用户只能干等到 TTL 过期，
// 运营无法逐个放行；误判时体验很差。白名单里的用户改为「只记录、不拦截」：
//   1) 已有会话屏蔽键对其不生效（准入直接放行）；
//   2) 新的 cyber 命中不参与自动封号判定，日志 mode 记为 cyber_log_only。
//
// 语义与官方一致：白名单是「平台用户 ID 列表」，用逗号或空白分隔；**任一非法项
// 整表拒绝**（绝不能部分生效，否则运营以为加进去了却没生效）。

// ContentModerationModeCyberLogOnly 标记这类 cyber 日志行：保留上游证据，但
// 永远不参与后续自动封号计数（即使之后把用户移出白名单也不再追溯计数）。
const ContentModerationModeCyberLogOnly = "cyber_log_only"

// ParseCyberPolicyUserAllowlist 解析正整数用户 ID 列表（逗号或空白分隔）。
// 非法项整表报错；空串返回空集合。
func ParseCyberPolicyUserAllowlist(raw string) (map[int64]struct{}, error) {
	if len(raw) > 16384 {
		return nil, fmt.Errorf("cyber_policy_user_allowlist must not exceed 16384 bytes")
	}
	ids := make(map[int64]struct{})
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("cyber_policy_user_allowlist must contain positive user IDs separated by commas or whitespace")
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}

// IsCyberPolicyUserAllowlisted 判断用户是否在白名单里。
// 走 GetCyberSessionBlockRuntime 的进程内缓存（热路径零 DB 往返，~60s 生效）；
// 缓存读不到或未加载时返回 false（fail-closed：宁可多拦一次，也不静默放行）。
func (s *SettingService) IsCyberPolicyUserAllowlisted(ctx context.Context, userID int64) bool {
	if s == nil || s.settingRepo == nil || userID <= 0 {
		return false
	}
	s.GetCyberSessionBlockRuntime(ctx)
	if cached, ok := s.cyberSessionBlockRuntimeCache.Load().(*cachedCyberSessionBlockRuntime); ok && cached != nil {
		_, allowed := cached.allowlistedUsers[userID]
		return allowed
	}
	return false
}

// CyberPolicyLogOnly 等价于「该 API Key 所属用户在 cyber 白名单里」。
func (s *OpenAIGatewayService) CyberPolicyLogOnly(ctx context.Context, apiKey *APIKey) bool {
	return s != nil && apiKey != nil && s.settingService.IsCyberPolicyUserAllowlisted(ctx, apiKey.UserID)
}
