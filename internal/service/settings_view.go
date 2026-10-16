package service

import "strings"

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type SystemSettings struct {
	// Version 是管理员设置的修订令牌，和公开的应用发行版本分开。
	Version string
	// 管理端从同一查询解析策略，避免热路径缓存与编辑修订不一致。
	OpenAIFastPolicySettings         *OpenAIFastPolicySettings
	RegistrationEnabled              bool
	EmailVerifyEnabled               bool
	RegistrationEmailSuffixWhitelist []string
	PromoCodeEnabled                 bool
	PasswordResetEnabled             bool
	FrontendURL                      string
	InvitationCodeEnabled            bool
	TotpEnabled                      bool // TOTP 双因素认证
	LoginAgreementEnabled            bool
	LoginAgreementMode               string
	LoginAgreementUpdatedAt          string
	LoginAgreementDocuments          []LoginAgreementDocument
	GatewayCustomErrorMessage        string

	SMTPHost               string
	SMTPPort               int
	SMTPUsername           string
	SMTPPassword           string
	SMTPPasswordConfigured bool
	SMTPFrom               string
	SMTPFromName           string
	SMTPUseTLS             bool

	TurnstileEnabled             bool
	TurnstileSiteKey             string
	TurnstileSecretKey           string
	TurnstileSecretKeyConfigured bool
	APIKeyACLTrustForwardedIP    bool
	ForwardedClientIPHeaders     []string

	// [v0.2.5] 站点计费模式派生开关：与 BALANCE_PAYMENT_DISABLED 一起构成"站点计费模式"选择器。
	// subscription_enabled 缺省 true（opt-out），见 domain_constants.go 注释。
	SubscriptionEnabled bool

	// [v0.2.5] 腾讯云验证码配置；两个 secret 保留原值供更新链路使用，
	// Configured 供管理端 GET 脱敏回显（不回传明文）。
	TencentCaptchaEnabled               bool
	TencentCaptchaAppID                 string
	TencentCaptchaAppSecretKey          string
	TencentCaptchaAppSecretConfigured   bool
	TencentCaptchaCloudSecretID         string
	TencentCaptchaCloudSecretKey        string
	TencentCaptchaCloudSecretConfigured bool
	TencentCaptchaRegion                string

	// [v0.2.5] 阿里云验证码配置；AccessKeySecret 脱敏规则同上。
	AliyunCaptchaEnabled                   bool
	AliyunCaptchaAccessKeyID               string
	AliyunCaptchaAccessKeySecret           string
	AliyunCaptchaAccessKeySecretConfigured bool
	AliyunCaptchaSceneID                   string
	AliyunCaptchaPrefix                    string
	AliyunCaptchaRegion                    string

	// LinuxDo Connect OAuth 登录
	LinuxDoConnectEnabled                bool
	LinuxDoConnectClientID               string
	LinuxDoConnectClientSecret           string
	LinuxDoConnectClientSecretConfigured bool
	LinuxDoConnectRedirectURL            string

	// DingTalk Connect OAuth 登录
	DingTalkConnectEnabled                 bool
	DingTalkConnectClientID                string
	DingTalkConnectClientSecret            string
	DingTalkConnectClientSecretConfigured  bool
	DingTalkConnectRedirectURL             string
	DingTalkConnectCorpRestrictionPolicy   string
	DingTalkConnectInternalCorpID          string
	DingTalkConnectBypassRegistration      bool
	DingTalkConnectSyncCorpEmail           bool
	DingTalkConnectSyncDisplayName         bool
	DingTalkConnectSyncDept                bool
	DingTalkConnectSyncCorpEmailAttrKey    string
	DingTalkConnectSyncDisplayNameAttrKey  string
	DingTalkConnectSyncDeptAttrKey         string
	DingTalkConnectSyncCorpEmailAttrName   string
	DingTalkConnectSyncDisplayNameAttrName string
	DingTalkConnectSyncDeptAttrName        string

	// WeChat Connect OAuth 登录
	WeChatConnectEnabled                   bool
	WeChatConnectAppID                     string
	WeChatConnectAppSecret                 string
	WeChatConnectAppSecretConfigured       bool
	WeChatConnectOpenAppID                 string
	WeChatConnectOpenAppSecret             string
	WeChatConnectOpenAppSecretConfigured   bool
	WeChatConnectMPAppID                   string
	WeChatConnectMPAppSecret               string
	WeChatConnectMPAppSecretConfigured     bool
	WeChatConnectMobileAppID               string
	WeChatConnectMobileAppSecret           string
	WeChatConnectMobileAppSecretConfigured bool
	WeChatConnectOpenEnabled               bool
	WeChatConnectMPEnabled                 bool
	WeChatConnectMobileEnabled             bool
	WeChatConnectMode                      string
	WeChatConnectScopes                    string
	WeChatConnectRedirectURL               string
	WeChatConnectFrontendRedirectURL       string

	// Generic OIDC OAuth 登录
	OIDCConnectEnabled                bool
	OIDCConnectProviderName           string
	OIDCConnectClientID               string
	OIDCConnectClientSecret           string
	OIDCConnectClientSecretConfigured bool
	OIDCConnectIssuerURL              string
	OIDCConnectDiscoveryURL           string
	OIDCConnectAuthorizeURL           string
	OIDCConnectTokenURL               string
	OIDCConnectUserInfoURL            string
	OIDCConnectJWKSURL                string
	OIDCConnectScopes                 string
	OIDCConnectRedirectURL            string
	OIDCConnectFrontendRedirectURL    string
	OIDCConnectTokenAuthMethod        string
	OIDCConnectUsePKCE                bool
	OIDCConnectValidateIDToken        bool
	OIDCConnectAllowedSigningAlgs     string
	OIDCConnectClockSkewSeconds       int
	OIDCConnectRequireEmailVerified   bool
	OIDCConnectUserInfoEmailPath      string
	OIDCConnectUserInfoIDPath         string
	OIDCConnectUserInfoUsernamePath   string

	// GitHub / Google 邮箱快捷登录
	GitHubOAuthEnabled                bool
	GitHubOAuthClientID               string
	GitHubOAuthClientSecret           string
	GitHubOAuthClientSecretConfigured bool
	GitHubOAuthRedirectURL            string
	GitHubOAuthFrontendRedirectURL    string
	GoogleOAuthEnabled                bool
	GoogleOAuthClientID               string
	GoogleOAuthClientSecret           string
	GoogleOAuthClientSecretConfigured bool
	GoogleOAuthRedirectURL            string
	GoogleOAuthFrontendRedirectURL    string

	SiteName                    string
	SiteLogo                    string
	SiteSubtitle                string
	APIBaseURL                  string
	ContactInfo                 string
	DocURL                      string
	HomeContent                 string
	HideCcsImportButton         bool
	PurchaseSubscriptionEnabled bool
	PurchaseSubscriptionURL     string
	TableDefaultPageSize        int
	TablePageSizeOptions        []int
	CustomMenuItems             string // JSON array of custom menu items
	CustomEndpoints             string // JSON array of custom endpoints

	DefaultConcurrency           int
	DefaultBalance               float64
	RiskControlEnabled           bool
	CyberSessionBlockEnabled     bool
	CyberSessionBlockTTLSeconds  int
	CyberPolicyUserAllowlist     string // cyber 只记录不拦截的用户 ID 白名单（原文，逗号/空白分隔）
	AffiliateEnabled             bool
	AffiliateRebateRate          float64
	AffiliateRebateFreezeHours   int
	AffiliateRebateDurationDays  int
	AffiliateRebatePerInviteeCap float64
	DefaultUserRPMLimit          int
	DefaultSubscriptions         []DefaultSubscriptionSetting

	// Model fallback configuration
	EnableModelFallback      bool   `json:"enable_model_fallback"`
	FallbackModelAnthropic   string `json:"fallback_model_anthropic"`
	FallbackModelOpenAI      string `json:"fallback_model_openai"`
	FallbackModelGemini      string `json:"fallback_model_gemini"`
	FallbackModelAntigravity string `json:"fallback_model_antigravity"`

	// Identity patch configuration (Claude -> Gemini)
	EnableIdentityPatch bool   `json:"enable_identity_patch"`
	IdentityPatchPrompt string `json:"identity_patch_prompt"`

	// Ops monitoring (vNext)
	OpsMonitoringEnabled         bool
	OpsRealtimeMonitoringEnabled bool
	OpsQueryModeDefault          string
	OpsMetricsIntervalSeconds    int

	// Channel Monitor feature
	ChannelMonitorEnabled                bool   `json:"channel_monitor_enabled"`
	ChannelMonitorMode                   string `json:"channel_monitor_mode"`
	ChannelMonitorDefaultIntervalSeconds int    `json:"channel_monitor_default_interval_seconds"`
	ChannelMonitorHideThroughput         bool   `json:"channel_monitor_hide_throughput"`

	// Available Channels feature (user-facing aggregate view)
	AvailableChannelsEnabled bool `json:"available_channels_enabled"`

	// Claude Code version check
	MinClaudeCodeVersion string
	MaxClaudeCodeVersion string

	// 分组隔离：允许未分组 Key 调度（默认 false → 403）
	AllowUngroupedKeyScheduling bool

	// Backend 模式：禁用用户注册和自助服务，仅管理员可登录
	BackendModeEnabled bool

	// Gateway forwarding behavior
	EnableFingerprintUnification           bool   // 是否统一 OAuth 账号的指纹头（默认 true）
	EnableMetadataPassthrough              bool   // 是否透传客户端原始 metadata（默认 false）
	EnableCCHSigning                       bool   // 已废弃 no-op：新版 Claude Code CLI 取消 cch 签名后网关不再注入/签名 cch，开关无效果
	EnableClaudeOAuthSystemPromptInjection bool   // 是否对 Claude OAuth mimic 路径注入 Claude Code system blocks（默认 true）
	ClaudeOAuthSystemPrompt                string // Claude OAuth mimic 路径注入的通用扩展 system prompt；空值使用内置默认
	ClaudeOAuthSystemPromptBlocks          string // Claude OAuth mimic 路径注入的 system blocks JSON 配置；空值使用内置默认
	EnableAnthropicCacheTTL1hInjection     bool   // 是否对 Anthropic OAuth/SetupToken 请求体注入 1h cache_control ttl（默认 false）
	EnableClientDatelineNormalization      bool   // 是否对 Anthropic OAuth/SetupToken 请求体做客户端 dateline 归一化（默认 true）
	RewriteMessageCacheControl             bool   // 是否改写 messages[*].content[*].cache_control（默认 false）
	AntigravityUserAgentVersion            string // Antigravity 上游 User-Agent 版本号；空值使用配置/默认值
	OpenAICodexUserAgent                   string // OpenAI Codex 上游完整 User-Agent；空值使用内置默认
	OpenAICodexClientVersion               string // 管理员固定版本；空值跟随自动同步
	OpenAICodexClientVersionSynced         string // 自动同步得到的官方稳定版，只读展示
	OpenAICodexVersionAutoSyncEnabled      bool   // 是否每 6 小时同步官方稳定版
	OpenAICodexTicketHarvestProxyURL       string // Codex 292 打票代理 URL；空则回退 yaml/env
	OpenAICodexTicketEnabled               bool   // [v0.2.6] Codex 292 打票功能开关
	OpenAIAllowClaudeCodeCodexPlugin       bool   // 全局开关：是否额外放行 Claude Code 的 Codex 插件（默认 false）
	GrokDefaultTextModel                   string // Grok 空映射账号使用的默认文本模型
	GrokCrossClientModelMapEnabled         bool   // 是否启用 GPT/Codex/Claude 到 Grok 的通配映射
	GrokSchedulingThresholdPercent         int    // 1..100；100 表示关闭提前停调

	// Web Search Emulation
	WebSearchEmulationEnabled bool // 是否启用 web search 模拟

	// Payment visible method routing
	PaymentVisibleMethodAlipaySource  string
	PaymentVisibleMethodWxpaySource   string
	PaymentVisibleMethodAlipayEnabled bool
	PaymentVisibleMethodWxpayEnabled  bool

	// OpenAI 账号调度
	OpenAIAdvancedSchedulerEnabled bool
	// Sub2API 上游采购倍率探测与调度软权重。默认全部关闭；只影响上游账号选择，绝不参与客户销售计费。
	UpstreamBillingAutoProbeEnabled     bool
	UpstreamBillingProbeIntervalMinutes int
	UpstreamBillingProbeBatchSize       int
	UpstreamBillingProbeConcurrency     int
	OpenAIUpstreamCostSchedulingEnabled bool
	OpenAIUpstreamCostWeight            float64
	OpenAIQuotaHeadroomWeight           float64
	OpenAIPreferSoonestResetWeight      float64

	// 余额不足提醒
	BalanceLowNotifyEnabled     bool
	BalanceLowNotifyThreshold   float64
	BalanceLowNotifyRechargeURL string

	// 订阅到期提醒
	SubscriptionExpiryNotifyEnabled bool

	// 账号限额通知
	AccountQuotaNotifyEnabled bool
	AccountQuotaNotifyEmails  []NotifyEmailEntry

	// 系统全局默认平台配额（key = platform，nil/缺省 = 不限制）
	DefaultPlatformQuotas map[string]*DefaultPlatformQuotaSetting `json:"default_platform_quotas"`

	// 允许终端用户在用量页查看自己的失败请求
	AllowUserViewErrorRequests bool
}

