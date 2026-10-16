package dto

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type User struct {
	ID            int64      `json:"id"`
	Email         string     `json:"email"`
	Username      string     `json:"username"`
	Role          string     `json:"role"`
	Balance       float64    `json:"balance"`
	Concurrency   int        `json:"concurrency"`
	Status        string     `json:"status"`
	AllowedGroups []int64    `json:"allowed_groups"`
	LastActiveAt  *time.Time `json:"last_active_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`

	// 余额不足通知
	BalanceNotifyEnabled       bool               `json:"balance_notify_enabled"`
	BalanceNotifyThresholdType string             `json:"balance_notify_threshold_type"`
	BalanceNotifyThreshold     *float64           `json:"balance_notify_threshold"`
	BalanceNotifyExtraEmails   []NotifyEmailEntry `json:"balance_notify_extra_emails"`
	TotalRecharged             float64            `json:"total_recharged"`

	// RPMLimit 用户级每分钟请求数上限（0 = 不限制），仅在所用分组未设置 rpm_limit 时作为兜底生效。
	RPMLimit int `json:"rpm_limit"`

	APIKeys       []APIKey           `json:"api_keys,omitempty"`
	Subscriptions []UserSubscription `json:"subscriptions,omitempty"`
}

// AdminUser 是管理员接口使用的 user DTO（包含敏感/内部字段）。
// 注意：普通用户接口不得返回 notes 等管理员备注信息。
type AdminUser struct {
	User

	Notes      string     `json:"notes"`
	LastUsedAt *time.Time `json:"last_used_at"`
	// GroupRates 用户专属分组倍率配置
	// map[groupID]rateMultiplier
	GroupRates map[int64]float64 `json:"group_rates,omitempty"`
}

type APIKey struct {
	ID      int64  `json:"id"`
	UserID  int64  `json:"user_id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	GroupID *int64 `json:"group_id"`
	Status  string `json:"status"`
	// CurrentConcurrency 是当前活跃请求数，来自 Redis（缓存服务）统计槽位；统计失败时为 0。
	CurrentConcurrency int        `json:"current_concurrency"`
	IPWhitelist        []string   `json:"ip_whitelist"`
	IPBlacklist        []string   `json:"ip_blacklist"`
	LastUsedAt         *time.Time `json:"last_used_at"`
	Quota              float64    `json:"quota"`      // Quota limit in USD (0 = unlimited)
	QuotaUsed          float64    `json:"quota_used"` // Used quota amount in USD
	ExpiresAt          *time.Time `json:"expires_at"` // Expiration time (nil = never expires)
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`

	// Rate limit fields
	RateLimit5h   float64    `json:"rate_limit_5h"`
	RateLimit1d   float64    `json:"rate_limit_1d"`
	RateLimit7d   float64    `json:"rate_limit_7d"`
	Usage5h       float64    `json:"usage_5h"`
	Usage1d       float64    `json:"usage_1d"`
	Usage7d       float64    `json:"usage_7d"`
	Window5hStart *time.Time `json:"window_5h_start"`
	Window1dStart *time.Time `json:"window_1d_start"`
	Window7dStart *time.Time `json:"window_7d_start"`
	Reset5hAt     *time.Time `json:"reset_5h_at,omitempty"`
	Reset1dAt     *time.Time `json:"reset_1d_at,omitempty"`
	Reset7dAt     *time.Time `json:"reset_7d_at,omitempty"`

	User  *User  `json:"user,omitempty"`
	Group *Group `json:"group,omitempty"`
}

