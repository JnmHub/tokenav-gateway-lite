package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
)

// GatewayImageTaskBilling reuses the request reservation created by the
// gateway-lite authentication middleware. Full sub2api mode has no regional
// lease; its normal RecordUsage path remains the wallet authority.
type GatewayImageTaskBilling struct{}

func NewGatewayImageTaskBilling() ImageTaskBilling { return GatewayImageTaskBilling{} }

func (GatewayImageTaskBilling) Reserve(ctx context.Context, task *ImageTask, maxCents int64) (ImageTaskReservation, error) {
	if task == nil || maxCents <= 0 {
		return ImageTaskReservation{}, errors.New("image task reservation amount must be positive")
	}
	reserved, err := gatewaylite.EnsureRequestReservation(ctx, maxCents)
	if err != nil {
		return ImageTaskReservation{}, err
	}
	if meta, ok := gatewaylite.RequestMetaFromContext(ctx); ok {
		task.OrganizationID = meta.OrganizationID
		task.BillingSubjectType = strings.TrimSpace(meta.BillingSubjectType)
		task.BillingSubjectID = meta.BillingSubjectID
		task.QuotaRequestID = strings.TrimSpace(meta.RequestID)
		task.LeaseID = strings.TrimSpace(meta.LeaseID)
		task.Region = strings.TrimSpace(meta.Region)
		task.GatewayID = strings.TrimSpace(meta.GatewayID)
		task.RateMultiplier = meta.RateMultiplier
		task.BillingSettlementMode = meta.BillingSettlementMode
		task.EntryGatewayNodeID = meta.EntryGatewayNodeID
		task.EntryGlobalGroupID = meta.EntryGlobalGroupID
		if meta.Funding != nil {
			a := meta.Funding.Snapshot()
			task.FundingSnapshot = &a
			task.LeaseID = ""
		}
		if meta.RatePolicyAudit != nil && meta.RatePolicyAudit.Valid() {
			cloned := meta.RatePolicyAudit.Clone()
			task.RatePolicyAudit = &cloned
		}
	}
	return ImageTaskReservation{ID: task.BillingRequestID(), ReservedCents: reserved}, nil
}

// ReserveReplay creates a new regional reservation for an administrator
// replay. Reusing the failed task's request ID would make the control plane
// correctly de-duplicate the second run to the old zero-cost release, so every
// replay receives its own financial identity before it re-enters the queue.
func (GatewayImageTaskBilling) ReserveReplay(ctx context.Context, task *ImageTask, maxCents int64) (ImageTaskReservation, error) {
	if task == nil || task.ID == "" || task.UserID <= 0 || maxCents <= 0 {
		return ImageTaskReservation{}, errors.New("image task replay reservation is invalid")
	}
	quota := gatewaylite.DefaultRedisQuota()
	if task.FundingSnapshot != nil && (quota == nil || !quota.Enabled()) {
		return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
	}
	if quota == nil || !quota.Enabled() {
		// Full sub2api mode has no regional lease. Its normal RecordUsage path is
		// authoritative and uses the task ID as the idempotency identity.
		return ImageTaskReservation{ID: "image-task:" + task.ID, ReservedCents: maxCents}, nil
	}
	if runtimeRef := gatewaylite.DefaultRuntimeConfigRef(); runtimeRef != nil {
		quota = quota.WithPrefix(runtimeRef.Current().RedisPrefix)
	}
	region := strings.TrimSpace(task.Region)
	if region == "" {
		return ImageTaskReservation{}, errors.New("image task replay region is unavailable")
	}
	requestID := "image-task-replay:" + task.ID
	control := gatewaylite.DefaultControlPlaneClientRef()
	if control != nil && control.Configured() && task.EntryGatewayNodeID > 0 && task.EntryGlobalGroupID > 0 {
		response, err := control.AuthorizeFunding(ctx, gatewaylite.AuthorizeFundingRequest{RequestID: requestID, KeyID: task.GatewayKeyID, UserID: task.UserID, GatewayCode: task.GatewayID, Region: task.Region, EntryGatewayNodeID: task.EntryGatewayNodeID, EntryGroupID: task.EntryGlobalGroupID, RequestedModel: task.Model, Endpoint: task.Endpoint, BoundKind: "selection", ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli()})
		if err != nil || response == nil || !response.OK || response.Authorization == nil {
			return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
		}
		a := response.Authorization.Clone()
		if a.Disposition == "" {
			a.Disposition = response.Disposition
		}
		if a.RequestID != requestID || a.KeyID != task.GatewayKeyID || a.UserID != task.UserID || a.OrganizationID != task.OrganizationID || a.EntryGroupID != task.EntryGlobalGroupID || a.EntryGatewayNodeID != task.EntryGatewayNodeID || a.RequestedModel != task.Model {
			return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
		}
		if a.Disposition == "authorization_required" || a.Disposition == "authorized" {
			if maxCents > (1<<53-1)/1_000_000 {
				return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaInsufficient
			}
			funding := gatewaylite.NewRequestFunding(a, control, quota)
			bounded, err := funding.Ensure(ctx, maxCents*1_000_000, "image_replay_fixed_media_maximum")
			if err != nil {
				return ImageTaskReservation{}, err
			}
			task.FundingSnapshot = &bounded
			task.QuotaRequestID = requestID
			task.LeaseID = ""
			task.BillingSubjectType = bounded.BillingSubjectType
			task.BillingSubjectID = bounded.BillingSubjectID
			task.BillingSettlementMode = gatewaylite.BillingSettlementModeIdempotentV2
			return ImageTaskReservation{ID: requestID, ReservedCents: maxCents}, nil
		}
		if a.Disposition != "legacy_balance" {
			return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
		}
		task.FundingSnapshot = nil
	} else if task.FundingSnapshot != nil {
		return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
	}
	reserve := gatewaylite.ReserveRequest{
		RequestID: requestID, KeyID: strings.TrimSpace(task.GatewayKeyID), UserID: task.UserID,
		OrganizationID: task.OrganizationID, BillingSubjectType: task.BillingSubjectType, BillingSubjectID: task.BillingSubjectID,
		Region: region, EstimatedCostCents: maxCents,
	}
	lease, ok, _, err := quota.ReserveActiveLease(ctx, reserve)
	if err != nil {
		return ImageTaskReservation{}, fmt.Errorf("reserve active lease for image replay: %w", err)
	}
	if !ok {
		control := gatewaylite.DefaultControlPlaneClientRef()
		if control == nil || !control.Configured() {
			return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
		}
		response, rebalanceErr := control.RebalanceLease(ctx, gatewaylite.RebalanceLeaseRequest{
			KeyID: task.GatewayKeyID, UserID: task.UserID, OrganizationID: task.OrganizationID,
			BillingSubjectType: task.BillingSubjectType, BillingSubjectID: task.BillingSubjectID,
			Region: region, GatewayCode: strings.TrimSpace(task.GatewayID),
			EstimatedCostCents: maxCents, Reason: "image_task_admin_replay",
		})
		if rebalanceErr != nil {
			return ImageTaskReservation{}, fmt.Errorf("rebalance image replay lease: %w", rebalanceErr)
		}
		if response == nil || !response.OK {
			if response != nil && (response.Error == "insufficient_balance" || response.Error == "usage_limit_exceeded" || response.Error == "insufficient_quota") {
				return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaInsufficient
			}
			return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaUnavailable
		}
		lease = response.Lease
		if lease.UserID == 0 {
			lease.UserID = task.UserID
		}
		if strings.TrimSpace(lease.Region) == "" {
			lease.Region = region
		}
		if err := quota.EnsureLease(ctx, lease); err != nil {
			return ImageTaskReservation{}, fmt.Errorf("install image replay lease: %w", err)
		}
		reserve.LeaseID = lease.LeaseID
		reserved, reserveErr := quota.Reserve(ctx, reserve)
		if reserveErr != nil {
			return ImageTaskReservation{}, fmt.Errorf("reserve image replay quota: %w", reserveErr)
		}
		if !reserved {
			return ImageTaskReservation{}, gatewaylite.ErrRequestQuotaInsufficient
		}
	}
	if _, err := quota.ResizeReservation(ctx, requestID, maxCents); err != nil {
		return ImageTaskReservation{}, fmt.Errorf("extend image replay reservation: %w", err)
	}
	task.QuotaRequestID = requestID
	task.LeaseID = strings.TrimSpace(lease.LeaseID)
	return ImageTaskReservation{ID: requestID, ReservedCents: maxCents}, nil
}

