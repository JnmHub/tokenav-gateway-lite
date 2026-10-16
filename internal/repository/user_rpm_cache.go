package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 用户/分组级 RPM 计数器 Redis 实现。
//
// 设计说明：
//   - key 形式：rate:user:{uid}，当前分钟与计数类型放在 Hash field 中。
//   - 时间来源：Lua 内 Redis TIME，避免多实例时钟漂移。
//   - 原子操作：单 key Lua，兼容 Redis Cluster，并把 TIME+计数+TTL 合成一次网络往返。
//   - TTL：120s，覆盖当前分钟窗口 + 少量冗余。
//   - 返回值语义：超限判断由调用方（billing_cache_service.checkRPM）与 RPMLimit 比较完成。
const (
	userRateKeyPrefix               = "rate:user:"
	organizationRateKeyPrefix       = "rate:organization:"
	organizationMemberRateKeyPrefix = "rate:organization_member:"
	organizationKeyRateKeyPrefix    = "rate:organization_key:"
	userRPMFieldPrefix              = "rpm:"
	userTPMFieldPrefix              = "tpm:"
	userGroupRPMFieldLabel          = "group:%d:rpm:"
	userRPMKeyTTLSeconds            = 120
)

var incrementUserRateFieldScript = redis.NewScript(`
local now = redis.call('TIME')
local minute = math.floor(tonumber(now[1]) / 60)
local field = ARGV[1] .. tostring(minute)
local value = redis.call('HINCRBY', KEYS[1], field, ARGV[2])
redis.call('EXPIRE', KEYS[1], ARGV[3])
return value
`)

// 企业与成员 Hash 使用相同的 Redis Cluster hash tag，确保 Lua 可在集群模式
// 原子执行。所有限制先读取并判断，只有全部通过后才递增，拒绝请求不计数。
var incrementOrganizationMemberGroupRPMAndTPMScript = redis.NewScript(`
local now = redis.call('TIME')
local minute = math.floor(tonumber(now[1]) / 60)
local org_rpm_field = 'rpm:' .. tostring(minute)
local org_tpm_field = 'tpm:' .. tostring(minute)
local member_rpm_field = 'rpm:' .. tostring(minute)
local member_tpm_field = 'tpm:' .. tostring(minute)
local group_rpm_field = 'group:' .. ARGV[7] .. ':rpm:' .. tostring(minute)
local key_rpm_field = 'rpm:' .. tostring(minute)
local key_tpm_field = 'tpm:' .. tostring(minute)
local org_rpm = tonumber(redis.call('HGET', KEYS[1], org_rpm_field) or 0)
local org_tpm = tonumber(redis.call('HGET', KEYS[1], org_tpm_field) or 0)
local member_rpm = tonumber(redis.call('HGET', KEYS[2], member_rpm_field) or 0)
local member_tpm = tonumber(redis.call('HGET', KEYS[2], member_tpm_field) or 0)
local group_rpm = tonumber(redis.call('HGET', KEYS[2], group_rpm_field) or 0)
local key_rpm = tonumber(redis.call('HGET', KEYS[3], key_rpm_field) or 0)
local key_tpm = tonumber(redis.call('HGET', KEYS[3], key_tpm_field) or 0)
local org_rpm_limit = tonumber(ARGV[1]) or 0
local org_tpm_limit = tonumber(ARGV[2]) or 0
local member_rpm_limit = tonumber(ARGV[3]) or 0
local member_tpm_limit = tonumber(ARGV[4]) or 0
local group_rpm_limit = tonumber(ARGV[5]) or 0
local tokens = tonumber(ARGV[6]) or 0
local key_rpm_limit = tonumber(ARGV[9]) or 0
local key_tpm_limit = tonumber(ARGV[10]) or 0
if org_rpm_limit > 0 and org_rpm + 1 > org_rpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 1} end
if org_tpm_limit > 0 and org_tpm + tokens > org_tpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 2} end
if member_rpm_limit > 0 and member_rpm + 1 > member_rpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 3} end
if member_tpm_limit > 0 and member_tpm + tokens > member_tpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 4} end
if group_rpm_limit > 0 and group_rpm + 1 > group_rpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 5} end
if key_rpm_limit > 0 and key_rpm + 1 > key_rpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 6} end
if key_tpm_limit > 0 and key_tpm + tokens > key_tpm_limit then return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 7} end
org_rpm = redis.call('HINCRBY', KEYS[1], org_rpm_field, 1)
member_rpm = redis.call('HINCRBY', KEYS[2], member_rpm_field, 1)
key_rpm = redis.call('HINCRBY', KEYS[3], key_rpm_field, 1)
if tokens > 0 then
  org_tpm = redis.call('HINCRBY', KEYS[1], org_tpm_field, tokens)
  member_tpm = redis.call('HINCRBY', KEYS[2], member_tpm_field, tokens)
  key_tpm = redis.call('HINCRBY', KEYS[3], key_tpm_field, tokens)
end
if group_rpm_limit > 0 then group_rpm = redis.call('HINCRBY', KEYS[2], group_rpm_field, 1) end
redis.call('EXPIRE', KEYS[1], ARGV[8])
redis.call('EXPIRE', KEYS[2], ARGV[8])
redis.call('EXPIRE', KEYS[3], ARGV[8])
return {org_rpm, org_tpm, member_rpm, member_tpm, group_rpm, key_rpm, key_tpm, 0}
`)