type DefaultSubscriptionSetting struct {
	GroupID      int64 `json:"group_id"`
	ValidityDays int   `json:"validity_days"`
}

type PublicSettings struct {
	RegistrationEnabled              bool
	EmailVerifyEnabled               bool
	ForceEmailOnThirdPartySignup     bool
	RegistrationEmailSuffixWhitelist []string
	RunMode                          string
	UISurface                        string
	GatewayLiteAdminEnabled          bool
	CustomerSelfServiceEnabled       bool
	PromoCodeEnabled                 bool
	PasswordResetEnabled             bool
	InvitationCodeEnabled            bool
	TotpEnabled                      bool // TOTP 双因素认证
	LoginAgreementEnabled            bool
	LoginAgreementMode               string
	LoginAgreementUpdatedAt          string
	LoginAgreementRevision           string
	LoginAgreementDocuments          []LoginAgreementDocument
	TurnstileEnabled                 bool
	TurnstileSiteKey                 string
	SiteName                         string
	SiteLogo                         string
	SiteSubtitle                     string
	APIBaseURL                       string
	ContactInfo                      string
	DocURL                           string
	HomeContent                      string
	HideCcsImportButton              bool

	PurchaseSubscriptionEnabled bool
	PurchaseSubscriptionURL     string
	TableDefaultPageSize        int
	TablePageSizeOptions        []int
	CustomMenuItems             string // JSON array of custom menu items
	CustomEndpoints             string // JSON array of custom endpoints

	LinuxDoOAuthEnabled      bool
	DingTalkOAuthEnabled     bool
	WeChatOAuthEnabled       bool
	WeChatOAuthOpenEnabled   bool
	WeChatOAuthMPEnabled     bool
	WeChatOAuthMobileEnabled bool
	BackendModeEnabled       bool
	PaymentEnabled           bool
	OIDCOAuthEnabled         bool
	OIDCOAuthProviderName    string
	GitHubOAuthEnabled       bool
	GoogleOAuthEnabled       bool
	Version                  string

	BalanceLowNotifyEnabled     bool
	AccountQuotaNotifyEnabled   bool
	BalanceLowNotifyThreshold   float64
	BalanceLowNotifyRechargeURL string

	// Channel Monitor feature
	ChannelMonitorEnabled                bool   `json:"channel_monitor_enabled"`
	ChannelMonitorMode                   string `json:"channel_monitor_mode"`
	ChannelMonitorDefaultIntervalSeconds int    `json:"channel_monitor_default_interval_seconds"`
	ChannelMonitorHideThroughput         bool   `json:"channel_monitor_hide_throughput"`

	// Available Channels feature (user-facing aggregate view)
	AvailableChannelsEnabled bool `json:"available_channels_enabled"`

	// Affiliate (邀请返利) feature toggle
	AffiliateEnabled bool `json:"affiliate_enabled"`

	// 风控中心功能开关
	RiskControlEnabled bool `json:"risk_control_enabled"`

	// 允许终端用户在用量页查看自己的失败请求
	AllowUserViewErrorRequests bool `json:"allow_user_view_error_requests"`
}

