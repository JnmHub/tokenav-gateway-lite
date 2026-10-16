package gatewaylite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultKeySnapshotTTL = 30 * time.Second
const keyUserIndexTTL = 24 * time.Hour
const keyOrganizationIndexTTL = 24 * time.Hour
const keySnapshotHotReadTTL = 500 * time.Millisecond
const keySnapshotHotMaxEntries = 4096

type keySnapshotHotEntry struct {
	raw string
	// snapshot 是与 raw 同一时刻解码出的只读视图。模型请求中间件只读取
	// 它，不允许修改；这样同一热 Key 不必在 Peek + Get 阶段重复 JSON 解码。
	snapshot       *KeySnapshot
	expiresAt      int64
	userID         int64
	organizationID int64
	region         string
	prefix         string
}

type keySnapshotHotCache struct {
	mu          sync.RWMutex
	entries     map[string]keySnapshotHotEntry
	generations map[string]int64
}

// KeyCacheFillToken 固定一次回源开始时看到的缓存失效代次。字段不导出，
// 避免调用方自行拼造 token（回填凭证）绕过失效门禁。
type KeyCacheFillToken struct {
	prefix     string
	region     string
	generation int64
}

var setKeySnapshotIfGenerationUnchangedScript = redis.NewScript(`
local current_generation = redis.call('GET', KEYS[1])
if not current_generation then
  current_generation = '0'
end
if tostring(current_generation) ~= ARGV[1] then
  return 0
end

redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
if ARGV[7] == '1' then
  redis.call('SADD', KEYS[3], ARGV[4])
  redis.call('PEXPIRE', KEYS[3], ARGV[5])
end
if ARGV[8] == '1' then
  redis.call('SADD', KEYS[4], ARGV[4])
  redis.call('PEXPIRE', KEYS[4], ARGV[6])
end
return 1
`)

type RedisKeyCache struct {
	client      *redis.Client
	prefixValue atomic.Value
	hot         *keySnapshotHotCache
}

func NewRedisKeyCache(client *redis.Client, prefix string) *RedisKeyCache {
	cache := &RedisKeyCache{
		client: client,
		hot: &keySnapshotHotCache{
			entries:     make(map[string]keySnapshotHotEntry),
			generations: make(map[string]int64),
		},
	}
	cache.SetPrefix(prefix)
	return cache
}

func (c *RedisKeyCache) Enabled() bool {
	return c != nil && c.client != nil
}

func (c *RedisKeyCache) WithPrefix(prefix string) *RedisKeyCache {
	if c == nil {
		return nil
	}
	cache := &RedisKeyCache{client: c.client, hot: c.hot}
	cache.SetPrefix(prefix)
	return cache
}

func (c *RedisKeyCache) Get(ctx context.Context, keyID, region string) (*KeySnapshot, bool, error) {
	if !c.Enabled() {
		return nil, false, nil
	}
	if keyID == "" || region == "" {
		return nil, false, errors.New("invalid key cache lookup")
	}
	cacheKey := c.cacheKey(keyID, region)
	if entry, ok := c.getHot(cacheKey); ok {
		return decodeKeySnapshot(entry.raw)
	}
	raw, err := c.client.Get(ctx, cacheKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, err
	}
	snapshot, ok, err := decodeKeySnapshot(raw)
	if err != nil {
		_ = c.client.Del(ctx, cacheKey).Err()
		return nil, false, nil
	}
	c.setHot(cacheKey, raw, snapshot, region)
	return snapshot, ok, nil
}

// GetImmutable 返回仅供请求热路径读取的共享快照。调用方不得修改返回值或
// 其中的 slice/map；停用、删除与 TTL 仍由同一份 hot cache 精确失效。
// 普通管理/测试调用继续使用 Get，可保留“每次独立解码”的可变值语义。
func (c *RedisKeyCache) GetImmutable(ctx context.Context, keyID, region string) (*KeySnapshot, bool, error) {
	if !c.Enabled() {
		return nil, false, nil
	}
	if keyID == "" || region == "" {
		return nil, false, errors.New("invalid key cache lookup")
	}
	cacheKey := c.cacheKey(keyID, region)
	if entry, ok := c.getHot(cacheKey); ok && entry.snapshot != nil {
		return entry.snapshot, true, nil
	}
	raw, err := c.client.Get(ctx, cacheKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, nil
		}
		return nil, false, err
	}
	snapshot, ok, err := decodeKeySnapshot(raw)
	if err != nil {
		_ = c.client.Del(ctx, cacheKey).Err()
		return nil, false, nil
	}
	c.setHot(cacheKey, raw, snapshot, region)
	return snapshot, ok, nil
}