type Group struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	Platform           string  `json:"platform"`
	DisplayBrand       string  `json:"display_brand"`
	DisplayIcon        string  `json:"display_icon"`
	RateMultiplier     float64 `json:"rate_multiplier"`
	PeakRateEnabled    bool    `json:"peak_rate_enabled"`
	PeakStart          string  `json:"peak_start"`
	PeakEnd            string  `json:"peak_end"`
	PeakRateMultiplier float64 `json:"peak_rate_multiplier"`
	PeakTimezone       string  `json:"peak_timezone"`
	IsExclusive        bool    `json:"is_exclusive"`
	LocalOnly          bool    `json:"local_only"`
	MinimumTier        *int    `json:"minimum_tier"`
	Status             string  `json:"status"`
	SchedulingMode     string  `json:"scheduling_mode"`
	PerUserConcurrency int     `json:"per_user_concurrency"`

	SubscriptionType string   `json:"subscription_type"`
	DailyLimitUSD    *float64 `json:"daily_limit_usd"`
	WeeklyLimitUSD   *float64 `json:"weekly_limit_usd"`
	MonthlyLimitUSD  *float64 `json:"monthly_limit_usd"`

	// 图片生成计费配置（仅 antigravity 平台使用）
	AllowImageGeneration  bool                          `json:"allow_image_generation"`
	ImageRateIndependent  bool                          `json:"image_rate_independent"`
	ImageRateMultiplier   float64                       `json:"image_rate_multiplier"`
	ImagePrice1K          *float64                      `json:"image_price_1k"`
	ImagePrice2K          *float64                      `json:"image_price_2k"`
	ImagePrice4K          *float64                      `json:"image_price_4k"`
	VideoRateIndependent  bool                          `json:"video_rate_independent"`
	VideoRateMultiplier   float64                       `json:"video_rate_multiplier"`
	VideoPrice480P        *float64                      `json:"video_price_480p"`
	VideoPrice720P        *float64                      `json:"video_price_720p"`
	VideoPrice1080P       *float64                      `json:"video_price_1080p"`
	VideoModelPrices      map[string]map[string]float64 `json:"video_model_prices,omitempty"`
	WebSearchPricePerCall *float64                      `json:"web_search_price_per_call"`
	SearchPricePer1k      *float64                      `json:"search_price_per_1k"`

	// [v0.2.5] 利润控制（openai/anthropic/gemini/grok/antigravity 平台）
	ProfitControlEnabled          bool     `json:"profit_control_enabled"`
	ProfitMinMargin               float64  `json:"profit_min_margin"`
	ProfitSafetyBuffer            float64  `json:"profit_safety_buffer"`
	AudioRealtimePricePerMin      *float64 `json:"audio_realtime_price_per_min"`
	AudioTtsPricePerMillionChars  *float64 `json:"audio_tts_price_per_million_chars"`
	AudioSttPricePerHour          *float64 `json:"audio_stt_price_per_hour"`
	AllowOpenAILongContextBilling bool     `json:"allow_openai_long_context_billing"`
	AllowBlockedKeywordRequests   bool     `json:"allow_blocked_keyword_requests"`

	// Claude Code 客户端限制
	ClaudeCodeOnly  bool   `json:"claude_code_only"`
	FallbackGroupID *int64 `json:"fallback_group_id"`
	// 无效请求兜底分组
	FallbackGroupIDOnInvalidRequest *int64 `json:"fallback_group_id_on_invalid_request"`

	// OpenAI Messages 调度开关（用户侧需要此字段判断是否展示 Claude Code 教程）
	AllowMessagesDispatch bool `json:"allow_messages_dispatch"`
	AllowLive             bool `json:"allow_live"`

	// 账号过滤控制（仅 OpenAI/Antigravity 平台有效）
	RequireOAuthOnly  bool `json:"require_oauth_only"`
	RequirePrivacySet bool `json:"require_privacy_set"`

	// RPMLimit 分组级每分钟请求数上限（0 = 不限制），设置后覆盖用户级 rpm_limit。
	RPMLimit int `json:"rpm_limit"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AdminGroup 是管理员接口使用的 group DTO（包含敏感/内部字段）。
// 注意：普通用户接口不得返回 model_routing/account_count/account_groups 等内部信息。
type AdminGroup struct {
	Group
	MaxReasoningEffort      string                          `json:"max_reasoning_effort"`
	ReasoningEffortMappings []domain.ReasoningEffortMapping `json:"reasoning_effort_mappings"`

	// 模型路由配置（仅 anthropic 平台使用）
	ModelRouting        map[string][]int64 `json:"model_routing"`
	ModelRoutingEnabled bool               `json:"model_routing_enabled"`
	ModelMapping        map[string]string  `json:"model_mapping"`
	// 智能分流映射：公开模型 -> [{model,weight}]；空对象表示未启用
	WeightedModelMapping map[string][]service.WeightedModelTarget `json:"weighted_model_mapping"`
	FallbackModel        string                                   `json:"fallback_model"`
	FailoverGroupID      *int64                                   `json:"failover_group_id,omitempty"`
	FailoverStrategy     string                                   `json:"failover_strategy"`
	BillingModelSource   string                                   `json:"billing_model_source"`

	// MCP XML 协议注入（仅 antigravity 平台使用）
	MCPXMLInject bool `json:"mcp_xml_inject"`

	// OpenAI Messages 调度配置（仅 openai 平台使用）
	DefaultMappedModel          string                                   `json:"default_mapped_model"`
	MessagesDispatchModelConfig domain.OpenAIMessagesDispatchModelConfig `json:"messages_dispatch_model_config"`
	ModelsListConfig            domain.GroupModelsListConfig             `json:"models_list_config"`

	// 支持的模型系列（仅 antigravity 平台使用）
	SupportedModelScopes []string       `json:"supported_model_scopes"`
	AccountGroups        []AccountGroup `json:"account_groups,omitempty"`
	// 空绑定只能在本字段为 true 时解释为真实的空集合；列表接口不会加载完整边。
	AccountBindingsLoaded                bool   `json:"account_bindings_loaded"`
	AccountCount                         int64  `json:"account_count,omitempty"`
	ActiveAccountCount                   int64  `json:"active_account_count,omitempty"`
	RateLimitedAccountCount              int64  `json:"rate_limited_account_count,omitempty"`
	OpenAILongContextBillingAccountCount int64  `json:"openai_long_context_billing_account_count"`
	OpenAIStandardBillingAccountCount    int64  `json:"openai_standard_billing_account_count"`
	OpenAILongContextBillingStatus       string `json:"openai_long_context_billing_status"`

	// 分组排序
	SortOrder int `json:"sort_order"`
}

type Account struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Notes    *string `json:"notes"`
	Platform string  `json:"platform"`
	Type     string  `json:"type"`
	// Credentials 经 RedactCredentials 处理后只含非敏感子键；敏感 token / api_key / 私钥
	// 的存在性通过 CredentialsStatus（has_<key>）暴露，原始值不返回前端。
	Credentials             map[string]any                         `json:"credentials"`
	CredentialsStatus       map[string]bool                        `json:"credentials_status,omitempty"`
	Extra                   map[string]any                         `json:"extra"`
	CodexTurnTickets        []service.OpenAICodexTicketStatus      `json:"codex_turn_tickets,omitempty"`  // [v0.2.6] Codex 票据状态
	CodexRouteCookies       []service.OpenAICodexRouteCookieStatus `json:"codex_route_cookies,omitempty"` // [2026-09] Codex 路由 Cookie 池状态（__cfblb/__oailb）
	ProxyID                 *int64                                 `json:"proxy_id"`
	ProxyFallbackOriginID   *int64                                 `json:"proxy_fallback_origin_id"`
	ProxyFallbackOriginName *string                                `json:"proxy_fallback_origin_name,omitempty"`
	Concurrency             int                                    `json:"concurrency"`
	LoadFactor              *int                                   `json:"load_factor,omitempty"`
	Priority                int                                    `json:"priority"`
	RateMultiplier          float64                                `json:"rate_multiplier"`
	Status                  string                                 `json:"status"`
	ErrorMessage            string                                 `json:"error_message"`
	LastUsedAt              *time.Time                             `json:"last_used_at"`
	ExpiresAt               *int64                                 `json:"expires_at"`
	AutoPauseOnExpired      bool                                   `json:"auto_pause_on_expired"`
	CreatedAt               time.Time                              `json:"created_at"`
	UpdatedAt               time.Time                              `json:"updated_at"`

	Schedulable bool `json:"schedulable"`

	RateLimitedAt    *time.Time `json:"rate_limited_at"`
	RateLimitResetAt *time.Time `json:"rate_limit_reset_at"`
	OverloadUntil    *time.Time `json:"overload_until"`

	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason"`

	SessionWindowStart  *time.Time `json:"session_window_start"`
	SessionWindowEnd    *time.Time `json:"session_window_end"`
	SessionWindowStatus string     `json:"session_window_status"`

	// CodexQuotaHealth 是根据已有上游响应头快照计算的只读摘要，不参与调度。
	CodexQuotaHealth *CodexQuotaHealth `json:"codex_quota_health,omitempty"`

	// 仅 Grok 账号返回；把管理员覆写与自动探测证据分开，避免前端从
	// 任意 extra map 自行推断调度资格。
	GrokMediaEligibility *GrokMediaEligibility `json:"grok_media_eligibility,omitempty"`

	// 5h窗口费用控制（仅 Anthropic OAuth/SetupToken 账号有效）
	// 从 extra 字段提取，方便前端显示和编辑
	WindowCostLimit         *float64 `json:"window_cost_limit,omitempty"`
	WindowCostStickyReserve *float64 `json:"window_cost_sticky_reserve,omitempty"`

	// 会话数量控制（仅 Anthropic OAuth/SetupToken 账号有效）
	// 从 extra 字段提取，方便前端显示和编辑
	MaxSessions           *int `json:"max_sessions,omitempty"`
	SessionIdleTimeoutMin *int `json:"session_idle_timeout_minutes,omitempty"`

	// RPM 限制（仅 Anthropic OAuth/SetupToken 账号有效）
	// 从 extra 字段提取，方便前端显示和编辑
	BaseRPM          *int    `json:"base_rpm,omitempty"`
	RPMStrategy      *string `json:"rpm_strategy,omitempty"`
	RPMStickyBuffer  *int    `json:"rpm_sticky_buffer,omitempty"`
	UserMsgQueueMode *string `json:"user_msg_queue_mode,omitempty"`

	// TLS指纹伪装（仅 Anthropic OAuth/SetupToken 账号有效）
	// 从 extra 字段提取，方便前端显示和编辑
	EnableTLSFingerprint    *bool  `json:"enable_tls_fingerprint,omitempty"`
	TLSFingerprintProfileID *int64 `json:"tls_fingerprint_profile_id,omitempty"`

	// 会话ID伪装（仅 Anthropic OAuth/SetupToken 账号有效）
	// 启用后将在15分钟内固定 metadata.user_id 中的 session ID
	// 从 extra 字段提取，方便前端显示和编辑
	EnableSessionIDMasking *bool `json:"session_id_masking_enabled,omitempty"`

	// 缓存 TTL 强制替换（仅 Anthropic OAuth/SetupToken 账号有效）
	// 启用后将所有 cache creation tokens 归入指定的 TTL 类型计费
	CacheTTLOverrideEnabled *bool   `json:"cache_ttl_override_enabled,omitempty"`
	CacheTTLOverrideTarget  *string `json:"cache_ttl_override_target,omitempty"`

	// 自定义 Base URL 中继转发（仅 Anthropic OAuth/SetupToken 账号有效）
	CustomBaseURLEnabled *bool   `json:"custom_base_url_enabled,omitempty"`
	CustomBaseURL        *string `json:"custom_base_url,omitempty"`

	// API Key 账号配额限制
	QuotaLimit       *float64 `json:"quota_limit,omitempty"`
	QuotaUsed        *float64 `json:"quota_used,omitempty"`
	QuotaDailyLimit  *float64 `json:"quota_daily_limit,omitempty"`
	QuotaDailyUsed   *float64 `json:"quota_daily_used,omitempty"`
	QuotaWeeklyLimit *float64 `json:"quota_weekly_limit,omitempty"`
	QuotaWeeklyUsed  *float64 `json:"quota_weekly_used,omitempty"`

	// 配额固定时间重置配置
	QuotaDailyResetMode  *string `json:"quota_daily_reset_mode,omitempty"`
	QuotaDailyResetHour  *int    `json:"quota_daily_reset_hour,omitempty"`
	QuotaWeeklyResetMode *string `json:"quota_weekly_reset_mode,omitempty"`
	QuotaWeeklyResetDay  *int    `json:"quota_weekly_reset_day,omitempty"`
	QuotaWeeklyResetHour *int    `json:"quota_weekly_reset_hour,omitempty"`
	QuotaResetTimezone   *string `json:"quota_reset_timezone,omitempty"`
	QuotaDailyResetAt    *string `json:"quota_daily_reset_at,omitempty"`
	QuotaWeeklyResetAt   *string `json:"quota_weekly_reset_at,omitempty"`

	// 配额通知配置
	QuotaNotifyDailyEnabled    *bool    `json:"quota_notify_daily_enabled,omitempty"`
	QuotaNotifyDailyThreshold  *float64 `json:"quota_notify_daily_threshold,omitempty"`
	QuotaNotifyWeeklyEnabled   *bool    `json:"quota_notify_weekly_enabled,omitempty"`
	QuotaNotifyWeeklyThreshold *float64 `json:"quota_notify_weekly_threshold,omitempty"`
	QuotaNotifyTotalEnabled    *bool    `json:"quota_notify_total_enabled,omitempty"`
	QuotaNotifyTotalThreshold  *float64 `json:"quota_notify_total_threshold,omitempty"`

	// Spark 影子账号只保存模型映射，授权凭据从母账号解析；
	// 前端据此隐藏重授权、Token 刷新和隐私设置等无效操作。
	ParentAccountID *int64 `json:"parent_account_id,omitempty"`
	QuotaDimension  string `json:"quota_dimension,omitempty"`

	Proxy         *Proxy         `json:"proxy,omitempty"`
	AccountGroups []AccountGroup `json:"account_groups,omitempty"`

	GroupIDs []int64  `json:"group_ids,omitempty"`
	Groups   []*Group `json:"groups,omitempty"`
}

