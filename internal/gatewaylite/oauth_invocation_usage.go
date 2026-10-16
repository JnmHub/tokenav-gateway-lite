package gatewaylite

import (
	"context"
	"errors"
	"fmt"
)

// AttachOAuthInvocationUsage follows AttachFundingUsage, so both systems freeze
// precisely the same actual micro-cents and revision from the actual price path.
func AttachOAuthInvocationUsage(ctx context.Context, event *UsageEvent) error {
	reservation, delegated := OAuthInvocationReservationFromContext(ctx)
	if !delegated {
		if HasOAuthInvocationCredential(ctx) || hasOAuthInvocationUsageSnapshot(ctx) {
			return ErrOAuthInvocationConflict
		}
		return nil
	}
	if event == nil || event.FundingSnapshot == nil || event.ActualMicroCents == nil || event.SettlementRevision <= 0 ||
		reservation.Status != "active" || !sameOAuthInvocationFunding(reservation, *event.FundingSnapshot) ||
		event.RequestID != reservation.RequestID || event.FundingAuthorizationID != reservation.FundingAuthorizationID {
		return ErrOAuthInvocationConflict
	}
	value := reservation
	event.OAuthInvocationReservation = &value
	return nil
}

// OAuthInvocationUsageSink wraps only the queue's delivery sink. ReportUsage
// returns success after the original funding usage is durable AND the delegated
// spend is confirmed. The Redis queue therefore retries either ambiguous side
// with the same frozen snapshot and does not ACK half a financial operation.
type OAuthInvocationUsageSink struct {
	base    UsageReportClient
	control OAuthInvocationControlPlane
}

func NewOAuthInvocationUsageSink(base UsageReportClient, control OAuthInvocationControlPlane) *OAuthInvocationUsageSink {
	return &OAuthInvocationUsageSink{base: base, control: control}
}

func (s *OAuthInvocationUsageSink) ReportUsage(ctx context.Context, event UsageEvent) error {
	if s == nil || s.base == nil {
		return ErrOAuthInvocationUnavailable
	}
	if event.OAuthInvocationReservation == nil {
		return s.base.ReportUsage(ctx, event)
	}
	if s.control == nil {
		return ErrOAuthInvocationUnavailable
	}
	if err := validateOAuthInvocationUsage(event); err != nil {
		return err
	}
	if err := s.base.ReportUsage(ctx, event); err != nil {
		return err
	}
	return s.finalize(ctx, event)
}

func (s *OAuthInvocationUsageSink) ReportUsageBatch(ctx context.Context, events []UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	if s == nil || s.base == nil {
		return ErrOAuthInvocationUnavailable
	}
	for _, event := range events {
		if event.OAuthInvocationReservation == nil {
			continue
		}
		if s.control == nil {
			return ErrOAuthInvocationUnavailable
		}
		if err := validateOAuthInvocationUsage(event); err != nil {
			return err
		}
	}
	if batch, ok := s.base.(UsageBatchReportClient); ok {
		if err := batch.ReportUsageBatch(ctx, events); err != nil {
			return err
		}
	} else {
		for _, event := range events {
			if err := s.base.ReportUsage(ctx, event); err != nil {
				return err
			}
		}
	}
	for _, event := range events {
		if event.OAuthInvocationReservation != nil {
			if err := s.finalize(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateOAuthInvocationUsage(event UsageEvent) error {
	reservation := event.OAuthInvocationReservation
	if reservation == nil {
		return nil
	}
	if event.FundingSnapshot == nil || event.ActualMicroCents == nil || *event.ActualMicroCents < 0 || *event.ActualMicroCents > maxFundingExactInteger ||
		event.SettlementRevision <= 0 || reservation.ReservationID == "" || reservation.Version <= 0 ||
		reservation.FundingAuthorizationID == "" || !sameOAuthInvocationFunding(*reservation, *event.FundingSnapshot) ||
		event.RequestID != reservation.RequestID || event.KeyID != reservation.KeyID || event.GatewayID != reservation.GatewayCode ||
		event.FundingAuthorizationID != reservation.FundingAuthorizationID || event.FundingRevision != event.FundingSnapshot.Version ||
		event.UserID != event.FundingSnapshot.UserID || event.OrganizationID != event.FundingSnapshot.OrganizationID ||
		event.BillingSubjectType != event.FundingSnapshot.BillingSubjectType || event.BillingSubjectID != event.FundingSnapshot.BillingSubjectID {
		return ErrOAuthInvocationConflict
	}
	return nil
}

func (s *OAuthInvocationUsageSink) finalize(ctx context.Context, event UsageEvent) error {
	reservation := event.OAuthInvocationReservation
	expectedVersion := reservation.Version
	if status, supported := s.control.(OAuthInvocationStatusControl); supported {
		response, callErr := status.OAuthInvocationStatus(ctx, OAuthInvocationStatusRequest{ReservationID: reservation.ReservationID,
			RequestID: reservation.RequestID, GatewayCode: reservation.GatewayCode, DelegationID: reservation.DelegationID})
		if err := oauthInvocationOperationError(callErr, response); err != nil {
			return err
		}
		if !sameOAuthInvocationReservation(*reservation, *response.Reservation) || response.Reservation.FundingAuthorizationID != reservation.FundingAuthorizationID {
			return ErrOAuthInvocationConflict
		}
		expectedVersion = response.Reservation.Version
	}
	// A new revision needs the current version. Include the chosen version in the
	// operation ID so a reply-loss replay never changes that ID's frozen input;
	// the durable original revision amount prevents charging twice across IDs.
	request := OAuthInvocationFinalizeRequest{ReservationID: reservation.ReservationID, RequestID: reservation.RequestID,
		GatewayCode: reservation.GatewayCode, FundingAuthorizationID: reservation.FundingAuthorizationID,
		OperationID:     fmt.Sprintf("%s:oauth-settle:%d:v%d", reservation.RequestID, event.SettlementRevision, expectedVersion),
		ExpectedVersion: expectedVersion, Revision: event.SettlementRevision,
		ActualMicroCents: *event.ActualMicroCents, TerminalConfirmed: event.Final}
	response, callErr := s.control.SettleOAuthInvocation(ctx, request)
	if err := oauthInvocationOperationError(callErr, response); err != nil {
		return err
	}
	confirmed := response.Reservation
	if !sameOAuthInvocationReservation(*reservation, *confirmed) || confirmed.FundingAuthorizationID != reservation.FundingAuthorizationID {
		return ErrOAuthInvocationConflict
	}
	// A higher revision means a newer correction already committed. Older replay
	// may safely ACK, but the exact revision must freeze the exact same cost.
	if confirmed.Revision < request.Revision || (confirmed.Revision == request.Revision && confirmed.ActualMicroCents != request.ActualMicroCents) {
		return ErrOAuthInvocationConflict
	}
	if request.TerminalConfirmed && confirmed.Status != "settled" && confirmed.Status != "released" {
		return errors.Join(ErrOAuthInvocationUnknown, errors.New("delegation terminal settlement not confirmed"))
	}
	return nil
}
