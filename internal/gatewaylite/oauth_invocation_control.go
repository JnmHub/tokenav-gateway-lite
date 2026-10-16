package gatewaylite

import (
	"context"
	"errors"
)

// ReleaseOAuthInvocationRequest is only for a proven zero-cost terminal outcome.
// An HTTP error or a cancelled connection is not that proof after dispatch.
func ReleaseOAuthInvocationRequest(ctx context.Context, terminalConfirmed bool) (resultErr error) {
	request, ok := OAuthInvocationRequestFromContext(ctx)
	if !ok {
		return nil
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	defer func() { resultErr = errors.Join(resultErr, request.checkpointLocked(ctx, false)) }()
	if !terminalConfirmed || request.snapshot.OutcomeUnknown || request.snapshot.ExecutionStarted || request.snapshot.DispatchUncertain {
		return ErrOAuthInvocationUnknown
	}
	return request.releaseLocked(ctx)
}

func (r *RequestOAuthInvocation) releaseLocked(ctx context.Context) error {
	if r.snapshot.Closed {
		return nil
	}
	if r.snapshot.OutcomeUnknown || r.snapshot.ExecutionStarted || r.snapshot.DispatchUncertain {
		return ErrOAuthInvocationUnknown
	}
	if r.snapshot.Reservation == nil {
		r.snapshot.Closed = true
		return nil
	}
	fundingID := ""
	if r.snapshot.Funding != nil {
		a := r.snapshot.Funding
		fundingID = r.snapshot.Reservation.FundingAuthorizationID
		r.snapshot.PendingPhase = "funding_release"
		if r.snapshot.PendingFundingRelease == nil {
			r.snapshot.PendingFundingRelease = &ReleaseFundingAuthorizationRequest{
				AuthorizationID: a.AuthorizationID, RequestID: a.RequestID, GatewayCode: a.GatewayCode,
				ExpectedVersion: a.Version, OperationID: a.RequestID + ":oauth-release:zero-cost", TerminalConfirmed: true, Reason: "oauth_invocation_zero_cost_terminal",
			}
		}
		if err := r.checkpointLocked(ctx, false); err != nil {
			return err
		}
		response, callErr := r.control.ReleaseFundingAuthorization(ctx, *r.snapshot.PendingFundingRelease)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
			return err
		}
		if !sameFundingScope(*a, *response.Authorization) || response.Authorization.Status != "released" {
			r.snapshot.OutcomeUnknown = true
			return ErrOAuthInvocationConflict
		}
		// Freeze the original executable snapshot, not the released empty items.
		// Recovery needs its immutable source/fee evidence for the other side.
	}
	reservation := r.snapshot.Reservation
	r.snapshot.PendingPhase = "delegation_release"
	if r.snapshot.PendingRelease == nil {
		r.snapshot.PendingRelease = &OAuthInvocationFinalizeRequest{
			ReservationID: reservation.ReservationID, RequestID: reservation.RequestID, GatewayCode: reservation.GatewayCode,
			FundingAuthorizationID: fundingID, OperationID: reservation.RequestID + ":oauth-release:zero-cost", ExpectedVersion: reservation.Version,
			Revision: 0, ActualMicroCents: 0, TerminalConfirmed: true,
		}
	}
	if err := r.checkpointLocked(ctx, false); err != nil {
		return err
	}
	response, callErr := r.control.ReleaseOAuthInvocation(ctx, *r.snapshot.PendingRelease)
	if err := oauthInvocationOperationError(callErr, response); err != nil {
		r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
		return err
	}
	next := *response.Reservation
	if !sameOAuthInvocationReservation(*reservation, next) || next.Status != "released" || next.ActualMicroCents != 0 || (next.FundingAuthorizationID != "" && next.FundingAuthorizationID != fundingID) {
		r.snapshot.OutcomeUnknown = true
		return ErrOAuthInvocationConflict
	}
	r.snapshot.Reservation = &next
	r.snapshot.PendingPhase = ""
	r.snapshot.Closed = true
	return nil
}

