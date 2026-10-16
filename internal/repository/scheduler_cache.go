package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	schedulerBucketSetKey          = "sched:buckets"
	schedulerOutboxWatermarkKey    = "sched:outbox:watermark"
	schedulerAccountPrefix         = "sched:acc:"
	schedulerAccountMetaPrefix     = "sched:meta:"
	schedulerAccountLastUsedPrefix = "sched:acc:last_used:"
	// v2 增加了 OpenAI 思考上限和精确映射。使用新命名空间让升级前的
	// 永久缓存自然失效，避免旧 JSON 无法区分“策略为空”和“字段尚不存在”。
	schedulerGroupPrefix    = "sched:group:v2:"
	schedulerActivePrefix   = "sched:active:"
	schedulerReadyPrefix    = "sched:ready:"
	schedulerVersionPrefix  = "sched:ver:"
	schedulerEpochPrefix    = "sched:epoch:"
	schedulerRetiredPrefix  = "sched:retired:"
	schedulerSnapshotPrefix = "sched:"
	schedulerLockPrefix     = "sched:lock:"

	defaultSchedulerSnapshotMGetChunkSize  = 128
	defaultSchedulerSnapshotWriteChunkSize = 256
	schedulerLastUsedUpdateChunkSize       = 256

	// snapshotGraceTTLSeconds 旧快照过期的宽限期（秒）。
	// 替代立即 DEL，让正在读取旧版本的 reader 有足够时间完成 ZRANGE。
	snapshotGraceTTLSeconds = 60
)

const (
	schedulerGroupLifecycleLockPrefix      = "sched:group:lifecycle-lock:"
	schedulerGroupLifecycleOwnerTokenBytes = 16
)

// updateSchedulerLastUsedScript 只更新轻量 side key（旁路键），避免每次请求后
// 重新读写可能包含大凭据的完整账号 JSON；同时用单调比较阻止旧异步任务回退时间。
var updateSchedulerLastUsedScript = redis.NewScript(`
local updated = 0
for index = 1, #ARGV do
    local key_index = (index - 1) * 2 + 1
    local candidate = tonumber(ARGV[index])
    if candidate == nil then
        return redis.error_reply('invalid last_used value')
    end
    if redis.call('EXISTS', KEYS[key_index]) == 1 then
        local current = tonumber(redis.call('GET', KEYS[key_index + 1]))
        if current == nil or candidate > current then
            redis.call('SET', KEYS[key_index + 1], ARGV[index])
            updated = updated + 1
        end
    end
end
return updated
`)

var (
	captureBucketWriteTokenScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
	return -1
end

local currentEpoch = redis.call('GET', KEYS[1])
if currentEpoch == false then
	redis.call('SET', KEYS[1], '1')
	return 1
end

local parsedEpoch = tonumber(currentEpoch)
if parsedEpoch == nil or parsedEpoch < 1 then
	return -2
end
return parsedEpoch
`)

	allocateSnapshotVersionScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[2]) == 1 then
	return -1
end

local currentEpoch = tonumber(redis.call('GET', KEYS[1]))
local expectedEpoch = tonumber(ARGV[1])
if currentEpoch == nil or expectedEpoch == nil or currentEpoch ~= expectedEpoch then
	return -2
end

return redis.call('INCR', KEYS[3])
`)

	retireBucketScript = redis.NewScript(`
local retired = redis.call('GET', KEYS[2])
local currentEpoch = tonumber(redis.call('GET', KEYS[1])) or 0

if retired == false then
	currentEpoch = currentEpoch + 1
	if currentEpoch < 1 then
		currentEpoch = 1
	end
	redis.call('SET', KEYS[1], tostring(currentEpoch))
	redis.call('SET', KEYS[2], tostring(currentEpoch))
elseif currentEpoch < 1 then
	currentEpoch = tonumber(retired) or 1
	redis.call('SET', KEYS[1], tostring(currentEpoch))
end

redis.call('SREM', KEYS[3], ARGV[1])
local currentActive = redis.call('GET', KEYS[5])
if currentActive ~= false then
	redis.call('EXPIRE', ARGV[2] .. currentActive, tonumber(ARGV[3]))
end
redis.call('DEL', KEYS[4], KEYS[5])
return currentEpoch
`)

	reopenBucketScript = redis.NewScript(`
local currentEpochRaw = redis.call('GET', KEYS[1])
local currentEpoch = tonumber(currentEpochRaw)
local retiredEpochRaw = redis.call('GET', KEYS[2])

if retiredEpochRaw == false then
	if currentEpochRaw == false then
		redis.call('SET', KEYS[1], '1')
		return 1
	end
	if currentEpoch == nil or currentEpoch < 1 then
		return -2
	end
	return currentEpoch
end

local retiredEpoch = tonumber(retiredEpochRaw)
if retiredEpoch == nil or retiredEpoch < 1 then
	return -2
end
if currentEpoch == nil or currentEpoch < retiredEpoch then
	currentEpoch = retiredEpoch
end

redis.call('SET', KEYS[1], tostring(currentEpoch))
redis.call('DEL', KEYS[2])
redis.call('SREM', KEYS[3], ARGV[1])
local currentActive = redis.call('GET', KEYS[5])
if currentActive ~= false then
	redis.call('EXPIRE', ARGV[2] .. currentActive, tonumber(ARGV[3]))
end
redis.call('DEL', KEYS[4], KEYS[5])
return currentEpoch
`)

	releaseGroupLifecycleLeaseScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`)

	// activateSnapshotScript 原子 CAS 切换快照版本。
	// 仅当新版本号 >= 当前激活版本时才切换，防止并发写入导致版本回滚。
	// 旧快照使用 EXPIRE 设置宽限期而非立即 DEL，避免与 reader 竞态。
	//
	// KEYS[1] = activeKey     (sched:active:{bucket})
	// KEYS[2] = readyKey      (sched:ready:{bucket})
	// KEYS[3] = bucketSetKey  (sched:buckets)
	// KEYS[4] = snapshotKey   (新写入的快照 key)
	// KEYS[5] = epochKey      (sched:epoch:{bucket})
	// KEYS[6] = retiredKey    (sched:retired:{bucket})
	// ARGV[1] = 新版本号字符串
	// ARGV[2] = bucket 字符串 (用于 SADD)
	// ARGV[3] = 快照 key 前缀 (用于构造旧快照 key)
	// ARGV[4] = 宽限期 TTL 秒数
	// ARGV[5] = writer epoch
	//
	// 返回 1 = 已激活, 0 = 版本过旧未激活
	activateSnapshotScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[6]) == 1 then
	redis.call('DEL', KEYS[4])
	return -1
end

local currentEpoch = tonumber(redis.call('GET', KEYS[5]))
local expectedEpoch = tonumber(ARGV[5])
if currentEpoch == nil or expectedEpoch == nil or currentEpoch ~= expectedEpoch then
	redis.call('DEL', KEYS[4])
	return -2
end

local currentActive = redis.call('GET', KEYS[1])
local newVersion = tonumber(ARGV[1])

if currentActive ~= false then
	local curVersion = tonumber(currentActive)
	if curVersion and newVersion < curVersion then
		redis.call('DEL', KEYS[4])
		return 0
	end
end

redis.call('SET', KEYS[1], ARGV[1])
redis.call('SET', KEYS[2], '1')
redis.call('SADD', KEYS[3], ARGV[2])

if currentActive ~= false and currentActive ~= ARGV[1] then
	redis.call('EXPIRE', ARGV[3] .. currentActive, tonumber(ARGV[4]))
end

return 1
`)
)

