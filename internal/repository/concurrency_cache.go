package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 并发控制缓存常量定义
//
// 性能优化说明：
// 原实现使用 SCAN 命令遍历独立的槽位键（concurrency:account:{id}:{requestID}），
// 在高并发场景下 SCAN 需要多次往返，且遍历大量键时性能下降明显。
//
// 新实现改用 Redis 有序集合（Sorted Set）：
// 1. 每个账号/用户只有一个键，成员为 requestID，分数为时间戳
// 2. 使用 ZCARD 原子获取并发数，时间复杂度 O(1)
// 3. 使用 ZREMRANGEBYSCORE 清理过期槽位，避免手动管理 TTL
// 4. 单次 Redis 调用完成计数，减少网络往返
const (
	// 并发槽位键前缀（有序集合）
	// 格式: concurrency:account:{accountID}
	accountSlotKeyPrefix = "concurrency:account:"
	// 格式: concurrency:user:{userID}
	userSlotKeyPrefix = "concurrency:user:"
	// 格式: concurrency:group_user:{groupID}:{userID}
	groupUserSlotKeyPrefix = "concurrency:group_user:"
	// 企业槽位按 organizationID 跨成员共享；成员槽位带企业前缀，避免同一用户
	// 在两个企业中的请求互相占用。
	organizationSlotKeyPrefix            = "concurrency:organization:"
	organizationMemberSlotKeyPrefix      = "concurrency:organization_member:"
	organizationGroupMemberSlotKeyPrefix = "concurrency:organization_group_member:"
	// 格式: concurrency:api_key:{apiKeyID}
	apiKeySlotKeyPrefix = "concurrency:api_key:"
	// 格式: concurrency:group_ingress:{groupID}。只统计请求最初进入的分组，
	// 不参加任何限流，也不会在请求切换到兜底分组时移动。
	groupIngressSlotKeyPrefix                = "concurrency:group_ingress:"
	liveAccountSlotKeyPrefix                 = "concurrency:live:account:"
	liveUserSlotKeyPrefix                    = "concurrency:live:user:"
	liveGroupUserSlotKeyPrefix               = "concurrency:live:group_user:"
	liveOrganizationSlotKeyPrefix            = "concurrency:live:organization:"
	liveOrganizationMemberSlotKeyPrefix      = "concurrency:live:organization_member:"
	liveOrganizationGroupMemberSlotKeyPrefix = "concurrency:live:organization_group_member:"
	liveAPIKeySlotKeyPrefix                  = "concurrency:live:api_key:"
	// 客户端 WebSocket 连接租约独立于请求槽位；连接空闲时不会占用 turn 并发槽位。
	openAIWSIngressLeaseKeyPrefix  = "concurrency:openai_ws_ingress:api_key:"
	openAIWSIngressLeaseTTLSeconds = 60
	liveLeaseTTLSeconds            = 60
	// 等待队列计数器格式: concurrency:wait:{userID}
	waitQueueKeyPrefix = "concurrency:wait:"
	// 账号级等待队列计数器格式: wait:account:{accountID}
	accountWaitKeyPrefix = "wait:account:"

	// 默认槽位过期时间（分钟），可通过配置覆盖
	defaultSlotTTLMinutes = 15
)