// PeekHot 只读取进程内 500ms 正缓存，不访问 Redis。入口鉴权用它先确认
// 已知有效 Key，避免同一 NAT 偶发非法请求后，让已经缓存的合法请求等待
// invalid-auth（非法鉴权）跨节点 Redis 检查。最终授权仍会走 Get，停用和
// 删除事件也会同步清除此热缓存，因此这里不能替代正式鉴权。
func (c *RedisKeyCache) PeekHot(keyID, region string) (*KeySnapshot, bool) {
	if c == nil || keyID == "" || region == "" {
		return nil, false
	}
	cacheKey := c.cacheKey(keyID, region)
	entry, ok := c.getHot(cacheKey)
	if !ok {
		return nil, false
	}
	snapshot, ok, err := decodeKeySnapshot(entry.raw)
	if err != nil || !ok || snapshot == nil {
		c.deleteHotKey(cacheKey)
		return nil, false
	}
	return snapshot, true
}

// PeekHotImmutable 是 GetImmutable 的无 Redis 探测版本，只供鉴权中间件在
// invalid-auth（非法鉴权）检查前验证已知正缓存。返回值同样必须只读。
func (c *RedisKeyCache) PeekHotImmutable(keyID, region string) (*KeySnapshot, bool) {
	if c == nil || keyID == "" || region == "" {
		return nil, false
	}
	entry, ok := c.getHot(c.cacheKey(keyID, region))
	if !ok || entry.snapshot == nil {
		return nil, false
	}
	return entry.snapshot, true
}

func (c *RedisKeyCache) Set(ctx context.Context, key KeySnapshot, region string) error {
	if key.TokenUse == "agent_api_key" {
		return nil
	}
	token, err := c.BeginFill(ctx, region)
	if err != nil {
		return err
	}
	_, err = c.SetIfGenerationUnchanged(ctx, key, region, token)
	return err
}

// BeginFill 在访问 control-plane（主站控制面）前固定共享失效代次。它只在
// Redis miss（缓存未命中）的回源路径执行，不会给 500ms 热缓存命中增加调用。
func (c *RedisKeyCache) BeginFill(ctx context.Context, region string) (KeyCacheFillToken, error) {
	if !c.Enabled() {
		return KeyCacheFillToken{}, nil
	}
	if region == "" {
		return KeyCacheFillToken{}, errors.New("invalid key cache region")
	}
	prefix := c.prefix()
	generationRaw, err := c.client.Get(ctx, keyGenerationKey(prefix, region)).Result()
	if errors.Is(err, redis.Nil) {
		generationRaw = "0"
		err = nil
	}
	if err != nil {
		return KeyCacheFillToken{}, err
	}
	generation, err := strconv.ParseInt(generationRaw, 10, 64)
	if err != nil || generation < 0 {
		return KeyCacheFillToken{}, errors.New("invalid key cache generation")
	}
	c.observeHotGeneration(prefix, region, generation)
	return KeyCacheFillToken{prefix: prefix, region: region, generation: generation}, nil
}