type LoginAgreementDocument struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	ContentMD string `json:"content_md"`
}

type WeChatConnectOAuthConfig struct {
	Enabled             bool
	LegacyAppID         string
	LegacyAppSecret     string
	OpenAppID           string
	OpenAppSecret       string
	MPAppID             string
	MPAppSecret         string
	MobileAppID         string
	MobileAppSecret     string
	OpenEnabled         bool
	MPEnabled           bool
	MobileEnabled       bool
	Mode                string
	Scopes              string
	RedirectURL         string
	FrontendRedirectURL string
}

func (cfg WeChatConnectOAuthConfig) SupportsMode(mode string) bool {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return cfg.MPEnabled
	case "mobile":
		return cfg.MobileEnabled
	default:
		return cfg.OpenEnabled
	}
}

func (cfg WeChatConnectOAuthConfig) ScopeForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return normalizeWeChatConnectScopeSetting(cfg.Scopes, "mp")
	case "mobile":
		return ""
	}
	return defaultWeChatConnectScopeForMode("open")
}

func (cfg WeChatConnectOAuthConfig) AppIDForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return strings.TrimSpace(firstNonEmpty(cfg.MPAppID, cfg.LegacyAppID))
	case "mobile":
		return strings.TrimSpace(firstNonEmpty(cfg.MobileAppID, cfg.LegacyAppID))
	}
	return strings.TrimSpace(firstNonEmpty(cfg.OpenAppID, cfg.LegacyAppID))
}