var getUserRateFieldScript = redis.NewScript(`
local now = redis.call('TIME')
local minute = math.floor(tonumber(now[1]) / 60)
local field = ARGV[1] .. tostring(minute)
local value = redis.call('HGET', KEYS[1], field)
if not value then
  return 0
end
return tonumber(value)
`)

var incrementUserRPMAndTPMScript = redis.NewScript(`
local now = redis.call('TIME')
local minute = math.floor(tonumber(now[1]) / 60)
local rpm_field = 'rpm:' .. tostring(minute)
local tpm_field = 'tpm:' .. tostring(minute)
local rpm = redis.call('HINCRBY', KEYS[1], rpm_field, 1)
redis.call('EXPIRE', KEYS[1], ARGV[3])

if rpm > tonumber(ARGV[1]) then
  local current_tpm = redis.call('HGET', KEYS[1], tpm_field)
  if not current_tpm then
    current_tpm = 0
  end
  return {rpm, tonumber(current_tpm)}
end

local tpm = redis.call('HINCRBY', KEYS[1], tpm_field, ARGV[2])
return {rpm, tpm}
`)

// incrementUserGroupRPMAndUserRPMTPMScript 把最终分组 RPM、用户 RPM 与 TPM
// 合进同一个 user hash Lua。它只合并同属 fail-open（失败放行）的限流计数，
// 不跨越 fail-closed（失败关闭）的额度租约或并发槽位边界。
var incrementUserGroupRPMAndUserRPMTPMScript = redis.NewScript(`
local now = redis.call('TIME')
local minute = math.floor(tonumber(now[1]) / 60)
local group_rpm_field = 'group:' .. ARGV[1] .. ':rpm:' .. tostring(minute)
local user_rpm_field = 'rpm:' .. tostring(minute)
local user_tpm_field = 'tpm:' .. tostring(minute)
local group_rpm = redis.call('HINCRBY', KEYS[1], group_rpm_field, 1)
redis.call('EXPIRE', KEYS[1], ARGV[5])

-- 保持旧顺序：最终分组先拒绝时，不能继续增加用户 RPM 或 TPM。
if group_rpm > tonumber(ARGV[2]) then
  local current_rpm = redis.call('HGET', KEYS[1], user_rpm_field) or 0
  local current_tpm = redis.call('HGET', KEYS[1], user_tpm_field) or 0
  return {group_rpm, tonumber(current_rpm), tonumber(current_tpm)}
end

local user_rpm = 0
local user_rpm_limit = tonumber(ARGV[3]) or 0
if user_rpm_limit > 0 then
  user_rpm = redis.call('HINCRBY', KEYS[1], user_rpm_field, 1)
  -- 保持旧顺序：用户 RPM 先拒绝时，不能继续增加 TPM。
  if user_rpm > user_rpm_limit then
    local current_tpm = redis.call('HGET', KEYS[1], user_tpm_field) or 0
    return {group_rpm, user_rpm, tonumber(current_tpm)}
  end
end

local user_tpm = 0
local tokens = tonumber(ARGV[4]) or 0
if tokens > 0 then
  user_tpm = redis.call('HINCRBY', KEYS[1], user_tpm_field, tokens)
end
return {group_rpm, user_rpm, user_tpm}
`)