// SetIfGenerationUnchanged 只允许失效代次未变化的回源结果写入缓存。Lua 将
// 代次比较、快照和索引写入合成一个 Redis 原子操作，避免企业白名单收紧后，
// 已在途的旧 ResolveKey（密钥解析）响应重新获得最长一个缓存周期的授权。
func (c *RedisKeyCache) SetIfGenerationUnchanged(ctx context.Context, key KeySnapshot, region string, token KeyCacheFillToken) (bool, error) {
	// 用途而非 TTL 决定实时核验。即使回填开始时尚未知用途，主站返回
	// Agent Key 后也不能写 Redis/索引/热缓存；失效通知到达前亦不能复用。
	if key.TokenUse == "agent_api_key" {
		return false, nil
	}
	if !c.Enabled() {
		return true, nil
	}
	if key.KeyID == "" || region == "" || token.prefix == "" || token.region != region {
		return false, errors.New("invalid key cache snapshot")
	}
	// prefix（缓存前缀）运行时切换后，旧命名空间取得的 token 不能跨空间写入。
	if token.prefix != c.prefix() {
		return false, nil
	}
	if !c.hotGenerationUnchanged(token) {
		return false, nil
	}
	ttl := time.Duration(key.CacheTTLSecond) * time.Second
	if ttl <= 0 {
		ttl = defaultKeySnapshotTTL
	}
	body, err := json.Marshal(key)
	if err != nil {
		return false, err
	}
	generationKey := keyGenerationKey(token.prefix, region)
	userIndexKey := generationKey
	organizationIndexKey := generationKey
	hasUserIndex := "0"
	hasOrganizationIndex := "0"
	if key.UserID > 0 {
		userIndexKey = userIndexKeyForPrefix(token.prefix, key.UserID, region)
		hasUserIndex = "1"
	}
	if key.OrganizationID > 0 {
		organizationIndexKey = organizationIndexKeyForPrefix(token.prefix, key.OrganizationID, region)
		hasOrganizationIndex = "1"
	}
	written, err := setKeySnapshotIfGenerationUnchangedScript.Run(ctx, c.client, []string{
		generationKey,
		cacheKeyForPrefix(token.prefix, key.KeyID, region),
		userIndexKey,
		organizationIndexKey,
	}, strconv.FormatInt(token.generation, 10), string(body), ttl.Milliseconds(), key.KeyID,
		keyUserIndexTTL.Milliseconds(), keyOrganizationIndexTTL.Milliseconds(), hasUserIndex, hasOrganizationIndex).Int64()
	if err != nil {
		return false, err
	}
	if written != 1 {
		return false, nil
	}
	// body 是 Redis 中的权威序列化形态；从它解码一次可确保热缓存不与
	// 调用方后续修改 key 的 slice/map 共享底层内存。
	hotSnapshot, _, err := decodeKeySnapshot(string(body))
	if err != nil {
		return false, err
	}
	if !c.setHotIfGenerationUnchanged(cacheKeyForPrefix(token.prefix, key.KeyID, region), string(body), hotSnapshot, token) {
		return false, nil
	}
	return true, nil
}

func (c *RedisKeyCache) DeleteRegion(ctx context.Context, region string) error {
	if !c.Enabled() {
		return nil
	}
	if region == "" {
		return errors.New("invalid key cache region")
	}
	if err := c.bumpKeyGeneration(ctx, region, func(entry keySnapshotHotEntry) bool {
		return entry.prefix == c.prefix() && entry.region == region
	}); err != nil {
		return err
	}
	pattern := fmt.Sprintf("%s:key:%s:*", c.prefix(), region)
	iter := c.client.Scan(ctx, 0, pattern, 100).Iterator()
	keys := make([]string, 0)
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	return c.client.Del(ctx, keys...).Err()
}

func (c *RedisKeyCache) DeleteKey(ctx context.Context, keyID, region string) error {
	if !c.Enabled() {
		return nil
	}
	if keyID == "" || region == "" {
		return errors.New("invalid key cache delete")
	}
	if err := c.bumpKeyGeneration(ctx, region, func(entry keySnapshotHotEntry) bool {
		return entry.prefix == c.prefix() && entry.region == region && entry.snapshot != nil && entry.snapshot.KeyID == keyID
	}); err != nil {
		return err
	}
	return c.client.Del(ctx, c.cacheKey(keyID, region)).Err()
}

func (c *RedisKeyCache) DeleteUser(ctx context.Context, userID int64, region string) error {
	if !c.Enabled() {
		return nil
	}
	if userID <= 0 || region == "" {
		return errors.New("invalid key cache user delete")
	}
	if err := c.bumpKeyGeneration(ctx, region, func(entry keySnapshotHotEntry) bool {
		return entry.prefix == c.prefix() && entry.region == region && entry.userID == userID
	}); err != nil {
		return err
	}
	indexKey := c.userIndexKey(userID, region)
	keyIDs, err := c.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	}
	keys := make([]string, 0, len(keyIDs)+1)
	for _, keyID := range keyIDs {
		if keyID == "" {
			continue
		}
		keys = append(keys, c.cacheKey(keyID, region))
	}
	keys = append(keys, indexKey)
	if len(keys) == 0 {
		return nil
	}
	return c.client.Del(ctx, keys...).Err()
}