func (cfg WeChatConnectOAuthConfig) AppSecretForMode(mode string) string {
	switch normalizeWeChatConnectModeSetting(mode) {
	case "mp":
		return strings.TrimSpace(firstNonEmpty(cfg.MPAppSecret, cfg.LegacyAppSecret))
	case "mobile":
		return strings.TrimSpace(firstNonEmpty(cfg.MobileAppSecret, cfg.LegacyAppSecret))
	}
	return strings.TrimSpace(firstNonEmpty(cfg.OpenAppSecret, cfg.LegacyAppSecret))
}

// StreamTimeoutSettings 流超时处理配置（仅控制超时后的处理方式，超时判定由网关配置控制）
type StreamTimeoutSettings struct {
	// Enabled 是否启用流超时处理
	Enabled bool `json:"enabled"`
	// Action 超时后的处理方式: "temp_unsched" | "error" | "none"
	Action string `json:"action"`
	// TempUnschedMinutes 临时不可调度持续时间（分钟）
	TempUnschedMinutes int `json:"temp_unsched_minutes"`
	// ThresholdCount 触发阈值次数（累计多少次超时才触发）
	ThresholdCount int `json:"threshold_count"`
	// ThresholdWindowMinutes 阈值窗口时间（分钟）
	ThresholdWindowMinutes int `json:"threshold_window_minutes"`
}

