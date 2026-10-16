package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

const (
	BillingTypeBalance      int8 = 0 // 钱包余额
	BillingTypeSubscription int8 = 1 // 订阅套餐
)

type RequestType int16

const (
	RequestTypeUnknown      RequestType = 0
	RequestTypeSync         RequestType = 1
	RequestTypeStream       RequestType = 2
	RequestTypeWSV2         RequestType = 3
	RequestTypeCyberBlocked RequestType = 4 // cyber_policy 命中（透传但被上游安全策略拒绝）
	RequestTypeLive         RequestType = 5
)

func (t RequestType) IsValid() bool {
	switch t {
	case RequestTypeUnknown, RequestTypeSync, RequestTypeStream, RequestTypeWSV2, RequestTypeCyberBlocked, RequestTypeLive:
		return true
	default:
		return false
	}
}

func (t RequestType) Normalize() RequestType {
	if t.IsValid() {
		return t
	}
	return RequestTypeUnknown
}

func (t RequestType) String() string {
	switch t.Normalize() {
	case RequestTypeSync:
		return "sync"
	case RequestTypeStream:
		return "stream"
	case RequestTypeWSV2:
		return "ws_v2"
	case RequestTypeCyberBlocked:
		return "cyber"
	case RequestTypeLive:
		return "live"
	default:
		return "unknown"
	}
}

func RequestTypeFromInt16(v int16) RequestType {
	return RequestType(v).Normalize()
}

func ParseUsageRequestType(value string) (RequestType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unknown":
		return RequestTypeUnknown, nil
	case "sync":
		return RequestTypeSync, nil
	case "stream":
		return RequestTypeStream, nil
	case "ws_v2":
		return RequestTypeWSV2, nil
	case "cyber":
		return RequestTypeCyberBlocked, nil
	case "live":
		return RequestTypeLive, nil
	default:
		return RequestTypeUnknown, fmt.Errorf("invalid request_type, allowed values: unknown, sync, stream, ws_v2, cyber, live")
	}
}

func RequestTypeFromLegacy(stream bool, openAIWSMode bool) RequestType {
	if openAIWSMode {
		return RequestTypeWSV2
	}
	if stream {
		return RequestTypeStream
	}
	return RequestTypeSync
}

func ApplyLegacyRequestFields(requestType RequestType, fallbackStream bool, fallbackOpenAIWSMode bool) (stream bool, openAIWSMode bool) {
	switch requestType.Normalize() {
	case RequestTypeSync:
		return false, false
	case RequestTypeStream:
		return true, false
	case RequestTypeWSV2:
		return true, true
	default:
		return fallbackStream, fallbackOpenAIWSMode
	}
}

