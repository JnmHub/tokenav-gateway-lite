package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/platform/liveattestation"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/cespare/xxhash/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const (
	// ChatGPT internal API for OAuth accounts
	chatgptCodexURL = "https://chatgpt.com/backend-api/codex/responses"
	// OpenAI Platform API for API Key accounts (fallback)
	openaiPlatformAPIURL            = "https://api.openai.com/v1/responses"
	openaiPlatformAPIInputTokensURL = "https://api.openai.com/v1/responses/input_tokens"
	openaiStickySessionTTL          = time.Hour // 粘性会话TTL
	// 与真实 Codex CLI 的 User-Agent 结构对齐：
	// {originator}/{version} ({OS} {OS_version}; {arch}) {terminal}
	// 版本与当前 Codex 客户端保持一致；旧版本可能被上游优先降载。
	codexCLIUserAgentSuffix = " (Ubuntu 22.4.0; x86_64) xterm-256color"
	codexCLIUserAgent       = openai.CodexDefaultOriginator + "/" + codexCLIVersion + codexCLIUserAgentSuffix
	// codex_cli_only 拒绝时单个请求头日志长度上限（字符）
	codexCLIOnlyHeaderValueMaxBytes = 256

	// OpenAI WS Mode 失败后的重连次数上限（不含首次尝试）。
	// 与 Codex 客户端保持一致：失败后最多重连 5 次。
	openAIWSReconnectRetryLimit = 5
	// 上游错误体只需要提取错误 JSON/日志摘要，默认 512KiB 避免错误风暴叠加大请求体。
	openAIUpstreamErrorBodyReadLimit int64 = 512 << 10
	// OpenAI WS Mode 重连退避默认值（可由配置覆盖）。
	openAIWSRetryBackoffInitialDefault = 120 * time.Millisecond
	openAIWSRetryBackoffMaxDefault     = 2 * time.Second
	openAIWSRetryJitterRatioDefault    = 0.2
	openAICompactSessionSeedKey        = "openai_compact_session_seed"
	codexCLIVersion                    = "0.146.0"
	// Codex 限额快照仅用于后台展示/诊断，不需要每个成功请求都立即落库。
	openAICodexSnapshotPersistMinInterval = 30 * time.Second
	// 配额自动暂停时，超过该时长仍未刷新的 used% 快照视为陈旧，不再据此暂停账号。
	// 被暂停的账号收不到流量，其快照永远不会从上游响应头刷新；该兜底让账号在快照
	// 陈旧时放行一次请求，从而通过正常响应头自愈，而无需等待整个窗口（5h/7d）重置。
	openAICodexAutoPauseStaleAfter = 2 * time.Hour
)

var explicitOpenAIHeaderSessionNames = []string{
	"session-id",
	"session_id",
	"conversation_id",
	openCodeSessionAffinityHeader,
	openCodeSessionIDHeader,
	openCodeNativeSessionHeader,
	codeBuddyConversationHeader,
}

// OpenAI allowed headers whitelist (for non-passthrough).
var openaiAllowedHeaders = map[string]bool{
	"accept-language":         true,
	"content-type":            true,
	"conversation_id":         true,
	"user-agent":              true,
	"originator":              true,
	"session-id":              true,
	"session_id":              true,
	"x-codex-beta-features":   true,
	"x-codex-installation-id": true,
	"x-codex-turn-state":      true,
	"x-codex-turn-metadata":   true,
	"x-codex-window-id":       true,
	responsesLiteHeaderKey:    true,
}

// OpenAI passthrough allowed headers whitelist.
// 透传模式下仅放行这些低风险请求头，避免将非标准/环境噪声头传给上游触发风控。
var openaiPassthroughAllowedHeaders = map[string]bool{
	"accept":                  true,
	"accept-language":         true,
	"content-type":            true,
	"conversation_id":         true,
	"openai-beta":             true,
	"user-agent":              true,
	"originator":              true,
	"session-id":              true,
	"session_id":              true,
	"x-codex-beta-features":   true,
	"x-codex-installation-id": true,
	"x-codex-turn-state":      true,
	"x-codex-turn-metadata":   true,
	"x-codex-window-id":       true,
	responsesLiteHeaderKey:    true,
}

// codex_cli_only 拒绝时记录的请求头白名单（仅用于诊断日志，不参与上游透传）
var codexCLIOnlyDebugHeaderWhitelist = []string{
	"User-Agent",
	"Content-Type",
	"Accept",
	"Accept-Language",
	"OpenAI-Beta",
	"Originator",
	"Session_ID",
	"Conversation_ID",
	"X-Request-ID",
	"X-Client-Request-ID",
	"X-Forwarded-For",
	"X-Real-IP",
}

// OpenAICodexUsageSnapshot represents Codex API usage limits from response headers
type OpenAICodexUsageSnapshot struct {
	PrimaryUsedPercent          *float64 `json:"primary_used_percent,omitempty"`
	PrimaryResetAfterSeconds    *int     `json:"primary_reset_after_seconds,omitempty"`
	PrimaryWindowMinutes        *int     `json:"primary_window_minutes,omitempty"`
	SecondaryUsedPercent        *float64 `json:"secondary_used_percent,omitempty"`
	SecondaryResetAfterSeconds  *int     `json:"secondary_reset_after_seconds,omitempty"`
	SecondaryWindowMinutes      *int     `json:"secondary_window_minutes,omitempty"`
	PrimaryOverSecondaryPercent *float64 `json:"primary_over_secondary_percent,omitempty"`
	UpdatedAt                   string   `json:"updated_at,omitempty"`
}

// NormalizedCodexLimits contains normalized 5h/7d rate limit data
type NormalizedCodexLimits struct {
	Used5hPercent   *float64
	Reset5hSeconds  *int
	Window5hMinutes *int
	Used7dPercent   *float64
	Reset7dSeconds  *int
	Window7dMinutes *int
}

// Normalize converts primary/secondary fields to canonical 5h/7d fields.
// Strategy: Compare window_minutes to determine which is 5h vs 7d.
// Returns nil if snapshot is nil or has no useful data.
func (s *OpenAICodexUsageSnapshot) Normalize() *NormalizedCodexLimits {
	if s == nil {
		return nil
	}

	result := &NormalizedCodexLimits{}

	primaryMins := 0
	secondaryMins := 0
	hasPrimaryWindow := false
	hasSecondaryWindow := false

	if s.PrimaryWindowMinutes != nil {
		primaryMins = *s.PrimaryWindowMinutes
		hasPrimaryWindow = true
	}
	if s.SecondaryWindowMinutes != nil {
		secondaryMins = *s.SecondaryWindowMinutes
		hasSecondaryWindow = true
	}

	// Determine mapping based on window_minutes
	use5hFromPrimary := false
	use7dFromPrimary := false

	if hasPrimaryWindow && hasSecondaryWindow {
		// Both known: smaller window is 5h, larger is 7d
		if primaryMins < secondaryMins {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasPrimaryWindow {
		// Only primary known: classify by threshold (<=360 min = 6h -> 5h window)
		if primaryMins <= 360 {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasSecondaryWindow {
		// Only secondary known: classify by threshold
		if secondaryMins <= 360 {
			// 5h from secondary, so primary (if any data) is 7d
			use7dFromPrimary = true
		} else {
			// 7d from secondary, so primary (if any data) is 5h
			use5hFromPrimary = true
		}
	} else {
		// No window_minutes: fall back to legacy assumption (primary=7d, secondary=5h)
		use7dFromPrimary = true
	}

	// Assign values
	if use5hFromPrimary {
		result.Used5hPercent = s.PrimaryUsedPercent
		result.Reset5hSeconds = s.PrimaryResetAfterSeconds
		result.Window5hMinutes = s.PrimaryWindowMinutes
		result.Used7dPercent = s.SecondaryUsedPercent
		result.Reset7dSeconds = s.SecondaryResetAfterSeconds
		result.Window7dMinutes = s.SecondaryWindowMinutes
	} else if use7dFromPrimary {
		result.Used7dPercent = s.PrimaryUsedPercent
		result.Reset7dSeconds = s.PrimaryResetAfterSeconds
		result.Window7dMinutes = s.PrimaryWindowMinutes
		result.Used5hPercent = s.SecondaryUsedPercent
		result.Reset5hSeconds = s.SecondaryResetAfterSeconds
		result.Window5hMinutes = s.SecondaryWindowMinutes
	}

	return result
}

// OpenAIUsage represents OpenAI API response usage
type OpenAIUsage struct {
	InputTokens              int `json:"input_tokens"`
	ImageInputTokens         int `json:"image_input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	ImageOutputTokens        int `json:"image_output_tokens,omitempty"`
	ImageCacheReadTokens     int // [v0.2.5 新增字段]
}

// OpenAIForwardResult represents the result of forwarding
type OpenAIForwardResult struct {
	RequestID  string
	ResponseID string
	// ResponseBody is retained only for small JSON control-plane responses that
	// need a durable local binding after forwarding (for example custom voices).
	// Large media bytes are never copied into this field.
	ResponseBody []byte
	// ResponseStatus is retained for deferred pass-through responses such as STT,
	// where the handler must audit returned text before writing any client bytes.
	ResponseStatus int
	Usage          OpenAIUsage
	Model          string // 原始模型（用于响应和日志显示）
	// BillingModel is the model used for cost calculation.
	// When non-empty, CalculateCost uses this instead of Model.
	// This is set by the Anthropic Messages conversion path where
	// the mapped upstream model differs from the client-facing model.
	BillingModel string
	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Empty when no mapping was applied (requested model was used as-is).
	UpstreamModel                 string
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	UpstreamModelMismatch         *bool
	// UpstreamEndpoint records the actual API path used when one inbound protocol
	// can safely fall back to a different upstream endpoint.
	UpstreamEndpoint string
	// ServiceTier records the tier actually reported by the upstream response.
	// Compatible upstreams that omit the response field fall back to the tier sent upstream.
	ServiceTier *string
	// SentServiceTier is the final value sent upstream after global and API Key policies.
	SentServiceTier *string
	// ResponseServiceTier is only set when the upstream explicitly reports service_tier.
	ResponseServiceTier *string
	// RequestedServiceTier records the normalized value supplied by the client before
	// the administrator's Priority policy rewrites it.
	RequestedServiceTier *string
	// PriorityPolicyMode records the global three-state policy used for this request.
	PriorityPolicyMode string
	// PriorityDetectionSource records whether final billing followed the request,
	// the upstream response, or the standard default when no enabled source had evidence.
	PriorityDetectionSource string
	PriorityPolicyAction    string
	PriorityPolicyRuleID    string
	PriorityPolicyFallback  bool
	// RequestedReasoningEffort is the explicit client value before policies.
	// ReasoningEffort is the explicit value in the final upstream payload.
	// Defaults and model suffixes are intentionally not inferred for audit logs.
	RequestedReasoningEffort *string
	ReasoningEffort          *string
	Stream                   bool
	OpenAIWSMode             bool
	ResponseHeaders          http.Header
	Duration                 time.Duration
	FirstTokenMs             *int
	FakeFirstTokenMs         *int // 伪造首字（SSE 注释行）发出时刻；未开启/未触发为 nil
	ClientDisconnect         bool
	ImageCount               int
	ImageSize                string
	ImageInputSize           string
	ImageOutputSize          string
	ImageOutputSizes         []string
	ImageSizeSource          string
	ImageSizeBreakdown       map[string]int
	VideoCount               int
	VideoResolution          string
	VideoDurationSeconds     int
	// WebSearchCalls counts successful Codex alpha/search calls. The upstream
	// does not return token usage for this endpoint, so it uses per-call billing.
	WebSearchCalls int
	// SearchCount records hosted Grok search tool calls for per-1k billing.
	SearchCount int
	// AudioUsage carries Voice billing units for realtime/TTS/STT.
	AudioUsage       *AudioUsage
	MediaTaskState   string
	MediaTaskMessage string

	wsReplayInput       []json.RawMessage
	wsReplayInputExists bool
	// wsAccountFailoverReplayInput keeps every completed output item needed to
	// rebuild the current turn on a replacement account. It is internal only and
	// never changes the client-visible response or billing model.
	wsAccountFailoverReplayInput []json.RawMessage
}

type OpenAIPriorityDecision struct {
	RequestedServiceTier       *string
	ServiceTier                *string
	PolicyAction               string
	PolicyRuleID               string
	PolicyRuleFallback         bool
	DetectionByRequestEnabled  bool
	DetectionByResponseEnabled bool
	FallbackReason             string
}

type openAIPriorityStandardRetry struct {
	RequestedServiceTier *string
	PolicyAction         string
	PolicyRuleID         string
	PolicyRuleFallback   bool
	DetectByRequest      bool
	DetectByResponse     bool
	Reason               string
}

type openAIPriorityStandardRetryContextKey struct{}

func contextWithOpenAIPriorityStandardRetry(ctx context.Context, decision OpenAIPriorityDecision, reason string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	retry := openAIPriorityStandardRetry{
		RequestedServiceTier: cloneOptionalString(decision.RequestedServiceTier),
		PolicyAction:         decision.PolicyAction,
		PolicyRuleID:         decision.PolicyRuleID,
		PolicyRuleFallback:   decision.PolicyRuleFallback,
		DetectByRequest:      decision.DetectionByRequestEnabled,
		DetectByResponse:     decision.DetectionByResponseEnabled,
		Reason:               strings.TrimSpace(reason),
	}
	return context.WithValue(ctx, openAIPriorityStandardRetryContextKey{}, retry)
}

func openAIPriorityStandardRetryFromContext(ctx context.Context) (openAIPriorityStandardRetry, bool) {
	if ctx == nil {
		return openAIPriorityStandardRetry{}, false
	}
	retry, ok := ctx.Value(openAIPriorityStandardRetryContextKey{}).(openAIPriorityStandardRetry)
	return retry, ok
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type OpenAIWSRetryMetricsSnapshot struct {
	RetryAttemptsTotal            int64 `json:"retry_attempts_total"`
	RetryBackoffMsTotal           int64 `json:"retry_backoff_ms_total"`
	RetryExhaustedTotal           int64 `json:"retry_exhausted_total"`
	NonRetryableFastFallbackTotal int64 `json:"non_retryable_fast_fallback_total"`
}

type OpenAICompatibilityFallbackMetricsSnapshot struct {
	SessionHashLegacyReadFallbackTotal int64   `json:"session_hash_legacy_read_fallback_total"`
	SessionHashLegacyReadFallbackHit   int64   `json:"session_hash_legacy_read_fallback_hit"`
	SessionHashLegacyDualWriteTotal    int64   `json:"session_hash_legacy_dual_write_total"`
	SessionHashLegacyReadHitRate       float64 `json:"session_hash_legacy_read_hit_rate"`

	MetadataLegacyFallbackIsMaxTokensOneHaikuTotal int64 `json:"metadata_legacy_fallback_is_max_tokens_one_haiku_total"`
	MetadataLegacyFallbackThinkingEnabledTotal     int64 `json:"metadata_legacy_fallback_thinking_enabled_total"`
	MetadataLegacyFallbackPrefetchedStickyAccount  int64 `json:"metadata_legacy_fallback_prefetched_sticky_account_total"`
	MetadataLegacyFallbackPrefetchedStickyGroup    int64 `json:"metadata_legacy_fallback_prefetched_sticky_group_total"`
	MetadataLegacyFallbackSingleAccountRetryTotal  int64 `json:"metadata_legacy_fallback_single_account_retry_total"`
	MetadataLegacyFallbackAccountSwitchCountTotal  int64 `json:"metadata_legacy_fallback_account_switch_count_total"`
	MetadataLegacyFallbackTotal                    int64 `json:"metadata_legacy_fallback_total"`
}

type openAIWSRetryMetrics struct {
	retryAttempts            atomic.Int64
	retryBackoffMs           atomic.Int64
	retryExhausted           atomic.Int64
	nonRetryableFastFallback atomic.Int64
}

type accountWriteThrottle struct {
	minInterval time.Duration
	mu          sync.Mutex
	lastByID    map[int64]time.Time
}

func newAccountWriteThrottle(minInterval time.Duration) *accountWriteThrottle {
	return &accountWriteThrottle{
		minInterval: minInterval,
		lastByID:    make(map[int64]time.Time),
	}
}

func (t *accountWriteThrottle) Allow(id int64, now time.Time) bool {
	if t == nil || id <= 0 || t.minInterval <= 0 {
		return true
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if last, ok := t.lastByID[id]; ok && now.Sub(last) < t.minInterval {
		return false
	}
	t.lastByID[id] = now

	if len(t.lastByID) > 4096 {
		cutoff := now.Add(-4 * t.minInterval)
		for accountID, writtenAt := range t.lastByID {
			if writtenAt.Before(cutoff) {
				delete(t.lastByID, accountID)
			}
		}
	}

	return true
}

var defaultOpenAICodexSnapshotPersistThrottle = newAccountWriteThrottle(openAICodexSnapshotPersistMinInterval)

// ErrNoAvailableCompactAccounts indicates the request needs /responses/compact
// support but no compatible account is available.
var ErrNoAvailableCompactAccounts = errors.New("no available accounts support /responses/compact")

// OpenAIGatewayService handles OpenAI API gateway operations
type OpenAIGatewayService struct {
	openAIModelsCache     openAIModelsCache // [v0.2.5 新增字段] 模型清单请求缓存（零值可用）
	pluginManager         *PluginManager    // [v0.2.5 新增字段] 插件管理器（可选，nil 时走直连）
	accountRepo           AccountRepository
	usageLogRepo          UsageLogRepository
	usageBillingRepo      UsageBillingRepository
	userRepo              UserRepository
	userSubRepo           UserSubscriptionRepository
	userGroupRateRepo     UserGroupRateRepository
	cache                 GatewayCache
	cfg                   *config.Config
	codexDetector         CodexClientRestrictionDetector
	schedulerSnapshot     *SchedulerSnapshotService
	concurrencyService    *ConcurrencyService
	billingService        *BillingService
	rateLimitService      *RateLimitService
	billingCacheService   *BillingCacheService
	userGroupRateResolver *userGroupRateResolver
	httpUpstream          HTTPUpstream
	// tlsFPProfileService 为可选注入：账号开启 TLS 指纹伪装后，OpenAI 热路径
	// 经 DoWithTLS 使用 utls 指纹，避免 Go stdlib 固定 ClientHello 与 UA 声称
	// 平台不符的连接层铁证。nil 时保持原有 Do 路径，行为不变。
	tlsFPProfileService   *TLSFingerprintProfileService
	deferredService       *DeferredService
	openAITokenProvider   *OpenAITokenProvider
	grokTokenProvider     *GrokTokenProvider
	toolCorrector         *CodexToolCorrector
	openaiWSResolver      OpenAIWSProtocolResolver
	resolver              *ModelPricingResolver
	channelService        *ChannelService
	balanceNotifyService  *BalanceNotifyService
	settingService        *SettingService
	userPlatformQuotaRepo UserPlatformQuotaRepository
	grokVideoTaskStore    GrokVideoTaskStore
	grokCustomVoiceStore  GrokCustomVoiceStore
	// syncMediaCache is optional so existing installations and unit tests can
	// continue serving base64/image responses without object-storage setup.
	// When configured, synchronous remote image URLs are copied before the
	// response leaves the gateway, preventing short-lived foreign URLs from
	// becoming unusable to customers without international network access.
	syncMediaCache *SyncMediaCache
	// Live 的 DeviceCheck（设备证明）按运行平台提供；Linux/Windows 会明确返回不支持，
	// 禁止伪造证明或在能力策略中猜测可用。
	liveAttestation       liveattestation.Provider
	liveAttestationCipher SecretEncryptor

	openaiWSPoolOnce              sync.Once
	openaiWSStateStoreOnce        sync.Once
	openaiSchedulerOnce           sync.Once
	openaiProxyStreamCircuitOnce  sync.Once
	openaiWSPassthroughDialerOnce sync.Once
	openaiModelTransientOnce      sync.Once
	openaiWSPool                  *openAIWSConnPool
	// [v0.2.5 新增字段] Codex 292 门票内存缓存与采集器生命周期
	openaiCodexTickets              sync.Map
	openaiWSSessionPreemptions      openAIWSSessionPreemptRegistry // [v0.2.5 新增字段]
	openaiCodexTicketFlight         singleflight.Group
	openaiCodexRouteCookies         sync.Map // key: int64(accountID), value: openAICodexRouteCookiePool（账号级路由 Cookie 池，见 openai_codex_cookie_pool.go）
	openaiCodexRouteCookiePersistAt sync.Map // key: int64(accountID), value: time.Time（Cookie 池落库节流戳）
	openaiCodexTicketLifecycleMu    sync.Mutex
	openaiCodexTicketStopped        bool
	openaiCodexTicketCancel         context.CancelFunc
	openaiCodexTicketDone           chan struct{}
	agentIdentityTaskMu             sync.Mutex
	openaiWSStateStore              OpenAIWSStateStore
	openaiScheduler                 OpenAIAccountScheduler
	openaiWSPassthroughDialer       openAIWSClientDialer
	openaiAccountStats              *openAIAccountRuntimeStats
	openaiModelTransient            *openAIAccountModelTransientState
	openaiProxyStreamCircuit        *openAIProxyStreamCircuit
	openaiProxyStreamFailOpenLogAt  atomic.Int64

	openaiWSFallbackUntil               sync.Map // key: int64(accountID), value: time.Time
	openaiAccountRuntimeBlockUntil      sync.Map // key: int64(accountID), value: time.Time
	openaiAccountRuntimeBlockLocks      sync.Map // key: int64(accountID), value: *sync.Mutex
	openaiAccountRuntimeBlockGeneration sync.Map // key: int64(accountID), value: uint64
	openaiAccountRuntimeBlockSequence   atomic.Uint64
	grokCredentialMutationLocks         sync.Map // key: int64(accountID), value: *oauthRefreshLocalLock
	openaiOAuth429WindowStartUnixNano   atomic.Int64
	openaiOAuth429WindowCount           atomic.Int64
	// OAuth 429 瞬时错误的请求级同账号重试窗口。配额耗尽时会删除该状态，
	// 避免后续请求绕过长窗口冷却；sync.Map 只保存活跃账号，窗口结束后惰性清理。
	openaiOAuth429RetryStartedAt        sync.Map // key: int64(accountID), value: time.Time
	openaiWSRetryMetrics                openAIWSRetryMetrics
	responseHeaderFilter                *responseheaders.CompiledHeaderFilter
	codexSnapshotThrottle               *accountWriteThrottle
	openaiStickyRefreshGate             *openAIStickyRefreshGate
	codexModelsManifestCache            codexModelsManifestCache
	openaiCompatSessionResponses        sync.Map
	openaiCompatAnthropicDigestSessions sync.Map
	// 下游会话 -> 最近一次下发 x-codex-turn-state 的上游账号。
	// 用于跨账号 failover 时剥离由旧账号铸造的状态。
	openaiCodexTurnStateOrigins sync.Map
	openaiCodexTurnStateWrites  atomic.Uint64
}

// NewOpenAIGatewayService creates a new OpenAIGatewayService
func NewOpenAIGatewayService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	resolver *ModelPricingResolver,
	channelService *ChannelService,
	balanceNotifyService *BalanceNotifyService,
	settingService *SettingService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
) *OpenAIGatewayService {
	if cfg != nil {
		SetCodexIdentityEnforcementEnabled(!cfg.Gateway.DisableCodexIdentityEnforcement)
	}
	svc := &OpenAIGatewayService{
		accountRepo:         accountRepo,
		usageLogRepo:        usageLogRepo,
		usageBillingRepo:    usageBillingRepo,
		userRepo:            userRepo,
		userSubRepo:         userSubRepo,
		userGroupRateRepo:   userGroupRateRepo,
		cache:               cache,
		cfg:                 cfg,
		codexDetector:       NewOpenAICodexClientRestrictionDetector(cfg),
		schedulerSnapshot:   schedulerSnapshot,
		concurrencyService:  concurrencyService,
		billingService:      billingService,
		rateLimitService:    rateLimitService,
		billingCacheService: billingCacheService,
		userGroupRateResolver: newUserGroupRateResolver(
			userGroupRateRepo,
			nil,
			resolveUserGroupRateCacheTTL(cfg),
			nil,
			"service.openai_gateway",
		),
		httpUpstream:          newFundingHTTPUpstream(httpUpstream, billingService, resolver),
		deferredService:       deferredService,
		openAITokenProvider:   openAITokenProvider,
		toolCorrector:         NewCodexToolCorrector(),
		openaiWSResolver:      NewOpenAIWSProtocolResolver(cfg),
		resolver:              resolver,
		channelService:        channelService,
		balanceNotifyService:  balanceNotifyService,
		settingService:        settingService,
		userPlatformQuotaRepo: userPlatformQuotaRepo,
		liveAttestation:       liveattestation.NewProvider(),
		liveAttestationCipher: newLiveAttestationCipher(cfg),
		responseHeaderFilter:  compileResponseHeaderFilter(cfg),
		codexSnapshotThrottle: newAccountWriteThrottle(openAICodexSnapshotPersistMinInterval),
		openaiStickyRefreshGate: newOpenAIStickyRefreshGate(
			openAIStickyRefreshMinInterval,
			openAIStickyRefreshMaxEntriesPerShard,
		),
		openaiModelTransient: newOpenAIAccountModelTransientState(openAIModelTransientDefaultMax),
	}
	if rateLimitService != nil {
		rateLimitService.SetAccountRuntimeBlocker(svc)
	}
	if openAITokenProvider != nil {
		openAITokenProvider.SetAccountRuntimeBlocker(svc)
	}
	svc.logOpenAIWSModeBootstrap()
	return svc
}

// FreezeRatePolicyForAPIKey refreshes the immutable billing evidence after a
// content policy reroutes an OpenAI-compatible request to another group.
func (s *OpenAIGatewayService) FreezeRatePolicyForAPIKey(ctx context.Context, apiKey *APIKey, now time.Time) (context.Context, error) {
	if s == nil {
		return ctx, errors.New("openai gateway service is unavailable")
	}
	return freezeRatePolicyForReroutedAPIKey(ctx, s.cfg, s.userGroupRateRepo, apiKey, now)
}

// AdmitCodexTailBurstAfterSlot applies the final Tail-Burst guard after an
// account concurrency slot has been acquired.  Protocol handlers use this
// wrapper instead of reaching into the rate-limit service, so a missing
// optional service keeps normal accounts unaffected.
func (s *OpenAIGatewayService) AdmitCodexTailBurstAfterSlot(ctx context.Context, account *Account) bool {
	if s == nil || s.rateLimitService == nil {
		return true
	}
	return s.rateLimitService.AdmitCodexTailBurstAfterSlot(ctx, account)
}

// SetGrokTokenProvider injects the refresh-aware xAI token provider without
// changing the legacy constructor used by existing OpenAI unit tests.
func (s *OpenAIGatewayService) SetGrokTokenProvider(provider *GrokTokenProvider) {
	if s == nil {
		return
	}
	s.grokTokenProvider = provider
}

// SetSyncMediaCache attaches the response-stage image copier without changing
// the long-standing service constructor.  Wire/bootstrap code can call this
// after both the object store and gateway service have been created.
func (s *OpenAIGatewayService) SetSyncMediaCache(cache *SyncMediaCache) {
	if s != nil {
		s.syncMediaCache = cache
	}
}

// SetTLSFingerprintProfileService 注入 TLS 指纹模板服务（可选依赖）。
// OpenAI OAuth 热路径此前直接走 httpUpstream.Do（Go stdlib 固定 TLS 指纹），
// 与请求 UA 声称的客户端平台不一致，是上游连接层识别的铁证之一。
// 注入后账号开启 TLS 指纹即经 DoWithTLS 伪装；未开启的账号不受影响。
func (s *OpenAIGatewayService) SetTLSFingerprintProfileService(profileService *TLSFingerprintProfileService) {
	if s != nil {
		s.tlsFPProfileService = profileService
	}
}

// doUpstreamRequest 是 OpenAI 上游请求的统一出口：
// 注入了 TLS 指纹服务且账号解析出 profile 时走 DoWithTLS，否则退回 Do。
// profile 为 nil 时 DoWithTLS 内部等价于 Do，此处显式分流便于排查日志。
func (s *OpenAIGatewayService) doUpstreamRequest(upstreamReq *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)
	if s.tlsFPProfileService != nil && account != nil {
		if profile := s.tlsFPProfileService.ResolveTLSProfile(account); profile != nil {
			resp, err = s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, profile)
		}
	}
	if resp == nil && err == nil {
		resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, accountIDOf(account), concurrencyOf(account))
	}
	// [2026-09 上游规则变更] 上游会在 Set-Cookie 里轮换账号级路由 Cookie
	// （__cfblb/__oailb），任意响应都可喂池（节流落库），保证出站注入始终有新值。
	if resp != nil {
		s.captureOpenAICodexRouteCookies(account, resp.Header)
	}
	return resp, err
}

// egressProxyURL 解析账号出口代理：代理池（extra.proxy_pool）多端点时按
// 连接池观测选择——熔断（最近连续失败）与满载端点自动避开、其余取最小在途；
// 无池或无观测时回退主代理。
func (s *OpenAIGatewayService) egressProxyURL(account *Account) string {
	if account == nil {
		return ""
	}
	if len(account.ProxyEndpoints()) <= 1 {
		if account.ProxyID != nil && account.Proxy != nil {
			return account.Proxy.URL()
		}
		return ""
	}
	observer, ok := s.httpUpstream.(ProxyEgressObserver)
	if !ok || observer == nil {
		if account.ProxyID != nil && account.Proxy != nil {
			return account.Proxy.URL()
		}
		return ""
	}
	return SelectEgressProxyHealthy(account,
		func(proxyURL string) int64 { return observer.EgressInFlight(account.ID, proxyURL) },
		func(proxyURL string) bool { return observer.EgressTripped(account.ID, proxyURL, 5) },
	)
}

// ProxyEgressStatsFor 返回账号的（代理×在途×最近状态）观测快照，供 admin
// API 的代理并发展示使用。冷启动（账号尚无请求）时对配置的每个出口端点
// 补一条 0 在途行，保证代理池配置完成后组件立即可见（对齐参考系统的
// 容量标签交互）；观测能力不可用且无池配置时返回空。
func (s *OpenAIGatewayService) ProxyEgressStatsFor(account *Account) []ProxyEgressStat {
	if account == nil {
		return nil
	}
	var stats []ProxyEgressStat
	if observer, ok := s.httpUpstream.(ProxyEgressObserver); ok && observer != nil {
		all := observer.EgressStats()
		stats = make([]ProxyEgressStat, 0, 4)
		for _, stat := range all {
			if stat.AccountID == account.ID {
				stats = append(stats, stat)
			}
		}
	}
	// 无观测能力时：账号配置了代理池（或主代理）仍返回 0 在途占位行，
	// 否则（直连且无观测）返回空保持旧行为。
	if observer, ok := s.httpUpstream.(ProxyEgressObserver); !ok || observer == nil {
		if len(account.ProxyEndpoints()) == 0 {
			return nil
		}
		stats = nil
	}
	seen := make(map[string]struct{}, len(stats))
	for _, stat := range stats {
		seen[stat.ProxyKey] = struct{}{}
	}
	for _, ep := range account.ProxyEndpoints() {
		dup := false
		if _, ok := seen[ep.URL]; ok {
			dup = true
		}
		if !dup {
			// 观测键是规范化地址（scheme://host[:port]），端点 URL 可能带认证
			// 信息或默认端口差异：按 host 粗匹配去重，避免同代理出现两行。
			for key := range seen {
				if SameEgressProxyKey(ep.URL, key) {
					dup = true
					break
				}
			}
		}
		if dup {
			continue
		}
		stats = append(stats, ProxyEgressStat{
			AccountID: account.ID,
			ProxyKey:  ep.URL,
			InFlight:  0,
			Recent:    []bool{},
		})
	}
	return stats
}

func accountIDOf(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

func concurrencyOf(account *Account) int {
	if account == nil {
		return 0
	}
	return account.Concurrency
}

// RewriteSynchronousImageURLs copies generated-image URLs into the gateway's
// object store and returns a client-facing response body.  It deliberately
// keeps failed copies as the original URL (best effort), so a transient cache
// outage cannot turn an otherwise successful generation into a 5xx response.
// The optional gin context is used only for request-id/log correlation.
func (s *OpenAIGatewayService) RewriteSynchronousImageURLs(
	ctx context.Context,
	c *gin.Context,
	body []byte,
	requestID string,
	scope string,
) []byte {
	if s == nil || s.syncMediaCache == nil || len(body) == 0 {
		return body
	}
	if ctx == nil && c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	// The upstream x-request-id and the client-supplied X-Request-ID are not
	// ownership-safe identifiers. Prefer the canonical gateway reservation ID,
	// which is generated by the auth middleware and shared with billing/logs;
	// when an older unauthenticated test path has no reservation, skip caching
	// rather than allowing user-controlled IDs to collide across responses.
	if canonicalID, ok := gatewaylite.CanonicalRequestIDFromContext(ctx); ok {
		requestID = canonicalID
	} else {
		requestID = ""
	}
	if strings.TrimSpace(requestID) == "" {
		// SyncMediaCache requires a stable non-empty key.  Returning unchanged
		// body is safer than writing an object under a guessed identifier.
		return body
	}
	rewritten, err := s.syncMediaCache.RewriteOutputURLs(ctx, body, requestID, scope)
	if err != nil {
		logger.LegacyPrintf("service.openai_gateway", "synchronous image URL cache partially failed (request_id=%s): %v", requestID, err)
	}
	return rewritten
}

// ResolveChannelMapping 解析渠道级模型映射（代理到 ChannelService）
func (s *OpenAIGatewayService) ResolveChannelMapping(ctx context.Context, groupID int64, model string) ChannelMappingResult {
	if s.channelService == nil {
		return ChannelMappingResult{MappedModel: model}
	}
	return s.channelService.ResolveChannelMapping(ctx, groupID, model)
}

// IsModelRestricted 检查模型是否被渠道限制（代理到 ChannelService）
func (s *OpenAIGatewayService) IsModelRestricted(ctx context.Context, groupID int64, model string) bool {
	if s.channelService == nil {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, groupID, model)
}

// ResolveChannelMappingAndRestrict 解析渠道映射。
// 模型限制检查已移至调度阶段，restricted 始终返回 false。
func (s *OpenAIGatewayService) ResolveChannelMappingAndRestrict(ctx context.Context, groupID *int64, model string) (ChannelMappingResult, bool) {
	if s.channelService == nil {
		return ChannelMappingResult{MappedModel: model}, false
	}
	return s.channelService.ResolveChannelMappingAndRestrict(ctx, groupID, model)
}

func (s *OpenAIGatewayService) isCodexImageGenerationBridgeEnabled(ctx context.Context, account *Account, apiKey *APIKey) bool {
	if override := account.CodexImageGenerationBridgeOverride(); override != nil {
		return *override
	}
	if s != nil && s.channelService != nil && apiKey != nil && apiKey.GroupID != nil {
		ch, err := s.channelService.GetChannelForGroup(ctx, *apiKey.GroupID)
		if err != nil {
			slog.Warn("failed to resolve codex image generation bridge channel override", "group_id", *apiKey.GroupID, "error", err)
		} else if override := ch.CodexImageGenerationBridgeOverride(PlatformOpenAI); override != nil {
			return *override
		}
	}
	return s != nil && s.cfg != nil && s.cfg.Gateway.CodexImageGenerationBridgeEnabled
}

func (s *OpenAIGatewayService) checkChannelPricingRestriction(ctx context.Context, groupID *int64, requestedModel string) bool {
	if groupID == nil || s.channelService == nil || requestedModel == "" {
		return false
	}
	mapping := s.channelService.ResolveChannelMapping(ctx, *groupID, requestedModel)
	billingModel := billingModelForRestriction(mapping.BillingModelSource, requestedModel, mapping.MappedModel)
	if billingModel == "" {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, *groupID, billingModel)
}

func (s *OpenAIGatewayService) isUpstreamModelRestrictedByChannel(ctx context.Context, groupID int64, account *Account, requestedModel string, requireCompact bool) bool {
	if s.channelService == nil {
		return false
	}
	if forwardModel, ok := openAIForwardModelFromContext(ctx); ok {
		requestedModel = forwardModel.model
		requireCompact = forwardModel.useCompactModelMapping
	}
	upstreamModel := resolveOpenAIAccountUpstreamModelForRequest(ctx, &groupID, account, requestedModel, requireCompact)
	if upstreamModel == "" {
		return false
	}
	return s.channelService.IsModelRestricted(ctx, groupID, upstreamModel)
}

func (s *OpenAIGatewayService) needsUpstreamChannelRestrictionCheck(ctx context.Context, groupID *int64) bool {
	if groupID == nil || s.channelService == nil {
		return false
	}
	ch, err := s.channelService.GetChannelForGroup(ctx, *groupID)
	if err != nil {
		slog.Warn("failed to check openai channel upstream restriction", "group_id", *groupID, "error", err)
		return false
	}
	if ch == nil || !ch.RestrictModels {
		return false
	}
	source := ch.BillingModelSource
	if group := finalMappingGroupFromContext(ctx, *groupID); group != nil {
		if groupSource := normalizeGroupBillingModelSource(group.BillingModelSource); groupSource != "" {
			source = groupSource
		}
	}
	return source == BillingModelSourceUpstream
}

// ReplaceModelInBody 替换请求体中的 JSON model 字段（通用 gjson/sjson 实现）。
func (s *OpenAIGatewayService) ReplaceModelInBody(body []byte, newModel string) []byte {
	return ReplaceModelInBody(body, newModel)
}

func (s *OpenAIGatewayService) getCodexSnapshotThrottle() *accountWriteThrottle {
	if s != nil && s.codexSnapshotThrottle != nil {
		return s.codexSnapshotThrottle
	}
	return defaultOpenAICodexSnapshotPersistThrottle
}

func (s *OpenAIGatewayService) billingDeps() *billingDeps {
	return &billingDeps{
		accountRepo:           s.accountRepo,
		userRepo:              s.userRepo,
		userSubRepo:           s.userSubRepo,
		billingCacheService:   s.billingCacheService,
		deferredService:       s.deferredService,
		balanceNotifyService:  s.balanceNotifyService,
		userPlatformQuotaRepo: s.userPlatformQuotaRepo,
	}
}

// CloseOpenAIWSPool 关闭 OpenAI WebSocket 连接池的后台 worker 和空闲连接。
// 应在应用优雅关闭时调用。
func (s *OpenAIGatewayService) CloseOpenAIWSPool() {
	if s != nil && s.openaiWSPool != nil {
		s.openaiWSPool.Close()
	}
}

func (s *OpenAIGatewayService) InvalidateAgentIdentityWSConnections(accountID int64) {
	if pool := s.getOpenAIWSConnPool(); pool != nil {
		pool.ClearAccount(accountID)
	}
}

func (s *OpenAIGatewayService) logOpenAIWSModeBootstrap() {
	if s == nil || s.cfg == nil {
		return
	}
	wsCfg := s.cfg.Gateway.OpenAIWS
	logOpenAIWSModeInfo(
		"bootstrap enabled=%v oauth_enabled=%v apikey_enabled=%v force_http=%v responses_websockets_v2=%v responses_websockets=%v payload_log_sample_rate=%.3f event_flush_batch_size=%d event_flush_interval_ms=%d prewarm_cooldown_ms=%d retry_backoff_initial_ms=%d retry_backoff_max_ms=%d retry_jitter_ratio=%.3f retry_total_budget_ms=%d ws_read_limit_bytes=%d",
		wsCfg.Enabled,
		wsCfg.OAuthEnabled,
		wsCfg.APIKeyEnabled,
		wsCfg.ForceHTTP,
		wsCfg.ResponsesWebsocketsV2,
		wsCfg.ResponsesWebsockets,
		wsCfg.PayloadLogSampleRate,
		wsCfg.EventFlushBatchSize,
		wsCfg.EventFlushIntervalMS,
		wsCfg.PrewarmCooldownMS,
		wsCfg.RetryBackoffInitialMS,
		wsCfg.RetryBackoffMaxMS,
		wsCfg.RetryJitterRatio,
		wsCfg.RetryTotalBudgetMS,
		openAIWSMessageReadLimitBytes,
	)
}

func (s *OpenAIGatewayService) getCodexClientRestrictionDetector() CodexClientRestrictionDetector {
	if s != nil && s.codexDetector != nil {
		return s.codexDetector
	}
	var cfg *config.Config
	if s != nil {
		cfg = s.cfg
	}
	return NewOpenAICodexClientRestrictionDetector(cfg)
}

func (s *OpenAIGatewayService) getOpenAIWSProtocolResolver() OpenAIWSProtocolResolver {
	if s != nil && s.openaiWSResolver != nil {
		return s.openaiWSResolver
	}
	var cfg *config.Config
	if s != nil {
		cfg = s.cfg
	}
	return NewOpenAIWSProtocolResolver(cfg)
}

// openAIHTTPClientWSBridgeEnabled 读取全局「HTTP 客户端 WS 桥接」开关。
// 默认关闭：HTTP/SSE 客户端保持走 HTTP 上游；开启后按账号 WS 开关放行 WS 上游，
// 由网关做 HTTP -> WS 协议转换，WS 失败时回落同账号 HTTP 保证可用性。
func (s *OpenAIGatewayService) openAIHTTPClientWSBridgeEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.HTTPClientWSBridgeEnabled
}

// shouldOpenAIWSBridgeFallbackToHTTP 判断 HTTP 桥接模式下 WS 失败后能否回落同账号 HTTP。
// 仅当开关开启、客户端为 HTTP 入站、WS 确实出错，且尚未向客户端写出任何字节时允许：
// 一旦写出流式字节就无法安全重放原始请求，只能按原错误返回（保持原行为）。
func (s *OpenAIGatewayService) shouldOpenAIWSBridgeFallbackToHTTP(c *gin.Context, clientTransport OpenAIClientTransport, wsErr error) bool {
	if !s.openAIHTTPClientWSBridgeEnabled() {
		return false
	}
	// 原生 WS 客户端失败仍按原行为返回错误，不悄悄换协议。
	if clientTransport != OpenAIClientTransportHTTP || wsErr == nil {
		return false
	}
	if c == nil || c.Writer == nil {
		return false
	}
	return !c.Writer.Written()
}

func classifyOpenAIWSReconnectReason(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var fallbackErr *openAIWSFallbackError
	if !errors.As(err, &fallbackErr) || fallbackErr == nil {
		return "", false
	}
	reason := strings.TrimSpace(fallbackErr.Reason)
	if reason == "" {
		return "", false
	}

	baseReason := strings.TrimPrefix(reason, "prewarm_")

	switch baseReason {
	case "policy_violation",
		"message_too_big",
		"upgrade_required",
		"ws_unsupported",
		"auth_failed",
		"service_tier_unsupported",
		"invalid_encrypted_content",
		"previous_response_not_found":
		return reason, false
	}

	switch baseReason {
	case "read_event",
		"write_request",
		"write",
		"acquire_timeout",
		"acquire_conn",
		"conn_queue_full",
		"dial_failed",
		"upstream_5xx",
		"event_error",
		"error_event",
		"upstream_error_event",
		"ws_connection_limit_reached",
		"missing_final_response":
		return reason, true
	default:
		return reason, false
	}
}

func resolveOpenAIWSFallbackErrorResponse(err error) (statusCode int, errType string, clientMessage string, upstreamMessage string, ok bool) {
	if err == nil {
		return 0, "", "", "", false
	}
	var fallbackErr *openAIWSFallbackError
	if !errors.As(err, &fallbackErr) || fallbackErr == nil {
		return 0, "", "", "", false
	}

	reason := strings.TrimSpace(fallbackErr.Reason)
	reason = strings.TrimPrefix(reason, "prewarm_")
	if reason == "" {
		return 0, "", "", "", false
	}

	var dialErr *openAIWSDialError
	if fallbackErr.Err != nil && errors.As(fallbackErr.Err, &dialErr) && dialErr != nil {
		if dialErr.StatusCode > 0 {
			statusCode = dialErr.StatusCode
		}
		if dialErr.Err != nil {
			upstreamMessage = sanitizeUpstreamErrorMessage(strings.TrimSpace(dialErr.Err.Error()))
		}
	}

	switch reason {
	case "invalid_encrypted_content":
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
		errType = "invalid_request_error"
		if upstreamMessage == "" {
			upstreamMessage = "encrypted content could not be verified"
		}
	case "previous_response_not_found":
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
		errType = "invalid_request_error"
		if upstreamMessage == "" {
			upstreamMessage = "previous response not found"
		}
	case "upgrade_required":
		if statusCode == 0 {
			statusCode = http.StatusUpgradeRequired
		}
	case "ws_unsupported":
		if statusCode == 0 {
			statusCode = http.StatusBadRequest
		}
	case "auth_failed":
		if statusCode == 0 {
			statusCode = http.StatusUnauthorized
		}
	case "upstream_rate_limited":
		if statusCode == 0 {
			statusCode = http.StatusTooManyRequests
		}
	default:
		if statusCode == 0 {
			return 0, "", "", "", false
		}
	}

	if upstreamMessage == "" && fallbackErr.Err != nil {
		upstreamMessage = sanitizeUpstreamErrorMessage(strings.TrimSpace(fallbackErr.Err.Error()))
	}
	if upstreamMessage == "" {
		switch reason {
		case "upgrade_required":
			upstreamMessage = "upstream websocket upgrade required"
		case "ws_unsupported":
			upstreamMessage = "upstream websocket not supported"
		case "auth_failed":
			upstreamMessage = "upstream authentication failed"
		case "upstream_rate_limited":
			upstreamMessage = "upstream rate limit exceeded, please retry later"
		default:
			upstreamMessage = "Upstream request failed"
		}
	}

	if errType == "" {
		if statusCode == http.StatusTooManyRequests {
			errType = "rate_limit_error"
		} else {
			errType = "upstream_error"
		}
	}
	clientMessage = upstreamMessage
	return statusCode, errType, clientMessage, upstreamMessage, true
}

func (s *OpenAIGatewayService) writeOpenAIWSFallbackErrorResponse(c *gin.Context, account *Account, wsErr error) bool {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return false
	}
	statusCode, errType, clientMessage, upstreamMessage, ok := resolveOpenAIWSFallbackErrorResponse(wsErr)
	if !ok {
		return false
	}
	if strings.TrimSpace(clientMessage) == "" {
		clientMessage = "Upstream request failed"
	}
	if strings.TrimSpace(upstreamMessage) == "" {
		upstreamMessage = clientMessage
	}

	setOpsUpstreamError(c, statusCode, upstreamMessage, "")
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			ProxyHost:          proxyHostForAccount(account),
			UpstreamStatusCode: statusCode,
			Kind:               "ws_error",
			Message:            upstreamMessage,
		})
	}
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": clientMessage,
		},
	})
	return true
}

func (s *OpenAIGatewayService) openAIWSRetryBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}

	initial := openAIWSRetryBackoffInitialDefault
	maxBackoff := openAIWSRetryBackoffMaxDefault
	jitterRatio := openAIWSRetryJitterRatioDefault
	if s != nil && s.cfg != nil {
		wsCfg := s.cfg.Gateway.OpenAIWS
		if wsCfg.RetryBackoffInitialMS > 0 {
			initial = time.Duration(wsCfg.RetryBackoffInitialMS) * time.Millisecond
		}
		if wsCfg.RetryBackoffMaxMS > 0 {
			maxBackoff = time.Duration(wsCfg.RetryBackoffMaxMS) * time.Millisecond
		}
		if wsCfg.RetryJitterRatio >= 0 {
			jitterRatio = wsCfg.RetryJitterRatio
		}
	}
	if initial <= 0 {
		return 0
	}
	if maxBackoff <= 0 {
		maxBackoff = initial
	}
	if maxBackoff < initial {
		maxBackoff = initial
	}
	if jitterRatio < 0 {
		jitterRatio = 0
	}
	if jitterRatio > 1 {
		jitterRatio = 1
	}

	shift := attempt - 1
	if shift < 0 {
		shift = 0
	}
	backoff := initial
	if shift > 0 {
		backoff = initial * time.Duration(1<<shift)
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	if jitterRatio <= 0 {
		return backoff
	}
	jitter := time.Duration(float64(backoff) * jitterRatio)
	if jitter <= 0 {
		return backoff
	}
	delta := time.Duration(mathrand.Int63n(int64(jitter)*2+1)) - jitter
	withJitter := backoff + delta
	if withJitter < 0 {
		return 0
	}
	return withJitter
}

func (s *OpenAIGatewayService) openAIWSRetryTotalBudget() time.Duration {
	if s != nil && s.cfg != nil {
		ms := s.cfg.Gateway.OpenAIWS.RetryTotalBudgetMS
		if ms <= 0 {
			return 0
		}
		return time.Duration(ms) * time.Millisecond
	}
	return 0
}

func (s *OpenAIGatewayService) recordOpenAIWSRetryAttempt(backoff time.Duration) {
	if s == nil {
		return
	}
	s.openaiWSRetryMetrics.retryAttempts.Add(1)
	if backoff > 0 {
		s.openaiWSRetryMetrics.retryBackoffMs.Add(backoff.Milliseconds())
	}
}

func (s *OpenAIGatewayService) recordOpenAIWSRetryExhausted() {
	if s == nil {
		return
	}
	s.openaiWSRetryMetrics.retryExhausted.Add(1)
}

func (s *OpenAIGatewayService) recordOpenAIWSNonRetryableFastFallback() {
	if s == nil {
		return
	}
	s.openaiWSRetryMetrics.nonRetryableFastFallback.Add(1)
}

func (s *OpenAIGatewayService) SnapshotOpenAIWSRetryMetrics() OpenAIWSRetryMetricsSnapshot {
	if s == nil {
		return OpenAIWSRetryMetricsSnapshot{}
	}
	return OpenAIWSRetryMetricsSnapshot{
		RetryAttemptsTotal:            s.openaiWSRetryMetrics.retryAttempts.Load(),
		RetryBackoffMsTotal:           s.openaiWSRetryMetrics.retryBackoffMs.Load(),
		RetryExhaustedTotal:           s.openaiWSRetryMetrics.retryExhausted.Load(),
		NonRetryableFastFallbackTotal: s.openaiWSRetryMetrics.nonRetryableFastFallback.Load(),
	}
}

func SnapshotOpenAICompatibilityFallbackMetrics() OpenAICompatibilityFallbackMetricsSnapshot {
	legacyReadFallbackTotal, legacyReadFallbackHit, legacyDualWriteTotal := openAIStickyCompatStats()
	isMaxTokensOneHaiku, thinkingEnabled, prefetchedStickyAccount, prefetchedStickyGroup, singleAccountRetry, accountSwitchCount := RequestMetadataFallbackStats()

	readHitRate := float64(0)
	if legacyReadFallbackTotal > 0 {
		readHitRate = float64(legacyReadFallbackHit) / float64(legacyReadFallbackTotal)
	}
	metadataFallbackTotal := isMaxTokensOneHaiku + thinkingEnabled + prefetchedStickyAccount + prefetchedStickyGroup + singleAccountRetry + accountSwitchCount

	return OpenAICompatibilityFallbackMetricsSnapshot{
		SessionHashLegacyReadFallbackTotal: legacyReadFallbackTotal,
		SessionHashLegacyReadFallbackHit:   legacyReadFallbackHit,
		SessionHashLegacyDualWriteTotal:    legacyDualWriteTotal,
		SessionHashLegacyReadHitRate:       readHitRate,

		MetadataLegacyFallbackIsMaxTokensOneHaikuTotal: isMaxTokensOneHaiku,
		MetadataLegacyFallbackThinkingEnabledTotal:     thinkingEnabled,
		MetadataLegacyFallbackPrefetchedStickyAccount:  prefetchedStickyAccount,
		MetadataLegacyFallbackPrefetchedStickyGroup:    prefetchedStickyGroup,
		MetadataLegacyFallbackSingleAccountRetryTotal:  singleAccountRetry,
		MetadataLegacyFallbackAccountSwitchCountTotal:  accountSwitchCount,
		MetadataLegacyFallbackTotal:                    metadataFallbackTotal,
	}
}

func (s *OpenAIGatewayService) detectCodexClientRestriction(c *gin.Context, account *Account) CodexClientRestrictionDetectionResult {
	var globalAllowedClients []string
	if account != nil && account.IsCodexCLIOnlyEnabled() && s != nil && s.settingService != nil {
		ctx := context.Background()
		if c != nil && c.Request != nil {
			ctx = c.Request.Context()
		}
		if s.settingService.IsOpenAIAllowClaudeCodeCodexPluginEnabled(ctx) {
			globalAllowedClients = []string{openai.AllowedClientClaudeCode}
		}
	}
	return s.getCodexClientRestrictionDetector().Detect(c, account, globalAllowedClients)
}

func getAPIKeyIDFromContext(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	v, exists := c.Get("api_key")
	if !exists {
		return 0
	}
	apiKey, ok := v.(*APIKey)
	if !ok || apiKey == nil {
		return 0
	}
	return apiKey.ID
}

// isolateOpenAISessionID 将 apiKeyID 混入 session 标识符，
// 确保不同 API Key 的用户即使使用相同的原始 session_id/conversation_id，
// 到达上游的标识符也不同，防止跨用户会话碰撞。
func isolateOpenAISessionID(apiKeyID int64, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	h := xxhash.New()
	_, _ = fmt.Fprintf(h, "k%d:", apiKeyID)
	_, _ = h.WriteString(raw)
	return fmt.Sprintf("%016x", h.Sum64())
}

func logCodexCLIOnlyDetection(ctx context.Context, c *gin.Context, account *Account, apiKeyID int64, result CodexClientRestrictionDetectionResult, body []byte) {
	if !result.Enabled {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.Bool("codex_cli_only_enabled", result.Enabled),
		zap.Bool("codex_official_client_match", result.Matched),
		zap.String("reject_reason", result.Reason),
	}
	if apiKeyID > 0 {
		fields = append(fields, zap.Int64("api_key_id", apiKeyID))
	}
	if !result.Matched {
		fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, body)
	}
	log := logger.FromContext(ctx).With(fields...)
	if result.Matched {
		log.Info("OpenAI codex_cli_only 放行请求")
		return
	}
	log.Warn("OpenAI codex_cli_only 拒绝非官方客户端请求")
}

func appendCodexCLIOnlyRejectedRequestFields(fields []zap.Field, c *gin.Context, body []byte) []zap.Field {
	if c == nil || c.Request == nil {
		return fields
	}

	req := c.Request
	requestModel, requestStream, promptCacheKey := extractOpenAIRequestMetaFromBody(body)
	fields = append(fields,
		zap.String("request_method", strings.TrimSpace(req.Method)),
		zap.String("request_path", strings.TrimSpace(req.URL.Path)),
		zap.String("request_query", strings.TrimSpace(req.URL.RawQuery)),
		zap.String("request_host", strings.TrimSpace(req.Host)),
		zap.String("request_client_ip", strings.TrimSpace(ip.GetClientIP(c))),
		zap.String("request_remote_addr", strings.TrimSpace(req.RemoteAddr)),
		zap.String("request_user_agent", strings.TrimSpace(req.Header.Get("User-Agent"))),
		zap.String("request_content_type", strings.TrimSpace(req.Header.Get("Content-Type"))),
		zap.Int64("request_content_length", req.ContentLength),
		zap.Bool("request_stream", requestStream),
	)
	if requestModel != "" {
		fields = append(fields, zap.String("request_model", requestModel))
	}
	if promptCacheKey != "" {
		fields = append(fields, zap.String("request_prompt_cache_key_sha256", hashSensitiveValueForLog(promptCacheKey)))
	}

	if headers := snapshotCodexCLIOnlyHeaders(req.Header); len(headers) > 0 {
		fields = append(fields, zap.Any("request_headers", headers))
	}
	fields = append(fields, zap.Int("request_body_size", len(body)))
	return fields
}

func snapshotCodexCLIOnlyHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	result := make(map[string]string, len(codexCLIOnlyDebugHeaderWhitelist))
	for _, key := range codexCLIOnlyDebugHeaderWhitelist {
		value := strings.TrimSpace(header.Get(key))
		if value == "" {
			continue
		}
		result[strings.ToLower(key)] = truncateString(value, codexCLIOnlyHeaderValueMaxBytes)
	}
	return result
}

func hashSensitiveValueForLog(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func logOpenAIInstructionsRequiredDebug(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	upstreamStatusCode int,
	upstreamMsg string,
	requestBody []byte,
	upstreamBody []byte,
) {
	msg := strings.TrimSpace(upstreamMsg)
	if !isOpenAIInstructionsRequiredError(upstreamStatusCode, msg, upstreamBody) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	accountID := int64(0)
	accountName := ""
	if account != nil {
		accountID = account.ID
		accountName = strings.TrimSpace(account.Name)
	}

	userAgent := ""
	originator := ""
	if c != nil {
		userAgent = strings.TrimSpace(c.GetHeader("User-Agent"))
		originator = strings.TrimSpace(c.GetHeader("originator"))
	}

	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.String("account_name", accountName),
		zap.Int("upstream_status_code", upstreamStatusCode),
		zap.String("upstream_error_message", msg),
		zap.String("request_user_agent", userAgent),
		zap.Bool("codex_official_client_match", openai.IsCodexOfficialClientByHeaders(userAgent, originator)),
	}
	fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, requestBody)

	logger.FromContext(ctx).With(fields...).Warn("OpenAI 上游返回 Instructions are required，已记录请求详情用于排查")
}

func isOpenAIInstructionsRequiredError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode != http.StatusBadRequest {
		return false
	}

	hasInstructionRequired := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "instructions are required") {
			return true
		}
		if strings.Contains(lower, "required parameter: 'instructions'") {
			return true
		}
		if strings.Contains(lower, "required parameter: instructions") {
			return true
		}
		if strings.Contains(lower, "missing required parameter") && strings.Contains(lower, "instructions") {
			return true
		}
		return strings.Contains(lower, "instruction") && strings.Contains(lower, "required")
	}

	if hasInstructionRequired(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}

	errMsg := gjson.GetBytes(upstreamBody, "error.message").String()
	errMsgLower := strings.ToLower(strings.TrimSpace(errMsg))
	errCode := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.code").String()))
	errParam := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.param").String()))
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(upstreamBody, "error.type").String()))

	if errParam == "instructions" {
		return true
	}
	if hasInstructionRequired(errMsg) {
		return true
	}
	if strings.Contains(errCode, "missing_required_parameter") && strings.Contains(errMsgLower, "instructions") {
		return true
	}
	if strings.Contains(errType, "invalid_request") && strings.Contains(errMsgLower, "instructions") && strings.Contains(errMsgLower, "required") {
		return true
	}

	return false
}

func isOpenAITransientProcessingError(upstreamStatusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if upstreamStatusCode < http.StatusBadRequest {
		return false
	}

	hasOpenAIServerOverloadedCode := func(payload []byte) bool {
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
		if code == "" {
			code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
		}
		return code == "server_is_overloaded" || code == "slow_down"
	}

	if len(upstreamBody) > 0 && hasOpenAIServerOverloadedCode(upstreamBody) {
		return true
	}
	if isOpenAICapacityShedMessage(upstreamMsg) ||
		isOpenAICapacityShedMessage(gjson.GetBytes(upstreamBody, "error.message").String()) ||
		isOpenAICapacityShedMessage(gjson.GetBytes(upstreamBody, "response.error.message").String()) ||
		isOpenAICapacityShedMessage(string(upstreamBody)) {
		return true
	}
	if upstreamStatusCode != http.StatusBadRequest && upstreamStatusCode != http.StatusServiceUnavailable {
		return false
	}
	if upstreamStatusCode != http.StatusBadRequest {
		return false
	}

	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "an error occurred while processing your request") {
			return true
		}
		if strings.Contains(lower, "selected model is at capacity") {
			return true
		}
		return strings.Contains(lower, "you can retry your request") &&
			strings.Contains(lower, "help.openai.com") &&
			strings.Contains(lower, "request id")
	}

	if match(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}
	if match(gjson.GetBytes(upstreamBody, "error.message").String()) {
		return true
	}
	return match(string(upstreamBody))
}

func isOpenAICapacityShedMessage(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "server is overloaded") ||
		strings.Contains(lower, "servers are overloaded") ||
		strings.Contains(lower, "servers are currently overloaded")
}

func isOpenAIRequestScopedCapacityShed(upstreamMsg string, upstreamBody []byte) bool {
	return isOpenAIUpstreamCapacityShedEvent(upstreamBody) ||
		isOpenAICapacityShedMessage(upstreamMsg) ||
		isOpenAICapacityShedMessage(string(upstreamBody))
}

func openAIUpstreamCapacityShedCode(payload []byte) string {
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	}
	return code
}

func isOpenAIUpstreamCapacityShedResponse(statusCode int, payload []byte) bool {
	if statusCode < http.StatusBadRequest {
		return false
	}
	return isOpenAIRequestScopedCapacityShed("", payload)
}

func isOpenAIContextWindowError(upstreamMsg string, upstreamBody []byte) bool {
	match := func(text string) bool {
		lower := strings.ToLower(strings.TrimSpace(text))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "context_too_large") || strings.Contains(lower, "context_length_exceeded") {
			return true
		}
		if strings.Contains(lower, "maximum context length") || strings.Contains(lower, "max context length") {
			return true
		}
		hasExceeded := strings.Contains(lower, "exceed") || strings.Contains(lower, "too large") || strings.Contains(lower, "too long")
		if strings.Contains(lower, "context window") && hasExceeded {
			return true
		}
		if strings.Contains(lower, "context length") && hasExceeded {
			return true
		}
		return strings.Contains(lower, "token limit") &&
			strings.Contains(lower, "context") &&
			hasExceeded
	}

	if match(upstreamMsg) {
		return true
	}
	if len(upstreamBody) == 0 {
		return false
	}
	for _, path := range []string{
		"error.message",
		"response.error.message",
		"message",
		"error.code",
		"response.error.code",
		"code",
	} {
		if match(gjson.GetBytes(upstreamBody, path).String()) {
			return true
		}
	}
	return match(string(upstreamBody))
}

const (
	openCodeSessionAffinityHeader = "X-Session-Affinity"
	openCodeSessionIDHeader       = "X-Session-Id"
	openCodeNativeSessionHeader   = "X-OpenCode-Session"
	codeBuddyConversationHeader   = "X-Conversation-ID"
)

// explicitOpenAIHeaderSessionID 只采纳会话级标识；请求 ID 每轮都会变化，
// 若误用会破坏 sticky（粘性调度）和上游提示词缓存。
func explicitOpenAIHeaderSessionID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	for _, header := range explicitOpenAIHeaderSessionNames {
		if sessionID := strings.TrimSpace(c.GetHeader(header)); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// ExtractSessionID extracts the raw session ID from headers or body without hashing.
// Used by ForwardAsAnthropic to pass as prompt_cache_key for upstream cache.
func (s *OpenAIGatewayService) ExtractSessionID(c *gin.Context, body []byte) string {
	return explicitOpenAIRequestSessionID(c, body)
}

func explicitOpenAISessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := explicitOpenAIHeaderSessionID(c)
	if sessionID == "" && len(body) > 0 {
		sessionID = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	return sessionID
}

// explicitOpenAIRequestSessionID accepts Grok's native conversation header
// only when the authenticated API key is bound to a Grok group. This prevents
// unrelated headers from changing OpenAI-group scheduling behavior.
func explicitOpenAIRequestSessionID(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := explicitOpenAIHeaderSessionID(c)
	if sessionID == "" && isGrokRequestContext(c) {
		sessionID = strings.TrimSpace(c.GetHeader(grokConversationIDHeader))
	}
	if sessionID == "" && len(body) > 0 {
		sessionID = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	return sessionID
}

// GenerateExplicitSessionHash generates a sticky-session hash only from explicit
// client session signals. It intentionally skips content-derived fallback and is
// used by stateless endpoints such as /v1/images.
func (s *OpenAIGatewayService) GenerateExplicitSessionHash(c *gin.Context, body []byte) string {
	sessionID := explicitOpenAIRequestSessionID(c, body)
	if sessionID == "" {
		return ""
	}

	currentHash, legacyHash := deriveOpenAISessionHashes(sessionID)
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	return currentHash
}

// GenerateSessionHash generates a sticky-session hash for OpenAI requests.
//
// Priority:
//  1. Header: session-id / session_id
//  2. Header: conversation_id
//  3. Body:   prompt_cache_key (opencode)
//  4. Body:   content-based fallback (model + system + tools + first user message)
func (s *OpenAIGatewayService) GenerateSessionHash(c *gin.Context, body []byte) string {
	if c == nil {
		return ""
	}

	sessionID := explicitOpenAIRequestSessionID(c, body)
	if sessionID == "" && len(body) > 0 {
		sessionID = deriveOpenAIContentSessionSeed(body)
	}
	if sessionID == "" {
		return ""
	}

	currentHash, legacyHash := deriveOpenAISessionHashes(sessionID)
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	return currentHash
}

// GenerateSessionHashWithFallback 先按常规信号生成会话哈希；
// 当未携带 session_id/conversation_id/prompt_cache_key 时，使用 fallbackSeed 生成稳定哈希。
// 该方法用于 WS ingress，避免会话信号缺失时发生跨账号漂移。
func (s *OpenAIGatewayService) GenerateSessionHashWithFallback(c *gin.Context, body []byte, fallbackSeed string) string {
	sessionHash := s.GenerateSessionHash(c, body)
	if sessionHash != "" {
		return sessionHash
	}

	seed := strings.TrimSpace(fallbackSeed)
	if seed == "" {
		return ""
	}

	currentHash, legacyHash := deriveOpenAISessionHashes(seed)
	attachOpenAILegacySessionHashToGin(c, legacyHash)
	return currentHash
}

func resolveOpenAIUpstreamOriginator(c *gin.Context, isOfficialClient bool) string {
	if c != nil {
		if originator := strings.TrimSpace(c.GetHeader("originator")); originator != "" {
			return originator
		}
	}
	if isOfficialClient {
		return "codex_cli_rs"
	}
	return "opencode"
}

// BindStickySession sets session -> account binding with standard TTL.
func (s *OpenAIGatewayService) BindStickySession(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if sessionHash == "" || accountID <= 0 {
		return nil
	}
	ttl := openaiStickySessionTTL
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		ttl = time.Duration(s.cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	return s.setStickySessionAccountID(ctx, groupID, sessionHash, accountID, ttl)
}

// SelectAccount selects an OpenAI account with sticky session support
func (s *OpenAIGatewayService) SelectAccount(ctx context.Context, groupID *int64, sessionHash string) (*Account, error) {
	return s.SelectAccountForModel(ctx, groupID, sessionHash, "")
}

// SelectAccountForModel selects an account supporting the requested model
func (s *OpenAIGatewayService) SelectAccountForModel(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*Account, error) {
	return s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, nil)
}

// SelectAccountForModelWithExclusions selects an account supporting the requested model while excluding specified accounts.
// SelectAccountForModelWithExclusions 选择支持指定模型的账号，同时排除指定的账号。
func (s *OpenAIGatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	return s.selectAccountForModelWithExclusions(s.withOpenAIQuotaAutoPauseContext(ctx), groupID, sessionHash, requestedModel, excludedIDs, false, 0, "", PlatformOpenAI)
}

// noAvailableOpenAISelectionError builds the standard "no account available" error
// while preserving the compact-specific error when applicable.
func normalizeOpenAICompatiblePlatform(platform string) string {
	if strings.TrimSpace(platform) == PlatformGrok {
		return PlatformGrok
	}
	return PlatformOpenAI
}

func noAvailableOpenAISelectionError(requestedModel string, compactBlocked bool, details string) error {
	if compactBlocked {
		return ErrNoAvailableCompactAccounts
	}
	message := "no available OpenAI accounts"
	if requestedModel != "" {
		message = fmt.Sprintf("no available OpenAI accounts supporting model: %s", requestedModel)
	}
	// details 仅进入服务端日志诊断，HTTP（接口）层仍向用户返回通用错误，
	// 避免泄露账号池和配额内部状态。
	if details != "" {
		message += " (" + details + ")"
	}
	return openAINoAvailableSelectionError{message: message}
}

type openAINoAvailableSelectionError struct {
	message string
}

func (e openAINoAvailableSelectionError) Error() string {
	return e.message
}

func (e openAINoAvailableSelectionError) Unwrap() error {
	return ErrNoAvailableAccounts
}

func wrapGatewayGroupUnavailableSelectionError(ctx context.Context, groupID *int64, err error) error {
	if err == nil || groupID == nil || *groupID <= 0 {
		return wrapOpenAILongContextSelectionError(ctx, err)
	}
	if errors.Is(err, ErrGatewayGroupUnavailable) || errors.Is(err, ErrNoAvailableCompactAccounts) {
		return err
	}
	err = wrapOpenAILongContextSelectionError(ctx, err)
	var longContextErr *OpenAILongContextRoutingError
	if errors.As(err, &longContextErr) {
		return err
	}
	if errors.Is(err, ErrNoAvailableAccounts) || isOpenAINoAvailableSelectionMessage(err) {
		return &GatewayGroupUnavailableError{
			GroupID: *groupID,
			Cause:   err,
		}
	}
	return err
}

func (s *OpenAIGatewayService) validateGatewayLiteFixedOpenAIGroup(ctx context.Context, groupID *int64) error {
	if s == nil || groupID == nil || *groupID <= 0 || s.schedulerSnapshot == nil || !s.schedulerSnapshot.isRunModeGatewayLite() {
		return nil
	}
	group, err := s.getOpenAISelectionGroup(ctx, *groupID)
	if err != nil {
		return err
	}
	// gateway-lite 本地分组才是真实调度权威；固定分组缺失或禁用时必须明确拒绝，
	// 不能继续退化成普通全局调度，否则被删除/关闭的分组绑定 Key 仍可能误走账号。
	if group == nil {
		return &GatewayGroupUnavailableError{
			GroupID: *groupID,
			Cause:   fmt.Errorf("group not found"),
		}
	}
	if !group.IsActive() {
		return &GatewayGroupUnavailableError{
			GroupID: *groupID,
			Cause:   fmt.Errorf("group status is %s", group.Status),
		}
	}
	return nil
}

func isOpenAINoAvailableSelectionMessage(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "no available openai accounts") ||
		strings.Contains(msg, "no available accounts")
}

// openAICompactSupportTier classifies an OpenAI account by compact capability.
// 0 = explicitly unsupported, 1 = unknown / not yet probed, 2 = explicitly supported.
func openAICompactSupportTier(account *Account) int {
	if account == nil {
		return 0
	}
	// Grok 由轻量网关把 compact（上下文压缩）转换为普通 Responses 总结轮次，
	// 因此不依赖上游原生 compact 探测结果。
	if account.IsGrok() {
		return 2
	}
	if !account.IsOpenAI() {
		return 0
	}
	supported, known := account.OpenAICompactSupportKnown()
	if !known {
		return 1
	}
	if supported {
		return 2
	}
	return 0
}

func isOpenAIOAuthCompatibleModelName(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	normalized = strings.TrimPrefix(normalized, "openai/")
	if normalized == "" {
		return true
	}
	for _, item := range openai.DefaultModels {
		if strings.EqualFold(item.ID, normalized) {
			return true
		}
	}
	return strings.HasPrefix(normalized, "gpt-") ||
		strings.HasPrefix(normalized, "gpt") ||
		strings.HasPrefix(normalized, "codex") ||
		strings.HasPrefix(normalized, "o1") ||
		strings.HasPrefix(normalized, "o3") ||
		strings.HasPrefix(normalized, "o4")
}

func isOpenAIAccountModelEligible(account *Account, requestedModel string) bool {
	if account == nil {
		return false
	}
	if !account.IsOpenAIOAuth() {
		return account.IsModelSupported(requestedModel)
	}
	if strings.TrimSpace(requestedModel) == "" {
		return true
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return isOpenAIOAuthCompatibleModelName(requestedModel)
	}
	mappedModel, matched := account.ResolveMappedModel(requestedModel)
	if !matched {
		return false
	}
	// OAuth/Codex 账号只能打到 ChatGPT/OpenAI 自身支持的模型。允许管理员
	// 显式把别名映射到 gpt/codex/o 系模型，但不能把第三方模型透传给它。
	return isOpenAIOAuthCompatibleModelName(mappedModel)
}

// isOpenAIAccountModelEligibleForGroup keeps candidate filtering aligned with
// account-group mappings. A dedicated rule is an explicit declaration for the
// selected group and therefore takes precedence over the account-wide map;
// unbound/legacy requests retain the exact former eligibility behaviour.
func isOpenAIAccountModelEligibleForGroup(account *Account, groupID *int64, requestedModel string) bool {
	if groupID == nil || *groupID <= 0 {
		return isOpenAIAccountModelEligible(account, requestedModel)
	}
	if account == nil {
		return false
	}
	if !account.IsOpenAIOAuth() {
		return account.IsModelSupportedForGroupID(groupID, requestedModel)
	}
	if strings.TrimSpace(requestedModel) == "" {
		return true
	}
	if !account.IsModelSupportedForGroupID(groupID, requestedModel) {
		return false
	}
	mappedModel, matched := account.ResolveModelForGroupID(groupID, requestedModel)
	if !matched {
		// Empty account-wide mappings deliberately keep OAuth compatibility
		// permissive for OpenAI aliases. This is the pre-existing behaviour.
		return len(account.GetModelMapping()) == 0 && isOpenAIOAuthCompatibleModelName(requestedModel)
	}
	return isOpenAIOAuthCompatibleModelName(mappedModel)
}

var openAIUndefinedSentinelTopLevelFields = map[string]struct{}{
	"conversation":           {},
	"instructions":           {},
	"max_output_tokens":      {},
	"max_tool_calls":         {},
	"metadata":               {},
	"parallel_tool_calls":    {},
	"previous_response_id":   {},
	"prompt_cache_key":       {},
	"prompt_cache_retention": {},
	"safety_identifier":      {},
	"service_tier":           {},
	"temperature":            {},
	"tool_choice":            {},
	"tools":                  {},
	"top_logprobs":           {},
	"top_p":                  {},
	"truncation":             {},
	"user":                   {},
}

func isOpenAIUndefinedSentinelString(value any) bool {
	str, ok := value.(string)
	if !ok {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(str))
	return normalized == "[undefined]" || normalized == "undefined"
}

func SanitizeOpenAIUndefinedSentinelRequestBody(body []byte) ([]byte, bool, error) {
	if !bytes.Contains(body, []byte("undefined")) {
		return body, false, nil
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, false, err
	}
	modified := false
	for key := range openAIUndefinedSentinelTopLevelFields {
		if isOpenAIUndefinedSentinelString(payload[key]) {
			delete(payload, key)
			modified = true
		}
	}
	if sanitizeOpenAIUndefinedSentinelInputIDs(payload["input"]) {
		modified = true
	}
	if !modified {
		return body, false, nil
	}
	sanitized, err := marshalOpenAIUpstreamJSON(payload)
	if err != nil {
		return nil, false, err
	}
	return sanitized, true, nil
}

func sanitizeOpenAIUndefinedSentinelInputIDs(value any) bool {
	modified := false
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if sanitizeOpenAIUndefinedSentinelInputIDs(item) {
				modified = true
			}
		}
	case map[string]any:
		if isOpenAIUndefinedSentinelString(typed["id"]) {
			delete(typed, "id")
			modified = true
		}
		for _, item := range typed {
			if sanitizeOpenAIUndefinedSentinelInputIDs(item) {
				modified = true
			}
		}
	}
	return modified
}

// isOpenAICompatibleAccountEligibleForRequest centralises the schedulable,
// platform, model and endpoint checks shared by OpenAI and Grok text routing.
func isOpenAICompatibleAccountEligibleForRequest(ctx context.Context, account *Account, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, rateLimitServices ...*RateLimitService) bool {
	return isOpenAICompatibleAccountEligibleForGroupRequest(ctx, account, nil, platform, requestedModel, requireCompact, requiredCapability, rateLimitServices...)
}

// isOpenAICompatibleAccountEligibleForGroupRequest is the group-aware variant
// used by the advanced scheduler after group mapping has been resolved. It
// avoids treating an account-wide mapping as the source of truth when the
// bound group intentionally overrides that one model.
func isOpenAICompatibleAccountEligibleForGroupRequest(ctx context.Context, account *Account, groupID *int64, platform string, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, rateLimitServices ...*RateLimitService) bool {
	platform = normalizeOpenAICompatiblePlatform(platform)
	if account == nil || account.Platform != platform || !account.IsOpenAICompatible() || !account.IsSchedulableForModelWithContext(ctx, requestedModel) {
		return false
	}
	var rateLimitService *RateLimitService
	if len(rateLimitServices) > 0 {
		rateLimitService = rateLimitServices[0]
	}
	if !openAIAccountMatchesLongContextRouting(ctx, account) {
		return false
	}
	var quotaPaused bool
	var quotaPauseReason openAIQuotaAutoPauseDecision
	if account.IsGrok() {
		if paused, reason := shouldAutoPauseGrokAccountByQuota(account); paused {
			slog.Debug("grok_account_auto_paused_by_quota",
				"account_id", account.ID,
				"window", reason.window,
				"threshold", reason.threshold,
				"utilization", reason.utilization,
			)
			return false
		}
	} else {
		quotaPaused, quotaPauseReason = shouldAutoPauseOpenAIAccountByQuota(ctx, account)
	}
	if requestedModel != "" && !isOpenAIAccountModelEligibleForGroup(account, groupID, requestedModel) {
		return false
	}
	if !account.SupportsOpenAIEndpointCapability(requiredCapability) {
		if account.IsGrok() && requiredCapability == OpenAIEndpointCapabilityGrokMediaGeneration {
			_, reason := account.GrokMediaGenerationEligibility()
			slog.Debug("grok_media_account_ineligible", "account_id", account.ID, "reason", reason)
		}
		return false
	}
	if requireCompact && (!account.IsOpenAI() || openAICompactSupportTier(account) == 0) {
		return false
	}
	// Evaluate/start Tail-Burst only after all request compatibility gates pass.
	// This keeps an incompatible model or endpoint from opening a session that
	// cannot receive any traffic, while remaining independent of auto-pause.
	// Candidate filtering must remain read-only; the durable marker is written
	// only once this account has won a request slot.
	tailBurstAllowed := rateLimitService != nil && rateLimitService.codexTailBurstEligible(ctx, account)
	if quotaPaused && !tailBurstAllowed {
		// Debug level: this fires per-candidate on the scheduling hot path, so Info
		// would amplify into log spam once several accounts cross the threshold.
		slog.Debug("account_auto_paused_by_quota",
			"account_id", account.ID,
			"window", quotaPauseReason.window,
			"threshold", quotaPauseReason.threshold,
			"utilization", quotaPauseReason.utilization,
		)
		return false
	}
	return true
}

// isOpenAIAccountEligibleForRequest preserves the legacy OpenAI-only helper
// used by existing tests and call sites.
func isOpenAIAccountEligibleForRequest(ctx context.Context, account *Account, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) bool {
	return isOpenAICompatibleAccountEligibleForRequest(ctx, account, PlatformOpenAI, requestedModel, requireCompact, requiredCapability)
}

type openAIQuotaAutoPauseDecision struct {
	window      string
	threshold   float64
	utilization float64
}

func shouldAutoPauseGrokAccountByQuota(account *Account) (bool, openAIQuotaAutoPauseDecision) {
	if account == nil || !account.IsGrokOAuth() {
		return false, openAIQuotaAutoPauseDecision{}
	}
	snapshot, err := grokQuotaSnapshotFromExtra(account.Extra)
	if err != nil || snapshot == nil {
		return false, openAIQuotaAutoPauseDecision{}
	}
	now := time.Now()
	if grokQuotaSnapshotStaleForPause(snapshot, now) {
		return false, openAIQuotaAutoPauseDecision{}
	}
	if grokQuotaRetryAfterActive(snapshot, now) {
		return true, openAIQuotaAutoPauseDecision{window: "retry_after", threshold: 1, utilization: 1}
	}
	if paused, decision := shouldAutoPauseGrokQuotaWindow("requests", snapshot.Requests, now); paused {
		return true, decision
	}
	if paused, decision := shouldAutoPauseGrokQuotaWindow("tokens", snapshot.Tokens, now); paused {
		return true, decision
	}
	return false, openAIQuotaAutoPauseDecision{}
}

func grokQuotaRetryAfterActive(snapshot *xai.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || snapshot.RetryAfterSeconds == nil || *snapshot.RetryAfterSeconds <= 0 {
		return false
	}
	if strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return true
	}
	updatedAt, err := parseTime(snapshot.UpdatedAt)
	if err != nil {
		return true
	}
	return now.Before(updatedAt.Add(time.Duration(*snapshot.RetryAfterSeconds) * time.Second))
}

func shouldAutoPauseGrokQuotaWindow(name string, window *xai.QuotaWindow, now time.Time) (bool, openAIQuotaAutoPauseDecision) {
	if window == nil || window.Limit == nil || window.Remaining == nil || *window.Limit <= 0 {
		return false, openAIQuotaAutoPauseDecision{}
	}
	if window.ResetUnix != nil && *window.ResetUnix > 0 && !now.Before(time.Unix(*window.ResetUnix, 0)) {
		return false, openAIQuotaAutoPauseDecision{}
	}
	utilization := float64(*window.Limit-*window.Remaining) / float64(*window.Limit)
	if *window.Remaining <= 0 || utilization >= 1 {
		return true, openAIQuotaAutoPauseDecision{window: name, threshold: 1, utilization: utilization}
	}
	return false, openAIQuotaAutoPauseDecision{}
}

func grokQuotaSnapshotStaleForPause(snapshot *xai.QuotaSnapshot, now time.Time) bool {
	if snapshot == nil || strings.TrimSpace(snapshot.UpdatedAt) == "" {
		return false
	}
	updatedAt, err := parseTime(snapshot.UpdatedAt)
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= openAICodexAutoPauseStaleAfter
}

func shouldAutoPauseOpenAIAccountByQuota(ctx context.Context, account *Account) (bool, openAIQuotaAutoPauseDecision) {
	if account == nil || !account.IsOpenAI() {
		return false, openAIQuotaAutoPauseDecision{}
	}
	// Per-account explicit-disable flags must take precedence over the global default.
	// Without these, leaving the account threshold blank means "use global default",
	// so an admin has no way to exempt a single account from auto-pause once a global
	// default exists. The disable flag is per-window so an account can opt out of
	// only 5h or only 7d auto-pause.
	disabled5h := resolveAccountExtraBool(account.Extra, "auto_pause_5h_disabled")
	disabled7d := resolveAccountExtraBool(account.Extra, "auto_pause_7d_disabled")
	threshold5h, threshold7d := resolveOpenAIQuotaAutoPauseThresholds(ctx, account)
	now := time.Now()
	if !disabled5h && threshold5h > 0 {
		if utilization, ok := resolveOpenAIQuotaUtilization(account.Extra, "5h", now); ok && utilization >= threshold5h {
			return true, openAIQuotaAutoPauseDecision{window: "5h", threshold: threshold5h, utilization: utilization}
		}
	}
	if !disabled7d && threshold7d > 0 {
		if utilization, ok := resolveOpenAIQuotaUtilization(account.Extra, "7d", now); ok && utilization >= threshold7d {
			return true, openAIQuotaAutoPauseDecision{window: "7d", threshold: threshold7d, utilization: utilization}
		}
	}
	return false, openAIQuotaAutoPauseDecision{}
}

// resolveAccountExtraBool reads a bool-like value from account extra, tolerating
// the few shapes JSON unmarshalling may produce (real bool, "true"/"false"
// strings, 0/1 numbers).
func resolveAccountExtraBool(extra map[string]any, key string) bool {
	if len(extra) == 0 {
		return false
	}
	value, ok := extra[key]
	if !ok || value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && parsed
	case float64:
		return v != 0
	case float32:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i != 0
		}
	}
	return false
}

func resolveOpenAIQuotaAutoPauseThresholds(ctx context.Context, account *Account) (float64, float64) {
	threshold5h, _ := resolveAccountExtraNumber(account.Extra, "auto_pause_5h_threshold")
	threshold7d, _ := resolveAccountExtraNumber(account.Extra, "auto_pause_7d_threshold")
	threshold5h = clamp01(threshold5h)
	threshold7d = clamp01(threshold7d)
	if threshold5h > 0 && threshold7d > 0 {
		return threshold5h, threshold7d
	}
	settings := openAIQuotaAutoPauseSettingsFromContext(ctx)
	if threshold5h <= 0 {
		threshold5h = clamp01(settings.DefaultThreshold5h)
	}
	if threshold7d <= 0 {
		threshold7d = clamp01(settings.DefaultThreshold7d)
	}
	return threshold5h, threshold7d
}

func resolveAccountExtraNumber(extra map[string]any, keys ...string) (float64, bool) {
	if len(extra) == 0 {
		return 0, false
	}
	for _, key := range keys {
		value, ok := extra[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case float64:
			return v, true
		case float32:
			return float64(v), true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case json.Number:
			parsed, err := v.Float64()
			if err == nil {
				return parsed, true
			}
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

// resolveOpenAIQuotaUtilization returns the current utilization ratio (0..1) for the
// given Codex usage window. ok=false means there is no usable signal to pause on:
// either no snapshot exists, or the window has already rolled over so the cached
// percentage is stale. The stale guard matters because a paused account stops
// receiving requests, so its snapshot is never refreshed from upstream headers —
// without this check an old used_percent would keep the account paused forever even
// after the real window reset.
func resolveOpenAIQuotaUtilization(extra map[string]any, window string, now time.Time) (float64, bool) {
	usedPercent := readOpenAIQuotaUsedPercent(extra, window)
	if usedPercent <= 0 {
		return 0, false
	}
	if openAIQuotaWindowReset(extra, window, now) {
		return 0, false
	}
	// 快照过于陈旧（账号长期未收到流量刷新）时，不再据此暂停。放行后下一次响应头
	// 会刷新快照实现自愈，避免账号在错误/过期的 used% 上被永久跳过（issue #2994）。
	if openAICodexSnapshotStaleForPause(extra, window, now) {
		return 0, false
	}
	return usedPercent / 100, true
}

// openAICodexSnapshotStaleForPause reports whether the Codex usage snapshot is stale
// enough that it should no longer keep an account auto-paused. It anchors on
// codex_usage_updated_at (always written by buildCodexUsageExtraUpdates). A missing or
// unparseable timestamp returns false (treated as fresh, so the account stays paused) —
// this is deliberate: it prevents any snapshot without a write time from silently escaping
// auto-pause, and a genuinely-exhausted account that is actively served refreshes the snapshot.
func openAICodexSnapshotStaleForPause(extra map[string]any, window string, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	updatedRaw, ok := extra["codex_usage_updated_at"]
	if !ok {
		return false
	}
	updatedAt, err := parseTime(fmt.Sprint(updatedRaw))
	if err != nil {
		return false
	}
	return now.Sub(updatedAt) >= openAICodexAutoPauseStaleAfter
}

// openAIQuotaWindowReset reports whether the Codex usage window's reset time has
// already passed relative to now. It prefers the absolute codex_<window>_reset_at
// timestamp and falls back to codex_<window>_reset_after_seconds anchored at
// codex_usage_updated_at, mirroring AccountUsageService's window-progress logic.
func openAIQuotaWindowReset(extra map[string]any, window string, now time.Time) bool {
	if len(extra) == 0 {
		return false
	}
	if resetAtRaw, ok := extra["codex_"+window+"_reset_at"]; ok {
		if resetAt, err := parseTime(fmt.Sprint(resetAtRaw)); err == nil {
			return !now.Before(resetAt)
		}
	}
	resetAfter := parseExtraInt(extra["codex_"+window+"_reset_after_seconds"])
	if resetAfter <= 0 {
		return false
	}
	base := now
	if updatedAt, ok := codexWindowUpdatedAt(extra, window); ok && !updatedAt.After(now) {
		base = updatedAt
	}
	resetAt := base.Add(time.Duration(resetAfter) * time.Second)
	return !now.Before(resetAt)
}

func readOpenAIQuotaUsedPercent(extra map[string]any, window string) float64 {
	if len(extra) == 0 {
		return 0
	}
	if value, ok := resolveAccountExtraNumber(extra, "codex_"+window+"_used_percent"); ok {
		return value
	}
	return 0
}

type openAIQuotaAutoPauseCtxKey struct{}

// openAISelectionSnapshotCache 把一次用户请求内已经读取过的分组和完整账号固定下来。
// 调度候选、实时资格复查和凭据 hydrate（补全）原本会连续读取同一个 Redis 快照；
// 请求内复用既不延长跨请求陈旧窗口，也不会绕过独立的并发与 runtime block（运行时封禁）检查。
type openAISelectionSnapshotCache struct {
	mu       sync.Mutex
	accounts map[int64]openAISelectionAccountRead
	groups   map[int64]openAISelectionGroupRead
}

type openAISelectionAccountRead struct {
	account *Account
	err     error
}

type openAISelectionGroupRead struct {
	group *Group
	err   error
}

type openAISelectionSnapshotCacheContextKey struct{}

func withOpenAISelectionSnapshotCache(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if cached, _ := ctx.Value(openAISelectionSnapshotCacheContextKey{}).(*openAISelectionSnapshotCache); cached != nil {
		return ctx
	}
	return context.WithValue(ctx, openAISelectionSnapshotCacheContextKey{}, &openAISelectionSnapshotCache{
		accounts: make(map[int64]openAISelectionAccountRead),
		groups:   make(map[int64]openAISelectionGroupRead),
	})
}

func openAISelectionSnapshotCacheFromContext(ctx context.Context) *openAISelectionSnapshotCache {
	if ctx == nil {
		return nil
	}
	cached, _ := ctx.Value(openAISelectionSnapshotCacheContextKey{}).(*openAISelectionSnapshotCache)
	return cached
}

func withOpenAIQuotaAutoPauseSettings(ctx context.Context, settings OpsOpenAIAccountQuotaAutoPauseSettings) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIQuotaAutoPauseCtxKey{}, settings)
}

func openAIQuotaAutoPauseSettingsFromContext(ctx context.Context) OpsOpenAIAccountQuotaAutoPauseSettings {
	if ctx == nil {
		return OpsOpenAIAccountQuotaAutoPauseSettings{}
	}
	settings, _ := ctx.Value(openAIQuotaAutoPauseCtxKey{}).(OpsOpenAIAccountQuotaAutoPauseSettings)
	return settings
}

func (s *OpenAIGatewayService) withOpenAIQuotaAutoPauseContext(ctx context.Context) context.Context {
	if s == nil || s.settingService == nil {
		return ctx
	}
	return withOpenAIQuotaAutoPauseSettings(ctx, s.settingService.GetOpenAIQuotaAutoPauseSettings(ctx))
}

// prioritizeOpenAICompactAccounts re-orders a slice so that accounts with known
// compact support are tried first, followed by unknown, then explicitly unsupported.
// The relative order within each tier is preserved.
func prioritizeOpenAICompactAccounts(accounts []*Account) []*Account {
	if len(accounts) == 0 {
		return nil
	}
	supported := make([]*Account, 0, len(accounts))
	unknown := make([]*Account, 0, len(accounts))
	unsupported := make([]*Account, 0, len(accounts))
	for _, account := range accounts {
		switch openAICompactSupportTier(account) {
		case 2:
			supported = append(supported, account)
		case 1:
			unknown = append(unknown, account)
		default:
			unsupported = append(unsupported, account)
		}
	}
	out := make([]*Account, 0, len(accounts))
	out = append(out, supported...)
	out = append(out, unknown...)
	out = append(out, unsupported...)
	return out
}

// resolveOpenAIAccountUpstreamModelForRequest resolves the model that the
// selected account's real Forward branch will send upstream. Passthrough and
// native Responses both apply the selected account/group mapping; raw Chat
// Completions fallback never applies the legacy compact-only mapping.
func resolveOpenAIAccountUpstreamModelForRequest(ctx context.Context, groupID *int64, account *Account, requestedModel string, requireCompact bool) string {
	if shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		upstreamModel := ResolveOpenAIForwardModelForRequest(ctx, groupID, account, requestedModel, "")
		return normalizeOpenAIModelForUpstream(account, upstreamModel)
	}

	if account != nil && account.IsOpenAIPassthroughEnabled() {
		upstreamModel := MappedAccountModelForRequest(ctx, groupID, account, requestedModel)
		if upstreamModel == "" {
			return ""
		}
		if requireCompact {
			return resolveOpenAICompactForwardModel(account, upstreamModel)
		}
		return upstreamModel
	}

	upstreamModel := ResolveOpenAIForwardModelForRequest(ctx, groupID, account, requestedModel, "")
	if upstreamModel == "" {
		return ""
	}
	if requireCompact {
		compactModel := resolveOpenAICompactForwardModel(account, upstreamModel)
		if compactModel != upstreamModel {
			return compactModel
		}
	}
	return normalizeOpenAIModelForUpstream(account, upstreamModel)
}

func (s *OpenAIGatewayService) selectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability, platformOverride ...string) (*Account, error) {
	excludedIDs = MergeKeywordProtectedAccountIDs(ctx, excludedIDs)
	var mappingErr error
	ctx, requestedModel, mappingErr = s.resolveOpenAIGroupModelForSelection(ctx, groupID, requestedModel)
	if mappingErr != nil {
		return nil, mappingErr
	}
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	// 1. 尝试粘性会话命中
	// Try sticky session hit
	randomScheduling := groupID != nil && GroupUsesRandomScheduling(finalMappingGroupFromContext(ctx, *groupID))
	if !randomScheduling {
		if account := s.tryStickySessionHit(ctx, groupID, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability, platform); account != nil {
			return account, nil
		}
	}

	// 2. 获取可调度的 OpenAI 账号
	// Get schedulable OpenAI accounts
	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}

	// 3. 按优先级 + LRU 选择最佳账号
	// Select by priority + LRU
	selected, compactBlocked := s.selectBestAccount(ctx, groupID, accounts, requestedModel, excludedIDs, requireCompact, requiredCapability, platform)

	if selected == nil {
		return nil, noAvailableOpenAISelectionError(requestedModel, compactBlocked, "")
	}

	hydrated, err := s.hydrateSelectedAccount(ctx, selected)
	if err != nil {
		return nil, err
	}

	// 4. 设置粘性会话绑定
	// Set sticky session binding
	if !randomScheduling && sessionHash != "" {
		_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, selected.ID, openaiStickySessionTTL)
	}

	return hydrated, nil
}

// tryStickySessionHit 尝试从粘性会话获取账号。
// 如果命中且账号可用则返回账号；如果账号不可用则清理会话并返回 nil。
//
// tryStickySessionHit attempts to get account from sticky session.
// Returns account if hit and usable; clears session and returns nil if account is unavailable.
func (s *OpenAIGatewayService) tryStickySessionHit(ctx context.Context, groupID *int64, sessionHash, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, stickyAccountID int64, requiredCapability OpenAIEndpointCapability, platformOverride ...string) *Account {
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	if sessionHash == "" {
		return nil
	}

	accountID := stickyAccountID
	if accountID <= 0 {
		var err error
		accountID, err = s.getStickySessionAccountID(ctx, groupID, sessionHash)
		if err != nil || accountID <= 0 {
			return nil
		}
	}

	if _, excluded := excludedIDs[accountID]; excluded {
		return nil
	}

	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil {
		return nil
	}

	// 检查账号是否需要清理粘性会话
	// Check if sticky session should be cleared
	if shouldClearStickySession(account, requestedModel) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		return nil
	}

	// 验证账号是否可用于当前请求
	// Verify account is usable for current request
	if !openAIAccountMatchesLongContextRouting(ctx, account) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		return nil
	}
	if !isOpenAICompatibleAccountEligibleForRequest(ctx, account, platform, requestedModel, false, requiredCapability, s.rateLimitService) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlockedForGroup(account, groupID, requestedModel) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		return nil
	}
	account = s.recheckSelectedOpenAIAccountFromDB(ctx, account, requestedModel, requireCompact, requiredCapability, platform)
	if account == nil || !openAIStickyAccountMatchesGroup(account, groupID) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		return nil
	}
	if groupID != nil && s.needsUpstreamChannelRestrictionCheck(ctx, groupID) &&
		s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		return nil
	}
	// 该旧选择入口不负责获取账号槽位（例如轻量探测请求）。它不能领取
	// half-open（半开）恢复探测，因此必须跳过恢复中的账号+实际模型，
	// 交由会原子抢槽位的正常调度路径完成一次探测。
	if s.requiresOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel) {
		return nil
	}

	// 刷新会话 TTL 并返回账号
	// Refresh session TTL and return account
	_ = s.refreshStickySessionTTL(ctx, groupID, sessionHash, openaiStickySessionTTL)
	return account
}

// selectBestAccount 从候选账号中选择最佳账号（优先级 + LRU）。
// 返回 nil 表示无可用账号。
//
// selectBestAccount selects the best account from candidates (priority + LRU).
// Returns nil if no available account. The second return reports whether at
// least one candidate was filtered out solely because it lacks compact support
// (only meaningful when requireCompact=true).
func (s *OpenAIGatewayService) selectBestAccount(ctx context.Context, groupID *int64, accounts []Account, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, platformOverride ...string) (*Account, bool) {
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	var selected *Account
	selectedCompactTier := -1
	compactBlocked := false
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	randomScheduling := groupID != nil && GroupUsesRandomScheduling(finalMappingGroupFromContext(ctx, *groupID))
	eligibleCount := 0

	for i := range accounts {
		acc := &accounts[i]

		// 跳过被排除的账号
		// Skip excluded accounts
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}

		fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, acc, requestedModel, false, requiredCapability, platform)
		if fresh == nil {
			continue
		}
		fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, requestedModel, false, requiredCapability, platform)
		if fresh == nil {
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
			continue
		}
		// selectBestAccount 也会被不获取账号槽位的兼容入口调用。恢复模型
		// 只能由随后可原子获取槽位的调度路径使用，不能在这里直接放行。
		if s.requiresOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel) {
			continue
		}
		compactTier := 0
		if requireCompact {
			compactTier = openAICompactSupportTier(fresh)
			if compactTier == 0 {
				compactBlocked = true
				continue
			}
		}
		eligibleCount++

		// 选择优先级最高且最久未使用的账号
		// Select highest priority and least recently used
		if selected == nil {
			selected = fresh
			selectedCompactTier = compactTier
			continue
		}
		if randomScheduling {
			if mathrand.Intn(eligibleCount) == 0 {
				selected = fresh
				selectedCompactTier = compactTier
			}
			continue
		}

		// compact 模式下高 tier 优先；同 tier 内才比较 priority/LRU。
		if requireCompact && compactTier != selectedCompactTier {
			if compactTier > selectedCompactTier {
				selected = fresh
				selectedCompactTier = compactTier
			}
			continue
		}

		if s.isBetterAccount(fresh, selected) {
			selected = fresh
			selectedCompactTier = compactTier
		}
	}

	return selected, compactBlocked
}

// isBetterAccount 判断 candidate 是否比 current 更优。
// 规则：优先级更高（数值更小）优先；同优先级时，未使用过的优先，其次是最久未使用的。
//
// isBetterAccount checks if candidate is better than current.
// Rules: higher priority (lower value) wins; same priority: never used > least recently used.
func (s *OpenAIGatewayService) isBetterAccount(candidate, current *Account) bool {
	// 优先级更高（数值更小）
	// Higher priority (lower value)
	if candidate.Priority < current.Priority {
		return true
	}
	if candidate.Priority > current.Priority {
		return false
	}

	// 同优先级，比较最后使用时间
	// Same priority, compare last used time
	switch {
	case candidate.LastUsedAt == nil && current.LastUsedAt != nil:
		// candidate 从未使用，优先
		return true
	case candidate.LastUsedAt != nil && current.LastUsedAt == nil:
		// current 从未使用，保持
		return false
	case candidate.LastUsedAt == nil && current.LastUsedAt == nil:
		// 都未使用，保持
		return false
	default:
		// 都使用过，选择最久未使用的
		return candidate.LastUsedAt.Before(*current.LastUsedAt)
	}
}

// SelectAccountWithLoadAwareness selects an account with load-awareness and wait plan.
func (s *OpenAIGatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*AccountSelectionResult, error) {
	selection, err := s.selectAccountWithLoadAwareness(s.withOpenAIQuotaAutoPauseContext(ctx), groupID, sessionHash, requestedModel, excludedIDs, false, "", PlatformOpenAI)
	if err != nil {
		return nil, wrapGatewayGroupUnavailableSelectionError(ctx, groupID, err)
	}
	return selection, nil
}

// tryAcquireLoadAwareStickyAccount handles the common sticky-session hit before
// loading the full account pool. The old order loaded and filtered every account
// even when the sticky account was healthy, creating avoidable Redis/cache work
// for long conversations under high concurrency.
func (s *OpenAIGatewayService) tryAcquireLoadAwareStickyAccount(
	ctx context.Context,
	groupID *int64,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requireCompact bool,
	requiredCapability OpenAIEndpointCapability,
	platform string,
	stickyAccountID int64,
	needsUpstreamCheck bool,
	cfg config.GatewaySchedulingConfig,
) (*AccountSelectionResult, bool, bool, error) {
	if sessionHash == "" || stickyAccountID <= 0 {
		return nil, false, false, nil
	}
	if _, excluded := excludedIDs[stickyAccountID]; excluded {
		return nil, false, false, nil
	}

	accountResolveStart := time.Now()
	account, err := s.getSchedulableAccount(ctx, stickyAccountID)
	if err != nil || account == nil {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, false, nil
	}
	clearSticky := shouldClearStickySession(account, requestedModel)
	if clearSticky {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
	}
	if !clearSticky && !openAIAccountMatchesLongContextRouting(ctx, account) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		clearSticky = true
	}
	if clearSticky || !isOpenAICompatibleAccountEligibleForRequest(ctx, account, platform, requestedModel, false, requiredCapability, s.rateLimitService) {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, false, nil
	}
	account = s.recheckSelectedOpenAIAccountFromDB(ctx, account, requestedModel, requireCompact, requiredCapability, platform)
	if account == nil || !openAIStickyAccountMatchesGroup(account, groupID) || s.isOpenAIAccountRequestRuntimeBlockedForGroup(account, groupID, requestedModel) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, false, nil
	}
	if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact) {
		_ = s.deleteStickySessionAccountID(ctx, groupID, sessionHash)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))
		return nil, false, false, nil
	}
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(accountResolveStart))

	// 粘性命中的恢复模型必须先竞争单探测租约，再立即占用账号槽位。
	// 等待队列无法在唤醒时保留该原子边界，因此恢复模型不能被排队。
	recoveryProbeRequired := s.requiresOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
	if !s.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel) {
		return nil, false, false, nil
	}
	accountSlotStart := time.Now()
	result, acquireErr := s.tryAcquireAccountSlot(ctx, stickyAccountID, account.Concurrency)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountSlot, time.Since(accountSlotStart))
	if acquireErr == nil && result != nil && result.Acquired {
		if s.rateLimitService != nil && !s.rateLimitService.admitCodexTailBurstAfterSlot(ctx, account) {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
			return nil, false, false, nil
		}
		materializeStart := time.Now()
		selection, selectErr := s.newAcquiredSelectionResult(ctx, account, result.ReleaseFunc)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageAccountResolve, time.Since(materializeStart))
		if selectErr != nil {
			s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
			return nil, true, false, selectErr
		}
		stickyRefreshStart := time.Now()
		_ = s.refreshStickySessionTTL(ctx, groupID, sessionHash, openaiStickySessionTTL)
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageStickyRefresh, time.Since(stickyRefreshStart))
		return selection, true, false, nil
	}
	s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
	if recoveryProbeRequired {
		return nil, false, false, nil
	}

	waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, stickyAccountID)
	if waitingCount < cfg.StickySessionMaxWaiting {
		// 粘性超额账号不能进入等待队列：唤醒时无法和槽位获取原子地
		// 提交 Tail-Burst 标记，交回候选池由立即占槽路径处理。
		if s.rateLimitService != nil && s.rateLimitService.codexTailBurstEligible(ctx, account) {
			return nil, false, true, nil
		}
		selection, selectErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID:      stickyAccountID,
			MaxConcurrency: account.Concurrency,
			Timeout:        cfg.StickySessionWaitTimeout,
			MaxWaiting:     cfg.StickySessionMaxWaiting,
		})
		return selection, true, false, selectErr
	}
	// 粘性账号健康但瞬时满载时只允许本次请求分流；
	// 不能把持久绑定迁移到新账号，否则后续请求会丢失上游会话缓存。
	return nil, false, true, nil
}

func (s *OpenAIGatewayService) selectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, platformOverride ...string) (*AccountSelectionResult, error) {
	excludedIDs = MergeKeywordProtectedAccountIDs(ctx, excludedIDs)
	ctx = withOpenAISelectionSnapshotCache(ctx)
	var mappingErr error
	ctx, requestedModel, mappingErr = s.resolveOpenAIGroupModelForSelection(ctx, groupID, requestedModel)
	if mappingErr != nil {
		return nil, mappingErr
	}
	randomScheduling := groupID != nil && GroupUsesRandomScheduling(finalMappingGroupFromContext(ctx, *groupID))
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	groupValidationStart := time.Now()
	groupValidationErr := s.validateGatewayLiteFixedOpenAIGroup(ctx, groupID)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageGroupValidation, time.Since(groupValidationStart))
	if groupValidationErr != nil {
		return nil, groupValidationErr
	}
	if s.checkChannelPricingRestriction(ctx, groupID, requestedModel) {
		slog.Warn("channel pricing restriction blocked request",
			"group_id", derefGroupID(groupID),
			"model", requestedModel)
		return nil, fmt.Errorf("%w supporting model: %s (channel pricing restriction)", ErrNoAvailableAccounts, requestedModel)
	}

	settingsStart := time.Now()
	cfg := s.schedulingConfig(ctx)
	needsUpstreamCheck := s.needsUpstreamChannelRestrictionCheck(ctx, groupID)
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageSettings, time.Since(settingsStart))
	var stickyAccountID int64
	if !randomScheduling && sessionHash != "" && s.cache != nil {
		stickyLookupStart := time.Now()
		if accountID, err := s.getStickySessionAccountID(ctx, groupID, sessionHash); err == nil {
			stickyAccountID = accountID
		}
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageStickyLookup, time.Since(stickyLookupStart))
	}
	if s.concurrencyService == nil || (!cfg.LoadBatchEnabled && !randomScheduling) {
		account, err := s.selectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, excludedIDs, requireCompact, stickyAccountID, requiredCapability, platform)
		if err != nil {
			return nil, err
		}
		recoveryProbeRequired := s.requiresOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
		if !s.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel) {
			return nil, ErrNoAvailableAccounts
		}
		result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err == nil && result != nil && result.Acquired {
			if s.rateLimitService != nil && !s.rateLimitService.admitCodexTailBurstAfterSlot(ctx, account) {
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
				return nil, ErrNoAvailableAccounts
			}
			selection, selectErr := s.newAcquiredSelectionResult(ctx, account, result.ReleaseFunc)
			if selectErr != nil {
				s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
			}
			return selection, selectErr
		}
		s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(account, groupID, requestedModel)
		if recoveryProbeRequired {
			return nil, ErrNoAvailableAccounts
		}
		if stickyAccountID > 0 && stickyAccountID == account.ID && s.concurrencyService != nil {
			if s.rateLimitService != nil && s.rateLimitService.codexTailBurstEligible(ctx, account) {
				return nil, ErrNoAvailableAccounts
			}
			waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, account.ID)
			if waitingCount < cfg.StickySessionMaxWaiting {
				return s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
					AccountID:      account.ID,
					MaxConcurrency: account.Concurrency,
					Timeout:        cfg.StickySessionWaitTimeout,
					MaxWaiting:     cfg.StickySessionMaxWaiting,
				})
			}
		}
		if !s.canUseFallbackQueue(ctx, cfg, account.ID) {
			if s.isFallbackQueueFull(ctx, cfg, account.ID) {
				return nil, ErrFallbackQueueFull
			}
			return s.newSelectionResult(ctx, account, false, nil, nil)
		}
		return s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID:      account.ID,
			MaxConcurrency: account.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
	}

	stickySpillover := false
	if !randomScheduling {
		if selection, handled, spillover, stickyErr := s.tryAcquireLoadAwareStickyAccount(
			ctx, groupID, sessionHash, requestedModel, excludedIDs, requireCompact,
			requiredCapability, platform, stickyAccountID, needsUpstreamCheck, cfg,
		); stickyErr != nil {
			return nil, stickyErr
		} else if handled {
			return selection, nil
		} else {
			stickySpillover = spillover
		}
	}

	candidateResolutionStart := time.Now()
	accounts, err := s.listSchedulableAccounts(ctx, groupID, platform)
	if err != nil {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return nil, err
	}
	diagTotalAccounts := len(accounts)
	if len(accounts) == 0 {
		AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))
		return nil, ErrNoAvailableAccounts
	}
	AddOpsRoutingSelectionLatency(ctx, OpsRoutingSelectionStageCandidateResolution, time.Since(candidateResolutionStart))

	isExcluded := func(accountID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[accountID]
		return excluded
	}

	// ============ Load-aware selection after sticky miss ============
	baseCandidateCount := 0
	candidates := make([]*Account, 0, len(accounts))
	diagExcluded := 0
	diagIneligible := 0
	diagRuntimeBlocked := 0
	diagUpstreamRestricted := 0
	for i := range accounts {
		acc := &accounts[i]
		if isExcluded(acc.ID) {
			diagExcluded++
			continue
		}
		// Scheduler snapshots can be temporarily stale (bucket rebuild is throttled);
		// re-check schedulability here so recently rate-limited/overloaded accounts
		// are not selected again before the bucket is rebuilt.
		if !isOpenAICompatibleAccountEligibleForRequest(ctx, acc, platform, requestedModel, false, requiredCapability, s.rateLimitService) {
			diagIneligible++
			continue
		}
		if s.isOpenAIAccountRequestRuntimeBlockedForGroup(acc, groupID, requestedModel) {
			diagRuntimeBlocked++
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, acc, requestedModel, requireCompact) {
			diagUpstreamRestricted++
			continue
		}
		baseCandidateCount++
		candidates = append(candidates, acc)
	}

	if len(candidates) == 0 {
		slog.Warn("openai_load_awareness_no_candidates",
			"group_id", derefGroupID(groupID),
			"model", requestedModel,
			"total_accounts", diagTotalAccounts,
			"excluded", diagExcluded,
			"ineligible", diagIneligible,
			"runtime_blocked", diagRuntimeBlocked,
			"upstream_restricted", diagUpstreamRestricted,
			"required_capability", string(requiredCapability),
		)
		return nil, ErrNoAvailableAccounts
	}

	accountLoads := make([]AccountWithConcurrency, 0, len(candidates))
	for _, acc := range candidates {
		accountLoads = append(accountLoads, AccountWithConcurrency{
			ID:             acc.ID,
			MaxConcurrency: acc.EffectiveLoadFactor(),
		})
	}

	diagLoadAvailable := 0
	diagAcquireAttempts := 0
	diagAcquireDenied := 0
	diagAcquireErrors := 0
	diagFreshNil := 0
	diagRecheckNil := 0
	diagFallbackFreshNil := 0
	diagFallbackRecheckNil := 0
	diagFallbackQueueRejected := 0
	tryAcquireFromLoadMap := func(loadMap map[int64]*AccountLoadInfo) (*AccountSelectionResult, bool, error) {
		var available []accountWithLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			if loadInfo == nil {
				loadInfo = &AccountLoadInfo{AccountID: acc.ID}
			}
			if loadInfo.LoadRate < 100 {
				diagLoadAvailable++
				available = append(available, accountWithLoad{
					account:  acc,
					loadInfo: loadInfo,
				})
			}
		}

		if len(available) == 0 {
			return nil, false, nil
		}

		sort.SliceStable(available, func(i, j int) bool {
			a, b := available[i], available[j]
			if a.account.Priority != b.account.Priority {
				return a.account.Priority < b.account.Priority
			}
			if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
				return a.loadInfo.LoadRate < b.loadInfo.LoadRate
			}
			switch {
			case a.account.LastUsedAt == nil && b.account.LastUsedAt != nil:
				return true
			case a.account.LastUsedAt != nil && b.account.LastUsedAt == nil:
				return false
			case a.account.LastUsedAt == nil && b.account.LastUsedAt == nil:
				return false
			default:
				return a.account.LastUsedAt.Before(*b.account.LastUsedAt)
			}
		})
		if randomScheduling {
			// 候选已经通过平台、模型、额度、通道限制和负载门禁；完全随机
			// 不再参考账号优先级、负载率或最近使用时间。
			mathrand.Shuffle(len(available), func(i, j int) { available[i], available[j] = available[j], available[i] })
		} else {
			shuffleWithinSortGroups(available)
		}

		selectionOrder := make([]accountWithLoad, 0, len(available))
		if requireCompact {
			appendTier := func(out []accountWithLoad, tier int) []accountWithLoad {
				for _, item := range available {
					if openAICompactSupportTier(item.account) == tier {
						out = append(out, item)
					}
				}
				return out
			}
			selectionOrder = appendTier(selectionOrder, 2)
			selectionOrder = appendTier(selectionOrder, 1)
			// tier 0 候选作为兜底追加：DB recheck 时若发现 cache tier 0 实际
			// 已升级为 1/2（探测刚跑完，cache 尚未刷新），仍可正常命中。
			selectionOrder = appendTier(selectionOrder, 0)
		} else {
			selectionOrder = append(selectionOrder, available...)
		}

		for _, item := range selectionOrder {
			fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, item.account, requestedModel, false, requiredCapability, platform)
			if fresh == nil {
				diagFreshNil++
				continue
			}
			fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, requestedModel, requireCompact, requiredCapability, platform)
			if fresh == nil {
				diagRecheckNil++
				continue
			}
			if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
				diagUpstreamRestricted++
				continue
			}
			diagAcquireAttempts++
			if !s.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel) {
				diagAcquireDenied++
				continue
			}
			result, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
			if err == nil && result != nil && result.Acquired {
				if s.rateLimitService != nil && !s.rateLimitService.admitCodexTailBurstAfterSlot(ctx, fresh) {
					if result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
					s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel)
					continue
				}
				selection, selectErr := s.newAcquiredSelectionResult(ctx, fresh, result.ReleaseFunc)
				if selectErr != nil {
					s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel)
					return nil, true, selectErr
				}
				if !randomScheduling && !stickySpillover && sessionHash != "" {
					_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, fresh.ID, openaiStickySessionTTL)
				}
				return selection, true, nil
			}
			s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel)
			if err != nil {
				diagAcquireErrors++
			} else {
				diagAcquireDenied++
			}
		}
		return nil, true, nil
	}

	loadMap, err := s.concurrencyService.GetAccountsLoadBatch(ctx, accountLoads)
	if err != nil {
		ordered := append([]*Account(nil), candidates...)
		if randomScheduling {
			mathrand.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
		} else {
			sortAccountsByPriorityAndLastUsed(ordered, false)
		}
		if requireCompact {
			ordered = prioritizeOpenAICompactAccounts(ordered)
		}
		for _, acc := range ordered {
			fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, acc, requestedModel, false, requiredCapability, platform)
			if fresh == nil {
				continue
			}
			fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, requestedModel, requireCompact, requiredCapability, platform)
			if fresh == nil {
				continue
			}
			if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
				continue
			}
			if s.rateLimitService != nil && s.rateLimitService.codexTailBurstEligible(ctx, fresh) {
				continue
			}
			if !s.TryAcquireOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel) {
				continue
			}
			result, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
			if err == nil && result != nil && result.Acquired {
				if s.rateLimitService != nil && !s.rateLimitService.admitCodexTailBurstAfterSlot(ctx, fresh) {
					if result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
					s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel)
					continue
				}
				selection, selectErr := s.newAcquiredSelectionResult(ctx, fresh, result.ReleaseFunc)
				if selectErr != nil {
					s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel)
					return nil, selectErr
				}
				if !randomScheduling && !stickySpillover && sessionHash != "" {
					_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, fresh.ID, openaiStickySessionTTL)
				}
				return selection, nil
			}
			s.ReleaseOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel)
		}
	} else {
		if selection, attempted, selectErr := tryAcquireFromLoadMap(loadMap); selectErr != nil {
			return nil, selectErr
		} else if selection != nil {
			return selection, nil
		} else if attempted {
			if freshLoadMap, loadErr := s.concurrencyService.GetAccountsLoadBatchFresh(ctx, accountLoads); loadErr == nil {
				if selection, _, selectErr := tryAcquireFromLoadMap(freshLoadMap); selectErr != nil {
					return nil, selectErr
				} else if selection != nil {
					return selection, nil
				}
			}
		}
	}

	// ============ Layer 3: Fallback wait ============
	if randomScheduling {
		mathrand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	} else {
		sortAccountsByPriorityAndLastUsed(candidates, false)
	}
	if requireCompact {
		candidates = prioritizeOpenAICompactAccounts(candidates)
	}
	fallbackQueueFull := false
	for _, acc := range candidates {
		fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, acc, requestedModel, false, requiredCapability, platform)
		if fresh == nil {
			diagFallbackFreshNil++
			continue
		}
		fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, requestedModel, requireCompact, requiredCapability, platform)
		if fresh == nil {
			diagFallbackRecheckNil++
			continue
		}
		if needsUpstreamCheck && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, fresh, requestedModel, requireCompact) {
			diagUpstreamRestricted++
			continue
		}
		// WaitPlan（等待计划）唤醒时没有 Tail-Burst 的原子准入边界，
		// 因此超额候选不能被放入回退队列。
		if s.rateLimitService != nil && s.rateLimitService.codexTailBurstEligible(ctx, fresh) {
			continue
		}
		// Recovery probes require an immediate account-slot acquisition. A
		// WaitPlan would defer acquisition and let multiple queued callers
		// bypass the one-probe guarantee when the slot becomes available.
		if s.requiresOpenAIAccountModelRecoveryProbeForGroup(fresh, groupID, requestedModel) {
			continue
		}
		if !s.canUseFallbackQueue(ctx, cfg, fresh.ID) {
			diagFallbackQueueRejected++
			if s.isFallbackQueueFull(ctx, cfg, fresh.ID) {
				fallbackQueueFull = true
			}
			continue
		}
		return s.newSelectionResult(ctx, fresh, false, nil, &AccountWaitPlan{
			AccountID:      fresh.ID,
			MaxConcurrency: fresh.Concurrency,
			Timeout:        cfg.FallbackWaitTimeout,
			MaxWaiting:     cfg.FallbackMaxWaiting,
		})
	}

	if requireCompact && baseCandidateCount > 0 {
		return nil, ErrNoAvailableCompactAccounts
	}
	if fallbackQueueFull {
		return nil, ErrFallbackQueueFull
	}
	slog.Warn("openai_load_awareness_selection_exhausted",
		"group_id", derefGroupID(groupID),
		"model", requestedModel,
		"total_accounts", diagTotalAccounts,
		"candidates", len(candidates),
		"base_candidate_count", baseCandidateCount,
		"excluded", diagExcluded,
		"ineligible", diagIneligible,
		"runtime_blocked", diagRuntimeBlocked,
		"upstream_restricted", diagUpstreamRestricted,
		"load_available", diagLoadAvailable,
		"acquire_attempts", diagAcquireAttempts,
		"acquire_denied", diagAcquireDenied,
		"acquire_errors", diagAcquireErrors,
		"fresh_nil", diagFreshNil,
		"recheck_nil", diagRecheckNil,
		"fallback_fresh_nil", diagFallbackFreshNil,
		"fallback_recheck_nil", diagFallbackRecheckNil,
		"fallback_queue_rejected", diagFallbackQueueRejected,
		"required_capability", string(requiredCapability),
	)
	return nil, ErrNoAvailableAccounts
}

func (s *OpenAIGatewayService) listSchedulableAccounts(ctx context.Context, groupID *int64, platformOverride ...string) ([]Account, error) {
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	if s.schedulerSnapshot != nil {
		accounts, _, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, false)
		if err == nil {
			accounts = s.filterOpenAIAccountsByGrokSchedulingThreshold(ctx, accounts)
		}
		return accounts, err
	}
	var accounts []Account
	var err error
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
	} else {
		// gateway-lite 模式下账号已经强制归入分组；system recommended（系统推荐）
		// key 不带固定调度分组时，应从平台全量可调度账号池中选择，而不是只查未分组账号。
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	}
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}
	return s.filterOpenAIAccountsByGrokSchedulingThreshold(ctx, accounts), nil
}

func (s *OpenAIGatewayService) filterOpenAIAccountsByGrokSchedulingThreshold(ctx context.Context, accounts []Account) []Account {
	if s == nil || s.rateLimitService == nil || len(accounts) == 0 {
		return accounts
	}
	filtered := make([]Account, 0, len(accounts))
	for index := range accounts {
		if s.rateLimitService.ApplyGrokSchedulingThreshold(ctx, &accounts[index]) {
			continue
		}
		filtered = append(filtered, accounts[index])
	}
	return filtered
}

func (s *OpenAIGatewayService) tryAcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (*AcquireResult, error) {
	if s.concurrencyService == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
}

func (s *OpenAIGatewayService) tryAcquireFirstAvailableAccountSlot(ctx context.Context, candidates []AccountWithConcurrency) (int64, *AcquireResult, error) {
	if len(candidates) == 0 {
		return 0, &AcquireResult{Acquired: false}, nil
	}
	if s.concurrencyService == nil {
		return candidates[0].ID, &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireFirstAvailableAccountSlot(ctx, candidates)
}

func (s *OpenAIGatewayService) resolveFreshSchedulableOpenAIAccount(ctx context.Context, account *Account, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, platformOverride ...string) *Account {
	return s.resolveFreshSchedulableOpenAIAccountForGroup(ctx, account, nil, requestedModel, requireCompact, requiredCapability, platformOverride...)
}

// resolveFreshSchedulableOpenAIAccountForGroup revalidates a cached scheduler
// candidate without losing the account-group mapping that made it eligible.
// The previous helper remains for legacy and unbound call sites.
func (s *OpenAIGatewayService) resolveFreshSchedulableOpenAIAccountForGroup(ctx context.Context, account *Account, groupID *int64, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, platformOverride ...string) *Account {
	if account == nil {
		return nil
	}
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}

	fresh := account
	if s.schedulerSnapshot != nil {
		current, err := s.getSchedulableAccount(ctx, account.ID)
		if err != nil || current == nil {
			return nil
		}
		fresh = current
	}

	if !isOpenAICompatibleAccountEligibleForGroupRequest(ctx, fresh, groupID, platform, requestedModel, requireCompact, requiredCapability, s.rateLimitService) {
		return nil
	}
	if !s.isShadowParentHealthy(ctx, fresh) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlockedForGroup(fresh, groupID, requestedModel) {
		return nil
	}
	if s.isOpenAIProxyStreamQuarantined(ctx, fresh) {
		return nil
	}
	return fresh
}

func (s *OpenAIGatewayService) recheckSelectedOpenAIAccountFromDB(ctx context.Context, account *Account, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, platformOverride ...string) *Account {
	return s.recheckSelectedOpenAIAccountFromDBForGroup(ctx, account, nil, requestedModel, requireCompact, requiredCapability, platformOverride...)
}

// recheckSelectedOpenAIAccountFromDBForGroup mirrors the final DB/snapshot
// safety check while preserving per-group model eligibility and health state.
func (s *OpenAIGatewayService) recheckSelectedOpenAIAccountFromDBForGroup(ctx context.Context, account *Account, groupID *int64, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, platformOverride ...string) *Account {
	if account == nil {
		return nil
	}
	platform := PlatformOpenAI
	if len(platformOverride) > 0 {
		platform = normalizeOpenAICompatiblePlatform(platformOverride[0])
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeGatewayLite {
		return s.resolveFreshSchedulableOpenAIAccountForGroup(ctx, account, groupID, requestedModel, requireCompact, requiredCapability, platform)
	}
	if s.schedulerSnapshot == nil || s.accountRepo == nil {
		if !isOpenAICompatibleAccountEligibleForGroupRequest(ctx, account, groupID, platform, requestedModel, requireCompact, requiredCapability, s.rateLimitService) {
			return nil
		}
		if !s.isShadowParentHealthy(ctx, account) {
			return nil
		}
		return account
	}

	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || latest == nil {
		return nil
	}
	if !isOpenAICompatibleAccountEligibleForGroupRequest(ctx, latest, groupID, platform, requestedModel, requireCompact, requiredCapability, s.rateLimitService) {
		return nil
	}
	if !s.isShadowParentHealthy(ctx, latest) {
		return nil
	}
	if s.isOpenAIAccountRequestRuntimeBlockedForGroup(latest, groupID, requestedModel) {
		return nil
	}
	if s.isOpenAIProxyStreamQuarantined(ctx, latest) {
		return nil
	}
	return latest
}

func (s *OpenAIGatewayService) getSchedulableAccount(ctx context.Context, accountID int64) (*Account, error) {
	var (
		account *Account
		err     error
	)
	if s.schedulerSnapshot != nil {
		account, err = s.getOpenAISelectionAccount(ctx, accountID)
	} else {
		account, err = s.accountRepo.GetByID(ctx, accountID)
	}
	if err != nil || account == nil {
		return account, err
	}
	if s.rateLimitService != nil && s.rateLimitService.ApplyGrokSchedulingThreshold(ctx, account) {
		return nil, nil
	}
	return account, nil
}

func (s *OpenAIGatewayService) hydrateSelectedAccount(ctx context.Context, account *Account) (*Account, error) {
	if account == nil {
		return account, nil
	}
	hydrated := account
	if s.schedulerSnapshot != nil {
		var err error
		hydrated, err = s.getOpenAISelectionAccount(ctx, account.ID)
		if err != nil {
			return nil, err
		}
		if hydrated == nil {
			return nil, fmt.Errorf("selected openai account %d not found during hydration", account.ID)
		}
	}
	if hydrated.IsCredentialShadow() {
		runtimeAccount, _, err := resolveShadowRuntimeAccount(ctx, s.accountRepo, hydrated)
		if err != nil {
			return nil, err
		}
		return runtimeAccount, nil
	}
	return hydrated, nil
}

// isShadowParentHealthy 使用调度快照读母账号健康度，不在候选筛选中增加数据库往返。
func (s *OpenAIGatewayService) isShadowParentHealthy(ctx context.Context, account *Account) bool {
	if account == nil || !account.IsCredentialShadow() {
		return true
	}
	return parentHealthyForShadow(account, func(parentID int64) *Account {
		if s.schedulerSnapshot != nil {
			parent, err := s.getOpenAISelectionAccount(ctx, parentID)
			if err == nil {
				return parent
			}
			return nil
		}
		parent, err := s.accountRepo.GetByID(ctx, parentID)
		if err != nil {
			return nil
		}
		return parent
	})
}

func (s *OpenAIGatewayService) getOpenAISelectionAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.schedulerSnapshot == nil {
		return nil, nil
	}
	requestCache := openAISelectionSnapshotCacheFromContext(ctx)
	if requestCache == nil {
		return s.schedulerSnapshot.GetAccount(ctx, accountID)
	}
	requestCache.mu.Lock()
	defer requestCache.mu.Unlock()
	if read, ok := requestCache.accounts[accountID]; ok {
		return read.account, read.err
	}
	account, err := s.schedulerSnapshot.GetAccount(ctx, accountID)
	requestCache.accounts[accountID] = openAISelectionAccountRead{account: account, err: err}
	return account, err
}

func (s *OpenAIGatewayService) getOpenAISelectionGroup(ctx context.Context, groupID int64) (*Group, error) {
	if s == nil || s.schedulerSnapshot == nil {
		return nil, nil
	}
	requestCache := openAISelectionSnapshotCacheFromContext(ctx)
	if requestCache == nil {
		return s.schedulerSnapshot.GetGroupByID(ctx, groupID)
	}
	requestCache.mu.Lock()
	defer requestCache.mu.Unlock()
	if read, ok := requestCache.groups[groupID]; ok {
		return read.group, read.err
	}
	group, err := s.schedulerSnapshot.GetGroupByID(ctx, groupID)
	requestCache.groups[groupID] = openAISelectionGroupRead{group: group, err: err}
	return group, err
}

func (s *OpenAIGatewayService) resolveOpenAIGroupModelForSelection(ctx context.Context, groupID *int64, model string) (context.Context, string, error) {
	if groupID == nil || *groupID <= 0 || strings.TrimSpace(model) == "" {
		return ctx, strings.TrimSpace(model), nil
	}
	group := finalMappingGroupFromContext(ctx, *groupID)
	if group == nil {
		var err error
		group, err = s.getOpenAISelectionGroup(ctx, *groupID)
		if err != nil {
			return ctx, model, err
		}
	}
	if group != nil {
		ctx = context.WithValue(ctx, ctxkey.Group, group)
	}
	ctx, model = resolveGroupModelForSelection(ctx, group, model)
	return ctx, model, nil
}

func (s *OpenAIGatewayService) newSelectionResult(ctx context.Context, account *Account, acquired bool, release func(), waitPlan *AccountWaitPlan) (*AccountSelectionResult, error) {
	hydrated, err := s.hydrateSelectedAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	return &AccountSelectionResult{
		Account:     hydrated,
		Acquired:    acquired,
		ReleaseFunc: release,
		WaitPlan:    waitPlan,
	}, nil
}

func (s *OpenAIGatewayService) newAcquiredSelectionResult(ctx context.Context, account *Account, release func()) (*AccountSelectionResult, error) {
	selection, err := s.newSelectionResult(ctx, account, true, release, nil)
	if err != nil && release != nil {
		release()
	}
	return selection, err
}

func (s *OpenAIGatewayService) schedulingConfig(ctx context.Context) config.GatewaySchedulingConfig {
	if s.cfg != nil {
		cfg := s.cfg.Gateway.Scheduling
		return s.applyFallbackQueueSettings(ctx, cfg)
	}
	return s.applyFallbackQueueSettings(ctx, config.GatewaySchedulingConfig{
		StickySessionMaxWaiting:  3,
		StickySessionWaitTimeout: 45 * time.Second,
		FallbackWaitTimeout:      30 * time.Second,
		FallbackQueueEnabled:     true,
		FallbackMaxWaiting:       100,
		LoadBatchEnabled:         true,
		SlotCleanupInterval:      30 * time.Second,
	})
}

func (s *OpenAIGatewayService) applyFallbackQueueSettings(ctx context.Context, cfg config.GatewaySchedulingConfig) config.GatewaySchedulingConfig {
	if s == nil || s.settingService == nil {
		return cfg
	}
	settings, err := s.settingService.GetFallbackQueueSettings(ctx)
	if err != nil || settings == nil {
		return cfg
	}
	cfg.FallbackQueueEnabled = settings.Enabled
	cfg.FallbackMaxWaiting = settings.MaxWaiting
	if settings.WaitTimeoutSeconds > 0 {
		cfg.FallbackWaitTimeout = time.Duration(settings.WaitTimeoutSeconds) * time.Second
	}
	return cfg
}

func (s *OpenAIGatewayService) canUseFallbackQueue(ctx context.Context, cfg config.GatewaySchedulingConfig, accountID int64) bool {
	if !cfg.FallbackQueueEnabled {
		return false
	}
	if cfg.FallbackMaxWaiting <= 0 || s == nil || s.concurrencyService == nil {
		return cfg.FallbackMaxWaiting > 0
	}
	waitingCount, err := s.concurrencyService.GetAccountWaitingCount(ctx, accountID)
	if err != nil {
		return true
	}
	return waitingCount < cfg.FallbackMaxWaiting
}

func (s *OpenAIGatewayService) isFallbackQueueFull(ctx context.Context, cfg config.GatewaySchedulingConfig, accountID int64) bool {
	if !cfg.FallbackQueueEnabled || cfg.FallbackMaxWaiting <= 0 || s == nil || s.concurrencyService == nil {
		return false
	}
	waitingCount, err := s.concurrencyService.GetAccountWaitingCount(ctx, accountID)
	return err == nil && waitingCount >= cfg.FallbackMaxWaiting
}

// GetAccessToken gets the access token for an OpenAI account
func (s *OpenAIGatewayService) GetAccessToken(ctx context.Context, account *Account) (string, string, error) {
	if account == nil {
		return "", "", errors.New("account is nil")
	}
	if account.IsCredentialShadow() {
		credentialAccount := account.credentialParent
		if credentialAccount == nil {
			var err error
			credentialAccount, err = resolveCredentialAccount(ctx, s.accountRepo, account)
			if err != nil {
				return "", "", err
			}
		}
		account = credentialAccount
	}
	if account.Platform == PlatformGrok {
		switch account.Type {
		case AccountTypeOAuth:
			if refreshedToken := strings.TrimSpace(grokRefreshedAccessTokenFromContext(ctx)); refreshedToken != "" {
				return refreshedToken, "oauth", nil
			}
			if s.grokTokenProvider != nil {
				accessToken, err := s.grokTokenProvider.GetAccessToken(ctx, account)
				if err != nil {
					return "", "", err
				}
				return accessToken, "oauth", nil
			}
			accessToken := account.GetGrokAccessToken()
			if strings.TrimSpace(accessToken) == "" {
				return "", "", errors.New("grok access_token not found in credentials")
			}
			return accessToken, "oauth", nil
		case AccountTypeAPIKey:
			apiKey := strings.TrimSpace(account.GetCredential("api_key"))
			if apiKey == "" {
				return "", "", errors.New("grok api_key not found in credentials")
			}
			return apiKey, "apikey", nil
		default:
			return "", "", fmt.Errorf("unsupported grok account type: %s", account.Type)
		}
	}
	if account.IsCNProvider() {
		apiKey := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	}
	switch account.Type {
	case AccountTypeOAuth:
		// Agent Identity authenticates with a per-request signed assertion and
		// intentionally has no bearer token or refresh lifecycle.
		if account.IsOpenAIAgentIdentity() {
			return "", "agent_identity", nil
		}
		// 使用 TokenProvider 获取缓存的 token
		if s.openAITokenProvider != nil {
			accessToken, err := s.openAITokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return "", "", err
			}
			return accessToken, "oauth", nil
		}
		// 降级：TokenProvider 未配置时直接从账号读取
		accessToken := account.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case AccountTypeAPIKey:
		apiKey := account.GetOpenAIApiKey()
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	default:
		return "", "", fmt.Errorf("unsupported account type: %s", account.Type)
	}
}

func (s *OpenAIGatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 402, 403, 405, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func (s *OpenAIGatewayService) shouldFailoverOpenAIUpstreamResponse(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if isOpenAIRequestScopedContentPolicyError(statusCode, upstreamBody) {
		return false
	}
	if isOpenAIContextWindowError(upstreamMsg, upstreamBody) {
		return false
	}
	if s.shouldFailoverUpstreamError(statusCode) {
		return true
	}
	return isOpenAITransientProcessingError(statusCode, upstreamMsg, upstreamBody)
}

func marshalOpenAIUpstreamJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}

func openAIUpstreamErrorBodyReadLimitForConfig(cfg *config.Config) int64 {
	limit := openAIUpstreamErrorBodyReadLimit
	if cfg != nil && cfg.Gateway.LogUpstreamErrorBody && cfg.Gateway.LogUpstreamErrorBodyMaxBytes > int(limit) {
		limit = int64(cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
	}
	return limit
}

func (s *OpenAIGatewayService) readUpstreamErrorBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	cfg := (*config.Config)(nil)
	if s != nil {
		cfg = s.cfg
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, openAIUpstreamErrorBodyReadLimitForConfig(cfg)))
	return body
}

func (s *OpenAIGatewayService) handleFailoverSideEffects(ctx context.Context, resp *http.Response, account *Account, responseBody []byte, requestedModel ...string) bool {
	if len(requestedModel) > 0 {
		return s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody, requestedModel[0])
	}
	return s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody)
}

// Forward forwards request to OpenAI API
func (s *OpenAIGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	if next, err := s.ensureInitialOAuthTextFunding(ctx, c, account, body, gjson.GetBytes(body, "model").String()); err != nil {
		return nil, err
	} else {
		ctx = next
	}
	startTime := time.Now()
	requestedReasoningEffort := requestedReasoningEffortFromContextOrBody(ctx, body, "reasoning.effort", "reasoning_effort")
	if account != nil && account.IsCredentialShadow() && account.credentialParent == nil {
		runtimeAccount, _, err := resolveShadowRuntimeAccount(ctx, s.accountRepo, account)
		if err != nil {
			return nil, err
		}
		account = runtimeAccount
	}
	// 固定渠道映射后的请求级 canonical body；账号 normalize/strip 不得改写跨 failover hint。
	canonicalImageIntentBody := body

	restrictionResult := s.detectCodexClientRestriction(c, account)
	apiKeyID := getAPIKeyIDFromContext(c)
	logCodexCLIOnlyDetection(ctx, c, account, apiKeyID, restrictionResult, body)
	if restrictionResult.Enabled && !restrictionResult.Matched {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "forbidden_error",
				"message": "This account only allows Codex official clients",
			},
		})
		return nil, errors.New("codex_cli_only restriction: only codex official clients are allowed")
	}

	if sanitizedBody, sanitized, sanitizeErr := SanitizeOpenAIUndefinedSentinelRequestBody(body); sanitizeErr != nil {
		setOpsUpstreamError(c, http.StatusBadRequest, sanitizeErr.Error(), "")
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "Invalid request body"}})
		return nil, sanitizeErr
	} else if sanitized {
		body = sanitizedBody
	}
	// Codex Desktop 会把未设置的 parameters.type 序列化成 null；在分流前
	// 一次性修正，避免坏 Schema 随历史请求在账号池中反复触发 400。
	toolBody, toolChanged, toolErr := sanitizeOpenAIResponsesToolParameterTypes(body)
	if toolErr != nil {
		return nil, fmt.Errorf("sanitize OpenAI Responses tool parameters: %w", toolErr)
	}
	if toolChanged {
		body = toolBody
	}
	responsesLite := account != nil && account.IsOpenAI() && isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader))
	if responsesLite {
		liteBody, changed, liteErr := normalizeOpenAIResponsesLitePayloadForAccount(body, account)
		if liteErr != nil {
			param := "tools"
			var validationErr *openAIResponsesLiteValidationError
			if errors.As(liteErr, &validationErr) {
				param = validationErr.param
			}
			setOpsUpstreamError(c, http.StatusBadRequest, liteErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type": "invalid_request_error", "message": liteErr.Error(), "param": param,
			}})
			return nil, liteErr
		}
		if changed {
			body = liteBody
		}
	}
	// 同一客户端请求发生账号故障切换时，Gin context 会被复用。每次尝试先
	// 清除上一账号的映射，再由当前账号和传输协议决定是否重新记录。
	clearOpenAIResponsesNamespaceNames(c)

	originalBody := body
	requestView := newOpenAIRequestView(body)
	reqModel, reqStream, promptCacheKey := requestView.Model, requestView.Stream, requestView.PromptCacheKey
	// Keep the model requested by the client immutable throughout this attempt.
	// Input normalization and account/compact mappings may update reqModel for
	// routing, but must never change the model name exposed in the response.
	clientRequestedModel := strings.TrimSpace(reqModel)
	if requested, ok := clientRequestedResponseModel(ctx); ok {
		clientRequestedModel = requested
	}
	beginClientResponseModelPolicy(c, ctx, account, clientRequestedModel)
	if account.Platform == PlatformGrok {
		_ = promptCacheKey
		return s.forwardGrokResponses(ctx, c, account, body, clientRequestedModel, reqStream, startTime)
	}

	if account.IsAnthropicProtocol() {
		return s.forwardResponsesViaNativeAnthropic(ctx, c, account, body, reqModel)
	}

	if shouldForwardOpenAIResponsesViaRawChatCompletions(account) {
		return s.forwardResponsesViaRawChatCompletions(ctx, c, account, body)
	}
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey {
		sanitizedBody, changed, sanitizeErr := sanitizeOpenAIResponsesInputItemIDs(body)
		if sanitizeErr != nil {
			return nil, fmt.Errorf("sanitize OpenAI Responses input item IDs: %w", sanitizeErr)
		}
		if changed {
			body = sanitizedBody
			originalBody = sanitizedBody
			requestView = newOpenAIRequestView(sanitizedBody)
			reqModel, reqStream, promptCacheKey = requestView.Model, requestView.Stream, requestView.PromptCacheKey
		}
	}

	compatMessagesBridge := isOpenAICompatMessagesBridgeBody(body)
	setOpenAICompatMessagesBridgeContext(c, compatMessagesBridge)

	isCodexCLI := openai.IsCodexOfficialClientByHeaders(c.GetHeader("User-Agent"), c.GetHeader("originator")) || (s.cfg != nil && s.cfg.Gateway.ForceCodexCLI)
	codexImageGenerationExplicitToolPolicy := codexImageGenerationExplicitToolPolicyAllow
	if isCodexCLI {
		codexImageGenerationExplicitToolPolicy = account.CodexImageGenerationExplicitToolPolicy()
	}
	wsDecision := s.getOpenAIWSProtocolResolver().Resolve(account)
	clientTransport := GetOpenAIClientTransport(c)
	// 默认仅允许 WS 入站请求走 WS 上游，避免 HTTP -> WS 协议混用；
	// 开启 http_client_ws_bridge_enabled 后，HTTP 客户端也允许按账号 WS 开关
	// 走 WS 上游（网关负责协议转换，失败时回落同账号 HTTP）。
	wsDecision = resolveOpenAIWSDecisionByClientTransport(wsDecision, clientTransport, s.openAIHTTPClientWSBridgeEnabled())
	if c != nil {
		c.Set("openai_ws_transport_decision", string(wsDecision.Transport))
		c.Set("openai_ws_transport_reason", wsDecision.Reason)
	}
	if wsDecision.Transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
		logOpenAIWSModeDebug(
			"selected account_id=%d account_type=%s transport=%s reason=%s model=%s stream=%v",
			account.ID,
			account.Type,
			normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
			normalizeOpenAIWSLogValue(wsDecision.Reason),
			reqModel,
			reqStream,
		)
	}
	// 当前仅支持 WSv2；WSv1 命中时直接返回错误，避免出现“配置可开但行为不确定”。
	if wsDecision.Transport == OpenAIUpstreamTransportResponsesWebsocket {
		if c != nil {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "OpenAI WSv1 is temporarily unsupported. Please enable responses_websockets_v2.",
				},
			})
		}
		return nil, errors.New("openai ws v1 is temporarily unsupported; use ws v2")
	}
	passthroughEnabled := account.IsOpenAIPassthroughEnabled()
	compactPath := isOpenAIResponsesCompactPath(c)
	if shouldFlattenOpenAIResponsesNamespaces(account, wsDecision.Transport, passthroughEnabled, compactPath) {
		flattenedBody, flattenErr := flattenOpenAIResponsesNamespaces(c, body)
		if flattenErr != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, flattenErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type": "invalid_request_error", "message": flattenErr.Error(), "param": "tools",
			}})
			return nil, flattenErr
		}
		if !bytes.Equal(flattenedBody, body) {
			body = flattenedBody
			originalBody = body
			requestView = newOpenAIRequestView(body)
			reqModel, reqStream, promptCacheKey = requestView.Model, requestView.Stream, requestView.PromptCacheKey
		}
	}
	if shouldStripOpenAIResponsesInputNamespaces(account, wsDecision.Transport, passthroughEnabled) {
		keepToolCallNamespaces := shouldKeepOpenAIResponsesToolCallNamespaces(account, wsDecision.Transport, passthroughEnabled, compactPath)
		strippedBody, stripErr := stripOpenAIResponsesInputNamespaces(body, keepToolCallNamespaces)
		if stripErr != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, stripErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type": "invalid_request_error", "message": stripErr.Error(), "param": "input",
			}})
			return nil, stripErr
		}
		if !bytes.Equal(strippedBody, body) {
			body = strippedBody
			originalBody = body
			requestView = newOpenAIRequestView(body)
			reqModel, reqStream, promptCacheKey = requestView.Model, requestView.Stream, requestView.PromptCacheKey
		}
	}
	// 工具名归一化：上游（OpenAI Responses API / Codex 后端）只接受
	// ^[a-zA-Z0-9_-]{1,64}$，客户端常见的 MCP 风格名（fs.read_file、mcp__srv.tool）
	// 与中文名会被上游 400 拒绝（Invalid 'input[7].name': string does not match pattern）。
	// 这里把声明与引用一起改成合法名并记下反向映射，响应侧再还原成原名。
	// 解析失败时 fail-open（保持原 body）：这种修复不能让本来能成功的请求变成失败。
	if normalizedToolBody, reverse, toolNameChanged, toolNameErr := normalizeOpenAIResponsesToolNamesBody(body); toolNameErr != nil {
		logger.LegacyPrintf("service.openai_gateway", "Warning: normalize OpenAI tool names skipped: %v", toolNameErr)
	} else if toolNameChanged {
		body = normalizedToolBody
		originalBody = body
		requestView = newOpenAIRequestView(body)
		reqModel, reqStream, promptCacheKey = requestView.Model, requestView.Stream, requestView.PromptCacheKey
		// 与 WS session 的映射合并，避免覆盖其它帧已登记的映射。
		mergeCodexToolNameReverse(c, reverse)
	}
	if passthroughEnabled {
		attemptImageIntentInvalidated := false
		if isCodexCLI && codexImageGenerationExplicitToolPolicy == codexImageGenerationExplicitToolPolicyStrip {
			strippedBody, changed, stripErr := stripOpenAIImageGenerationToolsFromRawPayload(body)
			if stripErr != nil {
				return nil, stripErr
			}
			if changed {
				body = strippedBody
				originalBody = strippedBody
				attemptImageIntentInvalidated = true
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Stripped /responses image_generation tool for Codex client by account policy")
			}
		}
		// 透传分支只需要轻量提取字段，避免热路径全量 Unmarshal。这里的
		// reasoningEffort 仅用于首字超时选择；用量审计仍从最终 wire body 取值。
		reasoningEffort := extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort")
		result, forwardErr := s.forwardOpenAIPassthrough(
			ctx,
			c,
			account,
			originalBody,
			canonicalImageIntentBody,
			reqModel,
			attemptImageIntentInvalidated,
			reasoningEffort,
			reqStream,
			startTime,
		)
		if result != nil {
			result.RequestedReasoningEffort = cloneOptionalString(requestedReasoningEffort)
		}
		return result, forwardErr
	}

	bodyModified := false
	prioritySettings := s.openAIFastPolicySettings(ctx)
	priorityDecision := OpenAIPriorityDecision{
		PolicyAction:               BetaPolicyActionPass,
		DetectionByRequestEnabled:  prioritySettings.DetectionByRequestEnabled,
		DetectionByResponseEnabled: prioritySettings.DetectionByResponseEnabled,
	}
	var reqBody map[string]any
	ensureReqBody := func() (map[string]any, error) {
		if requestView.HasPatches() {
			patchedBody, patchErr := requestView.ApplyPatches()
			if patchErr != nil {
				return nil, patchErr
			}
			body = patchedBody
			requestView = newOpenAIRequestView(body)
			reqBody = nil
			bodyModified = false
		}
		if reqBody != nil {
			return reqBody, nil
		}
		decoded, decodeErr := requestView.Decode(c)
		if decodeErr != nil {
			return nil, decodeErr
		}
		reqBody = decoded
		return reqBody, nil
	}
	markPatchSet := func(path string, value any) {
		bodyModified = true
		if requestView.patchesDisabled {
			if reqBody != nil {
				setOpenAIRequestMapPath(reqBody, path, value)
			}
			return
		}
		requestView.MarkPatchSet(path, value)
	}
	markPatchDelete := func(path string) {
		bodyModified = true
		if requestView.patchesDisabled {
			if reqBody != nil {
				deleteOpenAIRequestMapPath(reqBody, path)
			}
			return
		}
		requestView.MarkPatchDelete(path)
	}
	disablePatch := func() {
		requestView.DisablePatches()
	}
	markDecodedModified := func() {
		bodyModified = true
		disablePatch()
	}

	apiKey := getAPIKeyFromContext(c)
	imageGenerationAllowed := GroupAllowsImageGeneration(nil)
	if apiKey != nil {
		imageGenerationAllowed = GroupAllowsImageGeneration(apiKey.Group)
	}
	codexImageGenerationBridgeEnabled := isCodexCLI &&
		!isOpenAIResponsesLiteHeader(c.GetHeader(responsesLiteHeader)) &&
		imageGenerationAllowed &&
		codexImageGenerationExplicitToolPolicy != codexImageGenerationExplicitToolPolicyStrip &&
		s.isCodexImageGenerationBridgeEnabled(ctx, account, apiKey)
	var imageIntent bool
	canonicalImageIntent := resolveOpenAIImageIntentHint(c, reqModel, canonicalImageIntentBody, IsImageGenerationIntent)
	if isCodexCLI && codexImageGenerationExplicitToolPolicy == codexImageGenerationExplicitToolPolicyStrip {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if stripOpenAIImageGenerationTools(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Stripped /responses image_generation tool for Codex client by account policy")
		}
		imageIntent = IsImageGenerationIntentMap(openAIResponsesEndpoint, reqModel, decoded)
	} else {
		imageIntent = canonicalImageIntent
	}
	if imageIntent && !imageGenerationAllowed {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": ImageGenerationPermissionMessage()}})
		return nil, errors.New("image generation disabled for group")
	}

	instructions := gjson.GetBytes(body, "instructions")
	instructionsEmpty := !instructions.Exists() || instructions.Type != gjson.String || strings.TrimSpace(instructions.String()) == ""
	// 只有 ChatGPT/Codex 协议账号缺 instructions 才合成默认值；API Key 账号
	// 走标准 Responses 协议，调用方省略 instructions 是合法语义，不能强插。
	if instructionsEmpty && account.UsesOpenAICodexProtocol() && !compatMessagesBridge {
		markPatchSet("instructions", defaultCodexSynthInstructions(reqModel))
	}

	billingModel := MappedAccountModelForRequest(ctx, nil, account, reqModel)
	if billingModel != reqModel {
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Model mapping applied: %s -> %s (account: %s, isCodexCLI: %v)", reqModel, billingModel, account.Name, isCodexCLI)
		reqModel = billingModel
		markPatchSet("model", billingModel)
	}
	upstreamModel := billingModel
	isCompactRequest := isOpenAIResponsesCompactPath(c)
	compactMapped := false
	if isCompactRequest {
		compactMappedModel := resolveOpenAICompactForwardModel(account, billingModel)
		if compactMappedModel != "" && compactMappedModel != billingModel {
			compactMapped = true
			upstreamModel = compactMappedModel
			reqModel = compactMappedModel
			markPatchSet("model", compactMappedModel)
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Compact model mapping applied: %s -> %s (account: %s, isCodexCLI: %v)", billingModel, compactMappedModel, account.Name, isCodexCLI)
		}
	}
	if !compactMapped {
		modelForNormalize := reqModel
		if modelForNormalize == "" {
			modelForNormalize = requestView.Model
		}
		upstreamModel = normalizeOpenAIModelForUpstream(account, modelForNormalize)
		if upstreamModel != "" && upstreamModel != modelForNormalize {
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Upstream model resolved: %s -> %s (account: %s, type: %s, isCodexCLI: %v)", modelForNormalize, upstreamModel, account.Name, account.Type, isCodexCLI)
			reqModel = upstreamModel
			markPatchSet("model", upstreamModel)
		}
	}
	if strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()) == "minimal" {
		markPatchSet("reasoning.effort", "none")
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Normalized reasoning.effort: minimal -> none (account: %s)", account.Name)
	}

	imageIntent = imageIntent || IsImageGenerationIntent(openAIResponsesEndpoint, reqModel, nil) || isOpenAIImageGenerationModel(upstreamModel)
	if imageIntent && !imageGenerationAllowed {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": ImageGenerationPermissionMessage()}})
		return nil, errors.New("image generation disabled for group")
	}

	// /responses/compact 是会话压缩请求，上游不接受 tool_choice，注入
	// image_generation 工具也没有意义，必须整体跳过图片桥接。
	if imageGenerationAllowed && !isCompactRequest && (codexImageGenerationBridgeEnabled || isOpenAIImageGenerationModel(requestView.Model) || openAIRequestBodyImageGenerationToolNeedsNormalization(body) || isOpenAIImageGenerationModel(upstreamModel)) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if codexImageGenerationBridgeEnabled && ensureOpenAIResponsesImageGenerationTool(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Injected /responses image_generation tool for Codex client")
		}
		if codexImageGenerationBridgeEnabled && ensureOpenAIResponsesImageGenerationToolChoiceAuto(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Set /responses image_generation tool_choice=auto for Codex client")
		}
		if normalizeOpenAIResponsesImageGenerationTools(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Normalized /responses image_generation tool payload")
		}
		if normalizeOpenAIResponsesImageOnlyModel(decoded) {
			markDecodedModified()
			if model, ok := decoded["model"].(string); ok {
				upstreamModel = strings.TrimSpace(model)
			}
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Normalized /responses image-only model request inbound_model=%s image_model=%s upstream_model=%s", requestView.Model, billingModel, upstreamModel)
		}
		if err := validateOpenAIResponsesImageModel(decoded, upstreamModel); err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error(), "param": "model"}})
			return nil, err
		}
		if hasOpenAIImageGenerationTool(decoded) {
			imageIntent = true
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] /responses image_generation request inbound_model=%s mapped_model=%s account_type=%s", requestView.Model, upstreamModel, account.Type)
		}
		if codexImageGenerationBridgeEnabled && applyCodexImageGenerationBridgeInstructions(decoded) {
			markDecodedModified()
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Added Codex image_generation bridge instructions")
		}
	} else if imageGenerationAllowed && imageIntent && openAIRequestBodyHasImageGenerationDeclaration(body) {
		// 完整 image_generation tool 只做 raw 计费读取，校验/桥接/旧字段迁移命中时才展开大 input map。
		logger.LegacyPrintf("service.openai_gateway", "[OpenAI] /responses image_generation request inbound_model=%s mapped_model=%s account_type=%s", requestView.Model, upstreamModel, account.Type)
	}

	if isCodexSparkModel(upstreamModel) && openAIRequestBodyMayContainImageInput(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err := validateCodexSparkInput(decoded, upstreamModel); err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error(), "param": "input"}})
			return nil, err
		}
	}

	// gpt-5.3-codex-spark 上游会拒绝 image_generation 工具。这里在 APIKey
	// 和 OAuth /responses 分支前统一剥离，避免 Codex 客户端默认工具导致 400/502。
	if isCodexSparkModel(upstreamModel) && openAIRequestBodyHasImageGenerationDeclaration(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if stripCodexSparkImageGenerationTools(decoded) {
			markDecodedModified()
		}
	}

	if account.Type == AccountTypeOAuth {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		codexResult := codexTransformResult{}
		if compatMessagesBridge {
			codexResult = applyCodexOAuthTransformWithOptions(decoded, codexOAuthTransformOptions{IsCodexCLI: isCodexCLI, IsCompact: isCompactRequest, SkipDefaultInstructions: true, PreserveToolCallIDs: true})
			ensureCodexOAuthInstructionsField(decoded)
			markDecodedModified()
		} else {
			codexResult = applyCodexOAuthTransform(decoded, isCodexCLI, isCompactRequest)
		}
		if codexResult.Modified {
			markDecodedModified()
		}
		// 带真实 device_id 时补齐 client_metadata 安装标识，与真实 Codex 对齐（compact 形态不同，跳过）。
		if !isCompactRequest && applyCodexClientMetadata(decoded, account) {
			markDecodedModified()
		}
		if !isCompactRequest {
			var headers http.Header
			if c != nil && c.Request != nil {
				headers = c.Request.Header
			}
			fingerprintIDs := resolveCodexFingerprintIDsFromRequest(account, headers)
			if fingerprintIDs != nil {
				if applyCodexFingerprintClientMetadata(decoded, fingerprintIDs) {
					markDecodedModified()
				}
			}
			// 必须含 nil 一并覆盖：同一请求 failover 到 off 账号时，不得沿用
			// 上一个账号的收敛 ID。
			stageCodexFingerprintIDs(c, fingerprintIDs)
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
	}

	if !SupportsVerbosity(upstreamModel) && gjson.GetBytes(body, "text.verbosity").Exists() {
		markPatchDelete("text.verbosity")
	}

	if !isCodexCLI {
		maxOutputTokens := gjson.GetBytes(body, "max_output_tokens")
		// 原生 CN Responses 端点（DeepSeek / Kimi）按标准协议接受 max_output_tokens。
		preserveDeepSeekResponsesMaxTokens := account.UsesNativeCNResponses() || preserveFundingResponsesOutputLimit(ctx, account)
		if maxOutputTokens.Exists() && !preserveDeepSeekResponsesMaxTokens {
			switch account.Platform {
			case PlatformOpenAI:
				if account.Type == AccountTypeAPIKey {
					markPatchDelete("max_output_tokens")
				}
			case PlatformAnthropic:
				decoded, decodeErr := ensureReqBody()
				if decodeErr != nil {
					return nil, decodeErr
				}
				delete(decoded, "max_output_tokens")
				if _, hasMaxTokens := decoded["max_tokens"]; !hasMaxTokens {
					decoded["max_tokens"] = maxOutputTokens.Value()
				}
				markDecodedModified()
			case PlatformGemini:
				markPatchDelete("max_output_tokens")
			default:
				markPatchDelete("max_output_tokens")
			}
		}
		if gjson.GetBytes(body, "max_completion_tokens").Exists() && (account.Type == AccountTypeAPIKey || account.Platform != PlatformOpenAI) {
			markPatchDelete("max_completion_tokens")
		}
		for _, unsupportedField := range []string{"prompt_cache_retention", "safety_identifier"} {
			if gjson.GetBytes(body, unsupportedField).Exists() {
				markPatchDelete(unsupportedField)
			}
		}
	}
	if wsDecision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 && gjson.GetBytes(body, "previous_response_id").Exists() {
		markPatchDelete("previous_response_id")
	}
	if openAIRequestBodyMayContainEmptyBase64InputImage(body) {
		decoded, decodeErr := ensureReqBody()
		if decodeErr != nil {
			return nil, decodeErr
		}
		if sanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(decoded) {
			markDecodedModified()
		}
	}

	if imageIntent {
		requestedAudit := strings.ToLower(strings.TrimSpace(requestView.ServiceTier))
		var requested *string
		if requestedAudit != "" {
			requested = &requestedAudit
		}
		priorityDecision = OpenAIPriorityDecision{
			RequestedServiceTier:       requested,
			PolicyAction:               BetaPolicyActionPass,
			DetectionByRequestEnabled:  prioritySettings.DetectionByRequestEnabled,
			DetectionByResponseEnabled: prioritySettings.DetectionByResponseEnabled,
		}
	} else {
		decidedPriority, priorityErr := s.decideOpenAIPriorityPolicy(ctx, account, upstreamModel, requestView.ServiceTier)
		if priorityErr != nil {
			var blocked *OpenAIFastBlockedError
			if errors.As(priorityErr, &blocked) {
				writeOpenAIFastPolicyBlockedResponse(c, blocked)
			}
			return nil, priorityErr
		}
		priorityDecision = decidedPriority
		if priorityErr := s.validateOpenAIPriorityInputContext(ctx, upstreamModel, body, &priorityDecision); priorityErr != nil {
			var blocked *OpenAIFastBlockedError
			if errors.As(priorityErr, &blocked) {
				writeOpenAIFastPolicyBlockedResponse(c, blocked)
			}
			return nil, priorityErr
		}
		serviceTierFieldExists := gjson.GetBytes(body, "service_tier").Exists()
		if priorityDecision.ServiceTier == nil {
			if serviceTierFieldExists {
				if normalizeOpenAIServiceTier(requestView.ServiceTier) == nil {
					priorityDecision.PolicyAction = "filter_invalid"
					priorityDecision.FallbackReason = "invalid_service_tier"
				}
				markPatchDelete("service_tier")
			}
		} else if requestView.ServiceTier != *priorityDecision.ServiceTier || !serviceTierFieldExists {
			markPatchSet("service_tier", *priorityDecision.ServiceTier)
		}
	}

	if bodyModified {
		if requestView.HasPatches() {
			if patchedBody, patchErr := requestView.ApplyPatches(); patchErr == nil {
				body = patchedBody
				requestView = newOpenAIRequestView(body)
				reqBody = nil
				bodyModified = false
			}
		}
		if bodyModified {
			decoded, decodeErr := ensureReqBody()
			if decodeErr != nil {
				return nil, decodeErr
			}
			var marshalErr error
			body, marshalErr = marshalOpenAIUpstreamJSON(decoded)
			if marshalErr != nil {
				return nil, fmt.Errorf("serialize request body: %w", marshalErr)
			}
			requestView = newOpenAIRequestView(body)
		}
	}
	imageBillingModel := ""
	imageSizeTier := ""
	imageInputSize := ""
	if imageIntent {
		var imageCfg OpenAIResponsesImageBillingConfig
		var imageCfgErr error
		if reqBody != nil {
			imageCfg, imageCfgErr = resolveOpenAIResponsesImageBillingConfigDetailed(reqBody, billingModel)
		} else {
			imageCfg, imageCfgErr = resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, billingModel)
		}
		if imageCfgErr != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, imageCfgErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": imageCfgErr.Error(), "param": "size"}})
			return nil, imageCfgErr
		}
		imageBillingModel = imageCfg.Model
		imageSizeTier = imageCfg.SizeTier
		imageInputSize = imageCfg.InputSize
	}

	// Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 命中 WS 时仅走 WebSocket Mode；不再自动回退 HTTP。
	if wsDecision.Transport == OpenAIUpstreamTransportResponsesWebsocketV2 {
		// WS 分支需要结构化 payload 与重连恢复，命中后再触发 full-map decode。
		wsReqBody, err := ensureReqBody()
		if err != nil {
			return nil, err
		}
		_, hasPreviousResponseID := wsReqBody["previous_response_id"]
		logOpenAIWSModeDebug(
			"forward_start account_id=%d account_type=%s model=%s stream=%v has_previous_response_id=%v",
			account.ID,
			account.Type,
			upstreamModel,
			reqStream,
			hasPreviousResponseID,
		)
		maxAttempts := openAIWSReconnectRetryLimit + 1
		wsAttempts := 0
		var wsResult *OpenAIForwardResult
		var wsErr error
		wsLastFailureReason := ""
		wsPrevResponseRecoveryTried := false
		wsInvalidEncryptedContentRecoveryTried := false
		recoverPrevResponseNotFound := func(attempt int) bool {
			if wsPrevResponseRecoveryTried {
				return false
			}
			previousResponseID := openAIWSPayloadString(wsReqBody, "previous_response_id")
			if previousResponseID == "" {
				logOpenAIWSModeInfo(
					"reconnect_prev_response_recovery_skip account_id=%d attempt=%d reason=missing_previous_response_id previous_response_id_present=false",
					account.ID,
					attempt,
				)
				return false
			}
			if HasFunctionCallOutput(wsReqBody) {
				logOpenAIWSModeInfo(
					"reconnect_prev_response_recovery_skip account_id=%d attempt=%d reason=has_function_call_output previous_response_id_present=true",
					account.ID,
					attempt,
				)
				return false
			}
			delete(wsReqBody, "previous_response_id")
			wsPrevResponseRecoveryTried = true
			logOpenAIWSModeInfo(
				"reconnect_prev_response_recovery account_id=%d attempt=%d action=drop_previous_response_id retry=1 previous_response_id=%s previous_response_id_kind=%s",
				account.ID,
				attempt,
				truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(ClassifyOpenAIPreviousResponseIDKind(previousResponseID)),
			)
			return true
		}
		recoverInvalidEncryptedContent := func(attempt int) bool {
			if wsInvalidEncryptedContentRecoveryTried {
				return false
			}
			removedReasoningItems := trimOpenAIEncryptedReasoningItems(wsReqBody)
			if !removedReasoningItems {
				logOpenAIWSModeInfo(
					"reconnect_invalid_encrypted_content_recovery_skip account_id=%d attempt=%d reason=missing_encrypted_reasoning_items",
					account.ID,
					attempt,
				)
				return false
			}
			previousResponseID := openAIWSPayloadString(wsReqBody, "previous_response_id")
			hasFunctionCallOutput := HasFunctionCallOutput(wsReqBody)
			if previousResponseID != "" && !hasFunctionCallOutput {
				delete(wsReqBody, "previous_response_id")
			}
			wsInvalidEncryptedContentRecoveryTried = true
			logOpenAIWSModeInfo(
				"reconnect_invalid_encrypted_content_recovery account_id=%d attempt=%d action=drop_encrypted_reasoning_items retry=1 previous_response_id_present=%v previous_response_id=%s previous_response_id_kind=%s has_function_call_output=%v dropped_previous_response_id=%v",
				account.ID,
				attempt,
				previousResponseID != "",
				truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(ClassifyOpenAIPreviousResponseIDKind(previousResponseID)),
				hasFunctionCallOutput,
				previousResponseID != "" && !hasFunctionCallOutput,
			)
			return true
		}
		retryBudget := s.openAIWSRetryTotalBudget()
		retryStartedAt := time.Now()
	wsRetryLoop:
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			wsAttempts = attempt
			// 记录本次 WS 尝试实际使用的模型；失败可能发生在
			// ForwardResult 生成前，错误日志仍需保留映射后的模型。
			SetOpsUpstreamModel(c, upstreamModel)
			wsResult, wsErr = s.forwardOpenAIWSV2(
				ctx,
				c,
				account,
				wsReqBody,
				token,
				wsDecision,
				isCodexCLI,
				reqStream,
				clientRequestedModel,
				upstreamModel,
				startTime,
				attempt,
				wsLastFailureReason,
			)
			if wsErr == nil {
				break
			}
			if c != nil && c.Writer != nil && c.Writer.Written() {
				break
			}

			reason, retryable := classifyOpenAIWSReconnectReason(wsErr)
			if reason != "" {
				wsLastFailureReason = reason
			}
			if reason == "service_tier_unsupported" && openAIPriorityCanRetryStandard(ctx, priorityDecision) {
				logOpenAIPriorityStandardRetry(ctx, account, upstreamModel, reason)
				return s.Forward(contextWithOpenAIPriorityStandardRetry(ctx, priorityDecision, reason), c, account, originalBody)
			}
			// previous_response_not_found 说明续链锚点不可用：
			// 对非 function_call_output 场景，允许一次“去掉 previous_response_id 后重放”。
			if reason == "previous_response_not_found" && recoverPrevResponseNotFound(attempt) {
				continue
			}
			if reason == "invalid_encrypted_content" && recoverInvalidEncryptedContent(attempt) {
				continue
			}
			if retryable && attempt < maxAttempts {
				backoff := s.openAIWSRetryBackoff(attempt)
				if retryBudget > 0 && time.Since(retryStartedAt)+backoff > retryBudget {
					s.recordOpenAIWSRetryExhausted()
					logOpenAIWSModeInfo(
						"reconnect_budget_exhausted account_id=%d attempts=%d max_retries=%d reason=%s elapsed_ms=%d budget_ms=%d",
						account.ID,
						attempt,
						openAIWSReconnectRetryLimit,
						normalizeOpenAIWSLogValue(reason),
						time.Since(retryStartedAt).Milliseconds(),
						retryBudget.Milliseconds(),
					)
					break
				}
				s.recordOpenAIWSRetryAttempt(backoff)
				logOpenAIWSModeInfo(
					"reconnect_retry account_id=%d retry=%d max_retries=%d reason=%s backoff_ms=%d",
					account.ID,
					attempt,
					openAIWSReconnectRetryLimit,
					normalizeOpenAIWSLogValue(reason),
					backoff.Milliseconds(),
				)
				if backoff > 0 {
					timer := time.NewTimer(backoff)
					select {
					case <-ctx.Done():
						if !timer.Stop() {
							<-timer.C
						}
						wsErr = wrapOpenAIWSFallback("retry_backoff_canceled", ctx.Err())
						break wsRetryLoop
					case <-timer.C:
					}
				}
				continue
			}
			if retryable {
				s.recordOpenAIWSRetryExhausted()
				logOpenAIWSModeInfo(
					"reconnect_exhausted account_id=%d attempts=%d max_retries=%d reason=%s",
					account.ID,
					attempt,
					openAIWSReconnectRetryLimit,
					normalizeOpenAIWSLogValue(reason),
				)
			} else if reason != "" {
				s.recordOpenAIWSNonRetryableFastFallback()
				logOpenAIWSModeInfo(
					"reconnect_stop account_id=%d attempt=%d reason=%s",
					account.ID,
					attempt,
					normalizeOpenAIWSLogValue(reason),
				)
			}
			break
		}
		if wsErr == nil {
			firstTokenMs := int64(0)
			hasFirstTokenMs := wsResult != nil && wsResult.FirstTokenMs != nil
			if hasFirstTokenMs {
				firstTokenMs = int64(*wsResult.FirstTokenMs)
			}
			requestID := ""
			if wsResult != nil {
				requestID = strings.TrimSpace(wsResult.RequestID)
			}
			logOpenAIWSModeDebug(
				"forward_succeeded account_id=%d request_id=%s stream=%v has_first_token_ms=%v first_token_ms=%d ws_attempts=%d",
				account.ID,
				requestID,
				reqStream,
				hasFirstTokenMs,
				firstTokenMs,
				wsAttempts,
			)
			wsResult.UpstreamModel = upstreamModel
			// WS forwarder 看到的是最终真正写入长连接的 payload，必须保留它
			// 已采集的实际上游档次；外层 HTTP body 只可补客户原始值，不能覆盖。
			if wsResult.RequestedReasoningEffort == nil {
				wsResult.RequestedReasoningEffort = cloneOptionalString(requestedReasoningEffort)
			}
			observedServiceTier := wsResult.ServiceTier
			applyOpenAIPriorityDecisionToResult(wsResult, priorityDecision, observedServiceTier)
			logOpenAIServiceTierOverride(ctx, account, upstreamModel, priorityDecision.ServiceTier, observedServiceTier)
			if wsResult.ImageCount > 0 {
				wsResult.ImageSize = imageSizeTier
				wsResult.ImageInputSize = imageInputSize
				wsResult.BillingModel = imageBillingModel
			}
			return wsResult, nil
		}
		// HTTP 客户端桥接模式：WS 失败且尚未向客户端写出任何字节时，
		// 回落到下方同账号 HTTP 循环重放原始 body（WS 分支只改动解码后的
		// map，不污染 body 字节），保证开启桥接后可用性不降级；
		// 已写出字节（流中途失败）无法安全重放，保持原错误返回。
		if !s.shouldOpenAIWSBridgeFallbackToHTTP(c, clientTransport, wsErr) {
			s.writeOpenAIWSFallbackErrorResponse(c, account, wsErr)
			return nil, wsErr
		}
		fallbackReason, _ := classifyOpenAIWSReconnectReason(wsErr)
		logOpenAIWSModeInfo(
			"ws_http_bridge_fallback account_id=%d client_transport=%s reason=%s detail=%s",
			account.ID,
			string(clientTransport),
			normalizeOpenAIWSLogValue(fallbackReason),
			truncateOpenAIWSLogValue(wsErr.Error(), 200),
		)
	}

	httpInvalidEncryptedContentRetryTried := false
	agentIdentityTaskRetryTried := false
	rejectedFieldRetryState := newOpenAIResponsesRejectedFieldRetryState(body)
	for {
		// Build upstream request
		upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
		upstreamReq, err := s.buildUpstreamRequest(upstreamCtx, c, account, body, token, reqStream, promptCacheKey, isCodexCLI)
		releaseUpstreamCtx()
		if err != nil {
			appendOpenAIResponsesRejectedFieldRetryEvent(c, account, rejectedFieldRetryState, "field_retry_failed", 0, "", "request_build_failed")
			return nil, err
		}

		// Get proxy URL：账号配置了代理池（extra.proxy_pool）时按最小在途
		// 选择出口，使多代理并发展示与流量均衡有真实数据来源。
		proxyURL := s.egressProxyURL(account)

		// Send request
		SetOpsUpstreamModel(c, upstreamModel)
		upstreamStart := time.Now()
		resp, err := s.doUpstreamRequest(upstreamReq, proxyURL, account)
		if resp != nil {
			// [2026-09 上游规则变更] 240s 寿命下用一次就换票：把本次响应重铸的
			// turn-state 回收进记账，滚动续命；Cookie 捕获已统一在 doUpstreamRequest。
			s.noteOpenAICodexTurnStateRefresh(account, upstreamModel, extractOpenAICodexTurnState(resp.Header))
		}
		SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
		if err != nil {
			appendOpenAIResponsesRejectedFieldRetryEvent(c, account, rejectedFieldRetryState, "field_retry_failed", 0, "", "transport_failed")
			// Transport-level failure (proxy/DNS/TCP/TLS — no HTTP response). Convert to
			// a failover so the handler switches to a healthy account, and temporarily
			// unschedule the account on durable faults (e.g. rejected proxy credentials).
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
		}

		// Handle error response
		if resp.StatusCode >= 400 {
			respBody := s.readUpstreamErrorBody(resp)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			if account.IsOpenAIAgentIdentity() && !agentIdentityTaskRetryTried && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, respBody) {
				expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
				if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
					return nil, fmt.Errorf("agent identity task recovery failed: %w", recoverErr)
				}
				agentIdentityTaskRetryTried = true
				continue
			}

			upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
			upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
			upstreamCode := extractUpstreamErrorCode(respBody)
			if rejectedFieldRetryState.attempted() {
				appendOpenAIResponsesRejectedFieldRetryEvent(
					c, account, rejectedFieldRetryState, "field_retry_failed", resp.StatusCode,
					resp.Header.Get("x-request-id"), "followup_rejected",
				)
			}
			if openAIServiceTierUnsupportedResponse(resp.StatusCode, upstreamCode, upstreamMsg, respBody) && openAIPriorityCanRetryStandard(ctx, priorityDecision) {
				logOpenAIPriorityStandardRetry(ctx, account, upstreamModel, "service_tier_unsupported")
				return s.Forward(contextWithOpenAIPriorityStandardRetry(ctx, priorityDecision, "service_tier_unsupported"), c, account, originalBody)
			}
			if !httpInvalidEncryptedContentRetryTried && resp.StatusCode == http.StatusBadRequest && upstreamCode == "invalid_encrypted_content" {
				decoded, decodeErr := ensureReqBody()
				if decodeErr != nil {
					return nil, decodeErr
				}
				if trimOpenAIEncryptedReasoningItems(decoded) {
					body, err = marshalOpenAIUpstreamJSON(decoded)
					if err != nil {
						return nil, fmt.Errorf("serialize invalid_encrypted_content retry body: %w", err)
					}
					httpInvalidEncryptedContentRetryTried = true
					rejectedFieldRetryState.remember(body)
					logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Retrying non-WSv2 request once after invalid_encrypted_content (account: %s)", account.Name)
					continue
				}
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI] Skip non-WSv2 invalid_encrypted_content retry because encrypted reasoning items are missing (account: %s)", account.Name)
			}
			if decision, changed, retryErr := normalizeOpenAIResponsesRejectedFieldRetryBody(resp.StatusCode, body, respBody); retryErr != nil {
				return nil, fmt.Errorf("normalize rejected Responses field retry body: %w", retryErr)
			} else if changed && (c == nil || c.Writer == nil || !c.Writer.Written()) && rejectedFieldRetryState.Allow(decision) {
				body = decision.Body
				requestView = newOpenAIRequestView(body)
				reqBody = nil
				appendOpenAIResponsesRejectedFieldRetryEvent(
					c, account, rejectedFieldRetryState, "field_retry_started", resp.StatusCode,
					resp.Header.Get("x-request-id"), "retrying",
				)
				logger.LegacyPrintf(
					"service.openai_gateway",
					"[OpenAI] Retrying non-WSv2 request once after allowlisted field rejection field=%s rule=%s account=%s",
					decision.Field,
					decision.Rule,
					account.Name,
				)
				continue
			}
			// 请求体上限是账号/上游代理能力差异。只允许尚未向客户端输出的
			// 可重放 JSON Responses 请求切换到不同账号；不调用账号健康副作用，
			// 避免把大请求限制误判为账号、凭据或模型故障。
			if isOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, respBody) &&
				(c == nil || c.Writer == nil || !c.Writer.Written()) {
				upstreamDetail := ""
				if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
					maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
					if maxBytes <= 0 {
						maxBytes = 2048
					}
					upstreamDetail = truncateString(string(respBody), maxBytes)
				}
				setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					ProxyHost:          proxyHostForAccount(account),
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					Kind:               "failover",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})
				return nil, newOpenAIRequestBodyTooLargeFailoverError(resp.StatusCode, resp.Header, respBody)
			}
			ordinaryFailover := s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody)
			if failoverErr := s.buildOpenAIHTTPFailoverError(ctx, c, account, resp.StatusCode, resp.Header, respBody, upstreamMsg, upstreamModel, ordinaryFailover); failoverErr != nil {
				upstreamDetail := ""
				if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
					maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
					if maxBytes <= 0 {
						maxBytes = 2048
					}
					upstreamDetail = truncateString(string(respBody), maxBytes)
				}
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					ProxyHost:          proxyHostForAccount(account),
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					Kind:               "failover",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				return nil, failoverErr
			}
			return s.handleErrorResponse(ctx, resp, c, account, body, billingModel)
		}
		appendOpenAIResponsesRejectedFieldRetryEvent(
			c, account, rejectedFieldRetryState, "field_retry_succeeded", resp.StatusCode,
			resp.Header.Get("x-request-id"), "accepted",
		)
		defer func() { _ = resp.Body.Close() }()

		finalReasoningEffort := extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort")
		sentServiceTier := priorityDecision.ServiceTier
		var observedServiceTier *string
		// 上游接受后只保留计费需要的标量，避免响应处理期间继续保活完整 input/tools map。
		reqBody = nil

		// Handle normal response
		var usage *OpenAIUsage
		beginUpstreamResponseModelObservation(c)
		var firstTokenMs *int
		var fakeFirstTokenMs *int
		responseID := ""
		imageCount := 0
		var imageOutputSizes []string
		if reqStream {
			streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, clientRequestedModel, upstreamModel, finalReasoningEffort)
			if err != nil {
				return nil, err
			}
			usage = streamResult.usage
			firstTokenMs = streamResult.firstTokenMs
			fakeFirstTokenMs = streamResult.fakeFirstTokenMs
			responseID = strings.TrimSpace(streamResult.responseID)
			observedServiceTier = streamResult.serviceTier
			imageCount = streamResult.imageCount
			imageOutputSizes = streamResult.imageOutputSizes
		} else {
			nonStreamResult, err := s.handleNonStreamingResponse(ctx, resp, c, account, clientRequestedModel, upstreamModel)
			if err != nil {
				return nil, err
			}
			usage = nonStreamResult.usage
			responseID = strings.TrimSpace(nonStreamResult.responseID)
			observedServiceTier = nonStreamResult.serviceTier
			imageCount = nonStreamResult.imageCount
			imageOutputSizes = nonStreamResult.imageOutputSizes
		}
		s.bindHTTPResponseAccount(ctx, c, account, responseID)

		// Extract and save Codex usage snapshot from response headers (for OAuth accounts)
		if account.Type == AccountTypeOAuth {
			if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
				s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
			}
		}

		if usage == nil {
			usage = &OpenAIUsage{}
		}
		logOpenAIServiceTierOverride(ctx, account, upstreamModel, sentServiceTier, observedServiceTier)

		forwardResult := &OpenAIForwardResult{
			RequestID:                     resp.Header.Get("x-request-id"),
			ResponseID:                    responseID,
			Usage:                         *usage,
			Model:                         clientRequestedModel,
			UpstreamModel:                 upstreamModel,
			UpstreamResponseModel:         observedUpstreamResponseModel(c),
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			RequestedReasoningEffort:      requestedReasoningEffort,
			ReasoningEffort:               finalReasoningEffort,
			Stream:                        reqStream,
			OpenAIWSMode:                  false,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  firstTokenMs,
			FakeFirstTokenMs:              fakeFirstTokenMs,
		}
		forwardResult.UpstreamModelMismatch = upstreamModelMismatch(upstreamModel, forwardResult.UpstreamResponseModel)
		priorityDecision.ServiceTier = cloneOptionalString(sentServiceTier)
		applyOpenAIPriorityDecisionToResult(forwardResult, priorityDecision, observedServiceTier)
		if imageCount > 0 {
			forwardResult.ImageCount = imageCount
			forwardResult.ImageSize = imageSizeTier
			forwardResult.ImageInputSize = imageInputSize
			forwardResult.ImageOutputSizes = imageOutputSizes
			forwardResult.BillingModel = imageBillingModel
		}
		return forwardResult, nil
	}
}

func shouldForwardOpenAIResponsesViaRawChatCompletions(account *Account) bool {
	if account == nil || account.Type != AccountTypeAPIKey {
		return false
	}
	if account.IsCNProvider() {
		switch account.GetAPIProtocol() {
		case APIProtocolChatCompletions:
			return true
		case APIProtocolAdaptive:
			return !account.SupportsNativeCNResponses()
		default:
			return false
		}
	}
	return !openai_compat.ShouldUseResponsesAPI(account.Extra)
}

func (s *OpenAIGatewayService) forwardOpenAIPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	canonicalImageIntentBody []byte,
	reqModel string,
	attemptImageIntentInvalidated bool,
	reasoningEffort *string,
	reqStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	// passthrough（透传）只表示协议与未声明字段尽量原样保留；管理员显式
	// 配置的账号/分组模型映射仍必须落到真实 wire body，否则调度健康键、
	// 渠道限制判断和上游实际模型会互相矛盾。
	mappedPassthroughModel := MappedAccountModelForRequest(ctx, nil, account, reqModel)
	upstreamPassthroughModel := ""
	if mappedPassthroughModel != "" && mappedPassthroughModel != reqModel {
		nextBody, setErr := sjson.SetBytes(body, "model", mappedPassthroughModel)
		if setErr != nil {
			return nil, fmt.Errorf("set passthrough account mapped model: %w", setErr)
		}
		body = nextBody
		upstreamPassthroughModel = mappedPassthroughModel
		attemptImageIntentInvalidated = true
	}
	if isOpenAIResponsesCompactPath(c) {
		modelBeforeCompact := firstNonEmpty(upstreamPassthroughModel, reqModel)
		compactMappedModel := resolveOpenAICompactForwardModel(account, modelBeforeCompact)
		if compactMappedModel != "" && compactMappedModel != modelBeforeCompact {
			nextBody, setErr := sjson.SetBytes(body, "model", compactMappedModel)
			if setErr != nil {
				return nil, fmt.Errorf("set compact passthrough model: %w", setErr)
			}
			body = nextBody
			upstreamPassthroughModel = compactMappedModel
			attemptImageIntentInvalidated = true
		}
	}
	// 标准档重试必须复用已经完成账号/分组映射的请求体，不能退回用户原模型。
	priorityRetryBody := append([]byte(nil), body...)

	strippedUndefinedBody, strippedUndefined, stripErr := stripOpenAITopLevelUndefinedStringFields(body)
	if stripErr != nil {
		return nil, stripErr
	}
	if strippedUndefined {
		body = strippedUndefinedBody
	}

	if account != nil && account.Type == AccountTypeOAuth {
		normalizedInstructionsBody, instructionsNormalized, instructionsErr := normalizeOpenAIPassthroughInstructions(body, reqModel)
		if instructionsErr != nil {
			return nil, instructionsErr
		}
		if instructionsNormalized {
			body = normalizedInstructionsBody
		}
		if rejectReason := detectOpenAIPassthroughInstructionsRejectReason(reqModel, body); rejectReason != "" {
			rejectMsg := "OpenAI codex passthrough requires a non-empty instructions field"
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			logOpenAIPassthroughInstructionsRejected(ctx, c, account, reqModel, rejectReason, body)
			c.JSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"type":    "forbidden_error",
					"message": rejectMsg,
				},
			})
			return nil, fmt.Errorf("openai passthrough rejected before upstream: %s", rejectReason)
		}

		normalizedBody, normalized, err := normalizeOpenAIPassthroughOAuthBody(body, isOpenAIResponsesCompactPath(c))
		if err != nil {
			return nil, err
		}
		if normalized {
			body = normalizedBody
		}
		reqStream = gjson.GetBytes(body, "stream").Bool()

		// 透传路径也必须让请求头和 client_metadata 使用同一组收敛 ID。
		// 仅解码 client_metadata 小对象，避免对大请求体做完整 Unmarshal。
		if !isOpenAIResponsesCompactPath(c) {
			var clientHeaders http.Header
			if c != nil && c.Request != nil {
				clientHeaders = c.Request.Header
			}
			fingerprintIDs := resolveCodexFingerprintIDsFromRequest(account, clientHeaders)
			if fingerprintIDs != nil {
				fingerprintBody, changed, fingerprintErr := applyCodexFingerprintClientMetadataRaw(body, fingerprintIDs)
				if fingerprintErr != nil {
					return nil, fingerprintErr
				}
				if changed {
					body = fingerprintBody
				}
			}
			stageCodexFingerprintIDs(c, fingerprintIDs)
		}
	}

	sanitizedBody, sanitized, err := sanitizeEmptyBase64InputImagesInOpenAIBody(body)
	if err != nil {
		return nil, err
	}
	if sanitized {
		body = sanitizedBody
	}

	// Apply OpenAI fast policy to the passthrough body (filter/block by service_tier).
	// 统一使用 upstream 视角的 model：透传路径下 body 已经过 compact 映射 +
	// OAuth normalize，body 中的 model 字段即上游真正会看到的 slug。
	// 这样可以与 chat-completions / messages / native /responses 入口的
	// upstreamModel 保持一致，避免 whitelist 命中差异。当 body 中没有
	// model 字段时退回 reqModel。
	policyModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if policyModel == "" {
		policyModel = reqModel
	}
	updatedBody, priorityDecision, policyErr := s.applyOpenAIPriorityPolicyToBody(ctx, account, policyModel, body)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, policyErr
	}
	body = updatedBody

	// OpenAI API Key upstreams commonly accept only public function tools. Lower
	// Codex custom/tool_search/namespace tools immediately before the wire body
	// is built, then retain the request-scoped mapping for JSON and SSE restore.
	// OAuth keeps its native tool schema and must not enter this compatibility path.
	var clientToolMapping apicompat.ResponsesClientToolMapping
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeAPIKey {
		adaptedBody, mapping, adaptErr := adaptResponsesClientToolsForFunctionUpstream(body, "OpenAI API Key")
		if adaptErr != nil {
			return nil, fmt.Errorf("adapt OpenAI API Key client tools: %w", adaptErr)
		}
		body = adaptedBody
		clientToolMapping = mapping
		if hasResponsesClientToolMapping(mapping) {
			setOpenAIResponsesClientToolMapping(c, mapping)
		} else if previousMapping, ok := openAIResponsesClientToolMapping(c); ok {
			// A retry after an allowlisted upstream-field rejection may carry the
			// already lowered function-tool body. Keep the first mapping alive so
			// the retry response still uses the client tool contract.
			clientToolMapping = previousMapping
		} else {
			clearOpenAIResponsesClientToolMapping(c)
		}
	}

	apiKey := getAPIKeyFromContext(c)
	// 同一 attempt 的最终 model/body 只判定一次，权限检查与后续图片状态设置共用该结果。
	imageIntent := resolveOpenAIPassthroughImageIntent(
		c,
		reqModel,
		canonicalImageIntentBody,
		policyModel,
		body,
		attemptImageIntentInvalidated,
		IsImageGenerationIntent,
	)
	if imageIntent && !GroupAllowsImageGeneration(apiKeyGroup(apiKey)) {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalFeatureGate)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "permission_error",
				"message": ImageGenerationPermissionMessage(),
			},
		})
		return nil, errors.New("image generation disabled for group")
	}
	imageBillingModel := ""
	imageSizeTier := ""
	imageInputSize := ""
	if imageIntent {
		var imageCfgErr error
		imageCfg, imageCfgErr := resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, reqModel)
		if imageCfgErr != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, imageCfgErr.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": imageCfgErr.Error(),
					"param":   "size",
				},
			})
			return nil, imageCfgErr
		}
		imageBillingModel = imageCfg.Model
		imageSizeTier = imageCfg.SizeTier
		imageInputSize = imageCfg.InputSize
	}

	logger.LegacyPrintf("service.openai_gateway",
		"[OpenAI 自动透传] 命中自动透传分支: account=%d name=%s type=%s model=%s stream=%v",
		account.ID,
		account.Name,
		account.Type,
		reqModel,
		reqStream,
	)
	if reqStream && c != nil && c.Request != nil {
		if timeoutHeaders := collectOpenAIPassthroughTimeoutHeaders(c.Request.Header); len(timeoutHeaders) > 0 {
			streamWarnLogger := logger.FromContext(ctx).With(
				zap.String("component", "service.openai_gateway"),
				zap.Int64("account_id", account.ID),
				zap.Strings("timeout_headers", timeoutHeaders),
			)
			if s.isOpenAIPassthroughTimeoutHeadersAllowed() {
				streamWarnLogger.Warn("OpenAI passthrough 透传请求包含超时相关请求头，且当前配置为放行，可能导致上游提前断流")
			} else {
				streamWarnLogger.Warn("OpenAI passthrough 检测到超时相关请求头，将按配置过滤以降低断流风险")
			}
		}
	}

	// Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := s.buildUpstreamRequestOpenAIPassthrough(upstreamCtx, c, account, body, token)
	releaseUpstreamCtx()
	if err != nil {
		return nil, err
	}

	// 透传路径同样走代理池最小在途选择。
	proxyURL := s.egressProxyURL(account)

	if c != nil {
		c.Set("openai_passthrough", true)
	}

	SetOpsUpstreamModel(c, firstNonEmpty(upstreamPassthroughModel, strings.TrimSpace(gjson.GetBytes(body, "model").String()), reqModel))
	upstreamStart := time.Now()
	resp, err := s.doUpstreamRequest(upstreamReq, proxyURL, account)
	if resp != nil {
		// [2026-09 上游规则变更] 透传路径同样滚动续票；出站模型与 build 阶段
		// applyOpenAICodexTicket 读到的 body.model 同源。
		s.noteOpenAICodexTurnStateRefresh(account, firstNonEmpty(upstreamPassthroughModel, strings.TrimSpace(gjson.GetBytes(body, "model").String()), reqModel), extractOpenAICodexTurnState(resp.Header))
	}
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(upstreamStart).Milliseconds())
	if err != nil {
		// Transport-level failure (proxy/DNS/TCP/TLS — no HTTP response). Convert to
		// a failover so the handler switches to a healthy account, and temporarily
		// unschedule the account on durable faults (e.g. rejected proxy credentials).
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, true)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		respBody = s.redactAgentIdentitySensitiveBody(ctx, account, respBody)
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
		upstreamCode := extractUpstreamErrorCode(respBody)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if account.IsOpenAIAgentIdentity() && !agentIdentityTaskRecoveryWasTried(ctx) && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, respBody) {
			expectedTaskID := strings.TrimSpace(account.GetCredential("task_id"))
			if recoverErr := s.recoverAgentIdentityTask(ctx, account, expectedTaskID); recoverErr != nil {
				return nil, fmt.Errorf("agent identity task recovery failed: %w", recoverErr)
			}
			return s.forwardOpenAIPassthrough(
				markAgentIdentityTaskRecoveryTried(ctx), c, account, body,
				canonicalImageIntentBody, reqModel, attemptImageIntentInvalidated,
				reasoningEffort, reqStream, startTime,
			)
		}
		if openAIServiceTierUnsupportedResponse(resp.StatusCode, upstreamCode, upstreamMsg, respBody) && openAIPriorityCanRetryStandard(ctx, priorityDecision) {
			logOpenAIPriorityStandardRetry(ctx, account, policyModel, "service_tier_unsupported")
			return s.forwardOpenAIPassthrough(
				contextWithOpenAIPriorityStandardRetry(ctx, priorityDecision, "service_tier_unsupported"),
				c,
				account,
				priorityRetryBody,
				canonicalImageIntentBody,
				reqModel,
				attemptImageIntentInvalidated,
				reasoningEffort,
				reqStream,
				startTime,
			)
		}
		// 透传分支仍然是已完整缓存在内存中的 JSON 请求，可跨账号重放；
		// Images multipart 不经过这里，因此不会因 413 被盲目复制和换号。
		if isOpenAIRequestBodyTooLargeError(resp.StatusCode, upstreamMsg, respBody) &&
			(c == nil || c.Writer == nil || !c.Writer.Written()) {
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:             account.Platform,
				AccountID:            account.ID,
				AccountName:          account.Name,
				ProxyHost:            proxyHostForAccount(account),
				UpstreamStatusCode:   resp.StatusCode,
				UpstreamRequestID:    resp.Header.Get("x-request-id"),
				Passthrough:          true,
				Kind:                 "failover",
				Message:              upstreamMsg,
				Detail:               upstreamDetail,
				UpstreamResponseBody: upstreamDetail,
			})
			return nil, newOpenAIRequestBodyTooLargeFailoverError(resp.StatusCode, resp.Header, respBody)
		}
		// 透传模式默认保持原样代理；但容量类、凭据类和上游 5xx 错误
		// 应先触发多账号 failover，避免单个坏账号直接把 502/503 暴露给客户。
		if shouldFailoverOpenAIPassthroughResponse(resp.StatusCode) {
			return nil, s.handleFailoverErrorResponsePassthrough(ctx, resp, c, account, body)
		}
		return nil, s.handleErrorResponsePassthrough(ctx, resp, c, account, body)
	}
	// A streaming handler may return an error after it already committed headers
	// and partial output. Record provenance in that case as well, but never for
	// a pre-output attempt that is discarded and retried on another account.
	turnState := extractOpenAICodexTurnState(resp.Header)
	if turnState != "" {
		defer func() {
			if c != nil && c.Writer != nil && c.Writer.Written() {
				s.noteOpenAICodexTurnStateProvenance(c, account)
			}
		}()
	}

	var usage *OpenAIUsage
	beginUpstreamResponseModelObservation(c)
	var firstTokenMs *int
	var fakeFirstTokenMs *int
	responseID := ""
	var observedServiceTier *string
	imageCount := 0
	var imageOutputSizes []string
	if reqStream {
		if hasResponsesClientToolMapping(clientToolMapping) {
			maxLineSize := defaultMaxLineSize
			if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
				maxLineSize = s.cfg.Gateway.MaxLineSize
			}
			resp.Body = newResponsesClientToolStreamBody(resp.Body, clientToolMapping, maxLineSize)
		}
		result, err := s.handleStreamingResponsePassthrough(ctx, resp, c, account, startTime, reqModel, upstreamPassthroughModel)
		if err != nil {
			return nil, err
		}
		usage = result.usage
		firstTokenMs = result.firstTokenMs
		fakeFirstTokenMs = result.fakeFirstTokenMs
		responseID = strings.TrimSpace(result.responseID)
		observedServiceTier = result.serviceTier
		imageCount = result.imageCount
		imageOutputSizes = result.imageOutputSizes
	} else {
		result, err := s.handleNonStreamingResponsePassthrough(ctx, resp, c, reqModel, upstreamPassthroughModel)
		if err != nil {
			return nil, err
		}
		usage = result.usage
		responseID = strings.TrimSpace(result.responseID)
		observedServiceTier = result.serviceTier
		imageCount = result.imageCount
		imageOutputSizes = result.imageOutputSizes
	}
	s.bindHTTPResponseAccount(ctx, c, account, responseID)

	if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
		s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
	}

	if usage == nil {
		usage = &OpenAIUsage{}
	}
	logOpenAIServiceTierOverride(ctx, account, upstreamPassthroughModel, priorityDecision.ServiceTier, observedServiceTier)

	forwardResult := &OpenAIForwardResult{
		RequestID:                     resp.Header.Get("x-request-id"),
		ResponseID:                    responseID,
		Usage:                         *usage,
		Model:                         reqModel,
		UpstreamModel:                 upstreamPassthroughModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		ReasoningEffort:               extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort"),
		Stream:                        reqStream,
		OpenAIWSMode:                  false,
		Duration:                      time.Since(startTime),
		FirstTokenMs:                  firstTokenMs,
		FakeFirstTokenMs:              fakeFirstTokenMs,
	}
	forwardResult.UpstreamModelMismatch = upstreamModelMismatch(firstNonEmpty(upstreamPassthroughModel, reqModel), forwardResult.UpstreamResponseModel)
	applyOpenAIPriorityDecisionToResult(forwardResult, priorityDecision, observedServiceTier)
	if imageCount > 0 {
		forwardResult.ImageCount = imageCount
		forwardResult.ImageSize = imageSizeTier
		forwardResult.ImageInputSize = imageInputSize
		forwardResult.ImageOutputSizes = imageOutputSizes
		forwardResult.BillingModel = imageBillingModel
	}
	return forwardResult, nil
}

func logOpenAIPassthroughInstructionsRejected(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	reqModel string,
	rejectReason string,
	body []byte,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	accountID := int64(0)
	accountName := ""
	accountType := ""
	if account != nil {
		accountID = account.ID
		accountName = strings.TrimSpace(account.Name)
		accountType = strings.TrimSpace(string(account.Type))
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.Int64("account_id", accountID),
		zap.String("account_name", accountName),
		zap.String("account_type", accountType),
		zap.String("request_model", strings.TrimSpace(reqModel)),
		zap.String("reject_reason", strings.TrimSpace(rejectReason)),
	}
	fields = appendCodexCLIOnlyRejectedRequestFields(fields, c, body)
	logger.FromContext(ctx).With(fields...).Warn("OpenAI passthrough 本地拦截：Codex 请求缺少有效 instructions")
}

func (s *OpenAIGatewayService) buildUpstreamRequestOpenAIPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	token string,
) (*http.Request, error) {
	targetURL := openaiPlatformAPIURL
	switch account.Type {
	case AccountTypeOAuth:
		targetURL = chatgptCodexURL
	case AccountTypeAPIKey:
		baseURL := account.GetOpenAIBaseURL()
		if account.UsesNativeCNResponses() && account.IsAdaptiveAPIProtocol() {
			baseURL = account.GetCNProtocolBaseURL(APIProtocolResponses)
		}
		if baseURL != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesURLForPlatform(account.Platform, validatedURL)
		}
	}
	targetURL = appendOpenAIResponsesRequestPathSuffix(targetURL, openAIResponsesRequestPathSuffix(c))
	body = normalizeDeepSeekResponsesRequestBody(account, body)
	// 按账号出口时区改写 body 里的 <timezone> 环境声明（与出口 IP 地理对齐，
	// 消除「客户端时区 + 机房出口 IP」的降智风控矛盾）。未配置时原样透传。
	body = rewriteUpstreamTimezoneFromAccount(account, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	// 透传客户端请求头（安全白名单）。
	allowTimeoutHeaders := s.isOpenAIPassthroughTimeoutHeadersAllowed()
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lower := strings.ToLower(strings.TrimSpace(key))
			if !isOpenAIPassthroughAllowedRequestHeader(lower, allowTimeoutHeaders) {
				continue
			}
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	// 客户端回带的 turn-state 只能返回其铸造账号；换号后先剥离。
	s.guardOpenAICodexTurnStateEcho(c, account, req.Header)

	if err := s.applyOpenAICodexTicket(ctx, account, extractOpenAICodexTicketModel(body), req.Header); err != nil {
		return nil, err
	}

	// 覆盖入站鉴权残留，并注入上游认证
	req.Header.Del("authorization")
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// OAuth 透传到 ChatGPT internal API 时补齐必要头。
	if account.Type == AccountTypeOAuth {
		// 新版 Codex OAuth HTTP 已不再协商旧 Responses 实验协议。透传入口可能
		// 收到旧客户端带来的该 token，只移除它，保留其他独立 beta 能力。
		stripOpenAILegacyResponsesBeta(req.Header)
		promptCacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
		req.Host = "chatgpt.com"
		if chatgptAccountID := account.GetChatGPTAccountID(); chatgptAccountID != "" {
			req.Header.Set("chatgpt-account-id", chatgptAccountID)
		}
		apiKeyID := getAPIKeyIDFromContext(c)
		// 先保存客户端原始值，再做 compact 补充，避免后续统一隔离时读到已处理的值。
		// Codex 新版使用连字符形式；旧客户端仍可能发送下划线形式。
		// 两者都只作为客户端会话种子，之后必须删除原值并写入按 API Key
		// 隔离后的 session_id，不能把跨用户的原始标识直接透传给上游。
		clientSessionID := strings.TrimSpace(req.Header.Get("session-id"))
		if clientSessionID == "" {
			clientSessionID = strings.TrimSpace(req.Header.Get("session_id"))
		}
		req.Header.Del("session-id")
		req.Header.Del("session_id")
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		if isOpenAIResponsesCompactPath(c) {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", codexCLIVersion)
			}
			if clientSessionID == "" {
				clientSessionID = resolveOpenAICompactSessionID(c)
			}
		} else if req.Header.Get("accept") == "" {
			req.Header.Set("accept", "text/event-stream")
		}
		if req.Header.Get("originator") == "" {
			req.Header.Set("originator", "codex_cli_rs")
		}
		// 用隔离后的 session 标识符覆盖客户端透传值，防止跨用户会话碰撞。
		if clientSessionID == "" {
			clientSessionID = promptCacheKey
		}
		if clientConversationID == "" {
			clientConversationID = promptCacheKey
		}
		if clientSessionID != "" {
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, clientSessionID))
		}
		if clientConversationID != "" {
			req.Header.Set("conversation_id", isolateOpenAISessionID(apiKeyID, clientConversationID))
		}
	}

	// 透传模式也支持账户自定义 User-Agent 与 ForceCodexCLI 兜底。
	customUA := account.GetOpenAIUserAgent()
	if customUA != "" {
		req.Header.Set("user-agent", customUA)
	}
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		req.Header.Set("user-agent", codexCLIUserAgent)
	}
	// 浏览器型 UA 兜底：仅 OAuth（ChatGPT 内部接口）账号生效，若最终 user-agent 仍为浏览器
	// （Chrome/Firefox/Safari/Edge 等），替换为后台配置的 Codex UA，避免 Cloudflare 触发 JS 质询。
	s.overrideBrowserUserAgent(ctx, account, req)

	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	account.ApplyHeaderOverrides(req.Header)
	if account.Type == AccountTypeOAuth {
		applyStagedCodexFingerprintHeaders(c, account, req.Header)
		enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	}
	applyOpenAICodexBetaFeatures(c, account, req.Header)
	setOpenAICodexRoutingHintFromBody(req.Header, account, body)

	return req, nil
}

func stripOpenAILegacyResponsesBeta(headers http.Header) {
	if headers == nil {
		return
	}

	preserved := make([]string, 0)
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), "OpenAI-Beta") {
			continue
		}
		delete(headers, key)
		for _, value := range values {
			parts := strings.Split(value, ",")
			kept := parts[:0]
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" || strings.EqualFold(part, "responses=experimental") {
					continue
				}
				kept = append(kept, part)
			}
			if len(kept) > 0 {
				preserved = append(preserved, strings.Join(kept, ", "))
			}
		}
	}
	for _, value := range preserved {
		headers.Add("OpenAI-Beta", value)
	}
}

func shouldFailoverOpenAIPassthroughResponse(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		529:
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) handleFailoverErrorResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
) error {
	body := s.readUpstreamErrorBody(resp)

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	logOpenAIInstructionsRequiredDebug(ctx, c, account, resp.StatusCode, upstreamMsg, requestBody, body)
	reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
	_ = s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, reqModel)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		ProxyHost:            proxyHostForAccount(account),
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          true,
		Kind:                 "failover",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})
	return &UpstreamFailoverError{
		StatusCode:             resp.StatusCode,
		ResponseBody:           body,
		ResponseHeaders:        resp.Header.Clone(),
		RetryableOnSameAccount: account != nil && account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
	}
}

func (s *OpenAIGatewayService) handleErrorResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
) error {
	MarkResponseCommitted(c)
	body := s.readUpstreamErrorBody(resp)

	// cyber_policy：透传账号本就把原始 body 回给客户端（下方 c.Data），此处仅打标记，
	// 供 handler 事后写风控/邮件。cyber 是上游网络安全策略拦截，不冷却账号，
	// 故下方跳过 handleOpenAIAccountUpstreamError（避免自定义 temp-unschedulable 规则误冷却）。
	cyberHit, cyberCode, cyberMsg := detectOpenAICyberPolicy(body)
	if cyberHit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           cyberCode,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	logOpenAIInstructionsRequiredDebug(ctx, c, account, resp.StatusCode, upstreamMsg, requestBody, body)
	// 透传模式保留原始上游错误响应，但运行态账号状态仍需更新，
	// 避免粘性路由继续复用刚被限流的账号。cyber 例外：不冷却账号。
	if !cyberHit {
		reqModel, _, _ := extractOpenAIRequestMetaFromBody(requestBody)
		_ = s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, reqModel)
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:             account.Platform,
		AccountID:            account.ID,
		AccountName:          account.Name,
		ProxyHost:            proxyHostForAccount(account),
		UpstreamStatusCode:   resp.StatusCode,
		UpstreamRequestID:    resp.Header.Get("x-request-id"),
		Passthrough:          true,
		Kind:                 "http_error",
		Message:              upstreamMsg,
		Detail:               upstreamDetail,
		UpstreamResponseBody: upstreamDetail,
	})

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", resp.StatusCode)
	}
	return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

func isOpenAIPassthroughAllowedRequestHeader(lowerKey string, allowTimeoutHeaders bool) bool {
	if lowerKey == "" {
		return false
	}
	if isOpenAIPassthroughTimeoutHeader(lowerKey) {
		return allowTimeoutHeaders
	}
	return openaiPassthroughAllowedHeaders[lowerKey]
}

func isOpenAIPassthroughTimeoutHeader(lowerKey string) bool {
	switch lowerKey {
	case "x-stainless-timeout", "x-stainless-read-timeout", "x-stainless-connect-timeout", "x-request-timeout", "request-timeout", "grpc-timeout":
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) isOpenAIPassthroughTimeoutHeadersAllowed() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIPassthroughAllowTimeoutHeaders
}

func collectOpenAIPassthroughTimeoutHeaders(h http.Header) []string {
	if h == nil {
		return nil
	}
	var matched []string
	for key, values := range h {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if isOpenAIPassthroughTimeoutHeader(lowerKey) {
			entry := lowerKey
			if len(values) > 0 {
				entry = fmt.Sprintf("%s=%s", lowerKey, strings.Join(values, "|"))
			}
			matched = append(matched, entry)
		}
	}
	sort.Strings(matched)
	return matched
}

type openaiStreamingResultPassthrough struct {
	usage            *OpenAIUsage
	firstTokenMs     *int
	fakeFirstTokenMs *int // 伪造首字（SSE 注释行）发出时刻；未开启/未触发为 nil
	responseID       string
	serviceTier      *string
	imageCount       int
	imageOutputSizes []string
}

type openaiNonStreamingResultPassthrough struct {
	*OpenAIUsage
	usage            *OpenAIUsage
	responseID       string
	serviceTier      *string
	imageCount       int
	imageOutputSizes []string
}

const openAIStreamKeepaliveBytesKey = "openai_stream_keepalive_bytes"

func recordOpenAIStreamKeepaliveBytes(c *gin.Context, written int) {
	if c == nil || written <= 0 {
		return
	}
	current := 0
	if value, ok := c.Get(openAIStreamKeepaliveBytesKey); ok {
		current, _ = value.(int)
	}
	c.Set(openAIStreamKeepaliveBytesKey, current+written)
}

func openAIStreamClientOutputStarted(c *gin.Context, localStarted bool) bool {
	if localStarted {
		return true
	}
	if c == nil || c.Writer == nil {
		return false
	}
	// Compact keepalive comments may commit HTTP 200 without model output. They
	// must not suppress a safe retry before the first semantic event.
	return OpenAICompactKeepaliveAdjustedWrittenSize(c) >= 0
}

func openAIStreamEventIsPreamble(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress":
		return true
	default:
		return false
	}
}

func openAIStreamAddedEventStartsClientOutput(payload []byte, eventType string) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return true
	}

	switch strings.TrimSpace(eventType) {
	case "response.output_item.added", "response.output_item.done":
		item := gjson.GetBytes(payload, "item")
		if !item.Exists() || !item.IsObject() {
			return true
		}
		switch strings.TrimSpace(item.Get("type").String()) {
		case "reasoning":
			if item.Get("encrypted_content").String() != "" {
				return true
			}
			summary := item.Get("summary")
			if !summary.IsArray() {
				return false
			}
			for _, part := range summary.Array() {
				if strings.TrimSpace(part.Get("type").String()) != "summary_text" || part.Get("text").String() != "" {
					return true
				}
			}
			return false
		case "message":
			content := item.Get("content")
			if !content.IsArray() {
				return false
			}
			for _, part := range content.Array() {
				switch strings.TrimSpace(part.Get("type").String()) {
				case "output_text":
					if part.Get("text").String() != "" {
						return true
					}
				case "refusal":
					if part.Get("refusal").String() != "" {
						return true
					}
				default:
					return true
				}
			}
			return false
		case "function_call":
			return item.Get("arguments").String() != ""
		case "custom_tool_call":
			return item.Get("input").String() != ""
		case "compaction":
			return item.Get("encrypted_content").String() != ""
		default:
			return true
		}
	case "response.content_part.added", "response.content_part.done":
		part := gjson.GetBytes(payload, "part")
		if !part.Exists() || !part.IsObject() {
			return true
		}
		switch strings.TrimSpace(part.Get("type").String()) {
		case "output_text":
			return part.Get("text").String() != ""
		case "refusal":
			return part.Get("refusal").String() != ""
		default:
			return true
		}
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part := gjson.GetBytes(payload, "part")
		if !part.Exists() || !part.IsObject() || strings.TrimSpace(part.Get("type").String()) != "summary_text" {
			return true
		}
		return part.Get("text").String() != ""
	default:
		return true
	}
}

func openAIStreamDataStartsClientOutput(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" {
		return false
	}
	switch strings.TrimSpace(eventType) {
	case "response.failed":
		return false
	case "error":
		payload := []byte(trimmed)
		return !openAIStreamFailedEventShouldFailover(payload, extractOpenAISSEErrorMessage(payload))
	case "response.output_item.added", "response.output_item.done",
		"response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		return openAIStreamAddedEventStartsClientOutput([]byte(trimmed), eventType)
	}
	// 空 delta/done 只表示协议结构进度，既不能停止首输出计时，也不能
	// 阻止过载帧安全换号；只有真正携带文本、工具参数、媒体或加密思考
	// 内容时才锁定当前 attempt（尝试）。
	if strings.HasSuffix(strings.TrimSpace(eventType), ".delta") ||
		strings.HasSuffix(strings.TrimSpace(eventType), ".done") {
		return openAIStreamDataStartsVisibleOutput(trimmed, eventType) ||
			gjson.Get(trimmed, "encrypted_content").String() != "" ||
			gjson.Get(trimmed, "item.encrypted_content").String() != ""
	}
	return !openAIStreamEventIsPreamble(eventType)
}

func openAIStreamItemHasVisibleOutput(item gjson.Result) bool {
	if item.Get("arguments").String() != "" || item.Get("input").String() != "" || item.Get("result").String() != "" {
		return true
	}
	for _, path := range []string{"content", "summary"} {
		for _, part := range item.Get(path).Array() {
			if part.Get("text").String() != "" || part.Get("transcript").String() != "" {
				return true
			}
		}
	}
	return false
}

// 结构进度仍可确认本次上游请求有效，但只有用户真正可见的文本、工具参数、
// 图片或音频内容才计入 TTFT（首个可见输出延迟）。
func openAIStreamDataStartsVisibleOutput(data, eventType string) bool {
	trimmed := strings.TrimSpace(data)
	if trimmed == "" || trimmed == "[DONE]" || !gjson.Valid(trimmed) {
		return false
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = strings.TrimSpace(gjson.Get(trimmed, "type").String())
	}
	if strings.HasSuffix(eventType, ".delta") {
		delta := gjson.Get(trimmed, "delta")
		return delta.Exists() && delta.String() != ""
	}
	switch eventType {
	case "response.output_text.done",
		"response.reasoning_summary_text.done",
		"response.reasoning_text.done",
		"response.audio_transcript.done":
		return gjson.Get(trimmed, "text").String() != ""
	case "response.function_call_arguments.done":
		return gjson.Get(trimmed, "arguments").String() != ""
	case "response.custom_tool_call_input.done":
		return gjson.Get(trimmed, "input").String() != ""
	case "response.image_generation_call.partial_image":
		return gjson.Get(trimmed, "partial_image_b64").String() != ""
	case "response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part := gjson.Get(trimmed, "part")
		return part.Get("text").String() != "" || part.Get("transcript").String() != ""
	case "response.output_item.added", "response.output_item.done":
		return openAIStreamItemHasVisibleOutput(gjson.Get(trimmed, "item"))
	case "response.completed", "response.done":
		for _, item := range gjson.Get(trimmed, "response.output").Array() {
			if openAIStreamItemHasVisibleOutput(item) {
				return true
			}
		}
	}
	return false
}

func openAIStreamFailedEventSemanticStatus(payload []byte, message string) int {
	if isOpenAIContextWindowError(message, payload) {
		return http.StatusBadRequest
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	combined := strings.TrimSpace(errType + " " + code + " " + strings.ToLower(strings.TrimSpace(message)))
	switch {
	case strings.Contains(combined, "rate_limit"):
		return http.StatusTooManyRequests
	case strings.Contains(errType, "invalid_request"):
		return http.StatusBadRequest
	case strings.Contains(combined, "authentication"), strings.Contains(combined, "unauthorized"), strings.Contains(combined, "invalid_api_key"):
		return http.StatusUnauthorized
	case strings.Contains(combined, "permission"), strings.Contains(combined, "forbidden"), strings.Contains(combined, "access denied"):
		return http.StatusForbidden
	case isOpenAIUpstreamCapacityShedEvent(payload):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func openAIStreamFailureStatus(payload []byte, message string) int {
	if len(bytes.TrimSpace(payload)) == 0 || !gjson.ValidBytes(payload) {
		return http.StatusBadGateway
	}
	if openAIStreamFailedEventSemanticStatus(payload, message) == http.StatusTooManyRequests {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

func openAIStreamFailedEventErrorCode(payload []byte) string {
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	}
	return code
}

func isOpenAIUpstreamCapacityShedEvent(payload []byte) bool {
	switch openAIStreamFailedEventErrorCode(payload) {
	case "server_is_overloaded", "slow_down":
		return true
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if isOpenAICapacityShedMessage(gjson.GetBytes(payload, path).String()) {
			return true
		}
	}
	return false
}

// Codex treats server_is_overloaded and slow_down as terminal model-capacity
// failures. Once semantic output is already visible the gateway cannot safely
// switch accounts, so rewrite only the client copy to a retryable code while
// retaining the original payload for health and scheduling decisions.
const openAICapacityShedRetryableClientCode = "server_error"

func sanitizeOpenAICapacityShedErrorCodeForClient(payload []byte) ([]byte, bool) {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !isOpenAIUpstreamCapacityShedEvent(payload) {
		return payload, false
	}
	updated := payload
	changed := false
	for _, path := range []string{"response.error.code", "error.code"} {
		parent := strings.TrimSuffix(path, ".code")
		if !gjson.GetBytes(updated, parent).Exists() {
			continue
		}
		code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(updated, path).String()))
		if code != "" && code != "server_is_overloaded" && code != "slow_down" {
			continue
		}
		next, err := sjson.SetBytes(updated, path, openAICapacityShedRetryableClientCode)
		if err != nil {
			return payload, false
		}
		updated = next
		changed = true
	}
	return updated, changed
}

// logOpenAICapacityFailoverSuppressed records the deliberate no-failover decision
// after semantic output has reached the client. Switching accounts at this point
// would splice two upstream responses together, so operators need an explicit
// diagnostic instead of interpreting the missing retry as a scheduler failure.
func logOpenAICapacityFailoverSuppressed(
	ctx context.Context,
	account *Account,
	path string,
	upstreamRequestID string,
	eventType string,
) {
	fields := []zap.Field{
		zap.String("path", path),
		zap.String("event_type", strings.TrimSpace(eventType)),
		zap.String("upstream_request_id", strings.TrimSpace(upstreamRequestID)),
	}
	if account != nil {
		fields = append(fields,
			zap.Int64("account_id", account.ID),
			zap.String("platform", account.Platform),
		)
	}
	logger.FromContext(ctx).Warn("gateway.failover_suppressed_after_semantic_output", fields...)
}

func openAIStreamFailedEventShouldFailover(payload []byte, message string) bool {
	if isOpenAIContextWindowError(message, payload) {
		return false
	}
	if openAIStreamFailureStatus(payload, message) == http.StatusTooManyRequests {
		return true
	}
	if isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
		return true
	}
	code := openAIStreamFailedEventErrorCode(payload)
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	}
	combined := strings.ToLower(strings.TrimSpace(message + " " + code + " " + errType))
	if combined == "" {
		return true
	}
	nonRetryableMarkers := []string{
		"invalid_request",
		"content_policy",
		"policy",
		"safety",
		"high-risk cyber",
		"not allowed",
		"violat",
	}
	for _, marker := range nonRetryableMarkers {
		if strings.Contains(combined, marker) {
			return false
		}
	}
	return true
}

func openAIStreamErrorEventShouldFailover(payload []byte, message string) bool {
	if strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return false
	}
	if isOpenAIContextWindowError(message, payload) {
		return false
	}
	return isOpenAITransientProcessingError(http.StatusBadRequest, message, payload)
}

func (s *OpenAIGatewayService) recordOpenAIStreamUpstreamError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	kind string,
	payload []byte,
	message string,
) string {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI upstream response failed"
	}
	statusCode := openAIStreamFailureStatus(payload, message)
	detail := ""
	if len(payload) > 0 && s != nil && s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(string(payload), maxBytes)
	}
	if c != nil {
		setOpsUpstreamError(c, statusCode, message, detail)
		event := OpsUpstreamErrorEvent{
			Platform:           PlatformOpenAI,
			UpstreamStatusCode: statusCode,
			UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
			Passthrough:        passthrough,
			Kind:               kind,
			Message:            message,
			Detail:             detail,
		}
		if account != nil {
			event.Platform = account.Platform
			event.AccountID = account.ID
			event.AccountName = account.Name
		}
		appendOpsUpstreamError(c, event)
	}
	return message
}

func (s *OpenAIGatewayService) newOpenAIStreamFailoverError(
	c *gin.Context,
	account *Account,
	passthrough bool,
	upstreamRequestID string,
	payload []byte,
	message string,
	responseHeaders ...http.Header,
) *UpstreamFailoverError {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "OpenAI stream disconnected before completion"
	}
	statusCode := openAIStreamFailureStatus(payload, message)
	var headers http.Header
	if len(responseHeaders) > 0 && responseHeaders[0] != nil {
		headers = responseHeaders[0].Clone()
	}
	message = s.recordOpenAIStreamUpstreamError(c, account, passthrough, upstreamRequestID, "failover", payload, message)
	errType := "upstream_error"
	if statusCode == http.StatusTooManyRequests {
		errType = "rate_limit_error"
	}
	body, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	failoverErr := &UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           body,
		ResponseHeaders:        headers,
		RetryableOnSameAccount: account != nil && (isOpenAIRequestScopedCapacityShed(message, payload) || (account.IsPoolMode() && (account.IsPoolModeRetryableStatus(statusCode) || isOpenAITransientProcessingError(http.StatusBadRequest, message, payload)))),
		RequestScopedTransient: isOpenAIRequestScopedCapacityShed(message, payload),
	}
	// A semantic 429 can be carried inside an HTTP 200 SSE stream. The outer
	// headers describe the successful stream, so do not use them to classify
	// quota exhaustion; keep them for Retry-After/request-id propagation.
	classificationHeaders := headers
	if statusCode == http.StatusTooManyRequests {
		classificationHeaders = nil
	}
	return s.decorateOpenAIOAuth429FailoverError(failoverErr, account, statusCode, false, classificationHeaders, headers, payload)
}

func (s *OpenAIGatewayService) handleStreamingResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	startTime time.Time,
	originalModel string,
	mappedModel string,
) (*openaiStreamingResultPassthrough, error) {
	// 失败 attempt（尝试）的上游响应头必须与正文一起延迟提交；否则安全
	// 换号后，客户端会看到旧账号的 request-id / turn-state（回合状态）。
	attemptResponseHeaders := make(http.Header)
	writeOpenAIPassthroughResponseHeaders(attemptResponseHeaders, resp.Header, s.responseHeaderFilter)
	if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
		attemptResponseHeaders.Set("x-request-id", requestID)
	}
	responseHeadersApplied := false
	applyAttemptResponseHeaders := func() {
		if responseHeadersApplied || c.Writer.Written() {
			return
		}
		for key, values := range attemptResponseHeaders {
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		responseHeadersApplied = true
		s.noteStagedOpenAICodexTurnStateCommitted(c, account, attemptResponseHeaders)
	}

	// SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := &OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	var firstTokenMs *int
	responseID := ""
	var responseServiceTier *string
	clientDisconnected := false
	sawDone := false
	sawTerminalEvent := false
	sawFailedEvent := false
	semanticOutputSeen := false
	failedMessage := ""
	clientOutputStarted := false
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	capacityFailoverSuppressedLogged := false
	pendingLines := make([]string, 0, 8)
	var pendingLineBytes int64
	// flushPending 表示已经写出但还没遇到 SSE 空行边界；函数退出时兜底刷新，避免上游缺少尾随空行导致客户端收不到最后一帧。
	flushPending := false
	// clientWriteMu 保护客户端写出：伪造首字 goroutine 与主循环并发写互斥
	//（同步扫描循环无法像事件循环那样在 select 内安全注入）。
	var clientWriteMu sync.Mutex
	// handler 层可能已在上游请求之前抢先提交伪造首字，此时不再重复排程。
	var fakeFirstTokenMs = CommittedFakeFirstTokenMs(c)
	// handler 层已武装过就接管：本函数开始写真实字节，定时器不再补发注释行。
	ClaimFakeFirstTokenOutput(c)
	flushPendingOutput := func() {
		if clientDisconnected || !flushPending {
			return
		}
		clientWriteMu.Lock()
		flusher.Flush()
		clientWriteMu.Unlock()
		flushPending = false
	}
	defer flushPendingOutput()
	writePendingLines := func() bool {
		applyAttemptResponseHeaders()
		clientWriteMu.Lock()
		defer clientWriteMu.Unlock()
		for _, pending := range pendingLines {
			if _, err := fmt.Fprintln(w, pending); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: account=%d", account.ID)
				return false
			}
		}
		pendingLines = pendingLines[:0]
		pendingLineBytes = 0
		return true
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	var firstOutputScanGuard atomic.Bool
	firstOutputScanGuard.Store(true)
	scanner.Split(openAIFirstOutputDynamicScanLines(&firstOutputScanGuard))
	defer putSSEScannerBuf64K(scanBuf)
	documentScanner := newOpenAISSEJSONDocumentScanner(scanner)
	// 部分 OpenAI 兼容上游把事件类型仅放在 SSE 的 event: 字段中，
	// data JSON 不会重复 type。保留当前帧的 event 类型，避免把已完成
	// 的 Responses 流误判为提前断开。
	currentSSEEventType := ""

	resultWithUsage := func() *openaiStreamingResultPassthrough {
		clientWriteMu.Lock()
		fakeMs := fakeFirstTokenMs
		clientWriteMu.Unlock()
		return &openaiStreamingResultPassthrough{
			usage:            usage,
			firstTokenMs:     firstTokenMs,
			fakeFirstTokenMs: fakeMs,
			responseID:       responseID,
			serviceTier:      responseServiceTier,
			imageCount:       imageCounter.Count(),
			imageOutputSizes: imageCounter.Sizes(),
		}
	}

	// 伪造流式首字（与 Anthropic 主路径同一配置）：随机延迟后先给客户端发
	// SSE 注释行；上游已开始输出/客户端已断开则不发。锁内判定+写出。
	fakeFTDone := make(chan struct{})
	defer close(fakeFTDone)
	if fakeFirstTokenMs == nil && !FakeFirstTokenArmedByHandler(c) {
		if fakeFTCh, stopFakeFT := ScheduleFakeFirstToken(fakeFirstTokenSettingsFor(s.settingService)); fakeFTCh != nil {
			defer stopFakeFT()
			go func() {
				select {
				case <-fakeFTCh:
				case <-fakeFTDone:
					return
				}
				clientWriteMu.Lock()
				defer clientWriteMu.Unlock()
				if clientOutputStarted || clientDisconnected {
					return
				}
				if _, err := w.WriteString(fakeFirstTokenComment); err == nil {
					flusher.Flush()
					ms := int(time.Since(startTime).Milliseconds())
					fakeFirstTokenMs = &ms
				}
			}()
		}
	}

	for documentScanner.Scan() {
		line := documentScanner.Text()
		noteFakeFirstTokenFromUpstreamLine(line, &fakeFirstTokenMs, startTime)
		if eventType, ok := extractOpenAISSEEventLine(line); ok {
			currentSSEEventType = eventType
		}
		lineStartsClientOutput := false
		forceFlushFailedEvent := false
		if data, ok := extractOpenAISSEDataLine(line); ok {
			dataBytes := []byte(data)
			trimmedData := strings.TrimSpace(data)
			if trimmedData != "[DONE]" {
				if normalizedData, normalized := normalizeCompletedImageGenerationStatus(dataBytes); normalized {
					dataBytes = normalizedData
					trimmedData = strings.TrimSpace(string(normalizedData))
					line = "data: " + string(normalizedData)
				}
				restoredData, restoreErr := restoreGrokResponsesClientToolPayload(c, dataBytes)
				if restoreErr != nil {
					return resultWithUsage(), fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
				}
				restoredData, restoreErr = restoreOpenAIResponsesClientToolPayload(c, restoredData)
				if restoreErr != nil {
					return resultWithUsage(), fmt.Errorf("restore OpenAI passthrough client tool response: %w", restoreErr)
				}
				restoredData, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredData)
				if restoreErr != nil {
					return resultWithUsage(), fmt.Errorf("restore OpenAI passthrough namespace response: %w", restoreErr)
				}
				restoredData = restoreCodexToolNamesFromContext(c, restoredData)
				if !bytes.Equal(restoredData, dataBytes) {
					dataBytes = restoredData
					trimmedData = strings.TrimSpace(string(restoredData))
					line = "data: " + string(restoredData)
				}
			}
			eventType := resolveOpenAIResponsesSSEEventType(dataBytes, currentSSEEventType)
			upstreamResponseModelObserverFromContext(c).ObserveOpenAI(dataBytes, eventType)
			line = rewriteResponseModelSSELineForClient(c, account, originalModel, line)
			if rewrittenData, rewritten := extractOpenAISSEDataLine(line); rewritten {
				dataBytes = []byte(rewrittenData)
				trimmedData = strings.TrimSpace(rewrittenData)
			}
			if !capacityFailoverSuppressedLogged && account != nil && account.Platform == PlatformOpenAI &&
				(eventType == "error" || eventType == "response.failed") &&
				openAIStreamClientOutputStarted(c, clientOutputStarted) &&
				isOpenAIUpstreamCapacityShedEvent(dataBytes) {
				logOpenAICapacityFailoverSuppressed(ctx, account, "passthrough_sse", upstreamRequestID, eventType)
				capacityFailoverSuppressedLogged = true
			}
			if eventType == "error" && !openAIStreamClientOutputStarted(c, clientOutputStarted) {
				errorMessage := extractOpenAISSEErrorMessage(dataBytes)
				if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(
					c, account.Platform, dataBytes, errorMessage,
				); matched {
					s.recordOpenAIStreamUpstreamError(c, account, true, upstreamRequestID, "http_error", dataBytes, errorMessage)
					MarkResponseCommitted(c)
					c.Header("Content-Type", "application/json; charset=utf-8")
					c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": errMsg}})
					return resultWithUsage(), fmt.Errorf("upstream error event: passthrough rule matched message=%s", errMsg)
				}
				if openAIStreamErrorEventShouldFailover(dataBytes, errorMessage) {
					return resultWithUsage(),
						s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, dataBytes, errorMessage, resp.Header)
				}
			}
			if eventType == "response.failed" {
				failedMessage = extractOpenAISSEErrorMessage(dataBytes)
				// response.failed 自带上游已消耗的 usage（input token 通常已扣）；必须先解析
				// 再打 cyber 标记，否则 mark 记到的是解析前的 0，导致流式 cyber 按 0 token 计费
				// 而漏记真实用量。对齐 WS V2 / Chat 流式路径（均先解析 usage 再 Mark）。
				s.parseSSEUsageBytes(dataBytes, usage)
				cyberHit, code, msg := detectOpenAICyberPolicy(dataBytes)
				if cyberHit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(string(dataBytes), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) {
					if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(
						c, account.Platform, dataBytes, failedMessage,
					); matched {
						s.recordOpenAIStreamUpstreamError(c, account, true, upstreamRequestID, "http_error", dataBytes, failedMessage)
						MarkResponseCommitted(c)
						c.Header("Content-Type", "application/json; charset=utf-8")
						c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": errMsg}})
						return resultWithUsage(), fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
					}
					if !cyberHit && openAIStreamFailedEventShouldFailover(dataBytes, failedMessage) {
						return resultWithUsage(),
							s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, dataBytes, failedMessage, resp.Header)
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
			}
			if trimmedData == "[DONE]" {
				sawDone = true
			}
			if openAIStreamEventIsTerminalWithType(trimmedData, eventType) {
				sawTerminalEvent = true
			}
			if responseID == "" {
				responseID = extractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			if observed := extractOpenAIServiceTierFromResponseJSONBytes(dataBytes); observed != nil {
				responseServiceTier = observed
			}
			imageCounter.AddSSEData(dataBytes)
			if sanitizedData, sanitized := sanitizeOpenAIResponseFailedEventForClient(
				dataBytes,
				eventType,
				openAIStreamClientOutputStarted(c, clientOutputStarted),
			); sanitized {
				dataBytes = sanitizedData
				trimmedData = strings.TrimSpace(string(sanitizedData))
				line = "data: " + string(sanitizedData)
			}
			lineStartsClientOutput = forceFlushFailedEvent || openAIStreamDataStartsClientOutput(trimmedData, eventType)
			if lineStartsClientOutput {
				firstOutputScanGuard.Store(false)
			}
			if lineStartsClientOutput && trimmedData != "[DONE]" && !openAIStreamEventTypeIsTerminal(eventType) {
				semanticOutputSeen = true
			}
			if (eventType == "response.completed" || eventType == "response.done") &&
				!sawFailedEvent && !semanticOutputSeen && !clientOutputStarted &&
				openAIResponsesCompletedEventIsEmpty(dataBytes, usage) {
				return resultWithUsage(), newOpenAIResponsesEmptyCompletedFailoverError(c, account, upstreamRequestID)
			}
			if firstTokenMs == nil && openAIStreamDataStartsVisibleOutput(trimmedData, eventType) {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			s.parseSSEUsageBytes(dataBytes, usage)
		}

		if !clientDisconnected {
			if !clientOutputStarted && !lineStartsClientOutput {
				incomingBytes := int64(len(line) + 1)
				if incomingBytes > openAIFirstOutputStageMaxBytes-pendingLineBytes {
					_ = resp.Body.Close()
					return resultWithUsage(), s.newOpenAIStreamFailoverError(
						c,
						account,
						true,
						upstreamRequestID,
						nil,
						"OpenAI passthrough pre-output staging limit exceeded",
						resp.Header,
					)
				}
				pendingLines = append(pendingLines, line)
				pendingLineBytes += incomingBytes
				continue
			}
			if !clientOutputStarted && len(pendingLines) > 0 {
				if !writePendingLines() {
					continue
				}
			}
			if !clientOutputStarted {
				applyAttemptResponseHeaders()
			}
			clientWriteMu.Lock()
			_, writeErr := fmt.Fprintln(w, line)
			clientWriteMu.Unlock()
			if writeErr != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] Client disconnected during streaming, continue draining upstream for usage: account=%d", account.ID)
			} else {
				clientOutputStarted = true
				flushPending = true
				if line == "" {
					flushPendingOutput()
				}
			}
		}
		if line == "" {
			currentSSEEventType = ""
		}
	}
	if err := documentScanner.Err(); err != nil {
		if sawTerminalEvent && !sawFailedEvent {
			s.clearOpenAIProxyStreamDisconnect(account)
			return resultWithUsage(), nil
		}
		if sawFailedEvent {
			return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
		}
		if errors.Is(err, errOpenAIFirstOutputScannerLimit) && !clientOutputStarted {
			return resultWithUsage(), s.newOpenAIStreamFailoverError(
				c,
				account,
				true,
				upstreamRequestID,
				nil,
				"OpenAI passthrough SSE line exceeds guarded first-output limit",
				resp.Header,
			)
		}
		if errors.Is(err, bufio.ErrTooLong) {
			logger.LegacyPrintf("service.openai_gateway", "[OpenAI passthrough] SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, err)
			return resultWithUsage(), err
		}
		if !openAIStreamClientOutputStarted(c, clientOutputStarted) {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(err.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(),
				s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, nil, msg)
		}
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", err)
		}
		s.recordOpenAIProxyStreamDisconnect(account, err, upstreamRequestID)
		logger.LegacyPrintf("service.openai_gateway",
			"[OpenAI passthrough] 流读取异常中断: account=%d request_id=%s err=%v",
			account.ID,
			upstreamRequestID,
			err,
		)
		return resultWithUsage(), fmt.Errorf("stream read error: %w", err)
	}
	if sawFailedEvent {
		return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
	}
	if !clientDisconnected && !sawDone && !sawTerminalEvent && ctx.Err() == nil {
		logger.FromContext(ctx).With(
			zap.String("component", "service.openai_gateway"),
			zap.Int64("account_id", account.ID),
			zap.String("upstream_request_id", upstreamRequestID),
		).Info("OpenAI passthrough 上游流在未收到 [DONE] 时结束，疑似断流")
		if !openAIStreamClientOutputStarted(c, clientOutputStarted) {
			return resultWithUsage(),
				s.newOpenAIStreamFailoverError(c, account, true, upstreamRequestID, nil, "OpenAI stream ended before a terminal event")
		}
		s.recordOpenAIProxyStreamDisconnect(account, errors.New("stream ended before terminal event"), upstreamRequestID)
		return resultWithUsage(), errors.New("stream usage incomplete: missing terminal event")
	}
	if (sawDone || sawTerminalEvent) && !sawFailedEvent {
		s.clearOpenAIProxyStreamDisconnect(account)
	}

	return resultWithUsage(), nil
}

func (s *OpenAIGatewayService) handleNonStreamingResponsePassthrough(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	originalModel string,
	mappedModel string,
) (*openaiNonStreamingResultPassthrough, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	upstreamResponseModelObserverFromContext(c).ObserveOpenAI(body, "response.completed")

	// Detect SSE responses from upstream and convert to JSON.
	// Some upstreams (e.g. other sub2api instances) may return SSE even when
	// stream=false was requested. Without this conversion the client would
	// receive raw SSE text or a terminal event with empty output.
	if isEventStreamResponse(resp.Header) {
		return s.handlePassthroughSSEToJSON(resp, c, body, originalModel, mappedModel)
	}

	usage := &OpenAIUsage{}
	usageParsed := false
	if len(body) > 0 {
		if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(body); ok {
			*usage = parsedUsage
			usageParsed = true
		}
	}
	if !usageParsed {
		// 兜底：尝试从 SSE 文本中解析 usage
		usage = s.parseSSEUsageFromBody(string(body))
	}

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	body = rewriteResponseModelForClient(c, nil, originalModel, body)
	body, err = restoreOpenAIResponsesClientToolPayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI passthrough client tools: %w", err)
	}
	body, err = restoreOpenAIResponsesNamespacePayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI passthrough namespace response: %w", err)
	}
	body = restoreCodexToolNamesFromContext(c, body)
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}
	return &openaiNonStreamingResultPassthrough{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		serviceTier:      extractOpenAIServiceTierFromResponseJSONBytes(body),
		imageCount:       countOpenAIResponseImageOutputsFromJSONBytes(body),
		imageOutputSizes: collectOpenAIResponseImageOutputSizesFromJSONBytes(body),
	}, nil
}

// handlePassthroughSSEToJSON converts an SSE response body into a JSON
// response for the passthrough path. It mirrors handleSSEToJSON while
// preserving passthrough payloads, except compact-only model remapping may
// rewrite model fields back to the original requested model.
func (s *OpenAIGatewayService) handlePassthroughSSEToJSON(resp *http.Response, c *gin.Context, body []byte, originalModel string, mappedModel string) (*openaiNonStreamingResultPassthrough, error) {
	observer := upstreamResponseModelObserverFromContext(c)
	forEachOpenAISSEDataPayload(string(body), func(payload []byte) {
		observer.ObserveOpenAI(payload, strings.TrimSpace(gjson.GetBytes(payload, "type").String()))
	})
	bodyText := string(body)
	finalResponse, ok := extractCodexFinalResponse(bodyText)

	usage := &OpenAIUsage{}
	if ok {
		if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(finalResponse); parsed {
			*usage = parsedUsage
		}
		// When the terminal event has an empty output array, reconstruct
		// output from accumulated delta events so the client gets full content.
		if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
			if outputJSON, reconstructed := reconstructResponseOutputFromSSE(bodyText); reconstructed {
				if patched, err := sjson.SetRawBytes(finalResponse, "output", outputJSON); err == nil {
					finalResponse = patched
				}
			}
		}
		finalResponse = supplementCompactionItemFromSSE(c, finalResponse, bodyText)
		body = finalResponse
		body = rewriteResponseModelForClient(c, nil, originalModel, body)
		// Correct tool calls in final response
		body = s.correctToolCallsInResponseBody(body)
		restoredClientToolBody, restoreErr := restoreOpenAIResponsesClientToolPayload(c, body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI passthrough client tools: %w", restoreErr)
		}
		body = restoredClientToolBody
		restoredBody, restoreErr := restoreOpenAIResponsesNamespacePayload(c, body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI passthrough namespace response: %w", restoreErr)
		}
		restoredBody = restoreCodexToolNamesFromContext(c, restoredBody)
		body = restoredBody
	} else {
		terminalType, terminalPayload, terminalOK := extractOpenAISSETerminalEvent(bodyText)
		if terminalOK && terminalType == "response.failed" {
			msg := extractOpenAISSEErrorMessage(terminalPayload)
			if msg == "" {
				msg = "Upstream compact response failed"
			}
			return nil, s.writeOpenAINonStreamingProtocolError(resp, c, msg)
		}
		usage = s.parseSSEUsageFromBody(bodyText)
		bodyText = rewriteResponseModelSSEBodyForClient(c, nil, originalModel, bodyText)
		body = []byte(bodyText)
	}

	writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)

	contentType := "application/json; charset=utf-8"
	if !ok {
		contentType = resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
	}
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResultPassthrough{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		serviceTier:      extractOpenAIServiceTierFromResponseJSONBytes(body),
		imageCount:       countOpenAIImageOutputsFromSSEBody(bodyText),
		imageOutputSizes: collectOpenAIImageOutputSizesFromSSEBody(bodyText),
	}, nil
}

func writeOpenAIPassthroughResponseHeaders(dst http.Header, src http.Header, filter *responseheaders.CompiledHeaderFilter) {
	if dst == nil || src == nil {
		return
	}
	if filter != nil {
		responseheaders.WriteFilteredHeaders(dst, src, filter)
	} else {
		// 兜底：尽量保留最基础的 content-type
		if v := strings.TrimSpace(src.Get("Content-Type")); v != "" {
			dst.Set("Content-Type", v)
		}
	}
	// 透传模式强制放行 x-codex-* 响应头（若上游返回）。
	// 注意：真实 http.Response.Header 的 key 一般会被 canonicalize；但为了兼容测试/自建响应，
	// 这里用 EqualFold 做一次大小写不敏感的查找。
	getCaseInsensitiveValues := func(h http.Header, want string) []string {
		if h == nil {
			return nil
		}
		for k, vals := range h {
			if strings.EqualFold(k, want) {
				return vals
			}
		}
		return nil
	}

	for _, rawKey := range []string{
		"x-codex-primary-used-percent",
		"x-codex-primary-reset-after-seconds",
		"x-codex-primary-window-minutes",
		"x-codex-secondary-used-percent",
		"x-codex-secondary-reset-after-seconds",
		"x-codex-secondary-window-minutes",
		"x-codex-primary-over-secondary-limit-percent",
	} {
		vals := getCaseInsensitiveValues(src, rawKey)
		if len(vals) == 0 {
			continue
		}
		key := http.CanonicalHeaderKey(rawKey)
		dst.Del(key)
		for _, v := range vals {
			dst.Add(key, v)
		}
	}

	// turn-state 不在通用响应头白名单中，但 Codex 协议要求回传。
	// 上游未返回时也必须删除旧值，避免 failover attempt 串扰。
	turnStateKey := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	dst.Del(turnStateKey)
	for _, value := range getCaseInsensitiveValues(src, openAICodexTurnStateHeader) {
		dst.Add(turnStateKey, value)
	}
}

// writeDecodedJSONResponseHeaders copies the normal passthrough headers for a
// response body that has already been fully read and possibly re-encoded by a
// JSON bridge.  The upstream Content-Encoding and Content-Length describe the
// original wire body, not the bytes about to be written, so retaining either
// header can make clients attempt a second decompression or truncate JSON.
func writeDecodedJSONResponseHeaders(dst http.Header, src http.Header, filter *responseheaders.CompiledHeaderFilter) {
	writeOpenAIPassthroughResponseHeaders(dst, src, filter)
	dst.Del("Content-Encoding")
	dst.Del("Content-Length")
}

func (s *OpenAIGatewayService) buildUpstreamRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token string, isStream bool, promptCacheKey string, isCodexCLI bool) (*http.Request, error) {
	// Determine target URL based on account type
	var targetURL string
	switch account.Type {
	case AccountTypeOAuth:
		// OAuth accounts use ChatGPT internal API
		targetURL = chatgptCodexURL
	case AccountTypeAPIKey:
		// API Key accounts use Platform API or custom base URL
		baseURL := account.GetOpenAIBaseURL()
		if account.UsesNativeCNResponses() && account.IsAdaptiveAPIProtocol() {
			baseURL = account.GetCNProtocolBaseURL(APIProtocolResponses)
		}
		if baseURL == "" {
			targetURL = openaiPlatformAPIURL
		} else {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = buildOpenAIResponsesURLForPlatform(account.Platform, validatedURL)
		}
	default:
		targetURL = openaiPlatformAPIURL
	}
	targetURL = appendOpenAIResponsesRequestPathSuffix(targetURL, openAIResponsesRequestPathSuffix(c))
	body = normalizeDeepSeekResponsesRequestBody(account, body)
	// 按账号出口时区改写 body 里的 <timezone> 环境声明（与出口 IP 地理对齐，
	// 消除「客户端时区 + 机房出口 IP」的降智风控矛盾）。未配置时原样透传。
	body = rewriteUpstreamTimezoneFromAccount(account, body)

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	// Set authentication header. Agent Identity assertions are generated here
	// so delayed/retried requests never reuse an old timestamp or task.
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return nil, fmt.Errorf("build openai authentication headers: %w", err)
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// Set headers specific to OAuth accounts (ChatGPT internal API)
	if account.Type == AccountTypeOAuth {
		// Required: set Host for ChatGPT API (must use req.Host, not Header.Set)
		req.Host = "chatgpt.com"
		// Required: set chatgpt-account-id header
		chatgptAccountID := account.GetChatGPTAccountID()
		if chatgptAccountID != "" {
			req.Header.Set("chatgpt-account-id", chatgptAccountID)
		}
	}

	// Whitelist passthrough headers
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if openaiAllowedHeaders[lowerKey] {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}
	// 在会话隔离和指纹改写之前判断原始客户端状态的来源。
	s.guardOpenAICodexTurnStateEcho(c, account, req.Header)

	if err := s.applyOpenAICodexTicket(ctx, account, extractOpenAICodexTicketModel(body), req.Header); err != nil {
		return nil, err
	}
	if account.Type == AccountTypeOAuth {
		compatMessagesBridge := isOpenAICompatMessagesBridgeContext(c) || isOpenAICompatMessagesBridgeBody(body)
		// 清除客户端透传的 session 头，后续用隔离后的值重新设置，防止跨用户会话碰撞。
		clientConversationID := strings.TrimSpace(req.Header.Get("conversation_id"))
		clientSessionID := strings.TrimSpace(req.Header.Get("session-id"))
		if clientSessionID == "" {
			clientSessionID = strings.TrimSpace(req.Header.Get("session_id"))
		}
		req.Header.Del("conversation_id")
		req.Header.Del("session-id")
		req.Header.Del("session_id")

		if compatMessagesBridge {
			req.Header.Del("OpenAI-Beta")
			req.Header.Del("originator")
		} else {
			req.Header.Set("originator", resolveOpenAIUpstreamOriginator(c, isCodexCLI))
		}
		apiKeyID := getAPIKeyIDFromContext(c)
		if isOpenAIResponsesCompactPath(c) {
			req.Header.Set("accept", "application/json")
			if req.Header.Get("version") == "" {
				req.Header.Set("version", codexCLIVersion)
			}
			compactSession := resolveOpenAICompactSessionID(c)
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, compactSession))
		} else {
			req.Header.Set("accept", "text/event-stream")
		}
		if clientSessionID != "" {
			req.Header.Set("session_id", isolateOpenAISessionID(apiKeyID, clientSessionID))
		}
		if promptCacheKey != "" {
			isolated := isolateOpenAISessionID(apiKeyID, promptCacheKey)
			if clientSessionID == "" {
				req.Header.Set("session_id", isolated)
			}
			if !compatMessagesBridge || clientConversationID != "" {
				req.Header.Set("conversation_id", isolated)
			}
		}
	}

	// Apply custom User-Agent if configured
	customUA := account.GetOpenAIUserAgent()
	if customUA != "" {
		req.Header.Set("user-agent", customUA)
	}

	// 若开启 ForceCodexCLI，则强制将上游 User-Agent 伪装为 Codex CLI。
	// 用于网关未透传/改写 User-Agent 时，仍能命中 Codex 侧识别逻辑。
	if s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		req.Header.Set("user-agent", codexCLIUserAgent)
	}

	// 浏览器型 UA 兜底：仅 OAuth（ChatGPT 内部接口）账号生效，若最终 user-agent 仍为浏览器
	// （Chrome/Firefox/Safari/Edge 等），替换为后台配置的 Codex UA，避免 Cloudflare 触发 JS 质询。
	s.overrideBrowserUserAgent(ctx, account, req)
	if account.Type == AccountTypeOAuth {
		applyStagedCodexFingerprintHeaders(c, account, req.Header)
		enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	}

	// Ensure required headers exist
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	applyOpenAICodexBetaFeatures(c, account, req.Header)
	setOpenAICodexRoutingHintFromBody(req.Header, account, body)

	return req, nil
}

// overrideBrowserUserAgent 检查请求的最终 user-agent，若为浏览器 UA 则替换为后台配置的 Codex UA。
// 用于规避 Cloudflare 对浏览器型 UA 在 ChatGPT 内部接口上的访问质询。
// 影响范围严格限定：仅 OAuth（Codex/ChatGPT 内部接口）账号生效；API Key 等其他账号原样透传。
// 仅在识别为浏览器（Mozilla/...）时改写，其他 CLI/工具 UA 不动。
func (s *OpenAIGatewayService) overrideBrowserUserAgent(ctx context.Context, account *Account, req *http.Request) {
	if req == nil || account == nil {
		return
	}
	if account.Type != AccountTypeOAuth {
		return
	}
	currentUA := req.Header.Get("user-agent")
	if !openai.IsBrowserUserAgent(currentUA) {
		return
	}
	codexUA := DefaultOpenAICodexUserAgent
	if s != nil && s.settingService != nil {
		if v := strings.TrimSpace(s.settingService.GetOpenAICodexUserAgent(ctx)); v != "" {
			codexUA = v
		}
	}
	req.Header.Set("user-agent", codexUA)
}

func (s *OpenAIGatewayService) handleErrorResponse(
	ctx context.Context,
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestBody []byte,
	requestedModel ...string,
) (*OpenAIForwardResult, error) {
	body := s.readUpstreamErrorBody(resp)

	if isOpenAIRequestScopedContentPolicyError(resp.StatusCode, body) {
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, truncateString(string(body), 2048))
		writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		MarkResponseCommitted(c)
		c.Data(resp.StatusCode, contentType, body)
		return nil, fmt.Errorf("openai content policy rejection: %s", upstreamMsg)
	}

	// cyber_policy 硬阻断：透传上游原始错误体给客户端（不重包成通用 502），不冷却账号。
	// 当前请求恒透传（需求1）；标记供 handler 事后写风控/邮件。400 cyber 不可 failover
	// （shouldFailoverUpstreamError(400)=false），故走到此处即可安全早返回。
	if hit, code, cyberMsg := detectOpenAICyberPolicy(body); hit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           code,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
		setOpsUpstreamError(c, resp.StatusCode, cyberMsg, truncateString(string(body), 2048))
		writeOpenAIPassthroughResponseHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
			c.Data(resp.StatusCode, contentType, body)
		}
		if cyberMsg == "" {
			return nil, fmt.Errorf("openai cyber_policy: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", cyberMsg)
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	logOpenAIInstructionsRequiredDebug(ctx, c, account, resp.StatusCode, upstreamMsg, requestBody, body)

	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		logger.LegacyPrintf("service.openai_gateway",
			"OpenAI upstream error %d (account=%d platform=%s type=%s): %s",
			resp.StatusCode,
			account.ID,
			account.Platform,
			account.Type,
			truncateForLog(body, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
		)
	}

	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c,
		PlatformOpenAI,
		resp.StatusCode,
		body,
		http.StatusBadGateway,
		"upstream_error",
		"Upstream request failed",
	); matched {
		MarkResponseCommitted(c)
		if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
			writeOpenAICompactSSEFailureMessage(c, status, errType, errMsg)
		} else {
			c.JSON(status, gin.H{
				"error": gin.H{
					"type":    errType,
					"message": errMsg,
				},
			})
		}
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Check custom error codes
	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			ProxyHost:          proxyHostForAccount(account),
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
			writeOpenAICompactSSEFailureMessage(c, http.StatusInternalServerError, "upstream_error", "Upstream gateway error")
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": gin.H{
					"type":    "upstream_error",
					"message": "Upstream gateway error",
				},
			})
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Handle upstream error (mark account status)
	var reqModel string
	if len(requestedModel) > 0 {
		reqModel = strings.TrimSpace(requestedModel[0])
	}
	if reqModel == "" {
		reqModel, _, _ = extractOpenAIRequestMetaFromBody(requestBody)
	}
	shouldDisable := s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, body, reqModel)
	kind := "http_error"
	if shouldDisable {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		ProxyHost:          proxyHostForAccount(account),
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if shouldDisable {
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           body,
			RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
		}
	}

	MarkResponseCommitted(c)

	// 不可切换的 400 是确定性请求错误：换账号仍会失败。保留真实状态码及
	// type/code/param，避免下游将统一包装后的 502 当作临时故障重复请求。
	if isOpenAIDeterministicClientError(resp.StatusCode) {
		writeOpenAIUpstreamClientError(c, resp.StatusCode, body, upstreamMsg)
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	// Return appropriate error response
	var errType, errMsg string
	var statusCode int

	switch resp.StatusCode {
	case 401:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream authentication failed, please contact administrator"
	case 402:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream payment required: insufficient balance or billing issue"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream access forbidden, please contact administrator"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
		writeOpenAICompactSSEFailureMessage(c, statusCode, errType, errMsg)
	} else {
		c.JSON(statusCode, gin.H{
			"error": gin.H{
				"type":    errType,
				"message": errMsg,
			},
		})
	}

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

// compatErrorWriter is the signature for format-specific error writers used by
// the compat paths (Chat Completions and Anthropic Messages).
type compatErrorWriter func(c *gin.Context, statusCode int, errType, message string)

// handleCompatErrorResponse is the shared non-failover error handler for the
// Chat Completions and Anthropic Messages compat paths. It mirrors the logic of
// handleErrorResponse (passthrough rules, ShouldHandleErrorCode, rate-limit
// tracking, secondary failover) but delegates the final error write to the
// format-specific writer function.
func (s *OpenAIGatewayService) handleCompatErrorResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	writeError compatErrorWriter,
	requestedModel ...string,
) (*OpenAIForwardResult, error) {
	body := s.readUpstreamErrorBody(resp)

	if isOpenAIRequestScopedContentPolicyError(resp.StatusCode, body) {
		upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
		if upstreamMsg == "" {
			upstreamMsg = "Request blocked by upstream content policy"
		}
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, truncateString(string(body), 2048))
		MarkResponseCommitted(c)
		writeError(c, resp.StatusCode, "invalid_request_error", upstreamMsg)
		return nil, fmt.Errorf("openai content policy rejection: %s", upstreamMsg)
	}

	// cyber_policy：兼容路径（Chat Completions / Anthropic）以各自格式回写错误，
	// 不原样透传 responses 格式的 cyber body（否则对下游格式不合法）。cyber 是上游网络
	// 安全策略拦截，不冷却账号，故标记后直接以兼容格式回写错误并返回，跳过下方
	// handleOpenAIAccountUpstreamError（避免自定义 temp-unschedulable 规则误冷却）。
	if hit, code, cyberMsg := detectOpenAICyberPolicy(body); hit {
		MarkOpsCyberPolicy(c, CyberPolicyMark{
			Code:           code,
			Message:        cyberMsg,
			Body:           truncateString(string(body), 4096),
			UpstreamStatus: resp.StatusCode,
		})
		setOpsUpstreamError(c, resp.StatusCode, cyberMsg, truncateString(string(body), 2048))
		clientMsg := cyberMsg
		if clientMsg == "" {
			clientMsg = "Request blocked by upstream cyber-security policy"
		}
		writeError(c, resp.StatusCode, "invalid_request_error", clientMsg)
		if cyberMsg == "" {
			return nil, fmt.Errorf("openai cyber_policy: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("openai cyber_policy: %s", cyberMsg)
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if upstreamMsg == "" {
		upstreamMsg = fmt.Sprintf("Upstream error: %d", resp.StatusCode)
	}
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)

	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

	// Apply error passthrough rules
	if status, errType, errMsg, matched := applyErrorPassthroughRule(
		c, account.Platform, resp.StatusCode, body,
		http.StatusBadGateway, "api_error", "Upstream request failed",
	); matched {
		MarkResponseCommitted(c)
		writeError(c, status, errType, errMsg)
		if upstreamMsg == "" {
			upstreamMsg = errMsg
		}
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (passthrough rule matched)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (passthrough rule matched) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Check custom error codes — if the account does not handle this status,
	// return a generic error without exposing upstream details.
	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			ProxyHost:          proxyHostForAccount(account),
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		MarkResponseCommitted(c)
		writeError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Track rate limits and decide whether to trigger secondary failover.
	var modelForCooldown string
	if len(requestedModel) > 0 {
		modelForCooldown = requestedModel[0]
	}
	shouldDisable := s.handleOpenAIAccountUpstreamError(
		c.Request.Context(), account, resp.StatusCode, resp.Header, body, modelForCooldown,
	)
	kind := "http_error"
	if shouldDisable {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		ProxyHost:          proxyHostForAccount(account),
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if shouldDisable {
		return nil, &UpstreamFailoverError{
			StatusCode:             resp.StatusCode,
			ResponseBody:           body,
			RetryableOnSameAccount: account.IsPoolMode() && account.IsPoolModeRetryableStatus(resp.StatusCode),
		}
	}

	MarkResponseCommitted(c)

	// Map status code to error type and write response
	errType := "api_error"
	switch {
	case resp.StatusCode == 400:
		errType = "invalid_request_error"
	case resp.StatusCode == 404:
		errType = "not_found_error"
	case resp.StatusCode == 429:
		errType = "rate_limit_error"
	case resp.StatusCode >= 500:
		errType = "api_error"
	}

	writeError(c, resp.StatusCode, errType, upstreamMsg)
	return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, upstreamMsg)
}

// openaiStreamingResult streaming response result
type openaiStreamingResult struct {
	usage            *OpenAIUsage
	firstTokenMs     *int
	fakeFirstTokenMs *int // 伪造首字（SSE 注释行）发出时刻；未开启/未触发为 nil
	responseID       string
	serviceTier      *string
	imageCount       int
	imageOutputSizes []string
	searchCount      int // [v0.2.5 新增字段]
}

type openaiNonStreamingResult struct {
	*OpenAIUsage
	usage            *OpenAIUsage
	responseID       string
	serviceTier      *string
	imageCount       int
	imageOutputSizes []string
}

func firstOpenAIReasoningEffortArg(values ...*string) *string {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func (s *OpenAIGatewayService) openAIFirstOutputTimeout(reasoningEffort *string) time.Duration {
	if s == nil || s.cfg == nil {
		return 0
	}
	if isOpenAIHighReasoningEffort(reasoningEffort) && s.cfg.Gateway.OpenAIFirstOutputHighReasoningTimeoutSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIFirstOutputHighReasoningTimeoutSeconds) * time.Second
	}
	if s.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIFirstOutputTimeoutSeconds) * time.Second
	}
	return 0
}

func isOpenAIHighReasoningEffort(reasoningEffort *string) bool {
	if reasoningEffort == nil {
		return false
	}
	switch normalizeOpenAIReasoningEffort(*reasoningEffort) {
	case "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) handleStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel string, reasoningEffort ...*string) (*openaiStreamingResult, error) {
	stageFirstOutput := account != nil && account.Platform == PlatformOpenAI
	var attemptResponseHeaders http.Header
	if stageFirstOutput {
		if s.responseHeaderFilter != nil {
			attemptResponseHeaders = responseheaders.FilterHeaders(resp.Header, s.responseHeaderFilter)
		} else if requestID := strings.TrimSpace(resp.Header.Get("x-request-id")); requestID != "" {
			attemptResponseHeaders = http.Header{"X-Request-Id": []string{requestID}}
		}
	} else if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	// turn-state 属于当前账号 attempt（尝试），不能在首输出前写进共享的
	// Gin writer。否则本次 attempt 被丢弃并换号时，旧账号状态可能随下一次
	// 错误响应泄出。先留在局部 header，真正 flush（写给客户端）时再提交。
	var attemptTurnStateHeaders http.Header
	stageOpenAICodexTurnState(&attemptTurnStateHeaders, resp.Header)
	turnStateKey := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	c.Writer.Header().Del(turnStateKey)
	turnStateHeadersApplied := false
	turnStateHeadersCommitted := false
	turnStateProvenanceRecorded := false

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Pass through other headers
	if !stageFirstOutput {
		if v := resp.Header.Get("x-request-id"); v != "" {
			c.Header("x-request-id", v)
		}
	}
	responseHeadersApplied := false
	applyAttemptResponseHeaders := func() {
		if responseHeadersApplied || len(attemptResponseHeaders) == 0 || c.Writer.Written() {
			return
		}
		for key, values := range attemptResponseHeaders {
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		// 上游过滤头可能也含 Content-Type / Cache-Control；网关对外提供的
		// SSE 协议头必须最终生效，不能被失败或兼容上游的普通 JSON 头覆盖。
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		responseHeadersApplied = true
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}
	// fakeOutputStarted 伪造首字抑制标志：只有客户端真正收到流式正文才置 true，
	// 伪造 goroutine 检查后跳过（原子操作，避免锁住全部写出路径）。
	var fakeOutputStarted atomic.Bool
	// clientWriteMu 串行化对底层 writer 的写出（flushBuffered / 伪造首字注释行 /
	// keepalive），避免伪造 goroutine 与主循环并发写导致 SSE 帧错位。
	var clientWriteMu sync.Mutex
	bufferedWriter := bufio.NewWriterSize(w, 4*1024)
	var firstOutputStage *openAIFirstOutputStage
	if stageFirstOutput {
		firstOutputStage = newOpenAIFirstOutputStage()
		defer func() {
			if err := firstOutputStage.Close(); err != nil {
				logger.LegacyPrintf("service.openai_gateway", "OpenAI first-output staging cleanup failed: account=%d model=%s error=%v", account.ID, originalModel, err)
			}
		}()
	}
	writePendingString := func(value string) (int, error) {
		if firstOutputStage != nil && !firstOutputStage.closed {
			return firstOutputStage.WriteString(value)
		}
		return bufferedWriter.WriteString(value)
	}
	pendingBytes := func() int64 {
		if firstOutputStage != nil && !firstOutputStage.closed {
			return firstOutputStage.Buffered()
		}
		return int64(bufferedWriter.Buffered())
	}
	// commitResponseHeaders 提交本 attempt（尝试）的透传头与 turn-state 头。
	// 必须在真正写给客户端之前、且在 clientWriteMu 保护下调用一次：伪造首字
	// 注释行可能抢在正文之前提交响应头，这里保证它带走正确的协议头。
	commitResponseHeaders := func() {
		applyAttemptResponseHeaders()
		if !turnStateHeadersApplied {
			if !c.Writer.Written() {
				c.Writer.Header().Del(turnStateKey)
				for _, value := range attemptTurnStateHeaders.Values(openAICodexTurnStateHeader) {
					c.Writer.Header().Add(turnStateKey, value)
					turnStateHeadersCommitted = true
				}
			}
			turnStateHeadersApplied = true
		}
	}
	flushBuffered := func() error {
		clientWriteMu.Lock()
		defer clientWriteMu.Unlock()
		commitResponseHeaders()
		if firstOutputStage != nil && !firstOutputStage.closed {
			if err := firstOutputStage.CommitTo(w); err != nil {
				return err
			}
		} else {
			if err := bufferedWriter.Flush(); err != nil {
				return err
			}
		}
		flusher.Flush()
		if turnStateHeadersCommitted && !turnStateProvenanceRecorded {
			s.noteStagedOpenAICodexTurnStateCommitted(c, account, attemptTurnStateHeaders)
			turnStateProvenanceRecorded = true
		}
		return nil
	}

	usage := &OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	var firstTokenMs *int
	firstOutputProgressObserved := false
	responseID := ""
	var responseServiceTier *string
	var firstOutputScanGuard atomic.Bool
	firstOutputScanGuard.Store(stageFirstOutput)
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	if stageFirstOutput {
		scanner.Split(openAIFirstOutputDynamicScanLines(&firstOutputScanGuard))
	}
	documentScanner := newOpenAISSEJSONDocumentScanner(scanner)
	// event: 与 data: 属于同一个 SSE 帧；某些兼容上游只在前者声明
	// response.completed / response.failed 等终态，data 内没有 type。
	currentSSEEventType := ""

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	if account != nil && account.Platform == PlatformGrok {
		configuredSeconds := 0
		if s.cfg != nil {
			configuredSeconds = s.cfg.Gateway.StreamDataIntervalTimeout
		}
		streamInterval = resolveGrokStreamIdleTimeout(configuredSeconds)
	}
	firstOutputTimeout := s.openAIFirstOutputTimeout(firstOpenAIReasoningEffortArg(reasoningEffort...))
	guardFirstOutput := stageFirstOutput && firstOutputTimeout > 0
	// 仅监控上游数据间隔超时，不被下游写入阻塞影响
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	// 下游 keepalive 仅用于防止代理空闲断开
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	var firstOutputTimer *time.Timer
	if firstOutputTimeout > 0 {
		remaining := time.Until(startTime.Add(firstOutputTimeout))
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
		firstOutputTimer = time.NewTimer(remaining)
		defer firstOutputTimer.Stop()
	}
	var firstOutputCh <-chan time.Time
	if firstOutputTimer != nil {
		firstOutputCh = firstOutputTimer.C
	}
	stopFirstOutputTimer := func() {
		if firstOutputTimer == nil {
			return
		}
		if !firstOutputTimer.Stop() {
			select {
			case <-firstOutputTimer.C:
			default:
			}
		}
		firstOutputCh = nil
		firstOutputTimer = nil
	}
	// Track downstream writes separately from upstream reads: pre-output failover
	// can buffer response.created / response.in_progress, so keepalive must be
	// based on downstream idle time.
	lastDownstreamWriteAt := time.Now()

	// 仅发送一次错误事件，避免多次写入导致协议混乱。
	// 注意：OpenAI `/v1/responses` streaming 事件必须符合 OpenAI Responses schema；
	// 否则下游 SDK（例如 OpenCode）会因为类型校验失败而报错。
	errorEventSent := false
	clientDisconnected := false // 客户端断开后继续 drain 上游以收集 usage
	sawTerminalEvent := false
	sawFailedEvent := false
	responsesSemanticOutputSeen := false
	failedMessage := ""
	clientOutputStarted := false
	upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id"))
	capacityFailoverSuppressedLogged := false

	// 伪造流式首字（与 OAuth passthrough 同一配置）：随机延迟后先给客户端
	// 发 SSE 注释行（直写 w，绕过 firstOutputStage/bufferedWriter 这两个正文
	// 通道）；客户端已收到正文（fakeOutputStarted）则不发。
	// 该注释行的字节数计入 keepalive 字节，因此不计入「已向客户端输出」，
	// 首输出前的静默换号（failover）仍然成立。
	var fakeFirstTokenMs = CommittedFakeFirstTokenMs(c)
	// handler 层已武装过就接管：本函数开始写真实字节，定时器不再补发注释行。
	ClaimFakeFirstTokenOutput(c)
	fakeFTDone := make(chan struct{})
	defer close(fakeFTDone)
	if fakeFirstTokenMs == nil && !FakeFirstTokenArmedByHandler(c) {
		if fakeFTCh, stopFakeFT := ScheduleFakeFirstToken(fakeFirstTokenSettingsFor(s.settingService)); fakeFTCh != nil {
			defer stopFakeFT()
			go func() {
				select {
				case <-fakeFTCh:
				case <-fakeFTDone:
					return
				}
				if fakeOutputStarted.Load() {
					return
				}
				clientWriteMu.Lock()
				defer clientWriteMu.Unlock()
				if fakeOutputStarted.Load() {
					return
				}
				// 注释行先出站，必须先把响应头（含 turn-state）落定，避免 gin
				// 隐式 WriteHeader 丢掉本 attempt 的透传头。
				commitResponseHeaders()
				n, err := w.WriteString(fakeFirstTokenComment)
				if err == nil {
					flusher.Flush()
					// 与 keepalive 同口径记账：注释行不算「真实正文」。
					recordOpenAIStreamKeepaliveBytes(c, n)
					ms := int(time.Since(startTime).Milliseconds())
					fakeFirstTokenMs = &ms
				}
			}()
		}
	}

	var streamFailoverErr error
	eventInProgress := false
	eventStartsClientOutput := false
	eventStartsVisibleOutput := false
	eventShouldFlush := false
	handlePendingWriteError := func(err error) {
		if firstOutputStage != nil && !firstOutputStage.closed {
			message := "OpenAI first-output staging failed"
			if errors.Is(err, errOpenAIFirstOutputStageLimit) {
				message = "OpenAI first-output staging limit exceeded"
			}
			failoverErr := s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, message)
			streamFailoverErr = failoverErr
			_ = resp.Body.Close()
			return
		}
		clientDisconnected = true
		logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
	}
	completeGuardedEvent := func(queueDrained bool) {
		completedProgressEvent := eventStartsClientOutput
		completedVisibleEvent := eventStartsVisibleOutput
		shouldFlush := eventShouldFlush || (queueDrained && clientOutputStarted)
		eventInProgress = false
		if !clientDisconnected && shouldFlush {
			if err := flushBuffered(); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming flush, continuing to drain upstream for billing")
			} else {
				clientOutputStarted = true
				// 客户端已真正收到正文：伪造首字不再需要。
				fakeOutputStarted.Store(true)
				lastDownstreamWriteAt = time.Now()
			}
		}
		if completedProgressEvent && !firstOutputProgressObserved {
			firstOutputScanGuard.Store(false)
			firstOutputProgressObserved = true
			stopFirstOutputTimer()
		}
		if completedVisibleEvent && firstTokenMs == nil {
			milliseconds := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &milliseconds
		}
		eventStartsClientOutput = false
		eventStartsVisibleOutput = false
		eventShouldFlush = false
	}
	sendErrorEvent := func(reason string) {
		if errorEventSent || clientDisconnected {
			return
		}
		errorEventSent = true
		payload := `{"type":"error","sequence_number":0,"error":{"type":"upstream_error","message":` + strconv.Quote(reason) + `,"code":` + strconv.Quote(reason) + `}}`
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		if _, err := writePendingString("data: " + payload + "\n\n"); err != nil {
			handlePendingWriteError(err)
			return
		}
		if err := flushBuffered(); err != nil {
			clientDisconnected = true
			return
		}
		clientOutputStarted = true
		fakeOutputStarted.Store(true)
		lastDownstreamWriteAt = time.Now()
	}

	streamOutputAccumulator := apicompat.NewBufferedResponseAccumulator()
	streamImageOutputs := make([]json.RawMessage, 0, 1)
	streamSeenImages := make(map[string]struct{})
	resultWithUsage := func() *openaiStreamingResult {
		clientWriteMu.Lock()
		fakeMs := fakeFirstTokenMs
		clientWriteMu.Unlock()
		return &openaiStreamingResult{
			usage:            usage,
			firstTokenMs:     firstTokenMs,
			fakeFirstTokenMs: fakeMs,
			responseID:       responseID,
			serviceTier:      responseServiceTier,
			imageCount:       imageCounter.Count(),
			imageOutputSizes: imageCounter.Sizes(),
		}
	}
	finalizeStream := func() (*openaiStreamingResult, error) {
		if stageFirstOutput && eventInProgress {
			// 兼容上游最后一个 SSE（流式响应）事件缺少尾随空行；EOF 仍应
			// 把完整已读事件作为一个整体提交，不能丢掉最后一帧。
			completeGuardedEvent(true)
		}
		if !sawTerminalEvent {
			if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
				return resultWithUsage(), s.newOpenAIStreamFailoverError(
					c,
					account,
					false,
					upstreamRequestID,
					nil,
					"OpenAI stream ended before a terminal event",
				)
			}
			s.recordOpenAIProxyStreamDisconnect(account, errors.New("stream ended before terminal event"), upstreamRequestID)
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		if sawFailedEvent {
			return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage)
		}
		if !clientDisconnected {
			hadBufferedData := pendingBytes() > 0
			if err := flushBuffered(); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during final flush, returning collected usage")
			} else if hadBufferedData {
				clientOutputStarted = true
				fakeOutputStarted.Store(true)
				lastDownstreamWriteAt = time.Now()
			}
		}
		s.clearOpenAIProxyStreamDisconnect(account)
		return resultWithUsage(), nil
	}
	handleScanErr := func(scanErr error) (*openaiStreamingResult, error, bool) {
		if scanErr == nil {
			return nil, nil, false
		}
		if errors.Is(scanErr, errOpenAIFirstOutputScannerLimit) && !firstOutputProgressObserved {
			logger.LegacyPrintf(
				"service.openai_gateway",
				"SSE token exceeded guarded first-output limit: account=%d limit=%d error=%v",
				account.ID,
				openAIFirstOutputStageMaxBytes+openAIFirstOutputScannerFramingAllowance,
				scanErr,
			)
			return resultWithUsage(), s.newOpenAIStreamFailoverError(
				c,
				account,
				false,
				upstreamRequestID,
				nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			), true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) && stageFirstOutput && !firstOutputProgressObserved {
			logger.LegacyPrintf("service.openai_gateway", "SSE line too long before first output: account=%d max_size=%d error=%v", account.ID, maxLineSize, scanErr)
			return resultWithUsage(), s.newOpenAIStreamFailoverError(
				c,
				account,
				false,
				upstreamRequestID,
				nil,
				"OpenAI SSE line exceeds guarded first-output limit",
			), true
		}
		if sawTerminalEvent && !sawFailedEvent {
			logger.LegacyPrintf("service.openai_gateway", "Upstream scan ended after terminal event: %v", scanErr)
			result, err := finalizeStream()
			return result, err, true
		}
		if sawFailedEvent {
			return resultWithUsage(), fmt.Errorf("upstream response failed: %s", failedMessage), true
		}
		// 客户端断开/取消请求时，上游读取往往会返回 context canceled。
		// /v1/responses 的 SSE 事件必须符合 OpenAI 协议；这里不注入自定义 error event，避免下游 SDK 解析失败。
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", scanErr), true
		}
		if errors.Is(scanErr, bufio.ErrTooLong) {
			logger.LegacyPrintf("service.openai_gateway", "SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, scanErr)
			sendErrorEvent("response_too_large")
			return resultWithUsage(), scanErr, true
		}
		if !openAIStreamClientOutputStarted(c, clientOutputStarted) && !eventShouldFlush {
			msg := "OpenAI stream disconnected before completion"
			if errText := strings.TrimSpace(scanErr.Error()); errText != "" {
				msg += ": " + errText
			}
			return resultWithUsage(), s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, nil, msg), true
		}
		// 客户端已断开时，上游出错仅影响体验，不影响计费；返回已收集 usage
		if clientDisconnected {
			return resultWithUsage(), fmt.Errorf("stream usage incomplete after disconnect: %w", scanErr), true
		}
		s.recordOpenAIProxyStreamDisconnect(account, scanErr, upstreamRequestID)
		sendErrorEvent("stream_read_error")
		return resultWithUsage(), fmt.Errorf("stream read error: %w", scanErr), true
	}
	processSSELine := func(line string, queueDrained bool) {
		if streamFailoverErr != nil {
			return
		}
		noteFakeFirstTokenFromUpstreamLine(line, &fakeFirstTokenMs, startTime)
		if eventType, ok := extractOpenAISSEEventLine(line); ok {
			currentSSEEventType = eventType
		}
		// Extract data from SSE line (supports both "data: " and "data:" formats)
		if data, ok := extractOpenAISSEDataLine(line); ok {
			dataBytes := []byte(data)
			eventType := resolveOpenAIResponsesSSEEventType(dataBytes, currentSSEEventType)
			upstreamResponseModelObserverFromContext(c).ObserveOpenAI(dataBytes, eventType)
			if openAIStreamEventIsTerminalWithType(data, eventType) {
				sawTerminalEvent = true
			}
			if responseID == "" {
				responseID = extractOpenAIResponseIDFromJSONBytes(dataBytes)
			}
			if observed := extractOpenAIServiceTierFromResponseJSONBytes(dataBytes); observed != nil {
				responseServiceTier = observed
			}
			forceFlushFailedEvent := false
			if !capacityFailoverSuppressedLogged && account != nil && account.Platform == PlatformOpenAI &&
				(eventType == "error" || eventType == "response.failed") &&
				openAIStreamClientOutputStarted(c, clientOutputStarted) &&
				isOpenAIUpstreamCapacityShedEvent(dataBytes) {
				logOpenAICapacityFailoverSuppressed(ctx, account, "native_sse", upstreamRequestID, eventType)
				capacityFailoverSuppressedLogged = true
			}
			if eventType == "error" &&
				!firstOutputProgressObserved && !eventStartsClientOutput &&
				!openAIStreamClientOutputStarted(c, clientOutputStarted) {
				errorMessage := extractOpenAISSEErrorMessage(dataBytes)
				if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(
					c, account.Platform, dataBytes, errorMessage,
				); matched {
					s.recordOpenAIStreamUpstreamError(c, account, false, upstreamRequestID, "http_error", dataBytes, errorMessage)
					MarkResponseCommitted(c)
					c.Header("Content-Type", "application/json; charset=utf-8")
					c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": errMsg}})
					streamFailoverErr = fmt.Errorf("upstream error event: passthrough rule matched message=%s", errMsg)
					return
				}
				if openAIStreamErrorEventShouldFailover(dataBytes, errorMessage) {
					streamFailoverErr = s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, dataBytes, errorMessage, resp.Header)
					return
				}
			}
			if eventType == "response.failed" {
				failedMessage = extractOpenAISSEErrorMessage(dataBytes)
				// response.failed 自带上游已消耗的 usage（input token 通常已扣）；必须先解析
				// 再打 cyber 标记，否则 mark 记到的是解析前的 0，导致流式 cyber 按 0 token 计费
				// 而漏记真实用量。对齐 WS V2 / Chat 流式路径（均先解析 usage 再 Mark）。
				s.parseSSEUsageBytes(dataBytes, usage)
				cyberHit, code, msg := detectOpenAICyberPolicy(dataBytes)
				if cyberHit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(string(dataBytes), 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
				}
				if !firstOutputProgressObserved && !eventStartsClientOutput &&
					!openAIStreamClientOutputStarted(c, clientOutputStarted) {
					if status, errType, errMsg, matched := applyOpenAIStreamFailedErrorPassthroughRule(
						c, account.Platform, dataBytes, failedMessage,
					); matched {
						sawFailedEvent = true
						s.recordOpenAIStreamUpstreamError(c, account, false, upstreamRequestID, "http_error", dataBytes, failedMessage)
						MarkResponseCommitted(c)
						c.Header("Content-Type", "application/json; charset=utf-8")
						c.JSON(status, gin.H{"error": gin.H{"type": errType, "message": errMsg}})
						streamFailoverErr = fmt.Errorf("upstream response failed: passthrough rule matched message=%s", errMsg)
						return
					}
					if !cyberHit && openAIStreamFailedEventShouldFailover(dataBytes, failedMessage) {
						sawFailedEvent = true
						streamFailoverErr = s.newOpenAIStreamFailoverError(c, account, false, upstreamRequestID, dataBytes, failedMessage, resp.Header)
						return
					}
				}
				forceFlushFailedEvent = true
				sawFailedEvent = true
			}
			if normalizedData, normalized := normalizeCompletedImageGenerationStatus(dataBytes); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			imageCounter.AddSSEData(dataBytes)

			// Correct Codex tool calls if needed (apply_patch -> edit, etc.)
			if correctedData, corrected := s.toolCorrector.CorrectToolCallsInSSEBytes(dataBytes); corrected {
				dataBytes = correctedData
				data = string(correctedData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			if imageOutput, ok := extractImageGenerationOutputFromSSEData(dataBytes, streamSeenImages); ok {
				streamImageOutputs = append(streamImageOutputs, imageOutput)
			}
			if responsesStreamEventMayContributeToOutput(eventType) {
				var streamEvent apicompat.ResponsesStreamEvent
				if err := json.Unmarshal(dataBytes, &streamEvent); err == nil {
					streamOutputAccumulator.ProcessEvent(&streamEvent)
				}
			}
			if normalizedData, normalized := normalizeResponsesStreamingTerminalOutput(dataBytes, streamOutputAccumulator, streamImageOutputs); normalized {
				dataBytes = normalizedData
				data = string(normalizedData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			restoredData, restoreErr := restoreGrokResponsesClientToolPayload(c, dataBytes)
			if restoreErr != nil {
				streamFailoverErr = fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
				return
			}
			restoredData, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredData)
			if restoreErr != nil {
				streamFailoverErr = fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
				return
			}
			restoredData = restoreCodexToolNamesFromSSEContext(c, restoredData, eventType)
			if !bytes.Equal(restoredData, dataBytes) {
				dataBytes = restoredData
				data = string(restoredData)
				line = "data: " + data
				eventType = strings.TrimSpace(gjson.GetBytes(dataBytes, "type").String())
			}
			if sanitizedData, sanitized := sanitizeOpenAIResponseFailedEventForClient(
				dataBytes,
				eventType,
				openAIStreamClientOutputStarted(c, clientOutputStarted),
			); sanitized {
				dataBytes = sanitizedData
				data = string(sanitizedData)
				line = "data: " + data
			}
			// Replace model in response if needed.
			// Fast path: most events do not contain model field values.
			line = rewriteResponseModelSSELineForClient(c, account, originalModel, line)
			startsClientOutput := forceFlushFailedEvent || openAIStreamDataStartsClientOutput(data, eventType)
			startsVisibleOutput := openAIStreamDataStartsVisibleOutput(data, eventType)
			if stageFirstOutput {
				eventStartsClientOutput = eventStartsClientOutput || startsClientOutput
				eventStartsVisibleOutput = eventStartsVisibleOutput || startsVisibleOutput
				if startsClientOutput {
					firstOutputScanGuard.Store(false)
				}
			}
			if startsClientOutput && !openAIStreamEventTypeIsTerminal(eventType) {
				responsesSemanticOutputSeen = true
			}
			if account != nil && account.Platform == PlatformOpenAI &&
				(eventType == "response.completed" || eventType == "response.done") &&
				!sawFailedEvent && !responsesSemanticOutputSeen && !clientOutputStarted &&
				!firstOutputProgressObserved && !eventStartsClientOutput &&
				openAIResponsesCompletedEventIsEmpty(dataBytes, usage) {
				sawTerminalEvent = true
				streamFailoverErr = newOpenAIResponsesEmptyCompletedFailoverError(c, account, upstreamRequestID)
				return
			}

			// 写入客户端（客户端断开后继续 drain 上游）
			if !clientDisconnected {
				shouldFlush := queueDrained && (clientOutputStarted || startsClientOutput)
				if firstTokenMs == nil && startsVisibleOutput {
					// 保证首个 token 事件尽快出站，避免影响 TTFT。
					shouldFlush = true
				}
				eventShouldFlush = eventShouldFlush || shouldFlush
				if _, err := writePendingString(line); err != nil {
					handlePendingWriteError(err)
				} else if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				} else {
					eventInProgress = true
				}
			}

			// Record first token time
			if !stageFirstOutput && firstTokenMs == nil && startsVisibleOutput {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			s.parseSSEUsageBytes(dataBytes, usage)
			return
		}

		// 空行是 SSE（流式响应）的事件提交边界；首输出暂存、普通缓冲和
		// keepalive（心跳）都只能在这里提交，不能把 event/data 半帧拆开。
		if stageFirstOutput && line == "" {
			if !clientDisconnected {
				if _, err := writePendingString("\n"); err != nil {
					handlePendingWriteError(err)
				}
			}
			if streamFailoverErr == nil {
				completeGuardedEvent(queueDrained)
			}
			currentSSEEventType = ""
			return
		}

		shouldFlush := false
		if line == "" {
			shouldFlush = eventShouldFlush || (queueDrained && clientOutputStarted)
			eventShouldFlush = false
		}
		// Forward non-data lines as-is
		if !clientDisconnected {
			if _, err := writePendingString(line); err != nil {
				handlePendingWriteError(err)
			} else if _, err := writePendingString("\n"); err != nil {
				handlePendingWriteError(err)
			} else {
				eventInProgress = line != ""
				if shouldFlush {
					if err := flushBuffered(); err != nil {
						clientDisconnected = true
						logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming flush, continuing to drain upstream for billing")
					} else {
						clientOutputStarted = true
						fakeOutputStarted.Store(true)
						lastDownstreamWriteAt = time.Now()
					}
				}
			}
		}
		if line == "" {
			currentSSEEventType = ""
		}
	}

	// 无超时/无 keepalive 的常见路径走同步扫描，减少 goroutine 与 channel 开销。
	if streamInterval <= 0 && keepaliveInterval <= 0 && firstOutputTimeout <= 0 {
		defer putSSEScannerBuf64K(scanBuf)
		for documentScanner.Scan() {
			processSSELine(documentScanner.Text(), true)
			if streamFailoverErr != nil {
				return resultWithUsage(), streamFailoverErr
			}
		}
		if result, err, done := handleScanErr(documentScanner.Err()); done {
			return result, err
		}
		return finalizeStream()
	}

	type scanEvent struct {
		line      string
		err       error
		processed chan struct{}
	}
	// 独立 goroutine 读取上游，避免读取阻塞影响 keepalive/超时处理
	// 首输出保护期间每次只允许一个待处理 token；结合动态 Scanner 上限，
	// 使异常上游无法借 16 个大帧把一次请求放大为数 GiB 内存占用。
	events := make(chan scanEvent, openAIFirstOutputEventQueueSize(guardFirstOutput))
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
		if firstOutputScanGuard.Load() {
			ev.processed = make(chan struct{})
		}
		select {
		case events <- ev:
		case <-done:
			return false
		}
		if ev.processed == nil {
			return true
		}
		select {
		case <-ev.processed:
			return true
		case <-done:
			return false
		}
	}
	markEventProcessed := func(ev scanEvent) {
		if ev.processed != nil {
			close(ev.processed)
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func(scanBuf *sseScannerBuf64K) {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		for documentScanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: documentScanner.Text()}) {
				return
			}
		}
		if err := documentScanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}(scanBuf)
	defer close(done)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return finalizeStream()
			}
			if result, err, done := handleScanErr(ev.err); done {
				markEventProcessed(ev)
				return result, err
			}
			processSSELine(ev.line, len(events) == 0)
			markEventProcessed(ev)
			if streamFailoverErr != nil {
				return resultWithUsage(), streamFailoverErr
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.LegacyPrintf("service.openai_gateway", "Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			// 处理流超时，可能标记账户为临时不可调度或错误状态
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			// 尚未向客户输出有效内容时才能换号重放；已经输出后仍返回原有
			// stream_timeout，避免两个账号的内容拼到同一条流里并重复计费。
			if account != nil && account.Platform == PlatformGrok {
				s.tempUnscheduleGrok(ctx, account, grokStreamIdleCooldown, "grok stream idle timeout")
				if !openAIStreamClientOutputStarted(c, clientOutputStarted) {
					_ = resp.Body.Close()
					return resultWithUsage(), grokStreamIdleFailoverError(account, streamInterval)
				}
			}
			sendErrorEvent("stream_timeout")
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-firstOutputCh:
			if firstOutputProgressObserved {
				stopFirstOutputTimer()
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after first output timeout")
			}
			logger.LegacyPrintf(
				"service.openai_gateway",
				"OpenAI first output timeout: account=%d model=%s timeout=%s request_id=%s",
				account.ID,
				originalModel,
				firstOutputTimeout,
				upstreamRequestID,
			)
			_ = resp.Body.Close()
			for event := range events {
				markEventProcessed(event)
			}
			return resultWithUsage(), s.newOpenAIStreamFailoverError(
				c,
				account,
				false,
				upstreamRequestID,
				nil,
				"OpenAI stream first output timeout",
			)

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if eventInProgress {
				continue
			}
			if time.Since(lastDownstreamWriteAt) < keepaliveInterval {
				continue
			}
			if firstOutputStage != nil && !firstOutputStage.closed {
				// 心跳只写注释行，不提交 attempt（尝试）响应头：心跳可以提交
				// HTTP 200，但失败 attempt 的账号响应头必须保持私有。
				clientWriteMu.Lock()
				n, err := w.Write([]byte(":\n\n"))
				if err == nil {
					flusher.Flush()
				}
				clientWriteMu.Unlock()
				recordOpenAIStreamKeepaliveBytes(c, n)
				if err != nil {
					clientDisconnected = true
					logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
					continue
				}
				lastDownstreamWriteAt = time.Now()
				continue
			}
			if _, err := writePendingString(":\n\n"); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during streaming, continuing to drain upstream for billing")
				continue
			}
			if err := flushBuffered(); err != nil {
				clientDisconnected = true
				logger.LegacyPrintf("service.openai_gateway", "Client disconnected during keepalive flush, continuing to drain upstream for billing")
			} else {
				lastDownstreamWriteAt = time.Now()
			}
		}
	}

}

// extractOpenAISSEDataLine 低开销提取 SSE `data:` 行内容。
// 兼容 `data: xxx` 与 `data:xxx` 两种格式。
func extractOpenAISSEDataLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	start := len("data:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return line[start:], true
}

func extractOpenAISSEEventLine(line string) (string, bool) {
	if !strings.HasPrefix(line, "event:") {
		return "", false
	}
	start := len("event:")
	for start < len(line) {
		if line[start] != ' ' && line[start] != '	' {
			break
		}
		start++
	}
	return strings.TrimSpace(line[start:]), true
}

// resolveOpenAIResponsesSSEEventType keeps the JSON type authoritative while
// accepting the standard SSE event: field as a fallback for compatible
// upstreams that do not duplicate it inside data.
func resolveOpenAIResponsesSSEEventType(data []byte, sseEventType string) string {
	if eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String()); eventType != "" {
		return eventType
	}
	return strings.TrimSpace(sseEventType)
}

type openAICompatSSEFrame struct {
	EventType string
	Data      string
}

type openAICompatSSEFrameParser struct {
	eventType string
	dataLines []string
}

func (p *openAICompatSSEFrameParser) AddLine(line string) (openAICompatSSEFrame, bool) {
	if line == "" {
		return p.dispatch()
	}
	if strings.HasPrefix(line, ":") {
		return openAICompatSSEFrame{}, false
	}
	if eventType, ok := extractOpenAISSEEventLine(line); ok {
		p.eventType = eventType
		return openAICompatSSEFrame{}, false
	}
	if data, ok := extractOpenAISSEDataLine(line); ok {
		p.dataLines = append(p.dataLines, data)
	}
	return openAICompatSSEFrame{}, false
}

func (p *openAICompatSSEFrameParser) Finish() (openAICompatSSEFrame, bool) {
	return p.dispatch()
}

func (p *openAICompatSSEFrameParser) dispatch() (openAICompatSSEFrame, bool) {
	frame := openAICompatSSEFrame{
		EventType: p.eventType,
		Data:      strings.Join(p.dataLines, "\n"),
	}
	p.eventType = ""
	p.dataLines = nil
	return frame, frame.Data != ""
}

func openAICompatPayloadWithEventType(payload, eventType string) string {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || strings.TrimSpace(payload) == "" || strings.TrimSpace(payload) == "[DONE]" {
		return payload
	}
	if gjson.Get(payload, "type").Exists() {
		return payload
	}
	patched, err := sjson.Set(payload, "type", eventType)
	if err != nil {
		return payload
	}
	return patched
}

func (s *OpenAIGatewayService) replaceModelInSSELine(line, fromModel, toModel string) string {
	data, ok := extractOpenAISSEDataLine(line)
	if !ok {
		return line
	}
	if data == "" || data == "[DONE]" {
		return line
	}

	// 使用 gjson 精确检查 model 字段，避免全量 JSON 反序列化
	if m := gjson.Get(data, "model"); m.Exists() && m.Str == fromModel {
		newData, err := sjson.Set(data, "model", toModel)
		if err != nil {
			return line
		}
		return "data: " + newData
	}

	// 检查嵌套的 response.model 字段
	if m := gjson.Get(data, "response.model"); m.Exists() && m.Str == fromModel {
		newData, err := sjson.Set(data, "response.model", toModel)
		if err != nil {
			return line
		}
		return "data: " + newData
	}

	return line
}

// correctToolCallsInResponseBody 修正响应体中的工具调用
func (s *OpenAIGatewayService) correctToolCallsInResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(body)
	if changed {
		return corrected
	}
	return body
}

func (s *OpenAIGatewayService) parseSSEUsage(data string, usage *OpenAIUsage) {
	s.parseSSEUsageBytes([]byte(data), usage)
}

func (s *OpenAIGatewayService) parseSSEUsageBytes(data []byte, usage *OpenAIUsage) {
	if usage == nil || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	// 选择性解析：仅在数据中包含终止事件标识时才进入字段提取。
	if len(data) < 72 {
		return
	}
	eventType := gjson.GetBytes(data, "type").String()
	if eventType != "response.completed" && eventType != "response.done" && eventType != "response.failed" &&
		eventType != "response.incomplete" && eventType != "response.cancelled" && eventType != "response.canceled" {
		return
	}

	if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(data); ok {
		*usage = parsedUsage
	}
}

func extractOpenAIUsageFromJSONBytes(body []byte) (OpenAIUsage, bool) {
	// 旧版 OAuth 响应只在 image_generation_call.usage 给出完整生图总量，
	// 这时沿用本地的去重聚合；v0.1.163 新增的 hosted tool_usage 则只补
	// 图片 Token，不覆盖 response.usage 的文本/编排总量。
	if gjson.GetBytes(body, `response.output.#(type=="image_generation_call")`).Exists() ||
		gjson.GetBytes(body, `output.#(type=="image_generation_call")`).Exists() {
		if usage, ok := openai.ExtractResponseUsage(body); ok {
			return openAIUsageFromProtocol(usage), true
		}
	}
	candidates := []struct {
		usagePath      string
		imageUsagePath string
	}{
		{usagePath: "usage", imageUsagePath: "tool_usage.image_gen"},
		{usagePath: "response.usage", imageUsagePath: "response.tool_usage.image_gen"},
		{usagePath: "data.usage", imageUsagePath: "data.tool_usage.image_gen"},
		{usagePath: "data.response.usage", imageUsagePath: "data.response.tool_usage.image_gen"},
	}
	for _, candidate := range candidates {
		if usage, ok := openAIUsageFromGJSON(gjson.GetBytes(body, candidate.usagePath)); ok {
			mergeHostedImageGenToolUsage(gjson.GetBytes(body, candidate.imageUsagePath), &usage)
			return usage, true
		}
	}
	return OpenAIUsage{}, false
}

func openAIResponsesCompletedEventIsEmpty(data []byte, usage *OpenAIUsage) bool {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return false
	}
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0 ||
		usage.ImageInputTokens > 0 || usage.ImageOutputTokens > 0 ||
		usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0) {
		return false
	}
	for _, path := range []string{"usage", "response.usage", "data.usage", "data.response.usage"} {
		if gjson.GetBytes(data, path).Exists() {
			return false
		}
	}
	if gjson.GetBytes(data, "error").Exists() || gjson.GetBytes(data, "response.error").Exists() {
		return false
	}
	if output := gjson.GetBytes(data, "response.output"); output.Exists() && output.IsArray() && len(output.Array()) > 0 {
		return false
	}
	return true
}

// hosted image_generation（托管生图工具）的图片 Token 位于 tool_usage，
// 不在标准 usage 内；只在标准字段尚未给出图片 Token 时补齐，防止重复相加。
func mergeHostedImageGenToolUsage(imageGen gjson.Result, usage *OpenAIUsage) {
	if usage == nil || !imageGen.Exists() || !imageGen.IsObject() {
		return
	}
	if usage.ImageOutputTokens == 0 {
		if value := imageGen.Get("output_tokens_details.image_tokens").Int(); value > 0 {
			usage.ImageOutputTokens = int(value)
		}
	}
	if usage.ImageInputTokens == 0 {
		if value := imageGen.Get("input_tokens_details.image_tokens").Int(); value > 0 {
			usage.ImageInputTokens = int(value)
		}
	}
}

func extractOpenAIResponseIDFromJSONBytes(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	if id := strings.TrimSpace(gjson.GetBytes(body, "id").String()); id != "" {
		return id
	}
	return strings.TrimSpace(gjson.GetBytes(body, "response.id").String())
}

// extractOpenAIServiceTierFromResponseJSONBytes reads the processing tier
// actually reported by OpenAI. That value may differ from the request and is
// therefore authoritative for billing when present.
func extractOpenAIServiceTierFromResponseJSONBytes(body []byte) *string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil
	}
	if tier := extractOpenAIResponseServiceTierValue(gjson.GetBytes(body, "service_tier")); tier != nil {
		return tier
	}
	return extractOpenAIResponseServiceTierValue(gjson.GetBytes(body, "response.service_tier"))
}

func extractOpenAIResponseServiceTierValue(value gjson.Result) *string {
	if !value.Exists() {
		return nil
	}
	if value.Type != gjson.String {
		marker := "invalid_type"
		return &marker
	}
	raw := strings.ToLower(strings.TrimSpace(value.String()))
	if raw == "" {
		marker := "invalid_empty"
		return &marker
	}
	// usage_logs.response_service_tier is intentionally compact. Preserve a
	// short unknown value for admin diagnosis, and use a stable marker for an
	// oversized value so malformed upstream data cannot break log insertion.
	if len(raw) > 16 {
		marker := "invalid_value"
		return &marker
	}
	return &raw
}

type openAIServiceTierDetectionPolicy struct {
	ByRequest  bool
	ByResponse bool
}

func (decision OpenAIPriorityDecision) detectionPolicy() openAIServiceTierDetectionPolicy {
	policy := openAIServiceTierDetectionPolicy{
		ByRequest:  decision.DetectionByRequestEnabled,
		ByResponse: decision.DetectionByResponseEnabled,
	}
	if !policy.ByRequest && !policy.ByResponse {
		policy.ByRequest = true
		policy.ByResponse = true
	}
	return policy
}

func resolveOpenAIEffectiveServiceTier(sent, observed *string, policies ...openAIServiceTierDetectionPolicy) *string {
	tier, _ := resolveOpenAIEffectiveServiceTierWithSource(sent, observed, policies...)
	return tier
}

func resolveOpenAIEffectiveServiceTierWithSource(sent, observed *string, policies ...openAIServiceTierDetectionPolicy) (*string, string) {
	policy := openAIServiceTierDetectionPolicy{ByRequest: true, ByResponse: true}
	if len(policies) > 0 {
		policy = policies[0]
		if !policy.ByRequest && !policy.ByResponse {
			policy = openAIServiceTierDetectionPolicy{ByRequest: true, ByResponse: true}
		}
	}
	responseInvalid := false
	if policy.ByResponse && observed != nil {
		if normalized := normalizeOpenAIServiceTier(*observed); normalized != nil {
			return normalized, "response"
		}
		responseInvalid = true
	}
	if policy.ByRequest && sent != nil {
		if normalized := normalizeOpenAIServiceTier(*sent); normalized != nil {
			if responseInvalid {
				return normalized, "request_response_invalid"
			}
			return normalized, "request"
		}
	}
	if responseInvalid {
		return nil, "standard_response_invalid"
	}
	return nil, "standard_default"
}

func applyOpenAIPriorityDecisionToResult(result *OpenAIForwardResult, decision OpenAIPriorityDecision, observed *string) {
	if result == nil {
		return
	}
	result.SentServiceTier = cloneOptionalString(decision.ServiceTier)
	result.ResponseServiceTier = cloneOptionalString(observed)
	result.ServiceTier, result.PriorityDetectionSource = resolveOpenAIEffectiveServiceTierWithSource(
		decision.ServiceTier,
		observed,
		decision.detectionPolicy(),
	)
	result.RequestedServiceTier = cloneOptionalString(decision.RequestedServiceTier)
	result.PriorityPolicyMode = ""
	result.PriorityPolicyAction = strings.TrimSpace(decision.PolicyAction)
	result.PriorityPolicyRuleID = strings.TrimSpace(decision.PolicyRuleID)
	result.PriorityPolicyFallback = decision.PolicyRuleFallback
}

func logOpenAIServiceTierOverride(ctx context.Context, account *Account, model string, sent, observed *string) {
	if sent == nil || observed == nil {
		return
	}
	if normalizeBillingServiceTier(*sent) == normalizeBillingServiceTier(*observed) {
		return
	}
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.String("model", strings.TrimSpace(model)),
		zap.String("sent_service_tier", strings.TrimSpace(*sent)),
		zap.String("observed_service_tier", strings.TrimSpace(*observed)),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.FromContext(ctx).With(fields...).Warn("openai_service_tier.upstream_override")
}

func (s *OpenAIGatewayService) bindHTTPResponseAccount(ctx context.Context, c *gin.Context, account *Account, responseID string) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return
	}
	groupID := getOpenAIGroupIDFromContext(c)
	ttl := s.openAIWSResponseStickyTTL()
	logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, store.BindResponseAccount(ctx, groupID, responseID, account.ID, ttl))
}

func openAIUsageFromGJSON(value gjson.Result) (OpenAIUsage, bool) {
	usage, ok := openai.ParseUsageObject(value)
	if !ok {
		return OpenAIUsage{}, false
	}
	return openAIUsageFromProtocol(usage), true
}

func openAIUsageFromProtocol(usage openai.ResponseUsage) OpenAIUsage {
	return OpenAIUsage{
		InputTokens:              usage.InputTokens,
		ImageInputTokens:         usage.ImageInputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		ImageOutputTokens:        usage.ImageOutputTokens,
	}
}

func (s *OpenAIGatewayService) handleNonStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, originalModel, mappedModel string) (*openaiNonStreamingResult, error) {
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}

	// Detect SSE responses for ALL account types via Content-Type header.
	// Some OpenAI-compatible upstreams (including other sub2api instances)
	// may return SSE even when stream=false was requested.
	if isEventStreamResponse(resp.Header) {
		return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
	}
	bodyLooksLikeSSE := bodyHasSSEFraming(body)
	upstreamResponseModelObserverFromContext(c).ObserveOpenAI(body, "response.completed")

	// For OAuth accounts, also fall back to a body-content heuristic because
	// the upstream may omit the Content-Type header while still sending SSE.
	// This heuristic is NOT applied to API-key accounts to avoid false
	// positives on JSON responses that coincidentally contain "data:" or
	// "event:" in their text content.
	if account.Type == AccountTypeOAuth && bodyLooksLikeSSE {
		return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
	}
	if account != nil && account.IsGrok() && isOpenAIResponsesCompactPath(c) {
		body, err = convertGrokResponseToOpenAICompact(body)
		if err != nil {
			return nil, fmt.Errorf("convert Grok compact response: %w", err)
		}
	}

	usageValue, usageOK := extractOpenAIUsageFromJSONBytes(body)
	if !usageOK {
		if bodyLooksLikeSSE {
			return s.handleSSEToJSON(resp, c, account, body, originalModel, mappedModel)
		}
		return nil, fmt.Errorf("parse response: invalid json response")
	}
	usage := &usageValue

	// Replace model in response if needed
	body = rewriteResponseModelForClient(c, account, originalModel, body)
	body, err = restoreGrokResponsesClientToolPayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore Grok Responses client tool response: %w", err)
	}
	body, err = restoreOpenAIResponsesNamespacePayload(c, body)
	if err != nil {
		return nil, fmt.Errorf("restore OpenAI namespace response: %w", err)
	}
	body = restoreCodexToolNamesFromContext(c, body)

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	s.relayOpenAICodexTurnState(c, account, resp.Header)

	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResult{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		serviceTier:      extractOpenAIServiceTierFromResponseJSONBytes(body),
		imageCount:       countOpenAIResponseImageOutputsFromJSONBytes(body),
		imageOutputSizes: collectOpenAIResponseImageOutputSizesFromJSONBytes(body),
	}, nil
}

func isEventStreamResponse(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

// bodyHasSSEFraming only treats data:/event: at the beginning of a physical line
// as SSE. JSON text may legitimately contain those words inside a quoted string.
func bodyHasSSEFraming(body []byte) bool {
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if bytes.HasPrefix(line, []byte("data:")) || bytes.HasPrefix(line, []byte("event:")) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) handleSSEToJSON(resp *http.Response, c *gin.Context, account *Account, body []byte, originalModel, mappedModel string) (*openaiNonStreamingResult, error) {
	bodyText := string(body)
	observer := upstreamResponseModelObserverFromContext(c)
	forEachOpenAISSEDataPayload(bodyText, func(payload []byte) {
		observer.ObserveOpenAI(payload, strings.TrimSpace(gjson.GetBytes(payload, "type").String()))
	})
	finalResponse, ok := extractCodexFinalResponse(bodyText)

	usage := &OpenAIUsage{}
	if ok {
		if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(finalResponse); parsed {
			*usage = parsedUsage
		}
		// When the terminal event has an empty output array, reconstruct
		// output from accumulated delta events so the client gets full content.
		// gjson Array() returns empty slice for null, missing, or empty arrays.
		if len(gjson.GetBytes(finalResponse, "output").Array()) == 0 {
			if outputJSON, reconstructed := reconstructResponseOutputFromSSE(bodyText); reconstructed {
				if patched, err := sjson.SetRawBytes(finalResponse, "output", outputJSON); err == nil {
					finalResponse = patched
				}
			}
		}
		finalResponse = supplementCompactionItemFromSSE(c, finalResponse, bodyText)
		body = finalResponse
		body = rewriteResponseModelForClient(c, nil, originalModel, body)
		// Correct tool calls in final response
		body = s.correctToolCallsInResponseBody(body)
		restoredBody, restoreErr := restoreGrokResponsesClientToolPayload(c, body)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
		}
		restoredBody, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredBody)
		if restoreErr != nil {
			return nil, fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
		}
		restoredBody = restoreCodexToolNamesFromContext(c, restoredBody)
		body = restoredBody
	} else {
		terminalType, terminalPayload, terminalOK := extractOpenAISSETerminalEvent(bodyText)
		if terminalOK && terminalType == "response.failed" {
			msg := extractOpenAISSEErrorMessage(terminalPayload)
			if msg == "" {
				msg = "Upstream compact response failed"
			}
			return nil, s.writeOpenAINonStreamingProtocolError(resp, c, msg)
		}
		usage = s.parseSSEUsageFromBody(bodyText)
		bodyText = rewriteResponseModelSSEBodyForClient(c, nil, originalModel, bodyText)
		body = []byte(bodyText)
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	s.relayOpenAICodexTurnState(c, account, resp.Header)

	contentType := "application/json; charset=utf-8"
	if !ok {
		contentType = resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
	}
	if !writeOpenAICompactSSEBridge(c, resp.StatusCode, body) {
		c.Data(resp.StatusCode, contentType, body)
	}

	return &openaiNonStreamingResult{
		OpenAIUsage:      usage,
		usage:            usage,
		responseID:       extractOpenAIResponseIDFromJSONBytes(body),
		serviceTier:      extractOpenAIServiceTierFromResponseJSONBytes(body),
		imageCount:       countOpenAIImageOutputsFromSSEBody(bodyText),
		imageOutputSizes: collectOpenAIImageOutputSizesFromSSEBody(bodyText),
	}, nil
}

func extractOpenAISSETerminalEvent(body string) (string, []byte, bool) {
	var terminalType string
	var terminalPayload []byte
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		if terminalPayload != nil {
			return
		}
		eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
		switch eventType {
		case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
			terminalType = eventType
			terminalPayload = append([]byte(nil), data...)
		}
	})
	if terminalPayload != nil {
		return terminalType, terminalPayload, true
	}
	return "", nil, false
}

func extractOpenAISSEErrorMessage(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range []string{"response.error.message", "error.message", "message"} {
		if msg := strings.TrimSpace(gjson.GetBytes(payload, path).String()); msg != "" {
			return sanitizeUpstreamErrorMessage(msg)
		}
	}
	return sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
}

func sanitizeOpenAIResponseFailedEventForClient(payload []byte, eventType string, clientOutputStarted bool) ([]byte, bool) {
	eventType = strings.TrimSpace(eventType)
	isFailedEvent := eventType == "response.failed"
	if (!isFailedEvent && eventType != "error") || len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload, false
	}
	updated := payload
	if rewritten, changed := sanitizeOpenAICapacityShedErrorCodeForClient(updated); changed {
		updated = rewritten
	}
	if !isFailedEvent {
		return updated, !bytes.Equal(updated, payload)
	}
	if clientOutputStarted && isOpenAIContextWindowError(extractOpenAISSEErrorMessage(payload), payload) {
		errorPath := ""
		switch {
		case gjson.GetBytes(updated, "response.error").Exists():
			errorPath = "response.error"
		case gjson.GetBytes(updated, "error").Exists():
			errorPath = "error"
		}
		if errorPath != "" {
			next, err := sjson.SetBytes(updated, errorPath+".type", "invalid_request_error")
			if err != nil {
				return payload, false
			}
			updated = next
			next, err = sjson.SetBytes(updated, errorPath+".code", "context_length_exceeded")
			if err != nil {
				return payload, false
			}
			updated = next
		}
	}
	if !gjson.GetBytes(updated, "response").Exists() {
		return updated, !bytes.Equal(updated, payload)
	}
	for _, path := range []string{
		"response.instructions",
		"response.output",
		"response.usage",
		"response.metadata",
		"response.reasoning",
		"response.tools",
		"response.tool_choice",
		"response.parallel_tool_calls",
		"response.text",
		"response.truncation",
		"response.max_output_tokens",
		"response.incomplete_details",
	} {
		next, err := sjson.DeleteBytes(updated, path)
		if err != nil {
			return payload, false
		}
		updated = next
	}
	return updated, !bytes.Equal(updated, payload)
}

func (s *OpenAIGatewayService) writeOpenAINonStreamingProtocolError(resp *http.Response, c *gin.Context, message string) error {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "Upstream returned an invalid non-streaming response"
	}
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	if openAICompactClientWantsStream(c) && StopOpenAICompactSSEKeepaliveCommitted(c) {
		writeOpenAICompactSSEFailureMessage(c, http.StatusBadGateway, "upstream_error", message)
		return fmt.Errorf("non-streaming openai protocol error: %s", message)
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.JSON(http.StatusBadGateway, gin.H{
		"error": gin.H{
			"type":    "upstream_error",
			"message": message,
		},
	})
	return fmt.Errorf("non-streaming openai protocol error: %s", message)
}

func extractCodexFinalResponse(body string) ([]byte, bool) {
	var finalResponse []byte
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		if finalResponse != nil {
			return
		}
		if normalized, changed := normalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		eventType := gjson.GetBytes(data, "type").String()
		if eventType == "response.done" || eventType == "response.completed" {
			if response := gjson.GetBytes(data, "response"); response.Exists() && response.Type == gjson.JSON && response.Raw != "" {
				finalResponse = []byte(response.Raw)
			}
		}
	})
	if finalResponse != nil {
		return finalResponse, true
	}
	return nil, false
}

// normalizeCompletedImageGenerationStatus fixes an upstream inconsistency where
// a terminal Responses event already contains the final image result but still
// reports the image item as generating. Only terminal events with a non-empty
// result are changed so failed and genuinely unfinished calls remain visible.
func normalizeCompletedImageGenerationStatus(data []byte) ([]byte, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return data, false
	}

	shouldNormalize := func(item gjson.Result) bool {
		if !item.Exists() || !item.IsObject() ||
			strings.TrimSpace(item.Get("type").String()) != "image_generation_call" {
			return false
		}
		switch strings.TrimSpace(item.Get("status").String()) {
		case "generating", "in_progress":
			return strings.TrimSpace(item.Get("result").String()) != ""
		default:
			return false
		}
	}

	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.output_item.done":
		if !shouldNormalize(gjson.GetBytes(data, "item")) {
			return data, false
		}
		updated, err := sjson.SetBytes(data, "item.status", "completed")
		if err != nil {
			return data, false
		}
		return updated, true
	case "response.completed", "response.done":
		output := gjson.GetBytes(data, "response.output")
		if !output.Exists() || !output.IsArray() {
			return data, false
		}
		updated := data
		changed := false
		for i, item := range output.Array() {
			if !shouldNormalize(item) {
				continue
			}
			next, err := sjson.SetBytes(updated, "response.output."+strconv.Itoa(i)+".status", "completed")
			if err != nil {
				return data, false
			}
			updated = next
			changed = true
		}
		return updated, changed
	default:
		return data, false
	}
}

func normalizeResponsesStreamingTerminalOutput(data []byte, acc *apicompat.BufferedResponseAccumulator, imageOutputs []json.RawMessage) ([]byte, bool) {
	eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch eventType {
	case "response.completed", "response.done", "response.incomplete", "response.cancelled", "response.canceled":
	default:
		return data, false
	}

	output := gjson.GetBytes(data, "response.output")
	hasAccumulatedOutput := (acc != nil && acc.HasContent()) || len(imageOutputs) > 0
	if output.Exists() && output.IsArray() {
		if len(output.Array()) > 0 || !hasAccumulatedOutput {
			return data, false
		}
	}

	outputJSON := []byte("[]")
	if reconstructed, ok := buildResponsesOutputJSON(acc, imageOutputs); ok {
		outputJSON = reconstructed
	}
	updated, err := sjson.SetRawBytes(data, "response.output", outputJSON)
	if err != nil {
		return data, false
	}
	return updated, true
}

func responsesStreamEventMayContributeToOutput(eventType string) bool {
	switch eventType {
	case "response.output_text.delta",
		"response.output_item.added",
		"response.function_call_arguments.delta",
		"response.reasoning_summary_text.delta":
		return true
	default:
		return false
	}
}

// collectRawResponsesOutputItemsFromSSE preserves authoritative output_item.done
// payloads byte-for-byte. This keeps compact-specific and future opaque fields that
// the narrower compatibility accumulator does not understand.
func collectRawResponsesOutputItemsFromSSE(bodyText string) ([]byte, bool) {
	var items []json.RawMessage
	seen := make(map[string]struct{})
	hasCompactionItem := false
	appendItem := func(item gjson.Result) {
		if !item.Exists() || !item.IsObject() {
			return
		}
		key := strings.TrimSpace(item.Get("id").String())
		if key == "" {
			key = item.Raw
		}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		if isResponsesCompactionItemType(item.Get("type").String()) {
			hasCompactionItem = true
		}
		items = append(items, json.RawMessage(item.Raw))
	}
	forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
		if normalized, changed := normalizeCompletedImageGenerationStatus(data); changed {
			data = normalized
		}
		if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.done" {
			return
		}
		appendItem(gjson.GetBytes(data, "item"))
	})
	if !hasCompactionItem {
		forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
			if strings.TrimSpace(gjson.GetBytes(data, "type").String()) != "response.output_item.added" {
				return
			}
			item := gjson.GetBytes(data, "item")
			if isResponsesCompactionItemType(item.Get("type").String()) {
				appendItem(item)
			}
		})
	}
	if len(items) == 0 {
		return nil, false
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func isResponsesCompactionItemType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

// supplementCompactionItemFromSSE handles an upstream inconsistency where the
// terminal response has non-empty output but omits the compaction item emitted earlier.
func supplementCompactionItemFromSSE(c *gin.Context, finalResponse []byte, bodyText string) []byte {
	if !isOpenAIResponsesCompactPath(c) || len(gjson.GetBytes(finalResponse, "output").Array()) == 0 || responsesOutputHasCompactionItem(finalResponse) {
		return finalResponse
	}
	item, found := findRawCompactionItemFromSSE(bodyText)
	if !found {
		return finalResponse
	}
	patched, err := sjson.SetRawBytes(finalResponse, "output.-1", item)
	if err != nil {
		return finalResponse
	}
	return patched
}

func responsesOutputHasCompactionItem(response []byte) bool {
	for _, item := range gjson.GetBytes(response, "output").Array() {
		if isResponsesCompactionItemType(item.Get("type").String()) {
			return true
		}
	}
	return false
}

func findRawCompactionItemFromSSE(bodyText string) (json.RawMessage, bool) {
	var found json.RawMessage
	pick := func(eventType string) {
		forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
			if found != nil || strings.TrimSpace(gjson.GetBytes(data, "type").String()) != eventType {
				return
			}
			item := gjson.GetBytes(data, "item")
			if item.IsObject() && isResponsesCompactionItemType(item.Get("type").String()) {
				found = json.RawMessage(item.Raw)
			}
		})
	}
	pick("response.output_item.done")
	if found == nil {
		pick("response.output_item.added")
	}
	return found, found != nil
}

// reconstructResponseOutputFromSSE prefers final raw output items, then falls
// back to rebuilding known text, tool-call, reasoning and image delta types.
func reconstructResponseOutputFromSSE(bodyText string) ([]byte, bool) {
	if output, ok := collectRawResponsesOutputItemsFromSSE(bodyText); ok {
		return output, true
	}
	acc := apicompat.NewBufferedResponseAccumulator()
	imageOutputs := make([]json.RawMessage, 0, 1)
	seenImages := make(map[string]struct{})
	forEachOpenAISSEDataPayload(bodyText, func(data []byte) {
		if imageOutput, ok := extractImageGenerationOutputFromSSEData(data, seenImages); ok {
			imageOutputs = append(imageOutputs, imageOutput)
		}
		eventType := strings.TrimSpace(gjson.GetBytes(data, "type").String())
		if responsesStreamEventMayContributeToOutput(eventType) {
			var event apicompat.ResponsesStreamEvent
			if err := json.Unmarshal(data, &event); err == nil {
				acc.ProcessEvent(&event)
			}
		}
	})
	return buildResponsesOutputJSON(acc, imageOutputs)
}

func buildResponsesOutputJSON(acc *apicompat.BufferedResponseAccumulator, imageOutputs []json.RawMessage) ([]byte, bool) {
	if (acc == nil || !acc.HasContent()) && len(imageOutputs) == 0 {
		return nil, false
	}
	var output []json.RawMessage
	if acc != nil && acc.HasContent() {
		outputJSON, err := json.Marshal(acc.BuildOutput())
		if err == nil {
			_ = json.Unmarshal(outputJSON, &output)
		}
	}
	output = append(output, imageOutputs...)
	if len(output) == 0 {
		return nil, false
	}

	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, false
	}
	return outputJSON, true
}

func extractImageGenerationOutputFromSSEData(data []byte, seen map[string]struct{}) (json.RawMessage, bool) {
	if len(data) == 0 || !gjson.ValidBytes(data) {
		return nil, false
	}
	if gjson.GetBytes(data, "type").String() != "response.output_item.done" {
		return nil, false
	}
	item := gjson.GetBytes(data, "item")
	if !item.Exists() || !item.IsObject() || item.Get("type").String() != "image_generation_call" {
		return nil, false
	}
	if strings.TrimSpace(item.Get("result").String()) == "" {
		return nil, false
	}
	key := strings.TrimSpace(item.Get("id").String())
	if key == "" {
		key = strings.TrimSpace(item.Get("output_format").String()) + "|" + strings.TrimSpace(item.Get("result").String())
	}
	if key != "" && seen != nil {
		if _, exists := seen[key]; exists {
			return nil, false
		}
		seen[key] = struct{}{}
	}
	return json.RawMessage(item.Raw), true
}

func (s *OpenAIGatewayService) parseSSEUsageFromBody(body string) *OpenAIUsage {
	usage := &OpenAIUsage{}
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		s.parseSSEUsageBytes(data, usage)
	})
	return usage
}

func (s *OpenAIGatewayService) replaceModelInSSEBody(body, fromModel, toModel string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if _, ok := extractOpenAISSEDataLine(line); !ok {
			continue
		}
		lines[i] = s.replaceModelInSSELine(line, fromModel, toModel)
	}
	return strings.Join(lines, "\n")
}

func (s *OpenAIGatewayService) validateUpstreamBaseURL(raw string) (string, error) {
	if s.cfg != nil && !s.cfg.Security.URLAllowlist.Enabled {
		normalized, err := urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid base_url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.UpstreamHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

// buildOpenAIResponsesURL 组装 OpenAI Responses 端点。
// - base 以 /v1 结尾：追加 /responses
// - base 以其他版本段结尾（如 /v4）：追加 /responses
// - base 已是 /responses：原样返回
// - 其他情况：追加 /v1/responses
func buildOpenAIResponsesURL(base string) string {
	return buildOpenAIEndpointURL(base, "/v1/responses")
}

func trimOpenAIEncryptedReasoningItems(reqBody map[string]any) bool {
	if len(reqBody) == 0 {
		return false
	}

	inputValue, has := reqBody["input"]
	if !has {
		return false
	}

	switch input := inputValue.(type) {
	case []any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			nextItem, itemChanged, keep := sanitizeEncryptedReasoningInputItem(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				continue
			}
			filtered = append(filtered, nextItem)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return true
		}
		reqBody["input"] = filtered
		return true
	case []map[string]any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			nextItem, itemChanged, keep := sanitizeEncryptedReasoningInputItem(item)
			if itemChanged {
				changed = true
			}
			if !keep {
				continue
			}
			nextMap, ok := nextItem.(map[string]any)
			if !ok {
				filtered = append(filtered, item)
				continue
			}
			filtered = append(filtered, nextMap)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
			return true
		}
		reqBody["input"] = filtered
		return true
	case map[string]any:
		nextItem, changed, keep := sanitizeEncryptedReasoningInputItem(input)
		if !changed {
			return false
		}
		if !keep {
			delete(reqBody, "input")
			return true
		}
		nextMap, ok := nextItem.(map[string]any)
		if !ok {
			return false
		}
		reqBody["input"] = nextMap
		return true
	default:
		return false
	}
}

func sanitizeEncryptedReasoningInputItem(item any) (next any, changed bool, keep bool) {
	inputItem, ok := item.(map[string]any)
	if !ok {
		return item, false, true
	}

	itemType, _ := inputItem["type"].(string)
	switch strings.TrimSpace(itemType) {
	case "compaction", "compaction_summary":
		// Encrypted compaction summaries are tied to the account that produced
		// them. On cross-account recovery they must be removed as a whole.
		if _, encrypted := inputItem["encrypted_content"]; encrypted {
			return nil, true, false
		}
		return item, false, true
	case "reasoning":
	default:
		return item, false, true
	}

	_, hasEncryptedContent := inputItem["encrypted_content"]
	if !hasEncryptedContent {
		return item, false, true
	}

	delete(inputItem, "encrypted_content")
	if len(inputItem) == 1 {
		return nil, true, false
	}
	return inputItem, true, true
}

// SanitizeOpenAICrossModeFailoverReasoning derives a non-passthrough attempt
// body from the immutable canonical request. Provider-specific encrypted
// reasoning items must be removed as a whole because their id and summary are
// coupled to the passthrough provider that created encrypted_content.
func SanitizeOpenAICrossModeFailoverReasoning(body []byte) (sanitized []byte, changed bool, err error) {
	if len(body) == 0 || !gjson.GetBytes(body, "input").Exists() {
		return body, false, nil
	}
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return body, false, fmt.Errorf("decode cross-mode failover body: %w", err)
	}
	if !dropOpenAIEncryptedReasoningInputItems(decoded) {
		return body, false, nil
	}
	out, marshalErr := marshalOpenAIUpstreamJSON(decoded)
	if marshalErr != nil {
		return body, false, fmt.Errorf("serialize cross-mode failover body: %w", marshalErr)
	}
	return out, true, nil
}

func dropOpenAIEncryptedReasoningInputItems(reqBody map[string]any) bool {
	if len(reqBody) == 0 {
		return false
	}
	inputValue, has := reqBody["input"]
	if !has {
		return false
	}
	switch input := inputValue.(type) {
	case []any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			if isOpenAIEncryptedReasoningInputItem(item) {
				changed = true
				continue
			}
			filtered = append(filtered, item)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
		} else {
			reqBody["input"] = filtered
		}
		return true
	case []map[string]any:
		filtered := input[:0]
		changed := false
		for _, item := range input {
			if isOpenAIEncryptedReasoningInputItem(item) {
				changed = true
				continue
			}
			filtered = append(filtered, item)
		}
		if !changed {
			return false
		}
		if len(filtered) == 0 {
			delete(reqBody, "input")
		} else {
			reqBody["input"] = filtered
		}
		return true
	case map[string]any:
		if !isOpenAIEncryptedReasoningInputItem(input) {
			return false
		}
		delete(reqBody, "input")
		return true
	default:
		return false
	}
}

func isOpenAIEncryptedReasoningInputItem(item any) bool {
	inputItem, ok := item.(map[string]any)
	if !ok {
		return false
	}
	itemType, _ := inputItem["type"].(string)
	if strings.TrimSpace(itemType) != "reasoning" {
		return false
	}
	_, hasEncryptedContent := inputItem["encrypted_content"]
	return hasEncryptedContent
}

func IsOpenAIResponsesCompactPathForTest(c *gin.Context) bool {
	return isOpenAIResponsesCompactPath(c)
}

func OpenAICompactSessionSeedKeyForTest() string {
	return openAICompactSessionSeedKey
}

func NormalizeOpenAICompactRequestBodyForTest(body []byte) ([]byte, bool, error) {
	return normalizeOpenAICompactRequestBody(body)
}

func isOpenAIResponsesCompactPath(c *gin.Context) bool {
	suffix := strings.TrimSpace(openAIResponsesRequestPathSuffix(c))
	return suffix == "/compact" || strings.HasPrefix(suffix, "/compact/")
}

func normalizeOpenAICompactRequestBody(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}

	normalized := []byte(`{}`)
	// Keep the current Codex /compact schema while still dropping request-scoped
	// fields such as prompt_cache_key, store, and stream.
	for _, field := range []string{
		"model",
		"input",
		"instructions",
		"tools",
		"parallel_tool_calls",
		"reasoning",
		"service_tier",
		"text",
		"previous_response_id",
	} {
		value := gjson.GetBytes(body, field)
		if !value.Exists() {
			continue
		}
		next, err := sjson.SetRawBytes(normalized, field, []byte(value.Raw))
		if err != nil {
			return body, false, fmt.Errorf("normalize compact body %s: %w", field, err)
		}
		normalized = next
	}

	if bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(normalized)) {
		return body, false, nil
	}
	return normalized, true, nil
}

func resolveOpenAICompactSessionID(c *gin.Context) string {
	if c != nil {
		if sessionID := strings.TrimSpace(c.GetHeader("session_id")); sessionID != "" {
			return sessionID
		}
		if conversationID := strings.TrimSpace(c.GetHeader("conversation_id")); conversationID != "" {
			return conversationID
		}
		if seed, ok := c.Get(openAICompactSessionSeedKey); ok {
			if seedStr, ok := seed.(string); ok && strings.TrimSpace(seedStr) != "" {
				return strings.TrimSpace(seedStr)
			}
		}
	}
	return uuid.NewString()
}

func openAIResponsesRequestPathSuffix(c *gin.Context) string {
	suffix, ok := sanitizedUpstreamPathSuffix(rawOpenAIResponsesRequestPathSuffix(c))
	if !ok {
		return ""
	}
	return suffix
}

// IsForwardableOpenAIResponsesRequestPath 在鉴权后、调度前拒绝不安全子路径。
func IsForwardableOpenAIResponsesRequestPath(c *gin.Context) bool {
	_, ok := sanitizedUpstreamPathSuffix(rawOpenAIResponsesRequestPathSuffix(c))
	return ok
}

// IsOpenAIResponsesInputTokensRequestPath 判断当前请求是否为原生 Responses
// Token 预检端点。它必须在普通 Responses 生成处理器之前被分流。
func IsOpenAIResponsesInputTokensRequestPath(c *gin.Context) bool {
	return openAIResponsesRequestPathSuffix(c) == "/input_tokens"
}

func rawOpenAIResponsesRequestPathSuffix(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	if normalizedPath == "" {
		return ""
	}
	idx := strings.LastIndex(normalizedPath, "/responses")
	if idx < 0 {
		return ""
	}
	suffix := normalizedPath[idx+len("/responses"):]
	if suffix == "" || suffix == "/" {
		return ""
	}
	if !strings.HasPrefix(suffix, "/") {
		return ""
	}
	return suffix
}

func appendOpenAIResponsesRequestPathSuffix(baseURL, suffix string) string {
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	trimmedSuffix, ok := sanitizedUpstreamPathSuffix(suffix)
	if !ok || trimmedBase == "" || trimmedSuffix == "" {
		return trimmedBase
	}
	return trimmedBase + trimmedSuffix
}

func (s *OpenAIGatewayService) replaceModelInResponseBody(body []byte, fromModel, toModel string) []byte {
	// 使用 gjson/sjson 精确替换 model 字段，避免全量 JSON 反序列化
	if m := gjson.GetBytes(body, "model"); m.Exists() && m.Str == fromModel {
		newBody, err := sjson.SetBytes(body, "model", toModel)
		if err != nil {
			return body
		}
		return newBody
	}
	return body
}

// OpenAIRecordUsageInput input for recording usage
type OpenAIRecordUsageInput struct {
	Result             *OpenAIForwardResult
	APIKey             *APIKey
	User               *User
	Account            *Account
	Subscription       *UserSubscription
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string // 请求的 User-Agent
	IPAddress          string // 请求的客户端 IP 地址
	SessionID          string // 客户端显式会话标识，只用于用量审计，不参与调度或幂等
	RequestPayloadHash string
	APIKeyService      APIKeyQuotaUpdater
	QuotaPlatform      string // 异步后扣可显式携带请求时已解析的平台；为空时按实际账号平台兜底。
	// CostOverride and multiplier overrides are used by durable asynchronous
	// media tasks. Their price is resolved when the upstream accepts the task,
	// so later administrator pricing changes cannot alter an in-flight charge.
	CostOverride                *CostBreakdown
	RateMultiplierOverride      *float64
	ImageRateMultiplierOverride *float64
	VideoRateMultiplierOverride *float64
	// CyberBlocked 为 true 时把该用量行标记为 cyber（request_type=cyber），计费逻辑不变。
	CyberBlocked bool
	ChannelUsageFields
	PricingAt time.Time // [v0.2.5 新增字段]
}

// CyberPolicyUsageInput 是 cyber 拒绝、未走正常 RecordUsage 的请求记录用量的入参。
// 用量按上游真实 token 计费，与 WS cyber 及正常请求口径一致（InputTokens/OutputTokens
// 取自上游 response.failed 报告的 usage，即 mark.UpstreamInTok/OutTok）。
type CyberPolicyUsageInput struct {
	APIKey       *APIKey
	Account      *Account
	Subscription *UserSubscription
	RequestID    string
	Model        string
	// UpstreamModel is the final model sent before the upstream cyber rejection.
	// It is separate from Model because account/group mappings may have changed it.
	UpstreamModel string
	Stream        bool
	InputTokens   int
	OutputTokens  int
	// 渠道归因与请求级 meta，使 cyber 计费行与正常 RecordUsage 行口径一致
	// （否则 cyber 行 channel_id 等为空，渠道维度统计会遗漏 cyber 命中）。
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	SessionID          string
	RequestPayloadHash string
	APIKeyService      APIKeyQuotaUpdater
	ChannelUsageFields
}

// RecordCyberPolicyUsageLog 为被上游 cyber_policy 拒绝、未走正常 RecordUsage 的请求
// （HTTP forward 返回错误路径）记录用量并按上游真实 token 计费，使其与 WS cyber 路径、
// 与正常请求的计费口径统一（不再是 tokens=0 免费行）。token 取自上游 response.failed
// 报告的 usage（非流式直接拒通常为 0，cost 随之为 0）。复用 RecordUsage 完成成本计算、
// 扣费与用量行写入（request_type=cyber 由 CyberBlocked 置位）。仅 forward 返回错误的
// 路径由 handler 调用，避免与成功路径的正常 RecordUsage 重复。
func (s *OpenAIGatewayService) RecordCyberPolicyUsageLog(ctx context.Context, in CyberPolicyUsageInput) {
	if s == nil || in.APIKey == nil || in.APIKey.User == nil || in.Account == nil || strings.TrimSpace(in.Model) == "" {
		return
	}
	result := &OpenAIForwardResult{
		RequestID:     in.RequestID,
		Model:         in.Model,
		UpstreamModel: strings.TrimSpace(in.UpstreamModel),
		Stream:        in.Stream,
		Usage: OpenAIUsage{
			InputTokens:  in.InputTokens,
			OutputTokens: in.OutputTokens,
		},
	}
	if err := s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:             result,
		APIKey:             in.APIKey,
		User:               in.APIKey.User,
		Account:            in.Account,
		Subscription:       in.Subscription,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		UserAgent:          in.UserAgent,
		IPAddress:          in.IPAddress,
		SessionID:          in.SessionID,
		RequestPayloadHash: in.RequestPayloadHash,
		APIKeyService:      in.APIKeyService,
		ChannelUsageFields: in.ChannelUsageFields,
		CyberBlocked:       true,
	}); err != nil {
		logger.LegacyPrintf("service.openai_gateway", "cyber usage record failed: request_id=%s err=%v", in.RequestID, err)
	}
}

func isLocalOpenAIWSTurnBillingRequestID(ctx context.Context, requestID string) bool {
	requestID = strings.TrimSpace(requestID)
	connectionRequestID, ok := gatewaylite.CanonicalRequestIDFromContext(ctx)
	if !ok {
		// Without a canonical context identity, resolveUsageBillingRequestID
		// already preserves a non-empty result.RequestID; no override is needed.
		return false
	}
	prefix := strings.TrimSpace(connectionRequestID) + ":ws-turn:"
	turnRaw, ok := strings.CutPrefix(requestID, prefix)
	if !ok || turnRaw == "" {
		return false
	}
	turn, err := strconv.Atoi(turnRaw)
	return err == nil && turn > 0 && strconv.Itoa(turn) == turnRaw
}

// RecordUsage records usage and deducts balance
func (s *OpenAIGatewayService) RecordUsage(ctx context.Context, input *OpenAIRecordUsageInput) error {
	if input == nil {
		return errors.New("openai usage input is nil")
	}
	result := input.Result
	if result == nil {
		return errors.New("openai usage result is nil")
	}
	if s.rateLimitService != nil && input.Account != nil && input.Account.Platform == PlatformOpenAI {
		s.rateLimitService.ResetOpenAI403Counter(ctx, input.Account.ID)
	}

	apiKey := input.APIKey
	user := input.User
	account := input.Account
	subscription := input.Subscription
	if result.VideoCount <= 0 {
		ApplyOpenAIImageBillingResolution(result)
	}

	// input_tokens 同时包含普通输入、缓存读取和缓存写入。三类必须拆开计费，
	// 否则 GPT-5.6 的 cache_write_tokens 会既按普通输入价又按缓存写入价重复扣费。
	cacheAttributedTokens := result.Usage.CacheReadInputTokens + result.Usage.CacheCreationInputTokens
	actualInputTokens := result.Usage.InputTokens - cacheAttributedTokens
	if actualInputTokens < 0 {
		// 上游 usage（用量）字段自相矛盾时不能制造负 Token，也不能静默吞掉。
		// 结构化 warn（警告）会进入管理员系统日志，便于按模型和请求定位。
		logger.L().With(
			zap.String("component", "service.openai_gateway"),
			zap.String("model", result.Model),
			zap.String("request_id", result.RequestID),
			zap.Int("input_tokens", result.Usage.InputTokens),
			zap.Int("cache_read_tokens", result.Usage.CacheReadInputTokens),
			zap.Int("cache_write_tokens", result.Usage.CacheCreationInputTokens),
			zap.Int("cache_attributed_tokens", cacheAttributedTokens),
		).Warn("openai_usage.cache_tokens_exceed_input_tokens")
		actualInputTokens = 0
	}

	// Calculate cost
	tokens := UsageTokens{
		InputTokens:         actualInputTokens,
		ImageInputTokens:    result.Usage.ImageInputTokens,
		OutputTokens:        result.Usage.OutputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		ImageOutputTokens:   result.Usage.ImageOutputTokens,
	}

	// Get rate multiplier
	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		// 请求入口已经冻结“分组默认 → 用户覆盖 → 高峰叠加”。
		// 流式结束、重试和延迟结算都只能使用这份快照，
		// 否则会因结束时钟或管理员中途改配置而改变费用。
		multiplier = policy.EffectiveRateMultiplier
	} else if meta, funded := gatewaylite.RequestMetaFromContext(ctx); funded && meta.Funding != nil {
		multiplier = meta.RateMultiplier
	} else if apiKey.GroupID != nil && apiKey.Group != nil {
		userRate := apiKey.UserRateMultiplier
		resolvedRate := apiKey.Group.RateMultiplier
		if shouldResolveLocalUserGroupRate(ctx, s.cfg) {
			resolver := s.userGroupRateResolver
			if resolver == nil {
				resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
			}
			var resolveErr error
			resolvedRate, resolveErr = resolver.Resolve(ctx, user.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
			if resolveErr != nil {
				return fmt.Errorf("resolve user group rate for openai usage: %w", resolveErr)
			}
			if resolvedRate != apiKey.Group.RateMultiplier {
				userRate = &resolvedRate
			} else {
				userRate = nil
			}
		} else if userRate != nil {
			resolvedRate = *userRate
		}
		// 仅为没有经过新版鉴权中间件的内部/历史调用保留回退。
		// 正常客户请求必须命中上面的冻结快照分支。
		multiplier = resolvedRate
		if policy, err := ResolveEffectiveRate(apiKey.Group, userRate, time.Now()); err == nil {
			multiplier = policy.EffectiveRateMultiplier
		}
	}
	if input.RateMultiplierOverride != nil {
		multiplier = *input.RateMultiplierOverride
	}
	if (result.ImageCount > 0 || result.VideoCount > 0) && input.CostOverride == nil {
		apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	}
	imageMultiplier := resolveImageRateMultiplier(apiKey, multiplier)
	videoMultiplier := resolveVideoRateMultiplier(apiKey, multiplier)
	if input.ImageRateMultiplierOverride != nil {
		imageMultiplier = *input.ImageRateMultiplierOverride
	}
	if input.VideoRateMultiplierOverride != nil {
		videoMultiplier = *input.VideoRateMultiplierOverride
	}

	var cost *CostBreakdown
	var err error
	billingModel := forwardResultBillingModel(result.Model, result.UpstreamModel)
	if result.BillingModel != "" {
		billingModel = strings.TrimSpace(result.BillingModel)
	}
	if input.BillingModelSource == BillingModelSourceChannelMapped && input.ChannelMappedModel != "" && input.ChannelMappedModel != input.OriginalModel {
		billingModel = input.ChannelMappedModel
	}
	if input.BillingModelSource == BillingModelSourceRequested && input.OriginalModel != "" {
		billingModel = input.OriginalModel
	}
	concreteBillingModel := forwardResultBillingModel(result.Model, result.UpstreamModel)
	if !gatewaylite.HasRequestFunding(ctx) && apiKey.Group != nil && apiKey.Group.Platform == PlatformComposite && billingModel != concreteBillingModel &&
		s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey) == nil {
		billingModel = concreteBillingModel
	}
	billingModels := usageBillingModelCandidates(
		billingModel,
		result.BillingModel,
		input.ChannelMappedModel,
		input.OriginalModel,
		result.UpstreamModel,
		result.Model,
	)
	executionPricingFrozen := false
	for _, candidate := range billingModels {
		if snapshot, frozen := ExecutionPricingSnapshotFromContext(ctx, candidate); frozen {
			// 已派发/持久任务的最终计费候选优先于混合组回退和后来新增的客户价格。
			// 只保留该候选，缺损原价必须失败；没有快照的旧调用仍沿原候选顺序。
			billingModels = []string{candidate}
			multiplier = snapshot.RateMultiplier
			executionPricingFrozen = true
			break
		}
	}
	serviceTier := ""
	if result.ServiceTier != nil {
		serviceTier = strings.TrimSpace(*result.ServiceTier)
	}
	longContextBillingGate := openAILongContextBillingGate(apiKey, account)
	if input.CostOverride != nil {
		copyCost := *input.CostOverride
		cost = &copyCost
	} else {
		cost, err = s.calculateOpenAIRecordUsageCost(ctx, result, apiKey, billingModels, multiplier, imageMultiplier, videoMultiplier, tokens, serviceTier, longContextBillingGate)
	}
	if err != nil {
		if gatewaylite.HasRequestFunding(ctx) || executionPricingFrozen {
			// 原执行价缺失/损坏意味着结算结果未知，不能伪造零价终态释放卡或钱包占用。
			return err
		}
		if !isUsagePricingUnavailableError(err) {
			return err
		}
		logFields := []zap.Field{
			zap.String("component", "service.openai_gateway"),
			zap.Strings("billing_models", billingModels),
			zap.String("requested_model", input.OriginalModel),
			zap.String("mapped_model", input.ChannelMappedModel),
			zap.String("upstream_model", result.UpstreamModel),
			zap.Int64("api_key_id", apiKey.ID),
			zap.Int64("account_id", account.ID),
		}
		var pricingFieldErr *PricingFieldError
		if errors.As(err, &pricingFieldErr) {
			logFields = append(logFields,
				zap.String("model", pricingFieldErr.Model),
				zap.String("pricing_field", pricingFieldErr.Field),
				zap.String("pricing_service_tier", pricingFieldErr.ServiceTier),
				zap.Bool("pricing_long_context", pricingFieldErr.LongContext),
			)
		} else {
			// 没有结构化定价错误时（最常见：该模型在价格目录里完全没有条目，
			// 只带回 ErrModelPricingUnavailable 文案）也要标明缺失维度，
			// 否则运维端只能看到一条没有 pricing_field 的告警，无法区分缺的是哪一项。
			logFields = append(logFields, zap.String("pricing_field", "model_pricing"))
			if fallbackModel := firstUsageBillingModel(billingModels); fallbackModel != "" {
				logFields = append(logFields, zap.String("model", fallbackModel))
			}
		}
		logger.L().With(logFields...).Warn("openai_usage.pricing_missing_record_zero_cost", zap.Error(err))
		cost = &CostBreakdown{BillingMode: string(BillingModeToken)}
	}
	if input.CostOverride == nil && !gatewaylite.HasRequestFunding(ctx) && !executionPricingFrozen {
		baselineBillingModel := firstUsageBillingModel(billingModels)
		if responseModel := responseModelBillingDeclaration(
			input.BillingModelSource,
			result.UpstreamResponseModel,
			result.UpstreamResponseModelConflict,
			result.ImageCount > 0 || result.VideoCount > 0 || result.WebSearchCalls > 0 || result.AudioUsage != nil || result.SearchCount > 0,
		); responseModel != "" && !strings.EqualFold(responseModel, baselineBillingModel) {
			if identified, responseChannelPriced := s.hasIdentifiedOpenAIResponsePricing(ctx, responseModel, apiKey); identified {
				responseModels := usageBillingModelCandidates(responseModel)
				responseCost, responseErr := s.calculateOpenAIRecordUsageCost(
					ctx, result, apiKey, responseModels, multiplier, imageMultiplier, videoMultiplier, tokens, serviceTier, longContextBillingGate,
				)
				baselineChannelPriced := s.resolveOpenAIChannelPricing(ctx, baselineBillingModel, apiKey) != nil
				if responseErr == nil && responseModelBillingAdoptable(cost, responseCost, baselineChannelPriced, responseChannelPriced) {
					logResponseModelBillingApplied("service.openai_gateway", account, result.RequestID, baselineBillingModel, responseModel, cost, responseCost)
					billingModels = responseModels
					cost = responseCost
				}
			}
		}
	}

	// Determine billing type
	isSubscriptionBilling := subscription != nil && apiKey.Group != nil && apiKey.Group.IsSubscriptionType()
	billingType := BillingTypeBalance
	if isSubscriptionBilling {
		billingType = BillingTypeSubscription
	}

	// Create usage log
	durationMs := int(result.Duration.Milliseconds())
	accountRateMultiplier := account.BillingRateMultiplier()
	requestID := resolveUsageBillingRequestID(ctx, result.RequestID)
	if result.OpenAIWSMode && isLocalOpenAIWSTurnBillingRequestID(ctx, result.RequestID) {
		// Handler 已把连接级 canonical ID 加上 turn 序号，形成每轮唯一的
		// 本地财务编号。仅这个明确格式可以覆盖连接级 ID；普通 HTTP 和
		// 上游 response.id 仍沿用 canonical ID，避免破坏原有幂等边界。
		requestID = strings.TrimSpace(result.RequestID)
	}

	// 确定 RequestedModel（渠道映射前的原始模型）
	requestedModel := result.Model
	if input.OriginalModel != "" {
		requestedModel = input.OriginalModel
	}

	usageLog := &UsageLog{
		UserID:         user.ID,
		APIKeyID:       apiKey.ID,
		AccountID:      account.ID,
		RequestID:      requestID,
		Model:          result.Model,
		RequestedModel: requestedModel,
		// Keep the final upstream identifier even when it equals the public model;
		// this makes mapping and failover audits deterministic.
		UpstreamModel:            optionalTrimmedStringPtr(result.UpstreamModel),
		UpstreamResponseModel:    optionalTrimmedStringPtr(result.UpstreamResponseModel),
		UpstreamModelMismatch:    result.UpstreamModelMismatch,
		ServiceTier:              result.ServiceTier,
		SentServiceTier:          result.SentServiceTier,
		ResponseServiceTier:      result.ResponseServiceTier,
		RequestedServiceTier:     result.RequestedServiceTier,
		PriorityPolicyMode:       optionalTrimmedStringPtr(result.PriorityPolicyMode),
		PriorityDetectionSource:  optionalTrimmedStringPtr(result.PriorityDetectionSource),
		PriorityPolicyAction:     optionalTrimmedStringPtr(result.PriorityPolicyAction),
		PriorityPolicyRuleID:     optionalTrimmedStringPtr(result.PriorityPolicyRuleID),
		PriorityPolicyFallback:   result.PriorityPolicyFallback,
		RequestedReasoningEffort: result.RequestedReasoningEffort,
		ReasoningEffort:          result.ReasoningEffort,
		InboundEndpoint:          optionalTrimmedStringPtr(input.InboundEndpoint),
		UpstreamEndpoint:         optionalTrimmedStringPtr(input.UpstreamEndpoint),
		SessionID:                optionalTrimmedStringPtr(input.SessionID),
		InputTokens:              actualInputTokens,
		OutputTokens:             result.Usage.OutputTokens,
		CacheCreationTokens:      result.Usage.CacheCreationInputTokens,
		CacheReadTokens:          result.Usage.CacheReadInputTokens,
		ImageInputTokens:         result.Usage.ImageInputTokens,
		ImageOutputTokens:        result.Usage.ImageOutputTokens,
		ImageCount:               result.ImageCount,
		ImageSize:                optionalTrimmedStringPtr(result.ImageSize),
		ImageInputSize:           optionalTrimmedStringPtr(result.ImageInputSize),
		ImageOutputSize:          optionalTrimmedStringPtr(result.ImageOutputSize),
		ImageSizeSource:          optionalTrimmedStringPtr(result.ImageSizeSource),
		ImageSizeBreakdown:       result.ImageSizeBreakdown,
		VideoCount:               result.VideoCount,
		VideoResolution:          optionalTrimmedStringPtr(result.VideoResolution),
		VideoDurationSeconds:     optionalPositiveIntPtr(result.VideoDurationSeconds),
	}
	if cost != nil {
		usageLog.InputCost = cost.InputCost
		usageLog.ImageInputCost = cost.ImageInputCost
		usageLog.ImageInputPrice = cost.ImageInputPrice
		usageLog.ImageInputPriceSource = optionalTrimmedStringPtr(cost.ImageInputPriceSource)
		usageLog.OutputCost = cost.OutputCost
		usageLog.ImageOutputCost = cost.ImageOutputCost
		usageLog.CacheCreationCost = cost.CacheCreationCost
		usageLog.CacheReadCost = cost.CacheReadCost
		usageLog.TotalCost = cost.TotalCost
		usageLog.ActualCost = cost.ActualCost
		usageLog.LongContextBillingApplied = cost.LongContextBillingApplied
	}
	if result.VideoCount > 0 {
		usageLog.RateMultiplier = videoMultiplier
	} else if result.ImageCount > 0 && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = imageMultiplier
	} else {
		usageLog.RateMultiplier = multiplier
	}
	usageLog.AccountRateMultiplier = &accountRateMultiplier
	usageLog.BillingType = billingType
	usageLog.Stream = result.Stream
	if input.CyberBlocked {
		usageLog.RequestType = RequestTypeCyberBlocked
	}
	usageLog.OpenAIWSMode = result.OpenAIWSMode
	usageLog.DurationMs = &durationMs
	usageLog.FirstTokenMs = result.FirstTokenMs
	// 伪造首字时刻一并落日志（OpenAI 侧由 ops context 透传）。
	usageLog.FakeFirstTokenMs = result.FakeFirstTokenMs
	usageLog.CreatedAt = time.Now()
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		// 把真正用于 Token 扣费的冻结策略同时写入本地日志和
		// gateway-lite usage event，便于主站对账，不需要根据结束时刻反推。
		cloned := policy.Clone()
		usageLog.RatePolicyAudit = &cloned
	}
	// 设置渠道信息
	usageLog.ChannelID = optionalInt64Ptr(input.ChannelID)
	usageLog.ModelMappingChain = optionalTrimmedStringPtr(input.ModelMappingChain)
	usageLog.ChannelMappedModel = strings.TrimSpace(input.ChannelMappedModel)
	usageLog.TargetPlatform = account.Platform
	if source, ok := CompositeRouteSourceFromContext(ctx); ok {
		usageLog.CompositeRouteSource = source
	}
	// 设置计费模式
	if cost != nil && cost.BillingMode != "" {
		billingMode := cost.BillingMode
		usageLog.BillingMode = &billingMode
	} else if result.VideoCount > 0 {
		billingMode := string(BillingModeVideo)
		usageLog.BillingMode = &billingMode
	} else if result.ImageCount > 0 {
		billingMode := string(BillingModeImage)
		usageLog.BillingMode = &billingMode
	} else {
		billingMode := string(BillingModeToken)
		usageLog.BillingMode = &billingMode
	}
	// 添加 UserAgent
	if input.UserAgent != "" {
		usageLog.UserAgent = &input.UserAgent
	}

	// 添加 IPAddress
	if input.IPAddress != "" {
		usageLog.IPAddress = &input.IPAddress
	}

	if apiKey.GroupID != nil {
		usageLog.GroupID = apiKey.GroupID
	}
	if subscription != nil {
		usageLog.SubscriptionID = &subscription.ID
	}

	// 计算账号统计定价费用（使用最终上游模型匹配自定义规则）
	if apiKey.GroupID != nil {
		accountStatsMode := accountStatsCostModeDefault
		if result.AudioUsage != nil || result.WebSearchCalls > 0 || result.SearchCount > 0 {
			accountStatsMode = accountStatsCostModeNoProcurementEvidence
		}
		applyAccountStatsCost(ctx, usageLog, s.channelService, s.billingService,
			account.ID, *apiKey.GroupID, result.UpstreamModel, result.Model,
			tokens, cost.TotalCost, accountStatsMode,
		)
	}

	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway", s.cfg)
		logger.LegacyPrintf("service.openai_gateway", "[SIMPLE MODE] Usage recorded (not billed): user=%d, tokens=%d", usageLog.UserID, usageLog.TotalTokens())
		s.deferredService.ScheduleLastUsedUpdate(account.ID)
		return nil
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeGatewayLite && IsGatewayLiteLocalAPIKeyContext(ctx) {
		// Local gateway keys deliberately bypass user balance, subscription,
		// API-key quota and control-plane usage correction. The gateway still
		// records local observability and upstream-account quota consumption.
		writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway", s.cfg)
		if s.deferredService != nil {
			s.deferredService.ScheduleLastUsedUpdate(account.ID)
		}
		if account.IsAPIKeyOrBedrock() && account.HasAnyQuotaLimit() && s.accountRepo != nil {
			billingCtx, cancel := detachedBillingContext(ctx)
			accountCost := accountQuotaCostFromUsageLog(usageLog, cost.TotalCost, accountRateMultiplier)
			if accountCost > 0 {
				if err := s.accountRepo.IncrementQuotaUsed(billingCtx, account.ID, accountCost); err != nil {
					logger.LegacyPrintf("service.openai_gateway", "gateway-lite local key account quota update failed account=%d cost=%f: %v", account.ID, accountCost, err)
				}
			}
			cancel()
		}
		return nil
	}

	quotaPlatform := strings.TrimSpace(input.QuotaPlatform)
	if quotaPlatform == "" {
		quotaPlatform = PlatformFromAPIKey(apiKey)
		if quotaPlatform == PlatformComposite && account != nil {
			quotaPlatform = account.Platform
		}
	}
	billingErr := func() error {
		_, err := applyUsageBilling(ctx, requestID, usageLog, &postUsageBillingParams{
			Cost:                  cost,
			AccountStatsCost:      usageLog.AccountStatsCost,
			UpstreamModel:         usageLog.UpstreamModel,
			User:                  user,
			APIKey:                apiKey,
			Account:               account,
			Subscription:          subscription,
			RequestPayloadHash:    resolveUsageBillingPayloadFingerprint(ctx, input.RequestPayloadHash),
			IsSubscriptionBill:    isSubscriptionBilling,
			AccountRateMultiplier: accountRateMultiplier,
			APIKeyService:         input.APIKeyService,
			Platform:              quotaPlatform,
		}, s.billingDeps(), s.usageBillingRepo)
		return err
	}()

	if billingErr != nil {
		if reportGatewayLiteUsageCorrection(ctx, usageLog) {
			schedulePostUsageLastUsed(&postUsageBillingParams{Account: account}, s.billingDeps())
			writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway", s.cfg)
			return nil
		}
		recordUnsettledUsageLog(ctx, s.usageLogRepo, usageLog, "service.openai_gateway", billingErr, s.cfg)
		return billingErr
	}
	// 远程 Key 的主站计费事件是权威数据，必须先进入 Redis Stream；
	// 本地 usage log 只用于节点观测，不能反过来拖住或阻断关键计费入队。
	if !reportGatewayLiteUsageCorrection(ctx, usageLog) {
		if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok && meta.AsyncSettlement {
			// An asynchronous upstream job may already have consumed real money.
			// Keep its durable task in succeeded (retryable) state until the Redis
			// usage outbox accepts the final correction; never mark it billed early.
			correctionErr := errors.New("gateway-lite async usage correction was not durably enqueued")
			recordUnsettledUsageLog(ctx, s.usageLogRepo, usageLog, "service.openai_gateway", correctionErr, s.cfg)
			return correctionErr
		}
	}
	writeUsageLogBestEffort(ctx, s.usageLogRepo, usageLog, "service.openai_gateway", s.cfg)

	return nil
}

func (s *OpenAIGatewayService) calculateOpenAIRecordUsageCost(
	ctx context.Context,
	result *OpenAIForwardResult,
	apiKey *APIKey,
	billingModels []string,
	multiplier float64,
	imageMultiplier float64,
	videoMultiplier float64,
	tokens UsageTokens,
	serviceTier string,
	longContextBillingGate *bool,
) (*CostBreakdown, error) {
	billingModel := firstUsageBillingModel(billingModels)
	if result != nil && result.AudioUsage != nil {
		return s.billingService.CalculateAudioCost(result.AudioUsage.Mode, result.AudioUsage.DurationOrUnits, groupAudioPriceConfigFromAPIKey(apiKey), multiplier), nil
	}
	if result != nil && result.WebSearchCalls > 0 {
		var groupPrice *float64
		if apiKey != nil && apiKey.Group != nil {
			groupPrice = apiKey.Group.WebSearchPricePerCall
		}
		return s.billingService.CalculateWebSearchCost(result.WebSearchCalls, groupPrice, multiplier), nil
	}
	if isGrokVideoUsageResult(result, billingModels) {
		if billingModel == "" {
			return nil, errors.New("openai video usage billing model is empty")
		}
		if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved == nil || resolved.Mode != BillingModeToken {
			return s.calculateOpenAIVideoCost(ctx, billingModel, apiKey, result, videoMultiplier), nil
		}
	}
	if result != nil && result.ImageCount > 0 {
		// 渠道定价为 token 计费时走 token 路径，否则走图片计费
		if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved == nil || resolved.Mode != BillingModeToken {
			return s.calculateOpenAIImageCost(ctx, billingModel, apiKey, result, imageMultiplier), nil
		}
	}
	if len(billingModels) == 0 || billingModel == "" {
		return nil, errors.New("openai usage billing model is empty")
	}
	var tokenCost *CostBreakdown
	var lastErr error
	for _, candidate := range billingModels {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if snapshot, frozen := ExecutionPricingSnapshotFromContext(ctx, candidate); frozen {
			// The usage log and financial correction must expose the multiplier
			// used by the frozen calculation, rather than a live group value.
			multiplier = snapshot.RateMultiplier
		}
		cost, err := s.calculateOpenAIRecordUsageTokenCost(ctx, apiKey, candidate, multiplier, tokens, serviceTier, longContextBillingGate)
		if err == nil {
			tokenCost = cost
			break
		}
		lastErr = err
	}
	searchCost := (*CostBreakdown)(nil)
	if result != nil && result.SearchCount > 0 {
		searchCost = s.billingService.CalculateSearchCost(result.SearchCount, groupSearchPricePer1kFromAPIKey(apiKey), multiplier)
	}
	if tokenCost == nil {
		if len(billingModels) == 0 || billingModel == "" {
			if searchCost != nil {
				return searchCost, nil
			}
			return nil, errors.New("openai usage billing model is empty")
		}
		if lastErr == nil {
			lastErr = errors.New("no non-empty billing model candidates")
		}
		return nil, fmt.Errorf("calculate OpenAI usage cost failed for billing models %s: %w", strings.Join(billingModels, ","), lastErr)
	}
	if searchCost != nil {
		tokenCost.TotalCost += searchCost.TotalCost
		tokenCost.ActualCost += searchCost.ActualCost
	}
	return tokenCost, nil
}

func isGrokVideoBillingModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-imagine-video")
}

func isGrokVideoUsageResult(result *OpenAIForwardResult, billingModels []string) bool {
	if result == nil || result.VideoCount <= 0 {
		return false
	}
	candidates := append([]string{}, billingModels...)
	candidates = append(candidates, result.BillingModel, result.Model, result.UpstreamModel)
	for _, candidate := range candidates {
		if isGrokVideoBillingModel(candidate) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) calculateOpenAIVideoCost(
	ctx context.Context,
	billingModel string,
	apiKey *APIKey,
	result *OpenAIForwardResult,
	multiplier float64,
) *CostBreakdown {
	videoCount := result.VideoCount
	if videoCount <= 0 {
		videoCount = 1
	}
	resolution := NormalizeVideoBillingResolutionOrDefault(result.VideoResolution)
	durationSeconds := NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds)
	groupConfig := videoPriceConfigFromAPIKey(apiKey)
	if apiKeyHasConfiguredVideoPrice(apiKey, billingModel, resolution) {
		return s.billingService.CalculateVideoCost(billingModel, resolution, videoCount, durationSeconds, groupConfig, multiplier)
	}
	if refreshed := s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey); refreshed != apiKey {
		apiKey = refreshed
		groupConfig = videoPriceConfigFromAPIKey(apiKey)
		if apiKeyHasConfiguredVideoPrice(apiKey, billingModel, resolution) {
			return s.billingService.CalculateVideoCost(billingModel, resolution, videoCount, durationSeconds, groupConfig, multiplier)
		}
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil &&
		(resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage || resolved.Mode == BillingModeVideo) {
		requestCount := videoCount
		if resolved.Mode == BillingModeVideo {
			requestCount *= durationSeconds
		}
		gid := apiKey.Group.ID
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          billingModel,
			GroupID:        &gid,
			RequestCount:   requestCount,
			SizeTier:       resolution,
			RateMultiplier: multiplier,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
		if err == nil {
			cost.BillingMode = string(BillingModeVideo)
			return cost
		}
		logger.LegacyPrintf("service.openai_gateway", "Calculate video channel cost failed: %v", err)
	}
	return s.billingService.CalculateVideoCost(
		billingModel,
		resolution,
		videoCount,
		durationSeconds,
		groupConfig,
		multiplier,
	)
}

func isUsagePricingUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrModelPricingUnavailable) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no pricing available") || strings.Contains(msg, "pricing not found")
}

func (s *OpenAIGatewayService) calculateOpenAIRecordUsageTokenCost(
	ctx context.Context,
	apiKey *APIKey,
	billingModel string,
	multiplier float64,
	tokens UsageTokens,
	serviceTier string,
	longContextBillingGate *bool,
) (*CostBreakdown, error) {
	if snapshot, frozen := ExecutionPricingSnapshotFromContext(ctx, billingModel); frozen {
		input, err := snapshot.ApplyToCostInput(CostInput{Ctx: ctx, Model: billingModel, Tokens: tokens, RequestCount: 1})
		if err != nil {
			return nil, errors.Join(gatewaylite.ErrRequestQuotaUnavailable, err)
		}
		return s.billingService.CalculateCostUnified(input)
	}
	if gatewaylite.HasRequestFunding(ctx) {
		// A funded request cannot invent a new price after execution. Missing
		// frozen material keeps its hold pending instead of charging a new tariff.
		return nil, errors.Join(gatewaylite.ErrRequestQuotaUnavailable, errors.New("execution pricing snapshot is missing"))
	}
	if s.resolver != nil && apiKey.Group != nil {
		gid := apiKey.Group.ID
		groupLongContextEnabled := apiKey.Group.AllowOpenAILongContextBilling
		return s.billingService.CalculateCostUnified(CostInput{
			Ctx:                       ctx,
			Model:                     billingModel,
			GroupID:                   &gid,
			Tokens:                    tokens,
			RequestCount:              1,
			RateMultiplier:            multiplier,
			ServiceTier:               serviceTier,
			Resolver:                  s.resolver,
			LongContextBillingEnabled: longContextBillingGate,
			LongContextGroupEnabled:   &groupLongContextEnabled,
		})
	}
	return s.billingService.calculateCostWithServiceTierPolicy(
		billingModel,
		tokens,
		multiplier,
		serviceTier,
		longContextBillingGate == nil || *longContextBillingGate,
	)
}

func (s *OpenAIGatewayService) calculateOpenAIImageCost(
	ctx context.Context,
	billingModel string,
	apiKey *APIKey,
	result *OpenAIForwardResult,
	multiplier float64,
) *CostBreakdown {
	sizeTier := NormalizeImageBillingTierOrDefault(result.ImageSize)
	groupConfig := imagePriceConfigFromAPIKey(apiKey)
	if apiKeyHasConfiguredImagePrice(apiKey, sizeTier) {
		return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
	}
	if refreshed := s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey); refreshed != apiKey {
		apiKey = refreshed
		groupConfig = imagePriceConfigFromAPIKey(apiKey)
		if apiKeyHasConfiguredImagePrice(apiKey, sizeTier) {
			return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
		}
	}
	if resolved := s.resolveOpenAIChannelPricing(ctx, billingModel, apiKey); resolved != nil &&
		(resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage) {
		gid := apiKey.Group.ID
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          billingModel,
			GroupID:        &gid,
			RequestCount:   result.ImageCount,
			SizeTier:       sizeTier,
			RateMultiplier: multiplier,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
		if err == nil {
			return cost
		}
		logger.LegacyPrintf("service.openai_gateway", "Calculate image channel cost failed: %v", err)
	}

	return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
}

func imagePriceConfigFromAPIKey(apiKey *APIKey) *ImagePriceConfig {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return &ImagePriceConfig{
		Price1K: apiKey.Group.ImagePrice1K,
		Price2K: apiKey.Group.ImagePrice2K,
		Price4K: apiKey.Group.ImagePrice4K,
	}
}

func videoPriceConfigFromAPIKey(apiKey *APIKey) *VideoPriceConfig {
	if apiKey == nil || apiKey.Group == nil {
		return nil
	}
	return &VideoPriceConfig{
		Price480P:   apiKey.Group.VideoPrice480P,
		Price720P:   apiKey.Group.VideoPrice720P,
		Price1080P:  apiKey.Group.VideoPrice1080P,
		ModelPrices: apiKey.Group.VideoModelPrices,
	}
}

func apiKeyHasConfiguredImagePrice(apiKey *APIKey, sizeTier string) bool {
	return apiKey != nil && apiKey.Group != nil && apiKey.Group.GetImagePrice(sizeTier) != nil
}

func apiKeyHasConfiguredVideoPrice(apiKey *APIKey, model, resolution string) bool {
	return apiKey != nil && apiKey.Group != nil && apiKey.Group.GetVideoPriceForModel(model, resolution) != nil
}

// apiKeyWithFreshGroupMediaPricing 只在旧缓存明显缺少媒体价格字段时回源一次。
// 正常快照包含默认倍率 1，因此不会让媒体请求持续访问数据库。
func (s *OpenAIGatewayService) apiKeyWithFreshGroupMediaPricing(ctx context.Context, apiKey *APIKey) *APIKey {
	if apiKey == nil || apiKey.GroupID == nil || *apiKey.GroupID <= 0 || !groupMediaPricingLooksIncomplete(apiKey.Group) {
		return apiKey
	}
	if s == nil || s.channelService == nil || s.channelService.groupRepo == nil {
		return apiKey
	}
	group, err := s.channelService.groupRepo.GetByIDLite(ctx, *apiKey.GroupID)
	if err != nil || group == nil {
		return apiKey
	}
	clone := *apiKey
	clone.Group = group
	return &clone
}

func groupMediaPricingLooksIncomplete(group *Group) bool {
	if group == nil {
		return true
	}
	if group.ImageRateIndependent || group.VideoRateIndependent {
		return false
	}
	if group.ImageRateMultiplier != 0 || group.VideoRateMultiplier != 0 {
		return false
	}
	return group.ImagePrice1K == nil && group.ImagePrice2K == nil && group.ImagePrice4K == nil &&
		group.VideoPrice480P == nil && group.VideoPrice720P == nil && group.VideoPrice1080P == nil
}

func (s *OpenAIGatewayService) resolveOpenAIChannelPricing(ctx context.Context, billingModel string, apiKey *APIKey) *ResolvedPricing {
	if s.resolver == nil || apiKey == nil || apiKey.Group == nil {
		return nil
	}
	gid := apiKey.Group.ID
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: &gid})
	if resolved.Source == PricingSourceChannel {
		return resolved
	}
	return nil
}

// ParseCodexRateLimitHeaders extracts Codex usage limits from response headers.
// Exported for use in ratelimit_service when handling OpenAI 429 responses.
func ParseCodexRateLimitHeaders(headers http.Header) *OpenAICodexUsageSnapshot {
	snapshot := &OpenAICodexUsageSnapshot{}
	hasData := false

	// Helper to parse float64 from header
	parseFloat := func(key string) *float64 {
		if v := headers.Get(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return &f
			}
		}
		return nil
	}

	// Helper to parse int from header
	parseInt := func(key string) *int {
		if v := headers.Get(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return &i
			}
		}
		return nil
	}

	// Primary (weekly) limits
	if v := parseFloat("x-codex-primary-used-percent"); v != nil {
		snapshot.PrimaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-reset-after-seconds"); v != nil {
		snapshot.PrimaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-window-minutes"); v != nil {
		snapshot.PrimaryWindowMinutes = v
		hasData = true
	}

	// Secondary (5h) limits
	if v := parseFloat("x-codex-secondary-used-percent"); v != nil {
		snapshot.SecondaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-reset-after-seconds"); v != nil {
		snapshot.SecondaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-window-minutes"); v != nil {
		snapshot.SecondaryWindowMinutes = v
		hasData = true
	}

	// Overflow ratio
	if v := parseFloat("x-codex-primary-over-secondary-limit-percent"); v != nil {
		snapshot.PrimaryOverSecondaryPercent = v
		hasData = true
	}

	if !hasData {
		return nil
	}

	snapshot.UpdatedAt = time.Now().Format(time.RFC3339)
	return snapshot
}

func codexSnapshotBaseTime(snapshot *OpenAICodexUsageSnapshot, fallback time.Time) time.Time {
	if snapshot == nil {
		return fallback
	}
	if snapshot.UpdatedAt == "" {
		return fallback
	}
	base, err := time.Parse(time.RFC3339, snapshot.UpdatedAt)
	if err != nil {
		return fallback
	}
	return base
}

func codexResetAtRFC3339(base time.Time, resetAfterSeconds *int) *string {
	if resetAfterSeconds == nil {
		return nil
	}
	sec := *resetAfterSeconds
	if sec < 0 {
		sec = 0
	}
	resetAt := base.Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
	return &resetAt
}

func buildCodexUsageExtraUpdates(snapshot *OpenAICodexUsageSnapshot, fallbackNow time.Time) map[string]any {
	if snapshot == nil {
		return nil
	}

	baseTime := codexSnapshotBaseTime(snapshot, fallbackNow)
	updates := make(map[string]any)

	// 保存原始 primary/secondary 字段，便于排查问题
	if snapshot.PrimaryUsedPercent != nil {
		updates["codex_primary_used_percent"] = *snapshot.PrimaryUsedPercent
	}
	if snapshot.PrimaryResetAfterSeconds != nil {
		updates["codex_primary_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
	}
	if snapshot.PrimaryWindowMinutes != nil {
		updates["codex_primary_window_minutes"] = *snapshot.PrimaryWindowMinutes
	}
	if snapshot.SecondaryUsedPercent != nil {
		updates["codex_secondary_used_percent"] = *snapshot.SecondaryUsedPercent
	}
	if snapshot.SecondaryResetAfterSeconds != nil {
		updates["codex_secondary_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
	}
	if snapshot.SecondaryWindowMinutes != nil {
		updates["codex_secondary_window_minutes"] = *snapshot.SecondaryWindowMinutes
	}
	if snapshot.PrimaryOverSecondaryPercent != nil {
		updates["codex_primary_over_secondary_percent"] = *snapshot.PrimaryOverSecondaryPercent
	}
	updates["codex_usage_updated_at"] = baseTime.Format(time.RFC3339)

	// 归一化到 5h/7d 规范字段
	if normalized := snapshot.Normalize(); normalized != nil {
		if normalized.Used5hPercent != nil {
			updates["codex_5h_used_percent"] = *normalized.Used5hPercent
			updates["codex_5h_usage_updated_at"] = baseTime.Format(time.RFC3339)
		}
		if normalized.Reset5hSeconds != nil {
			updates["codex_5h_reset_after_seconds"] = *normalized.Reset5hSeconds
		}
		if normalized.Window5hMinutes != nil {
			updates["codex_5h_window_minutes"] = *normalized.Window5hMinutes
		}
		if normalized.Used7dPercent != nil {
			updates["codex_7d_used_percent"] = *normalized.Used7dPercent
			updates["codex_7d_usage_updated_at"] = baseTime.Format(time.RFC3339)
		}
		if normalized.Reset7dSeconds != nil {
			updates["codex_7d_reset_after_seconds"] = *normalized.Reset7dSeconds
		}
		if normalized.Window7dMinutes != nil {
			updates["codex_7d_window_minutes"] = *normalized.Window7dMinutes
		}
		if reset5hAt := codexResetAtRFC3339(baseTime, normalized.Reset5hSeconds); reset5hAt != nil {
			updates["codex_5h_reset_at"] = *reset5hAt
		}
		if reset7dAt := codexResetAtRFC3339(baseTime, normalized.Reset7dSeconds); reset7dAt != nil {
			updates["codex_7d_reset_at"] = *reset7dAt
		}
	}

	return updates
}

// updateCodexUsageSnapshot saves the Codex usage snapshot to account's Extra field
func (s *OpenAIGatewayService) updateCodexUsageSnapshot(ctx context.Context, accountID int64, snapshot *OpenAICodexUsageSnapshot) {
	if snapshot == nil {
		return
	}
	if s == nil || s.accountRepo == nil {
		return
	}

	now := time.Now()
	updates := buildCodexUsageExtraUpdates(snapshot, now)
	if len(updates) == 0 {
		return
	}
	if !s.getCodexSnapshotThrottle().Allow(accountID, now) {
		return
	}

	go func() {
		updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.accountRepo.UpdateExtra(updateCtx, accountID, updates)
	}()
}

func (s *OpenAIGatewayService) UpdateCodexUsageSnapshotFromHeaders(ctx context.Context, accountID int64, headers http.Header) {
	if accountID <= 0 || headers == nil {
		return
	}
	if snapshot := ParseCodexRateLimitHeaders(headers); snapshot != nil {
		s.updateCodexUsageSnapshot(ctx, accountID, snapshot)
	}
}

func getOpenAIReasoningEffortFromReqBody(reqBody map[string]any, requestedModel string) (value string, present bool) {
	if reqBody == nil {
		return "", false
	}

	// Primary: reasoning.effort
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			return normalizeOpenAIReasoningEffortForModel(effort, requestedModel), true
		}
	}

	// Fallback: some clients may use a flat field.
	if effort, ok := reqBody["reasoning_effort"].(string); ok {
		return normalizeOpenAIReasoningEffortForModel(effort, requestedModel), true
	}

	return "", false
}

func deriveOpenAIReasoningEffortFromModel(model string) string {
	if strings.TrimSpace(model) == "" {
		return ""
	}

	modelID := strings.TrimSpace(model)
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}

	parts := strings.FieldsFunc(strings.ToLower(modelID), func(r rune) bool {
		switch r {
		case '-', '_', ' ':
			return true
		default:
			return false
		}
	})
	if len(parts) == 0 {
		return ""
	}

	return normalizeOpenAIReasoningEffortForModel(parts[len(parts)-1], modelID)
}

func deriveOpenAIReasoningEffortFromModelCandidates(models []string) string {
	for _, model := range models {
		if value := deriveOpenAIReasoningEffortFromModel(model); value != "" {
			return value
		}
	}
	return ""
}

type openAIRequestView struct {
	body               []byte
	Model              string
	Stream             bool
	PromptCacheKey     string
	PreviousResponseID string
	ServiceTier        string
	ReasoningEffort    string
	patches            []openAIRequestPatch
	patchesDisabled    bool
}

type openAIRequestPatch struct {
	path   string
	delete bool
	value  any
}

func newOpenAIRequestView(body []byte) openAIRequestView {
	if len(body) == 0 {
		return openAIRequestView{}
	}
	return openAIRequestView{
		body:               body,
		Model:              strings.TrimSpace(gjson.GetBytes(body, "model").String()),
		Stream:             gjson.GetBytes(body, "stream").Bool(),
		PromptCacheKey:     strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()),
		PreviousResponseID: strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()),
		ServiceTier:        strings.TrimSpace(gjson.GetBytes(body, "service_tier").String()),
		ReasoningEffort:    strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()),
	}
}

// Decode 保留阶段一既有 full-map 行为；后续阶段会把调用点下沉到复杂分支。
func (v openAIRequestView) Decode(c *gin.Context) (map[string]any, error) {
	return getOpenAIRequestBodyMap(c, v.body)
}

func (v *openAIRequestView) MarkPatchSet(path string, value any) {
	if v == nil || v.patchesDisabled {
		return
	}
	path = strings.TrimSpace(path)
	if !isSimpleOpenAIRequestPatchPath(path) {
		v.DisablePatches()
		return
	}
	v.patches = append(v.patches, openAIRequestPatch{path: path, value: value})
}

func (v *openAIRequestView) MarkPatchDelete(path string) {
	if v == nil || v.patchesDisabled {
		return
	}
	path = strings.TrimSpace(path)
	if !isSimpleOpenAIRequestPatchPath(path) {
		v.DisablePatches()
		return
	}
	v.patches = append(v.patches, openAIRequestPatch{path: path, delete: true})
}

func isSimpleOpenAIRequestPatchPath(path string) bool {
	if path == "" || strings.ContainsRune(path, '\\') {
		return false
	}
	for _, part := range strings.Split(path, ".") {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return true
}

func (v *openAIRequestView) DisablePatches() {
	if v == nil {
		return
	}
	v.patchesDisabled = true
	v.patches = nil
}

func (v openAIRequestView) HasPatches() bool {
	return !v.patchesDisabled && len(v.patches) > 0
}

func (v openAIRequestView) ApplyPatches() ([]byte, error) {
	if v.patchesDisabled || len(v.patches) == 0 {
		return nil, errors.New("openai request patches disabled")
	}
	body := v.body
	for _, patch := range v.patches {
		var err error
		if patch.delete {
			body, err = sjson.DeleteBytes(body, patch.path)
		} else {
			body, err = sjson.SetBytes(body, patch.path, patch.value)
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

func setOpenAIRequestMapPath(reqBody map[string]any, path string, value any) {
	path = strings.TrimSpace(path)
	if reqBody == nil || path == "" {
		return
	}
	parts := strings.Split(path, ".")
	current := reqBody
	for _, part := range parts[:len(parts)-1] {
		part = strings.TrimSpace(part)
		if part == "" {
			return
		}
		next, _ := current[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last != "" {
		current[last] = value
	}
}

func deleteOpenAIRequestMapPath(reqBody map[string]any, path string) {
	path = strings.TrimSpace(path)
	if reqBody == nil || path == "" {
		return
	}
	parts := strings.Split(path, ".")
	current := reqBody
	for _, part := range parts[:len(parts)-1] {
		part = strings.TrimSpace(part)
		if part == "" {
			return
		}
		next, _ := current[part].(map[string]any)
		if next == nil {
			return
		}
		current = next
	}
	last := strings.TrimSpace(parts[len(parts)-1])
	if last != "" {
		delete(current, last)
	}
}

func extractOpenAIRequestMetaFromBody(body []byte) (model string, stream bool, promptCacheKey string) {
	view := newOpenAIRequestView(body)
	return view.Model, view.Stream, view.PromptCacheKey
}

// normalizeOpenAIPassthroughOAuthBody 将透传 OAuth 请求体收敛为旧链路关键行为：
// 1) 删除 ChatGPT internal API 不支持的顶层 Responses 参数
// 2) store=false 3) 非 compact 保持 stream=true；compact 强制 stream=false
func normalizeOpenAIPassthroughOAuthBody(body []byte, compact bool) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}

	normalized := body
	changed := false

	for _, field := range openAIChatGPTInternalUnsupportedFields {
		if value := gjson.GetBytes(normalized, field); !value.Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(normalized, field)
		if err != nil {
			return body, false, fmt.Errorf("normalize passthrough body delete %s: %w", field, err)
		}
		normalized = next
		changed = true
	}

	// ChatGPT internal Responses API only accepts an input array. Normalizing
	// here keeps passthrough OAuth aligned with the transformed OAuth path and
	// avoids the upstream "Input must be a list" rejection.
	if inputResult := gjson.GetBytes(normalized, "input"); inputResult.Exists() {
		switch {
		case inputResult.Type == gjson.String:
			text := inputResult.String()
			var inputValue any
			if strings.TrimSpace(text) != "" {
				inputValue = []any{map[string]any{
					"type": "message", "role": "user", "content": text,
				}}
			} else {
				inputValue = []any{}
			}
			next, err := sjson.SetBytes(normalized, "input", inputValue)
			if err != nil {
				return body, false, fmt.Errorf("normalize passthrough body input string: %w", err)
			}
			normalized = next
			changed = true
		case inputResult.Type == gjson.JSON && !inputResult.IsArray():
			next, err := sjson.SetRawBytes(normalized, "input", []byte("["+inputResult.Raw+"]"))
			if err != nil {
				return body, false, fmt.Errorf("normalize passthrough body input object: %w", err)
			}
			normalized = next
			changed = true
		}
	}

	if compact {
		if store := gjson.GetBytes(normalized, "store"); store.Exists() {
			next, err := sjson.DeleteBytes(normalized, "store")
			if err != nil {
				return body, false, fmt.Errorf("normalize passthrough body delete store: %w", err)
			}
			normalized = next
			changed = true
		}
		if stream := gjson.GetBytes(normalized, "stream"); stream.Exists() {
			next, err := sjson.DeleteBytes(normalized, "stream")
			if err != nil {
				return body, false, fmt.Errorf("normalize passthrough body delete stream: %w", err)
			}
			normalized = next
			changed = true
		}
	} else {
		if store := gjson.GetBytes(normalized, "store"); !store.Exists() || store.Type != gjson.False {
			next, err := sjson.SetBytes(normalized, "store", false)
			if err != nil {
				return body, false, fmt.Errorf("normalize passthrough body store=false: %w", err)
			}
			normalized = next
			changed = true
		}
		if stream := gjson.GetBytes(normalized, "stream"); !stream.Exists() || stream.Type != gjson.True {
			next, err := sjson.SetBytes(normalized, "stream", true)
			if err != nil {
				return body, false, fmt.Errorf("normalize passthrough body stream=true: %w", err)
			}
			normalized = next
			changed = true
		}
	}

	return normalized, changed, nil
}

func stripOpenAITopLevelUndefinedStringFields(body []byte) ([]byte, bool, error) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false, nil
	}

	var fields []string
	root.ForEach(func(key, value gjson.Result) bool {
		if value.Type == gjson.String && isOpenAIUndefinedPlaceholder(value.String()) {
			fields = append(fields, key.String())
		}
		return true
	})
	if len(fields) == 0 {
		return body, false, nil
	}

	normalized := body
	for _, field := range fields {
		next, err := sjson.DeleteBytes(normalized, field)
		if err != nil {
			return body, false, fmt.Errorf("strip openai undefined field %s: %w", field, err)
		}
		normalized = next
	}
	return normalized, true, nil
}

func isOpenAIUndefinedPlaceholder(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "[undefined]", "undefined":
		return true
	default:
		return false
	}
}

func normalizeOpenAIPassthroughInstructions(body []byte, reqModel string) ([]byte, bool, error) {
	instructions := gjson.GetBytes(body, "instructions")
	if instructions.Exists() && instructions.Type == gjson.String && strings.TrimSpace(instructions.String()) != "" {
		return body, false, nil
	}
	if instructions.Exists() && instructions.Type != gjson.String {
		return body, false, nil
	}
	normalized, err := sjson.SetBytes(body, "instructions", defaultCodexSynthInstructions(reqModel))
	if err != nil {
		return body, false, fmt.Errorf("normalize passthrough instructions: %w", err)
	}
	return normalized, true, nil
}

func detectOpenAIPassthroughInstructionsRejectReason(reqModel string, body []byte) string {
	model := strings.ToLower(strings.TrimSpace(reqModel))
	if !strings.Contains(model, "codex") {
		return ""
	}

	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() {
		return "instructions_missing"
	}
	if instructions.Type != gjson.String {
		return "instructions_not_string"
	}
	if strings.TrimSpace(instructions.String()) == "" {
		return "instructions_empty"
	}
	return ""
}

func extractOpenAIReasoningEffortFromBody(body []byte, modelCandidates ...string) *string {
	reasoningEffort := strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())
	if reasoningEffort == "" {
		reasoningEffort = strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	}
	if reasoningEffort != "" {
		normalized := normalizeOpenAIReasoningEffortForModel(reasoningEffort, firstNonEmpty(modelCandidates...))
		if normalized == "" {
			return nil
		}
		return &normalized
	}

	value := deriveOpenAIReasoningEffortFromModelCandidates(modelCandidates)
	if value == "" {
		return nil
	}
	return &value
}

// extractReasoningEffortAuditFromBody reads only an explicitly supplied value.
// Unlike the compatibility helper above it never derives a value from a model
// suffix and keeps none/minimal distinct for request-log auditing.
func extractReasoningEffortAuditFromBody(body []byte, paths ...string) *string {
	for _, path := range paths {
		field := gjson.GetBytes(body, path)
		if !field.Exists() || field.Type != gjson.String {
			continue
		}
		value := normalizeReasoningEffortAudit(field.String())
		if value != "" {
			return &value
		}
	}
	return nil
}

func normalizeReasoningEffortAudit(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "none", "minimal", "low", "medium", "high", "max":
		return value
	case "xhigh", "extrahigh":
		return "xhigh"
	default:
		// Preserve future provider values as audit evidence. Bound the value by
		// runes so an unexpected extension cannot inflate logs or split UTF-8.
		valueRunes := []rune(strings.ToLower(strings.TrimSpace(raw)))
		if len(valueRunes) > 32 {
			valueRunes = valueRunes[:32]
		}
		return string(valueRunes)
	}
}

// NormalizeReasoningEffortAudit normalizes known effort levels while keeping
// a bounded unknown extension for forward-compatible request-log auditing.
func NormalizeReasoningEffortAudit(raw string) string {
	return normalizeReasoningEffortAudit(raw)
}

// ExtractRequestedOpenAIReasoningEffort returns only a client-explicit value;
// it never infers an effort from the model name or provider defaults.
func ExtractRequestedOpenAIReasoningEffort(body []byte) *string {
	return extractReasoningEffortAuditFromBody(body, "reasoning.effort", "reasoning_effort")
}

func requestedReasoningEffortFromContextOrBody(ctx context.Context, body []byte, paths ...string) *string {
	if audit, ok := gatewaylite.ReasoningEffortAuditFromContext(ctx); ok && strings.TrimSpace(audit.Requested) != "" {
		value := strings.TrimSpace(audit.Requested)
		return &value
	}
	return extractReasoningEffortAuditFromBody(body, paths...)
}

func setOpenAIResultReasoningAudit(result *OpenAIForwardResult, requested, final *string) {
	if result == nil {
		return
	}
	result.RequestedReasoningEffort = cloneOptionalString(requested)
	result.ReasoningEffort = cloneOptionalString(final)
}

func extractOpenAIServiceTier(reqBody map[string]any) *string {
	if reqBody == nil {
		return nil
	}
	raw, ok := reqBody["service_tier"].(string)
	if !ok {
		return nil
	}
	return normalizeOpenAIServiceTier(raw)
}

func extractOpenAIServiceTierFromBody(body []byte) *string {
	if len(body) == 0 {
		return nil
	}
	return normalizeOpenAIServiceTier(gjson.GetBytes(body, "service_tier").String())
}

func normalizeOpenAIServiceTier(raw string) *string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return nil
	}
	if value == "fast" {
		value = "priority"
	}
	// 放过 OpenAI 官方文档定义的所有合法 tier 值：priority/flex/auto/default/scale。
	// 对 Codex 客户端零影响（Codex 只发 priority 或 flex，见 codex-rs/core/src/client.rs），
	// 但能让直连 OpenAI SDK 的用户透传 auto/default/scale 以便抓包/调试。
	// 真未知值仍返回 nil，由 normalizeResponsesBodyServiceTier 从 body 中删除。
	switch value {
	case "priority", "flex", "auto", "default", "scale":
		return &value
	default:
		return nil
	}
}

// OpenAIFastBlockedError indicates a request was rejected by the OpenAI fast
// policy (action=block). Mirrors BetaBlockedError on the Claude side.
type OpenAIFastBlockedError struct {
	Message    string
	StatusCode int
	ErrorType  string
	Code       string
}

func (e *OpenAIFastBlockedError) Error() string { return e.Message }

func (e *OpenAIFastBlockedError) HTTPStatusCode() int {
	if e != nil && e.StatusCode > 0 {
		return e.StatusCode
	}
	return http.StatusForbidden
}

func (e *OpenAIFastBlockedError) APIErrorType() string {
	if e != nil && strings.TrimSpace(e.ErrorType) != "" {
		return strings.TrimSpace(e.ErrorType)
	}
	return "permission_error"
}

func (e *OpenAIFastBlockedError) APIErrorCode() string {
	if e != nil && strings.TrimSpace(e.Code) != "" {
		return strings.TrimSpace(e.Code)
	}
	return "policy_violation"
}

// evaluateOpenAIFastPolicy returns the action and error message that should be
// applied for a request with the given account/model/service_tier. When the
// policy service is unavailable or no rule matches, it returns
// (BetaPolicyActionPass, "") so callers can short-circuit safely.
//
// Matching rules:
//   - Scope filters by account type (all / oauth / apikey / bedrock)
//   - ServiceTier must be empty (= any), "all", or equal the normalized tier
//   - ModelWhitelist narrows the rule to specific models; FallbackAction
//     handles the non-matching case (default: pass)
//
// 与 Claude BetaPolicy 的差异（保留首条匹配 short-circuit）：
//   - BetaPolicy 处理的是 anthropic-beta header 中的 token 集合，不同
//     规则可能针对不同 token，filter 需要累加成 set；block 则 first-match。
//   - OpenAI fast policy 操作的是单个字段 service_tier：filter 即删字段，
//     没有可累加的对象。一次请求只携带一个 service_tier，规则的 tier
//     维度天然互斥；同一 (scope, tier) 下若多条规则的 model whitelist
//     发生重叠，admin 可通过规则顺序明确意图。因此采用 first-match 而
//     非 BetaPolicy 那样的"block 覆盖 filter 覆盖 pass"语义。
func (s *OpenAIGatewayService) evaluateOpenAIFastPolicy(ctx context.Context, account *Account, model, serviceTier string) (action, errMsg string) {
	match := s.evaluateOpenAIFastPolicyMatch(ctx, account, model, serviceTier)
	return match.Action, match.ErrorMessage
}

type openAIFastPolicyMatch struct {
	Action       string
	ErrorMessage string
	RuleID       string
	UsedFallback bool
}

func (s *OpenAIGatewayService) evaluateOpenAIFastPolicyMatch(ctx context.Context, account *Account, model, serviceTier string) openAIFastPolicyMatch {
	if s == nil || s.settingService == nil {
		return openAIFastPolicyMatch{Action: BetaPolicyActionPass}
	}
	tier := strings.ToLower(strings.TrimSpace(serviceTier))
	if tier == "" {
		return openAIFastPolicyMatch{Action: BetaPolicyActionPass}
	}
	settings := openAIFastPolicySettingsFromContext(ctx)
	if settings == nil {
		fetched, err := s.settingService.GetOpenAIFastPolicySettings(ctx)
		if err != nil || fetched == nil {
			return openAIFastPolicyMatch{Action: BetaPolicyActionPass}
		}
		settings = fetched
	}
	return evaluateOpenAIFastPolicyWithSettingsDetailed(settings, openAIFastPolicyUserID(ctx), account, model, tier)
}

func (s *OpenAIGatewayService) openAIFastPolicySettings(ctx context.Context) *OpenAIFastPolicySettings {
	settings := openAIFastPolicySettingsFromContext(ctx)
	if settings != nil {
		return settings
	}
	if s == nil || s.settingService == nil {
		return DefaultOpenAIFastPolicySettings()
	}
	fetched, err := s.settingService.GetOpenAIFastPolicySettings(ctx)
	if err != nil || fetched == nil {
		return DefaultOpenAIFastPolicySettings()
	}
	return fetched
}

func (s *OpenAIGatewayService) decideOpenAIPriorityPolicy(
	ctx context.Context,
	account *Account,
	model string,
	rawServiceTier string,
) (OpenAIPriorityDecision, error) {
	settings := s.openAIFastPolicySettings(ctx)
	requested := normalizeOpenAIServiceTier(rawServiceTier)
	requestedAudit := strings.ToLower(strings.TrimSpace(rawServiceTier))
	decision := OpenAIPriorityDecision{
		ServiceTier:                requested,
		PolicyAction:               BetaPolicyActionPass,
		DetectionByRequestEnabled:  settings.DetectionByRequestEnabled,
		DetectionByResponseEnabled: settings.DetectionByResponseEnabled,
	}
	if requestedAudit != "" {
		decision.RequestedServiceTier = &requestedAudit
	}
	if retry, ok := openAIPriorityStandardRetryFromContext(ctx); ok {
		decision.RequestedServiceTier = cloneOptionalString(retry.RequestedServiceTier)
		decision.ServiceTier = nil
		decision.PolicyAction = retry.PolicyAction
		decision.PolicyRuleID = retry.PolicyRuleID
		decision.PolicyRuleFallback = retry.PolicyRuleFallback
		decision.DetectionByRequestEnabled = retry.DetectByRequest
		decision.DetectionByResponseEnabled = retry.DetectByResponse
		decision.FallbackReason = retry.Reason
		return decision, nil
	}

	if requested == nil {
		decision.ServiceTier = nil
		return decision, nil
	}

	match := s.evaluateOpenAIFastPolicyMatch(ctx, account, model, *requested)
	decision.PolicyAction = match.Action
	decision.PolicyRuleID = match.RuleID
	decision.PolicyRuleFallback = match.UsedFallback
	switch match.Action {
	case BetaPolicyActionBlock:
		msg := match.ErrorMessage
		if msg == "" {
			msg = fmt.Sprintf("openai service_tier=%s is not allowed for model %s", *requested, model)
		}
		return decision, &OpenAIFastBlockedError{Message: msg}
	case BetaPolicyActionFilter:
		decision.ServiceTier = nil
	case OpenAIFastPolicyActionForcePriority:
		priority := OpenAIFastTierPriority
		decision.ServiceTier = &priority
	default:
		decision.ServiceTier = requested
	}

	if decision.ServiceTier != nil && normalizeBillingServiceTier(*decision.ServiceTier) == OpenAIFastTierPriority {
		if !s.openAIModelSupportsPriority(model) {
			return decision, &OpenAIFastBlockedError{
				Message:    fmt.Sprintf("model %s does not support Priority (优先处理) mode", model),
				StatusCode: http.StatusBadRequest,
				ErrorType:  "invalid_request_error",
				Code:       "priority_model_unsupported",
			}
		}
		if ready, reason := s.openAIModelPriorityPricingReady(model, false); !ready {
			return decision, &OpenAIFastBlockedError{
				Message:    fmt.Sprintf("Priority pricing is incomplete for model %s: %s / 模型 %s 的优先处理价格配置不完整", model, reason, model),
				StatusCode: http.StatusBadRequest,
				ErrorType:  "invalid_request_error",
				Code:       "priority_pricing_unavailable",
			}
		}
	}
	return decision, nil
}

func (s *OpenAIGatewayService) validateOpenAIPriorityInputContext(
	ctx context.Context,
	model string,
	body []byte,
	decision *OpenAIPriorityDecision,
) error {
	if decision == nil || decision.ServiceTier == nil || normalizeBillingServiceTier(*decision.ServiceTier) != OpenAIFastTierPriority {
		return nil
	}

	_, threshold := s.resolveOpenAILongContextThreshold(model)
	if threshold <= 0 {
		return nil
	}

	estimated, err := estimateOpenAIInputTokensFromRequestBody(model, body)
	if err != nil {
		logger.L().Warn("openai priority input token estimate failed; upstream remains source of truth",
			zap.String("model", model),
			zap.Int("long_context_threshold", threshold),
			zap.Error(err),
		)
		return nil
	}
	if estimated <= threshold {
		return nil
	}
	if ready, _ := s.openAIModelPriorityPricingReady(model, true); ready {
		return nil
	}
	return &OpenAIFastBlockedError{
		Message: fmt.Sprintf(
			"Priority pricing is incomplete for input contexts above %d tokens. Remove service_tier=priority or shorten the input. / 超过 %d Token 的优先处理价格配置不完整，请关闭优先处理或缩短输入。",
			threshold,
			threshold,
		),
		StatusCode: http.StatusBadRequest,
		ErrorType:  "invalid_request_error",
		Code:       "priority_long_context_pricing_unavailable",
	}
}

func (s *OpenAIGatewayService) openAIModelSupportsPriority(model string) bool {
	if s != nil && s.billingService != nil {
		pricing, err := s.billingService.GetModelPricing(model)
		if err == nil && pricing != nil {
			return hasExplicitPriorityPricing(pricing)
		}
	}
	if pricing := officialOpenAITextPricingForModel(normalizeKnownOpenAICodexModel(model)); pricing != nil {
		return pricing.InputCostPerTokenPriority > 0 || pricing.OutputCostPerTokenPriority > 0 ||
			pricing.CacheCreationInputTokenCostPriority > 0 || pricing.CacheReadInputTokenCostPriority > 0
	}
	normalized := normalizeKnownOpenAICodexModel(model)
	return normalized == "gpt-5.4-mini" || normalized == "gpt-5.2" || normalized == "gpt-5.3-codex" || normalized == "gpt-5.3-codex-spark"
}

func (s *OpenAIGatewayService) openAIModelPriorityPricingReady(model string, longContext bool) (bool, string) {
	if s != nil && s.billingService != nil {
		pricing, err := s.billingService.GetModelPricing(model)
		if err != nil || pricing == nil {
			return false, "model_pricing_missing"
		}
		return openAIModelPriorityPricingFieldsReady(model, pricing, longContext)
	}
	pricing := officialOpenAITextPricingForModel(normalizeKnownOpenAICodexModel(model))
	if pricing == nil || (pricing.InputCostPerTokenPriority <= 0 && pricing.OutputCostPerTokenPriority <= 0 &&
		pricing.CacheCreationInputTokenCostPriority <= 0 && pricing.CacheReadInputTokenCostPriority <= 0) {
		return false, "supports_service_tier"
	}
	if longContext {
		if pricing.InputCostPerTokenPriorityAbove272K <= 0 {
			return false, "input_cost_per_token_priority_above_272k"
		}
		if pricing.OutputCostPerTokenPriorityAbove272K <= 0 {
			return false, "output_cost_per_token_priority_above_272k"
		}
		return true, ""
	}
	if pricing.InputCostPerTokenPriority <= 0 {
		return false, "input_cost_per_token_priority"
	}
	if pricing.OutputCostPerTokenPriority <= 0 {
		return false, "output_cost_per_token_priority"
	}
	if pricing.CacheReadInputTokenCost > 0 && pricing.CacheReadInputTokenCostPriority <= 0 {
		return false, "cache_read_cost_per_token_priority"
	}
	if requiresBillableOpenAICacheWrites(model) && pricing.CacheCreationInputTokenCostPriority <= 0 {
		return false, "cache_creation_cost_per_token_priority"
	}
	return true, ""
}

func openAIModelPriorityPricingFieldsReady(model string, pricing *ModelPricing, longContext bool) (bool, string) {
	if !hasExplicitPriorityPricing(pricing) {
		return false, "supports_service_tier"
	}
	inputPrice := pricing.InputPricePerTokenPriority
	outputPrice := pricing.OutputPricePerTokenPriority
	cacheReadPrice := pricing.CacheReadPricePerTokenPriority
	cacheWritePrice := pricing.CacheCreationPricePerTokenPriority
	standardCacheReadPrice := pricing.CacheReadPricePerToken
	if longContext {
		inputPrice = pricing.LongContextInputPricePerTokenPriority
		outputPrice = pricing.LongContextOutputPricePerTokenPriority
		cacheReadPrice = pricing.LongContextCacheReadPricePerTokenPriority
		cacheWritePrice = pricing.LongContextCacheCreationPricePerTokenPriority
		standardCacheReadPrice = pricing.LongContextCacheReadPricePerToken
	}
	if inputPrice <= 0 {
		return false, "input_price"
	}
	if outputPrice <= 0 {
		return false, "output_price"
	}
	if standardCacheReadPrice > 0 && cacheReadPrice <= 0 {
		return false, "cache_read_price"
	}
	if requiresBillableOpenAICacheWrites(model) && cacheWritePrice <= 0 {
		return false, "cache_write_price"
	}
	return true, ""
}

func logOpenAIKeyPriorityFallback(ctx context.Context, account *Account, model, reason string) {
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.String("model", strings.TrimSpace(model)),
		zap.String("priority_fallback_reason", strings.TrimSpace(reason)),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.FromContext(ctx).With(fields...).Info("openai_priority.fallback_to_standard")
}

func openAIPriorityCanRetryStandard(ctx context.Context, decision OpenAIPriorityDecision) bool {
	if _, alreadyRetried := openAIPriorityStandardRetryFromContext(ctx); alreadyRetried {
		return false
	}
	return decision.ServiceTier != nil && normalizeBillingServiceTier(*decision.ServiceTier) == OpenAIFastTierPriority
}

func openAIServiceTierUnsupportedResponse(statusCode int, code, message string, body []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	combined := strings.ToLower(strings.Join([]string{code, message, string(body)}, " "))
	return openAIServiceTierUnsupportedText(combined)
}

func openAIServiceTierUnsupportedText(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || (!strings.Contains(value, "service_tier") && !strings.Contains(value, "service tier")) {
		return false
	}
	for _, marker := range []string{"unsupported", "not supported", "unknown parameter", "unrecognized", "not allowed", "invalid value"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func logOpenAIPriorityStandardRetry(ctx context.Context, account *Account, model, reason string) {
	fields := []zap.Field{
		zap.String("component", "service.openai_gateway"),
		zap.String("model", strings.TrimSpace(model)),
		zap.String("priority_fallback_reason", strings.TrimSpace(reason)),
	}
	if account != nil {
		fields = append(fields, zap.Int64("account_id", account.ID))
	}
	logger.FromContext(ctx).With(fields...).Info("openai_priority.retry_standard_before_output")
}

// evaluateOpenAIFastPolicyWithSettings is the pure-function core extracted so
// long-lived sessions (e.g. WS) can prefetch settings once and avoid hitting
// the settingService on every frame. See WSSession entry and
// openAIFastPolicySettingsFromContext for the caching glue.
func evaluateOpenAIFastPolicyWithSettings(settings *OpenAIFastPolicySettings, account *Account, model, tier string) (action, errMsg string) {
	match := evaluateOpenAIFastPolicyWithSettingsDetailed(settings, 0, account, model, tier)
	return match.Action, match.ErrorMessage
}

func evaluateOpenAIFastPolicyWithSettingsDetailed(settings *OpenAIFastPolicySettings, userID int64, account *Account, model, tier string) openAIFastPolicyMatch {
	if settings == nil {
		return openAIFastPolicyMatch{Action: BetaPolicyActionPass}
	}
	isOAuth := account != nil && account.IsOAuth()
	isBedrock := account != nil && account.IsBedrock()

	// 指定用户规则优先于全局规则；同一层内按管理员排序取首条命中。
	for _, userScoped := range []bool{true, false} {
		for _, rule := range settings.Rules {
			if (len(rule.UserIDs) > 0) != userScoped || !openAIFastPolicyUserMatches(rule.UserIDs, userID) {
				continue
			}
			if !betaPolicyScopeMatches(rule.Scope, isOAuth, isBedrock) {
				continue
			}
			ruleTier := strings.ToLower(strings.TrimSpace(rule.ServiceTier))
			if ruleTier != "" && ruleTier != OpenAIFastTierAny && ruleTier != tier {
				continue
			}
			action, message, fallback := resolveOpenAIFastPolicyRuleAction(rule, model)
			return openAIFastPolicyMatch{
				Action:       action,
				ErrorMessage: message,
				RuleID:       strings.TrimSpace(rule.ID),
				UsedFallback: fallback,
			}
		}
	}
	return openAIFastPolicyMatch{Action: BetaPolicyActionPass}
}

func resolveOpenAIFastPolicyRuleAction(rule OpenAIFastPolicyRule, model string) (action, message string, fallback bool) {
	if len(rule.ModelWhitelist) == 0 || matchModelWhitelist(model, rule.ModelWhitelist) {
		return rule.Action, rule.ErrorMessage, false
	}
	if rule.FallbackAction != "" {
		return rule.FallbackAction, rule.FallbackErrorMessage, true
	}
	return BetaPolicyActionPass, "", true
}

func openAIFastPolicyUserID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	if userID <= 0 {
		return 0
	}
	return userID
}

func openAIFastPolicyUserMatches(ruleUserIDs []int64, userID int64) bool {
	if len(ruleUserIDs) == 0 {
		return true
	}
	for _, ruleUserID := range ruleUserIDs {
		if ruleUserID == userID {
			return true
		}
	}
	return false
}

// openAIFastPolicyCtxKey 是 context 中预取的 OpenAIFastPolicySettings 缓存
// 键，仅用于 WebSocket 长会话内多帧复用同一份策略快照，避免每帧 DB 命中。
//
// Trade-off：策略变更不会影响当前 WS session（只影响新 session）。这是
// 有意为之 —— 对长会话来说，"策略一致性"比"立刻生效"更重要，且 Claude
// BetaPolicy 的 gin.Context 缓存也是同样取舍。需要 hot-reload 时管理员
// 可以通过踢断 session 强制刷新。
type openAIFastPolicyCtxKeyType struct{}

var openAIFastPolicyCtxKey = openAIFastPolicyCtxKeyType{}

// withOpenAIFastPolicyContext 将一份 settings 快照绑定到 context，供该 ctx
// 衍生 goroutine 中的 evaluateOpenAIFastPolicy 复用。
func withOpenAIFastPolicyContext(ctx context.Context, settings *OpenAIFastPolicySettings) context.Context {
	if ctx == nil || settings == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIFastPolicyCtxKey, settings)
}

func openAIFastPolicySettingsFromContext(ctx context.Context) *OpenAIFastPolicySettings {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(openAIFastPolicyCtxKey).(*OpenAIFastPolicySettings); ok {
		return v
	}
	return nil
}

// applyOpenAIFastPolicyToBody applies the OpenAI fast policy to a raw request
// body. When action=filter it removes the service_tier field; when
// action=block it returns (body, *OpenAIFastBlockedError). On pass it
// normalizes the service_tier value (e.g. client alias "fast" → "priority"),
// rewriting the body so the upstream receives a slug it recognizes.
//
// Rationale for normalize-on-pass: chat-completions / messages 入口在调用本
// 函数之前已经通过 normalizeResponsesBodyServiceTier 把 service_tier 归一化
// 到了上游可识别值；passthrough（OpenAI 自动透传） / native /responses 等
// 入口没有这一前置步骤，pass 路径下若不在此处归一化，"fast" 就会被原样
// 透传到 OpenAI 上游导致 400/拒绝。把归一化收敛到本函数，所有入口行为一致。
func (s *OpenAIGatewayService) applyOpenAIFastPolicyToBody(ctx context.Context, account *Account, model string, body []byte) ([]byte, error) {
	updated, _, err := s.applyOpenAIPriorityPolicyToBody(ctx, account, model, body)
	return updated, err
}

func (s *OpenAIGatewayService) applyOpenAIPriorityPolicyToBody(
	ctx context.Context,
	account *Account,
	model string,
	body []byte,
) ([]byte, OpenAIPriorityDecision, error) {
	if len(body) == 0 {
		decision, err := s.decideOpenAIPriorityPolicy(ctx, account, model, "")
		return body, decision, err
	}
	tierField := gjson.GetBytes(body, "service_tier")
	rawTier := ""
	if tierField.Exists() && tierField.Type == gjson.String {
		rawTier = tierField.String()
	}
	if IsImageGenerationIntent(openAIResponsesEndpoint, model, body) || isOpenAIImageGenerationModel(model) {
		settings := s.openAIFastPolicySettings(ctx)
		requestedAudit := strings.ToLower(strings.TrimSpace(rawTier))
		var requested *string
		if requestedAudit != "" {
			requested = &requestedAudit
		}
		return body, OpenAIPriorityDecision{
			RequestedServiceTier:       requested,
			PolicyAction:               BetaPolicyActionPass,
			DetectionByRequestEnabled:  settings.DetectionByRequestEnabled,
			DetectionByResponseEnabled: settings.DetectionByResponseEnabled,
		}, nil
	}
	decision, err := s.decideOpenAIPriorityPolicy(ctx, account, model, rawTier)
	if err != nil {
		return body, decision, err
	}
	if err := s.validateOpenAIPriorityInputContext(ctx, model, body, &decision); err != nil {
		return body, decision, err
	}
	if decision.ServiceTier == nil {
		if !tierField.Exists() {
			return body, decision, nil
		}
		if normalizeOpenAIServiceTier(rawTier) == nil {
			decision.PolicyAction = "filter_invalid"
			decision.FallbackReason = "invalid_service_tier"
		}
		trimmed, err := sjson.DeleteBytes(body, "service_tier")
		if err != nil {
			return body, decision, fmt.Errorf("strip service_tier from body: %w", err)
		}
		return trimmed, decision, nil
	}
	if tierField.Exists() && rawTier == *decision.ServiceTier {
		return body, decision, nil
	}
	updated, err := sjson.SetBytes(body, "service_tier", *decision.ServiceTier)
	if err != nil {
		return body, decision, fmt.Errorf("apply openai priority policy: %w", err)
	}
	return updated, decision, nil
}

// writeOpenAIFastPolicyBlockedResponse writes a 403 JSON response for a
// request blocked by the OpenAI fast policy.
func writeOpenAIFastPolicyBlockedResponse(c *gin.Context, err *OpenAIFastBlockedError) {
	if c == nil || err == nil {
		return
	}
	MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
	c.JSON(err.HTTPStatusCode(), gin.H{
		"error": gin.H{
			"type":    err.APIErrorType(),
			"code":    err.APIErrorCode(),
			"message": err.Message,
		},
	})
}

// applyOpenAIFastPolicyToWSResponseCreate evaluates the OpenAI fast policy
// against a single client→upstream WebSocket frame whose top-level
// "type"=="response.create". It mirrors the HTTP-side
// applyOpenAIFastPolicyToBody contract but operates on a Realtime/Responses
// WS payload:
//
//   - pass: keeps service_tier, normalizing aliases such as "fast" to "priority"
//   - filter: returns a copy with top-level service_tier removed
//   - block: returns (frame, *OpenAIFastBlockedError)
//
// Only frames whose "type" field strictly equals "response.create" are
// inspected/mutated. Any other frame type — including the empty string —
// passes through untouched. The OpenAI Realtime client-event spec requires
// "type" to be set, so an empty type is treated as a malformed frame we do
// not police; the upstream is the source of truth for rejecting it.
//
// service_tier lives at the top level of response.create — same as the
// Responses HTTP body shape (see openai_gateway_chat_completions.go:304 +
// extractOpenAIServiceTierFromBody at line 5593, and the test fixture at
// openai_ws_forwarder_ingress_session_test.go:402). We therefore only need
// to inspect / strip the top-level field; there is no nested form in the
// schema today.
//
// The caller is responsible for choosing the upstream model passed in —
// this helper does not re-derive it.
func (s *OpenAIGatewayService) applyOpenAIFastPolicyToWSResponseCreate(
	ctx context.Context,
	account *Account,
	model string,
	frame []byte,
) ([]byte, *OpenAIFastBlockedError, error) {
	updated, _, blocked, err := s.applyOpenAIPriorityPolicyToWSResponseCreate(ctx, account, model, frame)
	return updated, blocked, err
}

func (s *OpenAIGatewayService) applyOpenAIPriorityPolicyToWSResponseCreate(
	ctx context.Context,
	account *Account,
	model string,
	frame []byte,
) ([]byte, OpenAIPriorityDecision, *OpenAIFastBlockedError, error) {
	settings := s.openAIFastPolicySettings(ctx)
	defaultDecision := OpenAIPriorityDecision{
		PolicyAction:               BetaPolicyActionPass,
		DetectionByRequestEnabled:  settings.DetectionByRequestEnabled,
		DetectionByResponseEnabled: settings.DetectionByResponseEnabled,
	}
	if len(frame) == 0 {
		return frame, defaultDecision, nil, nil
	}
	if !gjson.ValidBytes(frame) {
		return frame, defaultDecision, nil, nil
	}
	frameType := strings.TrimSpace(gjson.GetBytes(frame, "type").String())
	// Strict match: only response.create is policy-checked. Empty / other
	// types pass through untouched so we never accidentally strip fields
	// from response.cancel, conversation.item.create, or any future
	// client-event the spec adds. The Realtime spec requires "type" on
	// every client event, so an empty type is malformed input — let the
	// upstream reject it rather than guessing at our layer.
	if frameType != "response.create" {
		return frame, defaultDecision, nil, nil
	}
	updated, decision, err := s.applyOpenAIPriorityPolicyToBody(ctx, account, model, frame)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			return frame, decision, blocked, nil
		}
		return frame, decision, nil, err
	}
	return updated, decision, nil, nil
}

// newOpenAIFastPolicyWSEventID returns a Realtime-style event_id for a
// server-emitted error event. Matches the loose "evt_<rand>" convention used
// by upstream Realtime servers; the exact value is not load-bearing and is
// only required for client-side log correlation. We reuse the existing
// google/uuid dependency rather than pulling a new one.
func newOpenAIFastPolicyWSEventID() string {
	id, err := uuid.NewRandom()
	if err != nil {
		// Extremely unlikely; fall back to a fixed prefix so the field is
		// still non-empty and the schema stays self-consistent.
		return "evt_openai_fast_policy"
	}
	// Strip dashes so it visually matches "evt_<hex>" rather than UUID v4
	// canonical form, mirroring what real Realtime traces look like.
	return "evt_" + strings.ReplaceAll(id.String(), "-", "")
}

// buildOpenAIFastPolicyBlockedWSEvent renders an OpenAI Realtime/Responses
// style "error" event payload for a request blocked by the OpenAI fast
// policy. The shape mirrors Realtime error events as observed in upstream
// traces and per the spec's server "error" event:
//
//	{
//	  "event_id": "evt_<random>",
//	  "type": "error",
//	  "error": {
//	    "type": "invalid_request_error",
//	    "code": "policy_violation",
//	    "message": "..."
//	  }
//	}
//
// event_id lets clients correlate the rejection in their logs; "code" gives
// programmatic clients a stable identifier (HTTP-side equivalent is the
// 403 permission_error JSON body).
func buildOpenAIFastPolicyBlockedWSEvent(err *OpenAIFastBlockedError) []byte {
	if err == nil {
		return nil
	}
	eventID := newOpenAIFastPolicyWSEventID()
	payload, mErr := json.Marshal(map[string]any{
		"event_id": eventID,
		"type":     "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"code":    err.APIErrorCode(),
			"message": err.Message,
		},
	})
	if mErr != nil {
		// Fallback to a minimal hand-rolled payload; Marshal of the literal
		// shape above should never fail in practice.
		return []byte(`{"event_id":"` + eventID + `","type":"error","error":{"type":"invalid_request_error","code":"policy_violation","message":"openai fast policy blocked this request"}}`)
	}
	return payload
}

func openAIRequestBodyMayContainImageInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	messages := gjson.GetBytes(body, "messages.#-1")
	return openAIJSONValueMayContainImageInput(input) || openAIJSONValueMayContainImageInput(messages)
}

func openAIJSONValueMayContainImageInput(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool {
			if openAIJSONValueMayContainImageInput(item) {
				found = true
				return false
			}
			return true
		})
		return found
	}
	if value.IsObject() {
		if strings.TrimSpace(value.Get("type").String()) == "input_image" || value.Get("image_url").Exists() {
			return true
		}
		return openAIJSONValueMayContainImageInput(value.Get("content"))
	}
	return false
}

func openAIRequestBodyMayContainEmptyBase64InputImage(body []byte) bool {
	if len(body) == 0 || !openAIRequestBodyMayContainInputImageToken(body) {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	return openAIJSONValueMayContainEmptyBase64InputImage(input)
}

func openAIRequestBodyMayContainInputImageToken(body []byte) bool {
	if bytes.Contains(body, []byte("input_image")) {
		return true
	}
	// JSON 字符串任意字符都可能被 unicode escape，遇到 \u 时交给 gjson 解码后的结构扫描兜底。
	return bytes.Contains(body, []byte("\\u"))
}

func openAIJSONValueMayContainEmptyBase64InputImage(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	if value.IsArray() {
		found := false
		value.ForEach(func(_, item gjson.Result) bool {
			if openAIJSONValueMayContainEmptyBase64InputImage(item) {
				found = true
				return false
			}
			return true
		})
		return found
	}
	if value.IsObject() {
		if strings.TrimSpace(value.Get("type").String()) == "input_image" && isEmptyBase64DataURI(value.Get("image_url").String()) {
			return true
		}
		return openAIJSONValueMayContainEmptyBase64InputImage(value.Get("content"))
	}
	return false
}

func sanitizeEmptyBase64InputImagesInOpenAIBody(body []byte) ([]byte, bool, error) {
	if !openAIRequestBodyMayContainEmptyBase64InputImage(body) {
		return body, false, nil
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body, false, fmt.Errorf("sanitize request body: %w", err)
	}
	if !sanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(reqBody) {
		return body, false, nil
	}
	normalized, err := marshalOpenAIUpstreamJSON(reqBody)
	if err != nil {
		return body, false, fmt.Errorf("serialize sanitized request body: %w", err)
	}
	return normalized, true, nil
}

func sanitizeEmptyBase64InputImagesInOpenAIRequestBodyMap(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	input, ok := reqBody["input"]
	if !ok {
		return false
	}
	normalizedInput, changed := sanitizeEmptyBase64InputImagesInOpenAIInput(input)
	if !changed {
		return false
	}
	reqBody["input"] = normalizedInput
	return true
}

func sanitizeEmptyBase64InputImagesInOpenAIInput(input any) (any, bool) {
	items, ok := input.([]any)
	if !ok {
		return input, false
	}

	normalizedItems := make([]any, 0, len(items))
	changed := false
	for _, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			normalizedItems = append(normalizedItems, item)
			continue
		}
		if shouldDropEmptyBase64InputImagePart(itemMap) {
			changed = true
			continue
		}
		content, ok := itemMap["content"]
		if !ok {
			normalizedItems = append(normalizedItems, itemMap)
			continue
		}
		parts, ok := content.([]any)
		if !ok {
			normalizedItems = append(normalizedItems, itemMap)
			continue
		}

		normalizedParts := make([]any, 0, len(parts))
		itemChanged := false
		for _, part := range parts {
			if shouldDropEmptyBase64InputImagePart(part) {
				changed = true
				itemChanged = true
				continue
			}
			normalizedParts = append(normalizedParts, part)
		}
		if itemChanged {
			if len(normalizedParts) == 0 {
				continue
			}
			itemMap["content"] = normalizedParts
		}
		normalizedItems = append(normalizedItems, itemMap)
	}
	if !changed {
		return input, false
	}
	return normalizedItems, true
}

func shouldDropEmptyBase64InputImagePart(part any) bool {
	partMap, ok := part.(map[string]any)
	if !ok {
		return false
	}
	typeValue, _ := partMap["type"].(string)
	if strings.TrimSpace(typeValue) != "input_image" {
		return false
	}
	imageURL, _ := partMap["image_url"].(string)
	return isEmptyBase64DataURI(imageURL)
}

func isEmptyBase64DataURI(raw string) bool {
	if !strings.HasPrefix(raw, "data:") {
		return false
	}
	rest := strings.TrimPrefix(raw, "data:")
	semicolonIdx := strings.Index(rest, ";")
	if semicolonIdx < 0 {
		return false
	}
	rest = rest[semicolonIdx+1:]
	if !strings.HasPrefix(rest, "base64,") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(rest, "base64,")) == ""
}

func getOpenAIRequestBodyMap(_ *gin.Context, body []byte) (map[string]any, error) {
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	return reqBody, nil
}

func extractOpenAIReasoningEffort(reqBody map[string]any, modelCandidates ...string) *string {
	if value, present := getOpenAIReasoningEffortFromReqBody(reqBody, firstNonEmpty(modelCandidates...)); present {
		if value == "" {
			return nil
		}
		return &value
	}

	value := deriveOpenAIReasoningEffortFromModelCandidates(modelCandidates)
	if value == "" {
		return nil
	}
	return &value
}

func normalizeOpenAIReasoningEffortForModel(raw, model string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "max") && isOpenAIGPT56Model(model) {
		return "max"
	}
	return normalizeOpenAIReasoningEffort(raw)
}

func normalizeOpenAIReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}

	// Normalize separators for "x-high"/"x_high" variants.
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)

	switch value {
	case "none", "minimal":
		return ""
	case "low", "medium", "high":
		return value
	case "xhigh", "extrahigh", "max":
		return "xhigh"
	default:
		// Only store known effort levels for now to keep UI consistent.
		return ""
	}
}