type GrokMediaEligibility struct {
	State             string   `json:"state"`
	Eligible          *bool    `json:"eligible"`
	EffectiveEligible bool     `json:"effective_eligible"`
	Source            string   `json:"source"`
	Reason            string   `json:"reason"`
	Override          *bool    `json:"override"`
	ObservedAt        string   `json:"observed_at,omitempty"`
	ProbedAt          string   `json:"probed_at,omitempty"`
	Stale             bool     `json:"stale"`
	WeeklyStatusCode  int      `json:"weekly_status_code,omitempty"`
	MonthlyStatusCode int      `json:"monthly_status_code,omitempty"`
	FailedWindows     []string `json:"failed_windows,omitempty"`
}

type CodexQuotaHealth struct {
	Status           string                  `json:"status"`
	TriggerWindow    string                  `json:"trigger_window,omitempty"`
	Reason           string                  `json:"reason,omitempty"`
	Source           string                  `json:"source,omitempty"`
	ObservedAt       *time.Time              `json:"observed_at,omitempty"`
	NextResetAt      *time.Time              `json:"next_reset_at,omitempty"`
	Stale            bool                    `json:"stale"`
	ThresholdPercent float64                 `json:"threshold_percent"`
	FiveHour         *CodexQuotaWindowHealth `json:"five_hour,omitempty"`
	SevenDay         *CodexQuotaWindowHealth `json:"seven_day,omitempty"`
}

