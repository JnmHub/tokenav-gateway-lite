package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

type OpenAIMessagesDispatchModelConfig = domain.OpenAIMessagesDispatchModelConfig
type GroupModelsListConfig = domain.GroupModelsListConfig
type ReasoningEffortMapping = domain.ReasoningEffortMapping

const (
	GroupSchedulingModeDefault        = "default"
	GroupSchedulingModeRandom         = "random"
	GroupSchedulingModeStrictPriority = "strict_priority"

	FailoverStrategyOriginalModelFirst = "original_model_first"
	FailoverStrategyCurrentGroupFirst  = "current_group_first"
)

func NormalizeGroupFailoverStrategy(strategy string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "", FailoverStrategyOriginalModelFirst:
		return FailoverStrategyOriginalModelFirst, true
	case FailoverStrategyCurrentGroupFirst:
		return FailoverStrategyCurrentGroupFirst, true
	default:
		return "", false
	}
}

type Group struct {
	ID             int64
	Name           string
	Description    string
	Platform       string
	DisplayBrand   string
	DisplayIcon    string
	RateMultiplier float64
	// 高峰时段只叠加到 Token 销售倍率；图片/视频按次独立倍率不受影响。
	// PeakTimezone 使用 IANA 名称，避免多区域节点按各自本地时区产生不同扣费。
	PeakRateEnabled    bool
	PeakStart          string
	PeakEnd            string
	PeakRateMultiplier float64
	PeakTimezone       string
	IsExclusive        bool
	// LocalOnly 分组只允许轻量网关本地 glk_ 密钥使用，不得进入主站镜像。
	// 该字段必须保留到调度缓存，避免远程 Key 利用旧快照或 fallback 绕过。
	LocalOnly bool
	Status    string
	// SchedulingMode 只改变通过所有门禁后的账号选择顺序，不得绕过模型、额度或并发保护。
	SchedulingMode string
	// PerUserConcurrency 是分组维度的主站用户并发硬上限，0 表示不限。
	// 它与用户等级并发分别检查，不能被多建 API Key 绕过。
	PerUserConcurrency int
	Hydrated           bool // indicates the group was loaded from a trusted repository source
	// MinimumTier 为 nil 时继承主站节点等级；非 nil 时只允许 T0/T1/T2。
	// EffectiveRequiredTier 仅承载主站计算后的最终门槛，不作为本地持久化配置。
	MinimumTier           *int
	EffectiveRequiredTier int
	// DuplicateOperationID 仅用于恢复已经提交但响应结果不确定的一键复制操作。
	// 该内部字段禁止映射到 API DTO（接口传输对象）。
	DuplicateOperationID string

	SubscriptionType    string
	DailyLimitUSD       *float64
	WeeklyLimitUSD      *float64
	MonthlyLimitUSD     *float64
	DefaultValidityDays int

	// 图片生成计费配置（antigravity 和 gemini 平台使用）
	AllowImageGeneration bool
	ImageRateIndependent bool
	ImageRateMultiplier  float64
	ImagePrice1K         *float64
	ImagePrice2K         *float64
	ImagePrice4K         *float64
	VideoRateIndependent bool
	VideoRateMultiplier  float64
	VideoPrice480P       *float64
	VideoPrice720P       *float64
	VideoPrice1080P      *float64
	// VideoModelPrices allows model-family × resolution overrides.
	VideoModelPrices map[string]map[string]float64
	// Codex alpha/search 网页搜索单次价格（USD/次，仅 OpenAI 平台使用）。
	// nil 使用官方默认价，0 表示免费。
	WebSearchPricePerCall *float64
	// SearchPricePer1k 是 Grok 原生搜索工具每千次调用的价格；nil 使用默认价，0 表示免费。
	SearchPricePer1k             *float64
	AudioRealtimePricePerMin     *float64
	AudioTTSPricePerMillionChars *float64
	AudioSTTPricePerHour         *float64
	// 账号开关描述上游成本事实；分组开关描述是否向客户开放该计费档位。
	AllowOpenAILongContextBilling        bool
	OpenAILongContextBillingAccountCount int64
	OpenAIStandardBillingAccountCount    int64
	OpenAILongContextBillingStatus       string
	// AllowBlockedKeywordRequests 只决定强关键词命中后本分组能否承接请求；
	// 语种白名单、用户等级、计费资格和账号可用性仍必须独立校验。
	AllowBlockedKeywordRequests bool

	// Claude Code 客户端限制
	ClaudeCodeOnly  bool
	FallbackGroupID *int64
	// 无效请求兜底分组（仅 anthropic 平台使用）
	FallbackGroupIDOnInvalidRequest *int64

	// 模型路由配置
	// key: 模型匹配模式（支持 * 通配符，如 "claude-opus-*"）
	// value: 优先账号 ID 列表
	ModelRouting        map[string][]int64
	ModelRoutingEnabled bool
	// ModelMapping 在账号选择前把客户模型解析为分组目标模型；账号映射再作用于该目标。
	// 精确规则优先，末尾 * 规则按最长前缀优先，保证结果与 map 遍历顺序无关。
	ModelMapping map[string]string
	// WeightedModelMapping 是智能分流映射：公开模型 -> 带权重的目标模型列表。
	// 与 ModelMapping 的键互斥；选择时按权重平滑轮询并参考目标健康状态，
	// 失败目标自动冷却禁用。空 map 表示未启用，行为与单目标映射完全一致。
	WeightedModelMapping map[string][]WeightedModelTarget
	// FallbackModel 仅供调度层在原模型没有任何可用账号时尝试一次。
	// 空字符串表示关闭；它不改变客户请求模型、响应声明或默认计费口径。
	FallbackModel string
	// FailoverGroupID 是普通文本上游可恢复故障时一次性转移到的分组。
	// 它与 fallback_group_id（客户端限制/无效请求兜底）完全独立。
	FailoverGroupID *int64
	// FailoverStrategy 控制跨分组前的同组模型兜底顺序；空值按 original_model_first 处理。
	FailoverStrategy string
	// BillingModelSource 决定价格表使用客户请求、映射后或实际上游模型。
	// 该分组级选择优先于渠道配置，避免管理员跨页面维护同一条计费规则。
	BillingModelSource string

	// MCP XML 协议注入开关（仅 antigravity 平台使用）
	MCPXMLInject bool

	// 支持的模型系列（仅 antigravity 平台使用）
	// 可选值: claude, gemini_text, gemini_image
	SupportedModelScopes []string

	// 分组排序
	SortOrder int

	// OpenAI Messages 调度配置（仅 openai 平台使用）
	AllowMessagesDispatch       bool
	AllowLive                   bool
	RequireOAuthOnly            bool // 仅允许非 apikey 类型账号关联（OpenAI/Antigravity/Anthropic/Gemini）
	RequirePrivacySet           bool // 调度时仅允许 privacy 已成功设置的账号（OpenAI/Antigravity/Anthropic/Gemini）
	DefaultMappedModel          string
	MessagesDispatchModelConfig OpenAIMessagesDispatchModelConfig
	ModelsListConfig            GroupModelsListConfig

	// RPMLimit 分组级每分钟请求数上限（0 = 不限制）。
	// 一旦设置即接管该分组用户的限流（覆盖用户级 rpm_limit），可被 user-group rpm_override 进一步覆盖。
	RPMLimit int

	// OpenAI/Codex 显式推理档位策略。空上限表示不限制；映射先于上限执行。
	MaxReasoningEffort      string
	ReasoningEffortMappings []ReasoningEffortMapping

	CreatedAt time.Time
	UpdatedAt time.Time

	AccountGroups []AccountGroup
	// AccountBindingsLoaded 只用于管理员详情读取。空数组只有在该标记为 true 时，
	// 才表示“该分组确实没有绑定账号”；避免跨服务调用把未加载误当成全部解绑。
	AccountBindingsLoaded   bool
	AccountCount            int64
	ActiveAccountCount      int64
	RateLimitedAccountCount int64
	// [v0.2.5 新增字段]
	AllowBatchImageGeneration    bool
	BatchImageDiscountMultiplier float64
	BatchImageHoldMultiplier     float64
	LongContextPricingEnabled    bool
	ModelPricing                 []ChannelModelPricing
	ForceOpenAIFast              bool // 强制 OpenAI 网关请求使用 service_tier=priority
	FreeOpenAIFast               bool // OpenAI Fast 请求按 Standard 价格向用户计费
	ModelAllowlist               GroupModelAllowlist
	CodexModelsManifestConfig    GroupCodexModelsManifestConfig
	MaxReasoningEffortOverLimit  string
	ProfitControlEnabled         bool
	ProfitMinMargin              float64 // 最低毛利率，小数存储（0.30=30%）
	ProfitSafetyBuffer           float64 // 安全缓冲，小数，与 margin 相加后从 D 中扣除
}

func NormalizeGroupSchedulingMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", GroupSchedulingModeDefault:
		return GroupSchedulingModeDefault, true
	case GroupSchedulingModeRandom:
		return GroupSchedulingModeRandom, true
	case GroupSchedulingModeStrictPriority:
		return GroupSchedulingModeStrictPriority, true
	default:
		return "", false
	}
}

func GroupUsesRandomScheduling(group *Group) bool {
	return group != nil && group.SchedulingMode == GroupSchedulingModeRandom
}

func GroupUsesStrictPriorityScheduling(group *Group) bool {
	return group != nil && group.SchedulingMode == GroupSchedulingModeStrictPriority
}

// GroupSchedulingModeSupportsPlatform prevents an administrator from saving a
// scheduling mode that no request path can honor. Strict priority currently
// runs through the OpenAI-compatible scheduler, which also serves Grok.
func GroupSchedulingModeSupportsPlatform(mode, platform string) bool {
	normalizedMode, ok := NormalizeGroupSchedulingMode(mode)
	if !ok || normalizedMode != GroupSchedulingModeStrictPriority {
		return ok
	}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformOpenAI, PlatformGrok:
		return true
	default:
		return false
	}
}

// GroupRequiredTier 返回请求热路径应执行的最终等级门槛。主站快照优先，
// 本地管理场景则退回分组自身配置；两者均未设置时按 T0 处理。
func GroupRequiredTier(group *Group) int {
	if group == nil {
		return 0
	}
	if group.EffectiveRequiredTier > 0 {
		return group.EffectiveRequiredTier
	}
	if group.MinimumTier != nil && *group.MinimumTier > 0 {
		return *group.MinimumTier
	}
	return 0
}

