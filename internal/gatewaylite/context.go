package gatewaylite

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type contextKey string

const (
	requestMetaContextKey  contextKey = "gateway_lite_request_meta"
	ingressGroupContextKey contextKey = "gateway_lite_ingress_group"
)

type BillingSettlementMode string

const (
	BillingSettlementModeLegacy       BillingSettlementMode = "legacy"
	BillingSettlementModeIdempotentV2 BillingSettlementMode = "idempotent_v2"
)

type RequestMeta struct {
	RequestID                string
	KeyID                    string
	UserID                   int64
	OrganizationID           int64
	OrganizationName         string
	OrganizationRole         string
	BillingSubjectType       string
	BillingSubjectID         int64
	OrganizationConcurrency  int
	OrganizationRateLimitRPM int
	OrganizationRateLimitTPM int
	MemberConcurrency        int
	MemberRateLimitRPM       int
	MemberRateLimitTPM       int
	UserEmail                string
	Username                 string
	LeaseID                  string
	Region                   string
	GatewayID                string
	GroupID                  int64
	GroupName                string
	OriginalGroupID          int64
	OriginalGroupName        string
	KeywordPolicyMatched     bool
	KeywordPolicyRerouted    bool
	KeywordPolicySource      string
	KeywordPolicyReason      string
	DetectedLanguages        []string
	// RequestedReasoningEffort is the explicit client value before group policy,
	// protocol conversion, or provider normalization. ReasoningEffort is the
	// explicit value in the final upstream body. Empty means unspecified; it is
	// never filled from model defaults.
	RequestedReasoningEffort string
	ReasoningEffort          string
	RateMultiplier           float64
	RatePolicyAudit          *RatePolicyAudit
	AccountRateMultiplier    float64
	PolicyRevision           int64
	PolicyChecksum           string
	PriceVersion             int64
	Reservation              *RequestReservation
	// Funding is mutually exclusive with a regional balance lease; its immutable
	// scope is selected by the main site before keyword routing or failover.
	Funding             *RequestFunding
	EntryGatewayNodeID  int64
	EntryGlobalGroupID  int64
	ActualCentsOverride *int64
	// UpstreamConsumed distinguishes a local post-response policy rejection from
	// a pre-dispatch rejection. Once the provider has completed billable work, a
	// later 4xx returned by the gateway must not release the customer's lease.
	UpstreamConsumed bool
	AsyncSettlement  bool
	// 仅已恢复原身份的 Seedance 完成路径设置；与 HTTP 锁及逐轮 Final 合同无关。
	CommitAsyncReservation bool `json:"-"`
	// AsyncPersistenceUncertain keeps a reservation open even when the HTTP
	// response is an error because PostgreSQL may already have committed the task.
	AsyncPersistenceUncertain bool
	// BillingSettlementMode is frozen when the request enters gateway-lite.
	// A later admin switch must not make one request use two rounding rules.
	BillingSettlementMode BillingSettlementMode
}

type reasoningEffortAuditContextKey struct{}

type ReasoningEffortAudit struct {
	Requested string
	Final     string
}

// ContextWithIngressGroup records only the group where a request first entered
// the gateway. It is deliberately independent from RequestMeta: local glk_ keys
// need observability, but must never acquire a remote billing identity or lease.
// The first valid value wins so later failover cannot move the ingress counter.
func ContextWithIngressGroup(ctx context.Context, groupID int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if groupID <= 0 {
		return ctx
	}
	if _, ok := IngressGroupIDFromContext(ctx); ok {
		return ctx
	}
	return context.WithValue(ctx, ingressGroupContextKey, groupID)
}

func IngressGroupIDFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	groupID, ok := ctx.Value(ingressGroupContextKey).(int64)
	return groupID, ok && groupID > 0
}

// ContextWithReasoningEffortAudit preserves explicit request evidence without
// coupling it to remote-key authentication. Local glk_ keys do not carry a
// RequestMeta, so the dedicated context value is authoritative while the
// RequestMeta mirror lets the admission middleware include the original value
// in its base usage event.
func ContextWithReasoningEffortAudit(ctx context.Context, requested, final string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	audit, _ := ReasoningEffortAuditFromContext(ctx)
	if strings.TrimSpace(requested) != "" {
		audit.Requested = strings.TrimSpace(requested)
	}
	if strings.TrimSpace(final) != "" {
		audit.Final = strings.TrimSpace(final)
	}
	ctx = context.WithValue(ctx, reasoningEffortAuditContextKey{}, audit)
	if meta, ok := RequestMetaFromContext(ctx); ok {
		if audit.Requested != "" {
			meta.RequestedReasoningEffort = audit.Requested
		}
		if audit.Final != "" {
			meta.ReasoningEffort = audit.Final
		}
		ctx = ContextWithRequestMeta(ctx, meta)
	}
	return ctx
}

func ReasoningEffortAuditFromContext(ctx context.Context) (ReasoningEffortAudit, bool) {
	if ctx == nil {
		return ReasoningEffortAudit{}, false
	}
	audit, ok := ctx.Value(reasoningEffortAuditContextKey{}).(ReasoningEffortAudit)
	if !ok {
		return ReasoningEffortAudit{}, false
	}
	return audit, audit.Requested != "" || audit.Final != ""
}

// CanonicalRequestIDFromContext returns the billing and usage identity shared
// by the regional quota reservation, the local usage log, and the control
// plane report. RequestMeta is authoritative once the gateway-lite auth
// middleware has created a reservation; the prefixed context fallbacks keep
// older and non-gateway-lite call paths collision-safe.
func CanonicalRequestIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if meta, ok := RequestMetaFromContext(ctx); ok {
		if requestID := strings.TrimSpace(meta.RequestID); requestID != "" {
			return requestID, true
		}
	}
	if clientRequestID, _ := ctx.Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(clientRequestID) != "" {
		return "client:" + strings.TrimSpace(clientRequestID), true
	}
	if requestID, _ := ctx.Value(ctxkey.RequestID).(string); strings.TrimSpace(requestID) != "" {
		return "local:" + strings.TrimSpace(requestID), true
	}
	return "", false
}

