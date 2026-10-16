package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

const (
	GrokVideoTaskStatePending   = "pending"
	GrokVideoTaskStateRunning   = "running"
	GrokVideoTaskStateSucceeded = "succeeded"
	GrokVideoTaskStateFailed    = "failed"
	GrokVideoTaskStateCancelled = "cancelled"
	GrokVideoTaskStateBilled    = "billed"

	GrokVideoOperationGeneration = "generation"
	GrokVideoOperationEdit       = "edit"
	GrokVideoOperationExtension  = "extension"

	grokVideoTaskSettlementLockTTL = 2 * time.Minute
	grokVideoTaskPollLockTTL       = 45 * time.Second
	GrokVideoTaskPollInterval      = 5 * time.Second
	GrokVideoTaskMaxPollInterval   = time.Minute
	GrokVideoTaskMaxPendingAge     = 48 * time.Hour
	GrokVideoTaskFinalRetention    = 7 * 24 * time.Hour
	// Terminal transform metadata is upstream-controlled. Accept exact source
	// durations beyond the generation API's 15-second request limit, but reject
	// implausible values instead of overflowing or silently clamping billing.
	GrokVideoTransformMaxBillableDurationSeconds = 3600
)

var ErrGrokVideoTaskNotFound = errors.New("grok video task not found")

// GrokVideoTask keeps the minimum non-sensitive metadata required to route a
// status request back to the account that created it and settle billing once.
// Tokens and OAuth credentials deliberately never enter this record.
type GrokVideoTask struct {
	RequestID             string                            `json:"request_id"`
	Operation             string                            `json:"operation,omitempty"`
	APIKeyID              int64                             `json:"api_key_id"`
	UserID                int64                             `json:"user_id"`
	OrganizationID        int64                             `json:"organization_id,omitempty"`
	BillingSubjectType    string                            `json:"billing_subject_type,omitempty"`
	BillingSubjectID      int64                             `json:"billing_subject_id,omitempty"`
	AccountID             int64                             `json:"account_id"`
	GroupID               *int64                            `json:"group_id,omitempty"`
	SubscriptionID        *int64                            `json:"subscription_id,omitempty"`
	RequestedModel        string                            `json:"requested_model"`
	BillingModel          string                            `json:"billing_model"`
	UpstreamModel         string                            `json:"upstream_model"`
	Resolution            string                            `json:"resolution"`
	DurationSeconds       int                               `json:"duration_seconds"`
	VideoCount            int                               `json:"video_count"`
	InboundEndpoint       string                            `json:"inbound_endpoint,omitempty"`
	UpstreamEndpoint      string                            `json:"upstream_endpoint,omitempty"`
	UserAgent             string                            `json:"user_agent,omitempty"`
	IPAddress             string                            `json:"ip_address,omitempty"`
	SessionID             string                            `json:"session_id,omitempty"`
	RequestPayloadHash    string                            `json:"request_payload_hash,omitempty"`
	ChannelID             int64                             `json:"channel_id,omitempty"`
	ChannelMappedModel    string                            `json:"channel_mapped_model,omitempty"`
	BillingModelSource    string                            `json:"billing_model_source,omitempty"`
	ModelMappingChain     string                            `json:"model_mapping_chain,omitempty"`
	TargetPlatform        string                            `json:"target_platform,omitempty"`
	CompositeRouteSource  string                            `json:"composite_route_source,omitempty"`
	QuotaRequestID        string                            `json:"quota_request_id,omitempty"`
	GatewayKeyID          string                            `json:"gateway_key_id,omitempty"`
	LeaseID               string                            `json:"lease_id,omitempty"`
	Region                string                            `json:"region,omitempty"`
	GatewayID             string                            `json:"gateway_id,omitempty"`
	ReservedCents         int64                             `json:"reserved_cents,omitempty"`
	BillingSettlementMode gatewaylite.BillingSettlementMode `json:"billing_settlement_mode,omitempty"`
	FundingSnapshot *gatewaylite.FundingAuthorization `json:"funding_snapshot,omitempty"`
	GroupSnapshot         *GrokVideoTaskGroupSnapshot       `json:"group_snapshot,omitempty"`
	BillingSnapshot       *GrokVideoTaskBillingSnapshot     `json:"billing_snapshot,omitempty"`
	State                 string                            `json:"state"`
	LastStatusMessage     string                            `json:"last_status_message,omitempty"`
	PollAttempts          int                               `json:"poll_attempts,omitempty"`
	ConsecutiveErrors     int                               `json:"consecutive_errors,omitempty"`
	LastPolledAt          *time.Time                        `json:"last_polled_at,omitempty"`
	NextPollAt            *time.Time                        `json:"next_poll_at,omitempty"`
	CreatedAt             time.Time                         `json:"created_at"`
	UpdatedAt             time.Time                         `json:"updated_at"`
	CompletedAt           *time.Time                        `json:"completed_at,omitempty"`
	BilledAt              *time.Time                        `json:"billed_at,omitempty"`
	QuotaFinalizedAt      *time.Time                        `json:"quota_finalized_at,omitempty"`
}

type GrokVideoTaskRateQuote struct {
	TotalUnitCost   float64 `json:"total_unit_cost"`
	ActualUnitCost  float64 `json:"actual_unit_cost"`
	ScaleByDuration bool    `json:"scale_by_duration"`
}

// GrokVideoTaskBillingSnapshot stores effective one-video rates after group,
// user-group and channel pricing have been resolved. This prevents an
// administrator price change during generation from repricing accepted work.
type GrokVideoTaskBillingSnapshot struct {
	RateMultiplier      float64 `json:"rate_multiplier"`
	VideoRateMultiplier float64 `json:"video_rate_multiplier"`
	// RatePolicyAudit keeps the original request-time group/user/peak evidence.
	// Async settlement can happen hours later, so rebuilding this from current
	// group settings would make the amount correct but the audit trail false.
	RatePolicyAudit *gatewaylite.RatePolicyAudit      `json:"rate_policy_audit,omitempty"`
	Rates           map[string]GrokVideoTaskRateQuote `json:"rates"`
}