var (
	// acquireScript 使用有序集合计数并在未达上限时添加槽位
	// 使用 Redis TIME 命令获取服务器时间，避免多实例时钟不同步问题
	// KEYS[1] = 普通请求有序集合键，KEYS[2] = 同主体 Live 租约有序集合键。
	// ARGV[1] = maxConcurrency
	// ARGV[2] = TTL（秒）
	// ARGV[3] = requestID
	acquireScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local liveKey = KEYS[2]
		local maxConcurrency = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local requestID = ARGV[3]

		-- 使用 Redis 服务器时间，确保多实例时钟一致
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		-- 清理过期槽位
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)

		-- 检查是否已存在（支持重试场景刷新时间戳）
		local exists = redis.call('ZSCORE', key, requestID)
		if exists ~= false then
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end

		-- 检查是否达到并发上限
		local count = redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
		if count < maxConcurrency then
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end

		return 0
	`)

	// acquireFirstAvailableAccountScript 在一个优先级层的候选批次中只占用
	// 第一个有容量的账号。先统一清理并检查 requestID，可在客户端重试脚本时
	// 找回已经成功的槽位，避免一次请求因超时重试而占用两个账号。
	// KEYS 每个候选占两个：普通请求键、Live 租约键。
	// ARGV[1] = TTL（秒），ARGV[2] = requestID，ARGV[3] = 候选数，
	// ARGV[4...] = 每个候选的最大并发；返回选中的 1-based（从 1 开始）下标。
	acquireFirstAvailableAccountScript = redis.NewScript(`
		redis.replicate_commands()
		local ttl = tonumber(ARGV[1])
		local requestID = ARGV[2]
		local candidateCount = tonumber(ARGV[3])
		if candidateCount == nil or candidateCount <= 0 or #KEYS ~= candidateCount * 2 then
			return redis.error_reply('invalid account slot candidate batch')
		end

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		-- 先清理所有候选并恢复幂等结果。若上次执行成功但客户端只收到超时，
		-- 必须先找到原槽位，不能因为前面的账号刚好空闲而重复占槽。
		for index = 1, candidateCount do
			local keyIndex = (index - 1) * 2 + 1
			local key = KEYS[keyIndex]
			local liveKey = KEYS[keyIndex + 1]
			local maxConcurrency = tonumber(ARGV[index + 3])
			redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
			redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)
			if maxConcurrency > 0 and redis.call('ZSCORE', key, requestID) ~= false then
				redis.call('ZADD', key, now, requestID)
				redis.call('EXPIRE', key, ttl)
				return index
			end
		end

		for index = 1, candidateCount do
			local keyIndex = (index - 1) * 2 + 1
			local key = KEYS[keyIndex]
			local liveKey = KEYS[keyIndex + 1]
			local maxConcurrency = tonumber(ARGV[index + 3])
			-- 非正数沿用现有“不限制并发”语义，不创建无意义的 Redis 槽位。
			if maxConcurrency <= 0 then
				return index
			end
			local count = redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
			if count < maxConcurrency then
				redis.call('ZADD', key, now, requestID)
				redis.call('EXPIRE', key, ttl)
				return index
			end
		end

		return 0
	`)

	// getCountScript 统计有序集合中的槽位数量并清理过期条目
	// 使用 Redis TIME 命令获取服务器时间
	// KEYS[1] = 普通请求有序集合键，KEYS[2] = 同主体 Live 租约有序集合键。
	// ARGV[1] = TTL（秒）
	getCountScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local liveKey = KEYS[2]
		local ttl = tonumber(ARGV[1])

		-- 使用 Redis 服务器时间
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveKey, '-inf', now - 60)
		return redis.call('ZCARD', key) + redis.call('ZCARD', liveKey)
	`)

	// Live 创建阶段同时持有普通用户/账号槽位。replacing=1 只临时放宽
	// 这一个正在被替换的槽位，Live 租约成功后普通槽位会立即释放；因此既
	// 不会误拒绝 max=1 的正常创建，也不能借转换过程越过真实并发上限。
	acquireLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local accountRegular = KEYS[1]
		local accountLive = KEYS[2]
		local userRegular = KEYS[3]
		local userLive = KEYS[4]
		local groupRegular = KEYS[5]
		local groupLive = KEYS[6]
		local apiLive = KEYS[7]
		local accountMax = tonumber(ARGV[1])
		local userMax = tonumber(ARGV[2])
		local groupMax = tonumber(ARGV[3])
		local ttl = tonumber(ARGV[4])
		local leaseID = ARGV[5]
		local replacing = tonumber(ARGV[6])
		local now = tonumber(redis.call('TIME')[1])
		local liveExpireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', accountLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', userLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', groupLive, '-inf', liveExpireBefore)
		redis.call('ZREMRANGEBYSCORE', apiLive, '-inf', liveExpireBefore)
		if redis.call('ZSCORE', accountLive, leaseID) ~= false then
			return 1
		end
		local accountCount = redis.call('ZCARD', accountRegular) + redis.call('ZCARD', accountLive)
		local userCount = redis.call('ZCARD', userRegular) + redis.call('ZCARD', userLive)
		local groupCount = redis.call('ZCARD', groupRegular) + redis.call('ZCARD', groupLive)
		local allowance = 0
		if replacing == 1 then allowance = 1 end
		if accountMax > 0 and accountCount >= accountMax + allowance then return 0 end
		if userMax > 0 and userCount >= userMax + allowance then return 0 end
		if groupMax > 0 and groupCount >= groupMax + allowance then return 0 end
		redis.call('ZADD', accountLive, now, leaseID)
		redis.call('ZADD', userLive, now, leaseID)
		redis.call('ZADD', groupLive, now, leaseID)
		redis.call('ZADD', apiLive, now, leaseID)
		redis.call('EXPIRE', accountLive, ttl)
		redis.call('EXPIRE', userLive, ttl)
		redis.call('EXPIRE', groupLive, ttl)
		redis.call('EXPIRE', apiLive, ttl)
		return 1
	`)

	acquireOrganizationLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local accountRegular = KEYS[1]
		local accountLive = KEYS[2]
		local organizationRegular = KEYS[3]
		local organizationLive = KEYS[4]
		local memberRegular = KEYS[5]
		local memberLive = KEYS[6]
		local groupRegular = KEYS[7]
		local groupLive = KEYS[8]
		local apiRegular = KEYS[9]
		local apiLive = KEYS[10]
		local accountMax = tonumber(ARGV[1]) or 0
		local organizationMax = tonumber(ARGV[2]) or 0
		local memberMax = tonumber(ARGV[3]) or 0
		local groupMax = tonumber(ARGV[4]) or 0
		local keyMax = tonumber(ARGV[5]) or 0
		local ttl = tonumber(ARGV[6])
		local leaseID = ARGV[7]
		local replacing = tonumber(ARGV[8])
		local now = tonumber(redis.call('TIME')[1])
		local liveExpireBefore = now - ttl
		for _, key in ipairs({accountLive, organizationLive, memberLive, groupLive, apiLive}) do
			redis.call('ZREMRANGEBYSCORE', key, '-inf', liveExpireBefore)
		end
		if redis.call('ZSCORE', accountLive, leaseID) ~= false then return 1 end
		local allowance = 0
		if replacing == 1 then allowance = 1 end
		if accountMax > 0 and redis.call('ZCARD', accountRegular) + redis.call('ZCARD', accountLive) >= accountMax + allowance then return 0 end
		if organizationMax > 0 and redis.call('ZCARD', organizationRegular) + redis.call('ZCARD', organizationLive) >= organizationMax + allowance then return 0 end
		if memberMax > 0 and redis.call('ZCARD', memberRegular) + redis.call('ZCARD', memberLive) >= memberMax + allowance then return 0 end
		if groupMax > 0 and redis.call('ZCARD', groupRegular) + redis.call('ZCARD', groupLive) >= groupMax + allowance then return 0 end
		if keyMax > 0 and redis.call('ZCARD', apiRegular) + redis.call('ZCARD', apiLive) >= keyMax + allowance then return 0 end
		for _, key in ipairs({accountLive, organizationLive, memberLive, groupLive, apiLive}) do
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// 续租必须确认三个主体的成员都还存在；任何一个被运维清除或自然过期，
	// 都不能重新创建租约，否则失去租约的实例会继续占用上游长连接。
	refreshLiveLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local ttl = tonumber(ARGV[1])
		local leaseID = ARGV[2]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		for _, key in ipairs(KEYS) do
			redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
			if redis.call('ZSCORE', key, leaseID) == false then return 0 end
		end
		for _, key in ipairs(KEYS) do
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// trackSlotScript 记录 stats-only（仅统计）槽位，不做并发上限判断。
	// KEYS[1] = 有序集合键
	// ARGV[1] = TTL（秒）
	// ARGV[2] = requestID
	trackSlotScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local requestID = ARGV[2]

		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		redis.call('ZADD', key, now, requestID)
		redis.call('EXPIRE', key, ttl)
		return 1
	`)

	// acquireUserAPIKeySlotScript 在同一次原子操作中执行严格用户限流和 API Key 活跃统计。
	// KEYS[1] = user slot zset，KEYS[2] = api key stats zset，KEYS[3] = Live user lease zset。
	// ARGV[1] = maxConcurrency，ARGV[2] = TTL 秒，ARGV[3] = requestID。
	acquireUserAPIKeySlotScript = redis.NewScript(`
		redis.replicate_commands()
		local userKey = KEYS[1]
		local apiKey = KEYS[2]
		local liveUserKey = KEYS[3]
		local maxConcurrency = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local requestID = ARGV[3]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl

		redis.call('ZREMRANGEBYSCORE', userKey, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', apiKey, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveUserKey, '-inf', now - 60)
		if redis.call('ZSCORE', userKey, requestID) == false and
			redis.call('ZCARD', userKey) + redis.call('ZCARD', liveUserKey) >= maxConcurrency then
			return 0
		end

		redis.call('ZADD', userKey, now, requestID)
		redis.call('EXPIRE', userKey, ttl)
		redis.call('ZADD', apiKey, now, requestID)
		redis.call('EXPIRE', apiKey, ttl)
		return 1
	`)

	// 同时检查用户等级并发与最终分组并发；任一达到上限时不写入任何槽位。
	acquireUserGroupAPIKeySlotScript = redis.NewScript(`
		redis.replicate_commands()
		local userKey = KEYS[1]
		local groupUserKey = KEYS[2]
		local apiKey = KEYS[3]
		local liveUserKey = KEYS[4]
		local liveGroupUserKey = KEYS[5]
		local userMax = tonumber(ARGV[1])
		local groupMax = tonumber(ARGV[2])
		local ttl = tonumber(ARGV[3])
		local requestID = ARGV[4]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', userKey, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', groupUserKey, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', apiKey, '-inf', expireBefore)
		redis.call('ZREMRANGEBYSCORE', liveUserKey, '-inf', now - 60)
		redis.call('ZREMRANGEBYSCORE', liveGroupUserKey, '-inf', now - 60)
		local exists = redis.call('ZSCORE', groupUserKey, requestID) ~= false
		if not exists and userMax > 0 and redis.call('ZCARD', userKey) + redis.call('ZCARD', liveUserKey) >= userMax then return 0 end
		if not exists and groupMax > 0 and redis.call('ZCARD', groupUserKey) + redis.call('ZCARD', liveGroupUserKey) >= groupMax then return 0 end
		redis.call('ZADD', userKey, now, requestID)
		redis.call('EXPIRE', userKey, ttl)
		redis.call('ZADD', groupUserKey, now, requestID)
		redis.call('EXPIRE', groupUserKey, ttl)
		redis.call('ZADD', apiKey, now, requestID)
		redis.call('EXPIRE', apiKey, ttl)
		return 1
	`)

	// 企业请求在一次 Lua 中检查企业共享、成员、最终分组和 API Key 四个维度。
	// 任何一项超限都在写入前返回，防止失败请求污染其它计数。
	acquireOrganizationMemberGroupAPIKeySlotScript = redis.NewScript(`
		redis.replicate_commands()
		local organizationKey = KEYS[1]
		local memberKey = KEYS[2]
		local groupMemberKey = KEYS[3]
		local apiKey = KEYS[4]
		local liveOrganizationKey = KEYS[5]
		local liveMemberKey = KEYS[6]
		local liveGroupMemberKey = KEYS[7]
		local liveAPIKey = KEYS[8]
		local organizationMax = tonumber(ARGV[1]) or 0
		local memberMax = tonumber(ARGV[2]) or 0
		local groupMax = tonumber(ARGV[3]) or 0
		local keyMax = tonumber(ARGV[4]) or 0
		local ttl = tonumber(ARGV[5])
		local requestID = ARGV[6]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		for _, key in ipairs({organizationKey, memberKey, groupMemberKey, apiKey}) do
			redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		end
		for _, key in ipairs({liveOrganizationKey, liveMemberKey, liveGroupMemberKey, liveAPIKey}) do
			redis.call('ZREMRANGEBYSCORE', key, '-inf', now - 60)
		end
		local exists = redis.call('ZSCORE', apiKey, requestID) ~= false
		if not exists and organizationMax > 0 and redis.call('ZCARD', organizationKey) + redis.call('ZCARD', liveOrganizationKey) >= organizationMax then return 0 end
		if not exists and memberMax > 0 and redis.call('ZCARD', memberKey) + redis.call('ZCARD', liveMemberKey) >= memberMax then return 0 end
		if not exists and groupMax > 0 and redis.call('ZCARD', groupMemberKey) + redis.call('ZCARD', liveGroupMemberKey) >= groupMax then return 0 end
		if not exists and keyMax > 0 and redis.call('ZCARD', apiKey) + redis.call('ZCARD', liveAPIKey) >= keyMax then return 0 end
		for _, key in ipairs({organizationKey, memberKey, groupMemberKey, apiKey}) do
			redis.call('ZADD', key, now, requestID)
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// 原子清理崩溃实例留下的过期成员，并在未达到 API Key 连接上限时获取租约。
	acquireOpenAIWSIngressLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local maxConnections = tonumber(ARGV[1])
		local ttl = tonumber(ARGV[2])
		local leaseID = ARGV[3]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZSCORE', key, leaseID) ~= false then
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end
		if redis.call('ZCARD', key) < maxConnections then
			redis.call('ZADD', key, now, leaseID)
			redis.call('EXPIRE', key, ttl)
			return 1
		end
		return 0
	`)

	// 续租不能重新创建已丢失成员，否则失去租约的实例会越过分布式连接上限继续服务。
	refreshOpenAIWSIngressLeaseScript = redis.NewScript(`
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local leaseID = ARGV[2]
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZSCORE', key, leaseID) == false then
			return 0
		end
		redis.call('ZADD', key, now, leaseID)
		redis.call('EXPIRE', key, ttl)
		return 1
	`)

	// incrementWaitScript - refreshes TTL on each increment to keep queue depth accurate
	// KEYS[1] = wait queue key
	// ARGV[1] = maxWait
	// ARGV[2] = TTL in seconds
	incrementWaitScript = redis.NewScript(`
		local current = redis.call('GET', KEYS[1])
		if current == false then
			current = 0
		else
			current = tonumber(current)
		end

		if current >= tonumber(ARGV[1]) then
			return 0
		end

		local newVal = redis.call('INCR', KEYS[1])

		-- Refresh TTL so long-running traffic doesn't expire active queue counters.
		redis.call('EXPIRE', KEYS[1], ARGV[2])

			return 1
		`)

	// incrementAccountWaitScript - account-level wait queue count (refresh TTL on each increment)
	incrementAccountWaitScript = redis.NewScript(`
			local current = redis.call('GET', KEYS[1])
			if current == false then
				current = 0
			else
				current = tonumber(current)
			end

			if current >= tonumber(ARGV[1]) then
				return 0
			end

			local newVal = redis.call('INCR', KEYS[1])

			-- Refresh TTL so long-running traffic doesn't expire active queue counters.
			redis.call('EXPIRE', KEYS[1], ARGV[2])

			return 1
		`)

	// decrementWaitScript - same as before
	decrementWaitScript = redis.NewScript(`
			local current = redis.call('GET', KEYS[1])
			if current ~= false and tonumber(current) > 0 then
				redis.call('DECR', KEYS[1])
			end
			return 1
		`)

	// cleanupExpiredSlotsScript 清理单个账号/用户有序集合中过期槽位
	// KEYS[1] = 有序集合键
	// ARGV[1] = TTL（秒）
	cleanupExpiredSlotsScript = redis.NewScript(`
		-- Redis 3.2-4.x compat: opt into effects replication so redis.call('TIME')
		-- replicates correctly. No-op on Redis 5.0+ (effects replication is default).
		redis.replicate_commands()
		local key = KEYS[1]
		local ttl = tonumber(ARGV[1])
		local timeResult = redis.call('TIME')
		local now = tonumber(timeResult[1])
		local expireBefore = now - ttl
		redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
		if redis.call('ZCARD', key) == 0 then
			redis.call('DEL', key)
		else
			redis.call('EXPIRE', key, ttl)
		end
		return 1
	`)

	// startupCleanupScript 只清理真正超过 TTL 的槽位成员。
	// 多个实例会共享 Redis；进程前缀不同不代表槽位失效，按前缀删除会让
	// 新实例启动时误删其他健康实例正在使用的并发槽位。
	// KEYS 是有序集合键列表，ARGV[1] 是槽位 TTL。
	startupCleanupScript = redis.NewScript(`
		redis.replicate_commands()
		local slotTTL = tonumber(ARGV[1])
		local now = tonumber(redis.call('TIME')[1])
		local expireBefore = now - slotTTL
		local removed = 0
		for i = 1, #KEYS do
			local key = KEYS[i]
			removed = removed + redis.call('ZREMRANGEBYSCORE', key, '-inf', expireBefore)
			if redis.call('ZCARD', key) == 0 then
				redis.call('DEL', key)
			else
				redis.call('EXPIRE', key, slotTTL)
			end
		end
		return removed
	`)
)