// StreamTimeoutAction 流超时处理方式常量
const (
	StreamTimeoutActionTempUnsched = "temp_unsched" // 临时不可调度
	StreamTimeoutActionError       = "error"        // 标记为错误状态
	StreamTimeoutActionNone        = "none"         // 不处理
)

// DefaultStreamTimeoutSettings 返回默认的流超时配置
func DefaultStreamTimeoutSettings() *StreamTimeoutSettings {
	return &StreamTimeoutSettings{
		Enabled:                false,
		Action:                 StreamTimeoutActionTempUnsched,
		TempUnschedMinutes:     5,
		ThresholdCount:         3,
		ThresholdWindowMinutes: 10,
	}
}

// RectifierSettings 请求整流器配置
type RectifierSettings struct {
	Enabled                  bool     `json:"enabled"`                    // 总开关
	ThinkingSignatureEnabled bool     `json:"thinking_signature_enabled"` // Thinking 签名整流
	ThinkingBudgetEnabled    bool     `json:"thinking_budget_enabled"`    // Thinking Budget 整流
	APIKeySignatureEnabled   bool     `json:"apikey_signature_enabled"`   // API Key 签名整流开关
	APIKeySignaturePatterns  []string `json:"apikey_signature_patterns"`  // API Key 自定义匹配关键词
}