func (s *GrokVideoTaskBillingSnapshot) CostFor(resolution string, durationSeconds, videoCount int) *CostBreakdown {
	return s.costForOperation(GrokVideoOperationGeneration, resolution, durationSeconds, videoCount)
}

func (s *GrokVideoTaskBillingSnapshot) costForOperation(operation, resolution string, durationSeconds, videoCount int) *CostBreakdown {
	if s == nil || len(s.Rates) == 0 {
		return nil
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	quote, ok := s.Rates[resolution]
	if !ok {
		return nil
	}
	if videoCount <= 0 {
		videoCount = 1
	}
	factor := float64(videoCount)
	if quote.ScaleByDuration {
		if normalizeGrokVideoOperation(operation) == GrokVideoOperationGeneration {
			durationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(durationSeconds)
		} else if durationSeconds <= 0 || durationSeconds > GrokVideoTransformMaxBillableDurationSeconds {
			return nil
		}
		factor *= float64(durationSeconds)
	}
	return &CostBreakdown{
		TotalCost:   quote.TotalUnitCost * factor,
		ActualCost:  quote.ActualUnitCost * factor,
		BillingMode: string(BillingModeVideo),
	}
}

// GrokVideoTaskGroupSnapshot freezes the billing identity used when the
// upstream task was created. Administrators may rename, reprice or delete a
// group while a video is still running; the already accepted request must keep
// its original subscription type and media pricing context.
type GrokVideoTaskGroupSnapshot struct {
	ID                   int64    `json:"id"`
	Name                 string   `json:"name,omitempty"`
	Platform             string   `json:"platform"`
	SubscriptionType     string   `json:"subscription_type,omitempty"`
	RateMultiplier       float64  `json:"rate_multiplier"`
	VideoRateIndependent bool     `json:"video_rate_independent,omitempty"`
	VideoRateMultiplier  float64  `json:"video_rate_multiplier,omitempty"`
	VideoPrice480P       *float64 `json:"video_price_480p,omitempty"`
	VideoPrice720P       *float64 `json:"video_price_720p,omitempty"`
	VideoPrice1080P      *float64 `json:"video_price_1080p,omitempty"`
}

func newGrokVideoTaskGroupSnapshot(group *Group) *GrokVideoTaskGroupSnapshot {
	if group == nil || group.ID <= 0 {
		return nil
	}
	return &GrokVideoTaskGroupSnapshot{
		ID:                   group.ID,
		Name:                 group.Name,
		Platform:             group.Platform,
		SubscriptionType:     group.SubscriptionType,
		RateMultiplier:       group.RateMultiplier,
		VideoRateIndependent: group.VideoRateIndependent,
		VideoRateMultiplier:  group.VideoRateMultiplier,
		VideoPrice480P:       cloneFloat64Ptr(group.VideoPrice480P),
		VideoPrice720P:       cloneFloat64Ptr(group.VideoPrice720P),
		VideoPrice1080P:      cloneFloat64Ptr(group.VideoPrice1080P),
	}
}

func (s *GrokVideoTaskGroupSnapshot) Group() *Group {
	if s == nil || s.ID <= 0 {
		return nil
	}
	return &Group{
		ID:                   s.ID,
		Name:                 s.Name,
		Platform:             firstNonEmpty(strings.TrimSpace(s.Platform), PlatformGrok),
		Status:               StatusActive,
		Hydrated:             true,
		SubscriptionType:     s.SubscriptionType,
		RateMultiplier:       s.RateMultiplier,
		VideoRateIndependent: s.VideoRateIndependent,
		VideoRateMultiplier:  s.VideoRateMultiplier,
		VideoPrice480P:       cloneFloat64Ptr(s.VideoPrice480P),
		VideoPrice720P:       cloneFloat64Ptr(s.VideoPrice720P),
		VideoPrice1080P:      cloneFloat64Ptr(s.VideoPrice1080P),
	}
}

func cloneFloat64Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func (s *OpenAIGatewayService) captureGrokVideoTaskBillingSnapshot(ctx context.Context, apiKey *APIKey, billingModel string) *GrokVideoTaskBillingSnapshot {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.User == nil {
		return nil
	}
	apiKey = s.apiKeyWithFreshGroupMediaPricing(ctx, apiKey)
	multiplier := 1.0
	var ratePolicyAudit *gatewaylite.RatePolicyAudit
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if policy, ok := EffectiveRatePolicyFromContext(ctx); ok {
		// 异步视频任务同样必须沿用请求入口冻结的倍率，不能在任务注册或轮询时重算。
		multiplier = policy.EffectiveRateMultiplier
		cloned := policy.Clone()
		ratePolicyAudit = &cloned
	} else if apiKey.GroupID != nil && apiKey.Group != nil {
		resolver := s.userGroupRateResolver
		if resolver == nil {
			resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.grok_video_task")
		}
		resolved, err := resolver.Resolve(ctx, apiKey.User.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
		if err != nil {
			logger.LegacyPrintf("service.grok_video_task", "resolve user group rate failed: user=%d group=%d err=%v", apiKey.User.ID, *apiKey.GroupID, err)
			return nil
		}
		multiplier = resolved
	}
	videoMultiplier := resolveVideoRateMultiplier(apiKey, multiplier)
	snapshot := &GrokVideoTaskBillingSnapshot{
		RateMultiplier:      multiplier,
		VideoRateMultiplier: videoMultiplier,
		RatePolicyAudit:     ratePolicyAudit,
		Rates:               make(map[string]GrokVideoTaskRateQuote, 3),
	}
	for _, resolution := range []string{VideoBillingResolution480P, VideoBillingResolution720P, VideoBillingResolution1080P} {
		oneSecond := s.calculateOpenAIVideoCost(ctx, billingModel, apiKey, &OpenAIForwardResult{
			Model:                billingModel,
			BillingModel:         billingModel,
			UpstreamModel:        billingModel,
			VideoCount:           1,
			VideoResolution:      resolution,
			VideoDurationSeconds: 1,
		}, videoMultiplier)
		twoSeconds := s.calculateOpenAIVideoCost(ctx, billingModel, apiKey, &OpenAIForwardResult{
			Model:                billingModel,
			BillingModel:         billingModel,
			UpstreamModel:        billingModel,
			VideoCount:           1,
			VideoResolution:      resolution,
			VideoDurationSeconds: 2,
		}, videoMultiplier)
		if oneSecond == nil || twoSeconds == nil {
			continue
		}
		snapshot.Rates[resolution] = GrokVideoTaskRateQuote{
			TotalUnitCost:   oneSecond.TotalCost,
			ActualUnitCost:  oneSecond.ActualCost,
			ScaleByDuration: nearlyEqualFloat(twoSeconds.ActualCost, oneSecond.ActualCost*2) && nearlyEqualFloat(twoSeconds.TotalCost, oneSecond.TotalCost*2),
		}
	}
	if len(snapshot.Rates) == 0 {
		return nil
	}
	return snapshot
}

// PrepareGrokVideoTaskBilling freezes the effective rate and expands the
// gateway-lite quota reservation before xAI accepts asynchronous work. The
// estimate rounds upward for reservation safety; final settlement still uses
// the exact frozen USD cost and the shared sub-cent carry rule.
func (s *OpenAIGatewayService) PrepareGrokVideoTaskBilling(
	ctx context.Context,
	apiKey *APIKey,
	billingModel string,
	resolution string,
	durationSeconds int,
	videoCount int,
) (*GrokVideoTaskBillingSnapshot, int64, error) {
	snapshot := s.captureGrokVideoTaskBillingSnapshot(ctx, apiKey, billingModel)
	if snapshot == nil {
		return nil, 0, errors.New("grok video pricing is unavailable")
	}
	cost := snapshot.CostFor(resolution, durationSeconds, videoCount)
	if cost == nil || cost.ActualCost < 0 || math.IsNaN(cost.ActualCost) || math.IsInf(cost.ActualCost, 0) {
		return nil, 0, errors.New("grok video price cannot be resolved")
	}
	estimatedCents := int64(math.Ceil(cost.ActualCost*100 - 1e-9))
	if estimatedCents <= 0 {
		estimatedCents = 1
	}
	if gatewaylite.HasRequestFunding(ctx) {
		if videoCount<1 || videoCount>16{return nil,0,ErrStrictCardEndpointUnsupported}
		maximum:=cost.ActualCost
		for tier:=range snapshot.Rates {
			candidate:=snapshot.costForOperation(GrokVideoOperationEdit,tier,GrokVideoTransformMaxBillableDurationSeconds,videoCount)
			if candidate==nil || !finiteNonNegative(candidate.ActualCost){return nil,0,ErrModelPricingUnavailable}
			if candidate.ActualCost>maximum {maximum=candidate.ActualCost}
		}
		bound,err:=fundingBoundFromUSD(maximum,"video:"+billingModel+",maximum_resolution_duration")
		if err!=nil{return nil,0,err}
		if err:=gatewaylite.EnsureFundingUpperBound(ctx,bound.MicroCents,bound.Evidence);err!=nil{return nil,0,err}
		return snapshot,(bound.MicroCents+999_999)/1_000_000,nil
	}
	reservedCents, err := gatewaylite.EnsureRequestReservation(ctx, estimatedCents)
	if err != nil {
		return nil, 0, err
	}
	return snapshot, reservedCents, nil
}

func nearlyEqualFloat(left, right float64) bool {
	delta := left - right
	if delta < 0 {
		delta = -delta
	}
	return delta <= 1e-12
}

func (t *GrokVideoTask) Normalize() {
	if t == nil {
		return
	}
	t.RequestID = strings.TrimSpace(t.RequestID)
	t.Operation = normalizeGrokVideoOperation(t.Operation)
	t.RequestedModel = strings.TrimSpace(t.RequestedModel)
	t.BillingModel = strings.TrimSpace(t.BillingModel)
	t.UpstreamModel = strings.TrimSpace(t.UpstreamModel)
	t.TargetPlatform = strings.ToLower(strings.TrimSpace(t.TargetPlatform))
	t.CompositeRouteSource = strings.ToLower(strings.TrimSpace(t.CompositeRouteSource))
	t.QuotaRequestID = strings.TrimSpace(t.QuotaRequestID)
	t.GatewayKeyID = strings.TrimSpace(t.GatewayKeyID)
	t.LeaseID = strings.TrimSpace(t.LeaseID)
	t.Region = strings.TrimSpace(t.Region)
	t.GatewayID = strings.TrimSpace(t.GatewayID)
	t.Resolution = NormalizeVideoBillingResolutionOrDefault(t.Resolution)
	// Edit duration is not present in the create request; it is learned from
	// the terminal xAI response. Keeping zero avoids charging the generic eight
	// second default when terminal metadata is incomplete.
	switch t.Operation {
	case GrokVideoOperationEdit:
		if t.DurationSeconds < 0 {
			t.DurationSeconds = 0
		}
	case GrokVideoOperationExtension:
		if t.DurationSeconds <= 0 {
			t.DurationSeconds = grokVideoExtensionDefaultSeconds
		}
	default:
		t.DurationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(t.DurationSeconds)
	}
	if t.VideoCount <= 0 {
		t.VideoCount = 1
	}
	if strings.TrimSpace(t.State) == "" {
		t.State = GrokVideoTaskStatePending
	}
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	if t.NeedsBackgroundWork() && t.NextPollAt == nil {
		next := now.Add(GrokVideoTaskPollInterval)
		if t.State == GrokVideoTaskStateSucceeded {
			next = now
		}
		t.NextPollAt = &next
	}
	if t.IsFinalized() {
		t.NextPollAt = nil
	}
}

func normalizeGrokVideoOperation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case GrokVideoOperationEdit:
		return GrokVideoOperationEdit
	case GrokVideoOperationExtension:
		return GrokVideoOperationExtension
	default:
		// Records created before transforms existed are normal generations.
		return GrokVideoOperationGeneration
	}
}