// RequestReservation is shared by the auth middleware and the downstream
// handler. Media handlers may learn the real upper-bound cost only after the
// request body has been parsed, so the original one-cent reservation must be
// expandable before any upstream work is accepted.
type RequestReservation struct {
	mu             sync.RWMutex
	estimatedCents int64
	leaseID        string
	forceRefund    bool
	// A WebSocket turn has no HTTP finalizer; its usage transaction must also
	// commit the original balance hold. Ordinary HTTP reservations keep theirs.
	finalizeOnUsage bool
}

// SalesRateReservationCents turns the frozen effective sales multiplier into
// the request's initial lease placeholder. One cent is deliberately retained
// for a free (0x) policy: it serializes the request against the regional lease,
// while final settlement releases that placeholder when no billable cost was
// produced. Account/upstream cost multipliers must never be passed here.
func SalesRateReservationCents(effectiveRateMultiplier float64) int64 {
	if effectiveRateMultiplier <= 0 || math.IsNaN(effectiveRateMultiplier) {
		return 1
	}
	if math.IsInf(effectiveRateMultiplier, 1) || effectiveRateMultiplier >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	estimatedCents := int64(math.Ceil(effectiveRateMultiplier))
	if estimatedCents < 1 {
		return 1
	}
	return estimatedCents
}

func NewRequestReservation(estimatedCents int64, leaseID string) *RequestReservation {
	if estimatedCents < 0 {
		estimatedCents = 0
	}
	return &RequestReservation{
		estimatedCents: estimatedCents,
		leaseID:        strings.TrimSpace(leaseID),
	}
}

func NewTurnRequestReservation(estimatedCents int64, leaseID string) *RequestReservation {
	r := NewRequestReservation(estimatedCents, leaseID)
	r.finalizeOnUsage = true
	return r
}

func (r *RequestReservation) FinalizesOnUsage() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.finalizeOnUsage
}

func (r *RequestReservation) Snapshot() (estimatedCents int64, leaseID string, forceRefund bool) {
	if r == nil {
		return 0, "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.estimatedCents, r.leaseID, r.forceRefund
}

func (r *RequestReservation) update(estimatedCents int64, leaseID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if estimatedCents >= 0 {
		r.estimatedCents = estimatedCents
	}
	if leaseID = strings.TrimSpace(leaseID); leaseID != "" {
		r.leaseID = leaseID
	}
}

func (r *RequestReservation) markForRefund() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.forceRefund = true
	r.mu.Unlock()
}

var (
	ErrRequestQuotaInsufficient = errors.New("request quota is insufficient")
	ErrRequestQuotaUnavailable  = errors.New("request quota service is unavailable")
)

var defaultUsageReporter atomic.Value
var defaultControlPlaneClientRef atomic.Value
var defaultConfigSyncer atomic.Value
var defaultConfigChangeNotifier atomic.Value
var defaultRuntimeHealthMonitor atomic.Value
var defaultRuntimeConfigRef atomic.Value
var defaultRuntimeHealthSettings atomic.Value
var defaultRedisQuota atomic.Value
var defaultRedisKeyCache atomic.Value
var defaultRedisConfigCache atomic.Value
var defaultCacheInvalidationSyncer atomic.Value

const actualCostMicroCentsScale int64 = 1_000_000

const (
	actualCostLocalSettlementTTL        = 24 * time.Hour
	actualCostLocalSettlementMaxEntries = 100_000
)

type actualCostLocalSettlementResult struct {
	cents      int64
	microCents int64
	expiresAt  int64
}

var actualCostLocalRemainders = struct {
	sync.Mutex
	values        map[string]int64
	settlements   map[string]actualCostLocalSettlementResult
	nextSweepUnix int64
}{
	values:      map[string]int64{},
	settlements: map[string]actualCostLocalSettlementResult{},
}

func ContextWithRequestMeta(ctx context.Context, meta RequestMeta) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if meta.GatewayID == "" {
		meta.GatewayID = meta.Region
	}
	if meta.RatePolicyAudit != nil && meta.RatePolicyAudit.Valid() {
		policy := meta.RatePolicyAudit.Clone()
		meta.RatePolicyAudit = &policy
		meta.RateMultiplier = policy.EffectiveRateMultiplier
	} else {
		meta.RatePolicyAudit = nil
	}
	// 显式 0 用户倍率是合法免费策略；只有没有有效冻结审计时，0 才表示旧调用方未填写。
	if meta.RatePolicyAudit == nil && (meta.RateMultiplier <= 0 || math.IsNaN(meta.RateMultiplier) || math.IsInf(meta.RateMultiplier, 0)) {
		meta.RateMultiplier = 1
	}
	if meta.AccountRateMultiplier <= 0 {
		meta.AccountRateMultiplier = 1
	}
	if meta.OriginalGroupID <= 0 {
		meta.OriginalGroupID = meta.GroupID
	}
	if strings.TrimSpace(meta.OriginalGroupName) == "" {
		meta.OriginalGroupName = meta.GroupName
	}
	meta.DetectedLanguages = append([]string(nil), meta.DetectedLanguages...)
	ctx = ContextWithIngressGroup(ctx, meta.OriginalGroupID)
	return context.WithValue(ctx, requestMetaContextKey, meta)
}

