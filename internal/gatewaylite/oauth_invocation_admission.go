package gatewaylite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// OAuthInvocationRequestSnapshot is non-secret durable recovery evidence. Both
// systems share the original request, scope, bound and funding authorization.
type OAuthInvocationRequestSnapshot struct {
	ReserveRequest        OAuthInvocationReserveRequest       `json:"reserve_request"`
	FundingRequest        AuthorizeFundingRequest             `json:"funding_request"`
	Reservation           *OAuthInvocationReservation         `json:"reservation,omitempty"`
	Funding               *FundingAuthorization               `json:"funding,omitempty"`
	PendingPhase          string                              `json:"pending_phase,omitempty"`
	PendingBind           *OAuthInvocationBindRequest         `json:"pending_bind,omitempty"`
	PendingFundingResize  *ResizeFundingAuthorizationRequest  `json:"pending_funding_resize,omitempty"`
	PendingResize         *OAuthInvocationResizeRequest       `json:"pending_resize,omitempty"`
	PendingFundingRelease *ReleaseFundingAuthorizationRequest `json:"pending_funding_release,omitempty"`
	PendingRelease        *OAuthInvocationFinalizeRequest     `json:"pending_release,omitempty"`
	OutcomeUnknown        bool                                `json:"outcome_unknown,omitempty"`
	DispatchUncertain     bool                                `json:"dispatch_uncertain,omitempty"`
	ExecutionStarted      bool                                `json:"execution_started,omitempty"`
	Closed                bool                                `json:"closed,omitempty"`
}

type RequestOAuthInvocation struct {
	mu               sync.Mutex
	snapshot         OAuthInvocationRequestSnapshot
	control          OAuthInvocationFundingControl
	installer        OAuthInvocationFundingInstaller
	recovery         *oauthInvocationRecoveryLease
	recoveryFinished bool
	blocked          bool
	ready            bool
}

