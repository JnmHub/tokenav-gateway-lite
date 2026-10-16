package gatewaylite

import (
	"context"
	"errors"
)

// replayPending reuses the exact payload saved before a remote mutation. It does
// not choose a current version for an old OperationID, rebind a Key or dispatch.
func (o *OAuthInvocationRecoveryOutbox) replayPending(ctx context.Context, item *oauthInvocationRecoveryItem, s *OAuthInvocationRequestSnapshot) error {
	if err := o.saveClaim(ctx, item, *s, "pending", nil); err != nil {
		return err
	}
	var reservation *OAuthInvocationReservation
	var funding *FundingAuthorization
	phase := s.PendingPhase
	switch phase {
	case "":
		if s.Reservation == nil {
			// SQL begin committed, but the reserve-before checkpoint never did.
			// The former owner is fenced, so no remote hold could have started.
			s.Closed = true
		}
		return nil
	case "reserve":
		response, callErr := o.control.ReserveOAuthInvocation(ctx, s.ReserveRequest)
		if err := oauthInvocationOperationError(callErr, response); err != nil {
			return err
		}
		reservation = response.Reservation
	case "authorize":
		response, callErr := o.control.AuthorizeFunding(ctx, s.FundingRequest)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			return err
		}
		funding = response.Authorization
	case "bind":
		if s.PendingBind == nil {
			return ErrOAuthInvocationConflict
		}
		response, callErr := o.control.BindOAuthInvocation(ctx, *s.PendingBind)
		if err := oauthInvocationOperationError(callErr, response); err != nil {
			return err
		}
		reservation = response.Reservation
	case "funding_resize":
		if s.PendingFundingResize == nil {
			return ErrOAuthInvocationConflict
		}
		response, callErr := o.control.ResizeFundingAuthorization(ctx, *s.PendingFundingResize)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			return err
		}
		funding = response.Authorization
	case "delegation_resize":
		if s.PendingResize == nil {
			return ErrOAuthInvocationConflict
		}
		response, callErr := o.control.ResizeOAuthInvocation(ctx, *s.PendingResize)
		if err := oauthInvocationOperationError(callErr, response); err != nil {
			return err
		}
		reservation = response.Reservation
	case "funding_release":
		if s.PendingFundingRelease == nil {
			return ErrOAuthInvocationConflict
		}
		response, callErr := o.control.ReleaseFundingAuthorization(ctx, *s.PendingFundingRelease)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			return err
		}
		funding = response.Authorization
		if funding.Status != "released" || funding.ActualMicroCents != 0 {
			return ErrOAuthInvocationConflict
		}
	case "delegation_release":
		if s.PendingRelease == nil || s.PendingRelease.Revision != 0 || s.PendingRelease.ActualMicroCents != 0 {
			return ErrOAuthInvocationConflict
		}
		response, callErr := o.control.ReleaseOAuthInvocation(ctx, *s.PendingRelease)
		if err := oauthInvocationOperationError(callErr, response); err != nil {
			return err
		}
		reservation = response.Reservation
		if reservation.Status != "released" || reservation.ActualMicroCents != 0 {
			return ErrOAuthInvocationConflict
		}
		s.Closed = true
	default:
		return ErrOAuthInvocationConflict
	}
	if reservation != nil {
		if !sameOAuthInvocationReserve(s.ReserveRequest, *reservation) ||
			(s.Reservation != nil && !sameOAuthInvocationReservation(*s.Reservation, *reservation)) ||
			(reservation.FundingAuthorizationID != "" && (s.Funding == nil || s.Funding.AuthorizationID != reservation.FundingAuthorizationID)) {
			return ErrOAuthInvocationConflict
		}
		copy := *reservation
		s.Reservation = &copy
	}
	if funding != nil {
		copy := funding.Clone()
		if copy.Disposition == "" {
			copy.Disposition = "authorized"
		}
		if !(&RequestOAuthInvocation{snapshot: *s}).fundingMatchesIdentity(copy) ||
			(s.Funding != nil && (s.Funding.AuthorizationID != copy.AuthorizationID || !sameFundingScope(*s.Funding, copy))) {
			return ErrOAuthInvocationConflict
		}
		s.Funding = &copy
	}
	s.PendingPhase, s.OutcomeUnknown = "", false
	if err := o.saveClaim(ctx, item, *s, "pending", nil); err != nil {
		return errors.Join(ErrOAuthInvocationUnavailable, err)
	}
	return nil
}