// EnsureOAuthInvocationFundingForContext is the shared funding hook after the
// initial service helper has attached its context. It resizes the two original
// authorizations and updates the funding pointer observed by detached workers.
func EnsureOAuthInvocationFundingForContext(ctx context.Context, microCents int64, evidence string) (bool, error) {
	request, ok := OAuthInvocationRequestFromContext(ctx)
	if !ok {
		if HasOAuthInvocationCredential(ctx) {
			return true, ErrOAuthInvocationConflict
		}
		return false, nil
	}
	if err := request.Ensure(ctx, microCents, evidence); err != nil {
		return true, err
	}
	if meta, hasMeta := RequestMetaFromContext(ctx); hasMeta && meta.Funding != nil {
		snapshot := request.Snapshot()
		meta.Funding.mu.Lock()
		meta.Funding.authorization = snapshot.Funding.Clone()
		meta.Funding.mu.Unlock()
	}
	return true, nil
}

type oauthInvocationUsageSnapshotKey struct{}
type oauthInvocationUsageSnapshot struct {
	reservation *OAuthInvocationReservation
}

// ContextWithOAuthInvocationUsageSnapshot carries only the original non-secret
// reservation to a detached usage worker. Copying a live credential or admission
// request would let a financial retry accidentally become permission to execute.
func ContextWithOAuthInvocationUsageSnapshot(base, parent context.Context) context.Context {
	if base == nil {
		base = context.Background()
	}
	reservation, ok := OAuthInvocationReservationFromContext(parent)
	if !ok && !HasOAuthInvocationCredential(parent) && !hasOAuthInvocationUsageSnapshot(parent) {
		return base
	}
	value := oauthInvocationUsageSnapshot{}
	if ok {
		value.reservation = &reservation
	}
	return context.WithValue(base, oauthInvocationUsageSnapshotKey{}, value)
}

func hasOAuthInvocationUsageSnapshot(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(oauthInvocationUsageSnapshotKey{}).(oauthInvocationUsageSnapshot)
	return ok
}

func OAuthInvocationReservationFromContext(ctx context.Context) (OAuthInvocationReservation, bool) {
	request, ok := OAuthInvocationRequestFromContext(ctx)
	if !ok {
		if ctx != nil {
			if value, copied := ctx.Value(oauthInvocationUsageSnapshotKey{}).(oauthInvocationUsageSnapshot); copied && value.reservation != nil {
				return *value.reservation, true
			}
		}
		return OAuthInvocationReservation{}, false
	}
	snapshot := request.Snapshot()
	if snapshot.Reservation == nil {
		return OAuthInvocationReservation{}, false
	}
	return *snapshot.Reservation, true
}

// RestoreOAuthInvocationRequest attaches only frozen, non-secret evidence. It
// is for usage/recovery, never new execution or a new delegated grant.
func RestoreOAuthInvocationRequest(ctx context.Context, snapshot OAuthInvocationRequestSnapshot, control OAuthInvocationFundingControl, installer OAuthInvocationFundingInstaller) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if snapshot.ReserveRequest.RequestID == "" || snapshot.ReserveRequest.DelegationID == "" || snapshot.ReserveRequest.GatewayCode == "" ||
		snapshot.ReserveRequest.RequestID != snapshot.FundingRequest.RequestID || snapshot.ReserveRequest.KeyID != snapshot.FundingRequest.KeyID ||
		(snapshot.Reservation != nil && !sameOAuthInvocationReserve(snapshot.ReserveRequest, *snapshot.Reservation)) {
		return ctx, ErrOAuthInvocationConflict
	}
	request := &RequestOAuthInvocation{snapshot: cloneOAuthInvocationRequestSnapshot(snapshot), control: control, installer: installer, blocked: true}
	if snapshot.Funding != nil && (!request.fundingMatchesIdentity(*snapshot.Funding) ||
		(snapshot.Reservation != nil && snapshot.Reservation.FundingAuthorizationID != "" && !sameOAuthInvocationFunding(*snapshot.Reservation, *snapshot.Funding))) {
		return ctx, ErrOAuthInvocationConflict
	}
	return context.WithValue(ctx, oauthInvocationRequestKey{}, request), nil
}

func sameOAuthInvocationFunding(reservation OAuthInvocationReservation, funding FundingAuthorization) bool {
	return reservation.FundingAuthorizationID == funding.AuthorizationID && reservation.RequestID == funding.RequestID &&
		reservation.KeyID == funding.KeyID && reservation.GatewayCode == funding.GatewayCode &&
		reservation.EntryGatewayNodeID == funding.EntryGatewayNodeID && reservation.EntryGroupID == funding.EntryGroupID &&
		reservation.RequestedModel == funding.RequestedModel && reservation.Endpoint == funding.Endpoint
}