func (r *RequestOAuthInvocation) Snapshot() OAuthInvocationRequestSnapshot {
	if r == nil {
		return OAuthInvocationRequestSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneOAuthInvocationRequestSnapshot(r.snapshot)
}

func cloneOAuthInvocationRequestSnapshot(snapshot OAuthInvocationRequestSnapshot) OAuthInvocationRequestSnapshot {
	if snapshot.Reservation != nil {
		value := *snapshot.Reservation
		snapshot.Reservation = &value
	}
	if snapshot.Funding != nil {
		value := snapshot.Funding.Clone()
		value.BoundEvidence = cloneOAuthInvocationProof(value.BoundEvidence)
		snapshot.Funding = &value
	}
	snapshot.FundingRequest.BoundEvidence = cloneOAuthInvocationProof(snapshot.FundingRequest.BoundEvidence)
	if snapshot.PendingFundingResize != nil {
		value := *snapshot.PendingFundingResize
		value.BoundEvidence = cloneOAuthInvocationProof(value.BoundEvidence)
		snapshot.PendingFundingResize = &value
	}
	if snapshot.PendingBind != nil {
		value := *snapshot.PendingBind
		snapshot.PendingBind = &value
	}
	if snapshot.PendingResize != nil {
		value := *snapshot.PendingResize
		snapshot.PendingResize = &value
	}
	if snapshot.PendingFundingRelease != nil {
		value := *snapshot.PendingFundingRelease
		snapshot.PendingFundingRelease = &value
	}
	if snapshot.PendingRelease != nil {
		value := *snapshot.PendingRelease
		snapshot.PendingRelease = &value
	}
	return snapshot
}

// EnsureOAuthInvocationUpperBound returns its context even on an error so the
// finalizer can retain/recover a successful allocation whose reply was lost.
func EnsureOAuthInvocationUpperBound(ctx context.Context, control OAuthInvocationFundingControl, installer OAuthInvocationFundingInstaller, microCents int64, evidence, model, endpoint string) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	credential, _ := ctx.Value(oauthInvocationCredentialKey{}).(*oauthInvocationCredential)
	if credential == nil {
		return ctx, nil
	}
	if control == nil || installer == nil || microCents <= 0 || microCents > maxFundingExactInteger || strings.TrimSpace(evidence) == "" {
		return ctx, ErrOAuthInvocationUnavailable
	}
	if !OAuthInvocationEndpointSupported(endpoint) {
		return ctx, ErrOAuthInvocationUnsupported
	}
	meta, ok := RequestMetaFromContext(ctx)
	if !ok || strings.TrimSpace(meta.RequestID) == "" || meta.LeaseID != "" || meta.Reservation != nil {
		return ctx, ErrOAuthInvocationConflict
	}
	proof := FundingBoundEvidence(evidence)
	if structured := oauthInvocationBoundProofFromContext(ctx); structured != nil {
		proof = structured
	}
	proof["endpoint"] = endpoint
	proof["policy_revision"] = meta.PolicyRevision
	proof["policy_checksum"] = meta.PolicyChecksum
	if meta.PriceVersion > 0 {
		proof["price_version"] = meta.PriceVersion
	}
	request, existing := OAuthInvocationRequestFromContext(ctx)
	if !existing {
		// Never cache grant lifecycle between HTTP requests or response.create turns.
		resolved, err := ResolveOAuthInvocationCredential(ctx, control, credential.rawToken, credential.gateway, credential.region)
		if err != nil {
			return ctx, err
		}
		key, consent := resolved.Key, resolved.Invocation
		if key.KeyID != meta.KeyID || key.UserID != meta.UserID || meta.OrganizationID != 0 ||
			meta.GatewayID != credential.gateway || meta.EntryGatewayNodeID != consent.GatewayNodeID ||
			meta.EntryGlobalGroupID != consent.GroupID || !oauthInvocationModelAllowed(*consent, model) {
			return ctx, ErrOAuthInvocationDenied
		}
		if microCents > consent.MaxCostCents*actualCostMicroCentsScale {
			return ctx, ErrRequestQuotaInsufficient
		}
		deadline := min(resolved.ExpiresAtMillis, time.Now().Add(time.Hour).UnixMilli())
		if deadline <= time.Now().UnixMilli() {
			return ctx, ErrOAuthInvocationExpired
		}
		reserve := OAuthInvocationReserveRequest{DelegationID: resolved.DelegationID, RequestID: meta.RequestID,
			GatewayCode: credential.gateway, KeyID: meta.KeyID, EntryGatewayNodeID: consent.GatewayNodeID,
			EntryGroupID: consent.GroupID, RequestedModel: strings.TrimSpace(model), Endpoint: endpoint,
			UpperBoundMicroCents: microCents, ExecutionDeadlineMillis: deadline}
		funding := AuthorizeFundingRequest{ForceAuthorization: true, RequestID: reserve.RequestID, KeyID: reserve.KeyID,
			UserID: meta.UserID, GatewayCode: reserve.GatewayCode, Region: meta.Region,
			EntryGatewayNodeID: reserve.EntryGatewayNodeID, EntryGroupID: reserve.EntryGroupID,
			RequestedModel: reserve.RequestedModel, Endpoint: endpoint, UpperBoundMicroCents: microCents,
			BoundKind: "bounded", BoundEvidence: proof,
			ExecutionDeadlineMillis: deadline}
		request = &RequestOAuthInvocation{control: control, installer: installer,
			snapshot: OAuthInvocationRequestSnapshot{ReserveRequest: reserve, FundingRequest: funding}}
		ctx = context.WithValue(ctx, oauthInvocationRequestKey{}, request)
		if outbox := DefaultOAuthInvocationRecoveryOutbox(); outbox != nil {
			lease, persistErr := outbox.begin(ctx, request.snapshot)
			if persistErr != nil {
				request.blocked = true
				return ctx, errors.Join(ErrOAuthInvocationUnavailable, persistErr)
			}
			request.recovery = lease
		}
	}
	snapshot := request.Snapshot()
	if snapshot.ReserveRequest.RequestID != meta.RequestID || snapshot.ReserveRequest.KeyID != meta.KeyID ||
		snapshot.ReserveRequest.RequestedModel != strings.TrimSpace(model) || snapshot.ReserveRequest.Endpoint != endpoint {
		return ctx, ErrOAuthInvocationConflict
	}
	if err := request.Ensure(ctx, microCents, evidence); err != nil {
		return ctx, err
	}
	snapshot = request.Snapshot()
	meta.LeaseID, meta.Reservation = "", nil
	meta.BillingSubjectType = snapshot.Funding.BillingSubjectType
	meta.BillingSubjectID = snapshot.Funding.BillingSubjectID
	meta.BillingSettlementMode = BillingSettlementModeIdempotentV2
	quota, _ := installer.(*RedisQuota)
	meta.Funding = NewRequestFunding(*snapshot.Funding, control, quota)
	return ContextWithRequestMeta(ctx, meta), nil
}

