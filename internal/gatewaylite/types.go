package gatewaylite

import "time"

// KeySnapshot is the hot authentication shape a regional gateway needs from
// the control plane. It intentionally excludes user profile and billing UI data.
type KeySnapshot struct {
	KeyID  string `json:"key_id"`
	UserID int64  `json:"user_id"`
	// UserID 保留真实员工身份，付款和企业聚合限制使用独立主体。
	OrganizationID     int64  `json:"organization_id,omitempty"`
	OrganizationName   string `json:"organization_name,omitempty"`
	OrganizationRole   string `json:"organization_role,omitempty"`
	BillingSubjectType string `json:"billing_subject_type,omitempty"`
	BillingSubjectID   int64  `json:"billing_subject_id,omitempty"`
	UserEmail          string `json:"user_email,omitempty"`
	Username           string `json:"username,omitempty"`
	SecretHash         string `json:"secret_hash"`
	Status             string `json:"status"`
	Tier               int    `json:"tier"`
	Platform           string `json:"platform"`
	GroupID            int64  `json:"group_id"`
	GroupName          string `json:"group_name"`
	// UserRateMultiplier 只对应当前固定分组；nil 表示使用本地分组默认倍率。
	UserRateMultiplier *float64 `json:"user_rate_multiplier,omitempty"`
	// UserGroupRateMultipliers 以本地分组编号为键，供同节点关键词重路由精确取值。
	UserGroupRateMultipliers        map[int64]float64 `json:"user_group_rate_multipliers,omitempty"`
	IPWhitelist                     []string          `json:"ip_whitelist,omitempty"`
	IPBlacklist                     []string          `json:"ip_blacklist,omitempty"`
	AllowedModels                   []string          `json:"allowed_models,omitempty"`
	AllowedKeywordRouteGroupIDs     []int64           `json:"allowed_keyword_route_group_ids,omitempty"`
	KeywordRouteGroupAccessEnforced bool              `json:"keyword_route_group_access_enforced,omitempty"`
	RateLimitRPM                    int               `json:"rate_limit_rpm"`
	RateLimitTPM                    int               `json:"rate_limit_tpm"`
	Concurrency                     int               `json:"concurrency"`
	OrganizationConcurrency         int               `json:"organization_concurrency,omitempty"`
	OrganizationRateLimitRPM        int               `json:"organization_rate_limit_rpm,omitempty"`
	OrganizationRateLimitTPM        int               `json:"organization_rate_limit_tpm,omitempty"`
	MemberConcurrency               int               `json:"member_concurrency,omitempty"`
	MemberRateLimitRPM              int               `json:"member_rate_limit_rpm,omitempty"`
	MemberRateLimitTPM              int               `json:"member_rate_limit_tpm,omitempty"`
	MemberBudgetCents               int64             `json:"member_budget_cents,omitempty"`
	MemberBudgetUsedCents           int64             `json:"member_budget_used_cents,omitempty"`
	// Key* 是 API Key（接口密钥）自己的硬限制，必须保留独立维度，不能预先
	// 合并进成员限制，否则同一成员的多个 Key 会错误共享一个 Key 配额桶。
	KeyConcurrency  int   `json:"key_concurrency,omitempty"`
	KeyRateLimitRPM int   `json:"key_rate_limit_rpm,omitempty"`
	KeyRateLimitTPM int   `json:"key_rate_limit_tpm,omitempty"`
	Version         int64 `json:"version"`
	ExpiresAt       int64 `json:"expires_at,omitempty"`
	// TokenUse 精确为 agent_api_key 时，应用与授权状态必须逐次回源核验，不能正缓存。
	TokenUse                string                `json:"token_use,omitempty"`
	CacheTTLSecond          int                   `json:"cache_ttl_seconds"`
	Group                   *GatewayGroupSnapshot `json:"group,omitempty"`
	DefaultGateway          *GatewayRouteSummary  `json:"default_gateway,omitempty"`
	AvailableGateways       []GatewayRouteSummary `json:"available_gateways,omitempty"`
	GatewayAccessEnforced   bool                  `json:"gateway_access_enforced,omitempty"`
	GatewayStrategy         string                `json:"gateway_strategy,omitempty"`
	KeyDefaultGatewayNodeID int64                 `json:"key_default_gateway_node_id,omitempty"`
}

func (s KeySnapshot) Active() bool {
	return s.Status == "" || s.Status == "active"
}

// LeaseSnapshot is a regional spend lease. The regional Redis copy is allowed
// to make local reserve/commit decisions within this allocated budget.
type LeaseSnapshot struct {
	LeaseID            string `json:"lease_id"`
	UserID             int64  `json:"user_id"`
	OrganizationID     int64  `json:"organization_id,omitempty"`
	BillingSubjectType string `json:"billing_subject_type,omitempty"`
	BillingSubjectID   int64  `json:"billing_subject_id,omitempty"`
	Region             string `json:"region"`
	AllocatedCents     int64  `json:"allocated_cents"`
	ReservedCents      int64  `json:"reserved_cents"`
	SpentCents         int64  `json:"spent_cents"`
	Version            int64  `json:"version"`
	ExpiresAt          int64  `json:"expires_at"`
	TargetCents        int64  `json:"target_cents,omitempty"`
	RefillAtCents      int64  `json:"refill_at_cents,omitempty"`
}

// LeaseSubject identifies both the actor and the wallet that owns a regional
// lease. Personal users deliberately keep OrganizationID=0 so their Redis key
// remains byte-for-byte compatible with earlier gateway versions.
type LeaseSubject struct {
	UserID             int64
	OrganizationID     int64
	BillingSubjectType string
	BillingSubjectID   int64
}

func (l LeaseSnapshot) Subject() LeaseSubject {
	return LeaseSubject{
		UserID:             l.UserID,
		OrganizationID:     l.OrganizationID,
		BillingSubjectType: l.BillingSubjectType,
		BillingSubjectID:   l.BillingSubjectID,
	}
}

