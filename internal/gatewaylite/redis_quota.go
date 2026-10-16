package gatewaylite

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	reservationTTL      = 24 * time.Hour
	asyncReservationTTL = 72 * time.Hour
)

const (
	RequestCostMissing      = -1
	RequestCostInsufficient = 0
	RequestCostAdjusted     = 1
)

var ErrLeaseSnapshotConflict = errors.New("lease snapshot conflicts with active local lease")

type RedisQuota struct {
	client      *redis.Client
	prefixValue atomic.Value
}

type ReserveRequest struct {
	RequestID          string
	KeyID              string
	UserID             int64
	OrganizationID     int64
	BillingSubjectType string
	BillingSubjectID   int64
	Region             string
	LeaseID            string
	EstimatedCostCents int64
}

type UsageCommit struct {
	RequestID   string
	ActualCents int64
}

func NewRedisQuota(client *redis.Client, prefix string) *RedisQuota {
	quota := &RedisQuota{client: client}
	quota.SetPrefix(prefix)
	return quota
}

func (q *RedisQuota) Enabled() bool {
	return q != nil && q.client != nil
}

func (q *RedisQuota) WithPrefix(prefix string) *RedisQuota {
	if q == nil {
		return nil
	}
	return NewRedisQuota(q.client, prefix)
}

func (q *RedisQuota) EnsureLease(ctx context.Context, lease LeaseSnapshot) error {
	if !q.Enabled() {
		return nil
	}
	if lease.UserID <= 0 || lease.Region == "" || lease.LeaseID == "" ||
		lease.AllocatedCents < 0 || lease.ReservedCents < 0 || lease.SpentCents < 0 {
		return errors.New("invalid lease snapshot")
	}
	lease = normalizeLeaseSnapshotSubject(lease)
	key := q.leaseKeyForSubject(lease.Subject(), lease.Region)
	result, err := ensureLeaseScript.Run(ctx, q.client, []string{key},
		time.Now().Unix(),
		lease.LeaseID,
		lease.UserID,
		lease.Region,
		lease.AllocatedCents,
		lease.ReservedCents,
		lease.SpentCents,
		lease.Version,
		lease.ExpiresAt,
		lease.TargetCents,
		lease.RefillAtCents,
		int64(time.Hour.Seconds()),
		lease.OrganizationID,
		lease.BillingSubjectType,
		lease.BillingSubjectID,
	).Int()
	if err != nil {
		return err
	}
	if result < 0 {
		return ErrLeaseSnapshotConflict
	}
	return nil
}

func (q *RedisQuota) LoadLease(ctx context.Context, userID int64, region string) (LeaseSnapshot, bool, error) {
	return q.LoadLeaseForSubject(ctx, LeaseSubject{UserID: userID}, region)
}

func (q *RedisQuota) LoadLeaseForSubject(ctx context.Context, subject LeaseSubject, region string) (LeaseSnapshot, bool, error) {
	if !q.Enabled() {
		return LeaseSnapshot{}, false, nil
	}
	subject = normalizeLeaseSubject(subject)
	if subject.UserID <= 0 || region == "" {
		return LeaseSnapshot{}, false, errors.New("invalid lease lookup")
	}
	fields, err := q.client.HGetAll(ctx, q.leaseKeyForSubject(subject, region)).Result()
	if err != nil {
		return LeaseSnapshot{}, false, err
	}
	if len(fields) == 0 {
		return LeaseSnapshot{}, false, nil
	}
	lease := LeaseSnapshot{
		LeaseID:            fields["lease_id"],
		UserID:             ParseInt64Field(fields, "user_id"),
		OrganizationID:     ParseInt64Field(fields, "organization_id"),
		BillingSubjectType: fields["billing_subject_type"],
		BillingSubjectID:   ParseInt64Field(fields, "billing_subject_id"),
		Region:             fields["region"],
		AllocatedCents:     ParseInt64Field(fields, "allocated_cents"),
		ReservedCents:      ParseInt64Field(fields, "reserved_cents"),
		SpentCents:         ParseInt64Field(fields, "spent_cents"),
		Version:            ParseInt64Field(fields, "version"),
		ExpiresAt:          ParseInt64Field(fields, "expires_at"),
		TargetCents:        ParseInt64Field(fields, "target_cents"),
		RefillAtCents:      ParseInt64Field(fields, "refill_at_cents"),
	}
	if lease.UserID == 0 {
		lease.UserID = subject.UserID
	}
	if lease.OrganizationID == 0 && subject.OrganizationID > 0 {
		lease.OrganizationID = subject.OrganizationID
		lease.BillingSubjectType = subject.BillingSubjectType
		lease.BillingSubjectID = subject.BillingSubjectID
	}
	lease = normalizeLeaseSnapshotSubject(lease)
	if lease.Region == "" {
		lease.Region = region
	}
	if lease.LeaseID == "" {
		return LeaseSnapshot{}, false, nil
	}
	if lease.Expired(time.Now()) {
		return LeaseSnapshot{}, false, nil
	}
	return lease, true, nil
}