type CodexQuotaWindowHealth struct {
	Status      string     `json:"status"`
	UsedPercent *float64   `json:"used_percent,omitempty"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
}

type AccountGroup struct {
	AccountID    int64             `json:"account_id"`
	GroupID      int64             `json:"group_id"`
	Priority     int               `json:"priority"`
	ModelMapping map[string]string `json:"model_mapping,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`

	Account *Account `json:"account,omitempty"`
	Group   *Group   `json:"group,omitempty"`
}

type Proxy struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Protocol  string    `json:"protocol"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Username  string    `json:"username"`
	Password  string    `json:"-"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	ExpiresAt      *time.Time `json:"expires_at"`
	FallbackMode   string     `json:"fallback_mode"`
	BackupProxyID  *int64     `json:"backup_proxy_id"`
	ExpiryWarnDays int        `json:"expiry_warn_days"`
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64  `json:"account_count"`
	LatencyMs      *int64 `json:"latency_ms,omitempty"`
	LatencyStatus  string `json:"latency_status,omitempty"`
	LatencyMessage string `json:"latency_message,omitempty"`
	IPAddress      string `json:"ip_address,omitempty"`
	Country        string `json:"country,omitempty"`
	CountryCode    string `json:"country_code,omitempty"`
	Region         string `json:"region,omitempty"`
	City           string `json:"city,omitempty"`
	QualityStatus  string `json:"quality_status,omitempty"`
	QualityScore   *int   `json:"quality_score,omitempty"`
	QualityGrade   string `json:"quality_grade,omitempty"`
	QualitySummary string `json:"quality_summary,omitempty"`
	QualityChecked *int64 `json:"quality_checked,omitempty"`
}