func (s KeySnapshot) LeaseSubject() LeaseSubject {
	return LeaseSubject{
		UserID:             s.UserID,
		OrganizationID:     s.OrganizationID,
		BillingSubjectType: s.BillingSubjectType,
		BillingSubjectID:   s.BillingSubjectID,
	}
}

// LeaseObservation carries the gateway's Redis-authoritative view back to the
// control plane. Local spent/reserved counters can be newer than the durable
// usage report, so the control plane must not treat its older snapshot as proof
// that the gateway still has usable capacity.
type LeaseObservation struct {
	ObservedLeaseID        string `json:"observed_lease_id,omitempty"`
	ObservedVersion        int64  `json:"observed_version,omitempty"`
	ObservedAllocatedCents int64  `json:"observed_allocated_cents,omitempty"`
	ObservedReservedCents  int64  `json:"observed_reserved_cents,omitempty"`
	ObservedSpentCents     int64  `json:"observed_spent_cents,omitempty"`
	ObservedExpiresAt      int64  `json:"observed_expires_at,omitempty"`
	LocalExhausted         bool   `json:"local_exhausted,omitempty"`
	LowWatermark           bool   `json:"low_watermark,omitempty"`
}

func (l LeaseSnapshot) Observation(localExhausted bool) LeaseObservation {
	return LeaseObservation{
		ObservedLeaseID:        l.LeaseID,
		ObservedVersion:        l.Version,
		ObservedAllocatedCents: l.AllocatedCents,
		ObservedReservedCents:  l.ReservedCents,
		ObservedSpentCents:     l.SpentCents,
		ObservedExpiresAt:      l.ExpiresAt,
		LocalExhausted:         localExhausted,
	}
}