type concurrencyCache struct {
	rdb                 *redis.Client
	slotTTLSeconds      int // 槽位过期时间（秒）
	waitQueueTTLSeconds int // 等待队列过期时间（秒）
}

var _ service.AccountSlotBatchAcquireCache = (*concurrencyCache)(nil)

// NewConcurrencyCache 创建并发控制缓存
// slotTTLMinutes: 槽位过期时间（分钟），0 或负数使用默认值 15 分钟
// waitQueueTTLSeconds: 等待队列过期时间（秒），0 或负数使用 slot TTL
func NewConcurrencyCache(rdb *redis.Client, slotTTLMinutes int, waitQueueTTLSeconds int) service.ConcurrencyCache {
	if slotTTLMinutes <= 0 {
		slotTTLMinutes = defaultSlotTTLMinutes
	}
	if waitQueueTTLSeconds <= 0 {
		waitQueueTTLSeconds = slotTTLMinutes * 60
	}
	return &concurrencyCache{
		rdb:                 rdb,
		slotTTLSeconds:      slotTTLMinutes * 60,
		waitQueueTTLSeconds: waitQueueTTLSeconds,
	}
}

// Helper functions for key generation
func accountSlotKey(accountID int64) string {
	return fmt.Sprintf("%s%d", accountSlotKeyPrefix, accountID)
}

func userSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", userSlotKeyPrefix, userID)
}

func groupUserSlotKey(groupID, userID int64) string {
	return fmt.Sprintf("%s%d:%d", groupUserSlotKeyPrefix, groupID, userID)
}

func organizationSlotKey(organizationID int64) string {
	return fmt.Sprintf("%s%d", organizationSlotKeyPrefix, organizationID)
}

func organizationMemberSlotKey(organizationID, userID int64) string {
	return fmt.Sprintf("%s%d:%d", organizationMemberSlotKeyPrefix, organizationID, userID)
}

func organizationGroupMemberSlotKey(organizationID, groupID, userID int64) string {
	return fmt.Sprintf("%s%d:%d:%d", organizationGroupMemberSlotKeyPrefix, organizationID, groupID, userID)
}

func apiKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", apiKeySlotKeyPrefix, apiKeyID)
}

func groupIngressSlotKey(groupID int64) string {
	return fmt.Sprintf("%s%d", groupIngressSlotKeyPrefix, groupID)
}

func liveAccountSlotKey(accountID int64) string {
	return fmt.Sprintf("%s%d", liveAccountSlotKeyPrefix, accountID)
}

func liveUserSlotKey(userID int64) string {
	return fmt.Sprintf("%s%d", liveUserSlotKeyPrefix, userID)
}

func liveGroupUserSlotKey(groupID, userID int64) string {
	return fmt.Sprintf("%s%d:%d", liveGroupUserSlotKeyPrefix, groupID, userID)
}

func liveOrganizationSlotKey(organizationID int64) string {
	return fmt.Sprintf("%s%d", liveOrganizationSlotKeyPrefix, organizationID)
}

func liveOrganizationMemberSlotKey(organizationID, userID int64) string {
	return fmt.Sprintf("%s%d:%d", liveOrganizationMemberSlotKeyPrefix, organizationID, userID)
}

func liveOrganizationGroupMemberSlotKey(organizationID, groupID, userID int64) string {
	return fmt.Sprintf("%s%d:%d:%d", liveOrganizationGroupMemberSlotKeyPrefix, organizationID, groupID, userID)
}

func liveAPIKeySlotKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", liveAPIKeySlotKeyPrefix, apiKeyID)
}

func openAIWSIngressLeaseKey(apiKeyID int64) string {
	return fmt.Sprintf("%s%d", openAIWSIngressLeaseKeyPrefix, apiKeyID)
}

func waitQueueKey(userID int64) string {
	return fmt.Sprintf("%s%d", waitQueueKeyPrefix, userID)
}

func accountWaitKey(accountID int64) string {
	return fmt.Sprintf("%s%d", accountWaitKeyPrefix, accountID)
}

// Account slot operations

func (c *concurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	key := accountSlotKey(accountID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取，确保多实例时钟一致
	result, err := acquireScript.Run(ctx, c.rdb, []string{key, liveAccountSlotKey(accountID)}, maxConcurrency, c.slotTTLSeconds, requestID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) AcquireFirstAvailableAccountSlot(ctx context.Context, candidates []service.AccountWithConcurrency, requestID string) (int64, bool, error) {
	if len(candidates) == 0 {
		return 0, false, nil
	}

	keys := make([]string, 0, len(candidates)*2)
	args := make([]interface{}, 0, len(candidates)+3)
	args = append(args, c.slotTTLSeconds, requestID, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, accountSlotKey(candidate.ID), liveAccountSlotKey(candidate.ID))
		args = append(args, candidate.MaxConcurrency)
	}

	selectedIndex, err := acquireFirstAvailableAccountScript.Run(ctx, c.rdb, keys, args...).Int()
	if err != nil {
		return 0, false, err
	}
	if selectedIndex <= 0 {
		return 0, false, nil
	}
	if selectedIndex > len(candidates) {
		return 0, false, fmt.Errorf("invalid account slot batch result: %d", selectedIndex)
	}
	return candidates[selectedIndex-1].ID, true, nil
}

func (c *concurrencyCache) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	key := accountSlotKey(accountID)
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

// ReleaseAccountSlots 把同一批完成请求按账号合并，再通过一条 pipeline（流水线）发送。
// 同一账号的一批 requestID 会合并成一次 ZREM，高并发同账号场景可显著减少 Redis 命令数。
func (c *concurrencyCache) ReleaseAccountSlots(ctx context.Context, releases []service.AccountSlotRelease) error {
	if len(releases) == 0 {
		return nil
	}
	groupCapacity := len(releases)
	if groupCapacity > 16 {
		// 同一波通常集中在少量账号，按请求数预分配 map 会在 256 条批次中浪费内存。
		groupCapacity = 16
	}
	byAccount := make(map[int64][]interface{}, groupCapacity)
	for _, release := range releases {
		if release.AccountID <= 0 || release.RequestID == "" {
			continue
		}
		byAccount[release.AccountID] = append(byAccount[release.AccountID], release.RequestID)
	}
	if len(byAccount) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for accountID, requestIDs := range byAccount {
		pipe.ZRem(ctx, accountSlotKey(accountID), requestIDs...)
	}
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("batch release account slots: %w", err)
	}
	return nil
}

