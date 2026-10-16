package gatewaylite

import (
	"context"
	"errors"
)

// RecoverOAuthInvocationRequest resolves only financial uncertainty. It never
// dispatches an upstream or treats the recovered old grant as permission for a
// new execution. A caller must explicitly settle/release a proven terminal cost.
func RecoverOAuthInvocationRequest(ctx context.Context) (OAuthInvocationRequestSnapshot, error) {
	request, ok := OAuthInvocationRequestFromContext(ctx)
	if !ok {
		return OAuthInvocationRequestSnapshot{}, ErrOAuthInvocationConflict
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	request.blocked = true
	// Recovery may replay the original authorize operation, so it must still own
	// the SQL generation. A consumer that fenced this request owns all later writes.
	if err := request.checkpointLocked(ctx, false); err != nil {
		return cloneOAuthInvocationRequestSnapshot(request.snapshot), err
	}
	statusControl, supported := request.control.(OAuthInvocationStatusControl)
	if !supported {
		return cloneOAuthInvocationRequestSnapshot(request.snapshot), ErrOAuthInvocationUnknown
	}
	lookup := OAuthInvocationStatusRequest{RequestID: request.snapshot.ReserveRequest.RequestID,
		GatewayCode: request.snapshot.ReserveRequest.GatewayCode, DelegationID: request.snapshot.ReserveRequest.DelegationID}
	if request.snapshot.Reservation != nil {
		lookup.ReservationID = request.snapshot.Reservation.ReservationID
	}
	response, callErr := statusControl.OAuthInvocationStatus(ctx, lookup)
	if err := oauthInvocationOperationError(callErr, response); err != nil {
		// A missing row is not an atomic no-commit barrier for an in-flight call.
		return cloneOAuthInvocationRequestSnapshot(request.snapshot), errors.Join(ErrOAuthInvocationUnknown, err)
	}
	next := *response.Reservation
	if !sameOAuthInvocationReserve(request.snapshot.ReserveRequest, next) ||
		(request.snapshot.Reservation != nil && !sameOAuthInvocationReservation(*request.snapshot.Reservation, next)) {
		return cloneOAuthInvocationRequestSnapshot(request.snapshot), ErrOAuthInvocationConflict
	}
	request.snapshot.Reservation = &next
	if request.snapshot.Funding == nil && request.snapshot.PendingPhase == "authorize" {
		fundingResponse, err := request.control.AuthorizeFunding(ctx, request.snapshot.FundingRequest)
		if err := oauthInvocationFundingError(err, fundingResponse); err != nil {
			return cloneOAuthInvocationRequestSnapshot(request.snapshot), errors.Join(ErrOAuthInvocationUnknown, err)
		}
		funding := fundingResponse.Authorization.Clone()
		if funding.Disposition == "" {
			funding.Disposition = fundingResponse.Disposition
		}
		if !request.fundingMatchesIdentity(funding) {
			return cloneOAuthInvocationRequestSnapshot(request.snapshot), ErrOAuthInvocationConflict
		}
		request.snapshot.Funding = &funding
	}
	if request.snapshot.Funding != nil {
		fundingResponse, err := request.control.LookupFundingAuthorization(ctx, request.snapshot.Funding.AuthorizationID)
		if err := oauthInvocationFundingError(err, fundingResponse); err != nil {
			return cloneOAuthInvocationRequestSnapshot(request.snapshot), errors.Join(ErrOAuthInvocationUnknown, err)
		}
		funding := fundingResponse.Authorization.Clone()
		if !sameFundingScope(*request.snapshot.Funding, funding) {
			return cloneOAuthInvocationRequestSnapshot(request.snapshot), ErrOAuthInvocationConflict
		}
		if next.FundingAuthorizationID != "" && next.FundingAuthorizationID != funding.AuthorizationID {
			return cloneOAuthInvocationRequestSnapshot(request.snapshot), ErrOAuthInvocationConflict
		}
		if funding.Status == "active" {
			if validateFundingAuthorization(funding) != nil {
				return cloneOAuthInvocationRequestSnapshot(request.snapshot), ErrOAuthInvocationConflict
			}
			request.snapshot.Funding = &funding
		}
	}
	request.snapshot.OutcomeUnknown = false
	if next.Status == "released" || next.Status == "settled" {
		request.snapshot.Closed = true
	}
	// Retain PendingPhase and the original expected-version payload. If a release
	// reply was lost, replay exactly that operation; a new version changes its
	// persistent fingerprint and is correctly rejected as a conflicting action.
	return cloneOAuthInvocationRequestSnapshot(request.snapshot), nil
}

func (r *RequestOAuthInvocation) fundingMatchesIdentity(a FundingAuthorization) bool {
	req := r.snapshot.FundingRequest
	return a.AuthorizationID != "" && a.RequestID == req.RequestID && a.KeyID == req.KeyID && a.UserID == req.UserID &&
		a.OrganizationID == 0 && a.BillingSubjectType == "user" && a.BillingSubjectID == req.UserID &&
		a.GatewayCode == req.GatewayCode && a.Region == req.Region && a.EntryGatewayNodeID == req.EntryGatewayNodeID &&
		a.EntryGroupID == req.EntryGroupID && a.RequestedModel == req.RequestedModel && a.Endpoint == req.Endpoint
}