// AdminProxy 是管理员接口使用的 proxy DTO（包含密码等敏感字段）。
// 注意：普通接口不得使用此 DTO。
type AdminProxy struct {
	Proxy
	Password string `json:"password,omitempty"`
}

// AdminProxyWithAccountCount 是管理员接口使用的带账号统计的 proxy DTO。
type AdminProxyWithAccountCount struct {
	AdminProxy
	AccountCount   int64  `json:"account_count"`
	LatencyMs      *int64 `json:"latency_ms,omitempty"`
	LatencyStatus  string `json:"latency_status,omitempty"`
	LatencyMessage string `json:"latency_message,omitempty"`
	IPAddress      string `json:"ip_address,omitempty"`
	Country        string `json:"country,omitempty"`
	CountryCode    string `json:"country_code,omitempty"`
	Region         string `json:"region,omitempty"`
	City           string `json:"city,omitempty"`
	QualityStatus  string `json:"quality_status,omitempty"`
	QualityScore   *int   `json:"quality_score,omitempty"`
	QualityGrade   string `json:"quality_grade,omitempty"`
	QualitySummary string `json:"quality_summary,omitempty"`
	QualityChecked *int64 `json:"quality_checked,omitempty"`
}

type ProxyAccountSummary struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name"`
	Platform string  `json:"platform"`
	Type     string  `json:"type"`
	Notes    *string `json:"notes,omitempty"`
}