// ContextWithRatePolicyAudit replaces only the immutable request billing
// evidence. It is used once after authentication and again only when a keyword
// policy intentionally reroutes this request before upstream dispatch.
func ContextWithRatePolicyAudit(ctx context.Context, policy RatePolicyAudit) context.Context {
	if ctx == nil || !policy.Valid() {
		return ctx
	}
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return ctx
	}
	cloned := policy.Clone()
	meta.RatePolicyAudit = &cloned
	meta.RateMultiplier = cloned.EffectiveRateMultiplier
	return ContextWithRequestMeta(ctx, meta)
}

// ContextWithEffectiveGroup records a request-scoped group switch that happened
// after admission (for example an invalid-request fallback). The API key cache
// remains bound to the requested group; only the usage/diagnostic evidence uses
// the group that actually handled this request.
func ContextWithEffectiveGroup(ctx context.Context, effectiveGroupID int64, effectiveGroupName string) context.Context {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || effectiveGroupID <= 0 {
		return ctx
	}
	meta.GroupID = effectiveGroupID
	meta.GroupName = strings.TrimSpace(effectiveGroupName)
	return ContextWithRequestMeta(ctx, meta)
}

// ContextWithKeywordPolicyDecision replaces only request-scoped routing evidence.
// It never changes the API Key cache or database binding, so the next request starts
// from the original group again.
func ContextWithKeywordPolicyDecision(ctx context.Context, effectiveGroupID int64, effectiveGroupName, source string, languages []string, rerouted bool, reason string) context.Context {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return ctx
	}
	if effectiveGroupID > 0 {
		meta.GroupID = effectiveGroupID
		meta.GroupName = strings.TrimSpace(effectiveGroupName)
	}
	meta.KeywordPolicyMatched = true
	meta.KeywordPolicyRerouted = rerouted
	meta.KeywordPolicySource = strings.TrimSpace(source)
	meta.KeywordPolicyReason = strings.TrimSpace(reason)
	meta.DetectedLanguages = append([]string(nil), languages...)
	return ContextWithRequestMeta(ctx, meta)
}

func RequestMetaFromContext(ctx context.Context) (RequestMeta, bool) {
	if ctx == nil {
		return RequestMeta{}, false
	}
	meta, ok := ctx.Value(requestMetaContextKey).(RequestMeta)
	if !ok || strings.TrimSpace(meta.RequestID) == "" || meta.UserID <= 0 {
		return RequestMeta{}, false
	}
	if meta.Reservation != nil {
		_, leaseID, _ := meta.Reservation.Snapshot()
		if leaseID != "" {
			meta.LeaseID = leaseID
		}
	}
	return meta, true
}

// EnsureRequestReservation expands the current gateway-lite reservation to
// the requested upper bound. The resize happens before contacting an upstream,
// otherwise a keyword reroute or accepted async task could consume more than
// the request-start reservation after other requests exhaust the same lease.
func EnsureRequestReservation(ctx context.Context, estimatedCents int64) (int64, error) {
	if estimatedCents <= 0 {
		return 0, errors.New("estimated_cents must be positive")
	}
	meta, ok := RequestMetaFromContext(ctx)
	if ok && meta.Funding != nil {
		if estimatedCents > maxFundingExactInteger/actualCostMicroCentsScale {
			return 0, ErrRequestQuotaInsufficient
		}
		if err := EnsureFundingUpperBound(ctx, estimatedCents*actualCostMicroCentsScale, "existing_media_maximum_cents"); err != nil {
			return 0, err
		}
		return estimatedCents, nil
	}
	if !ok || meta.Reservation == nil || strings.TrimSpace(meta.LeaseID) == "" {
		// Full sub2api mode does not use the gateway-lite regional lease. The
		// existing local billing eligibility path remains authoritative there.
		return estimatedCents, nil
	}
	current, currentLeaseID, forceRefund := meta.Reservation.Snapshot()
	if forceRefund {
		return 0, ErrRequestQuotaUnavailable
	}
	if estimatedCents <= current {
		return current, nil
	}

	quota := DefaultRedisQuota()
	if quota == nil || !quota.Enabled() {
		return 0, ErrRequestQuotaUnavailable
	}
	if runtimeRef := DefaultRuntimeConfigRef(); runtimeRef != nil {
		quota = quota.WithPrefix(runtimeRef.Current().RedisPrefix)
	}
	resized, err := quota.ResizeReservation(ctx, meta.RequestID, estimatedCents)
	if err != nil {
		return 0, err
	}
	if resized {
		meta.Reservation.update(estimatedCents, currentLeaseID)
		return estimatedCents, nil
	}

	control := DefaultControlPlaneClientRef()
	if control == nil || !control.Configured() {
		return 0, ErrRequestQuotaUnavailable
	}
	subject := LeaseSubject{UserID: meta.UserID, OrganizationID: meta.OrganizationID, BillingSubjectType: meta.BillingSubjectType, BillingSubjectID: meta.BillingSubjectID}
	observation, err := loadLeaseObservationForSubject(ctx, quota, subject, meta.Region, true)
	if err != nil {
		return 0, errors.Join(ErrRequestQuotaUnavailable, err)
	}
	response, err := control.RebalanceLease(ctx, RebalanceLeaseRequest{
		KeyID:              meta.KeyID,
		UserID:             meta.UserID,
		OrganizationID:     meta.OrganizationID,
		BillingSubjectType: meta.BillingSubjectType,
		BillingSubjectID:   meta.BillingSubjectID,
		Region:             meta.Region,
		GatewayCode:        meta.GatewayID,
		EstimatedCostCents: estimatedCents,
		Reason:             "request_reservation_resize",
		LeaseObservation:   observation,
	})
	if err != nil {
		return 0, errors.Join(ErrRequestQuotaUnavailable, err)
	}
	if response == nil || !response.OK {
		if response != nil {
			switch strings.TrimSpace(response.Error) {
			case "usage_limit_exceeded", "insufficient_balance", "insufficient_quota":
				return 0, ErrRequestQuotaInsufficient
			}
		}
		return 0, ErrRequestQuotaUnavailable
	}
	lease := response.Lease
	if lease.UserID == 0 {
		lease.UserID = meta.UserID
	}
	if lease.OrganizationID == 0 {
		lease.OrganizationID = meta.OrganizationID
		lease.BillingSubjectType = meta.BillingSubjectType
		lease.BillingSubjectID = meta.BillingSubjectID
	}
	if strings.TrimSpace(lease.Region) == "" {
		lease.Region = meta.Region
	}
	if err := quota.EnsureLease(ctx, lease); err != nil {
		return 0, errors.Join(ErrRequestQuotaUnavailable, err)
	}
	resized, err = quota.ResizeReservation(ctx, meta.RequestID, estimatedCents)
	if err != nil {
		return 0, err
	}
	if !resized {
		return 0, ErrRequestQuotaUnavailable
	}
	meta.Reservation.update(estimatedCents, lease.LeaseID)
	return estimatedCents, nil
}