func (t *GrokVideoTask) Touch() {
	if t == nil {
		return
	}
	t.Normalize()
	t.UpdatedAt = time.Now().UTC()
}

func (t *GrokVideoTask) BillingRequestID() string {
	if t == nil {
		return ""
	}
	if requestID := strings.TrimSpace(t.QuotaRequestID); requestID != "" {
		return requestID
	}
	if strings.TrimSpace(t.RequestID) == "" {
		return ""
	}
	return "grok-video:" + strings.TrimSpace(t.RequestID)
}

func (t *GrokVideoTask) usageContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil {
		return ctx
	}
	usageCtx := ctx
	if strings.TrimSpace(t.QuotaRequestID) == "" || t.UserID <= 0 {
		return t.withCompositeUsageContext(usageCtx)
	}
	groupID := int64(0)
	groupName := ""
	rateMultiplier := 1.0
	if t.GroupSnapshot != nil {
		groupID = t.GroupSnapshot.ID
		groupName = t.GroupSnapshot.Name
		if t.GroupSnapshot.RateMultiplier > 0 {
			rateMultiplier = t.GroupSnapshot.RateMultiplier
		}
	} else if t.GroupID != nil {
		groupID = *t.GroupID
	}
	if t.BillingSnapshot != nil && t.BillingSnapshot.RateMultiplier > 0 {
		rateMultiplier = t.BillingSnapshot.RateMultiplier
	}
	var ratePolicyAudit *gatewaylite.RatePolicyAudit
	if t.BillingSnapshot != nil && t.BillingSnapshot.RatePolicyAudit != nil && t.BillingSnapshot.RatePolicyAudit.Valid() {
		cloned := t.BillingSnapshot.RatePolicyAudit.Clone()
		ratePolicyAudit = &cloned
	}
	var funding *gatewaylite.RequestFunding
	entryGatewayID,entryGroupID:=int64(0),int64(0)
	if t.FundingSnapshot!=nil {
		funding=gatewaylite.NewRequestFunding(*t.FundingSnapshot,nil,nil)
		entryGatewayID=t.FundingSnapshot.EntryGatewayNodeID;entryGroupID=t.FundingSnapshot.EntryGroupID
	}
	usageCtx = gatewaylite.ContextWithRequestMeta(usageCtx, gatewaylite.RequestMeta{
		Funding:funding,EntryGatewayNodeID:entryGatewayID,EntryGlobalGroupID:entryGroupID,
		RequestID:             t.QuotaRequestID,
		KeyID:                 t.GatewayKeyID,
		UserID:                t.UserID,
		OrganizationID:        t.OrganizationID,
		BillingSubjectType:    t.BillingSubjectType,
		BillingSubjectID:      t.BillingSubjectID,
		LeaseID:               t.LeaseID,
		Region:                t.Region,
		GatewayID:             t.GatewayID,
		GroupID:               groupID,
		GroupName:             groupName,
		RateMultiplier:        rateMultiplier,
		RatePolicyAudit:       ratePolicyAudit,
		AccountRateMultiplier: 1,
		AsyncSettlement:       true,
		BillingSettlementMode: t.BillingSettlementMode,
	})
	return t.withCompositeUsageContext(usageCtx)
}