func (r *RequestOAuthInvocation) Ensure(ctx context.Context, microCents int64, evidence string) (resultErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { resultErr = errors.Join(resultErr, r.checkpointLocked(ctx, false)) }()
	if r.snapshot.Closed || r.blocked || microCents <= 0 || microCents > maxFundingExactInteger {
		return ErrOAuthInvocationConflict
	}
	if r.control == nil || r.installer == nil {
		return ErrOAuthInvocationUnavailable
	}
	if r.snapshot.OutcomeUnknown {
		return ErrOAuthInvocationUnknown
	}
	if r.snapshot.ReserveRequest.ExecutionDeadlineMillis <= time.Now().UnixMilli() {
		return ErrOAuthInvocationExpired
	}
	if r.snapshot.Reservation == nil {
		r.snapshot.PendingPhase = "reserve"
		if err := r.checkpointLocked(ctx, false); err != nil {
			return err
		}
		response, err := r.control.ReserveOAuthInvocation(ctx, r.snapshot.ReserveRequest)
		if err := oauthInvocationOperationError(err, response); err != nil {
			r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
			return err
		}
		reservation := *response.Reservation
		if !sameOAuthInvocationReserve(r.snapshot.ReserveRequest, reservation) || reservation.Status != "reserved" || reservation.FundingAuthorizationID != "" {
			r.snapshot.OutcomeUnknown = true
			return ErrOAuthInvocationConflict
		}
		r.snapshot.Reservation = &reservation
		r.snapshot.PendingPhase = ""
	}
	if r.snapshot.Funding == nil {
		r.snapshot.PendingPhase = "authorize"
		if err := r.checkpointLocked(ctx, false); err != nil {
			return err
		}
		response, callErr := r.control.AuthorizeFunding(ctx, r.snapshot.FundingRequest)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
			if !r.snapshot.OutcomeUnknown {
				return errors.Join(err, r.releaseLocked(ctx))
			}
			return err
		}
		authorization := response.Authorization.Clone()
		if authorization.Disposition == "" {
			authorization.Disposition = response.Disposition
		}
		if !r.fundingMatches(authorization) {
			r.snapshot.OutcomeUnknown = true
			return ErrOAuthInvocationConflict
		}
		r.snapshot.Funding = &authorization
		r.snapshot.PendingPhase = ""
	}
	reservation := r.snapshot.Reservation
	if reservation.Status == "reserved" {
		r.snapshot.PendingPhase = "bind"
		pending := oauthInvocationBindRequest(*reservation, r.snapshot.Funding.AuthorizationID)
		r.snapshot.PendingBind = &pending
		if err := r.checkpointLocked(ctx, false); err != nil {
			return err
		}
		response, callErr := r.control.BindOAuthInvocation(ctx, pending)
		if err := oauthInvocationOperationError(callErr, response); err != nil {
			r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
			if !r.snapshot.OutcomeUnknown {
				return errors.Join(err, r.releaseLocked(ctx))
			}
			return err
		}
		next := *response.Reservation
		if !sameOAuthInvocationReservation(*reservation, next) || next.Status != "active" || next.FundingAuthorizationID != r.snapshot.Funding.AuthorizationID || next.Version < reservation.Version {
			r.snapshot.OutcomeUnknown = true
			return ErrOAuthInvocationConflict
		}
		r.snapshot.Reservation = &next
		r.snapshot.PendingPhase = ""
		r.snapshot.PendingBind = nil
	}
	if r.snapshot.Reservation.Status != "active" {
		return ErrOAuthInvocationConflict
	}
	if microCents > r.snapshot.Reservation.UpperBoundMicroCents {
		r.ready = false
		if err := r.resizeLocked(ctx, microCents, evidence); err != nil {
			return err
		}
	}
	if r.snapshot.Reservation.UpperBoundMicroCents < microCents || r.snapshot.Funding.UpperBoundMicroCents < microCents {
		return ErrOAuthInvocationConflict
	}
	r.ready = false
	if err := r.installer.ReserveFunding(ctx, *r.snapshot.Funding); err != nil {
		// No upstream has started, but Redis may have committed the claim. Keep the
		// two main-site allocations and retry that exact local snapshot.
		return errors.Join(ErrOAuthInvocationUnavailable, err)
	}
	r.ready = true
	return nil
}

