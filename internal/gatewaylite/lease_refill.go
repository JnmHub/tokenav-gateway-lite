package gatewaylite

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	postUsageLeaseRefillLockTTL = 5 * time.Second
	postUsageLeaseRefillTimeout = 3 * time.Second
)

// LeaseRefillControlPlane is the narrow control-plane surface needed by both
// request-time prefetch and post-usage refill. All capacity changes still go
// through the control plane, so this helper cannot mint quota locally.
type LeaseRefillControlPlane interface {
	AcquireLease(context.Context, AcquireLeaseRequest) (*AcquireLeaseResponse, error)
	RebalanceLease(context.Context, RebalanceLeaseRequest) (*RebalanceLeaseResponse, error)
}

type leaseRefillSignal struct {
	Needed bool
	Lease  LeaseSnapshot
}

var postUsageLeaseRefillPending sync.Map

// RefillLeaseAtLowWatermark runs the same safe AcquireLease -> RebalanceLease
// sequence for request-time and post-usage callers. It only installs snapshots
// returned by the control plane; balance and shared-pool checks remain
// authoritative there.
func RefillLeaseAtLowWatermark(
	ctx context.Context,
	control LeaseRefillControlPlane,
	quota *RedisQuota,
	userID int64,
	region string,
	gatewayCode string,
	reason string,
	estimatedCostCents ...int64,
) (bool, error) {
	return RefillLeaseAtLowWatermarkForSubject(ctx, control, quota, LeaseSubject{UserID: userID}, region, gatewayCode, reason, estimatedCostCents...)
}

func RefillLeaseAtLowWatermarkForSubject(
	ctx context.Context,
	control LeaseRefillControlPlane,
	quota *RedisQuota,
	subject LeaseSubject,
	region string,
	gatewayCode string,
	reason string,
	estimatedCostCents ...int64,
) (bool, error) {
	subject = normalizeLeaseSubject(subject)
	userID := subject.UserID
	if control == nil || quota == nil || !quota.Enabled() || userID <= 0 {
		return false, errors.New("lease refill is not configured")
	}
	region = strings.TrimSpace(region)
	if region == "" {
		return false, errors.New("lease refill region is required")
	}
	gatewayCode = strings.TrimSpace(gatewayCode)
	if gatewayCode == "" {
		gatewayCode = region
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "gateway_low_watermark_prefetch"
	}
	estimatedCents := int64(1)
	if len(estimatedCostCents) > 0 && estimatedCostCents[0] > 0 {
		estimatedCents = estimatedCostCents[0]
	}

	current, found, err := quota.LoadLeaseForSubject(ctx, subject, region)
	if err != nil || !found {
		return false, err
	}
	if !leaseNeedsRefill(current) {
		return true, nil
	}

	observation := current.Observation(false)
	observation.LowWatermark = true
	acquire, err := control.AcquireLease(ctx, AcquireLeaseRequest{
		UserID:             userID,
		OrganizationID:     subject.OrganizationID,
		BillingSubjectType: subject.BillingSubjectType,
		BillingSubjectID:   subject.BillingSubjectID,
		Region:             region,
		GatewayCode:        gatewayCode,
		EstimatedCostCents: estimatedCents,
		Reason:             reason,
		LeaseObservation:   observation,
	})
	if err != nil {
		return false, err
	}
	if acquire != nil && acquire.OK {
		ready, applyErr := applyRefilledLease(ctx, quota, subject, region, acquire.Lease)
		if applyErr != nil && !errors.Is(applyErr, ErrLeaseSnapshotConflict) {
			return false, applyErr
		}
		if applyErr == nil && ready {
			return true, nil
		}
		// A concurrent control push may replace the local lease between the
		// initial observation and applying AcquireLease's response. Reload below
		// and rebalance the current lease instead of treating that safe conflict
		// as a hard refill failure.
	} else if acquire == nil || leaseRefillMustStop(acquire.Error) {
		return false, nil
	}

	// Acquire may safely return the durable snapshot while the gateway's newer
	// Redis counters already crossed the threshold. Rebalance with that latest
	// observation once; the control plane remains responsible for reclaiming or
	// rejecting shared-pool capacity.
	latest, found, err := quota.LoadLeaseForSubject(ctx, subject, region)
	if err != nil || !found {
		return false, err
	}
	if !leaseNeedsRefill(latest) {
		return true, nil
	}
	observation = latest.Observation(false)
	observation.LowWatermark = true
	rebalance, err := control.RebalanceLease(ctx, RebalanceLeaseRequest{
		UserID:             userID,
		OrganizationID:     subject.OrganizationID,
		BillingSubjectType: subject.BillingSubjectType,
		BillingSubjectID:   subject.BillingSubjectID,
		Region:             region,
		GatewayCode:        gatewayCode,
		EstimatedCostCents: estimatedCents,
		Reason:             reason,
		LeaseObservation:   observation,
	})
	if err != nil {
		return false, err
	}
	if rebalance == nil || !rebalance.OK {
		return false, nil
	}
	return applyRefilledLease(ctx, quota, subject, region, rebalance.Lease)
}