type GatewayRouteSummary struct {
	ID           int64  `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Region       string `json:"region"`
	Country      string `json:"country,omitempty"`
	City         string `json:"city,omitempty"`
	BaseURL      string `json:"base_url"`
	PublicURL    string `json:"public_url"`
	Format       string `json:"format"`
	RequiredTier int    `json:"required_tier"`
	LatencyMS    int    `json:"latency_ms"`
	HealthStatus string `json:"health_status"`
	OnlineUsers  int    `json:"online_users"`
	IsDefault    bool   `json:"is_default"`
}

func (l LeaseSnapshot) AvailableCents() int64 {
	return l.AllocatedCents - l.ReservedCents - l.SpentCents
}

func (l LeaseSnapshot) Expired(now time.Time) bool {
	return l.ExpiresAt > 0 && now.Unix() >= l.ExpiresAt
}

type ResolveKeyRequest struct {
	KeyID          string `json:"key_id"`
	Region         string `json:"region"`
	GatewayCode    string `json:"gateway_code,omitempty"`
	BillingExempt  bool   `json:"billing_exempt,omitempty"`
	SkipUsageTouch bool   `json:"skip_usage_touch,omitempty"`
}

type ResolveKeyResponse struct {
	OK    bool        `json:"ok"`
	Key   KeySnapshot `json:"key"`
	Error string      `json:"error,omitempty"`
}

type AcquireLeaseRequest struct {
	KeyID              string `json:"key_id,omitempty"`
	UserID             int64  `json:"user_id"`
	OrganizationID     int64  `json:"organization_id,omitempty"`
	BillingSubjectType string `json:"billing_subject_type,omitempty"`
	BillingSubjectID   int64  `json:"billing_subject_id,omitempty"`
	Region             string `json:"region"`
	GatewayCode        string `json:"gateway_code,omitempty"`
	EstimatedCostCents int64  `json:"estimated_cost_cents"`
	Reason             string `json:"reason,omitempty"`
	LeaseObservation
}

type AcquireLeaseResponse struct {
	OK          bool          `json:"ok"`
	Lease       LeaseSnapshot `json:"lease"`
	Error       string        `json:"error,omitempty"`
	ErrorDetail string        `json:"error_detail,omitempty"`
}

type RebalanceLeaseRequest struct {
	KeyID              string `json:"key_id,omitempty"`
	UserID             int64  `json:"user_id"`
	OrganizationID     int64  `json:"organization_id,omitempty"`
	BillingSubjectType string `json:"billing_subject_type,omitempty"`
	BillingSubjectID   int64  `json:"billing_subject_id,omitempty"`
	Region             string `json:"region"`
	GatewayCode        string `json:"gateway_code,omitempty"`
	EstimatedCostCents int64  `json:"estimated_cost_cents"`
	Reason             string `json:"reason,omitempty"`
	LeaseObservation
}

type RebalanceLeaseResponse struct {
	OK               bool          `json:"ok"`
	Lease            LeaseSnapshot `json:"lease,omitempty"`
	TransferredCents int64         `json:"transferred_cents,omitempty"`
	ReleasedRegions  []string      `json:"released_regions,omitempty"`
	Error            string        `json:"error,omitempty"`
	ErrorDetail      string        `json:"error_detail,omitempty"`
}

type UsageEvent struct {
	// Funding fields use the main site's flattened FundedUsageEvent contract.
	FundingAuthorizationID     string                      `json:"funding_authorization_id,omitempty"`
	FundingRevision            int64                       `json:"funding_revision,omitempty"`
	SettlementRevision         int64                       `json:"settlement_revision,omitempty"`
	ActualMicroCents           *int64                      `json:"actual_micro_cents,omitempty"`
	Final                      bool                        `json:"final,omitempty"`
	FundingSnapshot            *FundingAuthorization       `json:"funding_snapshot,omitempty"`
	OAuthInvocationReservation *OAuthInvocationReservation `json:"oauth_invocation_reservation,omitempty"`
	RequestID                  string                      `json:"request_id"`
	UserID                     int64                       `json:"user_id"`
	OrganizationID             int64                       `json:"organization_id,omitempty"`
	BillingSubjectType         string                      `json:"billing_subject_type,omitempty"`
	BillingSubjectID           int64                       `json:"billing_subject_id,omitempty"`
	KeyID                      string                      `json:"key_id"`
	SessionID                  string                      `json:"session_id,omitempty"`
	LeaseID                    string                      `json:"lease_id,omitempty"`
	Region                     string                      `json:"region"`
	GatewayID                  string                      `json:"gateway_id"`
	UpstreamID                 string                      `json:"upstream_id,omitempty"`
	GatewayGroupID             int64                       `json:"gateway_group_id,omitempty"`
	GatewayGroupName           string                      `json:"gateway_group_name,omitempty"`
	RequestedGatewayGroupID    int64                       `json:"requested_gateway_group_id,omitempty"`
	RequestedGatewayGroupName  string                      `json:"requested_gateway_group_name,omitempty"`
	KeywordPolicyMatched       bool                        `json:"keyword_policy_matched,omitempty"`
	KeywordPolicyRerouted      bool                        `json:"keyword_policy_rerouted,omitempty"`
	KeywordPolicySource        string                      `json:"keyword_policy_source,omitempty"`
	KeywordPolicyReason        string                      `json:"keyword_policy_reason,omitempty"`
	DetectedLanguages          []string                    `json:"detected_languages,omitempty"`
	Protocol                   string                      `json:"protocol"`
	Model                      string                      `json:"model"`
	RequestedModel             string                      `json:"requested_model,omitempty"`
	ChannelMappedModel         string                      `json:"channel_mapped_model,omitempty"`
	UpstreamModel              string                      `json:"upstream_model,omitempty"`
	TargetPlatform             string                      `json:"target_platform,omitempty"`
	RouteSource                string                      `json:"route_source,omitempty"`
	ModelMappingChain          string                      `json:"model_mapping_chain,omitempty"`
	Method                     string                      `json:"method"`
	Path                       string                      `json:"path"`
	Status                     int                         `json:"status"`
	ErrorMessage               string                      `json:"error_message,omitempty"`
	TokensIn                   int64                       `json:"tokens_in"`
	TokensOut                  int64                       `json:"tokens_out"`
	CacheReadTokens            int64                       `json:"cache_read_tokens"`
	CacheWriteTokens           int64                       `json:"cache_write_tokens"`
	RequestedReasoningEffort   string                      `json:"requested_reasoning_effort,omitempty"`
	ReasoningEffort            string                      `json:"reasoning_effort,omitempty"`
	ServiceTier                string                      `json:"service_tier,omitempty"`
	SentServiceTier            string                      `json:"sent_service_tier,omitempty"`
	ResponseServiceTier        string                      `json:"response_service_tier,omitempty"`
	RequestedServiceTier       string                      `json:"requested_service_tier,omitempty"`
	PriorityPolicyMode         string                      `json:"priority_policy_mode,omitempty"`
	PriorityDetectionSource    string                      `json:"priority_detection_source,omitempty"`
	PriorityPolicyAction       string                      `json:"priority_policy_action,omitempty"`
	PriorityPolicyRuleID       string                      `json:"priority_policy_rule_id,omitempty"`
	PriorityPolicyFallback     bool                        `json:"priority_policy_fallback,omitempty"`
	LongContextBillingApplied  bool                        `json:"long_context_billing_applied,omitempty"`
	BillingMode                string                      `json:"billing_mode,omitempty"`
	BillingTier                string                      `json:"billing_tier,omitempty"`
	SettlementMode             string                      `json:"settlement_mode,omitempty"`
	// The following settlement fields are gateway-local inputs. They are omitted
	// from the control-plane payload after Redis has frozen ActualCents. A
	// WebSocket connection has one quota request but several resp_* settlement
	// requests, so those identities must remain separate.
	SettlementMicroCents       int64            `json:"-"`
	SettlementPending          bool             `json:"-"`
	QuotaRequestID             string           `json:"-"`
	SuppressBaseCharge         bool             `json:"-"`
	CommitAsyncReservation     bool             `json:"-"`
	AsyncReservationMicroCents int64            `json:"-"`
	PolicyRevision             int64            `json:"policy_revision,omitempty"`
	PolicyChecksum             string           `json:"policy_checksum,omitempty"`
	PriceVersion               int64            `json:"price_version,omitempty"`
	ImageCount                 int64            `json:"image_count,omitempty"`
	ImageSize                  string           `json:"image_size,omitempty"`
	ImageInputTokens           int64            `json:"image_input_tokens,omitempty"`
	ImageInputCost             float64          `json:"image_input_cost,omitempty"`
	ImageInputPrice            float64          `json:"image_input_price,omitempty"`
	ImageInputPriceSource      string           `json:"image_input_price_source,omitempty"`
	ImageOutputTokens          int64            `json:"image_output_tokens,omitempty"`
	ImageOutputCost            float64          `json:"image_output_cost,omitempty"`
	VideoCount                 int64            `json:"video_count,omitempty"`
	VideoResolution            string           `json:"video_resolution,omitempty"`
	VideoDurationSeconds       int64            `json:"video_duration_seconds,omitempty"`
	EstimatedCents             int64            `json:"estimated_cents"`
	ActualCents                int64            `json:"actual_cents"`
	RateMultiplier             float64          `json:"rate_multiplier,omitempty"`
	RatePolicyAudit            *RatePolicyAudit `json:"rate_policy_audit,omitempty"`
	// AccountRateMultiplier is optional because only the post-routing correction
	// knows the real upstream account. A non-nil 0 is a valid free procurement
	// multiplier and must remain distinguishable from an omitted legacy value.
	AccountRateMultiplier *float64 `json:"account_rate_multiplier,omitempty"`
	// AccountStatsCost is the raw procurement cost resolved for the final
	// upstream model, before applying AccountRateMultiplier. A nil value means
	// no cost evidence was available; an explicit zero is authoritative.
	AccountStatsCost       *float64                 `json:"account_stats_cost,omitempty"`
	LatencyMillis          int64                    `json:"latency_millis"`
	FirstTokenMillis       int64                    `json:"first_token_millis"`
	AuthLatencyMillis      int64                    `json:"auth_latency_millis,omitempty"`
	QuotaLatencyMillis     int64                    `json:"quota_latency_millis,omitempty"`
	RoutingLatencyMillis   int64                    `json:"routing_latency_millis,omitempty"`
	RoutingBreakdownMillis *RoutingLatencyBreakdown `json:"routing_breakdown_millis,omitempty"`
	UpstreamLatencyMillis  int64                    `json:"upstream_latency_millis,omitempty"`
	ResponseLatencyMillis  int64                    `json:"response_latency_millis,omitempty"`
	ReportLatencyMillis    int64                    `json:"report_latency_millis,omitempty"`
	StartedAtMillis        int64                    `json:"started_at_millis"`
	EndedAtMillis          int64                    `json:"ended_at_millis"`
}

// RoutingLatencyBreakdown explains the broad routing timer without changing
// dispatch semantics. Stages are sequential; Other is the non-negative
// remainder after subtracting explicitly measured stages from the total.
type RoutingLatencyBreakdown struct {
	Version                  int   `json:"version"`
	UserSlot                 int64 `json:"user_slot"`
	BillingRecheck           int64 `json:"billing_recheck"`
	SessionPolicy            int64 `json:"session_policy"`
	AccountSelection         int64 `json:"account_selection"`
	PostSelectionAccountSlot int64 `json:"post_selection_account_slot"`
	SelectionGroupValidation int64 `json:"selection_group_validation"`
	SelectionStickyLookup    int64 `json:"selection_sticky_lookup"`
	SelectionAccountResolve  int64 `json:"selection_account_resolve"`
	SelectionAccountSlot     int64 `json:"selection_account_slot"`
	SelectionStickyRefresh   int64 `json:"selection_sticky_refresh"`
	SelectionCandidates      int64 `json:"selection_candidates"`
	SelectionSettings        int64 `json:"selection_settings"`
	SelectionOther           int64 `json:"selection_other"`
	Other                    int64 `json:"other"`
}

type UsageBatchReportRequest struct {
	Events []UsageEvent `json:"events"`
}

type UsageBatchReportResponse struct {
	OK            bool   `json:"ok"`
	AcceptedCount int    `json:"accepted_count"`
	AppliedCount  int    `json:"applied_count"`
	Error         string `json:"error,omitempty"`
}

type ContentModerationPenaltyRequest struct {
	RequestID                       string `json:"request_id"`
	UserID                          int64  `json:"user_id"`
	OrganizationID                  int64  `json:"organization_id,omitempty"`
	BillingSubjectType              string `json:"billing_subject_type,omitempty"`
	BillingSubjectID                int64  `json:"billing_subject_id,omitempty"`
	KeyID                           string `json:"key_id,omitempty"`
	Region                          string `json:"region,omitempty"`
	GatewayID                       string `json:"gateway_id,omitempty"`
	Action                          string `json:"action"`
	Category                        string `json:"category,omitempty"`
	DeductBalanceCents              int64  `json:"deduct_balance_cents,omitempty"`
	PermanentBanEnabled             bool   `json:"permanent_ban_enabled,omitempty"`
	PermanentBanThreshold           int    `json:"permanent_ban_threshold,omitempty"`
	ViolationWindowHours            int    `json:"violation_window_hours,omitempty"`
	OrganizationSuspensionEnabled   bool   `json:"organization_suspension_enabled,omitempty"`
	OrganizationSuspensionThreshold int    `json:"organization_suspension_threshold,omitempty"`
	Reason                          string `json:"reason,omitempty"`
	CreatedAtMillis                 int64  `json:"created_at_millis,omitempty"`
}

type ContentModerationPenaltyResponse struct {
	OK                    bool   `json:"ok"`
	UserID                int64  `json:"user_id,omitempty"`
	OrganizationID        int64  `json:"organization_id,omitempty"`
	BillingSubjectType    string `json:"billing_subject_type,omitempty"`
	BillingSubjectID      int64  `json:"billing_subject_id,omitempty"`
	BlockedCount          int    `json:"blocked_count,omitempty"`
	DeductedBalanceCents  int64  `json:"deducted_balance_cents,omitempty"`
	PermanentlyBanned     bool   `json:"permanently_banned,omitempty"`
	OrganizationSuspended bool   `json:"organization_suspended,omitempty"`
	Skipped               bool   `json:"skipped,omitempty"`
	Duplicate             bool   `json:"duplicate,omitempty"`
	Error                 string `json:"error,omitempty"`
}

type GatewayHealthReportRequest struct {
	GatewayNodeID          int64               `json:"gateway_node_id,omitempty"`
	GatewayCode            string              `json:"gateway_code,omitempty"`
	Region                 string              `json:"region,omitempty"`
	HealthStatus           string              `json:"health_status"`
	LatencyMS              int                 `json:"latency_ms,omitempty"`
	OnlineUsers            int                 `json:"online_users,omitempty"`
	Message                string              `json:"message,omitempty"`
	UsageQueueStatus       string              `json:"usage_queue_status,omitempty"`
	UsageQueueStreamLength int64               `json:"usage_queue_stream_length,omitempty"`
	UsageQueuePendingCount int64               `json:"usage_queue_pending_count,omitempty"`
	UsageQueueDeadCount    int64               `json:"usage_queue_dead_count,omitempty"`
	AvailableModels        []string            `json:"available_models,omitempty"`
	ModelPrices            []GatewayModelPrice `json:"model_prices,omitempty"`
	Metadata               map[string]any      `json:"metadata,omitempty"`
}

type GatewayModelPrice struct {
	GatewayNodeID int64  `json:"gateway_node_id,omitempty"`
	GatewayCode   string `json:"gateway_code,omitempty"`
	Model         string `json:"model"`
	Provider      string `json:"provider,omitempty"`
	// ModelVendor 不复用 Provider；转发渠道不能证明模型的真实厂商。
	ModelVendor     string   `json:"model_vendor,omitempty"`
	InputModalities []string `json:"input_modalities,omitempty"`
	MaxInputTokens  int      `json:"max_input_tokens,omitempty"`
	MetadataSource  string   `json:"metadata_source,omitempty"`
	// nil 表示旧合同未知；数组（含空集）为权威清单，清单内省略值表示显式零价。
	PriceFields                                   []string                    `json:"price_fields"`
	Mode                                          string                      `json:"mode,omitempty"`
	BillingMode                                   string                      `json:"billing_mode,omitempty"`
	InputCostPer1MTokens                          float64                     `json:"input_cost_per_1m_tokens"`
	InputCostPer1MTokensPriority                  float64                     `json:"input_cost_per_1m_tokens_priority,omitempty"`
	OutputCostPer1MTokens                         float64                     `json:"output_cost_per_1m_tokens"`
	OutputCostPer1MTokensPriority                 float64                     `json:"output_cost_per_1m_tokens_priority,omitempty"`
	CacheReadCostPer1MTokens                      float64                     `json:"cache_read_cost_per_1m_tokens"`
	CacheReadCostPer1MTokensPriority              float64                     `json:"cache_read_cost_per_1m_tokens_priority,omitempty"`
	CacheCreationCostPer1MTokens                  float64                     `json:"cache_creation_cost_per_1m_tokens"`
	CacheCreationCostPer1MTokensPriority          float64                     `json:"cache_creation_cost_per_1m_tokens_priority,omitempty"`
	InputCostPer1MTokensAbove272K                 float64                     `json:"input_cost_per_1m_tokens_above_272k,omitempty"`
	InputCostPer1MTokensPriorityAbove272K         float64                     `json:"input_cost_per_1m_tokens_priority_above_272k,omitempty"`
	OutputCostPer1MTokensAbove272K                float64                     `json:"output_cost_per_1m_tokens_above_272k,omitempty"`
	OutputCostPer1MTokensPriorityAbove272K        float64                     `json:"output_cost_per_1m_tokens_priority_above_272k,omitempty"`
	CacheReadCostPer1MTokensAbove272K             float64                     `json:"cache_read_cost_per_1m_tokens_above_272k,omitempty"`
	CacheReadCostPer1MTokensPriorityAbove272K     float64                     `json:"cache_read_cost_per_1m_tokens_priority_above_272k,omitempty"`
	CacheCreationCostPer1MTokensAbove272K         float64                     `json:"cache_creation_cost_per_1m_tokens_above_272k,omitempty"`
	CacheCreationCostPer1MTokensPriorityAbove272K float64                     `json:"cache_creation_cost_per_1m_tokens_priority_above_272k,omitempty"`
	LongContextInputTokenThreshold                int                         `json:"long_context_input_token_threshold,omitempty"`
	InputCostPerImageToken                        *float64                    `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImage                            float64                     `json:"output_cost_per_image,omitempty"`
	OutputCostPerImageToken                       float64                     `json:"output_cost_per_image_token,omitempty"`
	PerRequestPrice                               float64                     `json:"per_request_price,omitempty"`
	VideoPricePerSecond480P                       float64                     `json:"video_price_per_second_480p,omitempty"`
	VideoPricePerSecond720P                       float64                     `json:"video_price_per_second_720p,omitempty"`
	VideoPricePerSecond1080P                      float64                     `json:"video_price_per_second_1080p,omitempty"`
	SupportsPromptCaching                         bool                        `json:"supports_prompt_caching,omitempty"`
	SupportsServiceTier                           bool                        `json:"supports_service_tier,omitempty"`
	Disabled                                      bool                        `json:"disabled,omitempty"`
	Intervals                                     []GatewayModelPriceInterval `json:"intervals,omitempty"`
	Currency                                      string                      `json:"currency"`
	Source                                        string                      `json:"source,omitempty"`
	UpdatedAtMillis                               int64                       `json:"updated_at_millis,omitempty"`
}