// DefaultRectifierSettings 返回默认的整流器配置（全部启用）
func DefaultRectifierSettings() *RectifierSettings {
	return &RectifierSettings{
		Enabled:                  true,
		ThinkingSignatureEnabled: true,
		ThinkingBudgetEnabled:    true,
	}
}

// Beta Policy 策略常量
const (
	BetaPolicyActionPass   = "pass"   // 透传，不做任何处理
	BetaPolicyActionFilter = "filter" // 过滤，从 beta header 中移除该 token
	BetaPolicyActionBlock  = "block"  // 拦截，直接返回错误

	BetaPolicyScopeAll     = "all"     // 所有账号类型
	BetaPolicyScopeOAuth   = "oauth"   // 仅 OAuth 账号
	BetaPolicyScopeAPIKey  = "apikey"  // 仅 API Key 账号
	BetaPolicyScopeBedrock = "bedrock" // 仅 AWS Bedrock 账号
)

// BetaPolicyRule 单条 Beta 策略规则
type BetaPolicyRule struct {
	BetaToken            string   `json:"beta_token"`                       // beta token 值
	Action               string   `json:"action"`                           // "pass" | "filter" | "block"
	Scope                string   `json:"scope"`                            // "all" | "oauth" | "apikey" | "bedrock"
	ErrorMessage         string   `json:"error_message,omitempty"`          // 自定义错误消息 (action=block 时生效)
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // 模型匹配模式列表（为空=对所有模型生效）
	FallbackAction       string   `json:"fallback_action,omitempty"`        // 未匹配白名单的模型的处理方式
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // 未匹配白名单时的自定义错误消息 (fallback_action=block 时生效)
}

// BetaPolicySettings Beta 策略配置
type BetaPolicySettings struct {
	Rules []BetaPolicyRule `json:"rules"`
}

// OverloadCooldownSettings 529过载冷却配置
type OverloadCooldownSettings struct {
	// Enabled 是否在收到529时暂停账号调度
	Enabled bool `json:"enabled"`
	// CooldownMinutes 冷却时长（分钟）
	CooldownMinutes int `json:"cooldown_minutes"`
}

// RateLimit429CooldownSettings 429默认回避配置
type RateLimit429CooldownSettings struct {
	// Enabled 是否在无法解析上游重置时间时应用默认429回避
	Enabled bool `json:"enabled"`
	// CooldownSeconds 默认回避时长（秒）
	CooldownSeconds int `json:"cooldown_seconds"`
}

