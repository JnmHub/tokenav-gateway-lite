package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
)

// API Key status constants
const (
	StatusAPIKeyActive         = "active"
	StatusAPIKeyDisabled       = "disabled"
	StatusAPIKeyQuotaExhausted = "quota_exhausted"
	StatusAPIKeyExpired        = "expired"
)

const GatewayLiteLocalAPIKeyPrefix = "glk_"

type gatewayLiteLocalAPIKeyContextKey struct{}

// IsGatewayLiteLocalAPIKey identifies keys that are owned and authenticated by
// the current gateway-lite node. The explicit prefix keeps local auth isolated
// from control-plane key formats.
func IsGatewayLiteLocalAPIKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), GatewayLiteLocalAPIKeyPrefix)
}

// ContextWithGatewayLiteLocalAPIKey marks a request as locally authenticated.
// Usage recording runs in a worker pool, so this marker must survive context
// detachment to prevent the request from entering user balance billing or
// control-plane usage correction paths later in the request lifecycle.
func ContextWithGatewayLiteLocalAPIKey(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, gatewayLiteLocalAPIKeyContextKey{}, true)
}

func IsGatewayLiteLocalAPIKeyContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	local, _ := ctx.Value(gatewayLiteLocalAPIKeyContextKey{}).(bool)
	return local
}

// Rate limit window durations
const (
	RateLimitWindow5h = 5 * time.Hour
	RateLimitWindow1d = 24 * time.Hour
	RateLimitWindow7d = 7 * 24 * time.Hour
)

// IsWindowExpired returns true if the window starting at windowStart has exceeded the given duration.
// A nil windowStart is treated as expired — no initialized window means any accumulated usage is stale.
func IsWindowExpired(windowStart *time.Time, duration time.Duration) bool {
	return windowStart == nil || time.Since(*windowStart) >= duration
}

type APIKey struct {
	ID          int64
	UserID      int64
	Key         string
	Name        string
	GroupID     *int64
	Status      string
	IPWhitelist []string
	IPBlacklist []string
	// 预编译的 IP 规则，用于认证热路径避免重复 ParseIP/ParseCIDR。
	CompiledIPWhitelist *ip.CompiledIPRules `json:"-"`
	CompiledIPBlacklist *ip.CompiledIPRules `json:"-"`
	LastUsedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	User                *User
	Group               *Group
	// UserRateMultiplier 是鉴权快照在缓存重建时解析的用户专属销售倍率。
	// nil 表示使用分组默认倍率；请求热路径禁止为它逐次查询数据库。
	UserRateMultiplier *float64
	// UserGroupRateMultipliers 只用于主站远程 Key 的同节点关键词重路由，
	// 以本地分组编号精确取值；缺失目标必须恢复目标分组默认倍率。
	UserGroupRateMultipliers map[int64]float64
	// AllowedModels 来自主站套餐快照，用于 gateway-lite 热路径模型拦截。
	AllowedModels []string
	// AllowedKeywordRouteGroupIDs 来自主站套餐分组规则，只约束命中强关键词后的
	// 临时切组；普通请求仍严格使用 Key 原始绑定分组。
	AllowedKeywordRouteGroupIDs     []int64
	KeywordRouteGroupAccessEnforced bool
	// GatewayStrategy 来自主站 Key 快照。新版只接受 fixed_group，避免请求退化到未约束的全账号池。
	GatewayStrategy         string
	KeyDefaultGatewayNodeID int64
	// RequestGroupOverrideID 只存在于当前请求复制出的 APIKey。关键词命中切组后，
	// 调度必须锁定实际分组，即使这是本地 Key、没有 control-plane 的 fixed_group
	// 元数据；该字段绝不写回缓存或数据库。
	RequestGroupOverrideID *int64
	// CurrentConcurrency 是 Redis（缓存服务）里的实时活跃请求数，只用于展示，不参与限流。
	CurrentConcurrency int
	// Key*Limit 只承载主站企业 Key 自身策略。成员、企业和分组限制分别保存在
	// User / Group 上，运行时通过同一个 Redis Lua 原子检查，不能提前取最小值。
	KeyConcurrencyLimit int
	KeyRPMLimit         int
	KeyTPMLimit         int

	// Quota fields
	Quota     float64    // Quota limit in USD (0 = unlimited)
	QuotaUsed float64    // Used quota amount
	ExpiresAt *time.Time // Expiration time (nil = never expires)

	// Rate limit fields
	RateLimit5h   float64    // Rate limit in USD per 5h (0 = unlimited)
	RateLimit1d   float64    // Rate limit in USD per 1d (0 = unlimited)
	RateLimit7d   float64    // Rate limit in USD per 7d (0 = unlimited)
	Usage5h       float64    // Used amount in current 5h window
	Usage1d       float64    // Used amount in current 1d window
	Usage7d       float64    // Used amount in current 7d window
	Window5hStart *time.Time // Start of current 5h window
	Window1dStart *time.Time // Start of current 1d window
	Window7dStart *time.Time // Start of current 7d window
}