type schedulerCache struct {
	rdb            *redis.Client
	mgetChunkSize  int
	writeChunkSize int
}

func NewSchedulerCache(rdb *redis.Client) service.SchedulerCache {
	return newSchedulerCacheWithChunkSizes(rdb, defaultSchedulerSnapshotMGetChunkSize, defaultSchedulerSnapshotWriteChunkSize)
}

func newSchedulerCacheWithChunkSizes(rdb *redis.Client, mgetChunkSize, writeChunkSize int) service.SchedulerCache {
	if mgetChunkSize <= 0 {
		mgetChunkSize = defaultSchedulerSnapshotMGetChunkSize
	}
	if writeChunkSize <= 0 {
		writeChunkSize = defaultSchedulerSnapshotWriteChunkSize
	}
	return &schedulerCache{
		rdb:            rdb,
		mgetChunkSize:  mgetChunkSize,
		writeChunkSize: writeChunkSize,
	}
}

func (c *schedulerCache) GetSnapshot(ctx context.Context, bucket service.SchedulerBucket) ([]*service.Account, bool, error) {
	readyKey := schedulerBucketKey(schedulerReadyPrefix, bucket)
	readyVal, err := c.rdb.Get(ctx, readyKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if readyVal != "1" {
		return nil, false, nil
	}

	activeKey := schedulerBucketKey(schedulerActivePrefix, bucket)
	activeVal, err := c.rdb.Get(ctx, activeKey).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	snapshotKey := schedulerSnapshotKey(bucket, activeVal)
	ids, err := c.rdb.ZRange(ctx, snapshotKey, 0, -1).Result()
	if err != nil {
		return nil, false, err
	}
	if len(ids) == 0 {
		// 空快照视为缓存未命中，触发数据库回退查询
		// 这解决了新分组创建后立即绑定账号时的竞态条件问题
		return nil, false, nil
	}

	keys := make([]string, 0, len(ids))
	lastUsedKeys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, schedulerAccountMetaKey(id))
		lastUsedKeys = append(lastUsedKeys, schedulerLastUsedKey(id))
	}
	values, err := c.mgetChunked(ctx, keys)
	if err != nil {
		return nil, false, err
	}
	lastUsedValues, err := c.mgetChunked(ctx, lastUsedKeys)
	if err != nil {
		return nil, false, err
	}

	accounts := make([]*service.Account, 0, len(values))
	for i, val := range values {
		if val == nil {
			return nil, false, nil
		}
		account, err := decodeCachedAccount(val)
		if err != nil {
			return nil, false, err
		}
		if err := applySchedulerLastUsed(account, lastUsedValues[i]); err != nil {
			return nil, false, err
		}
		accounts = append(accounts, account)
	}

	return accounts, true, nil
}

func (c *schedulerCache) SetSnapshot(ctx context.Context, bucket service.SchedulerBucket, accounts []service.Account) error {
	token, err := c.CaptureBucketWriteToken(ctx, bucket)
	if err != nil {
		return err
	}
	return c.SetSnapshotWithToken(ctx, bucket, token, accounts)
}

func (c *schedulerCache) CaptureBucketWriteToken(ctx context.Context, bucket service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	result, err := captureBucketWriteTokenScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
	}).Int64()
	if err != nil {
		return service.SchedulerBucketWriteToken{}, err
	}
	if err := schedulerBucketWriteResultError(result, bucket); err != nil {
		return service.SchedulerBucketWriteToken{}, err
	}
	return service.SchedulerBucketWriteToken{Bucket: bucket, Epoch: result}, nil
}

