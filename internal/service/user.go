package service

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID int64
	// 企业字段只来自主站 KeySnapshot（密钥快照），不写入轻量网关本地用户表。
	// UserID 始终保留真实员工身份，OrganizationID 只定义共享付款和聚合限制作用域。
	OrganizationID          int64
	OrganizationConcurrency int
	OrganizationRPMLimit    int
	OrganizationTPMLimit    int
	MemberConcurrency       int
	MemberRPMLimit          int
	MemberTPMLimit          int
	// Tier 仅用于主站快照请求的分组资格校验；轻量网关本地用户默认 T0。
	Tier           int
	Email          string
	Username       string
	Notes          string
	AvatarURL      string
	AvatarSource   string
	AvatarMIME     string
	AvatarByteSize int
	AvatarSHA256   string
	PasswordHash   string
	Role           string
	Balance        float64
	Concurrency    int
	Status         string
	AllowedGroups  []int64
	TokenVersion   int64 // Incremented on password change to invalidate existing tokens
	// TokenVersionResolved indicates TokenVersion already contains the fingerprint-derived
	// value expected in JWT claims and refresh-token state.
	TokenVersionResolved bool
	SignupSource         string
	LastLoginAt          *time.Time
	LastActiveAt         *time.Time
	LastUsedAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	DeletedAt            *time.Time // 非 nil 表示用户已软删除

	// GroupRates 用户专属分组倍率配置
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64

	// TOTP 双因素认证字段
	TotpSecretEncrypted *string    // AES-256-GCM 加密的 TOTP 密钥
	TotpEnabled         bool       // 是否启用 TOTP
	TotpEnabledAt       *time.Time // TOTP 启用时间

	// 余额不足通知
	BalanceNotifyEnabled       bool
	BalanceNotifyThresholdType string // "fixed" (default) | "percentage"
	BalanceNotifyThreshold     *float64
	BalanceNotifyExtraEmails   []NotifyEmailEntry
	TotalRecharged             float64

	// RPMLimit 用户级每分钟请求数上限（0 = 不限制）。仅在所用分组未设置 rpm_limit
	// 且该 (用户, 分组) 无 rpm_override 时作为全局兜底生效。
	RPMLimit int

	// TPMLimit 用户级每分钟 token 预估上限（0 = 不限制）。该值来自主站套餐/API Key 快照。
	// 网关按请求体做保守预估，用于阻止同一用户用多个 API Key 打穿分钟 token 上限。
	TPMLimit int

	// UserGroupRPMOverride 来自 auth cache snapshot 的 (user, group) RPM 覆盖值。
	// nil 只有结合 UserGroupRPMOverrideResolved 才能区分“确认无 override”和“尚未读取”。
	// 字段不持久化到数据库。
	UserGroupRPMOverride *int
	// UserGroupRPMOverrideResolved 表示上面的值已被权威数据源解析。
	// true+nil = 确认无 override，禁止请求热路径重复查 DB；false = 允许安全回退 DB。
	UserGroupRPMOverrideResolved bool

	APIKeys       []APIKey
	Subscriptions []UserSubscription
	// [v0.2.5 新增字段]
	FrozenBalance float64
	// RestrictPublicGroups narrows the public groups this user may bind to the
	// ones listed in AllowedGroups. False keeps the default, where every public
	// group is bindable.
	RestrictPublicGroups bool
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsActive() bool {
	return u.Status == StatusActive
}

// CanBindGroup checks whether a user can bind to a given group.
// For standard groups:
//   - Public groups (non-exclusive): all users can bind, unless the user has
//     RestrictPublicGroups enabled, in which case the group must be in AllowedGroups
//   - Exclusive groups: only users with the group in AllowedGroups can bind
//
// 订阅型分组的授权不在这里判定（只看有效订阅），各调用点会在 IsSubscriptionType 时先短路。
func (u *User) CanBindGroup(groupID int64, isExclusive bool) bool {
	// 公开分组（非专属）：默认所有用户都可以绑定；仅当该用户开启了公开分组限制时，
	// 才需要落在 AllowedGroups 中（RestrictPublicGroups 的语义见 User 结构体注释）。
	if !isExclusive && !u.RestrictPublicGroups {
		return true
	}
	// 专属分组，以及受限用户的公开分组：需要在 AllowedGroups 中
	for _, id := range u.AllowedGroups {
		if id == groupID {
			return true
		}
	}
	return false
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}