// Successful image execution records the durable usage correction before this
// method is reached. The remaining local boundary converts the Redis hold into
// spent quota. Commit is idempotent, so a crash after Redis succeeds but before
// the task row is saved cannot double charge on retry.
func (GatewayImageTaskBilling) Settle(ctx context.Context, task *ImageTask, actualCents int64) error {
	if task == nil || actualCents < 0 {
		return errors.New("invalid image task settlement")
	}
	if task.FundingSnapshot != nil {
		// The executor queues the actual micro-cent event before marking settled.
		// Applying the old balance commit here would claim the same request twice.
		if !task.ExecutorSettled {
			return errors.New("funded image usage is not durably settled")
		}
		return nil
	}
	if strings.TrimSpace(task.QuotaRequestID) == "" {
		return nil
	}
	quota := gatewaylite.DefaultRedisQuota()
	if quota == nil || !quota.Enabled() {
		return gatewaylite.ErrRequestQuotaUnavailable
	}
	if runtimeRef := gatewaylite.DefaultRuntimeConfigRef(); runtimeRef != nil {
		quota = quota.WithPrefix(runtimeRef.Current().RedisPrefix)
	}
	if err := quota.Commit(ctx, gatewaylite.UsageCommit{
		RequestID: task.QuotaRequestID, ActualCents: actualCents,
	}); err != nil {
		return fmt.Errorf("commit image task quota reservation: %w", err)
	}
	return nil
}

func (GatewayImageTaskBilling) Release(ctx context.Context, task *ImageTask) error {
	if task == nil || strings.TrimSpace(task.QuotaRequestID) == "" {
		return nil
	}
	if task.FundingSnapshot != nil && task.ExecutionPhase == ImageTaskExecutionPhaseDispatched {
		return gatewaylite.ErrRequestQuotaUnavailable
	}
	event := gatewaylite.UsageEvent{
		RequestID: task.BillingRequestID(), UserID: task.UserID, KeyID: task.GatewayKeyID,
		OrganizationID: task.OrganizationID, BillingSubjectType: task.BillingSubjectType, BillingSubjectID: task.BillingSubjectID,
		LeaseID: task.LeaseID, Region: task.Region, GatewayID: task.GatewayID,
		Protocol: "openai_compatible", Model: task.Model, Method: "POST", Path: task.Endpoint,
		Status: 200, BillingMode: string(BillingModeImage), ImageCount: int64(task.ImageCount),
		EstimatedCents: task.ReservedCents, ActualCents: 0,
		StartedAtMillis: task.CreatedAt.UnixMilli(), EndedAtMillis: time.Now().UTC().UnixMilli(),
	}
	if err := gatewaylite.ReportUsageCorrection(task.UsageContext(ctx), event); err != nil {
		return fmt.Errorf("release image task quota reservation: %w", err)
	}
	return nil
}