func (r *RequestOAuthInvocation) resizeLocked(ctx context.Context, microCents int64, evidence string) error {
	proof := FundingBoundEvidence(evidence)
	if structured := oauthInvocationBoundProofFromContext(ctx); structured != nil {
		proof = structured
	}
	proof["endpoint"] = r.snapshot.ReserveRequest.Endpoint
	authorization := r.snapshot.Funding
	if authorization.UpperBoundMicroCents < microCents {
		pending := ResizeFundingAuthorizationRequest{
			AuthorizationID: authorization.AuthorizationID, RequestID: authorization.RequestID, GatewayCode: authorization.GatewayCode,
			ExpectedVersion: authorization.Version, OperationID: fmt.Sprintf("%s:oauth-resize:%d:%d", authorization.RequestID, authorization.Version, microCents),
			UpperBoundMicroCents: microCents, BoundEvidence: proof,
		}
		r.snapshot.PendingPhase = "funding_resize"
		r.snapshot.PendingFundingResize = &pending
		if err := r.checkpointLocked(ctx, false); err != nil {
			return err
		}
		response, callErr := r.control.ResizeFundingAuthorization(ctx, pending)
		if err := oauthInvocationFundingError(callErr, response); err != nil {
			r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
			return err
		}
		next := response.Authorization.Clone()
		if next.Disposition == "" {
			next.Disposition = response.Disposition
		}
		if !sameFundingScope(*authorization, next) || next.Version < authorization.Version || next.UpperBoundMicroCents < microCents || !r.fundingMatches(next) {
			r.snapshot.OutcomeUnknown = true
			return ErrOAuthInvocationConflict
		}
		r.snapshot.Funding = &next
		r.snapshot.PendingPhase = ""
		r.snapshot.PendingFundingResize = nil
	}
	reservation := r.snapshot.Reservation
	bind := oauthInvocationBindRequest(*reservation, r.snapshot.Funding.AuthorizationID)
	bind.OperationID = fmt.Sprintf("%s:oauth-resize:%d:%d", reservation.RequestID, reservation.Version, microCents)
	pending := OAuthInvocationResizeRequest{OAuthInvocationBindRequest: bind, UpperBoundMicroCents: microCents}
	r.snapshot.PendingPhase = "delegation_resize"
	r.snapshot.PendingResize = &pending
	if err := r.checkpointLocked(ctx, false); err != nil {
		return err
	}
	response, callErr := r.control.ResizeOAuthInvocation(ctx, pending)
	if err := oauthInvocationOperationError(callErr, response); err != nil {
		r.snapshot.OutcomeUnknown = errors.Is(err, ErrOAuthInvocationUnknown)
		if !r.snapshot.OutcomeUnknown {
			r.blocked = true
			if !r.snapshot.ExecutionStarted {
				return errors.Join(err, r.releaseLocked(ctx))
			}
		}
		return err
	}
	next := *response.Reservation
	if !sameOAuthInvocationReservation(*reservation, next) || next.FundingAuthorizationID != r.snapshot.Funding.AuthorizationID || next.Status != "active" || next.Version < reservation.Version || next.UpperBoundMicroCents < microCents {
		r.snapshot.OutcomeUnknown = true
		return ErrOAuthInvocationConflict
	}
	r.snapshot.Reservation = &next
	r.snapshot.PendingPhase = ""
	r.snapshot.PendingResize = nil
	return nil
}

func (r *RequestOAuthInvocation) fundingMatches(a FundingAuthorization) bool {
	req := r.snapshot.FundingRequest
	return a.RequestID == req.RequestID && a.KeyID == req.KeyID && a.UserID == req.UserID && a.OrganizationID == 0 &&
		a.BillingSubjectType == "user" && a.BillingSubjectID == req.UserID && a.GatewayCode == req.GatewayCode && a.Region == req.Region &&
		a.EntryGatewayNodeID == req.EntryGatewayNodeID && a.EntryGroupID == req.EntryGroupID && a.RequestedModel == req.RequestedModel &&
		a.Endpoint == req.Endpoint && a.Status == "active" && a.Disposition == "authorized" &&
		a.ExecutionDeadlineMillis > time.Now().UnixMilli() && a.ExecutionDeadlineMillis <= req.ExecutionDeadlineMillis &&
		a.UpperBoundMicroCents >= req.UpperBoundMicroCents && validateFundingAuthorization(a) == nil
}

