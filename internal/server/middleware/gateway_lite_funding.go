package middleware

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/gatewaylite"
	"github.com/gin-gonic/gin"
)

// A handshake has no response.create model yet. Card/balance selection is made
// for each bounded turn after its ordinary permission check, never on upgrade.
func gatewayLiteFundingAdmission(c *gin.Context, control GatewayLiteControlPlane, quota *gatewaylite.RedisQuota, reporter gatewaylite.UsageReportClient, key gatewaylite.KeySnapshot, meta gatewaylite.RequestMeta, model string) bool {
	fundingControl, supported := control.(gatewaylite.FundingControlPlane)
	if !supported {
		return false
	} // Legacy mocks/standalone clients retain the original lease contract.
	if quota == nil || !quota.Enabled() {
		AbortWithError(c, 503, "QUOTA_UNAVAILABLE", "资金缓存服务不可用，请稍后重试")
		return true
	}
	response, err := fundingControl.AuthorizeFunding(c.Request.Context(), gatewaylite.AuthorizeFundingRequest{
		RequestID: meta.RequestID, KeyID: key.KeyID, UserID: key.UserID, GatewayCode: meta.GatewayID, Region: meta.Region,
		EntryGatewayNodeID: key.KeyDefaultGatewayNodeID, EntryGroupID: key.Group.ID, RequestedModel: model,
		Endpoint: c.Request.URL.Path, BoundKind: "selection", BoundEvidence: map[string]any{"phase": "main_site_selection"}, ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli(),
	})
	if err != nil || response == nil || !response.OK || response.Authorization == nil {
		AbortWithError(c, 503, "FUNDING_UNAVAILABLE", "主站资金选择暂不可用，请稍后重试")
		return true
	}
	a := response.Authorization.Clone()
	if a.Disposition == "" {
		a.Disposition = response.Disposition
	}
	if a.RequestID != meta.RequestID || a.KeyID != key.KeyID || a.UserID != key.UserID || a.OrganizationID != key.OrganizationID || a.EntryGatewayNodeID != key.KeyDefaultGatewayNodeID || a.EntryGroupID != key.Group.ID || a.RequestedModel != model {
		AbortWithError(c, 503, "FUNDING_SCOPE_CONFLICT", "资金授权与当前请求不一致")
		return true
	}
	if a.Disposition == "legacy_balance" {
		return false
	}
	if a.Disposition != "authorization_required" && a.Disposition != "authorized" {
		AbortWithError(c, 503, "FUNDING_UNAVAILABLE", "主站返回了未知资金状态")
		return true
	}
	if reporter == nil {
		AbortWithError(c, 503, "FUNDING_UNAVAILABLE", "资金用量队列不可用")
		return true
	}
	meta.LeaseID = ""
	meta.Reservation = nil
	meta.BillingSubjectType = a.BillingSubjectType
	meta.BillingSubjectID = a.BillingSubjectID
	meta.BillingSettlementMode = gatewaylite.BillingSettlementModeIdempotentV2
	meta.EntryGatewayNodeID = a.EntryGatewayNodeID
	meta.EntryGlobalGroupID = a.EntryGroupID
	meta.Funding = gatewaylite.NewRequestFunding(a, fundingControl, quota)
	c.Request = c.Request.WithContext(gatewaylite.ContextWithRequestMeta(c.Request.Context(), meta))
	c.Next()
	finalMeta, _ := gatewaylite.RequestMetaFromContext(c.Request.Context())
	if finalMeta.AsyncSettlement || finalMeta.AsyncPersistenceUncertain {
		return true
	}
	// Successful final usage is queued by RecordUsage. A lower-revision zero
	// event is harmless if it arrives afterwards, and covers pre-upstream errors.
	if c.Writer.Status() >= 400 && !finalMeta.UpstreamConsumed && finalMeta.Funding != nil && !finalMeta.Funding.Dispatched() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 10*time.Second)
		defer cancel()
		auth := finalMeta.Funding.Snapshot()
		if auth.Disposition == "authorization_required" {
			response, releaseErr := fundingControl.ReleaseFundingAuthorization(ctx, gatewaylite.ReleaseFundingAuthorizationRequest{AuthorizationID: auth.AuthorizationID, RequestID: auth.RequestID, GatewayCode: auth.GatewayCode, ExpectedVersion: auth.Version, OperationID: auth.RequestID + ":release:pre_dispatch", TerminalConfirmed: true, Reason: "pre_dispatch_failure"})
			if releaseErr != nil || response == nil || !response.OK {
				log.Printf("gateway-lite funding selection release pending request=%s", auth.RequestID)
			}
		} else {
			event := gatewaylite.UsageEvent{UserID: auth.UserID, KeyID: auth.KeyID, OrganizationID: auth.OrganizationID, BillingSubjectType: auth.BillingSubjectType, BillingSubjectID: auth.BillingSubjectID, Region: auth.Region, GatewayID: auth.GatewayCode, Model: auth.RequestedModel, RequestedModel: auth.RequestedModel, Method: c.Request.Method, Path: auth.Endpoint, Status: c.Writer.Status(), SettlementMode: string(gatewaylite.BillingSettlementModeIdempotentV2), EndedAtMillis: time.Now().UnixMilli()}
			gatewaylite.AttachFundingUsage(&event, auth, 0, 1, true)
			if err := reporter.ReportUsage(ctx, event); err != nil {
				log.Printf("gateway-lite funded error release failed request=%s error=%v", auth.RequestID, err)
			}
		}
	}
	return true
}