func (t *GrokVideoTask) withCompositeUsageContext(ctx context.Context) context.Context {
	if t == nil || strings.TrimSpace(t.TargetPlatform) == "" {
		return ctx
	}
	return WithCompositeRouteDecision(ctx, CompositeRouteDecision{
		Matched:        true,
		Source:         strings.TrimSpace(t.CompositeRouteSource),
		PublicModel:    strings.TrimSpace(t.RequestedModel),
		TargetPlatform: strings.TrimSpace(t.TargetPlatform),
		UpstreamModel:  strings.TrimSpace(t.UpstreamModel),
	})
}

func (t *GrokVideoTask) requiresQuotaFinalization() bool {
	return t != nil && strings.TrimSpace(t.QuotaRequestID) != "" && t.QuotaFinalizedAt == nil
}

func (t *GrokVideoTask) IsTerminal() bool {
	if t == nil {
		return false
	}
	switch t.State {
	case GrokVideoTaskStateSucceeded, GrokVideoTaskStateFailed, GrokVideoTaskStateCancelled, GrokVideoTaskStateBilled:
		return true
	default:
		return false
	}
}

// NeedsBackgroundWork distinguishes an upstream terminal success from a fully
// settled task. A succeeded task must remain visible to the worker until the
// corresponding usage charge has been committed idempotently.
func (t *GrokVideoTask) NeedsBackgroundWork() bool {
	if t == nil {
		return false
	}
	switch t.State {
	case GrokVideoTaskStatePending, GrokVideoTaskStateRunning, GrokVideoTaskStateSucceeded:
		return true
	case GrokVideoTaskStateFailed, GrokVideoTaskStateCancelled:
		return t.requiresQuotaFinalization()
	default:
		return false
	}
}

func (t *GrokVideoTask) IsFinalized() bool {
	if t == nil {
		return false
	}
	switch t.State {
	case GrokVideoTaskStateBilled:
		return true
	case GrokVideoTaskStateFailed, GrokVideoTaskStateCancelled:
		return !t.requiresQuotaFinalization()
	default:
		return false
	}
}