type userRPMCacheImpl struct {
	rdb *redis.Client
}

// NewUserRPMCache 创建用户/分组级 RPM 计数器。
func NewUserRPMCache(rdb *redis.Client) service.UserRPMCache {
	return &userRPMCacheImpl{rdb: rdb}
}

func userRateKey(userID int64) string {
	// 花括号保证同一用户的所有速率操作固定在同一个 Redis Cluster slot。
	return fmt.Sprintf("%s{%d}", userRateKeyPrefix, userID)
}

func organizationRateKey(organizationID int64) string {
	return fmt.Sprintf("%s{%d}", organizationRateKeyPrefix, organizationID)
}

func organizationMemberRateKey(organizationID, userID int64) string {
	return fmt.Sprintf("%s{%d}:%d", organizationMemberRateKeyPrefix, organizationID, userID)
}

func organizationAPIKeyRateKey(organizationID, apiKeyID int64) string {
	return fmt.Sprintf("%s{%d}:%d", organizationKeyRateKeyPrefix, organizationID, apiKeyID)
}

func (c *userRPMCacheImpl) incrementField(ctx context.Context, userID int64, fieldPrefix string, delta int) (int, error) {
	value, err := incrementUserRateFieldScript.Run(
		ctx,
		c.rdb,
		[]string{userRateKey(userID)},
		fieldPrefix,
		delta,
		userRPMKeyTTLSeconds,
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("user rate increment: %w", err)
	}
	return int(value), nil
}

func (c *userRPMCacheImpl) getField(ctx context.Context, userID int64, fieldPrefix string) (int, error) {
	value, err := getUserRateFieldScript.Run(ctx, c.rdb, []string{userRateKey(userID)}, fieldPrefix).Int64()
	if err != nil && err != redis.Nil {
		return 0, fmt.Errorf("user rate get: %w", err)
	}
	return int(value), nil
}

// IncrementUserGroupRPM 递增 (user, group) 分钟计数。
func (c *userRPMCacheImpl) IncrementUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	return c.incrementField(ctx, userID, fmt.Sprintf(userGroupRPMFieldLabel, groupID), 1)
}

// IncrementUserRPM 递增用户分钟计数。
func (c *userRPMCacheImpl) IncrementUserRPM(ctx context.Context, userID int64) (int, error) {
	return c.incrementField(ctx, userID, userRPMFieldPrefix, 1)
}

// GetUserGroupRPM 获取 (user, group) 当前分钟已用 RPM（只读）。
func (c *userRPMCacheImpl) GetUserGroupRPM(ctx context.Context, userID, groupID int64) (int, error) {
	return c.getField(ctx, userID, fmt.Sprintf(userGroupRPMFieldLabel, groupID))
}

// GetUserRPM 获取用户当前分钟已用 RPM（只读）。
func (c *userRPMCacheImpl) GetUserRPM(ctx context.Context, userID int64) (int, error) {
	return c.getField(ctx, userID, userRPMFieldPrefix)
}

func (c *userRPMCacheImpl) AddUserTPM(ctx context.Context, userID int64, tokens int) (int, error) {
	if tokens <= 0 {
		tokens = 1
	}
	return c.incrementField(ctx, userID, userTPMFieldPrefix, tokens)
}

func (c *userRPMCacheImpl) GetUserTPM(ctx context.Context, userID int64) (int, error) {
	return c.getField(ctx, userID, userTPMFieldPrefix)
}