func (c *schedulerCache) SetSnapshotWithToken(ctx context.Context, bucket service.SchedulerBucket, token service.SchedulerBucketWriteToken, accounts []service.Account) error {
	if !token.ValidFor(bucket) {
		return fmt.Errorf("%w: bucket=%s", service.ErrSchedulerBucketWriteFenced, bucket.String())
	}
	versionStr, err := c.allocateSnapshotVersion(ctx, bucket, token)
	if err != nil {
		return err
	}
	// 快照成员只依赖可编码账号的有序 ID；直接复用 ID 路径，避免分配
	// 随后立即丢弃的完整 Account 临时切片。
	if _, err := c.writeSnapshotVersionAndReturnAccountIDs(ctx, bucket, versionStr, accounts); err != nil {
		return err
	}
	return c.activateSnapshotVersion(ctx, bucket, token, versionStr)
}

// SetSnapshotAndReturnAccountIDs 完整发布快照，并返回 writeAccountPayloads
// 实际接受的有序账号 ID。只有版本激活和 fencing（写入防护）都成功后才返回，
// 因而调用方可以安全地在同一重建批次的后续桶中复用这些成员。
func (c *schedulerCache) SetSnapshotAndReturnAccountIDs(ctx context.Context, bucket service.SchedulerBucket, token service.SchedulerBucketWriteToken, accounts []service.Account) ([]int64, error) {
	if !token.ValidFor(bucket) {
		return nil, fmt.Errorf("%w: bucket=%s", service.ErrSchedulerBucketWriteFenced, bucket.String())
	}
	version, err := c.allocateSnapshotVersion(ctx, bucket, token)
	if err != nil {
		return nil, err
	}
	accountIDs, err := c.writeSnapshotVersionAndReturnAccountIDs(ctx, bucket, version, accounts)
	if err != nil {
		return nil, err
	}
	if err := c.activateSnapshotVersion(ctx, bucket, token, version); err != nil {
		return nil, err
	}
	return accountIDs, nil
}

// SetSnapshotByAccountIDs 复用同批次首次完整写入后的账号成员。
// 每个桶仍独立分配版本、写入有序集合并执行激活 fencing；这里只省略
// 重复的账号 JSON 序列化、镜像凭据合并和全局账号键写入。
func (c *schedulerCache) SetSnapshotByAccountIDs(ctx context.Context, bucket service.SchedulerBucket, token service.SchedulerBucketWriteToken, accountIDs []int64) error {
	if !token.ValidFor(bucket) {
		return fmt.Errorf("%w: bucket=%s", service.ErrSchedulerBucketWriteFenced, bucket.String())
	}
	version, err := c.allocateSnapshotVersion(ctx, bucket, token)
	if err != nil {
		return err
	}
	if err := c.writeSnapshotAccountIDs(ctx, bucket, version, accountIDs); err != nil {
		return err
	}
	return c.activateSnapshotVersion(ctx, bucket, token, version)
}

func (c *schedulerCache) allocateSnapshotVersion(ctx context.Context, bucket service.SchedulerBucket, token service.SchedulerBucketWriteToken) (string, error) {
	result, err := allocateSnapshotVersionScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
		schedulerBucketKey(schedulerVersionPrefix, bucket),
	}, token.Epoch).Int64()
	if err != nil {
		return "", err
	}
	if err := schedulerBucketWriteResultError(result, bucket); err != nil {
		return "", err
	}
	return strconv.FormatInt(result, 10), nil
}

func (c *schedulerCache) writeSnapshotVersionAndReturnAccountIDs(ctx context.Context, bucket service.SchedulerBucket, version string, accounts []service.Account) ([]int64, error) {
	accountIDs, err := c.writeAccountIDs(ctx, accounts)
	if err != nil {
		return nil, err
	}
	if err := c.writeSnapshotAccountIDs(ctx, bucket, version, accountIDs); err != nil {
		return nil, err
	}
	return accountIDs, nil
}

func (c *schedulerCache) writeSnapshotAccountIDs(ctx context.Context, bucket service.SchedulerBucket, version string, accountIDs []int64) error {
	return c.writeSnapshotMembers(ctx, bucket, version, schedulerSnapshotMembers(accountIDs))
}

func schedulerSnapshotMembers(accountIDs []int64) []redis.Z {
	if len(accountIDs) == 0 {
		return nil
	}
	// 使用序号作为 score，保持数据库返回顺序；重复 ID 延续 Redis ZADD
	// 最后一次 score 覆盖的既有语义。
	members := make([]redis.Z, 0, len(accountIDs))
	for idx, accountID := range accountIDs {
		members = append(members, redis.Z{
			Score:  float64(idx),
			Member: strconv.FormatInt(accountID, 10),
		})
	}
	return members
}