func (t *GrokVideoTask) RecordPollFailure(message string, retryAfter time.Duration) {
	if t == nil || t.IsFinalized() {
		return
	}
	now := time.Now().UTC()
	t.PollAttempts++
	t.ConsecutiveErrors++
	t.LastPolledAt = &now
	if message = strings.TrimSpace(message); message != "" {
		t.LastStatusMessage = message
	}
	if retryAfter <= 0 {
		retryAfter = GrokVideoTaskPollInterval
		for i := 1; i < t.ConsecutiveErrors && retryAfter < GrokVideoTaskMaxPollInterval; i++ {
			retryAfter *= 2
		}
	}
	if retryAfter > GrokVideoTaskMaxPollInterval {
		retryAfter = GrokVideoTaskMaxPollInterval
	}
	next := now.Add(retryAfter)
	t.NextPollAt = &next
}

type GrokVideoTaskStore interface {
	Ready(ctx context.Context) error
	Save(ctx context.Context, task *GrokVideoTask) error
	Get(ctx context.Context, requestID string) (*GrokVideoTask, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]*GrokVideoTask, error)
	TryLockPolling(ctx context.Context, requestID string, ttl time.Duration) (bool, error)
	UnlockPolling(ctx context.Context, requestID string) error
	TryLockSettlement(ctx context.Context, requestID string, ttl time.Duration) (bool, error)
	UnlockSettlement(ctx context.Context, requestID string) error
	CleanupFinalized(ctx context.Context, before time.Time) (int64, error)
}

type GrokVideoTaskRegistration struct {
	APIKey             *APIKey
	Account            *Account
	Subscription       *UserSubscription
	Result             *OpenAIForwardResult
	RequestedModel     string
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	SessionID          string
	RequestPayloadHash string
	ChannelUsageFields ChannelUsageFields
	BillingSnapshot    *GrokVideoTaskBillingSnapshot
	Operation          string
}

type GrokVideoTaskStatus struct {
	State           string
	Message         string
	VideoCount      int
	Resolution      string
	DurationSeconds int
}

func normalizeGrokVideoTaskState(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "queued", "pending", "submitted", "created", "waiting":
		return GrokVideoTaskStatePending
	case "running", "processing", "in_progress", "generating":
		return GrokVideoTaskStateRunning
	case "completed", "complete", "done", "succeeded", "success", "ready":
		return GrokVideoTaskStateSucceeded
	case "cancelled", "canceled":
		return GrokVideoTaskStateCancelled
	case "failed", "error", "errored", "rejected", "expired":
		return GrokVideoTaskStateFailed
	default:
		return ""
	}
}

// ParseGrokVideoTaskStatus accepts the currently observed xAI shapes as well
// as common compatible relay wrappers. Unknown states remain non-terminal so a
// schema addition cannot accidentally charge or fail a task.
func ParseGrokVideoTaskStatus(body []byte) GrokVideoTaskStatus {
	status := GrokVideoTaskStatus{}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return status
	}
	for _, path := range []string{"status", "state", "data.status", "data.state", "video.status", "video.state"} {
		if value := normalizeGrokVideoTaskState(gjson.GetBytes(body, path).String()); value != "" {
			status.State = value
			break
		}
	}
	for _, path := range []string{"error.message", "message", "data.error.message", "data.message", "video.error.message"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			status.Message = value
			break
		}
	}
	for _, path := range []string{"duration", "duration_seconds", "data.duration", "data.duration_seconds", "video.duration", "video.duration_seconds"} {
		if value := gjson.GetBytes(body, path); value.Exists() && value.Type == gjson.Number && value.Int() > 0 {
			status.DurationSeconds = int(value.Int())
			break
		}
	}
	for _, path := range []string{"resolution", "data.resolution", "video.resolution"} {
		if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
			status.Resolution = value
			break
		}
	}
	for _, path := range []string{"n", "count", "video_count", "data.video_count"} {
		if value := gjson.GetBytes(body, path); value.Exists() && value.Type == gjson.Number && value.Int() > 0 {
			status.VideoCount = int(value.Int())
			break
		}
	}
	if status.VideoCount <= 0 {
		status.VideoCount = 1
	}

	// Some compatible upstreams omit status after completion but return a media
	// URL. Treat that as success; never infer success from an ID alone.
	if status.State == "" {
		for _, path := range []string{"url", "video.url", "data.url", "data.video.url", "output.url"} {
			if strings.TrimSpace(gjson.GetBytes(body, path).String()) != "" {
				status.State = GrokVideoTaskStateSucceeded
				break
			}
		}
	}
	if status.State == "" && status.Message != "" && (gjson.GetBytes(body, "error").Exists() || gjson.GetBytes(body, "data.error").Exists()) {
		status.State = GrokVideoTaskStateFailed
	}
	return status
}

// ParseGrokVideoTaskStatusForAccount keeps compatible relay status shapes for
// custom upstreams, while official xAI endpoints only settle documented
// status=done responses that include video.url.
func ParseGrokVideoTaskStatusForAccount(account *Account, body []byte) GrokVideoTaskStatus {
	status := ParseGrokVideoTaskStatus(body)
	if account == nil || !isOfficialGrokAPIBaseURL(account.GetGrokMediaBaseURL()) || status.State != GrokVideoTaskStateSucceeded {
		return status
	}
	if !strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "status").String()), "done") ||
		strings.TrimSpace(gjson.GetBytes(body, "video.url").String()) == "" {
		status.State = ""
	}
	return status
}

func (s *OpenAIGatewayService) SetGrokVideoTaskStore(store GrokVideoTaskStore) {
	if s == nil {
		return
	}
	s.grokVideoTaskStore = store
}

func (s *OpenAIGatewayService) CheckGrokVideoTaskStore(ctx context.Context) error {
	if s == nil || s.grokVideoTaskStore == nil {
		return errors.New("grok video task store is unavailable")
	}
	return s.grokVideoTaskStore.Ready(ctx)
}