func sameOAuthInvocationReserve(req OAuthInvocationReserveRequest, reservation OAuthInvocationReservation) bool {
	return reservation.ReservationID != "" && reservation.Version > 0 && reservation.DelegationID == req.DelegationID &&
		reservation.RequestID == req.RequestID && reservation.GatewayCode == req.GatewayCode && reservation.KeyID == req.KeyID &&
		reservation.EntryGatewayNodeID == req.EntryGatewayNodeID && reservation.EntryGroupID == req.EntryGroupID &&
		reservation.RequestedModel == req.RequestedModel && reservation.Endpoint == req.Endpoint &&
		reservation.ExecutionDeadlineMillis == req.ExecutionDeadlineMillis && reservation.UpperBoundMicroCents >= req.UpperBoundMicroCents
}

func sameOAuthInvocationReservation(a, b OAuthInvocationReservation) bool {
	return a.ReservationID == b.ReservationID && a.DelegationID == b.DelegationID && a.RequestID == b.RequestID &&
		a.GatewayCode == b.GatewayCode && a.KeyID == b.KeyID && a.EntryGatewayNodeID == b.EntryGatewayNodeID &&
		a.EntryGroupID == b.EntryGroupID && a.RequestedModel == b.RequestedModel && a.Endpoint == b.Endpoint &&
		a.ExecutionDeadlineMillis == b.ExecutionDeadlineMillis
}

func oauthInvocationBindRequest(reservation OAuthInvocationReservation, fundingID string) OAuthInvocationBindRequest {
	return OAuthInvocationBindRequest{ReservationID: reservation.ReservationID, RequestID: reservation.RequestID,
		GatewayCode: reservation.GatewayCode, FundingAuthorizationID: fundingID,
		OperationID: reservation.RequestID + ":oauth-bind", ExpectedVersion: reservation.Version}
}

func oauthInvocationFundingError(err error, response *FundingAuthorizationResponse) error {
	if err != nil {
		var httpErr *ControlPlaneHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode >= 400 && httpErr.StatusCode < 500 && httpErr.StatusCode != 408 {
			return errors.Join(ErrOAuthInvocationDenied, err)
		}
		return errors.Join(ErrOAuthInvocationUnknown, err)
	}
	if response == nil {
		return ErrOAuthInvocationUnknown
	}
	if !response.OK {
		if strings.Contains(response.Error, "insufficient") || strings.Contains(response.Error, "budget") {
			return errors.Join(ErrRequestQuotaInsufficient, errors.New(response.Error))
		}
		return errors.Join(ErrOAuthInvocationDenied, errors.New(response.Error))
	}
	if response.Authorization == nil {
		return ErrOAuthInvocationUnknown
	}
	return nil
}

func MarkOAuthInvocationExecutionStarted(ctx context.Context) error {
	request, ok := OAuthInvocationRequestFromContext(ctx)
	if !ok {
		return nil
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if !request.ready || request.snapshot.ExecutionStarted || request.snapshot.Closed || request.snapshot.OutcomeUnknown || request.blocked || request.snapshot.Reservation == nil || request.snapshot.Funding == nil || request.snapshot.Reservation.Status != "active" {
		return ErrOAuthInvocationConflict
	}
	if min(request.snapshot.Reservation.ExecutionDeadlineMillis, request.snapshot.Funding.ExecutionDeadlineMillis) <= time.Now().UnixMilli() {
		return ErrOAuthInvocationExpired
	}
	// Mark the live uncertainty before attempting SQL: a driver error can follow
	// a committed fence. A later recovery/status call must not turn it into zero
	// cost. Only the SQL consumer can prove no dispatch by owning the row and fence.
	request.snapshot.DispatchUncertain = true
	if err := request.checkpointLocked(ctx, true); err != nil {
		return err
	}
	request.snapshot.ExecutionStarted = true
	request.snapshot.DispatchUncertain = false
	return nil
}