func GroupTierAllowed(userTier int, group *Group) bool {
	return userTier >= GroupRequiredTier(group)
}

// GroupTierAllowedForUser keeps personal tier gates separate from enterprise
// authorization. Organization API keys are already narrowed by the control
// plane's organization/member gateway, group and model allowlists, so applying
// the employee's personal T0/T1/T2 tier here would incorrectly reject paid
// enterprise traffic.
func GroupTierAllowedForUser(user *User, group *Group) bool {
	return user != nil && (user.OrganizationID > 0 || GroupTierAllowed(user.Tier, group))
}

// GroupAccessibleFromRequest 区分轻量网关本地密钥与主站远程密钥。
// local_only 是请求边界而非展示属性，因此 fallback 和关键词重路由也必须复用它。
func GroupAccessibleFromRequest(ctx context.Context, group *Group) bool {
	return group != nil && (!group.LocalOnly || IsGatewayLiteLocalAPIKeyContext(ctx))
}

func (g *Group) IsActive() bool {
	return g.Status == StatusActive
}

func (g *Group) IsSubscriptionType() bool {
	return g.SubscriptionType == SubscriptionTypeSubscription
}

func (g *Group) HasDailyLimit() bool {
	return g.DailyLimitUSD != nil && *g.DailyLimitUSD > 0
}

func (g *Group) HasWeeklyLimit() bool {
	return g.WeeklyLimitUSD != nil && *g.WeeklyLimitUSD > 0
}

func (g *Group) HasMonthlyLimit() bool {
	return g.MonthlyLimitUSD != nil && *g.MonthlyLimitUSD > 0
}

// GetImagePrice 根据 image_size 返回对应的图片生成价格
// 如果分组未配置价格，返回 nil（调用方应使用默认值）
func (g *Group) GetImagePrice(imageSize string) *float64 {
	switch imageSize {
	case "1K":
		return g.ImagePrice1K
	case "2K":
		return g.ImagePrice2K
	case "4K":
		return g.ImagePrice4K
	default:
		// 未知尺寸默认按 2K 计费
		return g.ImagePrice2K
	}
}

// GetVideoPrice 返回对应分辨率的视频每秒单价；nil 表示使用 xAI 默认价。
func (g *Group) GetVideoPrice(resolution string) *float64 {
	switch NormalizeVideoBillingResolutionOrDefault(resolution) {
	case VideoBillingResolution480P:
		return g.VideoPrice480P
	case VideoBillingResolution720P:
		return g.VideoPrice720P
	case VideoBillingResolution1080P:
		return g.VideoPrice1080P
	default:
		return g.VideoPrice480P
	}
}

func (g *Group) GetVideoPriceForModel(model, resolution string) *float64 {
	if g == nil {
		return nil
	}
	if price := LookupVideoModelPrice(g.VideoModelPrices, model, resolution); price != nil {
		return price
	}
	return g.GetVideoPrice(resolution)
}

// GetSearchPricePer1k returns the optional Grok search surcharge per 1,000 calls.
func (g *Group) GetSearchPricePer1k() *float64 {
	if g == nil {
		return nil
	}
	return g.SearchPricePer1k
}

// IsGroupContextValid reports whether a group from context has the fields required for routing decisions.
func IsGroupContextValid(group *Group) bool {
	if group == nil {
		return false
	}
	if group.ID <= 0 {
		return false
	}
	if !group.Hydrated {
		return false
	}
	if group.Platform == "" || group.Status == "" {
		return false
	}
	return true
}

// GetRoutingAccountIDs 根据请求模型获取路由账号 ID 列表
// 返回匹配的优先账号 ID 列表，如果没有匹配规则则返回 nil
func (g *Group) GetRoutingAccountIDs(requestedModel string) []int64 {
	if !g.ModelRoutingEnabled || len(g.ModelRouting) == 0 || requestedModel == "" {
		return nil
	}

	// 1. 精确匹配优先
	if accountIDs, ok := g.ModelRouting[requestedModel]; ok && len(accountIDs) > 0 {
		return accountIDs
	}

	// 2. 通配符匹配（前缀匹配）
	for pattern, accountIDs := range g.ModelRouting {
		if matchModelPattern(pattern, requestedModel) && len(accountIDs) > 0 {
			return accountIDs
		}
	}

	return nil
}

// matchModelPattern 检查模型是否匹配模式
// 支持 * 通配符，如 "claude-opus-*" 匹配 "claude-opus-4-20250514"
func matchModelPattern(pattern, model string) bool {
	if pattern == model {
		return true
	}

	// 处理 * 通配符（仅支持末尾通配符）
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(model, prefix)
	}

	return false
}

// [v0.2.5 新增] 类型别名
type GroupCodexModelsManifestConfig = domain.GroupCodexModelsManifestConfig