func (s *OpenAIGatewayService) RegisterGrokVideoTask(ctx context.Context, in GrokVideoTaskRegistration) (*GrokVideoTask, error) {
	if s == nil || s.grokVideoTaskStore == nil {
		return nil, errors.New("grok video task store is unavailable")
	}
	if in.APIKey == nil || in.APIKey.User == nil || in.Account == nil || in.Result == nil {
		return nil, errors.New("grok video task registration context is incomplete")
	}
	requestID := strings.TrimSpace(in.Result.ResponseID)
	if requestID == "" {
		return nil, errors.New("grok video task request_id is empty")
	}
	billingAPIKey := s.apiKeyWithFreshGroupMediaPricing(ctx, in.APIKey)
	task := &GrokVideoTask{
		RequestID:          requestID,
		Operation:          normalizeGrokVideoOperation(in.Operation),
		APIKeyID:           in.APIKey.ID,
		UserID:             in.APIKey.User.ID,
		AccountID:          in.Account.ID,
		GroupID:            in.APIKey.GroupID,
		RequestedModel:     firstNonEmpty(strings.TrimSpace(in.RequestedModel), strings.TrimSpace(in.Result.Model)),
		BillingModel:       firstNonEmpty(strings.TrimSpace(in.Result.BillingModel), strings.TrimSpace(in.Result.Model)),
		UpstreamModel:      firstNonEmpty(strings.TrimSpace(in.Result.UpstreamModel), strings.TrimSpace(in.Result.Model)),
		Resolution:         in.Result.VideoResolution,
		DurationSeconds:    in.Result.VideoDurationSeconds,
		VideoCount:         in.Result.VideoCount,
		InboundEndpoint:    strings.TrimSpace(in.InboundEndpoint),
		UpstreamEndpoint:   strings.TrimSpace(in.UpstreamEndpoint),
		UserAgent:          strings.TrimSpace(in.UserAgent),
		IPAddress:          strings.TrimSpace(in.IPAddress),
		SessionID:          sanitizeSessionID(in.SessionID),
		RequestPayloadHash: strings.TrimSpace(in.RequestPayloadHash),
		ChannelID:          in.ChannelUsageFields.ChannelID,
		ChannelMappedModel: strings.TrimSpace(in.ChannelUsageFields.ChannelMappedModel),
		BillingModelSource: strings.TrimSpace(in.ChannelUsageFields.BillingModelSource),
		ModelMappingChain:  strings.TrimSpace(in.ChannelUsageFields.ModelMappingChain),
		GroupSnapshot:      newGrokVideoTaskGroupSnapshot(billingAPIKey.Group),
		State:              GrokVideoTaskStatePending,
	}
	if platform, ok := ResolvedTargetPlatformFromContext(ctx); ok {
		task.TargetPlatform = platform
	}
	if source, ok := CompositeRouteSourceFromContext(ctx); ok {
		task.CompositeRouteSource = source
	}
	if requested, ok := RequestedPublicModelFromContext(ctx); ok {
		task.RequestedModel = requested
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		task.OrganizationID = meta.OrganizationID
		task.BillingSubjectType = strings.TrimSpace(meta.BillingSubjectType)
		task.BillingSubjectID = meta.BillingSubjectID
		task.QuotaRequestID = meta.RequestID
		task.GatewayKeyID = meta.KeyID
		task.LeaseID = meta.LeaseID
		task.Region = meta.Region
		task.GatewayID = meta.GatewayID
		task.BillingSettlementMode = meta.BillingSettlementMode
		if meta.Funding!=nil {a:=meta.Funding.Snapshot();task.FundingSnapshot=&a;task.LeaseID="";task.ReservedCents=a.MaximumCents}
		if meta.Reservation != nil {
			task.ReservedCents, task.LeaseID, _ = meta.Reservation.Snapshot()
		}
	}
	task.BillingSnapshot = in.BillingSnapshot
	if task.BillingSnapshot == nil {
		task.BillingSnapshot = s.captureGrokVideoTaskBillingSnapshot(ctx, billingAPIKey, task.BillingModel)
	}
	if in.Subscription != nil {
		task.SubscriptionID = &in.Subscription.ID
	}
	task.Normalize()
	// The upstream has already accepted the task. Persist using a detached
	// context so a client disconnect cannot cancel the financial state write.
	// Brief retries absorb a transient connection handoff without charging an
	// untracked task immediately.
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var saveErr error
	for attempt := 0; attempt < 3; attempt++ {
		if saveErr = s.grokVideoTaskStore.Save(saveCtx, task); saveErr == nil {
			break
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
			select {
			case <-saveCtx.Done():
				timer.Stop()
				attempt = 2
			case <-timer.C:
			}
		}
	}
	if saveErr != nil {
		return nil, fmt.Errorf("save grok video task: %w", saveErr)
	}
	return task, nil
}

func (s *OpenAIGatewayService) GetGrokVideoTask(ctx context.Context, requestID string) (*GrokVideoTask, error) {
	if s == nil || s.grokVideoTaskStore == nil {
		return nil, ErrGrokVideoTaskNotFound
	}
	return s.grokVideoTaskStore.Get(ctx, requestID)
}

