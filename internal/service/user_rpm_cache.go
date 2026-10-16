package service

import "context"

// UserRPMCache 用户/分组级 RPM 计数器接口。
//
// 与账号级 RPMCache 的区别：
//   - RPMCache    —— 按外部 AI provider 账号聚合（key: rpm:{accountID}:{min}）。
//   - UserRPMCache —— 按用户或 (用户, 分组) 聚合，杜绝"同一用户创建多个 API Key 绕过 RPM"的路径。
//     Redis 使用 rate:user:{userID} Hash，分钟与计数维度放在 field 中。
type UserRPMCache interface {
	// IncrementUserGroupRPM 原子递增 (user, group) 级分钟计数并返回最新值。
	// 用于分组 rpm_limit 与 user-group rpm_override 两种命中分支。
	IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// IncrementUserRPM 原子递增用户级分钟计数并返回最新值。
	// 用于用户全局 rpm_limit 兜底分支（分组未设且无 override 时）。
	IncrementUserRPM(ctx context.Context, userID int64) (count int, err error)

	// GetUserGroupRPM 获取 (user, group) 当前分钟已用 RPM（只读，不递增）。
	GetUserGroupRPM(ctx context.Context, userID, groupID int64) (count int, err error)

	// GetUserRPM 获取用户当前分钟已用 RPM（只读，不递增）。
	GetUserRPM(ctx context.Context, userID int64) (count int, err error)

	// AddUserTPM 原子增加用户当前分钟预估 token 数并返回最新累计值。
	AddUserTPM(ctx context.Context, userID int64, tokens int) (count int, err error)

	// GetUserTPM 获取用户当前分钟已用预估 token 数（只读）。
	GetUserTPM(ctx context.Context, userID int64) (count int, err error)
}

// UserRPMTPMCombinedCache 是 UserRPMCache 的可选高并发扩展。
// 实现方必须在同一次 Redis 原子操作中先递增用户 RPM；仅 RPM 未超限时才递增 TPM，
// 这样既减少网络往返，也不会让被 RPM 拒绝的请求污染 TPM 计数。
type UserRPMTPMCombinedCache interface {
	IncrementUserRPMAndTPM(ctx context.Context, userID int64, rpmLimit int, tokens int) (rpmCount int, tpmCount int, err error)
}

// UserGroupRPMTPMCombinedCache 是最终分组 RPM、用户 RPM 与 TPM 的单次原子门禁。
// 三类计数本来就在同一个 user hash 中；实现方必须保持原有顺序：分组超限时
// 不污染用户 RPM/TPM，用户 RPM 超限时不污染 TPM。Redis 故障仍由调用方统一
// fail-open（失败放行），不能把限流缓存故障升级成请求失败。
type UserGroupRPMTPMCombinedCache interface {
	IncrementUserGroupRPMAndUserRPMTPM(
		ctx context.Context,
		userID int64,
		groupID int64,
		groupRPMLimit int,
		userRPMLimit int,
		tokens int,
	) (groupRPMCount int, userRPMCount int, userTPMCount int, err error)
}

// OrganizationMemberGroupRPMTPMCombinedCache 是企业请求的完整分钟门禁。
// organizationID 使用共享 Hash，所有员工共同消耗企业 RPM/TPM；成员、Key
// 与分组限制保存在该企业内的成员 Hash。实现方必须先检查全部上限，再统一递增，
// 被拒请求不得污染任何一个计数。
type OrganizationMemberGroupRPMTPMCombinedCache interface {
	IncrementOrganizationMemberGroupRPMAndTPM(
		ctx context.Context,
		organizationID int64,
		organizationRPMLimit int,
		organizationTPMLimit int,
		userID int64,
		memberRPMLimit int,
		memberTPMLimit int,
		groupID int64,
		groupRPMLimit int,
		apiKeyID int64,
		apiKeyRPMLimit int,
		apiKeyTPMLimit int,
		tokens int,
	) (organizationRPMCount, organizationTPMCount, memberRPMCount, memberTPMCount, groupRPMCount, rejectionReason int, err error)
}