func (q *RedisQuota) Reserve(ctx context.Context, req ReserveRequest) (bool, error) {
	if !q.Enabled() {
		return true, nil
	}
	if req.RequestID == "" || req.UserID <= 0 || req.Region == "" || req.EstimatedCostCents <= 0 {
		return false, errors.New("invalid reserve request")
	}
	reqSubject := normalizeLeaseSubject(LeaseSubject{UserID: req.UserID, OrganizationID: req.OrganizationID, BillingSubjectType: req.BillingSubjectType, BillingSubjectID: req.BillingSubjectID})
	leaseKey := q.leaseKeyForSubject(reqSubject, req.Region)
	result, err := reserveScript.Run(ctx, q.client, []string{
		leaseKey,
		q.requestKey(req.RequestID),
	}, time.Now().Unix(), req.EstimatedCostCents, req.RequestID, req.KeyID, req.UserID, req.Region, req.LeaseID, leaseKey, int(reservationTTL.Seconds()), reqSubject.OrganizationID, reqSubject.BillingSubjectType, reqSubject.BillingSubjectID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// ReserveActiveLease atomically reads the current lease and creates the
// request reservation. The hot request path needs both operations, so keeping
// them in one Redis script avoids a second round trip without weakening the
// lease capacity check or request-id idempotency.
func (q *RedisQuota) ReserveActiveLease(ctx context.Context, req ReserveRequest) (LeaseSnapshot, bool, bool, error) {
	if !q.Enabled() {
		return LeaseSnapshot{}, false, false, nil
	}
	if req.RequestID == "" || req.UserID <= 0 || req.Region == "" || req.EstimatedCostCents <= 0 {
		return LeaseSnapshot{}, false, false, errors.New("invalid reserve request")
	}
	reqSubject := normalizeLeaseSubject(LeaseSubject{UserID: req.UserID, OrganizationID: req.OrganizationID, BillingSubjectType: req.BillingSubjectType, BillingSubjectID: req.BillingSubjectID})
	leaseKey := q.leaseKeyForSubject(reqSubject, req.Region)
	values, err := reserveActiveLeaseScript.Run(ctx, q.client, []string{
		leaseKey,
		q.requestKey(req.RequestID),
	}, time.Now().Unix(), req.EstimatedCostCents, req.RequestID, req.KeyID, req.UserID, req.Region, leaseKey, int(reservationTTL.Seconds()), reqSubject.OrganizationID, reqSubject.BillingSubjectType, reqSubject.BillingSubjectID).Slice()
	if err != nil {
		return LeaseSnapshot{}, false, false, err
	}
	if len(values) == 0 {
		return LeaseSnapshot{}, false, false, errors.New("invalid active lease reservation result")
	}
	result := redisScriptInt64(values[0])
	if result == 0 {
		return LeaseSnapshot{}, false, false, nil
	}
	if len(values) < 11 {
		return LeaseSnapshot{}, false, false, errors.New("incomplete active lease reservation result")
	}
	lease := LeaseSnapshot{
		LeaseID:        redisScriptString(values[1]),
		UserID:         redisScriptInt64(values[2]),
		Region:         redisScriptString(values[3]),
		AllocatedCents: redisScriptInt64(values[4]),
		ReservedCents:  redisScriptInt64(values[5]),
		SpentCents:     redisScriptInt64(values[6]),
		Version:        redisScriptInt64(values[7]),
		ExpiresAt:      redisScriptInt64(values[8]),
		TargetCents:    redisScriptInt64(values[9]),
		RefillAtCents:  redisScriptInt64(values[10]),
	}
	if len(values) >= 14 {
		lease.OrganizationID = redisScriptInt64(values[11])
		lease.BillingSubjectType = redisScriptString(values[12])
		lease.BillingSubjectID = redisScriptInt64(values[13])
	}
	if lease.UserID == 0 {
		lease.UserID = req.UserID
	}
	lease = normalizeLeaseSnapshotSubject(lease)
	if lease.Region == "" {
		lease.Region = req.Region
	}
	if lease.LeaseID == "" {
		return LeaseSnapshot{}, false, false, nil
	}
	return lease, true, result == 1, nil
}

func redisScriptString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func redisScriptInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		parsed, _ := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		return parsed
	}
}

func parseLeaseRefillSignal(values []any) leaseRefillSignal {
	if len(values) < 12 || redisScriptInt64(values[1]) != 1 {
		return leaseRefillSignal{}
	}
	lease := LeaseSnapshot{
		LeaseID:        redisScriptString(values[2]),
		UserID:         redisScriptInt64(values[3]),
		Region:         redisScriptString(values[4]),
		AllocatedCents: redisScriptInt64(values[5]),
		ReservedCents:  redisScriptInt64(values[6]),
		SpentCents:     redisScriptInt64(values[7]),
		Version:        redisScriptInt64(values[8]),
		ExpiresAt:      redisScriptInt64(values[9]),
		TargetCents:    redisScriptInt64(values[10]),
		RefillAtCents:  redisScriptInt64(values[11]),
	}
	if len(values) >= 15 {
		lease.OrganizationID = redisScriptInt64(values[12])
		lease.BillingSubjectType = redisScriptString(values[13])
		lease.BillingSubjectID = redisScriptInt64(values[14])
	}
	lease = normalizeLeaseSnapshotSubject(lease)
	if lease.LeaseID == "" || lease.UserID <= 0 || strings.TrimSpace(lease.Region) == "" || !leaseNeedsRefill(lease) {
		return leaseRefillSignal{}
	}
	return leaseRefillSignal{Needed: true, Lease: lease}
}