type GatewayModelPriceInterval struct {
	TierLabel                 string   `json:"tier_label,omitempty"`
	MinTokens                 int      `json:"min_tokens"`
	MaxTokens                 *int     `json:"max_tokens,omitempty"`
	InputCostPer1MTokens      *float64 `json:"input_cost_per_1m_tokens,omitempty"`
	OutputCostPer1MTokens     *float64 `json:"output_cost_per_1m_tokens,omitempty"`
	CacheReadCostPer1MTokens  *float64 `json:"cache_read_cost_per_1m_tokens,omitempty"`
	CacheWriteCostPer1MTokens *float64 `json:"cache_creation_cost_per_1m_tokens,omitempty"`
	OutputCostPerImage        *float64 `json:"output_cost_per_image,omitempty"`
	PerRequestPrice           *float64 `json:"per_request_price,omitempty"`
	SortOrder                 int      `json:"sort_order,omitempty"`
}

type GatewayHealthReportResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type GatewayConfigChangedRequest struct {
	EventID         string `json:"event_id,omitempty"`
	GatewayCode     string `json:"gateway_code,omitempty"`
	Region          string `json:"region,omitempty"`
	Resource        string `json:"resource,omitempty"`
	Action          string `json:"action,omitempty"`
	ResourceID      int64  `json:"resource_id,omitempty"`
	ChangedAtMillis int64  `json:"changed_at_millis,omitempty"`
}