// gatewayLiteTurnAdmission freezes the original entry scope for every create
// frame. Keyword routing cannot substitute the connection's eventual group.
func gatewayLiteTurnAdmission(control GatewayLiteControlPlane, quota *gatewaylite.RedisQuota, key gatewaylite.KeySnapshot, base gatewaylite.RequestMeta, endpoint string) gatewaylite.TurnAdmission {
	return func(ctx context.Context, requestID, model string) (gatewaylite.RequestMeta, error) {
		meta := base
		meta.RequestID = requestID
		meta.Funding = nil
		meta.Reservation = nil
		meta.LeaseID = ""
		meta.ActualCentsOverride = nil
		meta.UpstreamConsumed = false
		meta.AsyncSettlement = false
		meta.AsyncPersistenceUncertain = false
		if quota == nil || !quota.Enabled() {
			return meta, gatewaylite.ErrRequestQuotaUnavailable
		}
		if funding, ok := control.(gatewaylite.FundingControlPlane); ok {
			response, err := funding.AuthorizeFunding(ctx, gatewaylite.AuthorizeFundingRequest{RequestID: requestID, KeyID: key.KeyID, UserID: key.UserID, GatewayCode: base.GatewayID, Region: base.Region, EntryGatewayNodeID: base.EntryGatewayNodeID, EntryGroupID: base.EntryGlobalGroupID, RequestedModel: model, Endpoint: endpoint, BoundKind: "selection", ExecutionDeadlineMillis: time.Now().Add(time.Hour).UnixMilli()})
			if err != nil || response == nil || !response.OK || response.Authorization == nil {
				return meta, errors.Join(gatewaylite.ErrRequestQuotaUnavailable, err)
			}
			a := response.Authorization.Clone()
			if a.Disposition == "" {
				a.Disposition = response.Disposition
			}
			if a.RequestID != requestID || a.KeyID != key.KeyID || a.UserID != key.UserID || a.OrganizationID != key.OrganizationID || a.EntryGroupID != base.EntryGlobalGroupID || a.EntryGatewayNodeID != base.EntryGatewayNodeID || a.RequestedModel != model {
				return meta, gatewaylite.ErrRequestQuotaUnavailable
			}
			if a.Disposition == "authorized" || a.Disposition == "authorization_required" {
				meta.Funding = gatewaylite.NewRequestFunding(a, funding, quota)
				meta.BillingSubjectType = a.BillingSubjectType
				meta.BillingSubjectID = a.BillingSubjectID
				meta.BillingSettlementMode = gatewaylite.BillingSettlementModeIdempotentV2
				return meta, nil
			}
			if a.Disposition != "legacy_balance" {
				return meta, gatewaylite.ErrRequestQuotaUnavailable
			}
		}
		estimate := gatewaylite.SalesRateReservationCents(meta.RateMultiplier)
		lease, err := gatewayLiteReserveQuota(ctx, control, quota, key, meta.Region, requestID, estimate)
		if err != nil {
			return meta, err
		}
		if lease == nil {
			return meta, gatewaylite.ErrRequestQuotaInsufficient
		}
		meta.LeaseID = lease.LeaseID
		meta.Reservation = gatewaylite.NewTurnRequestReservation(estimate, lease.LeaseID)
		// Per-turn V2 usage commits its own reservation in the atomic queue, so
		// neither the handshake nor a resp_* supplied upstream can own the hold.
		meta.BillingSettlementMode = gatewaylite.BillingSettlementModeIdempotentV2
		return meta, nil
	}
}

func gatewayLiteFundingCode(err error) string {
	if errors.Is(err, gatewaylite.ErrRequestQuotaInsufficient) {
		return "INSUFFICIENT_QUOTA"
	}
	if strings.Contains(err.Error(), "strict_card_endpoint_unsupported") {
		return "strict_card_endpoint_unsupported"
	}
	return "FUNDING_UNAVAILABLE"
}