// IncrementUserRPMAndTPM 将请求热路径里的用户 RPM 与 TPM 门禁合成一次 Redis Lua。
// 当 RPM 已超限时不递增 TPM，保持“先检查 RPM，再检查 TPM”的原业务语义。
func (c *userRPMCacheImpl) IncrementUserRPMAndTPM(ctx context.Context, userID int64, rpmLimit int, tokens int) (int, int, error) {
	values, err := incrementUserRPMAndTPMScript.Run(
		ctx,
		c.rdb,
		[]string{userRateKey(userID)},
		rpmLimit,
		tokens,
		userRPMKeyTTLSeconds,
	).Slice()
	if err != nil {
		return 0, 0, fmt.Errorf("user rpm/tpm increment: %w", err)
	}
	if len(values) != 2 {
		return 0, 0, fmt.Errorf("user rpm/tpm increment: unexpected result length %d", len(values))
	}
	rpmCount, rpmOK := values[0].(int64)
	tpmCount, tpmOK := values[1].(int64)
	if !rpmOK || !tpmOK {
		return 0, 0, fmt.Errorf("user rpm/tpm increment: unexpected result types %T/%T", values[0], values[1])
	}
	return int(rpmCount), int(tpmCount), nil
}

// IncrementUserGroupRPMAndUserRPMTPM 用一次 Redis 往返执行最终分组和用户级
// 限流。groupRPMLimit 必须大于 0；tokens=0 表示本次不启用 TPM 门禁。
func (c *userRPMCacheImpl) IncrementUserGroupRPMAndUserRPMTPM(
	ctx context.Context,
	userID int64,
	groupID int64,
	groupRPMLimit int,
	userRPMLimit int,
	tokens int,
) (int, int, int, error) {
	values, err := incrementUserGroupRPMAndUserRPMTPMScript.Run(
		ctx,
		c.rdb,
		[]string{userRateKey(userID)},
		groupID,
		groupRPMLimit,
		userRPMLimit,
		tokens,
		userRPMKeyTTLSeconds,
	).Slice()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("user group/rpm/tpm increment: %w", err)
	}
	if len(values) != 3 {
		return 0, 0, 0, fmt.Errorf("user group/rpm/tpm increment: unexpected result length %d", len(values))
	}
	groupRPMCount, groupOK := values[0].(int64)
	userRPMCount, rpmOK := values[1].(int64)
	userTPMCount, tpmOK := values[2].(int64)
	if !groupOK || !rpmOK || !tpmOK {
		return 0, 0, 0, fmt.Errorf(
			"user group/rpm/tpm increment: unexpected result types %T/%T/%T",
			values[0], values[1], values[2],
		)
	}
	return int(groupRPMCount), int(userRPMCount), int(userTPMCount), nil
}

func (c *userRPMCacheImpl) IncrementOrganizationMemberGroupRPMAndTPM(
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
) (int, int, int, int, int, int, error) {
	values, err := incrementOrganizationMemberGroupRPMAndTPMScript.Run(
		ctx,
		c.rdb,
		[]string{
			organizationRateKey(organizationID),
			organizationMemberRateKey(organizationID, userID),
			organizationAPIKeyRateKey(organizationID, apiKeyID),
		},
		organizationRPMLimit,
		organizationTPMLimit,
		memberRPMLimit,
		memberTPMLimit,
		groupRPMLimit,
		tokens,
		groupID,
		userRPMKeyTTLSeconds,
		apiKeyRPMLimit,
		apiKeyTPMLimit,
	).Slice()
	if err != nil {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("organization/member rate increment: %w", err)
	}
	if len(values) != 8 {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("organization/member rate increment: unexpected result length %d", len(values))
	}
	counts := make([]int, 5)
	for index := range counts {
		value, ok := values[index].(int64)
		if !ok {
			return 0, 0, 0, 0, 0, 0, fmt.Errorf("organization/member rate increment: unexpected result type %T", values[index])
		}
		counts[index] = int(value)
	}
	rejectionReason, ok := values[7].(int64)
	if !ok {
		return 0, 0, 0, 0, 0, 0, fmt.Errorf("organization/member/key rate increment: unexpected rejection type %T", values[7])
	}
	return counts[0], counts[1], counts[2], counts[3], counts[4], int(rejectionReason), nil
}