func leaseNeedsRefill(lease LeaseSnapshot) bool {
	return lease.RefillAtCents > 0 && lease.AvailableCents() <= lease.RefillAtCents
}

func applyRefilledLease(ctx context.Context, quota *RedisQuota, subject LeaseSubject, region string, lease LeaseSnapshot) (bool, error) {
	subject = normalizeLeaseSubject(subject)
	if lease.UserID == 0 {
		lease.UserID = subject.UserID
	}
	if lease.OrganizationID == 0 {
		lease.OrganizationID = subject.OrganizationID
		lease.BillingSubjectType = subject.BillingSubjectType
		lease.BillingSubjectID = subject.BillingSubjectID
	}
	// The lookup key is authoritative for this refill. Do not let a malformed
	// control-plane snapshot install capacity under a different region hash.
	lease.Region = region
	if err := quota.EnsureLease(ctx, lease); err != nil {
		return false, err
	}
	current, found, err := quota.LoadLeaseForSubject(ctx, subject, region)
	if err != nil || !found {
		return false, err
	}
	return !leaseNeedsRefill(current), nil
}

func leaseRefillMustStop(code string) bool {
	switch strings.TrimSpace(code) {
	case "subscription_required", "phone_verification_required", "linux_do_binding_required", "github_binding_required", "lease_state_conflict":
		return true
	default:
		return false
	}
}

// schedulePostUsageLeaseRefill is called only after the Redis Lua settlement
// reports a low-watermark transition and the usage event has been durably
// appended to Redis Stream. Non-low-watermark usage never reaches this helper.
func schedulePostUsageLeaseRefill(quota *RedisQuota, signal leaseRefillSignal, gatewayCode string) {
	control := DefaultControlPlaneClientRef()
	if control == nil || !control.Configured() {
		return
	}
	schedulePostUsageLeaseRefillWithControl(control, quota, signal, gatewayCode)
}

func schedulePostUsageLeaseRefillWithControl(control LeaseRefillControlPlane, quota *RedisQuota, signal leaseRefillSignal, gatewayCode string) {
	if control == nil || quota == nil || !quota.Enabled() || !signal.Needed || !leaseNeedsRefill(signal.Lease) {
		return
	}
	subject := signal.Lease.Subject()
	key := quota.Prefix() + ":" + strings.TrimSpace(signal.Lease.Region) + ":" + strconv.FormatInt(subject.OrganizationID, 10) + ":" + strconv.FormatInt(subject.UserID, 10)
	if _, loaded := postUsageLeaseRefillPending.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	go func() {
		defer postUsageLeaseRefillPending.Delete(key)

		// The request-entry prefetch uses the same short Redis lock. If it caught
		// the lease just before the real-cost correction, wait for that lock once
		// and recheck instead of losing the final low-watermark transition.
		for attempt := 0; attempt < 2; attempt++ {
			lockCtx, lockCancel := context.WithTimeout(context.Background(), time.Second)
			locked, err := quota.TryAcquireLeaseRefillLockForSubject(lockCtx, subject, signal.Lease.Region, postUsageLeaseRefillLockTTL)
			lockCancel()
			if err != nil {
				log.Printf("gateway-lite: post-usage lease refill lock failed user_id=%d gateway=%s: %v", signal.Lease.UserID, signal.Lease.Region, err)
				return
			}
			if !locked {
				if attempt == 0 {
					timer := time.NewTimer(postUsageLeaseRefillLockTTL + 50*time.Millisecond)
					<-timer.C
					continue
				}
				return
			}

			refillCtx, refillCancel := context.WithTimeout(context.Background(), postUsageLeaseRefillTimeout)
			_, refillErr := RefillLeaseAtLowWatermarkForSubject(
				refillCtx,
				control,
				quota,
				subject,
				signal.Lease.Region,
				gatewayCode,
				"gateway_post_usage_refill",
			)
			refillCancel()
			if refillErr != nil {
				log.Printf("gateway-lite: post-usage lease refill failed user_id=%d gateway=%s: %v", signal.Lease.UserID, signal.Lease.Region, refillErr)
			}
			return
		}
	}()
}