type GatewayConfigChangedResponse struct {
	OK            bool           `json:"ok"`
	GatewayNodeID int64          `json:"gateway_node_id,omitempty"`
	GatewayCode   string         `json:"gateway_code,omitempty"`
	Region        string         `json:"region,omitempty"`
	Sync          map[string]any `json:"sync,omitempty"`
	Error         string         `json:"error,omitempty"`
}

type GatewayConfigPushRequest struct {
	EventID               string                   `json:"event_id,omitempty"`
	GatewayCode           string                   `json:"gateway_code,omitempty"`
	Region                string                   `json:"region,omitempty"`
	Resource              string                   `json:"resource,omitempty"`
	Action                string                   `json:"action,omitempty"`
	ResourceID            int64                    `json:"resource_id,omitempty"`
	ChangedAtMillis       int64                    `json:"changed_at_millis,omitempty"`
	Accounts              []GatewayAccountSnapshot `json:"accounts,omitempty"`
	AccountFull           bool                     `json:"accounts_full,omitempty"`
	Groups                []GatewayGroupSnapshot   `json:"groups,omitempty"`
	GroupsFull            bool                     `json:"groups_full,omitempty"`
	ModelPrices           []GatewayModelPrice      `json:"model_prices,omitempty"`
	ModelPricesFull       bool                     `json:"model_prices_full,omitempty"`
	EffectivePolicies     []GatewayEffectivePolicy `json:"effective_policies,omitempty"`
	EffectivePoliciesFull bool                     `json:"effective_policies_full,omitempty"`
	RuntimeSettings       *GatewayRuntimeSettings  `json:"runtime_settings,omitempty"`
	SnapshotChecksum      string                   `json:"snapshot_checksum,omitempty"`
	RefreshOnly           bool                     `json:"refresh_only,omitempty"`
}