// MarkRequestReservationForRefund handles the narrow case where an async
// upstream accepted work but the durable task record could not be saved. The
// middleware must not leave the enlarged local lease amount committed forever.
func MarkRequestReservationForRefund(ctx context.Context) {
	meta, ok := RequestMetaFromContext(ctx)
	if ok && meta.Reservation != nil {
		meta.Reservation.markForRefund()
	}
}

// ContextWithUpstreamConsumed freezes the fact that billable upstream work has
// completed. The HTTP finalizer uses it only to preserve billing on a later
// local policy rejection; it does not turn transport failures into successes.
func ContextWithUpstreamConsumed(ctx context.Context) context.Context {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return ctx
	}
	meta.UpstreamConsumed = true
	return ContextWithRequestMeta(ctx, meta)
}

// ContextWithActualCentsOverride carries a previously rounded cent amount into
// the usage reporter. Async settlement must round once before capacity checks;
// converting the same USD value again would consume the sub-cent accumulator a
// second time and eventually overcharge.
func ContextWithActualCentsOverride(ctx context.Context, actualCents int64) context.Context {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return ctx
	}
	value := actualCents
	meta.ActualCentsOverride = &value
	return ContextWithRequestMeta(ctx, meta)
}

// ContextWithAsyncSettlement tells the HTTP admission middleware to leave the
// resized reservation open. A durable worker will later commit the actual
// image/video cost or release it on failure; the 202 submit response itself is
// not a billable model result.
func ContextWithAsyncSettlement(ctx context.Context) context.Context {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return ctx
	}
	meta.AsyncSettlement = true
	return ContextWithRequestMeta(ctx, meta)
}

// ContextWithUncertainAsyncPersistence prevents the HTTP finalizer from
// refunding a reservation whose matching durable task may already exist.
func ContextWithUncertainAsyncPersistence(ctx context.Context) context.Context {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return ctx
	}
	meta.AsyncSettlement = true
	meta.AsyncPersistenceUncertain = true
	return ContextWithRequestMeta(ctx, meta)
}

// EnsureCommittedUsageCapacity applies the final async cost to the local lease
// before the durable correction is queued for the control plane. Positive
// deltas cannot overdraw the regional lease; negative deltas release capacity
// immediately. A missing Redis request record is tolerated because the control
// plane's usage event and ledger are durable and remain authoritative.
func EnsureCommittedUsageCapacity(ctx context.Context, actualCents int64) error {
	if actualCents < 0 {
		return errors.New("actual_cents cannot be negative")
	}
	meta, ok := RequestMetaFromContext(ctx)
	if ok && meta.Funding != nil {
		// Actual usage is queued against the original authorization. It must not
		// resize a balance lease or acquire more money after upstream execution.
		return nil
	}
	if !ok || !meta.AsyncSettlement || strings.TrimSpace(meta.RequestID) == "" {
		return nil
	}
	quota := DefaultRedisQuota()
	if quota == nil || !quota.Enabled() {
		return ErrRequestQuotaUnavailable
	}
	if runtimeRef := DefaultRuntimeConfigRef(); runtimeRef != nil {
		quota = quota.WithPrefix(runtimeRef.Current().RedisPrefix)
	}
	result, err := quota.EnsureCommittedCost(ctx, meta.RequestID, actualCents)
	if err != nil {
		return err
	}
	if result == RequestCostAdjusted || result == RequestCostMissing {
		return nil
	}
	if actualCents == 0 {
		return ErrRequestQuotaUnavailable
	}

	control := DefaultControlPlaneClientRef()
	if control == nil || !control.Configured() {
		return ErrRequestQuotaUnavailable
	}
	subject := LeaseSubject{UserID: meta.UserID, OrganizationID: meta.OrganizationID, BillingSubjectType: meta.BillingSubjectType, BillingSubjectID: meta.BillingSubjectID}
	observation, err := loadLeaseObservationForSubject(ctx, quota, subject, meta.Region, true)
	if err != nil {
		return errors.Join(ErrRequestQuotaUnavailable, err)
	}
	response, err := control.RebalanceLease(ctx, RebalanceLeaseRequest{
		KeyID:              meta.KeyID,
		UserID:             meta.UserID,
		OrganizationID:     meta.OrganizationID,
		BillingSubjectType: meta.BillingSubjectType,
		BillingSubjectID:   meta.BillingSubjectID,
		Region:             meta.Region,
		GatewayCode:        meta.GatewayID,
		EstimatedCostCents: actualCents,
		Reason:             "async_media_final_cost_adjustment",
		LeaseObservation:   observation,
	})
	if err != nil {
		return errors.Join(ErrRequestQuotaUnavailable, err)
	}
	if response == nil || !response.OK {
		if response != nil {
			switch strings.TrimSpace(response.Error) {
			case "usage_limit_exceeded", "insufficient_balance", "insufficient_quota":
				return ErrRequestQuotaInsufficient
			}
		}
		return ErrRequestQuotaUnavailable
	}
	lease := response.Lease
	if lease.UserID == 0 {
		lease.UserID = meta.UserID
	}
	if lease.OrganizationID == 0 {
		lease.OrganizationID = meta.OrganizationID
		lease.BillingSubjectType = meta.BillingSubjectType
		lease.BillingSubjectID = meta.BillingSubjectID
	}
	if strings.TrimSpace(lease.Region) == "" {
		lease.Region = meta.Region
	}
	if err := quota.EnsureLease(ctx, lease); err != nil {
		return errors.Join(ErrRequestQuotaUnavailable, err)
	}
	result, err = quota.EnsureCommittedCost(ctx, meta.RequestID, actualCents)
	if err != nil {
		return err
	}
	if result == RequestCostAdjusted || result == RequestCostMissing {
		return nil
	}
	return ErrRequestQuotaUnavailable
}