// FallbackQueueSettings 兜底账号排队配置
type FallbackQueueSettings struct {
	// Enabled 是否允许账号并发满时进入兜底排队等待
	Enabled bool `json:"enabled"`
	// MaxWaiting 单账号最多允许排队等待的请求数
	MaxWaiting int `json:"max_waiting"`
	// WaitTimeoutSeconds 单个等待请求最多等待账号槽位的秒数
	WaitTimeoutSeconds int `json:"wait_timeout_seconds"`
}

// [tokenav 定制] 以下为 v0.2.5 合并引入的官方 OpenAI 池 API Key 熔断配置。
// 官方把该类型定义在 settings_view.go，我们保留同一定位，避免与本仓库既有配置项命名冲突。
// OpenAIAPIKeyHealthBreakerSettings 控制 OpenAI 池 API Key 的跨实例失败计数与熔断。
type OpenAIAPIKeyHealthBreakerSettings struct {
	Enabled          bool `json:"enabled"`
	WindowMinutes    int  `json:"window_minutes"`
	FailureThreshold int  `json:"failure_threshold"`
	CooldownMinutes  int  `json:"cooldown_minutes"`
}

// DefaultOpenAIAPIKeyHealthBreakerSettings 返回默认的熔断配置（默认关闭，避免影响存量渠道）。
func DefaultOpenAIAPIKeyHealthBreakerSettings() *OpenAIAPIKeyHealthBreakerSettings {
	return &OpenAIAPIKeyHealthBreakerSettings{
		Enabled:          false,
		WindowMinutes:    2,
		FailureThreshold: 10,
		CooldownMinutes:  5,
	}
}

// DefaultOverloadCooldownSettings 返回默认的过载冷却配置（启用，10分钟）
func DefaultOverloadCooldownSettings() *OverloadCooldownSettings {
	return &OverloadCooldownSettings{
		Enabled:         true,
		CooldownMinutes: 10,
	}
}

// DefaultRateLimit429CooldownSettings 返回默认的429回避配置（启用，5秒）
func DefaultRateLimit429CooldownSettings() *RateLimit429CooldownSettings {
	return &RateLimit429CooldownSettings{
		Enabled:         true,
		CooldownSeconds: 5,
	}
}

// DefaultFallbackQueueSettings 返回默认兜底排队配置（启用，最多100个等待请求）
func DefaultFallbackQueueSettings() *FallbackQueueSettings {
	return &FallbackQueueSettings{
		Enabled:            true,
		MaxWaiting:         100,
		WaitTimeoutSeconds: 120,
	}
}

// DefaultBetaPolicySettings 返回默认的 Beta 策略配置。
//
// context-1m-2025-08-07 的默认策略：
//   - 仅 claude-sonnet-5 及后续版本在上游默认支持 1M 上下文。
//   - Sonnet 4.x 及以下、Opus、Haiku 上游都不支持该 beta，透传会导致上游 400 或降级。
//   - 因此默认对 sonnet-5* 放行，其余模型过滤。
func DefaultBetaPolicySettings() *BetaPolicySettings {
	return &BetaPolicySettings{
		Rules: []BetaPolicyRule{
			{
				BetaToken: "fast-mode-2026-02-01",
				Action:    BetaPolicyActionFilter,
				Scope:     BetaPolicyScopeAll,
			},
			{
				BetaToken: "context-1m-2025-08-07",
				Action:    BetaPolicyActionPass,
				Scope:     BetaPolicyScopeAll,
				ModelWhitelist: []string{
					"claude-sonnet-5",
					"claude-sonnet-5-*",
					"claude-sonnet-5@*",
					"us.anthropic.claude-sonnet-5*",
					"eu.anthropic.claude-sonnet-5*",
					"apac.anthropic.claude-sonnet-5*",
					"jp.anthropic.claude-sonnet-5*",
					"au.anthropic.claude-sonnet-5*",
					"us-gov.anthropic.claude-sonnet-5*",
					"global.anthropic.claude-sonnet-5*",
					"anthropic.claude-sonnet-5*",
				},
				FallbackAction: BetaPolicyActionFilter,
			},
		},
	}
}