type RedeemCode struct {
	ID        int64      `json:"id"`
	Code      string     `json:"code"`
	Type      string     `json:"type"`
	Value     float64    `json:"value"`
	Status    string     `json:"status"`
	UsedBy    *int64     `json:"used_by"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	GroupID      *int64 `json:"group_id"`
	ValidityDays int    `json:"validity_days"`

	// Notes is only populated for admin_balance/admin_concurrency types
	// so users can see why they were charged or credited
	Notes *string `json:"notes,omitempty"`

	User  *User  `json:"user,omitempty"`
	Group *Group `json:"group,omitempty"`
}

// AdminRedeemCode 是管理员接口使用的 redeem code DTO（包含 notes 等字段）。
// 注意：普通用户接口不得返回 notes 等内部信息。
type AdminRedeemCode struct {
	RedeemCode

	Notes string `json:"notes"`
}

type NullableTimeField struct {
	Set   bool
	Value *time.Time
}

func (f *NullableTimeField) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(data, []byte("null")) {
		f.Value = nil
		return nil
	}
	var value time.Time
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

type NullableInt64Field struct {
	Set   bool
	Value *int64
}

func (f *NullableInt64Field) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(data, []byte("null")) {
		f.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.Value = &value
	return nil
}

type BatchUpdateRedeemCodeFields struct {
	Status    *string            `json:"status,omitempty"`
	ExpiresAt NullableTimeField  `json:"expires_at,omitempty"`
	Notes     *string            `json:"notes,omitempty"`
	GroupID   NullableInt64Field `json:"group_id,omitempty"`

	Type  *string  `json:"type,omitempty"`
	Value *float64 `json:"value,omitempty"`
}

type BatchUpdateRedeemCodesRequest struct {
	IDs    []int64                     `json:"ids" binding:"required,min=1"`
	Fields BatchUpdateRedeemCodeFields `json:"fields" binding:"required"`
}

// UsageLog 是普通用户接口使用的 usage log DTO（不包含管理员字段）。
type UsageLog struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	APIKeyID  int64  `json:"api_key_id"`
	AccountID int64  `json:"account_id"`
	RequestID string `json:"request_id"`
	Model     string `json:"model"`
	// ServiceTier records the OpenAI service tier used for billing, e.g. "priority" / "flex".
	ServiceTier *string `json:"service_tier,omitempty"`
	// LongContextBillingApplied indicates that this request crossed the model
	// threshold and was charged using the long-context tier.
	LongContextBillingApplied bool `json:"long_context_billing_applied"`
	// ReasoningEffort is the explicit level in the final payload sent upstream.
	// It never falls back to the client request, a model suffix, or a provider default.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	// InboundEndpoint is the client-facing API endpoint path, e.g. /v1/chat/completions.
	InboundEndpoint *string `json:"inbound_endpoint,omitempty"`
	// UpstreamEndpoint is the normalized upstream endpoint path, e.g. /v1/responses.
	UpstreamEndpoint *string `json:"upstream_endpoint,omitempty"`

	GroupID        *int64 `json:"group_id"`
	SubscriptionID *int64 `json:"subscription_id"`

	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`

	CacheCreation5mTokens int `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens"`

	InputCost         float64 `json:"input_cost"`
	OutputCost        float64 `json:"output_cost"`
	CacheCreationCost float64 `json:"cache_creation_cost"`
	CacheReadCost     float64 `json:"cache_read_cost"`
	TotalCost         float64 `json:"total_cost"`
	ActualCost        float64 `json:"actual_cost"`
	RateMultiplier    float64 `json:"rate_multiplier"`

	BillingType  int8   `json:"billing_type"`
	RequestType  string `json:"request_type"`
	Stream       bool   `json:"stream"`
	OpenAIWSMode bool   `json:"openai_ws_mode"`
	DurationMs   *int   `json:"duration_ms"`
	FirstTokenMs *int   `json:"first_token_ms"`
	// FakeFirstTokenMs 伪造首字（SSE 注释行）发出时刻（毫秒）；未开启/未触发为 nil，
	// 前端按 null 判断是否展示。
	FakeFirstTokenMs *int `json:"fake_first_token_ms"`

	// 图片生成字段
	ImageCount            int            `json:"image_count"`
	ImageSize             *string        `json:"image_size"`
	ImageInputSize        *string        `json:"image_input_size"`
	ImageOutputSize       *string        `json:"image_output_size"`
	ImageInputTokens      int            `json:"image_input_tokens"`
	ImageInputCost        float64        `json:"image_input_cost"`
	ImageInputPrice       float64        `json:"image_input_price"`
	ImageInputPriceSource *string        `json:"image_input_price_source"`
	ImageOutputTokens     int            `json:"image_output_tokens"`
	ImageOutputCost       float64        `json:"image_output_cost"`
	ImageSizeSource       *string        `json:"image_size_source"`
	ImageSizeBreakdown    map[string]int `json:"image_size_breakdown"`
	MediaType             *string        `json:"media_type"`
	VideoCount            int            `json:"video_count"`
	VideoResolution       *string        `json:"video_resolution"`
	VideoDurationSeconds  *int           `json:"video_duration_seconds"`

	// User-Agent
	UserAgent *string `json:"user_agent"`
	// SessionID 是客户端主动提供的会话关联标识；为空时不返回。
	SessionID *string `json:"session_id,omitempty"`

	// Cache TTL Override 标记
	CacheTTLOverridden bool `json:"cache_ttl_overridden"`

	// BillingMode 计费模式：token/image/video
	BillingMode *string `json:"billing_mode,omitempty"`

	CreatedAt time.Time `json:"created_at"`

	User         *User             `json:"user,omitempty"`
	APIKey       *APIKey           `json:"api_key,omitempty"`
	Group        *Group            `json:"group,omitempty"`
	Subscription *UserSubscription `json:"subscription,omitempty"`
}