type GatewayConfigPushResponse struct {
	OK                       bool           `json:"ok"`
	GatewayNodeID            int64          `json:"gateway_node_id,omitempty"`
	GatewayCode              string         `json:"gateway_code,omitempty"`
	Region                   string         `json:"region,omitempty"`
	Sync                     map[string]any `json:"sync,omitempty"`
	FullSnapshotRequired     bool           `json:"full_snapshot_required,omitempty"`
	SnapshotChecksumAccepted bool           `json:"snapshot_checksum_accepted,omitempty"`
	Error                    string         `json:"error,omitempty"`
}

type GatewayRuntimeSettings struct {
	OpenAIFastPolicySettings *OpenAIFastPolicyRuntimeSettings `json:"openai_fast_policy_settings,omitempty"`
}

type OpenAIFastPolicyRuntimeSettings struct {
	Version                            int   `json:"version"`
	PriorityDetectionByRequestEnabled  bool  `json:"priority_detection_by_request_enabled"`
	PriorityDetectionByResponseEnabled bool  `json:"priority_detection_by_response_enabled"`
	RuleCount                          int   `json:"rule_count"`
	ReportedAtMillis                   int64 `json:"reported_at_millis,omitempty"`
}

type GatewayConfigSnapshotRequest struct {
	GatewayCode  string `json:"gateway_code,omitempty"`
	Region       string `json:"region"`
	SinceVersion int64  `json:"since_version,omitempty"`
}

type GatewayConfigSnapshotResponse struct {
	OK       bool                  `json:"ok"`
	Snapshot GatewayConfigSnapshot `json:"snapshot"`
	Error    string                `json:"error,omitempty"`
}

type GatewayConfigSnapshot struct {
	Version              int64                         `json:"version"`
	GeneratedAtMillis    int64                         `json:"generated_at_millis"`
	Accounts             []GatewayAccountSnapshot      `json:"accounts"`
	Groups               []GatewayGroupSnapshot        `json:"groups"`
	GatewayNodes         []GatewayNodeSnapshot         `json:"gateway_nodes,omitempty"`
	GatewayCategories    []GatewayCategorySnapshot     `json:"gateway_categories,omitempty"`
	GatewayCategoryNodes []GatewayCategoryNodeSnapshot `json:"gateway_category_nodes,omitempty"`
	PlanGatewayRules     []PlanGatewayRuleSnapshot     `json:"plan_gateway_rules,omitempty"`
	GatewayUnlockRules   []GatewayUnlockRuleSnapshot   `json:"gateway_unlock_rules,omitempty"`
}