// ResizeReservation changes an existing pre-commit reservation without
// opening a second request record. It is used by async media requests whose
// exact maximum price is known only after body and group pricing resolution.
func (q *RedisQuota) ResizeReservation(ctx context.Context, requestID string, estimatedCostCents int64) (bool, error) {
	if !q.Enabled() {
		return true, nil
	}
	if strings.TrimSpace(requestID) == "" || estimatedCostCents <= 0 {
		return false, errors.New("invalid resize reservation request")
	}
	result, err := resizeReservationScript.Run(
		ctx,
		q.client,
		[]string{q.requestKey(requestID)},
		estimatedCostCents,
		int(asyncReservationTTL.Seconds()),
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// EnsureCommittedCost atomically adjusts an async request's committed local
// spend while enforcing the remaining lease capacity for positive deltas.
// A missing request record is reported separately because Redis may have been
// restored; the control plane's durable usage correction remains authoritative.
func (q *RedisQuota) EnsureCommittedCost(ctx context.Context, requestID string, actualCents int64) (int, error) {
	if !q.Enabled() {
		return RequestCostAdjusted, nil
	}
	if strings.TrimSpace(requestID) == "" || actualCents < 0 {
		return RequestCostMissing, errors.New("invalid committed cost request")
	}
	return ensureCommittedCostScript.Run(
		ctx,
		q.client,
		[]string{q.requestKey(requestID)},
		actualCents,
		int(asyncReservationTTL.Seconds()),
	).Int()
}

func (q *RedisQuota) Commit(ctx context.Context, req UsageCommit) error {
	_, err := q.CommitResolved(ctx, req)
	return err
}

// CommitResolved commits a reservation and returns the actual cent amount
// stored by Redis. A usage correction may arrive before the HTTP middleware
// finishes; in that ordering the corrected amount, not the original one-cent
// estimate, is the only value safe to forward to the control plane.
func (q *RedisQuota) CommitResolved(ctx context.Context, req UsageCommit) (int64, error) {
	if !q.Enabled() {
		return req.ActualCents, nil
	}
	if req.RequestID == "" {
		return 0, errors.New("request_id is required")
	}
	if req.ActualCents < 0 {
		return 0, errors.New("actual_cents cannot be negative")
	}
	actualCents, err := commitScript.Run(ctx, q.client, []string{q.requestKey(req.RequestID)}, req.ActualCents).Int64()
	if err != nil {
		return 0, err
	}
	if actualCents < 0 {
		return req.ActualCents, nil
	}
	return actualCents, nil
}

func (q *RedisQuota) AdjustCommitted(ctx context.Context, req UsageCommit) error {
	_, err := q.adjustCommittedWithRefillSignal(ctx, req)
	return err
}

// adjustCommittedWithRefillSignal keeps the correction and its low-watermark
// decision in one Redis script. The common non-low path returns only two small
// integers; the full lease snapshot is returned only when an active refill is
// actually needed.
func (q *RedisQuota) adjustCommittedWithRefillSignal(ctx context.Context, req UsageCommit) (leaseRefillSignal, error) {
	if !q.Enabled() {
		return leaseRefillSignal{}, nil
	}
	if req.RequestID == "" {
		return leaseRefillSignal{}, errors.New("request_id is required")
	}
	if req.ActualCents < 0 {
		return leaseRefillSignal{}, errors.New("actual_cents cannot be negative")
	}
	values, err := adjustCommittedScript.Run(ctx, q.client, []string{q.requestKey(req.RequestID)}, req.ActualCents).Slice()
	if err != nil {
		return leaseRefillSignal{}, err
	}
	return parseLeaseRefillSignal(values), nil
}

func (q *RedisQuota) AccumulateActualCostMicroCents(ctx context.Context, key string, deltaMicroCents, scale int64) (int64, error) {
	if !q.Enabled() {
		return 0, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "global"
	}
	if deltaMicroCents <= 0 {
		return 0, nil
	}
	if scale <= 0 {
		scale = actualCostMicroCentsScale
	}
	return actualCostAccumulatorScript.Run(ctx, q.client, []string{q.actualCostRemainderKey(key)}, deltaMicroCents, scale, int((30 * 24 * time.Hour).Seconds())).Int64()
}

// AccumulateActualCostMicroCentsIdempotent advances one API key's sub-cent
// carry and records the emitted cents in the existing request hash. This path
// is reserved for asynchronous jobs that need the integer result before their
// final usage event is queued; ordinary corrections settle and enqueue in one
// script inside RedisUsageReporter.
func (q *RedisQuota) AccumulateActualCostMicroCentsIdempotent(ctx context.Context, key, requestID string, deltaMicroCents, scale int64) (int64, error) {
	if !q.Enabled() {
		return 0, nil
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = "global"
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return 0, errors.New("request_id is required")
	}
	if deltaMicroCents < 0 {
		return 0, errors.New("delta_micro_cents cannot be negative")
	}
	if scale <= 0 {
		scale = actualCostMicroCentsScale
	}
	return actualCostIdempotentAccumulatorScript.Run(
		ctx,
		q.client,
		[]string{q.actualCostRemainderKey(key), q.requestKey(requestID)},
		deltaMicroCents,
		scale,
		int((30 * 24 * time.Hour).Seconds()),
		int((72 * time.Hour).Seconds()),
		key,
	).Int64()
}

func (q *RedisQuota) Refund(ctx context.Context, requestID string) error {
	if !q.Enabled() {
		return nil
	}
	if requestID == "" {
		return errors.New("request_id is required")
	}
	_, err := refundScript.Run(ctx, q.client, []string{q.requestKey(requestID)}).Int()
	return err
}

func (q *RedisQuota) TryAcquireLeaseRefillLock(ctx context.Context, userID int64, region string, ttl time.Duration) (bool, error) {
	return q.TryAcquireLeaseRefillLockForSubject(ctx, LeaseSubject{UserID: userID}, region, ttl)
}

func (q *RedisQuota) TryAcquireLeaseRefillLockForSubject(ctx context.Context, subject LeaseSubject, region string, ttl time.Duration) (bool, error) {
	if !q.Enabled() {
		return false, nil
	}
	subject = normalizeLeaseSubject(subject)
	if subject.UserID <= 0 || strings.TrimSpace(region) == "" {
		return false, errors.New("invalid lease refill lock")
	}
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return q.client.SetNX(ctx, q.leaseRefillLockKeyForSubject(subject, region), time.Now().UnixNano(), ttl).Result()
}

// InvalidateLeaseSnapshot removes a locally cached lease only when the pushed
// lease ID still matches and the local version is not newer. This protects a
// newly issued lease from a delayed reclaim event for an older lease.
func (q *RedisQuota) InvalidateLeaseSnapshot(ctx context.Context, userID int64, region, leaseID string, version int64) (bool, error) {
	return q.InvalidateLeaseSnapshotForSubject(ctx, LeaseSubject{UserID: userID}, region, leaseID, version)
}

func (q *RedisQuota) InvalidateLeaseSnapshotForSubject(ctx context.Context, subject LeaseSubject, region, leaseID string, version int64) (bool, error) {
	if !q.Enabled() {
		return false, nil
	}
	region = strings.TrimSpace(region)
	leaseID = strings.TrimSpace(leaseID)
	subject = normalizeLeaseSubject(subject)
	if subject.UserID <= 0 || region == "" {
		return false, errors.New("invalid lease invalidation request")
	}
	result, err := invalidateLeaseSnapshotScript.Run(ctx, q.client, []string{
		q.leaseKeyForSubject(subject, region),
		q.leaseRefillLockKeyForSubject(subject, region),
	}, leaseID, version).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (q *RedisQuota) requestKey(requestID string) string {
	return fmt.Sprintf("%s:req:%s", q.prefix(), requestID)
}

func (q *RedisQuota) leaseKey(userID int64, region string) string {
	return q.leaseKeyForSubject(LeaseSubject{UserID: userID}, region)
}

func (q *RedisQuota) leaseKeyForSubject(subject LeaseSubject, region string) string {
	subject = normalizeLeaseSubject(subject)
	if subject.OrganizationID > 0 {
		return fmt.Sprintf("%s:lease:organization:%d:member:%d:%s", q.prefix(), subject.OrganizationID, subject.UserID, region)
	}
	return fmt.Sprintf("%s:lease:%d:%s", q.prefix(), subject.UserID, region)
}

func (q *RedisQuota) actualCostRemainderKey(key string) string {
	return fmt.Sprintf("%s:usage:actual_cost_remainder:%s", q.prefix(), key)
}

func (q *RedisQuota) leaseRefillLockKey(userID int64, region string) string {
	return q.leaseRefillLockKeyForSubject(LeaseSubject{UserID: userID}, region)
}

func (q *RedisQuota) leaseRefillLockKeyForSubject(subject LeaseSubject, region string) string {
	subject = normalizeLeaseSubject(subject)
	if subject.OrganizationID > 0 {
		return fmt.Sprintf("%s:lease-refill:organization:%d:member:%d:%s", q.prefix(), subject.OrganizationID, subject.UserID, region)
	}
	return fmt.Sprintf("%s:lease-refill:%d:%s", q.prefix(), subject.UserID, region)
}

func normalizeLeaseSubject(subject LeaseSubject) LeaseSubject {
	if subject.OrganizationID > 0 {
		subject.BillingSubjectType = "organization"
		subject.BillingSubjectID = subject.OrganizationID
		return subject
	}
	subject.OrganizationID = 0
	subject.BillingSubjectType = "user"
	subject.BillingSubjectID = subject.UserID
	return subject
}

func normalizeLeaseSnapshotSubject(lease LeaseSnapshot) LeaseSnapshot {
	subject := normalizeLeaseSubject(lease.Subject())
	lease.OrganizationID = subject.OrganizationID
	lease.BillingSubjectType = subject.BillingSubjectType
	lease.BillingSubjectID = subject.BillingSubjectID
	return lease
}

func (q *RedisQuota) SetPrefix(prefix string) {
	if q != nil {
		q.prefixValue.Store(NormalizeRedisPrefix(prefix))
	}
}

func (q *RedisQuota) prefix() string {
	if q == nil {
		return NormalizeRedisPrefix("")
	}
	if value := q.prefixValue.Load(); value != nil {
		if prefix, ok := value.(string); ok && prefix != "" {
			return prefix
		}
	}
	return NormalizeRedisPrefix("")
}

func (q *RedisQuota) Prefix() string {
	return q.prefix()
}

func ParseInt64Field(fields map[string]string, name string) int64 {
	value, _ := strconv.ParseInt(fields[name], 10, 64)
	return value
}

var ensureLeaseScript = redis.NewScript(`
local lease_key = KEYS[1]
local now = tonumber(ARGV[1]) or 0
local incoming_lease_id = ARGV[2]
local incoming_user_id = ARGV[3]
local incoming_region = ARGV[4]
local incoming_allocated = tonumber(ARGV[5]) or 0
local incoming_reserved = tonumber(ARGV[6]) or 0
local incoming_spent = tonumber(ARGV[7]) or 0
local incoming_version = tonumber(ARGV[8]) or 0
local incoming_expires_at = tonumber(ARGV[9]) or 0
local incoming_target = tonumber(ARGV[10]) or 0
local incoming_refill_at = tonumber(ARGV[11]) or 0
local expiry_grace = tonumber(ARGV[12]) or 3600
local incoming_organization_id = tonumber(ARGV[13]) or 0
local incoming_billing_subject_type = ARGV[14] or "user"
local incoming_billing_subject_id = tonumber(ARGV[15]) or incoming_user_id

-- Control-plane snapshots should already be non-negative. Clamp at this last
-- boundary so malformed metadata can never create an invalid local lease.
if incoming_reserved < 0 then
  incoming_reserved = 0
end
if incoming_spent < 0 then
  incoming_spent = 0
end

if redis.call("EXISTS", lease_key) == 0 then
  redis.call("HSET", lease_key,
    "lease_id", incoming_lease_id,
    "user_id", incoming_user_id,
	"organization_id", incoming_organization_id,
	"billing_subject_type", incoming_billing_subject_type,
	"billing_subject_id", incoming_billing_subject_id,
    "region", incoming_region,
    "allocated_cents", incoming_allocated,
    "reserved_cents", incoming_reserved,
    "spent_cents", incoming_spent,
    "version", incoming_version,
    "expires_at", incoming_expires_at,
    "target_cents", incoming_target,
    "refill_at_cents", incoming_refill_at
  )
  if incoming_expires_at > 0 then
    redis.call("EXPIREAT", lease_key, incoming_expires_at + expiry_grace)
  end
  return 1
end

local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
local current_version = tonumber(redis.call("HGET", lease_key, "version") or "0")
local current_allocated = tonumber(redis.call("HGET", lease_key, "allocated_cents") or "0")
local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
local current_expires_at = tonumber(redis.call("HGET", lease_key, "expires_at") or "0")

if current_allocated < 0 or current_reserved < 0 or current_spent < 0 then
  return -1
end

if current_lease_id ~= "" and current_lease_id ~= incoming_lease_id then
  -- A different lease ID may only replace a fully expired lease with no
  -- in-flight reservation. Otherwise request records would settle against the
  -- wrong lease hash and corrupt local quota accounting.
  if current_reserved ~= 0 or current_expires_at == 0 or current_expires_at > now then
    return -1
  end
  redis.call("HSET", lease_key,
    "lease_id", incoming_lease_id,
    "user_id", incoming_user_id,
	"organization_id", incoming_organization_id,
	"billing_subject_type", incoming_billing_subject_type,
	"billing_subject_id", incoming_billing_subject_id,
    "region", incoming_region,
    "allocated_cents", incoming_allocated,
    "reserved_cents", incoming_reserved,
    "spent_cents", incoming_spent,
    "version", incoming_version,
    "expires_at", incoming_expires_at,
    "target_cents", incoming_target,
    "refill_at_cents", incoming_refill_at
  )
  if incoming_expires_at > 0 then
    redis.call("EXPIREAT", lease_key, incoming_expires_at + expiry_grace)
  end
  return 2
end

if incoming_version < current_version then
  return 0
end
if incoming_version == current_version and incoming_allocated < current_allocated then
  return 0
end

-- spent/reserved are Redis-authoritative hot-path counters. A control-plane
-- response may update metadata and increase allocation, but it must never
-- rewind local usage or reduce an allocation because of a delayed response.
local next_allocated = current_allocated
if incoming_allocated > next_allocated then
  next_allocated = incoming_allocated
end
local next_version = current_version
if incoming_version > next_version then
  next_version = incoming_version
end
local next_expires_at = current_expires_at
if incoming_expires_at > next_expires_at then
  next_expires_at = incoming_expires_at
end
redis.call("HSET", lease_key,
  "lease_id", incoming_lease_id,
  "user_id", incoming_user_id,
	"organization_id", incoming_organization_id,
	"billing_subject_type", incoming_billing_subject_type,
	"billing_subject_id", incoming_billing_subject_id,
  "region", incoming_region,
  "allocated_cents", next_allocated,
  "version", next_version,
  "expires_at", next_expires_at,
  "target_cents", incoming_target,
  "refill_at_cents", incoming_refill_at
)
redis.call("HSETNX", lease_key, "reserved_cents", incoming_reserved)
redis.call("HSETNX", lease_key, "spent_cents", incoming_spent)
if next_expires_at > 0 then
  redis.call("EXPIREAT", lease_key, next_expires_at + expiry_grace)
end
return 1
`)

var reserveActiveLeaseScript = redis.NewScript(`
local lease_key = KEYS[1]
local req_key = KEYS[2]
local now = tonumber(ARGV[1]) or 0
local estimated = tonumber(ARGV[2]) or 0
local request_id = ARGV[3]
local key_id = ARGV[4]
local user_id = ARGV[5]
local region = ARGV[6]
local saved_lease_key = ARGV[7]
local ttl = tonumber(ARGV[8]) or 0
local organization_id = tonumber(ARGV[9]) or 0
local billing_subject_type = ARGV[10] or "user"
local billing_subject_id = tonumber(ARGV[11]) or user_id

local values = redis.call("HMGET", lease_key,
  "lease_id", "user_id", "region", "allocated_cents", "reserved_cents",
  "spent_cents", "version", "expires_at", "target_cents", "refill_at_cents",
  "organization_id", "billing_subject_type", "billing_subject_id")
local lease_id = values[1] or ""
if lease_id == "" then
  return {0}
end
local expires_at = tonumber(values[8] or "0")
if expires_at > 0 and expires_at <= now then
  return {0}
end

local allocated = tonumber(values[4] or "0")
local reserved = tonumber(values[5] or "0")
local spent = tonumber(values[6] or "0")
local result = 1
if reserved < 0 or spent < 0 then
  return {0}
end
local request_exists = redis.call("EXISTS", req_key)
if request_exists == 1 then
  local request_status = redis.call("HGET", req_key, "status") or ""
  local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
  local request_user_id = tonumber(redis.call("HGET", req_key, "user_id") or "0")
  local request_organization_id = tonumber(redis.call("HGET", req_key, "organization_id") or "0")
  if request_status ~= "reserved" or request_lease_id ~= lease_id or request_user_id ~= tonumber(user_id) or request_organization_id ~= organization_id then
    return {0}
  end
else
  if allocated - reserved - spent < estimated then
    result = 2
  else
    reserved = reserved + estimated
    redis.call("HINCRBY", lease_key, "reserved_cents", estimated)
    redis.call("HSET", req_key,
      "status", "reserved",
      "request_id", request_id,
      "key_id", key_id,
      "user_id", user_id,
	  "organization_id", organization_id,
	  "billing_subject_type", billing_subject_type,
	  "billing_subject_id", billing_subject_id,
      "region", region,
      "lease_id", lease_id,
      "lease_key", saved_lease_key,
      "estimated_cents", estimated,
      "actual_cents", 0
    )
    redis.call("EXPIRE", req_key, ttl)
  end
end

return {
  result,
  lease_id,
  values[2] or user_id,
  values[3] or region,
  allocated,
  reserved,
  spent,
  tonumber(values[7] or "0"),
  expires_at,
  tonumber(values[9] or "0"),
  tonumber(values[10] or "0"),
  tonumber(values[11] or organization_id),
  values[12] or billing_subject_type,
  tonumber(values[13] or billing_subject_id)
}
`)

var reserveScript = redis.NewScript(`
local lease_key = KEYS[1]
local req_key = KEYS[2]
local now = tonumber(ARGV[1])
local estimated = tonumber(ARGV[2])
local request_id = ARGV[3]
local key_id = ARGV[4]
local user_id = ARGV[5]
local region = ARGV[6]
local lease_id = ARGV[7]
local saved_lease_key = ARGV[8]
local ttl = tonumber(ARGV[9])
local organization_id = tonumber(ARGV[10]) or 0
local billing_subject_type = ARGV[11] or "user"
local billing_subject_id = tonumber(ARGV[12]) or user_id

if redis.call("EXISTS", lease_key) == 0 then
  return 0
end

local expires_at = tonumber(redis.call("HGET", lease_key, "expires_at") or "0")
if expires_at > 0 and expires_at <= now then
  return 0
end

local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
if current_lease_id == "" then
  return 0
end
if redis.call("EXISTS", req_key) == 1 then
  local request_status = redis.call("HGET", req_key, "status") or ""
  local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
	local request_user_id = tonumber(redis.call("HGET", req_key, "user_id") or "0")
	local request_organization_id = tonumber(redis.call("HGET", req_key, "organization_id") or "0")
  if request_status == "reserved" and request_lease_id == current_lease_id and request_user_id == tonumber(user_id) and request_organization_id == organization_id then
    return 1
  end
  return 0
end
if lease_id and lease_id ~= "" and lease_id ~= current_lease_id then
  return 0
end
lease_id = current_lease_id
local allocated = tonumber(redis.call("HGET", lease_key, "allocated_cents") or "0")
local reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
local spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
if reserved < 0 or spent < 0 then
  return 0
end
if allocated - reserved - spent < estimated then
  return 0
end

redis.call("HINCRBY", lease_key, "reserved_cents", estimated)
redis.call("HSET", req_key,
  "status", "reserved",
  "request_id", request_id,
  "key_id", key_id,
  "user_id", user_id,
	"organization_id", organization_id,
	"billing_subject_type", billing_subject_type,
	"billing_subject_id", billing_subject_id,
  "region", region,
  "lease_id", lease_id,
  "lease_key", saved_lease_key,
  "estimated_cents", estimated,
  "actual_cents", 0
)
redis.call("EXPIRE", req_key, ttl)
return 1
`)

var resizeReservationScript = redis.NewScript(`
local req_key = KEYS[1]
local target = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
if redis.call("EXISTS", req_key) == 0 then
  return 0
end
local status = redis.call("HGET", req_key, "status")
if status ~= "reserved" then
  return 0
end
local current = tonumber(redis.call("HGET", req_key, "estimated_cents") or "0")
local delta = target - current
if delta == 0 then
  return 1
end
local lease_key = redis.call("HGET", req_key, "lease_key")
if not lease_key or lease_key == "" or redis.call("EXISTS", lease_key) == 0 then
  return 0
end
local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
if request_lease_id == "" or current_lease_id == "" or request_lease_id ~= current_lease_id then
  return 0
end
if delta > 0 then
  local allocated = tonumber(redis.call("HGET", lease_key, "allocated_cents") or "0")
  local reserved = math.max(tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0"), 0)
  local spent = math.max(tonumber(redis.call("HGET", lease_key, "spent_cents") or "0"), 0)
  if allocated - reserved - spent < delta then
    return 0
  end
end
local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
local next_reserved = current_reserved + delta
if next_reserved < 0 then
  next_reserved = 0
end
redis.call("HSET", lease_key, "reserved_cents", next_reserved)
redis.call("HSET", req_key, "estimated_cents", target)
redis.call("EXPIRE", req_key, ttl)
return 1
`)

var ensureCommittedCostScript = redis.NewScript(`
local req_key = KEYS[1]
local target = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
if redis.call("EXISTS", req_key) == 0 then
  return -1
end
local status = redis.call("HGET", req_key, "status")
local lease_key = redis.call("HGET", req_key, "lease_key")
if not lease_key or lease_key == "" or redis.call("EXISTS", lease_key) == 0 then
  return -1
end
local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
if request_lease_id == "" or current_lease_id == "" or request_lease_id ~= current_lease_id then
  return -1
end

if status == "reserved" then
  local current = tonumber(redis.call("HGET", req_key, "estimated_cents") or "0")
  local delta = target - current
  if delta > 0 then
    local allocated = tonumber(redis.call("HGET", lease_key, "allocated_cents") or "0")
    local reserved = math.max(tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0"), 0)
    local spent = math.max(tonumber(redis.call("HGET", lease_key, "spent_cents") or "0"), 0)
    if allocated - reserved - spent < delta then
      return 0
    end
  end
  if delta ~= 0 then
    local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
    local next_reserved = current_reserved + delta
    if next_reserved < 0 then
      next_reserved = 0
    end
    redis.call("HSET", lease_key, "reserved_cents", next_reserved)
  end
  redis.call("HSET", req_key,
    "estimated_cents", target,
    "corrected_actual_cents", target
  )
  redis.call("EXPIRE", req_key, ttl)
  return 1
end

if status ~= "committed" then
  return -1
end
local current = tonumber(redis.call("HGET", req_key, "actual_cents") or "0")
local delta = target - current
if delta > 0 then
  local allocated = tonumber(redis.call("HGET", lease_key, "allocated_cents") or "0")
  local reserved = math.max(tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0"), 0)
  local spent = math.max(tonumber(redis.call("HGET", lease_key, "spent_cents") or "0"), 0)
  if allocated - reserved - spent < delta then
    return 0
  end
end
if delta ~= 0 then
  local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
  local next_spent = current_spent + delta
  if next_spent < 0 then
    next_spent = 0
  end
  redis.call("HSET", lease_key, "spent_cents", next_spent)
end
redis.call("HSET", req_key, "actual_cents", target)
redis.call("EXPIRE", req_key, ttl)
return 1
`)

var commitScript = redis.NewScript(`
local req_key = KEYS[1]
local actual = tonumber(ARGV[1])
if redis.call("EXISTS", req_key) == 0 then
  return -1
end
local status = redis.call("HGET", req_key, "status")
if status == "committed" then
  return tonumber(redis.call("HGET", req_key, "actual_cents") or actual)
end
if status ~= "reserved" then
  return -1
end
local user_id = redis.call("HGET", req_key, "user_id")
local region = redis.call("HGET", req_key, "region")
local estimated = tonumber(redis.call("HGET", req_key, "estimated_cents") or "0")
local corrected = redis.call("HGET", req_key, "corrected_actual_cents")
if corrected and corrected ~= "" then
  actual = tonumber(corrected)
end
local lease_key = redis.call("HGET", req_key, "lease_key")
if not lease_key or lease_key == "" then
  lease_key = string.gsub(req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
end
if redis.call("EXISTS", lease_key) == 1 then
  local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
  local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
  if request_lease_id ~= "" and request_lease_id == current_lease_id then
    local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
    local next_reserved = current_reserved - math.max(estimated, 0)
    if next_reserved < 0 then
      next_reserved = 0
    end
    local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
    local next_spent = current_spent + math.max(actual, 0)
    if next_spent < 0 then
      next_spent = 0
    end
    redis.call("HSET", lease_key, "reserved_cents", next_reserved, "spent_cents", next_spent)
  end
end
redis.call("HSET", req_key, "status", "committed", "actual_cents", actual)
return actual
`)

var adjustCommittedScript = redis.NewScript(`
local req_key = KEYS[1]
local actual = tonumber(ARGV[1])

local function result_with_refill(status, lease_key)
  if status ~= 1 or not lease_key or lease_key == "" or redis.call("EXISTS", lease_key) == 0 then
    return {status, 0}
  end
  local values = redis.call("HMGET", lease_key,
    "lease_id", "user_id", "region", "allocated_cents", "reserved_cents",
    "spent_cents", "version", "expires_at", "target_cents", "refill_at_cents",
	"organization_id", "billing_subject_type", "billing_subject_id")
  local refill_at = tonumber(values[10] or "0")
  if refill_at <= 0 then
    return {status, 0}
  end
  local allocated = tonumber(values[4] or "0")
  local reserved = tonumber(values[5] or "0")
  local spent = tonumber(values[6] or "0")
  if reserved < 0 or spent < 0 then
    return {status, 0}
  end
  if allocated - reserved - spent > refill_at then
    return {status, 0}
  end
  return {
    status, 1,
    values[1] or "", values[2] or "0", values[3] or "",
    allocated, reserved, spent,
    tonumber(values[7] or "0"), tonumber(values[8] or "0"),
    tonumber(values[9] or "0"), refill_at,
	tonumber(values[11] or "0"), values[12] or "user", tonumber(values[13] or values[2] or "0")
  }
end

if redis.call("EXISTS", req_key) == 0 then
  return {0, 0}
end
local status = redis.call("HGET", req_key, "status")
if status == "reserved" then
  local current = tonumber(redis.call("HGET", req_key, "estimated_cents") or "0")
  local delta = actual - current
  local user_id = redis.call("HGET", req_key, "user_id")
  local region = redis.call("HGET", req_key, "region")
  local lease_key = redis.call("HGET", req_key, "lease_key")
  if not lease_key or lease_key == "" then
    lease_key = string.gsub(req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
  end
  local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
  local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
  local lease_matches = request_lease_id ~= "" and request_lease_id == current_lease_id
  if delta ~= 0 and lease_matches then
    -- The final cost is already known. Resize the live reservation now so the
    -- refill observation is truthful; commit will later move the same amount
    -- from reserved to spent without changing available capacity.
    local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
    local next_reserved = current_reserved + delta
    if next_reserved < 0 then
      next_reserved = 0
    end
    redis.call("HSET", lease_key, "reserved_cents", next_reserved)
  end
  redis.call("HSET", req_key,
    "estimated_cents", actual,
    "corrected_actual_cents", actual
  )
  if lease_matches then
    return result_with_refill(1, lease_key)
  end
  return {1, 0}
end
if status ~= "committed" then
  return {0, 0}
end
local current = tonumber(redis.call("HGET", req_key, "actual_cents") or "0")
local delta = actual - current
local user_id = redis.call("HGET", req_key, "user_id")
local region = redis.call("HGET", req_key, "region")
local lease_key = redis.call("HGET", req_key, "lease_key")
if not lease_key or lease_key == "" then
  lease_key = string.gsub(req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
end
local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
local lease_matches = request_lease_id ~= "" and request_lease_id == current_lease_id
if delta ~= 0 and lease_matches then
  local current_spent = tonumber(redis.call("HGET", lease_key, "spent_cents") or "0")
  local next_spent = current_spent + delta
  if next_spent < 0 then
    next_spent = 0
  end
  redis.call("HSET", lease_key, "spent_cents", next_spent)
end
if delta ~= 0 then
  redis.call("HSET", req_key, "actual_cents", actual)
end
-- A previous attempt may have applied the correction but failed while
-- appending the usage event. Its idempotent retry has delta=0, yet it must
-- still surface an already-low lease after the queue write succeeds.
if lease_matches then
  return result_with_refill(1, lease_key)
end
return {1, 0}
`)

var actualCostAccumulatorScript = redis.NewScript(`
local key = KEYS[1]
local delta = tonumber(ARGV[1]) or 0
local scale = tonumber(ARGV[2]) or 1000000
local ttl = tonumber(ARGV[3]) or 2592000
if delta <= 0 then
  return 0
end
local current = tonumber(redis.call("GET", key) or "0")
local total = current + delta
local whole = math.floor(total / scale)
local remainder = total - (whole * scale)
redis.call("SET", key, remainder, "EX", ttl)
return whole
`)

var actualCostIdempotentAccumulatorScript = redis.NewScript(`
local remainder_key = KEYS[1]
local req_key = KEYS[2]
local delta = tonumber(ARGV[1]) or 0
local scale = tonumber(ARGV[2]) or 1000000
local remainder_ttl = tonumber(ARGV[3]) or 2592000
local sparse_request_ttl = tonumber(ARGV[4]) or 259200
local scope = ARGV[5] or "global"

local req_type_reply = redis.call("TYPE", req_key)
local req_type = req_type_reply
if type(req_type_reply) == "table" then
  req_type = req_type_reply["ok"]
end
if req_type ~= "none" and req_type ~= "hash" then
  return redis.error_reply("WRONGTYPE settlement request key is not a hash")
end
local remainder_type_reply = redis.call("TYPE", remainder_key)
local remainder_type = remainder_type_reply
if type(remainder_type_reply) == "table" then
  remainder_type = remainder_type_reply["ok"]
end
if remainder_type ~= "none" and remainder_type ~= "string" then
  return redis.error_reply("WRONGTYPE settlement remainder key is not a string")
end

local settled = redis.call("HGET", req_key, "settlement_actual_cents")
if settled then
  local settled_micro = tonumber(redis.call("HGET", req_key, "settlement_micro_cents") or "0")
  local settled_scope = redis.call("HGET", req_key, "settlement_scope") or scope
  -- A zero-cost retry is allowed to recover the already frozen amount. Any
  -- different non-zero fact indicates two billing calculations for one ID.
  if delta > 0 and (settled_micro ~= delta or settled_scope ~= scope) then
    return redis.error_reply("SETTLEMENT_CONFLICT request already has different actual cost")
  end
  return tonumber(settled) or 0
end
local remainder_type_reply = redis.call("TYPE", remainder_key)
local remainder_type = remainder_type_reply
if type(remainder_type_reply) == "table" then
  remainder_type = remainder_type_reply["ok"]
end
if remainder_type ~= "none" and remainder_type ~= "string" then
  return redis.error_reply("WRONGTYPE settlement remainder key is not a string")
end
local current = tonumber(redis.call("GET", remainder_key) or "0")
local total = current + delta
local whole = math.floor(total / scale)
local remainder = total - (whole * scale)
redis.call("SET", remainder_key, remainder, "EX", remainder_ttl)
local request_exists = redis.call("EXISTS", req_key)
redis.call("HSET", req_key,
  "settlement_micro_cents", delta,
  "settlement_actual_cents", whole,
  "settlement_scope", scope,
  "settlement_mode", "idempotent_v2"
)
if request_exists == 0 then
  redis.call("EXPIRE", req_key, sparse_request_ttl)
end
return whole
`)

var refundScript = redis.NewScript(`
local req_key = KEYS[1]
if redis.call("EXISTS", req_key) == 0 then
  return 0
end
local status = redis.call("HGET", req_key, "status")
if status == "refunded" or status == "committed" then
  return 1
end
if status ~= "reserved" then
  return 0
end
local user_id = redis.call("HGET", req_key, "user_id")
local region = redis.call("HGET", req_key, "region")
local estimated = tonumber(redis.call("HGET", req_key, "estimated_cents") or "0")
local lease_key = redis.call("HGET", req_key, "lease_key")
if not lease_key or lease_key == "" then
  lease_key = string.gsub(req_key, ":req:.*$", ":lease:" .. user_id .. ":" .. region)
end
if redis.call("EXISTS", lease_key) == 1 then
  local request_lease_id = redis.call("HGET", req_key, "lease_id") or ""
  local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
  if request_lease_id ~= "" and request_lease_id == current_lease_id then
    local current_reserved = tonumber(redis.call("HGET", lease_key, "reserved_cents") or "0")
    local next_reserved = current_reserved - math.max(estimated, 0)
    if next_reserved < 0 then
      next_reserved = 0
    end
    redis.call("HSET", lease_key, "reserved_cents", next_reserved)
  end
end
redis.call("HSET", req_key, "status", "refunded")
return 1
`)

var invalidateLeaseSnapshotScript = redis.NewScript(`
local lease_key = KEYS[1]
local refill_lock_key = KEYS[2]
local incoming_lease_id = ARGV[1] or ""
local incoming_version = tonumber(ARGV[2]) or 0

if redis.call("EXISTS", lease_key) == 0 then
  redis.call("DEL", refill_lock_key)
  return 0
end

local current_lease_id = redis.call("HGET", lease_key, "lease_id") or ""
local current_version = tonumber(redis.call("HGET", lease_key, "version") or "0")

if incoming_lease_id ~= "" and current_lease_id ~= incoming_lease_id then
  return 0
end
if incoming_version > 0 and current_version > incoming_version then
  return 0
end

redis.call("DEL", lease_key)
redis.call("DEL", refill_lock_key)
return 1
`)