// OpenAI Fast Policy 策略常量
// OpenAI 的 "fast 模式" 通过请求体中的 service_tier 字段识别：
//   - "priority"（客户端可传 "fast"，归一化为 "priority"）：fast 模式
//   - "flex"：低优先级模式
//   - 省略：normal 默认
//
// 本策略复用 BetaPolicyAction*/BetaPolicyScope* 常量语义，只是匹配键从
// anthropic-beta header 换成 body 的 service_tier 字段。
const (
	OpenAIFastTierAny       = "all"       // 匹配任意已识别的 service_tier
	OpenAIFastTierPriority  = "priority"  // 仅匹配 fast（priority）
	OpenAIFastTierUltrafast = "ultrafast" // [v0.2.5 新增] 仅匹配 ultrafast
	OpenAIFastTierFlex      = "flex"      // 仅匹配 flex

	// 只改写已经存在且可识别的 service_tier，不给普通请求凭空注入 Priority。
	OpenAIFastPolicyActionForcePriority = "force_priority"
	OpenAIFastPolicySettingsVersion     = 1

	// 仅用于读取历史日志和迁移旧测试数据；新设置与请求热路径不再使用三态模式。
	OpenAIPriorityModeFollowRequest     = "follow_request"
	OpenAIPriorityModeForcePriority     = "force_priority"
	OpenAIPriorityModeForceStandard     = "force_standard"
	OpenAIPriorityModeKeyPreferPriority = "key_prefer_priority"
)

// OpenAIFastPolicyRule 单条 OpenAI fast/flex 策略规则
type OpenAIFastPolicyRule struct {
	ID                   string   `json:"id,omitempty"`                     // 稳定规则标识，用于日志审计
	ServiceTier          string   `json:"service_tier"`                     // "priority" | "flex" | "auto" | "default" | "scale" | "all"
	Action               string   `json:"action"`                           // "pass" | "filter" | "block" | "force_priority"
	Scope                string   `json:"scope"`                            // "all" | "oauth" | "apikey" | "bedrock"
	UserIDs              []int64  `json:"user_ids,omitempty"`               // 空=所有用户；非空=仅指定 API Key 所属用户
	ErrorMessage         string   `json:"error_message,omitempty"`          // 自定义错误消息 (action=block 时生效)
	ModelWhitelist       []string `json:"model_whitelist,omitempty"`        // 模型匹配模式列表（为空=对所有模型生效）
	FallbackAction       string   `json:"fallback_action,omitempty"`        // 未匹配白名单的模型的处理方式
	FallbackErrorMessage string   `json:"fallback_error_message,omitempty"` // 未匹配白名单时的自定义错误消息 (fallback_action=block 时生效)
}

// OpenAIFastPolicySettings OpenAI fast 策略配置
type OpenAIFastPolicySettings struct {
	Version                    int                    `json:"version,omitempty"`
	DetectionByRequestEnabled  bool                   `json:"priority_detection_by_request_enabled"`
	DetectionByResponseEnabled bool                   `json:"priority_detection_by_response_enabled"`
	Rules                      []OpenAIFastPolicyRule `json:"rules"`
}

// DefaultOpenAIFastPolicySettings 返回默认的 OpenAI fast 策略配置。
// 默认不配置任何规则，保留 OpenAI 上游 service_tier 语义；管理员如需
// 限制 priority/flex，可以在 admin UI 中显式配置 filter 或 block 规则。
func DefaultOpenAIFastPolicySettings() *OpenAIFastPolicySettings {
	return &OpenAIFastPolicySettings{
		Version:                    OpenAIFastPolicySettingsVersion,
		DetectionByRequestEnabled:  true,
		DetectionByResponseEnabled: true,
		Rules:                      []OpenAIFastPolicyRule{},
	}
}