func loadLeaseObservation(ctx context.Context, quota *RedisQuota, userID int64, region string, localExhausted bool) (LeaseObservation, error) {
	return loadLeaseObservationForSubject(ctx, quota, LeaseSubject{UserID: userID}, region, localExhausted)
}

func loadLeaseObservationForSubject(ctx context.Context, quota *RedisQuota, subject LeaseSubject, region string, localExhausted bool) (LeaseObservation, error) {
	if quota == nil || !quota.Enabled() {
		return LeaseObservation{}, nil
	}
	lease, ok, err := quota.LoadLeaseForSubject(ctx, subject, region)
	if err != nil || !ok {
		return LeaseObservation{}, err
	}
	return lease.Observation(localExhausted), nil
}

type usageReporterValue struct {
	reporter UsageReportClient
}

func SetDefaultUsageReporter(reporter UsageReportClient) {
	// Store a non-nil wrapper so tests and controlled shutdown paths can clear
	// the optional reporter without triggering atomic.Value's nil panic.
	defaultUsageReporter.Store(usageReporterValue{reporter: reporter})
}

func DefaultUsageReporter() UsageReportClient {
	value := defaultUsageReporter.Load()
	if value == nil {
		return nil
	}
	if holder, ok := value.(usageReporterValue); ok {
		return holder.reporter
	}
	// Backward-compatible in-process fallback for callers initialized before a
	// hot configuration refresh in older builds.
	reporter, _ := value.(UsageReportClient)
	return reporter
}

type ControlPlaneClientRef struct {
	client atomic.Pointer[ControlPlaneClient]
}

func NewControlPlaneClientRef(client *ControlPlaneClient) *ControlPlaneClientRef {
	ref := &ControlPlaneClientRef{}
	ref.Set(client)
	return ref
}

func (r *ControlPlaneClientRef) Set(client *ControlPlaneClient) {
	if r != nil && client != nil {
		r.client.Store(client)
	}
}

func (r *ControlPlaneClientRef) Configured() bool {
	if r == nil {
		return false
	}
	return r.client.Load() != nil
}

func (r *ControlPlaneClientRef) current() (*ControlPlaneClient, error) {
	if r == nil {
		return nil, errors.New("control plane client is not configured")
	}
	client := r.client.Load()
	if client == nil {
		return nil, errors.New("control plane client is not configured")
	}
	return client, nil
}

func (r *ControlPlaneClientRef) ResolveKey(ctx context.Context, req ResolveKeyRequest) (*ResolveKeyResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.ResolveKey(ctx, req)
}

func (r *ControlPlaneClientRef) AcquireLease(ctx context.Context, req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.AcquireLease(ctx, req)
}

func (r *ControlPlaneClientRef) RefillLease(ctx context.Context, req AcquireLeaseRequest) (*AcquireLeaseResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.RefillLease(ctx, req)
}

func (r *ControlPlaneClientRef) RebalanceLease(ctx context.Context, req RebalanceLeaseRequest) (*RebalanceLeaseResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.RebalanceLease(ctx, req)
}

func (r *ControlPlaneClientRef) ReportUsage(ctx context.Context, event UsageEvent) error {
	client, err := r.current()
	if err != nil {
		return err
	}
	return client.ReportUsage(ctx, event)
}

func (r *ControlPlaneClientRef) ReportUsageBatch(ctx context.Context, events []UsageEvent) error {
	client, err := r.current()
	if err != nil {
		return err
	}
	return client.ReportUsageBatch(ctx, events)
}

func (r *ControlPlaneClientRef) ReportContentModerationPenalty(ctx context.Context, req ContentModerationPenaltyRequest) (*ContentModerationPenaltyResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.ReportContentModerationPenalty(ctx, req)
}

func (r *ControlPlaneClientRef) ReportGatewayHealth(ctx context.Context, req GatewayHealthReportRequest) (*GatewayHealthReportResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.ReportGatewayHealth(ctx, req)
}

func (r *ControlPlaneClientRef) ReportGatewayConfigChanged(ctx context.Context, req GatewayConfigChangedRequest) (*GatewayConfigChangedResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.ReportGatewayConfigChanged(ctx, req)
}

func (r *ControlPlaneClientRef) ReportGatewayConfigPush(ctx context.Context, req GatewayConfigPushRequest) (*GatewayConfigPushResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.ReportGatewayConfigPush(ctx, req)
}

func (r *ControlPlaneClientRef) ProbeControlPlaneHealth(ctx context.Context) (time.Duration, error) {
	client, err := r.current()
	if err != nil {
		return 0, err
	}
	return client.ProbeControlPlaneHealth(ctx)
}

func (r *ControlPlaneClientRef) FetchGatewayConfigSnapshot(ctx context.Context, req GatewayConfigSnapshotRequest) (*GatewayConfigSnapshotResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.FetchGatewayConfigSnapshot(ctx, req)
}