func (c *RedisKeyCache) DeleteOrganization(ctx context.Context, organizationID int64, region string) error {
	if !c.Enabled() {
		return nil
	}
	if organizationID <= 0 || region == "" {
		return errors.New("invalid key cache organization delete")
	}
	if err := c.bumpKeyGeneration(ctx, region, func(entry keySnapshotHotEntry) bool {
		return entry.prefix == c.prefix() && entry.region == region && entry.organizationID == organizationID
	}); err != nil {
		return err
	}
	indexKey := c.organizationIndexKey(organizationID, region)
	keyIDs, err := c.client.SMembers(ctx, indexKey).Result()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(keyIDs)+1)
	for _, keyID := range keyIDs {
		if keyID != "" {
			keys = append(keys, c.cacheKey(keyID, region))
		}
	}
	// 即使企业当前没有缓存 Key，也删除索引本身；不使用整区 SCAN 兜底，
	// 避免一个企业策略变更误伤其它企业和个人请求。
	keys = append(keys, indexKey)
	return c.client.Del(ctx, keys...).Err()
}

func (c *RedisKeyCache) cacheKey(keyID, region string) string {
	return cacheKeyForPrefix(c.prefix(), keyID, region)
}

func (c *RedisKeyCache) userIndexKey(userID int64, region string) string {
	return fmt.Sprintf("%s:key-user:%s:%d", c.prefix(), region, userID)
}

func (c *RedisKeyCache) organizationIndexKey(organizationID int64, region string) string {
	return organizationIndexKeyForPrefix(c.prefix(), organizationID, region)
}

func cacheKeyForPrefix(prefix, keyID, region string) string {
	return fmt.Sprintf("%s:key:%s:%s", prefix, region, keyID)
}

func userIndexKeyForPrefix(prefix string, userID int64, region string) string {
	return fmt.Sprintf("%s:key-user:%s:%d", prefix, region, userID)
}

func organizationIndexKeyForPrefix(prefix string, organizationID int64, region string) string {
	return fmt.Sprintf("%s:key-organization:%s:%d", prefix, region, organizationID)
}

func keyGenerationKey(prefix, region string) string {
	return fmt.Sprintf("%s:key-generation:%s", prefix, region)
}

func hotGenerationKey(prefix, region string) string {
	return prefix + "\x00" + region
}

func (c *RedisKeyCache) SetPrefix(prefix string) {
	if c != nil {
		c.prefixValue.Store(NormalizeRedisPrefix(prefix))
	}
}

func (c *RedisKeyCache) prefix() string {
	if c == nil {
		return NormalizeRedisPrefix("")
	}
	if value := c.prefixValue.Load(); value != nil {
		if prefix, ok := value.(string); ok && prefix != "" {
			return prefix
		}
	}
	return NormalizeRedisPrefix("")
}

func (c *RedisKeyCache) Prefix() string {
	return c.prefix()
}

func decodeKeySnapshot(raw string) (*KeySnapshot, bool, error) {
	var snapshot KeySnapshot
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, false, err
	}
	// 拒绝持久层中已有的 Agent 快照，避免旧节点或并发回填留下的值
	// 在新请求里继续授权；其它/未知用途保留现有缓存语义。
	if snapshot.TokenUse == "agent_api_key" {
		return nil, false, nil
	}
	return &snapshot, true, nil
}

func (c *RedisKeyCache) getHot(key string) (keySnapshotHotEntry, bool) {
	if c == nil || c.hot == nil || key == "" {
		return keySnapshotHotEntry{}, false
	}
	now := time.Now().UnixNano()
	c.hot.mu.RLock()
	entry, ok := c.hot.entries[key]
	c.hot.mu.RUnlock()
	if !ok || entry.expiresAt <= now || (entry.snapshot != nil && entry.snapshot.TokenUse == "agent_api_key") {
		if ok {
			c.deleteHotKey(key)
		}
		return keySnapshotHotEntry{}, false
	}
	return entry, true
}

func (c *RedisKeyCache) setHot(key, raw string, snapshot *KeySnapshot, region string) {
	if c == nil || c.hot == nil || key == "" || raw == "" || snapshot == nil || snapshot.TokenUse == "agent_api_key" {
		return
	}
	now := time.Now()
	c.hot.mu.Lock()
	// 本地热缓存只用于消除同一波请求的重复 Redis GET；达到上限时先清理过期项，
	// 仍满则整体丢弃，宁可回源也不让大量低频 Key 常驻 4C4G 节点内存。
	if len(c.hot.entries) >= keySnapshotHotMaxEntries {
		for cachedKey, entry := range c.hot.entries {
			if entry.expiresAt <= now.UnixNano() {
				delete(c.hot.entries, cachedKey)
			}
		}
		if len(c.hot.entries) >= keySnapshotHotMaxEntries {
			clear(c.hot.entries)
		}
	}
	c.hot.entries[key] = keySnapshotHotEntry{
		raw:            raw,
		snapshot:       snapshot,
		expiresAt:      now.Add(keySnapshotHotReadTTL).UnixNano(),
		userID:         snapshot.UserID,
		organizationID: snapshot.OrganizationID,
		region:         region,
		prefix:         c.prefix(),
	}
	c.hot.mu.Unlock()
}