// AdminUsageLog 是管理员接口使用的 usage log DTO（包含管理员字段）。
type AdminUsageLog struct {
	UsageLog
	// RequestedGroup* 是请求进入网关时的初始分组；当内容策略临时切组时，
	// 它与 EffectiveGroup* 不同。仅管理员日志返回，避免普通用户看到内部路由证据。
	RequestedGroupID   *int64 `json:"requested_group_id,omitempty"`
	RequestedGroupName string `json:"requested_group_name,omitempty"`
	EffectiveGroupID   *int64 `json:"effective_group_id,omitempty"`
	EffectiveGroupName string `json:"effective_group_name,omitempty"`
	GroupRerouted      bool   `json:"group_rerouted"`
	// MainSiteUsername/MainSiteEmail 是远程 Key snapshot 保存的主站身份，
	// 只在轻量网关管理员日志返回；普通用户接口不得暴露。
	MainSiteUsername string `json:"main_site_username,omitempty"`
	MainSiteEmail    string `json:"main_site_email,omitempty"`
	MainSiteUserID   int64  `json:"main_site_user_id,omitempty"`
	// RatePolicyAudit 是请求开始时冻结的销售倍率证据，仅管理员可见。
	// 账号采购成本倍率不在这里，避免把采购成本和客户扣费混为一谈。
	RatePolicyAudit *gatewaylite.RatePolicyAudit `json:"rate_policy_audit,omitempty"`
	// SentServiceTier is the final value sent upstream after all policies.
	SentServiceTier *string `json:"sent_service_tier,omitempty"`
	// ResponseServiceTier is only present when the upstream explicitly reports it.
	ResponseServiceTier *string `json:"response_service_tier,omitempty"`
	// RequestedServiceTier is the normalized client value before policy rewriting.
	RequestedServiceTier *string `json:"requested_service_tier,omitempty"`
	// PriorityPolicyMode identifies follow_request / key_prefer_priority / force_priority / force_standard.
	PriorityPolicyMode *string `json:"priority_policy_mode,omitempty"`
	// PriorityDetectionSource identifies whether request or upstream response
	// evidence determined the final billing tier.
	PriorityDetectionSource *string `json:"priority_detection_source,omitempty"`
	PriorityPolicyAction    *string `json:"priority_policy_action,omitempty"`
	PriorityPolicyRuleID    *string `json:"priority_policy_rule_id,omitempty"`
	PriorityPolicyFallback  bool    `json:"priority_policy_fallback"`

	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Omitted when no mapping was applied (requested model was used as-is).
	UpstreamModel *string `json:"upstream_model,omitempty"`
	// UpstreamResponseModel 是上游响应原文声明的模型，只在轻量网关管理员接口返回。
	UpstreamResponseModel *string `json:"upstream_response_model,omitempty"`
	// UpstreamModelMismatch 为 nil 表示响应没有声明模型，不能据此判断一致性。
	UpstreamModelMismatch *bool `json:"upstream_model_mismatch,omitempty"`

	// ChannelID 渠道 ID
	ChannelID *int64 `json:"channel_id,omitempty"`
	// ModelMappingChain 模型映射链，如 "a→b→c"
	ModelMappingChain *string `json:"model_mapping_chain,omitempty"`
	// BillingTier 计费层级标签（per_request/image 模式）
	BillingTier *string `json:"billing_tier,omitempty"`

	// AccountRateMultiplier 账号计费倍率快照（nil 表示按 1.0 处理）
	AccountRateMultiplier *float64 `json:"account_rate_multiplier"`
	// AccountStatsCost 账号采购成本定价结果（nil 表示未解析出专用成本；有上游模型时按零处理）
	AccountStatsCost *float64 `json:"account_stats_cost,omitempty"`

	// IPAddress 用户请求 IP（仅管理员可见）
	IPAddress *string `json:"ip_address,omitempty"`

	// Account 最小账号信息（避免泄露敏感字段）
	Account *AccountSummary `json:"account,omitempty"`
}