func (r *ControlPlaneClientRef) FetchCacheInvalidations(ctx context.Context, req CacheInvalidationRequest) (*CacheInvalidationResponse, error) {
	client, err := r.current()
	if err != nil {
		return nil, err
	}
	return client.FetchCacheInvalidations(ctx, req)
}

func SetDefaultControlPlaneClientRef(ref *ControlPlaneClientRef) {
	defaultControlPlaneClientRef.Store(ref)
}

func DefaultControlPlaneClientRef() *ControlPlaneClientRef {
	value := defaultControlPlaneClientRef.Load()
	if value == nil {
		return nil
	}
	ref, _ := value.(*ControlPlaneClientRef)
	return ref
}

func SetDefaultConfigSyncer(syncer *ConfigSyncer) {
	defaultConfigSyncer.Store(syncer)
}

func DefaultConfigSyncer() *ConfigSyncer {
	value := defaultConfigSyncer.Load()
	if value == nil {
		return nil
	}
	syncer, _ := value.(*ConfigSyncer)
	return syncer
}

func SetDefaultConfigChangeNotifier(notifier *ConfigChangeNotifier) {
	defaultConfigChangeNotifier.Store(notifier)
}

func DefaultConfigChangeNotifier() *ConfigChangeNotifier {
	value := defaultConfigChangeNotifier.Load()
	if value == nil {
		return nil
	}
	notifier, _ := value.(*ConfigChangeNotifier)
	return notifier
}

func NotifyDefaultConfigChanged(resource, action string, resourceID int64) {
	if notifier := DefaultConfigChangeNotifier(); notifier != nil {
		notifier.Notify(resource, action, resourceID)
	}
}

func NotifyDefaultFullConfigSnapshot(resource, action string, resourceID int64) {
	if notifier := DefaultConfigChangeNotifier(); notifier != nil {
		notifier.NotifyFullSnapshot(resource, action, resourceID)
	}
}

func SetDefaultRuntimeHealthMonitor(monitor *RuntimeHealthMonitor) {
	defaultRuntimeHealthMonitor.Store(monitor)
}

func DefaultRuntimeHealthMonitor() *RuntimeHealthMonitor {
	value := defaultRuntimeHealthMonitor.Load()
	if value == nil {
		return nil
	}
	monitor, _ := value.(*RuntimeHealthMonitor)
	return monitor
}

func SetDefaultRuntimeConfigRef(ref *RuntimeConfigRef) {
	defaultRuntimeConfigRef.Store(ref)
}

func DefaultRuntimeConfigRef() *RuntimeConfigRef {
	value := defaultRuntimeConfigRef.Load()
	if value == nil {
		return nil
	}
	ref, _ := value.(*RuntimeConfigRef)
	return ref
}

func SetDefaultRuntimeHealthSettings(settings *RuntimeHealthSettings) {
	defaultRuntimeHealthSettings.Store(settings)
}

func DefaultRuntimeHealthSettings() *RuntimeHealthSettings {
	value := defaultRuntimeHealthSettings.Load()
	if value == nil {
		return nil
	}
	settings, _ := value.(*RuntimeHealthSettings)
	return settings
}

func SetDefaultRedisQuota(quota *RedisQuota) {
	defaultRedisQuota.Store(quota)
}

func DefaultRedisQuota() *RedisQuota {
	value := defaultRedisQuota.Load()
	if value == nil {
		return nil
	}
	quota, _ := value.(*RedisQuota)
	return quota
}

func SetDefaultRedisKeyCache(cache *RedisKeyCache) {
	defaultRedisKeyCache.Store(cache)
}

func DefaultRedisKeyCache() *RedisKeyCache {
	value := defaultRedisKeyCache.Load()
	if value == nil {
		return nil
	}
	cache, _ := value.(*RedisKeyCache)
	return cache
}

func SetDefaultRedisConfigCache(cache *RedisConfigCache) {
	defaultRedisConfigCache.Store(cache)
}

func DefaultRedisConfigCache() *RedisConfigCache {
	value := defaultRedisConfigCache.Load()
	if value == nil {
		return nil
	}
	cache, _ := value.(*RedisConfigCache)
	return cache
}

func SetDefaultCacheInvalidationSyncer(syncer *CacheInvalidationSyncer) {
	defaultCacheInvalidationSyncer.Store(syncer)
}

func DefaultCacheInvalidationSyncer() *CacheInvalidationSyncer {
	value := defaultCacheInvalidationSyncer.Load()
	if value == nil {
		return nil
	}
	syncer, _ := value.(*CacheInvalidationSyncer)
	return syncer
}

// ActualCostToCentsForContext is the only USD-to-cent settlement entry. It
// first rounds to one-millionth of a cent, then carries the sub-cent remainder
// per API key. This avoids both per-request ceiling overcharge and long-term
// loss from repeatedly truncating small requests. idempotent_v2 callers use
// PrepareActualCostSettlement instead, because financial idempotency must be
// completed in the same Redis transaction that persists the usage event.
func ActualCostToCentsForContext(ctx context.Context, cost float64) int64 {
	deltaMicroCents := actualCostUSDToMicroCents(cost)
	if HasRequestFunding(ctx) {
		return deltaMicroCents / actualCostMicroCentsScale
	}
	if deltaMicroCents <= 0 {
		return 0
	}
	key := actualCostAccumulatorKey(ctx)
	if quota := DefaultRedisQuota(); quota != nil && quota.Enabled() {
		if cents, err := quota.AccumulateActualCostMicroCents(ctx, key, deltaMicroCents, actualCostMicroCentsScale); err == nil {
			return cents
		}
	}
	return accumulateActualCostMicroCentsLocal(key, deltaMicroCents, actualCostMicroCentsScale)
}