type UsageLog struct {
	ID        int64
	UserID    int64
	APIKeyID  int64
	AccountID int64
	RequestID string
	Model     string
	// RequestedModel is the client-requested model name recorded for stable user/admin display.
	// Empty should be treated as Model for backward compatibility with historical rows.
	RequestedModel string
	// UpstreamModel is the actual model sent to the upstream provider after mapping.
	// Nil means no mapping was applied (requested model was used as-is).
	UpstreamModel *string
	// UpstreamResponseModel 是成功上游响应原文声明的模型，仅供轻量网关管理员审计。
	UpstreamResponseModel *string
	// UpstreamModelMismatch 比较实际发往上游的模型与响应声明模型；nil 表示响应未声明。
	UpstreamModelMismatch *bool
	// ChannelID 渠道 ID
	ChannelID *int64
	// ModelMappingChain 模型映射链，如 "a→b→c"
	ModelMappingChain *string
	// 以下字段用于 gateway-lite（轻量网关）即时用量事件，不单独扩张本地日志表。
	ChannelMappedModel   string
	TargetPlatform       string
	CompositeRouteSource string
	// BillingTier 计费层级标签（per_request/image 模式）
	BillingTier *string
	// BillingMode 计费模式：token/image
	BillingMode *string
	// ServiceTier records the OpenAI service tier used for billing, e.g. "priority" / "flex".
	ServiceTier *string
	// SentServiceTier records the final tier sent upstream after all policies.
	SentServiceTier *string
	// ResponseServiceTier records the explicit tier observed from the upstream response.
	ResponseServiceTier *string
	// RequestedServiceTier records the normalized client value before the global
	// Priority policy rewrites it. It is admin-only audit evidence.
	RequestedServiceTier *string
	// PriorityPolicyMode records follow_request / key_prefer_priority / force_priority / force_standard.
	// Nil is reserved for historical rows created before the policy existed.
	PriorityPolicyMode *string
	// PriorityDetectionSource records which enabled evidence source determined
	// the final billing tier: response, request, or standard_default.
	PriorityDetectionSource *string
	// PriorityPolicyAction and PriorityPolicyRuleID preserve the exact rule
	// decision used before forwarding. Fallback is true when the rule's model
	// whitelist did not match and its fallback action was used.
	PriorityPolicyAction   *string
	PriorityPolicyRuleID   *string
	PriorityPolicyFallback bool
	// LongContextBillingApplied records that the provider-specific long-context
	// price tier actually changed this request's charge.
	LongContextBillingApplied bool
	// RequestedReasoningEffort is the explicit client value before policy or
	// protocol conversion. ReasoningEffort is the explicit value actually sent
	// upstream. Nil means unspecified / not applicable; defaults are not guessed.
	RequestedReasoningEffort *string
	ReasoningEffort          *string
	// InboundEndpoint is the client-facing API endpoint path, e.g. /v1/chat/completions.
	InboundEndpoint *string
	// UpstreamEndpoint is the normalized upstream endpoint path, e.g. /v1/responses.
	UpstreamEndpoint *string

	GroupID *int64
	// GroupID 是实际计费/调度分组；OriginalGroupID 保存请求最初进入的分组。
	// OriginalGroup 仅用于管理员日志展示分组名称，不参与计费或调度。
	OriginalGroupID       *int64
	OriginalGroup         *Group
	KeywordPolicyMatched  bool
	KeywordPolicyRerouted bool
	KeywordPolicySource   string
	KeywordPolicyReason   string
	DetectedLanguages     []string
	SubscriptionID        *int64

	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int

	CacheCreation5mTokens int `gorm:"column:cache_creation_5m_tokens"`
	CacheCreation1hTokens int `gorm:"column:cache_creation_1h_tokens"`

	ImageInputTokens      int
	ImageInputCost        float64
	ImageInputPrice       float64
	ImageInputPriceSource *string
	ImageOutputTokens     int
	ImageOutputCost       float64

	InputCost         float64
	OutputCost        float64
	CacheCreationCost float64
	CacheReadCost     float64
	TotalCost         float64
	ActualCost        float64
	RateMultiplier    float64
	// RatePolicyAudit 固化“分组默认 → 用户覆盖 → 高峰叠加”的完整证据。
	// 账号成本倍率不在其中，避免采购成本误参与客户扣款。
	RatePolicyAudit *gatewaylite.RatePolicyAudit
	// AccountRateMultiplier 账号计费倍率快照（nil 表示历史数据，按 1.0 处理）
	AccountRateMultiplier *float64
	// AccountStatsCost 账号统计定价预计算费用；nil 表示未解析出专用采购成本。
	// 若 UpstreamModel 已知，nil 按零成本处理，不能回退客户侧 total_cost。
	AccountStatsCost *float64

	BillingType  int8
	RequestType  RequestType
	Stream       bool
	OpenAIWSMode bool
	DurationMs   *int
	FirstTokenMs *int
	// FakeFirstTokenMs 伪造首字（SSE 注释行）发出时刻（毫秒）；未开启/未触发为 nil。
	FakeFirstTokenMs *int
	UserAgent        *string
	IPAddress        *string
	// SessionID 仅保存客户端显式会话头；不能由内容或缓存键推导，避免把
	// 它误当成调度标识或 request_id 幂等键。
	SessionID *string

	// Cache TTL Override 标记（管理员强制替换了缓存 TTL 计费）
	CacheTTLOverridden bool

	// 图片生成字段
	ImageCount         int
	ImageSize          *string
	ImageInputSize     *string
	ImageOutputSize    *string
	ImageSizeSource    *string
	ImageSizeBreakdown map[string]int
	MediaType          *string

	// 视频生成字段。价格按每秒单价乘以时长和数量结算。
	VideoCount           int
	VideoResolution      *string
	VideoDurationSeconds *int

	CreatedAt time.Time

	User         *User
	APIKey       *APIKey
	Account      *Account
	Group        *Group
	Subscription *UserSubscription
}

func (u *UsageLog) TotalTokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

func (u *UsageLog) EffectiveRequestType() RequestType {
	if u == nil {
		return RequestTypeUnknown
	}
	if normalized := u.RequestType.Normalize(); normalized != RequestTypeUnknown {
		return normalized
	}
	return RequestTypeFromLegacy(u.Stream, u.OpenAIWSMode)
}

func (u *UsageLog) SyncRequestTypeAndLegacyFields() {
	if u == nil {
		return
	}
	requestType := u.EffectiveRequestType()
	u.RequestType = requestType
	u.Stream, u.OpenAIWSMode = ApplyLegacyRequestFields(requestType, u.Stream, u.OpenAIWSMode)
}