func (c *schedulerCache) writeSnapshotMembers(ctx context.Context, bucket service.SchedulerBucket, version string, members []redis.Z) error {
	if len(members) == 0 {
		return nil
	}
	snapshotKey := schedulerSnapshotKey(bucket, version)
	pipe := c.rdb.Pipeline()
	for start := 0; start < len(members); start += c.writeChunkSize {
		end := start + c.writeChunkSize
		if end > len(members) {
			end = len(members)
		}
		pipe.ZAdd(ctx, snapshotKey, members[start:end]...)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (c *schedulerCache) activateSnapshotVersion(ctx context.Context, bucket service.SchedulerBucket, token service.SchedulerBucketWriteToken, version string) error {
	// Phase 2: 原子 CAS 激活版本。
	// Lua 脚本保证：仅当新版本 >= 当前激活版本时才切换 active 指针，
	// 防止并发写入导致版本回滚。
	// 旧快照使用 EXPIRE 宽限期而非立即 DEL，避免 reader 竞态。
	activeKey := schedulerBucketKey(schedulerActivePrefix, bucket)
	readyKey := schedulerBucketKey(schedulerReadyPrefix, bucket)
	snapshotKeyPrefix := fmt.Sprintf("%s%d:%s:%s:v", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode)
	snapshotKey := schedulerSnapshotKey(bucket, version)

	keys := []string{
		activeKey,
		readyKey,
		schedulerBucketSetKey,
		snapshotKey,
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
	}
	args := []any{version, bucket.String(), snapshotKeyPrefix, snapshotGraceTTLSeconds, token.Epoch}

	result, err := activateSnapshotScript.Run(ctx, c.rdb, keys, args...).Int64()
	if err != nil {
		return err
	}
	return schedulerBucketWriteResultError(result, bucket)
}

func (c *schedulerCache) RetireBucket(ctx context.Context, bucket service.SchedulerBucket) error {
	snapshotKeyPrefix := fmt.Sprintf("%s%d:%s:%s:v", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode)
	result, err := retireBucketScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
		schedulerBucketSetKey,
		schedulerBucketKey(schedulerReadyPrefix, bucket),
		schedulerBucketKey(schedulerActivePrefix, bucket),
	}, bucket.String(), snapshotKeyPrefix, snapshotGraceTTLSeconds).Int64()
	if err != nil {
		return err
	}
	if result < 1 {
		return fmt.Errorf("retire scheduler bucket %s returned invalid epoch %d", bucket.String(), result)
	}
	return nil
}

func (c *schedulerCache) ReopenBucket(ctx context.Context, bucket service.SchedulerBucket) (service.SchedulerBucketWriteToken, error) {
	snapshotKeyPrefix := fmt.Sprintf("%s%d:%s:%s:v", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode)
	result, err := reopenBucketScript.Run(ctx, c.rdb, []string{
		schedulerBucketKey(schedulerEpochPrefix, bucket),
		schedulerBucketKey(schedulerRetiredPrefix, bucket),
		schedulerBucketSetKey,
		schedulerBucketKey(schedulerReadyPrefix, bucket),
		schedulerBucketKey(schedulerActivePrefix, bucket),
	}, bucket.String(), snapshotKeyPrefix, snapshotGraceTTLSeconds).Int64()
	if err != nil {
		return service.SchedulerBucketWriteToken{}, err
	}
	if err := schedulerBucketWriteResultError(result, bucket); err != nil {
		return service.SchedulerBucketWriteToken{}, err
	}
	return service.SchedulerBucketWriteToken{Bucket: bucket, Epoch: result}, nil
}

func (c *schedulerCache) TryAcquireGroupLifecycleLease(ctx context.Context, groupID int64, ttl time.Duration) (service.SchedulerGroupLifecycleLease, bool, error) {
	if groupID <= 0 {
		return service.SchedulerGroupLifecycleLease{}, false, fmt.Errorf("%w: group id must be positive", service.ErrSchedulerGroupLifecycleLeaseInvalid)
	}
	if ttl <= 0 {
		return service.SchedulerGroupLifecycleLease{}, false, fmt.Errorf("%w: ttl must be positive", service.ErrSchedulerGroupLifecycleLeaseInvalid)
	}
	ownerToken, err := newSchedulerGroupLifecycleOwnerToken()
	if err != nil {
		return service.SchedulerGroupLifecycleLease{}, false, err
	}
	acquired, err := c.rdb.SetNX(ctx, schedulerGroupLifecycleLockKey(groupID), ownerToken, ttl).Result()
	if err != nil {
		return service.SchedulerGroupLifecycleLease{}, false, err
	}
	if !acquired {
		return service.SchedulerGroupLifecycleLease{}, false, nil
	}
	return service.SchedulerGroupLifecycleLease{GroupID: groupID, OwnerToken: ownerToken}, true, nil
}

func (c *schedulerCache) ReleaseGroupLifecycleLease(ctx context.Context, lease service.SchedulerGroupLifecycleLease) error {
	if !lease.ValidFor(lease.GroupID) {
		return service.ErrSchedulerGroupLifecycleLeaseInvalid
	}
	result, err := releaseGroupLifecycleLeaseScript.Run(
		ctx,
		c.rdb,
		[]string{schedulerGroupLifecycleLockKey(lease.GroupID)},
		lease.OwnerToken,
	).Int64()
	if err != nil {
		return err
	}
	if result == 0 {
		return fmt.Errorf("%w: group=%d", service.ErrSchedulerGroupLifecycleLeaseLost, lease.GroupID)
	}
	if result != 1 {
		return fmt.Errorf("release scheduler group lifecycle lease returned %d", result)
	}
	return nil
}