type UsageCleanupFilters struct {
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	UserID      *int64    `json:"user_id,omitempty"`
	APIKeyID    *int64    `json:"api_key_id,omitempty"`
	AccountID   *int64    `json:"account_id,omitempty"`
	GroupID     *int64    `json:"group_id,omitempty"`
	Model       *string   `json:"model,omitempty"`
	RequestType *string   `json:"request_type,omitempty"`
	Stream      *bool     `json:"stream,omitempty"`
	BillingType *int8     `json:"billing_type,omitempty"`
}

type UsageCleanupTask struct {
	ID           int64               `json:"id"`
	Status       string              `json:"status"`
	Filters      UsageCleanupFilters `json:"filters"`
	CreatedBy    int64               `json:"created_by"`
	DeletedRows  int64               `json:"deleted_rows"`
	ErrorMessage *string             `json:"error_message,omitempty"`
	CanceledBy   *int64              `json:"canceled_by,omitempty"`
	CanceledAt   *time.Time          `json:"canceled_at,omitempty"`
	StartedAt    *time.Time          `json:"started_at,omitempty"`
	FinishedAt   *time.Time          `json:"finished_at,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

// AccountSummary is a minimal account info for usage log display.
// It intentionally excludes sensitive fields like Credentials, Proxy, etc.
type AccountSummary struct {
	ID                      int64              `json:"id"`
	Name                    string             `json:"name"`
	Platform                string             `json:"platform,omitempty"`
	TempUnschedulableUntil  *time.Time         `json:"temp_unschedulable_until,omitempty"`
	TempUnschedulableReason string             `json:"temp_unschedulable_reason,omitempty"`
	GrokQuotaSnapshot       *xai.QuotaSnapshot `json:"grok_quota_snapshot,omitempty"`
}

type Setting struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserSubscription struct {
	ID      int64 `json:"id"`
	UserID  int64 `json:"user_id"`
	GroupID int64 `json:"group_id"`

	StartsAt  time.Time `json:"starts_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Status    string    `json:"status"`

	DailyWindowStart   *time.Time `json:"daily_window_start"`
	WeeklyWindowStart  *time.Time `json:"weekly_window_start"`
	MonthlyWindowStart *time.Time `json:"monthly_window_start"`

	DailyUsageUSD   float64 `json:"daily_usage_usd"`
	WeeklyUsageUSD  float64 `json:"weekly_usage_usd"`
	MonthlyUsageUSD float64 `json:"monthly_usage_usd"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	User  *User  `json:"user,omitempty"`
	Group *Group `json:"group,omitempty"`
}

// AdminUserSubscription 是管理员接口使用的订阅 DTO（包含分配信息/备注等字段）。
// 注意：普通用户接口不得返回 assigned_by/assigned_at/notes/assigned_by_user 等管理员字段。
type AdminUserSubscription struct {
	UserSubscription

	AssignedBy *int64    `json:"assigned_by"`
	AssignedAt time.Time `json:"assigned_at"`
	Notes      string    `json:"notes"`

	AssignedByUser *User `json:"assigned_by_user,omitempty"`
}

type BulkAssignResult struct {
	SuccessCount  int                     `json:"success_count"`
	CreatedCount  int                     `json:"created_count"`
	ReusedCount   int                     `json:"reused_count"`
	FailedCount   int                     `json:"failed_count"`
	Subscriptions []AdminUserSubscription `json:"subscriptions"`
	Errors        []string                `json:"errors"`
	Statuses      map[string]string       `json:"statuses,omitempty"`
}

// PromoCode 注册优惠码
type PromoCode struct {
	ID          int64      `json:"id"`
	Code        string     `json:"code"`
	BonusAmount float64    `json:"bonus_amount"`
	MaxUses     int        `json:"max_uses"`
	UsedCount   int        `json:"used_count"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Notes       string     `json:"notes"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// PromoCodeUsage 优惠码使用记录
type PromoCodeUsage struct {
	ID          int64     `json:"id"`
	PromoCodeID int64     `json:"promo_code_id"`
	UserID      int64     `json:"user_id"`
	BonusAmount float64   `json:"bonus_amount"`
	UsedAt      time.Time `json:"used_at"`

	User *User `json:"user,omitempty"`
}