func (c *concurrencyCache) GetAccountConcurrency(ctx context.Context, accountID int64) (int, error) {
	key := accountSlotKey(accountID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveAccountSlotKey(accountID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

func (c *concurrencyCache) GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	if len(accountIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	type accountCmd struct {
		accountID int64
		zcardCmd  *redis.IntCmd
		liveCmd   *redis.IntCmd
	}
	cmds := make([]accountCmd, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		slotKey := accountSlotKeyPrefix + strconv.FormatInt(accountID, 10)
		liveKey := liveAccountSlotKeyPrefix + strconv.FormatInt(accountID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		cmds = append(cmds, accountCmd{
			accountID: accountID,
			zcardCmd:  pipe.ZCard(ctx, slotKey),
			liveCmd:   pipe.ZCard(ctx, liveKey),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	result := make(map[int64]int, len(accountIDs))
	for _, cmd := range cmds {
		result[cmd.accountID] = int(cmd.zcardCmd.Val() + cmd.liveCmd.Val())
	}
	return result, nil
}

// User slot operations

func (c *concurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	key := userSlotKey(userID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取，确保多实例时钟一致
	result, err := acquireScript.Run(ctx, c.rdb, []string{key, liveUserSlotKey(userID)}, maxConcurrency, c.slotTTLSeconds, requestID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	key := userSlotKey(userID)
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

func (c *concurrencyCache) AcquireUserAndAPIKeySlot(ctx context.Context, userID int64, maxConcurrency int, apiKeyID int64, requestID string) (bool, error) {
	if c == nil || c.rdb == nil || userID <= 0 || maxConcurrency <= 0 || apiKeyID == 0 || requestID == "" {
		return false, nil
	}
	result, err := acquireUserAPIKeySlotScript.Run(
		ctx,
		c.rdb,
		[]string{userSlotKey(userID), apiKeySlotKey(apiKeyID), liveUserSlotKey(userID)},
		maxConcurrency,
		c.slotTTLSeconds,
		requestID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseUserAndAPIKeySlot(ctx context.Context, userID int64, apiKeyID int64, requestID string) error {
	if c == nil || c.rdb == nil || userID <= 0 || apiKeyID == 0 || requestID == "" {
		return nil
	}
	pipe := c.rdb.Pipeline()
	pipe.ZRem(ctx, userSlotKey(userID), requestID)
	pipe.ZRem(ctx, apiKeySlotKey(apiKeyID), requestID)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release combined user/api key slot: %w", err)
	}
	return nil
}

func (c *concurrencyCache) AcquireUserGroupAndAPIKeySlot(ctx context.Context, userID int64, userMaxConcurrency int, groupID int64, groupMaxConcurrency int, apiKeyID int64, requestID string) (bool, error) {
	if c == nil || c.rdb == nil || userID <= 0 || groupID <= 0 || groupMaxConcurrency <= 0 || apiKeyID == 0 || requestID == "" {
		return false, nil
	}
	result, err := acquireUserGroupAPIKeySlotScript.Run(
		ctx, c.rdb,
		[]string{
			userSlotKey(userID),
			groupUserSlotKey(groupID, userID),
			apiKeySlotKey(apiKeyID),
			liveUserSlotKey(userID),
			liveGroupUserSlotKey(groupID, userID),
		},
		userMaxConcurrency, groupMaxConcurrency, c.slotTTLSeconds, requestID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseUserGroupAndAPIKeySlot(ctx context.Context, userID, groupID, apiKeyID int64, requestID string) error {
	if c == nil || c.rdb == nil || userID <= 0 || groupID <= 0 || apiKeyID == 0 || requestID == "" {
		return nil
	}
	pipe := c.rdb.Pipeline()
	pipe.ZRem(ctx, userSlotKey(userID), requestID)
	pipe.ZRem(ctx, groupUserSlotKey(groupID, userID), requestID)
	pipe.ZRem(ctx, apiKeySlotKey(apiKeyID), requestID)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release combined user/group/api key slot: %w", err)
	}
	return nil
}

func (c *concurrencyCache) AcquireOrganizationMemberGroupAndAPIKeySlot(
	ctx context.Context,
	organizationID int64,
	organizationMaxConcurrency int,
	userID int64,
	memberMaxConcurrency int,
	groupID int64,
	groupMaxConcurrency int,
	apiKeyID int64,
	apiKeyMaxConcurrency int,
	requestID string,
) (bool, error) {
	if c == nil || c.rdb == nil || organizationID <= 0 || userID <= 0 || apiKeyID == 0 || requestID == "" {
		return false, nil
	}
	result, err := acquireOrganizationMemberGroupAPIKeySlotScript.Run(
		ctx,
		c.rdb,
		[]string{
			organizationSlotKey(organizationID),
			organizationMemberSlotKey(organizationID, userID),
			organizationGroupMemberSlotKey(organizationID, groupID, userID),
			apiKeySlotKey(apiKeyID),
			liveOrganizationSlotKey(organizationID),
			liveOrganizationMemberSlotKey(organizationID, userID),
			liveOrganizationGroupMemberSlotKey(organizationID, groupID, userID),
			liveAPIKeySlotKey(apiKeyID),
		},
		organizationMaxConcurrency,
		memberMaxConcurrency,
		groupMaxConcurrency,
		apiKeyMaxConcurrency,
		c.slotTTLSeconds,
		requestID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseOrganizationMemberGroupAndAPIKeySlot(ctx context.Context, organizationID, userID, groupID, apiKeyID int64, requestID string) error {
	if c == nil || c.rdb == nil || organizationID <= 0 || userID <= 0 || apiKeyID == 0 || requestID == "" {
		return nil
	}
	pipe := c.rdb.Pipeline()
	pipe.ZRem(ctx, organizationSlotKey(organizationID), requestID)
	pipe.ZRem(ctx, organizationMemberSlotKey(organizationID, userID), requestID)
	if groupID > 0 {
		pipe.ZRem(ctx, organizationGroupMemberSlotKey(organizationID, groupID, userID), requestID)
	}
	pipe.ZRem(ctx, apiKeySlotKey(apiKeyID), requestID)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release organization/member/group/api key slot: %w", err)
	}
	return nil
}

// ReleaseUserAndAPIKeySlots 按用户和 API Key 分别聚合 requestID，并在一次 pipeline
// （流水线）往返中删除。即使同一批包含多个用户或 Key，也只产生一次网络往返。
func (c *concurrencyCache) ReleaseUserAndAPIKeySlots(ctx context.Context, releases []service.UserAPIKeySlotRelease) error {
	if c == nil || c.rdb == nil || len(releases) == 0 {
		return nil
	}
	groupCapacity := len(releases)
	if groupCapacity > 16 {
		// 高并发完成波次通常属于少量用户/Key，限制初始容量可避免按请求数过度分配。
		groupCapacity = 16
	}
	byUser := make(map[int64][]interface{}, groupCapacity)
	byOrganization := make(map[int64][]interface{}, groupCapacity)
	byOrganizationMember := make(map[string][]interface{}, groupCapacity)
	byGroupUser := make(map[string][]interface{}, groupCapacity)
	byAPIKey := make(map[int64][]interface{}, groupCapacity)
	for _, release := range releases {
		if release.UserID <= 0 || release.APIKeyID == 0 || release.RequestID == "" {
			continue
		}
		if release.OrganizationID > 0 {
			byOrganization[release.OrganizationID] = append(byOrganization[release.OrganizationID], release.RequestID)
			key := organizationMemberSlotKey(release.OrganizationID, release.UserID)
			byOrganizationMember[key] = append(byOrganizationMember[key], release.RequestID)
		} else {
			byUser[release.UserID] = append(byUser[release.UserID], release.RequestID)
		}
		if release.GroupID > 0 {
			key := groupUserSlotKey(release.GroupID, release.UserID)
			if release.OrganizationID > 0 {
				key = organizationGroupMemberSlotKey(release.OrganizationID, release.GroupID, release.UserID)
			}
			byGroupUser[key] = append(byGroupUser[key], release.RequestID)
		}
		byAPIKey[release.APIKeyID] = append(byAPIKey[release.APIKeyID], release.RequestID)
	}
	if len(byUser) == 0 && len(byOrganization) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for userID, requestIDs := range byUser {
		pipe.ZRem(ctx, userSlotKey(userID), requestIDs...)
	}
	for organizationID, requestIDs := range byOrganization {
		pipe.ZRem(ctx, organizationSlotKey(organizationID), requestIDs...)
	}
	for key, requestIDs := range byOrganizationMember {
		pipe.ZRem(ctx, key, requestIDs...)
	}
	for apiKeyID, requestIDs := range byAPIKey {
		pipe.ZRem(ctx, apiKeySlotKey(apiKeyID), requestIDs...)
	}
	for key, requestIDs := range byGroupUser {
		pipe.ZRem(ctx, key, requestIDs...)
	}
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("batch release combined user/api key slots: %w", err)
	}
	return nil
}

func (c *concurrencyCache) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	key := userSlotKey(userID)
	// 时间戳在 Lua 脚本内使用 Redis TIME 命令获取
	result, err := getCountScript.Run(ctx, c.rdb, []string{key, liveUserSlotKey(userID)}, c.slotTTLSeconds).Int()
	if err != nil {
		return 0, err
	}
	return result, nil
}

func (c *concurrencyCache) TrackAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	_, err := trackSlotScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds, requestID).Result()
	return err
}

func (c *concurrencyCache) ReleaseAPIKeySlot(ctx context.Context, apiKeyID int64, requestID string) error {
	key := apiKeySlotKey(apiKeyID)
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

func (c *concurrencyCache) TrackGroupIngressSlot(ctx context.Context, groupID int64, requestID string) error {
	if c == nil || c.rdb == nil || groupID <= 0 || requestID == "" {
		return nil
	}
	_, err := trackSlotScript.Run(ctx, c.rdb, []string{groupIngressSlotKey(groupID)}, c.slotTTLSeconds, requestID).Result()
	return err
}

func (c *concurrencyCache) RefreshGroupIngressSlot(ctx context.Context, groupID int64, requestID string) (bool, error) {
	if c == nil || c.rdb == nil || groupID <= 0 || requestID == "" {
		return false, nil
	}
	result, err := refreshLiveLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{groupIngressSlotKey(groupID)},
		c.slotTTLSeconds,
		requestID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) GroupIngressSlotRefreshInterval() time.Duration {
	if c == nil || c.slotTTLSeconds <= 0 {
		return 0
	}
	interval := time.Duration(c.slotTTLSeconds) * time.Second / 3
	// 默认 30 分钟 TTL 每 5 分钟续一次即可，避免按请求高频访问 Redis；
	// 自定义较短 TTL 时仍保留至少三次续租机会。
	if interval > 5*time.Minute {
		return 5 * time.Minute
	}
	if interval <= 0 {
		return time.Second
	}
	return interval
}

func (c *concurrencyCache) ReleaseGroupIngressSlot(ctx context.Context, groupID int64, requestID string) error {
	if c == nil || c.rdb == nil || groupID <= 0 || requestID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, groupIngressSlotKey(groupID), requestID).Err()
}

func (c *concurrencyCache) AcquireFailoverGroupSlot(ctx context.Context, organizationID, userID, groupID int64, maxConcurrency int, requestID string) (bool, error) {
	if c == nil || c.rdb == nil || userID <= 0 || groupID <= 0 || maxConcurrency <= 0 || requestID == "" {
		return false, nil
	}
	regularKey := groupUserSlotKey(groupID, userID)
	liveKey := liveGroupUserSlotKey(groupID, userID)
	if organizationID > 0 {
		regularKey = organizationGroupMemberSlotKey(organizationID, groupID, userID)
		liveKey = liveOrganizationGroupMemberSlotKey(organizationID, groupID, userID)
	}
	result, err := acquireScript.Run(ctx, c.rdb, []string{regularKey, liveKey}, maxConcurrency, c.slotTTLSeconds, requestID).Int()
	return result == 1, err
}

func (c *concurrencyCache) ReleaseFailoverGroupSlot(ctx context.Context, organizationID, userID, groupID int64, requestID string) error {
	if c == nil || c.rdb == nil || userID <= 0 || groupID <= 0 || requestID == "" {
		return nil
	}
	key := groupUserSlotKey(groupID, userID)
	if organizationID > 0 {
		key = organizationGroupMemberSlotKey(organizationID, groupID, userID)
	}
	return c.rdb.ZRem(ctx, key, requestID).Err()
}

func (c *concurrencyCache) AcquireOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, maxConnections int, leaseID string) (bool, error) {
	// 正数 ID 属于本地数据库 Key，负数 ID 属于主站快照 Key 的稳定命名空间；
	// 只有 0 表示无法确定租约主体，不能把合法的主站 Key 拒之门外。
	if c == nil || c.rdb == nil || apiKeyID == 0 || maxConnections <= 0 || leaseID == "" {
		return false, nil
	}
	result, err := acquireOpenAIWSIngressLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{openAIWSIngressLeaseKey(apiKeyID)},
		maxConnections,
		openAIWSIngressLeaseTTLSeconds,
		leaseID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) RefreshOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || apiKeyID == 0 || leaseID == "" {
		return false, nil
	}
	result, err := refreshOpenAIWSIngressLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{openAIWSIngressLeaseKey(apiKeyID)},
		openAIWSIngressLeaseTTLSeconds,
		leaseID,
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseOpenAIWSIngressLease(ctx context.Context, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || apiKeyID == 0 || leaseID == "" {
		return nil
	}
	return c.rdb.ZRem(ctx, openAIWSIngressLeaseKey(apiKeyID), leaseID).Err()
}

func (c *concurrencyCache) AcquireLiveLease(
	ctx context.Context,
	accountID int64,
	accountMax int,
	userID int64,
	userMax int,
	groupID int64,
	groupMax int,
	apiKeyID int64,
	leaseID string,
	replacingRegularSlots bool,
) (bool, error) {
	// gateway-lite（轻量网关）的主站 Key 使用负数稳定 ID；0 才表示身份缺失。
	if c == nil || c.rdb == nil || accountID <= 0 || userID <= 0 || groupID <= 0 || apiKeyID == 0 || leaseID == "" {
		return false, nil
	}
	replacing := 0
	if replacingRegularSlots {
		replacing = 1
	}
	result, err := acquireLiveLeaseScript.Run(ctx, c.rdb, []string{
		accountSlotKey(accountID),
		liveAccountSlotKey(accountID),
		userSlotKey(userID),
		liveUserSlotKey(userID),
		groupUserSlotKey(groupID, userID),
		liveGroupUserSlotKey(groupID, userID),
		liveAPIKeySlotKey(apiKeyID),
	}, accountMax, userMax, groupMax, liveLeaseTTLSeconds, leaseID, replacing).Int()
	return result == 1, err
}

func (c *concurrencyCache) AcquireOrganizationLiveLease(
	ctx context.Context,
	accountID int64,
	accountMax int,
	organizationID int64,
	organizationMax int,
	userID int64,
	memberMax int,
	groupID int64,
	groupMax int,
	apiKeyID int64,
	apiKeyMax int,
	leaseID string,
	replacingRegularSlots bool,
) (bool, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || organizationID <= 0 || userID <= 0 || apiKeyID == 0 || leaseID == "" {
		return false, nil
	}
	replacing := 0
	if replacingRegularSlots {
		replacing = 1
	}
	result, err := acquireOrganizationLiveLeaseScript.Run(ctx, c.rdb, []string{
		accountSlotKey(accountID), liveAccountSlotKey(accountID),
		organizationSlotKey(organizationID), liveOrganizationSlotKey(organizationID),
		organizationMemberSlotKey(organizationID, userID), liveOrganizationMemberSlotKey(organizationID, userID),
		organizationGroupMemberSlotKey(organizationID, groupID, userID), liveOrganizationGroupMemberSlotKey(organizationID, groupID, userID),
		apiKeySlotKey(apiKeyID), liveAPIKeySlotKey(apiKeyID),
	}, accountMax, organizationMax, memberMax, groupMax, apiKeyMax, liveLeaseTTLSeconds, leaseID, replacing).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) RefreshOrganizationLiveLease(ctx context.Context, accountID, organizationID, userID, groupID, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || organizationID <= 0 || userID <= 0 || apiKeyID == 0 || leaseID == "" {
		return false, nil
	}
	result, err := refreshLiveLeaseScript.Run(ctx, c.rdb, []string{
		liveAccountSlotKey(accountID), liveOrganizationSlotKey(organizationID),
		liveOrganizationMemberSlotKey(organizationID, userID),
		liveOrganizationGroupMemberSlotKey(organizationID, groupID, userID), liveAPIKeySlotKey(apiKeyID),
	}, liveLeaseTTLSeconds, leaseID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) ReleaseOrganizationLiveLease(ctx context.Context, accountID, organizationID, userID, groupID, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || leaseID == "" {
		return nil
	}
	pipe := c.rdb.Pipeline()
	pipe.ZRem(ctx, liveAccountSlotKey(accountID), leaseID)
	pipe.ZRem(ctx, liveOrganizationSlotKey(organizationID), leaseID)
	pipe.ZRem(ctx, liveOrganizationMemberSlotKey(organizationID, userID), leaseID)
	pipe.ZRem(ctx, liveOrganizationGroupMemberSlotKey(organizationID, groupID, userID), leaseID)
	pipe.ZRem(ctx, liveAPIKeySlotKey(apiKeyID), leaseID)
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("release organization live lease: %w", err)
	}
	return nil
}

func (c *concurrencyCache) RefreshLiveLease(ctx context.Context, accountID, userID, groupID, apiKeyID int64, leaseID string) (bool, error) {
	if c == nil || c.rdb == nil || accountID <= 0 || userID <= 0 || groupID <= 0 || apiKeyID == 0 || leaseID == "" {
		return false, nil
	}
	result, err := refreshLiveLeaseScript.Run(ctx, c.rdb, []string{
		liveAccountSlotKey(accountID),
		liveUserSlotKey(userID),
		liveGroupUserSlotKey(groupID, userID),
		liveAPIKeySlotKey(apiKeyID),
	}, liveLeaseTTLSeconds, leaseID).Int()
	return result == 1, err
}

func (c *concurrencyCache) ReleaseLiveLease(ctx context.Context, accountID, userID, groupID, apiKeyID int64, leaseID string) error {
	if c == nil || c.rdb == nil || accountID <= 0 || userID <= 0 || groupID <= 0 || apiKeyID == 0 || leaseID == "" {
		return nil
	}
	pipe := c.rdb.TxPipeline()
	pipe.ZRem(ctx, liveAccountSlotKey(accountID), leaseID)
	pipe.ZRem(ctx, liveUserSlotKey(userID), leaseID)
	pipe.ZRem(ctx, liveGroupUserSlotKey(groupID, userID), leaseID)
	pipe.ZRem(ctx, liveAPIKeySlotKey(apiKeyID), leaseID)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *concurrencyCache) GetAPIKeyConcurrencyBatch(ctx context.Context, apiKeyIDs []int64) (map[int64]int, error) {
	if len(apiKeyIDs) == 0 {
		return map[int64]int{}, nil
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()
	type apiKeyCmd struct {
		apiKeyID int64
		zcardCmd *redis.IntCmd
		liveCmd  *redis.IntCmd
	}
	cmds := make([]apiKeyCmd, 0, len(apiKeyIDs))
	for _, apiKeyID := range apiKeyIDs {
		slotKey := apiKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		liveKey := liveAPIKeySlotKeyPrefix + strconv.FormatInt(apiKeyID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		cmds = append(cmds, apiKeyCmd{
			apiKeyID: apiKeyID,
			zcardCmd: pipe.ZCard(ctx, slotKey),
			liveCmd:  pipe.ZCard(ctx, liveKey),
		})
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	result := make(map[int64]int, len(apiKeyIDs))
	for _, cmd := range cmds {
		result[cmd.apiKeyID] = int(cmd.zcardCmd.Val() + cmd.liveCmd.Val())
	}
	return result, nil
}

func (c *concurrencyCache) GetGroupIngressConcurrencyBatch(ctx context.Context, groupIDs []int64) (map[int64]int, error) {
	if len(groupIDs) == 0 {
		return map[int64]int{}, nil
	}
	if c == nil || c.rdb == nil {
		return nil, errors.New("redis client is unavailable")
	}

	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)
	pipe := c.rdb.Pipeline()
	type groupCommand struct {
		groupID int64
		count   *redis.IntCmd
	}
	commands := make([]groupCommand, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		key := groupIngressSlotKey(groupID)
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoffTime, 10))
		commands = append(commands, groupCommand{groupID: groupID, count: pipe.ZCard(ctx, key)})
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read group ingress concurrency: %w", err)
	}
	result := make(map[int64]int, len(groupIDs))
	for _, groupID := range groupIDs {
		result[groupID] = 0
	}
	for _, command := range commands {
		result[command.groupID] = int(command.count.Val())
	}
	return result, nil
}

// Wait queue operations

func (c *concurrencyCache) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	key := waitQueueKey(userID)
	result, err := incrementWaitScript.Run(ctx, c.rdb, []string{key}, maxWait, c.waitQueueTTLSeconds).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) DecrementWaitCount(ctx context.Context, userID int64) error {
	key := waitQueueKey(userID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	return err
}

// Account wait queue operations

func (c *concurrencyCache) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	key := accountWaitKey(accountID)
	result, err := incrementAccountWaitScript.Run(ctx, c.rdb, []string{key}, maxWait, c.waitQueueTTLSeconds).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (c *concurrencyCache) DecrementAccountWaitCount(ctx context.Context, accountID int64) error {
	key := accountWaitKey(accountID)
	_, err := decrementWaitScript.Run(ctx, c.rdb, []string{key}).Result()
	return err
}

func (c *concurrencyCache) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	key := accountWaitKey(accountID)
	val, err := c.rdb.Get(ctx, key).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return 0, err
	}
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return val, nil
}

func (c *concurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	if len(accounts) == 0 {
		return map[int64]*service.AccountLoadInfo{}, nil
	}

	// 使用 Pipeline 替代 Lua 脚本，兼容 Redis Cluster（Lua 内动态拼 key 会 CROSSSLOT）。
	// 每个账号执行 3 个命令：ZREMRANGEBYSCORE（清理过期）、ZCARD（并发数）、GET（等待数）。
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	type accountCmds struct {
		id             int64
		maxConcurrency int
		zcardCmd       *redis.IntCmd
		liveCmd        *redis.IntCmd
		getCmd         *redis.StringCmd
	}
	cmds := make([]accountCmds, 0, len(accounts))
	for _, acc := range accounts {
		slotKey := accountSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		liveKey := liveAccountSlotKeyPrefix + strconv.FormatInt(acc.ID, 10)
		waitKey := accountWaitKeyPrefix + strconv.FormatInt(acc.ID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		ac := accountCmds{
			id:             acc.ID,
			maxConcurrency: acc.MaxConcurrency,
			zcardCmd:       pipe.ZCard(ctx, slotKey),
			liveCmd:        pipe.ZCard(ctx, liveKey),
			getCmd:         pipe.Get(ctx, waitKey),
		}
		cmds = append(cmds, ac)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	loadMap := make(map[int64]*service.AccountLoadInfo, len(accounts))
	for _, ac := range cmds {
		currentConcurrency := int(ac.zcardCmd.Val() + ac.liveCmd.Val())
		waitingCount := 0
		if v, err := ac.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if ac.maxConcurrency > 0 {
			loadRate = (currentConcurrency + waitingCount) * 100 / ac.maxConcurrency
		}
		loadMap[ac.id] = &service.AccountLoadInfo{
			AccountID:          ac.id,
			CurrentConcurrency: currentConcurrency,
			WaitingCount:       waitingCount,
			LoadRate:           loadRate,
		}
	}

	return loadMap, nil
}

func (c *concurrencyCache) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	if len(users) == 0 {
		return map[int64]*service.UserLoadInfo{}, nil
	}

	// 使用 Pipeline 替代 Lua 脚本，兼容 Redis Cluster。
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis TIME: %w", err)
	}
	cutoffTime := now.Unix() - int64(c.slotTTLSeconds)

	pipe := c.rdb.Pipeline()

	type userCmds struct {
		id             int64
		maxConcurrency int
		zcardCmd       *redis.IntCmd
		liveCmd        *redis.IntCmd
		getCmd         *redis.StringCmd
	}
	cmds := make([]userCmds, 0, len(users))
	for _, u := range users {
		slotKey := userSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		liveKey := liveUserSlotKeyPrefix + strconv.FormatInt(u.ID, 10)
		waitKey := waitQueueKeyPrefix + strconv.FormatInt(u.ID, 10)
		pipe.ZRemRangeByScore(ctx, slotKey, "-inf", strconv.FormatInt(cutoffTime, 10))
		pipe.ZRemRangeByScore(ctx, liveKey, "-inf", strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10))
		uc := userCmds{
			id:             u.ID,
			maxConcurrency: u.MaxConcurrency,
			zcardCmd:       pipe.ZCard(ctx, slotKey),
			liveCmd:        pipe.ZCard(ctx, liveKey),
			getCmd:         pipe.Get(ctx, waitKey),
		}
		cmds = append(cmds, uc)
	}

	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("pipeline exec: %w", err)
	}

	loadMap := make(map[int64]*service.UserLoadInfo, len(users))
	for _, uc := range cmds {
		currentConcurrency := int(uc.zcardCmd.Val() + uc.liveCmd.Val())
		waitingCount := 0
		if v, err := uc.getCmd.Int(); err == nil {
			waitingCount = v
		}
		loadRate := 0
		if uc.maxConcurrency > 0 {
			loadRate = (currentConcurrency + waitingCount) * 100 / uc.maxConcurrency
		}
		loadMap[uc.id] = &service.UserLoadInfo{
			UserID:             uc.id,
			CurrentConcurrency: currentConcurrency,
			WaitingCount:       waitingCount,
			LoadRate:           loadRate,
		}
	}

	return loadMap, nil
}

func (c *concurrencyCache) CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error {
	key := accountSlotKey(accountID)
	_, err := cleanupExpiredSlotsScript.Run(ctx, c.rdb, []string{key}, c.slotTTLSeconds).Result()
	return err
}

func (c *concurrencyCache) CleanupStaleProcessSlots(ctx context.Context, _ string) error {
	// 多实例共享 Redis 时不能根据“不是当前进程前缀”判断槽位陈旧；这里只
	// 清理已超过 TTL 的成员。崩溃实例遗留的槽位由 TTL 自然回收，健康实例
	// 的账号、用户、企业、分组和入口统计槽位均保持不变。
	slotPatterns := []string{
		accountSlotKeyPrefix + "*",
		userSlotKeyPrefix + "*",
		groupUserSlotKeyPrefix + "*",
		organizationSlotKeyPrefix + "*",
		organizationMemberSlotKeyPrefix + "*",
		organizationGroupMemberSlotKeyPrefix + "*",
		groupIngressSlotKeyPrefix + "*",
	}
	for _, pattern := range slotPatterns {
		if err := c.cleanupSlotsByPattern(ctx, pattern); err != nil {
			return err
		}
	}
	// 等待队列同样可能属于其他健康实例，保留并依赖其自身 TTL 回收。
	return nil
}

// cleanupSlotsByPattern 扫描匹配 pattern 的有序集合键，批量清理过期成员。
func (c *concurrencyCache) cleanupSlotsByPattern(ctx context.Context, pattern string) error {
	const scanCount = 200
	var cursor uint64
	for {
		keys, nextCursor, err := c.rdb.Scan(ctx, cursor, pattern, scanCount).Result()
		if err != nil {
			return fmt.Errorf("scan %s: %w", pattern, err)
		}
		if len(keys) > 0 {
			_, err := startupCleanupScript.Run(ctx, c.rdb, keys, c.slotTTLSeconds).Result()
			if err != nil {
				return fmt.Errorf("cleanup slots %s: %w", pattern, err)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}

// deleteKeysByPattern 扫描匹配 pattern 的键并删除。
func (c *concurrencyCache) deleteKeysByPattern(ctx context.Context, pattern string) error {
	const scanCount = 200
	var cursor uint64
	for {
		keys, nextCursor, err := c.rdb.Scan(ctx, cursor, pattern, scanCount).Result()
		if err != nil {
			return fmt.Errorf("scan %s: %w", pattern, err)
		}
		if len(keys) > 0 {
			if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("del %s: %w", pattern, err)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}