func newSchedulerGroupLifecycleOwnerToken() (string, error) {
	raw := make([]byte, schedulerGroupLifecycleOwnerTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate scheduler group lifecycle owner token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func schedulerBucketWriteResultError(result int64, bucket service.SchedulerBucket) error {
	switch result {
	case -1:
		return fmt.Errorf("%w: bucket=%s", service.ErrSchedulerBucketRetired, bucket.String())
	case -2:
		return fmt.Errorf("%w: bucket=%s", service.ErrSchedulerBucketWriteFenced, bucket.String())
	default:
		return nil
	}
}

func (c *schedulerCache) GetAccount(ctx context.Context, accountID int64) (*service.Account, error) {
	id := strconv.FormatInt(accountID, 10)
	values, err := c.rdb.MGet(ctx, schedulerAccountKey(id), schedulerLastUsedKey(id)).Result()
	if err != nil {
		return nil, err
	}
	if len(values) != 2 || values[0] == nil {
		return nil, nil
	}
	account, err := decodeCachedAccount(values[0])
	if err != nil {
		return nil, err
	}
	if err := applySchedulerLastUsed(account, values[1]); err != nil {
		return nil, err
	}
	return account, nil
}

func (c *schedulerCache) GetAccountConcurrencyLimits(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	limits := make(map[int64]int, len(accountIDs))
	uniqueIDs := make([]int64, 0, len(accountIDs))
	keys := make([]string, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		uniqueIDs = append(uniqueIDs, accountID)
		keys = append(keys, schedulerAccountMetaKey(strconv.FormatInt(accountID, 10)))
	}
	if len(keys) == 0 {
		return limits, nil
	}

	values, err := c.mgetChunked(ctx, keys)
	if err != nil {
		return nil, err
	}
	for index, value := range values {
		if value == nil {
			continue
		}
		account, decodeErr := decodeCachedAccount(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		limits[uniqueIDs[index]] = account.Concurrency
	}
	return limits, nil
}

func (c *schedulerCache) SetAccount(ctx context.Context, account *service.Account) error {
	if account == nil || account.ID <= 0 {
		return nil
	}
	accountIDs, err := c.writeAccountIDs(ctx, []service.Account{*account})
	if err != nil {
		return err
	}
	if len(accountIDs) == 0 {
		return c.DeleteAccount(ctx, account.ID)
	}
	return nil
}

func (c *schedulerCache) GetGroup(ctx context.Context, groupID int64) (*service.Group, error) {
	if groupID <= 0 {
		return nil, nil
	}
	val, err := c.rdb.Get(ctx, schedulerGroupKey(strconv.FormatInt(groupID, 10))).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var group service.Group
	if err := json.Unmarshal([]byte(val), &group); err != nil {
		return nil, err
	}
	return &group, nil
}

func (c *schedulerCache) SetGroup(ctx context.Context, group *service.Group) error {
	if group == nil || group.ID <= 0 {
		return nil
	}
	payload, err := json.Marshal(buildSchedulerMetadataGroup(*group))
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, schedulerGroupKey(strconv.FormatInt(group.ID, 10)), payload, 0).Err()
}

func (c *schedulerCache) DeleteGroup(ctx context.Context, groupID int64) error {
	if groupID <= 0 {
		return nil
	}
	return c.rdb.Del(ctx, schedulerGroupKey(strconv.FormatInt(groupID, 10))).Err()
}

func (c *schedulerCache) DeleteAccount(ctx context.Context, accountID int64) error {
	if accountID <= 0 {
		return nil
	}
	id := strconv.FormatInt(accountID, 10)
	return c.rdb.Del(ctx, schedulerAccountKey(id), schedulerAccountMetaKey(id), schedulerLastUsedKey(id)).Err()
}

func (c *schedulerCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	if len(updates) == 0 {
		return nil
	}

	pipe := c.rdb.Pipeline()
	queued := 0
	keys := make([]string, 0, schedulerLastUsedUpdateChunkSize*2)
	args := make([]any, 0, schedulerLastUsedUpdateChunkSize)
	queueBatch := func() {
		if len(args) == 0 {
			return
		}
		updateSchedulerLastUsedScript.Eval(ctx, pipe, keys, args...)
		queued++
		keys = make([]string, 0, schedulerLastUsedUpdateChunkSize*2)
		args = make([]any, 0, schedulerLastUsedUpdateChunkSize)
	}
	for id, usedAt := range updates {
		if id <= 0 {
			continue
		}
		millis, err := schedulerLastUsedMillis(usedAt)
		if err != nil {
			slog.Warn("scheduler cache removes account with invalid last-used timestamp", "account_id", id, "error", err)
			idText := strconv.FormatInt(id, 10)
			pipe.Del(ctx, schedulerAccountKey(idText), schedulerAccountMetaKey(idText), schedulerLastUsedKey(idText))
			queued++
			continue
		}
		idText := strconv.FormatInt(id, 10)
		keys = append(keys, schedulerAccountKey(idText), schedulerLastUsedKey(idText))
		args = append(args, millis)
		if len(args) >= schedulerLastUsedUpdateChunkSize {
			queueBatch()
		}
	}
	queueBatch()
	if queued == 0 {
		return nil
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (c *schedulerCache) TryLockBucket(ctx context.Context, bucket service.SchedulerBucket, ttl time.Duration) (bool, error) {
	key := schedulerBucketKey(schedulerLockPrefix, bucket)
	return c.rdb.SetNX(ctx, key, time.Now().UnixNano(), ttl).Result()
}

func (c *schedulerCache) UnlockBucket(ctx context.Context, bucket service.SchedulerBucket) error {
	key := schedulerBucketKey(schedulerLockPrefix, bucket)
	return c.rdb.Del(ctx, key).Err()
}

func (c *schedulerCache) ListBuckets(ctx context.Context) ([]service.SchedulerBucket, error) {
	raw, err := c.rdb.SMembers(ctx, schedulerBucketSetKey).Result()
	if err != nil {
		return nil, err
	}
	out := make([]service.SchedulerBucket, 0, len(raw))
	for _, entry := range raw {
		bucket, ok := service.ParseSchedulerBucket(entry)
		if !ok {
			continue
		}
		out = append(out, bucket)
	}
	return out, nil
}

func (c *schedulerCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	val, err := c.rdb.Get(ctx, schedulerOutboxWatermarkKey).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (c *schedulerCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	return c.rdb.Set(ctx, schedulerOutboxWatermarkKey, strconv.FormatInt(id, 10), 0).Err()
}

func schedulerBucketKey(prefix string, bucket service.SchedulerBucket) string {
	return fmt.Sprintf("%s%d:%s:%s", prefix, bucket.GroupID, bucket.Platform, bucket.Mode)
}

func schedulerGroupLifecycleLockKey(groupID int64) string {
	return schedulerGroupLifecycleLockPrefix + strconv.FormatInt(groupID, 10)
}

func schedulerSnapshotKey(bucket service.SchedulerBucket, version string) string {
	return fmt.Sprintf("%s%d:%s:%s:v%s", schedulerSnapshotPrefix, bucket.GroupID, bucket.Platform, bucket.Mode, version)
}

func schedulerAccountKey(id string) string {
	return schedulerAccountPrefix + id
}

func schedulerAccountMetaKey(id string) string {
	return schedulerAccountMetaPrefix + id
}

func schedulerLastUsedKey(id string) string {
	return schedulerAccountLastUsedPrefix + id
}

func schedulerGroupKey(id string) string {
	return schedulerGroupPrefix + id
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func schedulerLastUsedMillis(value time.Time) (int64, error) {
	// MarshalJSON 会拒绝 time.Time 无法表达的年份，避免错误时间污染 Redis。
	if _, err := value.MarshalJSON(); err != nil {
		return 0, err
	}
	return value.UTC().UnixMilli(), nil
}

func applySchedulerLastUsed(account *service.Account, value any) error {
	if account == nil || value == nil {
		return nil
	}
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case []byte:
		raw = string(typed)
	default:
		return fmt.Errorf("unexpected last_used cache type: %T", value)
	}
	millis, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid last_used cache value %q: %w", raw, err)
	}
	lastUsedAt := time.UnixMilli(millis).UTC()
	if account.LastUsedAt == nil || lastUsedAt.After(*account.LastUsedAt) {
		account.LastUsedAt = ptrTime(lastUsedAt)
	}
	return nil
}

func decodeCachedAccount(val any) (*service.Account, error) {
	var payload []byte
	switch raw := val.(type) {
	case string:
		payload = []byte(raw)
	case []byte:
		payload = raw
	default:
		return nil, fmt.Errorf("unexpected account cache type: %T", val)
	}
	var account service.Account
	if err := json.Unmarshal(payload, &account); err != nil {
		return nil, err
	}
	return &account, nil
}

func (c *schedulerCache) writeAccountIDs(ctx context.Context, accounts []service.Account) ([]int64, error) {
	if len(accounts) == 0 {
		return nil, nil
	}

	pipe := c.rdb.Pipeline()
	accountIDs := make([]int64, 0, len(accounts))
	pending := 0
	flush := func() error {
		if pending == 0 {
			return nil
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		pipe = c.rdb.Pipeline()
		pending = 0
		return nil
	}

	for _, account := range accounts {
		if gatewayLiteMirrorSnapshotAccount(account) {
			existing, err := c.GetAccount(ctx, account.ID)
			if err != nil {
				return nil, err
			}
			account.Credentials = mergeGatewayLiteMirrorCredentials(account.Credentials, existing)
			account.Extra = mergeGatewayLiteMirrorExtra(account.Extra, existing)
		}
		fullPayload, metaPayload, err := marshalSchedulerCacheAccount(account)
		if err != nil {
			slog.Warn("scheduler cache skips account with unencodable payload", "account_id", account.ID, "error", err)
			id := strconv.FormatInt(account.ID, 10)
			pipe.Del(ctx, schedulerAccountKey(id), schedulerAccountMetaKey(id), schedulerLastUsedKey(id))
			pending++
			continue
		}

		id := strconv.FormatInt(account.ID, 10)
		pipe.Set(ctx, schedulerAccountKey(id), fullPayload, 0)
		pipe.Set(ctx, schedulerAccountMetaKey(id), metaPayload, 0)
		// 不触碰 LastUsedAt 的 side key：落后的快照重建不能覆盖热路径刚写入的新时间。
		accountIDs = append(accountIDs, account.ID)
		pending++
		if pending >= c.writeChunkSize {
			if err := flush(); err != nil {
				return nil, err
			}
		}
	}

	if err := flush(); err != nil {
		return nil, err
	}
	return accountIDs, nil
}

func marshalSchedulerCacheAccount(account service.Account) ([]byte, []byte, error) {
	fullPayload, err := json.Marshal(account)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal account: %w", err)
	}
	metaPayload, err := json.Marshal(buildSchedulerMetadataAccount(account))
	if err != nil {
		return nil, nil, fmt.Errorf("marshal account metadata: %w", err)
	}
	return fullPayload, metaPayload, nil
}

func gatewayLiteMirrorSnapshotAccount(account service.Account) bool {
	if len(account.Extra) == 0 {
		return false
	}
	if _, ok := account.Extra["gateway_upstream_mirror_id"]; ok {
		return true
	}
	if _, ok := account.Extra["gateway_node_id"]; ok {
		return true
	}
	return false
}

func mergeGatewayLiteMirrorCredentials(incoming map[string]any, existing *service.Account) map[string]any {
	if existing == nil || len(existing.Credentials) == 0 {
		return incoming
	}
	merged := make(map[string]any, len(existing.Credentials)+len(incoming))
	for key, value := range existing.Credentials {
		if value != nil {
			merged[key] = value
		}
	}
	for key, value := range incoming {
		if value != nil {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func mergeGatewayLiteMirrorExtra(incoming map[string]any, existing *service.Account) map[string]any {
	merged := make(map[string]any, len(incoming)+3)
	for key, value := range incoming {
		if value != nil {
			merged[key] = value
		}
	}
	if existing == nil || len(existing.Extra) == 0 {
		if len(merged) == 0 {
			return nil
		}
		return merged
	}

	// 覆写属于账号配置：主站显式传值/传 null 时生效，缺省时保留节点值。
	// 探测快照与 billing 证据属于节点运行时事实，镜像同步不能用主站的
	// 缺失或陈旧副本覆盖，否则 cache-only 调度会错误恢复无媒体权限账号。
	if _, provided := incoming[service.GrokMediaEligibleExtraKey]; !provided {
		if value, ok := existing.Extra[service.GrokMediaEligibleExtraKey]; ok && value != nil {
			merged[service.GrokMediaEligibleExtraKey] = value
		}
	}
	for _, key := range []string{service.GrokMediaEligibilitySnapshotExtraKey, "grok_billing_snapshot"} {
		if value, ok := existing.Extra[key]; ok && value != nil {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func (c *schedulerCache) mgetChunked(ctx context.Context, keys []string) ([]any, error) {
	if len(keys) == 0 {
		return []any{}, nil
	}

	out := make([]any, 0, len(keys))
	chunkSize := c.mgetChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultSchedulerSnapshotMGetChunkSize
	}
	for start := 0; start < len(keys); start += chunkSize {
		end := start + chunkSize
		if end > len(keys) {
			end = len(keys)
		}
		part, err := c.rdb.MGet(ctx, keys[start:end]...).Result()
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func buildSchedulerMetadataAccount(account service.Account) service.Account {
	return service.Account{
		ID:                      account.ID,
		Name:                    account.Name,
		Platform:                account.Platform,
		Type:                    account.Type,
		Concurrency:             account.Concurrency,
		LoadFactor:              account.LoadFactor,
		Priority:                account.Priority,
		RateMultiplier:          account.RateMultiplier,
		Status:                  account.Status,
		LastUsedAt:              account.LastUsedAt,
		ExpiresAt:               account.ExpiresAt,
		AutoPauseOnExpired:      account.AutoPauseOnExpired,
		Schedulable:             account.Schedulable,
		RateLimitedAt:           account.RateLimitedAt,
		RateLimitResetAt:        account.RateLimitResetAt,
		OverloadUntil:           account.OverloadUntil,
		TempUnschedulableUntil:  account.TempUnschedulableUntil,
		TempUnschedulableReason: account.TempUnschedulableReason,
		SessionWindowStart:      account.SessionWindowStart,
		SessionWindowEnd:        account.SessionWindowEnd,
		SessionWindowStatus:     account.SessionWindowStatus,
		ParentAccountID:         account.ParentAccountID,
		QuotaDimension:          account.QuotaDimensionOrDefault(),
		AccountGroups:           filterSchedulerAccountGroups(account.AccountGroups),
		GroupIDs:                filterSchedulerGroupIDs(account.GroupIDs, account.AccountGroups),
		Groups:                  filterSchedulerGroups(account.Groups),
		Credentials:             filterSchedulerCredentials(account.Credentials),
		Extra:                   filterSchedulerExtra(account.Extra),
	}
}

func buildSchedulerMetadataGroup(group service.Group) service.Group {
	return service.Group{
		ID:                              group.ID,
		Name:                            group.Name,
		Platform:                        group.Platform,
		RateMultiplier:                  group.RateMultiplier,
		PeakRateEnabled:                 group.PeakRateEnabled,
		PeakStart:                       group.PeakStart,
		PeakEnd:                         group.PeakEnd,
		PeakRateMultiplier:              group.PeakRateMultiplier,
		PeakTimezone:                    group.PeakTimezone,
		IsExclusive:                     group.IsExclusive,
		LocalOnly:                       group.LocalOnly,
		Status:                          group.Status,
		SchedulingMode:                  group.SchedulingMode,
		PerUserConcurrency:              group.PerUserConcurrency,
		Hydrated:                        group.Hydrated,
		SubscriptionType:                group.SubscriptionType,
		DailyLimitUSD:                   group.DailyLimitUSD,
		WeeklyLimitUSD:                  group.WeeklyLimitUSD,
		MonthlyLimitUSD:                 group.MonthlyLimitUSD,
		AllowImageGeneration:            group.AllowImageGeneration,
		ImageRateIndependent:            group.ImageRateIndependent,
		ImageRateMultiplier:             group.ImageRateMultiplier,
		ImagePrice1K:                    group.ImagePrice1K,
		ImagePrice2K:                    group.ImagePrice2K,
		ImagePrice4K:                    group.ImagePrice4K,
		VideoRateIndependent:            group.VideoRateIndependent,
		VideoRateMultiplier:             group.VideoRateMultiplier,
		VideoPrice480P:                  group.VideoPrice480P,
		VideoPrice720P:                  group.VideoPrice720P,
		VideoPrice1080P:                 group.VideoPrice1080P,
		WebSearchPricePerCall:           group.WebSearchPricePerCall,
		AllowOpenAILongContextBilling:   group.AllowOpenAILongContextBilling,
		AllowBlockedKeywordRequests:     group.AllowBlockedKeywordRequests,
		ClaudeCodeOnly:                  group.ClaudeCodeOnly,
		FallbackGroupID:                 group.FallbackGroupID,
		FallbackGroupIDOnInvalidRequest: group.FallbackGroupIDOnInvalidRequest,
		FailoverGroupID:                 group.FailoverGroupID,
		FailoverStrategy:                group.FailoverStrategy,
		ModelRouting:                    group.ModelRouting,
		ModelRoutingEnabled:             group.ModelRoutingEnabled,
		ModelMapping:                    group.ModelMapping,
		WeightedModelMapping:            service.CloneWeightedModelMapping(group.WeightedModelMapping),
		BillingModelSource:              group.BillingModelSource,
		MCPXMLInject:                    group.MCPXMLInject,
		SupportedModelScopes:            group.SupportedModelScopes,
		AllowMessagesDispatch:           group.AllowMessagesDispatch,
		AllowLive:                       group.AllowLive,
		RequireOAuthOnly:                group.RequireOAuthOnly,
		RequirePrivacySet:               group.RequirePrivacySet,
		DefaultMappedModel:              group.DefaultMappedModel,
		MessagesDispatchModelConfig:     group.MessagesDispatchModelConfig,
		ModelsListConfig:                group.ModelsListConfig,
		RPMLimit:                        group.RPMLimit,
		MaxReasoningEffort:              group.MaxReasoningEffort,
		ReasoningEffortMappings:         append([]service.ReasoningEffortMapping(nil), group.ReasoningEffortMappings...),
	}
}

func filterSchedulerGroups(groups []*service.Group) []*service.Group {
	if len(groups) == 0 {
		return nil
	}
	filtered := make([]*service.Group, 0, len(groups))
	for _, group := range groups {
		if group == nil || group.ID <= 0 {
			continue
		}
		meta := buildSchedulerMetadataGroup(*group)
		filtered = append(filtered, &meta)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerAccountGroups(accountGroups []service.AccountGroup) []service.AccountGroup {
	if len(accountGroups) == 0 {
		return nil
	}

	filtered := make([]service.AccountGroup, 0, len(accountGroups))
	for _, ag := range accountGroups {
		if ag.GroupID <= 0 {
			continue
		}
		filtered = append(filtered, service.AccountGroup{
			AccountID: ag.AccountID,
			GroupID:   ag.GroupID,
			Priority:  ag.Priority,
			CreatedAt: ag.CreatedAt,
		})
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerGroupIDs(groupIDs []int64, accountGroups []service.AccountGroup) []int64 {
	if len(groupIDs) == 0 && len(accountGroups) == 0 {
		return nil
	}

	seen := make(map[int64]struct{}, len(groupIDs)+len(accountGroups))
	filtered := make([]int64, 0, len(groupIDs)+len(accountGroups))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	for _, ag := range accountGroups {
		if ag.GroupID <= 0 {
			continue
		}
		if _, ok := seen[ag.GroupID]; ok {
			continue
		}
		seen[ag.GroupID] = struct{}{}
		filtered = append(filtered, ag.GroupID)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerCredentials(credentials map[string]any) map[string]any {
	if len(credentials) == 0 {
		return nil
	}
	// 调度元数据只保留转发热路径需要的字段。base_url 必须保留，
	// 否则 gateway-lite 从快照选中账号后无法转发到自定义上游。
	// 能力声明参与选号；遗漏会拒绝 Seedance，并把显式禁止的文本能力误当默认放行。
	keys := []string{"model_mapping", "api_key", "base_url", "project_id", "oauth_type", "openai_capabilities"}
	filtered := make(map[string]any)
	for _, key := range keys {
		if value, ok := credentials[key]; ok && value != nil {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func filterSchedulerExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	keys := []string{
		"quota_limit",
		"quota_used",
		"quota_daily_limit",
		"quota_daily_used",
		"quota_daily_start",
		"quota_daily_reset_mode",
		"quota_daily_reset_hour",
		"quota_weekly_limit",
		"quota_weekly_used",
		"quota_weekly_start",
		"quota_weekly_reset_mode",
		"quota_weekly_reset_day",
		"quota_weekly_reset_hour",
		"quota_reset_timezone",
		"mixed_scheduling",
		"window_cost_limit",
		"window_cost_sticky_reserve",
		"max_sessions",
		"session_idle_timeout_minutes",
		"openai_oauth_responses_websockets_v2_enabled",
		"openai_oauth_responses_websockets_v2_mode",
		"openai_apikey_responses_websockets_v2_enabled",
		"openai_apikey_responses_websockets_v2_mode",
		"responses_websockets_v2_enabled",
		"openai_ws_enabled",
		"openai_ws_force_http",
		"openai_responses_mode",
		"openai_responses_supported",
		// 透传开关必须进投影：候选过滤(ListSchedulableAccounts)读的是本投影，
		// 而 Account.IsModelSupported 靠 extra 上的这两个键短路 model_mapping 白名单。
		// 裁掉它们，透传账号在选号阶段会退回按(常为过期的)白名单判定并被误判为
		// model_not_supported —— 转发阶段却仍按透传工作，表现为"单独测账号能通、
		// 走网关报 no available accounts"。
		"openai_passthrough",
		"openai_oauth_passthrough",
		"openai_long_context_billing_enabled",
		service.KeywordPolicyModeExtraKey,
		"codex_5h_used_percent",
		"codex_7d_used_percent",
		"codex_5h_reset_at",
		"codex_7d_reset_at",
		"codex_5h_reset_after_seconds",
		"codex_7d_reset_after_seconds",
		"codex_usage_updated_at",
		"auto_pause_5h_threshold",
		"auto_pause_7d_threshold",
		"auto_pause_5h_disabled",
		"auto_pause_7d_disabled",
		"model_rate_limits",
		service.GrokMediaEligibleExtraKey,
		service.GrokMediaEligibilitySnapshotExtraKey,
		"grok_billing_snapshot",
	}
	filtered := make(map[string]any)
	for _, key := range keys {
		if value, ok := extra[key]; ok && value != nil {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}