func (c *RedisKeyCache) observeHotGeneration(prefix, region string, generation int64) {
	if c == nil || c.hot == nil || prefix == "" || region == "" {
		return
	}
	c.hot.mu.Lock()
	generationKey := hotGenerationKey(prefix, region)
	if current := c.hot.generations[generationKey]; generation > current {
		c.hot.generations[generationKey] = generation
	}
	c.hot.mu.Unlock()
}

func (c *RedisKeyCache) hotGenerationUnchanged(token KeyCacheFillToken) bool {
	if c == nil || c.hot == nil {
		return true
	}
	c.hot.mu.RLock()
	unchanged := c.hot.generations[hotGenerationKey(token.prefix, token.region)] == token.generation
	c.hot.mu.RUnlock()
	return unchanged
}

func (c *RedisKeyCache) setHotIfGenerationUnchanged(key, raw string, snapshot *KeySnapshot, token KeyCacheFillToken) bool {
	if c == nil || c.hot == nil || key == "" || raw == "" || snapshot == nil || snapshot.TokenUse == "agent_api_key" {
		return false
	}
	now := time.Now()
	c.hot.mu.Lock()
	defer c.hot.mu.Unlock()
	if c.hot.generations[hotGenerationKey(token.prefix, token.region)] != token.generation {
		return false
	}
	c.pruneHotLocked(now)
	c.hot.entries[key] = keySnapshotHotEntry{
		raw:            raw,
		snapshot:       snapshot,
		expiresAt:      now.Add(keySnapshotHotReadTTL).UnixNano(),
		userID:         snapshot.UserID,
		organizationID: snapshot.OrganizationID,
		region:         token.region,
		prefix:         token.prefix,
	}
	return true
}

func (c *RedisKeyCache) pruneHotLocked(now time.Time) {
	if len(c.hot.entries) < keySnapshotHotMaxEntries {
		return
	}
	for cachedKey, entry := range c.hot.entries {
		if entry.expiresAt <= now.UnixNano() {
			delete(c.hot.entries, cachedKey)
		}
	}
	if len(c.hot.entries) >= keySnapshotHotMaxEntries {
		clear(c.hot.entries)
	}
}

func (c *RedisKeyCache) bumpKeyGeneration(ctx context.Context, region string, match func(keySnapshotHotEntry) bool) error {
	prefix := c.prefix()
	// 失效属于低频控制面操作，允许在同一实例的热缓存锁内完成一次 Redis INCR。
	// 这样可以先阻断本地回填，再推进共享水位；Redis 短暂失败时本地保持
	// fail closed（失败即拒绝回填），事件重试成功后又能对齐真实共享水位，
	// 不会永久退化成每次都回源主站。
	c.hot.mu.Lock()
	defer c.hot.mu.Unlock()
	generationKey := hotGenerationKey(prefix, region)
	c.hot.generations[generationKey]++
	if match != nil {
		for key, entry := range c.hot.entries {
			if match(entry) {
				delete(c.hot.entries, key)
			}
		}
	}
	generation, err := c.client.Incr(ctx, keyGenerationKey(prefix, region)).Result()
	if err != nil {
		return err
	}
	c.hot.generations[generationKey] = generation
	return nil
}

func (c *RedisKeyCache) deleteHotKey(key string) {
	if c == nil || c.hot == nil || key == "" {
		return
	}
	c.hot.mu.Lock()
	delete(c.hot.entries, key)
	c.hot.mu.Unlock()
}

func (c *RedisKeyCache) deleteHot(match func(keySnapshotHotEntry) bool) {
	if c == nil || c.hot == nil || match == nil {
		return
	}
	c.hot.mu.Lock()
	for key, entry := range c.hot.entries {
		if match(entry) {
			delete(c.hot.entries, key)
		}
	}
	c.hot.mu.Unlock()
}