func (k *APIKey) IsActive() bool {
	return k.Status == StatusActive
}

// SchedulerGroupID returns the only group that this key may schedule.
func (k *APIKey) SchedulerGroupID() *int64 {
	if k != nil && k.RequestGroupOverrideID != nil && *k.RequestGroupOverrideID > 0 {
		return k.RequestGroupOverrideID
	}
	if k == nil || k.GroupID == nil || *k.GroupID <= 0 {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(k.GatewayStrategy)) != "fixed_group" || k.KeyDefaultGatewayNodeID <= 0 {
		return nil
	}
	return k.GroupID
}

// WSIngressConcurrencyID returns the Redis lease namespace for one client API
// key. Local keys use their database ID. Keys authenticated from a control-plane
// snapshot intentionally have ID=0, so they use a stable negative hash of the
// non-secret KeyID instead. Keeping the namespaces signed and disjoint prevents
// a remote KeyID hash from colliding with a local database row; the raw API key
// is never hashed or written to Redis.
func (k *APIKey) WSIngressConcurrencyID(ctx context.Context) int64 {
	if k == nil {
		return 0
	}
	if k.ID > 0 {
		return k.ID
	}
	keyID := ""
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		keyID = strings.TrimSpace(meta.KeyID)
	}
	if keyID == "" {
		// gatewayLiteSnapshotToAPIKey stores the control-plane KeyID in Name;
		// this fallback keeps direct handler tests and detached contexts stable.
		keyID = strings.TrimSpace(k.Name)
	}
	if keyID == "" {
		return 0
	}
	sum := sha256.Sum256([]byte("gateway-lite-ws-ingress\x00" + keyID))
	value := int64(binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff)
	if value == 0 {
		value = 1
	}
	return -value
}

// HasRateLimits returns true if any rate limit window is configured
func (k *APIKey) HasRateLimits() bool {
	return k.RateLimit5h > 0 || k.RateLimit1d > 0 || k.RateLimit7d > 0
}

// IsExpired checks if the API key has expired
func (k *APIKey) IsExpired() bool {
	if k.ExpiresAt == nil {
		return false
	}
	return time.Now().After(*k.ExpiresAt)
}

// IsQuotaExhausted checks if the API key quota is exhausted
func (k *APIKey) IsQuotaExhausted() bool {
	if k.Quota <= 0 {
		return false // unlimited
	}
	return k.QuotaUsed >= k.Quota
}

// GetQuotaRemaining returns remaining quota (-1 for unlimited)
func (k *APIKey) GetQuotaRemaining() float64 {
	if k.Quota <= 0 {
		return -1 // unlimited
	}
	remaining := k.Quota - k.QuotaUsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetDaysUntilExpiry returns days until expiry (-1 for never expires)
func (k *APIKey) GetDaysUntilExpiry() int {
	if k.ExpiresAt == nil {
		return -1 // never expires
	}
	duration := time.Until(*k.ExpiresAt)
	if duration < 0 {
		return 0
	}
	return int(duration.Hours() / 24)
}

// EffectiveUsage5h returns the 5h window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage5h() float64 {
	if IsWindowExpired(k.Window5hStart, RateLimitWindow5h) {
		return 0
	}
	return k.Usage5h
}

// EffectiveUsage1d returns the 1d window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage1d() float64 {
	if IsWindowExpired(k.Window1dStart, RateLimitWindow1d) {
		return 0
	}
	return k.Usage1d
}

// EffectiveUsage7d returns the 7d window usage, or 0 if the window has expired.
func (k *APIKey) EffectiveUsage7d() float64 {
	if IsWindowExpired(k.Window7dStart, RateLimitWindow7d) {
		return 0
	}
	return k.Usage7d
}

// APIKeyListFilters holds optional filtering parameters for listing API keys.
type APIKeyListFilters struct {
	Search  string
	Status  string
	GroupID *int64 // nil=不筛选, 0=无分组, >0=指定分组
}