type GatewayNodeSnapshot struct {
	ID           int64          `json:"id"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	Region       string         `json:"region"`
	Format       string         `json:"format"`
	RequiredTier int            `json:"required_tier"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type GatewayCategorySnapshot struct {
	ID              int64  `json:"id"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Status          string `json:"status"`
	SortOrder       int    `json:"sort_order"`
	Version         int64  `json:"version"`
	CreatedAtMillis int64  `json:"created_at_millis,omitempty"`
	UpdatedAtMillis int64  `json:"updated_at_millis,omitempty"`
}

type GatewayCategoryNodeSnapshot struct {
	CategoryID    int64 `json:"category_id"`
	GatewayNodeID int64 `json:"gateway_node_id"`
	Priority      int   `json:"priority"`
	Weight        int   `json:"weight"`
}

type PlanGatewayRuleSnapshot struct {
	ID              int64          `json:"id"`
	PlanID          int64          `json:"plan_id"`
	CategoryID      *int64         `json:"category_id,omitempty"`
	GatewayNodeID   *int64         `json:"gateway_node_id,omitempty"`
	GatewayGroupID  *int64         `json:"gateway_group_id,omitempty"`
	Allowed         *bool          `json:"allowed,omitempty"`
	Priority        int            `json:"priority"`
	RuleConfig      map[string]any `json:"rule_config,omitempty"`
	Version         int64          `json:"version"`
	CreatedAtMillis int64          `json:"created_at_millis,omitempty"`
	UpdatedAtMillis int64          `json:"updated_at_millis,omitempty"`
}

type GatewayUnlockRuleSnapshot struct {
	ID                   int64  `json:"id"`
	Code                 string `json:"code"`
	Tier                 int    `json:"tier"`
	Name                 string `json:"name"`
	Status               string `json:"status"`
	MinTotalTopupCents   int64  `json:"min_total_topup_cents"`
	RequireEmailVerified bool   `json:"require_email_verified"`
	RequiredPlanRank     int    `json:"required_plan_rank"`
	Description          string `json:"description,omitempty"`
	Version              int64  `json:"version"`
	CreatedAtMillis      int64  `json:"created_at_millis,omitempty"`
	UpdatedAtMillis      int64  `json:"updated_at_millis,omitempty"`
}

type CacheInvalidationRequest struct {
	GatewayCode string  `json:"gateway_code,omitempty"`
	Region      string  `json:"region,omitempty"`
	SinceID     int64   `json:"since_id,omitempty"`
	Limit       int     `json:"limit,omitempty"`
	AckIDs      []int64 `json:"ack_ids,omitempty"`
}

type CacheInvalidationResponse struct {
	OK       bool                     `json:"ok"`
	Events   []CacheInvalidationEvent `json:"events,omitempty"`
	LatestID int64                    `json:"latest_id,omitempty"`
	Error    string                   `json:"error,omitempty"`
}

type CacheInvalidationEvent struct {
	ID                 int64          `json:"id"`
	Scope              string         `json:"scope"`
	Reason             string         `json:"reason"`
	GatewayCode        string         `json:"gateway_code,omitempty"`
	Region             string         `json:"region,omitempty"`
	ConfigVersion      int64          `json:"config_version,omitempty"`
	Payload            map[string]any `json:"payload,omitempty"`
	CreatedAtMillis    int64          `json:"created_at_millis,omitempty"`
	AcknowledgedMillis int64          `json:"acknowledged_at_millis,omitempty"`
}

type ControlPlaneEventPushRequest struct {
	DeliveryID int64                  `json:"delivery_id"`
	Event      CacheInvalidationEvent `json:"event"`
}

type ControlPlaneEventPushResponse struct {
	OK               bool   `json:"ok"`
	Acknowledged     bool   `json:"acknowledged"`
	AlreadyProcessed bool   `json:"already_processed,omitempty"`
	DeliveryID       int64  `json:"delivery_id,omitempty"`
	EventID          int64  `json:"event_id,omitempty"`
	Error            string `json:"error,omitempty"`
}

type GatewayAccountSnapshot struct {
	ID                int64           `json:"id"`
	Name              string          `json:"name"`
	Platform          string          `json:"platform"`
	Type              string          `json:"type"`
	Status            string          `json:"status"`
	Schedulable       bool            `json:"schedulable"`
	Concurrency       int             `json:"concurrency"`
	Priority          int             `json:"priority"`
	RateMultiplier    *float64        `json:"rate_multiplier,omitempty"`
	LoadFactor        *int            `json:"load_factor,omitempty"`
	Credentials       map[string]any  `json:"credentials,omitempty"`
	CredentialsStatus map[string]bool `json:"credentials_status,omitempty"`
	Extra             map[string]any  `json:"extra,omitempty"`
	GroupIDs          []int64         `json:"group_ids,omitempty"`
	// GroupPriorities carries account_groups.priority keyed by local group ID.
	// The binding priority is independent from the account-wide Priority and is
	// required by strict per-group scheduling.
	GroupPriorities map[int64]int `json:"group_priorities,omitempty"`
	// GroupModelMappings carries the account_groups model mappings keyed by
	// local group ID. It is part of the scheduler snapshot because candidate
	// validation and forwarding must see the same binding-specific route.
	GroupModelMappings map[int64]map[string]string `json:"group_model_mappings,omitempty"`
	Runtime            map[string]any              `json:"runtime,omitempty"`
	Version            int64                       `json:"version"`
	UpdatedAtMillis    int64                       `json:"updated_at_millis"`
}

// ReasoningEffortMapping is the transport-safe form of one exact reasoning
// effort rewrite. It deliberately lives in gatewaylite so snapshot DTOs do not
// depend on the request-path service package.
type ReasoningEffortMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// WeightedModelTarget 镜像 service 层的智能分流目标；独立定义避免
// gatewaylite 传输类型反向依赖 service 包。
type WeightedModelTarget struct {
	Model  string `json:"model"`
	Weight int    `json:"weight"`
}

type GatewayGroupSnapshot struct {
	ID            int64  `json:"id"`
	GatewayNodeID int64  `json:"gateway_node_id,omitempty"`
	GatewayCode   string `json:"gateway_code,omitempty"`
	LocalGroupID  int64  `json:"local_group_id,omitempty"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Platform      string `json:"platform"`
	DisplayBrand  string `json:"display_brand,omitempty"`
	DisplayIcon   string `json:"display_icon,omitempty"`
	Status        string `json:"status"`
	SortOrder     int    `json:"sort_order"`
	// SchedulingMode is the group-local account selection policy. Keeping it
	// in the transport DTO prevents strict priority from silently reverting to
	// the default mode after a control-plane snapshot restore.
	SchedulingMode                       string                            `json:"scheduling_mode,omitempty"`
	IsExclusive                          bool                              `json:"is_exclusive"`
	MinimumTier                          *int                              `json:"minimum_tier"`
	EffectiveRequiredTier                int                               `json:"effective_required_tier"`
	PerUserConcurrency                   int                               `json:"per_user_concurrency"`
	SubscriptionType                     string                            `json:"subscription_type,omitempty"`
	RateMultiplier                       float64                           `json:"rate_multiplier"`
	PeakRateEnabled                      bool                              `json:"peak_rate_enabled"`
	PeakStart                            string                            `json:"peak_start,omitempty"`
	PeakEnd                              string                            `json:"peak_end,omitempty"`
	PeakRateMultiplier                   float64                           `json:"peak_rate_multiplier"`
	PeakTimezone                         string                            `json:"peak_timezone,omitempty"`
	DailyLimitUSD                        *float64                          `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD                       *float64                          `json:"weekly_limit_usd,omitempty"`
	MonthlyLimitUSD                      *float64                          `json:"monthly_limit_usd,omitempty"`
	AllowImageGeneration                 bool                              `json:"allow_image_generation"`
	ImageRateIndependent                 bool                              `json:"image_rate_independent"`
	ImageRateMultiplier                  float64                           `json:"image_rate_multiplier"`
	ImagePrice1K                         *float64                          `json:"image_price_1k,omitempty"`
	ImagePrice2K                         *float64                          `json:"image_price_2k,omitempty"`
	ImagePrice4K                         *float64                          `json:"image_price_4k,omitempty"`
	VideoRateIndependent                 bool                              `json:"video_rate_independent"`
	VideoRateMultiplier                  float64                           `json:"video_rate_multiplier"`
	VideoPrice480P                       *float64                          `json:"video_price_480p,omitempty"`
	VideoPrice720P                       *float64                          `json:"video_price_720p,omitempty"`
	VideoPrice1080P                      *float64                          `json:"video_price_1080p,omitempty"`
	VideoModelPrices                     map[string]map[string]float64     `json:"video_model_prices,omitempty"`
	WebSearchPricePerCall                *float64                          `json:"web_search_price_per_call,omitempty"`
	SearchPricePer1k                     *float64                          `json:"search_price_per_1k,omitempty"`
	AudioRealtimePricePerMin             *float64                          `json:"audio_realtime_price_per_min,omitempty"`
	AudioTtsPricePerMillionChars         *float64                          `json:"audio_tts_price_per_million_chars,omitempty"`
	AudioSttPricePerHour                 *float64                          `json:"audio_stt_price_per_hour,omitempty"`
	AllowOpenAILongContextBilling        bool                              `json:"allow_openai_long_context_billing"`
	AllowBlockedKeywordRequests          bool                              `json:"allow_blocked_keyword_requests"`
	OpenAILongContextBillingAccountCount int64                             `json:"openai_long_context_billing_account_count"`
	OpenAIStandardBillingAccountCount    int64                             `json:"openai_standard_billing_account_count"`
	OpenAILongContextBillingStatus       string                            `json:"openai_long_context_billing_status,omitempty"`
	ClaudeCodeOnly                       bool                              `json:"claude_code_only"`
	FallbackGroupID                      *int64                            `json:"fallback_group_id,omitempty"`
	FallbackGroupIDOnInvalidRequest      *int64                            `json:"fallback_group_id_on_invalid_request,omitempty"`
	FailoverGroupID                      *int64                            `json:"failover_group_id,omitempty"`
	FailoverStrategy                     string                            `json:"failover_strategy,omitempty"`
	ModelRouting                         map[string][]int64                `json:"model_routing,omitempty"`
	ModelRoutingEnabled                  bool                              `json:"model_routing_enabled"`
	ModelMapping                         map[string]string                 `json:"model_mapping,omitempty"`
	WeightedModelMapping                 map[string][]WeightedModelTarget  `json:"weighted_model_mapping,omitempty"`
	FallbackModel                        string                            `json:"fallback_model,omitempty"`
	BillingModelSource                   string                            `json:"billing_model_source,omitempty"`
	MCPXMLInject                         bool                              `json:"mcp_xml_inject"`
	SupportedModelScopes                 []string                          `json:"supported_model_scopes,omitempty"`
	AllowMessagesDispatch                bool                              `json:"allow_messages_dispatch"`
	AllowLive                            bool                              `json:"allow_live"`
	RequireOAuthOnly                     bool                              `json:"require_oauth_only"`
	RequirePrivacySet                    bool                              `json:"require_privacy_set"`
	DefaultMappedModel                   string                            `json:"default_mapped_model,omitempty"`
	MessagesDispatchModelConfig          OpenAIMessagesDispatchModelConfig `json:"messages_dispatch_model_config,omitempty"`
	ModelsListConfig                     GroupModelsListConfig             `json:"models_list_config,omitempty"`
	EffectiveModels                      []string                          `json:"effective_models"`
	EffectiveModelCount                  int                               `json:"effective_model_count"`
	EffectiveModelsReported              bool                              `json:"effective_models_reported"`
	EffectiveModelsVersion               int64                             `json:"effective_models_version"`
	RPMLimit                             int                               `json:"rpm_limit"`
	MaxReasoningEffort                   string                            `json:"max_reasoning_effort,omitempty"`
	ReasoningEffortMappings              []ReasoningEffortMapping          `json:"reasoning_effort_mappings,omitempty"`
	// ModelPrices 是按客户请求模型展开的倍率前计费合同。它只包含公开模型和
	// 价格，不携带渠道、账号、映射后模型或实际上游模型等内部路由信息。
	ModelPrices         []GatewayModelPrice `json:"model_prices"`
	ModelPricesReported bool                `json:"model_prices_reported"`
	Config              map[string]any      `json:"config,omitempty"`
	Version             int64               `json:"version"`
	UpdatedAtMillis     int64               `json:"updated_at_millis"`
}

type OpenAIMessagesDispatchModelConfig struct {
	OpusMappedModel    string            `json:"opus_mapped_model,omitempty"`
	SonnetMappedModel  string            `json:"sonnet_mapped_model,omitempty"`
	HaikuMappedModel   string            `json:"haiku_mapped_model,omitempty"`
	ExactModelMappings map[string]string `json:"exact_model_mappings,omitempty"`
}

type GroupModelsListConfig struct {
	Enabled             bool     `json:"enabled"`
	Models              []string `json:"models,omitempty"`
	ResponseModelMode   string   `json:"response_model_mode,omitempty"`
	ResponseModelCustom string   `json:"response_model_custom,omitempty"`
}