// ActualCostMicroCents converts a USD amount to the fixed-point unit used by
// the settlement scripts. Keeping floats outside Redis avoids two callers
// rounding the same request differently.
func ActualCostMicroCents(cost float64) int64 {
	return actualCostUSDToMicroCents(cost)
}

// ActualCostAccumulatorScope returns the per-key carry scope without exposing
// the raw API key. Region, public key ID and user ID together keep independent
// gateways and users from sharing a fractional-cent remainder.
func ActualCostAccumulatorScope(ctx context.Context) string {
	return actualCostAccumulatorKey(ctx)
}

// PrepareActualCostSettlement is used when an asynchronous job needs the
// integer-cent result before it can adjust its quota reservation. With Redis,
// the result is stored inside the existing request hash; an ambiguous timeout
// is returned to the caller and retried against the same hash, never against an
// independent local remainder bucket.
func PrepareActualCostSettlement(ctx context.Context, requestID string, cost float64) (int64, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, errors.New("request_id is required for idempotent settlement")
	}
	deltaMicroCents := actualCostUSDToMicroCents(cost)
	if HasRequestFunding(ctx) {
		return deltaMicroCents / actualCostMicroCentsScale, nil
	}
	scope := actualCostAccumulatorKey(ctx)
	if quota := DefaultRedisQuota(); quota != nil && quota.Enabled() {
		return quota.AccumulateActualCostMicroCentsIdempotent(ctx, scope, requestID, deltaMicroCents, actualCostMicroCentsScale)
	}
	return settleActualCostMicroCentsLocalIdempotent(scope, requestID, deltaMicroCents, actualCostMicroCentsScale, time.Now())
}

func actualCostUSDToMicroCents(cost float64) int64 {
	if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0
	}
	scaled := cost * 100 * float64(actualCostMicroCentsScale)
	maxInt64 := int64(^uint64(0) >> 1)
	if scaled >= float64(maxInt64) {
		return maxInt64
	}
	return int64(math.Round(scaled))
}

func actualCostAccumulatorKey(ctx context.Context) string {
	meta, ok := RequestMetaFromContext(ctx)
	if !ok {
		return "global"
	}
	parts := []string{
		strings.TrimSpace(meta.Region),
		strings.TrimSpace(meta.KeyID),
	}
	if meta.UserID > 0 {
		parts = append(parts, strconvFormatInt(meta.UserID))
	}
	return strings.Join(parts, ":")
}

func accumulateActualCostMicroCentsLocal(key string, deltaMicroCents, scale int64) int64 {
	if key == "" {
		key = "global"
	}
	if scale <= 0 || deltaMicroCents <= 0 {
		return 0
	}
	actualCostLocalRemainders.Lock()
	defer actualCostLocalRemainders.Unlock()
	total := actualCostLocalRemainders.values[key] + deltaMicroCents
	cents := total / scale
	actualCostLocalRemainders.values[key] = total % scale
	return cents
}

func actualCostLocalSettlementKey(scope, requestID string) string {
	return scope + "\x00" + requestID
}

func settleActualCostMicroCentsLocalIdempotent(scope, requestID string, deltaMicroCents, scale int64, now time.Time) (int64, error) {
	if scope == "" {
		scope = "global"
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, errors.New("request_id is required for local idempotent settlement")
	}
	if scale <= 0 {
		scale = actualCostMicroCentsScale
	}
	if deltaMicroCents < 0 {
		deltaMicroCents = 0
	}
	settlementKey := actualCostLocalSettlementKey(scope, requestID)
	actualCostLocalRemainders.Lock()
	defer actualCostLocalRemainders.Unlock()
	if result, ok := actualCostLocalRemainders.settlements[settlementKey]; ok && result.expiresAt > now.Unix() {
		if deltaMicroCents > 0 && result.microCents != deltaMicroCents {
			return 0, errors.New("settlement conflict: request already has different actual cost")
		}
		return result.cents, nil
	}
	sweepActualCostLocalSettlementsLocked(now)
	// When Redis is absent this map is the only financial idempotency boundary.
	// Capacity exhaustion must fail closed instead of forgetting a live request
	// and charging its retry a second time.
	if len(actualCostLocalRemainders.settlements) >= actualCostLocalSettlementMaxEntries {
		return 0, errors.New("local idempotent settlement capacity exhausted")
	}
	total := actualCostLocalRemainders.values[scope] + deltaMicroCents
	cents := total / scale
	actualCostLocalRemainders.values[scope] = total % scale
	actualCostLocalRemainders.settlements[settlementKey] = actualCostLocalSettlementResult{
		cents:      cents,
		microCents: deltaMicroCents,
		expiresAt:  now.Add(actualCostLocalSettlementTTL).Unix(),
	}
	return cents, nil
}

func sweepActualCostLocalSettlementsLocked(now time.Time) {
	nowUnix := now.Unix()
	if len(actualCostLocalRemainders.settlements) < actualCostLocalSettlementMaxEntries && nowUnix < actualCostLocalRemainders.nextSweepUnix {
		return
	}
	for key, result := range actualCostLocalRemainders.settlements {
		if result.expiresAt <= nowUnix {
			delete(actualCostLocalRemainders.settlements, key)
		}
	}
	actualCostLocalRemainders.nextSweepUnix = now.Add(time.Minute).Unix()
}

func strconvFormatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	negative := value < 0
	if negative {
		value = -value
	}
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ReportUsageCorrection(ctx context.Context, event UsageEvent) error {
	reporter := DefaultUsageReporter()
	meta, ok := RequestMetaFromContext(ctx)
	if event.CommitAsyncReservation || (ok && meta.CommitAsyncReservation) {
		if !ok || !meta.CommitAsyncReservation || !meta.AsyncSettlement || !event.CommitAsyncReservation ||
			meta.Funding != nil || meta.Reservation != nil || event.RequestID != meta.RequestID ||
			event.QuotaRequestID != meta.RequestID || event.KeyID != meta.KeyID || event.UserID != meta.UserID ||
			event.OrganizationID != meta.OrganizationID || event.BillingSubjectType != meta.BillingSubjectType ||
			event.BillingSubjectID != meta.BillingSubjectID || event.LeaseID != meta.LeaseID || event.Region != meta.Region ||
			event.GatewayID != meta.GatewayID {
			return errors.New("SETTLEMENT_CONFLICT async completion lost its original identity")
		}
		mode := meta.BillingSettlementMode
		if mode == "" {
			mode = BillingSettlementModeLegacy
		}
		if event.SettlementMode != string(mode) {
			return errors.New("SETTLEMENT_CONFLICT async completion changed its frozen mode")
		}
		atomicReporter, supported := reporter.(AtomicAsyncReservationReporter)
		if !supported || !atomicReporter.SupportsAtomicAsyncReservation() {
			return ErrRequestQuotaUnavailable
		}
		if err := validateAsyncReservationEvent(event); err != nil {
			return err
		}
	}
	if reporter == nil {
		if HasRequestFunding(ctx) {
			return ErrRequestQuotaUnavailable
		}
		return nil
	}
	if !ok {
		return nil
	}
	if event.RequestID == "" {
		event.RequestID = meta.RequestID
	}
	if event.QuotaRequestID == "" {
		event.QuotaRequestID = meta.RequestID
	}
	if event.UserID == 0 {
		event.UserID = meta.UserID
	}
	if event.KeyID == "" {
		event.KeyID = meta.KeyID
	}
	if event.LeaseID == "" {
		event.LeaseID = meta.LeaseID
	}
	if event.Region == "" {
		event.Region = meta.Region
	}
	if event.GatewayID == "" {
		event.GatewayID = meta.GatewayID
	}
	if event.GatewayGroupID == 0 {
		event.GatewayGroupID = meta.GroupID
	}
	if event.GatewayGroupName == "" {
		event.GatewayGroupName = meta.GroupName
	}
	if event.RequestedGatewayGroupID == 0 {
		event.RequestedGatewayGroupID = meta.OriginalGroupID
	}
	if event.RequestedGatewayGroupName == "" {
		event.RequestedGatewayGroupName = meta.OriginalGroupName
	}
	if !event.KeywordPolicyMatched {
		event.KeywordPolicyMatched = meta.KeywordPolicyMatched
	}
	if !event.KeywordPolicyRerouted {
		event.KeywordPolicyRerouted = meta.KeywordPolicyRerouted
	}
	if event.KeywordPolicySource == "" {
		event.KeywordPolicySource = meta.KeywordPolicySource
	}
	if event.KeywordPolicyReason == "" {
		event.KeywordPolicyReason = meta.KeywordPolicyReason
	}
	if len(event.DetectedLanguages) == 0 {
		event.DetectedLanguages = append([]string(nil), meta.DetectedLanguages...)
	}
	if event.RatePolicyAudit == nil && meta.RatePolicyAudit != nil && meta.RatePolicyAudit.Valid() {
		policy := meta.RatePolicyAudit.Clone()
		event.RatePolicyAudit = &policy
	}
	mediaRateSnapshot := (event.BillingMode == "image" || event.BillingMode == "video" ||
		event.BillingMode == "per_request") && (event.ImageCount > 0 || event.VideoCount > 0) &&
		finiteNonNegativeRate(event.RateMultiplier)
	if event.RatePolicyAudit != nil && event.RatePolicyAudit.Valid() && !mediaRateSnapshot {
		event.RateMultiplier = event.RatePolicyAudit.EffectiveRateMultiplier
	} else if event.RateMultiplier <= 0 || math.IsNaN(event.RateMultiplier) || math.IsInf(event.RateMultiplier, 0) {
		// An explicit zero image/video multiplier is valid and must not inherit the
		// Token multiplier. Other missing/invalid rates fall back to request-time
		// Token policy for legacy reporters.
		if !mediaRateSnapshot {
			event.RateMultiplier = meta.RateMultiplier
		}
	}
	if auth, funded := FundingFromContext(ctx); funded {
		micro := event.ActualCents * actualCostMicroCentsScale
		if event.ActualMicroCents != nil {
			micro = *event.ActualMicroCents
		} else if event.SettlementPending {
			micro = event.SettlementMicroCents
		}
		revision := event.SettlementRevision
		if revision <= 0 {
			revision = 2
			if micro == 0 {
				revision = 1
			}
		}
		event.OrganizationID = auth.OrganizationID
		event.BillingSubjectType = auth.BillingSubjectType
		event.BillingSubjectID = auth.BillingSubjectID
		AttachFundingUsage(&event, auth, micro, revision, true)
		if err := AttachOAuthInvocationUsage(ctx, &event); err != nil {
			return err
		}
	}
	// Do not synthesize an account multiplier from request metadata. The account
	// is selected after authentication, so only the usage correction can provide
	// an authoritative procurement multiplier (including an explicit 0x).
	if event.SettlementPending && !event.CommitAsyncReservation {
		atomicReporter, ok := reporter.(AtomicUsageSettlementReporter)
		if !ok || !atomicReporter.SupportsAtomicUsageSettlement() {
			cents, err := settleActualCostMicroCentsLocalIdempotent(
				actualCostAccumulatorScopeForUsageEvent(event),
				event.RequestID,
				event.SettlementMicroCents,
				actualCostMicroCentsScale,
				time.Now(),
			)
			if err != nil {
				return err
			}
			event.ActualCents = cents
			event.SettlementPending = false
		}
	}
	return reporter.ReportUsage(ctx, event)
}