func (s *OpenAIGatewayService) GetGrokVideoTaskAccount(ctx context.Context, task *GrokVideoTask) (*Account, error) {
	if s == nil || s.accountRepo == nil || task == nil || task.AccountID <= 0 {
		return nil, errors.New("grok video task account is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil && errors.Is(err, ErrAccountNotFound) {
		// An accepted async task remains account-affine even after a later soft
		// delete. This narrow optional interface is not used by normal routing.
		if reader, ok := s.accountRepo.(interface {
			GetByIDIncludingDeleted(context.Context, int64) (*Account, error)
		}); ok {
			account, err = reader.GetByIDIncludingDeleted(ctx, task.AccountID)
		}
	}
	if err != nil {
		return nil, err
	}
	if !supportsGrokMediaAccount(account) {
		return nil, errors.New("grok video task account is invalid")
	}
	return account, nil
}

// GetMediaTaskAccountByID 按任务绑定的账号 ID 加载账号（含软删兜底）。
// 与 GetGrokVideoTaskAccount 的区别：这里不做 Grok 媒体账号能力校验，
// 因为 Seedance 等媒体任务绑定的是 OpenAI 平台 API Key 账号。
func (s *OpenAIGatewayService) GetMediaTaskAccountByID(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return nil, errors.New("media task account is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil && errors.Is(err, ErrAccountNotFound) {
		// 已受理的异步任务仍然账号亲和，即使账号之后被软删也要能查回来。
		if reader, ok := s.accountRepo.(interface {
			GetByIDIncludingDeleted(context.Context, int64) (*Account, error)
		}); ok {
			account, err = reader.GetByIDIncludingDeleted(ctx, accountID)
		}
	}
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, errors.New("media task account is unavailable")
	}
	return account, nil
}

func (s *OpenAIGatewayService) UpdateGrokVideoTaskStatus(ctx context.Context, task *GrokVideoTask, status GrokVideoTaskStatus) error {
	if s == nil || s.grokVideoTaskStore == nil || task == nil {
		return nil
	}
	// Once a task has been billed or reached a failure terminal state, later
	// polling responses cannot reopen it and create a second charge.
	if task.State == GrokVideoTaskStateBilled || ((task.State == GrokVideoTaskStateFailed || task.State == GrokVideoTaskStateCancelled) && !task.requiresQuotaFinalization()) {
		return nil
	}
	now := time.Now().UTC()
	task.PollAttempts++
	task.ConsecutiveErrors = 0
	task.LastPolledAt = &now
	if normalized := normalizeGrokVideoTaskState(status.State); normalized != "" {
		task.State = normalized
	}
	if status.Message != "" {
		task.LastStatusMessage = strings.TrimSpace(status.Message)
	}
	// Extension status reports the total output duration (source + generated
	// segment), while pricing applies only to the requested extension segment.
	if status.DurationSeconds > 0 && task.Operation != GrokVideoOperationExtension {
		if task.Operation == GrokVideoOperationEdit {
			task.DurationSeconds = status.DurationSeconds
		} else {
			task.DurationSeconds = NormalizeVideoBillingDurationSecondsOrDefault(status.DurationSeconds)
		}
	}
	if status.Resolution != "" {
		task.Resolution = NormalizeVideoBillingResolutionOrDefault(status.Resolution)
	}
	if status.VideoCount > 0 {
		task.VideoCount = status.VideoCount
	}
	if task.IsTerminal() && task.CompletedAt == nil {
		task.CompletedAt = &now
	}
	switch task.State {
	case GrokVideoTaskStatePending, GrokVideoTaskStateRunning:
		next := now.Add(GrokVideoTaskPollInterval)
		task.NextPollAt = &next
	case GrokVideoTaskStateSucceeded:
		// Persist success before billing, but keep it immediately due so a crash
		// between the two writes is recovered by the background worker.
		task.NextPollAt = &now
	case GrokVideoTaskStateFailed, GrokVideoTaskStateCancelled:
		if task.requiresQuotaFinalization() {
			task.NextPollAt = &now
		} else {
			task.NextPollAt = nil
		}
	case GrokVideoTaskStateBilled:
		task.NextPollAt = nil
	}
	task.Normalize()
	return s.grokVideoTaskStore.Save(ctx, task)
}

func (s *OpenAIGatewayService) FinalizeGrokVideoTask(
	ctx context.Context,
	task *GrokVideoTask,
	status GrokVideoTaskStatus,
	apiKey *APIKey,
	subscription *UserSubscription,
	account *Account,
	apiKeyService APIKeyQuotaUpdater,
) error {
	if s == nil || s.grokVideoTaskStore == nil || task == nil {
		return errors.New("grok video task settlement is unavailable")
	}
	if task.State == GrokVideoTaskStateBilled || ((task.State == GrokVideoTaskStateFailed || task.State == GrokVideoTaskStateCancelled) && !task.requiresQuotaFinalization()) {
		return nil
	}
	if err := s.UpdateGrokVideoTaskStatus(ctx, task, status); err != nil {
		return err
	}
	if task.State == GrokVideoTaskStateFailed || task.State == GrokVideoTaskStateCancelled {
		return s.finalizeGrokVideoTaskQuotaRelease(ctx, task)
	}
	if task.State == GrokVideoTaskStateBilled {
		return nil
	}
	if task.State != GrokVideoTaskStateSucceeded {
		return nil
	}
	if apiKey == nil || apiKey.User == nil || account == nil {
		return errors.New("grok video task billing context is incomplete")
	}
	locked, err := s.grokVideoTaskStore.TryLockSettlement(ctx, task.RequestID, grokVideoTaskSettlementLockTTL)
	if err != nil {
		return fmt.Errorf("lock grok video settlement: %w", err)
	}
	if !locked {
		return nil
	}
	originalTask := task
	settled := false
	defer func() {
		if !settled {
			_ = s.grokVideoTaskStore.UnlockSettlement(context.Background(), task.RequestID)
		}
	}()
	latest, err := s.grokVideoTaskStore.Get(ctx, task.RequestID)
	if err != nil {
		return fmt.Errorf("reload grok video task for settlement: %w", err)
	}
	if latest.State == GrokVideoTaskStateBilled || latest.IsFinalized() {
		*originalTask = *latest
		settled = true
		return s.grokVideoTaskStore.UnlockSettlement(ctx, latest.RequestID)
	}
	if latest.State != GrokVideoTaskStateSucceeded {
		*originalTask = *latest
		settled = true
		return s.grokVideoTaskStore.UnlockSettlement(ctx, latest.RequestID)
	}
	task = latest

	result := &OpenAIForwardResult{
		RequestID:            task.BillingRequestID(),
		ResponseID:           task.RequestID,
		Model:                task.RequestedModel,
		BillingModel:         task.BillingModel,
		UpstreamModel:        task.UpstreamModel,
		Duration:             time.Since(task.CreatedAt),
		VideoCount:           task.VideoCount,
		VideoResolution:      task.Resolution,
		VideoDurationSeconds: task.DurationSeconds,
	}
	if task.BillingSnapshot == nil {
		return errors.New("grok video task billing snapshot is unavailable")
	}
	if task.Operation == GrokVideoOperationEdit && task.DurationSeconds <= 0 {
		return errors.New("grok video edit terminal duration is unavailable")
	}
	costOverride := task.BillingSnapshot.costForOperation(task.Operation, task.Resolution, task.DurationSeconds, task.VideoCount)
	if costOverride == nil {
		return errors.New("grok video task frozen price is unavailable")
	}
	billingCtx := task.usageContext(ctx)
	if meta, ok := gatewaylite.RequestMetaFromContext(billingCtx); ok && meta.AsyncSettlement {
		actualCents := int64(0)
		if meta.BillingSettlementMode == gatewaylite.BillingSettlementModeIdempotentV2 {
			actualCents, err = gatewaylite.PrepareActualCostSettlement(billingCtx, task.BillingRequestID(), costOverride.ActualCost)
			if err != nil {
				return fmt.Errorf("freeze grok video idempotent settlement: %w", err)
			}
		} else {
			actualCents = gatewaylite.ActualCostToCentsForContext(billingCtx, costOverride.ActualCost)
		}
		if err := gatewaylite.EnsureCommittedUsageCapacity(billingCtx, actualCents); err != nil {
			return fmt.Errorf("ensure grok video final quota: %w", err)
		}
		billingCtx = gatewaylite.ContextWithActualCentsOverride(billingCtx, actualCents)
	}
	if err := s.RecordUsage(billingCtx, &OpenAIRecordUsageInput{
		Result:             result,
		APIKey:             apiKey,
		User:               apiKey.User,
		Account:            account,
		Subscription:       subscription,
		InboundEndpoint:    task.InboundEndpoint,
		UpstreamEndpoint:   task.UpstreamEndpoint,
		UserAgent:          task.UserAgent,
		IPAddress:          task.IPAddress,
		SessionID:          task.SessionID,
		RequestPayloadHash: task.RequestPayloadHash,
		APIKeyService:      apiKeyService,
		CostOverride:       costOverride,
		RateMultiplierOverride: func() *float64 {
			if task.BillingSnapshot == nil {
				return nil
			}
			return cloneFloat64Ptr(&task.BillingSnapshot.RateMultiplier)
		}(),
		VideoRateMultiplierOverride: func() *float64 {
			if task.BillingSnapshot == nil {
				return nil
			}
			return cloneFloat64Ptr(&task.BillingSnapshot.VideoRateMultiplier)
		}(),
		ChannelUsageFields: ChannelUsageFields{
			ChannelID:          task.ChannelID,
			OriginalModel:      task.RequestedModel,
			ChannelMappedModel: task.ChannelMappedModel,
			BillingModelSource: task.BillingModelSource,
			ModelMappingChain:  task.ModelMappingChain,
		},
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	task.State = GrokVideoTaskStateBilled
	task.BilledAt = &now
	task.CompletedAt = &now
	task.QuotaFinalizedAt = &now
	task.UpdatedAt = now
	task.NextPollAt = nil
	if err := s.grokVideoTaskStore.Save(ctx, task); err != nil {
		// RecordUsage is idempotent by BillingRequestID. Leaving the lock until its
		// TTL expires avoids a tight duplicate loop; a later retry can safely save
		// the terminal marker without charging twice.
		return fmt.Errorf("mark grok video task billed: %w", err)
	}
	*originalTask = *task
	settled = true
	return s.grokVideoTaskStore.UnlockSettlement(ctx, task.RequestID)
}

func (s *OpenAIGatewayService) finalizeGrokVideoTaskQuotaRelease(ctx context.Context, task *GrokVideoTask) error {
	if task == nil || !task.requiresQuotaFinalization() {
		return nil
	}
	releaseCtx := task.usageContext(ctx)
	billingTier := NormalizeVideoBillingResolutionOrDefault(task.Resolution)
	event := gatewaylite.UsageEvent{
		RequestID:            task.BillingRequestID(),
		UserID:               task.UserID,
		OrganizationID:       task.OrganizationID,
		BillingSubjectType:   task.BillingSubjectType,
		BillingSubjectID:     task.BillingSubjectID,
		KeyID:                task.GatewayKeyID,
		LeaseID:              task.LeaseID,
		Region:               task.Region,
		GatewayID:            task.GatewayID,
		Protocol:             "openai_compatible",
		Model:                firstNonEmpty(task.RequestedModel, task.BillingModel, task.UpstreamModel),
		Method:               "POST",
		Path:                 task.InboundEndpoint,
		Status:               200,
		BillingMode:          string(BillingModeVideo),
		BillingTier:          billingTier,
		VideoCount:           int64(task.VideoCount),
		VideoResolution:      billingTier,
		VideoDurationSeconds: int64(task.DurationSeconds),
		EstimatedCents:       task.ReservedCents,
		ActualCents:          0,
		StartedAtMillis:      task.CreatedAt.UnixMilli(),
		EndedAtMillis:        time.Now().UTC().UnixMilli(),
	}
	if err := gatewaylite.ReportUsageCorrection(releaseCtx, event); err != nil {
		return fmt.Errorf("release grok video quota reservation: %w", err)
	}
	now := time.Now().UTC()
	task.QuotaFinalizedAt = &now
	task.UpdatedAt = now
	task.NextPollAt = nil
	return s.grokVideoTaskStore.Save(ctx, task)
}
